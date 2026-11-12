package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 潘多拉窗口：独立于金币窗口的一个 mygo 窗口，入口是主窗口标题栏上的
// 「潘多拉」入口（登录后出现）。窗口里用一张大卡片展示今日抽奖进度与事件，
// 数据与动作在 pandora.go，卡片内容在 pandora_ui.go。
//
// 和通知 / 金币 / 我的窗口一样自绘标题栏、用 StateKey 记住位置与大小，
// 关掉后从标题栏入口再打开。

const (
	pandoraWindowWidth  = 520
	pandoraWindowHeight = 620
	pandoraWinMinWidth  = 420
	pandoraWinMinHeight = 480
)

// pandoraIcon 是礼物盒：标题栏入口、窗口标题与卡片都用同一个图形。
var pandoraIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 12v9H4v-9"/><path d="M2 7h20v5H2z"/><path d="M12 7v14"/><path d="M12 7H7.5a2.5 2.5 0 0 1 0-5C11 2 12 7 12 7z"/><path d="M12 7h4.5a2.5 2.5 0 0 0 0-5C13 2 12 7 12 7z"/></svg>`))

/* ------------------------------------------------------------------
   入口与窗口
--------------------------------------------------------------- */

// showPandora 是潘多拉窗口的全局入口：任何窗口、任何 goroutine 都能调用。
// 工作挪到主线程；窗口已经开着时只带到前台。
func (a *app) showPandora() {
	if a.onPandoraOpen != nil {
		a.onPandoraOpen()
	}
	a.publish(a.showPandoraWindow)
}

// showPandoraWindow 在主线程上真正打开潘多拉窗口。打开时补齐今日状态，
// 第一帧就有可以抽的结果，而不是先看一会儿空壳。
func (a *app) showPandoraWindow() {
	if !a.auth.loggedIn() {
		a.requestLogin("登录 2Libra 后才能打开潘多拉盲盒。", a.showPandora)
		return
	}
	if a.pandora == nil {
		a.pandora = newPandoraState()
	}
	a.pandora.owner = a.auth.username()
	a.ensurePandoraLoaded()
	// headless 测试没有主循环：碰 mygo 的窗口会等一个不存在的处理者，
	// 所以只把状态准备好，测试直接渲染 pandoraWindowView。
	if a.win == nil {
		return
	}
	if win := a.pandoraWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		a.setPandoraWindowShown(true)
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             "潘多拉",
		Width:             pandoraWindowWidth,
		Height:            pandoraWindowHeight,
		MinWidth:          pandoraWinMinWidth,
		MinHeight:         pandoraWinMinHeight,
		StateKey:          "pandora",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden,
		HideWindowButtons: true,
		Content:           ui.View(a.pandoraWindowView),
	})
	a.pandoraWin = win
	win.OnShow(func() { a.setPandoraWindowShown(true) })
	win.OnHide(func() { a.setPandoraWindowShown(false) })
	win.OnClosed(func() {
		a.pandoraWin = nil
		a.setPandoraWindowShown(false)
	})
	a.setPandoraWindowShown(true)
}

// pandoraWindowVisible 报告潘多拉窗口此刻是否在屏幕上：标题栏入口据此高亮。
func (a *app) pandoraWindowVisible() bool { return a.pandoraShown }

// setPandoraWindowShown 记下窗口显示状态，并让主窗口重画一次。只在主线程调用。
func (a *app) setPandoraWindowShown(shown bool) {
	if a.pandoraShown == shown {
		return
	}
	a.pandoraShown = shown
	if a.win != nil {
		a.win.Invalidate()
	}
}

// closePandoraWindow 关掉潘多拉窗口：退出登录或主窗口关闭时调用，别把上一
// 个账号的抽奖结果留在屏幕上。只在主线程调用。
func (a *app) closePandoraWindow() {
	win := a.pandoraWin
	if win == nil {
		return
	}
	a.pandoraWin = nil
	a.setPandoraWindowShown(false)
	win.Close()
}

// redrawPandoraWindow 让窗口按最新状态重建。只在主线程调用。
func (a *app) redrawPandoraWindow() {
	if win := a.pandoraWin; win != nil {
		win.Invalidate()
	}
}

