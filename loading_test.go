package main

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
)

// lobeSpin 必须让转圈真正落在居中容器的正中：feedback.Spin 自带
// AlignSelf(Start)，不覆盖的话转圈会被钉在交叉轴起点（实测 X=0）。
func TestLobeSpinCentered(t *testing.T) {
	const w, h = 400, 300

	t.Run("column", func(t *testing.T) {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Fill().Center().Children(func() {
				lobeSpin(c, feedback.SpinProps{})
			})
		}, w, h)
		tt.Frame()
		assertSpinCenter(t, tt, w/2, h/2)
	})

	t.Run("row", func(t *testing.T) {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Row(c).Fill().Center().Children(func() {
				lobeSpin(c, feedback.SpinProps{})
			})
		}, w, h)
		tt.Frame()
		assertSpinCenter(t, tt, w/2, h/2)
	})
}

func assertSpinCenter(t *testing.T, tt *ui.Tester, wantX, wantY float32) {
	t.Helper()
	info, ok := tt.AccessByRole(ui.RoleStatus, "")
	if !ok {
		t.Fatal("未找到 Lobe 转圈（RoleStatus）")
	}
	gotX := info.Bounds.X + info.Bounds.W/2
	gotY := info.Bounds.Y + info.Bounds.H/2
	if math.Abs(float64(gotX-wantX)) > 1 || math.Abs(float64(gotY-wantY)) > 1 {
		t.Fatalf("转圈未居中：中心 (%.1f, %.1f)，期望 (%.1f, %.1f)",
			gotX, gotY, wantX, wantY)
	}
}
