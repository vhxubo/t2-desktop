package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"time"
)

// 后台检查更新的间隔偏好，进程级一份（见 appDeps），落盘在 update_interval.json。
// 默认 6 小时：mygo 更新插件自己的间隔固定在建插件时（24 小时）且没有运行时
// 修改入口，所以检查由本应用的后台循环负责，插件只保留手动检查的窗口。
const defaultUpdateIntervalHours = 6

var (
	updateIntervalOptions = []string{"1小时", "6小时", "12小时", "24小时"}
	updateIntervalHours   = []int{1, 6, 12, 24}
)

type updateIntervalPrefsStore struct {
	dir   string
	hours int
}

type updateIntervalConfig struct {
	Hours int `json:"hours"`
}

func newUpdateIntervalPrefsStore(dir string) *updateIntervalPrefsStore {
	if dir == "" {
		dir = configDir()
	}
	s := &updateIntervalPrefsStore{dir: dir, hours: defaultUpdateIntervalHours}
	var cfg updateIntervalConfig
	if err := readJSONFile(filepath.Join(dir, "update_interval.json"), &cfg); err == nil &&
		slices.Contains(updateIntervalHours, cfg.Hours) {
		s.hours = cfg.Hours
	}
	return s
}

// interval 返回当前间隔；nil 偏好与损坏的数值都回默认 6 小时。
func (s *updateIntervalPrefsStore) interval() time.Duration {
	if s == nil || !slices.Contains(updateIntervalHours, s.hours) {
		return defaultUpdateIntervalHours * time.Hour
	}
	return time.Duration(s.hours) * time.Hour
}

// index 返回当前档位在 updateIntervalOptions 中的序号。
func (s *updateIntervalPrefsStore) index() int {
	h := s.interval() / time.Hour
	for i, v := range updateIntervalHours {
		if v == int(h) {
			return i
		}
	}
	return 0
}

func (s *updateIntervalPrefsStore) save() error {
	if err := writeJSONFile(filepath.Join(s.dir, "update_interval.json"),
		updateIntervalConfig{Hours: s.hours}); err != nil {
		return fmt.Errorf("保存检查更新间隔失败：%w", err)
	}
	return nil
}

// setHours 切换档位并落盘，未知档位按默认处理。
func (s *updateIntervalPrefsStore) setHours(hours int) error {
	if s == nil {
		return nil
	}
	if !slices.Contains(updateIntervalHours, hours) {
		hours = defaultUpdateIntervalHours
	}
	if s.hours == hours {
		return nil
	}
	s.hours = hours
	return s.save()
}

// Reset 恢复默认间隔：6 小时。
func (s *updateIntervalPrefsStore) Reset() error {
	if s == nil || s.hours == defaultUpdateIntervalHours {
		return nil
	}
	s.hours = defaultUpdateIntervalHours
	return s.save()
}
