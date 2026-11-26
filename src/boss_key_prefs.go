package main

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 老板键偏好：启用开关与系统级热键落盘在 boss_key.json，进程级一份。默认不
// 启用——系统热键是全局资源，用户没主动打开前不该悄悄占住一组组合键。
//
// 热键用 mygo 的 accelerator 写法（"CmdOrCtrl+Alt+B"）：CmdOrCtrl 在 Windows /
// Linux 上是 Ctrl，macOS 上是 ⌘，同一份配置换平台仍是用户习惯的物理按键。

const (
	bossKeyPrefsFile  = "boss_key.json"
	bossKeyDefaultAcc = "CmdOrCtrl+Alt+B"
)

type bossKeyConfig struct {
	Enabled     bool   `json:"enabled"`
	Accelerator string `json:"accelerator"`
}

type bossKeyPrefs struct {
	dir     string
	enabled bool
	acc     string
}

func newBossKeyPrefs(dir string) *bossKeyPrefs {
	if dir == "" {
		dir = configDir()
	}
	p := &bossKeyPrefs{dir: dir, acc: bossKeyDefaultAcc}
	var cfg bossKeyConfig
	if err := readJSONFile(filepath.Join(dir, bossKeyPrefsFile), &cfg); err == nil {
		p.enabled = cfg.Enabled
		// 手工编辑的坏值退回默认，不让应用起不来或注册一组无效热键。
		if validBossKeyAcc(cfg.Accelerator) {
			p.acc = cfg.Accelerator
		}
	}
	return p
}

func (p *bossKeyPrefs) path() string { return filepath.Join(p.dir, bossKeyPrefsFile) }

func (p *bossKeyPrefs) on() bool {
	if p == nil {
		return false
	}
	return p.enabled
}

func (p *bossKeyPrefs) accelerator() string {
	if p == nil {
		return bossKeyDefaultAcc
	}
	return p.acc
}

func (p *bossKeyPrefs) save() error {
	return writeJSONFile(p.path(), bossKeyConfig{Enabled: p.enabled, Accelerator: p.acc})
}

func (p *bossKeyPrefs) setEnabled(on bool) error {
	if p == nil {
		return nil
	}
	old := p.enabled
	p.enabled = on
	if err := p.save(); err != nil {
		p.enabled = old
		return fmt.Errorf("保存老板键开关失败：%w", err)
	}
	return nil
}

func (p *bossKeyPrefs) setAccelerator(acc string) error {
	if p == nil || !validBossKeyAcc(acc) {
		return nil
	}
	old := p.acc
	p.acc = acc
	if err := p.save(); err != nil {
		p.acc = old
		return fmt.Errorf("保存老板键热键失败：%w", err)
	}
	return nil
}

// Reset 恢复默认（关闭 + CmdOrCtrl+Alt+B）并落盘，供「一键重置所有设置」使用。
func (p *bossKeyPrefs) Reset() error {
	if p == nil {
		return nil
	}
	old := *p
	p.enabled = false
	p.acc = bossKeyDefaultAcc
	if err := p.save(); err != nil {
		*p = old
		return fmt.Errorf("重置老板键失败：%w", err)
	}
	return nil
}

/* ------------------------------------------------------------------
   组合键与 mygo accelerator 之间的转换
---------------------------------------------------------------- */

// bossKeyAcceleratorFromShortcut 把设置页录到的按键写成 mygo 能注册的
// accelerator。主修饰键统一写成 CmdOrCtrl：Windows / Linux 上 ui.Cmd 与 ui.Ctrl
// 是同一位，macOS 上 ui.Cmd 与 ui.Super 是同一位。按 canonical 的顺序逐个消费
// 修饰键位，避免同一位既写成 CmdOrCtrl 又写成 Super。
func bossKeyAcceleratorFromShortcut(s commandShortcut) (string, bool) {
	if !s.valid() {
		return "", false
	}
	mods := s.Mods
	parts := make([]string, 0, 4)
	if mods&ui.Cmd != 0 {
		parts = append(parts, "CmdOrCtrl")
		mods &^= ui.Cmd
	}
	if mods&ui.Ctrl != 0 {
		parts = append(parts, "Ctrl")
		mods &^= ui.Ctrl
	}
	if mods&ui.Super != 0 {
		parts = append(parts, "Super")
		mods &^= ui.Super
	}
	if mods&ui.Alt != 0 {
		parts = append(parts, "Alt")
		mods &^= ui.Alt
	}
	if mods&ui.Shift != 0 {
		parts = append(parts, "Shift")
		mods &^= ui.Shift
	}
	key, ok := bossKeyAccKey(s.Key)
	if !ok || len(parts) == 0 {
		return "", false
	}
	return strings.Join(append(parts, key), "+"), true
}

