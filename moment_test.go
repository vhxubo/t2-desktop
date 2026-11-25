package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 动态端点的契约：POST /v1/topics/moments，body 只有 content，必须带幂等键。
func TestCreateMomentRequest(t *testing.T) {
	type request struct {
		path, key, auth, body string
	}
	reqCh := make(chan request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		reqCh <- request{
			path: r.URL.Path, key: r.Header.Get("Idempotency-Key"),
			auth: r.Header.Get("Authorization"), body: string(raw),
		}
		fmt.Fprint(w, envelopeJSON(`{"short_id":"M1","type":"moment"}`))
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	created, err := c.createMoment(context.Background(), "key-1", "你好")
	if err != nil {
		t.Fatalf("发动态失败：%v", err)
	}
	if created.ShortID != "M1" || created.URL != "https://2libra.com/post/M1" {
		t.Errorf("创建结果 = %+v", created)
	}

	req := <-reqCh
	if req.path != "/api/v1/topics/moments" || req.key != "key-1" || req.auth != "Bearer tok" {
		t.Errorf("请求 = %+v", req)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.body), &body); err != nil {
		t.Fatal(err)
	}
	if body["content"] != "你好" || len(body) != 1 {
		t.Errorf("请求体 = %s", req.body)
	}
}

// 提交成功后：草稿清空、幂等键清掉，并留下创建结果。
func TestSubmitMoment(t *testing.T) {
	reqCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		reqCh <- string(raw) + "|" + r.Header.Get("Idempotency-Key")
		fmt.Fprint(w, envelopeJSON(`{"short_id":"M1"}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.compose = newComposeState()
	a.compose.mode = composeModeMoment
	a.compose.momentDraft = "你好"
	done := make(chan createdTopic, 1)
	a.onPostPublished = func(created createdTopic, err error) {
		if err != nil {
			t.Errorf("发动态失败：%v", err)
		}
		done <- created
	}
	a.submitMoment()
	select {
	case created := <-done:
		if created.ShortID != "M1" {
			t.Errorf("创建结果 = %+v", created)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("发动态没有完成")
	}
	if a.compose.momentDraft != "" || a.compose.momentKey != "" || a.compose.momentBusy {
		t.Errorf("成功后状态没收拾：%+v", a.compose)
	}

	req := <-reqCh
	if !strings.HasPrefix(req, `{"content":"你好"}|`) {
		t.Errorf("请求体 / 键 = %q", req)
	}
	key := strings.TrimPrefix(req, `{"content":"你好"}|`)
	if !keyPattern.MatchString(key) {
		t.Errorf("幂等键格式不对：%q", key)
	}
}

// 空内容与超长内容都不发请求，原因留在表单里。
func TestMomentValidation(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv)
	a.compose = newComposeState()
	a.compose.mode = composeModeMoment
	a.submitMoment()
	if !strings.Contains(a.compose.momentErr, "不能为空") {
		t.Errorf("空内容的提示 = %q", a.compose.momentErr)
	}

	a.compose.momentDraft = strings.Repeat("字", composeMomentMax+1)
	a.submitMoment()
	if !strings.Contains(a.compose.momentErr, "不能超过") {
		t.Errorf("超长内容的提示 = %q", a.compose.momentErr)
	}
	if a.compose.momentBusy {
		t.Error("校验失败不该进入提交状态")
	}
}

// 动态缺 write:topic：补授权要真的把 write:topic 带进授权地址，不能只申请
// 默认范围。发动态与发帖共用这个 scope，但动态的入口没有节点 / 标题可以提示
// 用户，更容易在授权这一步漏掉。
func TestMomentMissingScopeAsksForWriteTopic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"c":403,"code":"insufficient_scope",`+
			`"m":"缺少权限范围","required_scope":["write:topic"],"d":null}`)
	}))
	defer srv.Close()

	b := testBook(t)
	auth := addTestAccount(t, b, "jimmy")
	a := newAppWithDeps(b.deps(), auth)
	a.autoLoad = false
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.compose = newComposeState()
	a.compose.mode = composeModeMoment
	a.compose.momentDraft = "你好"

	a.submitMoment()
	waitFor(t, func() bool { return a.authGate.open }, "动态缺权限的授权弹窗")
	if got := a.authGate.need.Scopes; len(got) != 1 || got[0] != "write:topic" {
		t.Fatalf("授权弹窗登记的 scope = %v，期望 write:topic", got)
	}

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	a.authorizeGate()
	t.Cleanup(a.auth.Cancel)
	urls := tt.OpenedURLs()
	if len(urls) != 1 {
		t.Fatalf("没有打开授权页：%v", urls)
	}
	u, err := url.Parse(urls[0])
	if err != nil {
		t.Fatal(err)
	}
	if scope := " " + u.Query().Get("scope") + " "; !strings.Contains(scope, " write:topic ") {
		t.Errorf("授权地址没有申请 write:topic：%q", scope)
	}
}

