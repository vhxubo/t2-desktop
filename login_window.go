package main

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// scopeLabels 是 scope 的中文说明。profile.go 的「我的 · 权限与账号」与登录
// 窗口的「本次申请」共用这一份，两处文案始终一致；未列出的 scope 原样展示，
// 新 scope 上线时不会显示成空白，也不会假装认识它。
var scopeLabels = map[string]string{
	"profile":       "读取个人资料与头像",
	"read":          "读取公开内容",
	"read:me":       "读取我的内容（收藏 / 书签 / 通知）",
	"read:economy":  "读取金币余额、流水与签到状态",
	"write:comment": "发表评论",
	"write:topic":   "发表帖子、动态与投票",
	"write:economy": "金币变动（签到 / 打赏 / 佩戴徽章 / 解锁）",
	"interact":      "收藏、关注等互动",
	"upload":        "上传图片",
}

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
		b.loginURL = ""
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
	b.loginURL = ""
	win.OnClosed(func() {
		if b.loginWin != win {
			return
		}
		b.loginWin = nil
		b.loginView = nil
		b.loginURL = ""
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
	// 记住页面停在哪：站点登录完成后往往停在自己的页面上，授权流程不再继续，
	// 这时窗口要给出「重新授权」（见 loginLeftAuthorizePage）。站点是单页应用，
	// 登录与授权页之间可能没有整页跳转，所以站内切页（标题变化）也读一次地址。
	view.Page().OnDidNavigate(func(string) { b.postMain(b.syncLoginPage) })
	view.Page().OnPageTitleUpdated(func(*mygo.TitleEvent) { b.postMain(b.syncLoginPage) })
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
	b.loginURL = ""
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
	// 窗口在后台放了一会儿：重新读一次页面地址，底部该不该给「重新授权」按
	// 真实页面来。
	b.syncLoginPage()
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

// syncLoginPage 把内置浏览器当前所在的地址记下来，并据此决定要不要露出「重新
// 授权」。地址以 webview 自己报的为准：2Libra 站点是单页应用，登录与授权页之间
// 可能没有整页跳转，导航事件带过来的地址会过期。只在主线程调用；窗口已经收起时
// 迟到的回调不再管。
func (b *accountBook) syncLoginPage() {
	if b.loginWin == nil || b.loginView == nil {
		return
	}
	cur := b.loginView.Page().URL()
	if cur == "" || cur == b.loginURL {
		return
	}
	before := b.loginLeftAuthorizePage()
	b.loginURL = cur
	if b.loginLeftAuthorizePage() != before {
		b.redrawLoginWindow()
	}
}

// loginLeftAuthorizePage 报告内置浏览器是不是停在了授权页之外：站点登录完成后
// 常常停在自己的首页上，OAuth 流程不会自己继续，界面这时给出「重新授权」。
func (b *accountBook) loginLeftAuthorizePage() bool {
	if b.loginURL == "" {
		return false
	}
	p := b.pendingLogin()
	if p == nil || p.url == "" {
		return false
	}
	return !sameAuthorizePage(b.loginURL, p.url)
}

// sameAuthorizePage 比较两个地址是不是同一个页面：协议、主机与路径一致就算，
// 查询串不参与比较 —— 授权页的查询串里是 state 与 PKCE，重新发起授权会换一份。
func sameAuthorizePage(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil {
		return false
	}
	ub, err := url.Parse(b)
	if err != nil {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) &&
		strings.EqualFold(ua.Host, ub.Host) &&
		ua.EscapedPath() == ub.EscapedPath()
}

// reauthorizeLoginWindow 是窗口里的「重新授权」：用同一份临时会话重新发起
// 一次授权。站点登录完成后常常停在自己的页面上，OAuth 流程不会自己继续；这个
// 窗口里的 cookie 还在，重新发起会直接落到权限清单上。重新发起同时换掉
// state / PKCE，并把等待授权的时间重新计时（AuthManager 只等 5 分钟）。
func (b *accountBook) reauthorizeLoginWindow() {
	p := b.pendingLogin()
	if p == nil {
		return
	}
	authorize, err := p.auth.BeginScopes(context.Background(), nil)
	if err != nil {
		b.loginErr = "无法重新发起授权：" + friendlyError(err)
		b.redrawLoginWindow()
		return
	}
	b.mu.Lock()
	if b.pending == p {
		p.url = authorize
	}
	b.mu.Unlock()
	b.loginErr = ""
	if b.loginView != nil {
		if err := b.loginView.Page().LoadURL(authorize); err != nil {
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
	// 提示语要在拿 b.mu 之前算：它要按 id 查账号名（见 loginWaitingMessage）。
	msg := b.loginWaitingMessage(false, p.accountID)
	b.mu.Lock()
	if b.pending == p {
		b.pending.embedded = false
		b.loginMsg = msg
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
	title, subtitle, desc := b.loginWindowLabels()

	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, loginIcon).Size(16, 16).TextColor(t.Accent)
				ui.Text(c, title).FontSize(13).Bold().SingleLine().TextColor(t.Text)
				ui.Text(c, subtitle).FontSize(11).TextColor(t.TextMuted).SingleLine()
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
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, desc).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
				b.loginWindowScopes(c)
			})
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
		b.loginWindowFooter(c)
	})
}

