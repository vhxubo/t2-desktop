package main

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// identiconSignature 把一份图案压成可比较的字符串：底色、图形数量，以及
// 每个图形的颜色与位置。路径没有导出的坐标，用包围盒代替。
func identiconSignature(art *identiconArt) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%v|%d", art.bg, len(art.pieces))
	for _, pc := range art.pieces {
		if pc.path != nil {
			fmt.Fprintf(&b, "|p%v%v", pc.color, pc.path.Bounds())
			continue
		}
		fmt.Fprintf(&b, "|r%v%v%.2f", pc.color, pc.rect, pc.radius)
	}
	return b.String()
}

// 图案只由标识决定：同一个标识每次得到同一份图案，标识的大小写与首尾空格
// 不影响结果；不同标识不会画出同一张图。
func TestIdenticonDeterministic(t *testing.T) {
	if got, want := identiconSignature(identiconFor("jimmy", 32)), identiconSignature(identiconFor("jimmy", 32)); got != want {
		t.Error("同一个标识两次算出的图案不一致")
	}
	if got, want := identiconSignature(identiconFor(" JIMMY ", 32)), identiconSignature(identiconFor("jimmy", 32)); got != want {
		t.Errorf("大小写与空格改变了图案：%q != %q", got, want)
	}

	names := []string{
		"jimmy", "alice", "bob", "carol", "dave", "erin", "frank", "grace",
		"heidi", "ivan", "judy", "ken", "leo", "mallory", "niaj", "olivia",
		"peggy", "rupert", "sybil", "trent", "uma", "victor", "wendy", "xavier",
	}
	seen := make(map[string]string, len(names))
	for _, n := range names {
		sig := identiconSignature(identiconFor(n, 32))
		if other, ok := seen[sig]; ok {
			t.Errorf("%q 与 %q 画出了同一张图案", n, other)
		}
		seen[sig] = n
	}
}

// 同一个标识与边长复用同一份图案：头像一帧里要画很多次，不能每帧重算。
func TestIdenticonCacheReuse(t *testing.T) {
	a := identiconFor("jimmy", 32)
	if b := identiconFor("jimmy", 32); a != b {
		t.Error("同一个标识与边长没有复用缓存")
	}
	if c := identiconFor("jimmy", 48); a == c {
		t.Error("不同边长不应共用同一份图案")
	}
}

// 图形都落在头像框内：圆形头像框会把越界的部分裁掉，图形贴边就缺一块。
func TestIdenticonStaysInsideAvatar(t *testing.T) {
	for _, size := range []float32{14, 18, 32, 48} {
		for _, name := range []string{"jimmy", "alice", "bob", "carol", "dave", "erin"} {
			art := identiconFor(name, size)
			if art.bg.A != 255 {
				t.Errorf("%q 在 %.0f 下的底色不透明", name, size)
			}
			for _, pc := range art.pieces {
				box := pc.rect
				if pc.path != nil {
					box = pc.path.Bounds()
				}
				if box.X < 0 || box.Y < 0 || box.X+box.W > size+0.01 || box.Y+box.H > size+0.01 {
					t.Errorf("%q 在 %.0f 下有图形越界：%v", name, size, box)
				}
			}
		}
	}
}

// 还没有标识时只画一个中性圆，不编造一张属于某个人的图案。
func TestIdenticonWithoutSeed(t *testing.T) {
	if art := identiconFor("", 32); len(art.pieces) != 0 {
		t.Errorf("没有标识时不应画图形，实际 %d 个", len(art.pieces))
	}
	if art := identiconFor("   ", 32); len(art.pieces) != 0 {
		t.Errorf("只有空白的标识不应画图形，实际 %d 个", len(art.pieces))
	}
}

// 图案的色相由标识决定，但明度分层是固定的：浅底上压一层深色图形，深色
// 主题下也看得清；中间调的底纹只作层次，不参与这条要求。
func TestIdenticonContrast(t *testing.T) {
	for _, name := range []string{"jimmy", "alice", "bob", "carol", "dave", "erin"} {
		art := identiconFor(name, 32)
		if len(art.pieces) == 0 {
			continue
		}
		bgL, dark := luminance(art.bg), 1.0
		for _, pc := range art.pieces {
			dark = math.Min(dark, luminance(pc.color))
		}
		if bgL-dark < 0.3 {
			t.Errorf("%q 的深色图形与底色明度太接近：%.2f / %.2f", name, dark, bgL)
		}
	}
}

// luminance 是颜色的相对明度，0 黑 1 白。
func luminance(c ui.Color) float64 {
	return 0.299*float64(c.R)/255 + 0.587*float64(c.G)/255 + 0.114*float64(c.B)/255
}
