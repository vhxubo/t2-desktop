package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 镜像重写：github.com 的链接套上前缀，其它链接原样；镜像名允许带协议与
// 结尾斜杠，不认识的一律按「不使用」归空。
func TestMirrorURLRewrite(t *testing.T) {
	for _, c := range []struct {
		mirror, raw, want string
	}{
		{"", "https://github.com/vhxubo/t2-desktop/releases/download/v1/app.tar.gz",
			"https://github.com/vhxubo/t2-desktop/releases/download/v1/app.tar.gz"},
		{"gh-proxy.com",
			"https://github.com/vhxubo/t2-desktop/releases/download/v1/app.tar.gz",
			"https://gh-proxy.com/https://github.com/vhxubo/t2-desktop/releases/download/v1/app.tar.gz"},
		{"https://ghproxy.net/",
			"https://github.com/vhxubo/t2-desktop/releases/latest/download/update-windows-amd64.json",
			"https://ghproxy.net/https://github.com/vhxubo/t2-desktop/releases/latest/download/update-windows-amd64.json"},
		{"github.akams.cn", "https://example.com/file.tar.gz", "https://example.com/file.tar.gz"},
		{"ghproxy.homeboyc.cn", "http://127.0.0.1:8080/x", "http://127.0.0.1:8080/x"},
		{"not-a-mirror.example", "https://github.com/vhxubo/t2-desktop/a", "https://github.com/vhxubo/t2-desktop/a"},
	} {
		if got := mirrorURL(c.mirror, c.raw); got != c.want {
			t.Errorf("mirrorURL(%q, %q) = %q，期望 %q", c.mirror, c.raw, got, c.want)
		}
	}
	if normalizeUpdateMirror("https://GH-proxy.com/") != "gh-proxy.com" {
		t.Errorf("镜像名归一化失败：%q", normalizeUpdateMirror("https://GH-proxy.com/"))
	}
	if normalizeUpdateMirror("") != "" || normalizeUpdateMirror("nope.example") != "" {
		t.Errorf("未知镜像没有归空")
	}
}

// 「下载加速」下拉的选项：首项「不使用（走代理）」的 Value 是空串，选中它就
// 清空加速入口；后面跟着全部候选镜像，顺序与 updateMirrorOptions 一致。
func TestUpdateMirrorSelectOptions(t *testing.T) {
	options := updateMirrorSelectOptions()
	if len(options) != len(updateMirrorOptions)+1 {
		t.Fatalf("选项数量 = %d，期望 %d", len(options), len(updateMirrorOptions)+1)
	}
	if options[0].Value != "" || options[0].Label != updateMirrorNoChoice {
		t.Errorf("首项不是清空项：%+v", options[0])
	}
	for i, m := range updateMirrorOptions {
		if options[i+1].Value != m || options[i+1].Label != m {
			t.Errorf("第 %d 个选项 = %+v，期望 %s", i+1, options[i+1], m)
		}
	}
}

// 下载加速选择落盘：重新读回还是同一个值；手改文件出现未知值时归回「不使用」。
func TestUpdateMirrorStorePersist(t *testing.T) {
	dir := t.TempDir()
	s := newUpdateMirrorStore(dir)
	if s.current() != "" {
		t.Fatalf("默认选择不是「不使用」：%q", s.current())
	}
	if err := s.set("gh-proxy.com"); err != nil {
		t.Fatal(err)
	}
	if again := newUpdateMirrorStore(dir); again.current() != "gh-proxy.com" {
		t.Errorf("重新读回的选择 = %q，期望 gh-proxy.com", again.current())
	}
	if err := s.set("https://ghproxy.net/"); err != nil {
		t.Fatal(err)
	}
	if again := newUpdateMirrorStore(dir); again.current() != "ghproxy.net" {
		t.Errorf("带协议前缀的选择没有归一化：%q", again.current())
	}
	if err := writeJSONFile(dir+"/update_mirror.json", updateMirrorConfig{Mirror: "evil.example"}); err != nil {
		t.Fatal(err)
	}
	if again := newUpdateMirrorStore(dir); again.current() != "" {
		t.Errorf("配置文件里的未知镜像没有归空：%q", again.current())
	}
}

