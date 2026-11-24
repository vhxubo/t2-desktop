package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 详情页正文末尾的当前合集卡片：条数来自合集内帖子列表，「查看全部」打开发帖人
// 资料卡的「ta的合集」浏览视图，「上一篇」在同一合集里打开前一条帖子。
func TestDetailCurrentCollectionCard(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Path == "/api/v1/collections/c1/topics" {
			_, _ = fmt.Fprint(w, envelopeJSON(`{"posts":[
				{"short_id":"T0","title":"合集第一篇"},
				{"short_id":"T1","title":"合集第二篇"},
				{"short_id":"T2","title":"合集第三篇"}],
				"total":3,"page":1,"limit":50,"total_pages":1}`))
			return
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()

	a := detailAppWithReplies(t)
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: a.detail.topic.Author,
	}); err != nil {
		t.Fatal(err)
	}
	a.autoLoad = true
	d := a.detail
	d.shortID = "T1"
	d.topic.CollectionKnown = true
	d.topic.CollectionID = "c1"
	d.topic.CollectionName = "甲合集"
	a.ensureDetailCollectionNav(d)
	waitFor(t, func() bool { return d.collNav.loaded }, "合集导航没有加载出来")

	mu.Lock()
	want := "GET /api/v1/collections/c1/topics?page=1&limit=50"
	if len(calls) == 0 || calls[0] != want {
		t.Errorf("第一个请求 = %v，期望 %q", calls, want)
	}
	mu.Unlock()

	// 无头测试里不必让「我的」窗口再发资料请求。
	a.autoLoad = false
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"合集 · 甲合集", "共 3 篇", "查看全部", "合集第一篇"} {
		if !tt.HasText(want) {
			t.Errorf("合集卡片缺少 %q：%q", want, tt.Texts())
		}
	}

	if err := tt.Click("查看全部"); err != nil {
		t.Fatalf("点不到「查看全部」：%v", err)
	}
	if a.userCard == nil || a.userCard.collBrowse == nil || a.userCard.collBrowse.id != "c1" {
		t.Fatalf("「查看全部」没有打开发帖人的合集浏览视图：%+v", a.userCard)
	}
	if a.userCard.tab != userCardTabCollections || a.userCard.username != d.topic.Author {
		t.Errorf("没有定位到发帖人的「ta的合集」：tab=%d user=%q",
			a.userCard.tab, a.userCard.username)
	}
	cardTT := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	cardTT.Frame()
	for _, want := range []string{"合集 · 甲合集", "共 3 篇"} {
		if !cardTT.HasText(want) {
			t.Errorf("资料卡里没有展示合集：%q", cardTT.Texts())
		}
	}

	tt.Frame()
	if err := tt.Click("上一篇 合集第一篇"); err != nil {
		t.Fatalf("点不到「上一篇」：%v", err)
	}
	if a.detail == d || a.detail.shortID != "T0" {
		t.Fatalf("「上一篇」没有打开合集里的前一条：%+v", a.detail)
	}
}

// 当前帖不在第一页时也要找得到它的上一篇：按页续拉，直到在列表里认出自己。
func TestDetailCollectionPrevAcrossPages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/collections/c1/topics" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			_, _ = fmt.Fprint(w, envelopeJSON(`{"posts":[
				{"short_id":"T3","title":"第三篇"},
				{"short_id":"T2","title":"第四篇"}],
				"total":4,"page":2,"limit":50,"total_pages":2}`))
			return
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{"posts":[
			{"short_id":"T5","title":"第一篇"},
			{"short_id":"T4","title":"第二篇"}],
			"total":4,"page":1,"limit":50,"total_pages":2}`))
	}))
	defer srv.Close()

	a := detailAppWithReplies(t)
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.autoLoad = true
	d := a.detail
	d.shortID = "T2"
	d.topic.CollectionKnown = true
	d.topic.CollectionID = "c1"
	d.topic.CollectionName = "甲合集"
	a.ensureDetailCollectionNav(d)
	waitFor(t, func() bool { return d.collNav.loaded }, "合集导航没有加载出来")

	if d.collNav.count != 4 || !d.collNav.hasPrev {
		t.Fatalf("合集条数或上一篇不对：%+v", d.collNav)
	}
	if d.collNav.prev.ShortID != "T3" || d.collNav.prev.Title != "第三篇" {
		t.Errorf("上一篇 = %+v，期望第三篇", d.collNav.prev)
	}
}

// 正文没有合集归属时不画卡片：不给「查看全部」也不给「上一篇」。
func TestDetailCollectionCardOnlyWhenJoined(t *testing.T) {
	a := detailAppWithReplies(t)
	a.detail.topic.CollectionKnown = false
	a.detail.topic.CollectionID = ""
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if tt.HasText("合集 ·") || tt.HasText("查看全部") {
		t.Errorf("没归合集时不该出现合集卡片：%q", tt.Texts())
	}
}

