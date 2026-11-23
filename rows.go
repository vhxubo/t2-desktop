package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 评论行 / 动态行的字号阶梯。评论没有标题，正文就是这一行的阅读主体：
// 它要比作者名大一档、比帖子标题（17）小一档，才能一眼分出「谁说的」和
// 「说了什么」；元信息再降一档，并整行改用等宽字体，和正文的无衬线体分层。
// 数值与详情页的字号阶梯（detail_ui.go）同一节奏，列表与详情之间切换时
// 正文不会忽大忽小。动态里的帖子行另有 17 粗体标题占住重心，正文只是
// 摘要，因此再小一档，免得标题与摘要糊成一片。
const (
	richCommentFontSize float32 = 15 // 评论正文：这一行的阅读主体
	richPreviewFontSize float32 = 14 // 帖子摘要：标题之下、辅助信息之上
	richAuthorFontSize  float32 = 13 // 作者、被回复人、行内动作
	richMetaFontSize    float32 = 11 // 时间、楼层、节点、原帖、计数、表态（等宽）
)

// foldRowView 画一行可见行：组首条评论先照常画成一条评论，再在它下面挂
// 「展开另外几条」的入口；其余行仍走 rowWith。
func (a *app) foldRowView(c *ui.Context, row foldRow) {
	if row.group == nil {
		a.rowWith(c, row.item, a.openItem)
		return
	}
	g := *row.group
	ui.Column(c).FillWidth().Gap(4).Children(func() {
		a.rowWith(c, row.item, a.openItem)
		a.commentGroupToggle(c, g, row.total, row.open, func() {
			a.tabs[a.tab].toggle(g)
		})
	})
}

// commentGroupToggle 是「最新评论」里一组连续同作者评论的展开 / 收起入口，
// 挂在组首条评论的下方、左对齐。它不再单独占一张卡片：首条评论已经写明了
// 是谁、在哪篇帖子下，这里只需要说清「下面还收着几条」和点一下会怎样。
func (a *app) commentGroupToggle(c *ui.Context, g commentGroup, total int, open bool, toggle func()) {
	tk := lobetheme.TokensOf(c)
	author := commentItemAuthor(g.members[0])
	posts := commentGroupPosts(g.members)
	b := ui.ButtonBase(c).AlignSelf(ui.Start).Padding(4, 6).Radius(tk.BorderRadius).Transition(listRowTransition)
	// 按钮不带外观，悬停底色自己给：点一下才展开，平时只是一行说明文字。
	b.Background(ui.Transparent)
	ink := tk.TextDescription
	if b.Hovered() {
		b.Background(tk.FillTertiary)
		ink = tk.Primary
	}
	b.Children(func() {
		ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
			icon := chevronRightIcon
			if open {
				icon = chevronDownIcon
			}
			ui.Icon(c, icon).Size(12, 12).TextColor(ink).Shrink(0)
			if open {
				ui.Text(c, "收起连续评论").FontSize(richMetaFontSize).TextColor(ink)
				return
			}
			ui.Textf(c, "展开另外 %d 条", total-1).FontSize(richMetaFontSize).TextColor(ink)
			// 同一帖子下连着刷时不必再说地点：首条评论的底行已经挂着原帖。
			if posts > 1 {
				ui.Textf(c, "· 横跨 %d 个帖子", posts).FontSize(richMetaFontSize).
					TextColor(tk.TextDescription)
			}
		})
	})
	if open {
		b.Label("收起 @" + author + " 的连续评论")
		b.Tooltip("收起另外 " + fmt.Sprint(total-1) + " 条，只留第一条")
	} else {
		b.Label("展开 @" + author + " 的连续评论")
		b.Tooltip(commentGroupTitle(g.members, posts) + "，点一下展开")
	}
	b.OnClick(toggle)
}

