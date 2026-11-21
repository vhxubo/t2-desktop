package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestTokenServer 是一个只处理令牌端点的测试服务器；其余接口一律 401，
// 用来把请求送进「刷新 + 重试」这条路径。/oauth/* 与 /v1/me/summary 供
// 身份与连通性检查使用。
func newTestTokenServer(t *testing.T, tokenHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			tokenHandler(w, r)
		case "/api/oauth/me", "/api/v1/me/summary":
			_, _ = fmt.Fprint(w, `{"c":0,"d":{"username":"jimmy"}}`)
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"c":401,"code":"unauthorized","m":"令牌失效","d":null}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func signedInManager(t *testing.T, dir string) *AuthManager {
	t.Helper()
	m := newAuthManager(dir)
	if err := m.storeToken(tokenData{AccessToken: "old", RefreshToken: "r1", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	return m
}

// 记录 onChange 的通知，便于断言登录/退出/失效的真实走向。
type noticeLog struct {
	mu  sync.Mutex
	err []error
}

func (n *noticeLog) record(err error) {
	n.mu.Lock()
	n.err = append(n.err, err)
	n.mu.Unlock()
}

func (n *noticeLog) all() []error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]error(nil), n.err...)
}

// 刷新令牌时服务端 5xx / 断网都只是暂时的：不能把一个还有效的登录态清掉，
// 否则用户每次网络抖动都要重新授权一次。
func TestRefreshTransientFailureKeepsSession(t *testing.T) {
	srv := newTestTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, `{"c":500,"m":"服务端错误","d":null}`)
	})
	dir := t.TempDir()
	m := signedInManager(t, dir)
	m.apiBase = srv.URL + "/api"
	m.http = srv.Client()
	var notices noticeLog
	m.onChange = notices.record

	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: m}
	_, err := c.fetch(context.Background(), tabSpecs[0], 1)
	if err == nil {
		t.Fatal("401 之后刷新失败，请求应返回错误")
	}
	if !strings.Contains(friendlyError(err), "刷新令牌失败") {
		t.Errorf("错误应说明是刷新失败，而不是让用户重新登录：%q", friendlyError(err))
	}
	if !m.loggedIn() || m.accessToken() != "old" {
		t.Fatalf("临时故障不应清掉登录态：%+v", m.token)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "token.json")); statErr != nil {
		t.Errorf("令牌文件不应被删除：%v", statErr)
	}
	if got := notices.all(); len(got) != 0 {
		t.Errorf("临时故障不应触发登录态变化回调：%v", got)
	}
}

// 刷新令牌被服务端明确拒绝（invalid_grant）才算登录失效：清令牌并通知 UI。
func TestRefreshInvalidGrantClearsSession(t *testing.T) {
	srv := newTestTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"c":400,"code":"invalid_grant","m":"refresh_token 已失效","d":null}`)
	})
	dir := t.TempDir()
	m := signedInManager(t, dir)
	m.apiBase = srv.URL + "/api"
	m.http = srv.Client()
	var notices noticeLog
	m.onChange = notices.record

	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: m}
	if _, err := c.fetch(context.Background(), tabSpecs[0], 1); err == nil {
		t.Fatal("刷新失败后请求应返回错误")
	}
	if m.loggedIn() {
		t.Fatal("refresh_token 被拒绝后应退出登录")
	}
	if _, err := os.Stat(filepath.Join(dir, "token.json")); !os.IsNotExist(err) {
		t.Error("登录失效后令牌文件应被删除")
	}
	got := notices.all()
	if len(got) != 1 || got[0] == nil || !strings.Contains(got[0].Error(), "刷新令牌失败") {
		t.Fatalf("应通知一次登录失效：%v", got)
	}
}

// 用新令牌重试仍然是 401：会话确实没了，立刻清掉，不要每个请求都再刷一遍。
func TestFreshTokenStillRejectedClearsSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/oauth/token" {
			_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"new","refresh_token":"r2","token_type":"Bearer","expires_in":3600}}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"c":401,"code":"unauthorized","m":"令牌失效","d":null}`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := signedInManager(t, dir)
	m.apiBase = srv.URL + "/api"
	m.http = srv.Client()
	var notices noticeLog
	m.onChange = notices.record

	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: m}
	if _, err := c.fetch(context.Background(), tabSpecs[0], 1); err == nil {
		t.Fatal("两次 401 后请求应返回错误")
	}
	if m.loggedIn() {
		t.Fatal("新令牌也被 401 拒绝时应清掉登录态")
	}
	got := notices.all()
	if len(got) != 1 || got[0] == nil || !strings.Contains(got[0].Error(), "登录已失效") {
		t.Fatalf("应通知一次登录失效：%v", got)
	}
}

