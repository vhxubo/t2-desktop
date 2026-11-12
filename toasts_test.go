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
