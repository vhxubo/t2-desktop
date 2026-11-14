package main

import (
	"image"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 宠物窗口是透明窗口里自己画出来的一杆天平，所以它的内容只能从像素上验：
// 先确认画布确实有东西（不是一块空透明窗口），再确认四角是透明的（否则窗口
// 会盖住桌面上一个方框）。
func TestPetViewDrawsOnATransparentWindow(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.petView, petWindowW, petWindowH)
	tt.Frame()

	img := tt.Image()
	if img == nil {
		t.Fatal("没有渲染出画布")
	}
	opaque := 0
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, al := img.At(x, y).RGBA(); al > 0 {
				opaque++
			}
		}
	}
	// 底座、立柱、横梁、两只托盘加起来占画布一成多，给一个宽松的下限：
	// 画歪了、画没了都会掉到这以下。
	if opaque < b.Dx()*b.Dy()/10 {
		t.Errorf("宠物窗口里只有 %d 个不透明像素，几乎什么都没画", opaque)
	}
	if _, _, _, al := img.At(b.Min.X, b.Min.Y).RGBA(); al != 0 {
		t.Errorf("左上角不是透明的（alpha=%d），窗口会盖住桌面", al)
	}
}

// 通知到达时宠物以气泡说一句：标题是「谁做了什么」，正文是帖子标题或评论
// 摘要，底部留一个「点开看看」的入口。
func TestPetSaysNotificationInBubble(t *testing.T) {
	a := testApp(t)
	a.setPetEnabled(true)
	if !a.petShown {
		t.Fatal("打开桌面宠物后 petShown 应为 true")
	}

	a.petAnnounce([]nativeNotice{{
		ID:     "n1",
		Title:  "@小明 回复了你",
		Body:   "关于 Go 桌面开发的那篇帖子",
		Target: "https://2libra.com/post/x/abc",
	}})
	if a.pet == nil || !a.pet.open {
		t.Fatal("通知到达后没有冒出气泡")
	}

	tt := ui.NewTester(a.petBubbleView, petBubbleW, 120)
	tt.Frame()
	for _, want := range []string{"@小明 回复了你", "关于 Go 桌面开发的那篇帖子", "点开看看"} {
		if !tt.HasText(want) {
			t.Errorf("气泡里缺少 %q：%q", want, tt.Texts())
		}
	}
	// 气泡不抢键盘：它是通知，弹出来不能打断用户正在写的东西。
	if a.petBubble != nil {
		t.Error("无头测试里不该真的建出气泡窗口")
	}
}

// 一次来好几条消息时只说最后一条，其余的折成一句「另有 N 条」：气泡不是
// 列表，一口气念三条谁也读不完。
func TestPetAnnounceCollapsesManyNotifications(t *testing.T) {
	a := testApp(t)
	a.setPetEnabled(true)
	a.petAnnounce([]nativeNotice{
		{ID: "n1", Title: "@甲 回复了你", Body: "第一条"},
		{ID: "n2", Title: "@乙 提到了你", Body: "第二条"},
		{ID: "n3", Title: "@丙 赞了你", Body: "第三条"},
	})
	if a.pet == nil {
		t.Fatal("没有生成气泡状态")
	}
	if a.pet.line != "第三条" {
		t.Errorf("气泡正文是 %q，期望只说最后一条「第三条」", a.pet.line)
	}
	if a.pet.title != "@丙 赞了你，另有 2 条" {
		t.Errorf("气泡标题是 %q，期望把其余条数折进去", a.pet.title)
	}
}

// 宠物没打开时来消息不该留下一个过期气泡：等用户再打开宠物，看到的应该是
// 一张干净的脸，而不是十分钟前的通知。
func TestPetStaysQuietWhenHidden(t *testing.T) {
	a := testApp(t)
	a.petAnnounce([]nativeNotice{{ID: "n1", Title: "@小明 回复了你", Body: "在吗"}})
	if a.pet != nil && a.pet.open {
		t.Fatal("宠物没打开却冒出了气泡")
	}
}

// 气泡到点自己收起，宠物回到发呆。收起走的是真实定时器，所以靠钩子等它。
func TestPetBubbleHidesItself(t *testing.T) {
	a := testApp(t)
	a.setPetEnabled(true)

	old := petBubbleShowFor
	petBubbleShowFor = time.Millisecond
	defer func() { petBubbleShowFor = old }()

	hidden := make(chan struct{}, 1)
	a.onPetBubbleHidden = func() { hidden <- struct{}{} }

	a.sayPet("@小明 回复了你", "在吗", "")
	select {
	case <-hidden:
	case <-time.After(2 * time.Second):
		t.Fatal("气泡到点没有自己收起")
	}
	if a.pet == nil || a.pet.open {
		t.Error("气泡收起后 open 仍为 true")
	}
	if a.pet != nil && a.pet.mood != petIdle {
		t.Error("气泡收起后宠物没有回到发呆")
	}
}

