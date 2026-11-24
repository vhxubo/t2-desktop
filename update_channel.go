package main

import (
	"fmt"
	"path/filepath"

	"github.com/egoist/mygo"
)

// 「接收测试版」偏好，进程级一份（见 appDeps），落盘在 update_channel.json。
// 默认只收正式版：老版本没有这个文件时 prerelease 为 false，行为与加这个开关
// 之前一致。打开后检查更新也会提供 GitHub 上标记为 prerelease 的版本（例如
// 1.0.0-beta.2），下载加速入口同样适用于它（见 update_mirror.go）。
type updateChannelStore struct {
	dir string
	pre bool
}

type updateChannelConfig struct {
	Prerelease bool `json:"prerelease"`
}

func newUpdateChannelStore(dir string) *updateChannelStore {
	if dir == "" {
		dir = configDir()
	}
	s := &updateChannelStore{dir: dir}
	var cfg updateChannelConfig
	if err := readJSONFile(filepath.Join(dir, "update_channel.json"), &cfg); err == nil {
		s.pre = cfg.Prerelease
	}
	return s
}

// prerelease 报告是否接收测试版。nil 偏好按默认（只收正式版）处理。
func (s *updateChannelStore) prerelease() bool { return s != nil && s.pre }

// setPrerelease 开关测试版通道并落盘。写盘失败时内存里保留新值（本次运行仍按
// 新值走），错误交给调用方提示；下次启动回到旧配置，和镜像、图片偏好一致。
func (s *updateChannelStore) setPrerelease(on bool) error {
	if s == nil || s.pre == on {
		return nil
	}
	s.pre = on
	if err := writeJSONFile(filepath.Join(s.dir, "update_channel.json"),
		updateChannelConfig{Prerelease: on}); err != nil {
		return fmt.Errorf("保存更新通道设置失败：%w", err)
	}
	return nil
}

// Reset 恢复默认通道：只接收正式版。
func (s *updateChannelStore) Reset() error { return s.setPrerelease(false) }

// applyUpdateChannel 把「接收测试版」接到 mygo 更新器上：关着时读 latest
// release，打开后读版本最高的 release（含 prerelease）。启动时按保存的选择
// 调一次，设置窗口改动时立即再调。任何 goroutine 可调。
func applyUpdateChannel(pre bool) { mygo.Updater.SetPrereleases(pre) }
