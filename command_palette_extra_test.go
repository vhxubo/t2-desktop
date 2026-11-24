package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestCommandPaletteGroupsFavoritesAndRecents(t *testing.T) {
	a := testApp(t)
	if _, err := a.commands.prefs.toggleFavorite("settings"); err != nil {
		t.Fatal(err)
	}
	if err := a.commands.prefs.noteUsed("coins"); err != nil {
		t.Fatal(err)
	}
	rows := a.paletteRows("")
	if len(rows) == 0 {
		t.Fatal("命令面板没有可显示的行")
	}
	if rows[0].heading != "收藏" || rows[0].cmd.id != "settings" {
		t.Fatalf("第一行应该是收藏的设置：%+v", rows[0])
	}
	foundRecent := false
	for _, row := range rows {
		if row.heading == "最近使用" && row.cmd.id == "coins" {
			foundRecent = true
		}
		if row.cmd.id == "settings" && row.heading == commandCategoryTools {
			t.Error("收藏的命令不该在分类里重复出现")
		}
	}
	if !foundRecent {
		t.Error("最近使用的金币命令没有出现在对应分组")
	}
	// 每条命令只出现一次。
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.cmd.id] {
			t.Errorf("命令 %s 重复出现", row.cmd.id)
		}
		seen[row.cmd.id] = true
	}
}

func TestCommandPaletteIncludesRegisteredCommandsAndShortcuts(t *testing.T) {
	a := testApp(t)
	ran := false
	a.commands.register(commandDef{
		ID: "test.palette", Title: "自定义命令", Category: commandCategoryTools,
		Hint: "测试注册入口", Keywords: "zidingyi custom",
		DefaultShortcut: cmdKey(ui.KeyT), Run: func() { ran = true },
	})
	tt := ui.NewTester(a.view, 1080, 2400)
	tt.Frame()
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Type("自定义")
	tt.Frame()
	if _, ok := tt.Find("命令 自定义命令"); !ok {
		t.Fatalf("新注册的命令没有出现在面板：%q", tt.Texts())
	}
	if shortcut := a.commands.bindingLabel("test.palette"); shortcut == "" || !tt.HasText(shortcut) {
		t.Errorf("面板没有显示命令的快捷键 %q：%q", shortcut, tt.Texts())
	}
	if err := tt.Click("命令 自定义命令"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !ran {
		t.Error("点击注册的命令没有执行它的 Run")
	}
}

func TestCommandPaletteStarTogglesWithoutRunning(t *testing.T) {
	a := testApp(t)
	ran := false
	a.commands.register(commandDef{
		ID: "test.star", Title: "星标命令", Category: commandCategoryTools,
		Hint: "测试星标", Run: func() { ran = true },
	})
	tt := ui.NewTester(a.view, 1080, 2400)
	tt.Frame()
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Type("星标")
	tt.Frame()
	if err := tt.Click("收藏 星标命令"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if ran {
		t.Error("点星标不应该执行命令")
	}
	if !a.palette.open {
		t.Error("点星标不应该关掉面板")
	}
	rows := a.paletteRows("")
	if len(rows) == 0 || rows[0].cmd.id != "test.star" || rows[0].heading != "收藏" {
		t.Fatalf("星标后命令没有排到收藏分组：%+v", rows)
	}
	if err := tt.Click("收藏 星标命令"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, row := range a.paletteRows("") {
		if row.cmd.id == "test.star" && row.heading == "收藏" {
			t.Error("再点一次星标应该取消收藏")
		}
	}
}

func TestCommandShortcutTriggersRegisteredCommand(t *testing.T) {
	a := testApp(t)
	ran := false
	a.commands.register(commandDef{
		ID: "test.hotkey", Title: "热键命令", Category: commandCategoryTools,
		DefaultShortcut: cmdKey(ui.KeyT), Run: func() { ran = true },
	})
	tt := ui.NewTester(a.view, 1080, 2400)
	tt.Frame()

	tt.Key(ui.Cmd, ui.KeyT)
	tt.Frame()
	if !ran {
		t.Error("默认快捷键没有触发注册的命令")
	}

	// 用户自定义后，旧键位不再触发，新键位生效。
	if err := a.commands.prefs.setBinding("test.hotkey", cmdShiftKey(ui.KeyT).canonical()); err != nil {
		t.Fatal(err)
	}
	ran = false
	tt.Key(ui.Cmd, ui.KeyT)
	tt.Frame()
	if ran {
		t.Error("改过键位后旧组合不该继续触发")
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyT)
	tt.Frame()
	if !ran {
		t.Error("用户配置的组合键没有触发命令")
	}
}

func TestCommandShortcutOpensPalette(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 2400)
	tt.Frame()
	if a.palette.open {
		t.Fatal("一开始命令面板不该是展开的")
	}
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	if !a.palette.open {
		t.Error("Cmd/Ctrl+K 没有打开命令面板")
	}
}
