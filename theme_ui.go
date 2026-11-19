package main

import (
	"github.com/egoist/mygo/ui"
)

// setThemeMode 切换外观并落盘。写失败只影响下次启动的默认外观，本次运行
// 仍按新选择显示，所以不回滚，用 Toast 说明原因而不是静默丢掉配置。
func (a *app) setThemeMode(c *ui.Context, mode int) {
	if a.theme.mode == mode {
		return
	}
	err := a.theme.Save(mode)
	a.syncNativeAppearance()
	if err != nil && c != nil {
		c.Toast(err.Error())
	}
}
