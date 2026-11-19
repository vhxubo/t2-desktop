package main

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown/parser"
)

// 多图预览窗口
//
// 详情页正文、每条评论（以及发帖预览、个人简介这类同样走 Markdown 的地方）
// 各自成组：点组里任意一张配图，独立窗口打开这一组并停在它上面，可以左右翻页、
// 滚轮缩放、按住左键拖动看局部、右键（或工具条按钮）另存为。两组互不影响
// ——在某条评论的大图里翻页只在这条评论自己的图之间走，不会串到正文或别的
// 评论。
//
// 分组靠「从解析树认图」：article_render.go 的 articlePictures 按 Markdown
// 视图解析好的文档认出这一份源文里的全部配图，与 articleBlock 的显示规则
// 同源。正文、附言、个人简介各是独立的一份源文，评论一行一份，因此天然各自
// 成组；窗口里翻的正是这一组，张数与画面里显示的一致。
//
// 预览是一扇独立窗口（见 showViewerWindow）：不内嵌到详情页或其它窗口里，
// 点开图片后可以拖到一边、缩放或最小化，详情窗口照常能操作。状态只在本文件
// 维护；图还在加载时点一下先记下，图片到位后由来源窗口的下一次重建补开
// （见 wantImage / viewerTick）。

// 预览窗口的外观与交互参数。
const (
	// 窗口的默认尺寸与下限：默认比主窗口矮一点，小屏也放得下；缩放后按
	// 窗口大小重新算「刚好放下」。
	imageViewerWindowWidth  = 960
	imageViewerWindowHeight = 720
	imageViewerWindowMinW   = 560
	imageViewerWindowMinH   = 360
	// 缩放两端与步长：再小就只剩一个点，再大也只是糊成一片。滚轮、加减键与
	// 工具条按钮都在这个范围里乘，按一次不会跳出范围。
	imageViewerMinZoom  float32 = 0.1
	imageViewerMaxZoom  float32 = 8
	imageViewerWheel    float32 = 1.25 // 滚轮一格放大 / 缩小的倍数
	imageViewerKeyZoom  float32 = 1.2  // 加减号、工具条按钮每次的倍数
	imageViewerToolbarH float32 = 52   // 顶部工具条的高度
	imageViewerFooterH  float32 = 26   // 底部说明与另存为结果那一行的高度
	imageViewerButton   float32 = 32   // 工具条上图标按钮的边长
	imageViewerPad      float32 = 24   // 图片与窗口边缘的留白
)

// 预览窗口里给人看与给测试找的文案。
const (
	imageViewerHint     = "滚轮缩放 · 拖动平移 · 左右方向键翻页 · 右键另存为 · Esc 关闭"
	imageViewerPrev     = "上一张"
	imageViewerNext     = "下一张"
	imageViewerZoomIn   = "放大"
	imageViewerZoomOut  = "缩小"
	imageViewerActual   = "实际大小"
	imageViewerSave     = "另存为"
	imageViewerMinimize = "最小化"
	imageViewerClose    = "关闭预览"
	imageViewerLabel    = "图片预览"
	imageViewerSaving   = "正在保存…"
	imageViewerEmpty    = "图片还没加载好"
)

// viewerNoticeSeconds 是另存为结果说明在窗口底部的停留时长，测试可以调短。
var viewerNoticeSeconds int64 = 6

// viewerNoteInterval 是结果说明出现后重新构建界面的间隔：说明到点自己撤掉，
// 不能等下一次输入才消失。测试把它调短。
var viewerNoteInterval = 2 * time.Second

// nowTime 是窗口里计时用的时钟。测试可以把它换成假时钟，验证结果说明会自己
// 撤掉（见 image_viewer_test.go）。
var nowTime = time.Now

// imageViewerGroup 是窗口正在看的那组图：按源文顺序排列的配图。正文的一段、
// 附言、某条评论各是一组，组内翻页，组间互不影响。
type imageViewerGroup struct {
	Items []articlePicture
}

// same 报告两组是不是同样几张图：同一处再点一次不必重置缩放。
func (g imageViewerGroup) same(o imageViewerGroup) bool {
	if len(g.Items) != len(o.Items) {
		return false
	}
	for i := range g.Items {
		if viewerKey(g.Items[i]) != viewerKey(o.Items[i]) {
			return false
		}
	}
	return true
}

// imageViewerWant 是「点了但还没加载好」的那张图与它所在的组。
type imageViewerWant struct {
	url   string
	group imageViewerGroup
}

// imageViewer 是预览窗口的状态。它挂在 app 上而不是某个页面：同一个详情页的
// 正文、评论视图与独立窗口都可能打开同一组图。
type imageViewer struct {
	open  bool
	group imageViewerGroup
	index int
	// zoom 是当前这张图的缩放，0 表示还没算过：第一次绘制按「刚好放下」算
	// （见 fitZoom）。1 是图片自身的像素尺寸。
	zoom float32
	// panX / panY 是图片相对居中的平移（窗口 DIP），只在图片放大到超出舞台
	// 时生效；拖动与按住左键挪动都在这里改（见 viewerInput）。换图、重开或
	// 关闭时归零。
	panX, panY float32
	// dragging 表示左键正按在图上拖动：松手前指针怎么挪都只平移，不翻页、
	// 不关窗口。imagePress 记住这一次按下落在图上，松手时遮罩收到的点击是
	// 它引起的，不能当成「点空白处关闭」。
	dragging   bool
	imagePress bool
	// want 是点了但还在加载的那张图：图片到位后自动打开，失败则忘掉，不弹
	// 一个空窗。
	want *imageViewerWant
	// pointerX / pointerY 是遮罩收到的最后一次指针位置（窗口 DIP）：右键落在
	// 图上时才弹「另存为」菜单（见 viewerInput）。
	pointerX, pointerY float32
	// menuOpen 表示图上的右键菜单开着；menuAt 是按下右键的位置（窗口 DIP）。
	menuOpen bool
	menuAt   ui.Point
	// 另存为的结果：saving 是正在另存为的图地址（还没开始或已结束时为空），
	// savedNote 是上一次结果的说明，noteAt 是它出现的时间。它们由后台另存为
	// 在结果回来时写、界面每帧读，经 savingMu 保护（真实窗口里同一线程，但
	// 无窗口的测试里后台协程直接写）。
	savingMu  sync.Mutex
	saving    string
	savedNote string
	noteAt    time.Time
	// savingDone 在这一次另存为彻底结束（成功、失败或取消，结果已经写回
	// 界面）时关闭：测试靠它等到状态稳定，不必轮询只由 UI 线程写的字段。
	savingDone chan struct{}
	// keys 是快捷键层（见 viewerKeyLayer）的身份：窗口每帧重建，靠它把键盘
	// 焦点稳定地留在窗口里，不落回页面的正文。
	keys ui.Handle
}

