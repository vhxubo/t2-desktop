package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 「关闭 / 开启评论」端点的契约与行为：
//
//	POST /v1/topics/:short_id/comments-status   scope write:topic，仅作者
//	请求体只有 closed；端点没有 🔑，不带 Idempotency-Key。
//
// 路径、字段名、「只有作者能开关」与「需开通」都按 api-write.md 原文，逐条断言。

func TestCommentsStatusPathMatchesContract(t *testing.T) {
	if got, want := topicCommentsStatusPath("a1b2"), "/v1/topics/a1b2/comments-status"; got != want {
		t.Errorf("评论开关路径 = %s，期望 %s", got, want)
	}
	// short_id 是路径段：转义由 topicPath 统一做，不能被绕过。
	if got, want := topicCommentsStatusPath("a/b?c"), "/v1/topics/a%2Fb%3Fc/comments-status"; got != want {
		t.Errorf("short_id 未转义：%s，期望 %s", got, want)
	}
}

// commentsStatusRecorder 记下一次评论开关请求，并回一个按 closed 变化的响应。
type commentsStatusRecorder struct {
	mu     sync.Mutex
	calls  []string
	auth   string
	key    string
	body   map[string]any
	status int    // 非 0 时用作 HTTP 状态码
	errEnv string // status 非 0 时返回的完整错误信封
}

func newCommentsStatusServer(t *testing.T, rec *commentsStatusRecorder) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		closed, _ := body["closed"].(bool)
		rec.mu.Lock()
		rec.calls = append(rec.calls, r.Method+" "+r.URL.Path)
		rec.auth = r.Header.Get("Authorization")
		rec.key = r.Header.Get("Idempotency-Key")
		rec.body = body
		status, errEnv := rec.status, rec.errEnv
		rec.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, errEnv)
			return
		}
		_, _ = fmt.Fprint(w, envelopeJSON(fmt.Sprintf(`{"comments_closed":%v}`, closed)))
	}))
}

func (r *commentsStatusRecorder) snapshot() (calls []string, auth, key string, body map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...), r.auth, r.key, r.body
}

// 请求体只有 closed 的布尔值，不带幂等键（端点没有 🔑），并带上令牌。
func TestClientSetTopicCommentsClosed(t *testing.T) {
	rec := &commentsStatusRecorder{}
	srv := newCommentsStatusServer(t, rec)
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	ctx := context.Background()

	res, err := c.setTopicCommentsClosed(ctx, "a1b2", true)
	if err != nil {
		t.Fatalf("关闭评论失败：%v", err)
	}
	if !res.Known || !res.Closed {
		t.Errorf("关闭评论的响应没有认出 comments_closed=true：%+v", res)
	}
	res, err = c.setTopicCommentsClosed(ctx, "a1b2", false)
	if err != nil {
		t.Fatalf("开启评论失败：%v", err)
	}
	if !res.Known || res.Closed {
		t.Errorf("开启评论的响应没有认出 comments_closed=false：%+v", res)
	}

	calls, auth, key, body := rec.snapshot()
	if len(calls) != 2 {
		t.Fatalf("请求次数 = %d，期望 2：%v", len(calls), calls)
	}
	for _, got := range calls {
		if got != "POST /api/v1/topics/a1b2/comments-status" {
			t.Errorf("请求 = %q，期望评论开关的 POST", got)
		}
	}
	if auth != "Bearer tok" {
		t.Errorf("鉴权头 = %q", auth)
	}
	if key != "" {
		t.Errorf("这个端点没有 🔑，不该带幂等键：%q", key)
	}
	if body["closed"] != false || len(body) != 1 {
		t.Errorf("最后一次请求体 = %v，期望只有 closed=false", body)
	}
}

// 响应里没有 comments_closed 时按「未知」处理：不能拿 false 当「已开启」。
func TestDecodeCommentsStatus(t *testing.T) {
	on := decodeCommentsStatus(json.RawMessage(`{"message":"评论已关闭","comments_closed":true,
		"comments_reopened_at":null,"next_close_available_at":"2026-11-20T00:00:00.000Z"}`))
	if !on.Known || !on.Closed {
		t.Errorf("comments_closed=true 没有解析出来：%+v", on)
	}
	off := decodeCommentsStatus(json.RawMessage(`{"comments_closed":false}`))
	if !off.Known || off.Closed {
		t.Errorf("comments_closed=false 解析不对：%+v", off)
	}
	none := decodeCommentsStatus(json.RawMessage(`{"message":"ok"}`))
	if none.Known {
		t.Errorf("没有 comments_closed 时应按未知处理：%+v", none)
	}
}

