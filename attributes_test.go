package main

import (
	"image"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

/* ------------------------------------------------------------------
   解码与几何
---------------------------------------------------------------- */

// user_attributes 的六项按固定顺序解出来：字段名、中文名、数值都要对上。
func TestDecodeProfileAttributes(t *testing.T) {
	p := decodeAccountProfile([]byte(`{
		"username": "vhxubo",
		"user_attributes": {
			"experience_attr": "4", "diligence_attr": "7", "karma_attr": "6",
			"charm_attr": "5", "craft_attr": "8", "luck_attr": "6"
		}
	}`))
	if !p.AttributesKnown {
		t.Fatal("资料里带了 user_attributes，却没有标记为已知")
	}
	want := []struct {
		key, label string
		value      int
	}{
		{"karma_attr", "财富", 6},
		{"experience_attr", "阅历", 4},
		{"charm_attr", "魅力", 5},
		{"luck_attr", "幸运", 6},
		{"diligence_attr", "勤勉", 7},
		{"craft_attr", "技艺", 8},
	}
	if len(p.Attributes) != len(want) {
		t.Fatalf("属性数量 = %d，期望 %d：%+v", len(p.Attributes), len(want), p.Attributes)
	}
	for i, w := range want {
		got := p.Attributes[i]
		if got.Key != w.key || got.Label != w.label || got.Value != w.value {
			t.Errorf("第 %d 项 = %+v，期望 %s/%s/%d", i, got, w.key, w.label, w.value)
		}
	}

	// 没有这个字段：不展示属性块，而不是画一个全是 0 的图。
	if p := decodeAccountProfile([]byte(`{"username":"x"}`)); p.AttributesKnown ||
		len(p.Attributes) != 0 {
		t.Errorf("没有 user_attributes 时应标记为未知：%+v", p)
	}

	// 只给了一项时按 0 补齐其余轴，保证雷达图还是六边形。
	p = decodeAccountProfile([]byte(`{"username":"x",
		"user_attributes":{"luck_attr":"3"}}`))
	if !p.AttributesKnown || len(p.Attributes) != 6 {
		t.Fatalf("部分属性没有补齐：%+v", p)
	}
	if p.Attributes[3].Label != "幸运" || p.Attributes[3].Value != 3 ||
		p.Attributes[0].Value != 0 {
		t.Errorf("部分属性的值不对：%+v", p.Attributes)
	}

	// 数字而不是字符串也认；顶层直接摊开时同样兼容。
	p = decodeAccountProfile([]byte(`{"username":"x","charm_attr":9}`))
	if !p.AttributesKnown || len(p.Attributes) != 6 || p.Attributes[2].Value != 9 {
		t.Errorf("顶层属性或数字值没有解出来：%+v", p)
	}
}

// 雷达图按 0～10 换算：负值贴中心，满格贴外圈，超过满格不外溢。
func TestProfileAttributeLevel(t *testing.T) {
	cases := []struct {
		value int
		want  float32
	}{
		{-3, 0}, {0, 0}, {1, 0.1}, {5, 0.5}, {10, 1}, {12, 1},
	}
	for _, c := range cases {
		if got := profileAttributeLevel(c.value); math.Abs(float64(got-c.want)) > 1e-6 {
			t.Errorf("profileAttributeLevel(%d) = %v，期望 %v", c.value, got, c.want)
		}
	}
}

// 第一条轴朝正上方，六条轴顺时针均分；顶点位置决定雷达图的朝向。
func TestProfileAttributePoint(t *testing.T) {
	cx, cy, radius := float32(100), float32(100), float32(60)
	x, y := radarPoint(cx, cy, radius, 0, 6)
	if math.Abs(float64(x-cx)) > 0.01 || math.Abs(float64(y-(cy-radius))) > 0.01 {
		t.Errorf("第一条轴没有朝上：(%v, %v)", x, y)
	}
	x, y = radarPoint(cx, cy, radius, 1, 6)
	wantX := cx + radius*float32(math.Cos(-math.Pi/2+math.Pi/3))
	wantY := cy + radius*float32(math.Sin(-math.Pi/2+math.Pi/3))
	if math.Abs(float64(x-wantX)) > 0.01 || math.Abs(float64(y-wantY)) > 0.01 {
		t.Errorf("第二条轴的角度不对：(%v, %v)，期望 (%v, %v)", x, y, wantX, wantY)
	}
}

/* ------------------------------------------------------------------
   界面
---------------------------------------------------------------- */

func attributeProfileApp(t *testing.T) *app {
	t.Helper()
	a := profileWindowApp(t)
	a.profile.user.Attributes = []profileAttribute{
		{Key: "karma_attr", Label: "财富", Value: 6},
		{Key: "experience_attr", Label: "阅历", Value: 4},
		{Key: "charm_attr", Label: "魅力", Value: 5},
		{Key: "luck_attr", Label: "幸运", Value: 6},
		{Key: "diligence_attr", Label: "勤勉", Value: 7},
		{Key: "craft_attr", Label: "技艺", Value: 8},
	}
	a.profile.user.AttributesKnown = true
	return a
}

// 概览页展示六维属性雷达图与右侧数值列表；雷达图的强调色像素真的画在了
// 画布区域里，不是只有一块空白。
func TestProfileAttributesRadarRenders(t *testing.T) {
	a := attributeProfileApp(t)
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()

	for _, want := range []string{
		"六维属性", "技艺", "勤勉", "魅力", "阅历", "财富", "幸运",
		"单项上限 10",
	} {
		if !tt.HasText(want) {
			t.Errorf("概览页缺少属性文案 %q：%q", want, tt.Texts())
		}
	}
	chart, ok := tt.Find("六维属性雷达图")
	if !ok {
		t.Fatalf("找不到雷达图画布：%q", tt.Texts())
	}
	if chart.X < 0 || chart.X+chart.W > profileWindowWidth {
		t.Errorf("雷达图超出台宽：x=%.0f 宽 %.0f", chart.X, chart.W)
	}
	if n := countAccentPixels(tt.Image(), chart); n < 100 {
		t.Errorf("雷达图区域里的强调色像素只有 %d 个，数据多边形可能没有画出来", n)
	}

	// 窗口缩到最小宽度时，图与右侧数值仍要在窗口内，不能把数值挤出右边。
	small := ui.NewTester(a.profileWindowView, profileWinMinWidth, profileWindowHeight)
	small.Frame()
	chart, ok = small.Find("六维属性雷达图")
	if !ok {
		t.Fatalf("最小宽度下找不到雷达图：%q", small.Texts())
	}
	if chart.X < 0 || chart.X+chart.W > profileWinMinWidth {
		t.Errorf("最小宽度下雷达图超出台宽：x=%.0f 宽 %.0f", chart.X, chart.W)
	}
	for _, label := range []string{"技艺", "勤勉", "魅力", "阅历", "财富", "幸运"} {
		r, ok := small.Find(label)
		if !ok {
			t.Fatalf("最小宽度下缺少属性 %q：%q", label, small.Texts())
		}
		if r.X+r.W > profileWinMinWidth {
			t.Errorf("最小宽度下属性 %q 超出右边：x=%.0f 宽 %.0f", label, r.X, r.W)
		}
	}
}

// 没有属性数据、还在读、读取失败三种状态各自说明，不用假图占位。
func TestProfileAttributesSectionStates(t *testing.T) {
	a := profileWindowApp(t)
	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if !tt.HasText("当前账号没有返回属性数据。") {
		t.Errorf("没有属性数据时缺少说明：%q", tt.Texts())
	}

	a = profileWindowApp(t)
	a.profile.infoLoaded = false
	a.profile.infoLoading = true
	tt = ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if !tt.HasText("正在读取属性…") {
		t.Errorf("读取属性时缺少进度说明：%q", tt.Texts())
	}

	a = profileWindowApp(t)
	a.profile.user = accountProfile{}
	a.profile.infoErr = "网络错误"
	tt = ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if !tt.HasText("属性数据暂时不可用。") {
		t.Errorf("读取失败时缺少说明：%q", tt.Texts())
	}
}

// countAccentPixels 数一块区域里带颜色的像素（R/G/B 明显不等）。雷达图的
// 网格和文字都是灰阶，只有数据多边形与顶点用主题强调色，因此它们能证明
// 图真的画出来了。
func countAccentPixels(img *image.RGBA, r ui.Rect) int {
	if img == nil {
		return 0
	}
	x0, y0 := int(r.X), int(r.Y)
	x1, y1 := int(r.X+r.W), int(r.Y+r.H)
	x0, y0 = max(x0, img.Bounds().Min.X), max(y0, img.Bounds().Min.Y)
	x1, y1 = min(x1, img.Bounds().Max.X), min(y1, img.Bounds().Max.Y)
	n := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			c := img.RGBAAt(x, y)
			hi, lo := max(c.R, max(c.G, c.B)), min(c.R, min(c.G, c.B))
			if hi-lo > 8 {
				n++
			}
		}
	}
	return n
}
