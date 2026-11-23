package main

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"math"
	"path/filepath"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"
)

// 头像裁剪编辑器
//
// 选图之后不直接上传：先在「我的」窗口里弹一个方形裁剪框，让用户拖动四边
// 四角调整取景范围、拖动框内移动框，滚轮或滑杆缩放图片，点「上传」才发请求。
// 头像接口会把图片重编码成固定尺寸，客户端这里只负责从原图里裁出裁剪框内的
// 正方形并缩到 512 像素，保留透明通道，格式统一成 PNG。
//
// 头像上传要消耗 500 金币（服务端扣费），这一步在气泡、文件对话框和裁剪框
// 三处都提前说明，用户选图前就知道要花钱。

const (
	// avatarCost 是换一次头像要消耗的金币数，仅用于界面提示；真正扣多少由
	// 服务端决定，客户端不参与记账。
	avatarCost = 500
	// avatarCropStageSize 是裁剪舞台的边长（DIP）。Modal 内容区在最小窗口里
	// 也要一屏看到费用、方形预览、滑杆与说明，再大就得滚动才能看全。
	avatarCropStageSize float32 = 200
	// avatarCropSourceSide 是解码后保留的最长边。头像最终只上传 512 像素，
	// 1600 已经留了足够的放大余量，也避免一张 4000 像素的手机照片解码后
	// 占几十 MB。
	avatarCropSourceSide = 1600
	// avatarCropOutputSide 是上传的正方形边长。2Libra 的头像显示尺寸很小，
	// 512 在 3 倍屏上也够清晰，PNG 体积远低于 5 MB 上限。
	avatarCropOutputSide = 512
	// 缩放以「刚好覆盖舞台」为 1 倍，最小不能小于它，否则框里会露出底色；
	// 上限给 4 倍，再放大的部分超出原图像素，只会变糊。
	avatarCropMinZoom = 1
	avatarCropMaxZoom = 4
	// avatarCropDefaultRatio 是裁剪框初始边长占舞台的比例。
	avatarCropDefaultRatio float32 = 0.72
	// avatarCropMinSide 是最小裁剪框边长，再小手柄就点不准了。
	avatarCropMinSide float32 = 64
	// avatarCropHandle 是四边 / 四角手柄的命中半径（DIP）。
	avatarCropHandle float32 = 12
)

// 裁剪框拖动的部位。移动框与缩放图片之外的四种边、四个角各自独立。
const (
	cropDragNone = iota
	cropDragPan
	cropDragMove
	cropDragLeft
	cropDragRight
	cropDragTop
	cropDragBottom
	cropDragTopLeft
	cropDragTopRight
	cropDragBottomLeft
	cropDragBottomRight
)

// avatarCropState 是裁剪编辑器的状态。img 保存转正、缩边后的原图用于生成
// 上传字节，bmp 是同一张图给界面绘制用的位图；两者在裁剪框关闭时一起丢掉。
type avatarCropState struct {
	open    bool
	loading bool
	busy    bool
	err     string
	name    string
	img     image.Image
	bmp     *ui.Bitmap
	iw, ih  float32
	// zoom 是相对「刚好覆盖舞台」的倍数，1～4。
	zoom float64
	// panX / panY 是图片中心相对舞台中心的偏移，单位是 DIP。
	panX, panY float32
	// cropX / cropY / cropSide 是方形裁剪框在舞台坐标系里的位置与边长。
	// cropSide 为 0 时按默认比例居中（见 cropRect）。
	cropX, cropY, cropSide float32

	// drag 是正在拖动的部位，dragRect 是按下时裁剪框的位置。
	drag                       int
	dragPointerX, dragPointerY float32
	dragRect                   ui.Rect
}

