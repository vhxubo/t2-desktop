package main

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 多图预览窗口：正文与每条评论各自成组，组内左右翻页、滚轮缩放、拖动平移、
// 右键另存为；预览是一扇独立窗口，不内嵌在详情页里。

const viewerPicPrefix = "https://example.com/pics/"

// viewerZoom 返回窗口当前的缩放（0 表示还没算过）。只有测试从这里读，界面
// 自己按布局算，不需要这层访问器。
func viewerZoom(a *app) float32 { return a.viewer.zoom }

// viewerPanState 返回窗口当前的平移（窗口 DIP），测试用它断言拖动确实挪了图。
func viewerPanState(a *app) (float32, float32) { return a.viewer.panX, a.viewer.panY }

// viewerSquareBitmap 造一张透明的宽图，正中一块绿色方块：平移之后绿色方块的
// 位置跟着走，用 markerBounds 就能量出图片实际画在哪。
func viewerSquareBitmap(w, h, side int) *ui.Bitmap {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	x, y := (w-side)/2, (h-side)/2
	draw.Draw(img, image.Rect(x, y, x+side, y+side), &image.Uniform{C: marker}, image.Point{}, draw.Src)
	return ui.NewBitmap(img)
}

// viewerDetailApp 打开一篇带配图的详情：正文段落里两张图、附言里一张，前
// replyCount 条评论各带一组自己的三张图，用来验证「正文与单条评论各自独立
// 成组」。图片直接放进图片缓存，不联网。
func viewerDetailApp(t *testing.T, replyCount int) *app {
	t.Helper()
	a := testApp(t)
	a.openDetail(Item{ShortID: "VIEW001", URL: "https://2libra.com/post/work/VIEW001"})
	a.detail.topic = &topicDetail{
		ShortID: "VIEW001", Title: "多图预览", Author: "jimmy",
		Content:     "第一段 ![一](" + viewerPicPrefix + "p1.png)\n\n第二段 ![二](" + viewerPicPrefix + "p2.png)",
		URL:         "https://2libra.com/post/work/VIEW001",
		Postscripts: []postscript{{Content: "附言 ![三](" + viewerPicPrefix + "p3.png)"}},
	}
	for i := 0; i < replyCount; i++ {
		parts := make([]string, 0, 3)
		for k := 1; k <= 3; k++ {
			parts = append(parts, fmt.Sprintf("![r%d-%d](%sr%d-%d.png)", i+1, k, viewerPicPrefix, i+1, k))
		}
		a.detail.replyList = append(a.detail.replyList, replyItem{
			ID: fmt.Sprintf("c%d", i+1), Author: "dannyng", Floor: i + 1,
			Content: fmt.Sprintf("评论 %d 的配图：%s", i+1, strings.Join(parts, " ")),
		})
	}
	a.detail.replyTotal = replyCount
	a.detail.replyLoaded = true
	// 每张图都当作已经加载好：窗口里要立刻能翻、能缩、能存。
	a.images.put(viewerPicPrefix+"p1.png", solidBitmap(800, 400, marker))
	a.images.put(viewerPicPrefix+"p2.png", solidBitmap(800, 600, marker))
	a.images.put(viewerPicPrefix+"p3.png", solidBitmap(400, 800, marker))
	for i := 1; i <= replyCount; i++ {
		for k := 1; k <= 3; k++ {
			a.images.put(fmt.Sprintf("%sr%d-%d.png", viewerPicPrefix, i, k), solidBitmap(320, 240, marker))
		}
	}
	return a
}

// openViewer 在详情窗口里点一张配图，再为独立预览窗口建一个 tester 返回。
// 预览不再画在详情窗口里，所以两个窗口各用一个 tester。
func openViewer(t *testing.T, a *app, tt *ui.Tester, label string) *ui.Tester {
	t.Helper()
	tt.Frame()
	if err := tt.Click(label); err != nil {
		t.Fatalf("点开配图 %q 失败：%v（%q）", label, err, tt.Texts())
	}
	if !a.viewer.open {
		t.Fatalf("点了 %q 没有打开预览：%q", label, tt.Texts())
	}
	vt := newViewerTester(a)
	vt.Frame()
	if !vt.HasText(imageViewerHint) {
		t.Fatalf("预览窗口没有画出来：%q", vt.Texts())
	}
	return vt
}

