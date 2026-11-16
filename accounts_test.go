package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

/* ------------------------------------------------------------------
   测试用的账号册
------------------------------------------------------------------ */

// testBook 建一份隔离的账号册：令牌走文件存储（见 auth_store_test.go 的
// tokenStoreFactory），打开浏览器换成记录调用，不碰真实系统与主循环。
func testBook(t *testing.T) *accountBook {
	t.Helper()
	b := newAccountBook(t.TempDir())
	b.openURL = func(string) error { return nil }
	return b
}

// addTestAccount 往账号册里挂一个假会话：测试不跑浏览器授权，直接给一份
// 令牌，走的是 adoptLogin 那条真实的落库路径。
func addTestAccount(t *testing.T, b *accountBook, name string) *account {
	t.Helper()
	m := newAuthManagerFor(b.dir, b.proxy, legacyTokenKey)
	m.store = &memoryTokenStore{}
	if err := m.storeToken(tokenData{
		AccessToken:  "tok-" + name,
		RefreshToken: "r-" + name,
		TokenType:    "Bearer",
		Username:     name,
	}); err != nil {
		t.Fatal(err)
	}
	acc, err := b.adoptLogin(m)
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

// setTestUnread 直接摆一个未读数：窗口没开的账号由后台轮询写这个字段，
// 测试里不必真去发一次请求。
func setTestUnread(b *accountBook, id string, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if acc := b.accountLocked(id); acc != nil {
		acc.unread = n
	}
}

// setTestNeedLogin 让一个账号的会话失效：清掉令牌就是「刷新被拒」之后的
// 状态，面板据此显示「登录已失效」。
func setTestNeedLogin(b *accountBook, id string) {
	acc := b.account(id)
	if acc == nil {
		return
	}
	if err := acc.auth.clearToken(); err != nil {
		panic(err)
	}
	b.bindSession(acc)
}

/* ------------------------------------------------------------------
   账号 id 与索引
------------------------------------------------------------------ */

// 账号 id 由昵称决定：同一个人重新登录一定落到同一份凭据上，大小写与标点
// 不同的昵称不会互相覆盖，还没有昵称时也不会算出空 id。
func TestAccountIDForIsStableAndDistinct(t *testing.T) {
	if a, b := accountIDFor("jimmy"), accountIDFor("Jimmy"); a != b {
		t.Fatalf("大小写不同应得到同一个 id：%q / %q", a, b)
	}
	if a, b := accountIDFor("jimmy"), accountIDFor("amy"); a == b {
		t.Fatalf("不同昵称应得到不同 id：%q", a)
	}
	// 规范化后撞车的两个昵称也不能落到同一份凭据上。
	if a, b := accountIDFor("a/b"), accountIDFor("a?b"); a == b {
		t.Fatalf("规范化后相同的昵称应仍能区分：%q", a)
	}
	// 还没有昵称（身份接口失败）时退回随机 id，但不能是空串。
	if id := accountIDFor(""); id == "" || !strings.HasPrefix(id, "user-") {
		t.Fatalf("空昵称的 id = %q", id)
	}
	// id 必须能直接做文件名。
	if id := accountIDFor("jim/my"); strings.ContainsAny(id, `/\:`) {
		t.Fatalf("id 不能含路径分隔符：%q", id)
	}
}

// 每个账号一份令牌：两条会话互不覆盖，重新打开账号册（相当于重启应用）后
// 都还在。
func TestAccountsStoreOneTokenEachAndSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	b := newAccountBook(dir)
	jimmy := addTestAccount(t, b, "jimmy")
	amy := addTestAccount(t, b, "amy")
	if jimmy.ID == amy.ID {
		t.Fatalf("两个账号的 id 相同：%q", jimmy.ID)
	}
	if got := b.accountCount(); got != 2 {
		t.Fatalf("账号数 = %d，期望 2", got)
	}

	// 重启：索引与各自的令牌都要读回来。
	restarted := newAccountBook(dir)
	if got := restarted.accountCount(); got != 2 {
		t.Fatalf("重启后账号数 = %d，期望 2", got)
	}
	for _, name := range []string{"jimmy", "amy"} {
		acc := restarted.accountByUsername(name)
		if acc == nil {
			t.Fatalf("重启后找不到账号 %q", name)
		}
		if got := acc.auth.accessToken(); got != "tok-"+name {
			t.Errorf("%s 的令牌 = %q，期望它自己那一份", name, got)
		}
	}
	// 凭据落在各自的文件里，不是一份共用。
	for _, name := range []string{"jimmy", "amy"} {
		if _, err := os.Stat(filepath.Join(dir, "token-"+accountIDFor(name)+".json")); err != nil {
			t.Errorf("%s 的凭据文件不存在：%v", name, err)
		}
	}
}

