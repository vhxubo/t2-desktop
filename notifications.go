package main

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// 通知（消息中心）的数据层，对齐参考扩展的 lib/notifications.ts 与
// lib/notification-view.ts：
//
//	GET  /v1/me/notifications              消息列表（列表字段是 list）
//	GET  /v1/me/notifications/unread-count 未读数
//	POST /v1/me/notifications/read         标记已读：不带 body = 全部；{"id":[...]} = 按条
//	GET  /v1/me/notifications/stream       SSE 长连接（见 stream.go）
//
// 消息 payload 的结构没有在文档里逐项承诺，这里按「认得的字段用于展示、
// 不认识的一律忽略」处理，未知消息退回到「一条新消息」。
//
// 按条已读用的是开放 API 的兼容增强，不再借用站点自己的
// POST /notifications/mark-as-read：那个接口不在开放 API 的 scope 里，
// 拿 reader 令牌去调它等于绕过了授权约定。

const (
	notificationsPath      = "/v1/me/notifications"
	notificationUnreadPath = "/v1/me/notifications/unread-count"
	notificationStreamPath = "/v1/me/notifications/stream"
	notificationReadPath   = "/v1/me/notifications/read"

	notificationLimit = 50

	// notificationReadBatchMax 是「按条已读」单次请求的 id 上限（服务端约定
	// 最多 100 条），超过就分片；空 id 列表绝不发请求。
	notificationReadBatchMax = 100
)

// 消息分段筛选：直接复用接口已有的 type / unread_only 参数，不在客户端
// 对已加载的一页做假筛选。
const (
	notificationFilterAll = iota
	notificationFilterUnread
	notificationFilterReply
	notificationFilterMention
	notificationFilterReaction
	notificationFilterReward
)

type notificationFilterOption struct {
	Label      string
	Type       string // 传给接口的 type；空表示不按类型过滤
	UnreadOnly bool
	Hint       string
	EmptyTitle string
	EmptyHint  string
}

var notificationFilters = []notificationFilterOption{
	{Label: "全部", Hint: "显示全部消息", EmptyTitle: "没有消息", EmptyHint: "收到回复、提及、表态、打赏或系统消息时，会出现在这里。"},
	{Label: "未读", UnreadOnly: true, Hint: "只看未读消息", EmptyTitle: "没有未读消息", EmptyHint: "所有消息都已读；切到「全部」可以回看历史。"},
	{Label: "回复", Type: "reply", Hint: "只看回复消息", EmptyTitle: "没有回复消息", EmptyHint: "这个分类暂时没有消息，切到「全部」看看其它消息。"},
	{Label: "提及", Type: "mention", Hint: "只看提及消息", EmptyTitle: "没有提及消息", EmptyHint: "这个分类暂时没有消息，切到「全部」看看其它消息。"},
	{Label: "表态", Type: "reaction", Hint: "只看表态消息", EmptyTitle: "没有表态消息", EmptyHint: "这个分类暂时没有消息，切到「全部」看看其它消息。"},
	{Label: "打赏", Type: "get_reward", Hint: "只看打赏消息", EmptyTitle: "没有打赏消息", EmptyHint: "这个分类暂时没有消息，切到「全部」看看其它消息。"},
}

func notificationFilterAt(i int) notificationFilterOption {
	if i < 0 || i >= len(notificationFilters) {
		return notificationFilters[notificationFilterAll]
	}
	return notificationFilters[i]
}

// notificationsQuery 是通知列表第 1 页的 query；页数由 endpoint 负责替换。
func notificationsQuery(filter int) string {
	f := notificationFilterAt(filter)
	q := "page=1&limit=" + strconv.Itoa(notificationLimit)
	if f.UnreadOnly {
		q += "&unread_only=true"
	}
	if f.Type != "" {
		q += "&type=" + url.QueryEscape(f.Type)
	}
	return q
}

// notification is one message in the center. Only the fields documented or
// used by the site's message center are decoded.
type notification struct {
	ID        string
	Type      string
	IsRead    bool
	CreatedAt string
	FromUser  object
	Payload   object
}

type notificationPage struct {
	Items      []notification
	Total      int
	Page       int
	Limit      int
	TotalPages int
}