// 退出登录与在途刷新竞争时，退出必须赢：迟到的刷新结果不能把令牌写回。
func TestLogoutWinsOverInFlightRefresh(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	srv := newTestTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"new","refresh_token":"r2","token_type":"Bearer","expires_in":3600}}`)
	})
	dir := t.TempDir()
	m := signedInManager(t, dir)
	m.apiBase = srv.URL + "/api"
	m.http = srv.Client()

	done := make(chan error, 1)
	go func() {
		_, err := m.refresh(context.Background(), "old")
		done <- err
	}()
	<-started
	m.Logout()
	close(release)
	err := <-done
	if !errors.Is(err, errSessionChanged) {
		t.Fatalf("退出后的刷新结果应被丢弃，实际：%v", err)
	}
	if m.loggedIn() {
		t.Fatalf("退出后登录态又回来了：%+v", m.token)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "token.json")); !os.IsNotExist(statErr) {
		t.Error("退出后令牌文件不应被重新写回")
	}
}

// 退出登录会取消还在等待的回调；之后迟到的回调不能把用户又签回来。
func TestLogoutCancelsPendingLogin(t *testing.T) {
	dir := t.TempDir()
	m := newAuthManager(dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	authorize, err := m.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(authorize)
	if err != nil {
		t.Fatal(err)
	}
	m.Logout()
	callback := defaultRedirectURI + "?code=c&state=" + u.Query().Get("state")
	if err := m.Complete(callback); err == nil {
		t.Fatal("退出后仍接受了这次授权的回调")
	}
	if m.loggedIn() {
		t.Error("退出后不该被迟到的回调重新登录")
	}
}

// 上一次授权被新的一次取代时，旧协程不能把自己的取消错误报给 UI，否则
// 会盖掉新一次授权的提示。
func TestSupersededLoginAttemptStaysSilent(t *testing.T) {
	srv := newTestTokenServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"T","refresh_token":"R","token_type":"Bearer","expires_in":3600}}`)
	})
	dir := t.TempDir()
	m := newAuthManager(dir)
	m.apiBase = srv.URL + "/api"
	m.http = srv.Client()
	got := make(chan error, 8)
	m.onChange = func(err error) { got <- err }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := m.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	authorize, err := m.Begin(ctx) // 取代上一次
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authorize)

	select {
	case err := <-got:
		t.Fatalf("被取代的授权不该再通知 UI：%v", err)
	case <-time.After(150 * time.Millisecond):
	}

	if err := m.Complete(defaultRedirectURI + "?code=c&state=" + u.Query().Get("state")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("第二次授权应成功：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("第二次授权超时")
	}
	if !m.loggedIn() || m.accessToken() != "T" || m.username() != "jimmy" {
		t.Fatalf("登录态不对：%+v", m.token)
	}
}

// The profile endpoints differ in shape: the account name and the avatar URL
// must be found whether they sit at the top level or under user, and both are
// cached in token.json so the next start shows the avatar without a request.
func TestFetchIdentityReadsNameAndAvatar(t *testing.T) {
	bodies := []string{
		`{"c":0,"d":{"username":"jimmy","avatar_url":"https://r2.2libra.com/a.png"}}`,
		`{"c":0,"d":{"user":{"username":"jimmy","avatarUrl":"https://r2.2libra.com/a.png"}}}`,
	}
	for _, body := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/oauth/me" {
				http.NotFound(w, r)
				return
			}
			_, _ = fmt.Fprint(w, body)
		}))
		dir := t.TempDir()
		m := newAuthManager(dir)
		m.apiBase = srv.URL + "/api"
		m.http = srv.Client()
		if err := m.storeToken(tokenData{AccessToken: "T", TokenType: "Bearer"}); err != nil {
			t.Fatal(err)
		}
		m.fetchIdentity(context.Background(), "T")
		srv.Close()
		if m.username() != "jimmy" || m.avatarURL() != "https://r2.2libra.com/a.png" {
			t.Fatalf("身份解析不对：%q / %q（响应 %s）", m.username(), m.avatarURL(), body)
		}
		var saved tokenData
		if err := readJSONFile(filepath.Join(dir, "token.json"), &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Username != "jimmy" || saved.Avatar != "https://r2.2libra.com/a.png" {
			t.Errorf("用户名或头像没有写入 token.json：%+v", saved)
		}
	}
}

