package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
)

// disclaimerDialog 是首次启动的声明弹窗：写明 T2 全称（Third Party 2Libra）、
// 与 2Libra 的关系和版权说明。它盖在主窗口内容之上，点任一按钮或关闭后写盘，
// 本进程之后不再弹；想再看可随时打开「关于」窗口。
func (a *app) disclaimerDialog(c *ui.Context) {
	if a.disclaimer == nil || !a.disclaimerOpen {
		return
	}
	t := c.Theme()
	open := true
	ok := feedback.Modal(c, &open, feedback.ModalProps{
		Title:      "关于 T2",
		Width:      480,
		OkText:     "我已了解",
		CancelText: "关闭",
	}, func() {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			ui.Text(c, "T2 全称 Third Party 2Libra（第三方 2Libra），是由 vhxubo 独立开发运营的非官方桌面客户端。").
				TextColor(t.Text).MaxLines(3)
			ui.Text(c, "与 2Libra 无隶属或背书关系；2Libra 仅提供账号授权与开放接口。"+
				"内容版权归原作者所有，详情页保留作者昵称与原文链接。").
				FontSize(12).TextColor(t.TextMuted).MaxLines(4)
		})
	})
	if ok || !open {
		a.disclaimerOpen = false
		if err := a.disclaimer.markSeen(); err != nil {
			a.toast(toastMain, err.Error())
		}
	}
}
