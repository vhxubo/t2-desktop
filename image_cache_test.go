package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// waitForSource 等一张图从缓存里就位；无头测试里下载协程通过 publish 直接
// 回填，主协程轮询即可。
func waitForSource(t *testing.T, a *app, raw string) ui.ImageSource {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if src := a.images.source(raw); src != nil {
			return src
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s 没有在 3 秒内加载完成", raw)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitForDisk 等下载协程把文件写进缓存目录：测试的临时目录要等后台写完
// 才能删干净，也顺带验证了「下载后会落盘」。
func waitForDisk(t *testing.T, c *imageCache, raw string, cat imageCategory) {
	t.Helper()
	path := filepath.Join(c.categoryDir(cat), imageKey(raw))
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s 没有写进磁盘缓存", raw)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func imageTestServer(t *testing.T, hits *int32) (*httptest.Server, []byte) {
	t.Helper()
	pixels := solidPNG(t, 8, 8, marker)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pixels)
	}))
	t.Cleanup(srv.Close)
	return srv, pixels
}

// 缓存项要能区分「还在加载」「已解码」「失败」：界面据此决定转圈还是给说明，
// 三种状态都不会让同一地址重复下载。
func TestImageCacheTracksLoadState(t *testing.T) {
	c := newImageCache(t.TempDir())
	u := "https://example.com/pic.png"
	if got := c.state(u); got != imageIdle {
		t.Errorf("没登记过的地址状态 = %v，期望 imageIdle", got)
	}
	e, created := c.begin(u)
	if !created {
		t.Fatal("第一次登记没有返回 created")
	}
	if got := c.state(u); got != imageLoading {
		t.Errorf("刚登记的状态 = %v，期望 imageLoading", got)
	}
	src := solidBitmap(8, 8, marker)
	e.set(src)
	if got := c.state(u); got != imageLoaded || c.source(u) != src {
		t.Errorf("解码成功后状态 = %v，source 是否就位 = %v", got, c.source(u) == src)
	}
	e.set(nil)
	if got := c.state(u); got != imageFailed {
		t.Errorf("加载失败后状态 = %v，期望 imageFailed", got)
	}
	// 状态查询按 trim 后的地址命中，与界面登记缓存项时用的键一致。
	if got := c.state("  " + u + "  "); got != imageFailed {
		t.Errorf("带空白的地址状态 = %v，没有命中同一项", got)
	}
}

// 失败项不是永久空白：冷却过后允许有限次重试，成功后清零，超过上限才停。
// 测试直接把 failedAt 往前调，免去等待真实冷却时间。
func TestImageCacheRetriesFailedItemWithLimit(t *testing.T) {
	c := newImageCache(t.TempDir())
	u := "https://example.com/avatar.png"
	backdate := func() {
		e := c.get(u)
		if e == nil {
			t.Fatal("缓存项不存在")
		}
		e.mu.Lock()
		e.failedAt = time.Now().Add(-imageRetryCooldown - time.Second)
		e.mu.Unlock()
	}

	e, created := c.begin(u)
	if !created {
		t.Fatal("第一次登记没有返回 created")
	}

	// 首次失败后，冷却期内不重试。
	e.set(nil)
	if got := c.state(u); got != imageFailed {
		t.Fatalf("失败后状态 = %v，期望 imageFailed", got)
	}
	if _, retry := c.begin(u); retry {
		t.Error("冷却期内不该重试")
	}

	// 冷却过后允许重试，每次失败后再等冷却又能重试，直到用满 imageRetryLimit 次。
	for i := 0; i < imageRetryLimit; i++ {
		backdate()
		if _, retry := c.begin(u); !retry {
			t.Fatalf("第 %d 次重试机会没有给出（已失败 %d 次）", i+1, i+1)
		}
		e = c.get(u)
		e.set(nil)
	}
	backdate()
	if _, retry := c.begin(u); retry {
		t.Errorf("已重试 %d 次后再不该重试", imageRetryLimit)
	}

	// 成功会把失败计数清零，之后的新失败重新开始计重试额度。
	e = c.get(u)
	e.set(solidBitmap(8, 8, marker))
	if e.failures != 0 {
		t.Errorf("成功后失败次数没有清零：%d", e.failures)
	}
	e.set(nil)
	if e.failures != 1 {
		t.Errorf("重新失败后次数应为 1，实际 %d", e.failures)
	}
	backdate()
	if _, retry := c.begin(u); !retry {
		t.Error("清零后的新失败应当重新获得重试机会")
	}
}

