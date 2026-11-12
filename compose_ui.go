package main

import (
	"context"
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
// 和通知、设置、「我的」一样，它是独立的 mygo 窗口，而不是主窗口里的一层
// 遮罩：一份草稿要同时放下标题、正文和六块可选组件，塞进弹窗里只会互相挤。
// 数据、校验与请求在 compose.go；这里只负责窗口的开关与界面。

const (
	composeWindowWidth   = 780
	composeWindowHeight  = 760
	composeWinMinWidth   = 600
	composeWinMinHeight  = 520
	composeEditorLines   = 10
	composeEditorMaxLine = 30
	composePreviewHeight = 320
	// composeFabClearance 是主页内容区底部为悬浮发帖按钮让出的高度。FAB 浮在
	// 右下角，如果不留这条空带，列表最后几行与「加载更多 / 重试」那一行就
	// 会被按钮盖住——按钮在按钮上面，点下去就变成了发帖。
	composeFabClearance = 64
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
	a.ensureComposeNodes()
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

// composeFab 是主页右下角的悬浮发帖按钮（FAB）。它绝对定位在内容区之上，
// 不参与列表布局，也不随列表滚动；公开 tab 与私人 tab 都能用（未登录时点
// 它先走授权）。图标 + 文案一起显示，任何窗口宽度下都能看出这是「发帖」。
func (a *app) composeFab(c *ui.Context) {
	t := c.Theme()
	// 边框始终占 1 DIP：只在悬停时加边框会让按钮在鼠标移入时变大一点，
	// 位置来回跳。悬停只换边框颜色。
	ring := t.Accent
	b := ui.Box(c).Absolute().Right(24).Bottom(24).Padding(10, 16).Radius(999).
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
	// Ctrl+Enter 发布、Esc 关闭：两者都是写长文的常用手势。Esc 不会丢草稿，
	// 草稿留在 a.compose 里，重新打开还在。
	c.OnShortcut(ui.Ctrl, ui.KeyEnter, a.submitCompose)
	c.OnShortcut(0, ui.KeyEscape, a.closeComposeWindow)

	st := a.compose
	if st == nil {
		st = newComposeState()
		a.compose = st
	}
	a.ensureComposeNodes()

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

		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Padding(18, 20).Gap(14).Children(func() {
				a.composeBasics(c, st)
				a.composeEditorSection(c, st)
				ui.Divider(c)
				a.composeModules(c, st)
				a.composeMessages(c, st)
			})
		})

		ui.Divider(c)
		a.composeFooter(c, st)
	})
}

// composeBasics 是节点、匿名与标题三块。节点是必填项：发帖必须给 node_id
// （uuid），不是 slug，所以下拉框的显示名在提交前会换回 uuid。
func (a *app) composeBasics(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(12).AlignItems(ui.End).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, "节点").FontSize(12).TextColor(t.TextMuted)
			if len(st.nodes) > 0 {
				options := make([]string, 0, len(st.nodes))
				for _, n := range st.nodes {
					options = append(options, n.display())
				}
				ui.Select(c, &st.nodeName, options).Label("节点").FillWidth()
				return
			}
			// 节点还没到（或取不到）时不给一个空的下拉框：说明正在做什么，
			// 失败时把原因和服务端的原话摆出来。
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if st.nodeLoading {
					ui.Spinner(c).Size(12, 12)
				}
				msg := "正在读取节点…"
				switch {
				case st.nodeErr != "":
					msg = "节点读取失败：" + st.nodeErr
				case st.nodeLoaded:
					// 读到了但一个节点都没有：接口异常或权限问题，不是「还在读」。
					msg = "没有可用的节点，请稍后重试，或到站点发帖。"
				}
				ui.Text(c, msg).FontSize(12).TextColor(t.TextMuted).MaxLines(2).Grow(1)
				if st.nodeErr != "" || st.nodeLoaded {
					if ui.Button(c, "重试").Clicked() {
						st.nodesAt = time.Time{}
						a.ensureComposeNodes()
					}
				}
			})
		})
		ui.Toggle(c, &st.anonymous, "匿名发帖").Label("匿名发帖")
	})

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

	// 这三个是可选的补充信息，收在一行里，不抢标题和正文的位置。
	ui.Row(c).FillWidth().Gap(10).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, "标题前缀").FontSize(12).TextColor(t.TextMuted)
			ui.TextInput(c, &st.titlePrefix).Label("标题前缀").
				Placeholder("可选，如「求助」").FillWidth().MaxLength(composeTitlePrefixMax)
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, "状态").FontSize(12).TextColor(t.TextMuted)
			ui.TextInput(c, &st.status).Label("状态").
				Placeholder("可选").FillWidth().MaxLength(composeStatusMax)
		})
	})
	ui.Row(c).FillWidth().Gap(10).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, "提示信息").FontSize(12).TextColor(t.TextMuted)
			ui.TextInput(c, &st.tip).Label("提示信息").
				Placeholder("可选").FillWidth().MaxLength(composeTipMax)
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, "联系方式").FontSize(12).TextColor(t.TextMuted)
			ui.TextInput(c, &st.contact).Label("联系方式").
				Placeholder("可选").FillWidth().MaxLength(composeContactMax)
		})
	})
}