// beginAvatarCrop 读入选中的本地图片并进入裁剪编辑器。大小与真实格式先由
// inspectImage 校验（只看文件头，不信扩展名）；解码与降采样放到后台，回来
// 后用 publish 打开裁剪框，超大照片不会卡住界面。
func (a *app) beginAvatarCrop(ps *profileState, path string) {
	if ps == nil || a.profile != ps || ps.avatarBusy || path == "" {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLoginFor(toastProfile, "登录后才能更换头像。")
		return
	}
	src, err := inspectImage(uploadSource{Path: path, Name: filepath.Base(path)}, avatarMaxBytes)
	if err != nil {
		ps.avatarErr = err.Error()
		return
	}
	ps.avatarErr = ""
	ps.avatarCrop = avatarCropState{loading: true}
	actor := a.auth.username()
	go func() {
		data, err := src.bytes()
		var img image.Image
		decoding := false
		if err == nil {
			decoding = true
			img, _, err = image.Decode(bytes.NewReader(data))
			if err == nil {
				// 先缩再转：EXIF 只影响方向，缩放不改变长边；转置放在小图上
				// 做能省一个数量级的像素操作。
				img = applyJPEGOrientation(scaleToMaxSide(img, avatarCropSourceSide), data)
			}
		}
		a.publish(func() {
			if a.profile != ps {
				return
			}
			if !a.auth.loggedIn() || a.auth.username() != actor {
				ps.avatarCrop = avatarCropState{}
				return
			}
			if err != nil {
				ps.avatarCrop = avatarCropState{}
				if decoding {
					ps.avatarErr = "这张图片读不出来，换一张 JPEG / PNG / WebP 试试。"
				} else {
					ps.avatarErr = "读取图片失败：" + err.Error()
				}
				return
			}
			b := img.Bounds()
			ps.avatarCrop = avatarCropState{
				open:    true,
				name:    src.Name,
				img:     img,
				bmp:     ui.NewBitmap(img),
				iw:      float32(b.Dx()),
				ih:      float32(b.Dy()),
				zoom:    avatarCropMinZoom,
				loading: false,
			}
			ps.avatarCrop.resetCrop()
			ps.avatarOpen = false
		})
	}()
}

// closeAvatarCrop 收起裁剪框并释放原图。上传成功、取消与切换账号都走这里。
func (a *app) closeAvatarCrop(ps *profileState) {
	if ps == nil {
		return
	}
	ps.avatarCrop = avatarCropState{}
}

// confirmAvatarCrop 把当前裁剪结果渲染成 PNG 并上传。裁剪本身在 UI 线程
// 完成（512 像素的重采样只有几毫秒），上传仍在后台，失败时裁剪框保持打开，
// 用户调好之后可以直接重试。
func (a *app) confirmAvatarCrop(ps *profileState) {
	if ps == nil || a.profile != ps || !ps.avatarCrop.open || ps.avatarCrop.busy || ps.avatarBusy {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLoginFor(toastProfile, "登录后才能更换头像。")
		return
	}
	data, err := ps.avatarCrop.render()
	if err != nil {
		ps.avatarCrop.err = err.Error()
		return
	}
	ps.avatarCrop.err = ""
	ps.avatarCrop.busy = true
	a.uploadAvatarData(ps, ps.avatarCrop.name, data)
}

// displayScale 返回图片在裁剪框里的绘制比例（DIP / 原图像素）。
func (c *avatarCropState) displayScale() float32 {
	if c.iw <= 0 || c.ih <= 0 {
		return 0
	}
	return max(avatarCropStageSize/c.iw, avatarCropStageSize/c.ih) * float32(c.zoom)
}

// drawRect 返回图片左上角在舞台上的位置与显示尺寸。x / y 是舞台元素的偏移。
func (c *avatarCropState) drawRect(x, y float32) ui.Rect {
	s := c.displayScale()
	w, h := c.iw*s, c.ih*s
	cx := x + avatarCropStageSize/2 + c.panX
	cy := y + avatarCropStageSize/2 + c.panY
	return ui.Rect{X: cx - w/2, Y: cy - h/2, W: w, H: h}
}

// cropRect 返回方形裁剪框在舞台坐标系里的位置与边长。还没初始化时按默认
// 比例居中，这样测试直接构造状态也能渲染出结果。
func (c *avatarCropState) cropRect() ui.Rect {
	side := c.cropSide
	x, y := c.cropX, c.cropY
	if side <= 0 {
		side = avatarCropStageSize * avatarCropDefaultRatio
		x = (avatarCropStageSize - side) / 2
		y = x
	}
	return ui.Rect{X: x, Y: y, W: side, H: side}
}

