package main

import (
	"context"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 一个窗口（= 一个账号）的生命周期
//
// 多账号时每个账号一个主窗口：窗口关掉只丢弃这个窗口的界面状态，账号本身
// 仍留在账号册里（见 accounts.go），随时可以再打开；本机缓存（阅读记录、
// 已读、金币缓存、正文缓存）都在磁盘上按账号隔离，重开就回来。

// windowTitle 是窗口标题：带上账号名，任务栏与 Alt+Tab 里才分得清哪个窗口
// 是哪个账号。昵称还没读到时就是应用名。
func (a *app) windowTitle() string {
	if name := a.auth.username(); name != "" {
		return "T2 Desktop · " + name
	}
	return "T2 Desktop"
}

// windowStateKey 是窗口位置与大小的记忆键：每个账号一份，重开时回到原处。
func (a *app) windowStateKey() string {
	if a.accountID == "" {
		return "main"
	}
	return "main-" + a.accountID
}

// openMainWindow 建这个窗口的主窗口并接上关闭钩子。只在应用就绪后的主线程
// 调用：无头测试不经过这里，它们直接渲染 view。
func (a *app) openMainWindow() {
	if a.mainClosed {
		// 详情窗口还开着时主窗口曾被顶上来当排队的窗口；现在真要重开主窗口，
		// 先把这个引用让出来，详情窗口自己照旧活着。
		a.win = nil
		a.mainClosed = false
	}
	if a.win != nil {
		a.raiseMainWindow()
		return
	}
	opts := mygo.WindowOptions{
		Title:             a.windowTitle(),
		Width:             1080,
		Height:            720,
		MinWidth:          760,
		MinHeight:         520,
		StateKey:          a.windowStateKey(),
		Content:           ui.View(a.view),
		TitleBarStyle:     mygo.TitleBarHidden, // 隐藏原生标题栏
		HideWindowButtons: true,                // 连系统按钮也隐藏，完全自绘最小化/最大化/关闭
	}
	// 同时开出多个账号窗口时错开位置：默认全叠在屏幕中央的话，看起来就像
	// 只开了一个。用户自己调整过的位置由 mygo 的窗口状态记忆覆盖。
	if a.book != nil {
		if x, y := a.book.windowOrigin(a.accountID); x != 0 || y != 0 {
			opts.X, opts.Y = x, y
		}
	}
	a.win = mygo.NewWindow(opts)
	// 窗口真的关掉之后才轮到收尾：关闭按钮可能被「收进托盘」拦下来（见
	// window_mode.go），那种情况下窗口只是藏起来，状态什么都不用丢。
	a.win.OnClosed(a.onMainWindowClosed)
	a.win.OnClose(a.onMainWindowClose)
	a.afterMainWindowOpen()
}

// afterMainWindowOpen 是窗口建好之后要接上的东西：表情表、未读数、通知长
// 连接、金币数、潘多拉入口、头像与 Pro 状态。都在主线程。
func (a *app) afterMainWindowOpen() {
	// 表情表启动时就拉一次（文档同样建议启动时取并缓存）：等到第一次打开
	// 详情再拉，正文会先按 :code: 短码排一遍，短码变成图片时整段重排。
	a.ensureEmojis()
	if !a.auth.loggedIn() {
		return
	}
	a.refreshUnread()
	a.refreshFollowsUnread()
	a.startNotificationWatch()
	// 登录过的会话启动时就先把金币数读出来，之后定时刷新。
	a.startCoinTagWatch()
	// 首页标题栏的潘多拉事件名也来自这一份静默读取。
	a.loadPandoraEntry()
	// 令牌里已经缓存了用户名与头像地址；这里再校准一次，老会话也能补上
	// 头像，并在加载完成后刷新标题栏。
	a.ensureAvatar()
	// Pro 状态同样属于老会话要补齐的信息：官方图床只对 Pro 开放，令牌里
	// 还没记过就现在读一次。
	a.refreshProStatus(false)
	go func() {
		ctx := context.Background()
		token := a.auth.accessToken()
		_ = a.auth.verify(ctx, token)
		a.auth.fetchIdentity(ctx, token)
		a.publish(func() {
			a.status = a.auth.verifyStatus()
			a.ensureAvatar()
			if a.win != nil {
				a.win.SetTitle(a.windowTitle())
			}
			if a.book != nil && a.accountID != "" {
				a.book.noteAuthChanged(a.accountID)
			}
		})
	}()
}

// onMainWindowClosed 是主窗口真的关掉之后：附属窗口照旧收掉，但详情窗口
// 留着——它们是独立的 mygo 窗口，关主窗口不该把正在读的几篇帖子一起关了。
// 还有详情窗口时只把其中一扇顶上来当排队的窗口，等最后一扇详情窗口关掉
// 再收这个账号的界面状态（见 afterDetailWindowRemoved）。
func (a *app) onMainWindowClosed() {
	a.mainClosed = true
	a.stopCoinTagWatch()
	a.stopNotificationWatch()
	a.closeNotificationWindow()
	a.closeSettingsWindow()
	a.closeProfileWindow()
	a.closeCoinWindow()
	a.closePandoraWindow()
	a.closeComposeWindow()
	a.closeReplyWindow()
	a.closeEditWindow()
	a.closeSearchWindow()
	a.closeAboutWindow()
	a.closeNetLogWindow()
	a.closeFollowsWindow()
	a.closeUserCardWindow()
	a.closeViewerWindow()
	if len(a.detailWindows) > 0 {
		// 主窗口没了，但详情窗口还活着：留一扇做 publish / toast 的出口。
		if a.win == nil {
			for _, h := range a.detailWindows {
				if !h.removed && h.win != nil {
					a.win = h.win
					break
				}
			}
		}
		if a.detail == nil {
			for _, h := range a.detailWindows {
				if !h.removed {
					a.detail = h.current()
					break
				}
			}
		}
		return
	}
	a.finalizeAccountWindow()
}

// finalizeAccountWindow 是这个账号所有窗口都没了之后的收尾：通知长连接、
// 界面状态都交回账号册（账号本身仍挂在账号册里）。
func (a *app) finalizeAccountWindow() {
	a.win = nil
	if a.book == nil {
		return
	}
	if a.accountID != "" {
		a.book.windowClosed(a)
		return
	}
	a.book.guestWindowClosed(a)
}

// closeAllWindows 关掉这个窗口的全部窗口：先收附属窗口，再销毁主窗口。
// 退出账号、以及「未登录窗口让位给第一个账号窗口」都走这里。用 Destroy
// 而不是 Close：这时不该再弹「退出还是收进托盘」（见 window_mode.go）。
func (a *app) closeAllWindows() {
	a.stopCoinTagWatch()
	a.stopNotificationWatch()
	a.closeNotificationWindow()
	a.closeSettingsWindow()
	a.closeProfileWindow()
	a.closeCoinWindow()
	a.closePandoraWindow()
	a.closeComposeWindow()
	a.closeReplyWindow()
	a.closeEditWindow()
	a.closeSearchWindow()
	a.closeAboutWindow()
	a.closeNetLogWindow()
	a.closeFollowsWindow()
	a.closeUserCardWindow()
	a.closeViewerWindow()
	a.closeAllDetailWindows()
	win := a.win
	a.win = nil
	if win != nil {
		win.Destroy()
	}
	if a.book == nil {
		return
	}
	if a.accountID != "" {
		a.book.windowClosed(a)
		return
	}
	a.book.guestWindowClosed(a)
}
