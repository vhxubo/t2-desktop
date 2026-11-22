package main

import (
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// proTestBitmap 画一张不对称的测试图（左上角红色、其余黑色），这样图标转过
// 任意角度后像素都会变化，便于断言悬停确实驱动了旋转。
func proTestBitmap() *ui.Bitmap {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			c := color.RGBA{0, 0, 0, 255}
			if x < 32 && y < 32 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return ui.NewBitmap(img)
}

func proTestApp(t *testing.T) *app {
	a := testApp(t)
	a.images.put(proMarkURL, proTestBitmap())
	return a
}

// Pro 用户头像右下角有图标，普通用户没有，也不再出现「PRO」文字。
func TestProMarkShownOnlyForPro(t *testing.T) {
	a := proTestApp(t)
	tt := ui.NewTester(func(c *ui.Context) {
		a.avatarWithPro(c, "vip", nil, 48, true)
	}, 120, 120)
	tt.Frame()
	badge, ok := tt.AccessByRole(ui.RoleImage, "2Libra Pro")
	if !ok {
		t.Fatalf("Pro 用户没有渲染右下角图标：%q", tt.Texts())
	}
	avatar, ok := tt.AccessByRole(ui.RoleImage, "vip")
	if !ok {
		t.Fatalf("没有找到头像：%q", tt.Texts())
	}
	if badge.Bounds.X <= avatar.Bounds.X || badge.Bounds.Y <= avatar.Bounds.Y {
		t.Fatalf("Pro 图标不在头像右下角：icon=%v avatar=%v", badge.Bounds, avatar.Bounds)
	}
	if tt.HasText("PRO") {
		t.Fatalf("还在显示 PRO 文字：%q", tt.Texts())
	}

	plain := ui.NewTester(func(c *ui.Context) {
		a.avatarWithPro(c, "free", nil, 48, false)
	}, 120, 120)
	plain.Frame()
	if _, ok := plain.AccessByRole(ui.RoleImage, "2Libra Pro"); ok {
		t.Fatalf("普通用户不该有 Pro 图标：%q", plain.Texts())
	}
}

// 悬停头像时图标转起来，离开后立刻回到静止角度；不悬浮时不产生动画帧。
func TestProMarkSpinsOnHoverOnly(t *testing.T) {
	a := proTestApp(t)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Padding(20).Children(func() {
			a.avatarWithPro(c, "vip", nil, 64, true)
		})
	}, 120, 120)
	tt.Frame()

	if _, ok := tt.AccessByRole(ui.RoleImage, "2Libra Pro"); !ok {
		t.Fatalf("没有找到 Pro 图标：%q", tt.Texts())
	}
	avatar, ok := tt.AccessByRole(ui.RoleImage, "vip")
	if !ok {
		t.Fatalf("没有找到头像：%q", tt.Texts())
	}
	rest := cloneRGBA(tt.Image())

	// 移到头像中心（不是右下角的小图标）：整块头像都是悬停热区。
	tt.Move(avatar.Bounds.X+avatar.Bounds.W/2, avatar.Bounds.Y+avatar.Bounds.H/2)
	tt.Advance(400 * time.Millisecond)
	spun := cloneRGBA(tt.Image())
	if sameRGBA(rest, spun) {
		t.Fatal("悬停头像后 Pro 图标没有旋转")
	}

	// 移开鼠标：Jump 把角度直接落回 0，画面应与静止时完全一致。
	tt.Move(-100, -100)
	tt.Frame()
	if !sameRGBA(rest, cloneRGBA(tt.Image())) {
		t.Fatal("鼠标离开后 Pro 图标没有回到静止角度")
	}
	// 不悬浮时不再请求动画帧：动态效果完全由悬停触发。
	if tt.FramePending() {
		t.Fatal("鼠标离开后仍在请求动画帧")
	}
}

func cloneRGBA(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}

func sameRGBA(a, b *image.RGBA) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	for i := 0; i < len(a.Pix); i++ {
		if a.Pix[i] != b.Pix[i] {
			return false
		}
	}
	return true
}
