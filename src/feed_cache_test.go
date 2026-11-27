package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 首屏缓存写入后能整份读回；条目、总数与缓存时间都保持。
func TestFeedCacheRoundTrip(t *testing.T) {
	a := testApp(t)
	spec := a.specFor(0)
	items := []Item{
		{Key: "k1", ShortID: "S1", Title: "第一条", Author: "alice"},
		{Key: "k2", ShortID: "S2", Title: "第二条", Author: "bob"},
	}
	if !a.writeFeedCache(spec, items, 41, 2, time.Now()) {
		t.Fatal("首屏缓存没有写进去")
	}

	c, ok := a.readCachedFeed(spec)
	if !ok {
		t.Fatal("读不到首屏缓存")
	}
	if len(c.Items) != 2 || c.Items[0].ShortID != "S1" || c.Total != 41 || c.TotalPages != 2 {
		t.Fatalf("缓存内容 = %+v", c)
	}
	if c.SavedAt.IsZero() {
		t.Error("缓存时间没有记下来")
	}
}

// 首屏预载只摆内容，不把 loaded 置真：ensureLoaded 仍要发请求刷新。
func TestPreloadFeedCacheKeepsLoadedFalse(t *testing.T) {
	a := testApp(t)
	a.contentFilter.filter = newContentFilter(contentFilterDTO{})
	a.contentFilter.loaded = true
	spec := a.specFor(0)
	if !a.writeFeedCache(spec, []Item{{Key: "k1", ShortID: "S1", Title: "缓存帖"}}, 1, 1, time.Now()) {
		t.Fatal("缓存没写进去")
	}

	st := a.tabs[0]
	a.preloadFeedCache(st, spec)
	if st.loaded {
		t.Error("预载后 loaded 必须仍为 false，否则不会再刷新")
	}
	if len(st.items) != 1 || !st.fromCache || st.cachedAt.IsZero() {
		t.Fatalf("预载状态 = items=%d fromCache=%v cachedAt=%v",
			len(st.items), st.fromCache, st.cachedAt)
	}
}

// 断网首屏失败时保留缓存内容，页头说明来自本地缓存，而不是换成错误页。
func TestLoadSpecKeepsFeedCacheOffline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // 连接失败，触发网络错误

	a := testApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.contentFilter.filter = newContentFilter(contentFilterDTO{})
	a.contentFilter.loaded = true

	spec := a.specFor(0)
	if !a.writeFeedCache(spec, []Item{{Key: "k1", ShortID: "S1", Title: "缓存帖"}}, 1, 1, time.Now()) {
		t.Fatal("缓存没写进去")
	}

	a.load(0, 1)
	waitFor(t, func() bool { return !a.tabs[0].loading }, "首屏加载没有结束")

	st := a.tabs[0]
	if st.err != "" {
		t.Fatalf("断网时不该把缓存换成错误页：%q", st.err)
	}
	if len(st.items) != 1 || !st.fromCache {
		t.Fatalf("断网时缓存内容没有保留：items=%d fromCache=%v", len(st.items), st.fromCache)
	}
	if st.cacheNote == "" {
		t.Error("断网时页头没有说明这是本地缓存")
	}
}

// 需要登录的列表缓存按账号隔离，公开列表用 local。换账号不能读到上一账号的动态。
func TestFeedCacheKeyScope(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", Username: "Alice"}); err != nil {
		t.Fatal(err)
	}

	pub := a.specFor(0)
	if key := a.feedCacheKey(pub); !strings.HasPrefix(key, feedCachePrefix+localCacheSegment+":") {
		t.Errorf("公开列表键 = %q，期望 local 作用域", key)
	}

	follows := a.specFor(6)
	if !follows.NeedsAuth {
		t.Fatal("前置条件：动态 tab 应需要登录")
	}
	if key := a.feedCacheKey(follows); !strings.HasPrefix(key, feedCachePrefix+"alice:") {
		t.Errorf("登录列表键 = %q，期望账号作用域", key)
	}
}

// 没有主窗口时不写缓存：列表不可见，后台回包也不该在测试目录清理后落盘。
func TestSaveCachedFeedSkipsWithoutWindow(t *testing.T) {
	a := testApp(t)
	spec := a.specFor(0)
	a.saveCachedFeed(spec, []Item{{Key: "k1", ShortID: "S1"}}, 1, 1)
	if _, ok := a.readCachedFeed(spec); ok {
		t.Error("没有窗口时不该写首屏缓存")
	}
}

// 首屏成功后缓存标记要清掉，不留着上一份缓存的说明。
func TestClearFeedCacheState(t *testing.T) {
	a := testApp(t)
	st := a.tabs[0]
	st.cacheNote = "网络不可用，显示上次加载的内容"
	st.fromCache = true
	a.clearFeedCacheState(st)
	if st.cacheNote != "" || st.fromCache || !st.cachedAt.IsZero() {
		t.Fatalf("缓存标记没有清干净：%q %v %v", st.cacheNote, st.fromCache, st.cachedAt)
	}
}