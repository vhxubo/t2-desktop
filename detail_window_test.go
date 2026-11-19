package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 无头测试里没有原生窗口：openDetailWindow 会把每扇窗口的状态摆好就返回，
// view 由 ui.NewTester 直接渲染。这样多开、合并、关一扇的记账都能断言。

func detailWindowsApp(t *testing.T) *app {
	t.Helper()
	a := testApp(t)
	m := newAuthManager(t.TempDir())
	_ = m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"})
	a.auth = m
	return a
}

func itemForWindow(id string) Item {
	return Item{ShortID: id, URL: "https://2libra.com/post/work/" + id, Title: "帖子 " + id}
}

func TestDetailWindowsOpenSeparately(t *testing.T) {
	a := detailWindowsApp(t)
	a.openDetailWindow(itemForWindow("POST001"))
	a.openDetailWindow(itemForWindow("POST002"))

	if got := a.detailWindowCount(); got != 2 {
		t.Fatalf("打开两篇应有两扇详情窗口，实际 %d", got)
	}
	h1, h2 := a.detailWindows[0], a.detailWindows[1]
	if h1 == h2 || h1.states[0] == h2.states[0] {
		t.Fatal("两扇窗口共用了状态")
	}
	if h1.states[0].host != h1 || h2.states[0].host != h2 {
		t.Fatal("detailState 没有记下自己的窗口")
	}
	if !a.detailLive(h1.states[0]) || !a.detailLive(h2.states[0]) {
		t.Fatal("两篇帖子都还活着，不该被对方的游标顶掉")
	}
	if h1.states[0].dead || h2.states[0].dead {
		t.Fatal("新开窗口不该把状态标死")
	}
	if a.detail != h2.states[0] {
		t.Fatal("最后打开的一篇应成为当前详情")
	}
}

func TestDetailWindowReopensSamePostInsteadOfDuplicating(t *testing.T) {
	a := detailWindowsApp(t)
	a.openDetailWindow(itemForWindow("POST001"))
	first := a.detailWindows[0]
	a.openDetailWindow(itemForWindow("POST001"))

	if got := a.detailWindowCount(); got != 1 {
		t.Fatalf("同一篇不该开第二扇窗口，实际 %d 扇", got)
	}
	if first.states[0] != a.detail {
		t.Fatal("重新打开同一篇应选中已有那篇")
	}
}

func TestDetailWindowsMergeIntoTabs(t *testing.T) {
	a := detailWindowsApp(t)
	a.openDetailWindow(itemForWindow("POST001"))
	a.openDetailWindow(itemForWindow("POST002"))
	a.openDetailWindow(itemForWindow("POST003"))
	// 合并发生在用户当时所在的那扇窗口（a.detail 指向它）。
	a.detail = a.detailWindows[0].states[0]
	kept := a.detailWindows[0]
	states := make([]*detailState, 0, 3)
	for _, h := range a.detailWindows {
		states = append(states, h.states[0])
	}

	a.mergeDetailWindows()

	if got := a.detailWindowCount(); got != 1 {
		t.Fatalf("合并后应只剩一扇窗口，实际 %d", got)
	}
	if a.detailWindows[0] != kept {
		t.Fatal("合并应留在当前窗口，而不是另开一扇")
	}
	if len(kept.states) != 3 || len(kept.stacks) != 3 {
		t.Fatalf("合并后应有 3 个标签，实际 %d 个 / %d 栈", len(kept.states), len(kept.stacks))
	}
	for i, d := range states {
		if kept.states[i] != d || d.host != kept {
			t.Fatalf("第 %d 篇没搬进合并窗口", i)
		}
		if !a.detailLive(d) {
			t.Fatalf("第 %d 篇合并后不该被判死", i)
		}
	}
	// 合并的结果报在留下来的那扇窗口。
	found := false
	for _, note := range a.toasts {
		if note.target == kept.toast {
			found = true
		}
	}
	if !found {
		t.Fatal("合并提示没有发到留下的详情窗口")
	}
}

