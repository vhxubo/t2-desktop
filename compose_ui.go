package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/choice"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 「发帖」窗口：点主页右下角的悬浮按钮（FAB）打开，写标题与正文（Markdown，
// 带工具条与预览），并按需挂上发帖接口提供的各类组件——投票、评分、竞猜、
// 兑换码 / 红包、金币池，以及「谁可以看」的可见性门槛。
//
// 左边专心写：标题、正文，最下面才是「节点」。节点是选填的：留空就发出去，
// 由站点的 AI 判定分类；想指定时用两列级联选择挑一个，也随时能清掉。右边是
// 常驻的控制栏：上面是匿名等发布设置，下面是各块组件，每块点开在弹窗里填写；
// 已添加的行尾直接有「移除」，不必再进弹窗，底部还有「全部移除」。凡是可以有
// 多个选项的地方（投票、竞猜、兑换码）都有「批量添加」与批量删除入口。
//
// 整套界面走 Lobe UI：颜色取 TokensOf(c)，标签 / 按钮 / 开关 / 输入框 / 提示
// 分别由 display.Text、action.Button、choice.Switch、field 与 feedback 提供，
// 和主窗口、账号面板、详情页是同一套设计语言；文案与动作保持不变，只换观感。
//
// 这是独立的 mygo 窗口而不是主窗口里的一层遮罩。数据、校验与请求在
// compose.go；这里只负责窗口的开关与界面。

const (
	composeWindowWidth  = 1000
	composeWindowHeight = 760
	composeWinMinWidth  = 600
	composeWinMinHeight = 520
	// 右侧常驻的附加组件栏与它的编辑弹窗。Lobe 的 Modal 自带标题、关闭、
	// 取消/确认按钮与「内容区可滚动、页脚常驻」的布局，窗口缩到最小尺寸时
	// 字段也不会长出屏幕，这里只给面板宽度。
	composeSidebarWidth      = 200
	composeModuleDialogWidth = 520
	composeEditorLines       = 10
	composeEditorMaxLine     = 30
	composePreviewHeight     = 320
	// composeFabInset 是为右下角 FAB 列预留的右侧宽度（按钮 + 边距）。列表
	// 底部的「加载更多 / 重试」那一行据此让出这一段，免得被浮在上面的按钮盖住；
	// FAB 本身不占布局高度，只是浮在内容区右下角。
	composeFabInset = 110
)

/* ------------------------------------------------------------------
   入口、显示与关闭
---------------------------------------------------------------- */

// showCompose 是「发帖」窗口的全局入口：任何窗口、任何 goroutine 都能调用。
// 它把工作挪到主线程；窗口已经开着就只把它带到前台，不会开出第二个。从
// 「发帖」入口进来时总是切回帖子模式；动态入口见 showMomentCompose。
func (a *app) showCompose() {
	a.publish(func() {
		if a.compose == nil {
			// 表单是空的：续写最近编辑的一条帖子草稿；一条都没有才开空白表单。
			if e, ok := a.draftsForCurrent().latestCompose(composeModePost); ok {
				a.compose = newComposeState()
				a.applyComposeDraft(a.compose, e)
			} else {
				a.compose = newComposeState()
			}
		}
		a.compose.mode = composeModePost
		a.showComposeWindow()
	})
}

// showMomentCompose 是「发动态」入口：复用同一扇窗口，切到动态模式。
func (a *app) showMomentCompose() {
	a.publish(a.showMomentComposeWindow)
}

func (a *app) showMomentComposeWindow() {
	if a.compose == nil {
		if e, ok := a.draftsForCurrent().latestCompose(composeModeMoment); ok {
			a.compose = newComposeState()
			a.applyComposeDraft(a.compose, e)
		} else {
			a.compose = newComposeState()
		}
	}
	a.compose.mode = composeModeMoment
	a.showComposeWindow()
}

// composeWindowTitle 是窗口标题栏 / 任务栏上的名字。
func composeWindowTitle(mode int) string {
	if mode == composeModeMoment {
		return "发动态"
	}
	return "发帖"
}

// showComposeWindow 在主线程上真正打开窗口。草稿留在 a.compose 里，关掉再
// 打开还是原样；退出登录时才丢弃。
func (a *app) showComposeWindow() {
	if a.compose == nil {
		a.compose = newComposeState()
		a.compose.focusTitle = true
	}
	if !a.auth.loggedIn() {
		msg := "登录 2Libra 后才能发帖。"
		if a.compose.mode == composeModeMoment {
			msg = "登录 2Libra 后才能发动态。"
		}
		// 先开窗口再登记授权：弹窗只画在发帖窗口里，登记时它必须已经存在。
		// 登录授权一次就把发帖需要的 write:topic 一起申请下来。授权完成后由
		// 用户自己点发送，应用不代发。
		a.openComposeWindow()
		a.requestLoginWithScopesFor(toastCompose, msg, []string{"write:topic"})
		return
	}
	// 节点是选填的（留空由 AI 判定分类），但列表照样先读本地缓存、按新鲜度
	// 刷新：用户想手动指定时，级联选择里已经有东西可选。
	a.openComposeWindow()
}

// openComposeWindow 建窗口或把已有窗口带到前台。headless 测试没有主循环：
// 碰 mygo 的窗口会等待一个不存在的处理者，所以只把状态准备好，测试直接
// 渲染 composeWindowView。
func (a *app) openComposeWindow() {
	if a.win == nil {
		return
	}
	if win := a.composeWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle(composeWindowTitle(a.compose.mode)),
		Width:             composeWindowWidth,
		Height:            composeWindowHeight,
		MinWidth:          composeWinMinWidth,
		MinHeight:         composeWinMinHeight,
		StateKey:          "compose",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口、通知窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.windowView(a.composeWindowView)),
	})
	a.composeWin = win
	win.OnClosed(func() {
		a.composeWin = nil
		a.authGateWindowClosed(toastCompose)
		// 用户关窗就是「存好了」：同步落盘，别只等定时器。
		a.flushDrafts()
		a.accountWindowClosed(win)
	})
}

// closeComposeWindow 关掉发帖窗口：用户关窗或退出登录时调用。关主窗口不再
// 顺手收掉它——每个窗口独立。草稿留在 a.compose 里，下次打开继续写。只在
// 主线程调用。
func (a *app) closeComposeWindow() {
	win := a.composeWin
	if win == nil {
		return
	}
	a.composeWin = nil
	a.flushDrafts()
	win.Close()
}

// redrawComposeWindow 让发帖窗口按最新状态重建。只在主线程调用。
func (a *app) redrawComposeWindow() {
	if win := a.composeWin; win != nil {
		win.Invalidate()
	}
}

/* ------------------------------------------------------------------
   主页的悬浮入口
---------------------------------------------------------------- */

// composePostIcon 画发帖主操作里的图标。窗口底部的「发布」按钮用它；主页
// 悬浮的「发帖」按钮是 Lobe ActionIcon，直接吃 lucide.SquarePen，同一枚图标
// 由 ActionIcon 按自己的字号绘制（见 composeFab、composePublishButton）。
func composePostIcon(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	ui.Icon(c, lucide.SquarePen()).Size(15, 15).TextColor(tk.PrimaryForeground)
}

// composePublishButton 画底部的主操作按钮：图标 + 文案，图标与文字的间距沿用
// Lobe Button 自带的 6 DIP，配色直接取强调色按钮的前景色，与 FAB 同一套观感。
func composePublishButton(c *ui.Context, label string) ui.Element {
	b := action.StyleButton(c, ui.ButtonBase(c), "", action.ButtonProps{Variant: action.Primary})
	b.Label(label).Children(func() {
		composePostIcon(c)
		// 与 action.Button 的文案用同一字阶，只是前面多了一枚图标。
		ui.Text(c, label).SingleLine()
	})
	return b
}

// composeFab 是主页右下角那一列悬浮按钮里的「发帖」：主操作，放在最下面。
// 位置由 listFabs 的竖列给（见 fabs.go），这里只管长什么样；公开 tab 与
// 私人 tab 都能用（未登录时点它先走授权）。
//
// 和列里其余按钮一样用 Lobe 的 ActionIcon：圆形热区、悬停 / 按下反馈与图标
// 都由它负责，图标是 lucide.SquarePen，文案交给 tooltip。可访问名仍是「发帖」
// （Label），读屏与测试按这个名字找它。ActionIcon 默认的中性配色留给次级按钮；
// 这里是主操作，底 / 前景色改取 Lobe 的 Primary token，静止、悬停、按下分别
// 对应 Primary、PrimaryHover、PrimaryActive，保持原来强调色按钮的层次。
func (a *app) composeFab(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	b := action.ActionIcon(c, lucide.SquarePen(), action.ActionIconProps{BlockSize: fabPrimary})
	b.Label("发帖").Tooltip("写一篇新帖子").OnClick(a.showCompose)
	bg := tk.Primary
	switch {
	case b.Pressed():
		bg = tk.PrimaryActive
	case b.Hovered():
		bg = tk.PrimaryHover
	}
	b.Background(bg).Border(1, bg).TextColor(tk.PrimaryForeground).Radius(fabPrimary / 2)
}

