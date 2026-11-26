package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 浅色主题沿用 Lobe 默认底色：BgLayout 是浅灰，选中的白色药丸因此能从轨道
// 上分出来。不能为了「纯白页面」再把它覆写成白色。
func TestLightThemeKeepsLobeDefaultBgLayout(t *testing.T) {
	if got, want := lobeCfg.Tokens(false).BgLayout, lobetheme.Light().BgLayout; got != want {
		t.Errorf("浅色 BgLayout = %v，期望 Lobe 默认 %v", got, want)
	}
}

// 分段控件的选中态按 Lobe 默认画法就有区分度：白色药丸落在浅灰轨道上，不
// 需要额外染成品牌色。
func TestSegmentedSelectedPillDiffersFromTrackInLightTheme(t *testing.T) {
	selected := 0
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(homeTheme(false))
		ui.Column(c).Children(func() {
			action.Segmented(c, &selected, []string{"甲选项", "乙选项"}, action.SegmentedProps{})
		})
	}, 400, 120)
	tt.Frame()

	first, ok := tt.AccessByRole(ui.RoleRadio, "甲选项")
	if !ok {
		t.Fatalf("找不到选中分段：%q", tt.Texts())
	}
	second, ok := tt.AccessByRole(ui.RoleRadio, "乙选项")
	if !ok {
		t.Fatalf("找不到未选中分段：%q", tt.Texts())
	}
	img := tt.Image()
	on := img.RGBAAt(int(first.Bounds.X+3), int(first.Bounds.Y+first.Bounds.H/2))
	track := lobeCfg.Tokens(false).BgLayout
	gap := img.RGBAAt(int((first.Bounds.X+first.Bounds.W+second.Bounds.X)/2),
		int(first.Bounds.Y+first.Bounds.H/2))
	if on == gap {
		t.Fatalf("选中药丸与轨道同色：%v", on)
	}
	if on.R != 255 || on.G != 255 || on.B != 255 {
		t.Errorf("选中药丸该是 Lobe 的 BgElevated 白色，得到 %v", on)
	}
	if gap.R != track.R || gap.G != track.G || gap.B != track.B {
		t.Errorf("轨道该是 Lobe 默认 BgLayout %v，得到 %v", track, gap)
	}
}
