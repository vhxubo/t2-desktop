package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// sidebarWidth 是左侧导航栏的宽度，与 view 里的布局一致。底部的设置、外观
// 与账号入口都得待在这条带子里，不能溢到右侧内容区。
const sidebarWidth = 150

// 设置入口在主页左侧的导航栏里，三档外观在它上面，登录 / 退出登录再往下：
// 这三样都不在标题栏里了，标题栏只留品牌、铃铛和登录后的头像昵称。
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
	login, ok := tt.Find("登录")
	if !ok {
		t.Fatalf("侧边栏没有「登录」入口：%q", tt.Texts())
	}
	if login.Y <= set.Y {
		t.Errorf("「登录」的 y=%.0f，应在设置入口（y=%.0f）下方", login.Y, set.Y)
	}
	for _, label := range []string{"外观：跟随系统", "外观：浅色", "外观：深色", "登录"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("侧边栏缺少 %q：%q", label, tt.Texts())
		}
		if r.X+r.W > sidebarWidth {
			t.Errorf("%q 的 x=%.0f 宽 %.0f，超出 %d 宽的侧边栏", label, r.X, r.W, sidebarWidth)
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

// 「动态」归入「浏览」：侧边栏只剩一个分组，动态排在公开榜单之后；未登录
// 时也照常显示（点它才弹授权），不再单独占一个「我的」分组。
func TestSidebarTimelineBelongsToBrowse(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	browse, ok := tt.Find("浏览")
	if !ok {
		t.Fatalf("侧边栏没有「浏览」分组：%q", tt.Texts())
	}
	if _, ok := tt.Find("我的"); ok {
		t.Errorf("侧边栏不该再有「我的」分组：%q", tt.Texts())
	}
	timeline, ok := tt.Find("动态")
	if !ok {
		t.Fatalf("侧边栏没有「动态」：%q", tt.Texts())
	}
	if timeline.Y <= browse.Y {
		t.Errorf("「动态」的 y=%.0f，应在「浏览」分组标题（y=%.0f）下方", timeline.Y, browse.Y)
	}
	last, ok := tt.Find("社区讨论")
	if !ok {
		t.Fatalf("侧边栏缺少「社区讨论」：%q", tt.Texts())
	}
	if timeline.Y <= last.Y {
		t.Errorf("「动态」的 y=%.0f，应排在「社区讨论」（y=%.0f）之后", timeline.Y, last.Y)
	}
	if timeline.X >= sidebarWidth {
		t.Errorf("「动态」的 x=%.0f 越出了侧边栏宽度 %d", timeline.X, sidebarWidth)
	}
}

// 未登录时侧边栏的「登录」直接打开浏览器授权，不离开当前页；等待期间变成
// 「粘贴回调」与「取消」，点「取消」就回到未登录。
func TestSidebarLoginStartsAndCancels(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	t.Cleanup(a.auth.Cancel)
	if err := tt.Click("登录"); err != nil {
		t.Fatal(err)
	}
	if a.pageName != "list" {
		t.Fatalf("点「登录」不该进登录页，实际页面 = %q", a.pageName)
	}
	if !a.loginPending {
		t.Fatal("点「登录」后没有进入等待授权状态")
	}
	tt.Frame()
	if !tt.HasText("等待浏览器授权…") {
		t.Errorf("侧边栏没有显示等待授权：%q", tt.Texts())
	}
	if _, ok := tt.Find("登录"); ok {
		t.Errorf("等待授权期间不该还摆着「登录」：%q", tt.Texts())
	}
	for _, label := range []string{"粘贴回调", "取消"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("等待授权期间缺少 %q：%q", label, tt.Texts())
		}
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if a.loginPending {
		t.Error("点「取消」后仍在等待授权")
	}
	tt.Frame()
	if _, ok := tt.Find("登录"); !ok {
		t.Errorf("取消后应回到「登录」入口：%q", tt.Texts())
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
	if _, ok := tt.Find("退出登录"); !ok {
		t.Fatalf("侧边栏没有「退出登录」入口：%q", tt.Texts())
	}
	if err := tt.Click("退出登录"); err != nil {
		t.Fatal(err)
	}
	if a.auth.loggedIn() {
		t.Fatal("点「退出登录」后仍是登录状态")
	}
	tt.Frame()
	if _, ok := tt.Find("登录"); !ok {
		t.Errorf("退出后侧边栏应回到「登录」：%q", tt.Texts())
	}
	if _, ok := tt.Find("退出登录"); ok {
		t.Errorf("退出后不该还留着「退出登录」：%q", tt.Texts())
	}
}

// 侧边栏多了「关于」入口：点一下打开「关于」窗口。它排在设置下方、账号入口
// 上方，并且整行仍待在侧边栏宽度之内，不溢到右侧内容区。
func TestSidebarAboutOpensWindow(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	about, ok := tt.Find("关于")
	if !ok {
		t.Fatalf("侧边栏没有「关于」入口：%q", tt.Texts())
	}
	if about.X+about.W > sidebarWidth {
		t.Errorf("「关于」的 x=%.0f 宽 %.0f，超出 %d 宽的侧边栏", about.X, about.W, sidebarWidth)
	}
	set, _ := tt.Find("设置")
	if about.Y <= set.Y {
		t.Errorf("「关于」的 y=%.0f，应在设置入口（y=%.0f）下方", about.Y, set.Y)
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
	for i, lab := range []string{"开发者", "邮箱", "项目地址", "2Libra 主页"} {
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
