package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 合集的写端点必须打到文档路径：归入带 short_id 与 collection_id（移出时是
// null），一步新建并归入用 new_collection_name。
func TestClientCollectionEndpoints(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		bodies = append(bodies, string(body))
		mu.Unlock()
		if r.URL.Path != "/api/v1/me/collections/assign" {
			t.Errorf("意外路径 %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	ctx := context.Background()

	if err := c.assignTopicCollection(ctx, "T1", "c9"); err != nil {
		t.Fatalf("归入合集失败：%v", err)
	}
	if err := c.assignTopicCollection(ctx, "T1", ""); err != nil {
		t.Fatalf("移出合集失败：%v", err)
	}
	if err := c.assignTopicNewCollection(ctx, "T1", "新合集"); err != nil {
		t.Fatalf("新建并归入失败：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	wantCalls := []string{
		"POST /api/v1/me/collections/assign",
		"POST /api/v1/me/collections/assign",
		"POST /api/v1/me/collections/assign",
	}
	for i, w := range wantCalls {
		if i >= len(calls) || calls[i] != w {
			t.Errorf("第 %d 个请求 = %q，期望 %q（实际 %v）", i, at(calls, i), w, calls)
		}
	}
	if got := strings.TrimSpace(at(bodies, 0)); got != `{"collection_id":"c9","short_id":"T1"}` {
		t.Errorf("归入请求体 = %s", got)
	}
	if got := strings.TrimSpace(at(bodies, 1)); got != `{"collection_id":null,"short_id":"T1"}` {
		t.Errorf("移出请求体 = %s", got)
	}
	if got := strings.TrimSpace(at(bodies, 2)); got != `{"new_collection_name":"新合集","short_id":"T1"}` {
		t.Errorf("新建并归入请求体 = %s", got)
	}
}

// 合集列表整页返回，d 可能是裸数组，也可能包在 collections / items 里。
func TestDecodeCollections(t *testing.T) {
	bare := decodeCollections(json.RawMessage(`[{"id":"c1","name":"甲","topic_count":3}]`))
	if len(bare) != 1 || bare[0].ID != "c1" || bare[0].Name != "甲" || bare[0].Count != 3 {
		t.Errorf("裸数组解析不对：%+v", bare)
	}
	wrapped := decodeCollections(json.RawMessage(`{"collections":[{"id":"c2","name":"乙"}]}`))
	if len(wrapped) != 1 || wrapped[0].ID != "c2" || wrapped[0].Count != 0 {
		t.Errorf("包装形状解析不对：%+v", wrapped)
	}
	// 没有 id 或名字的项跳过：气泡里点不了，也不该显示成「未命名」。
	skip := decodeCollections(json.RawMessage(`[{"name":"没有 id"},{"id":"c3"}]`))
	if len(skip) != 0 {
		t.Errorf("不完整项不该出现：%+v", skip)
	}
}

// 详情响应里带合集归属时解析出来（文档没有这个字段，线上可能有）。
func TestDecodeTopicCollection(t *testing.T) {
	byID := decodeTopicRaw(t, `{"short_id":"T1","collection_id":"c1"}`)
	if !byID.CollectionKnown || byID.CollectionID != "c1" {
		t.Errorf("collection_id 没有解析出来：%+v", byID)
	}
	byObj := decodeTopicRaw(t, `{"short_id":"T1","collection":{"id":"c2","name":"乙"}}`)
	if !byObj.CollectionKnown || byObj.CollectionID != "c2" || byObj.CollectionName != "乙" {
		t.Errorf("collection 对象没有解析出来：%+v", byObj)
	}
	none := decodeTopicRaw(t, `{"short_id":"T1"}`)
	if none.CollectionKnown {
		t.Errorf("没有合集字段时应按未知处理：%+v", none)
	}
}

func decodeTopicRaw(t *testing.T, raw string) topicDetail {
	t.Helper()
	td, err := decodeTopic(json.RawMessage(raw), "T1")
	if err != nil {
		t.Fatalf("解析帖子失败：%v", err)
	}
	return td
}

// collectionTestApp 造一个已登录、正文已加载的详情页，合集接口由假服务端
// 提供。合集只对自己的帖子开放，所以登录账号就是夹具里那篇帖子的作者。
func collectionTestApp(t *testing.T, handler http.HandlerFunc) (*app, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	a := detailAppWithReplies(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: a.detail.topic.Author,
	}); err != nil {
		t.Fatal(err)
	}
	return a, srv
}

// 合集只对自己的帖子开放：别人的帖子、匿名帖都不给入口，免得点开只换来
// 一句 403。
func TestDetailCollectionOnlyOnOwnTopic(t *testing.T) {
	a, _ := collectionTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	if !a.canCollectTopic(a.detail) {
		t.Fatalf("夹具里这篇帖子应当属于当前账号：author=%q", a.detail.topic.Author)
	}

	for _, author := range []string{"alice", "匿名"} {
		a.detail.topic.Author = author
		a.detail.topic.CollectionKnown = true
		a.detail.topic.CollectionID = "c1"
		a.detail.coll.open = true
		if a.canCollectTopic(a.detail) {
			t.Errorf("作者为 %q 时不该给出合集入口", author)
		}
		tt := ui.NewTester(a.view, 1100, 760)
		tt.Frame()
		if _, ok := tt.Find("合集"); ok {
			t.Errorf("作者为 %q 时页面上出现了合集按钮：%q", author, tt.Texts())
		}
		if _, ok := tt.Find("已在合集"); ok {
			t.Errorf("作者为 %q 时页面上出现了「已在合集」：%q", author, tt.Texts())
		}
		// 气泡里的动作同样被挡住：不发请求。
		a.assignTopicCollection(a.detail, collectionItem{ID: "c2", Name: "别的合集"}, true)
		if a.detail.coll.busy {
			t.Errorf("作者为 %q 时仍然发起了归入请求", author)
		}
	}
}

// 详情页的「合集」胶囊：点开气泡列出我的合集，点一行就把帖子归入，成功后
// 按钮变成「已在合集」并用 toast 报告。
func TestDetailCollectionAssign(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	a, _ := collectionTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/me/collections":
			_, _ = fmt.Fprint(w, envelopeJSON(`[{"id":"c1","name":"我的收藏夹","topic_count":9}]`))
		case "/api/v1/me/collections/assign":
			_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
		default:
			http.NotFound(w, r)
		}
	})

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("合集"); err != nil {
		t.Fatalf("点不到合集按钮：%v（%q）", err, tt.Texts())
	}
	waitFor(t, func() bool { return a.detail.coll.loaded }, "合集列表没有加载出来")
	tt.Frame()
	if !tt.HasText("我的收藏夹") {
		t.Fatalf("气泡里没有列出合集：%q", tt.Texts())
	}
	if err := tt.Click("合集 我的收藏夹"); err != nil {
		t.Fatalf("点不到合集行：%v（%q）", err, tt.Texts())
	}
	waitFor(t, func() bool {
		return !a.detail.coll.busy && a.detail.topic.CollectionID == "c1"
	}, "归入合集请求没有完成")
	tt.Frame()

	if !tt.HasText("已加入合集「我的收藏夹」") {
		t.Errorf("没有报告归入结果：%q", tt.Texts())
	}
	if _, ok := tt.Find("已在合集"); !ok {
		t.Errorf("按钮没有变成「已在合集」：%q", tt.Texts())
	}
	mu.Lock()
	defer mu.Unlock()
	// 打开详情本身会拉评论；这里只关心合集相关的两次请求。
	var got []string
	for _, c := range calls {
		if strings.Contains(c, "/me/collections") {
			got = append(got, c)
		}
	}
	want := []string{"GET /api/v1/me/collections", "POST /api/v1/me/collections/assign"}
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Errorf("第 %d 个请求 = %q，期望 %q（实际 %v）", i, at(got, i), w, got)
		}
	}
}

