package main

import (
	"github.com/egoist/mygo/ui"
)

// 「属性雷达」标签页：站点给的六维属性画成雷达图，右侧是六项数值。属性
// 来自 GET /v1/users/:userName 的 user_attributes（和「我的 - 概览」同一
// 份数据），资料读到时就有了，不必再等 about 请求。
//
// 画布、属性顺序与满格值都复用 radar.go 的 radarChart 组件：两个窗口里
// 同样的六项属性必须长得一样，不能各画一套。

// userCardAttrsBody 是「属性雷达」标签页。四种状态各自说明，没数据时不画
// 一张全 0 的假图。
func (a *app) userCardAttrsBody(c *ui.Context, cs *userCardState) {
	t := c.Theme()
	switch {
	case cs.user.AttributesKnown:
		// 有数据，往下画图。
	case !cs.profileLoaded && cs.profileErr == "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			ui.Spinner(c).Size(20, 20)
			ui.Text(c, "正在读取属性…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	case cs.profileErr != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			ui.Text(c, "属性暂时不可用").FontSize(13).Bold()
			ui.Text(c, cs.profileErr).FontSize(12).TextColor(t.Danger).MaxLines(3)
		})
		return
	default:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(6).Children(func() {
			ui.Text(c, "这位用户没有公开属性。").FontSize(12).TextColor(t.TextMuted)
		})
		return
	}

	attrs := cs.user.Attributes
	ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			ui.Text(c, "六维属性").FontSize(12).Bold().SingleLine()
			ui.Row(c).FillWidth().Gap(14).AlignItems(ui.Center).Children(func() {
				// 与「我的 - 概览」同一个 radarChart 组件：轴标签、网格与
				// 数据多边形都走同一份绘制逻辑，深浅主题下颜色也一致。
				radarChart(c, attrs, profileAttributeRadarSize)
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					for _, attr := range attrs {
						profileAttributeRow(c, attr)
					}
				})
			})
			ui.Textf(c, "六维属性由服务端计算，单项上限 %d；雷达图按当前值绘制。",
				profileAttributeMax).FontSize(10).TextColor(t.TextMuted).MaxLines(2)
		})
	})
}