// richRow 是最新评论 / 动态专用的行：比通用标题+副标题+元信息多了作者、
// 被回复人、父评论引用、正文摘要与表情表态 / 打赏汇总。字段都来自 comments.go
// 的归一结果，普通榜单行不会走到这里。open 决定点击后怎么打开这一行
// （「我的」窗口会先收起自己）。
func (a *app) richRow(c *ui.Context, it Item, open func(Item), watchLater bool) {
	tk := lobetheme.TokensOf(c)
	x := it.Extra
	if x == nil {
		return
	}
	comment := it.CommentID != ""
	// 帖子看它的已读表，评论看评论自己的已读表——读过帖子不等于点开过这条
	// 评论，反过来也一样，不能拿一边的状态给另一边置灰。
	read := a.isPostRead(it.ShortID)
	if comment {
		read = a.isCommentRead(it.CommentID)
	}
	titleColor := postReadInk(tk, read)

	// 统一的 Lobe 行表面：悬停 FillTertiary、圆角与过渡取主题；纵向 padding
	// 收紧到 Lobe 节奏，和主页帖子行保持一致。
	row := feedRowSurface(c).PaddingY(10).PaddingX(12)
	reveal := watchLater && (row.Hovered() || row.FocusWithin())
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
			// 第一行：谁做了什么 · 时间 · 楼层。整行等宽，和标题分层。
			ui.Row(c).Gap(6).AlignItems(ui.Center).Font(sidebarFont).Children(func() {
				author := x.Author
				// 作者与被回复人都是用户名：统一走 userTag，头像跟着 13px
				// 的昵称走，和详情页评论头部一致。接口没给作者（例如
				// /v1/me/bookmarks）时整段作者不画，也不编一个「匿名」。
				if author != "" {
					a.userTag(c, userTagSpec{
						Name:   author,
						Avatar: x.AuthorAvatar,
						Label:  "@" + author,
						Size:   richAuthorFontSize,
						Bold:   true,
						Badges: x.AuthorBadges,
						Pro:    x.AuthorIsPro,
					})
				}
				if comment && x.ReplyTo != "" {
					// 没有作者段时，这一行读起来是「回复 @某人」。
					ui.Text(c, "回复").FontSize(richAuthorFontSize).TextColor(tk.TextDescription)
					a.userTag(c, userTagSpec{
						Name:   x.ReplyTo,
						Avatar: x.ReplyToAvatar,
						Label:  "@" + x.ReplyTo,
						Size:   richAuthorFontSize,
						Bold:   true,
						Badges: x.ReplyToBadges,
						Pro:    x.ReplyToIsPro,
					})
				} else if comment {
					ui.Text(c, "评论了帖子").FontSize(richAuthorFontSize).TextColor(tk.TextDescription)
				} else if x.NodeLabel != "" {
					nodeTagAction(c, nodeTagProps{
						Label: x.NodeLabel, Ref: x.Node, OnPick: a.showNodeFeed,
					})
				}
				ui.Spacer(c).Grow(1)
				if x.When != "" {
					ui.Text(c, x.When).FontSize(richMetaFontSize).TextColor(tk.TextDescription)
				}
				if comment && x.Floor > 0 {
					ui.Textf(c, "#%d", x.Floor).FontSize(richMetaFontSize).TextColor(tk.TextDescription)
				}
				// 和主页帖子行一样，按钮紧跟时间，不独占行尾那一列。
				if !comment {
					a.feedWatchLaterButton(c, it, watchLater, reveal)
				}
			})

			// 帖子行保留标题；评论行的正文在下面作为摘要展示。标题字号比
			// 作者、节点等辅助信息大一档，和主列表行保持一致。
			if !comment && it.Title != "" {
				ui.Text(c, it.Title).FontSize(tk.FontSizeLG).Bold().MaxLines(2).TextColor(titleColor)
			}
			body := x.Excerpt
			if body == "" && comment {
				body = it.Title
			}
			if body != "" {
				// 动态与点开过的评论，正文跟着这一行一起置灰；没点开的评论
				// 仍用正文字色，它是新内容，压灰会让人先跳过它。行高不变。
				size := richPreviewFontSize
				if comment {
					size = richCommentFontSize
				}
				bodyColor := postReadInk(tk, read)
				ui.Text(c, body).FontSize(size).LineHeight(1.55).
					MaxLines(3).TextColor(bodyColor)
			}
			// 被引用的父评论：嵌套回复里这是理解上下文的关键。
			if x.Quote != "" {
				ui.Text(c, "↳ "+x.Quote).FontSize(richMetaFontSize).TextColor(tk.TextDescription).MaxLines(2)
			}

			ui.Row(c).Gap(8).AlignItems(ui.Center).Font(sidebarFont).Children(func() {
				// 节点名与评论数固定宽度、不折行：这一行要让「原帖」在剩余
				// 空间里省略，而不是把旁边的短标签挤成两行。
				if comment && x.NodeLabel != "" {
					ref := x.Node
					if ref.ID == "" && ref.Slug == "" {
						ref = it.Node
					}
					nodeTagAction(c, nodeTagProps{
						Label: x.NodeLabel, Ref: ref, OnPick: a.showNodeFeed,
					}).Shrink(0)
				}
				if comment && x.PostTitle != "" {
					a.postLink(c, it, x.PostTitle, open)
				}
				if comment && x.ReplyCount > 0 {
					ui.Textf(c, "%d 评论", x.ReplyCount).FontSize(richMetaFontSize).
						TextColor(tk.TextDescription).SingleLine().Shrink(0)
				}
				ui.Spacer(c).Grow(1)
				social := x.Reactions
				if x.RewardTotal > 0 {
					if social != "" {
						social += "  "
					}
					social += fmt.Sprintf("💰 %d", x.RewardTotal)
				}
				if social != "" {
					ui.Text(c, social).FontSize(richMetaFontSize).TextColor(tk.TextDescription).Shrink(0)
				}
			})
		})
	})

	// 评论行点开的是它所属的帖子，并记住这条评论以便定位/高亮。
	if it.ShortID != "" || it.URL != "" {
		row.OnClick(func() { open(it) })
	}
}

// postLink 是评论行底部那条「原帖：「标题」」。整行点开的是这条评论，这里
// 再单独给原帖一个入口：点它直接看帖子本身，从帖子顶部开始，不定位到楼层。
// 它挂在行内的 Row 上而不是外层的 ButtonBase 上——最内层的可点元素会吃掉
// 这次点击，所以点「原帖」不会连带触发整行的「打开这条评论」。
func (a *app) postLink(c *ui.Context, it Item, title string, open func(Item)) {
	tk := lobetheme.TokensOf(c)
	link := ui.Row(c).Shrink(1).MinWidth(0).Cursor(ui.CursorPointer)
	link.Children(func() {
		text := ui.Text(c, "原帖：「"+title+"」").FontSize(richMetaFontSize).
			SingleLine().Ellipsis("…")
		// 这一行其余文字都不可点，只有这里能点：悬停时换成强调色加下划线，
		// 否则它看起来和旁边的节点名一样是纯说明。
		if link.Hovered() {
			text.TextColor(tk.Primary).Underline()
		} else {
			text.TextColor(tk.TextDescription)
		}
	})
	// 清掉 CommentID 就是「只看这篇帖子」：openDetail 不再定位楼层，也不再
	// 强制扁平视图（见 detail_ui.go 的 openDetail）。
	post := it
	post.CommentID = ""
	link.OnClick(func() { open(post) })
	link.Tooltip("打开原帖")
}
