package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/display"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// nodeTagRightGap 是标签和右侧相邻文字之间的额外留白。
const nodeTagRightGap float32 = 4

// nodeTagProps 是可点击节点标签的参数。Ref 为空或 OnPick 为空时退回只读
// Tag：接口没给节点信息的地方不应该出现一个点了没反应的标签。
type nodeTagProps struct {
	Label  string
	Ref    nodeRef
	OnPick func(nodeRef)
}

// nodeTagAction 在 Lobe Tag 外面包一层无外观的 ButtonBase：视觉与只读标签
// 完全一致，但指针、键盘与无障碍都会把它当成按钮。最内层按钮会吃掉点击，
// 列表行里的节点标签不会连带打开帖子。
func nodeTagAction(c *ui.Context, p nodeTagProps) ui.Element {
	if p.Ref.ID == "" && p.Ref.Slug == "" || p.OnPick == nil {
		return nodeTag(c, p.Label)
	}
	tk := lobetheme.TokensOf(c)
	label := p.Label
	if label == "" {
		label = p.Ref.label()
	}
	b := ui.ButtonBase(c).Cursor(ui.CursorPointer).Radius(tk.BorderRadiusSM)
	if b.Hovered() {
		b.Background(tk.FillTertiary)
	}
	b.Children(func() {
		nodeTag(c, label)
	})
	b.Label("筛选节点：" + label)
	b.Tooltip("筛选到「" + label + "」")
	b.OnClick(func() { p.OnPick(p.Ref) })
	return b.MarginEnd(nodeTagRightGap)
}

// nodeTag 把节点 / 分类名统一画成 Lobe 的默认 Tag：中性填充、圆角、小尺寸，
// 不上额外颜色。列表副标题、详情页副标题与评论行共用这一处，避免同一个节点
// 在各处一会儿是灰字、一会儿是彩色胶囊；小尺寸让标签贴在 11～13px 的元信息旁
// 不会把行高撑起来。
func nodeTag(c *ui.Context, name string) ui.Element {
	// 右侧留出一点间距：Tag 后面往往紧跟作者名或元信息文字，贴太近会糊在一起。
	return display.Tag(c, name, display.TagProps{
		Size:  lobetheme.Small,
		Round: true,
	}).MarginEnd(nodeTagRightGap)
}
