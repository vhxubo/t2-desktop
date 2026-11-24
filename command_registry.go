package main

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

// 命令中心采用注册表：每一条命令只定义一次元数据与动作，命令面板、快捷键
// 与设置页都从同一份注册表读取。内置命令在 command_builtin.go 注册，其它
// 功能以后只要调 a.registerCommand 就能加进来，不必再改面板的硬编码列表。
//
// 快捷键的偏好与收藏 / 最近使用一起放在配置目录的 commands.json 里：
//   - bindings 是「命令 id -> 规范键位」。缺省项表示沿用默认键位；"" 表示
//     用户显式清除了这条命令的快捷键；其余值是 "cmd+k" 这样的规范写法。
//   - favorites / recent 是命令 id 列表，决定面板顶部的收藏与最近使用。
//
// 键位用 "cmd/ctrl/alt/shift/super + 键名" 的规范形式保存，其中的 "cmd" 是
// 平台主修饰键（Windows/Linux 的 Ctrl、macOS 的 Command），所以同一份配置
// 换平台后仍然是用户习惯的那组物理按键。

const commandPrefsFile = "commands.json"

// 命令分组。面板在没有过滤词时按这个顺序分组显示。
const (
	commandCategoryNavigate = "页面导航"
	commandCategoryCreate   = "创作"
	commandCategorySocial   = "社区"
	commandCategoryAccount  = "账号"
	commandCategoryTools    = "工具"
)

var commandCategories = []string{
	commandCategoryNavigate,
	commandCategoryCreate,
	commandCategorySocial,
	commandCategoryAccount,
	commandCategoryTools,
}

// commandShortcut 是一条可配置的组合键。Mods 用 mygo 的修饰键位，Key 是主键。
type commandShortcut struct {
	Mods ui.Modifiers
	Key  ui.Key
}

func (s commandShortcut) valid() bool { return s.Key != 0 && s.Key != ui.KeyUnknown }

// canonical 把键位写成稳定的小写字符串，例如 "cmd+shift+k"。规范顺序是
// 主修饰键、Ctrl、Super、Alt、Shift；在 Windows 上 ui.Cmd 与 ui.Ctrl 是同一位，
// 先取 ui.Cmd 就能避免把 Ctrl 写成两个 token。
func (s commandShortcut) canonical() string {
	mods := s.Mods
	parts := make([]string, 0, 4)
	if mods&ui.Cmd != 0 {
		parts = append(parts, "cmd")
		mods &^= ui.Cmd
	}
	if mods&ui.Ctrl != 0 {
		parts = append(parts, "ctrl")
		mods &^= ui.Ctrl
	}
	if mods&ui.Super != 0 {
		parts = append(parts, "super")
		mods &^= ui.Super
	}
	if mods&ui.Alt != 0 {
		parts = append(parts, "alt")
	}
	if mods&ui.Shift != 0 {
		parts = append(parts, "shift")
	}
	name, ok := shortcutKeyNames[s.Key]
	if !ok {
		return ""
	}
	return strings.Join(append(parts, name), "+")
}

// label 是给用户看的写法：Windows/Linux 上 "cmd" 显示为 Ctrl，macOS 上
// 显示为 ⌘。其它修饰键保持通用名称。
func (s commandShortcut) label() string {
	raw := s.canonical()
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "+")
	for i, p := range parts {
		switch p {
		case "cmd":
			if runtime.GOOS == "darwin" {
				parts[i] = "⌘"
			} else {
				parts[i] = "Ctrl"
			}
		case "ctrl":
			parts[i] = "Ctrl"
		case "alt":
			parts[i] = "Alt"
		case "shift":
			parts[i] = "Shift"
		case "super":
			parts[i] = "Win"
		default:
			// 单字母键名显示成大写：Ctrl+Shift+T，而不是 Ctrl+Shift+t。
			if len(p) == 1 && p[0] >= 'a' && p[0] <= 'z' {
				parts[i] = strings.ToUpper(p)
			}
		}
	}
	return strings.Join(parts, "+")
}

