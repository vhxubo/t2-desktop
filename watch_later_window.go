package main

import (
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/nav"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 稍后再看 / 浏览历史窗口：侧边栏「稍后再看」入口打开（见 sidebarNavItems），
// 一扇窗口里分「稍后再看」「浏览历史」两页。
//
// 两页的数据都只在本机、按账号隔离（见 watch_later.go 与 reading_history.go）：
// 换账号窗口时各自重新载入，清空只清当前账号。稍后再看的打开行为由设置窗口
// 「阅读」页决定：默认打开即移除，打开「保留」后留在列表里并区分已读未读。
//
// 界面走 Lobe UI：分段、按钮、Tag、空状态、滚动区分别由 action / display /
// feedback / layout 提供，颜色取 TokensOf(c)，和评分榜等附属窗口同一套观感。

const (
	watchLaterWindowWidth  = 560
	watchLaterWindowHeight = 680
	watchLaterWinMinWidth  = 420
	watchLaterWinMinHeight = 420
)

func (a *app) showWatchLater() {
	a.publish(a.showWatchLaterWindow)
}

func (a *app) showWatchLaterWindow() {
	a.loadWatchLater()
	a.loadReadingHistory()
	// headless 测试没有主循环：只准备状态，测试直接渲染 watchLaterWindowView。
	if a.win == nil {
		return
	}
	if win := a.watchLaterWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle("稍后再看"),
		Width:             watchLaterWindowWidth,
		Height:            watchLaterWindowHeight,
		MinWidth:          watchLaterWinMinWidth,
		MinHeight:         watchLaterWinMinHeight,
		StateKey:          "watch-later",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden,
		HideWindowButtons: true,
		Content:           ui.View(a.windowView(a.watchLaterWindowView)),
	})
	a.watchLaterWin = win
	win.OnClosed(func() {
		a.watchLaterWin = nil
		a.accountWindowClosed(win)
	})
	a.win.Invalidate()
}

func (a *app) closeWatchLaterWindow() {
	win := a.watchLaterWin
	a.watchLaterWin = nil
	if win != nil {
		win.Close()
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// redrawWatchLaterWindow 在列表变化后让窗口按新数据重建。窗口没开时是空操作。
func (a *app) redrawWatchLaterWindow() {
	if win := a.watchLaterWin; win != nil {
		win.Invalidate()
	}
}

func (a *app) watchLaterToast() toastTarget { return toastWatchLater }

/* ------------------------------------------------------------------
   窗口
--------------------------------------------------------------- */

func (a *app) watchLaterWindowView(c *ui.Context) {
	a.applyTheme(c)
	tk := lobetheme.TokensOf(c)
	a.srv = c.Services()
	c.Root().Background(tk.BgLayout)
	c.OnShortcut(0, ui.KeyEscape, a.closeWatchLaterWindow)
	defer a.drainToasts(c, a.claimsToasts(toastWatchLater))

	// 每帧确认内存里的两份列表属于当前账号：换账号、重新登录后自动换成
	// 新的作用域，不会把上一个账号的记录画出来。
	a.loadWatchLater()
	a.loadReadingHistory()

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、最小化与关闭，画法与其它窗口一致。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(tk.BgLayout).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, lucide.Bookmark()).Size(16, 16).TextColor(tk.Primary)
				display.Text(c, "稍后再看", display.TextProps{Strong: true, FontSize: 13, Rows: 1})
				a.accountWindowTag(c)
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := a.watchLaterWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", a.closeWatchLaterWindow)
		})
		layout.Divider(c, layout.DividerProps{})

		a.watchLaterTabBar(c)
		layout.Divider(c, layout.DividerProps{})

		ui.Column(c).Grow(1).MinHeight(0).Padding(6, 10).Children(func() {
			if a.laterTab == 1 {
				a.readingHistoryBody(c)
				return
			}
			a.watchLaterBody(c)
		})
	})
}

