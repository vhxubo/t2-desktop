package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 加入稍后再看会置顶，重复加入只保留一条；重启（同一配置目录）后还在。
func TestWatchLaterAddPersists(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false

	a.addWatchLater(Item{ShortID: "P1", Title: "第一篇"}, nil)
	a.addWatchLater(Item{ShortID: "P2", Title: "第二篇"}, nil)
	if got := len(a.later); got != 2 {
		t.Fatalf("稍后再看 %d 条，期望 2 条", got)
	}
	if a.later[0].ShortID != "P2" {
		t.Errorf("新加入的没有置顶：%+v", a.later)
	}
	// 重复加入不新增条目，只是提到最前面。
	a.addWatchLater(Item{ShortID: "P1", Title: "第一篇"}, nil)
	if got := len(a.later); got != 2 || a.later[0].ShortID != "P1" {
		t.Fatalf("重复加入处理不对：%+v", a.later)
	}
	if !a.inWatchLater("P1") || a.inWatchLater("P3") {
		t.Error("inWatchLater 判断不对")
	}

	b := newApp(dir)
	b.autoLoad = false
	if !b.inWatchLater("P1") || !b.inWatchLater("P2") || len(b.later) != 2 {
		t.Errorf("重启后稍后再看丢失：%+v", b.later)
	}

	b.removeWatchLater("P1")
	if b.inWatchLater("P1") || len(b.later) != 1 {
		t.Errorf("移除没有生效：%+v", b.later)
	}
}

// 稍后再看按账号隔离：匿名攒的条目不会出现在登录账号里，退出后又回来。
func TestWatchLaterIsPerAccount(t *testing.T) {
	a := testApp(t)
	a.addWatchLater(Item{ShortID: "ANON", Title: "匿名时加入的"}, nil)
	if !a.inWatchLater("ANON") {
		t.Fatal("匿名加入没有记下")
	}

	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok",
		TokenType:   "Bearer",
		Username:    "jimmy",
	}); err != nil {
		t.Fatal(err)
	}
	if a.inWatchLater("ANON") {
		t.Error("登录后看到了匿名账号的稍后再看")
	}
	a.addWatchLater(Item{ShortID: "JIMMY", Title: "账号里加入的"}, nil)

	a.auth.Logout()
	if !a.inWatchLater("ANON") {
		t.Error("退出后没有回到匿名账号的稍后再看")
	}
	if a.inWatchLater("JIMMY") {
		t.Error("退出后仍能看到上个账号的稍后再看")
	}
}

// 默认偏好：打开一篇稍后再看的帖子就把它从列表移除。
func TestWatchLaterOpenRemovesByDefault(t *testing.T) {
	a := testApp(t)
	if a.watchLaterPrefs.keepAfterOpen() {
		t.Fatal("默认不应保留打开过的条目")
	}
	a.addWatchLater(Item{ShortID: "P1", Title: "等会儿读"}, nil)
	a.openDetail(Item{ShortID: "P1", URL: topicURL("P1", "")})
	if a.inWatchLater("P1") {
		t.Error("默认偏好下打开后仍留在稍后再看")
	}
	if got := a.watchLaterCount(); got != 0 {
		t.Errorf("侧边栏角标数量 = %d，期望 0", got)
	}
}

// 打开「保留」后：打开过的条目留在列表里并标为已读，数量只数未读，未读
// 排在已读前面。
func TestWatchLaterOpenKeepsAndMarksRead(t *testing.T) {
	a := testApp(t)
	if err := a.watchLaterPrefs.setKeepAfterOpen(true); err != nil {
		t.Fatal(err)
	}
	a.addWatchLater(Item{ShortID: "P1", Title: "第一篇"}, nil)
	a.addWatchLater(Item{ShortID: "P2", Title: "第二篇"}, nil)
	a.openDetail(Item{ShortID: "P1", URL: topicURL("P1", "")})

	if !a.inWatchLater("P1") {
		t.Fatal("保留偏好下打开后条目被移除了")
	}
	if a.watchLaterCount() != 1 {
		t.Errorf("未读数 = %d，期望 1（P2）", a.watchLaterCount())
	}
	// P1 已读，应排到未读的 P2 后面。
	if len(a.later) != 2 || a.later[0].ShortID != "P2" || a.later[1].ReadAt.IsZero() {
		t.Fatalf("已读 / 未读排序不对：%+v", a.later)
	}

	// 关掉保留后清掉已读标记，恢复成普通队列。
	if err := a.watchLaterPrefs.setKeepAfterOpen(false); err != nil {
		t.Fatal(err)
	}
	a.clearWatchLaterReadMarks()
	for _, r := range a.later {
		if !r.ReadAt.IsZero() {
			t.Errorf("关闭保留后仍留着已读标记：%+v", r)
		}
	}
}

