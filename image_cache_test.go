package main

import (
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
		if s := a.cacheStatus.snapshot(); !s.Busy && strings.Contains(s.Msg, "已清理") {
			if !strings.HasPrefix(s.Msg, "已清理 3.0 KB") {
				t.Errorf("清理提示 = %q，期望释放 3.0 KB", s.Msg)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("清理缓存没有完成：%+v", a.cacheStatus.snapshot())
		}
		time.Sleep(5 * time.Millisecond)
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
