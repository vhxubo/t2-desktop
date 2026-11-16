package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/netproxy"
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
	// Skyimg 这类要先取 CSRF 令牌再上传的适配器会复制一份客户端加 cookie
	// jar，复制品必须继续共用同一个 transport（也就是同一个代理）。
	if got := c.clientWithJar(nil).Transport; got != shared {
		t.Errorf("带 cookie jar 的图床客户端没有使用共享 transport：%T", got)
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

// 设置是独立的窗口：主页侧边栏底部只剩一个「设置」入口，代理、检查更新与
// 图片缓存都在那个窗口里。无头测试没有 mygo 主循环，所以直接渲染设置视图。
func TestSettingsProxySectionSaves(t *testing.T) {
	clearProxyEnv(t)
	a := testApp(t)
	opened := false
	a.onSettingsOpen = func() { opened = true }
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if _, ok := tt.Find("设置"); !ok {
		t.Fatalf("侧边栏没有「设置」入口：%q", tt.Texts())
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
	cfg, status := a.proxy.Snapshot()
	if cfg.Mode != proxyManual || cfg.URL != "127.0.0.1:7890" || cfg.NoProxy != "localhost" {
		t.Errorf("保存后的配置 = %+v", cfg)
	}
	if !strings.Contains(status, "http://127.0.0.1:7890") {
		t.Errorf("状态没有显示手动代理：%q", status)
	}
	// 保存结果用窗口内 toast 说明，面板里不再留一行提示文字。
	st.Frame()
	if !st.HasText("已保存，新请求立即生效") {
		t.Errorf("保存成功没有弹出 toast：%q", st.Texts())
	}

	// 无效地址不落盘、不静默按直连跑，原因同样用 toast 说明；草稿原样留在
	// 输入框里等用户改。
	a.proxyURL = "ftp://127.0.0.1:21"
	st.Frame()
	if err := st.Click("保存代理设置"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !st.HasText("不支持") {
		t.Errorf("无效代理没有报错：%q", st.Texts())
	}
	if cfg, _ := a.proxy.Snapshot(); cfg.URL != "127.0.0.1:7890" {
		t.Errorf("无效地址被写进配置：%+v", cfg)
	}
}

// 设置窗口里必须有检查更新入口，点击时接到 updater 上（无头测试用钩子
// 确认接线，不碰更新插件的主循环）。
func TestSettingsCheckForUpdatesWired(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabUpdates
	clicked := false
	a.onUpdateCheck = func() { clicked = true }
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, settingsWindowHeight)
	st.Frame()
	if !st.HasText("检查更新") {
		t.Fatalf("设置窗口没有检查更新：%q", st.Texts())
	}
	if !st.HasText("都会走「网络代理」里设置的代理") {
		t.Errorf("设置窗口没有说明检查更新也走代理：%q", st.Texts())
	}
	if err := st.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	if !clicked {
		t.Fatal("点击「检查更新」没有触发更新检查")
	}
}

/* ------------------------------------------------------------------
   设置窗口分栏与代理连通性测试
---------------------------------------------------------------- */

// 设置按分类分页：首帧只画「网络代理」这一块，其它分类的按钮不再把页面
// 撑成一条要滚很久的长列表；切页之后只显示新分类的内容。
func TestSettingsTabsShowOneSectionAtATime(t *testing.T) {
	a := testApp(t)
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, settingsWindowHeight)
	st.Frame()
	for _, want := range []string{"网络代理", "测试连通性", "保存代理设置", "当前生效"} {
		if !st.HasText(want) {
			t.Errorf("默认分类缺少 %q：%q", want, st.Texts())
		}
	}
	for _, hidden := range []string{"保存图库设置", "清理缓存", "检查更新", "关闭主窗口时"} {
		if st.HasText(hidden) {
			t.Errorf("默认分类不该显示其它页的 %q：%q", hidden, st.Texts())
		}
	}

	if err := st.Click("图库"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.settingsTab != settingsTabHosts {
		t.Fatalf("点「图库」后分类 = %d", a.settingsTab)
	}
	if !st.HasText("保存图库设置") || st.HasText("保存代理设置") {
		t.Errorf("切到图库页后内容不对：%q", st.Texts())
	}

	if err := st.Click("图片缓存"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !st.HasText("清理缓存") || st.HasText("保存图库设置") {
		t.Errorf("切到图片缓存页后内容不对：%q", st.Texts())
	}
}

// 连通性测试用的是「草稿」配置：不必先保存，请求也必须经过填的代理，
// 账号密码照常带到代理上。
func TestProxyCheckUsesConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	var hits atomic.Int32
	var auth atomic.Value
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if v := r.Header.Get("Proxy-Authorization"); v != "" {
			auth.Store(v)
		}
		_, _ = fmt.Fprint(w, proxyTestEnvelope)
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	res := p.Check(proxyConfig{
		Mode: proxyManual, URL: proxySrv.URL,
		Username: "alice", Password: "secret",
	}, "http://upstream.invalid/api/v1/topics/latest?limit=1")
	if res.Err != nil {
		t.Fatalf("经代理测试失败：%v", res.Err)
	}
	if hits.Load() != 1 {
		t.Errorf("代理收到 %d 个请求，期望 1", hits.Load())
	}
	if res.Status != http.StatusOK || !strings.Contains(res.Via, "手动代理") {
		t.Errorf("结果 = %+v", res)
	}
	if !strings.Contains(res.Message(), "连接正常") {
		t.Errorf("成功文案 = %q", res.Message())
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))
	if got, _ := auth.Load().(string); got != wantAuth {
		t.Errorf("Proxy-Authorization = %q，期望 %q", got, wantAuth)
	}
}

// 直连模式不该把测试请求送进代理；「当前生效」也要说明这次走的是直连。
func TestProxyCheckDirectModeSkipsProxy(t *testing.T) {
	clearProxyEnv(t)
	var proxyHits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		_, _ = fmt.Fprint(w, proxyTestEnvelope)
	}))
	defer proxySrv.Close()
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		_, _ = fmt.Fprint(w, proxyTestEnvelope)
	}))
	defer target.Close()

	p := newProxyManager(t.TempDir())
	res := p.Check(proxyConfig{Mode: proxyDirect}, target.URL)
	if res.Err != nil || res.Status != http.StatusOK {
		t.Fatalf("直连测试结果 = %+v", res)
	}
	if proxyHits.Load() != 0 || targetHits.Load() != 1 {
		t.Errorf("代理命中 %d 次、目标命中 %d 次", proxyHits.Load(), targetHits.Load())
	}
	if !strings.Contains(res.Via, "直连") {
		t.Errorf("线路说明 = %q", res.Via)
	}
}

