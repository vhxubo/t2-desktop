package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
)

// authDialogFor 是授权弹窗：列表页、详情页、登录回退页与各个附属窗口共用
// 同一份状态，但只画在当前聚焦的窗口里（见 focusedAuthTarget）。点
// 「前往授权」打开授权页，授权成功后应用不会重跑被中断的操作——用户需要自己
// 再点一次刚才的操作。发起窗口已经关掉的那一次在登记时就被丢掉了（见
// demandAuthorizationFor），不会换一个窗口再弹出来。
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
	// Lobe 的 Modal：标题栏与 ✕、取消/前往授权按钮由组件提供；点窗外、Esc
	// 与「取消」都由 Modal 关窗，下面统一按取消处理，清掉这次授权状态。
	ok := feedback.Modal(c, &a.authGate.open, feedback.ModalProps{
		Title:      title,
		Width:      460,
		OkText:     "前往授权",
		CancelText: "取消",
	}, func() {
		ui.Column(c).FillWidth().Gap(10).Children(func() {
			ui.Text(c, need.Message).TextColor(t.TextMuted).MaxLines(3)
			if len(need.Scopes) > 0 {
				ui.Text(c, "缺少的权限范围："+strings.Join(need.Scopes, "、")).
					FontSize(12).TextColor(t.Text).MaxLines(2)
			}
			ui.Text(c, "点「前往授权」会在浏览器里打开授权页；授权完成后，请回到这里自己再点一次刚才的操作。").
				FontSize(12).TextColor(t.TextMuted).MaxLines(3)
			ui.Text(c, "这次授权会重新申请："+strings.Join(a.auth.requestedScopePreview(need.Scopes), "、")+
				"。授权页上要整体同意；如果之后仍缺某项，去 2Libra 的「授权应用」页确认它没被单独关掉。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(3)
		})
	})
	if ok {
		// authorizeGate 自己会关掉弹窗并打开授权页；授权成功后由用户重新操作。
		a.authorizeGate()
		return
	}
	if !a.authGate.open {
		a.cancelAuthGate("已取消授权")
	}
}

// authGateShowsIn 报告弹窗该不该画在 owner 窗口里：只画在发起过失败调用的
// 窗口里，别的窗口一律不画。
func (a *app) authGateShowsIn(owner toastTarget) bool {
	return a.authGate.open && a.authGate.wants(owner)
}
