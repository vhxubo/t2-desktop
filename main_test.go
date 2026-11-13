package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func testApp(t *testing.T) *app {
	t.Helper()
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false // 测试中不发真实网络请求
	return a
}

// The tabs must hit exactly the documented endpoints and page-1 queries.
func TestTabEndpointsMatchContract(t *testing.T) {
	want := []string{
		"https://2libra.com/api/v1/topics?page=1&limit=50&parent_slug=All&sort=value",
		"https://2libra.com/api/v1/topics/hot",
		"https://2libra.com/api/v1/topics/hot?period=7d",
		"https://2libra.com/api/v1/topics/latest",
		"https://2libra.com/api/v1/comments/latest?limit=50",
		"https://2libra.com/api/v1/topics?page=1&limit=50&node_id=" + communityNodeID,
		"https://2libra.com/api/v1/me/follows/timeline?page=1&limit=50",
		"https://2libra.com/api/v1/me/notifications?page=1&limit=50",
	}
	if len(tabSpecs) != len(want) || len(tabLabels()) != 8 {
		t.Fatalf("tab 数量不对：%d", len(tabSpecs))
	}
	if len(tabIcons) != len(tabSpecs) {
		t.Fatalf("tabIcons 与 tabSpecs 数量不一致：%d / %d", len(tabIcons), len(tabSpecs))
	}
	for i, s := range tabSpecs {
		if got := endpoint(apiBaseURL, s, 1); got != want[i] {
			t.Errorf("tab %s 第 1 页 URL = %s，期望 %s", s.Label, got, want[i])
		}
		if i >= 6 && !s.NeedsAuth {
			t.Errorf("tab %s 应标记为需要登录", s.Label)
		}
		if i < 6 && s.NeedsAuth {
			t.Errorf("tab %s 是匿名可读端点", s.Label)
		}
	}
	// 第 2 页才带 page；不引入其它参数。
	cases := []struct {
		i    int
		want string
	}{
		{0, "https://2libra.com/api/v1/topics?limit=50&page=2&parent_slug=All&sort=value"},
		{1, "https://2libra.com/api/v1/topics/hot?page=2"},
		{2, "https://2libra.com/api/v1/topics/hot?page=2&period=7d"},
		{3, "https://2libra.com/api/v1/topics/latest?page=2"},
		{6, "https://2libra.com/api/v1/me/follows/timeline?limit=50&page=2"},
		{7, "https://2libra.com/api/v1/me/notifications?limit=50&page=2"},
	}
	for _, c := range cases {
		if got := endpoint(apiBaseURL, tabSpecs[c.i], 2); got != c.want {
			t.Errorf("tab %s 第 2 页 URL = %s，期望 %s", tabSpecs[c.i].Label, got, c.want)
		}
	}
	if tabSpecs[4].Paging != pagingIgnored || tabSpecs[4].ListKey != "items" {
		t.Errorf("最新评论必须用 items 且不翻页：%+v", tabSpecs[4])
	}
	if tabSpecs[7].Kind != kindNotification || tabSpecs[7].ListKey != "list" {
		t.Errorf("通知的列表字段是 list：%+v", tabSpecs[7])
	}
	// 收藏 / 书签 / 我的帖子 / 我的合集与金币页都不在侧边栏：前四类在「我的」
	// 窗口的「我的内容」里，金币页是独立的金币窗口。
	for i, s := range tabSpecs {
		switch s.Path {
		case "/v1/me/favorites", "/v1/me/bookmarks", "/v1/me/topics",
			"/v1/me/collections", "/v1/me/coins/balance":
			t.Errorf("tab %d（%s）不该还留在侧边栏里", i, s.Label)
		}
	}
}

func envelopeJSON(d string) string {
	return fmt.Sprintf(`{"c":0,"m":"请求成功","d":%s,"t":1730000000000}`, d)
}

