package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

/* ------------------------------------------------------------------
   网络在线状态机（网络状态工具的核心）
---------------------------------------------------------------- */

// 状态机挂在进程级的 proxyManager 上，所有窗口共用一份。任何请求出现
// 连接类错误（DNS 失败、连接被拒、超时等）时由 netLogTransport 报一次；状态机
// 用一次很小的探测确认是不是真的断网，确认后进入离线态：
//
//   - transport 直接拦下后续请求，不再发到网络（也就不再触发各种退避重试）；
//   - 通知长连接暂停重连（见 stream.go 的 waitNetworkOnline）；
//   - 通知界面弹「网络未连接」弹窗（见 netstatus_ui.go），点「重新连接」探测，
//     成功才恢复。
//
// 探测本身走 proxyManager 的共享 transport（不经 netLogTransport），否则离线时
// 会被自己拦住，也无法在探测失败时再次触发探测。探测拿到任何 HTTP 响应就算
// 在线（4xx/5xx 是服务端问题，不是断网），只有传输错误才算离线。

// errOffline 是离线期间被 transport 拦下的请求返回的错误。它不是 API 错误，
// 也不会再次触发探测，避免离线时反复探测。
var errOffline = errors.New("网络未连接，已暂停自动重试")

// netProbeTarget 是探测用的小请求地址：公开读端点，不需要登录、响应很小。
// 用例会把它换成不可解析的域名来模拟断网。与 proxyCheckTarget 分开，互不干扰。
var netProbeTarget = apiBaseURL + "/v1/topics/latest?limit=1"

const (
	netProbeTimeout  = 6 * time.Second
	netProbeCooldown = 2 * time.Second // 同一波失败只探测一次，别把探测打成洪水
	netNudgeInterval = 3 * time.Second // 弹窗被关掉后，用户再次触发请求才重新弹出
)

type networkStatus struct {
	mu        sync.Mutex
	offline   bool
	probe     func() bool
	probing   bool
	lastProbe time.Time
	lastNudge time.Time
	listeners []func(offline bool)
	now       func() time.Time // 测试用时钟；生产为 nil
}

func newNetworkStatus(probe func() bool) *networkStatus {
	return &networkStatus{probe: probe}
}

func (s *networkStatus) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// isOffline 报告当前是否处于离线态。transport 与 SSE 循环每次请求前都读它。
func (s *networkStatus) isOffline() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offline
}

// addListener 注册状态变化回调，返回反注册函数。回调在网络协程或重试按钮的
// goroutine 里触发，收到通知的一方自己把界面动作挪回 UI 线程。
func (s *networkStatus) addListener(fn func(offline bool)) (remove func()) {
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	last := len(s.listeners) - 1
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			if last < len(s.listeners) {
				s.listeners[last] = nil
			}
			s.mu.Unlock()
		})
	}
}

// notify 把回调拷出来在锁外调用，避免回调里再进状态机时死锁。
func (s *networkStatus) notify(offline bool) {
	s.mu.Lock()
	fns := make([]func(bool), 0, len(s.listeners))
	for _, fn := range s.listeners {
		if fn != nil {
			fns = append(fns, fn)
		}
	}
	s.mu.Unlock()
	for _, fn := range fns {
		fn(offline)
	}
}

// reportFailure 由 transport 在请求出现连接类错误时调用。它不阻塞请求：先做
// 节流与去重，真正的探测丢到后台 goroutine。
func (s *networkStatus) reportFailure(err error) {
	if !isNetworkError(err) {
		return
	}
	s.mu.Lock()
	if s.probing || s.clock().Sub(s.lastProbe) < netProbeCooldown {
		s.mu.Unlock()
		return
	}
	s.probing = true
	s.lastProbe = s.clock()
	s.mu.Unlock()
	go s.runProbe()
}

// runProbe 跑一次后台探测；探测结果统一交给 applyProbe 更新状态。
func (s *networkStatus) runProbe() {
	online := s.probe != nil && s.probe()
	s.applyProbe(online)
}

// applyProbe 用一次探测结果更新状态；只有在线/离线真的翻转时才通知界面。
func (s *networkStatus) applyProbe(online bool) {
	s.mu.Lock()
	s.probing = false
	offline := !online
	changed := offline != s.offline
	s.offline = offline
	s.mu.Unlock()
	if changed {
		s.notify(offline)
	}
}

// recheck 是「重新连接」按钮走的强制探测：忽略节流，同步把结果带回来。调用方
// 必须已经在后台 goroutine 里，因为探测要发网络请求。
func (s *networkStatus) recheck() bool {
	online := s.probe != nil && s.probe()
	s.applyProbe(online)
	return online
}

// nudge 在离线期间又有请求被拦下时调用：弹窗可能被用户关掉了，这时再把它
// 顶出来。带节流，避免批量请求反复弹窗。
func (s *networkStatus) nudge() {
	s.mu.Lock()
	if !s.offline || s.clock().Sub(s.lastNudge) < netNudgeInterval {
		s.mu.Unlock()
		return
	}
	s.lastNudge = s.clock()
	s.mu.Unlock()
	s.notify(true)
}

// isNetworkError 区分「网络不通」和「服务端报错」：只有连接类错误才该触发
// 断网探测。HTTP 4xx/5xx（apiError）与限流退避都不是网络错误，不在这里处理。
func isNetworkError(err error) bool {
	if err == nil || errors.Is(err, errOffline) {
		return false
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return isNetworkError(ue.Err)
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return true
	}
	// http.Client 整体超时表现为 context.DeadlineExceeded；连接在建好之前
	// 就被对端关掉则是 EOF。用户主动取消（context.Canceled）不算断网。
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// probeConnectivity 用当前生效的代理规则跑一个很小的公开请求：拿到任何 HTTP
// 响应就算在线（4xx/5xx 是服务端问题，不是断网）；连接、解析、超时失败才算离线。
// 直接用共享 transport（不经 netLogTransport），一是离线时不会被自己拦住，二是
// 探测失败不会再回头触发探测。请求打上标记，让 proxyFor 里的 netlog 补记跳过。
func (p *proxyManager) probeConnectivity() bool {
	ctx, cancel := context.WithTimeout(context.Background(), netProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, netProbeTarget, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "2libra-native-reader/0.1 (+mygo)")
	req.Header.Set("Accept", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), netLogMarkerKey{}, true))
	client := &http.Client{
		Transport: p.Transport(),
		Timeout:   netProbeTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2<<10))
	return true
}
