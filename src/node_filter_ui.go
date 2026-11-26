package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
)

// 首页页头的筛选条：一个显示当前节点的 chip，有筛选时右侧多一个 X。
// chip 自己就是级联浮层的触发框，选择后立即应用；排序由节点快捷入口自己
// 决定，浮层里不再放全局排序开关。
func (a *app) feedNodeFilter(c *ui.Context, st *tabState) {
	if st.filterOpen {
		// 只有浮层打开时才懒加载节点表：普通首屏不额外发请求。
		a.ensureNodeCatalog()
	}
	ui.Row(c).AlignItems(ui.Center).Gap(6).Shrink(0).Children(func() {
		nodeCascader(c, a.nodeCatalogNodes(), nodeCascadeProps{
			Label:             "节点筛选",
			Value:             &st.homeFilterNode,
			Open:              &st.filterOpen,
			ActiveRoot:        &st.filterRoot,
			Search:            &st.filterSearch,
			AllowAll:          true,
			SelectableParents: true,
			Chip:              true,
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
