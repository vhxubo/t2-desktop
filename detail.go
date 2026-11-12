package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Read endpoints used by the in-app detail page:
//
//	GET /v1/topics/:short_id                  postscripts / vote / reactions
//	GET /v1/topics/:short_id/replies         items + pinned, ?flat=1 supported
//	GET /v1/emojis                          :code: -> img_url mapping
//
// Paths, query names and field names are copied verbatim from api-read.md.
const replyLimit = 30

// topicPath escapes the short_id so a malformed list row can never turn
// into a different endpoint (or a path outside /v1/topics).
func topicPath(shortID string) string {
	return "/v1/topics/" + url.PathEscape(shortID)
}

func repliesPath(shortID string, page int, flat bool) string {
	if page < 1 {
		page = 1
	}
	p := fmt.Sprintf("%s/replies?page=%d&limit=%d", topicPath(shortID), page, replyLimit)
	if flat {
		p += "&flat=1"
	}
	return p
}

func (c *Client) apiURL(pathQuery string) string {
	return strings.TrimSuffix(c.base(), "/") + pathQuery
}

// topicDetail only keeps the stable fields from api-read.md. Every decoder
// below ignores unknown fields, as the v1 compatibility promise requires.
type topicDetail struct {
	ShortID, Title, Content, Type string
	Author                        string
	AuthorID                      string // 作者 uuid，关注 / 取关路径需要
	Avatar                        string // 作者头像 URL，正文底部左下角展示
	Badges                        []badgeInfo
	AuthorLevel                   int
	// Following / FollowingKnown 是作者对象里可选的 is_following。线上详情
	// 目前不带这个字段，关注状态改由 GET /v1/users/:userName 补齐；这里
	// 仍然解析，字段一旦下发就不必多发一次请求。
	Following                bool
	FollowingKnown           bool
	NodeName, NodeSlug       string
	CreatedAt, UpdatedAt     string
	Views, CommentCount      int
	IsPinned, CommentsClosed bool
	Tags                     []string
	Postscripts              []postscript
	Vote                     *voteBlock
	// Reactions 是帖子收到的表情表态汇总；Rewards 是打赏合计与打赏者。
	Reactions []reactionStat
	Rewards   rewardStat
	// Emojis 是这篇帖子自己的短码 → 图片映射；社区自定义图标会出现在
	// 这里，值可能是 /emojis/xxx.png 这样的相对地址。
	Emojis map[string]string
	URL    string // 原文链接，展示内容时必须保留
}

// reactionStat 是一种表情的聚合表态：谁、多少人、我是否也选了它。
// 线上字段名是 reactions_summary，my_reactions 单独给出我选过的表情。
type reactionStat struct {
	Emoji string
	Count int
	Mine  bool
}

// rewardStat 是一条内容收到的打赏汇总：金额合计、打赏次数（按记录数）与
// 打赏者昵称；同一个人多次打赏只记一次昵称。
type rewardStat struct {
	Total int
	Count int
	Users []string
}

type postscript struct {
	Content, CreatedAt string
	Emojis             map[string]string
}

// badgeInfo 是用户佩戴的社区徽章；icon_url 是 22×15 的小图，直接用于
// 昵称旁。佩戴「忽冷忽热」时服务端还会带 weather，网页会用天气图标和
// 温度代替徽章图标。
type badgeInfo struct {
	Name, IconURL string
	Weather       *weatherInfo
}

type weatherInfo struct {
	SymbolCode  string
	Temperature float64
}

type voteBlock struct {
	Title     string
	Finished  bool
	Options   []voteOption
	MaxChoice int
}

type voteOption struct {
	Text  string
	Votes int
}

