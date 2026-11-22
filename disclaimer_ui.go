package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
)

// disclaimerContent 是声明弹窗的正文：首次启动弹窗与「关于」窗口的重看共用
// 同一段文案，避免两处各写一份以后改了其一忘了其二。
func disclaimerContent(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, "T2 全称 Third Party 2Libra（第三方 2Libra），是由 vhxubo 独立开发运营的非官方桌面客户端，"+
			"与 2Libra 无隶属、合作或背书关系。").
			TextColor(t.Text).MaxLines(3)
		ui.Text(c, "2Libra 仅提供账号授权与开放接口，内容版权归原作者所有。"+
			"本应用基于开放 API 开发，仅就界面完整性与接口一致性承担责任。").
			FontSize(12).TextColor(t.TextMuted).MaxLines(4)
		ui.Text(c, "您因授权或使用本应用而对账号产生的任何影响，均应自行评估并承担，与开发者无关。"+
			"继续使用视为您已知悉并同意本条款；如不接受，请退出本应用。").
			FontSize(12).TextColor(t.TextMuted).MaxLines(4)
		// 条款要求的另外三件事也放进首启弹窗：授权可撤销、金币与内容的后果自负、
		// 令牌的存放方式。更细的权限逐项用途与责任划分放在「关于」窗口的「条款」。
		ui.Text(c, "授权令牌仅存于本机系统凭据库，本应用不上传、不共享；您可随时在 2Libra 的「授权应用」页"+
			"撤销授权或逐项关闭权限，撤销后下一个请求即失效。以您名义发布的内容及打赏、盲盒等金币变动不可撤销，"+
			"仅在您确认后执行，相关后果由您承担。未成年人须在监护人同意下使用。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(6)
	})
}

// disclaimerActions 是声明弹窗的底部动作：认可就继续，不认可就退出整个应用。
// 退出走 a.quitApp，与托盘、系统菜单是同一条路（无头测试里换成记录调用）。
// Modal 用它自己的页脚时无法区分「退出」与 Esc / ✕ 的关闭，所以这里关掉
// 默认页脚，把两个动作画在正文里。
func (a *app) disclaimerActions(c *ui.Context, onContinue func()) {
	ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
		if action.Button(c, "退出", action.ButtonProps{}).Clicked() {
			a.quitApp()
		}
		if action.Button(c, "继续使用", action.ButtonProps{Variant: action.Primary}).Clicked() {
			onContinue()
		}
	})
}

// disclaimerDialog 是首次启动的声明弹窗：写明 T2 全称（Third Party 2Libra）、
// 与 2Libra 的关系、版权说明和免责声明。盖在主窗口内容之上，点「继续使用」或
// 关掉弹窗都写盘（继续使用即视为同意），本进程之后不再弹；点「退出」则直接
// 退出应用、不写盘，下次启动仍会看到。想再看可随时打开「关于」窗口。
func (a *app) disclaimerDialog(c *ui.Context) {
	if a.disclaimer == nil || !a.disclaimerOpen {
		return
	}
	open, keepGoing := true, false
	feedback.Modal(c, &open, feedback.ModalProps{
		Title:    "关于 T2",
		Width:    480,
		NoFooter: true,
	}, func() {
		ui.Column(c).Gap(16).Children(func() {
			disclaimerContent(c)
			a.disclaimerActions(c, func() {
				keepGoing = true
				open = false
			})
		})
	})
	if keepGoing || !open {
		a.disclaimerOpen = false
		if err := a.disclaimer.markSeen(); err != nil {
			a.toast(toastMain, err.Error())
		}
	}
}

// aboutDisclaimerDialog 是「关于」窗口里的重看声明弹窗。用 a 自己的开合状态，
// 与首次启动的 a.disclaimerOpen 分开：重看只是再看一遍，不写 disclaimer.json
// （seen 由首次启动那条路径负责），主窗口也不会因此跟着画一遍。
func (a *app) aboutDisclaimerDialog(c *ui.Context) {
	if !a.aboutDisclaimerOpen {
		return
	}
	feedback.Modal(c, &a.aboutDisclaimerOpen, feedback.ModalProps{
		Title:    "关于 T2",
		Width:    480,
		NoFooter: true,
	}, func() {
		ui.Column(c).Gap(16).Children(func() {
			disclaimerContent(c)
			a.disclaimerActions(c, func() { a.aboutDisclaimerOpen = false })
		})
	})
}
