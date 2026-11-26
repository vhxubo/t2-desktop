package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// waitNetStatus 轮询条件直到成立或超时。用于等后台探测 goroutine 完成。
func waitNetStatus(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func TestIsNetworkError(t *testing.T) {
	wrapped := func(err error) error {
		return &url.Error{Op: "Get", URL: "https://2libra.com/api", Err: err}
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"离线哨兵", errOffline, false},
		{"DNS 失败", wrapped(&net.DNSError{IsNotFound: true}), true},
		{"连接被拒", wrapped(&net.OpError{Op: "dial", Err: errors.New("connection refused")}), true},
		{"整体超时", wrapped(context.DeadlineExceeded), true},
		{"连接被提前关闭", wrapped(io.EOF), true},
		{"用户取消", wrapped(context.Canceled), false},
		{"API 错误", &apiError{Status: 500}, false},
		{"限流退避", rateLimitWaitError(time.Second), false},
		{"普通错误", errors.New("响应不是信封"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNetworkError(tc.err); got != tc.want {
				t.Errorf("isNetworkError(%v) = %v，期望 %v", tc.err, got, tc.want)
			}
		})
	}
}

// 请求失败触发一次探测；确认离线后状态翻转并通知界面，重新探测成功再翻回来。
func TestNetworkStatusFailureDetectsOfflineAndRecheckRecovers(t *testing.T) {
	var online atomic.Bool
	online.Store(false) // 探测一开始判定断网
	status := newNetworkStatus(func() bool { return online.Load() })

	changed := make(chan bool, 4)
	status.addListener(func(offline bool) { changed <- offline })

	status.reportFailure(&net.DNSError{IsNotFound: true})
	select {
	case got := <-changed:
		if !got {
			t.Error("探测判定离线应通知 offline=true")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时候选：后台探测没有通知状态变化")
	}
	if !status.isOffline() {
		t.Error("确认断网后应进入离线态")
	}

	// 已离线时再有失败不该重复翻转（节流 + 去重的探测不会再次通知）。
	status.reportFailure(&net.OpError{Op: "dial", Err: errors.New("refused")})
	select {
	case extra := <-changed:
		t.Errorf("离线态重复失败不该再翻转状态，收到 %v", extra)
	case <-time.After(100 * time.Millisecond):
	}

	online.Store(true)
	if !status.recheck() {
		t.Error("重新探测应报告在线")
	}
	select {
	case got := <-changed:
		if got {
			t.Error("恢复在线应通知 offline=false")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("恢复在线没有通知")
	}
	if status.isOffline() {
		t.Error("恢复后不该还在离线态")
	}
}

// 离线后所有请求都被 transport 拦下，不再发到网络；恢复后请求照常出去。
func TestOfflineBlocksRequestsUntilRecheck(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = fmt.Fprint(w, `{"c":0,"m":"ok","d":{}}`)
	}))
	defer srv.Close()

	a := testApp(t)
	a.client.Base = srv.URL
	st := a.proxy.netStatus()

	st.applyProbe(false) // 模拟已确认断网
	if _, err := a.client.getJSON(context.Background(), srv.URL+"/api", ""); !errors.Is(err, errOffline) {
		t.Fatalf("离线时请求应被拦下，得到 %v", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("离线时请求仍发到了网络：%d 次", n)
	}

	st.applyProbe(true)
	if _, err := a.client.getJSON(context.Background(), srv.URL+"/api", ""); err != nil {
		t.Fatalf("恢复后请求应成功：%v", err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("恢复后请求应发出 1 次，实际 %d", n)
	}
}

// 新增的网络出口（断网探测）必须走配置的代理，不能直连。
func TestNetworkProbeUsesConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	var hits atomic.Int32
	var host atomic.Value
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		host.Store(r.Host)
		_, _ = fmt.Fprint(w, `{"c":0,"m":"ok","d":{}}`)
	}))
	defer proxySrv.Close()

	old := netProbeTarget
	netProbeTarget = "http://upstream.invalid/probe"
	t.Cleanup(func() { netProbeTarget = old })

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	if !p.probeConnectivity() {
		t.Fatal("代理给出了 HTTP 200，探测应判为在线")
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("探测应只到代理 1 次，实际 %d", n)
	}
	if got, _ := host.Load().(string); got != "upstream.invalid" {
		t.Errorf("代理看到的目标主机 = %q，期望 upstream.invalid", got)
	}
}

// 探测拿到任何 HTTP 响应（含 4xx/5xx）都算在线：服务端报错不是断网。
func TestNetworkProbeTreatsHTTPErrorAsOnline(t *testing.T) {
	clearProxyEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	old := netProbeTarget
	netProbeTarget = srv.URL + "/probe"
	t.Cleanup(func() { netProbeTarget = old })

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyDirect}); err != nil {
		t.Fatal(err)
	}
	if !p.probeConnectivity() {
		t.Error("5xx 是服务端问题，探测应判为在线")
	}

	// 不可解析的域名才是真断网。
	netProbeTarget = "http://upstream.invalid/probe"
	if p.probeConnectivity() {
		t.Error("域名解析失败应判为离线")
	}
}

