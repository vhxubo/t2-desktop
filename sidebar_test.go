package main

import (
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// sidebarWidth 是左侧导航栏的宽度，与 view 里的布局一致（见 main.go 的
// navSidebarWidth）。底部的设置与账号入口都得待在这条带子里，不能溢到右侧
// 内容区。
const sidebarWidth = navSidebarWidth

// 设置入口在主页左侧 Lobe 导航轨里，登录 / 注册再往下；退出登录已收进标题栏
// 账号菜单的气泡。标题栏只留搜索、铃铛和登录后的头像昵称。外观三档已收进设置
// 窗口。
const logoutLabel = "退出登录"
const loginLabel = "登录 2Libra"

func TestSidebarFooterHoldsSettingsAndAccount(t *testing.T) {
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
	for _, label := range []string{loginLabel} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("侧边栏缺少 %q：%q", label, tt.Texts())
		}
		if r.X+r.W > sidebarWidth+1 {
			t.Errorf("%q 的 x=%.0f 宽 %.0f，超出 %d 宽的导航轨", label, r.X, r.W, sidebarWidth)
		}
	}
	// 外观三档已经从侧边栏移除，统一到设置窗口的「外观」页里。
	for _, label := range []string{"外观：跟随系统", "外观：浅色", "外观：深色"} {
		if _, ok := tt.Find(label); ok {
			t.Errorf("侧边栏不该再有 %q：%q", label, tt.Texts())
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

// 登录后侧边栏不再占一行放「退出登录」：它和金币、签到一样收进标题栏账号
// 菜单的气泡里（见 profile_test.go 的 TestAccountMenuLogout）。标题栏只显示
// 头像与昵称，不写「已登录」。
func TestSidebarHasNoLogoutAfterLogin(t *testing.T) {
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
	if _, ok := tt.Find(logoutLabel); ok {
		t.Errorf("退出登录不该再占侧边栏一行：%q", tt.Texts())
	}
	if _, ok := tt.Find(loginLabel); ok {
		t.Errorf("登录后侧边栏不该还摆着「登录」：%q", tt.Texts())
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

// 侧边栏的折叠 / 展开只保留顶部那一处开关：接缝与底部列表都不再画重复的开关。
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

	// 默认折叠：顶部开关给出「展开侧边栏」。
	if a.navExpanded {
		t.Fatal("前置条件：默认应是折叠态")
	}
	only("展开侧边栏")
	// 开关位于左上角的侧边栏头部，不在底部列表里。
	toggle, ok := tt.Access("展开侧边栏")
	if !ok {
		t.Fatal("顶部开关不可访问")
	}
	if toggle.Bounds.X+toggle.Bounds.W > sidebarWidth+1 {
		t.Errorf("展开开关的 x=%.0f 宽 %.0f，越出了 %d 宽的导航轨", toggle.Bounds.X, toggle.Bounds.W, sidebarWidth)
	}
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
	fold, ok := tt.Access("折叠侧边栏")
	if !ok {
		t.Fatal("顶部开关展开后不可访问")
	}
	if fold.Description != "折叠侧边栏" {
		t.Errorf("折叠开关的悬浮说明 = %q，期望同名 tooltip", fold.Description)
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

// railInkCenter 量一行里图标墨迹的水平中心。无障碍矩形在展开态覆盖整行，
// 拿不到图标自身的边界，所以按像素取导航轨左半边的墨迹范围；图标是左右对称
// 的，这个中心就是它的对齐位置。
func railInkCenter(im *image.RGBA, y0, y1 int) float32 {
	minX, maxX := 999, -1
	for y := y0; y < y1; y++ {
		for x := 0; x < int(navRailWidth/2)+8; x++ {
			if lum(im, x, y) < 205 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
			}
		}
	}
	return float32(minX+maxX) / 2
}

// 顶部开关、导航项与底部项在展开和收起两态必须落在同一条竖线上，否则点开
// 侧边栏时整列图标会横向跳一下。中心对准的是折叠图标轨的中线 32 DIP。
func TestSidebarIconsAlignAcrossExpandStates(t *testing.T) {
	near := func(a, b float32) bool { return a-b < 1.5 && b-a < 1.5 }
	for _, expanded := range []bool{false, true} {
		a := testApp(t)
		a.navExpanded = expanded
		tt := ui.NewTester(a.view, 1100, 760)
		tt.Frame()
		im := tt.Image()
		toggle := "展开侧边栏"
		if expanded {
			toggle = "折叠侧边栏"
		}
		centers := make([]float32, 0, 3)
		for _, name := range []string{toggle, "首页", "设置"} {
			info, ok := tt.Access(name)
			if !ok {
				t.Fatalf("展开=%v 时找不到 %q：%q", expanded, name, tt.Texts())
			}
			b := info.Bounds
			c := railInkCenter(im, int(b.Y), int(b.Y+b.H))
			centers = append(centers, c)
			if !near(c, navRailWidth/2) {
				t.Errorf("展开=%v 时 %q 的图标中心 %.1f，未对准轨中线 %.1f", expanded, name, c, navRailWidth/2)
			}
		}
		for i, c := range centers {
			if !near(c, centers[0]) {
				t.Errorf("展开=%v 时第 %d 个图标中心 %.1f 与顶部开关 %.1f 不齐", expanded, i, c, centers[0])
			}
		}
	}
}

// glyphInkSize 量一个按钮里图标墨迹的宽高。无障碍矩形是整块 32 的按钮，
// 拿不到图标本身；从中心区域按亮度取墨迹范围，避开 1px 边框。不同图标的
// 墨迹形状不同，但同一 glyph size 下面板与齿轮二者的墨迹都应落在 14 上下。
func glyphInkSize(im *image.RGBA, b ui.Rect) (w, h int) {
	minX, minY, maxX, maxY := 999, 999, -1, -1
	for y := int(b.Y) + 4; y < int(b.Y+b.H)-4; y++ {
		for x := int(b.X) + 4; x < int(b.X+b.W)-4; x++ {
			if lum(im, x, y) < 180 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < 0 {
		return 0, 0
	}
	return maxX - minX + 1, maxY - minY + 1
}

// 顶部折叠开关和下面图标轨的图标必须一样大。曾经开关用 GlyphSize 18、
// 轨里用 16，面板图标比齿轮/账号图标高出一圈，看着很突兀；现在统一 16。
func TestSidebarToggleGlyphSizeMatchesRail(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	im := tt.Image()

	toggle, ok := tt.Access("展开侧边栏")
	if !ok {
		t.Fatalf("找不到顶部折叠开关：%q", tt.Texts())
	}
	rail, ok := tt.Access("设置")
	if !ok {
		t.Fatalf("找不到底部「设置」图标：%q", tt.Texts())
	}
	tw, th := glyphInkSize(im, toggle.Bounds)
	rw, rh := glyphInkSize(im, rail.Bounds)
	if tw == 0 || rw == 0 {
		t.Fatalf("量不到图标墨迹：开关 %dx%d，设置 %dx%d", tw, th, rw, rh)
	}
	if d := th - rh; d > 1 || d < -1 {
		t.Errorf("折叠开关图标高 %d，与图标轨的「设置」高 %d 不一致", th, rh)
	}
	if d := tw - rw; d > 1 || d < -1 {
		t.Errorf("折叠开关图标宽 %d，与图标轨的「设置」宽 %d 不一致", tw, rw)
	}
}

// 展开后图标不能贴着侧边栏左缘：列表行整体往右让出一段留白，和折叠后
// 图标轨的居中位置对齐；文字标签也跟着一起右移。
func TestSidebarExpandedRowsHaveLeftInset(t *testing.T) {
	a := testApp(t)
	a.navExpanded = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	for _, name := range []string{"首页", "设置", "账号"} {
		row, ok := tt.Access(name)
		if !ok {
			t.Fatalf("展开态缺少 %q：%q", name, tt.Texts())
		}
		if row.Bounds.X < navListInset {
			t.Errorf("%q 的行左边距 %.1f，展开态应至少留出 %.1f", name, row.Bounds.X, navListInset)
		}
	}
}

// 折叠后的图标轨用 Lobe ActionIcon：每一项（含顶部展开开关）的无障碍描述
// 就是它的名字，指针停在上面时由 Lobe 的 tooltip 把名字显示出来。
func TestSidebarCollapsedIconsShowTooltips(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if a.navExpanded {
		t.Fatal("前置条件：默认应是折叠态")
	}
	for _, name := range []string{"首页", "设置", "账号", "展开侧边栏"} {
		info, ok := tt.Access(name)
		if !ok {
			t.Fatalf("折叠态缺少 %q：%q", name, tt.Texts())
		}
		if info.Description != name {
			t.Errorf("%q 的悬浮说明 = %q，期望同名 tooltip", name, info.Description)
		}
	}

	// 指针停在「首页」上：tooltip 浮层出现，文字与行同名。
	home, ok := tt.Access("首页")
	if !ok {
		t.Fatal("找不到「首页」图标")
	}
	tt.Move(home.Bounds.X+home.Bounds.W/2, home.Bounds.Y+home.Bounds.H/2)
	tt.Advance(800 * time.Millisecond)
	tip, ok := tt.AccessByRole(ui.RoleTooltip, "首页")
	if !ok {
		t.Fatalf("悬浮「首页」没有出现 tooltip：%q", tt.Texts())
	}
	if tip.Bounds.X < home.Bounds.X+home.Bounds.W {
		t.Errorf("tooltip 应浮在图标右侧：tooltip x=%.1f，图标右缘 %.1f",
			tip.Bounds.X, home.Bounds.X+home.Bounds.W)
	}
}
