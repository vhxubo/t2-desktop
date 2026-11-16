package main

import (
	"errors"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 内置浏览器授权窗口
//
// 第二个及以后的账号不能靠系统浏览器：那里的 2libra.com 会话多半还登着另一个
// 账号，授权页会直接把授权码发给它，用户只能先去网页端退出登录再来。内置
// 浏览器用一个独立的临时会话（内存里的 InPrivate / nonPersistent 存储）：它
// 没有系统浏览器里的任何 cookie，用户可以直接在窗口里登录要挂的另一个账号。
//
// 授权页把结果跳到本应用的回调地址（com.vhxubo.t2-desktop:/callback?code=…），
// 那是一个自定义协议，webview 不会加载它：我们在导航发生前拦下来，交给账号册
// 的 completeCallback —— 与系统浏览器深链走的是同一条完成路径，state 校验、
// 令牌交换、落库都只有一份实现。
//
// 窗口本身也是原生 UI：自绘标题栏 + 一行说明 + webview，底部留着「改用系统
// 浏览器」与「取消」两个出口。系统 webview 不可用（缺 WebView2 运行时、Linux
// 缺 WebKitGTK）时退回系统浏览器，并在面板上说明原因。

const (
	loginWindowWidth  = 560
	loginWindowHeight = 760
	loginWindowMinW   = 400
	loginWindowMinH   = 460
)

// openLoginWindow 打开内置浏览器授权窗口并载入授权页。返回错误表示内置浏览器
// 起不来，调用方据此退回系统浏览器。
func (b *accountBook) openLoginWindow(authorizeURL string) error {
	if !b.afterReady {
		return errLoginWindowNotReady
	}
	if win := b.loginWin; win != nil {
		// 已经开着：换到新的授权页（重新登录、重试都会走到这里）。
		b.loginErr = ""
		if b.loginView != nil {
			if err := b.loginView.Page().LoadURL(authorizeURL); err != nil {
				b.loginErr = "内置浏览器打不开授权页：" + err.Error()
			}
		}
		win.Invalidate()
		return nil
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             "登录另一个账号",
		Width:             loginWindowWidth,
		Height:            loginWindowHeight,
		MinWidth:          loginWindowMinW,
		MinHeight:         loginWindowMinH,
		StateKey:          "login",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(b.loginWindowView),
	})
	b.loginWin = win
	b.loginErr = ""
	win.OnClosed(func() {
		if b.loginWin != win {
			return
		}
		b.loginWin = nil
		b.loginView = nil
		// 关掉窗口就是放弃这次授权：迟到的回调不能再把账号签进来。
		if b.pendingLogin() != nil {
			b.cancelLogin()
		}
	})

	// 每次授权一份新的临时会话：上一个账号的 cookie 不该带进来，用户才能
	// 直接登录另一个账号。
	view, err := win.NewWebView(mygo.WebViewOptions{
		URL: authorizeURL,
		Page: mygo.PageOptions{
			Session: mygo.NewEphemeralSession(),
		},
	})
	if err != nil {
		b.loginErr = "内置浏览器不可用：" + err.Error()
		win.Invalidate()
		return err
	}
	b.loginView = view
	// 授权回调是自定义协议：拦下导航，把链接交给账号册。
	view.Page().OnWillNavigate(func(e *mygo.NavigateEvent) {
		if e == nil || !isCallbackLink(e.URL) {
			return
		}
		e.PreventDefault()
		raw := e.URL
		// 导航回调里不直接改状态：排到下一轮主线程，让这次导航先结束。
		b.postMain(func() { b.completeCallback(raw) })
	})
	// 有的后端把自定义协议当成「加载失败」报上来：同一个链接也认。
	view.Page().OnDidFailLoad(func(le *mygo.LoadError) {
		if le == nil || !isCallbackLink(le.URL) {
			return
		}
		raw := le.URL
		b.postMain(func() { b.completeCallback(raw) })
	})
	win.Invalidate()
	return nil
}

// closeLoginWindow 收起内置浏览器授权窗口。授权结束（成功或取消）时调用；
// 重复调用无害。
func (b *accountBook) closeLoginWindow() {
	win := b.loginWin
	if win == nil {
		return
	}
	b.loginWin = nil
	b.loginView = nil
	b.loginErr = ""
	win.Close()
}

// showLoginWindow 把内置浏览器授权窗口带到前台（面板上的「打开授权窗口」）。
func (b *accountBook) showLoginWindow() {
	win := b.loginWin
	if win == nil {
		// 窗口被关掉了就等于放弃了这次授权，这里只兜个底。
		if b.pendingLogin() != nil {
			b.cancelLogin()
		}
		return
	}
	if win.IsMinimized() {
		win.Restore()
	}
	win.Show()
	win.Focus()
	win.Invalidate()
}