// setCropRect 设置方形裁剪框，边长限制在最小值与舞台之间，位置夹在舞台内。
func (c *avatarCropState) setCropRect(x, y, side float32) {
	side = min(max(side, avatarCropMinSide), avatarCropStageSize)
	x = min(max(x, 0), avatarCropStageSize-side)
	y = min(max(y, 0), avatarCropStageSize-side)
	c.cropX, c.cropY, c.cropSide = x, y, side
}

// resetCrop 把裁剪框恢复成居中的默认方形。
func (c *avatarCropState) resetCrop() {
	side := avatarCropStageSize * avatarCropDefaultRatio
	off := (avatarCropStageSize - side) / 2
	c.setCropRect(off, off, side)
}

// hit 判断指针落在裁剪框的哪个部位：四角优先于四边，四边优先于框内。
// 返回 cropDragNone 表示落在框外（拖动它平移图片）。
func (c *avatarCropState) hit(x, y float32) int {
	r := c.cropRect()
	h := avatarCropHandle
	nearL := x >= r.X-h && x <= r.X+h
	nearR := x >= r.X+r.W-h && x <= r.X+r.W+h
	nearT := y >= r.Y-h && y <= r.Y+h
	nearB := y >= r.Y+r.H-h && y <= r.Y+r.H+h
	withinX := x >= r.X-h && x <= r.X+r.W+h
	withinY := y >= r.Y-h && y <= r.Y+r.H+h
	switch {
	case nearL && nearT:
		return cropDragTopLeft
	case nearR && nearT:
		return cropDragTopRight
	case nearL && nearB:
		return cropDragBottomLeft
	case nearR && nearB:
		return cropDragBottomRight
	case nearL && withinY:
		return cropDragLeft
	case nearR && withinY:
		return cropDragRight
	case nearT && withinX:
		return cropDragTop
	case nearB && withinX:
		return cropDragBottom
	case withinX && withinY:
		return cropDragMove
	}
	return cropDragNone
}

// dragTo 按指针当前位置更新裁剪框或图片。拖动边时固定对边，拖角时固定
// 对角，始终是正方形；拖动框外则平移图片。
func (c *avatarCropState) dragTo(x, y float32) {
	dx, dy := x-c.dragPointerX, y-c.dragPointerY
	if c.drag == cropDragPan {
		c.panX += dx
		c.panY += dy
		c.clampPan()
		c.dragPointerX, c.dragPointerY = x, y
		return
	}
	r := c.dragRect
	left, top := r.X, r.Y
	right, bottom := r.X+r.W, r.Y+r.H
	switch c.drag {
	case cropDragMove:
		c.setCropRect(r.X+dx, r.Y+dy, r.W)
		// 每次移动后把基准框更新到当前位置：指针事件是一小步一小步来的，
		// 若一直以按下时的框为基准，框会反复跳回起点，看起来像在回弹。
		c.dragRect = c.cropRect()
		c.dragPointerX, c.dragPointerY = x, y
		return
	case cropDragLeft:
		left += dx
	case cropDragRight:
		right += dx
	case cropDragTop:
		top += dy
	case cropDragBottom:
		bottom += dy
	case cropDragTopLeft:
		left, top = left+dx, top+dy
	case cropDragTopRight:
		right, top = right+dx, top+dy
	case cropDragBottomLeft:
		left, bottom = left+dx, bottom+dy
	case cropDragBottomRight:
		right, bottom = right+dx, bottom+dy
	}
	var side, nx, ny float32
	switch c.drag {
	case cropDragLeft:
		// 拖左边：右边固定。
		side = right - left
		nx, ny = right-side, top
	case cropDragRight:
		// 拖右边：左边固定。
		side, nx, ny = right-left, left, top
	case cropDragTop:
		// 拖上边：底边固定。
		side = bottom - top
		nx, ny = left, bottom-side
	case cropDragBottom:
		// 拖下边：顶边固定。
		side, nx, ny = bottom-top, left, top
	default:
		// 四个角：固定对角，边长取两个方向里较大的一个。
		side = max(right-left, bottom-top)
		switch c.drag {
		case cropDragTopLeft:
			nx, ny = right-side, bottom-side
		case cropDragTopRight:
			nx, ny = left, bottom-side
		case cropDragBottomLeft:
			nx, ny = right-side, top
		default: // cropDragBottomRight
			nx, ny = left, top
		}
	}
	c.setCropRect(nx, ny, side)
	// 同上：拖边 / 拖角也以更新后的框作为下一小步的基准，否则每次移动都
	// 只叠加最后一步的增量，框会粘在原处或回弹。
	c.dragRect = c.cropRect()
	c.dragPointerX, c.dragPointerY = x, y
}

