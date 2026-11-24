package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/field"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 选项很多时，弹层会被 mygo 压到窗口剩余高度；下拉必须让选项在里面滚动，
// 而不是溢出面板、画到窗口外。这里用 End 跳到最后一项验证它仍在窗口里。
func TestLobeSelectLongListStaysInWindow(t *testing.T) {
	options := make([]field.SelectOption, 40)
	for i := range options {
		options[i] = field.SelectOption{
			Value: fmt.Sprintf("Option %02d", i), Label: fmt.Sprintf("Option %02d", i),
		}
	}
	last := options[len(options)-1].Label
	selected := options[0].Value
	const winH = 320
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Padding(140, 20).Children(func() {
			lobeSelect(c, &selected, field.SelectProps{Options: options, Size: lobetheme.Small}).
				Label("Choice").Width(150)
		})
	}, 460, winH)
	tt.Frame()
	if err := tt.Click("Choice"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnd)
	tt.Frame()
	r, ok := tt.Find(last)
	if !ok {
		t.Fatalf("下拉最后一项 %q 不在画面里", last)
	}
	if r.Y < 0 || r.Y+r.H > winH-4 {
		t.Errorf("最后一项画到窗口外：%q 位于 %+v", last, r)
	}
}

// 金币窗口按最小尺寸打开时，流水页的「金币类型」下拉也必须完整落在窗口里。
func TestCoinHistoryReasonDropdownFitsWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, envelopeJSON(`{"items":[],"total":0,"page":1,"limit":20,"total_pages":1}`))
	}))
	defer srv.Close()
	a := coinTestClient(t, srv)
	a.coins.view = coinViewHistory
	a.coins.baseLoaded = true
	a.coins.historyLoaded = true

	tt := ui.NewTester(a.coinWindowView, coinWinMinWidth, coinWinMinHeight)
	tt.Frame()
	if err := tt.Click("金币类型"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyEnd)
	tt.Frame()
	last := coinReasonOptions[len(coinReasonOptions)-1]
	r, ok := tt.Find(last)
	if !ok {
		t.Fatalf("类型下拉最后一项 %q 不在画面里", last)
	}
	if r.Y < 0 || r.Y+r.H > coinWinMinHeight-4 {
		t.Errorf("类型下拉最后一项画到窗口外：%q 位于 %+v", last, r)
	}
}
