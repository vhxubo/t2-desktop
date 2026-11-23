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

// 首页节点筛选的侧边栏快捷入口。
//
// 每个快捷入口自带排序偏好（价值优先 / 最新），随快捷入口一起落盘在 localKV
// 的离线缓存文件里；不新增独立文件，也不发请求。启动时首页总是默认筛选，
// 不恢复上次的节点。

const (
	nodeShortcutKey    = "node.shortcuts:v1"
	nodeShortcutMax    = 8
	nodeSortValue      = "value"
	nodeSortLatest     = "latest"
	nodeShortcutKeyTag = "shortcut:"
)

// nodeShortcutFile 是落盘结构。Version 不认识时按空处理，不阻塞界面。
// 老版本写入的 prefs 会被忽略：排序已归到每个快捷入口自己身上。
type nodeShortcutFile struct {
	Version int       `json:"version"`
	Items   []nodeRef `json:"items"`
}

// validNodeSort 把旧数据 / 损坏数据兜回默认的价值优先。
func validNodeSort(s string) string {
	if s == nodeSortLatest {
		return nodeSortLatest
	}
	return nodeSortValue
}

// nodeSortLabel 是快捷入口上显示的当前排序。
func nodeSortLabel(s string) string {
	if validNodeSort(s) == nodeSortLatest {
		return "最新"
	}
	return "价值"
}

// toggleNodeSort 在价值优先与最新之间切换。
func toggleNodeSort(s string) string {
	if validNodeSort(s) == nodeSortLatest {
		return nodeSortValue
	}
	return nodeSortLatest
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
		r.Sort = validNodeSort(r.Sort)
		out = append(out, r)
		if len(out) >= nodeShortcutMax {
			break
		}
	}
	return out
}

// loadNodePrefs 在启动时读一次快捷入口；文件不存在或损坏都按空处理。
func (a *app) loadNodePrefs() {
	if a.kv == nil {
		return
	}
	var f nodeShortcutFile
	if _, ok := a.kv.get(nodeShortcutKey, &f); ok && f.Version == 1 {
		a.nodeShortcuts = normalizeNodeRefs(f.Items)
	}
}

