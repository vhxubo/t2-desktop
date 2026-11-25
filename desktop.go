package main

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/egoist/mygo"
)

// desktop 是进程级单例：一份登录会话、一套共享依赖、一个主窗口，以及
// 深链、通知、托盘与退出这些系统事件的接线。原生资源只在主线程创建和更新。
//
// 界面状态只有一份（app），读写的都是同一个 AuthManager；没有账号列表，
// 也没有后台保活循环 —— 登录态的刷新与未读数仍由这一份界面的 SSE / 轮询维护。

type desktop struct {
	dir        string
	proxy      *proxyManager
	theme      *themeManager
	images     *imageCache
	kv         *localKV
	closeMode  *closeModeStore
	disclaimer *disclaimerStore
	// commentCostPrefs 是评论费用的两个开关，进程级一份（见 comment_cost_prefs.go）。
	commentCostPrefs *commentCostPrefsStore
	// notificationPrefs 是通知偏好，进程级一份（见 notification_prefs.go）。
	notificationPrefs *notificationPrefsStore
	// watchLaterPrefs 是稍后再看的打开行为，进程级一份（见 watch_later_prefs.go）。
	watchLaterPrefs *watchLaterPrefsStore
	// imagePrefs 是图片显示的两项偏好，进程级一份（见 image_prefs.go）。
	imagePrefs *imagePrefsStore
	// typePrefs 是阅读排版的五项偏好，进程级一份（见 typography.go）。
	typePrefs *typographyStore
	// updateMirror 是更新包下载加速入口的选择，进程级一份（见 update_mirror.go）。
	updateMirror *updateMirrorStore
	// updateChannel 是「稳定渠道」的选择，进程级一份（见 update_channel.go）。
	updateChannel *updateChannelStore
	// updateIdlePrefs 是「闲时自动下载更新」的开关，进程级一份（见 update_idle_prefs.go）。
	updateIdlePrefs *updateIdlePrefsStore
	// updateIntervalPrefs 是后台检查更新间隔，进程级一份，默认 6 小时。
	updateIntervalPrefs *updateIntervalPrefsStore
	// updateMgr 是应用自己那套后台检查 / 下载 / 待重启状态（见 update_manager.go）。
	updateMgr *updateManager
	// autoSignPrefs 是「打开应用时自动签到」的开关，进程级一份（见
	// auto_sign_prefs.go），默认开启。
	autoSignPrefs *autoSignPrefsStore
	// sidebarPrefs 是首页到「节点」之前导航入口的显示与排序偏好，进程级一份
	// （见 sidebar_prefs.go）。
	sidebarPrefs *sidebarPrefsStore
	// commands 是命令中心的快捷键 / 收藏 / 最近使用偏好，进程级一份。
	commands *commandPrefs

	auth *AuthManager
	app  *app

	// 以下只在主线程读写。
	tray       *mygo.Tray
	trayReady  bool
	quitting   bool
	schemeWarn string
	updates    bool
	// mainLoop 表示应用主循环已经跑起来：只有这时才能把工作投到主线程。
	mainLoop bool
	// afterReady 表示 mygo 应用已经就绪，可以创建原生窗口了。
	afterReady bool

	// icons 管应用与托盘图标的深浅切换（见 app_icon.go）。进程级一份，
	// 只在主线程读写。
	icons *iconTheme
	// imgHTTP 是头像等长期缓存图片用的客户端，走配置的代理。
	imgHTTP *http.Client

	// notify / openURL / quitApp / closeAllWindows 是可以替换的系统动作：无头测试里
	// 换成记录调用，不碰原生窗口、浏览器与主循环。
	notify          func(nativeNotice) error
	openURL         func(string) error
	quitApp         func()
	closeAllWindows func()
}

// newDesktop 建进程级单例：装载共享依赖与唯一的会话。它只读本机文件，不碰
// 原生窗口，可以在 main 的最前面调用。
func newDesktop(dir string) *desktop {
	if dir == "" {
		dir = configDir()
	}
	d := &desktop{
		dir:                 dir,
		proxy:               newProxyManager(dir),
		theme:               newThemeManager(dir),
		images:              newImageCache(dir),
		kv:                  newOfflineKV(dir),
		closeMode:           newCloseModeStore(dir),
		disclaimer:          newDisclaimerStore(dir),
		commentCostPrefs:    newCommentCostPrefsStore(dir),
		notificationPrefs:   newNotificationPrefsStore(dir),
		watchLaterPrefs:     newWatchLaterPrefsStore(dir),
		imagePrefs:          newImagePrefsStore(dir),
		typePrefs:           newTypographyStore(dir),
		updateMirror:        newUpdateMirrorStore(dir),
		updateChannel:       newUpdateChannelStore(dir),
		updateIdlePrefs:     newUpdateIdlePrefsStore(dir),
		updateIntervalPrefs: newUpdateIntervalPrefsStore(dir),
		updateMgr:           newUpdateManager(),
		autoSignPrefs:       newAutoSignPrefsStore(dir),
		sidebarPrefs:        newSidebarPrefsStore(dir),
		commands:            newCommandPrefs(dir),
		openURL:             func(u string) error { return mygo.Shell.OpenExternal(u) },
	}
	d.icons = newIconTheme(d)
	d.imgHTTP = d.proxy.Client(20 * time.Second)
	d.auth = newAuthManagerAt(d.dir, d.proxy)
	d.quitApp = d.quitProgram
	d.closeAllWindows = d.closeAllWindowsToTray
	if err := d.kv.load(); err != nil {
		log.Printf("离线缓存不可用：%v", err)
	}
	return d
}

