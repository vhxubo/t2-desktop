package main

import (
	"crypto/sha256"
	"math"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// 头像占位图案（jdenticon 风格）：头像地址为空、或者图片还没下载到本机时，
// 按用户标识画一个确定性的几何图案，取代过去的昵称首字母。
//
// 图案只由标识的哈希决定，不依赖窗口、页面与启动次数，所以同一个用户在
// 列表、评论、通知、资料头与「我的」窗口里看到的都是同一张图；两个不同的
// 用户几乎不会撞图。图形按 4×4 的格子排布，只取左半 2 列再镜像到右半，
// 因此左右对称，跟 jdenticon 一样是一枚「花纹」而不是随机噪点。

// identiconGrid 是图案的格数。只画左半 identiconGrid/2 列，右半镜像。
const identiconGrid = 4

// identiconPadding 是图案与头像框之间的留白比例。头像框是圆形，图案按方形
// 排布，四角会被圆边裁掉，留出这段距离后图形不会贴着圆边。
const identiconPadding float32 = 0.14

// identiconCacheMax 是图案缓存的上限。图案按「标识 + 边长」缓存，键的数量
// 是用户数乘尺寸档数，正常远小于这个数；超了整份丢掉重建，不做逐项淘汰。
const identiconCacheMax = 4096

// identiconArt 是一张按边长算好的图案：底色与若干图形。
type identiconArt struct {
	bg     ui.Color
	pieces []identiconPiece
}

// identiconPiece 是一个图形：path 不为 nil 时画路径，否则画圆角矩形。
type identiconPiece struct {
	rect   ui.Rect
	radius float32
	path   *ui.Path
	color  ui.Color
}

// paint 把图案画到 (x, y) 处。路径与矩形都按原点 0 算好，平移交给画笔的
// 变换，同一份图案在任何位置都能复用。
func (art *identiconArt) paint(p *ui.Painter, x, y float32) {
	p.Transform(ui.Translation(x, y), 0, 0, func() {
		for _, pc := range art.pieces {
			if pc.path != nil {
				p.FillPath(pc.path, pc.color)
				continue
			}
			p.Fill(pc.rect, pc.color, pc.radius)
		}
	})
}

// identiconKey 是缓存的键：规范化后的标识与取整后的边长。
type identiconKey struct {
	seed string
	size int
}

var identiconCache = struct {
	sync.Mutex
	items map[identiconKey]*identiconArt
}{items: map[identiconKey]*identiconArt{}}

// identiconTone 是一组头像配色：淡底来自 Lobe 的 Fill，图形用同色系的
// Active 实色，保证浅底上的深色图形始终清楚。
type identiconTone struct {
	fill, ink ui.Color
}

// identiconPalette 固定取 Lobe 浅色 token：头像代表用户身份，不能因为
// 应用切深浅色就换一套图；浅色 token 的淡底叠在 BgContainer 上得到不透明底色。
var identiconPalette = func() []identiconTone {
	tk := lobeCfg.Tokens(false)
	entries := []identiconTone{
		{tk.RedFillTertiary, tk.RedActive},
		{tk.OrangeFillTertiary, tk.OrangeActive},
		{tk.GoldFillTertiary, tk.GoldActive},
		{tk.GreenFillTertiary, tk.GreenActive},
		{tk.CyanFillTertiary, tk.CyanActive},
		{tk.BlueFillTertiary, tk.BlueActive},
		{tk.PurpleFillTertiary, tk.PurpleActive},
		{tk.MagentaFillTertiary, tk.MagentaActive},
	}
	for i := range entries {
		entries[i].fill = entries[i].fill.Over(tk.BgContainer)
	}
	return entries
}()

// identiconNeutral 是还没有标识时的中性圆：Lobe 灰色三级填充叠容器底色。
var identiconNeutral = func() ui.Color {
	tk := lobeCfg.Tokens(false)
	return tk.GrayFillTertiary.Over(tk.BgContainer)
}()

// identiconFor 返回标识在给定边长下的图案。头像在一帧里会被画很多次，
// 同一个标识与边长只算一次，之后直接取缓存。
func identiconFor(seed string, size float32) *identiconArt {
	if size <= 0 {
		size = userAvatarMinSize
	}
	px := int(math.Round(float64(size)))
	if px < 1 {
		px = 1
	}
	seed = strings.ToLower(strings.TrimSpace(seed))
	k := identiconKey{seed: seed, size: px}

	identiconCache.Lock()
	art, ok := identiconCache.items[k]
	identiconCache.Unlock()
	if ok {
		return art
	}

	art = buildIdenticon(seed, float32(px))

	identiconCache.Lock()
	if len(identiconCache.items) >= identiconCacheMax {
		identiconCache.items = map[identiconKey]*identiconArt{}
	}
	identiconCache.items[k] = art
	identiconCache.Unlock()
	return art
}

// buildIdenticon 按标识的 SHA-256 摘要画图案：摘要决定 Lobe 配色和每格图形，
// 因此完全确定，换机器、换版本都不会变。
func buildIdenticon(seed string, size float32) *identiconArt {
	if seed == "" {
		// 还没有标识（昵称没读到）：只画一个中性圆，不编造一张属于
		// 别人的图案。
		return &identiconArt{bg: identiconNeutral}
	}
	sum := sha256.Sum256([]byte(seed))
	tone := identiconPalette[int(sum[0])%len(identiconPalette)]
	art := &identiconArt{bg: tone.fill}
	ink := tone.ink

	pad := size * identiconPadding
	cell := (size - 2*pad) / identiconGrid

	// 底纹：一半的图案在中部铺一块介于底色与图形之间的色块，图形压在
	// 它上面，图案就有了层次，不会只剩一层剪影。
	if sum[10]&0x80 != 0 {
		mid := ink.Mix(tone.fill, 0.45)
		x, y := pad+cell, pad+cell
		switch sum[10] % 3 {
		case 0:
			art.pieces = append(art.pieces, identiconPiece{
				rect: ui.Rect{X: x, Y: y, W: cell * 2, H: cell * 2}, radius: cell, color: mid,
			})
		case 1:
			art.pieces = append(art.pieces, identiconPiece{
				rect: ui.Rect{X: x, Y: y, W: cell * 2, H: cell * 2}, radius: cell * 0.28, color: mid,
			})
		default:
			art.pieces = append(art.pieces, identiconPiece{
				path: identiconDiamond(x+cell, y+cell, cell*1.02), color: mid,
			})
		}
	}

	for gy := 0; gy < identiconGrid; gy++ {
		for gx := 0; gx < identiconGrid/2; gx++ {
			b := sum[1+gy*(identiconGrid/2)+gx]
			for _, col := range [2]int{gx, identiconGrid - 1 - gx} {
				x := pad + float32(col)*cell
				y := pad + float32(gy)*cell
				art.pieces = append(art.pieces, identiconCell(b, x, y, cell, ink)...)
			}
		}
	}
	return art
}

// identiconCell 画一格。低三位选图形（0、1 是空格，让图案疏密有致），高位
// 选朝向，两者互不干扰。图形统一内缩一点，相邻两格的图形不会粘成一整块。
func identiconCell(b byte, x, y, cell float32, ink ui.Color) []identiconPiece {
	rot := int(b>>3) & 3
	inset := cell * 0.05
	x, y, cell = x+inset, y+inset, cell-2*inset
	switch b % 8 {
	case 2:
		return []identiconPiece{{
			rect: ui.Rect{X: x, Y: y, W: cell, H: cell}, radius: cell * 0.2, color: ink,
		}}
	case 3:
		return []identiconPiece{{
			rect: ui.Rect{X: x, Y: y, W: cell, H: cell}, radius: cell / 2, color: ink,
		}}
	case 4:
		return []identiconPiece{{path: identiconTriangle(x, y, cell, rot), color: ink}}
	case 5:
		return []identiconPiece{{path: identiconLeaf(x, y, cell, rot), color: ink}}
	case 6:
		return []identiconPiece{{path: identiconDiamond(x+cell/2, y+cell/2, cell*0.54), color: ink}}
	case 7:
		// 居中的小圆点：小尺寸下只是个小点，比一圈细环干净。
		return []identiconPiece{{
			rect:   ui.Rect{X: x + cell*0.28, Y: y + cell*0.28, W: cell * 0.44, H: cell * 0.44},
			radius: cell * 0.22, color: ink,
		}}
	}
	return nil
}

// identiconLeaf 是格子里贴着一条边的四分之一圆，按 90 度的整数倍换到四角
// 之一：比半块矩形更像花纹，不会看起来像一根随机的条。
func identiconLeaf(x, y, cell float32, rot int) *ui.Path {
	// 圆心在这个角，圆弧从相邻的一条边走到另一条边，控制点落在对角上。
	cx, cy := identiconRotate(0, 0, rot)
	ax, ay := identiconRotate(1, 0, rot)
	bx, by := identiconRotate(0, 1, rot)
	qx, qy := identiconRotate(1, 1, rot)
	p := &ui.Path{}
	p.MoveTo(x+cx*cell, y+cy*cell)
	p.LineTo(x+ax*cell, y+ay*cell)
	p.QuadTo(x+qx*cell, y+qy*cell, x+bx*cell, y+by*cell)
	p.Close()
	return p
}

// identiconTriangle 是格子里指向右的等腰三角形，按 90 度的整数倍绕格心
// 旋转。
func identiconTriangle(x, y, cell float32, rot int) *ui.Path {
	pts := [3][2]float32{{0, 0}, {1, 0.5}, {0, 1}}
	p := &ui.Path{}
	for i, pt := range pts {
		u, v := identiconRotate(pt[0], pt[1], rot)
		if i == 0 {
			p.MoveTo(x+u*cell, y+v*cell)
			continue
		}
		p.LineTo(x+u*cell, y+v*cell)
	}
	p.Close()
	return p
}

// identiconDiamond 是以 (cx, cy) 为心、半对角线为 r 的菱形。
func identiconDiamond(cx, cy, r float32) *ui.Path {
	p := &ui.Path{}
	p.MoveTo(cx, cy-r).LineTo(cx+r, cy).LineTo(cx, cy+r).LineTo(cx-r, cy).Close()
	return p
}

// identiconRotate 把格子里的归一化坐标绕格心顺时针转 rot 个 90 度。
func identiconRotate(u, v float32, rot int) (float32, float32) {
	switch rot & 3 {
	case 1:
		return 1 - v, u
	case 2:
		return 1 - u, 1 - v
	case 3:
		return v, 1 - u
	}
	return u, v
}
