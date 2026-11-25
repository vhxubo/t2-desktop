package main

import (
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

/* ------------------------------------------------------------------
   端点契约与解析
----------------------------------------------------------------- */

// 「我的」窗口里的列表必须打到文档里的端点，且第 1 页参数原样保留。
func TestProfileEndpointsMatchContract(t *testing.T) {
	cases := []struct {
		kind int
		spec tabSpec
		want string
	}{
		{profileContentTopics, profileTopicsSpec("jimmy", 1),
			"https://2libra.com/api/v1/users/jimmy/topics?page=1&limit=50"},
		{profileContentFavorites, profileContentSpecs[profileContentFavorites],
			"https://2libra.com/api/v1/me/favorites?page=1&limit=50"},
		{profileContentBookmarks, profileContentSpecs[profileContentBookmarks],
			"https://2libra.com/api/v1/me/bookmarks?page=1&limit=100"},
		{profileContentCollections, profileContentSpecs[profileContentCollections],
			"https://2libra.com/api/v1/me/collections"},
	}
	for _, c := range cases {
		if got := endpoint(apiBaseURL, c.spec, 1); got != c.want {
			t.Errorf("%s URL = %s，期望 %s", profileContentLabels[c.kind], got, c.want)
		}
	}
	// 帖子 / 收藏 / 书签按页；合集整页返回。
	for _, c := range cases {
		wantPaging := pagingPages
		if c.kind == profileContentCollections {
			wantPaging = pagingOff
		}
		if c.spec.Paging != wantPaging || !c.spec.NeedsAuth {
			t.Errorf("%s 的分页 / 登录约定不对：%+v", c.want, c.spec)
		}
	}
	// 帖子列表的用户名要转义，第 2 页只换 page。
	if got, want := endpoint(apiBaseURL, profileTopicsSpec("a/b", 2), 2),
		"https://2libra.com/api/v1/users/a%2Fb/topics?limit=50&page=2"; got != want {
		t.Errorf("我的帖子第 2 页 URL = %s，期望 %s", got, want)
	}
	if got, want := endpoint(apiBaseURL, profileRepliesSpec("jimmy", 1), 1),
		"https://2libra.com/api/v1/users/jimmy/replies?page=1&limit=50"; got != want {
		t.Errorf("我的回复 URL = %s，期望 %s", got, want)
	}
	// 第 2 页只改 page，不引入其它参数。
	if got, want := endpoint(apiBaseURL, profileRepliesSpec("jimmy", 2), 2),
		"https://2libra.com/api/v1/users/jimmy/replies?limit=50&page=2"; got != want {
		t.Errorf("我的回复第 2 页 URL = %s，期望 %s", got, want)
	}
	// 昵称里的特殊字符必须转义，不能改变请求路径。
	if got, want := endpoint(apiBaseURL, profileRepliesSpec("a/b", 1), 1),
		"https://2libra.com/api/v1/users/a%2Fb/replies?page=1&limit=50"; got != want {
		t.Errorf("昵称转义不对：%s，期望 %s", got, want)
	}
}

// 公开资料接口字段名有几种历史写法：认得的用、不认识的忽略。
func TestDecodeAccountProfile(t *testing.T) {
	d := json.RawMessage(`{
		"username": "jimmy", "display_name": "吉米", "avatar_url": "https://x/a.png",
		"user_number": 123456, "created_at": "2024-02-01T08:00:00Z", "level": 7,
		"is_pro": true, "follower_count": 12, "following_count": 3, "home_view": 1024
	}`)
	p := decodeAccountProfile(d)
	if p.Name != "jimmy" || p.DisplayName != "吉米" || p.Avatar != "https://x/a.png" {
		t.Errorf("身份字段解析不对：%+v", p)
	}
	if p.UserNumber != 123456 || p.Level != 7 || !p.IsPro {
		t.Errorf("编号 / 等级 / PRO 解析不对：%+v", p)
	}
	if p.Followers != 12 || p.Following != 3 || p.HomeView != 1024 {
		t.Errorf("统计字段解析不对：%+v", p)
	}
	if p.CreatedAt != "2024-02-01T08:00:00Z" {
		t.Errorf("注册时间解析不对：%q", p.CreatedAt)
	}

	// 账号包在 user 里、且用驼峰写法时，同样能认出来。
	nested := decodeAccountProfile(json.RawMessage(`{"user":{"username":"amy","userNumber":42,"isPro":true}}`))
	if nested.Name != "amy" || nested.UserNumber != 42 || !nested.IsPro {
		t.Errorf("嵌套账号解析不对：%+v", nested)
	}
}

// 注册天数按自然日算：注册当天记为第 1 天，解析不了的时间不编数字。
func TestProfileRegisteredText(t *testing.T) {
	old := profileNow
	defer func() { profileNow = old }()
	profileNow = func() time.Time {
		return time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	}
	if got, want := profileRegisteredText("2024-02-01T08:00:00Z"), "注册 2024-02-01 · 30 天"; got != want {
		t.Errorf("注册天数 = %q，期望 %q", got, want)
	}
	// 时区不同也不该算出负数天。
	if got, want := profileRegisteredText("2024-03-05T08:00:00Z"), "注册 2024-03-05 · 1 天"; got != want {
		t.Errorf("未来时间 = %q，期望 %q", got, want)
	}
	if got := profileRegisteredText("不是时间"); got == "" || got == "注册时间 " {
		t.Errorf("无法解析的注册时间没有给出说明：%q", got)
	}
	if got := profileRegisteredText(""); got != "" {
		t.Errorf("没有注册时间时不该显示占位：%q", got)
	}
}

// 资料头那一行是「用户 ID | 注册时间」，缺少编号时用破折号而不是 0。
func TestProfileIdentityLine(t *testing.T) {
	old := profileNow
	defer func() { profileNow = old }()
	profileNow = func() time.Time { return time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC) }

	got := profileIdentityLine(accountProfile{UserNumber: 123456, CreatedAt: "2024-02-01T00:00:00Z"})
	if want := "用户 ID: #123456  |  注册 2024-02-01 · 30 天"; got != want {
		t.Errorf("资料行 = %q，期望 %q", got, want)
	}
	if got := profileIdentityLine(accountProfile{}); got != "用户 ID: —" {
		t.Errorf("没有资料时应只显示占位：%q", got)
	}
}

