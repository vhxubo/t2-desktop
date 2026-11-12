package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	// sizes 是从磁盘缓存文件头读出的尺寸，键是图片地址。详情正文用它给
	// 还没解出来的配图先占住位置：尺寸读一次就记住，渲染每帧不会重复开文件。
	sizes map[string]imageSize
	// gen 在清理缓存时递增：清理前发出的下载即使晚到，也不再写回磁盘，
	// 否则刚清空的大小会立刻被在途请求补回来。
	gen uint64
}

// imageSize 是图片的宽高（像素）。ok 为 false 表示磁盘上还没有这张图，
// 或它的格式读不出尺寸。
type imageSize struct {
	w, h float32
	ok   bool
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
		sizes:   make(map[string]imageSize),
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

// cachedSize 返回磁盘缓存里这张图的宽高：只读文件头，不下载也不解码整张图。
// 详情正文在图片还没解出来时用它先占住一样大的位置，图片到达时就地替换，
// 正文不会先缩后跳。读不到的格式（例如 SVG）返回 ok=false，退回小占位。
func (c *imageCache) cachedSize(raw string, cat imageCategory) (w, h float32, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, 0, false
	}
	c.mu.Lock()
	s, seen := c.sizes[raw]
	c.mu.Unlock()
	if !seen {
		w, h, ok = c.readDiskSize(raw, cat)
		s = imageSize{w: w, h: h, ok: ok}
		c.mu.Lock()
		c.sizes[raw] = s
		c.mu.Unlock()
	}
	return s.w, s.h, s.ok
}

// readDiskSize 从缓存文件头读出宽高。临时缓存过期按未命中处理，与 readDisk
// 同一套规则；头像和表情的长期缓存不看时间。
func (c *imageCache) readDiskSize(raw string, cat imageCategory) (w, h float32, ok bool) {
	path := filepath.Join(c.categoryDir(cat), imageKey(raw))
	if cat == imageCacheTemporary {
		info, err := os.Stat(path)
		if err != nil || time.Since(info.ModTime()) > imageCacheTTL {
			return 0, 0, false
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	// DecodeConfig 只读文件头，不会把整张图读进来。
	if cfg, _, err := image.DecodeConfig(f); err == nil && cfg.Width > 0 && cfg.Height > 0 {
		return float32(cfg.Width), float32(cfg.Height), true
	}
	// 标准库不认识 WebP（站点自己的图床会发 WebP）：回头读一段文件头自己量。
	if _, err := f.Seek(0, io.SeekStart); err == nil {
		head := make([]byte, 64<<10)
		n, _ := io.ReadFull(f, head)
		if w, h, ok := webpSize(head[:n]); ok {
			return w, h, true
		}
	}
	return 0, 0, false
}

// webpSize 从 WebP 文件头读出宽高：支持扩展格式 VP8X、有损 VP8 与无损 VP8L。
// 只需文件开头几十个字节，读不出来就返回 ok=false。
func webpSize(data []byte) (w, h float32, ok bool) {
	if len(data) < 30 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, false
	}
	le16 := func(b []byte) int { return int(b[0]) | int(b[1])<<8 }
	le24 := func(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 }
	switch string(data[12:16]) {
	case "VP8X": // 扩展格式：画布是 24 位宽高减一，放在标志位后面。
		if len(data) < 30 {
			return 0, 0, false
		}
		iw, ih := le24(data[24:27])+1, le24(data[27:30])+1
		return float32(iw), float32(ih), true
	case "VP8 ": // 有损：起始码之后是两个 14 位的宽高。
		if len(data) < 30 || data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return 0, 0, false
		}
		return float32(le16(data[26:28]) & 0x3fff), float32(le16(data[28:30]) & 0x3fff), true
	case "VP8L": // 无损：0x2f 签名之后，宽高减一各占 14 位。
		if len(data) < 25 || data[20] != 0x2f {
			return 0, 0, false
		}
		bits := uint32(data[21]) | uint32(data[22])<<8 | uint32(data[23])<<16 | uint32(data[24])<<24
		return float32(bits&0x3fff + 1), float32((bits>>14)&0x3fff + 1), true
	}
	return 0, 0, false
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
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// 刚写下磁盘：之前「读不到尺寸」的记录作废，下次排版就能先占对位置。
	c.forgetSize(raw)
	return nil
}

// forgetSize 丢掉一个地址的尺寸记录。写入或删除缓存文件后调用。
func (c *imageCache) forgetSize(raw string) {
	c.mu.Lock()
	delete(c.sizes, raw)
	c.mu.Unlock()
}

// forgetDisk 删掉一个缓存文件：文件损坏、解不出来时调用，让下次重新下载。
func (c *imageCache) forgetDisk(raw string, cat imageCategory) {
	_ = os.Remove(filepath.Join(c.categoryDir(cat), imageKey(raw)))
	c.forgetSize(raw)
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
	c.sizes = make(map[string]imageSize)
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

// embeddedRasterPattern 找 SVG 里内嵌的位图（2Libra 的小徽章图标就是这种
// 形式：一个 22×22 的 SVG，内容只是一张 512×512 PNG 的 base64）。
var embeddedRasterPattern = regexp.MustCompile(`data:image/(?:png|jpe?g|gif|webp);base64,([A-Za-z0-9+/=\s]+)`)

// decodeImageData 解码一张图片。MyGo 的 SVG 渲染器不支持 pattern / 内嵌图片，
// 而站内徽章小图正是「SVG 包一张位图」：直接交给 ui.DecodeImage 会得到一张
// 尺寸正确但什么都没有的 SVG。这里先把内嵌位图抽出来解码，抽不到再按普通
// SVG / 位图处理（天气图标就是普通矢量 SVG）。
func decodeImageData(raw string, data []byte) (ui.ImageSource, error) {
	if strings.Contains(raw, "/badge/svg/") || strings.Contains(raw, "/weather/svg/") {
		if src := embeddedRasterImage(data); src != nil {
			return src, nil
		}
	}
	return ui.DecodeImage(data)
}

// embeddedRasterImage 解码 SVG 里第一张内嵌位图。没有内嵌位图或 base64 坏了
// 时返回 nil，让调用方回退到 ui.DecodeImage。
func embeddedRasterImage(data []byte) ui.ImageSource {
	m := embeddedRasterPattern.FindSubmatch(data)
	if len(m) < 2 {
		return nil
	}
	// base64 可能被 SVG 里的换行拆开：去掉所有空白再解。
	encoded := strings.Join(strings.Fields(string(m[1])), "")
	raster, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	src, err := ui.DecodeImage(raster)
	if err != nil {
		return nil
	}
	return src
}

// loadImageEntry 先看磁盘缓存，没有再下载，解好后通知 UI 重绘。
func (a *app) loadImageEntry(raw string, cat imageCategory) {
	e := a.images.get(raw)
	if e == nil {
		return
	}
	gen := a.images.generation()
	if data, ok := a.images.readDisk(raw, cat); ok {
		if src, err := decodeImageData(raw, data); err == nil {
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
	src, err := decodeImageData(raw, data)
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
	src, err := decodeImageData(raw, data)
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