// 头像、表情和帖子配图共用一份全局缓存：同一个地址不管从哪条路径来，
// 都只下载一次。
func TestGlobalImageCacheDownloadsEachURLOnce(t *testing.T) {
	var hits int32
	srv, _ := imageTestServer(t, &hits)
	a := testApp(t)
	a.autoLoad = true
	a.client = &Client{HTTP: srv.Client()}
	u := srv.URL + "/a.png"

	a.prefetchAvatars(u, u)
	_ = a.articleImage(u, imageCacheStable)
	_ = a.articleImage(u, imageCacheTemporary)
	waitForSource(t, a, u)
	waitForDisk(t, a.images, u, imageCacheStable)

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("同一地址请求了 %d 次，期望 1 次", got)
	}
	if a.avatarBitmap(u) == nil {
		t.Error("头像路径没有拿到同一份缓存里的位图")
	}
}

// 下载到的原始字节要落盘：换一个进程（新 app、同一配置目录）也能直接读出来，
// 不再访问网络。
func TestGlobalImageCachePersistsToDisk(t *testing.T) {
	var hits int32
	srv, _ := imageTestServer(t, &hits)
	dir := t.TempDir()
	first := newApp(dir)
	first.autoLoad = true
	first.client = &Client{HTTP: srv.Client()}
	u := srv.URL + "/persist.png"
	first.prefetchAvatars(u)
	waitForSource(t, first, u)
	waitForDisk(t, first.images, u, imageCacheStable)

	srv.Close() // 之后任何网络请求都会失败：能显示就说明来自磁盘

	second := newApp(dir)
	second.autoLoad = true
	second.client = &Client{HTTP: srv.Client()}
	// 第一次读只登记缓存项并启动后台加载（先读磁盘）；等它解码完成。
	_ = second.articleImage(u, imageCacheStable)
	src := waitForSource(t, second, u)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Image(c, src).Size(8, 8)
	}, 32, 32)
	tt.Frame()
	if n := countMarker(tt.Image()); n == 0 {
		t.Error("磁盘缓存里的图片没有解码出来")
	}
}

// 帖子配图超过 TTL 后由定期清理删除；头像与表情是长期缓存，再老也不动。
func TestImageCacheCleansExpiredPostImagesOnly(t *testing.T) {
	a := testApp(t)
	c := a.images
	old := time.Now().Add(-imageCacheTTL - time.Hour)

	post := "https://example.com/post.png"
	avatar := "https://example.com/avatar.png"
	if err := c.writeDisk(post, imageCacheTemporary, c.generation(), []byte("post")); err != nil {
		t.Fatal(err)
	}
	if err := c.writeDisk(avatar, imageCacheStable, c.generation(), []byte("avatar")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		filepath.Join(c.dir, "temp", imageKey(post)),
		filepath.Join(c.dir, "stable", imageKey(avatar)),
	} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	files, bytes := c.cleanExpired(time.Now())
	if files != 1 || bytes == 0 {
		t.Errorf("清理了 %d 个文件 / %d 字节，期望只清掉 1 个过期的帖子图片", files, bytes)
	}
	if _, err := os.Stat(filepath.Join(c.dir, "temp", imageKey(post))); !os.IsNotExist(err) {
		t.Error("过期的帖子图片没有被删除")
	}
	if _, err := os.Stat(filepath.Join(c.dir, "stable", imageKey(avatar))); err != nil {
		t.Error("头像（长期缓存）不该被定期清理删掉")
	}

	// 过期的帖子图片按未命中处理：再读会删掉文件并返回 false。
	if _, ok := c.readDisk(post, imageCacheTemporary); ok {
		t.Error("过期的帖子图片仍被当成缓存命中")
	}
}