// 旧版本只有一个账号、一份 token.json：升级后第一次启动要把它迁移成正式
// 账号，登录态不能因为升级掉线。
func TestLegacySessionMigratesToAnAccount(t *testing.T) {
	dir := t.TempDir()
	legacy := newAuthManager(dir) // 旧版单账号会话（空键）
	if err := legacy.storeToken(tokenData{
		AccessToken: "old", RefreshToken: "r0", TokenType: "Bearer", Username: "jimmy",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "token.json")); err != nil {
		t.Fatalf("前置条件：旧版应写 token.json：%v", err)
	}

	b := newAccountBook(dir)
	if got := b.accountCount(); got != 1 {
		t.Fatalf("迁移后账号数 = %d，期望 1", got)
	}
	view := b.accountsView()[0]
	if view.Name != "jimmy" {
		t.Errorf("迁移出的账号名 = %q", view.Name)
	}
	if _, err := os.Stat(filepath.Join(dir, "token.json")); !os.IsNotExist(err) {
		t.Error("迁移后旧的 token.json 应被删除")
	}
	if _, err := os.Stat(filepath.Join(dir, "token-"+view.ID+".json")); err != nil {
		t.Errorf("迁移后应写入账号自己的凭据：%v", err)
	}
	if got := b.account(view.ID).auth.accessToken(); got != "old" {
		t.Errorf("迁移后令牌 = %q，期望「old」", got)
	}
	// 再启动一次不会再迁移一遍：账号还是那一个。
	if again := newAccountBook(dir); again.accountCount() != 1 {
		t.Errorf("重复迁移：账号数 = %d", again.accountCount())
	}
}

