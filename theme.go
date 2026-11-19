package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 外观模式：默认跟随系统深浅色，也可以固定为浅色或深色。三者共用同一套
// 布局，只换主题颜色，所以网页式的内容渲染、标题栏和侧边栏会一起切换。
const (
	themeFollowSystem = iota
	themeLight
	themeDark
)

// themeConfig 是持久化到配置目录 theme.json 的内容。用字符串而不是数字，
// 是为了让配置文件可读、可手改；无法识别的值按「跟随系统」处理。
type themeConfig struct {
	Mode string `json:"mode"`
}

// themeManager 保存外观偏好，并把模式解析成当前帧的主题。只在 UI 线程
// 读写：切换时写入 theme.json，下一帧 view 据此建树。
type themeManager struct {
	dir  string
	mode int
}

func newThemeManager(dir string) *themeManager {
	if dir == "" {
		dir = configDir()
	}
	m := &themeManager{dir: dir, mode: themeFollowSystem}
	var cfg themeConfig
	if err := readJSONFile(filepath.Join(dir, "theme.json"), &cfg); err == nil {
		m.mode = parseThemeMode(cfg.Mode)
	}
	return m
}

// parseThemeMode 把配置文件里的值转成模式序号；空值、旧版本残留和拼错的
// 值都退回「跟随系统」，不让外观配置挡住应用启动。
func parseThemeMode(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "light":
		return themeLight
	case "dark":
		return themeDark
	default:
		return themeFollowSystem
	}
}

func (m *themeManager) Mode() int { return m.mode }

// modeName 是写进 theme.json 的值，和 parseThemeMode 一一对应。
func (m *themeManager) modeName() string {
	switch m.mode {
	case themeLight:
		return "light"
	case themeDark:
		return "dark"
	default:
		return "system"
	}
}

// label 是给界面看的中文名称。
func (m *themeManager) label() string {
	switch m.mode {
	case themeLight:
		return "浅色"
	case themeDark:
		return "深色"
	default:
		return "跟随系统"
	}
}

// Save 切换模式并落盘。先改内存里的模式再写文件：写失败时本次运行仍然
// 采用用户刚选的外观，只把错误交给调用方提示；下次启动会回到旧配置。
func (m *themeManager) Save(mode int) error {
	if mode < themeFollowSystem || mode > themeDark {
		mode = themeFollowSystem
	}
	m.mode = mode
	if err := writeJSONFile(filepath.Join(m.dir, "theme.json"), themeConfig{Mode: m.modeName()}); err != nil {
		return fmt.Errorf("保存外观设置失败：%w", err)
	}
	return nil
}

// theme 返回本帧要用的主题。systemDark 来自 c.Theme().Dark：每帧建树前
// 主题会从系统外观重置，所以这个值就是当前系统的深浅色。
func (m *themeManager) theme(systemDark bool) *ui.Theme {
	dark := systemDark
	switch m.mode {
	case themeLight:
		dark = false
	case themeDark:
		dark = true
	}
	return homeTheme(dark)
}

// applyTheme 在 view 建树前定好本帧主题。必须在取根背景之前调用，否则
// 根背景还是系统主题的底色，会在页面边缘露出一圈不协调的颜色。
func (a *app) applyTheme(c *ui.Context) {
	c.SetTheme(a.theme.theme(c.Theme().Dark))
}

// themeSource 把外观模式映射成 mygo 的原生外观来源。自绘界面由 applyTheme
// 负责；这个来源影响 mygo 自己画的窗口、菜单与更新/通知窗口，让它们不会
// 在固定深色时仍然按浅色系统外观弹出。
func themeSource(mode int) mygo.ThemeSource {
	switch mode {
	case themeLight:
		return mygo.ThemeLight
	case themeDark:
		return mygo.ThemeDark
	default:
		return mygo.ThemeSystem
	}
}

// syncNativeAppearance 同步原生外观。只在真实窗口存在后调用：headless
// 测试没有 App.Run，触碰 mygo 的主循环会等待一个不存在的处理者。
func (a *app) syncNativeAppearance() {
	if a.win == nil {
		return
	}
	mygo.Theme.SetSource(themeSource(a.theme.Mode()))
}

// 品牌强调色：浅色用蓝 #1D4ED8，深色用亮蓝 #60A5FA（见 lobeCfg）。
const (
	darkInkBackground = "#121417"
)

// lobeCfg 是全应用共用的 Lobe UI 主题配置。由它派生的主题会让
// lobeTheme.TokensOf(c) 命中同一套 token，Lobe 组件与自绘部分因此配色一致。
// 只覆写三处：浅色背景保持纯白（白色页面语义），两套 Primary 族换成应用
// 品牌蓝色（浅深各一支），其余 token 全用 Lobe 默认。
var lobeCfg = func() *lobetheme.Config {
	light, dark := *lobetheme.Light(), *lobetheme.Dark()
	light.BgLayout = ui.Hex("#ffffff")

	// 应用品牌强调色：浅色 #1D4ED8、深色 #60A5FA，hover / active 在各自
	// 色阶上依次加深一档。Lobe 默认 Primary 是中性灰，覆写成品牌色后，
	// Lobe 的 Primary 按钮/选中态与自绘 t.Accent 落在同一颜色上。
	light.Primary, light.PrimaryHover, light.PrimaryActive = ui.Hex("#1D4ED8"), ui.Hex("#1E40AF"), ui.Hex("#1E3A8A")
	light.PrimaryForeground = ui.Hex("#ffffff")
	dark.Primary, dark.PrimaryHover, dark.PrimaryActive = ui.Hex("#60A5FA"), ui.Hex("#3B82F6"), ui.Hex("#2563EB")
	dark.PrimaryForeground = ui.Hex(darkInkBackground)
	return lobetheme.NewConfig(light, dark)
}()

// homeTheme 返回当前帧的 Lobe 主题。深浅由外观模式决定，主题实例直接取自
// lobeCfg，这样 Lobe 组件能用 TokensOf(c) 取到匹配的深浅 token。
func homeTheme(dark bool) *ui.Theme {
	return lobeCfg.Theme(dark)
}