// 退出登录也会走 onChange(nil)，UI 不能把它当成一次登录成功。
func TestAuthChangedDistinguishesLogout(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", Username: "jimmy", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.onAuthChanged(nil)
	if a.loginMsg != "已登录 · jimmy" {
		t.Errorf("登录提示不对：%q", a.loginMsg)
	}
	a.auth.Logout()
	if a.loginMsg != "已退出登录" || a.status != "" {
		t.Errorf("退出后仍显示登录成功：%q / %q", a.loginMsg, a.status)
	}
	if a.auth.loggedIn() {
		t.Error("退出后仍有令牌")
	}
}

// wrapAuthError 把登录链路的超时/网络错误说成人话，并附上当前生效的代理线路。
func TestWrapAuthErrorShowsProxyAndFallback(t *testing.T) {
	withProxy := &AuthManager{proxy: newProxyManager(t.TempDir())}
	msg := withProxy.wrapAuthError(context.DeadlineExceeded).Error()
	if !strings.Contains(msg, "超时") || !strings.Contains(msg, "直连") {
		t.Fatalf("期望超时并提示改为直连，实际：%s", msg)
	}
	if !strings.Contains(msg, withProxy.proxy.Status()) {
		t.Fatalf("期望带上当前代理线路，实际：%s", msg)
	}

	// 没有代理管理器时也不能 panic，仍要给出通用提示。
	noProxy := &AuthManager{}
	msg2 := noProxy.wrapAuthError(context.DeadlineExceeded).Error()
	if !strings.Contains(msg2, "超时") || !strings.Contains(msg2, "直连") {
		t.Fatalf("无代理时也应有超时+直连提示，实际：%s", msg2)
	}

	// 已是直连时不能再叫用户「改为直连」，应引导去查防火墙。
	direct := &AuthManager{proxy: newProxyManager(t.TempDir())}
	if err := direct.proxy.Save(proxyConfig{Mode: proxyDirect}); err != nil {
		t.Fatal(err)
	}
	msg3 := direct.wrapAuthError(context.DeadlineExceeded).Error()
	if !strings.Contains(msg3, "已是直连") || strings.Contains(msg3, "改为「直连」") {
		t.Fatalf("直连模式不应再建议改直连，实际：%s", msg3)
	}

	// 普通（非超时、非网络）错误原样返回，不画蛇添足。
	if withProxy.wrapAuthError(nil) != nil {
		t.Fatal("nil 应原样返回 nil")
	}
	if err := errors.New("boom"); withProxy.wrapAuthError(err) != err {
		t.Fatal("普通错误应原样返回")
	}
}

// requestedScopesList 是这次授权地址里真正申请过的 scope：默认范围加上补
// 的那几项。登录窗口用它展示「本次申请」，finishLogin 用它对比服务端实际
// 授予了什么（申请了但没授予要能看出来）。
// redirect_mode 只给内置浏览器的 webview 用：app 让自定义协议直接 302，
// 系统浏览器仍走落地页默认行为，去掉参数后必须能回到原始地址。
func TestRedirectModeHelpers(t *testing.T) {
	raw := "https://2libra.com/oauth/authorize?client_id=c&scope=read&state=s"
	embedded := embeddedAuthorizeURL(raw)
	u, err := url.Parse(embedded)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("redirect_mode"); got != "app" {
		t.Errorf("内置浏览器地址 redirect_mode = %q，期望 app", got)
	}
	if got := u.Query().Get("state"); got != "s" || u.Query().Get("scope") != "read" {
		t.Errorf("加 redirect_mode 时丢了其它参数：%s", embedded)
	}
	if got := systemAuthorizeURL(embedded); got != raw {
		t.Errorf("去掉 redirect_mode 后 = %q，期望 %q", got, raw)
	}
}

