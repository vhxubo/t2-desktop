package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/netproxy"
)

// 下载加速入口：GitHub 直连（即便走代理）经常很慢或彻底不通，设置窗口的
// 「更新」块允许选一个镜像把 github.com 的链接套上镜像前缀。默认不选：检查
// 更新与下载更新包都只走「网络代理」里配置的代理，行为与没有这个功能时一致。
//
// 实现挂在 mygo.Updater.SetSource 的 UpdateSource 接口上：选中镜像后，检查更新读
// manifest 与下载更新包都会先经 mirrorURL 重写再发请求；请求本身仍走
// netproxy.Transport() 这个共享单例，代理规则、连接断开与网络调试记录照旧。
var updateMirrorOptions = []string{
	"gh-proxy.com",
	"ghproxy.net",
	"github.akams.cn",
	"ghproxy.homeboyc.cn",
}

// updateMirrorNoChoice 是「不使用加速」的展示文案，也出现在下拉的占位里。
const updateMirrorNoChoice = "不使用（走代理）"

// updateFeedBase / updateFeedRepo 复刻 mygo build 注入的更新 feed：
//
//	https://github.com/<repo>/releases/latest/download/update-<goos>-<goarch>.json
//
// mygo.json 的 updates.github 与 cmd/mygo updates.go 的 updateFile 就是这样拼
// feed 的（updates.tagPrefix 缺省 "v"，tagged() 为 false，走 latest release）。
// 改 mygo.json 的 updates.github 时要同步 updateFeedRepo；升级 mygo 时按
// updates.go 重核拼法。updateFeedBase 供测试换成 httptest 地址（模式与 mygo
// internal/update 的 GitHubAPI 变量一致）。
var (
	updateFeedBase = "https://github.com"
	updateFeedRepo = "vhxubo/t2-desktop"
)

func updateFeedURL() string {
	name := "update-" + runtime.GOOS + "-" + runtime.GOARCH + ".json"
	return updateFeedBase + "/" + updateFeedRepo + "/releases/latest/download/" + url.PathEscape(name)
}

// normalizeUpdateMirror 归一化用户选择的镜像：允许带上 https:// 前缀与结尾
// 斜杠，但不认识的一律按「不使用」处理——这功能默认关闭，手改配置文件出现
// 未知值时回到走代理的默认行为，而不是报错卡住。
func normalizeUpdateMirror(m string) string {
	m = strings.ToLower(strings.TrimSpace(m))
	m = strings.TrimPrefix(m, "https://")
	m = strings.TrimPrefix(m, "http://")
	m = strings.Trim(m, "/")
	if slices.Contains(updateMirrorOptions, m) {
		return m
	}
	return ""
}

// mirrorURL 把 github.com 的链接改写成镜像加速地址。这类镜像服务的格式是
// 「https://<mirror>/https://github.com/...」：原样接上完整地址。镜像域名之外
// 的链接（比如图床、2Libra API）不重写，保持原样。
func mirrorURL(mirror, raw string) string {
	m := normalizeUpdateMirror(mirror)
	if m == "" || !strings.HasPrefix(raw, "https://github.com/") {
		return raw
	}
	return "https://" + m + "/" + raw
}

// updateMirrorStore 是「下载加速入口」的持久化（update_mirror.json）。进程级
// 一份，挂在 appDeps 上；空串表示不使用镜像，走代理直连 GitHub。
type updateMirrorStore struct {
	dir    string
	mirror string
}

type updateMirrorConfig struct {
	Mirror string `json:"mirror,omitempty"`
}

func newUpdateMirrorStore(dir string) *updateMirrorStore {
	if dir == "" {
		dir = configDir()
	}
	s := &updateMirrorStore{dir: dir}
	var cfg updateMirrorConfig
	if err := readJSONFile(filepath.Join(dir, "update_mirror.json"), &cfg); err == nil {
		s.mirror = normalizeUpdateMirror(cfg.Mirror)
	}
	return s
}

// current 返回当前选择的镜像域名，空串表示不使用。
func (s *updateMirrorStore) current() string {
	if s == nil {
		return ""
	}
	return s.mirror
}

// set 保存选择并立即生效。写盘失败时内存里保留新值（本次运行仍按新值走），
// 错误交给调用方提示；下次启动回到旧配置。
func (s *updateMirrorStore) set(m string) error {
	if s == nil {
		return errors.New("下载加速设置不可用")
	}
	m = normalizeUpdateMirror(m)
	s.mirror = m
	if err := writeJSONFile(filepath.Join(s.dir, "update_mirror.json"), updateMirrorConfig{Mirror: m}); err != nil {
		return fmt.Errorf("保存下载加速设置失败：%w", err)
	}
	return nil
}

// Reset 恢复默认下载加速入口：不使用镜像，走网络代理直连 GitHub。
func (s *updateMirrorStore) Reset() error { return s.set("") }

// applyUpdateMirror 按 mirror 的取值挂上或撤下自定义更新源。SetSource(nil) 恢复
// mygo 默认的 feed 读取；传镜像时换成 mirrorSource。任何 goroutine 可调。
func applyUpdateMirror(m string) {
	m = normalizeUpdateMirror(m)
	if m == "" {
		mygo.Updater.SetSource(nil)
		return
	}
	mygo.Updater.SetSource(mirrorSource{mirror: m})
}

