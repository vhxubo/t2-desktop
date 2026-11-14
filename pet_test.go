package main

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 宠物窗口是透明窗口里自己画出来的一只，所以它的内容只能从像素上验：先确认
// 画布确实有东西（不是一块空透明窗口），再确认四角是透明的（否则窗口会盖住
// 桌面上一个方框）。
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
	// 身体加耳朵大约占画布的三分之一，给一个宽松的下限：画歪了、画没了都
	// 会掉到这以下。
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
