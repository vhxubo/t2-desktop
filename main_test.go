package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	t.Setenv("TWOLIBRA_CLIENT_ID", "")
	t.Setenv("TWOLIBRA_REDIRECT_URI", "")
	t.Setenv("TWOLIBRA_SCOPES", "")
	t.Setenv("TWOLIBRA_OAUTH_PORT", "")
	a := newApp(dir)
	a.autoLoad = false // 测试中不发真实网络请求
	return a
}

// The 11 tabs must hit exactly the documented endpoints and page-1 queries.
func TestTabEndpointsMatchContract(t *testing.T) {
	want := []string{
		"https://2libra.com/api/v1/topics?page=1&limit=50&parent_slug=All&sort=value",
		"https://2libra.com/api/v1/topics/hot",
		"https://2libra.com/api/v1/topics/hot?period=7d",
		"https://2libra.com/api/v1/topics/latest",
		"https://2libra.com/api/v1/comments/latest?limit=50",
		"https://2libra.com/api/v1/topics?page=1&limit=50&node_id=" + communityNodeID,
		"https://2libra.com/api/v1/me/follows/timeline?page=1&limit=50",
		"https://2libra.com/api/v1/me/favorites?page=1&limit=50",
		"https://2libra.com/api/v1/me/bookmarks?page=1&limit=50",
		"https://2libra.com/api/v1/me/topics",
		"https://2libra.com/api/v1/me/collections",
	}
	if len(tabSpecs) != len(want) || len(tabLabels()) != 11 {
		t.Fatalf("tab 数量不对：%d", len(tabSpecs))
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
	}
	for _, c := range cases {
		if got := endpoint(apiBaseURL, tabSpecs[c.i], 2); got != c.want {
			t.Errorf("tab %s 第 2 页 URL = %s，期望 %s", tabSpecs[c.i].Label, got, c.want)
		}
	}
	if tabSpecs[4].Paging != pagingIgnored || tabSpecs[4].ListKey != "items" {
		t.Errorf("最新评论必须用 items 且不翻页：%+v", tabSpecs[4])
	}
	if tabSpecs[7].ListKey != "posts" || tabSpecs[8].ListKey != "items" {
		t.Errorf("收藏/书签的列表字段不符合约定")
	}
	if tabSpecs[10].ListKey != "" || tabSpecs[10].Paging != pagingOff {
		t.Errorf("我的合集响应是数组且不分页")
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
	res, err = decodePage(env3.D, tabSpecs[10], 1)
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
	res, err = decodePage(env4.D, tabSpecs[9], 1)
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

func TestLoginWithTokenVerifiesIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/me":
			_, _ = fmt.Fprint(w, `{"c":0,"d":{"username":"jimmy","level":4}}`)
		case "/api/v1/me/summary":
			if r.Header.Get("Authorization") != "Bearer tok" {
				t.Errorf("校验请求缺少令牌")
			}
			_, _ = fmt.Fprint(w, `{"c":0,"d":{"username":"jimmy"}}`)
		default:
			t.Errorf("意外路径 %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := newAuthManager(dir)
	m.apiBase = srv.URL + "/api"
	m.http = srv.Client()
	m.LoginWithToken(context.Background(), "Bearer tok")
	if !m.loggedIn() || m.username() != "jimmy" {
		t.Fatalf("登录态不对：%+v", m.token)
	}
	if !strings.Contains(m.verifyStatus(), "连通正常") {
		t.Errorf("连通性检查不对：%s", m.verifyStatus())
	}
	m.Logout()
	if m.loggedIn() {
		t.Error("退出后仍有令牌")
	}
	if _, err := os.Stat(filepath.Join(dir, "token.json")); !os.IsNotExist(err) {
		t.Error("退出后令牌文件未删除")
	}
}

// Full public-client PKCE round trip against a fake 2Libra server:
// authorize URL -> loopback callback -> token exchange in d -> identity.
func TestOAuthPKCELoopbackFlow(t *testing.T) {
	var gotForm atomic.Value
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			_ = r.ParseForm()
			gotForm.Store(r.Form.Encode())
			_, _ = fmt.Fprint(w, `{"c":0,"m":"请求成功","d":{"access_token":"ACCESS","refresh_token":"REFRESH","token_type":"Bearer","expires_in":3600,"scope":"read profile read:me"}}`)
		case "/api/oauth/me":
			_, _ = fmt.Fprint(w, `{"c":0,"d":{"username":"jimmy"}}`)
		case "/api/v1/me/summary":
			_, _ = fmt.Fprint(w, `{"c":0,"d":{"username":"jimmy"}}`)
		default:
			t.Errorf("意外路径 %s", r.URL.Path)
		}
	}))
	defer api.Close()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	dir := t.TempDir()
	m := newAuthManager(dir)
	m.apiBase = api.URL + "/api"
	m.http = api.Client()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/cb", port)
	if err := m.updateSettings(Settings{ClientID: "cid", RedirectURI: redirect, Scopes: "read profile read:me", Port: port}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	m.onChange = func(err error) {
		select {
		case done <- err:
		default:
		}
	}

	authorize, err := m.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(authorize)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Host != strings.TrimPrefix(api.URL, "http://") || u.Path != "/oauth/authorize" {
		t.Errorf("授权页地址不对：%s", authorize)
	}
	if q.Get("client_id") != "cid" || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || q.Get("scope") != "read profile read:me" ||
		q.Get("redirect_uri") != redirect || len(q.Get("code_challenge")) != 43 || q.Get("state") == "" {
		t.Errorf("授权参数不对：%v", q)
	}
	state := q.Get("state")
	callback := fmt.Sprintf("http://127.0.0.1:%d/cb?code=the-code&state=%s", port, state)
	resp, err := http.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	select {
	case loginErr := <-done:
		if loginErr != nil {
			t.Fatalf("登录失败：%v", loginErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("登录回调超时")
	}
	if !m.loggedIn() || m.accessToken() != "ACCESS" || m.username() != "jimmy" {
		t.Fatalf("登录态不对：%+v", m.token)
	}
	form, _ := gotForm.Load().(string)
	if !strings.Contains(form, "code=the-code") || !strings.Contains(form, "code_verifier=") ||
		strings.Contains(form, "client_secret") || !strings.Contains(form, "grant_type=authorization_code") {
		t.Errorf("换 token 的表单不对：%s", form)
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
// tabs, the settings page and the list rows are all exercised here.
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
	if err := tt.Click("收藏"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("「收藏」需要登录") {
		t.Errorf("私人 tab 未显示登录引导：%q", tt.Texts())
	}
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("设置 · OAuth 公开客户端（PKCE S256）") {
		t.Errorf("设置页没有渲染：%q", tt.Texts())
	}
	if err := tt.Click("← 返回"); err != nil {
		t.Fatal(err)
	}
	if a.pageName != "list" {
		t.Errorf("返回后仍在 %s 页", a.pageName)
	}
}
