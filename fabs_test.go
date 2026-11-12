package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// fabListItems 造 n 行列表内容，标题带序号：滚动之后能看出哪一行还在视区里。
func fabListItems(n int) []Item {
	items := make([]Item, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, Item{Key: fmt.Sprintf("k%d", i), Title: fmt.Sprintf("第 %02d 项", i)})
	}
	return items
}

// 主页右下角是一列 FAB：发帖在最下面，刷新在它上面；「返回顶部」只在列表
// 滚离顶部之后才出现，出现时也不会把已有的两个按钮挤动。
func TestListFabsRenderRefreshAndBackTop(t *testing.T) {
	a := testApp(t)
	a.tabs[0].items = fabListItems(30)
	a.tabs[0].loaded = true
	a.tabs[0].total = 30
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	fab, ok := tt.Find("发帖")
	if !ok {
		t.Fatalf("找不到发帖 FAB：%q", tt.Texts())
	}
	refresh, ok := tt.Find("刷新")
	if !ok {
		t.Fatalf("找不到刷新 FAB：%q", tt.Texts())
	}
	// 一列都贴右下角：刷新在发帖正上方。
	if refresh.Y+refresh.H > fab.Y {
		t.Errorf("刷新没有排在发帖上面：刷新 %+v / 发帖 %+v", refresh, fab)
	}
	if refresh.X+refresh.W != fab.X+fab.W {
		t.Errorf("两个 FAB 没有右对齐：刷新 %+v / 发帖 %+v", refresh, fab)
	}
	if fab.X < 900 || fab.Y < 600 {
		t.Errorf("FAB 没有贴在右下角：发帖 %+v", fab)
	}
	// 还没滚动：不该有「返回顶部」。
	if r, ok := tt.Find("返回顶部"); ok {
		t.Errorf("列表在顶部时不该出现返回顶部：%+v", r)
	}

	// 滚下去之后出现「返回顶部」，位置在刷新之上，两个已有的按钮不动。
	tt.Scroll(500, 400, 0, 900)
	tt.Frame()
	if tt.HasText("第 00 项") {
		t.Fatalf("列表没有滚动：%q", tt.Texts())
	}
	top, ok := tt.Find("返回顶部")
	if !ok {
		t.Fatalf("滚离顶部后没有出现返回顶部：%q", tt.Texts())
	}
	if top.Y+top.H > refresh.Y {
		t.Errorf("返回顶部没有排在刷新上面：返回顶部 %+v / 刷新 %+v", top, refresh)
	}
	if again, _ := tt.Find("刷新"); again != refresh {
		t.Errorf("返回顶部出现把刷新挤动了：%+v → %+v", refresh, again)
	}
	if again, _ := tt.Find("发帖"); again != fab {
		t.Errorf("返回顶部出现把发帖挤动了：%+v → %+v", fab, again)
	}
}

