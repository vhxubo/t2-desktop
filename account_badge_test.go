package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

/* ------------------------------------------------------------------
   多账号：回复处与发布处写明当前账号
------------------------------------------------------------------ */

// multiAccountWindow 造一个挂了两个账号的账号册，并返回其中一个账号的窗口。
// active 故意停在另一个账号上：标签必须跟着这个窗口的令牌走，而不是账号册
// 里「上次用的那个」。
func multiAccountWindow(t *testing.T, name string) *app {
	t.Helper()
	b := testBook(t)
	acc := addTestAccount(t, b, name)
	other := addTestAccount(t, b, "bystander")
	b.setActive(other.ID)
	a := newAppWithDeps(b.deps(), acc)
	a.autoLoad = false
	return a
}

// singleAccountWindow 是同一套界面只挂一个账号时的样子：标签不该出现。
func singleAccountWindow(t *testing.T, name string) *app {
	t.Helper()
	b := testBook(t)
	acc := addTestAccount(t, b, name)
	a := newAppWithDeps(b.deps(), acc)
	a.autoLoad = false
	return a
}

// openTestDetail 给 app 摆上一篇带评论的帖子（与 detailAppWithReplies 同一套
// 夹具），评论输入框才会被画出来。
func openTestDetail(a *app) *app {
	a.openDetail(Item{ShortID: "JBYGf2A", URL: "https://2libra.com/post/work/JBYGf2A"})
	topic, _ := decodeTopic([]byte(detailTopicJSON), "JBYGf2A")
	a.detail.topic = &topic
	pg := decodeReplies([]byte(detailRepliesJSON))
	a.detail.replyList = flattenReplies(pg)
	a.detail.replyTotal = pg.Total
	a.detail.replyLoaded = true
	return a
}

// 发帖窗口的「发布」旁边写着当前账号；标签和「发布」在同一行、在它左侧，
// 不会盖住按钮。
func TestComposeFooterNamesCurrentAccount(t *testing.T) {
	a := multiAccountWindow(t, "jimmy")
	composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	badge, ok := tt.Find("当前账号 @jimmy")
	if !ok {
		t.Fatalf("发帖窗口没有写明当前账号：%q", tt.Texts())
	}
	publish, ok := tt.Find("发布")
	if !ok {
		t.Fatalf("找不到发布按钮：%q", tt.Texts())
	}
	if dy := badge.Y - publish.Y; dy > 10 || dy < -10 {
		t.Errorf("账号标签应与「发布」在同一行：Δy=%v", dy)
	}
	if badge.X >= publish.X {
		t.Errorf("账号标签应在「发布」左侧：badge=%+v publish=%+v", badge, publish)
	}
}