// 线上真实路径：详情接口的稳定字段没有合集归属，合集是「从合集浏览点开」
// 时带进来的。帖子行带着 CollectionID 打开详情，正文照样能画出卡片。
func TestDetailCollectionCardFromBrowseItem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/collections/c1/topics" {
			_, _ = fmt.Fprint(w, envelopeJSON(`{"posts":[
				{"short_id":"T0","title":"合集第一篇"},
				{"short_id":"T1","title":"合集第二篇"}],
				"total":2,"page":1,"limit":50,"total_pages":1}`))
			return
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()

	a := detailAppWithReplies(t)
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.autoLoad = true
	a.openDetail(Item{ShortID: "T1", CollectionID: "c1", CollectionName: "甲合集"})
	d := a.detail
	if id, _, known := d.collectionContext(); !known || id != "c1" {
		t.Fatalf("合集上下文没有带进详情：%+v", d)
	}
	waitFor(t, func() bool { return d.collNav.loaded }, "合集导航没有加载出来")

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"合集 · 甲合集", "共 2 篇", "合集第一篇"} {
		if !tt.HasText(want) {
			t.Errorf("从合集点开后卡片缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 线上详情带 d.post_collection 时，即使不是从合集浏览点开、列表还没加载，
// 正文也能画出卡片，条数先用响应里自带的 topic_count。
func TestDetailCollectionCardFromPostCollection(t *testing.T) {
	a := detailAppWithReplies(t)
	a.autoLoad = false
	topic, err := decodeTopic([]byte(`{"short_id":"T1","title":"帖子",
		"post_collection":{"id":"c1","name":"甲合集","topic_count":7}}`), "T1")
	if err != nil {
		t.Fatal(err)
	}
	a.detail.topic = &topic
	a.detail.collID, a.detail.collName = "", ""

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"合集 · 甲合集", "共 7 篇", "查看全部"} {
		if !tt.HasText(want) {
			t.Errorf("post_collection 卡片缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 详情接口自带 prev / next 时，卡片立即画出两个入口；点「下一篇」直接打开，
// 不必先拉合集内帖子列表。
func TestDetailCollectionNeighborShortcuts(t *testing.T) {
	var collectionCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/collections/") {
			collectionCalls++
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()

	a := detailAppWithReplies(t)
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.autoLoad = true
	topic, err := decodeTopic([]byte(`{"short_id":"T1","title":"帖子",
		"post_collection":{"id":"c1","name":"社区相关","post_count":6,
			"prev":{"short_id":"LoFrwAQ","node_slug":"forum-function","title":"评论消耗计算"},
			"next":{"short_id":"NxT1234","node_slug":"personal-works","title":"下一篇脚本"}}}`), "T1")
	if err != nil {
		t.Fatal(err)
	}
	a.detail.topic = &topic

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"合集 · 社区相关", "共 6 篇", "上一篇 评论消耗计算", "下一篇 下一篇脚本"} {
		if !tt.HasText(want) {
			t.Errorf("合集卡片缺少 %q：%q", want, tt.Texts())
		}
	}
	if collectionCalls != 0 {
		t.Errorf("详情已给相邻两篇时不该再拉合集列表，实际 %d 次", collectionCalls)
	}

	if err := tt.Click("下一篇 下一篇脚本"); err != nil {
		t.Fatalf("点不到「下一篇」：%v（%q）", err, tt.Texts())
	}
	if a.detail.shortID != "NxT1234" {
		t.Fatalf("「下一篇」没有打开接口给的帖子：%+v", a.detail)
	}
	if a.detail.collID != "c1" || a.detail.collName != "社区相关" {
		t.Errorf("打开下一篇时没带上合集上下文：%+v", a.detail)
	}
}

// 合集浏览视图点一行时把合集 id / 名字盖到那一行的 Item 上，详情页才有上下文。
func TestCollectionBrowseStampsContextOnOpen(t *testing.T) {
	a := detailAppWithReplies(t)
	a.autoLoad = false
	a.profile = newProfileState()
	a.profile.tab = profileTabContent
	a.profile.contentKind = profileContentCollection
	b := newCollectionBrowse(collectionItem{ID: "c1", Name: "甲合集"}, "", toastProfile)
	b.list.loaded = true
	b.list.items = []Item{{Key: "T1", ShortID: "T1", Title: "合集里的一篇"}}
	a.profile.collBrowse = b

	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if err := tt.Click("合集里的一篇"); err != nil {
		t.Fatalf("点不到合集里的帖子：%v（%q）", err, tt.Texts())
	}
	if a.detail == nil || a.detail.collID != "c1" || a.detail.collName != "甲合集" {
		t.Fatalf("点开合集帖子没有带上合集上下文：%+v", a.detail)
	}
}