// 设置窗口的「图片缓存」一块要能看到两类缓存的大小，并且能一键清空。
func TestSettingsShowsCacheSizeAndClearsIt(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabCache
	c := a.images
	postData := strings.Repeat("p", 2048)
	avatarData := strings.Repeat("a", 1024)
	if err := c.writeDisk("https://example.com/post.png", imageCacheTemporary, c.generation(), []byte(postData)); err != nil {
		t.Fatal(err)
	}
	if err := c.writeDisk("https://example.com/avatar.png", imageCacheStable, c.generation(), []byte(avatarData)); err != nil {
		t.Fatal(err)
	}
	// 内存里也放一张，清理后要一起丢掉。
	a.images.put("https://example.com/avatar.png", solidBitmap(4, 4, marker))

	a.startCacheStats()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if s := a.cacheStatus.snapshot(); s.Scanned {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("缓存大小没有统计出来")
		}
		time.Sleep(5 * time.Millisecond)
	}

	tt := ui.NewTester(a.settingsWindowView, settingsWindowWidth, settingsWindowHeight)
	tt.Frame()
	for _, want := range []string{"图片缓存", "头像与表情", "帖子图片", "合计", "清理缓存", "重新统计"} {
		if !tt.HasText(want) {
			t.Errorf("缓存面板缺少 %q：%q", want, tt.Texts())
		}
	}
	if !tt.HasText("1.0 KB") || !tt.HasText("2.0 KB") {
		t.Errorf("缓存大小没有按两类显示：%q", tt.Texts())
	}

	if err := tt.Click("清理缓存"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		if s := a.cacheStatus.snapshot(); !s.Busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("清理缓存没有完成：%+v", a.cacheStatus.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
	}
	// 清理结果用窗口内 toast 说明，面板里不留结论文字。
	tt.Frame()
	if !tt.HasText("已清理 3.0 KB") {
		t.Errorf("清理后没有弹出释放了多少的 toast：%q", tt.Texts())
	}
	if stats := c.Stats(); stats.TotalFiles() != 0 {
		t.Errorf("清理后磁盘上仍有 %d 个缓存文件", stats.TotalFiles())
	}
	if a.images.source("https://example.com/avatar.png") != nil {
		t.Error("清理缓存后内存里的解码结果没有被丢掉")
	}
	tt.Frame()
	if !tt.HasText("0 B") {
		t.Errorf("清理后面板仍显示旧大小：%q", tt.Texts())
	}
}

// 头像与表情属于长期缓存，帖子图片属于临时缓存：同一张图按类别写进不同
// 目录，清理时互不影响。
func TestImageCacheCategoriesUseSeparateDirectories(t *testing.T) {
	c := newImageCache(t.TempDir())
	u := "https://example.com/same.png"
	if err := c.writeDisk(u, imageCacheStable, c.generation(), []byte("stable")); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.readDisk(u, imageCacheStable); !ok {
		t.Fatal("长期缓存没有读回来")
	}
	if _, ok := c.readDisk(u, imageCacheTemporary); ok {
		t.Error("临时缓存的目录里出现了长期缓存的图")
	}
	if !strings.HasPrefix(formatBytes(1536), "1.5 KB") {
		t.Errorf("formatBytes(1536) = %q", formatBytes(1536))
	}
}

// 2Libra 的徽章小图是「22×22 SVG 里放一张 base64 位图」。MyGo 的 SVG 渲染器
// 不支持 pattern / 内嵌图片，直接解码会得到空白；缓存必须先抽出内嵌位图。
func TestBadgeSVGDecodesEmbeddedRaster(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	img.SetRGBA(1, 1, color.RGBA{R: 255, A: 255})
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	enc := base64.StdEncoding.EncodeToString(pngBuf.Bytes())
	svg := []byte(`<svg width="22" height="22" viewBox="0 0 22 22" xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink">
<rect width="22" height="22" fill="url(#p)"/>
<defs><pattern id="p" width="1" height="1"><use xlink:href="#i"/></pattern>
<image id="i" width="512" height="512" xlink:href="data:image/png;base64,` + enc + `"/></defs></svg>`)

	src, err := decodeImageData("https://r2.2libra.com/badge/svg/test.svg", imageCacheStable, svg)
	if err != nil {
		t.Fatalf("徽章 SVG 解码失败：%v", err)
	}
	bmp, ok := src.(*ui.Bitmap)
	if !ok {
		t.Fatalf("徽章 SVG 没有解出内嵌位图，得到 %T", src)
	}
	if w, h := bmp.Size(); w != 3 || h != 2 {
		t.Errorf("内嵌位图尺寸 = %dx%d，期望 3x2", w, h)
	}
}