/* ------------------------------------------------------------------
   窗口
--------------------------------------------------------------- */

// showViewerWindow 打开（或提到前台）预览窗口：预览是一扇独立窗口，不占
// 详情、发帖这些窗口的位置，可以拖到一边或最小化。只在 UI 线程调用；无头
// 测试没有窗口，只准备状态，测试直接渲染 imageViewerWindowView。
func (a *app) showViewerWindow() {
	if a.win == nil {
		return
	}
	v := a.viewer
	if v == nil || !v.open {
		return
	}
	if win := a.viewerWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle(imageViewerLabel),
		Width:             imageViewerWindowWidth,
		Height:            imageViewerWindowHeight,
		MinWidth:          imageViewerWindowMinW,
		MinHeight:         imageViewerWindowMinH,
		StateKey:          "image-viewer",
		BackgroundColor:   "#101012",
		TitleBarStyle:     mygo.TitleBarHidden, // 工具条兼作标题栏，见 imageViewerToolbar
		HideWindowButtons: true,
		Content:           ui.View(a.imageViewerWindowView),
	})
	a.viewerWin = win
	win.OnClosed(func() {
		a.viewerWin = nil
		if v := a.viewer; v != nil {
			v.close()
		}
		a.accountWindowClosed(win)
	})
}

// closeViewerWindow 关掉预览窗口：状态由调用方先收，用户从系统那里关掉窗口
// （Alt+F4、任务栏）时由 OnClosed 收尾。
func (a *app) closeViewerWindow() {
	win := a.viewerWin
	a.viewerWin = nil
	if win != nil {
		win.Close()
	}
}

// redrawViewerWindow 让预览窗口按最新状态重建。窗口没开时什么都不做。
func (a *app) redrawViewerWindow() {
	if win := a.viewerWin; win != nil {
		win.Invalidate()
	}
}

// imageViewerWindowView 是预览窗口的 view：整扇窗口就是预览，顶部工具条兼作
// 标题栏（可拖动窗口，右侧最小化与关闭），底部一行操作提示。
func (a *app) imageViewerWindowView(c *ui.Context) {
	a.applyTheme(c)
	a.srv = c.Services()
	c.Root().Background(ui.Hex("#101012"))
	a.drainToasts(c, a.claimsToasts(toastViewer))
	a.imageViewer(c)
}

/* ------------------------------------------------------------------
   打开、翻页与关闭
--------------------------------------------------------------- */

// openImageGroup 点开 pictures 里的 dest：这一组图按源文顺序打开，停在 dest
// 上。图文正文、评论、表格里的配图都走这里。
func (a *app) openImageGroup(pictures []articlePicture, dest string) {
	a.openImage(imageViewerGroup{Items: pictures}, dest)
}

// wantImageGroup 记录「点了 pictures 里还在加载的 dest」，见 wantImage。
func (a *app) wantImageGroup(pictures []articlePicture, dest string) {
	a.wantImage(imageViewerGroup{Items: pictures}, dest)
}

// openImage 打开 group 并停在 key 那张图上（匹配见 viewerKey）。点击发生在
// 正文、评论还是窗口里都一样：组由调用方按源文认好，状态只在这里维护；
// 预览窗口没开就开一扇，已经开着就把它提到前台。
func (a *app) openImage(group imageViewerGroup, key string) {
	v := a.viewer
	if v == nil || len(group.Items) == 0 {
		return
	}
	// 已经在看同一组的同一张图：保持现状，缩放与翻页位置都不丢。
	if v.open && v.group.same(group) {
		if cur, ok := v.current(); ok && viewerKey(cur) == strings.TrimSpace(key) {
			v.want = nil
			a.publish(a.showViewerWindow)
			return
		}
	}
	v.open = true
	v.group = group
	v.index = viewerIndexOf(group.Items, key)
	v.zoom = 0
	v.panX, v.panY = 0, 0
	v.dragging, v.imagePress = false, false
	v.clearNote()
	v.want = nil
	a.publish(a.showViewerWindow)
}

// wantImage 记下「点了这张图，但它还在加载」：图片到位后的下一次重建补开
// 预览窗口（见 viewerTick）。
func (a *app) wantImage(group imageViewerGroup, key string) {
	v := a.viewer
	if v == nil || len(group.Items) == 0 {
		return
	}
	v.want = &imageViewerWant{url: strings.TrimSpace(key), group: group}
}

// viewerTick 在图片到位后补开预览：点了的那张已经解出来，就打开它所在的组
// 并停在它上面；失败则忘掉这次点击，不开一扇加载不出来的空窗。来源窗口
// （详情、发帖、编辑、写评论）每帧构建时调用（见 dialogs 与各窗口 view）。
func (a *app) viewerTick() {
	v := a.viewer
	if v == nil || v.want == nil {
		return
	}
	w := v.want
	switch a.images.state(w.url) {
	case imageLoaded:
		v.want = nil
		a.openImage(w.group, w.url)
	case imageFailed:
		v.want = nil
	}
}

