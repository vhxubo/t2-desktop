package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/updater"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 关于窗口的尺寸：比设置窗口矮一些，内容分几张 Lobe 卡片放，装不下时滚动。
const (
	aboutWindowWidth  = 470
	aboutWindowHeight = 640
	aboutWindowMinW   = 400
	aboutWindowMinH   = 460

	// aboutLabelWidth 是信息行标签列的固定宽度：开发者、邮箱、项目地址等各行
	// 都用它，标签左边缘因此落在同一列（见 sidebar_test.go 的对齐断言）。
	aboutLabelWidth = 84
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
		Title:             a.subWindowTitle("关于"),
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
	win.OnClosed(func() {
		a.aboutWin = nil
		a.accountWindowClosed(win)
	})
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

   其余窗口都已经走 Lobe UI；这里把「关于」也换成同一套语言：颜色取
   TokensOf(c)，卡片 / 文本 / 标签 / 按钮 / 提示分别由 layout.Block、
   display.Text、display.Tag、action.Button 与 feedback.Alert 提供。文案与
   动作保持不变，只换观感。
---------------------------------------------------------------- */

// aboutWindowView 是「关于」窗口的 view。窗口自己带标题栏与窗口按钮，所以
// 这里不需要遮罩，也不占主窗口的位置。
func (a *app) aboutWindowView(c *ui.Context) {
	a.applyTheme(c)
	tk := lobetheme.TokensOf(c)
	c.Root().Background(tk.BgLayout)
	// Esc 直接关掉「关于」窗口，和系统关于面板的直觉一致。
	c.OnShortcut(0, ui.KeyEscape, a.closeAboutWindow)

	ui.Column(c).Fill().Children(func() {
		// 自绘标题栏：拖动窗口、右上角是最小化与关闭，画法与主窗口一致。
		// 图标换成 Lucide 的信息符号，文字走 display.Text，配色走 Lobe token。
		ui.Row(c).Height(titleBarHeight).FillWidth().AlignItems(ui.Stretch).
			Background(tk.BgLayout).DragWindow().Children(func() {
			ui.Row(c).Grow(1).MinWidth(0).Padding(0, 10).Gap(8).
				AlignItems(ui.Center).Children(func() {
				ui.Icon(c, lucide.Info()).Size(16, 16).TextColor(tk.Primary)
				display.Text(c, "关于", display.TextProps{Strong: true, FontSize: 13, Rows: 1})
				a.accountWindowTag(c)
			})
			titleBarButton(c, titleMinimize, "最小化", func() {
				if win := a.aboutWin; win != nil {
					win.Minimize()
				}
			})
			titleBarButton(c, titleClose, "关闭", func() { a.closeAboutWindow() })
		})
		layout.Divider(c, layout.DividerProps{})

		// ScrollArea 用 Lobe 的细滚动条与边缘渐隐，不再用 mygo 原生 Scroll。
		list := layout.ScrollArea(c, layout.ScrollAreaProps{Fade: true}, func() {
			ui.Column(c).FillWidth().Padding(16, 16).Gap(12).Children(func() {
				a.aboutHero(c)
				a.aboutContactSection(c)
				a.aboutTechSection(c)
				a.aboutTermsSection(c)
			})
		})
		list.Grow(1).MinHeight(0)
	})
	// 重看声明的弹窗叠在内容之上，和主窗口的全局弹窗同一套画法。
	a.aboutDisclaimerDialog(c)
}

// aboutHero 是品牌卡片：应用头像、名称与定位，以及版本和检查更新入口。
func (a *app) aboutHero(c *ui.Context) {
	tk := lobetheme.TokensOf(c)
	card := layout.Block(c, layout.BlockProps{Variant: layout.BlockOutlined}).
		FillWidth().Padding(18, 16)
	card.Children(func() {
		ui.Column(c).FillWidth().Gap(12).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				// 主色圆角方块 + Lucide 会话图标，像 Lobe 里的应用头像。
				logo := ui.Row(c).Size(48, 48).Shrink(0).Radius(12).
					Background(tk.Primary).AlignItems(ui.Center).Justify(ui.Center)
				logo.Children(func() {
					ui.Icon(c, lucide.MessagesSquare()).Size(26, 26).TextColor(tk.PrimaryForeground)
				})
				ui.Column(c).Gap(3).MinWidth(0).Children(func() {
					display.Text(c, "T2 Desktop", display.TextProps{Strong: true, FontSize: 18, Rows: 1})
					display.Text(c, "Third Party 2Libra · 第三方 2Libra", display.TextProps{
						Type: display.TextSecondary, FontSize: 12, Rows: 1,
					})
				})
			})
			layout.Divider(c, layout.DividerProps{})
			// 版本与更新：展示当前版本，并给想立即确认的人一个检查入口；
			// 更新窗口会说明结果或列出新版本。应用无法自更新时（开发构建、
			// 包管理器安装的版本）点一下也会在更新窗口里说明原因。
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				display.Text(c, "当前版本 "+mygo.App.Version(), display.TextProps{
					Type: display.TextSecondary, FontSize: 12, Rows: 1,
				}).Grow(1).MinWidth(0)
				if action.Button(c, "检查更新", action.ButtonProps{
					Variant: action.Primary, Size: lobetheme.Small,
				}).Clicked() {
					a.checkForUpdates()
				}
			})
			// 第三方声明放在卡片顶部、不用滚动就能看到；弹窗只在这个窗口里画，
			// 不动 seen（见 disclaimer_ui.go 的 aboutDisclaimerDialog）。
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				display.Text(c, "第三方声明", display.TextProps{
					Type: display.TextSecondary, FontSize: 12, Rows: 1,
				}).Grow(1).MinWidth(0)
				if action.Button(c, "重看声明", action.ButtonProps{
					Size: lobetheme.Small,
				}).Clicked() {
					a.aboutDisclaimerOpen = true
				}
			})
			// 鸿蒙字体的授权条款要求在用到它的软件里显著声明（fonts/LICENSE）。
			// 这段放在不滚动就能看到的顶部卡片；macOS 用系统苹方，不显示。
			if usesHarmonySans() {
				display.Text(c, "界面字体：鸿蒙字体 HarmonyOS Sans · © 2021 Huawei", display.TextProps{
					Type: display.TextSecondary, FontSize: 12, Rows: 2,
				})
			}
			// 上一次检查若发现有新版本，这里提示一下，点「检查更新」看详情。
			if a.updates {
				if v := updater.AvailableVersion(); v != "" {
					feedback.Alert(c, "发现新版本 "+v, feedback.AlertProps{
						Type: feedback.Info, Variant: feedback.Soft,
					})
				}
			}
		})
	})
}

