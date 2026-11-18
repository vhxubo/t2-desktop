package main

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// API 限流退避。文档（openapi/v1/index.md 4.6）：超限返回 429 +
// code "rate_limited"，错误体根上的 retry_after 以秒为单位，按它退避。
//
// 这里守的是「被限流之后不要再发请求」：UI 的 ensure* 每帧都会被调用，
// 列表加载失败后页面仍是未加载态，下一帧就会把同一个请求再发一遍。闸门
// 记下退避截止时间，期内所有走 API 信封的请求直接返回错误，不碰网络。
// 一个闸门挂在共享的 proxyManager 上，多个账号窗口与后台轮询共用；没有
// proxyManager 的测试客户端各自用一份私有的。
const (
	rateLimitMaxServerDelay = 10 * time.Minute // 服务端给的秒数异常大时不无限期卡死
)

var rateLimitFallbackDelays = []time.Duration{
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	40 * time.Second,
	60 * time.Second,
}

type rateLimitGate struct {
	mu       sync.Mutex
	until    time.Time
	failures int
	// now 只给测试替换时钟用；生产为 nil，走 time.Now。
	now func() time.Time
}

func newRateLimitGate() *rateLimitGate { return &rateLimitGate{} }

func (g *rateLimitGate) clock() time.Time {
	if g.now != nil {
		return g.now()
	}
	return time.Now()
}

// remaining 是退避剩余时间，0 表示现在可以发请求。
func (g *rateLimitGate) remaining() time.Duration {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.until.IsZero() {
		return 0
	}
	if d := g.until.Sub(g.clock()); d > 0 {
		return d
	}
	return 0
}

// hold 记下一次限流，返回本次要退避多久。服务端给了 retry_after 就按它来；
// 没给才按 5/10/20/40/60 秒逐级退避。退避期内又收到限流（并发请求常见）
// 不累加失败次数，避免把退避抬到几小时；只有服务端明确要求更久才延长。
func (g *rateLimitGate) hold(retryAfter int) time.Duration {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	server := serverRateLimitDelay(retryAfter)
	if now.Before(g.until) {
		if server > g.until.Sub(now) {
			g.until = now.Add(server)
		}
		return g.until.Sub(now)
	}
	g.failures++
	delay := server
	if delay <= 0 {
		delay = fallbackRateLimitDelay(g.failures - 1)
	}
	g.until = now.Add(delay)
	return delay
}

// success 在退避已经结束后清掉连续失败次数：隔了很久再来一次限流，应该
// 从最短退避开始，而不是接着上次的次数往上翻。退避期内的成功不清零。
func (g *rateLimitGate) success() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.clock().Before(g.until) {
		g.failures = 0
	}
}

func serverRateLimitDelay(retryAfter int) time.Duration {
	if retryAfter <= 0 {
		return 0
	}
	d := time.Duration(retryAfter) * time.Second
	if d > rateLimitMaxServerDelay {
		return rateLimitMaxServerDelay
	}
	return d
}

func fallbackRateLimitDelay(n int) time.Duration {
	if n < 0 {
		n = 0
	}
	if n >= len(rateLimitFallbackDelays) {
		n = len(rateLimitFallbackDelays) - 1
	}
	return rateLimitFallbackDelays[n]
}

// retryAfterSeconds 优先读错误体根上的 retry_after（文档字段）；响应头只
// 作为服务端偶尔补上时的兼容兜底，不作为判断限流的依据。
func retryAfterSeconds(header string, body int) int {
	if body > 0 {
		return body
	}
	h := strings.TrimSpace(header)
	if h == "" {
		return 0
	}
	if n, err := strconv.Atoi(h); err == nil {
		return n
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return ceilSeconds(d)
		}
	}
	return 0
}

func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}

// rateLimitWaitError 是退避期内被拦掉的请求：留成 rate_limited 的
// apiError，前端照常显示「请 N 秒后重试」，授权链路也不会把它当成缺权限。
func rateLimitWaitError(d time.Duration) *apiError {
	secs := ceilSeconds(d)
	if secs < 1 {
		secs = 1
	}
	return &apiError{
		Status:     http.StatusTooManyRequests,
		Code:       "rate_limited",
		Message:    "请求已按限流退避",
		RetryAfter: secs,
	}
}