// 清空只清当前账号，不碰另一个账号的清单。
func TestWatchLaterClearOnlyCurrentAccount(t *testing.T) {
	a := testApp(t)
	a.addWatchLater(Item{ShortID: "ANON", Title: "匿名"}, nil)

	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: "jimmy",
	}); err != nil {
		t.Fatal(err)
	}
	a.addWatchLater(Item{ShortID: "JIMMY", Title: "jimmy 的"}, nil)
	a.clearWatchLater()
	if len(a.later) != 0 {
		t.Fatalf("清空后仍有 %d 条", len(a.later))
	}

	a.auth.Logout()
	if !a.inWatchLater("ANON") {
		t.Error("清空 jimmy 的清单时把匿名账号的记录也删了")
	}
}

// 内嵌页的操作结果走主窗口 toast，不再单独认领一种 toastTarget：页面和
// 草稿箱一样没有自己的窗口，提示得画在唯一那扇主窗口里。
func TestWatchLaterToastUsesMainWindow(t *testing.T) {
	a := testApp(t)
	a.addWatchLater(Item{ShortID: "P1", Title: "待读"}, nil)
	a.clearWatchLater()
	if len(a.toasts) != 1 || a.toasts[0].target != toastMain {
		t.Fatalf("清空结果没有走主窗口 toast：%+v", a.toasts)
	}
}

// 页面：两段都在，行里画标题、节点、作者与数量；点一行打开正文并按默认
// 偏好从列表移除。页面和草稿箱一样内嵌在主窗口，不新开 mygo 窗口。
func TestWatchLaterPageRendersAndRemovesOnOpen(t *testing.T) {
	a := testApp(t)
	a.addWatchLater(Item{
		ShortID: "P1", Title: "稍后要读的帖子", Author: "jimmy",
		SubtitlePrefix: "职场 · ",
	}, nil)
	a.addWatchLater(Item{ShortID: "P2", Title: "第二篇待读"}, nil)

	a.showWatchLater()
	if a.pageName != "watchlater" || a.navSel != "watchlater" {
		t.Fatalf("没进稍后再看页：page=%q nav=%q", a.pageName, a.navSel)
	}
	if a.win != nil {
		t.Fatal("进稍后再看页不该新开窗口")
	}
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{
		"稍后再看", "浏览历史", "稍后要读的帖子", "第二篇待读", "共 2 条", "jimmy",
	} {
		if !tt.HasText(want) {
			t.Errorf("稍后再看页缺少 %q：%q", want, tt.Texts())
		}
	}

	if err := tt.Click("稍后要读的帖子"); err != nil {
		t.Fatalf("点击待读行失败：%v", err)
	}
	// 无头路径下详情页占了主窗口，回到稍后再看页再看列表。
	a.showWatchLater()
	tt.Frame()
	if a.inWatchLater("P1") {
		t.Error("从页面点开后没有从稍后再看移除")
	}
	if !tt.HasText("共 1 条") {
		t.Errorf("列表没有按移除后的数量重建：%q", tt.Texts())
	}
}