// 代理连不上时明确报失败，不能把超时或拒绝连接当成「线路可用」。
func TestProxyCheckReportsUnreachableProxy(t *testing.T) {
	clearProxyEnv(t)
	p := newProxyManager(t.TempDir())
	res := p.Check(proxyConfig{Mode: proxyManual, URL: "http://127.0.0.1:1"},
		"http://upstream.invalid/api/v1/topics/latest?limit=1")
	if res.Err == nil || res.Status != 0 {
		t.Fatalf("连不上的代理却得到结果：%+v", res)
	}
	if !strings.Contains(res.Message(), "连通性测试失败") {
		t.Errorf("失败文案 = %q", res.Message())
	}
}

// 无头窗口里点「测试连通性」：进度在页面里，结果用窗口内 toast 报告。
func TestSettingsProxyCheckButtonReportsResult(t *testing.T) {
	clearProxyEnv(t)
	var hits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = fmt.Fprint(w, proxyTestEnvelope)
	}))
	defer proxySrv.Close()
	old := proxyCheckTarget
	proxyCheckTarget = "http://upstream.invalid/api/v1/topics/latest?limit=1"
	t.Cleanup(func() { proxyCheckTarget = old })

	a := testApp(t)
	a.proxyMode = proxyManual
	a.proxyURL = proxySrv.URL
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, settingsWindowHeight)
	st.Frame()
	if !st.HasText("测试连通性") {
		t.Fatalf("代理页没有测试入口：%q", st.Texts())
	}
	if err := st.Click("测试连通性"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if busy, _ := a.proxyCheck.snapshot(); !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("连通性测试没有结束")
		}
		time.Sleep(5 * time.Millisecond)
	}
	st.Frame()
	if hits.Load() != 1 {
		t.Errorf("代理收到 %d 个请求，期望 1", hits.Load())
	}
	if !st.HasText("连接正常") {
		t.Errorf("没有弹出成功 toast：%q", st.Texts())
	}
}

