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

// windowView 是所有窗口 view 的统一入口：记下这一帧属于哪扇窗口。正文图与
// 表情在渲染时发起预取，加载完成后按这个记录只重绘那一扇窗口（见
// imageFetcher），不再把主窗口、详情窗口和十来个附属窗口全部重建一遍。
func (a *app) windowView(fn func(*ui.Context)) func(*ui.Context) {
	return func(c *ui.Context) {
		a.frameSvc = c.Services()
		a.images.beginFrame(a.frameSvc)
		defer func() {
			a.images.endFrame()
			a.frameSvc = ui.Services{}
		}()
		fn(c)
	}
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
		Content:           ui.View(a.windowView(a.view)),
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
	// 首次启动声明只认领一次：几个账号窗口接连打开时也只由第一扇弹。
	if a.disclaimer.claim() {
		a.disclaimerOpen = true
	}
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
	// 首页标题栏的潘多拉事件名也来自这一份静默读取。
	a.loadPandoraEntry()
	// 令牌里已经缓存了用户名与头像地址；这里再校准一次，老会话也能补上
	// 头像，并在加载完成后刷新标题栏。
	a.ensureAvatar()
	// 访问权限同样属于老会话要补齐的信息：官方图床只对 Pro 会员与守护者开放，
	// 令牌里还没记过就现在读一次。
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

// onMainWindowClosed 是主窗口真的关掉之后：其它窗口一律留着。详情窗口、
// 通知 / 设置 / 金币等附属窗口都是各自的 mygo 窗口，关主窗口不该把它们一起
// 关了——那样既丢了正在读的帖子，也会在恰好没有详情窗口时误触 mygo 的
// 「最后一个窗口关了就退出」。把还活着的一扇顶上来当 publish / toast 的
// 出口，等这个账号最后一扇窗口关掉再收尾（见 accountWindowClosed）。
func (a *app) onMainWindowClosed() {
	a.mainClosed = true
	// 主窗口没了就没有帧尾了；此时别的窗口可能还在，但先落一次盘兜底。
	a.flushDrafts()
	a.accountWindowClosed(a.win)
}

// accountWindowClosed 在任意一扇窗口关掉后调用（窗口的 OnClosed 回调、只在
// 主线程）：主窗口还开着时什么也不做；主窗口已关时把还活着的一扇顶上来当
// 排队的窗口，让后台结果和 toast 仍有落点。这个账号再没有别的窗口时，才
// 真正收掉它的界面状态。
func (a *app) accountWindowClosed(closed *mygo.Window) {
	if a.win == closed {
		a.win = nil
	}
	if !a.mainClosed {
		return
	}
	// 留一扇活着窗口顶上；详情窗口还带着正文状态，也要有人接着读。
	a.win = a.anyLiveWindow()
	if a.detail == nil {
		for _, h := range a.detailWindows {
			if !h.removed {
				a.detail = h.current()
				break
			}
		}
	}
	if a.hasLiveWindow() {
		return
	}
	a.finalizeAccountWindow()
}

// anyLiveWindow 找一扇这个账号还开着的窗口：详情窗口优先，然后是通知、
// 设置、金币等附属窗口。主窗口关掉后用它顶上 a.win；无头测试里原生窗口
// 都是 nil，这里也只会返回 nil，生命周期改由 hasLiveWindow 判断。
func (a *app) anyLiveWindow() *mygo.Window {
	for _, h := range a.detailWindows {
		if !h.removed && h.win != nil {
			return h.win
		}
	}
	for _, win := range []*mygo.Window{
		a.notifWin, a.settingsWin, a.aboutWin, a.netLogWin,
		a.profileWin, a.coinWin, a.composeWin,
		a.replyWin, a.editWin, a.nodeEditWin, a.searchWin, a.followsWin,
		a.userCardWin, a.viewerWin,
	} {
		if win != nil {
			return win
		}
	}
	return nil
}

// hasLiveWindow 报告主窗口已关之后，这个账号是否还有别的窗口开着。详情窗口
// 的状态（而非原生窗口对象）算数：无头测试里没有原生窗口，但详情状态还在，
// 账号的生命周期就不能结束。
func (a *app) hasLiveWindow() bool {
	if !a.mainClosed {
		return true
	}
	for _, h := range a.detailWindows {
		if !h.removed {
			return true
		}
	}
	for _, win := range []*mygo.Window{
		a.notifWin, a.settingsWin, a.aboutWin, a.netLogWin,
		a.profileWin, a.coinWin, a.composeWin,
		a.replyWin, a.editWin, a.nodeEditWin, a.searchWin, a.followsWin,
		a.userCardWin, a.viewerWin,
	} {
		if win != nil {
			return true
		}
	}
	return false
}

// finalizeAccountWindow 是这个账号所有窗口都没了之后的收尾：通知长连接、
// 界面状态都交回账号册（账号本身仍挂在账号册里）。late 的窗口回调可能再
// 走到这里，finalized 保证只收一次。
func (a *app) finalizeAccountWindow() {
	if a.finalized {
		return
	}
	a.finalized = true
	// 最后一扇窗口也没了：没有帧尾能再算草稿指纹，先同步落盘并停掉定时器。
	a.flushDrafts()
	a.stopDraftTimer()
	// 后台刷新跟着账号的生命周期：只要还有一扇窗口开着，通知长连接就继续
	// 更新；最后一扇关掉才停。金币数只在展开账号菜单时请求，这里只丢弃在途
	// 的余额请求。
	a.stopCoinTagWatch()
	a.stopNotificationWatch()
	if a.netStatusUnwatch != nil {
		a.netStatusUnwatch()
		a.netStatusUnwatch = nil
	}
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
	a.closePandora()
	a.closeComposeWindow()
	a.closeReplyWindow()
	a.closeEditWindow()
	a.closeNodeEditWindow()
	a.closeSearchWindow()
	a.closeAboutWindow()
	a.closeNetLogWindow()
	a.closeFollowsWindow()
	a.closeUserCardWindow()
	a.closeViewerWindow()
	a.closeAllDetailWindows()
	if a.netStatusUnwatch != nil {
		a.netStatusUnwatch()
		a.netStatusUnwatch = nil
	}
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
