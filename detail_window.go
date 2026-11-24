package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/nav"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 详情页独立窗口：一篇帖子一扇 mygo 窗口，可以同时开很多扇；也可以把几扇
// 合并成一扇带标签的窗口，每个标签还是那份独立的 detailState。
//
// 为什么不用「一个窗口里放很多面板」：详情页是双栏长文，一屏里塞两篇谁也
// 读不了；用户要的是像浏览器那样想看几篇开几篇。合并只是给用户一个把散落
// 的窗口收拢起来的手段，不是把内容再叠回一屏。
//
// 每扇窗口只从 app 借共享依赖（会话、代理、图片缓存），界面状态都属于自己
// 的 detailState。a.detail 是「本帧正在画哪一篇」的游标：窗口自己的 view
// 开头会把它拨到自己的状态，画完就可以被下一扇窗口拨走。后台回调判定某份
// 状态还活不活，靠 detailState.host / dead，不看游标（见 detailLive）。

const (
	detailWindowWidth    = 1100
	detailWindowHeight   = 760
	detailWinMinWidth    = 720
	detailWinMinHeight   = 480
	detailTabTitleMaxRun = 18
	// toastDetailBase 之后的 toastTarget 留给详情窗口，一个窗口一个编号。
	// 固定枚举值都在 20 以内，这里从 100 起不会撞上。
	toastDetailBase = 100
)

// detailWindow 是一扇详情窗口（合并后它承载多个标签）。只在主线程读写。
type detailWindow struct {
	id    int
	owner *app
	win   *mygo.Window
	// states / stacks 一一对应：每个标签一篇帖子，stacks[i] 是那篇的内联
	// 返回栈。合并时把别的窗口整个搬过来，所以两边始终是同一长度。
	states []*detailState
	stacks [][]*detailState
	active int
	toast  toastTarget
	// removed 在窗口从注册表摘掉时置位；之后所有后台结果都必须丢弃。
	removed bool
}

func (h *detailWindow) current() *detailState {
	if h == nil || h.active < 0 || h.active >= len(h.states) {
		return nil
	}
	return h.states[h.active]
}

// indexOf 找某篇帖子在这一窗的标签序号；没有则 -1。
func (h *detailWindow) indexOf(shortID string) int {
	if h == nil || shortID == "" {
		return -1
	}
	for i, d := range h.states {
		if d != nil && d.shortID == shortID {
			return i
		}
	}
	return -1
}

// detailLive 回答「这份详情状态的结果现在还有没有人要」。无窗口的测试仍按
// 旧规矩只认游标；有窗口时每扇详情窗口的每篇帖子都是活的。
func (a *app) detailLive(d *detailState) bool {
	if d == nil || d.dead {
		return false
	}
	if d.host != nil {
		return !d.host.removed && !d.host.closedForMerge(d)
	}
	return a.detail == d
}

// closedForMerge 是合并时搬走的状态在旧窗口里的标记：搬走后旧窗口的
// OnClosed 迟早会到，那时不能把已经进了新窗口的那份状态一起清掉。
func (h *detailWindow) closedForMerge(d *detailState) bool {
	return h != nil && h.removed
}

// detailHostOf 返回这篇帖子所在的详情窗口；不在任何详情窗口里时 nil。
func (a *app) detailHostOf(d *detailState) *detailWindow {
	if d != nil && d.host != nil && !d.host.removed {
		return d.host
	}
	return nil
}

// detailToast 返回当前这篇详情的操作提示该落在哪扇窗口。详情窗口里的动作
// 报在自己的窗口，老的内联路径仍报主窗口。
func (a *app) detailToast() toastTarget {
	return a.detailToastFor(a.detail)
}

// detailToastFor 回答某篇具体帖子的动作提示该落在哪扇窗口。后台回调里
// a.detail 可能已经被别的窗口拨走，那里必须传回调自己抓住的 d。
func (a *app) detailToastFor(d *detailState) toastTarget {
	if h := a.detailHostOf(d); h != nil {
		return h.toast
	}
	return toastMain
}

func (h *detailWindow) invalidate() {
	if h != nil && h.win != nil {
		h.win.Invalidate()
	}
}

