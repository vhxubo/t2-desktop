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
		t.Errorf("首页入口数量 = %d，期望 0", got)
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

// 窗口：两页都在，行里画标题、节点、作者与数量；点一行打开正文并按默认
// 偏好从列表移除。
func TestWatchLaterWindowRendersAndRemovesOnOpen(t *testing.T) {
	a := testApp(t)
	a.addWatchLater(Item{
		ShortID: "P1", Title: "稍后要读的帖子", Author: "jimmy",
		SubtitlePrefix: "职场 · ",
	}, nil)
	a.addWatchLater(Item{ShortID: "P2", Title: "第二篇待读"}, nil)

	tt := ui.NewTester(a.watchLaterWindowView, watchLaterWindowWidth, watchLaterWindowHeight)
	tt.Frame()
	for _, want := range []string{
		"稍后再看", "浏览历史", "稍后要读的帖子", "第二篇待读", "共 2 条", "jimmy",
	} {
		if !tt.HasText(want) {
			t.Errorf("稍后再看窗口缺少 %q：%q", want, tt.Texts())
		}
	}

	if err := tt.Click("稍后要读的帖子"); err != nil {
		t.Fatalf("点击待读行失败：%v", err)
	}
	tt.Frame()
	if a.inWatchLater("P1") {
		t.Error("从窗口点开后没有从稍后再看移除")
	}
	if !tt.HasText("共 1 条") {
		t.Errorf("列表没有按移除后的数量重建：%q", tt.Texts())
	}
}

// 保留模式下窗口区分已读与未读：未读带「未读」标签，读过的带「已读」且
// 标题置灰。
func TestWatchLaterWindowMarksRead(t *testing.T) {
	a := testApp(t)
	if err := a.watchLaterPrefs.setKeepAfterOpen(true); err != nil {
		t.Fatal(err)
	}
	a.addWatchLater(Item{ShortID: "P1", Title: "读过的帖子"}, nil)
	a.addWatchLater(Item{ShortID: "P2", Title: "还没读的帖子"}, nil)
	a.handleWatchLaterOpen("P1")

	tt := ui.NewTester(a.watchLaterWindowView, watchLaterWindowWidth, watchLaterWindowHeight)
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
func TestReadingHistoryTabRenders(t *testing.T) {
	a := testApp(t)
	a.recordReading(Item{ShortID: "R1", Title: "浏览过的帖子", Author: "amy"}, nil)
	a.laterTab = 1

	tt := ui.NewTester(a.watchLaterWindowView, watchLaterWindowWidth, watchLaterWindowHeight)
	tt.Frame()
	for _, want := range []string{"浏览历史", "浏览过的帖子", "amy", "共 1 条"} {
		if !tt.HasText(want) {
			t.Errorf("浏览历史页缺少 %q：%q", want, tt.Texts())
		}
	}
	if _, ok := tt.Access("清空浏览历史"); !ok {
		t.Errorf("浏览历史页没有清空入口：%q", tt.Texts())
	}
}

// 首页页头有「稍后再看」入口，有待读条目时带数量；没有内容时页头也留着。
func TestHomeWatchLaterEntry(t *testing.T) {
	a := testApp(t)
	a.tabs[0].loaded = true
	a.tabs[0].noMore = true // 只有一页：别让无限滚动在测试里发起续拉
	a.tabs[0].items = []Item{{Key: "P1", ShortID: "P1", Title: "首页帖子"}}

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("稍后再看") {
		t.Fatalf("首页没有稍后再看入口：%q", tt.Texts())
	}

	a.addWatchLater(Item{ShortID: "P9", Title: "待读"}, nil)
	tt.Frame()
	if !tt.HasText("稍后再看 1") {
		t.Errorf("入口没有带待读数量：%q", tt.Texts())
	}

	// 列表空掉时入口仍在，否则没有新帖时反而进不去待读清单。
	a.tabs[0].items = nil
	tt.Frame()
	if !tt.HasText("稍后再看") {
		t.Errorf("首页空状态下入口消失了：%q", tt.Texts())
	}
}

// 详情页操作组里那枚图标跟着状态切换：加入后变成移出。
func TestDetailWatchLaterActionToggles(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "P1", Title: "正在读的帖子", URL: topicURL("P1", "")})
	d := a.detail
	if d == nil {
		t.Fatal("没有打开详情")
	}

	find := func() detailToolAction {
		for _, act := range a.detailToolActions(d) {
			if act.key == "later" {
				return act
			}
		}
		t.Fatal("详情操作组里没有稍后再看")
		return detailToolAction{}
	}
	act := find()
	if act.label != "加入稍后再看" {
		t.Errorf("未加入时文案 = %q", act.label)
	}
	act.onClick()
	if !a.inWatchLater("P1") {
		t.Error("点击后没有加入稍后再看")
	}
	if got := find().label; got != "移出稍后再看" {
		t.Errorf("已加入时文案 = %q", got)
	}
	find().onClick()
	if a.inWatchLater("P1") {
		t.Error("再点一次没有移出")
	}
}

// 设置窗口「阅读」页：开关可见、点击立即落盘；关掉时把已读标记清掉。
func TestReadingSettingsKeepAfterOpen(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false
	a.settingsTab = settingsTabReading

	tt := ui.NewTester(a.settingsWindowView, settingsWindowWidth, settingsWindowHeight)
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
