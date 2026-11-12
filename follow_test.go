package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

func TestDecodeFollowFields(t *testing.T) {
	p := decodeUserProfile([]byte(`{"id":"u1","username":"jimmy","is_following":true}`))
	if p.ID != "u1" || p.Username != "jimmy" || !p.Known || !p.Following {
		t.Errorf("用户主页关注字段解析错误：%+v", p)
	}
	// 字段缺失时不能把它当成「未关注」。
	unknown := decodeUserProfile([]byte(`{"id":"u1","username":"jimmy"}`))
	if unknown.Known {
		t.Errorf("缺少 is_following 时不应断言关注状态：%+v", unknown)
	}

	topic, err := decodeTopic([]byte(`{"short_id":"X","title":"t","content":"x",
		"author":{"id":"u1","username":"jimmy","is_following":true}}`), "X")
	if err != nil {
		t.Fatal(err)
	}
	if topic.AuthorID != "u1" || !topic.FollowingKnown || !topic.Following {
		t.Errorf("作者对象里的关注字段解析错误：%+v", topic)
	}
}

// POST / DELETE /v1/users/:userId/follow 与用户主页读取都走既有写 / 读链路。
func TestClientFollowEndpoints(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.RequestURI()+" auth="+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/users/jimmy":
			_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"u1","username":"jimmy","is_following":true}`))
		case "/api/v1/users/u1/follow":
			_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
		default:
			t.Errorf("意外路径 %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	ctx := context.Background()

	prof, err := c.fetchUserProfile(ctx, "jimmy")
	if err != nil || prof.ID != "u1" || !prof.Following {
		t.Fatalf("读取关注状态失败：%v %+v", err, prof)
	}
	if err := c.setFollow(ctx, "u1", true); err != nil {
		t.Fatalf("关注失败：%v", err)
	}
	if err := c.setFollow(ctx, "u1", false); err != nil {
		t.Fatalf("取关失败：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"GET /api/v1/users/jimmy auth=Bearer tok",
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

// followTestApp 造一个已登录、正文已加载但没发任何请求的详情页。
func followTestApp(t *testing.T) (*app, *detailState) {
	t.Helper()
	a := testApp(t)
	m := newAuthManager(t.TempDir())
	if err := m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "reader"}); err != nil {
		t.Fatal(err)
	}
	a.auth = m
	a.client = &Client{Auth: m}
	topic, err := decodeTopic([]byte(detailTopicJSON), "JBYGf2A")
	if err != nil {
		t.Fatal(err)
	}
	topic.AuthorID = "u1"
	a.pageName = "detail"
	a.detail = &detailState{shortID: "JBYGf2A", fallbackURL: topic.URL, replyPage: 1}
	a.detail.topic = &topic
	a.detail.follow.userID = topic.AuthorID
	return a, a.detail
}

// waitFollowLabel 等待按钮文案出现 / 消失。关注请求在后台完成后经 publish
// 回写状态，headless 下需要多跑几帧才能看到。
func waitFollowLabel(t *testing.T, tt *ui.Tester, label string, want bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, ok := tt.Find(label)
		if ok == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待按钮 %q 出现=%v 超时；当前文本：%q", label, want, tt.Texts())
		}
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
}

// 点击「关注」变「已关注」，再点「已关注」取关变回「关注」；请求方法与
// 路径都要对。
func TestFollowButtonToggles(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	var following atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/users/jimmy":
			_, _ = fmt.Fprintf(w, envelopeJSON(`{"id":"u1","username":"jimmy","is_following":%v}`), following.Load())
		case "/api/v1/users/u1/follow":
			mu.Lock()
			methods = append(methods, r.Method)
			mu.Unlock()
			following.Store(r.Method == http.MethodPost)
			_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
		default:
			t.Errorf("意外路径 %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a, _ := followTestApp(t)
	a.autoLoad = true
	a.client.HTTP = srv.Client()
	a.client.Base = srv.URL + "/api"

	tt := ui.NewTester(a.view, 1100, 760)
	if _, ok := tt.Find("关注"); !ok {
		t.Fatalf("未关注时应显示「关注」：%q", tt.Texts())
	}
	// 按钮要在头像 / 昵称右侧，而不是挤在名字前面。
	btn, _ := tt.Find("关注")
	author, ok := tt.Find("jimmy")
	if !ok || btn.X <= author.X {
		t.Errorf("关注按钮应位于作者信息右侧：按钮 %+v，昵称 %+v", btn, author)
	}
	if err := tt.Click("关注"); err != nil {
		t.Fatalf("点击关注失败：%v", err)
	}
	waitFollowLabel(t, tt, "已关注", true)

	if err := tt.Click("已关注"); err != nil {
		t.Fatalf("点击已关注失败：%v", err)
	}
	waitFollowLabel(t, tt, "已关注", false)

	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 2 || methods[0] != http.MethodPost || methods[1] != http.MethodDelete {
		t.Errorf("关注 / 取关请求方法不对：%v", methods)
	}
}

// 作者主页返回 is_following=true 时，作者块直接显示「已关注」。
func TestFollowButtonShowsAlreadyFollowing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/jimmy" {
			t.Errorf("意外路径 %s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"u1","username":"jimmy","is_following":true}`))
	}))
	defer srv.Close()

	a, _ := followTestApp(t)
	a.autoLoad = true
	a.client.HTTP = srv.Client()
	a.client.Base = srv.URL + "/api"
	a.detail.follow.need = true

	tt := ui.NewTester(a.view, 1100, 760)
	waitFollowLabel(t, tt, "已关注", true)
}

