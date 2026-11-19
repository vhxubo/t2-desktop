package main

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	updatenative "github.com/egoist/mygo/plugins/updater/native"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/motion"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
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

	// fold 是「最新评论」页的连续同用户聚合：相邻同名评论收成一组，超过两条
	// 才默认只露第一条（见 comments.go 的 groupComments）。它是 items 的派生
	// 数据，items 每次变动后由 refold 重算，不单独维护顺序。
	fold tabFold

	// 通知（弹窗）专用：分段筛选、关键词搜索、正在提交已读的 id 与勾选批量。
	// keyword 是已经生效的搜索词（拼进请求），keywordDraft 是输入框里的草稿：
	// 只有点「搜索」才应用，避免「输入即搜索」把配额耗在打字的中间状态上。
	filter       int
	keyword      string
	keywordDraft string
	reading      map[string]bool
	selected     map[string]bool
	markingAll   bool
}

// tabFold 存一页的分组结果与展开状态。
type tabFold struct {
	on bool // 这个 tab 开着聚合；只有它为真时才读 groups
	// groups 覆盖 items 的全部行并保持原顺序；start 是组首条在 items 里的下标。
	groups []commentGroup
	// open 记哪几组被展开了。键是组首条评论的评论 ID，不放下标：列表会定时
	// 刷新，新评论插到最前面时下标整体后移，用下标会把展开状态挪到别组头上。
	open map[string]bool
}

// app holds only data and widget state. Never store ui.Element or *Context:
// background work publishes results through Window.Update.
type app struct {
	auth *AuthManager
	// book 是进程级的账号册（见 accounts.go）。多账号时每个账号一个 app、
	// 一个主窗口，共用同一个账号册；没有账号册时（无头测试、单窗口场景）
	// 登录与退出登录退回到「本窗口自己的会话」那套行为。
	book *accountBook
	// accountID 是这个窗口所属账号在本机存储里的 id。未登录窗口为空。
	accountID string
	proxy     *proxyManager
	theme     *themeManager
	client    *Client
	win       *mygo.Window
	srv       ui.Services
	autoLoad  bool

	// viewer 是多图预览窗口的状态（见 image_viewer.go）：正文与每条评论各自
	// 成组，点任意一张配图打开这一组，左右翻页、滚轮缩放、按住左键拖动平移、
	// 右键另存为。预览画在自己的窗口里，不内嵌到详情页；同一个账号的多个窗口
	// 共用这一份状态与这一扇预览窗口。viewerNoteTimer 让另存为的结果说明到点
	// 自己撤掉。
	// viewerDoc / viewerPics 是「上一次认过图的那份解析结果」的小缓存：Markdown
	// 视图逐块问一遍，同一份文档不必每块都重认（见 picturesForDoc）。
	// 都只在 UI 线程读写。
	viewer          *imageViewer
	viewerWin       *mygo.Window
	viewerNoteTimer *time.Timer
	viewerDoc       *parser.Document
	viewerPics      []articlePicture
	// saveImagePathHook 是「另存为」系统对话框的测试替身：headless 里没有原生
	// 对话框，测试把它换成直接返回一个路径。为 nil 时走 mygo.Dialog.Save。
	saveImagePathHook func(raw string) (string, error)

	// 设置窗口里的代理草稿。保存前只改这些字段，不影响正在使用的 transport；
	// 点「保存」才写入 proxy.json 并立即切换。保存结果见 toasts.go 的 toast。
	proxyMode    int
	proxyURL     string
	proxyUser    string
	proxyPass    string
	proxyNoProxy string
	// proxyCheck 是「测试连通性」的进行状态；结果走窗口内 toast，不留在页面里。
	proxyCheck proxyCheckStatus

	// settingsTab 是设置窗口当前显示的分类。原来五块内容全堆在一个滚动区，
	// 页面长到要滚很久；分栏后每页只画一块，切换分类不丢已填的草稿。
	settingsTab int

	// settingsWin 是设置窗口：代理、检查更新与图片缓存都收在这一个窗口里，
	// 不再是标题栏上的两个按钮。入口在主页侧边栏底部，见 sidebarFooter
	// 与 showSettings；只在主线程读写。
	settingsWin *mygo.Window
	// aboutWin 是「关于」窗口：开发者、邮箱、项目地址与 2Libra 主页都列在
	// 这一个窗口里。入口见 sidebarFooter 与 showAbout；只在主线程读写。
	aboutWin *mygo.Window
	// 网络调试窗口（见 netlog_ui.go）：查看进程级记录的所有 HTTP 请求。
	// netLogUnwatch 是记录器回调的反注册函数，窗口开着才非 nil；
	// netLogSel / netLogKind / netLogFilter 是列表的选中与筛选状态；
	// netLogLastRedraw 配合 netLogRedrawMu 给「新请求到达就刷新窗口」节流。
	// 都只在主线程读写（netLogChanged 在网络协程里只碰节流用的互斥量）。
	netLogWin        *mygo.Window
	netLogSel        int
	netLogKind       int
	netLogFilter     string
	netLogUnwatch    func()
	netLogRedrawMu   sync.Mutex
	netLogLastRedraw time.Time
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
	// navWidth / navExpanded 是 Lobe DraggableSideNav 的持久状态：可拖拽调宽、
	// 可折叠成图标轨。默认折叠，宽度 0 让组件用它的展开默认值。
	navWidth    float32
	navExpanded bool
	tabs        []*tabState
	reqSeq      uint64

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
	// 对应窗口下一次重建时再交给 c.Toast。可能由后台 goroutine（经 publish）
	// 写入，也由渲染帧读取，所以用 toastMu 保护，避免竞态。
	toasts  []toastNote
	toastMu sync.Mutex
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

	// commentCost 是评论框上方那条「发评金币与徽章」状态栏的数据：我的徽章
	// （含 attributes）、佩戴状态与正在切换的那一枚（见 comment_cost.go）。
	// 它是账号级的，与具体帖子无关，所以挂在这里而不是 detailState 上。
	commentCost *commentCostState

	detail      *detailState
	detailStack []*detailState // 正文内联跳转的返回栈
	// detailWindows 是这扇主窗口开出的所有详情窗口（一篇一扇，可合并成
	// 一扇带标签的窗口）。详情窗口与主窗口互不牵连：关掉一篇不影响别的
	// 标签、别的窗口；主窗口关掉后详情窗口仍可继续看，最后一扇窗口关掉时
	// 这个账号的界面才真正收掉。都只在主线程读写。
	detailWindows []*detailWindow
	nextDetailID  int
	mainClosed    bool // 主窗口已经真的关掉，但还有别的窗口开着
	// finalized 保证这个账号的界面只收尾一次：所有窗口（主窗口、详情窗口与
	// 各附属窗口）都关掉时才 finalize，晚到的窗口关闭回调不再重复收尾。
	finalized    bool
	emojis       map[string]emojiInfo
	emojiLoading bool
	emojiTried   bool
	md           *markdown.Options
	// commentMd 是评论正文的 Markdown 选项：与正文同一套渲染，字号更小
	// （commentTextStyle），让评论区比正文紧凑一号。
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
	// replyWin 是独立写评论窗口（见 reply_window.go）：入口是详情页评论框
	// 工具条上的展开图标。replyWinFor 是它正在编辑的那份 detailState——草稿
	// 一直存在那个状态里，所以关掉窗口或「收回评论区」都不会丢字。都只在
	// 主线程读写。
	replyWin    *mygo.Window
	replyWinFor *detailState
	// editWin 是「编辑」窗口（见 edit_window.go）：改已发表的帖子、改自己的
	// 评论、给帖子追加附言。edit 是这次编辑的原文与草稿，窗口关掉就丢弃——
	// 编辑是一次性的短操作，留一份旧正文只会在下次打开时盖掉最新内容。都只在
	// 主线程读写。
	editWin *mygo.Window
	edit    *editState
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
	// followsWin 是「关注与粉丝」窗口（见 follows.go / follows_ui.go）：入口是
	// 「我的」窗口概览里的粉丝 / 关注两个统计块。follows 是它读的状态，
	// followsShown 表示窗口此刻是否在屏幕上。都只在主线程读写。
	followsWin   *mygo.Window
	follows      *followsState
	followsShown bool
	// followUnread 是关注流未读数，作为侧边栏「动态」一项的角标；
	// followUnreadGen 丢弃过期的未读数请求。都只在主线程读写。
	followUnread    int
	followUnreadGen uint64
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
	// 「只关窗口 / 收进托盘 / 退出应用」，以及这次要不要记住选择。只在主线程读写。
	closePromptOpen bool
	closeRemember   bool
	// closingAfterPrompt 表示这次关闭是弹窗里刚选的「只关这个窗口」：放行一次，
	// 不再重复询问（见 window_mode.go 的 chooseCloseMode）。
	closingAfterPrompt bool
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
	// id 里带账号段（见 notificationNativeID），多账号时点击才知道该找哪个
	// 窗口。
	noticeTargets map[string]string
	// seenNotifications 是一帧里「看到了就标已读」的消息：详情页在评论列表里
	// 发现目标评论进入了可视区时记下来，该帧构建结束再发请求（见
	// markNotificationSeen / flushSeenNotifications）。
	seenNotifications []string

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

	// scores / scoresWin 是评分榜窗口的状态与窗口（见 scores_window.go）。
	scores    *scoresState
	scoresWin *mygo.Window
}

