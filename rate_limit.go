package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// API 限流。文档（openapi/v1/index.md 4.6）说每个响应都带
// X-RateLimit-Limit / X-RateLimit-Remaining / X-RateLimit-Reset，并明确建议
// 「按 Remaining 主动降频，比撞 429 再退避体验好得多」。
//
// 因此这里不再等 429 才反应：每个 API 响应都把配额窗口记进共享闸门，
// Remaining 归零就拦到 X-RateLimit-Reset 的窗口结束，期内不发网络请求。
// 真收到 429 时按服务端给的 Retry-After / Reset 退避；两者都没有才退回
// 错误体里的 retry_after（文档字段）或一个很短的固定值，避免无信号时猛冲。
//
// 一个闸门挂在共享的 proxyManager 上，所有窗口共用；没有 proxyManager 的
// 测试客户端各自用一份私有的。第三方图床与 multipart 上传各有自己的配额
// 规则，不走这个闸门。
const (
	rateLimitMaxServerDelay = 10 * time.Minute // 服务端给的秒数异常大时不无限期卡死
	rateLimitNoHeaderDelay  = 5 * time.Second  // 429 且完全没有时间信号时的保守退避
)

type rateLimitGate struct {
	mu    sync.Mutex
	until time.Time // 配额耗尽或 429 后的退避截止；期内不发请求

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

// blockUntil 把退避截止推到 at（只延长、不缩短）；返回实际剩余退避。
func (g *rateLimitGate) blockUntil(at time.Time) time.Duration {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clock()
	if at.After(g.until) {
		g.until = at
	}
	if d := g.until.Sub(now); d > 0 {
		return d
	}
	return 0
}

// observe 在每次 API 响应后调用（成功或失败都要调），用响应头维护配额状态：
//
//   - X-RateLimit-Remaining <= 0：拦到 X-RateLimit-Reset，窗口结束再放行。
//   - 429：Retry-After（秒数或 HTTP 日期）优先，其次 Reset，再退回错误体的
//     retry_after，最后用一个固定短延迟；封顶 rateLimitMaxServerDelay。
//
// bodyRetryAfter 是错误体根上的 retry_after，只在响应头没给时间信号时兜底。
func (g *rateLimitGate) observe(resp *http.Response, bodyRetryAfter int) {
	if g == nil || resp == nil {
		return
	}
	remaining, hasRemaining := headerInt(resp.Header.Get("X-RateLimit-Remaining"))
	resetUnix, _ := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset")), 10, 64)
	var resetAt time.Time
	if resetUnix > 0 {
		resetAt = time.Unix(resetUnix, 0)
	}

	if hasRemaining && remaining <= 0 {
		until := resetAt
		if !until.After(g.clock()) {
			until = g.clock().Add(time.Second)
		}
		g.blockUntil(until)
	}

	if resp.StatusCode != http.StatusTooManyRequests {
		return
	}
	now := g.clock()
	delay := serverRateLimitDelay(retryAfterHeaderSeconds(resp.Header.Get("Retry-After"), now))
	if delay <= 0 && resetAt.After(now) {
		delay = resetAt.Sub(now)
	}
	if delay <= 0 && bodyRetryAfter > 0 {
		delay = time.Duration(bodyRetryAfter) * time.Second
	}
	if delay <= 0 {
		delay = rateLimitNoHeaderDelay
	}
	g.blockUntil(now.Add(delay))
}

// wait 在发请求前由 sendJSON 调用：退避期内直接返回 rate_limited 错误，
// 不发网络请求。context 已取消时不制造多余的等待。
func (g *rateLimitGate) wait(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if left := g.remaining(); left > 0 {
		return rateLimitWaitError(left)
	}
	return nil
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

// headerInt 读一个整数响应头；空串或非法值当作「没有这个头」。
func headerInt(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

// retryAfterHeaderSeconds 只认 Retry-After 响应头：整数秒，或 HTTP 日期。
func retryAfterHeaderSeconds(header string, now time.Time) int {
	h := strings.TrimSpace(header)
	if h == "" {
		return 0
	}
	if n, err := strconv.Atoi(h); err == nil {
		return n
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := t.Sub(now); d > 0 {
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