// 同一个账号再次授权（比如换掉失效的令牌）不该多开一条账号记录。
func TestAdoptLoginMergesTheSameAccount(t *testing.T) {
	b := testBook(t)
	first := addTestAccount(t, b, "jimmy")

	m := newAuthManagerFor(b.dir, b.proxy, legacyTokenKey)
	m.store = &memoryTokenStore{}
	if err := m.storeToken(tokenData{AccessToken: "tok-new", Username: "Jimmy", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	second, err := b.adoptLogin(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.accountCount(); got != 1 {
		t.Fatalf("同一账号重复授权后账号数 = %d，期望 1", got)
	}
	if second.ID != first.ID {
		t.Errorf("重复授权换了 id：%q → %q", first.ID, second.ID)
	}
	if got := second.auth.accessToken(); got != "tok-new" {
		t.Errorf("合并后令牌 = %q，期望新的那一份", got)
	}
}

/* ------------------------------------------------------------------
   添加账号：浏览器授权的回调
------------------------------------------------------------------ */

// 一次「添加账号」授权：回调送达后应当落成一个账号，令牌写进它自己的凭据。
func TestAddAccountCompletesFromCallback(t *testing.T) {
	srv := newTestTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"T","refresh_token":"R","token_type":"Bearer","expires_in":3600}}`)
	})
	dir := t.TempDir()
	b := newAccountBook(dir)
	b.openURL = func(string) error { return nil }

	m := newAuthManagerFor(dir, b.proxy, legacyTokenKey)
	m.apiBase = srv.URL + "/api"
	m.http = srv.Client()
	m.store = &memoryTokenStore{}
	m.onChange = func(err error) { b.finishAddAccount(m, err) }
	authorize, err := m.BeginScopes(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authorize)
	b.mu.Lock()
	b.pending = &accountLogin{auth: m}
	b.mu.Unlock()

	if !b.completeCallback(defaultRedirectURI + "?code=c&state=" + u.Query().Get("state")) {
		t.Fatal("账号册没有认领这次回调")
	}
	waitFor(t, func() bool { return b.accountCount() == 1 }, "账号落库")
	acc := b.accountsView()[0]
	if acc.Name != "jimmy" {
		t.Errorf("新账号名 = %q，期望身份接口返回的 jimmy", acc.Name)
	}
	if got := b.account(acc.ID).auth.accessToken(); got != "T" {
		t.Errorf("落库后的令牌 = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "token-"+acc.ID+".json")); err != nil {
		t.Errorf("令牌应写进账号自己的凭据：%v", err)
	}
	if b.pendingLogin() != nil {
		t.Error("授权结束后不该还挂着 pending")
	}
}

// 没有人在等回调时不能假装完成：那说明应用是被这个链接刚拉起来的，
// PKCE 会话不在本进程。
func TestCompleteCallbackWithoutPendingIsRejected(t *testing.T) {
	b := testBook(t)
	b.handleDeepLink(defaultRedirectURI + "?code=c&state=s")
	if b.accountCount() != 0 {
		t.Fatalf("没有 pending 时不该添出账号：%d", b.accountCount())
	}
	if msg := b.loginStatus(); !strings.Contains(msg, "没有对应的授权") {
		t.Errorf("提示 = %q，期望说明没有对应的授权", msg)
	}
}

// 深链要认得到「哪个窗口在等授权」：账号册没在等时，回调该交给正在补权限的
// 账号窗口；别的小程序链接（非本应用协议）不该动到任何一次授权。
func TestDeepLinkRoutingFindsWindowAwaitingAuthorization(t *testing.T) {
	b := testBook(t)
	acc := addTestAccount(t, b, "jimmy")
	app := newAccountApp(b, acc)
	acc.app = app
	t.Cleanup(func() { acc.auth.Cancel() })

	b.handleDeepLink("https://example.com/whatever")
	if acc.auth.awaitingLogin() {
		t.Fatal("前置条件：还没有发起授权")
	}

	if _, err := acc.auth.BeginScopes(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := b.appAwaitingLogin(); got != app {
		t.Fatalf("认不出正在等授权的窗口：%v", got)
	}
	// 外站链接既不是本应用的回调，也不该取消窗口里正在等的那次授权。
	b.handleDeepLink("https://example.com/whatever")
	if !acc.auth.awaitingLogin() {
		t.Error("外站链接不该动到窗口里正在等的那次授权")
	}
	if b.pendingLogin() != nil {
		t.Error("外站链接不该变成账号册的授权")
	}
}

/* ------------------------------------------------------------------
   退出账号
------------------------------------------------------------------ */

// 退出账号：关掉它的窗口、删掉它的令牌，并从账号册里摘掉；别的账号不动。
func TestLogoutAccountRemovesOnlyThatAccount(t *testing.T) {
	dir := t.TempDir()
	b := newAccountBook(dir)
	jimmy := addTestAccount(t, b, "jimmy")
	amy := addTestAccount(t, b, "amy")
	tokenPath := filepath.Join(dir, "token-"+jimmy.ID+".json")
	if _, err := os.Stat(tokenPath); err != nil {
		t.Fatalf("前置条件：jimmy 的凭据应存在：%v", err)
	}

	b.logoutAccount(jimmy.ID)
	if got := b.accountCount(); got != 1 {
		t.Fatalf("退出后账号数 = %d，期望 1", got)
	}
	if b.account(jimmy.ID) != nil {
		t.Error("退出的账号还在账号册里")
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Error("退出的账号令牌应被删除")
	}
	if b.account(amy.ID) == nil {
		t.Error("别的账号不该受影响")
	}
	// 索引也要落盘：重启后只剩 amy。
	if again := newAccountBook(dir); again.accountCount() != 1 {
		t.Errorf("重启后账号数 = %d，期望 1", again.accountCount())
	}
}

// 退出账号要压过还在等回调的授权：否则浏览器里点一下同意，刚退出的账号又被
// 签回来，还会在凭据目录里留下一个没有索引的令牌文件。
func TestLogoutAccountCancelsPendingAuthorization(t *testing.T) {
	b := testBook(t)
	acc := addTestAccount(t, b, "jimmy")
	// 这个账号在窗口里补过一次权限，正在等浏览器回调。
	if _, err := acc.auth.BeginScopes(context.Background(), []string{"write:topic"}); err != nil {
		t.Fatal(err)
	}
	if !acc.auth.awaitingLogin() {
		t.Fatal("前置条件：应当有一次授权在等回调")
	}
	b.logoutAccount(acc.ID)
	if acc.auth.awaitingLogin() {
		t.Error("退出账号后仍有一次授权在等回调")
	}
	if b.accountCount() != 0 {
		t.Fatalf("退出后账号还在：%d", b.accountCount())
	}
}

/* ------------------------------------------------------------------
   重新登录失效的账号
------------------------------------------------------------------ */

// 令牌被服务端拒掉的账号可以从面板重新登录：授权回调落地后同一个账号恢复
// 可用，不会多出一条账号记录。
func TestReloginRecoversExpiredAccount(t *testing.T) {
	srv := newTestTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"fresh","refresh_token":"r2","token_type":"Bearer","expires_in":3600}}`)
	})
	dir := t.TempDir()
	b := newAccountBook(dir)
	var opened string
	b.openURL = func(u string) error { opened = u; return nil }
	acc := addTestAccount(t, b, "jimmy")
	acc.auth.apiBase = srv.URL + "/api"
	acc.auth.http = srv.Client()
	setTestNeedLogin(b, acc.ID)
	if !b.accountsView()[0].NeedsLogin {
		t.Fatal("前置条件：这个账号应当已失效")
	}

	b.reloginAccount(acc.ID)
	if opened == "" {
		t.Fatal("重新登录没有打开授权页")
	}
	u, err := url.Parse(opened)
	if err != nil {
		t.Fatal(err)
	}
	if b.pendingLogin() == nil {
		t.Fatal("重新登录没有登记等待中的授权")
	}
	b.handleDeepLink(defaultRedirectURI + "?code=c&state=" + u.Query().Get("state"))

	waitFor(t, func() bool { return acc.auth.accessToken() == "fresh" }, "重新登录后的令牌")
	if b.accountCount() != 1 {
		t.Fatalf("重新登录后账号数 = %d，期望 1", b.accountCount())
	}
	if b.accountsView()[0].NeedsLogin {
		t.Error("重新登录后账号仍标着失效")
	}
	if b.pendingLogin() != nil {
		t.Error("重新登录结束后不该还挂着 pending")
	}
	if acc.auth.onChange == nil {
		t.Error("重新登录后会话的处理函数没有交还给账号册")
	}
}

