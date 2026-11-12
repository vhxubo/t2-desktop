package main

import (
	"errors"
	"fmt"
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
// 模式都通过同一个 http.Transport 生效，所以 API、OAuth、SSE 长连接和图片
// 下载不会有漏网之鱼。
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
}

func newProxyManager(dir string) *proxyManager {
	if dir == "" {
		dir = configDir()
	}
	p := &proxyManager{dir: dir, cfg: proxyConfig{Mode: proxyFollowSystem}}
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
	p.Transport().CloseIdleConnections()
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
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = p.proxyFor
		p.transport = tr
	}
	return p.transport
}

func (p *proxyManager) proxyFor(req *http.Request) (*url.URL, error) {
	p.mu.Lock()
	fn := p.fn
	p.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(req.URL)
}

// Client 返回一个使用共享 transport 的客户端。timeout 为 0 时不设整体
// 超时，供 SSE 长连接使用。
func (p *proxyManager) Client(timeout time.Duration) *http.Client {
	return &http.Client{Transport: p.Transport(), Timeout: timeout}
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
