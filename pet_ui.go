package main

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// 桌面宠物的绘制：宠物本体（透明窗口里画出来的一只小东西）与它说话的气泡。
//
// 颜色不跟主题走：宠物是个角色，浅色和深色桌面上都该是同一只，跟着主题变色
// 会变成另一只生物。气泡反过来，用的是当前主题的表面色与文字色，和窗口里的
// 其它浮层保持一致。

var (
	petBodyColor   = ui.RGB(0xF5, 0xB3, 0x6B) // 身体
	petRimColor    = ui.RGB(0xD0, 0x8A, 0x3F) // 身体外沿那一圈深色
	petMuzzleColor = ui.RGB(0xFF, 0xE9, 0xCC) // 嘴周围浅色的一小块
	petInkColor    = ui.RGB(0x4A, 0x33, 0x20) // 眼睛、嘴
	petCheekColor  = ui.RGBA(0xE8, 0x79, 0x5F, 0.38)
)

// petView 是宠物窗口的 view：整块透明，只有画出来的那团东西看得见，其余
// 部分是真正的桌面。
func (a *app) petView(c *ui.Context) {
	a.applyTheme(c)
	c.Root().Background(ui.Transparent)

	st := a.pet
	if st == nil {
		st = &petState{}
	}
	ui.Box(c).Fill().
		Label("桌面宠物").
		Tooltip("拖动可以换位置，右键有菜单").
		DragWindow().
		ContextMenu(func(m *ui.Menu) {
			if m.Item("打开主窗口").Chosen() {
				a.showMainWindow()
			}
			if m.Item("说句话").Chosen() {
				a.petGreet()
			}
			m.Separator()
			if m.Item("收起桌面宠物").Chosen() {
				a.setPetEnabled(false)
			}
		}).
		Draw(func(p *ui.Painter, r ui.Rect) {
			// 呼吸是慢动作：12 帧足够，不必跟着显示器刷新率空转。
			p.AnimationFrameRate(12)
			drawPet(p, r, st, p.Now())
		})
}