/* ------------------------------------------------------------------
   窗口界面
----------------------------------------------------------------- */

// profileWindowApp 造一份已登录、四块数据都已到位的状态。窗口由测试直接
// 渲染 profileWindowView，headless 里不创建真实窗口。
func profileWindowApp(t *testing.T) *app {
	t.Helper()
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok",
		TokenType:   "Bearer",
		Username:    "jimmy",
		Scope:       "read profile read:me write:comment read:economy",
	}); err != nil {
		t.Fatal(err)
	}
	ps := a.profile
	ps.infoLoaded = true
	ps.user = accountProfile{
		Name:        "jimmy",
		DisplayName: "吉米",
		UserNumber:  123456,
		CreatedAt:   "2024-02-01T08:00:00Z",
		Level:       7,
		Followers:   12,
		Following:   3,
		HomeView:    1024,
	}
	topics := &ps.content[profileContentTopics]
	topics.loaded = true
	topics.items = []Item{
		{Key: "T1", ShortID: "T1", Title: "第一篇帖子", SubtitleSuffix: " · 2 天前", Meta: "3 回复 · 10 浏览"},
		{Key: "T2", ShortID: "T2", Title: "第二篇帖子", SubtitleSuffix: " · 5 天前", Meta: "1 回复"},
	}
	ps.replyLoaded = true
	ps.replyTotal = 2
	ps.replies = []Item{
		{Key: "R1", ShortID: "T1", CommentID: "R1", Title: "我的一条回复",
			Extra: &rowExtra{PostTitle: "第一篇帖子", Floor: 2, When: "昨天"}},
	}
	return a
}

// 窗口第一屏：资料头、四个标签页与概览里的统计区块。
func TestProfileWindowView(t *testing.T) {
	a := profileWindowApp(t)
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	for _, want := range []string{
		"个人中心", "吉米", "Lv.7",
		"用户 ID: #123456", "注册 2024-02-01",
		"概览", "个人介绍", "内容", "资产", "徽章", "设置",
		"最近统计", "粉丝", "关注", "主页访问", "等级",
	} {
		if !tt.HasText(want) {
			t.Errorf("「个人中心」窗口缺少文案 %q：%q", want, tt.Texts())
		}
	}
	// 概览底部不再放授权说明与「查看当前权限」，授权入口收在「权限与账号」页。
	for _, gone := range []string{"授权管理", "查看当前权限"} {
		if tt.HasText(gone) {
			t.Errorf("概览不该再展示 %q：%q", gone, tt.Texts())
		}
	}
	// 窗口自带标题栏按钮。
	for _, label := range []string{"最小化", "关闭"} {
		if _, ok := tt.Find(label); !ok {
			t.Errorf("「我的」窗口缺少「%s」按钮：%q", label, tt.Texts())
		}
	}
	// 四个标签页都待在窗口宽度之内。
	for _, label := range profileTabLabels {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("缺少标签页 %q：%q", label, tt.Texts())
		}
		if r.X+r.W > profileWindowWidth {
			t.Errorf("标签页 %q 的 x=%.0f 宽 %.0f，超出 %d 宽的窗口",
				label, r.X, r.W, profileWindowWidth)
		}
	}
}

// 资料头里的等级紧跟昵称，和昵称同一行并留出这一行的 Gap 间距。只渲染资料头：
// 窗口标题栏上的昵称与头像占位也会带同一份文字，单独画才找得到昵称那一行。
func TestProfileLevelFollowsNickname(t *testing.T) {
	a := profileWindowApp(t)
	// 头像占位只取前两个字符「JI」，昵称因此是唯一的一处文字。
	a.profile.user.DisplayName = "Jimmy测试"
	tt := ui.NewTester(func(c *ui.Context) { a.profileHeader(c, a.profile) }, profileWindowWidth, 120)
	tt.Frame()
	name, ok := tt.Find("Jimmy测试")
	if !ok {
		t.Fatalf("找不到昵称：%q", tt.Texts())
	}
	level, ok := tt.Find("Lv.7")
	if !ok {
		t.Fatalf("找不到等级：%q", tt.Texts())
	}
	t.Logf("昵称 %+v，等级 %+v", name, level)
	if level.X <= name.X {
		t.Errorf("等级该在昵称后面：昵称 %+v，等级 %+v", name, level)
	}
	gap := level.X - (name.X + name.W)
	if gap <= 0 || gap > 16 {
		t.Errorf("昵称与等级的横向间距只有 %.0f，期望同行且留出这行的 gap", gap)
	}
	dy := level.Y - name.Y
	if dy < -12 || dy > 12 {
		t.Errorf("等级与昵称不在同一行：昵称 y=%.0f，等级 y=%.0f", name.Y, level.Y)
	}
}

