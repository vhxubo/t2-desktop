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

// 首次启动声明弹窗写明 T2 全称，点「继续使用」后关窗并落盘；下一帧不再画。
func TestFirstLaunchDisclaimerDialog(t *testing.T) {
	a := testApp(t)
	a.disclaimer = newDisclaimerStore(t.TempDir())
	a.disclaimerOpen = true
	tt := ui.NewTester(a.disclaimerDialog, 600, 420)
	tt.Frame()
	for _, want := range []string{"关于 T2", "Third Party 2Libra", "非官方桌面客户端", "基于开放 API 开发", "与开发者无关", "如果继续使用", "退出", "继续使用"} {
		if !tt.HasText(want) {
			t.Errorf("首次启动声明弹窗缺少 %q：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("继续使用"); err != nil {
		t.Fatalf("点「继续使用」失败：%v（%q）", err, tt.Texts())
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

// 「关于」窗口的「重看声明」只在这个窗口里弹同样的正文：不动 seen，
// 也不会把主窗口的 disclaimerOpen 置位。
func TestAboutReplayDisclaimer(t *testing.T) {
	a := testApp(t)
	a.disclaimer = newDisclaimerStore(t.TempDir())
	tt := ui.NewTester(a.aboutWindowView, aboutWindowWidth, aboutWindowHeight)
	tt.Frame()

	if !tt.HasText("重看声明") {
		t.Fatalf("关于窗口没有重看声明入口：%q", tt.Texts())
	}
	if err := tt.Click("重看声明"); err != nil {
		t.Fatalf("点「重看声明」失败：%v（%q）", err, tt.Texts())
	}
	if !a.aboutDisclaimerOpen {
		t.Fatal("点重看后应打开声明弹窗")
	}
	if a.disclaimerOpen {
		t.Error("重看不该让主窗口也画声明弹窗")
	}

	tt.Frame()
	for _, want := range []string{"关于 T2", "Third Party 2Libra", "非官方桌面客户端", "基于开放 API 开发", "与开发者无关", "如果继续使用", "退出", "继续使用"} {
		if !tt.HasText(want) {
			t.Errorf("重看声明弹窗缺少 %q：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("继续使用"); err != nil {
		t.Fatalf("点「继续使用」失败：%v（%q）", err, tt.Texts())
	}
	if a.aboutDisclaimerOpen {
		t.Error("确认后应关掉重看弹窗")
	}
	if a.disclaimer.seen {
		t.Error("重看不该改动首次启动的已看过记录")
	}
	tt.Frame()
	if tt.HasText("关于 T2") {
		t.Error("确认后下一帧不该再画重看弹窗")
	}
}

// 声明弹窗的「退出」按钮走 a.quitApp，与托盘、系统菜单同一条退出流程；
// 点它不算认可，不写 seen，下次启动仍会看到声明。
func TestDisclaimerExitButton(t *testing.T) {
	a := testApp(t)
	a.disclaimer = newDisclaimerStore(t.TempDir())
	a.disclaimerOpen = true
	quits := 0
	a.quitApp = func() { quits++ }
	tt := ui.NewTester(a.disclaimerDialog, 600, 420)
	tt.Frame()
	if err := tt.Click("退出"); err != nil {
		t.Fatalf("点「退出」失败：%v（%q）", err, tt.Texts())
	}
	if quits != 1 {
		t.Errorf("点「退出」应调用 quitApp 一次，实际 %d", quits)
	}
	if a.disclaimer.seen {
		t.Error("退出不该记成已看过")
	}
	if !a.disclaimerOpen {
		t.Error("退出时弹窗应保持，交给退出流程关窗")
	}
}

// 「关于」窗口重看声明时的「退出」同样退出整个应用，也不动 seen。
func TestAboutDisclaimerExitButton(t *testing.T) {
	a := testApp(t)
	a.disclaimer = newDisclaimerStore(t.TempDir())
	a.aboutDisclaimerOpen = true
	quits := 0
	a.quitApp = func() { quits++ }
	tt := ui.NewTester(a.aboutWindowView, aboutWindowWidth, aboutWindowHeight)
	tt.Frame()
	if err := tt.Click("退出"); err != nil {
		t.Fatalf("点「退出」失败：%v（%q）", err, tt.Texts())
	}
	if quits != 1 {
		t.Errorf("点「退出」应调用 quitApp 一次，实际 %d", quits)
	}
	if a.disclaimer.seen {
		t.Error("重看里的退出不该记成已看过")
	}
}