func decodeNotification(raw json.RawMessage) *notification {
	return decodeNotificationObject(asObject(raw))
}

func decodeNotificationObject(o object) *notification {
	if o == nil {
		return nil
	}
	id := o.str("id")
	if id == "" {
		return nil
	}
	n := &notification{
		ID:        id,
		Type:      o.str("type"),
		IsRead:    o.flag("is_read", "read"),
		CreatedAt: o.str("created_at", "createdAt"),
		FromUser:  o.sub("from_user", "user"),
		Payload:   o.sub("payload"),
	}
	if n.Payload == nil {
		n.Payload = object{}
	}
	return n
}

// notificationItem adapts a message to the generic list row. The row itself
// is rendered by notificationRow, which reads Item.Notif.
func notificationItem(o object) Item {
	n := decodeNotificationObject(o)
	if n == nil {
		return Item{}
	}
	return Item{Key: n.ID, Notif: n}
}

func decodeNotificationsPage(d json.RawMessage, requestedPage int) notificationPage {
	out := notificationPage{Page: requestedPage, Limit: notificationLimit}
	o := asObject(d)
	if o == nil {
		return out
	}
	out.Total = rawInt(o["total"])
	out.Page = rawInt(o["page"])
	out.Limit = rawInt(o["limit"])
	out.TotalPages = rawInt(o["total_pages"])
	for _, raw := range rawArray(o["list"]) {
		if n := decodeNotification(raw); n != nil {
			out.Items = append(out.Items, *n)
		}
	}
	if out.Page <= 0 {
		out.Page = requestedPage
	}
	if out.Limit <= 0 {
		out.Limit = notificationLimit
	}
	if out.Total <= 0 {
		out.Total = len(out.Items)
	}
	if out.TotalPages <= 0 {
		out.TotalPages = 1
	}
	return out
}

// fetchNotifications loads one page of messages. It reuses endpoint() so the
// query shape is the same as the tab contract.
func (c *Client) fetchNotifications(ctx context.Context, page, filter int) (notificationPage, error) {
	return withAuthRetry(c, ctx, func(token string) (notificationPage, error) {
		s := tabSpec{Path: notificationsPath, Query: notificationsQuery(filter)}
		d, err := c.getJSON(ctx, endpoint(c.base(), s, page), token)
		if err != nil {
			return notificationPage{}, err
		}
		return decodeNotificationsPage(d, page), nil
	})
}

func (c *Client) fetchUnreadNotificationCount(ctx context.Context) (int, error) {
	return withAuthRetry(c, ctx, func(token string) (int, error) {
		d, err := c.getJSON(ctx, c.apiURL(notificationUnreadPath), token)
		if err != nil {
			return 0, err
		}
		if o := asObject(d); o != nil {
			return o.num("unread_count", "count"), nil
		}
		return rawInt(d), nil
	})
}

// notificationReadBody 是「按条已读」的请求体。字段名必须是 id：服务端会
// 静默丢弃未知字段，写成 ids 就等于什么都没传，整个请求会退化成「全部已读」，
// 而且不会报错。ids 的顺序与分片顺序由调用方决定，这里只做去重与去空。
type notificationReadBody struct {
	ID []string `json:"id"`
}

// markNotificationsRead 只把指定 id 的消息标记为已读。id 列表会先去空串、
// 去重，再按 100 条分片（分批失败即中止）；列表本来为空时直接返回，
// 绝不发出 id: []（服务端会 400）。
func (c *Client) markNotificationsRead(ctx context.Context, ids []string) error {
	for _, batch := range notificationReadBatches(ids) {
		if err := c.postNotificationRead(ctx, notificationReadBody{ID: batch}); err != nil {
			return err
		}
	}
	return nil
}

// postNotificationRead 是两种已读写请求的唯一出口：补上 API base 并复用
// 读侧的 401 单飞刷新 + 一次重试。payload 为 nil 表示不带 body，即全部已读。
func (c *Client) postNotificationRead(ctx context.Context, payload any) error {
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		_, err := c.postJSON(ctx, c.apiURL(notificationReadPath), token, "", payload)
		return struct{}{}, err
	})
	return err
}

func (c *Client) markAllNotificationsRead(ctx context.Context) error {
	// 不带 body 才是「全部已读」的旧用法，语义比传 {} 更明确。
	return c.postNotificationRead(ctx, nil)
}

