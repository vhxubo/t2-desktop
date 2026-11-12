package main

import (
	"context"
	"errors"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 金币窗口：余额、今日明细、流水、统计与排行从「我的」窗口里独立出来，成为
// 一个和通知 / 设置 / 我的一样的 mygo 窗口。入口是主窗口标题栏上登录账号
// 左侧的金币数（见 coinTag）；点一下打开，窗口已经开着时就把它带到前台。
//
// 数据与请求在 coins.go，窗口里的内容仍然由 coins_ui.go 的 coinsBody 画出；
// 这里只负责窗口的开关、窗口标题栏，以及标题栏金币数的定时刷新。

// 金币窗口的默认尺寸。和其他附属窗口一样用 StateKey 记住用户摆放的位置与
// 大小，所以这里只是首次打开时的默认值。
const (
	coinWindowWidth  = 560
	coinWindowHeight = 680
	coinWinMinWidth  = 460
	coinWinMinHeight = 480
)

// coinTagRefreshEvery 是标题栏金币数的刷新间隔：登录后先读一次，之后每隔
// 这么久再读一次余额。提成变量是为了测试能把它缩短。
var coinTagRefreshEvery = time.Minute

/* ------------------------------------------------------------------
   金币窗口：全局入口、显示与关闭
---------------------------------------------------------------- */

// showCoins 是金币窗口的全局入口：任何窗口、任何 goroutine 都能调用。它把
// 工作挪到主线程；窗口已经开着时只带到前台，不会开出第二个。
func (a *app) showCoins() {
	if a.onCoinsOpen != nil {
		a.onCoinsOpen()
	}
	a.publish(a.showCoinWindow)
}

// showCoinWindow 在主线程上真正打开金币窗口。打开时补齐首屏数据，第一帧里
// 就有余额与今日明细，而不是先看一会儿空壳。
func (a *app) showCoinWindow() {
	if !a.auth.loggedIn() {
		a.requestLogin("登录 2Libra 后才能查看金币。", a.showCoins)
		return
	}
	a.ensureCoinsLoaded()
	// headless 测试没有主循环：碰 mygo 的窗口会等待一个不存在的处理者，
	// 所以只把状态准备好，测试直接渲染 coinWindowView。
	if a.win == nil {
		return
	}
	if win := a.coinWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		a.setCoinWindowShown(true)
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             "金币",
		Width:             coinWindowWidth,
		Height:            coinWindowHeight,
		MinWidth:          coinWinMinWidth,
		MinHeight:         coinWinMinHeight,
		StateKey:          "coins",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口、通知窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.coinWindowView),
	})
	a.coinWin = win
	// 显示状态跟着窗口事件走：标题栏金币数据此高亮，不能只按窗口对象还
	// 在不在判断（用户可以最小化或者收起来）。
	win.OnShow(func() { a.setCoinWindowShown(true) })
	win.OnHide(func() { a.setCoinWindowShown(false) })
	win.OnClosed(func() {
		a.coinWin = nil
		a.setCoinWindowShown(false)
	})
	a.setCoinWindowShown(true)
}

// coinWindowVisible 报告金币窗口此刻是否在屏幕上：标题栏金币数据此高亮。
func (a *app) coinWindowVisible() bool { return a.coinShown }

// setCoinWindowShown 记下金币窗口的显示状态，并让主窗口重画一次。只在主线程
// 调用。
func (a *app) setCoinWindowShown(shown bool) {
	if a.coinShown == shown {
		return
	}
	a.coinShown = shown
	if a.win != nil {
		a.win.Invalidate()
	}
}

// closeCoinWindow 关掉金币窗口：退出登录或主窗口关闭时调用，别把上一个账号
// 的余额或者一个没有主窗口的孤岛留在屏幕上。只在主线程调用。
func (a *app) closeCoinWindow() {
	win := a.coinWin
	if win == nil {
		return
	}
	a.coinWin = nil
	a.setCoinWindowShown(false)
	win.Close()
}

// redrawCoinWindow 让金币窗口按最新状态重建。只在主线程调用。
func (a *app) redrawCoinWindow() {
	if win := a.coinWin; win != nil {
		win.Invalidate()
	}
}

/* ------------------------------------------------------------------
   标题栏金币数
---------------------------------------------------------------- */

// coinTag 是主窗口标题栏上登录账号左侧的金币数：💰 + 千分位余额。只有登录
// 用户才出现；余额还没读到时显示破折号，入口本身不隐藏。点它打开金币窗口。
func (a *app) coinTag(c *ui.Context) {
	t := c.Theme()
	cs := a.coins
	amount, known := "—", false
	if cs != nil && cs.balanceOK {
		amount, known = coinThousands(cs.balance), true
	}
	b := ui.Row(c).Padding(3, 8).Radius(6).Gap(4).AlignItems(ui.Center).
		Background(ui.Transparent)
	open := a.coinWindowVisible()
	if open || b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	ink := t.TextMuted
	switch {
	case open:
		ink = t.Accent
	case b.Hovered():
		ink = t.Text
	}
	b.Children(func() {
		// 图标放在数字左边；数字用千分位隔开，和金币窗口里的写法一致。
		ui.Text(c, "💰").FontSize(12)
		ui.Text(c, amount).FontSize(12).Bold().SingleLine().TextColor(ink)
	})
	hint := "查看金币余额、今日明细与流水"
	if known {
		hint = "金币余额 " + amount + "，点开金币窗口"
	}
	b.Label("金币余额").Tooltip(hint).OnClick(a.showCoins)
}