// 稍后再看页的分段和评分榜排序条用同一套 Segmented：点「浏览历史」能切段，
// 列表随之换成阅读记录。分段绑定持久状态，切换由 lastLaterTab 接住并复位滚动。
func TestWatchLaterTabSwitchWithSegmented(t *testing.T) {
	a := testApp(t)
	a.addWatchLater(Item{ShortID: "P1", Title: "稍后要读的帖子"}, nil)
	a.recordReading(Item{ShortID: "R1", Title: "浏览过的帖子"}, nil)
	a.showWatchLater()

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("稍后要读的帖子") {
		t.Fatalf("初始段没渲染：%q", tt.Texts())
	}

	if err := tt.Click("浏览历史"); err != nil {
		t.Fatalf("切换分段失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if a.laterTab != 1 {
		t.Fatalf("点浏览历史后 laterTab=%d，期望 1", a.laterTab)
	}
	if a.lastLaterTab != 1 {
		t.Errorf("lastLaterTab 没跟上分段：%d", a.lastLaterTab)
	}
	if !tt.HasText("浏览过的帖子") || tt.HasText("稍后要读的帖子") {
		t.Errorf("分段切换后列表没换：%q", tt.Texts())
	}
}

// 保留模式下页面区分已读与未读：未读带「未读」标签，读过的带「已读」且
// 标题置灰。
func TestWatchLaterPageMarksRead(t *testing.T) {
	a := testApp(t)
	if err := a.watchLaterPrefs.setKeepAfterOpen(true); err != nil {
		t.Fatal(err)
	}
	a.addWatchLater(Item{ShortID: "P1", Title: "读过的帖子"}, nil)
	a.addWatchLater(Item{ShortID: "P2", Title: "还没读的帖子"}, nil)
	a.handleWatchLaterOpen("P1")

	a.showWatchLater()
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("已读") {
		t.Errorf("保留模式下没有已读标签：%q", tt.Texts())
	}
	if !tt.HasText("未读") {
		t.Errorf("保留模式下没有未读标签：%q", tt.Texts())
	}
	if !tt.HasText("读过的帖子") || !tt.HasText("还没读的帖子") {
		t.Errorf("两条记录都应显示：%q", tt.Texts())
	}
	if !tt.HasText("共 2 条 · 1 未读") {
		t.Errorf("保留模式下没有写出未读数：%q", tt.Texts())
	}
}

// 浏览历史页复用阅读记录：打开过的帖子列在这里，清空入口按当前账号清。
// 它本身不是待读清单，行尾不显示「已缓存正文」，悬浮也不挂加入稍后再看按钮。
func TestReadingHistoryTabRenders(t *testing.T) {
	a := testApp(t)
	a.recordReading(Item{ShortID: "R1", Title: "浏览过的帖子", Author: "amy"}, nil)
	a.saveCachedDetail(&detailState{shortID: "R1", topic: &topicDetail{
		ShortID: "R1", Title: "浏览过的帖子",
	}})
	a.laterTab = 1
	a.showWatchLater()

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"浏览历史", "浏览过的帖子", "amy", "共 1 条"} {
		if !tt.HasText(want) {
			t.Errorf("浏览历史页缺少 %q：%q", want, tt.Texts())
		}
	}
	if tt.HasText("已缓存正文") {
		t.Errorf("浏览历史行不该写「已缓存正文」：%q", tt.Texts())
	}
	if _, ok := tt.Access("清空浏览历史"); !ok {
		t.Errorf("浏览历史页没有清空入口：%q", tt.Texts())
	}

	// 悬浮整行也不能露出加入稍后再看的按钮。
	title, ok := tt.Find("浏览过的帖子")
	if !ok {
		t.Fatalf("找不到历史行标题：%q", tt.Texts())
	}
	tt.Move(title.X+title.W/2, title.Y+title.H/2)
	tt.Frame()
	if _, ok := tt.Find("加入稍后再看"); ok {
		t.Errorf("浏览历史行不该有加入稍后再看按钮：%q", tt.Texts())
	}
}

