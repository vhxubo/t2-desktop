package main

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestSecondaryPagesUseFeedColumnWidth 确认稍后再看、浏览历史与评分榜沿用
// 首页正文栏的留白：同一窗口尺寸下，三页内容的左缘落在同一条列宽里，
// 宽窗口里不会被拉成一整条横带。
func TestSecondaryPagesUseFeedColumnWidth(t *testing.T) {
	const w, h = 1100, 760

	// 首页参照：列表行与首页共用 feedRowSurface，标题左缘 = feedInset + 12。
	home := testApp(t)
	home.tabs[0].items = []Item{{Key: "a", Title: "列宽参照行"}}
	home.tabs[0].loaded = true
	home.tabs[0].page = 1
	ht := ui.NewTester(home.view, w, h)
	ht.Frame()
	href, ok := ht.Find("列宽参照行")
	if !ok {
		t.Fatalf("首页参照行没渲染：%q", ht.Texts())
	}

	// 稍后再看：列表同样收进 feedInset，行表面也与首页一致。
	wl := testApp(t)
	wl.addWatchLater(Item{ShortID: "W1", Title: "稍后再看参照行"}, nil)
	wl.showWatchLater()
	wt := ui.NewTester(wl.view, w, h)
	wt.Frame()
	wref, ok := wt.Find("稍后再看参照行")
	if !ok {
		t.Fatalf("稍后再看参照行没渲染：%q", wt.Texts())
	}
	if d := math.Abs(float64(wref.X - href.X)); d > 2 {
		t.Errorf("稍后再看行左缘 %.0f，首页 %.0f，相差 %.0f", wref.X, href.X, d)
	}

	// 浏览历史复用同一套行表面，列宽也应一致。
	hl := testApp(t)
	hl.recordReading(Item{ShortID: "R1", Title: "浏览历史参照行"}, nil)
	hl.laterTab = 1
	hl.showWatchLater()
	lt := ui.NewTester(hl.view, w, h)
	lt.Frame()
	lref, ok := lt.Find("浏览历史参照行")
	if !ok {
		t.Fatalf("浏览历史参照行没渲染：%q", lt.Texts())
	}
	if d := math.Abs(float64(lref.X - href.X)); d > 2 {
		t.Errorf("浏览历史行左缘 %.0f，首页 %.0f，相差 %.0f", lref.X, href.X, d)
	}

	// 评分榜：筛选条外层吃掉 feedInset，内容内边距 14；Segmented 自己的
	// 按钮内边距还会再缩进一截，所以只要求落在首页列表左缘附近（相差
	// 不超过 20），关键是别贴着侧边栏铺满整屏。
	sc := testApp(t)
	sc.scores = newScoresState()
	st := sc.scores
	st.loaded = true
	sc.showScores()
	ct := ui.NewTester(sc.view, w, h)
	ct.Frame()
	// 取筛选条最左侧的「全部」：卡片内边距 14，和首页列表的 12 只差 2。
	first, ok := ct.Find("全部")
	if !ok {
		t.Fatalf("评分排序没有渲染：%q", ct.Texts())
	}
	if d := math.Abs(float64(first.X - href.X)); d > 20 {
		t.Errorf("评分榜筛选左缘 %.0f，首页列表左缘 %.0f，相差 %.0f", first.X, href.X, d)
	}
}
