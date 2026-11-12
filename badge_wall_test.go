package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

/* ------------------------------------------------------------------
   端点契约与解析
---------------------------------------------------------------- */

const badgeWallJSON = `[
  {
    "id": "ub1", "user_id": "u1", "badge_id": "b1",
    "achieved_at": "2026-01-01T00:00:00.000Z", "custom_fields": null,
    "badge": {
      "id": "b1", "code": "early_bird", "name": "早起鸟",
      "description": "连续早起签到的社区徽章",
      "icon_url": "https://r2.2libra.com/badge/svg/early_bird.svg",
      "icon_url_large": "https://r2.2libra.com/badge/early_bird.svg",
      "level": 2, "attributes": {"exp": 10},
      "acquisition_condition": "连续早起 30 天",
      "user_count": 1234, "user_percentage": 12.3
    }
  }
]`

// 徽章墙与佩戴切换必须打到文档里的路径；参数名、方法与幂等键都要对上。
func TestBadgeWallRequests(t *testing.T) {
	if myBadgesPath != "/v1/me/badges" || myBadgesEquippedPath != "/v1/me/badges/equipped" {
		t.Fatalf("徽章端点路径不对：%s / %s", myBadgesPath, myBadgesEquippedPath)
	}
	if got, want := myBadgeEquipPath("b/1"), "/v1/me/badges/b%2F1/equip"; got != want {
		t.Errorf("徽章 uuid 转义不对：%s，期望 %s", got, want)
	}

	var mu sync.Mutex
	var calls []string
	var equipKeys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPost {
			equipKeys = append(equipKeys, r.Header.Get("Idempotency-Key"))
		}
		mu.Unlock()

		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("%s 没有带令牌：%q", r.URL.Path, got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me/badges":
			fmt.Fprint(w, envelopeJSON(badgeWallJSON))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me/badges/equipped":
			fmt.Fprint(w, envelopeJSON(`[{"badge_id":"b1","badge":{"id":"b1","name":"早起鸟"}}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/me/badges/b1/equip":
			fmt.Fprint(w, envelopeJSON(`{"message":"佩戴成功，扣除 5 金币"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/me/badges/b1/equip":
			fmt.Fprint(w, envelopeJSON(`{"message":"已取消佩戴"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := testApp(t)
	a.client.Base = srv.URL + "/api"
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}

	badges, err := a.client.fetchMyBadges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(badges) != 1 || badges[0].Name != "早起鸟" {
		t.Fatalf("徽章墙没有解析出来：%+v", badges)
	}
	ids, err := a.client.fetchEquippedBadgeIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ids["b1"] {
		t.Errorf("佩戴状态没有解析出来：%+v", ids)
	}

	msg, err := a.client.setBadgeEquipped(context.Background(), "b1", "key-1", true)
	if err != nil || msg != "佩戴成功，扣除 5 金币" {
		t.Errorf("佩戴失败：msg=%q err=%v", msg, err)
	}
	msg, err = a.client.setBadgeEquipped(context.Background(), "b1", "key-1", false)
	if err != nil || msg != "已取消佩戴" {
		t.Errorf("取消佩戴失败：msg=%q err=%v", msg, err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"GET /api/v1/me/badges",
		"GET /api/v1/me/badges/equipped",
		"POST /api/v1/me/badges/b1/equip",
		"DELETE /api/v1/me/badges/b1/equip",
	}
	if len(calls) != len(want) {
		t.Fatalf("请求序列不对：%v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("第 %d 个请求 = %s，期望 %s", i+1, calls[i], want[i])
		}
	}
	if len(equipKeys) != 1 || equipKeys[0] != "key-1" {
		t.Errorf("佩戴请求的幂等键不对：%q", equipKeys)
	}
}

// 徽章信息在 item.badge 里；两个 id、大小图、等级、持有比例与获得时间都要
// 分开解出来，佩戴状态由 /badges/equipped 的 badge_id 对齐。
func TestDecodeUserBadges(t *testing.T) {
	badges := decodeUserBadges([]byte(badgeWallJSON))
	if len(badges) != 1 {
		t.Fatalf("徽章数量 = %d，期望 1：%+v", len(badges), badges)
	}
	b := badges[0]
	if b.UserBadgeID != "ub1" || b.BadgeID != "b1" {
		t.Errorf("徽章 id 解析不对：%+v", b)
	}
	if b.Name != "早起鸟" || b.Description != "连续早起签到的社区徽章" {
		t.Errorf("徽章名称 / 说明解析不对：%+v", b)
	}
	if b.IconURL != "https://r2.2libra.com/badge/svg/early_bird.svg" ||
		b.IconURLLarge != "https://r2.2libra.com/badge/early_bird.svg" {
		t.Errorf("徽章图标解析不对：%+v", b)
	}
	if b.Level != 2 || b.UserCount != 1234 || b.UserPercentage != 12.3 {
		t.Errorf("徽章元数据解析不对：%+v", b)
	}
	if b.AchievedAt != "2026-01-01T00:00:00.000Z" {
		t.Errorf("获得时间解析不对：%q", b.AchievedAt)
	}

	ids := decodeEquippedBadgeIDs([]byte(`[
		{"id":"ue1","badge_id":"b1","equipped_at":"2026-01-02T00:00:00Z","badge":{"id":"b1"}},
		{"id":"ue2","badge":{"id":"b2"}}
	]`))
	if !ids["b1"] || !ids["b2"] || len(ids) != 2 {
		t.Errorf("佩戴状态解析不对：%+v", ids)
	}

	// 只有小图的徽章：大图保持为空，界面据此把图缩小，而不是放大糊掉。
	small := decodeUserBadges([]byte(`[{"id":"ub2","badge_id":"b2","badge":{
		"id":"b2","name":"全勤","icon_url":"https://r2.2libra.com/badge/svg/year_streak.svg"}}]`))
	if len(small) != 1 || small[0].IconURLLarge != "" ||
		small[0].IconURL != "https://r2.2libra.com/badge/svg/year_streak.svg" {
		t.Errorf("只有小图时回退不对：%+v", small)
	}
}

/* ------------------------------------------------------------------
   界面：展示、加载与切换
---------------------------------------------------------------- */

// 徽章标签页把全部徽章排成两列卡片：名称、说明、等级 / 持有比例、获得时间，
// 佩戴中的另有标记；每枚徽章的按钮都有唯一的无障碍名。
func TestProfileBadgeTabRenders(t *testing.T) {
	a := profileWindowApp(t)
	ps := a.profile
	ps.tab = profileTabBadges
	ps.badgeLoaded = true
	large := "https://r2.2libra.com/badge/early_bird.svg"
	a.images.put(large, solidBitmap(64, 64, marker))
	ps.badges = []badgeWallEntry{
		{
			BadgeID: "b1", Name: "早起鸟", Description: "连续早起签到的社区徽章",
			IconURL: "https://r2.2libra.com/badge/svg/early_bird.svg", IconURLLarge: large,
			Level: 2, UserPercentage: 12.3,
			AchievedAt: "2026-01-01T00:00:00Z", Equipped: true,
		},
		{
			BadgeID: "b2", Name: "全勤", Description: "一年没有断签",
			IconURL:   "https://r2.2libra.com/badge/svg/year_streak.svg",
			UserCount: 1234, AchievedAt: "2026-02-01T00:00:00Z",
		},
	}
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()

	for _, want := range []string{
		"我的徽章", "共 2 枚 · 佩戴中 1 枚", "徽章",
		"早起鸟", "全勤", "佩戴中", "取消佩戴", "佩戴",
		"Lv.2 · 12.3% 的用户拥有", "1234 人拥有",
		"连续早起签到的社区徽章", "获得 2026-01-01",
		"首次佩戴免费",
	} {
		if !tt.HasText(want) {
			t.Errorf("徽章页缺少 %q：%q", want, tt.Texts())
		}
	}
	for _, label := range []string{"取消佩戴「早起鸟」", "佩戴「全勤」"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("找不到徽章按钮 %q：%q", label, tt.Texts())
		}
		if r.X+r.W > profileWindowWidth {
			t.Errorf("%q 超出台宽：x=%.0f 宽 %.0f", label, r.X, r.W)
		}
	}
	if n := countMarker(tt.Image()); n == 0 {
		t.Error("徽章墙没有画出徽章图标")
	}
}

// 首次打开徽章标签页才发请求：墙与佩戴状态都打到文档端点，并用佩戴接口的
// badge_id 对齐。
func TestProfileBadgesLoadOnTab(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/me/badges":
			// 不带图片地址：这个测试只关心加载时序，别让图片预取去碰网络。
			fmt.Fprint(w, envelopeJSON(`[{"id":"ub1","badge_id":"b1",
				"badge":{"id":"b1","name":"早起鸟"}}]`))
		case "/api/v1/me/badges/equipped":
			fmt.Fprint(w, envelopeJSON(`[{"badge_id":"b1","badge":{"id":"b1"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := testApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	a.profile.tab = profileTabBadges
	a.profile.infoLoaded = true
	a.ensureProfileLoaded()
	waitProfile(t, "徽章墙", func() bool { return a.profile.badgeLoaded })

	if len(a.profile.badges) != 1 || !a.profile.badges[0].Equipped {
		t.Fatalf("徽章墙没有回填：%+v", a.profile.badges)
	}
	if len(a.profile.user.Badges) != 1 || a.profile.user.Badges[0].Name != "早起鸟" {
		t.Errorf("资料头的佩戴徽章没有同步：%+v", a.profile.user.Badges)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"/api/v1/me/badges", "/api/v1/me/badges/equipped"}
	if len(paths) != len(want) {
		t.Fatalf("请求路径不对：%v", paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("第 %d 个请求 = %s，期望 %s", i+1, paths[i], want[i])
		}
	}
}

// 读徽章墙缺 profile 权限（旧令牌）时也进入全局授权链路，而不是只把错误
// 留在页面里；授权成功后同一次加载会自动重跑。
func TestProfileBadgesLoadPromptsAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","m":"需要更多权限","d":null,
			"required_scope":["profile"],"t":1730000000000}`)
	}))
	defer srv.Close()

	a := testApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	a.profile.tab = profileTabBadges
	a.profile.infoLoaded = true
	a.ensureProfileLoaded()
	waitProfile(t, "授权弹窗", func() bool { return a.authGate.open })
	if len(a.authGate.replays) != 1 {
		t.Fatalf("被中断的徽章加载没有登记重放：%+v", a.authGate)
	}
	if len(a.authGate.need.Scopes) != 1 || a.authGate.need.Scopes[0] != "profile" {
		t.Errorf("缺失的 scope 没有登记：%+v", a.authGate.need)
	}
	if a.profile.badgeErr == "" {
		t.Error("授权弹窗之外，页面也应说明加载失败")
	}
}

// 徽章页的三种整页状态：加载失败带重试、没有徽章、读取中。
func TestProfileBadgeStates(t *testing.T) {
	a := profileWindowApp(t)
	ps := a.profile
	ps.tab = profileTabBadges
	ps.badgeErr = "网络错误"
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if !tt.HasText("加载失败") || !tt.HasText("网络错误") || !tt.HasText("重试") {
		t.Errorf("加载失败没有留在页面里：%q", tt.Texts())
	}

	a = profileWindowApp(t)
	a.profile.tab = profileTabBadges
	a.profile.badgeLoaded = true
	tt = ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if !tt.HasText("还没有获得任何徽章。") {
		t.Errorf("没有徽章时缺少空状态说明：%q", tt.Texts())
	}

	a = profileWindowApp(t)
	a.profile.tab = profileTabBadges
	a.profile.badgeLoading = true
	tt = ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if !tt.HasText("正在读取我的徽章…") {
		t.Errorf("读取中缺少进度说明：%q", tt.Texts())
	}
}

// badgeToggleApp 造一个已经打开徽章页的应用，和一份可按方法回应的服务器。
func badgeToggleApp(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*app, *ui.Tester) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(handler))
	t.Cleanup(srv.Close)

	a := profileWindowApp(t)
	a.autoLoad = false // 徽章图不预取，测试只关心状态与请求
	a.client.Base = srv.URL + "/api"
	ps := a.profile
	ps.tab = profileTabBadges
	ps.badgeLoaded = true
	ps.badges = []badgeWallEntry{{
		BadgeID: "b1", Name: "全勤", Description: "一年没有断签",
		IconURL:    "https://r2.2libra.com/badge/svg/year_streak.svg",
		AchievedAt: "2026-01-01T00:00:00Z",
	}}
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	return a, tt
}

// 点「佩戴」发 POST、成功后更新本地状态与资料头、发金币事件并用服务器文案
// 报 toast；再点「取消佩戴」发 DELETE。两次操作都在同一个窗口里出结果。
func TestProfileBadgeToggle(t *testing.T) {
	var mu sync.Mutex
	var methods, keys []string
	a, tt := badgeToggleApp(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		if r.Method == http.MethodDelete {
			fmt.Fprint(w, envelopeJSON(`{"message":"已取消佩戴"}`))
			return
		}
		fmt.Fprint(w, envelopeJSON(`{"message":"佩戴成功，扣除 5 金币"}`))
	})
	var eventsMu sync.Mutex
	var events []coinEvent
	a.onCoinsChanged = func(e coinEvent) {
		eventsMu.Lock()
		events = append(events, e)
		eventsMu.Unlock()
	}

	if err := tt.Click("佩戴「全勤」"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitProfile(t, "佩戴", func() bool {
		return a.profile.badges[0].Equipped && a.profile.badgeBusyID == ""
	})
	if len(a.profile.user.Badges) != 1 || a.profile.user.Badges[0].Name != "全勤" {
		t.Errorf("资料头的佩戴徽章没有跟着切换：%+v", a.profile.user.Badges)
	}
	eventsMu.Lock()
	gotEvents := append([]coinEvent(nil), events...)
	eventsMu.Unlock()
	if len(gotEvents) != 1 || gotEvents[0] != coinEventBadge {
		t.Errorf("佩戴没有发出金币刷新事件：%+v", gotEvents)
	}
	// 提示可能在请求完成的那一帧里已经被窗口领走，所以看界面而不是队列：
	// 「我的」窗口只认领发给自己的 toast，能在它里面看到就说明目标窗口对。
	tt.Frame()
	if !tt.HasText("佩戴成功，扣除 5 金币") {
		t.Errorf("徽章窗口没有显示佩戴结果：%q", tt.Texts())
	}

	if err := tt.Click("取消佩戴「全勤」"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitProfile(t, "取消佩戴", func() bool {
		return !a.profile.badges[0].Equipped && a.profile.badgeBusyID == ""
	})
	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 2 || methods[0] != http.MethodPost || methods[1] != http.MethodDelete {
		t.Fatalf("佩戴 / 取消佩戴的方法不对：%v", methods)
	}
	if keys[0] == "" {
		t.Error("佩戴请求没有带幂等键")
	}
	if keys[1] != "" {
		t.Errorf("取消佩戴不该带幂等键：%q", keys[1])
	}
}

// 缺 write:economy 时进入全局授权链路；授权成功后同一次点击自动重跑，并且
// 重放沿用同一个 Idempotency-Key，不会重复扣币。
func TestProfileBadgeEquipReplaysWithSameKey(t *testing.T) {
	var mu sync.Mutex
	var keys []string
	attempt := 0
	a, tt := badgeToggleApp(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		attempt++
		n := attempt
		mu.Unlock()
		if n == 1 {
			fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","m":"需要更多权限","d":null,
				"required_scope":["write:economy"],"t":1730000000000}`)
			return
		}
		fmt.Fprint(w, envelopeJSON(`{"message":"佩戴成功"}`))
	})

	if err := tt.Click("佩戴「全勤」"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitProfile(t, "授权弹窗", func() bool { return a.authGate.open })
	if len(a.authGate.replays) != 1 {
		t.Fatalf("被中断的佩戴没有登记重放：%+v", a.authGate)
	}
	if len(a.authGate.need.Scopes) != 1 || a.authGate.need.Scopes[0] != "write:economy" {
		t.Errorf("缺失的 scope 没有登记：%+v", a.authGate.need)
	}

	a.authGate.open = false
	a.replayAuthorizedCalls()
	waitProfile(t, "重放后的佩戴", func() bool {
		return a.profile.badges[0].Equipped && a.profile.badgeBusyID == ""
	})
	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 2 {
		t.Fatalf("重放没有重新发起请求：%v", keys)
	}
	if keys[0] == "" || keys[0] != keys[1] {
		t.Errorf("重放换了幂等键：%q", keys)
	}
}

// 服务端拒绝时状态保持原样，失败原因用 toast 报告在「我的」窗口里。
func TestProfileBadgeEquipFailure(t *testing.T) {
	a, tt := badgeToggleApp(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"c":500,"code":"internal_error","m":"服务器打盹了","d":null,"t":1730000000000}`)
	})
	if err := tt.Click("佩戴「全勤」"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitProfile(t, "佩戴失败", func() bool { return a.profile.badgeBusyID == "" })
	if a.profile.badges[0].Equipped {
		t.Error("请求失败后徽章仍是佩戴中")
	}
	// 同样看界面：失败原因必须以 toast 出现在「我的」窗口里。
	tt.Frame()
	if !tt.HasText("服务端错误") {
		t.Errorf("失败原因没有用 toast 报告：%q", tt.Texts())
	}
}
