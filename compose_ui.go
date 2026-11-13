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
)

// 「发帖」窗口：点主页右下角的悬浮按钮（FAB）打开，写标题与正文（Markdown，
// 带工具条与预览），并按需挂上发帖接口提供的各类组件——投票、评分、竞猜、
// 兑换码 / 红包、金币池，以及「谁可以看」的可见性门槛。
//
// 左边专心写：标题、正文，最下面才是「节点」——默认由站点 AI 判定分类，
// 不用选也不拉列表，想手动指定才展开。右边是常驻的控制栏：上面是匿名等
// 发布设置，下面是六块组件，每块点开在弹窗里填写；已添加的行尾直接有
// 「移除」，不必再进弹窗，底部还有「全部移除」。凡是可以有多个选项的地方
// （投票、竞猜、兑换码）都有「批量添加」与批量删除入口。
//
// 这是独立的 mygo 窗口而不是主窗口里的一层遮罩。数据、校验与请求在
// compose.go；这里只负责窗口的开关与界面。

const (
	composeWindowWidth  = 1000
	composeWindowHeight = 760
	composeWinMinWidth  = 600
	composeWinMinHeight = 520
	// 右侧常驻的附加组件栏与它的编辑弹窗：弹窗里的字段可以滚动，窗口缩到
	// 最小尺寸时也不会长出屏幕。
	composeSidebarWidth       = 200
	composeModuleDialogWidth  = 520
	composeModuleDialogHeight = 380
	// 弹窗里滚动区以外的部分（标题、说明、两条分隔线、按钮行、间距与
	// 内边距）约 210 DIP：窗口矮时按这个差把滚动区压小，底部按钮不会被
	// 挤出屏幕。
	composeModuleDialogChrome = 210
	composeModuleDialogMin    = 220
	composeEditorLines        = 10
	composeEditorMaxLine      = 30
	composePreviewHeight      = 320
	// composeFabInset 是 FAB 占用的右侧宽度（按钮 + 边距）。列表底部的
	// 「加载更多 / 重试」那一行据此让出这一段，免得被浮在上面的按钮盖住；
	// FAB 本身不占布局高度，只是浮在内容区右下角。
	composeFabInset = 110
)

/* ------------------------------------------------------------------
   入口、显示与关闭
---------------------------------------------------------------- */

// showCompose 是「发帖」窗口的全局入口：任何窗口、任何 goroutine 都能调用。
// 它把工作挪到主线程；窗口已经开着就只把它带到前台，不会开出第二个。
func (a *app) showCompose() {
	a.publish(a.showComposeWindow)
}

// showComposeWindow 在主线程上真正打开窗口。草稿留在 a.compose 里，关掉再
// 打开还是原样；退出登录时才丢弃。
func (a *app) showComposeWindow() {
	if a.compose == nil {
		a.compose = newComposeState()
		a.compose.focusTitle = true
	}
	if !a.auth.loggedIn() {
		// 没登录就先授权；回来后把窗口打开，用户不用再点一次 FAB。
		a.requestLogin("登录 2Libra 后才能发帖。", a.showCompose)
		a.openComposeWindow()
		return
	}
	// 节点列表按需加载：默认由 AI 判定分类，用户展开「手动指定节点」时
	// 才在视图里拉（见 composeWindowView）。
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
		Title:             "发帖",
		Width:             composeWindowWidth,
		Height:            composeWindowHeight,
		MinWidth:          composeWinMinWidth,
		MinHeight:         composeWinMinHeight,
		StateKey:          "compose",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口、通知窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.composeWindowView),
	})
	a.composeWin = win
	win.OnClosed(func() { a.composeWin = nil })
}

// closeComposeWindow 关掉发帖窗口：主窗口关闭或退出登录时调用。草稿留在
// a.compose 里，下次打开继续写。只在主线程调用。
func (a *app) closeComposeWindow() {
	win := a.composeWin
	if win == nil {
		return
	}
	a.composeWin = nil
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

// composeFab 是主页右下角那一列悬浮按钮里的「发帖」：主操作，放在最下面。
// 位置由 listFabs 的竖列给（见 fabs.go），这里只管长什么样；公开 tab 与
// 私人 tab 都能用（未登录时点它先走授权）。图标 + 文案一起显示，任何窗口
// 宽度下都能看出这是「发帖」。
func (a *app) composeFab(c *ui.Context) {
	t := c.Theme()
	// 边框始终占 1 DIP：只在悬停时加边框会让按钮在鼠标移入时变大一点，
	// 位置来回跳。悬停只换边框颜色。
	ring := t.Accent
	b := ui.Box(c).Padding(10, 16).Radius(999).
		Background(t.Accent).Border(1, ring).AlignItems(ui.Center)
	if b.Hovered() {
		b.Border(1, t.Text)
	}
	b.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, composeIcon).Size(16, 16).TextColor(t.AccentText)
			ui.Text(c, "发帖").FontSize(13).Bold().SingleLine().TextColor(t.AccentText)
		})
	})
	b.Label("发帖").Tooltip("写一篇新帖子").OnClick(a.showCompose)
}

