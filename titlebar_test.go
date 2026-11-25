package main

import (
	"image"
	"testing"

	"github.com/egoist/mygo/ui"
)

// openAccountMenu 点标题栏「个人中心」打开账号菜单气泡，并跑一帧让气泡画出来。
func openAccountMenu(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("个人中心"); err != nil {
		t.Fatalf("点账号菜单失败：%v", err)
	}
	tt.Frame()
}

// lum is the perceived brightness of a pixel, 0 (black) to 255 (white).
func lum(im *image.RGBA, x, y int) float64 {
	c := im.RGBAAt(x, y)
	return 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
}

func darkAt(im *image.RGBA, x, y int) bool { return lum(im, x, y) < 160 }

// lightLum 是浅色页面底色的亮度：取自 Lobe 的 BgLayout，不写死纯白。
func lightLum() float64 {
	c := lobeCfg.Tokens(false).BgLayout
	return 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
}

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

// 标题栏横跨窗口顶端，侧边栏放在它下面：开关在标题栏最左侧，侧边栏首行紧跟
// 标题栏下沿，横向只有标题栏底下那一条分割线，竖分隔线仍在两栏之间。
func TestCollapseToggleInTitleBarWithoutSidebarHeader(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	toggle, ok := tt.Find("展开侧边栏")
	if !ok {
		t.Fatalf("找不到折叠开关：%q", tt.Texts())
	}
	if toggle.Y < 0 || toggle.Y+toggle.H > float32(titleBarHeight) {
		t.Errorf("折叠开关不在标题栏里：%+v", toggle)
	}
	home, ok := tt.Find("首页")
	if !ok {
		t.Fatalf("侧边栏首行导航不见了：%q", tt.Texts())
	}
	if want := float32(titleBarHeight) + 1 + sidebarNavInsetTop; home.Y < want-1 || home.Y > want+1 {
		t.Errorf("侧边栏首行没有留出顶部内边距：首页 y=%.0f，期望 %.0f", home.Y, want)
	}
}

// The title bar is drawn by the view, so it must take the page's colors: the
// whole app uses the Lobe light theme, and a bar painted with the stock light
// theme's grey surface showed as a grey band across the page.
func TestTitleBarSharesTheLightPageTheme(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	im := tt.Image()
	// 取标题栏中段的一处空白底色。搜索框宽窗口时会居中（见
	// TestTitleBarSearchFollowsWindowWidth），x=400 落在框内，不能拿来量底色。
	for _, p := range [][2]int{
		{700, 1},
		{700, titleBarHeight / 2},
		{700, titleBarHeight - 1},
		{700, titleBarHeight + 4}, // 分隔线下的页面
	} {
		if got := lum(im, p[0], p[1]); got < lightLum()-1 {
			t.Errorf("(%d,%d) 的亮度 %.0f，标题栏与页面应同为浅色底色", p[0], p[1], got)
		}
	}
	// 底边是一条分隔线，而不是把灰带和页面隔开的白线。
	c := im.RGBAAt(700, titleBarHeight)
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
			if got := lum(im, 700, y); got > 80 {
				t.Errorf("深色系统主题下 %s 页 y=%d 亮度 %.0f，标题栏与页面应同为深色", page, y, got)
			}
		}
	}
}

// 固定浅色时不再跟随系统：深色系统下仍保持浅色标题栏与页面，用户手动
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
			if got := lum(im, 700, y); got < lightLum()-1 {
				t.Errorf("固定浅色时 %s 页 y=%d 亮度 %.0f，应仍为浅色底色", page, y, got)
			}
		}
	}
}