// tabID 把 tab 序号映射成导航项 id；导航轨的选中态由 selectTab 直接维护。
func tabID(i int) string { return "t" + strconv.Itoa(i) }

// appDeps 是进程级的共享依赖：一个进程一份，多个账号窗口共用。
//
// 代理、外观、图片缓存与离线缓存都必须是全应用一份：每个窗口各建一份会让
// 代理切换只对一个窗口生效，而离线缓存（cache/offline.json）会被几个窗口
// 各自整份写回，后写的把前面账号的阅读记录覆盖掉。
type appDeps struct {
	dir       string
	proxy     *proxyManager
	theme     *themeManager
	images    *imageCache
	kv        *localKV
	closeMode *closeModeStore
	book      *accountBook
}

// newAppDeps 造一份独立的共享依赖：没有账号册（无头测试）时每个 app 自己
// 一份，互不干扰。
func newAppDeps(dir string) appDeps {
	if dir == "" {
		dir = configDir()
	}
	return appDeps{
		dir:       dir,
		proxy:     newProxyManager(dir),
		theme:     newThemeManager(dir),
		images:    newImageCache(dir),
		kv:        newOfflineKV(dir),
		closeMode: newCloseModeStore(dir),
	}
}

// deps 是账号册那份共享依赖：所有账号窗口都从这里建，不各自造一份。
func (b *accountBook) deps() appDeps {
	return appDeps{
		dir:       b.dir,
		proxy:     b.proxy,
		theme:     b.theme,
		images:    b.images,
		kv:        b.kv,
		closeMode: b.closeMode,
		book:      b,
	}
}

// newApp 建一个独立的单账号界面（无头测试、以及没有账号册的场景）。
func newApp(dir string) *app { return newAppWithDeps(newAppDeps(dir), nil) }

// newAccountApp 建一个账号窗口的界面状态：会话来自账号册，共享依赖也来自
// 账号册。窗口本身由 app.openMainWindow 创建。
func newAccountApp(b *accountBook, acc *account) *app {
	a := newAppWithDeps(b.deps(), acc)
	a.autoLoad = true
	return a
}

// newGuestApp 建未登录窗口的界面状态：没有账号时它照样能浏览公开内容，登录
// 入口把它变成一个新账号的窗口（见 accountBook.startAddAccount）。
func newGuestApp(b *accountBook) *app {
	a := newAppWithDeps(b.deps(), nil)
	a.autoLoad = true
	return a
}

func newAppWithDeps(d appDeps, acc *account) *app {
	// 账号窗口用它自己的会话；没有账号时用旧版单账号的会话（同一份凭据，
	// 也就是升级前的 token.json）。
	// 账号窗口用它自己的会话；没有账号时用旧版单账号的会话（同一份凭据，
	// 也就是升级前的 token.json）：未登录窗口与无头测试都走这一条。
	var auth *AuthManager
	if acc != nil {
		auth = acc.auth
	} else {
		auth = newAuthManagerAt(d.dir, d.proxy)
	}
	a := &app{
		auth:      auth,
		book:      d.book,
		proxy:     d.proxy,
		theme:     d.theme,
		client:    &Client{Auth: auth, Proxy: d.proxy},
		images:    d.images,
		kv:        d.kv,
		closeMode: d.closeMode,
		tabs:      make([]*tabState, len(tabSpecs)),
		lastTab:   -1,
		navSel:    tabID(0),
		pageName:  "list",
		// 原生通知默认开；多账号时每个账号窗口各自弹自己收到的消息，
		// 通知里写明是哪个账号（见 notify.go 的 notificationNative）。
		nativeNotify: true,
		coins:        newCoinState(),
		pandora:      newPandoraState(),
		profile:      newProfileState(),
		commentCost:  newCommentCostState(),
	}
	if acc != nil {
		a.accountID = acc.ID
	}
	if d.book != nil {
		// 这几项是进程级的：回调协议是否注册成功、更新插件装没装、托盘建没
		// 建出来、以及怎么真正退出应用。窗口只是照着用。
		a.schemeWarn = d.book.schemeWarning()
		a.updates = d.book.updates
		a.notify = d.book.notify
		a.trayReady = d.book.trayIsReady()
		a.quitApp = d.book.quitProgram
	}
	// 托盘的「退出」与窗口关闭走同一条退出流程；测试里换成记录调用，无头
	// 环境没有主循环，不能真的退出。先立 quitting：退出时主窗口的 OnClose
	// 不能再把这次退出拦成「收进托盘」。
	if a.quitApp == nil {
		a.quitApp = func() { a.quitting = true; mygo.App.Quit() }
	}
	// 本地缓存是"先读文件、后发请求"的首屏底稿：启动时就把金币与阅读记录
	// 装进内存，第一帧就能画出来。文件不存在或损坏都只是空缓存（共享的
	// kv 只会真正读一次盘）。
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
	a.bindAuth()
	a.md = newMarkdownOptions(a, postTextStyle)
	a.commentMd = newMarkdownOptions(a, commentTextStyle)
	a.bioMd = newMarkdownOptions(a, bioTextStyle)
	a.viewer = &imageViewer{}
	return a
}

// bindAuth 把会话变化接回本窗口。窗口开着时由窗口处理：它要重画、要弹授权；
// 账号册只从 onAuthChanged 里同步昵称与头像（见 noteAuthChange）。
func (a *app) bindAuth() {
	if a.auth == nil {
		return
	}
	// 回调记下它属于哪一次会话：重新授权换掉 a.auth 之后，旧会话迟到的
	// 刷新失败 / 401 不能把新会话的窗口切回登录页（那看起来就是刚授权完又
	// 掉登录）。
	auth := a.auth
	auth.setOnChange(func(err error) {
		a.publish(func() {
			if a.auth != auth {
				return
			}
			a.onAuthChanged(err)
		})
	})
}

// replaceAuth 换掉本窗口的会话（同一账号重新授权后令牌换了）：客户端、长
// 连接与界面全部改读新会话，然后按「刚登录」重建一次。
func (a *app) replaceAuth(auth *AuthManager) {
	if auth == nil || auth == a.auth {
		return
	}
	old := a.auth
	if old != nil {
		// 旧会话上可能还挂着在途请求：断开回调，迟到的失败不许再影响界面。
		old.detach()
	}
	a.auth = auth
	if a.client != nil {
		a.client.Auth = auth
	}
	a.bindAuth()
	a.onAuthChanged(nil)
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
		a.redrawReplyWindow()
		a.redrawEditWindow()
		a.redrawUserCardWindow()
		a.redrawSearchWindow()
		a.redrawFollowsWindow()
		a.redrawViewerWindow()
		for _, h := range a.detailWindows {
			h.invalidate()
		}
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
	a.loadFor(toastMain, idx, page)
}