func TestRequestedScopesIncludeExtraScopes(t *testing.T) {
	m := newAuthManager(t.TempDir())
	if _, err := m.BeginScopes(context.Background(), []string{"write:topic"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Cancel)

	got := m.requestedScopesList()
	if !containsString(got, "write:topic") {
		t.Fatalf("申请过的 scope = %v，缺少 write:topic", got)
	}
	for _, want := range strings.Fields(defaultScopes) {
		if !containsString(got, want) {
			t.Errorf("申请过的 scope = %v，缺少默认项 %q", got, want)
		}
	}

	granted, missing := grantedAndMissing(got, strings.Fields(defaultScopes))
	if !containsString(missing, "write:topic") {
		t.Errorf("未授予列表 = %v，应包含 write:topic", missing)
	}
	if containsString(granted, "write:topic") {
		t.Errorf("已授予列表不该包含 write:topic：%v", granted)
	}
}

// 重新授权是替换而不是累加：重新登录时要把上一次拿到的 scope 一起申请，
// 否则只申请默认范围就等于把按需开通的 write:topic / write:economy 撤掉。
func TestBeginScopesKeepsPreviouslyGrantedScopes(t *testing.T) {
	m := newAuthManager(t.TempDir())
	if err := m.storeToken(tokenData{
		AccessToken: "tok",
		TokenType:   "Bearer",
		Scope:       defaultScopes + " write:topic write:economy",
	}); err != nil {
		t.Fatal(err)
	}
	authorize, err := m.BeginScopes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(authorize)
	if err != nil {
		t.Fatal(err)
	}
	scope := " " + u.Query().Get("scope") + " "
	for _, want := range []string{"write:topic", "write:economy"} {
		if !strings.Contains(scope, " "+want+" ") {
			t.Errorf("重新授权没有保留 %s：%q", want, scope)
		}
	}
	m.Cancel()

	// 令牌被服务端拒绝清掉后，记住的 scope 仍要在下一次授权里带上。
	m.invalidateFor("tok", errors.New("登录失效"))
	if m.accessToken() != "" {
		t.Fatal("前置条件：令牌应已被清掉")
	}
	authorize, err = m.BeginScopes(context.Background(), []string{"interact"})
	if err != nil {
		t.Fatal(err)
	}
	u, err = url.Parse(authorize)
	if err != nil {
		t.Fatal(err)
	}
	scope = " " + u.Query().Get("scope") + " "
	for _, want := range []string{"write:topic", "write:economy", "interact"} {
		if !strings.Contains(scope, " "+want+" ") {
			t.Errorf("令牌清掉后重新授权没有保留 %s：%q", want, scope)
		}
	}
}

// 旧会话迟到的 401 不能清掉刚重新授权拿到的令牌：invalidateFor 只认它还
// 持有的那个 access token。
func TestInvalidateForIgnoresReplacedToken(t *testing.T) {
	m := newAuthManager(t.TempDir())
	if err := m.storeToken(tokenData{AccessToken: "NEW", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	notice := 0
	m.setOnChange(func(error) { notice++ })

	m.invalidateFor("OLD", errors.New("旧请求的 401"))
	if m.accessToken() != "NEW" || notice != 0 {
		t.Errorf("旧令牌的失效处理清掉了新会话：token=%q notice=%d", m.accessToken(), notice)
	}

	m.invalidateFor("NEW", errors.New("新令牌也被拒"))
	if m.accessToken() != "" || notice != 1 {
		t.Errorf("当前令牌被拒时应清理并通知一次：token=%q notice=%d", m.accessToken(), notice)
	}
}

// 重新授权换掉窗口的会话后，旧会话的回调必须断掉：否则旧请求迟到的失败会
// 把界面切回登录页，看起来就是刚授权完又掉登录。
func TestStaleSessionCallbackDoesNotLogOutNewSession(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "OLD", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.bindAuth()
	old := a.auth

	next := newAuthManager(t.TempDir())
	if err := next.storeToken(tokenData{AccessToken: "NEW", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.replaceAuth(next)
	if a.auth != next || !a.auth.loggedIn() {
		t.Fatalf("重新授权后窗口没有换到新会话：%+v", a.auth)
	}
	a.pageName = "list"

	old.notify(errors.New("旧请求的 401"))
	if a.auth != next || !a.auth.loggedIn() {
		t.Errorf("旧会话的回调清掉了新会话：%+v", a.auth)
	}
	if a.pageName == "login" {
		t.Errorf("旧会话的迟到失败把界面切回了登录页")
	}
}

// 令牌响应体读到一半才超时（"Client Timeout ... while reading body"），
// 是打包后登录报错的真实现场。确认 postForm 在这个阶段也套上了诊断提示，
// 而不是把裸的 context deadline exceeded 直接抛给界面。
func TestPostFormBodyReadTimeoutIsExplained(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// 先把响应头发回去，然后卡住 body，逼 client 在读 body 阶段超时。
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	p := newProxyManager(t.TempDir())
	m := &AuthManager{
		apiBase: srv.URL,
		http:    p.Client(50 * time.Millisecond),
		proxy:   p,
	}
	_, err := m.postForm(context.Background(), url.Values{})
	if err == nil {
		t.Fatal("期望读响应体超时，实际没有报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, "令牌请求超时") || !strings.Contains(msg, "直连") {
		t.Fatalf("期望读 body 超时也被包装成诊断提示，实际：%s", msg)
	}
}
