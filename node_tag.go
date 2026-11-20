package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/display"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// nodeTagRightGap 是标签和右侧相邻文字之间的额外留白。
const nodeTagRightGap float32 = 4

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
