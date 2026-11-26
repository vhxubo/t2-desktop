package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 关键词是接口的可选参数：有才拼进去（去首尾空白后转义），空串不出现。
func TestNotificationsQueryKeyword(t *testing.T) {
	q, _ := url.Parse("/?" + notificationsQuery(notificationFilterReply, "  关键词  "))
	if got := q.Query().Get("keyword"); got != "关键词" {
		t.Errorf("keyword = %q，期望去空白后的「关键词」", got)
	}
	if got := q.Query().Get("type"); got != "reply" {
		t.Errorf("关键词不该吃掉分段筛选：type = %q", got)
	}
	for _, empty := range []string{"", "   "} {
		if strings.Contains(notificationsQuery(notificationFilterAll, empty), "keyword=") {
			t.Errorf("空关键词不该拼参数：%q", notificationsQuery(notificationFilterAll, empty))
		}
	}
}

// 应用关键词会从第一页重拉，请求里带上 keyword；超长关键词在本地挡掉，
// 不发注定 400 的请求。
func TestApplyNotificationKeywordReloads(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		_, _ = fmt.Fprint(w, envelopeJSON(`{"list":[],"total":0,"page":1,"limit":50,"total_pages":1}`))
	}))
	defer srv.Close()
	a := testApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: "jimmy", Scope: "read profile read:me",
	}); err != nil {
		t.Fatal(err)
	}
	idx := notificationTabIndex()
	if idx < 0 {
		t.Fatal("找不到通知页")
	}
	st := a.tabs[idx]

	a.applyNotificationKeyword(st, "  找这条  ")
	waitFor(t, func() bool { return st.loaded && !st.loading }, "关键词搜索没有完成")
	if st.keyword != "找这条" {
		t.Errorf("生效的关键词 = %q", st.keyword)
	}

	mu.Lock()
	first := at(queries, 0)
	mu.Unlock()
	if !strings.Contains(first, "keyword="+url.QueryEscape("找这条")) {
		t.Errorf("请求没有带上关键词：%q", first)
	}

	// 超长关键词：只提示，不改状态、不发请求。
	mu.Lock()
	before := len(queries)
	mu.Unlock()
	a.applyNotificationKeyword(st, strings.Repeat("字", notificationKeywordMax+1))
	if st.keyword != "找这条" {
		t.Errorf("超长关键词不该生效：%q", st.keyword)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(queries) != before {
		t.Errorf("超长关键词不该发请求：%d -> %d", before, len(queries))
	}
}

// 通知窗口里有关键词输入与「清除搜索」；筛选到空结果时说清是关键词没命中，
// 而不是「没有消息」。
func TestNotificationSearchRow(t *testing.T) {
	a := testApp(t)
	idx := notificationTabIndex()
	if idx < 0 {
		t.Fatal("找不到通知页")
	}
	st := a.tabs[idx]
	st.keyword = "找这条"
	st.keywordDraft = "找这条"
	st.loaded = true

	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	for _, want := range []string{"搜索", "清除搜索", "没有匹配的通知", "找这条"} {
		if !tt.HasText(want) {
			t.Errorf("通知窗口缺少 %q：%q", want, tt.Texts())
		}
	}
}
