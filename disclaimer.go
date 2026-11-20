package main

import (
	"fmt"
	"path/filepath"
)

// disclaimerConfig 是 disclaimer.json 的内容：首次启动的声明弹窗是否看过。
type disclaimerConfig struct {
	Seen bool `json:"seen"`
}

// disclaimerStore 进程级共享：首次启动弹一次 T2 声明，确认后落盘，之后不再弹。
// claimed 保证同一时刻只有一扇主窗口负责展示——多账号一起开时也只弹一次。
// 只在主线程读写。
type disclaimerStore struct {
	dir     string
	seen    bool
	claimed bool
}

func newDisclaimerStore(dir string) *disclaimerStore {
	if dir == "" {
		dir = configDir()
	}
	s := &disclaimerStore{dir: dir}
	var cfg disclaimerConfig
	if err := readJSONFile(filepath.Join(dir, "disclaimer.json"), &cfg); err == nil {
		s.seen = cfg.Seen
	}
	return s
}

// claim 认领这次展示：已经看过、或已经被别的窗口认领就返回 false。
func (s *disclaimerStore) claim() bool {
	if s == nil || s.seen || s.claimed {
		return false
	}
	s.claimed = true
	return true
}

// markSeen 记下已看过并落盘。写失败时进程内仍视为已看过，避免反复弹窗。
func (s *disclaimerStore) markSeen() error {
	if s == nil {
		return nil
	}
	s.seen = true
	s.claimed = false
	if err := writeJSONFile(filepath.Join(s.dir, "disclaimer.json"), disclaimerConfig{Seen: true}); err != nil {
		return fmt.Errorf("保存声明确认失败：%w", err)
	}
	return nil
}
