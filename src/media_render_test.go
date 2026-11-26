package main

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 正文里的媒体嵌入：独占一段的视频链接画成封面卡片，裸图片地址按图片显示，
// 「默认不加载图片」时配图先给占位、点一下才下载。

// mediaDetailApp 打开一篇正文固定的详情。autoLoad 保持关闭：这些用例不发
// 真实请求，图片由测试直接放进缓存。
func mediaDetailApp(t *testing.T, content string) *app {
	t.Helper()
	a := testApp(t)
	a.openDetail(Item{ShortID: "MEDIA01", URL: "https://2libra.com/post/work/MEDIA01"})
	a.detail.topic = &topicDetail{
		ShortID: "MEDIA01", Title: "媒体嵌入", Author: "jimmy",
		Content: content, URL: "https://2libra.com/post/work/MEDIA01",
	}
	a.detail.replyLoaded = true
	return a
}

// 独占一段的 YouTube 链接画成卡片：写出来源与编号，点开预览后按播放交给
// 系统浏览器。
func TestArticleVideoLinkRendersCardAndPlays(t *testing.T) {
	const watch = "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
	a := mediaDetailApp(t, "看这个视频：\n\n"+watch+"\n\n结束")
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("YouTube · dQw4w9WgXcQ") {
		t.Fatalf("视频卡片没有写出来源与编号：%q", tt.Texts())
	}
	if !tt.HasText("结束") {
		t.Errorf("卡片后面的正文丢了：%q", tt.Texts())
	}
	if err := tt.Click("打开视频：YouTube · dQw4w9WgXcQ"); err != nil {
		t.Fatalf("点开视频卡片失败：%v（%q）", err, tt.Texts())
	}
	if !a.viewer.open {
		t.Fatalf("点视频卡片没有打开预览")
	}
	vt := newViewerTester(a)
	vt.Frame()
	if !vt.HasText(imageViewerVideoHint) {
		t.Fatalf("预览里没有视频说明：%q", vt.Texts())
	}
	if !vt.HasText("第 1 / 1 项 · YouTube") {
		t.Errorf("预览没有停在视频这一项：%q", vt.Texts())
	}
	if err := vt.Click(imageViewerPlay); err != nil {
		t.Fatalf("点「播放」失败：%v（%q）", err, vt.Texts())
	}
	if got := vt.OpenedURLs(); len(got) != 1 || got[0] != watch {
		t.Errorf("播放没有交给系统浏览器：%q", got)
	}
}

// 哔哩哔哩的 BV 号同样认成视频，播放页补成站内正式地址。
func TestArticleBilibiliLinkRendersCard(t *testing.T) {
	a := mediaDetailApp(t, "https://www.bilibili.com/video/BV1xx411c7mD?p=2")
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("哔哩哔哩 · BV1xx411c7mD") {
		t.Fatalf("B 站视频卡片没有写出来源与编号：%q", tt.Texts())
	}
	if err := tt.Click("打开视频：哔哩哔哩 · BV1xx411c7mD"); err != nil {
		t.Fatalf("点开视频卡片失败：%v（%q）", err, tt.Texts())
	}
	vt := newViewerTester(a)
	vt.Frame()
	if err := vt.Click(imageViewerPlay); err != nil {
		t.Fatalf("点「播放」失败：%v（%q）", err, vt.Texts())
	}
	if got := vt.OpenedURLs(); len(got) != 1 || got[0] != "https://www.bilibili.com/video/BV1xx411c7mD" {
		t.Errorf("播放没有打开 B 站播放页：%q", got)
	}
}

// 独占一段的裸图片地址按图片嵌入，和 ![]() 写法一样点得开预览。
func TestArticleBareImageURLRendersPicture(t *testing.T) {
	const pic = "https://img.example.com/a/photo.png"
	a := mediaDetailApp(t, pic)
	a.images.put(pic, solidBitmap(400, 300, marker))
	tt := ui.NewTester(a.view, 1100, 760)
	if n := countMarker(tt.Image()); n == 0 {
		t.Fatalf("裸图片地址没有画成图片：%q", tt.Texts())
	}
	if err := tt.Click("photo.png"); err != nil {
		t.Fatalf("点图片失败：%v（%q）", err, tt.Texts())
	}
	if !a.viewer.open {
		t.Fatalf("点裸地址嵌入的图片没有打开预览")
	}
}