// Each tab declares its own list field; do not fall back to a generic
// "take items" getter for endpoints whose field is documented.
func TestDecodePageListFields(t *testing.T) {
	post := `{"short_id":"JBYGf2A","title":"拿到了 offer","author":{"username":"bighammer"},` +
		`"node":{"slug":"workplace-stories","name":"职场那些事"},"created_at":"2026-11-11T01:36:24.624Z",` +
		`"comment_count":64,"views":340,"score":12}`
	posts := envelopeJSON(`{"posts":[` + post + `],"total":8709,"page":1,"limit":50,"total_pages":175}`)
	var env envelope
	if err := json.Unmarshal([]byte(posts), &env); err != nil {
		t.Fatal(err)
	}
	res, err := decodePage(env.D, tabSpecs[0], 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Total != 8709 || res.TotalPages != 175 {
		t.Fatalf("posts 解析结果：%+v %+v", res.Items, res)
	}
	it := res.Items[0]
	if it.Title != "拿到了 offer" || it.Key != "JBYGf2A" {
		t.Errorf("帖子字段错误：%+v", it)
	}
	if it.URL != "https://2libra.com/post/workplace-stories/JBYGf2A" {
		t.Errorf("帖子链接错误：%s", it.URL)
	}
	if !strings.Contains(it.Meta, "64 回复") || !strings.Contains(it.Meta, "价值 12") {
		t.Errorf("帖子摘要错误：%s", it.Meta)
	}

	// 一个只有 items 的响应，用 posts 的 tab 解析时必须为空。
	itemsOnly := `{"items":[` + post + `]}`
	res, err = decodePage([]byte(itemsOnly), tabSpecs[0], 1)
	if err != nil || len(res.Items) != 0 {
		t.Errorf("posts tab 不应去读 items：%d 条 %v", len(res.Items), err)
	}

	// 最新评论：items + 评论正文 + 所属帖子信息 + commentId 链接
	comment := `{"id":"f86db611","floor":12,"content":"有啥看啥:feel_wronged:",` +
		`"author":{"username":"dannyng"},"post":{"short_id":"wEOIDnn","title":"社会观察","node":{"slug":"social-observation","name":"社会观察"}}}`
	var env2 envelope
	if err := json.Unmarshal([]byte(envelopeJSON(`{"items":[`+comment+`],"total":50,"page":1,"limit":50,"total_pages":1}`)), &env2); err != nil {
		t.Fatal(err)
	}
	res, err = decodePage(env2.D, tabSpecs[4], 1)
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("评论解析失败：%v %+v", err, res.Items)
	}
	ci := res.Items[0]
	if ci.Title != "有啥看啥" {
		t.Errorf("表情短码未清理：%q", ci.Title)
	}
	if !strings.Contains(ci.Subtitle, "dannyng") || !strings.Contains(ci.Subtitle, "社会观察") {
		t.Errorf("评论副标题错误：%q", ci.Subtitle)
	}
	if ci.URL != "https://2libra.com/post/social-observation/wEOIDnn?commentId=f86db611" {
		t.Errorf("评论链接错误：%s", ci.URL)
	}

	// 我的合集：d 本身就是数组，不分页
	var env3 envelope
	if err := json.Unmarshal([]byte(envelopeJSON(`[{"id":"c1","name":"我的收藏夹","topic_count":9}]`)), &env3); err != nil {
		t.Fatal(err)
	}
	res, err = decodePage(env3.D, profileContentSpecs[profileContentCollections], 1)
	if err != nil || len(res.Items) != 1 || res.Items[0].Title != "我的收藏夹" {
		t.Fatalf("合集数组解析失败：%v %+v", err, res.Items)
	}
	if !strings.Contains(res.Items[0].Meta, "9 个帖子") {
		t.Errorf("合集数量错误：%s", res.Items[0].Meta)
	}

	// 我的帖子：文档没约定字段名，允许 auto 兜底
	var env4 envelope
	if err := json.Unmarshal([]byte(envelopeJSON(`{"topics":[`+post+`]}`)), &env4); err != nil {
		t.Fatal(err)
	}
	res, err = decodePage(env4.D, profileContentSpecs[profileContentTopics], 1)
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("auto 列表解析失败：%v %+v", err, res.Items)
	}
}

