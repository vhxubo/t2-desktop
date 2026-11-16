package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

/* ------------------------------------------------------------------
   多账号：回复输入框与发布处写明当前账号
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

// 评论输入框的发送按钮旁边写着当前账号；草稿搬进独立写评论窗口后，标签
// 跟着走到那边的发送按钮旁。
func TestReplyInputNamesCurrentAccount(t *testing.T) {
	a := openTestDetail(multiAccountWindow(t, "jimmy"))
	tt := ui.NewTester(a.view, 1100, 900)
	tt.Frame()
	badge, ok := tt.Find("当前账号 @jimmy")
	if !ok {
		t.Fatalf("评论输入框没有写明当前账号：%q", tt.Texts())
	}
	send, ok := tt.Find("发送")
	if !ok {
		t.Fatalf("找不到发送按钮：%q", tt.Texts())
	}
	if dy := badge.Y - send.Y; dy > 10 || dy < -10 {
		t.Errorf("账号标签应与发送按钮在同一行：Δy=%v", dy)
	}
	if badge.X >= send.X {
		t.Errorf("账号标签应在发送按钮左侧：badge=%+v send=%+v", badge, send)
	}

	a.openReplyWindow()
	wt := ui.NewTester(a.replyWindowView, replyWindowWidth, replyWindowHeight)
	wt.Frame()
	winBadge, ok := wt.Find("当前账号 @jimmy")
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
		t.Fatalf("单账号评论输入框不该出现账号标签：%q", dt.Texts())
	}
}
