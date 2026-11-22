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

// 评论区操作组已经嵌到输入栏发送按钮右侧，不再悬在列表上；列表滚到底时
// 最后一条评论的「回复」图标不该被它压住。
func TestDetailCommentToolbarDoesNotCoverLastReply(t *testing.T) {
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

	// 取最后一条可见评论的回复按钮，和输入栏那排操作里最靠上的一枚比高度。
	var lastReply, toolbarTop ui.Rect
	var hasReply, hasToolbar bool
	for _, n := range tt.AccessAll() {
		switch n.Name {
		case "回复":
			if !hasReply || n.Bounds.Y > lastReply.Y {
				lastReply, hasReply = n.Bounds, true
			}
		case "评论回到顶部", "切到扁平时间序", "切到楼层视图", "刷新评论":
			if !hasToolbar || n.Bounds.Y < toolbarTop.Y {
				toolbarTop, hasToolbar = n.Bounds, true
			}
		}
	}
	if !hasReply {
		t.Fatalf("滚到底后看不到回复按钮：%q", tt.Texts())
	}
	if !hasToolbar {
		t.Fatalf("看不到评论区操作组：%q", tt.Texts())
	}
	if lastReply.Y+lastReply.H > toolbarTop.Y {
		t.Errorf("评论操作组压住了最后一条评论的回复图标：回复 %+v / 操作组顶端 %+v", lastReply, toolbarTop)
	}
}