// newViewerTester 是预览窗口自己的 tester：窗口内容只由 imageViewerWindowView
// 构建，与详情、发帖这些窗口无关。
func newViewerTester(a *app) *ui.Tester {
	return ui.NewTester(a.imageViewerWindowView, 1100, 760)
}

// viewerStatus 是工具条上「第几张 / 共几张」那一行的开头。
func viewerStatus(index, total int) string {
	return fmt.Sprintf("第 %d / %d 张 · ", index, total)
}

// viewerLine 返回工具条上「第 index / total 张 · …」那一行；对不上时返回空串。
func viewerLine(tt *ui.Tester, index, total int) string {
	for _, s := range tt.Texts() {
		if strings.HasPrefix(s, viewerStatus(index, total)) {
			return s
		}
	}
	return ""
}

// viewerZoomText 返回工具条上显示的缩放百分比（「100%」）；找不到时返回空串。
func viewerZoomText(tt *ui.Tester) string {
	for _, s := range tt.Texts() {
		if strings.HasPrefix(s, "第 ") && strings.Contains(s, " 张 · ") {
			if _, pct, ok := strings.Cut(s, " · "); ok {
				return pct
			}
		}
	}
	return ""
}

// 点正文段落的第二张图：预览停在第二张；方向键在正文这一段文字的图之间
// 翻页，到头不绕回，也不会翻到附言或评论的图上去。
func TestImageViewerPagesWithinGroup(t *testing.T) {
	a := viewerDetailApp(t, 1)
	tt := ui.NewTester(a.view, 1100, 760)
	vt := openViewer(t, a, tt, "二")
	if viewerLine(vt, 2, 2) == "" {
		t.Fatalf("预览没有停在点开的那张：%q", vt.Texts())
	}
	// 最后一张：下一张按钮禁用，按 → 也不动。
	if next, ok := vt.Access(imageViewerNext); !ok || !next.Disabled {
		t.Errorf("最后一页的「下一张」应禁用：%+v", next)
	}
	vt.Key(0, ui.KeyRight)
	vt.Frame()
	if viewerLine(vt, 2, 2) == "" {
		t.Errorf("到头后按 → 不该绕回第一张：%q", vt.Texts())
	}
	// 按 ← 回到第一张；第一张的「上一张」禁用。
	vt.Key(0, ui.KeyLeft)
	vt.Frame()
	if viewerLine(vt, 1, 2) == "" {
		t.Errorf("按 ← 没有回到上一张：%q", vt.Texts())
	}
	if prev, ok := vt.Access(imageViewerPrev); !ok || !prev.Disabled {
		t.Errorf("第一页的「上一张」应禁用：%+v", prev)
	}
	// 工具条上的「下一张」也能翻。
	if err := vt.Click(imageViewerNext); err != nil {
		t.Fatalf("点「下一张」失败：%v（%q）", err, vt.Texts())
	}
	vt.Frame()
	if viewerLine(vt, 2, 2) == "" {
		t.Errorf("点「下一张」没有翻过去：%q", vt.Texts())
	}
	// 一组几张看得出来：正文段落两张，不掺附言与评论。
	if vt.HasText(viewerStatus(2, 3)) || vt.HasText(viewerStatus(2, 6)) {
		t.Errorf("正文组的张数对不上：%q", vt.Texts())
	}
}

