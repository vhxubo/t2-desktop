package main

import (
	"strconv"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 独立写评论窗口：把评论输入框里的草稿搬进一个单独的 mygo 窗口，左边写、
// 右边实时预览，主窗口留在帖子上——长回复要一边翻正文一边写，页面里的
// 输入框再高也只够写两行。
//
// 整套界面走 Lobe UI：颜色取 TokensOf(c)，标题 / 说明 / 提示用 display.Text，
// 图标用 lucide，分隔线用 layout.Divider，工具条开关用 action.ActionIcon，
// 与发帖窗口、主窗口是同一套设计语言。
//
// 草稿始终是 detailState.draft 那一份字符串，窗口只借用它：打开时把
// composerDetached 置位，页面里的评论框换成一行说明与「收回」入口；关掉
// 窗口（点标题栏的关闭按钮、Esc，或页面里的「收回评论区」）时清掉标记，
// 文字原样回到页面里的评论框。窗口内不再单独放一个「收回评论区」按钮。
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
	// 身份气泡跟着评论框走：主窗口里点开的那一份先收起，到窗口里再点开。
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
	win := a.newAppWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle("写评论"),
		Width:             replyWindowWidth,
		Height:            replyWindowHeight,
		MinWidth:          replyWinMinWidth,
		MinHeight:         replyWinMinHeight,
		StateKey:          "reply",
		BackgroundColor:   lobeWindowBackground,
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口、发帖窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.windowView(a.replyWindowView)),
	}, toastReply)
	a.replyWin = win
	win.OnClosed(func() {
		a.replyWin = nil
		a.authGateWindowClosed(toastReply)
		// 关窗口不丢草稿：文字还在 detailState 里，先落盘再清标记。
		a.flushDrafts()
		// 清掉标记，页面里的评论框原样展开，文字还在。
		if d.composerDetached {
			d.composerDetached = false
		}
		a.replyWinFor = nil
		if a.win != nil {
			a.win.Invalidate()
		}
		a.accountWindowClosed(win)
	})
}

// closeReplyWindow 收起独立窗口，把草稿还给页面里的评论框。窗口没开着时
// 只清状态（headless 测试走的就是这条），所以窗口里的关闭按钮和页面里的
// 「收回评论区」在任何情况下都能把输入框放回页面。
func (a *app) closeReplyWindow() {
	// 关窗前先落盘：这一帧之后独立窗口不再重建，草稿靠 detailState 继续活着。
	a.flushDrafts()
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
	tk := lobetheme.TokensOf(c)
	a.srv = c.Services()
	c.Root().Background(tk.BgLayout)
	// 这一帧的详情游标拨到写评论窗口绑定的那篇：正文上传、发送结果里的
	// toast 与身份气泡都要落在这篇帖子上，而不是上一条画过的详情窗口。
	if d := a.replyWinFor; d != nil {
		a.detail = d
	}
	// 本窗口里发送失败 / 成功的提示弹在这里；窗口不在了就退回主窗口。
	defer a.drainToasts(c, a.claimsToasts(toastReply))
	defer a.draftTick()
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
		// 自绘标题栏：左边是标题与「在回复谁」，右边是最小化 / 关闭。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(tk.BgLayout).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, lucide.MessageSquareText()).Size(16, 16).TextColor(tk.Primary)
				display.Text(c, "写评论", display.TextProps{
					Strong: true, FontSize: 13, Rows: 1,
				})
				if sub := replyWindowSubtitle(d); sub != "" {
					display.Text(c, sub, display.TextProps{
						Type: display.TextSecondary, FontSize: 11, Rows: 1,
					})
				}
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := a.replyWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", a.closeReplyWindow)
		})
		layout.Divider(c, layout.DividerProps{})

		a.replyWindowBody(c, d)
	})
	a.authDialogFor(c, toastReply)
}

// replyWindowSubtitle 是标题栏里的一行：在回复哪一层楼，或者是一句新评论。
func replyWindowSubtitle(d *detailState) string {
	if d.replyTo.ID == "" {
		return "新评论"
	}
	return "回复 " + d.replyTo.label()
}

// replyWindowPreviewToggle 是窗口工具条上的预览开关：Lobe 的图标按钮，
// 选中时用底色标出「显示中」。label 保留文字名，读屏与点击测试照旧按
// 「预览」找得到；KeepFocus 让点开关时光标仍留在编辑区里。
func replyWindowPreviewToggle(c *ui.Context, on bool, onClick func()) {
	icon := lucide.Eye()
	if !on {
		icon = lucide.EyeOff()
	}
	b := action.ActionIcon(c, icon, action.ActionIconProps{
		Size: lobetheme.Small, Active: on, Title: "预览",
	}).KeepFocus()
	if b.Clicked() {
		onClick()
	}
}

