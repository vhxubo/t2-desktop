package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 关于窗口的品牌区用随包的应用图标（任务栏 / 托盘那枚 T2），浅色 / 深色两份
// 都能解码；图标在窗口里是合适的小尺寸，不再是铺满宽度的大图。
func TestAboutWindowShowsAppIcon(t *testing.T) {
	for _, tc := range []struct {
		name string
		dark bool
	}{
		{"light", false},
		{"dark", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := aboutIconSource(tc.dark)
			bmp, ok := src.(*ui.Bitmap)
			if !ok {
				t.Fatalf("应用图标没有解码成位图：%T", src)
			}
			w, h := bmp.Size()
			if w < 64 || h < 64 || w != h {
				t.Fatalf("应用图标尺寸不对：%dx%d", w, h)
			}
		})
	}
	if aboutWindowWidth < 700 || aboutWindowHeight < 720 {
		t.Errorf("关于窗口不够大气：%dx%d", aboutWindowWidth, aboutWindowHeight)
	}

	a := testApp(t)
	tt := ui.NewTester(a.aboutWindowView, aboutWindowWidth, aboutWindowHeight)
	tt.Frame()
	r, ok := tt.Find("T2 应用图标")
	if !ok {
		t.Fatalf("关于窗口没有画应用图标：%q", tt.Texts())
	}
	if r.W > 140 || r.H > 140 || r.W < 60 || r.H < 60 {
		t.Errorf("应用图标尺寸不合适：%v", r)
	}
	if !tt.HasText("T2 Desktop") || !tt.HasText("Third Party 2Libra") {
		t.Errorf("关于窗口缺少应用名或定位：%q", tt.Texts())
	}
}

// 开发者信息精简成四个蓝色可点击短文字，横向并排且不展示具体链接；致谢
// 名单里的 Jimmy 是一枚显眼的胶囊按钮，点一下打开他的 2Libra 主页。
func TestAboutWindowHasCompactLinksAndThanks(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.aboutWindowView, aboutWindowWidth, aboutWindowHeight)
	tt.Frame()

	labels := []string{"开发者 vhxubo", "邮箱", "项目地址", "2Libra 主页", "致谢", "Jimmy"}
	for _, want := range labels {
		if !tt.HasText(want) {
			t.Errorf("关于窗口缺少 %q：%q", want, tt.Texts())
		}
	}
	for _, url := range []string{
		"https://github.com/vhxubo", "vhxubo@gmail.com",
		"github.com/vhxubo/t2-desktop", "https://2libra.com/user/vhxubo/about",
	} {
		if tt.HasText(url) {
			t.Errorf("关于窗口还把具体链接 %q 铺在界面上", url)
		}
	}
	first, ok := tt.Find(labels[0])
	if !ok {
		t.Fatalf("找不到开发者入口：%q", tt.Texts())
	}
	for _, label := range labels[1:4] {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("找不到入口 %q：%q", label, tt.Texts())
		}
		if r.Y < first.Y-4 || r.Y > first.Y+4 {
			t.Errorf("入口 %q 没和开发者入口并排：%v vs %v", label, r, first)
		}
	}

	// Jimmy 不再是小 Tag：字更大，且点击会打开对应主页。
	jimmy, ok := tt.Find("Jimmy")
	if !ok {
		t.Fatalf("找不到致谢入口：%q", tt.Texts())
	}
	// 致谢里的名字和开发者入口是同一种链接文字，字号一致，不额外放大。
	if jimmy.H > first.H+2 || jimmy.H < first.H-2 {
		t.Errorf("Jimmy 字号和开发者入口不一致：%v vs %v", jimmy, first)
	}
	if err := tt.Click("Jimmy"); err != nil {
		t.Fatalf("点 Jimmy 失败：%v", err)
	}
	var opened bool
	for _, u := range tt.OpenedURLs() {
		if u == jimmyAboutURL {
			opened = true
		}
	}
	if !opened {
		t.Errorf("点 Jimmy 没有打开 %s，实际 %v", jimmyAboutURL, tt.OpenedURLs())
	}
}

// 界面字体标识放在整页最下方，排在条款区块之后。
func TestAboutWindowFontNoticeAtBottom(t *testing.T) {
	if !usesHarmonySans() {
		t.Skip("macOS 用系统苹方，不显示鸿蒙字体声明")
	}
	a := testApp(t)
	tt := ui.NewTester(a.aboutWindowView, aboutWindowWidth, aboutWindowHeight)
	tt.Frame()

	font, ok := tt.Find("界面字体：鸿蒙字体 HarmonyOS Sans · © 2021 Huawei")
	if !ok {
		t.Fatalf("关于窗口没有字体标识：%q", tt.Texts())
	}
	terms, ok := tt.Find("服务条款")
	if !ok {
		t.Fatalf("关于窗口没有条款区块：%q", tt.Texts())
	}
	if font.Y <= terms.Y {
		t.Errorf("字体标识没放在条款之后：font=%v terms=%v", font, terms)
	}
}

// 「关于 - 条款」补齐第三方接入条款要求的逐项权限用途、撤销方式与责任划分，
// 且整段不被 Rows 截断（窗口可滚动）。
func TestAboutWindowListsScopesAndTerms(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.aboutWindowView, aboutWindowWidth, aboutWindowHeight)
	tt.Frame()
	for _, want := range []string{
		"与 2Libra 无隶属或合作关系", "Vibe Coding", "AI 辅助编程", "开发方式",
		"授权与权限", "默认申请的权限及用途",
		"read:me（通知、收藏、关注与合集）", "write:economy（签到、打赏、盲盒等金币变动）",
		"撤销后下一个请求即失效", "责任与风险", "系统凭据库", "社区内容",
		"不代表开发者的思想、观点或意识形态", "第三方服务与可用性",
	} {
		if !tt.HasText(want) {
			t.Errorf("关于窗口条款缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 关于窗口展示当前版本，并提供一个检查更新入口；点一下走和设置窗口同一套
// 检查逻辑（无头测试用 onUpdateCheck 钩子确认接线，不碰更新插件的主循环）。
func TestAboutWindowShowsVersionAndUpdateCheck(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.aboutWindowView, aboutWindowWidth, aboutWindowHeight)
	tt.Frame()

	if !tt.HasText("当前版本") {
		t.Errorf("关于窗口没有展示版本号：%q", tt.Texts())
	}
	if !tt.HasText("检查更新") {
		t.Fatalf("关于窗口没有检查更新入口：%q", tt.Texts())
	}
	if usesHarmonySans() && !tt.HasText("鸿蒙字体") {
		t.Errorf("关于窗口没有鸿蒙字体的授权声明：%q", tt.Texts())
	}

	clicked := false
	a.onUpdateCheck = func() { clicked = true }
	if err := tt.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	if !clicked {
		t.Fatal("点击「检查更新」没有触发更新检查")
	}
}
