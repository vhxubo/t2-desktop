package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
)

// 「关注与粉丝」窗口：数据与请求在 follows.go，这里只负责窗口开关与界面。
//
// 入口是「我的」窗口概览里的「粉丝」「关注」两个统计块：这两个数字本来
// 只说明有多少人，点不进去，名单端点到位后它们才真正可用。窗口里两个
// 分段（我关注的人 / 关注我的人）各自分页，行内的按钮直接回关 / 取关，
// 点昵称打开用户资料卡。

// 窗口的默认尺寸。和通知、我的两个窗口一样用 StateKey 记住位置与大小，
// 这里只是首次打开时的默认值。
const (
	followsWindowWidth  = 460
	followsWindowHeight = 620
	followsWinMinWidth  = 360
	followsWinMinHeight = 440
)

/* ------------------------------------------------------------------
   入口、显示与关闭
---------------------------------------------------------------- */

// showFollows 是窗口的全局入口：任何窗口、任何 goroutine 都能调用。
// tab 决定打开后落在哪个分段（粉丝 / 关注）。
func (a *app) showFollows(tab int) {
	a.publish(func() { a.showFollowsWindow(tab) })
}

// showFollowsWindow 在主线程上真正打开窗口。打开时补齐当前分段的首屏。
func (a *app) showFollowsWindow(tab int) {
	if !a.auth.loggedIn() {
		a.requestLoginFor(toastProfile, "登录 2Libra 后才能查看关注与粉丝。", func() { a.showFollows(tab) })
		return
	}
	if a.follows == nil {
		a.follows = &followsState{tab: followsFollowing}
	}
	if tab >= 0 && tab < followsKindCount {
		a.follows.tab = tab
	}
	a.ensureFollowsLoaded()
	// headless 测试没有主循环：碰 mygo 的窗口会等待一个不存在的处理者，
	// 所以只把状态准备好，测试直接渲染 followsWindowView。
	if a.win == nil {
		return
	}
	if win := a.followsWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		a.setFollowsWindowShown(true)
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle("关注与粉丝"),
		Width:             followsWindowWidth,
		Height:            followsWindowHeight,
		MinWidth:          followsWinMinWidth,
		MinHeight:         followsWinMinHeight,
		StateKey:          "follows",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden,
		HideWindowButtons: true,
		Content:           ui.View(a.followsWindowView),
	})
	a.followsWin = win
	win.OnShow(func() { a.setFollowsWindowShown(true) })
	win.OnHide(func() { a.setFollowsWindowShown(false) })
	win.OnClosed(func() {
		a.followsWin = nil
		a.setFollowsWindowShown(false)
		a.authGateWindowClosed(toastFollows)
		a.accountWindowClosed(win)
	})
	a.setFollowsWindowShown(true)
}

// setFollowsWindowShown 记下显示状态并让主窗口重画一次。只在主线程调用。
func (a *app) setFollowsWindowShown(shown bool) {
	if a.followsShown == shown {
		return
	}
	a.followsShown = shown
	if a.win != nil {
		a.win.Invalidate()
	}
}

// closeFollowsWindow 关掉窗口：退出登录、主窗口关闭或收进托盘时调用，
// 别把上一个账号的名单留在屏幕上。只在主线程调用。
func (a *app) closeFollowsWindow() {
	win := a.followsWin
	if win == nil {
		return
	}
	a.followsWin = nil
	a.setFollowsWindowShown(false)
	win.Close()
}

// redrawFollowsWindow 让窗口按最新状态重建。只在主线程调用。
func (a *app) redrawFollowsWindow() {
	if win := a.followsWin; win != nil {
		win.Invalidate()
	}
}

/* ------------------------------------------------------------------
   窗口界面
---------------------------------------------------------------- */

func (a *app) followsWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	// 名单里的用户标识可能把链接交给系统浏览器，服务取本窗口自己的那份。
	a.srv = c.Services()
	c.Root().Background(t.Background)
	a.drainToasts(c, a.claimsToasts(toastFollows))
	c.OnShortcut(0, ui.KeyEscape, a.closeFollowsWindow)

	fs := a.follows
	if fs == nil {
		return // 没有状态就不画：showFollowsWindow 会建好它
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Text(c, "关注与粉丝").FontSize(13).Bold().SingleLine().TextColor(t.Text)
				if l := fs.list(fs.tab); l.loaded {
					ui.Textf(c, "%s 人", followPageLabel(l)).FontSize(11).
						TextColor(t.TextMuted).SingleLine()
				}
				a.accountWindowTag(c)
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := a.followsWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", a.closeFollowsWindow)
		})
		ui.Divider(c)
		ui.Column(c).Grow(1).MinHeight(0).Padding(12, 14).Gap(10).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Tabs(c, &fs.tab, followsTabLabels[:]...)
				ui.Spacer(c).Grow(1)
				if l := fs.list(fs.tab); l.loaded {
					a.smallRefreshButton(c, l.loading, func() { a.loadFollows(fs.tab, 1) })
				}
			})
			a.followsListBody(c, fs)
		})
	})
	a.ensureFollowsLoaded()
	a.authDialogFor(c, toastFollows)
}