/* ------------------------------------------------------------------
   窗口界面
---------------------------------------------------------------- */

// composeWindowView 是发帖窗口的 view。窗口自带标题栏与窗口按钮，所以这里
// 不需要遮罩，也不占主窗口的位置。
func (a *app) composeWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	// 回执里的「看原文」要打开系统浏览器：服务在 UI 线程取一次，回调里不再碰 Context。
	a.srv = c.Services()
	c.Root().Background(t.Background)
	// 本窗口里发起的操作结果以 toast 显示在这里，不退回主窗口。
	a.drainToasts(c, a.claimsToasts(toastCompose))
	// Ctrl+Enter 发布：写长文的常用手势。
	c.OnShortcut(ui.Ctrl, ui.KeyEnter, a.submitCompose)

	st := a.compose
	if st == nil {
		st = newComposeState()
		a.compose = st
	}
	// 节点列表按需加载：默认不指定节点，由站点 AI 判定分类，只有展开
	// 「手动指定节点」才去读缓存 / 发请求。
	if st.nodeManual {
		a.ensureComposeNodes()
	}
	// Esc 关闭窗口，但组件弹窗开着时由 Modal 自己吃掉 Esc（只关弹窗，不丢
	// 草稿，也不关窗口），所以那一帧不再注册窗口级快捷键。
	if !st.moduleOpen {
		c.OnShortcut(0, ui.KeyEscape, a.closeComposeWindow)
	}

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口，右上角是最小化与关闭。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Text(c, "发帖").FontSize(13).Bold().SingleLine().TextColor(t.Text)
				ui.Text(c, "· 支持 Markdown").FontSize(11).SingleLine().TextColor(t.TextMuted)
			})
			a.titleButton(c, titleMinimize, "最小化", func() {
				if win := a.composeWin; win != nil {
					win.Minimize()
				}
			})
			a.titleButton(c, titleClose, "关闭", func() { a.closeComposeWindow() })
		})
		ui.Divider(c)

		// 主体分两栏：左边专心写，右边常驻组件入口。两栏各自滚动，正文
		// 写多长都不会把组件列表顶出视野。
		ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() {
				ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
					ui.Column(c).FillWidth().Padding(18, 20).Gap(14).Children(func() {
						a.composeTitleField(c, st)
						a.composeEditorSection(c, st)
						ui.Divider(c)
						a.composeMessages(c, st)
						// 节点排在写作区最后：默认不用选，AI 判定分类。
						a.composeNodeSection(c, st)
					})
				})
			})
			ui.Box(c).Width(1).AlignSelf(ui.Stretch).Background(t.Border)
			a.composeSidebar(c, st)
		})

		ui.Divider(c)
		a.composeFooter(c, st)
	})
	// 弹窗画在内容之上：和授权弹窗一样放在 view 末尾，才能盖住整扇窗口。
	a.composeModuleDialog(c, st)
}

// composeTitleField 是写作区第一块：只有标题。节点挪到最下面并降级为默认
// AI 判定，匿名开关挪到右侧栏，打开窗口时标题就是唯一要填的东西。
func (a *app) composeTitleField(c *ui.Context, st *composeState) {
	ui.Field(c, "标题", func() {
		ui.TextInput(c, &st.title).Label("标题").
			Placeholder("一句话说清这帖讲什么").FillWidth().
			MaxLength(composeTitleMax).Bind(&st.titleField)
		// 打开窗口就把焦点放进标题：写帖子的第一件事是给它起个名字。
		if st.focusTitle {
			st.titleField.Focus(c)
			st.focusTitle = false
		}
	}).Description(fmt.Sprintf("%d / %d 字", utf8.RuneCountInString(st.title), composeTitleMax))
}