/* ------------------------------------------------------------------
   Lobe 字段的小工具
---------------------------------------------------------------- */

// composeInputRow 让 Lobe 字段铺满一整行：field 的输入控件返回的是内层输入，
// 外层那一行默认按内容收缩，这里借 Column 的 stretch 把外层也撑满，再把可继续
// 配置（Label / Bind / MaxLength）的内层控件交回调用方。
func composeInputRow(c *ui.Context, build func() ui.Element) ui.Element {
	var el ui.Element
	ui.Column(c).FillWidth().AlignItems(ui.Stretch).Children(func() {
		el = build()
	})
	return el
}

// composeInput 是一行铺满的 Lobe 文本框。
func composeInput(c *ui.Context, value *string, p field.InputProps) ui.Element {
	return composeInputRow(c, func() ui.Element { return field.Input(c, value, p) })
}

// composeTextArea 是一行铺满的 Lobe 多行输入框。
func composeTextArea(c *ui.Context, value *string, p field.TextAreaProps) ui.Element {
	return composeInputRow(c, func() ui.Element { return field.TextArea(c, value, p) })
}

// composeNumber 是一行铺满的 Lobe 数字输入框。
func composeNumber(c *ui.Context, value *float64, p field.InputNumberProps) ui.Element {
	return composeInputRow(c, func() ui.Element { return field.InputNumber(c, value, p) })
}

// composeToggle 是一行 Lobe 开关：开关本体与文字都能切换。原来的 ui.Toggle
// 把文字画在开关里，点文字也生效；这里开关与文字是两个控件，文字那一半补上
// 点击，保住同样的手感，两者的无障碍名仍是 label。
func composeToggle(c *ui.Context, on *bool, label string) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		choice.Switch(c, on, choice.SwitchProps{Size: lobetheme.Small}).Label(label)
		if action.Button(c, label, action.ButtonProps{
			Variant: action.Text, Size: lobetheme.Small,
		}).Clicked() {
			*on = !*on
		}
	})
}

// composeDeadlineField 画选填的截止时间。开关决定这一项是否进请求体；打开后
// 用 Lobe（mygo ui）自带的日期与时间控件直接编辑同一个本地 time.Time，不再
// 让用户手敲时间字符串。name 用来给两个控件各自的无障碍名加前缀。
func composeDeadlineField(c *ui.Context, on *bool, at *time.Time, name string) {
	composeToggle(c, on, "设置截止时间")
	if *on {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.DateInput(c, at).Label(name + "截止日期")
			ui.TimeInput(c, at).Label(name + "截止时间")
		})
	}
}

/* ------------------------------------------------------------------
   窗口界面
---------------------------------------------------------------- */

// composeWindowView 是发帖窗口的 view。窗口自带标题栏与窗口按钮，所以这里
// 不需要遮罩，也不占主窗口的位置。
func (a *app) composeWindowView(c *ui.Context) {
	a.applyTheme(c)
	tk := lobetheme.TokensOf(c)
	// 回执里的「看原文」要打开系统浏览器：服务在 UI 线程取一次，回调里不再碰 Context。
	a.srv = c.Services()
	c.Root().Background(tk.BgLayout)
	// 本窗口里发起的操作结果以 toast 显示在这里，不退回主窗口。
	defer a.drainToasts(c, a.claimsToasts(toastCompose))
	defer a.draftTick()
	// Ctrl+Enter 发布：写长文的常用手势。动态模式下发的是短动态。
	c.OnShortcut(ui.Ctrl, ui.KeyEnter, func() {
		if a.compose != nil && a.compose.mode == composeModeMoment {
			a.submitMoment()
			return
		}
		a.submitCompose()
	})

	st := a.compose
	if st == nil {
		st = newComposeState()
		a.compose = st
	}
	// 模式切换后窗口标题也跟着变；窗口还没建时由 openComposeWindow 用当前
	// 模式命名。
	if st.mode != st.lastMode {
		st.lastMode = st.mode
		// 切换帖子 / 动态时两块候选面板都收起：草稿各存各的，面板不跟着切。
		resetComposeSuggest(st)
		if a.composeWin != nil {
			a.composeWin.SetTitle(a.subWindowTitle(composeWindowTitle(st.mode)))
		}
	}
	// 节点是选填的：默认留空，由站点的 AI 判定分类。列表照样先读本地缓存、
	// 再按新鲜度刷新，用户想指定时级联选择里已经有东西可选。
	a.ensureComposeNodes()
	// Esc 关闭窗口，但组件弹窗开着时由 Modal 自己吃掉 Esc（只关弹窗，不丢
	// 草稿，也不关窗口），所以那一帧不再注册窗口级快捷键。
	if !st.moduleOpen {
		c.OnShortcut(0, ui.KeyEscape, a.closeComposeWindow)
	}

	title := "发帖"
	hint := "· 支持 Markdown"
	if st.mode == composeModeMoment {
		title, hint = "发动态", "· 最多 500 字"
	}
	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口，右上角是最小化与关闭。图标与文字都走 Lobe。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(tk.BgLayout).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, lucide.SquarePen()).Size(16, 16).TextColor(tk.Primary)
				display.Text(c, title, display.TextProps{Strong: true, FontSize: 13, Rows: 1})
				display.Text(c, hint, display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
				a.accountWindowTag(c)
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := a.composeWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", func() { a.closeComposeWindow() })
		})
		layout.Divider(c, layout.DividerProps{})

		// 模式切换：帖子与动态共用一扇窗口，草稿各自保留。
		ui.Row(c).FillWidth().Padding(6, 14).Children(func() {
			action.Segmented(c, &st.mode, composeModeLabels, action.SegmentedProps{})
		})
		layout.Divider(c, layout.DividerProps{})

		if st.mode == composeModeMoment {
			a.composeMomentBody(c, st)
		} else {
			// 主体分两栏：左边专心写，右边常驻组件入口。两栏各自滚动，正文
			// 写多长都不会把组件列表顶出视野。ScrollArea 用 Lobe 的细滚动条。
			c.Key("compose-body")
			ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
				c.Key("compose-writing-scroll")
				main := layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
					ui.Column(c).FillWidth().Padding(18, 20).Gap(14).Children(func() {
						a.composeTitleField(c, st)
						a.composeEditorSection(c, st)
						layout.Divider(c, layout.DividerProps{})
						a.composeMessages(c, st)
						// 节点排在写作区最后：选填，默认留空，由 AI 判定分类。
						a.composeNodeSection(c, st)
					})
				})
				main.Grow(1).MinHeight(0)
				ui.Box(c).Width(1).AlignSelf(ui.Stretch).Background(tk.BorderSecondary)
				a.composeSidebar(c, st)
			})
		}

		layout.Divider(c, layout.DividerProps{})
		a.composeFooter(c, st)
	})
	// 弹窗画在内容之上：和授权弹窗一样放在 view 末尾，才能盖住整扇窗口。
	a.composeModuleDialog(c, st)
	// 预览区里的配图同样能点开多图预览；预览是一扇独立窗口，这里只在图片
	// 到位后补开它（见 viewerTick）。
	a.viewerTick()
	// 缺权限时就在这个窗口里弹授权：发帖 / 发动态的请求是从这里发起的。
	a.authDialogFor(c, toastCompose)
}

// composeMomentBody 是动态模式：只有一段 1～500 字的正文，不显示标题、节点
// 与附加组件。结果以 toast 报告，失败原因留在框里。
func (a *app) composeMomentBody(c *ui.Context, st *composeState) {
	c.Key("compose-moment-scroll")
	list := layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
		ui.Column(c).FillWidth().Padding(18, 20).Gap(12).Children(func() {
			display.Text(c, "写一条动态", display.TextProps{Strong: true, FontSize: 14, Rows: 1})
			display.Text(c, "短内容，最多 500 字；不需要标题和节点。",
				display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
			momentEditor := composeSuggestEditor{
				body: &st.momentDraft, ed: &st.mdEditor,
				field: &st.momentField, font: composeSuggestFontSize,
				s: &st.momentSuggest,
			}
			momentArea := composeTextArea(c, &st.momentDraft, field.TextAreaProps{
				Placeholder: "此刻想说什么？",
				MinRows:     8,
				MaxRows:     12,
			}).Label("动态内容").MaxLength(composeMomentMax).Bind(&st.momentField)
			a.wireComposeSuggest(c, momentArea, momentEditor, st)
			count := utf8.RuneCountInString(st.momentDraft)
			ctype := display.TextSecondary
			if count >= composeMomentMax {
				ctype = display.TextDanger
			}
			display.Text(c, fmt.Sprintf("%d / %d 字", count, composeMomentMax),
				display.TextProps{Type: ctype, FontSize: 10, Rows: 1})
			if st.momentErr != "" {
				feedback.Alert(c, st.momentErr, feedback.AlertProps{
					Type: feedback.Error, Variant: feedback.Soft,
				})
			}
			if a.auth.loggedIn() && !a.hasAuthScope("write:topic") {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					display.Text(c, "当前授权还不能发动态，需要补充 write:topic 权限。",
						display.TextProps{Type: display.TextSecondary, FontSize: 12, Rows: 2}).Grow(1)
					if action.Button(c, "去授权", action.ButtonProps{
						Size: lobetheme.Small,
					}).Clicked() {
						a.demandAuthorizationFor(toastCompose, scopeNeed([]string{"write:topic"}))
					}
				})
			}
		})
	})
	list.Grow(1).MinHeight(0)
}