/* ------------------------------------------------------------------
   后台：未读角标与令牌保活
------------------------------------------------------------------ */

// 窗口没开的账号也要有未读角标：后台轮询把数字写进账号册，面板与托盘读它。
func TestUnreadPollUpdatesClosedAccountBadge(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != notificationUnreadPath {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, `{"c":0,"d":{"unread_count":3}}`)
	}))
	defer srv.Close()

	b := testBook(t)
	addTestAccount(t, b, "jimmy")
	b.bgClient = func(acc *account) *Client {
		return &Client{HTTP: srv.Client(), Base: srv.URL, Auth: acc.auth}
	}
	b.pollUnread()
	waitFor(t, func() bool { return b.accountsView()[0].Unread == 3 }, "未读数写回")
	if gotAuth != "Bearer tok-jimmy" {
		t.Errorf("轮询没有带上这个账号的令牌：%q", gotAuth)
	}
	if !strings.Contains(b.summary(), "3 条未读") {
		t.Errorf("汇总 = %q，期望带上未读数", b.summary())
	}
}

// 令牌快过期就提前换掉：挂在后台的账号也要保持登录，切过去时不用先等一次
// 刷新。
func TestKeepAliveRefreshesExpiringToken(t *testing.T) {
	srv := newTestTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"new","refresh_token":"r2","token_type":"Bearer","expires_in":3600}}`)
	})
	b := testBook(t)
	acc := addTestAccount(t, b, "jimmy")
	acc.auth.apiBase = srv.URL + "/api"
	acc.auth.http = srv.Client()
	// 一分钟后过期：落在提前刷新的窗口里。
	if err := acc.auth.storeToken(tokenData{
		AccessToken: "old", RefreshToken: "r1", TokenType: "Bearer",
		ObtainedAt: time.Now().Unix(), ExpiresIn: 60, Username: "jimmy",
	}); err != nil {
		t.Fatal(err)
	}
	b.keepAliveOnce()
	waitFor(t, func() bool { return acc.auth.accessToken() == "new" }, "令牌被提前刷新")
	if got := time.Until(acc.auth.expiry()); got < 50*time.Minute {
		t.Errorf("刷新后到期时间没有更新：还剩 %v", got)
	}
}

// 还早着的令牌不去碰：保活不该每十分钟换一次用不上的令牌。
func TestKeepAliveLeavesFreshTokenAlone(t *testing.T) {
	srv := newTestTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("令牌还早着，不该请求刷新")
		_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"new","token_type":"Bearer","expires_in":3600}}`)
	})
	b := testBook(t)
	acc := addTestAccount(t, b, "jimmy")
	acc.auth.apiBase = srv.URL + "/api"
	acc.auth.http = srv.Client()
	if err := acc.auth.storeToken(tokenData{
		AccessToken: "old", RefreshToken: "r1", TokenType: "Bearer",
		ObtainedAt: time.Now().Unix(), ExpiresIn: 3600, Username: "jimmy",
	}); err != nil {
		t.Fatal(err)
	}
	b.keepAliveOnce()
	time.Sleep(80 * time.Millisecond)
	if got := acc.auth.accessToken(); got != "old" {
		t.Fatalf("令牌被无谓地换掉了：%q", got)
	}
}

