package main

import (
	"log"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 首页节点筛选的本机偏好与侧边栏快捷入口。
//
// 快捷入口与筛选偏好是本机共享的；「上次筛选」按账号键落盘，多账号不会
// 互相覆盖。全部落在 localKV 的离线缓存文件里，不新增独立文件，也不发请求。

const (
	nodeShortcutKey    = "node.shortcuts:v1"
	nodeLastFilterKey  = "node.lastfilter:v1:"
	nodeShortcutMax    = 8
	nodeSortValue      = "value"
	nodeSortLatest     = "latest"
	nodeRememberOff    = 0
	nodeRememberOn     = 1
	nodeShortcutKeyTag = "shortcut:"
)

// nodeFeedPrefs 是首页筛选的两个偏好：排序方式与是否在启动时恢复上次筛选。
type nodeFeedPrefs struct {
	Sort           string `json:"sort"`
	RememberFilter bool   `json:"remember_filter"`
}

// nodeShortcutFile 是落盘结构。Version 不认识时按空处理，不阻塞界面。
type nodeShortcutFile struct {
	Version int           `json:"version"`
	Items   []nodeRef     `json:"items"`
	Prefs   nodeFeedPrefs `json:"prefs"`
}

type nodeLastFilterFile struct {
	Filter nodeFilter `json:"filter"`
}

// validNodeSort 把旧数据 / 损坏数据兜回默认的价值优先。
func validNodeSort(s string) string {
	if s == nodeSortLatest {
		return nodeSortLatest
	}
	return nodeSortValue
}

// normalizeNodeRefs 去重、丢掉空引用并截断到 8 个，保存与读取都走这里。
func normalizeNodeRefs(items []nodeRef) []nodeRef {
	out := make([]nodeRef, 0, len(items))
	seen := make(map[string]bool, len(items))
	for _, r := range items {
		key := r.key()
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
		if len(out) >= nodeShortcutMax {
			break
		}
	}
	return out
}

func (a *app) lastFilterKey() string {
	id := a.accountID
	if id == "" {
		id = "anonymous"
	}
	return nodeLastFilterKey + id
}

// loadNodePrefs 在启动时读一次偏好与快捷入口；文件不存在或损坏都按空处理。
func (a *app) loadNodePrefs() {
	a.nodePrefs = &nodeFeedPrefs{Sort: nodeSortValue}
	if a.kv == nil {
		return
	}
	var f nodeShortcutFile
	if _, ok := a.kv.get(nodeShortcutKey, &f); ok && f.Version == 1 {
		a.nodeShortcuts = normalizeNodeRefs(f.Items)
		a.nodePrefs.Sort = validNodeSort(f.Prefs.Sort)
		a.nodePrefs.RememberFilter = f.Prefs.RememberFilter
	}
	if !a.nodePrefs.RememberFilter || len(a.tabs) == 0 || a.tabs[0] == nil {
		return
	}
	var last nodeLastFilterFile
	if _, ok := a.kv.get(a.lastFilterKey(), &last); ok {
		a.nodeLastFilter = last.Filter
		a.tabs[0].homeFilterNode = last.Filter
	}
}

// saveNodeShortcuts 写快捷入口与首选项；写不进去只记日志，不影响界面。
func (a *app) saveNodeShortcuts() {
	if a.kv == nil {
		return
	}
	a.nodeShortcuts = normalizeNodeRefs(a.nodeShortcuts)
	if a.nodePrefs == nil {
		a.nodePrefs = &nodeFeedPrefs{Sort: nodeSortValue}
	}
	f := nodeShortcutFile{Version: 1, Items: a.nodeShortcuts, Prefs: *a.nodePrefs}
	if err := a.kv.put(nodeShortcutKey, f, time.Time{}); err != nil {
		log.Printf("节点快捷入口写入失败：%v", err)
	}
}

// saveLastFilter 只在「启动时恢复上次筛选」打开时记录；关掉后不再写盘，
// 但旧记录留着也无妨——下次打开开关时它仍可用。
func (a *app) saveLastFilter(f nodeFilter) {
	if a.nodePrefs == nil || !a.nodePrefs.RememberFilter || a.kv == nil {
		return
	}
	a.nodeLastFilter = f
	if err := a.kv.put(a.lastFilterKey(), nodeLastFilterFile{Filter: f}, time.Time{}); err != nil {
		log.Printf("节点上次筛选写入失败：%v", err)
	}
}

/* ------------------------------------------------------------------
   侧边栏
--------------------------------------------------------------- */

// sidebarNodeSection 是展开态的「节点」分组：标题右侧的设置图标、快捷入口
// 列表与空态。点击快捷行直接进入「首页 + 该节点筛选」，不新开窗口。
func (a *app) sidebarNodeSection(c *ui.Context, nav layout.SideNavState) {
	ui.Box(c).Height(1).Margin(8, 8).AlignSelf(ui.Stretch).Background(c.Theme().Border)
	tk := lobetheme.TokensOf(c)
	ui.Row(c).FillWidth().Padding(0, sidebarInsetX, 2, sidebarInsetX).
		AlignItems(ui.Center).Children(func() {
		ui.Text(c, "节点").FontSize(11).TextColor(tk.TextDescription).Grow(1)
		setting := action.StyleActionIcon(c, ui.ButtonBase(c), lucide.Settings2(), action.ActionIconProps{
			GlyphSize: 13,
		})
		setting.Label("配置节点快捷入口")
		lobeoverlay.Tooltip(c, setting, "配置节点快捷入口", lobeoverlay.TooltipProps{Placement: lobeoverlay.Top})
		if setting.Clicked() {
			a.openNodeEditor()
		}
	})
	if len(a.nodeShortcuts) == 0 {
		if action.Button(c, "添加节点快捷入口", action.ButtonProps{
			Variant: action.Text, Size: lobetheme.Small,
		}).Clicked() {
			a.openNodeEditor()
		}
		return
	}
	items := make([]display.ListItem, 0, len(a.nodeShortcuts))
	activeKey := a.activeNodeShortcutKey()
	for _, r := range a.nodeShortcuts {
		items = append(items, display.ListItem{
			Key: nodeShortcutKeyTag + r.key(), Label: r.label(), Icon: nodeRefIcon(r),
		})
	}
	clicked := display.List(c, display.ListProps{Items: items, Active: activeKey, Compact: true})
	if clicked != "" {
		for _, r := range a.nodeShortcuts {
			if nodeShortcutKeyTag+r.key() != clicked {
				continue
			}
			a.showNodeFeed(r)
			break
		}
	}
}

// sidebarNodeRail 是折叠态的单个 Hash 入口。菜单列出快捷入口，末项是配置
// 弹窗；当前筛选命中任一项时图标保持激活态。
func (a *app) sidebarNodeRail(c *ui.Context) {
	ui.Box(c).Height(1).Margin(4, 8).AlignSelf(ui.Stretch).Background(c.Theme().Border)
	active := a.activeNodeShortcutKey() != ""
	items := make([]lobeoverlay.MenuItem, 0, len(a.nodeShortcuts)+2)
	for _, r := range a.nodeShortcuts {
		items = append(items, lobeoverlay.MenuItem{
			Key: nodeShortcutKeyTag + r.key(), Label: r.label(), Icon: nodeRefIcon(r),
		})
	}
	items = append(items, lobeoverlay.MenuItem{Divider: true})
	items = append(items, lobeoverlay.MenuItem{Key: "configure", Label: "配置节点…", Icon: lucide.Settings2()})
	chosen := lobeoverlay.DropdownMenu(c, &a.nodeRailOpen, lobeoverlay.DropdownMenuProps{Items: items}, func(t ui.Element) {
		// DropdownMenu 的触发按钮默认靠左，和其余图标轨项目不在同一条中线；
		// 对齐父 Column 的中线，折叠态节点入口才不会贴着侧边栏左缘。
		t.AlignSelf(ui.Center)
		action.StyleActionIcon(c, t, lucide.Hash(), action.ActionIconProps{
			BlockSize: 32, GlyphSize: 16, Active: active, Title: "节点快捷入口",
		})
	})
	if chosen == "configure" {
		a.openNodeEditor()
		return
	}
	if chosen != "" {
		for _, r := range a.nodeShortcuts {
			if nodeShortcutKeyTag+r.key() == chosen {
				a.showNodeFeed(r)
				break
			}
		}
	}
}

func nodeRefIcon(r nodeRef) *ui.SVG {
	if r.ID == "" && r.ParentSlug == "" {
		return lucide.FolderTree()
	}
	return lucide.Hash()
}

func shortcutFilter(r nodeRef) nodeFilter {
	if r.ID == "" && r.ParentSlug == "" {
		return parentFilter(r)
	}
	return nodeFilterOf(r)
}

// activeNodeShortcutKey 是当前筛选命中的快捷入口 key，没命中时为空。
// 展开列表与折叠图标轨共用这一份：节点筛选一清，自定义节点高亮同时熄灭。
func (a *app) activeNodeShortcutKey() string {
	for _, r := range a.nodeShortcuts {
		if shortcutFilter(r).sameAs(a.homeFilter()) {
			return nodeShortcutKeyTag + r.key()
		}
	}
	return ""
}

// homeFilter 是首页当前筛选；没进首页时当作「全部」。
func (a *app) homeFilter() nodeFilter {
	if len(a.tabs) > 0 && a.tabs[0] != nil {
		return a.tabs[0].homeFilterNode
	}
	return allNodes()
}

/* ------------------------------------------------------------------
   配置弹窗
--------------------------------------------------------------- */

// nodeEditor 是配置弹窗的草稿。保存前所有增删排序只改 draft，取消即丢弃。
type nodeEditor struct {
	open       bool
	draft      []nodeRef
	search     string
	pick       nodeFilter
	pickOpen   bool
	activeRoot string
	sort       int
	remember   int
}

// openNodeEditor 从任何入口打开节点配置弹窗：先把持久值复制到草稿，再拉起
// 主窗口。工作统一回到主线程，避免从别的窗口直接改 UI 状态。
func (a *app) openNodeEditor() {
	a.publish(a.openNodeEditorOnMain)
}

func (a *app) openNodeEditorOnMain() {
	a.nodeEditor = nodeEditor{
		open:     true,
		draft:    append([]nodeRef(nil), a.nodeShortcuts...),
		pick:     allNodes(),
		sort:     sortIndex(a.nodePrefs.Sort),
		remember: nodeRememberOff,
	}
	if a.nodePrefs == nil || !a.nodePrefs.RememberFilter {
		a.nodeEditor.remember = nodeRememberOff
	} else {
		a.nodeEditor.remember = nodeRememberOn
	}
	a.ensureNodeCatalog()
	if a.win != nil {
		a.raiseMainWindow()
		a.win.Invalidate()
	}
}

func sortIndex(sort string) int {
	if validNodeSort(sort) == nodeSortLatest {
		return 1
	}
	return 0
}

func sortName(i int) string {
	if i == 1 {
		return nodeSortLatest
	}
	return nodeSortValue
}

// saveNodeEditor 把草稿写盘并刷新侧边栏。取消 / 关闭弹窗不会走到这里。
func (a *app) saveNodeEditor() {
	st := &a.nodeEditor
	a.nodeShortcuts = normalizeNodeRefs(st.draft)
	if a.nodePrefs == nil {
		a.nodePrefs = &nodeFeedPrefs{}
	}
	a.nodePrefs.Sort = validNodeSort(sortName(st.sort))
	a.nodePrefs.RememberFilter = st.remember == nodeRememberOn
	if !a.nodePrefs.RememberFilter {
		// 关掉「恢复上次筛选」时把首页放回全部，避免界面还留着上次的条件。
		if len(a.tabs) > 0 && a.tabs[0] != nil {
			a.tabs[0].homeFilterNode = allNodes()
		}
	}
	a.saveNodeShortcuts()
	if a.nodePrefs.RememberFilter && len(a.tabs) > 0 && a.tabs[0] != nil {
		a.saveLastFilter(a.tabs[0].homeFilterNode)
	}
	st.open = false
}

// nodeEditorDialog 挂在 main.go 的 dialogs 里。Modal 的取消、关闭与 Esc 都会
// 把 open 置回 false；只有 OK 返回 true 时才写盘。
func (a *app) nodeEditorDialog(c *ui.Context) {
	st := &a.nodeEditor
	if !st.open {
		return
	}
	ok := feedback.Modal(c, &st.open, feedback.ModalProps{
		Title: "节点快捷入口", Width: 560,
		OkText: "保存", CancelText: "取消",
	}, func() {
		a.nodeEditorContent(c, st)
	})
	if ok {
		a.saveNodeEditor()
	}
}

func (a *app) nodeEditorContent(c *ui.Context, st *nodeEditor) {
	display.Text(c, "侧边栏显示这些节点，点击会把首页筛到该节点。最多 8 个。",
		display.TextProps{Type: display.TextSecondary, FontSize: 12, Rows: 2})
	ui.Column(c).FillWidth().Gap(10).Children(func() {
		if len(st.draft) == 0 {
			display.Empty(c, display.EmptyProps{
				Title: "还没有快捷入口", Description: "从下面的节点选择里添加一个节点。",
			})
		} else {
			layout.SortableList(c, &st.draft, func(r nodeRef) string { return r.key() },
				layout.SortableListProps{}, func(r nodeRef, handle func()) {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
						handle()
						ui.Icon(c, nodeRefIcon(r)).FontSize(14)
						ui.Text(c, r.label()).FontSize(12).Grow(1).SingleLine()
						x := action.StyleActionIcon(c, ui.ButtonBase(c), lucide.X(), action.ActionIconProps{
							GlyphSize: 13,
						})
						x.Label("移除 " + r.label())
						if x.Clicked() {
							a.removeNodeShortcut(st, r)
						}
					})
				})
		}

		ui.Box(c).Height(1).FillWidth().Background(c.Theme().Border)
		display.Text(c, "添加节点", display.TextProps{Type: display.TextSecondary, FontSize: 12, Rows: 1})
		a.ensureNodeCatalog()
		if a.nodeCat.err != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "节点列表读取失败："+a.nodeCat.err).FontSize(12).
					TextColor(c.Theme().Danger).Grow(1).MaxLines(2)
				if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
					a.retryNodeCatalog()
				}
			})
		} else if len(a.nodeCatalogNodes()) == 0 {
			ui.Text(c, "正在读取节点…").FontSize(12).TextColor(c.Theme().TextMuted)
		} else {
			nodeCascader(c, a.nodeCatalogNodes(), nodeCascadeProps{
				Label: "选择节点", Value: &st.pick, Open: &st.pickOpen,
				ActiveRoot: &st.activeRoot, Search: &st.search,
				KeepOpenOnPick: true,
				OnPick: func(f nodeFilter) {
					a.addNodeShortcut(st, f.Ref)
					st.pick = allNodes()
				},
			})
		}

		ui.Box(c).Height(1).FillWidth().Background(c.Theme().Border)
		display.Text(c, "筛选设置", display.TextProps{Type: display.TextSecondary, FontSize: 12, Rows: 1})
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Text(c, "排序方式").FontSize(12).Grow(1)
			sortSel := st.sort
			action.Segmented(c, &sortSel, []string{"价值优先", "最新"}, action.SegmentedProps{Size: lobetheme.Small})
			st.sort = sortSel
		})
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Text(c, "启动时").FontSize(12).Grow(1)
			remember := st.remember
			action.Segmented(c, &remember, []string{"显示全部", "恢复上次筛选"}, action.SegmentedProps{Size: lobetheme.Small})
			st.remember = remember
		})
	})
}

func (a *app) addNodeShortcut(st *nodeEditor, r nodeRef) {
	for _, existing := range st.draft {
		if existing.equal(r) {
			a.toast(toastMain, "这个节点已经在快捷入口里了")
			return
		}
	}
	if len(st.draft) >= nodeShortcutMax {
		a.toast(toastMain, "最多添加 8 个快捷入口")
		return
	}
	st.draft = append(st.draft, r)
}

func (a *app) removeNodeShortcut(st *nodeEditor, r nodeRef) {
	out := st.draft[:0]
	for _, item := range st.draft {
		if !item.equal(r) {
			out = append(out, item)
		}
	}
	st.draft = out
}
