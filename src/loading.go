package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
)

// lobeSpin 返回一个遵循父容器居中的 Lobe UI 转圈。
//
// feedback.Spin 内部会给根元素设 AlignSelf(Start)，即使父容器 Center() 了，
// 转圈仍会被钉在交叉轴起点（实测在居中的 Column 里 X=0）。改回 Center 后
// 转圈才真正落在容器的上下左右正中。所有加载态都走这里，保证观感一致：
// 在居中的 Column 里水平居中，在 AlignItems(Center) 的 Row 里垂直居中。
func lobeSpin(c *ui.Context, p feedback.SpinProps) ui.Element {
	return feedback.Spin(c, p, nil).AlignSelf(ui.Center)
}
