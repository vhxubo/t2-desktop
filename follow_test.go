package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 关注端点的参数是用户 uuid，不是 username：路径必须严格照文档。
func TestFollowPathsMatchContract(t *testing.T) {
	if got := userPath("jimmy"); got != "/v1/users/jimmy" {
		t.Errorf("用户主页路径 = %s", got)
	}
	if got := userPath("a/b?c=d"); got != "/v1/users/a%2Fb%3Fc=d" {
		t.Errorf("username 未转义：%s", got)
	}
	if got := userFollowPath("9f2f0b1a-8a3a"); got != "/v1/users/9f2f0b1a-8a3a/follow" {
		t.Errorf("关注路径 = %s", got)
	}
}

// POST / DELETE /v1/users/:userId/follow 都走既有写链路。
func TestClientFollowEndpoints(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.RequestURI()+" auth="+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/users/u1/follow" {
			t.Errorf("意外路径 %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	ctx := context.Background()

	if err := c.setFollow(ctx, "u1", true); err != nil {
		t.Fatalf("关注失败：%v", err)
	}
	if err := c.setFollow(ctx, "u1", false); err != nil {
		t.Fatalf("取关失败：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"POST /api/v1/users/u1/follow auth=Bearer tok",
		"DELETE /api/v1/users/u1/follow auth=Bearer tok",
	}
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Errorf("第 %d 个请求 = %q，期望 %q（实际 %v）", i, at(got, i), w, got)
		}
	}
}

// 重复关注 / 重复取关的 409 视为成功：目标状态已经成立。
func TestSetFollowConflictIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"c":409,"code":"conflict","m":"已关注","d":null}`)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	if err := c.setFollow(context.Background(), "u1", true); err != nil {
		t.Errorf("409 conflict 应按成功处理：%v", err)
	}
}

// userCardFollowTestApp 造一个已登录、资料已读到的资料卡。关注按钮的目标
// 用户 uuid 已经由资料响应填好，点一下就能直接发请求。
func userCardFollowTestApp(t *testing.T) *app {
	t.Helper()
	a := testApp(t)
	m := newAuthManager(t.TempDir())
	if err := m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "reader"}); err != nil {
		t.Fatal(err)
	}
	a.auth = m
	a.client = &Client{Auth: m}
	a.userCard = newUserCardState("jimmy")
	a.userCard.profileLoaded = true
	a.userCard.user = accountProfile{ID: "u1", Name: "jimmy"}
	a.userCard.follow.userID = "u1"
	return a
}

func waitAuthGate(t *testing.T, a *app) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !a.authGate.open {
		if time.Now().After(deadline) {
			t.Fatal("关注失败没有打开授权弹窗")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 关注接口缺 interact scope 时交给全局授权弹窗，并把这次关注登记为授权后重跑。
func TestUserCardFollowScopeErrorOpensAuthorization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","m":"缺少权限范围：interact",`+
			`"required_scope":["interact"],"d":null}`)
	}))
	defer srv.Close()

	a := userCardFollowTestApp(t)
	a.client.HTTP = srv.Client()
	a.client.Base = srv.URL + "/api"

	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if err := tt.Click("关注他"); err != nil {
		t.Fatalf("点击关注失败：%v", err)
	}
	waitAuthGate(t, a)
	if len(a.authGate.need.Scopes) != 1 || a.authGate.need.Scopes[0] != "interact" {
		t.Errorf("授权弹窗缺少 interact scope：%v", a.authGate.need.Scopes)
	}
	if len(a.authGate.replays) != 1 {
		t.Errorf("关注操作应登记为授权后重跑：%d", len(a.authGate.replays))
	}
}

// 未登录点击资料卡上的「关注他」进入全局授权弹窗，并把这次关注登记为授权
// 成功后重跑。
func TestUserCardFollowAnonymousRequestsLogin(t *testing.T) {
	a := testApp(t)
	a.userCard = newUserCardState("jimmy")
	a.userCard.follow.userID = "u1"

	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if err := tt.Click("关注他"); err != nil {
		t.Fatalf("点击关注失败：%v", err)
	}
	if !a.authGate.open {
		t.Fatal("未登录点击关注应打开授权弹窗")
	}
	if !strings.Contains(a.authGate.need.Message, "关注这位用户") {
		t.Errorf("授权弹窗说明不对：%q", a.authGate.need.Message)
	}
	if len(a.authGate.replays) != 1 {
		t.Errorf("关注操作应登记为授权后重跑：%d", len(a.authGate.replays))
	}
}
