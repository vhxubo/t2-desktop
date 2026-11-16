package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

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
