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

// 缺权限的窗口自己弹授权窗：发帖窗口缺 write:topic 时，弹窗只出现在发帖
// 窗口里，主窗口与别的附属窗口不跟着闪一个。
func TestAuthPromptStaysInOriginatingWindow(t *testing.T) {
	a := testApp(t)
	a.demandAuthorizationFor(toastCompose, scopeNeed([]string{"write:topic"}), func() {})

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

// 同一个账号的两个窗口同时缺权限时，两处都弹；一次授权解决两条被中断的
// 调用。主窗口仍然不跟着弹。
func TestAuthPromptShowsInEveryFailingWindow(t *testing.T) {
	a := testApp(t)
	a.userCard = newUserCardState("amy")
	replays := 0
	a.demandAuthorizationFor(toastCompose, scopeNeed([]string{"write:topic"}), func() { replays++ })
	a.demandAuthorizationFor(toastUserCard, scopeNeed([]string{"interact"}), func() { replays++ })

	for _, owner := range []toastTarget{toastCompose, toastUserCard} {
		if !a.authGate.wants(owner) {
			t.Errorf("缺少权限的窗口 %v 应显示授权弹窗", owner)
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
	if !ct.HasText("缺少的权限范围：write:topic、interact") {
		t.Errorf("发帖窗口没有列出合并后的 scope：%q", ct.Texts())
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
	if replays != 0 {
		t.Errorf("还没授权就重跑了 %d 次", replays)
	}
}

// 关掉某个窗口只丢掉它自己等重跑的调用；别的窗口缺权限的那一份继续留着。
// 最后一个发起窗口也关掉时整次授权作废。
func TestAuthGateDropsCallsOfClosedWindow(t *testing.T) {
	a := testApp(t)
	composeRuns, cardRuns := 0, 0
	a.demandAuthorizationFor(toastCompose, scopeNeed([]string{"write:topic"}), func() { composeRuns++ })
	a.demandAuthorizationFor(toastUserCard, scopeNeed([]string{"interact"}), func() { cardRuns++ })

	a.authGateWindowClosed(toastCompose)
	if a.authGate.wants(toastCompose) {
		t.Error("关掉的窗口不该继续显示授权弹窗")
	}
	if !a.authGate.wants(toastUserCard) {
		t.Error("还开着的窗口应继续显示授权弹窗")
	}
	if got := len(a.authGate.replays); got != 1 {
		t.Fatalf("关掉窗口后待重跑调用 = %d，期望只剩 1 条", got)
	}
	a.replayAuthorizedCalls()
	if composeRuns != 0 || cardRuns != 1 {
		t.Errorf("重跑次数 compose=%d card=%d，期望 0/1", composeRuns, cardRuns)
	}

	// 另一条也关掉：授权状态整体清空，不会残留一个没人能看到的弹窗。
	a.demandAuthorizationFor(toastUserCard, scopeNeed([]string{"interact"}), nil)
	a.authGateWindowClosed(toastUserCard)
	if a.authGate.open || a.hasPendingAuthorization() {
		t.Errorf("最后一个发起窗口关掉后应清空授权状态：%+v", a.authGate)
	}
}

// 多账号下补权限不能借系统浏览器的登录态：账号窗口的「前往授权」要开在
// 账号册的内置浏览器里（独立会话），并且仍然申请缺失的 scope。
func TestScopeReauthUsesEmbeddedBrowserForAccountWindow(t *testing.T) {
	b := testBook(t)
	acc := addTestAccount(t, b, "jimmy")
	b.webview = true
	b.afterReady = true
	var embeddedURL string
	b.openLogin = func(u string) error { embeddedURL = u; return nil }
	var systemURL string
	b.openURL = func(u string) error { systemURL = u; return nil }

	a := newAppWithDeps(b.deps(), acc)
	a.autoLoad = false
	acc.app = a
	replayed := 0
	a.demandAuthorization(scopeNeed([]string{"write:vote"}), func() { replayed++ })
	a.authorizeGate()

	if systemURL != "" {
		t.Errorf("多账号补授权不该打开系统浏览器：%s", systemURL)
	}
	p := b.pendingLogin()
	if p == nil {
		t.Fatal("没有登记等待中的授权")
	}
	if p.accountID != acc.ID {
		t.Errorf("等待授权的账号 = %q，期望 %q", p.accountID, acc.ID)
	}
	if !p.embedded {
		t.Error("补授权应走内置浏览器")
	}
	if p.auth == a.auth {
		t.Error("补授权应挂在临时会话上，不能拿窗口里的会话去换别人的令牌")
	}
	if embeddedURL == "" {
		t.Fatal("没有打开内置浏览器授权页")
	}
	u, err := url.Parse(embeddedURL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(" "+u.Query().Get("scope")+" ", " write:vote ") {
		t.Errorf("授权 URL 没有申请缺失的 scope：%q", u.Query().Get("scope"))
	}

	// 取消这次补授权：窗口里登记的待重跑调用一并丢掉。
	b.cancelLogin()
	if a.hasPendingAuthorization() {
		t.Error("取消后仍留着待重跑的调用")
	}
	if replayed != 0 {
		t.Error("取消后不该重跑被中断的调用")
	}
	if !strings.Contains(a.loginMsg, "已取消授权") {
		t.Errorf("取消后窗口提示不对：%q", a.loginMsg)
	}
}

// 内置浏览器里补权限成功后，窗口换成新会话并自动重跑被中断的操作 —— 与系统
// 浏览器深链走的是同一条完成路径。
func TestScopeReauthReplaysAfterEmbeddedLogin(t *testing.T) {
	srv := newTestTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"NEW","refresh_token":"R","token_type":"Bearer","expires_in":3600}}`)
	})
	b := testBook(t)
	acc := addTestAccount(t, b, "jimmy")
	b.webview = true
	b.afterReady = true
	b.openLogin = func(string) error { return nil }
	b.newLoginAuth = func() *AuthManager {
		m := newAuthManagerFor(b.dir, b.proxy, legacyTokenKey)
		m.store = &memoryTokenStore{}
		m.apiBase = srv.URL + "/api"
		m.http = srv.Client()
		return m
	}

	a := newAppWithDeps(b.deps(), acc)
	a.autoLoad = false
	acc.app = a
	replayed := atomic.Int32{}
	a.demandAuthorization(scopeNeed([]string{"write:vote"}), func() { replayed.Add(1) })
	a.authorizeGate()

	p := b.pendingLogin()
	if p == nil || !p.embedded {
		t.Fatalf("没有在内置浏览器里开始补授权：%+v", p)
	}
	u, err := url.Parse(p.url)
	if err != nil {
		t.Fatal(err)
	}
	b.handleDeepLink(defaultRedirectURI + "?code=c&state=" + u.Query().Get("state"))

	waitFor(t, func() bool { return replayed.Load() == 1 }, "授权成功后重跑被中断的调用")
	if a.auth.accessToken() != "NEW" {
		t.Errorf("窗口仍用旧会话：%q", a.auth.accessToken())
	}
	if a.hasPendingAuthorization() {
		t.Error("重跑完成后授权状态应清空")
	}
	if b.pendingLogin() != nil {
		t.Error("授权结束后不该还挂着 pending")
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
		case "/api/v1/me/follows/timeline":
			favoritesCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer NEW" {
				w.WriteHeader(http.StatusForbidden)
				_, _ = fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","m":"缺少权限范围","required_scope":["write:vote"],"d":null}`)
				return
			}
			sawNewToken.Store(true)
			_, _ = fmt.Fprint(w, envelopeJSON(`{"items":[{"id":"f1","short_id":"REPLAY1","title":"授权后重新加载的内容","author":{"username":"jimmy"}}],"total":1,"page":1,"limit":50,"total_pages":1}`))
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
		t.Errorf("动态请求次数 = %d，期望首次 403 + 重跑 1 次", got)
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
