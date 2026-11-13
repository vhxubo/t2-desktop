package main

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	updatenative "github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

type tabState struct {
	list       ui.ListState
	items      []Item
	page       int
	total      int
	totalPages int
	loaded     bool
	loading    bool
	noMore     bool // 分页已到底（空页 / 无新增），用于无限滚动停止
	err        string
	// moreErr 是「续拉下一页」失败的原因：首屏已经有内容时，续拉失败不能
	// 把整页换成错误页，只在新内容该出现的位置提示并可重试。
	moreErr string
	// authWait 表示首屏失败是在等全局授权把缺失的权限补上。等待期间
	// ensureLoaded 不再重发同一个注定 403 的请求；授权成功后的重跑或用户点
	// 「重试」会清掉它。
	authWait bool
	gen      uint64

	// 通知（弹窗）专用：分段筛选、正在提交已读的 id 与勾选批量。
	filter     int
	reading    map[string]bool
	selected   map[string]bool
	markingAll bool
}

// app holds only data and widget state. Never store ui.Element or *Context:
// background work publishes results through Window.Update.
type app struct {
	auth     *AuthManager
	proxy    *proxyManager
	theme    *themeManager
	client   *Client
	win      *mygo.Window
	srv      ui.Services
	autoLoad bool

	// 设置窗口里的代理草稿。保存前只改这些字段，不影响正在使用的 transport；
	// 点「保存」才写入 proxy.json 并立即切换。保存结果见 toasts.go 的 toast。
	proxyMode    int
	proxyURL     string
	proxyUser    string
	proxyPass    string
	proxyNoProxy string

	// settingsWin 是设置窗口：代理、检查更新与图片缓存都收在这一个窗口里，
	// 不再是标题栏上的两个按钮。入口在主页侧边栏底部，见 sidebarFooter
	// 与 showSettings；只在主线程读写。
	settingsWin *mygo.Window
	// aboutWin 是「关于」窗口：开发者、邮箱、项目地址与 2Libra 主页都列在
	// 这一个窗口里。入口见 sidebarFooter 与 showAbout；只在主线程读写。
	aboutWin *mygo.Window
	// cacheStatus 是设置窗口「图片缓存」一块的显示状态；统计在后台跑，
	// 结果回到主线程。
	cacheStatus cacheStatus
	// updates 表示更新插件已安装（main 里 mygo.Use 之后置 true）。无头
	// 测试不装插件，点「检查更新」只走测试钩子，不会碰 updater 的主循环。
	updates bool
	// onSettingsOpen / onUpdateCheck / onCoinsOpen / onPandoraOpen 是测试钩子：
	// 无窗口时用来确认按钮确实接上了动作。生产为 nil。
	onSettingsOpen func()
	onUpdateCheck  func()
	onCoinsOpen    func()
	onPandoraOpen  func()
	onAboutOpen    func()

	tab     int
	lastTab int
	navSel  string // 左侧导航栏选中项的 id（"t"+序号），与 tab 保持同步
	tabs    []*tabState
	reqSeq  uint64

	pageName    string // list / detail / login
	status      string
	loginMsg    string
	callbackURL string
	// 登录：点「登录」直接打开浏览器，loginPending 表示正在等系统跳回；
	// 只有失败或用户主动粘贴回调时才进入 login 页。
	loginPending bool
	// images 是全应用共用的图片缓存（见 image_cache.go）：登录账号、帖子
	// 作者、评论作者与通知操作者的头像，正文与评论里的表情、社区图标，
	// 以及帖子配图都由它统一管理，同一个地址只下载一次。下载完成前
	// userTag 先画昵称首字母。
	images *imageCache
	// kv 是离线缓存（见 offline_store.go）：金币页的余额与资产曲线、阅读
	// 记录，以及每篇帖子缓存的正文。只读写本机文件，不发请求。
	kv *localKV
	// reading 是当前账号的阅读历史（最近打开的帖子），readingFor 是它所属
	// 的作用域。未登录时记在 anonymous 下，换账号时整体重新载入。
	reading       []readingRecord
	readingFor    string
	readingLoaded bool
	readingList   ui.ListState
	// readAt 是当前账号在本机读过的帖子（short_id → 首次打开时间），列表
	// 据此把读过的帖子置灰（见 post_read.go）。它和阅读记录一样按账号
	// 作用域懒加载，只记本机，不向服务端同步。只在 UI 线程读写。
	readAt     map[string]time.Time
	readFor    string
	readLoaded bool
	// toasts 是等着显示的窗口内提示（见 toasts.go）：操作结果先排队，等到
	// 对应窗口下一次重建时再交给 c.Toast。只在 UI 线程读写。
	toasts []toastNote
	// schemeWarn 是「系统没有把回调协议关联到本应用」的说明，仅在这种情况下
	// 非空：它决定了浏览器授权后能不能自动跳回，得让用户在登录页看见。
	schemeWarn string
	// authGate 是全局授权弹窗：任何缺少权限的调用都收敛到这里，授权成功后
	// 重跑被中断的调用。
	authGate authGate

	// coins 是金币页的聚合状态：余额、今日明细、流水、统计与排行各自独立
	// 加载，某个高级接口未开通时只降级那一块。标题栏的金币数也读这里的
	// balance，两处显示始终是同一个数字。
	coins *coinState
	// coinWin 是金币窗口：金币页从「我的」窗口里独立出来，成为一个独立的
	// mygo 窗口。入口是标题栏上登录账号左侧的金币数（见 coins_window.go）。
	// coinShown 表示窗口此刻是否在屏幕上，金币数据此高亮。都只在主线程读写。
	coinWin   *mygo.Window
	coinShown bool
	// coinTagTimer / coinTagGen 维护标题栏金币数的定时刷新；coinTagSeq 与
	// coinTagLoading 让过期的余额请求结果被丢弃，coinTagPending 记下刷新期间
	// 又收到的金币变动事件，等这次请求回来再补一次。都只在主线程读写。
	coinTagTimer   *time.Timer
	coinTagGen     uint64
	coinTagSeq     uint64
	coinTagLoading bool
	coinTagPending bool
	// coinRefreshTimer / coinRefreshGen 是金币变动全局事件（见 coin_events.go）
	// 的合并刷新：短期内多次事件只发一轮请求。都只在主线程读写。
	coinRefreshTimer *time.Timer
	coinRefreshGen   uint64

	// pandora 是潘多拉窗口的状态：今日盲盒、两次抽奖进度与动作结果。入口是
	// 标题栏上的「潘多拉」（见 pandora_window.go），窗口单独一个 mygo 窗口。
	// pandoraShown 表示窗口此刻是否在屏幕上；都只在主线程读写。
	pandora      *pandoraState
	pandoraWin   *mygo.Window
	pandoraShown bool

	detail       *detailState
	detailStack  []*detailState // 正文内联跳转的返回栈
	emojis       map[string]emojiInfo
	emojiLoading bool
	emojiTried   bool
	md           *markdown.Options
	// commentMd 是评论正文的 Markdown 选项：与正文同一套渲染，字号更小
	// （commentFontSize），让评论区比正文紧凑一号。
	commentMd *markdown.Options
	// bioMd 是资料卡「个人简介」的 Markdown 选项：介绍正文可能带标题、
	// 列表与表情，字号取 userCardBioFontSize。
	bioMd *markdown.Options
	// mdBreaks 缓存「软换行 → 硬换行」改写后的 Markdown 源文，键是原文。
	// 只在 UI 线程读写，见 hardBreakSource。
	mdBreaks map[string]string

	// 通知 / 消息中心：未读数、SSE 长连接与原生通知。
	unread        int
	unreadLoading bool
	unreadGen     uint64
	// notifWin 是通知窗口：通知独立成一个 mygo 窗口，不再是主窗口里的页面
	// 或弹窗。入口见 showNotifications；只在主线程读写。
	notifWin *mygo.Window
	// notifShown 表示通知窗口此刻是否在屏幕上，由窗口的 show / hide / closed
	// 事件维护：铃铛据此高亮，免得每帧去问一次原生窗口（那是跨线程调用）。
	// 只在主线程读写。
	notifShown bool
	// profileWin 是「我的」窗口：点标题栏上的登录头像打开，概览 / 内容 /
	// 回复 / 徽章 / 权限分五个标签页。入口见 showProfile，界面与数据在
	// profile.go、badges.go 与 profile_ui.go。只在主线程读写。
	profileWin *mygo.Window
	// profile 是「我的」窗口读的状态；profileShown 表示窗口此刻是否在屏幕上，
	// 头像按钮据此高亮。都只在主线程读写。
	profile      *profileState
	profileShown bool
	// composeWin 是「发帖」窗口：入口是主页右下角的悬浮按钮（见 composeFab）。
	// compose 是它的草稿与各组件状态，界面与请求在 compose.go / compose_ui.go。
	// 都只在主线程读写；关窗口后草稿保留，退出登录时丢弃。
	composeWin *mygo.Window
	compose    *composeState
	// hosts 是图库（图床）配置：几个内置预设加用户手工添加的项，落盘在
	// localKV 的 imagehosts:v1。hostActiveLabel 是当前激活项的名字，编辑器
	// 的工具面板直接把它绑到下拉框上。都只在 UI 线程读写，见 image_hosts.go。
	hosts           imageHostConfig
	hostActiveLabel string
	// proChecking 表示正在读当前账号的 Pro 状态（官方图床只对 Pro 开放）。
	// 同一时刻只发一次请求，结果写回 auth 的令牌缓存；只在 UI 线程读写。
	proChecking bool
	// hostTesting 是正在测试的那条图库 ID：设置里点「测试这个图床」会上传
	// 一张 1×1 的测试图，结果用 toast 报告；只在 UI 线程读写。
	hostTesting string
	// 设置窗口「图库」一块的编辑草稿：hostEditLabel 是当前展开编辑的条目名字，
	// hostMaxDraft / hostMaxFor 把 int64 的 MaxBytes 接到 NumberInput 的
	// float64 上（切条目时重新读一次）。都只在 UI 线程读写。
	hostEditLabel string
	hostMaxDraft  float64
	hostMaxFor    string
	// uploads 是编辑器里所有图片上传任务（含已完成 / 失败的），按加入顺序
	// 串行执行。占位符 → 真实 URL 的替换、失败重试与移除都靠它；只在 UI
	// 线程读写，后台结果经 publish 回来，见 editor_upload.go。
	uploads      []*uploadJob
	uploadSeq    uint64
	uploadWorker bool
	// pendingPaste 是刚粘进来、还没插进正文的一组图片。输入框的 OnPaste
	// 回调不能直接改正文（输入框会把自己持有的文本写回去，覆盖掉外面写进去
	// 的内容），所以回调只登记来源并吞掉这次粘贴，等目标编辑器下一次构建、
	// 在构造输入框之前领走，见 editor_upload.go 的 drainPaste。只在 UI 线程
	// 读写。
	pendingPaste *pendingPaste
	// onUploadDone 是上传结束（成功或失败）的测试钩子：headless 没有主循环，
	// 靠它把结果同步回测试。生产为 nil。
	onUploadDone func(uploadJob)
	// pickImagePaths 是文件选择框的测试替身：headless 里没有原生对话框，
	// 测试把它换成直接返回几个路径。为 nil 时走 mygo.Dialog.Open。
	pickImagePaths func() ([]string, error)
	// nodes 是节点表的共享缓存（内存 + cache/offline.json，见 nodes.go）：
	// 发帖级联选择用它，节点页以后也读这一份。只在主线程读写。
	nodes nodeCache
	// userCardWin 是「用户资料」窗口：点任何一处统一用户标识（头像 + 昵称）
	// 打开，见 user_ui.go 的 userTag；userCard 是它读的状态，userCardShown
	// 表示窗口此刻是否在屏幕上。界面与请求在 user_card.go / user_card_ui.go。
	// 都只在主线程读写。
	userCardWin   *mygo.Window
	userCard      *userCardState
	userCardShown bool
	// searchWin 是搜索窗口：入口是标题栏的放大镜。search 是它的输入、结果与
	// 防抖状态，界面与请求在 search.go / search_ui.go。都只在主线程读写。
	searchWin *mygo.Window
	search    *searchState
	// tray 是系统托盘图标（见 tray.go）：应用就绪后创建，退出前销毁；
	// 无头测试保持 nil，不会碰到原生托盘。只在主线程读写。
	tray *mygo.Tray
	// trayReady 表示托盘图标真的建出来了：没建出来时「收进托盘」不能让窗口
	// 藏起来，否则用户既没有图标也回不到窗口。
	trayReady bool
	// closeMode 是「关闭主窗口时」的偏好（见 window_mode.go），落盘在
	// window.json。只在主线程读写。
	closeMode *closeModeStore
	// closePromptOpen / closeRemember 是关闭提示弹窗的状态：是否正在询问
	// 「退出还是收进托盘」，以及这次要不要记住选择。只在主线程读写。
	closePromptOpen bool
	closeRemember   bool
	// quitting 表示已经在退出流程里：这时主窗口的 OnClose 不能再把退出拦成
	// 「收进托盘」。由 OnBeforeQuit 与托盘的「退出」置位。
	quitting bool
	// quitApp 退出应用，生产里是 mygo.App.Quit；无头测试换成记录调用，
	// 没有主循环时不能真的走退出流程。
	quitApp       func()
	streamLive    bool
	watchCancel   context.CancelFunc
	watchGen      uint64
	notifSeen     map[string]bool
	notifBaseline bool
	notifPollGen  uint64
	notifTimer    *time.Timer
	// notify 是平台通知函数；在应用就绪后才装上，headless 测试保持 nil，
	// 因此不会触碰真实的系统通知。
	notify       func(nativeNotice) error
	nativeNotify bool // 原生通知开关（默认开）
	// noticeTargets 是「原生通知 id → 目标地址」的映射：系统通知的点击回调
	// 只带 id，靠它把点击落回对应的帖子 / 评论，进程重启后再点击则回到站点。
	noticeTargets map[string]string

	// onReplyPosted is a hook invoked after a reply is posted (or fails),
	// carrying the created reply and any error. It runs wherever publish
	// runs (the UI thread in the app, the network goroutine under headless
	// tests). Nil in production; used by tests to await completion.
	onReplyPosted func(created replyItem, err error)
	// onNotificationsRead 是「按条已读」落账后的测试钩子：headless 测试没有
	// 窗口，publish 在请求协程里直接执行，靠它把结果同步回测试。
	onNotificationsRead func(ids []string)
	// onAuthPrompt / onAuthReplay 是全局授权弹窗的测试钩子：弹窗打开与授权
	// 成功后重跑被中断的调用时触发。生产为 nil。
	onAuthPrompt func()
	onAuthReplay func()
	// onPostPublished 是发帖完成（成功或失败）后的测试钩子：headless 测试
	// 没有窗口，publish 在请求协程里直接执行，靠它把结果同步回测试。
	onPostPublished func(created createdTopic, err error)
	// onSearchDone 是搜索完成（成功或失败）后的测试钩子，原因同上。
	onSearchDone func(query string, err error)
	// onInteractDone 是收藏 / 表态完成（成功或失败）后的测试钩子，原因同上。
	onInteractDone func(err error)
	// onCoinsChanged 是金币变动全局事件的测试钩子：headless 测试没有窗口，
	// 靠它确认各写操作确实发出了刷新事件。生产为 nil。
	onCoinsChanged func(event coinEvent)
	// onNodesLoaded 是节点表刷新完成（成功或失败）后的测试钩子，原因同上。
	onNodesLoaded func(err error)
}

