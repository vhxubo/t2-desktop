package main

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

/* ------------------------------------------------------------------
   一键重置：把设置窗口里所有可改的配置写回默认值
---------------------------------------------------------------- */

// resetSettingsSection 是「通用」页末尾的一块：所有设置项一键回到默认值。
// 按钮只打开 Lobe 气泡，真正执行要再点一次「确认重置」；这里只动设置，
// 登录态、草稿、阅读记录等本机数据不碰。
func (a *app) resetSettingsSection(c *ui.Context) {
	settingsSection(c, "重置", func() {
		settingsDescription(c, "只重置设置，不影响登录账号与本机数据。", 1)
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			btn := action.Button(c, "一键重置所有设置", action.ButtonProps{})
			btn.Label("一键重置所有设置")
			lobeoverlay.Popover(c, btn, &a.resetConfirmOpen, lobeoverlay.PopoverProps{
				Placement: lobeoverlay.Top,
			}, func() {
				a.resetConfirmPanel(c)
			})
			ui.Spacer(c).Grow(1)
		})
	})
}

// resetConfirmPanel 是重置的二次确认气泡：说清后果，点「确认重置」才执行。
func (a *app) resetConfirmPanel(c *ui.Context) {
	ui.Column(c).Width(240).Gap(10).Children(func() {
		display.Text(c, "确定恢复所有设置的默认值？", display.TextProps{
			Strong: true, FontSize: 12, Rows: 2,
		})
		display.Text(c, "登录账号、草稿与本机数据不受影响。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 2})
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c).Grow(1)
			if action.Button(c, "取消", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.resetConfirmOpen = false
			}
			if action.Button(c, "确认重置", action.ButtonProps{
				Size: lobetheme.Small, Variant: action.Primary,
			}).Clicked() {
				a.resetConfirmOpen = false
				a.resetAllSettings()
			}
		})
	})
}

// resetAllSettings 把设置窗口能改的每一份配置恢复为默认值并落盘。各 store 的
// Reset 负责写入，这里的副作用让正在运行的界面与连接按新值切换；最后用一条
// toast 交代结果。只在 UI 线程调用（按钮回调 / publish）。
func (a *app) resetAllSettings() {
	var errs []string
	fail := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}

	if a.proxy != nil {
		fail(a.proxy.Reset())
	}
	if a.theme != nil {
		fail(a.theme.Reset())
	}
	if a.closeMode != nil {
		fail(a.closeMode.Reset())
	}
	if a.updateMirror != nil {
		fail(a.updateMirror.Reset())
	}
	if a.commentCostPrefs != nil {
		fail(a.commentCostPrefs.Reset())
	}
	if a.imagePrefs != nil {
		fail(a.imagePrefs.Reset())
	}
	if a.watchLaterPrefs != nil {
		fail(a.watchLaterPrefs.Reset())
	}
	if a.notificationPrefs != nil {
		fail(a.notificationPrefs.Reset())
	}
	fail(a.restoreImageHosts())
	if a.commands != nil && a.commands.prefs != nil {
		fail(a.commands.prefs.resetAll())
	}

	// 草稿与瞬时状态也跟回默认值，已经开着的页面下一帧就显示默认配置。
	if a.proxy != nil {
		a.reloadProxyDraft()
	}
	a.syncNotifyDrafts()
	a.revealedImages = nil
	a.stopShortcutRecording()
	a.shortcutConflict = ""

	// 运行时状态跟着偏好切换：撤掉更新镜像、重连通知流、清掉默认模式里
	// 不再有意义的已读标记。
	applyUpdateMirror("")
	a.applyNotificationStreamPref()
	a.clearWatchLaterReadMarks()
	a.redrawWatchLaterPage()
	a.publish(func() {})

	if len(errs) > 0 {
		a.toast(toastSettings, "部分设置重置失败："+strings.Join(errs, "；"))
		return
	}
	a.toast(toastSettings, "已重置所有设置为默认值")
}
