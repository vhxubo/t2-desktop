package main

import (
	"fmt"
	"path/filepath"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/choice"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 侧边栏导航自定义：首页到「节点」分组之前的所有入口都可以单独显示 / 隐藏并
// 拖动排序。偏好是进程级的（见 appDeps），所有窗口共用同一份，落盘在
// sidebar.json；底部设置 / 关于入口与节点快捷入口不在这里。

const sidebarPrefsFileName = "sidebar.json"

type sidebarPref struct {
	ID      string `json:"id"`
	Visible bool   `json:"visible"`
}

type sidebarPrefsConfig struct {
	Version int           `json:"version"`
	Items   []sidebarPref `json:"items"`
}

type sidebarPrefsStore struct {
	dir   string
	items []sidebarPref
}

// sidebarNavDef 是一个可自定义的导航入口。Key 与点击分发用的 key 一致：tab
// 用 tabID，内嵌页直接用页面名，所以排序 / 显示改动不需要碰动作分发。
type sidebarNavDef struct {
	ID    string
	Label string
	Icon  *ui.SVG
}

// sidebarNavDefs 按代码默认顺序列出「节点」之前的全部导航入口。通知在标题栏
// 铃铛里，不是侧边栏页面，所以不进这份清单。
func sidebarNavDefs() []sidebarNavDef {
	defs := make([]sidebarNavDef, 0, len(tabSpecs)+2)
	for i, s := range tabSpecs {
		if s.Kind == kindNotification {
			continue
		}
		icon := lucide.LayoutGrid()
		if i < len(tabIcons) && tabIcons[i] != nil {
			icon = tabIcons[i]
		}
		defs = append(defs, sidebarNavDef{ID: tabID(i), Label: s.Label, Icon: icon})
	}
	defs = append(defs,
		sidebarNavDef{ID: "drafts", Label: "草稿箱", Icon: lucide.NotebookPen()},
		sidebarNavDef{ID: "watchlater", Label: "稍后再看", Icon: lucide.Bookmark()},
		sidebarNavDef{ID: "scores", Label: "评分榜", Icon: lucide.Trophy()},
	)
	return defs
}

func defaultSidebarPrefs() []sidebarPref {
	defs := sidebarNavDefs()
	out := make([]sidebarPref, 0, len(defs))
	for _, def := range defs {
		out = append(out, sidebarPref{ID: def.ID, Visible: true})
	}
	return out
}

// normalizeSidebarPrefs 保留文件里的顺序与可见性，补齐新增入口，丢掉未知或
// 重复的 ID，并把缺失项按默认顺序接在后面。损坏数据因此不会让入口消失。
func normalizeSidebarPrefs(items []sidebarPref) []sidebarPref {
	defs := sidebarNavDefs()
	known := make(map[string]bool, len(defs))
	visible := make(map[string]bool, len(defs))
	for _, def := range defs {
		known[def.ID] = true
		visible[def.ID] = true
	}
	order := make([]string, 0, len(defs))
	seen := make(map[string]bool, len(defs))
	for _, item := range items {
		if !known[item.ID] || seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		visible[item.ID] = item.Visible
		order = append(order, item.ID)
	}
	for _, def := range defs {
		if !seen[def.ID] {
			order = append(order, def.ID)
		}
	}
	out := make([]sidebarPref, 0, len(order))
	for _, id := range order {
		out = append(out, sidebarPref{ID: id, Visible: visible[id]})
	}
	return out
}

func newSidebarPrefsStore(dir string) *sidebarPrefsStore {
	if dir == "" {
		dir = configDir()
	}
	s := &sidebarPrefsStore{dir: dir, items: defaultSidebarPrefs()}
	var cfg sidebarPrefsConfig
	if err := readJSONFile(filepath.Join(dir, sidebarPrefsFileName), &cfg); err == nil && cfg.Version == 1 {
		s.items = normalizeSidebarPrefs(cfg.Items)
	}
	return s
}

func (s *sidebarPrefsStore) save() error {
	if s == nil {
		return nil
	}
	s.items = normalizeSidebarPrefs(s.items)
	cfg := sidebarPrefsConfig{Version: 1, Items: s.items}
	if err := writeJSONFile(filepath.Join(s.dir, sidebarPrefsFileName), cfg); err != nil {
		return fmt.Errorf("保存侧边栏设置失败：%w", err)
	}
	return nil
}

// Reset 恢复默认顺序与全部显示。nil 偏好走默认值，不算出错。
func (s *sidebarPrefsStore) Reset() error {
	if s == nil {
		return nil
	}
	s.items = defaultSidebarPrefs()
	return s.save()
}

func (s *sidebarPrefsStore) entries() []sidebarPref {
	if s == nil {
		return defaultSidebarPrefs()
	}
	return s.items
}

func (s *sidebarPrefsStore) visible(id string) bool {
	for _, item := range s.entries() {
		if item.ID == id {
			return item.Visible
		}
	}
	return true
}

// setVisible 只改内存；调用方负责落盘并通知其它窗口。
func (s *sidebarPrefsStore) setVisible(id string, on bool) bool {
	if s == nil {
		return false
	}
	for i := range s.items {
		if s.items[i].ID != id || s.items[i].Visible == on {
			continue
		}
		s.items[i].Visible = on
		return true
	}
	return false
}

// order 返回当前顺序，用来在设置页构建前后比较是否发生了拖动。
func (s *sidebarPrefsStore) order() []string {
	items := s.entries()
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ID)
	}
	return out
}