// tabID / tabFromID 把 tab 序号与侧边栏条目的 id 互相映射。
func tabID(i int) string { return "t" + strconv.Itoa(i) }

func tabFromID(id string) (int, bool) {
	if len(id) < 2 || id[0] != 't' {
		return 0, false
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil || n < 0 || n >= len(tabSpecs) {
		return 0, false
	}
	return n, true
}

func newApp(dir string) *app {
	if dir == "" {
		dir = configDir()
	}
	proxy := newProxyManager(dir)
	auth := newAuthManagerAt(dir, proxy)
	a := &app{
		auth:         auth,
		proxy:        proxy,
		theme:        newThemeManager(dir),
		client:       &Client{Auth: auth, Proxy: proxy},
		images:       newImageCache(dir),
		kv:           newOfflineKV(dir),
		tabs:         make([]*tabState, len(tabSpecs)),
		lastTab:      -1,
		navSel:       tabID(0),
		pageName:     "list",
		nativeNotify: true,
		coins:        newCoinState(),
		pandora:      newPandoraState(),
		profile:      newProfileState(),
		closeMode:    newCloseModeStore(dir),
	}
	// 托盘的「退出」与窗口关闭走同一条退出流程；测试里换成记录调用，无头
	// 环境没有主循环，不能真的退出。先立 quitting：退出时主窗口的 OnClose
	// 不能再把这次退出拦成「收进托盘」。
	a.quitApp = func() { a.quitting = true; mygo.App.Quit() }
	// 本地缓存是"先读文件、后发请求"的首屏底稿：启动时就把金币与阅读记录
	// 装进内存，第一帧就能画出来。文件不存在或损坏都只是空缓存。
	if err := a.kv.load(); err != nil {
		log.Printf("离线缓存不可用：%v", err)
	}
	a.loadCoinCache()
	a.loadReadingHistory()
	a.loadPostReads()
	// 图库配置也要在首帧前就位：编辑器工具面板打开时不能还是空列表。
	a.loadImageHosts()
	for i := range a.tabs {
		a.tabs[i] = &tabState{}
	}
	a.auth.onChange = func(err error) {
		a.publish(func() { a.onAuthChanged(err) })
	}
	a.md = newMarkdownOptions(a, 0)
	a.commentMd = newMarkdownOptions(a, commentFontSize)
	a.bioMd = newMarkdownOptions(a, userCardBioFontSize)
	return a
}

// publish marshals a state change onto the UI thread, then rebuilds every
// window that shows the same state. In headless tests there is no window, so
// the change applies directly.
func (a *app) publish(fn func()) {
	if a.win == nil {
		fn()
		return
	}
	a.win.Update(func() {
		fn()
		// 主窗口、通知窗口、设置窗口、「我的」窗口、金币窗口与发帖窗口读的是
		// 同一份状态：改完之后另外几个窗口也要重建，否则它们显示的仍是上一帧
		// 的未读数、代理状态、缓存大小或账号资料。
		a.redrawNotificationWindow()
		a.redrawSettingsWindow()
		a.redrawProfileWindow()
		a.redrawCoinWindow()
		a.redrawPandoraWindow()
		a.redrawComposeWindow()
		a.redrawUserCardWindow()
		a.redrawSearchWindow()
	})
}

// redrawNotificationWindow asks the notification window, when it is open, for
// a frame. Main thread only.
func (a *app) redrawNotificationWindow() {
	if win := a.notifWin; win != nil {
		win.Invalidate()
	}
}

func (a *app) load(idx, page int) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	st := a.tabs[idx]
	spec := tabSpecs[idx]
	if spec.Kind == kindNotification {
		// 分段筛选直接改接口参数，不额外请求参数名。
		spec.Query = notificationsQuery(st.filter)
	}
	a.loadSpec(st, spec, page, func() { a.load(idx, page) })
}

