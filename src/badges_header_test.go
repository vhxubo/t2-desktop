package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

/* ------------------------------------------------------------------
   资料头·徽章行
---------------------------------------------------------------- */

// 徽章行左边是佩戴中的徽章，竖分割线右边是徽章墙里的全部徽章；两处的小图
// 都有无障碍名，tooltip 带上说明，说明由徽章墙按名字 / 机器码补齐。
func TestHeaderBadgeRowShowsWornAndAllWithDescription(t *testing.T) {
	a := testApp(t)
	const wornURL = "https://r2.2libra.com/badge/svg/early_bird.svg"
	const plainURL = "https://r2.2libra.com/badge/svg/year_streak.svg"
	a.images.put(wornURL, solidBitmap(22, 15, marker))
	a.images.put(plainURL, solidBitmap(22, 15, marker))
	equipped := []badgeInfo{{Name: "早起鸟", Code: "early_bird", IconURL: wornURL}}
	all := []badgeWallEntry{
		{BadgeID: "b1", Name: "早起鸟", Code: "early_bird",
			Description: "连续早起签到的社区徽章", IconURL: wornURL},
		{BadgeID: "b2", Name: "全勤", Description: "一年没有断签", IconURL: plainURL},
	}

	tt := ui.NewTester(func(c *ui.Context) {
		a.headerBadgeRow(c, equipped, all, true, true, nil)
	}, 420, 80)
	tt.Frame()

	var worn, plain ui.AccessInfo
	for _, info := range tt.AccessAll() {
		if info.Role != ui.RoleImage {
			continue
		}
		switch info.Name {
		case "早起鸟":
			if worn.Name == "" {
				worn = info
			}
		case "全勤":
			plain = info
		}
	}
	if worn.Name == "" || plain.Name == "" {
		t.Fatalf("徽章行没有画出两枚小图：%q", tt.Texts())
	}
	if want := "早起鸟：连续早起签到的社区徽章"; worn.Description != want {
		t.Errorf("佩戴徽章的 tooltip = %q，期望 %q", worn.Description, want)
	}
	if want := "全勤：一年没有断签"; plain.Description != want {
		t.Errorf("全部徽章的 tooltip = %q，期望 %q", plain.Description, want)
	}
	// 两段在同一流动行里：佩戴在前、全部在后，Y 对齐，中间夹一条竖分割线。
	if plain.Bounds.X <= worn.Bounds.X {
		t.Errorf("全部徽章应排在佩戴徽章右侧：worn=%+v all=%+v", worn.Bounds, plain.Bounds)
	}
	if dy := plain.Bounds.Y - worn.Bounds.Y; dy < -2 || dy > 2 {
		t.Errorf("两段没有排在同一行：dy=%.1f", dy)
	}
	if gap := plain.Bounds.X - (worn.Bounds.X + worn.Bounds.W); gap < 12 {
		t.Errorf("两段之间没有留出分割线：gap=%.1f", gap)
	}

	// 指针停在全部徽章的小图上：tooltip 浮层出现，文字就是徽章说明。
	tt.Move(plain.Bounds.X+plain.Bounds.W/2, plain.Bounds.Y+plain.Bounds.H/2)
	tt.Advance(800 * time.Millisecond)
	if tip, ok := tt.AccessByRole(ui.RoleTooltip, "全勤：一年没有断签"); !ok {
		t.Errorf("悬浮徽章小图没有出现说明 tooltip：%q", tt.Texts())
	} else if tip.Bounds.W <= 0 {
		t.Errorf("tooltip 没有可见面积：%+v", tip.Bounds)
	}
}

