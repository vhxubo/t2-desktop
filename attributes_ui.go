package main

import (
	"strconv"

	"github.com/egoist/mygo/ui"
)

// 「我的 - 概览」里的六维属性：左侧是雷达图，右侧是六项数值。雷达图本身封装
// 在 radar.go 的 radarChart 组件里（用 ui.Painter 直接画，颜色取自当前主题，
// 不依赖图片或第三方图表库），这里只负责把它和右侧列表摆进一块。
//
// 数据来自已经拉到的 GET /v1/users/:userName（user_attributes），属性块不
// 单独发请求。满格按 10 计，超过 10 的值在图上贴外圈，图例仍显示原值。

const (
	// profileAttributeRadarSize 是雷达图画布边长（DIP）。窗口默认 520、最小
	// 430 宽：减去左右内边距后，196 的图加 14 的间距正好放得下右侧列表。
	profileAttributeRadarSize float32 = 196
)

// profileAttributesSection 是概览页里的「六维属性」一块。资料还没读到时
// 说明正在读取；读到了但没有 user_attributes 时只说明暂无，不画一个全是 0
// 的假图。
func (a *app) profileAttributesSection(c *ui.Context, ps *profileState) {
	t := c.Theme()
	settingsSection(c, "六维属性", func() {
		switch {
		case ps.user.AttributesKnown:
			attrs := ps.user.Attributes
			ui.Row(c).FillWidth().Gap(14).AlignItems(ui.Center).Children(func() {
				radarChart(c, attrs, profileAttributeRadarSize)
				ui.Column(c).Grow(1).MinWidth(0).Gap(4).Children(func() {
					for _, attr := range attrs {
						profileAttributeRow(c, attr)
					}
				})
			})
			ui.Textf(c, "六维属性由服务端计算，单项上限 %d；雷达图按当前值绘制。",
				profileAttributeMax).FontSize(10).TextColor(t.TextMuted).MaxLines(2)
		case ps.infoLoading:
			ui.Text(c, "正在读取属性…").FontSize(11).TextColor(t.TextMuted)
		case ps.infoErr != "":
			ui.Text(c, "属性数据暂时不可用。").FontSize(11).TextColor(t.TextMuted)
		default:
			ui.Text(c, "当前账号没有返回属性数据。").FontSize(11).TextColor(t.TextMuted)
		}
	})
}

// profileAttributeRow 是右侧列表的一行：属性名、当前值，以及满格值。
func profileAttributeRow(c *ui.Context, attr profileAttribute) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, attr.Label).FontSize(11).TextColor(t.TextMuted).Width(32).SingleLine()
		ui.Text(c, strconv.Itoa(attr.Value)).FontSize(12).Bold().SingleLine()
		ui.Spacer(c).Grow(1)
		ui.Textf(c, "/%d", profileAttributeMax).FontSize(9).TextColor(t.TextMuted).SingleLine()
	})
}
