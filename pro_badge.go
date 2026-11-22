package main

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// 官网的 Pro 标识不是文字，而是 r2.2libra.com/site/pro.png：黑底四叶风车。
// 它作为头像右下角的一枚圆章出现（官网 100px 头像配 28px 图，默认取头像的
// 1/3），用底色描边压住头像；鼠标悬停时 1.5 秒转满一圈（官网是
// transition-transform duration-1500 + group-hover:rotate-[360deg]），离开即停。
// 客户端原来只在昵称旁写一个 "PRO" 标签，这里换成同一枚图标与同一段动作。

const (
	// proMarkURL 是官网 Pro 图标的地址，走统一的图片缓存（也走配置的代理）。
	proMarkURL = "https://r2.2libra.com/site/pro.png"

	// proMarkDuration 与官网的 duration-1500 一致。
	proMarkDuration = 1500 * time.Millisecond

	// proMarkRotateKey 区分这枚元素上「悬停旋转」这一路动画值。
	proMarkRotateKey = "pro-spin"
)

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

// proMarkBitmap 只读缓存，不在渲染里发请求。图标由 afterMainWindowOpen 预取。
func (a *app) proMarkBitmap() *ui.Bitmap {
	return a.images.bitmap(proMarkURL)
}

// proMark 画一枚 Pro 圆章：绝对定位在宿主右下角、略微外扩，描边取自当前主题
// 底色（官网的 border-base-100）。angle 由宿主根据悬停状态算好后传进来，
// 这里只把它落到变换上。
func (a *app) proMark(c *ui.Context, size float32) ui.Element {
	t := c.Theme()
	overhang := size * 0.15
	mark := ui.Box(c).Absolute().Right(-overhang).Bottom(-overhang).
		Size(size, size).Radius(size/2).Shrink(0).Clip().
		Border(1.5, t.Background).Background(t.Background).
		Role(ui.RoleImage).Label("2Libra Pro").Tooltip("2Libra Pro")
	mark.Children(func() {
		if bmp := a.proMarkBitmap(); bmp != nil {
			ui.Image(c, bmp).Fit(ui.Cover).Size(size, size)
			return
		}
		// 还没缓存好时给一个实心圆占位，位置和大小至少是对的。
		ui.Box(c).Fill().Background(ui.RGB(17, 17, 17))
	})
	return mark
}

// avatarHost 把一段头像画法包进固定尺寸的容器；pro 为真时在右下角叠加官网同款
// Pro 图标。悬停整个头像区时图标在 proMarkDuration 内转满一圈；鼠标离开后立刻
// 回到 0 度 —— Jump 只落值、不请求动画帧，所以不悬浮时完全没有动态效果。
//
// 传 build 而不是位图，是为了兼容资料头使用的 Lobe Avatar 与各处自绘的头像。
func (a *app) avatarHost(c *ui.Context, size float32, pro bool, build func() ui.Element) ui.Element {
	if !pro {
		return build()
	}
	host := ui.Box(c).Size(size, size).Shrink(0)
	var mark ui.Element
	host.Children(func() {
		build()
		mark = a.proMark(c, proMarkSize(size))
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

// avatarWithPro 画头像，并在 Pro 用户的头像右下角叠加 Pro 图标。
func (a *app) avatarWithPro(c *ui.Context, name string, bmp *ui.Bitmap, size float32, pro bool) ui.Element {
	return a.avatarHost(c, size, pro, func() ui.Element {
		return avatarView(c, name, bmp, size)
	})
}
