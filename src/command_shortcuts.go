package main

import "github.com/egoist/mygo/ui"

// bindCommandShortcuts 在主窗口视图里声明当前生效的命令键位。用窗口级快捷键
// 而不是输入框上的局部快捷键：焦点在列表、正文或标题栏时都能触发。
//
// 同一个键位只注册一次：设置页已经拦下了冲突，但旧配置可能来自手工编辑，
// 这里再挡一道，避免两个命令抢同一个组合键。
func (a *app) bindCommandShortcuts(c *ui.Context) {
	if a.commands == nil {
		return
	}
	seen := make(map[string]bool)
	for _, def := range a.commands.available() {
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
	// 键盘上的 + 实际是 Shift+=，所以 Ctrl+Shift+= 也算「放大界面」。这条
	// 是不出现在设置里的辅助键位：用户改过任何命令到这个组合时，让设置里的
	// 那条生效，避免一次按键触发两个动作。
	plus := commandShortcut{Mods: ui.Cmd | ui.Shift, Key: ui.KeyEqual}
	if !seen[plus.canonical()] && a.commands.binding("zoomIn").valid() {
		c.OnWindowShortcut(plus.Mods, plus.Key, a.zoomIn)
	}
}