// 服务端只回 insufficient_scope、没带 required_scope 时，客户端也必须知道
// 动态要的是 write:topic：否则授权窗口只申请默认范围，登录回来仍然发不出去。
func TestMomentMissingScopeWithoutRequiredScopeStillAsksWriteTopic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"c":403,"code":"insufficient_scope",`+
			`"m":"缺少权限范围","d":null}`)
	}))
	defer srv.Close()

	b := testBook(t)
	auth := addTestAccount(t, b, "jimmy")
	a := newAppWithDeps(b.deps(), auth)
	a.autoLoad = false
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.compose = newComposeState()
	a.compose.mode = composeModeMoment
	a.compose.momentDraft = "你好"

	a.submitMoment()
	waitFor(t, func() bool { return a.authGate.open }, "动态缺权限的授权弹窗")
	if got := a.authGate.need.Scopes; len(got) != 1 || got[0] != "write:topic" {
		t.Fatalf("授权弹窗登记的 scope = %v，期望 write:topic", got)
	}

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	a.authorizeGate()
	t.Cleanup(a.auth.Cancel)
	urls := tt.OpenedURLs()
	if len(urls) != 1 {
		t.Fatalf("没有打开授权页：%v", urls)
	}
	u, err := url.Parse(urls[0])
	if err != nil {
		t.Fatal(err)
	}
	if scope := " " + u.Query().Get("scope") + " "; !strings.Contains(scope, " write:topic ") {
		t.Errorf("授权地址没有申请 write:topic：%q", scope)
	}
}

// 未登录时打开动态窗口：登录授权一次就要把 write:topic 一起申请，登录回来
// 不用再补第二次授权。
func TestMomentLoginAsksWriteTopicUpFront(t *testing.T) {
	a := testApp(t)
	a.autoLoad = false

	a.compose = newComposeState()
	a.compose.mode = composeModeMoment
	a.showComposeWindow()
	if !a.authGate.open {
		t.Fatal("未登录打开动态窗口没有弹授权窗")
	}
	if got := a.authGate.need.Scopes; len(got) != 1 || got[0] != "write:topic" {
		t.Fatalf("登录前登记的 scope = %v，期望 write:topic", got)
	}
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	a.authorizeGate()
	t.Cleanup(a.auth.Cancel)
	urls := tt.OpenedURLs()
	if len(urls) != 1 {
		t.Fatalf("没有打开授权页：%v", urls)
	}
	u, err := url.Parse(urls[0])
	if err != nil {
		t.Fatal(err)
	}
	if scope := " " + u.Query().Get("scope") + " "; !strings.Contains(scope, " write:topic ") {
		t.Errorf("登录授权地址没有申请 write:topic：%q", scope)
	}
}

// 动态模式只画短正文，不画标题 / 节点 / 附加组件；底部按钮叫「发布动态」。
func TestMomentModeUI(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv)
	a.compose = newComposeState()
	a.compose.mode = composeModeMoment
	a.compose.momentDraft = "你好"

	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, composeWindowHeight)
	tt.Frame()
	for _, want := range []string{
		"发动态", "写一条动态", "动态内容", "2 / 500 字", "发布动态",
		"Ctrl+Enter 发布动态",
	} {
		if !tt.HasText(want) {
			t.Errorf("动态模式缺少 %q：%q", want, tt.Texts())
		}
	}
	for _, gone := range []string{"标题", "发布设置", "节点", "附加组件"} {
		if _, ok := tt.Find(gone); ok {
			t.Errorf("动态模式不该出现 %q：%q", gone, tt.Texts())
		}
	}
}

// 主页有「发动态」FAB；点它走 showMomentCompose，打开同一扇窗口的动态模式。
// 点击命中由 FAB 的既有用例覆盖（发帖 / 刷新），这里只锁入口与动作。
func TestMomentFabOpensMomentMode(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv)
	a.tabs[0].items = []Item{{Key: "k1", Title: "一条列表"}}
	a.tabs[0].loaded = true
	a.tabs[0].total = 1

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if _, ok := tt.Find("发动态"); !ok {
		t.Fatalf("主页没有「发动态」FAB：%q", tt.Texts())
	}
	a.showMomentCompose()
	if a.compose == nil || a.compose.mode != composeModeMoment {
		t.Errorf("发动态没有切到动态模式：%+v", a.compose)
	}
}

// 帖子与动态的草稿分开存：来回切模式不会互相覆盖。
func TestComposeModeKeepsSeparateDrafts(t *testing.T) {
	st := newComposeState()
	st.title = "帖子标题"
	st.body = "帖子正文"
	st.mode = composeModeMoment
	st.momentDraft = "动态正文"
	st.mode = composeModePost
	if st.title != "帖子标题" || st.body != "帖子正文" {
		t.Errorf("切回帖子后草稿丢了：%+v", st)
	}
	st.mode = composeModeMoment
	if st.momentDraft != "动态正文" {
		t.Errorf("切回动态后草稿丢了：%q", st.momentDraft)
	}
}