// composeTitleField 是写作区第一块：只有标题。节点放在最下面且是选填的
// （留空由站点的 AI 判定分类），匿名开关挪到右侧栏，打开窗口时标题就是唯一
// 要动手填的东西。
func (a *app) composeTitleField(c *ui.Context, st *composeState) {
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			display.Text(c, "标题", display.TextProps{Strong: true, FontSize: 12, Rows: 1}).Grow(1)
			display.Text(c, fmt.Sprintf("%d / %d 字", utf8.RuneCountInString(st.title), composeTitleMax),
				display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
		})
		in := composeInput(c, &st.title, field.InputProps{
			Placeholder: "一句话说清这帖讲什么",
		})
		in.Label("标题").MaxLength(composeTitleMax).Bind(&st.titleField)
		// 打开窗口就把焦点放进标题：写帖子的第一件事是给它起个名字。
		if st.focusTitle {
			st.titleField.Focus(c)
			st.focusTitle = false
		}
	})
}

// composeNodeSection 是写作区最下面的一块：节点是选填项，默认留空——发帖后
// 由站点的 AI 按标题与正文判定分类。想指定节点时用两列级联选择挑一个，面板
// 顶部与触发框里都能再清掉。列表读取失败或为空也只是少了一个可选项，仍然
// 可以照常发布。
func (a *app) composeNodeSection(c *ui.Context, st *composeState) {
	layout.Divider(c, layout.DividerProps{})
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					display.Text(c, "节点", display.TextProps{
						Type: display.TextSecondary, FontSize: 12, Rows: 1,
					})
					display.Tag(c, "选填", display.TagProps{Size: lobetheme.Small})
				})
				state := "留空即可：发布后由 AI 按内容判定分类。"
				switch node := st.selectedNode(); {
				case node != nil:
					state = "发布到：" + node.parentDisplay()
				case st.nodeID != "":
					// 选中过但节点表里已经没有了（缓存被换掉）：不要显示一串 uuid。
					state = "已选的节点已不在列表里，留空会让 AI 判定分类。"
				case st.nodeLoading:
					state = "正在读取节点…留空也能发布，由 AI 判定分类。"
				case st.nodeErr != "":
					state = "节点读取失败，留空发布时由 AI 判定分类。"
				case st.nodeLoaded:
					state = "没有可用的节点，留空发布时由 AI 判定分类。"
				}
				display.Text(c, state, display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 2,
				})
				// 选中节点后补一条简介：列表响应不带可靠的 description，
				// 单独读一次 GET /v1/nodes/:slug。失败只影响这条说明。
				if node := st.selectedNode(); node != nil && st.nodeDetailSlug == node.Slug {
					switch {
					case st.nodeDetailLoading:
						display.Text(c, "正在读取节点简介…", display.TextProps{
							Type: display.TextSecondary, FontSize: 11, Rows: 1,
						})
					case st.nodeDetailErr != "":
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							display.Text(c, "节点简介暂时不可用", display.TextProps{
								Type: display.TextDanger, FontSize: 11, Rows: 1,
							})
							if action.Button(c, "重试", action.ButtonProps{
								Size: lobetheme.Small,
							}).Clicked() {
								a.loadNodeDetail(st, node.Slug)
							}
						})
					case strings.TrimSpace(st.nodeDetail.Description) != "":
						display.Text(c, strings.TrimSpace(st.nodeDetail.Description),
							display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 3})
					}
				}
				a.ensureNodeDetail(st)
			})
		})
		if len(st.nodes) > 0 {
			options := nodeCascaderOptions(st.nodes)
			if len(options) > 0 {
				// 节点选填：面板顶部给出清除入口，清掉就回到「由 AI 判定」。
				// 空值用 nodeFilterNode + 空引用表示：触发框显示「由 AI 判定分类」，
				// 提交时 st.nodeID 仍是空串。
				value := nodeFilterOf(nodeRef{})
				if node := st.selectedNode(); node != nil {
					value = nodeFilterOf(nodeRefFromNode(*node))
				}
				nodeCascader(c, st.nodes, nodeCascadeProps{
					Label:      "节点",
					Value:      &value,
					Open:       &st.nodePick.open,
					ActiveRoot: &st.nodePick.active,
					Search:     &st.nodeSearch,
					AllowClear: true,
					ClearLabel: "清除节点选择",
					EmptyLabel: "由 AI 判定分类",
					OnPick: func(f nodeFilter) {
						st.nodeID = f.Ref.ID
					},
					OnClear: func() { st.nodeID = "" },
				})
				return
			}
		}
		// 节点还没到（或取不到）时不给一个空的级联选择：说明正在做什么，
		// 失败时把原因摆出来，并留一个重试入口。
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if st.nodeLoading {
				lobeSpin(c, feedback.SpinProps{CustomSize: 12})
			}
			msg := "节点列表还没准备好；留空发布时由 AI 判定分类。"
			if st.nodeLoading {
				msg = "正在读取节点…"
			}
			switch {
			case st.nodeErr != "":
				msg = "节点读取失败：" + st.nodeErr + "（不影响发布，留空由 AI 判定分类）"
			case st.nodeLoaded:
				msg = "没有可用的节点，留空发布时由 AI 判定分类。"
			}
			display.Text(c, msg, display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 2,
			}).Grow(1)
			if st.nodeErr != "" || st.nodeLoaded {
				if action.Button(c, "重试", action.ButtonProps{
					Size: lobetheme.Small,
				}).Clicked() {
					// 手动重试要跳过失败冷却，并立刻重新请求。
					st.nodesAt = time.Time{}
					st.nodeFailedAt = time.Time{}
					a.ensureComposeNodes()
				}
			}
		})
	})
}

// composeEditorSection 是正文区：上面是 Markdown 工具条与编辑 / 预览切换，
// 下面是输入框或预览，中间按需展开表情 / 图片面板。整个编辑框同时是拖放
// 区：文件落进来就交给上传队列，不必先点开图片面板。
func (a *app) composeEditorSection(c *ui.Context, st *composeState) {
	tk := lobetheme.TokensOf(c)
	// 刚粘进来的图片在这里落地：必须在构造输入框之前，和拖放一样，这一帧
	// 就要把占位符写进正文（见 drainPaste）。
	a.drainPaste(uploadTargetCompose)
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		// 标题行：区块名、编辑 / 预览切换与字数。切换放在工具条外面，因为
		// 工具条窄了会把末尾的控件收进「»」菜单——模式切换不该被藏起来。
		// 与评论区一样只画图标：编辑 / 预览是反复点的开关，图标比文字省地方，
		// 说明在悬浮提示里。
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			display.Text(c, "正文", display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 1,
			}).Grow(1)
			ui.Row(c).Gap(2).AlignItems(ui.Center).KeepFocus().
				Label("正文视图").Children(func() {
				a.editorIconToggle(c, st.preview == 0, editIcon, "编辑", "编辑模式",
					func() { st.preview = 0 })
				a.editorIconToggle(c, st.preview == 1, eyeIcon, "预览", "预览模式",
					func() { st.preview = 1 })
			})
			a.editorPanelButtons(c, &st.mdEditor, &st.body, nil, "表情", "上传图片")
			count := display.Text(c, fmt.Sprintf("%d / %d 字",
				utf8.RuneCountInString(st.body), composeContentMax),
				display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
			if n := utf8.RuneCountInString(st.body); n > composeContentMax {
				count.TextColor(tk.Error)
			}
		})
		zone := popupSurface(c, false).FillWidth()
		// 拖放区要在 Children 之前登记：这一帧就得把 flagDropTarget 记进
		// 节点，下一次 drop 事件才找得到它。悬停时只铺浅底，不画整圈描边。
		over := a.editorDropZone(zone, uploadTargetCompose)
		if over {
			zone.Background(tk.FillSecondary)
		}
		zone.Children(func() {
			// 工具条固定在输入框上方（「头部」），滚动正文时始终可见。
			ui.Row(c).FillWidth().Padding(2, 4).Children(func() {
				a.editorToolbar(c, &st.mdEditor, &st.body)
			})
			layout.Divider(c, layout.DividerProps{})
			a.editorPanels(c, uploadTargetCompose, &st.mdEditor, &st.body, nil)
			if over {
				display.Text(c, "松开即可把图片插入正文", display.TextProps{
					Type: display.TextInfo, FontSize: 11, Rows: 1,
				}).Padding(6, 10)
			}
			if st.preview == 1 {
				a.composePreview(c, st)
				return
			}
			// 打开表情 / 图片面板、拖入文件时的悬停提示都会在输入框之前加
			// 兄弟节点。用 key 固定输入框身份，否则焦点会随节点 ID 漂移，
			// 工具条插入后就没法接着打字了。复合控件要用 Context.Key 在
			// 构造之前给，不能用 Element.Key。
			c.Key("compose-body-area")
			// 可访问名用「正文内容」：区块标题已经叫「正文」，两者重名会让
			// 读屏与测试分不清点的是哪一块。
			// OnPaste：直接粘进来的图片（截图或图片文件）自动走图床，见
			// pasteIntoEditor；文字照常插入。
			area := composeTextArea(c, &st.body, field.TextAreaProps{
				Placeholder: "支持 Markdown：**加粗**、# 标题、> 引用、- 列表、`代码`、[链接](url)、![图片](url) 与 :smile: 表情；图片可直接拖进来或粘贴",
				MinRows:     composeEditorLines,
				MaxRows:     composeEditorMaxLine,
				Variant:     field.InputBorderless,
			})
			area.Label("正文内容").OnPaste(func(text string) bool {
				return a.pasteIntoEditor(uploadTargetCompose, text)
			}).Bind(&st.bodyField)
			bodyEditor := composeSuggestEditor{
				body: &st.body, ed: &st.mdEditor,
				field: &st.bodyField, font: composeSuggestFontSize,
				s: &st.bodySuggest,
			}
			a.wireComposeSuggest(c, area, bodyEditor, st)
		})
	})
}

