package main

import (
	"bytes"
	"image/png"
	"testing"
)

// 托盘图标是 32×32 的 PNG：16 点在高分屏上的尺寸。图标不对时 Windows 会
// 画出模糊或空白的一格，而这里不会跑到真窗口上才发现。
func TestTrayIconIsA32PixelPNG(t *testing.T) {
	cfg, err := png.DecodeConfig(bytes.NewReader(trayIconPNG))
	if err != nil {
		t.Fatalf("托盘图标不是 PNG：%v", err)
	}
	if cfg.Width != 32 || cfg.Height != 32 {
		t.Errorf("托盘图标 %dx%d，期望 32x32", cfg.Width, cfg.Height)
	}
}