// 六个一级标签在最窄窗口下也不能溢出。
func TestProfileTabsFitMinWidth(t *testing.T) {
	a := profileWindowApp(t)
	tt := ui.NewTester(a.profileWindowView, profileWinMinWidth, profileWinMinHeight)
	tt.Frame()
	for _, label := range profileTabLabels {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("缺少标签页 %q：%q", label, tt.Texts())
		}
		if r.X+r.W > float32(profileWinMinWidth) {
			t.Errorf("标签页 %q 超出最窄窗口：x=%.0f 宽 %.0f", label, r.X, r.W)
		}
	}
}

// 「我的」窗口的标题栏与页面底色都取 Lobe token（浅色是 BgLayout 默认值），
// 不再用原生主题的灰色 Surface；设置窗口用的是同一支颜色。
func TestProfileWindowUsesLobeTokens(t *testing.T) {
	a := profileWindowApp(t)
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	im := tt.Image()
	want := lobeCfg.Tokens(false).BgLayout
	for _, p := range [][2]int{
		{300, 1},
		{300, titleBarHeight / 2},
		{300, titleBarHeight - 1},
	} {
		got := im.RGBAAt(p[0], p[1])
		if got.R != want.R || got.G != want.G || got.B != want.B {
			t.Errorf("(%d,%d) 底色 = %v，期望 Lobe BgLayout %v", p[0], p[1], got, want)
		}
	}
}

// 金币相关的入口都不在「我的」窗口里：余额、签到、金币明细与全部流水
// 都随金币页一起搬去了独立的金币窗口。
func TestProfileHasNoCoinEntries(t *testing.T) {
	a := profileWindowApp(t)
	a.coins.balance, a.coins.balanceOK = 1280, true
	a.coins.sign = coinSignStatus{Known: true, Signed: true, Streak: 5}
	opened := 0
	a.onCoinsOpen = func() { opened++ }
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	for _, forbidden := range []string{
		"金币余额", "今日签到", "金币明细", "展开最近记录", "全部流水",
		"1,280 金币", "今日已签到",
	} {
		if tt.HasText(forbidden) {
			t.Errorf("「我的」窗口不该再有金币入口 %q：%q", forbidden, tt.Texts())
		}
	}
	if opened != 0 {
		t.Errorf("打开「我的」窗口不该打开金币窗口：opened=%d", opened)
	}
}