// composeNodeSection 是写作区最下面的一块：节点默认由站点的 AI 判定分类，
// 不需要选、也不会为了一个用不上的列表去发请求。只有点了「手动指定节点」
// 才展开级联选择并按需读缓存 / 拉节点；「恢复 AI 判定」直接把手动选择清掉，
// 清除入口摆在明显位置，不用进弹窗。
func (a *app) composeNodeSection(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Divider(c)
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "节点").FontSize(12).TextColor(t.TextMuted)
					ui.Text(c, "选填 · 默认由 AI 判定").FontSize(11).TextColor(t.TextMuted)
				})
				state := "发帖后由 AI 自动判定分类，无需选择。"
				if node := st.selectedNode(); node != nil {
					state = "已手动指定：" + node.parentDisplay() + "，发布时发到这个节点。"
				}
				ui.Text(c, state).FontSize(11).TextColor(t.TextMuted).MaxLines(2)
			})
			if st.nodeManual {
				if ui.Button(c, "收起节点").Clicked() {
					st.nodeManual = false
					st.nodePick.open = false
				}
			} else if ui.Button(c, "手动指定节点").Clicked() {
				st.nodeManual = true
			}
			if st.nodeID != "" {
				if ui.Button(c, "恢复 AI 判定").Clicked() {
					st.nodeID = ""
					st.nodePick.open = false
					a.toast(toastCompose, "已恢复由 AI 判定分类")
				}
			}
		})
		if !st.nodeManual {
			return
		}
		if len(st.nodes) > 0 {
			options := nodeCascaderOptions(st.nodes)
			if len(options) > 0 {
				cascader(c, "节点", options, &st.nodeID, &st.nodePick,
					"清除节点选择", "由 AI 判定分类")
				return
			}
		}
		// 节点还没到（或取不到）时不给一个空的级联选择：说明正在做什么，
		// 失败时把原因摆出来；节点本来就选填，读不到也能发帖。
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if st.nodeLoading {
				ui.Spinner(c).Size(12, 12)
			}
			msg := "节点列表还没准备好，可不选节点直接发布。"
			if st.nodeLoading {
				msg = "正在读取节点…（可不选节点直接发布）"
			}
			switch {
			case st.nodeErr != "":
				msg = "节点读取失败：" + st.nodeErr + "；可不选节点直接发布"
			case st.nodeLoaded:
				msg = "没有可用的节点，可不选节点直接发布。"
			}
			ui.Text(c, msg).FontSize(12).TextColor(t.TextMuted).MaxLines(2).Grow(1)
			if st.nodeErr != "" || st.nodeLoaded {
				if ui.Button(c, "重试").Clicked() {
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
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		// 标题行：区块名、编辑 / 预览切换与字数。切换放在工具条外面，因为
		// 工具条窄了会把末尾的控件收进「»」菜单——模式切换不该被藏起来。
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "正文").FontSize(12).TextColor(t.TextMuted).Grow(1)
			a.editorPanelButtons(c, &st.mdEditor, "表情", "上传图片", false)
			ui.Segmented(c, &st.preview, "编辑", "预览").Label("正文视图")
			count := ui.Text(c, fmt.Sprintf("%d / %d 字",
				utf8.RuneCountInString(st.body), composeContentMax)).
				FontSize(11).TextColor(t.TextMuted).SingleLine()
			if n := utf8.RuneCountInString(st.body); n > composeContentMax {
				count.TextColor(t.Danger)
			}
		})
		zone := ui.Column(c).FillWidth().Radius(10).Background(t.Background)
		// 拖放区要在 Children 之前登记：这一帧就得把 flagDropTarget 记进
		// 节点，下一次 drop 事件才找得到它。悬停时整圈换成强调色。
		over := a.editorDropZone(zone, uploadTargetCompose)
		if over {
			zone.Border(1, t.Accent).Background(t.SurfaceHover)
		} else {
			zone.Border(1, t.Border)
		}
		zone.Children(func() {
			// 工具条固定在输入框上方（「头部」），滚动正文时始终可见。
			ui.Row(c).FillWidth().Padding(2, 4).Children(func() {
				a.editorToolbar(c, &st.mdEditor, &st.body, false)
			})
			ui.Divider(c)
			a.editorPanels(c, uploadTargetCompose, &st.mdEditor, &st.body, nil, false)
			if over {
				ui.Text(c, "松开即可把图片插入正文").FontSize(11).
					TextColor(t.Accent).Padding(6, 10)
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
			area := ui.TextArea(c, &st.body).Label("正文内容").
				Border(0, t.Border).Background(ui.Transparent).FillWidth().
				Lines(composeEditorLines, composeEditorMaxLine).
				Placeholder("支持 Markdown：**加粗**、# 标题、> 引用、- 列表、`代码`、[链接](url)、![图片](url) 与 :smile: 表情；图片可直接拖进来").
				Bind(&st.bodyField)
			a.syncComposeSelection(area, st)
		})
	})
}

// markdownButton 是工具条上的一项：显示文案（图标模式下作无障碍名与
// 悬停说明的回退）、悬停说明、它插入的标记，以及图标模式下的线性图标。
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

// syncComposeSelection 现在只是共用编辑器状态的一个薄包装：选区逻辑在
// editor_core.go 的 mdEditor.syncSelection 里，评论输入框用的是同一个。
func (a *app) syncComposeSelection(area ui.Element, st *composeState) {
	st.syncSelection(area)
}

/* ------------------------------------------------------------------
   附加组件
---------------------------------------------------------------- */