// notificationReadBatches 收敛 id 列表并按上限切片。返回 nil 表示什么
// 都不用发：空数组、全是空串、全被去重掉都属于这种情况。
func notificationReadBatches(ids []string) [][]string {
	ids = cleanNotificationIDs(ids)
	var out [][]string
	for len(ids) > 0 {
		n := len(ids)
		if n > notificationReadBatchMax {
			n = notificationReadBatchMax
		}
		out = append(out, ids[:n])
		ids = ids[n:]
	}
	return out
}

// cleanNotificationIDs 去掉空串与重复 id，保持原顺序（分片有先后语义）。
func cleanNotificationIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

/* ------------------------------------------------------------------
   消息 → 展示字段
------------------------------------------------------------------ */

// notificationView is everything a row (and a native notification) needs;
// pages never have to guess payload fields themselves.
type notificationView struct {
	Kind         string
	TypeLabel    string
	Tone         string
	ActorName    string
	ActorAvatar  string
	ActorIsAlias bool
	Action       string
	TargetTitle  string
	TargetURL    string
	Excerpt      string
	Detail       string
	DetailTone   string
	Meta         []string
	Reactions    string
}

var notificationTypeLabels = map[string]string{
	"reply":          "回复",
	"reply_mention":  "回复提及",
	"mention":        "提及",
	"comment_pin":    "置顶",
	"system":         "系统",
	"pandora":        "潘多拉",
	"reward":         "打赏",
	"get_reward":     "打赏",
	"like":           "点赞",
	"reaction":       "表态",
	"follow":         "关注",
	"favorite":       "收藏",
	"level_up":       "升级",
	"content_check":  "检定",
	"badge_unlocked": "徽章",
}

func notificationTypeLabel(typ string) string {
	if typ == "" {
		return "消息"
	}
	if label, ok := notificationTypeLabels[typ]; ok {
		return label
	}
	return typ
}

func notificationKind(typ string) string {
	switch typ {
	case "reply", "reply_mention", "mention", "comment_pin", "favorite",
		"system", "pandora", "reaction", "get_reward", "level_up",
		"content_check", "badge_unlocked", "follow":
		return typ
	}
	return "unknown"
}