// focusDetailWindow 把窗口带到前台：最小化就还原，再 Show/Focus/Invalidate。
func (a *app) focusDetailWindow(h *detailWindow) {
	win := h.win
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

/* ------------------------------------------------------------------
   开窗口、关窗口、合并
---------------------------------------------------------------- */

// openDetailWindow 建一扇只装这一帖的详情窗口。已经开着的同一篇不重复开，
// 直接带前台，这样从通知、搜索、合集几个入口点到同一篇不会堆一屏副本。
func (a *app) openDetailWindow(it Item) {
	if it.ShortID == "" {
		return
	}
	if a.autoLoad && a.win == nil && len(a.detailWindows) == 0 {
		// 应用已经没有任何窗口了：mygo 会在最后一个窗口关闭时退出。这里
		// 没有可以借来排队的窗口，按老的内联路径画在主窗口视图里。
		a.openDetailInline(it)
		return
	}
	for _, h := range a.detailWindows {
		if h.removed {
			continue
		}
		if i := h.indexOf(it.ShortID); i >= 0 {
			h.active = i
			a.focusDetailWindow(h)
			return
		}
	}
	h := &detailWindow{
		id:     a.nextDetailID,
		owner:  a,
		active: 0,
		toast:  toastTarget(toastDetailBase + a.nextDetailID),
	}
	a.nextDetailID++
	d := a.newDetailState(it)
	d.host = h
	h.states = []*detailState{d}
	h.stacks = [][]*detailState{nil}
	a.detailWindows = append(a.detailWindows, h)
	a.detail = d
	a.loadDetail(d, it)
	if a.win == nil {
		// headless：调用方直接渲染 h.view，不碰 mygo 的原生窗口。
		return
	}
	a.createDetailWindow(h)
}

// createDetailWindow 真正建原生窗口。只在主线程、有主窗口可排队时调用。
func (a *app) createDetailWindow(h *detailWindow) {
	d := h.current()
	title := "详情"
	if d != nil {
		title = detailTabTitle(d)
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle(title),
		Width:             detailWindowWidth,
		Height:            detailWindowHeight,
		MinWidth:          detailWinMinWidth,
		MinHeight:         detailWinMinHeight,
		StateKey:          fmt.Sprintf("detail-%d", h.id),
		BackgroundColor:   lobeWindowBackground,
		TitleBarStyle:     mygo.TitleBarHidden,
		HideWindowButtons: true,
		Content:           ui.View(a.windowView(func(c *ui.Context) { a.detailWindowView(h, c) })),
	})
	h.win = win
	win.OnClosed(func() {
		if h.win != win {
			return // 旧窗口迟到的回调，别动新的
		}
		h.win = nil
		if !h.removed {
			a.removeDetailWindow(h)
		} else {
			a.afterDetailWindowRemoved(win)
		}
	})
	// 主窗口已经关掉时，详情窗口顶上来当排队的窗口：publish、toast 退回
	// 目标都得指着还活着的窗口，不然后台结果没人收。
	if a.win == nil {
		a.win = win
	}
}

// closeDetailWindow 关一扇详情窗口。窗口里的每篇帖子各自落盘浏览状态，
// 关掉这篇不会影响同一窗口的其它标签，更不会影响别的窗口与主窗口。
func (a *app) closeDetailWindow(h *detailWindow) {
	if h == nil || h.removed {
		return
	}
	a.removeDetailWindow(h)
	win := h.win
	h.win = nil
	if win != nil {
		win.Close()
	}
}

// removeDetailWindow 把窗口从注册表摘掉并清掉它名下所有状态。原生窗口的
// OnClosed 与主动 Close 都走这里，removed 保证只清一次。
func (a *app) removeDetailWindow(h *detailWindow) {
	if h == nil || h.removed {
		return
	}
	h.removed = true
	win := h.win
	a.authGateWindowClosed(h.toast)
	for i, x := range a.detailWindows {
		if x == h {
			a.detailWindows = append(a.detailWindows[:i], a.detailWindows[i+1:]...)
			break
		}
	}
	for _, d := range h.states {
		a.disposeDetailState(d)
	}
	if a.detail != nil && a.detail.host == h {
		a.detail = nil
	}
	a.afterDetailWindowRemoved(win)
	if a.win != nil {
		a.win.Invalidate()
	}
}