// 点「返回顶部」把列表滚动到开头，按钮随即收起；它只动滚动位置，不重发请求。
func TestListFabBackToTopScrollsToStart(t *testing.T) {
	a := testApp(t)
	a.tabs[0].items = fabListItems(30)
	a.tabs[0].loaded = true
	a.tabs[0].total = 30
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	tt.Scroll(500, 400, 0, 2000)
	tt.Frame()
	if _, ok := tt.Find("返回顶部"); !ok {
		t.Fatalf("滚到底后没有出现返回顶部：%q", tt.Texts())
	}

	if err := tt.Click("返回顶部"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("第 00 项") {
		t.Errorf("点返回顶部没有回到列表开头：%q", tt.Texts())
	}
	if r, ok := tt.Find("返回顶部"); ok {
		t.Errorf("回到顶部后按钮应当收起：%+v", r)
	}
	// 回去之后刷新与发帖还在原位。
	if _, ok := tt.Find("刷新"); !ok {
		t.Error("回到顶部后刷新 FAB 不见了")
	}
	if _, ok := tt.Find("发帖"); !ok {
		t.Error("回到顶部后发帖 FAB 不见了")
	}
}

// 点「刷新」真的重发一次当前列表的请求，并用返回的内容替换列表。
func TestListFabRefreshReloads(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// 「首页」的列表字段是 posts（见 tabSpecs）。
		_, _ = io.WriteString(w, envelopeJSON(`{"posts":[{"id":"n1","short_id":"NEW1","title":"刷新后的内容"}],"total":1,"page":1,"limit":50,"total_pages":1}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.autoLoad = false // 只让「刷新」这一个动作发请求
	a.tabs[0].items = []Item{{Key: "old", Title: "旧内容"}}
	a.tabs[0].loaded = true
	a.tabs[0].total = 1
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("旧内容") {
		t.Fatalf("开始时应显示旧内容：%q", tt.Texts())
	}

	if err := tt.Click("刷新"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := a.tabs[0]
		if len(st.items) == 1 && st.items[0].Title == "刷新后的内容" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("点刷新后没有重新加载（请求 %d 次，err=%q，items=%d）",
				calls.Load(), a.tabs[0].err, len(a.tabs[0].items))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Errorf("刷新应当只发一次请求，实际 %d 次", calls.Load())
	}
	tt.Frame()
	if !tt.HasText("刷新后的内容") {
		t.Errorf("刷新后列表没有换成新内容：%q", tt.Texts())
	}
}

// 详情页不再有页头：刷新与返回顶部收在右上角的悬浮工具条里，压在评论卡片
// 头部右侧的留白上，不占正文高度。评论滚到底后出现返回顶部，点了评论回到
// 开头；按钮不会和卡片底部的「发送」抢位置。
func TestDetailToolbarRefreshAndBackToTop(t *testing.T) {
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
	a.detail.list.ScrollTo(59, ui.End)

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("第60条评论") {
		t.Fatalf("评论没有滚到底：%q", tt.Texts())
	}
	refresh, ok := tt.Find("刷新")
	if !ok {
		t.Fatalf("详情页没有刷新按钮：%q", tt.Texts())
	}
	top, ok := tt.Find("返回顶部")
	if !ok {
		t.Fatalf("评论滚离顶部后没有出现返回顶部：%q", tt.Texts())
	}
	// 工具条悬浮在右上角：靠近窗口顶部与右边缘，并与评论卡片头部同一带，
	// 说明它没有像旧版页头那样单独占一行。
	if refresh.Y > 90 || top.Y > 90 {
		t.Errorf("刷新 / 返回顶部应悬浮在内容右上角：刷新 %+v / 返回顶部 %+v", refresh, top)
	}
	if refresh.X < 900 {
		t.Errorf("悬浮工具条应贴在窗口右侧：刷新 %+v", refresh)
	}
	if top.Y != refresh.Y {
		t.Errorf("返回顶部与刷新应在同一行：返回顶部 %+v / 刷新 %+v", top, refresh)
	}
	comment, ok := tt.Find("评论")
	if !ok {
		t.Fatalf("找不到评论卡片标题：%q", tt.Texts())
	}
	if dy := refresh.Y + refresh.H/2 - (comment.Y + comment.H/2); dy > 20 || dy < -20 {
		t.Errorf("悬浮工具条没有压在评论卡片头部一带：刷新 %+v / 评论 %+v", refresh, comment)
	}
	// 评论输入栏固定在卡片底部，远在工具条下方，不再需要为 FAB 让宽度。
	send, ok := tt.Find("发送")
	if !ok {
		t.Fatalf("找不到发送按钮：%q", tt.Texts())
	}
	if send.Y <= refresh.Y+100 {
		t.Errorf("发送按钮应固定在评论卡片底部：发送 %+v / 工具条刷新 %+v", send, refresh)
	}

	if err := tt.Click("返回顶部"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("第1条评论") {
		t.Errorf("点返回顶部没有回到评论开头：%q", tt.Texts())
	}
	if r, ok := tt.Find("返回顶部"); ok {
		t.Errorf("回到顶部后按钮应当收起：%+v", r)
	}
}

// 详情页悬浮工具条里的「刷新」重新拉正文与评论：页面上还挂着的旧失败提示
// 要被清掉。
func TestDetailToolbarRefreshClearsError(t *testing.T) {
	a := detailAppWithReplies(t)
	a.detail.topicErr = "上一次读取失败的原因"
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("正文加载失败") {
		t.Fatalf("开始时应显示失败提示：%q", tt.Texts())
	}

	if err := tt.Click("刷新"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.detail.topicErr != "" {
		t.Errorf("刷新没有清掉失败状态：%q", a.detail.topicErr)
	}
	if tt.HasText("正文加载失败") {
		t.Errorf("刷新后仍显示失败提示：%q", tt.Texts())
	}
	// 已有正文保持显示，不闪成空白。
	if !tt.HasText("正文与评论的测试帖") {
		t.Errorf("刷新时正文不该消失：%q", tt.Texts())
	}
}

// 正文那一栏自己滚动同样会露出悬浮工具条的「返回顶部」：它把正文与评论一起
// 拉回开头。
func TestDetailToolbarBackToTopScrollsBody(t *testing.T) {
	a := detailAppWithReplies(t)
	var body strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&body, "第 %d 段正文。\n\n", i)
	}
	a.detail.topic.Content = body.String()
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if _, ok := tt.Find("返回顶部"); ok {
		t.Fatalf("正文在顶部时不该出现返回顶部：%q", tt.Texts())
	}
	before, ok := tt.Find("刷新")
	if !ok {
		t.Fatalf("找不到工具条刷新：%q", tt.Texts())
	}

	// 指针放在左栏正文上滚轮。
	tt.Scroll(300, 300, 0, 800)
	tt.Frame()
	if a.detail.bodyScroll.Y <= 0 {
		t.Fatalf("正文没有滚动：bodyScroll=%+v", a.detail.bodyScroll)
	}
	if _, ok := tt.Find("返回顶部"); !ok {
		t.Fatalf("正文滚离顶部后没有出现返回顶部：%q", tt.Texts())
	}
	// 悬浮工具条不随正文滚动：刷新始终停在同一位置。
	if after, ok := tt.Find("刷新"); !ok || after != before {
		t.Errorf("滚动后悬浮工具条移动了：刷新 %+v → %+v", before, after)
	}

	if err := tt.Click("返回顶部"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.detail.bodyScroll.Y != 0 {
		t.Errorf("点返回顶部后正文没有回到开头：bodyScroll=%+v", a.detail.bodyScroll)
	}
	if r, ok := tt.Find("返回顶部"); ok {
		t.Errorf("回到顶部后按钮应当收起：%+v", r)
	}
}