// 刷新被服务端明确拒绝时，这个账号标成「登录已失效」，面板给出重新登录入口；
// 别的账号不受影响，也不会把整条登录记录删掉。
func TestKeepAliveInvalidGrantMarksNeedsLogin(t *testing.T) {
	srv := newTestTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"c":400,"code":"invalid_grant","m":"refresh_token 已失效","d":null}`)
	})
	b := testBook(t)
	acc := addTestAccount(t, b, "jimmy")
	acc.auth.apiBase = srv.URL + "/api"
	acc.auth.http = srv.Client()
	if err := acc.auth.storeToken(tokenData{
		AccessToken: "old", RefreshToken: "r1", TokenType: "Bearer",
		ObtainedAt: time.Now().Unix(), ExpiresIn: 60, Username: "jimmy",
	}); err != nil {
		t.Fatal(err)
	}
	b.keepAliveOnce()
	waitFor(t, func() bool { return b.accountsView()[0].NeedsLogin }, "账号被标成需要重新登录")
	if got := b.accountCount(); got != 1 {
		t.Fatalf("失效的账号应留着等重新授权，账号数 = %d", got)
	}
}

/* ------------------------------------------------------------------
   窗口与界面
------------------------------------------------------------------ */

// 关掉窗口只是把这个窗口的界面状态丢掉：账号还挂在账号册里，未读数沿用
// 窗口里的最后一个值，等后台轮询接班。
func TestClosingWindowKeepsAccountInBook(t *testing.T) {
	b := testBook(t)
	acc := addTestAccount(t, b, "jimmy")
	app := newAppWithDeps(b.deps(), acc)
	acc.app = app
	app.unread = 5

	b.windowClosed(app)
	if acc.app != nil {
		t.Error("关掉的窗口还挂在账号上")
	}
	if b.accountCount() != 1 {
		t.Fatalf("关窗口不该退出账号，账号数 = %d", b.accountCount())
	}
	view := b.accountsView()[0]
	if view.Open {
		t.Error("窗口已关，面板却还标着「已打开」")
	}
	if view.Unread != 5 {
		t.Errorf("未读数 = %d，期望沿用窗口里的 5", view.Unread)
	}
}

// 一个账号窗口真的能画出来：标题栏带账号名（任务栏、Alt+Tab 靠它区分），
// 侧边栏底部有「账号」入口与「退出登录」。说明这个窗口确实挂在账号册上，
// 而不是一个没人管的孤立窗口。
func TestAccountWindowViewBuildsWithBook(t *testing.T) {
	b := testBook(t)
	acc := addTestAccount(t, b, "jimmy")
	app := newAccountApp(b, acc)
	app.autoLoad = false

	tt := ui.NewTester(app.view, 1080, 720)
	tt.Frame()
	for _, want := range []string{"T2 Desktop", "jimmy", "账号", "设置", "关于", "退出登录"} {
		if !tt.HasText(want) {
			t.Errorf("账号窗口缺少 %q：%q", want, tt.Texts())
		}
	}
	if got, want := app.windowTitle(), "T2 Desktop · jimmy"; got != want {
		t.Errorf("窗口标题 = %q，期望 %q", got, want)
	}
	if got, want := app.windowStateKey(), "main-"+acc.ID; got != want {
		t.Errorf("窗口状态键 = %q，期望 %q", got, want)
	}
}

// 一个账号都没有时的未登录窗口：照样能浏览公开内容，侧边栏给「登录」「注册」
// 与「账号」三个入口。
func TestGuestWindowViewBuildsWithBook(t *testing.T) {
	b := testBook(t)
	app := newGuestApp(b)
	app.autoLoad = false

	tt := ui.NewTester(app.view, 1080, 720)
	tt.Frame()
	for _, want := range []string{"T2 Desktop", "登录", "注册", "账号"} {
		if !tt.HasText(want) {
			t.Errorf("未登录窗口缺少 %q：%q", want, tt.Texts())
		}
	}
	if got, want := app.windowTitle(), "T2 Desktop"; got != want {
		t.Errorf("未登录窗口标题 = %q，期望 %q", got, want)
	}
	if got, want := app.windowStateKey(), "main"; got != want {
		t.Errorf("未登录窗口状态键 = %q，期望 %q", got, want)
	}
}

// 账号面板：每个账号一行，未读数、登录是否失效与几个动作都在一屏里。
func TestPanelViewListsAccountsWithUnreadAndActions(t *testing.T) {
	b := testBook(t)
	jimmy := addTestAccount(t, b, "jimmy")
	amy := addTestAccount(t, b, "amy")
	setTestUnread(b, jimmy.ID, 3)
	setTestNeedLogin(b, amy.ID)

	tt := ui.NewTester(b.panelView, accountsWindowWidth, accountsWindowHeight)
	tt.Frame()
	for _, want := range []string{
		"账号", "2 个账号", "jimmy", "amy",
		"登录已失效，需要重新授权",
		"重新登录", "退出", "添加账号", "每个账号一个窗口",
	} {
		if !tt.HasText(want) {
			t.Errorf("账号面板缺少文案 %q：%q", want, tt.Texts())
		}
	}
	// 未读数是昵称旁边的角标：数字本身要画出来。
	if _, ok := tt.Find("3"); !ok {
		t.Errorf("未读角标没有画出来：%q", tt.Texts())
	}
}

// 面板排版：两个账号各占一行（不是叠在一起），动作按钮与底部的「添加账号」
// 都在窗口内，没有被裁掉。
func TestPanelViewLayoutFitsTheWindow(t *testing.T) {
	b := testBook(t)
	jimmy := addTestAccount(t, b, "jimmy")
	amy := addTestAccount(t, b, "amy")
	setTestUnread(b, jimmy.ID, 3)
	_ = amy

	tt := ui.NewTester(b.panelView, accountsWindowWidth, accountsWindowHeight)
	tt.Frame()
	first, ok := tt.Find("jimmy")
	if !ok {
		t.Fatalf("找不到第一行账号：%q", tt.Texts())
	}
	second, ok := tt.Find("amy")
	if !ok {
		t.Fatalf("找不到第二行账号：%q", tt.Texts())
	}
	if second.Y <= first.Y {
		t.Errorf("两个账号行叠在一起：jimmy y=%v，amy y=%v", first.Y, second.Y)
	}
	add, ok := tt.Find("添加账号")
	if !ok {
		t.Fatalf("找不到「添加账号」：%q", tt.Texts())
	}
	if add.Y <= second.Y {
		t.Errorf("「添加账号」应在账号行下面：y=%v（第二行 y=%v）", add.Y, second.Y)
	}
	if add.Y+add.H > float32(accountsWindowHeight) {
		t.Errorf("「添加账号」被窗口裁掉了：y=%v h=%v", add.Y, add.H)
	}
	for _, label := range []string{"jimmy", "amy", "打开", "退出", "添加账号"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Errorf("找不到 %q", label)
			continue
		}
		if r.X < 0 || r.X+r.W > float32(accountsWindowWidth) {
			t.Errorf("%q 超出窗口宽度：x=%v w=%v", label, r.X, r.W)
		}
	}
}

// 一个账号都没有时，面板要说清怎么开始，而不是留一屏空白。
func TestPanelViewExplainsHowToAddFirstAccount(t *testing.T) {
	b := testBook(t)
	tt := ui.NewTester(b.panelView, accountsWindowWidth, accountsWindowHeight)
	tt.Frame()
	for _, want := range []string{"还没有登录账号", "添加账号", "未登录"} {
		if !tt.HasText(want) {
			t.Errorf("空账号面板缺少文案 %q：%q", want, tt.Texts())
		}
	}
}

// 面板上的动作真的接上了：点「打开」记下最近使用的账号，点「退出」先问一句，
// 确认后这个账号从账号册里消失、面板回到空状态。
func TestPanelActionsOpenAndLogout(t *testing.T) {
	b := testBook(t)
	acc := addTestAccount(t, b, "jimmy")
	tt := ui.NewTester(b.panelView, accountsWindowWidth, accountsWindowHeight)
	tt.Frame()

	if err := tt.Click("打开"); err != nil {
		t.Fatal(err)
	}
	if got := b.activeAccountID(); got != acc.ID {
		t.Fatalf("点「打开」后最近使用的账号 = %q，期望 %q", got, acc.ID)
	}

	if err := tt.Click("退出"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if b.confirmOut != acc.ID {
		t.Fatalf("点「退出」没有进入确认：%q", b.confirmOut)
	}
	if !tt.HasText("退出账号？") {
		t.Fatalf("确认框没有出现：%q", tt.Texts())
	}

	if err := tt.Click("确认退出"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if b.accountCount() != 0 {
		t.Fatalf("确认退出后账号还在：%d", b.accountCount())
	}
	if !tt.HasText("还没有登录账号") {
		t.Errorf("退出最后一个账号后面板没有回到空状态：%q", tt.Texts())
	}
}

// 正在等浏览器授权时，面板给出粘贴回调与取消两个兜底动作（系统没注册
// 回调协议时这是唯一能走通的路）。
func TestPanelViewShowsPendingAuthorization(t *testing.T) {
	srv := newTestTokenServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"T","refresh_token":"R","token_type":"Bearer","expires_in":3600}}`)
	})
	b := testBook(t)
	m := newAuthManagerFor(b.dir, b.proxy, legacyTokenKey)
	m.apiBase = srv.URL + "/api"
	m.http = srv.Client()
	m.store = &memoryTokenStore{}
	m.onChange = func(err error) { b.finishAddAccount(m, err) }
	if _, err := m.BeginScopes(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.pending = &accountLogin{auth: m}
	b.loginMsg = "已在浏览器打开授权页，同意后会自动跳回。"
	b.mu.Unlock()
	t.Cleanup(func() { b.cancelAddAccount() })

	tt := ui.NewTester(b.panelView, accountsWindowWidth, accountsWindowHeight)
	tt.Frame()
	for _, want := range []string{"等待浏览器授权", "回调链接", "用回调链接完成", "取消授权"} {
		if !tt.HasText(want) {
			t.Errorf("等待授权的面板缺少文案 %q：%q", want, tt.Texts())
		}
	}
}

