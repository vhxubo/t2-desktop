package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestPopupRowHasNoRoundedBorder 锁住弹窗窗口里列表行的平面视觉：行的左缘
// 与页面背景同色，旧的圆角描边卡片会在这里画一圈 1px 深色边框。用无头渲染
// 的真实像素断言，避免以后又被改回卡片。
func TestPopupRowHasNoRoundedBorder(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		c.Root().Background(ui.Hex("#ffffff"))
		ui.Column(c).FillWidth().Padding(10, 10).Children(func() {
			popupRow(c, true).FillWidth().Padding(4, 8).
				Label("平面列表行").Children(func() {
				ui.Text(c, "平面列表行").FontSize(12)
			})
		})
	}, 300, 120)
	tt.Frame()

	row, ok := tt.Find("平面列表行")
	if !ok {
		t.Fatalf("没找到平面列表行：%q", tt.Texts())
	}
	im := tt.Image()
	midY := int(row.Y + row.H/2)
	bg := im.RGBAAt(int(row.X)-2, midY)
	for dx := 0; dx <= 3; dx++ {
		got := im.RGBAAt(int(row.X)+dx, midY)
		if d := rgbDistance(got, bg); d > 12 {
			t.Errorf("列表行左缘 dx=%d 与背景不一致：行内 %v / 背景 %v", dx, got, bg)
		}
	}
}
