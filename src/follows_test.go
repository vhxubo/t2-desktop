package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 两个名单端点的路径与分页参数必须严格照文档：limit 上限 50。
func TestFollowsPathsMatchContract(t *testing.T) {
	cases := []struct {
		kind int
		page int
		want string
	}{
		{followsFollowing, 1, "/v1/me/follows/following?page=1&limit=50"},
		{followsFollowing, 2, "/v1/me/follows/following?page=2&limit=50"},
		{followsFollowers, 1, "/v1/me/follows/followers?page=1&limit=50"},
	}
	for _, c := range cases {
		if got := followsListPath(c.kind, c.page); got != c.want {
			t.Errorf("名单路径 = %s，期望 %s", got, c.want)
		}
	}
	// 没有上次读取时间时不带参数：服务端会回 0。
	if got, want := followsUnreadPath(""), "/v1/me/follows/unread-count"; got != want {
		t.Errorf("未读数路径 = %s，期望 %s", got, want)
	}
	at := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	got := followsUnreadPath(at.Format(time.RFC3339))
	if !strings.HasPrefix(got, "/v1/me/follows/unread-count?last_read_at=") {
		t.Errorf("未读数路径没有带 last_read_at：%s", got)
	}
	if !strings.Contains(got, "2026-11-01T08%3A00%3A00Z") {
		t.Errorf("last_read_at 没有转义：%s", got)
	}
}

// 名单项字段是 id / username / avatar_url / is_pro / is_following /
// followed_at；没有 is_following 时按未知处理。
func TestDecodeFollows(t *testing.T) {
	d := json.RawMessage(`{"items":[
		{"id":"u1","username":"jimmy","avatar_url":"https://x/a.png","is_pro":true,
		 "is_following":true,"followed_at":"2026-10-02T08:31:12.004Z"},
		{"id":"u2","username":"amy"}],
		"total":2,"page":1,"limit":50,"total_pages":1}`)
	p := decodeFollows(d)
	if len(p.Items) != 2 || p.Total != 2 || p.TotalPages != 1 {
		t.Fatalf("名单解析不对：%+v", p)
	}
	first := p.Items[0]
	if first.ID != "u1" || first.Username != "jimmy" || !first.IsPro ||
		!first.IsFollowing || !first.FollowKnown || first.Avatar != "https://x/a.png" {
		t.Errorf("第一行解析不对：%+v", first)
	}
	if p.Items[1].FollowKnown {
		t.Errorf("没有 is_following 时应按未知处理：%+v", p.Items[1])
	}
}

// 未读数响应文档没给示例：unread_count / count / 裸数字都要认。
func TestDecodeUnreadCount(t *testing.T) {
	cases := map[string]int{
		`{"unread_count":7}`: 7,
		`{"count":3}`:        3,
		`{"unread":2}`:       2,
		`5`:                  5,
		`null`:               0,
	}
	for raw, want := range cases {
		if got := decodeUnreadCount(json.RawMessage(raw)); got != want {
			t.Errorf("%s 解析成 %d，期望 %d", raw, got, want)
		}
	}
}

// 读名单要带令牌，路径与查询原样。
func TestClientFollowsEndpoints(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.RequestURI()+" auth="+r.Header.Get("Authorization"))
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/unread-count"):
			_, _ = fmt.Fprint(w, envelopeJSON(`{"unread_count":4}`))
		default:
			_, _ = fmt.Fprint(w, envelopeJSON(`{"items":[{"id":"u1","username":"jimmy"}],
				"total":1,"page":1,"limit":50,"total_pages":1}`))
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	ctx := context.Background()

	if _, err := c.fetchFollows(ctx, followsFollowing, 1); err != nil {
		t.Fatalf("读关注名单失败：%v", err)
	}
	if _, err := c.fetchFollows(ctx, followsFollowers, 2); err != nil {
		t.Fatalf("读粉丝名单失败：%v", err)
	}
	n, err := c.fetchFollowsUnread(ctx, "2026-11-01T00:00:00Z")
	if err != nil || n != 4 {
		t.Fatalf("读未读数失败：%d %v", n, err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"/api/v1/me/follows/following?page=1&limit=50 auth=Bearer tok",
		"/api/v1/me/follows/followers?page=2&limit=50 auth=Bearer tok",
		"/api/v1/me/follows/unread-count?last_read_at=2026-11-01T00%3A00%3A00Z auth=Bearer tok",
	}
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Errorf("第 %d 个请求 = %q，期望 %q（实际 %v）", i, at(got, i), w, got)
		}
	}
}

// followsTestApp 造一个已登录、名单已经拉到一页的窗口。
func followsTestApp(t *testing.T, handler http.HandlerFunc) *app {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	a := testApp(t)
	m := newAuthManager(t.TempDir())
	if err := m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "reader"}); err != nil {
		t.Fatal(err)
	}
	a.auth = m
	a.client = &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: m}
	a.follows = &followsState{viewer: "reader"}
	a.follows.lists[followsFollowers] = followList{
		loaded: true,
		items: []followUser{{
			ID: "u1", Username: "jimmy", IsFollowing: false, FollowKnown: true,
		}},
		total: 1,
	}
	a.follows.lists[followsFollowing] = followList{
		loaded: true,
		items: []followUser{{
			ID: "u2", Username: "amy", IsFollowing: true, FollowKnown: true,
		}},
		total: 1,
	}
	return a
}

