package main

import (
	"fmt"
	"testing"
	"time"

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

// 正文底部的表态 / 打赏图标必须在互动栏内上下居中：Column 默认沿主轴
// （垂直）从顶部排子元素，光靠固定的条带高度和对称内边距不会自动居中，
// 图标会贴着上沿，看着像浮在框上方。
func TestTopicInteractionEntriesCenteredInBar(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	bar, ok := tt.Find("正文互动栏")
	if !ok {
		t.Fatal("正文底部互动栏没有渲染")
	}
	mid := bar.Y + bar.H/2
	for _, name := range []string{"表情", "打赏"} {
		entry, ok := tt.Find(name)
		if !ok {
			t.Fatalf("正文互动栏里没有 %s 入口：%q", name, tt.Texts())
		}
		if dy := (entry.Y + entry.H/2) - mid; dy < -2 || dy > 2 {
			t.Errorf("%s 图标应在互动栏内上下居中：Δy=%.1f（栏中线 %.1f，%+v）",
				name, dy, mid, entry)
		}
	}
	// 汇总走 Lobe Small Tag：高度固定 20，和 28 的图标一起落在条带中线上。
	chip, ok := tt.Find("表情，共 14 次")
	if !ok {
		t.Fatalf("正文互动栏里没有表态汇总：%q", tt.Texts())
	}
	if chip.H != 20 {
		t.Errorf("表态汇总应是 20px 的 Lobe Tag：%+v", chip)
	}
	if dy := (chip.Y + chip.H/2) - mid; dy < -2 || dy > 2 {
		t.Errorf("表态汇总应在互动栏内上下居中：Δy=%.1f（%+v）", dy, chip)
	}
}

// 评论区列表必须吃满面板高度，最后一条评论一直铺到输入栏上方。列表容器
// 里曾用一个 ui.Spacer 垫高右下角操作组：Spacer 默认 Grow(1)，和列表均分了
// 整列高度，评论只占一半、下面空出一大片。操作组已经移到输入栏发送按钮
// 右侧，列表底部不再预留空带，这里用「最后一条可见评论紧贴输入栏」锁住。
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
	// 操作组已嵌进输入栏，列表底部不该再留一条空白。
	if gap := editor.Y - lastBottom; gap > 40 {
		t.Errorf("评论列表底部空出过多：最后一行底边 %.1f，输入栏顶 %.1f，空隙 %.1f",
			lastBottom, editor.Y, gap)
	}
}

