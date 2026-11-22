package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 切走再切回一个已经翻过多页的列表时，不重新拉第一页把用户弹回顶部：
// 静默刷新只合并新的第一页，已加载的旧页与滚动位置都保留。
func TestSilentRefreshKeepsLoadedPagesAndScroll(t *testing.T) {
	var feeds atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feeds.Add(1)
		_, _ = io.WriteString(w, filterFeedJSON(
			`[{"id":"new","short_id":"NEW1","title":"新帖子","author":{"username":"newbie"}}]`, 1))
	}))
	defer srv.Close()

	a := testApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL
	a.client.HTTP = srv.Client()
	a.contentFilter.filter = newContentFilter(contentFilterDTO{})
	a.contentFilter.loaded = true

	st := a.tabs[0]
	st.raw = make([]Item, 60)
	st.items = make([]Item, 60)
	for i := 0; i < 60; i++ {
		it := Item{
			Key: fmt.Sprintf("k%02d", i), ShortID: fmt.Sprintf("S%02d", i),
			Title: fmt.Sprintf("帖子 %d", i), Author: "alice",
		}
		st.raw[i], st.items[i] = it, it
	}
	st.loaded, st.page, st.total, st.totalPages = true, 2, 61, 2

	tt := ui.NewTester(a.view, 1100, 760)
	st.list.ScrollTo(40, ui.Start)
	tt.Frame()
	if first, _ := st.list.Visible(); first < 30 {
		t.Fatalf("前置滚动没有生效：first=%d", first)
	}

	a.refreshTabSilently(0)
	waitFor(t, func() bool {
		return !st.refreshing && len(st.raw) > 0 && st.raw[0].Key == "NEW1"
	}, "静默刷新没有把新的第一页合并进来")

	if feeds.Load() != 1 {
		t.Errorf("静默刷新发了 %d 次请求，期望 1", feeds.Load())
	}
	if st.page != 2 {
		t.Errorf("切页把页码重置成了 %d，旧页会丢", st.page)
	}
	if len(st.raw) < 60 || len(st.items) < 60 {
		t.Errorf("已加载的旧页被丢了：raw=%d items=%d", len(st.raw), len(st.items))
	}
	tt.Frame()
	if first, _ := st.list.Visible(); first == 0 {
		t.Error("静默刷新把列表弹回了顶部")
	}
}

// SSE 每来一条通知只做增量合并：已经翻到第 3 页时，列表与页码都不重置。
func TestNotificationReloadMergesInsteadOfReplacing(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = fmt.Fprint(w, envelopeJSON(`{
			"list":[{"id":"fresh","type":"reply","is_read":false,
				"created_at":"2026-11-20T10:00:00Z"}],
			"total":24,"page":1,"limit":50,"total_pages":3}`))
	}))
	defer srv.Close()

	a, st := notificationApp(t, srv)
	st.page, st.total, st.totalPages = 3, 23, 3
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("old%02d", i)
		st.items = append(st.items, Item{Key: id, Notif: &notification{ID: id, Type: "reply", IsRead: true}})
	}
	before := len(st.items)

	a.reloadNotificationTab()
	waitFor(t, func() bool {
		return !st.refreshing && len(st.items) > 0 && st.items[0].Key == "fresh"
	}, "通知没有增量合并新消息")

	if hits.Load() != 1 {
		t.Errorf("通知刷新发了 %d 次请求，期望 1", hits.Load())
	}
	if st.page != 3 {
		t.Errorf("通知刷新把页码重置成了 %d", st.page)
	}
	if len(st.items) != before+1 {
		t.Errorf("旧消息被整页替换：items %d -> %d", before, len(st.items))
	}
	found := false
	for _, it := range st.items {
		if it.Key == "old19" {
			found = true
			break
		}
	}
	if !found {
		t.Error("已加载的旧消息在合并后丢了")
	}
}