// 详情页的「当前账号」标签落在最底部的版权声明那一行、靠右：它原来挤在评论
// 输入栏里和发送按钮分宽度，现在输入栏整行留给输入。版权声明和原文链接在
// 左边自己折行，标签不压住它们。草稿搬进独立写评论窗口后，页脚仍把标签放在
// 发送按钮左侧（那边点发送就在页脚里）。
func TestReplyFooterNamesCurrentAccount(t *testing.T) {
	a := openTestDetail(multiAccountWindow(t, "jimmy"))
	tt := ui.NewTester(a.view, 1100, 900)
	tt.Frame()
	// 按无障碍名找整枚标签：标签右侧现在还有一枚「换账号」图标，文字本身
	// 不再贴着窗口右边（见 reply_identity.go）。
	badge, ok := tt.Find("当前账号 jimmy")
	if !ok {
		t.Fatalf("详情页底部没有写明当前账号：%q", tt.Texts())
	}
	// 标签在版权声明那一行：高度对得上、位置在它右边。
	copyright, ok := tt.Find("内容来自 2Libra 用户，版权归原作者所有。")
	if !ok {
		t.Fatalf("找不到底部的版权声明：%q", tt.Texts())
	}
	if dy := badge.Y - copyright.Y; dy > 10 || dy < -10 {
		t.Errorf("账号标签应与版权声明在同一行：Δy=%v", dy)
	}
	link, ok := tt.Find(truncate(a.detail.originURL(), 60))
	if !ok {
		t.Fatalf("找不到原文链接：%q", tt.Texts())
	}
	if badge.X < link.X+link.W {
		t.Errorf("账号标签应在版权声明与原文链接右侧：badge=%+v link=%+v", badge, link)
	}
	// 贴着窗口右下角，但留出与页面一致的内边距。
	if left := float32(1100) - (badge.X + badge.W); left < 0 || left > 32 {
		t.Errorf("账号标签应贴住右下角：右边距=%.1f", left)
	}
	// 输入栏里不再有账号标签：标签在评论框下面，发送按钮旁边只剩发送。
	field, ok := tt.Find("写评论")
	if !ok {
		t.Fatalf("找不到评论输入框：%q", tt.Texts())
	}
	if badge.Y <= field.Y+field.H {
		t.Errorf("账号标签应在评论输入框下面：badge=%+v field=%+v", badge, field)
	}
	send, ok := tt.Find("发送")
	if !ok {
		t.Fatalf("找不到发送按钮：%q", tt.Texts())
	}
	// 输入框铺到右端、发送按钮浮在它的右下角：账号标签也没有回来占位置。
	// 发送右侧还嵌着评论区操作组，所以它不必贴住输入框右边缘。
	if send.X >= field.X+field.W {
		t.Errorf("发送按钮应浮在输入框右下角：field=%+v send=%+v", field, send)
	}
	if gap := (field.X + field.W) - (send.X + send.W); gap < -6 || gap > replyComposerToolReserve+8 {
		t.Errorf("输入框应铺到右端：差 %.1f（field=%+v send=%+v）", gap, field, send)
	}

	a.openReplyWindow()
	wt := ui.NewTester(a.replyWindowView, replyWindowWidth, replyWindowHeight)
	wt.Frame()
	winBadge, ok := wt.Find("当前账号 jimmy")
	if !ok {
		t.Fatalf("独立写评论窗口没有写明当前账号：%q", wt.Texts())
	}
	winSend, ok := wt.Find("发送")
	if !ok {
		t.Fatalf("独立写评论窗口里找不到发送按钮：%q", wt.Texts())
	}
	if dy := winBadge.Y - winSend.Y; dy > 10 || dy < -10 {
		t.Errorf("标签应与发送按钮在同一行：Δy=%v", dy)
	}
	if winBadge.X >= winSend.X {
		t.Errorf("标签应在发送按钮左侧：badge=%+v send=%+v", winBadge, winSend)
	}
}

// 窄窗口下标签仍然贴右下角，版权声明留在左边、不会被标签盖住；昵称过长时
// 在标签内截断，不把整行撑出窗口。
func TestReplyFooterFitsAccountBadgeWhenNarrow(t *testing.T) {
	a := openTestDetail(multiAccountWindow(t, "a-very-long-account-name"))
	tt := ui.NewTester(a.view, 760, 800)
	tt.Frame()
	badge, ok := tt.Find("当前账号 a-very-long-account-name")
	if !ok {
		t.Fatalf("窄窗口下昵称应截断但仍在标签里：%q", tt.Texts())
	}
	copyright, ok := tt.Find("内容来自 2Libra 用户，版权归原作者所有。")
	if !ok {
		t.Fatalf("窄窗口下找不到版权声明：%q", tt.Texts())
	}
	// 版权声明留在左边，标签在它右边，两者不重叠。
	if badge.X < copyright.X+copyright.W {
		t.Errorf("窄窗口下账号标签压住版权声明：badge=%+v copyright=%+v", badge, copyright)
	}
	if left := float32(760) - (badge.X + badge.W); left < 0 || left > 32 {
		t.Errorf("窄窗口下账号标签应贴住右下角：右边距=%.1f", left)
	}
}

// 只挂一个账号时标题栏已经说明身份，这两处都不该多出一行标签。
func TestAccountBadgeHiddenForSingleAccount(t *testing.T) {
	a := singleAccountWindow(t, "jimmy")

	composeReady(a)
	ct := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	ct.Frame()
	if ct.HasText("当前账号 @") || ct.HasText("当前账号") {
		t.Fatalf("单账号发帖窗口不该出现账号标签：%q", ct.Texts())
	}

	openTestDetail(a)
	dt := ui.NewTester(a.view, 1100, 900)
	dt.Frame()
	if dt.HasText("当前账号 @") || dt.HasText("当前账号") {
		t.Fatalf("单账号详情页不该出现账号标签：%q", dt.Texts())
	}
}