func notificationTone(kind string) string {
	switch kind {
	case "reply", "reply_mention", "mention", "reaction", "content_check":
		return "info"
	case "comment_pin", "follow":
		return "primary"
	case "favorite", "system", "pandora":
		return "warning"
	case "get_reward", "level_up", "badge_unlocked":
		return "success"
	}
	return "muted"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// notificationViewOf normalises one raw message into display fields.
func notificationViewOf(n *notification) notificationView {
	payload := n.Payload
	if payload == nil {
		payload = object{}
	}
	kind := notificationKind(n.Type)
	v := notificationView{
		Kind:      kind,
		TypeLabel: notificationTypeLabel(n.Type),
		Tone:      notificationTone(kind),
	}

	alias := payload.str("alias_name")
	follower := payload.str("followerUsername")
	actor := firstNonEmpty(alias, follower, authorName(n.FromUser))
	if kind == "reaction" {
		actor = "" // 表态是聚合事件，from_user 不是唯一操作者
	}
	v.ActorName = actor
	v.ActorIsAlias = kind != "reaction" && alias != ""
	switch {
	case v.ActorIsAlias:
		v.ActorAvatar = payload.str("alias_avatar_url")
	case kind != "reaction":
		v.ActorAvatar = authorAvatar(n.FromUser)
	}

	v.Action = notificationAction(kind, payload)
	v.TargetTitle = firstNonEmpty(
		payload.str("postTitle"), payload.str("contentTitle"), payload.str("articleTitle"))
	v.TargetURL = notificationTargetURL(n, payload, actor)
	v.Excerpt = notificationExcerpt(kind, payload)
	v.Detail, v.DetailTone = notificationDetail(kind, payload)
	v.Meta = notificationMeta(kind, payload)
	if kind == "reaction" {
		v.Reactions = notificationReactions(payload)
	}
	return v
}

func notificationAction(kind string, p object) string {
	subject := ""
	if p.has("commentLevel") {
		switch level := p.num("commentLevel"); {
		case level == 0:
			subject = "帖子"
		case level > 0:
			subject = "评论"
		}
	}
	rewardType := strings.ToLower(p.str("rewardType"))
	switch kind {
	case "reply":
		if p.str("target") == "postscript" {
			return "回复了你的附言"
		}
		if subject != "" {
			return "回复了你的" + subject
		}
		return "回复了你"
	case "reply_mention":
		return "回复并提到了你"
	case "mention":
		if subject != "" {
			return "在" + subject + "里提到了你"
		}
		return "提到了你"
	case "comment_pin":
		return "置顶了你的评论"
	case "favorite":
		return "收藏了你的帖子"
	case "system":
		return "发来一条系统消息"
	case "pandora":
		return "潘多拉事件已生效"
	case "reaction":
		switch p.str("reactionType") {
		case "post":
			return "你的帖子收到了新表态"
		case "comment":
			return "你的评论收到了新表态"
		}
		return "你的内容收到了新表态"
	case "get_reward":
		switch rewardType {
		case "post":
			return "打赏了你的帖子"
		case "comment":
			return "打赏了你的评论"
		case "rss_article", "rss":
			return "打赏了你的博客文章"
		}
		return "打赏了你"
	case "level_up":
		return "等级提升了"
	case "content_check":
		switch p.str("contentType") {
		case "post":
			return "你的帖子完成了内容检定"
		case "comment":
			return "你的评论完成了内容检定"
		}
		return "内容检定已完成"
	case "badge_unlocked":
		return "解锁了新徽章"
	case "follow":
		return "关注了你"
	}
	return "发来一条新消息"
}

func notificationDetail(kind string, p object) (string, string) {
	switch kind {
	case "system":
		errText := p.str("error")
		if errText != "" {
			return errText, "danger"
		}
		return p.str("message"), "muted"
	case "pandora":
		if msg := p.str("message"); msg != "" {
			return msg, "muted"
		}
		eventName := p.str("event_name")
		eventTitle := p.str("event_title")
		switch {
		case eventName != "" && eventTitle != "":
			return eventName + "：" + eventTitle, "muted"
		case eventTitle != "":
			return eventTitle, "muted"
		case eventName != "":
			return eventName, "muted"
		}
		return "潘多拉事件已生效", "muted"
	case "level_up":
		return p.str("message"), "success"
	case "badge_unlocked":
		if badge := p.str("badge_name"); badge != "" {
			return "解锁徽章「" + badge + "」", "success"
		}
		return "", "success"
	case "content_check":
		return firstNonEmpty(p.str("message"), "内容检定已完成"), "warning"
	case "get_reward":
		if p.has("amount") {
			amount := p.num("amount")
			prefix := ""
			if amount > 0 {
				prefix = "+"
			}
			return prefix + strconv.Itoa(amount) + " 金币", "success"
		}
		return "", "success"
	case "comment_pin":
		if p.has("coins") {
			return "获得 " + strconv.Itoa(p.num("coins")) + " 金币", "success"
		}
		return "", "success"
	}
	return "", "muted"
}

func notificationMeta(kind string, p object) []string {
	if kind != "system" {
		return nil
	}
	var meta []string
	if feed := p.str("feedUrl"); feed != "" {
		meta = append(meta, "RSS："+feed)
	}
	if p.has("consecutiveFailures") {
		meta = append(meta, "连续失败 "+strconv.Itoa(p.num("consecutiveFailures"))+" 次")
	}
	return meta
}

func notificationExcerpt(kind string, p object) string {
	switch kind {
	case "reply", "reply_mention", "mention", "comment_pin", "reaction", "content_check":
		return cleanText(p.str("commentText"))
	case "get_reward":
		reward := strings.ToLower(p.str("rewardType"))
		if reward != "post" && reward != "rss_article" && reward != "rss" {
			return cleanText(p.str("commentText"))
		}
	}
	return ""
}

func normalizeSiteHref(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	lower := strings.ToLower(v)
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return v
	case strings.HasPrefix(v, "//"):
		return "https:" + v
	case strings.HasPrefix(v, "/"):
		return siteBaseURL + v
	}
	v = strings.TrimPrefix(v, "./")
	v = strings.TrimPrefix(v, "/")
	if v == "" {
		return siteBaseURL
	}
	return siteBaseURL + "/" + v
}

