package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// 通知长连接（SSE），对齐参考扩展的 lib/notification-stream.ts 与 lib/sse.ts。
//
// 文档：https://r2.2libra.com/openapi/v1/api-read.md#我的
//
// 这个端点只认 Authorization 请求头，所以不能用通用 GET 封装；它也是整套
// API 里唯一不返回 {c,m,d,t} 信封的端点：200 就是裸事件流，非 200 才是 JSON
// 错误。
//
// 连接生命周期（按文档约定）：
//   - ready：握手成功；
//   - notification_update：有新消息，帧里可能带 unread_count（没有就补拉一次）；
//   - reconnect：服务端主动断开（15 分钟寿命 / 令牌将过期），不是故障，立即重连；
//   - 每 25 秒一次心跳；45 秒一个字节都没收到就认为连接已死；
//   - 断线重连：5s → 10s → 20s → 40s → 60s 封顶；
//   - 401 先刷新令牌重试一次，刷新失败再清掉本地令牌并停止；403 属于授权问题，
//     等重新授权。

const (
	streamIdleTimeout    = 45 * time.Second
	streamReconnectDelay = 500 * time.Millisecond
)

var defaultStreamBackoff = []time.Duration{
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	40 * time.Second,
	60 * time.Second,
}

type streamErrorKind string

const (
	streamErrAuth    streamErrorKind = "auth"
	streamErrHTTP    streamErrorKind = "http"
	streamErrNetwork streamErrorKind = "network"
	streamErrTimeout streamErrorKind = "timeout"
)

type notificationStreamError struct {
	kind       streamErrorKind
	message    string
	status     int
	retryAfter int // 秒
}

func (e *notificationStreamError) Error() string { return e.message }

func streamAuthError(message string, status int) *notificationStreamError {
	return &notificationStreamError{kind: streamErrAuth, message: message, status: status}
}

// notificationStreamHandlers receives the documented frames. The unread
// count pointer is nil when the frame did not carry one.
type notificationStreamHandlers struct {
	onReady        func(unreadCount *int)
	onNotification func(unreadCount *int)
	onReconnect    func()
	onError        func(error)
}

type streamFrame struct {
	Type        string `json:"type"`
	UnreadCount *int   `json:"unread_count"`
	Count       *int   `json:"count"`
}

func (f streamFrame) unread() *int {
	if f.UnreadCount != nil {
		return f.UnreadCount
	}
	return f.Count
}

type streamResult int

const (
	streamEnded streamResult = iota
	streamReconnect
)

func (c *Client) streamHTTPClient() *http.Client {
	if c.StreamHTTP != nil {
		return c.StreamHTTP
	}
	if c.HTTP != nil {
		return c.HTTP
	}
	// 普通 Client 带 20s 超时，对流式读取会在 20 秒后掐断连接，所以这里
	// 单独用一个没有整体超时的客户端；空闲由下面的 idle timer 兜底。
	if c.Proxy != nil {
		return c.Proxy.Client(0)
	}
	return &http.Client{}
}

func (c *Client) streamIdleDuration() time.Duration {
	if c.streamIdleTimeout > 0 {
		return c.streamIdleTimeout
	}
	return streamIdleTimeout
}

func (c *Client) streamReconnectWait() time.Duration {
	if c.streamReconnectDelay > 0 {
		return c.streamReconnectDelay
	}
	return streamReconnectDelay
}

func (c *Client) streamBackoff(failures int) time.Duration {
	backoff := c.streamBackoffSeq
	if len(backoff) == 0 {
		backoff = defaultStreamBackoff
	}
	if failures < 1 {
		failures = 1
	}
	i := failures - 1
	if i >= len(backoff) {
		i = len(backoff) - 1
	}
	if i < 0 {
		return defaultStreamBackoff[0]
	}
	return backoff[i]
}

// watchNotifications keeps one stream alive until ctx is cancelled or the
// authorization is gone. Reconnects use the documented backoff.
func (c *Client) watchNotifications(ctx context.Context, h notificationStreamHandlers) {
	failures := 0
	for ctx.Err() == nil {
		result, err := c.connectNotificationStream(ctx, h)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if h.onError != nil {
				h.onError(err)
			}
			var se *notificationStreamError
			if errors.As(err, &se) && se.kind == streamErrAuth {
				return // 等重新授权，停止重连
			}
			failures++
			delay := c.streamBackoff(failures)
			if se != nil && se.retryAfter > 0 {
				delay = max(time.Duration(se.retryAfter)*time.Second, time.Second)
			}
			if !sleepContext(ctx, delay) {
				return
			}
			continue
		}

		failures = 0
		if result == streamReconnect {
			if h.onReconnect != nil {
				h.onReconnect()
			}
			if !sleepContext(ctx, c.streamReconnectWait()) {
				return
			}
			continue
		}
		// 服务端没发 reconnect 就结束了流：按异常退避重连。
		failures++
		if !sleepContext(ctx, c.streamBackoff(failures)) {
			return
		}
	}
}

