package main

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
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
	list ui.ListState
	// raw 是接口返回并已按 Key 去重的全部条目，屏蔽与否都留着：在资料卡上
	// 屏蔽 / 取消屏蔽后要立刻重算 items，不能重新发请求。
	raw []Item
	// items 是 raw 按当前名单过滤后的可见列表；foldRows、渲染与头像预取
	// 只读它。未开过滤的 tab 里 items 就是 raw。
	items []Item
	// hidden 是 raw 里被剔除的条数，只用于页头说明。
	hidden int
	// filterSkip 是连续「整页被过滤」的页数，用来限制自动续拉。
	filterSkip int
	page       int
	total      int
	totalPages int
	loaded     bool
	loading    bool
	// cacheNote 是列表首屏来自本地缓存时的页头说明；cachedAt 是那份缓存的
	// 写入时间，fromCache 表示当前列表还没有被网络结果覆盖。
	cacheNote string
	cachedAt  time.Time
	fromCache bool
	// refreshing 表示后台静默刷新第一页正在进行；refreshGen 是它的回包
	// 代次，只认最新一次。它与分页加载用的 gen 分开，静默刷新不会顶掉
	// 正在续拉的下一页。
	refreshing bool
	refreshGen uint64
	noMore     bool // 分页已到底（空页 / 无新增），用于无限滚动停止
	err        string
	// moreErr 是「续拉下一页」失败的原因：首屏已经有内容时，续拉失败不能
	// 把整页换成错误页，只在新内容该出现的位置提示并可重试。
	moreErr string
	// authWait 表示首屏失败是在等全局授权把缺失的权限补上。等待期间
	// ensureLoaded 不再重发同一个注定 403 的请求；重新登录或用户点「重试」
	// 会清掉它。
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
	// notifMu 保护上面两张表：真实窗口里它们只在 UI 线程读写，但无头测试
	// 没有窗口，publish 的落地回调就在后台 goroutine 里执行（标已读的收尾
	// 会在那里删这两张表）。不加锁会撞上并发 map 读写，直接把进程打崩。
	notifMu    sync.Mutex
	markingAll bool
	// 快捷回复：同一时刻只展开一条消息的输入框，草稿只属于那一条（换一条
	// 就清空）。posting 表示有一次回复在途，期间不再接受新的提交。都只在
	// UI 线程读写，网络结果经 publish 回来。
	quickReplyOpen    string
	quickReplyDraft   string
	quickReplyPosting bool
	quickReplyFocus   bool
	quickReplyField   ui.Handle

	// 首页节点筛选：只在 UI 线程读写。filterOpen 是筛选 chip 的浮层，
	// filterSearch 是浮层顶部的搜索词，filterRoot 是左列展开的父 slug。
	// homeFilterNode 是首页的节点筛选；通知页的 filter 是另一种筛选，
	// 两者都是 int/struct 不同语义，不能共用一个字段。
	homeFilterNode nodeFilter
	filterOpen     bool
	filterSearch   string
	filterRoot     string
}

// tabFold 存一页的分组结果与展开状态。
type tabFold struct {
	on bool // 这个 tab 开着聚合；只有它为真时才读 groups
	// groups 覆盖 items 的全部行并保持原顺序；start 是组首条在 items 里的下标。
	groups []commentGroup
	// open 记哪几组被展开了。键是组首条评论的评论 ID，不放下标：列表会定时
	// 刷新，新评论插到最前面时下标整体后移，用下标会把展开状态挪到别组头上。
	open map[string]bool
	// rows 是 foldRows 的缓存。列表视图每帧都会要一遍可见行，而 items 与
	// 展开状态只在数据到达 / 用户点开收起时才变；不缓存的话每帧都要把整份
	// 列表重新展开、重新分配一遍。items 的签名（长度 + 首尾 Key）兜住绕过
	// refold 直接改 items 的地方（例如测试与恢复路径）。
	rows      []foldRow
	rowsValid bool
	rowsLen   int
	rowsHead  string
	rowsTail  string
}

// invalidateRows 让 foldRows 的缓存在下一次渲染时重建。
func (f *tabFold) invalidateRows() {
	f.rows = nil
	f.rowsValid = false
}

