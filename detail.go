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
	Avatar                        string // 作者头像 URL，正文底部左下角展示
	Badges                        []badgeInfo
	AuthorLevel                   int
	NodeName, NodeSlug            string
	CreatedAt, UpdatedAt          string
	Views, CommentCount           int
	IsPinned, CommentsClosed      bool
	Tags                          []string
	Postscripts                   []postscript
	Vote                          *voteBlock
	// Emojis 是这篇帖子自己的短码 → 图片映射；社区自定义图标会出现在
	// 这里，值可能是 /emojis/xxx.png 这样的相对地址。
	Emojis map[string]string
	URL    string // 原文链接，展示内容时必须保留
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
	Floor, FlatFloor, LocatedFloor int
	ParentFloor                    int
	ParentSummary                  string
	Reactions                      string
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
		t.AuthorLevel = a.num("level")
		t.Avatar = authorAvatar(a)
		t.Badges = decodeBadges(a)
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
	t.Postscripts = decodePostscripts(o["postscripts"])
	if extra := o.sub("extra"); extra != nil {
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
	for _, raw := range rawArray(o["pinned"]) {
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
		Content:      o.str("content"),
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
		r.Badges = decodeBadges(a)
	}
	if p := o.sub("parent"); p != nil {
		r.ParentFloor = p.num("floor")
		// 「回复 N 楼：…」是父评论的纯文本预览，与列表行的评论摘要同一套
		// 规则去掉 Markdown 标记，避免引用里冒出 ** / > 之类符号。
		r.ParentSummary = truncate(commentExcerpt(p.str("content")), 60)
	}
	r.Reactions = reactionSummary(o["reactions"])
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

func reactionSummary(raw json.RawMessage) string {
	var parts []string
	for _, item := range rawArray(raw) {
		o := asObject(item)
		if o == nil {
			continue
		}
		emoji := o.str("emoji")
		if emoji == "" {
			continue
		}
		n := o.num("count")
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s%d", emoji, n))
		} else {
			parts = append(parts, emoji)
		}
		if len(parts) == 6 {
			break
		}
	}
	return strings.Join(parts, " ")
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