// petBubbleView 是气泡窗口的 view。气泡是 flyout，窗口本身透明，所以整块
// 圆角与下面那个小尖角都自己画（见 drawPetBubble）。
func (a *app) petBubbleView(c *ui.Context) {
	a.applyTheme(c)
	t := c.Theme()
	c.Root().Background(ui.Transparent)

	st := a.pet
	if st == nil {
		return
	}
	hint := ""
	if st.target != "" {
		hint = "点开看看"
	}
	ui.Column(c).Padding(10, 12, 10+petBubbleTail, 12).Gap(3).
		Cursor(ui.CursorPointer).
		Draw(func(p *ui.Painter, r ui.Rect) { drawPetBubble(p, r, t.Surface) }).
		OnClick(a.openPetBubbleTarget).
		Children(func() {
			if st.title != "" {
				ui.Text(c, st.title).FontSize(12).Bold().TextColor(t.Accent).MaxLines(2)
			}
			ui.Text(c, st.line).FontSize(13).TextColor(t.Text).MaxLines(4)
			if hint != "" {
				ui.Text(c, hint).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
}

// drawPetBubble 画气泡：一块圆角矩形加一个指向宠物的小尖角。边框不画，交给
// flyout 的系统阴影，深浅两套桌面都分得清边界。
func drawPetBubble(p *ui.Painter, r ui.Rect, surface ui.Color) {
	if p == nil || r.W <= 0 || r.H <= petBubbleTail {
		return
	}
	body := ui.Rect{X: r.X, Y: r.Y, W: r.W, H: r.H - petBubbleTail}
	p.Fill(body, surface, 12)
	// 尖角与矩形同色，压着底边画，接缝看不出来。
	cx := r.X + r.W/2
	var tail ui.Path
	tail.MoveTo(cx-7, body.Y+body.H-1).
		LineTo(cx, body.Y+body.H+petBubbleTail-1).
		LineTo(cx+7, body.Y+body.H-1).
		Close()
	p.FillPath(&tail, surface)
}

// drawPet 画宠物本体。坐标以 120×128 的窗口为基准，窗口大小变了就整体缩放，
// 这样换个尺寸不用重算一堆数字。
//
// 动画只有两样：四秒一个来回的轻微起伏（呼吸）与每四秒一次的眨眼。都是纯
// 绘制，不改布局，所以能走 AnimationFrameRate 的重绘而不是重建视图。
func drawPet(p *ui.Painter, r ui.Rect, st *petState, now time.Time) {
	if p == nil || r.W < 40 || r.H < 40 {
		return
	}
	bob, blink := float32(0), false
	if !now.IsZero() {
		sec := math.Mod(float64(now.UnixNano())/float64(time.Second), 4)
		if sec < 0 {
			sec += 4
		}
		bob = float32(math.Sin(sec/2*math.Pi)) * 1.5
		blink = sec > 3.55 && sec < 3.68
	}

	s := min(r.W/petWindowW, r.H/petWindowH)
	ox := r.X + (r.W-float32(petWindowW)*s)/2
	oy := r.Y + r.H - float32(petWindowH)*s + bob
	x := func(v float32) float32 { return ox + v*s }
	y := func(v float32) float32 { return oy + v*s }
	rect := func(x0, y0, w, h float32) ui.Rect {
		return ui.Rect{X: x(x0), Y: y(y0), W: w * s, H: h * s}
	}

	// 耳朵在身体后面：先画，身体压住耳根。三个点按顺序是「外底、耳尖、内底」。
	// 先画深色的一圈，再往重心缩一点画身体色，耳朵才和身体是同一个质感，
	// 而不是两块贴上去的尖三角。
	ear := func(ax, ay, bx, by, cx, cy float32) {
		mx, my := (ax+bx+cx)/3, (ay+by+cy)/3
		draw := func(k float32, col ui.Color) {
			shrink := func(v, m float32) float32 { return v + (m-v)*k }
			var tri ui.Path
			tri.MoveTo(x(shrink(ax, mx)), y(shrink(ay, my))).
				LineTo(x(shrink(bx, mx)), y(shrink(by, my))).
				LineTo(x(shrink(cx, mx)), y(shrink(cy, my))).
				Close()
			p.FillPath(&tri, col)
		}
		draw(0, petRimColor)
		draw(0.24, petBodyColor)
	}
	ear(34, 52, 36, 26, 54, 44) // 左耳
	ear(86, 52, 84, 26, 66, 44) // 右耳

	// 脚先画，再被身体压住上半截，露出来的那一点才像脚。
	p.Fill(rect(34, 102, 18, 12), petRimColor, 6*s)
	p.Fill(rect(68, 102, 18, 12), petRimColor, 6*s)

	// 身体：先画一圈深色，再画浅色身体，得到 1 DIP 的外沿。
	p.Fill(rect(21, 43, 78, 68), petRimColor, 31*s)
	p.Fill(rect(22, 44, 76, 66), petBodyColor, 30*s)

	// 嘴周围浅色的一块，让表情有个落点。
	p.Fill(rect(46, 78, 28, 20), petMuzzleColor, 10*s)

	// 腮红。
	p.Fill(rect(30, 80, 13, 9), petCheekColor, 4.5*s)
	p.Fill(rect(77, 80, 13, 9), petCheekColor, 4.5*s)

	// 眼睛：眨眼时压成一条线。
	for _, ex := range []float32{48, 72} {
		if blink {
			p.Line(x(ex-3.4), y(72), x(ex+3.4), y(72), 2*s, petInkColor)
			continue
		}
		p.Fill(rect(ex-3.8, 68.2, 7.6, 7.6), petInkColor, 3.8*s)
	}

	// 嘴：发呆时是一条弯的线，说话时张开成一个小圆。
	if st != nil && st.mood == petTalk {
		p.Fill(rect(56.5, 87, 7.5, 6.5), petInkColor, 3.2*s)
		return
	}
	var mouth ui.Path
	mouth.MoveTo(x(55), y(87)).QuadTo(x(60), y(92), x(65), y(87))
	p.StrokePath(&mouth, 1.8*s, petInkColor)
}
