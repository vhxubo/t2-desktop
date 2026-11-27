package main

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
)

// fakeStartup 把开机启动状态保存在内存里，避免无头测试碰到原生登录项。
// 返回值指向这份状态，用例可以直接改写它来模拟「已经开启」。
func fakeStartup(t *testing.T) *bool {
	t.Helper()
	state := false
	oldGet, oldSet := openAtLoginForApp, setOpenAtLoginForApp
	openAtLoginForApp = func() bool { return state }
	setOpenAtLoginForApp = func(on bool) error { state = on; return nil }
	t.Cleanup(func() { openAtLoginForApp, setOpenAtLoginForApp = oldGet, oldSet })
	return &state
}

// 设置窗口「通用」页有开机启动开关，全新安装读回的是关闭状态。
func TestStartupSettingsDefaultOff(t *testing.T) {
	state := fakeStartup(t)
	a := testApp(t)
	a.settingsTab = settingsTabGeneral
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1400)
	st.Frame()
	if !st.HasText("开机启动") {
		t.Fatalf("通用页没有开机启动开关：%q", st.Texts())
	}
	if *state {
		t.Fatal("默认不该开机启动")
	}
}

// 点一下开、再点一下关，每次都立即写到系统登录项并给出提示。
func TestStartupSettingsToggle(t *testing.T) {
	state := fakeStartup(t)
	a := testApp(t)
	a.settingsTab = settingsTabGeneral
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1400)
	st.Frame()

	if err := st.Click("开机启动"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !*state {
		t.Fatal("点开关后没有开启开机启动")
	}
	if !st.HasText("已开启开机启动") {
		t.Errorf("开启后没有提示：%q", st.Texts())
	}

	if err := st.Click("开机启动"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if *state {
		t.Fatal("再点开关后没有关闭开机启动")
	}
	if !st.HasText("已关闭开机启动") {
		t.Errorf("关闭后没有提示：%q", st.Texts())
	}
}

// 系统拒绝写登录项时只提示失败，不能假装已经生效。
func TestStartupSettingsApplyError(t *testing.T) {
	oldGet, oldSet := openAtLoginForApp, setOpenAtLoginForApp
	t.Cleanup(func() { openAtLoginForApp, setOpenAtLoginForApp = oldGet, oldSet })
	openAtLoginForApp = func() bool { return false }
	setOpenAtLoginForApp = func(bool) error { return errors.New("系统拒绝") }

	a := testApp(t)
	a.settingsTab = settingsTabGeneral
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1400)
	st.Frame()
	if err := st.Click("开机启动"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !st.HasText("设置开机启动失败：系统拒绝") {
		t.Errorf("写登录项失败后没有提示：%q", st.Texts())
	}
}

// 一键重置把开机启动也恢复成默认的关闭。
func TestResetAllSettingsDisablesStartup(t *testing.T) {
	state := fakeStartup(t)
	*state = true
	a := testApp(t)
	a.resetAllSettings()
	if *state {
		t.Fatal("重置后仍然开机启动")
	}
}