// 主页右下角的一列 FAB 全是圆按钮：主操作「发帖」是整圆（对角的像素透出页面
// 底色，最宽处才是强调色），次级按钮也按直径对半取圆角。这里用有实色填充的
// 发帖按钮验证圆角几何，其余几枚核对尺寸没有被改掉。
func TestHomeFabsAreCircular(t *testing.T) {
	a := testApp(t)
	a.tabs[0].items = fabListItems(30)
	a.tabs[0].loaded = true
	a.tabs[0].total = 30
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	img := tt.Image()
	fab, ok := tt.Find("发帖")
	if !ok {
		t.Fatalf("找不到发帖 FAB：%q", tt.Texts())
	}
	if fab.W != fabPrimary || fab.H != fabPrimary {
		t.Fatalf("发帖 FAB 尺寸不对：%+v", fab)
	}
	// 整圆：四角应是页面底色（浅色主题纯白），横向最宽处才是强调色。
	left, top := int(fab.X), int(fab.Y)
	right, bottom := int(fab.X+fab.W)-1, int(fab.Y+fab.H)-1
	cy := int(fab.Y + fab.H/2)
	for _, p := range [][2]int{{left + 1, top + 1}, {right - 1, top + 1}, {left + 1, bottom - 1}, {right - 1, bottom - 1}} {
		if cl := img.RGBAAt(p[0], p[1]); cl.R != 255 || cl.G != 255 || cl.B != 255 {
			t.Errorf("发帖 FAB 角落 (%d,%d) 应是页面底色，实际 %v", p[0], p[1], cl)
		}
	}
	if cl := img.RGBAAt(left+1, cy); cl.R != 0x1D || cl.G != 0x4E || cl.B != 0xD8 {
		t.Errorf("发帖 FAB 最宽处应是强调色 #1D4ED8，实际 %v", cl)
	}

	// 次级按钮维持原尺寸，仍是圆按钮。
	for _, name := range []string{"刷新", "发动态"} {
		r, ok := tt.Find(name)
		if !ok {
			t.Fatalf("找不到 %s FAB：%q", name, tt.Texts())
		}
		if r.W != fabCircle || r.H != fabCircle {
			t.Errorf("%s FAB 尺寸不对：%+v", name, r)
		}
	}

	// 滚离顶部后出现的「返回顶部」也是同一套圆按钮。
	tt.Scroll(500, 400, 0, 900)
	tt.Frame()
	topFab, ok := tt.Find("返回顶部")
	if !ok {
		t.Fatalf("滚离顶部后没有出现返回顶部：%q", tt.Texts())
	}
	if topFab.W != fabCircle || topFab.H != fabCircle {
		t.Errorf("返回顶部 FAB 尺寸不对：%+v", topFab)
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
	// 分页状态要写成「只有这一页」：首页列表有无限滚动，第一帧列表还没排过
	// 版时 AtEnd 为真，若 noMore 还是 false 就会在构建期自发续拉第 2 页，
	// 把「旧内容」换成本测试服务器的返回值，断言随之偶发失败。
	a.tabs[0].page = 1
	a.tabs[0].totalPages = 1
	a.tabs[0].noMore = true
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

// 正文与评论区各有自己的一键回顶与刷新：正文那组竖排贴在正文面板右下角，
// 评论那组横排嵌在输入栏发送按钮右侧；两组只滚各自那一栏，互不干扰。
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
	bodyRefresh, ok := tt.Find("刷新")
	if !ok {
		t.Fatalf("正文区没有刷新按钮：%q", tt.Texts())
	}
	commentTop, ok := tt.Find("评论回到顶部")
	if !ok {
		t.Fatalf("评论滚离顶部后没有出现回顶按钮：%q", tt.Texts())
	}
	commentRefresh, ok := tt.Find("刷新评论")
	if !ok {
		t.Fatalf("评论区没有刷新按钮：%q", tt.Texts())
	}
	// 正文没滚，不该出现正文的回顶：那一枚只归正文滚动管。
	if r, ok := tt.Find("返回顶部"); ok {
		t.Errorf("正文没滚时不该出现正文回顶：%+v", r)
	}
	// 评论组横排嵌在输入栏右下、发送右侧：回顶在刷新左边，两枚同一行。
	if commentTop.Y != commentRefresh.Y {
		t.Errorf("评论回顶与刷新应排在同一行：%+v / %+v", commentTop, commentRefresh)
	}
	if commentTop.X+commentTop.W > commentRefresh.X {
		t.Errorf("评论回顶应排在刷新左边：%+v / %+v", commentTop, commentRefresh)
	}
	// 两组各管一栏：正文组在左栏下部，评论组在右栏底部的输入栏里。
	bar, ok := tt.Access("正文互动栏")
	if !ok {
		t.Fatalf("找不到正文互动栏：%q", tt.Texts())
	}
	if bodyRefresh.X >= commentRefresh.X {
		t.Errorf("正文组应在评论组左侧：%v / %v", bodyRefresh, commentRefresh)
	}
	if bodyRefresh.Y < 380 || bodyRefresh.X+bodyRefresh.W > bar.Bounds.X+bar.Bounds.W {
		t.Errorf("正文组应贴在正文区下部：刷新 %+v / 互动栏 %+v", bodyRefresh, bar.Bounds)
	}
	if commentRefresh.Y < 380 {
		t.Errorf("评论组应贴在评论区下部：%+v", commentRefresh)
	}
	// 评论输入栏固定在卡片底部，发送与操作组同处这一行。
	send, ok := tt.Find("发送")
	if !ok {
		t.Fatalf("找不到发送按钮：%q", tt.Texts())
	}
	if send.Y < commentRefresh.Y {
		t.Errorf("发送按钮应固定在评论卡片底部：发送 %+v / 评论刷新 %+v", send, commentRefresh)
	}

	// 评论回顶只滚评论列表，正文组不跟着收起。
	if err := tt.Click("评论回到顶部"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("第1条评论") {
		t.Errorf("点评论回顶没有回到评论开头：%q", tt.Texts())
	}
	if r, ok := tt.Find("评论回到顶部"); ok {
		t.Errorf("回到顶部后评论回顶按钮应当收起：%+v", r)
	}
	if _, ok := tt.Find("刷新"); !ok {
		t.Error("评论回顶不该把正文区的刷新一起收起")
	}
}

// 正文区操作组里的「刷新」只重拉正文：页面上还挂着的旧正文失败提示要被
// 清掉，已有正文保持显示。
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

// 两枚刷新各发各的请求：正文那枚只打正文端点，评论那枚只打评论端点。
func TestDetailRefreshIsSplitBetweenBodyAndComments(t *testing.T) {
	var topicCalls, replyCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/replies"):
			replyCalls.Add(1)
			_, _ = io.WriteString(w, envelopeJSON(detailRepliesJSON))
		case strings.HasPrefix(r.URL.Path, "/api/v1/topics/"):
			topicCalls.Add(1)
			_, _ = io.WriteString(w, envelopeJSON(detailTopicJSON))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := detailAppWithReplies(t)
	a.autoLoad = true
	a.client = &Client{HTTP: srv.Client(), Base: srv.URL + "/api"}
	// 首帧的无限滚动不能自己续拉，否则计数会混进这次点击的请求。
	a.detail.replyNoMore = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if err := tt.Click("刷新"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return topicCalls.Load() == 1 }, "正文刷新没有发出正文请求")
	if replyCalls.Load() != 0 {
		t.Errorf("正文刷新不该重拉评论：%d 次", replyCalls.Load())
	}

	if err := tt.Click("刷新评论"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return replyCalls.Load() == 1 }, "评论刷新没有发出评论请求")
	if topicCalls.Load() != 1 {
		t.Errorf("评论刷新不该重拉正文：%d 次", topicCalls.Load())
	}
}

