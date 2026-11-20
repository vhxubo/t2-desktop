package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 他人徽章墙的两个端点：路径参数是 username 或 uuid，应用里只有 username，
// 特殊字符必须转义。
func TestUserBadgesPaths(t *testing.T) {
	if got, want := userBadgesPath("alice"), "/v1/users/alice/badges"; got != want {
		t.Errorf("徽章墙路径 = %q，期望 %q", got, want)
	}
	if got, want := userBadgesPath("a/b?c"), "/v1/users/a%2Fb%3Fc/badges"; got != want {
		t.Errorf("昵称转义不对：%q，期望 %q", got, want)
	}
	if got, want := userBadgesEquippedPath("alice"), "/v1/users/alice/badges/equipped"; got != want {
		t.Errorf("佩戴徽章路径 = %q，期望 %q", got, want)
	}
}

// 读取他人徽章墙与佩戴状态：路径、信封解析与 404 都要对。
func TestFetchUserBadges(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/users/alice/badges":
			fmt.Fprint(w, envelopeJSON(badgeWallJSON))
		case "/api/v1/users/alice/badges/equipped":
			fmt.Fprint(w, envelopeJSON(`[{"badge_id":"b1","badge":{"id":"b1","name":"早起鸟"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/api"}
	badges, err := c.fetchUserBadges(context.Background(), "alice")
	if err != nil {
		t.Fatalf("读取他人徽章墙失败：%v", err)
	}
	if len(badges) != 1 || badges[0].Name != "早起鸟" || badges[0].BadgeID != "b1" {
		t.Fatalf("徽章墙解析不对：%+v", badges)
	}
	ids, err := c.fetchUserEquippedBadgeIDs(context.Background(), "alice")
	if err != nil {
		t.Fatalf("读取他人佩戴徽章失败：%v", err)
	}
	if !ids["b1"] {
		t.Errorf("佩戴状态解析不对：%+v", ids)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"/api/v1/users/alice/badges", "/api/v1/users/alice/badges/equipped"}
	if len(paths) != len(want) {
		t.Fatalf("请求路径 = %v，期望 %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("第 %d 个请求 = %s，期望 %s", i+1, paths[i], want[i])
		}
	}
}

// 切到「他的徽章」后真正拉两条端点，并把佩戴状态合并回徽章墙。
func TestUserCardLoadsBadges(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/users/alice/badges":
			fmt.Fprint(w, envelopeJSON(badgeWallJSON))
		case "/api/v1/users/alice/badges/equipped":
			fmt.Fprint(w, envelopeJSON(`[{"badge_id":"b1","badge":{"id":"b1","name":"早起鸟"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := testApp(t)
	a.client.Base = srv.URL + "/api"
	a.userCard = newUserCardState("alice")
	a.userCard.tab = userCardTabBadges
	a.loadUserCardBadges()
	waitProfile(t, "他人徽章墙", func() bool { return a.userCard.badges.loaded })

	cs := a.userCard
	if cs.badges.err != "" {
		t.Fatalf("读取他人徽章墙失败：%s", cs.badges.err)
	}
	if len(cs.badges.entries) != 1 {
		t.Fatalf("徽章墙没有回填：%+v", cs.badges.entries)
	}
	if !cs.badges.entries[0].Equipped {
		t.Errorf("佩戴状态没有合并回徽章墙：%+v", cs.badges.entries[0])
	}
}

// 「他的徽章」标签页：只读卡片显示名字、等级 / 持有比例、说明与获得日期，
// 佩戴中的标出来，但没有佩戴按钮。
func TestUserCardBadgesBody(t *testing.T) {
	a := userCardTestApp(t)
	cs := a.userCard
	cs.tab = userCardTabBadges
	cs.badges.loaded = true
	cs.badges.entries = []badgeWallEntry{{
		BadgeID:        "b1",
		Name:           "早起鸟",
		Description:    "连续早起签到的社区徽章",
		Level:          2,
		UserPercentage: 12.3,
		AchievedAt:     "2026-01-01T00:00:00.000Z",
		Equipped:       true,
	}}

	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	for _, want := range []string{
		"他的徽章", "共 1 枚 · 佩戴中 1 枚", "早起鸟", "佩戴中",
		"Lv.2 · 12.30% 的用户拥有", "连续早起签到的社区徽章", "获得 2026-01-01",
	} {
		if !tt.HasText(want) {
			t.Errorf("徽章标签页缺少 %q：%q", want, tt.Texts())
		}
	}
	for _, unwanted := range []string{"佩戴「早起鸟」", "取消佩戴"} {
		if tt.HasText(unwanted) {
			t.Errorf("他人的徽章不该出现佩戴操作 %q：%q", unwanted, tt.Texts())
		}
	}
}

// 徽章墙的空状态与错误状态：说明为什么是空的，失败时给重试入口。
func TestUserCardBadgesStates(t *testing.T) {
	a := userCardTestApp(t)
	cs := a.userCard
	cs.tab = userCardTabBadges
	cs.badges.loaded = true

	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if !tt.HasText("这位用户还没有获得任何徽章。") {
		t.Errorf("空徽章墙缺少说明：%q", tt.Texts())
	}

	cs.badges.err = "网络不可用"
	tt.Frame()
	for _, want := range []string{"加载失败", "网络不可用", "重试"} {
		if !tt.HasText(want) {
			t.Errorf("失败状态缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 五个标签页都能点到；切到「他的徽章」后状态里的 tab 要跟着走。
func TestUserCardBadgeTabClick(t *testing.T) {
	a := userCardTestApp(t)
	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	// 五个页签都要留在窗口里：新增的「他的徽章」不能把「他的合集」挤出去。
	for _, label := range userCardTabLabels {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("找不到页签 %q：%q", label, tt.Texts())
		}
		if r.X < 0 || r.X+r.W > float32(userCardWindowWidth) {
			t.Errorf("页签 %q 超出窗口：x=%.0f 宽=%.0f", label, r.X, r.W)
		}
	}
	if err := tt.Click("他的徽章"); err != nil {
		t.Fatalf("点「他的徽章」失败：%v", err)
	}
	if a.userCard.tab != userCardTabBadges {
		t.Errorf("点击后 tab = %d，期望徽章页 %d", a.userCard.tab, userCardTabBadges)
	}
}

// 徽章墙失败后点「重试」会重新请求，成功后回填并显示卡片。
func TestUserCardBadgesRetry(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/users/alice/badges":
			calls++
			fmt.Fprint(w, envelopeJSON(badgeWallJSON))
		case "/api/v1/users/alice/badges/equipped":
			fmt.Fprint(w, envelopeJSON(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := userCardTestApp(t)
	a.client.Base = srv.URL + "/api"
	cs := a.userCard
	cs.tab = userCardTabBadges
	cs.badges.loaded = true
	cs.badges.err = "网络不可用"

	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if err := tt.Click("重试"); err != nil {
		t.Fatalf("点重试失败：%v", err)
	}
	waitProfile(t, "重试后的他人徽章墙", func() bool {
		return cs.badges.loaded && cs.badges.err == "" && len(cs.badges.entries) == 1
	})
	tt.Frame()
	if calls != 1 {
		t.Errorf("重试请求次数 = %d，期望 1", calls)
	}
	if !tt.HasText("早起鸟") {
		t.Errorf("重试成功后没有显示徽章：%q", tt.Texts())
	}
}

// 登录态变化后，属于旧账号的本人隐私徽章字段不能再留在墙上。
func TestRefreshUserCardForAuthDropsBadges(t *testing.T) {
	a := userCardTestApp(t)
	cs := a.userCard
	cs.badges.loaded = true
	cs.badges.entries = []badgeWallEntry{{BadgeID: "b1", Name: "早起鸟"}}

	a.refreshUserCardForAuth()

	if cs.badges.loaded || len(cs.badges.entries) != 0 {
		t.Errorf("换账号后仍留着旧徽章墙：%+v", cs.badges)
	}
}

// 保证测试里的本地点与线上路径一致：两个端点都必须落在 users/:name 下。
func TestUserBadgesPathsStayUnderUser(t *testing.T) {
	for _, path := range []string{userBadgesPath("alice"), userBadgesEquippedPath("alice")} {
		if !strings.HasPrefix(path, "/v1/users/alice/") {
			t.Errorf("徽章路径没有挂在用户下：%q", path)
		}
	}
}
