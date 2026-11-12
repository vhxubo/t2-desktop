package main

import (
	"github.com/egoist/mygo/ui"
)

// 三种外观模式的图标：显示器表示跟随系统，太阳表示浅色，月亮表示深色。
// 都是 24×24、stroke=currentColor 的线性图标，和左侧导航栏同一套画法。
var (
	systemThemeIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/></svg>`))
	lightThemeIcon  = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/></svg>`))
	darkThemeIcon   = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20.5 14.5A8.5 8.5 0 0 1 9.5 3.5a8.5 8.5 0 1 0 11 11z"/></svg>`))
)

// themeButtons 在标题栏直接摆出三档外观：跟随系统、浅色、深色。外观只有
// 三种，点一下就切换，没必要再开一层弹窗；当前档用强调色标出，其余档悬停
// 变亮，一眼能看出现在用的是哪种。
func (a *app) themeButtons(c *ui.Context) {
	ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
		a.themeButton(c, themeFollowSystem, systemThemeIcon, "跟随系统")
		a.themeButton(c, themeLight, lightThemeIcon, "浅色")
		a.themeButton(c, themeDark, darkThemeIcon, "深色")
	})
}

// themeButton 画一个外观图标按钮。图标本身是装饰，可访问名由 Label 提供，
// 读屏软件读到的是「外观：深色」这样的完整选项名。
func (a *app) themeButton(c *ui.Context, mode int, icon *ui.SVG, label string) {
	t := c.Theme()
	selected := a.theme.mode == mode
	b := ui.Box(c).Padding(4, 6).Radius(6).Center().Background(ui.Transparent)
	if selected || b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	ink := t.TextMuted
	switch {
	case selected:
		ink = t.Accent
	case b.Hovered():
		ink = t.Text
	}
	b.Children(func() {
		ui.Icon(c, icon).Size(16, 16).TextColor(ink)
	})
	tip := "外观：" + label
	if selected {
		tip += "（当前）"
	}
	b.Label("外观：" + label).Tooltip(tip).OnClick(func() { a.setThemeMode(c, mode) })
}

// setThemeMode 切换外观并落盘。写失败只影响下次启动的默认外观，本次运行
// 仍按新选择显示，所以不回滚，用 Toast 说明原因而不是静默丢掉配置。
func (a *app) setThemeMode(c *ui.Context, mode int) {
	if a.theme.mode == mode {
		return
	}
	err := a.theme.Save(mode)
	a.syncNativeAppearance()
	if err != nil {
		c.Toast(err.Error())
	}
}
