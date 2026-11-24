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

	// notificationKeywordMax 是通知关键词的长度上限（文档 1–50 字，超出是
	// 400 invalid_request）。界面先按同一上限挡掉，省一次注定失败的请求。
	notificationKeywordMax = 50

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
// keyword 是可选的内容搜索（1–50 字，服务端忽略大小写做包含匹配），为空时不发
// 这个参数，行为与以前完全一致。
func notificationsQuery(filter int, keyword string) string {
	f := notificationFilterAt(filter)
	q := "page=1&limit=" + strconv.Itoa(notificationLimit)
	if f.UnreadOnly {
		q += "&unread_only=true"
	}
	if f.Type != "" {
		q += "&type=" + url.QueryEscape(f.Type)
	}
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		q += "&keyword=" + url.QueryEscape(keyword)
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
func (c *Client) fetchNotifications(ctx context.Context, page, filter int, keyword string) (notificationPage, error) {
	return withAuthRetry(c, ctx, func(token string) (notificationPage, error) {
		s := tabSpec{Path: notificationsPath, Query: notificationsQuery(filter, keyword)}
		d, err := c.getJSON(ctx, endpoint(c.base(), s, page), token)
		if err != nil {
			return notificationPage{}, err
		}
		return decodeNotificationsPage(d, page), nil
	})
}

// notificationUnreadCounts 是 GET /me/notifications/unread-count 的全部计数。
// Total 是通知总数（标题栏铃铛），其余是各内容分区自上次读取以来的新内容数：
// 文档 2026-11-12 新增这些分项，字段名是 xxx_unread_count，watch 一项叫
// watch_content_count。api-read.md 的正文用了简写，两种都认，未知字段忽略。
type notificationUnreadCounts struct {
	Total        int
	Badge        int
	Follow       int
	Home         int
	Community    int
	WatchContent int
}

func decodeNotificationUnreadCounts(d json.RawMessage) notificationUnreadCounts {
	o := asObject(d)
	if o == nil {
		return notificationUnreadCounts{Total: rawInt(d)}
	}
	return notificationUnreadCounts{
		Total:        o.num("unread_count", "count", "unread"),
		Badge:        o.num("badge_unread_count", "badge"),
		Follow:       o.num("follow_unread_count", "follow"),
		Home:         o.num("home_unread_count", "home"),
		Community:    o.num("community_unread_count", "community"),
		WatchContent: o.num("watch_content_count", "watch"),
	}
}

// notificationUnreadCountPath 把本机的分区读取基线传给未读数接口。没有基线时
// 不带参数，交给服务端自己的口径，不伪造「现在」把计数清零。
func notificationUnreadCountPath(homeReadAt, communityReadAt string) string {
	q := url.Values{}
	if homeReadAt != "" {
		q.Set("home_last_read_at", homeReadAt)
	}
	if communityReadAt != "" {
		q.Set("community_last_read_at", communityReadAt)
	}
	if len(q) == 0 {
		return notificationUnreadPath
	}
	return notificationUnreadPath + "?" + q.Encode()
}

func (c *Client) fetchUnreadNotificationCount(ctx context.Context, homeReadAt, communityReadAt string) (notificationUnreadCounts, error) {
	return withAuthRetry(c, ctx, func(token string) (notificationUnreadCounts, error) {
		d, err := c.getJSON(ctx, c.apiURL(notificationUnreadCountPath(homeReadAt, communityReadAt)), token)
		if err != nil {
			return notificationUnreadCounts{}, err
		}
		return decodeNotificationUnreadCounts(d), nil
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
	ActorBadges  []badgeInfo
	ActorIsPro   bool // 操作者是 Pro 会员时头像带官网 Pro 图标
	ActorIsAlias bool
	Action       string
	TargetTitle  string
	TargetURL    string
	Excerpt      string
	Detail       string
	DetailTone   string
	Meta         []string
	// ReplyShortID / ReplyTarget 是「在消息中心直接回复」需要的帖子与父评论。
	// CanReply 为假时（不可回复的类型、缺帖子定位、或三级评论缺挂载点）不展示
	// 快捷回复。
	CanReply     bool
	ReplyShortID string
	ReplyTarget  replyTarget
	// Reaction 是表态消息合并后的视图：表情合到一行、点名的人去重后只写一次。
	// 非表态消息是零值（Groups 为空）。
	Reaction notificationReactionOverview
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
		// 马甲头像在 payload 里，字段名有专属拼写与通用拼写两种，都认：
		// 只认 alias_avatar_url 时，服务端换成通用名就会让头像变空。
		v.ActorAvatar = payload.str("alias_avatar_url", "aliasAvatarUrl",
			"avatar_url", "avatarUrl", "avatar")
	case kind != "reaction":
		v.ActorAvatar = authorAvatar(n.FromUser)
		// 徽章只能用消息里已经带的 equipped_badges；没有就不显示，
		// 不为补徽章单独请求用户资料。
		v.ActorBadges = decodeBadges(n.FromUser)
		v.ActorIsPro = n.FromUser.flag("is_pro", "isPro")
	}

	v.Action = notificationAction(kind, payload)
	v.TargetTitle = firstNonEmpty(
		payload.str("postTitle"), payload.str("contentTitle"), payload.str("articleTitle"))
	v.TargetURL = notificationTargetURL(n, payload, actor)
	v.Excerpt = notificationExcerpt(kind, payload)
	v.Detail, v.DetailTone = notificationDetail(kind, payload)
	v.Meta = notificationMeta(kind, payload)
	v.ReplyShortID, v.ReplyTarget, v.CanReply = notificationQuickReplyTarget(n, payload, kind)
	if kind == "reaction" {
		v.Reaction = notificationReactionOverviewOf(payload)
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
		// 表态消息的句首是表情与人名（见界面），这里只留动词，接在「谁」后面
		// 就是「张三 表态了你的评论」。
		switch p.str("reactionType") {
		case "post":
			return "表态了你的帖子"
		case "comment":
			return "表态了你的评论"
		}
		return "表态了你的内容"
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

// notificationQuickReplyTarget 判断一条消息能不能在消息中心里直接回复，并
// 给出提交所需的帖子 short_id 与父评论。只有回复 / 回复提及 / 提及这三类
// 消息带可回复的评论；表态、打赏、关注等没有可以挂载的评论。
//
// 层级按「读接口 1 起、写接口 0 起」的约定换算（见 replyTarget）：
//   - reply / reply_mention 的 commentLevel 是「被回复对象」的层级：0 = 帖子，
//     >0 = 评论；commentId 是对方新写的那条，所以提交的 level 比它多一层。
//   - mention 的 commentLevel 是「被提及的评论」本身的层级；在帖子正文里
//     提及（level 0）时没有评论可挂，直接发一条顶层评论。
//
// 三级评论必须知道它的挂载点（父评论），否则服务端会把它当成第四层拒绝。
// 消息里没有父评论 id 时宁可返回 false，让用户点开详情再回复，也不发一条
// 注定失败的请求。
func notificationQuickReplyTarget(n *notification, p object, kind string) (string, replyTarget, bool) {
	switch kind {
	case "reply", "reply_mention", "mention":
	default:
		return "", replyTarget{}, false
	}
	shortID := p.str("postShortId")
	if shortID == "" {
		// 有的消息只给 path（与通知点击目标同一份地址），从里面取 short_id。
		if it, ok := parseInAppTopicLink(normalizeSiteHref(p.str("path", "articleLink"))); ok {
			shortID = it.ShortID
		}
	}
	if shortID == "" {
		return "", replyTarget{}, false
	}

	commentID := p.str("commentId")
	level, known := 0, p.has("commentLevel")
	if known {
		level = p.num("commentLevel")
	}
	if kind == "mention" {
		if commentID == "" {
			if known && level > 0 {
				// 消息说是评论里的提及，但没有评论 id：不能在帖子下发一条顶层
				// 评论冒充分回复。
				return "", replyTarget{}, false
			}
			// 帖子里提到了你：回一条顶层评论即可。
			return shortID, replyTarget{}, true
		}
		if known && level <= 0 {
			return shortID, replyTarget{}, true
		}
		return quickReplyToComment(shortID, commentID, level, p)
	}
	// reply / reply_mention：没有评论可挂（回复了你的附言）时同样发顶层评论。
	if commentID == "" {
		if known && level > 0 {
			return "", replyTarget{}, false
		}
		return shortID, replyTarget{}, true
	}
	replyLevel := 0
	if known {
		replyLevel = min(level+1, replyMaxLevel)
	}
	return quickReplyToComment(shortID, commentID, replyLevel, p)
}

// quickReplyToComment 组装「回复某条评论」的 target。level 是提交时的写层级
// （从 0 起），0 表示未知、交给服务端推断。三级评论必须有挂载点，缺父评论
// 时返回 false。
func quickReplyToComment(shortID, commentID string, level int, p object) (string, replyTarget, bool) {
	target := replyTarget{ID: commentID, Level: level}
	if level >= replyMaxLevel {
		parent := p.str("parentCommentId", "parent_comment_id", "parentId")
		if parent == "" || parent == commentID {
			return "", replyTarget{}, false
		}
		target.ParentID = parent
	}
	return shortID, target, true
}

// notificationReactionUser 是给这条内容表态的一个人；头像地址可能为空，界面
// 退回按昵称生成的图案。
type notificationReactionUser struct {
	Name   string
	Avatar string
	Badges []badgeInfo
	IsPro  bool
}

// notificationReactionGroup 是一种表情的表态计数。
type notificationReactionGroup struct {
	Emoji string
	Count int
}

// notificationReactionNamesMax 是一条表态消息里最多点名几个人；多出来的人用
// 「等 N 人」概括，不把昵称铺满整行。
const notificationReactionNamesMax = 3

// notificationReactionOverview 是一条表态消息合并后的视图。服务端按表情分别
// 给计数和点名，同一个人可以用好几种表情；按表情分开展示就得把同一个名字写
// 好几遍（旧写法是「👍1（@张三）· 😂1（@张三）」）。这里把表情合成一组、人名
// 去重，界面才写得出一句「👍 😂 张三」。
type notificationReactionOverview struct {
	Groups []notificationReactionGroup // 每种表情一条，按表情排序
	People []notificationReactionUser  // 去重后的表态人，按首次出现排序
	Total  int                         // 各表情计数之和
	// Records 是 relations 里逐条表态的条数。Total 比它多说明服务端只给了部分
	// 点名（relations 被截断），界面据此补一句「等 N 人」，而不是假装已经点全。
	Records int
}

// notificationReactionOverviewOf 把 payload.relations（逐条表态，可能带头像）
// 与 payload.reactions（服务端计数）汇总成合并视图：两个来源都要认，前者给出
// 人名与头像，后者给出 relations 可能截断后的真实次数。
func notificationReactionOverviewOf(p object) notificationReactionOverview {
	var out notificationReactionOverview
	if p == nil {
		return out
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	var order []string
	note := func(emoji string) {
		if _, ok := counts[emoji]; !ok {
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
		note(emoji)
		counts[emoji]++
		out.Records++
		name := r.str("username")
		if name == "" || seen[name] {
			// 同一个人的第二条表态只算次数，不再写一遍名字。
			continue
		}
		seen[name] = true
		out.People = append(out.People, notificationReactionUser{
			Name:   name,
			Avatar: authorAvatar(r),
			Badges: decodeBadges(r),
			IsPro:  r.flag("is_pro", "isPro"),
		})
	}
	if ro := p.sub("reactions"); ro != nil {
		for emoji, raw := range ro {
			n := rawInt(raw)
			if emoji == "" || n <= 0 {
				continue
			}
			note(emoji)
			counts[emoji] = n
		}
	}

	sort.Strings(order)
	for _, emoji := range order {
		if c := counts[emoji]; c > 0 {
			out.Groups = append(out.Groups, notificationReactionGroup{Emoji: emoji, Count: c})
			out.Total += c
		}
	}
	return out
}

// notificationReactionOthers 返回还要用「等 N 人」概括的人数，两名以内的表态
// 用不着它：点名超过上限时按点到的真实人数算，relations 被截断（少于计数）
// 时退回用计数，都看不出还有别人时返回 0。
func notificationReactionOthers(o notificationReactionOverview, shown int) int {
	if len(o.People) > shown {
		return len(o.People)
	}
	if o.Records > 0 && o.Total > o.Records {
		return o.Total
	}
	return 0
}

// notificationReactionText 是合并视图的一行纯文本，原生通知用：表情合在一起，
// 人名用顿号相连、只写一遍，剩下的人用「等 N 人」收尾，不套括号。
func notificationReactionText(o notificationReactionOverview) string {
	names := o.People
	if len(names) > notificationReactionNamesMax {
		names = names[:notificationReactionNamesMax]
	}
	var parts []string
	for _, g := range o.Groups {
		if g.Count > 1 {
			parts = append(parts, g.Emoji+strconv.Itoa(g.Count))
			continue
		}
		parts = append(parts, g.Emoji)
	}
	text := strings.Join(parts, " ")
	if len(names) == 0 {
		return text
	}
	list := make([]string, len(names))
	for i, u := range names {
		list[i] = "@" + u.Name
	}
	who := strings.Join(list, "、")
	if others := notificationReactionOthers(o, len(names)); others > 0 {
		who += " 等 " + strconv.Itoa(others) + " 人"
	}
	if text == "" {
		return who
	}
	return text + " " + who
}

// nativeNotice is what the platform actually shows. Target is where a click
// should land; the app resolves it on the UI thread, never on the platform's
// callback thread.
//
// AccountID / Subtitle 标明这条消息属于哪个账号：多账号时每个账号一个窗口，
// 系统通知中心里的消息与本应用的其它窗口混在一起，不写账号就分不清点开后
// 该切到哪个窗口。
type nativeNotice struct {
	ID        string
	AccountID string // 消息属于哪个账号；通知 id 与 macOS 的通知分组都用它
	Subtitle  string // 「账号 @昵称」；单账号与未登录窗口为空
	Title     string
	Body      string
	Target    string
	Silent    bool // 不播提示音（由通知设置里的「提示音」开关决定）
}

// notificationNative turns a message into the native notification text:
// "@谁 做了什么" as the title, then the target title and the comment body.
// 表态消息没有单一操作者，句首换成合并后的「表情 + 人名」。
//
// accountID / accountName 是这条消息所属的账号：通知 id 带上账号段，多账号
// 时点击才认得出归属；accountName 非空时副标题写明「账号 @昵称」。
func notificationNative(n *notification, accountID, accountName string) nativeNotice {
	v := notificationViewOf(n)
	title := v.Action
	switch {
	case v.ActorName != "":
		title = "@" + v.ActorName + " " + v.Action
	case len(v.Reaction.Groups) > 0:
		if summary := notificationReactionText(v.Reaction); summary != "" {
			title = summary + " " + v.Action
		}
	case v.Kind == "reaction":
		// 没有表情也没有点名：补一个主语，标题不能只剩动词。
		title = "有人" + v.Action
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
	notice := nativeNotice{
		ID:        notificationNativeID(accountID, n.ID),
		AccountID: accountID,
		Title:     truncate(title, 80),
		Target:    v.TargetURL,
	}
	if accountName != "" {
		// 副标题在 Windows / Linux 上会并进正文最前面，昵称过长时截一下，
		// 免得把「谁做了什么」那行挤到看不见。
		notice.Subtitle = truncate("账号 @"+accountName, 40)
	}
	// 副标题占的就是正文那一段的位置（Windows 的气泡正文与副标题共用一
	// 段文字）：正文要给它让出地方，否则系统在末尾截断，切掉的正是评论正文。
	budget := noticeBodyMax
	if notice.Subtitle != "" {
		budget = max(0, budget-len([]rune(notice.Subtitle))-1)
	}
	notice.Body = truncate(strings.Join(lines, "\n"), budget)
	return notice
}

// noticeBodyMax 是系统通知正文的长度上限（字符）。
const noticeBodyMax = 220

// noticeIDPrefix 是系统通知 id 的前缀，后面接「账号 id:消息 id」。
const noticeIDPrefix = "t2-notification:"

// notificationNativeID 给系统通知一个 id。系统只把这个 id 带回来，所以一次
// 写全「哪个账号的哪条消息」：多账号时点击才认得出归属，也不会让两个账号的
// 消息在系统通知中心里互相顶掉。
func notificationNativeID(accountID, messageID string) string {
	if accountID == "" {
		return noticeIDPrefix + messageID
	}
	return noticeIDPrefix + accountID + ":" + messageID
}

// splitNotificationNoticeID 把系统通知 id 拆回账号与消息。账号 id 由
// accountIDFor 生成，只含 [a-z0-9_-]，所以第一个冒号一定是它的结尾；升级前
// 弹出的通知只有消息 id，账号段为空。
func splitNotificationNoticeID(id string) (accountID, messageID string) {
	rest, ok := strings.CutPrefix(id, noticeIDPrefix)
	if !ok {
		return "", id
	}
	accountID, messageID, found := strings.Cut(rest, ":")
	if !found {
		return "", rest
	}
	return accountID, messageID
}

// noticeMessageID 是系统通知 id 里那条消息的 id，用于点击后按条标为已读。
// 认不出通知前缀的 id 返回空串：宁可不动已读状态，也不能拿一个猜出来的 id
// 去提交（服务端会把它当成真实消息）。
func noticeMessageID(id string) string {
	if !strings.HasPrefix(id, noticeIDPrefix) {
		return ""
	}
	_, messageID := splitNotificationNoticeID(id)
	return messageID
}