// 「内容」下有发布 / 合集 / 评论三段，「资产」下有收藏 / 书签两段。四类列表
// 原先占着「我的内容」的分段，现在按自产输出与内容收集分到两个一级页里。
func TestProfileContentSegments(t *testing.T) {
	if len(profileContentSpecs) != profileContentCount ||
		len(profileContentUnits) != profileContentCount ||
		len(profileContentEmpty) != profileContentCount ||
		len(profileContentEmptyIcons) != profileContentCount {
		t.Fatalf("列表状态表长度不一致：specs=%d units=%d empty=%d icons=%d",
			len(profileContentSpecs), len(profileContentUnits),
			len(profileContentEmpty), len(profileContentEmptyIcons))
	}
	if len(profileContentLabels) != profileContentSectionCount ||
		len(profileAssetLabels) != profileAssetSectionCount ||
		len(profileSettingsLabels) != profileSettingsSectionCount {
		t.Fatalf("二级分段表长度不一致：内容=%d 资产=%d 设置=%d",
			len(profileContentLabels), len(profileAssetLabels), len(profileSettingsLabels))
	}
	a := profileWindowApp(t)
	ps := a.profile

	favorites := &ps.content[profileContentFavorites]
	favorites.loaded = true
	favorites.items = []Item{{Key: "F1", ShortID: "F1", Title: "收藏的帖子"}}
	bookmarks := &ps.content[profileContentBookmarks]
	bookmarks.loaded = true
	bookmarks.items = []Item{{Key: "B1", ShortID: "T9", CommentID: "B1", Title: "书签里的评论",
		Extra: &rowExtra{Excerpt: "书签里的评论", Author: "jimmy"}}}
	collections := &ps.content[profileContentCollections]
	collections.loaded = true
	collections.items = []Item{{Key: "C1", Title: "我的收藏夹", Subtitle: "合集", Meta: "9 个帖子"}}

	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if err := tt.Click("内容"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("第一篇帖子") {
		t.Errorf("默认分段应是发布：%q", tt.Texts())
	}
	// 每个一级页的二级分段都要待在窗口宽度之内，不能挤到窗口外面。
	for _, tc := range []struct {
		section string
		labels  []string
	}{
		{"内容", profileContentLabels[:]},
		{"资产", profileAssetLabels[:]},
		{"设置", profileSettingsLabels[:]},
	} {
		if err := tt.Click(tc.section); err != nil {
			t.Fatalf("切到 %s 失败：%v", tc.section, err)
		}
		tt.Frame()
		for _, label := range tc.labels {
			r, ok := tt.Find(label)
			if !ok {
				t.Fatalf("%s 页缺少二级分段 %q：%q", tc.section, label, tt.Texts())
			}
			if r.X+r.W > profileWindowWidth {
				t.Errorf("%s - %s 超出窗口：x=%.0f 宽 %.0f", tc.section, label, r.X, r.W)
			}
		}
	}

	for _, c := range []struct{ section, segment, want string }{
		{"内容", "合集", "我的收藏夹"},
		{"内容", "评论", "我的一条回复"},
		{"资产", "收藏", "收藏的帖子"},
		{"资产", "书签", "书签里的评论"},
	} {
		if err := tt.Click(c.section); err != nil {
			t.Fatalf("切到 %s 失败：%v", c.section, err)
		}
		tt.Frame()
		if err := tt.Click(c.segment); err != nil {
			t.Fatalf("切到 %s - %s 失败：%v", c.section, c.segment, err)
		}
		tt.Frame()
		if !tt.HasText(c.want) {
			t.Errorf("%s - %s 里看不到 %q：%q", c.section, c.segment, c.want, tt.Texts())
		}
	}
}

// 「发布」分段、合集筛选与列表是三层关键部分，彼此留出 Lobe 的纵向
// 间距：分段选中线不该连到下拉框，首行帖子也不该顶着筛选控件。
func TestProfileContentGaps(t *testing.T) {
	a := profileWindowApp(t)
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if err := tt.Click("内容"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	filter, ok := tt.Find("按合集名筛选帖子")
	if !ok {
		t.Fatalf("找不到合集筛选控件：%q", tt.Texts())
	}
	first, ok := tt.Find("第一篇帖子")
	if !ok {
		t.Fatalf("找不到第一篇帖子：%q", tt.Texts())
	}
	gap := first.Y - (filter.Y + filter.H)
	t.Logf("筛选控件底 y=%.0f，首行帖子顶 y=%.0f，间距 %.0f", filter.Y+filter.H, first.Y, gap)
	if gap < 12 {
		t.Errorf("合集筛选与帖子列表之间的间距只有 %.0f，期望至少 12", gap)
	}
}

/* ------------------------------------------------------------------
   二级分段的滚动
---------------------------------------------------------------- */

// assertProfileTabScrolls 用长列表断言某个二级分段能滚动：首屏只看见头部，
// 滚到底才看见尾巴。根节点若只给百分比宽度，列表会按全部内容撑高，行画到
// 窗口外面，滚轮也跟着失效。ScrollArea 会把屏外节点裁成 0 高，ui.List 则
// 根本不画它们，所以这里按可见矩形判断，而不是按有没有这个 label。
func assertProfileTabScrolls(t *testing.T, a *app, head, tail string) {
	t.Helper()
	visible := func(text string, r ui.Rect, ok bool) bool {
		return ok && r.H > 0 && r.Y >= 0 && r.Y < float32(profileWindowHeight)
	}
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if r, ok := tt.Find(head); !visible(head, r, ok) {
		t.Fatalf("首屏没画出列表头部 %q：%q", head, tt.Texts())
	}
	if r, ok := tt.Find(tail); visible(tail, r, ok) {
		t.Fatalf("没滚动就画出了列表末尾 %q（%v）：%q", tail, r, tt.Texts())
	}
	tt.Scroll(260, 400, 0, 8000)
	tt.Frame()
	if r, ok := tt.Find(tail); !visible(tail, r, ok) {
		t.Fatalf("滚动后没画出列表末尾 %q（%v）：%q", tail, r, tt.Texts())
	}
	if r, ok := tt.Find(head); visible(head, r, ok) {
		t.Errorf("滚到底后头部 %q 还在视野里：%q", head, tt.Texts())
	}
}

// 内容、资产与设置下的二级分段都要在窗口内滚动，长列表不能把后面的行画到
// 窗口外面。
func TestProfileSecondaryTabsScroll(t *testing.T) {
	t.Run("发布", func(t *testing.T) {
		a := profileWindowApp(t)
		a.profile.tab = profileTabContent
		st := &a.profile.content[profileContentTopics]
		st.items = nil
		for i := 0; i < 40; i++ {
			st.items = append(st.items, Item{Key: fmt.Sprintf("T%02d", i),
				ShortID: fmt.Sprintf("T%02d", i), Title: fmt.Sprintf("帖子第 %02d 项", i)})
		}
		assertProfileTabScrolls(t, a, "帖子第 00 项", "帖子第 39 项")
	})
	t.Run("合集", func(t *testing.T) {
		a := profileWindowApp(t)
		a.profile.tab = profileTabContent
		a.profile.contentKind = profileContentCollection
		st := &a.profile.content[profileContentCollections]
		st.items = nil
		for i := 0; i < 40; i++ {
			st.items = append(st.items, Item{Key: fmt.Sprintf("C%02d", i),
				Title: fmt.Sprintf("合集第 %02d 项", i)})
		}
		assertProfileTabScrolls(t, a, "合集第 00 项", "合集第 39 项")
	})
	t.Run("评论", func(t *testing.T) {
		a := profileWindowApp(t)
		a.profile.tab = profileTabContent
		a.profile.contentKind = profileContentComments
		a.profile.replies = nil
		for i := 0; i < 40; i++ {
			a.profile.replies = append(a.profile.replies, Item{Key: fmt.Sprintf("R%02d", i),
				ShortID: "T1", Title: fmt.Sprintf("回复第 %02d 项", i)})
		}
		assertProfileTabScrolls(t, a, "回复第 00 项", "回复第 39 项")
	})
	t.Run("收藏", func(t *testing.T) {
		a := profileWindowApp(t)
		a.profile.tab = profileTabAssets
		st := &a.profile.content[profileContentFavorites]
		st.items = nil
		for i := 0; i < 40; i++ {
			st.items = append(st.items, Item{Key: fmt.Sprintf("F%02d", i),
				ShortID: fmt.Sprintf("T%02d", i), Title: fmt.Sprintf("收藏第 %02d 项", i)})
		}
		assertProfileTabScrolls(t, a, "收藏第 00 项", "收藏第 39 项")
	})
	t.Run("书签", func(t *testing.T) {
		a := profileWindowApp(t)
		a.profile.tab = profileTabAssets
		a.profile.assetKind = profileAssetBookmarks
		st := &a.profile.content[profileContentBookmarks]
		st.items = nil
		for i := 0; i < 40; i++ {
			st.items = append(st.items, Item{Key: fmt.Sprintf("B%02d", i),
				ShortID: "T1", CommentID: fmt.Sprintf("B%02d", i),
				Title: fmt.Sprintf("书签第 %02d 项", i)})
		}
		assertProfileTabScrolls(t, a, "书签第 00 项", "书签第 39 项")
	})
	t.Run("账号与权限", func(t *testing.T) {
		a := profileWindowApp(t)
		a.profile.tab = profileTabSettings
		scopes := make([]string, 40)
		for i := range scopes {
			scopes[i] = fmt.Sprintf("scope%02d", i)
		}
		if err := a.auth.storeToken(tokenData{
			AccessToken: "tok", TokenType: "Bearer", Username: "jimmy",
			Scope: strings.Join(scopes, " "),
		}); err != nil {
			t.Fatal(err)
		}
		assertProfileTabScrolls(t, a, "scope00", "scope39")
	})
}

// 进入评论只拉第一页：列表有固定可视高度后，AtEnd 要等真正滚到底才成立。
// 根节点若被内容撑高，首帧就当成已经到底，一帧一帧把后续所有页全拉一遍。
func TestProfileCommentsLoadsFirstPageOnly(t *testing.T) {
	var mu sync.Mutex
	pages := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/jimmy/replies" {
			http.NotFound(w, r)
			return
		}
		p := r.URL.Query().Get("page")
		mu.Lock()
		pages[p]++
		mu.Unlock()
		items := make([]string, 0, 50)
		for i := 0; i < 50; i++ {
			items = append(items, fmt.Sprintf(
				`{"id":"r%s-%d","content":"回复 %s-%d","post":{"short_id":"T1","title":"帖子"}}`, p, i, p, i))
		}
		fmt.Fprintf(w, `{"c":0,"d":{"items":[%s],"total":250,"page":%s,"limit":50,"total_pages":5}}`,
			strings.Join(items, ","), p)
	}))
	defer srv.Close()

	a := testApp(t)
	a.client.Base = srv.URL + "/api"
	a.autoLoad = true
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	a.profile.tab = profileTabContent
	a.profile.contentKind = profileContentComments
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	waitProfile(t, "评论第一页", func() bool { return a.profile.replyLoaded })
	for i := 0; i < 6; i++ {
		tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	for p := 2; p <= 5; p++ {
		if n := pages[fmt.Sprintf("%d", p)]; n != 0 {
			t.Errorf("没滚到底就拉了第 %d 页 %d 次：%v", p, n, pages)
		}
	}
}

