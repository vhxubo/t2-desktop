package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
)

// 首页页头的筛选条：一个显示当前节点的 chip，有筛选时右侧多一个 X。
// chip 自己就是级联浮层的触发框，选择后立即应用；浮层底部的排序切换会
// 持久化为偏好并重载首页。
func (a *app) feedNodeFilter(c *ui.Context, st *tabState) {
	if st.filterOpen {
		// 只有浮层打开时才懒加载节点表：普通首屏不额外发请求。
		a.ensureNodeCatalog()
	}
	sort := 0
	if a.nodePrefs != nil {
		sort = sortIndex(a.nodePrefs.Sort)
	}
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
		nodeCascader(c, a.nodeCatalogNodes(), nodeCascadeProps{
			Label:             "节点筛选",
			Value:             &st.homeFilterNode,
			Open:              &st.filterOpen,
			ActiveRoot:        &st.filterRoot,
			Search:            &st.filterSearch,
			AllowAll:          true,
			SelectableParents: true,
			Chip:              true,
			ShowSort:          true,
			Sort:              &sort,
			OnSortChanged:     a.setNodeSort,
			OnManage:          a.openNodeEditor,
			OnPick: func(f nodeFilter) {
				a.setHomeFilter(f)
			},
		})
		if !st.homeFilterNode.isAll() {
			clear := action.StyleActionIcon(c, ui.ButtonBase(c), lucide.X(), action.ActionIconProps{
				GlyphSize: 12,
			})
			clear.Label("清除节点筛选")
			lobeoverlay.Tooltip(c, clear, "清除节点筛选", lobeoverlay.TooltipProps{})
			if clear.Clicked() {
				a.setHomeFilter(allNodes())
			}
		}
	})
}

// setNodeSort 切换首页排序偏好：写入本机配置后立刻重载当前筛选。
func (a *app) setNodeSort(idx int) {
	if a.nodePrefs == nil {
		a.nodePrefs = &nodeFeedPrefs{Sort: nodeSortValue}
	}
	next := validNodeSort(sortName(idx))
	if a.nodePrefs.Sort == next {
		return
	}
	a.nodePrefs.Sort = next
	a.saveNodeShortcuts()
	if len(a.tabs) > 0 && a.tabs[0] != nil {
		a.tabs[0].loaded = false
		a.tabs[0].items = nil
		a.tabs[0].list.ScrollTo(0, ui.Start)
		a.load(0, 1)
	}
}

// defaultSortIndex 是配置弹窗与筛选条共用的默认值读取。
func defaultSortIndex(p *nodeFeedPrefs) int {
	if p == nil {
		return 0
	}
	return sortIndex(p.Sort)
}
