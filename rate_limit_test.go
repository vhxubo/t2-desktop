package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"c":429,"code":"rate_limited","m":"请求过于频繁","dimension":"user","d":null,"t":1}`)
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

// 文档 4.6 要求按 X-RateLimit-* 主动降频：成功响应里 Remaining 归零，
// 下一个请求就该在本地拦到窗口重置，而不是等服务端回 429。
func TestRateLimitProactiveHeaderThrottle(t *testing.T) {
	var calls int32
	now := time.Now()
	resetAt := now.Add(60 * time.Second).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("X-RateLimit-Limit", "1800")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(resetAt, 10))
		_, _ = io.WriteString(w, envelopeJSON(`{"posts":[],"total":0,"page":1,"limit":50,"total_pages":1}`))
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api"}
	gate := c.rateGate()
	gate.now = func() time.Time { return now }

	if _, err := c.fetch(context.Background(), tabSpecs[0], 1); err != nil {
		t.Fatalf("配额未耗尽前应正常请求，得到 %v", err)
	}
	_, err := c.fetch(context.Background(), tabSpecs[0], 1)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != "rate_limited" {
		t.Fatalf("Remaining=0 后应本地拦掉，得到 %v", err)
	}
	if ae.RetryAfter < 55 || ae.RetryAfter > 60 {
		t.Errorf("拦截秒数 = %d，期望约 60（Reset 窗口）", ae.RetryAfter)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("主动降频期内发出去 %d 个请求，期望 1", got)
	}

	now = time.Unix(resetAt, 0).Add(time.Second)
	if _, err = c.fetch(context.Background(), tabSpecs[0], 1); err != nil {
		t.Fatalf("窗口重置后应恢复请求，得到 %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("恢复后请求数 = %d，期望 2", got)
	}
}

// 多个账号窗口与后台轮询共用同一份代理管理器，一个被限流，其它都跳过。
func TestRateLimitGateSharedAcrossClients(t *testing.T) {
	p := newProxyManager(t.TempDir())
	var firstCalls, secondCalls int32
	limited := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&firstCalls, 1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"c":429,"code":"rate_limited","m":"请求过于频繁","d":null,"t":1}`)
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
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"c":429,"code":"rate_limited","m":"请求过于频繁","d":null,"t":1}`)
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

func TestRetryAfterHeaderSeconds(t *testing.T) {
	now := time.Now()
	if got := retryAfterHeaderSeconds("12", now); got != 12 {
		t.Errorf("整数秒头应读到 12，得到 %d", got)
	}
	if got := retryAfterHeaderSeconds("bad", now); got != 0 {
		t.Errorf("无法解析时不应退避，得到 %d", got)
	}
	if got := retryAfterHeaderSeconds(now.Add(30*time.Second).UTC().Format(http.TimeFormat), now); got < 28 || got > 30 {
		t.Errorf("HTTP 日期头应读到约 30 秒，得到 %d", got)
	}
	if n, ok := headerInt("0"); !ok || n != 0 {
		t.Errorf("X-RateLimit-Remaining: 0 应被识别为有该头，得到 (%d,%v)", n, ok)
	}
	if _, ok := headerInt(""); ok {
		t.Error("空响应头不应被识别")
	}
}

// 429 没有任何响应头时，才退回错误体 retry_after；再没有就用固定短延迟。
func TestRateLimitHeaderFallbacks(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}
	g := newRateLimitGate()
	now := time.Now()
	g.now = func() time.Time { return now }

	g.observe(resp, 9)
	if d := g.remaining(); d < 8*time.Second || d > 9*time.Second {
		t.Errorf("错误体 retry_after 兜底 = %v，期望约 9s", d)
	}

	plain := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}
	g2 := newRateLimitGate()
	g2.now = func() time.Time { return now }
	g2.observe(plain, 0)
	if d := g2.remaining(); d != rateLimitNoHeaderDelay {
		t.Errorf("无信号时退避 = %v，期望 %v", d, rateLimitNoHeaderDelay)
	}
}
