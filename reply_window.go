package main

import (
	"fmt"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 独立写评论窗口：把评论输入框里的草稿搬进一个单独的 mygo 窗口，左边写、
// 右边实时预览，主窗口留在帖子上——长回复要一边翻正文一边写，页面里的
// 输入框再高也只够写两行。
//
// 草稿始终是 detailState.draft 那一份字符串，窗口只借用它：打开时把
// composerDetached 置位，页面里的评论框换成一行说明与「收回」入口，关掉
// 窗口（或点「收回评论区」）时清掉标记，文字原样回到页面里的评论框。
//
// 数据与请求在 detail.go / detail_ui.go（submitReply）；这里只负责窗口的
// 开关与窗口里的排版。

const (
	replyWindowWidth  = 900
	replyWindowHeight = 620
	replyWinMinWidth  = 520
	replyWinMinHeight = 420
	// replyWindowPad 是窗口内容区与标题栏之间的内边距。
	replyWindowPad = 10
)

// replyFieldMode 决定输入框按哪种形态画：页面里的评论框，还是独立窗口里
// 那个占满窗口高度的编辑区。
type replyFieldMode int

const (
	replyFieldInline replyFieldMode = iota
	replyFieldWindow
)

/* ------------------------------------------------------------------
   入口、显示与关闭
---------------------------------------------------------------- */

// showReplyWindow 是「独立窗口写评论」的入口：从页面评论框的展开图标进来。
// 工作挪到主线程；窗口已经开着时只带到前台，不会开出第二个。
func (a *app) showReplyWindow() {
	a.publish(a.openReplyWindow)
}

// openReplyWindow 在主线程上真正打开窗口。草稿已经在 d.draft 里，所以
// 打开这一帧就能接着写，不会丢字。
func (a *app) openReplyWindow() {
	d := a.detail
	if d == nil {
		return
	}
	if d.topic != nil && d.topic.CommentsClosed {
		a.toast(toastMain, "该帖子已关闭评论")
		return
	}
	// 先把状态摆好：headless 测试没有主循环，碰 mygo 的窗口会等一个不存在
	// 的处理者，所以只把状态准备好，测试直接渲染 replyWindowView。
	d.composerDetached = true
	d.preview = 0 // 页面里的「编辑 / 预览」不带到窗口里：窗口一直两边都显示
	a.replyWinFor = d
	if a.win == nil {
		return
	}
	if win := a.replyWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             "写评论",
		Width:             replyWindowWidth,
		Height:            replyWindowHeight,
		MinWidth:          replyWinMinWidth,
		MinHeight:         replyWinMinHeight,
		StateKey:          "reply",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口、发帖窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.replyWindowView),
	})
	a.replyWin = win
	win.OnClosed(func() {
		a.replyWin = nil
		// 关窗口不丢草稿：清掉标记，页面里的评论框原样展开，文字还在。
		if d.composerDetached {
			d.composerDetached = false
		}
		a.replyWinFor = nil
		if a.win != nil {
			a.win.Invalidate()
		}
	})
}

// closeReplyWindow 收起独立窗口，把草稿还给页面里的评论框。窗口没开着时
// 只清状态（headless 测试走的就是这条），所以「收回评论区」在任何情况下
// 都能把输入框放回页面。
func (a *app) closeReplyWindow() {
	if d := a.replyWinFor; d != nil {
		d.composerDetached = false
	}
	if d := a.detail; d != nil {
		d.composerDetached = false
	}
	a.replyWinFor = nil
	if win := a.replyWin; win != nil {
		a.replyWin = nil
		win.Close()
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// replyWindowOpen 报告独立写评论窗口此刻是否在屏幕上：提交结果据此决定
// 提示弹在哪个窗口。
func (a *app) replyWindowOpen() bool { return a.replyWin != nil }

// redrawReplyWindow 让窗口按最新状态重建（上传进度、发送状态）。只在主线程调用。
func (a *app) redrawReplyWindow() {
	if win := a.replyWin; win != nil {
		win.Invalidate()
	}
}

/* ------------------------------------------------------------------
   窗口界面
---------------------------------------------------------------- */

// replyWindowView 是独立写评论窗口的 view。窗口自带标题栏与窗口按钮，所以
// 这里不需要遮罩，也不占主窗口的位置。
func (a *app) replyWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	a.srv = c.Services()
	c.Root().Background(t.Background)
	// 本窗口里发送失败 / 成功的提示弹在这里；窗口不在了就退回主窗口。
	a.drainToasts(c, a.claimsToasts(toastReply))
	c.OnShortcut(0, ui.KeyEscape, a.closeReplyWindow)
	c.OnShortcut(ui.Ctrl, ui.KeyEnter, a.submitReply)

	d := a.replyWinFor
	if d == nil {
		return
	}
	// 刚粘进来的图片在这里落地：和页面里的评论框一样，这一帧就要把占位符
	// 写进草稿（见 drainPaste）。
	a.drainPaste(uploadTargetReply)

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：左边是标题与「在回复谁」，右边是收回 / 最小化 / 关闭。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, commentBubbleIcon).Size(16, 16).TextColor(t.Accent)
				ui.Text(c, "写评论").FontSize(13).Bold().SingleLine().TextColor(t.Text)
				if sub := replyWindowSubtitle(d); sub != "" {
					ui.Text(c, sub).FontSize(11).TextColor(t.TextMuted).SingleLine()
				}
			})
			// 「收回评论区」给一个带文字的入口：这是窗口里唯一会「关掉」但
			// 不丢草稿的动作，值得比一个叉更明确。
			ui.Row(c).AlignItems(ui.Center).Padding(0, 6).Children(func() {
				if ui.Button(c, "收回评论区").
					Tooltip("把草稿收回页面里的评论框，已经写好的文字不变").
					Clicked() {
					a.closeReplyWindow()
				}
			})
			a.titleButton(c, titleMinimize, "最小化", func() {
				if win := a.replyWin; win != nil {
					win.Minimize()
				}
			})
			a.titleButton(c, titleClose, "关闭", a.closeReplyWindow)
		})
		ui.Divider(c)

		a.replyWindowBody(c, d)
	})
	a.authDialog(c)
}