// 正文与单条评论各自独立成组：点评论里的图，只在这条评论自己的三张之间翻，
// 不掺正文与附言；关掉窗口再点附言，又是附言自己的一组。
func TestImageViewerCommentIsItsOwnGroup(t *testing.T) {
	a := viewerDetailApp(t, 1)
	tt := ui.NewTester(a.view, 1100, 760)
	vt := openViewer(t, a, tt, "r1-1")
	if viewerLine(vt, 1, 3) == "" {
		t.Fatalf("评论应是它自己的三张：%q", vt.Texts())
	}
	vt.Key(0, ui.KeyRight)
	vt.Frame()
	if viewerLine(vt, 2, 3) == "" {
		t.Errorf("评论组里没有翻到第二张：%q", vt.Texts())
	}
	vt.Key(0, ui.KeyLeft)
	vt.Frame()
	if viewerLine(vt, 1, 3) == "" {
		t.Errorf("评论组里没有翻回第一张：%q", vt.Texts())
	}
	// 附言是另一份源文，同样自成一组：只有它自己那一张。它排在正文末尾，
	// 先把左栏正文滚到底，等附言露出来再点。
	if err := vt.Click(imageViewerClose); err != nil {
		t.Fatalf("关不掉预览：%v（%q）", err, vt.Texts())
	}
	vt.Frame()
	if a.viewer.open || vt.HasText(imageViewerHint) {
		t.Fatalf("点关闭后预览窗口还在：%q", vt.Texts())
	}
	a.detail.bodyScroll.Y = math.MaxFloat32
	tt.Frame()
	if err := tt.Click("三"); err != nil {
		t.Fatalf("点开附言配图失败：%v（%q）", err, tt.Texts())
	}
	vt = newViewerTester(a)
	vt.Frame()
	if viewerLine(vt, 1, 1) == "" {
		t.Errorf("附言的组只有它自己那一张：%q", vt.Texts())
	}
}

// 预览是单独一扇窗口：详情窗口里不再叠一层遮罩，点开以后详情页照常能点；
// 在详情里点另一张图，预览窗口跟着换组。
func TestImageViewerIsSeparateWindow(t *testing.T) {
	a := viewerDetailApp(t, 0)
	tt := ui.NewTester(a.view, 1100, 760)
	vt := openViewer(t, a, tt, "一")
	tt.Frame()
	if tt.HasText(imageViewerHint) {
		t.Errorf("预览仍内嵌在详情窗口里：%q", tt.Texts())
	}
	// 独立窗口没有挡住详情页：点第二张会切到另一组。
	if err := tt.Click("二"); err != nil {
		t.Fatalf("预览开着时详情页点不动：%v（%q）", err, tt.Texts())
	}
	vt.Frame()
	if viewerLine(vt, 2, 2) == "" {
		t.Errorf("点详情里的另一张没有换组：%q", vt.Texts())
	}
	// 工具条上的最小化只收起窗口，不会顺手把它关掉。
	if err := vt.Click(imageViewerMinimize); err != nil {
		t.Fatalf("点「最小化」失败：%v（%q）", err, vt.Texts())
	}
	vt.Frame()
	if !vt.HasText(imageViewerHint) {
		t.Errorf("点最小化之后预览被关掉了：%q", vt.Texts())
	}
}

