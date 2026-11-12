package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// 全应用只有这一份图片缓存：列表与详情里的用户头像、正文与评论里的行内
// 表情和社区图标、帖子里的配图都从这里取。同一个地址只下载一次，解码结果
// 留在内存里，原始字节落盘到配置目录下的 cache/images，重启后不必重新下载。
//
// 缓存按内容的变化快慢分两类：
//
//   - 长期缓存（imageCacheStable）：用户头像与社区小表情。换头像要花金币、
//     表情是服务端固定的一批图，内容基本不变，因此长期保留，不参与定期清理。
//   - 临时缓存（imageCacheTemporary）：帖子正文里嵌入的外部图片。来源不可控、
//     体积可能很大，超过 imageCacheTTL 后由后台定期清理，下次浏览时再下载。
const (
	imageCacheStable imageCategory = iota
	imageCacheTemporary
)

type imageCategory int

// imageCacheTTL 是帖子图片的保留时长。头像与表情不看这个时间。
const imageCacheTTL = 7 * 24 * time.Hour

// imageCacheCleanupInterval 是运行中自动清理过期帖子图片的间隔。启动时
// 会先清一次，之后按这个间隔再清，长期开着的窗口也不会一直堆别人的图。
const imageCacheCleanupInterval = 6 * time.Hour

// imageCache 是缓存本体。entries 由 UI 线程读写；无窗口测试里 publish 直接
// 在网络协程执行，所以 map 也必须加锁。
type imageCache struct {
	dir string

	mu      sync.Mutex
	entries map[string]*imageEntry
	// gen 在清理缓存时递增：清理前发出的下载即使晚到，也不再写回磁盘，
	// 否则刚清空的大小会立刻被在途请求补回来。
	gen uint64
}

// imageEntry 是一个地址的下载 / 解码状态。src 为 nil 表示还没加载完或加载
// 失败；失败也记在这里，避免每帧重发注定失败的请求。
type imageEntry struct {
	mu  sync.Mutex
	src ui.ImageSource
}

func (e *imageEntry) set(src ui.ImageSource) {
	e.mu.Lock()
	e.src = src
	e.mu.Unlock()
}

func (e *imageEntry) source() ui.ImageSource {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.src
}

// bitmap 只在解码结果是位图时返回它（头像要的是位图）；SVG 表情对头像没有
// 意义，返回 nil 让调用方继续画昵称首字母。
func (e *imageEntry) bitmap() *ui.Bitmap {
	b, _ := e.source().(*ui.Bitmap)
	return b
}

// newImageCache 把缓存目录定在配置目录下的 cache/images：与 token.json、
// proxy.json 同一处，测试通过 TWOLIBRA_CONFIG_DIR 或临时目录隔离。
func newImageCache(dir string) *imageCache {
	if dir == "" {
		dir = configDir()
	}
	return &imageCache{
		dir:     filepath.Join(dir, "cache", "images"),
		entries: make(map[string]*imageEntry),
	}
}

func (c *imageCache) categoryDir(cat imageCategory) string {
	if cat == imageCacheTemporary {
		return filepath.Join(c.dir, "temp")
	}
	return filepath.Join(c.dir, "stable")
}

// imageKey 是地址的存储名：URL 里有 / ? : 等不能直接进文件名的字符，用
// SHA-256 十六进制统一成定长安全名，也不会有截断或大小写冲突。
func imageKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (c *imageCache) get(raw string) *imageEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[raw]
}

// begin 返回地址对应的缓存项；created 为 true 表示需要调用方启动下载。
func (c *imageCache) begin(raw string) (*imageEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[raw]; ok {
		return e, false
	}
	e := &imageEntry{}
	c.entries[raw] = e
	return e, true
}

// put 直接记下一张已解码的图片，供同步加载与测试注入使用。
func (c *imageCache) put(raw string, src ui.ImageSource) {
	e, _ := c.begin(raw)
	e.set(src)
}

func (c *imageCache) source(raw string) ui.ImageSource {
	if e := c.get(raw); e != nil {
		return e.source()
	}
	return nil
}

func (c *imageCache) bitmap(raw string) *ui.Bitmap {
	if e := c.get(raw); e != nil {
		return e.bitmap()
	}
	return nil
}

