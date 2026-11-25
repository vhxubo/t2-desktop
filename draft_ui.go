package main

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 草稿箱页面。它是主窗口右侧内容区的一个页面（pageName = "drafts"），不新开
// mygo 窗口：标题栏与侧边栏照常保留，任务栏里的窗口数不变。切走时和列表页一样
// 整块换掉，右下角的 FAB 列随之收起。

// showDrafts 进入草稿箱：先把正在写的那份并进 store，再换页面。
func (a *app) showDrafts() {
	a.flushDrafts()
	a.pageName = "drafts"
	a.navSel = "drafts"
	if a.win != nil {
		a.win.Invalidate()
	}
}

// newComposeDraft 在草稿箱页头「写新帖」里调用：先把当前表单落盘，再换一个新
// id 打开空表单，已有草稿原样留在箱子里。
func (a *app) newComposeDraft() {
	a.flushDrafts()
	a.compose = newComposeState()
	a.compose.focusTitle = true
	a.compose.mode = composeModePost
	a.showComposeWindow()
}

// draftsBody 画草稿箱页面：页头 + 两组列表（发帖与动态、回复），空箱子走空态。
func (a *app) draftsBody(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	store := a.draftsForCurrent()
	rows := store.rows()
	composeRows, replyRows := splitDraftRows(rows)

	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Padding(24, a.feedInset(c)+16, 24, a.feedInset(c)+16).
			Gap(8).Children(func() {
			// 页头：标题与条数；右边是「写新帖」和「清空全部」。
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Children(func() {
					ui.Text(c, "草稿箱").FontSize(tk.FontSizeHeading4).FontWeight(600).
						LetterSpacing(-0.4).TextColor(tk.TextHeading).SingleLine()
				})
				if n := len(rows); n > 0 {
					ui.Text(c, fmt.Sprintf("共 %d 条", n)).FontSize(tk.FontSizeSM).
						Font(sidebarFont).TextColor(tk.TextDescription).SingleLine().Shrink(0)
				}
				if action.Button(c, "写新帖", action.ButtonProps{
					Size: lobetheme.Small, Variant: action.Text,
				}).Clicked() {
					a.newComposeDraft()
				}
				clear := action.Button(c, "清空全部", action.ButtonProps{
					Size: lobetheme.Small,
				}).Disabled(len(rows) == 0)
				clear.TextColor(tk.Error)
				if clear.Clicked() {
					a.draftsClearOpen = true
				}
			})

			if len(rows) == 0 {
				ui.Column(c).FillWidth().Center().Padding(48, 16).Children(func() {
					display.Empty(c, display.EmptyProps{
						Icon:  lucide.NotebookPen(),
						Title: "还没有草稿",
						Description: "在发帖窗口或评论框里写下的内容会自动存到这里，" +
							"关掉窗口、甚至异常退出也不会丢。",
					})
				})
				return
			}

			if len(composeRows) > 0 {
				a.draftGroup(c, "发帖与动态", composeRows, &a.draftsList)
			}
			if len(replyRows) > 0 {
				a.draftGroup(c, "回复", replyRows, &a.draftsReplyList)
			}
		})
	})
}

func splitDraftRows(rows []draftEntry) (compose, reply []draftEntry) {
	for _, e := range rows {
		if e.Kind == draftKindReply {
			reply = append(reply, e)
		} else {
			compose = append(compose, e)
		}
	}
	return
}

// draftGroup 画一分组：小字分组标题 + 用 ui.List 排的若干行。
func (a *app) draftGroup(c *ui.Context, title string, rows []draftEntry, list *ui.ListState) {
	tk := lobetheme.TokensOf(c)
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Text(c, title).FontSize(11).TextColor(tk.TextDescription).SingleLine()
		ui.List(c, list, len(rows), func(i int) {
			a.draftRow(c, rows[i])
		}).FillWidth()
	})
}

