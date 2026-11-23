package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
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
	// imageCacheViewer 是帖子配图的「预览档」：多图预览窗口要把图放大到好几倍，
	// 按正文那一档（长边 1600）解码的原图一放大就糊。预览档按长边 3200 解码，
	// 只给预览窗口用，仍然落在临时目录、同样按 imageCacheTTL 清理；正文里按栏宽
	// 缩小显示时还是用省内存的临时档（见 image_viewer.go 的 viewerImage）。
	imageCacheViewer
)

type imageCategory int

// imageLoadState 是一个地址的加载阶段。以前缓存只用 src 是否为 nil 区分
// 「有图」和「没图」，界面分不出「还在下载」与「已经失败」，只能一律留白。
// 阶段记在缓存项里：加载中才转圈，失败后给一句说明，二者都不会重发请求。
type imageLoadState uint8

const (
	imageIdle    imageLoadState = iota // 没登记过：未开启自动加载或地址不合法
	imageLoading                       // 后台正在下载 / 解码
	imageLoaded                        // 已解码，source() 能取到图
	imageFailed                        // 加载失败，本轮不再重试
)

// imageCacheTTL 是帖子图片的保留时长。头像与表情不看这个时间。
const imageCacheTTL = 7 * 24 * time.Hour

// 失败头像的有限重试：网络抖动或刚切换代理时，头像可能下载失败一次。以前
// 失败项永久不再下载，同一会话里这张头像一直是昵称首字母，直到换地址或清
// 缓存。现在允许在数据再次到达（重新打开窗口、刷新列表）时重试，但设上限
// 与冷却，避免每批数据都重发请求，或对永久坏链无限重试。
const (
	imageRetryLimit    = 2               // 首次失败后最多再试的次数
	imageRetryCooldown = 3 * time.Second // 两次尝试之间的最短间隔
)

// imageCacheCleanupInterval 是运行中自动清理过期帖子图片的间隔。启动时
// 会先清一次，之后按这个间隔再清，长期开着的窗口也不会一直堆别人的图。
const imageCacheCleanupInterval = 6 * time.Hour

// 解码后允许的最长边（像素）。站上的原图常是 3000～4000 像素的手机照片，
// 磁盘上只有几百 KB，解码成 RGBA 却要 30～48 MB：缓存几十张就是几百 MB。
// 界面里正文配图最宽约 700 DIP，2 倍屏 1400 像素就够；头像最大显示 56 DIP
// （用户卡片），3 倍屏 168 像素，256 留了一倍余量。超出的一律等比缩到这个
// 上限再进缓存。
const (
	imageDecodeMaxSideStable    = 256
	imageDecodeMaxSideTemporary = 1600
	imageDecodeMaxSideViewer    = 3200
)

// imageDecodeMaxSide 按用途给出解码尺寸上限。
func imageDecodeMaxSide(cat imageCategory) int {
	switch cat {
	case imageCacheStable:
		return imageDecodeMaxSideStable
	case imageCacheViewer:
		return imageDecodeMaxSideViewer
	}
	return imageDecodeMaxSideTemporary
}

// imageCacheMemoryBudget 是内存里已解码图片的估算总量上限。尺寸上限把单张
// 图压到几 MB 之后，这个预算只用来兜住「一直滚、一直看新图」的情况：
// 超过就按最后显示时间淘汰最久没露面的，磁盘缓存还在，再看时重新解码即可。
const imageCacheMemoryBudget int64 = 192 << 20

// imageCacheKeepWarm 是淘汰时跳过的时间窗：这一屏正在画的图刚被取用，
// 不能淘汰，否则下一帧又要重新解码，画面会闪。
const imageCacheKeepWarm = 10 * time.Second

// imageWorkerCount 是同时下载并解码的图片数。以前每张图单起一个协程，一页
// 列表最多能同时起五十个：头像和正文配图一起抢连接，头像反而排在后面。
// 五个足够喂满一条连接，也不会让解码一起抢满 CPU。
const imageWorkerCount = 5

// imageRedrawInterval 是图片加载完成后合并重绘的窗口。一屏几十张图经常在
// 同一小段时间里接连解码完成，逐张重建界面会白烧几十帧；攒一帧再统一通知，
// 图片照样在下一帧出现。
const imageRedrawInterval = 16 * time.Millisecond

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
	// bytes 是 entries 里所有已解码图片的估算占用，超过 budget 时按最后
	// 显示时间淘汰。budget 默认 imageCacheMemoryBudget，测试里调小来验证淘汰。
	bytes    int64
	budget   int64
	keepWarm time.Duration

	// 图片任务队列：下载与解码都交给有限数量的 worker（见 prefetch）。
	// 头像这类长期缓存排在帖子配图前面，列表一打开时头像先出来。
	jobMu   sync.Mutex
	jobsHi  []imageJob
	jobsLo  []imageJob
	workers int

	// frame 是正在渲染的那一帧所属窗口的重绘服务（见 beginFrame）。渲染时
	// 读到的、仍在加载的图会登记它，加载完成只重绘真正显示这张图的窗口。
	frameMu sync.Mutex
	frame   ui.Services
}

