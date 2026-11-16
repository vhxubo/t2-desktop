package main

import (
	"bytes"
	"fmt"
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

// 多图预览弹窗：正文与每条评论各自成组，组内左右翻页、滚轮缩放、右键另存为。

const viewerPicPrefix = "https://example.com/pics/"

// viewerZoom 返回弹窗当前的缩放（0 表示还没算过）。只有测试从这里读，界面
// 自己按布局算，不需要这层访问器。
func viewerZoom(a *app) float32 { return a.viewer.zoom }

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
	// 每张图都当作已经加载好：弹窗里要立刻能翻、能缩、能存。
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

// openViewer 跑一帧让正文画出来，再点那张图打开预览。
func openViewer(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	tt.Frame()
	if err := tt.Click(label); err != nil {
		t.Fatalf("点开配图 %q 失败：%v（%q）", label, err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText(imageViewerHint) {
		t.Fatalf("点了 %q 没有弹出多图预览：%q", label, tt.Texts())
	}
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

// 点正文段落的第二张图：弹窗停在第二张；方向键在正文这一段文字的图之间
// 翻页，到头不绕回，也不会翻到附言或评论的图上去。
func TestImageViewerPagesWithinGroup(t *testing.T) {
	a := viewerDetailApp(t, 1)
	tt := ui.NewTester(a.view, 1100, 760)
	openViewer(t, tt, "二")
	if viewerLine(tt, 2, 2) == "" {
		t.Fatalf("弹窗没有停在点开的那张：%q", tt.Texts())
	}
	// 最后一张：下一张按钮禁用，按 → 也不动。
	if next, ok := tt.Access(imageViewerNext); !ok || !next.Disabled {
		t.Errorf("最后一页的「下一张」应禁用：%+v", next)
	}
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	if viewerLine(tt, 2, 2) == "" {
		t.Errorf("到头后按 → 不该绕回第一张：%q", tt.Texts())
	}
	// 按 ← 回到第一张；第一张的「上一张」禁用。
	tt.Key(0, ui.KeyLeft)
	tt.Frame()
	if viewerLine(tt, 1, 2) == "" {
		t.Errorf("按 ← 没有回到上一张：%q", tt.Texts())
	}
	if prev, ok := tt.Access(imageViewerPrev); !ok || !prev.Disabled {
		t.Errorf("第一页的「上一张」应禁用：%+v", prev)
	}
	// 工具条上的「下一张」也能翻。
	if err := tt.Click(imageViewerNext); err != nil {
		t.Fatalf("点「下一张」失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if viewerLine(tt, 2, 2) == "" {
		t.Errorf("点「下一张」没有翻过去：%q", tt.Texts())
	}
	// 一组几张看得出来：正文段落两张，不掺附言与评论。
	if tt.HasText(viewerStatus(2, 3)) || tt.HasText(viewerStatus(2, 6)) {
		t.Errorf("正文组的张数对不上：%q", tt.Texts())
	}
}

// 正文与单条评论各自独立成组：点评论里的图，只在这条评论自己的三张之间翻，
// 不掺正文与附言；关掉再点附言，又是附言自己的一组。
func TestImageViewerCommentIsItsOwnGroup(t *testing.T) {
	a := viewerDetailApp(t, 1)
	tt := ui.NewTester(a.view, 1100, 760)
	openViewer(t, tt, "r1-1")
	if viewerLine(tt, 1, 3) == "" {
		t.Fatalf("评论应是它自己的三张：%q", tt.Texts())
	}
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	if viewerLine(tt, 2, 3) == "" {
		t.Errorf("评论组里没有翻到第二张：%q", tt.Texts())
	}
	tt.Key(0, ui.KeyLeft)
	tt.Frame()
	if viewerLine(tt, 1, 3) == "" {
		t.Errorf("评论组里没有翻回第一张：%q", tt.Texts())
	}
	// 附言是另一份源文，同样自成一组：只有它自己那一张。它排在正文末尾，
	// 先把左栏正文滚到底，等附言露出来再点。
	if err := tt.Click(imageViewerClose); err != nil {
		t.Fatalf("关不掉预览：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if tt.HasText(imageViewerHint) {
		t.Fatalf("点关闭后弹窗还在：%q", tt.Texts())
	}
	a.detail.bodyScroll.Y = math.MaxFloat32
	tt.Frame()
	if err := tt.Click("三"); err != nil {
		t.Fatalf("点开附言配图失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if viewerLine(tt, 1, 1) == "" {
		t.Errorf("附言的组只有它自己那一张：%q", tt.Texts())
	}
}

// 弹窗开着时点遮罩下面的正文不会穿透：底下那张图不会因此又开一次、也不会
// 把弹窗换成别的组。
func TestImageViewerBlocksClicksBehind(t *testing.T) {
	a := viewerDetailApp(t, 0)
	tt := ui.NewTester(a.view, 1100, 760)
	openViewer(t, tt, "一")
	// 正文第二张大致在左栏中上位置；点它只会落到遮罩上，弹窗照旧停在第一张。
	if err := tt.Click("二"); err != nil {
		// 被遮罩挡住时 Click 会明确报错，这正是期望：正文不在可点范围内。
		if !strings.Contains(err.Error(), "not the input target") {
			t.Fatalf("点正文图的报错不符合预期：%v", err)
		}
	}
	tt.Frame()
	if viewerLine(tt, 1, 2) == "" {
		t.Errorf("遮罩下的点击把弹窗换组了：%q", tt.Texts())
	}
}

// 滚轮放大缩小：向上放大、向下缩小，工具条上的百分比跟着变；缩放有上下限。
func TestImageViewerWheelZooms(t *testing.T) {
	a := viewerDetailApp(t, 0)
	tt := ui.NewTester(a.view, 1100, 760)
	openViewer(t, tt, "一")
	// 第一张 800×400 缩到 1:1；向下滚一格变成 0.8，再回来。
	tt.Wheel(550, 400, 0, -1)
	tt.Frame()
	if v := viewerZoom(a); v != imageViewerWheel {
		t.Errorf("滚轮向上没有放大：zoom = %v", v)
	}
	if got, want := viewerZoomText(tt), viewerPercent(imageViewerWheel); got != want {
		t.Errorf("工具条没有跟着显示 %s：%q", want, got)
	}
	tt.Wheel(550, 400, 0, 1)
	tt.Frame()
	if v := viewerZoom(a); v != 1 {
		t.Errorf("滚轮向下没有缩小：zoom = %v", v)
	}
	tt.Wheel(550, 400, 0, 1)
	tt.Frame()
	if v := viewerZoom(a); v != 1/imageViewerWheel {
		t.Errorf("连滚两格向下应缩到 %v：zoom = %v", 1/imageViewerWheel, v)
	}
	if a.detail.bodyScroll.Y != 0 {
		t.Errorf("弹窗里的滚轮把正文滚走了：Y = %v", a.detail.bodyScroll.Y)
	}
	// 连放大到顶：夹在最大倍数上。
	for i := 0; i < 40; i++ {
		tt.Wheel(550, 400, 0, -1)
	}
	tt.Frame()
	if viewerZoom(a) != imageViewerMaxZoom {
		t.Errorf("放大到顶时 zoom = %v，想要 %v", viewerZoom(a), imageViewerMaxZoom)
	}
	if zoomIn, ok := tt.Access(imageViewerZoomIn); !ok || !zoomIn.Disabled {
		t.Errorf("放到最大时「放大」应禁用：%+v", zoomIn)
	}
	// 减号键缩小一点，0 键还原成原尺寸。
	tt.Key(0, ui.KeyMinus)
	tt.Frame()
	if viewerZoom(a) != imageViewerMaxZoom/imageViewerKeyZoom {
		t.Errorf("减号键没有缩小：zoom = %v", viewerZoom(a))
	}
	tt.Key(0, ui.Key0)
	tt.Frame()
	if got := viewerZoomText(tt); got != "100%" {
		t.Errorf("0 键没有还原成原尺寸：%q", got)
	}
	// 滚轮不会被弹窗吃掉之后又把下面的正文滚走：关掉弹窗，正文还在原处。
	if err := tt.Click(imageViewerClose); err != nil {
		t.Fatalf("关不掉预览：%v", err)
	}
	tt.Frame()
	for _, want := range []string{"第一段", "第二段"} {
		if !tt.HasText(want) {
			t.Errorf("关掉预览后正文不见了：%q", want)
		}
	}
	if a.detail.bodyScroll.Y != 0 {
		t.Errorf("弹窗里的滚轮把正文滚走了：Y = %v", a.detail.bodyScroll.Y)
	}
}

// Esc 只关弹窗：不会顺手把详情页也关掉。
func TestImageViewerEscapeClosesViewerOnly(t *testing.T) {
	a := viewerDetailApp(t, 0)
	tt := ui.NewTester(a.view, 1100, 760)
	openViewer(t, tt, "一")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if tt.HasText(imageViewerHint) {
		t.Fatalf("Esc 没有关掉预览：%q", tt.Texts())
	}
	if a.pageName != "detail" || a.detail == nil {
		t.Errorf("Esc 把详情页也关掉了：page=%q", a.pageName)
	}
	if !tt.HasText("第一段") {
		t.Errorf("详情页正文不见了：%q", tt.Texts())
	}
}

// 点遮罩的空白处也能关掉弹窗（和授权弹窗一样）。
func TestImageViewerBackdropCloses(t *testing.T) {
	a := viewerDetailApp(t, 0)
	tt := ui.NewTester(a.view, 1100, 760)
	openViewer(t, tt, "一")
	tt.ClickAt(4, 4)
	tt.Frame()
	if tt.HasText(imageViewerHint) {
		t.Errorf("点空白处没有关掉预览：%q", tt.Texts())
	}
}

// 右键另存为：在图上按右键弹出菜单，菜单里有「另存为」，点了把原图写到选好
// 的路径，弹窗底部报告结果，弹窗自己留着。
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
	openViewer(t, tt, "一")
	// 图片 800×400 缩到 1:1 后比窗口还宽，正中一定落在图上；右键之前先挪到
	// 那里，弹窗按指针位置判断右键是不是落在图上。
	tt.Move(550, 380)
	tt.RightClickAt(550, 380)
	tt.Frame()
	if !tt.HasText(imageViewerSave) {
		t.Fatalf("图上右键没有弹出「另存为」菜单：%q", tt.Texts())
	}
	startViewerSave(t, tt)
	data := waitViewerSave(t, a, target)
	if !bytes.Equal(data, raw) {
		t.Errorf("写出来的不是原图：%d 字节", len(data))
	}
	tt.Frame()
	if !tt.HasText("已另存为 保存的图.png") {
		t.Errorf("没有说明另存结果：%q", tt.Texts())
	}
	if !tt.HasText(imageViewerHint) {
		t.Errorf("另存为之后弹窗不该自己关掉：%q", tt.Texts())
	}
}

// 在图片外面（图周围的空白、工具栏上）右键不弹「另存为」：那里没有图可存。
// 菜单一旦弹出就跟着指针走，所以每一种情况都开一个新的弹窗来验。
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
			// 开着弹窗的前提下右键（弹窗本身在预览前就先画出来，不经过图片
			// 上的点击）：点图片外面不该冒出一份「另存为」菜单。
			a := viewerDetailApp(t, 0)
			a.viewer.open = true
			a.viewer.group = imageViewerGroup{Items: []articlePicture{{URL: viewerPicPrefix + "p1.png", Alt: "一"}}}
			tt := ui.NewTester(a.view, 1100, 760)
			tt.Frame()
			if b := imageBounds(viewerLayoutFor(1100, 760), 800, 400, 1); b.Contains(c.x, c.y) {
				t.Fatalf("测试坐标 %v,%v 落在图片 %+v 里", c.x, c.y, b)
			}
			tt.RightClickAt(c.x, c.y)
			tt.Frame()
			if tt.Menu() != nil || a.viewer.menuOpen {
				t.Errorf("%s右键不该弹出「另存为」：%q", c.name, tt.Menu())
			}
		})
	}
}

// 另存为失败（写不进目标目录）时说清原因，不假装成功。
func TestImageViewerSaveAsFailureReports(t *testing.T) {
	a := viewerDetailApp(t, 0)
	a.saveImagePathHook = func(string) (string, error) {
		return filepath.Join(t.TempDir(), "没有这个目录", "x.png"), nil
	}
	tt := ui.NewTester(a.view, 1100, 760)
	openViewer(t, tt, "一")
	startViewerSave(t, tt)
	waitViewerIdle(t, a)
	_, note := a.viewer.viewerSaveState()
	if !strings.HasPrefix(note, "另存为失败：") {
		t.Errorf("失败说明 = %q", note)
	}
	tt.Frame()
	if !tt.HasText(note) {
		t.Errorf("失败没有写在弹窗底部：%q", tt.Texts())
	}
}

// 用户取消另存为：不报错，也不说「已保存」。
func TestImageViewerSaveAsCanceledIsQuiet(t *testing.T) {
	a := viewerDetailApp(t, 0)
	a.saveImagePathHook = func(string) (string, error) { return "", nil }
	tt := ui.NewTester(a.view, 1100, 760)
	openViewer(t, tt, "一")
	startViewerSave(t, tt)
	waitViewerIdle(t, a)
	if _, note := a.viewer.viewerSaveState(); note != "" {
		t.Errorf("取消之后不该留说明：%q", note)
	}
	tt.Frame()
	if tt.HasText(imageViewerSaving) || tt.HasText("另存为失败：") {
		t.Errorf("取消之后不该有提示：%q", tt.Texts())
	}
}

// 预览按原图重新解码：正文里那张按栏宽缩到 1600 像素，放大就糊；弹窗自己再
// 解一份「预览档」，长边 2000 的图在弹窗里仍是 2000。
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
		tt.Frame()
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
	if tt.HasText(imageViewerHint) {
		t.Fatalf("图片没到之前不该弹窗：%q", tt.Texts())
	}
	a.images.put(viewerPicPrefix+"p1.png", solidBitmap(800, 400, marker))
	tt.Frame()
	if !tt.HasText(imageViewerHint) {
		t.Fatalf("图片到位后没有自动打开预览：%q", tt.Texts())
	}
	if viewerLine(tt, 1, 2) == "" {
		t.Errorf("自动打开时应停在点的那张：%q", tt.Texts())
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
