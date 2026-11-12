package main

import (
	"image"
	"testing"

	"github.com/egoist/mygo/ui"
)

// lum is the perceived brightness of a pixel, 0 (black) to 255 (white).
func lum(im *image.RGBA, x, y int) float64 {
	c := im.RGBAAt(x, y)
	return 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
}

func darkAt(im *image.RGBA, x, y int) bool { return lum(im, x, y) < 160 }

// titleRect fails the test unless the labeled element sits where the title
// bar puts it: at the top edge, exactly titleBarHeight tall.
func titleRect(t *testing.T, tt *ui.Tester, label string) ui.Rect {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("标题栏里找不到「%s」：%q", label, tt.Texts())
	}
	if r.Y != 0 || r.H != titleBarHeight {
		t.Fatalf("「%s」的位置 %v，期望贴顶且高 %d", label, r, titleBarHeight)
	}
	return r
}

// The title bar is drawn by the view, so it must take the page's colors: the
// whole app is the white home theme, and a bar painted with the stock light
// theme's grey surface showed as a grey band across a pure white page.
func TestTitleBarSharesTheWhitePageTheme(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	im := tt.Image()
	for _, p := range [][2]int{
		{400, 1},
		{400, titleBarHeight / 2},
		{400, titleBarHeight - 1},
		{400, titleBarHeight + 4}, // 分隔线下的页面
	} {
		if got := lum(im, p[0], p[1]); got < 250 {
			t.Errorf("(%d,%d) 的亮度 %.0f，标题栏与页面应同为白色", p[0], p[1], got)
		}
	}
	// 底边是一条分隔线，而不是把灰带和页面隔开的白线。
	c := im.RGBAAt(400, titleBarHeight)
	if c.R == 0xff && c.G == 0xff && c.B == 0xff {
		t.Errorf("标题栏底边没有分隔线：%v", c)
	}
}

// 默认「跟随系统」：系统切到深色时，自绘标题栏和页面一起变深。以前固定
// 白底且隐藏原生标题栏，深色系统下会留下一条浅色标题栏压着深色页面。
func TestTitleBarFollowsDarkSystemByDefault(t *testing.T) {
	a := testApp(t)
	for _, page := range []string{"list", "login"} {
		a.pageName = page
		tt := ui.NewTesterWith(a.view, 1080, 720, ui.TesterOptions{Dark: true})
		tt.Frame()
		im := tt.Image()
		for _, y := range []int{titleBarHeight / 2, titleBarHeight + 10} {
			if got := lum(im, 400, y); got > 80 {
				t.Errorf("深色系统主题下 %s 页 y=%d 亮度 %.0f，标题栏与页面应同为深色", page, y, got)
			}
		}
	}
}

// 固定浅色时不再跟随系统：深色系统下仍保持白色标题栏与页面，用户手动
// 选定的外观不会被系统设置覆盖。
func TestTitleBarKeepsForcedLightUnderDarkSystem(t *testing.T) {
	a := testApp(t)
	if err := a.theme.Save(themeLight); err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"list", "login"} {
		a.pageName = page
		tt := ui.NewTesterWith(a.view, 1080, 720, ui.TesterOptions{Dark: true})
		tt.Frame()
		im := tt.Image()
		for _, y := range []int{titleBarHeight / 2, titleBarHeight + 10} {
			if got := lum(im, 400, y); got < 250 {
				t.Errorf("固定浅色时 %s 页 y=%d 亮度 %.0f，应仍为白色", page, y, got)
			}
		}
	}
}