// 滚轮放大缩小：向上放大、向下缩小，工具条上的百分比跟着变；缩放有上下限。
func TestImageViewerWheelZooms(t *testing.T) {
	a := viewerDetailApp(t, 0)
	tt := ui.NewTester(a.view, 1100, 760)
	vt := openViewer(t, a, tt, "一")
	// 第一张 800×400 缩到 1:1；向下滚一格变成 0.8，再回来。
	vt.Wheel(550, 400, 0, -1)
	vt.Frame()
	if v := viewerZoom(a); v != imageViewerWheel {
		t.Errorf("滚轮向上没有放大：zoom = %v", v)
	}
	if got, want := viewerZoomText(vt), viewerPercent(imageViewerWheel); got != want {
		t.Errorf("工具条没有跟着显示 %s：%q", want, got)
	}
	vt.Wheel(550, 400, 0, 1)
	vt.Frame()
	if v := viewerZoom(a); v != 1 {
		t.Errorf("滚轮向下没有缩小：zoom = %v", v)
	}
	vt.Wheel(550, 400, 0, 1)
	vt.Frame()
	if v := viewerZoom(a); v != 1/imageViewerWheel {
		t.Errorf("连滚两格向下应缩到 %v：zoom = %v", 1/imageViewerWheel, v)
	}
	if a.detail.bodyScroll.Y != 0 {
		t.Errorf("预览窗口里的滚轮把正文滚走了：Y = %v", a.detail.bodyScroll.Y)
	}
	// 连放大到顶：夹在最大倍数上。
	for i := 0; i < 40; i++ {
		vt.Wheel(550, 400, 0, -1)
	}
	vt.Frame()
	if viewerZoom(a) != imageViewerMaxZoom {
		t.Errorf("放大到顶时 zoom = %v，想要 %v", viewerZoom(a), imageViewerMaxZoom)
	}
	if zoomIn, ok := vt.Access(imageViewerZoomIn); !ok || !zoomIn.Disabled {
		t.Errorf("放到最大时「放大」应禁用：%+v", zoomIn)
	}
	// 减号键缩小一点，0 键还原成原尺寸。
	vt.Key(0, ui.KeyMinus)
	vt.Frame()
	if viewerZoom(a) != imageViewerMaxZoom/imageViewerKeyZoom {
		t.Errorf("减号键没有缩小：zoom = %v", viewerZoom(a))
	}
	vt.Key(0, ui.Key0)
	vt.Frame()
	if got := viewerZoomText(vt); got != "100%" {
		t.Errorf("0 键没有还原成原尺寸：%q", got)
	}
	// 预览里的滚轮与按键不该动到详情窗口：关掉窗口，正文还在原处。
	if err := vt.Click(imageViewerClose); err != nil {
		t.Fatalf("关不掉预览：%v", err)
	}
	vt.Frame()
	tt.Frame()
	for _, want := range []string{"第一段", "第二段"} {
		if !tt.HasText(want) {
			t.Errorf("关掉预览后正文不见了：%q", want)
		}
	}
	if a.detail.bodyScroll.Y != 0 {
		t.Errorf("预览窗口里的滚轮把正文滚走了：Y = %v", a.detail.bodyScroll.Y)
	}
}

// 放大到超出舞台后，按住左键直接拖动图片：图跟着鼠标走，拖到边就停住（不会
// 露出空边），松手不关窗口；点图片也不关，点空白处才关。
func TestImageViewerDragsImage(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "VIEW004", URL: "https://2libra.com/post/work/VIEW004"})
	a.detail.replyLoaded = true
	raw := viewerPicPrefix + "wide.png"
	a.images.put(raw, viewerSquareBitmap(1600, 800, 120))
	a.viewer.group = imageViewerGroup{Items: []articlePicture{{URL: raw, Alt: "宽图"}}}
	a.viewer.open = true
	a.viewer.zoom = 1 // 原尺寸比 1100×760 的舞台大，才拖得动
	vt := newViewerTester(a)
	vt.Frame()

	before := markerBounds(vt.Image())
	vt.Press(550, 380)
	vt.Move(400, 350)
	vt.Release(400, 350)
	vt.Frame()
	if !vt.HasText(imageViewerHint) {
		t.Fatalf("拖动图片把预览关闭了：%q", vt.Texts())
	}
	if px, py := viewerPanState(a); px != -150 || py != -30 {
		t.Fatalf("拖动后平移 = (%v, %v)，想要 (-150, -30)", px, py)
	}
	after := markerBounds(vt.Image())
	if dx, dy := after.Min.X-before.Min.X, after.Min.Y-before.Min.Y; dx != -150 || dy != -30 {
		t.Errorf("图片跟着指针挪了 (%d, %d)，想要 (-150, -30)", dx, dy)
	}

	// 继续往右下猛拖：停在图片边缘贴住舞台的位置，不会拖出空边。
	vt.Press(550, 380)
	vt.Move(5000, 5000)
	vt.Release(5000, 5000)
	vt.Frame()
	lay := viewerLayoutFor(1100, 760)
	if px, py := viewerPanState(a); px != viewerPanLimit(1600, lay.stageW) || py != viewerPanLimit(800, lay.stageH) {
		t.Errorf("拖到边后平移 = (%v, %v)，想要 (%v, %v)",
			px, py, viewerPanLimit(1600, lay.stageW), viewerPanLimit(800, lay.stageH))
	}
	if !vt.HasText(imageViewerHint) {
		t.Errorf("拖到边之后预览被关掉了：%q", vt.Texts())
	}

	// 拖完点空白处照旧关闭。
	vt.ClickAt(4, 400)
	vt.Frame()
	if a.viewer.open || vt.HasText(imageViewerHint) {
		t.Errorf("拖动之后点空白处没有关掉预览：%q", vt.Texts())
	}
}

