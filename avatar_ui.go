package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 「我的」资料头的换头像入口与气泡。数据、上传与状态回填在 avatar.go。

// openAvatarDialog 打开单选文件对话框。头像只支持 JPEG / PNG / WebP，上限
// 5 MB；这里把可能扣金币的说明写在对话框里，用户选图前就能看到。
func openAvatarDialog(parent *mygo.Window) ([]string, error) {
	return mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:      parent,
		Title:       "更换头像",
		ButtonLabel: "使用这张图片",
		Message:     "支持 JPEG / PNG / WebP；单张上限 5 MB。首次设置免费，之后每次可能扣除金币。",
		Filters: []mygo.FileFilter{{
			Name:      "图片",
			MIMETypes: []string{"image/jpeg", "image/png", "image/webp"},
		}},
		Multiple: false,
	})
}

// profileAvatarEntry 是资料头右侧的换头像图标：开着气泡时高亮，悬停给浅底。
// 只画图标，说明与可能扣费的提醒放在 tooltip 与气泡里。
func (a *app) profileAvatarEntry(c *ui.Context, ps *profileState) {
	// Lobe 的 ActionIcon：气泡开着时保持高亮，悬停给浅底；标签与悬浮说明
	// 分开写，前者供读屏与点击测试命中，后者说清可能扣费的后果。
	b := action.StyleActionIcon(c, ui.ButtonBase(c), lucide.ImagePlus(), action.ActionIconProps{
		Size: lobetheme.Small, GlyphSize: 15, Active: ps.avatarOpen,
	})
	b.Label("更换头像").Tooltip("更换头像；首次免费，之后可能扣除金币")
	b.OnClick(func() { a.openAvatarPopup(ps) })
	// Lobe 的气泡：点锚点开合、点窗外与 Esc 关闭，面板用 Lobe token。
	// 气泡挂在资料头图标下方，不遮住头像本身。
	lobeoverlay.Popover(c, b, &ps.avatarOpen, lobeoverlay.PopoverProps{
		Placement: lobeoverlay.Bottom,
	}, func() {
		a.profileAvatarPanel(c, ps)
	})
}

// profileAvatarPanel 是换头像气泡：说明格式与大小、选择本地图片、恢复默认。
// 上传期间只留进度，结果用 toast 报告；失败原因留在气泡里，方便原图重试。
func (a *app) profileAvatarPanel(c *ui.Context, ps *profileState) {
	ui.Column(c).Width(300).Gap(8).Children(func() {
		display.Text(c, "换一张头像", display.TextProps{
			Type: display.TextSecondary, FontSize: metaFontSize, Rows: 1,
		})
		if !a.auth.loggedIn() {
			a.loginHintFor(c, toastProfile, "登录后才能更换头像。")
			return
		}
		display.Text(c, "支持 JPEG / PNG / WebP，单张不超过 5 MB。首次设置免费，之后每次可能扣除金币。",
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 3})
		if ps.avatarErr != "" {
			feedback.Alert(c, ps.avatarErr, feedback.AlertProps{
				Type: feedback.Error, Variant: feedback.Soft,
			})
		}
		if ps.avatarBusy {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				feedback.Spin(c, feedback.SpinProps{Size: lobetheme.Small}, nil)
				display.Text(c, "正在上传头像…",
					display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
			})
			return
		}
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			if action.Button(c, "选择图片…", action.ButtonProps{
				Size: lobetheme.Small, Variant: action.Primary,
			}).Clicked() {
				a.pickAvatar(ps)
			}
			ui.Spacer(c).Grow(1)
			if ps.user.Avatar != "" {
				reset := action.Button(c, "重置为默认头像", action.ButtonProps{
					Size: lobetheme.Small, Variant: action.Text,
				})
				reset.TextColor(lobetheme.TokensOf(c).Error).
					Tooltip("恢复默认头像；可能扣除金币")
				if reset.Clicked() {
					a.resetAvatar(ps)
				}
			}
		})
	})
}