// composeEditorSection 是正文区：上面是 Markdown 工具条与编辑 / 预览切换，
// 下面是输入框或预览。
func (a *app) composeEditorSection(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		// 标题行：区块名、编辑 / 预览切换与字数。切换放在工具条外面，因为
		// 工具条窄了会把末尾的控件收进「»」菜单——模式切换不该被藏起来。
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "正文").FontSize(12).TextColor(t.TextMuted).Grow(1)
			ui.Segmented(c, &st.preview, "编辑", "预览").Label("正文视图")
			count := ui.Text(c, fmt.Sprintf("%d / %d 字",
				utf8.RuneCountInString(st.body), composeContentMax)).
				FontSize(11).TextColor(t.TextMuted).SingleLine()
			if n := utf8.RuneCountInString(st.body); n > composeContentMax {
				count.TextColor(t.Danger)
			}
		})
		ui.Column(c).FillWidth().Radius(10).Background(t.Background).
			Border(1, t.Border).Children(func() {
			// 工具条固定在输入框上方（「头部」），滚动正文时始终可见。
			ui.Row(c).FillWidth().Padding(2, 4).Children(func() {
				a.composeMarkdownToolbar(c, st)
			})
			ui.Divider(c)
			if st.preview == 1 {
				a.composePreview(c, st)
				return
			}
			// 可访问名用「正文内容」：区块标题已经叫「正文」，两者重名会让
			// 读屏与测试分不清点的是哪一块。
			area := ui.TextArea(c, &st.body).Label("正文内容").
				Border(0, t.Border).Background(ui.Transparent).FillWidth().
				Lines(composeEditorLines, composeEditorMaxLine).
				Placeholder("支持 Markdown：**加粗**、# 标题、> 引用、- 列表、`代码`、[链接](url)、![图片](url) 与 :smile: 表情").
				Bind(&st.bodyField)
			a.syncComposeSelection(area, st)
		})
	})
}

// composeMarkdownToolbar 是正文的编辑工具条：一排 Markdown 标记按钮。窗口
// 太窄时放不下的按钮会收进工具条末尾的「»」菜单，仍然可用。
func (a *app) composeMarkdownToolbar(c *ui.Context, st *composeState) {
	ui.Toolbar(c, func() {
		for _, b := range markdownButtons {
			if ui.Button(c, b.label).Tooltip(b.hint).Clicked() {
				st.applyMarkdown(b.format)
			}
		}
	}).Label("编辑工具").FillWidth()
}

// markdownButton 是工具条上的一项：显示文案、悬停说明与它插入的标记。
type markdownButton struct {
	label  string
	hint   string
	format composeFormat
}

var markdownButtons = []markdownButton{
	{"加粗", "**加粗**", composeBold},
	{"斜体", "*斜体*", composeItalic},
	{"删除线", "~~删除线~~", composeStrike},
	{"代码", "`行内代码`", composeInlineCode},
	{"标题", "## 标题", composeHeading},
	{"引用", "> 引用", composeQuote},
	{"代码块", "```代码块```", composeCodeBlock},
	{"列表", "- 列表项", composeBulletList},
	{"编号", "1. 编号列表", composeOrderedList},
	{"链接", "[文字](url)", composeLink},
	{"图片", "![说明](url)", composeImage},
	{"分割线", "---", composeDivider},
}

// composePreview 是正文的预览：与详情页同一套 Markdown 渲染（一个回车换行、
// 表情与配图都按文章正文的方式画），所以预览里看到的就是发表后的样子。
func (a *app) composePreview(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Scroll(c).FillWidth().Height(composePreviewHeight).Children(func() {
		ui.Column(c).FillWidth().Padding(12, 14).Children(func() {
			if strings.TrimSpace(st.body) == "" {
				ui.Text(c, "还没有正文。切回「编辑」写点什么，这里会按发表后的样子显示。").
					FontSize(12).TextColor(t.TextMuted).MaxLines(3)
				return
			}
			a.markdownView(c, st.body, a.md)
		})
	})
}