// composeModuleSpec 是右侧栏里的一项。flag 指向「这一块随帖发送」的开关，
// body 画弹窗里的字段；侧栏与弹窗共用同一张表，加组件时不用改两处。
type composeModuleSpec struct {
	name string
	hint string
	flag func(*composeState) *bool
	body func(*app, *ui.Context, *composeState)
}

// composeModuleSpecs 是发帖接口支持的全部可选块，顺序就是侧栏里的顺序。
// 默认一个都不启用：没点「添加此组件」的块，一个字段都不会进请求体。
var composeModuleSpecs = []composeModuleSpec{
	{"投票", "给帖子加一个单选或多选投票。",
		func(st *composeState) *bool { return &st.voteOpen },
		func(a *app, c *ui.Context, st *composeState) { a.composeVote(c, st) }},
	{"评分", "给某个对象打分并附上理由。",
		func(st *composeState) *bool { return &st.scoreOpen },
		func(a *app, c *ui.Context, st *composeState) { a.composeScore(c, st) }},
	{"竞猜", "发起一场竞猜，让读者选边。",
		func(st *composeState) *bool { return &st.betOpen },
		func(a *app, c *ui.Context, st *composeState) { a.composeBet(c, st) }},
	{"兑换码 / 红包", "发兑换码，或开一个随帖红包。",
		func(st *composeState) *bool { return &st.redeemOpen },
		func(a *app, c *ui.Context, st *composeState) { a.composeRedeem(c, st) }},
	{"金币池", "放一笔金币，读者浏览时按概率领取。",
		func(st *composeState) *bool { return &st.poolOpen },
		func(a *app, c *ui.Context, st *composeState) { a.composePool(c, st) }},
	{"可见性（谁可以看）", "限制谁能读这篇帖子。",
		func(st *composeState) *bool { return &st.guardOpen },
		func(a *app, c *ui.Context, st *composeState) { a.composeGuard(c, st) }},
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

// composeSidebar 是窗口右侧常驻的控制栏：上半是匿名等发布设置，下半是六块
// 附加组件。点一行在弹窗里编辑；已添加的行尾直接放「移除」，不必为了移除再
// 进一次弹窗，底部还有「全部移除」一次清掉所有组件。
func (a *app) composeSidebar(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Column(c).Width(composeSidebarWidth).FillHeight().MinHeight(0).
		Background(t.Surface).Padding(14, 12).Gap(8).Children(func() {
		ui.Text(c, "发布设置").FontSize(13).Bold().TextColor(t.Text).SingleLine()
		ui.Row(c).FillWidth().Children(func() {
			ui.Toggle(c, &st.anonymous, "匿名发帖").Label("匿名发帖")
		})
		ui.Text(c, "匿名只随这篇帖子生效。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		ui.Divider(c)
		ui.Text(c, "附加组件").FontSize(13).Bold().TextColor(t.Text).SingleLine()
		ui.Text(c, "选填。点一行，在弹窗里设置好再添加。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(3)
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Gap(6).Children(func() {
				for i := range composeModuleSpecs {
					m := composeModuleSpecs[i]
					index := i
					on := *m.flag(st)
					// 边框始终是 1 DIP，只换颜色：启用状态切换时按钮不会跳。
					ring := t.Border
					if on {
						ring = t.Accent
					}
					ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
						setup := ui.Button(c, "").Grow(1).MinWidth(0).
							Justify(ui.SpaceBetween).
							Border(1, ring).Label(m.name).Tooltip(m.hint)
						setup.Children(func() {
							ui.Text(c, m.name).FontSize(12).SingleLine().
								TextColor(t.Text).Grow(1)
							mark, fg := "设置", t.TextMuted
							if on {
								mark, fg = "已添加", t.Success
							}
							ui.Text(c, mark).FontSize(11).SingleLine().TextColor(fg)
						})
						setup.OnClick(func() {
							st.moduleIndex = index
							st.moduleOpen = true
						})
						if on {
							// 移除就在行尾：不用再打开弹窗，填过的内容仍留在草稿里。
							remove := ui.Button(c, "移除").
								Label("移除" + m.name).
								Tooltip("移除" + m.name + "，填过的内容仍留在草稿里")
							if remove.Clicked() {
								*m.flag(st) = false
								a.toast(toastCompose, "已移除"+m.name)
							}
						}
					})
				}
			})
		})
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("已添加 %d / %d 项", st.enabledModuleCount(),
				len(composeModuleSpecs))).FontSize(11).
				TextColor(t.TextMuted).SingleLine().Grow(1)
			if st.enabledModuleCount() > 0 {
				if ui.Button(c, "全部移除").Label("全部移除附加组件").Clicked() {
					for _, m := range composeModuleSpecs {
						*m.flag(st) = false
					}
					a.toast(toastCompose, "已移除全部附加组件")
				}
			}
		})
	})
}

