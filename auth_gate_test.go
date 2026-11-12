package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 服务端只声明缺少哪个 scope 时，全局授权弹窗必须把它翻译成可授权的需求：
// 401 是重新登录，403 insufficient_scope 是补权限，其它 403 不进入授权流程。
func TestAuthNeedFromError(t *testing.T) {
	need, ok := authNeedFromError(&apiError{Status: http.StatusUnauthorized, Code: "unauthorized"})
	if !ok || len(need.Scopes) != 0 || need.Message == "" {
		t.Errorf("401 应转成需要登录：%+v %v", need, ok)
	}

	need, ok = authNeedFromError(&apiError{
		Status: http.StatusForbidden, Code: "insufficient_scope",
		RequiredScope: []string{" write:vote ", "read", "write:vote"},
	})
	if !ok || len(need.Scopes) != 2 || need.Scopes[0] != "write:vote" || need.Scopes[1] != "read" {
		t.Errorf("403 缺 scope 应去重并保序：%+v %v", need, ok)
	}
	if !strings.Contains(need.Message, "write:vote") {
		t.Errorf("说明里应写明缺失的 scope：%q", need.Message)
	}

	// 刷新令牌被服务端明确拒绝（fatal）同样需要重新授权；临时故障不算。
	if _, ok := authNeedFromError(&tokenHTTPError{status: 400, msg: "refresh_token 已失效", fatal: true}); !ok {
		t.Error("fatal 的刷新失败应进入授权流程")
	}
	if _, ok := authNeedFromError(&tokenHTTPError{status: 503, msg: "服务端错误"}); ok {
		t.Error("临时性的刷新失败不该弹授权窗")
	}

	// 用户级限制重新授权也解决不了，不能弹授权窗。
	for _, err := range []error{
		&apiError{Status: http.StatusForbidden, Code: "permission_denied", Message: "禁止"},
		&apiError{Status: http.StatusInternalServerError, Code: "internal_error"},
		&apiError{Status: http.StatusForbidden},
		fmt.Errorf("普通错误"),
	} {
		if _, ok := authNeedFromError(err); ok {
			t.Errorf("%v 不该进入授权流程", err)
		}
	}
}

