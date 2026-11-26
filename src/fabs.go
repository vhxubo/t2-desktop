package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 右下角的悬浮按钮（FAB）组：只用在主页列表上，画法与几何见这里。详情页
// 的刷新、返回顶部与关闭走正文区右下角的操作组（见 detailToolBar），收藏则在
// 标题下方的作者行最右侧；它们都不压住评论输入框。
//
// 一列按钮贴着内容区右下角，最下面那个恒定不动，其余往上长——新出现的
// 按钮（「返回顶部」）不会把「刷新」「发帖」顶得上下跳。整列绝对定位：
// 不占内容布局的高度，也不随内容滚动。
const (
	fabRight   float32 = 24 // 距内容区右边
	fabBottom  float32 = 24 // 距内容区下边
	fabGap     float32 = 12 // 竖排按钮之间的间距
	fabCircle  float32 = 38 // 次级按钮（刷新 / 返回顶部 / 发动态）的圆形直径
	fabPrimary float32 = 44 // 主操作（发帖）的圆形直径，比次级的大一圈
)

// fabStack 画右下角竖排的一列悬浮按钮。buttons 按从上到下的顺序给出，
// 最后一个贴着右下角；空列表什么也不画。
//
// Lobe 没有「绝对定位的悬浮按钮列」这种组件，这里仍用 mygo 的 Column 做
// 定位容器；列里的每枚按钮本身都是 Lobe 组件（见 fabButton / composeFab），
// 配色取自 Lobe token，圆角由各按钮按自身尺寸对半取整圆。
func (a *app) fabStack(c *ui.Context, buttons ...func(*ui.Context)) {
	if len(buttons) == 0 {
		return
	}
	ui.Column(c.Key("list-fabs")).Absolute().Right(fabRight).Bottom(fabBottom).
		Gap(fabGap).AlignItems(ui.End).Children(func() {
		for _, draw := range buttons {
			draw(c)
		}
	})
}

// fabButton 画一个 Lobe 次级 FAB：ActionIcon 的 BlockSize 给圆形直径，再按
// 半径对半设圆角做成整圆；图标由调用处传 Lobe 的 lucide 图标（见 listFabs）。
// 用 Outlined 变体取 Lobe 的实色容器底色，再去掉边框，按钮浮在列表上时不会
// 看起来只有一枚图标、透出下面的内容；悬停 / 按下由它自己加深底色。label 是
// 可访问名，tip 是悬停说明；loading 时禁用按钮并在图标位转圈，请求还在路上，
// 连点不该重复发。
func (a *app) fabButton(c *ui.Context, icon *ui.SVG, label, tip string, loading bool, onClick func()) {
	c.Key(label)
	if loading {
		// 请求在路上：按钮退成同样的圆形实底禁用态——尺寸与配色不变，只把
		// 图标位换成转圈。关掉 Disabled 自带的半透明，免得实底又透出列表；
		// 请求还在路上，连点不该重复发。
		tk := lobetheme.TokensOf(c)
		b := ui.ButtonBase(c).Size(fabCircle, fabCircle).Center().Disabled(true).
			DimDisabled(false).Radius(fabCircle / 2).Background(tk.BgContainer).BorderWidth(0)
		b.Label(label).Tooltip(tip)
		b.Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 16})
		})
		return
	}
	b := action.ActionIcon(c, icon, action.ActionIconProps{
		Variant:   action.ActionIconOutlined,
		BlockSize: fabCircle,
	})
	b.Label(label).Tooltip(tip).OnClick(onClick).Radius(fabCircle / 2).BorderWidth(0)
}

// listFabs 是主页内容区右下角的一列悬浮按钮：发帖（主操作，强调色的
// Lobe ActionIcon）、发动态、刷新，以及列表滚离顶部之后才出现的「返回顶部」。
// 发帖固定在最下面，离手最近，其余往上排。评分榜入口已移到侧边栏「动态」下面
// （见 sidebarNavItems），它开的是独立窗口，不适合和这些页面操作混在一起。
func (a *app) listFabs(c *ui.Context) {
	st := a.tabs[a.tab]
	var buttons []func(*ui.Context)
	if listScrolled(st) {
		buttons = append(buttons, func(c *ui.Context) {
			a.fabButton(c, lucide.ArrowUp(), "返回顶部", "回到列表顶部", false, func() {
				st.list.ScrollTo(0, ui.Start)
			})
		})
	}
	loading := st.loading
	buttons = append(buttons, func(c *ui.Context) {
		tip := "重新加载当前列表"
		if loading {
			tip = "正在刷新…"
		}
		a.fabButton(c, lucide.RefreshCw(), "刷新", tip, loading, a.reloadCurrent)
	})
	buttons = append(buttons, func(c *ui.Context) {
		a.fabButton(c, lucide.MessageCircle(), "发动态", "写一条短动态（最多 500 字）", false, a.showMomentCompose)
	})
	buttons = append(buttons, func(c *ui.Context) {
		a.composeFab(c.Key("发帖"))
	})
	a.fabStack(c, buttons...)
}

// listScrolled 汇报列表是否已经滚离顶部：第一行不在可视区里才算。首屏还
// 没排版时 Visible 返回 (0, -1)，按「没有滚动」处理。
func listScrolled(st *tabState) bool {
	first, _ := st.list.Visible()
	return first > 0
}