// step 翻到上 / 下一张：到头就停住，不绕回另一头，连按方向键不会突然跳回
// 第一张。换页后缩放回到「刚好放下」，平移也归零。
func (v *imageViewer) step(delta int) {
	if !v.canStep(delta) {
		return
	}
	v.index += delta
	v.zoom = 0
	v.panX, v.panY = 0, 0
	v.dragging, v.imagePress = false, false
	v.clearNote()
}

// current 返回正在看的那张图。
func (v *imageViewer) current() (articlePicture, bool) {
	if v.index < 0 || v.index >= len(v.group.Items) {
		return articlePicture{}, false
	}
	return v.group.Items[v.index], true
}

// canStep 报告上 / 下一张存不存在：按钮据此禁用，方向键翻页走同一判断。
func (v *imageViewer) canStep(delta int) bool {
	next := v.index + delta
	return next >= 0 && next < len(v.group.Items)
}

// close 关掉预览状态：缩放、平移、结果说明、菜单与待打开的图都清掉。
func (v *imageViewer) close() {
	v.open = false
	v.zoom = 0
	v.panX, v.panY = 0, 0
	v.dragging, v.imagePress = false, false
	v.clearNote()
	v.want = nil
	v.menuOpen = false
}

// startViewerClose 是「关闭」这个动作的统一入口：窗口里的按钮、右键菜单与
// Esc 都走它，收掉状态再把那扇独立窗口关掉。关窗口排到下一轮主线程更新里：
// 这些操作有的发生在构建期（底层的点击 / Esc 查询），不能在自己的帧里销毁
// 自己的窗口。
func (a *app) startViewerClose() {
	if v := a.viewer; v != nil {
		v.close()
	}
	a.publish(a.closeViewerWindow)
}

/* ------------------------------------------------------------------
   缩放与命中
--------------------------------------------------------------- */

// fitZoom 返回「刚好放进舞台」的缩放：按图片自身的像素尺寸算，最多 1（小图
// 不硬拉大）。尺寸未知时按 1。stageW / stageH 是图片可用区域的尺寸。
func fitZoom(iw, ih, stageW, stageH float32) float32 {
	if iw <= 0 || ih <= 0 || stageW <= 0 || stageH <= 0 {
		return 1
	}
	return min(1, min(stageW/iw, stageH/ih))
}

// zoomBy 把缩放乘上 factor 并夹在两端之间。zoom 还是 0（没算过）时以 fit 为
// 起点，第一次滚轮不会因为起点缺失跳成 1。
func zoomBy(zoom, fit, factor float32) float32 {
	if zoom <= 0 {
		zoom = fit
	}
	return min(max(zoom*factor, imageViewerMinZoom), imageViewerMaxZoom)
}

// viewerPanLimit 返回某个方向上能平移的最远距离：图片比舞台大多少，就最多
// 挪多少，图片边缘正好贴住舞台边缘；图片没超出舞台时不许挪，免得拖出空边。
func viewerPanLimit(size, stage float32) float32 {
	return max((size-stage)/2, 0)
}

// clampPan 把平移夹在 ±limit 之间。
func clampPan(v, limit float32) float32 {
	return min(max(v, -limit), limit)
}

// viewerPan 把平移夹进当前图片与舞台允许的范围。
func viewerPan(panX, panY, iw, ih, zoom float32, lay viewerLayout) (float32, float32) {
	w, h := iw*zoom, ih*zoom
	return clampPan(panX, viewerPanLimit(w, lay.stageW)),
		clampPan(panY, viewerPanLimit(h, lay.stageH))
}

// imageBounds 返回图片在窗口里的位置，按窗口尺寸、布局、缩放与平移算：舞台
// 在工具条与说明之间居中，图片在舞台里居中后按 panX / panY 挪动。绘制按它
// 摆图，右键菜单与拖动命中都按它判断。
func imageBounds(lay viewerLayout, iw, ih, zoom, panX, panY float32) ui.Rect {
	panX, panY = viewerPan(panX, panY, iw, ih, zoom, lay)
	w, h := iw*zoom, ih*zoom
	stageY := lay.toolbarH
	stageH := lay.h - lay.toolbarH - lay.footerH
	return ui.Rect{X: (lay.w-w)/2 + panX, Y: stageY + (stageH-h)/2 + panY, W: w, H: h}
}

// viewerStageRect 返回舞台在窗口里的矩形。图片放大平移后会溢出舞台，绘制时
// 由舞台的 Clip 裁掉（见 imageViewerStage）；命中判断必须裁在同一范围，否则
// 盖到工具条上的那部分图片会抢走关闭、翻页这些按钮的点击（见 overImage）。
func viewerStageRect(lay viewerLayout) ui.Rect {
	return ui.Rect{X: 0, Y: lay.toolbarH, W: lay.w, H: lay.h - lay.toolbarH - lay.footerH}
}

// overImage 报告窗口里的 (x, y) 是否落在当前图片上。先要求点落在舞台内，再
// 看是否落在图片上：图片放大后可以比舞台大得多，不先裁进舞台的话，工具条和
// 底栏上的点也会被当成按在图上，按钮就点不动了。
func (v *imageViewer) overImage(lay viewerLayout, iw, ih, zoom float32) bool {
	return viewerStageRect(lay).Contains(v.pointerX, v.pointerY) &&
		imageBounds(lay, iw, ih, zoom, v.panX, v.panY).Contains(v.pointerX, v.pointerY)
}

// dragBy 把这一次指针移动加到平移上，并立刻夹进允许的范围：拖到边就停住，
// 继续拖也不会把图拖出舞台。
func (v *imageViewer) dragBy(dx, dy float32, lay viewerLayout, iw, ih, zoom float32) {
	v.panX, v.panY = viewerPan(v.panX+dx, v.panY+dy, iw, ih, zoom, lay)
}

/* ------------------------------------------------------------------
   认组
--------------------------------------------------------------- */

