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

	"github.com/egoist/mygo"
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

// 弹窗里点「前往授权」：请求的 scope 带上缺失项，授权状态保留到回调。
func TestAuthPromptOpensBrowserWithMissingScope(t *testing.T) {
	a := testApp(t)
	a.handleAuthError(&apiError{
		Status: http.StatusForbidden, Code: "insufficient_scope",
		RequiredScope: []string{"write:vote"},
	})

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
		t.Error("等待回调期间不应丢掉授权状态")
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

// 缺权限的窗口自己弹授权窗：发帖窗口缺 write:topic 时，弹窗只出现在发帖
// 窗口里，主窗口与别的附属窗口不跟着闪一个。
func TestAuthPromptStaysInOriginatingWindow(t *testing.T) {
	a := testApp(t)
	a.demandAuthorizationFor(toastCompose, scopeNeed([]string{"write:topic"}))

	if !a.authGate.wants(toastCompose) {
		t.Errorf("发帖窗口应显示授权弹窗：%+v", a.authGate)
	}
	if a.authGate.wants(toastMain) {
		t.Error("主窗口不该显示发帖窗口缺权限的弹窗")
	}

	ct := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	ct.Frame()
	for _, want := range []string{"需要授权", "缺少的权限范围：write:topic", "前往授权", "取消"} {
		if !ct.HasText(want) {
			t.Errorf("发帖窗口里的授权弹窗缺少 %q：%q", want, ct.Texts())
		}
	}

	mt := ui.NewTester(a.view, 1100, 760)
	mt.Frame()
	if mt.HasText("需要授权") || mt.HasText("缺少的权限范围：write:topic") {
		t.Errorf("主窗口不该跟着弹发帖窗口的授权窗：%q", mt.Texts())
	}

	// 在这个窗口里取消，清掉的是同一份授权状态。
	if err := ct.Click("取消"); err != nil {
		t.Fatalf("在发帖窗口里取消失败：%v", err)
	}
	if a.authGate.open || a.hasPendingAuthorization() {
		t.Errorf("取消后授权状态应清空：%+v", a.authGate)
	}
}

func TestAuthPromptMovesWithoutLosingScopes(t *testing.T) {
	a := testApp(t)
	a.userCard = newUserCardState("amy")
	a.demandAuthorizationFor(toastCompose, scopeNeed([]string{"write:topic"}))
	a.demandAuthorizationFor(toastUserCard, scopeNeed([]string{"interact"}))

	for _, owner := range []toastTarget{toastCompose, toastUserCard} {
		if !a.authGate.wants(owner) {
			t.Errorf("应保留窗口 %v 的授权请求", owner)
		}
	}
	if a.authGate.wants(toastMain) {
		t.Error("主窗口不该显示别的窗口缺权限的弹窗")
	}
	for i, scope := range []string{"write:topic", "interact"} {
		if a.authGate.need.Scopes[i] != scope {
			t.Errorf("合并后的 scope = %v，第 %d 项应为 %q", a.authGate.need.Scopes, i, scope)
		}
	}
	if a.authGate.need.Message == "" {
		t.Error("弹窗应保留一条说明")
	}

	ct := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	ct.Frame()
	if ct.HasText("前往授权") {
		t.Errorf("发帖窗口仍显示已经移走的弹窗：%q", ct.Texts())
	}
	ut := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	ut.Frame()
	if !ut.HasText("缺少的权限范围：write:topic、interact") {
		t.Errorf("资料卡窗口没有列出合并后的 scope：%q", ut.Texts())
	}
	mt := ui.NewTester(a.view, 1100, 760)
	mt.Frame()
	if mt.HasText("需要授权") {
		t.Errorf("主窗口不该跟着弹：%q", mt.Texts())
	}
}

func TestAuthPromptHostsInSecondaryWindows(t *testing.T) {
	for _, target := range []toastTarget{toastViewer, toastNetLog, toastAbout, toastDetailBase} {
		a := testApp(t)
		window := &mygo.Window{}
		var view func(*ui.Context)
		switch target {
		case toastViewer:
			a.viewerWin, view = window, a.imageViewerWindowView
		case toastNetLog:
			a.netLogWin, view = window, a.netLogWindowView
		case toastAbout:
			a.aboutWin, view = window, a.aboutWindowView
		case toastDetailBase:
			a.detailWindows = []*detailWindow{{win: window, toast: target}}
		}
		a.win, a.mainClosed = window, true
		if got, ok := a.authTargetForWindow(window); !ok || got != target {
			t.Fatalf("主窗关闭后宿主 = %v, %v，预期 %v", got, ok, target)
		}
		a.win = nil
		if view == nil {
			continue
		}
		a.requestLoginFor(target, "继续操作需要授权")
		tester := ui.NewTester(view, 900, 680)
		tester.Frame()
		if !tester.HasText("前往授权") {
			t.Errorf("窗口 %v 没有显示授权按钮：%v", target, tester.Texts())
		}
		if err := tester.Click("取消"); err != nil || a.hasPendingAuthorization() {
			t.Errorf("窗口 %v 取消授权失败：%v", target, err)
		}
	}
}

// 关掉某个窗口只摘掉它自己那一份授权提示；别的窗口缺权限的那一份继续留着。
// 最后一个发起窗口也关掉时整次授权作废。
func TestAuthGateDropsCallsOfClosedWindow(t *testing.T) {
	a := testApp(t)
	a.demandAuthorizationFor(toastCompose, scopeNeed([]string{"write:topic"}))
	a.demandAuthorizationFor(toastUserCard, scopeNeed([]string{"interact"}))

	a.authGateWindowClosed(toastCompose)
	if a.authGate.wants(toastCompose) {
		t.Error("关掉的窗口不该继续显示授权弹窗")
	}
	if !a.authGate.wants(toastUserCard) {
		t.Error("还开着的窗口应继续显示授权弹窗")
	}

	// 另一条也关掉：授权状态整体清空，不会残留一个没人能看到的弹窗。
	a.authGateWindowClosed(toastUserCard)
	if a.authGate.open || a.hasPendingAuthorization() {
		t.Errorf("最后一个发起窗口关掉后应清空授权状态：%+v", a.authGate)
	}
}

// 补权限直接走系统浏览器：授权地址仍申请缺失的 scope，且不带 webview 专用
// 的 redirect_mode 参数。
func TestScopeReauthUsesSystemBrowser(t *testing.T) {
	a := testApp(t)
	a.demandAuthorization(scopeNeed([]string{"write:vote"}))
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	a.authorizeGate()

	urls := tt.OpenedURLs()
	if len(urls) != 1 {
		t.Fatalf("没有打开系统浏览器授权页：%v", urls)
	}
	systemURL := urls[0]
	if strings.Contains(systemURL, "redirect_mode=") {
		t.Errorf("系统浏览器的授权地址不该带 redirect_mode：%s", systemURL)
	}
	u, err := url.Parse(systemURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(" "+u.Query().Get("scope")+" ", " write:vote ") {
		t.Errorf("授权 URL 没有申请缺失的 scope：%q", u.Query().Get("scope"))
	}
	if !a.loginPending {
		t.Error("授权应进入等待回调状态")
	}

	// 取消这次补授权：窗口里的授权状态一并清掉。
	a.cancelLogin()
	if a.hasPendingAuthorization() {
		t.Error("取消后仍留着授权状态")
	}
	if a.loginPending {
		t.Error("取消后不该还在等回调")
	}
	if !strings.Contains(a.loginMsg, "已取消授权") {
		t.Errorf("取消后窗口提示不对：%q", a.loginMsg)
	}
}

// 系统浏览器里补权限成功后，窗口换成新会话并清掉授权状态，但应用不替用户
// 重跑被中断的操作。
func TestScopeReauthCompletesAfterSystemLogin(t *testing.T) {
	srv := newTestTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"NEW","refresh_token":"R","token_type":"Bearer","expires_in":3600}}`)
	})
	a := newApp(t.TempDir())
	a.autoLoad = false
	a.auth.apiBase = srv.URL + "/api"
	a.auth.http = srv.Client()
	a.client = &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: a.auth}

	a.demandAuthorization(scopeNeed([]string{"write:vote"}))
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	a.authorizeGate()

	urls := tt.OpenedURLs()
	if len(urls) != 1 {
		t.Fatalf("没有打开系统浏览器授权页：%v", urls)
	}
	u, err := url.Parse(urls[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := a.auth.Complete(defaultRedirectURI + "?code=c&state=" + u.Query().Get("state")); err != nil {
		t.Fatalf("回调没有被接受：%v", err)
	}
	waitFor(t, func() bool { return a.auth.accessToken() == "NEW" }, "授权成功后窗口没有换成新会话")
	waitFor(t, func() bool { return !a.hasPendingAuthorization() }, "授权完成后没有清掉授权状态")
}

// 取消授权会清掉授权状态：之后某次无关的登录成功不该再弹出过期提示。
func TestAuthPromptCancelDropsPendingCall(t *testing.T) {
	a := testApp(t)
	a.demandAuthorization(scopeNeed([]string{"write:vote"}))

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

	// 之后的登录成功不能重新弹出已取消的授权提示。
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.onAuthChanged(nil)
	if a.authGate.open || a.hasPendingAuthorization() {
		t.Errorf("取消后登录不该重弹授权窗：%+v", a.authGate)
	}
}

// 完整链路：403 缺权限 → 弹窗 → 打开浏览器 → 回调。授权成功后应用不替用户
// 重发原请求，由用户自己再点一次。
func TestMissingScopeDoesNotRetryAfterCallback(t *testing.T) {
	var favoritesCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			_, _ = fmt.Fprint(w, `{"c":0,"m":"请求成功","d":{"access_token":"NEW","refresh_token":"R","token_type":"Bearer","expires_in":3600,"scope":"read profile read:me write:comment write:vote"}}`)
		case "/api/oauth/me", "/api/v1/me/summary":
			_, _ = fmt.Fprint(w, `{"c":0,"d":{"username":"jimmy"}}`)
		case "/api/v1/me/follows/timeline":
			favoritesCalls.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","m":"缺少权限范围","required_scope":["write:vote"],"d":null}`)
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
		if s.Label == "动态" {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("找不到动态 tab")
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
	waitFor(t, func() bool { return !a.hasPendingAuthorization() }, "授权完成后没有清掉授权状态")
	tt.Frame()
	if tt.HasText("授权后重新加载的内容") {
		t.Errorf("应用不该替用户重发原请求：%q", tt.Texts())
	}
	if !a.auth.loggedIn() || a.auth.accessToken() != "NEW" {
		t.Errorf("窗口没有换用新令牌：loggedIn=%v", a.auth.loggedIn())
	}
	if got := favoritesCalls.Load(); got != 1 {
		t.Errorf("动态请求次数 = %d，期望只发首次 403，不再自动重发", got)
	}
}

