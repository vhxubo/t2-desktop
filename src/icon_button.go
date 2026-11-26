package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
)

// iconActionSize 是普通图标按钮里图标的边长。按钮本身不写文字，靠图标表意，
// hover 时由 Tooltip 给出完整说明（见 iconActionButton）。
const iconActionSize float32 = 16

// iconActionBox 是图标按钮的方形热区：比详情页标题栏那枚动作的 30×30 略紧凑。
// 固定尺寸也让它放进 Column 时不会被拉成一整行宽——那样 hover 底色会横贯
// 整行，看着像一条横幅。
const iconActionBox float32 = 28

// iconActionButton 画一个「图标 + 无障碍名 + 悬浮说明」的普通操作按钮：平时
// 只有图标，鼠标扫过时给一层浅底并弹出说明。界面里所有能用一个通行图标说清
// 的动作都该用它，别再写「关闭」「删除」这类纯文字按钮。
//
// 已经各有尺寸与 hover 规范的几处仍用各自的画法：窗口标题栏用
// titleBarButton、详情页标题栏那枚动作用 detailToolButton、编辑器工具条用
// editorIconButton。这里服务的是对话框标题行、设置项与列表行这类普通位置。
//
// label 是读屏名字与点击测试的命中名；tip 要说清点了会发生什么，而不是把
// label 再抄一遍。返回值交给调用方判断点击：要改本帧的局部变量（例如批量删除
// 先攒下要删的行号）就用 `.Clicked()`，它当场生效；只改能留到下一帧的状态才用
// `.OnClick(fn)`，那是构建结束之后才跑的延迟动作。禁用态在返回值上补
// `.Disabled(true)` 即可，Tooltip 仍能提示为什么点不动。
//
// 外观交给 Lobe 的 action.ActionIcon：方块热区、圆角、悬停底色、图标着色与
// Tooltip 都由它按主题 token 统一处理，这里只负责把两种文案分开——Title 给
// Tooltip 用，Label 覆盖成读屏用的短名。
func iconActionButton(c *ui.Context, icon *ui.SVG, label, tip string) ui.Element {
	b := action.ActionIcon(c, icon, action.ActionIconProps{
		BlockSize: iconActionBox,
		GlyphSize: iconActionSize,
		Title:     tip,
	})
	// Lobe 的静止/悬停图标色都比本应用原来的 TextMuted/Text 浅一档；这里沿用
	// 原来的两种墨色，hover 时仍由 ActionIcon 负责底色，只有字形颜色跟随。
	ink := c.Theme().TextMuted
	if b.Hovered() {
		ink = c.Theme().Text
	}
	return b.TextColor(ink).Label(label).KeepFocus()
}
