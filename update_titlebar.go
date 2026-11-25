package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 标题栏的更新按钮默认不露出：只有发现新版本、下载中、装完待重启时才出现一枚
// Lobe 风格的圆角图标按钮。下载中沿按钮边缘自绘一圈描边进度条，点它可以取消
// 本次下载；装完只提示「重启更新」，重启完全由用户点击触发。
const titleUpdateIconSize float32 = 28

func (a *app) titleBarUpdateButton(c *ui.Context) {
	s := a.updateStatus()
	if !titleUpdateVisible(s.Phase) {
		return
	}
	glyph, label, tip, primary, ring := titleUpdateLook(s)

	c.Key("title-update")
	b := action.ActionIcon(c, glyph, action.ActionIconProps{
		BlockSize: titleUpdateIconSize, GlyphSize: 15,
		Title: titleUpdateTip(tip, s.Version, s.Notes),
	})
	b.Label(label).Radius(titleUpdateIconSize / 2)
	tk := lobetheme.TokensOf(c)
	cancelable := s.Phase == updateDownloading
	switch {
	case primary:
		b.Background(tk.Primary).Border(1, tk.Primary).TextColor(tk.PrimaryForeground)
	case ring >= 0:
		// 进度环里图标用主色；下载中可点取消，给手型；安装中不接受点击。
		b.Background(ui.Transparent).Border(0, ui.Transparent).TextColor(tk.Primary)
		if !cancelable {
			b.Cursor(ui.CursorDefault)
		}
		b.Draw(func(p *ui.Painter, r ui.Rect) {
			drawUpdateRing(p, r, ring, tk.FillTertiary, tk.Primary)
		})
	}
	if b.Clicked() {
		switch {
		case cancelable:
			a.cancelUpdateDownload()
		case s.Phase == updateReady:
			a.restartForUpdate()
		case s.Phase == updateDetected:
			a.checkForUpdates()
		}
	}
}

// titleUpdateTip 把操作提示和该版本的更新日志拼成悬浮说明。清单里的日志可能
// 很长，截断后只留一段预览，免得一个提示把整块标题栏遮住。
func titleUpdateTip(tip, version, notes string) string {
	notes = strings.TrimSpace(notes)
	if notes == "" {
		return tip
	}
	const maxRunes = 320
	if r := []rune(notes); len(r) > maxRunes {
		notes = string(r[:maxRunes]) + "…"
	}
	return fmt.Sprintf("%s\n\n更新日志（%s）：\n%s", tip, version, notes)
}

// titleUpdateVisible 决定标题栏是否露出更新按钮。空闲、检查中与失败都不露出：
// 没有更新时不占标题栏，后台每轮检查也不会让按钮闪一下；手动检查仍在「关于」、
// 设置与命令面板里。
func titleUpdateVisible(p updatePhase) bool {
	switch p {
	case updateDetected, updateDownloading, updateInstalling, updateReady:
		return true
	}
	return false
}

// titleUpdateLook 给出各状态用的图标、无障碍名、悬浮说明、是否主色与进度值
// （负数表示不画进度环）。无障碍名同时供读屏与点击测试使用，真正画出来的只有
// 图标与下载时的进度环。
func titleUpdateLook(s updateStatus) (*ui.SVG, string, string, bool, float32) {
	switch s.Phase {
	case updateDetected:
		return lucide.ArrowDownToLine(), "有新版本",
			fmt.Sprintf("发现新版本 %s，点击查看并手动更新", s.Version), true, -1
	case updateDownloading:
		pct := fmt.Sprintf("下载中 %d%%", int(s.Percent))
		return lucide.Download(), pct, pct + "，点击取消下载", false, s.Percent
	case updateInstalling:
		return lucide.Download(), "安装中…", "正在安装更新…", false, 100
	case updateReady:
		return lucide.RefreshCw(), "重启更新", "重启以完成更新（不会自动重启）", true, -1
	default:
		return lucide.CircleAlert(), "更新失败", "更新失败，点击查看原因", false, -1
	}
}

// drawUpdateRing 自绘一圈贴着按钮边缘的描边进度条：底圈是未完成的轨道，亮色
// 圆弧从正上方顺时针走到百分比对应的角度。只负责画，状态由调用方传进来，回调
// 在一帧里可能跑多次，不在里面改状态。
func drawUpdateRing(p *ui.Painter, r ui.Rect, percent float32, track, fill ui.Color) {
	cx, cy := r.X+r.W/2, r.Y+r.H/2
	rad := min(r.W, r.H)/2 - 2
	const stroke float32 = 2
	var ring ui.Path
	p.StrokePath(ring.Circle(cx, cy, rad), stroke, track)
	shown := min(max(percent, 0), 100) / 100
	if shown <= 0 {
		return
	}
	var arc ui.Path
	steps := max(2, int(96*shown))
	for i := 0; i <= steps; i++ {
		a := float64(shown) * 2 * math.Pi * float64(i) / float64(steps)
		x := cx + rad*float32(math.Sin(a))
		y := cy - rad*float32(math.Cos(a))
		if i == 0 {
			arc.MoveTo(x, y)
		} else {
			arc.LineTo(x, y)
		}
	}
	p.StrokePath(&arc, stroke, fill)
}