// 评论区自己的操作已经嵌入折叠输入框右下角：发送按钮右侧是视图切换与刷新，
// 已有的评论数 / 浏览数留在发送左侧。两组之间靠竖线区分，位置写死在测试里，
// 免得以后又飘回评论列表上压住最后一条评论的回复入口。
func TestReplyToolsEmbeddedBesideSend(t *testing.T) {
	a := detailWindowsApp(t)
	a.openDetailWindow(itemForWindow("POST001"))
	h := a.detailWindows[0]
	d := h.states[0]
	topic, err := decodeTopic([]byte(detailTopicJSON), "POST001")
	if err != nil {
		t.Fatal(err)
	}
	d.topic = &topic
	for i := 1; i <= 30; i++ {
		d.replyList = append(d.replyList, replyItem{
			ID: fmt.Sprintf("c%d", i), Content: fmt.Sprintf("第 %d 楼评论内容", i),
			Author: fmt.Sprintf("user%d", i), Floor: i, Level: 1,
		})
	}
	d.replyLoaded = true
	// 滚离顶部，让「评论回到顶部」也出现在这排里。
	d.list.ScrollTo(29, ui.End)

	tt := ui.NewTester(func(c *ui.Context) { a.detailWindowView(h, c) }, detailWindowWidth, detailWindowHeight)
	tt.Frame()

	send, ok := tt.Find("发送")
	if !ok {
		t.Fatalf("找不到发送按钮：%q", tt.Texts())
	}
	view, ok := tt.Find("切到扁平时间序")
	if !ok {
		t.Fatalf("找不到视图切换：%q", tt.Texts())
	}
	refresh, ok := tt.Find("刷新评论")
	if !ok {
		t.Fatalf("找不到评论刷新：%q", tt.Texts())
	}
	top, ok := tt.Find("评论回到顶部")
	if !ok {
		t.Fatalf("滚离顶部后找不到评论回顶：%q", tt.Texts())
	}
	count, ok := tt.Find("评论数")
	if !ok {
		t.Fatalf("找不到评论数：%q", tt.Texts())
	}
	composer, ok := tt.Find("评论输入栏")
	if !ok {
		t.Fatalf("找不到评论输入栏：%q", tt.Texts())
	}
	area, ok := tt.Find("写评论")
	if !ok {
		t.Fatalf("找不到评论输入框：%q", tt.Texts())
	}
	if count.X+count.W > send.X {
		t.Errorf("评论数应在发送左侧：count=%+v send=%+v", count, send)
	}
	// 右侧整排在输入框内上下居中：计数、发送、操作组的中线都落在输入框中线上。
	for _, part := range []struct {
		name string
		rect ui.Rect
	}{{"评论数", count}, {"发送", send}, {"视图切换", view}, {"刷新", refresh}, {"回顶", top}} {
		if dy := (part.rect.Y + part.rect.H/2) - (area.Y + area.H/2); dy < -2 || dy > 2 {
			t.Errorf("%s应在输入框内上下居中：Δy=%.1f（输入框中线 %.1f，%+v）",
				part.name, dy, area.Y+area.H/2, part.rect)
		}
	}
	for _, tool := range []struct {
		name string
		rect ui.Rect
	}{{"视图切换", view}, {"刷新", refresh}, {"回顶", top}} {
		if tool.rect.X < send.X+send.W {
			t.Errorf("%s应在发送右侧：send=%+v %s=%+v", tool.name, send, tool.name, tool.rect)
		}
		if tool.rect.X+tool.rect.W > composer.X+composer.W {
			t.Errorf("%s越出了评论输入栏：tool=%+v composer=%+v", tool.name, tool.rect, composer)
		}
	}

	// 嵌进输入栏后这组仍可用：点视图切换照旧切到扁平时间序。
	if err := tt.Click("切到扁平时间序"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !d.flat {
		t.Error("点视图切换没有切到扁平时间序")
	}
}

// 评论区右下角的「跳到最新回复」和回顶对称：停在顶部时出现，点一下滚到
// 当前已加载的最后一条评论；到底后让位给回顶，回顶后又回来。行号取 display，
// 折叠的楼中楼不会把目标带偏。
func TestReplyJumpToLatestButton(t *testing.T) {
	a := detailAppWithReplies(t)
	rows := make([]replyItem, 0, 60)
	for i := 0; i < 60; i++ {
		rows = append(rows, replyItem{
			ID: fmt.Sprintf("c%d", i), Author: "u", Floor: i + 1,
			Content: fmt.Sprintf("第%d条评论", i+1),
		})
	}
	a.detail.replyList = rows
	a.detail.replyTotal = len(rows)
	a.detail.replyLoaded = true
	a.detail.replyNoMore = true // 不触发无限滚动续拉，专注验证跳转本身

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if _, ok := tt.Find("跳到最新回复"); !ok {
		t.Fatalf("停在顶部应有跳到最新回复：%q", tt.Texts())
	}
	if _, ok := tt.Find("评论回到顶部"); ok {
		t.Fatalf("停在顶部不该有回顶：%q", tt.Texts())
	}
	if !tt.HasText("第1条评论") {
		t.Fatalf("初始应停在评论顶部：%q", tt.Texts())
	}

	// 停在中间时两枚跳转都在，输入框右侧留白跟着加宽，按钮不压到文字上。
	a.detail.list.ScrollTo(30, ui.Start)
	tt.Frame()
	for _, label := range []string{"评论回到顶部", "跳到最新回复"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("停在中间应有 %q：%q", label, tt.Texts())
		}
	}
	area, _ := tt.Find("写评论")
	send, _ := tt.Find("发送")
	if gap := (area.X + area.W) - (send.X + send.W); gap > replyComposerToolReserve+replyJumpExtraReserve+8 {
		t.Errorf("两枚跳转同屏时输入框留白不够：差 %.1f（area=%+v send=%+v）", gap, area, send)
	}

	if err := tt.Click("跳到最新回复"); err != nil {
		t.Fatalf("点跳到最新回复失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("第60条评论") {
		t.Errorf("点跳到最新回复后没滚到最后一条：%q", tt.Texts())
	}
	if _, ok := tt.Find("评论回到顶部"); !ok {
		t.Errorf("到底后应出现回顶：%q", tt.Texts())
	}
	if _, ok := tt.Find("跳到最新回复"); ok {
		t.Errorf("到底后不该再画跳到最新：%q", tt.Texts())
	}

	if err := tt.Click("评论回到顶部"); err != nil {
		t.Fatalf("点回顶失败：%v", err)
	}
	tt.Frame()
	if !tt.HasText("第1条评论") {
		t.Errorf("点回顶后没回到第一条：%q", tt.Texts())
	}
	if _, ok := tt.Find("跳到最新回复"); !ok {
		t.Errorf("回顶后应重新出现跳到最新：%q", tt.Texts())
	}
}

// 右侧评论区收起后，输入框照旧平铺在正文底部，但那组评论操作不该跟着过来：
// 此刻没有评论列表可回顶 / 切换 / 刷新。展开后整组回到发送右侧。
func TestReplyToolsHiddenWhenReplyCollapsed(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if _, ok := tt.Find("刷新评论"); !ok {
		t.Fatalf("展开态找不到评论操作组：%q", tt.Texts())
	}
	if err := tt.Click("收起评论"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(500 * time.Millisecond)
	if !a.detail.replyCollapsed {
		t.Fatal("点收起评论没有生效")
	}
	if _, ok := tt.Find("刷新评论"); ok {
		t.Errorf("右栏收起后不该再画评论操作组：%q", tt.Texts())
	}
	if _, ok := tt.Find("切到扁平时间序"); ok {
		t.Errorf("右栏收起后不该再画视图切换：%q", tt.Texts())
	}
	if _, ok := tt.Find("写评论"); !ok {
		t.Errorf("右栏收起后输入框仍应平铺在正文底部：%q", tt.Texts())
	}
	if err := tt.Click("展开评论"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(500 * time.Millisecond)
	if _, ok := tt.Find("刷新评论"); !ok {
		t.Errorf("展开右栏后评论操作组应回到发送右侧：%q", tt.Texts())
	}
}
