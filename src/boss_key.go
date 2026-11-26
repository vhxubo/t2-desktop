package main

import (
	"log"

	"github.com/egoist/mygo"
)

// 老板键的运行时部分：把偏好接成系统级热键，按下时把本账号所有窗口一起藏
// 起来，再按一次恢复。注册 / 隐藏 / 显示都单独包一层，让无头测试只验证逻辑，
// 不碰 mygo 的主循环与原生窗口。

var (
	bossKeyRegister   = func(acc string, fn func()) error { return mygo.GlobalShortcut.Register(acc, fn) }
	bossKeyUnregister = func(acc string) { mygo.GlobalShortcut.Unregister(acc) }
	// bossKeyHideWindow 每次调用都读 window_mode.go 的 hideNativeWindow，测试里
	// 替换那个钩子也能覆盖老板键的隐藏动作。
	bossKeyHideWindow = func(w *mygo.Window) { hideNativeWindow(w) }
	bossKeyShowWindow = func(w *mygo.Window) {
		if w.IsMinimized() {
			w.Restore()
		}
		w.Show()
		w.Focus()
	}
)

// bossKeyWindows 是本账号所有还活着的窗口：主窗口、详情窗口与各附属窗口。
// 同一个窗口只出现一次（主窗口关掉后 a.win 可能指向一扇附属窗口）。
func (a *app) bossKeyWindows() []*mygo.Window {
	if a == nil {
		return nil
	}
	var wins []*mygo.Window
	seen := make(map[*mygo.Window]bool)
	add := func(w *mygo.Window) {
		if w != nil && !seen[w] {
			seen[w] = true
			wins = append(wins, w)
		}
	}
	add(a.win)
	for _, h := range a.detailWindows {
		if h != nil && !h.removed {
			add(h.win)
		}
	}
	for _, w := range a.auxiliaryWindows() {
		add(w)
	}
	return wins
}

// toggleBossKey 是热键按下时的动作：第一次记住哪些窗口当时可见，然后全部
// 隐藏；第二次只恢复这些窗口，不把本来就关着 / 藏着的窗口翻出来。只在主线程
// 调用（GlobalShortcut 的回调就在主线程）。
func (a *app) toggleBossKey() {
	if a == nil {
		return
	}
	if !a.bossKeyHidden {
		shown := make([]*mygo.Window, 0)
		for _, w := range a.bossKeyWindows() {
			if w.IsVisible() {
				shown = append(shown, w)
			}
			bossKeyHideWindow(w)
		}
		a.bossKeyShown = shown
		a.bossKeyHidden = true
		return
	}
	a.restoreBossKeyWindows()
}

// restoreBossKeyWindows 把老板键藏起来的窗口放回去，并清掉隐藏状态。关闭老板
// 键、重置设置时也会调用，避免用户再也找不到回到窗口的入口。
func (a *app) restoreBossKeyWindows() {
	if a == nil {
		return
	}
	for _, w := range a.bossKeyShown {
		bossKeyShowWindow(w)
	}
	a.bossKeyShown = nil
	a.bossKeyHidden = false
}

/* ------------------------------------------------------------------
   偏好改动 → 系统热键
---------------------------------------------------------------- */

func (a *app) setBossKeyEnabled(on bool) {
	p := a.bossKeyPrefs
	if p == nil || p.on() == on {
		return
	}
	if err := p.setEnabled(on); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	a.applyBossKeyPref()
}

func (a *app) setBossKeyAccelerator(acc string) {
	p := a.bossKeyPrefs
	if p == nil || p.accelerator() == acc {
		return
	}
	if err := p.setAccelerator(acc); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	a.applyBossKeyPref()
}

// applyBossKeyPref 在偏好落盘后重接系统热键，并让设置窗口下一帧显示新状态。
func (a *app) applyBossKeyPref() {
	if a.desktop != nil {
		a.desktop.applyBossKey()
	}
	a.publish(func() {})
}

// applyBossKey 按当前偏好注册 / 注销系统热键。只在主线程调用：应用就绪时一次，
// 之后每次改偏好各一次。d.bossKeyAcc 记的是真正注册成功的那一组，失败时留空，
// 下次改动会再试；注销旧键后立刻注册新键，不会同时占住两组。
func (d *desktop) applyBossKey() {
	if d == nil || !d.mainLoop {
		return
	}
	acc := ""
	if p := d.bossKeyPrefs; p != nil && p.on() && validBossKeyAcc(p.accelerator()) {
		acc = p.accelerator()
	}
	if d.bossKeyAcc != "" && d.bossKeyAcc != acc {
		bossKeyUnregister(d.bossKeyAcc)
		d.bossKeyAcc = ""
	}
	if acc == "" {
		// 关掉老板键时把藏起来的窗口放回来，否则用户看不到任何入口。
		if a := d.app; a != nil {
			a.restoreBossKeyWindows()
		}
		return
	}
	if d.bossKeyAcc == acc {
		return
	}
	if err := bossKeyRegister(acc, d.bossKeyPressed); err != nil {
		log.Printf("注册老板键 %s 失败：%v", acc, err)
		if a := d.app; a != nil {
			a.toast(toastSettings, "老板键注册失败："+err.Error())
		}
		return
	}
	d.bossKeyAcc = acc
	log.Printf("老板键已注册：%s", acc)
}

// bossKeyPressed 是系统热键的回调。mygo 保证回调在主线程执行，这里再过一次
// onMain，好让无头测试里直接调用也不会碰原生窗口。
func (d *desktop) bossKeyPressed() {
	d.onMain(func() {
		if a := d.app; a != nil {
			a.toggleBossKey()
		}
	})
}