// draftRow 画一条草稿。行表面复用 feedRowSurface，右侧「打开 / 复制 / 删除」
// 在悬停时才出现（与 display.ListItem.Actions 同一套行为），删除用错误色。
// 悬停判定放在外层 Box 上：若拿可点的行本身判 hover，鼠标一移到浮层按钮上
// 行就失去 hover、浮层随即消失，删除按钮根本点不到。
func (a *app) draftRow(c *ui.Context, e draftEntry) {
	tk := lobetheme.TokensOf(c)
	w, _ := c.Size()
	narrow := w-navSidebarWidth < 640
	icon := lucide.FilePen()
	if e.Kind == draftKindReply {
		icon = lucide.MessageSquareText()
	} else if e.Mode == composeModeMoment {
		icon = lucide.NotebookPen()
	}

	headline := draftHeadline(e)
	preview := draftPreview(e)
	meta := draftSummary(e) + " · " + relativeTime(e.UpdatedAt.Format(time.RFC3339))
	runes := utf8.RuneCountInString(e.Body + e.Moment + e.Text)
	if runes > 0 {
		meta += fmt.Sprintf(" · %d 字", runes)
	}

	ui.Column(c).FillWidth().Children(func() {
		// 用一层不可点的 Box 包住整行：浮层按钮在上面时，行本身不再是
		// hover 目标，拿它判 hover 会让浮层在点击瞬间消失（见函数注释）。
		cell := ui.Box(c).AlignSelf(ui.Stretch)
		cell.Children(func() {
			row := feedRowSurface(c).Padding(a.rowPadY(), 12)
			row.Label("打开草稿：" + headline)
			row.Children(func() {
				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icon).Size(16, 16).TextColor(tk.TextSecondary).Shrink(0)
					ui.Column(c).Grow(1).MinWidth(0).Gap(a.rowGap()).Children(func() {
						ui.Text(c, headline).FontSize(tk.FontSizeLG).Bold().
							MaxLines(1).TextColor(tk.Text)
						if preview != "" {
							lines := 2
							if narrow {
								lines = 1
							}
							ui.Text(c, preview).FontSize(tk.FontSizeSM).MaxLines(lines).
								TextColor(tk.TextSecondary)
						}
						ui.Text(c, meta).FontSize(tk.FontSizeSM).Font(sidebarFont).
							MaxLines(1).TextColor(tk.TextDescription)
					})
					// 给右侧动作留出位置，文字不会被浮在上面的按钮压住。
					ui.Box(c).Width(180).Shrink(0)
				})
			})
			if row.Clicked() {
				a.restoreDraft(e)
			}
			if cell.Hovered() || cell.FocusWithin() {
				ui.Row(c).Absolute().Right(12).Top(0).Bottom(0).
					Gap(4).AlignItems(ui.Center).Children(func() {
					if narrow {
						open := action.ActionIcon(c, lucide.SquarePen(), action.ActionIconProps{
							BlockSize: 28, GlyphSize: 14, Title: "打开",
						})
						if open.Clicked() {
							a.restoreDraft(e)
						}
					} else if action.Button(c, "打开", action.ButtonProps{
						Size: lobetheme.Small, Variant: action.Text,
					}).Clicked() {
						a.restoreDraft(e)
					}
					if e.Kind == draftKindReply {
						copyBtn := action.ActionIcon(c, lucide.Copy(), action.ActionIconProps{
							BlockSize: 28, GlyphSize: 14, Title: "复制正文",
						})
						if copyBtn.Clicked() {
							a.copyToClipboard(c, e.Text, toastMain, "已复制草稿正文")
						}
					}
					del := action.ActionIcon(c, lucide.Trash2(), action.ActionIconProps{
						BlockSize: 28, GlyphSize: 14, Title: "删除草稿", Danger: true,
					})
					if del.Clicked() {
						a.deleteDraft(e.ID)
						if a.win != nil {
							a.win.Invalidate()
						}
					}
				})
			}
		})
	})
}