// syncComposeSelection 处理工具条插入标记之后的选区：pendingSel 是刚算好的
// 位置，交给同一个构建帧里的输入框；否则把当前选区记下来，供下一帧的工具条
// 按钮使用（元素出构建帧就失效，所以选区必须先读进状态）。
func (a *app) syncComposeSelection(area ui.Element, st *composeState) {
	if st.pendingSel.ok {
		area.SetTextSelection(st.pendingSel.start, st.pendingSel.end)
		st.selStart, st.selEnd = st.pendingSel.start, st.pendingSel.end
		st.pendingSel = composeSelection{}
		return
	}
	st.selStart, st.selEnd = area.TextSelection()
}

/* ------------------------------------------------------------------
   附加组件
---------------------------------------------------------------- */

// composeModules 是发帖接口支持的可选块。默认全部收起：大多数帖子不需要
// 它们，展开哪一块就发哪一块的字段，没展开的一个字段都不发。
func (a *app) composeModules(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Text(c, "附加组件").FontSize(12).Bold().TextColor(t.Text).SingleLine()
		ui.Collapsible(c, "投票", &st.voteOpen, func() { a.composeVote(c, st) })
		ui.Collapsible(c, "评分", &st.scoreOpen, func() { a.composeScore(c, st) })
		ui.Collapsible(c, "竞猜", &st.betOpen, func() { a.composeBet(c, st) })
		ui.Collapsible(c, "兑换码 / 红包", &st.redeemOpen, func() { a.composeRedeem(c, st) })
		ui.Collapsible(c, "金币池", &st.poolOpen, func() { a.composePool(c, st) })
		ui.Collapsible(c, "可见性（谁可以看）", &st.guardOpen, func() { a.composeGuard(c, st) })
	})
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
		// 选项是动态行：每行一个输入框 + 删除按钮，末尾是「添加选项」。
		// 行数受 1～20 限制，删除最后一行的按钮在只剩一行时禁用。
		for i := range st.voteOptions {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c, &st.voteOptions[i]).
					Label(fmt.Sprintf("选项 %d", i+1)).
					Placeholder(fmt.Sprintf("选项 %d", i+1)).
					FillWidth().MaxLength(composeOptionTextMax)
				remove := ui.Button(c, "删除").Disabled(len(st.voteOptions) <= 1)
				if remove.Clicked() {
					st.voteOptions = append(st.voteOptions[:i], st.voteOptions[i+1:]...)
				}
			})
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			add := ui.Button(c, "添加选项").Disabled(len(st.voteOptions) >= composeOptionMax)
			if add.Clicked() {
				st.voteOptions = append(st.voteOptions, "")
			}
			ui.Spacer(c).Grow(1)
		})
		ui.Toggle(c, &st.voteResultVisible, "公开投票结果")
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
		for i := range st.betOptions {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.TextInput(c, &st.betOptions[i]).
					Label(fmt.Sprintf("竞猜选项 %d", i+1)).
					Placeholder(fmt.Sprintf("选项 %d", i+1)).
					FillWidth().MaxLength(composeBetOptionMax)
				remove := ui.Button(c, "删除").Disabled(len(st.betOptions) <= composeBetOptionsMin)
				if remove.Clicked() {
					st.betOptions = append(st.betOptions[:i], st.betOptions[i+1:]...)
				}
			})
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			add := ui.Button(c, "添加选项").Disabled(len(st.betOptions) >= composeBetOptionsMax)
			if add.Clicked() {
				st.betOptions = append(st.betOptions, "")
			}
			ui.Spacer(c).Grow(1)
		})
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, "参与截止时间").FontSize(12).TextColor(t.TextMuted)
			ui.TextInput(c, &st.betDeadline).Label("竞猜参与截止时间").
				Placeholder("可选，如 2026-11-20 12:00").FillWidth()
		})
	})
}

// composeRedeem 是兑换码 / 红包块：一行一个兑换码，加上说明、每人可领数量
// 与可选的红包。红包金额只有文档给出的六档，用分段控件选。
func (a *app) composeRedeem(c *ui.Context, st *composeState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, "兑换码（一行一个）").FontSize(12).TextColor(t.TextMuted)
			ui.TextArea(c, &st.redeemCodes).Label("兑换码").
				Placeholder("一行一个兑换码，也可以用逗号分隔").
				FillWidth().Lines(4, 10).MaxLength(composeCodeMax * (composeCodeTextMax + 1))
		})
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
		ui.Toggle(c, &st.redPacket, "同时开红包")
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
			node := st.nodeName
			*st = *newComposeState()
			st.nodeName = node
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
	// 幂等键在第一次提交时铸造，超时重试沿用同一个；发表成功后清掉。
	if st.key == "" {
		st.key = randomURLSafe(24)
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
			if a.onPostPublished != nil {
				a.onPostPublished(created, nil)
			}
		})
	}()
}