/* ------------------------------------------------------------------
   全局出口：检查更新、默认客户端、图床与图片链接
---------------------------------------------------------------- */

// 更新插件不用本应用的 Client：mygo v0.8.3 的 updater.go/httpGet 固定用
// (&http.Client{Transport: netproxy.Transport()}).Do(...)，只认系统代理与
// 环境变量。installGlobalProxy 把那个共享 transport 的 Proxy 换成应用配置。
// 下面几条用例都按更新插件的真实客户端发请求，验证「配好代理就能检查更新」。

// installGlobalProxyForTest 安装后还原两个进程级变量，避免污染其它用例。
// 用例内没有并发请求，替换 transport 的 Proxy 字段是安全的。
func installGlobalProxyForTest(t *testing.T, p *proxyManager) {
	t.Helper()
	oldDefault := http.DefaultTransport
	nt := netproxy.Transport()
	oldProxy := nt.Proxy
	t.Cleanup(func() {
		http.DefaultTransport = oldDefault
		nt.Proxy = oldProxy
	})
	installGlobalProxy(p)
}

const updateManifestJSON = `{"version":"9.9.9","date":"2026-11-14T00:00:00Z","url":"https://github.com/vhxubo/t2-desktop/releases/download/v9.9.9/t2-desktop-windows-amd64.tar.gz","signature":"AAAA"}`

func TestUpdateCheckUsesConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	var hits atomic.Int32
	var connectHost atomic.Value
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			connectHost.Store(r.Host)
			// 假代理不真的开隧道；这条请求只用来确认 HTTPS 流量到了代理。
			http.Error(w, "tunnel closed", http.StatusBadGateway)
			return
		}
		hits.Add(1)
		_, _ = fmt.Fprint(w, updateManifestJSON)
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	installGlobalProxyForTest(t, p)

	wantHost := strings.TrimPrefix(proxySrv.URL, "http://")
	// 更新插件只发 HTTPS 请求（mygo 的 httpGet 拒绝非 HTTPS 目标）：直接问
	// transport 的 Proxy 函数，确认 manifest 与 GitHub API 的 prerelease
	// 列表这两个真实地址都会落到应用里填的代理上。
	for _, raw := range []string{
		"https://github.com/vhxubo/t2-desktop/releases/latest/download/update-windows-amd64.json",
		"https://api.github.com/repos/vhxubo/t2-desktop/releases?per_page=100&page=1",
	} {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		u, err := netproxy.Transport().Proxy(req)
		if err != nil {
			t.Fatalf("检查更新请求 %s 的代理解析失败：%v", raw, err)
		}
		if u == nil || u.Host != wantHost {
			t.Fatalf("检查更新请求 %s 没有走配置的代理：%v", raw, u)
		}
	}

	// 再按更新插件 httpGet 的方式真正发一次请求并读回 manifest：证明这条
	// 链路不只是「接上了」，而是能经代理完成一次完整的检查。这里用 http
	// 假域名让 httptest 假代理直接应答；HTTPS 走 CONNECT 用的是同一个
	// Proxy 决策，上面已经断言过。
	resp, err := (&http.Client{Transport: netproxy.Transport()}).Get(
		"http://upstream.invalid/releases/latest/download/update-windows-amd64.json")
	if err != nil {
		t.Fatalf("经代理检查更新失败：%v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"version":"9.9.9"`) {
		t.Errorf("没有读回更新 manifest：%q", body)
	}
	if hits.Load() != 1 {
		t.Errorf("代理收到 %d 个更新请求，期望 1", hits.Load())
	}

	// 真实的检查更新地址是 HTTPS：Go transport 会先向代理发 CONNECT。
	// 假代理故意拒绝隧道，只断言这条 CONNECT 确实打到了配置的代理。
	httpsClient := &http.Client{Transport: netproxy.Transport()}
	_, _ = httpsClient.Get(
		"https://github.com/vhxubo/t2-desktop/releases/latest/download/update-windows-amd64.json")
	httpsClient.CloseIdleConnections()
	if got, _ := connectHost.Load().(string); got != "github.com:443" {
		t.Errorf("HTTPS 检查更新没有向代理发起 CONNECT，代理看到 %q", got)
	}
}

