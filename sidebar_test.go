package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// sidebarWidth 是左侧导航栏的宽度，与 view 里的布局一致（见 main.go 的
// navSidebarWidth）。底部的设置、外观与账号入口都得待在这条带子里，不能
// 溢到右侧内容区。
const sidebarWidth = navSidebarWidth

// 设置入口在主页左侧 Lobe 导航轨里，外观三档在它上面，登录 / 退出登录再往下：
// 这三样都不在标题栏里了，标题栏只留品牌、铃铛和登录后的头像昵称。
const logoutLabel = "退出登录"
const loginLabel = "登录 2Libra"

func TestSidebarFooterHoldsThemesSettingsAndAccount(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	set, ok := tt.Find("设置")
	if !ok {
		t.Fatalf("侧边栏没有「设置」入口：%q", tt.Texts())
	}
	if set.Y <= titleBarHeight {
		t.Errorf("「设置」的 y=%.0f，应在标题栏下方的侧边栏里", set.Y)
	}
	login, ok := tt.Find(loginLabel)
	if !ok {
		t.Fatalf("侧边栏没有「%s」入口：%q", loginLabel, tt.Texts())
	}
	if login.Y <= set.Y {
		t.Errorf("「登录」的 y=%.0f，应在设置入口（y=%.0f）下方", login.Y, set.Y)
	}
	for _, label := range []string{"外观：跟随系统", "外观：浅色", "外观：深色", loginLabel} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("侧边栏缺少 %q：%q", label, tt.Texts())
		}
		if r.X+r.W > sidebarWidth+1 {
			t.Errorf("%q 的 x=%.0f 宽 %.0f，超出 %d 宽的导航轨", label, r.X, r.W, sidebarWidth)
		}
	}
	// 外观在设置上面：先看到三档图标，再往下才是「设置」。
	for _, label := range []string{"外观：跟随系统", "外观：浅色", "外观：深色"} {
		r, _ := tt.Find(label)
		if r.Y >= set.Y {
			t.Errorf("%q 的 y=%.0f，应在设置入口（y=%.0f）上方", label, r.Y, set.Y)
		}
		if r.Y >= login.Y {
			t.Errorf("%q 的 y=%.0f，应在账号入口（y=%.0f）上方", label, r.Y, login.Y)
		}
	}
	// 设置仍然是独立窗口的入口。
	opened := false
	a.onSettingsOpen = func() { opened = true }
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Error("点侧边栏的「设置」没有打开设置窗口")
	}
}

// 收藏 / 书签 / 我的帖子 / 我的合集 / 金币 不再占侧边栏的位置：前四类都收进
// 了「我的」窗口的「我的内容」一页，金币页是独立的金币窗口。
func TestSidebarHasNoMyContentEntries(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, label := range []string{"收藏", "书签", "我的帖子", "我的合集"} {
		if _, ok := tt.Find(label); ok {
			t.Errorf("侧边栏不该再有 %q：%q", label, tt.Texts())
		}
	}
	if _, ok := tt.Find("金币"); ok {
		t.Errorf("侧边栏不该再有金币入口：%q", tt.Texts())
	}
}

// Lobe 导航轨是纯图标轨，不再有「浏览」分组标题；「动态」作为浏览页之一
// 排在「社区讨论」之后，未登录时也照常显示（点它才弹授权）。
func TestSidebarTimelineBelongsToBrowse(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if _, ok := tt.Find("我的"); ok {
		t.Errorf("侧边栏不该再有「我的」分组：%q", tt.Texts())
	}
	timeline, ok := tt.Find("动态")
	if !ok {
		t.Fatalf("侧边栏没有「动态」：%q", tt.Texts())
	}
	last, ok := tt.Find("社区讨论")
	if !ok {
		t.Fatalf("侧边栏缺少「社区讨论」：%q", tt.Texts())
	}
	if timeline.Y <= last.Y {
		t.Errorf("「动态」的 y=%.0f，应排在「社区讨论」（y=%.0f）之后", timeline.Y, last.Y)
	}
	if timeline.X >= sidebarWidth {
		t.Errorf("「动态」的 x=%.0f 越出了导航轨宽度 %d", timeline.X, sidebarWidth)
	}
}