// loadSpec 是通用的列表加载：把 spec 的第 page 页拉回来填进 st。「我的」
// 窗口里的四类内容也走这条路，所以它按 spec 而不是按侧边栏下标工作。
// replay 是授权补上权限后重跑这次请求用的回调。
func (a *app) loadSpec(st *tabState, spec tabSpec, page int, replay func()) {
	if st == nil || page < 1 {
		return
	}
	if st.loading {
		return
	}
	st.authWait = false
	if spec.NeedsAuth && !a.auth.loggedIn() {
		st.err = "需要登录后才能查看「" + spec.Label + "」"
		return
	}
	st.loading = true
	st.err = ""
	st.moreErr = ""
	st.page = page
	a.reqSeq++
	gen := a.reqSeq
	st.gen = gen
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetch(ctx, spec, page)
		a.publish(func() {
			if st.gen != gen { // a newer request superseded this one
				return
			}
			st.loading = false
			if err != nil {
				// 首页因授权失败时，弹窗负责去补权限；把这一页标成等待授权，
				// 避免每帧重建都再发一次注定失败的请求。
				if a.handleAuthError(err, replay) {
					st.authWait = true
				}
				if page == 1 {
					st.err = friendlyError(err)
					st.items = nil
				} else {
					// 已有内容时续拉失败：保留当前列表，在列表底部提示。
					st.moreErr = friendlyError(err)
				}
				return
			}
			st.moreErr = ""
			if page == 1 {
				st.items = res.Items
				st.list.ScrollTo(0, ui.Start)
				st.noMore = len(res.Items) == 0
			} else {
				// 页码分页可能重复：按资源 ID 去重
				seen := make(map[string]bool, len(st.items))
				for _, it := range st.items {
					seen[it.Key] = true
				}
				added := 0
				for _, it := range res.Items {
					if !seen[it.Key] {
						st.items = append(st.items, it)
						seen[it.Key] = true
						added++
					}
				}
				// 服务端返回空页或没有新增条目：标记到底，避免无限加载
				st.noMore = added == 0
			}
			st.total = res.Total
			st.totalPages = res.TotalPages
			st.page = page
			st.loaded = true
			// 列表行里的作者、被回复人与通知操作者都要带头像：数据到了就
			// 先预取，渲染时只读缓存，不在每帧里发请求。
			a.prefetchItemAvatars(res.Items)
		})
	}()
}

// ensureLoaded is a guarded job: it starts at most one request per tab
// until the tab has been loaded, and can be called from every rebuild.
func (a *app) ensureLoaded() {
	if !a.autoLoad {
		return
	}
	spec := tabSpecs[a.tab]
	st := a.tabs[a.tab]
	if st.loading || st.loaded || st.authWait {
		return
	}
	if spec.NeedsAuth && !a.auth.loggedIn() {
		return
	}
	a.load(a.tab, 1)
}

func (a *app) reloadCurrent() {
	st := a.tabs[a.tab]
	if st.loading {
		return
	}
	st.authWait = false
	st.loaded = false
	st.items = nil
	st.err = ""
	st.moreErr = ""
	st.noMore = false
	st.list.ScrollTo(0, ui.Start)
	a.load(a.tab, 1)
}

