package main

import (
	"fmt"
	"path/filepath"
)

// 稍后再看的打开行为，全应用一份（见 appDeps），落盘在 watch-later.json：
//
//	keep_after_open=false（默认）  打开过的条目直接从稍后再看移除，当临时队列用。
//	keep_after_open=true           打开后留在列表里并标为已读，未读在前、已读置灰，
//	                               当长期待读清单用。
//
// 记录本身存在本机（见 watch_later.go），这份开关也是本机的阅读习惯，所有窗口
// 共用一份。
type watchLaterPrefsStore struct {
	dir  string
	keep bool
}

type watchLaterPrefsConfig struct {
	KeepAfterOpen bool `json:"keep_after_open"`
}

func newWatchLaterPrefsStore(dir string) *watchLaterPrefsStore {
	if dir == "" {
		dir = configDir()
	}
	s := &watchLaterPrefsStore{dir: dir}
	var cfg watchLaterPrefsConfig
	if err := readJSONFile(filepath.Join(dir, "watch-later.json"), &cfg); err == nil {
		s.keep = cfg.KeepAfterOpen
	}
	return s
}

// save 先写盘：失败时内存里保留用户刚选的值（本次运行仍按新值走），只把错误
// 交给调用方提示；下次启动会回到旧配置。和 theme / closeMode 的处理一致。
func (s *watchLaterPrefsStore) save() error {
	cfg := watchLaterPrefsConfig{KeepAfterOpen: s.keep}
	if err := writeJSONFile(filepath.Join(s.dir, "watch-later.json"), cfg); err != nil {
		return fmt.Errorf("保存稍后再看设置失败：%w", err)
	}
	return nil
}

// Reset 恢复默认阅读行为：打开后从稍后再看移除。
func (s *watchLaterPrefsStore) Reset() error {
	s.keep = false
	return s.save()
}

func (s *watchLaterPrefsStore) keepAfterOpen() bool { return s.keep }

// setKeepAfterOpen 切换「打开后保留」。关掉时调用方要把已有的已读标记清掉：
// 默认模式没有「已读」这个概念，留着会让重新打开开关时冒出上次的旧状态。
func (s *watchLaterPrefsStore) setKeepAfterOpen(on bool) error {
	if s.keep == on {
		return nil
	}
	s.keep = on
	return s.save()
}