// commentsStatusApp 造一个已登录、正停在详情页的应用：当前账号就是这篇帖子的作者。
func commentsStatusApp(t *testing.T, srv *httptest.Server) *app {
	t.Helper()
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok",
		TokenType:   "Bearer",
		Username:    "jimmy",
		Scope:       "read profile write:topic",
	}); err != nil {
		t.Fatal(err)
	}
	a.client.Base = srv.URL
	a.pageName = "detail"
	a.detail = &detailState{shortID: "a1b2", topic: &topicDetail{
		ShortID: "a1b2", Title: "标题", Content: "正文",
		Author: "jimmy", AuthorID: "u1",
		URL: "https://2libra.com/post/work/a1b2",
	}}
	return a
}

// 竖排操作组多了一枚图标：它必须落在窗口内，与相邻按钮不重叠、排在同一列，
// 否则作者版的详情页右下角会互相盖住。
func TestCommentsStatusToolbarLayout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	a := commentsStatusApp(t, srv)
	const width, height = 1100, 760
	tt := ui.NewTester(a.view, width, height)
	tt.Frame()

	var prev ui.Rect
	for i, label := range []string{"编辑帖子", "追加附言", "关闭评论", "刷新", "关闭详情"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("工具条里找不到 %q：%q", label, tt.Texts())
		}
		if r.X < 0 || r.Y < 0 || r.X+r.W > width || r.Y+r.H > height {
			t.Errorf("%s 跑出了窗口：%+v", label, r)
		}
		if i > 0 {
			if r.Y < prev.Y+prev.H {
				t.Errorf("%s 与前一个按钮重叠：%+v / %+v", label, r, prev)
			}
			if r.X != prev.X {
				t.Errorf("%s 没有和前一个按钮排在同一列：%+v / %+v", label, r, prev)
			}
		}
		prev = r
	}
}

// 入口只给作者：别人的帖子、匿名帖都不该出现一个点了必然 403 的按钮。
func TestCommentsStatusEntryOnlyForAuthor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("不该发出任何请求：%s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	a := commentsStatusApp(t, srv)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if _, ok := tt.Find("关闭评论"); !ok {
		t.Fatalf("自己的帖子没有「关闭评论」入口：%q", tt.Texts())
	}

	a.detail.topic.Author = "alice"
	tt.Frame()
	if _, ok := tt.Find("关闭评论"); ok {
		t.Error("别人的帖子不该给「关闭评论」入口")
	}

	a.detail.topic.Author = "匿名"
	tt.Frame()
	if _, ok := tt.Find("关闭评论"); ok {
		t.Error("匿名帖确认不了作者，不该给入口")
	}

	// 绕过按钮直接调入口同样被拦住，一次请求都不发。
	a.detail.topic.Author = "alice"
	a.askToggleComments(a.detail)
	if a.detail.comments.awaiting {
		t.Error("非作者点关闭评论不该展开确认框")
	}
	a.toggleTopicComments(a.detail)
	if a.detail.comments.busy {
		t.Error("非作者不该发出开关请求")
	}
}

