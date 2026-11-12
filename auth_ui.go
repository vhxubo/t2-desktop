package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// authDialog 是全局授权弹窗：列表页、详情页与登录回退页共用同一个弹窗，
// 叠在最上层。点「前往授权」打开系统浏览器，回调送达后 onAuthChanged 会
// 自动重跑被中断的调用。
func (a *app) authDialog(c *ui.Context) {
	if !a.authGate.open {
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
			ui.Text(c, "点「前往授权」会在系统浏览器里打开授权页；回调被接受后，刚才被中断的操作会自动继续。").
				FontSize(12).TextColor(t.TextMuted).MaxLines(3)
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
