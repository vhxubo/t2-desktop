package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 偏好落盘、默认值与越界保护：手工编辑的配置不能把整页缩放设到 mygo 支持的
// 范围之外，否则每帧 SetZoom 都会被夹住，界面和设置页显示不一致。
func TestZoomPrefsStorePersistsAndClamps(t *testing.T) {
	dir := t.TempDir()
	s := newZoomPrefsStore(dir)
	if s.value() != appZoomDefault {
		t.Fatalf("默认缩放 = %v，想要 %v", s.value(), appZoomDefault)
	}

	s.set(1.37)
	if got := clampAppZoom(s.value()); math.Abs(float64(got-1.37)) > 0.001 {
		t.Fatalf("设置后缩放 = %v，想要 1.37", s.value())
	}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	if got := newZoomPrefsStore(dir).value(); math.Abs(float64(got-1.37)) > 0.001 {
		t.Fatalf("重读缩放 = %v，想要 1.37", got)
	}

	if err := os.WriteFile(filepath.Join(dir, zoomPrefsFile), []byte(`{"zoom":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := newZoomPrefsStore(dir).value(); got != appZoomMax {
		t.Fatalf("越界配置缩放 = %v，想要上限 %v", got, appZoomMax)
	}
	if err := os.WriteFile(filepath.Join(dir, zoomPrefsFile), []byte(`{"zoom":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := newZoomPrefsStore(dir).value(); got != appZoomDefault {
		t.Fatalf("<=0 的配置缩放 = %v，想要默认 %v（和 mygo SetZoom 一致）", got, appZoomDefault)
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := newZoomPrefsStore(dir).value(); got != appZoomDefault {
		t.Fatalf("重置后缩放 = %v，想要 %v", got, appZoomDefault)
	}
}

// 进程级共享：两扇窗口的 app 共用同一份偏好，任一处改动另一处下一帧就生效。
func TestZoomSharedAcrossApps(t *testing.T) {
	s := newZoomPrefsStore(t.TempDir())
	a1, a2 := &app{zoomPrefs: s}, &app{zoomPrefs: s}
	if err := a1.setAppZoom(1.5); err != nil {
		t.Fatal(err)
	}
	if a2.zoom() != 1.5 {
		t.Fatalf("另一扇窗口看到的缩放 = %v，想要 1.5", a2.zoom())
	}
	if err := a2.setAppZoom(appZoomDefault); err != nil {
		t.Fatal(err)
	}
	if a1.zoom() != appZoomDefault {
		t.Fatalf("重置后另一扇窗口看到的缩放 = %v，想要 %v", a1.zoom(), appZoomDefault)
	}
}

// Ctrl+滚轮与 Ctrl+触控板缩放改整页；普通滚轮留给容器与图片预览。
func TestZoomInput(t *testing.T) {
	a := testApp(t)

	if a.zoomInput(ui.InputEvent{Kind: ui.InputScroll, DY: -1}) {
		t.Error("没按 Ctrl 的滚轮不该被整页缩放吃掉")
	}
	if a.zoom() != appZoomDefault {
		t.Fatalf("普通滚轮改了缩放：%v", a.zoom())
	}
	if !a.zoomInput(ui.InputEvent{Kind: ui.InputScroll, Mods: ui.Ctrl, DY: -1}) {
		t.Error("Ctrl+滚轮向上没被处理")
	}
	if math.Abs(float64(a.zoom()-appZoomWheelStep)) > 0.001 {
		t.Fatalf("Ctrl+滚轮向上后缩放 = %v，想要 %v", a.zoom(), appZoomWheelStep)
	}
	if !a.zoomInput(ui.InputEvent{Kind: ui.InputScroll, Mods: ui.Ctrl, DY: 1}) {
		t.Error("Ctrl+滚轮向下没被处理")
	}
	if math.Abs(float64(a.zoom()-appZoomDefault)) > 0.001 {
		t.Fatalf("一上一下应回到 100%%，实际 %v", a.zoom())
	}
	if !a.zoomInput(ui.InputEvent{Kind: ui.InputZoom, Mods: ui.Ctrl, Scale: 1.5}) {
		t.Error("Ctrl+触控板缩放没被处理")
	}
	if math.Abs(float64(a.zoom()-1.5)) > 0.001 {
		t.Fatalf("Ctrl+触控板缩放后 = %v，想要 1.5", a.zoom())
	}
	// 图片预览与头像裁剪用的普通双指缩放不能被整页缩放抢走。
	if a.zoomInput(ui.InputEvent{Kind: ui.InputZoom, Scale: 2}) {
		t.Error("没按 Ctrl 的双指缩放不该被整页缩放吃掉")
	}
}

// 每扇窗口的 view 都走 windowView：偏好变化后，下一帧 view 看到的可用尺寸
// 应按缩放缩小，这正是 c.SetZoom 生效的证据。
func TestWindowViewAppliesZoom(t *testing.T) {
	a := testApp(t)
	var width float32
	view := a.windowView(func(c *ui.Context) {
		width, _ = c.Size()
		ui.Box(c).Fill()
	})
	tt := ui.NewTester(view, 640, 480)
	width = 0
	tt.Frame()
	if width != 640 {
		t.Fatalf("未缩放时 view 宽度 = %v，想要 640", width)
	}
	if err := a.setAppZoom(2); err != nil {
		t.Fatal(err)
	}
	width = 0
	tt.Frame()
	if width != 320 {
		t.Fatalf("缩放 2 倍后 view 宽度 = %v，想要 320", width)
	}
}

// 设置页「通用」里能按常用档位选，也能手动输入百分比；两种改法都落盘。
func TestZoomSettingsSection(t *testing.T) {
	a := testApp(t)
	a.settingsTab = settingsTabGeneral
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1400)
	st.Frame()

	if _, ok := st.Access("界面缩放百分比"); !ok {
		t.Fatalf("设置页缺少缩放数值框：%q", st.Texts())
	}
	if _, ok := st.Access("150%"); !ok {
		t.Fatalf("设置页缺少 150%% 档位：%q", st.Texts())
	}
	if err := st.Click("150%"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if math.Abs(float64(a.zoom()-1.5)) > 0.001 {
		t.Fatalf("点 150%% 后缩放 = %v，想要 1.5", a.zoom())
	}

	if err := st.Click("界面缩放百分比"); err != nil {
		t.Fatal(err)
	}
	st.Key(ui.Cmd, ui.KeyA)
	st.Type("175")
	st.Key(0, ui.KeyEnter)
	st.Frame()
	if math.Abs(float64(a.zoom()-1.75)) > 0.001 {
		t.Fatalf("手动输入 175%% 后缩放 = %v，想要 1.75", a.zoom())
	}

	var cfg zoomPrefsConfig
	if err := readJSONFile(filepath.Join(a.draftDir, zoomPrefsFile), &cfg); err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(cfg.Zoom-1.75)) > 0.001 {
		t.Fatalf("缩放没有落盘：%+v", cfg)
	}
}

// 命令面板里能搜到三条缩放命令，默认键位是 Ctrl+= / Ctrl+- / Ctrl+0，
// 并且这些键确实改的是进程级缩放。
func TestZoomCommandsAndShortcuts(t *testing.T) {
	a := testApp(t)
	want := map[string]commandShortcut{
		"zoomIn":    cmdKey(ui.KeyEqual),
		"zoomOut":   cmdKey(ui.KeyMinus),
		"zoomReset": cmdKey(ui.Key0),
	}
	for id, key := range want {
		def, ok := a.commands.get(id)
		if !ok {
			t.Fatalf("缺少命令 %s", id)
		}
		if def.DefaultShortcut != key {
			t.Errorf("命令 %s 默认键位 = %v，想要 %v", id, def.DefaultShortcut, key)
		}
	}

	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyEqual)
	tt.Frame()
	if a.zoom() <= appZoomDefault {
		t.Fatalf("Ctrl+= 没有放大：%v", a.zoom())
	}
	tt.Key(ui.Cmd, ui.Key0)
	tt.Frame()
	if a.zoom() != appZoomDefault {
		t.Fatalf("Ctrl+0 没有重置：%v", a.zoom())
	}
	tt.Key(ui.Cmd, ui.KeyMinus)
	tt.Frame()
	if a.zoom() >= appZoomDefault {
		t.Fatalf("Ctrl+- 没有缩小：%v", a.zoom())
	}

	// 面板过滤「缩放」应列出三条命令，点其中一条即可改缩放。
	_ = a.setAppZoom(appZoomDefault)
	if err := tt.Click("搜索关键词"); err != nil {
		t.Fatal(err)
	}
	tt.Type("缩放")
	tt.Frame()
	for _, label := range []string{"命令 放大界面", "命令 缩小界面", "命令 重置缩放"} {
		if _, ok := tt.Find(label); !ok {
			t.Errorf("命令面板过滤「缩放」后找不到 %s：%q", label, tt.Texts())
		}
	}
}