// 未登录时侧边栏的「登录」直接打开浏览器授权，不离开当前页；等待期间变成
// 「粘贴回调」与「取消」，点「取消」就回到未登录。
func TestSidebarLoginStartsAndCancels(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	t.Cleanup(a.auth.Cancel)
	if err := tt.Click(loginLabel); err != nil {
		t.Fatal(err)
	}
	if a.pageName != "list" {
		t.Fatalf("点「登录」不该进登录页，实际页面 = %q", a.pageName)
	}
	if !a.loginPending {
		t.Fatal("点「登录」后没有进入等待授权状态")
	}
	tt.Frame()
	if _, ok := tt.Find("等待浏览器授权"); !ok {
		t.Errorf("侧边栏没有显示等待授权：%q", tt.Texts())
	}
	if _, ok := tt.Find(loginLabel); ok {
		t.Errorf("等待授权期间不该还摆着「登录」：%q", tt.Texts())
	}
	for _, label := range []string{"取消登录"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("等待授权期间缺少 %q：%q", label, tt.Texts())
		}
	}
	if err := tt.Click("取消登录"); err != nil {
		t.Fatal(err)
	}
	if a.loginPending {
		t.Error("点「取消」后仍在等待授权")
	}
	tt.Frame()
	if _, ok := tt.Find(loginLabel); !ok {
		t.Errorf("取消后应回到「登录」入口：%q", tt.Texts())
	}
}

// 未登录时侧边栏除了「登录」还要给出「注册」入口：注册是网页流程，点一下
// 只打开系统浏览器，不进入等待授权、也不离开当前页。
func TestSidebarOffersSignupWhenSignedOut(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if _, ok := tt.Find("注册"); !ok {
		t.Fatalf("未登录侧边栏没有「注册」入口：%q", tt.Texts())
	}
	if err := tt.Click("注册"); err != nil {
		t.Fatal(err)
	}
	if a.pageName != "list" {
		t.Errorf("点「注册」不该离开列表页，实际页面 = %q", a.pageName)
	}
	if a.loginPending {
		t.Error("点「注册」不该进入等待授权状态")
	}
}

// 登录后侧边栏的账号入口变成「退出登录」，点一下就退出，入口回到「登录」；
// 标题栏只显示头像与昵称，不写「已登录」。
func TestSidebarLogoutAfterLogin(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", Username: "jimmy", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.onAuthChanged(nil)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()

	if !tt.HasText("jimmy") {
		t.Errorf("标题栏没有显示用户名：%q", tt.Texts())
	}
	if tt.HasText("已登录") {
		t.Errorf("标题栏不应再显示「已登录」：%q", tt.Texts())
	}
	if _, ok := tt.Find(logoutLabel); !ok {
		t.Fatalf("侧边栏没有「退出登录」入口：%q", tt.Texts())
	}
	if err := tt.Click(logoutLabel); err != nil {
		t.Fatal(err)
	}
	if a.auth.loggedIn() {
		t.Fatal("点「退出登录」后仍是登录状态")
	}
	tt.Frame()
	if _, ok := tt.Find(loginLabel); !ok {
		t.Errorf("退出后侧边栏应回到「登录」：%q", tt.Texts())
	}
	if _, ok := tt.Find(logoutLabel); ok {
		t.Errorf("退出后不该还留着「退出登录」：%q", tt.Texts())
	}
}

