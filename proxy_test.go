package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

const proxyTestEnvelope = `{"c":0,"m":"请求成功","d":{
	"posts":[{"id":"proxy-1","short_id":"PXY001","title":"经代理加载的内容",
		"author":{"username":"jimmy"}}],
	"total":1,"page":1,"limit":50,"total_pages":1}}`

// clearProxyEnv 把环境变量代理清空，让用例只验证自己要设的那一项。
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"http_proxy", "HTTP_PROXY",
		"https_proxy", "HTTPS_PROXY",
		"all_proxy", "ALL_PROXY",
		"no_proxy", "NO_PROXY",
	} {
		t.Setenv(name, "")
	}
}

func TestManualProxyRoutesRequests(t *testing.T) {
	clearProxyEnv(t)
	var hits atomic.Int32
	var proxyAuth atomic.Value
	var targetHost atomic.Value
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		targetHost.Store(r.Host)
		if v := r.Header.Get("Proxy-Authorization"); v != "" {
			proxyAuth.Store(v)
		}
		_, _ = fmt.Fprint(w, proxyTestEnvelope)
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{
		Mode: proxyManual, URL: proxySrv.URL,
		Username: "alice", Password: "secret",
	}); err != nil {
		t.Fatal(err)
	}
	// 目标域名无法解析也没关系：请求应该只到代理，由代理给出响应。
	c := &Client{Proxy: p, Base: "http://upstream.invalid/api"}
	res, err := c.fetch(context.Background(), tabSpecs[0], 1)
	if err != nil {
		t.Fatalf("经代理请求失败：%v", err)
	}
	if len(res.Items) != 1 || res.Items[0].Title != "经代理加载的内容" {
		t.Errorf("代理响应没有解析出来：%+v", res.Items)
	}
	if hits.Load() != 1 {
		t.Errorf("代理收到 %d 个请求，期望 1", hits.Load())
	}
	if got, _ := targetHost.Load().(string); got != "upstream.invalid" {
		t.Errorf("代理看到的目标主机 = %q，期望 upstream.invalid", got)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))
	if got, _ := proxyAuth.Load().(string); got != wantAuth {
		t.Errorf("Proxy-Authorization = %q，期望 %q", got, wantAuth)
	}
}

func TestFollowSystemUsesAllProxyFallback(t *testing.T) {
	clearProxyEnv(t)
	var hits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = fmt.Fprint(w, proxyTestEnvelope)
	}))
	defer proxySrv.Close()
	t.Setenv("ALL_PROXY", proxySrv.URL)

	p := newProxyManager(t.TempDir())
	c := &Client{Proxy: p, Base: "http://upstream.invalid/api"}
	if _, err := c.fetch(context.Background(), tabSpecs[0], 1); err != nil {
		t.Fatalf("ALL_PROXY 兜底失败：%v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("ALL_PROXY 收到 %d 个请求，期望 1", hits.Load())
	}
}

func TestFollowSystemAndDirectModes(t *testing.T) {
	clearProxyEnv(t)
	p := newProxyManager(t.TempDir())
	if status := p.Status(); !strings.Contains(status, "系统代理") {
		t.Errorf("默认模式没有说明跟随系统代理：%q", status)
	}
	p.mu.Lock()
	follow := p.fn
	p.mu.Unlock()
	if follow == nil {
		t.Error("跟随系统模式没有可用的 Proxy 函数")
	}

	if err := p.Save(proxyConfig{Mode: proxyDirect}); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	direct := p.fn
	p.mu.Unlock()
	if direct != nil {
		t.Error("直连模式仍然返回代理")
	}
	if status := p.Status(); !strings.Contains(status, "直连") {
		t.Errorf("直连模式状态不对：%q", status)
	}
}

