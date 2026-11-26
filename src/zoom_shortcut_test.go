package main

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 缩放快捷键挂在 windowView 上：主窗口、设置页、详情窗口等任何一扇窗口里按
// Ctrl+= / Ctrl++ / Ctrl+- / Ctrl+0 都改同一份偏好。之前只注册在主窗口 view，
// 附属窗口里按完全没反应。
func TestZoomShortcutsInEveryWindow(t *testing.T) {
	a := testApp(t)
	views := map[string]func(*ui.Context){
		"主窗口": a.windowView(a.view),
		"附属窗口": a.windowView(func(c *ui.Context) {
			ui.Text(c, "附属窗口")
		}),
	}
	for name, view := range views {
		t.Run(name, func(t *testing.T) {
			a.zoomPrefs.Reset()
			tt := ui.NewTester(view, 1080, 720)
			tt.Frame()

			for _, tc := range []struct {
				name string
				mods ui.Modifiers
				key  ui.Key
				want float32
			}{
				{"Ctrl+=", ui.Cmd, ui.KeyEqual, appZoomWheelStep},
				{"Ctrl++", ui.Cmd | ui.Shift, ui.KeyEqual, appZoomWheelStep * appZoomWheelStep},
				{"Ctrl+-", ui.Cmd, ui.KeyMinus, appZoomWheelStep},
				{"Ctrl+0", ui.Cmd, ui.Key0, appZoomDefault},
			} {
				tt.Key(tc.mods, tc.key)
				tt.Frame()
				if math.Abs(float64(a.zoom()-tc.want)) > 0.001 {
					t.Errorf("%s 后缩放 = %v，期望 %v", tc.name, a.zoom(), tc.want)
				}
			}
		})
	}
}

// 用户在快捷键设置里改过缩放键位时，以设置为准；辅助的 Ctrl++ 仍然可用。
func TestZoomShortcutUsesCustomBinding(t *testing.T) {
	a := testApp(t)
	if err := a.commands.prefs.setBinding("zoomIn", cmdKey(ui.KeyP).canonical()); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.windowView(a.view), 1080, 720)
	tt.Frame()

	tt.Key(ui.Cmd, ui.KeyP)
	tt.Frame()
	if a.zoom() != appZoomWheelStep {
		t.Errorf("自定义键位没有放大：%v", a.zoom())
	}
	tt.Key(ui.Cmd, ui.KeyEqual)
	tt.Frame()
	if a.zoom() != appZoomWheelStep {
		t.Errorf("改过键位后旧组合不该继续触发：%v", a.zoom())
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyEqual)
	tt.Frame()
	if math.Abs(float64(a.zoom()-appZoomWheelStep*appZoomWheelStep)) > 0.001 {
		t.Errorf("Ctrl++ 辅助键位没有放大：%v", a.zoom())
	}
}
