package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/egoist/mygo"
)

// 应用自己的后台更新循环。mygo 更新插件的间隔在建插件时就固定成 24 小时，
// 没有运行时修改入口，也不暴露后台下载的进度；这里接管「检查 → 下载 → 待重启」
// 全过程：间隔可以即时切换，进度能送到标题栏，装完只标记待重启，绝不自动拉起。
//
// 插件仍保留手动「检查更新」窗口（DisableAutomaticChecks），两种入口共用同一套
// 通道 / 镜像设置；插件装完也会经 OnInstalled 把状态置为待重启。

type updatePhase int

const (
	updateIdle updatePhase = iota // 没有新版本
	updateChecking
	updateDownloading
	updateInstalling
	updateDetected // 有新版本，但用户关掉了闲时自动下载
	updateReady    // 已下载安装，等用户手动重启
	updateFailed
)

// updateStatus 是标题栏要画的快照，只放数据，不放 ui.Element / *ui.Context。
type updateStatus struct {
	Phase   updatePhase
	Version string
	Percent float32
	Err     string
}

// updateCandidate 把 mygo 的 Update 收敛成可替换的最小接口，测试可以注入假的
// 检查结果，不必真的访问 GitHub。
type updateCandidate struct {
	version string
	install func(context.Context, func(int64, int64)) error
}

var checkUpdateForApp = func(ctx context.Context) (*updateCandidate, error) {
	up, err := mygo.Updater.Check(ctx)
	if err != nil || up == nil {
		return nil, err
	}
	return &updateCandidate{version: up.Version, install: up.Install}, nil
}

const (
	updateFirstCheckDelay = 10 * time.Second
	updateCheckTimeout    = 2 * time.Minute
)

type updateManager struct {
	mu              sync.Mutex
	status          updateStatus
	stop            chan struct{}
	intervalChanged chan struct{}
	started         bool
	stopped         bool
	// relaunch 由测试替换，避免无头环境真的拉起应用。
	relaunch func()
}

func newUpdateManager() *updateManager {
	return &updateManager{
		stop:            make(chan struct{}),
		intervalChanged: make(chan struct{}, 1),
		relaunch:        func() { mygo.App.Relaunch() },
	}
}

// updateStatusSnapshot 复制状态给界面读。
func (d *desktop) updateStatusSnapshot() updateStatus {
	if d == nil || d.updateMgr == nil {
		return updateStatus{Phase: updateIdle}
	}
	m := d.updateMgr
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// setUpdateStatus 更新状态并让窗口下一帧重画。相同状态（进度按整百分点）不重画。
func (d *desktop) setUpdateStatus(s updateStatus) {
	if d == nil || d.updateMgr == nil {
		return
	}
	m := d.updateMgr
	m.mu.Lock()
	old := m.status
	if old.Phase == s.Phase && old.Version == s.Version &&
		int(old.Percent) == int(s.Percent) && old.Err == s.Err {
		m.mu.Unlock()
		return
	}
	m.status = s
	a := d.app
	m.mu.Unlock()
	if a != nil {
		a.publish(func() {})
	}
}

// idleAutoDownload 是合并后的生效值：用户关掉时按他选的走，但默认接收测试版时
// 强制开启，确保 beta 能装上；只有打开「稳定渠道」才按用户选择走。
func (d *desktop) idleAutoDownload() bool {
	if d == nil {
		return true
	}
	return d.updateIdlePrefs.on() || d.updateChannel.prerelease()
}

// startUpdateLoop 在应用就绪后起后台循环；无头测试不调它。
func (d *desktop) startUpdateLoop() {
	if d == nil || d.updateMgr == nil {
		return
	}
	m := d.updateMgr
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.mu.Unlock()
	go d.updateLoop()
}

// stopUpdateLoop 让后台循环在退出时停下来。
func (d *desktop) stopUpdateLoop() {
	if d == nil || d.updateMgr == nil {
		return
	}
	m := d.updateMgr
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	m.mu.Unlock()
	close(m.stop)
}

// signalUpdateInterval 通知循环按新间隔重新排下一次检查。
func (d *desktop) signalUpdateInterval() {
	if d == nil || d.updateMgr == nil {
		return
	}
	select {
	case d.updateMgr.intervalChanged <- struct{}{}:
	default:
	}
}

func (d *desktop) updateLoop() {
	m := d.updateMgr
	timer := time.NewTimer(updateFirstCheckDelay)
	defer timer.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-m.intervalChanged:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(updateFirstCheckDelay)
			continue
		case <-timer.C:
		}
		d.runUpdateCheck()
		timer.Reset(d.updateIntervalPrefs.interval())
	}
}

// runUpdateCheck 检查并在允许时静默下载。安装完只置 updateReady：重启动作只由
// 用户在标题栏点「重启更新」触发。
func (d *desktop) runUpdateCheck() {
	if d == nil || !d.updates {
		return
	}
	// 已经装好等重启时不再检查，免得下一轮把「重启更新」按钮冲掉。
	if d.updateStatusSnapshot().Phase == updateReady {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	d.setUpdateStatus(updateStatus{Phase: updateChecking})
	cand, err := checkUpdateForApp(ctx)
	switch {
	case err != nil:
		log.Printf("后台检查更新失败：%v", err)
		d.setUpdateStatus(updateStatus{Phase: updateFailed, Err: err.Error()})
		return
	case cand == nil:
		d.setUpdateStatus(updateStatus{Phase: updateIdle})
		return
	}
	if !d.idleAutoDownload() {
		d.setUpdateStatus(updateStatus{Phase: updateDetected, Version: cand.version})
		return
	}
	d.setUpdateStatus(updateStatus{Phase: updateDownloading, Version: cand.version})
	// 下载不设总时限：慢网可以慢慢下，mygo 自己的响应头 / 停滞看门狗会
	// 处理断掉的链接；给安装加总超时会误杀正常的慢速下载。
	err = cand.install(context.Background(), func(downloaded, total int64) {
		if total <= 0 {
			return
		}
		if downloaded >= total {
			d.setUpdateStatus(updateStatus{Phase: updateInstalling, Version: cand.version, Percent: 100})
			return
		}
		pct := float32(downloaded) * 100 / float32(total)
		d.setUpdateStatus(updateStatus{Phase: updateDownloading, Version: cand.version, Percent: pct})
	})
	if err != nil {
		log.Printf("后台下载更新失败：%v", err)
		d.setUpdateStatus(updateStatus{Phase: updateFailed, Version: cand.version, Err: err.Error()})
		return
	}
	d.setUpdateStatus(updateStatus{Phase: updateReady, Version: cand.version, Percent: 100})
}

// restartForUpdate 只由用户点击触发：没装好之前点不动，装好之后才真正重启。
func (d *desktop) restartForUpdate() {
	if d == nil || d.updateMgr == nil || d.updateStatusSnapshot().Phase != updateReady {
		return
	}
	if fn := d.updateMgr.relaunch; fn != nil {
		fn()
	}
}

// app 侧的入口，标题栏与设置窗口都走这里；无窗口测试只触发钩子。
func (a *app) updateStatus() updateStatus {
	if a != nil && a.desktop != nil {
		return a.desktop.updateStatusSnapshot()
	}
	return updateStatus{Phase: updateIdle}
}

func (a *app) restartForUpdate() {
	if a != nil && a.desktop != nil {
		a.desktop.restartForUpdate()
		return
	}
	if a != nil && a.onUpdateRestart != nil {
		a.onUpdateRestart()
	}
}
