package main

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo"
)

// 关主窗口不关其它窗口：详情窗口还开着时界面继续，最后一扇窗口关掉时才收尾。
func TestMainWindowCloseKeepsOtherWindowsAlive(t *testing.T) {
	b := testBook(t)
	auth := addTestAccount(t, b, "jimmy")
	a := newAppWithDeps(b.deps(), auth)
	a.openDetailWindow(itemForWindow("POST001"))
	a.openDetailWindow(itemForWindow("POST002"))
	h1, h2 := a.detailWindows[0], a.detailWindows[1]

	a.onMainWindowClosed()

	if !a.mainClosed {
		t.Fatal("应记下主窗口已关")
	}
	if a.finalized {
		t.Fatal("还有其它窗口开着，不该收尾")
	}
	if got := a.detailWindowCount(); got != 2 {
		t.Fatalf("关主窗口不该关详情窗口，实际剩 %d 扇", got)
	}
	if h1.removed || h2.removed {
		t.Fatal("详情窗口不该被顺手关掉")
	}
	if !a.hasLiveWindow() {
		t.Fatal("还有详情窗口，应算有活窗口")
	}

	// 关掉其中一扇，留下的那扇还撑着界面。
	a.closeDetailWindow(h1)
	if a.finalized {
		t.Fatal("还剩一扇详情窗口，不该收尾")
	}

	// 最后一扇关掉，界面才收尾。
	a.closeDetailWindow(h2)
	if !a.finalized {
		t.Fatal("最后一扇窗口关掉后应收尾")
	}
}

// 托盘「关闭全部」收掉详情等附属窗口，但界面状态不收尾 —— 窗口只是收进托盘，
// 不是退出登录；点托盘图标还能回到它。
func TestHideAllWindowsToTrayKeepsAppAlive(t *testing.T) {
	b := testBook(t)
	auth := addTestAccount(t, b, "jimmy")
	a := newAppWithDeps(b.deps(), auth)
	a.openDetailWindow(itemForWindow("POST001"))
	a.openDetailWindow(itemForWindow("POST002"))

	a.hideAllWindowsToTray()

	if got := a.detailWindowCount(); got != 0 {
		t.Fatalf("附属窗口该收干净，实际剩 %d 扇", got)
	}
	if a.finalized {
		t.Fatal("收进托盘不该收掉界面状态")
	}
}

// 没有任何其它窗口时，关主窗口就立刻收尾（单窗口场景）。
func TestMainWindowCloseWithNoOtherWindowsFinalizes(t *testing.T) {
	a := testApp(t)
	a.onMainWindowClosed()
	if !a.mainClosed || !a.finalized {
		t.Fatal("没有其它窗口时关主窗口应立即收尾")
	}
}

// finalized 保证晚到的窗口关闭回调不会重复收尾。
func TestFinalizeAccountWindowOnlyOnce(t *testing.T) {
	a := testApp(t)
	a.onMainWindowClosed()
	a.finalizeAccountWindow()
	a.finalizeAccountWindow()
	if !a.finalized {
		t.Fatal("应收尾")
	}
}

// 每一扇弹窗窗口都能独立撑住账号：主窗口关掉后任意一扇还开着，就不该收尾，
// 并且能被挑出来当 publish / toast 的出口。反射先核对窗口字段数量，新增弹窗
// 窗口忘了加进 auxiliaryWindows 或本用例时会在这里失败。
func TestEveryPopupWindowSurvivesMainWindowClose(t *testing.T) {
	setters := []struct {
		name string
		set  func(*app, *mygo.Window)
	}{
		{"notifWin", func(a *app, w *mygo.Window) { a.notifWin = w }},
		{"settingsWin", func(a *app, w *mygo.Window) { a.settingsWin = w }},
		{"aboutWin", func(a *app, w *mygo.Window) { a.aboutWin = w }},
		{"netLogWin", func(a *app, w *mygo.Window) { a.netLogWin = w }},
		{"profileWin", func(a *app, w *mygo.Window) { a.profileWin = w }},
		{"coinWin", func(a *app, w *mygo.Window) { a.coinWin = w }},
		{"composeWin", func(a *app, w *mygo.Window) { a.composeWin = w }},
		{"replyWin", func(a *app, w *mygo.Window) { a.replyWin = w }},
		{"editWin", func(a *app, w *mygo.Window) { a.editWin = w }},
		{"nodeEditWin", func(a *app, w *mygo.Window) { a.nodeEditWin = w }},
		{"searchWin", func(a *app, w *mygo.Window) { a.searchWin = w }},
		{"followsWin", func(a *app, w *mygo.Window) { a.followsWin = w }},
		{"userCardWin", func(a *app, w *mygo.Window) { a.userCardWin = w }},
		{"viewerWin", func(a *app, w *mygo.Window) { a.viewerWin = w }},
	}
	v := reflect.ValueOf(&app{}).Elem()
	winType := reflect.TypeOf((*mygo.Window)(nil))
	fieldCount := 0
	for i := 0; i < v.NumField(); i++ {
		if v.Type().Field(i).Type == winType && v.Type().Field(i).Name != "win" {
			fieldCount++
		}
	}
	if fieldCount != len(setters) {
		t.Fatalf("app 有 %d 个弹窗窗口字段，但用例只覆盖 %d 个", fieldCount, len(setters))
	}

	a := testApp(t)
	a.mainClosed = true
	for _, s := range setters {
		// 先把所有窗口字段清空，只留当前这一扇。
		for _, other := range setters {
			other.set(a, nil)
		}
		want := new(mygo.Window)
		s.set(a, want)
		if !a.hasLiveWindow() {
			t.Errorf("%s 还开着，却没算作活窗口", s.name)
		}
		if got := a.anyLiveWindow(); got != want {
			t.Errorf("%s 还开着，顶上来的却是 %p", s.name, got)
		}
	}
}
