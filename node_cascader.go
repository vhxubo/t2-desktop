package main

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 节点选择器：Lobe Popover + 两列 display.List，顶部是搜索框。
//
// 空查询走两列：左列是「全部节点 / 父分类」，右列是「整个 X / 子节点」。
// 非空查询换成单列扁平结果，父分类按 parent_slug 筛、子节点按 node_id 筛。
// 发帖窗口把 AllowAll 与 SelectableParents 关掉，结果里只出子节点；配置弹窗
// 用 KeepOpenOnPick，方便连续添加。

// nodeCascadeProps 是一个选择器实例的全部状态。指针字段由调用方长期持有，
// 保证我的输入、展开与高亮能跨帧保留。
type nodeCascadeProps struct {
	Label      string
	Value      *nodeFilter
	Open       *bool
	ActiveRoot *string
	Search     *string
	// AllowAll 显示「全部节点」；SelectableParents 允许父分类本身被选中。
	AllowAll          bool
	SelectableParents bool
	// KeepOpenOnPick 选中后只清空搜索，不收起浮层（配置弹窗连续添加用）。
	KeepOpenOnPick bool
	// AllowClear / ClearLabel 让发帖窗口能把节点清空，交回站点 AI 判定。
	AllowClear bool
	ClearLabel string
	EmptyLabel string
	// Chip 用首页筛选条上的小胶囊外观；否则是发帖窗口里的普通按钮。
	Chip bool
	// ShowSort / Sort / OnSortChanged 只在首页筛选浮层里出现。
	ShowSort      bool
	Sort          *int
	OnSortChanged func(int)
	OnManage      func()
	OnPick        func(nodeFilter)
	OnClear       func()
}

// nodeCascader 画一个节点选择器。nodes 是当前可见的节点表；组件只负责画，
// 搜索由 nodeSearch 计算。
func nodeCascader(c *ui.Context, nodes []composeNode, p nodeCascadeProps) {
	if p.Value == nil || p.Open == nil {
		return
	}
	label := nodeFilterLabel(*p.Value)
	if p.Value.Kind == nodeFilterNode && p.Value.Ref.ID == "" && p.EmptyLabel != "" {
		label = p.EmptyLabel
	}
	fg := c.Theme().Text
	if p.Value.Kind == nodeFilterNode && p.Value.Ref.ID == "" {
		fg = c.Theme().TextMuted
	}
	trigger := action.StyleButton(c, ui.ButtonBase(c), "", action.ButtonProps{
		Variant: action.Filled,
		Size:    lobetheme.Small,
	})
	if !p.Chip {
		trigger = action.StyleButton(c, ui.ButtonBase(c), "", action.ButtonProps{
			Variant: action.Filled,
		})
	}
	trigger.Label(p.Label + "：" + label).TextColor(fg)
	trigger.Children(func() {
		ui.Icon(c, lucide.Hash()).FontSize(13).TextColor(fg)
		ui.Text(c, label).SingleLine().TextColor(fg)
		ui.Icon(c, lucide.ChevronDown()).FontSize(12).TextColor(fg)
	})

	apply := func(f nodeFilter) {
		*p.Value = f
		if p.Search != nil {
			*p.Search = ""
		}
		if p.OnPick != nil {
			p.OnPick(f)
		}
		if !p.KeepOpenOnPick {
			*p.Open = false
		}
	}
	clear := func() {
		if p.EmptyLabel != "" {
			*p.Value = nodeFilterOf(nodeRef{})
		} else {
			*p.Value = allNodes()
		}
		if p.Search != nil {
			*p.Search = ""
		}
		if p.OnClear != nil {
			p.OnClear()
		}
		*p.Open = false
	}

	lobeoverlay.Popover(c, trigger, p.Open, lobeoverlay.PopoverProps{
		Placement: lobeoverlay.Bottom,
	}, func() {
		ui.Column(c).Width(420).Gap(8).Children(func() {
			var input ui.Element
			input = field.SearchBar(c, searchPtr(p.Search), field.SearchBarProps{
				Placeholder: "搜索节点",
				Size:        lobetheme.Small,
				Variant:     field.InputFilled,
				OnSearch: func(string) {
					if input.Composing() {
						return // 输入法组合中的回车是确认候选词，不是选中节点
					}
					hits := searchHitsForPicker(nodes, searchValue(p.Search), p.SelectableParents)
					if len(hits) > 0 {
						apply(hitFilter(hits[0]))
					}
				},
			})
			input.Label("搜索节点")
			input.OnShortcut(0, ui.KeyEscape, func() {
				if strings.TrimSpace(searchValue(p.Search)) != "" {
					setSearch(p.Search, "")
					return
				}
				*p.Open = false
			})

			q := searchValue(p.Search)
			if strings.TrimSpace(q) != "" {
				nodeCascadeResults(c, nodes, q, p, apply)
			} else {
				nodeCascadeColumns(c, nodes, p, apply, clear)
			}

			if p.ShowSort || p.OnManage != nil {
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					if p.ShowSort && p.Sort != nil {
						ui.Text(c, "排序").FontSize(11).TextColor(c.Theme().TextMuted)
						sel := *p.Sort
						action.Segmented(c, &sel, []string{"价值", "最新"}, action.SegmentedProps{
							Size: lobetheme.Small,
						})
						if sel != *p.Sort {
							*p.Sort = sel
							if p.OnSortChanged != nil {
								p.OnSortChanged(sel)
							}
						}
					}
					ui.Spacer(c).Grow(1)
					if p.OnManage != nil {
						if action.Button(c, "管理快捷入口", action.ButtonProps{
							Variant: action.Text, Size: lobetheme.Small,
						}).Clicked() {
							p.OnManage()
						}
					}
				})
			}
		})
	})
}

