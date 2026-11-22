//go:build windows

package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 内置字体注册了常规(400)与加粗(700)两档，界面用 FontWeight(700) 时必须
// 真的比常规更粗：直接比较同一段中文在两种字重下的墨迹量，防止只嵌入
// Regular 或 Bold 注册失败后回退到同一份字形。
func TestHarmonySansBoldRenders(t *testing.T) {
	if !usesHarmonySans() {
		t.Skip("macOS 用系统苹方")
	}
	ink := func(weight int) int {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Text(c, "常规加粗").Font(appFontFamily).FontSize(40).FontWeight(weight).
				TextColor(ui.RGB(0, 0, 0))
		}, 400, 160)
		img := tt.Image()
		n := 0
		for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
			for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
				r, g, b, a := img.RGBAAt(x, y).RGBA()
				if a > 0 && r < 0x8000 && g < 0x8000 && b < 0x8000 {
					n++
				}
			}
		}
		return n
	}
	regular, bold := ink(400), ink(700)
	if regular == 0 {
		t.Fatal("没有渲染出字形")
	}
	if bold <= regular {
		t.Fatalf("加粗没有比常规更粗：400=%d 700=%d", regular, bold)
	}
}