// replyItem is one floor. Children of the floor view are flattened for
// display with Depth, so the UI needs no recursive layout.
type replyItem struct {
	ID, Content, Author, CreatedAt string
	// Avatar 是评论作者的头像地址；统一用户标识据此在昵称旁画头像。
	Avatar                         string
	Floor, FlatFloor, LocatedFloor int
	ParentID                       string // 被回复评论的 uuid，点「回复 N 楼」时用来定位
	ParentFloor                    int
	ParentSummary                  string
	Reactions                      []reactionStat
	Rewards                        rewardStat
	Emojis                         map[string]string
	Badges                         []badgeInfo
	Deleted, Pinned                bool
	Depth                          int
	Children                       []replyItem
}

type replyPage struct {
	Items, Pinned                  []replyItem
	Total, Page, Limit, TotalPages int
}

type emojiInfo struct {
	Code, Name, ImgURL string
}

func (c *Client) fetchTopic(ctx context.Context, shortID string) (topicDetail, error) {
	return withAuthRetry(c, ctx, func(token string) (topicDetail, error) {
		d, err := c.getJSON(ctx, c.apiURL(topicPath(shortID)), token)
		if err != nil {
			return topicDetail{}, err
		}
		return decodeTopic(d, shortID)
	})
}

func (c *Client) fetchReplies(ctx context.Context, shortID string, page int, flat bool) (replyPage, error) {
	return withAuthRetry(c, ctx, func(token string) (replyPage, error) {
		d, err := c.getJSON(ctx, c.apiURL(repliesPath(shortID, page, flat)), token)
		if err != nil {
			return replyPage{}, err
		}
		return decodeReplies(d), nil
	})
}

func (c *Client) fetchEmojis(ctx context.Context) (map[string]emojiInfo, error) {
	return withAuthRetry(c, ctx, func(token string) (map[string]emojiInfo, error) {
		d, err := c.getJSON(ctx, c.apiURL("/v1/emojis"), token)
		if err != nil {
			return nil, err
		}
		return decodeEmojis(d), nil
	})
}

func decodeTopic(d json.RawMessage, requested string) (topicDetail, error) {
	o := asObject(d)
	if o == nil {
		// A hidden / deleted post can come back as 200 + {d:null}.
		return topicDetail{}, &apiError{
			Status: 404, Code: "not_found", Message: "帖子不存在或不可见",
		}
	}
	shortID := o.str("short_id")
	if shortID == "" {
		shortID = requested
	}
	t := topicDetail{
		ShortID:        shortID,
		Title:          o.str("title"),
		Content:        o.str("content"), // raw markdown, rendered by the UI
		Type:           o.str("type"),
		Author:         "匿名", // 匿名帖的 author 为 null，必须判空
		CreatedAt:      o.str("created_at"),
		UpdatedAt:      o.str("updated_at"),
		Views:          o.num("views"),
		CommentCount:   o.num("comment_count"),
		IsPinned:       o.flag("is_pinned"),
		CommentsClosed: o.flag("comments_closed"),
		URL:            topicURL(shortID, ""), // 没有 node 时也要有原文链接
	}
	if a := o.sub("author"); a != nil {
		if name := a.str("username"); name != "" {
			t.Author = name
		}
		t.AuthorID = a.str("id")
		t.AuthorLevel = a.num("level")
		t.Avatar = authorAvatar(a)
		t.Badges = decodeBadges(a)
		if _, ok := a["is_following"]; ok {
			t.FollowingKnown = true
			t.Following = a.flag("is_following", "followed")
		}
	}
	if n := o.sub("node"); n != nil {
		t.NodeName = n.str("name")
		t.NodeSlug = n.str("slug")
		t.URL = topicURL(shortID, t.NodeSlug)
	}
	for _, raw := range rawArray(o["tags"]) {
		tag := asObject(raw)
		if tag == nil {
			continue
		}
		if name := tag.str("name"); name != "" {
			t.Tags = append(t.Tags, name)
		}
	}
	t.Emojis = decodeEmojiURLs(o["emojis"])
	t.Reactions = decodeReactionStats(o["reactions_summary"], o["reactions"], o["my_reactions"])
	t.Rewards = decodeRewards(o["rewards"])
	t.Postscripts = decodePostscripts(o["postscripts"])
	// 线上 API 把投票放在 d.vote（顶层）；早期文档示例写在 d.extra.vote。
	// 两处都认，避免投票块在真实帖子上去世。
	if vote := o.sub("vote"); vote != nil {
		t.Vote = decodeVote(vote)
	} else if extra := o.sub("extra"); extra != nil {
		if vote := extra.sub("vote"); vote != nil {
			t.Vote = decodeVote(vote)
		}
	}
	return t, nil
}

