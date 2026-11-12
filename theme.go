package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
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

// homeTheme 返回全应用共用的主题：浅色是纯白背景（页面、侧边栏与自绘
// 标题栏均白）+ 指定边框色 oklab(0.21 0.00164224 -0.00577088 / 0.1)
// （即 oklch(0.21 0.006 285.885) 的 10% 不透明版本）；深色沿用 mygo 的
// 深色主题，只把背景、表面与描边调成同一套灰阶，让标题栏和侧边栏与页面
// 同色，不出现一条更亮的横带。其余文字/强调色沿用浅色或深色主题。
func homeTheme(dark bool) *ui.Theme {
	if dark {
		th := ui.DarkTheme()
		th.Background = ui.Hex("#18181b")
		th.Surface = ui.Hex("#18181b")
		th.Border = ui.Oklch(0.92, 0.006, 285.885).Alpha(0.14)
		return th
	}
	th := ui.LightTheme()
	th.Background = ui.Hex("#ffffff")
	th.Surface = ui.Hex("#ffffff")
	th.Border = ui.Oklch(0.21, 0.006, 285.885).Alpha(0.1)
	return th
}
