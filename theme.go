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

// Reset 恢复默认外观（跟随系统）并落盘。
func (m *themeManager) Reset() error { return m.Save(themeFollowSystem) }

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
// 颜色方面只覆写品牌相关 token：两套 Primary 族换成应用品牌蓝色（浅深各一
// 支），文本选中高亮改为品牌色的半透明版本；底色等其余 token 全部沿用 Lobe
// 默认，浅色不强制纯白，让分段控件等组件的选中态按 Lobe 自己的配色显示。
var lobeCfg = func() *lobetheme.Config {
	light, dark := *lobetheme.Light(), *lobetheme.Dark()
	// 应用品牌强调色：浅色 #1D4ED8、深色 #60A5FA，hover / active 在各自
	// 色阶上依次加深一档。Lobe 默认 Primary 是中性灰，覆写成品牌色后，
	// Lobe 的 Primary 按钮/选中态与自绘 t.Accent 落在同一颜色上。
	light.Primary, light.PrimaryHover, light.PrimaryActive = ui.Hex("#1D4ED8"), ui.Hex("#1E40AF"), ui.Hex("#1E3A8A")
	light.PrimaryForeground = ui.Hex("#ffffff")
	// Lobe 的 PrimaryFill* 默认是黑色半透明，和覆写后的品牌蓝不是一族：
	// 自己的回复、定位行铺出来的淡底会发灰。按 Lobe 自己的透明度层级把
	// 这一族换成品牌蓝，浅色下强调色才真正统一（深色沿用默认，不动）。
	light.PrimaryFill = light.Primary.Alpha(0.36)
	light.PrimaryFillSecondary = light.Primary.Alpha(0.22)
	light.PrimaryFillTertiary = light.Primary.Alpha(0.16)
	light.PrimaryFillQuaternary = light.Primary.Alpha(0.08)
	dark.Primary, dark.PrimaryHover, dark.PrimaryActive = ui.Hex("#60A5FA"), ui.Hex("#3B82F6"), ui.Hex("#2563EB")
	dark.PrimaryForeground = ui.Hex(darkInkBackground)

	cfg := lobetheme.NewConfig(light, dark)
	// Lobe 默认把选中高亮设成亮黄（Yellow9），和蓝色品牌不搭，在浅色底上也
	// 偏淡。改成品牌强调色的半透明版本：选中处的正文仍然清楚，深浅两套
	// 外观各用自己那支蓝。在这里改派生主题本身而不是复制一份，主题指针
	// 不变，TokensOf(c) 才能继续命中同一套 token。
	cfg.Theme(false).Selection = cfg.Theme(false).Accent.Alpha(0.25)
	cfg.Theme(true).Selection = cfg.Theme(true).Accent.Alpha(0.32)
	// 字体是进程级外观：两个主题实例都被 homeTheme 直接复用，设置一次就
	// 作用于所有窗口的文字（含 Markdown）。macOS 留空，走系统苹方。
	if usesHarmonySans() {
		cfg.Theme(false).Font = appFontFamily
		cfg.Theme(true).Font = appFontFamily
	}
	return cfg
}()

// hexToken 把不透明的 Lobe token 转成 mygo 窗口选项接受的 #rrggbb 字符串。
func hexToken(c ui.Color) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// lobeWindowBackground 是原生窗口在自绘之前铺的底色，直接取自 Lobe 的
// BgContainer。附属窗口不再另写一套深灰，深浅外观都和自绘页面同底。
var lobeWindowBackground = "light-dark(" +
	hexToken(lobeCfg.Tokens(false).BgContainer) + ", " +
	hexToken(lobeCfg.Tokens(true).BgContainer) + ")"

// lobeStageBackground 是图片预览窗口与视频卡片共用的深色舞台，取自锁定
// 深色主题后的 Lobe BgContainer。
var lobeStageBackground = lobeCfg.Tokens(true).BgContainer

// homeTheme 返回当前帧的 Lobe 主题。深浅由外观模式决定，主题实例直接取自
// lobeCfg，这样 Lobe 组件能用 TokensOf(c) 取到匹配的深浅 token。
func homeTheme(dark bool) *ui.Theme {
	return lobeCfg.Theme(dark)
}