// mirrorSource 是选中镜像后的更新源：manifest 与更新包都经镜像转发。它实现
// mygo 的 UpdateSource 接口，由 applyUpdateMirror 通过 SetSource 挂到更新器上。
//
// 本仓库不启用 prerelease（没有调 SetPrereleases 的地方），Manifest 的 pre 恒
// 为 false；latest feed 也没有 {version} 占位符，mygo 默认链路对它原样返回，
// 所以这里直取 latest feed 即可，不需要复刻 prerelease 的 GitHub API 解析。
type mirrorSource struct {
	mirror string
}

func (s mirrorSource) Manifest(ctx context.Context, pre bool) (io.ReadCloser, error) {
	return updateGet(ctx, mirrorURL(s.mirror, updateFeedURL()))
}

func (s mirrorSource) Open(ctx context.Context, raw string) (io.ReadCloser, error) {
	return updateGet(ctx, mirrorURL(s.mirror, raw))
}

/* ------------------------------------------------------------------
   updateGet：镜像路径上的 HTTP 取回

   复刻 mygo v0.10.0 updater.go httpGet 的关键行为（升级 mygo 时对照重核）：
   只走 HTTPS（回环 http 除外，供测试）、User-Agent 固定 "mygo-updater"
   （GitHub 的 release CDN 对非 ASCII UA 回 400）、瞬时错误重试两次、响应头
   30 秒看门狗、响应体 60 秒停滞看门狗。与 mygo 版的差异只有一点：响应头超时
   用 time.AfterFunc 的取消实现，而不是 transport 的 ResponseHeaderTimeout——
   这样可以直接复用 netproxy.Transport() 共享单例（代理规则、断开空闲连接、
   网络调试记录都自动生效），不用另养一份要接进 closeIdleConnections 的
   transport。
---------------------------------------------------------------- */

const (
	updateHeaderTimeout = 30 * time.Second
	updateStallTimeout  = time.Minute
)

// updateRetries 与 mygo httpRetries 相同：三次尝试，后两次分别等 1s、3s。
var updateRetries = []time.Duration{time.Second, 3 * time.Second}

func updateGet(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return nil, fmt.Errorf("拒绝下载 %s：更新只走 HTTPS", rawURL)
	}
	client := &http.Client{Transport: netproxy.Transport()}
	for try := 0; ; try++ {
		// 请求取消由两个看门狗负责：响应头迟迟不来就取消整个请求；响应体
		// 交给 stalledUpdateBody，60 秒没有新数据同样取消。
		reqCtx, cancel := context.WithCancel(ctx)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("User-Agent", "mygo-updater")
		watchdog := time.AfterFunc(updateHeaderTimeout, cancel)
		resp, err := client.Do(req)
		watchdog.Stop()
		var wait time.Duration
		switch {
		case err != nil:
			cancel()
			if ctx.Err() != nil || try >= len(updateRetries) {
				return nil, err
			}
		case resp.StatusCode == http.StatusOK:
			return newStalledUpdateBody(resp.Body, cancel), nil
		default:
			resp.Body.Close()
			cancel()
			if !transientUpdateStatus(resp.StatusCode) || try >= len(updateRetries) {
				return nil, fmt.Errorf("GET %s: %s", rawURL, resp.Status)
			}
			wait = updateRetryAfter(resp.Header.Get("Retry-After"))
		}
		wait = max(wait, updateRetries[try])
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// transientUpdateStatus 与 mygo transientStatus 相同：限流或网关暂时故障，稍
// 再试可能就成了。
func transientUpdateStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// updateRetryAfter 与 mygo retryAfter 相同：只认整数秒，封顶 30 秒。
func updateRetryAfter(h string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || n <= 0 {
		return 0
	}
	return min(time.Duration(n)*time.Second, 30*time.Second)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// stalledUpdateBody 是 mygo stalledBody 的复刻：读不到新数据就取消请求，并把
// 错误说成停滞而不是取消，背景检查不必为一个哑掉的连接等到底。
type stalledUpdateBody struct {
	body   io.ReadCloser
	cancel context.CancelFunc
	d      time.Duration
	last   atomic.Int64
	stall  atomic.Bool
	done   atomic.Bool
	timer  *time.Timer
}

func newStalledUpdateBody(body io.ReadCloser, cancel context.CancelFunc) *stalledUpdateBody {
	b := &stalledUpdateBody{body: body, cancel: cancel, d: updateStallTimeout}
	b.last.Store(time.Now().UnixNano())
	// 先发布 timer 再允许它的回调读字段。
	b.timer = time.AfterFunc(1<<63-1, b.check)
	b.timer.Stop()
	b.timer.Reset(b.d / 4)
	return b
}

func (b *stalledUpdateBody) check() {
	if b.done.Load() {
		return
	}
	if time.Since(time.Unix(0, b.last.Load())) >= b.d {
		b.stall.Store(true)
		b.cancel()
		return
	}
	b.timer.Reset(b.d / 4)
}

func (b *stalledUpdateBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.last.Store(time.Now().UnixNano())
	}
	if err != nil && b.stall.Load() {
		return n, errors.New("下载停滞：连接不再返回数据")
	}
	return n, err
}

func (b *stalledUpdateBody) Close() error {
	b.done.Store(true)
	b.timer.Stop()
	b.cancel()
	return b.body.Close()
}