func (c *imageCache) generation() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// readDisk 读回某个地址的原始字节。临时缓存超过 imageCacheTTL 的文件按
// 未命中处理并顺手删掉，用户再次浏览时重新下载一份新的。
func (c *imageCache) readDisk(raw string, cat imageCategory) ([]byte, bool) {
	path := filepath.Join(c.categoryDir(cat), imageKey(raw))
	if cat == imageCacheTemporary {
		info, err := os.Stat(path)
		if err != nil {
			return nil, false
		}
		if time.Since(info.ModTime()) > imageCacheTTL {
			_ = os.Remove(path)
			return nil, false
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

// writeDisk 原子地把字节写进缓存。先写 .tmp 再改名，进程中途退出也不会
// 留下半张图；gen 变了说明期间清理过缓存，这次结果直接丢弃。
func (c *imageCache) writeDisk(raw string, cat imageCategory, gen uint64, data []byte) error {
	if c.generation() != gen {
		return nil
	}
	dir := c.categoryDir(cat)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, imageKey(raw))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// forgetDisk 删掉一个缓存文件：文件损坏、解不出来时调用，让下次重新下载。
func (c *imageCache) forgetDisk(raw string, cat imageCategory) {
	_ = os.Remove(filepath.Join(c.categoryDir(cat), imageKey(raw)))
}

// imageCacheStats 是磁盘缓存的占用情况，按两类分开统计。
type imageCacheStats struct {
	StableFiles int
	StableBytes int64
	TempFiles   int
	TempBytes   int64
}

func (s imageCacheStats) TotalBytes() int64 { return s.StableBytes + s.TempBytes }

func (s imageCacheStats) TotalFiles() int { return s.StableFiles + s.TempFiles }

// Stats 遍历缓存目录统计大小。只在后台调用：目录里可能有很多文件。
func (c *imageCache) Stats() imageCacheStats {
	stableFiles, stableBytes := dirUsage(c.categoryDir(imageCacheStable))
	tempFiles, tempBytes := dirUsage(c.categoryDir(imageCacheTemporary))
	return imageCacheStats{
		StableFiles: stableFiles,
		StableBytes: stableBytes,
		TempFiles:   tempFiles,
		TempBytes:   tempBytes,
	}
}

// dirUsage 统计一个目录里的普通文件数与字节数；目录不存在是正常的空缓存。
func dirUsage(dir string) (files int, bytes int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files++
		bytes += info.Size()
	}
	return files, bytes
}

// cleanExpired 删除过期的帖子图片和上次异常退出留下的半成品，返回删除的
// 文件数与字节数。头像与表情不参与。
func (c *imageCache) cleanExpired(now time.Time) (files int, bytes int64) {
	dir := c.categoryDir(imageCacheTemporary)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		name := e.Name()
		expired := now.Sub(info.ModTime()) > imageCacheTTL
		// .tmp 是写到一半的文件；超过一小时还没有改名，就是上次退出的残留。
		leftover := strings.HasSuffix(name, ".tmp") && now.Sub(info.ModTime()) > time.Hour
		if !expired && !leftover {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err == nil {
			files++
			bytes += info.Size()
		}
	}
	return files, bytes
}

// clear 清空内存与磁盘缓存，返回被释放的空间。清理后递增 gen，让在途的
// 下载不再写回磁盘。
func (c *imageCache) clear() (imageCacheStats, error) {
	before := c.Stats()
	c.mu.Lock()
	c.gen++
	c.entries = make(map[string]*imageEntry)
	c.mu.Unlock()
	var errs []error
	for _, cat := range []imageCategory{imageCacheStable, imageCacheTemporary} {
		if err := os.RemoveAll(c.categoryDir(cat)); err != nil {
			errs = append(errs, err)
		}
	}
	return before, errors.Join(errs...)
}

// ---- 应用侧的取图入口 ----

// prefetchImage 启动一次后台加载。同一个地址只下一次：成功、失败还是仍在
// 下载都记在缓存里，不会因为每帧渲染而重发。
func (a *app) prefetchImage(raw string, cat imageCategory) {
	if !a.autoLoad {
		return
	}
	if _, err := parseImageURL(raw); err != nil {
		return
	}
	if _, created := a.images.begin(raw); !created {
		return
	}
	go a.loadImageEntry(raw, cat)
}

// loadImageEntry 先看磁盘缓存，没有再下载，解好后通知 UI 重绘。
func (a *app) loadImageEntry(raw string, cat imageCategory) {
	e := a.images.get(raw)
	if e == nil {
		return
	}
	gen := a.images.generation()
	if data, ok := a.images.readDisk(raw, cat); ok {
		if src, err := ui.DecodeImage(data); err == nil {
			e.set(src)
			a.publish(func() {})
			return
		}
		a.images.forgetDisk(raw, cat)
	}
	data, err := a.fetchImage(raw)
	if err != nil {
		e.set(nil)
		log.Printf("图片加载失败：%v", err)
		return
	}
	src, err := ui.DecodeImage(data)
	if err != nil {
		e.set(nil)
		log.Printf("图片加载失败：%v", err)
		return
	}
	e.set(src)
	if err := a.images.writeDisk(raw, cat, gen, data); err != nil {
		log.Printf("图片缓存写入失败：%v", err)
	}
	a.publish(func() {}) // 走 Window.Update，让图片出现在下一帧
}

// loadImageNow 是给 Markdown 同步 LoadImage 回调用的版本：先读缓存，没有再
// 当场下载并落盘。返回错误让 Markdown 显示替代文字。
func (a *app) loadImageNow(raw string, cat imageCategory) (ui.ImageSource, error) {
	raw = strings.TrimSpace(raw)
	if src := a.images.source(raw); src != nil {
		return src, nil
	}
	data, cached := a.images.readDisk(raw, cat)
	if !cached {
		if _, err := parseImageURL(raw); err != nil {
			return nil, err
		}
		var err error
		if data, err = a.fetchImage(raw); err != nil {
			return nil, err
		}
	}
	src, err := ui.DecodeImage(data)
	if err != nil {
		if cached {
			a.images.forgetDisk(raw, cat)
		}
		return nil, err
	}
	a.images.put(raw, src)
	if !cached {
		if err := a.images.writeDisk(raw, cat, a.images.generation(), data); err != nil {
			log.Printf("图片缓存写入失败：%v", err)
		}
	}
	return src, nil
}

// parseImageURL 只放行 http(s)，与原来直接下载图片时的校验一致。
func parseImageURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("不支持的图片地址：%s", raw)
	}
	return u, nil
}

