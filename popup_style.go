package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 弹窗窗口里的卡片与列表行统一走平面表面：保留 Lobe Block 的悬停底色与
// 过渡，但去掉组件默认的圆角和整圈描边。需要把相邻区块分开时，调用方再挂
// 一条底部分隔线（BorderWidth(0, 0, 1, 0) + BorderColor），与主窗口的扁平
// 列表行、通知行同一套视觉，不再用圆角描边卡片把内容一格格框起来。

// popupSurface 是弹窗窗口区块的平面表面。clickable 决定它是否是一张可点
// 的按钮；两者都没有圆角与整圈描边。
func popupSurface(c *ui.Context, clickable bool) ui.Element {
	return layout.Block(c, layout.BlockProps{
		Clickable: clickable, Variant: layout.BlockBorderless,
	}).Radius(0)
}

// popupRow 是弹窗窗口里的一条扁平列表行：平面表面加一条底部分隔线。
func popupRow(c *ui.Context, clickable bool) ui.Element {
	tk := lobetheme.TokensOf(c)
	return popupSurface(c, clickable).
		BorderWidth(0, 0, 1, 0).BorderColor(tk.BorderSecondary)
}
