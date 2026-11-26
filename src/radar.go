package main

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// radarChart 把属性雷达图封装成一个独立、可复用的组件。它不再依赖 *app，
// 只接收一组已按展示顺序排列的轴（首条轴在顶部、其余顺时针排布）与画布边长，
// 返回一个 ui.Element。网格、轴、数据多边形与顶点全部由 Painter 直接绘制，
// 颜色取自当前主题，浅色与深色外观都不用另做素材，因此属性页与以后任何需要
// 雷达图的地方都能复用同一份绘制逻辑。
//
// 调用方负责把轴按「先上、后顺时针」的顺序排好再传入；本组件不关心数据来源，
// 也不负责把数值换算到 0～满格——那一步在 profileAttributeLevel 里完成。
func radarChart(c *ui.Context, axes []profileAttribute, size float32) ui.Element {
	return ui.Box(c).Size(size, size).Shrink(0).
		Label("六维属性雷达图").Tooltip("六维属性雷达图：越靠外表示数值越高").
		DrawOver(func(p *ui.Painter, r ui.Rect) {
			drawRadarChart(p, r, axes)
		})
}

// drawRadarChart 画雷达图：5 圈网格、n 条轴、数据多边形与顶点。axis 数 < 3
// 时不绘制（退化成点或线没有意义）。所有颜色取自主题，文字只用于轴标签，
// 右侧列表已经给出可读的数字，所以这里只画属性名，给图形留出空间。
func drawRadarChart(p *ui.Painter, r ui.Rect, axes []profileAttribute) {
	if p == nil || len(axes) < 3 || r.W <= 0 || r.H <= 0 {
		return
	}
	t := p.Theme()
	n := len(axes)
	size := min(r.W, r.H)
	radius := size * 0.34
	cx, cy := r.X+r.W/2, r.Y+r.H/2

	// 网格：从 1/5 到满格共 5 圈。
	for step := 1; step <= 5; step++ {
		ringRadius := radius * float32(step) / 5
		ring := &ui.Path{}
		for i := 0; i < n; i++ {
			x, y := radarPoint(cx, cy, ringRadius, i, n)
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
		x, y := radarPoint(cx, cy, radius, i, n)
		p.Line(cx, cy, x, y, 1, t.Border)
	}

	// 数据多边形：先填一层半透明强调色，再描边，数值越大多边形越大。
	data := &ui.Path{}
	points := make([][2]float32, n)
	for i, axis := range axes {
		level := profileAttributeLevel(axis.Value)
		x, y := radarPoint(cx, cy, radius*level, i, n)
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
	for i, axis := range axes {
		x, y := radarPoint(cx, cy, radius+15, i, n)
		const fontSize float32 = 10
		width := radarTextWidth(axis.Label, fontSize)
		p.Text(x-width/2, y-fontSize/2, axis.Label, fontSize, t.Text)
	}
}

// radarPoint 返回第 i 条轴、给定半径处的点。第一条轴朝正上方，其余按顺时针
// 均分，读起来和常见的雷达图一致。
func radarPoint(cx, cy, radius float32, i, n int) (float32, float32) {
	angle := -math.Pi/2 + 2*math.Pi*float64(i)/float64(n)
	return cx + radius*float32(math.Cos(angle)), cy + radius*float32(math.Sin(angle))
}

// radarTextWidth 估算一行文字在给定字号下的宽度。Painter.Text 不返回测量
// 结果，雷达图的轴标签只有两三个汉字，按全角一个字宽、半角六成字宽估算足以
// 居中，也不会把标签挤进图里。
func radarTextWidth(s string, size float32) float32 {
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
