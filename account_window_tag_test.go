package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

/* ------------------------------------------------------------------
   多账号：每扇窗口都写明属于哪个账号
----------------------------------------------------------------- */

// accountWindowCase 是一扇附属窗口：怎么准备状态、按什么尺寸渲染。
type accountWindowCase struct {
	name       string
	width      int
	height     int
	prepare    func(a *app)
	windowView func(a *app) func(*ui.Context)
}

// 通知、设置、金币、发帖……这些窗口的自绘标题栏都只写自己的名字，多账号
// 并排开着时看不出是谁的窗口。这里逐扇核对标题栏最右那枚「账号 @昵称」。
func accountWindowCases() []accountWindowCase {
	return []accountWindowCase{
		{"通知", notifWindowWidth, notifWindowHeight, nil,
			func(a *app) func(*ui.Context) { return a.notificationsWindowView }},
		{"设置", settingsWindowWidth, settingsWindowHeight, nil,
			func(a *app) func(*ui.Context) { return a.settingsWindowView }},
		{"个人中心", profileWindowWidth, profileWindowHeight, nil,
			func(a *app) func(*ui.Context) { return a.profileWindowView }},
		{"发帖", composeWindowWidth, composeWindowHeight, func(a *app) { composeReady(a) },
			func(a *app) func(*ui.Context) { return a.composeWindowView }},
		{"写评论", replyWindowWidth, replyWindowHeight, func(a *app) {
			a.detail = &detailState{shortID: "a1b2", mdEditor: newMDEditor()}
			a.showReplyWindow()
		}, func(a *app) func(*ui.Context) { return a.replyWindowView }},
		{"编辑", editWindowWidth, editWindowHeight, func(a *app) {
			a.edit = newPostscriptState("a1b2", nil, a.md)
		}, func(a *app) func(*ui.Context) { return a.editWindowView }},
		{"金币", coinWindowWidth, coinWindowHeight, nil,
			func(a *app) func(*ui.Context) { return a.coinWindowView }},
		{"用户资料", userCardWindowWidth, userCardWindowHeight, func(a *app) {
			a.userCard = newUserCardState("bob")
		}, func(a *app) func(*ui.Context) { return a.userCardWindowView }},
		{"搜索", searchWindowWidth, searchWindowHeight, func(a *app) {
			a.search = newSearchState()
		}, func(a *app) func(*ui.Context) { return a.searchWindowView }},
		{"关注与粉丝", followsWindowWidth, followsWindowHeight, func(a *app) {
			a.follows = &followsState{viewer: "jimmy"}
		}, func(a *app) func(*ui.Context) { return a.followsWindowView }},
		{"关于", aboutWindowWidth, aboutWindowHeight, nil,
			func(a *app) func(*ui.Context) { return a.aboutWindowView }},
	}
}

func TestEverySubWindowNamesItsAccount(t *testing.T) {
	for _, tc := range accountWindowCases() {
		t.Run(tc.name, func(t *testing.T) {
			a := multiAccountWindow(t, "jimmy")
			if tc.prepare != nil {
				tc.prepare(a)
			}
			tt := ui.NewTester(tc.windowView(a), tc.width, tc.height)
			tt.Frame()

			tag, ok := tt.Find("账号 jimmy")
			if !ok {
				t.Fatalf("「%s」窗口没有写明是哪个账号：%q", tc.name, tt.Texts())
			}
			// 标识必须在标题栏里，而且不压住右侧的窗口按钮。
			if tag.Y < 0 || tag.Y+tag.H > titleBarHeight {
				t.Errorf("账号标识不在标题栏里：%+v", tag)
			}
			closeR, ok := tt.Find("关闭")
			if !ok {
				t.Fatalf("找不到关闭按钮：%q", tt.Texts())
			}
			if tag.X+tag.W > closeR.X {
				t.Errorf("账号标识压住了窗口按钮：tag=%+v close=%+v", tag, closeR)
			}
		})
	}
}

// 主窗口的标题栏本来就用头像 + 昵称说明身份，不再多挂一枚标识。
func TestMainWindowTitleBarShowsAccountName(t *testing.T) {
	a := multiAccountWindow(t, "jimmy")
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	name, ok := tt.Find("jimmy")
	if !ok {
		t.Fatalf("主窗口标题栏没有写账号名：%q", tt.Texts())
	}
	if name.Y < 0 || name.Y+name.H > titleBarHeight {
		t.Errorf("账号名不在标题栏里：%+v", name)
	}
}

// 单账号时不挂标识：窗口标题已经说明身份，多一枚只会白占版面。
func TestSubWindowTagHiddenForSingleAccount(t *testing.T) {
	a := singleAccountWindow(t, "jimmy")
	if got := a.windowAccountName(); got != "" {
		t.Errorf("单账号的窗口账号名 = %q，期望空", got)
	}
	tt := ui.NewTester(a.settingsWindowView, settingsWindowWidth, settingsWindowHeight)
	tt.Frame()
	if _, ok := tt.Find("账号 jimmy"); ok {
		t.Errorf("单账号窗口不该出现账号标识：%q", tt.Texts())
	}
}

// 原生的窗口标题也带上账号：任务栏与 Alt+Tab 里只看到「通知」「设置」时，
// 多账号下一样分不清是哪一扇。
func TestSubWindowTitlesCarryAccountName(t *testing.T) {
	multi := multiAccountWindow(t, "jimmy")
	if got, want := multi.subWindowTitle("通知"), "通知 · jimmy"; got != want {
		t.Errorf("多账号的窗口标题 = %q，期望 %q", got, want)
	}
	single := singleAccountWindow(t, "jimmy")
	if got, want := single.subWindowTitle("通知"), "通知"; got != want {
		t.Errorf("单账号的窗口标题 = %q，期望 %q", got, want)
	}
}