func TestDetailWindowCloseOneDoesNotAffectOthers(t *testing.T) {
	a := detailWindowsApp(t)
	a.openDetailWindow(itemForWindow("POST001"))
	a.openDetailWindow(itemForWindow("POST002"))
	h1, h2 := a.detailWindows[0], a.detailWindows[1]
	d1, d2 := h1.states[0], h2.states[0]

	a.closeDetailWindow(h1)

	if got := a.detailWindowCount(); got != 1 || a.detailWindows[0] != h2 {
		t.Fatalf("关掉一扇不该影响另一扇，剩余 %d 扇", got)
	}
	if !h1.removed || !d1.dead || a.detailLive(d1) {
		t.Fatal("被关掉的那篇应标记为结束")
	}
	if h2.removed || d2.dead || !a.detailLive(d2) {
		t.Fatal("另一扇窗口和它的帖子不该受影响")
	}
	if a.detail != d2 {
		t.Fatal("当前详情应转到还开着的那一篇")
	}
}

func TestDetailMergedTabCloseKeepsWindowAndOtherTabs(t *testing.T) {
	a := detailWindowsApp(t)
	a.openDetailWindow(itemForWindow("POST001"))
	a.openDetailWindow(itemForWindow("POST002"))
	a.mergeDetailWindows()
	h := a.detailWindows[0]
	second := h.states[1]

	a.closeDetailTab(h, 0)

	if h.removed || a.detailWindowCount() != 1 {
		t.Fatal("关一个标签不该关掉整扇窗口")
	}
	if len(h.states) != 1 || h.states[0] != second || a.detail != second {
		t.Fatal("关掉第一个标签后应只剩第二篇")
	}
	if !a.detailLive(second) {
		t.Fatal("留下的标签应该还活着")
	}
}

func TestDetailWindowTitleAndTabsRender(t *testing.T) {
	a := detailWindowsApp(t)
	a.openDetailWindow(itemForWindow("POST001"))
	h := a.detailWindows[0]
	h.states[0].topic = &topicDetail{Title: "窗口里的帖子"}
	h.states[0].shortID = "POST001"

	tt := ui.NewTester(func(c *ui.Context) { a.detailWindowView(h, c) }, detailWindowWidth, detailWindowHeight)
	tt.Frame()
	for _, want := range []string{"详情", "窗口里的帖子", "关闭"} {
		if !tt.HasText(want) {
			t.Errorf("详情窗口标题栏缺少 %q：%q", want, tt.Texts())
		}
	}
	if _, ok := tt.Access("合并详情窗口"); !ok {
		t.Error("标题栏应有「合并详情窗口」按钮")
	}

	// 合并后标签栏出现，两个标题都能看到，当前标签能单独关闭。
	a.openDetailWindow(itemForWindow("POST002"))
	a.mergeDetailWindows()
	h = a.detailWindows[0]
	h.states[0].topic = &topicDetail{Title: "第一篇帖子"}
	h.states[1].topic = &topicDetail{Title: "第二篇帖子"}
	tt = ui.NewTester(func(c *ui.Context) { a.detailWindowView(h, c) }, detailWindowWidth, detailWindowHeight)
	tt.Frame()
	for _, want := range []string{"第一篇帖子", "第二篇帖子"} {
		if !tt.HasText(want) {
			t.Errorf("合并窗口缺少 %q：%q", want, tt.Texts())
		}
	}
	if _, ok := tt.Access("关闭当前标签"); !ok {
		t.Error("合并窗口应能单独关闭当前标签")
	}
}

