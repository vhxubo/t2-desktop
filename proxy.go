package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/netproxy"
	"golang.org/x/net/http/httpproxy"
)

// 代理模式。默认跟随系统：先看 HTTP(S)_PROXY / ALL_PROXY 环境变量，再看
// 系统代理（Windows 的 WinHTTP、macOS 的 CFNetwork、Linux 的 GIO，含 PAC
// 与自动检测）。手动模式只认应用内填写的地址，直连模式跳过所有代理。三种
// 模式都通过同一个 http.Transport 生效；启动时 installGlobalProxy 还会把
// 这套规则接到 mygo 的更新插件和 http.DefaultTransport 上，让检查更新、
// 更新包下载与所有默认客户端也一起走配置的代理。
const (
	proxyFollowSystem = iota
	proxyManual
	proxyDirect
)

// proxyConfig 是持久化到配置目录 proxy.json 的内容。密码按明文保存在
// 0o600 权限的文件里：这是本机代理凭据，和 token.json 同一处存放。
type proxyConfig struct {
	Mode     int    `json:"mode"` // 0 跟随系统 / 1 手动 / 2 直连
	URL      string `json:"url,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// NoProxy 用逗号或分号分隔；与常见 NO_PROXY、Windows「跳过代理」列表同义。
	NoProxy string `json:"no_proxy,omitempty"`
}

func (c proxyConfig) normalized() proxyConfig {
	if c.Mode < proxyFollowSystem || c.Mode > proxyDirect {
		c.Mode = proxyFollowSystem
	}
	c.URL = strings.TrimSpace(c.URL)
	c.Username = strings.TrimSpace(c.Username)
	c.NoProxy = strings.TrimSpace(c.NoProxy)
	return c
}

func (c proxyConfig) validate() error {
	switch c.Mode {
	case proxyFollowSystem, proxyDirect:
		return nil
	case proxyManual:
		_, err := manualProxyURL(c)
		return err
	default:
		return errors.New("未知的代理模式")
	}
}

// manualProxyURL 校验并归一化手动填写的地址。省略协议时按 http:// 处理，
// 于是「127.0.0.1:7890」这种常见写法也能直接用。
func manualProxyURL(c proxyConfig) (*url.URL, error) {
	raw := strings.TrimSpace(c.URL)
	if raw == "" {
		return nil, errors.New("请填写代理地址，例如 http://127.0.0.1:7890")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("代理地址无法解析：%v", err)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	case "socks": // 未写明版本的 SOCKS，按 SOCKS5 处理（与 mygo netproxy 一致）
		u.Scheme = "socks5"
	default:
		return nil, fmt.Errorf("不支持的代理协议 %q，只支持 http、https、socks5", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, errors.New("代理地址缺少主机名")
	}
	if u.Path != "" && u.Path != "/" {
		return nil, errors.New("代理地址不能带路径")
	}
	if c.Username != "" {
		u.User = url.UserPassword(c.Username, c.Password)
	}
	return u, nil
}

// proxyManager 持有当前配置和共享 transport。Proxy 函数在每次请求时读取
// 已解析好的规则，所以保存配置后新请求立即生效，无需重启应用。
type proxyManager struct {
	dir string

	mu        sync.Mutex
	cfg       proxyConfig
	fn        func(*url.URL) (*url.URL, error)
	status    string
	transport *http.Transport
	// net 是进程级的网络请求记录器（见 netlog.go）：Client() 返回的客户端
	// 都包了记录层，网络调试窗口展示的就是它记下的请求。
	net *netRecorder
	// global 是 mygo 内部（更新插件等）共用的 transport。它的 Proxy 由
	// installGlobalProxy 换成本管理器的 proxyFor，改配置时也要断开它上面
	// 的空闲连接，否则检查更新可能继续复用连着旧代理的连接。
	global *http.Transport
	// rateLimit 是全应用共享的限流退避闸门（见 rate_limit.go）：所有走这份
	// 代理配置的窗口和后台轮询共用，一处被限流，其余请求在退避期内跳过。
	rateLimit *rateLimitGate
	// connectivity 是进程级的网络在线状态机（见 netstatus.go）。请求失败时由
	// netLogTransport 报告，确认离线后拦下后续请求、暂停重试并弹窗。
	connectivity *networkStatus
}

// rateLimitGate 返回这份代理配置下的共享闸门；零值代理管理器没有闸门，
// 调用方应退回自己私有的一份。
func (p *proxyManager) rateLimitGate() *rateLimitGate {
	if p == nil {
		return nil
	}
	return p.rateLimit
}

// netStatus 返回这份配置下的共享网络状态机；零值代理管理器没有状态机。
func (p *proxyManager) netStatus() *networkStatus {
	if p == nil {
		return nil
	}
	return p.connectivity
}

func newProxyManager(dir string) *proxyManager {
	if dir == "" {
		dir = configDir()
	}
	p := &proxyManager{dir: dir, cfg: proxyConfig{Mode: proxyFollowSystem},
		net: newNetRecorder(), rateLimit: newRateLimitGate()}
	p.connectivity = newNetworkStatus(p.probeConnectivity)
	var cfg proxyConfig
	if err := readJSONFile(filepath.Join(dir, "proxy.json"), &cfg); err == nil {
		p.cfg = cfg.normalized()
	}
	p.reload()
	return p
}

// Reload 重新探测系统/环境代理。设置面板打开时和点「重新检测」时调用，
// 这样用户在 Clash 之类工具里改了系统代理后不必重启应用。
func (p *proxyManager) Reload() {
	p.reload()
	p.closeIdleConnections()
}

func (p *proxyManager) reload() {
	p.mu.Lock()
	cfg := p.cfg
	p.mu.Unlock()

	fn, status := resolveProxy(cfg)

	p.mu.Lock()
	p.fn = fn
	p.status = status
	p.mu.Unlock()
}

// Transport 是应用里所有 HTTP 客户端共用的 transport。共享它意味着代理
// 规则只解析一次、连接池也共享，而且改配置时只需要 CloseIdleConnections。
func (p *proxyManager) Transport() *http.Transport {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.transport == nil {
		// 默认模板从 http.DefaultTransport 复制。它正常是 *http.Transport；
		// 记录层接管后（installGlobalProxy 换成了 netLogTransport）退回一份
		// 空白默认值，保证任何时候都能建出可用的共享 transport。
		if base, ok := http.DefaultTransport.(*http.Transport); ok {
			p.transport = base.Clone()
		} else {
			p.transport = (&http.Transport{}).Clone()
		}
		p.transport.Proxy = p.proxyFor
	}
	return p.transport
}

// netTransport 是包了请求记录层的共享 transport：Client() 与
// installGlobalProxy 都从这拿，普通 API、SSE、图床上传、图片下载与兜底的
// 默认客户端因此都会被网络调试窗口记录。
func (p *proxyManager) netTransport() http.RoundTripper {
	return &netLogTransport{inner: p.Transport(), rec: p.net, status: p.connectivity}
}

// closeIdleConnections 断开所有共享 transport 上的空闲连接。换代理地址后
// 连接池里连着旧代理的连接必须作废，否则检查更新这类复用连接的请求会绕过
// 新配置，看起来像「保存了没生效」。
func (p *proxyManager) closeIdleConnections() {
	p.Transport().CloseIdleConnections()
	p.mu.Lock()
	global := p.global
	p.mu.Unlock()
	if global != nil {
		global.CloseIdleConnections()
	}
}

// installGlobalProxy 把应用配置的代理规则接到进程里其余的 HTTP 出口。
// 必须在启动阶段、任何请求发出之前、以及装载更新插件之前调用一次。
//
// 应用自己的 Client / AuthManager / SSE / 图床上传 / 图片下载都显式使用
// proxyManager.Transport()，但还有两类请求拿不到它：
//
//   - mygo 的更新插件：检查更新时读 GitHub Release 的 manifest 与 prerelease
//     列表、以及下载更新包，内部固定用
//     (&http.Client{Transport: netproxy.Transport()}).Do(...)。那个 transport
//     只认系统代理和环境变量，应用里填的手动代理、账号密码和「不走代理的
//     地址」对它完全无效 —— 这正是「配了代理却检查不了更新」的原因；
//   - http.DefaultClient，以及任何 Transport 为 nil 的 http.Client（本项目
//     的兜底客户端、第三方库的默认客户端）：它们走 http.DefaultTransport。
//
// 默认 transport 直接换成共享 transport；netproxy 的共享 transport 换成同
// 一个 Proxy 函数。proxyFor 每次请求都读当前规则，所以在设置窗口保存代理、
// 点「重新检测」之后，检查更新也立即跟着切换，不用重启应用。
//
// 注意这里依赖 mygo v0.10.0 的 updater.go/httpGet：客户端 transport 源自
// netproxy.Transport().Clone()，Clone 发生在本函数换掉 nt.Proxy 之后，所以
// Proxy 字段仍指向 p.proxyFor。升级 mygo 时要重新核对那一行，并跑
// TestUpdateCheckUsesConfiguredProxy。
func installGlobalProxy(p *proxyManager) {
	// 必须先初始化 netproxy 的共享单例，再换掉 http.DefaultTransport：
	// netproxy.Transport() 首次调用会把 DefaultTransport 断言成
	// *http.Transport 并克隆（netproxy.go 的 lazy.get），断言失败会 panic。
	// 顺序反了只在真实启动时炸——测试进程里这个 sync.Once 单例早已被
	// 其他用例初始化，掩盖了顺序问题。
	nt := netproxy.Transport()

	http.DefaultTransport = p.netTransport()

	nt.Proxy = p.proxyFor
	p.mu.Lock()
	p.global = nt
	p.mu.Unlock()
}

func (p *proxyManager) proxyFor(req *http.Request) (*url.URL, error) {
	// 更新插件走 netproxy 的共享单例，绕不进记录层：这里按 context 标记
	// 认出还没记过的请求，补一条仅请求侧的记录（见 netlog.go）。
	p.net.noteUnwrapped(req)
	p.mu.Lock()
	fn := p.fn
	p.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(req.URL)
}

// Client 返回一个使用共享 transport（含请求记录层）的客户端。timeout 为 0
// 时不设整体超时，供 SSE 长连接使用。
func (p *proxyManager) Client(timeout time.Duration) *http.Client {
	return &http.Client{Transport: p.netTransport(), Timeout: timeout}
}

// Snapshot 返回已保存的配置和当前生效状态的说明。
func (p *proxyManager) Snapshot() (proxyConfig, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg, p.status
}

func (p *proxyManager) Status() string {
	_, status := p.Snapshot()
	return status
}

// Save 先校验再落盘，保存成功后立即换用新规则并断开旧连接池里的空闲
// 连接；否则复用的连接还可能绕过新代理。
func (p *proxyManager) Save(cfg proxyConfig) error {
	cfg = cfg.normalized()
	if err := cfg.validate(); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(p.dir, "proxy.json"), cfg); err != nil {
		return fmt.Errorf("保存代理配置失败：%w", err)
	}
	p.mu.Lock()
	p.cfg = cfg
	p.mu.Unlock()
	p.Reload()
	return nil
}

// resolveProxy 把配置变成 Proxy 函数和一句给人看的状态说明。
func resolveProxy(cfg proxyConfig) (func(*url.URL) (*url.URL, error), string) {
	switch cfg.Mode {
	case proxyDirect:
		return nil, "直连（不使用代理）"

	case proxyManual:
		u, err := manualProxyURL(cfg)
		if err != nil {
			return nil, "手动代理配置无效：" + err.Error() + "（已按直连处理）"
		}
		noProxy, bypassLocal := splitNoProxy(cfg.NoProxy)
		raw := u.String()
		return newProxyFunc(raw, raw, noProxy, bypassLocal), "手动代理 " + u.Redacted()

	default:
		httpProxy, httpsProxy, noProxy := environmentProxy()
		if httpProxy != "" || httpsProxy != "" {
			list, bypassLocal := splitNoProxy(noProxy)
			return newProxyFunc(httpProxy, httpsProxy, list, bypassLocal),
				"环境变量代理 " + describeProxies(httpProxy, httpsProxy)
		}
		// 系统设置交给 mygo 的 netproxy：Windows 走 WinHTTP（含 PAC / 自动
		// 检测），macOS 走 CFNetwork，Linux 走 GIO/libproxy；结果缓存 5 秒，
		// 用户切换系统代理后后续请求会跟着变，不必重启应用。
		return func(u *url.URL) (*url.URL, error) {
			return netproxy.Proxy(&http.Request{URL: u})
		}, "跟随系统代理（含 PAC / 自动检测）"
	}
}

// environmentProxy 读取 HTTP(S)_PROXY / ALL_PROXY / NO_PROXY；小写优先，
// 与 net/http 的约定一致。ALL_PROXY 作为两类请求共同的兜底。
func environmentProxy() (httpProxy, httpsProxy, noProxy string) {
	httpProxy = envAny("http_proxy", "HTTP_PROXY")
	httpsProxy = envAny("https_proxy", "HTTPS_PROXY")
	all := envAny("all_proxy", "ALL_PROXY")
	if httpProxy == "" {
		httpProxy = all
	}
	if httpsProxy == "" {
		httpsProxy = all
	}
	noProxy = envAny("no_proxy", "NO_PROXY")
	return
}

func envAny(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

// newProxyFunc 用 x/net/http/httpproxy 处理 NO_PROXY、CIDR 和通配域名；
// bypassLocal 对应 Windows 的 <local>（不带点的主机名）。
func newProxyFunc(httpProxy, httpsProxy, noProxy string, bypassLocal bool) func(*url.URL) (*url.URL, error) {
	cfg := &httpproxy.Config{
		HTTPProxy:  httpProxy,
		HTTPSProxy: httpsProxy,
		NoProxy:    noProxy,
	}
	fn := cfg.ProxyFunc()
	if !bypassLocal {
		return fn
	}
	return func(u *url.URL) (*url.URL, error) {
		if isSimpleHostname(u.Hostname()) {
			return nil, nil
		}
		return fn(u)
	}
}

func isSimpleHostname(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" || net.ParseIP(host) != nil {
		return false
	}
	return !strings.Contains(host, ".")
}

// describeProxies 把 HTTP/HTTPS 两个地址压成一行，并隐去密码。
func describeProxies(httpProxy, httpsProxy string) string {
	h := redactedProxy(httpProxy)
	s := redactedProxy(httpsProxy)
	switch {
	case h == "" && s == "":
		return "未设置"
	case h == s:
		return h
	case h == "":
		return "HTTPS " + s
	case s == "":
		return "HTTP " + h
	default:
		return "HTTP " + h + "；HTTPS " + s
	}
}

func redactedProxy(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Redacted()
}

/* ------------------------------------------------------------------
   连通性测试（设置窗口「测试连通性」按钮）
---------------------------------------------------------------- */

// proxyCheckTimeout 是单次测试的上限：代理不通时不要让用户干等。
const proxyCheckTimeout = 8 * time.Second

// proxyCheckTarget 是测试访问的地址：公开读端点，不需要登录、响应也很小。
// 用例会把它换成不可解析的域名，用来确认请求只到达假代理。
var proxyCheckTarget = apiBaseURL + "/v1/topics/latest?limit=1"

// proxyCheckResult 是一次测试的结果。Status 为 0 表示没拿到 HTTP 响应。
type proxyCheckResult struct {
	Via     string        // 这次用的线路说明，和「当前生效」是同一句话
	Status  int           // 目标或代理返回的 HTTP 状态码
	Elapsed time.Duration // 从发起请求到拿到响应头
	Err     error         // 连接 / 传输失败的原因
}

// proxyForURL 把按目标 URL 解析的规则包成 http.Transport 需要的形状；
// 直连模式的规则为 nil，transport 的 Proxy 也保持 nil。
func proxyForURL(fn func(*url.URL) (*url.URL, error)) func(*http.Request) (*url.URL, error) {
	if fn == nil {
		return nil
	}
	return func(req *http.Request) (*url.URL, error) { return fn(req.URL) }
}

// Check 用 cfg（可以是设置窗口里还没保存的草稿）解析出的规则访问 target。
// 它不改正在生效的配置：transport 从共享 transport 复制一份，只换 Proxy，
// 于是账号密码、「不走代理的地址」等规则照样生效，测试用的连接也不会留在
// 共享连接池里。没有自建裸客户端，请求不会绕过配置的代理。
func (p *proxyManager) Check(cfg proxyConfig, target string) proxyCheckResult {
	cfg = cfg.normalized()
	if err := cfg.validate(); err != nil {
		return proxyCheckResult{Err: err}
	}
	fn, via := resolveProxy(cfg)
	tr := p.Transport().Clone()
	tr.Proxy = proxyForURL(fn)
	defer tr.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), proxyCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return proxyCheckResult{Via: via, Err: err}
	}
	req.Header.Set("User-Agent", "2libra-native-reader/0.1 (+mygo)")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")

	client := &http.Client{
		Transport: tr,
		Timeout:   proxyCheckTimeout,
		// 重定向本身也算一次响应，不再往新地址发第二轮请求。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return proxyCheckResult{Via: via, Elapsed: elapsed, Err: err}
	}
	defer resp.Body.Close()
	// 只读一小段就把连接还回去：测试不需要整页内容。
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10))
	return proxyCheckResult{Via: via, Status: resp.StatusCode, Elapsed: elapsed}
}

// Message 把结果写成一句窗口内 toast 文案。
func (r proxyCheckResult) Message() string {
	if r.Err != nil {
		reason := proxyCheckErrorText(r.Err)
		if r.Via == "" {
			return "连通性测试失败：" + reason
		}
		return "连通性测试失败 · " + r.Via + " · " + reason
	}
	cost := r.Elapsed.Round(time.Millisecond)
	if cost < time.Millisecond {
		cost = time.Millisecond
	}
	switch {
	case r.Status >= 500:
		return fmt.Sprintf("连接失败 · HTTP %d · 用时 %s · %s", r.Status, cost, r.Via)
	case r.Status >= 400:
		return fmt.Sprintf("线路已通但接口返回 HTTP %d · 用时 %s · %s", r.Status, cost, r.Via)
	default:
		return fmt.Sprintf("连接正常 · HTTP %d · 用时 %s · %s", r.Status, cost, r.Via)
	}
}

// proxyCheckErrorText 把传输错误说成人能看懂的原因。
func proxyCheckErrorText(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "连接超时，请检查代理地址与网络"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "连接超时，请检查代理地址与网络"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "域名解析失败：" + dns.Name
	}
	return friendlyError(err)
}

// splitNoProxy 统一处理逗号/分号分隔的例外列表，并识别 Windows 的
// <local>。<-loopback> 表示「连回环地址也走代理」，这里的 transport 始终
// 直连 127.0.0.1 等地址（httpproxy 的内置行为），因此忽略该项。
func splitNoProxy(raw string) (string, bool) {
	var out []string
	bypassLocal := false
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	}) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		switch strings.ToLower(part) {
		case "<local>":
			bypassLocal = true
		case "<-loopback>":
			// 忽略：回环地址仍然直连。
		default:
			out = append(out, part)
		}
	}
	return strings.Join(out, ","), bypassLocal
}