func decodePostscripts(raw json.RawMessage) []postscript {
	list := rawArray(raw)
	if len(list) == 0 {
		if o := asObject(raw); o != nil {
			for _, k := range []string{"items", "list", "postscripts"} {
				if list = rawArray(o[k]); len(list) > 0 {
					break
				}
			}
		}
	}
	var out []postscript
	for _, item := range list {
		o := asObject(item)
		if o == nil {
			continue
		}
		content := o.str("content", "text", "body")
		if content == "" {
			continue
		}
		out = append(out, postscript{
			Content:   content,
			CreatedAt: o.str("created_at"),
			Emojis:    decodeEmojiURLs(o["emojis"]),
		})
	}
	return out
}

func decodeVote(o object) *voteBlock {
	v := &voteBlock{
		Title:     o.str("title"),
		Finished:  o.flag("voting_finished"),
		MaxChoice: o.num("max_choices"),
	}
	for _, raw := range rawArray(o["options"]) {
		opt := asObject(raw)
		if opt == nil {
			continue
		}
		v.Options = append(v.Options, voteOption{
			Text:  opt.str("text"),
			Votes: opt.num("vote_count"),
		})
	}
	if v.Title == "" && len(v.Options) == 0 {
		return nil
	}
	return v
}

func decodeReplies(d json.RawMessage) replyPage {
	var out replyPage
	o := asObject(d)
	if o == nil {
		return out // 帖子不可见时 d 为 null
	}
	out.Total = rawInt(o["total"])
	out.Page = rawInt(o["page"])
	out.Limit = rawInt(o["limit"])
	out.TotalPages = rawInt(o["total_pages"])
	for _, raw := range rawArray(o["items"]) {
		out.Items = append(out.Items, decodeReply(raw, 0))
	}
	// 线上 API 用 pinned_comments；pinned 是旧文档 / 示例里的名字。
	pinned := rawArray(o["pinned_comments"])
	if len(pinned) == 0 {
		pinned = rawArray(o["pinned"])
	}
	for _, raw := range pinned {
		r := decodeReply(raw, 0)
		r.Pinned = true
		out.Pinned = append(out.Pinned, r)
	}
	if out.Page == 0 {
		out.Page = 1
	}
	if out.Limit == 0 {
		out.Limit = replyLimit
	}
	return out
}

func decodeReply(raw json.RawMessage, depth int) replyItem {
	o := asObject(raw)
	if o == nil {
		return replyItem{}
	}
	r := replyItem{
		ID:           o.str("id"),
		Content:      o.str("content"), // raw markdown, rendered by the UI
		Author:       "匿名",
		CreatedAt:    o.str("created_at"),
		Floor:        o.num("floor"),
		FlatFloor:    o.num("flat_floor"),
		LocatedFloor: o.num("located_floor"),
		Deleted:      o.flag("is_deleted"),
		Emojis:       decodeEmojiURLs(o["emojis"]),
		Depth:        depth,
	}
	if a := o.sub("author"); a != nil {
		if name := a.str("username"); name != "" {
			r.Author = name
		}
		r.Avatar = authorAvatar(a)
		r.Badges = decodeBadges(a)
	}
	if p := o.sub("parent"); p != nil {
		r.ParentID = p.str("id")
		r.ParentFloor = p.num("floor")
		// 「回复 N 楼：…」是父评论的纯文本预览，与列表行的评论摘要同一套
		// 规则去掉 Markdown 标记，避免引用里冒出 ** / > 之类符号。
		r.ParentSummary = truncate(commentExcerpt(p.str("content")), 60)
	}
	// 线上 API 的字段是 reactions_summary；reactions 是旧文档 / 示例里的名字。
	r.Reactions = decodeReactionStats(o["reactions_summary"], o["reactions"], o["my_reactions"])
	r.Rewards = decodeRewards(o["rewards"])
	if depth < 8 { // guard against pathological nesting
		for _, child := range rawArray(o["children"]) {
			r.Children = append(r.Children, decodeReply(child, depth+1))
		}
	}
	return r
}

