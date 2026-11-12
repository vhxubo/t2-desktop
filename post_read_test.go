package main

import (
	"fmt"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 打开一篇帖子就在本机记下已读；重启（同一配置目录）后仍然算读过，别的
// 帖子不会跟着变成已读。
func TestPostReadMarksAndPersists(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false

	if a.isPostRead("P1") {
		t.Fatal("新账号不应已有已读帖子")
	}
	a.openDetail(Item{ShortID: "P1", URL: topicURL("P1", "")})
	if !a.isPostRead("P1") {
		t.Error("打开正文后没有记下已读")
	}
	if a.isPostRead("P2") {
		t.Error("没打开的帖子被误标为已读")
	}

	b := newApp(dir)
	b.autoLoad = false
	if !b.isPostRead("P1") {
		t.Error("重启后已读状态丢失")
	}
	if b.isPostRead("P2") {
		t.Error("重启后多出了没读过的帖子")
	}
}

// 已读状态按账号隔离：未登录时读的帖子不会算成登录账号的已读，退出后匿名
// 作用域的记录又回来。
func TestPostReadIsPerAccount(t *testing.T) {
	a := testApp(t)
	a.markPostRead("ANON")
	if !a.isPostRead("ANON") {
		t.Fatal("匿名阅读没有记下已读")
	}

	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok",
		TokenType:   "Bearer",
		Username:    "jimmy",
	}); err != nil {
		t.Fatal(err)
	}
	if a.isPostRead("ANON") {
		t.Error("登录后把匿名读过的帖子算给了当前账号")
	}
	a.markPostRead("JIMMY")
	if !a.isPostRead("JIMMY") {
		t.Error("登录账号的已读没有记下")
	}

	a.auth.Logout()
	if !a.isPostRead("ANON") {
		t.Error("退出后没有回到匿名作用域的已读状态")
	}
	if a.isPostRead("JIMMY") {
		t.Error("退出后仍能看到上个账号的已读状态")
	}
}