// 授权 URL 要在默认范围之外带上服务端要求的 scope，且不能重复。
func TestBeginScopesAsksForExtraScopes(t *testing.T) {
	m := newAuthManager(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	raw, err := m.BeginScopes(ctx, []string{"write:vote", "read", "write:vote", ""})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if got, want := q.Get("scope"), defaultScopes+" write:vote"; got != want {
		t.Errorf("scope = %q，期望 %q", got, want)
	}
	if n := strings.Count(q.Get("scope"), "write:vote"); n != 1 {
		t.Errorf("额外 scope 应只出现一次，实际 %d 次：%q", n, q.Get("scope"))
	}
	if q.Get("client_id") != clientID || q.Get("redirect_uri") != defaultRedirectURI ||
		q.Get("code_challenge_method") != "S256" {
		t.Errorf("授权参数不完整：%v", q)
	}
	m.Cancel()
}

// 弹窗里点「前往授权」：请求的 scope 带上缺失项，待重跑调用保留到回调。
func TestAuthPromptOpensBrowserWithMissingScope(t *testing.T) {
	a := testApp(t)
	a.handleAuthError(&apiError{
		Status: http.StatusForbidden, Code: "insufficient_scope",
		RequiredScope: []string{"write:vote"},
	}, func() {})

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"需要授权", "缺少的权限范围：write:vote", "前往授权", "取消"} {
		if !tt.HasText(want) {
			t.Errorf("授权弹窗缺少 %q：%q", want, tt.Texts())
		}
	}
	t.Cleanup(a.auth.Cancel)
	if err := tt.Click("前往授权"); err != nil {
		t.Fatalf("点击前往授权失败：%v", err)
	}
	if a.authGate.open {
		t.Error("点「前往授权」后弹窗应关闭")
	}
	if !a.hasPendingAuthorization() {
		t.Error("等待回调期间不应丢掉待重跑的调用")
	}
	if !a.loginPending {
		t.Error("应进入等待浏览器授权状态")
	}
	urls := tt.OpenedURLs()
	if len(urls) != 1 || !strings.Contains(urls[0], "/oauth/authorize?") {
		t.Fatalf("没有打开授权页：%v", urls)
	}
	u, err := url.Parse(urls[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(" "+u.Query().Get("scope")+" ", " write:vote ") {
		t.Errorf("授权 URL 没有申请缺失的 scope：%q", u.Query().Get("scope"))
	}
}

// 取消授权会丢弃待重跑的调用：它们不能在某次无关登录成功后被突然执行。
func TestAuthPromptCancelDropsPendingCall(t *testing.T) {
	a := testApp(t)
	replayed := 0
	a.demandAuthorization(scopeNeed([]string{"write:vote"}), func() { replayed++ })

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("取消"); err != nil {
		t.Fatalf("点击取消失败：%v", err)
	}
	if a.authGate.open || a.hasPendingAuthorization() {
		t.Errorf("取消后授权状态应清空：%+v", a.authGate)
	}
	if a.loginMsg != "已取消授权" {
		t.Errorf("取消后提示不对：%q", a.loginMsg)
	}

	// 之后的登录成功不能再触发已取消的调用。
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.onAuthChanged(nil)
	if replayed != 0 {
		t.Errorf("已取消的调用被执行了 %d 次", replayed)
	}
}

// 完整链路：403 缺权限 → 弹窗 → 打开浏览器 → 回调 → 自动重发原请求。
func TestMissingScopeRetriesOriginalCallAfterCallback(t *testing.T) {
	var favoritesCalls atomic.Int32
	var sawNewToken atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			_, _ = fmt.Fprint(w, `{"c":0,"m":"请求成功","d":{"access_token":"NEW","refresh_token":"R","token_type":"Bearer","expires_in":3600,"scope":"read profile read:me write:comment write:vote"}}`)
		case "/api/oauth/me", "/api/v1/me/summary":
			_, _ = fmt.Fprint(w, `{"c":0,"d":{"username":"jimmy"}}`)
		case "/api/v1/me/favorites":
			favoritesCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer NEW" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","m":"缺少权限范围","required_scope":["write:vote"],"d":null}`)
				return
			}
			sawNewToken.Store(true)
			_, _ = fmt.Fprint(w, envelopeJSON(`{"posts":[{"id":"f1","short_id":"REPLAY1","title":"授权后重新加载的内容","author":{"username":"jimmy"}}],"total":1,"page":1,"limit":50,"total_pages":1}`))
		default:
			t.Errorf("意外路径 %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := newApp(t.TempDir())
	a.autoLoad = false
	a.auth.apiBase = srv.URL + "/api"
	a.auth.http = srv.Client()
	a.client = &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: a.auth}
	// 已登录但令牌缺少 write:vote：请求真的发出去，由服务端回 403。
	if err := a.auth.storeToken(tokenData{AccessToken: "OLD", RefreshToken: "R", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}

	idx := -1
	for i, s := range tabSpecs {
		if s.Label == "收藏" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("找不到收藏 tab")
	}
	a.tab = idx
	a.navSel = tabID(idx)

	prompted := make(chan struct{}, 1)
	a.onAuthPrompt = func() {
		select {
		case prompted <- struct{}{}:
		default:
		}
	}
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	a.load(idx, 1)
	select {
	case <-prompted:
	case <-time.After(5 * time.Second):
		t.Fatal("缺权限的请求没有打开全局授权弹窗")
	}
	if !a.authGate.open {
		t.Fatal("授权弹窗没有打开")
	}
	tt.Frame()
	if !tt.HasText("缺少的权限范围：write:vote") {
		t.Errorf("弹窗没有写出缺失的 scope：%q", tt.Texts())
	}
	if err := tt.Click("前往授权"); err != nil {
		t.Fatalf("点击前往授权失败：%v", err)
	}
	urls := tt.OpenedURLs()
	if len(urls) != 1 {
		t.Fatalf("没有打开授权页：%v", urls)
	}
	u, err := url.Parse(urls[0])
	if err != nil {
		t.Fatal(err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatal("授权 URL 没有 state")
	}

	if err := a.auth.Complete(defaultRedirectURI + "?code=c&state=" + state); err != nil {
		t.Fatalf("回调没有被接受：%v", err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for !tt.HasText("授权后重新加载的内容") {
		if time.Now().After(deadline) {
			t.Fatalf("回调后没有自动重发原请求：%q", tt.Texts())
		}
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if !a.auth.loggedIn() || !sawNewToken.Load() {
		t.Errorf("重发没有使用新令牌：loggedIn=%v", a.auth.loggedIn())
	}
	if a.authGate.open || a.hasPendingAuthorization() {
		t.Errorf("重跑完成后授权状态应清空：%+v", a.authGate)
	}
	if got := favoritesCalls.Load(); got != 2 {
		t.Errorf("收藏请求次数 = %d，期望首次 403 + 重跑 1 次", got)
	}
}

// 私有 tab 在未登录时被点开：弹窗负责去登录，登录成功后自动加载这一页。
func TestPrivateTabClickAsksForLogin(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	if err := tt.Click("收藏"); err != nil {
		t.Fatal(err)
	}
	if !a.authGate.open {
		t.Fatal("点私有 tab 没有打开全局授权弹窗")
	}
	tt.Frame()
	for _, want := range []string{"需要登录", "「收藏」需要登录 2Libra 后才能查看。", "前往授权", "取消"} {
		if !tt.HasText(want) {
			t.Errorf("登录弹窗缺少 %q：%q", want, tt.Texts())
		}
	}
}
