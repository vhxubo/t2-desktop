package main

import "github.com/egoist/mygo/ui"

// iconActionSize 是普通图标按钮里图标的边长。按钮本身不写文字，靠图标表意，
// hover 时由 Tooltip 给出完整说明（见 iconActionButton）。
const iconActionSize float32 = 16

// iconActionBox 是图标按钮的方形热区：比详情页悬浮工具条的 30×30 略紧凑。
// 固定尺寸也让它放进 Column 时不会被拉成一整行宽——那样 hover 底色会横贯
// 整行，看着像一条横幅。
const iconActionBox float32 = 28

// iconActionButton 画一个「图标 + 无障碍名 + 悬浮说明」的普通操作按钮：平时
// 只有图标，鼠标扫过时给一层浅底并弹出说明。界面里所有能用一个通行图标说清
// 的动作都该用它，别再写「关闭」「删除」这类纯文字按钮。
//
// 已经各有尺寸与 hover 规范的几处仍用各自的画法：窗口标题栏用
// titleBarButton、详情页悬浮工具条用 detailToolButton、编辑器工具条用
// editorIconButton。这里服务的是对话框标题行、设置项与列表行这类普通位置。
//
// label 同时是读屏名字与点击测试的命中名；tip 要说清点了会发生什么，而不是把
// label 再抄一遍。返回值交给调用方判断点击：要改本帧的局部变量（例如批量删除
// 先攒下要删的行号）就用 `.Clicked()`，它当场生效；只改能留到下一帧的状态才用
// `.OnClick(fn)`，那是构建结束之后才跑的延迟动作。禁用态在返回值上补
// `.Disabled(true)` 即可，Tooltip 仍能提示为什么点不动。
func iconActionButton(c *ui.Context, icon *ui.SVG, label, tip string) ui.Element {
	t := c.Theme()
	b := ui.Button(c, "").
		Size(iconActionBox, iconActionBox).
		Border(0, t.Border).
		Radius(6).
		// ui.Button 默认带 Padding(6,14)，而这里把按钮框死了 28×28：默认
		// 左右各 14 的中度内边距会把内容盒压成 0 宽，图标贴着内容盒左沿画，
		// 结果整枚图标偏右 8px，hover 底色却铺满整框，看着就是底色错位。
		// 显式清成 0，让图标在方形热区里真正居中，hover 底色也与图标对齐。
		Padding(0).
		KeepFocus()
	// 悬停底色要自己给：按钮自带的 hover 样式只在背景没被改过时才生效，
	// 而这里要的是「没有边框、只有一圈浅底」的图标按钮。
	b.Background(ui.Transparent)
	hovered := b.Hovered()
	if hovered {
		b.Background(t.SurfaceHover)
	}
	ink := t.TextMuted
	if hovered {
		ink = t.Text
	}
	b.Children(func() {
		ui.Icon(c, icon).Size(iconActionSize, iconActionSize).TextColor(ink)
	})
	return b.Label(label).Tooltip(tip)
}
