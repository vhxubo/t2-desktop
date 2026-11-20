package main

import (
	"github.com/egoist/mygo/ui"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 键入 @ / : 弹出的候选气泡共用的精简外观。Lobe 的 Popover 默认是 12px
// 内边距 + 圆角 + 投影，套在只放几行候选的小列表上像一圈大边框；这里换成
// 1px 细边框、4px 内边距、极小圆角且不投影，只留一层清楚的分界。点窗外 /
// Esc 关闭仍交给 ui.PopoverBase，定位与 Lobe 的 attach 一致。
const (
	suggestPopoverPad float32 = 4
	suggestPopoverGap float32 = 6
)

// suggestPopover 在 anchor 的上/下方摆一个精简气泡，below 决定展开方向；
// 放不下时调用方已经算好，传 false 就向上。content 在面板内构建。
func suggestPopover(c *ui.Context, anchor ui.Element, open *bool, below bool, content func()) {
	ui.PopoverBase(c, anchor, open, func(panel ui.Element) {
		tk := lobetheme.TokensOf(c)
		if below {
			panel.AttachTo(anchor, ui.AnchorBottom, ui.AnchorTop).Margin(suggestPopoverGap, 0, 0, 0)
		} else {
			panel.AttachTo(anchor, ui.AnchorTop, ui.AnchorBottom).Margin(0, 0, suggestPopoverGap, 0)
		}
		panel.AttachedOrigin()
		panel.Padding(suggestPopoverPad).Radius(tk.BorderRadiusXS).
			Border(1, tk.BorderSecondary).Background(tk.BgElevated).
			TextColor(tk.Text).FontSize(tk.FontSize)
		panel.Children(content)
	})
}