func TestClientRequestAndErrorCodes(t *testing.T) {
	var path, query, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query, auth = r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, envelopeJSON(`{"posts":[{"short_id":"ABC12345","title":"测试帖","node":{"slug":"ai-tools","name":"AI 工具"}}],"total":1,"page":1,"limit":50,"total_pages":1}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	res, err := c.fetch(context.Background(), tabSpecs[0], 1)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/topics" || query != "page=1&limit=50&parent_slug=All&sort=value" {
		t.Errorf("请求路径不对：%s?%s", path, query)
	}
	if auth != "Bearer tok" {
		t.Errorf("Authorization 头不对：%q", auth)
	}
	if len(res.Items) != 1 || res.Items[0].URL != "https://2libra.com/post/ai-tools/ABC12345" {
		t.Errorf("解析结果不对：%+v", res.Items)
	}

	// 匿名请求不能带空的 Authorization 头
	c.bearer = func() string { return "" }
	_, _ = c.fetch(context.Background(), tabSpecs[1], 1)
	if auth != "" {
		t.Errorf("匿名请求带了 Authorization 头：%q", auth)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","m":"缺少权限范围","required_scope":["read:me"],"d":null}`)
	}))
	defer errSrv.Close()
	c2 := &Client{HTTP: errSrv.Client(), Base: errSrv.URL + "/api"}
	_, err = c2.fetch(context.Background(), tabSpecs[7], 1)
	if err == nil {
		t.Fatal("403 应返回错误")
	}
	if !strings.Contains(friendlyError(err), "read:me") || !strings.Contains(friendlyError(err), "重新授权") {
		t.Errorf("错误提示不对：%s", friendlyError(err))
	}
}

func TestPKCE(t *testing.T) {
	v, ch := newPKCE()
	if len(v) < 43 || len(v) > 128 || len(ch) != 43 {
		t.Fatalf("PKCE 长度不对：verifier=%d challenge=%d", len(v), len(ch))
	}
	sum := sha256.Sum256([]byte(v))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != ch {
		t.Fatal("code_challenge 不是 S256(verifier)")
	}
	if randomURLSafe(16) == randomURLSafe(16) {
		t.Fatal("state 不是随机串")
	}
}

func TestRefreshRotatesTokenOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth/token" {
			t.Errorf("意外的路径 %s", r.URL.Path)
		}
		calls.Add(1)
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_secret") != "" {
			t.Errorf("刷新请求参数不对（公开客户端不应带 secret）：%v", r.Form)
		}
		_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"new","refresh_token":"r2","token_type":"Bearer","expires_in":3600,"scope":"read"}}`)
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := newAuthManager(dir)
	m.apiBase = srv.URL + "/api"
	m.http = srv.Client()
	if err := m.storeToken(tokenData{AccessToken: "old", RefreshToken: "r1", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	got := make([]string, 4)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, err := m.refresh(context.Background(), "old")
			if err != nil {
				t.Errorf("刷新失败：%v", err)
			}
			got[i] = tok
		}(i)
	}
	wg.Wait()
	for _, g := range got {
		if g != "new" {
			t.Errorf("并发刷新结果不对：%q", g)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("刷新请求应只发一次，实际 %d 次", calls.Load())
	}
	var saved tokenData
	if err := readJSONFile(filepath.Join(dir, "token.json"), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.RefreshToken != "r2" || saved.AccessToken != "new" {
		t.Errorf("轮换后的令牌未落盘：%+v", saved)
	}
}

func TestLogoutClearsToken(t *testing.T) {
	dir := t.TempDir()
	m := newAuthManager(dir)
	if err := m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	if !m.loggedIn() {
		t.Fatal("令牌未生效")
	}
	m.Logout()
	if m.loggedIn() {
		t.Error("退出后仍有令牌")
	}
	if _, err := os.Stat(filepath.Join(dir, "token.json")); !os.IsNotExist(err) {
		t.Error("退出后令牌文件未删除")
	}
}

// Optional live smoke test: TWOLIBRA_LIVE=1 go test -run TestLivePublicTabs -v
func TestLivePublicTabs(t *testing.T) {
	if os.Getenv("TWOLIBRA_LIVE") == "" {
		t.Skip("设置 TWOLIBRA_LIVE=1 才会访问真实 API")
	}
	c := &Client{}
	for _, s := range tabSpecs[:6] {
		res, err := c.fetch(context.Background(), s, 1)
		if err != nil {
			t.Errorf("tab %s 请求失败：%v", s.Label, err)
			continue
		}
		if len(res.Items) == 0 {
			t.Errorf("tab %s 返回 0 条", s.Label)
		}
		t.Logf("%-6s %3d 条 / 共 %d 条，首条：%s", s.Label, len(res.Items), res.Total, res.Items[0].Title)
	}
}

// The native view runs headless: tab switching, the login gate on private
// tabs and the list rows are all exercised here. The old login settings page
// is gone; the sidebar's 设置 now opens the separate settings window that
// holds the proxy, update and image-cache sections.
func TestNativeView(t *testing.T) {
	a := testApp(t)
	a.tabs[0].items = []Item{
		{Key: "a", Title: "测试帖子标题", Subtitle: "职场那些事 · bighammer · 刚刚", Meta: "64 回复", URL: "https://2libra.com/post/workplace-stories/JBYGf2A"},
	}
	a.tabs[0].loaded = true
	a.tabs[0].page = 1
	a.tabs[0].total = 1
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("测试帖子标题") || !tt.HasText("职场那些事 · bighammer · 刚刚") {
		t.Fatalf("列表行没有渲染：%q", tt.Texts())
	}
	if err := tt.Click("近期热议"); err != nil {
		t.Fatal(err)
	}
	if a.tab != 2 {
		t.Errorf("点击 tab 后选中 %d，期望 2", a.tab)
	}
	if err := tt.Click("动态"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("「动态」需要登录") {
		t.Errorf("私人 tab 未显示登录引导：%q", tt.Texts())
	}
	// 授权弹窗盖在整页上，先取消它再点标题栏。
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// 侧边栏的「设置」打开独立设置窗口；登录配置的那个设置页不会回来。
	opened := false
	a.onSettingsOpen = func() { opened = true }
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("点击「设置」失败：%v", err)
	}
	if !opened {
		t.Errorf("点击「设置」没有打开设置窗口：%q", tt.Texts())
	}
	if tt.HasText("设置 · OAuth 公开客户端（PKCE S256）") {
		t.Errorf("登录配置的设置页又出现了：%q", tt.Texts())
	}
}

// The packaged app must declare the same id as the Go code, and register
// its custom scheme so the OS can hand the callback URL back to the app.
// 续拉失败时不能把已经加载出来的列表换成整页错误：列表保持不动，只在
// 底部提示失败原因并给出「重试」。
func TestLoadMoreFailureKeepsRows(t *testing.T) {
	a := loadMoreApp(t)
	a.tabs[0].moreErr = "服务端错误（500），请稍后重试"
	tt := ui.NewTester(a.view, 1100, 760)
	for _, want := range []string{"列表项一", "列表项二", "加载更多失败", "重试"} {
		if !tt.HasText(want) {
			t.Errorf("续拉失败时缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 续拉进行中，列表仍可见，底部显示进度而不是空白页。
func TestLoadMoreShowsProgress(t *testing.T) {
	a := loadMoreApp(t)
	a.tabs[0].loading = true
	tt := ui.NewTester(a.view, 1100, 760)
	for _, want := range []string{"列表项一", "正在加载更多…"} {
		if !tt.HasText(want) {
			t.Errorf("续拉进度缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 点「重试」按失败那一页续拉，新内容接在已有列表之后，原有行不丢。
func TestLoadMoreRetryAppendsPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"c":0,"m":"请求成功","d":{
			"posts":[{"id":"new-1","short_id":"NEW001","title":"续拉的新内容",
				"author":{"username":"jimmy"}}],
			"total":3,"page":2,"limit":50,"total_pages":2}}`)
	}))
	defer srv.Close()

	a := loadMoreApp(t)
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.tabs[0].moreErr = "服务端错误（500），请稍后重试"

	tt := ui.NewTester(a.view, 1100, 760)
	if err := tt.Click("重试"); err != nil {
		t.Fatalf("点击重试失败：%v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !tt.HasText("续拉的新内容") {
		if time.Now().After(deadline) {
			t.Fatalf("重试后没有加载出第 2 页：%q", tt.Texts())
		}
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if !tt.HasText("列表项一") || !tt.HasText("列表项二") {
		t.Errorf("续拉后原有行丢失：%q", tt.Texts())
	}
}

// loadMoreApp 造一个已有第 1 页、正在/曾经续拉第 2 页的首页 tab。
func loadMoreApp(t *testing.T) *app {
	t.Helper()
	a := testApp(t)
	st := a.tabs[0]
	st.items = []Item{
		{Key: "a", Title: "列表项一"},
		{Key: "b", Title: "列表项二"},
	}
	st.loaded = true
	st.total = 3
	st.page = 2
	st.totalPages = 2
	return a
}

func TestAppConfigDeclaresNativeScheme(t *testing.T) {
	b, err := os.ReadFile("mygo.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Identifier string   `json:"identifier"`
		URLSchemes []string `json:"urlSchemes"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Identifier != appIdentifier {
		t.Errorf("mygo.json identifier = %q，期望 %q", cfg.Identifier, appIdentifier)
	}
	if len(cfg.URLSchemes) != 1 || cfg.URLSchemes[0] != appIdentifier {
		t.Errorf("mygo.json urlSchemes = %v，期望 [%s]", cfg.URLSchemes, appIdentifier)
	}
	if defaultRedirectURI != "com.vhxubo.t2-desktop:/callback" {
		t.Errorf("回调地址不是原生/桌面端形式：%s", defaultRedirectURI)
	}
}

// There must be no second login path hiding in the data model: no
// configurable client_id/scope, no redirect/port, no local server, no
// manual-token entry point.
func TestSingleLoginFlowStructure(t *testing.T) {
	if clientID != "e471281af4d84ecfae77c39f94bcf372" {
		t.Fatalf("client_id 未固定为项目内的常量：%q", clientID)
	}
	if _, ok := reflect.TypeOf(AuthManager{}).FieldByName("settings"); ok {
		t.Error("AuthManager 仍保存可变设置")
	}
	if _, ok := reflect.TypeOf(&AuthManager{}).MethodByName("updateSettings"); ok {
		t.Error("仍能通过 updateSettings 改写 client_id / scope")
	}
	if _, ok := reflect.TypeOf(app{}).FieldByName("draft"); ok {
		t.Error("UI 状态里仍有设置页草稿")
	}
	if _, ok := reflect.TypeOf(&app{}).MethodByName("settingsView"); ok {
		t.Error("设置页 settingsView 仍在")
	}
	for _, banned := range []string{"server", "redirectURI", "clientID"} {
		if _, ok := reflect.TypeOf(pendingLogin{}).FieldByName(banned); ok {
			t.Errorf("pendingLogin 仍有 %s，说明配置/回环监听回来了", banned)
		}
	}
	if _, ok := reflect.TypeOf(&AuthManager{}).MethodByName("LoginWithToken"); ok {
		t.Error("仍存在手动令牌登录入口 LoginWithToken")
	}
	if _, ok := reflect.TypeOf(app{}).FieldByName("manualToken"); ok {
		t.Error("UI 状态里仍有手动令牌输入框")
	}
}

// client_id / scope / redirect_uri are compile-time constants. Old builds
// wrote settings.json and honoured env overrides; none of that may change
// the authorize URL any more.
func TestHardcodedClientIDIgnoresLegacyConfig(t *testing.T) {
	t.Setenv("TWOLIBRA_CLIENT_ID", "env-client")
	t.Setenv("TWOLIBRA_SCOPES", "read")
	t.Setenv("TWOLIBRA_REDIRECT_URI", "http://127.0.0.1:9999/cb")
	t.Setenv("TWOLIBRA_OAUTH_PORT", "9999")

	dir := t.TempDir()
	legacy := `{"client_id":"cid","scopes":"read","redirect_uri":"http://127.0.0.1:8765/cb","port":8765}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
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
	q := u.Query()
	if q.Get("client_id") != clientID || q.Get("scope") != defaultScopes ||
		q.Get("redirect_uri") != defaultRedirectURI {
		t.Errorf("授权参数被旧配置或环境变量改写：%v", q)
	}
	if defaultScopes != "read profile read:me write:comment interact read:economy" {
		t.Errorf("scope 常量被改动：%s", defaultScopes)
	}

	fresh := t.TempDir()
	_ = newAuthManager(fresh)
	if _, err := os.Stat(filepath.Join(fresh, "settings.json")); !os.IsNotExist(err) {
		t.Error("仍在写 settings.json，说明配置层没有移除干净")
	}
}

func TestCallbackLinkValidation(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{defaultRedirectURI + "?code=c&state=s", true},
		{defaultRedirectURI, true},
		{alternateCallbackURI + "?code=c&state=s", true},
		{"http://127.0.0.1/cb?code=c&state=s", false},
		{"http://127.0.0.1:8765/cb?code=c&state=s", false},
		{"http://localhost:8765/cb?code=c&state=s", false},
		{"https://evil.example/cb?code=c&state=s", false},
		{"com.other.app:/callback?code=c&state=s", false},
		{appIdentifier + ":/other?code=c&state=s", false},
		{appIdentifier + "://other?code=c&state=s", false},
		{appIdentifier + ":/callback/extra?code=c&state=s", false},
		{"", false},
		{"not a url", false},
	}
	for _, c := range cases {
		if got := isCallbackLink(c.raw); got != c.want {
			t.Errorf("isCallbackLink(%q) = %v，期望 %v", c.raw, got, c.want)
		}
	}
}

func TestCompleteRejectsForeignCallbacks(t *testing.T) {
	m := newAuthManager(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := m.Begin(ctx); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"https://evil.example/cb?code=x&state=y",
		"com.other.app:/callback?code=x&state=y",
		appIdentifier + ":/other?code=x&state=y",
		"http://127.0.0.1:8765/cb?code=x&state=y",
		"http://127.0.0.1/cb?code=x&state=y",
	} {
		if err := m.Complete(raw); err == nil {
			t.Errorf("不该接受回调链接 %s", raw)
		}
	}
}

// Full native-flow round trip: authorize URL -> system deep link -> token
// exchange. No loopback listener, port or second login path is involved.
func TestOAuthNativeSchemeFlow(t *testing.T) {
	var gotForm atomic.Value
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			_ = r.ParseForm()
			gotForm.Store(r.Form.Encode())
			_, _ = fmt.Fprint(w, `{"c":0,"m":"请求成功","d":{"access_token":"NATIVE","refresh_token":"R","token_type":"Bearer","expires_in":3600,"scope":"read profile read:me write:comment"}}`)
		case "/api/oauth/me", "/api/v1/me/summary":
			_, _ = fmt.Fprint(w, `{"c":0,"d":{"username":"jimmy"}}`)
		default:
			t.Errorf("意外路径 %s", r.URL.Path)
		}
	}))
	defer api.Close()

	dir := t.TempDir()
	m := newAuthManager(dir)
	m.apiBase = api.URL + "/api"
	m.http = api.Client()
	done := make(chan error, 1)
	m.onChange = func(err error) {
		select {
		case done <- err:
		default:
		}
	}
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
	q := u.Query()
	if q.Get("redirect_uri") != defaultRedirectURI || q.Get("client_id") != clientID ||
		q.Get("scope") != defaultScopes || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
		t.Fatalf("授权参数不对：%v", q)
	}
	if strings.Contains(defaultRedirectURI, "127.0.0.1") || strings.Contains(defaultRedirectURI, "http") {
		t.Fatalf("回调地址里混入了回环形式：%s", defaultRedirectURI)
	}

	callback := defaultRedirectURI + "?code=native-code&state=" + q.Get("state")
	if !isCallbackLink(callback) {
		t.Fatal("系统回调链接未被识别")
	}
	if err := m.Complete(callback); err != nil {
		t.Fatal(err)
	}
	select {
	case loginErr := <-done:
		if loginErr != nil {
			t.Fatalf("登录失败：%v", loginErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("登录回调超时")
	}
	if !m.loggedIn() || m.accessToken() != "NATIVE" || m.username() != "jimmy" {
		t.Fatalf("登录态不对：%+v", m.token)
	}
	form, _ := gotForm.Load().(string)
	vals, err := url.ParseQuery(form)
	if err != nil {
		t.Fatal(err)
	}
	if vals.Get("grant_type") != "authorization_code" || vals.Get("code") != "native-code" ||
		vals.Get("redirect_uri") != defaultRedirectURI || vals.Get("client_id") != clientID ||
		vals.Get("code_verifier") == "" || vals.Get("client_secret") != "" {
		t.Errorf("换 token 的表单不对：%s", form)
	}
}

func TestDeepLinkColdStart(t *testing.T) {
	a := testApp(t)
	// A callback that launched a fresh process has no PKCE verifier here.
	a.handleCallbackURL(defaultRedirectURI + "?code=x&state=y")
	if a.pageName != "login" || !strings.Contains(a.loginMsg, "重新发起") {
		t.Errorf("冷启动回调应提示重新登录：%q / %s", a.pageName, a.loginMsg)
	}
	// Deep links that are not the native OAuth callback are ignored.
	a.pageName = "list"
	a.handleCallbackURL(appIdentifier + ":/something-else")
	if a.pageName != "list" {
		t.Errorf("无关 deep link 不该切页：%s", a.pageName)
	}
	a.handleCallbackURL("http://127.0.0.1:8765/cb?code=x&state=y")
	if a.pageName != "list" {
		t.Error("回环回调不该被当成系统回调处理")
	}
}

// A delivered link that carries a code but does not match the registered
// callback must say what arrived: silently dropping it leaves the user on the
// login page with no idea whether the system handed anything over at all.
func TestDeepLinkWrongCallbackSpellingReports(t *testing.T) {
	a := testApp(t)
	a.pageName = "list"
	a.handleCallbackURL(appIdentifier + ":/callback/?code=x&state=y")
	if a.pageName != "login" {
		t.Fatalf("带 code 的非标准回调应切到登录页：%s", a.pageName)
	}
	if !strings.Contains(a.loginMsg, defaultRedirectURI) || !strings.Contains(a.loginMsg, "不一致") {
		t.Errorf("提示里应给出期望的回调地址：%q", a.loginMsg)
	}
	if a.status == "" {
		t.Error("状态栏也应留下同样的说明")
	}
}

// hasCallbackScheme separates "ours but unusable" from "another app's link",
// which decides whether a delivery is worth reporting.
func TestCallbackSchemeDetection(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{defaultRedirectURI + "?code=c&state=s", true},
		{alternateCallbackURI + "?code=c&state=s", true},
		{appIdentifier + ":/something-else", true},
		{"https://2libra.com/post/x", false},
		{"com.other.app:/callback?code=c", false},
		{"", false},
		{"not a url", false},
	}
	for _, c := range cases {
		if got := hasCallbackScheme(c.raw); got != c.want {
			t.Errorf("hasCallbackScheme(%q) = %v，期望 %v", c.raw, got, c.want)
		}
	}
}

