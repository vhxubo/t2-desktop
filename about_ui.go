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
				ui.Text(c, "第三方 2Libra 原生桌面端").
					FontSize(12).TextColor(t.TextMuted).MaxLines(2)

				ui.Divider(c)

				// 联系与来源：开发者、邮箱、项目地址与 2Libra 主页都可点击打开。
				a.aboutRow(c, "开发者", "vhxubo", "https://github.com/vhxubo")
				a.aboutRow(c, "邮箱", "vhxubo@gmail.com", "mailto:vhxubo@gmail.com")
				a.aboutRow(c, "项目地址", "github.com/vhxubo/t2-desktop",
					"https://github.com/vhxubo/t2-desktop")
				a.aboutRow(c, "2Libra 主页", "https://2libra.com/user/vhxubo/about",
					"https://2libra.com/user/vhxubo/about")

				ui.Divider(c)

				// 技术栈：说明这是用 Go 与原生 UI 框架实现的桌面端，没有内嵌
				// 浏览器。整段展示，让用户一眼看清运行环境与依赖。
				ui.Text(c, "技术栈").FontSize(13).Bold().TextColor(t.Text)
				ui.Text(c, "Go 语言，基于 mygo（github.com/egoist/mygo）原生 UI 框架构建，无内嵌浏览器；"+
					"数据全部来自 2Libra 开放 API v1。").
					FontSize(11).TextColor(t.TextMuted).MaxLines(4)
				// 协议：以 MIT 协议开源，点一下跳到仓库里的 LICENSE 文件。
				a.aboutRow(c, "协议", "MIT",
					"https://github.com/vhxubo/t2-desktop/blob/main/LICENSE")

				ui.Divider(c)

				// 条款：第三方接入的必须指引。整段展示不截断，让用户打开应用
				// 就能看到本应用与 2Libra 的关系、授权撤回方式与版权说明。
				ui.Text(c, "条款").FontSize(13).Bold().TextColor(t.Text)
				ui.Text(c, "本应用由第三方开发者 vhxubo 独立开发运营，与 2Libra 无隶属或合作关系。"+
					"2Libra 仅提供账号授权与接口，不对本应用的可用性、内容与数据处理负责。"+
					"你可以随时在 2Libra 的「授权应用」页面撤销本应用的授权。"+
					"应用内展示的内容版权归原作者所有，详情页保留作者昵称与原文链接。").
					FontSize(11).TextColor(t.TextMuted).MaxLines(12)
				a.aboutRow(c, "服务条款", "2Libra 第三方接入条款",
					"https://r2.2libra.com/openapi/v1/terms.md")
			})
		})
	})
}

// aboutRow 是「关于」窗口里的一行信息：左侧是标签（灰色固定宽度），右侧是
// 值。无论是否可点击，整行的内边距与标签列宽都一致，保证各行标签左边缘与值
// 左边缘分别对齐。link 非空时整行可点击，悬停浅灰底，点一下用系统默认程序
// 打开（邮件或浏览器）；link 为空时是只读文本。
func (a *app) aboutRow(c *ui.Context, label, value, link string) {
	t := c.Theme()
	color := t.Text
	if link != "" {
		color = t.Accent
	}
	b := ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Padding(6, 8).
		Radius(t.Radius).Background(ui.Transparent)
	if link != "" && b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Text(c, label).FontSize(12).TextColor(t.TextMuted).Width(84)
		ui.Text(c, value).FontSize(12).TextColor(color).SingleLine().Grow(1)
	})
	if link != "" {
		b.Label(value).Tooltip("点击打开").OnClick(func() { a.openExternal(link) })
	}
}