func TestFollowSystemUsesEnvironmentProxy(t *testing.T) {
	clearProxyEnv(t)
	var hits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = fmt.Fprint(w, proxyTestEnvelope)
	}))
	defer proxySrv.Close()
	t.Setenv("HTTP_PROXY", proxySrv.URL)

	p := newProxyManager(t.TempDir())
	cfg, status := p.Snapshot()
	if cfg.Mode != proxyFollowSystem {
		t.Fatalf("默认模式 = %d，期望跟随系统", cfg.Mode)
	}
	if !strings.Contains(status, "环境变量代理") {
		t.Errorf("状态没有说明环境变量代理：%q", status)
	}
	c := &Client{Proxy: p, Base: "http://upstream.invalid/api"}
	if _, err := c.fetch(context.Background(), tabSpecs[0], 1); err != nil {
		t.Fatalf("经环境变量代理请求失败：%v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("代理收到 %d 个请求，期望 1", hits.Load())
	}
}

// 回环地址必须直连：OAuth 回调、测试服务器与本机服务都不该被送进代理。
func TestLoopbackIsNeverProxied(t *testing.T) {
	clearProxyEnv(t)
	var proxyHits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		_, _ = fmt.Fprint(w, proxyTestEnvelope)
	}))
	defer proxySrv.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, proxyTestEnvelope)
	}))
	defer target.Close()
	t.Setenv("HTTP_PROXY", proxySrv.URL)

	p := newProxyManager(t.TempDir())
	c := &Client{Proxy: p, Base: target.URL + "/api"}
	if _, err := c.fetch(context.Background(), tabSpecs[0], 1); err != nil {
		t.Fatalf("回环地址请求失败：%v", err)
	}
	if proxyHits.Load() != 0 {
		t.Errorf("回环地址被送进代理 %d 次", proxyHits.Load())
	}
}

func TestManualProxyOverridesEnvironment(t *testing.T) {
	clearProxyEnv(t)
	// 环境里先放一个必然连不上的代理：手动配置必须压过它。
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	var hits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = fmt.Fprint(w, proxyTestEnvelope)
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	c := &Client{Proxy: p, Base: "http://upstream.invalid/api"}
	if _, err := c.fetch(context.Background(), tabSpecs[0], 1); err != nil {
		t.Fatalf("手动代理没有压过环境变量：%v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("手动代理收到 %d 个请求，期望 1", hits.Load())
	}
}

func TestProxyReloadPicksUpNewEnvironment(t *testing.T) {
	clearProxyEnv(t)
	p := newProxyManager(t.TempDir())

	var hits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = fmt.Fprint(w, proxyTestEnvelope)
	}))
	defer proxySrv.Close()
	t.Setenv("HTTP_PROXY", proxySrv.URL)
	p.Reload()

	c := &Client{Proxy: p, Base: "http://upstream.invalid/api"}
	if _, err := c.fetch(context.Background(), tabSpecs[0], 1); err != nil {
		t.Fatalf("重新检测后没有使用新代理：%v", err)
	}
	if hits.Load() != 1 {
		t.Errorf("重新检测后代理收到 %d 个请求，期望 1", hits.Load())
	}
}

// 所有网络路径必须共用同一个 transport：漏掉 SSE 或 OAuth 就会出现
// 「列表走了代理，通知长连接/登录却直连」的半吊子状态。
func TestProxySharedByAPIStreamAndOAuth(t *testing.T) {
	p := newProxyManager(t.TempDir())
	shared := p.Transport()

	c := &Client{Proxy: p}
	if got := c.httpClient().Transport; got != shared {
		t.Errorf("普通 API 请求没有使用共享 transport：%T", got)
	}
	if got := c.streamHTTPClient().Transport; got != shared {
		t.Errorf("SSE 长连接没有使用共享 transport：%T", got)
	}
	m := newAuthManagerAt(t.TempDir(), p)
	if got := m.http.Transport; got != shared {
		t.Errorf("OAuth 请求没有使用共享 transport：%T", got)
	}
}