// imageSize 是图片的宽高（像素）。ok 为 false 表示磁盘上还没有这张图，
// 或它的格式读不出尺寸。
type imageSize struct {
	w, h float32
	ok   bool
}

// imageEntry 是一个地址的下载 / 解码状态。src 与 state 一起读：src 为 nil
// 时 state 说明是仍在加载还是已经失败；失败也记在这里，避免每帧重发注定
// 失败的请求。
type imageEntry struct {
	mu    sync.Mutex
	src   ui.ImageSource
	state imageLoadState
	// size 是这张图解码后的大致字节数，供内存预算统计；lastUsed 是最后一次
	// 被界面取用的时间（Unix 纳秒），淘汰时按它从旧到新排。两者都用原子量：
	// 界面每帧都会读 lastUsed，不值得为它加锁。
	size     atomic.Int64
	lastUsed atomic.Int64
	// failures 是连续失败次数、failedAt 是最近一次失败时间，都由 e.mu 保护。
	// 有限重试据此判断失败项能否在下一批数据到达时再下载一次。
	failures int
	failedAt time.Time
	// subs 是在这轮加载期间渲染过这张图的窗口。加载完成（成功或失败）时
	// 只重绘这些窗口，不再让一张图的回包把每一扇附属窗口都重建一遍。
	subs map[ui.Services]struct{}
	// cache 是登记这张图的那份缓存，set 时把占用变化报回去；key 是它在
	// entries 里的键，用来确认自己还没被淘汰。测试里手工造出来的条目可以为空。
	cache *imageCache
	key   string
}

// set 记下最终结果与阶段：解码成功才有图，nil 一律算失败。
func (e *imageEntry) set(src ui.ImageSource) {
	size := imageSourceBytes(src)
	e.mu.Lock()
	old := e.size.Load()
	// 先记大小再置状态：淘汰逻辑看到 imageLoaded 时读到的必须是最新的大小，
	// 否则会把这张图按 0 字节「免费」留在内存里。
	e.size.Store(size)
	e.src = src
	if src == nil {
		e.state = imageFailed
		e.failures++
		e.failedAt = time.Now()
	} else {
		e.state = imageLoaded
		e.failures = 0
		e.failedAt = time.Time{}
		e.lastUsed.Store(time.Now().UnixNano())
	}
	// 这一轮结束了：订阅者一次通知完就清空，下一轮（重试）重新登记。
	subs := e.subs
	e.subs = nil
	e.mu.Unlock()
	for svc := range subs {
		imageRedraws.schedule(svc)
	}
	if e.cache != nil {
		e.cache.adjustMemory(e, old, size)
	}
}

// imageSourceBytes 估算一张图在内存里占多少字节：位图按 4 字节/像素算，
// 再乘 1.33 —— mygo 为缩小绘制会留各级减半的副本，合计约多三分之一。
// SVG 是矢量，本身只有一份路径数据，按一个小常数计。
func imageSourceBytes(src ui.ImageSource) int64 {
	switch s := src.(type) {
	case *ui.Bitmap:
		w, h := s.Size()
		return int64(float64(w) * float64(h) * 4 * 1.33)
	case *ui.SVG:
		return 8 << 10
	}
	return 0
}

func (e *imageEntry) loadState() imageLoadState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

func (e *imageEntry) source() ui.ImageSource {
	e.mu.Lock()
	src := e.src
	// 还没解码完的图，把当前渲染它的窗口记下来：回包到了只重绘这一扇窗口。
	if src == nil && e.state == imageLoading && e.cache != nil {
		e.subscribeLocked(e.cache.frameServices())
	}
	e.mu.Unlock()
	if src != nil {
		// 每帧都会走到这里：隔一秒记一次就够，不必为每次取图都写原子量。
		now := time.Now().UnixNano()
		if now-e.lastUsed.Load() > int64(time.Second) {
			e.lastUsed.Store(now)
		}
	}
	return src
}