// watchLaterTabBar 是窗口顶部的一条：分页切两页，右侧是当前页的条数与清空
// 入口。清空只清当前账号，不碰服务端的收藏 / 书签。
func (a *app) watchLaterTabBar(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	ui.Row(c).FillWidth().Padding(6, 14).Gap(8).AlignItems(ui.Center).Children(func() {
		c.Key("watch-later-tabs")
		sel := a.laterTab
		nav.Tabs(c, &sel, nav.TabsProps{
			Items:   []nav.TabItem{{Label: "稍后再看"}, {Label: "浏览历史"}},
			Variant: nav.TabsSquare,
			Size:    lobetheme.Small,
		}, nil)
		if sel != a.laterTab && sel >= 0 && sel < 2 {
			a.laterTab = sel
			a.laterList.ScrollTo(0, ui.Start)
			a.readingList.ScrollTo(0, ui.Start)
		}
		ui.Spacer(c).Grow(1)
		count := len(a.later)
		if a.laterTab == 1 {
			count = len(a.reading)
		}
		if count > 0 {
			label := fmt.Sprintf("共 %d 条", count)
			// 保留模式下顺手写出未读数：已读的条目还留在列表里，只写总数
			// 会让人以为还有这么多没读。
			if a.laterTab == 0 && a.watchLaterPrefs.keepAfterOpen() {
				label = fmt.Sprintf("共 %d 条 · %d 未读", count, a.watchLaterCount())
			}
			display.Text(c, label, display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
			clearLabel := "清空稍后再看"
			if a.laterTab == 1 {
				clearLabel = "清空浏览历史"
			}
			b := action.Button(c, "清空", action.ButtonProps{
				Size: lobetheme.Small, Variant: action.Text,
			})
			b.Label(clearLabel).Tooltip("只清空当前账号的本机记录")
			b.TextColor(tk.Error)
			if b.Clicked() {
				if a.laterTab == 1 {
					a.clearReadingHistory(toastWatchLater)
					return
				}
				a.clearWatchLater()
			}
		}
	})
}

/* ------------------------------------------------------------------
   稍后再看列表
--------------------------------------------------------------- */

func (a *app) watchLaterBody(c *ui.Context) {
	if len(a.later) == 0 {
		ui.Column(c).Grow(1).Center().Padding(24).Children(func() {
			display.Empty(c, display.EmptyProps{
				Icon:        lucide.Bookmark(),
				Title:       "还没有稍后再看",
				Description: "在文章列表或详情页点「加入稍后再看」，再从侧边栏回来继续读。",
			})
		})
		return
	}
	rows := a.later
	a.laterList.Key = func(i int) any {
		if i >= 0 && i < len(rows) {
			return rows[i].ShortID
		}
		return i
	}
	ui.List(c, &a.laterList, len(rows), func(i int) {
		a.watchLaterRow(c, rows[i])
	}).Grow(1).Gap(0)
}

// watchLaterRow 画一条待读记录：标题、节点 / 作者 / 加入时间，右侧是已读
// 状态与移除按钮。整行可点，点开正文；打开后是否留在列表里由偏好决定
// （见 handleWatchLaterOpen）。
func (a *app) watchLaterRow(c *ui.Context, r watchLaterRecord) {
	tk := lobetheme.TokensOf(c)
	keep := a.watchLaterPrefs.keepAfterOpen()
	read := keep && !r.ReadAt.IsZero()

	title := r.Title
	if title == "" {
		title = "帖子 " + r.ShortID
	}
	titleColor := postReadInk(tk, read)

	row := feedRowSurface(c).PaddingY(10).PaddingX(12)
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
			ui.Text(c, title).FontSize(tk.FontSizeLG).Bold().MaxLines(2).TextColor(titleColor)
			ui.Row(c).MinWidth(0).AlignItems(ui.Center).Wrap().Font(sidebarFont).Children(func() {
				if r.Node != "" {
					ui.Text(c, r.Node).FontSize(tk.FontSizeSM).TextColor(tk.TextDescription).SingleLine()
				}
				if r.Author != "" {
					if r.Node != "" {
						ui.Text(c, " · ").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription).SingleLine()
					}
					a.userTag(c, userTagSpec{
						Name:   r.Author,
						Avatar: r.Avatar,
						Size:   tk.FontSizeSM - 1,
						Color:  tk.TextDescription,
						Badges: r.Badges,
					})
				}
				if when := watchLaterTimeText(r.AddedAt); when != "" {
					sep := " · "
					if r.Node == "" && r.Author == "" {
						sep = ""
					}
					ui.Text(c, sep+when).FontSize(tk.FontSizeSM).
						TextColor(tk.TextDescription).SingleLine()
				}
			})
		})
		ui.Row(c).Shrink(0).Gap(6).AlignItems(ui.Center).Children(func() {
			// 默认模式没有「已读」概念：打开过的条目已经不在列表里了。
			if keep {
				if read {
					display.Tag(c, "已读", display.TagProps{Size: lobetheme.Small})
				} else {
					display.Tag(c, "未读", display.TagProps{
						Size: lobetheme.Small, Color: display.TagProcessing,
					})
				}
			}
			rm := action.StyleActionIcon(c, ui.ButtonBase(c), lucide.X(), action.ActionIconProps{
				BlockSize: 24, GlyphSize: 14,
			})
			rm.Label("移出稍后再看")
			lobeoverlay.Tooltip(c, rm, "从稍后再看移除", lobeoverlay.TooltipProps{})
			if rm.Clicked() {
				a.removeWatchLater(r.ShortID)
			}
		})
	})
	row.OnClick(func() {
		a.openDetail(a.watchLaterItem(r))
		// 默认偏好下点开即移除：给一句说明，免得列表里的条目突然消失让人
		// 以为点错了。保留模式下它只是变成已读，不需要提示。
		if !a.watchLaterPrefs.keepAfterOpen() {
			a.toast(toastWatchLater, "已从稍后再看移除")
		}
	})
}