func TestManualProxyURLNormalization(t *testing.T) {
	u, err := manualProxyURL(proxyConfig{
		Mode: proxyManual, URL: "SOCKS://127.0.0.1:1080",
		Username: "u", Password: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "socks5" || u.Host != "127.0.0.1:1080" {
		t.Errorf("归一化结果 = %s", u.Redacted())
	}
	if got := u.User.Username(); got != "u" {
		t.Errorf("用户名 = %q", got)
	}
	if p, _ := u.User.Password(); p != "p" {
		t.Errorf("密码没有传入代理地址")
	}

	u, err = manualProxyURL(proxyConfig{Mode: proxyManual, URL: "127.0.0.1:7890"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "http" || u.Host != "127.0.0.1:7890" {
		t.Errorf("省略协议时应按 http 处理，实际 %s", u)
	}
}

func TestProxyConfigPersistsAndRedactsPassword(t *testing.T) {
	dir := t.TempDir()
	p := newProxyManager(dir)
	want := proxyConfig{
		Mode: proxyManual, URL: "socks5://127.0.0.1:1080",
		Username: "u", Password: "p", NoProxy: "localhost, .corp.example",
	}
	if err := p.Save(want); err != nil {
		t.Fatal(err)
	}

	q := newProxyManager(dir)
	got, status := q.Snapshot()
	if got != want {
		t.Errorf("重新加载的配置 = %+v，期望 %+v", got, want)
	}
	if strings.Contains(status, "p@") {
		t.Errorf("状态里泄露了密码：%q", status)
	}
	if !strings.Contains(status, "socks5://u:xxxxx@127.0.0.1:1080") {
		t.Errorf("状态没有显示脱敏后的代理地址：%q", status)
	}
}

func TestProxyInvalidManualDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	p := newProxyManager(dir)
	good := proxyConfig{Mode: proxyManual, URL: "http://127.0.0.1:7890"}
	if err := p.Save(good); err != nil {
		t.Fatal(err)
	}

	bad := []proxyConfig{
		{Mode: proxyManual, URL: ""},
		{Mode: proxyManual, URL: "ftp://127.0.0.1:21"},
		{Mode: proxyManual, URL: "http://127.0.0.1:8080/some/path"},
		{Mode: proxyManual, URL: "not a url"},
	}
	for _, cfg := range bad {
		if err := p.Save(cfg); err == nil {
			t.Errorf("无效代理配置 %q 竟然保存成功", cfg.URL)
		}
		if got, _ := p.Snapshot(); got != good {
			t.Errorf("保存失败后配置被改写：%+v", got)
		}
	}
	onDisk := newProxyManager(dir)
	if got, _ := onDisk.Snapshot(); got != good {
		t.Errorf("保存失败后磁盘配置被改写：%+v", got)
	}
}

func TestProxyBadManualInFileFallsBackToDirect(t *testing.T) {
	dir := t.TempDir()
	raw := `{"mode":1,"url":"ftp://127.0.0.1:21"}`
	if err := os.WriteFile(filepath.Join(dir, "proxy.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p := newProxyManager(dir)
	if status := p.Status(); !strings.Contains(status, "配置无效") {
		t.Errorf("手改坏配置后状态没有提示：%q", status)
	}
	req, err := http.NewRequest(http.MethodGet, "http://example.com/api", nil)
	if err != nil {
		t.Fatal(err)
	}
	if proxy, err := p.proxyFor(req); proxy != nil || err != nil {
		t.Errorf("坏配置应回退到直连，实际代理 = %v，错误 = %v", proxy, err)
	}
}

func TestProxyUnknownModeNormalizesToSystem(t *testing.T) {
	dir := t.TempDir()
	raw := `{"mode":9,"url":"http://127.0.0.1:7890"}`
	if err := os.WriteFile(filepath.Join(dir, "proxy.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	p := newProxyManager(dir)
	if cfg, _ := p.Snapshot(); cfg.Mode != proxyFollowSystem {
		t.Errorf("未知代理模式 = %d，期望回落到跟随系统", cfg.Mode)
	}
}

func TestSplitNoProxy(t *testing.T) {
	cases := []struct {
		raw         string
		want        string
		bypassLocal bool
	}{
		{"", "", false},
		{"localhost;127.0.0.1", "localhost,127.0.0.1", false},
		{"<local>;*.corp.example;.foo", "*.corp.example,.foo", true},
		{"<-loopback>;10.0.0.0/8", "10.0.0.0/8", false},
		{"a, b ,, c", "a,b,c", false},
	}
	for _, c := range cases {
		got, bypassLocal := splitNoProxy(c.raw)
		if got != c.want || bypassLocal != c.bypassLocal {
			t.Errorf("splitNoProxy(%q) = (%q, %v)，期望 (%q, %v)",
				c.raw, got, bypassLocal, c.want, c.bypassLocal)
		}
	}
}

// 设置是独立的窗口：标题栏只剩一个「设置」入口，代理、检查更新与图片
// 缓存都在那个窗口里。无头测试没有 mygo 主循环，所以直接渲染设置视图。
func TestSettingsProxySectionSaves(t *testing.T) {
	clearProxyEnv(t)
	a := testApp(t)
	opened := false
	a.onSettingsOpen = func() { opened = true }
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if _, ok := tt.Find("设置"); !ok {
		t.Fatalf("标题栏没有「设置」入口：%q", tt.Texts())
	}
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Fatal("点击「设置」没有打开设置窗口")
	}

	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, settingsWindowHeight)
	st.Frame()
	for _, want := range []string{"设置", "网络代理", "跟随系统", "手动代理", "直连", "当前生效"} {
		if !st.HasText(want) {
			t.Errorf("设置窗口缺少 %q：%q", want, st.Texts())
		}
	}

	if err := st.Click("手动代理"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.proxyMode != proxyManual {
		t.Fatalf("分段控件没有切到手动代理：%d", a.proxyMode)
	}
	if _, ok := st.Find("代理地址"); !ok {
		t.Fatalf("手动模式没有代理地址输入框：%q", st.Texts())
	}

	a.proxyURL = "127.0.0.1:7890"
	a.proxyNoProxy = "localhost"
	st.Frame()
	if err := st.Click("保存代理设置"); err != nil {
		t.Fatal(err)
	}
	if a.proxyMsgErr {
		t.Fatalf("保存有效代理失败：%s", a.proxyMsg)
	}
	cfg, status := a.proxy.Snapshot()
	if cfg.Mode != proxyManual || cfg.URL != "127.0.0.1:7890" || cfg.NoProxy != "localhost" {
		t.Errorf("保存后的配置 = %+v", cfg)
	}
	if !strings.Contains(status, "http://127.0.0.1:7890") {
		t.Errorf("状态没有显示手动代理：%q", status)
	}

	// 无效地址留在面板里给出错误，不能悄悄按直连跑。
	a.proxyURL = "ftp://127.0.0.1:21"
	st.Frame()
	if err := st.Click("保存代理设置"); err != nil {
		t.Fatal(err)
	}
	if !a.proxyMsgErr || !strings.Contains(a.proxyMsg, "不支持") {
		t.Errorf("无效代理没有在面板内报错：err=%v msg=%q", a.proxyMsgErr, a.proxyMsg)
	}
}

// 设置窗口里必须有检查更新入口，点击时接到 updater 上（无头测试用钩子
// 确认接线，不碰更新插件的主循环）。
func TestSettingsCheckForUpdatesWired(t *testing.T) {
	a := testApp(t)
	clicked := false
	a.onUpdateCheck = func() { clicked = true }
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, settingsWindowHeight)
	st.Frame()
	if !st.HasText("检查更新") {
		t.Fatalf("设置窗口没有检查更新：%q", st.Texts())
	}
	if err := st.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	if !clicked {
		t.Fatal("点击「检查更新」没有触发更新检查")
	}
}
