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

// 合集的两个写端点必须打到文档路径，新建只带 name，归入带 short_id 与
// collection_id（移出时是 null）。
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
		switch r.URL.Path {
		case "/api/v1/me/collections":
			_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"c9","name":"新合集","topic_count":0}`))
		case "/api/v1/me/collections/assign":
			_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
		default:
			t.Errorf("意外路径 %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	ctx := context.Background()

	item, err := c.createCollection(ctx, "新合集")
	if err != nil {
		t.Fatalf("新建合集失败：%v", err)
	}
	if item.ID != "c9" || item.Name != "新合集" {
		t.Errorf("新建结果解析不对：%+v", item)
	}
	if err := c.assignTopicCollection(ctx, "T1", "c9"); err != nil {
		t.Fatalf("归入合集失败：%v", err)
	}
	if err := c.assignTopicCollection(ctx, "T1", ""); err != nil {
		t.Fatalf("移出合集失败：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	wantCalls := []string{
		"POST /api/v1/me/collections",
		"POST /api/v1/me/collections/assign",
		"POST /api/v1/me/collections/assign",
	}
	for i, w := range wantCalls {
		if i >= len(calls) || calls[i] != w {
			t.Errorf("第 %d 个请求 = %q，期望 %q（实际 %v）", i, at(calls, i), w, calls)
		}
	}
	if got := strings.TrimSpace(at(bodies, 0)); got != `{"name":"新合集"}` {
		t.Errorf("新建请求体 = %s", got)
	}
	if got := strings.TrimSpace(at(bodies, 1)); got != `{"collection_id":"c9","short_id":"T1"}` {
		t.Errorf("归入请求体 = %s", got)
	}
	if got := strings.TrimSpace(at(bodies, 2)); got != `{"collection_id":null,"short_id":"T1"}` {
		t.Errorf("移出请求体 = %s", got)
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
// 提供。
func collectionTestApp(t *testing.T, handler http.HandlerFunc) (*app, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	a := detailAppWithReplies(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	return a, srv
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

// 新建合集后直接归入：两次写请求，气泡里的名字立刻可用，按钮同步变成
// 「已在合集」。
func TestDetailCollectionCreateAndAssign(t *testing.T) {
	var mu sync.Mutex
	var creates int
	var assignBody string
	a, _ := collectionTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/me/collections":
			mu.Lock()
			creates++
			mu.Unlock()
			_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"c7","name":"临时夹"}`))
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
	if creates != 1 {
		t.Errorf("新建请求次数 = %d，期望 1", creates)
	}
	if !strings.Contains(assignBody, `"collection_id":"c7"`) {
		t.Errorf("归入请求体没有带上新合集：%s", assignBody)
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