// 稍后再看入口收进侧边栏，和草稿箱一上一下成组：待读数走 Lobe 红点角标，
// 折叠成图标轨与展开态都展示同一个数字；点它切到主窗口里的双段页面。
func TestSidebarWatchLaterEntry(t *testing.T) {
	a := testApp(t)

	items := a.sidebarNavItems()
	idx := -1
	for i, it := range items {
		if it.Key == "watchlater" {
			idx = i
		}
	}
	if idx < 2 || items[idx].Label != "稍后再看" || items[idx-1].Key != "drafts" ||
		!items[idx-2].Divider || !items[idx+1].Divider {
		t.Fatalf("稍后再看没和草稿箱成组：%+v", items)
	}
	if got := a.navBadgeCount("watchlater"); got != 0 {
		t.Fatalf("空清单不该有红点计数：%d", got)
	}

	checkDot := func(expanded bool, want string) {
		t.Helper()
		// 上一轮点过入口后停在这一页；回到列表再量侧边栏，免得页头里的
		// 同名标题抢走「稍后再看」这一行。
		a.pageName = "list"
		a.navSel = "t0"
		a.navExpanded = expanded
		tt := ui.NewTester(a.view, 1100, 760)
		tt.Frame()
		row, ok := tt.Find("稍后再看")
		if !ok {
			t.Fatalf("展开=%v 时侧边栏没有稍后再看：%q", expanded, tt.Texts())
		}
		dot, ok := tt.Find(want)
		if !ok {
			t.Fatalf("展开=%v 时没有红点计数 %q：%q", expanded, want, tt.Texts())
		}
		if dot.Y < row.Y-20 || dot.Y > row.Y+row.H+20 {
			t.Errorf("展开=%v 时红点 y=%.0f 不在稍后再看行 y=%.0f 附近", expanded, dot.Y, row.Y)
		}
		if err := tt.Click("稍后再看"); err != nil {
			t.Fatalf("展开=%v 时点侧边栏入口失败：%v", expanded, err)
		}
	}

	a.addWatchLater(Item{ShortID: "P9", Title: "待读"}, nil)
	if got := a.navBadgeCount("watchlater"); got != 1 {
		t.Fatalf("待读计数 = %d，期望 1", got)
	}
	checkDot(true, "1")
	checkDot(false, "1")
}

// 悬浮露出的稍后再看按钮紧跟发帖时间放在副标题这一行，不独占最右侧那一列；
// 回复数仍在行尾。
func TestFeedRowWatchLaterButtonAfterTime(t *testing.T) {
	a := testApp(t)
	a.tabs[0].loaded = true
	a.tabs[0].noMore = true
	a.tabs[0].items = []Item{{
		Key: "P1", ShortID: "P1", Title: "列表第一篇", Author: "jimmy",
		SubtitleSuffix: " · 3 小时前", Stats: &replyStats{Replies: 12},
	}}

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	// 悬浮动作：平时不可见，指针落到行上才露出。
	if _, ok := tt.Find("加入稍后再看"); ok {
		t.Fatal("未悬浮时不该有稍后再看按钮")
	}
	title, ok := tt.Find("列表第一篇")
	if !ok {
		t.Fatalf("找不到文章标题：%q", tt.Texts())
	}
	whenBefore, ok := tt.Find(" · 3 小时前")
	if !ok {
		t.Fatalf("找不到发布时间：%q", tt.Texts())
	}
	tt.Move(title.X+title.W/2, title.Y+title.H/2)
	tt.Frame()

	when, ok := tt.Find(" · 3 小时前")
	if !ok {
		t.Fatalf("悬浮后找不到发布时间：%q", tt.Texts())
	}
	// 按钮固定占住同一格位置，只切换可见性；时间不能因为悬浮而位移，
	// 否则整行重排会让指针下的元素来回变化，视觉上就是抖动。
	if when.X != whenBefore.X || when.Y != whenBefore.Y {
		t.Errorf("悬浮前后发布时间从 (%.1f,%.1f) 跑到 (%.1f,%.1f)",
			whenBefore.X, whenBefore.Y, when.X, when.Y)
	}
	btn, ok := tt.Find("加入稍后再看")
	if !ok {
		t.Fatalf("悬浮后没有加入按钮：%q", tt.Texts())
	}
	count, ok := tt.Find("12")
	if !ok {
		t.Fatalf("行尾没有回复数：%q", tt.Texts())
	}

	centers := func(r ui.Rect) float32 { return r.Y + r.H/2 }
	if d := centers(btn) - centers(when); d > 1.5 || d < -1.5 {
		t.Errorf("按钮中线 %.1f 没和时间中线 %.1f 对齐（差 %.1f）",
			centers(btn), centers(when), d)
	}
	if gap := btn.X - (when.X + when.W); gap < feedWatchLaterGap-1 || gap > feedWatchLaterGap+1 {
		t.Errorf("按钮与时间的间距 %.1f，期望 %.1f：时间右缘 %.1f，按钮左缘 %.1f",
			gap, feedWatchLaterGap, when.X+when.W, btn.X)
	}
	if btn.X >= count.X {
		t.Errorf("按钮 x=%.1f 不该跑到行尾回复数 x=%.1f 的位置", btn.X, count.X)
	}
}

