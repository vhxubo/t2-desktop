package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
)

// disclaimerContent 是声明弹窗的正文：首次启动弹窗与「关于」窗口的重看共用
// 同一段文案，避免两处各写一份以后改了其一忘了其二。
func disclaimerContent(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, "T2 全称 Third Party 2Libra（第三方 2Libra），是由 vhxubo 独立开发运营的非官方桌面客户端。").
			TextColor(t.Text).MaxLines(3)
		ui.Text(c, "与 2Libra 无隶属或背书关系；2Libra 仅提供账号授权与开放接口。"+
			"内容版权归原作者所有，详情页保留作者昵称与原文链接。").
			FontSize(12).TextColor(t.TextMuted).MaxLines(4)
	})
}

// disclaimerDialog 是首次启动的声明弹窗：写明 T2 全称（Third Party 2Libra）、
// 与 2Libra 的关系和版权说明。它盖在主窗口内容之上，点任一按钮或关闭后写盘，
// 本进程之后不再弹；想再看可随时打开「关于」窗口。
func (a *app) disclaimerDialog(c *ui.Context) {
	if a.disclaimer == nil || !a.disclaimerOpen {
		return
	}
	open := true
	ok := feedback.Modal(c, &open, feedback.ModalProps{
		Title:      "关于 T2",
		Width:      480,
		OkText:     "我已了解",
		CancelText: "关闭",
	}, func() {
		disclaimerContent(c)
	})
	if ok || !open {
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
	ok := feedback.Modal(c, &a.aboutDisclaimerOpen, feedback.ModalProps{
		Title:      "关于 T2",
		Width:      480,
		OkText:     "我已了解",
		CancelText: "关闭",
	}, func() {
		disclaimerContent(c)
	})
	// Modal 只在取消 / 关闭时自己写 open，点 OK 只是返回 true，留给调用方关
	// （见 lobe feedback.Modal 的注释）；这里别漏掉，否则确认后弹窗不消失。
	if ok {
		a.aboutDisclaimerOpen = false
	}
}