// loadFor 是指定窗口的列表加载：通知窗口、资料窗口里的内容都复用这一套分页
// 与错误处理，缺权限时授权弹窗要回到发起请求的那个窗口。
func (a *app) loadFor(owner toastTarget, idx, page int) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	st := a.tabs[idx]
	spec := tabSpecs[idx]
	if spec.Kind == kindNotification {
		// 分段筛选与关键词搜索直接改接口参数，不额外请求参数名。
		spec.Query = notificationsQuery(st.filter, st.keyword)
	}
	a.loadSpec(owner, st, spec, page, func() { a.loadFor(owner, idx, page) })
}

// loadSpec 是通用的列表加载：把 spec 的第 page 页拉回来填进 st。「我的」
// 窗口里的四类内容也走这条路，所以它按 spec 而不是按侧边栏下标工作。owner
// 是发起这次加载的窗口；replay 是授权补上权限后重跑这次请求用的回调。
func (a *app) loadSpec(owner toastTarget, st *tabState, spec tabSpec, page int, replay func()) {
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
				if a.handleAuthErrorFor(owner, err, replay) {
					st.authWait = true
				}
				if page == 1 {
					st.err = friendlyError(err)
					st.items = nil
					st.fold.reset()
				} else {
					// 已有内容时续拉失败：保留当前列表，在列表底部提示。
					st.moreErr = friendlyError(err)
				}
				return
			}
			st.moreErr = ""
			if page == 1 {
				st.items = res.Items
				st.fold.reset()
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
			// 分组按整份 items 重算：第 2 页正好接上第 1 页末尾的同一个人时，
			// 尾部那一组要跟着变长，不能因为分两次到达就断成两组。
			st.refold(spec)
			// 列表行里的作者、被回复人与通知操作者都要带头像：数据到了就
			// 先预取，渲染时只读缓存，不在每帧里发请求。
			a.prefetchItemAvatars(res.Items)
		})
	}()
}

// refold 按当前 items 重算聚合分组。只对开着 FoldByAuthor 的端点做，其余
// tab 留一组「每行自成一组」的空结果。
func (st *tabState) refold(spec tabSpec) {
	st.fold.on = spec.FoldByAuthor
	if !spec.FoldByAuthor {
		st.fold.groups = nil
		st.fold.open = nil
		return
	}
	st.fold.groups = groupComments(st.items)
}

// reset 在整页重载（回到第 1 页）时清掉展开状态：刷新后还留着一堆展开的旧组，
// 会把刚刷新的一屏又撑长；分页追加不动它，用户展开过的那几组保持原样。
func (f *tabFold) reset() {
	f.groups = nil
	f.open = nil
}

// open 报告某一组当前是否展开。组键取组首条的评论 ID，没有 ID 时退回它在
// items 里的下标——这种行不会与别的组撞键。
func (f *tabFold) openFor(g commentGroup) bool {
	if len(f.open) == 0 {
		return false
	}
	return f.open[foldKey(g)]
}

// toggle 折叠 / 展开一组并返回它现在是否展开。
func (st *tabState) toggle(g commentGroup) bool {
	if st.fold.open == nil {
		st.fold.open = make(map[string]bool)
	}
	key := foldKey(g)
	next := !st.fold.open[key]
	if next {
		st.fold.open[key] = true
	} else {
		delete(st.fold.open, key)
	}
	return next
}

func foldKey(g commentGroup) string {
	head := g.members[0]
	if id := head.CommentID; id != "" {
		return id
	}
	return "at:" + strconv.Itoa(g.start)
}

// foldMinSize 是开始折叠的组大小：两条以内直接全部列出，超过两条才收成
// 「首条评论 + 展开入口」，免得两条的小聚合还要多点一次。
const foldMinSize = 3

// foldRow 是折叠后在列表里出现的一行。group 非 nil 时这一行是组首条评论：
// 它下面接着挂展开 / 收起入口（见 rows.go 的 foldRowView），total / open
// 只对这样的行有意义。折叠时组里其余成员不出现，展开后各自成行跟在后面。
type foldRow struct {
	item  Item
	group *commentGroup
	total int
	open  bool
}

// foldRows 展开成列表真正要画的可见行。超过两条的组先出首条评论，展开 /
// 收起入口挂在它下面（不是另起一行卡片）；折叠时组里只露这一条，展开时逐条
// 跟全部成员。两条以内的组、单条的组与没开聚合的 tab 都逐行列出。
func (st *tabState) foldRows() []foldRow {
	if !st.fold.on || len(st.fold.groups) == 0 {
		rows := make([]foldRow, len(st.items))
		for i, it := range st.items {
			rows[i] = foldRow{item: it}
		}
		return rows
	}
	rows := make([]foldRow, 0, len(st.items))
	for gi := range st.fold.groups {
		g := &st.fold.groups[gi]
		if len(g.members) < foldMinSize {
			for _, it := range g.members {
				rows = append(rows, foldRow{item: it})
			}
			continue
		}
		open := st.fold.openFor(*g)
		rows = append(rows, foldRow{item: g.members[0], group: g, total: len(g.members), open: open})
		if !open {
			continue
		}
		for _, it := range g.members[1:] {
			rows = append(rows, foldRow{item: it})
		}
	}
	return rows
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
	// 被限流后不要每帧重发：闸门在退避期内会拒绝所有 API 请求，这里连
	// 失败的本地回调也不必再排一遍，页面上的提示保留第一次的秒数。
	if a.client != nil && a.client.rateGate().remaining() > 0 {
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
	st.fold.reset()
	st.err = ""
	st.moreErr = ""
	st.noMore = false
	st.list.ScrollTo(0, ui.Start)
	a.load(a.tab, 1)
}

// refreshTab 把某一页的第一页内容重新拉一遍，对应「进入这一页就刷新一次」。
// 不先清空列表：切页时先看到上一次的内容，新数据到了再整体换掉并回到顶部，
// 不会闪一下空白。正在加载、正在等授权时不动，免得两次请求抢同一份结果。
func (a *app) refreshTab(idx int) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	st := a.tabs[idx]
	if st == nil || st.loading || st.authWait {
		return
	}
	if spec := tabSpecs[idx]; spec.NeedsAuth && !a.auth.loggedIn() {
		// 需要登录的页交给选中处理去弹授权，这里不发注定失败的请求。
		return
	}
	// 打开「动态」就把关注流标成已读：角标说明的是上次离开之后新增了多少
	// 条，看完这一页它就该消失（见 follows.go 的 markFollowsRead）。
	if tabSpecs[idx].Path == "/v1/me/follows/timeline" {
		a.markFollowsRead()
	}
	st.err = ""
	st.moreErr = ""
	a.load(idx, 1)
}

// reclickTab 是侧边栏一行的点击：点的不是当前这一页就交给选中变化去切页，
// 点的正是当前这一页就再刷新一次——选中值没变时 Sidebar 不报 Changed，
// 所以这一下挂在这一行自己的点击上。
func (a *app) reclickTab(idx int) {
	if idx != a.tab {
		return
	}
	a.refreshTab(idx)
}