// 主列表文章行悬浮时露出带文案的稍后再看动作：未加入时是「稍后再看」，点一下
// 加入；已加入时换成「移出稍后再看」，再点一下移除。两种状态都不打开正文。
func TestFeedRowWatchLaterButton(t *testing.T) {
	a := testApp(t)
	a.tabs[0].loaded = true
	a.tabs[0].noMore = true
	a.tabs[0].items = []Item{
		{Key: "P1", ShortID: "P1", Title: "列表第一篇", Author: "jimmy", SubtitleSuffix: " · 3 小时前"},
		{Key: "P2", ShortID: "P2", Title: "列表第二篇", Author: "jimmy", SubtitleSuffix: " · 5 小时前"},
	}

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	title, ok := tt.Find("列表第一篇")
	if !ok {
		t.Fatalf("找不到文章标题：%q", tt.Texts())
	}
	tt.Move(title.X+title.W/2, title.Y+title.H/2)
	tt.Frame()
	btn, ok := tt.Find("加入稍后再看")
	if !ok {
		t.Fatalf("悬浮后文章行没有加入稍后再看按钮：%q", tt.Texts())
	}
	// 侧边栏已有一项「稍后再看」，行内文案是第二处。
	withText := 0
	for _, s := range tt.Texts() {
		if s == "稍后再看" {
			withText++
		}
	}
	if withText < 2 {
		t.Errorf("图标旁没有「稍后再看」文案：%q", tt.Texts())
	}
	tt.ClickAt(btn.X+btn.W/2, btn.Y+btn.H/2)
	tt.Frame()
	if !a.inWatchLater("P1") {
		t.Error("点文章行的按钮没有把这篇加入稍后再看")
	}
	if a.detail != nil {
		t.Error("点加入按钮不该打开正文")
	}
	if len(a.later) != 1 {
		t.Fatalf("稍后再看应有 1 条，实际 %d", len(a.later))
	}

	// 已入清单后图标与文案切成移出动作，再点一次会移除。
	again, ok := tt.Find("移出稍后再看")
	if !ok {
		t.Fatalf("加入后按钮没换成移出状态：%q", tt.Texts())
	}
	tt.ClickAt(again.X+again.W/2, again.Y+again.H/2)
	tt.Frame()
	if a.inWatchLater("P1") {
		t.Error("再点一次没有移出稍后再看")
	}
	if len(a.later) != 0 {
		t.Errorf("移出后清单应为空，实际 %+v", a.later)
	}
}