// parseCommandShortcut 把配置里的规范写法还原成键位。未知键名或没有主键
// 都当作无效；旧版本写错的项会退回默认键位，不会让应用起不来。
func parseCommandShortcut(raw string) (commandShortcut, bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return commandShortcut{}, false
	}
	var mods ui.Modifiers
	keyName := ""
	hasModifier := false
	for _, part := range strings.Split(raw, "+") {
		switch part {
		case "cmd":
			mods |= ui.Cmd
			hasModifier = true
		case "ctrl":
			mods |= ui.Ctrl
			hasModifier = true
		case "alt":
			mods |= ui.Alt
			hasModifier = true
		case "shift":
			mods |= ui.Shift
			hasModifier = true
		case "super":
			mods |= ui.Super
			hasModifier = true
		default:
			if keyName != "" {
				return commandShortcut{}, false
			}
			keyName = part
		}
	}
	key, ok := shortcutNameToKeys[keyName]
	if !ok || !hasModifier {
		return commandShortcut{}, false
	}
	return commandShortcut{Mods: mods, Key: key}, true
}

// shortcutKeyNames / shortcutNameToKeys 覆盖字母、数字、功能键与常用编辑键。
// Key 常量在 mygo 里是连续区间，这里按区间生成，避免手写 60 行映射漏项。
var shortcutKeyNames = map[ui.Key]string{}
var shortcutNameToKeys = map[string]ui.Key{}

func init() {
	add := func(key ui.Key, name string) {
		shortcutKeyNames[key] = name
		shortcutNameToKeys[name] = key
	}
	for i := 0; i < 26; i++ {
		add(ui.KeyA+ui.Key(i), string(rune('a'+i)))
	}
	for i := 0; i < 10; i++ {
		add(ui.Key0+ui.Key(i), string(rune('0'+i)))
	}
	for i := 0; i < 12; i++ {
		add(ui.KeyF1+ui.Key(i), fmt.Sprintf("f%d", i+1))
	}
	for key, name := range map[ui.Key]string{
		ui.KeyEnter: "enter", ui.KeyEscape: "esc", ui.KeyBackspace: "backspace",
		ui.KeyTab: "tab", ui.KeySpace: "space", ui.KeyDelete: "delete",
		ui.KeyInsert: "insert", ui.KeyHome: "home", ui.KeyEnd: "end",
		ui.KeyPageUp: "pageup", ui.KeyPageDown: "pagedown",
		ui.KeyLeft: "left", ui.KeyRight: "right", ui.KeyUp: "up", ui.KeyDown: "down",
		ui.KeyMinus: "minus", ui.KeyEqual: "equal", ui.KeyComma: "comma",
		ui.KeyPeriod: "period", ui.KeySlash: "slash", ui.KeySemicolon: "semicolon",
		ui.KeyQuote: "quote", ui.KeyBracketLeft: "bracketleft",
		ui.KeyBracketRight: "bracketright", ui.KeyBackslash: "backslash",
		ui.KeyBackquote: "backquote",
	} {
		add(key, name)
	}
}

// cmdKey 是注册默认快捷键时的简写：平台主修饰键 + 主键。
func cmdKey(key ui.Key) commandShortcut {
	return commandShortcut{Mods: ui.Cmd, Key: key}
}

// cmdShiftKey 是平台主修饰键加 Shift 的简写。
func cmdShiftKey(key ui.Key) commandShortcut {
	return commandShortcut{Mods: ui.Cmd | ui.Shift, Key: key}
}

// commandDef 是一条可注册的命令。Run 在 UI 线程执行；Available 为空表示
// 任何登录态都能用，仅供面板过滤，快捷键触发时也会再查一次。
type commandDef struct {
	ID              string
	Title           string
	Hint            string
	Keywords        string
	Category        string
	Icon            *ui.SVG
	DefaultShortcut commandShortcut
	Available       func() bool
	Run             func()
}

// commandRegistry 是当前窗口可用的命令表。定义按注册顺序保存，命令面板与
// 设置页都按这个顺序展示；偏好是进程级共享的，所以多账号窗口的快捷键和
// 收藏始终一致。
type commandRegistry struct {
	prefs *commandPrefs
	defs  []commandDef
	byID  map[string]int
}

func newCommandRegistry(prefs *commandPrefs) *commandRegistry {
	if prefs == nil {
		prefs = newCommandPrefs("")
	}
	return &commandRegistry{prefs: prefs, byID: make(map[string]int)}
}

// register 添加一条命令。重复 id 只保留第一次注册的那条：后加载的插件不能
// 悄悄顶掉已有入口，除非先换一个新 id。
func (r *commandRegistry) register(def commandDef) bool {
	if def.ID == "" || def.Run == nil || def.Title == "" {
		return false
	}
	if _, exists := r.byID[def.ID]; exists {
		return false
	}
	if def.Category == "" {
		def.Category = commandCategoryTools
	}
	r.byID[def.ID] = len(r.defs)
	r.defs = append(r.defs, def)
	return true
}