// 新建合集后直接归入：只发一次写请求（new_collection_name），随后重读合集
// 列表补齐 id，按钮同步变成「已在合集」。
func TestDetailCollectionCreateAndAssign(t *testing.T) {
	var mu sync.Mutex
	var creates int
	var assignBody string
	a, _ := collectionTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/me/collections":
			if r.Method == http.MethodPost {
				mu.Lock()
				creates++
				mu.Unlock()
			}
			_, _ = fmt.Fprint(w, envelopeJSON(`[{"id":"c7","name":"临时夹"}]`))
		case "/api/v1/me/collections/assign":
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			mu.Lock()
			assignBody = string(body)
			mu.Unlock()
			_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
		default:
			http.NotFound(w, r)
		}
	})
	a.detail.coll.loaded = true
	a.detail.coll.draft = "临时夹"
	a.createCollectionForTopic(a.detail)

	waitFor(t, func() bool {
		return !a.detail.coll.creating && !a.detail.coll.busy && a.detail.topic.CollectionID == "c7"
	}, "新建并归入没有完成")

	mu.Lock()
	defer mu.Unlock()
	if creates != 0 {
		t.Errorf("独立的新建请求次数 = %d，期望 0（应一次写入完成）", creates)
	}
	if !strings.Contains(assignBody, `"new_collection_name":"临时夹"`) {
		t.Errorf("归入请求体没有带上新合集名：%s", assignBody)
	}
	if strings.Contains(assignBody, `"collection_id"`) {
		t.Errorf("一步新建不该带 collection_id：%s", assignBody)
	}
	if a.detail.coll.draft != "" {
		t.Errorf("新建成功后输入框没有清空：%q", a.detail.coll.draft)
	}
	if a.detail.topic.CollectionName != "临时夹" {
		t.Errorf("合集名没有写回详情：%+v", a.detail.topic)
	}
}

