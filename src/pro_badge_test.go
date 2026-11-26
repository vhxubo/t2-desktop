package main

import (
	"image"
	"image/color"
	"math"
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

// Pro 图标是随包内置的静态资源：缓存为空时也能拿到位图，不发网络请求。
func TestProMarkUsesEmbeddedResource(t *testing.T) {
	src := embeddedProMark()
	if src == nil {
		t.Fatal("内置 Pro 图标解码失败")
	}
	bmp, ok := src.(*ui.Bitmap)
	if !ok {
		t.Fatalf("内置 Pro 图标不是位图：%T", src)
	}
	if w, h := ui.ImageSize(src); w <= 0 || h <= 0 {
		t.Fatalf("内置 Pro 图标尺寸不对：%vx%v", w, h)
	}
	a := testApp(t) // 空缓存、autoLoad=false，不会下载任何图片
	if got := a.proMarkBitmap(); got != bmp {
		t.Fatal("图片缓存为空时没有回落到内置 Pro 图标")
	}
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

// 旋转以图片中心为圆心：测试图左上象限是红色，象限的右 / 下边界就是图片
// 中线，它必须与圆章中心重合；转到任意角度圆章中心都不能漂移。以前圆章
// 自带 Border，图片从边框内侧起排，中心偏右下半个边框宽，旋转时看着像绕
// 着别的点打转（见 proMarkFor 的描边覆盖层）。
func TestProMarkRotatesAroundImageCenter(t *testing.T) {
	render := func(angle float32) (float64, float64, ui.Rect) {
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Box(c).Size(80, 80).Children(func() {
				mark := proMarkFor(c, 60, proTestBitmap())
				mark.Rotate(angle)
			})
		}, 140, 140)
		tt.Frame()
		badge, ok := tt.AccessByRole(ui.RoleImage, "2Libra Pro")
		if !ok {
			t.Fatal("没有找到 Pro 圆章")
		}
		img := tt.Image()
		maxX, maxY := -1, -1
		for y := 0; y < img.Bounds().Dy(); y++ {
			for x := 0; x < img.Bounds().Dx(); x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				if r>>8 > 200 && g>>8 < 60 && b>>8 < 60 {
					if x > maxX {
						maxX = x
					}
					if y > maxY {
						maxY = y
					}
				}
			}
		}
		if maxX < 0 {
			t.Fatal("没有找到测试图的红色象限")
		}
		return float64(maxX + 1), float64(maxY + 1), badge.Bounds
	}

	imageX, imageY, box := render(0)
	wantX, wantY := float64(box.X+box.W/2), float64(box.Y+box.H/2)
	if math.Abs(imageX-wantX) > 1.5 || math.Abs(imageY-wantY) > 1.5 {
		t.Fatalf("图片中心 (%.1f,%.1f) 与圆章中心 (%.1f,%.1f) 不重合", imageX, imageY, wantX, wantY)
	}
	for _, angle := range []float32{15, 30, 45, 90, 180} {
		_, _, box := render(angle)
		if gotX, gotY := float64(box.X+box.W/2), float64(box.Y+box.H/2); math.Abs(gotX-wantX) > 0.5 || math.Abs(gotY-wantY) > 0.5 {
			t.Fatalf("旋转 %.0f° 后圆章中心漂移到 (%.1f,%.1f)，期望 (%.1f,%.1f)", angle, gotX, gotY, wantX, wantY)
		}
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
