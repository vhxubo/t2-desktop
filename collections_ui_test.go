package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// profileCollectionsApp 造一个已登录、已经停在「我的内容 - 合集」的窗口状态：
// 合集列表里有一项，合集相关的请求都打到假服务端上。
func profileCollectionsApp(t *testing.T, handler http.HandlerFunc) (*app, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	a := profileWindowApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.profile.tab = profileTabContent
	a.profile.contentKind = profileContentCollection
	st := &a.profile.content[profileContentCollections]
	st.loaded = true
	st.items = []Item{{Key: "c1", Title: "甲合集", Meta: "3 个帖子"}}
	return a, srv
}

// 「合集」分段的每一行都有改名与删除两枚图标按钮：合集是我自己的内容，
// 在这个窗口里就能改，不必回站点。
func TestProfileCollectionRowActions(t *testing.T) {
	a, _ := profileCollectionsApp(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()

	if !tt.HasText("甲合集") {
		t.Fatalf("合集列表没有画出来：%q", tt.Texts())
	}
	for _, label := range []string{"重命名合集 甲合集", "删除合集 甲合集"} {
		if _, ok := tt.Find(label); !ok {
			t.Errorf("合集行缺少「%s」按钮：%q", label, tt.Texts())
		}
	}

	// 点改名：对话框带着输入框与保存按钮出现。
	if err := tt.Click("重命名合集 甲合集"); err != nil {
		t.Fatalf("点不到改名按钮：%v", err)
	}
	tt.Frame()
	for _, want := range []string{"重命名合集", "保存", "取消"} {
		if !tt.HasText(want) {
			t.Errorf("改名对话框缺少 %q：%q", want, tt.Texts())
		}
	}
	if a.profile.collEdit != "c1" || a.profile.collDraft != "甲合集" {
		t.Errorf("改名状态不对：edit=%q draft=%q", a.profile.collEdit, a.profile.collDraft)
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatalf("点不到取消：%v", err)
	}
	tt.Frame()
	if a.profile.collEdit != "" {
		t.Errorf("取消后不该还停在改名状态：%q", a.profile.collEdit)
	}

	// 整行是「打开合集」的入口：点标题进合集浏览视图，再点返回回到列表。
	if err := tt.Click("甲合集"); err != nil {
		t.Fatalf("点不到合集行：%v", err)
	}
	if a.profile.collBrowse == nil || a.profile.collBrowse.id != "c1" {
		t.Fatalf("点合集行没有进入浏览视图：%+v", a.profile.collBrowse)
	}
	tt.Frame()
	if err := tt.Click("返回合集列表"); err != nil {
		t.Fatalf("点不到返回按钮：%v", err)
	}
	if a.profile.collBrowse != nil {
		t.Error("点返回后应当回到合集列表")
	}
}

// 改名走 PATCH，成功后用窗口内 toast 报告并重读列表；名字没变时不该发请求
// 之外的任何写操作（这里只验证一次请求）。
func TestProfileCollectionRenameSubmits(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var patchBody string
	a, _ := profileCollectionsApp(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/me/collections/c1":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			patchBody = string(body)
			mu.Unlock()
			_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"c1","name":"乙合集"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me/collections":
			_, _ = fmt.Fprint(w, envelopeJSON(`[{"id":"c1","name":"乙合集","topic_count":3}]`))
		default:
			http.NotFound(w, r)
		}
	})

	a.askRenameCollection(Item{Key: "c1", Title: "甲合集"})
	a.profile.collDraft = "乙合集"
	a.submitRenameCollection()
	waitFor(t, func() bool {
		return a.profile.collBusy == "" && a.profile.collEdit == ""
	}, "改名请求没有完成")

	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if !tt.HasText("已改名为「乙合集」") {
		t.Errorf("没有报告改名结果：%q", tt.Texts())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) == 0 || calls[0] != "PATCH /api/v1/me/collections/c1" {
		t.Fatalf("第一个请求 = %v，期望一次 PATCH", calls)
	}
	if strings.TrimSpace(patchBody) != `{"name":"乙合集"}` {
		t.Errorf("改名请求体 = %s", patchBody)
	}
}