func withCommentParam(href, commentID string) string {
	if commentID == "" || strings.Contains(href, "commentId=") {
		return href
	}
	sep := "?"
	if strings.Contains(href, "?") {
		sep = "&"
	}
	return href + sep + "commentId=" + url.QueryEscape(commentID)
}

// notificationTargetURL 是这条消息最值得打开的目标：优先用服务端给出的
// path（RSS、投票等都在里面），其次是帖子 short_id；两者都没有时回到站点
// 的消息中心。
func notificationTargetURL(n *notification, p object, actor string) string {
	path := p.str("path", "articleLink")
	relatedID := p.str("related_id")
	relatedIsComment := (n.Type == "reaction" && p.str("reactionType") == "comment") ||
		(n.Type == "content_check" && p.str("contentType") == "comment")
	commentID := p.str("commentId")
	if commentID == "" && relatedIsComment {
		commentID = relatedID
	}

	if n.Type == "follow" {
		follower := firstNonEmpty(p.str("followerUsername"), actor)
		if follower != "" {
			return siteBaseURL + "/user/" + url.PathEscape(follower) + "/about"
		}
	}

	if path != "" {
		if href := normalizeSiteHref(path); href != "" {
			return withCommentParam(href, commentID)
		}
	}

	if short := p.str("postShortId"); short != "" {
		slug := p.str("postNodeSlug")
		if slug == "" {
			slug = "_"
		}
		return withCommentParam(topicURL(short, slug), commentID)
	}

	return siteBaseURL + "/notifications"
}

// notificationReactions summarises payload.reactions (emoji → count) and
// attaches the usernames found in payload.relations.
func notificationReactions(p object) string {
	counts := map[string]int{}
	users := map[string][]string{}
	var order []string

	addOrder := func(emoji string) {
		if _, ok := counts[emoji]; !ok {
			counts[emoji] = 0
			order = append(order, emoji)
		}
	}
	for _, raw := range rawArray(p["relations"]) {
		r := asObject(raw)
		if r == nil {
			continue
		}
		emoji := r.str("emoji")
		if emoji == "" {
			continue
		}
		addOrder(emoji)
		counts[emoji]++
		if u := r.str("username"); u != "" && !containsString(users[emoji], u) {
			users[emoji] = append(users[emoji], u)
		}
	}
	if ro := p.sub("reactions"); ro != nil {
		for emoji, raw := range ro {
			n := rawInt(raw)
			if emoji == "" || n <= 0 {
				continue
			}
			if _, ok := counts[emoji]; !ok {
				order = append(order, emoji)
			}
			counts[emoji] = n
		}
	}

	sort.Strings(order)
	var parts []string
	for _, emoji := range order {
		count := counts[emoji]
		if count <= 0 {
			continue
		}
		part := emoji + strconv.Itoa(count)
		if names := users[emoji]; len(names) > 0 {
			shown := names
			if len(shown) > 3 {
				shown = shown[:3]
			}
			part += "（@" + strings.Join(shown, "、@") + "）"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " · ")
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// nativeNotice is what the platform actually shows. Target is where a click
// should land; the app resolves it on the UI thread, never on the platform's
// callback thread.
type nativeNotice struct {
	ID     string
	Title  string
	Body   string
	Target string
}

// notificationNative turns a message into the native notification text:
// "@谁 做了什么" as the title, then the target title and the comment body.
func notificationNative(n *notification) nativeNotice {
	v := notificationViewOf(n)
	title := v.Action
	if v.ActorName != "" {
		title = "@" + v.ActorName + " " + v.Action
	}
	var lines []string
	if v.TargetTitle != "" {
		lines = append(lines, "《"+v.TargetTitle+"》")
	}
	body := firstNonEmpty(v.Excerpt, v.Detail)
	if body != "" {
		lines = append(lines, body)
	}
	if len(lines) == 0 {
		lines = append(lines, v.TypeLabel)
	}
	return nativeNotice{
		ID:     notificationNativeID(n.ID),
		Title:  truncate(title, 80),
		Body:   truncate(strings.Join(lines, "\n"), 220),
		Target: v.TargetURL,
	}
}

func notificationNativeID(id string) string { return "t2-notification:" + id }
