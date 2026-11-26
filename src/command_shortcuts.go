package main

import "github.com/egoist/mygo/ui"

// bindCommandShortcuts 在主窗口视图里声明当前生效的命令键位。用窗口级快捷键
// 而不是输入框上的局部快捷键：焦点在列表、正文或标题栏时都能触发。
//
// 界面缩放的三条命令不在这里注册：它们由 bindZoomShortcuts 挂到 windowView，
// 每扇窗口都能改缩放（设置页、详情窗口里也有效），主窗口因此跳过，免得同一组
// 键被注册两次。
//
// 同一个键位只注册一次：设置页已经拦下了冲突，但旧配置可能来自手工编辑，
// 这里再挡一道，避免两个命令抢同一个组合键。
func (a *app) bindCommandShortcuts(c *ui.Context) {
	if a.commands == nil {
		return
	}
	seen := make(map[string]bool)
	for _, def := range a.commands.available() {
		if isZoomCommand(def.ID) {
			continue
		}
		s := a.commands.binding(def.ID)
		if !s.valid() {
			continue
		}
		raw := s.canonical()
		if raw == "" || seen[raw] {
			continue
		}
		seen[raw] = true
		id := def.ID
		c.OnWindowShortcut(s.Mods, s.Key, func() { a.runCommandByID(id) })
	}
}

// bindZoomShortcuts 把缩放命令注册成窗口级快捷键。挂在 windowView 上，所以
// 主窗口、详情窗口和各附属窗口都能用同一组键改整页缩放；和 Ctrl+滚轮改的是
// 同一份进程级偏好。
func (a *app) bindZoomShortcuts(c *ui.Context) {
	if a.commands == nil {
		return
	}
	seen := make(map[string]bool)
	bind := func(s commandShortcut, fn func()) {
		if !s.valid() {
			return
		}
		raw := s.canonical()
		if raw == "" || seen[raw] {
			return
		}
		seen[raw] = true
		c.OnWindowShortcut(s.Mods, s.Key, fn)
	}
	for _, id := range zoomCommandIDs {
		actionID := id
		bind(a.commands.binding(id), func() { a.runCommandByID(actionID) })
	}
	// 键盘上的 + 实际是 Shift+=，设置页写成 Ctrl+= 时也要认 Ctrl++。用户把
	// 别的命令改到这个组合时，让设置里那条生效，避免一次按键触发两个动作。
	plus := commandShortcut{Mods: ui.Cmd | ui.Shift, Key: ui.KeyEqual}
	bind(plus, a.zoomIn)
}

// zoomCommandIDs 是改整页缩放的三条命令，按命令面板里的顺序。
var zoomCommandIDs = []string{"zoomIn", "zoomOut", "zoomReset"}

func isZoomCommand(id string) bool {
	for _, zoomID := range zoomCommandIDs {
		if id == zoomID {
			return true
		}
	}
	return false
}
