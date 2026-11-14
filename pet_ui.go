package main

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// 桌面宠物的绘制：本体是一杆黄铜天平（透明窗口里画出来的），加上它说话的
// 气泡。
//
// 颜色不跟主题走：天平是个物件，浅色和深色桌面上都该是同一杆，跟着主题
// 变色会变成另一件东西。黄铜分三层——深色外沿保证浅色桌面上还有轮廓，
// 主体色，再加一道亮高光。气泡反过来，用的是当前主题的表面色与文字色，
// 和窗口里的其它浮层保持一致。

var (
	petBrassOutline = ui.RGB(0x53, 0x36, 0x18) // 外沿、吊绳、砝码轮廓
	petBrassDark    = ui.RGB(0x9A, 0x68, 0x2C) // 暗面：底座、托盘里层
	petBrass        = ui.RGB(0xD8, 0xA5, 0x50) // 主体黄铜
	petBrassLight   = ui.RGB(0xF7, 0xDE, 0xA6) // 高光
	petGem          = ui.RGB(0x74, 0xD6, 0xC4) // 支点上的青色宝石
)

// 天平的基准几何：坐标以 120×128 的窗口为准，窗口大小变了就整体缩放。
const (
	petPivotX = 60 // 支点
	petPivotY = 44
	petArm    = 40 // 横梁半长
	petRope   = 30 // 横梁端点到托盘口的垂距
	petPanW   = 30 // 托盘宽
	petPanH   = 9  // 托盘深
	petBaseY  = 116
)

