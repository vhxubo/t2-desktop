package main

import (
	"fmt"
	"path/filepath"

	"github.com/egoist/mygo"
)

// 「稳定渠道」偏好，进程级一份（见 appDeps），落盘在 update_channel.json。
// 默认关闭：接收全部版本，包括 GitHub 上标记为 prerelease 的 Beta；只有打开
// 稳定渠道时才只读 latest release。反选逻辑让新用户默认跟上测试版，老文件里
// 的 prerelease 字段按反选读回，保留他们之前的选择。
//
// 关闭稳定渠道（接收测试版）时闲时自动下载更新强制开启（见 update_idle_prefs.go）。
type updateChannelStore struct {
	dir        string
	stableOnly bool
}

type updateChannelConfig struct {
	Stable     *bool `json:"stable,omitempty"`
	Prerelease *bool `json:"prerelease,omitempty"` // 旧版字段，只读兼容
}

func newUpdateChannelStore(dir string) *updateChannelStore {
	if dir == "" {
		dir = configDir()
	}
	s := &updateChannelStore{dir: dir}
	var cfg updateChannelConfig
	if err := readJSONFile(filepath.Join(dir, "update_channel.json"), &cfg); err == nil {
		switch {
		case cfg.Stable != nil:
			s.stableOnly = *cfg.Stable
		case cfg.Prerelease != nil:
			s.stableOnly = !*cfg.Prerelease
		}
	}
	return s
}

// save 写盘。写失败时内存里保留新值（本次运行仍按新值走），错误交给调用方提示；
// 下次启动回到旧配置，和镜像、图片偏好一致。
func (s *updateChannelStore) save() error {
	cfg := updateChannelConfig{Stable: &s.stableOnly}
	if err := writeJSONFile(filepath.Join(s.dir, "update_channel.json"), cfg); err != nil {
		return fmt.Errorf("保存更新通道设置失败：%w", err)
	}
	return nil
}

// stable 报告是否只接收正式版。nil 偏好按默认（接收全部版本）处理。
func (s *updateChannelStore) stable() bool { return s != nil && s.stableOnly }

// prerelease 报告是否接收测试版。默认接收，nil 偏好同样按默认处理。
func (s *updateChannelStore) prerelease() bool { return s == nil || !s.stableOnly }

// setStable 开关稳定渠道并落盘。
func (s *updateChannelStore) setStable(on bool) error {
	if s == nil || s.stableOnly == on {
		return nil
	}
	s.stableOnly = on
	return s.save()
}

// setPrerelease 是 setStable 的反选写法，保留给按旧语义调用的入口。
func (s *updateChannelStore) setPrerelease(on bool) error { return s.setStable(!on) }

// Reset 恢复默认通道：关闭稳定渠道，接收全部版本。
func (s *updateChannelStore) Reset() error { return s.setStable(false) }

// applyUpdateChannel 把更新通道接到 mygo 更新器上：pre 为 true 时读版本最高的
// release（含 prerelease），false 时只读 latest release。启动时按保存的选择
// 调一次，设置窗口改动时立即再调。任何 goroutine 可调。
func applyUpdateChannel(pre bool) { mygo.Updater.SetPrereleases(pre) }