// 选中镜像后，检查更新（manifest）与更新包下载都改走镜像域名，而且请求仍然
// 经过「网络代理」里配置的代理：HTTPS 目标由 transport 向代理发起 CONNECT，
// 假代理记下隧道主机名即可断言两件事都成立。
func TestUpdateMirrorSourceGoesThroughMirrorAndProxy(t *testing.T) {
	clearProxyEnv(t)
	var connectHost atomic.Value
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			connectHost.Store(r.Host)
			http.Error(w, "tunnel closed", http.StatusBadGateway)
			return
		}
		http.Error(w, "unexpected", http.StatusForbidden)
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	installGlobalProxyForTest(t, p)

	src := mirrorSource{mirror: "gh-proxy.com"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 下载更新包：重写后的目标主机是镜像域名，CONNECT 打到配置的代理。
	_, _ = src.Open(ctx,
		"https://github.com/vhxubo/t2-desktop/releases/download/v9.9.9/t2-desktop-windows-amd64.tar.gz")
	if got, _ := connectHost.Load().(string); got != "gh-proxy.com:443" {
		t.Errorf("更新包下载没有经镜像走配置的代理，代理看到 %q", got)
	}

	// 检查更新读 manifest：feed 同样被重写。
	connectHost.Store("")
	_, _ = src.Manifest(ctx, false)
	if got, _ := connectHost.Load().(string); got != "gh-proxy.com:443" {
		t.Errorf("检查更新没有经镜像走配置的代理，代理看到 %q", got)
	}

	// 不选镜像时恢复默认：目标主机回到 github.com，代理决策不变。
	connectHost.Store("")
	_, _ = updateGet(ctx,
		"https://github.com/vhxubo/t2-desktop/releases/latest/download/update-windows-amd64.json")
	if got, _ := connectHost.Load().(string); got != "github.com:443" {
		t.Errorf("不选镜像时没有直连 github.com，代理看到 %q", got)
	}
}

// 打开「接收测试版」并选了加速入口时，新增的 GitHub API 请求同样走「网络
// 代理」里配置的代理；API 不经镜像（镜像只转发 github.com 上的文件）。假代理
// 不开隧道，只记下 CONNECT 的主机。
func TestUpdatePrereleaseAPIUsesConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	var connectHost atomic.Value
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			connectHost.Store(r.Host)
			http.Error(w, "tunnel closed", http.StatusForbidden)
			return
		}
		http.Error(w, "unexpected", http.StatusForbidden)
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	installGlobalProxyForTest(t, p)
	old := updateAPIBase
	updateAPIBase = "https://api.github.com"
	t.Cleanup(func() { updateAPIBase = old })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = (mirrorSource{mirror: "gh-proxy.com"}).Manifest(ctx, true)
	if got, _ := connectHost.Load().(string); got != "api.github.com:443" {
		t.Errorf("测试版的 release 列表没有走配置的代理，代理看到 %q", got)
	}
}

// mirrorSource 的 Manifest 拉的是 latest release 的平台 manifest。这里把 feed
// 基址换成 httptest 地址（updateGet 允许回环 http），断言路径与读回内容。
func TestUpdateMirrorSourceManifestFeed(t *testing.T) {
	var gotPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "mygo-updater" {
			http.Error(w, "bad user agent", http.StatusBadRequest)
			return
		}
		gotPath.Store(r.URL.Path)
		_, _ = fmt.Fprint(w, updateManifestJSON)
	}))
	defer srv.Close()

	old := updateFeedBase
	updateFeedBase = srv.URL
	t.Cleanup(func() { updateFeedBase = old })

	rc, err := mirrorSource{}.Manifest(context.Background(), false)
	if err != nil {
		t.Fatalf("拉取 manifest 失败：%v", err)
	}
	body, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("/vhxubo/t2-desktop/releases/latest/download/update-%s-%s.json",
		runtime.GOOS, runtime.GOARCH)
	if p, _ := gotPath.Load().(string); p != want {
		t.Errorf("manifest 路径 = %q，期望 %q", p, want)
	}
	if !strings.Contains(string(body), `"version":"9.9.9"`) {
		t.Errorf("没有读回更新 manifest：%q", body)
	}
}

// updateGet 只认 HTTPS（回环 http 除外）：防止镜像配置把更新流量引到明文
// HTTP 上。
func TestUpdateGetRequiresHTTPS(t *testing.T) {
	if _, err := updateGet(context.Background(), "http://example.com/app.tar.gz"); err == nil ||
		!strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("非 HTTPS 目标没有被拒绝：%v", err)
	}
}
