package main

import "github.com/egoist/mygo/ui"

// 两列级联选择（Cascader）。
//
// 第一列是顶层分类，第二列是当前分类下的可选项；点第二列的一项才算选中，
// 收起后触发框显示「分类 · 节点」。第一列里没有子项的分类本身就是叶子，
// 点它直接选中并收起。
//
// 站点节点是「父节点 / 子节点」两层，父节点只是分类，发帖要的 node_id 是
// 子节点的 uuid，所以这里不把父节点当可选项；发帖窗口的选中值因此一直存
// uuid（见 composeState.nodeID），显示时才拼成父/子名字。
//
// clearLabel 非空时节点是选填的：第一列顶部多一行清除入口（发帖窗口写
// 「清除节点选择」），点它把值置空并收起面板。emptyLabel 是空值时触发框上的
// 文字（发帖窗口写「由 AI 判定分类」），为空时退回「不选择节点」。

// cascaderOption 是一列里的一个选项。Children 非空时它是分类，只在第一列
// 出现；Detail 是可选的右侧说明（节点表里放帖子数）。
type cascaderOption struct {
	Value    string
	Label    string
	Detail   string
	Children []cascaderOption
}

// cascaderState 是级联选择的展开状态，由使用它的窗口保存。active 是第一列
// 当前展开的分类值；选中项本身在 *value 里。
type cascaderState struct {
	open   bool
	active string
}

// cascader 画一个两列级联选择，把选中项写进 *value。label 是控件的可访问
// 名称；空值默认显示「请选择节点」，允许清空时显示「不选择节点」，
// emptyLabel 非空时优先显示它。
func cascader(c *ui.Context, label string, options []cascaderOption, value *string, st *cascaderState, clearLabel, emptyLabel string) {
	t := c.Theme()
	display, picked := cascaderLabel(options, *value)
	if !picked {
		display = "请选择节点"
		switch {
		case emptyLabel != "":
			display = emptyLabel
		case clearLabel != "":
			display = "不选择节点"
		}
	}
	textColor := t.Text
	if !picked {
		textColor = t.TextMuted
	}

	active := st.active
	activeIndex := -1
	for i := range options {
		if options[i].Value == active {
			activeIndex = i
			break
		}
	}
	// 第一次打开或节点表变了：展开当前选中项所在的分类；再不行就展开第一列。
	if activeIndex < 0 {
		active = cascaderRootOf(options, *value)
		for i := range options {
			if options[i].Value == active {
				activeIndex = i
				break
			}
		}
	}
	if activeIndex < 0 && len(options) > 0 {
		activeIndex = 0
	}

	trigger := ui.Button(c, "").FillWidth().Justify(ui.SpaceBetween).
		Label(label + "：" + display).Expanded(st.open)
	trigger.Children(func() {
		ui.Text(c, display).FontSize(12).SingleLine().TextColor(textColor)
		ui.Icon(c, chevronDownIcon).Size(10, 10).TextColor(t.TextMuted)
	})
	trigger.OnClick(func() { st.open = !st.open })

	ui.Popover(c, trigger, &st.open, func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Start).Children(func() {
			// 两列都限高并各自滚动，窗口缩到最小尺寸时面板也不会长出窗口。
			ui.Scroll(c).Width(180).MaxHeight(320).Children(func() {
				ui.Column(c).Gap(2).Children(func() {
					if clearLabel != "" {
						row := ui.ButtonBase(c).FillWidth().Padding(6, 9).Radius(6).
							AlignItems(ui.Center)
						switch {
						case *value == "":
							row.Background(t.Surface)
						case row.Hovered():
							row.Background(t.SurfaceHover)
						}
						row.Children(func() {
							ui.Text(c, clearLabel).FontSize(12).SingleLine().
								Grow(1).TextColor(t.TextMuted)
						})
						row.Label(clearLabel)
						row.OnClick(func() {
							*value = ""
							st.open = false
						})
					}
					for i := range options {
						a := &options[i]
						row := ui.ButtonBase(c).FillWidth().Padding(6, 9).Radius(6).
							Gap(6).AlignItems(ui.Center)
						fg := t.Text
						switch {
						case i == activeIndex:
							row.Background(t.Accent)
							fg = t.AccentText
						case row.Hovered():
							row.Background(t.SurfaceHover)
						}
						row.Children(func() {
							ui.Text(c, a.Label).FontSize(12).SingleLine().Grow(1).TextColor(fg)
							if a.Detail != "" {
								ui.Text(c, a.Detail).FontSize(10).SingleLine().TextColor(fg)
							}
							if len(a.Children) > 0 {
								ui.Icon(c, chevronRightIcon).Size(10, 10).TextColor(fg)
							}
						})
						row.Label("分类 " + a.Label)
						row.Tooltip(a.Label)
						row.OnClick(func() {
							st.active = a.Value
							// 没有子项的分类就是叶子：点第一列直接选中。
							if len(a.Children) == 0 {
								*value = a.Value
								st.open = false
							}
						})
					}
				})
			})

			ui.Box(c).Width(1).AlignSelf(ui.Stretch).Background(t.Border)

			ui.Scroll(c).Grow(1).MinWidth(250).MaxHeight(320).Children(func() {
				ui.Column(c).Gap(2).Children(func() {
					if activeIndex < 0 {
						ui.Text(c, "没有可选的分类").FontSize(11).TextColor(t.TextMuted).Padding(2, 6)
						return
					}
					active := options[activeIndex]
					ui.Text(c, "选择「"+active.Label+"」里的节点").FontSize(11).
						TextColor(t.TextMuted).Padding(2, 6)
					if len(active.Children) == 0 {
						ui.Text(c, "这个分类下没有子节点").FontSize(11).
							TextColor(t.TextMuted).Padding(2, 6)
						return
					}
					for j := range active.Children {
						ch := active.Children[j]
						row := ui.ButtonBase(c).FillWidth().Padding(6, 9).Radius(6).
							Gap(6).AlignItems(ui.Center)
						switch {
						case ch.Value == *value:
							row.Background(t.Surface)
						case row.Hovered():
							row.Background(t.SurfaceHover)
						}
						row.Children(func() {
							ui.Text(c, ch.Label).FontSize(12).SingleLine().Grow(1).
								TextColor(t.Text)
							if ch.Detail != "" {
								ui.Text(c, ch.Detail).FontSize(10).SingleLine().
									TextColor(t.TextMuted)
							}
						})
						row.Label(active.Label + " · " + ch.Label)
						row.Tooltip(ch.Label)
						row.OnClick(func() {
							*value = ch.Value
							st.open = false
						})
					}
				})
			})
		})
	})
}

// cascaderLabel 返回选中项在触发框里的显示文字：子项带上父分类，选中的
// 是叶子分类时只有它自己。
func cascaderLabel(options []cascaderOption, value string) (string, bool) {
	if value == "" {
		return "", false
	}
	for _, o := range options {
		if o.Value == value {
			return o.Label, true
		}
		for _, ch := range o.Children {
			if ch.Value == value {
				return o.Label + " · " + ch.Label, true
			}
		}
	}
	return "", false
}

// cascaderRootOf 返回 value 所在的第一列选项值；value 本身就是分类时返回它。
func cascaderRootOf(options []cascaderOption, value string) string {
	if value == "" {
		return ""
	}
	for _, o := range options {
		if o.Value == value {
			return o.Value
		}
		for _, ch := range o.Children {
			if ch.Value == value {
				return o.Value
			}
		}
	}
	return ""
}