// markdownButton 是工具条上的一项：图标（必填，工具条只画图标）、无障碍名
// 与悬浮提示用的名字、它插入的标记，以及悬浮提示里附带的标记示例。
// 悬浮提示写成「名字：标记」的形式（见 editorToolbar）。
type markdownButton struct {
	label  string
	hint   string
	format composeFormat
	icon   *ui.SVG
}

var markdownButtons = []markdownButton{
	{"加粗", "**加粗**", composeBold, boldIcon},
	{"斜体", "*斜体*", composeItalic, italicIcon},
	{"删除线", "~~删除线~~", composeStrike, strikeIcon},
	{"代码", "`行内代码`", composeInlineCode, inlineCodeIcon},
	{"标题", "## 标题", composeHeading, headingIcon},
	{"引用", "> 引用", composeQuote, quoteIcon},
	{"代码块", "```代码块```", composeCodeBlock, codeBlockIcon},
	{"列表", "- 列表项", composeBulletList, bulletListIcon},
	{"编号", "1. 编号列表", composeOrderedList, orderedListIcon},
	{"链接", "[文字](url)", composeLink, linkIcon},
	{"图片", "![说明](url)", composeImage, imageIcon},
	{"分割线", "---", composeDivider, dividerIcon},
}

// composePreview 是正文的预览：与详情页同一套 Markdown 渲染（一个回车换行、
// 表情与配图都按文章正文的方式画），所以预览里看到的就是发表后的样子。
// 发帖草稿只用全局表情表；社区自带的短码也在这张表里。
func (a *app) composePreview(c *ui.Context, st *composeState) {
	a.editorPreviewView(c, st.body, a.emojis, a.md, composePreviewHeight)
}

/* ------------------------------------------------------------------
   附加组件
---------------------------------------------------------------- */

// composeModuleSpec 是右侧栏里的一项。flag 指向「这一块随帖发送」的开关，
// body 画弹窗里的字段；侧栏与弹窗共用同一张表，加组件时不用改两处。
// tag 是这一块在标题里对应的【】关键词（网页端加扩展时自动加、移除时自动删）；
// review 表示它用《X》点评：N 分｜ 而不是【】标签写标题（网页端的「点评」）。
type composeModuleSpec struct {
	name   string
	hint   string
	tag    string
	review bool
	flag   func(*composeState) *bool
	body   func(*app, *ui.Context, *composeState)
}

// composeModuleSpecs 是发帖接口支持的全部可选块，顺序就是侧栏里的顺序。
// 默认一个都不启用：没点「添加此组件」的块，一个字段都不会进请求体。
var composeModuleSpecs = []composeModuleSpec{
	{name: "投票", hint: "给帖子加一个单选或多选投票。", tag: "投票",
		flag: func(st *composeState) *bool { return &st.voteOpen },
		body: func(a *app, c *ui.Context, st *composeState) { a.composeVote(c, st) }},
	{name: "评分", hint: "给某个对象打分并附上理由。", review: true,
		flag: func(st *composeState) *bool { return &st.scoreOpen },
		body: func(a *app, c *ui.Context, st *composeState) { a.composeScore(c, st) }},
	{name: "竞猜", hint: "发起一场竞猜，让读者选边。", tag: "竞猜",
		flag: func(st *composeState) *bool { return &st.betOpen },
		body: func(a *app, c *ui.Context, st *composeState) { a.composeBet(c, st) }},
	{name: "兑换码 / 红包", hint: "发兑换码，或开一个随帖红包。", tag: "兑换码",
		flag: func(st *composeState) *bool { return &st.redeemOpen },
		body: func(a *app, c *ui.Context, st *composeState) { a.composeRedeem(c, st) }},
	{name: "金币池", hint: "放一笔金币，读者浏览时按概率领取。", tag: "💰",
		flag: func(st *composeState) *bool { return &st.poolOpen },
		body: func(a *app, c *ui.Context, st *composeState) { a.composePool(c, st) }},
	{name: "可见性（谁可以看）", hint: "限制谁能读这篇帖子。",
		flag: func(st *composeState) *bool { return &st.guardOpen },
		body: func(a *app, c *ui.Context, st *composeState) { a.composeGuard(c, st) }},
	{name: "隐藏内容", hint: "付费 / 属性检定 / 竞拍才能解锁的正文。", tag: "🔒",
		flag: func(st *composeState) *bool { return &st.restrictedOpen },
		body: func(a *app, c *ui.Context, st *composeState) { a.composeRestricted(c, st) }},
}

// composeApplyModuleTitle 按网页端规则同步标题：加组件就插【关键词】/点评前缀，
// 移除就删掉。评分可能反复打开改分值，所以 on 为真时也重算点评前缀（先剥旧的）。
func composeApplyModuleTitle(st *composeState, m composeModuleSpec, on bool) {
	if m.tag != "" {
		st.title = composeSetTitleTag(st.title, m.tag, on)
	}
	if m.review {
		if !on {
			st.title = composeStripReviewPrefix(st.title)
		} else if target := strings.TrimSpace(st.scoreTarget); target != "" {
			st.title = composeWithReviewPrefix(st.title, target, int(st.scoreValue))
		}
	}
}

// enabledModuleCount 是侧栏底部那行汇总用的数量。
func (st *composeState) enabledModuleCount() int {
	n := 0
	for _, m := range composeModuleSpecs {
		if *m.flag(st) {
			n++
		}
	}
	return n
}

// composeAnonymousToggle 是匿名开关。网页端规定匿名帖不能用金币池、竞猜与
// 隐藏内容：勾上匿名时，这几块连同标题里对应的【💰】【竞猜】【🔒】一起撤掉，
// 免得发出去被服务端拒绝。关闭匿名不自动恢复——草稿与开关状态都还在。
func (a *app) composeAnonymousToggle(c *ui.Context, st *composeState) {
	before := st.anonymous
	ui.Row(c).FillWidth().Children(func() {
		composeToggle(c, &st.anonymous, "匿名发帖")
	})
	if before || !st.anonymous {
		return
	}
	dropped := 0
	for _, m := range composeModuleSpecs {
		if !*m.flag(st) || (m.tag != "💰" && m.tag != "竞猜" && m.tag != "🔒") {
			continue
		}
		composeApplyModuleTitle(st, m, false)
		*m.flag(st) = false
		dropped++
	}
	// 手工写进标题的【🔒】也要清掉：隐藏内容没作为组件启用时，上面的循环
	// 不会经过它。已启用时 composeApplyModuleTitle 已经清过，再调一次无害。
	st.title = composeSetTitleTag(st.title, "🔒", false)
	if dropped > 0 {
		a.toast(toastCompose, "匿名发帖已移除金币池、竞猜、隐藏内容，及相关标题标签")
	}
}