// nodeCascadeColumns 画空查询时的两列级联。左列只展开右列；叶子分类直接
// 应用父分类筛选。选中项右侧挂一个 Check 图标。
func nodeCascadeColumns(c *ui.Context, nodes []composeNode, p nodeCascadeProps, apply func(nodeFilter), clear func()) {
	filters := make(map[string]nodeFilter)
	left := make([]display.ListItem, 0, len(nodes)+2)
	if p.AllowAll {
		filters["all"] = allNodes()
		left = append(left, display.ListItem{Key: "all", Label: "全部节点", Icon: lucide.Hash(),
			Actions: checkAction(c, p.Value.Kind == nodeFilterAll), ShowAction: p.Value.Kind == nodeFilterAll})
	}
	if p.AllowClear && p.ClearLabel != "" {
		filters["clear"] = allNodes()
		left = append(left, display.ListItem{Key: "clear", Label: p.ClearLabel, Icon: lucide.X()})
	}
	parents := make([]composeNode, 0, len(nodes))
	for _, n := range nodes {
		if n.ParentSlug == "" {
			parents = append(parents, n)
		}
	}
	active := ""
	if p.ActiveRoot != nil {
		active = *p.ActiveRoot
	}
	activeFound := false
	for _, n := range parents {
		if n.Slug == active {
			activeFound = true
		}
	}
	if !activeFound && len(parents) > 0 {
		active = parents[0].Slug
	}
	for _, n := range parents {
		key := "parent:" + n.Slug
		hasChildren := false
		for _, ch := range nodes {
			if ch.ParentSlug == n.Slug {
				hasChildren = true
				break
			}
		}
		selected := p.Value.Kind == nodeFilterParent && p.Value.Ref.Slug == n.Slug
		if !p.SelectableParents && p.Value.Kind == nodeFilterNode && p.Value.Ref.ID == n.ID {
			selected = true
		}
		// 发帖窗口不能发到父分类：没有子节点的父节点本身就是可发帖节点，
		// 直接按 node_id 选中；首页仍按父分类筛。
		if !hasNodeChildren(nodes, n.Slug) && !p.SelectableParents {
			filters[key] = nodeFilterOf(nodeRefFromNode(n))
		} else {
			filters[key] = parentRefFilter(n)
		}
		left = append(left, display.ListItem{
			Key: key, Label: n.nameDisplay(), Icon: lucide.FolderTree(),
			Actions: checkAction(c, selected), ShowAction: selected,
		})
		_ = hasChildren
	}
	right := make([]display.ListItem, 0, len(nodes))
	activeName := active
	for _, n := range parents {
		if n.Slug == active {
			activeName = n.Name
		}
	}
	if active != "" && p.SelectableParents {
		key := "whole:" + active
		filters[key] = parentFilter(nodeRef{Slug: active, Name: activeName})
		selected := p.Value.Kind == nodeFilterParent && p.Value.Ref.Slug == active
		right = append(right, display.ListItem{
			Key: key, Label: "整个「" + activeName + "」", Icon: lucide.FolderTree(),
			Actions: checkAction(c, selected), ShowAction: selected,
		})
	}
	for _, n := range nodes {
		if n.ParentSlug != active || n.ID == "" {
			continue
		}
		key := "node:" + n.ID
		filters[key] = nodeFilterOf(nodeRefFromNode(n))
		selected := p.Value.Kind == nodeFilterNode && p.Value.Ref.ID == n.ID
		right = append(right, display.ListItem{
			Key: key, Label: n.nameDisplay(), Icon: lucide.Hash(),
			Extra:   nodePostCount(n.PostCount),
			Actions: checkAction(c, selected), ShowAction: selected,
		})
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
		ui.Scroll(c).Width(168).MaxHeight(260).Children(func() {
			if key := display.List(c, display.ListProps{Items: left, Compact: true}); key != "" {
				if key == "clear" {
					clear()
					return
				}
				if strings.HasPrefix(key, "parent:") {
					slug := strings.TrimPrefix(key, "parent:")
					if p.ActiveRoot != nil {
						*p.ActiveRoot = slug
					}
					// 没有子节点的父分类是叶子：直接应用父分类筛选。
					if !hasNodeChildren(nodes, slug) {
						apply(filters[key])
					}
					return
				}
				apply(filters[key])
			}
		})
		ui.Box(c).Width(1).AlignSelf(ui.Stretch).Background(c.Theme().Border)
		ui.Scroll(c).Grow(1).MinWidth(220).MaxHeight(260).Children(func() {
			if key := display.List(c, display.ListProps{Items: right, Compact: true}); key != "" {
				apply(filters[key])
			}
		})
	})
}

