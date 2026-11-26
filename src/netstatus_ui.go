package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

/* ------------------------------------------------------------------
   断网提示弹窗与「重新连接」
---------------------------------------------------------------- */

// offlineDialog 是断网提示：网络状态机确认离线时自动打开，此时全应用的重试与
// 通知推送都已暂停。点「重新连接」重新探测，成功才关掉并恢复；点「稍后」（或
// ✕ / Esc / 点窗外）只关掉提示，离线状态仍保持，下次有请求被拦下时会再弹出来
// （见 netstatus.go 的 nudge）。
func (a *app) offlineDialog(c *ui.Context) {
	if !a.offlineDialogOpen {
		return
	}
	t := c.Theme()
	feedback.Modal(c, &a.offlineDialogOpen, feedback.ModalProps{
		Title:    "网络未连接",
		Width:    420,
		NoFooter: true,
	}, func() {
		ui.Column(c).Gap(14).Children(func() {
			ui.Text(c, "当前无法连接到网络，已暂停自动重试与通知推送。请检查网络或代理设置后重试。").
				TextColor(t.TextMuted).MaxLines(4)
			if a.offlineRechecking {
				ui.Text(c, "正在重新连接…").TextColor(t.TextMuted)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c).Grow(1)
				if action.Button(c, "稍后", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
					a.offlineDialogOpen = false
				}
				if action.Button(c, "重新连接", action.ButtonProps{
					Size: lobetheme.Small, Variant: action.Primary,
				}).Clicked() {
					a.retryNetwork()
				}
			})
		})
	})
}

// retryNetwork 在后台重新探测网络：探测要发请求，不能占 UI 线程。结果经 publish
// 回到主线程——成功关掉弹窗并恢复请求，失败则继续显示弹窗并给出说明。
func (a *app) retryNetwork() {
	st := a.proxy.netStatus()
	if st == nil {
		a.offlineDialogOpen = false
		return
	}
	a.offlineRechecking = true
	go func() {
		online := st.recheck()
		a.publish(func() {
			a.offlineRechecking = false
			if online {
				a.offlineDialogOpen = false
			} else {
				a.offlineDialogOpen = true
				a.toast(toastMain, "仍然无法连接网络，请检查网络或代理设置")
			}
		})
	}()
}

// redrawNetLogWindow 让网络调试窗口里的在线/离线指示跟着状态更新。窗口没开
// 着就是空操作。
func (a *app) redrawNetLogWindow() {
	if win := a.netLogWin; win != nil {
		win.Invalidate()
	}
}
