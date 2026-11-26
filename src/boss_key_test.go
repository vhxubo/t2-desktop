package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestBossKeyPrefsPersistAndValidate(t *testing.T) {
	dir := t.TempDir()
	p := newBossKeyPrefs(dir)
	if p.on() {
		t.Fatal("老板键默认应该关闭")
	}
	if p.accelerator() != bossKeyDefaultAcc {
		t.Fatalf("默认热键 = %q，想要 %q", p.accelerator(), bossKeyDefaultAcc)
	}
	if err := p.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := p.setAccelerator("CmdOrCtrl+Shift+K"); err != nil {
		t.Fatal(err)
	}
	again := newBossKeyPrefs(dir)
	if !again.on() || again.accelerator() != "CmdOrCtrl+Shift+K" {
		t.Fatalf("重读偏好 = %v / %q", again.on(), again.accelerator())
	}

	// 坏配置不能让应用起不来：无效写法退回默认，开关本身仍按文件里的值。
	if err := os.WriteFile(filepath.Join(dir, bossKeyPrefsFile),
		[]byte(`{"enabled":true,"accelerator":"nonsense"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := newBossKeyPrefs(dir); !got.on() || got.accelerator() != bossKeyDefaultAcc {
		t.Fatalf("坏配置重读 = %v / %q", got.on(), got.accelerator())
	}
	if err := again.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := newBossKeyPrefs(dir); got.on() || got.accelerator() != bossKeyDefaultAcc {
		t.Fatalf("重置后 = %v / %q", got.on(), got.accelerator())
	}
}

func TestBossKeyAcceleratorConversion(t *testing.T) {
	cases := []struct {
		shortcut commandShortcut
		acc      string
	}{
		{commandShortcut{Mods: ui.Cmd | ui.Alt, Key: ui.KeyB}, "CmdOrCtrl+Alt+B"},
		{commandShortcut{Mods: ui.Cmd | ui.Shift, Key: ui.KeyF12}, "CmdOrCtrl+Shift+F12"},
		{commandShortcut{Mods: ui.Cmd, Key: ui.KeySpace}, "CmdOrCtrl+Space"},
		{commandShortcut{Mods: ui.Cmd, Key: ui.KeyMinus}, "CmdOrCtrl+-"},
		{commandShortcut{Mods: ui.Alt, Key: ui.KeyA}, "Alt+A"},
	}
	for _, tc := range cases {
		acc, ok := bossKeyAcceleratorFromShortcut(tc.shortcut)
		if !ok || acc != tc.acc {
			t.Errorf("转换 %v = %q / %v，想要 %q", tc.shortcut, acc, ok, tc.acc)
			continue
		}
		if !validBossKeyAcc(acc) {
			t.Errorf("生成的 accelerator 校验不过：%q", acc)
		}
		if label := bossKeyAccLabel(acc); label == "" {
			t.Errorf("%q 没有可读标签", acc)
		}
	}
	// 没有修饰键、以及 accelerator 不认识的键，都不能录成系统热键。
	if _, ok := bossKeyAcceleratorFromShortcut(commandShortcut{Key: ui.KeyA}); ok {
		t.Error("不带修饰键的组合不该被接受")
	}
	if _, ok := bossKeyAcceleratorFromShortcut(commandShortcut{Mods: ui.Cmd, Key: ui.KeyUnknown}); ok {
		t.Error("未知主键不该被接受")
	}
	for _, bad := range []string{"", "b", "Alt+", "Alt+Nope", "Alt+B+Shift"} {
		if validBossKeyAcc(bad) {
			t.Errorf("无效 accelerator 却通过了校验：%q", bad)
		}
	}
}

func TestBossKeyToggleRestoresOnlyShownWindows(t *testing.T) {
	a := testApp(t)
	a.bossKeyHidden = true
	// 没有原生窗口时，两个回调都只改状态；隐藏过的窗口列表为空，切两次仍
	// 该回到初始状态，不会卡在「已隐藏」。
	a.toggleBossKey()
	if a.bossKeyHidden {
		t.Error("第二次触发应该恢复窗口")
	}
	a.toggleBossKey()
	if !a.bossKeyHidden || len(a.bossKeyShown) != 0 {
		t.Fatalf("第一次触发应该隐藏：hidden=%v shown=%d", a.bossKeyHidden, len(a.bossKeyShown))
	}
	a.restoreBossKeyWindows()
	if a.bossKeyHidden {
		t.Error("恢复后不该仍是隐藏状态")
	}
}

func TestBossKeyApplyRegistersAndUnregisters(t *testing.T) {
	var registered, unregistered []string
	oldRegister, oldUnregister := bossKeyRegister, bossKeyUnregister
	t.Cleanup(func() { bossKeyRegister, bossKeyUnregister = oldRegister, oldUnregister })
	bossKeyRegister = func(acc string, fn func()) error {
		registered = append(registered, acc)
		return nil
	}
	bossKeyUnregister = func(acc string) { unregistered = append(unregistered, acc) }

	dir := t.TempDir()
	p := newBossKeyPrefs(dir)
	a := &app{bossKeyPrefs: p}
	d := &desktop{mainLoop: true, bossKeyPrefs: p, app: a}
	a.desktop = d

	// 默认关闭：不占用任何系统热键。
	d.applyBossKey()
	if len(registered) != 0 {
		t.Fatalf("关闭时不该注册热键：%v", registered)
	}

	if err := p.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	d.applyBossKey()
	if len(registered) != 1 || registered[0] != bossKeyDefaultAcc {
		t.Fatalf("启用后注册结果 = %v", registered)
	}
	// 重复应用不该重复注册同一组热键。
	d.applyBossKey()
	if len(registered) != 1 {
		t.Fatalf("重复应用不该再注册：%v", registered)
	}

	if err := p.setAccelerator("CmdOrCtrl+Shift+K"); err != nil {
		t.Fatal(err)
	}
	d.applyBossKey()
	if len(unregistered) != 1 || unregistered[0] != bossKeyDefaultAcc {
		t.Fatalf("换热键应该注销旧的：%v", unregistered)
	}
	if len(registered) != 2 || registered[1] != "CmdOrCtrl+Shift+K" {
		t.Fatalf("换热键应该注册新的：%v", registered)
	}

	// 关掉时注销，并把藏起来的窗口放回来。
	a.bossKeyHidden = true
	if err := p.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	d.applyBossKey()
	if len(unregistered) != 2 || unregistered[1] != "CmdOrCtrl+Shift+K" {
		t.Fatalf("关闭时应该注销：%v", unregistered)
	}
	if a.bossKeyHidden {
		t.Error("关闭老板键后不该继续隐藏窗口")
	}
}

func TestBossKeySuppressesNotificationsWhileHidden(t *testing.T) {
	a := testApp(t)
	var shown []nativeNotice
	a.desktop = &desktop{notify: func(n nativeNotice) error {
		shown = append(shown, n)
		return nil
	}}
	notices := []nativeNotice{{ID: "N1", Title: "新回复", Target: "topic:1"}}

	a.bossKeyHidden = true
	a.showNotices(notices)
	if len(shown) != 0 {
		t.Fatalf("老板键隐藏时不该弹系统通知：%+v", shown)
	}
	if got := a.noticeTarget("N1"); got != "" {
		t.Errorf("被屏蔽的通知不该记住点击目标：%q", got)
	}

	a.restoreBossKeyWindows()
	a.showNotices(notices)
	if len(shown) != 1 || shown[0].ID != "N1" {
		t.Fatalf("恢复后通知应该照常弹出：%+v", shown)
	}
}

func TestBossKeySettingsRecordsAccelerator(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabShortcuts
	tt := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 3200)
	tt.Frame()
	for _, label := range []string{"老板键", "启用老板键", "录制老板键", "恢复老板键默认"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("快捷键页缺少「%s」：%q", label, tt.Texts())
		}
	}
	if got := bossKeyAccLabel(bossKeyDefaultAcc); !tt.HasText(got) {
		t.Errorf("没有显示默认热键 %q：%q", got, tt.Texts())
	}

	if err := tt.Click("录制老板键"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.bossKeyRecording {
		t.Fatal("没有进入老板键录制状态")
	}
	if _, ok := tt.Find("老板键录制框"); !ok {
		t.Fatalf("录制框没有出现：%q", tt.Texts())
	}
	tt.Key(ui.Cmd|ui.Alt, ui.KeyB)
	tt.Frame()
	if a.bossKeyRecording {
		t.Error("录到键位后没有退出录制状态")
	}
	if got := a.bossKeyPrefs.accelerator(); got != bossKeyDefaultAcc {
		t.Fatalf("录制的热键没有保存：%q", got)
	}

	// 与命令默认键位冲突时给出提示，且不改动已保存的热键。
	if err := a.bossKeyPrefs.setAccelerator("CmdOrCtrl+Shift+K"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("录制老板键"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(ui.Cmd|ui.Shift, ui.KeyK)
	tt.Frame()
	if a.bossKeyConflict == "" {
		t.Error("冲突的键位没有给出提示")
	}
	if got := a.bossKeyPrefs.accelerator(); got != "CmdOrCtrl+Shift+K" {
		t.Errorf("冲突时不该改动热键：%q", got)
	}
}
