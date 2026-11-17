package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
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
	t := c.Theme()
	b := ui.Box(c).Size(26, 26).Radius(6).Center().Background(ui.Transparent).Shrink(0)
	if ps.avatarOpen || b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	ink := t.TextMuted
	if ps.avatarOpen || b.Hovered() {
		ink = t.Text
	}
	b.Children(func() {
		ui.Icon(c, imageIcon).Size(15, 15).TextColor(ink)
	})
	b.Label("更换头像").Tooltip("更换头像；首次免费，之后可能扣除金币")
	b.OnClick(func() { a.openAvatarPopup(ps) })
	ui.Popover(c, b, &ps.avatarOpen, func() {
		a.profileAvatarPanel(c, ps)
	})
}

// profileAvatarPanel 是换头像气泡：说明格式与大小、选择本地图片、恢复默认。
// 上传期间只留进度，结果用 toast 报告；失败原因留在气泡里，方便原图重试。
func (a *app) profileAvatarPanel(c *ui.Context, ps *profileState) {
	t := c.Theme()
	ui.Column(c).Width(300).Gap(8).Children(func() {
		ui.Text(c, "换一张头像").FontSize(metaFontSize).TextColor(t.TextMuted)
		if !a.auth.loggedIn() {
			a.loginHintFor(c, toastProfile, "登录后才能更换头像。")
			return
		}
		ui.Text(c, "支持 JPEG / PNG / WebP，单张不超过 5 MB。首次设置免费，之后每次可能扣除金币。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(3)
		if ps.avatarErr != "" {
			ui.Text(c, ps.avatarErr).FontSize(11).TextColor(t.Danger).MaxLines(2)
		}
		if ps.avatarBusy {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(13, 13)
				ui.Text(c, "正在上传头像…").FontSize(11).TextColor(t.TextMuted)
			})
			return
		}
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c, "选择图片…").Clicked() {
				a.pickAvatar(ps)
			}
			ui.Spacer(c).Grow(1)
			if ps.user.Avatar != "" {
				reset := ui.Button(c, "重置为默认头像").
					Background(ui.Transparent).TextColor(t.TextMuted).
					FontSize(11).Padding(2, 6).Radius(6)
				if reset.Hovered() {
					reset.Background(t.SurfaceHover).TextColor(t.Danger)
				}
				reset.Tooltip("恢复默认头像；可能扣除金币")
				if reset.Clicked() {
					a.resetAvatar(ps)
				}
			}
		})
	})
}