// 跟随系统模式读环境变量时，检查更新同样要跟着走代理。
func TestEnvironmentProxyAlsoCoversUpdateCheck(t *testing.T) {
	clearProxyEnv(t)
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, updateManifestJSON)
	}))
	defer proxySrv.Close()
	t.Setenv("HTTPS_PROXY", proxySrv.URL)

	p := newProxyManager(t.TempDir())
	installGlobalProxyForTest(t, p)
	req, err := http.NewRequest(http.MethodGet,
		"https://github.com/vhxubo/t2-desktop/releases/latest/download/update-windows-amd64.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := netproxy.Transport().Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil || u.Host != strings.TrimPrefix(proxySrv.URL, "http://") {
		t.Errorf("环境变量代理没有覆盖检查更新：%v", u)
	}
}

// http.DefaultClient 以及任何 Transport 为 nil 的 http.Client 都走默认
// transport；换过之后这些兜底路径也不会绕过代理。
func TestDefaultTransportUsesConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	var hits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = fmt.Fprint(w, "ok")
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	installGlobalProxyForTest(t, p)

	resp, err := http.Get("http://upstream.invalid/ping")
	if err != nil {
		t.Fatalf("默认客户端经代理请求失败：%v", err)
	}
	resp.Body.Close()
	if hits.Load() != 1 {
		t.Errorf("默认 transport 没走代理，代理收到 %d 个请求", hits.Load())
	}
}

// 设置窗口保存新代理后，检查更新必须立即改走新地址，且不再复用连着旧代理
// 的空闲连接。
func TestSavingProxyReroutesUpdateCheck(t *testing.T) {
	clearProxyEnv(t)
	var hitsA, hitsB atomic.Int32
	newProxy := func(hits *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			_, _ = fmt.Fprint(w, updateManifestJSON)
		}))
	}
	srvA, srvB := newProxy(&hitsA), newProxy(&hitsB)
	defer srvA.Close()
	defer srvB.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: srvA.URL}); err != nil {
		t.Fatal(err)
	}
	installGlobalProxyForTest(t, p)
	client := &http.Client{Transport: netproxy.Transport()}
	check := func() {
		t.Helper()
		resp, err := client.Get("http://upstream.invalid/update.json")
		if err != nil {
			t.Fatalf("检查更新失败：%v", err)
		}
		resp.Body.Close()
	}
	check()
	if hitsA.Load() != 1 || hitsB.Load() != 0 {
		t.Fatalf("第一次检查更新没有走旧代理：A=%d B=%d", hitsA.Load(), hitsB.Load())
	}

	if err := p.Save(proxyConfig{Mode: proxyManual, URL: srvB.URL}); err != nil {
		t.Fatal(err)
	}
	check()
	if hitsB.Load() != 1 {
		t.Errorf("保存新代理后检查更新没有走新代理：B=%d", hitsB.Load())
	}
	if hitsA.Load() != 1 {
		t.Errorf("旧代理还收到 %d 次请求，期望 1", hitsA.Load())
	}
}