// app holds only data and widget state. Never store ui.Element or *Context:
// background work publishes results through Window.Update.
type app struct {
	auth *AuthManager
	// desktop 是进程级单例（见 desktop.go）：共享依赖、系统事件与窗口调度
	// 都挂在它上面。无头测试可以不建它，这时登录与退出只用本窗口的会话。
	desktop  *desktop
	proxy    *proxyManager
	theme    *themeManager
	client   *Client
	win      *mygo.Window
	srv      ui.Services
	autoLoad bool
	// frameSvc 是现在正在渲染的那一帧所属窗口的重绘服务（见 windowView）。
	// 图片预取据此把「加载完成」只通知到真正显示它的窗口，而不是重建所有
	// 附属窗口。只在 UI 线程读写。
	frameSvc ui.Services

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
	// resetConfirmOpen 是「一键重置所有设置」二次确认气泡的开合。
	resetConfirmOpen bool
	// typographyConfirmOpen 是「恢复默认排版」二次确认气泡的开合。
	typographyConfirmOpen bool
	// commands 是这个窗口的命令注册表。定义随窗口注册（Run 闭包要绑到本
	// 窗口），绑定 / 收藏 / 最近使用的偏好来自进程级共享的 commands.json。
	commands *commandRegistry
	// shortcutRecordingID 非空表示设置页正在等用户按下组合键；
	// shortcutCapture / shortcutCaptureFocus 把焦点交给录制框，好收到原始按键；
	// shortcutConflict 是最近一次录制遇到的冲突说明。
	shortcutRecordingID  string
	shortcutCapture      ui.Handle
	shortcutCaptureFocus int
	shortcutConflict     string
	// bossKeyPrefs 是老板键的启用状态与系统热键，进程级一份（见 boss_key.go）。
	bossKeyPrefs *bossKeyPrefs
	// bossKeyRecording 表示设置页正在等用户按下老板键；bossKeyCapture /
	// bossKeyCaptureFocus 把焦点交给录制框；bossKeyConflict 是最近的冲突说明。
	bossKeyRecording    bool
	bossKeyCapture      ui.Handle
	bossKeyCaptureFocus int
	bossKeyConflict     string
	// bossKeyHidden 表示窗口正被老板键隐藏；bossKeyShown 记下藏起来之前可见
	// 的窗口，再按一次只恢复这些。都只在主线程读写。
	bossKeyHidden bool
	bossKeyShown  []*mygo.Window

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
	// 网络状态弹窗（见 netstatus.go / netstatus_ui.go）：请求失败确认离线后，
	// 状态机通知本窗口把 offlineDialogOpen 置位，由 dialogs 画出来；点「重新
	// 连接」走 retryNetwork 后台探测。netStatusUnwatch 是反注册函数。只在
	// UI 线程读写（回调经 publish 回到 UI 线程）。
	offlineDialogOpen bool
	offlineRechecking bool
	netStatusUnwatch  func()
	// cacheStatus 是设置窗口「图片缓存」一块的显示状态；统计在后台跑，
	// 结果回到主线程。
	cacheStatus cacheStatus
	// 诊断包导出（见 diagnostics.go）：includeNetLog 默认开，导出期间
	// exporting 置位来禁用按钮。pathHook 与 onExported 是测试钩子：无头环境
	// 没有原生保存对话框，导出完成后用它把结果同步回测试。只在 UI 线程读写。
	diagnosticsExporting     bool
	diagnosticsIncludeNetLog bool
	diagnosticsIncludePrefs  bool
	diagnosticsPathHook      func() (string, error)
	onDiagnosticsExported    func(path string, err error)
	// onSettingsOpen / onUpdateCheck / onCoinsOpen / onPandoraOpen /
	// onProfileOpen / onSignIn 是测试钩子：无窗口时用来确认按钮确实接上了
	// 动作。生产为 nil。
	onSettingsOpen func()
	onUpdateCheck  func()
	// onIdleAutoDownload 是测试钩子：无窗口时确认「闲时自动下载更新」接到了
	// 更新插件。生产为 nil。
	onIdleAutoDownload func(on bool)
	// onUpdateInterval / onUpdateRestart / onUpdateCancel 是测试钩子：无窗口时
	// 确认间隔切换、标题栏「重启更新」与「取消下载」按钮接上了动作。生产为 nil。
	onUpdateInterval func(hours int)
	onUpdateRestart  func()
	onUpdateCancel   func()
	onCoinsOpen      func()
	onPandoraOpen    func()
	onProfileOpen    func()
	onSignIn         func()
	onAboutOpen      func()

	tab     int
	lastTab int
	navSel  string // 左侧导航栏选中项的 id（"t"+序号），与 tab 保持同步
	// navWidth / navExpanded 是 Lobe DraggableSideNav 的持久状态：可拖拽调宽、
	// 可折叠成图标轨。默认折叠，宽度 0 让组件用它的展开默认值。
	navWidth    float32
	navExpanded bool
	tabs        []*tabState
	reqSeq      uint64
	// sidebarRefresh 把侧边栏切换后的重新请求排进防抖 + 节流队列，避免连续
	// 点击把配额耗在一闪而过的页面上；详见 sidebar_refresh.go。
	sidebarRefresh sidebarRefreshScheduler
	// sidebarPrefs 是首页到「节点」之前导航入口的显示与排序偏好，进程级一份
	// （见 sidebar_prefs.go），只在 UI 线程读写。
	sidebarPrefs *sidebarPrefsStore

	pageName    string // list / detail / login / drafts / watchlater / scores
	status      string
	loginMsg    string
	callbackURL string
	// 登录：点「登录」直接打开浏览器，loginPending 表示正在等系统跳回；
	// 只有失败或用户主动粘贴回调时才进入 login 页。
	loginPending bool
	// images 是全应用共用的图片缓存（见 image_cache.go）：登录账号、帖子
	// 作者、评论作者与通知操作者的头像，正文与评论里的表情、社区图标，
	// 以及帖子配图都由它统一管理，同一个地址只下载一次。下载完成前
	// userTag 先画按昵称生成的图案。
	images *imageCache
	// kv 是离线缓存（见 offline_store.go）：金币页的余额与资产曲线、阅读
	// 记录，以及每篇帖子缓存的正文。只读写本机文件，不发请求。
	kv *localKV
	// reading 是本机的阅读历史（最近打开的帖子）。启动后从本机缓存懒加载
	// 一次，与登录态无关。
	reading       []readingRecord
	readingLoaded bool
	readingList   ui.ListState
	// later 是本机的稍后再看（见 watch_later.go）；watchLaterPrefs 决定打开
	// 后移除还是保留。laterTab 是页面里的
	// 分段（0 稍后再看 / 1 浏览历史），lastLaterTab 用来接住分段切换并复位
	// 两个列表的滚动位置，laterList 是列表状态。页面见 watch_later_ui.go，
	// 和首页、草稿箱一样内嵌在主窗口，都只在主线程读写。
	later           []watchLaterRecord
	laterLoaded     bool
	laterList       ui.ListState
	laterTab        int
	lastLaterTab    int
	watchLaterPrefs *watchLaterPrefsStore
	// readAt / commentReadAt 是本机读过的帖子与评论（id → 首次打开时间），
	// 列表据此把读过的行置灰（见 post_read.go）。两者和阅读记录一样懒加载，
	// 存在同一个 read:local 缓存里，只记本机，不向服务端同步。只在 UI 线程
	// 读写。
	readAt        map[string]time.Time
	commentReadAt map[string]time.Time
	readLoaded    bool
	// toasts 是等着显示的窗口内提示（见 toasts.go）：操作结果先排队，等到
	// 对应窗口下一次重建时再交给 Lobe 的 feedback.ShowToast。可能由后台 goroutine（经 publish）
	// 写入，也由渲染帧读取，所以用 toastMu 保护，避免竞态。
	toasts  []toastNote
	toastMu sync.Mutex
	// schemeWarn 是「系统没有把回调协议关联到本应用」的说明，仅在这种情况下
	// 非空：它决定了浏览器授权后能不能自动跳回，得让用户在登录页看见。
	// 生产里实时读 desktop；这个字段只给无头测试注入（见 schemeWarning）。
	schemeWarn string
	// authGate 是全局授权弹窗：任何缺少权限的调用都收敛到这里，授权成功后
	// 重跑被中断的调用。
	authGate     authGate
	lastAuthHost toastTarget
	// contentFilter 是当前账号的内容屏蔽名单与运行状态（见 content_filter.go）。
	// 只在当前会话里保留一份，不放进 appDeps：名单不能进程级共享。
	contentFilter *contentFilterState

	// coins 是金币页的聚合状态：余额、今日明细、流水、统计与排行各自独立
	// 加载，某个高级接口未开通时只降级那一块。账号菜单里的金币数也读这里的
	// balance，两处显示始终是同一个数字。
	coins *coinState
	// coinWin 是金币窗口：金币页从「我的」窗口里独立出来，成为一个独立的
	// mygo 窗口。入口收在标题栏「我的」的账号菜单气泡里（见 profile_ui.go）。
	// coinShown 表示窗口此刻是否在屏幕上，金币数据此高亮。都只在主线程读写。
	coinWin   *mygo.Window
	coinShown bool
	// coinTagSeq 与 coinTagLoading 让过期的余额请求结果被丢弃，coinTagPending
	// 记下刷新期间又收到的请求，等这次请求回来再补一次。金币余额只在展开账号
	// 菜单 popover 时请求。都只在主线程读写。
	coinTagSeq     uint64
	coinTagLoading bool
	coinTagPending bool

	// pandora 是潘多拉抽卡弹窗的状态：今日盲盒、两次抽奖结果与弹窗开合。
	// 入口收在标题栏「我的」的账号菜单气泡里（见 profile_ui.go），弹窗画在
	// 主窗口的 dialogs 里；只在主线程读写。
	pandora *pandoraState

	// commentCost 是评论框上方那条「发评金币与徽章」状态栏的数据：我的徽章
	// （含 attributes）、佩戴状态与正在切换的那一枚（见 comment_cost.go）。
	// 它属于当前会话，与具体帖子无关，所以挂在这里而不是 detailState 上。
	commentCost *commentCostState
	// commentCostPrefs 是评论费用相关的两个开关（见 comment_cost_prefs.go）：
	// 金币消耗与徽章切换状态栏、发送按钮上的消耗金币数。进程级共享，
	// 所有已开窗口的评论框据此决定要不要画状态栏与数字。只在 UI 线程读写。
	commentCostPrefs *commentCostPrefsStore
	// imagePrefs 是图片显示的两项偏好（见 image_prefs.go）：默认不加载图片时
	// 正文与评论里的配图先画占位、点一下才下载；动图自动播放决定 gif / apng
	// 在正文与预览里是动图还是首帧。进程级共享，只在本窗口的界面里读。
	imagePrefs *imagePrefsStore
	// typePrefs 是阅读排版的五项偏好（见 typography.go）：正文与正文标题的字号、
	// 评论字号、行距、列表页宽与列表密度。进程级共享，只在 UI 线程读写。
	typePrefs *typographyStore
	// updateMirror 是更新包下载加速入口的选择（见 update_mirror.go）：默认
	// 不选、走「网络代理」里配置的代理。进程级共享，只在本窗口的界面里读。
	updateMirror *updateMirrorStore
	// updateChannel 是「稳定渠道」的选择（见 update_channel.go）：默认关闭，
	// 接收全部版本；打开后只收正式版。进程级共享，设置窗口据此画开关。
	updateChannel *updateChannelStore
	// updateIdlePrefs 是「闲时自动下载更新」的开关（见 update_idle_prefs.go）：
	// 默认开启。进程级共享，接收测试版时强制开启。
	updateIdlePrefs *updateIdlePrefsStore
	// updateIntervalPrefs 是后台检查更新间隔，进程级共享，默认 6 小时。
	updateIntervalPrefs *updateIntervalPrefsStore
	// autoSignPrefs 是「打开应用时自动签到」的开关，进程级共享。
	autoSignPrefs *autoSignPrefsStore
	// autoSignStarted 保证本窗口只尝试一次自动签到；窗口关掉重开不会重复。
	// autoSignNotice 是签到成功后的顶部 Alert 提示，nil 表示没有要显示的提示。
	autoSignStarted bool
	autoSignNotice  *autoSignNotice
	// revealedImages 是「默认不加载图片」模式下已经点开过的配图地址：点过一次
	// 之后这张图不再退回占位。只在 UI 线程读写。
	revealedImages map[string]bool

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
	// （commentTextStyle），让评论区比正文紧凑一号。浅色主题另有一份
	// commentMdLight，把引用 / 代码 / 表格这些辅助块铺成 Lobe 派生的淡灰；
	// 列表行另有一对 shareSelection 版本，让正文点击落到整行。四份都在启动
	// 时建好，渲染时按窗口外观二选一，不复制 Options（它带 sync.Map）。
	commentMd          *markdown.Options
	commentMdLight     *markdown.Options
	commentMdList      *markdown.Options
	commentMdListLight *markdown.Options
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
	// sectionUnread 是未读数接口带回来的分区计数（首页 / 社区 / 关注 / 徽章 /
	// 追更内容），给侧边栏对应浏览页挂红点。与 unread 同一次请求拿到，
	// 只在主线程读写，见 section_unread.go。
	sectionUnread notificationUnreadCounts
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
	// profileMenuOpen 是标题栏「我的」账号气泡的开合：点账号标识弹出，里面
	// 收着金币、潘多拉与「我的」三个入口。只在主线程读写。
	profileMenuOpen bool
	// composeWin 是「发帖」窗口：入口是主页右下角的悬浮按钮（见 composeFab）。
	// compose 是它的草稿与各组件状态，界面与请求在 compose.go / compose_ui.go。
	// 都只在主线程读写；关窗口后草稿保留，退出登录时丢弃。
	composeWin *mygo.Window
	compose    *composeState
	// 本地草稿箱（见 draft.go / draft_ui.go）：drafts 是本机唯一的草稿集合，
	// draftDir 是配置目录（与 appDeps.dir 一致，固定存在 drafts/local.json）。
	// draftsList / draftsClearOpen 是草稿页的列表状态与清空确认弹窗；
	// draftPending / draftDirtySince / draftTimer 是 800ms 防抖 + 5s 上限的落盘节奏。
	// 都只在主线程读写；定时器回调经 publish 回来。
	drafts          *draftStore
	draftDir        string
	draftsList      ui.ListState // 发帖与动态那组
	draftsReplyList ui.ListState // 回复那组，与上一组分开，免得滚动状态串台
	draftsClearOpen bool
	draftPending    bool
	draftDirtySince time.Time
	draftTimer      *time.Timer
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
	// nodeEditWin 是「修改节点」窗口（见 node_edit_window.go），与编辑正文
	// 窗口完全独立：两个窗口各自计时、各自成功后关闭。nodeEdit 是这一次
	// 选择的节点与幂等键，窗口关掉就丢弃；只在 UI 线程读写。
	nodeEditWin *mygo.Window
	nodeEdit    *nodeEditState
	// hosts 是图库（图床）配置：几个内置预设加用户手工添加的项，落盘在
	// localKV 的 imagehosts:v1。hostActiveLabel 是当前激活项的名字，编辑器
	// 的工具面板直接把它绑到下拉框上。都只在 UI 线程读写，见 image_hosts.go。
	hosts           imageHostConfig
	hostActiveLabel string
	// proChecking 表示正在读当前账号的 Pro 状态 / 守护者角色（官方图床权限）。
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
	// nodeCat 是首页筛选、配置弹窗与发帖窗口共用的节点目录（见
	// node_catalog.go）：单飞加载、共享缓存，避免多处并发拉同一份节点表。
	nodeCat nodeCatalog
	// nodeShortcuts / nodeEditor 是首页节点筛选的侧边栏快捷入口（见
	// node_shortcuts.go）：每个入口自带排序偏好，随快捷入口一起落盘；
	// editor 是配置弹窗的草稿，只在主线程读写。
	nodeShortcuts []nodeRef
	nodeEditor    nodeEditor
	nodeRailOpen  bool
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
	// searchWin 是搜索窗口：入口是标题栏搜索框的命令面板。search 是它的输入、
	// 结果与防抖状态，界面与请求在 search.go / search_ui.go。都只在主线程读写。
	// 标题栏输入框与它共用 search.q：点输入框默认是命令面板（见 command_palette.go），
	// 切到「搜索」模式才把 q 带进搜索窗口。
	searchWin *mygo.Window
	search    *searchState
	// palette 是标题栏搜索框的命令面板状态（开合与高亮项）。
	palette paletteState
	// trayReady 只在无头测试里手动模拟托盘可用性。生产里托盘是进程级资源，
	// 由 desktop 在应用就绪后才创建（晚于 app 构建），窗口必须实时查 desktop
	// 的托盘状态，不能用构建时的快照（见 window_mode.go 的 trayIsReady）。
	trayReady bool
	// closeMode 是「关闭主窗口时」的偏好（见 window_mode.go），落盘在
	// window.json。只在主线程读写。
	closeMode *closeModeStore
	// disclaimer 是首次启动声明的进程级状态（见 disclaimer.go）：全应用一份，
	// 只弹一次；claim 成功后把 disclaimerOpen 置位，由 dialogs 画出来。
	disclaimer     *disclaimerStore
	disclaimerOpen bool
	// aboutDisclaimerOpen 是「关于」窗口里重看声明弹窗的开合（见
	// disclaimer_ui.go）。与 disclaimerOpen 分开：重看只读、不写 seen，
	// 也不会让主窗口跟着画一遍。只在主线程读写。
	aboutDisclaimerOpen bool
	// closePromptOpen / closeRemember 是关闭提示弹窗的状态：是否正在询问
	// 「收进托盘 / 退出应用」，以及这次要不要记住选择。只在主线程读写。
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
	// notifPollTimer 是关掉实时长连接后的兜底轮询：定时刷新未读数、列表与
	// 桌面通知。开着 SSE 时始终为 nil，两者不会同时跑。
	notifPollTimer    *time.Timer
	notificationPrefs *notificationPrefsStore // 通知偏好，进程级共享并落盘
	// notifyKeywordDraft 是设置窗口「提醒关键词」输入框的草稿，点保存才落盘。
	notifyKeywordDraft string
	// notifyQuietFromDraft / notifyQuietToDraft 是免打扰时段输入框的草稿。
	notifyQuietFromDraft string
	notifyQuietToDraft   string
	// noticeTargets 是「原生通知 id → 目标地址」的映射：系统通知的点击回调
	// 只带 id，靠它把点击落回对应的帖子 / 评论，映射丢了就回到消息中心。
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
	// onAuthPrompt 是全局授权弹窗的测试钩子：弹窗打开时触发。生产为 nil。
	onAuthPrompt func()
	// onPostPublished 是发帖完成（成功或失败）后的测试钩子：headless 测试
	// 没有窗口，publish 在请求协程里直接执行，靠它把结果同步回测试。
	onPostPublished func(created createdTopic, err error)
	// onSearchDone 是搜索完成（成功或失败）后的测试钩子，原因同上。
	onSearchDone func(query string, err error)
	// onInteractDone 是收藏 / 表态完成（成功或失败）后的测试钩子，原因同上。
	onInteractDone func(err error)
	// onNodesLoaded 是节点表刷新完成（成功或失败）后的测试钩子，原因同上。
	onNodesLoaded func(err error)

	// scores 是评分榜页面的状态（见 scores_ui.go），和首页、草稿箱一样
	// 内嵌在主窗口，不单独占一扇窗口。只在主线程读写。
	scores *scoresState
	// zoomPrefs 是整页缩放偏好，进程级一份（见 zoom_prefs.go）。所有窗口
	// 的 view 每帧从它取缩放，设置页与命令面板改的也是这一份。
	zoomPrefs *zoomPrefsStore
}

// tabID 把 tab 序号映射成导航项 id；导航轨的选中态由 selectTab 直接维护。
func tabID(i int) string { return "t" + strconv.Itoa(i) }

