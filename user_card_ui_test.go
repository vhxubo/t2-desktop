package main

import "testing"

// 资料卡里的帖子 / 回复行：主窗口切到对应正文，资料卡本身留着不关。
func TestUserCardDetailKeepsCardOpen(t *testing.T) {
	a := testApp(t)
	a.userCard = newUserCardState("alice")
	a.setUserCardWindowShown(true)

	a.openUserCardDetail(Item{ShortID: "T1", CommentID: "R1"})

	if a.pageName != "detail" {
		t.Fatalf("点帖子后主窗口没有进正文页：%q", a.pageName)
	}
	if a.detail == nil || a.detail.shortID != "T1" || a.detail.focusID != "R1" {
		t.Fatalf("打开的帖子不对：%+v", a.detail)
	}
	if a.userCard == nil || !a.userCardShown {
		t.Error("打开帖子后资料卡窗口不该被关掉")
	}
}