// setOrder 按给定 ID 重排，未列出的入口保持已有可见性并接在后面。
func (s *sidebarPrefsStore) setOrder(ids []string) {
	if s == nil {
		return
	}
	visible := make(map[string]bool, len(s.items))
	for _, item := range s.items {
		visible[item.ID] = item.Visible
	}
	items := make([]sidebarPref, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		items = append(items, sidebarPref{ID: id, Visible: visible[id]})
	}
	for _, item := range s.items {
		if !seen[item.ID] {
			items = append(items, item)
		}
	}
	s.items = normalizeSidebarPrefs(items)
}

func sidebarNavDefByID(id string) (sidebarNavDef, bool) {
	for _, def := range sidebarNavDefs() {
		if def.ID == id {
			return def, true
		}
	}
	return sidebarNavDef{}, false
}

// sidebarNavItems 把偏好翻译成侧边栏列表：只留可见入口，顺序与设置页一致。
// 已隐藏的页面仍然可以通过命令面板等方式进入，只是不再占导航轨的位置。
func (a *app) sidebarNavItems() []display.ListItem {
	defs := sidebarNavDefs()
	byID := make(map[string]sidebarNavDef, len(defs))
	for _, def := range defs {
		byID[def.ID] = def
	}
	items := make([]display.ListItem, 0, len(defs)+2)
	groupDivider := false
	for _, pref := range a.sidebarPrefs.entries() {
		if !pref.Visible {
			continue
		}
		def, ok := byID[pref.ID]
		if !ok {
			continue
		}
		// 两条分组线跟着入口走：草稿箱 / 稍后再看是一组，评分榜单独一组。
		// 这样自定义排序后仍保留原来的视觉分组，又不占设置页的列表项。
		switch pref.ID {
		case "drafts", "watchlater":
			if !groupDivider && len(items) > 0 {
				items = append(items, display.ListItem{Divider: true})
			}
			groupDivider = true
		case "scores":
			if len(items) > 0 {
				items = append(items, display.ListItem{Divider: true})
			}
		}
		items = append(items, display.ListItem{Key: def.ID, Label: def.Label, Icon: def.Icon})
	}
	return items
}

/* ------------------------------------------------------------------
   设置窗口的「侧边栏」页
--------------------------------------------------------------- */

// sidebarSettingsSection 是设置窗口里自定义侧边栏的一块。列表用 Lobe 的
// SortableList（鼠标拖拽与键盘 Space + 方向键都能排序），每行用 Lobe 的
// Checkbox 控制显示；改动立即落盘并让其它窗口下一帧重建。
func (a *app) sidebarSettingsSection(c *ui.Context) {
	if a.sidebarPrefs == nil {
		a.sidebarPrefs = newSidebarPrefsStore("")
	}
	settingsSection(c, "侧边栏导航", func() {
		settingsDescription(c, "拖动左侧手柄排序，勾选框控制显示。首页到「节点」分组之前的入口都能改；节点快捷入口与底部设置 / 关于入口不在这里。", 2)
		before := a.sidebarPrefs.order()
		visibleChanged := false

		layout.SortableList(c, &a.sidebarPrefs.items, func(p sidebarPref) string {
			return p.ID
		}, layout.SortableListProps{Variant: layout.SortableOutlined}, func(p sidebarPref, handle func()) {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
				handle()
				def, ok := sidebarNavDefByID(p.ID)
				label := p.ID
				if ok {
					label = def.Label
					display.Icon(c, def.Icon, display.IconProps{Size: display.IconSmall})
				}
				display.Text(c, label, display.TextProps{FontSize: 12, Rows: 1}).Grow(1)
				checked := p.Visible
				box := choice.Checkbox(c, &checked, "", choice.CheckboxProps{Size: 14})
				box.Label("显示" + label)
				if checked != p.Visible && a.sidebarPrefs.setVisible(p.ID, checked) {
					visibleChanged = true
				}
			})
		})

		if visibleChanged || !sameSidebarOrder(before, a.sidebarPrefs.order()) {
			if err := a.sidebarPrefs.save(); err != nil {
				a.toast(toastSettings, err.Error())
			}
			a.publish(func() {})
		}
		if action.Button(c, "恢复默认排序与显示", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
			if err := a.sidebarPrefs.Reset(); err != nil {
				a.toast(toastSettings, err.Error())
			} else {
				a.publish(func() {})
			}
		}
	})
}

func sameSidebarOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