func (a *app) onAuthChanged(err error) {
	a.loginPending = false
	// 标题栏的窗口标题带着账号名：登录、退出、重新授权后都要跟着变，任务栏
	// 里才好分辨这是哪个账号的窗口。
	if a.win != nil {
		a.win.SetTitle(a.windowTitle())
	}
	// 账号册记的是「本机有哪些账号」：昵称、头像与「登录是否还有效」都要
	// 从这次会话变化同步过去（账号面板与托盘读的是那一份）。
	if a.book != nil && a.accountID != "" {
		a.book.noteAuthChanged(a.accountID)
	}
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
	// 评论框的状态栏读的是这个账号的徽章与佩戴状态：重新授权后按新令牌重新
	// 读一次，换账号 / 退出则整体丢弃，不沿用上一个账号的徽章。
	a.commentCost = newCommentCostState()
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
		a.resetFollowsUnread()
		a.refreshUnread()
		a.startNotificationWatch()
		// 关注流的未读角标也属于当前账号：登录 / 换账号后重新读一次。
		a.refreshFollowsUnread()
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
		// 独立写评论窗口也一样：退出后没有可发的身份，草稿留在页面状态里，
		// 窗口收起来。
		a.closeReplyWindow()
		// 编辑窗口里是上一个账号的内容，退出后一并关掉并丢弃。
		a.closeEditWindow()
		// 关注与粉丝名单、关注流未读角标都只属于当前账号。
		a.closeFollowsWindow()
		a.follows = nil
		a.resetFollowsUnread()
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
		a.detailView(c, true)
		a.dialogs(c)
		return
	}

	// 整页分两栏：左侧是从窗口顶端贯到底的导航栏，右侧是标题栏 + 内容。
	// 标题栏只盖住内容区，左栏因此没有一条横切过去的深色带。
	ui.Row(c).Fill().Children(func() {
		a.sidebar(c)
		ui.Divider(c)
		ui.Column(c).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() {
			a.titleBar(c)
			ui.Divider(c)
			// 右侧内容区：正文列表（无限滚动，无底部状态栏）。右下角浮着
			// 一列 FAB（返回顶部 / 刷新 / 发帖），绝对定位，不占列表的高度。
			ui.Column(c).Fill().MinHeight(0).Children(func() {
				a.body(c)
				a.listFabs(c)
			})
		})
	})
	a.ensureLoaded()
	a.dialogs(c)
}

// dialogs 画出全局授权弹窗与关闭询问，并补开还在等图片的预览。列表页、登录
// 回退页与详情页共用，放在内容之后叠在最上层。预览本身是一扇独立窗口，不画
// 在这里；这里只负责在图片到位后把它打开（见 viewerTick）。
func (a *app) dialogs(c *ui.Context) {
	a.viewerTick()
	a.authDialogFor(c, toastMain)
	a.closePromptDialog(c)
}

// feedMaxWidth 是正文栏的最大宽度：窗口再宽，一行文字也不超过 760 DIP，
// 剩下的空间平均留白，阅读视线不必横向来回扫。
const feedMaxWidth float32 = 760

// navSidebarWidth 是正文居中计算用的导航轨折叠宽度。Lobe DraggableSideNav
// 折叠后是 64 DIP（MinWidth），展开宽度由 navWidth 决定。
const navSidebarWidth = 64
const navRailWidth float32 = 64

// navListInset 是展开态列表在 Compact 行内边距之外再留的左侧空白：图标不再
// 贴着侧边栏左缘，与折叠后图标轨的居中位置对齐。
const navListInset float32 = 8

// sidebarListInset 随折叠进度收回展开态的额外左留白，让行滑进图标轨时不错位。
func sidebarListInset(nav layout.SideNavState) float32 {
	return (1 - min(max(nav.Collapse, 0), 1)) * navListInset
}

// sidebarFont 是元信息用的等宽字体：小字号下数字与时序排得整齐，
// 和无衬线字体形成两层质感。
const sidebarFont = "monospace"

// sidebar 是 Lobe 风格的可展开侧边栏：layout.DraggableSideNav 提供拖拽调宽，
// 展开/折叠只走底部列表的「展开 / 折叠侧边栏」入口（见 sidebarFooterList），
// 不再在接缝上画一个重复的开关。内容与 Lobe 的 display.List 配套——把
// nav.Collapse / nav.RailWidth 交给 List，收起时行缩成图标方块，展开时显示
// 文字。顶部是账号，中部是浏览页，底部是外观与账号入口。
func (a *app) sidebar(c *ui.Context) {
	layout.DraggableSideNav(c, &a.navWidth, &a.navExpanded, layout.DraggableSideNavProps{
		MinWidth:        navRailWidth,
		MaxWidth:        280,
		NoHandle:        true,
		HighlightHandle: true,
		Header: func(nav layout.SideNavState) {
			a.sidebarHeader(c, nav)
		},
		Footer: func(nav layout.SideNavState) {
			a.sidebarFooterList(c, nav)
		},
	}, func(nav layout.SideNavState) {
		a.sidebarNavList(c, nav)
	})
}

// sidebarHeader 是可展开侧边栏的顶部：品牌标记 + 账号头像，折叠时只留头像
// 图标，展开时并排显示应用名。点头像开账号面板。
func (a *app) sidebarHeader(c *ui.Context, nav layout.SideNavState) {
	ui.Column(c).Gap(6).Padding(10, 6, 6, 6).Children(func() {
		b := ui.ButtonBase(c).Label("账号头像").Tooltip("打开账号面板").OnClick(a.showAccounts)
		b.Row().Gap(10).AlignItems(ui.Center).Radius(lobetheme.TokensOf(c).BorderRadius).Padding(4, 6)
		b.Children(func() {
			if a.auth.loggedIn() {
				avatarView(c, "", a.avatarBitmap(a.auth.avatarURL()), 24)
			} else {
				ui.Icon(c, lucide.UserRound()).FontSize(18)
			}
			if nav.Expanded {
				ui.Text(c, "T2 Desktop").FontSize(13).FontWeight(600).SingleLine()
			}
		})
	})
}

// sidebarNavItems 是浏览页导航的列表数据，展开态交给 display.List，折叠态
// 交给 sidebarRail 的 ActionIcon。
func (a *app) sidebarNavItems() []display.ListItem {
	items := make([]display.ListItem, 0, len(tabSpecs))
	for i, s := range tabSpecs {
		if s.Kind == kindNotification {
			continue
		}
		icon := tabIcons[i]
		if i >= len(tabIcons) || icon == nil {
			icon = lucide.LayoutGrid()
		}
		items = append(items, display.ListItem{
			Key:   tabID(i),
			Label: a.followsTabLabel(i, s.Label),
			Icon:  icon,
		})
	}
	return items
}

// sidebarNavList 展开时用 Lobe display.List 画带文字的导航行，左侧留出
// navListInset；折叠时换成 sidebarRail 的图标轨，指针停留有 tooltip。
func (a *app) sidebarNavList(c *ui.Context, nav layout.SideNavState) {
	items := a.sidebarNavItems()
	if !nav.Expanded {
		sidebarRail(c, items, a.navSel, func(key string) {
			if idx, ok := tabIndexFromID(key); ok {
				a.selectTab(idx)
			}
		})
		return
	}
	ui.Column(c).Padding(0, 0, 0, sidebarListInset(nav)).Children(func() {
		clicked := display.List(c, display.ListProps{
			Items:     items,
			Active:    a.navSel,
			Compact:   true,
			Collapse:  nav.Collapse,
			RailWidth: nav.RailWidth,
		})
		if clicked != "" {
			if idx, ok := tabIndexFromID(clicked); ok {
				a.selectTab(idx)
			}
		}
	})
}

// sidebarRail 把折叠态的导航画成 Lobe ActionIcon 图标轨：保留 ActionIcon 的
// 外观与状态，再用 Lobe 的 overlay.Tooltip 把名字浮在图标右侧，指针停留或
// 键盘聚焦都能看到，无障碍描述也是同一个名字。
func sidebarRail(c *ui.Context, items []display.ListItem, active string, onSelect func(string)) {
	ui.Column(c).Gap(2).AlignItems(ui.Center).AlignSelf(ui.Stretch).Children(func() {
		for _, it := range items {
			if it.Divider {
				continue
			}
			b := action.StyleActionIcon(c, ui.ButtonBase(c), it.Icon, action.ActionIconProps{
				Active:    it.Key == active,
				BlockSize: 32,
				GlyphSize: 16,
			})
			b.Label(it.Label)
			lobeoverlay.Tooltip(c, b, it.Label, lobeoverlay.TooltipProps{Placement: lobeoverlay.Right})
			if b.Clicked() {
				onSelect(it.Key)
			}
		}
	})
}

