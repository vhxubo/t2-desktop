package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 设置窗口的「快捷键」页：命令注册表是唯一数据源，每一行显示当前键位，
// 「录制」把焦点交给一个只在这一帧存在的捕获框，由 HandleInput 收下原始
// 按键（包括字母键，OnShortcut 只能收到已声明的组合）。

// shortcutSettingsSection 画命令键位列表。修改立即落盘并通过 publish 让其它
// 窗口重画：注册表偏好是进程级共享的。
func (a *app) shortcutSettingsSection(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	settingsSection(c, "快捷键", func() {
		settingsDescription(c, "给命令录制组合键，在命令面板与窗口里都能直接触发。"+
			"必须带 Ctrl / Alt / ⌘ 中的一个修饰键，单独的方向键与字母键不会占用输入。"+
			"修改立即保存，对所有已开窗口生效。", 3)

		if a.shortcutConflict != "" {
			row := ui.Row(c).FillWidth().Padding(6, 8).Radius(tk.BorderRadiusSM).
				Background(tk.ErrorBg).AlignItems(ui.Center)
			row.Label("快捷键冲突提示")
			row.Children(func() {
				display.Text(c, a.shortcutConflict, display.TextProps{
					Type: display.TextDanger, FontSize: 11, Rows: 2,
				})
			})
		}

		if a.shortcutRecordingID != "" {
			label := "请按下组合键… Esc 取消"
			if def, ok := a.commands.get(a.shortcutRecordingID); ok {
				label = "为「" + def.Title + "」按下组合键… Esc 取消"
			}
			capture := ui.Box(c).FillWidth().Height(34).Radius(tk.BorderRadiusSM).
				Background(tk.PrimaryBg).Center().
				Focusable().Bind(&a.shortcutCapture).FocusBind(&a.shortcutCaptureFocus, 1)
			capture.Label("快捷键录制框")
			capture.HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind != ui.InputKeyDown || ev.Repeat {
					return true
				}
				if ev.Key == ui.KeyEscape {
					a.stopShortcutRecording()
					return true
				}
				// 修饰键自己按下时 Key 是 KeyUnknown，等用户按下真正的主键。
				if ev.Key == ui.KeyUnknown || ev.Key == 0 {
					return true
				}
				shortcut, ok := commandShortcutFromEvent(ev)
				if !ok {
					a.shortcutConflict = "组合键要包含 Ctrl / Alt / ⌘"
					a.stopShortcutRecording()
					return true
				}
				target := a.shortcutRecordingID
				if other := a.commands.conflict(target, shortcut); other != "" {
					a.shortcutConflict = "与「" + other + "」的快捷键冲突，换一组再试"
					a.stopShortcutRecording()
					return true
				}
				a.shortcutConflict = ""
				a.stopShortcutRecording()
				a.setCommandBinding(target, shortcut)
				return true
			})
			capture.Children(func() {
				display.Text(c, label, display.TextProps{
					Strong: true, FontSize: 12, Rows: 1,
				})
			})
		}

		for _, binding := range a.commands.bindings() {
			def := binding.Def
			row := ui.Row(c).FillWidth().Padding(5, 2).Gap(8).AlignItems(ui.Center)
			row.Label("快捷键 " + def.Title)
			row.Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
					display.Text(c, def.Title, display.TextProps{Strong: true, FontSize: 12, Rows: 1})
					display.Text(c, def.Hint, display.TextProps{
						Type: display.TextSecondary, FontSize: 10, Rows: 1,
					})
				})
				shown := binding.Label
				if shown == "" {
					shown = "未设置"
				}
				display.Text(c, shown, display.TextProps{FontSize: 11, Rows: 1}).Width(80)
				if action.Button(c, "录制", action.ButtonProps{Size: lobetheme.Small}).
					Label("录制 " + def.Title).Clicked() {
					a.startShortcutRecording(def.ID)
				}
				if binding.Label != "" {
					if action.Button(c, "清除", action.ButtonProps{Size: lobetheme.Small}).
						Label("清除 " + def.Title).Clicked() {
						a.clearCommandBinding(def.ID)
					}
				}
				if action.Button(c, "默认", action.ButtonProps{Size: lobetheme.Small}).
					Label("恢复 " + def.Title).Clicked() {
					a.resetCommandBinding(def.ID)
				}
			})
		}

		ui.Row(c).FillWidth().Padding(4, 0).Gap(8).AlignItems(ui.Center).Children(func() {
			if action.Button(c, "全部恢复默认", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.resetAllCommandBindings()
			}
			ui.Spacer(c).Grow(1)
		})
	})
}

// commandShortcutFromEvent 把捕获到的原始按键变成可保存的组合键。只允许带
// 主修饰键 / Ctrl / Alt / Super 的组合，Shift 单独的字母键会照常打字，不该被
// 这里抢走。
func commandShortcutFromEvent(ev ui.InputEvent) (commandShortcut, bool) {
	if ev.Key == 0 || ev.Key == ui.KeyUnknown {
		return commandShortcut{}, false
	}
	if ev.Mods&(ui.Cmd|ui.Ctrl|ui.Alt|ui.Super) == 0 {
		return commandShortcut{}, false
	}
	raw := (commandShortcut{Mods: ev.Mods, Key: ev.Key}).canonical()
	return parseCommandShortcut(raw)
}

func (a *app) startShortcutRecording(id string) {
	a.shortcutConflict = ""
	a.shortcutRecordingID = id
	a.shortcutCaptureFocus = 1
}

func (a *app) stopShortcutRecording() {
	a.shortcutRecordingID = ""
	a.shortcutCaptureFocus = 0
}

func (a *app) setCommandBinding(id string, s commandShortcut) {
	raw := ""
	if s.valid() {
		raw = s.canonical()
	}
	if a.commands == nil || a.commands.prefs == nil {
		return
	}
	if err := a.commands.prefs.setBinding(id, raw); err != nil {
		a.toast(toastSettings, err.Error())
	}
	a.publish(func() {})
}

func (a *app) clearCommandBinding(id string) {
	a.setCommandBinding(id, commandShortcut{})
}

func (a *app) resetCommandBinding(id string) {
	if a.commands == nil || a.commands.prefs == nil {
		return
	}
	if err := a.commands.prefs.resetBinding(id); err != nil {
		a.toast(toastSettings, err.Error())
	}
	a.publish(func() {})
}

func (a *app) resetAllCommandBindings() {
	if a.commands == nil || a.commands.prefs == nil {
		return
	}
	if err := a.commands.prefs.resetAllBindings(); err != nil {
		a.toast(toastSettings, err.Error())
	}
	a.publish(func() {})
}