// get 按 id 查定义。
func (r *commandRegistry) get(id string) (commandDef, bool) {
	if r == nil {
		return commandDef{}, false
	}
	i, ok := r.byID[id]
	if !ok || i >= len(r.defs) {
		return commandDef{}, false
	}
	return r.defs[i], true
}

// available 返回当前能用的命令，保持注册顺序。登录态相关的命令由
// Available 决定，未登录窗口里不会出现「退出登录」这类入口。
func (r *commandRegistry) available() []commandDef {
	if r == nil {
		return nil
	}
	out := make([]commandDef, 0, len(r.defs))
	for _, def := range r.defs {
		if def.Available == nil || def.Available() {
			out = append(out, def)
		}
	}
	return out
}

// binding 返回命令当前生效的键位：用户没有改过时用默认，显式清除时返回
// 无效值。配置里的非法写法同样退回默认，不会凭空禁掉入口。
func (r *commandRegistry) binding(id string) commandShortcut {
	def, ok := r.get(id)
	if !ok {
		return commandShortcut{}
	}
	if r.prefs == nil {
		return def.DefaultShortcut
	}
	raw, customized := r.prefs.binding(id)
	if !customized {
		return def.DefaultShortcut
	}
	// 空串是用户显式清除，不是配置损坏，不能退回默认键位。
	if raw == "" {
		return commandShortcut{}
	}
	s, ok := parseCommandShortcut(raw)
	if !ok {
		return def.DefaultShortcut
	}
	return s
}

// bindingLabel 是给用户看的键位文字，没有绑定时为空串。
func (r *commandRegistry) bindingLabel(id string) string {
	s := r.binding(id)
	if !s.valid() {
		return ""
	}
	return s.label()
}

// conflict 返回与键位 s 冲突的另一条命令标题；没有冲突返回空串。被清除的
// 键位、以及同一个命令自己的旧绑定都不算冲突。
func (r *commandRegistry) conflict(id string, s commandShortcut) string {
	if r == nil || !s.valid() {
		return ""
	}
	for _, def := range r.defs {
		if def.ID == id {
			continue
		}
		if r.binding(def.ID) == s {
			return def.Title
		}
	}
	return ""
}

// bindings 给设置页列出所有命令与当前键位，顺序即注册顺序。
type commandBinding struct {
	Def   commandDef
	Label string
}

func (r *commandRegistry) bindings() []commandBinding {
	defs := r.available()
	out := make([]commandBinding, 0, len(defs))
	for _, def := range defs {
		out = append(out, commandBinding{Def: def, Label: r.bindingLabel(def.ID)})
	}
	return out
}

// commandPrefsConfig 是 commands.json 的内容。
type commandPrefsConfig struct {
	Bindings  map[string]string `json:"bindings,omitempty"`
	Favorites []string          `json:"favorites,omitempty"`
	Recent    []string          `json:"recent,omitempty"`
}

// commandPrefs 保存命令相关的用户偏好。文件读写只在主线程做，但快照会被
// 后台 goroutine 间接读到（例如渲染被 publish 打断），所以字段加锁。
type commandPrefs struct {
	mu  sync.Mutex
	dir string
	cfg commandPrefsConfig
}

type commandPrefsSnapshot struct {
	Bindings  map[string]string
	Favorites []string
	Recent    []string
}

func newCommandPrefs(dir string) *commandPrefs {
	if dir == "" {
		dir = configDir()
	}
	p := &commandPrefs{dir: dir, cfg: commandPrefsConfig{Bindings: map[string]string{}}}
	var cfg commandPrefsConfig
	if err := readJSONFile(filepath.Join(dir, commandPrefsFile), &cfg); err == nil {
		if cfg.Bindings != nil {
			p.cfg.Bindings = cfg.Bindings
		}
		p.cfg.Favorites = cfg.Favorites
		p.cfg.Recent = cfg.Recent
	}
	return p
}

func (p *commandPrefs) path() string { return filepath.Join(p.dir, commandPrefsFile) }

func (p *commandPrefs) save() error {
	return writeJSONFile(p.path(), p.cfg)
}

