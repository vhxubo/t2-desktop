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

// 多图预览弹窗
//
// 详情页正文、每条评论（以及发帖预览、个人简介这类同样走 Markdown 的地方）
// 各自成组：点组里任意一张配图，弹窗打开这一组并停在它上面，可以左右翻页、
// 滚轮缩放、右键（或工具条按钮）另存为。两组互不影响——在某条评论的大图里
// 翻页只在这条评论自己的图之间走，不会串到正文或别的评论。
//
// 分组靠「从解析树认图」：article_render.go 的 articlePictures 按 Markdown
// 视图解析好的文档认出这一份源文里的全部配图，与 articleBlock 的显示规则
// 同源。正文、附言、个人简介各是独立的一份源文，评论一行一份，因此天然各自
// 成组；弹窗里翻的正是这一组，张数与画面里显示的一致。
//
// 弹窗画在可能显示它的每个窗口的 view 末尾（见 imageViewer），与授权弹窗同一
// 层：详情页、评论视图与各独立窗口都能弹出来。状态只在本文件维护；图还在加载
// 时点一下先记下，图片到位后的下一次重建自动打开（见 wantImage / viewerTick）。

// 预览弹窗的外观与交互参数。
const (
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

// 预览弹窗里给人看与给测试找的文案。
const (
	imageViewerHint    = "滚轮缩放 · 左右方向键翻页 · 右键另存为 · Esc 关闭"
	imageViewerPrev    = "上一张"
	imageViewerNext    = "下一张"
	imageViewerZoomIn  = "放大"
	imageViewerZoomOut = "缩小"
	imageViewerActual  = "实际大小"
	imageViewerSave    = "另存为"
	imageViewerClose   = "关闭预览"
	imageViewerLabel   = "图片预览"
	imageViewerSaving  = "正在保存…"
	imageViewerEmpty   = "图片还没加载好"
)

// viewerNoticeSeconds 是另存为结果说明在弹窗底部的停留时长，测试可以调短。
var viewerNoticeSeconds int64 = 6

// viewerNoteInterval 是结果说明出现后重新构建界面的间隔：说明到点自己撤掉，
// 不能等下一次输入才消失。测试把它调短。
var viewerNoteInterval = 2 * time.Second

// nowTime 是弹窗里计时用的时钟。测试可以把它换成假时钟，验证结果说明会自己
// 撤掉（见 image_viewer_test.go）。
var nowTime = time.Now

// imageViewerGroup 是弹窗正在看的那组图：按源文顺序排列的配图。正文的一段、
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

// imageViewer 是预览弹窗的状态。它挂在 app 上而不是某个页面：同一个详情页的
// 正文、评论视图与独立窗口都可能打开同一组图。
type imageViewer struct {
	open  bool
	group imageViewerGroup
	index int
	// zoom 是当前这张图的缩放，0 表示还没算过：第一次绘制按「刚好放下」算
	// （见 fitZoom）。1 是图片自身的像素尺寸。
	zoom float32
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
	// keys 是快捷键层（见 viewerKeyLayer）的身份：弹窗每帧重建，靠它把键盘
	// 焦点稳定地留在弹窗里，不落回页面的正文。
	keys ui.Handle
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
// 正文、评论还是窗口里都一样：组由调用方按源文认好，状态只在这里维护。
func (a *app) openImage(group imageViewerGroup, key string) {
	v := a.viewer
	if v == nil || len(group.Items) == 0 {
		return
	}
	// 已经在看同一组的同一张图：保持现状，缩放与翻页位置都不丢。
	if v.open && v.group.same(group) {
		if cur, ok := v.current(); ok && viewerKey(cur) == strings.TrimSpace(key) {
			v.want = nil
			return
		}
	}
	v.open = true
	v.group = group
	v.index = viewerIndexOf(group.Items, key)
	v.zoom = 0
	v.clearNote()
	v.want = nil
	a.invalidateViewerWindows()
}

// wantImage 记下「点了这张图，但它还在加载」：图片到位后的下一次重建补开
// 弹窗（见 viewerTick）。
func (a *app) wantImage(group imageViewerGroup, key string) {
	v := a.viewer
	if v == nil || len(group.Items) == 0 {
		return
	}
	v.want = &imageViewerWant{url: strings.TrimSpace(key), group: group}
}

// viewerTick 在图片到位后补开弹窗：点了的那张已经解出来，就打开它所在的组
// 并停在它上面；失败则忘掉这次点击，不弹一个加载不出来的空窗。每帧构建末尾
// 调用（见 imageViewer）。
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
// 第一张。换页后缩放回到「刚好放下」。
func (v *imageViewer) step(delta int) {
	if !v.canStep(delta) {
		return
	}
	v.index += delta
	v.zoom = 0
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

// close 关掉弹窗：缩放、结果说明、菜单与待打开的图都清掉。
func (v *imageViewer) close() {
	v.open = false
	v.zoom = 0
	v.clearNote()
	v.want = nil
	v.menuOpen = false
}

// startViewerClose 是「关闭」这个动作的统一入口：弹窗自己的按钮、右键菜单
// 与 Esc 都走它，关掉之后重画一次，让遮罩立刻消失。
func (a *app) startViewerClose() {
	if a.viewer == nil {
		return
	}
	a.viewer.close()
	a.invalidateViewerWindows()
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

// imageBounds 返回图片在窗口里的位置，按窗口尺寸、布局与缩放算：舞台在工具条
// 与说明之间居中，图片在舞台里居中。绘制按它摆图，右键菜单按它判断命中。
func imageBounds(lay viewerLayout, iw, ih, zoom float32) ui.Rect {
	w, h := iw*zoom, ih*zoom
	stageY := lay.toolbarH
	stageH := lay.h - lay.toolbarH - lay.footerH
	return ui.Rect{X: (lay.w - w) / 2, Y: stageY + (stageH-h)/2, W: w, H: h}
}

// overImage 报告窗口里的 (x, y) 是否落在当前图片上。
func (v *imageViewer) overImage(lay viewerLayout, iw, ih, zoom float32) bool {
	return imageBounds(lay, iw, ih, zoom).Contains(v.pointerX, v.pointerY)
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

// imageViewer 画出多图预览弹窗。每个可能显示它的窗口在 view 末尾调用它，
// 所以详情页、评论视图与各个独立窗口里都能弹出来；没有打开时什么都不画。
//
// 版式：铺满整扇窗口的半透明遮罩（点空白处关闭，和授权弹窗一样），顶部一条
// 工具条（第几张 / 共几张、翻页、缩放、另存为、关闭），中间是舞台里居中的
// 图片，底部一行说明。图片按 zoom 缩放后整体居中；放大到超出窗口时滚轮只
// 缩放不滚动，看局部靠缩放。
//
// 输入：
//   - 滚轮与右键由遮罩的 HandleInput 处理：滚轮只缩放，不再滚动背后的正文与
//     评论列表；右键落在图上时弹系统菜单（里面有「另存为」），位置记下来
//     （见 viewerInput）。
//   - 方向键 / PageUp / PageDown 翻页，加减号缩放，0 还原成原尺寸，
//     Ctrl+S（macOS 上 ⌘S）另存为。这些键登记在一个铺满遮罩、透传指针的透明
//     层上（见 viewerKeyLayer）：焦点落在页面里时也归弹窗，而工具条按钮照常
//     可点。
//   - Esc 交给遮罩的 OverlayShortcut，只关弹窗；弹窗开着时另外给窗口的 Esc
//     放一个空动作，免得它落到详情页的「Esc 关闭」上。
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
		// 弹窗会自己换成清楚的（见 viewerImage）。
		iw, ih = 1, 1
	}
	w, h := c.Size()
	lay := viewerLayoutFor(w, h)
	fit := fitZoom(iw, ih, lay.stageW, lay.stageH)
	zoom := v.zoom
	if zoom <= 0 {
		zoom = fit
	}

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
	// 键盘焦点留在弹窗里：点「下一张」这类按钮后，焦点不该掉回页面的正文，
	// 方向键与加号减号才有地方使。每帧重新聚焦，弹窗重建也不会丢。
	v.keys.Focus(c)
	// 指针到遮罩：滚轮在这里缩放，右键在这里记下位置（见 viewerInput）。
	back.HandleInput(func(ev ui.InputEvent) bool {
		return a.viewerInput(v, p, lay, iw, ih, zoom, fit, ev)
	})
	// 弹窗开着时给窗口的 Esc 放一个空动作：有它，Esc 才归遮罩的 OverlayShortcut
	// （见下），不会同时触发详情页的「Esc 关闭」。
	c.OnShortcut(0, ui.KeyEscape, func() {})
	// Esc 只关弹窗，点遮罩的空白处也一样。
	if back.OverlayShortcut(0, ui.KeyEscape) || back.Clicked() {
		v.close()
	}
}

// viewerKeyLayer 是弹窗里承载快捷键的一层：它先画、铺满整个弹窗，不画东西、
// 指针透传（PassThrough），所以点击与悬停照常落到随后画的工具条与图片上。
//
// 为什么不用遮罩自己的 HandleInput：HandleInput 只把键交给有焦点的元素和它周围
// 的元素；OnShortcut 则随「焦点与它周围的元素都没吃掉这个键」生效，弹窗开着时
// 这些键自然归它。这一层还被 Bind 成弹窗的持久身份，每帧由弹窗把焦点放在它
// 身上（见 imageViewer），焦点不会掉回页面的正文。
func (a *app) viewerKeyLayer(c *ui.Context, v *imageViewer, fit float32) {
	keys := ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().
		Background(ui.Transparent).PassThrough()
	step := func(d int) func() {
		return func() {
			v.step(d)
			a.invalidateViewerWindows()
		}
	}
	zoomStep := func(factor float32) func() {
		return func() {
			z := v.zoom
			if z <= 0 {
				z = fit
			}
			v.zoom = zoomBy(z, fit, factor)
			a.invalidateViewerWindows()
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
		a.invalidateViewerWindows()
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

// viewerInput 处理弹窗里的滚轮、触控板缩放与指针，返回是否吃掉了这个事件。
// 键盘见 viewerKeyLayer。
//
// 指针只记下位置：遮罩是最上层，落点本就到不了背后，但右键要按位置判断是不是
// 落在图上（见 overImage），记下来的位置留着给下一帧判断。
func (a *app) viewerInput(v *imageViewer, p articlePicture, lay viewerLayout, iw, ih, zoom, fit float32, ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputScroll:
		// 滚轮：向上放大、向下缩小。弹窗开着时滚轮只缩放，不再滚动背后的正文
		// 或评论列表。
		switch {
		case ev.DY < 0:
			v.zoom = zoomBy(zoom, fit, imageViewerWheel)
		case ev.DY > 0:
			v.zoom = zoomBy(zoom, fit, 1/imageViewerWheel)
		default:
			return false
		}
		a.invalidateViewerWindows()
		return true
	case ui.InputZoom:
		// 触控板的双指缩放：按它给的比例缩放。
		if ev.Scale <= 0 || ev.Scale == 1 {
			return false
		}
		v.zoom = zoomBy(zoom, fit, ev.Scale)
		a.invalidateViewerWindows()
		return true
	case ui.InputPointerMove, ui.InputPointerDown:
		v.pointerX, v.pointerY = ev.X, ev.Y
		if ev.Kind != ui.InputPointerDown {
			return false
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
	}
	return false
}

// imageViewerToolbar 是弹窗顶部的工具条：左边说明这是第几张、共几张与当前
// 缩放，右边是翻页、缩放、另存为与关闭。按钮都是图标 + tooltip。
func (a *app) imageViewerToolbar(c *ui.Context, v *imageViewer, lay viewerLayout, zoom, fit float32) {
	t := c.Theme()
	total := len(v.group.Items)
	// 正在另存为时按钮禁用；状态一次读出来，别在 Children 里再去碰（后台
	// 线程可能正好写回结果）。
	saving, _ := v.viewerSaveState()
	ui.Row(c).FillWidth().Height(lay.toolbarH).Shrink(0).
		Padding(0, 18).Gap(8).AlignItems(ui.Center).
		Background(t.Surface.Alpha(0.96)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).
		Children(func() {
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
			a.viewerButton(c, closeIcon, imageViewerClose, "关闭预览（Esc）", false, a.startViewerClose)
		})
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

// imageViewerStage 是图片舞台：铺满工具条与底栏之间，图片按 zoom 缩放后居中。
// 放大到超出舞台时仍居中显示，四周被裁掉（看局部靠缩放，不拖拽）。
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
		b := imageBounds(lay, iw, ih, zoom)
		w, h := b.W, b.H
		if w <= 0 || h <= 0 {
			return
		}
		ui.Image(c, src).Size(w, h).Fit(ui.FillBox).Label(p.Alt).Tooltip(p.Alt)
	})
}

// imageViewerFooter 是弹窗底部的一行：上一次另存为的结果（成功给出文件名，
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

// viewerLayout 是弹窗里各块的位置与尺寸（窗口 DIP）。绘制与测试共用同一套
// 数：工具条在上、说明在下，中间全部留给舞台。
type viewerLayout struct {
	w, h     float32
	toolbarH float32
	stageW   float32
	stageH   float32
	footerH  float32
}

// viewerLayoutFor 按窗口尺寸算出弹窗布局。窗口很矮时工具条与底栏各让出四分
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
// 下载下来，到位后弹窗自己换成清楚的。
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
	// 弹窗自己换成清楚的（见 loadViewerImage）。
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
				a.publish(func() { a.invalidateViewerWindows() })
				return
			}
			if err := a.images.writeDisk(raw, imageCacheViewer, gen, data); err != nil {
				log.Printf("图片缓存写入失败：%v", err)
			}
		}
		src, err := decodeImageData(raw, imageCacheViewer, data)
		if err != nil {
			e.set(nil)
			a.publish(func() { a.invalidateViewerWindows() })
			return
		}
		e.set(src)
		a.publish(func() { a.invalidateViewerWindows() })
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
// 代理）。整个过程在后台，不挡界面；结果用 toast 报告，并在弹窗底部留一句
// 说明。
func (a *app) saveImage(raw string) {
	v := a.viewer
	raw = strings.TrimSpace(raw)
	if v == nil || raw == "" {
		return
	}
	if _, err := parseImageURL(raw); err != nil {
		a.toast(toastMain, "这张图的地址不能另存："+err.Error())
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
	a.invalidateViewerWindows()
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

// viewerSaved 报告一次另存为的结果：成功与失败都用 toast，并在弹窗底部留一句
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
		a.invalidateViewerWindows()
		return
	}
	if v != nil {
		v.savingMu.Lock()
		v.saving = ""
		v.savedNote = text
		v.noteAt = nowTime()
		v.savingMu.Unlock()
	}
	a.toast(toastMain, text)
	a.scheduleViewerNoteExpiry()
	a.invalidateViewerWindows()
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

// clearNote 收掉上一次的另存为说明：换页、重开或关闭弹窗时调用。
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
// …」到点自己撤掉。只在主线程调用；弹窗已经关了就不再排。
func (a *app) scheduleViewerNoteExpiry() {
	if a.viewerNoteTimer != nil {
		a.viewerNoteTimer.Stop()
	}
	a.viewerNoteTimer = time.AfterFunc(viewerNoteInterval, func() {
		a.publish(func() {
			a.viewerNoteTimer = nil
			if v := a.viewer; v != nil && v.open && v.viewerNoteShown() {
				a.invalidateViewerWindows()
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
// 一段；没有像样的文件名时叫「图片」，没有扩展名时按地址猜一个。
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
   分发：把弹窗挂到可能显示它的窗口
--------------------------------------------------------------- */

// invalidateViewerWindows 让可能画着预览弹窗的窗口重画一次：主窗口（详情页的
// 正文与评论）、发帖、编辑与独立写评论窗口的预览区都会画它。
func (a *app) invalidateViewerWindows() {
	for _, win := range []*mygo.Window{a.win, a.composeWin, a.editWin, a.replyWin} {
		if win != nil {
			win.Invalidate()
		}
	}
}

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
