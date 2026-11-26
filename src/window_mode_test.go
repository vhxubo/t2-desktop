package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 默认每次询问：头一次关闭主窗口会弹出「退出还是收进托盘」，不替用户猜。
// 选择写进 window.json，下次启动沿用；坏值退回询问，绝不留一个藏起来就点
// 不回来的窗口。
func TestCloseModeDefaultsToAskAndPersists(t *testing.T) {
	dir := t.TempDir()
	s := newCloseModeStore(dir)
	if s.mode != closeAsk {
		t.Fatalf("缺省模式 = %d，期望每次询问", s.mode)
	}
	if err := s.Save(closeToTray); err != nil {
		t.Fatal(err)
	}

	var cfg windowConfig
	if err := readJSONFile(filepath.Join(dir, "window.json"), &cfg); err != nil {
		t.Fatalf("window.json 没有写出来：%v", err)
	}
	if cfg.Close != "tray" {
		t.Errorf("保存的关闭行为 = %q，期望 tray", cfg.Close)
	}
	if got := newCloseModeStore(dir).mode; got != closeToTray {
		t.Errorf("重启后模式 = %d，期望收进托盘", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "window.json"), []byte(`{"close":"hide"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := newCloseModeStore(dir).mode; got != closeAsk {
		t.Errorf("无法识别的关闭行为 = %d，期望退回每次询问", got)
	}
	// 旧版本写下的 quit 就是「关窗口即退出」，合并后仍要认作退出应用。
	if err := os.WriteFile(filepath.Join(dir, "window.json"), []byte(`{"close":"quit"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := newCloseModeStore(dir).mode; got != closeApp {
		t.Errorf("旧版 quit 配置应映射到退出应用，得到 %d", got)
	}
}

// 只有「选了收进托盘 + 托盘真的建出来了 + 不是正在退出」三个条件同时成立
// 才藏窗口：托盘没建出来时藏起来就再也点不回来，退出流程里拦一下就退不掉。
func TestHideOnCloseNeedsTrayAndNoQuit(t *testing.T) {
	a := testApp(t)
	a.closeMode.mode = closeToTray
	a.trayReady = false
	if a.hideOnClose() {
		t.Fatal("托盘不可用时不该收进托盘")
	}
	a.trayReady = true
	if !a.hideOnClose() {
		t.Fatal("托盘可用、已选收进托盘时应拦截关闭")
	}
	a.quitting = true
	if a.hideOnClose() {
		t.Fatal("正在退出时不该把退出拦成收进托盘")
	}
	a.quitting = false
	a.closeMode.mode = closeApp
	if a.hideOnClose() {
		t.Fatal("选了退出应用时不该拦截关闭")
	}
}

// 生产里 app 比托盘先建好：托盘要等应用就绪（startTray）才创建，退出时还会
// 销毁。关闭行为必须实时查进程级状态，不能拿构建时的快照 —— 否则 app 里的
// 托盘标记永远是 false，「收进托盘」按下去窗口直接关掉，等于这个选项失效。
func TestCloseBehaviourUsesLiveDesktopTrayState(t *testing.T) {
	b := testBook(t)
	a := newAppWithDeps(b.deps(), nil)
	b.app = a

	// 构建 app 时托盘还没建出来：两种模式都按「没有托盘」处理。
	a.closeMode.mode = closeToTray
	if a.hideOnClose() {
		t.Fatal("托盘还没建出来，不该收进托盘")
	}
	early := &mygo.CloseEvent{}
	a.onMainWindowClose(early)
	if early.DefaultPrevented() {
		t.Fatal("托盘未就绪时关闭不该被拦下")
	}

	// 模拟 startTray 成功：只改进程级状态，app 侧不做任何重新赋值。
	b.trayReady = true
	if !a.hideOnClose() {
		t.Fatal("托盘就绪后应直接收进托盘")
	}
	next := &mygo.CloseEvent{}
	a.onMainWindowClose(next)
	if !next.DefaultPrevented() {
		t.Fatal("托盘就绪后关主窗口应被拦下收进托盘")
	}
	if a.closePromptOpen {
		t.Error("已记住收进托盘，不该再弹提示")
	}

	// 「每次询问」同样跟着托盘的实时状态走。
	a.closeMode.mode = closeAsk
	b.trayReady = false
	if a.askOnClose() {
		t.Fatal("托盘未就绪时不该询问")
	}
	b.trayReady = true
	if !a.askOnClose() {
		t.Fatal("托盘就绪后关主窗口该先问一句")
	}
}

// 收起主窗口只藏主窗口本体：每扇弹窗都是独立的 mygo 窗口，不跟着关；
// 只有托盘「关闭全部」才统一收掉。原生隐藏换成记录调用，不碰主循环。
func TestHideMainWindowToTrayKeepsOtherWindows(t *testing.T) {
	a := testApp(t)
	mainWindow := new(mygo.Window)
	popup := new(mygo.Window)
	a.win = mainWindow
	a.notifWin = popup
	a.settingsWin = popup
	a.composeWin = popup

	old := hideNativeWindow
	var hidden []*mygo.Window
	hideNativeWindow = func(w *mygo.Window) { hidden = append(hidden, w) }
	t.Cleanup(func() { hideNativeWindow = old })

	a.hideMainWindowToTray()

	if len(hidden) != 1 || hidden[0] != mainWindow {
		t.Fatalf("只该收起主窗口，实际隐藏了 %d 扇", len(hidden))
	}
	if a.notifWin != popup || a.settingsWin != popup || a.composeWin != popup {
		t.Fatal("收起主窗口不该关掉其它窗口")
	}
}

// 没记住选择时，关主窗口会被拦下来换成询问；托盘不可用就直接关，因为
// 「收进托盘」这个选项根本不成立。
func TestMainWindowCloseAsksOnceUntilRemembered(t *testing.T) {
	a := testApp(t)
	a.trayReady = true

	e := &mygo.CloseEvent{}
	a.onMainWindowClose(e)
	if !e.DefaultPrevented() {
		t.Fatal("没记住选择时关闭应被拦下并询问")
	}
	if !a.closePromptOpen {
		t.Fatal("没有弹出关闭提示")
	}

	// 记住「收进托盘」：弹窗关闭、配置落盘，之后的关闭直接收起窗口，不再问。
	a.closeRemember = true
	a.chooseCloseMode(closeToTray)
	if a.closePromptOpen {
		t.Fatal("做出选择后弹窗应关闭")
	}
	if a.closeMode.mode != closeToTray {
		t.Fatalf("记住的选择 = %d，期望收进托盘", a.closeMode.mode)
	}
	next := &mygo.CloseEvent{}
	a.onMainWindowClose(next)
	if !next.DefaultPrevented() {
		t.Fatal("记住收进托盘后，关闭应直接收起窗口")
	}
	if a.closePromptOpen {
		t.Fatal("记住之后不该再弹提示")
	}

	// 记住「退出应用」：之后的关闭直接走退出流程。
	a.closeMode.mode = closeApp
	quits := 0
	a.quitApp = func() { quits++ }
	quit := &mygo.CloseEvent{}
	a.onMainWindowClose(quit)
	if !quit.DefaultPrevented() || quits != 1 {
		t.Fatalf("记住退出应用后，关闭应拦下并退出，实际 quits=%d", quits)
	}

	// 退出流程里的关闭一律放行，否则应用退不掉。
	a.closeMode.mode = closeToTray
	a.quitting = true
	final := &mygo.CloseEvent{}
	a.onMainWindowClose(final)
	if final.DefaultPrevented() {
		t.Fatal("退出流程里的关闭被拦下来了")
	}
}

// 没记住选择、也没勾「记住」时，收起一次之后下次关闭还得再问。
func TestClosePromptForgetsWithoutRemember(t *testing.T) {
	a := testApp(t)
	a.trayReady = true
	a.closeRemember = false
	a.chooseCloseMode(closeToTray)
	if a.closeMode.mode != closeAsk {
		t.Fatalf("没勾记住却写入了选择：%d", a.closeMode.mode)
	}
	next := &mygo.CloseEvent{}
	a.onMainWindowClose(next)
	if !next.DefaultPrevented() || !a.closePromptOpen {
		t.Fatal("下次关闭应重新询问")
	}
}

// 提示框里两个选项都点得动：退出应用走应用自己的退出流程，收进托盘收起窗口。
func TestClosePromptButtons(t *testing.T) {
	for _, tc := range []struct {
		button string
		mode   int
	}{
		{"退出应用", closeApp},
		{"收进托盘", closeToTray},
	} {
		t.Run(tc.button, func(t *testing.T) {
			a := testApp(t)
			a.trayReady = true
			quits := 0
			a.quitApp = func() { quits++ }
			a.openClosePrompt()
			tt := ui.NewTester(a.view, 1100, 760)
			tt.Frame()
			for _, want := range []string{"关闭主窗口", "记住我的选择", "退出应用", "收进托盘"} {
				if !tt.HasText(want) {
					t.Fatalf("关闭提示缺少 %q：%q", want, tt.Texts())
				}
			}
			if tt.HasText("只关窗口") {
				t.Error("合并后不该再出现「只关窗口」")
			}
			if err := tt.Click(tc.button); err != nil {
				t.Fatal(err)
			}
			if a.closePromptOpen {
				t.Error("点了按钮弹窗还开着")
			}
			if tc.mode == closeApp && quits != 1 {
				t.Errorf("点「退出应用」调用了 %d 次退出，期望 1 次", quits)
			}
			if tc.mode != closeApp && quits != 0 {
				t.Error("点「收进托盘」不该退出应用")
			}
		})
	}
}

// 设置窗口的「窗口」一块给出三档，点一下就落盘，结果用窗口内 toast 说明。
func TestSettingsWindowCloseBehaviourSavesChoice(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false
	a.trayReady = true
	a.settingsTab = settingsTabGeneral
	// 比真实窗口高：这一块在滚动区末尾，无头测试不滚动也能点到。
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1400)
	st.Frame()

	for _, want := range []string{"窗口", "每次询问", "收进托盘", "退出应用", "关闭主窗口时"} {
		if !st.HasText(want) {
			t.Fatalf("设置窗口缺少 %q：%q", want, st.Texts())
		}
	}
	if st.HasText("关闭窗口") {
		t.Error("合并后设置里不该再出现「关闭窗口」")
	}

	if err := st.Click("收进托盘"); err != nil {
		t.Fatal(err)
	}
	if a.closeMode.mode != closeToTray {
		t.Fatalf("选择收进托盘后模式 = %d", a.closeMode.mode)
	}
	var cfg windowConfig
	if err := readJSONFile(filepath.Join(dir, "window.json"), &cfg); err != nil {
		t.Fatalf("选择后没有写入 window.json：%v", err)
	}
	if cfg.Close != "tray" {
		t.Errorf("window.json 关闭行为 = %q，期望 tray", cfg.Close)
	}
	st.Frame()
	if !st.HasText("关闭主窗口时：收进托盘") {
		t.Errorf("切换后没有弹出 toast：%q", st.Texts())
	}

	if err := st.Click("每次询问"); err != nil {
		t.Fatal(err)
	}
	if a.closeMode.mode != closeAsk {
		t.Fatalf("切回每次询问后模式 = %d", a.closeMode.mode)
	}
	if err := readJSONFile(filepath.Join(dir, "window.json"), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Close != "ask" {
		t.Errorf("window.json 关闭行为 = %q，期望 ask", cfg.Close)
	}
}

// 托盘不可用时设置里要说明「收进托盘」会按退出处理，不能让人以为选了就
// 一定能藏到后台。
func TestSettingsWindowExplainsUnavailableTray(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabGeneral
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1400)
	st.Frame()
	if !st.HasText("系统托盘当前不可用") {
		t.Errorf("托盘不可用时没有说明：%q", st.Texts())
	}
	a.trayReady = true
	st.Frame()
	if st.HasText("系统托盘当前不可用") {
		t.Errorf("托盘可用时不该再提示不可用：%q", st.Texts())
	}

	// 生产里 app 比托盘先建好：设置里同样要读实时状态，而不是构建时的快照。
	b := testBook(t)
	a2 := newAppWithDeps(b.deps(), nil)
	a2.settingsTab = settingsTabGeneral
	st2 := ui.NewTester(a2.settingsWindowView, settingsWindowWidth, 1400)
	st2.Frame()
	if !st2.HasText("系统托盘当前不可用") {
		t.Error("托盘还没建出来时设置里该说明不可用")
	}
	b.trayReady = true
	st2.Frame()
	if st2.HasText("系统托盘当前不可用") {
		t.Error("托盘就绪后不该再提示不可用")
	}
}
