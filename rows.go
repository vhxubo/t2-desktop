package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
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

	row := ui.ButtonBase(c).FillWidth().Padding(12, 14).Radius(10).
		Background(t.Background).Border(1, t.Border)
	if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Column(c).Grow(1).Gap(4).Children(func() {
			// 第一行：谁做了什么 · 时间 · 楼层。
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				author := x.Author
				if author == "" {
					author = "匿名"
				}
				// 作者与被回复人都是用户名：统一走 userTag，头像跟着 12px
				// 的昵称走，和评论头部保持一致。
				a.userTag(c, userTagSpec{
					Name:   author,
					Avatar: x.AuthorAvatar,
					Label:  "@" + author,
					Size:   12,
					Bold:   true,
				})
				if comment {
					if x.ReplyTo != "" {
						ui.Text(c, "回复").FontSize(12).TextColor(t.TextMuted)
						a.userTag(c, userTagSpec{
							Name:   x.ReplyTo,
							Avatar: x.ReplyToAvatar,
							Label:  "@" + x.ReplyTo,
							Size:   12,
							Bold:   true,
						})
					} else {
						ui.Text(c, "评论了帖子").FontSize(12).TextColor(t.TextMuted)
					}
				} else if x.NodeLabel != "" {
					ui.Text(c, x.NodeLabel).FontSize(11).TextColor(t.TextMuted)
				}
				ui.Spacer(c).Grow(1)
				if x.When != "" {
					ui.Text(c, x.When).FontSize(11).TextColor(t.TextMuted)
				}
				if comment && x.Floor > 0 {
					ui.Textf(c, "#%d", x.Floor).FontSize(11).TextColor(t.TextMuted)
				}
			})

			// 帖子行保留标题；评论行的正文在下面作为摘要展示。
			if !comment && it.Title != "" {
				ui.Text(c, it.Title).FontSize(14).Bold().MaxLines(2).TextColor(titleColor)
			}
			body := x.Excerpt
			if body == "" && comment {
				body = it.Title
			}
			if body != "" {
				ui.Text(c, body).FontSize(13).MaxLines(3)
			}
			// 被引用的父评论：嵌套回复里这是理解上下文的关键。
			if x.Quote != "" {
				ui.Text(c, "↳ "+x.Quote).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
			}

			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if comment && x.NodeLabel != "" {
					ui.Text(c, x.NodeLabel).FontSize(11).TextColor(t.TextMuted)
				}
				if comment && x.PostTitle != "" {
					ui.Text(c, "原帖：「"+x.PostTitle+"」").FontSize(11).
						TextColor(t.TextMuted).SingleLine()
				}
				if comment && x.ReplyCount > 0 {
					ui.Textf(c, "%d 评论", x.ReplyCount).FontSize(11).TextColor(t.TextMuted)
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
					ui.Text(c, social).FontSize(11).TextColor(t.TextMuted)
				}
			})
		})
	})

	// 评论行点开的是它所属的帖子，并记住这条评论以便定位/高亮。
	if it.ShortID != "" || it.URL != "" {
		row.OnClick(func() { open(it) })
	}
}
