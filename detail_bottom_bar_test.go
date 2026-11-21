package main

import (
	"fmt"
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

// 评论区列表必须吃满面板高度，底部只空出右下角操作组那一条窄带。列表容器
// 里曾用一个 ui.Spacer 垫高操作组：Spacer 默认 Grow(1)，和列表均分了整列
// 高度，评论只占一半、下面空出一大片。换成固定高度的占位后列表铺满，
// 这里用「最后一条可见评论要一直画到输入栏上方」把它锁住。
func TestReplyListFillsPaneAboveToolBar(t *testing.T) {
	a := detailWindowsApp(t)
	a.openDetailWindow(itemForWindow("POST001"))
	h := a.detailWindows[0]
	d := h.states[0]
	topic, err := decodeTopic([]byte(detailTopicJSON), "POST001")
	if err != nil {
		t.Fatal(err)
	}
	d.topic = &topic
	// 造足够多的评论，让列表一定溢出视口。
	const n = 30
	for i := 1; i <= n; i++ {
		d.replyList = append(d.replyList, replyItem{
			ID: fmt.Sprintf("c%d", i), Content: fmt.Sprintf("第 %d 楼评论内容", i),
			Author: fmt.Sprintf("user%d", i), Floor: i, Level: 1,
		})
	}
	d.replyLoaded = true

	tt := ui.NewTester(func(c *ui.Context) { a.detailWindowView(h, c) }, detailWindowWidth, detailWindowHeight)
	tt.Frame()

	editor, ok := tt.Find("写评论")
	if !ok {
		t.Fatal("找不到评论输入栏")
	}
	// 统计真正画出高度的行（视口外的行 H 为 0），取最靠下的一条。
	visible, lastBottom := 0, float32(0)
	for i := 1; i <= n; i++ {
		e, ok := tt.Find(fmt.Sprintf("第 %d 楼评论内容", i))
		if !ok || e.H <= 0 {
			continue
		}
		visible++
		if y := e.Y + e.H; y > lastBottom {
			lastBottom = y
		}
	}
	if visible < 5 {
		t.Errorf("评论可视行数过少：%d 行，列表应铺满评论面板", visible)
	}
	// 列表底部只留操作组那一条窄带（约 52 DIP），不该空出一大片。
	if gap := editor.Y - lastBottom; gap > replyToolBarReserve+40 {
		t.Errorf("评论列表底部空出过多：最后一行底边 %.1f，输入栏顶 %.1f，空隙 %.1f",
			lastBottom, editor.Y, gap)
	}
}
