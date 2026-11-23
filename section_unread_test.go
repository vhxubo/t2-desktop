package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 未读数接口的分区分项要与文档字段名对齐：长名来自 changelog，api-read.md
// 正文用了简写，两种都要认；未知字段忽略，裸数字按总数处理。
func TestDecodeNotificationUnreadCounts(t *testing.T) {
	got := decodeNotificationUnreadCounts(json.RawMessage(`{
		"unread_count": 9, "badge_unread_count": 2, "follow_unread_count": 3,
		"home_unread_count": 5, "community_unread_count": 7,
		"watch_content_count": 11, "some_new_field": 99
	}`))
	want := notificationUnreadCounts{Total: 9, Badge: 2, Follow: 3, Home: 5, Community: 7, WatchContent: 11}
	if got != want {
		t.Fatalf("分项解析 = %+v，期望 %+v", got, want)
	}

	short := decodeNotificationUnreadCounts(json.RawMessage(`{
		"unread": 4, "badge": 1, "follow": 2, "home": 3, "community": 4, "watch": 5
	}`))
	if short.Home != 3 || short.Community != 4 || short.WatchContent != 5 {
		t.Errorf("简写字段名没认出来：%+v", short)
	}
	if bare := decodeNotificationUnreadCounts(json.RawMessage(`6`)); bare.Total != 6 {
		t.Errorf("裸数字总数 = %d，期望 6", bare.Total)
	}
}

// 读取基线只在有记录时带给接口；没有记录就不带参数，不伪造「现在」清零计数。
func TestNotificationUnreadCountPath(t *testing.T) {
	if got := notificationUnreadCountPath("", ""); got != notificationUnreadPath {
		t.Errorf("无基线路径 = %q，期望 %q", got, notificationUnreadPath)
	}
	got := notificationUnreadCountPath("2026-11-01T00:00:00Z", "2026-11-02T00:00:00Z")
	if !strings.HasPrefix(got, notificationUnreadPath+"?") ||
		!strings.Contains(got, "home_last_read_at=2026-11-01T00%3A00%3A00Z") ||
		!strings.Contains(got, "community_last_read_at=2026-11-02T00%3A00%3A00Z") {
		t.Errorf("带基线的路径不对：%q", got)
	}
}

// 分区计数映射到侧边栏浏览页：首页读 home，社区讨论读 community，动态读
// 关注流专用端点的 followUnread；其它浏览页不挂角标。
func TestNavBadgeCountFromSectionUnread(t *testing.T) {
	a := testApp(t)
	a.sectionUnread = notificationUnreadCounts{Home: 5, Community: 7}
	a.followUnread = 3

	want := make(map[int]int, len(tabSpecs))
	for idx, s := range tabSpecs {
		want[idx] = 0
		switch {
		case s.Path == "/v1/me/follows/timeline":
			want[idx] = 3
		case s.FilterByNode:
			want[idx] = 5
		case s.IsCommunity:
			want[idx] = 7
		}
	}
	for idx := range tabSpecs {
		if got := a.navBadgeCount(tabID(idx)); got != want[idx] {
			t.Errorf("%s 角标 = %d，期望 %d", tabSpecs[idx].Label, got, want[idx])
		}
	}
}

// 展开态与折叠图标轨都画同一份红点计数；数字写进角标而不是文案。
func TestSidebarSectionUnreadBadges(t *testing.T) {
	for _, tc := range []struct {
		count int
		row   string
		set   func(*app, int)
	}{
		{count: 37, row: "首页", set: func(a *app, n int) { a.sectionUnread.Home = n }},
		{count: 41, row: "社区讨论", set: func(a *app, n int) { a.sectionUnread.Community = n }},
	} {
		a := testApp(t)
		tc.set(a, tc.count)
		for _, expanded := range []bool{true, false} {
			a.navExpanded = expanded
			tt := ui.NewTester(a.view, 1100, 760)
			tt.Frame()
			row, ok := tt.Find(tc.row)
			if !ok {
				t.Fatalf("展开=%v 时侧边栏没有 %q：%q", expanded, tc.row, tt.Texts())
			}
			dot, ok := tt.Find(strconv.Itoa(tc.count))
			if !ok {
				t.Fatalf("展开=%v 时 %q 没有计数 %d：%q", expanded, tc.row, tc.count, tt.Texts())
			}
			if dot.Y < row.Y-20 || dot.Y > row.Y+row.H+20 {
				t.Errorf("展开=%v 时计数 y=%.0f 不在 %q 行 y=%.0f 附近", expanded, dot.Y, tc.row, row.Y)
			}
			if expanded {
				// 展开态数值落在行右端的 Extra 槽位，不再挂在图标角上。
				if dot.X < row.X+row.W-60 {
					t.Errorf("展开态计数 x=%.0f 不在 %q 行右侧（行右缘 %.0f）", dot.X, tc.row, row.X+row.W)
				}
			} else {
				// 折叠态才是图标角标，紧贴左侧图标轨。
				if dot.X > row.X+row.W+20 {
					t.Errorf("折叠态角标 x=%.0f 离图标轨太远（行右缘 %.0f）", dot.X, row.X+row.W)
				}
			}
		}
	}
}