// 左右两侧各自独立：没佩戴画「暂未佩戴」，没有徽章画「暂无徽章」，都是
// Lobe 的淡色虚线 tag；还没读完的那一侧留空，不把读取中说成没有。
func TestHeaderBadgeRowEmptyTags(t *testing.T) {
	a := testApp(t)

	both := ui.NewTester(func(c *ui.Context) {
		a.headerBadgeRow(c, nil, nil, true, true, nil)
	}, 300, 60)
	both.Frame()
	for _, label := range []string{"暂未佩戴", "暂无徽章"} {
		if !both.HasText(label) {
			t.Errorf("两侧都为空时应画 %q：%q", label, both.Texts())
		}
	}
	worn, ok := both.Find("暂未佩戴")
	if !ok {
		t.Fatal("找不到「暂未佩戴」")
	}
	all, ok := both.Find("暂无徽章")
	if !ok {
		t.Fatal("找不到「暂无徽章」")
	}
	if all.X <= worn.X+worn.W {
		t.Errorf("「暂未佩戴」应排在「暂无徽章」左侧：worn=%+v all=%+v", worn, all)
	}
	if dy := all.Y - worn.Y; dy < -2 || dy > 2 {
		t.Errorf("两个空状态 tag 没有排在同一行：dy=%.1f", dy)
	}
	// tag 的 Find 给的是文字边界（不含左右内边距），分割线再加两侧 Gap。
	if gap := all.X - (worn.X + worn.W); gap < 12 {
		t.Errorf("两个空状态之间没有留出分割线：gap=%.1f", gap)
	}

	for _, tc := range []struct {
		name, want, absent  string
		wornReady, allReady bool
	}{
		{"两侧都还没读完", "", "暂未佩戴 暂无徽章", false, false},
		{"只读完佩戴侧", "暂未佩戴", "暂无徽章", true, false},
		{"只读完徽章墙", "暂无徽章", "暂未佩戴", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tt := ui.NewTester(func(c *ui.Context) {
				a.headerBadgeRow(c, nil, nil, tc.wornReady, tc.allReady, nil)
			}, 300, 60)
			tt.Frame()
			if tc.want != "" && !tt.HasText(tc.want) {
				t.Errorf("应画 %q：%q", tc.want, tt.Texts())
			}
			for _, label := range strings.Fields(tc.absent) {
				if tt.HasText(label) {
					t.Errorf("不该画 %q：%q", label, tt.Texts())
				}
			}
		})
	}
}

// 一侧是图标、另一侧是空 tag 时，分割线仍在同一个流动行里把两段隔开。
func TestHeaderBadgeRowDividerWithEmptySide(t *testing.T) {
	a := testApp(t)
	const iconURL = "https://r2.2libra.com/badge/svg/year_streak.svg"
	a.images.put(iconURL, solidBitmap(22, 15, marker))
	equipped := []badgeInfo{{Name: "全勤", IconURL: iconURL}}

	// 有佩戴、徽章墙为空。
	onlyWorn := ui.NewTester(func(c *ui.Context) {
		a.headerBadgeRow(c, equipped, nil, true, true, nil)
	}, 300, 60)
	onlyWorn.Frame()
	icon, ok := onlyWorn.Find("全勤")
	if !ok {
		t.Fatalf("没有画出佩戴徽章：%q", onlyWorn.Texts())
	}
	tag, ok := onlyWorn.Find("暂无徽章")
	if !ok {
		t.Fatalf("没有画出「暂无徽章」：%q", onlyWorn.Texts())
	}
	if dy := tag.Y - icon.Y; dy < -2 || dy > 2 {
		t.Errorf("佩戴徽章与空状态不在同一行：dy=%.1f", dy)
	}
	if gap := tag.X - (icon.X + icon.W); gap < 12 {
		t.Errorf("佩戴徽章与空状态之间没有分割线：gap=%.1f", gap)
	}

	// 没佩戴、徽章墙有徽章。
	onlyAll := ui.NewTester(func(c *ui.Context) {
		a.headerBadgeRow(c, nil, []badgeWallEntry{
			{Name: "全勤", IconURL: iconURL},
		}, true, true, nil)
	}, 300, 60)
	onlyAll.Frame()
	tag, ok = onlyAll.Find("暂未佩戴")
	if !ok {
		t.Fatalf("没有画出「暂未佩戴」：%q", onlyAll.Texts())
	}
	icon, ok = onlyAll.Find("全勤")
	if !ok {
		t.Fatalf("没有画出徽章墙的徽章：%q", onlyAll.Texts())
	}
	if dy := icon.Y - tag.Y; dy < -2 || dy > 2 {
		t.Errorf("空状态与徽章墙徽章不在同一行：dy=%.1f", dy)
	}
	if gap := icon.X - (tag.X + tag.W); gap < 12 {
		t.Errorf("空状态与徽章墙徽章之间没有分割线：gap=%.1f", gap)
	}
}