// nodeCascadeResults 画搜索态的单列结果。命中超过上限时给一行提示；
// 没有命中时不画空列表，只留一句弱化文字。
func nodeCascadeResults(c *ui.Context, nodes []composeNode, q string, p nodeCascadeProps, apply func(nodeFilter)) {
	hits := searchHitsForPicker(nodes, q, p.SelectableParents)
	if len(hits) == 0 {
		ui.Text(c, "没有匹配的节点").FontSize(12).TextColor(c.Theme().TextMuted).Padding(8, 4)
		return
	}
	items := make([]display.ListItem, 0, len(hits))
	filters := make(map[string]nodeFilter, len(hits))
	for i, h := range hits {
		key := "hit:" + strconv.Itoa(i)
		filters[key] = hitFilter(h)
		item := display.ListItem{
			Key: key, Label: h.Ref.label(), Icon: lucide.Hash(),
			Extra: nodePostCount(h.PostCount),
		}
		if h.IsParent {
			item.Icon = lucide.FolderTree()
			item.Description = "分类"
		} else if h.Ref.ParentName != "" {
			item.Description = h.Ref.ParentName
		}
		items = append(items, item)
	}
	ui.Scroll(c).FillWidth().MaxHeight(260).Children(func() {
		if key := display.List(c, display.ListProps{Items: items, Compact: true}); key != "" {
			apply(filters[key])
		}
		if len(nodeSearch(nodes, q)) >= nodeSearchLimit {
			ui.Text(c, "结果较多，继续输入以缩小范围").FontSize(11).
				TextColor(c.Theme().TextMuted).Padding(6, 4)
		}
	})
}

func hitFilter(h nodeHit) nodeFilter {
	if h.IsParent {
		return parentFilter(h.Ref)
	}
	return nodeFilterOf(h.Ref)
}

func parentRefFilter(n composeNode) nodeFilter {
	return parentFilter(nodeRef{Slug: n.Slug, Name: n.Name})
}

func hasNodeChildren(nodes []composeNode, slug string) bool {
	for _, n := range nodes {
		if n.ParentSlug == slug {
			return true
		}
	}
	return false
}

// checkAction 是选中项右侧的 Check。display.List 的 Actions 默认只在悬停时
// 出现，选中行把 ShowAction 打开，让当前项一眼可辨。
func checkAction(c *ui.Context, on bool) func() {
	if !on {
		return nil
	}
	return func() { ui.Icon(c, lucide.Check()).FontSize(13) }
}

func searchPtr(s *string) *string {
	if s == nil {
		return new(string)
	}
	return s
}

func searchValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func setSearch(s *string, v string) {
	if s != nil {
		*s = v
	}
}