// 自己的帖子不显示关注按钮。
func TestFollowButtonHiddenOnOwnPost(t *testing.T) {
	a, _ := followTestApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "tok2", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.view, 1100, 760)
	if _, ok := tt.Find("关注"); ok {
		t.Errorf("自己的帖子不应显示关注按钮：%q", tt.Texts())
	}
}

// 关注接口缺 interact scope 时交给全局授权弹窗，并把这次关注登记为授权后重跑。
func TestFollowScopeErrorOpensAuthorization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","m":"缺少权限范围：interact",`+
			`"required_scope":["interact"],"d":null}`)
	}))
	defer srv.Close()

	a, _ := followTestApp(t)
	a.client.HTTP = srv.Client()
	a.client.Base = srv.URL + "/api"

	tt := ui.NewTester(a.view, 1100, 760)
	if err := tt.Click("关注"); err != nil {
		t.Fatalf("点击关注失败：%v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !a.authGate.open {
		if time.Now().After(deadline) {
			t.Fatal("缺 scope 的关注失败没有打开授权弹窗")
		}
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if len(a.authGate.need.Scopes) != 1 || a.authGate.need.Scopes[0] != "interact" {
		t.Errorf("授权弹窗缺少 interact scope：%v", a.authGate.need.Scopes)
	}
	if len(a.authGate.replays) != 1 {
		t.Errorf("关注操作应登记为授权后重跑：%d", len(a.authGate.replays))
	}
}

// 未登录点击「关注」进入全局授权弹窗，并把这次关注登记为授权成功后重跑。
func TestFollowButtonAnonymousRequestsLogin(t *testing.T) {
	a := testApp(t)
	topic, _ := decodeTopic([]byte(detailTopicJSON), "JBYGf2A")
	a.pageName = "detail"
	a.detail = &detailState{shortID: "JBYGf2A", fallbackURL: topic.URL, replyPage: 1}
	a.detail.topic = &topic

	tt := ui.NewTester(a.view, 1100, 760)
	if err := tt.Click("关注"); err != nil {
		t.Fatalf("点击关注失败：%v", err)
	}
	if !a.authGate.open {
		t.Fatal("未登录点击关注应打开授权弹窗")
	}
	if !strings.Contains(a.authGate.need.Message, "关注作者") {
		t.Errorf("授权弹窗说明不对：%q", a.authGate.need.Message)
	}
	if len(a.authGate.replays) != 1 {
		t.Errorf("关注操作应登记为授权后重跑：%d", len(a.authGate.replays))
	}
}