// subscribeLocked 登记一个正在渲染这张图的窗口。调用方持有 e.mu；零值服务
// （无窗口的无头测试）不登记。只在加载中登记一次，加载完成即清空。
func (e *imageEntry) subscribeLocked(svc ui.Services) {
	if svc == (ui.Services{}) {
		return
	}
	if e.subs == nil {
		e.subs = make(map[ui.Services]struct{}, 1)
	}
	e.subs[svc] = struct{}{}
}

// subscribeRender 在界面查询加载状态时登记当前渲染窗口：占位转圈的那张图
// 加载失败后也要让那一扇窗口换成失败说明。
func (e *imageEntry) subscribeRender(svc ui.Services) {
	e.mu.Lock()
	if e.state == imageLoading {
		e.subscribeLocked(svc)
	}
	e.mu.Unlock()
}

// bitmap 只在解码结果是位图时返回它（头像要的是位图）；SVG 表情对头像没有
// 意义，返回 nil 让调用方继续画昵称首字母。
func (e *imageEntry) bitmap() *ui.Bitmap {
	b, _ := e.source().(*ui.Bitmap)
	return b
}

// newImageCache 把缓存目录定在配置目录下的 cache/images：与令牌（OS 凭据库，
// 无凭据库时退回 token.json）、proxy.json 同一处，测试通过 TWOLIBRA_CONFIG_DIR
// 或临时目录隔离。
func newImageCache(dir string) *imageCache {
	if dir == "" {
		dir = configDir()
	}
	c := &imageCache{
		dir:      filepath.Join(dir, "cache", "images"),
		entries:  make(map[string]*imageEntry),
		sizes:    make(map[string]imageSize),
		budget:   imageCacheMemoryBudget,
		keepWarm: imageCacheKeepWarm,
	}
	return c
}

// beginFrame 记录正在渲染的窗口，endFrame 在帧尾清掉。图片预取都在渲染时
// 发起，加载回包据此只重绘这一扇窗口（见 imageEntry.subs）。
func (c *imageCache) beginFrame(svc ui.Services) {
	c.frameMu.Lock()
	c.frame = svc
	c.frameMu.Unlock()
}

func (c *imageCache) endFrame() {
	c.frameMu.Lock()
	c.frame = ui.Services{}
	c.frameMu.Unlock()
}

func (c *imageCache) frameServices() ui.Services {
	c.frameMu.Lock()
	defer c.frameMu.Unlock()
	return c.frame
}

// memoryBudget 返回当前缓存的内存预算，未设置时用默认值。
func (c *imageCache) memoryBudget() int64 {
	if c.budget > 0 {
		return c.budget
	}
	return imageCacheMemoryBudget
}

func (c *imageCache) categoryDir(cat imageCategory) string {
	switch cat {
	case imageCacheStable:
		return filepath.Join(c.dir, "stable")
	case imageCacheViewer:
		// 预览档与临时档共用目录：同一张图的字节只有一份，两档只是解码尺寸
		// 不同（见 imageCacheViewer）。
		return filepath.Join(c.dir, "temp")
	}
	return filepath.Join(c.dir, "temp")
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
// 失败项默认不再下载，但冷却期过后、未超重试上限时会被重新拉起一次，让
// 网络抖动 / 切代理造成的偶发失败有机会在下一批数据到达时恢复。
func (c *imageCache) begin(raw string) (*imageEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[raw]; ok {
		return e, e.maybeRetry()
	}
	e := &imageEntry{state: imageLoading, cache: c, key: raw}
	c.entries[raw] = e
	return e, true
}

// maybeRetry 判断一个失败项是否该再下载一次：仅在失败态、未超上限、且距
// 上次失败已过冷却期时返回 true，并把状态改回加载中。加载中与已成功的项
// 一律返回 false，保持"同一地址只下一次"。调用方持有 c.mu。
func (e *imageEntry) maybeRetry() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state != imageFailed {
		return false
	}
	if e.failures > imageRetryLimit {
		return false
	}
	if time.Since(e.failedAt) < imageRetryCooldown {
		return false
	}
	e.state = imageLoading
	return true
}

// adjustMemory 把一张图解码后的占用变化记进总量，超出预算时顺手淘汰。
// 条目在解码期间可能已被淘汰：那时它不再是表里那一个，这笔占用没有主人，
// 直接丢掉，否则 c.bytes 只涨不落，淘汰会越跑越无效。
func (c *imageCache) adjustMemory(e *imageEntry, old, size int64) {
	if old == size {
		return
	}
	c.mu.Lock()
	if c.entries[e.key] != e {
		c.mu.Unlock()
		return
	}
	c.bytes += size - old
	c.trimMemoryLocked()
	c.mu.Unlock()
}

