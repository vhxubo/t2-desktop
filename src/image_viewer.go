package main

import (
	"fmt"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
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
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
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
	imageViewerPlay     = "播放"
	imageViewerOpen     = "在浏览器中播放"
	imageViewerMinimize = "最小化"
	imageViewerClose    = "关闭预览"
	imageViewerLabel    = "媒体预览"
	imageViewerSaving   = "正在保存…"
	imageViewerEmpty    = "图片还没加载好"
	// imageViewerVideoHint 是视频那一项的底栏说明：正文与预览里都不内嵌
	// 播放器，播放交给系统浏览器（见 playVideo）。
	imageViewerVideoHint = "回车或点「播放」，在浏览器里打开这个视频"
)

// viewerNoticeSeconds 是另存为结果说明在窗口底部的停留时长，测试可以调短。
var viewerNoticeSeconds int64 = 6

// viewerNoteInterval 是结果说明出现后重新构建界面的间隔：说明到点自己撤掉，
// 不能等下一次输入才消失。测试把它调短。
var viewerNoteInterval = 2 * time.Second

// nowTime 是窗口里计时用的时钟。测试可以把它换成假时钟，验证结果说明会自己
// 撤掉（见 image_viewer_test.go）。
var nowTime = time.Now

// imageViewerGroup 是窗口正在看的那组媒体：按源文顺序排列的配图与视频。
// 正文的一段、附言、某条评论各是一组，组内翻页，组间互不影响。
type imageViewerGroup struct {
	Items []articlePicture
}