// composeSidebar 是窗口右侧常驻的控制栏：上半是匿名等发布设置，下半是各块
// 附加组件。点一行在弹窗里编辑；已添加的行尾直接放「移除」，不必为了移除再
// 进一次弹窗，底部还有「全部移除」一次清掉所有组件。
func (a *app) composeSidebar(c *ui.Context, st *composeState) {
	tk := lobetheme.TokensOf(c)
	ui.Column(c).Width(composeSidebarWidth).FillHeight().MinHeight(0).
		Background(tk.BgContainer).Padding(14, 12).Gap(8).Children(func() {
		display.Text(c, "发布设置", display.TextProps{Strong: true, FontSize: 13, Rows: 1})
		a.composeAnonymousToggle(c, st)
		display.Text(c, "匿名只随这篇帖子生效。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
		layout.Divider(c, layout.DividerProps{})
		display.Text(c, "附加组件", display.TextProps{Strong: true, FontSize: 13, Rows: 1})
		display.Text(c, "选填。点一行，在弹窗里设置好再添加。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 3})
		c.Key("compose-side-scroll")
		list := layout.ScrollArea(c, layout.ScrollAreaProps{}, func() {
			ui.Column(c).FillWidth().Gap(6).Children(func() {
				for i := range composeModuleSpecs {
					m := composeModuleSpecs[i]
					index := i
					on := *m.flag(st)
					ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
						// 整行是扁平列表行：不画圆角与整圈描边，只留底部分隔线；
						// 启用时左侧一条主色竖条标出，切换时几何不会跳。
						setup := popupRow(c, true).
							Grow(1).MinWidth(0).Row().AlignItems(ui.Center).Padding(8, 10)
						if on {
							setup.BorderStartWidth(3).BorderColor(tk.Primary)
						}
						setup.Children(func() {
							display.Text(c, m.name, display.TextProps{
								Strong: true, FontSize: 12, Rows: 1,
							}).Grow(1)
							mark, fg := "设置", display.TextSecondary
							if on {
								mark, fg = "已添加", display.TextSuccess
							}
							display.Text(c, mark, display.TextProps{Type: fg, FontSize: 11, Rows: 1})
						})
						setup.Label(m.name).Tooltip(m.hint)
						if setup.Clicked() {
							st.moduleIndex = index
							st.moduleOpen = true
						}
						if on {
							// 移除就在行尾：不用再打开弹窗，填过的内容仍留在草稿里。
							// 行尾位置窄，用废纸篓图标，说明与无障碍名一起给出移除的是谁。
							if iconActionButton(c, trashIcon, "移除"+m.name,
								"移除"+m.name+"，填过的内容仍留在草稿里").Clicked() {
								*m.flag(st) = false
								composeApplyModuleTitle(st, m, false)
								a.toast(toastCompose, "已移除"+m.name)
							}
						}
					})
				}
			})
		})
		list.Grow(1).MinHeight(0)
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			display.Text(c, fmt.Sprintf("已添加 %d / %d 项", st.enabledModuleCount(),
				len(composeModuleSpecs)), display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			}).Grow(1)
			if st.enabledModuleCount() > 0 {
				if action.Button(c, "全部移除", action.ButtonProps{
					Size: lobetheme.Small,
				}).Label("全部移除附加组件").Clicked() {
					for _, m := range composeModuleSpecs {
						if *m.flag(st) {
							composeApplyModuleTitle(st, m, false)
						}
						*m.flag(st) = false
					}
					a.toast(toastCompose, "已移除全部附加组件")
				}
			}
		})
	})
}

// composeModuleDialog 是点侧栏某一行之后弹出的编辑窗。字段和以前折叠面板里
// 的一模一样，只是搬进了弹窗：启不启用由 Lobe Modal 的确认按钮决定，编辑中
// 的内容一直留在草稿里，关掉弹窗再点进来还在。移除不在这里做——侧栏对应行
// 的行尾直接有「移除」。点窗外或按 Esc 只关弹窗，不关窗口。
func (a *app) composeModuleDialog(c *ui.Context, st *composeState) {
	if !st.moduleOpen || st.moduleIndex < 0 || st.moduleIndex >= len(composeModuleSpecs) {
		return
	}
	m := composeModuleSpecs[st.moduleIndex]
	on := *m.flag(st)
	label := "添加此组件"
	if on {
		label = "完成"
	}
	// 标题栏、✕ 关闭、取消/确认按钮与「内容可滚动、页脚常驻」都由 Lobe 的
	// Modal 提供，和主页、详情页用的是同一套 token 与动画。这里只画这块
	// 组件自己的字段和启用状态；确认返回 true 时再落账并关窗。
	ok := feedback.Modal(c, &st.moduleOpen, feedback.ModalProps{
		Title:      m.name,
		Width:      composeModuleDialogWidth,
		OkText:     label,
		CancelText: "取消",
	}, func() {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			state := "未添加：不会随帖发送"
			if on {
				state = "已添加：会随帖一起发送；移除在右侧栏这一行的行尾。"
			}
			display.Text(c, state, display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 2,
			})
			layout.Divider(c, layout.DividerProps{})
			display.Text(c, m.hint, display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 3,
			})
			layout.Divider(c, layout.DividerProps{})
			m.body(a, c, st)
			// 最后一行不要紧贴滚动区下沿：看起来像被截断。
			ui.Spacer(c).Height(8)
		})
	})
	if ok {
		if !on {
			*m.flag(st) = true
			a.toast(toastCompose, "已添加"+m.name)
		}
		// 加组件时按网页端规则给标题插【关键词】/点评前缀；已添加的块再次
		// 点「完成」也重算一次（评分可能改了对象或分值）。
		composeApplyModuleTitle(st, m, true)
		st.moduleOpen = false
	}
}

// composeOptionList 是所有「可以有多个选项」的地方共用的编辑区。操作行放在
// 最上面：「添加选项」「批量添加」「删除选中」都在显眼处；批量添加展开的内联
// 录入区紧跟其后，弹窗不用滚动就能点「加入选项」。下面才是逐行的输入框、
// 行首勾选框与行尾「删除」。增删都卡在 minCount～maxCount 之间，操作结果
// 一律用窗口内 toast 报告。
func (a *app) composeOptionList(c *ui.Context, opts *[]string, checked *[]bool,
	batchOpen *bool, batchText *string, label string, minCount, maxCount, maxLen int) {
	*checked = syncComposeChecks(*checked, len(*opts))
	nChecked := 0
	for _, on := range *checked {
		if on {
			nChecked++
		}
	}
	batchToggled, removeChecked := false, false
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		add := action.Button(c, "添加选项", action.ButtonProps{Size: lobetheme.Small}).
			Disabled(len(*opts) >= maxCount)
		if add.Clicked() {
			*opts = append(*opts, "")
			*checked = append(*checked, false)
		}
		if action.Button(c, "批量添加", action.ButtonProps{Size: lobetheme.Small}).
			Tooltip("一次粘贴多行，每行一个选项").Clicked() {
			batchToggled = true
		}
		ui.Spacer(c).Grow(1)
		// 批量删除在行尾，用废纸篓图标：勾选数写进 Tooltip，按钮本身不占文字宽度。
		drop := iconActionButton(c, trashIcon, "删除选中",
			fmt.Sprintf("删除勾选的 %d 个%s", nChecked, label))
		drop.Disabled(nChecked == 0)
		if drop.Clicked() {
			removeChecked = true
		}
	})
	if batchToggled {
		*batchOpen = !*batchOpen
	}
	// 批量删除在画选项行之前落账，这一帧看到的就是删完的列表。
	if removeChecked {
		next, flags, removed, held := removeComposeChecked(*opts, *checked, minCount)
		*opts, *checked = next, flags
		msg := fmt.Sprintf("已删除 %d 个%s", removed, label)
		if held {
			msg += fmt.Sprintf("；至少保留 %d 个", minCount)
		}
		a.toast(toastCompose, msg)
	}
	if *batchOpen {
		popupSurface(c, false).FillWidth().Padding(8, 10).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				display.Text(c, "批量添加：一行一个，可直接粘贴多行",
					display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1}).Grow(1)
				if action.Button(c, "取消", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
					*batchOpen = false
					*batchText = ""
				}
				if action.Button(c, "加入选项", action.ButtonProps{
					Size: lobetheme.Small, Variant: action.Primary,
				}).Clicked() {
					lines := composeBatchLines(*batchText)
					if len(lines) == 0 {
						a.toast(toastCompose, "请先粘贴要添加的"+label)
						return
					}
					next, added, skipped := appendComposeBatch(*opts, lines, minCount, maxCount)
					*opts = next
					*checked = syncComposeChecks(*checked, len(next))
					*batchOpen = false
					*batchText = ""
					msg := fmt.Sprintf("已批量添加 %d 个%s", added, label)
					if skipped > 0 {
						msg += fmt.Sprintf("，超出上限省略 %d 个", skipped)
					}
					a.toast(toastCompose, msg)
				}
			})
			display.Text(c, fmt.Sprintf("最多 %d 项", maxCount),
				display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
			composeTextArea(c, batchText, field.TextAreaProps{
				Placeholder: "第一行\n第二行\n第三行",
				MinRows:     4,
				MaxRows:     12,
			}).Label(label + "批量录入")
		})
	}
	removeAt := -1
	for i := range *opts {
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			choice.Checkbox(c, &(*checked)[i], "", choice.CheckboxProps{}).Shrink(0).
				Label(fmt.Sprintf("勾选%s %d", label, i+1)).
				Tooltip("勾选后可批量删除")
			composeInput(c, &(*opts)[i], field.InputProps{
				Placeholder: fmt.Sprintf("%s %d", label, i+1),
			}).Label(fmt.Sprintf("%s %d", label, i+1)).MaxLength(maxLen)
			// 行尾删除同样是图标按钮：无障碍名带上第几项，读屏与点击测试都能区分。
			remove := iconActionButton(c, trashIcon,
				fmt.Sprintf("删除%s %d", label, i+1),
				fmt.Sprintf("删掉%s %d；至少保留 %d 项", label, i+1, minCount))
			remove.Disabled(len(*opts) <= minCount)
			if remove.Clicked() {
				removeAt = i
			}
		})
	}
	// 正在构建的集合留到循环结束后再改，避免遍历到一半行数变了。
	if removeAt >= 0 {
		*opts = append((*opts)[:removeAt], (*opts)[removeAt+1:]...)
		*checked = append((*checked)[:removeAt], (*checked)[removeAt+1:]...)
	}
}