// trimMemoryLocked 把内存占用压回预算内：按最后显示时间从旧到新淘汰，
// imageCacheKeepWarm 内刚取用过的留着，免得把这一屏正在画的图丢掉。
// 淘汰只是从内存里移除，磁盘缓存还在，再次显示时重新解码即可。
func (c *imageCache) trimMemoryLocked() {
	budget := c.memoryBudget()
	if c.bytes <= budget {
		return
	}
	type candidate struct {
		key string
		at  int64
	}
	cands := make([]candidate, 0, len(c.entries))
	for key, e := range c.entries {
		// 走 loadState 而不是直接读 e.state：state 由 set 在解码协程里改，
		// 这里在别的协程上读，不取锁就是数据竞争。
		if e.loadState() != imageLoaded {
			continue
		}
		cands = append(cands, candidate{key: key, at: e.lastUsed.Load()})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].at < cands[j].at })
	cutoff := time.Now().Add(-c.keepWarm).UnixNano()
	target := budget * 3 / 4
	for _, cd := range cands {
		if c.bytes <= target || cd.at > cutoff {
			break
		}
		e := c.entries[cd.key]
		if e == nil {
			continue
		}
		c.bytes -= e.size.Load()
		delete(c.entries, cd.key)
	}
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

// state 返回某个地址的加载阶段，没登记过返回 imageIdle。界面只用它决定占位
// 里要不要转圈，自身不发起任何请求。仍在加载的图顺手登记当前窗口，失败后
// 那一扇窗口才会从转圈换成失败说明。
func (c *imageCache) state(raw string) imageLoadState {
	if e := c.get(strings.TrimSpace(raw)); e != nil {
		e.subscribeRender(c.frameServices())
		return e.loadState()
	}
	return imageIdle
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
	removed := make(map[string]bool, len(entries))
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
			removed[name] = true
		}
	}
	c.forgetTemporary(removed)
	return files, bytes
}

// forgetTemporary 把磁盘上已经删掉的临时图片对应的内存项一起丢掉。只删
// 文件不管内存的话，浏览过的帖子配图会一直留在内存里，过期清理等于白做。
// 键是地址的 SHA-256 文件名，这里反查一遍；清理每 6 小时才跑一次，代价可忽略。
func (c *imageCache) forgetTemporary(names map[string]bool) {
	if len(names) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for raw, e := range c.entries {
		if !names[imageKey(raw)] {
			continue
		}
		c.bytes -= e.size.Load()
		delete(c.entries, raw)
		delete(c.sizes, raw)
	}
}