// appDeps 是进程级的共享依赖：一个进程一份，所有窗口共用。
//
// 代理、外观、图片缓存与离线缓存都必须是全应用一份：各窗口建一份会让代理
// 切换只对一扇窗口生效，而离线缓存（cache/offline.json）被各自整份写回时
// 后写的会覆盖前写的阅读记录。
type appDeps struct {
	dir        string
	proxy      *proxyManager
	theme      *themeManager
	images     *imageCache
	kv         *localKV
	closeMode  *closeModeStore
	disclaimer *disclaimerStore
	// commentCostPrefs 是评论费用的两个开关，进程级一份（见 comment_cost_prefs.go）。
	commentCostPrefs *commentCostPrefsStore
	// watchLaterPrefs 是稍后再看的打开行为（默认移除 / 保留并区分已读），
	// 进程级一份（见 watch_later_prefs.go）。
	watchLaterPrefs *watchLaterPrefsStore
	// imagePrefs 是图片显示的两项偏好：默认不加载图片、动图自动播放，
	// 进程级一份（见 image_prefs.go）。
	imagePrefs *imagePrefsStore
	// typePrefs 是阅读排版的五项偏好（见 typography.go），进程级一份。
	typePrefs *typographyStore
	// updateMirror 是更新包下载加速入口的选择，进程级一份（见 update_mirror.go）。
	updateMirror *updateMirrorStore
	// updateChannel 是「稳定渠道」的选择，进程级一份（见 update_channel.go）。
	updateChannel *updateChannelStore
	// updateIdlePrefs 是「闲时自动下载更新」的开关，进程级一份（见
	// update_idle_prefs.go），默认开启。
	updateIdlePrefs *updateIdlePrefsStore
	// updateIntervalPrefs 是后台检查更新间隔，进程级一份，默认 6 小时。
	updateIntervalPrefs *updateIntervalPrefsStore
	// notificationPrefs 是通知偏好，进程级一份（见 notification_prefs.go）。
	notificationPrefs *notificationPrefsStore
	// autoSignPrefs 是「打开应用时自动签到」的开关，进程级一份（见
	// auto_sign_prefs.go），默认开启。
	autoSignPrefs *autoSignPrefsStore
	// sidebarPrefs 是首页到「节点」之前导航入口的显示与排序偏好，进程级一份
	// （见 sidebar_prefs.go）。
	sidebarPrefs *sidebarPrefsStore
	// commands 是命令快捷键 / 收藏 / 最近使用的偏好，进程级一份。
	commands *commandPrefs
	// zoomPrefs 是界面缩放偏好，进程级一份（见 zoom_prefs.go）。
	zoomPrefs *zoomPrefsStore
	// bossKeyPrefs 是老板键的启用状态与系统热键，进程级一份（见 boss_key_prefs.go）。
	bossKeyPrefs *bossKeyPrefs
	desktop      *desktop
}

// newAppDeps 造一份独立的共享依赖：无头测试时每个 app 自己
// 一份，互不干扰。
func newAppDeps(dir string) appDeps {
	if dir == "" {
		dir = configDir()
	}
	return appDeps{
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
		autoSignPrefs:       newAutoSignPrefsStore(dir),
		sidebarPrefs:        newSidebarPrefsStore(dir),
		commands:            newCommandPrefs(dir),
		zoomPrefs:           newZoomPrefsStore(dir),
		bossKeyPrefs:        newBossKeyPrefs(dir),
	}
}

// deps 是进程级那份共享依赖：所有窗口都从这里建，不各自造一份。
func (b *desktop) deps() appDeps {
	return appDeps{
		dir:                 b.dir,
		proxy:               b.proxy,
		theme:               b.theme,
		images:              b.images,
		kv:                  b.kv,
		closeMode:           b.closeMode,
		disclaimer:          b.disclaimer,
		commentCostPrefs:    b.commentCostPrefs,
		notificationPrefs:   b.notificationPrefs,
		watchLaterPrefs:     b.watchLaterPrefs,
		imagePrefs:          b.imagePrefs,
		typePrefs:           b.typePrefs,
		updateMirror:        b.updateMirror,
		updateChannel:       b.updateChannel,
		updateIdlePrefs:     b.updateIdlePrefs,
		updateIntervalPrefs: b.updateIntervalPrefs,
		autoSignPrefs:       b.autoSignPrefs,
		sidebarPrefs:        b.sidebarPrefs,
		commands:            b.commands,
		zoomPrefs:           b.zoomPrefs,
		bossKeyPrefs:        b.bossKeyPrefs,
		desktop:             b,
	}
}

// newApp 建一个独立的界面状态（无头测试用；没有进程级单例时自己建一份会话）。
func newApp(dir string) *app { return newAppWithDeps(newAppDeps(dir), nil) }

// newMainApp 建主窗口的界面状态：会话与共享依赖都来自进程级单例。
func newMainApp(d *desktop) *app {
	a := newAppWithDeps(d.deps(), d.auth)
	a.autoLoad = true
	// 冷启动时已有会话也要读一次名单：缓存已撑住首帧，这里拉最新一份。
	a.ensureContentFilter(false)
	return a
}

// schemeWarning 返回回调协议的注册结果。注册晚于 app 构建，所以有进程级
// 单例时必须实时查；无头测试没有单例，用 app 上注入的字段。
func (a *app) schemeWarning() string {
	if a == nil {
		return ""
	}
	if a.desktop != nil {
		return a.desktop.schemeWarning()
	}
	return a.schemeWarn
}

// updatesEnabled 报告更新插件是否已安装。这个标记在 mygo.Use 之后才由 main
// 立起来，晚于 newMainApp，所以窗口不能构造时快照一次就一直用：否则设置页
// 的「检查更新」会以为插件没装而直接返回，点了没反应。托盘同理，都实时查。
func (a *app) updatesEnabled() bool {
	return a != nil && a.desktop != nil && a.desktop.updates
}

func newAppWithDeps(d appDeps, auth *AuthManager) *app {
	if auth == nil {
		auth = newAuthManagerAt(d.dir, d.proxy)
	}
	a := &app{
		auth:                     auth,
		desktop:                  d.desktop,
		proxy:                    d.proxy,
		theme:                    d.theme,
		client:                   &Client{Auth: auth, Proxy: d.proxy},
		images:                   d.images,
		kv:                       d.kv,
		closeMode:                d.closeMode,
		disclaimer:               d.disclaimer,
		commentCostPrefs:         d.commentCostPrefs,
		notificationPrefs:        d.notificationPrefs,
		watchLaterPrefs:          d.watchLaterPrefs,
		imagePrefs:               d.imagePrefs,
		typePrefs:                d.typePrefs,
		updateMirror:             d.updateMirror,
		updateChannel:            d.updateChannel,
		updateIdlePrefs:          d.updateIdlePrefs,
		updateIntervalPrefs:      d.updateIntervalPrefs,
		autoSignPrefs:            d.autoSignPrefs,
		sidebarPrefs:             d.sidebarPrefs,
		commands:                 newCommandRegistry(d.commands),
		zoomPrefs:                d.zoomPrefs,
		bossKeyPrefs:             d.bossKeyPrefs,
		draftDir:                 d.dir,
		tabs:                     make([]*tabState, len(tabSpecs)),
		lastTab:                  -1,
		navSel:                   tabID(0),
		pageName:                 "list",
		coins:                    newCoinState(),
		pandora:                  newPandoraState(),
		profile:                  newProfileState(),
		commentCost:              newCommentCostState(),
		diagnosticsIncludeNetLog: true,
	}
	// HTTP 出口的全局授权拦截：任何 insufficient_scope 都会回到这里登记弹窗，
	// 不需要每个调用点自己接。必须在 a 建好之后再挂，回调要引用它。
	a.client.onAuthError = a.handleGlobalAuthError
	// 设置窗口的关键词 / 时段草稿从已保存偏好起手；无头测试直接渲染设置页时也一致。
	a.syncNotifyDrafts()
	// 草稿启动时就读进内存，侧边栏角标与草稿页首帧就是对的（文件不存在或
	// 损坏都按空草稿处理）。
	a.drafts = newDraftStore(d.dir)
	if d.desktop != nil {
		// 退出动作建单例时就有。回调协议注册、原生通知、托盘和更新插件都
		// 晚于 app 构建才就绪，窗口必须实时查 desktop（见 schemeWarning /
		// nativeNotifier / trayIsReady / updatesEnabled），不能在这里拍快照。
		a.quitApp = d.desktop.quitProgram
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
	// 内容屏蔽名单也先摆缓存：有缓存时首帧就能按名单过滤，后台再拉最新一份。
	a.contentFilter = newContentFilterState()
	a.loadContentFilterCache()
	a.loadCoinCache()
	a.loadReadingHistory()
	a.loadPostReads()
	a.loadWatchLater()
	// 图库配置也要在首帧前就位：编辑器工具面板打开时不能还是空列表。
	a.loadImageHosts()
	for i := range a.tabs {
		a.tabs[i] = &tabState{}
	}
	a.loadNodePrefs()
	a.bindAuth()
	a.bindNetworkStatus()
	a.rebuildMarkdownViews()
	a.viewer = &imageViewer{}
	a.registerBuiltinCommands()
	return a
}

// bindAuth 把会话变化接回本窗口。窗口开着时由窗口处理：它要重画、要弹授权；
// 进程级单例只从 onAuthChanged 里同步昵称与头像（见 noteAuthChange）。
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

// bindNetworkStatus 把进程级的网络状态机接回本窗口：离线/上线翻转时置位弹窗
// 状态并解冻界面。回调来自网络协程，统一经 publish 挪回 UI 线程。
func (a *app) bindNetworkStatus() {
	st := a.proxy.netStatus()
	if st == nil {
		return
	}
	a.netStatusUnwatch = st.addListener(func(offline bool) {
		a.publish(func() {
			if offline {
				a.offlineDialogOpen = true
			} else {
				a.offlineDialogOpen = false
				a.toast(toastMain, "网络已恢复")
			}
			a.redrawNetLogWindow()
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
	a.proChecking = false
	if a.client != nil {
		a.client.Auth = auth
	}
	a.bindAuth()
	a.onAuthChanged(nil)
}

// publish marshals a state change onto the UI thread, then rebuilds every
// window that shows the same state. In headless tests there is no window, so
// the change applies directly on the calling goroutine: anything it writes
// that a frame may read concurrently (maps especially) needs its own lock.
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
		a.redrawComposeWindow()
		a.redrawReplyWindow()
		a.redrawEditWindow()
		a.redrawNodeEditWindow()
		a.redrawUserCardWindow()
		a.redrawSearchWindow()
		a.redrawFollowsWindow()
		a.redrawViewerWindow()
		for _, h := range a.detailWindows {
			h.invalidate()
		}
	})
}

// rebuildMarkdownViews 按当前排版偏好重建五套 Markdown 选项。Options 带
// sync.Map，不能复制也不能原地改：这里只整体替换指针。只在 UI 线程调用。
func (a *app) rebuildMarkdownViews() {
	post, comment, bio := a.mdStyles()
	a.md = newMarkdownOptions(a, post, false, false)
	a.commentMd = a.newCommentMarkdownOptions(comment, false, false)
	a.commentMdLight = a.newCommentMarkdownOptions(comment, true, false)
	a.commentMdList = a.newCommentMarkdownOptions(comment, false, true)
	a.commentMdListLight = a.newCommentMarkdownOptions(comment, true, true)
	a.bioMd = newMarkdownOptions(a, bio, false, false)
}

// invalidateReadingWindows 让本窗口所有会显示阅读内容或分段选项的窗口
// 下一帧重画。不改状态、不重建页面；只在 UI 线程调用。
func (a *app) invalidateReadingWindows() {
	if a == nil {
		return
	}
	if a.win != nil {
		a.win.Invalidate()
	}
	for _, h := range a.detailWindows {
		h.invalidate()
	}
	for _, win := range []*mygo.Window{
		a.notifWin, a.settingsWin, a.profileWin, a.userCardWin, a.searchWin, a.followsWin,
	} {
		if win != nil {
			win.Invalidate()
		}
	}
}

// applyTypographyChange 把新排版推给当前窗口：Markdown 选项绑在 app 上，
// 必须整体重建，然后重画所有读到它的窗口。只在 UI 线程调用。
func (a *app) applyTypographyChange() {
	a.rebuildMarkdownViews()
	a.invalidateReadingWindows()
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
	spec := a.specFor(idx)
	a.loadSpec(owner, st, spec, page,
		func() { a.loadFor(owner, idx, page+1) })
}

// loadSpec 是通用的列表加载：把 spec 的第 page 页拉回来填进 st。「我的」
// 窗口里的四类内容也走这条路，所以它按 spec 而不是按侧边栏下标工作。owner
// 是发起这次加载的窗口；next 是整页都被屏蔽时自动续拉下一页的回调（没有则
// 为 nil）。授权成功后应用不自动重拉，由用户自己刷新或重新打开页面。
func (a *app) loadSpec(owner toastTarget, st *tabState, spec tabSpec, page int, next func()) {
	if st == nil || page < 1 {
		return
	}
	// 兼容直接给 items 塞数据的旧状态（无头测试、本地恢复）：续拉时把它当
	// 成原始行，去重与过滤都从它派生，不会把已有行丢掉。
	if page > 1 && len(st.raw) == 0 && len(st.items) > 0 {
		st.raw = append([]Item(nil), st.items...)
	}
	if st.loading {
		return
	}
	if page == 1 {
		a.preloadFeedCache(st, spec)
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
				if a.handleAuthErrorFor(owner, err) {
					st.authWait = true
				}
				if page == 1 {
					// 首屏失败但列表来自本地缓存且是网络问题：保留内容，
					// 页头说明这是缓存，而不是整页换成错误。
					if st.fromCache && feedOfflineError(err) {
						a.markFeedOffline(st)
					} else {
						st.err = friendlyError(err)
						st.raw = nil
						st.items = nil
						st.hidden = 0
						st.filterSkip = 0
						st.fold.reset()
						a.clearFeedCacheState(st)
					}
				} else {
					// 已有内容时续拉失败：保留当前列表，在列表底部提示。
					st.moreErr = friendlyError(err)
				}
				return
			}
			st.moreErr = ""
			// raw 留着全部原始行：屏蔽 / 取消后按它重算 items，不重新请求。
			// 去重也以 raw 为准，重复页不会因为「过滤后没新增」被误判到底。
			rawAdded := 0
			if page == 1 {
				st.raw = append([]Item(nil), res.Items...)
				st.fold.reset()
				st.list.ScrollTo(0, ui.Start)
				st.noMore = len(res.Items) == 0
				st.filterSkip = 0
				rawAdded = len(st.raw)
			} else {
				seen := make(map[string]bool, len(st.raw))
				for _, it := range st.raw {
					seen[it.Key] = true
				}
				for _, it := range res.Items {
					if !seen[it.Key] {
						st.raw = append(st.raw, it)
						seen[it.Key] = true
						rawAdded++
					}
				}
				// 服务端返回空页或没有新增条目：标记到底，避免无限加载。
				// 必须用原始响应判断：整页都被屏蔽时过滤后的新增是 0，
				// 按它判断会把后面的页永远挡在「到底」之外。
				st.noMore = rawAdded == 0
			}
			st.total = res.Total
			st.totalPages = res.TotalPages
			st.page = page
			st.loaded = true
			st.items, st.hidden = a.filterItems(spec, st.raw)
			// 分组按整份可见 items 重算：第 2 页正好接上第 1 页末尾的同一个人时，
			// 尾部那一组要跟着变长，不能因为分两次到达就断成两组。
			st.refold(spec)
			if page == 1 {
				// 首屏成功了：缓存标记清掉，并把这份首屏写回本地。
				a.clearFeedCacheState(st)
				a.saveCachedFeed(spec, st.raw, st.total, st.totalPages)
			}
			// 只预取本次新增且可见的条目：被屏蔽作者的头像不该被下载。
			if rawAdded > 0 {
				visible := make(map[string]bool, len(st.items))
				for _, it := range st.items {
					visible[it.Key] = true
				}
				fresh := make([]Item, 0, rawAdded)
				for _, it := range res.Items {
					if visible[it.Key] {
						fresh = append(fresh, it)
					}
				}
				a.prefetchItemAvatars(fresh)
			}
			// 整页都被过滤掉时自动续拉下一页，最多连续跳过 3 页，免得为了
			// 找一条内容把配额打光。续拉失败照旧走 moreErr，不影响已有列表。
			if spec.FilterBlocked && len(st.items) == 0 && rawAdded > 0 &&
				st.filterSkip < contentFilterSkipPages && next != nil &&
				(st.totalPages == 0 || page < st.totalPages) {
				st.filterSkip++
				next()
			}
		})
	}()
}

