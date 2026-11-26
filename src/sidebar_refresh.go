package main

import (
	"sync"
	"time"
)

const (
	// 侧边栏连续点击的防抖窗口：一串快速切换只保留最后一次对应的请求，
	// 不为中间那些一闪而过的页面浪费配额。
	sidebarRefreshDebounce = 200 * time.Millisecond
	// 两次真实请求之间的最小间隔。节流到点时顺延补发而不是丢掉，
	// 保证每次有效切换最终都会重新请求对应接口。
	sidebarRefreshThrottle = 400 * time.Millisecond
)

// sidebarRefreshScheduler 给侧边栏切换后的重新请求排队。定时器回调在独立
// goroutine 上，而排队动作来自 UI 线程，所以字段都要由 mu 保护。
type sidebarRefreshScheduler struct {
	mu      sync.Mutex
	timer   *time.Timer
	pending func()
	gen     uint64
	last    time.Time
}

// schedule 记下这次切换要发的请求并重置防抖计时。窗口内再次切换会覆盖
// pending，只让最后一次真正发出去。
func (s *sidebarRefreshScheduler) schedule(delay time.Duration, fire func(uint64), fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	gen := s.gen
	s.pending = fn
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(delay, func() { fire(gen) })
}

// flush 在 UI 线程执行。代次过时说明又有了新切换，这次作废；还在节流窗口
// 内就按剩余时间重新排队，不丢请求。
func (s *sidebarRefreshScheduler) flush(gen uint64, fire func(uint64)) {
	s.mu.Lock()
	if gen != s.gen || s.pending == nil {
		s.mu.Unlock()
		return
	}
	if wait := sidebarRefreshThrottle - time.Since(s.last); wait > 0 {
		s.timer = time.AfterFunc(wait, func() { fire(gen) })
		s.mu.Unlock()
		return
	}
	fn := s.pending
	s.pending = nil
	s.timer = nil
	s.last = time.Now()
	s.mu.Unlock()
	fn()
}

// hasPending 报告是否有排队中的刷新。每帧的 ensureLoaded 要靠它让路，
// 否则首页还没加载过时会在防抖等待期间抢先把请求发出去。
func (s *sidebarRefreshScheduler) hasPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending != nil
}

// cancel 清掉排队中的请求（重新登录、窗口收尾、测试收尾），免得迟到的定时器
// 拿旧会话去发请求。
func (s *sidebarRefreshScheduler) cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.pending = nil
	s.gen++
}

// scheduleSidebarRefresh 把一次导航切换的重新请求排进防抖 + 节流队列。
// 选中状态在 selectTab 里已经立即更新，这里只延迟网络请求。
func (a *app) scheduleSidebarRefresh(fn func()) {
	a.sidebarRefresh.schedule(sidebarRefreshDebounce, func(gen uint64) {
		a.publish(func() { a.flushSidebarRefresh(gen) })
	}, fn)
}

func (a *app) flushSidebarRefresh(gen uint64) {
	a.sidebarRefresh.flush(gen, func(g uint64) {
		a.publish(func() { a.flushSidebarRefresh(g) })
	})
}