// 图片没放大到超出舞台时拖不动：没有可看的局部，拖动也不该把图挪歪。单击图片
// 本身只是结束这一次手势，不关窗口。
func TestImageViewerDragNeedsZoom(t *testing.T) {
	a := viewerDetailApp(t, 0)
	tt := ui.NewTester(a.view, 1100, 760)
	vt := openViewer(t, a, tt, "一")
	vt.Press(550, 380)
	vt.Move(400, 350)
	vt.Release(400, 350)
	vt.Frame()
	if px, py := viewerPanState(a); px != 0 || py != 0 {
		t.Errorf("没放大的图被拖动了：平移 = (%v, %v)", px, py)
	}
	vt.ClickAt(550, 380)
	vt.Frame()
	if !vt.HasText(imageViewerHint) {
		t.Errorf("点图片本身不该关掉预览：%q", vt.Texts())
	}
}

// Esc 只关预览窗口：不会顺手把详情页也关掉。
func TestImageViewerEscapeClosesViewerOnly(t *testing.T) {
	a := viewerDetailApp(t, 0)
	tt := ui.NewTester(a.view, 1100, 760)
	vt := openViewer(t, a, tt, "一")
	vt.Key(0, ui.KeyEscape)
	vt.Frame()
	if a.viewer.open || vt.HasText(imageViewerHint) {
		t.Fatalf("Esc 没有关掉预览：%q", vt.Texts())
	}
	if a.pageName != "detail" || a.detail == nil {
		t.Errorf("Esc 把详情页也关掉了：page=%q", a.pageName)
	}
	tt.Frame()
	if !tt.HasText("第一段") {
		t.Errorf("详情页正文不见了：%q", tt.Texts())
	}
}

// 点预览窗口的空白处也能关掉它。
func TestImageViewerBackdropCloses(t *testing.T) {
	a := viewerDetailApp(t, 0)
	tt := ui.NewTester(a.view, 1100, 760)
	vt := openViewer(t, a, tt, "一")
	vt.ClickAt(4, 400)
	vt.Frame()
	if a.viewer.open || vt.HasText(imageViewerHint) {
		t.Errorf("点空白处没有关掉预览：%q", vt.Texts())
	}
}

