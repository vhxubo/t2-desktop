package main

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestCommandShortcutCanonicalRoundTrip(t *testing.T) {
	cases := []struct {
		shortcut commandShortcut
		raw      string
	}{
		{cmdKey(ui.KeyK), "cmd+k"},
		{cmdShiftKey(ui.KeyF), "cmd+shift+f"},
		{commandShortcut{Mods: ui.Cmd | ui.Alt | ui.Shift, Key: ui.KeyF12}, "cmd+alt+shift+f12"},
		{commandShortcut{Mods: ui.Cmd, Key: ui.KeyComma}, "cmd+comma"},
	}
	for _, tc := range cases {
		if got := tc.shortcut.canonical(); got != tc.raw {
			t.Errorf("canonical = %q，期望 %q", got, tc.raw)
		}
		back, ok := parseCommandShortcut(tc.raw)
		if !ok || back != tc.shortcut {
			t.Errorf("parse(%q) = %+v, %v，期望 %+v", tc.raw, back, ok, tc.shortcut)
		}
	}
	for _, raw := range []string{"", "k", "cmd+", "cmd+nope", "cmd+k+x"} {
		if _, ok := parseCommandShortcut(raw); ok {
			t.Errorf("%q 不该解析成有效键位", raw)
		}
	}
}

func TestCommandPrefsPersist(t *testing.T) {
	dir := t.TempDir()
	prefs := newCommandPrefs(dir)
	if err := prefs.setBinding("settings", "cmd+shift+t"); err != nil {
		t.Fatal(err)
	}
	if on, err := prefs.toggleFavorite("coins"); err != nil || !on {
		t.Fatalf("收藏金币失败：on=%v err=%v", on, err)
	}
	if err := prefs.noteUsed("post"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < recentLimit+3; i++ {
		if err := prefs.noteUsed("cmd" + string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}

	reloaded := newCommandPrefs(dir)
	if raw, ok := reloaded.binding("settings"); !ok || raw != "cmd+shift+t" {
		t.Errorf("快捷键没有落盘：raw=%q ok=%v", raw, ok)
	}
	snap := reloaded.snapshot()
	if len(snap.Favorites) != 1 || snap.Favorites[0] != "coins" {
		t.Errorf("收藏没有落盘：%v", snap.Favorites)
	}
	if len(snap.Recent) != recentLimit || snap.Recent[0] != "cmdi" {
		t.Errorf("最近使用没有按上限保存：%v", snap.Recent)
	}

	if err := reloaded.resetBinding("settings"); err != nil {
		t.Fatal(err)
	}
	if _, ok := newCommandPrefs(dir).binding("settings"); ok {
		t.Error("恢复默认后不该还有用户覆盖项")
	}
}

func TestCommandRegistryBindingConflictAndRegistration(t *testing.T) {
	a := testApp(t)
	r := a.commands
	settingsDefault := r.binding("settings")
	if !settingsDefault.valid() {
		t.Fatal("设置命令应该有默认快捷键")
	}

	// 自定义命令也能注册进来，并带上自己的默认键位。
	called := false
	if !r.register(commandDef{
		ID: "test.custom", Title: "自定义命令", Category: commandCategoryTools,
		DefaultShortcut: cmdKey(ui.KeyT), Run: func() { called = true },
	}) {
		t.Fatal("注册自定义命令失败")
	}
	if r.register(commandDef{ID: "test.custom", Title: "重名", Run: func() {}}) {
		t.Error("重复 id 不该注册成功")
	}
	if got := r.binding("test.custom"); got != cmdKey(ui.KeyT) {
		t.Errorf("自定义命令的默认键位不对：%s", got.canonical())
	}
	if other := r.conflict("test.custom", cmdKey(ui.KeyT)); other != "" {
		t.Errorf("同一个命令自己的绑定不该算冲突：%s", other)
	}
	if other := r.conflict("test.custom", settingsDefault); other != "设置" {
		t.Errorf("没识别出与「设置」的冲突：%q", other)
	}

	if err := r.prefs.setBinding("test.custom", cmdKey(ui.KeyT).canonical()); err != nil {
		t.Fatal(err)
	}
	if err := r.prefs.setBinding("test.custom", ""); err != nil {
		t.Fatal(err)
	}
	if r.binding("test.custom").valid() {
		t.Error("显式清空的键位不该再生效")
	}
	if err := r.prefs.resetBinding("test.custom"); err != nil {
		t.Fatal(err)
	}
	if got := r.binding("test.custom"); got != cmdKey(ui.KeyT) {
		t.Errorf("恢复默认后键位不对：%s", got.canonical())
	}

	a.runCommandByID("test.custom")
	if !called {
		t.Error("按 id 执行自定义命令没有调用它的 Run")
	}
	if err := r.prefs.resetAllBindings(); err != nil {
		t.Fatal(err)
	}
	if got := r.binding("settings"); got != settingsDefault {
		t.Error("全部恢复默认后设置命令的键位不对")
	}
}

func TestCommandPrefsFileLocation(t *testing.T) {
	dir := t.TempDir()
	p := newCommandPrefs(dir)
	want := filepath.Join(dir, commandPrefsFile)
	if p.path() != want {
		t.Fatalf("偏好文件路径 = %s，期望 %s", p.path(), want)
	}
}
