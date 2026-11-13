package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

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

	clicked := false
	a.onUpdateCheck = func() { clicked = true }
	if err := tt.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	if !clicked {
		t.Fatal("点击「检查更新」没有触发更新检查")
	}
}