// refold 按当前 items 重算聚合分组。只对开着 FoldByAuthor 的端点做，其余
// tab 留一组「每行自成一组」的空结果。
func (st *tabState) refold(spec tabSpec) {
	st.fold.on = spec.FoldByAuthor
	st.fold.invalidateRows()
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
	f.invalidateRows()
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
	// 展开 / 收起改变可见行数，下一帧必须重新展开。
	st.fold.invalidateRows()
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
	if st.fold.rowsValid && st.fold.rowsLen == len(st.items) && st.fold.rowsHead == itemsHeadKey(st.items) &&
		st.fold.rowsTail == itemsTailKey(st.items) {
		return st.fold.rows
	}
	rows := st.buildFoldRows()
	st.fold.rows = rows
	st.fold.rowsValid = true
	st.fold.rowsLen = len(st.items)
	st.fold.rowsHead = itemsHeadKey(st.items)
	st.fold.rowsTail = itemsTailKey(st.items)
	return rows
}

// itemsHeadKey / itemsTailKey 是 items 缓存签名用的首尾 Key。
func itemsHeadKey(items []Item) string {
	if len(items) == 0 {
		return ""
	}
	return items[0].Key
}

func itemsTailKey(items []Item) string {
	if len(items) == 0 {
		return ""
	}
	return items[len(items)-1].Key
}

// buildFoldRows 从 items 与分组状态重新展开可见行。只有 foldRows 在缓存
// 失效时调用。
func (st *tabState) buildFoldRows() []foldRow {
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
	spec := a.specFor(a.tab)
	st := a.tabs[a.tab]
	// 名单未落定前先别露出会被屏蔽的帖子；最多等 5 秒，超时按无名单放行。
	// 等待期间也确保那次 GET 已经发出（后台登录流程可能还没走到）。
	if spec.FilterBlocked && !a.contentFilter.ready(a.auth.loggedIn()) {
		a.ensureContentFilter(false)
		return
	}
	if st.loading || st.loaded || st.authWait {
		return
	}
	// 侧边栏切换已经排了重新请求：防抖 / 节流到期前让路，别让每帧兜底
	// 抢先把这一页的请求发出去，否则连点多个页时防抖形同虚设。
	if a.sidebarRefresh.hasPending() {
		return
	}
	// 被限流后不要每帧重发：闸门在退避期内会拒绝所有 API 请求，这里连
	// 失败的本地回调也不必再排一遍，页面上的提示保留第一次的秒数。
	if a.client != nil && a.client.rateGate().remaining() > 0 {
		return
	}
	// 网络未连接时状态机会弹窗并暂停全应用重试，这里也别每帧发注定失败的
	// 请求（transport 仍会兜底拦下，但连失败的本地回调都不必排）。
	if st := a.proxy.netStatus(); st != nil && st.isOffline() {
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
	st.raw = nil
	st.items = nil
	st.hidden = 0
	st.filterSkip = 0
	st.fold.reset()
	st.err = ""
	st.moreErr = ""
	st.noMore = false
	a.clearFeedCacheState(st)
	st.list.ScrollTo(0, ui.Start)
	a.load(a.tab, 1)
}

// refreshTab 是用户主动刷新（点当前页、失败重试）：重新拉第一页，成功后
// 整份列表换成最新内容并回到顶部。切页走 refreshTabSilently，不会把已经
// 读到第 50 条的位置丢掉。
//
// 请求期间先留着旧内容，不先清空列表，不会闪一下空白。正在加载、正在等
// 授权时不动，免得两次请求抢同一份结果。
func (a *app) refreshTab(idx int) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	st := a.tabs[idx]
	if st == nil || st.loading || st.refreshing || st.authWait {
		return
	}
	if spec := a.specFor(idx); spec.NeedsAuth && !a.auth.loggedIn() {
		// 需要登录的页交给选中处理去弹授权，这里不发注定失败的请求。
		return
	}
	// 打开分区页就把对应角标标成已读：角标说明的是上次离开之后新增了多少，
	// 看完这一页它就该消失（动态见 follows.go，首页 / 社区见 section_unread.go）。
	a.markTabRead(idx)
	st.err = ""
	st.moreErr = ""
	a.load(idx, 1)
}

// refreshTabSilently 是切到某一页时的刷新：已经加载过的页在后台合并新的
// 第一页，不重置 page、不清掉第 2 页以后的内容，也不把滚动位置弹回顶部。
// 第一次进入（尚未加载）仍按首屏处理。
func (a *app) refreshTabSilently(idx int) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	st := a.tabs[idx]
	if st == nil || st.loading || st.refreshing || st.authWait {
		return
	}
	if spec := a.specFor(idx); spec.NeedsAuth && !a.auth.loggedIn() {
		return
	}
	a.markTabRead(idx)
	st.err = ""
	st.moreErr = ""
	if !st.loaded {
		a.load(idx, 1)
		return
	}
	a.silentRefreshTab(toastMain, idx)
}

// silentRefreshTab 在后台重拉第 1 页，并把新头部合并进现有列表。它不清空
// items、不重置 page、也不调用 ScrollTo：切走再切回来时，用户读到的位置与
// 翻过的页都还在。合并只动第一页覆盖到的行，第 2 页以后的内容原样保留；
// ListState 按条目的 Key 对齐，滚动位置跟着原来那条内容走。
func (a *app) silentRefreshTab(owner toastTarget, idx int) {
	if a.client == nil {
		return
	}
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	st := a.tabs[idx]
	spec := a.specFor(idx)
	if st == nil || st.loading || st.refreshing || st.authWait {
		return
	}
	if spec.NeedsAuth && !a.auth.loggedIn() {
		return
	}
	// 被限流、断网时连静默请求也别发：界面上的列表保持上次内容。
	if a.client.rateGate().remaining() > 0 {
		return
	}
	if net := a.proxy.netStatus(); net != nil && net.isOffline() {
		return
	}
	st.refreshing = true
	a.reqSeq++
	gen := a.reqSeq
	st.refreshGen = gen
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetch(ctx, spec, 1)
		a.publish(func() {
			if st.refreshGen != gen {
				return
			}
			st.refreshing = false
			if err != nil {
				// 失败不打扰正在读列表的用户：旧列表继续显示。授权问题仍走
				// 全球授权链路，补上权限后重跑这次静默刷新。
				if a.handleAuthErrorFor(owner, err) {
					st.authWait = true
				}
				return
			}
			a.mergeFreshPage(st, spec, res)
		})
	}()
}

// mergeFreshPage 把静默刷新拿到的第 1 页并进现有列表。第 1 页以外的旧条目
// 保留：用户已经翻到第 3 页时不能只留下新的 50 条。第一页整体替换而不是
// 逐条插入，避免已删除 / 已改序的帖子留在头部。
func (a *app) mergeFreshPage(st *tabState, spec tabSpec, res pageResult) {
	if st == nil {
		return
	}
	// 兼容直接给 items 塞数据的旧状态（无头测试、本地恢复）：把它当成
	// 已加载的原始行，才能保住已有内容与滚动位置。
	if len(st.raw) == 0 && len(st.items) > 0 {
		st.raw = append([]Item(nil), st.items...)
	}
	if st.page <= 1 || len(st.raw) == 0 {
		// 还没翻过页：整份换成最新的第一页。滚动位置不重置，行数相同时
		// 用户停在原来那一行附近。
		st.raw = append([]Item(nil), res.Items...)
		st.page = 1
		st.noMore = len(res.Items) == 0
		st.filterSkip = 0
	} else {
		fresh := make(map[string]bool, len(res.Items))
		merged := make([]Item, 0, len(res.Items)+len(st.raw))
		for _, it := range res.Items {
			merged = append(merged, it)
			fresh[it.Key] = true
		}
		for _, it := range st.raw {
			if !fresh[it.Key] {
				merged = append(merged, it)
			}
		}
		st.raw = merged
	}
	st.total = res.Total
	st.totalPages = res.TotalPages
	st.loaded = true
	st.items, st.hidden = a.filterItems(spec, st.raw)
	st.refold(spec)

	// 只预取本次第一页里可见条目的头像，被屏蔽作者不必下载。
	if len(res.Items) > 0 {
		visible := make(map[string]bool, len(st.items))
		for _, it := range st.items {
			visible[it.Key] = true
		}
		freshItems := make([]Item, 0, len(res.Items))
		for _, it := range res.Items {
			if visible[it.Key] {
				freshItems = append(freshItems, it)
			}
		}
		a.prefetchItemAvatars(freshItems)
	}
	// 静默刷新成功后同样把新首屏写回缓存，断网时看到的是最近一次成功的内容。
	a.clearFeedCacheState(st)
	a.saveCachedFeed(spec, res.Items, res.Total, res.TotalPages)
}

// reclickTab 是侧边栏一行的点击：点的不是当前这一页就交给选中变化去切页，
// 点的正是当前这一页就再刷新一次——选中值没变时 Sidebar 不报 Changed，
// 所以这一下挂在这一行自己的点击上。
func (a *app) reclickTab(idx int) {
	if idx != a.tab {
		return
	}
	// 再点当前页也要重新请求对应接口，同样过防抖 + 节流队列。
	a.scheduleSidebarRefresh(func() { a.refreshTab(idx) })
}