// petView 是宠物窗口的 view：整块透明，只有画出来的那杆天平看得见，其余
// 部分是真正的桌面。
func (a *app) petView(c *ui.Context) {
	a.applyTheme(c)
	c.Root().Background(ui.Transparent)

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
			// 呼吸与摇摆都是慢动作：12 帧足够，不必跟着显示器刷新率空转。
			p.AnimationFrameRate(12)
			// 在每帧里读，不要在建视图时把值拷进闭包：AnimationFrameRate
			// 只重绘不重建视图，拷进来的未读数会一直停在旧值上。
			st := a.pet
			if st == nil {
				st = &petState{}
			}
			drawPet(p, r, st, p.Now(), a.unread)
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

// petPose 是某一帧的姿态。全部由 petMotion 从时间与状态算出来，绘制过程
// 不写任何状态：Draw 回调会被反复调用，在里面存「当前角度」会让动画跟着
// 重绘次数而不是时间走。
type petPose struct {
	angle float32 // 横梁倾角（度），正 = 右盘下沉
	bob   float32 // 整杆天平的轻微起伏，DIP
	glow  float32 // 支点宝石亮度，0..1
	drop  float32 // 新砝码落下的进度，0..1
}

// petMotion 算这一帧天平该怎么摆。两路驱动叠加：
//
//   - 时间：6 秒一轮、不到 1.2° 的微摆，加 4 秒一轮的上下呼吸，永远在动，
//     但安静到不会抢桌面的注意力；
//   - 状态：右盘放「未读消息」的砝码，越多重心越往右偏；来消息/说句话时
//     以 st.at 为起点叠一个阻尼振荡，像刚被放上一颗砝码那样先一沉再摆回。
//
// 事件只认 st.at（说一句话的时刻），不额外维护动画状态；气泡收起后振荡
// 自己衰减到零，不需要回调收尾。
func petMotion(now time.Time, st *petState, unread int) petPose {
	var pose petPose
	if now.IsZero() {
		pose.drop = 1 // 没有时钟时画一个静止、砝码已经落好的天平
		return pose
	}
	t := float64(now.UnixNano()) / float64(time.Second)
	pose.angle = float32(1.1 * math.Sin(t*2*math.Pi/6))
	pose.bob = float32(0.6 * math.Sin(t*2*math.Pi/4))
	pose.glow = float32(0.45 + 0.25*math.Sin(t*2*math.Pi/3))
	pose.drop = 1

	// 一颗配重加未读消息，未读越多右盘越沉，横梁就有一个稳态倾角。
	pose.angle += float32(petPanWeights(unread)-1) * 1.3

	if st == nil || st.at.IsZero() {
		return pose
	}
	d := now.Sub(st.at).Seconds()
	if d < 0 || d > 6 {
		return pose
	}
	pose.angle += float32(8.5 * math.Exp(-1.15*d) * math.Cos(6.2*d))
	if st.mood == petTalk {
		pose.glow = 1 // 说话时支点亮起来
	}
	// 新砝码 0.36 秒落进盘里，先快后慢。
	k := math.Min(1, d/0.36)
	pose.drop = float32(1 - (1-k)*(1-k))
	return pose
}

// petPanWeights 把未读数折成右盘的砝码数：一颗固定配重，加最多三颗消息
// 砝码。托盘只有 30 DIP 宽，第四颗以上挤不下，也没必要再表达「更多」。
func petPanWeights(unread int) int {
	if unread < 0 {
		unread = 0
	}
	if unread > 3 {
		unread = 3
	}
	return 1 + unread
}

// drawPet 画天平本体。所有活动件都从 pose.angle 推出来：横梁绕支点转，
// 吊绳挂在转后的端点上并带一点反向滞后，两只托盘始终水平（重力），砝码
// 坐在盘口。这样倾斜看着是「称东西」，不是整张图被旋转了一下。
func drawPet(p *ui.Painter, r ui.Rect, st *petState, now time.Time, unread int) {
	if p == nil || r.W < 40 || r.H < 40 {
		return
	}
	pose := petMotion(now, st, unread)

	s := min(r.W/petWindowW, r.H/petWindowH)
	ox := r.X + (r.W-float32(petWindowW)*s)/2
	oy := r.Y + r.H - float32(petWindowH)*s + pose.bob
	x := func(v float32) float32 { return ox + v*s }
	y := func(v float32) float32 { return oy + v*s }
	rect := func(x0, y0, w, h float32) ui.Rect {
		return ui.Rect{X: x(x0), Y: y(y0), W: w * s, H: h * s}
	}

	rad := float64(pose.angle) * math.Pi / 180
	cs, sn := float32(math.Cos(rad)), float32(math.Sin(rad))
	// 正角度 = 右盘下沉；横梁两端随支点旋转。
	lx, ly := petPivotX-petArm*cs, petPivotY-petArm*sn
	rx, ry := petPivotX+petArm*cs, petPivotY+petArm*sn

	// 斜线只能走 StrokePath：Painter.Line 只画水平/垂直线，两点都在动时
	// 会退化成框的一条直角边，吊绳就成了从天上垂下来的竖条。
	stroke := func(x0, y0, x1, y1, w float32, col ui.Color) {
		var path ui.Path
		path.MoveTo(x0, y0).LineTo(x1, y1)
		p.StrokePath(&path, w, col)
	}

	// 底座：深色梯形压在亮色内层下面，浅色桌面上也有轮廓。
	var base ui.Path
	base.MoveTo(x(38), y(petBaseY)).LineTo(x(82), y(petBaseY)).
		LineTo(x(88), y(126)).LineTo(x(32), y(126)).Close()
	p.FillPath(&base, petBrassOutline)
	base.Reset().
		MoveTo(x(39.5), y(petBaseY+1.5)).LineTo(x(80.5), y(petBaseY+1.5)).
		LineTo(x(85.5), y(124.5)).LineTo(x(34.5), y(124.5)).Close()
	p.FillPath(&base, petBrassDark)
	p.Line(x(36), y(petBaseY+1), x(84), y(petBaseY+1), 1.2*s, petBrassLight)

	// 立柱与它上面的三格刻度（中间那格是平衡点）。
	p.Fill(rect(56.5, petPivotY+2, 7, 70), petBrassOutline, 2*s)
	p.Fill(rect(57.5, petPivotY+2, 5, 70), petBrass, 1.5*s)
	p.Fill(rect(58.2, petPivotY+2, 1.2, 70), petBrassLight, 0.6*s)
	for _, k := range []float32{-5, 0, 5} {
		p.Fill(rect(petPivotX+k-0.8, 62, 1.6, 2), petBrassOutline, 0.8*s)
	}

	// 刀口三角：先画，横梁随后压住它的中段，看着才像横梁架在支点上。
	var fulcrum ui.Path
	fulcrum.MoveTo(x(60), y(31)).LineTo(x(51), y(54)).LineTo(x(69), y(54)).Close()
	p.FillPath(&fulcrum, petBrassOutline)
	fulcrum.Reset().MoveTo(x(60), y(33)).LineTo(x(53.5), y(52.5)).
		LineTo(x(66.5), y(52.5)).Close()
	p.FillPath(&fulcrum, petBrassDark)

	// 两只托盘。左盘永远一颗配重，右盘是配重加未读消息。
	pan := func(ex, ey float32, weights int, lastDrops bool) {
		// 吊绳滞后：横梁猛地一沉时，盘子先往反方向荡一点点，再回到垂线。
		lag := float64(-pose.angle) * 0.22 * math.Pi / 180
		px := ex + petRope*float32(math.Sin(lag))
		py := ey + petRope*float32(math.Cos(lag))

		rope := func(tx, ty float32) {
			stroke(x(ex), y(ey), x(tx), y(ty), 1.6*s, petBrassOutline)
			stroke(x(ex), y(ey), x(tx), y(ty), 0.7*s, petBrassLight)
		}
		rope(px-petPanW/2, py)
		rope(px+petPanW/2, py)

		// 砝码坐在盘口上；最新的那颗从上方落进来。
		total := float32(weights)*6 + float32(weights-1)*2
		start := px - total/2
		for i := 0; i < weights; i++ {
			off := float32(0)
			if lastDrops && weights > 1 && i == weights-1 {
				off = -14 * (1 - pose.drop)
			}
			bx, by := start+float32(i)*8, py-7.5+off
			p.Fill(rect(bx, by, 6, 7.5), petBrassOutline, 1.4*s)
			p.Fill(rect(bx+0.9, by+0.9, 4.2, 5.6), petBrassDark, 0.9*s)
			p.Fill(rect(bx+1.4, by+1.2, 3.2, 1), petBrassLight, 0.5*s)
		}

		// 弧碗：深色外碗 + 内缩的亮色内碗 + 口沿高光。
		var bowl ui.Path
		bowl.MoveTo(x(px-petPanW/2), y(py)).
			QuadTo(x(px), y(py+petPanH*1.9), x(px+petPanW/2), y(py)).Close()
		p.FillPath(&bowl, petBrassOutline)
		bowl.Reset().
			MoveTo(x(px-petPanW/2+1.6), y(py+0.4)).
			QuadTo(x(px), y(py+petPanH*1.5), x(px+petPanW/2-1.6), y(py+0.4)).Close()
		p.FillPath(&bowl, petBrass)
		p.Line(x(px-petPanW/2), y(py), x(px+petPanW/2), y(py), 1.2*s, petBrassLight)
	}
	pan(lx, ly, 1, false)
	pan(rx, ry, petPanWeights(unread), true)

	// 横梁：绕支点转的矩形，三层叠出外沿、主体和跟着转的高光。
	beam := func(half, thick, vOff float32, col ui.Color) {
		corners := [][2]float32{
			{-half, -thick/2 + vOff}, {half, -thick/2 + vOff},
			{half, thick/2 + vOff}, {-half, thick/2 + vOff},
		}
		var path ui.Path
		for i, pt := range corners {
			u := pt[0]*cs - pt[1]*sn
			v := pt[0]*sn + pt[1]*cs
			if i == 0 {
				path.MoveTo(x(petPivotX+u), y(petPivotY+v))
			} else {
				path.LineTo(x(petPivotX+u), y(petPivotY+v))
			}
		}
		path.Close()
		p.FillPath(&path, col)
	}
	beam(petArm+2, 6.5, 0, petBrassOutline)
	beam(petArm+2, 4.5, 0, petBrass)
	beam(petArm+1.5, 1.1, -1.5, petBrassLight)

	// 端点的挂环。
	for _, e := range [][2]float32{{lx, ly}, {rx, ry}} {
		p.Fill(rect(e[0]-3, e[1]-3, 6, 6), petBrassOutline, 3*s)
		p.Fill(rect(e[0]-1.8, e[1]-1.8, 3.6, 3.6), petBrassLight, 1.8*s)
	}

	// 指针固定在横梁上，随倾角在刻度前扫过。
	nx := petPivotX + 18*sn
	ny := petPivotY + 18*cs
	stroke(x(petPivotX), y(petPivotY+3), x(nx), y(ny+3), 1.8*s, petBrassOutline)
	stroke(x(petPivotX), y(petPivotY+3), x(nx), y(ny+3), 0.9*s, petGem)

	// 支点宝石：光晕随呼吸涨落，说话时最亮。
	halo := 4.5 + pose.glow*3
	p.Fill(rect(petPivotX-halo, petPivotY-halo, halo*2, halo*2),
		ui.RGBA(0x74, 0xD6, 0xC4, 0.10+0.16*pose.glow), halo*s)
	p.Fill(rect(petPivotX-3.5, petPivotY-3.5, 7, 7), petBrassOutline, 3.5*s)
	p.Fill(rect(petPivotX-2.2, petPivotY-2.2, 4.4, 4.4), petGem, 2.2*s)
}