// 稍后再看只在主窗口信息流的行里出现：同一篇帖子放到「我的」、搜索、详情页
// 都不该再挂这枚按钮，免得用户从一个入口加、又从另一个入口看到两套状态。
func TestWatchLaterOnlyOnMainFeed(t *testing.T) {
	it := Item{Key: "WL1", ShortID: "WL1", Title: "待读测试帖", Author: "jimmy",
		SubtitleSuffix: " · 刚刚"}

	// 主窗口信息流的行：悬浮后有按钮。
	a := testApp(t)
	a.tabs[0].loaded = true
	a.tabs[0].noMore = true
	a.tabs[0].items = []Item{it}
	mt := ui.NewTester(a.view, 1100, 760)
	mt.Frame()
	title, ok := mt.Find("待读测试帖")
	if !ok {
		t.Fatalf("主列表没渲染出来：%q", mt.Texts())
	}
	mt.Move(title.X+title.W/2, title.Y+title.H/2)
	mt.Frame()
	if _, ok := mt.Find("加入稍后再看"); !ok {
		t.Errorf("主列表悬浮后应有稍后再看按钮：%q", mt.Texts())
	}

	// 「我的」窗口的同一行：悬浮也不露出按钮。
	b := profileWindowApp(t)
	b.profile.tab = profileTabContent
	b.profile.contentKind = profileContentTopics
	bst := &b.profile.content[profileContentTopics]
	bst.loaded, bst.items = true, []Item{it}
	pt := ui.NewTester(b.profileWindowView, profileWindowWidth, profileWindowHeight)
	pt.Frame()
	ptitle, ok := pt.Find("待读测试帖")
	if !ok {
		t.Fatalf("「我的」窗口没渲染出来：%q", pt.Texts())
	}
	pt.Move(ptitle.X+ptitle.W/2, ptitle.Y+ptitle.H/2)
	pt.Frame()
	if _, ok := pt.Find("加入稍后再看"); ok {
		t.Errorf("「我的」窗口不该有稍后再看按钮：%q", pt.Texts())
	}

	// 搜索窗口的同一行：同样不挂按钮。
	c := testApp(t)
	c.showSearchWindow()
	cs := c.search
	cs.q, cs.kind, cs.loaded = "待读", int(searchPosts), true
	cs.hits = []searchHit{{kind: searchPosts, item: it}}
	ct := ui.NewTester(c.searchWindowView, 620, 680)
	ct.Frame()
	ctitle, ok := ct.Find("待读测试帖")
	if !ok {
		t.Fatalf("搜索窗口没渲染出来：%q", ct.Texts())
	}
	ct.Move(ctitle.X+ctitle.W/2, ctitle.Y+ctitle.H/2)
	ct.Frame()
	if _, ok := ct.Find("加入稍后再看"); ok {
		t.Errorf("搜索窗口不该有稍后再看按钮：%q", ct.Texts())
	}

	// 详情页操作组里也不再有稍后再看。
	d := testApp(t)
	d.openDetail(it)
	for _, act := range d.detailToolActions(d.detail) {
		if act.key == "later" {
			t.Error("详情页不该有稍后再看动作")
		}
	}
}

// 设置窗口「阅读」页：开关可见、点击立即落盘；关掉时把已读标记清掉。
func TestReadingSettingsKeepAfterOpen(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false
	a.settingsTab = settingsTabReading

	// 阅读记录在「阅读与评论」页里排在评论下方，给足高度让开关与按钮可见。
	tt := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1000)
	tt.Frame()
	for _, want := range []string{"阅读记录", "打开后保留在稍后再看", "打开稍后再看"} {
		if !tt.HasText(want) {
			t.Errorf("阅读设置页缺少 %q：%q", want, tt.Texts())
		}
	}

	if err := tt.Click("打开后保留在稍后再看"); err != nil {
		t.Fatalf("点击保留开关失败：%v", err)
	}
	tt.Frame()
	if !a.watchLaterPrefs.keepAfterOpen() {
		t.Fatal("点击后没有打开保留开关")
	}
	// 落盘：换一个实例读同一份配置。
	if !newWatchLaterPrefsStore(dir).keepAfterOpen() {
		t.Error("保留开关没有落盘")
	}

	// 保留模式留下的已读标记在关掉开关后清掉。
	a.addWatchLater(Item{ShortID: "P1", Title: "读过的"}, nil)
	a.handleWatchLaterOpen("P1")
	if a.later[0].ReadAt.IsZero() {
		t.Fatal("保留模式下没有标记已读")
	}
	if err := tt.Click("打开后保留在稍后再看"); err != nil {
		t.Fatalf("再次点击保留开关失败：%v", err)
	}
	if a.watchLaterPrefs.keepAfterOpen() {
		t.Fatal("再次点击后开关仍是打开的")
	}
	if !a.later[0].ReadAt.IsZero() {
		t.Errorf("关掉保留后仍留着已读标记：%+v", a.later[0])
	}
}
