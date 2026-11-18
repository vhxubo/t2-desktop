package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 429 之后，同一份数据哪怕每帧都请求，也只能真正发出第一次。
func TestRateLimitBackoffStopsRepeatedRequests(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"c":429,"code":"rate_limited","m":"请求过于频繁","dimension":"user","retry_after":60,"d":null,"t":1}`)
			return
		}
		_, _ = io.WriteString(w, envelopeJSON(`{"posts":[],"total":0,"page":1,"limit":50,"total_pages":1}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api"}
	now := time.Now()
	gate := c.rateGate()
	gate.now = func() time.Time { return now }

	_, err := c.fetch(context.Background(), tabSpecs[0], 1)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != "rate_limited" {
		t.Fatalf("第一次 429 应返回 rate_limited，得到 %v", err)
	}
	if ae.RetryAfter < 60 {
		t.Errorf("错误里的重试秒数 = %d，期望至少 60", ae.RetryAfter)
	}
	for i := 0; i < 10; i++ {
		_, err = c.fetch(context.Background(), tabSpecs[0], 1)
		if !errors.As(err, &ae) || ae.Code != "rate_limited" || ae.RetryAfter < 1 {
			t.Fatalf("退避期内的请求应被本地拦掉并给出秒数，得到 %v", err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("退避期内发出去 %d 个请求，期望 1", got)
	}

	now = now.Add(61 * time.Second) // 只推测试时钟，不等真实一分钟
	if _, err = c.fetch(context.Background(), tabSpecs[0], 1); err != nil {
		t.Fatalf("退避结束后应恢复请求，得到 %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("恢复后请求数 = %d，期望 2", got)
	}
}

// 服务端没给 retry_after 时按固定序列指数退避；给了就按服务端来。
func TestRateLimitBackoffSequence(t *testing.T) {
	g := newRateLimitGate()
	now := time.Now()
	g.now = func() time.Time { return now }
	for i, want := range []time.Duration{
		5 * time.Second, 10 * time.Second, 20 * time.Second,
		40 * time.Second, 60 * time.Second, 60 * time.Second,
	} {
		if got := g.hold(0); got != want {
			t.Fatalf("第 %d 次无 retry_after 的退避 = %v，期望 %v", i+1, got, want)
		}
		now = now.Add(want + time.Millisecond)
	}

	withHeader := newRateLimitGate()
	if got := withHeader.hold(7); got != 7*time.Second {
		t.Errorf("按服务端 retry_after 退避 = %v，期望 7s", got)
	}

	// 并发请求会同时吃到 429：退避期内再来一次不叠加次数，只认更长的服务端要求。
	concurrent := newRateLimitGate()
	if got := concurrent.hold(0); got != 5*time.Second {
		t.Fatalf("首次退避 = %v，期望 5s", got)
	}
	if got := concurrent.hold(0); got > 5*time.Second {
		t.Errorf("退避期内重复限流被抬高到 %v", got)
	}
	if concurrent.failures != 1 {
		t.Errorf("退避期内重复限流把失败次数加到 %d，期望 1", concurrent.failures)
	}
	if got := concurrent.hold(30); got != 30*time.Second {
		t.Errorf("服务端要求更长时应延长到 30s，实际 %v", got)
	}
}

// 多个账号窗口与后台轮询共用同一份代理管理器，一个被限流，其它都跳过。
func TestRateLimitGateSharedAcrossClients(t *testing.T) {
	p := newProxyManager(t.TempDir())
	var firstCalls, secondCalls int32
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&firstCalls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"c":429,"code":"rate_limited","m":"请求过于频繁","retry_after":30,"d":null,"t":1}`)
	}))
	defer limited.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&secondCalls, 1)
		_, _ = io.WriteString(w, envelopeJSON(`{"posts":[],"total":0,"page":1,"limit":50,"total_pages":1}`))
	}))
	defer other.Close()

	c1 := &Client{HTTP: limited.Client(), Base: limited.URL + "/api", Proxy: p}
	c2 := &Client{HTTP: other.Client(), Base: other.URL + "/api", Proxy: p}
	if c1.rateGate() != c2.rateGate() {
		t.Fatal("同一份代理配置下的客户端没有共用限流闸门")
	}
	_, _ = c1.fetch(context.Background(), tabSpecs[0], 1)
	_, err := c2.fetch(context.Background(), tabSpecs[0], 1)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != "rate_limited" {
		t.Fatalf("另一个窗口应被共享闸门拦掉，得到 %v", err)
	}
	if got := atomic.LoadInt32(&secondCalls); got != 0 {
		t.Errorf("退避期内其它账号窗口还发出去 %d 个请求", got)
	}
}

// 视图每帧调用的 ensureLoaded 在限流后退避，不再排队注定被拒的请求。
func TestEnsureLoadedStopsAfterRateLimit(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"c":429,"code":"rate_limited","m":"请求过于频繁","retry_after":60,"d":null,"t":1}`)
			return
		}
		_, _ = io.WriteString(w, envelopeJSON(`{"posts":[],"total":0,"page":1,"limit":50,"total_pages":1}`))
	}))
	defer srv.Close()

	a := testApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	now := time.Now()
	gate := a.client.rateGate()
	gate.now = func() time.Time { return now }

	a.ensureLoaded()
	waitFor(t, func() bool { return !a.tabs[0].loading }, "首次加载没有结束")
	for i := 0; i < 20; i++ { // 模拟连续 20 帧
		a.ensureLoaded()
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("连续帧发出去 %d 个请求，期望 1", got)
	}
	if !strings.Contains(a.tabs[0].err, "请求过于频繁") {
		t.Errorf("页面错误提示 = %q，期望说明限流", a.tabs[0].err)
	}

	now = now.Add(61 * time.Second)
	a.ensureLoaded()
	waitFor(t, func() bool { return a.tabs[0].loaded }, "退避结束后没有恢复加载")
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("恢复后请求数 = %d，期望 2", got)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	if got := retryAfterSeconds("", 12); got != 12 {
		t.Errorf("错误体 retry_after 应优先，得到 %d", got)
	}
	if got := retryAfterSeconds("9", 0); got != 9 {
		t.Errorf("响应头兜底应读到 9 秒，得到 %d", got)
	}
	if got := retryAfterSeconds("bad", 0); got != 0 {
		t.Errorf("无法解析时不应退避，得到 %d", got)
	}
}
