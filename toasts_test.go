package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 操作结果用窗口内 toast 报告。目标窗口不在时退回主窗口，否则用户根本看不到
// 结果（设置窗口关掉以后，保存代理的报错不该消失得无声无息）。
func TestToastFallsBackToMainWindow(t *testing.T) {
	a := testApp(t)
	a.toast(toastSettings, "保存代理失败：不支持的代理地址")

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("保存代理失败：不支持的代理地址") {
		t.Errorf("设置窗口不在时，设置的结果没有退到主窗口：%q", tt.Texts())
	}
	if len(a.toasts) != 0 {
		t.Errorf("提示显示后没有出队：%+v", a.toasts)
	}
}

// 通知窗口的操作失败显示在它自己的窗口里，并且到时间就消失——这两点正是
// 「用 toast 而不是内联文字」能观察到的地方。
func TestNotificationToastShowsInItsWindowThenFades(t *testing.T) {
	a := testApp(t)
	a.toast(toastNotifications, "全部已读失败：服务端错误（500），请稍后重试")

	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	if !tt.HasText("全部已读失败") {
		t.Fatalf("通知窗口没有显示操作失败的 toast：%q", tt.Texts())
	}
	tt.Advance(6 * time.Second)
	if tt.HasText("全部已读失败") {
		t.Errorf("提示过期后还留在窗口里，说明它是内联文字而不是 toast：%q", tt.Texts())
	}
}

// 操作发生在哪个窗口，toast 就在哪个窗口报告：用户资料卡上的关注要显示在
// 资料卡里，而不是丢给可能被它盖住的主窗口。资料还没读到时，关注在发请求
// 前就被拦下，正好直接观察它把结果交给了哪个窗口。
func TestUserCardToastShowsInItsOwnWindow(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok",
		TokenType:   "Bearer",
		Username:    "reader",
	}); err != nil {
		t.Fatal(err)
	}
	a.userCard = newUserCardState("jimmy")

	a.toggleUserCardFollow(true)
	if len(a.toasts) != 1 || a.toasts[0].target != toastUserCard {
		t.Fatalf("关注结果没有交给资料卡窗口：%+v", a.toasts)
	}

	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if !tt.HasText("缺少用户标识") {
		t.Errorf("资料卡窗口没有显示关注失败的 toast：%q", tt.Texts())
	}
	if len(a.toasts) != 0 {
		t.Errorf("提示显示后没有出队：%+v", a.toasts)
	}
}

// toast 是窗口里层级最高的一层：页面自己叠的全窗口遮罩（弹窗、浮层）不能把
// 它盖住。用一个纯红全屏遮罩验证——底部只要有一个像素不是红色，就说明
// toast 画在遮罩之上。
func TestToastDrawsAboveWindowOverlays(t *testing.T) {
	red := ui.RGB(255, 0, 0)
	shown := false
	tt := ui.NewTester(func(c *ui.Context) {
		if !shown {
			shown = true
			c.Toast("层级最高的提示")
		}
		ui.Overlay(c, func() {
			ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Background(red)
		})
	}, 420, 260)
	tt.Frame()
	tt.Advance(300 * time.Millisecond) // 等淡入结束，toast 完全显出来
	tt.Frame()
	if !tt.HasText("层级最高的提示") {
		t.Fatalf("窗口里没有 toast：%q", tt.Texts())
	}
	img := tt.Image()
	for y := 180; y < 260; y++ {
		for x := 0; x < 420; x++ {
			if c := img.RGBAAt(x, y); c.R != red.R || c.G != red.G || c.B != red.B {
				return
			}
		}
	}
	t.Error("全窗口遮罩盖住了 toast：toast 不是窗口里最上面的一层")
}

// 页面级的加载失败仍然留在页面里：那里有内容位置和「重试」，换成 toast 就
// 只剩一片空白，用户也没有地方点重试。
func TestPageLoadFailureStaysInline(t *testing.T) {
	a := testApp(t)
	a.tabs[0].err = "服务端错误（500），请稍后重试"

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("加载失败") || !tt.HasText("重试") {
		t.Errorf("列表加载失败没有留在页面里：%q", tt.Texts())
	}
	if len(a.toasts) != 0 {
		t.Errorf("页面级加载失败不该变成 toast：%+v", a.toasts)
	}
}