func (a *app) onAuthChanged(err error) {
	a.loginPending = false
	// 登录状态变化时，上一次会话排队的侧边栏刷新不再有意义。
	a.sidebarRefresh.cancel()
	// 内容屏蔽名单与会话绑定：退出后整体重建，登录成功则后台拉一次最新名单。
	a.resetContentFilter()
	if a.autoLoad && a.auth != nil && a.auth.loggedIn() {
		a.ensureContentFilter(false)
	}
	// 资料菜单属于当前会话：登录、退出、重新授权都先把它收起。
	a.profileMenuOpen = false
	// 标题栏带着昵称：登录、退出、重新授权后都要跟着变。
	if a.win != nil {
		a.win.SetTitle(a.windowTitle())
	}
	switch {
	case err != nil:
		a.loginMsg = friendlyError(err)
		a.status = a.loginMsg
		if !a.authGate.open {
			a.pageName = "login"
		}
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
	// 潘多拉状态在重新授权时要保留：用户可能要重新点重抽，需要还拿着原来的
	// box_instance_id。退出才整体丢弃；
	// 保留时把「已加载」标成过期，弹窗下一帧会重读今日状态。
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
	// 「我的」窗口里的资料与列表随会话变化：退出后一律丢弃，下次打开时按
	// 新的登录态重新拉取。
	a.profile = newProfileState()
	// 评论框的状态栏读的是徽章与佩戴状态：重新授权后按新令牌重新读一次，
	// 退出则整体丢弃。
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
		// 关注流的未读角标登录后重新读一次。
		a.refreshFollowsUnread()
		// 潘多拉入口也静默读一次今日状态：首页标题栏要能直接显示抽中的
		// 事件名，不必先打开潘多拉弹窗。
		a.loadPandoraEntry()
		// 官方图床只对 Pro 会员与守护者开放：登录后就把权限读回来，别等用户拖
		// 图片时才提示「还没确认」。
		a.refreshProStatus(true)
	} else {
		a.stopNotificationWatch()
		// 通知窗口只展示登录后的消息：退出后立即关掉，别留旧内容。
		a.closeNotificationWindow()
		// 「我的」窗口展示的是个人资料与列表，同样随退出关掉。
		a.closeProfileWindow()
		// 金币窗口与标题栏金币数随退出关掉。
		a.stopCoinTagWatch()
		a.closeCoinWindow()
		// 潘多拉弹窗展示盲盒与抽奖结果，退出后一并关掉。
		a.closePandora()
		// 草稿在退出前同步落盘，下次访问按需重新载入。
		a.flushDrafts()
		a.clearLiveDrafts()
		a.stopDraftTimer()
		// 发帖窗口退出后一并关掉并丢弃。
		a.closeComposeWindow()
		a.compose = nil
		// 独立写评论窗口也一样：退出后没有可发的身份，草稿留在页面状态里，
		// 窗口收起来。
		a.closeReplyWindow()
		// 编辑窗口退出后一并关掉并丢弃。
		a.closeEditWindow()
		// 关注与粉丝名单、关注流未读角标都随退出清掉。
		a.closeFollowsWindow()
		a.follows = nil
		a.resetFollowsUnread()
		a.unread = 0
		a.unreadLoading = false
		a.sectionUnread = notificationUnreadCounts{}
		a.notifSeen = nil
		a.notifBaseline = false
		// 退出后系统里可能还留着通知，不再解析它的点击目标。
		a.noticeTargets = nil
	}
	// 资料卡上的关注状态与「是不是我」都跟着登录态走，变化后重新读一次
	// （窗口没开时是空操作）。
	a.refreshUserCardForAuth()
	// 搜索窗口里的「收藏」结果随退出清掉，关键词与类型保留。窗口本身留着：
	// 搜索结果大多是公开内容。
	a.resetSearchResults()
	a.ensureLoaded()
	// 授权成功后只清掉这次授权状态，不替用户重跑被中断的操作——由用户自己
	// 再点一次刚才的操作。
	if err == nil && a.auth.loggedIn() {
		a.cancelAuthGate("")
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
	defer a.drainToasts(c, a.mainClaimsToasts)
	// 帧尾算草稿指纹：输入必然触发重建，非键入改动（表情、图片、选节点）也能捕捉到。
	defer a.draftTick()
	// 命令快捷键是窗口级的：在标题栏、列表或正文里按都能触发。放在建树前，
	// 让这一帧注册的处理器覆盖整个窗口。
	a.bindCommandShortcuts(c)

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

	// 标题栏横跨整个窗口顶端，下面才是左右两栏：左侧导航栏、右侧正文。
	// 横向只有标题栏底下这一条分割线，竖线只出现在两栏之间。
	ui.Column(c).Fill().Children(func() {
		a.titleBar(c)
		ui.Divider(c)
		ui.Row(c).Grow(1).MinHeight(0).Children(func() {
			a.sidebar(c)
			ui.Divider(c)
			// 右侧内容区：信息流、草稿箱、稍后再看与评分榜共用标题栏和
			// 侧边栏，只换这一块。后三页不是信息流，不画右下角的 FAB 列
			// （那里没有可刷新、可发帖的列表）。
			ui.Column(c).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() {
				// 自动签到的成功提示放在内容顶部：一条绿色 Alert 比底部一闪
				// 而过的 toast 明显，点关闭或到点后收起。
				a.autoSignAlert(c)
				switch a.pageName {
				case "drafts":
					a.draftsBody(c)
				case "watchlater":
					a.watchLaterPage(c)
				case "scores":
					a.scoresPage(c)
				default:
					a.body(c)
					a.listFabs(c)
				}
			})
		})
	})
	// 非信息流页没有主列表要拉，跳过 ensureLoaded，别白白发请求、占限流额度。
	switch a.pageName {
	case "drafts", "watchlater", "scores":
	default:
		a.ensureLoaded()
	}
	a.dialogs(c)
}

// dialogs 画出全局授权弹窗与关闭询问，并补开还在等图片的预览。列表页、登录
// 回退页与详情页共用，放在内容之后叠在最上层。预览本身是一扇独立窗口，不画
// 在这里；这里只负责在图片到位后把它打开（见 viewerTick）。
func (a *app) dialogs(c *ui.Context) {
	a.viewerTick()
	// 抽卡弹窗先画：抽卡缺权限时授权框后画、叠在它上面，关掉授权还能回来。
	a.pandoraDialog(c)
	a.nodeEditorDialog(c)
	a.draftsClearDialog(c)
	a.closePromptDialog(c)
	// 首次启动声明放最后，盖在其它弹窗之上；确认后本进程不再画。
	a.disclaimerDialog(c)
	// 授权提示保持在其它弹窗之上。
	a.offlineDialog(c)
	a.authDialogFor(c, toastMain)
}

// feedMaxWidth 是正文栏的最大宽度：窗口再宽，一行文字也不超过 760 DIP，
// 剩下的空间平均留白，阅读视线不必横向来回扫。
const feedMaxWidth float32 = 760

// navSidebarWidth 是正文居中计算用的导航轨折叠宽度。Lobe DraggableSideNav
// 折叠后是 64 DIP（MinWidth），展开宽度由 navWidth 决定。
const navSidebarWidth = 64
const navRailWidth float32 = 64

// navListInset 是展开态列表在 Compact 行内边距（8）之外再留的左侧空白，让
// 展开后的图标与折叠态图标轨（以及左上角开关）的中心严格对齐在 32 DIP：
// 8 + 16 = 24 是图标左缘，加半个 16 的图标宽正好落在 32。折叠进度插值时，
// List 自身的居中留白与本值此消彼长，图标不会在展开/收起过程中横跳。
const navListInset float32 = 16

// sidebarListInset 随折叠进度收回展开态的额外左留白，让行滑进图标轨时不错位。
func sidebarListInset(nav layout.SideNavState) float32 {
	return (1 - min(max(nav.Collapse, 0), 1)) * navListInset
}

// 侧边栏内边距。侧边栏在标题栏下方：上半的导航列表距标题栏下沿留出 8 DIP，
// 下半的底部列表上边留 8、下边留 10，两头都不贴边。左右仍留 16，展开态与列表
// 对齐，折叠态归零，保证图标轨仍居中在 32 DIP。
const (
	sidebarInsetX            float32 = 16
	sidebarNavInsetTop       float32 = 8
	sidebarFooterInsetTop    float32 = 8
	sidebarFooterInsetBottom float32 = 10
)

// sidebarFont 是元信息用的等宽字体：小字号下数字与时序排得整齐，
// 和无衬线字体形成两层质感。
const sidebarFont = "monospace"

// sidebar 是 Lobe 风格的可展开侧边栏：layout.DraggableSideNav 提供拖拽调宽，
// 展开/折叠只走标题栏最左侧那一个开关（见 titleBarSidebarToggle），侧边栏自己
// 不再占一行头部，接缝与底部列表也不画重复的开关。内容与 Lobe 的 display.List
// 配套——把 nav.Collapse / nav.RailWidth 交给 List，收起时行缩成图标方块，
// 展开时显示文字。首行图标与标题栏开关同高，两栏之间只留标题栏底下那一条
// 横向分割线。
func (a *app) sidebar(c *ui.Context) {
	layout.DraggableSideNav(c, &a.navWidth, &a.navExpanded, layout.DraggableSideNavProps{
		MinWidth:        navRailWidth,
		MaxWidth:        280,
		NoHandle:        true,
		HighlightHandle: true,
		Footer: func(nav layout.SideNavState) {
			a.sidebarFooterList(c, nav)
		},
	}, func(nav layout.SideNavState) {
		a.sidebarNavList(c, nav)
	})
}

// navBadgeCount 是侧边栏导航项的 Lobe 红点计数。草稿箱读内存 store，稍后再看
// 读当前账号的待读清单，浏览页读接口带回来的分区未读数（见 section_unread.go）；
// 其余导航项不挂角标。返回 0 时 Badge 自动隐藏。
func (a *app) navBadgeCount(key string) int {
	switch key {
	case "drafts":
		if a.drafts != nil {
			return a.drafts.count()
		}
		return 0
	case "watchlater":
		return a.watchLaterCount()
	}
	if idx, ok := tabIndexFromID(key); ok {
		return a.tabUnreadCount(idx)
	}
	return 0
}

// 折叠图标轨里 ActionIcon 的方块与图标尺寸。角标要贴着图标画，不能落在带内边距
// 的按钮角上：方块比图标大一圈，两者差的一半就是角标从按钮角往图标角的内缩量。
const (
	sidebarRailButtonSize float32 = 32
	sidebarRailGlyphSize  float32 = 16
	navBadgeInset                 = (sidebarRailButtonSize - sidebarRailGlyphSize) / 2
)

// navBadgeDrop 是角标 pill 相对图标顶边的下移量。Lobe 默认把 pill 中心钉在
// 内容顶边，16 的小图标上就像悬在图标上方、贴着上一行。
const navBadgeDrop float32 = 5

// navBadge 在折叠图标轨上画计数角标，用的是 Lobe 的 feedback.Badge。Badge 固定
// 把 pill 钉在内容的右上角，而这里的内容是带内边距的 32x32 按钮，直接包按钮会
// 让 pill 落在按钮角上。所以给 Badge 一个与图标同大的透明内容块，再整层绝对
// 定位到图标位置，pill 就贴着图标的右上角。底色取 Lobe 的 Idle 灰（Badge 的
// 默认状态色 TextQuaternary），不用错误红，也不引入品牌色。
func navBadge(c *ui.Context, count int, build func()) ui.Element {
	tk := lobetheme.TokensOf(c)
	bg := tk.TextQuaternary
	root := ui.Box(c).AlignSelf(ui.Start).Shrink(0)
	root.Children(func() {
		build()
		feedback.Badge(c, feedback.BadgeProps{Count: count, Small: true, Color: bg}, func() {
			ui.Box(c).Size(sidebarRailGlyphSize, sidebarRailGlyphSize)
		}).Absolute().Right(navBadgeInset).Top(navBadgeDrop + navBadgeInset).IgnoreInput(true)
	})
	return root
}

// sidebarNavList 展开时用 Lobe display.List 画带文字的导航行，左侧留出
// navListInset；折叠时换成 sidebarRail 的图标轨，指针停留有 tooltip。
func (a *app) sidebarNavList(c *ui.Context, nav layout.SideNavState) {
	items := a.sidebarNavItems()
	// 展开态数值走 Lobe List 的 Extra 槽位，落在行的右端；角标只在折叠的
	// 图标轨上出现，避免展开行里图标再挂一枚 pill 显得拥挤。
	for i := range items {
		if count := a.navBadgeCount(items[i].Key); count > 0 {
			items[i].Extra = strconv.Itoa(count)
		}
	}
	if !nav.Expanded {
		// 折叠态图标轨居中靠左右零内边距，这里只补顶部留白，别把图标推离轨中线。
		ui.Column(c).Padding(sidebarNavInsetTop, 0, 0, 0).Children(func() {
			sidebarRail(c, items, a.navSel, a.handleSidebarNavItem, a.navBadgeCount)
			a.sidebarNodeRail(c)
		})
		return
	}
	ui.Column(c).Padding(sidebarNavInsetTop, 0, 0, sidebarListInset(nav)).Children(func() {
		clicked := display.List(c, display.ListProps{
			Items:     items,
			Active:    a.navSel,
			Compact:   true,
			Collapse:  nav.Collapse,
			RailWidth: nav.RailWidth,
		})
		if clicked != "" {
			a.handleSidebarNavItem(clicked)
		}
		a.sidebarNodeSection(c, nav)
	})
}

