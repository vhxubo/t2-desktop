package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 详情页左右两栏的底部条带默认等高：左边是正文的表态 / 打赏互动栏，右边是
// 折叠态的评论输入框。两栏一起渲染时用无头测试量实际像素，免得一边改了尺寸
// 另一边忘了跟。
func TestDetailBottomBarsAligned(t *testing.T) {
	a := detailWindowsApp(t)
	a.openDetailWindow(itemForWindow("POST001"))
	h := a.detailWindows[0]
	d := h.states[0]
	topic, err := decodeTopic([]byte(detailTopicJSON), "POST001")
	if err != nil {
		t.Fatal(err)
	}
	d.topic = &topic
	pg := decodeReplies([]byte(detailRepliesJSON))
	d.replyList = flattenReplies(pg)
	d.replyLoaded = true

	tt := ui.NewTester(func(c *ui.Context) { a.detailWindowView(h, c) }, detailWindowWidth, detailWindowHeight)
	tt.Frame()

	left, ok := tt.Find("正文互动栏")
	if !ok {
		t.Fatal("正文底部互动栏没有渲染")
	}
	right, ok := tt.Find("评论输入栏")
	if !ok {
		t.Fatal("评论区底部输入栏没有渲染")
	}
	if left.H != right.H {
		t.Errorf("两栏底部条带应等高：正文互动栏 %.0f，评论输入栏 %.0f", left.H, right.H)
	}
	// 正文互动栏里的表情入口应是大号尺寸，不是评论底行的紧凑尺寸。
	emoji, ok := tt.Find("表情")
	if !ok {
		t.Fatal("正文互动栏里没有表情入口")
	}
	if emoji.W != interactEntryComfy.box || emoji.H != interactEntryComfy.box {
		t.Errorf("正文表情入口应是 %.0f 的盒子，实际 %.0fx%.0f",
			interactEntryComfy.box, emoji.W, emoji.H)
	}
}