// viewerKey 是配图在组里的身份：去掉首尾空白的地址。同一组里重复出现的同一
// 张图按下标先到先得，翻页时两张一模一样，停在哪张都行。
func viewerKey(p articlePicture) string { return strings.TrimSpace(p.URL) }

// viewerIndexOf 返回 key 在组里的下标，找不到是 0（打开组的第一张）。
func viewerIndexOf(items []articlePicture, key string) int {
	key = strings.TrimSpace(key)
	for i, p := range items {
		if viewerKey(p) == key {
			return i
		}
	}
	return 0
}

/* ------------------------------------------------------------------
   界面
--------------------------------------------------------------- */

// imageViewer 画出预览窗口的全部内容（见 imageViewerWindowView）；没有打开时
// 什么都不画。
//
// 版式：铺满整扇窗口的半透明底（点空白处关闭），顶部一条工具条（第几张 /
// 共几张、翻页、缩放、另存为、最小化、关闭），中间是舞台里居中的图片，底部
// 一行说明。工具条同时是这扇窗口的自绘标题栏：空白处按住可以拖动窗口，右侧
// 是最小化与关闭。图片按 zoom 缩放后整体居中；放大到超出舞台时按住左键可以
// 拖动图片看局部，拖到边就停住，不会拖出空边。
//
// 输入：
//   - 滚轮、左键拖动与右键由底层的 HandleInput 处理：滚轮只缩放；左键落在图上
//     按住就能平移，松手时不算「点了空白处」；右键落在图上时弹系统菜单（里面
//     有「另存为」），位置记下来（见 viewerInput）。
//   - 方向键 / PageUp / PageDown 翻页，加减号缩放，0 还原成原尺寸，
//     Ctrl+S（macOS 上 ⌘S）另存为。这些键登记在一个铺满窗口、透传指针的透明
//     层上（见 viewerKeyLayer）：焦点落在页面里时也归预览，而工具条按钮照常
//     可点。
//   - Esc 交给底层的 OverlayShortcut，关掉这扇预览窗口。
func (a *app) imageViewer(c *ui.Context) {
	a.viewerTick()
	v := a.viewer
	if v == nil || !v.open {
		return
	}
	p, ok := v.current()
	if !ok {
		a.startViewerClose()
		return
	}
	src, iw, ih := a.viewerImage(p.URL)
	if iw <= 0 || ih <= 0 {
		// 尺寸还不知道（原图还没到）：先按 1×1 占着，图片到位后的下一帧
		// 窗口会自己换成清楚的（见 viewerImage）。
		iw, ih = 1, 1
	}
	w, h := c.Size()
	lay := viewerLayoutFor(w, h)
	fit := fitZoom(iw, ih, lay.stageW, lay.stageH)
	zoom := v.zoom
	if zoom <= 0 {
		zoom = fit
	}
	// 平移夹进当前缩放允许的范围：缩小之后原来的平移可能已经越界。
	v.panX, v.panY = viewerPan(v.panX, v.panY, iw, ih, zoom, lay)

	back := ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).
		Background(ui.RGBA(0, 0, 0, 0.72)).Role(ui.RoleDialog).Label(imageViewerLabel)
	back.Modal()
	back.Children(func() {
		// 快捷键层先画，后面的工具条与图片盖在它上面；它透传指针，所以只占
		// 按键、不挡点击（见 viewerKeyLayer）。
		a.viewerKeyLayer(c, v, fit)
		ui.Column(c).FillWidth().FillHeight().Clip().Children(func() {
			a.imageViewerToolbar(c, v, lay, zoom, fit)
			a.imageViewerStage(c, v, p, src, iw, ih, lay, zoom)
			a.imageViewerFooter(c, v, lay)
		})
	})
	// 右键菜单远在光标处，锚点很小；它画在这里，与遮罩同层。
	if v.menuOpen {
		a.imageViewerMenu(c, v, p)
	}
	// 键盘焦点留在窗口里：点「下一张」这类按钮后，焦点不该掉回页面的正文，
	// 方向键与加号减号才有地方使。每帧重新聚焦，窗口重建也不会丢。
	v.keys.Focus(c)
	// 指针到底层：滚轮在这里缩放，右键在这里记下位置（见 viewerInput）。
	back.HandleInput(func(ev ui.InputEvent) bool {
		return a.viewerInput(v, p, lay, iw, ih, zoom, fit, ev)
	})
	// 底层收到的点击若是按在图上那一下的收尾（拖动或单击），只结束手势，不
	// 当成点空白处；真正点在舞台的空白处才关闭。工具条是窗口拖动区，底部
	// 说明行点一下也不该误关整扇窗口。
	clicked := back.Clicked()
	if clicked && v.imagePress {
		clicked, v.imagePress = false, false
	}
	if clicked && !viewerStageRect(lay).Contains(v.pointerX, v.pointerY) {
		clicked = false
	}
	// Esc 与点舞台空白处都关掉这扇预览窗口。
	if back.OverlayShortcut(0, ui.KeyEscape) || clicked {
		a.startViewerClose()
	}
}