// disposeDetailState 收起一篇帖子：停节点轮询、存浏览状态、不让它再收结果。
func (a *app) disposeDetailState(d *detailState) {
	if d == nil || d.dead {
		return
	}
	// 状态马上就 dead，之后没有帧尾能替它算指纹，先同步落盘。
	a.flushDrafts()
	a.stopDetailNodeWatch(d)
	a.saveReplyView(d)
	if d.composerDetached && a.replyWinFor == d {
		a.closeReplyWindow()
	}
	a.resetReplyIdentity(d)
	d.dead = true
}

// afterDetailWindowRemoved 在详情窗口关掉后走通用的窗口收尾：只要这个账号
// 还有别的窗口（详情或其它附属窗口），生命周期就继续，最后一扇关掉才收尾。
func (a *app) afterDetailWindowRemoved(closed *mygo.Window) {
	a.accountWindowClosed(closed)
}

// closeAllDetailWindows 退出账号、收进托盘以外的一次性清理（Destroy 不弹
// 关闭询问）。合并搬走的状态已经在旧窗口里标 dead，这里只处理还活着的。
func (a *app) closeAllDetailWindows() {
	for _, h := range append([]*detailWindow(nil), a.detailWindows...) {
		h.removed = true
		a.authGateWindowClosed(h.toast)
		for _, d := range h.states {
			a.disposeDetailState(d)
		}
		if win := h.win; win != nil {
			h.win = nil
			win.Destroy()
		}
	}
	a.detailWindows = nil
	if a.detail != nil {
		a.detail = nil
	}
}

// mergeDetailWindows 把所有详情窗口收进当前这一扇，做成标签。被合并的窗口
// 用 Destroy（不走 OnClose），它们名下的 detailState 原封不动搬过来，所以
// 正文、评论、草稿、滚动位置全都还在。
func (a *app) mergeDetailWindows() {
	if len(a.detailWindows) < 2 {
		return
	}
	target := a.detailHostOf(a.detail)
	if target == nil || target.removed {
		target = a.detailWindows[0]
	}
	merged := 0
	for _, h := range append([]*detailWindow(nil), a.detailWindows...) {
		if h == target || h.removed {
			continue
		}
		for i, d := range h.states {
			d.host = target
			target.states = append(target.states, d)
			var stack []*detailState
			if i < len(h.stacks) {
				stack = h.stacks[i]
			}
			target.stacks = append(target.stacks, stack)
		}
		h.removed = true
		for i, x := range a.detailWindows {
			if x == h {
				a.detailWindows = append(a.detailWindows[:i], a.detailWindows[i+1:]...)
				break
			}
		}
		merged += len(h.states)
		if win := h.win; win != nil {
			h.win = nil
			win.Destroy()
		}
	}
	if merged == 0 {
		return
	}
	target.active = 0
	for i, d := range target.states {
		if d != nil && !d.dead {
			target.active = i
			break
		}
	}
	a.detail = target.current()
	target.invalidate()
	a.toast(target.toast, fmt.Sprintf("已合并 %d 篇帖子到这个窗口", len(target.states)))
}

// closeDetailTab 只关掉当前标签；还有别的标签时窗口留着，切到相邻那篇。
// 只有最后一个标签才关整扇窗口。
func (a *app) closeDetailTab(h *detailWindow, idx int) {
	if h == nil || h.removed || idx < 0 || idx >= len(h.states) {
		return
	}
	d := h.states[idx]
	a.disposeDetailState(d)
	h.states = append(h.states[:idx], h.states[idx+1:]...)
	if idx < len(h.stacks) {
		h.stacks = append(h.stacks[:idx], h.stacks[idx+1:]...)
	}
	if len(h.states) == 0 {
		a.closeDetailWindow(h)
		return
	}
	if h.active >= len(h.states) {
		h.active = len(h.states) - 1
	}
	a.detail = h.current()
	h.invalidate()
	if a.win != nil {
		a.win.Invalidate()
	}
}