// 已读表超过上限时只保留最近读过的那些，内存与落盘都不无限增长。
func TestPostReadTrimsToNewest(t *testing.T) {
	a := testApp(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a.readAt = make(map[string]time.Time, postReadLimit+5)
	for i := 0; i < postReadLimit+5; i++ {
		a.readAt[fmt.Sprintf("T%04d", i)] = base.Add(time.Duration(i) * time.Minute)
	}
	a.trimPostReads()
	if len(a.readAt) != postReadLimit {
		t.Fatalf("裁剪后 %d 条，期望 %d 条", len(a.readAt), postReadLimit)
	}
	if _, ok := a.readAt["T0000"]; ok {
		t.Error("最早读过的记录没有被裁掉")
	}
	if _, ok := a.readAt[fmt.Sprintf("T%04d", postReadLimit+4)]; !ok {
		t.Error("最近读过的记录被裁掉了")
	}

	records := postReadRecords(a.readAt, 3)
	want := fmt.Sprintf("T%04d", postReadLimit+4)
	if len(records) != 3 || records[0].ShortID != want {
		t.Errorf("落盘记录顺序不对：%+v", records)
	}
}

// 列表里读过的帖子只把标题置灰：标题从正文色退到次要文字色，卡片底色、
// 副标题与未读行保持一致。
func TestReadPostRowIsGrayed(t *testing.T) {
	a := testApp(t)
	a.markPostRead("READROW")
	a.tabs[0].items = []Item{
		{Key: "UNREADROW", ShortID: "UNREADROW", Title: "还没读过的帖子", Subtitle: "职场 · 某人 · 刚刚"},
		{Key: "READROW", ShortID: "READROW", Title: "已经读过的帖子", Subtitle: "职场 · 某人 · 昨天"},
	}
	a.tabs[0].loaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	readR, ok := tt.Find("已经读过的帖子")
	if !ok {
		t.Fatalf("没有找到已读行：%q", tt.Texts())
	}
	unreadR, ok := tt.Find("还没读过的帖子")
	if !ok {
		t.Fatalf("没有找到未读行：%q", tt.Texts())
	}

	img := tt.Image()
	readInk := darkestLum(img, readR)
	unreadInk := darkestLum(img, unreadR)
	if readInk < unreadInk+40 {
		t.Errorf("已读标题没有置灰：亮度 %.0f，未读 %.0f", readInk, unreadInk)
	}

	// 标题左侧 8px、垂直居中处是卡片内边距：已读行与未读行的底色必须一样。
	bg := func(r ui.Rect) float64 {
		return colorLum(img.RGBAAt(int(r.X)-8, int(r.Y+r.H/2)))
	}
	if readBG, unreadBG := bg(readR), bg(unreadR); readBG != unreadBG {
		t.Errorf("只有标题该变灰，已读卡片底色却变了：%.0f vs %.0f", readBG, unreadBG)
	}
}

// 富行（动态 / 「我的」里的评论）只把帖子标题置灰：正文摘要与评论都是帖子
// 之外的新内容，不能因为帖子读过就一起变灰。
func TestRichRowsOnlyGrayReadPosts(t *testing.T) {
	a := testApp(t)
	a.markPostRead("P1")
	a.tabs[0].items = []Item{
		{Key: "R1", ShortID: "P1", Title: "时间线里读过的帖子",
			Extra: &rowExtra{Author: "某人", Excerpt: "读过的帖子摘要"}},
		{Key: "C1", ShortID: "P1", CommentID: "C1", Title: "一条新评论",
			Extra: &rowExtra{Author: "某人", Excerpt: "一条新评论"}},
		{Key: "U1", ShortID: "U1", Title: "时间线里没读的帖子",
			Extra: &rowExtra{Author: "某人", Excerpt: "没读的帖子摘要"}},
	}
	a.tabs[0].loaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	find := func(text string) ui.Rect {
		t.Helper()
		r, ok := tt.Find(text)
		if !ok {
			t.Fatalf("没有找到 %q：%q", text, tt.Texts())
		}
		return r
	}
	img := tt.Image()
	readPost := darkestLum(img, find("时间线里读过的帖子"))
	readBody := darkestLum(img, find("读过的帖子摘要"))
	unreadPost := darkestLum(img, find("时间线里没读的帖子"))
	comment := darkestLum(img, find("一条新评论"))
	if readPost < unreadPost+40 {
		t.Errorf("富行里读过的帖子没有置灰：亮度 %.0f，未读 %.0f", readPost, unreadPost)
	}
	if readBody > unreadPost+30 {
		t.Errorf("已读帖子只该把标题置灰，摘要也变灰了：亮度 %.0f", readBody)
	}
	if comment > unreadPost+30 {
		t.Errorf("评论行跟着帖子的已读一起变灰了：亮度 %.0f，未读帖子 %.0f", comment, unreadPost)
	}
}

// 从列表点开帖子也会走同一套已读逻辑，而不是只有直接调 openDetail 才记。
func TestClickPostRowMarksRead(t *testing.T) {
	a := testApp(t)
	a.tabs[0].items = []Item{
		{Key: "P1", ShortID: "P1", Title: "要打开的帖子", Subtitle: "职场 · 某人 · 刚刚"},
	}
	a.tabs[0].loaded = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("要打开的帖子"); err != nil {
		t.Fatalf("点击列表行失败：%v", err)
	}
	tt.Frame()
	if a.pageName != "detail" || a.detail == nil || a.detail.shortID != "P1" {
		t.Fatalf("点击后没有进入详情页：%s %+v", a.pageName, a.detail)
	}
	if !a.isPostRead("P1") {
		t.Error("点开列表行后没有记下已读")
	}
}

// darkestLum 返回矩形里最暗像素的亮度。标题文字的抗锯齿边缘不影响判断：
// 已读标题的「最暗」也比未读标题亮得多。
func darkestLum(im *image.RGBA, r ui.Rect) float64 {
	darkest := 255.0
	bounds := im.Bounds()
	for y := int(r.Y); y < int(r.Y+r.H); y++ {
		for x := int(r.X); x < int(r.X+r.W); x++ {
			if x < bounds.Min.X || y < bounds.Min.Y || x >= bounds.Max.X || y >= bounds.Max.Y {
				continue
			}
			darkest = min(darkest, colorLum(im.RGBAAt(x, y)))
		}
	}
	return darkest
}

func colorLum(c color.RGBA) float64 {
	return 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
}
