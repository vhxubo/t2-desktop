package main

import (
	"image"
	"testing"

	"github.com/egoist/mygo/ui"
)

// userCardRadarApp 造一份六维属性已读到的资料卡，并切到「属性雷达」页签。
// 属性来自 GET /v1/users/:userName 的 user_attributes，不依赖 about 请求。
func userCardRadarApp(t *testing.T) (*app, *userCardState) {
	t.Helper()
	a := userCardTestApp(t)
	cs := a.userCard
	cs.user.Attributes = []profileAttribute{
		{Key: "craft_attr", Label: "技艺", Value: 10},
		{Key: "diligence_attr", Label: "勤勉", Value: 5},
		{Key: "charm_attr", Label: "魅力", Value: 2},
		{Key: "experience_attr", Label: "阅历", Value: 3},
		{Key: "karma_attr", Label: "财富", Value: 7},
		{Key: "luck_attr", Label: "幸运", Value: 9},
	}
	cs.user.AttributesKnown = true
	cs.tab = userCardTabAttrs
	return a, cs
}

// 「属性雷达」页签：标签栏里有入口，页面上画出雷达图与六项数值，数值取自
// user_attributes 而不是 about 的统计占位符。
func TestUserCardRadarTab(t *testing.T) {
	a, cs := userCardRadarApp(t)
	// about 还没读到时属性照样能看：两者是不同请求。
	cs.aboutLoaded = false
	cs.about.Stats = map[string]string{"craft_attr": "1"}
	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()

	if !tt.HasText("属性雷达") {
		t.Fatalf("标签栏没有「属性雷达」：%q", tt.Texts())
	}
	if !tt.HasText("六维属性") {
		t.Errorf("属性页没有标题：%q", tt.Texts())
	}
	// 右侧数值列表：六项名字与数值都在，数值来自 user_attributes。
	for _, want := range []string{"技艺", "勤勉", "魅力", "阅历", "财富", "幸运"} {
		if !tt.HasText(want) {
			t.Errorf("属性页缺少 %q：%q", want, tt.Texts())
		}
	}
	for _, want := range []string{"10", "5", "2", "3", "7", "9"} {
		if !tt.HasText(want) {
			t.Errorf("属性页缺少数值 %q：%q", want, tt.Texts())
		}
	}
	if !tt.HasText("单项上限 10") {
		t.Errorf("属性页没有说明满格值：%q", tt.Texts())
	}

	chart, ok := tt.Find("六维属性雷达图")
	if !ok {
		t.Fatalf("没有画出雷达图：%q", tt.Texts())
	}
	if chart.W != profileAttributeRadarSize || chart.H != profileAttributeRadarSize {
		t.Errorf("雷达图尺寸 = %.0f×%.0f，期望 %.0f×%.0f",
			chart.W, chart.H, profileAttributeRadarSize, profileAttributeRadarSize)
	}
	if chart.X < 0 || chart.X+chart.W > userCardWindowWidth {
		t.Errorf("雷达图超出台宽：x=%.0f 宽 %.0f", chart.X, chart.W)
	}
	// 图形用强调色画出来，不能只是一块空白。
	if n := countUserCardAccent(tt.Image(), chart); n < 100 {
		t.Errorf("雷达图里没有画出强调色的线：%d 像素", n)
	}
}

// 窄窗口下雷达图与右侧数值仍在窗口内，不会被挤到窗口外。
func TestUserCardRadarFitsMinWidth(t *testing.T) {
	a, _ := userCardRadarApp(t)
	tt := ui.NewTester(a.userCardWindowView, userCardMinWidth, userCardMinHeight)
	tt.Frame()

	chart, ok := tt.Find("六维属性雷达图")
	if !ok {
		t.Fatalf("窄窗口下没有雷达图：%q", tt.Texts())
	}
	if chart.X < 0 || chart.X+chart.W > userCardMinWidth {
		t.Errorf("窄窗口下雷达图超出台宽：x=%.0f 宽 %.0f", chart.X, chart.W)
	}
	for _, label := range []string{"技艺", "勤勉", "魅力", "阅历", "财富", "幸运"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("窄窗口下缺少属性 %q：%q", label, tt.Texts())
		}
		if r.X+r.W > userCardMinWidth {
			t.Errorf("窄窗口下 %q 超出右边：x=%.0f 宽 %.0f", label, r.X, r.W)
		}
	}
}

// 从列表页点「属性雷达」页签就能看到图，不需要先切状态；默认页不画属性。
func TestUserCardRadarTabSwitch(t *testing.T) {
	a, _ := userCardRadarApp(t)
	a.userCard.tab = userCardTabTopics
	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if _, ok := tt.Find("六维属性雷达图"); ok {
		t.Fatal("默认页不该画属性雷达")
	}
	if err := tt.Click("属性雷达"); err != nil {
		t.Fatalf("点「属性雷达」页签失败：%v", err)
	}
	tt.Frame()
	if a.userCard.tab != userCardTabAttrs {
		t.Errorf("点页签后选中的是 %d，期望 %d", a.userCard.tab, userCardTabAttrs)
	}
	if _, ok := tt.Find("六维属性雷达图"); !ok {
		t.Fatalf("切到属性页后没有雷达图：%q", tt.Texts())
	}
}

// 属性没读到时说明原因，不画一张全 0 的假图：读取中、没有数据、读取失败
// 三种状态各有文案。
func TestUserCardRadarStates(t *testing.T) {
	a := userCardTestApp(t)
	cs := a.userCard
	cs.tab = userCardTabAttrs
	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if !tt.HasText("没有公开属性") {
		t.Errorf("没有 user_attributes 时应说明没有属性：%q", tt.Texts())
	}
	if _, ok := tt.Find("六维属性雷达图"); ok {
		t.Error("没有属性数据时不该画雷达图")
	}

	// 资料还在读：转圈说明。
	a = userCardTestApp(t)
	cs = a.userCard
	cs.tab = userCardTabAttrs
	cs.profileLoaded = false
	cs.profileLoading = true
	tt = ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if !tt.HasText("正在读取属性…") {
		t.Errorf("读取中缺少说明：%q", tt.Texts())
	}

	// 资料读取失败：说明不可用，并给出原因。
	a = userCardTestApp(t)
	cs = a.userCard
	cs.tab = userCardTabAttrs
	cs.profileLoaded = false
	cs.profileErr = "网络不可用"
	tt = ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	for _, want := range []string{"属性暂时不可用", "网络不可用"} {
		if !tt.HasText(want) {
			t.Errorf("读取失败时缺少 %q：%q", want, tt.Texts())
		}
	}
}

// countUserCardAccent 数雷达图矩形里接近强调色（#2563eb）的像素。
func countUserCardAccent(img *image.RGBA, r ui.Rect) int {
	if img == nil {
		return 0
	}
	x0, y0 := max(int(r.X), img.Bounds().Min.X), max(int(r.Y), img.Bounds().Min.Y)
	x1, y1 := min(int(r.X+r.W), img.Bounds().Max.X), min(int(r.Y+r.H), img.Bounds().Max.Y)
	n := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			rr, gg, bb, _ := img.At(x, y).RGBA()
			if bb>>8 > 190 && rr>>8 < 90 && gg>>8 < 150 {
				n++
			}
		}
	}
	return n
}