// 粉丝名单里对没关注的人显示「回关」：点一下变成「已关注」，结果走窗口内
// toast。
func TestFollowsWindowBackfillFollow(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	a := followsTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
	})
	a.follows.tab = followsFollowers

	tt := ui.NewTester(a.followsWindowView, followsWindowWidth, followsWindowHeight)
	tt.Frame()
	if !tt.HasText("jimmy") {
		t.Fatalf("名单里没有这一行：%q", tt.Texts())
	}
	if err := tt.Click("回关 jimmy"); err != nil {
		t.Fatalf("点不到回关：%v（%q）", err, tt.Texts())
	}
	// 回关结果最后才排进提示队列：等它出现在窗口上，再看请求与按钮。
	waitForShownToast(t, a, tt, "已关注 @jimmy")

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != "POST /api/v1/users/u1/follow" {
		t.Errorf("回关请求 = %v", calls)
	}
	if _, ok := tt.Find("已关注 jimmy"); !ok {
		t.Errorf("按钮没有变成「已关注」：%q", tt.Texts())
	}
}

// 「我关注的人」里取关：这一行直接从名单里移出，计数跟着减一。
func TestFollowsWindowUnfollowRemovesRow(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	a := followsTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		_, _ = fmt.Fprint(w, envelopeJSON(`{"success":true}`))
	})
	a.follows.tab = followsFollowing

	tt := ui.NewTester(a.followsWindowView, followsWindowWidth, followsWindowHeight)
	tt.Frame()
	if err := tt.Click("已关注 amy"); err != nil {
		t.Fatalf("点不到取关：%v（%q）", err, tt.Texts())
	}
	waitFor(t, func() bool { return a.follows.busy == "" && len(a.follows.lists[followsFollowing].items) == 0 },
		"取关请求没有完成")
	tt.Frame()

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != "DELETE /api/v1/users/u2/follow" {
		t.Errorf("取关请求 = %v", calls)
	}
	if a.follows.lists[followsFollowing].total != 0 {
		t.Errorf("取关后计数没有减一：%d", a.follows.lists[followsFollowing].total)
	}
	if !tt.HasText("还没有关注任何人。") {
		t.Errorf("取关后没有显示空名单：%q", tt.Texts())
	}
}

// 未读数作为侧边栏「动态」的 Lobe 红点角标；打开这一页时清掉并记下读取时间。
func TestFollowsUnreadBadgeAndMarkRead(t *testing.T) {
	a := testApp(t)
	a.followUnread = 3
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	row, ok := tt.Find("动态")
	if !ok {
		t.Fatalf("侧边栏没有「动态」：%q", tt.Texts())
	}
	dot, ok := tt.Find("3")
	if !ok || dot.Y < row.Y-20 || dot.Y > row.Y+row.H+20 {
		t.Fatalf("侧边栏「动态」没有未读红点：%q", tt.Texts())
	}

	a.markFollowsRead()
	if a.followUnread != 0 {
		t.Errorf("标记已读后角标没清掉：%d", a.followUnread)
	}
	if a.followsReadAt().IsZero() {
		t.Error("标记已读没有记下时间")
	}
	tt.Frame()
	if _, ok := tt.Find("3"); ok {
		t.Errorf("角标没有消失：%q", tt.Texts())
	}
}

// 「加载更多」只在确实还有下一页时出现：服务端给了总数且已经全部拿到，
// 就不该留一个点下去只会得到空页的按钮。
func TestFollowsMoreButtonVisibility(t *testing.T) {
	a := followsTestApp(t, func(w http.ResponseWriter, r *http.Request) {})
	a.follows.tab = followsFollowers

	tt := ui.NewTester(a.followsWindowView, followsWindowWidth, followsWindowHeight)
	tt.Frame()
	if _, ok := tt.Find("加载更多"); ok {
		t.Errorf("已全部加载时不该显示「加载更多」：%q", tt.Texts())
	}

	a.follows.lists[followsFollowers].total = 120
	tt.Frame()
	if _, ok := tt.Find("加载更多"); !ok {
		t.Errorf("还有下一页时应当显示「加载更多」：%q", tt.Texts())
	}
}

// 「我的」窗口的粉丝 / 关注统计块可点：分别打开名单窗口的对应分段。
func TestProfileStatsOpenFollowsWindow(t *testing.T) {
	a := testApp(t)
	m := newAuthManager(t.TempDir())
	if err := m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "reader"}); err != nil {
		t.Fatal(err)
	}
	a.auth = m
	a.profile = newProfileState()
	a.profile.infoLoaded = true
	a.profile.user = accountProfile{Name: "reader", Followers: 12, Following: 3}
	a.profile.tab = profileTabOverview

	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if err := tt.Click("粉丝"); err != nil {
		t.Fatalf("点不到粉丝统计块：%v（%q）", err, tt.Texts())
	}
	if a.follows == nil || a.follows.tab != followsFollowers {
		t.Errorf("点粉丝没有打开粉丝名单：%+v", a.follows)
	}
	if err := tt.Click("关注"); err != nil {
		t.Fatalf("点不到关注统计块：%v（%q）", err, tt.Texts())
	}
	if a.follows.tab != followsFollowing {
		t.Errorf("点关注没有切到关注名单：%+v", a.follows)
	}
}
