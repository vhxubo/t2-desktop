package main

import (
	"sync"

	"github.com/egoist/mygo/ui"
)

// 「关于」窗口里用的是随包的应用图标本身，也就是任务栏 / 托盘上那枚 T2 图标，
// 浅色 / 深色外观各一份，和 app_icon.go 里原生窗口、托盘用的是同一套资源。
// 解码一次，所有「关于」窗口共用。
var (
	aboutIconOnce  sync.Once
	aboutIconLight ui.ImageSource
	aboutIconDark  ui.ImageSource
)

func aboutIconSource(dark bool) ui.ImageSource {
	aboutIconOnce.Do(func() {
		if src, err := ui.DecodeImage(appIcons.forDark(false)); err == nil {
			aboutIconLight = src
		}
		if src, err := ui.DecodeImage(appIcons.forDark(true)); err == nil {
			aboutIconDark = src
		}
	})
	if dark {
		return aboutIconDark
	}
	return aboutIconLight
}