// clear 清空内存与磁盘缓存，返回被释放的空间。清理后递增 gen，让在途的
// 下载不再写回磁盘。
func (c *imageCache) clear() (imageCacheStats, error) {
	before := c.Stats()
	c.mu.Lock()
	c.gen++
	c.entries = make(map[string]*imageEntry)
	c.sizes = make(map[string]imageSize)
	c.bytes = 0
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
// 下载都记在缓存项的 state 里，不会因为每帧渲染而重发。
func (a *app) prefetchImage(raw string, cat imageCategory) {
	if !a.autoLoad {
		return
	}
	a.images.prefetch(raw, cat, a.imageFetcher())
}

// imageFetcher 是本窗口取图与重绘的那一份实现：取图走配置的代理，结果到了
// 只让渲染这张图的窗口重绘。帧信息在渲染时由 app.windowView 记下；不在渲染
// 帧里发起的预取（数据到达时的头像）退回主窗口。
func (a *app) imageFetcher() imageFetcher {
	svc := a.frameSvc
	return imageFetcher{
		fetch: a.fetchImage,
		redraw: func() {
			if svc != (ui.Services{}) {
				imageRedraws.schedule(svc)
				return
			}
			if a.win != nil {
				a.win.Invalidate()
			}
		},
	}
}

// imageRedrawQueue 把图片加载完成的重绘请求攒到一帧再发。相邻几张图往往在
// 同一小段时间里解码完成，逐张重建界面没有意义；同一个窗口在窗口期内多次
// 请求只记一次。
type imageRedrawQueue struct {
	mu      sync.Mutex
	pending map[ui.Services]struct{}
	timer   *time.Timer
}

var imageRedraws = &imageRedrawQueue{}

func (q *imageRedrawQueue) schedule(svc ui.Services) {
	if svc == (ui.Services{}) {
		return
	}
	q.mu.Lock()
	if q.pending == nil {
		q.pending = make(map[ui.Services]struct{}, 1)
	}
	q.pending[svc] = struct{}{}
	if q.timer == nil {
		q.timer = time.AfterFunc(imageRedrawInterval, q.flush)
	}
	q.mu.Unlock()
}

func (q *imageRedrawQueue) flush() {
	q.mu.Lock()
	svcs := q.pending
	q.pending = nil
	q.timer = nil
	q.mu.Unlock()
	// Invalidate 从任意协程调用都安全，并且它自己还会把同一帧内的重复请求
	// 合并成一次重建。
	for svc := range svcs {
		svc.Invalidate()
	}
}

// embeddedRasterPattern 找 SVG 里内嵌的位图（2Libra 的小徽章图标就是这种
// 形式：一个 22×22 的 SVG，内容只是一张 512×512 PNG 的 base64）。
var embeddedRasterPattern = regexp.MustCompile(`data:image/(?:png|jpe?g|gif|webp);base64,([A-Za-z0-9+/=\s]+)`)

// decodeImageData 解码一张图片。MyGo 的 SVG 渲染器不支持 pattern / 内嵌图片，
// 而站内徽章小图正是「SVG 包一张位图」：直接交给 ui.DecodeImage 会得到一张
// 尺寸正确但什么都没有的 SVG。这里先把内嵌位图抽出来解码，抽不到再按普通
// SVG / 位图处理（天气图标就是普通矢量 SVG）。
func decodeImageData(raw string, cat imageCategory, data []byte) (ui.ImageSource, error) {
	if strings.Contains(raw, "/badge/svg/") || strings.Contains(raw, "/weather/svg/") {
		if src := embeddedRasterImage(data); src != nil {
			return src, nil
		}
	}
	// 徽章大图是「矢量外框 + pattern 内嵌位图」的混合 SVG，MyGo 不画
	// pattern：先把中间的位图合成进矢量底图，再按普通图处理。
	if src := compositeBadgeSVG(data, imageDecodeMaxSide(cat)); src != nil {
		return src, nil
	}
	return decodeBoundedImage(data, imageDecodeMaxSide(cat))
}

// decodeBoundedImage 解码一张位图，并把超过 maxSide 的图等比缩下来再交给
// mygo 建位图。原图有多大就解多大是内存暴涨的根源：一张 3072×4096 的
// JPEG 在磁盘上只有 300 KB，解成 RGBA 要 48 MB，缓存十几张就是几百 MB。
//
// 标准库认不出的数据（例如 SVG）交回 ui.DecodeImage，它还会再试一次 SVG，
// 与改造前的行为一致。
func decodeBoundedImage(data []byte, maxSide int) (ui.ImageSource, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// 动画 WebP 不在 x/image/webp 的支持范围里（ANIM 容器直接报 invalid
		// format）。退回首帧，至少能显示一张静态图，而不是整张图加载失败。
		if frame := webpFirstFrame(data); frame != nil {
			if still, _, ferr := image.Decode(bytes.NewReader(frame)); ferr == nil {
				img, err = still, nil
			}
		}
	}
	if err != nil || img == nil {
		return ui.DecodeImage(data)
	}
	if maxSide > 0 {
		img = scaleToMaxSide(img, maxSide)
	}
	// 先缩再转：EXIF 只影响方向，缩放不改变长边，顺序换过来结果一样，
	// 但转置放在小图上做能省一个数量级的像素操作。
	img = applyJPEGOrientation(img, data)
	return ui.NewBitmap(img), nil
}

// scaleToMaxSide 把长边超过 maxSide 的图等比缩到 maxSide，没超的原样返回。
func scaleToMaxSide(img image.Image, maxSide int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	long := max(w, h)
	if long <= maxSide || long == 0 {
		return img
	}
	scale := float64(maxSide) / float64(long)
	nw := max(1, int(math.Round(float64(w)*scale)))
	nh := max(1, int(math.Round(float64(h)*scale)))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	// ApproxBiLinear 在这个尺寸上的画质与速度最平衡：双三次对照片没有
	// 肉眼可见的提升，却要慢好几倍。
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	return dst
}

// applyJPEGOrientation 按 JPEG 的 EXIF 方向把图转正。mygo 的 DecodeImage
// 内部会做这件事，我们自己解码后要补上，否则手机竖拍的照片会横过来。
func applyJPEGOrientation(img image.Image, data []byte) image.Image {
	if o := jpegOrientation(data); o > 1 {
		return orientImage(img, o)
	}
	return img
}

