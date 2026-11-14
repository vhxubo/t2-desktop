//go:build !windows

package main

// removeWindowChrome 在非 Windows 上是空操作：边框与阴影是 Windows 11 的 DWM
// 给「圆角有框」的窗口画的，macOS 与 Linux 的窗口系统不会给无边框窗口加这些
// （见 docs/t2-pet.md）。
func removeWindowChrome(uintptr) {}