/* ------------------------------------------------------------------
   浏览历史列表
--------------------------------------------------------------- */

func (a *app) readingHistoryBody(c *ui.Context) {
	if len(a.reading) == 0 {
		ui.Column(c).Grow(1).Center().Padding(24).Children(func() {
			display.Empty(c, display.EmptyProps{
				Icon:        lucide.Clock(),
				Title:       "还没有浏览记录",
				Description: "打开过的帖子会记在这里，随时可以回来接着看。",
			})
		})
		return
	}
	rows := a.reading
	a.readingList.Key = func(i int) any {
		if i >= 0 && i < len(rows) {
			return rows[i].ShortID
		}
		return i
	}
	ui.List(c, &a.readingList, len(rows), func(i int) {
		// 浏览历史就是「读过的东西」，行尾不再挂加入稍后再看的按钮。
		a.rowWithNoWatchLater(c, a.readingItem(rows[i]), a.openDetail)
	}).Grow(1).Gap(0)
}

/* ------------------------------------------------------------------
   侧边栏入口、列表行动作与详情页动作
--------------------------------------------------------------- */

// feedWatchLaterGap 是文章行里稍后再看动作与前面发帖时间之间的间距。
// feedWatchLaterTextWidth 固定文案占位，加入 / 移除两种文案长短不同也不让整行重排。
const (
	feedWatchLaterGap       float32 = 8
	feedWatchLaterTextWidth float32 = 72
)

// feedWatchLaterButton 是文章行副标题里紧跟发布时间的稍后再看动作。它和同一行
// 的「最后回复」用同一套展示与点击：等宽小字、平时 TextDescription，悬停变 Primary
// 并加下划线、手型光标、Tooltip，点整段文字生效。未加入时是「稍后再看」，点一下
// 加入；已加入时换成「移出稍后再看」，再点一下移除。只改本地清单，不开正文。
//
// 它是悬浮动作：enabled 决定这一类行是否支持（浏览历史就不支持），revealed
// 决定此刻是否可见。整块始终构建并占住同一格位置，不可见时用 Invisible 隐藏——
// 它保留布局空间、不绘制也不吃输入。若按悬浮与否来增删元素，副标题会突然多出
// 一段文字，整行重排，指针下的元素随之变化，视觉上就是来回抖动。固定宽度则保证
// 加入 / 移除切换时同样不重排。
func (a *app) feedWatchLaterButton(c *ui.Context, it Item, enabled, revealed bool) {
	if it.ShortID == "" || !enabled {
		return
	}
	tk := lobetheme.TokensOf(c)
	in := a.inWatchLater(it.ShortID)
	face, label, tip := "稍后再看", "加入稍后再看", "把这篇文章放进稍后再看"
	if in {
		face, label, tip = "移出稍后再看", "移出稍后再看", "把这篇文章从稍后再看移除"
	}
	link := ui.Row(c).Shrink(0).AlignItems(ui.Center).Font(sidebarFont).
		Width(feedWatchLaterTextWidth).MarginStart(feedWatchLaterGap)
	ink := tk.TextDescription
	if link.Hovered() {
		ink = tk.Primary
	}
	link.Children(func() {
		t := ui.Text(c, face).FontSize(tk.FontSizeSM).TextColor(ink).SingleLine().Shrink(0)
		if link.Hovered() {
			t.Underline()
		}
	})
	link.Label(label).Cursor(ui.CursorPointer)
	link.OnClick(func() {
		if a.toggleWatchLater(it, nil) {
			a.toast(a.watchLaterToast(), "已加入稍后再看")
		} else {
			a.toast(a.watchLaterToast(), "已移出稍后再看")
		}
	})
	link.Tooltip(tip)
	if !revealed {
		link.Invisible()
	}
}