func (p *commandPrefs) snapshot() commandPrefsSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := commandPrefsSnapshot{
		Bindings:  make(map[string]string, len(p.cfg.Bindings)),
		Favorites: append([]string(nil), p.cfg.Favorites...),
		Recent:    append([]string(nil), p.cfg.Recent...),
	}
	for id, raw := range p.cfg.Bindings {
		out.Bindings[id] = raw
	}
	return out
}

// binding 查用户是否改过这条命令的键位；第二个返回值表示配置里有没有这一项。
func (p *commandPrefs) binding(id string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	raw, ok := p.cfg.Bindings[id]
	return raw, ok
}

// setBinding 设置或清除键位。raw 为空串表示用户显式关闭这条快捷键。
func (p *commandPrefs) setBinding(id, raw string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Bindings[id] = raw
	if err := p.save(); err != nil {
		delete(p.cfg.Bindings, id)
		return fmt.Errorf("保存快捷键失败：%w", err)
	}
	return nil
}

// resetBinding 删掉用户覆盖项，回到默认键位。
func (p *commandPrefs) resetBinding(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	old, existed := p.cfg.Bindings[id]
	if !existed {
		return nil
	}
	delete(p.cfg.Bindings, id)
	if err := p.save(); err != nil {
		p.cfg.Bindings[id] = old
		return fmt.Errorf("恢复默认快捷键失败：%w", err)
	}
	return nil
}

// resetAllBindings 只清键位覆盖，不动收藏与最近使用。
func (p *commandPrefs) resetAllBindings() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.cfg.Bindings
	p.cfg.Bindings = map[string]string{}
	if err := p.save(); err != nil {
		p.cfg.Bindings = old
		return fmt.Errorf("恢复默认快捷键失败：%w", err)
	}
	return nil
}

// resetAll 把命令偏好整份恢复为默认值：键位、收藏与最近使用全部清空。
// 一键重置设置用的是这一份；快捷键页的「全部恢复默认」只清键位（resetAllBindings）。
func (p *commandPrefs) resetAll() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	old := p.cfg
	p.cfg = commandPrefsConfig{Bindings: map[string]string{}}
	if err := p.save(); err != nil {
		p.cfg = old
		return fmt.Errorf("恢复默认设置失败：%w", err)
	}
	return nil
}

// toggleFavorite 切换收藏，返回切换后是否收藏。
func (p *commandPrefs) toggleFavorite(id string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	old := append([]string(nil), p.cfg.Favorites...)
	wasFavorite := false
	kept := make([]string, 0, len(old)+1)
	for _, fav := range old {
		if fav == id {
			wasFavorite = true
			continue
		}
		kept = append(kept, fav)
	}
	on := !wasFavorite
	if on {
		kept = append(kept, id)
	}
	p.cfg.Favorites = kept
	if err := p.save(); err != nil {
		p.cfg.Favorites = old
		return wasFavorite, fmt.Errorf("保存收藏失败：%w", err)
	}
	return on, nil
}

// noteUsed 把刚执行的命令记到最近使用的最前面，最多保留 recentLimit 条。
func (p *commandPrefs) noteUsed(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	old := append([]string(nil), p.cfg.Recent...)
	next := make([]string, 0, recentLimit)
	next = append(next, id)
	for _, prev := range old {
		if prev != id {
			next = append(next, prev)
		}
		if len(next) >= recentLimit {
			break
		}
	}
	p.cfg.Recent = next
	if err := p.save(); err != nil {
		p.cfg.Recent = old
		return fmt.Errorf("保存最近使用失败：%w", err)
	}
	return nil
}

const recentLimit = 6

// runCommandByID 是命令的统一步入口：面板点击、回车与快捷键都走这里，保证
// 「最近使用」和关闭面板的行为一致。
func (a *app) runCommandByID(id string) {
	def, ok := a.commands.get(id)
	if !ok || (def.Available != nil && !def.Available()) {
		return
	}
	// 从面板执行才需要收起；窗口快捷键触发时面板本来就是关的，别把焦点从
	// 用户正在输入的地方挪到标题栏。
	if a.palette.open {
		a.closePalette()
	}
	if a.commands.prefs != nil {
		_ = a.commands.prefs.noteUsed(id)
	}
	def.Run()
}

// commandByName 供设置页显示冲突提示时按 id 找名字（不要求当前可用）。
func (a *app) commandName(id string) string {
	if def, ok := a.commands.get(id); ok {
		return def.Title
	}
	return id
}

// sortedCommandNames 给测试与配置说明用：按 id 排序的稳定列表。
func sortedCommandNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
