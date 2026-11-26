package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 设置窗口「快捷键」页里的老板键一块。录制复用命令快捷键的捕获框：焦点交给
// 只在这一帧存在的输入框，由 HandleInput 收下原始按键（OnShortcut 只能收到
// 已声明的组合），再换成 mygo 系统热键能用的 accelerator 写法。

func (a *app) bossKeySettingsSection(c *ui.Context) {
	if a.bossKeyPrefs == nil {
		return
	}
	tk := lobetheme.TokensOf(c)
	settingsSection(c, "老板键", func() {
		settingsDescription(c, "系统级热键：按下立即隐藏所有应用窗口，再按一次恢复；"+
			"即使应用不在前台也能触发。隐藏期间桌面通知也一并屏蔽，消息与未读数照常更新；"+
			"关闭后不占用系统热键。", 3)

		on := a.bossKeyPrefs.on()
		if v := settingsCheckbox(c, "启用老板键", on); v != on {
			a.stopBossKeyRecording()
			a.setBossKeyEnabled(v)
		}

		if a.bossKeyConflict != "" {
			row := ui.Row(c).FillWidth().Padding(6, 8).Radius(tk.BorderRadiusSM).
				Background(tk.ErrorBg).AlignItems(ui.Center)
			row.Label("老板键冲突提示")
			row.Children(func() {
				display.Text(c, a.bossKeyConflict, display.TextProps{
					Type: display.TextDanger, FontSize: 11, Rows: 2,
				})
			})
		}

		if a.bossKeyRecording {
			label := "请按下组合键… Esc 取消"
			capture := ui.Box(c).FillWidth().Height(34).Radius(tk.BorderRadiusSM).
				Background(tk.PrimaryBg).Center().
				Focusable().Bind(&a.bossKeyCapture).FocusBind(&a.bossKeyCaptureFocus, 1)
			capture.Label("老板键录制框")
			capture.HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind != ui.InputKeyDown || ev.Repeat {
					return true
				}
				if ev.Key == ui.KeyEscape {
					a.stopBossKeyRecording()
					return true
				}
				// 修饰键自己按下时 Key 是 KeyUnknown，等用户按下真正的主键。
				if ev.Key == ui.KeyUnknown || ev.Key == 0 {
					return true
				}
				shortcut, ok := commandShortcutFromEvent(ev)
				if !ok {
					a.bossKeyConflict = "组合键要包含 Ctrl / Alt / ⌘ 中的一个修饰键"
					a.stopBossKeyRecording()
					return true
				}
				acc, ok := bossKeyAcceleratorFromShortcut(shortcut)
				if !ok {
					a.bossKeyConflict = "这个按键不能用作系统热键，换一个再试"
					a.stopBossKeyRecording()
					return true
				}
				if a.commands != nil {
					if other := a.commands.conflict("", shortcut); other != "" {
						a.bossKeyConflict = "与「" + other + "」的快捷键冲突，换一组再试"
						a.stopBossKeyRecording()
						return true
					}
				}
				a.bossKeyConflict = ""
				a.stopBossKeyRecording()
				a.setBossKeyAccelerator(acc)
				return true
			})
			capture.Children(func() {
				display.Text(c, label, display.TextProps{
					Strong: true, FontSize: 12, Rows: 1,
				})
			})
			return
		}

		shown := bossKeyAccLabel(a.bossKeyPrefs.accelerator())
		if shown == "" {
			shown = "未设置"
		}
		ui.Row(c).FillWidth().Padding(5, 2).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
				display.Text(c, "隐藏 / 显示热键", display.TextProps{Strong: true, FontSize: 12, Rows: 1})
				display.Text(c, "录制的按键对整台电脑生效", display.TextProps{
					Type: display.TextSecondary, FontSize: 10, Rows: 1,
				})
			})
			display.Text(c, shown, display.TextProps{FontSize: 11, Rows: 1}).Width(110)
			if action.Button(c, "录制", action.ButtonProps{Size: lobetheme.Small}).
				Label("录制老板键").Clicked() {
				a.startBossKeyRecording()
			}
			if action.Button(c, "默认", action.ButtonProps{Size: lobetheme.Small}).
				Label("恢复老板键默认").Clicked() {
				a.bossKeyConflict = ""
				a.setBossKeyAccelerator(bossKeyDefaultAcc)
			}
		})
	})
}

func (a *app) startBossKeyRecording() {
	a.bossKeyConflict = ""
	a.bossKeyRecording = true
	a.bossKeyCaptureFocus = 1
}

func (a *app) stopBossKeyRecording() {
	a.bossKeyRecording = false
	a.bossKeyCaptureFocus = 0
}