func (c *Client) requestNotificationStream(ctx context.Context, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base()+notificationStreamPath, nil)
	if err != nil {
		return nil, err
	}
	ua := c.UA
	if ua == "" {
		ua = "2libra-native-reader/0.1 (+mygo)"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if token == "" {
		return nil, errors.New("未登录")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return c.streamHTTPClient().Do(req)
}

func (c *Client) connectNotificationStream(ctx context.Context, h notificationStreamHandlers) (streamResult, error) {
	token := c.accessToken()
	if token == "" {
		return streamEnded, streamAuthError("未登录，无法建立通知长连接", 0)
	}
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	resp, err := c.requestNotificationStream(reqCtx, token)
	if err != nil {
		return streamEnded, &notificationStreamError{
			kind: streamErrNetwork, message: "通知长连接建立失败：" + err.Error(),
		}
	}

	if resp.StatusCode == http.StatusUnauthorized {
		// 与普通请求一致：先刷新一次，成功就用新令牌重试；失败才清掉登录态。
		fresh, rerr := c.refreshStreamToken(reqCtx, token)
		_ = resp.Body.Close()
		if rerr != nil {
			return streamEnded, streamAuthError("登录已过期，请重新授权", http.StatusUnauthorized)
		}
		resp, err = c.requestNotificationStream(reqCtx, fresh)
		if err != nil {
			return streamEnded, &notificationStreamError{
				kind: streamErrNetwork, message: "通知长连接建立失败：" + err.Error(),
			}
		}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		if c.Auth != nil {
			c.Auth.invalidate(errors.New("登录已过期，请重新授权"))
		}
		return streamEnded, streamAuthError("登录已过期，请重新授权", resp.StatusCode)
	case resp.StatusCode == http.StatusForbidden:
		message, _ := readStreamError(resp)
		return streamEnded, streamAuthError(message, resp.StatusCode)
	case resp.StatusCode >= 400:
		message, retryAfter := readStreamError(resp)
		return streamEnded, &notificationStreamError{
			kind: streamErrHTTP, message: message,
			status: resp.StatusCode, retryAfter: retryAfter,
		}
	}
	if resp.Body == nil {
		return streamEnded, &notificationStreamError{
			kind: streamErrHTTP, message: "通知长连接没有可读的数据流", status: resp.StatusCode,
		}
	}
	return readNotificationFrames(reqCtx, cancel, resp.Body, h, c.streamIdleDuration())
}

func (c *Client) refreshStreamToken(ctx context.Context, stale string) (string, error) {
	if c.Auth == nil {
		return "", errors.New("无法刷新令牌")
	}
	fresh, err := c.Auth.refresh(ctx, stale)
	if err != nil {
		// 与普通请求一致：只有授权真的失效才清登录态，网络问题保留令牌。
		if authFailure(err) {
			c.Auth.invalidate(err)
		}
		return "", err
	}
	return fresh, nil
}

// readStreamError reads the JSON error envelope of a non-200 stream response.
func readStreamError(resp *http.Response) (string, int) {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var env envelope
	if err := json.Unmarshal(body, &env); err == nil {
		msg := env.M
		if msg == "" {
			msg = fmt.Sprintf("通知长连接建立失败（HTTP %d）", resp.StatusCode)
		}
		return msg, env.RetryAfter
	}
	return fmt.Sprintf("通知长连接建立失败（HTTP %d）", resp.StatusCode), 0
}

// readNotificationFrames consumes the text/event-stream body. It returns when
// the server asks for a reconnect, the body ends, or the idle timer cancels
// the request.
func readNotificationFrames(
	reqCtx context.Context,
	cancel context.CancelFunc,
	body io.Reader,
	h notificationStreamHandlers,
	idleTimeout time.Duration,
) (streamResult, error) {
	if idleTimeout <= 0 {
		idleTimeout = streamIdleTimeout
	}
	var idled atomic.Bool
	idle := time.AfterFunc(idleTimeout, func() {
		idled.Store(true)
		cancel()
	})
	defer idle.Stop()

	reader := bufio.NewReaderSize(body, 64<<10)
	var data []string
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			idle.Reset(idleTimeout)
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				// 空行 = 帧结束。心跳帧没有 data，自然被丢掉。
				if len(data) > 0 {
					frame := strings.Join(data, "\n")
					data = data[:0]
					if result, stop := handleNotificationFrame(frame, h); stop {
						return result, nil
					}
				}
			case strings.HasPrefix(line, ":"):
				// 注释行，也常用作保活。
			case strings.HasPrefix(line, "data:"):
				value := strings.TrimPrefix(line, "data:")
				// 规范：冒号后只去掉一个前导空格。
				value = strings.TrimPrefix(value, " ")
				data = append(data, value)
			}
		}
		if err != nil {
			if reqCtx.Err() != nil {
				if idled.Load() {
					return streamEnded, &notificationStreamError{
						kind: streamErrTimeout, message: "45 秒没收到数据，主动重连",
					}
				}
				return streamEnded, reqCtx.Err()
			}
			if errors.Is(err, io.EOF) {
				return streamEnded, nil
			}
			return streamEnded, &notificationStreamError{
				kind: streamErrNetwork, message: "通知长连接读取失败：" + err.Error(),
			}
		}
	}
}

// handleNotificationFrame decodes one complete SSE data frame. Unknown types
// are ignored, as the contract requires; a dirty frame must not kill the
// connection.
func handleNotificationFrame(raw string, h notificationStreamHandlers) (streamResult, bool) {
	var frame streamFrame
	if err := json.Unmarshal([]byte(raw), &frame); err != nil {
		return streamEnded, false
	}
	switch frame.Type {
	case "ready":
		if h.onReady != nil {
			h.onReady(frame.unread())
		}
	case "notification_update":
		if h.onNotification != nil {
			h.onNotification(frame.unread())
		}
	case "reconnect":
		return streamReconnect, true
	}
	return streamEnded, false
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