func (a *app) onAuthChanged(err error) {
	a.loginPending = false
	switch {
	case err != nil:
		a.loginMsg = friendlyError(err)
		a.status = a.loginMsg
		// 失败时才把回退页摆出来：那里有「重新打开浏览器」和粘贴回调链接，
		// 正常授权成功不进这一页。授权弹窗先让位，待重跑调用保留：用户在
		// 回退页重新授权成功后，它们照样会继续。
		a.authGate.open = false
		a.pageName = "login"
	case a.auth.loggedIn():
		a.loginMsg = "已登录"
		if name := a.auth.username(); name != "" {
			a.loginMsg += " · " + name
		}
		a.status = a.auth.verifyStatus()
		if a.status == "" {
			a.status = "登录成功"
		}
		if a.pageName == "login" {
			a.pageName = "list"
		}
		a.ensureAvatar()
	default:
		// 退出登录也会走 onChange(nil)：不能把它当成一次登录成功。
		a.loginMsg = "已退出登录"
		a.status = ""
		// 之前没等到授权的调用不再属于当前会话。
		a.authGate = authGate{}
	}
	// 私人 tab 的缓存与登录态绑定，一律丢弃后按需重新拉取。
	for i, s := range tabSpecs {
		if s.NeedsAuth {
			a.tabs[i] = &tabState{}
		}
	}
	// 金币页同样绑定当前会话；授权补上的 read:economy 也从这个空状态重新
	// 发起请求，不会沿用授权前的空结果。
	a.coins = newCoinState()
	// 潘多拉状态在「同一个账号重新授权」时要保留：授权成功后重跑的可能是
	// 重抽，必须还拿着原来的 box_instance_id。换账号 / 退出才整体丢弃；
	// 保留时把「已加载」标成过期，窗口下一帧会重读今日状态。
	switch {
	case !a.auth.loggedIn(), a.pandora == nil:
		a.pandora = newPandoraState()
	case a.pandora.owner != "" && a.auth.username() != "" &&
		a.pandora.owner != a.auth.username():
		a.pandora = newPandoraState()
	default:
		a.pandora.loaded = false
		a.pandora.wait = false
		a.pandora.err = ""
		a.pandora.busy = ""
	}
	// 「我的」窗口里的资料与列表同样属于某个账号：换账号或退出后一律
	// 丢弃，下次打开时按新会话重新拉取。
	a.profile = newProfileState()
	// 授权状态变了，之前因缺权限停住的 tab 可以重新拉取。
	for _, st := range a.tabs {
		st.authWait = false
	}
	if !a.auth.loggedIn() && tabSpecs[a.tab].NeedsAuth {
		a.tab = 0
		a.lastTab = 0
		a.navSel = tabID(0)
	}
	if a.auth.loggedIn() {
		a.refreshUnread()
		a.startNotificationWatch()
		// 标题栏的金币数在登录后立即读一次，之后按间隔刷新；读取结果同时
		// 写回金币窗口共用的状态。
		a.startCoinTagWatch()
		// 潘多拉入口也静默读一次今日状态：首页标题栏要能直接显示抽中的
		// 事件名，不必先打开潘多拉窗口。
		a.loadPandoraEntry()
		// 官方图床只对 Pro 开放：登录后就把 Pro 状态读回来，别等用户拖图
		// 片时才提示「还没确认」。
		a.refreshProStatus(true)
	} else {
		a.stopNotificationWatch()
		// 通知窗口只展示当前账号的消息：退出后立即关掉，别留一屏上一个
		// 账号的内容。
		a.closeNotificationWindow()
		// 「我的」窗口展示的是当前账号的资料与列表，同样随退出关掉。
		a.closeProfileWindow()
		// 金币窗口与标题栏金币数也只属于当前账号：停掉定时刷新并关掉窗口。
		a.stopCoinTagWatch()
		a.closeCoinWindow()
		// 潘多拉窗口展示的是当前账号的盲盒与抽奖结果，退出后一并关掉。
		a.closePandoraWindow()
		// 发帖窗口里的草稿属于上一个账号，退出后一并关掉并丢弃。
		a.closeComposeWindow()
		a.compose = nil
		a.unread = 0
		a.unreadLoading = false
		a.notifSeen = nil
		a.notifBaseline = false
		// 退出后系统里可能还留着通知，但目标属于上一个账号，一律不再解析。
		a.noticeTargets = nil
	}
	// 资料卡上的关注状态与「是不是我」都跟着账号走：登录、退出、换账号都
	// 重新读一次（窗口没开时是空操作）。
	a.refreshUserCardForAuth()
	// 搜索窗口里的「收藏」结果属于上一个账号：换账号 / 退出后清掉结果，
	// 关键词与类型保留。窗口本身留着：搜索结果大多是公开内容。
	a.resetSearchResults()
	a.ensureLoaded()
	// 授权成功后，把因为缺权限被打断的调用照原样重跑。放在 ensureLoaded
	// 之后：两者都只会真正发出一次请求（load 自己会去重）。
	if err == nil && a.auth.loggedIn() {
		a.replayAuthorizedCalls()
	}
}

func (a *app) view(c *ui.Context) {
	// 外观按「跟随系统 / 浅色 / 深色」逐帧决定。SetTheme 只作用于当前帧，
	// 每帧从系统主题重置，所以要在建树前先定主题、再取根背景：否则跟随时
	// 根背景还是旧主题的底色，会在页面边缘露出一圈不协调的颜色。
	a.applyTheme(c)
	t := c.Theme()
	a.srv = c.Services()
	c.Root().Background(t.Background)
	// 操作结果以 toast 显示：主窗口、登录页与详情页都走这里。
	a.drainToasts(c, a.mainClaimsToasts)

	switch a.pageName {
	case "login":
		a.loginView(c)
		a.dialogs(c)
		return
	case "detail":
		a.detailView(c)
		a.dialogs(c)
		return
	}

	// 顶部自绘标题栏（可拖拽、避让系统窗口按钮）+ 下方 侧边栏/内容 布局。
	ui.Column(c).Fill().Children(func() {
		a.titleBar(c)
		ui.Divider(c)
		ui.Row(c).Fill().MinHeight(0).Children(func() {
			// 左侧导航栏：带图标的侧边栏
			ui.Column(c).Width(150).Shrink(0).FillHeight().Background(t.Surface).Children(func() {
				sb := ui.Sidebar(c, &a.navSel, func() {
					// 侧边栏只有「浏览」一组：动态虽然要登录，但它也是浏览
					// 内容，不再单独占一个「我的」分组。通知不在这里：它已经
					// 从页面改成标题栏铃铛打开的弹窗。
					ui.SidebarSection(c, "浏览", nil, func() {
						for i, s := range tabSpecs {
							if s.Kind == kindNotification {
								continue
							}
							ui.SidebarItem(c, tabID(i), tabIcons[i], s.Label)
						}
					})
				}).Grow(1)
				// 侧边栏选中变化 → 同步到 a.tab（用于 body 加载逻辑）
				if sb.Changed() {
					if i, ok := tabFromID(a.navSel); ok && i != a.tab {
						a.tab = i
						// 点上需要登录的页面（动态）而没登录：统一弹授权窗，
						// 登录成功后自动把这一页拉出来，不用再点一次。
						if spec := tabSpecs[i]; spec.NeedsAuth && !a.auth.loggedIn() {
							idx := i
							a.requestLogin("「"+spec.Label+"」需要登录 2Libra 后才能查看。",
								func() { a.load(idx, 1) })
						}
					}
				}
				if a.tab != a.lastTab {
					a.lastTab = a.tab
					a.status = ""
				}
				// 底部固定区：三档外观、设置入口，以及账号入口。
				ui.Divider(c)
				a.sidebarFooter(c)
			})
			ui.Divider(c)
			// 右侧内容区：正文列表（无限滚动，无底部状态栏）。右下角浮着
			// 一列 FAB（返回顶部 / 刷新 / 发帖），绝对定位，不占列表的高度。
			ui.Column(c).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() {
				a.body(c)
				a.listFabs(c)
			})
		})
	})
	a.ensureLoaded()
	a.dialogs(c)
}