// The window buttons are drawn with vector lines inside their buttons. They
// used to be font characters, which render tiny and, for the restore glyph,
// can be missing altogether; a coordinate mistake in Draw also painted them
// over the brand text.
func TestTitleBarWindowButtonsPaintTheirGlyphs(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	im := tt.Image()

	minR := titleRect(t, tt, "最小化")
	maxR := titleRect(t, tt, "最大化")
	closeR := titleRect(t, tt, "关闭")

	// 最小化：按钮内只有一条横线，宽度接近图标宽度，且竖向居中。
	dashRow, dash := -1, 0
	for y := 0; y < titleBarHeight; y++ {
		n := 0
		for x := int(minR.X); x < int(minR.X+minR.W); x++ {
			if darkAt(im, x, y) {
				n++
			}
		}
		if n > dash {
			dash, dashRow = n, y
		}
	}
	if dash < 8 || dash > 14 {
		t.Errorf("最小化图标宽 %d 像素（第 %d 行），期望约 10", dash, dashRow)
	}
	if dashRow < titleBarHeight/2-3 || dashRow > titleBarHeight/2+3 {
		t.Errorf("最小化图标在第 %d 行，没有竖向居中", dashRow)
	}

	// 最大化：方框的四条边在按钮中心之外，中心是空的。
	cx, cy := int(maxR.X+maxR.W/2), int(maxR.Y+maxR.H/2)
	if !darkAt(im, cx, titleBarHeight/2-5) {
		t.Error("最大化图标的上边没有画出来")
	}
	if !darkAt(im, cx-5, cy) {
		t.Error("最大化图标的左边没有画出来")
	}
	if darkAt(im, cx, cy) {
		t.Error("最大化图标中心应该是空的")
	}

	// 关闭：叉的两条对角线穿过按钮中心。
	if !darkAt(im, int(closeR.X+closeR.W/2), cy) {
		t.Error("关闭图标的中心没有画出叉")
	}

	// 图标必须落在各自的按钮里：品牌与标题栏按钮（「设置」及其右侧的
	// 外观图标、通知铃铛）之间不能出现窗口图标的墨迹。文字与图标本身当然
	// 会有墨迹，所以边界取最左侧文字按钮的左边，而不是写死 700。
	limit := 700
	for _, label := range []string{"设置"} {
		if r, ok := tt.Find(label); ok && int(r.X)-6 < limit {
			limit = int(r.X) - 6
		}
	}
	for y := 0; y < titleBarHeight; y++ {
		for x := 200; x < limit; x++ {
			if darkAt(im, x, y) {
				t.Fatalf("标题栏 (%d,%d) 有多余墨迹：窗口图标画到了按钮外面", x, y)
			}
		}
	}
}

// Hovering the close button paints the system's red with a white cross; the
// other two light up with the theme's hover surface instead.
func TestTitleBarButtonHover(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	closeR := titleRect(t, tt, "关闭")
	tt.Move(closeR.X+closeR.W/2, closeR.Y+closeR.H/2)
	tt.Frame()
	im := tt.Image()
	c := im.RGBAAt(int(closeR.X)+3, int(closeR.Y)+3)
	if c.R != 0xe8 || c.G != 0x11 || c.B != 0x23 {
		t.Errorf("悬停关闭按钮底色 = #%02x%02x%02x，期望 #e81123", c.R, c.G, c.B)
	}
	d := im.RGBAAt(int(closeR.X+closeR.W/2), int(closeR.Y+closeR.H/2))
	if d.R < 0xf0 || d.G < 0xf0 || d.B < 0xf0 {
		t.Errorf("悬停时叉子应为白色，实际 #%02x%02x%02x", d.R, d.G, d.B)
	}

	maxR := titleRect(t, tt, "最大化")
	tt.Move(maxR.X+maxR.W/2, maxR.Y+maxR.H/2)
	tt.Frame()
	im = tt.Image()
	c = im.RGBAAt(int(maxR.X)+3, int(maxR.Y)+3)
	if c.R == 0xff && c.G == 0xff && c.B == 0xff {
		t.Error("悬停最大化按钮没有底色")
	}
	if c.R > 0xf0 && c.G < 0x40 { // 只有关闭按钮才是红的
		t.Errorf("最大化按钮悬停变成了红色：%v", c)
	}
}

// The title bar's own buttons work: 登录 starts the browser authorization in
// place (no login page in between), and the three window buttons are reachable
// controls rather than plain drawings.
func TestTitleBarButtonsClick(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()

	for _, label := range []string{"最小化", "最大化", "关闭"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("窗口按钮「%s」不可点（没有可访问名）", label)
		}
	}
	// 未登录时点「登录」直接进入等待授权，不离开当前页。
	t.Cleanup(a.auth.Cancel)
	if err := tt.Click("登录"); err != nil {
		t.Fatal(err)
	}
	if a.pageName != "list" {
		t.Fatalf("点「登录」不该进登录页，实际页面 = %q", a.pageName)
	}
	if !a.loginPending {
		t.Fatal("点「登录」后没有进入等待授权状态")
	}
	tt.Frame()
	if !tt.HasText("等待浏览器授权…") {
		t.Errorf("标题栏没有显示等待授权：%q", tt.Texts())
	}
}

// 登录成功后标题栏显示用户名与头像，且不再回到登录页。
func TestTitleBarShowsAccountAfterLogin(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", Username: "jimmy", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.pageName = "login"
	a.onAuthChanged(nil)
	if a.pageName != "list" {
		t.Errorf("登录成功后应回到列表，实际 %q", a.pageName)
	}
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if !tt.HasText("已登录 · jimmy") {
		t.Errorf("标题栏没有显示用户名：%q", tt.Texts())
	}
	// 头像在图片加载完成前用姓名首字母，仍是一个可命名的头像控件。
	if _, ok := tt.Find("jimmy"); !ok {
		t.Errorf("标题栏没有头像控件：%q", tt.Texts())
	}
}
