package main

import (
	"fmt"
	"path/filepath"
)

// 自动签到偏好，进程级一份（见 appDeps），落盘在 auto_sign.json。默认开启，
// 所以存「关闭」标记：老版本没有这个文件时 enabled 保持 true，升级后无需改动
// 就会自动签到。
type autoSignPrefsStore struct {
	dir     string
	enabled bool
}

type autoSignPrefsConfig struct {
	Disabled bool `json:"disabled"`
}

func newAutoSignPrefsStore(dir string) *autoSignPrefsStore {
	if dir == "" {
		dir = configDir()
	}
	s := &autoSignPrefsStore{dir: dir, enabled: true}
	var cfg autoSignPrefsConfig
	if err := readJSONFile(filepath.Join(dir, "auto_sign.json"), &cfg); err == nil {
		s.enabled = !cfg.Disabled
	}
	return s
}

// save 先写盘：失败时内存里保留用户刚选的值（本次运行仍按新值走），只把错误
// 交给调用方提示；下次启动会回到旧配置。和 image / watchLater 偏好的处理一致。
func (s *autoSignPrefsStore) save() error {
	cfg := autoSignPrefsConfig{Disabled: !s.enabled}
	if err := writeJSONFile(filepath.Join(s.dir, "auto_sign.json"), cfg); err != nil {
		return fmt.Errorf("保存自动签到设置失败：%w", err)
	}
	return nil
}

// Reset 恢复默认：打开应用时自动签到。
func (s *autoSignPrefsStore) Reset() error {
	s.enabled = true
	return s.save()
}

// on 报告「打开应用时自动签到」是否开启。nil 偏好按默认开启处理。
func (s *autoSignPrefsStore) on() bool { return s == nil || s.enabled }

// setEnabled 开关自动签到并落盘。
func (s *autoSignPrefsStore) setEnabled(on bool) error {
	if s == nil || s.enabled == on {
		return nil
	}
	s.enabled = on
	return s.save()
}