// dialogs 画出全局授权弹窗。列表页、登录回退页与详情页共用，放在内容之后
// 叠在最上层。通知与设置都不在这里：它们各自是独立的窗口。
func (a *app) dialogs(c *ui.Context) {
	a.authDialog(c)
	a.closePromptDialog(c)
}

// sidebarFooter 是主页侧边栏底部的固定区：从上到下是外观的三档图标、设置
// 入口，以及账号入口（登录 / 退出登录）。放在滚动区之外，导航项再多也不会
// 把这些常驻入口挤走。
func (a *app) sidebarFooter(c *ui.Context) {
	ui.Column(c).FillWidth().Shrink(0).Padding(6, 8).Gap(2).Children(func() {
		a.themeButtons(c)
		a.sidebarButton(c, "设置", settingsIcon, "打开设置窗口", a.showSettings)
		a.sidebarButton(c, "关于", aboutIcon, "关于本应用", a.showAbout)
		a.sidebarAccount(c)
	})
}

// sidebarAccount 是侧边栏底部的账号入口：未登录时是「登录」，点一下直接打开
// 系统浏览器（不再先进登录页，那一步既多余，也容易让人以为要再点一次）；
// 等待授权时显示进度，并给出「粘贴回调」与「取消」两个兜底动作；登录后是
// 「退出登录」。标题栏不再放登录/退出：那里只留头像与昵称。
func (a *app) sidebarAccount(c *ui.Context) {
	t := c.Theme()
	switch {
	case a.loginPending:
		waiting := "等待浏览器授权…"
		if a.schemeWarn != "" {
			// 系统没注册回调协议时不会有自动跳回，别让人干等。
			waiting = "等待授权：系统未注册回调协议，请点「粘贴回调」"
		}
		ui.Text(c, waiting).FontSize(11).TextColor(t.TextMuted).MaxLines(3)
		a.sidebarButton(c, "粘贴回调", nil, "在登录页粘贴回调链接", func() { a.pageName = "login" })
		a.sidebarButton(c, "取消", nil, "取消这次登录", a.cancelLogin)
	case a.auth.loggedIn():
		a.sidebarButton(c, "退出登录", logoutIcon, "退出登录", func() { a.auth.Logout() })
	default:
		a.sidebarButton(c, "登录", loginIcon, "登录 2Libra", func() { a.startLogin() })
	}
}

// sidebarButton 是侧边栏底部的整行入口（设置、登录 / 退出登录）：图形与文案
// 并排，悬停浅灰底，画法贴近 SidebarItem，但它是动作而不是页面，不参与导航
// 选中。icon 为 nil 时只排文字（授权等待中的「粘贴回调」「取消」）。
func (a *app) sidebarButton(c *ui.Context, label string, icon *ui.SVG, tip string, onClick func()) {
	t := c.Theme()
	b := ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Padding(6, 8).
		Radius(t.Radius).Background(ui.Transparent)
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(16, 16).TextColor(t.TextMuted)
		}
		ui.Text(c, label).FontSize(12).TextColor(t.Text).SingleLine().Grow(1)
	})
	b.Label(label).Tooltip(tip).OnClick(onClick)
}

// 自绘标题栏的尺寸。32 DIP 是 Windows 标题栏的常规高度，窗口按钮 46×32
// 也与系统按钮同宽同高，文字和图标因此不会被压扁、裁切或挤在一起。
const (
	titleBarHeight   = 32
	titleButtonWidth = 46
)

// titleBar 是完全自绘的顶部标题栏：可拖拽移动窗口（双击最大化），左侧是
// 品牌与账号状态，右侧是最小化 / 最大化·还原 / 关闭按钮；系统标题栏与
// 系统按钮已由 TitleBarHidden + HideWindowButtons 隐藏。
//
// 配色全部取自当前主题：标题栏与页面同色，不出现一条突兀的灰带；跟随
// 系统切到深色时，标题栏也随之变深，不会出现浅色条压着深色页面。
func (a *app) titleBar(c *ui.Context) {
	t := c.Theme()
	// bar 是系统窗口按钮占用的区域，HideWindowButtons 下为零。保留这段
	// 计算，将来若改回系统按钮，标题栏仍然给它们让出位置。
	bar := c.TitleBar()
	h := float32(titleBarHeight)
	if bar.Height > h {
		h = bar.Height
	}
	ui.Row(c).Height(h).FillWidth().AlignItems(ui.Stretch).
		Padding(0, 0, 0, bar.Left).Background(t.Surface).DragWindow().Children(func() {
		// 左段：品牌、通知铃铛，登录后再加上头像与昵称，整体可随标题栏拖动。
		// 外观、设置与登录 / 退出登录都在主页侧边栏底部（见 sidebarFooter）。
		ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10, 0, 12).Gap(10).
			AlignItems(ui.Center).Children(func() {
			ui.Text(c, "T2 Desktop").FontSize(13).Bold().SingleLine().TextColor(t.Text)
			ui.Spacer(c).Grow(1)
			// 搜索：任何页面都能用，不依赖登录，也不占侧边栏。窗口开着时
			// 放大镜用强调色标出来。
			a.titleIconButton(c, searchIcon, a.searchWindowVisible(),
				"搜索", "搜索帖子、用户、节点与标签", a.showSearch)
			// 通知铃铛：未读数叠在铃铛右下角，点开独立窗口（不再是侧边栏页面）。
			a.notificationBell(c)
			if a.auth.loggedIn() {
				// 潘多拉盲盒：独立窗口的入口，放在金币数左侧。今天还能抽时
				// 入口自带「可抽」角标，窗口开着时高亮。
				a.pandoraTag(c)
				// 金币数与账号标识只给登录用户看。金币数在账号左侧：💰 + 千分位
				// 余额，点开独立的金币窗口。
				a.coinTag(c)
				// 头像 + 昵称整体可点，点开「我的」窗口；标题栏不写「已登录」，
				// 也不放退出按钮：那几个字和按钮都挪到了侧边栏底部。
				a.profileTag(c, a.auth.username())
			}
		})
		// 右段：自绘窗口控制按钮（最小化 / 最大化·还原 / 关闭），紧贴右上角。
		a.titleButton(c, titleMinimize, "最小化", func() {
			if a.win != nil {
				a.win.Minimize()
			}
		})
		if a.win != nil && a.win.IsMaximized() {
			a.titleButton(c, titleRestore, "还原", func() {
				a.win.ToggleMaximize()
			})
		} else {
			a.titleButton(c, titleMaximize, "最大化", func() {
				if a.win != nil {
					a.win.ToggleMaximize()
				}
			})
		}
		a.titleButton(c, titleClose, "关闭", func() {
			if a.win != nil {
				a.win.Close()
			}
		})
	})
}

// titleGlyph 是窗口按钮上要画的图形。
type titleGlyph int

const (
	titleMinimize titleGlyph = iota
	titleMaximize
	titleRestore // 已最大化时显示的「还原」
	titleClose
)

