package main

import (
	"fmt"
	"path/filepath"
)

// 评论费用相关的两个开关，全应用一份（见 appDeps），落盘在 comment-cost.json：
//
//	showBar    评论框上方的「金币消耗与徽章切换」状态栏。默认开，保持原有行为；
//	            关掉后不画状态栏，也不再读徽章与佩戴状态。
//	showOnSend 把预计消耗金币数画到评论的「发送」按钮上。默认关。
//
// 发送按钮上的数字属于「金币消耗」的一部分，所以它只在徽章数据已经加载时
// 出现；关掉 showBar 后仍可单独开这一项，数据会按需读取。
type commentCostPrefsStore struct {
	dir        string
	showBar    bool
	showOnSend bool
}

// 用 hide_bar 而不是 show_bar：老版本没有这个文件时默认 showBar = true，
// 保持原先一直显示状态栏的行为；show_on_send 缺失即默认关。
type commentCostPrefsConfig struct {
	HideBar    bool `json:"hide_bar"`
	ShowOnSend bool `json:"show_on_send"`
}

func newCommentCostPrefsStore(dir string) *commentCostPrefsStore {
	if dir == "" {
		dir = configDir()
	}
	s := &commentCostPrefsStore{dir: dir, showBar: true}
	var cfg commentCostPrefsConfig
	if err := readJSONFile(filepath.Join(dir, "comment-cost.json"), &cfg); err == nil {
		s.showBar = !cfg.HideBar
		s.showOnSend = cfg.ShowOnSend
	}
	return s
}

// save 先写盘：失败时内存里保留用户刚选的值（本次运行仍按新值走），只把错误
// 交给调用方提示；下次启动会回到旧配置。和 theme / closeMode 的处理一致。
func (s *commentCostPrefsStore) save() error {
	cfg := commentCostPrefsConfig{HideBar: !s.showBar, ShowOnSend: s.showOnSend}
	if err := writeJSONFile(filepath.Join(s.dir, "comment-cost.json"), cfg); err != nil {
		return fmt.Errorf("保存评论费用设置失败：%w", err)
	}
	return nil
}

func (s *commentCostPrefsStore) visible() bool { return s.showBar }
func (s *commentCostPrefsStore) onSend() bool  { return s.showOnSend }

// setVisible 开关「金币消耗与徽章切换」状态栏。
func (s *commentCostPrefsStore) setVisible(on bool) error {
	if s.showBar == on {
		return nil
	}
	s.showBar = on
	return s.save()
}

// setOnSend 开关发送按钮上的消耗金币数。
func (s *commentCostPrefsStore) setOnSend(on bool) error {
	if s.showOnSend == on {
		return nil
	}
	s.showOnSend = on
	return s.save()
}
