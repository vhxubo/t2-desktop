package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func waitForNavRequests(t *testing.T, want int, count func() int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for count() < want {
		if time.Now().After(deadline) {
			t.Fatalf("等待 %d 次侧边栏刷新请求超时，实际 %d 次", want, count())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 连续快速点两个导航项：防抖只保留最后一次切换对应的接口请求，不为中间
// 一闪而过的页面发请求；选中状态本身仍然立即跟着最后一次点击走。
func TestSidebarSwitchDebouncesRapidClicks(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.String())
		mu.Unlock()
		_, _ = io.WriteString(w, envelopeJSON(`{"posts":[],"total":0,"page":1,"limit":50,"total_pages":0}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if err := tt.Click(tabSpecs[1].Label); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click(tabSpecs[2].Label); err != nil {
		t.Fatal(err)
	}
	waitForNavRequests(t, 1, func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(paths)
	})
	// 给可能迟到的重复请求留一点时间再下结论。
	time.Sleep(sidebarRefreshDebounce + 100*time.Millisecond)

	mu.Lock()
	got := append([]string(nil), paths...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("快速连点应只发一次请求，实际 %d 次：%v", len(got), got)
	}
	want := tabSpecs[2].Path + "?" + tabSpecs[2].Query
	if !containsNavPath(got, want) {
		t.Errorf("防抖应保留最后一次切换的接口 %q，实际 %v", want, got)
	}
	if a.tab != 2 || a.navSel != tabID(2) {
		t.Errorf("选中状态应跟着最后一次点击：tab=%d navSel=%q", a.tab, a.navSel)
	}
}

// 切到还没加载过的页时，每帧兜底的 ensureLoaded 不能在防抖窗口里抢先发请求，
// 否则连点两个未加载页会各发一次，防抖就失效了。
func TestSidebarSwitchDebouncesUnloadedTabs(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.String())
		mu.Unlock()
		_, _ = io.WriteString(w, envelopeJSON(`{"posts":[],"total":0,"page":1,"limit":50,"total_pages":0}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.autoLoad = true
	a.tabs[0].loaded = true // 首帧别去拉首页，只统计热榜请求
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	hotCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, p := range paths {
			if strings.HasPrefix(p, "/v1/topics/hot") {
				n++
			}
		}
		return n
	}
	if err := tt.Click(tabSpecs[1].Label); err != nil {
		t.Fatal(err)
	}
	tt.Frame() // 模拟下一帧：ensureLoaded 必须给排队的刷新让路
	if err := tt.Click(tabSpecs[2].Label); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitForNavRequests(t, 1, hotCount)
	time.Sleep(sidebarRefreshDebounce + 100*time.Millisecond)

	mu.Lock()
	got := append([]string(nil), paths...)
	mu.Unlock()
	hits := 0
	for _, p := range got {
		if strings.HasPrefix(p, "/v1/topics/hot") {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("未加载页连点应只发一次热榜请求，实际 %d 次：%v", hits, got)
	}
	want := tabSpecs[2].Path + "?" + tabSpecs[2].Query
	if !containsNavPath(got, want) {
		t.Errorf("应只请求最后一次切换的接口 %q，实际 %v", want, got)
	}
}

// 刷新请求还在防抖 / 节流队列里时不能闪出「暂无内容」：列表正在等新数据，
// 该显示加载态。等空结果真的回来之后，才允许显示空文案。
func TestSidebarRefreshDoesNotFlashEmptyState(t *testing.T) {
	var mu sync.Mutex
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		_, _ = io.WriteString(w, envelopeJSON(`{"posts":[],"total":0,"page":1,"limit":50,"total_pages":0}`))
	}))
	defer srv.Close()
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}

	t.Run("再点当前页", func(t *testing.T) {
		base := count()
		a := loginApp(t, srv)
		a.tabs[0].loaded = true
		tt := ui.NewTester(a.view, 1100, 760)
		tt.Frame()
		if !tt.HasText("暂无内容") {
			t.Fatal("前置条件：空列表本来应显示「暂无内容」")
		}
		if err := tt.Click(tabSpecs[0].Label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if tt.HasText("暂无内容") {
			t.Error("刷新排队期间不该闪出「暂无内容」")
		}
		waitForNavRequests(t, base+1, count)
	})

	t.Run("点主页清筛选", func(t *testing.T) {
		base := count()
		a := loginApp(t, srv)
		a.tabs[0].loaded = true
		a.tabs[0].homeFilterNode = nodeFilterOf(nodeRef{ID: "n-work", Slug: "work", Name: "职场"})
		a.tabs[0].items = []Item{{Key: "old", Title: "旧内容"}}
		a.tabs[0].noMore = true // 别让首帧的无限滚动续拉覆盖这份内容
		tt := ui.NewTester(a.view, 1100, 760)
		tt.Frame()
		if !tt.HasText("旧内容") {
			t.Fatalf("前置条件：应显示已有内容，实际 %q", tt.Texts())
		}
		if err := tt.Click(tabSpecs[0].Label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if tt.HasText("暂无内容") {
			t.Error("清筛选后等请求的空窗期不该闪出「暂无内容」")
		}
		waitForNavRequests(t, base+1, count)
	})
}

func containsNavPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}

// 两次「间隔足够远、不会被防抖合并」的切换都要重新请求；但真实请求之间
// 至少隔一个节流窗口。节流的第二次是顺延补发，不会被丢掉。
func TestSidebarSwitchThrottlesWithoutDropping(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		_, _ = io.WriteString(w, envelopeJSON(`{"posts":[],"total":0,"page":1,"limit":50,"total_pages":0}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(times)
	}
	if err := tt.Click(tabSpecs[1].Label); err != nil {
		t.Fatal(err)
	}
	waitForNavRequests(t, 1, count)
	if err := tt.Click(tabSpecs[3].Label); err != nil {
		t.Fatal(err)
	}
	// 防抖窗口已经过去，但节流窗口还没到：这时不该有第二次请求。
	time.Sleep(sidebarRefreshDebounce + 50*time.Millisecond)
	if n := count(); n != 1 {
		t.Fatalf("节流窗口内不该发第二次请求，实际 %d 次", n)
	}
	waitForNavRequests(t, 2, count)

	mu.Lock()
	got := append([]time.Time(nil), times...)
	mu.Unlock()
	if gap := got[1].Sub(got[0]); gap < sidebarRefreshThrottle-100*time.Millisecond {
		t.Errorf("两次请求间隔太短：%v，节流窗口为 %v", gap, sidebarRefreshThrottle)
	}
}