// viewerKeyLayer 是预览窗口里承载快捷键的一层：它先画、铺满整扇窗口，不画
// 东西、指针透传（PassThrough），所以点击与悬停照常落到随后画的工具条与图片上。
//
// 为什么不用底层的 HandleInput：HandleInput 只把键交给有焦点的元素和它周围的
// 元素；OnShortcut 则随「焦点与它周围的元素都没吃掉这个键」生效，预览开着时
// 这些键自然归它。这一层还被 Bind 成预览的持久身份，每帧由预览把焦点放在它
// 身上（见 imageViewer），焦点不会掉到其它控件上。
func (a *app) viewerKeyLayer(c *ui.Context, v *imageViewer, fit float32) {
	keys := ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().
		Background(ui.Transparent).PassThrough()
	step := func(d int) func() {
		return func() {
			v.step(d)
			a.redrawViewerWindow()
		}
	}
	zoomStep := func(factor float32) func() {
		return func() {
			z := v.zoom
			if z <= 0 {
				z = fit
			}
			v.zoom = zoomBy(z, fit, factor)
			a.redrawViewerWindow()
		}
	}
	keys.OnShortcut(0, ui.KeyLeft, step(-1))
	keys.OnShortcut(0, ui.KeyUp, step(-1))
	keys.OnShortcut(0, ui.KeyRight, step(1))
	keys.OnShortcut(0, ui.KeyDown, step(1))
	keys.OnShortcut(0, ui.KeyPageUp, step(-1))
	keys.OnShortcut(0, ui.KeyPageDown, step(1))
	keys.OnShortcut(0, ui.KeyEqual, zoomStep(imageViewerKeyZoom))
	keys.OnShortcut(0, ui.KeyMinus, zoomStep(1/imageViewerKeyZoom))
	keys.OnShortcut(0, ui.Key0, func() {
		v.zoom = 1
		a.redrawViewerWindow()
	})
	keys.OnShortcut(ui.Cmd, ui.KeyS, func() {
		if p, ok := v.current(); ok {
			a.saveImage(p.URL)
		}
	})
	keys.Bind(&v.keys)
}

// imageViewerMenu 是图片上的右键菜单：一张藏在光标位置的定位块负责弹出系统
// 菜单（与别处一致），菜单项「另存为」「实际大小」「关闭预览」。
func (a *app) imageViewerMenu(c *ui.Context, v *imageViewer, p articlePicture) {
	at := ui.Box(c).Absolute().Left(v.menuAt.X).Top(v.menuAt.Y).Size(1, 1)
	at.ContextMenu(func(m *ui.Menu) {
		if m.Item(imageViewerSave).Shortcut(ui.Cmd, ui.KeyS).Chosen() {
			a.saveImage(p.URL)
		}
		m.Separator()
		if m.Item(imageViewerActual).Chosen() {
			v.zoom = 0
		}
		if m.Item(imageViewerClose).Chosen() {
			a.startViewerClose()
		}
	})
}

// viewerInput 处理窗口里的滚轮、触控板缩放与指针，返回是否吃掉了这个事件。
// 键盘见 viewerKeyLayer。
//
// 左键按在图上就开始拖动：吃掉这次按下，之后的移动只平移图片，松手时遮罩收到
// 的那一下点击也由 imagePress 认领，不会把窗口关掉。按在图上但没拖动就是一次
// 普通单击，同样不关窗口；点空白处才关（见 imageViewer）。右键按位置判断是不是
// 落在图上，记下来的位置留给下一帧弹菜单。
func (a *app) viewerInput(v *imageViewer, p articlePicture, lay viewerLayout, iw, ih, zoom, fit float32, ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputScroll:
		// 滚轮：向上放大、向下缩小。窗口开着时滚轮只缩放，不再滚动背后的正文
		// 或评论列表。
		switch {
		case ev.DY < 0:
			v.zoom = zoomBy(zoom, fit, imageViewerWheel)
		case ev.DY > 0:
			v.zoom = zoomBy(zoom, fit, 1/imageViewerWheel)
		default:
			return false
		}
		a.redrawViewerWindow()
		return true
	case ui.InputZoom:
		// 触控板的双指缩放：按它给的比例缩放。
		if ev.Scale <= 0 || ev.Scale == 1 {
			return false
		}
		v.zoom = zoomBy(zoom, fit, ev.Scale)
		a.redrawViewerWindow()
		return true
	case ui.InputPointerDown:
		v.pointerX, v.pointerY = ev.X, ev.Y
		// 每一次按下都重新认领点击：这一次落在图上，松手就不算点空白。
		v.imagePress = false
		if ev.Button == 0 && v.overImage(lay, iw, ih, zoom) {
			v.dragging, v.imagePress = true, true
			return true
		}
		// 右键（有的鼠标把副键报成中键）落在图上时弹菜单：位置先记下，菜单
		// 在下一帧按它弹出。之前那次移动可能没落到遮罩上（比如点开图片那
		// 一下），所以这里以按下时的位置为准；按在图上别处则收起旧菜单。
		switch {
		case v.overImage(lay, iw, ih, zoom) && (ev.Button == 1 || ev.Button == 2):
			v.menuAt, v.menuOpen = ui.Point{X: ev.X, Y: ev.Y}, true
		case !v.overImage(lay, iw, ih, zoom):
			v.menuOpen = false
		}
	case ui.InputPointerMove:
		prevX, prevY := v.pointerX, v.pointerY
		v.pointerX, v.pointerY = ev.X, ev.Y
		if !v.dragging {
			return false
		}
		if ev.Button != 0 {
			// 按着的那颗键已经换了：拖动结束，别再跟着指针跑。
			v.dragging = false
			return false
		}
		v.dragBy(ev.X-prevX, ev.Y-prevY, lay, iw, ih, zoom)
		a.redrawViewerWindow()
		return true
	case ui.InputPointerUp:
		v.pointerX, v.pointerY = ev.X, ev.Y
		if !v.dragging {
			return false
		}
		v.dragging = false
		return true
	}
	return false
}