// composeVote 是投票块：类型（pk / multiple）、标题（≤16 字）、1～20 个选项、
// 是否公开结果、可选截止时间与多选时最多可选几项。
func (a *app) composeVote(c *ui.Context, st *composeState) {
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		display.Text(c, "投票类型", display.TextProps{
			Type: display.TextSecondary, FontSize: 12, Rows: 1,
		})
		action.Segmented(c, &st.voteTypeIndex, []string{"单选", "多选"},
			action.SegmentedProps{}).Label("投票类型")
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			display.Text(c, "投票标题", display.TextProps{FontSize: 12, Rows: 1})
			composeInput(c, &st.voteTitle, field.InputProps{
				Placeholder: fmt.Sprintf("最多 %d 字", composeVoteTitleMax),
			}).Label("投票标题")
		})
		// 选项是动态行，支持逐行增删、批量粘贴与勾选批量删除；行数受
		// 1～20 限制（见 composeOptionList）。
		a.composeOptionList(c, &st.voteOptions, &st.voteChecked,
			&st.voteBatchOpen, &st.voteBatchText,
			"投票选项", 1, composeOptionMax, composeOptionTextMax)
		ui.Row(c).FillWidth().Children(func() {
			composeToggle(c, &st.voteResultVisible, "公开投票结果")
			ui.Spacer(c).Grow(1)
		})
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.End).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				composeDeadlineField(c, &st.voteDeadlineOn, &st.voteDeadlineAt, "投票")
			})
			if voteTypeAt(st.voteTypeIndex) == "multiple" {
				ui.Column(c).Width(150).Gap(4).Children(func() {
					display.Text(c, "最多可选", display.TextProps{
						Type: display.TextSecondary, FontSize: 12, Rows: 1,
					})
					composeNumber(c, &st.voteMaxChoices, field.InputNumberProps{
						Min: 1, Max: composeOptionMax, Step: 1,
					}).Label("最多可选几项")
				})
			}
		})
	})
}

// composeScore 是评分块：对象名、满分 10 分的星级评分与可选理由。
func (a *app) composeScore(c *ui.Context, st *composeState) {
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			display.Text(c, "评分对象", display.TextProps{FontSize: 12, Rows: 1})
			composeInput(c, &st.scoreTarget, field.InputProps{
				Placeholder: fmt.Sprintf("最多 %d 字", composeScoreTargetMax),
			}).Label("评分对象")
		})
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			display.Text(c, "分值", display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 1,
			})
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				choice.Rate(c, &st.scoreValue, choice.RateProps{Count: 10}).Label("分值（满分 10 分）")
				display.Text(c, fmt.Sprintf("%g / 10", st.scoreValue), display.TextProps{
					Strong: true, FontSize: 13, Rows: 1,
				})
			})
		})
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			display.Text(c, "理由", display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 1,
			})
			composeInput(c, &st.scoreComment, field.InputProps{
				Placeholder: "可选",
			}).Label("评分理由").MaxLength(composeScoreCommentMax)
		})
	})
}

// composeBet 是竞猜块：标题、2～6 个选项与可选参与截止时间。
func (a *app) composeBet(c *ui.Context, st *composeState) {
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			display.Text(c, "竞猜标题", display.TextProps{FontSize: 12, Rows: 1})
			composeInput(c, &st.betTitle, field.InputProps{
				Placeholder: fmt.Sprintf("最多 %d 字", composeBetTitleMax),
			}).Label("竞猜标题")
		})
		// 竞猜选项与投票共用一套动态行：2～6 项，批量入口也在里面。
		a.composeOptionList(c, &st.betOptions, &st.betChecked,
			&st.betBatchOpen, &st.betBatchText,
			"竞猜选项", composeBetOptionsMin, composeBetOptionsMax, composeBetOptionMax)
		composeDeadlineField(c, &st.betDeadlineOn, &st.betDeadlineAt, "竞猜")
	})
}

// composeRedeem 是兑换码 / 红包块：一行一个兑换码（本身就支持批量），另有
// 「批量粘贴」一次追加多行、「清空」一次移除全部，加上说明、每人可领数量
// 与可选的红包。红包金额只有文档给出的六档，用分段控件选。
func (a *app) composeRedeem(c *ui.Context, st *composeState) {
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				display.Text(c, "兑换码（一行一个）", display.TextProps{
					Type: display.TextSecondary, FontSize: 12, Rows: 1,
				}).Grow(1)
				if action.Button(c, "批量粘贴", action.ButtonProps{Size: lobetheme.Small}).
					Label("批量粘贴兑换码").
					Tooltip("一次粘贴多行，追加到兑换码列表").Clicked() {
					st.redeemBatchOpen = !st.redeemBatchOpen
				}
				clear := action.Button(c, "清空", action.ButtonProps{Size: lobetheme.Small}).
					Label("清空兑换码").Tooltip("清空全部兑换码").Disabled(st.redeemCodes == "")
				if clear.Clicked() {
					st.redeemCodes = ""
					st.redeemBatchText = ""
					a.toast(toastCompose, "已清空兑换码")
				}
			})
			composeTextArea(c, &st.redeemCodes, field.TextAreaProps{
				Placeholder: "一行一个兑换码，也可以用逗号分隔",
				MinRows:     4,
				MaxRows:     10,
			}).Label("兑换码").MaxLength(composeCodeMax * (composeCodeTextMax + 1))
			display.Text(c, "支持一次粘贴多行，或用逗号 / 分号分隔；上限 500 个。",
				display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
		})
		if st.redeemBatchOpen {
			a.composeRedeemBatch(c, st)
		}
		ui.Column(c).Gap(4).Children(func() {
			display.Text(c, "兑换说明", display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 1,
			})
			composeTextArea(c, &st.codeDescription, field.TextAreaProps{
				Placeholder: "可选",
				MinRows:     2,
				MaxRows:     5,
			}).Label("兑换说明").MaxLength(composeCodeDescMax)
		})
		ui.Row(c).Gap(10).AlignItems(ui.End).Children(func() {
			ui.Column(c).Width(180).Gap(4).Children(func() {
				display.Text(c, "每人可领", display.TextProps{
					Type: display.TextSecondary, FontSize: 12, Rows: 1,
				})
				composeNumber(c, &st.maxRedeemPerUser, field.InputNumberProps{
					Min: composeRedeemPerUserLo, Max: composeRedeemPerUserHi, Step: 1,
				}).Label("每人可领数量")
			})
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				display.Text(c, "红包上限", display.TextProps{
					Type: display.TextSecondary, FontSize: 12, Rows: 1,
				})
				action.Segmented(c, &st.redPacketIndex, redPacketLabels,
					action.SegmentedProps{}).Label("红包金额上限")
			})
		})
		ui.Row(c).FillWidth().Children(func() {
			composeToggle(c, &st.redPacket, "同时开红包")
			ui.Spacer(c).Grow(1)
		})
	})
}