// 徽章大图（/badge/level_10.svg 这类）是「矢量外框 + pattern 内嵌位图」的
// 混合 SVG：MyGo 不画 pattern，中间的奖牌会丢。解码时必须把内嵌位图合成到
// pattern 矩形的位置，外框矢量仍在。用一张纯红小 PNG 验证中间像素来自位图、
// 角落像素来自矢量 path。
func TestLargeBadgeSVGCompositesPatternRaster(t *testing.T) {
	raster := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 2; x++ {
			raster.SetRGBA(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, raster); err != nil {
		t.Fatal(err)
	}
	enc := base64.StdEncoding.EncodeToString(pngBuf.Bytes())
	svg := []byte(`<svg width="64" height="64" viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink">
<path d="M0 0h64v64H0z" fill="#000000"/>
<rect x="16" y="16" width="32" height="32" fill="url(#p)"/>
<defs><pattern id="p" width="1" height="1"><use xlink:href="#i" transform="scale(0.5)"/></pattern>
<image id="i" width="2" height="2" xlink:href="data:image/png;base64,` + enc + `"/></defs></svg>`)

	got := compositeBadgeImage(svg, 256)
	if got == nil {
		t.Fatal("大徽章 SVG 没有合成出图像")
	}
	if w, h := got.Bounds().Dx(), got.Bounds().Dy(); w != 64 || h != 64 {
		t.Fatalf("合成图像尺寸 = %dx%d，期望 64x64", w, h)
	}
	assertPixel := func(x, y int, name string, r, g, b, a uint32) {
		t.Helper()
		pr, pg, pb, pa := got.At(x, y).RGBA()
		if pr != r || pg != g || pb != b || pa != a {
			t.Errorf("%s 处像素 = (%d,%d,%d,%d)，期望 (%d,%d,%d,%d)", name, pr, pg, pb, pa, r, g, b, a)
		}
	}
	// 角落只有矢量 path 画的黑色。
	assertPixel(4, 4, "角落", 0, 0, 0, 0xffff)
	// 中间 pattern 矩形是内嵌的纯红位图。
	assertPixel(32, 32, "中间", 0xffff, 0, 0, 0xffff)

	// 走完整解码路径时也必须得到合成后的位图，而不是缺中间的 SVG。
	src, err := decodeImageData("https://r2.2libra.com/badge/test.svg", imageCacheStable, svg)
	if err != nil {
		t.Fatalf("大徽章 SVG 解码失败：%v", err)
	}
	if _, ok := src.(*ui.Bitmap); !ok {
		t.Fatalf("大徽章应解码成合成位图，得到 %T", src)
	}
}

// 没有内嵌位图的普通 SVG（天气图标、站内线性图标）仍按 SVG 解码。
func TestPlainSVGStillDecodes(t *testing.T) {
	svg := []byte(`<svg width="22" height="22" viewBox="0 0 22 22" xmlns="http://www.w3.org/2000/svg"><path d="M0 0h22v22H0z"/></svg>`)
	src, err := decodeImageData("https://r2.2libra.com/weather/svg/fog.svg", imageCacheStable, svg)
	if err != nil {
		t.Fatalf("普通 SVG 解码失败：%v", err)
	}
	if _, ok := src.(*ui.SVG); !ok {
		t.Fatalf("普通 SVG 变成了 %T", src)
	}
}

// 正文配图先占位时需要知道图片尺寸：缓存文件还在磁盘上时只读文件头，
// 不下载也不解码整张图。
func TestCachedImageSizeFromDisk(t *testing.T) {
	c := newImageCache(t.TempDir())
	const raw = "https://example.com/pic.png"
	if _, _, ok := c.cachedSize(raw, imageCacheTemporary); ok {
		t.Fatal("缓存里没有这张图，不应报出尺寸")
	}
	if err := c.writeDisk(raw, imageCacheTemporary, c.generation(), solidPNG(t, 320, 180, marker)); err != nil {
		t.Fatalf("写缓存失败：%v", err)
	}
	w, h, ok := c.cachedSize(raw, imageCacheTemporary)
	if !ok || w != 320 || h != 180 {
		t.Fatalf("读出的尺寸 = %.0fx%.0f ok=%v，期望 320x180", w, h, ok)
	}
	// 尺寸读一次就记住：文件被清掉后，本次运行里排版仍按它占位。
	if err := os.Remove(filepath.Join(c.categoryDir(imageCacheTemporary), imageKey(raw))); err != nil {
		t.Fatal(err)
	}
	if w2, h2, ok := c.cachedSize(raw, imageCacheTemporary); !ok || w2 != 320 || h2 != 180 {
		t.Errorf("尺寸没有缓存住：%.0fx%.0f ok=%v", w2, h2, ok)
	}
}

// 图床会直接发 WebP，而标准库不认识 WebP 头；三种 WebP 变体都要能读出宽高。
func TestWebPSizeFromHeader(t *testing.T) {
	header := func(kind string) []byte {
		b := make([]byte, 30)
		copy(b[0:], "RIFF")
		copy(b[8:], "WEBP")
		copy(b[12:], kind)
		return b
	}
	// VP8X 扩展格式：画布宽高减一，各 24 位。
	vp8x := header("VP8X")
	w, h := 1600, 900
	vp8x[24], vp8x[25], vp8x[26] = byte(w-1), byte((w-1)>>8), byte((w-1)>>16)
	vp8x[27], vp8x[28], vp8x[29] = byte(h-1), byte((h-1)>>8), byte((h-1)>>16)

	// VP8 有损：起始码之后两个 14 位宽高。
	vp8 := header("VP8 ")
	vp8[20], vp8[21], vp8[22] = 0x10, 0x00, 0x00
	vp8[23], vp8[24], vp8[25] = 0x9d, 0x01, 0x2a
	vp8[26], vp8[27] = byte(w), byte(w>>8)
	vp8[28], vp8[29] = byte(h), byte(h>>8)

	// VP8L 无损：0x2f 之后宽高减一各占 14 位。
	vp8l := header("VP8L")
	vp8l[20] = 0x2f
	bits := uint32(w-1) | uint32(h-1)<<14
	vp8l[21], vp8l[22], vp8l[23], vp8l[24] = byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24)

	for name, b := range map[string][]byte{"VP8X": vp8x, "VP8": vp8, "VP8L": vp8l} {
		w, h, ok := webpSize(b)
		if !ok || w != 1600 || h != 900 {
			t.Errorf("%s 头读出 %.0fx%.0f ok=%v，期望 1600x900", name, w, h, ok)
		}
	}
	if _, _, ok := webpSize([]byte("not a webp file")); ok {
		t.Error("非 WebP 数据不应读出尺寸")
	}
}