// titleButton 画一个窗口按钮。图形用矢量线条绘制，不依赖字体里的字符：
// 「– / □ / ▢ / ✕」这类符号在不同字体下大小不一，有的字体还缺字，最大化
// 之后会显示成空白或豆腐块，正是标题栏「显示不正常」的来源。
func (a *app) titleButton(c *ui.Context, g titleGlyph, label string, onClick func()) {
	t := c.Theme()
	b := ui.Box(c).Width(titleButtonWidth).Shrink(0).Background(ui.Transparent)
	ink := t.Text
	if b.Hovered() {
		if g == titleClose {
			b.Background(ui.Hex("#e81123")) // 关闭：红底白叉，与系统一致
			ink = ui.Hex("#ffffff")
		} else {
			b.Background(t.SurfaceHover)
		}
	}
	b.Draw(func(p *ui.Painter, r ui.Rect) {
		// r 是窗口坐标里的按钮矩形，图形要相对 r 偏移来画。
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		const half = 5 // 图标约 10×10 DIP
		switch g {
		case titleMinimize:
			p.Line(cx-half, cy+0.5, cx+half, cy+0.5, 1, ink)
		case titleMaximize:
			p.Stroke(ui.Rect{X: cx - half, Y: cy - half, W: 2 * half, H: 2 * half}, ink, 0, 1)
		case titleRestore:
			// 前面的方框，加上后面方框露出的上边和右边。
			p.Stroke(ui.Rect{X: cx - half, Y: cy - half + 2, W: 8, H: 8}, ink, 0, 1)
			p.Line(cx-half+2, cy-half, cx+half, cy-half, 1, ink)
			p.Line(cx+half, cy-half, cx+half, cy+half-2, 1, ink)
		case titleClose:
			p.StrokePolyline([][2]float32{{cx - half, cy - half}, {cx + half, cy + half}}, 1, ink)
			p.StrokePolyline([][2]float32{{cx + half, cy - half}, {cx - half, cy + half}}, 1, ink)
		}
	})
	b.Label(label).Tooltip(label).OnClick(onClick)
}

// titleIconButton 是标题栏里的纯图标按钮（外观）：图形本身是装饰，可访问名
// 与提示由 label / tip 提供；selected 为真时用强调色标出当前档。图标用
// 24×24 的矢量线，不占文字宽度，标题栏因此不会被挤满。
func (a *app) titleIconButton(c *ui.Context, icon *ui.SVG, selected bool, label, tip string, onClick func()) {
	t := c.Theme()
	b := ui.Box(c).Padding(4, 6).Radius(6).Center().Background(ui.Transparent)
	if selected || b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	ink := t.TextMuted
	switch {
	case selected:
		ink = t.Accent
	case b.Hovered():
		ink = t.Text
	}
	b.Children(func() {
		ui.Icon(c, icon).Size(16, 16).TextColor(ink)
	})
	b.Label(label).Tooltip(tip).OnClick(onClick)
}

func (a *app) body(c *ui.Context) {
	t := c.Theme()
	st := a.tabs[a.tab]
	spec := tabSpecs[a.tab]

	if spec.NeedsAuth && !a.auth.loggedIn() {
		ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
			ui.Text(c, "「"+spec.Label+"」需要登录").FontSize(17).Bold()
			ui.Text(c, "该端点要求 read:me / profile 权限，登录后才会发起请求。").TextColor(t.TextMuted)
			if ui.PrimaryButton(c, "去登录").Clicked() {
				idx := a.tab
				a.requestLogin("「"+spec.Label+"」需要登录 2Libra 后才能查看。",
					func() { a.load(idx, 1) })
			}
		})
		return
	}
	if st.loading && len(st.items) == 0 {
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(28, 28)
			ui.Text(c, "正在加载 "+spec.Label+"…").TextColor(t.TextMuted)
		})
		return
	}
	if st.err != "" {
		ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
			ui.Text(c, "加载失败").FontSize(16).Bold()
			ui.Text(c, st.err).TextColor(t.Danger).MaxLines(4)
			if ui.Button(c, "重试").Clicked() {
				a.reloadCurrent()
			}
			if strings.Contains(st.err, "登录") || strings.Contains(st.err, "授权") {
				if ui.Button(c, "去授权").Clicked() {
					idx := a.tab
					a.requestLogin("登录并授权后才能继续查看「"+spec.Label+"」。",
						func() { a.load(idx, 1) })
				}
			}
		})
		return
	}
	if len(st.items) == 0 {
		ui.Column(c).Grow(1).Center().Children(func() {
			ui.Text(c, "暂无内容").TextColor(t.TextMuted)
		})
		return
	}
	items := st.items
	st.list.Key = func(i int) any {
		if i >= 0 && i < len(items) {
			return items[i].Key
		}
		return i
	}
	ui.List(c, &st.list, len(items), func(i int) {
		a.row(c, items[i])
	}).Grow(1).Padding(8, 16).Gap(14)
	a.loadMoreFooter(c, st.moreErr, st.loading, func() {
		a.load(a.tab, st.page)
	})
	// 无限滚动：滚动到列表底部且还有下一页时，自动加载更多，不再依赖
	// 页码按钮。「最新评论」服务端忽略 page，因此只对 pagingPages 的 tab 触发。
	// 续拉失败后停在提示上，等用户点「重试」，否则每次重建都会再发一次注定
	// 失败的请求。
	if spec.Paging == pagingPages && st.loaded && !st.loading && st.moreErr == "" &&
		!st.noMore && len(st.items) > 0 && st.list.AtEnd() {
		hasMore := st.totalPages == 0 || st.page < st.totalPages
		if hasMore {
			a.load(a.tab, st.page+1)
		}
	}
}

// loadMoreFooter 是无限滚动列表底部的续拉状态行：加载中显示进度，失败时
// 给出原因与「重试」。已有内容时，续拉失败只出现在这里，列表本身保持不动。
// 右侧留出发帖 FAB 的位置：按钮浮在右下角，状态行里的「重试」不能压在它
// 下面，否则点下去会变成发帖。
func (a *app) loadMoreFooter(c *ui.Context, errText string, loading bool, retry func()) {
	if errText == "" && !loading {
		return
	}
	t := c.Theme()
	ui.Row(c).FillWidth().Padding(8, composeFabInset, 8, 16).Gap(8).AlignItems(ui.Center).Children(func() {
		if errText != "" {
			ui.Text(c, "加载更多失败："+errText).FontSize(11).
				TextColor(t.Danger).MaxLines(2).Grow(1)
			if ui.Button(c, "重试").Clicked() {
				retry()
			}
			return
		}
		ui.Spinner(c).Size(14, 14)
		ui.Text(c, "正在加载更多…").FontSize(11).TextColor(t.TextMuted)
	})
}

func (a *app) row(c *ui.Context, it Item) {
	a.rowWith(c, it, a.openItem)
}

// rowWith 画一行列表项，但由调用方决定点开它的动作。「我的」窗口渲染同一
// 批行时传 openProfileItem：正文在主窗口打开，本窗口留着不动。
func (a *app) rowWith(c *ui.Context, it Item, open func(Item)) {
	if it.Notif != nil {
		a.notificationRow(c, it)
		return
	}
	if it.Extra != nil {
		a.richRow(c, it, open)
		return
	}
	t := c.Theme()
	titleColor := postTitleColor(t, a.isPostRead(it.ShortID))
	row := ui.ButtonBase(c).FillWidth().Padding(14, 16).Radius(10).
		Background(t.Background).Border(1, t.Border)
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(5).Children(func() {
			// 标题是这一行里唯一的重心：字号明显大于副标题和时效信息，
			// 并保持 700 粗体，扫一眼就能把标题与下方文案分开。
			ui.Text(c, it.Title).FontSize(16).Bold().MaxLines(2).TextColor(titleColor)
			// 副标题里的作者用统一用户标识渲染：头像跟着 11px 的昵称走，
			// 同时保留「节点 · 昵称 · 发帖时间」的原有顺序；节点名或时间太长时
			// 允许折行，不把右侧的元信息挤出去。
			switch {
			case it.Author != "":
				ui.Row(c).MinWidth(0).AlignItems(ui.Center).Wrap().Children(func() {
					if it.SubtitlePrefix != "" {
						ui.Text(c, it.SubtitlePrefix).FontSize(11).
							TextColor(t.TextMuted).SingleLine()
					}
					a.userTag(c, userTagSpec{
						Name:   it.Author,
						Avatar: it.AuthorAvatar,
						Size:   11,
						Color:  t.TextMuted,
						Badges: it.AuthorBadges,
					})
					if it.SubtitleSuffix != "" {
						ui.Text(c, it.SubtitleSuffix).FontSize(11).
							TextColor(t.TextMuted).SingleLine()
					}
				})
			case it.Subtitle != "":
				ui.Text(c, it.Subtitle).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
			// 帖子行多一行时效信息：最后回复人、更新时间与最高楼层。没人
			// 回复时没有这一行，右侧的回复数仍显示 0。
			if it.Stats != nil && it.Stats.Replier != "" {
				a.latestReplyRow(c, it.Stats)
			}
		})
		if it.Stats != nil {
			a.postStatsBadge(c, it)
		} else if it.Meta != "" {
			ui.Text(c, it.Meta).FontSize(11).TextColor(t.TextMuted).
				SingleLine().Width(190).TextAlign(ui.End)
		}
	})
	// 有 short_id 的帖子/评论在应用内看正文和评论；其余条目保持打开原文。
	if it.ShortID != "" || it.URL != "" {
		row.OnClick(func() { open(it) })
	}
}

