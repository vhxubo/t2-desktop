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
	"time"

	"github.com/egoist/mygo/ui"
)

// waitFor 轮询一个条件直到成立：写操作在后台 goroutine 里完成，帧渲染不等
// 网络，测试必须等结果落回状态再断言。
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 书签端点的路径与转义必须严格照文档：评论 id 是路径段。
func TestReplyBookmarkPathMatchesContract(t *testing.T) {
	if got, want := replyBookmarkPath("c1"), "/v1/replies/c1/bookmark"; got != want {
		t.Errorf("书签路径 = %s，期望 %s", got, want)
	}
	if got, want := replyBookmarkPath("a/b?c"), "/v1/replies/a%2Fb%3Fc/bookmark"; got != want {
		t.Errorf("评论 id 未转义：%s", got)
	}
}

// POST 带空对象请求体（remark 可选），DELETE 不带 body；两者都带令牌。
func TestClientReplyBookmarkEndpoints(t *testing.T) {
	var mu sync.Mutex
	var got []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.RequestURI()+" auth="+r.Header.Get("Authorization"))
		bodies = append(bodies, string(body))
		mu.Unlock()
		if r.URL.Path != "/api/v1/replies/c1/bookmark" {
			t.Errorf("意外路径 %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	ctx := context.Background()

	if err := c.setReplyBookmark(ctx, "c1", true); err != nil {
		t.Fatalf("加书签失败：%v", err)
	}
	if err := c.setReplyBookmark(ctx, "c1", false); err != nil {
		t.Fatalf("取消书签失败：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"POST /api/v1/replies/c1/bookmark auth=Bearer tok",
		"DELETE /api/v1/replies/c1/bookmark auth=Bearer tok",
	}
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Errorf("第 %d 个请求 = %q，期望 %q（实际 %v）", i, at(got, i), w, got)
		}
	}
	// remark 是可选的：POST 发空对象，别让服务端把「没有 body」当坏请求。
	if len(bodies) == 0 || strings.TrimSpace(bodies[0]) != "{}" {
		t.Errorf("加书签的请求体 = %q，期望 {}", at(bodies, 0))
	}
}

// 重复加 / 重复移除的 409 视为成功：目标状态已经成立。
func TestReplyBookmarkConflictIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"c":409,"code":"conflict","m":"已加书签","d":null}`)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	if err := c.setReplyBookmark(context.Background(), "c1", true); err != nil {
		t.Errorf("409 conflict 应按成功处理：%v", err)
	}
}

// 评论的书签状态来自 is_bookmarked；匿名响应没有这个键时按「未知」处理，
// 不能当成「没加书签」。
func TestDecodeReplyBookmarkState(t *testing.T) {
	with := decodeReply(json.RawMessage(`{"id":"c1","content":"x","is_bookmarked":true}`), 0)
	if !with.BookmarkKnown || !with.Bookmarked {
		t.Errorf("is_bookmarked=true 没有解析出来：%+v", with)
	}
	off := decodeReply(json.RawMessage(`{"id":"c2","content":"x","is_bookmarked":false}`), 0)
	if !off.BookmarkKnown || off.Bookmarked {
		t.Errorf("is_bookmarked=false 解析不对：%+v", off)
	}
	anon := decodeReply(json.RawMessage(`{"id":"c3","content":"x"}`), 0)
	if anon.BookmarkKnown {
		t.Errorf("没有 is_bookmarked 时应按未知处理：%+v", anon)
	}
}

// 详情页评论底行的书签按钮：点一下打 POST，成功后就地变成「取消书签」，
// 并用 toast 报告结果。
func TestDetailReplyBookmarkToggles(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
	}))
	defer srv.Close()

	a := detailAppWithReplies(t)
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.detail.replyList = []replyItem{{
		ID: "c1", Author: "dannyng", Floor: 1, Content: "第一楼",
	}}
	a.detail.display = a.detail.replyList
	a.detail.replyTotal = 1

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("加书签"); err != nil {
		t.Fatalf("点不到书签按钮：%v（%q）", err, tt.Texts())
	}
	waitFor(t, func() bool { return a.detail.bmBusy == "" && a.detail.replyList[0].Bookmarked },
		"加书签请求没有完成")
	tt.Frame()

	mu.Lock()
	if len(calls) != 1 || calls[0] != "POST /api/v1/replies/c1/bookmark" {
		t.Errorf("书签请求 = %v", calls)
	}
	mu.Unlock()
	if !a.detail.replyList[0].Bookmarked || !a.detail.replyList[0].BookmarkKnown {
		t.Errorf("成功后评论状态没更新：%+v", a.detail.replyList[0])
	}
	if !tt.HasText("已加书签") {
		t.Errorf("没有报告加书签结果：%q", tt.Texts())
	}
	if _, ok := tt.Find("取消书签"); !ok {
		t.Errorf("按钮没有变成「取消书签」：%q", tt.Texts())
	}

	// 再点一次是移除。
	if err := tt.Click("取消书签"); err != nil {
		t.Fatalf("点不到取消书签：%v", err)
	}
	waitFor(t, func() bool { return a.detail.bmBusy == "" && !a.detail.replyList[0].Bookmarked },
		"取消书签请求没有完成")
	tt.Frame()
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[1] != "DELETE /api/v1/replies/c1/bookmark" {
		t.Errorf("取消书签请求 = %v", calls)
	}
	if a.detail.replyList[0].Bookmarked {
		t.Error("取消后评论仍标记为已加书签")
	}
}

// 「我的 - 书签」里可以直接移除：成功后这一行从列表里消失，详情页里同一条
// 评论的按钮状态也一并纠正。
func TestProfileBookmarkRemove(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
	}))
	defer srv.Close()

	a := testApp(t)
	m := newAuthManager(t.TempDir())
	if err := m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "reader"}); err != nil {
		t.Fatal(err)
	}
	a.auth = m
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.profile = newProfileState()
	st := &a.profile.content[profileContentBookmarks]
	st.loaded = true
	st.items = []Item{{Key: "c1", CommentID: "c1", Title: "书签里的评论"}}
	st.total = 1
	a.profile.tab = profileTabContent
	a.profile.contentKind = profileContentBookmarks
	a.detail = &detailState{replyList: []replyItem{{ID: "c1", Bookmarked: true, BookmarkKnown: true}}}

	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if err := tt.Click("移除书签"); err != nil {
		t.Fatalf("点不到移除按钮：%v（%q）", err, tt.Texts())
	}
	waitFor(t, func() bool { return a.profile.bookmarkBusy == "" && len(st.items) == 0 },
		"移除书签请求没有完成")
	tt.Frame()

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != "DELETE /api/v1/replies/c1/bookmark" {
		t.Errorf("移除书签请求 = %v", calls)
	}
	if len(st.items) != 0 {
		t.Errorf("移除后列表里还留着这一行：%+v", st.items)
	}
	if a.detail.replyList[0].Bookmarked {
		t.Error("详情页里同一条评论的书签状态没纠正")
	}
	if !tt.HasText("已移除书签") {
		t.Errorf("没有报告移除结果：%q", tt.Texts())
	}
}