// handleSidebarNavItem 分发导航列表的点击：t<n> 是浏览页切换，其余（草稿箱、
// 稍后再看、评分榜）都是主窗口内嵌页，走底部入口共用的动作分发。折叠图标轨
// 与展开列表都走这里。
func (a *app) handleSidebarNavItem(key string) {
	if idx, ok := tabIndexFromID(key); ok {
		a.selectTab(idx)
		return
	}
	a.handleSidebarAction(key)
}

// sidebarRail 把折叠态的导航画成 Lobe ActionIcon 图标轨：保留 ActionIcon 的
// 外观与状态，再用 Lobe 的 overlay.Tooltip 把名字浮在图标右侧，指针停留或
// 键盘聚焦都能看到，无障碍描述也是同一个名字。badgeCount 给出某项的小红点
// 计数，传 nil 表示这一轨没有角标（例如底部入口）。
func sidebarRail(c *ui.Context, items []display.ListItem, active string, onSelect func(string), badgeCount func(string) int) {
	tk := lobetheme.TokensOf(c)
	ui.Column(c).Gap(2).AlignItems(ui.Center).AlignSelf(ui.Stretch).Children(func() {
		for _, it := range items {
			if it.Divider {
				// 自定义导航项里没有内部分组线；节点分组与底部入口另有
				// 分隔线。保留这里的绘制，兼容仍带 Divider 的其它列表。
				ui.Box(c).Height(1).Margin(4, 8).AlignSelf(ui.Stretch).
					Background(tk.BorderSecondary)
				continue
			}
			count := 0
			if badgeCount != nil {
				count = badgeCount(it.Key)
			}
			build := func() {
				b := action.StyleActionIcon(c, ui.ButtonBase(c), it.Icon, action.ActionIconProps{
					Active:    it.Key == active,
					BlockSize: sidebarRailButtonSize,
					GlyphSize: sidebarRailGlyphSize,
				})
				b.Label(it.Label)
				lobeoverlay.Tooltip(c, b, it.Label, lobeoverlay.TooltipProps{Placement: lobeoverlay.Right})
				if b.Clicked() {
					onSelect(it.Key)
				}
			}
			if count > 0 {
				// Badge 自己把根元素设成 AlignSelf(Start)，这里改回居中，
				// 免得带角标的两项在图标轨里向左跑偏。
				navBadge(c, count, build).AlignSelf(ui.Center)
			} else {
				build()
			}
		}
	})
}

// selectTab 是导航项一次点击的完整处理：点当前页再刷新一次，点别的页先切
// 选中、再按登录要求决定弹授权还是重新拉首屏。三种入口都会重新请求对应
// 接口，请求交给 sidebarRefresh 做防抖 + 节流；选中状态本身仍然立即更新，
// 连续点击时列表数据以最后一次为准。
//
// 主页图标是个例外：还挂着节点筛选时点它，不只是刷新当前列表，而是清掉
// 筛选回到默认列表重新拉首屏。侧边栏自定义节点的高亮读的是同一个筛选，
// 随之熄灭，选中的仍是刚点过的主页图标。
func (a *app) selectTab(idx int) {
	if idx < 0 || idx >= len(a.tabs) {
		return
	}
	// 草稿页的侧边栏仍然可见：点任意浏览页（包括当前已选中的那一项）都要能回来。
	a.pageName = "list"
	a.navSel = tabID(idx)
	if idx == 0 && !a.homeFilter().isAll() {
		a.tab = 0
		a.navSel = tabID(0)
		a.pageName = "list"
		// 筛选与侧边栏高亮立即复位；这一页的重新请求走防抖 + 节流队列。
		a.applyHomeFilter(allNodes())
		a.scheduleSidebarRefresh(func() { a.load(0, 1) })
		if a.tab != a.lastTab {
			a.lastTab = a.tab
			a.status = ""
		}
		return
	}
	if idx == a.tab {
		a.reclickTab(idx)
		return
	}
	a.tab = idx
	a.navSel = tabID(idx)
	if spec := tabSpecs[idx]; spec.NeedsAuth && !a.auth.loggedIn() {
		a.requestLogin("「" + spec.Label + "」需要登录 2Libra 后才能查看。")
	} else {
		// 切页必须重新拉对应接口：已加载的页走后台静默刷新，保留多页
		// 与滚动位置。请求排进队列，由防抖 + 节流控制频率。
		a.scheduleSidebarRefresh(func() { a.refreshTabSilently(idx) })
	}
	if a.tab != a.lastTab {
		a.lastTab = a.tab
		a.status = ""
	}
}

// sidebarFooterItems 是底部常驻入口的数据：设置/关于，未登录时再加登录
// 与注册。账号入口已移除，账号操作收在标题栏账号菜单的气泡里，外观三档也在
// 设置窗口的「外观」页，侧边栏不再单独占这些行。评分榜入口在导航列表「动态」
// 之后（见 sidebarNavItems），和草稿箱、稍后再看一样是内嵌页，不在这里。
func (a *app) sidebarFooterItems() []display.ListItem {
	items := []display.ListItem{
		{Key: "settings", Label: "设置", Icon: lucide.Settings()},
		{Key: "about", Label: "关于", Icon: lucide.Info()},
	}
	switch {
	case a.loginPending:
		items = append(items,
			display.ListItem{Key: "wait", Label: "等待浏览器授权", Icon: lucide.LoaderCircle()},
			display.ListItem{Key: "cancel", Label: "取消登录", Icon: lucide.X()},
		)
	case !a.auth.loggedIn():
		items = append(items,
			display.ListItem{Key: "login", Label: "登录 2Libra", Icon: lucide.LogIn()},
			display.ListItem{Key: "signup", Label: "注册", Icon: lucide.UserRound()},
		)
	}
	return items
}

