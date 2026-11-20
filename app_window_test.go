package main

import (
	"testing"
)

// 关主窗口不关其它窗口：详情窗口还开着时账号的生命周期继续，账号册里仍
// 挂着这个 app，最后一扇窗口关掉时才收尾。
func TestMainWindowCloseKeepsOtherWindowsAndAccountAlive(t *testing.T) {
	b := testBook(t)
	acc := addTestAccount(t, b, "jimmy")
	a := newAppWithDeps(b.deps(), acc)
	acc.app = a
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
	if acc.app != a {
		t.Fatal("还有窗口开着，账号不该从账号册上摘掉")
	}
	if !a.hasLiveWindow() {
		t.Fatal("还有详情窗口，应算有活窗口")
	}

	// 关掉其中一扇，留下的那扇还撑着账号。
	a.closeDetailWindow(h1)
	if a.finalized || acc.app != a {
		t.Fatal("还剩一扇详情窗口，不该收尾")
	}

	// 最后一扇关掉，账号才回到账号册等着重新打开。
	a.closeDetailWindow(h2)
	if !a.finalized {
		t.Fatal("最后一扇窗口关掉后应收尾")
	}
	if acc.app != nil {
		t.Fatal("收尾后账号不该还挂着窗口界面")
	}
}

// 托盘「关闭全部」收掉详情等附属窗口，但账号仍挂在账号册上 —— 窗口只是
// 收进托盘，不是退出登录；点托盘图标还能回到它。
func TestHideAllWindowsToTrayKeepsAccountAlive(t *testing.T) {
	b := testBook(t)
	acc := addTestAccount(t, b, "jimmy")
	a := newAppWithDeps(b.deps(), acc)
	acc.app = a
	a.openDetailWindow(itemForWindow("POST001"))
	a.openDetailWindow(itemForWindow("POST002"))

	a.hideAllWindowsToTray()

	if got := a.detailWindowCount(); got != 0 {
		t.Fatalf("附属窗口该收干净，实际剩 %d 扇", got)
	}
	if a.finalized || acc.app != a {
		t.Fatal("收进托盘不该收掉账号界面状态")
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