// loginWindowScopes 列出这次授权地址里真正申请过的权限：授权页上也有一份
// 同样的清单，这里先写出来，用户不用开授权页就知道要点同意哪些权限，也能
// 核对「发动态要的 write:topic 到底申请了没有」。
func (b *accountBook) loginWindowScopes(c *ui.Context) {
	t := c.Theme()
	p := b.pendingLogin()
	if p == nil || p.auth == nil {
		return
	}
	scopes := p.auth.requestedScopesList()
	if len(scopes) == 0 {
		return
	}
	ui.Text(c, "本次申请：").FontSize(10).TextColor(t.TextMuted)
	ui.Row(c).FillWidth().Gap(4).Wrap().Children(func() {
		for _, scope := range scopes {
			chip := ui.Box(c).Padding(1, 6).Radius(6).
				Background(t.Surface).Border(1, t.Border)
			chip.Children(func() {
				ui.Text(c, scope).FontSize(10).TextColor(t.TextMuted).SingleLine()
			})
			chip.Label("申请权限 " + scope).Tooltip(scopeTooltip(scope))
		}
	})
}

// scopeTooltip 是 scope 的中文说明；没有说明时就写 scope 本身。
func scopeTooltip(scope string) string {
	if label := scopeLabel(scope); label != "" {
		return scope + "：" + label
	}
	return scope
}

// loginWindowGrant 说明这次授权回来的令牌实际拿到了哪些权限，以及申请过但
// 没拿到的（用户拒绝、或在站内「授权应用」页逐项关掉了某项）。后者必须当场
// 说出来：只申请不授予时，回头看「本次申请」会以为已经拿到了。
//
// 判断依据是授权地址里申请过的 scope（AuthManager.requestedScopesList）与
// 令牌里的 scope，不猜测服务端行为。
func (b *accountBook) loginWindowGrant(c *ui.Context) {
	p := b.pendingLogin()
	if p == nil {
		return
	}
	// 给已有账号补权限时，令牌在窗口自己的会话上（pending 里那次临时授权
	// 的令牌已经换成这个账号的存储了），从账号册取最准。
	if p.accountID != "" {
		if acc := b.account(p.accountID); acc != nil {
			b.loginWindowGrantFor(c, acc.auth)
			return
		}
	}
	b.loginWindowGrantFor(c, p.auth)
}

// loginWindowGrantFor 画某个会话的权限核对：授权地址里申请过的与令牌里拿到
// 的逐项对比。
func (b *accountBook) loginWindowGrantFor(c *ui.Context, auth *AuthManager) {
	t := c.Theme()
	if auth == nil {
		return
	}
	granted := auth.scopes()
	if len(granted) == 0 {
		return
	}
	got, missing := grantedAndMissing(auth.requestedScopesList(), granted)
	ui.Text(c, "已获得："+strings.Join(got, "、")).FontSize(10).
		TextColor(t.Text).SingleLine()
	if len(missing) > 0 {
		ui.Text(c, "未获得："+strings.Join(missing, "、")+
			"（在 2Libra 的「授权应用」页里被关掉或未同意；需要时重新授权并在授权页勾选）").
			FontSize(10).TextColor(t.Danger).MaxLines(3)
	}
}

// loginWindowLabels 是内置浏览器窗口的标题与说明：添加账号时是「登录另一个
// 账号」，给已有账号补权限时写明是哪个账号 —— 用户在那个窗口里登录，得先
// 知道该登谁。
func (b *accountBook) loginWindowLabels() (title, subtitle, desc string) {
	p := b.pendingLogin()
	if p == nil || p.accountID == "" {
		return "登录另一个账号", "独立会话",
			"这个窗口是独立会话：不会用到系统浏览器里的登录状态，" +
				"可以直接登录要挂的另一个账号。"
	}
	name := "这个账号"
	if acc := b.account(p.accountID); acc != nil {
		name = "「" + acc.name() + "」"
	}
	return "重新授权", name,
		"这个窗口是独立会话：不会用到系统浏览器里的登录状态，请登录要重新授权的" +
			name + "账号。"
}

// loginWindowFooter 是窗口底部的出口。站点登录完成后往往停在自己的页面上、
// OAuth 流程不再继续，所以离开授权页之后要多给一个「重新授权」：这个窗口里
// 已经登录，重新发起授权会直接进权限清单，不用再输一次密码。
func (b *accountBook) loginWindowFooter(c *ui.Context) {
	t := c.Theme()
	// 打不开授权页时由中间那块说明与「重试」负责，底部不再重复一个重新授权。
	left := b.loginLeftAuthorizePage() && b.loginErr == ""
	ui.Column(c).FillWidth().Padding(8, 12).Gap(8).Children(func() {
		b.loginWindowGrant(c)
		if left {
			ui.Text(c, "登录完成后如果页面没有回到授权页，点「重新授权」重新载入授权页："+
				"这个窗口已经登录，不用再输一次密码。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		}
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			if left {
				if ui.PrimaryButton(c, "重新授权").Clicked() {
					b.reauthorizeLoginWindow()
				}
			}
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