/* ------------------------------------------------------------------
   标题栏金币数的定时刷新
---------------------------------------------------------------- */

// startCoinTagWatch 在登录后启动金币数刷新：先立即读一次余额，再按
// coinTagRefreshEvery 定时重读。幂等：登录、回到前台、应用就绪都可调用；
// 已经在跑时只补一次立即校准，不会叠加定时器。
func (a *app) startCoinTagWatch() {
	if !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	a.refreshCoinTagBalance()
	a.scheduleCoinTagRefresh()
}

// scheduleCoinTagRefresh 排下一次刷新。gen 让 stop 之后已经排好的定时器不再
// 生效；到点后重新排下一次，形成固定间隔。
func (a *app) scheduleCoinTagRefresh() {
	if a.coinTagTimer != nil || !a.auth.loggedIn() {
		return
	}
	a.coinTagGen++
	gen := a.coinTagGen
	a.coinTagTimer = time.AfterFunc(coinTagRefreshEvery, func() {
		a.publish(func() {
			if a.coinTagGen != gen {
				return
			}
			a.coinTagTimer = nil
			if !a.auth.loggedIn() {
				return
			}
			a.refreshCoinTagBalance()
			a.scheduleCoinTagRefresh()
		})
	})
}

// stopCoinTagWatch 停掉定时刷新：退出登录、主窗口关闭时调用。seq 自增让还在
// 路上的请求结果直接被丢弃。
func (a *app) stopCoinTagWatch() {
	a.coinTagGen++
	if a.coinTagTimer != nil {
		a.coinTagTimer.Stop()
		a.coinTagTimer = nil
	}
	a.coinTagSeq++
	a.coinTagLoading = false
}

// refreshCoinTagBalance 只拉余额接口：标题栏不需要排名、签到与盲盒。结果写回
// 金币页共用的 a.coins，这样标题栏与金币窗口的数字始终一致；失败时保留上次
// 的值，标题栏不自作主张显示错误。
func (a *app) refreshCoinTagBalance() {
	if !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	cs := a.coins
	if cs == nil || a.coinTagLoading {
		return
	}
	a.coinTagLoading = true
	a.coinTagSeq++
	seq := a.coinTagSeq
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	go func() {
		defer cancel()
		balance, err := a.client.fetchCoinBalance(ctx)
		a.publish(func() {
			if a.coinTagSeq != seq {
				return
			}
			a.coinTagLoading = false
			if a.coins != cs || !a.auth.loggedIn() {
				return
			}
			if err != nil {
				// 令牌失效时把数字收起来，等重新登录；网络抖动则保留上次的值。
				var ae *apiError
				if errors.As(err, &ae) && (ae.Status == 401 || ae.Status == 403) {
					cs.balanceOK = false
				}
				return
			}
			cs.balance, cs.balanceOK = balance, true
		})
	}()
}

/* ------------------------------------------------------------------
   金币窗口的界面
---------------------------------------------------------------- */

// coinWindowView 是金币窗口的 view。窗口自己带标题栏与窗口按钮，所以这里不
// 需要遮罩，也不占主窗口的位置。
func (a *app) coinWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	// 排行榜里的用户标识可能把链接交给系统浏览器，服务取本窗口自己的那份。
	a.srv = c.Services()
	c.Root().Background(t.Background)
	// Esc 关闭窗口：随时可以从标题栏的金币数再打开。
	c.OnShortcut(0, ui.KeyEscape, a.closeCoinWindow)

	cs := a.coins
	if cs == nil {
		return // 没有状态就不画：newApp 与 onAuthChanged 都会建好它
	}
	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、右上角最小化与关闭，画法与主窗口一致。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Text(c, "💰").FontSize(13)
				ui.Text(c, "金币").FontSize(13).Bold().SingleLine().TextColor(t.Text)
				if cs.balanceOK {
					ui.Text(c, coinThousands(cs.balance)+" 金币").FontSize(11).
						TextColor(t.TextMuted).SingleLine()
				}
			})
			a.titleButton(c, titleMinimize, "最小化", func() {
				if win := a.coinWin; win != nil {
					win.Minimize()
				}
			})
			a.titleButton(c, titleClose, "关闭", a.closeCoinWindow)
		})
		ui.Divider(c)

		// 金币页自己也带内边距，放在这里时不再套一层。
		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			a.coinsBody(c)
		})
	})
	a.ensureCoinsLoaded()
	// 缺权限时也摆出同一个全局授权弹窗：从本窗口发起的请求失败后，用户就
	// 在这个窗口里看到原因与「前往授权」。
	a.authDialog(c)
}