// composeModuleDialog 是点侧栏某一行之后弹出的编辑窗。字段和以前折叠面板里
// 的一模一样，只是搬进了弹窗：启不启用由底部的按钮决定，编辑中的内容一直
// 留在草稿里，关掉弹窗再点进来还在。移除不在这里做——侧栏对应行的行尾
// 直接有「移除」。点窗外或按 Esc 只关弹窗，不关窗口。
func (a *app) composeModuleDialog(c *ui.Context, st *composeState) {
	if !st.moduleOpen || st.moduleIndex < 0 || st.moduleIndex >= len(composeModuleSpecs) {
		return
	}
	m := composeModuleSpecs[st.moduleIndex]
	t := c.Theme()
	ui.Modal(c, &st.moduleOpen, func() {
		ui.Column(c).Width(composeModuleDialogWidth).Gap(8).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, m.name).FontSize(16).Bold().TextColor(t.Text).Grow(1)
				if ui.Button(c, "关闭").Clicked() {
					st.moduleOpen = false
				}
			})
			ui.Text(c, m.hint).FontSize(12).TextColor(t.TextMuted).MaxLines(3)
			ui.Divider(c)
			_, windowHeight := c.Size()
			scrollHeight := windowHeight - composeModuleDialogChrome
			scrollHeight = max(composeModuleDialogMin,
				min(scrollHeight, composeModuleDialogHeight))
			ui.Scroll(c).FillWidth().MaxHeight(scrollHeight).Children(func() {
				ui.Column(c).FillWidth().Padding(2, 2).Children(func() {
					m.body(a, c, st)
					// 最后一行不要紧贴滚动区下沿：看起来像被截断。
					ui.Spacer(c).Height(8)
				})
			})
			ui.Divider(c)
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				on := *m.flag(st)
				state := "未添加：不会随帖发送"
				if on {
					state = "已添加：会随帖一起发送；移除在右侧栏这一行的行尾。"
				}
				ui.Text(c, state).FontSize(11).TextColor(t.TextMuted).
					MaxLines(2).Grow(1)
				label := "添加此组件"
				if on {
					label = "完成"
				}
				if ui.PrimaryButton(c, label).Clicked() {
					if !on {
						*m.flag(st) = true
						a.toast(toastCompose, "已添加"+m.name)
					}
					st.moduleOpen = false
				}
			})
		})
	})
}

// composeOptionList 是所有「可以有多个选项」的地方共用的编辑区。操作行放在
// 最上面：「添加选项」「批量添加」「删除选中」都在显眼处；批量添加展开的内联
// 录入区紧跟其后，弹窗不用滚动就能点「加入选项」。下面才是逐行的输入框、
// 行首勾选框与行尾「删除」。增删都卡在 minCount～maxCount 之间，操作结果
// 一律用窗口内 toast 报告。
func (a *app) composeOptionList(c *ui.Context, opts *[]string, checked *[]bool,
	batchOpen *bool, batchText *string, label string, minCount, maxCount, maxLen int) {
	t := c.Theme()
	*checked = syncComposeChecks(*checked, len(*opts))
	nChecked := 0
	for _, on := range *checked {
		if on {
			nChecked++
		}
	}
	batchToggled, removeChecked := false, false
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		add := ui.Button(c, "添加选项").Disabled(len(*opts) >= maxCount)
		if add.Clicked() {
			*opts = append(*opts, "")
			*checked = append(*checked, false)
		}
		if ui.Button(c, "批量添加").Tooltip("一次粘贴多行，每行一个选项").Clicked() {
			batchToggled = true
		}
		ui.Spacer(c).Grow(1)
		drop := ui.Button(c, "删除选中").
			Tooltip(fmt.Sprintf("删除勾选的 %d 个%s", nChecked, label)).
			Disabled(nChecked == 0)
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
		ui.Column(c).FillWidth().Gap(6).Radius(8).Background(t.Surface).
			Border(1, t.Border).Padding(8, 10).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "批量添加：一行一个，可直接粘贴多行").
					FontSize(11).TextColor(t.TextMuted).Grow(1)
				if ui.Button(c, "取消").Clicked() {
					*batchOpen = false
					*batchText = ""
				}
				if ui.PrimaryButton(c, "加入选项").Clicked() {
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
			ui.Text(c, fmt.Sprintf("最多 %d 项", maxCount)).
				FontSize(11).TextColor(t.TextMuted)
			ui.TextArea(c, batchText).Label(label+"批量录入").
				Placeholder("第一行\n第二行\n第三行").FillWidth().Lines(4, 12)
		})
	}
	removeAt := -1
	for i := range *opts {
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Checkbox(c, &(*checked)[i], "").Shrink(0).
				Label(fmt.Sprintf("勾选%s %d", label, i+1)).
				Tooltip("勾选后可批量删除")
			ui.TextInput(c, &(*opts)[i]).
				Label(fmt.Sprintf("%s %d", label, i+1)).
				Placeholder(fmt.Sprintf("%s %d", label, i+1)).
				FillWidth().MaxLength(maxLen)
			remove := ui.Button(c, "删除").
				Tooltip(fmt.Sprintf("删除%s %d", label, i+1)).
				Disabled(len(*opts) <= minCount)
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
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, "投票类型").FontSize(12).TextColor(t.TextMuted)
		ui.Segmented(c, &st.voteTypeIndex, "单选", "多选").Label("投票类型")
		ui.Field(c, "投票标题", func() {
			ui.TextInput(c, &st.voteTitle).Label("投票标题").
				Placeholder(fmt.Sprintf("最多 %d 字", composeVoteTitleMax)).
				FillWidth()
		})
		// 选项是动态行，支持逐行增删、批量粘贴与勾选批量删除；行数受
		// 1～20 限制（见 composeOptionList）。
		a.composeOptionList(c, &st.voteOptions, &st.voteChecked,
			&st.voteBatchOpen, &st.voteBatchText,
			"投票选项", 1, composeOptionMax, composeOptionTextMax)
		ui.Row(c).FillWidth().Children(func() {
			ui.Toggle(c, &st.voteResultVisible, "公开投票结果")
			ui.Spacer(c).Grow(1)
		})
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.End).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				ui.Text(c, "截止时间").FontSize(12).TextColor(t.TextMuted)
				ui.TextInput(c, &st.voteDeadline).Label("投票截止时间").
					Placeholder("可选，如 2026-11-20 12:00").FillWidth()
			})
			if voteTypeAt(st.voteTypeIndex) == "multiple" {
				ui.Column(c).Width(150).Gap(4).Children(func() {
					ui.Text(c, "最多可选").FontSize(12).TextColor(t.TextMuted)
					ui.NumberInput(c, &st.voteMaxChoices, 1, composeOptionMax, 1).
						Label("最多可选几项")
				})
			}
		})
	})
}