// sidebarFooterList 展开时用 display.List 画底部入口并留出左侧空白；折叠时
// 换成带 tooltip 的 ActionIcon 图标轨，点击仍走同一处 handleSidebarAction。
// 上下各留一点呼吸空间：左右与导航列表同为 16，最后一项距底缘留出 10 DIP，
// 不再贴着窗口底边。
func (a *app) sidebarFooterList(c *ui.Context, nav layout.SideNavState) {
	items := a.sidebarFooterItems()
	if !nav.Expanded {
		// 折叠态图标轨居中靠左右零内边距，这里只补底部留白，别把图标推离轨中线。
		ui.Column(c).Padding(0, 0, sidebarFooterInsetBottom, 0).Children(func() {
			sidebarRail(c, items, "", a.handleSidebarAction, nil)
		})
		return
	}
	ui.Column(c).Padding(sidebarFooterInsetTop, sidebarListInset(nav), sidebarFooterInsetBottom, sidebarListInset(nav)).Children(func() {
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
	case "settings":
		a.showSettings()
	case "about":
		a.showAbout()
	case "scores":
		a.showScores()
	case "drafts":
		a.showDrafts()
	case "watchlater":
		a.showWatchLater()
	case "wait":
		a.pageName = "login"
	case "cancel":
		a.cancelLogin()
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

// 自绘标题栏的尺寸。32 DIP 是 Windows 标题栏的常规高度，窗口按钮 46×32
// 也与系统按钮同宽同高，文字和图标因此不会被压扁、裁切或挤在一起。标题栏
// 横跨窗口顶端，侧边栏从它下沿开始。
const (
	titleBarHeight   = 32
	titleButtonWidth = 46
	// titleBarControlsWidth 是标题栏右侧窗口区至少占的宽度（DIP）：通知铃铛 +
	// 间距 + 三枚窗口按钮。整页缩放会缩小窗口的逻辑宽度，这一段不能被压窄，
	// 否则最小化 / 最大化 / 关闭按钮会被挤出窗口，点了也没反应。
	titleBarControlsWidth = titleBarHeight + 10 + 3*titleButtonWidth
	// titleGlyphHalf 是窗口按钮里矢量图标的半边长（DIP），10×10 的图标在
	// 32 DIP 高的按钮里比例合适，也不会被按钮边缘裁切。
	titleGlyphHalf = 5
	// titleSearchInsetY 是标题栏搜索框上下留出的边距（DIP）：矮一圈后居中
	// 悬浮，四周更透气。
	titleSearchInsetY = 5
	// titleSearchIconInset 是闪电图标连同左右内边距、间距占的宽度（7 +
	// 15 + 4）。提示文字要落在图标右边的文字区：居中的 hint 以它为左界，
	// 聚焦时的左对齐占位文案从它开始，都与输入内容对齐。
	titleSearchIconInset = 26
	// titleSearchWidthRatio 是标题栏搜索框宽度占窗口宽度的比例：窗口越宽，
	// 输入框越长，既不在窄窗口里挤到通知铃铛，也不在大窗口里显得局促。
	titleSearchWidthRatio = 0.24
	// 宽度的上下限：下限要放得下居中的提示文案加左右内边距，上限免得
	// 盖住标题栏大半条。
	titleSearchWidthMin = 240
	titleSearchWidthMax = 420
	// titleSearchHint 是输入框为空时显示的居中提示文案。它是一层覆盖在
	// 输入框上的文字，不用输入框自带 Placeholder：那个提示跟着输入内容
	// 左对齐，而这里要让 hint 居中、用户输入仍从左边开始。
	titleSearchHint = "命令面板"
)

// titleSearchWidthFor 按窗口宽度算出本帧搜索框的宽度，上下限之间线性缩放。
func titleSearchWidthFor(winW float32) float32 {
	w := winW * titleSearchWidthRatio
	if w < titleSearchWidthMin {
		return titleSearchWidthMin
	}
	if w > titleSearchWidthMax {
		return titleSearchWidthMax
	}
	return w
}

// titleBarHeightFor 是本帧标题栏的实际高度：系统按钮占位更高时跟着它走，
// 至少 32 DIP。标题栏横跨整个窗口，下面的左右两栏从它下沿开始。
func titleBarHeightFor(c *ui.Context) float32 {
	h := float32(titleBarHeight)
	if bar := c.TitleBar(); bar.Height > h {
		h = bar.Height
	}
	return h
}

// titleBar 是完全自绘的顶部标题栏：可拖拽移动窗口（双击最大化）。折叠开关在
// 最左端，搜索输入框居中在整扇窗口的水平中线上，通知铃铛 / 账号标识与自绘的
// 最小化 / 最大化·还原 / 关闭按钮贴在右端；系统标题栏与系统按钮已由
// TitleBarHidden + HideWindowButtons 隐藏。
//
// 标题栏横跨整个窗口顶端，底色与页面一致；下面才是左右两栏，横向只有它底下
// 一条分割线，两栏之间另有一条竖线。搜索框居中靠左、右两段等宽伸缩实现，
// 与窗口宽度无关，因此拖动改变窗口大小时它始终停在中线上。
func (a *app) titleBar(c *ui.Context) {
	t := c.Theme()
	// bar 是系统窗口按钮占用的区域，HideWindowButtons 下为零。保留这段
	// 计算，将来若改回系统按钮，标题栏仍然给它们让出位置。
	bar := c.TitleBar()
	h := titleBarHeightFor(c)
	winW, _ := c.Size()
	searchW := titleSearchWidthFor(winW)
	ui.Row(c).Height(h).FillWidth().AlignItems(ui.Stretch).
		Padding(0, 0, 0, bar.Left).Background(t.Background).DragWindow().Children(func() {
		// 左段：折叠开关。侧边栏头部已去掉，开关收在标题栏最左侧，与下面
		// 侧边栏的图标轨同在 32 DIP 竖中线上。它和右段 Grow(1) 等宽，把中间
		// 的搜索框顶到窗口正中。
		ui.Row(c).Grow(1).MinWidth(0).Padding(0, 0, 0, 16).
			AlignItems(ui.Center).Children(func() {
			a.titleBarSidebarToggle(c)
		})
		// 中段：品牌名的位置换成输入框：点一下就展开命令面板（默认「命令」
		// 模式），切到「搜索」才是搜索窗口入口。前导图标用闪电，有内容时
		// 右侧出现一键清空按钮（见 titleSearchField）。
		if a.search == nil {
			a.search = newSearchState()
		}
		q := a.palette.text
		// 中段可缩：缩放后窗口逻辑宽度变小，先让搜索框让位给右侧窗口按钮。
		ui.Column(c).Width(searchW).Height(h).Shrink(1).MinWidth(0).
			AlignItems(ui.Stretch).Justify(ui.Center).Children(func() {
			inputFocused := a.palette.input.Focused(c)
			in, box := a.titleSearchField(c, searchW, titleBarHeight-2*titleSearchInsetY)
			in.Bind(&a.palette.input).FocusBind(&a.palette.focusTarget, 1)
			if a.palette.blurPending {
				box.Focus()
				a.palette.blurPending = false
			}
			// 点输入框或聚焦时展开面板；焦点离开（切窗口、Tab 到别的控件）
			// 或点到面板外时收起。FocusLeft / PressedOutside 都把挂在输入框
			// 上的面板算作「还在里面」，点面板里的模式按钮/命令不会误关。
			// 面板关闭时 mygo 会把焦点还给锚点，那不是用户重新点进来，靠
			// suppressOpen 忽略这一次，避免「关了又立刻弹回来」。
			if box.PressedInside() {
				a.palette.open = true
				a.palette.suppressOpen = false
			}
			if inputFocused && !a.palette.wasFocused && !a.palette.suppressOpen {
				a.palette.open = true
			}
			if box.FocusLeft() || box.PressedOutside() {
				// 只有面板本来开着、这次是把它关掉时才移走焦点；面板本来就关着
				// 的时候，点详情评论框等控件不能把它们的焦点抢过来。
				if a.palette.open {
					a.palette.blurPending = true
				}
				a.palette.open = false
				a.palette.suppressOpen = true
			}
			a.palette.wasFocused = inputFocused
			// 输入一变回到第一项，清空则回到默认命令模式。
			in.OnChange(func() {
				a.palette.selected = 0
				if a.palette.text == "" {
					a.palette.mode = paletteModeCommands
				}
			})
			in.OnShortcut(0, ui.KeyDown, func() { a.paletteMove(q, 1) })
			in.OnShortcut(0, ui.KeyUp, func() { a.paletteMove(q, -1) })
			// Esc 有内容时清空输入，OnChange 据此回到默认命令模式；面板留着。
			in.OnShortcut(0, ui.KeyEscape, func() {
				if a.palette.text != "" {
					a.palette.text = ""
				}
			})
			in.OnSubmit(func() { a.paletteRun(q) })
			// 空输入时的提示。面板展开时用左对齐的占位文案，落在图标右边的
			// 文字区，与真正输入的内容同一位置；收起后改成居中的 hint。以面板
			// 开合为准而不是输入框聚焦：点窗外会先关面板，但 mygo 未必把输入
			// 框的焦点一起挪走，用聚焦判断会让占位文案赖着不走。IgnoreInput
			// 让点击透到下面的输入框，不会挡住聚焦与命令面板。
			if q == "" {
				if a.palette.open {
					ui.Row(c).Absolute().Left(titleSearchIconInset).Right(0).Top(0).Bottom(0).
						IgnoreInput(true).Children(func() {
						ui.Text(c, titleSearchPlaceholder).TextColor(t.TextMuted).SingleLine()
					})
				} else {
					ui.Row(c).Absolute().Left(titleSearchIconInset).Right(0).Top(0).Bottom(0).
						Center().IgnoreInput(true).Children(func() {
						ui.Text(c, titleSearchHint).TextColor(t.TextMuted).SingleLine()
					})
				}
			}
			if a.palette.open {
				a.commandPalette(c, box, q)
			}
		})
		// 右段：通知铃铛、账号标识与自绘窗口按钮，贴窗口右缘；与左段等宽
		// 伸缩。金币、盲盒、「我的」与退出登录都收在账号标识的气泡里；
		// 设置与未登录时的登录 / 注册留在侧边栏底部。
		ui.Row(c).Grow(1).MinWidth(titleBarControlsWidth).Justify(ui.End).AlignItems(ui.Stretch).Children(func() {
			// 铃铛与账号标识可缩：空间不够时先让窗口按钮保住可点尺寸。
			ui.Row(c).AlignItems(ui.Center).Gap(10).Padding(0, 10, 0, 0).Shrink(1).MinWidth(0).Children(func() {
				// 更新按钮：检查中发现新版本、下载进度与「重启更新」都收在这里。
				a.titleBarUpdateButton(c)
				// 通知铃铛：未读数叠在铃铛右下角，点开独立窗口（不再是侧边栏页面）。
				a.notificationBell(c)
				if a.auth.loggedIn() {
					// 头像 + 昵称是账号菜单的锚点：点一下弹出气泡，签到、金币、
					// 潘多拉、「我的」与退出登录都在气泡里选。标题栏不写
					// 「已登录」，也不放退出按钮：登录后才有的账号操作都由
					// 这颗锚点带出来。
					a.profileTag(c, a.auth.username())
				}
			})
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
	})
}

// titleBarSidebarToggle 是标题栏最左侧的侧边栏折叠开关。侧边栏头部那一行已
// 去掉，开关搬到这里，因此整扇窗口只留标题栏底下一条横向分割线；无障碍名与
// tooltip 沿用原侧边栏开关，读屏和测试都按「展开/折叠侧边栏」找到它。
func (a *app) titleBarSidebarToggle(c *ui.Context) {
	name := "展开侧边栏"
	icon := lucide.PanelLeftOpen()
	if a.navExpanded {
		name = "折叠侧边栏"
		icon = lucide.PanelLeftClose()
	}
	b := action.StyleActionIcon(c, ui.ButtonBase(c), icon, action.ActionIconProps{
		BlockSize: titleBarHeight, GlyphSize: 16,
	})
	b.Label(name).Tooltip(name)
	if b.Clicked() {
		a.navExpanded = !a.navExpanded
	}
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
	tk := lobetheme.TokensOf(c)
	// 显式给高度：按钮本体只设了宽度，过去靠父行的 AlignItems(Stretch) 撑出
	// 高度。详情窗口把这几个按钮套在一层居中、没定高的 Row 里，没有可撑的
	// 父级，Box 就塌成 0 高，点了没反应。把高度写进按钮自己，谁都能直接用。
	b := ui.Box(c).Width(titleButtonWidth).Height(titleBarHeight).Shrink(0).Background(ui.Transparent)
	ink := t.Text
	if b.Hovered() {
		if g == titleClose {
			b.Background(tk.Error) // 关闭：Lobe 错误色红底白叉
			ink = tk.White
		} else {
			b.Background(t.SurfaceHover)
		}
	}
	b.Draw(func(p *ui.Painter, r ui.Rect) {
		// r 是窗口坐标里的按钮矩形，图形要相对 r 偏移来画。
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		half, step := float32(titleGlyphHalf), float32(2) // 图标约 12×12 DIP
		switch g {
		case titleMinimize:
			p.Line(cx-half, cy+0.5, cx+half, cy+0.5, 1, ink)
		case titleMaximize:
			p.Stroke(ui.Rect{X: cx - half, Y: cy - half, W: 2 * half, H: 2 * half}, ink, 0, 1)
		case titleRestore:
			// 前面的方框，加上后面方框露出的上边和右边。错位距离随图标
			// 一起缩放，放大后两个方框仍然是叠在一起的「还原」形。
			front := 2*half - step
			p.Stroke(ui.Rect{X: cx - half, Y: cy - half + step, W: front, H: front}, ink, 0, 1)
			p.Line(cx-half+step, cy-half, cx+half, cy-half, 1, ink)
			p.Line(cx+half, cy-half, cx+half, cy+half-step, 1, ink)
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

// feedInset 是正文栏两侧的留白：内容区宽于偏好里的页宽时把多余空间等分
// 到两边，让文字栏居中；窗口本身更窄时退回 24 DIP 的最小边距。独立详情
// 窗口是双栏布局，不走这里，页宽偏好不会和双栏比例打架。
func (a *app) feedInset(c *ui.Context) float32 {
	width := float32(a.pageWidth())
	inset := float32(24)
	if w, _ := c.Size(); w > 0 {
		if avail := w - navSidebarWidth - 1; avail > width {
			inset = (avail - width) / 2
		}
	}
	return inset
}

// feedHeader 是正文栏顶部的页头：左边是页面名的大标题，右端依次是
// 节点筛选 chip 与条目数。整块靠留白、字号差与字距分层，不画底纹和方框；
// 配色走 Lobe token，让页头与下方列表行的层次一致。
func (a *app) feedHeader(c *ui.Context, spec tabSpec, st *tabState) {
	tk := lobetheme.TokensOf(c)
	ui.Column(c).FillWidth().Shrink(0).
		Padding(24, a.feedInset(c)+16, 14, a.feedInset(c)+16).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(24).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Children(func() {
				ui.Text(c, spec.Label).FontSize(tk.FontSizeHeading4).FontWeight(600).
					LetterSpacing(-0.4).TextColor(tk.TextHeading).SingleLine()
			})
			if spec.FilterByNode {
				a.feedNodeFilter(c, st)
			}
			if count := feedCountLabel(st); count != "" {
				ui.Text(c, count).FontSize(tk.FontSizeSM).Font(sidebarFont).
					TextColor(tk.TextDescription).SingleLine().Shrink(0)
			}
		})
		a.loginBanner(c)
		a.feedCacheNote(c, st)
	})
}

// feedCacheNote 是页头下方的离线说明：列表来自本地缓存时写清缓存时间，
// 不把旧内容当成刚刷新的内容。
func (a *app) feedCacheNote(c *ui.Context, st *tabState) {
	if st == nil || st.cacheNote == "" {
		return
	}
	tk := lobetheme.TokensOf(c)
	display.Text(c, st.cacheNote, display.TextProps{
		Type: display.TextSecondary, FontSize: tk.FontSizeSM, Rows: 2,
	})
}

// loginBanner 在未登录的页头里摆一条登录入口：侧边栏默认折叠成图标轨，
// 底部的「登录 2Libra」只剩一个图标，不点开很难发现。主页面一进来就能看见
// 这里的「立即登录 / 注册」；登录后、等待授权期间整条收起。
func (a *app) loginBanner(c *ui.Context) {
	if a.auth.loggedIn() || a.loginPending {
		return
	}
	tk := lobetheme.TokensOf(c)
	ui.Row(c).FillWidth().Shrink(0).Margin(16, 0, 0, 0).
		Padding(12, 14, 12, 14).Background(tk.FillTertiary).
		AlignItems(ui.Center).Gap(12).Children(func() {
		display.Text(c, "登录后可发帖、评论和收通知", display.TextProps{
			Strong: true, FontSize: 13, Rows: 1,
		})
		ui.Spacer(c).Grow(1)
		if action.Button(c, "立即登录", action.ButtonProps{
			Size: lobetheme.Small, Variant: action.Primary,
		}).Clicked() {
			a.startLogin()
		}
		if action.Button(c, "注册", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
			a.openSignup()
		}
	})
}

// feedCountLabel 是页头右端的小字：有总数时写「共 N 条」，服务端没给
// 总数的页（最新评论）只写已拿到的条数。滚动加载不显示当前页码。
func feedCountLabel(st *tabState) string {
	n := st.total
	if n <= 0 {
		n = len(st.raw)
	}
	if n <= 0 {
		return ""
	}
	return "共 " + strconv.Itoa(n) + " 条"
}

func (a *app) body(c *ui.Context) {
	st := a.tabs[a.tab]
	spec := a.specFor(a.tab)

	if spec.NeedsAuth && !a.auth.loggedIn() {
		ui.Column(c).Grow(1).Center().Padding(24).Children(func() {
			display.Empty(c, display.EmptyProps{
				Icon:        lucide.Lock(),
				Title:       "「" + spec.Label + "」需要登录",
				Description: "该端点要求 read:me / profile 权限，登录后才会发起请求。",
				Action: func() {
					if action.Button(c, "去登录", action.ButtonProps{Variant: action.Primary}).Clicked() {
						a.requestLogin("「" + spec.Label + "」需要登录 2Libra 后才能查看。")
					}
				},
			})
		})
		return
	}
	// 刷新请求还在防抖 / 节流队列里时，列表可能已经被清掉（点主页清筛选）
	// 或还没加载过。这时候不能把「马上就有数据」误画成「暂无内容」。
	if len(st.items) == 0 && (st.loading || a.sidebarRefresh.hasPending()) {
		ui.Column(c).Grow(1).Center().Children(func() {
			lobeSpin(c, feedback.SpinProps{
				Variant: feedback.SpinNetwork,
				Size:    lobetheme.Large,
				Tip:     "正在加载 " + spec.Label + "…",
			})
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
							a.requestLogin("登录并授权后才能继续查看「" + spec.Label + "」。")
						}
					}
				},
			})
		})
		return
	}
	if len(st.items) == 0 {
		// 整页都被名单剔除时不能说「暂无内容」：那会让人以为站点真的没帖子。
		if spec.FilterBlocked && st.hidden > 0 {
			ui.Column(c).Grow(1).Children(func() {
				a.feedHeader(c, spec, st)
				ui.Column(c).Grow(1).Center().Padding(24).Children(func() {
					display.Empty(c, display.EmptyProps{
						Icon:        lucide.EyeOff(),
						Title:       "内容已按屏蔽规则隐藏",
						Description: "当前列表的条目都在你的屏蔽名单里。",
						Action: func() {
							if action.Button(c, "管理屏蔽名单", action.ButtonProps{}).Clicked() {
								a.showProfileSettings(profileSettingsFilter)
							}
						},
					})
				})
			})
			return
		}
		// 首页即使没有内容也留住页头：页头标题与节点筛选不能随列表空掉消失。
		if spec.FilterByNode {
			ui.Column(c).Grow(1).Children(func() {
				a.feedHeader(c, spec, st)
				ui.Column(c).Grow(1).Center().Children(func() {
					display.Empty(c, display.EmptyProps{Title: "暂无内容"})
				})
			})
			return
		}
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
	}).Grow(1).Padding(0, a.feedInset(c), 10, a.feedInset(c)).Gap(0)
	a.loadMoreFooter(c, st.moreErr, st.loading, func() {
		a.load(a.tab, st.page)
	})
	// 无限滚动：滚动到列表底部且还有下一页时，自动加载更多，不再依赖
	// 页码按钮。「最新评论」服务端忽略 page，因此只对 pagingPages 的 tab 触发。
	// 续拉失败后停在提示上，等用户点「重试」，否则每次重建都会再发一次注定
	// 失败的请求。
	if spec.Paging == pagingPages && st.loaded && !st.loading && st.moreErr == "" &&
		!st.noMore && (len(st.raw) > 0 || len(st.items) > 0) && st.list.AtEnd() {
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
	ui.Row(c).FillWidth().Padding(8, composeFabInset, 8, a.feedInset(c)).Gap(8).AlignItems(ui.Center).Children(func() {
		if errText != "" {
			ui.Text(c, "加载更多失败："+errText).FontSize(tk.FontSizeSM).
				TextColor(tk.Error).MaxLines(2).Grow(1)
			if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				retry()
			}
			return
		}
		lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
		ui.Text(c, "正在加载更多…").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
	})
}

