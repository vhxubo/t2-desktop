package main

import (
	_ "embed"
	"log"
	"runtime"

	"github.com/egoist/mygo"
)

// 应用图标与托盘图标都备了深浅两套：浅色外观用白底深字的 T2，深色外观用
// 深底白字的 T2。图标随外观切换，而不是只跟着系统：应用里固定了浅色或深色
// 时也用对应的那套（见 theme.go 的 mode）。
//
// 运行时的应用图标走 mygo.App.Dock.SetIcon：Windows 上它换掉全部窗口（含以后
// 新建的窗口）的图标，Linux 上换掉全局默认窗口图标，macOS 上换掉 Dock 图标。
// 托盘图标走 mygo.Tray.SetIcon。构建安装包用的图标仍是 src/resources/icon.png。

//go:embed resources/app_icon_light.png
var appIconLightPNG []byte

//go:embed resources/app_icon_dark.png
var appIconDarkPNG []byte

//go:embed resources/icon.png
var appIconLightLargePNG []byte

//go:embed resources/icon-dark.png
var appIconDarkLargePNG []byte

//go:embed resources/tray-dark.png
var trayDarkIconPNG []byte

// iconPair 把同一枚图标的深浅两版收在一起，外观决定用哪一版。
type iconPair struct {
	light []byte
	dark  []byte
}

func (p iconPair) forDark(dark bool) []byte {
	if dark {
		return p.dark
	}
	return p.light
}

var (
	// appIcons 是 256×256 的那套：Windows 的图标资源上限就是 256，Linux 的
	// 窗口图标用这个尺寸也够清楚。
	appIcons = iconPair{light: appIconLightPNG, dark: appIconDarkPNG}
	// appIconsLarge 是 1024×1024 的那套，给 macOS 的 Dock 用，Retina 屏更清楚。
	appIconsLarge = iconPair{light: appIconLightLargePNG, dark: appIconDarkLargePNG}
	// trayIcons 用于通知区域 / 菜单栏：32×32，即为高分屏准备的两倍尺寸。
	// 浅色那份是 tray.go 里 embed 的 resources/tray.png，沿用原变量名。
	trayIcons = iconPair{light: trayIconPNG, dark: trayDarkIconPNG}
)

// appIconPNG 按平台挑运行时用的应用图标。
func appIconPNG(dark bool) []byte {
	if runtime.GOOS == "darwin" {
		return appIconsLarge.forDark(dark)
	}
	return appIcons.forDark(dark)
}

// currentIconDark 报告当前生效的深浅外观。mygo.Theme.IsDark 已经把「跟随系统
// / 固定浅色 / 固定深色」算进去了，但它在应用就绪前不能调用；无头测试里一律
// 按浅色处理，不去碰原生主循环。
func currentIconDark() bool {
	return mygo.App.IsReady() && mygo.Theme.IsDark()
}

// iconTheme 记下已经应用的外观，避免外观事件重复触发时反复设置原生图标。
// 只在主线程读写；无头测试可以替换 currentDark / apply 来断言切换逻辑。
type iconTheme struct {
	ready       bool
	dark        bool
	currentDark func() bool
	apply       func(dark bool)
}

func newIconTheme(b *desktop) *iconTheme {
	return &iconTheme{
		currentDark: currentIconDark,
		apply:       b.applyIcons,
	}
}

// setDark 应用指定外观；深浅没变时什么都不做。
func (m *iconTheme) setDark(dark bool) {
	if m == nil || (m.ready && m.dark == dark) {
		return
	}
	m.ready = true
	m.dark = dark
	if m.apply != nil {
		m.apply(dark)
	}
}

// refresh 按当前生效外观重新选图标。应用没就绪时不碰原生 API。
func (m *iconTheme) refresh() {
	if m == nil || !mygo.App.IsReady() {
		return
	}
	m.setDark(m.currentDark())
}

// applyIcons 把深浅两套图标真正装到原生应用与托盘上。托盘可能因为创建失败
// 而是 nil，这只影响托盘那一份，应用图标照常切换。
func (b *desktop) applyIcons(dark bool) {
	if err := mygo.App.Dock.SetIcon(appIconPNG(dark)); err != nil {
		log.Printf("切换应用图标失败：%v", err)
	}
	if b.tray != nil {
		if err := b.tray.SetIcon(trayIcons.forDark(dark), false); err != nil {
			log.Printf("切换托盘图标失败：%v", err)
		}
	}
}

// refreshIcons 供设置窗口保存外观、以及 mygo 的系统外观事件调用。
func (b *desktop) refreshIcons() {
	if b.icons != nil {
		b.icons.refresh()
	}
}

// startIcons 在应用就绪后接上系统外观变化事件，并立即按当前外观装一次图标。
// 用户从设置窗口切换外观时另由 saveThemeChoice 显式刷新：mygo 的 SetSource
// 不会触发 Theme.OnUpdated。
func (b *desktop) startIcons() {
	mygo.Theme.OnUpdated(b.refreshIcons)
	b.refreshIcons()
}
