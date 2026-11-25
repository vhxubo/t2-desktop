package main

import (
	"context"
	"testing"
	"time"
)

// 点标题栏进度按钮会取消后台下载：安装收到 ctx 取消，状态回到「有新版本」，
// 同一版本不会被后台循环偷偷下回来，但换新版本后恢复自动下载。
func TestCancelUpdateDownload(t *testing.T) {
	old := checkUpdateForApp
	t.Cleanup(func() { checkUpdateForApp = old })

	started := make(chan struct{})
	cancelled := make(chan struct{})
	checkUpdateForApp = func(context.Context) (*updateCandidate, error) {
		return &updateCandidate{
			version: "9.9.9",
			install: func(ctx context.Context, _ func(int64, int64)) error {
				close(started)
				<-ctx.Done()
				close(cancelled)
				return ctx.Err()
			},
		}, nil
	}

	d := &desktop{
		updates:         true,
		updateMgr:       newUpdateManager(),
		updateIdlePrefs: &updateIdlePrefsStore{enabled: true},
		updateChannel:   &updateChannelStore{},
	}
	done := make(chan struct{})
	go func() {
		d.runUpdateCheck()
		close(done)
	}()
	<-started
	if s := d.updateStatusSnapshot(); s.Phase != updateDownloading || s.Version != "9.9.9" {
		t.Fatalf("下载中状态 = %+v", s)
	}

	d.cancelUpdateDownload()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("取消没有传到安装函数")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("取消后下载循环没有返回")
	}
	s := d.updateStatusSnapshot()
	if s.Phase != updateDetected || s.Version != "9.9.9" {
		t.Fatalf("取消后状态 = %+v，期望有新版本", s)
	}
	// 下一轮只提示有新版本，不再自动下载这个版本。
	var downloads int
	checkUpdateForApp = func(context.Context) (*updateCandidate, error) {
		return &updateCandidate{version: "9.9.9", install: func(context.Context, func(int64, int64)) error {
			downloads++
			return nil
		}}, nil
	}
	d.runUpdateCheck()
	if downloads != 0 || d.updateStatusSnapshot().Phase != updateDetected {
		t.Fatalf("取消后又自动下载了：downloads=%d status=%+v", downloads, d.updateStatusSnapshot())
	}

	// 新版本不沿用「已取消」，照常自动下载。
	checkUpdateForApp = func(context.Context) (*updateCandidate, error) {
		return &updateCandidate{version: "10.0.0", install: func(_ context.Context, p func(int64, int64)) error {
			downloads++
			p(100, 100)
			return nil
		}}, nil
	}
	d.runUpdateCheck()
	if s := d.updateStatusSnapshot(); downloads != 1 || s.Phase != updateReady || s.Version != "10.0.0" {
		t.Fatalf("新版本没有恢复自动下载：downloads=%d status=%+v", downloads, s)
	}
}

// 非下载阶段点取消不改状态：安装中不能被半路打断，待重启也不受影响。
func TestCancelUpdateDownloadOnlyWhileDownloading(t *testing.T) {
	d := &desktop{updates: true, updateMgr: newUpdateManager()}
	for _, phase := range []updatePhase{updateDetected, updateInstalling, updateReady} {
		d.setUpdateStatus(updateStatus{Phase: phase, Version: "9.9.9"})
		d.cancelUpdateDownload()
		if s := d.updateStatusSnapshot(); s.Phase != phase {
			t.Fatalf("状态 %d 被取消改成了 %d", phase, s.Phase)
		}
	}
}