// 个人中心与用户资料的资料头都在两行身份信息下面补上徽章行，两侧空状态各
// 有自己的文案。
func TestProfileHeaderShowsEmptyBadgeTags(t *testing.T) {
	a := profileWindowApp(t)
	a.profile.badgeLoaded = true
	tt := ui.NewTester(func(c *ui.Context) { a.profileHeader(c, a.profile) }, profileWindowWidth, 180)
	tt.Frame()
	for _, label := range []string{"暂未佩戴", "暂无徽章"} {
		if !tt.HasText(label) {
			t.Errorf("个人中心资料头缺少 %q：%q", label, tt.Texts())
		}
	}
	tag, ok := tt.Find("暂无徽章")
	if !ok {
		t.Fatalf("个人中心资料头缺少徽章行：%q", tt.Texts())
	}
	name, ok := tt.Find("吉米")
	if !ok {
		t.Fatalf("资料头没有昵称：%q", tt.Texts())
	}
	if tag.Y <= name.Y {
		t.Errorf("徽章行没有落在两行身份信息下方：昵称 y=%.0f，徽章行 y=%.0f", name.Y, tag.Y)
	}
}

func TestUserCardHeaderShowsEmptyBadgeTags(t *testing.T) {
	a := userCardTestApp(t)
	a.userCard.badges.loaded = true
	tt := ui.NewTester(func(c *ui.Context) { a.userCardHeader(c, a.userCard) }, userCardWindowWidth, 200)
	tt.Frame()
	for _, label := range []string{"暂未佩戴", "暂无徽章"} {
		if !tt.HasText(label) {
			t.Errorf("用户资料资料头缺少 %q：%q", label, tt.Texts())
		}
	}
	tag, ok := tt.Find("暂无徽章")
	if !ok {
		t.Fatalf("用户资料资料头缺少徽章行：%q", tt.Texts())
	}
	name, ok := tt.Find("爱丽丝")
	if !ok {
		t.Fatalf("资料头没有昵称：%q", tt.Texts())
	}
	if tag.Y <= name.Y {
		t.Errorf("徽章行没有落在两行身份信息下方：昵称 y=%.0f，徽章行 y=%.0f", name.Y, tag.Y)
	}
}

/* ------------------------------------------------------------------
   加载时机：徽章行依赖徽章墙，窗口打开就补齐，不等切到徽章标签页
---------------------------------------------------------------- */

func TestProfileBadgesLoadOnOpenTab(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/users/jimmy":
			fmt.Fprint(w, envelopeJSON(`{"username":"jimmy"}`))
		case "/api/v1/me/badges":
			fmt.Fprint(w, envelopeJSON(`[]`))
		case "/api/v1/me/badges/equipped":
			fmt.Fprint(w, envelopeJSON(`[]`))
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
	if a.profile.tab == profileTabBadges {
		t.Fatal("前置条件：默认不该停在徽章标签页")
	}
	a.ensureProfileLoaded()
	waitFor(t, func() bool { return a.profile.badgeLoaded }, "窗口打开后没有读徽章墙")

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(paths, ",")
	for _, want := range []string{"/api/v1/me/badges", "/api/v1/me/badges/equipped"} {
		if !strings.Contains(joined, want) {
			t.Errorf("没有请求 %s：%v", want, paths)
		}
	}
}

func TestUserCardBadgesLoadOnOpenTab(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/users/alice":
			fmt.Fprint(w, envelopeJSON(`{"username":"alice"}`))
		case "/api/v1/users/alice/badges", "/api/v1/users/alice/badges/equipped":
			fmt.Fprint(w, envelopeJSON(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := testApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	a.userCard = newUserCardState("alice")
	if a.userCard.tab == userCardTabBadges {
		t.Fatal("前置条件：默认不该停在徽章标签页")
	}
	a.ensureUserCardLoaded()
	waitFor(t, func() bool { return a.userCard.badges.loaded }, "窗口打开后没有读徽章墙")

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(paths, ",")
	for _, want := range []string{"/api/v1/users/alice/badges", "/api/v1/users/alice/badges/equipped"} {
		if !strings.Contains(joined, want) {
			t.Errorf("没有请求 %s：%v", want, paths)
		}
	}
}
