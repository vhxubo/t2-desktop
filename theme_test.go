package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
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

// 标题栏直接摆三个外观图标，点击即切换并落盘，全程没有弹窗。测试从深色
// 系统开始，这样「浅色」按钮是否真的覆盖系统外观也能一起验证。
func TestThemeButtonsSwitchAndPersist(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false
	tt := ui.NewTesterWith(a.view, 1100, 760, ui.TesterOptions{Dark: true})
	tt.Frame()

	for _, label := range []string{"外观：跟随系统", "外观：浅色", "外观：深色"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("标题栏缺少 %q：%q", label, tt.Texts())
		}
	}

	// 深色系统下点「浅色」：立刻固定浅色，配置写成 light。
	if err := tt.Click("外观：浅色"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.theme.mode != themeLight {
		t.Fatalf("点「浅色」后模式 = %d，期望浅色", a.theme.mode)
	}
	if got := pageLum(t, tt); got < 240 {
		t.Errorf("固定浅色后页面亮度 %.0f，期望浅色主题", got)
	}
	var cfg themeConfig
	if err := readJSONFile(filepath.Join(dir, "theme.json"), &cfg); err != nil {
		t.Fatalf("切换后没有写入 theme.json：%v", err)
	}
	if cfg.Mode != "light" {
		t.Errorf("theme.json 模式 = %q，期望 light", cfg.Mode)
	}

	// 点「深色」：图标按钮直接换主题，配置跟着改。
	if err := tt.Click("外观：深色"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.theme.mode != themeDark {
		t.Fatalf("点「深色」后模式 = %d，期望深色", a.theme.mode)
	}
	if got := pageLum(t, tt); got > 80 {
		t.Errorf("固定深色后页面亮度 %.0f，期望深色主题", got)
	}
	if err := readJSONFile(filepath.Join(dir, "theme.json"), &cfg); err != nil {
		t.Fatalf("再次切换后没有写入 theme.json：%v", err)
	}
	if cfg.Mode != "dark" {
		t.Errorf("theme.json 模式 = %q，期望 dark", cfg.Mode)
	}

	// 点「跟随系统」：系统仍是深色，页面保持深色，配置回到 system。
	if err := tt.Click("外观：跟随系统"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.theme.mode != themeFollowSystem {
		t.Fatalf("点「跟随系统」后模式 = %d，期望跟随系统", a.theme.mode)
	}
	if got := pageLum(t, tt); got > 80 {
		t.Errorf("跟随深色系统时页面亮度 %.0f，期望深色主题", got)
	}
	if err := readJSONFile(filepath.Join(dir, "theme.json"), &cfg); err != nil {
		t.Fatalf("切回跟随后没有写入 theme.json：%v", err)
	}
	if cfg.Mode != "system" {
		t.Errorf("theme.json 模式 = %q，期望 system", cfg.Mode)
	}
}