// jpegOrientation 读 JPEG 的 EXIF Orientation（1～8），不是 JPEG、没有
// EXIF 或解析失败都返回 1（正向）。只扫到图像数据之前，不做整图解析。
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		// 无长度字段的标记：填充、RSTn 与 SOI 直接跳过。
		if marker == 0xFF || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD8) {
			i += 2
			continue
		}
		if marker == 0xDA { // 到了压缩数据，EXIF 只可能在前面的段里
			return 1
		}
		segLen := int(data[i+2])<<8 | int(data[i+3])
		if segLen < 2 || i+2+segLen > len(data) {
			return 1
		}
		if marker == 0xE1 && segLen >= 8 && string(data[i+4:i+8]) == "Exif" {
			// "Exif" 之后两个 0 字节，再往后才是 TIFF 头。
			return tiffOrientation(data[i+10 : i+2+segLen])
		}
		i += 2 + segLen
	}
	return 1
}

// tiffOrientation 在 TIFF 头里找 Orientation（tag 0x0112）。
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	if bo.Uint16(t[2:4]) != 42 {
		return 1
	}
	off := int(bo.Uint32(t[4:8]))
	if off < 8 || off+2 > len(t) {
		return 1
	}
	n := int(bo.Uint16(t[off : off+2]))
	for k := 0; k < n; k++ {
		e := off + 2 + k*12
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:e+2]) == 0x0112 {
			if o := int(bo.Uint16(t[e+8 : e+10])); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}