// selectTab 是导航项一次点击的完整处理：点当前页再刷新一次，点别的页先切
// 选中、再按登录要求决定弹授权还是直接拉首屏。
func (a *app) selectTab(idx int) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	if idx == a.tab {
		a.reclickTab(idx)
		return
	}
	a.tab = idx
	a.navSel = tabID(idx)
	if spec := tabSpecs[idx]; spec.NeedsAuth && !a.auth.loggedIn() {
		a.requestLogin("「"+spec.Label+"」需要登录 2Libra 后才能查看。",
			func() { a.load(idx, 1) })
	} else {
		a.refreshTab(idx)
	}
	if a.tab != a.lastTab {
		a.lastTab = a.tab
		a.status = ""
	}
}

// sidebarFooterItems 是底部常驻入口的数据：账号/设置/关于、登录/退出。外观
// 三档已经收进设置窗口的「外观」页，侧边栏不再单独占三行。
func (a *app) sidebarFooterItems() []display.ListItem {
	items := []display.ListItem{
		{Key: "expand", Label: "展开侧边栏", Icon: lucide.PanelLeftOpen()},
		{Key: "accounts", Label: "账号", Icon: lucide.UsersRound()},
		{Key: "settings", Label: "设置", Icon: lucide.Settings()},
		{Key: "about", Label: "关于", Icon: lucide.Info()},
	}
	// 折叠时把「展开」换成「折叠」，两种状态都有明确入口。
	if a.navExpanded {
		items[0].Label = "折叠侧边栏"
		items[0].Icon = lucide.PanelLeftClose()
	}
	switch {
	case a.loginPending:
		items = append(items,
			display.ListItem{Key: "wait", Label: "等待浏览器授权", Icon: lucide.LoaderCircle()},
			display.ListItem{Key: "cancel", Label: "取消登录", Icon: lucide.X()},
		)
	case a.book != nil && a.book.pendingLogin() != nil:
		items = append(items, display.ListItem{Key: "accounts-panel", Label: "账号面板", Icon: lucide.Users()})
	case a.auth.loggedIn():
		items = append(items, display.ListItem{Key: "logout", Label: "退出登录", Icon: lucide.LogOut()})
	default:
		items = append(items,
			display.ListItem{Key: "login", Label: "登录 2Libra", Icon: lucide.LogIn()},
			display.ListItem{Key: "signup", Label: "注册", Icon: lucide.UserRound()},
		)
	}
	return items
}

// sidebarFooterList 展开时用 display.List 画底部入口并留出左侧空白；折叠时
// 换成带 tooltip 的 ActionIcon 图标轨，点击仍走同一处 handleSidebarAction。
func (a *app) sidebarFooterList(c *ui.Context, nav layout.SideNavState) {
	items := a.sidebarFooterItems()
	if !nav.Expanded {
		sidebarRail(c, items, "", a.handleSidebarAction)
		return
	}
	ui.Column(c).Padding(0, 0, 0, sidebarListInset(nav)).Children(func() {
		clicked := display.List(c, display.ListProps{
			Items:     items,
			Compact:   true,
			Collapse:  nav.Collapse,
			RailWidth: nav.RailWidth,
		})
		a.handleSidebarAction(clicked)
	})
}

// handleSidebarAction 分发底部 List 的点击 key。
func (a *app) handleSidebarAction(key string) {
	switch key {
	case "":
		return
	case "expand":
		a.navExpanded = !a.navExpanded
	case "accounts":
		a.showAccounts()
	case "settings":
		a.showSettings()
	case "about":
		a.showAbout()
	case "wait":
		a.pageName = "login"
	case "cancel":
		a.cancelLogin()
	case "accounts-panel":
		a.showAccounts()
	case "logout":
		a.logoutAccount()
	case "login":
		a.startLogin()
	case "signup":
		a.openSignup()
	}
}

