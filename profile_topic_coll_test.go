package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 按合集名筛选「我的帖子」的端点契约：路径与查询参数原样照抄文档里的
// GET /v1/users/:userName/topics?collection=，用户名走路径转义，合集名走
// query 转义，翻页只换 page。
func TestProfileTopicCollSpec(t *testing.T) {
	s := profileTopicCollSpec("jimmy", "读过的书", 1)
	if s.Path != "/v1/users/jimmy/topics" {
		t.Errorf("路径 = %q，期望 /v1/users/jimmy/topics", s.Path)
	}
	if s.ListKey != "posts" || s.Paging != pagingPages || s.Kind != kindTopic {
		t.Errorf("端点约定不对：%+v", s)
	}
	got := endpoint(apiBaseURL, s, 1)
	want := "https://2libra.com/api/v1/users/jimmy/topics?page=1&limit=50&collection=" +
		url.QueryEscape("读过的书")
	if got != want {
		t.Errorf("第 1 页 URL = %s，期望 %s", got, want)
	}
	page2 := endpoint(apiBaseURL, profileTopicCollSpec("jimmy", "读过的书", 2), 2)
	if !strings.Contains(page2, "collection="+url.QueryEscape("读过的书")) ||
		!strings.Contains(page2, "page=2") {
		t.Errorf("第 2 页应只换 page，得到 %s", page2)
	}
	// 路径里的斜杠与 query 里的 & 都不能改变参数边界。
	odd := profileTopicCollSpec("a/b", "甲 & 乙", 1)
	oddURL := endpoint(apiBaseURL, odd, 1)
	if !strings.Contains(oddURL, "/users/a%2Fb/topics?") {
		t.Errorf("用户名没有按路径段转义：%s", oddURL)
	}
	if !strings.Contains(oddURL, "collection="+url.QueryEscape("甲 & 乙")) {
		t.Errorf("合集名没有按 query 转义：%s", oddURL)
	}
}

