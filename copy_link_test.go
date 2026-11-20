package main

import (
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

/* ------------------------------------------------------------------
   详情页：复制帖子与评论的链接
------------------------------------------------------------------ */

// 复制链接在详情页右下角的竖排操作组里：一枚图标按钮，Lobe 的 ActionIcon
// 把无障碍名同时当作 tooltip。点一下，剪贴板里就是这一帖的地址，结果用主
// 窗口 toast 报告。
func TestDetailCopyPostLink(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1200, 900)
	tt.Frame()

	info, ok := tt.Access("复制帖子链接")
	if !ok {
		t.Fatalf("详情页工具条里没有复制链接：%q", tt.Texts())
	}
	if info.Description != "复制帖子链接" {
		t.Errorf("复制链接的说明不对：%q", info.Description)
	}
	// 按钮落在正文区右下角，和操作组里的其它图标按钮排成一列。
	if close, ok := tt.Find("关闭详情"); ok && absInt(int(info.Bounds.X-close.X)) > 2 {
		t.Errorf("复制链接应与操作组其它按钮同一列：%+v vs %+v", info.Bounds, close)
	}
	if err := tt.Click("复制帖子链接"); err != nil {
		t.Fatalf("点复制链接失败：%v", err)
	}
	// 写入结果在下一帧之前才回来：先让窗口领走排队的 toast。
	tt.Frame()
	if want := a.detail.topic.URL; tt.Clipboard() != want {
		t.Fatalf("剪贴板里应是帖子链接 %q，实际 %q", want, tt.Clipboard())
	}
	if !tt.HasText("已复制链接") {
		t.Errorf("复制成功没有用 toast 报告：%q", tt.Texts())
	}
	if got := tt.OpenedURLs(); len(got) != 0 {
		t.Errorf("复制链接不该打开浏览器：%q", got)
	}
}

// 每条评论的底行各有一枚「复制链接」：复制的是那条评论自己的永久链接（与
// 楼层号点开的一致），不会复制成帖子地址，也不会两条评论共用一个链接。
func TestReplyCopyLinkIsPerComment(t *testing.T) {
	a := detailAppWithReplies(t)
	// 只留两条评论：第一条一楼，第二条是楼中楼（没有楼层号，走 /post-flat/）。
	a.detail.replyList = []replyItem{
		{ID: "c1", Author: "dannyng", Floor: 1, Content: "第一楼"},
		{ID: "c2", Author: "jimmy", FlatFloor: 32, Content: "楼中楼"},
	}
	a.detail.replyTotal = 2
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1200, 900)
	tt.Frame()

	info, ok := tt.Access("复制评论链接")
	if !ok {
		t.Fatalf("评论底行没有复制链接入口：%q", tt.Texts())
	}
	if info.Description != "把这条评论的链接复制到剪贴板" {
		t.Errorf("复制评论链接的说明不对：%q", info.Description)
	}
	// 两行评论各有一枚入口，各自落在自己那条评论里：楼层号在头部，
	// 复制入口在底行，隔着正文的几行字。
	first, ok := tt.Find("1 楼")
	if !ok {
		t.Fatalf("找不到一楼的楼层号：%q", tt.Texts())
	}
	if dy := info.Bounds.Y - first.Y; dy > 120 || dy < 0 {
		t.Errorf("复制入口应在一楼这条评论自己的底行：%+v vs 楼层号 %+v", info.Bounds, first)
	}

	if err := tt.Click("复制评论链接"); err != nil {
		t.Fatalf("点复制评论链接失败：%v", err)
	}
	tt.Frame()
	want := replyPermalink(a.detail.topic, a.detail.fallbackURL, a.detail.replyList[0])
	if got := tt.Clipboard(); got != want {
		t.Fatalf("剪贴板里应是第一条评论的链接 %q，实际 %q", want, got)
	}
	if want == "" || want == a.detail.topic.URL {
		t.Fatalf("评论链接不该与帖子链接相同：%q", want)
	}
	if !tt.HasText("已复制评论链接") {
		t.Errorf("复制成功没有用 toast 报告：%q", tt.Texts())
	}

	// 第二条评论的入口在它自己的底行，复制出来的是它自己的链接。
	if r, ok := tt.Find("@jimmy"); ok {
		tt.ClickAt(r.X+r.W/2, r.Y+r.H/2+28)
		tt.Frame()
		tt.Click("复制评论链接")
		tt.Frame()
		want = replyPermalink(a.detail.topic, a.detail.fallbackURL, a.detail.replyList[1])
		if got := tt.Clipboard(); got != want {
			t.Fatalf("第二条评论复制出来的是 %q，期望 %q", got, want)
		}
	}
}

// 已删除的评论没有可用的链接，底行不出复制入口；连帖子地址都没有时，
// 工具条里也不出现复制按钮（不给一个复制空字符串的按钮）。
func TestCopyLinkHiddenWithoutTarget(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "JBYGf2A"}) // 故意不带 URL
	a.detail.replyList = []replyItem{
		{ID: "c2", Author: "gone", Floor: 2, Deleted: true, Content: "这条已经删了"},
	}
	a.detail.replyTotal = 1
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1200, 900)
	tt.Frame()
	if tt.HasText("复制帖子链接") {
		t.Errorf("没有帖子地址时不该出现复制链接：%q", tt.Texts())
	}
	if tt.HasText("复制评论链接") {
		t.Errorf("已删除的评论不该有复制链接：%q", tt.Texts())
	}
}

// 写剪贴板失败（别的程序正占着剪贴板）时不能悄悄失败：toast 说明复制没成功，
// 并指出还能手动复制；剪贴板里也不该被写进半截内容。
func TestCopyLinkReportsClipboardFailure(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1200, 900)
	tt.Frame()

	tt.FailClipboard(errors.New("另一个程序正占着剪贴板"))
	if err := tt.Click("复制帖子链接"); err != nil {
		t.Fatalf("点复制链接失败：%v", err)
	}
	tt.Frame()
	if !tt.HasText("复制失败：") || !tt.HasText("可以手动选中链接复制") {
		t.Fatalf("复制失败没有说明原因与退路：%q", tt.Texts())
	}
	if got := tt.Clipboard(); got != "" {
		t.Errorf("失败时不该往剪贴板写内容：%q", got)
	}
	// 和别的 toast 一样，几秒后自己消失，不留在页面上。
	tt.Advance(6 * time.Second)
	if tt.HasText("复制失败：") {
		t.Errorf("复制失败的提示没有自己消失：%q", tt.Texts())
	}
}