// tabIndexFromID 把导航项 id（"t"+序号）映射回 tab 序号。
func tabIndexFromID(id string) (int, bool) {
	if len(id) < 2 || id[0] != 't' {
		return 0, false
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil || n < 0 || n >= len(tabSpecs) {
		return 0, false
	}
	return n, true
}

// showAccounts 打开账号面板。没有账号册（无头测试、旧的单窗口路径）时它
// 不存在，也就没有可切的东西。
func (a *app) showAccounts() {
	if a.book == nil {
		return
	}
	a.book.showPanel()
}

// 自绘标题栏的尺寸。32 DIP 是 Windows 标题栏的常规高度，窗口按钮 46×32
// 也与系统按钮同宽同高，文字和图标因此不会被压扁、裁切或挤在一起。
const (
	titleBarHeight   = 32
	titleButtonWidth = 46
)

// titleBarHeightFor 是本帧标题栏的实际高度：系统按钮占位更高时跟着它走，
// 至少 32 DIP。左侧导航栏顶端那一行要与它等高，左右两栏的分隔线才对得上。
func titleBarHeightFor(c *ui.Context) float32 {
	h := float32(titleBarHeight)
	if bar := c.TitleBar(); bar.Height > h {
		h = bar.Height
	}
	return h
}

// titleBar 是完全自绘的顶部标题栏：可拖拽移动窗口（双击最大化），右端是
// 搜索、通知与最小化 / 最大化·还原 / 关闭按钮；系统标题栏与系统按钮已由
// TitleBarHidden + HideWindowButtons 隐藏。
//
// 标题栏只盖住内容区，底色与页面一致；应用名在左侧导航栏顶端（见 sidebar），
// 左栏因此从窗口顶端贯到底，顶边不再压着一条深色横带。
func (a *app) titleBar(c *ui.Context) {
	t := c.Theme()
	// bar 是系统窗口按钮占用的区域，HideWindowButtons 下为零。保留这段
	// 计算，将来若改回系统按钮，标题栏仍然给它们让出位置。
	bar := c.TitleBar()
	h := titleBarHeightFor(c)
	ui.Row(c).Height(h).FillWidth().AlignItems(ui.Stretch).
		Padding(0, 0, 0, bar.Left).Background(t.Background).DragWindow().Children(func() {
		// 左段：品牌名、搜索、通知铃铛，登录后再加上金币、盲盒与头像昵称，
		// 整体可随标题栏拖动。设置与登录 / 退出登录都在侧边栏底部。
		ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10, 0, 16).Gap(10).
			AlignItems(ui.Center).Children(func() {
			// 品牌名对应 Lobe Header 的 Logo 位，占标题栏左端。
			display.Text(c, "T2 Desktop", display.TextProps{Strong: true})
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
		titleBarButton(c, titleMinimize, "最小化", func() {
			if a.win != nil {
				a.win.Minimize()
			}
		})
		titleMaximizeButton(c, a.win)
		titleBarButton(c, titleClose, "关闭", func() {
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

// titleBarButton 画一个窗口按钮（主窗口与各个账号/附属窗口共用同一套画法）。
// 图形用矢量线条绘制，不依赖字体里的字符：「– / □ / ▢ / ✕」这类符号在不同
// 字体下大小不一，有的字体还缺字，最大化之后会显示成空白或豆腐块，正是
// 标题栏「显示不正常」的来源。
func titleBarButton(c *ui.Context, g titleGlyph, label string, onClick func()) {
	t := c.Theme()
	// 显式给高度：按钮本体只设了宽度，过去靠父行的 AlignItems(Stretch) 撑出
	// 高度。详情窗口把这几个按钮套在一层居中、没定高的 Row 里，没有可撑的
	// 父级，Box 就塌成 0 高，点了没反应。把高度写进按钮自己，谁都能直接用。
	b := ui.Box(c).Width(titleButtonWidth).Height(titleBarHeight).Shrink(0).Background(ui.Transparent)
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

// titleMaximizeButton 画标题栏的「最大化 / 还原」按钮：按窗口此刻的状态切
// 图形、无障碍名与提示，点一下翻转。主窗口与详情窗口共用这一份，状态变化时
// 窗口尺寸跟着变，mygo 会重画标题栏，图标也随之切换。
func titleMaximizeButton(c *ui.Context, win *mygo.Window) {
	if win != nil && win.IsMaximized() {
		titleBarButton(c, titleRestore, "还原", func() { win.ToggleMaximize() })
		return
	}
	titleBarButton(c, titleMaximize, "最大化", func() {
		if win != nil {
			win.ToggleMaximize()
		}
	})
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

// listRowTransition 让列表行底色的出现与消失走 Lobe 的 150ms 缓动，和
// display.List 的行过渡保持同一节奏；只过渡颜色，不动几何尺寸。
var listRowTransition = ui.ElementTransition{
	Duration: 150 * time.Millisecond, Ease: motion.EaseOut, Colors: true,
}

// feedRowSurface 给主页列表行铺一层统一的 Lobe 表面：静止时透明，悬停时
// 铺 FillTertiary（与 display.List 行悬停同一 token），圆角取主题，颜色带
// 过渡。调用方只管往里填内容、设内边距并挂点击；这样帖子行、评论行与通知
// 行在同一套视觉语言里，不必各写一套 hover 配色。
func feedRowSurface(c *ui.Context) ui.Element {
	tk := lobetheme.TokensOf(c)
	row := ui.ButtonBase(c).FillWidth().Radius(tk.BorderRadius).Transition(listRowTransition)
	row.Background(ui.Transparent)
	if row.Hovered() {
		row.Background(tk.FillTertiary)
	}
	return row
}

// feedInset 是正文栏两侧的留白：内容区宽于 feedMaxWidth 时把多余空间等分
// 到两边，让文字栏居中；窗口本身更窄时退回 24 DIP 的最小边距。
func feedInset(c *ui.Context) float32 {
	inset := float32(24)
	if w, _ := c.Size(); w > 0 {
		if avail := w - navSidebarWidth - 1; avail > feedMaxWidth {
			inset = (avail - feedMaxWidth) / 2
		}
	}
	return inset
}

// feedHeader 是正文栏顶部的页头：上面一行是日期小字，下面是页面名的
// 大标题，右端是条目数。整块靠留白、字号差与字距分层，不画底纹和方框；
// 配色走 Lobe token，让页头与下方列表行的层次一致。
func (a *app) feedHeader(c *ui.Context, spec tabSpec, st *tabState) {
	tk := lobetheme.TokensOf(c)
	now := c.Now()
	ui.Column(c).FillWidth().Shrink(0).
		Padding(24, feedInset(c)+16, 14, feedInset(c)+16).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.End).Gap(16).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
				ui.Text(c, now.Format("2006.01.02")+" · "+weekdayCN(now.Weekday())).
					FontSize(tk.FontSizeSM).Font(sidebarFont).LetterSpacing(1.6).
					TextColor(tk.TextDescription).SingleLine()
				ui.Text(c, spec.Label).FontSize(tk.FontSizeHeading4).FontWeight(600).
					LetterSpacing(-0.4).TextColor(tk.TextHeading).SingleLine()
			})
			if count := feedCountLabel(st); count != "" {
				ui.Text(c, count).FontSize(tk.FontSizeSM).Font(sidebarFont).
					TextColor(tk.TextDescription).SingleLine().Shrink(0)
			}
		})
	})
}

// weekdayCN 把 Go 的星期翻成界面上的「周三」。页头只读本机时钟，不联网。
func weekdayCN(d time.Weekday) string {
	switch d {
	case time.Monday:
		return "周一"
	case time.Tuesday:
		return "周二"
	case time.Wednesday:
		return "周三"
	case time.Thursday:
		return "周四"
	case time.Friday:
		return "周五"
	case time.Saturday:
		return "周六"
	default:
		return "周日"
	}
}

// feedCountLabel 是页头右端的小字：有总数时写「共 N 条」，翻页时再带上
// 「第 2 / 5 页」；服务端没给总数的页（最新评论）只写已拿到的条数。
func feedCountLabel(st *tabState) string {
	n := st.total
	paged := st.totalPages > 1
	if n <= 0 {
		n = len(st.items)
		paged = false
	}
	if n <= 0 {
		return ""
	}
	out := "共 " + strconv.Itoa(n) + " 条"
	if paged {
		out += " · 第 " + strconv.Itoa(st.page) + " / " + strconv.Itoa(st.totalPages) + " 页"
	}
	return out
}

func (a *app) body(c *ui.Context) {
	st := a.tabs[a.tab]
	spec := tabSpecs[a.tab]

	if spec.NeedsAuth && !a.auth.loggedIn() {
		ui.Column(c).Grow(1).Center().Padding(24).Children(func() {
			display.Empty(c, display.EmptyProps{
				Icon:        lucide.Lock(),
				Title:       "「" + spec.Label + "」需要登录",
				Description: "该端点要求 read:me / profile 权限，登录后才会发起请求。",
				Action: func() {
					if action.Button(c, "去登录", action.ButtonProps{Variant: action.Primary}).Clicked() {
						idx := a.tab
						a.requestLogin("「"+spec.Label+"」需要登录 2Libra 后才能查看。",
							func() { a.load(idx, 1) })
					}
				},
			})
		})
		return
	}
	if st.loading && len(st.items) == 0 {
		ui.Column(c).Grow(1).Center().Children(func() {
			feedback.Spin(c, feedback.SpinProps{
				Variant: feedback.SpinNetwork,
				Size:    lobetheme.Large,
				Tip:     "正在加载 " + spec.Label + "…",
			}, nil)
		})
		return
	}
	if st.err != "" {
		ui.Column(c).Grow(1).Center().Padding(24).Children(func() {
			display.Empty(c, display.EmptyProps{
				Icon:        lucide.TriangleAlert(),
				Title:       "加载失败",
				Description: st.err,
				Action: func() {
					if action.Button(c, "重试", action.ButtonProps{}).Clicked() {
						a.reloadCurrent()
					}
					if strings.Contains(st.err, "登录") || strings.Contains(st.err, "授权") {
						if action.Button(c, "去授权", action.ButtonProps{}).Clicked() {
							idx := a.tab
							a.requestLogin("登录并授权后才能继续查看「"+spec.Label+"」。",
								func() { a.load(idx, 1) })
						}
					}
				},
			})
		})
		return
	}
	if len(st.items) == 0 {
		ui.Column(c).Grow(1).Center().Children(func() {
			display.Empty(c, display.EmptyProps{Title: "暂无内容"})
		})
		return
	}
	// 列表画的是「可见行」而不是 items 本身：最新评论开着连续同用户聚合，
	// 折叠的组在这里只出一行首条评论（展开入口挂在它下面，见 foldRows）。
	// Key 按可见行的条目给，展开 / 收起引起行数变化时列表状态按 key 对齐。
	rows := st.foldRows()
	st.list.Key = func(i int) any {
		if i >= 0 && i < len(rows) {
			return rows[i].item.Key
		}
		return i
	}
	// 正文栏居中：内容区再宽也只排 760 DIP，两侧留白等分。窗口本身比这窄时
	// 退回 24 DIP 的最小边距，不横向挤压文字。
	a.feedHeader(c, spec, st)
	ui.List(c, &st.list, len(rows), func(i int) {
		a.foldRowView(c, rows[i])
	}).Grow(1).Padding(0, feedInset(c), 10, feedInset(c)).Gap(0)
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
	tk := lobetheme.TokensOf(c)
	ui.Row(c).FillWidth().Padding(8, composeFabInset, 8, feedInset(c)).Gap(8).AlignItems(ui.Center).Children(func() {
		if errText != "" {
			ui.Text(c, "加载更多失败："+errText).FontSize(tk.FontSizeSM).
				TextColor(tk.Error).MaxLines(2).Grow(1)
			if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				retry()
			}
			return
		}
		feedback.Spin(c, feedback.SpinProps{Size: lobetheme.Small}, nil)
		ui.Text(c, "正在加载更多…").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
	})
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
	tk := lobetheme.TokensOf(c)
	titleColor := tk.Text
	if a.isPostRead(it.ShortID) {
		titleColor = tk.TextSecondary
	}
	// 统一的 Lobe 行表面：静止透明、悬停 FillTertiary、圆角与过渡取主题。
	// 纵向 padding 收紧到 Lobe 节奏（内容行略高于导航行的 6），横向 12 对齐 token。
	row := feedRowSurface(c).PaddingY(10).PaddingX(12)
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
			// 标题是这一行里唯一的重心：字号明显大于副标题和时效信息，
			// 并保持 700 粗体，扫一眼就能把标题与下方文案分开。字号用 Lobe
			// 的 LG（16），比原先的 19 收一档，贴近 Lobe 列表的密度。
			ui.Text(c, it.Title).FontSize(tk.FontSizeLG).Bold().MaxLines(2).TextColor(titleColor)
			// 副标题里的作者用统一用户标识渲染：头像跟着 11px 的昵称走，
			// 同时保留「节点 · 昵称 · 发帖时间」的原有顺序；节点名或时间太长时
			// 允许折行，不把右侧的元信息挤出去。Author 为空说明接口没给发帖人
			// （「我的」窗口的 /v1/me/topics、/v1/me/bookmarks 就是这样），
			// 这时整段作者不画，也不退回副标题里的空作者段。元信息整行用
			// 等宽字体，和标题的无衬线体形成两层。
			switch {
			case it.Author != "":
				ui.Row(c).MinWidth(0).AlignItems(ui.Center).Wrap().Font(sidebarFont).
					Children(func() {
						if it.SubtitlePrefix != "" {
							ui.Text(c, it.SubtitlePrefix).FontSize(tk.FontSizeSM).
								TextColor(tk.TextDescription).SingleLine()
						}
						a.userTag(c, userTagSpec{
							Name:   it.Author,
							Avatar: it.AuthorAvatar,
							Size:   tk.FontSizeSM - 1,
							Color:  tk.TextDescription,
							Badges: it.AuthorBadges,
						})
						// 有节点前缀时后缀自带「 · 」；没有前缀时昵称就是这一段的
						// 开头，得自己补上分隔符，别让「·」落在最前面。
						if suffix := strings.TrimPrefix(it.SubtitleSuffix, " · "); suffix != "" {
							ui.Text(c, " · "+suffix).FontSize(tk.FontSizeSM).
								TextColor(tk.TextDescription).SingleLine()
						}
					})
			case it.Subtitle != "":
				ui.Text(c, it.Subtitle).FontSize(tk.FontSizeSM).Font(sidebarFont).
					TextColor(tk.TextDescription).SingleLine()
			}
			// 帖子行多一行时效信息：最后回复人、更新时间与最高楼层。没人
			// 回复时没有这一行，右侧的回复数仍显示 0。
			if it.Stats != nil && it.Stats.Replier != "" {
				a.latestReplyRow(c, it, open)
			}
		})
		if it.Stats != nil {
			a.postStatsBadge(c, it)
		} else if it.Meta != "" {
			ui.Text(c, it.Meta).FontSize(tk.FontSizeSM).Font(sidebarFont).TextColor(tk.TextDescription).
				SingleLine().Width(190).TextAlign(ui.End)
		}
	})
	// 有 short_id 的帖子/评论在应用内看正文和评论；其余条目保持打开原文。
	// Clickable 留给没有这两种标识、但仍由调用方决定去处（合集行）的情况。
	if it.Clickable || it.ShortID != "" || it.URL != "" {
		row.OnClick(func() { open(it) })
	}
}

