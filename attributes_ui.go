package main

import (
	"math"
	"strconv"

	"github.com/egoist/mygo/ui"
)

// 「我的 - 概览」里的六维属性：左侧是雷达图，右侧是六项数值。雷达图用
// ui.Painter 直接画，不依赖图片或第三方图表库；颜色全部取自当前主题，
// 浅色与深色外观下都不用另做一套素材。
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
				a.profileAttributeRadar(c, attrs)
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

// profileAttributeRadar 是雷达图的画布。Box 本身不显示任何内容，图像由
// DrawOver 的 Painter 每帧直接绘制；圆点、网格与数据多边形都随主题变化。
func (a *app) profileAttributeRadar(c *ui.Context, attrs []profileAttribute) {
	ui.Box(c).Size(profileAttributeRadarSize, profileAttributeRadarSize).Shrink(0).
		Label("六维属性雷达图").Tooltip("六维属性雷达图：越靠外表示数值越高").
		DrawOver(func(p *ui.Painter, r ui.Rect) {
			a.drawProfileAttributeRadar(p, r, attrs)
		})
}

// drawProfileAttributeRadar 画雷达图：5 圈网格、6 条轴、数据多边形与顶点。
// 轴标签用 Painter 画在顶点外侧；右侧列表已经给出可读的数字，所以这里只画
// 属性名，给图形留出空间。
func (a *app) drawProfileAttributeRadar(p *ui.Painter, r ui.Rect, attrs []profileAttribute) {
	if p == nil || len(attrs) < 3 || r.W <= 0 || r.H <= 0 {
		return
	}
	t := p.Theme()
	n := len(attrs)
	size := min(r.W, r.H)
	radius := size * 0.34
	cx, cy := r.X+r.W/2, r.Y+r.H/2

	// 网格：从 1/5 到满格共 5 圈。
	for step := 1; step <= 5; step++ {
		ringRadius := radius * float32(step) / 5
		ring := &ui.Path{}
		for i := 0; i < n; i++ {
			x, y := profileAttributePoint(cx, cy, ringRadius, i, n)
			if i == 0 {
				ring.MoveTo(x, y)
			} else {
				ring.LineTo(x, y)
			}
		}
		p.StrokePath(ring.Close(), 1, t.Border)
	}

	// 从圆心到每个顶点的轴。
	for i := 0; i < n; i++ {
		x, y := profileAttributePoint(cx, cy, radius, i, n)
		p.Line(cx, cy, x, y, 1, t.Border)
	}

	// 数据多边形：先填一层半透明强调色，再描边，数值越大多边形越大。
	data := &ui.Path{}
	points := make([][2]float32, n)
	for i, attr := range attrs {
		level := profileAttributeLevel(attr.Value)
		x, y := profileAttributePoint(cx, cy, radius*level, i, n)
		points[i] = [2]float32{x, y}
		if i == 0 {
			data.MoveTo(x, y)
		} else {
			data.LineTo(x, y)
		}
	}
	data.Close()
	p.FillPath(data, ui.RGBA(t.Accent.R, t.Accent.G, t.Accent.B, 0.18))
	p.StrokePath(data, 2, t.Accent)

	// 每个数据顶点画一个小圆，数值为 0 时也落在圆心附近，能看出这一项低。
	for _, pt := range points {
		const dot = 2.5
		p.Fill(ui.Rect{X: pt[0] - dot, Y: pt[1] - dot, W: dot * 2, H: dot * 2},
			t.Accent, dot)
	}

	// 轴标签：沿轴再往外 15 DIP，按文字宽度水平居中。
	for i, attr := range attrs {
		x, y := profileAttributePoint(cx, cy, radius+15, i, n)
		const fontSize float32 = 10
		width := profileAttributeTextWidth(attr.Label, fontSize)
		p.Text(x-width/2, y-fontSize/2, attr.Label, fontSize, t.Text)
	}
}

// profileAttributePoint 返回第 i 条轴、给定半径处的点。第一条轴朝正上方，
// 其余按顺时针均分，读起来和常见的雷达图一致。
func profileAttributePoint(cx, cy, radius float32, i, n int) (float32, float32) {
	angle := -math.Pi/2 + 2*math.Pi*float64(i)/float64(n)
	return cx + radius*float32(math.Cos(angle)), cy + radius*float32(math.Sin(angle))
}

// profileAttributeTextWidth 估算一行文字在给定字号下的宽度。Painter.Text
// 不返回测量结果，雷达图的轴标签只有两三个汉字，按全角一个字宽、半角六成
// 字宽估算足以居中，也不会把标签挤进图里。
func profileAttributeTextWidth(s string, size float32) float32 {
	var width float32
	for _, r := range s {
		if r > 0x7f {
			width += size
		} else {
			width += size * 0.6
		}
	}
	return width
}