// 右键另存为：在图上按右键弹出菜单，菜单里有「另存为」，点了把原图写到选好
// 的路径，窗口底部报告结果，窗口自己留着。
func TestImageViewerRightClickSaveAs(t *testing.T) {
	a := viewerDetailApp(t, 0)
	raw := solidPNG(t, 40, 30, marker)
	if err := a.images.writeDisk(viewerPicPrefix+"p1.png", imageCacheTemporary, a.images.generation(), raw); err != nil {
		t.Fatalf("写图片缓存失败：%v", err)
	}
	target := filepath.Join(t.TempDir(), "保存的图.png")
	a.saveImagePathHook = func(got string) (string, error) {
		if got != viewerPicPrefix+"p1.png" {
			t.Errorf("另存为取错了图：%s", got)
		}
		return target, nil
	}
	tt := ui.NewTester(a.view, 1100, 760)
	vt := openViewer(t, a, tt, "一")
	// 图片 800×400 缩到 1:1 后比窗口还宽，正中一定落在图上；右键之前先挪到
	// 那里，预览按指针位置判断右键是不是落在图上。
	vt.Move(550, 380)
	vt.RightClickAt(550, 380)
	vt.Frame()
	if !vt.HasText(imageViewerSave) {
		t.Fatalf("图上右键没有弹出「另存为」菜单：%q", vt.Texts())
	}
	startViewerSave(t, vt)
	data := waitViewerSave(t, a, target)
	if !bytes.Equal(data, raw) {
		t.Errorf("写出来的不是原图：%d 字节", len(data))
	}
	vt.Frame()
	if !vt.HasText("已另存为 保存的图.png") {
		t.Errorf("没有说明另存结果：%q", vt.Texts())
	}
	if !vt.HasText(imageViewerHint) {
		t.Errorf("另存为之后预览不该自己关掉：%q", vt.Texts())
	}
}

// 在图片外面（图周围的空白、工具栏上）右键不弹「另存为」：那里没有图可存。
// 菜单一旦弹出就跟着指针走，所以每一种情况都开一个新的预览来验。
func TestImageViewerRightClickOffImageHasNoMenu(t *testing.T) {
	cases := []struct {
		name string
		x, y float32
	}{
		// 第一张 800×400 缩到 1:1 后居中：舞台上下各留出一截空白。
		{"图片下方的空白", 550, 700},
		{"工具栏上", 550, 8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// 预览窗口直接开着：点图片外面不该冒出一份「另存为」菜单。
			a := viewerDetailApp(t, 0)
			a.viewer.open = true
			a.viewer.group = imageViewerGroup{Items: []articlePicture{{URL: viewerPicPrefix + "p1.png", Alt: "一"}}}
			vt := newViewerTester(a)
			vt.Frame()
			if b := imageBounds(viewerLayoutFor(1100, 760), 800, 400, 1, 0, 0); b.Contains(c.x, c.y) {
				t.Fatalf("测试坐标 %v,%v 落在图片 %+v 里", c.x, c.y, b)
			}
			vt.RightClickAt(c.x, c.y)
			vt.Frame()
			if vt.Menu() != nil || a.viewer.menuOpen {
				t.Errorf("%s右键不该弹出「另存为」：%q", c.name, vt.Menu())
			}
		})
	}
}

// 另存为失败（写不进目标目录）时说清原因，不假装成功。
func TestImageViewerSaveAsFailureReports(t *testing.T) {
	a := viewerDetailApp(t, 0)
	// 原图先落进磁盘缓存：失败只来自「目标目录写不进去」。不落盘的话
	// saveImageTo 会真的去下载 example.com 那张图，网络一慢就撞上
	// waitViewerIdle 的 5 秒上限，和这里要验的写文件失败无关。
	raw := solidPNG(t, 40, 30, marker)
	if err := a.images.writeDisk(viewerPicPrefix+"p1.png", imageCacheTemporary, a.images.generation(), raw); err != nil {
		t.Fatalf("写图片缓存失败：%v", err)
	}
	a.saveImagePathHook = func(string) (string, error) {
		return filepath.Join(t.TempDir(), "没有这个目录", "x.png"), nil
	}
	tt := ui.NewTester(a.view, 1100, 760)
	vt := openViewer(t, a, tt, "一")
	startViewerSave(t, vt)
	waitViewerIdle(t, a)
	_, note := a.viewer.viewerSaveState()
	if !strings.HasPrefix(note, "另存为失败：") {
		t.Errorf("失败说明 = %q", note)
	}
	vt.Frame()
	if !vt.HasText(note) {
		t.Errorf("失败没有写在预览底部：%q", vt.Texts())
	}
}