// 作者关掉评论：点图标先出确认框（不发请求），确认后状态落地、副标题与输入框
// 跟着变、结果用 toast 报告；再点一次就把评论重新打开。
func TestCommentsStatusTogglesThroughConfirm(t *testing.T) {
	rec := &commentsStatusRecorder{}
	srv := newCommentsStatusServer(t, rec)
	defer srv.Close()
	a := commentsStatusApp(t, srv)

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("关闭评论"); err != nil {
		t.Fatalf("点不到「关闭评论」：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !a.detail.comments.awaiting {
		t.Fatal("点「关闭评论」应展开确认框")
	}
	for _, want := range []string{"关闭这篇帖子的评论？", "确认关闭评论"} {
		if !tt.HasText(want) {
			t.Errorf("确认框缺少 %q：%q", want, tt.Texts())
		}
	}
	if calls, _, _, _ := rec.snapshot(); len(calls) != 0 {
		t.Fatalf("只展开确认框不该发请求：%v", calls)
	}

	if err := tt.Click("确认关闭评论"); err != nil {
		t.Fatalf("点不到确认按钮：%v（%q）", err, tt.Texts())
	}
	waitFor(t, func() bool { return !a.detail.comments.busy && a.detail.topic.CommentsClosed },
		"关闭评论请求没有完成")
	// 结果提示：可能在队列里，也可能已经被点击那一帧领走画在窗口上。
	tt.Frame()
	if !tt.HasText("评论已关闭，其他读者不能再回复") {
		t.Errorf("关闭评论没有 toast：%q", tt.Texts())
	}
	calls, _, key, body := rec.snapshot()
	if len(calls) != 1 || calls[0] != "POST /v1/topics/a1b2/comments-status" {
		t.Fatalf("关闭评论请求 = %v", calls)
	}
	if body["closed"] != true {
		t.Errorf("关闭评论的请求体 = %v，期望 closed=true", body)
	}
	if key != "" {
		t.Errorf("评论开关不该带幂等键：%q", key)
	}

	// 页面跟着变：副标题与评论框都说明评论已关闭，入口换成「开启评论」。
	tt.Frame()
	if !tt.HasText("评论已关闭") {
		t.Errorf("关闭后页面没有「评论已关闭」：%q", tt.Texts())
	}
	if _, ok := tt.Find("开启评论"); !ok {
		t.Fatalf("关闭后入口应变成「开启评论」：%q", tt.Texts())
	}

	if err := tt.Click("开启评论"); err != nil {
		t.Fatalf("点不到「开启评论」：%v", err)
	}
	tt.Frame()
	if !tt.HasText("重新开启评论？") {
		t.Errorf("重新开启也要先确认：%q", tt.Texts())
	}
	if err := tt.Click("确认开启评论"); err != nil {
		t.Fatalf("点不到开启的确认按钮：%v", err)
	}
	waitFor(t, func() bool { return !a.detail.comments.busy && !a.detail.topic.CommentsClosed },
		"开启评论请求没有完成")
	tt.Frame()
	if !tt.HasText("评论已开启，其他读者可以继续回复") {
		t.Errorf("开启评论没有 toast：%q", tt.Texts())
	}
	tt.Frame()
	if _, ok := tt.Find("关闭评论"); !ok {
		t.Errorf("开启后入口应回到「关闭评论」：%q", tt.Texts())
	}
}

// 缺 write:topic 时走全局授权弹窗，授权成功后重跑同一次开关。
func TestCommentsStatusMissingScopeGoesThroughAuthGate(t *testing.T) {
	rec := &commentsStatusRecorder{
		status: http.StatusForbidden,
		errEnv: `{"c":403,"code":"insufficient_scope","m":"缺少权限范围：write:topic",` +
			`"d":null,"required_scope":["write:topic"],"t":0}`,
	}
	srv := newCommentsStatusServer(t, rec)
	defer srv.Close()
	a := commentsStatusApp(t, srv)
	// 令牌里没有 write:topic：缺的是用户的授权，应当去授权。
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy", Scope: "read profile"}); err != nil {
		t.Fatal(err)
	}

	a.toggleTopicComments(a.detail)
	waitFor(t, func() bool { return !a.detail.comments.busy }, "开关请求没有结束")
	if !a.authGate.open {
		t.Fatal("缺少 write:topic 时应打开授权弹窗")
	}
	if len(a.authGate.need.Scopes) != 1 || a.authGate.need.Scopes[0] != "write:topic" {
		t.Errorf("授权弹窗缺少的 scope = %v", a.authGate.need.Scopes)
	}
	if a.detail.topic.CommentsClosed {
		t.Error("失败不该改页面上的评论状态")
	}
}

// 「需开通」：端点属于高级能力时服务端也回 403 insufficient_scope，但用户其实
// 已经有 write:topic。这时不弹授权框，直接说明是应用没被开通。
func TestCommentsStatusNotGrantedExplainsWithoutAuthGate(t *testing.T) {
	rec := &commentsStatusRecorder{
		status: http.StatusForbidden,
		errEnv: `{"c":403,"code":"insufficient_scope","m":"缺少权限范围：write:topic",` +
			`"d":null,"required_scope":["write:topic"],"t":0}`,
	}
	srv := newCommentsStatusServer(t, rec)
	defer srv.Close()
	a := commentsStatusApp(t, srv) // 令牌里已经有 write:topic

	a.toggleTopicComments(a.detail)
	waitFor(t, func() bool { return !a.detail.comments.busy }, "开关请求没有结束")
	if a.authGate.open {
		t.Error("已有 write:topic 时不该再弹授权框：重新授权解决不了「需开通」")
	}
	if !hasToastContaining(a, "还没有给本应用开通") {
		t.Errorf("没有说明这个能力需要开通：%+v", a.toasts)
	}
	if a.detail.topic.CommentsClosed {
		t.Error("失败不该改页面上的评论状态")
	}
}
