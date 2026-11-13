package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// 评论行 / 动态行的字号阶梯。评论没有标题，正文就是这一行的阅读主体：
// 它要比作者名大一档、比帖子标题（16）小一档，才能一眼分出「谁说的」和
// 「说了什么」；元信息再降一档，不再出现 11 这种要凑近看的字号。
// 数值与详情页的字号阶梯（detail_ui.go）同一节奏，列表与详情之间切换时
// 正文不会忽大忽小。动态里的帖子行另有 16 粗体标题占住重心，正文只是
// 摘要，因此再小一档，免得标题与摘要糊成一片。
const (
	richCommentFontSize float32 = 15 // 评论正文：这一行的阅读主体
	richPreviewFontSize float32 = 14 // 帖子摘要：标题之下、辅助信息之上
	richAuthorFontSize  float32 = 13 // 作者、被回复人、行内动作
	richMetaFontSize    float32 = 12 // 时间、楼层、节点、原帖、计数、表态
)

// richRow 是最新评论 / 动态专用的行：比通用标题+副标题+元信息多了作者、
// 被回复人、父评论引用、正文摘要与表情表态 / 打赏汇总。字段都来自 comments.go
// 的归一结果，普通榜单行不会走到这里。open 决定点击后怎么打开这一行
// （「我的」窗口会先收起自己）。
func (a *app) richRow(c *ui.Context, it Item, open func(Item)) {
	t := c.Theme()
	x := it.Extra
	if x == nil {
		return
	}
	comment := it.CommentID != ""
	// 帖子行跟着本地已读状态把标题置灰；评论行不跟着变——读过帖子不等于
	// 读过这一条新评论，把评论标题也压灰会把新内容藏起来。
	titleColor := postTitleColor(t, !comment && a.isPostRead(it.ShortID))

	row := ui.ButtonBase(c).FillWidth().Padding(14, 16).Radius(10).
		Background(t.Background).Border(1, t.Border)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
			// 第一行：谁做了什么 · 时间 · 楼层。
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				author := x.Author
				if author == "" {
					author = "匿名"
				}
				// 作者与被回复人都是用户名：统一走 userTag，头像跟着 13px
				// 的昵称走，和详情页评论头部一致。
				a.userTag(c, userTagSpec{
					Name:   author,
					Avatar: x.AuthorAvatar,
					Label:  "@" + author,
					Size:   richAuthorFontSize,
					Bold:   true,
					Badges: x.AuthorBadges,
				})
				if comment {
					if x.ReplyTo != "" {
						ui.Text(c, "回复").FontSize(richAuthorFontSize).TextColor(t.TextMuted)
						a.userTag(c, userTagSpec{
							Name:   x.ReplyTo,
							Avatar: x.ReplyToAvatar,
							Label:  "@" + x.ReplyTo,
							Size:   richAuthorFontSize,
							Bold:   true,
							Badges: x.ReplyToBadges,
						})
					} else {
						ui.Text(c, "评论了帖子").FontSize(richAuthorFontSize).TextColor(t.TextMuted)
					}
				} else if x.NodeLabel != "" {
					ui.Text(c, x.NodeLabel).FontSize(richMetaFontSize).TextColor(t.TextMuted)
				}
				ui.Spacer(c).Grow(1)
				if x.When != "" {
					ui.Text(c, x.When).FontSize(richMetaFontSize).TextColor(t.TextMuted)
				}
				if comment && x.Floor > 0 {
					ui.Textf(c, "#%d", x.Floor).FontSize(richMetaFontSize).TextColor(t.TextMuted)
				}
			})

			// 帖子行保留标题；评论行的正文在下面作为摘要展示。标题字号比
			// 作者、节点等辅助信息大一档，和主列表行保持一致。
			if !comment && it.Title != "" {
				ui.Text(c, it.Title).FontSize(16).Bold().MaxLines(2).TextColor(titleColor)
			}
			body := x.Excerpt
			if body == "" && comment {
				body = it.Title
			}
			if body != "" {
				// 正文用正文字色（不用灰色）并放宽行高：这是评论行里唯一
				// 需要连续阅读的部分，压小或压灰都会让人先跳过它。
				size := richPreviewFontSize
				if comment {
					size = richCommentFontSize
				}
				ui.Text(c, body).FontSize(size).LineHeight(1.55).
					MaxLines(3).TextColor(t.Text)
			}
			// 被引用的父评论：嵌套回复里这是理解上下文的关键。
			if x.Quote != "" {
				ui.Text(c, "↳ "+x.Quote).FontSize(richMetaFontSize).TextColor(t.TextMuted).MaxLines(2)
			}

			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				// 节点名与评论数固定宽度、不折行：这一行要让「原帖」在剩余
				// 空间里省略，而不是把旁边的短标签挤成两行。
				if comment && x.NodeLabel != "" {
					ui.Text(c, x.NodeLabel).FontSize(richMetaFontSize).
						TextColor(t.TextMuted).SingleLine().Shrink(0)
				}
				if comment && x.PostTitle != "" {
					a.postLink(c, it, x.PostTitle, open)
				}
				if comment && x.ReplyCount > 0 {
					ui.Textf(c, "%d 评论", x.ReplyCount).FontSize(richMetaFontSize).
						TextColor(t.TextMuted).SingleLine().Shrink(0)
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
					ui.Text(c, social).FontSize(richMetaFontSize).TextColor(t.TextMuted).Shrink(0)
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
	t := c.Theme()
	link := ui.Row(c).Shrink(1).MinWidth(0).Cursor(ui.CursorPointer)
	link.Children(func() {
		text := ui.Text(c, "原帖：「"+title+"」").FontSize(richMetaFontSize).
			SingleLine().Ellipsis("…")
		// 这一行其余文字都不可点，只有这里能点：悬停时换成强调色加下划线，
		// 否则它看起来和旁边的节点名一样是纯说明。
		if link.Hovered() {
			text.TextColor(t.Accent).Underline()
		} else {
			text.TextColor(t.TextMuted)
		}
	})
	// 清掉 CommentID 就是「只看这篇帖子」：openDetail 不再定位楼层，也不再
	// 强制扁平视图（见 detail_ui.go 的 openDetail）。
	post := it
	post.CommentID = ""
	link.OnClick(func() { open(post) })
	link.Tooltip("打开原帖")
}
