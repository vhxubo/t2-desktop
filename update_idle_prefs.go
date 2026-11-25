package main

import (
	"fmt"
	"path/filepath"

	"github.com/egoist/mygo/plugins/updater"
)

// 「闲时自动下载更新」偏好，进程级一份（见 appDeps），落盘在 update_idle.json。
// 默认开启，所以存「关闭」标记：老版本没有这个文件时 on() 保持 true，升级后
// 无需手动打开就会在后台下载更新。
//
// 它接到 mygo 更新插件的 AutomaticDownloads：后台检查到新版本时不再弹窗询问，
// 直接下载安装，下次启动生效（Windows / Linux 立即替换文件，macOS 退出时换包）。
// 接收测试版时强制开启（见 a.idleAutoDownload），用户手动关闭的选择仍然留着，
// 打开「稳定渠道」后恢复原来的选择。
type updateIdlePrefsStore struct {
	dir     string
	enabled bool
}

type updateIdlePrefsConfig struct {
	Disabled bool `json:"disabled"`
}

func newUpdateIdlePrefsStore(dir string) *updateIdlePrefsStore {
	if dir == "" {
		dir = configDir()
	}
	s := &updateIdlePrefsStore{dir: dir, enabled: true}
	var cfg updateIdlePrefsConfig
	if err := readJSONFile(filepath.Join(dir, "update_idle.json"), &cfg); err == nil {
		s.enabled = !cfg.Disabled
	}
	return s
}

// save 先写盘：失败时内存里保留用户刚选的值（本次运行仍按新值走），只把错误
// 交给调用方提示；下次启动回到旧配置。和自动签到偏好的处理一致。
func (s *updateIdlePrefsStore) save() error {
	cfg := updateIdlePrefsConfig{Disabled: !s.enabled}
	if err := writeJSONFile(filepath.Join(s.dir, "update_idle.json"), cfg); err != nil {
		return fmt.Errorf("保存闲时自动下载更新设置失败：%w", err)
	}
	return nil
}

// Reset 恢复默认：闲时自动下载更新开启。
func (s *updateIdlePrefsStore) Reset() error {
	s.enabled = true
	return s.save()
}

// on 报告用户是否允许闲时自动下载更新。nil 偏好按默认开启处理。
func (s *updateIdlePrefsStore) on() bool { return s == nil || s.enabled }

// setEnabled 开关「闲时自动下载更新」并落盘。
func (s *updateIdlePrefsStore) setEnabled(on bool) error {
	if s == nil || s.enabled == on {
		return nil
	}
	s.enabled = on
	return s.save()
}

// idleAutoDownload 报告当前生效的值：用户关掉时按他选的走，但默认接收测试版
// 时强制开启，确保 beta 能装上；只有打开「稳定渠道」才按用户选择走。
func (a *app) idleAutoDownload() bool {
	return a.updateIdlePrefs.on() || a.updateChannel.prerelease()
}

// applyIdleAutoDownload 把生效值接到 mygo 更新插件上。无头测试没装插件，只
// 走测试钩子确认接线，避免碰到 updater 的主循环。
func (a *app) applyIdleAutoDownload() {
	on := a.idleAutoDownload()
	if a.onIdleAutoDownload != nil {
		a.onIdleAutoDownload(on)
	}
	if !a.updates {
		return
	}
	updater.SetAutomaticDownloads(on)
}

// applyUpdateIdleAutoDownload 在启动时按保存的选择接好更新插件；必须在
// mygo.Use(updatenative.Plugin) 之后调用。on 是合并后的生效值：用户选择，
// 或接收测试版时强制开启。
func applyUpdateIdleAutoDownload(on bool) { updater.SetAutomaticDownloads(on) }

// watchIdleAutoDownload 防止更新窗口自带的「以后自动下载并安装更新」开关在
// 测试版通道下把它关掉：mygo 的 OnChange 在主线程回调，关掉后立刻开回来。
func watchIdleAutoDownload(pre func() bool) {
	updater.OnChange(func() {
		if pre() && !updater.AutomaticDownloads() {
			updater.SetAutomaticDownloads(true)
		}
	})
}
