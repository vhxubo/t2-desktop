package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 标题栏的更新按钮：空闲显示「检查更新」，下载中显示进度，装完只提示
// 「重启更新」，且重启只由用户点击触发。
func TestTitleBarUpdateButtonStates(t *testing.T) {
	a := testApp(t)
	var restarts int
	d := &desktop{updateMgr: newUpdateManager()}
	d.updateMgr.relaunch = func() { restarts++ }
	a.desktop = d

	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if !tt.HasText("检查更新") {
		t.Fatalf("标题栏没有更新按钮：%q", tt.Texts())
	}
	if r, ok := tt.Find("检查更新"); !ok || r.Y > titleBarHeight {
		t.Fatalf("更新按钮不在标题栏内：%+v %v", r, ok)
	}

	d.setUpdateStatus(updateStatus{Phase: updateDownloading, Version: "9.9.9", Percent: 45})
	tt.Frame()
	if !tt.HasText("下载中 45%") {
		t.Errorf("下载中没有显示进度：%q", tt.Texts())
	}
	if restarts != 0 {
		t.Fatal("下载过程中不该重启")
	}

	d.setUpdateStatus(updateStatus{Phase: updateReady, Version: "9.9.9", Percent: 100})
	tt.Frame()
	if !tt.HasText("重启更新") {
		t.Fatalf("装完没有显示重启按钮：%q", tt.Texts())
	}
	if err := tt.Click("重启更新"); err != nil {
		t.Fatal(err)
	}
	if restarts != 1 {
		t.Errorf("点「重启更新」没有触发重启：%d", restarts)
	}
}

// 没有新版本时不会显示重启按钮；后台状态变化不触发重启。
func TestUpdateStatusDoesNotRelaunchItself(t *testing.T) {
	a := testApp(t)
	var restarts int
	d := &desktop{updateMgr: newUpdateManager()}
	d.updateMgr.relaunch = func() { restarts++ }
	a.desktop = d

	for _, phase := range []updatePhase{updateIdle, updateChecking, updateDetected, updateFailed} {
		d.setUpdateStatus(updateStatus{Phase: phase})
		if restarts != 0 {
			t.Fatalf("状态 %d 自己触发了重启", phase)
		}
	}
}