// 宠物有自己的开关，和「原生通知」互不影响：关掉系统通知、只想让宠物说话
// 的人是存在的，所以 petAnnounce 不读 nativeNotify。
func TestPetIgnoresNativeNotificationSwitch(t *testing.T) {
	a := testApp(t)
	a.setPetEnabled(true)
	a.nativeNotify = false
	a.petAnnounce([]nativeNotice{{ID: "n1", Title: "@小明 回复了你", Body: "在吗"}})
	if a.pet == nil || !a.pet.open {
		t.Error("关掉系统通知后宠物也不说话了")
	}
}

// 「要不要显示桌面宠物」落盘在 pet.json：重开应用要记得上次的选择，否则
// 每次启动都得去托盘里点一次。
func TestPetStorePersistsChoice(t *testing.T) {
	dir := t.TempDir()
	s := newPetStore(dir)
	if s.on() {
		t.Fatal("默认不该打开桌面宠物")
	}
	s.enabled = true
	if err := s.save(); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if !newPetStore(dir).on() {
		t.Error("重开应用后没有记住「开着」")
	}
}

// 托盘菜单里的「桌面宠物」是个勾选项，勾选状态跟着实际开关走，点一下把宠物
// 放出来、再点一下收回去，并把选择写进 pet.json。
func TestTrayPetToggleShowsAndHides(t *testing.T) {
	a := testApp(t)
	it := trayItem(t, a.trayMenu(), trayPetID)
	if it.Type != mygo.MenuItemCheckbox {
		t.Errorf("「桌面宠物」是 %q，期望勾选项", it.Type)
	}
	if it.Checked {
		t.Error("默认不该是勾上的")
	}

	it.Click(it, nil)
	if !a.petShown || !a.petStore.on() {
		t.Fatal("点「桌面宠物」后没有放出来")
	}
	if next := trayItem(t, a.trayMenu(), trayPetID); !next.Checked {
		t.Error("打开之后菜单没有勾上")
	}

	it = trayItem(t, a.trayMenu(), trayPetID)
	it.Click(it, nil)
	if a.petShown || a.petStore.on() {
		t.Fatal("再点一下没有收起来")
	}
	if next := trayItem(t, a.trayMenu(), trayPetID); next.Checked {
		t.Error("收起之后菜单仍然勾着")
	}
}

// 气泡的颜色跟着主题走：深色桌面上用深色表面，浅色桌面上用浅色表面。宠物
// 本体不跟主题（它是同一只），气泡要跟。
func TestPetBubbleFollowsTheme(t *testing.T) {
	a := testApp(t)
	a.setPetEnabled(true)
	a.sayPet("", "在吗", "")

	dark := ui.NewTester(a.petBubbleView, petBubbleW, 120)
	dark.SetDark(true)
	dark.Frame()
	darkPx := bubbleFillPixel(dark.Image())

	light := ui.NewTester(a.petBubbleView, petBubbleW, 120)
	light.SetDark(false)
	light.Frame()
	lightPx := bubbleFillPixel(light.Image())

	if darkPx == lightPx {
		t.Errorf("深浅两套主题下气泡底色相同（%v），说明没跟主题", darkPx)
	}
}

// bubbleFillPixel 取气泡顶部中间那一点的像素：那里是圆角矩形内部的纯色
// 背景，不会碰到文字。
func bubbleFillPixel(img *image.RGBA) [4]uint32 {
	if img == nil {
		return [4]uint32{}
	}
	r, g, b, al := img.At(petBubbleW/2, 4).RGBA()
	return [4]uint32{r, g, b, al}
}

/* ------------------------------------------------------------------
   天平造型与动态
---------------------------------------------------------------- */