// latestReplyRow 是帖子行标题下方的时效行：谁最后回复、多久以前、以及
// 最新（也是最高）楼层。没人回复过的帖子没有这一行；评论与合集行也不会走
// 到这里（它们的 Stats 为 nil）。
//
// 接口给了最后一条回复的评论 id 时，这一行整行是个链接：点它带着 CommentID
// 打开正文，直接定位到那层楼（openDetail 的 focusID）。它是最内层可点元素，
// 点这行不会连带触发外层整行的点击。id 未知时退回纯文本，不可点。
func (a *app) latestReplyRow(c *ui.Context, it Item, open func(Item)) {
	st := it.Stats
	tk := lobetheme.TokensOf(c)
	link := ui.Row(c).MinWidth(0).Gap(6).AlignItems(ui.Center).Font(sidebarFont)
	ink := tk.TextDescription
	if st.LastCommentID != "" && link.Hovered() {
		ink = tk.Primary
	}
	link.Children(func() {
		label := ui.Text(c, "最后回复").FontSize(tk.FontSizeSM).TextColor(ink).SingleLine().Shrink(0)
		name := ui.Text(c, st.Replier).FontSize(tk.FontSizeSM).Bold().TextColor(ink).
			SingleLine().Shrink(1).Ellipsis("…")
		if st.LastCommentID != "" && link.Hovered() {
			label.Underline()
			name.Underline()
		}
		if st.Updated != "" {
			ui.Text(c, "·").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription).Shrink(0)
			ui.Text(c, st.Updated).FontSize(tk.FontSizeSM).TextColor(tk.TextDescription).SingleLine().Shrink(0)
		}
		if st.MaxFloor > 0 {
			ui.Text(c, "·").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription).Shrink(0)
			ui.Textf(c, "%d 楼", st.MaxFloor).FontSize(tk.FontSizeSM).
				TextColor(tk.TextDescription).SingleLine().Shrink(0)
		}
	})
	if st.LastCommentID != "" {
		jump := it
		jump.CommentID = st.LastCommentID
		link.Cursor(ui.CursorPointer).OnClick(func() { open(jump) })
		link.Tooltip("跳到最后回复")
	}
}