// composeScore 是评分块：对象名、0～100 的分值与可选理由。
func (a *app) composeScore(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Field(c, "评分对象", func() {
			ui.TextInput(c, &st.scoreTarget).Label("评分对象").
				Placeholder(fmt.Sprintf("最多 %d 字", composeScoreTargetMax)).FillWidth()
		})
		ui.Row(c).FillWidth().Gap(10).AlignItems(ui.End).Children(func() {
			ui.Column(c).Width(160).Gap(4).Children(func() {
				ui.Text(c, "分值").FontSize(12).TextColor(t.TextMuted)
				ui.NumberInput(c, &st.scoreValue, 0, 100, 1).Label("分值（0～100）")
			})
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				ui.Text(c, "理由").FontSize(12).TextColor(t.TextMuted)
				ui.TextInput(c, &st.scoreComment).Label("评分理由").
					Placeholder("可选").FillWidth().MaxLength(composeScoreCommentMax)
			})
		})
	})
}

// composeBet 是竞猜块：标题、2～6 个选项与可选参与截止时间。
func (a *app) composeBet(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Field(c, "竞猜标题", func() {
			ui.TextInput(c, &st.betTitle).Label("竞猜标题").
				Placeholder(fmt.Sprintf("最多 %d 字", composeBetTitleMax)).FillWidth()
		})
		// 竞猜选项与投票共用一套动态行：2～6 项，批量入口也在里面。
		a.composeOptionList(c, &st.betOptions, &st.betChecked,
			&st.betBatchOpen, &st.betBatchText,
			"竞猜选项", composeBetOptionsMin, composeBetOptionsMax, composeBetOptionMax)
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, "参与截止时间").FontSize(12).TextColor(t.TextMuted)
			ui.TextInput(c, &st.betDeadline).Label("竞猜参与截止时间").
				Placeholder("可选，如 2026-11-20 12:00").FillWidth()
		})
	})
}

