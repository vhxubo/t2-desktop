package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 「闲时自动下载更新」默认开启，关闭后重新读回还是关闭，重置回到默认。
func TestUpdateIdlePrefsStorePersist(t *testing.T) {
	dir := t.TempDir()
	s := newUpdateIdlePrefsStore(dir)
	if !s.on() {
		t.Fatal("闲时自动下载更新默认该开启")
	}
	if err := s.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	if again := newUpdateIdlePrefsStore(dir); again.on() {
		t.Error("重新读回的选择不是关闭")
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if !s.on() {
		t.Error("重置后没有回到开启")
	}
	if again := newUpdateIdlePrefsStore(dir); !again.on() {
		t.Error("重置没有落盘")
	}
}

// 默认接收测试版时强制开启；只有打开稳定渠道才按用户选择走。
func TestIdleAutoDownloadForcedByPrerelease(t *testing.T) {
	a := testApp(t)
	if err := a.updateIdlePrefs.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	if !a.idleAutoDownload() {
		t.Fatal("默认接收测试版时该强制开启")
	}
	if err := a.updateChannel.setStable(true); err != nil {
		t.Fatal(err)
	}
	if a.idleAutoDownload() {
		t.Error("打开稳定渠道后该恢复用户关闭的选择")
	}
	if err := a.updateChannel.setStable(false); err != nil {
		t.Fatal(err)
	}
	if !a.idleAutoDownload() {
		t.Error("关掉稳定渠道后又该强制开启")
	}
}

// 设置窗口的开关：默认接收测试版时点不动；打开稳定渠道后可以关掉并接到
// 更新插件；关掉稳定渠道后又强制开启，再打开则恢复用户的选择。
func TestUpdateSettingsIdleAutoDownloadToggle(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabGeneral
	var got []bool
	a.onIdleAutoDownload = func(on bool) { got = append(got, on) }
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1400)
	st.Frame()
	if !st.HasText("闲时自动下载更新") {
		t.Fatalf("设置窗口没有闲时自动下载开关：%q", st.Texts())
	}
	if !st.HasText("接收测试版时强制开启；打开「稳定渠道」后恢复你的选择。") {
		t.Errorf("默认没有强制开启提示：%q", st.Texts())
	}
	if err := st.Click("闲时自动下载更新"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !a.idleAutoDownload() {
		t.Error("测试版下不该能关掉闲时自动下载")
	}
	if !st.HasText("接收测试版时强制开启闲时自动下载更新") {
		t.Errorf("强制开启时点开关没有提示：%q", st.Texts())
	}

	// 打开稳定渠道：用户关闭生效，并接到更新插件。
	if err := st.Click("稳定渠道"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if err := st.Click("闲时自动下载更新"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.updateIdlePrefs.on() {
		t.Fatal("点开关没有关闭闲时自动下载")
	}
	if len(got) == 0 || got[len(got)-1] {
		t.Fatalf("关闭没有接到更新插件：%v", got)
	}
	if !st.HasText("已关闭闲时自动下载更新") {
		t.Errorf("关闭后没有提示：%q", st.Texts())
	}

	// 关掉稳定渠道：又强制开启，并接给更新插件。
	if err := st.Click("稳定渠道"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !a.idleAutoDownload() || len(got) == 0 || !got[len(got)-1] {
		t.Fatalf("接收测试版没有强制开启：prefs=%v hook=%v", a.idleAutoDownload(), got)
	}
	if err := st.Click("闲时自动下载更新"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !a.idleAutoDownload() {
		t.Error("测试版下不该能关掉闲时自动下载")
	}

	// 再打开稳定渠道：恢复用户之前关掉的选择。
	if err := st.Click("稳定渠道"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.idleAutoDownload() {
		t.Error("打开稳定渠道后该恢复关闭状态")
	}
}
