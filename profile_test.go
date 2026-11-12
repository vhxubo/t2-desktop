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
		want string
	}{
		{profileContentTopics, "https://2libra.com/api/v1/me/topics"},
		{profileContentFavorites, "https://2libra.com/api/v1/me/favorites?page=1&limit=50"},
		{profileContentBookmarks, "https://2libra.com/api/v1/me/bookmarks?page=1&limit=100"},
		{profileContentCollections, "https://2libra.com/api/v1/me/collections"},
	}
	for _, c := range cases {
		if got := endpoint(apiBaseURL, profileContentSpecs[c.kind], 1); got != c.want {
			t.Errorf("%s URL = %s，期望 %s", profileContentLabels[c.kind], got, c.want)
		}
	}
	// 收藏 / 书签按页；帖子 / 合集整页返回。
	for _, c := range cases {
		spec := profileContentSpecs[c.kind]
		wantPaging := pagingPages
		if c.kind == profileContentTopics || c.kind == profileContentCollections {
			wantPaging = pagingOff
		}
		if spec.Paging != wantPaging || !spec.NeedsAuth {
			t.Errorf("%s 的分页 / 登录约定不对：%+v", c.want, spec)
		}
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
		"我的", "吉米",
		"用户 ID: #123456", "注册 2024-02-01",
		"概览", "我的内容", "我的回复", "权限与账号",
		"最近统计", "粉丝", "关注", "主页访问", "等级",
		"授权管理", "查看当前权限",
	} {
		if !tt.HasText(want) {
			t.Errorf("「我的」窗口缺少文案 %q：%q", want, tt.Texts())
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

// 「我的内容」一页里有四个分段：帖子 / 收藏 / 书签 / 合集，切到哪段显示
// 哪段的内容。这四类原先占着侧边栏，现在都收进这里。
func TestProfileContentSegments(t *testing.T) {
	if len(profileContentSpecs) != profileContentCount ||
		len(profileContentLabels) != profileContentCount ||
		len(profileContentUnits) != profileContentCount ||
		len(profileContentEmpty) != profileContentCount {
		t.Fatalf("「我的内容」的分段表长度不一致：specs=%d labels=%d units=%d empty=%d",
			len(profileContentSpecs), len(profileContentLabels),
			len(profileContentUnits), len(profileContentEmpty))
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
	if err := tt.Click("我的内容"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("第一篇帖子") {
		t.Errorf("默认分段应是帖子：%q", tt.Texts())
	}
	// 四个分段都要待在窗口宽度之内，不能挤到窗口外面。
	for _, label := range append(append([]string{}, profileContentLabels...), "刷新") {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("「我的内容」缺少 %q：%q", label, tt.Texts())
		}
		if r.X+r.W > profileWindowWidth {
			t.Errorf("%q 的 x=%.0f 宽 %.0f，超出 %d 宽的窗口",
				label, r.X, r.W, profileWindowWidth)
		}
	}

	for _, c := range []struct{ segment, want string }{
		{"收藏", "收藏的帖子"},
		{"书签", "书签里的评论"},
		{"合集", "我的收藏夹"},
	} {
		if err := tt.Click(c.segment); err != nil {
			t.Fatalf("切到 %s 失败：%v", c.segment, err)
		}
		tt.Frame()
		if !tt.HasText(c.want) {
			t.Errorf("%s 分段里看不到 %q：%q", c.segment, c.want, tt.Texts())
		}
	}
}

// 四个标签页都能切，各自显示自己的内容。
func TestProfileTabs(t *testing.T) {
	a := profileWindowApp(t)
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()

	if err := tt.Click("我的内容"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{"共 2 篇", "第一篇帖子", "第二篇帖子", "帖子", "收藏", "书签", "合集"} {
		if !tt.HasText(want) {
			t.Errorf("我的内容页缺少 %q：%q", want, tt.Texts())
		}
	}

	if err := tt.Click("我的回复"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{"共 2 条", "我的一条回复", "《第一篇帖子》"} {
		if !tt.HasText(want) {
			t.Errorf("我的回复页缺少 %q：%q", want, tt.Texts())
		}
	}

	// 权限与账号：令牌里的 scope 原样列出。
	if err := tt.Click("权限与账号"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{"授权范围", "read:me", "读取我的内容", "read:economy"} {
		if !tt.HasText(want) {
			t.Errorf("权限与账号页缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 从「我的」窗口发起的请求缺权限时，全局授权弹窗也出现在这个窗口里：
// 用户不用切回主窗口才知道为什么列表是空的。
func TestProfileShowsAuthDialog(t *testing.T) {
	a := profileWindowApp(t)
	a.demandAuthorization(scopeNeed([]string{"read:me"}), nil)
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
	a.profile.tab = profileTabAccount
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
	a.profile.tab = profileTabReplies
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

// 标题栏的账号标识可点，点开「我的」；headless 里不创建真实窗口，也不
// 切走主窗口的页面。
func TestHeaderAvatarOpensProfile(t *testing.T) {
	a := profileWindowApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()

	if _, ok := tt.Find("我的"); !ok {
		t.Fatalf("标题栏的头像没有可访问名「我的」：%q", tt.Texts())
	}
	// 头像与昵称都还在，点击头像等同于点击这个账号标识。
	if _, ok := tt.Find("jimmy"); !ok {
		t.Fatalf("标题栏没有头像控件：%q", tt.Texts())
	}
	if err := tt.Click("jimmy"); err != nil {
		t.Fatalf("点击头像失败：%v", err)
	}
	tt.Frame()
	if a.profileWin != nil {
		t.Error("headless 测试里不该创建「我的」窗口")
	}
	if a.pageName != "list" {
		t.Errorf("点头像不该切走主窗口的页面：%q", a.pageName)
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