// composeRedeem 是兑换码 / 红包块：一行一个兑换码（本身就支持批量），另有
// 「批量粘贴」一次追加多行、「清空」一次移除全部，加上说明、每人可领数量
// 与可选的红包。红包金额只有文档给出的六档，用分段控件选。
func (a *app) composeRedeem(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "兑换码（一行一个）").FontSize(12).
					TextColor(t.TextMuted).Grow(1)
				if ui.Button(c, "批量粘贴").Label("批量粘贴兑换码").
					Tooltip("一次粘贴多行，追加到兑换码列表").Clicked() {
					st.redeemBatchOpen = !st.redeemBatchOpen
				}
				clear := ui.Button(c, "清空").Label("清空兑换码").
					Tooltip("清空全部兑换码").Disabled(st.redeemCodes == "")
				if clear.Clicked() {
					st.redeemCodes = ""
					st.redeemBatchText = ""
					a.toast(toastCompose, "已清空兑换码")
				}
			})
			ui.TextArea(c, &st.redeemCodes).Label("兑换码").
				Placeholder("一行一个兑换码，也可以用逗号分隔").
				FillWidth().Lines(4, 10).MaxLength(composeCodeMax * (composeCodeTextMax + 1))
			ui.Text(c, "支持一次粘贴多行，或用逗号 / 分号分隔；上限 500 个。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		})
		if st.redeemBatchOpen {
			a.composeRedeemBatch(c, st)
		}
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, "兑换说明").FontSize(12).TextColor(t.TextMuted)
			ui.TextArea(c, &st.codeDescription).Label("兑换说明").
				Placeholder("可选").FillWidth().Lines(2, 5).MaxLength(composeCodeDescMax)
		})
		ui.Row(c).Gap(10).AlignItems(ui.End).Children(func() {
			ui.Column(c).Width(180).Gap(4).Children(func() {
				ui.Text(c, "每人可领").FontSize(12).TextColor(t.TextMuted)
				ui.NumberInput(c, &st.maxRedeemPerUser,
					composeRedeemPerUserLo, composeRedeemPerUserHi, 1).Label("每人可领数量")
			})
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				ui.Text(c, "红包上限").FontSize(12).TextColor(t.TextMuted)
				ui.Segmented(c, &st.redPacketIndex, redPacketLabels...).Label("红包金额上限")
			})
		})
		ui.Row(c).FillWidth().Children(func() {
			ui.Toggle(c, &st.redPacket, "同时开红包")
			ui.Spacer(c).Grow(1)
		})
	})
}

// composeRedeemBatch 是兑换码的批量入口：粘贴一段多行文本，一次追加到兑换码
// 列表里。空行自动丢掉，超过 500 个的部分省略，结果用 toast 报告。
func (a *app) composeRedeemBatch(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(6).Radius(8).Background(t.Surface).
		Border(1, t.Border).Padding(8, 10).Children(func() {
		// 按钮放在录入区最上面：弹窗不用滚动就能点「追加兑换码」。
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("批量粘贴兑换码（已有 %d / %d 个）",
				len(st.redeemCodeList()), composeCodeMax)).
				FontSize(11).TextColor(t.TextMuted).Grow(1).MaxLines(2)
			if ui.Button(c, "取消").Clicked() {
				st.redeemBatchOpen = false
				st.redeemBatchText = ""
			}
			if ui.PrimaryButton(c, "追加兑换码").Clicked() {
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
		ui.Text(c, "一行一个，可直接粘贴多行；空行会丢掉。").
			FontSize(11).TextColor(t.TextMuted)
		ui.TextArea(c, &st.redeemBatchText).Label("兑换码批量录入").
			Placeholder("CODE-1\nCODE-2\nCODE-3").FillWidth().Lines(4, 12)
	})
}

// composePool 是金币池块：初始金币（1～1000000）与可选的中奖概率（0.1～0.5）。
// 概率留 0 表示不发送，由服务端用默认值。
func (a *app) composePool(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, "发帖时先往帖子里的金币池放一笔金币，读者浏览时按概率领取。").
			FontSize(12).TextColor(t.TextMuted).MaxLines(3)
		ui.Row(c).Gap(10).AlignItems(ui.End).Children(func() {
			ui.Column(c).Width(200).Gap(4).Children(func() {
				ui.Text(c, "初始金币").FontSize(12).TextColor(t.TextMuted)
				ui.NumberInput(c, &st.poolCoins,
					composePoolCoinsLo, composePoolCoinsHi, 10).Label("金币池初始金币")
			})
			ui.Column(c).Width(180).Gap(4).Children(func() {
				ui.Text(c, "中奖概率（0 = 默认）").FontSize(12).TextColor(t.TextMuted)
				ui.NumberInput(c, &st.poolProbability, 0, composePoolProbHi, 0.1).
					Label("中奖概率")
			})
		})
	})
}

// composeGuard 是「谁可以看」：不限制 / 需登录 / 达到等级 / 满足 D20 属性。
// 文档里的 access_guard_type 就是这四种（不限制时不发字段）。
func (a *app) composeGuard(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, "可见范围").FontSize(12).TextColor(t.TextMuted)
			ui.Select(c, &st.guardChoice, guardTypeLabels).Label("可见范围").FillWidth()
		})
		switch guardFromLabel(st.guardChoice) {
		case guardLevel:
			ui.Column(c).Width(200).Gap(4).Children(func() {
				ui.Text(c, "最低等级（1～100）").FontSize(12).TextColor(t.TextMuted)
				ui.NumberInput(c, &st.guardLevel,
					composeGuardLevelLo, composeGuardLevelHi, 1).Label("最低可见等级")
			})
		case guardAttribute:
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.End).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					ui.Text(c, "D20 属性名").FontSize(12).TextColor(t.TextMuted)
					ui.TextInput(c, &st.guardAttr).Label("D20 属性名").
						Placeholder("如 力量 / 智力").FillWidth()
				})
				ui.Column(c).Width(160).Gap(4).Children(func() {
					ui.Text(c, "属性下限（0～10）").FontSize(12).TextColor(t.TextMuted)
					ui.NumberInput(c, &st.guardAttrValue,
						0, composeGuardAttrHi, 1).Label("属性下限")
				})
			})
		}
		ui.Text(c, "门槛由服务端判定；写错属性名时发表会返回 400 并说明原因。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(2)
	})
}

