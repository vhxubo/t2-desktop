package main

import (
	"context"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 主窗口的生命周期
//
// 应用只有一个主窗口：关掉它只丢弃这一份界面状态，本机缓存（阅读记录、
// 已读、金币缓存、正文缓存）都在磁盘上，重开就回来。

// subWindowTitle 是附属窗口的标题。集中在这里，是给所有弹窗一个统一的
// 取名入口；应用只有一个账号，标题不需要再加身份前缀。
func (a *app) subWindowTitle(base string) string { return base }

// windowTitle 是主窗口标题，昵称读到时带上它，方便任务栏与 Alt+Tab 辨认。
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
		// 所有窗口都从这一份偏好取缩放：文字、图标与布局一起放大，窗口
		// 尺寸不变。Ctrl+滚轮在根元素上接管，内层控件没吃掉时由它处理。
		c.SetZoom(a.zoom())
		c.Root().HandleInput(a.zoomInput)
		fn(c)
	}
}

// windowStateKey 是窗口位置与大小的记忆键，重开时回到原处。
func (a *app) windowStateKey() string { return "main" }

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
	a.win = a.newAppWindow(opts, toastMain)
	// 首次启动声明只认领一次。
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
	// 打开应用主窗口时静默签到一次：已签到不提示，新签到才在内容顶部
	// 弹一条明显的结果。偏好关掉或本窗口已经试过时是空操作。
	a.maybeAutoSignIn()
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
	a.authGateWindowClosed(toastMain)
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

// auxiliaryWindows 是这个账号除主窗口外的全部弹窗窗口。主窗口关掉后靠它
// 判断账号是否还活着、并挑一扇顶上来当 publish / toast 的出口。新增弹窗窗口
// 必须加进这里，否则主窗口一关就会被误当成最后一扇窗口而收掉账号。
func (a *app) auxiliaryWindows() []*mygo.Window {
	return []*mygo.Window{
		a.notifWin, a.settingsWin, a.aboutWin, a.netLogWin,
		a.profileWin, a.coinWin, a.composeWin,
		a.replyWin, a.editWin, a.nodeEditWin, a.searchWin, a.followsWin,
		a.userCardWin, a.viewerWin,
	}
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
	for _, win := range a.auxiliaryWindows() {
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
	for _, win := range a.auxiliaryWindows() {
		if win != nil {
			return true
		}
	}
	return false
}

// finalizeAccountWindow 是所有窗口都没了之后的收尾：停掉长连接与定时器和
// 落盘草稿。迟到的窗口回调可能再走到这里，finalized 保证只收一次。
func (a *app) finalizeAccountWindow() {
	if a.finalized {
		return
	}
	a.finalized = true
	// 最后一扇窗口也没了：没有帧尾能再算草稿指纹，先同步落盘并停掉定时器。
	a.flushDrafts()
	a.stopDraftTimer()
	// 后台刷新跟着窗口生命周期：只要还有一扇窗口开着，通知长连接就继续
	// 更新；最后一扇关掉才停。金币数只在展开菜单时请求，这里丢弃在途请求。
	a.stopCoinTagWatch()
	a.stopNotificationWatch()
	if a.netStatusUnwatch != nil {
		a.netStatusUnwatch()
		a.netStatusUnwatch = nil
	}
	a.win = nil
}

// closeAuxiliaryWindows 关掉除主窗口外的每一扇弹窗。所有 mygo 窗口两两独立，
// 只有「关闭全部」和退出这类明确要收掉一切的入口才调用它；关主窗口本身绝不
// 走这里。新增弹窗窗口必须加进 auxiliaryWindows 和这里，免得漏收。
// 只在主线程调用。
func (a *app) closeAuxiliaryWindows() {
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
}

// closeAllWindows 关掉全部窗口：先收弹窗，再销毁主窗口。用 Destroy 而不是
// Close：这时不该再弹「退出还是收进托盘」（见 window_mode.go）。
func (a *app) closeAllWindows() {
	a.stopCoinTagWatch()
	a.stopNotificationWatch()
	a.closeAuxiliaryWindows()
	if a.netStatusUnwatch != nil {
		a.netStatusUnwatch()
		a.netStatusUnwatch = nil
	}
	win := a.win
	a.win = nil
	if win != nil {
		win.Destroy()
	}
}
