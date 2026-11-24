package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// 本文件对齐参考扩展 t2-web-extension 的「最新评论 / 动态」实现：
//
// - 评论是一行里牵着三样东西的数据：回复人、被回复的人、所属帖子。这里把
//   这些字段归一成 commentItem，列表行和通知行都只读归一后的结果。
// - 帖子列表里的评论与「最新评论」用同一套摘要规则：去掉 Markdown 标记，
//   保留纯文本，不解析 HTML。
// - 评论的永久链接要带 commentId 和页码：顶层楼层走 /post/…（按 floor 分页），
//   嵌套回复没有 floor，改用 /post-flat/…（按 flat_floor 分页），否则站点
//   定位不到那条回复。

// siteCommentPageSize 是站点网页评论区每页的条数（仍为 30）；跨页定位时
// 必须一起带上 p。接口现在允许每页 100 条（见 detail.go 的 replyLimit），
// 但永久链接是给站点网页用的，不能跟着接口的页大小改。
const siteCommentPageSize = 30

// rowExtra 是帖子 / 评论行里比 Title/Subtitle/Meta 更细的展示字段。
// 只有最新评论、动态这类带正文的端点会填它，普通榜单行保持 nil。
type rowExtra struct {
	Author        string
	AuthorAvatar  string // 作者头像地址；统一用户标识据此在昵称旁画头像
	AuthorBadges  []badgeInfo
	AuthorIsPro   bool // 作者是 Pro 会员时，头像右下角叠加 Pro 图标
	ReplyTo       string
	ReplyToAvatar string // 被回复人的头像地址（通常同父评论作者）
	ReplyToBadges []badgeInfo
	ReplyToIsPro  bool
	Excerpt       string
	Quote         string
	NodeLabel     string
	Node          nodeRef
	PostTitle     string
	When          string
	Floor         int
	ReplyCount    int
	RewardTotal   int
	Deleted       bool
	Reactions     string
}

// commentItem 是一条归一后的评论。字段对齐参考扩展的 LatestComment：
// 认得的字段用于展示，不认识的一律忽略。
type commentItem struct {
	ID            string
	Content       string
	Excerpt       string
	Author        string
	Avatar        string
	Badges        []badgeInfo
	AuthorIsPro   bool
	CreatedAt     string
	Floor         int
	FlatFloor     int
	Deleted       bool
	ReplyTo       string
	ParentID      string
	ParentAuthor  string
	ParentAvatar  string
	ParentBadges  []badgeInfo
	ParentIsPro   bool
	ParentContent string
	ParentExcerpt string
	ParentDeleted bool

	PostShortID    string
	PostTitle      string
	PostReplyCount int
	NodeSlug       string
	NodeID         string
	NodeName       string
	ParentNodeSlug string
	ParentNodeName string

	Reactions string
	Rewards   int
}