// mixed 报告这组里有没有视频：只有视频与图片混在一起时才用「项」计数，
// 整组都是图片仍按「张」说。
func (g imageViewerGroup) mixed() bool {
	for _, it := range g.Items {
		if it.Kind == mediaVideo {
			return true
		}
	}
	return false
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
	// players 是动图在预览里的播放器，按图片地址各一份：gif / apng 在舞台上
	// 按「动图自动播放」的设置播放，换图时暂停，关窗时关闭（见 viewerPlayer）。
	// 播放器解出来的像素尺寸记在 playerSizes 里，第一帧到达之前先按它排版。
	players     map[string]*ui.VideoPlayer
	playerSizes map[string][2]float32
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
	win := a.newAppWindow(mygo.WindowOptions{
		Title:             a.subWindowTitle(imageViewerLabel),
		Width:             imageViewerWindowWidth,
		Height:            imageViewerWindowHeight,
		MinWidth:          imageViewerWindowMinW,
		MinHeight:         imageViewerWindowMinH,
		StateKey:          "image-viewer",
		BackgroundColor:   hexToken(lobeStageBackground),
		TitleBarStyle:     mygo.TitleBarHidden, // 工具条兼作标题栏，见 imageViewerToolbar
		HideWindowButtons: true,
		Content:           ui.View(a.windowView(a.imageViewerWindowView)),
	}, toastViewer)
	a.viewerWin = win
	win.OnClosed(func() {
		a.viewerWin = nil
		a.authGateWindowClosed(toastViewer)
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
	// 预览窗口是一块固定的深色底（Lobe BgMask 遮罩 + 深色 BgContainer 舞台）。工具条、
	// 底栏和图标必须跟着这块底走深色 token：照搬应用的浅色主题会把工具条
	// 翻成一条浅色横幅，图标也跟着变成深色，和深色舞台上的大图格格不入。
	// 这里锁定 Lobe 的深色主题（仍出自 lobeCfg，Lobe 组件照常能取到匹配的
	// token），于是无论应用选浅色、深色还是跟随系统，预览窗口都保持同一套
	// 深色配色。主题要在读根背景之前定好，见 theme.go 的 applyTheme 约定。
	c.SetTheme(homeTheme(true))
	a.srv = c.Services()
	c.Root().Background(lobeStageBackground)
	defer a.drainToasts(c, a.claimsToasts(toastViewer))
	a.imageViewer(c)
	a.authDialogFor(c, toastViewer)
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
	v.pausePlayers()
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
	v.pausePlayers()
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

// close 关掉预览状态：缩放、平移、结果说明、菜单与待打开的图都清掉，动图的
// 播放器一并关闭——播放器在后台持有解码好的帧，窗口关掉之后不该留着。
func (v *imageViewer) close() {
	v.open = false
	v.zoom = 0
	v.panX, v.panY = 0, 0
	v.dragging, v.imagePress = false, false
	v.clearNote()
	v.want = nil
	v.menuOpen = false
	v.closePlayers()
}

// pausePlayers 暂停所有动图播放器：换图、翻页与重开时调用，舞台上只该有当前
// 这一张在动。记着 nil 的条目是「不是动图」，跳过。
func (v *imageViewer) pausePlayers() {
	for _, pl := range v.players {
		if pl != nil {
			pl.Pause()
		}
	}
}

// closePlayers 关闭并丢掉所有动图播放器：关窗与关掉「动图自动播放」时调用。
func (v *imageViewer) closePlayers() {
	for key, pl := range v.players {
		if pl != nil {
			pl.Close()
		}
		delete(v.players, key)
	}
	v.playerSizes = nil
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
	tk := lobetheme.TokensOf(c)
	v := a.viewer
	if v == nil || !v.open {
		return
	}
	p, ok := v.current()
	if !ok {
		a.startViewerClose()
		return
	}
	var src ui.ImageSource
	var pl *ui.VideoPlayer
	var iw, ih float32
	if p.Kind == mediaVideo {
		// 视频按 16:9 排：卡片自己按舞台算尺寸，缩放与平移对它没有意义。
		iw, ih = 16, 9
	} else {
		src, iw, ih = a.viewerImage(p.URL)
		// 只有图片已经解出来（字节也在磁盘上）才去认动图：没加载完时每帧
		// 摸一次磁盘没有意义。
		if src != nil {
			pl = a.viewerPlayer(v, p)
		}
		if pl != nil {
			// 动图按播放器解出来的像素尺寸排版；第一帧还没到时用静态那一份的
			// 尺寸，实在不知道才退回 1×1。
			if pw, ph := pl.Size(); pw > 0 && ph > 0 {
				iw, ih = float32(pw), float32(ph)
				v.playerSizes[p.URL] = [2]float32{iw, ih}
			} else if sz, ok := v.playerSizes[p.URL]; ok {
				iw, ih = sz[0], sz[1]
			}
			pl.Play()
		}
	}
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
		Background(tk.BgMask.Alpha(0.72)).Role(ui.RoleDialog).Label(imageViewerLabel)
	back.Modal()
	back.Children(func() {
		// 快捷键层先画，后面的工具条与图片盖在它上面；它透传指针，所以只占
		// 按键、不挡点击（见 viewerKeyLayer）。
		a.viewerKeyLayer(c, v, fit)
		ui.Column(c).FillWidth().FillHeight().Clip().Children(func() {
			a.imageViewerToolbar(c, v, p, lay, zoom, fit)
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
	// Ctrl+滚轮先交给整页缩放，普通滚轮与触控板双指缩放仍归图片预览。
	back.HandleInput(func(ev ui.InputEvent) bool {
		return a.zoomInput(ev) || a.viewerInput(v, p, lay, iw, ih, zoom, fit, ev)
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
	// 视频那一项：回车或空格在浏览器里播放（工具条上的「播放」是同一个动作）。
	play := func() {
		if p, ok := v.current(); ok && p.Kind == mediaVideo {
			a.playVideo(p.ref())
		}
	}
	keys.OnShortcut(0, ui.KeyEnter, play)
	keys.OnShortcut(0, ui.KeySpace, play)
	keys.OnShortcut(ui.Cmd, ui.KeyS, func() {
		// 另存为只对图片有意义：视频地址存下来只是一个网页。
		if p, ok := v.current(); ok && p.Kind == mediaImage {
			a.saveImage(p.URL)
		}
	})
	keys.Bind(&v.keys)
}

// imageViewerMenu 是媒体上的右键菜单：图片给「另存为」「实际大小」「关闭
// 预览」，视频给「在浏览器中播放」与「关闭预览」。一张藏在光标位置的定位块
// 负责弹出 Lobe 菜单。Ctrl+S 由 viewerKeyLayer 单独绑定，这里的 Hotkey 只
// 负责在菜单里显示。
func (a *app) imageViewerMenu(c *ui.Context, v *imageViewer, p articlePicture) {
	at := ui.Box(c).Absolute().Left(v.menuAt.X).Top(v.menuAt.Y).Size(1, 1)
	items := []lobeoverlay.MenuItem{
		{Key: "save", Label: imageViewerSave, Hotkey: "mod+s"},
		{Divider: true},
		{Key: "actual", Label: imageViewerActual},
		{Key: "close", Label: imageViewerClose},
	}
	if p.Kind == mediaVideo {
		items = []lobeoverlay.MenuItem{
			{Key: "play", Label: imageViewerOpen},
			{Divider: true},
			{Key: "close", Label: imageViewerClose},
		}
	}
	switch lobeoverlay.ContextMenu(c, at, lobeoverlay.DropdownMenuProps{Items: items}) {
	case "save":
		a.saveImage(p.URL)
	case "actual":
		v.zoom = 0
	case "play":
		a.playVideo(p.ref())
	case "close":
		a.startViewerClose()
	}
}

// viewerInput 处理窗口里的滚轮、触控板缩放与指针，返回是否吃掉了这个事件。
// 键盘见 viewerKeyLayer。
//
// 左键按在图上就开始拖动：吃掉这次按下，之后的移动只平移图片，松手时遮罩收到
// 的那一下点击也由 imagePress 认领，不会把窗口关掉。按在图上但没拖动就是一次
// 普通单击，同样不关窗口；点空白处才关（见 imageViewer）。右键按位置判断是不是
// 落在图上，记下来的位置留给下一帧弹菜单。
//
// 视频那一项没有缩放与平移：整个舞台都算「在图上」，左键交给卡片的点击（在
// 浏览器里播放），右键弹视频菜单。
func (a *app) viewerInput(v *imageViewer, p articlePicture, lay viewerLayout, iw, ih, zoom, fit float32, ev ui.InputEvent) bool {
	video := p.Kind == mediaVideo
	over := func() bool {
		if video {
			return viewerStageRect(lay).Contains(v.pointerX, v.pointerY)
		}
		return v.overImage(lay, iw, ih, zoom)
	}
	switch ev.Kind {
	case ui.InputScroll:
		if video {
			return false
		}
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
		if video {
			return false
		}
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
		if ev.Button == 0 && over() {
			v.imagePress = true
			// 视频卡片不拖动，只是别让这次点击被当成「点空白处关闭」。
			v.dragging = !video
			return true
		}
		// 右键（有的鼠标把副键报成中键）落在图上时弹菜单：位置先记下，菜单
		// 在下一帧按它弹出。之前那次移动可能没落到遮罩上（比如点开图片那
		// 一下），所以这里以按下时的位置为准；按在图上别处则收起旧菜单。
		switch {
		case over() && (ev.Button == 1 || ev.Button == 2):
			v.menuAt, v.menuOpen = ui.Point{X: ev.X, Y: ev.Y}, true
		case !over():
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

// imageViewerToolbar 是预览窗口顶部的工具条：左边说明这是第几项、共几项与
// 当前缩放，右边是翻页、缩放 / 播放、另存为、最小化与关闭。按钮都是图标 +
// tooltip。视频那一项没有缩放与另存为，换成「播放」。
// 工具条同时兼作自绘标题栏：空白处可以拖动窗口（双击最大化 / 还原）。
func (a *app) imageViewerToolbar(c *ui.Context, v *imageViewer, p articlePicture, lay viewerLayout, zoom, fit float32) {
	t := c.Theme()
	// 正在另存为时按钮禁用；状态一次读出来，别在 Children 里再去碰（后台
	// 线程可能正好写回结果）。
	saving, _ := v.viewerSaveState()
	video := p.Kind == mediaVideo
	ui.Row(c).FillWidth().Height(lay.toolbarH).Shrink(0).
		Padding(0, 18).Gap(8).AlignItems(ui.Center).
		Background(t.Surface.Alpha(0.96)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		DragWindow().Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			// 显示与读屏用同一句话：「第 2 / 3 张 · 100%」，视频项写来源。
			ui.Text(c, viewerStatusText(v, p, zoom)).
				FontSize(11).TextColor(t.TextMuted).SingleLine()
		})
		a.viewerButton(c, prevImageIcon, imageViewerPrev, "看上一张（←）", !v.canStep(-1), func() { v.step(-1) })
		a.viewerButton(c, nextImageIcon, imageViewerNext, "看下一张（→）", !v.canStep(1), func() { v.step(1) })
		ui.Box(c).Width(1).Height(18).Background(t.Border).Shrink(0)
		if video {
			ref := p.ref()
			a.viewerButton(c, playIcon, imageViewerPlay, imageViewerOpen, false, func() {
				a.playVideo(ref)
			})
		} else {
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
		}
		ui.Box(c).Width(1).Height(18).Background(t.Border).Shrink(0)
		a.viewerWindowButton(c, minimizeIcon, imageViewerMinimize, "把预览窗口最小化", a.minimizeViewerWindow)
		a.viewerCloseButton(c, closeIcon, imageViewerClose, "关闭预览（Esc）", a.startViewerClose)
	})
}

// viewerStatusText 是工具条左边那句位置说明：整组都是图片时按「张」计数并
// 带上缩放百分比，掺了视频时按「项」计数，视频项写来源。底栏的读屏文案与
// 测试都按这一句认位置。
func viewerStatusText(v *imageViewer, p articlePicture, zoom float32) string {
	total := len(v.group.Items)
	if p.Kind == mediaVideo {
		return fmt.Sprintf("第 %d / %d 项 · %s", v.index+1, total, p.ref().providerLabel())
	}
	if v.group.mixed() {
		return fmt.Sprintf("第 %d / %d 项 · %s", v.index+1, total, viewerPercent(zoom))
	}
	return fmt.Sprintf("第 %d / %d 张 · %s", v.index+1, total, viewerPercent(zoom))
}

// playVideo 播放一段站外视频：交给系统浏览器打开原页面。应用里不内嵌播放器
// ——没有 webview 页面，浏览器自己的代理设置也才能决定它怎么联网（与打开
// 普通外链同一条出口，见 AGENTS.md）。
func (a *app) playVideo(ref mediaRef) {
	url := strings.TrimSpace(ref.WatchURL)
	if url == "" {
		url = strings.TrimSpace(ref.URL)
	}
	if url == "" {
		return
	}
	a.openExternal(url)
}

// minimizeViewerWindow 最小化预览窗口：从任务栏或 Alt+Tab 可以再还原回来。
func (a *app) minimizeViewerWindow() {
	if win := a.viewerWin; win != nil {
		win.Minimize()
	}
}

// viewerButtonTone 区分工具条上图标的角色。工具条兼作窗口标题栏，操作键、
// 窗口控制键与关闭键不能一律同一个墨色，否则一眼看不出哪个是「关闭」。
type viewerButtonTone int

const (
	viewerToneAction viewerButtonTone = iota // 翻页、缩放、另存为：亮色图标，悬停品牌蓝
	viewerToneWindow                         // 最小化：常态弱一档，悬停中性浅底
	viewerToneClose                          // 关闭：悬停 Lobe 错误色红底白叉
)

// viewerButton 画工具条上的一枚操作图标：常态亮色、悬停给浅底并转成品牌蓝、
// 禁用时明显变淡。label 供读屏与测试识别，tip 是悬浮说明。
func (a *app) viewerButton(c *ui.Context, icon *ui.SVG, label, tip string, disabled bool, onClick func()) {
	a.viewerIconButton(c, icon, label, tip, disabled, viewerToneAction, onClick)
}

// viewerWindowButton 是「最小化」这类窗口控制键：常态比操作键弱一档，悬停
// 只给中性浅底，不与操作键的品牌蓝混淆。
func (a *app) viewerWindowButton(c *ui.Context, icon *ui.SVG, label, tip string, onClick func()) {
	a.viewerIconButton(c, icon, label, tip, false, viewerToneWindow, onClick)
}

// viewerCloseButton 是「关闭」键：和标题栏关闭键一样，悬停时红底白叉。
func (a *app) viewerCloseButton(c *ui.Context, icon *ui.SVG, label, tip string, onClick func()) {
	a.viewerIconButton(c, icon, label, tip, false, viewerToneClose, onClick)
}

// viewerIconButton 是三类工具条图标按钮的共同画法：底色与图标颜色按角色和
// 状态分别给，保证常态、悬停、禁用、窗口控制与关闭各有可辨的颜色。
func (a *app) viewerIconButton(c *ui.Context, icon *ui.SVG, label, tip string, disabled bool, tone viewerButtonTone, onClick func()) {
	t := c.Theme()
	tk := lobetheme.TokensOf(c)
	b := ui.Box(c).Size(imageViewerButton, imageViewerButton).Radius(8).Shrink(0).Center().
		Background(ui.Transparent)
	hovered := b.Hovered()
	closeHover := tone == viewerToneClose && hovered && !disabled
	switch {
	case disabled:
		b.Disabled(true)
	case closeHover:
		b.Background(tk.Error) // 关闭：Lobe 错误色红底白叉，与标题栏一致
	case hovered:
		b.Background(t.SurfaceHover)
	}
	var ink ui.Color
	switch {
	case disabled:
		ink = t.TextMuted.Alpha(0.45)
	case closeHover:
		ink = tk.White
	case tone == viewerToneWindow:
		ink = t.TextMuted
		if hovered {
			ink = t.Text
		}
	case hovered:
		ink = t.Accent
	default:
		ink = t.Text
	}
	b.Children(func() {
		ui.Icon(c, icon).Size(17, 17).TextColor(ink)
	})
	b.Label(label).Tooltip(tip).OnClick(onClick)
}

// imageViewerStage 是媒体舞台：铺满工具条与底栏之间。图片按 zoom 缩放后居中，
// 再按 panX / panY 平移；放大到超出舞台时四周被裁掉，按住左键拖动可以挪着看，
// 指针停在图上时给出抓手光标。动图（gif / apng）在「动图自动播放」打开时用
// 播放器逐帧播放，否则只显示首帧。视频是一张封面卡片，点一下在浏览器里播放。
// 图还没出来时空态分加载中与失败：失败时整块可点，重新下载这张图。
func (a *app) imageViewerStage(c *ui.Context, v *imageViewer, p articlePicture, src ui.ImageSource, iw, ih float32, lay viewerLayout, zoom float32) {
	t := c.Theme()
	stage := ui.Box(c).Grow(1).FillWidth().MinHeight(0).Center().Clip()
	if p.Kind == mediaVideo {
		stage.Children(func() { a.imageViewerVideoStage(c, p, lay) })
		return
	}
	pl := a.viewerPlayer(v, p)
	// 空态分两种：还在下载时转圈，已经失败时给出可点的重试。预览档与正文档
	// 任有一张失败都算失败——它们都取不到图时舞台才空着（见 viewerImage）。
	pending := src == nil && pl == nil
	failed := pending && (a.images.state(viewerCacheKey(p.URL)) == imageFailed ||
		a.images.state(p.URL) == imageFailed)
	if failed {
		stage.Cursor(ui.CursorPointer).OnClick(func() { a.retryViewerImage(p.URL) })
	}
	stage.Children(func() {
		if pending {
			if failed {
				ui.Column(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, imageFailedText).FontSize(13).TextColor(t.TextMuted)
					ui.Text(c, imageRetryText).FontSize(12).TextColor(t.TextMuted)
				})
			} else {
				ui.Column(c).Gap(8).AlignItems(ui.Center).Children(func() {
					lobeSpin(c, feedback.SpinProps{CustomSize: 20}).Label("图片加载中")
					ui.Text(c, imageViewerEmpty).FontSize(13).TextColor(t.TextMuted)
				})
			}
			return
		}
		px, py := viewerPan(v.panX, v.panY, iw, ih, zoom, lay)
		b := imageBounds(lay, iw, ih, zoom, px, py)
		w, h := b.W, b.H
		if w <= 0 || h <= 0 {
			return
		}
		var img ui.Element
		if pl != nil {
			img = ui.Video(c, pl).Size(w, h).Fit(ui.Contain).Translate(px, py).
				Label(p.Alt).Tooltip(p.Alt)
		} else {
			img = ui.Image(c, src).Size(w, h).Fit(ui.FillBox).Translate(px, py).
				Label(p.Alt).Tooltip(p.Alt)
		}
		if viewerPanLimit(w, lay.stageW) > 0 || viewerPanLimit(h, lay.stageH) > 0 {
			if v.dragging {
				img.Cursor(ui.CursorGrabbing)
			} else {
				img.Cursor(ui.CursorGrab)
			}
		}
	})
}

// imageViewerVideoStage 在舞台里画视频卡片：按 16:9 放进可用区域，最多 720
// 宽，居中显示封面与播放按钮。点卡片在浏览器里打开这个视频（见 playVideo）。
func (a *app) imageViewerVideoStage(c *ui.Context, p articlePicture, lay viewerLayout) {
	ref := p.ref()
	title := ref.title()
	w := min(lay.stageW, 720)
	h := w * 9 / 16
	if h > lay.stageH {
		h = lay.stageH
		w = h * 16 / 9
	}
	card := ui.Box(c).Size(w, h).Radius(10).Background(videoCardBG).Clip().Center()
	card.Label("打开视频：" + title).Tooltip(imageViewerOpen)
	card.OnClick(func() { a.playVideo(ref) })
	card.Children(func() { a.videoCover(c, ref, title, true) })
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
		hint := imageViewerHint
		if p, ok := v.current(); ok && p.Kind == mediaVideo {
			hint = imageViewerVideoHint
		}
		ui.Text(c, hint).FontSize(11).TextColor(t.TextMuted).SingleLine()
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
	a.loadViewerImageFor(raw, false)
}

// loadViewerImageFor 是预览档取图的实现：force 为真时调用方已经把失败项复位
// （见 retryViewerImage），这里直接启动下载；否则由 begin 保证同一地址只下
// 一次。用户显式点重试不受 autoLoad 限制。
func (a *app) loadViewerImageFor(raw string, force bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	key := viewerCacheKey(raw)
	if !force {
		if _, created := a.images.begin(key); !created {
			return // 已经取过，或正在取
		}
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
				a.redrawViewerWindow()
				return
			}
			if err := a.images.writeDisk(raw, imageCacheViewer, gen, data); err != nil {
				log.Printf("图片缓存写入失败：%v", err)
			}
		}
		src, err := decodeImageData(raw, imageCacheViewer, data)
		if err != nil {
			e.set(nil)
			a.redrawViewerWindow()
			return
		}
		e.set(src)
		a.redrawViewerWindow()
	}()
}

// retryViewerImage 是预览空态点「重试」的动作：预览档自己走后台下载，先用
// restartFailed 清掉失败态再启动；正文那一份也一起重试，预览取不到时仍能
// 退回按栏宽解出的图。最后重建界面，让空态立刻换成加载中。
func (a *app) retryViewerImage(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	restarted := a.images.restartFailed(viewerCacheKey(raw))
	a.images.retry(raw, imageCacheTemporary, a.imageFetcher())
	if restarted {
		a.loadViewerImageFor(raw, true)
	}
	a.publish(func() {})
}

// viewerCacheKey 是预览档解码结果在图片缓存里登记的名字：与正文那张图分开
// 两份，放大时用清楚的这一份，正文里按栏宽缩小仍用省内存的那一份。
func viewerCacheKey(raw string) string {
	return raw + "|preview"
}

// viewerPlayer 返回一张动图在预览里的播放器：gif 与 apng 交给 mygo 自带的
// 解码器逐帧播放，其余格式（含只有一帧的 gif）返回 nil，按普通图片画。
// 「动图自动播放」关掉时同样返回 nil。
//
// 播放器按地址缓存在预览状态里：同一张图来回翻不会反复解码；换图时暂停、
// 关窗时关闭（见 pausePlayers / closePlayers）。原始字节取自磁盘缓存，正文
// 与预览两档都看——同一张图的字节只有一份（见 imageCacheViewer）。
func (a *app) viewerPlayer(v *imageViewer, p articlePicture) *ui.VideoPlayer {
	if v == nil || p.Kind != mediaImage || !a.imagePrefs.autoAnimate() {
		return nil
	}
	raw := strings.TrimSpace(p.URL)
	if pl, checked := v.players[raw]; checked {
		// 记着 nil 的是「看过了，不是动图」：静态图不必每帧重新解码一遍。
		return pl
	}
	data, ok := a.images.readDisk(raw, imageCacheViewer)
	if !ok {
		data, ok = a.images.readDisk(raw, imageCacheTemporary)
	}
	if !ok {
		return nil
	}
	dec, ok := animatedDecoder(data)
	if !ok {
		if v.players == nil {
			v.players = make(map[string]*ui.VideoPlayer, 1)
		}
		v.players[raw] = nil
		return nil
	}
	pl, err := ui.NewVideoPlayer(dec)
	if err != nil {
		return nil
	}
	pl.SetLoop(true)
	if v.players == nil {
		v.players = make(map[string]*ui.VideoPlayer, 1)
	}
	if v.playerSizes == nil {
		v.playerSizes = make(map[string][2]float32, 1)
	}
	v.players[raw] = pl
	return pl
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
// 既不该下载，也不该报一个ta根本没关心的网络错误。
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
