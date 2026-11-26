package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// countTextContaining 数一数有多少条文字包含 substr。Tester.HasText 是子串匹配，
// 状态栏的「发评约 30 金币」本身就含「30 金币」，要区分是状态栏还是发送按钮
// 上的那枚数字，只能按出现次数判断。
func countTextContaining(tt *ui.Tester, substr string) int {
	n := 0
	for _, s := range tt.Texts() {
		if strings.Contains(s, substr) {
			n++
		}
	}
	return n
}

// 两个开关的默认与持久化：状态栏默认开（保持原有行为），发送按钮上的数字
// 默认关；改动都写进 comment-cost.json，下一轮启动照着读。
func TestCommentCostPrefsDefaultsAndPersist(t *testing.T) {
	dir := t.TempDir()
	s := newCommentCostPrefsStore(dir)
	if !s.visible() || s.onSend() {
		t.Fatalf("默认值 = 状态栏 %v / 发送按钮 %v，期望 开/关", s.visible(), s.onSend())
	}
	if err := s.setVisible(false); err != nil {
		t.Fatal(err)
	}
	if err := s.setOnSend(true); err != nil {
		t.Fatal(err)
	}
	again := newCommentCostPrefsStore(dir)
	if again.visible() || !again.onSend() {
		t.Fatalf("重读后 = 状态栏 %v / 发送按钮 %v，期望 关/开", again.visible(), again.onSend())
	}
	if err := again.setVisible(true); err != nil {
		t.Fatal(err)
	}
	if err := again.setOnSend(false); err != nil {
		t.Fatal(err)
	}
	final := newCommentCostPrefsStore(dir)
	if !final.visible() || final.onSend() {
		t.Fatalf("再次重读后 = 状态栏 %v / 发送按钮 %v，期望 开/关", final.visible(), final.onSend())
	}
}

// 关掉「金币消耗与徽章切换」后状态栏不再画，字数退回工具条右端；再打开能回来。
func TestCommentCostBarCanBeTurnedOff(t *testing.T) {
	a, tt := commentCostApp(t)
	if !tt.HasText("当前：未佩戴 · 发评约 30 金币") {
		t.Fatalf("初始状态栏没画出来：%q", tt.Texts())
	}
	if err := a.commentCostPrefs.setVisible(false); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("当前：未佩戴 · 发评约 30 金币") {
		t.Errorf("关掉后不该再画费用状态栏：%q", tt.Texts())
	}
	if !tt.HasText("120 字") {
		t.Errorf("关掉后字数应退回工具条：%q", tt.Texts())
	}
	if err := a.commentCostPrefs.setVisible(true); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("当前：未佩戴 · 发评约 30 金币") {
		t.Errorf("重新打开后状态栏应回来：%q", tt.Texts())
	}
}

// 发送按钮上的金币数默认不画；打开开关后出现，且与状态栏共用同一个估算。
// 单独关掉状态栏不影响发送按钮上的数字。
func TestSendButtonCostToggle(t *testing.T) {
	a, tt := commentCostApp(t)
	// 默认只有状态栏里那一条；发送按钮上没有数字。
	if n := countTextContaining(tt, "30 金币"); n != 1 {
		t.Fatalf("默认出现 %d 条「30 金币」，期望只有状态栏那 1 条：%q", n, tt.Texts())
	}
	if err := a.commentCostPrefs.setOnSend(true); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if n := countTextContaining(tt, "30 金币"); n != 2 {
		t.Errorf("打开后应多出发送按钮上的数字，实际 %d 条：%q", n, tt.Texts())
	}
	if err := a.commentCostPrefs.setVisible(false); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if n := countTextContaining(tt, "30 金币"); n != 1 {
		t.Errorf("关掉状态栏后发送按钮仍应显示数字，实际 %d 条：%q", n, tt.Texts())
	}
	if tt.HasText("当前：未佩戴 · 发评约 30 金币") {
		t.Error("状态栏应已关闭")
	}
}

// 设置窗口的「评论」页：两枚开关点击后立即生效并落盘，且默认页不显示它们。
func TestCommentSettingsTogglesPersist(t *testing.T) {
	a := testApp(t)
	// 阅读页在上面多了一块排版设置：给足高度让评论开关落在可视区内。
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1600)
	st.Frame()
	if st.HasText("金币消耗与徽章切换") {
		t.Errorf("默认打开的通用页不该显示评论开关：%q", st.Texts())
	}
	if err := st.Click("阅读与评论"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !a.commentCostPrefs.visible() || a.commentCostPrefs.onSend() {
		t.Fatalf("评论页默认 = 状态栏 %v / 发送按钮 %v，期望 开/关",
			a.commentCostPrefs.visible(), a.commentCostPrefs.onSend())
	}
	if err := st.Click("在发送按钮上显示消耗金币数"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !a.commentCostPrefs.onSend() {
		t.Error("点发送按钮开关后没有生效")
	}
	if err := st.Click("金币消耗与徽章切换"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.commentCostPrefs.visible() {
		t.Error("点状态栏开关后没有关闭")
	}
	again := newCommentCostPrefsStore(a.commentCostPrefs.dir)
	if again.visible() || !again.onSend() {
		t.Errorf("设置没有写进 comment-cost.json：状态栏 %v / 发送按钮 %v",
			again.visible(), again.onSend())
	}
}
