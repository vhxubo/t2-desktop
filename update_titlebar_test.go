package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 标题栏的更新按钮默认不露出；发现新版本后才出现一枚圆角图标按钮，下载中用
// 自绘的描边圆环显示进度并可点取消，装完只提示「重启更新」，重启只由用户点击。
func TestTitleBarUpdateButtonStates(t *testing.T) {
	a := testApp(t)
	var restarts, checks, cancels int
	d := &desktop{updateMgr: newUpdateManager()}
	d.updateMgr.relaunch = func() { restarts++ }
	a.desktop = d
	a.onUpdateCheck = func() { checks++ }
	a.onUpdateCancel = func() { cancels++ }

	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if _, ok := tt.Find("有新版本"); ok {
		t.Fatalf("空闲时不该露出更新按钮：%q", tt.Texts())
	}
	d.setUpdateStatus(updateStatus{Phase: updateChecking})
	tt.Frame()
	if _, ok := tt.Find("有新版本"); ok {
		t.Fatal("检查中不该露出更新按钮")
	}

	d.setUpdateStatus(updateStatus{Phase: updateDetected, Version: "9.9.9"})
	tt.Frame()
	if r, ok := tt.Find("有新版本"); !ok || r.Y > titleBarHeight {
		t.Fatalf("有新版本时按钮不在标题栏内：%+v %v", r, ok)
	}
	if err := tt.Click("有新版本"); err != nil {
		t.Fatal(err)
	}
	if checks != 1 {
		t.Errorf("点「有新版本」没有打开更新详情：%d", checks)
	}

	d.setUpdateStatus(updateStatus{Phase: updateDownloading, Version: "9.9.9", Percent: 45})
	tt.Frame()
	if !tt.HasText("下载中 45%") {
		t.Errorf("下载中没有显示进度：%q", tt.Texts())
	}
	if err := tt.Click("下载中 45%"); err != nil {
		t.Fatal(err)
	}
	if cancels != 1 {
		t.Errorf("点下载进度没有取消下载：%d", cancels)
	}
	if restarts != 0 {
		t.Fatal("下载过程中不该重启")
	}

	d.setUpdateStatus(updateStatus{Phase: updateReady, Version: "9.9.9", Percent: 100})
	tt.Frame()
	if !tt.HasText("重启更新") {
		t.Fatalf("装完没有显示重启按钮：%q", tt.Texts())
	}
	if err := tt.Click("重启更新"); err != nil {
		t.Fatal(err)
	}
	if restarts != 1 {
		t.Errorf("点「重启更新」没有触发重启：%d", restarts)
	}
}

// 下载中的进度环由 drawUpdateRing 自绘：45% 时正上方应是主色描边，正下方只剩
// 轨道色，从而看出画的是一段圆弧而不是整圈实心。
func TestTitleBarUpdateDrawsProgressRing(t *testing.T) {
	a := testApp(t)
	d := &desktop{updateMgr: newUpdateManager()}
	a.desktop = d

	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	d.setUpdateStatus(updateStatus{Phase: updateDownloading, Version: "9.9.9", Percent: 45})
	tt.Frame()
	// 按钮的底色 / 图标色带 Lobe 的过渡动画，等它走完再看环的像素。
	tt.Advance(250 * time.Millisecond)
	r, ok := tt.Find("下载中 45%")
	if !ok {
		t.Fatalf("下载中没有更新按钮：%q", tt.Texts())
	}
	cx, cy := int(r.X+r.W/2), int(r.Y+r.H/2)
	rad := int(r.W/2) - 2
	im := tt.Image()
	tk := lobeCfg.Tokens(false)
	top := im.RGBAAt(cx, cy-rad)
	bottom := im.RGBAAt(cx, cy+rad)
	if top.R != tk.Primary.R || top.G != tk.Primary.G || top.B != tk.Primary.B {
		t.Errorf("进度环正上方 = #%02x%02x%02x，期望主色 %s", top.R, top.G, top.B, hexToken(tk.Primary))
	}
	if bottom.R == tk.Primary.R && bottom.G == tk.Primary.G && bottom.B == tk.Primary.B {
		t.Errorf("45%% 时进度环正下方不该是主色，实际 %+v", bottom)
	}
	// 轨道色是带低透明度的黑，画在浅底上会合成浅灰，不能直接和 token 的 RGB 比。
	if bottom.R < 200 || bottom.G < 200 || bottom.B < 200 {
		t.Errorf("进度环正下方应是浅色轨道，实际 #%02x%02x%02x", bottom.R, bottom.G, bottom.B)
	}
}

// 悬浮标题栏更新按钮能看到该版本的更新日志；日志来自更新清单，非空才追加。
func TestTitleBarUpdateTooltipShowsReleaseNotes(t *testing.T) {
	a := testApp(t)
	d := &desktop{updateMgr: newUpdateManager()}
	a.desktop = d

	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	d.setUpdateStatus(updateStatus{
		Phase: updateDetected, Version: "9.9.9",
		Notes: "本次更新：\n- 修复下载中断\n- 支持取消下载",
	})
	tt.Frame()
	r, ok := tt.Find("有新版本")
	if !ok {
		t.Fatalf("没有更新按钮：%q", tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(800 * time.Millisecond)
	tt.Frame()
	if !tt.HasText("更新日志（9.9.9）") || !tt.HasText("支持取消下载") {
		t.Fatalf("悬浮没有显示更新日志：%q", tt.Texts())
	}
}

func TestTitleUpdateTip(t *testing.T) {
	if got := titleUpdateTip("提示", "9.9.9", "  "); got != "提示" {
		t.Errorf("空日志不该追加标题：%q", got)
	}
	got := titleUpdateTip("提示", "9.9.9", "第一行\n第二行")
	if want := "提示\n\n更新日志（9.9.9）：\n第一行\n第二行"; got != want {
		t.Errorf("悬浮说明 = %q，期望 %q", got, want)
	}
	long := strings.Repeat("长", 400)
	got = titleUpdateTip("提示", "9.9.9", long)
	if r := []rune(got); len(r) > 340 || !strings.HasSuffix(got, "…") {
		t.Errorf("长日志没有截断：%d 字", len([]rune(got)))
	}
}

// 没有新版本时不会显示重启按钮；后台状态变化不触发重启。
func TestUpdateStatusDoesNotRelaunchItself(t *testing.T) {
	a := testApp(t)
	var restarts int
	d := &desktop{updateMgr: newUpdateManager()}
	d.updateMgr.relaunch = func() { restarts++ }
	a.desktop = d

	for _, phase := range []updatePhase{updateIdle, updateChecking, updateDetected, updateFailed} {
		d.setUpdateStatus(updateStatus{Phase: phase})
		if restarts != 0 {
			t.Fatalf("状态 %d 自己触发了重启", phase)
		}
	}
}