// saveNodeShortcuts 写快捷入口；写不进去只记日志，不影响界面。
func (a *app) saveNodeShortcuts() {
	if a.kv == nil {
		return
	}
	a.nodeShortcuts = normalizeNodeRefs(a.nodeShortcuts)
	f := nodeShortcutFile{Version: 1, Items: a.nodeShortcuts}
	if err := a.kv.put(nodeShortcutKey, f, time.Time{}); err != nil {
		log.Printf("节点快捷入口写入失败：%v", err)
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
			a.showNodeShortcut(r)
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
				a.showNodeShortcut(r)
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

// showNodeShortcut 是侧边栏快捷入口的点击动作。快捷入口落盘时只存了 nodeRef，
// 父 / 子的区别要靠 shortcutFilter 还原：父分类发 parent_slug，子节点发 node_id。
// 不能套用 showNodeFeed 里「解析后有没有 ID」的判断——父分类也能从节点表补到
// ID，那会把它错当成子节点，请求参数变成 node_id，侧边栏高亮也跟着熄灭。
func (a *app) showNodeShortcut(ref nodeRef) {
	if ref.ID == "" && ref.Slug == "" {
		return
	}
	f := shortcutFilter(ref)
	// 只补全展示用的名字 / 父名，筛选类型（父 vs 子）保持原样；resolveNodeRef
	// 补到的 ID 不参与父分类的 query，也不影响与高亮筛选的 sameAs 比较。
	f.Ref = a.resolveNodeRef(f.Ref)
	a.applyNodeFilter(f)
}

// activeNodeShortcutKey 是当前筛选命中的快捷入口 key，没命中时为空。
// 展开列表与折叠图标轨共用这一份：节点筛选一清，自定义节点高亮同时熄灭。
// 切到别的浏览页或草稿 / 稍后再看 / 评分榜页时，首页虽然还挂着筛选，高亮也
// 要跟着熄灭——它只代表「当前正在看这个节点的首页」，不能留在侧边栏上冒充
// 当前选中项。筛选本身不在这里清，点主页时由 selectTab 统一清掉。
func (a *app) activeNodeShortcutKey() string {
	if a.tab != 0 || a.pageName != "list" {
		return ""
	}
	for _, r := range a.nodeShortcuts {
		if shortcutFilter(r).sameAs(a.homeFilter()) {
			return nodeShortcutKeyTag + r.key()
		}
	}
	return ""
}

// homeFilter 是首页 tab 当前挂着的筛选。这里不能按当前页面截断：selectTab
// 点主页时要读它决定是否清筛选，而那时 a.tab / a.pageName 可能还停在别的页。
// 是否亮自定义节点由 activeNodeShortcutKey 另行判断当前是否停在首页列表。
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
	pickerOpen bool // 二级添加面板（Drawer）
	activeRoot string
}

// openNodeEditor 从任何入口打开节点配置弹窗：先把持久值复制到草稿，再拉起
// 主窗口。工作统一回到主线程，避免从别的窗口直接改 UI 状态。
func (a *app) openNodeEditor() {
	a.publish(a.openNodeEditorOnMain)
}

func (a *app) openNodeEditorOnMain() {
	a.nodeEditor = nodeEditor{
		open:  true,
		draft: append([]nodeRef(nil), a.nodeShortcuts...),
		pick:  allNodes(),
	}
	a.ensureNodeCatalog()
	if a.win != nil {
		a.raiseMainWindow()
		a.win.Invalidate()
	}
}

// saveNodeEditor 把草稿写盘并刷新侧边栏。取消 / 关闭弹窗不会走到这里。
func (a *app) saveNodeEditor() {
	st := &a.nodeEditor
	a.nodeShortcuts = normalizeNodeRefs(st.draft)
	a.saveNodeShortcuts()
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
	// 二级面板画在弹窗之后：添加节点的 Drawer 覆盖在配置弹窗之上，关掉它
	// 仍回到弹窗。弹窗被取消 / 保存时一并把二级状态清掉。
	if !st.open {
		st.pickerOpen = false
		return
	}
	a.nodeEditorPicker(c, st)
}

func (a *app) nodeEditorContent(c *ui.Context, st *nodeEditor) {
	display.Text(c, "侧边栏显示这些节点，点击会把首页筛到该节点。最多 8 个。行尾可单独切换该节点的排序。",
		display.TextProps{Type: display.TextSecondary, FontSize: 12, Rows: 2})
	ui.Column(c).FillWidth().Gap(10).Children(func() {
		if len(st.draft) == 0 {
			display.Empty(c, display.EmptyProps{
				Title: "还没有快捷入口", Description: "点「添加节点」从节点目录里挑选。",
			})
		} else {
			layout.SortableList(c, &st.draft, func(r nodeRef) string { return r.key() },
				layout.SortableListProps{}, func(r nodeRef, handle func()) {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
						handle()
						ui.Icon(c, nodeRefIcon(r)).FontSize(14)
						ui.Text(c, r.label()).FontSize(12).Grow(1).SingleLine()
						// 排序跟着节点走：点一下在当前节点的价值 / 最新之间切换。
						sortBtn := action.StyleButton(c, ui.ButtonBase(c), "", action.ButtonProps{
							Variant: action.Text, Size: lobetheme.Small,
						})
						sortBtn.Label("切换排序：" + r.label())
						sortBtn.Children(func() {
							ui.Text(c, nodeSortLabel(r.Sort)).FontSize(11)
						})
						if sortBtn.Clicked() {
							a.toggleShortcutSort(st, r)
						}
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

		// 弹窗里只放添加入口：节点表放在二级 Drawer，排序在每个节点行上。
		ui.Box(c).Height(1).FillWidth().Background(c.Theme().Border)
		add := action.StyleButton(c, ui.ButtonBase(c), "", action.ButtonProps{Variant: action.Filled})
		add.FillWidth()
		add.Children(func() {
			ui.Icon(c, lucide.Plus()).FontSize(14)
			ui.Text(c, "添加节点").FontSize(13).Grow(1)
			ui.Icon(c, lucide.ChevronRight()).FontSize(13).TextColor(c.Theme().TextMuted)
		})
		if add.Clicked() {
			st.search = ""
			st.pickerOpen = true
			a.ensureNodeCatalog()
		}
	})
}

// toggleShortcutSort 切换某一项快捷入口自己的排序，不动其它节点。
func (a *app) toggleShortcutSort(st *nodeEditor, r nodeRef) {
	for i := range st.draft {
		if st.draft[i].key() == r.key() {
			st.draft[i].Sort = toggleNodeSort(st.draft[i].Sort)
			return
		}
	}
}

// nodeEditorPicker 是「添加节点」的二级面板：右侧 Drawer 从窗口边缘滑入，
// 选中节点即写入草稿并关闭面板，回到配置弹窗。弹窗里的表单只留入口。
func (a *app) nodeEditorPicker(c *ui.Context, st *nodeEditor) {
	feedback.Drawer(c, &st.pickerOpen, feedback.DrawerProps{
		Placement: feedback.Right, Title: "添加节点", Size: 460,
	}, func() {
		a.ensureNodeCatalog()
		if a.nodeCat.err != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "节点列表读取失败："+a.nodeCat.err).FontSize(12).
					TextColor(c.Theme().Danger).Grow(1).MaxLines(2)
				if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
					a.retryNodeCatalog()
				}
			})
			return
		}
		if len(a.nodeCatalogNodes()) == 0 {
			ui.Text(c, "正在读取节点…").FontSize(12).TextColor(c.Theme().TextMuted)
			return
		}
		// Drawer 里按窗口高度铺满列表：抽屉头部、内边距与搜索框约留 120。
		listH := c.Root().LayoutBounds().H - 120
		if listH < 260 {
			listH = 260
		}
		nodeCascadePicker(c, a.nodeCatalogNodes(), nodeCascadeProps{
			Value: &st.pick, Search: &st.search, ActiveRoot: &st.activeRoot,
			SelectableParents: true, ListHeight: listH,
		}, func(f nodeFilter) {
			a.addNodeShortcut(st, f.Ref)
			st.search = ""
			st.pick = allNodes()
			st.pickerOpen = false
		}, func() {}, 0, func() { st.pickerOpen = false })
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