// 天平永远在轻轻摇摆：光是时间过去，横梁角度与整体起伏就该变，但幅度不能
// 大到抢桌面的注意力。基准取 6 秒周期的整数倍，相位好算。
func TestPetMotionSwaysWithTime(t *testing.T) {
	base := time.Unix(1_700_000_004, 0)
	a := petMotion(base, nil, 0)
	quarter := petMotion(base.Add(1500*time.Millisecond), nil, 0)
	threeQuarter := petMotion(base.Add(4500*time.Millisecond), nil, 0)
	if quarter.angle < 0.8 || threeQuarter.angle > -0.8 {
		t.Errorf("横梁没有随时间摆起来：%v / %v / %v", a.angle, quarter.angle, threeQuarter.angle)
	}
	if math.Abs(float64(quarter.angle)) > 1.2 || math.Abs(float64(threeQuarter.angle)) > 1.2 {
		t.Errorf("平时摆幅过大：%v / %v", quarter.angle, threeQuarter.angle)
	}
	if a.bob == quarter.bob {
		t.Error("整杆天平没有呼吸起伏")
	}
	if a.glow >= 1 {
		t.Error("没有消息时宝石不该一直全亮")
	}
}

// 来消息时以说话时刻为起点叠一段阻尼摆动：先明显一沉，几秒后自己衰减回
// 平衡，砝码也落进盘里。说话时支点宝石亮到满。
func TestPetMotionTiltsOnMessageAndSettles(t *testing.T) {
	base := time.Unix(1_700_000_004, 0)
	calm := petMotion(base, &petState{}, 0)
	st := &petState{mood: petTalk, at: base}
	hit := petMotion(base, st, 0)
	if hit.angle-calm.angle < 7 {
		t.Errorf("来消息时没有明显倾斜：%v -> %v", calm.angle, hit.angle)
	}
	if hit.glow != 1 || hit.drop != 0 {
		t.Errorf("说话时支点亮起、砝码刚开始落下，实际 glow=%v drop=%v", hit.glow, hit.drop)
	}
	settled := petMotion(base.Add(5*time.Second), st, 0)
	if math.Abs(float64(settled.angle)) > 1.3 {
		t.Errorf("5 秒后仍在大幅摆动：%v", settled.angle)
	}
	if settled.drop != 1 {
		t.Errorf("5 秒后砝码还没落好：%v", settled.drop)
	}
}

// 未读消息是右盘的砝码：越多右边越沉。
func TestPetMotionWeighsUnread(t *testing.T) {
	base := time.Unix(1_700_000_004, 0)
	light := petMotion(base, nil, 0)
	heavy := petMotion(base, nil, 3)
	if heavy.angle-light.angle < 3.5 {
		t.Errorf("未读没有压沉右盘：%v -> %v", light.angle, heavy.angle)
	}
}

// 砝码数要钳住：托盘只有 30 DIP 宽，第四颗以上挤不下，也不必再表达「更多」。
func TestPetPanWeightsClampsUnread(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{-5, 1}, {0, 1}, {1, 2}, {3, 4}, {99, 4},
	} {
		if got := petPanWeights(tc.in); got != tc.want {
			t.Errorf("petPanWeights(%d) = %d，期望 %d", tc.in, got, tc.want)
		}
	}
}

// 姿态算得对不算画得对：再从像素上确认两只托盘、横梁、底座都真的画在
// 窗口里，没有哪一半被裁掉。
func TestPetBalanceDrawsBothPans(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.petView, petWindowW, petWindowH)
	tt.Frame()
	img := tt.Image()
	if img == nil {
		t.Fatal("没有渲染出画布")
	}
	for _, tc := range []struct {
		name string
		box  image.Rectangle
	}{
		{"左盘", image.Rect(2, 55, 38, 100)},
		{"右盘", image.Rect(82, 55, 118, 100)},
		{"横梁", image.Rect(50, 36, 70, 52)},
		{"底座", image.Rect(30, 114, 90, 127)},
	} {
		if n := countOpaquePixels(img, tc.box); n < 20 {
			t.Errorf("%s 区域只有 %d 个不透明像素", tc.name, n)
		}
	}
}

// 未读变多时右盘真的多出砝码，而不只是倾角变了。
func TestPetBalanceAddsWeightsForUnread(t *testing.T) {
	render := func(unread int) *image.RGBA {
		a := testApp(t)
		a.unread = unread
		tt := ui.NewTester(a.petView, petWindowW, petWindowH)
		tt.Frame()
		return tt.Image()
	}
	box := image.Rect(82, 55, 118, 90)
	light := countOpaquePixels(render(0), box)
	heavy := countOpaquePixels(render(3), box)
	if heavy < light+25 {
		t.Errorf("未读 3 条时右盘没多出砝码：%d -> %d", light, heavy)
	}
}

// countOpaquePixels 数一块区域里 alpha>0 的像素，超出画布的部分自动裁掉。
func countOpaquePixels(img *image.RGBA, box image.Rectangle) int {
	if img == nil {
		return 0
	}
	b := img.Bounds().Intersect(box)
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, al := img.At(x, y).RGBA(); al > 0 {
				n++
			}
		}
	}
	return n
}
