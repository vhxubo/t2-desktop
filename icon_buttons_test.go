package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// iconGlyphCenter 返回 name 按钮里图标墨迹的中心（设备像素）。图标按钮是纯
// 图标，悬停底色铺满整框；图标若偏在一侧，看着就是高亮和按钮错位。
func iconGlyphCenter(t *testing.T, tt *ui.Tester, name string) (float32, float32) {
	t.Helper()
	r, ok := tt.Find(name)
	if !ok {
		t.Fatalf("找不到图标按钮 %q：%q", name, tt.Texts())
	}
	im := tt.Image()
	minX, minY, maxX, maxY := int(r.X+r.W), int(r.Y+r.H), -1, -1
	for y := int(r.Y); y < int(r.Y+r.H) && y < im.Bounds().Dy(); y++ {
		for x := int(r.X); x < int(r.X+r.W) && x < im.Bounds().Dx(); x++ {
			if x < 0 || y < 0 {
				continue
			}
			c := im.RGBAAt(x, y)
			if c.R < 0x60 && c.G < 0x60 && c.B < 0x60 { // 深色图标墨迹
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
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
		t.Fatalf("%q 的框里找不到图标墨迹：%q", name, tt.Texts())
	}
	return float32(minX+maxX) / 2, float32(minY+maxY) / 2
}

// 图标按钮的悬停底色铺满整框，图标必须与底色同心，偏在一侧看着就是悬浮高亮
// 错位。这里把四种图标按钮都锁住：iconActionButton 曾因漏清 ui.Button 默认的
// Padding(6,14)，固定 28×28 的框把图标挤到内容盒左沿、整枚偏右 8px。
func TestIconButtonsGlyphCenteredOnHover(t *testing.T) {
	a := &app{}
	cases := []struct {
		name  string
		label string
		draw  func(*ui.Context)
	}{
		{"iconActionButton", "居中甲", func(c *ui.Context) {
			iconActionButton(c, closeIcon, "居中甲", "提示")
		}},
		{"editorIconButton", "居中乙", func(c *ui.Context) {
			a.editorIconButton(c, closeIcon, "居中乙", "提示")
		}},
		{"titleIconButton", "居中丙", func(c *ui.Context) {
			a.titleIconButton(c, closeIcon, false, "居中丙", "提示", func() {})
		}},
		{"detailToolButton", "居中丁", func(c *ui.Context) {
			a.detailToolButton(c, closeIcon, "居中丁", "提示", func() {})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := func(c *ui.Context) {
				ui.Row(c).FillWidth().Background(ui.Hex("#ffffff")).Children(func() {
					ui.Spacer(c).Grow(1)
					tc.draw(c)
					ui.Spacer(c).Grow(1)
				})
			}
			tt := ui.NewTester(view, 300, 80)
			tt.Frame()
			r, ok := tt.Find(tc.label)
			if !ok {
				t.Fatalf("找不到图标按钮：%q", tt.Texts())
			}
			tt.Move(r.X+r.W/2, r.Y+r.H/2)
			tt.Frame()
			cx, cy := iconGlyphCenter(t, tt, tc.label)
			if dx := cx - (r.X + r.W/2); dx < -1.5 || dx > 1.5 {
				t.Errorf("图标横向偏离按钮中心 %.1fpx（框 %+v）", dx, r)
			}
			if dy := cy - (r.Y + r.H/2); dy < -1.5 || dy > 1.5 {
				t.Errorf("图标纵向偏离按钮中心 %.1fpx（框 %+v）", dy, r)
			}
		})
	}
}

// 操作按钮的规范：能用一个通行图标说清的动作就只画图标，名字给读屏，说明放
// Tooltip 里（见 AGENTS.md「操作按钮：图标 + tooltip」）。这几个用例盯住本次
// 从文字按钮改成图标按钮的位置，避免以后又退回一行字。

// assertIconAction 断言 name 是一个图标按钮：有无障碍名、有 Tooltip 文案，
// 而且框的宽度只够放图标（文字按钮会宽得多）。
func assertIconAction(t *testing.T, tt *ui.Tester, name string) ui.AccessInfo {
	t.Helper()
	info, ok := tt.Access(name)
	if !ok {
		t.Fatalf("找不到图标按钮 %q：%q", name, tt.Texts())
	}
	if info.Role != ui.RoleButton {
		t.Errorf("%q 的角色 = %v，期望按钮", name, info.Role)
	}
	if info.Description == "" {
		t.Errorf("%q 没有 tooltip 说明", name)
	}
	if info.Bounds.W > iconActionMaxWidth {
		t.Errorf("%q 的宽度 = %v，图标按钮不该占这么宽", name, info.Bounds.W)
	}
	return info
}

// iconActionMaxWidth 是图标按钮可以有的最大宽度：16 的图标 + 两侧 Padding(4)
// + 一圈焦点环，再多就说明里面塞了文字。
const iconActionMaxWidth float32 = 36

// 组件设置弹窗的关闭是叉号图标：名字与窗口标题栏的「关闭」区分开，点了收起
// 弹窗，填过的内容仍留在草稿里。
func TestComposeModuleDialogCloseIsIconButton(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.moduleIndex = 0
	st.moduleOpen = true
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	assertIconAction(t, tt, "关闭设置")
	if err := tt.Click("关闭设置"); err != nil {
		t.Fatalf("点关闭图标失败：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	if st.moduleOpen {
		t.Errorf("点关闭图标后设置弹窗还开着：%+v", st)
	}
}

// 图片面板和表情面板一样，右上角有一个收起用的叉号图标。
func TestEditorImagePanelCloseIsIconButton(t *testing.T) {
	a := testApp(t)
	composeReady(a)
	a.compose.imageOpen = true
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	assertIconAction(t, tt, "收起图片")
	if err := tt.Click("收起图片"); err != nil {
		t.Fatalf("点收起图片失败：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	if a.compose.imageOpen {
		t.Error("点收起图片后面板还开着")
	}
}

// 删除自定义图库是废纸篓图标，说明里写清删的是哪一条、之后会怎样。
func TestImageHostDeleteIsIconButton(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabHosts
	a.addCustomImageHost()
	tt := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1600)
	tt.Frame()
	info := assertIconAction(t, tt, "删除此图库")
	if !containsString(a.allHostNames(), "我的图库 1") {
		t.Fatalf("自定义图库没在列表里：%v", a.allHostNames())
	}
	if !strings.Contains(info.Description, "我的图库 1") {
		t.Errorf("删除按钮的说明没点出删的是哪一条：%q", info.Description)
	}
	if err := tt.Click("删除此图库"); err != nil {
		t.Fatalf("点删除图库失败：%v：%q", err, tt.Texts())
	}
	if hostByID(a.hosts, "user-1") != nil {
		t.Errorf("点删除后图库还在：%v", a.allHostNames())
	}
}

// 登录回退页的返回是左箭头图标，提示里说明离开会放弃还没完成的授权。
func TestLoginPageBackIsIconButton(t *testing.T) {
	a := testApp(t)
	a.pageName = "login"
	tt := ui.NewTester(a.view, 1100, 760)
	info := assertIconAction(t, tt, "返回")
	if !strings.Contains(info.Description, "授权") {
		t.Errorf("返回按钮的说明没提放弃授权：%q", info.Description)
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatalf("点返回失败：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	if a.pageName != "list" {
		t.Errorf("点返回后页面 = %q，期望回到列表", a.pageName)
	}
}