// decodeBadges reads a user object's equipped_badges. A badge may carry
// weather info: the web shows a weather glyph and the temperature in place
// of the badge icon then.
func decodeBadges(a object) []badgeInfo {
	var out []badgeInfo
	for _, raw := range rawArray(a["equipped_badges"]) {
		item := asObject(raw)
		if item == nil {
			continue
		}
		badge := item.sub("badge")
		if badge == nil {
			continue
		}
		b := badgeInfo{
			Name:    badge.str("name"),
			IconURL: badge.str("icon_url", "icon_url_large"),
		}
		if w := item.sub("weather"); w != nil {
			wi := &weatherInfo{SymbolCode: w.str("symbol_code")}
			if rawTemp, ok := w["temperature"]; ok {
				_ = json.Unmarshal(rawTemp, &wi.Temperature)
			}
			if wi.SymbolCode != "" {
				b.Weather = wi
			}
		}
		if b.IconURL == "" && b.Weather == nil {
			continue
		}
		out = append(out, b)
	}
	return out
}

func weatherIconURL(symbol string) string {
	return "https://r2.2libra.com/weather/svg/" + url.PathEscape(symbol) + ".svg"
}

// decodeReactionStats 汇总帖子 / 评论的表情表态。summary 是现在的字段名
// （reactions_summary），legacy 是旧文档示例里的 reactions；两者都认。
// mineRaw 是 my_reactions：当前登录用户选过的表情，匿名请求里为空数组。
// 单条记录里的 mine 布尔值也认，谁先给出都算「我」已表态。
func decodeReactionStats(summary, legacy, mineRaw json.RawMessage) []reactionStat {
	raw := summary
	if len(rawArray(raw)) == 0 {
		raw = legacy
	}
	mine := make(map[string]bool)
	var mineOrder []string
	addMine := func(emoji string) {
		if emoji == "" || mine[emoji] {
			return
		}
		mine[emoji] = true
		mineOrder = append(mineOrder, emoji)
	}
	for _, item := range rawArray(mineRaw) {
		if o := asObject(item); o != nil {
			addMine(o.str("emoji"))
			continue
		}
		var emoji string
		if json.Unmarshal(item, &emoji) == nil {
			addMine(emoji)
		}
	}
	var out []reactionStat
	seen := make(map[string]bool)
	for _, item := range rawArray(raw) {
		o := asObject(item)
		if o == nil {
			continue
		}
		emoji := o.str("emoji")
		if emoji == "" {
			continue
		}
		seen[emoji] = true
		out = append(out, reactionStat{
			Emoji: emoji,
			Count: o.num("count"),
			Mine:  mine[emoji] || o.flag("mine"),
		})
		if len(out) == 6 {
			return out
		}
	}
	// 自己表过、但聚合里暂时没有（例如刚表态还没回填）的表情也要留着，
	// 这样「我」的高亮不会因为 summary 缺一项就消失。
	for _, emoji := range mineOrder {
		if seen[emoji] {
			continue
		}
		out = append(out, reactionStat{Emoji: emoji, Mine: true})
		if len(out) == 6 {
			break
		}
	}
	return out
}