// 视频与图片在同一条正文里时，预览组按源文顺序把两者都算上，翻页不会漏。
func TestArticleGroupIncludesVideoAndImages(t *testing.T) {
	const pic = "https://img.example.com/a/one.png"
	a := mediaDetailApp(t, "![一]("+pic+")\n\nhttps://youtu.be/dQw4w9WgXcQ\n\n![二](https://img.example.com/a/two.png)")
	a.images.put(pic, solidBitmap(400, 300, marker))
	a.images.put("https://img.example.com/a/two.png", solidBitmap(400, 300, marker))
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("一"); err != nil {
		t.Fatalf("点第一张图失败：%v（%q）", err, tt.Texts())
	}
	vt := newViewerTester(a)
	vt.Frame()
	if !vt.HasText("第 1 / 3 项 · ") {
		t.Fatalf("混排组没有把视频算进去：%q", vt.Texts())
	}
	vt.Key(0, ui.KeyRight)
	vt.Frame()
	if !vt.HasText("第 2 / 3 项 · YouTube") {
		t.Errorf("翻到视频那一项时没有写来源：%q", vt.Texts())
	}
	vt.Key(0, ui.KeyRight)
	vt.Frame()
	if !vt.HasText("第 3 / 3 项 · ") {
		t.Errorf("视频之后没有翻到第二张图：%q", vt.Texts())
	}
}

// 「默认不加载图片」：第一帧只有占位，不发请求；点一下才开始下载，图片到位
// 后占位换成图片。
func TestLazyImagesLoadOnClick(t *testing.T) {
	var hits atomic.Int32
	pixels := solidPNG(t, 400, 300, marker)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pixels)
	}))
	t.Cleanup(srv.Close)
	pic := srv.URL + "/pic.png"

	a := mediaDetailApp(t, "看图：\n\n![配图]("+pic+")")
	a.client.HTTP = srv.Client()
	a.autoLoad = true
	if err := a.imagePrefs.setLazy(true); err != nil {
		t.Fatalf("打开「默认不加载图片」失败：%v", err)
	}

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText(imageLazyText) {
		t.Fatalf("没有画占位：%q", tt.Texts())
	}
	if n := countMarker(tt.Image()); n != 0 {
		t.Errorf("占位阶段就画出了图片：%d 个绿色像素", n)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("点开之前就发了 %d 次请求", got)
	}
	if err := tt.Click("配图"); err != nil {
		t.Fatalf("点占位失败：%v（%q）", err, tt.Texts())
	}
	waitForMarker(t, tt, "点开后的配图")
	if got := hits.Load(); got == 0 {
		t.Errorf("点开之后没有下载图片")
	}
	tt.Frame()
	if tt.HasText(imageLazyText) {
		t.Errorf("图片到位后占位没有退场：%q", tt.Texts())
	}
}

// 关掉「默认不加载图片」后，同一篇正文照常自动加载。
func TestLazyImagesOffLoadsNormally(t *testing.T) {
	var hits atomic.Int32
	pixels := solidPNG(t, 400, 300, marker)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pixels)
	}))
	t.Cleanup(srv.Close)
	pic := srv.URL + "/pic.png"

	a := mediaDetailApp(t, "![配图]("+pic+")")
	a.client.HTTP = srv.Client()
	a.autoLoad = true
	tt := ui.NewTester(a.view, 1100, 760)
	waitForMarker(t, tt, "自动加载的配图")
	if tt.HasText(imageLazyText) {
		t.Errorf("默认设置下不该出现占位：%q", tt.Texts())
	}
	if hits.Load() == 0 {
		t.Errorf("默认设置下没有自动下载图片")
	}
}

// 图片设置落盘：重新打开应用还是同样的选择。
func TestImagePrefsPersist(t *testing.T) {
	dir := t.TempDir()
	s := newImagePrefsStore(dir)
	if s.lazyLoad() || !s.autoAnimate() {
		t.Fatalf("默认值不对：lazy=%v animate=%v", s.lazyLoad(), s.autoAnimate())
	}
	if err := s.setLazy(true); err != nil {
		t.Fatal(err)
	}
	if err := s.setAnimate(false); err != nil {
		t.Fatal(err)
	}
	again := newImagePrefsStore(dir)
	if !again.lazyLoad() || again.autoAnimate() {
		t.Errorf("重新读回的值不对：lazy=%v animate=%v", again.lazyLoad(), again.autoAnimate())
	}
}