// 品牌名的位置换成了带输入框的搜索：输入框贴在标题栏里，输入即同步到搜索
// 状态，搜索窗口据此出结果（见 search_ui.go）。
func TestTitleBarSearchInput(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()

	box, ok := tt.Find("搜索关键词")
	if !ok {
		t.Fatalf("标题栏里没有搜索输入框：%q", tt.Texts())
	}
	if box.Y < 0 || box.Y+box.H > titleBarHeight {
		t.Errorf("搜索框不在标题栏里：%+v", box)
	}
	// 搜索框上下留出边距，不再贴着标题栏的上下边缘。
	if top := box.Y; top < titleSearchInsetY-1 || top > titleSearchInsetY+1 {
		t.Errorf("搜索框上边距 = %.0f，期望约 %d", top, titleSearchInsetY)
	}
	if bottom := float32(titleBarHeight) - (box.Y + box.H); bottom < titleSearchInsetY-1 || bottom > titleSearchInsetY+1 {
		t.Errorf("搜索框下边距 = %.0f，期望约 %d", bottom, titleSearchInsetY)
	}
	if box.W < 120 || box.W > 400 {
		t.Errorf("标题栏搜索框宽度 = %.0f，期望是个正常的输入框宽度", box.W)
	}
	// 标题栏不再画品牌名；应用名仍留在侧边栏（展开时可见，见 sidebar_test.go）。
	if r, ok := tt.Find("T2 Desktop"); ok && r.Y >= 0 && r.Y < titleBarHeight {
		t.Errorf("标题栏还留着品牌名：%+v", r)
	}

	// 点进去打字，文字留在标题栏自己的状态里（回车才交给搜索窗口）。
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Type("职场")
	tt.Frame()
	if a.palette.text != "职场" {
		t.Fatalf("标题栏搜索框没有收到关键词：%q", a.palette.text)
	}
}

// 标题栏搜索框聚焦后不该冒出一圈强调色高亮边框：mygo 的 field 默认在聚焦时
// 把边框改成 Accent 蓝（见 inputBorder），这里用 0 宽边框彻底禁掉描边。
func TestTitleBarSearchHasNoFocusBorder(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()

	box, ok := tt.Find("搜索关键词")
	if !ok {
		t.Fatalf("标题栏里没有搜索输入框：%q", tt.Texts())
	}
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	im := tt.Image()
	// 扫搜索框四条边：不该出现任何强调色（偏蓝）的像素。
	edges := []struct{ x0, y0, x1, y1 int }{
		{int(box.X), int(box.Y), int(box.X + box.W), int(box.Y) + 1},
		{int(box.X), int(box.Y+box.H) - 1, int(box.X + box.W), int(box.Y + box.H)},
		{int(box.X), int(box.Y), int(box.X) + 1, int(box.Y + box.H)},
		{int(box.X+box.W) - 1, int(box.Y), int(box.X + box.W), int(box.Y + box.H)},
	}
	for _, e := range edges {
		for y := e.y0; y < e.y1; y++ {
			for x := e.x0; x < e.x1; x++ {
				if x < 0 || y < 0 || x >= im.Rect.Max.X || y >= im.Rect.Max.Y {
					continue
				}
				if c := im.RGBAAt(x, y); accentTint(c) {
					t.Fatalf("聚焦后搜索框出现强调色边框：(%d,%d)=%v", x, y, c)
				}
			}
		}
	}
}

// 搜索框始终居中在整扇窗口的水平中线上：左段（折叠开关）与右段（铃铛、
// 账号标识、窗口按钮）等宽伸缩，与窗口宽度无关，拖动改变窗口大小也不会
// 让它偏向某一侧。宽度仍随窗口缩放，但被限制在上下限内。
func TestTitleBarSearchAlwaysCentered(t *testing.T) {
	a := testApp(t)

	var narrowW float32
	for i, w := range []int{900, 1080, 1440} {
		tt := ui.NewTester(a.view, w, 720)
		tt.Frame()
		s, ok := tt.Find("搜索关键词")
		if !ok {
			t.Fatalf("窗口 %d 里没有搜索框：%q", w, tt.Texts())
		}
		if _, ok := tt.Find("通知"); !ok {
			t.Fatalf("窗口 %d 里没有通知铃铛：%q", w, tt.Texts())
		}
		// 两端 Grow(1)、basis 0，中线就是窗口宽的一半。
		if got, want := s.X+s.W/2, float32(w)/2; got < want-8 || got > want+8 {
			t.Errorf("窗口 %d 搜索框没有居中：中心=%.0f 期望≈%.0f（%v）", w, got, want, s)
		}
		if want := titleSearchWidthFor(float32(w)); s.W < want-2 || s.W > want+2 {
			t.Errorf("窗口 %d 搜索框宽度 = %.0f，按窗口比例应约为 %.0f", w, s.W, want)
		}
		if s.W > titleSearchWidthMax {
			t.Errorf("窗口 %d 搜索框宽度 %.0f 超过上限 %d", w, s.W, titleSearchWidthMax)
		}
		if i == 0 {
			narrowW = s.W
		} else if s.W <= narrowW+10 {
			t.Errorf("搜索框宽度没有随窗口变宽：窄 %.0f 宽 %.0f", narrowW, s.W)
		}
	}
}

