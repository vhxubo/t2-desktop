package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// pageLum 取列表页空白处的亮度：x 在侧边栏右侧、y 在标题栏下方，避开设
// 置面板与列表内容，用来判断整页是浅色还是深色。
func pageLum(t *testing.T, tt *ui.Tester) float64 {
	t.Helper()
	return lum(tt.Image(), 400, 200)
}

// 默认「跟随系统」：系统深浅色变化后，下一次建树就换成对应主题，无需重启。
func TestThemeFollowsSystemByDefault(t *testing.T) {
	a := testApp(t)
	if a.theme.Mode() != themeFollowSystem {
		t.Fatalf("新配置的默认模式 = %d，期望跟随系统", a.theme.Mode())
	}
	tt := ui.NewTesterWith(a.view, 1080, 720, ui.TesterOptions{Dark: true})
	tt.Frame()
	if got := pageLum(t, tt); got > 80 {
		t.Errorf("系统为深色时页面亮度 %.0f，期望深色主题", got)
	}

	tt.SetDark(false)
	tt.Frame()
	if got := pageLum(t, tt); got < 240 {
		t.Errorf("系统切回浅色后页面亮度 %.0f，期望浅色主题", got)
	}

	tt.SetDark(true)
	tt.Frame()
	if got := pageLum(t, tt); got > 80 {
		t.Errorf("系统再切深色后页面亮度 %.0f，期望深色主题", got)
	}
}

// 固定浅色 / 深色时忽略系统外观：用户手动选定的模式不能被系统设置覆盖。
func TestThemeForcedModeIgnoresSystem(t *testing.T) {
	a := testApp(t)
	if err := a.theme.Save(themeLight); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTesterWith(a.view, 1080, 720, ui.TesterOptions{Dark: true})
	tt.Frame()
	if got := pageLum(t, tt); got < 240 {
		t.Errorf("固定浅色时深色系统下页面亮度 %.0f，期望仍为浅色", got)
	}

	if err := a.theme.Save(themeDark); err != nil {
		t.Fatal(err)
	}
	tt.SetDark(false)
	tt.Frame()
	if got := pageLum(t, tt); got > 80 {
		t.Errorf("固定深色时浅色系统下页面亮度 %.0f，期望仍为深色", got)
	}
}

// 外观模式写进 theme.json，下次启动继续沿用；无法识别的值退回跟随系统，
// 不让坏配置挡住应用启动。
func TestThemeModePersists(t *testing.T) {
	dir := t.TempDir()
	m := newThemeManager(dir)
	if m.Mode() != themeFollowSystem {
		t.Fatalf("缺省模式 = %d，期望跟随系统", m.Mode())
	}
	if err := m.Save(themeDark); err != nil {
		t.Fatal(err)
	}

	var cfg themeConfig
	if err := readJSONFile(filepath.Join(dir, "theme.json"), &cfg); err != nil {
		t.Fatalf("theme.json 没有写出来：%v", err)
	}
	if cfg.Mode != "dark" {
		t.Errorf("保存的模式 = %q，期望 %q", cfg.Mode, "dark")
	}
	if got := newThemeManager(dir).Mode(); got != themeDark {
		t.Errorf("重启后模式 = %d，期望深色", got)
	}

	raw := []byte(`{"mode":"purple"}`)
	if err := os.WriteFile(filepath.Join(dir, "theme.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := newThemeManager(dir).Mode(); got != themeFollowSystem {
		t.Errorf("无法识别的模式 = %d，期望退回跟随系统", got)
	}
}

// 文本选中高亮跟随品牌蓝，不再是 Lobe 默认的亮黄；半透明保证选中处的正文
// 仍可读。除了断言主题色，也在无头窗口里拖选一段文字核对实际绘制的像素。
func TestTextSelectionUsesBrandColor(t *testing.T) {
	const text = "这是一段可以拖选的文字"
	for _, tc := range []struct {
		name  string
		dark  bool
		alpha float32
	}{
		{"浅色", false, 0.25},
		{"深色", true, 0.32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th := homeTheme(tc.dark)
			want := th.Accent.Alpha(tc.alpha)
			if th.Selection != want {
				t.Errorf("选中高亮 = %v，期望品牌色的半透明版本 %v", th.Selection, want)
			}
			if th.Selection == lobeCfg.Tokens(tc.dark).Yellow9 {
				t.Error("选中高亮还是 Lobe 默认的亮黄")
			}

			tt := ui.NewTesterWith(func(c *ui.Context) {
				c.SetTheme(th)
				ui.Column(c).Selectable().Padding(12).Children(func() {
					ui.Text(c, text)
				})
			}, 320, 80, ui.TesterOptions{Dark: tc.dark})
			tt.Frame()
			r, ok := tt.Find(text)
			if !ok {
				t.Fatalf("找不到正文：%q", tt.Texts())
			}
			// 蓝色高亮：蓝通道明显高于红、绿通道。黄色高亮不会命中这里。
			bluish := func() int {
				img, n := tt.Image(), 0
				for y := int(r.Y); y < int(r.Y+r.H); y++ {
					for x := int(r.X); x < int(r.X+r.W); x++ {
						p := img.RGBAAt(x, y)
						if int(p.B) > int(p.R)+20 && int(p.B) > int(p.G)+5 {
							n++
						}
					}
				}
				return n
			}
			before := bluish()
			y := r.Y + r.H/2
			tt.Press(r.X+2, y)
			tt.Move(r.X+r.W-2, y)
			tt.Release(r.X+r.W-2, y)
			tt.Frame()
			if after := bluish(); after <= before {
				t.Errorf("拖选后没有画出蓝色高亮：之前 %d，之后 %d", before, after)
			}
		})
	}
}