// aboutSection 画一块带标题的 Lobe 卡片，各分类共用同一套版式。描边、圆角
// 与底色来自 layout.Block，标题走 display.Text。
func aboutSection(c *ui.Context, glyph *ui.SVG, title string, body func()) {
	tk := lobetheme.TokensOf(c)
	card := layout.Block(c, layout.BlockProps{Variant: layout.BlockOutlined}).
		FillWidth().Padding(14, 14)
	card.Children(func() {
		ui.Column(c).FillWidth().Gap(10).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, glyph).Size(15, 15).TextColor(tk.Primary).Shrink(0)
				display.Text(c, title, display.TextProps{Strong: true, FontSize: 13, Rows: 1})
			})
			body()
		})
	})
}

// aboutContactSection 是联系与来源：开发者、邮箱、项目地址与 2Libra 主页。
func (a *app) aboutContactSection(c *ui.Context) {
	aboutSection(c, lucide.Users(), "联系与来源", func() {
		a.aboutRow(c, lucide.User(), "开发者", "vhxubo", "https://github.com/vhxubo")
		a.aboutRow(c, lucide.Mail(), "邮箱", "vhxubo@gmail.com", "mailto:vhxubo@gmail.com")
		a.aboutRow(c, lucide.Code(), "项目地址", "github.com/vhxubo/t2-desktop",
			"https://github.com/vhxubo/t2-desktop")
		a.aboutRow(c, lucide.Globe(), "2Libra 主页", "https://2libra.com/user/vhxubo/about",
			"https://2libra.com/user/vhxubo/about")
	})
}

// aboutTechSection 是技术栈与开源协议：说明这是用 Go 与原生 UI 框架实现的
// 桌面端，没有内嵌浏览器，数据全部来自 2Libra 开放 API v1。
func (a *app) aboutTechSection(c *ui.Context) {
	aboutSection(c, lucide.Blocks(), "技术栈", func() {
		display.Text(c, "Go 语言，基于 mygo（github.com/egoist/mygo）原生 UI 框架构建，无内嵌浏览器；"+
			"数据全部来自 2Libra 开放 API v1。",
			display.TextProps{Type: display.TextSecondary, FontSize: 12, Rows: 4})
		// Lobe 标签列出关键组成，比整段文字更好扫读。
		ui.Row(c).Gap(6).Children(func() {
			for _, tag := range []string{"Go", "mygo", "Lobe UI", "2Libra API v1"} {
				display.Tag(c, tag, display.TagProps{
					Size: lobetheme.Small, Variant: display.TagFilled, Round: true,
				})
			}
		})
		// 协议：以 MIT 协议开源，点一下跳到仓库里的 LICENSE 文件。
		a.aboutRow(c, lucide.Scale(), "协议", "MIT",
			"https://github.com/vhxubo/t2-desktop/blob/main/LICENSE")
	})
}

