package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 「关于 - 条款」补齐第三方接入条款要求的逐项权限用途、撤销方式与责任划分，
// 且整段不被 Rows 截断（窗口可滚动）。
func TestAboutWindowListsScopesAndTerms(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.aboutWindowView, aboutWindowWidth, aboutWindowHeight)
	tt.Frame()
	for _, want := range []string{
		"与 2Libra 无隶属或合作关系", "授权与权限", "默认申请的权限及用途",
		"read:me（通知、收藏、关注与合集）", "write:economy（签到、打赏、盲盒等金币变动）",
		"撤销后下一个请求即失效", "责任与风险", "系统凭据库", "第三方服务与可用性",
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