// 空名字不提交：避免发一个必然被服务端拒绝的请求。
func TestDetailCollectionCreateRejectsEmptyName(t *testing.T) {
	var calls int
	a, _ := collectionTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/me/collections") {
			calls++
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
	})
	a.detail.coll.draft = "   "
	a.createCollectionForTopic(a.detail)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if calls != 0 {
		t.Errorf("空名字不该发请求：%d 次", calls)
	}
	if !tt.HasText("请先填写合集名称") {
		t.Errorf("没有提示名称必填：%q", tt.Texts())
	}
}

// 文档给的合集名上限是 30 字（api-write.md 的「合集」一节），界面按它挡掉超长输入。
func TestCollectionNameLimit(t *testing.T) {
	if collectionNameMax != 30 {
		t.Fatalf("合集名上限 = %d，期望 30", collectionNameMax)
	}
	var calls int
	a, _ := collectionTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/me/collections") {
			calls++
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
	})
	a.detail.coll.draft = strings.Repeat("字", 31)
	a.createCollectionForTopic(a.detail)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if calls != 0 {
		t.Errorf("超长名字不该发请求：%d 次", calls)
	}
	if !tt.HasText("合集名称最多 30 个字") {
		t.Errorf("没有提示名称上限：%q", tt.Texts())
	}
}

// 合集的三个路径都按文档拼：改名 / 删除打 /v1/me/collections/:id，公开合集
// 挂在用户下，合集内帖子挂在 /v1/collections/:id 下；id 与昵称都要转义。
func TestCollectionPaths(t *testing.T) {
	cases := []struct{ got, want string }{
		{collectionPath("c1"), "/v1/me/collections/c1"},
		{collectionPath("a/b?c"), "/v1/me/collections/a%2Fb%3Fc"},
		{userCollectionsPath("jimmy"), "/v1/users/jimmy/collections"},
		{userCollectionsPath("a b"), "/v1/users/a%20b/collections"},
		{collectionTopicsPath("c1"), "/v1/collections/c1/topics"},
		{collectionTopicsPath("c/1"), "/v1/collections/c%2F1/topics"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("路径 = %q，期望 %q", c.got, c.want)
		}
	}
}

