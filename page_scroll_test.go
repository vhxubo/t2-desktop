package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 稍后再看、浏览历史与评分榜都是主窗口内嵌页：列表必须拿到固定的可视高度
// 才能滚动。早先页面根节点只设了 FillWidth，列表按全部内容撑高，行直接画到
// 窗口外面，滚轮也失效。这里用长列表断言「首屏只看见头部、滚到底才看见尾巴」。
func assertEmbeddedPageScrolls(t *testing.T, a *app, head, tail string) {
	t.Helper()
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if _, ok := tt.Find(head); !ok {
		t.Fatalf("首屏没画出列表头部 %q：%q", head, tt.Texts())
	}
	if _, ok := tt.Find(tail); ok {
		t.Fatalf("没滚动就画出了列表末尾 %q：%q", tail, tt.Texts())
	}
	tt.Scroll(600, 500, 0, 4000)
	tt.Frame()
	if _, ok := tt.Find(tail); !ok {
		t.Fatalf("滚动后没画出列表末尾 %q：%q", tail, tt.Texts())
	}
	if _, ok := tt.Find(head); ok {
		t.Errorf("滚到底后头部 %q 还留在视野里：%q", head, tt.Texts())
	}
}

func TestWatchLaterPageScrolls(t *testing.T) {
	a := testApp(t)
	base := time.Now()
	for i := 0; i < 40; i++ {
		a.later = append(a.later, watchLaterRecord{
			ShortID: fmt.Sprintf("W%02d", i),
			Title:   fmt.Sprintf("待读第 %02d 项", i),
			AddedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	a.laterLoaded = true
	a.showWatchLater()
	assertEmbeddedPageScrolls(t, a, "待读第 00 项", "待读第 39 项")
}

func TestReadingHistoryPageScrolls(t *testing.T) {
	a := testApp(t)
	base := time.Now()
	for i := 0; i < 40; i++ {
		a.reading = append(a.reading, readingRecord{
			ShortID:  fmt.Sprintf("R%02d", i),
			Title:    fmt.Sprintf("浏览第 %02d 项", i),
			OpenedAt: base.Add(time.Duration(i) * time.Minute),
		})
	}
	a.readingLoaded = true
	a.laterTab = 1
	a.showWatchLater()
	assertEmbeddedPageScrolls(t, a, "浏览第 00 项", "浏览第 39 项")
}

func TestScoresPageScrolls(t *testing.T) {
	a := testApp(t)
	a.scores = newScoresState()
	st := a.scores
	st.loaded = true
	for i := 0; i < 40; i++ {
		st.items = append(st.items, scoreEntry{
			ID:         fmt.Sprintf("S%02d", i),
			Target:     fmt.Sprintf("评分第 %02d 项", i),
			Score:      8,
			ScoreKnown: true,
		})
	}
	a.showScores()
	assertEmbeddedPageScrolls(t, a, "评分第 00 项", "评分第 39 项")
}
