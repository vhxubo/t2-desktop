package main

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/field"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/motion"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 与 lobe-mygo 的 field 组件保持同一套过渡参数。
var lobeSelectFieldTransition = ui.ElementTransition{
	Duration: 150 * time.Millisecond, Ease: motion.EaseOut, Colors: true, Shadows: true,
}
var lobeSelectOptionTransition = ui.ElementTransition{
	Duration: 150 * time.Millisecond, Ease: motion.EaseOut, Colors: true,
}
var lobeSelectArrowTurn = ui.Animation{Duration: 150 * time.Millisecond, Ease: motion.EaseOut}

// lobeSelect 是 field.Select 的同款替代：外观、交互和返回元素都一致，只多
// 做一件事——把选项装进一个可收缩的滚动容器。mygo 在弹层超出窗口时会把它
// 压到剩余空间，但只有会收缩的子节点才跟着缩短；lobe-mygo 固定版本的
// field.Select 把选项直接铺在面板里，选项一多（例如金币类型的几十个 reason）
// 就会从面板底部溢出到窗口外。这里沿用同样的 token 和键盘操作，修掉溢出。
//
// 用法与 field.Select 相同，返回的仍是 trigger，可继续 .Label() / .Changed()。
func lobeSelect(c *ui.Context, selected *string, p field.SelectProps) ui.Element {
	tk := lobetheme.TokensOf(c)
	minHeight, fontSize, radius, padX, padY := float32(32), float32(14), tk.BorderRadius, float32(11), float32(4)
	switch p.Size {
	case lobetheme.Small:
		minHeight, fontSize, radius, padX, padY = 24, 12, tk.BorderRadiusSM, 8, 0
	case lobetheme.Large:
		minHeight, fontSize, radius, padX, padY = 40, 16, tk.BorderRadiusLG, 12, 6
	}
	sel := ui.SelectBase(c, selected)
	tr := sel.Trigger.MinHeight(minHeight).Padding(padY, padX).Gap(8).Radius(radius).FontSize(fontSize).
		AlignItems(ui.Center).TextColor(tk.Text).Cursor(ui.CursorPointer).Unselectable().
		Transition(lobeSelectFieldTransition)
	if p.Disabled {
		tr.Disabled(true).Cursor(ui.CursorNotAllowed).TextColor(tk.TextDisabled)
	}
	styleLobeSelectField(tr, tk, p.Variant, sel.Open())

	var current *field.SelectOption
	for i := range p.Options {
		if p.Options[i].Value == *selected {
			current = &p.Options[i]
		}
	}
	tr.Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Gap(8).AlignItems(ui.Center).Children(func() {
			switch {
			case current != nil:
				if current.Icon != nil {
					ui.Icon(c, current.Icon).TextColor(tk.TextSecondary)
				}
				ui.Text(c, current.Label).SingleLine()
			default:
				ui.Text(c, p.Placeholder).TextColor(tk.TextPlaceholder).SingleLine()
			}
		})
		turn := float32(0)
		if sel.Open() {
			turn = 180
		}
		arrow := ui.Icon(c, lucide.ChevronDown()).FontSize(16).TextColor(tk.TextSecondary).Shrink(0)
		arrow.Rotate(arrow.AnimateTo("turn", turn, lobeSelectArrowTurn))
	})

	width := tr.Bounds().W
	sel.Popup(func(panel ui.Element) {
		panel.Margin(4, 0, 0, 0).Padding(4).Radius(tk.BorderRadius).MinWidth(width).MaxHeight(256).
			Background(tk.BgElevated).Transition(motion.Popup()).AttachedOrigin()
		tk.Elevate(panel, lobetheme.BoxShadowSecondary)
		// 关键差异：panel 被 mygo 压到窗口剩余空间时，这个滚动容器跟着收缩，
		// 超出的选项在里面滚动，而不是画到面板外、甚至窗口外。
		ui.Scroll(c).Shrink(1).Children(func() {
			for _, o := range p.Options {
				lobeSelectOption(c, tk, sel, o, o.Value == *selected)
			}
		})
	})
	return tr
}

func lobeSelectOption(c *ui.Context, tk *lobetheme.Tokens, sel ui.SelectParts[string], o field.SelectOption, chosen bool) {
	item := sel.Item(o.Value).Disabled(o.Disabled).Padding(6, 12).MinHeight(32).Gap(8).AlignItems(ui.Center).
		Radius(tk.BorderRadiusSM).AlignSelf(ui.Stretch).FontSize(14).TextColor(tk.Text).
		Transition(lobeSelectOptionTransition)
	switch {
	case o.Disabled:
		item.TextColor(tk.TextDisabled).Cursor(ui.CursorNotAllowed)
	case item.Highlighted():
		item.Background(tk.FillTertiary).Cursor(ui.CursorPointer)
	default:
		item.Cursor(ui.CursorPointer)
	}
	item.Children(func() {
		if o.Icon != nil {
			ui.Icon(c, o.Icon).FontSize(14).TextColor(tk.TextSecondary)
		}
		ui.Text(c, o.Label).Grow(1).SingleLine()
		if chosen {
			ui.Icon(c, lucide.Check()).FontSize(14).TextColor(tk.Primary).Margin(0, 0, 0, 8)
		}
	})
}

// styleLobeSelectField 照搬 lobe-mygo 的 field.styleField（未导出），保证替换后
// 输入框的底色、边框和聚焦环与同页其它字段完全一致。field.Select 不会传
// invalid，所以这里省掉错误态。
func styleLobeSelectField(root ui.Element, tk *lobetheme.Tokens, v field.InputVariant, focused bool) {
	hovered := root.Hovered() && !focused && !root.IsDisabled()
	switch v {
	case field.InputFilled:
		bg := tk.FillTertiary
		if hovered {
			bg = tk.FillSecondary
		}
		root.Background(bg)
	case field.InputBorderless:
		root.Background(ui.Transparent)
	default:
		root.Background(tk.BgContainer)
	}
	border := ui.Transparent
	if v == field.InputOutlined {
		border = tk.BorderSecondary
		if hovered {
			border = tk.Border
		}
		if focused {
			border = tk.Primary
		}
	}
	root.Border(1, border)
	if focused && v == field.InputOutlined {
		root.Shadow(0, 0, 0, 2, tk.PrimaryBg)
	}
}