// composeRedeemBatch 是兑换码的批量入口：粘贴一段多行文本，一次追加到兑换码
// 列表里。空行自动丢掉，超过 500 个的部分省略，结果用 toast 报告。
func (a *app) composeRedeemBatch(c *ui.Context, st *composeState) {
	popupSurface(c, false).FillWidth().Padding(8, 10).Children(func() {
		// 按钮放在录入区最上面：弹窗不用滚动就能点「追加兑换码」。
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			display.Text(c, fmt.Sprintf("批量粘贴兑换码（已有 %d / %d 个）",
				len(st.redeemCodeList()), composeCodeMax), display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 2,
			}).Grow(1)
			if action.Button(c, "取消", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				st.redeemBatchOpen = false
				st.redeemBatchText = ""
			}
			if action.Button(c, "追加兑换码", action.ButtonProps{
				Size: lobetheme.Small, Variant: action.Primary,
			}).Clicked() {
				lines := composeBatchLines(st.redeemBatchText)
				if len(lines) == 0 {
					a.toast(toastCompose, "请先粘贴要添加的兑换码")
					return
				}
				room := composeCodeMax - len(st.redeemCodeList())
				if room < 0 {
					room = 0
				}
				added := min(room, len(lines))
				st.redeemCodes = strings.TrimSpace(st.redeemCodes)
				if st.redeemCodes != "" {
					st.redeemCodes += "\n"
				}
				st.redeemCodes += strings.Join(lines[:added], "\n")
				skipped := len(lines) - added
				st.redeemBatchOpen = false
				st.redeemBatchText = ""
				msg := fmt.Sprintf("已批量添加 %d 个兑换码", added)
				if skipped > 0 {
					msg += fmt.Sprintf("，超出上限省略 %d 个", skipped)
				}
				a.toast(toastCompose, msg)
			}
		})
		display.Text(c, "一行一个，可直接粘贴多行；空行会丢掉。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
		composeTextArea(c, &st.redeemBatchText, field.TextAreaProps{
			Placeholder: "CODE-1\nCODE-2\nCODE-3",
			MinRows:     4,
			MaxRows:     12,
		}).Label("兑换码批量录入")
	})
}

// composePool 是金币池块：初始金币（1～1000000）与可选的中奖概率（0.1～0.5）。
// 概率留 0 表示不发送，由服务端用默认值。
func (a *app) composePool(c *ui.Context, st *composeState) {
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		display.Text(c, "发帖时先往帖子里的金币池放一笔金币，读者浏览时按概率领取。",
			display.TextProps{Type: display.TextSecondary, FontSize: 12, Rows: 3})
		ui.Row(c).Gap(10).AlignItems(ui.End).Children(func() {
			ui.Column(c).Width(200).Gap(4).Children(func() {
				display.Text(c, "初始金币", display.TextProps{
					Type: display.TextSecondary, FontSize: 12, Rows: 1,
				})
				composeNumber(c, &st.poolCoins, field.InputNumberProps{
					Min: composePoolCoinsLo, Max: composePoolCoinsHi, Step: 10,
				}).Label("金币池初始金币")
			})
			ui.Column(c).Width(180).Gap(4).Children(func() {
				display.Text(c, "中奖概率（0 = 默认）", display.TextProps{
					Type: display.TextSecondary, FontSize: 12, Rows: 1,
				})
				composeNumber(c, &st.poolProbability, field.InputNumberProps{
					Min: 0, Max: composePoolProbHi, Step: 0.1,
				}).Label("中奖概率")
			})
		})
	})
}

// composeGuard 是「谁可以看」：不限制 / 需登录 / 达到等级 / 满足 D20 属性。
// 文档里的 access_guard_type 就是这四种（不限制时不发字段）。
func (a *app) composeGuard(c *ui.Context, st *composeState) {
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			display.Text(c, "可见范围", display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 1,
			})
			options := make([]field.SelectOption, 0, len(guardTypeLabels))
			for _, l := range guardTypeLabels {
				options = append(options, field.SelectOption{Value: l, Label: l})
			}
			composeInputRow(c, func() ui.Element {
				return field.Select(c, &st.guardChoice, field.SelectProps{Options: options})
			}).Label("可见范围")
		})
		switch guardFromLabel(st.guardChoice) {
		case guardLevel:
			ui.Column(c).Width(200).Gap(4).Children(func() {
				display.Text(c, "最低等级（1～100）", display.TextProps{
					Type: display.TextSecondary, FontSize: 12, Rows: 1,
				})
				composeNumber(c, &st.guardLevel, field.InputNumberProps{
					Min: composeGuardLevelLo, Max: composeGuardLevelHi, Step: 1,
				}).Label("最低可见等级")
			})
		case guardAttribute:
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.End).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					display.Text(c, "D20 属性", display.TextProps{
						Type: display.TextSecondary, FontSize: 12, Rows: 1,
					})
					attrs := make([]field.SelectOption, 0, len(d20Attrs))
					for _, at := range d20Attrs {
						attrs = append(attrs, field.SelectOption{Value: at.Value, Label: at.Label})
					}
					composeInputRow(c, func() ui.Element {
						return field.Select(c, &st.guardAttr, field.SelectProps{Options: attrs})
					}).Label("D20 属性")
				})
				ui.Column(c).Width(160).Gap(4).Children(func() {
					display.Text(c, "属性下限（0～10）", display.TextProps{
						Type: display.TextSecondary, FontSize: 12, Rows: 1,
					})
					composeNumber(c, &st.guardAttrValue, field.InputNumberProps{
						Min: 0, Max: composeGuardAttrHi, Step: 1,
					}).Label("属性下限")
				})
			})
		}
		display.Text(c, "门槛由服务端判定；属性取自官网 D20 属性字典。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
	})
}

// composeRestricted 是隐藏内容块：解锁方式、隐藏正文、有效期，以及对应解锁
// 方式的字段。机器取值与校验范围都在 compose.go 的 restrictedPayload。
func (a *app) composeRestricted(c *ui.Context, st *composeState) {
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			display.Text(c, "解锁方式", display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 1,
			})
			options := make([]field.SelectOption, 0, len(restrictedAccessLabels))
			for _, l := range restrictedAccessLabels {
				options = append(options, field.SelectOption{Value: l, Label: l})
			}
			composeInputRow(c, func() ui.Element {
				return field.Select(c, &st.restrictedAccessChoice, field.SelectProps{Options: options})
			}).Label("解锁方式")
		})
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			display.Text(c, "隐藏正文（Markdown）", display.TextProps{FontSize: 12, Rows: 1})
			composeTextArea(c, &st.restrictedContent, field.TextAreaProps{
				Placeholder: "满足解锁条件后才可见",
				MinRows:     4,
				MaxRows:     10,
			}).Label("隐藏正文").MaxLength(composeRestrictedContentMax)
		})
		switch restrictedAccessFromLabel(st.restrictedAccessChoice) {
		case restrictedAccessD20:
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.End).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					display.Text(c, "D20 属性", display.TextProps{
						Type: display.TextSecondary, FontSize: 12, Rows: 1,
					})
					attrs := make([]field.SelectOption, 0, len(d20Attrs))
					for _, at := range d20Attrs {
						attrs = append(attrs, field.SelectOption{Value: at.Value, Label: at.Label})
					}
					composeInputRow(c, func() ui.Element {
						return field.Select(c, &st.restrictedD20Attr, field.SelectProps{Options: attrs})
					}).Label("D20 属性")
				})
				ui.Column(c).Width(150).Gap(4).Children(func() {
					display.Text(c, "目标值（1～25）", display.TextProps{
						Type: display.TextSecondary, FontSize: 12, Rows: 1,
					})
					composeNumber(c, &st.restrictedD20Threshold, field.InputNumberProps{
						Min: composeRestrictedD20Lo, Max: composeRestrictedD20Hi, Step: 1,
					}).Label("属性检定目标值")
				})
			})
		case restrictedAccessAuction:
			ui.Column(c).Width(180).Gap(4).Children(func() {
				display.Text(c, "前 N 名可解锁", display.TextProps{
					Type: display.TextSecondary, FontSize: 12, Rows: 1,
				})
				composeNumber(c, &st.restrictedAuctionTopN, field.InputNumberProps{
					Min: composeRestrictedAuctionTopLo, Max: 1000, Step: 1,
				}).Label("竞拍解锁人数")
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.DateInput(c, &st.restrictedAuctionDeadline).Label("竞拍截止日期")
				ui.TimeInput(c, &st.restrictedAuctionDeadline).Label("竞拍截止时间")
			})
		}
		ui.Column(c).Width(200).Gap(4).Children(func() {
			display.Text(c, "有效期（月，0 = 不填）", display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 1,
			})
			composeNumber(c, &st.restrictedDurationMonths, field.InputNumberProps{
				Min: 0, Max: composeRestrictedDurationHi, Step: 1,
			}).Label("隐藏内容有效期")
		})
		display.Text(c, "隐藏内容涉及金币规则，发布后不可撤销。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
	})
}

/* ------------------------------------------------------------------
   提示与底部操作
---------------------------------------------------------------- */

// composeMessages 是表单下方的回执与错误：发表成功后给一条可点开的回执，
// 失败时把服务端的原因原样显示（friendlyError 已经翻译成中文）。
func (a *app) composeMessages(c *ui.Context, st *composeState) {
	if st.err != "" {
		feedback.Alert(c, st.err, feedback.AlertProps{
			Type: feedback.Error, Variant: feedback.Soft, Selectable: true,
		})
	}
	// 发表需要 write:topic，默认授权里没有它：提前说明，免得写完才被拦。
	if a.auth.loggedIn() && !a.hasAuthScope("write:topic") {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			display.Text(c, "当前授权还不能发帖，需要补充 write:topic 权限。",
				display.TextProps{Type: display.TextSecondary, FontSize: 12, Rows: 2}).Grow(1)
			if action.Button(c, "去授权", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.demandAuthorizationFor(toastCompose, scopeNeed([]string{"write:topic"}))
			}
		})
	}
	if st.okShortID == "" {
		return
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		display.Text(c, composeReceiptText(st), display.TextProps{
			Type: display.TextSuccess, FontSize: 12, Rows: 2,
		}).Grow(1)
		if action.Button(c, "在应用内打开", action.ButtonProps{
			Size: lobetheme.Small,
		}).Clicked() {
			it := Item{ShortID: st.okShortID, URL: st.okURL}
			a.closeComposeWindow()
			// 帖子是在主窗口里打开的：走 publish，主窗口才会按新页面重绘。
			a.publish(func() { a.openDetail(it) })
		}
		if st.okURL != "" {
			if action.Button(c, "看原文", action.ButtonProps{
				Size: lobetheme.Small,
			}).Clicked() {
				a.srv.OpenURL(st.okURL)
			}
		}
	})
}