/* ------------------------------------------------------------------
   主线程投递
------------------------------------------------------------------ */

// onMain 把 fn 投到主线程执行并等它结束；应用还没进主循环（无头测试）时就地
// 执行。
func (d *desktop) onMain(fn func()) {
	if !d.mainLoop {
		fn()
		return
	}
	mygo.RunOnMain(fn)
}

// postMain 把 fn 排到主线程的下一次事件处理里（不阻塞当前这一帧）。打开窗口
// 这类原生资源的操作走这条路：和 showSettings 经 Window.Update 打开设置窗口
// 是同一个道理，不能在别的窗口构建这一帧的过程中建出新窗口。
func (d *desktop) postMain(fn func()) {
	if win := d.anyWindow(); win != nil {
		win.Update(fn)
		return
	}
	d.onMain(fn)
}

// anyWindow 返回任意一个还活着的窗口，原生请求借它排到主循环上。
func (d *desktop) anyWindow() *mygo.Window {
	if d.app != nil {
		return d.app.anyLiveWindow()
	}
	return nil
}

/* ------------------------------------------------------------------
   给界面用的查询
------------------------------------------------------------------ */

// schemeWarning 是「系统没有把回调协议关联到本应用」的说明，仅在这种情况下
// 非空：它决定了浏览器授权后能不能自动跳回。
func (d *desktop) schemeWarning() string { return d.schemeWarn }

// trayIsReady 报告托盘图标真的建出来了：只有这时「关闭窗口收进托盘」才敢
// 藏窗口，否则用户既没有图标也回不到窗口。
func (d *desktop) trayIsReady() bool { return d.trayReady }

/* ------------------------------------------------------------------
   深链、通知点击、系统事件
------------------------------------------------------------------ */

// handleDeepLink 接收系统投来的深链（OAuth 回调）。会话只有一个，回调直接
// 交给它的窗口完成：每条链接都打到日志里，系统根本没投递和投递了拼写不同
// 的地址，在窗口里看起来都是「什么都没发生」，只有日志分得清。
func (d *desktop) handleDeepLink(raw string) {
	raw = strings.TrimSpace(raw)
	if !hasCallbackScheme(raw) {
		log.Printf("深链：忽略非本应用协议的链接 %q", raw)
		return
	}
	log.Printf("深链：收到 %q", raw)
	if d.app == nil {
		return
	}
	d.app.raiseMainWindow()
	d.app.publish(func() { d.app.handleCallbackURL(raw) })
}

// openNotice 处理原生通知的点击：系统只把通知 id 带回来，映射表里查它落在
// 哪条消息上，然后把主窗口带到前台并打开目标。映射清掉（进程重启、通知太旧）
// 时退回消息中心。
func (d *desktop) openNotice(id string) {
	d.onMain(func() {
		a := d.app
		if a == nil {
			return
		}
		messageID := noticeMessageID(id)
		if target := a.noticeTarget(id); target != "" {
			a.raiseMainWindow()
			a.publish(func() { a.openNotificationTarget(target, messageID) })
			return
		}
		a.raiseMainWindow()
		a.publish(a.showNotifications)
	})
}

// onBecameActive 是应用回到前台时：校准一次未读数。站内读掉消息不会产生
// 通知事件，只能靠这一次校准跟上。
func (d *desktop) onBecameActive() {
	d.onMain(func() {
		if a := d.app; a != nil {
			a.publish(a.onBecameActive)
		}
	})
}

// markQuitting 标记「正在退出」：窗口的关闭不能再被拦成「收进托盘」，草稿
// 同步落盘。只在主线程调用。
func (d *desktop) markQuitting() {
	d.quitting = true
	if a := d.app; a != nil {
		a.quitting = true
		a.flushDrafts()
		a.stopDraftTimer()
	}
}

// quitProgram 退出整个应用：先立「退出中」，再交给 mygo 退出。窗口的
// quitApp 都指向这里，所以托盘、标题栏与系统菜单走的是同一条路。
func (d *desktop) quitProgram() {
	d.markQuitting()
	mygo.App.Quit()
}

// openSignup 打开带邀请码的 2Libra 注册页：注册是网页流程，应用内不做。
func (d *desktop) openSignup() {
	if err := d.openURL(signupURL); err != nil {
		log.Printf("打开注册页失败：%v", err)
	}
}

// randomHex 返回 n 字节的随机十六进制串，供本机条目生成不重复的 id。
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err) // crypto/rand 失败没有可用退路
	}
	return hex.EncodeToString(buf)
}