// 删除要先确认（对话框里说明帖子不受影响），确认后发 DELETE，并用 toast
// 报告；正在浏览的正是这个合集时把它关掉。
func TestProfileCollectionDeleteSubmits(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	a, _ := profileCollectionsApp(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/me/collections/c1":
			_, _ = fmt.Fprint(w, envelopeJSON(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me/collections":
			_, _ = fmt.Fprint(w, envelopeJSON(`[]`))
		default:
			http.NotFound(w, r)
		}
	})

	a.openProfileCollection(Item{Key: "c1", Title: "甲合集"})
	a.askDeleteCollection(Item{Key: "c1", Title: "甲合集"})
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	for _, want := range []string{"删除合集？", "确认删除", "不可恢复"} {
		if !tt.HasText(want) {
			t.Errorf("删除确认框缺少 %q：%q", want, tt.Texts())
		}
	}
	a.submitDeleteCollection()
	waitFor(t, func() bool {
		return a.profile.collBusy == "" && a.profile.collDelete == ""
	}, "删除请求没有完成")

	// 结果提示排在 collBusy 清空之后：等它出现在窗口上。
	waitForShownToast(t, a, tt, "已删除合集「甲合集」")
	if a.profile.collBrowse != nil {
		t.Error("删除后不该还停在已删合集的浏览视图里")
	}

	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, c := range calls {
		if c == "DELETE /api/v1/me/collections/c1" {
			found = true
		}
	}
	if !found {
		t.Errorf("没有发出删除请求：%v", calls)
	}
}

// 点开一个合集：按文档路径读 /v1/collections/:id/topics，列表里显示帖子，
// 「返回合集列表」回到合集列表。
func TestProfileCollectionBrowseTopics(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	a, _ := profileCollectionsApp(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Path == "/api/v1/collections/c1/topics" {
			_, _ = fmt.Fprint(w, envelopeJSON(`{"posts":[{"short_id":"T1","title":"合集里的一篇",`+
				`"author":{"username":"jimmy"},"node":{"name":"职场"},"created_at":"2026-11-01T09:00:00Z"}],`+
				`"total":1,"page":1,"limit":50,"total_pages":1}`))
			return
		}
		http.NotFound(w, r)
	})

	a.openProfileCollection(Item{Key: "c1", Title: "甲合集"})
	waitFor(t, func() bool {
		return a.profile.collBrowse != nil && a.profile.collBrowse.list.loaded
	}, "合集里的帖子没有加载出来")

	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	for _, want := range []string{"合集 · 甲合集", "合集里的一篇", "返回合集列表"} {
		if !tt.HasText(want) {
			t.Errorf("合集浏览视图缺少 %q：%q", want, tt.Texts())
		}
	}

	if err := tt.Click("返回合集列表"); err != nil {
		t.Fatalf("点不到返回按钮：%v", err)
	}
	if a.profile.collBrowse != nil {
		t.Error("点返回后应当回到合集列表")
	}

	mu.Lock()
	defer mu.Unlock()
	want := "GET /api/v1/collections/c1/topics?page=1&limit=50"
	if len(calls) == 0 || calls[0] != want {
		t.Errorf("第一个请求 = %v，期望 %q", calls, want)
	}
}

// 资料卡的「ta的合集」分段：按文档路径读公开合集，点开一项就换成合集里的
// 帖子，返回时回到合集列表。
func TestUserCardCollectionsTab(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/users/alice/collections":
			_, _ = fmt.Fprint(w, envelopeJSON(`[{"id":"c9","name":"爱丽丝的合集","topic_count":4}]`))
		case r.URL.Path == "/api/v1/collections/c9/topics":
			_, _ = fmt.Fprint(w, envelopeJSON(`{"items":[{"short_id":"T9","title":"ta的帖子"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := testApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.userCard = newUserCardState("alice")
	a.userCard.tab = userCardTabCollections

	if got := userCardSpec("alice", userCardCollections, 1).Path; got != "/v1/users/alice/collections" {
		t.Fatalf("公开合集路径 = %q", got)
	}
	a.loadUserCardList(userCardCollections, 1)
	waitFor(t, func() bool { return a.userCard.collections.loaded }, "公开合集没有加载出来")
	// 后面的渲染不再自动补资料 / 主题：测试只关心合集这两次请求。
	a.autoLoad = false

	cs := a.userCard
	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	for _, want := range []string{"ta的合集", "爱丽丝的合集", "4 个帖子"} {
		if !tt.HasText(want) {
			t.Errorf("资料卡缺少 %q：%q", want, tt.Texts())
		}
	}

	a.openUserCardCollection(Item{Key: "c9", Title: "爱丽丝的合集"})
	waitFor(t, func() bool {
		return cs.collBrowse != nil && cs.collBrowse.list.loaded
	}, "合集里的帖子没有加载出来")
	tt.Frame()
	for _, want := range []string{"合集 · 爱丽丝的合集", "by @alice", "ta的帖子"} {
		if !tt.HasText(want) {
			t.Errorf("合集浏览视图缺少 %q：%q", want, tt.Texts())
		}
	}
	cs.collBrowse = nil // 返回列表

	mu.Lock()
	defer mu.Unlock()
	wantCalls := []string{
		"GET /api/v1/users/alice/collections?",
		"GET /api/v1/collections/c9/topics?page=1&limit=50",
	}
	for i, w := range wantCalls {
		if i >= len(calls) || calls[i] != w {
			t.Errorf("第 %d 个请求 = %q，期望 %q（实际 %v）", i, at(calls, i), w, calls)
		}
	}
}
