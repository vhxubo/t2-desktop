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

// 稍后再看 / 浏览历史窗口：首页页头右上角的「稍后再看」入口打开（见
// watchLaterEntry），一扇窗口里分「稍后再看」「浏览历史」两页。
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
		Content:           ui.View(a.watchLaterWindowView),
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
				Description: "在帖子详情页点「稍后再看」，或从首页入口回来继续读。",
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
	titleColor := tk.Text
	if read {
		titleColor = tk.TextSecondary
	}

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
				Description: "打开过的帖子会记在这里，离线也能回看缓存过的正文。",
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
		a.rowWith(c, a.readingItem(rows[i]), a.openDetail)
	}).Grow(1).Gap(0)
}

/* ------------------------------------------------------------------
   首页入口与详情页动作
--------------------------------------------------------------- */

// watchLaterEntry 是首页页头右上角的入口：书签图标 + 「稍后再看」，有待读
// 条目时带上数量。放在首页而不是侧边栏：它属于「读什么」的动线，和列表在
// 一起；浏览历史在打开的窗口里切页即可。
func (a *app) watchLaterEntry(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	n := a.watchLaterCount()
	label := "稍后再看"
	if n > 0 {
		label = fmt.Sprintf("稍后再看 %d", n)
	}
	b := action.Button(c, "", action.ButtonProps{Size: lobetheme.Small})
	b.Label(label).Tooltip("打开稍后再看与浏览历史")
	b.Children(func() {
		ui.Icon(c, lucide.Bookmark()).FontSize(14).TextColor(tk.TextSecondary)
		ui.Text(c, label).SingleLine()
	})
	if b.Clicked() {
		a.showWatchLater()
	}
	b.AlignSelf(ui.Center).Shrink(0)
}

// detailWatchLaterAction 是详情页操作组里那枚「稍后再看」图标：不在列表里
// 时加入，已在列表里时移出。文案跟着状态走，点一下就有明确结果。
func (a *app) detailWatchLaterAction(d *detailState) detailToolAction {
	in := a.inWatchLater(d.shortID)
	icon, label, tip := lucide.BookmarkPlus(), "加入稍后再看", "把这篇文章放进稍后再看，稍后从首页入口继续读"
	if in {
		icon, label, tip = lucide.BookmarkMinus(), "移出稍后再看", "这篇文章已在稍后再看里，点一下移出"
	}
	return detailToolAction{
		key: "later", icon: icon, label: label, tip: tip,
		onClick: func() { a.toggleWatchLaterFromDetail(d) },
	}
}

// toggleWatchLaterFromDetail 处理详情页那枚图标：加入 / 移出都在这里落账，
// 结果用详情窗口自己的 toast 说明。
func (a *app) toggleWatchLaterFromDetail(d *detailState) {
	if d == nil || d.shortID == "" {
		return
	}
	added := a.toggleWatchLater(detailWatchLaterItem(d), d.topic)
	if added {
		a.toast(a.detailToastFor(d), "已加入稍后再看")
		return
	}
	a.toast(a.detailToastFor(d), "已移出稍后再看")
}

// detailWatchLaterItem 从正文状态整理出加入稍后再看所需的列表字段。正文还
// 没回来时只有 short_id 与链接，标题等字段等 updateWatchLaterTopic 补。
func detailWatchLaterItem(d *detailState) Item {
	it := Item{ShortID: d.shortID, URL: d.originURL()}
	if d.topic != nil {
		it.Title = d.topic.Title
		it.Author = d.topic.Author
		it.AuthorAvatar = d.topic.Avatar
		it.AuthorBadges = d.topic.Badges
		if d.topic.NodeName != "" {
			it.SubtitlePrefix = d.topic.NodeName + " · "
		}
	}
	return it
}