// imageViewerToolbar 是预览窗口顶部的工具条：左边说明这是第几张、共几张与
// 当前缩放，右边是翻页、缩放、另存为、最小化与关闭。按钮都是图标 + tooltip。
// 工具条同时兼作自绘标题栏：空白处可以拖动窗口（双击最大化 / 还原）。
func (a *app) imageViewerToolbar(c *ui.Context, v *imageViewer, lay viewerLayout, zoom, fit float32) {
	t := c.Theme()
	total := len(v.group.Items)
	// 正在另存为时按钮禁用；状态一次读出来，别在 Children 里再去碰（后台
	// 线程可能正好写回结果）。
	saving, _ := v.viewerSaveState()
	ui.Row(c).FillWidth().Height(lay.toolbarH).Shrink(0).
		Padding(0, 18).Gap(8).AlignItems(ui.Center).
		Background(t.Surface.Alpha(0.96)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		DragWindow().Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			// 显示与读屏用同一句话：「第 2 / 3 张 · 100%」。
			ui.Text(c, fmt.Sprintf("第 %d / %d 张 · %s", v.index+1, total, viewerPercent(zoom))).
				FontSize(11).TextColor(t.TextMuted).SingleLine()
		})
		a.viewerButton(c, prevImageIcon, imageViewerPrev, "看上一张（←）", !v.canStep(-1), func() { v.step(-1) })
		a.viewerButton(c, nextImageIcon, imageViewerNext, "看下一张（→）", !v.canStep(1), func() { v.step(1) })
		ui.Box(c).Width(1).Height(18).Background(t.Border).Shrink(0)
		a.viewerButton(c, zoomOutIcon, imageViewerZoomOut, "缩小（− 键或滚轮向下）", zoom <= imageViewerMinZoom, func() {
			v.zoom = zoomBy(zoom, fit, 1/imageViewerKeyZoom)
		})
		a.viewerButton(c, zoomInIcon, imageViewerZoomIn, "放大（+ 键或滚轮向上）", zoom >= imageViewerMaxZoom, func() {
			v.zoom = zoomBy(zoom, fit, imageViewerKeyZoom)
		})
		a.viewerButton(c, actualSizeIcon, imageViewerActual, "还原成完整显示（0 键）", false, func() {
			v.zoom = 0
		})
		a.viewerButton(c, saveImageIcon, imageViewerSave, "把这张图另存为文件", saving, func() {
			if cur, ok := v.current(); ok {
				a.saveImage(cur.URL)
			}
		})
		ui.Box(c).Width(1).Height(18).Background(t.Border).Shrink(0)
		a.viewerButton(c, minimizeIcon, imageViewerMinimize, "把预览窗口最小化", false, a.minimizeViewerWindow)
		a.viewerButton(c, closeIcon, imageViewerClose, "关闭预览（Esc）", false, a.startViewerClose)
	})
}

// minimizeViewerWindow 最小化预览窗口：从任务栏或 Alt+Tab 可以再还原回来。
func (a *app) minimizeViewerWindow() {
	if win := a.viewerWin; win != nil {
		win.Minimize()
	}
}

// viewerButton 是工具条上的一枚图标按钮：悬停给浅底，禁用时变淡。label 供
// 读屏与测试识别，tip 是悬浮说明。
func (a *app) viewerButton(c *ui.Context, icon *ui.SVG, label, tip string, disabled bool, onClick func()) {
	t := c.Theme()
	b := ui.Box(c).Size(imageViewerButton, imageViewerButton).Radius(8).Shrink(0).Center().
		Background(ui.Transparent)
	hovered := b.Hovered()
	if hovered && !disabled {
		b.Background(t.SurfaceHover)
	}
	ink := t.Text
	switch {
	case disabled:
		b.Disabled(true)
		ink = t.TextMuted.Alpha(0.45)
	case hovered:
		ink = t.Accent
	}
	b.Children(func() {
		ui.Icon(c, icon).Size(17, 17).TextColor(ink)
	})
	b.Label(label).Tooltip(tip).OnClick(onClick)
}

// imageViewerStage 是图片舞台：铺满工具条与底栏之间，图片按 zoom 缩放后居中，
// 再按 panX / panY 平移。放大到超出舞台时四周被裁掉，按住左键拖动可以挪着看；
// 指针停在图上时给出抓手光标，拖动中换成抓紧的样子。
func (a *app) imageViewerStage(c *ui.Context, v *imageViewer, p articlePicture, src ui.ImageSource, iw, ih float32, lay viewerLayout, zoom float32) {
	t := c.Theme()
	stage := ui.Box(c).Grow(1).FillWidth().MinHeight(0).Center().Clip()
	stage.Children(func() {
		if src == nil {
			ui.Column(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(20, 20).Label("图片加载中")
				ui.Text(c, imageViewerEmpty).FontSize(13).TextColor(t.TextMuted)
			})
			return
		}
		px, py := viewerPan(v.panX, v.panY, iw, ih, zoom, lay)
		b := imageBounds(lay, iw, ih, zoom, px, py)
		w, h := b.W, b.H
		if w <= 0 || h <= 0 {
			return
		}
		img := ui.Image(c, src).Size(w, h).Fit(ui.FillBox).Translate(px, py).
			Label(p.Alt).Tooltip(p.Alt)
		if viewerPanLimit(w, lay.stageW) > 0 || viewerPanLimit(h, lay.stageH) > 0 {
			if v.dragging {
				img.Cursor(ui.CursorGrabbing)
			} else {
				img.Cursor(ui.CursorGrab)
			}
		}
	})
}

// imageViewerFooter 是窗口底部的一行：上一次另存为的结果（成功给出文件名，
// 失败给出原因；到点自己撤掉），以及常用的操作提示。
//
// 要显示的状态先一次读出来（经锁），再进 Children 建元素。
func (a *app) imageViewerFooter(c *ui.Context, v *imageViewer, lay viewerLayout) {
	t := c.Theme()
	_, note := v.viewerSaveState()
	danger := strings.HasPrefix(note, "另存为失败")
	ui.Row(c).FillWidth().Height(lay.footerH).Shrink(0).
		Padding(0, 18).Gap(10).AlignItems(ui.Center).
		Background(t.Surface.Alpha(0.96)).Children(func() {
		if note != "" {
			ink := t.Text
			if danger {
				ink = t.Danger
			}
			ui.Text(c, note).FontSize(11).TextColor(ink).SingleLine().MaxWidthPercent(70)
		}
		ui.Spacer(c).Grow(1)
		ui.Text(c, imageViewerHint).FontSize(11).TextColor(t.TextMuted).SingleLine()
	})
}

// viewerPercent 把缩放写成百分比整数（「100%」），供工具条显示。
func viewerPercent(z float32) string {
	return fmt.Sprintf("%d%%", int(float32(0.5)+z*100))
}