// 再点一次当前所在的导航项就刷新这一页：回到第一页重新请求，列表换成新内容。
// 当前页还在加载时不重发，免得两次请求抢结果。
func TestSidebarReclickRefreshesCurrentPage(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// 「首页」的列表字段是 posts（见 tabSpecs）。
		_, _ = io.WriteString(w, envelopeJSON(`{"posts":[{"id":"n1","short_id":"NEW1","title":"再点一次之后的内容"}],"total":1,"page":1,"limit":50,"total_pages":1}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.tabs[0].items = []Item{{Key: "old", Title: "旧内容"}}
	a.tabs[0].loaded = true
	a.tabs[0].total = 1
	// 已翻到第 2 页：刷新要回到第 1 页。noMore 挡住无限滚动，
	// 让这里只有「再点一次」发出的请求。
	a.tabs[0].page = 2
	a.tabs[0].noMore = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("旧内容") {
		t.Fatalf("开始时应显示旧内容：%q", tt.Texts())
	}

	label := tabSpecs[0].Label
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := a.tabs[0]
		if len(st.items) == 1 && st.items[0].Title == "再点一次之后的内容" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("再点「%s」没有重新加载（请求 %d 次，err=%q，items=%d）",
				label, calls.Load(), st.err, len(st.items))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Errorf("刷新应当只发一次请求，实际 %d 次", calls.Load())
	}
	if a.tab != 0 {
		t.Errorf("再点当前项不该切页：tab=%d", a.tab)
	}
	if a.tabs[0].page != 1 {
		t.Errorf("刷新应回到第 1 页：page=%d", a.tabs[0].page)
	}

	// 正在加载时再点一次不发新请求。
	a.tabs[0].loading = true
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	time.Sleep(20 * time.Millisecond)
	if calls.Load() != 1 {
		t.Errorf("加载中再点不该重发请求，实际 %d 次", calls.Load())
	}
	a.tabs[0].loading = false
}

// 点另一个导航项进入那一页时也自动刷新一次：即使这一页之前已经显示过，
// 进去仍然重新拉第一页；刷新期间旧内容先留着，不闪成空白。
func TestSidebarSwitchRefreshesEnteredPage(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/topics/hot" {
			t.Errorf("刷新应当拉当前页的接口，实际 %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, envelopeJSON(`{"posts":[{"id":"h1","short_id":"HOT1","title":"进入热榜后的内容"}],"total":1,"page":1,"limit":50,"total_pages":1}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	// 热榜之前已经显示过（旧内容），进去要换成新内容。
	hot := 1
	a.tabs[hot].items = []Item{{Key: "old", Title: "热榜旧内容"}}
	a.tabs[hot].loaded = true
	a.tabs[hot].total = 1
	a.tabs[hot].noMore = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	label := tabSpecs[hot].Label
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := a.tabs[hot]
		if len(st.items) == 1 && st.items[0].Title == "进入热榜后的内容" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("进入「%s」没有自动刷新（请求 %d 次，err=%q，items=%d）",
				label, calls.Load(), st.err, len(st.items))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Errorf("进入一页应当只刷新一次，实际 %d 次", calls.Load())
	}
	if a.tab != hot {
		t.Errorf("没有切到热榜：tab=%d", a.tab)
	}
	if st := a.tabs[hot]; !st.loaded || st.page != 1 {
		t.Errorf("刷新后应停在第 1 页并标记已加载：page=%d loaded=%v", st.page, st.loaded)
	}
}

// 侧边栏底部有「关于」入口：点一下打开「关于」窗口。导航轨里的常驻入口
// 竖排，整列待在 58 DIP 轨内，不溢到右侧内容区。
func TestSidebarAboutOpensWindow(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	about, ok := tt.Find("关于")
	if !ok {
		t.Fatalf("侧边栏没有「关于」入口：%q", tt.Texts())
	}
	if about.X+about.W > sidebarWidth+1 {
		t.Errorf("「关于」的 x=%.0f 宽 %.0f，超出 %d 宽的导航轨", about.X, about.W, sidebarWidth)
	}
	set, _ := tt.Find("设置")
	if about.Y <= set.Y {
		t.Errorf("「关于」的 y=%.0f，应排在设置入口（y=%.0f）下方", about.Y, set.Y)
	}
	opened := false
	a.onAboutOpen = func() { opened = true }
	if err := tt.Click("关于"); err != nil {
		t.Fatal(err)
	}
	if !opened {
		t.Error("点侧边栏的「关于」没有打开关于窗口")
	}
}

// 关于窗口列出开发者、邮箱、项目地址与 2Libra 主页；邮箱与两个网址都可点击
// 打开（邮件客户端 / 浏览器）。
func TestAboutWindowShowsCredits(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.aboutWindowView, aboutWindowWidth, aboutWindowHeight)
	tt.Frame()
	for _, s := range []string{
		"关于", "T2 Desktop",
		"开发者", "vhxubo",
		"邮箱", "vhxubo@gmail.com",
		"项目地址", "github.com/vhxubo/t2-desktop",
		"2Libra 主页", "https://2libra.com/user/vhxubo/about",
		"技术栈", "Go 语言", "mygo",
		"协议", "MIT",
		"条款", "第三方开发者 vhxubo",
		"服务条款", "2Libra 第三方接入条款",
	} {
		if !tt.HasText(s) {
			t.Errorf("关于窗口缺少 %q：%q", s, tt.Texts())
		}
	}
}

