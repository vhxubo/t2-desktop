package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 标题栏的更新按钮。空闲时是普通「检查更新」，有版本或已下载时换成主色按钮，
// 下载中显示进度条；装完只提示「重启更新」，重启完全由用户点击触发。
const titleUpdateButtonWidth = 104

func (a *app) titleBarUpdateButton(c *ui.Context) {
	s := a.updateStatus()
	if s.Phase == updateDownloading || s.Phase == updateInstalling {
		label := fmt.Sprintf("下载中 %d%%", int(s.Percent))
		if s.Phase == updateInstalling {
			label = "安装中…"
		}
		ui.Row(c).Width(titleUpdateButtonWidth + 36).Height(titleBarHeight).Shrink(0).
			AlignItems(ui.Center).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, label).SingleLine().FontSize(11)
				feedback.Progress(c, s.Percent, feedback.ProgressProps{
					Variant: feedback.ProgressInset, Size: lobetheme.Small,
					Status: feedback.ProgressActive,
				})
			})
		})
		return
	}

	label, tip := "检查更新", "检查更新"
	variant, disabled := action.Default, false
	onClick := func() { a.checkForUpdates() }
	switch s.Phase {
	case updateChecking:
		label, tip, disabled = "检查中…", "正在检查更新", true
		onClick = func() {}
	case updateDetected:
		label, tip, variant = "有新版本", "查看并手动更新", action.Primary
	case updateReady:
		label, tip, variant = "重启更新", "重启以完成更新（不会自动重启）", action.Primary
		onClick = func() { a.restartForUpdate() }
	case updateFailed:
		label, tip = "更新失败", "点击查看更新详情"
	}
	b := action.Button(c, label, action.ButtonProps{
		Size: lobetheme.Middle, Variant: variant,
	})
	b.Label(label).Tooltip(tip).Width(titleUpdateButtonWidth).Shrink(0).Disabled(disabled)
	if b.Clicked() && !disabled {
		onClick()
	}
}
