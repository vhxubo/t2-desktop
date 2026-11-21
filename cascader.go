package main

// 节点级联选择的数据结构。渲染已经统一到 node_cascader.go 的 Lobe
// Popover + display.List：这里只保留选项转换与显示文案的纯函数，供发帖
// 窗口、测试以及旧数据兼容使用，不再包含手写的按钮行样式。

// cascaderOption 是一列里的一个选项。Children 非空时它是分类，只在第一列
// 出现；Detail 是可选的右侧说明（节点表里放帖子数）。
type cascaderOption struct {
	Value    string
	Label    string
	Detail   string
	Children []cascaderOption
}

// cascaderState 是级联选择的展开状态，由使用它的窗口保存。active 是第一列
// 当前展开的分类值；选中项本身放在使用方的选中值里。
type cascaderState struct {
	open   bool
	active string
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