// composeNodePending 判断创建响应是不是还需要等 AI 分区：响应明确给了
// nebula，或者用户没选节点而响应又没带 node。选了节点但响应缺节点时不猜。
func composeNodePending(created createdTopic, nodeChosen bool) bool {
	return created.NodeSlug == nebulaNodeSlug || (!nodeChosen && created.NodeSlug == "")
}

// composeReceiptText 是发表成功那一行的文案：节点还在由 AI 分配时补一句
// 「节点分配中…」，分好后由 topic_node.go 换回普通回执。
func composeReceiptText(st *composeState) string {
	if st == nil {
		return ""
	}
	text := "已发表：" + st.okTitle
	if st.okPending {
		text += "（节点分配中…）"
	}
	return text
}

// composeFooter 是窗口底部固定的一条：左边是草稿状态，右边是「发布」。
// 动态模式复用同一条，只换提示、清空范围与按钮文字。
func (a *app) composeFooter(c *ui.Context, st *composeState) {
	moment := st.mode == composeModeMoment
	busy := st.posting
	if moment {
		busy = st.momentBusy
	}
	ui.Row(c).FillWidth().Padding(10, 16).Gap(10).AlignItems(ui.Center).Children(func() {
		if busy {
			lobeSpin(c, feedback.SpinProps{CustomSize: 14})
			display.Text(c, "正在发布…", display.TextProps{
				Type: display.TextSecondary, FontSize: 12, Rows: 1,
			})
		} else {
			hint := "Ctrl+Enter 发布"
			if moment {
				hint = "Ctrl+Enter 发布动态"
			}
			if !a.auth.loggedIn() {
				hint = "发帖需要登录 2Libra"
				if moment {
					hint = "发动态需要登录 2Libra"
				}
			}
			display.Text(c, hint, display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		}
		ui.Spacer(c).Grow(1)
		// 「清空」退成低调的文字按钮，让旁边的强调色「发布」成为唯一的视觉
		// 重心；发布在途时直接禁用，省得点了没反应还要靠提示解释。
		clear := action.Button(c, "清空", action.ButtonProps{
			Variant: action.Text,
		}).Tooltip("清空当前草稿，保留节点选择").Disabled(busy)
		if clear.Clicked() {
			id := st.draftID
			if moment {
				st.resetMomentForm()
			} else {
				// 清空草稿与各组件，但保留节点选择与已经拉到的节点列表。
				st.resetComposeForm()
			}
			// 表单清空了，草稿箱里那条也得删；不然下一次自动保存又写回来。
			if id != "" {
				a.deleteDraft(id)
			}
		}
		// 「当前账号」与「发布」贴在一起：多账号时点下去之前就能看清是哪个
		// 账号发出去（见 account_badge.go）。发布按钮带图标，用的是和主页悬浮
		// 「发帖」按钮同一枚 SquarePen（见 composePostIcon）。
		ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			a.accountBadge(c)
			label := "发布"
			if moment {
				label = "发布动态"
			}
			publish := composePublishButton(c, label).Disabled(busy)
			if publish.Clicked() {
				if moment {
					a.submitMoment()
				} else {
					a.submitCompose()
				}
			}
		})
	})
}

/* ------------------------------------------------------------------
   发布
---------------------------------------------------------------- */

// submitCompose 校验并发出 POST /v1/topics。失败时先交给全局授权链路：缺
// write:topic 会弹授权窗，授权成功后由用户自己再点发送，
// 用户不用把写好的正文丢掉重来。
func (a *app) submitCompose() {
	st := a.compose
	if st == nil || st.posting {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLoginWithScopesFor(toastCompose, "登录 2Libra 后才能发帖。", []string{"write:topic"})
		return
	}
	payload, err := st.payload()
	if err != nil {
		st.err = err.Error()
		st.okShortID, st.okURL, st.okTitle = "", "", ""
		return
	}
	// 幂等键跟着「这一份请求体」走：超时重试与授权后重发都用同一个键（不会
	// 重复发帖），但用户改过内容就换一个新键——同一个键配不同请求体会被
	// 服务端判成 409 idempotency_key_reuse。json.Marshal 对 map 的键排序，
	// 所以同一份内容得到的摘要稳定。
	sig, err := json.Marshal(payload)
	if err != nil {
		st.err = "请求体无法编码：" + err.Error()
		return
	}
	if st.key == "" || st.keySig != string(sig) {
		st.key = randomURLSafe(24)
		st.keySig = string(sig)
	}
	key := st.key
	// 没有 node_id 就是交给站点的 AI 自动分区：即使创建响应里没带 node，
	// 也按「节点待定」处理并轮询，别让回执与详情页停在两套说法上。
	_, nodeChosen := payload["node_id"]
	st.posting = true
	st.err = ""
	st.okShortID, st.okURL, st.okTitle = "", "", ""
	// 新的一次发布不再关心上一条回执：停掉它的节点轮询，成功后按新帖重启。
	a.stopComposeNodeWatch(st)
	st.okPending = false
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		created, err := a.client.createTopic(ctx, payload, key)
		a.publish(func() {
			if st != a.compose {
				return // 关掉窗口 / 退出登录后到达的旧结果，丢弃
			}
			st.posting = false
			if err != nil {
				a.handleAuthErrorWithScopesFor(toastCompose, err, []string{"write:topic"})
				st.err = friendlyError(err)
				if a.onPostPublished != nil {
					a.onPostPublished(createdTopic{}, err)
				}
				return
			}
			title := strings.TrimSpace(st.title)
			st.key = ""
			draftID := st.draftID
			st.resetComposeForm()
			// 发布成功：这条草稿已经变成帖子，从箱子里删掉。
			if draftID != "" {
				a.deleteDraft(draftID)
			}
			st.okTitle = title
			st.okShortID = created.ShortID
			st.okURL = created.URL
			// 不选节点时帖子先落在星云：回执上标明「节点分配中…」，并按
			// 文档要求轮询详情，分好后把原文链接换成正式地址。
			st.okPending = composeNodePending(created, nodeChosen)
			if st.okPending {
				a.watchComposeNode(st, created.ShortID)
			}
			// 刚发的帖子应该出现在列表里：把当前 tab 重新拉一遍。
			if a.autoLoad {
				a.reloadCurrent()
			}
			if a.onPostPublished != nil {
				a.onPostPublished(created, nil)
			}
		})
	}()
}

// submitMoment 发一条短动态。与发帖共用 write:topic 与同一套授权重跑；正文
// 1～500 字，空白内容不发请求。成功后清空草稿并用 toast 报告，不留在页面里。
func (a *app) submitMoment() {
	st := a.compose
	if st == nil || st.momentBusy {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLoginWithScopesFor(toastCompose, "登录 2Libra 后才能发动态。", []string{"write:topic"})
		return
	}
	content := strings.TrimSpace(st.momentDraft)
	if content == "" {
		st.momentErr = "动态内容不能为空"
		return
	}
	if utf8.RuneCountInString(content) > composeMomentMax {
		st.momentErr = fmt.Sprintf("动态不能超过 %d 个字符", composeMomentMax)
		return
	}
	if st.momentKey == "" || st.momentSig != content {
		st.momentKey = randomURLSafe(24)
		st.momentSig = content
	}
	key := st.momentKey
	st.momentBusy = true
	st.momentErr = ""
	st.momentGen++
	gen := st.momentGen
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		created, err := a.client.createMoment(ctx, key, content)
		a.publish(func() {
			if a.compose != st || st.momentGen != gen {
				return
			}
			st.momentBusy = false
			if err != nil {
				if a.handleAuthErrorWithScopesFor(toastCompose, err, []string{"write:topic"}) {
					return
				}
				st.momentErr = friendlyError(err)
				if a.onPostPublished != nil {
					a.onPostPublished(createdTopic{}, err)
				}
				return
			}
			st.momentKey = ""
			st.momentSig = ""
			draftID := st.draftID
			st.resetMomentForm()
			if draftID != "" {
				a.deleteDraft(draftID)
			}
			a.toast(toastCompose, "动态已发布")
			if a.onPostPublished != nil {
				a.onPostPublished(created, nil)
			}
		})
	}()
}