// 一级标签与它们的二级分段都能切，各自显示自己的内容。
func TestProfileTabs(t *testing.T) {
	a := profileWindowApp(t)
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()

	if err := tt.Click("内容"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{"共 2 篇", "第一篇帖子", "第二篇帖子", "发布", "合集", "评论"} {
		if !tt.HasText(want) {
			t.Errorf("内容页缺少 %q：%q", want, tt.Texts())
		}
	}

	// 评论分段就是原来的「我的回复」，评论与回复都列在这里。它和另外两段
	// 共用同一个二级分段条，直接画在选中的分段下面，不再跳进独立页面。
	if err := tt.Click("评论"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{"发布", "合集", "评论", "共 2 条", "我的一条回复", "《第一篇帖子》"} {
		if !tt.HasText(want) {
			t.Errorf("评论分段缺少 %q：%q", want, tt.Texts())
		}
	}
	tab, ok := tt.Find("评论")
	if !ok {
		t.Fatalf("评论分段里看不到二级分段条：%q", tt.Texts())
	}
	reply, ok := tt.Find("我的一条回复")
	if !ok {
		t.Fatalf("评论分段里看不到评论：%q", tt.Texts())
	}
	if reply.Y <= tab.Y {
		t.Errorf("评论该画在选中的二级 tab 下面：tab %+v，评论 %+v", tab, reply)
	}
	// 回到发布：分段条一直在，不需要先退出内容页。
	if err := tt.Click("发布"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("第一篇帖子") {
		t.Errorf("从评论切回发布没回到帖子列表：%q", tt.Texts())
	}

	// 设置 - 账号与权限：令牌里的 scope 原样列出。
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("账号与权限"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{"授权范围", "read:me", "读取我的内容", "read:economy"} {
		if !tt.HasText(want) {
			t.Errorf("账号与权限页缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 从「我的」窗口发起的请求缺权限时，全局授权弹窗也出现在这个窗口里：
// 用户不用切回主窗口才知道为什么列表是空的。
func TestProfileShowsAuthDialog(t *testing.T) {
	a := profileWindowApp(t)
	a.demandAuthorizationFor(toastProfile, scopeNeed([]string{"read:me"}))
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	for _, want := range []string{"需要授权", "缺少的权限范围：read:me", "前往授权", "取消"} {
		if !tt.HasText(want) {
			t.Errorf("「我的」窗口里的授权弹窗缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 没有 scope 的旧令牌也要说明白，而不是显示一片空白。
func TestProfileAccountTabWithoutScope(t *testing.T) {
	a := profileWindowApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	a.profile.tab = profileTabSettings
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if !tt.HasText("令牌里没有 scope 信息") {
		t.Errorf("缺少 scope 时没有说明：%q", tt.Texts())
	}
}

// 点帖子行回到主窗口的正文页；「我的」窗口留在原地不关。
func TestProfileTopicOpensDetail(t *testing.T) {
	a := profileWindowApp(t)
	a.profile.tab = profileTabContent
	a.setProfileWindowShown(true)
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if err := tt.Click("第一篇帖子"); err != nil {
		t.Fatal(err)
	}
	if a.pageName != "detail" {
		t.Fatalf("点帖子后主窗口没有进正文页：%q", a.pageName)
	}
	if a.detail == nil || a.detail.shortID != "T1" {
		t.Fatalf("打开的帖子不对：%+v", a.detail)
	}
	if !a.profileShown {
		t.Error("打开帖子后「我的」窗口不该被关掉")
	}
}

// 点回复行同样回到正文，并带上评论 id 用于定位楼层。
func TestProfileReplyOpensDetailWithComment(t *testing.T) {
	a := profileWindowApp(t)
	a.profile.tab = profileTabContent
	a.profile.contentKind = profileContentComments
	a.setProfileWindowShown(true)
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if err := tt.Click("我的一条回复"); err != nil {
		t.Fatal(err)
	}
	if a.detail == nil || a.detail.shortID != "T1" || a.detail.focusID != "R1" {
		t.Fatalf("打开的回复定位不对：%+v", a.detail)
	}
	if !a.profileShown {
		t.Error("打开回复后「我的」窗口不该被关掉")
	}
}

/* ------------------------------------------------------------------
   标题栏入口
----------------------------------------------------------------- */

// 标题栏的账号标识是账号菜单的锚点：点头像或昵称只弹出气泡，气泡里的「我的」
// 才打开窗口；headless 里不创建真实窗口，也不切走主窗口的页面。
func TestHeaderAvatarOpensProfile(t *testing.T) {
	a := profileWindowApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()

	if _, ok := tt.Find("个人中心"); !ok {
		t.Fatalf("标题栏的头像没有可访问名「个人中心」：%q", tt.Texts())
	}
	// 头像与昵称都还在，点昵称也等同于点这个账号标识，只开合气泡。
	if _, ok := tt.Find("jimmy"); !ok {
		t.Fatalf("标题栏没有头像控件：%q", tt.Texts())
	}
	if err := tt.Click("jimmy"); err != nil {
		t.Fatalf("点击头像失败：%v", err)
	}
	tt.Frame()
	if !a.profileMenuOpen {
		t.Error("点账号标识没有打开账号菜单气泡")
	}
	if a.profileWin != nil {
		t.Error("headless 测试里不该创建「我的」窗口")
	}
	if a.pageName != "list" {
		t.Errorf("点头像不该切走主窗口的页面：%q", a.pageName)
	}
	// 气泡里有「我的」入口，点它才打开「我的」窗口。入口的无障碍名与
	// 标题栏锚点不同，点它不会误触锚点。
	profileOpened := 0
	a.onProfileOpen = func() { profileOpened++ }
	if err := tt.Click("打开个人中心窗口"); err != nil {
		t.Fatalf("点账号菜单里的「个人中心」失败：%v", err)
	}
	tt.Frame()
	if profileOpened != 1 {
		t.Errorf("点「个人中心」入口没有触发打开：%d 次", profileOpened)
	}
	if a.profileMenuOpen {
		t.Error("点入口后账号菜单应收起")
	}
	if a.profileWin != nil {
		t.Error("headless 测试里不该创建「我的」窗口")
	}
}

// 账号菜单收着签到、金币、潘多拉与「我的」四个页面入口，末尾还隔着分隔线
// 放了退出登录（见 TestAccountMenuLogout）。点任一行都先收起气泡再进对应页面。
func TestAccountMenuRoutesToFourPages(t *testing.T) {
	freezePandoraNow(t)
	a := profileWindowApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	openAccountMenu(t, tt)
	for _, want := range []string{"签到", "金币", "潘多拉", "个人中心"} {
		if !tt.HasText(want) {
			t.Errorf("账号菜单缺少 %q：%q", want, tt.Texts())
		}
	}

	calls := 0
	a.onSignIn = func() { calls++ }
	if err := tt.Click("签到"); err != nil {
		t.Fatalf("点签到入口失败：%v", err)
	}
	tt.Frame()
	if calls != 1 || a.profileMenuOpen {
		t.Errorf("点签到应提交签到并收起气泡：calls=%d, open=%v", calls, a.profileMenuOpen)
	}

	a.onCoinsOpen = func() { calls++ }
	openAccountMenu(t, tt)
	if err := tt.Click("金币"); err != nil {
		t.Fatalf("点金币入口失败：%v", err)
	}
	tt.Frame()
	if calls != 2 || a.profileMenuOpen {
		t.Errorf("点金币应打开金币窗口并收起气泡：calls=%d, open=%v", calls, a.profileMenuOpen)
	}

	a.onPandoraOpen = func() { calls++ }
	openAccountMenu(t, tt)
	if err := tt.Click("潘多拉"); err != nil {
		t.Fatalf("点潘多拉入口失败：%v", err)
	}
	tt.Frame()
	if calls != 3 || a.profileMenuOpen {
		t.Errorf("点潘多拉应打开抽卡弹窗并收起气泡：calls=%d, open=%v", calls, a.profileMenuOpen)
	}
	if a.pandora == nil || !a.pandora.open {
		t.Error("点潘多拉入口没有打开抽卡弹窗")
	}
	// 抽卡弹窗盖住主窗口，先关掉才能再点标题栏的账号菜单。
	a.closePandora()
	tt.Frame()

	a.onProfileOpen = func() { calls++ }
	openAccountMenu(t, tt)
	if err := tt.Click("打开个人中心窗口"); err != nil {
		t.Fatalf("点「个人中心」入口失败：%v", err)
	}
	tt.Frame()
	if calls != 4 || a.profileMenuOpen {
		t.Errorf("点「个人中心」应打开窗口并收起气泡：calls=%d, open=%v", calls, a.profileMenuOpen)
	}
}

// 账号菜单气泡里只有潘多拉入口保留 tooltip（用来展示进行中事件的效果），
// 签到、金币、个人中心与退出登录都不再挂悬浮说明。
func TestAccountMenuOnlyPandoraHasTooltip(t *testing.T) {
	a := profileWindowApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	openAccountMenu(t, tt)

	for _, name := range []string{"签到", "金币", "打开个人中心窗口", "退出登录"} {
		row, ok := tt.Access(name)
		if !ok {
			t.Fatalf("账号菜单里没有 %q：%q", name, tt.Texts())
		}
		if row.Description != "" {
			t.Errorf("%q 不该带悬浮说明：%q", name, row.Description)
		}
		r, ok := tt.Find(name)
		if !ok {
			t.Fatalf("找不到 %q 的位置：%q", name, tt.Texts())
		}
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Advance(800 * time.Millisecond)
		if tip, ok := tt.AccessByRole(ui.RoleTooltip, ""); ok {
			t.Errorf("悬浮 %q 仍弹出 tooltip：%q", name, tip.Name)
		}
	}

	pandora, ok := tt.Access("潘多拉")
	if !ok {
		t.Fatalf("账号菜单里没有潘多拉入口：%q", tt.Texts())
	}
	if pandora.Description == "" {
		t.Error("潘多拉入口应保留展示事件效果的悬浮说明")
	}
}

// 退出登录从侧边栏挪到了标题栏账号菜单：收起的气泡里看不到它，展开后点一下
// 退出当前账号，气泡随之收起，侧边栏回到未登录的「登录 / 注册」入口。
func TestAccountMenuLogout(t *testing.T) {
	a := profileWindowApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if _, ok := tt.Find("退出登录"); ok {
		t.Fatalf("退出登录不该明摆在侧边栏：%q", tt.Texts())
	}
	openAccountMenu(t, tt)
	row, ok := tt.Access("退出登录")
	if !ok {
		t.Fatalf("账号菜单里没有「退出登录」：%q", tt.Texts())
	}
	if row.Description != "" {
		t.Errorf("退出登录不该带悬浮说明：%q", row.Description)
	}
	if err := tt.Click("退出登录"); err != nil {
		t.Fatalf("点账号菜单的退出登录失败：%v", err)
	}
	if a.auth.loggedIn() {
		t.Fatal("点「退出登录」后仍是登录状态")
	}
	if a.profileMenuOpen {
		t.Error("退出登录后账号菜单应收起")
	}
	tt.Frame()
	if !tt.HasText("登录 2Libra") {
		t.Errorf("退出后侧边栏应回到登录入口：%q", tt.Texts())
	}
	if _, ok := tt.Find("退出登录"); ok {
		t.Errorf("退出后不该还留着「退出登录」：%q", tt.Texts())
	}
}

// 账号切换 / 退出时账号菜单要收起，不能把上一个账号的入口留在气泡里。
func TestAccountMenuClosesOnAuthChange(t *testing.T) {
	a := profileWindowApp(t)
	a.profileMenuOpen = true
	a.auth.Logout()
	a.onAuthChanged(nil)
	if a.profileMenuOpen {
		t.Error("退出登录后账号菜单没有收起")
	}
}

// 退出登录后「我的」窗口的状态整体丢弃：不能把上一个账号的资料留给下一个人。
func TestProfileResetOnLogout(t *testing.T) {
	a := profileWindowApp(t)
	a.auth.Logout()
	ps := a.profile
	if ps == nil {
		t.Fatal("退出登录后「我的」状态被删掉了")
	}
	if ps.infoLoaded || len(ps.content[profileContentTopics].items) > 0 {
		t.Errorf("退出登录后「我的」状态没有清空：%+v", ps)
	}
}

// 未登录时点入口先弹全局授权窗，窗口本身不打开。
func TestProfileNeedsLogin(t *testing.T) {
	a := testApp(t)
	a.profileShown = false
	a.showProfileWindow()
	if !a.authGate.open {
		t.Error("未登录时应弹出授权弹窗")
	}
	if a.profileWin != nil {
		t.Error("未登录时不该打开「我的」窗口")
	}
}

/* ------------------------------------------------------------------
   数据加载
----------------------------------------------------------------- */

// 「我的」窗口只拉账号资料，不再请求金币余额、签到或流水。
func TestProfileLoadsInfoOnly(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/api/v1/users/jimmy" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"c":0,"d":{"username":"jimmy","user_number":123456,"created_at":"2024-02-01T08:00:00Z"}}`)
	}))
	defer srv.Close()

	a := testApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	a.ensureProfileLoaded()
	waitProfile(t, "资料", func() bool { return a.profile.infoLoaded })
	if a.profile.user.UserNumber != 123456 {
		t.Errorf("资料没有回填：%+v", a.profile.user)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, p := range paths {
		if strings.Contains(p, "/coins") || p == "/api/v1/me/sign" {
			t.Errorf("「我的」窗口不该请求金币数据：%v", paths)
		}
	}
}

// 「我的回复」按页追加，并在到底时停住；重复的条目不会出现两次。
func TestProfileRepliesPaging(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/jimmy/replies" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("page") {
		case "2":
			fmt.Fprint(w, `{"c":0,"d":{"items":[
				{"id":"r1","content":"重复的那条","post":{"short_id":"T1","title":"帖子一"}},
				{"id":"r2","content":"第二页的新回复","post":{"short_id":"T2","title":"帖子二"}}
			],"total":3,"page":2,"limit":50,"total_pages":2}}`)
		default:
			fmt.Fprint(w, `{"c":0,"d":{"items":[
				{"id":"r1","content":"第一页的回复","post":{"short_id":"T1","title":"帖子一"}}
			],"total":3,"page":1,"limit":50,"total_pages":2}}`)
		}
	}))
	defer srv.Close()

	a := testApp(t)
	a.client.Base = srv.URL + "/api"
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	a.loadProfileReplies("jimmy", 1)
	waitProfile(t, "第一页回复", func() bool { return a.profile.replyLoaded })
	if len(a.profile.replies) != 1 || a.profile.replyTotal != 3 {
		t.Fatalf("第一页没有回填：%+v", a.profile.replies)
	}
	a.loadProfileReplies("jimmy", 2)
	waitProfile(t, "第二页回复", func() bool { return a.profile.replyPage == 2 && !a.profile.replyLoading })
	if len(a.profile.replies) != 2 {
		t.Fatalf("第二页没有去重追加：%+v", a.profile.replies)
	}
	if a.profile.replies[1].Title != "第二页的新回复" {
		t.Errorf("追加顺序不对：%+v", a.profile.replies)
	}
	if !a.profile.replyNoMore {
		t.Error("最后一页之后没有标记到底")
	}
}

// 「我的回复」滚到底自动续拉：列表底部不再有「加载更多」按钮，看到末尾就
// 直接拉下一页；失败仍停在底栏等手动重试。
func TestProfileRepliesAutoLoadOnScroll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/jimmy/replies" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprint(w, `{"c":0,"d":{"items":[
				{"id":"r2","content":"第二页的新回复","post":{"short_id":"T2","title":"帖子二"}}
			],"total":2,"page":2,"limit":50,"total_pages":2}}`)
			return
		}
		fmt.Fprint(w, `{"c":0,"d":{"items":[
			{"id":"r1","content":"第一页的回复","post":{"short_id":"T1","title":"帖子一"}}
		],"total":2,"page":1,"limit":50,"total_pages":2}}`)
	}))
	defer srv.Close()

	a := testApp(t)
	a.client.Base = srv.URL + "/api"
	a.autoLoad = true
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	a.profile.tab = profileTabContent
	a.profile.contentKind = profileContentComments
	a.loadProfileReplies("jimmy", 1)
	waitProfile(t, "第一页回复", func() bool { return a.profile.replyLoaded })

	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	waitProfile(t, "滚到底自动续拉", func() bool {
		return a.profile.replyPage == 2 && !a.profile.replyLoading
	})
	tt.Frame()
	if !tt.HasText("第二页的新回复") {
		t.Errorf("没有自动续拉出第二页：%q", tt.Texts())
	}
	if _, ok := tt.Find("加载更多"); ok {
		t.Errorf("回复列表底部不该再有「加载更多」按钮：%q", tt.Texts())
	}
}

// waitProfile 等一个后台结果回到状态里。load* 在无窗口时直接执行，所以这里
// 只是给请求协程一点时间。
func waitProfile(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等待超时：%s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
