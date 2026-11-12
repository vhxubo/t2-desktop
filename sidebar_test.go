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