// retryLoginWindow 是窗口里授权页加载失败后的「重试」：用同一个地址重新载入。
func (b *accountBook) retryLoginWindow() {
	p := b.pendingLogin()
	if p == nil || p.url == "" {
		return
	}
	b.loginErr = ""
	if b.loginView != nil {
		if err := b.loginView.Page().LoadURL(p.url); err != nil {
			b.loginErr = "内置浏览器打不开授权页：" + err.Error()
		}
	}
	b.redrawLoginWindow()
}

// useSystemBrowserForLogin 让这次授权改用系统浏览器：内置浏览器窗口收起，
// 授权地址交给系统浏览器打开。授权仍然挂在账号册上，深链回来了照样完成。
func (b *accountBook) useSystemBrowserForLogin() {
	p := b.pendingLogin()
	if p == nil || p.url == "" {
		return
	}
	b.mu.Lock()
	if b.pending == p {
		b.pending.embedded = false
		b.loginMsg = loginWaitingMessage(false)
	}
	b.mu.Unlock()
	b.closeLoginWindow()
	if err := b.openURL(p.url); err != nil {
		b.setLoginMsg("打开浏览器失败：" + err.Error())
		return
	}
	b.redrawPanel()
}

// redrawLoginWindow 让内置浏览器窗口按最新状态重建。
func (b *accountBook) redrawLoginWindow() {
	if win := b.loginWin; win != nil {
		win.Invalidate()
	}
}

// errLoginWindowNotReady 表示应用还没就绪（或没有主循环），建不了原生窗口。
var errLoginWindowNotReady = errors.New("应用还没就绪，暂时打不开内置浏览器")

/* ------------------------------------------------------------------
   内置浏览器窗口的界面
------------------------------------------------------------------ */

// loginWindowView 是内置浏览器授权窗口的 view：自绘标题栏、一行说明、webview，
// 底部是两个出口。webview 还没建出来或建失败时，同一块位置换成说明与按钮。
func (b *accountBook) loginWindowView(c *ui.Context) {
	c.SetTheme(b.theme.theme(c.Theme().Dark))
	t := c.Theme()
	c.Root().Background(t.Background)
	c.OnShortcut(0, ui.KeyEscape, b.cancelLogin)

	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, loginIcon).Size(16, 16).TextColor(t.Accent)
				ui.Text(c, "登录另一个账号").FontSize(13).Bold().SingleLine().TextColor(t.Text)
				ui.Text(c, "独立会话").FontSize(11).TextColor(t.TextMuted).SingleLine()
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := b.loginWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", b.cancelLogin)
		})
		ui.Divider(c)

		ui.Row(c).FillWidth().Padding(8, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "这个窗口是独立会话：不会用到系统浏览器里的登录状态，"+
				"可以直接登录要挂的另一个账号。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(3).Grow(1)
		})
		ui.Divider(c)

		// webview 占据剩余高度；没建出来时这块位置放说明。
		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			switch {
			case b.loginErr != "":
				b.loginWindowError(c)
			case b.loginView == nil:
				ui.Column(c).Fill().Center().Gap(8).Children(func() {
					ui.Spinner(c).Size(24, 24)
					ui.Text(c, "正在打开授权页…").FontSize(12).TextColor(t.TextMuted)
				})
			default:
				ui.WebView(c, b.loginView).Fill()
			}
		})

		ui.Divider(c)
		ui.Row(c).FillWidth().Padding(8, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "改用系统浏览器").Clicked() {
				b.useSystemBrowserForLogin()
			}
			ui.Spacer(c).Grow(1)
			if ui.Button(c, "取消").Clicked() {
				b.cancelLogin()
			}
		})
	})
}

// loginWindowError 是内置浏览器起不来或页面加载失败时的替代内容：说明原因，
// 并给出「重试」与「改用系统浏览器」两个出口。
func (b *accountBook) loginWindowError(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Fill().Center().Gap(10).Padding(24).Children(func() {
		ui.Text(c, "内置浏览器打不开授权页").FontSize(14).Bold().TextColor(t.Text)
		ui.Text(c, b.loginErr).FontSize(11).TextColor(t.Danger).MaxLines(4)
		ui.Text(c, "可以重试一次，或者改用系统浏览器完成这次授权。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "重试").Clicked() {
				b.retryLoginWindow()
			}
			if ui.PrimaryButton(c, "改用系统浏览器").Clicked() {
				b.useSystemBrowserForLogin()
			}
		})
	})
}