// 全局拦截：HTTP 出口拿到 insufficient_scope 就登记授权弹窗，不需要调用点
// 自己接 handleAuthError。弹窗画在当前聚焦窗口上（这里伪装成通知窗口），
// 即使这次请求是从别处发出的——用户要先看见「前往授权」。
func TestGlobalAuthInterceptionUsesFocusedWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","required_scope":["write:economy"],"m":"缺少权限范围：write:economy","d":null,"t":1795363233804,"u":"/api/v1/me/sign"}`)
	}))
	defer srv.Close()

	a := newApp(t.TempDir())
	a.autoLoad = false
	a.client.Base = srv.URL + "/api"
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Scope: "read:economy"}); err != nil {
		t.Fatal(err)
	}
	focused := &mygo.Window{}
	a.notifWin = focused
	old := authFocusedWindow
	authFocusedWindow = func() *mygo.Window { return focused }
	defer func() { authFocusedWindow = old }()

	if _, err := a.client.fetchCoinSign(context.Background()); err == nil {
		t.Fatal("403 insufficient_scope 应该返回错误")
	}
	waitFor(t, func() bool { return a.authGate.open }, "全局拦截没有打开授权弹窗")
	if !a.authGate.wants(toastNotifications) {
		t.Errorf("弹窗应画在聚焦的通知窗口：%+v", a.authGate)
	}
	if a.authGate.wants(toastMain) || a.authGate.wants(toastCoins) {
		t.Errorf("弹窗不该画在没有聚焦的窗口：%+v", a.authGate)
	}
	if got := a.authGate.need.Scopes; len(got) != 1 || got[0] != "write:economy" {
		t.Errorf("弹窗缺少 write:economy：%+v", a.authGate.need)
	}
}

