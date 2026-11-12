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