// pandoraReady 回答标题栏入口要不要亮出「可抽」角标。窗口状态优先，其次
// 是启动时静默读到的入口状态；两者都没有时用金币窗口顺带读到的状态兜底。
func (a *app) pandoraReady() bool {
	if status, ok := pandoraDisplayStatus(a.pandora); ok {
		return status.readyToDraw(pandoraNow())
	}
	if cs := a.coins; cs != nil && cs.pandoraSeen {
		return cs.pandora.CanOpen
	}
	return false
}

// pandoraTag 是主窗口标题栏上的潘多拉入口：礼物盒 + 名字，登录后常驻，
// 未登录不画。抽中事件后名字直接换成事件名（例如「金币雨」），悬浮说明
// 里写具体效果；今天还能抽时右侧加一个「可抽」小角标。窗口开着时高亮。
func (a *app) pandoraTag(c *ui.Context) {
	t := c.Theme()
	open := a.pandoraWindowVisible()
	b := ui.Row(c).Padding(3, 8).Radius(6).Gap(5).AlignItems(ui.Center).
		Background(ui.Transparent)
	if open || b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	ink := t.Accent
	if open {
		ink = t.Accent
	} else if b.Hovered() {
		ink = t.Text
	}
	b.Children(func() {
		ui.Icon(c, pandoraIcon).Size(14, 14).TextColor(ink)
		ui.Text(c, pandoraEntryName(a.pandora)).FontSize(12).Bold().
			SingleLine().TextColor(ink)
		if a.pandoraReady() {
			ui.Box(c).Height(14).Padding(0, 4).Radius(999).Center().
				Background(t.Danger).Children(func() {
				ui.Text(c, "可抽").FontSize(9).Bold().SingleLine().
					TextColor(ui.Hex("#ffffff"))
			})
		}
	})
	b.Label("潘多拉").Tooltip(pandoraEntryHint(a.pandora)).OnClick(a.showPandora)
}

/* ------------------------------------------------------------------
   窗口界面
--------------------------------------------------------------- */

// pandoraWindowView 是潘多拉窗口的 view。窗口自带标题栏与窗口按钮，所以
// 这里不需要遮罩，也不占主窗口的位置。
func (a *app) pandoraWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	a.srv = c.Services()
	c.Root().Background(t.Background)
	// 本窗口里发起的抽奖结果以 toast 显示在这里；窗口不在了就退回主窗口。
	a.drainToasts(c, a.claimsToasts(toastPandora))
	c.OnShortcut(0, ui.KeyEscape, a.closePandoraWindow)

	st := a.pandora
	if st == nil {
		return // 没有状态就不画：newApp 与 onAuthChanged 都会建好它
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, pandoraIcon).Size(16, 16).TextColor(t.Accent)
				ui.Text(c, "潘多拉").FontSize(13).Bold().SingleLine().TextColor(t.Text)
				ui.Text(c, pandoraHeaderStatus(st)).FontSize(11).
					TextColor(t.TextMuted).SingleLine()
			})
			a.titleButton(c, titleMinimize, "最小化", func() {
				if win := a.pandoraWin; win != nil {
					win.Minimize()
				}
			})
			a.titleButton(c, titleClose, "关闭", a.closePandoraWindow)
		})
		ui.Divider(c)

		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			a.pandoraBody(c, st)
		})
	})
	a.ensurePandoraLoaded()
	a.authDialog(c)
}

// pandoraHeaderStatus 是窗口标题栏里的一行状态，只说明今天走到了哪一步。
func pandoraHeaderStatus(st *pandoraState) string {
	if st.loading && !st.loaded {
		return "正在读取今日盲盒…"
	}
	if st.wait {
		return "等待授权"
	}
	if st.err != "" {
		return "状态暂不可用"
	}
	now := pandoraNow()
	switch {
	case st.status.CanOpen:
		return "第一次抽奖待开启"
	case st.status.canReroll(now):
		return "第二次抽奖待重抽"
	case st.status.OpenedToday:
		return "今天的两次抽奖已完成"
	}
	return ""
}