// 用户取消另存为：不报错，也不说「已保存」。
func TestImageViewerSaveAsCanceledIsQuiet(t *testing.T) {
	a := viewerDetailApp(t, 0)
	a.saveImagePathHook = func(string) (string, error) { return "", nil }
	tt := ui.NewTester(a.view, 1100, 760)
	vt := openViewer(t, a, tt, "一")
	startViewerSave(t, vt)
	waitViewerIdle(t, a)
	if _, note := a.viewer.viewerSaveState(); note != "" {
		t.Errorf("取消之后不该留说明：%q", note)
	}
	vt.Frame()
	if vt.HasText(imageViewerSaving) || vt.HasText("另存为失败：") {
		t.Errorf("取消之后不该有提示：%q", vt.Texts())
	}
}

// 预览按原图重新解码：正文里那张按栏宽缩到 1600 像素，放大就糊；窗口自己再
// 解一份「预览档」，长边 2000 的图在窗口里仍是 2000。
func TestImageViewerDecodesOriginalForZoom(t *testing.T) {
	pixels := solidPNG(t, 2000, 1000, marker)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pixels)
	}))
	t.Cleanup(srv.Close)
	raw := srv.URL + "/big.png"

	a := testApp(t)
	a.openDetail(Item{ShortID: "VIEW003", URL: "https://2libra.com/post/work/VIEW003"})
	a.detail.topic = &topicDetail{
		ShortID: "VIEW003", Title: "大图", Author: "jimmy",
		Content: "看图：![" + raw + "](" + raw + ")", URL: "https://2libra.com/post/work/VIEW003",
	}
	a.detail.replyLoaded = true
	a.client.HTTP = srv.Client()
	a.autoLoad = true

	tt := ui.NewTester(a.view, 1100, 760)
	waitForMarker(t, tt, "正文配图")

	a.viewer.group = imageViewerGroup{Items: []articlePicture{{URL: raw, Alt: "大图"}}}
	a.viewer.open = true
	// 预览档由预览窗口的构建按需解码：循环里要重建的是它，不是详情窗口。
	vt := newViewerTester(a)
	deadline := time.Now().Add(5 * time.Second)
	for {
		src := a.images.source(viewerCacheKey(raw))
		if src != nil {
			if w, _ := ui.ImageSize(src); w != 2000 {
				t.Errorf("预览档的宽 = %v，想要原图的 2000", w)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("原图没有解成预览档")
		}
		vt.Frame()
		time.Sleep(10 * time.Millisecond)
	}
}

// 另存为的图不在缓存里时（还没下载过）照样能存：走配置的代理取一次字节。
func TestImageViewerSavesImageNotCached(t *testing.T) {
	raw := solidPNG(t, 12, 8, marker)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	url := srv.URL + "/not-cached.png"

	a := testApp(t)
	a.openDetail(Item{ShortID: "VIEW002", URL: "https://2libra.com/post/work/VIEW002"})
	a.detail.topic = &topicDetail{
		ShortID: "VIEW002", Title: "另存为", Author: "jimmy",
		Content: "看图：![" + url + "](" + url + ")", URL: "https://2libra.com/post/work/VIEW002",
	}
	a.detail.replyLoaded = true
	a.client.HTTP = srv.Client()
	target := filepath.Join(t.TempDir(), "远端的图.png")
	a.saveImagePathHook = func(string) (string, error) { return target, nil }

	a.viewer.group = imageViewerGroup{Items: []articlePicture{{URL: url, Alt: "远端的图"}}}
	a.viewer.open = true
	a.saveImage(url)
	if data := waitViewerSave(t, a, target); !bytes.Equal(data, raw) {
		t.Errorf("从网络取回的图不对：%d 字节", len(data))
	}
}