// 折叠角标底色取 Lobe 的 Idle 灰，也就是 Badge 默认状态用的 TextQuaternary：
// 浅色 RGB(176,176,176)，深色 RGB(107,107,107)。不用错误红，也不引入品牌色。
func TestNavBadgeNeutralGrayInBothThemes(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a := testApp(t)
		a.navExpanded = false
		a.sectionUnread.Home = 37
		tt := ui.NewTesterWith(a.view, 1100, 760, ui.TesterOptions{Dark: dark})
		tt.Frame()
		dot, ok := tt.Find("37")
		if !ok {
			t.Fatalf("dark=%v 侧边栏没有角标：%q", dark, tt.Texts())
		}
		im := tt.Image()
		x, y := int(dot.X)-3, int(dot.Y+dot.H/2)
		if x < 0 || y < 0 || x >= im.Bounds().Dx() || y >= im.Bounds().Dy() {
			t.Fatalf("取样点越界：%d,%d", x, y)
		}
		r, g, b, _ := im.At(x, y).RGBA()
		rr, gg, bb := int(r>>8), int(g>>8), int(b>>8)
		want := 176
		if dark {
			want = 107
		}
		if rr < want-2 || rr > want+2 || gg < want-2 || gg > want+2 || bb < want-2 || bb > want+2 {
			t.Errorf("dark=%v 角标底色 = RGB(%d,%d,%d)，期望 Idle 灰 %d", dark, rr, gg, bb, want)
		}
	}
}

// 打开分区页推进读取基线并清掉角标；基线按账号作用域落盘，下次读得回来。
func TestMarkTabReadClearsSectionBadge(t *testing.T) {
	a := testApp(t)
	a.sectionUnread = notificationUnreadCounts{Home: 5, Community: 7}
	a.markTabRead(0)
	if a.sectionUnread.Home != 0 || a.sectionUnread.Community != 7 {
		t.Fatalf("打开首页只该清掉首页角标：%+v", a.sectionUnread)
	}
	at := a.sectionLastRead(sectionHome)
	if at.IsZero() || time.Since(at) > time.Minute {
		t.Errorf("首页读取基线没记下来：%v", at)
	}

	idx := -1
	for i, s := range tabSpecs {
		if s.IsCommunity {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatal("找不到社区讨论 tab")
	}
	a.markTabRead(idx)
	if a.sectionUnread.Community != 0 {
		t.Errorf("打开社区讨论后角标没清掉：%+v", a.sectionUnread)
	}
	if a.sectionLastRead(sectionCommunity).IsZero() {
		t.Error("社区讨论的读取基线没记下来")
	}
}

// 未读数刷新把分项一并写回状态，铃铛仍读总数；基线随请求带上。
func TestRefreshUnreadStoresSectionCounts(t *testing.T) {
	var gotURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.URL.RequestURI()
		_, _ = w.Write([]byte(`{"c":0,"d":{"unread_count":9,"home_unread_count":5,
			"community_unread_count":7,"follow_unread_count":3}}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.autoLoad = true
	a.client.HTTP = srv.Client()
	now := time.Now().UTC()
	if err := a.kv.put(a.sectionReadKey(sectionHome), sectionReadRecord{At: now}, now); err != nil {
		t.Fatal(err)
	}
	a.refreshUnread()
	waitFor(t, func() bool { return a.sectionUnread.Home == 5 && a.unread == 9 }, "分区未读数写回")
	if a.sectionUnread.Community != 7 || a.sectionUnread.Follow != 3 {
		t.Errorf("其它分项没写回：%+v", a.sectionUnread)
	}
	if !strings.Contains(gotURI, "home_last_read_at=") {
		t.Errorf("刷新没有带上首页读取基线：%q", gotURI)
	}
}
