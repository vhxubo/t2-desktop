package main

import (
	"fmt"
	"path/filepath"
)

// 图片的两项显示偏好，全应用一份（见 appDeps），落盘在 images.json：
//
//	lazy    默认不加载图片：正文与评论里的配图先画占位，点一下才下载。默认关。
//	animate 动图自动播放：gif / apng 在预览窗口里逐帧播放。默认开。
//
// 这两项只管帖子正文与评论里的配图：头像、行内表情与站内徽章始终照常加载，
// 关掉图片不该让列表里的头像与表情变成一片空白。正文里的 gif 始终只画首帧，
// 逐帧播放只发生在点开的预览窗口；所有窗口共用一份，改完所有窗口下一帧就按新
// 状态重建。
type imagePrefsStore struct {
	dir     string
	lazy    bool
	animate bool
}

// 用 hide_images / still_gif 而不是正向字段：老版本没有这个文件时，
// lazy=false、animate=true，行为与改造前一致。
type imagePrefsConfig struct {
	HideImages bool `json:"hide_images"`
	StillGIF   bool `json:"still_gif"`
}

func newImagePrefsStore(dir string) *imagePrefsStore {
	if dir == "" {
		dir = configDir()
	}
	s := &imagePrefsStore{dir: dir, animate: true}
	var cfg imagePrefsConfig
	if err := readJSONFile(filepath.Join(dir, "images.json"), &cfg); err == nil {
		s.lazy = cfg.HideImages
		s.animate = !cfg.StillGIF
	}
	return s
}

// save 先写盘：失败时内存里保留用户刚选的值（本次运行仍按新值走），只把错误
// 交给调用方提示；下次启动会回到旧配置。和 theme / closeMode 的处理一致。
func (s *imagePrefsStore) save() error {
	cfg := imagePrefsConfig{HideImages: s.lazy, StillGIF: !s.animate}
	if err := writeJSONFile(filepath.Join(s.dir, "images.json"), cfg); err != nil {
		return fmt.Errorf("保存图片设置失败：%w", err)
	}
	return nil
}

// Reset 恢复默认图片偏好：自动加载配图、动图自动播放。
func (s *imagePrefsStore) Reset() error {
	s.lazy, s.animate = false, true
	return s.save()
}

// lazyLoad 报告是否「默认不加载图片」。
func (s *imagePrefsStore) lazyLoad() bool { return s != nil && s.lazy }

// autoAnimate 报告动图是否自动播放。
func (s *imagePrefsStore) autoAnimate() bool { return s == nil || s.animate }

// setLazy 开关「默认不加载图片」。
func (s *imagePrefsStore) setLazy(on bool) error {
	if s == nil || s.lazy == on {
		return nil
	}
	s.lazy = on
	return s.save()
}

// setAnimate 开关动图自动播放。
func (s *imagePrefsStore) setAnimate(on bool) error {
	if s == nil || s.animate == on {
		return nil
	}
	s.animate = on
	return s.save()
}
