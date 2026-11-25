package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
)

// 打开应用自动签到。主窗口打开时静默尝试一次：已经签过不弹任何
// 提示，真正签到成功才在主窗口内容顶部画一条明显的 Lobe 成功 Alert。失败
// 一律静默，不让后台能力打扰用户。

const autoSignNoticeTimeout = 10 * time.Second

// autoSignNotice 是一条成功提示。closed 由 Alert 的关闭按钮或定时收起置位。
type autoSignNotice struct {
	title  string
	detail string
	closed bool
}

// maybeAutoSignIn 在主窗口打开、已登录时调用。偏好关掉、未登录、本进程已经
// 试过或在途请求未完成都直接跳过；窗口关掉重开不会重复签到。
func (a *app) maybeAutoSignIn() {
	if a.autoSignStarted || a.autoSignPrefs == nil || !a.autoSignPrefs.on() {
		return
	}
	if a.auth == nil || !a.auth.loggedIn() {
		return
	}
	cs := a.coins
	if cs == nil || cs.signBusy {
		return
	}
	a.autoSignStarted = true
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), coinSignTimeout)
	go func() {
		defer cancel()
		// 先看今日状态：已签到就只同步状态，不提交写请求、不弹提示。
		status, err := a.client.fetchCoinSign(ctx)
		if err == nil && status.Known && status.Signed {
			a.publish(func() {
				if a.coins != cs || !a.auth.loggedIn() || a.auth.username() != actor {
					return
				}
				a.applyCoinSign(status)
			})
			return
		}
		// 没签过（或状态读不到）才提交签到；重复签到由服务端以 409 拒绝。
		result, err := a.client.signToday(ctx)
		a.publish(func() {
			if a.coins != cs || !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				var ae *apiError
				if errors.As(err, &ae) && ae.Code == "conflict" {
					// 已经签过：补上状态，但不弹提示。连续天数沿用读到的今日状态。
					signed := coinSignStatus{Known: true, Signed: true, Streak: cs.sign.Streak}
					if status.Known && status.Streak > 0 {
						signed.Streak = status.Streak
					}
					a.applyCoinSign(signed)
				}
				// 其它失败静默：自动签到不该弹错误打扰用户。
				return
			}
			next := result
			next.Known, next.Signed = true, true
			if next.Streak == 0 {
				next.Streak = cs.sign.Streak
			}
			a.applyCoinSign(next)
			a.autoSignNotice = &autoSignNotice{title: "签到成功", detail: autoSignDetail(next)}
			// 到点自己收起，别一直占着版面。
			time.AfterFunc(autoSignNoticeTimeout, func() {
				a.publish(func() {
					if a.autoSignNotice != nil {
						a.autoSignNotice.closed = true
					}
				})
			})
		})
	}()
}

// setAutoSignEnabled 开关「打开应用时自动签到」并立即落盘。偏好是进程级共享的，
// publish 让其它窗口的设置页下一帧按新状态重建。
func (a *app) setAutoSignEnabled(on bool) {
	if a.autoSignPrefs == nil || a.autoSignPrefs.on() == on {
		return
	}
	if err := a.autoSignPrefs.setEnabled(on); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	a.publish(func() {})
	if on {
		a.toast(toastSettings, "已开启打开应用时自动签到")
	} else {
		a.toast(toastSettings, "已关闭打开应用时自动签到")
	}
}

// autoSignDetail 是成功提示的一行说明：有明确的奖励字段才报金币数，连续天数
// 大于一天才提。金币字段可能是余额（见 coinSignStatus 的约定），不猜金额。
func autoSignDetail(s coinSignStatus) string {
	var parts []string
	if s.RewardKnown && s.Coins > 0 {
		parts = append(parts, "获得 "+coinAmountText(s.Coins)+" 金币")
	}
	if s.Streak > 1 {
		parts = append(parts, fmt.Sprintf("连续 %d 天", s.Streak))
	}
	if len(parts) == 0 {
		return "金币已到账"
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += " · " + p
	}
	return out
}

// autoSignAlert 在主窗口内容顶部画成功 Alert：绿色 Success 配色、勾选图标，
// 比底部一闪而过的 toast 明显得多。点关闭或到点后收起，不再占版面。
func (a *app) autoSignAlert(c *ui.Context) {
	n := a.autoSignNotice
	if n == nil {
		return
	}
	feedback.Alert(c, n.title, feedback.AlertProps{
		Type:        feedback.Success,
		Variant:     feedback.Soft,
		Description: n.detail,
		Closed:      &n.closed,
	})
}