// rowWith 画一行列表项，由调用方决定点开它的动作。它带悬浮的「加入稍后再看」
// 按钮，只给主窗口信息流（首页等各 tab）用；其它窗口走 rowWithNoWatchLater。
func (a *app) rowWith(c *ui.Context, it Item, open func(Item)) {
	a.rowWithOptions(c, it, open, true)
}

// rowWithNoWatchLater 是不带「加入稍后再看」按钮的行，给主窗口以外的所有
// 列表用：我的、搜索、合集浏览、稍后再看与浏览历史等窗口。稍后再看窗口本身
// 就是待读清单与历史，行尾再挂这个按钮没有意义，浏览历史尤其要干净。
func (a *app) rowWithNoWatchLater(c *ui.Context, it Item, open func(Item)) {
	a.rowWithOptions(c, it, open, false)
}

func (a *app) rowWithOptions(c *ui.Context, it Item, open func(Item), watchLater bool) {
	if it.Notif != nil {
		a.notificationRow(c, it)
		return
	}
	if it.Extra != nil {
		a.richRow(c, it, open, watchLater)
		return
	}
	tk := lobetheme.TokensOf(c)
	titleColor := postReadInk(tk, a.isPostRead(it.ShortID))
	// 统一的 Lobe 行表面：静止透明、悬停 FillTertiary、圆角与过渡取主题。
	// 纵向 padding 与行内间距跟列表密度偏好走，横向 12 对齐 token，字号不变。
	row := feedRowSurface(c).PaddingY(a.rowPadY()).PaddingX(12)
	// 稍后再看按钮是悬浮动作：指针停在行上或键盘焦点落进行内时才出现，
	// 平时让行尾只留回复数；浏览历史这类窗口整行不挂该按钮。
	reveal := watchLater && (row.Hovered() || row.FocusWithin())
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(a.rowGap()).Children(func() {
			// 标题是这一行里唯一的重心：字号明显大于副标题和时效信息，
			// 靠字号与颜色分层：未读用正文色，已读退到四级文字色。字号用
			// Lobe 的 LG（16），比原先的 19 收一档，贴近 Lobe 列表的密度。
			ui.Text(c, it.Title).FontSize(tk.FontSizeLG).MaxLines(2).TextColor(titleColor)
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
						if node := strings.TrimSuffix(it.SubtitlePrefix, " · "); node != "" {
							nodeTagAction(c, nodeTagProps{
								Label: node, Ref: it.Node, OnPick: a.showNodeFeed,
							})
						}
						a.userTag(c, userTagSpec{
							Name:   it.Author,
							Avatar: it.AuthorAvatar,
							Size:   tk.FontSizeSM - 1,
							Color:  tk.TextDescription,
							Badges: it.AuthorBadges,
							Pro:    it.AuthorIsPro,
						})
						// 有节点前缀时后缀自带「 · 」；没有前缀时昵称就是这一段的
						// 开头，得自己补上分隔符，别让「·」落在最前面。
						if suffix := strings.TrimPrefix(it.SubtitleSuffix, " · "); suffix != "" {
							ui.Text(c, " · "+suffix).FontSize(tk.FontSizeSM).
								TextColor(tk.TextDescription).SingleLine()
						}
						// 稍后再看紧跟发帖时间，留在元信息这一行里，不单独占
						// 行尾那一列（行尾只放回复数）。
						a.feedWatchLaterButton(c, it, watchLater, reveal)
					})
			case it.Subtitle != "":
				// 没有作者时 Subtitle 是「节点 · 时间」整段：节点仍拆成 Tag，
				// 剩下的时间保留原来的「 · 」前缀。
				if node := strings.TrimSuffix(it.SubtitlePrefix, " · "); node != "" {
					ui.Row(c).MinWidth(0).AlignItems(ui.Center).Wrap().Font(sidebarFont).
						Children(func() {
							nodeTagAction(c, nodeTagProps{
								Label: node, Ref: it.Node, OnPick: a.showNodeFeed,
							})
							if rest := strings.TrimPrefix(it.Subtitle, node); rest != "" {
								ui.Text(c, rest).FontSize(tk.FontSizeSM).
									TextColor(tk.TextDescription).SingleLine()
							}
							a.feedWatchLaterButton(c, it, watchLater, reveal)
						})
				} else {
					ui.Row(c).MinWidth(0).AlignItems(ui.Center).Font(sidebarFont).Children(func() {
						ui.Text(c, it.Subtitle).FontSize(tk.FontSizeSM).
							TextColor(tk.TextDescription).SingleLine()
						a.feedWatchLaterButton(c, it, watchLater, reveal)
					})
				}
			}
			// 帖子行多一行时效信息：最后回复人、更新时间与最高楼层。没人
			// 回复时没有这一行，右侧的回复数仍显示 0。
			if it.Stats != nil && it.Stats.Replier != "" {
				a.latestReplyRow(c, it, open)
			}
		})
		// 行尾只留回复数；稍后再看按钮已经挪到副标题里，紧跟发帖时间。
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
// 接口给了最后一条回复的评论 id 时，「最后回复 谁」这段文案是个链接：点它
// 带着 CommentID 打开正文，直接定位到那层楼（openDetail 的 focusID）。只有
// 这段文字可点、悬停高亮加下划线，后面的时间与最高楼层只是说明文字，不跟着
// 响应点击或悬停。链接是最内层可点元素，点它不会连带触发外层整行的点击。
// id 未知时退回纯文本，不可点。
func (a *app) latestReplyRow(c *ui.Context, it Item, open func(Item)) {
	st := it.Stats
	tk := lobetheme.TokensOf(c)
	row := ui.Row(c).MinWidth(0).Gap(6).AlignItems(ui.Center).Font(sidebarFont)
	row.Children(func() {
		link := ui.Row(c).Gap(6).AlignItems(ui.Center)
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
		})
		if st.LastCommentID != "" {
			jump := it
			jump.CommentID = st.LastCommentID
			link.Cursor(ui.CursorPointer).OnClick(func() { open(jump) })
			link.Tooltip("跳到最后回复")
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
}

// postStatsBadge 是帖子行右端的状态块：置顶角标，以及一个等宽数字表示的
// 回复数。数字只写值、不写「N 回复」，悬停提示负责说明它是什么；没有气泡
// 图标，右端只剩一个安静的计数。
func (a *app) postStatsBadge(c *ui.Context, it Item) {
	// 对齐交给 rowWith 里包住本块与稍后再看按钮的那一行，这里不再自己贴顶或
	// 加顶部留白，否则两块会用不同的中线。
	badge := ui.Row(c).Gap(8).Shrink(0).AlignItems(ui.Center)
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
				// 授权：清掉授权状态，避免以后再弹过期提示。
				if a.hasPendingAuthorization() {
					a.cancelAuthGate("已取消授权")
				}
				a.pageName = "list"
			}
			ui.Text(c, "登录 2Libra").FontSize(18).Bold()
			ui.Spacer(c).Grow(1)
			if name := a.auth.username(); name != "" {
				pro, _ := a.auth.proStatus()
				a.userTag(c, userTagSpec{
					Name:   name,
					Avatar: a.auth.avatarURL(),
					Label:  "当前：" + name,
					Size:   12,
					Color:  t.TextMuted,
					Pro:    pro,
				})
				if ui.Button(c, "退出登录").Clicked() {
					a.logoutAccount()
				}
			}
		})
		ui.Divider(c)
		if warn := a.schemeWarning(); warn != "" {
			ui.Text(c, warn).FontSize(12).TextColor(t.Danger).MaxLines(4)
		}
		if a.loginMsg != "" {
			ui.Text(c, a.loginMsg).TextColor(t.TextMuted).MaxLines(4)
		}
		if a.hasPendingAuthorization() {
			ui.Text(c, "完成这次授权后，请回到刚才的页面自己再点一次操作。").FontSize(12).TextColor(t.TextMuted)
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

// completeLoginCallback 是登录回退页上「用回调链接完成登录」：浏览器没自动
// 跳回时，用户粘贴的地址同样交给唯一的会话完成授权。
func (a *app) completeLoginCallback(raw string) error {
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
	// 老板键藏起窗口后，用户从托盘或深链把界面叫回来：同步清掉隐藏状态，
	// 下一次老板键才会重新隐藏，而不是先做一次无意义的恢复。
	if a.bossKeyHidden {
		a.restoreBossKeyWindows()
	}
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

// startLogin 是「登录」入口：重新授权用的也是这唯一的会话。
func (a *app) startLogin() { a.startLoginWithScopes(nil) }

// startLoginFromPage 是登录回退页上的「打开浏览器授权」。
func (a *app) startLoginFromPage() {
	a.startLoginWithScopes(a.pendingAuthScopes())
}

// logoutAccount 退出登录：清掉令牌，主窗口留在登录页。退出前先记下账号，
// 异步清掉这个账号专属的信息流缓存（动态等私有列表），不让下个账号读到。
func (a *app) logoutAccount() {
	name := ""
	if a.auth != nil {
		name = a.auth.username()
	}
	a.auth.Logout()
	if a.kv != nil && name != "" {
		prefix := feedCachePrefix + strings.ToLower(name) + ":"
		go func() {
			if _, err := a.kv.clearPrefixes(prefix); err != nil {
				log.Printf("清理账号信息流缓存失败：%v", err)
			}
		}()
	}
}

// openSignup 打开带邀请码的 2Libra 注册页：注册在网页完成，应用内不做。未
// 登录用户点「注册」直接打开系统浏览器，注册成功后再回应用里「登录」授权。
func (a *app) openSignup() {
	a.srv.OpenURL(signupURL) // URL 服务在 UI 线程调用
}

// startLoginWithScopes 是上面那条流程的唯一实现：额外 scope 来自全局授权
// 弹窗登记的缺失权限，普通登录传 nil 只申请默认范围。
func (a *app) startLoginWithScopes(scopes []string) {
	// 一次只允许一次授权在等回调：BeginScopes 会取消上一次，迟到的回调不会
	// 把新一次授权顶掉。
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
// 如果这次授权是为被中断的调用发起的，授权状态也一并清掉。
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
	// 日志先落盘：本机会话损坏、深链注册失败这类早期事件也要留下记录。
	// 打包成 GUI 后 stderr 不可见，这里是唯一的现场来源（见 logfile.go）。
	installLogSink(configDir())
	// 开机启动读写的是系统登录项：在设置窗口渲染之前把它接到 mygo 的原生
	// 实现。无头测试不经过 main，两个钩子保持空实现。
	wireStartupHooks()
	log.Printf("启动 t2-desktop %s", mygo.App.Version())
	// 进程级单例：唯一的会话、共享依赖（代理、外观、图片缓存、离线缓存）。
	// 从建 app 到进主循环的顺序集中在 runStartup，测试用假 host 走同一条链。
	d := newDesktop("")
	if err := runStartup(d, mygoStartupHost{}); err != nil {
		log.Fatal("应用启动失败：", err)
	}
}
