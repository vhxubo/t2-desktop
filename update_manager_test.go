package main

import (
	"context"
	"testing"
)

// 后台检查会把进度送到状态里，装完只标待重启；下一轮检查不会把它冲掉，
// 只有用户点「重启更新」才真正重启。
func TestRunUpdateCheckWaitsForManualRestart(t *testing.T) {
	old := checkUpdateForApp
	t.Cleanup(func() { checkUpdateForApp = old })

	var checks int
	checkUpdateForApp = func(context.Context) (*updateCandidate, error) {
		checks++
		return &updateCandidate{version: "9.9.9", install: func(_ context.Context, p func(int64, int64)) error {
			p(50, 100)
			p(100, 100)
			return nil
		}}, nil
	}

	d := &desktop{
		updates:         true,
		updateMgr:       newUpdateManager(),
		updateIdlePrefs: &updateIdlePrefsStore{enabled: true},
		updateChannel:   &updateChannelStore{},
	}
	var relaunched int
	d.updateMgr.relaunch = func() { relaunched++ }

	d.runUpdateCheck()
	s := d.updateStatusSnapshot()
	if s.Phase != updateReady || s.Version != "9.9.9" {
		t.Fatalf("下载完状态 = %+v，期望待重启 9.9.9", s)
	}
	if relaunched != 0 {
		t.Fatal("后台装完不该自动重启")
	}

	// 待重启期间不再检查，避免「重启更新」按钮被下一轮结果覆盖。
	d.runUpdateCheck()
	if checks != 1 || d.updateStatusSnapshot().Phase != updateReady {
		t.Fatalf("待重启时又检查了：checks=%d status=%+v", checks, d.updateStatusSnapshot())
	}
	d.restartForUpdate()
	if relaunched != 1 {
		t.Errorf("点重启没有触发：%d", relaunched)
	}
}

// 只有稳定渠道且用户关掉闲时下载时才不自动下载，状态停在「有新版本」。
func TestRunUpdateCheckDetectedWithoutAutoDownload(t *testing.T) {
	old := checkUpdateForApp
	t.Cleanup(func() { checkUpdateForApp = old })
	checkUpdateForApp = func(context.Context) (*updateCandidate, error) {
		return &updateCandidate{version: "9.9.9", install: func(context.Context, func(int64, int64)) error {
			t.Error("不该下载")
			return nil
		}}, nil
	}

	d := &desktop{
		updates:         true,
		updateMgr:       newUpdateManager(),
		updateIdlePrefs: &updateIdlePrefsStore{enabled: false},
		updateChannel:   &updateChannelStore{stableOnly: true},
	}
	d.runUpdateCheck()
	if s := d.updateStatusSnapshot(); s.Phase != updateDetected || s.Version != "9.9.9" {
		t.Fatalf("状态 = %+v，期望有新版本但未下载", s)
	}
}
