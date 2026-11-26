package main

import (
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 「我的」资料头的换头像入口与气泡。数据、裁剪与上传在 avatar.go 与
// avatar_crop.go。

// openAvatarDialog 打开单选文件对话框。头像只支持 JPEG / PNG / WebP，上限
// 5 MB；这里把「确定上传要消耗 500 金币」写在对话框里，用户选图前就能看到。
func openAvatarDialog(parent *mygo.Window) ([]string, error) {
	return mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:      parent,
		Title:       "更换头像",
		ButtonLabel: "使用这张图片",
		Message:     "支持 JPEG / PNG / WebP；单张上限 5 MB。选好后可以缩放、拖动裁剪；确定上传将消耗 500 金币。",
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
	b.Label("更换头像").Tooltip(fmt.Sprintf("更换头像；确定上传将消耗 %d 金币", avatarCost))
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
	ts := a.typeSizes()
	ui.Column(c).Width(300).Gap(8).Children(func() {
		display.Text(c, "换一张头像", display.TextProps{
			Type: display.TextSecondary, FontSize: ts.meta, Rows: 1,
		})
		if !a.auth.loggedIn() {
			a.loginHintFor(c, toastProfile, "登录后才能更换头像。")
			return
		}
		display.Text(c, fmt.Sprintf("支持 JPEG / PNG / WebP，单张不超过 5 MB。确定上传将消耗 %d 金币。", avatarCost),
			display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 3})
		if ps.avatarErr != "" {
			feedback.Alert(c, ps.avatarErr, feedback.AlertProps{
				Type: feedback.Error, Variant: feedback.Soft,
			})
		}
		if ps.avatarCrop.loading {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
				display.Text(c, "正在读取图片…",
					display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
			})
			return
		}
		if ps.avatarBusy {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
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

/* ------------------------------------------------------------------
   裁剪编辑器
---------------------------------------------------------------- */

// profileAvatarCrop 是选好图片后的裁剪框：中间一个圆形预览舞台，下面是
// 缩放滑杆、拖动说明与费用提示。点「上传」才真正发出请求（见 avatar_crop.go），
// 取消或点背景都只丢掉这次选择，不花金币。
func (a *app) profileAvatarCrop(c *ui.Context) {
	ps := a.profile
	if ps == nil || !ps.avatarCrop.open {
		return
	}
	crop := &ps.avatarCrop
	ok := feedback.Modal(c, &crop.open, feedback.ModalProps{
		Title:      "调整头像",
		Width:      360,
		OkText:     fmt.Sprintf("上传（%d 金币）", avatarCost),
		CancelText: "取消",
	}, func() {
		tk := lobetheme.TokensOf(c)
		ui.Column(c).FillWidth().Gap(10).Children(func() {
			// 费用放在最上面：最小窗口里即使内容要滚动，一打开也能看到。
			feedback.Alert(c, fmt.Sprintf("确定上传将消耗 %d 金币。", avatarCost), feedback.AlertProps{
				Type: feedback.Warning, Variant: feedback.Soft,
			})
			ui.Row(c).FillWidth().Justify(ui.Center).Children(func() {
				a.avatarCropStage(c, crop)
			})
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "缩小").FontSize(11).TextColor(tk.TextTertiary).Shrink(0)
				ui.Slider(c, &crop.zoom, avatarCropMinZoom, avatarCropMaxZoom).
					Grow(1).Label("缩放")
				ui.Text(c, "放大").FontSize(11).TextColor(tk.TextTertiary).Shrink(0)
			})

			if crop.err != "" {
				feedback.Alert(c, crop.err, feedback.AlertProps{
					Type: feedback.Error, Variant: feedback.Soft,
				})
			}
			if crop.busy {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
					display.Text(c, "正在上传头像…",
						display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
				})
			}
		})
	})
	if !crop.open {
		a.closeAvatarCrop(ps)
		return
	}
	if ok && !crop.busy {
		a.confirmAvatarCrop(ps)
	}
}

// avatarCropStage 画方形裁剪舞台：整张图按 zoom / pan 铺满舞台，裁剪框外
// 压一层半透明遮罩，四角带可拖动的手柄。框内拖动移动框，拖四边四角调整大小，
// 框外拖动平移图片，滚轮缩放（见 avatarCropState.input）。
func (a *app) avatarCropStage(c *ui.Context, crop *avatarCropState) {
	tk := lobetheme.TokensOf(c)
	crop.setZoom(crop.zoom)
	size := avatarCropStageSize
	stage := ui.Box(c).Size(size, size).
		Clip().Background(tk.FillTertiary).Label("头像裁剪区域").
		Cursor(ui.CursorCrosshair)
	stage.Draw(func(p *ui.Painter, r ui.Rect) {
		if crop.bmp == nil {
			return
		}
		// 图片按 cover 铺满舞台时会比舞台大，必须在这里裁到舞台内：
		// 元素自己的 Clip 只裁子元素，不让自绘溢出到上方的费用提示上。
		p.Clip(ui.Rect{X: r.X, Y: r.Y, W: size, H: size}, 0, func() {
			p.Image(crop.bmp, crop.drawRect(r.X, r.Y), ui.FillBox)
		})

		box := crop.cropRect()
		box.X += r.X
		box.Y += r.Y
		mask := tk.BgMask.Alpha(0.48)
		if top := box.Y - r.Y; top > 0 {
			p.Fill(ui.Rect{X: r.X, Y: r.Y, W: size, H: top}, mask, 0)
		}
		if bottom := r.Y + size - (box.Y + box.H); bottom > 0 {
			p.Fill(ui.Rect{X: r.X, Y: box.Y + box.H, W: size, H: bottom}, mask, 0)
		}
		if left := box.X - r.X; left > 0 {
			p.Fill(ui.Rect{X: r.X, Y: box.Y, W: left, H: box.H}, mask, 0)
		}
		if right := r.X + size - (box.X + box.W); right > 0 {
			p.Fill(ui.Rect{X: box.X + box.W, Y: box.Y, W: right, H: box.H}, mask, 0)
		}
		p.Stroke(box, tk.Primary, 0, 2)
		// 四角与四边中点的手柄：白底 + 主色描边，深浅图片上都看得清。
		const corner float32 = 9
		const edge float32 = 14
		for _, gx := range [2]float32{box.X, box.X + box.W} {
			for _, gy := range [2]float32{box.Y, box.Y + box.H} {
				h := ui.Rect{X: gx - corner/2, Y: gy - corner/2, W: corner, H: corner}
				p.Fill(h, tk.White, 1)
				p.Stroke(h, tk.Primary, 1, 1)
			}
		}
		midX, midY := box.X+box.W/2, box.Y+box.H/2
		for _, gy := range [2]float32{box.Y, box.Y + box.H} {
			h := ui.Rect{X: midX - edge/2, Y: gy - 2.5, W: edge, H: 5}
			p.Fill(h, tk.White, 2)
			p.Stroke(h, tk.Primary, 2, 1)
		}
		for _, gx := range [2]float32{box.X, box.X + box.W} {
			h := ui.Rect{X: gx - 2.5, Y: midY - edge/2, W: 5, H: edge}
			p.Fill(h, tk.White, 2)
			p.Stroke(h, tk.Primary, 2, 1)
		}
	})
	// Ctrl+滚轮缩放整页，裁剪框自己的滚轮 / 双指缩放保持原样。
	stage.HandleInput(func(ev ui.InputEvent) bool {
		return a.zoomInput(ev) || crop.input(ev)
	})
}