// 合集下拉的选项来自 GET /v1/me/collections，而不是手输。选中一项就按
// 那个名字筛帖子，请求打到公开端点；选回「全部合集」立刻回到原列表且不再
// 发请求。
func TestProfileTopicCollDropdownApplyAndClear(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	a, _ := profileCollectionsApp(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/me/collections":
			_, _ = fmt.Fprint(w, envelopeJSON(`[{"id":"c1","name":"读过的书","topic_count":3}]`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/users/jimmy/topics"):
			_, _ = fmt.Fprint(w, envelopeJSON(`{"posts":[{"short_id":"T7","title":"合集里的帖子",`+
				`"author":{"username":"jimmy"},"created_at":"2026-11-01T09:00:00Z"}],`+
				`"total":1,"page":1,"limit":50,"total_pages":1}`))
		default:
			http.NotFound(w, r)
		}
	})
	a.profile.tab = profileTabContent
	a.profile.contentKind = profileContentTopics
	topics := &a.profile.content[profileContentTopics]
	topics.loaded = true
	topics.items = []Item{{Key: "T1", ShortID: "T1", Title: "未筛选的帖子"}}

	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	// ensure 在视图末尾拉合集选项，等它回来再打开下拉。
	waitFor(t, func() bool { return a.profile.collNames.loaded }, "合集选项没有从接口加载出来")
	tt.Frame()
	if !tt.HasText("未筛选的帖子") {
		t.Fatalf("默认应显示未筛选列表：%q", tt.Texts())
	}
	if _, ok := tt.Find("按合集名筛选帖子"); !ok {
		t.Fatalf("帖子分段上方缺少合集下拉：%q", tt.Texts())
	}

	if err := tt.Click("按合集名筛选帖子"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// 选项里既有「全部合集」哨兵，也有接口返回的合集名。
	if !tt.HasText("全部合集") || !tt.HasText("读过的书") {
		t.Fatalf("下拉选项不对：%q", tt.Texts())
	}
	if err := tt.Click("读过的书"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return a.profile.topicCollSt.loaded }, "合集筛选结果没有加载出来")

	tt.Frame()
	if !tt.HasText("合集里的帖子") || tt.HasText("未筛选的帖子") {
		t.Errorf("筛选后列表不对：%q", tt.Texts())
	}
	if a.profile.topicColl != "读过的书" || a.profile.topicCollDraft != "读过的书" {
		t.Errorf("选中状态不对：topicColl=%q draft=%q", a.profile.topicColl, a.profile.topicCollDraft)
	}

	// 打开下拉选回「全部合集」：不发新请求，列表回到未筛选那份。
	if err := tt.Click("按合集名筛选帖子"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("全部合集"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.profile.topicColl != "" || a.profile.topicCollDraft != profileTopicCollAll {
		t.Errorf("选全部合集后状态不对：%q / %q", a.profile.topicColl, a.profile.topicCollDraft)
	}
	if !tt.HasText("未筛选的帖子") || tt.HasText("合集里的帖子") {
		t.Errorf("选全部合集后没回到原列表：%q", tt.Texts())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("请求次数 = %d，期望合集选项 1 次 + 筛选 1 次：%v", len(calls), calls)
	}
	if calls[0] != "GET /api/v1/me/collections?" {
		t.Errorf("第一个请求 = %q，期望合集列表端点", calls[0])
	}
	want := "GET /api/v1/users/jimmy/topics?page=1&limit=50&collection=" + url.QueryEscape("读过的书")
	if calls[1] != want {
		t.Errorf("筛选请求 = %q，期望 %q", calls[1], want)
	}
}

// 选项加载失败后退避：退避函数按 10s / 20s 递增，封顶 1 分钟；窗口内跳过。
func TestProfileCollNamesBackoff(t *testing.T) {
	if got := collNamesRetryAfter(1); got != 10*time.Second {
		t.Errorf("第 1 次失败退避 = %v，期望 10s", got)
	}
	if got := collNamesRetryAfter(2); got != 20*time.Second {
		t.Errorf("第 2 次失败退避 = %v，期望 20s", got)
	}
	if got := collNamesRetryAfter(8); got != time.Minute {
		t.Errorf("多次失败退避应封顶 1 分钟，得到 %v", got)
	}
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	st := &collNamesState{triedAt: now, failures: 1}
	if st.collNamesDue(now.Add(5 * time.Second)) {
		t.Error("退避窗口内不该重发")
	}
	if !st.collNamesDue(now.Add(11 * time.Second)) {
		t.Error("退避窗口过后应重发")
	}
}

// 合集被改名或删掉后，下拉重新加载时发现选中的名字不在列表里，就清掉筛选。
func TestProfileCollNamesClearsStaleSelection(t *testing.T) {
	a, _ := profileCollectionsApp(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me/collections" {
			http.NotFound(w, r)
			return
		}
		// 选中的「读过的书」已经被改名，接口现在只返回新名字。
		_, _ = fmt.Fprint(w, envelopeJSON(`[{"id":"c1","name":"改名后的合集","topic_count":3}]`))
	})
	a.profile.tab = profileTabContent
	a.profile.contentKind = profileContentTopics
	a.profile.topicColl = "读过的书"
	a.profile.topicCollDraft = "读过的书"

	// 手动加载一次，服务端返回的名字已经变了。
	a.ensureProfileCollNames()
	waitFor(t, func() bool { return a.profile.collNames.loaded }, "合集选项没有加载")
	if a.profile.topicColl != "" || a.profile.topicCollDraft != profileTopicCollAll {
		t.Errorf("旧合集名不在列表里时应清掉筛选：%q / %q",
			a.profile.topicColl, a.profile.topicCollDraft)
	}
}

// 最小窗口宽度下合集下拉也要完整落在窗口里，不把「刷新」挤出去。
func TestProfileCollDropdownFitsMinWidth(t *testing.T) {
	a := profileWindowApp(t)
	a.profile.collNames.loaded = true
	a.profile.collNames.names = []string{"读过的书"}
	a.profile.collNames.triedAt = time.Now() // 不触发真实请求
	tt := ui.NewTester(a.profileWindowView, profileWinMinWidth, profileWinMinHeight)
	tt.Frame()
	if err := tt.Click("内容"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, label := range []string{"按合集名筛选帖子", "刷新"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("最小宽度下缺少 %q：%q", label, tt.Texts())
		}
		if r.X < 0 || r.X+r.W > profileWinMinWidth {
			t.Errorf("%q 超出 %d 宽窗口：x=%.0f w=%.0f", label, profileWinMinWidth, r.X, r.W)
		}
	}
}