// followsListBody 画当前分段的名单：加载中、失败、空与列表四种状态。
func (a *app) followsListBody(c *ui.Context, fs *followsState) {
	t := c.Theme()
	kind := fs.tab
	if kind < 0 || kind >= followsKindCount {
		kind, fs.tab = followsFollowing, followsFollowing
	}
	l := fs.list(kind)
	switch {
	case !l.loaded || (l.loading && len(l.items) == 0):
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 24})
			ui.Text(c, "正在读取"+followsTabLabels[kind]+"…").
				FontSize(12).TextColor(t.TextMuted)
		})
		return
	case l.err != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(10).Children(func() {
			ui.Text(c, "加载失败").FontSize(14).Bold()
			ui.Text(c, l.err).FontSize(12).TextColor(t.Danger).MaxLines(3)
			if ui.Button(c, "重试").Clicked() {
				a.loadFollows(kind, 1)
			}
		})
		return
	case len(l.items) == 0:
		empty := followsEmptyText[kind]
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(6).Children(func() {
			ui.Text(c, empty[0]).FontSize(13).TextColor(t.TextMuted)
			ui.Text(c, empty[1]).FontSize(11).TextColor(t.TextMuted)
		})
		return
	}

	items := l.items
	l.list.Key = func(i int) any {
		if i >= 0 && i < len(items) {
			if items[i].ID != "" {
				return items[i].ID
			}
			return items[i].Username
		}
		return i
	}
	ui.List(c, &l.list, len(items), func(i int) {
		a.followsRow(c, fs, kind, items[i])
	}).Grow(1).MinHeight(0).Gap(6)

	if l.moreErr != "" {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "加载更多失败："+l.moreErr).FontSize(11).
				TextColor(t.Danger).MaxLines(2).Grow(1)
			a.smallAction(c, "重试", false, func() { a.loadFollows(kind, l.page+1) })
		})
		return
	}
	// 服务端给了总数且已经全部拿到时不必再点一次：否则「加载更多」会为
	// 一个注定空的结果多留一个按钮，点下去才知道没有下一页。
	if !l.noMore && !(l.total > 0 && len(l.items) >= l.total) {
		btn := ui.Button(c, "加载更多").Disabled(l.loading)
		if btn.Clicked() {
			a.loadFollows(kind, l.page+1)
		}
	}
}

// followsRow 是名单里的一行：头像、昵称（可点开资料卡）、关注时间，右侧是
// 关注 / 回关 / 取关按钮。
func (a *app) followsRow(c *ui.Context, fs *followsState, kind int, u followUser) {
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(8, 10).Radius(10).Gap(10).
		AlignItems(ui.Center).Background(t.Background).Border(1, t.Border).
		Children(func() {
			avatarView(c, u.Username, a.avatarBitmap(u.Avatar), 32)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					name := ui.Button(c, u.Username).
						Background(ui.Transparent).Border(0, t.Border).
						Padding(0, 0).FontSize(13).Bold()
					if name.Hovered() {
						name.TextColor(t.Accent)
					} else {
						name.TextColor(t.Text)
					}
					name.Label("用户 " + u.Username).Tooltip("打开用户资料")
					if name.Clicked() {
						a.showUserCard(u.Username)
					}
					if u.IsPro {
						ui.Text(c, "PRO").FontSize(10).TextColor(t.Accent).Shrink(0)
					}
				})
				if when := followRowSummary(u); when != "" {
					ui.Text(c, when).FontSize(11).TextColor(t.TextMuted).SingleLine()
				}
			})
			a.followRowButton(c, fs, kind, u)
		})
}

// followRowButton 是行内的关注按钮：已关注时可取关，未关注时是「回关」。
func (a *app) followRowButton(c *ui.Context, fs *followsState, kind int, u followUser) {
	t := c.Theme()
	busy := fs.busy == u.ID
	text, tip := "回关", "关注这位用户"
	if u.IsFollowing {
		text, tip = "已关注", "点一下取消关注"
	}
	if !u.FollowKnown {
		tip = "关注这位用户"
	}
	btn := ui.Button(c, text).
		Background(ui.Transparent).
		Border(1, t.Border).
		FontSize(metaSmallFontSize).
		Padding(3, 10).
		Radius(999).
		Shrink(0)
	hovered := btn.Hovered() && !busy
	ink := t.TextMuted
	if u.IsFollowing {
		ink = t.Accent
	}
	if hovered {
		ink = t.Text
		btn.Background(t.SurfaceHover)
	}
	if busy {
		btn.Disabled(true)
		tip = "正在提交…"
	}
	btn.TextColor(ink)
	btn.Label(text + " " + u.Username).Tooltip(tip)
	if !busy {
		btn.OnClick(func() { a.toggleFollowRow(kind, u) })
	}
}