// orientImage 按 EXIF 方向值（2～8）把图摆正。只在解码协程里调用，图已经
// 缩到上限以内，逐像素转置的代价可以接受。
func orientImage(img image.Image, o int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 { // 5～8 要旋转 90°，宽高互换
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch o {
			case 2:
				nx, ny = w-1-x, y
			case 3:
				nx, ny = w-1-x, h-1-y
			case 4:
				nx, ny = x, h-1-y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = h-1-y, x
			case 7:
				nx, ny = h-1-y, w-1-x
			case 8:
				nx, ny = y, w-1-x
			}
			dst.Set(nx, ny, img.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
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
	// 内嵌位图同样按头像那一档压尺寸：徽章图标是 512×512 的 PNG，徽章墙
	// 一屏几十个，按原样解出来就是几十 MB。
	src, err := decodeBoundedImage(raster, imageDecodeMaxSideStable)
	if err != nil {
		return nil
	}
	return src
}

// compositeBadgeSVG 合成「矢量外框 + pattern 内嵌位图」的徽章大图。
// MyGo 的 SVG 渲染器明确不画 pattern 与内嵌图片，而站内徽章大图
// （/badge/level_10.svg 这类 256×256 图）正是这种混合结构：外框、等级文字
// 与放射底纹是 <path>，中间的奖牌是 <pattern> 引用的一张 base64 PNG。直接
// 交给 ui.DecodeImage 只会得到缺了中间的半成品（外框文字在、奖牌没了）。
// 这里把两层分别栅格化再叠在一起，还原完整徽章。
//
// 小图 /badge/svg/*.svg 整张就是一张内嵌位图，走 embeddedRasterImage 即可，
// 不进这里；天气等纯矢量 SVG 没有 pattern/base64，也不会误入。
func compositeBadgeSVG(data []byte, maxSide int) ui.ImageSource {
	img := compositeBadgeImage(data, maxSide)
	if img == nil {
		return nil
	}
	return ui.NewBitmap(img)
}

// compositeBadgeImage 是合成后的像素，拆出来便于无头测试直接断言中间与角落
// 的颜色。任何一步认不出来（没有 pattern 矩形、没有内嵌位图、viewBox 异常）
// 都返回 nil，让调用方回退到普通解码。
func compositeBadgeImage(data []byte, maxSide int) *image.RGBA {
	if !bytes.Contains(data, []byte("<pattern")) ||
		!bytes.Contains(data, []byte("base64,")) {
		return nil
	}
	rect, ok := svgPatternRect(data)
	if !ok {
		return nil
	}
	minX, minY, vbW, vbH, ok := svgViewBox(data)
	if !ok || vbW <= 0 || vbH <= 0 {
		return nil
	}
	// 栅格化尺寸取 viewBox 的固有尺寸，与 (*ui.SVG).Image 默认的 meet 适配
	// 一致；徽章 viewBox 为正方形时坐标是 1:1 映射。
	rw, rh := int(math.Ceil(vbW)), int(math.Ceil(vbH))
	if rw <= 0 || rh <= 0 {
		return nil
	}
	if maxSide > 0 && (rw > maxSide || rh > maxSide) {
		scale := float64(maxSide) / float64(max(rw, rh))
		rw, rh = max(1, int(math.Round(float64(rw)*scale))), max(1, int(math.Round(float64(rh)*scale)))
	}
	doc, err := ui.ParseSVG(data)
	if err != nil {
		return nil
	}
	base := doc.Image(rw, rh, nil)

	m := embeddedRasterPattern.FindSubmatch(data)
	if len(m) < 2 {
		return nil
	}
	encoded := strings.Join(strings.Fields(string(m[1])), "")
	raster, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}
	rasterImg, _, err := image.Decode(bytes.NewReader(raster))
	if err != nil {
		return nil
	}

	sx, sy := float64(rw)/vbW, float64(rh)/vbH
	x0 := int(math.Round((rect.x - minX) * sx))
	y0 := int(math.Round((rect.y - minY) * sy))
	x1 := int(math.Round((rect.x + rect.w - minX) * sx))
	y1 := int(math.Round((rect.y + rect.h - minY) * sy))
	x0, y0 = max(x0, 0), max(y0, 0)
	x1, y1 = min(x1, rw), min(y1, rh)
	if x1 <= x0 || y1 <= y0 {
		return nil
	}
	// pattern 里 <use transform="scale(1/512)"> 把内嵌图铺满整个矩形，
	// preserveAspectRatio="none"，所以这里直接拉伸到矩形大小，透明处保留
	// 底下的矢量底纹。
	scaled := image.NewRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	draw.ApproxBiLinear.Scale(scaled, scaled.Bounds(), rasterImg, rasterImg.Bounds(), draw.Src, nil)
	draw.Draw(base, image.Rect(x0, y0, x1, y1), scaled, image.Point{}, draw.Over)
	return base
}

// svgRect 是 SVG 里引用了 pattern 的矩形，坐标单位是 viewBox 用户单位。
type svgRect struct{ x, y, w, h float64 }

var (
	svgRectTagRe = regexp.MustCompile(`(?s)<rect\b([^>]*)>`)
	svgFillURLRe = regexp.MustCompile(`fill\s*=\s*"url\(#`)
	svgAttrsRe   = regexp.MustCompile(`([\w:-]+)\s*=\s*"([^"]*)"`)
	svgViewBoxRe = regexp.MustCompile(`(?s)\bviewBox\s*=\s*"([^"]+)"`)
)

// svgPatternRect 找第一个 fill="url(#...)" 的 <rect>，读出 x/y/width/height。
func svgPatternRect(data []byte) (svgRect, bool) {
	for _, m := range svgRectTagRe.FindAllSubmatch(data, -1) {
		attrs := string(m[1])
		if !svgFillURLRe.MatchString(attrs) {
			continue
		}
		kv := make(map[string]string)
		for _, a := range svgAttrsRe.FindAllStringSubmatch(attrs, -1) {
			kv[a[1]] = a[2]
		}
		x, okX := svgAttrFloat(kv, "x")
		y, okY := svgAttrFloat(kv, "y")
		w, okW := svgAttrFloat(kv, "width")
		h, okH := svgAttrFloat(kv, "height")
		if !okX {
			x = 0
		}
		if !okY {
			y = 0
		}
		if okW && okH && w > 0 && h > 0 {
			return svgRect{x: x, y: y, w: w, h: h}, true
		}
	}
	return svgRect{}, false
}

// svgAttrFloat 读一个 SVG 长度属性，容忍末尾的 px 单位。
func svgAttrFloat(attrs map[string]string, name string) (float64, bool) {
	s, ok := attrs[name]
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(s, "px")), 64)
	return f, err == nil
}

// svgViewBox 读 viewBox="minX minY width height"。
func svgViewBox(data []byte) (minX, minY, w, h float64, ok bool) {
	m := svgViewBoxRe.FindSubmatch(data)
	if m == nil {
		return 0, 0, 0, 0, false
	}
	parts := strings.Fields(string(m[1]))
	if len(parts) != 4 {
		return 0, 0, 0, 0, false
	}
	vals := [4]float64{}
	for i, p := range parts {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, 0, 0, 0, false
		}
		vals[i] = f
	}
	return vals[0], vals[1], vals[2], vals[3], true
}

// imageFetcher 是图片加载要的外部依赖：怎么取字节、结果到了怎么通知界面
// 重绘。图片缓存是全应用一份（见 appDeps），而取图与重绘属于各自的界面：
// 账号窗口有它自己的未读数与窗口，账号面板只有它自己那一个窗口。
type imageFetcher struct {
	fetch  func(raw string) ([]byte, error)
	redraw func()
}

