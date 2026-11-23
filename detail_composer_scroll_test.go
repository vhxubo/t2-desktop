package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 展开评论输入框会吃掉评论列表的高度：列表必须往上让出位置，否则最底下
// 那条评论会被长出来的编辑器盖住。停在末尾和停在列表中间都要成立。
func TestReplyComposerExpandPushesCommentsUp(t *testing.T) {
	cases := []struct {
		name   string
		scroll func(*detailState)
	}{
		{"atEnd", func(d *detailState) { d.list.ScrollToEnd() }},
		{"inMiddle", func(d *detailState) { d.list.ScrollTo(25, ui.Start) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, tt, rows := detailWithLongReplyList(t)
			tc.scroll(a.detail)
			for i := 0; i < 3; i++ {
				tt.Frame()
			}

			_, last := a.detail.list.Visible()
			if last < 0 || last >= len(rows) {
				t.Fatalf("前置条件：评论列表没有可见行：last=%d", last)
			}
			text := rows[last].Content
			before, ok := tt.Find(text)
			if !ok {
				t.Fatalf("展开前最底下那条评论没有渲染：%q", text)
			}
			assertCommentAboveComposer := func(tag string) {
				t.Helper()
				after, ok := tt.Find(text)
				if !ok {
					t.Fatalf("%s：最底下那条评论不见了：%q", tag, text)
				}
				if after.Y >= before.Y {
					t.Errorf("%s：评论没有向上让位：Y %.1f -> %.1f", tag, before.Y, after.Y)
				}
				// 这条评论必须完整露在编辑器上方，而不是被盖住一截。
				if after.H < 20 {
					t.Errorf("%s：最底下那条评论被裁掉了：%+v", tag, after)
				}
				outer, ok := tt.Find("评论输入栏")
				if !ok {
					t.Fatal("找不到评论输入栏")
				}
				if bottom := after.Y + after.H; bottom > outer.Y+1 {
					t.Errorf("%s：评论被编辑器遮住：评论底 %.1f，输入栏顶 %.1f", tag, bottom, outer.Y)
				}
			}

			area, ok := tt.Find("写评论")
			if !ok {
				t.Fatal("找不到评论输入框")
			}
			tt.ClickAt(area.X+area.W/2, area.Y+area.H/2)
			for i := 0; i < 3; i++ {
				tt.Frame()
			}
			assertCommentAboveComposer("展开后")
		})
	}
}

// 编辑器展开后继续长高（多写几行）时，底部评论也要跟着往上走，不能被
// 重新盖住。列表停在末尾时由编辑期的跟随保证。
func TestReplyComposerGrowthKeepsBottomComment(t *testing.T) {
	a, tt, rows := detailWithLongReplyList(t)
	a.detail.list.ScrollToEnd()
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	_, last := a.detail.list.Visible()
	text := rows[last].Content
	before, ok := tt.Find(text)
	if !ok {
		t.Fatalf("展开前最底下那条评论没有渲染：%q", text)
	}

	area, ok := tt.Find("写评论")
	if !ok {
		t.Fatal("找不到评论输入框")
	}
	tt.ClickAt(area.X+area.W/2, area.Y+area.H/2)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	a.detail.draft = strings.Repeat("一行字\n", 9) + "最后一行"
	for i := 0; i < 3; i++ {
		tt.Frame()
	}

	after, ok := tt.Find(text)
	if !ok {
		t.Fatalf("写多行后最底下那条评论不见了：%q", text)
	}
	if after.Y >= before.Y || after.H < 20 {
		t.Errorf("写多行后评论没留在编辑器上方：之前 %+v，之后 %+v", before, after)
	}
	outer, ok := tt.Find("评论输入栏")
	if !ok {
		t.Fatal("找不到评论输入栏")
	}
	if bottom := after.Y + after.H; bottom > outer.Y+1 {
		t.Errorf("写多行后评论被编辑器遮住：评论底 %.1f，输入栏顶 %.1f", bottom, outer.Y)
	}
}

// 点评论底行的「回复」入口也会展开输入框：replyTo 在构建列表行的时候才
// 写入，比 replyPane 算展开状态晚，这里守住这次展开同样把底部评论推上去。
func TestReplyButtonKeepsBottomCommentVisible(t *testing.T) {
	cases := []struct {
		name   string
		scroll func(*detailState)
	}{
		{"atEnd", func(d *detailState) { d.list.ScrollToEnd() }},
		{"inMiddle", func(d *detailState) { d.list.ScrollTo(25, ui.Start) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, tt, rows := detailWithLongReplyList(t)
			tc.scroll(a.detail)
			for i := 0; i < 3; i++ {
				tt.Frame()
			}

			_, last := a.detail.list.Visible()
			if last < 0 || last >= len(rows) {
				t.Fatalf("前置条件：评论列表没有可见行：last=%d", last)
			}
			text := rows[last].Content
			before, ok := tt.Find(text)
			if !ok {
				t.Fatalf("点击前最底下那条评论没有渲染：%q", text)
			}
			outer, ok := tt.Find("评论输入栏")
			if !ok {
				t.Fatal("找不到评论输入栏")
			}
			// 取视口里最靠下的那个「回复」入口：评论行可能被裁掉，列表
			// 之外也有构建出来的行，不能拿它们当落点。
			var x, y, w, h float32
			found := false
			for _, it := range tt.AccessAll() {
				if it.Name != "回复" || it.Bounds.Y+it.Bounds.H >= outer.Y {
					continue
				}
				if !found || it.Bounds.Y > y {
					x, y, w, h = it.Bounds.X, it.Bounds.Y, it.Bounds.W, it.Bounds.H
					found = true
				}
			}
			if !found {
				t.Fatal("评论区里找不到「回复」入口")
			}
			tt.ClickAt(x+w/2, y+h/2)
			for i := 0; i < 4; i++ {
				tt.Frame()
			}
			if a.detail.replyTo.ID == "" {
				t.Fatal("点「回复」后没有记下回复目标")
			}

			after, ok := tt.Find(text)
			if !ok {
				t.Fatalf("点「回复」后最底下那条评论不见了：%q", text)
			}
			if after.Y >= before.Y || after.H < 20 {
				t.Errorf("点「回复」后评论没往上让位：之前 %+v，之后 %+v", before, after)
			}
			outer, ok = tt.Find("评论输入栏")
			if !ok {
				t.Fatal("找不到评论输入栏")
			}
			if bottom := after.Y + after.H; bottom > outer.Y+1 {
				t.Errorf("点「回复」后评论被编辑器遮住：评论底 %.1f，输入栏顶 %.1f", bottom, outer.Y)
			}
		})
	}
}

// detailWithLongReplyList 造一屏装不下的评论列表，供上面几条布局测试共用。
func detailWithLongReplyList(t *testing.T) (*app, *ui.Tester, []replyItem) {
	t.Helper()
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
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	return a, tt, rows
}