// replyWindowSubtitle 是标题栏里的一行：在回复哪一层楼，或者是一句新评论。
func replyWindowSubtitle(d *detailState) string {
	if d.replyTo.ID == "" {
		return "新评论"
	}
	return "回复 " + d.replyTo.label()
}

// replyWindowBody 是窗口的正文：一行紧凑工具条（标记 + 表情 / 图片 / 预览
// 开关），按需展开的面板，然后是左边写、右边预览的两栏，底部是状态与发送。
func (a *app) replyWindowBody(c *ui.Context, d *detailState) {
	t := c.Theme()
	// 整个编辑区也是拖放区：图片直接拖进窗口就能上传。
	box := ui.Column(c).FillWidth().Grow(1).MinHeight(0).
		Padding(6, replyWindowPad).Gap(4)
	over := a.editorDropZone(box, uploadTargetReply)
	if over {
		box.Background(t.SurfaceHover)
	}
	box.Children(func() {
		ui.Row(c).FillWidth().Gap(2).AlignItems(ui.Center).KeepFocus().Children(func() {
			a.editorToolbar(c, &d.mdEditor, &d.draft)
			a.editorPanelButtons(c, &d.mdEditor, "插入表情", "插入图片")
			// 预览开关：默认两边都显示，窗口窄的时候可以收起右栏专心写。
			a.editorIconToggle(c, d.winPreview, eyeIcon, "预览",
				"显示 / 收起右侧预览", func() { d.winPreview = !d.winPreview })
		})
		a.editorPanels(c, uploadTargetReply, &d.mdEditor, &d.draft,
			a.replyCommunityEmojis(d))
		if over {
			ui.Text(c, "松开即可把图片插入评论").FontSize(11).
				TextColor(t.Accent).Padding(4, 2)
		}
		ui.Row(c).FillWidth().Grow(1).MinHeight(0).Gap(replyWindowPad).
			AlignItems(ui.Stretch).Children(func() {
			// 左栏：输入框占满整栏，发送在栏底的页脚里。
			ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).
				Padding(4, 8).Radius(10).Border(1, t.Border).
				Background(t.Background).Children(func() {
				c.Key("reply-window-field")
				a.replyTextArea(c, d, replyFieldWindow, true)
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					// 窄窗口只留手势说明：两栏一分，页脚放不下整句
					// 「支持 Markdown 与 :短码: 表情」，截成省略号更难看。
					hint := "支持 Markdown 与 :短码: 表情，Ctrl+Enter 发送"
					if w, _ := c.Size(); w < replyWindowWidth-120 {
						hint = "Ctrl+Enter 发送"
					}
					ui.Text(c, hint).FontSize(11).TextColor(t.TextMuted).SingleLine().
						Grow(1).MinWidth(0)
					ui.Text(c, fmt.Sprintf("%d 字", utf8.RuneCountInString(d.draft))).
						FontSize(11).TextColor(t.TextMuted).SingleLine()
					if d.posting {
						ui.Spinner(c).Size(16, 16)
					}
					a.replyFieldActions(c, d)
				})
			})
			if d.winPreview {
				// 右栏：按发表后的样子实时渲染，跟着窗口高度长，不再固定
				// 180 DIP（那是页面里评论框的高度）。
				ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).
					Radius(10).Border(1, t.Border).
					Background(t.Surface).Children(func() {
					a.editorPreviewView(c, d.draft, a.replyEmojis(d),
						a.commentMd, 0)
				})
			}
		})
	})
}