// 输入框为空时显示的 hint 居中在搜索框内，且不拦截点击；一旦输入内容，
// hint 让位给文字，已输入的内容仍从左侧开始。
func TestTitleBarSearchHintCentered(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()

	box, ok := tt.Find("搜索关键词")
	if !ok {
		t.Fatalf("标题栏里没有搜索输入框：%q", tt.Texts())
	}
	hint, ok := tt.Find(titleSearchHint)
	if !ok {
		t.Fatalf("输入框里没有居中 hint：%q", tt.Texts())
	}
	// hint 居中在放大镜右边的文字区，比整条输入框中心偏右半个图标宽。
	if got, want := hint.X+hint.W/2, box.X+box.W/2+titleSearchIconInset/2; got < want-8 || got > want+8 {
		t.Errorf("hint 没有居中在输入框文字区：hint 中心=%.0f 期望≈%.0f", got, want)
	}

	// 点进输入框打字：hint 消失，文字进到标题栏自己的状态里。
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Type("职场")
	tt.Frame()
	if a.palette.text != "职场" {
		t.Fatalf("标题栏搜索框没有收到关键词：%q", a.palette.text)
	}
	if _, ok := tt.Find(titleSearchHint); ok {
		t.Errorf("输入后 hint 仍显示在输入框里：%q", tt.Texts())
	}
}

// The window buttons are drawn with vector lines inside their buttons. They
// used to be font characters, which render tiny and, for the restore glyph,
// can be missing altogether; a coordinate mistake in Draw also painted them
// over the search box.
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
	if want := 2 * titleGlyphHalf; dash < want-2 || dash > want+2 {
		t.Errorf("最小化图标宽 %d 像素（第 %d 行），期望约 %d", dash, dashRow, want)
	}
	if dashRow < titleBarHeight/2-3 || dashRow > titleBarHeight/2+3 {
		t.Errorf("最小化图标在第 %d 行，没有竖向居中", dashRow)
	}

	// 最大化：方框的四条边在按钮中心之外，中心是空的。坐标跟着矢量图标
	// 的半边长走，改标题栏尺寸时这条断言不会写死旧的 5px。
	cx, cy := int(maxR.X+maxR.W/2), int(maxR.Y+maxR.H/2)
	if !darkAt(im, cx, cy-titleGlyphHalf) {
		t.Error("最大化图标的上边没有画出来")
	}
	if !darkAt(im, cx-titleGlyphHalf, cy) {
		t.Error("最大化图标的左边没有画出来")
	}
	if darkAt(im, cx, cy) {
		t.Error("最大化图标中心应该是空的")
	}

	// 关闭：叉的两条对角线穿过按钮中心。
	if !darkAt(im, int(closeR.X+closeR.W/2), cy) {
		t.Error("关闭图标的中心没有画出叉")
	}

	// 图标必须落在各自的按钮里：搜索框与标题栏右侧的控件（通知铃铛、账号区）
	// 之间不能出现窗口图标的墨迹。更新按钮默认不露出，只在发现新版本等状态出现。
	// 文字与输入框本身当然会有墨迹，所以扫描范围从搜索框右缘之后开始，右边界取
	// 最左侧那个控件的左边，而不是写死 700。
	start := 200
	if r, ok := tt.Find("搜索关键词"); ok && int(r.X+r.W)+4 > start {
		start = int(r.X+r.W) + 4
	}
	limit := 700
	for _, name := range []string{"通知"} {
		if r, ok := tt.Find(name); ok && int(r.X)-6 < limit {
			limit = int(r.X) - 6
		}
	}
	for y := 0; y < titleBarHeight; y++ {
		for x := start; x < limit; x++ {
			if darkAt(im, x, y) {
				t.Fatalf("标题栏 (%d,%d) 有多余墨迹：窗口图标画到了按钮外面", x, y)
			}
		}
	}
}

