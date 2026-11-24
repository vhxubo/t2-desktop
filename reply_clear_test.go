package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 详情页评论框：空草稿不画清空，写完出现，点一下正文与候选一起清掉，
// 光标还留在输入框里可以继续写；回复目标有独立的「取消」，不该被清空带走。
func TestReplyComposerClearDraft(t *testing.T) {
	a := openTestDetail(singleAccountWindow(t, "jimmy"))
	a.detail.replyTo.ID = "r1"
	tt := ui.NewTester(a.view, 1100, 900)
	tt.Frame()
	if _, ok := tt.Find("清空"); ok {
		t.Fatalf("空草稿不该有清空按钮：%q", tt.Texts())
	}

	if err := tt.Click("写评论"); err != nil {
		t.Fatalf("点输入框失败：%v（%q）", err, tt.Texts())
	}
	tt.Type("要清掉的内容")
	tt.Frame()
	if a.detail.draft != "要清掉的内容" {
		t.Fatalf("打字没进草稿：%q", a.detail.draft)
	}
	clear, ok := tt.Find("清空")
	if !ok {
		t.Fatalf("有草稿后没有清空按钮：%q", tt.Texts())
	}
	send, ok := tt.Find("发送")
	if !ok {
		t.Fatalf("找不到发送按钮：%q", tt.Texts())
	}
	if clear.X >= send.X {
		t.Errorf("清空应在发送左侧：clear=%+v send=%+v", clear, send)
	}
	if dy := clear.Y - send.Y; dy > 10 || dy < -10 {
		t.Errorf("清空应与发送同一行：clear=%+v send=%+v", clear, send)
	}

	if err := tt.Click("清空"); err != nil {
		t.Fatalf("点清空失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if a.detail.draft != "" {
		t.Errorf("清空后草稿还在：%q", a.detail.draft)
	}
	if _, ok := tt.Find("清空"); ok {
		t.Errorf("清空后按钮该消失：%q", tt.Texts())
	}
	if !tt.Focused("写评论") {
		t.Error("清空后应把焦点还给输入框")
	}
	if a.detail.replyTo.ID != "r1" {
		t.Errorf("清空不该取消回复目标：%+v", a.detail.replyTo)
	}
}

// 独立写评论窗口共用同一份清空动作：窗口里写完点清空，页面草稿同步清空。
func TestReplyWindowClearDraft(t *testing.T) {
	a := openTestDetail(singleAccountWindow(t, "jimmy"))
	a.detail.draft = "窗口里写的内容"
	a.showReplyWindow()
	tt := ui.NewTester(a.replyWindowView, replyWindowWidth, replyWindowHeight)
	tt.Frame()
	clear, ok := tt.Find("清空")
	if !ok {
		t.Fatalf("独立窗口里有草稿却没有清空按钮：%q", tt.Texts())
	}
	send, ok := tt.Find("发送")
	if !ok {
		t.Fatalf("独立窗口里找不到发送按钮：%q", tt.Texts())
	}
	if clear.X >= send.X {
		t.Errorf("窗口里清空应在发送左侧：clear=%+v send=%+v", clear, send)
	}
	if dy := clear.Y - send.Y; dy > 10 || dy < -10 {
		t.Errorf("窗口里清空应与发送同一行：clear=%+v send=%+v", clear, send)
	}

	if err := tt.Click("清空"); err != nil {
		t.Fatalf("窗口里点清空失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if a.detail.draft != "" {
		t.Errorf("窗口清空后草稿还在：%q", a.detail.draft)
	}
	if _, ok := tt.Find("清空"); ok {
		t.Errorf("窗口清空后按钮该消失：%q", tt.Texts())
	}
}