func (a *app) draftsClearDialog(c *ui.Context) {
	if !a.draftsClearOpen {
		return
	}
	rows := a.draftsForCurrent().rows()
	cp, cm, cr := draftCounts(rows)
	body := fmt.Sprintf("将删除当前账号的 %d 条草稿（发帖 %d、动态 %d、回复 %d），"+
		"删除后无法恢复。其它账号的草稿不受影响。", len(rows), cp, cm, cr)
	ok := feedback.Modal(c, &a.draftsClearOpen, feedback.ModalProps{
		Title: "清空所有草稿？", Width: 420, OkText: "清空草稿", CancelText: "取消",
	}, func() {
		ui.Text(c, body).TextColor(c.Theme().TextMuted).MaxLines(4)
	})
	if !ok {
		return
	}
	a.draftsClearOpen = false
	n, err := a.clearAllDrafts()
	if err != nil {
		a.toast(toastMain, "清空草稿失败："+err.Error())
		return
	}
	a.toast(toastMain, fmt.Sprintf("已清空 %d 条草稿", n))
	if a.win != nil {
		a.win.Invalidate()
	}
}

// restoreDraft 打开一条草稿。无论哪种草稿，先 flush 当前正在写的那份，避免
// 「换了一条」把没存的内容冲掉。
func (a *app) restoreDraft(e draftEntry) {
	a.flushDrafts()
	switch e.Kind {
	case draftKindReply:
		a.restoreReplyDraft(e)
	default:
		if a.compose == nil {
			a.compose = newComposeState()
		}
		a.applyComposeDraft(a.compose, e)
		a.compose.mode = e.Mode
		a.showComposeWindow()
	}
}

func (a *app) restoreReplyDraft(e draftEntry) {
	// 这篇帖子已经开着：直接把草稿灌进那份 detailState，切到对应标签并前置。
	if h, idx, d := a.detailForShortID(e.ShortID); d != nil {
		if h != nil && idx >= 0 {
			h.active = idx
		}
		a.detail = d
		a.detailStack = nil
		if h != nil {
			a.detailStack = h.stacks[idx]
		}
		d.applyReplyDraft(e)
		if h != nil {
			a.focusDetailWindow(h)
			h.invalidate()
		} else if a.win != nil {
			a.pageName = "detail"
			a.win.Invalidate()
		}
		return
	}
	// 没开着：按 short_id 打开详情，带 CommentID 滚到那一层楼。
	a.openDetail(Item{
		ShortID:   e.ShortID,
		Title:     e.PostTitle,
		CommentID: e.ReplyTo.ID,
	})
	if _, _, d := a.detailForShortID(e.ShortID); d != nil {
		d.applyReplyDraft(e)
	}
	a.showMainWindowAfterDetail()
}

// detailForShortID 找一篇已经打开的帖子：先查详情窗口各标签，再查主窗口内联页
// 与返回栈。返回窗口、标签下标与状态本身（内联时窗口与下标为空）。
func (a *app) detailForShortID(shortID string) (*detailWindow, int, *detailState) {
	if shortID == "" {
		return nil, -1, nil
	}
	for _, h := range a.detailWindows {
		if h == nil || h.removed {
			continue
		}
		if idx := h.indexOf(shortID); idx >= 0 && idx < len(h.states) {
			return h, idx, h.states[idx]
		}
	}
	if d := a.detail; d != nil && !d.dead && d.shortID == shortID {
		return nil, -1, d
	}
	for _, d := range a.detailStack {
		if d != nil && !d.dead && d.shortID == shortID {
			return nil, -1, d
		}
	}
	return nil, -1, nil
}

func draftHeadline(e draftEntry) string {
	if e.Kind == draftKindReply {
		if strings.TrimSpace(e.PostTitle) != "" {
			return e.PostTitle
		}
		return firstDraftLine(e.Text)
	}
	if strings.TrimSpace(e.Title) != "" {
		return e.Title
	}
	if e.Mode == composeModeMoment {
		return firstDraftLine(e.Moment)
	}
	return firstDraftLine(e.Body)
}

func firstDraftLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "（空白草稿）"
	}
	return s
}

// draftPreview 把正文压成一行摘要：去掉换行与常见 Markdown 标记，再按字数截断。
func draftPreview(e draftEntry) string {
	s := e.Text
	if e.Kind != draftKindReply {
		s = e.Body
		if e.Mode == composeModeMoment || strings.TrimSpace(s) == "" {
			s = e.Moment
		}
	}
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ",
		"#", "", "*", "", ">", "", "`", "", "[", "", "]", "", "!", "")
	s = strings.TrimSpace(replacer.Replace(s))
	r := []rune(s)
	const max = 120
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
