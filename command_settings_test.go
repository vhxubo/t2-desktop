package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func settingsShortcutTester(t *testing.T, a *app) *ui.Tester {
	t.Helper()
	a.settingsTab = settingsTabShortcuts
	// 快捷键页列了全部命令，比设置窗口默认高度长；测试给足高度，好点到尾部按钮。
	return ui.NewTester(a.settingsWindowView, settingsWindowWidth, 2400)
}

func TestShortcutSettingsListsCommands(t *testing.T) {
	a := testApp(t)
	tt := settingsShortcutTester(t, a)
	tt.Frame()
	for _, label := range []string{"快捷键 设置", "快捷键 发帖", "录制 设置", "清除 设置", "恢复 设置", "全部恢复默认"} {
		if _, ok := tt.Find(label); !ok {
			t.Errorf("快捷键页缺少「%s」：%q", label, tt.Texts())
		}
	}
	if !tt.HasText("Ctrl+K") && !tt.HasText("⌘K") {
		t.Errorf("没有显示命令的默认键位：%q", tt.Texts())
	}
}

func TestShortcutSettingsRecordsBinding(t *testing.T) {
	a := testApp(t)
	tt := settingsShortcutTester(t, a)
	tt.Frame()
	if err := tt.Click("录制 设置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.shortcutRecordingID != "settings" {
		t.Fatalf("没有进入录制状态：%q", a.shortcutRecordingID)
	}
	if _, ok := tt.Find("快捷键录制框"); !ok {
		t.Fatalf("录制框没有出现：%q", tt.Texts())
	}

	tt.Key(ui.Cmd|ui.Shift, ui.KeyT)
	tt.Frame()
	if a.shortcutRecordingID != "" {
		t.Error("录到键位后没有退出录制状态")
	}
	raw, ok := a.commands.prefs.binding("settings")
	if !ok || raw != "cmd+shift+t" {
		t.Fatalf("录制的键位没有保存：raw=%q ok=%v", raw, ok)
	}
	if got := a.commands.bindingLabel("settings"); got != "Ctrl+Shift+T" && got != "⌘+Shift+T" {
		t.Errorf("列表里显示的键位不对：%q", got)
	}

	// 清除后这条命令没有快捷键，再恢复默认回到出厂键位。
	if err := tt.Click("清除 设置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.commands.binding("settings").valid() {
		t.Error("清除后不该还有键位")
	}
	if err := tt.Click("恢复 设置"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if raw, ok := a.commands.prefs.binding("settings"); ok {
		t.Errorf("恢复默认后不该保留用户覆盖项：%q", raw)
	}
}

func TestShortcutSettingsRejectsConflictAndCancels(t *testing.T) {
	a := testApp(t)
	tt := settingsShortcutTester(t, a)
	tt.Frame()
	if err := tt.Click("录制 发帖"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	// cmd+shift+k 是「快捷键设置」的默认键位。
	tt.Key(ui.Cmd|ui.Shift, ui.KeyK)
	tt.Frame()
	if a.shortcutConflict == "" {
		t.Error("冲突的键位没有给出提示")
	}
	if raw, ok := a.commands.prefs.binding("post"); ok {
		t.Errorf("冲突时不应该改掉原有绑定：%q", raw)
	}
	if a.shortcutRecordingID != "" {
		t.Error("冲突后应该退出录制状态")
	}

	// 再录制一次，用 Esc 取消，不写任何配置。
	a.shortcutConflict = ""
	if err := tt.Click("录制 发帖"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.shortcutRecordingID != "" {
		t.Error("Esc 没有取消录制")
	}
	if raw, ok := a.commands.prefs.binding("post"); ok {
		t.Errorf("Esc 取消后不该改动键位：%q", raw)
	}
}

func TestShortcutSettingsResetAll(t *testing.T) {
	a := testApp(t)
	if err := a.commands.prefs.setBinding("post", "cmd+shift+y"); err != nil {
		t.Fatal(err)
	}
	if err := a.commands.prefs.setBinding("settings", ""); err != nil {
		t.Fatal(err)
	}
	tt := settingsShortcutTester(t, a)
	tt.Frame()
	if err := tt.Click("全部恢复默认"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if raw, ok := a.commands.prefs.binding("post"); ok {
		t.Errorf("全部恢复后不该保留 post 的覆盖项：%q", raw)
	}
	if _, ok := a.commands.prefs.binding("settings"); ok {
		t.Error("全部恢复后不该保留 settings 的清除项")
	}
	if got := a.commands.bindingLabel("settings"); got == "" {
		t.Error("全部恢复后默认键位应该重新生效")
	}
}
