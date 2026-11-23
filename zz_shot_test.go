package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func saveShot(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := filepath.Join(".tmp-shots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name+".png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestZZShotFoldBtn(t *testing.T) {
	a := detailAppWithReplies(t)
	a.detail.draft = "展开态工具条右端是收起输入框按钮。"
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	r, _ := tt.Find("写评论")
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	saveShot(t, tt, "07-fold-btn")

	// 点收起：输入框折回一行，右下角回到折叠态布局。
	if err := tt.Click("收起输入框"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		tt.Frame()
	}
	saveShot(t, tt, "08-folded")
}