package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestScoreRowsAreFlat 确认评分行不画圆角描边卡片：行左缘的像素和页面背景
// 一致（描边卡片在这里会有一条 1px 深色边框），与首页等列表页的平面行表面
// 保持同一套视觉。用无头渲染的真实像素锁住这一约定。
func TestScoreRowsAreFlat(t *testing.T) {
	a := testApp(t)
	a.scores = newScoresState()
	st := a.scores
	st.loaded = true
	st.items = decodeScores([]byte(scoresJSON)).Items
	a.showScores()
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	row, ok := tt.Find("评分 罗小黑战记")
	if !ok {
		t.Fatalf("评分行没渲染：%q", tt.Texts())
	}
	im := tt.Image()
	midY := int(row.Y + row.H/2)
	// 左侧内边距处与行外同高的页面背景应一致：旧的描边卡片会在这里画一条
	// 1px 边框（实测灰 238 对背景白 255），平面透明表面则与背景同色。
	bg := im.RGBAAt(int(row.X)-2, midY)
	for dx := 0; dx <= 3; dx++ {
		got := im.RGBAAt(int(row.X)+dx, midY)
		if d := rgbDistance(got, bg); d > 12 {
			t.Errorf("评分行左缘 dx=%d 与页面背景不一致：行内 %v / 背景 %v", dx, got, bg)
		}
	}
}