// 改名是 PATCH 且只带 name；删除是同一个路径上的 DELETE。404 表示目标状态
// 已经成立（合集早就不在了），不该弹成错误。
func TestClientCollectionEditEndpoints(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		bodies = append(bodies, string(body))
		mu.Unlock()
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/me/collections/c1":
			_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"c1","name":"新名字","topic_count":4}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/me/collections/c1":
			_, _ = fmt.Fprint(w, envelopeJSON(`{"ok":true}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/me/collections/gone":
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"c":404,"code":"not_found","m":"合集不存在","d":null}`)
		default:
			t.Errorf("意外请求 %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	ctx := context.Background()

	item, err := c.renameCollection(ctx, "c1", "新名字")
	if err != nil {
		t.Fatalf("改名失败：%v", err)
	}
	if item.ID != "c1" || item.Name != "新名字" || item.Count != 4 {
		t.Errorf("改名结果解析不对：%+v", item)
	}
	if err := c.deleteCollection(ctx, "c1"); err != nil {
		t.Fatalf("删除合集失败：%v", err)
	}
	if err := c.deleteCollection(ctx, "gone"); err != nil {
		t.Errorf("404 应当视为已删除：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"PATCH /api/v1/me/collections/c1",
		"DELETE /api/v1/me/collections/c1",
		"DELETE /api/v1/me/collections/gone",
	}
	for i, w := range want {
		if i >= len(calls) || calls[i] != w {
			t.Errorf("第 %d 个请求 = %q，期望 %q（实际 %v）", i, at(calls, i), w, calls)
		}
	}
	if got := strings.TrimSpace(at(bodies, 0)); got != `{"name":"新名字"}` {
		t.Errorf("改名请求体 = %s", got)
	}
}

// 某用户的公开合集：匿名可读的端点，昵称要转义，整页返回（裸数组）。
func TestFetchUserCollections(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/api/v1/users/jimmy/collections" {
			t.Errorf("路径不对：%s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`[{"id":"c1","name":"职场","topic_count":2}]`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api"}

	items, err := c.fetchUserCollections(context.Background(), "jimmy")
	if err != nil {
		t.Fatalf("读公开合集失败：%v", err)
	}
	if len(items) != 1 || items[0].ID != "c1" || items[0].Name != "职场" || items[0].Count != 2 {
		t.Errorf("公开合集解析不对：%+v", items)
	}
	// 空昵称不发请求：拼出来的路径会落到别人的列表上。
	if _, err := c.fetchUserCollections(context.Background(), ""); err == nil {
		t.Error("空昵称应当直接报错，不该发请求")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Errorf("不该发出多余请求：%v", got)
	}
}

// 合集内帖子是普通的页码分页帖子列表；文档没约定列表字段名，所以用 auto。
func TestCollectionTopicsSpec(t *testing.T) {
	spec := collectionTopicsSpec("c1")
	if spec.Path != "/v1/collections/c1/topics" {
		t.Errorf("路径 = %q", spec.Path)
	}
	if spec.Query != "page=1&limit=50" {
		t.Errorf("第 1 页 query = %q", spec.Query)
	}
	if spec.ListKey != "auto" || spec.Kind != kindTopic || spec.Paging != pagingPages {
		t.Errorf("spec 不对：%+v", spec)
	}
	// 第 2 页只替换 page，其它参数原样保留（编码后按字母序重排）。
	if got := endpoint("https://2libra.com/api", spec, 2); got != "https://2libra.com/api/v1/collections/c1/topics?limit=50&page=2" {
		t.Errorf("第 2 页 URL = %q", got)
	}
}
