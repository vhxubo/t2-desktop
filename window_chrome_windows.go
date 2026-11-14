//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// Windows 11 的 DWM 会把窗口当成「圆角、有框」的窗口来渲染，于是画两样东西：
// 一圈 1 DIP 的描边（DWMWA_BORDER_COLOR 的默认值，深色主题下约 #404040），以及
// 贴着那圈圆角的阴影。普通窗口有标题栏陪着看不出来；宠物窗口整块透明，描边成了
// 桌面上一个空心的方框，阴影成了它周围一圈灰雾。
//
// mygo 没有关掉它们的出口：frameBorder 只处理 Windows 10 那条白线，Windows 11
// 的边框它明确留着；Panel 窗口反而主动调 roundFlyout 要了圆角。所以这里直接
// 问 DWM 要。
//
// 两样东西是同一个来源，实测（白底窗口上逐像素量窗口外的一行）：
//   - 只关描边：描边没了，阴影还在（255 → 225 的渐变，往外拖了二十多个像素）；
//   - 再把圆角关掉：窗口外立刻回到纯白，边缘就是内容本身。
const (
	dwmwaWindowCornerPreference = 33
	dwmwaBorderColor            = 34
	dwmwcpDoNotRound            = 1          // DWMWCP_DONOTROUND
	dwmColorNone                = 0xFFFFFFFE // DWMWA_COLOR_NONE：不画边框
)

var procDwmSetWindowAttribute = syscall.NewLazyDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")

// removeWindowChrome 让 DWM 不给这个窗口画边框与阴影。取不到 dwmapi 或调用失败
// 就算了：多一圈边、多一层灰雾不影响用，为它把应用搞崩不值。
func removeWindowChrome(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	if err := procDwmSetWindowAttribute.Find(); err != nil {
		return
	}
	corner := int32(dwmwcpDoNotRound)
	procDwmSetWindowAttribute.Call(hwnd, dwmwaWindowCornerPreference, uintptr(unsafe.Pointer(&corner)), unsafe.Sizeof(corner))
	none := uint32(dwmColorNone)
	procDwmSetWindowAttribute.Call(hwnd, dwmwaBorderColor, uintptr(unsafe.Pointer(&none)), unsafe.Sizeof(none))
}