// 探针与弹窗都带节流：同一波失败只探测一次，弹窗关掉后短期内不反复弹出。
func TestNetworkStatusThrottlesProbeAndNudge(t *testing.T) {
	var probeCalls, notices atomic.Int32
	now := time.Unix(100, 0)
	status := newNetworkStatus(func() bool {
		probeCalls.Add(1)
		return false
	})
	status.now = func() time.Time { return now }
	status.addListener(func(bool) { notices.Add(1) })

	status.reportFailure(&net.DNSError{IsNotFound: true})
	waitNetStatus(t, "首次探测", func() bool { return probeCalls.Load() == 1 })
	if n := notices.Load(); n != 1 {
		t.Errorf("首次确认离线应通知 1 次，实际 %d", n)
	}

	// 时钟没走：节流挡住，不再探测。
	status.reportFailure(&net.DNSError{IsNotFound: true})
	if n := probeCalls.Load(); n != 1 {
		t.Errorf("冷却期内不该重复探测，实际 %d 次", n)
	}

	// 时钟前进后允许再探测；但仍是离线态，状态没翻转就不再通知。
	now = now.Add(5 * time.Second)
	status.reportFailure(&net.DNSError{IsNotFound: true})
	waitNetStatus(t, "冷却结束后再次探测", func() bool { return probeCalls.Load() == 2 })
	if n := notices.Load(); n != 1 {
		t.Errorf("离线态重复探测不该重复通知，实际 %d 次", n)
	}

	// nudge：第一次弹出，紧接着的一次被节流挡住，时间前进后再弹出。
	status.nudge()
	if n := notices.Load(); n != 2 {
		t.Errorf("首次 nudge 应通知 1 次，累计 %d", n)
	}
	status.nudge()
	if n := notices.Load(); n != 2 {
		t.Errorf("nudge 冷却期内不该重复通知，累计 %d", n)
	}
	now = now.Add(5 * time.Second)
	status.nudge()
	if n := notices.Load(); n != 3 {
		t.Errorf("冷却结束后 nudge 应再通知 1 次，累计 %d", n)
	}
}

// 端到端：经代理的请求出现连接类错误时，transport 上报并通知状态机确认离线，
// 后续请求随即被拦下；不需要业务代码显式调用探测。
func TestFailedRequestThroughTransportMarksOffline(t *testing.T) {
	clearProxyEnv(t)
	p := newProxyManager(t.TempDir())
	// 代理本身解析不了：每个请求都会以 DNS 错误失败，探测同样失败。
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: "http://upstream.invalid:7890"}); err != nil {
		t.Fatal(err)
	}
	st := p.netStatus()
	c := &Client{Proxy: p, Base: "http://upstream.invalid/api"}
	if _, err := c.getJSON(context.Background(), c.base()+"/v1/topics", ""); err == nil {
		t.Fatal("请求经不通的代理应失败")
	}
	waitNetStatus(t, "请求失败后自动确认离线", func() bool { return st.isOffline() })
	if _, err := c.getJSON(context.Background(), c.base()+"/v1/topics", ""); !errors.Is(err, errOffline) {
		t.Errorf("离线后请求应被拦下，得到 %v", err)
	}
}

// 离线期间 SSE 循环暂停重连：恢复前不返回，恢复后立刻返回，取消也能退出。
func TestWaitNetworkOnlinePausesStream(t *testing.T) {
	var online atomic.Bool
	status := newNetworkStatus(func() bool { return online.Load() })
	status.applyProbe(false)

	done := make(chan bool, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- waitNetworkOnline(ctx, status) }()
	select {
	case v := <-done:
		t.Fatalf("离线时等待不该返回，得到 %v", v)
	case <-time.After(80 * time.Millisecond):
	}

	status.applyProbe(true)
	select {
	case v := <-done:
		if !v {
			t.Error("网络恢复后等待应返回 true")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("网络恢复后等待没有返回")
	}

	// 取消路径。
	status.applyProbe(false)
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan bool, 1)
	go func() { done2 <- waitNetworkOnline(ctx2, status) }()
	cancel2()
	select {
	case v := <-done2:
		if v {
			t.Error("取消后等待应返回 false")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("取消后等待没有返回")
	}
}

// 断网弹窗画在主窗口，提供「稍后」与「重新连接」；点重新连接后台探测成功
// 后弹窗关闭。
func TestOfflineDialogShowsAndReconnects(t *testing.T) {
	a := testApp(t)
	// 探测替身：点「重新连接」不应真的发网络请求。
	a.proxy.connectivity.probe = func() bool { return true }
	a.offlineDialogOpen = true

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"网络未连接", "已暂停自动重试", "稍后", "重新连接"} {
		if !tt.HasText(want) {
			t.Errorf("断网弹窗缺少 %q：%q", want, tt.Texts())
		}
	}

	if err := tt.Click("重新连接"); err != nil {
		t.Fatalf("点重新连接失败：%v", err)
	}
	waitNetStatus(t, "重新连接成功后关闭弹窗", func() bool { return !a.offlineDialogOpen })
	if a.proxy.netStatus().isOffline() {
		t.Error("重新连接成功后不该还在离线态")
	}
}