/* ------------------------------------------------------------------
   提示与底部操作
---------------------------------------------------------------- */

// composeMessages 是表单下方的回执与错误：发表成功后给一条可点开的回执，
// 失败时把服务端的原因原样显示（friendlyError 已经翻译成中文）。
func (a *app) composeMessages(c *ui.Context, st *composeState) {
	t := c.Theme()
	if st.err != "" {
		ui.Text(c, st.err).FontSize(12).TextColor(t.Danger).MaxLines(4)
	}
	// 发表需要 write:topic，默认授权里没有它：提前说明，免得写完才被拦。
	if a.auth.loggedIn() && !a.hasAuthScope("write:topic") {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "当前授权还不能发帖，需要补充 write:topic 权限。").
				FontSize(12).TextColor(t.TextMuted).MaxLines(2).Grow(1)
			if ui.Button(c, "去授权").Clicked() {
				a.demandAuthorization(scopeNeed([]string{"write:topic"}), nil)
			}
		})
	}
	if st.okShortID == "" {
		return
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "已发表："+st.okTitle).FontSize(12).TextColor(t.Success).MaxLines(2).Grow(1)
		if ui.Button(c, "在应用内打开").Clicked() {
			it := Item{ShortID: st.okShortID, URL: st.okURL}
			a.closeComposeWindow()
			// 帖子是在主窗口里打开的：走 publish，主窗口才会按新页面重绘。
			a.publish(func() { a.openDetail(it) })
		}
		if st.okURL != "" {
			if ui.Button(c, "看原文").Clicked() {
				a.srv.OpenURL(st.okURL)
			}
		}
	})
}

// composeFooter 是窗口底部固定的一条：左边是草稿状态，右边是「发布」。
func (a *app) composeFooter(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(10, 16).Gap(10).AlignItems(ui.Center).Children(func() {
		if st.posting {
			ui.Spinner(c).Size(14, 14)
			ui.Text(c, "正在发布…").FontSize(12).TextColor(t.TextMuted).SingleLine()
		} else {
			hint := "Ctrl+Enter 发布"
			if !a.auth.loggedIn() {
				hint = "发帖需要登录 2Libra"
			}
			ui.Text(c, hint).FontSize(11).TextColor(t.TextMuted).SingleLine()
		}
		ui.Spacer(c).Grow(1)
		if ui.Button(c, "清空").Clicked() && !st.posting {
			// 清空草稿与各组件，但保留节点选择与已经拉到的节点列表。
			st.resetComposeForm()
		}
		publish := ui.PrimaryButton(c, "发布").Disabled(st.posting)
		if publish.Clicked() {
			a.submitCompose()
		}
	})
}

/* ------------------------------------------------------------------
   发布
---------------------------------------------------------------- */

// submitCompose 校验并发出 POST /v1/topics。失败时先交给全局授权链路：缺
// write:topic 会弹授权窗，授权成功后按同一份内容与同一个幂等键自动重发，
// 用户不用把写好的正文丢掉重来。
func (a *app) submitCompose() {
	st := a.compose
	if st == nil || st.posting {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录 2Libra 后才能发帖。", a.submitCompose)
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
	st.posting = true
	st.err = ""
	st.okShortID, st.okURL, st.okTitle = "", "", ""
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
				a.handleAuthError(err, func() {
					if a.compose == st {
						a.submitCompose()
					}
				})
				st.err = friendlyError(err)
				if a.onPostPublished != nil {
					a.onPostPublished(createdTopic{}, err)
				}
				return
			}
			title := strings.TrimSpace(st.title)
			st.key = ""
			st.resetComposeForm()
			st.okTitle = title
			st.okShortID = created.ShortID
			st.okURL = created.URL
			// 刚发的帖子应该出现在列表里：把当前 tab 重新拉一遍。
			if a.autoLoad {
				a.reloadCurrent()
			}
			// 发帖会扣金币（随帖的金币池、兑换码红包也都算在这一笔里）：
			// 发出全局金币刷新事件，标题栏与金币窗口一起更新。
			a.coinsChanged(coinEventPost)
			if a.onPostPublished != nil {
				a.onPostPublished(created, nil)
			}
		})
	}()
}