// viewerLayout 是窗口里各块的位置与尺寸（窗口 DIP）。绘制与测试共用同一套
// 数：工具条在上、说明在下，中间全部留给舞台。
type viewerLayout struct {
	w, h     float32
	toolbarH float32
	stageW   float32
	stageH   float32
	footerH  float32
}

// viewerLayoutFor 按窗口尺寸算出窗口布局。窗口很矮时工具条与底栏各让出四分
// 之一，舞台至少留 1 DIP，不会算出负数。
func viewerLayoutFor(w, h float32) viewerLayout {
	toolbar := min(imageViewerToolbarH, h/4)
	footer := min(imageViewerFooterH, h/4)
	return viewerLayout{
		w: w, h: h,
		toolbarH: toolbar,
		stageW:   max(w-2*imageViewerPad, 1),
		stageH:   max(h-toolbar-footer-2*imageViewerPad, 1),
		footerH:  footer,
	}
}

// viewerImage 取一张预览图的图片源与尺寸。预览要放大到好几倍，所以先从磁盘
// 缓存的原图按「预览档」重新解码一次（见 imageCacheViewer），比正文里那张按
// 1600 像素上限缩小过的图清楚；缓存里还没有它时退回正文那一份，同时把原图
// 下载下来，到位后窗口自己换成清楚的。
func (a *app) viewerImage(raw string) (ui.ImageSource, float32, float32) {
	raw = strings.TrimSpace(raw)
	if src := a.images.source(viewerCacheKey(raw)); src != nil {
		w, h := ui.ImageSize(src)
		return src, w, h
	}
	if data, ok := a.images.readDisk(raw, imageCacheViewer); ok {
		if src, err := decodeImageData(raw, imageCacheViewer, data); err == nil {
			a.images.put(viewerCacheKey(raw), src)
			w, h := ui.ImageSize(src)
			return src, w, h
		}
	}
	// 原图不在本地：先显示正文那份（可能还没有），同时把原图取下来，到位后
	// 窗口自己换成清楚的（见 loadViewerImage）。
	a.loadViewerImage(raw)
	src := a.articleImage(raw, imageCacheTemporary)
	w, h := ui.ImageSize(src)
	return src, w, h
}

// loadViewerImage 在后台取一张配图的原图并解码成「预览档」，结果登记在
// viewerCacheKey 名下（与正文那张图分开）。预览档与正文档是同一个地址的两份
// 解码结果，所以这里自己登记、自己写盘，不复用 prefetchImage：那一份按地址
// 登记，正文已经有一份时会直接跳过，预览永远等不到清楚的图。
func (a *app) loadViewerImage(raw string) {
	if raw == "" || !a.autoLoad {
		return
	}
	key := viewerCacheKey(raw)
	if _, created := a.images.begin(key); !created {
		return // 已经取过，或正在取
	}
	gen := a.images.generation()
	go func() {
		e := a.images.get(key)
		if e == nil {
			return
		}
		data, ok := a.images.readDisk(raw, imageCacheViewer)
		if !ok {
			var err error
			if data, err = a.fetchImage(raw); err != nil {
				e.set(nil)
				a.publish(func() { a.redrawViewerWindow() })
				return
			}
			if err := a.images.writeDisk(raw, imageCacheViewer, gen, data); err != nil {
				log.Printf("图片缓存写入失败：%v", err)
			}
		}
		src, err := decodeImageData(raw, imageCacheViewer, data)
		if err != nil {
			e.set(nil)
			a.publish(func() { a.redrawViewerWindow() })
			return
		}
		e.set(src)
		a.publish(func() { a.redrawViewerWindow() })
	}()
}

// viewerCacheKey 是预览档解码结果在图片缓存里登记的名字：与正文那张图分开
// 两份，放大时用清楚的这一份，正文里按栏宽缩小仍用省内存的那一份。
func viewerCacheKey(raw string) string {
	return raw + "|preview"
}

/* ------------------------------------------------------------------
   另存为
--------------------------------------------------------------- */

// saveImage 把一张配图原样另存为文件：先弹系统「另存为」对话框问路径，再把
// 图取下来写进去（图片缓存在磁盘上有就直接用，没有就下载一份，同样走配置的
// 代理）。整个过程在后台，不挡界面；结果用 toast 报告，并在窗口底部留一句
// 说明。
func (a *app) saveImage(raw string) {
	v := a.viewer
	raw = strings.TrimSpace(raw)
	if v == nil || raw == "" {
		return
	}
	if _, err := parseImageURL(raw); err != nil {
		a.toast(toastViewer, "这张图的地址不能另存："+err.Error())
		return
	}
	v.savingMu.Lock()
	busy := v.saving != ""
	if !busy {
		v.saving = raw
		v.savedNote = ""
	}
	v.savingMu.Unlock()
	if busy {
		return
	}
	done := make(chan struct{})
	v.savingDone = done
	a.redrawViewerWindow()
	go func() {
		path, err := a.saveImageTo(raw)
		a.publish(func() {
			a.viewerSaved(path, err)
			close(done)
		})
	}()
}

// saveImageTo 是另存为的后台部分：问路径、取字节、写文件。用户取消时返回空
// 路径与 nil 错误，由调用方静默处理。先问路径再取字节：用户点「取消」时
// 既不该下载，也不该报一个他根本没关心的网络错误。
func (a *app) saveImageTo(raw string) (string, error) {
	path, err := a.pickImageSavePath(raw)
	if err != nil || path == "" {
		return "", err
	}
	data, err := a.imageBytesForSave(raw)
	if err != nil {
		return "", err
	}
	if err := writeImageFile(path, data); err != nil {
		return "", err
	}
	return path, nil
}