// aboutTermsSection 是第三方接入条款：用户打开应用就能看到 T2 全称、与 2Libra
// 的关系、权限逐项用途、授权撤回方式与责任划分。窗口本身可滚动，整段不截断。
func (a *app) aboutTermsSection(c *ui.Context) {
	aboutSection(c, lucide.FileText(), "条款", func() {
		props := display.TextProps{Type: display.TextSecondary, FontSize: 12, Rows: 0}
		display.Text(c, "T2 是 Third Party 2Libra（第三方 2Libra）的简称。"+
			"本应用由第三方开发者 vhxubo 独立开发运营，与 2Libra 无隶属或合作关系。"+
			"2Libra 仅提供账号授权与接口，不对本应用的可用性、内容与数据处理负责。"+
			"应用内展示的内容版权归原作者所有，详情页保留作者昵称与原文链接。", props)
		// 接入条款附录要求逐项列出申请的权限与用途；追加权限由 auth_gate 触发，
		// 授权弹窗里会展示本次重新申请的范围，这里只说明各自用来做什么。
		display.Text(c, "授权与权限：可随时在 2Libra 的「授权应用」页撤销授权或逐项关闭权限，"+
			"撤销后下一个请求即失效。默认申请的权限及用途："+
			"profile（公开资料）、read（公开内容与评论）、read:me（通知、收藏、关注与合集）、"+
			"read:economy（金币余额与流水）、write:comment（发表与编辑评论）、interact（收藏、表态、书签与关注）。"+
			"发帖与金币相关功能按需追加 write:topic（发帖、编辑与投票）、"+
			"write:economy（签到、打赏、盲盒等金币变动）、upload（上传图片）。", props)
		display.Text(c, "责任与风险：您因授权或正常使用本应用而对账号产生的任何影响均由您自行评估并承担；"+
			"以您名义发布的内容视为您发布，须自行确保合规。打赏、签到、盲盒、竞猜等金币变动不可撤销，"+
			"本应用仅在您逐次确认后执行。令牌仅存于本机系统凭据库，不上传、不共享；请妥善保管设备与账号。", props)
		display.Text(c, "第三方服务与可用性：图片可选用第三方图床，上传后按该服务条款处理。"+
			"接口按现状提供，2Libra 可调整配额、端点或响应；因限流、维护、接口变更或不可抗力导致的功能受限，"+
			"本应用不承担责任。未成年人应在监护人同意下使用与授权；条款更新后继续使用视为接受。", props)
		a.aboutRow(c, lucide.FileBadge(), "服务条款", "2Libra 第三方接入条款",
			"https://r2.2libra.com/openapi/v1/terms.md")
	})
}

// aboutRow 是「关于」窗口里的一行信息：左侧是图标与固定宽度的标签，右侧是值。
// 各行内边距与标签列宽一致，保证标签左边缘对齐。link 非空时整行可点击，悬停
// 浅底，点一下用系统默认程序打开（邮件或浏览器）；link 为空时是只读文本。
func (a *app) aboutRow(c *ui.Context, glyph *ui.SVG, label, value, link string) {
	tk := lobetheme.TokensOf(c)
	clickable := link != ""
	var row ui.Element
	if clickable {
		row = ui.ButtonBase(c).Row()
	} else {
		row = ui.Row(c)
	}
	row.FillWidth().AlignItems(ui.Center).Padding(7, 8).Gap(10).Radius(tk.BorderRadius)
	if clickable && row.Hovered() {
		row.Background(tk.FillTertiary)
	}
	row.Children(func() {
		ui.Icon(c, glyph).Size(16, 16).TextColor(tk.Icon).Shrink(0)
		display.Text(c, label, display.TextProps{
			Type: display.TextSecondary, FontSize: 12, Rows: 1,
		}).Width(aboutLabelWidth).Shrink(0)
		valueText := display.Text(c, value, display.TextProps{FontSize: 12, Rows: 1})
		if clickable {
			valueText.TextColor(tk.Primary)
		}
		valueText.Grow(1).MinWidth(0)
		if clickable {
			ui.Icon(c, lucide.ExternalLink()).Size(13, 13).TextColor(tk.TextQuaternary).Shrink(0)
		}
	})
	if clickable {
		row.Label(value).Tooltip("点击打开").OnClick(func() { a.openExternal(link) })
	}
}