// 关闭键悬停用 Lobe 错误色铺底、白色叉子；另外两个键只换主题的悬停底色。
func TestTitleBarButtonHover(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	closeR := titleRect(t, tt, "关闭")
	tt.Move(closeR.X+closeR.W/2, closeR.Y+closeR.H/2)
	tt.Frame()
	im := tt.Image()
	c := im.RGBAAt(int(closeR.X)+3, int(closeR.Y)+3)
	tk := lobeCfg.Tokens(false)
	if c.R != tk.Error.R || c.G != tk.Error.G || c.B != tk.Error.B {
		t.Errorf("悬停关闭按钮底色 = #%02x%02x%02x，期望 Lobe Error %s", c.R, c.G, c.B, hexToken(tk.Error))
	}
	d := im.RGBAAt(int(closeR.X+closeR.W/2), int(closeR.Y+closeR.H/2))
	// 叉子应为白色：红底上的白色矢量叉在抗锯齿后，中心像素会略低于纯白
	// （实测约 #feefef），只要绿、蓝通道明显高于红底（不是把叉画成红色或
	// 漏画）就说明是白叉，避免被渲染器的抗锯齿细节误伤。
	if d.R < tk.White.R-0x10 || d.G < tk.White.G-0x30 || d.B < tk.White.B-0x30 {
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
	if c.R == tk.Error.R && c.G == tk.Error.G && c.B == tk.Error.B { // 只有关闭按钮才是红的
		t.Errorf("最大化按钮悬停变成了红色：%v", c)
	}
}

// The window buttons are reachable controls rather than plain drawings. 登录与
// 设置都不在这里：它们在主页侧边栏的底部（见 sidebar_test.go）。退出登录也不
// 直接摆在标题栏里，它收在登录后的账号菜单气泡中（见 profile_test.go）。
func TestTitleBarWindowButtonsClick(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()

	for _, label := range []string{"最小化", "最大化", "关闭"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("窗口按钮「%s」不可点（没有可访问名）", label)
		}
	}
	for _, label := range []string{"登录", "设置"} {
		if r, ok := tt.Find(label); ok && r.Y < titleBarHeight {
			t.Errorf("「%s」不该再出现在标题栏里（y=%.0f）", label, r.Y)
		}
	}
}

// 登录成功后标题栏显示用户名与头像，且不再回到登录页。「已登录」这三个字
// 不再出现：头像和昵称本身就是登录状态。
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
	if !tt.HasText("jimmy") {
		t.Errorf("标题栏没有显示用户名：%q", tt.Texts())
	}
	if tt.HasText("已登录") {
		t.Errorf("标题栏不应再显示「已登录」：%q", tt.Texts())
	}
	// 头像在图片加载完成前画按用户名生成的图案，仍是一个可命名的头像控件。
	if _, ok := tt.Find("jimmy"); !ok {
		t.Errorf("标题栏没有头像控件：%q", tt.Texts())
	}
	// 头像可点，打开「个人中心」窗口。
	if _, ok := tt.Find("个人中心"); !ok {
		t.Errorf("标题栏头像没有可访问名：%q", tt.Texts())
	}
}

// 标题栏的头像与昵称与标准栏高配套：整块标签落在标题栏内，高度与搜索框
// 同一量级，头像用标题栏这一档的字号，不会在 32 DIP 的栏里显得突兀。
func TestTitleBarAccountTagScalesWithBar(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", Username: "jimmy", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.onAuthChanged(nil)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()

	tag, ok := tt.Find("个人中心")
	if !ok {
		t.Fatalf("找不到账号标签：%q", tt.Texts())
	}
	if tag.Y < 0 || tag.Y+tag.H > float32(titleBarHeight) {
		t.Errorf("账号标签不在标题栏里：%+v", tag)
	}
	search, ok := tt.Find("搜索关键词")
	if !ok {
		t.Fatalf("找不到标题栏搜索框：%q", tt.Texts())
	}
	if tag.H < search.H-1 {
		t.Errorf("账号标签高 %.0f，明显矮于搜索框 %.0f", tag.H, search.H)
	}
	avatar, ok := tt.Find("jimmy")
	if !ok {
		t.Fatalf("找不到标题栏头像：%q", tt.Texts())
	}
	if want := userAvatarSize(12); avatar.W < want-0.5 {
		t.Errorf("标题栏头像宽 %.0f，期望至少 %.0f", avatar.W, want)
	}
}