// 收起正文后，正文右下角那一列悬浮操作组要跟着正文面板一起隐藏：正文已经
// 不在画面上，再把它画到内容区右下角，正好压住评论区最下方评论的「回复」入口。
// 评论区自己的操作组（刷新 / 视图切换 / 回顶）嵌在输入栏发送右侧，仍留在原处，
// 展开正文后正文那组再回来。
func TestDetailBodyCollapseHidesBodyToolbar(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if _, ok := tt.Find("刷新"); !ok {
		t.Fatalf("展开态找不到正文操作组的刷新：%q", tt.Texts())
	}

	if err := tt.Click("收起正文"); err != nil {
		t.Fatalf("点收起正文失败：%v", err)
	}
	tt.Advance(500 * time.Millisecond)
	if !a.detail.bodyCollapsed {
		t.Fatalf("点了一下正文没有收起")
	}
	// 正文的工具按钮随面板一起收起，不再悬在评论区右下角。
	for _, label := range []string{"刷新", "复制帖子链接"} {
		if _, ok := tt.Find(label); ok {
			t.Errorf("正文收起后不该再画正文操作组的 %q：%q", label, tt.Texts())
		}
	}
	// 评论区自己的操作组与展开入口仍可用。
	if _, ok := tt.Find("刷新评论"); !ok {
		t.Errorf("正文收起后评论区刷新不该消失：%q", tt.Texts())
	}
	if _, ok := tt.Find("展开正文"); !ok {
		t.Errorf("正文收起后找不到展开入口：%q", tt.Texts())
	}

	if err := tt.Click("展开正文"); err != nil {
		t.Fatalf("点展开正文失败：%v", err)
	}
	tt.Advance(500 * time.Millisecond)
	if a.detail.bodyCollapsed {
		t.Fatalf("点了一下正文没有展开")
	}
	if _, ok := tt.Find("刷新"); !ok {
		t.Errorf("展开正文后正文操作组没有回来：%q", tt.Texts())
	}
}

// 正文那一栏自己滚动同样会露出正文操作组的「返回顶部」：它只把正文拉回
// 开头，不碰评论区。
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
	// 操作组不随正文滚动：刷新始终停在同一位置。
	if after, ok := tt.Find("刷新"); !ok || after != before {
		t.Errorf("滚动后操作组移动了：刷新 %+v → %+v", before, after)
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