func TestDirectModeUpdateCheckBypassesProxy(t *testing.T) {
	clearProxyEnv(t)
	var proxyHits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		_, _ = fmt.Fprint(w, updateManifestJSON)
	}))
	defer proxySrv.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, updateManifestJSON)
	}))
	defer target.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	installGlobalProxyForTest(t, p)
	if err := p.Save(proxyConfig{Mode: proxyDirect}); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodGet,
		"https://github.com/vhxubo/t2-desktop/releases/latest/download/update-windows-amd64.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if u, err := netproxy.Transport().Proxy(req); err != nil || u != nil {
		t.Errorf("直连模式下检查更新仍被送进代理：%v，错误 %v", u, err)
	}
	resp, err := (&http.Client{Transport: netproxy.Transport()}).Get(target.URL + "/update.json")
	if err != nil {
		t.Fatalf("直连模式下检查更新失败：%v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"version"`) || proxyHits.Load() != 0 {
		t.Errorf("直连模式结果不对：代理收到 %d 次，响应 %q", proxyHits.Load(), body)
	}
}

// 图床上传（第三方 multipart 接口）和图床返回的图片链接（正文里渲染时
// 下载）都必须经过代理，不能出现「列表走代理、传图与看图直连」的漏口。
func TestImageHostAndImageURLUseConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	var uploads, downloads atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/upload"):
			uploads.Add(1)
			_, _ = fmt.Fprint(w, "https://img.example/pic.png\n")
		case strings.Contains(r.URL.Path, "/pic.png"):
			downloads.Add(1)
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testUploadPNG())
		default:
			http.NotFound(w, r)
		}
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	a := &app{client: &Client{Proxy: p}}
	h := imageHost{
		ID: "proxy-host", Name: "代理图床", Kind: imageHostGeneric,
		Endpoint: "http://upstream.invalid/upload", Field: "file",
		TextResponse: true, MaxBytes: defaultImageMaxBytes, Enabled: true,
	}
	u, err := a.uploadImage(context.Background(), h,
		uploadSource{Name: "p.png", Mime: "image/png", Data: testUploadPNG()})
	if err != nil {
		t.Fatalf("经代理上传图片失败：%v", err)
	}
	if u != "https://img.example/pic.png" {
		t.Errorf("图床返回的地址 = %q", u)
	}

	data, err := a.fetchImage("http://img.example/pic.png")
	if err != nil {
		t.Fatalf("经代理下载图片链接失败：%v", err)
	}
	if len(data) == 0 {
		t.Error("图片内容为空")
	}
	if uploads.Load() != 1 || downloads.Load() != 1 {
		t.Errorf("代理收到上传 %d 次、图片下载 %d 次，期望各 1 次", uploads.Load(), downloads.Load())
	}
}

// 编辑帖子 / 编辑评论 / 追加附言这三个写端点也必须走配置的代理。它们和读端点
// 共用同一个 Client，但「共用」这件事要用假代理证明一次，不能只靠约定：新加的
// 出口一旦自建 http.Client，代理规则就会在这里漏掉。
func TestEditEndpointsUseConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	var mu sync.Mutex
	var paths []string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_, _ = fmt.Fprint(w, envelopeJSON(`{}`))
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	c := &Client{
		Proxy:  p,
		Base:   "http://upstream.invalid/api",
		bearer: func() string { return "tok" },
	}
	ctx := context.Background()
	if err := c.editTopic(ctx, "a1b2", "新标题", "新正文"); err != nil {
		t.Fatalf("经代理编辑帖子失败：%v", err)
	}
	if err := c.editReply(ctx, "c1", "新评论"); err != nil {
		t.Fatalf("经代理编辑评论失败：%v", err)
	}
	if err := c.addPostscript(ctx, "a1b2", "附言", "k1"); err != nil {
		t.Fatalf("经代理追加附言失败：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"/api/v1/topics/a1b2/edit",
		"/api/v1/replies/c1/edit",
		"/api/v1/topics/a1b2/postscript",
	}
	if len(paths) != len(want) {
		t.Fatalf("代理收到 %d 次请求，期望 %d 次：%v", len(paths), len(want), paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("第 %d 次请求 = %s，期望 %s", i+1, paths[i], want[i])
		}
	}
}

