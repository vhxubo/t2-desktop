package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 资料卡的合集筛选也走第二个接口：GET /v1/users/:name/topics?collection=，
// 用户名按路径段转义，合集名按 query 转义，翻页只换 page。
func TestUserCardTopicCollSpec(t *testing.T) {
	s := userCardTopicCollSpec("alice", "读过的书", 1)
	if s.Path != "/v1/users/alice/topics" {
		t.Errorf("路径 = %q，期望 /v1/users/alice/topics", s.Path)
	}
	if s.ListKey != "posts" || s.Paging != pagingPages || s.Kind != kindTopic {
		t.Errorf("端点约定不对：%+v", s)
	}
	got := endpoint(apiBaseURL, s, 1)
	want := "https://2libra.com/api/v1/users/alice/topics?page=1&limit=50&collection=" +
		url.QueryEscape("读过的书")
	if got != want {
		t.Errorf("第 1 页 URL = %s，期望 %s", got, want)
	}
	page2 := endpoint(apiBaseURL, userCardTopicCollSpec("alice", "读过的书", 2), 2)
	if !strings.Contains(page2, "page=2") ||
		!strings.Contains(page2, "collection="+url.QueryEscape("读过的书")) {
		t.Errorf("第 2 页应只换 page：%s", page2)
	}
	odd := endpoint(apiBaseURL, userCardTopicCollSpec("a/b", "甲 & 乙", 1), 1)
	if !strings.Contains(odd, "/users/a%2Fb/topics?") ||
		!strings.Contains(odd, "collection="+url.QueryEscape("甲 & 乙")) {
		t.Errorf("特殊字符没转义：%s", odd)
	}
	if got, want := endpoint(apiBaseURL, userCardTopicsSpec("alice", 1), 1),
		"https://2libra.com/api/v1/users/alice/topics?page=1&limit=50"; got != want {
		t.Errorf("未筛选列表 URL = %s，期望 %s", got, want)
	}
}

// 下拉选项来自这个人的公开合集；选中即按名字筛主题，选回「全部合集」回到
// 未筛选列表且不再发请求。
func TestUserCardTopicCollDropdownApplyAndClear(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 只记合集选项与主题筛选，资料卡首屏的 about / emojis 不算在内。
		if strings.HasSuffix(r.URL.Path, "/collections") || strings.HasSuffix(r.URL.Path, "/topics") {
			mu.Lock()
			calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/users/alice/collections":
			_, _ = fmt.Fprint(w, envelopeJSON(`[{"id":"c1","name":"读过的书","topic_count":3}]`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/users/alice/topics"):
			_, _ = fmt.Fprint(w, envelopeJSON(`{"posts":[{"short_id":"T7","title":"合集里的帖子",`+
				`"author":{"username":"alice"},"created_at":"2026-11-01T09:00:00Z"}],`+
				`"total":1,"page":1,"limit":50,"total_pages":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := userCardTestApp(t)
	a.autoLoad = true
	a.client.HTTP = srv.Client()
	a.client.Base = srv.URL + "/api"
	a.userCard.tab = userCardTabTopics
	topics := &a.userCard.topics
	topics.items = []Item{{Key: "T1", ShortID: "T1", Title: "未筛选的帖子"}}

	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	// 视图末尾的 ensure 会拉合集选项，等它回来再打开下拉。
	waitFor(t, func() bool { return a.userCard.collNames.loaded }, "合集选项没有从接口加载出来")
	tt.Frame()
	if !tt.HasText("未筛选的帖子") {
		t.Fatalf("默认应显示未筛选列表：%q", tt.Texts())
	}
	if _, ok := tt.Find("按合集名筛选帖子"); !ok {
		t.Fatalf("主题页缺少合集下拉：%q", tt.Texts())
	}
	if err := tt.Click("按合集名筛选帖子"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("全部合集") || !tt.HasText("读过的书") {
		t.Fatalf("下拉选项不对：%q", tt.Texts())
	}
	if err := tt.Click("读过的书"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return a.userCard.topicCollSt.loaded }, "合集筛选结果没有加载出来")
	tt.Frame()
	if !tt.HasText("合集里的帖子") || tt.HasText("未筛选的帖子") {
		t.Errorf("筛选后列表不对：%q", tt.Texts())
	}
	if a.userCard.topicColl != "读过的书" || a.userCard.topicCollDraft != "读过的书" {
		t.Errorf("选中状态不对：%q / %q", a.userCard.topicColl, a.userCard.topicCollDraft)
	}
	// 选回「全部合集」：不发新请求，列表回到未筛选那份。
	if err := tt.Click("按合集名筛选帖子"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("全部合集"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.userCard.topicColl != "" || a.userCard.topicCollDraft != userCardTopicCollAll {
		t.Errorf("选全部合集后状态不对：%q / %q", a.userCard.topicColl, a.userCard.topicCollDraft)
	}
	if !tt.HasText("未筛选的帖子") || tt.HasText("合集里的帖子") {
		t.Errorf("选全部合集后没回到原列表：%q", tt.Texts())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("请求次数 = %d，期望合集选项 1 次 + 筛选 1 次：%v", len(calls), calls)
	}
	if calls[0] != "GET /api/v1/users/alice/collections?" {
		t.Errorf("第一个请求 = %q，期望这个人的公开合集", calls[0])
	}
	want := "GET /api/v1/users/alice/topics?page=1&limit=50&collection=" + url.QueryEscape("读过的书")
	if calls[1] != want {
		t.Errorf("筛选请求 = %q，期望 %q", calls[1], want)
	}
}

// 合集被改名或删掉后，下拉重新加载时发现选中的名字不在列表里，就清掉筛选。
func TestUserCardCollNamesClearsStaleSelection(t *testing.T) {
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/alice/collections" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		_, _ = fmt.Fprint(w, envelopeJSON(`[{"id":"c1","name":"改名后的合集","topic_count":3}]`))
	}))
	defer srv.Close()

	a := userCardTestApp(t)
	a.autoLoad = true
	a.client.HTTP = srv.Client()
	a.client.Base = srv.URL + "/api"
	cs := a.userCard
	cs.topicColl, cs.topicCollDraft = "读过的书", "读过的书"
	a.ensureUserCardCollNames()
	waitFor(t, func() bool { return cs.collNames.loaded }, "合集选项没有加载")
	if cs.topicColl != "" || cs.topicCollDraft != userCardTopicCollAll {
		t.Errorf("旧合集名不在列表里时应清掉筛选：%q / %q", cs.topicColl, cs.topicCollDraft)
	}
}

// 失败退避沿用合集下拉的公共规则，别让每帧 ensure 每帧重发。
func TestUserCardCollNamesBackoff(t *testing.T) {
	if got := collNamesRetryAfter(2); got != 20*time.Second {
		t.Errorf("第 2 次失败退避 = %v，期望 20s", got)
	}
}