// 解码前先看尺寸：站上的原图常是 3000 像素以上的手机照片，按原样解成
// RGBA 一张就要几十 MB。头像与正文配图各有上限，超出的等比缩下来。
func TestDecodeImageScalesDownOversizedBitmap(t *testing.T) {
	data := solidPNG(t, 3000, 2000, marker)

	src, err := decodeImageData("https://example.com/big.png", imageCacheStable, data)
	if err != nil {
		t.Fatalf("头像图解码失败：%v", err)
	}
	bmp, ok := src.(*ui.Bitmap)
	if !ok {
		t.Fatalf("解出来的是 %T，期望位图", src)
	}
	if w, h := bmp.Size(); w != imageDecodeMaxSideStable || h != 171 {
		t.Errorf("头像缩到 %dx%d，期望 %dx171", w, h, imageDecodeMaxSideStable)
	}

	src, err = decodeImageData("https://example.com/big.png", imageCacheTemporary, data)
	if err != nil {
		t.Fatalf("正文配图解码失败：%v", err)
	}
	bmp, ok = src.(*ui.Bitmap)
	if !ok {
		t.Fatalf("解出来的是 %T，期望位图", src)
	}
	if w, h := bmp.Size(); w != imageDecodeMaxSideTemporary || h != 1067 {
		t.Errorf("配图缩到 %dx%d，期望 %dx1067", w, h, imageDecodeMaxSideTemporary)
	}
}