// replyWindowBody 是窗口的正文：一行紧凑工具条（标记 + 表情 / 图片 / 预览
// 开关），按需展开的面板，然后是左边写、右边预览的两栏，底部是状态与发送。
func (a *app) replyWindowBody(c *ui.Context, d *detailState) {
	tk := lobetheme.TokensOf(c)
	// 整个编辑区也是拖放区：图片直接拖进窗口就能上传。
	box := ui.Column(c).FillWidth().Grow(1).MinHeight(0).
		Padding(6, replyWindowPad).Gap(4)
	over := a.editorDropZone(box, uploadTargetReply)
	if over {
		box.Background(tk.FillSecondary)
	}
	box.Children(func() {
		ui.Row(c).FillWidth().Gap(2).AlignItems(ui.Center).KeepFocus().Children(func() {
			a.editorToolbar(c, &d.mdEditor, &d.draft)
			a.editorPanelButtons(c, &d.mdEditor, &d.draft, a.replyCommunityEmojis(d), "插入表情", "插入图片")
			// 预览开关：默认两边都显示，窗口窄的时候可以收起右栏专心写。
			replyWindowPreviewToggle(c, d.winPreview, func() { d.winPreview = !d.winPreview })
		})
		a.editorPanels(c, uploadTargetReply, &d.mdEditor, &d.draft,
			a.replyCommunityEmojis(d))
		if over {
			display.Text(c, "松开即可把图片插入评论", display.TextProps{
				Type: display.TextInfo, FontSize: 11, Rows: 1,
			}).Padding(4, 2)
		}
		// 评论费用状态栏：字数、预计发评金币、可省金币的徽章与「走个面」
		// 开关。占整窗宽度，左右两栏都看得到（见 comment_cost_ui.go）。
		a.commentCostBar(c, d.draft)
		// 两栏这一行给一个固定 key：状态栏在登录 / 退出时会多出或消失一个
		// 兄弟节点，没有 key 的话输入框会被挤到另一个节点身份上，焦点与
		// 选区随之丢失。
		c.Key("reply-window-columns")
		ui.Row(c).FillWidth().Grow(1).MinHeight(0).Gap(replyWindowPad).
			AlignItems(ui.Stretch).Children(func() {
			// 左栏：输入框占满整栏，发送在栏底的页脚里。
			ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).
				Padding(4, 8).Radius(10).Border(1, tk.BorderSecondary).
				Background(tk.BgContainer).Children(func() {
				c.Key("reply-window-field")
				a.replyTextArea(c, d, replySurfaceWindow, replyFieldWindow, true)
				// 独立窗口的输入框自带 AutoFocus，这里只消费一次性恢复标记。
				d.focusComposer = false
				// 面板要贴着 @ / :：输入框自己有 8 / 4 的内边距，字符的矩形
				// 要按同一份内边距算，否则面板会偏到输入框边框上。
				a.mentionPopover(c, d, replySurfaceWindow, 8, 4)
				a.emojiShortcodePopover(c, d, replySurfaceWindow, 8, 4)
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					// 窄窗口只留手势说明：两栏一分，页脚放不下整句
					// 「支持 Markdown 与 :短码: 表情」，截成省略号更难看。
					hint := "支持 Markdown 与 :短码: 表情，Ctrl+Enter 发送"
					if w, _ := c.Size(); w < replyWindowWidth-120 {
						hint = "Ctrl+Enter 发送"
					}
					display.Text(c, hint, display.TextProps{
						Type: display.TextSecondary, FontSize: 11, Rows: 1,
					}).Grow(1).MinWidth(0)
					// 登录后字数在评论费用状态栏里报（见 comment_cost_ui.go），
					// 同一条链上不重复写两遍；未登录时状态栏收起，仍留在这里。
					if !a.commentCostBarVisible() {
						display.Text(c, strconv.Itoa(utf8.RuneCountInString(d.draft))+" 字",
							display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
					}
					// 发送中的转圈跟着动作本身走（见 replyFieldActions）。
					a.replyFieldActions(c, d, false)
				})
			})
			if d.winPreview {
				// 右栏：按发表后的样子实时渲染，跟着窗口高度长，不再固定
				// 180 DIP（那是页面里评论框的高度）。
				ui.Column(c).Grow(1).MinWidth(0).MinHeight(0).
					Radius(10).Border(1, tk.BorderSecondary).
					Background(tk.BgElevated).Children(func() {
					a.editorPreviewView(c, d.draft, a.replyEmojis(d),
						a.commentMd, 0)
				})
			}
		})
	})
	// 预览里的配图也能点开多图预览；预览是一扇独立窗口，这里只在图片到位后
	// 补开它（见 viewerTick）。
	a.viewerTick()
}