var (
	mdImage    = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdLink     = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`)
	mdCode     = regexp.MustCompile("`{1,3}([^`]*)`{1,3}")
	mdBoldStar = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdBoldLow  = regexp.MustCompile(`__([^_]+)__`)
	mdItalic   = regexp.MustCompile(`(^|\s)[*_](\S(?:[^*_]*\S)?)[*_](\s|$)`)
	mdHeading  = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	mdQuote    = regexp.MustCompile(`(?m)^>\s?`)
	mdBullet   = regexp.MustCompile(`(?m)^[-*+]\s+`)
)

// commentExcerpt 把评论正文收敛成可安全放进文本节点的单行摘要：去掉
// Markdown 链接 / 图片 / 强调标记、表情短码与多余空白，不解析 HTML。规则与
// 参考扩展 lib/comments.ts 的 commentExcerpt 一致（Go 的 regexp 不支持
// lookahead，斜体那条换成了等价的三个分组）；表情短码（`:feel_wronged:`）
// 是站点自己的扩展，扩展在渲染层处理，这里在摘要阶段就抹掉，免得列表里
// 出现一串冒号单词。
func commentExcerpt(content string) string {
	if content == "" {
		return ""
	}
	out := mdImage.ReplaceAllString(content, "$1")
	out = mdLink.ReplaceAllString(out, "$1")
	out = mdCode.ReplaceAllString(out, "$1")
	out = mdBoldStar.ReplaceAllString(out, "$1")
	out = mdBoldLow.ReplaceAllString(out, "$1")
	out = mdItalic.ReplaceAllString(out, "$1$2$3")
	out = mdHeading.ReplaceAllString(out, "")
	out = mdQuote.ReplaceAllString(out, "")
	out = mdBullet.ReplaceAllString(out, "")
	out = shortCode.ReplaceAllString(out, "")
	return strings.Join(strings.Fields(out), " ")
}

func authorName(o object) string {
	if o == nil {
		return ""
	}
	return o.str("username", "name", "nickname")
}

func authorAvatar(o object) string {
	if o == nil {
		return ""
	}
	return o.str("avatar_url", "avatarUrl", "avatar")
}

var htmlTag = regexp.MustCompile(`<[^>]+>`)

// htmlToText 把 content_html 粗略退化成纯文本：不解析、不注入，只还原几个
// 常见实体。与参考扩展 lib/me.ts 的 htmlToText 同一思路。
func htmlToText(html string) string {
	if html == "" {
		return ""
	}
	out := htmlTag.ReplaceAllString(html, " ")
	r := strings.NewReplacer(
		"&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">",
		"&quot;", `"`, "&#39;", "'",
	)
	return strings.Join(strings.Fields(r.Replace(out)), " ")
}

// commentContent 取评论正文：优先 Markdown 原文；只有 content_html 的端点在
// 这里退化成纯文本，列表行与原生通知因此不会出现空内容。
func commentContent(o object) string {
	if o == nil {
		return ""
	}
	if text := o.str("content", "content_text"); text != "" {
		return text
	}
	return htmlToText(o.str("content_html"))
}

// decodeCommentItem 兼容两种形态：
//   - 评论对象平铺，`post` 是帖子摘要；
//   - `{ comment: {...}, post: {...} }`（动态 / 书签里的包装）。
func decodeCommentItem(o object) commentItem {
	var c commentItem
	src := o.sub("comment", "reply")
	nested := src != nil
	if !nested {
		src = o
	}
	c.ID = src.str("id", "comment_id")
	if c.ID == "" {
		c.ID = o.str("id", "comment_id")
	}
	c.Content = commentContent(src)
	if c.Content == "" && nested {
		c.Content = commentContent(o)
	}
	c.CreatedAt = src.str("created_at", "createdAt")
	if c.CreatedAt == "" {
		c.CreatedAt = o.str("created_at", "createdAt")
	}
	c.Floor = src.num("floor")
	c.FlatFloor = src.num("flat_floor")
	c.Deleted = src.flag("is_deleted") || o.flag("is_deleted")

	if a := src.sub("author", "user"); a != nil {
		c.Author = authorName(a)
		c.Avatar = authorAvatar(a)
		c.Badges = decodeBadges(a)
		c.AuthorIsPro = a.flag("is_pro", "isPro")
	}
	if c.Author == "" {
		if a := o.sub("author", "user"); a != nil {
			c.Author = authorName(a)
			c.Avatar = authorAvatar(a)
			c.Badges = decodeBadges(a)
			c.AuthorIsPro = a.flag("is_pro", "isPro")
		}
	}
	// 接口没给 author（例如 /v1/me/bookmarks）时保持空，不填「匿名」：
	// 列表行遇到空作者就不画作者段，副标题里也不会多出一个发帖人。

	post := o.sub("post")
	if post == nil {
		post = src.sub("post")
	}
	if post != nil {
		c.PostShortID = post.str("short_id", "shortId")
		c.PostTitle = cleanText(post.str("title", "name"))
		c.PostReplyCount = post.num("comment_count", "comments_count")
		if n := post.sub("node"); n != nil {
			c.NodeName = n.str("name", "title")
			c.NodeSlug = n.str("slug")
			c.NodeID = n.str("id", "uuid")
			c.ParentNodeSlug = n.str("parent_slug")
			if pn := n.sub("parent", "parent_node"); pn != nil {
				c.ParentNodeName = pn.str("name", "title")
				if c.ParentNodeSlug == "" {
					c.ParentNodeSlug = pn.str("slug")
				}
			}
		}
		if pn := post.sub("parent_node", "parentNode"); pn != nil {
			c.ParentNodeName = pn.str("name", "title")
			if c.ParentNodeSlug == "" {
				c.ParentNodeSlug = pn.str("slug")
			}
		}
	}
	if c.PostShortID == "" {
		c.PostShortID = src.str("short_id", "shortId")
	}

	parent := src.sub("parent")
	if parent == nil && nested {
		parent = o.sub("parent")
	}
	if parent != nil {
		c.ParentID = parent.str("id")
		if a := parent.sub("author"); a != nil {
			c.ParentAuthor = authorName(a)
			c.ParentAvatar = authorAvatar(a)
			c.ParentBadges = decodeBadges(a)
			c.ParentIsPro = a.flag("is_pro", "isPro")
		}
		c.ParentContent = parent.str("content")
		c.ParentDeleted = parent.flag("is_deleted")
	}
	c.ReplyTo = src.str("reply_to_username", "reply_to")
	if c.ReplyTo == "" {
		c.ReplyTo = c.ParentAuthor
	}

	c.Excerpt = commentExcerpt(c.Content)
	c.ParentExcerpt = commentExcerpt(c.ParentContent)
	c.Reactions = reactionTextFrom(src)
	if c.Reactions == "" && nested {
		c.Reactions = reactionTextFrom(o)
	}
	c.Rewards = rewardsTotal(src)
	if c.Rewards == 0 && nested {
		c.Rewards = rewardsTotal(o)
	}
	return c
}

// nodeLabel 与站点一致：父节点与节点不同名时显示「父 / 子」。
func (c commentItem) nodeLabel() string {
	name := c.NodeName
	if name == "" {
		name = c.NodeSlug
	}
	if name == "" {
		name = "未分区"
	}
	if c.ParentNodeName != "" && c.ParentNodeName != name {
		return c.ParentNodeName + " / " + name
	}
	return name
}

// effectiveFloor 用于展示与定位：嵌套回复没有 floor 时退回 flat_floor。
func (c commentItem) effectiveFloor() int {
	if c.Floor > 0 {
		return c.Floor
	}
	return c.FlatFloor
}

// quote 是被回复的父评论摘要；已删除时给一句占位，避免引用区整块消失。
func (c commentItem) quote() string {
	if c.ParentID == "" && c.ParentContent == "" {
		return ""
	}
	text := c.ParentExcerpt
	if c.ParentDeleted {
		text = "原回复已删除"
	}
	if text == "" {
		return ""
	}
	if c.ParentAuthor != "" {
		return "@" + c.ParentAuthor + "：" + text
	}
	return text
}

func commentPostURL(c commentItem) string {
	if c.PostShortID == "" {
		return ""
	}
	slug := c.NodeSlug
	if slug == "" {
		slug = "_" // 站点自己的兜底：没有 slug 时用 `_` 占位，链接仍能打开帖子
	}
	return siteBaseURL + "/post/" + url.PathEscape(slug) + "/" + url.PathEscape(c.PostShortID)
}

// commentURL 是这条评论的永久链接：
//   - 顶层楼层（有 floor）走楼层视图 /post/…，按 floor 分页；
//   - 嵌套回复（floor 为 0）在楼层视图里换算不出根楼层页码，改用平铺视图
//     /post-flat/…，按 flat_floor 分页。
func commentURL(c commentItem) string {
	base := commentPostURL(c)
	if base == "" || c.ID == "" {
		return base
	}
	nested := c.Floor == 0
	if nested {
		base = strings.Replace(base, "/post/", "/post-flat/", 1)
	}
	floor := c.effectiveFloor()
	page := 0
	if floor > 0 {
		page = (floor + siteCommentPageSize - 1) / siteCommentPageSize
	}
	out := base + "?commentId=" + url.QueryEscape(c.ID)
	if page > 1 {
		out += "&p=" + strconv.Itoa(page)
	}
	return out
}

// commentItemFrom 把一条原始评论变成列表行。Title/Subtitle/Meta 继续供
// 通用行使用，Extra 则给评论专用行提供作者、被回复人、父评论引用等字段。
func commentItemFrom(o object) Item {
	c := decodeCommentItem(o)
	title := c.Excerpt
	if c.Deleted {
		title = "该评论已删除"
	}
	if title == "" {
		title = c.PostTitle
	}
	if title == "" {
		title = "（无内容）"
	}
	it := Item{
		Key:       c.ID,
		ShortID:   c.PostShortID,
		CommentID: c.ID,
		Title:     title,
		URL:       commentURL(c),
		Node:      nodeRefFromComment(c),
	}
	// 接口没给作者时，副标题里不能留一个空段：直接写「帖子 · 时间」。
	parts := []string{}
	if c.Author != "" {
		parts = append(parts, c.Author)
	}
	if c.PostTitle != "" {
		parts = append(parts, c.PostTitle)
	}
	if when := relativeTime(c.CreatedAt); when != "" {
		parts = append(parts, when)
	}
	it.Subtitle = strings.Join(parts, " · ")
	meta := []string{}
	if floor := c.effectiveFloor(); floor > 0 {
		meta = append(meta, fmt.Sprintf("%d 楼", floor))
	}
	meta = append(meta, c.nodeLabel())
	it.Meta = strings.Join(meta, " · ")

	// 被回复人通常就是父评论作者；只有两者对得上时才借它的头像与徽章，
	// 否则统一用户标识会退回按昵称生成的图案，不张冠李戴。徽章同样只从已经
	// 拿到的父评论对象里取，不单独请求。
	replyAvatar := ""
	replyBadges := []badgeInfo(nil)
	replyIsPro := false
	if c.ReplyTo != "" && c.ReplyTo == c.ParentAuthor {
		replyAvatar = c.ParentAvatar
		replyBadges = c.ParentBadges
		replyIsPro = c.ParentIsPro
	}
	it.Extra = &rowExtra{
		Author:        c.Author,
		AuthorAvatar:  c.Avatar,
		AuthorBadges:  c.Badges,
		AuthorIsPro:   c.AuthorIsPro,
		ReplyTo:       c.ReplyTo,
		ReplyToAvatar: replyAvatar,
		ReplyToBadges: replyBadges,
		ReplyToIsPro:  replyIsPro,
		Excerpt:       title,
		Quote:         c.quote(),
		NodeLabel:     c.nodeLabel(),
		Node:          nodeRefFromComment(c),
		PostTitle:     c.PostTitle,
		When:          relativeTime(c.CreatedAt),
		Floor:         c.effectiveFloor(),
		ReplyCount:    c.PostReplyCount,
		RewardTotal:   c.Rewards,
		Deleted:       c.Deleted,
		Reactions:     c.Reactions,
	}
	if it.Key == "" {
		it.Key = c.PostShortID
	}
	return it
}

// 最新评论页的连续同用户聚合。「最新评论」接口只有一页 50 条，没有翻页可拉，
// 同一个人连着刷几条就会把这一屏的其他人都挤掉；把这些连续的同名评论收成
// 一组，超过两条才默认只露一条，需要时再展开。分组规则：
//   - 只切分「相邻且 username 相同」的行，绝不跨过中间的人合并：A A A B A A
//     是两组，合并会让时间线顺序失真（见 groupComments）。
//   - 「匿名」不参与聚合：不同人的匿名评论都叫这个名字，并到一起就是张冠李戴。
//   - 一组里还要看它涉及几个帖子：同一个帖子下连着刷是刷帖，横跨多个帖子
//     是同一个人很活跃，展开入口与悬浮说明把这两个数分开写（见 commentGroupTitle）。

// anonymousAuthor 是拿不到作者时 commentItemFrom 填的占位名。
const anonymousAuthor = "匿名"

// commentGroup 是列表里的一组相邻评论。Members 保持接口返回的顺序，首条就是
// 折叠时露出来的那条；start 是它在整体列表里的下标，用来生成稳定的元素 key
// （分组在页尾被新的同人评论续上时会变长，下标不会变）。
type commentGroup struct {
	start   int
	members []Item
}

// groupComments 把相邻且 username 相同的评论收成一组。author 读的是归一后的
// 作者名，空名与「匿名」一律视为无法归属，各自单独成组（每组只有一条）。
func groupComments(items []Item) []commentGroup {
	nameOf := func(it Item) string {
		if it.Extra == nil {
			return ""
		}
		name := it.Extra.Author
		if name == "" || name == anonymousAuthor {
			return ""
		}
		return name
	}
	var groups []commentGroup
	for i, it := range items {
		name := nameOf(it)
		if n := len(groups); n > 0 && name != "" && nameOf(groups[n-1].members[0]) == name {
			groups[n-1].members = append(groups[n-1].members, it)
			continue
		}
		groups = append(groups, commentGroup{start: i, members: []Item{it}})
	}
	return groups
}

// commentGroupPosts 数一组评论涉及几个不同的帖子；没有帖子信息（已删除或
// 接口没给）的条目不计入，标题据此退回「连续评论 N 条」。
func commentGroupPosts(members []Item) int {
	seen := make(map[string]bool, 2)
	for _, it := range members {
		if it.ShortID != "" {
			seen[it.ShortID] = true
		}
	}
	return len(seen)
}

// commentGroupTitle 是展开入口的悬浮说明：一个帖子下连着刷是刷帖，横跨好几个
// 帖子是同一个人很活跃，两种情况给出不同的说法，读的人不必再去数。
func commentGroupTitle(members []Item, posts int) string {
	n := len(members)
	switch posts {
	case 0:
		return fmt.Sprintf("连续评论 %d 条", n)
	case 1:
		if title := members[0].Extra.PostTitle; title != "" {
			return fmt.Sprintf("在《%s》下连续评论 %d 条", title, n)
		}
		return fmt.Sprintf("在同一帖子下连续评论 %d 条", n)
	default:
		return fmt.Sprintf("横跨 %d 个帖子连续评论 %d 条", posts, n)
	}
}

// commentItemAuthor 读一行的用户名；归一阶段没有作者时按「匿名」显示，与行内
// 第一列的头像标识保持一致。
func commentItemAuthor(it Item) string {
	if it.Extra == nil || it.Extra.Author == "" {
		return anonymousAuthor
	}
	return it.Extra.Author
}

// looksLikeComment 判断一行动态是评论还是帖子。必须在 overlay 之前调用：
// overlay 会把帖子正文 content 摊平到外层，之后就无法区分了。
func looksLikeComment(o object) bool {
	return o.sub("comment") != nil || o.sub("reply") != nil ||
		o.str("reply_to_username", "reply_to") != "" || o.has("content")
}

// reactionTextFrom 汇总表情表态（动态流与评论都可能带 reactions_summary）。
func reactionTextFrom(o object) string {
	if o == nil {
		return ""
	}
	raw := o["reactions_summary"]
	if len(raw) == 0 {
		raw = o["reactions"]
	}
	var parts []string
	for _, item := range rawArray(raw) {
		ro := asObject(item)
		if ro == nil {
			continue
		}
		emoji := ro.str("emoji")
		if emoji == "" {
			continue
		}
		if n := ro.num("count"); n > 0 {
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

// rewardsTotal 是动态流里打赏金额的合计。
func rewardsTotal(o object) int {
	if o == nil {
		return 0
	}
	total := 0
	for _, item := range rawArray(o["rewards"]) {
		ro := asObject(item)
		if ro == nil {
			continue
		}
		total += ro.num("amount")
	}
	return total
}

// topicExtra 给动态流里的帖子补上正文摘要与互动数据；普通榜单没有 content，
// 因此不会多出这一层展示。
func topicExtra(o object, author, avatar string, badges []badgeInfo, pro bool, nodeName, when string) *rowExtra {
	content := commentContent(o)
	reactions := reactionTextFrom(o)
	rewards := rewardsTotal(o)
	if content == "" && reactions == "" && rewards == 0 {
		return nil
	}
	return &rowExtra{
		Author:       author,
		AuthorAvatar: avatar,
		AuthorBadges: badges,
		AuthorIsPro:  pro,
		Excerpt:      truncate(commentExcerpt(content), 240),
		NodeLabel:    nodeName,
		When:         when,
		Reactions:    reactions,
		RewardTotal:  rewards,
	}
}
