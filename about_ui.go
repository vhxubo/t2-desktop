package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 关于窗口的尺寸：只列开发者、邮箱、项目地址与 2Libra 主页几项，比设置窗口
// 窄也矮一些。
const (
	aboutWindowWidth  = 460
	aboutWindowHeight = 380
	aboutWindowMinW   = 380
	aboutWindowMinH   = 300
)

/* ------------------------------------------------------------------
   关于窗口：全局入口、显示与关闭
---------------------------------------------------------------- */

// showAbout 是「关于」窗口的全局入口：任何窗口、任何 goroutine 都能调用。
// 它把工作挪到主线程，窗口已经开着就只把它带到前台，不会开出第二个。
func (a *app) showAbout() {
	if a.onAboutOpen != nil {
		a.onAboutOpen()
	}
	a.publish(a.showAboutWindow)
}

// showAboutWindow 在主线程上真正打开「关于」窗口。打开时如果已经开着就把它
// 提到前台，否则新建一个独立窗口。
func (a *app) showAboutWindow() {
	// 无头测试没有主循环：不碰 mygo 的窗口，测试直接渲染 aboutWindowView。
	if a.win == nil {
		return
	}
	if win := a.aboutWin; win != nil {
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
		win.Invalidate()
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             "关于",
		Width:             aboutWindowWidth,
		Height:            aboutWindowHeight,
		MinWidth:          aboutWindowMinW,
		MinHeight:         aboutWindowMinH,
		StateKey:          "about",
		BackgroundColor:   "light-dark(#ffffff, #18181b)",
		TitleBarStyle:     mygo.TitleBarHidden, // 和主窗口、设置窗口一样自绘标题栏
		HideWindowButtons: true,
		Content:           ui.View(a.aboutWindowView),
	})
	a.aboutWin = win
	win.OnClosed(func() { a.aboutWin = nil })
}

// closeAboutWindow 关掉「关于」窗口。只在主线程调用。
func (a *app) closeAboutWindow() {
	win := a.aboutWin
	if win == nil {
		return
	}
	a.aboutWin = nil
	win.Close()
}

/* ------------------------------------------------------------------
   关于窗口的界面
---------------------------------------------------------------- */

// aboutWindowView 是「关于」窗口的 view。窗口自己带标题栏与窗口按钮，所以
// 这里不需要遮罩，也不占主窗口的位置。
func (a *app) aboutWindowView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	c.Root().Background(t.Background)
	// Esc 直接关掉「关于」窗口，和系统关于面板的直觉一致。
	c.OnShortcut(0, ui.KeyEscape, a.closeAboutWindow)

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、右上角是最小化与关闭，按钮与主窗口同一套画法。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(t.Surface).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Text(c, "关于").FontSize(13).Bold().SingleLine().TextColor(t.Text)
			})
			a.titleButton(c, titleMinimize, "最小化", func() {
				if win := a.aboutWin; win != nil {
					win.Minimize()
				}
			})
			a.titleButton(c, titleClose, "关闭", func() { a.closeAboutWindow() })
		})
		ui.Divider(c)

		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).FillWidth().Padding(18, 20).Gap(16).Children(func() {
				// 标题区：应用名与一句话定位。
				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "T2 Desktop").FontSize(18).Bold().TextColor(t.Text)
				})
				ui.Text(c, "2Libra 第三方桌面客户端").
					FontSize(12).TextColor(t.TextMuted).MaxLines(2)

				ui.Divider(c)

				// 联系与来源：邮箱、项目地址与 2Libra 主页都可点击打开，
				// 开发者是纯文本（没有对应的可跳转链接）。
				a.aboutRow(c, "开发者", "vhxubo", "")
				a.aboutRow(c, "邮箱", "vhxubo@gmail.com", "mailto:vhxubo@gmail.com")
				a.aboutRow(c, "项目地址", "github.com/vhxubo/t2-desktop",
					"https://github.com/vhxubo/t2-desktop")
				a.aboutRow(c, "2Libra 主页", "https://2libra.com/user/vhxubo/about",
					"https://2libra.com/user/vhxubo/about")
			})
		})
	})
}

// aboutRow 是「关于」窗口里的一行信息：左侧是标签（灰色固定宽度），右侧是
// 值。link 非空时整行可点击，悬停浅灰底，点一下用系统默认程序打开（邮件或
// 浏览器）；link 为空时是只读文本。
func (a *app) aboutRow(c *ui.Context, label, value, link string) {
	t := c.Theme()
	if link == "" {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Width(72)
			ui.Text(c, value).FontSize(12).TextColor(t.Text).Grow(1)
		})
		return
	}
	b := ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Padding(6, 8).
		Radius(t.Radius).Background(ui.Transparent)
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Width(72)
		ui.Text(c, value).FontSize(12).TextColor(t.Accent).SingleLine().Grow(1)
	})
	b.Label(value).Tooltip("点击打开").OnClick(func() { a.openExternal(link) })
}