// 没超上限的小图原样解码，不因为缩放损失清晰度。
func TestDecodeImageKeepsSmallBitmap(t *testing.T) {
	data := solidPNG(t, 64, 48, marker)
	src, err := decodeImageData("https://example.com/small.png", imageCacheStable, data)
	if err != nil {
		t.Fatalf("小图解码失败：%v", err)
	}
	bmp, ok := src.(*ui.Bitmap)
	if !ok {
		t.Fatalf("解出来的是 %T，期望位图", src)
	}
	if w, h := bmp.Size(); w != 64 || h != 48 {
		t.Errorf("小图尺寸 = %dx%d，期望 64x48", w, h)
	}
}

// 内存预算兜住「一直滚、一直看新图」：超过预算就淘汰最久没显示的，
// 最新的一张必须留着，否则下一帧又要重新解码。
func TestImageCacheTrimsMemoryOverBudget(t *testing.T) {
	c := newImageCache(t.TempDir())
	c.budget = 4 << 20 // 4 MB：两张 800x800 的位图就超
	c.keepWarm = 0     // 测试里不保护「刚显示过」的图

	for _, raw := range []string{"a", "b", "c"} {
		time.Sleep(2 * time.Millisecond) // 拉开最后使用时间，淘汰顺序才确定
		c.put(raw, solidBitmap(800, 800, marker))
	}

	if c.source("a") != nil || c.source("b") != nil {
		t.Error("超预算后最久没显示的两张应当被淘汰")
	}
	if c.source("c") == nil {
		t.Error("最新的一张被误淘汰了")
	}
	if c.bytes > c.memoryBudget() {
		t.Errorf("淘汰后仍占 %d 字节，超过预算 %d", c.bytes, c.memoryBudget())
	}
}