// viewerSaved 报告一次另存为的结果：成功与失败都用 toast，并在窗口底部留一句
// 说明。路径为空表示用户取消，不提示。只在主线程调用。
func (a *app) viewerSaved(path string, err error) {
	v := a.viewer
	text := ""
	switch {
	case err != nil:
		text = "另存为失败：" + friendlyError(err)
	case path != "":
		text = "已另存为 " + filepath.Base(path)
	default:
		if v != nil {
			v.savingMu.Lock()
			v.saving = ""
			v.savingMu.Unlock()
		}
		a.redrawViewerWindow()
		return
	}
	if v != nil {
		v.savingMu.Lock()
		v.saving = ""
		v.savedNote = text
		v.noteAt = nowTime()
		v.savingMu.Unlock()
	}
	a.toast(toastViewer, text)
	a.scheduleViewerNoteExpiry()
	a.redrawViewerWindow()
}

// viewerSaveState 在锁里取出另存为的显示状态：是否正在保存、要显示的结果说明
// （到点之后的说明不再画，见 scheduleViewerNoteExpiry）。
func (v *imageViewer) viewerSaveState() (saving bool, note string) {
	v.savingMu.Lock()
	defer v.savingMu.Unlock()
	if v.saving != "" {
		return true, imageViewerSaving
	}
	if v.savedNote != "" && nowTime().Sub(v.noteAt) > time.Duration(viewerNoticeSeconds)*time.Second {
		return false, ""
	}
	return false, v.savedNote
}

// clearNote 收掉上一次的另存为说明：换页、重开或关闭窗口时调用。
func (v *imageViewer) clearNote() {
	v.savingMu.Lock()
	v.savedNote = ""
	v.savingMu.Unlock()
}

// viewerNoteShown 报告底部此刻是否画着另存为的说明：说明到点自己撤掉时，靠它
// 判断要不要再排一次重画。
func (v *imageViewer) viewerNoteShown() bool {
	v.savingMu.Lock()
	defer v.savingMu.Unlock()
	return v.savedNote != ""
}

// scheduleViewerNoteExpiry 在结果说明出现后隔一会儿重画一次，让「已另存为
// …」到点自己撤掉。只在主线程调用；窗口已经关了就不再排。
func (a *app) scheduleViewerNoteExpiry() {
	if a.viewerNoteTimer != nil {
		a.viewerNoteTimer.Stop()
	}
	a.viewerNoteTimer = time.AfterFunc(viewerNoteInterval, func() {
		a.publish(func() {
			a.viewerNoteTimer = nil
			if v := a.viewer; v != nil && v.open && v.viewerNoteShown() {
				a.redrawViewerWindow()
			}
		})
	})
}

// imageBytesForSave 取一张图的原始字节：先读图片缓存（帖子配图在临时目录里，
// 头像与表情在长期缓存里），没有再下载一份。
func (a *app) imageBytesForSave(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	for _, cat := range []imageCategory{imageCacheTemporary, imageCacheStable} {
		if data, ok := a.images.readDisk(raw, cat); ok {
			return data, nil
		}
	}
	return a.fetchImage(raw)
}

// imageSaveFileName 从图片地址里取出另存为的默认文件名：去掉查询串后取最后
// 一段；没有像样的文件名时叫「图片」，没有扩展名时按地址猜一个。图片地址是
// 不透明标识，这里只借最后一段给用户一个好认的文件名，不推断图片归属。
func imageSaveFileName(raw string) string {
	name := ""
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil {
		name = strings.TrimSpace(filepath.Base(u.Path))
	}
	if name == "" || name == "." || name == "/" || name == ".." ||
		strings.ContainsAny(name, `/\:*?"<>|`) || strings.HasPrefix(name, ".") {
		return "图片" + imageSaveExtension(raw)
	}
	if strings.Contains(name, ".") {
		return name
	}
	return name + imageSaveExtension(raw)
}

// imageSaveExtension 按地址里出现的格式给一个扩展名，认不出按 PNG。
func imageSaveExtension(raw string) string {
	lower := strings.ToLower(raw)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp", ".avif"} {
		if strings.Contains(lower, ext) {
			return ext
		}
	}
	return ".png"
}

// pickImageSavePath 弹系统「另存为」对话框，返回用户选的路径；取消返回空串。
// 默认打开系统的下载目录，文件名取自地址（见 imageSaveFileName）。测试把
// saveImagePathHook 换成直接返回路径：无头环境里没有原生对话框。
func (a *app) pickImageSavePath(raw string) (string, error) {
	if a.saveImagePathHook != nil {
		return a.saveImagePathHook(raw)
	}
	name := imageSaveFileName(raw)
	def := name
	if dir, err := mygo.App.Path(mygo.PathDownloads); err == nil && dir != "" {
		def = filepath.Join(dir, name)
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = ".png"
	}
	return mygo.Dialog.Save(mygo.SaveDialogOptions{
		Title:       "另存为图片",
		DefaultPath: def,
		ButtonLabel: "保存",
		Filters:     []mygo.FileFilter{{Name: "图片", Extensions: []string{ext}}},
	})
}

// writeImageFile 把图片字节写到 path。先写同目录下的临时文件再改名：写到一半
// 出错不会留下半张图；目标已存在时（对话框里刚确认过覆盖）先删掉再改名，
// Windows 的改名不能覆盖已存在的文件。
func writeImageFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".t2-image-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

/* ------------------------------------------------------------------
   认组
--------------------------------------------------------------- */

// picturesForDoc 返回这份解析结果里的全部配图（见 articlePictures）。Markdown
// 视图逐块调用 Block，同一份文档会连着问很多次，所以记住「上一次问的那份」：
// 一段正文、一条评论各自连续建完，一次收图就够。缓存持有文档的指针，旧文档
// 不会被 GC 复用地址，不会把另一篇的图认成这一份。
func (a *app) picturesForDoc(d *parser.Document) []articlePicture {
	if d == nil {
		return nil
	}
	if a.viewerDoc == d {
		return a.viewerPics
	}
	pics := a.articlePictures(d)
	a.viewerDoc, a.viewerPics = d, pics
	return pics
}