// 设置页的「图片」一块：两枚开关都接上，改完立即落盘。
func TestImageSettingsToggles(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabImages
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, settingsWindowHeight)
	st.Frame()
	if !st.HasText("默认不加载图片") || !st.HasText("动图自动播放") {
		t.Fatalf("图片页缺少开关：%q", st.Texts())
	}
	if err := st.Click("默认不加载图片"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !a.imagePrefs.lazyLoad() {
		t.Errorf("点开关没有打开「默认不加载图片」")
	}
	if err := st.Click("动图自动播放"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.imagePrefs.autoAnimate() {
		t.Errorf("点开关没有关掉动图自动播放")
	}
	// 重新读一份配置，确认已经写盘。
	again := newImagePrefsStore(a.imagePrefs.dir)
	if !again.lazyLoad() || again.autoAnimate() {
		t.Errorf("设置没有落盘：lazy=%v animate=%v", again.lazyLoad(), again.autoAnimate())
	}
}

// 混排组的底栏说明按当前项切换：图片给缩放提示，视频给播放提示。
func TestViewerFooterFollowsCurrentItem(t *testing.T) {
	a := mediaDetailApp(t, "https://youtu.be/dQw4w9WgXcQ")
	a.viewer.group = imageViewerGroup{Items: []articlePicture{
		{URL: "https://youtu.be/dQw4w9WgXcQ", Alt: "YouTube", Kind: mediaVideo},
	}}
	a.viewer.open = true
	vt := newViewerTester(a)
	vt.Frame()
	if !vt.HasText(imageViewerVideoHint) {
		t.Errorf("视频项的底栏说明不对：%q", vt.Texts())
	}
	if strings.Contains(strings.Join(vt.Texts(), " "), imageViewerHint) {
		t.Errorf("视频项不该给缩放提示：%q", vt.Texts())
	}
}

// animatedGIF 造一张两帧的动图：两帧颜色不同，每帧 100ms。
func animatedGIF(t *testing.T) []byte {
	t.Helper()
	pal := color.Palette{color.RGBA{}, marker, color.RGBA{R: 220, A: 255}}
	g := &gif.GIF{}
	for i := 0; i < 2; i++ {
		frame := image.NewPaletted(image.Rect(0, 0, 8, 8), pal)
		for k := range frame.Pix {
			frame.Pix[k] = uint8(1 + i)
		}
		g.Image = append(g.Image, frame)
		g.Delay = append(g.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatalf("编码测试动图失败：%v", err)
	}
	return buf.Bytes()
}

// 预览里 gif 交给逐帧播放器；关掉「动图自动播放」后不再建播放器，静态图也
// 不会被当成动图。
func TestViewerPlaysAnimatedGIF(t *testing.T) {
	a := testApp(t)
	raw := "https://img.example.com/a/anim.gif"
	if err := a.images.writeDisk(raw, imageCacheTemporary, a.images.generation(), animatedGIF(t)); err != nil {
		t.Fatalf("写入动图缓存失败：%v", err)
	}
	a.images.put(raw, solidBitmap(8, 8, marker))
	a.viewer.group = imageViewerGroup{Items: []articlePicture{{URL: raw, Alt: "动图"}}}
	a.viewer.open = true

	vt := newViewerTester(a)
	vt.Frame()
	if pl := a.viewer.players[raw]; pl == nil {
		t.Fatalf("动图没有交给播放器：%v", a.viewer.players)
	}

	// 静态 PNG 不会被当成动图：认过一次就记下，不再重复解码。
	static := "https://img.example.com/a/still.png"
	if err := a.images.writeDisk(static, imageCacheTemporary, a.images.generation(),
		solidPNG(t, 8, 8, marker)); err != nil {
		t.Fatalf("写入静态图缓存失败：%v", err)
	}
	a.images.put(static, solidBitmap(8, 8, marker))
	a.viewer.group = imageViewerGroup{Items: []articlePicture{{URL: static, Alt: "静态"}}}
	vt.Frame()
	if _, checked := a.viewer.players[static]; !checked {
		t.Errorf("静态图没有记下「不是动图」，会每帧重新解码")
	} else if a.viewer.players[static] != nil {
		t.Errorf("静态图被当成了动图")
	}

	// 关掉动图自动播放：已有的播放器关掉，之后不再新建。
	if err := a.imagePrefs.setAnimate(false); err != nil {
		t.Fatal(err)
	}
	a.viewer.closePlayers()
	a.viewer.group = imageViewerGroup{Items: []articlePicture{{URL: raw, Alt: "动图"}}}
	vt.Frame()
	if pl := a.viewer.players[raw]; pl != nil {
		t.Errorf("关掉自动播放后仍在放动图")
	}
}

// 链接文字是人写的说明时用它当卡片名字，裸地址没有说明才退回来源 · 编号。
func TestArticleVideoUsesLinkLabel(t *testing.T) {
	a := mediaDetailApp(t, "[看这里](https://youtu.be/dQw4w9WgXcQ)")
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("看这里") {
		t.Fatalf("链接文字丢了：%q", tt.Texts())
	}
	if err := tt.Click("打开视频：看这里"); err != nil {
		t.Fatalf("点视频卡片失败：%v（%q）", err, tt.Texts())
	}
	vt := newViewerTester(a)
	vt.Frame()
	if !vt.HasText("第 1 / 1 项 · YouTube") {
		t.Errorf("预览没有停在视频项：%q", vt.Texts())
	}
}
