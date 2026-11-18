package main

import (
	"github.com/egoist/mygo/ui"
)

// themeButtons 把三档外观压成一行等宽小字：悬浮提示写明「外观：跟随系统」
// 这样的完整选项名，读屏与点击测试认的也是这个名字。三档都是纯文字芯片，
// 选中档用强调色，其余档只在中性灰里悬停变亮——侧边栏因此不再被图标占满。
func (a *app) themeButtons(c *ui.Context) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "外观").FontSize(9).TextColor(t.TextMuted).SingleLine()
		a.themeChip(c, themeFollowSystem, "跟随", "跟随系统")
		a.themeChip(c, themeLight, "浅", "浅色")
		a.themeChip(c, themeDark, "深", "深色")
	})
}

// themeChip 画一档外观选项。可见文字只求短（跟随 / 浅 / 深），完整含义放在
// 可访问名与提示里，既省侧边栏的宽度，也不靠图标让人猜。
func (a *app) themeChip(c *ui.Context, mode int, text, name string) {
	t := c.Theme()
	selected := a.theme.mode == mode
	b := ui.ButtonBase(c).Padding(3, 6).Radius(3).Background(ui.Transparent)
	ink := t.TextMuted
	switch {
	case selected:
		ink = t.Accent
	case b.Hovered():
		ink = t.Text
	}
	b.Children(func() {
		ui.Text(c, text).FontSize(10).Font(sidebarFont).TextColor(ink).SingleLine()
	})
	tip := "外观：" + name
	if selected {
		tip += "（当前）"
	}
	b.Label("外观：" + name).Tooltip(tip).OnClick(func() { a.setThemeMode(c, mode) })
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