// decodeRewards 把 rewards 数组折成「合计 / 次数 / 打赏者」。只统计有金额
// 的记录；同一个人的多次打赏不会重复写昵称，但次数按记录累计。
func decodeRewards(raw json.RawMessage) rewardStat {
	var out rewardStat
	seen := make(map[string]bool)
	for _, item := range rawArray(raw) {
		o := asObject(item)
		if o == nil {
			continue
		}
		amount := o.num("amount")
		if amount <= 0 {
			continue
		}
		out.Total += amount
		out.Count++
		if u := o.sub("user"); u != nil {
			if name := authorName(u); name != "" && !seen[name] {
				seen[name] = true
				out.Users = append(out.Users, name)
			}
		}
	}
	return out
}

// replyPermalink 拼一条评论在站点上的永久链接。楼层视图按 floor 分页，
// 嵌套回复没有 floor，改用 /post-flat/ 按 flat_floor 分页；页码与站点
// 每页条数（siteCommentPageSize）一致，站点才能定位到那条评论。
func replyPermalink(topic *topicDetail, fallback string, r replyItem) string {
	if r.ID == "" {
		return ""
	}
	base := fallback
	if topic != nil && topic.URL != "" {
		base = topic.URL
	}
	if base == "" {
		return ""
	}
	if u, err := url.Parse(base); err == nil {
		u.RawQuery = ""
		u.Fragment = ""
		base = u.String()
	}
	nested := r.Floor == 0
	if nested {
		base = strings.Replace(base, "/post/", "/post-flat/", 1)
	}
	floor := r.Floor
	if floor == 0 {
		floor = r.FlatFloor
	}
	if floor == 0 {
		floor = r.LocatedFloor
	}
	q := url.Values{}
	q.Set("commentId", r.ID)
	if p := (floor + siteCommentPageSize - 1) / siteCommentPageSize; p > 1 {
		q.Set("p", fmt.Sprintf("%d", p))
	}
	return base + "?" + q.Encode()
}

// replyIndexByID 返回评论在已显示列表里的行号，找不到时返回 -1。
func replyIndexByID(list []replyItem, id string) int {
	if id == "" {
		return -1
	}
	for i := range list {
		if list[i].ID == id {
			return i
		}
	}
	return -1
}

// insertReply 把刚发布成功的评论放进当前列表，返回它落在的行号。顶层评论
// 与扁平时间序都排在已经显示出来的评论之后；楼层视图里的楼中楼插到父评论
// 的整棵子树后面，而不是跳到列表最顶端。更后面的页仍会按楼层顺序接上。
func insertReply(list []replyItem, created replyItem, parentID string, flat bool) ([]replyItem, int) {
	if flat || parentID == "" {
		return append(list, created), len(list)
	}
	parent := -1
	for i := range list {
		if list[i].ID == parentID {
			parent = i
			break
		}
	}
	if parent < 0 {
		// 父评论不在已加载的页里：至少不要丢掉刚发的评论。
		return append(list, created), len(list)
	}
	created.Depth = list[parent].Depth + 1
	end := parent + 1
	for end < len(list) && list[end].Depth > list[parent].Depth {
		end++
	}
	list = append(list, replyItem{})
	copy(list[end+1:], list[end:])
	list[end] = created
	return list, end
}

// flattenReplies shows pinned floors first, then top-level floors followed
// by their children (the floor view). flat=1 responses have no children.
func flattenReplies(p replyPage) []replyItem {
	out := make([]replyItem, 0, len(p.Items)+len(p.Pinned))
	var walk func([]replyItem)
	walk = func(list []replyItem) {
		for _, r := range list {
			shown := r
			shown.Children = nil
			out = append(out, shown)
			walk(r.Children)
		}
	}
	walk(p.Pinned)
	walk(p.Items)
	return out
}