// bossKeyAccKey 把 ui.Key 换成 accelerator 的主键。字母数字写成大写单字符；
// 标点在 accelerator 里也得是单字符（它没有 "minus" 这类名字）；其余只用
// accelerator 认识的具名键，不认识的一律拒绝录制，免得注册时报错。
func bossKeyAccKey(key ui.Key) (string, bool) {
	switch key {
	case ui.KeyMinus:
		return "-", true
	case ui.KeyEqual:
		return "=", true
	case ui.KeyComma:
		return ",", true
	case ui.KeyPeriod:
		return ".", true
	case ui.KeySlash:
		return "/", true
	case ui.KeySemicolon:
		return ";", true
	case ui.KeyQuote:
		return "'", true
	case ui.KeyBracketLeft:
		return "[", true
	case ui.KeyBracketRight:
		return "]", true
	case ui.KeyBackslash:
		return "\\", true
	case ui.KeyBackquote:
		return "`", true
	}
	name, ok := shortcutKeyNames[key]
	if !ok {
		return "", false
	}
	switch name {
	case "esc":
		return "Escape", true
	case "space":
		return "Space", true
	case "pageup":
		return "PageUp", true
	case "pagedown":
		return "PageDown", true
	case "tab", "enter", "backspace", "delete", "insert", "home", "end",
		"left", "right", "up", "down":
		return strings.ToUpper(name[:1]) + name[1:], true
	}
	if len(name) == 1 && ((name[0] >= 'a' && name[0] <= 'z') ||
		(name[0] >= '0' && name[0] <= '9')) {
		return strings.ToUpper(name), true
	}
	if len(name) >= 2 && name[0] == 'f' {
		n, digits := 0, true
		for i := 1; i < len(name); i++ {
			if name[i] < '0' || name[i] > '9' {
				digits = false
				break
			}
			n = n*10 + int(name[i]-'0')
		}
		if digits && n >= 1 && n <= 24 {
			return strings.ToUpper(name), true
		}
	}
	return "", false
}

// validBossKeyAcc 校验手工编辑过的配置：至少一个修饰键，主键是 accelerator
// 认识的写法。它不依赖 mygo 的私有包，规则和 internal/accelerator 保持一致。
func validBossKeyAcc(acc string) bool {
	parts := strings.Split(acc, "+")
	if len(parts) < 2 {
		return false
	}
	mods := 0
	for _, part := range parts[:len(parts)-1] {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "cmdorctrl", "commandorcontrol", "cmd", "command", "super",
			"meta", "win", "ctrl", "control", "alt", "option", "shift":
			mods++
		default:
			return false
		}
	}
	return mods > 0 && validBossKeyAccKey(parts[len(parts)-1])
}

func validBossKeyAccKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	if len(key) == 1 {
		r := rune(key[0])
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune("-=,./;'\\[]`", r)
	}
	lower := strings.ToLower(key)
	switch lower {
	case "space", "tab", "capslock", "numlock", "scrolllock", "backspace",
		"delete", "insert", "enter", "up", "down", "left", "right", "home",
		"end", "pageup", "pagedown", "escape", "plus":
		return true
	}
	if len(lower) >= 2 && lower[0] == 'f' {
		var n int
		if _, err := fmt.Sscanf(lower[1:], "%d", &n); err == nil &&
			n >= 1 && n <= 24 && fmt.Sprint(n) == lower[1:] {
			return true
		}
	}
	return false
}

// bossKeyAccLabel 把持久化的写法变成界面上给人看的键位文字。
func bossKeyAccLabel(acc string) string {
	if !validBossKeyAcc(acc) {
		return ""
	}
	parts := strings.Split(acc, "+")
	for i, part := range parts[:len(parts)-1] {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "cmdorctrl", "commandorcontrol":
			if runtime.GOOS == "darwin" {
				parts[i] = "⌘"
			} else {
				parts[i] = "Ctrl"
			}
		case "cmd", "command", "super", "meta", "win":
			if runtime.GOOS == "darwin" {
				parts[i] = "⌘"
			} else {
				parts[i] = "Win"
			}
		case "ctrl", "control":
			parts[i] = "Ctrl"
		case "alt", "option":
			parts[i] = "Alt"
		case "shift":
			parts[i] = "Shift"
		}
	}
	key := strings.TrimSpace(parts[len(parts)-1])
	if len(key) == 1 {
		key = strings.ToUpper(key)
	}
	parts[len(parts)-1] = key
	return strings.Join(parts, "+")
}