// 关于窗口每行的标签列左边缘对齐：四行的标签都在同一列，开发者行（不可点击）
// 和其余可点击行的内边距相同，不能因为少了 padding 而错位。
func TestAboutWindowRowsAligned(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.aboutWindowView, aboutWindowWidth, aboutWindowHeight)
	tt.Frame()

	var lastX float32
	for i, lab := range []string{"开发者", "邮箱", "项目地址", "2Libra 主页", "协议"} {
		r, ok := tt.Find(lab)
		if !ok {
			t.Fatalf("关于窗口缺少标签 %q：%q", lab, tt.Texts())
		}
		if i > 0 && (r.X-lastX > 0.5 || r.X-lastX < -0.5) {
			t.Errorf("标签列左边缘错位：%q x=%.0f，上一行 x=%.0f", lab, r.X, lastX)
		}
		lastX = r.X
	}
}

// 侧边栏的折叠 / 展开只保留底部列表那一处入口：接缝上不再画一个重复的开关。
// Lobe 组件自带的 handle 也带「展开 / 折叠侧边栏」无障碍名（测试里是英文的
// Expand / Collapse side nav），按名字数一遍就能看出重复。
func TestSidebarHasSingleCollapseToggle(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	count := func(name string) int {
		n := 0
		for _, info := range tt.AccessAll() {
			if info.Name == name {
				n++
			}
		}
		return n
	}
	only := func(name string) {
		t.Helper()
		if got := count(name); got != 1 {
			t.Errorf("应只有一个「%s」入口，实际 %d 个", name, got)
		}
	}
	absent := func(name string) {
		t.Helper()
		if got := count(name); got != 0 {
			t.Errorf("不该有「%s」入口，实际 %d 个", name, got)
		}
	}
	// 组件自带的 handle 在测试里用英文无障碍名；两种状态下都不能出现。
	noSeamHandle := func() {
		t.Helper()
		absent("Expand side nav")
		absent("Collapse side nav")
	}

	// 默认折叠：底部列表给出「展开侧边栏」。
	if a.navExpanded {
		t.Fatal("前置条件：默认应是折叠态")
	}
	only("展开侧边栏")
	only("Resize side nav") // 拖拽调宽的接缝保留，只是不再带开关
	absent("折叠侧边栏")
	noSeamHandle()

	// 点一下展开：入口换成「折叠侧边栏」，同样只有一处。
	if err := tt.Click("展开侧边栏"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.navExpanded {
		t.Fatal("点「展开侧边栏」后没有展开")
	}
	only("折叠侧边栏")
	absent("展开侧边栏")
	noSeamHandle()

	// 再点一下收起，回到原来的状态。
	if err := tt.Click("折叠侧边栏"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.navExpanded {
		t.Fatal("点「折叠侧边栏」后没有收起")
	}
	only("展开侧边栏")
	noSeamHandle()
}
