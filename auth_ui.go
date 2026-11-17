package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// authDialogFor 是授权弹窗：列表页、详情页、登录回退页与各个附属窗口共用
// 同一份状态，但只画在发起被中断调用的那个窗口里（owner 就是那个窗口）。点
// 「前往授权」打开授权页，回调送达后 onAuthChanged 会自动重跑被中断的调用。
// 发起窗口已经关掉的那一次在登记时就被丢掉了（见 demandAuthorizationFor），
// 不会换一个窗口再弹出来。
func (a *app) authDialogFor(c *ui.Context, owner toastTarget) {
	if !a.authGate.open || !a.authGateShowsIn(owner) {
		return
	}
	t := c.Theme()
	need := a.authGate.need
	title := "需要登录"
	if len(need.Scopes) > 0 {
		title = "需要授权"
	}
	dismissed := ui.Modal(c, &a.authGate.open, func() {
		ui.Column(c).Width(460).Gap(10).Children(func() {
			ui.Text(c, title).FontSize(16).Bold()
			ui.Text(c, need.Message).TextColor(t.TextMuted).MaxLines(3)
			if len(need.Scopes) > 0 {
				ui.Text(c, "缺少的权限范围："+strings.Join(need.Scopes, "、")).
					FontSize(12).TextColor(t.Text).MaxLines(2)
			}
			ui.Text(c, "点「前往授权」会在浏览器里打开授权页；回调被接受后，刚才被中断的操作会自动继续。").
				FontSize(12).TextColor(t.TextMuted).MaxLines(3)
			ui.Text(c, "这次授权会重新申请："+strings.Join(a.auth.requestedScopePreview(need.Scopes), "、")+
				"。授权页上要整体同意；如果之后仍缺某项，去 2Libra 的「授权应用」页确认它没被单独关掉。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(3)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c).Grow(1)
				if ui.Button(c, "取消").Clicked() {
					a.cancelAuthGate("已取消授权")
				}
				if ui.PrimaryButton(c, "前往授权").Clicked() {
					a.authorizeGate()
				}
			})
		})
	})
	// 点窗外或按 Esc 关闭：和「取消」同义，一并丢弃待重跑调用，否则它们会
	// 在以后某次无关的登录成功后突然执行。
	if dismissed.Dismissed() {
		a.cancelAuthGate("已取消授权")
	}
}

// authGateShowsIn 报告弹窗该不该画在 owner 窗口里：只画在发起过失败调用的
// 窗口里，别的窗口一律不画。
func (a *app) authGateShowsIn(owner toastTarget) bool {
	return a.authGate.open && a.authGate.wants(owner)
}