// 盲盒的延期是又一个用 postJSON 的写出口：必须经配置的代理，不能因为
// 和开盒 / 重抽共用 Client 就假设它天然不会直连。
func TestPandoraExtendUsesConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	var mu sync.Mutex
	var paths, bodies []string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		bodies = append(bodies, string(body))
		mu.Unlock()
		_, _ = fmt.Fprint(w, envelopeJSON(`{"cost":12,"next_cost":20,"active_extended":true}`))
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	c := &Client{
		Proxy:  p,
		Base:   "http://upstream.invalid/api",
		bearer: func() string { return "tok" },
	}
	res, err := c.extendPandora(context.Background(), "k1", "b1")
	if err != nil {
		t.Fatalf("经代理延期失败：%v", err)
	}
	if !res.CostKnown || res.Cost != 12 || !res.NextCostKnown || res.NextCost != 20 {
		t.Errorf("延期响应解析不对：%+v", res)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "POST /api/v1/pandora/extend" {
		t.Fatalf("代理收到的请求 = %v，期望延期 POST", paths)
	}
	if !strings.Contains(bodies[0], `"box_instance_id":"b1"`) {
		t.Errorf("延期请求体不对：%q", bodies[0])
	}
}

// 竞猜的读（GET /bets）与写（POST participate）两个出口都要走配置的代理。
func TestBetEndpointsUseConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	var mu sync.Mutex
	var paths []string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		_, _ = fmt.Fprint(w, envelopeJSON(`{
            "title":"经代理的竞猜","options":[{"id":"opt-a","text":"甲","total_coins":5}]
        }`))
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	c := &Client{
		Proxy:  p,
		Base:   "http://upstream.invalid/api",
		bearer: func() string { return "tok" },
	}
	ctx := context.Background()
	if _, err := c.fetchTopicBets(ctx, "a1b2"); err != nil {
		t.Fatalf("经代理读竞猜失败：%v", err)
	}
	if err := c.participateBet(ctx, "k1", "a1b2", "opt-a", 20); err != nil {
		t.Fatalf("经代理参与竞猜失败：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"GET /api/v1/topics/a1b2/bets",
		"POST /api/v1/topics/a1b2/bets/opt-a/participate",
	}
	if len(paths) != len(want) {
		t.Fatalf("代理收到 %d 次请求，期望 %d 次：%v", len(paths), len(want), paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("第 %d 次请求 = %s，期望 %s", i+1, paths[i], want[i])
		}
	}
}

// 受限内容三个读出口与解锁写出口都必须经配置的代理，不能因为共用 Client
// 就假设其中某一个天然不会直连。
func TestRestrictedEndpointsUseConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	var mu sync.Mutex
	var paths []string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		_, _ = fmt.Fprint(w, envelopeJSON(`{"unlocked":true,"content":"经代理的正文"}`))
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	c := &Client{
		Proxy:  p,
		Base:   "http://upstream.invalid/api",
		bearer: func() string { return "tok" },
	}
	ctx := context.Background()
	if _, err := c.fetchRestricted(ctx, "r1"); err != nil {
		t.Fatalf("经代理读受限内容失败：%v", err)
	}
	if _, err := c.fetchRestrictedAuction(ctx, "r1"); err != nil {
		t.Fatalf("经代理读竞拍状态失败：%v", err)
	}
	if _, err := c.fetchRestrictedBids(ctx, "r1"); err != nil {
		t.Fatalf("经代理读出价记录失败：%v", err)
	}
	if _, err := c.unlockRestricted(ctx, "k1", "r1"); err != nil {
		t.Fatalf("经代理解锁失败：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"GET /api/v1/topics/r1/restricted",
		"GET /api/v1/topics/r1/restricted/auction",
		"GET /api/v1/topics/r1/restricted/auction/bids",
		"POST /api/v1/topics/r1/restricted/unlock",
	}
	if len(paths) != len(want) {
		t.Fatalf("代理收到 %d 次请求，期望 %d 次：%v", len(paths), len(want), paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("第 %d 次请求 = %s，期望 %s", i+1, paths[i], want[i])
		}
	}
}
