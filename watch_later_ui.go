package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 稍后再看 / 浏览历史页面：侧边栏「稍后再看」入口打开（见 sidebarNavItems），
// 一页里分「稍后再看」「浏览历史」两段。它和首页、草稿箱一样是主窗口右侧
// 内容区的一个页面（pageName = "watchlater"），共用标题栏与侧边栏，不新开
// mygo 窗口。
//
// 两页的数据都只在本机、按账号隔离（见 watch_later.go 与 reading_history.go）：
// 换账号窗口时各自重新载入，清空只清当前账号。稍后再看的打开行为由设置窗口
// 「阅读」页决定：默认打开即移除，打开「保留」后留在列表里并区分已读未读。
//
// 界面走 Lobe UI：分段、按钮、Tag、空状态、滚动区分别由 action / display /
// feedback 提供，颜色取 TokensOf(c)，和主窗口其它页面同一套观感。

// showWatchLater 进入稍后再看页：和草稿箱一样只换主窗口右侧的内容区，
// 标题栏与侧边栏照常保留，任务栏里的窗口数不变。
func (a *app) showWatchLater() {
	a.loadWatchLater()
	a.loadReadingHistory()
	a.pageName = "watchlater"
	a.navSel = "watchlater"
	if a.win != nil {
		a.win.Invalidate()
	}
}

// redrawWatchLaterPage 在清单变化后让主窗口重建：页面内嵌在主窗口，侧边栏
// 角标也读这份数据，所以无论当前停在哪一页都要重画。窗口没开时是空操作。
func (a *app) redrawWatchLaterPage() {
	if a.win != nil {
		a.win.Invalidate()
	}
}

/* ------------------------------------------------------------------
   页面
--------------------------------------------------------------- */

func (a *app) watchLaterPage(c *ui.Context) {
	// 每帧确认内存里的两份列表属于当前账号：换账号、重新登录后自动换成
	// 新的作用域，不会把上一个账号的记录画出来。
	a.loadWatchLater()
	a.loadReadingHistory()

	tk := lobetheme.TokensOf(c)
	title := "稍后再看"
	if a.laterTab == 1 {
		title = "浏览历史"
	}
	// 页头和首页 feedHeader 同一套留白与字号：大标题写当前段名，下面的 tab
	// 只负责切换，不再兼当标题。整页左右收进 feedInset，和首页正文栏同宽。
	// 页头、tab、列表之间不画横线，留白本身就是分层。
	// Fill 而不是 FillWidth：根节点要占满内容区给出的高度，里面的列表才有
	// 一个固定的可视范围可以滚动；只给宽度时列表会按全部内容撑高，内容
	// 直接溢出窗口，滚轮也失效。
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).FillWidth().Shrink(0).
			Padding(24, a.feedInset(c)+16, 14, a.feedInset(c)+16).Children(func() {
			ui.Text(c, title).FontSize(tk.FontSizeHeading4).FontWeight(600).
				LetterSpacing(-0.4).TextColor(tk.TextHeading).SingleLine()
		})
		a.watchLaterTabBar(c)

		ui.Column(c).Grow(1).MinHeight(0).Padding(6, a.feedInset(c)).Children(func() {
			if a.laterTab == 1 {
				a.readingHistoryBody(c)
				return
			}
			a.watchLaterBody(c)
		})
	})
}

// watchLaterTabBar 是页头下方的一行：分段控件切两段，右侧是当前段的条数与
// 清空入口。分段和评分榜的排序条同一套（action.Segmented + Small），换成
// 胶囊轨道与滑动指示块，不再是方形下划线 tab。左右留白收进 feedInset，
// 和列表同一列宽。清空只清当前账号，不碰服务端的收藏 / 书签。
func (a *app) watchLaterTabBar(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	ui.Row(c).FillWidth().Shrink(0).Padding(0, a.feedInset(c), 8, a.feedInset(c)).
		Gap(8).AlignItems(ui.Center).Children(func() {
		c.Key("watch-later-tabs")
		// 和评分榜排序条同一套：Small 胶囊分段，绑定持久状态，切换由下面的
		// lastLaterTab 比对接住，顺手把两个列表滚回顶部。
		action.Segmented(c, &a.laterTab, []string{"稍后再看", "浏览历史"}, action.SegmentedProps{
			Size: lobetheme.Small,
		})
		if a.laterTab != a.lastLaterTab {
			a.lastLaterTab = a.laterTab
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
					a.clearReadingHistory(toastMain)
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

	row := feedRowSurface(c).PaddingY(a.rowPadY()).PaddingX(12)
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(a.rowGap()).Children(func() {
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
						Pro:    r.IsPro,
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
			a.toast(toastMain, "已从稍后再看移除")
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
			a.toast(toastMain, "已加入稍后再看")
		} else {
			a.toast(toastMain, "已移出稍后再看")
		}
	})
	link.Tooltip(tip)
	if !revealed {
		link.Invisible()
	}
}
