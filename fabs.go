package main

import "github.com/egoist/mygo/ui"

// 右下角的悬浮按钮（FAB）组：主页列表用这一套画法与几何。详情页不再
// 使用浮在内容上的按钮：刷新与返回顶部收进页头，避免压住评论输入框。
//
// 一列按钮贴着内容区右下角，最下面那个恒定不动，其余往上长——新出现的
// 按钮（「返回顶部」）不会把「刷新」「发帖」顶得上下跳。整列绝对定位：
// 不占内容布局的高度，也不随内容滚动。
const (
	fabRight  float32 = 24 // 距内容区右边
	fabBottom float32 = 24 // 距内容区下边
	fabGap    float32 = 10 // 竖排按钮之间的间距
	fabCircle float32 = 40 // 次级按钮（刷新 / 返回顶部）的直径
)

// fabStack 画右下角竖排的一列悬浮按钮。buttons 按从上到下的顺序给出，
// 最后一个贴着右下角；空列表什么也不画。
func (a *app) fabStack(c *ui.Context, buttons ...func(*ui.Context)) {
	if len(buttons) == 0 {
		return
	}
	ui.Column(c).Absolute().Right(fabRight).Bottom(fabBottom).
		Gap(fabGap).AlignItems(ui.End).Children(func() {
		for _, draw := range buttons {
			draw(c)
		}
	})
}

// fabButton 画一个次级 FAB：圆形的纯图标按钮，悬停给提示。label 是它的
// 可访问名（读屏与测试认的名字），tip 是悬停说明。loading 时换成转圈并
// 禁用：请求还在路上，连点不该重复发。
func (a *app) fabButton(c *ui.Context, icon *ui.SVG, label, tip string, loading bool, onClick func()) {
	t := c.Theme()
	b := ui.Box(c).Size(fabCircle, fabCircle).Radius(999).
		Background(t.SurfaceHover).Border(1, t.Border).Center()
	hovered := b.Hovered()
	switch {
	case loading:
		b.Background(t.Surface)
	case hovered:
		b.Background(t.SurfacePressed)
		b.Border(1, t.Accent)
	}
	ink := t.Text
	if hovered && !loading {
		ink = t.Accent
	}
	b.Children(func() {
		if loading {
			ui.Spinner(c).Size(16, 16)
			return
		}
		ui.Icon(c, icon).Size(18, 18).TextColor(ink)
	})
	b.Label(label).Tooltip(tip)
	if loading {
		b.Disabled(true)
		return
	}
	b.OnClick(onClick)
}

// listFabs 是主页内容区右下角的一列悬浮按钮：发帖（主操作，强调色胶囊）、
// 刷新，以及列表滚离顶部之后才出现的「返回顶部」。发帖固定在最下面、离手
// 最近，其余往上排。
func (a *app) listFabs(c *ui.Context) {
	st := a.tabs[a.tab]
	var buttons []func(*ui.Context)
	if listScrolled(st) {
		buttons = append(buttons, func(c *ui.Context) {
			a.fabButton(c, backTopIcon, "返回顶部", "回到列表顶部", false, func() {
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
		a.fabButton(c, refreshIcon, "刷新", tip, loading, a.reloadCurrent)
	})
	buttons = append(buttons, a.composeFab)
	a.fabStack(c, buttons...)
}

// listScrolled 汇报列表是否已经滚离顶部：第一行不在可视区里才算。首屏还
// 没排版时 Visible 返回 (0, -1)，按「没有滚动」处理。
func listScrolled(st *tabState) bool {
	first, _ := st.list.Visible()
	return first > 0
}
