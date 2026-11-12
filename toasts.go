package main

import "github.com/egoist/mygo/ui"

// 窗口内提示。操作的结果（成功或者失败）不再用内联文字占版面，而是在窗口
// 底部弹一条 toast，几秒后自己消失：报错与页面内容分开，页面不会因为一次
// 失败多出一行红字，也不会一直留着。
//
// toast 只能在构建期通过 *ui.Context 发出，而后台结果是在 publish 回到 UI
// 线程时产生的，所以先按窗口排队，等那个窗口下一次重建时再交给 c.Toast。

// toastTarget 是提示该出现在哪个窗口：操作发生在哪个窗口就提示在哪个窗口；
// 那个窗口不在了就退回主窗口，结果总有人看见。
type toastTarget int

const (
	toastMain          toastTarget = iota // 主窗口：列表、详情、登录页
	toastNotifications                    // 通知窗口
	toastSettings                         // 设置窗口
	toastCoins                            // 金币窗口：签到等金币操作
)

// toastNote 是一条等着显示的提示。
type toastNote struct {
	target toastTarget
	text   string
}

// toast 记下一条提示，并让将来显示它的那个窗口重画。只在 UI 线程调用：
// 它要读窗口对象。
func (a *app) toast(target toastTarget, text string) {
	if text == "" {
		return
	}
	a.toasts = append(a.toasts, toastNote{target: target, text: text})
	// 目标窗口开着就只重画它；不在了就由主窗口下一帧显示，规则与
	// mainClaimsToasts 一致。
	switch target {
	case toastNotifications:
		if a.notifWin != nil {
			a.redrawNotificationWindow()
			return
		}
	case toastSettings:
		if a.settingsWin != nil {
			a.redrawSettingsWindow()
			return
		}
	case toastCoins:
		if a.coinWin != nil {
			a.redrawCoinWindow()
			return
		}
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// drainToasts 把这一帧归本窗口的提示交给 c.Toast，其余留在队列里等自己的
// 窗口。claims 回答「这条提示归不归本窗口」。
func (a *app) drainToasts(c *ui.Context, claims func(toastTarget) bool) {
	if len(a.toasts) == 0 {
		return
	}
	kept := a.toasts[:0]
	var show []string
	for _, n := range a.toasts {
		if claims(n.target) {
			show = append(show, n.text)
			continue
		}
		kept = append(kept, n)
	}
	clear(a.toasts[len(kept):])
	a.toasts = kept
	for _, text := range show {
		c.Toast(text)
	}
}

// mainClaimsToasts 说主窗口该不该显示这条提示：主窗口自己的当然显示；通知
// 窗口、设置窗口或金币窗口不在了，它们的提示也退到主窗口，免得结果没人看见。
func (a *app) mainClaimsToasts(target toastTarget) bool {
	switch target {
	case toastNotifications:
		return a.notifWin == nil
	case toastSettings:
		return a.settingsWin == nil
	case toastCoins:
		return a.coinWin == nil
	}
	return true
}

// claimsToasts 是通知窗口、设置窗口与金币窗口用的规则：只显示发给自己的提示。
func (a *app) claimsToasts(mine toastTarget) func(toastTarget) bool {
	return func(target toastTarget) bool { return target == mine }
}