// cleanExpiredImages 清理过期的帖子图片，有删除时记一行日志。
func (a *app) cleanExpiredImages() {
	files, bytes := a.images.cleanExpired(time.Now())
	if files > 0 {
		log.Printf("图片缓存：清理了 %d 个过期帖子图片（%s）", files, formatBytes(bytes))
	}
}

// startImageCacheMaintenance 在启动时清一次过期帖子图片，之后每
// imageCacheCleanupInterval 再清一次。只在真实应用里启动（main 的
// WhenReady），无头测试不会在后台碰磁盘。
func (a *app) startImageCacheMaintenance() {
	go func() {
		a.cleanExpiredImages()
		ticker := time.NewTicker(imageCacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			a.cleanExpiredImages()
		}
	}()
}

// formatBytes 把字节数写成给人看的大小：小于 1 KB 用字节，其余保留一位
// 小数。缓存面板与清理提示共用这一种写法。
func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := [...]string{"KB", "MB", "GB", "TB"}
	v := float64(n)
	for _, unit := range units {
		v /= 1024
		if v < 1024 {
			return fmt.Sprintf("%.1f %s", v, unit)
		}
	}
	return fmt.Sprintf("%.1f PB", v/1024)
}

// formatDurationCN 把缓存保留时长写成中文（7 天 / 12 小时）。设置面板的说明
// 文案从这里取，改 imageCacheTTL 时文案不会说谎。
func formatDurationCN(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%d 天", int(d/(24*time.Hour)))
	case d%time.Hour == 0:
		return fmt.Sprintf("%d 小时", int(d/time.Hour))
	default:
		return fmt.Sprintf("%d 分钟", int(d/time.Minute))
	}
}