// createReply posts a new comment, optionally nested under parentID (the id
// of the comment being replied to), using the write:comment scope. It goes
// through withAuthRetry so a 401 triggers one refresh + retry like every read.
// A fresh Idempotency-Key is minted per call: retrying a timed-out request is
// the caller's job and must reuse the same key, while each new submit gets a
// new one (index.md: the key identifies one logical creation).
func (c *Client) createReply(ctx context.Context, shortID, content, parentID string) (replyItem, error) {
	return withAuthRetry(c, ctx, func(token string) (replyItem, error) {
		payload := map[string]any{"content": content}
		if parentID != "" {
			payload["parentId"] = parentID
		}
		d, err := c.postJSON(ctx, c.apiURL(topicPath(shortID)+"/replies"),
			token, randomURLSafe(24), payload)
		if err != nil {
			return replyItem{}, err
		}
		return decodeReply(d, 0), nil
	})
}

func decodeEmojis(d json.RawMessage) map[string]emojiInfo {
	out := make(map[string]emojiInfo)
	list := rawArray(d)
	if len(list) == 0 {
		if o := asObject(d); o != nil {
			for _, k := range []string{"emojis", "items", "list"} {
				if list = rawArray(o[k]); len(list) > 0 {
					break
				}
			}
		}
	}
	for _, raw := range list {
		o := asObject(raw)
		if o == nil {
			continue
		}
		e := emojiInfo{
			Code:   o.str("code"),
			Name:   o.str("name"),
			ImgURL: o.str("img_url"),
		}
		if e.Code != "" && e.ImgURL != "" {
			out[e.Code] = e
		}
	}
	return out
}

// decodeEmojiURLs reads a post / reply's own short code → image map. The
// server may send relative paths ("/emojis/x.png?size=medium"); the web
// resolves them against the media origin.
func decodeEmojiURLs(raw json.RawMessage) map[string]string {
	o := asObject(raw)
	if o == nil {
		return nil
	}
	var out map[string]string
	for code, v := range o {
		if code == "" {
			continue
		}
		var u string
		if json.Unmarshal(v, &u) != nil || u == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[code] = resolveEmojiURL(u)
	}
	return out
}

func resolveEmojiURL(u string) string {
	switch {
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"):
		return u
	case strings.HasPrefix(u, "//"):
		return "https:" + u
	case strings.HasPrefix(u, "/"):
		return "https://r2.2libra.com" + u
	}
	return u
}

// mergeEmojiMaps overlays a post's / reply's own icon map on the global
// one. Codes the global table lacks (community icons) get their own entry;
// known codes keep their human name but use the local image address.
func mergeEmojiMaps(base map[string]emojiInfo, extra ...map[string]string) map[string]emojiInfo {
	out := make(map[string]emojiInfo, len(base)+8)
	for code, e := range base {
		out[code] = e
	}
	changed := false
	for _, m := range extra {
		for code, u := range m {
			if code == "" || u == "" {
				continue
			}
			e, ok := out[code]
			if !ok {
				e = emojiInfo{Code: code, Name: strings.Trim(code, ":")}
			}
			e.ImgURL = resolveEmojiURL(u)
			out[code] = e
			changed = true
		}
	}
	if !changed {
		return base
	}
	return out
}

// rawArray only accepts a JSON array; a null / object / string yields nil.
func rawArray(raw json.RawMessage) []json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var out []json.RawMessage
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// replaceEmoji renders :code: short codes through the /v1/emojis map.
// With markdown=true it emits an image; plain text gets a readable [name].
// Unmapped codes are left verbatim: without the table that is all we can
// honestly show (api-read.md 表情).
func replaceEmoji(src string, emojis map[string]emojiInfo, markdown bool) string {
	if src == "" || len(emojis) == 0 {
		return src
	}
	return shortCode.ReplaceAllStringFunc(src, func(code string) string {
		e, ok := emojis[code]
		if !ok {
			return code
		}
		name := e.Name
		if name == "" {
			name = strings.Trim(code, ":")
		}
		name = strings.NewReplacer("[", "", "]", "", "\n", " ").Replace(name)
		if markdown && strings.HasPrefix(e.ImgURL, "https://") &&
			!strings.ContainsAny(e.ImgURL, "()[] \t\r\n") {
			return "![" + name + "](" + e.ImgURL + ")"
		}
		return "[" + name + "]"
	})
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