// detailTabTitle 是标签标题：正文加载出来用帖子标题，超长截断，没加载好
// 就用 short id，标签不会空着。
func detailTabTitle(d *detailState) string {
	if d == nil {
		return "详情"
	}
	title := ""
	if d.topic != nil {
		title = strings.TrimSpace(d.topic.Title)
	}
	if title == "" {
		title = d.shortID
	}
	runes := []rune(title)
	if len(runes) > detailTabTitleMaxRun {
		return string(runes[:detailTabTitleMaxRun]) + "…"
	}
	return title
}

/* ------------------------------------------------------------------
   窗口界面
-------------------------------- */

// detailWindowView 是详情窗口的 view：先拨游标、认领自己的 toast，再画自绘
// 标题栏与原来的详情内容。合并后的窗口多一条标签栏。
func (a *app) detailWindowView(h *detailWindow, c *ui.Context) {
	if h == nil || h.removed {
		return
	}
	a.applyTheme(c)
	t := c.Theme()
	a.srv = c.Services()
	c.Root().Background(t.Background)
	defer a.drainToasts(c, h.claimsToasts)
	defer a.draftTick()
	c.OnShortcut(0, ui.KeyEscape, func() { a.closeDetailTab(h, h.active) })
	if d := h.current(); d != nil {
		a.detail = d
		a.detailStack = h.stacks[h.active]
	} else {
		a.detail = nil
	}

	ui.Column(c).Fill().Children(func() {
		a.detailWindowTitleBar(h, c)
		ui.Divider(c)
		if len(h.states) > 1 {
			a.detailTabBar(h, c)
		}
		ui.Column(c).Grow(1).MinHeight(0).Children(func() {
			a.detailBody(c)
		})
	})
	a.authDialogFor(c, h.toast)
	a.viewerTick()
}

// claimsToasts 只认这一扇详情窗口的提示；窗口不在了，主窗口会接手。
func (h *detailWindow) claimsToasts(target toastTarget) bool {
	return h != nil && target == h.toast
}

// detailWindowTitleBar 是详情窗口的自绘标题栏：左标题、右合并 / 最小化 /
// 关闭，都是图标加 tooltip，不写文字标签。
func (a *app) detailWindowTitleBar(h *detailWindow, c *ui.Context) {
	t := c.Theme()
	d := h.current()
	// 常驻操作只剩一枚且没有「更多」动作时不再画操作组，直接收进这根自定义
	// 标题栏；清单与操作组共用，条件只写一遍（见 detailToolActions）。
	var lone *detailToolAction
	if d != nil {
		if icons, more := detailToolSplit(a.detailToolActions(d)); len(icons) == 1 && len(more) == 0 {
			act := icons[0]
			lone = &act
		}
	}
	ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
		Background(t.Surface).DragWindow().Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
			AlignItems(ui.Center).Children(func() {
			ui.Icon(c, lucide.FileText()).Size(16, 16).TextColor(t.Accent)
			ui.Text(c, "详情").FontSize(13).Bold().SingleLine().TextColor(t.Text)
			a.accountWindowTag(c)
		})
		ui.Row(c).AlignItems(ui.Center).Padding(0, 4).Gap(2).Children(func() {
			if lone != nil {
				a.detailTitleToolButton(c, d, *lone)
			}
			mergeLabel, mergeTip := "合并详情窗口", "把所有详情窗口收进这个窗口，作为标签切换"
			if len(a.detailWindows) < 2 {
				mergeTip = "当前只有一个详情窗口，打开更多帖子后才能合并"
			}
			if a.detailWindowIconButton(c, lucide.Layers(), mergeLabel, mergeTip, len(a.detailWindows) > 1) {
				a.mergeDetailWindows()
			}
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := h.win; win != nil {
					win.Minimize()
				}
			})
			titleMaximizeButton(c, h.win)
			titleBarButton(c, titleClose, "关闭", func() { a.closeDetailTab(h, h.active) })
		})
	})
}