// 图片还没加载好时点一下：先记下，图片到位后自动打开预览，并停在点的那张。
func TestImageViewerOpensWhenPendingImageArrives(t *testing.T) {
	a := viewerDetailApp(t, 0)
	// 把第一张图从内存缓存里拿掉，并让它停在「正在加载」：界面先画占位。
	a.images.mu.Lock()
	delete(a.images.entries, viewerPicPrefix+"p1.png")
	a.images.mu.Unlock()
	if e, _ := a.images.begin(viewerPicPrefix + "p1.png"); e == nil {
		t.Fatal("没能在缓存里登记一张加载中的图")
	}
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText(imageLoadingText) {
		t.Fatalf("界面没有画加载中的占位：%q", tt.Texts())
	}
	if err := tt.Click("一"); err != nil {
		t.Fatalf("点加载中的占位失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if a.viewer.open || tt.HasText(imageViewerHint) {
		t.Fatalf("图片没到之前不该打开预览：%q", tt.Texts())
	}
	a.images.put(viewerPicPrefix+"p1.png", solidBitmap(800, 400, marker))
	tt.Frame()
	if !a.viewer.open {
		t.Fatalf("图片到位后没有自动打开预览")
	}
	vt := newViewerTester(a)
	vt.Frame()
	if !vt.HasText(imageViewerHint) {
		t.Fatalf("图片到位后预览窗口没有画出来：%q", vt.Texts())
	}
	if viewerLine(vt, 1, 2) == "" {
		t.Errorf("自动打开时应停在点的那张：%q", vt.Texts())
	}
}

// 另存为的结果提示留在预览窗口里：预览是独立窗口，不该把结果丢给可能被它
// 盖住的主窗口。
func TestViewerToastShowsInItsWindow(t *testing.T) {
	a := viewerDetailApp(t, 0)
	a.toast(toastViewer, "已另存为 图片.png")
	vt := newViewerTester(a)
	vt.Frame()
	if !vt.HasText("已另存为 图片.png") {
		t.Errorf("预览窗口没有显示自己的提示：%q", vt.Texts())
	}
	if len(a.toasts) != 0 {
		t.Errorf("提示显示后没有出队：%+v", a.toasts)
	}
}

// 另存为的默认文件名取自地址：带扩展名的原样用，没有扩展名的补一个。
func TestImageSaveFileName(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"https://img.example.com/a/photo.png", "photo.png"},
		{"https://img.example.com/a.png?x=1", "a.png"},
		{"https://img.example.com/", "图片.png"},
		{"https://img.example.com/view/1", "1.png"},
		{"https://img.example.com/some.webp?v=2", "some.webp"},
		{"https://img.example.com/%2F", "图片.png"},
	}
	for _, c := range cases {
		if got := imageSaveFileName(c.raw); got != c.want {
			t.Errorf("imageSaveFileName(%q) = %q，想要 %q", c.raw, got, c.want)
		}
	}
}

// startViewerSave 点工具条上的「另存为」。另存为在后台做，结果回到界面状态里；
// 想等它落定用 waitViewerIdle / waitViewerSave。
func startViewerSave(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click(imageViewerSave); err != nil {
		t.Fatalf("点「另存为」失败：%v（%q）", err, tt.Texts())
	}
}

// waitViewerSave 等到另存为结束、文件真的写出来，返回文件内容。
func waitViewerSave(t *testing.T, a *app, path string) []byte {
	t.Helper()
	waitViewerIdle(t, a)
	data, err := os.ReadFile(path)
	if err != nil {
		_, note := a.viewer.viewerSaveState()
		t.Fatalf("另存为结束=%q，但文件读不到：%v", note, err)
	}
	return data
}

// waitViewerIdle 等到这一次另存为结束（成功、失败或取消）：状态都写回界面之后，
// 后台那段才关掉 savingDone，所以等它就等于等结果落定，不会和 UI 线程抢着读写。
func waitViewerIdle(t *testing.T, a *app) {
	t.Helper()
	v := a.viewer
	select {
	case <-v.savingDone:
	case <-time.After(5 * time.Second):
		t.Fatalf("另存为没有结束")
	}
}