// latestReplyRow 是帖子行标题下方的时效行：谁最后回复、多久以前、以及
// 最新（也是最高）楼层。没人回复过的帖子没有这一行；评论与合集行也不会走
// 到这里（它们的 Stats 为 nil）。
func (a *app) latestReplyRow(c *ui.Context, st *replyStats) {
	t := c.Theme()
	ui.Row(c).MinWidth(0).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "最后回复").FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(0)
		ui.Text(c, st.Replier).FontSize(11).Bold().TextColor(t.TextMuted).
			SingleLine().Shrink(1).Ellipsis("…")
		if st.Updated != "" {
			ui.Text(c, "·").FontSize(11).TextColor(t.TextMuted).Shrink(0)
			ui.Text(c, st.Updated).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(0)
		}
		if st.MaxFloor > 0 {
			ui.Text(c, "·").FontSize(11).TextColor(t.TextMuted).Shrink(0)
			ui.Textf(c, "%d 楼", st.MaxFloor).FontSize(11).
				TextColor(t.TextMuted).SingleLine().Shrink(0)
		}
	})
}

// postStatsBadge 是帖子行右端的状态块：置顶角标，以及一个回复气泡加回复数。
// 回复数只显示数字、不写「N 回复」，气泡图标负责说明这个数字是什么；完整
// 说法留在提示里。
func (a *app) postStatsBadge(c *ui.Context, it Item) {
	t := c.Theme()
	badge := ui.Row(c).Gap(6).Shrink(0).AlignItems(ui.Center)
	badge.Children(func() {
		if it.Pinned {
			ui.Text(c, "置顶").FontSize(10).Bold().TextColor(t.Accent).
				SingleLine().Shrink(0)
		}
		ui.Icon(c, commentBubbleIcon).Size(12, 12).TextColor(t.TextMuted)
		ui.Textf(c, "%d", it.Stats.Replies).FontSize(12).
			TextColor(t.TextMuted).SingleLine().Shrink(0)
	})
	badge.Tooltip(strconv.Itoa(it.Stats.Replies) + " 条回复")
}

// openItem 是列表行的默认打开方式：站内帖子进正文，其余交给系统浏览器。
func (a *app) openItem(it Item) {
	switch {
	case it.ShortID != "":
		a.openDetail(it)
	case it.URL != "":
		a.srv.OpenURL(it.URL)
	}
}

// loginView 只是回退页：正常登录在标题栏点一下就打开浏览器、等系统跳回，
// 不需要来这里。它只在授权失败、或用户主动选择粘贴回调链接时出现。
func (a *app) loginView(c *ui.Context) {
	t := c.Theme()
	a.titleBar(c)
	ui.Divider(c)
	ui.Column(c).Fill().Padding(20, 24).Gap(12).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "← 返回").Clicked() {
				// 从授权弹窗的「粘贴回调」来到这一页时，离开就等于放弃这次
				// 授权：把待重跑调用一起丢掉，避免以后突然执行过期操作。
				if a.hasPendingAuthorization() {
					a.cancelAuthGate("已取消授权")
				}
				a.pageName = "list"
			}
			ui.Text(c, "登录 2Libra").FontSize(18).Bold()
			ui.Spacer(c).Grow(1)
			if name := a.auth.username(); name != "" {
				a.userTag(c, userTagSpec{
					Name:   name,
					Avatar: a.auth.avatarURL(),
					Label:  "当前：" + name,
					Size:   12,
					Color:  t.TextMuted,
				})
				if ui.Button(c, "退出登录").Clicked() {
					a.auth.Logout()
				}
			}
		})
		ui.Divider(c)
		if a.schemeWarn != "" {
			ui.Text(c, a.schemeWarn).FontSize(12).TextColor(t.Danger).MaxLines(4)
		}
		if a.loginMsg != "" {
			ui.Text(c, a.loginMsg).TextColor(t.TextMuted).MaxLines(4)
		}
		if a.hasPendingAuthorization() {
			ui.Text(c, "完成这次授权后会自动继续刚才被中断的操作。").FontSize(12).TextColor(t.TextMuted)
		}
		if ui.PrimaryButton(c, "打开浏览器授权").Clicked() {
			a.startLoginWithScopes(a.pendingAuthScopes())
		}
		ui.Text(c, "浏览器没有自动跳回？把地址栏里的完整链接粘贴到这里，完成同一次授权：").
			FontSize(12).TextColor(t.TextMuted)
		ui.TextInput(c, &a.callbackURL).Label("回调链接").Placeholder(defaultRedirectURI + "?code=...&state=...").FillWidth()
		if ui.Button(c, "用回调链接完成登录").Clicked() {
			if err := a.auth.Complete(a.callbackURL); err != nil {
				a.loginMsg = err.Error()
			} else {
				a.loginMsg = "已收到回调，正在用授权码换取令牌…"
			}
		}
		ui.Divider(c)
		ui.Text(c, "本应用由第三方开发者独立开发运营，与 2Libra 无隶属或合作关系。2Libra 仅提供账号授权与接口，不对本应用的可用性、内容与数据处理负责。你可以随时在 2Libra 的「授权应用」页面撤销本应用的授权。应用内展示的内容版权归原作者所有，详情页保留作者昵称与原文链接。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(4)
		ui.Spacer(c).Grow(1)
	})
}

// handleCallbackURL receives the OS deep link. The PKCE verifier is held in
// memory, so a callback that cold-started the app cannot resume the login.
//
// The link is echoed to the console on every delivery: mygo dev forwards the
// app's stdout/stderr, and a callback the system never delivered and one it
// delivered in an unexpected spelling both look like "nothing happened" in
// the window. Only the console tells them apart.
func (a *app) handleCallbackURL(raw string) {
	raw = strings.TrimSpace(raw)
	if !hasCallbackScheme(raw) {
		log.Printf("深链：忽略非本应用协议的链接 %q", raw)
		return
	}
	log.Printf("深链：收到 %q", raw)
	if !isCallbackLink(raw) {
		// It is our scheme but not the callback path. Links carrying a code
		// are attempts at logging in: dropping them silently leaves the user
		// on the login page with no idea what happened, so say what arrived.
		if strings.Contains(raw, "code=") {
			a.loginMsg = "收到的系统回调地址与 " + defaultRedirectURI + " 不一致：" + raw + "。请把浏览器地址栏里的完整链接粘贴到下方完成登录。"
			a.status = a.loginMsg
			a.pageName = "login"
			return
		}
		return // some other deep link of this app, not an OAuth callback
	}
	if err := a.auth.Complete(raw); err != nil {
		a.loginMsg = "回调无法完成登录：" + err.Error() + "。如果应用是被浏览器链接刚启动的，PKCE 会话不在本进程，请点「打开浏览器授权」重新发起。"
		a.status = a.loginMsg
		a.pageName = "login"
		return
	}
	a.loginMsg = "已收到系统回调，正在用授权码换取令牌…"
}

