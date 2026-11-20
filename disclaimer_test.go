package main

import (
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestDisclaimerStoreShowsOnce(t *testing.T) {
	dir := t.TempDir()
	s := newDisclaimerStore(dir)
	if !s.claim() {
		t.Fatal("首次启动应认领声明弹窗")
	}
	if s.claim() {
		t.Fatal("第二个窗口不该再认领声明弹窗")
	}
	if err := s.markSeen(); err != nil {
		t.Fatal(err)
	}
	if s.claim() {
		t.Fatal("确认后不该再次弹出声明")
	}
	var cfg disclaimerConfig
	if err := readJSONFile(filepath.Join(dir, "disclaimer.json"), &cfg); err != nil {
		t.Fatalf("读不到声明确认记录：%v", err)
	}
	if !cfg.Seen {
		t.Error("disclaimer.json 没有记为已看过")
	}
	if !newDisclaimerStore(dir).seen {
		t.Error("重新启动后应读到已看过，不再弹声明")
	}
}

// 首次启动声明弹窗写明 T2 全称，点「我已了解」后关窗并落盘；下一帧不再画。
func TestFirstLaunchDisclaimerDialog(t *testing.T) {
	a := testApp(t)
	a.disclaimer = newDisclaimerStore(t.TempDir())
	a.disclaimerOpen = true
	tt := ui.NewTester(a.disclaimerDialog, 600, 420)
	tt.Frame()
	for _, want := range []string{"关于 T2", "Third Party 2Libra", "非官方桌面客户端", "我已了解"} {
		if !tt.HasText(want) {
			t.Errorf("首次启动声明弹窗缺少 %q：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("我已了解"); err != nil {
		t.Fatalf("点「我已了解」失败：%v（%q）", err, tt.Texts())
	}
	if a.disclaimerOpen {
		t.Error("确认后应关掉声明弹窗")
	}
	if !a.disclaimer.seen {
		t.Error("确认后应标记为已看过")
	}
	tt.Frame()
	if tt.HasText("关于 T2") {
		t.Error("确认后下一帧不该再画声明弹窗")
	}
}