// 工具条只剩一枚动作时，不再为它画一条悬浮栏：直接收进详情窗口的自定义
// 标题栏，和其它图标按钮同一排。
func TestDetailWindowLoneToolActionInTitleBar(t *testing.T) {
	a := detailWindowsApp(t)
	a.openDetailWindow(itemForWindow("POST001"))
	h := a.detailWindows[0]
	d := h.states[0]
	// 没有帖子地址就没有复制链接；非作者、未滚动，独立窗口的标题栏也已经有
	// 「关闭」，于是工具条只剩「刷新」这一枚。
	d.topic = nil
	d.fallbackURL = ""

	tt := ui.NewTester(func(c *ui.Context) { a.detailWindowView(h, c) }, detailWindowWidth, detailWindowHeight)
	tt.Frame()

	refresh, ok := tt.Access("刷新")
	if !ok {
		t.Fatalf("标题栏里应有唯一的刷新动作：%q", tt.Texts())
	}
	if refresh.Bounds.Y > titleBarHeight {
		t.Errorf("单枚动作应落在自定义标题栏内（Y<=%d），实际 %+v", titleBarHeight, refresh.Bounds)
	}
	count := 0
	for _, info := range tt.AccessAll() {
		if info.Name == "刷新" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("单枚动作只应画一次，实际 %d 个", count)
	}
}

// 详情窗口标题栏把窗口按钮套在一层居中、没定高的 Row 里：titleBarButton 过去
// 只设宽度、靠父行 Stretch 撑高，落到这里就塌成 0 高，关闭 / 最小化点不动。
// 按钮自己带高度后，必须真的占满标题栏并且能点到。
func TestDetailWindowTitleButtonsAreClickable(t *testing.T) {
	a := detailWindowsApp(t)
	a.openDetailWindow(itemForWindow("POST001"))
	h := a.detailWindows[0]
	h.states[0].topic = &topicDetail{Title: "窗口里的帖子"}

	tt := ui.NewTester(func(c *ui.Context) { a.detailWindowView(h, c) }, detailWindowWidth, detailWindowHeight)
	tt.Frame()
	for _, name := range []string{"最小化", "最大化", "关闭"} {
		info, ok := tt.Access(name)
		if !ok {
			t.Fatalf("标题栏缺少 %q 按钮：%q", name, tt.Texts())
		}
		if info.Bounds.H <= 0 || info.Bounds.Y < 0 || info.Bounds.Y+info.Bounds.H > titleBarHeight {
			t.Errorf("%q 按钮没有占满标题栏，实际 %+v", name, info.Bounds)
		}
	}
	// 点「关闭」应真的关掉这扇窗口；按钮塌成 0 高时这里点不到。
	if err := tt.Click("关闭"); err != nil {
		t.Fatalf("点不到标题栏的「关闭」按钮：%v", err)
	}
	tt.Frame()
	if !h.removed || a.detailWindowCount() != 0 {
		t.Fatal("点「关闭」后详情窗口应被关掉")
	}
}

func TestDetailWindowsSurviveMainWindowClose(t *testing.T) {
	a := detailWindowsApp(t)
	a.openDetailWindow(itemForWindow("POST001"))
	a.openDetailWindow(itemForWindow("POST002"))
	h1, h2 := a.detailWindows[0], a.detailWindows[1]

	// 模拟主窗口真的关掉：详情窗口不该被顺手关掉，账号也不该在此收尾。
	a.onMainWindowClosed()

	if !a.mainClosed {
		t.Fatal("应记下主窗口已关")
	}
	if got := a.detailWindowCount(); got != 2 {
		t.Fatalf("关主窗口不该关详情窗口，实际剩 %d 扇", got)
	}
	if h1.removed || h2.removed || h1.states[0].dead || h2.states[0].dead {
		t.Fatal("关主窗口不该把详情状态标死")
	}
	if !a.detailLive(h1.states[0]) || !a.detailLive(h2.states[0]) {
		t.Fatal("详情窗口里的帖子应继续可用")
	}
}

func TestDetailToastTargetsFollowWindow(t *testing.T) {
	a := detailWindowsApp(t)
	a.openDetailWindow(itemForWindow("POST001"))
	h := a.detailWindows[0]
	if a.detailToastFor(h.states[0]) != h.toast {
		t.Fatal("详情里的操作提示应落在自己那扇窗口")
	}
	if a.toastWindow(h.toast) != nil {
		t.Fatal("headless 没有原生窗口，退回目标应为空")
	}
	a.closeDetailWindow(h)
	if a.detailToast() != toastMain {
		t.Fatal("窗口关掉后提示应退回主窗口")
	}
}
