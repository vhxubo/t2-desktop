package main

import (
	"log"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	updatenative "github.com/egoist/mygo/plugins/updater/native"
)

/* ------------------------------------------------------------------
   生产启动链：从建 app 到进主循环的顺序
--------------------------------------------------------------- */

// startupHost 是启动链用到的系统能力。生产实现直接调 mygo，测试用假实现
// 记录调用顺序，并以同样的顺序触发 WhenReady 回调：这样「先建 app → 再注册
// 协议 → 就绪后装通知 → 最后开窗口」以及晚赋值状态对 UI 可见，都有回归覆盖。
type startupHost interface {
	configureHTTP(*desktop)
	onOpenURL(func(string))
	requestSingleInstanceLock() bool
	registerURLScheme(string) error
	onNotificationClick(func(string))
	onDidBecomeActive(func())
	onBeforeQuit(func(*mygo.QuitEvent))
	notificationsSupported() bool
	nativeNotifier() func(nativeNotice) error
	whenReady(func())
	startBackground(*desktop, *app)
	run() error
}

// runStartup 按生产顺序装配进程级单例、主窗口和系统事件。测试通过假 host
// 走完全部步骤，不需要真实 GUI、网络或主循环。
func runStartup(d *desktop, host startupHost) error {
	a := newMainApp(d)
	d.app = a
	host.configureHTTP(d)

	// 深链回调先注册，再取单实例锁：浏览器跳回时系统拉起的是新进程，
	// 它把命令行转给第一个实例，转发可能早于下面几行执行。
	host.onOpenURL(d.handleDeepLink)
	if !host.requestSingleInstanceLock() {
		return nil
	}
	// 注册晚于 newMainApp；失败只能写进程级状态，窗口实时查询（见
	// schemeWarning）。打包版也由 mygo.json 的 urlSchemes 声明。
	if err := host.registerURLScheme(appIdentifier); err != nil {
		d.schemeWarn = "系统未把 " + appIdentifier + " 关联到本应用（" + err.Error() + "）：授权后浏览器不会自动跳回，请复制地址栏里的完整链接粘贴到登录页完成授权。"
		log.Printf("运行时注册 %s 未成功（打包版由 mygo.json 的 urlSchemes 声明；仍可手动粘贴回调链接）：%v", appIdentifier, err)
	} else {
		log.Printf("回调协议已注册：%s", appIdentifier)
	}
	// 原生通知点击、前台校准和退出事件都在主循环之前注册，早到的事件不丢。
	host.onNotificationClick(d.openNotice)
	host.onDidBecomeActive(d.onBecameActive)
	host.onBeforeQuit(func(*mygo.QuitEvent) {
		d.markQuitting()
		d.stopUpdateLoop()
		// 退出前把队列写完；最多等 200 ms，超时丢弃剩余日志，不让退出卡住。
		log.Printf("退出应用")
		flushLogSink(logFlushTimeout)
	})
	host.whenReady(func() {
		// 原生通知要等应用就绪：Windows 走托盘气泡，macOS 首次会询问授权。
		// 它晚于 app 构建，所以写进程级状态，窗口与发送路径实时查询。
		if host.notificationsSupported() {
			d.notify = host.nativeNotifier()
		}
		// 从这里开始才有主循环：后台结果可以投到 UI 线程。
		d.mainLoop = true
		host.startBackground(d, a)
	})
	return host.run()
}

// mygoStartupHost 是生产用的启动链实现。它只封装原生调用，顺序由 runStartup
// 决定，测试覆盖的也是同一条链。
type mygoStartupHost struct{}

func (mygoStartupHost) configureHTTP(d *desktop) {
	// 先把代理规则接到进程内所有 HTTP 出口，再装更新插件：更新插件自己
	// 发请求（检查更新、下载更新包），不经过本应用的 Client。
	installGlobalProxy(d.proxy)
	// 下载加速入口：设置里选了镜像时，检查更新与更新包下载都经它转发。
	applyUpdateMirror(d.updateMirror.current())
	// 更新通道：默认接收全部版本，只有「稳定渠道」才只读 latest release。
	applyUpdateChannel(d.updateChannel.prerelease())
	// 更新包来自 GitHub Release，由 mygo build 签名，native 更新窗口只负责
	// 手动检查；后台检查和下载由应用自己的循环负责。
	mygo.Use(updatenative.New(updater.Options{DisableAutomaticChecks: true}))
	d.updates = true
	// 保留插件侧的选择同步：手动检查窗口里的同名开关与偏好保持一致。
	applyUpdateIdleAutoDownload(d.updateIdlePrefs.on() || d.updateChannel.prerelease())
	watchIdleAutoDownload(func() bool { return d.updateChannel.prerelease() })
	updater.OnInstalled(func() {
		d.setUpdateStatus(updateStatus{
			Phase: updateReady, Version: updater.AvailableVersion(), Percent: 100,
		})
	})
	// 先按保存的外观模式设置原生外观，让启动首帧、原生更新/通知窗口与
	// theme.json 一致；自绘界面仍由 view 每帧设置。
	mygo.Theme.SetSource(themeSource(d.theme.Mode()))
}

func (mygoStartupHost) onOpenURL(fn func(string)) { mygo.App.OnOpenURL(fn) }
func (mygoStartupHost) requestSingleInstanceLock() bool {
	return mygo.App.RequestSingleInstanceLock()
}
func (mygoStartupHost) registerURLScheme(id string) error {
	return mygo.App.RegisterURLScheme(id)
}
func (mygoStartupHost) onNotificationClick(fn func(string)) {
	mygo.App.OnNotificationClick(fn)
}
func (mygoStartupHost) onDidBecomeActive(fn func()) { mygo.App.OnDidBecomeActive(fn) }
func (mygoStartupHost) onBeforeQuit(fn func(*mygo.QuitEvent)) {
	mygo.App.OnBeforeQuit(fn)
}
func (mygoStartupHost) notificationsSupported() bool             { return mygo.NotificationsSupported() }
func (mygoStartupHost) nativeNotifier() func(nativeNotice) error { return showNativeNotice }
func (mygoStartupHost) whenReady(fn func())                      { mygo.App.WhenReady(fn) }

func (mygoStartupHost) startBackground(d *desktop, a *app) {
	// 图片缓存是进程级的一份，清理循环也只起一次。
	d.startImageCacheMaintenance()
	// 后台更新循环：间隔可在设置里即时切换，装完只标记待重启。
	d.startUpdateLoop()
	// 托盘图标：应用就绪后才有原生通知区域，创建失败只记日志。
	d.startTray()
	// 应用与托盘图标跟随深浅外观切换，放在托盘之后，首帧就同时设好。
	d.startIcons()
	// 打开唯一的主窗口。没登录时它显示登录页，登录后显示内容。
	a.openMainWindow()
	// 老板键是系统级热键，窗口建好后再注册；早于窗口按下不会记下空状态。
	d.applyBossKey()
}

func (mygoStartupHost) run() error { return mygo.App.Run() }