// 取消授权之后不能再认领迟到的回调：浏览器里那条链接会变成「没有对应的
// 授权」。
func TestCancelAddAccountRejectsLateCallback(t *testing.T) {
	b := testBook(t)
	m := newAuthManagerFor(b.dir, b.proxy, legacyTokenKey)
	m.store = &memoryTokenStore{}
	if _, err := m.BeginScopes(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.pending = &accountLogin{auth: m}
	b.mu.Unlock()
	b.cancelAddAccount()
	if b.pendingLogin() != nil {
		t.Error("取消后不该还挂着 pending")
	}
	if !strings.Contains(b.loginStatus(), "已取消") {
		t.Errorf("提示 = %q", b.loginStatus())
	}
}

// 账号册的汇总文案：几个账号、一共几条未读。
func TestAccountSummary(t *testing.T) {
	b := testBook(t)
	if got := b.summary(); got != "未登录" {
		t.Errorf("空账号册的汇总 = %q", got)
	}
	jimmy := addTestAccount(t, b, "jimmy")
	addTestAccount(t, b, "amy")
	setTestUnread(b, jimmy.ID, 2)
	if got := b.summary(); got != "2 个账号，2 条未读" {
		t.Errorf("汇总 = %q", got)
	}
}

// 同时开出多个账号窗口时位置要错开：完全重叠的话，看起来就像只开了一个。
func TestWindowOriginsAreStaggered(t *testing.T) {
	b := testBook(t)
	first := addTestAccount(t, b, "jimmy")
	second := addTestAccount(t, b, "amy")
	x1, y1 := b.windowOrigin(first.ID)
	x2, y2 := b.windowOrigin(second.ID)
	if x1 == x2 && y1 == y2 {
		t.Fatalf("两个账号窗口位置相同：%d,%d", x1, y1)
	}
	if x, y := b.windowOrigin(""); x != 0 || y != 0 {
		t.Errorf("未登录窗口用系统默认位置，实际 %d,%d", x, y)
	}
}

// 索引里有一条账号、凭据却没了（用户手动删了凭据库条目）：面板据此显示
// 「登录已失效」，而不是拿空令牌去请求。
func TestAccountWithoutTokenNeedsLogin(t *testing.T) {
	dir := t.TempDir()
	b := newAccountBook(dir)
	addTestAccount(t, b, "jimmy")
	if err := os.Remove(filepath.Join(dir, "token-"+accountIDFor("jimmy")+".json")); err != nil {
		t.Fatal(err)
	}
	restarted := newAccountBook(dir)
	if got := restarted.accountCount(); got != 1 {
		t.Fatalf("账号索引里的账号应留着，账号数 = %d", got)
	}
	if !restarted.accountsView()[0].NeedsLogin {
		t.Error("没有令牌的账号应标成需要重新登录")
	}
}

// 账号索引损坏时不能挡住启动：另存一份 .bad 副本，按空账号册继续。
func TestBrokenAccountsIndexDoesNotBlockStartup(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, accountsFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newAccountBook(dir)
	if got := b.accountCount(); got != 0 {
		t.Fatalf("损坏的索引应按空账号册处理，账号数 = %d", got)
	}
	if _, err := os.Stat(filepath.Join(dir, accountsFileName+".bad")); err != nil {
		t.Errorf("损坏的索引应另存一份副本：%v", err)
	}
}

// 主索引坏了但上一次写成功的备份还在：账号列表从备份读回来，不用逐个重新
// 授权；坏文件挪成 .bad，备份本身不被下一次保存覆盖掉。
func TestBrokenAccountsIndexRecoversFromBackup(t *testing.T) {
	dir := t.TempDir()
	b := newAccountBook(dir)
	addTestAccount(t, b, "jimmy")
	addTestAccount(t, b, "amy")
	// 再存一次，让 .bak 拿到完整的两条记录。
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(accountsBackupPath(filepath.Join(dir, accountsFileName))); err != nil {
		t.Fatalf("前置条件：应写出备份：%v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, accountsFileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	restarted := newAccountBook(dir)
	if got := restarted.accountCount(); got != 2 {
		t.Fatalf("从备份恢复后账号数 = %d，期望 2", got)
	}
	if restarted.accountByUsername("jimmy") == nil {
		t.Error("备份里的 jimmy 没有恢复")
	}
	if _, err := os.Stat(filepath.Join(dir, accountsFileName+".bad")); err != nil {
		t.Errorf("坏文件应挪成 .bad：%v", err)
	}
	// 恢复后主文件是合法的：再启动一次走的还是主文件。
	var f accountsFile
	if err := readJSONFile(filepath.Join(dir, accountsFileName), &f); err != nil {
		t.Errorf("恢复后主索引仍不可读：%v", err)
	}
}

// 账号索引落盘的形状：只有展示信息，令牌绝不进这个文件。
func TestAccountsIndexHoldsNoTokens(t *testing.T) {
	dir := t.TempDir()
	b := newAccountBook(dir)
	addTestAccount(t, b, "jimmy")
	raw, err := os.ReadFile(filepath.Join(dir, accountsFileName))
	if err != nil {
		t.Fatal(err)
	}
	var f accountsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Accounts) != 1 || f.Accounts[0].Username != "jimmy" {
		t.Fatalf("索引内容不对：%+v", f)
	}
	for _, secret := range []string{"tok-jimmy", "r-jimmy", "access_token"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("账号索引里不该出现令牌内容 %q", secret)
		}
	}
}