// startLogin 打开系统浏览器并进入等待状态：不回登录页、不需要第二次点击，
// 系统回调（或用户粘贴的回调链接）完成授权后自行登录。
func (a *app) startLogin() {
	a.startLoginWithScopes(nil)
}

// startLoginWithScopes 是上面那条流程的唯一实现：额外 scope 来自全局授权
// 弹窗登记的缺失权限，普通登录传 nil 只申请默认范围。
func (a *app) startLoginWithScopes(scopes []string) {
	authorizeURL, err := a.auth.BeginScopes(context.Background(), scopes)
	if err != nil {
		a.cancelAuthGate("无法开始授权：" + err.Error())
		a.pageName = "login"
		return
	}
	a.loginPending = true
	a.loginMsg = "已在浏览器打开授权页，同意后会自动跳回。"
	a.status = a.loginMsg
	a.srv.OpenURL(authorizeURL) // URL 服务在 UI 线程调用
}

// cancelLogin 放弃本次授权：AuthManager 丢掉 PKCE 会话，界面回到未登录。
// 如果这次授权是为被中断的调用发起的，那些调用也一并丢弃。
func (a *app) cancelLogin() {
	a.auth.Cancel()
	a.loginPending = false
	if a.hasPendingAuthorization() {
		a.cancelAuthGate("已取消授权")
		return
	}
	a.loginMsg = "已取消登录"
	a.status = a.loginMsg
}

func main() {
	a := newApp("")
	a.autoLoad = true
	// 自动更新：更新包来自 github.com/vhxubo/t2-desktop 的 release，由
	// mygo build 用 mygo.json 里 updates.publicKey 对应的私钥签名。native
	// 更新窗口不需要 WebView，与本应用的原生 UI 一致：启动后检查一次，之后
	// 每天一次，也在设置窗口的「检查更新」被点击时立即检查。
	mygo.Use(updatenative.Plugin)
	a.updates = true
	// 先按保存的外观模式设置原生外观，让启动首帧、原生更新/通知窗口与
	// theme.json 一致；自绘界面仍由 view 每帧设置。
	mygo.Theme.SetSource(themeSource(a.theme.Mode()))
	// 深链回调先注册，再取单实例锁：浏览器跳回时系统拉起的是一个新进程，
	// 它把命令行转给第一个实例，转发可能早于下面几行执行。mygo 的
	// OnOpenURL 只在投递那一刻有监听者时才送达，注册晚了链接就被丢掉。
	mygo.App.OnOpenURL(func(raw string) {
		a.publish(func() { a.handleCallbackURL(raw) })
	})
	// On Windows/Linux the custom-scheme callback starts a second process;
	// hand its URL to the instance that holds the PKCE verifier.
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}
	// mygo.json declares the scheme for packaged builds (macOS bundle,
	// Linux desktop entry). Registering at run time also covers Windows
	// and apps launched without an installer. Failure only disables the
	// automatic jump back: pasting the callback link still works.
	if err := mygo.App.RegisterURLScheme(appIdentifier); err != nil {
		a.schemeWarn = "系统未把 " + appIdentifier + " 关联到本应用（" + err.Error() + "）：授权后浏览器不会自动跳回，请复制地址栏里的完整链接粘贴到下方。"
		log.Printf("运行时注册 %s 未成功（打包版由 mygo.json 的 urlSchemes 声明；仍可手动粘贴回调链接）：%v", appIdentifier, err)
	} else {
		log.Printf("回调协议已注册：%s", appIdentifier)
	}
	// 原生通知的点击只带回 id，由 openNotificationNotice 查回目标：帖子和
	// 评论在应用内打开，其余交给系统浏览器。注册在 Run 之前，因此连「点击
	// 通知才启动本进程」的那一次也能收到。
	mygo.App.OnNotificationClick(func(id string) {
		a.publish(func() { a.openNotificationNotice(id) })
	})
	// 站内读掉消息不会产生 notification_update，窗口回到前台时校准一次。
	mygo.App.OnDidBecomeActive(func() {
		a.publish(func() { a.onBecameActive() })
	})
	// 退出可能来自托盘菜单、系统菜单或会话结束：先立这个标记，主窗口的关闭
	// 才不会被「收进托盘」拦下来，退不掉。
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { a.quitting = true })
	mygo.App.WhenReady(func() {
		// 原生通知要等应用就绪：Windows 走托盘气泡，macOS 首次会询问授权。
		// 头less 测试不经过这里，a.notify 保持 nil，不会触碰系统通知。
		if mygo.NotificationsSupported() {
			a.notify = showNativeNotice
		}
		// 图片缓存要在后台定期清理过期帖子图片：启动时先清一次，之后按
		// imageCacheCleanupInterval 再清。无头测试不经过这里。
		a.startImageCacheMaintenance()
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:             "T2 Desktop",
			Width:             1080,
			Height:            720,
			MinWidth:          760,
			MinHeight:         520,
			StateKey:          "main",
			Content:           ui.View(a.view),
			TitleBarStyle:     mygo.TitleBarHidden, // 隐藏原生标题栏
			HideWindowButtons: true,                // 连系统按钮也隐藏，完全自绘最小化/最大化/关闭
		})
		// 主窗口关掉时把通知、设置、我的、金币等附属窗口一起关掉：没有主
		// 窗口的它们都是孤岛，而且应用的最后一个窗口关闭时进程就该退出。
		a.win.OnClosed(func() {
			// 主窗口没了，标题栏的金币数也不用再刷新。
			a.stopCoinTagWatch()
			a.closeNotificationWindow()
			a.closeSettingsWindow()
			a.closeProfileWindow()
			a.closeCoinWindow()
			a.closePandoraWindow()
			a.closeComposeWindow()
			a.closeSearchWindow()
			a.closeAboutWindow()
		})
		// 关闭按钮（自绘标题栏、Alt+F4、任务栏右键都一样）先问设置：默认
		// 照常关闭，选了「收进托盘」就拦下来藏起窗口，见 window_mode.go。
		a.win.OnClose(a.onMainWindowClose)
		// 托盘图标：应用就绪后才有原生通知区域，见 tray.go。创建失败只记
		// 日志，主窗口照常使用。
		a.startTray()
		// 表情表启动时就拉一次（文档同样建议启动时取并缓存）：等到第一次打开
		// 详情再拉，正文会先按 :code: 短码排一遍，短码变成图片时整段重排。
		// 放在主窗口创建之后，结果才能经 publish 回到 UI 线程。
		a.ensureEmojis()
		if a.auth.loggedIn() {
			a.refreshUnread()
			a.startNotificationWatch()
			// 登录过的老会话启动时就先把金币数读出来，之后定时刷新。
			a.startCoinTagWatch()
			// 首页标题栏的潘多拉事件名也来自这一份静默读取。
			a.loadPandoraEntry()
			// 令牌里已经缓存了用户名与头像地址；这里再校准一次，老会话
			// 也能补上头像，并在加载完成后刷新标题栏。
			a.ensureAvatar()
			// Pro 状态同样属于老会话要补齐的信息：官方图床只对 Pro 开放，
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
				})
			}()
		}
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal("应用启动失败：", err)
	}
}
