package main

import (
	_ "embed"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 官网的 Pro 标识不是文字，而是 r2.2libra.com/site/pro.png：黑底四叶风车。
// 它作为头像右下角的一枚圆章出现（官网 100px 头像配 28px 图，默认取头像的
// 1/3），用底色描边压住头像；鼠标悬停时 1.5 秒转满一圈（官网是
// transition-transform duration-1500 + group-hover:rotate-[360deg]），离开即停。
// 客户端原来只在昵称旁写一个 "PRO" 标签，这里换成同一枚图标与同一段动作。

const (
	// proMarkURL 保留为缓存键，方便无头测试注入测试图；图标本体已随包内置，
	// 运行时不再从官网下载（见下方的 proMarkPNG）。
	proMarkURL = "https://r2.2libra.com/site/pro.png"

	// proMarkDuration 与官网的 duration-1500 一致。
	proMarkDuration = 1500 * time.Millisecond

	// proMarkRotateKey 区分这枚元素上「悬停旋转」这一路动画值。
	proMarkRotateKey = "pro-spin"

	// proMarkBorderWidth 是圆章的描边宽度。
	proMarkBorderWidth = 1.5
)

// proMarkPNG 是从官网取回、随应用一起打包的 Pro 图标（resources/pro.png）。
// 作为静态资源，首次启动、断网或清掉图片缓存后都能照常画出来。
//
//go:embed resources/pro.png
var proMarkPNG []byte

var (
	proMarkOnce   sync.Once
	proMarkStatic ui.ImageSource
)

// embeddedProMark 把内置 PNG 解码一次，之后所有窗口共用同一份位图。
func embeddedProMark() ui.ImageSource {
	proMarkOnce.Do(func() {
		src, err := ui.DecodeImage(proMarkPNG)
		if err == nil {
			proMarkStatic = src
		}
	})
	return proMarkStatic
}

// proMarkSize 按头像边长换算图标边长。官网默认用头像的 1/3，极小头像给一个
// 可读的下限，大头像也不让图标喧宾夺主。
func proMarkSize(avatar float32) float32 {
	s := avatar / 3
	if s < 8 {
		s = 8
	}
	if s > 28 {
		s = 28
	}
	return s
}

// proMarkBitmapOf 从给定图片缓存取 Pro 位图（无头测试借此注入不对称的测试图），
// 生产环境回落到随包内置的 PNG：图标是静态资源，既不发请求，也不受清缓存影响。
func proMarkBitmapOf(images *imageCache) *ui.Bitmap {
	if images != nil {
		if b := images.bitmap(proMarkURL); b != nil {
			return b
		}
	}
	b, _ := embeddedProMark().(*ui.Bitmap)
	return b
}

func (a *app) proMarkBitmap() *ui.Bitmap { return proMarkBitmapOf(a.images) }

// proMarkFor 画一枚 Pro 圆章：绝对定位在宿主右下角、略微外扩，描边取自当前
// 主题底色（官网的 border-base-100）。位图由调用方传入，便于「我的」窗口这种没有
// app 上下文的地方复用同一套画法。
func proMarkFor(c *ui.Context, size float32, bmp *ui.Bitmap) ui.Element {
	t := c.Theme()
	tk := lobetheme.TokensOf(c)
	overhang := size * 0.15
	mark := ui.Box(c).Absolute().Right(-overhang).Bottom(-overhang).
		Size(size, size).Radius(size / 2).Shrink(0).Clip().
		Background(t.Background).
		Role(ui.RoleImage).Label("2Libra Pro").Tooltip("2Libra Pro")
	mark.Children(func() {
		if bmp != nil {
			ui.Image(c, bmp).Fit(ui.Cover).Size(size, size)
		} else {
			// 解码失败时给一个实心圆占位，位置和大小至少是对的。
			ui.Box(c).Fill().Background(tk.TextBase)
		}
		// 描边画在不占布局的覆盖层上。若把 Border 放在圆章自身，子元素会从
		// 边框内侧开始排，图片中心就比圆章中心偏右下半个边框宽；旋转圆章时
		// 看着就像图片在绕着别的点打转，而不是以图片中心为圆心。
		ui.Box(c).Absolute().Left(0).Top(0).Size(size, size).
			Radius(size/2).Border(proMarkBorderWidth, t.Background)
	})
	return mark
}

func (a *app) proMark(c *ui.Context, size float32) ui.Element {
	return proMarkFor(c, size, a.proMarkBitmap())
}

// avatarHostWith 把一段头像画法包进固定尺寸的容器；pro 为真时在右下角叠加
// 官网同款 Pro 图标。悬停整个头像区时图标在 proMarkDuration 内转满一圈；鼠标
// 离开后立刻回到 0 度 —— Jump 只落值、不请求动画帧，所以不悬浮时没有动态效果。
//
// 传 build 而不是位图，是为了兼容资料头使用的 Lobe Avatar 与各处自绘的头像。
func avatarHostWith(c *ui.Context, size float32, pro bool, bmp *ui.Bitmap, build func() ui.Element) ui.Element {
	if !pro {
		return build()
	}
	host := ui.Box(c).Size(size, size).Shrink(0)
	var mark ui.Element
	host.Children(func() {
		build()
		mark = proMarkFor(c, proMarkSize(size), bmp)
	})
	var angle float32
	if host.Hovered() {
		angle = host.AnimateTo(proMarkRotateKey, 360, ui.Animation{
			Duration: proMarkDuration, Ease: ui.EaseInOut,
		})
	} else {
		angle = host.Jump(proMarkRotateKey, 0)
	}
	mark.Rotate(angle)
	return host
}

// avatarHost 是 app 侧的常用入口：Pro 位图从共享图片缓存取。
func (a *app) avatarHost(c *ui.Context, size float32, pro bool, build func() ui.Element) ui.Element {
	if !pro {
		return build()
	}
	return avatarHostWith(c, size, pro, a.proMarkBitmap(), build)
}

// avatarWithPro 画头像，并在 Pro 用户的头像右下角叠加 Pro 图标。
func (a *app) avatarWithPro(c *ui.Context, name string, bmp *ui.Bitmap, size float32, pro bool) ui.Element {
	return a.avatarHost(c, size, pro, func() ui.Element {
		return avatarView(c, name, bmp, size)
	})
}