// imageJob 是一张图的后台加载任务。头像这类长期缓存进高优先级队列，正文
// 配图进低优先级队列，worker 始终先取高优先级。
type imageJob struct {
	raw string
	cat imageCategory
	f   imageFetcher
}

// prefetch 登记一张图并排进后台队列。同一个地址只下一次：成功、失败还是
// 仍在下载都记在缓存项的 state 里，不会因为每帧渲染而重发。
func (c *imageCache) prefetch(raw string, cat imageCategory, f imageFetcher) {
	// 界面查状态用的是 trim 过的地址，登记缓存项也用同一个键。
	raw = strings.TrimSpace(raw)
	if _, err := parseImageURL(raw); err != nil {
		return
	}
	if _, created := c.begin(raw); !created {
		return
	}
	c.enqueue(imageJob{raw: raw, cat: cat, f: f})
}

// enqueue 把任务放到对应优先级队列，并按排队数量补 worker。队列没有上限：
// 每张图在 begin 时已去重，排队的都是还没取到的地址。
func (c *imageCache) enqueue(j imageJob) {
	c.jobMu.Lock()
	defer c.jobMu.Unlock()
	if j.cat == imageCacheStable {
		c.jobsHi = append(c.jobsHi, j)
	} else {
		c.jobsLo = append(c.jobsLo, j)
	}
	c.ensureWorkersLocked()
}

// ensureWorkersLocked 按「排队任务 + 已在跑的 worker」补足 worker，最多
// imageWorkerCount 个。只看排队数不够：worker 一旦把任务取走，队列就又短了，
// 连续入队时会永远补不满。调用方持有 c.jobMu。
func (c *imageCache) ensureWorkersLocked() {
	busy := c.workers
	want := min(imageWorkerCount, len(c.jobsHi)+len(c.jobsLo)+busy)
	for c.workers < want {
		c.workers++
		go c.work()
	}
}

// work 取图：高优先级（头像 / 表情）队列非空时先取它，否则才取帖子配图。
// 同一时刻最多 imageWorkerCount 张图在下载 / 解码。队列空了就退出，不常驻
// 持有缓存，窗口关掉或测试结束后这些图能正常回收。
func (c *imageCache) work() {
	for {
		c.jobMu.Lock()
		if len(c.jobsHi) == 0 && len(c.jobsLo) == 0 {
			c.workers--
			c.jobMu.Unlock()
			return
		}
		var j imageJob
		if len(c.jobsHi) > 0 {
			j = c.jobsHi[0]
			c.jobsHi = c.jobsHi[1:]
		} else {
			j = c.jobsLo[0]
			c.jobsLo = c.jobsLo[1:]
		}
		c.jobMu.Unlock()
		c.load(j.raw, j.cat, j.f)
	}
}

// load 先看磁盘缓存，没有再下载，解好后通知界面重绘。
func (c *imageCache) load(raw string, cat imageCategory, f imageFetcher) {
	e := c.get(raw)
	if e == nil {
		return
	}
	gen := c.generation()
	if data, ok := c.readDisk(raw, cat); ok {
		if src, err := decodeImageData(raw, cat, data); err == nil {
			e.set(src)
			f.redraw()
			return
		}
		c.forgetDisk(raw, cat)
	}
	data, err := f.fetch(raw)
	if err != nil {
		e.set(nil)
		log.Printf("图片加载失败：%v", err)
		// 失败也要重建界面：否则占位会一直停在「加载中」的转圈上。
		f.redraw()
		return
	}
	src, err := decodeImageData(raw, cat, data)
	if err != nil {
		e.set(nil)
		log.Printf("图片加载失败：%v", err)
		f.redraw()
		return
	}
	e.set(src)
	if err := c.writeDisk(raw, cat, gen, data); err != nil {
		log.Printf("图片缓存写入失败：%v", err)
	}
	f.redraw() // 走 Window.Update，让图片出现在下一帧
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
	src, err := decodeImageData(raw, cat, data)
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
func (b *accountBook) cleanExpiredImages() {
	files, bytes := b.images.cleanExpired(time.Now())
	if files > 0 {
		log.Printf("图片缓存：清理了 %d 个过期帖子图片（%s）", files, formatBytes(bytes))
	}
}

// startImageCacheMaintenance 在启动时清一次过期帖子图片，之后每
// imageCacheCleanupInterval 再清一次。图片缓存是全应用一份，所以这个循环
// 也只起一次（见 main 的 WhenReady）。无头测试不经过这里。
func (b *accountBook) startImageCacheMaintenance() {
	go func() {
		b.cleanExpiredImages()
		ticker := time.NewTicker(imageCacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			b.cleanExpiredImages()
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
