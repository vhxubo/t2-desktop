package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

func filterFeedJSON(posts string, page int) string {
	return envelopeJSON(fmt.Sprintf(`{"posts":%s,"total":5,"page":%d,"limit":50,"total_pages":3}`, posts, page))
}

func TestHomeFeedFiltersBlockedAuthor(t *testing.T) {
	var feeds atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feeds.Add(1)
		_, _ = io.WriteString(w, filterFeedJSON(`[
			{"id":"1","short_id":"A1","title":"甲的文章","author":{"username":"alice"}},
			{"id":"2","short_id":"B1","title":"乙的文章","author":{"username":"bob"}},
			{"id":"3","short_id":"C1","title":"丙的文章","author":{"username":"carol"}}
		]`, 1))
	}))
	defer srv.Close()

	a := filterApp(t)
	a.client.Base = srv.URL
	a.client.HTTP = srv.Client()
	a.contentFilter.filter = newContentFilter(contentFilterDTO{BlockedUsers: []string{"Alice"}})
	a.contentFilter.loaded = true
	a.load(0, 1)
	waitFor(t, func() bool { return len(a.tabs[0].raw) == 3 }, "首页列表没有加载")

	st := a.tabs[0]
	if len(st.items) != 2 || st.hidden != 1 {
		t.Fatalf("可见 %d 条 / 隐藏 %d 条，期望 2 / 1", len(st.items), st.hidden)
	}
	for _, it := range st.items {
		if it.Author == "alice" || it.Title == "甲的文章" {
			t.Errorf("被屏蔽作者的文章还在可见列表里：%+v", it)
		}
	}
	if st.noMore {
		t.Error("只过滤掉一条不该标记到底")
	}
	if feeds.Load() != 1 {
		t.Errorf("发了 %d 次列表请求，期望 1", feeds.Load())
	}
}

func TestFilteredPageAutoSkipsToNext(t *testing.T) {
	var feeds atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feeds.Add(1)
		if r.URL.Query().Get("page") == "2" {
			_, _ = io.WriteString(w, filterFeedJSON(`[
				{"id":"4","short_id":"D1","title":"可见文章","author":{"username":"dave"}}
			]`, 2))
			return
		}
		_, _ = io.WriteString(w, filterFeedJSON(`[
			{"id":"1","short_id":"A1","title":"甲的文章1","author":{"username":"alice"}},
			{"id":"2","short_id":"A2","title":"甲的文章2","author":{"username":"alice"}}
		]`, 1))
	}))
	defer srv.Close()

	a := filterApp(t)
	a.client.Base = srv.URL
	a.client.HTTP = srv.Client()
	a.contentFilter.filter = newContentFilter(contentFilterDTO{BlockedUsers: []string{"alice"}})
	a.contentFilter.loaded = true
	a.load(0, 1)
	waitFor(t, func() bool { return a.tabs[0].page == 2 && len(a.tabs[0].items) > 0 }, "整页被过滤后没有续拉下一页")

	st := a.tabs[0]
	if st.noMore {
		t.Error("整页被过滤不该被误判成已经到底")
	}
	if len(st.raw) != 3 || len(st.items) != 1 || st.hidden != 2 {
		t.Fatalf("raw/items/hidden = %d/%d/%d，期望 3/1/2", len(st.raw), len(st.items), st.hidden)
	}
	if feeds.Load() != 2 {
		t.Errorf("列表请求 %d 次，期望 2", feeds.Load())
	}
}

func TestBlockAndUnblockRecomputeWithoutRefetch(t *testing.T) {
	var feeds atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			var dto contentFilterDTO
			_ = json.Unmarshal(b, &dto)
			resp, _ := json.Marshal(map[string]any{"c": 0, "m": "ok", "d": dto, "t": 1})
			_, _ = w.Write(resp)
			return
		}
		feeds.Add(1)
		_, _ = io.WriteString(w, filterFeedJSON(`[
			{"id":"1","short_id":"A1","title":"甲的文章","author":{"username":"alice"}},
			{"id":"2","short_id":"B1","title":"乙的文章","author":{"username":"bob"}},
			{"id":"3","short_id":"C1","title":"丙的文章","author":{"username":"carol"}}
		]`, 1))
	}))
	defer srv.Close()

	a := filterApp(t)
	a.client.Base = srv.URL
	a.client.HTTP = srv.Client()
	a.contentFilter.filter = newContentFilter(contentFilterDTO{})
	a.contentFilter.loaded = true
	a.load(0, 1)
	waitFor(t, func() bool { return len(a.tabs[0].items) == 3 }, "初始列表没加载")
	if feeds.Load() != 1 {
		t.Fatalf("初始列表请求 %d 次", feeds.Load())
	}

	a.setUserBlocked("bob", true)
	waitFor(t, func() bool { return len(a.tabs[0].items) == 2 }, "屏蔽后没有立即重算")
	if got := feeds.Load(); got != 1 {
		t.Errorf("屏蔽后重新请求了列表：%d 次", got)
	}
	if !a.filterBlocksUser("bob") {
		t.Error("快照里应该已经有 bob")
	}

	a.setUserBlocked("bob", false)
	waitFor(t, func() bool { return len(a.tabs[0].items) == 3 }, "取消屏蔽后没有还原")
	if got := feeds.Load(); got != 1 {
		t.Errorf("取消屏蔽后重新请求了列表：%d 次", got)
	}
}

func TestFeedCountLabelWithHidden(t *testing.T) {
	st := &tabState{total: 50, raw: make([]Item, 5), items: make([]Item, 2), hidden: 3}
	if got, want := feedCountLabel(tabSpecs[0], st), "共 50 条 · 已屏蔽 3"; got != want {
		t.Errorf("页头计数 = %q，期望 %q", got, want)
	}
	st.hidden = 0
	if got, want := feedCountLabel(tabSpecs[0], st), "共 50 条"; got != want {
		t.Errorf("没有隐藏项时应保持原样，得到 %q", got)
	}
}

func TestAllHiddenFeedShowsDedicatedEmpty(t *testing.T) {
	a := filterApp(t)
	st := a.tabs[0]
	st.loaded = true
	st.raw = []Item{{Key: "a", Title: "隐藏的文章", Author: "alice"}}
	st.items = nil
	st.hidden = 1
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"内容已按屏蔽规则隐藏", "当前列表的条目都在你的屏蔽名单里。", "管理屏蔽名单"} {
		if !tt.HasText(want) {
			t.Errorf("空状态缺少文案 %q：%q", want, tt.Texts())
		}
	}
}