// clampPan 把平移夹进允许的范围：图片边缘最多贴到舞台边缘，任何一边都
// 不会露出空底色。缩小后原来的平移可能越界，绘制前都要再夹一次。
func (c *avatarCropState) clampPan() {
	s := c.displayScale()
	maxX := max(0, (c.iw*s-avatarCropStageSize)/2)
	maxY := max(0, (c.ih*s-avatarCropStageSize)/2)
	c.panX = min(max(c.panX, -maxX), maxX)
	c.panY = min(max(c.panY, -maxY), maxY)
}

// setZoom 设置缩放并立刻夹住平移。
func (c *avatarCropState) setZoom(z float64) {
	if math.IsNaN(z) || z <= 0 {
		return
	}
	c.zoom = min(max(z, avatarCropMinZoom), avatarCropMaxZoom)
	c.clampPan()
}

// render 按当前缩放、平移与裁剪框裁出正方形，缩成 512×512 的 PNG。
func (c *avatarCropState) render() ([]byte, error) {
	if c.img == nil || c.iw <= 0 || c.ih <= 0 {
		return nil, errors.New("图片还没准备好")
	}
	s := c.displayScale()
	if s <= 0 {
		return nil, errors.New("图片还没准备好")
	}
	r := c.cropRect()
	imgLeft := avatarCropStageSize/2 + c.panX - c.iw*s/2
	imgTop := avatarCropStageSize/2 + c.panY - c.ih*s/2
	x0 := (r.X - imgLeft) / s
	y0 := (r.Y - imgTop) / s
	side := r.W / s
	b := c.img.Bounds()
	src := image.Rect(
		b.Min.X+int(math.Round(float64(x0))),
		b.Min.Y+int(math.Round(float64(y0))),
		b.Min.X+int(math.Round(float64(x0+side))),
		b.Min.Y+int(math.Round(float64(y0+side))),
	).Intersect(b)
	if src.Dx() <= 0 || src.Dy() <= 0 {
		return nil, errors.New("裁剪区域超出图片范围")
	}
	dst := image.NewRGBA(image.Rect(0, 0, avatarCropOutputSide, avatarCropOutputSide))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), c.img, src, draw.Src, nil)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// input 处理裁剪框里的滚轮缩放、触控板缩放与左键拖动：框内移动框，四边
// 四角调整大小，框外拖动平移图片。
func (c *avatarCropState) input(ev ui.InputEvent) bool {
	if !c.open || c.busy || c.bmp == nil {
		return false
	}
	switch ev.Kind {
	case ui.InputScroll:
		switch {
		case ev.DY < 0:
			c.setZoom(c.zoom * 1.12)
		case ev.DY > 0:
			c.setZoom(c.zoom / 1.12)
		default:
			return false
		}
		return true
	case ui.InputZoom:
		if ev.Scale <= 0 || ev.Scale == 1 {
			return false
		}
		c.setZoom(c.zoom * float64(ev.Scale))
		return true
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		c.drag = c.hit(ev.X, ev.Y)
		if c.drag == cropDragNone {
			// 落在框外：按在图上拖动整张图。
			c.drag = cropDragPan
		}
		c.dragPointerX, c.dragPointerY = ev.X, ev.Y
		c.dragRect = c.cropRect()
		return true
	case ui.InputPointerMove:
		if c.drag == cropDragNone {
			return false
		}
		if ev.Button != 0 {
			c.drag = cropDragNone
			return false
		}
		c.dragTo(ev.X, ev.Y)
		return true
	case ui.InputPointerUp:
		if c.drag == cropDragNone {
			return false
		}
		c.drag = cropDragNone
		return true
	}
	return false
}