// 系统没有把回调协议关联到本应用时，登录页必须说出来：否则用户看到的是
// 「浏览器授权完了，应用没有任何反应」。
func TestLoginPageReportsSchemeRegistrationFailure(t *testing.T) {
	a := testApp(t)
	a.pageName = "login"
	a.schemeWarn = "系统未把 " + appIdentifier + " 关联到本应用：浏览器不会自动跳回，请复制地址栏里的完整链接粘贴到下方。"
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText(a.schemeWarn) {
		t.Errorf("登录页没有显示协议注册失败的说明：%q", tt.Texts())
	}
}

// 登录回退页也要给未登录用户一条注册出路：除了「打开浏览器授权」之外，
// 还摆着「注册账号」，点它只打开带邀请码的注册页，不进入授权等待、不跳页。
func TestLoginPageOffersSignup(t *testing.T) {
	a := testApp(t)
	a.pageName = "login"
	tt := ui.NewTester(a.view, 1100, 760)
	if _, ok := tt.Find("注册账号"); !ok {
		t.Fatalf("登录页没有「注册账号」入口：%q", tt.Texts())
	}
	if err := tt.Click("注册账号"); err != nil {
		t.Fatal(err)
	}
	if a.pageName != "login" {
		t.Errorf("点「注册账号」不该跳离登录页，实际页面 = %q", a.pageName)
	}
	if a.loginPending {
		t.Error("点「注册账号」不该进入等待授权状态")
	}
}