// detailTitleToolButton 把工具条里唯一的一枚动作画进详情窗口标题栏：沿用
// 标题栏 28×28 的图标按钮规格，无障碍名与 tooltip 照旧，忙碌时禁用入口。
func (a *app) detailTitleToolButton(c *ui.Context, d *detailState, act detailToolAction) {
	if !a.detailWindowIconButton(c, act.icon, act.label, act.tip, !act.busy) {
		return
	}
	// iconActionButton 的 Clicked 在构建期就返回，所以这里能安全用 c 取剪贴板服务。
	switch act.kind {
	case detailToolCopy:
		a.copyToClipboard(c, act.href, a.detailToastFor(d), "已复制链接")
	case detailToolRefresh:
		a.refreshTopic()
	case detailToolComments:
		a.askToggleComments(d)
	default:
		if act.onClick != nil {
			act.onClick()
		}
	}
}

// detailWindowIconButton 是标题栏里 28×28 的图标按钮（和普通操作按钮同规
// 格）：label 给读屏与点击测试，tip 解释点了会怎样。
func (a *app) detailWindowIconButton(c *ui.Context, icon *ui.SVG, label, tip string, enabled bool) bool {
	// 禁用态也把图标按钮画出来：读屏仍看得到入口和 tooltip，点了不动而已。
	b := iconActionButton(c, icon, label, tip)
	if !enabled {
		b.Disabled(true)
	}
	return b.Clicked()
}

// detailTabBar 用 Lobe 的 nav.Tabs 画标签。切换时只换游标与正文状态，窗口
// 还是那一扇；每篇帖子的滚动、草稿、折叠都留在自己的 detailState 里。
func (a *app) detailTabBar(h *detailWindow, c *ui.Context) {
	items := make([]nav.TabItem, 0, len(h.states))
	for _, d := range h.states {
		items = append(items, nav.TabItem{Label: detailTabTitle(d)})
	}
	sel := h.active
	// 标签栏与「关掉当前标签」并排：绝对定位会贴到窗口外层，标签一多就被
	// 挤到看不到的地方，还是老老实实占一行更可靠。
	ui.Row(c).FillWidth().AlignItems(ui.Center).Padding(0, 10, 0, 12).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			nav.Tabs(c, &sel, nav.TabsProps{
				Items:   items,
				Variant: nav.TabsSquare,
				Size:    lobetheme.Small,
			}, nil)
		})
		if a.detailWindowIconButton(c, lucide.X(), "关闭当前标签", "只关掉这一篇帖子，其它标签不受影响", len(h.states) > 1) {
			a.closeDetailTab(h, h.active)
		}
	})
	if sel != h.active && sel >= 0 && sel < len(h.states) {
		// 隐藏的标签不再重建，帧尾不会再算它的指纹：先把要走的那一篇落盘。
		// flushDrafts 会遍历所有 live 状态，用本帧刚算好的 draftFP 判断改动。
		a.flushDrafts()
		h.active = sel
		a.detail = h.current()
		a.detailStack = h.stacks[h.active]
		h.invalidate()
	}
}

// detailBody 是原来详情页的主体（不含标题栏、不含 Esc），主窗口内联与详情
// 窗口共用，避免两份排版实现慢慢长歪。
func (a *app) detailBody(c *ui.Context) {
	a.detailView(c, false)
}

// showMainWindowAfterDetail 是通知 / 搜索 / 资料卡这些入口打开详情之后把
// 界面露出来的动作：详情已经独立成窗口了，就带那扇窗口到前台；老的内联
// 路径仍然回主窗口。
func (a *app) showMainWindowAfterDetail() {
	if h := a.detailHostOf(a.detail); h != nil {
		a.focusDetailWindow(h)
		return
	}
	a.showMainWindow()
}

// toastDetailWindows 供 toasts.go 认领动态编号的详情窗口提示。
func (a *app) toastDetailWindow(target toastTarget) *mygo.Window {
	if int(target) < toastDetailBase {
		return nil
	}
	for _, h := range a.detailWindows {
		if h.toast == target && !h.removed {
			return h.win
		}
	}
	return nil
}

// detailWindowCount 只给测试与状态提示用：现在开着几扇详情窗口。
func (a *app) detailWindowCount() int {
	n := 0
	for _, h := range a.detailWindows {
		if !h.removed {
			n++
		}
	}
	return n
}
