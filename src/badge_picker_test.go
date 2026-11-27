package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

/* ------------------------------------------------------------------
   资料头：点击佩戴区打开徽章切换弹窗（仅本人）
---------------------------------------------------------------- */

// 个人中心里点「暂未佩戴」或佩戴中的徽章，打开 Lobe Modal 列出徽章墙。
func TestProfileBadgePickerOpensAndLists(t *testing.T) {
	a := profileWindowApp(t)
	ps := a.profile
	ps.badgeLoaded = true
	ps.badges = []badgeWallEntry{{
		BadgeID: "b1", Name: "全勤", Description: "一年没有断签", Equipped: true,
	}}
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()

	if _, ok := tt.Find("切换佩戴徽章"); !ok {
		t.Fatalf("个人中心资料头没有可点的佩戴区：%q", tt.Texts())
	}
	if err := tt.Click("切换佩戴徽章"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !ps.badgePickerOpen || ps.badgePickerFromCard {
		t.Fatalf("弹窗没有从个人中心打开：open=%v fromCard=%v", ps.badgePickerOpen, ps.badgePickerFromCard)
	}
	for _, want := range []string{"切换佩戴的徽章", "取消佩戴「全勤」", "完成"} {
		if !tt.HasText(want) {
			t.Errorf("弹窗缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 自己的资料卡同样能点开；弹窗画在资料卡这扇窗口里，不占个人中心。
func TestUserCardBadgePickerOnlyForSelf(t *testing.T) {
	// 自己的资料卡：有入口，能打开。
	self := userCardTestApp(t)
	if err := self.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "alice"}); err != nil {
		t.Fatal(err)
	}
	self.userCard.badges.loaded = true
	self.userCard.badges.entries = []badgeWallEntry{{BadgeID: "b1", Name: "全勤"}}
	selfTT := ui.NewTester(self.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	selfTT.Frame()
	if _, ok := selfTT.Find("切换佩戴徽章"); !ok {
		t.Fatalf("自己的资料卡没有佩戴区入口：%q", selfTT.Texts())
	}
	if err := selfTT.Click("切换佩戴徽章"); err != nil {
		t.Fatal(err)
	}
	selfTT.Frame()
	if !self.profile.badgePickerOpen || !self.profile.badgePickerFromCard {
		t.Fatalf("弹窗没有从资料卡打开：open=%v fromCard=%v",
			self.profile.badgePickerOpen, self.profile.badgePickerFromCard)
	}
	if !selfTT.HasText("切换佩戴的徽章") || !selfTT.HasText("佩戴「全勤」") {
		t.Errorf("资料卡里没有画出弹窗内容：%q", selfTT.Texts())
	}

	// 别人的资料卡：只展示，不挂切换事件。
	other := userCardTestApp(t)
	other.userCard.badges.loaded = true
	other.userCard.badges.entries = []badgeWallEntry{{BadgeID: "b1", Name: "全勤"}}
	otherTT := ui.NewTester(other.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	otherTT.Frame()
	if _, ok := otherTT.Find("切换佩戴徽章"); ok {
		t.Error("别人的资料卡不该有切换徽章入口")
	}
	if other.profile.badgePickerOpen {
		t.Error("别人的资料卡不该打开切换弹窗")
	}
}

// 弹窗里的按钮复用佩戴提交：打到文档端点，成功后资料头同步跟上。
func TestBadgePickerToggleSubmits(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	equipped := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/me/badges":
			fmt.Fprint(w, envelopeJSON(`[{"id":"ub1","badge_id":"b1","badge":{"id":"b1","name":"全勤"}}]`))
		case r.URL.Path == "/api/v1/me/badges/equipped":
			body := `[]`
			mu.Lock()
			if equipped {
				body = `[{"badge_id":"b1"}]`
			}
			mu.Unlock()
			fmt.Fprint(w, envelopeJSON(body))
		case r.URL.Path == "/api/v1/me":
			fmt.Fprint(w, envelopeJSON(`{"username":"jimmy","equipped_badges":[{"badge":{"name":"全勤"}}]}`))
		case r.URL.Path == "/api/v1/users/jimmy":
			fmt.Fprint(w, envelopeJSON(`{"username":"jimmy"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/me/badges/b1/equip":
			mu.Lock()
			equipped = true
			mu.Unlock()
			fmt.Fprint(w, envelopeJSON(`{"message":"佩戴成功"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := profileWindowApp(t)
	a.client.Base = srv.URL + "/api"
	ps := a.profile
	ps.badgeLoaded = true
	ps.badges = []badgeWallEntry{{BadgeID: "b1", Name: "全勤"}}
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if err := tt.Click("切换佩戴徽章"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("佩戴「全勤」"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitProfile(t, "佩戴提交完成", func() bool {
		return ps.badgeBusyID == "" && len(ps.user.Badges) == 1
	})
	mu.Lock()
	defer mu.Unlock()
	var posted bool
	for _, c := range calls {
		if c == "POST /api/v1/me/badges/b1/equip" {
			posted = true
		}
	}
	if !posted {
		t.Errorf("弹窗里的佩戴按钮没有打到文档端点：%v", calls)
	}
}