// Both UI pages must offer only the native-scheme PKCE flow. The paste box
// on the login page belongs to that same flow (it takes the callback URL).
func TestOnlyNativeSchemeLoginIsOffered(t *testing.T) {
	a := testApp(t)
	banned := []string{
		"access_token", "用令牌登录", "粘贴令牌",
		"本地监听端口", "端口", "127.0.0.1", "8765", "回环",
	}
	for _, page := range []string{"list", "login"} {
		a.pageName = page
		tt := ui.NewTester(a.view, 1100, 760)
		texts := tt.Texts()
		for _, word := range banned {
			for _, text := range texts {
				if strings.Contains(text, word) {
					t.Errorf("%s 页仍出现其它登录方式的文案 %q：%q", page, word, text)
				}
			}
		}
		if page == "login" {
			if !tt.HasText("打开浏览器授权") || !tt.HasText("用回调链接完成登录") {
				t.Errorf("登录页缺少唯一流程的操作入口：%q", texts)
			}
			if !tt.HasText("本应用由第三方开发者独立开发运营，与 2Libra 无隶属或合作关系。") {
				t.Errorf("登录页缺少第三方声明：%q", texts)
			}
		}
	}
	// A stale pageName must not resurrect the removed login settings page.
	// 设置窗口只放代理、更新与图片缓存，不含任何 OAuth 配置。
	a.pageName = "settings"
	tt := ui.NewTester(a.view, 1100, 760)
	if tt.HasText("设置 · OAuth 公开客户端（PKCE S256）") {
		t.Errorf("登录配置的设置页仍在渲染：%q", tt.Texts())
	}
}