// 自己的回复与定位行用的 PrimaryFill* 要和品牌蓝同族：Lobe 默认是黑色
// 半透明，浅色下铺出发灰的淡底。深色维持 Lobe 默认，不受这次调整影响。
func TestLightPrimaryFillUsesBrandColor(t *testing.T) {
	light := lobeCfg.Tokens(false)
	for _, tc := range []struct {
		name  string
		got   ui.Color
		alpha float32
	}{
		{"PrimaryFill", light.PrimaryFill, 0.36},
		{"PrimaryFillSecondary", light.PrimaryFillSecondary, 0.22},
		{"PrimaryFillTertiary", light.PrimaryFillTertiary, 0.16},
		{"PrimaryFillQuaternary", light.PrimaryFillQuaternary, 0.08},
	} {
		if want := light.Primary.Alpha(tc.alpha); tc.got != want {
			t.Errorf("浅色 %s = %v，期望品牌蓝的 %.0f%% 版本 %v", tc.name, tc.got, tc.alpha*100, want)
		}
	}
	dark := lobeCfg.Tokens(true)
	if got, want := dark.PrimaryFillQuaternary, lobetheme.Dark().PrimaryFillQuaternary; got != want {
		t.Errorf("深色 PrimaryFillQuaternary = %v，期望保持 Lobe 默认 %v", got, want)
	}
}

// 原生窗口底色与深色舞台都从 Lobe BgContainer 派生，附属窗口和视频卡片
// 不再各写一套灰底 / 近黑。
func TestCustomSurfacesFollowLobeTokens(t *testing.T) {
	light, dark := lobeCfg.Tokens(false), lobeCfg.Tokens(true)
	want := "light-dark(" + hexToken(light.BgContainer) + ", " + hexToken(dark.BgContainer) + ")"
	if lobeWindowBackground != want {
		t.Errorf("窗口底色 = %q，期望 Lobe BgContainer 派生值 %q", lobeWindowBackground, want)
	}
	if lobeStageBackground != dark.BgContainer {
		t.Errorf("深色舞台底色 = %v，期望 Lobe 深色 BgContainer %v", lobeStageBackground, dark.BgContainer)
	}
	if videoCardBG != lobeStageBackground {
		t.Errorf("视频卡片底色 = %v，期望与深色舞台一致 %v", videoCardBG, lobeStageBackground)
	}
}

// 外观三档收进设置窗口的「外观」页，侧边栏不再单独摆三个按钮。点选即切换
// 并落盘，结果用窗口内 toast 说明。测试从深色系统开始，这样选「浅色」是否
// 真的覆盖系统外观也能一起验证。
func TestThemeChoiceInSettingsSwitchesAndPersists(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false
	a.settingsTab = settingsTabGeneral
	st := ui.NewTesterWith(a.settingsWindowView, settingsWindowWidth, settingsWindowHeight,
		ui.TesterOptions{Dark: true})
	st.Frame()

	for _, label := range []string{"跟随系统", "浅色", "深色"} {
		if _, ok := st.Find(label); !ok {
			t.Fatalf("设置窗口外观页缺少 %q：%q", label, st.Texts())
		}
	}

	// 深色系统下选「浅色」：立刻固定浅色，配置写成 light。
	if err := st.Click("浅色"); err != nil {
		t.Fatal(err)
	}
	if a.theme.mode != themeLight {
		t.Fatalf("选「浅色」后模式 = %d，期望浅色", a.theme.mode)
	}
	var cfg themeConfig
	if err := readJSONFile(filepath.Join(dir, "theme.json"), &cfg); err != nil {
		t.Fatalf("切换后没有写入 theme.json：%v", err)
	}
	if cfg.Mode != "light" {
		t.Errorf("theme.json 模式 = %q，期望 light", cfg.Mode)
	}
	st.Frame()
	if !st.HasText("外观：浅色") {
		t.Errorf("切换后没有弹出 toast：%q", st.Texts())
	}

	// 选「深色」：直接换主题，配置跟着改。
	if err := st.Click("深色"); err != nil {
		t.Fatal(err)
	}
	if a.theme.mode != themeDark {
		t.Fatalf("选「深色」后模式 = %d，期望深色", a.theme.mode)
	}
	if err := readJSONFile(filepath.Join(dir, "theme.json"), &cfg); err != nil {
		t.Fatalf("再次切换后没有写入 theme.json：%v", err)
	}
	if cfg.Mode != "dark" {
		t.Errorf("theme.json 模式 = %q，期望 dark", cfg.Mode)
	}

	// 选「跟随系统」：配置回到 system，系统仍是深色。
	if err := st.Click("跟随系统"); err != nil {
		t.Fatal(err)
	}
	if a.theme.mode != themeFollowSystem {
		t.Fatalf("选「跟随系统」后模式 = %d，期望跟随系统", a.theme.mode)
	}
	if err := readJSONFile(filepath.Join(dir, "theme.json"), &cfg); err != nil {
		t.Fatalf("切回跟随后没有写入 theme.json：%v", err)
	}
	if cfg.Mode != "system" {
		t.Errorf("theme.json 模式 = %q，期望 system", cfg.Mode)
	}
}