// 头像与表情的长期缓存不参与定期清理，但帖子配图过期删盘时，内存里的
// 那一份也要跟着丢掉，否则浏览过的图会一直留在内存里。
func TestCleanExpiredForgetsMemoryEntries(t *testing.T) {
	c := newImageCache(t.TempDir())
	const raw = "https://example.com/old.png"
	c.put(raw, solidBitmap(16, 16, marker))
	if err := c.writeDisk(raw, imageCacheTemporary, c.generation(), solidPNG(t, 16, 16, marker)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(c.categoryDir(imageCacheTemporary), imageKey(raw))
	old := time.Now().Add(-imageCacheTTL - time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	if files, _ := c.cleanExpired(time.Now()); files != 1 {
		t.Fatalf("清理了 %d 个文件，期望 1 个", files)
	}
	if c.source(raw) != nil {
		t.Error("磁盘上的过期图片删掉了，内存里还留着")
	}
	if c.bytes != 0 {
		t.Errorf("清理后内存占用 = %d，期望 0", c.bytes)
	}
}

// EXIF 方向解析：只认 JPEG 的 APP1 段里 tag 0x0112，别的数据一律按正向。
func TestJPEGOrientationParsesEXIF(t *testing.T) {
	tiff := []byte{
		'I', 'I', 42, 0, 8, 0, 0, 0, // 小端 TIFF 头，IFD 在偏移 8
		1, 0, // 一个 entry
		0x12, 0x01, // tag 0x0112 = Orientation
		3, 0, // 类型 SHORT
		1, 0, 0, 0, // 数量 1
		6, 0, 0, 0, // 值 6（顺时针 90°）
		0, 0, 0, 0, // 没有下一个 IFD
	}
	segLen := len(tiff) + 2 + 6
	app1 := []byte{0xFF, 0xE1, byte(segLen >> 8), byte(segLen), 'E', 'x', 'i', 'f', 0, 0}
	data := append([]byte{0xFF, 0xD8}, append(app1, tiff...)...)

	if got := jpegOrientation(data); got != 6 {
		t.Errorf("EXIF 方向 = %d，期望 6", got)
	}
	if got := jpegOrientation([]byte("not a jpeg")); got != 1 {
		t.Errorf("非 JPEG 数据的方向 = %d，期望 1", got)
	}
	if got := jpegOrientation([]byte{0xFF, 0xD8, 0xFF, 0xD9}); got != 1 {
		t.Errorf("没有 EXIF 的 JPEG 方向 = %d，期望 1", got)
	}
}

// 一页列表最多只能同时取 imageWorkerCount 张图：以前每张图单起一个协程，
// 五十来个头像会一起抢连接，解码也一起抢 CPU。
func TestImageCacheCapsConcurrentLoads(t *testing.T) {
	c := newImageCache(t.TempDir())
	release := make(chan struct{})
	var running, peak int32
	f := imageFetcher{
		fetch: func(raw string) ([]byte, error) {
			n := atomic.AddInt32(&running, 1)
			for {
				old := atomic.LoadInt32(&peak)
				if n <= old || atomic.CompareAndSwapInt32(&peak, old, n) {
					break
				}
			}
			<-release
			atomic.AddInt32(&running, -1)
			return nil, errors.New("测试不出图")
		},
		redraw: func() {},
	}
	urls := make([]string, 0, imageWorkerCount*4)
	for i := 0; i < imageWorkerCount*4; i++ {
		u := fmt.Sprintf("https://example.com/pic-%d.png", i)
		urls = append(urls, u)
		c.prefetch(u, imageCacheTemporary, f)
	}

	// 等 worker 都把任务接住；其余任务应该仍在队列里排队。
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&running) < imageWorkerCount {
		if time.Now().After(deadline) {
			t.Fatalf("只有 %d 个任务开始，期望 %d 个", atomic.LoadInt32(&running), imageWorkerCount)
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(30 * time.Millisecond)
	if got := atomic.LoadInt32(&peak); got > imageWorkerCount {
		t.Fatalf("同时加载了 %d 张图，超过上限 %d", got, imageWorkerCount)
	}

	// 放行后所有排队的图都要依次完成，不能有漏掉的任务。
	close(release)
	for {
		done := 0
		for _, u := range urls {
			if c.state(u) != imageLoading {
				done++
			}
		}
		if done == len(urls) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d 张图在 3 秒内没有完成", len(urls)-done)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 头像（长期缓存）要排在帖子配图（临时缓存）前面：打开一页时先出人脸，
// 正文配图再慢慢补齐。
func TestImageCacheLoadsStableBeforeTemporary(t *testing.T) {
	c := newImageCache(t.TempDir())
	gate := make(chan struct{})
	defer close(gate)
	started := make(chan string, imageWorkerCount*2)
	f := imageFetcher{
		fetch: func(raw string) ([]byte, error) {
			started <- raw
			<-gate
			return nil, errors.New("测试不出图")
		},
		redraw: func() {},
	}
	// 先占满所有 worker，让后面的任务只能排队。
	for i := 0; i < imageWorkerCount; i++ {
		c.prefetch(fmt.Sprintf("https://example.com/block-%d.png", i), imageCacheTemporary, f)
	}
	for i := 0; i < imageWorkerCount; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("worker 没有接住占位任务")
		}
	}

	post := "https://example.com/post.png"
	avatar := "https://example.com/avatar.png"
	c.prefetch(post, imageCacheTemporary, f)
	c.prefetch(avatar, imageCacheStable, f)

	gate <- struct{}{} // 放行一个 worker
	select {
	case got := <-started:
		if got != avatar {
			t.Fatalf("先加载的是 %q，期望头像 %q", got, avatar)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("放行后没有任务开始")
	}
}

// 一张图加载完只请求「正在渲染它」的那扇窗口重绘。以前走 publish，会把主
// 窗口和每一扇详情 / 设置 / 金币窗口全部重建一遍。
func TestImageLoadInvalidatesOnlyRenderingWindow(t *testing.T) {
	var hits int32
	srv, _ := imageTestServer(t, &hits)
	a := testApp(t)
	a.autoLoad = true
	a.client = &Client{HTTP: srv.Client()}
	u := srv.URL + "/window.png"

	withImage := ui.NewTester(a.windowView(func(c *ui.Context) {
		a.articleImage(u, imageCacheTemporary)
		ui.Text(c, "占位")
	}), 64, 64)
	other := ui.NewTester(a.windowView(func(c *ui.Context) {
		ui.Text(c, "无关窗口")
	}), 64, 64)
	withImage.Frame()
	other.Frame()

	waitForSource(t, a, u)

	// 16ms 的合并窗口过后，只有渲染过这张图的窗口会请求新的一帧。
	deadline := time.Now().Add(time.Second)
	for !withImage.FramePending() {
		if time.Now().After(deadline) {
			t.Fatal("图片加载完成后渲染它的窗口没有请求重绘")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if other.FramePending() {
		t.Error("没显示这张图的窗口也被请求重绘了")
	}
}
