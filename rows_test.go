package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 最新评论行的字号阶梯：正文是这一行的阅读主体，必须比作者名和底部元信息
// 都大，否则一屏评论读起来全是同样大的小字，分不出「谁说的」和「说了什么」。
func TestLatestCommentRowFontHierarchy(t *testing.T) {
	a := testApp(t)
	const body = "这条评论写得够长，用来量出正文实际占的高度"
	it := itemFrom([]byte(`{"id":"c1","floor":7,"content":"`+body+`",`+
		`"author":{"username":"dannyng"},`+
		`"post":{"short_id":"S1","title":"帖子标题","node":{"slug":"n","name":"节点"}}}`), kindComment)
	a.tab = 4
	a.tabs[4].loaded = true
	a.tabs[4].items = []Item{it}
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	box := func(s string) ui.Rect {
		t.Helper()
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("找不到 %q：%q", s, tt.Texts())
		}
		return r
	}
	bodyBox := box(body)
	authorBox := box("@dannyng")
	metaBox := box("原帖：「帖子标题」")
	if bodyBox.H <= authorBox.H {
		t.Errorf("评论正文字号没有大于作者名：%.1f <= %.1f", bodyBox.H, authorBox.H)
	}
	if bodyBox.H <= metaBox.H {
		t.Errorf("评论正文字号没有大于元信息：%.1f <= %.1f", bodyBox.H, metaBox.H)
	}
	if authorBox.H <= metaBox.H {
		t.Errorf("作者名没有比元信息大一档：%.1f <= %.1f", authorBox.H, metaBox.H)
	}
	// 不只是「相对更大」：正文本身也要够大。15px 配 1.55 行高约 23 DIP，
	// 掉回 13px 时代只有 17 左右，那时整行就是一片小字。
	if bodyBox.H < 22 {
		t.Errorf("评论正文的行高只有 %.1f，字号还是偏小", bodyBox.H)
	}
}

// 评论行底部的「原帖」是独立入口：点它直接看帖子本身（从顶部开始，不定位
// 楼层），点这一行的其他地方仍然进到这条评论。两者不能互相连带——只点一次
// 原帖不该把详情页压进返回栈，说明外层行的点击没有被一起触发。
func TestCommentRowPostLinkOpensPostItself(t *testing.T) {
	const raw = `{"id":"c1","floor":12,"content":"这条评论本身",` +
		`"author":{"username":"dannyng"},` +
		`"post":{"short_id":"S1","title":"帖子标题","node":{"slug":"n","name":"节点"}}}`
	render := func() (*app, *ui.Tester) {
		a := testApp(t)
		a.tab = 4
		a.tabs[4].loaded = true
		a.tabs[4].items = []Item{itemFrom([]byte(raw), kindComment)}
		tt := ui.NewTester(a.view, 1100, 760)
		tt.Frame()
		return a, tt
	}

	a, tt := render()
	if err := tt.Click("原帖：「帖子标题」"); err != nil {
		t.Fatalf("点原帖失败：%v", err)
	}
	tt.Frame()
	if a.pageName != "detail" || a.detail == nil {
		t.Fatalf("点原帖没有打开详情：%q", a.pageName)
	}
	if a.detail.shortID != "S1" {
		t.Errorf("打开的不是原帖：%q", a.detail.shortID)
	}
	if a.detail.focusID != "" || a.detail.focusPending {
		t.Errorf("点原帖不该定位评论：%q %v", a.detail.focusID, a.detail.focusPending)
	}
	if len(a.detailStack) != 0 {
		t.Errorf("点原帖连带触发了整行的点击：返回栈 %d 层", len(a.detailStack))
	}

	// 点正文还是进到这条评论：整行的点击没有被「原帖」抢走。
	b, tt2 := render()
	if err := tt2.Click("这条评论本身"); err != nil {
		t.Fatalf("点正文失败：%v", err)
	}
	tt2.Frame()
	if b.detail == nil || b.detail.focusID != "c1" {
		t.Fatalf("点正文应定位到评论：%+v", b.detail)
	}
}