// 平台没给这个应用开通的端点也回 insufficient_scope，但 required_scope 当前
// 令牌已经全有——再授权也拿不到，全局拦截要跳过，别让用户白跑一趟。
func TestGlobalAuthInterceptionSkipsAlreadyGrantedScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","required_scope":["write:economy"],"m":"未开通","d":null}`)
	}))
	defer srv.Close()

	a := newApp(t.TempDir())
	a.autoLoad = false
	a.client.Base = srv.URL + "/api"
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Scope: "read:economy write:economy"}); err != nil {
		t.Fatal(err)
	}
	focused := &mygo.Window{}
	a.profileWin = focused
	old := authFocusedWindow
	authFocusedWindow = func() *mygo.Window { return focused }
	defer func() { authFocusedWindow = old }()

	if _, err := a.client.fetchCoinSign(context.Background()); err == nil {
		t.Fatal("403 insufficient_scope 应该返回错误")
	}
	time.Sleep(30 * time.Millisecond)
	if a.authGate.open {
		t.Errorf("scope 已授予时全局拦截不该弹窗：%+v", a.authGate)
	}
}

// 所有入口（显式 handleAuthError 与全局拦截）都收敛到当前聚焦窗口：后台从
// 发帖窗口发出的失败，用户正在看「我的」窗口时弹在「我的」里。
func TestAuthPromptOverridesToFocusedWindow(t *testing.T) {
	a := testApp(t)
	focused := &mygo.Window{}
	a.profileWin = focused
	old := authFocusedWindow
	authFocusedWindow = func() *mygo.Window { return focused }
	defer func() { authFocusedWindow = old }()

	a.demandAuthorizationFor(toastCompose, scopeNeed([]string{"write:topic"}))
	if !a.authGate.wants(toastProfile) {
		t.Errorf("弹窗应画在聚焦的「我的」窗口：%+v", a.authGate)
	}
	if a.authGate.wants(toastCompose) {
		t.Errorf("弹窗不该留在发起调用的发帖窗口：%+v", a.authGate)
	}
}

// 私有 tab 在未登录时被点开：弹窗负责去登录，登录成功后自动加载这一页。
func TestPrivateTabClickAsksForLogin(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	if err := tt.Click("动态"); err != nil {
		t.Fatal(err)
	}
	if !a.authGate.open {
		t.Fatal("点私有 tab 没有打开全局授权弹窗")
	}
	tt.Frame()
	for _, want := range []string{"需要登录", "「动态」需要登录 2Libra 后才能查看。", "前往授权", "取消"} {
		if !tt.HasText(want) {
			t.Errorf("登录弹窗缺少 %q：%q", want, tt.Texts())
		}
	}
}