// postStatsBadge 是帖子行右端的状态块：置顶角标，以及一个等宽数字表示的
// 回复数。数字只写值、不写「N 回复」，悬停提示负责说明它是什么；没有气泡
// 图标，右端只剩一个安静的计数。
func (a *app) postStatsBadge(c *ui.Context, it Item) {
	badge := ui.Row(c).Gap(8).Shrink(0).AlignSelf(ui.Start).AlignItems(ui.Center).
		Padding(4, 0, 0, 0)
	badge.Children(func() {
		if it.Pinned {
			// 置顶用 Lobe 的金色 Tag，和列表里其他角标同一套视觉。
			display.Tag(c, "置顶", display.TagProps{
				Color: display.TagGold,
				Size:  lobetheme.Small,
			})
		}
		// 回复数用无边框 Tag：只留一个安静的计数，和品牌视觉一致。
		display.Tag(c, strconv.Itoa(it.Stats.Replies), display.TagProps{
			Variant: display.TagBorderless,
			Size:    lobetheme.Small,
		})
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
			// 返回是图标按钮：这一行左边只放一个左箭头，标题就能贴住左边，
			// 点了会放弃还没完成的授权，这件事写在 Tooltip 里。
			if iconActionButton(c, chevronLeftIcon, "返回",
				"回到列表；离开这一页会放弃还没完成的授权").Clicked() {
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
					a.logoutAccount()
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
			a.startLoginFromPage()
		}
		if ui.Button(c, "注册账号").Clicked() {
			a.openSignup()
		}
		ui.Text(c, "浏览器没有自动跳回？把地址栏里的完整链接粘贴到这里，完成同一次授权：").
			FontSize(12).TextColor(t.TextMuted)
		ui.TextInput(c, &a.callbackURL).Label("回调链接").Placeholder(defaultRedirectURI + "?code=...&state=...").FillWidth()
		if ui.Button(c, "用回调链接完成登录").Clicked() {
			if err := a.completeLoginCallback(a.callbackURL); err != nil {
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

// completeLoginCallback 是登录回退页上「用回调链接完成登录」：账号窗口的补
// 授权挂在账号册上（内置浏览器那条路），回调也要交给它；未登录窗口与没有
// 账号册的单窗口路径才用本窗口自己的会话。
func (a *app) completeLoginCallback(raw string) error {
	if a.book != nil && a.accountID != "" {
		if a.book.completeCallback(raw) {
			return nil
		}
		return errors.New("现在没有在等回调的授权，请先点「打开浏览器授权」")
	}
	return a.auth.Complete(raw)
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

// raiseMainWindow 把主窗口放到最前面。系统投来深链（OAuth 回调）时，窗口可能
// 处于最小化、收进托盘（Hide）或只是被别的窗口盖住三种状态：用户刚在浏览器里
// 点了这个链接，期望本窗口立即出现，所以统一拉起。Windows/Linux 上深链是浏览器
// 拉起的新进程转发过来的，那个新进程退出前已经把前台权限让给本进程
// （AllowForeground），因此这次抢前台是被系统允许的。各附属窗口的「打开即拉起」
// 用的也是这套 IsMinimized→Restore / Show / Focus / Invalidate 写法。
func (a *app) raiseMainWindow() {
	// 主窗口已经关了，但详情 / 其它窗口还开着：a.win 只是后台结果的出口，
	// 不能把它当主窗口硬拉到前台。重开一扇真正的主窗口，其它窗口照旧留着。
	if a.mainClosed {
		a.openMainWindow()
		return
	}
	win := a.win
	if win == nil {
		return
	}
	if win.IsMinimized() {
		win.Restore()
	}
	win.Show()
	win.Focus()
	win.Invalidate()
}

// startLogin 是「登录」入口。多账号时它就是「添加账号」：浏览器授权回来后
// 会为那个账号开一个自己的主窗口，已经登录的那个账号不受影响；账号窗口的
// 令牌失效后再点「登录」，重新授权的是这个窗口自己的账号，而不是再添一个。
// 没有账号册时（无头测试、单窗口路径）走本窗口自己的会话。
func (a *app) startLogin() {
	if a.book != nil {
		if a.accountID != "" {
			a.book.reloginAccount(a.accountID)
			return
		}
		a.book.startAddAccount()
		return
	}
	a.startLoginWithScopes(nil)
}

// startLoginFromPage 是登录回退页上的「打开浏览器授权」：未登录窗口里就是
// 添加账号，账号窗口里则是给这个账号补权限（多账号时走内置浏览器）。
func (a *app) startLoginFromPage() {
	if a.book != nil && a.accountID == "" {
		a.book.startAddAccount()
		return
	}
	a.startLoginWithScopes(a.pendingAuthScopes())
}

// logoutAccount 退出当前窗口所属的账号：多账号时它把这个账号从账号册里摘掉
// （令牌删掉、窗口关掉），别的账号照常挂着；没有账号册时就是普通的一次退出
// 登录。
func (a *app) logoutAccount() {
	if a.book != nil && a.accountID != "" {
		a.book.logoutAccount(a.accountID)
		return
	}
	a.auth.Logout()
}

// openSignup 打开带邀请码的 2Libra 注册页：注册在网页完成，应用内不做。未
// 登录用户点「注册」直接打开系统浏览器，注册成功后再回应用里「登录」授权。
func (a *app) openSignup() {
	a.srv.OpenURL(signupURL) // URL 服务在 UI 线程调用
}

// startLoginWithScopes 是上面那条流程的唯一实现：额外 scope 来自全局授权
// 弹窗登记的缺失权限，普通登录传 nil 只申请默认范围。
func (a *app) startLoginWithScopes(scopes []string) {
	// 账号窗口的补授权挂在账号册上：多账号时它开内置浏览器的独立会话，而不是
	// 系统浏览器 —— 系统浏览器里登着的可能是另一个账号，授权码会落到别人头上。
	if a.book != nil && a.accountID != "" {
		a.book.reauthorizeAccount(a.accountID, scopes)
		return
	}
	// 一次只允许一次授权在等回调（回调只带 code 与 state）：这个账号窗口先
	// 认领，别的窗口 / 账号面板上正在等的授权让位。
	if a.book != nil {
		a.book.claimLogin(a.auth)
	}
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
	// 账号册：本机登录过的账号、每个账号一条令牌、进程级的共享依赖（代理、
	// 外观、图片缓存、离线缓存），以及多账号窗口的调度。它只读本机文件，
	// 先建好，后面所有窗口都从它这里开。
	b := newAccountBook("")
	// 先把代理规则接到进程内所有 HTTP 出口，再装更新插件：更新插件自己
	// 发请求（检查更新、下载更新包），不经过本应用的 Client。接好之后，
	// 手动代理 / 直连 / 跟随系统三种模式与「不走代理的地址」对它同样生效，
	// 设置窗口里改完代理立刻就能检查更新。
	installGlobalProxy(b.proxy)
	// 自动更新：更新包来自 github.com/vhxubo/t2-desktop 的 release，由
	// mygo build 用 mygo.json 里 updates.publicKey 对应的私钥签名。native
	// 更新窗口不需要 WebView，与本应用的原生 UI 一致：启动后检查一次，之后
	// 每天一次，也在设置窗口的「检查更新」被点击时立即检查。
	mygo.Use(updatenative.Plugin)
	b.updates = true
	// 先按保存的外观模式设置原生外观，让启动首帧、原生更新/通知窗口与
	// theme.json 一致；自绘界面仍由 view 每帧设置。
	mygo.Theme.SetSource(themeSource(b.theme.Mode()))
	// 深链回调先注册，再取单实例锁：浏览器跳回时系统拉起的是一个新进程，
	// 它把命令行转给第一个实例，转发可能早于下面几行执行。mygo 的
	// OnOpenURL 只在投递那一刻有监听者时才送达，注册晚了链接就被丢掉。
	mygo.App.OnOpenURL(b.handleDeepLink)
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
		b.schemeWarn = "系统未把 " + appIdentifier + " 关联到本应用（" + err.Error() + "）：授权后浏览器不会自动跳回，请复制地址栏里的完整链接粘贴到「账号」面板里。"
		log.Printf("运行时注册 %s 未成功（打包版由 mygo.json 的 urlSchemes 声明；仍可手动粘贴回调链接）：%v", appIdentifier, err)
	} else {
		log.Printf("回调协议已注册：%s", appIdentifier)
	}
	// 原生通知的点击只带回 id，由账号册查回它是哪个账号的消息、落到哪个
	// 窗口：帖子和评论在应用内打开，其余交给系统浏览器。注册在 Run 之前，
	// 因此连「点击通知才启动本进程」的那一次也能收到。
	mygo.App.OnNotificationClick(b.openNotice)
	// 站内读掉消息不会产生 notification_update，窗口回到前台时校准一次。
	mygo.App.OnDidBecomeActive(b.onBecameActive)
	// 退出可能来自托盘菜单、系统菜单或会话结束：先给所有窗口立这个标记，
	// 主窗口的关闭才不会被「收进托盘」拦下来，退不掉。
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { b.markQuitting() })
	mygo.App.WhenReady(func() {
		// 原生通知要等应用就绪：Windows 走托盘气泡，macOS 首次会询问授权。
		// 头less 测试不经过这里，notify 保持 nil，不会触碰系统通知。
		if mygo.NotificationsSupported() {
			b.notify = showNativeNotice
		}
		// 从这里开始才有主循环：后台结果可以投到 UI 线程。
		b.mainLoop = true
		// 内置浏览器授权要问一次系统 webview 在不在（Windows 的 WebView2
		// 运行时、macOS 的 WKWebView、Linux 的 WebKitGTK）。不在时授权退回
		// 系统浏览器，用户照旧能登录。
		b.webview = mygo.WebViewAvailable()
		if !b.webview {
			log.Printf("系统 webview 不可用：添加账号时改用系统浏览器授权")
		}
		// 图片缓存是进程级的一份，清理循环也只起一次。
		b.startImageCacheMaintenance()
		// 未激活账号的未读角标与令牌保活。
		b.startBackground()
		// 托盘图标：应用就绪后才有原生通知区域，见 tray.go。创建失败只记
		// 日志，窗口照常使用。
		b.startTray()
		// 每个账号一个主窗口；一个账号都没有时打开未登录窗口。
		b.restoreWindows()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal("应用启动失败：", err)
	}
}
