package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 检查间隔默认 6 小时，只认预设档位，重置回到默认。
func TestUpdateIntervalPrefsStore(t *testing.T) {
	dir := t.TempDir()
	s := newUpdateIntervalPrefsStore(dir)
	if got := s.interval(); got != 6*time.Hour {
		t.Fatalf("默认间隔 = %v，期望 6 小时", got)
	}
	if err := s.setHours(1); err != nil {
		t.Fatal(err)
	}
	if got := newUpdateIntervalPrefsStore(dir).interval(); got != time.Hour {
		t.Errorf("重新读回的间隔 = %v，期望 1 小时", got)
	}
	if err := writeJSONFile(filepath.Join(dir, "update_interval.json"),
		updateIntervalConfig{Hours: 7}); err != nil {
		t.Fatal(err)
	}
	if got := newUpdateIntervalPrefsStore(dir).interval(); got != 6*time.Hour {
		t.Errorf("非法档位 %v 没有回到默认 6 小时", got)
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := newUpdateIntervalPrefsStore(dir).interval(); got != 6*time.Hour {
		t.Errorf("重置没有回到默认：%v", got)
	}
}

// 设置窗口用 Lobe 分段选择切换间隔：立即落盘，并接到后台循环（无头测试走钩子）。
func TestUpdateSettingsIntervalChoice(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabGeneral
	var got []int
	a.onUpdateInterval = func(hours int) { got = append(got, hours) }
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1400)
	st.Frame()
	for _, label := range updateIntervalOptions {
		if !st.HasText(label) {
			t.Fatalf("设置窗口没有间隔档位 %q：%q", label, st.Texts())
		}
	}
	if err := st.Click("12小时"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.updateIntervalPrefs.interval() != 12*time.Hour {
		t.Errorf("点 12小时 没有落盘：%v", a.updateIntervalPrefs.interval())
	}
	if len(got) == 0 || got[len(got)-1] != 12 {
		t.Errorf("间隔切换没有接到后台循环：%v", got)
	}
	if !st.HasText("检查更新间隔已改为 12 小时") {
		t.Errorf("切换后没有提示：%q", st.Texts())
	}
	if again := newUpdateIntervalPrefsStore(a.updateIntervalPrefs.dir); again.interval() != 12*time.Hour {
		t.Error("间隔没有写到磁盘")
	}
}
