package main

import (
	"testing"
	"time"
)

// 折叠过滤：被折叠的顶层评论收起它的整棵子树（含更深的楼中楼），其余评论
// 顺序不变；无折叠时 display 与 replyList 逐元素一致。
func TestReplyViewRebuildDisplay(t *testing.T) {
	list := []replyItem{
		{ID: "r0", Depth: 0, HasChildren: true},
		{ID: "r1", Depth: 1},
		{ID: "r1a", Depth: 2},
		{ID: "r2", Depth: 1},
		{ID: "r3", Depth: 0},
	}

	// 无折叠：display 直接复用 replyList，索引不变。
	d := &detailState{replyList: list}
	d.rebuildDisplay()
	if len(d.display) != len(list) {
		t.Fatalf("无折叠时 display 长度应为 %d，实际 %d", len(list), len(d.display))
	}
	for i := range list {
		if d.display[i].ID != list[i].ID {
			t.Errorf("无折叠时第 %d 行应为 %s，实际 %s", i, list[i].ID, d.display[i].ID)
		}
	}

	// 折叠 r0：收起它的全部后代（r1/r1a/r2），只留 r0 自身与 r3。
	d.collapsed = map[string]bool{"r0": true}
	d.rebuildDisplay()
	want := []string{"r0", "r3"}
	if len(d.display) != len(want) {
		t.Fatalf("折叠后 display 长度应为 %d，实际 %d", len(want), len(d.display))
	}
	for i := range want {
		if d.display[i].ID != want[i] {
			t.Errorf("折叠后第 %d 行应为 %s，实际 %s", i, want[i], d.display[i].ID)
		}
	}
	if d.hiddenCount["r0"] != 3 {
		t.Errorf("r0 被收起的后代数应为 3，实际 %d", d.hiddenCount["r0"])
	}
}

// 新评论判定：创建时间晚于基线才算「新」；基线为空、时间解析失败或早于基线
// 都返回 false。
func TestReplyViewNewReplies(t *testing.T) {
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		created string
		base    time.Time
		want    bool
	}{
		{"2026-11-02T00:00:00Z", base, true},
		{"2026-10-31T00:00:00Z", base, false},
		{"", base, false},
		{"2026-11-02T00:00:00Z", time.Time{}, false},
		{"not-a-time", base, false},
	}
	for _, c := range cases {
		if got := isNewReply(c.created, c.base); got != c.want {
			t.Errorf("isNewReply(%q, %v) = %v，期望 %v", c.created, c.base, got, c.want)
		}
	}
}

// 记忆落盘后能读回，且停留位置与折叠集合都保留；不同帖子不串。
func TestReplyViewPersistsAndRestores(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false
	d := &detailState{
		shortID:   "TOPIC1",
		stayIdx:   7,
		stayID:    "r7",
		collapsed: map[string]bool{"r2": true},
	}
	a.saveReplyView(d)

	b := newApp(dir)
	b.autoLoad = false
	mem, ok := b.loadReplyView("TOPIC1")
	if !ok {
		t.Fatal("重启后没有读回评论记忆")
	}
	if mem.ScrollIdx != 7 || mem.ReadToID != "r7" {
		t.Errorf("停留位置没有保留：idx=%d id=%q", mem.ScrollIdx, mem.ReadToID)
	}
	found := false
	for _, id := range mem.Collapsed {
		if id == "r2" {
			found = true
		}
	}
	if !found {
		t.Error("折叠集合没有保留 r2")
	}

	if _, ok := b.loadReplyView("OTHER"); ok {
		t.Error("不同帖子的记忆被错误地读到了")
	}
}

// 记忆按账号隔离：匿名时存的评论记忆，登录后读不到；退出回到匿名又能读回。
func TestReplyViewIsPerAccount(t *testing.T) {
	a := testApp(t)
	d := &detailState{shortID: "X", collapsed: map[string]bool{"r1": true}}
	a.saveReplyView(d)
	if _, ok := a.loadReplyView("X"); !ok {
		t.Fatal("匿名状态没有存下评论记忆")
	}

	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok",
		TokenType:   "Bearer",
		Username:    "jimmy",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.loadReplyView("X"); ok {
		t.Error("登录后读到了匿名账号的评论记忆（未按账号隔离）")
	}
	// 登录账号存一条再退出，匿名那一条应仍在。
	a.saveReplyView(d)
	a.auth.Logout()
	mem, ok := a.loadReplyView("X")
	if !ok {
		t.Error("退出后没有回到匿名账号的评论记忆")
	}
	if len(mem.Collapsed) != 1 {
		t.Error("退出后读回的折叠集合不对")
	}
}

// 视图模式按账号记、与帖子无关：在一篇帖子里切到扁平，换一篇打开也还是
// 扁平；换账号读不到别人的偏好。
func TestReplyViewModeFollowsAccountNotTopic(t *testing.T) {
	a := testApp(t)
	if _, ok := a.loadReplyViewMode(); ok {
		t.Fatal("没有任何记录时不应读回视图偏好")
	}

	a.openDetail(Item{ShortID: "TOPIC1"})
	if a.detail == nil || a.detail.flat {
		t.Fatalf("默认应是楼层视图：%+v", a.detail)
	}
	a.detail.flat = true
	a.saveReplyViewMode(true)
	a.closeDetail()

	// 换一篇帖子：偏好跟着账号走，不跟着上一篇的记忆走。
	a.openDetail(Item{ShortID: "TOPIC2"})
	if !a.detail.flat {
		t.Error("打开另一篇帖子时没有沿用账号的扁平视图偏好")
	}
	a.closeDetail()

	// 从评论列表点进来要定位某层楼，仍强制扁平，但不该改写偏好。
	a.saveReplyViewMode(false)
	a.openDetail(Item{ShortID: "TOPIC3", CommentID: "c1"})
	if !a.detail.flat {
		t.Error("定位评论时应强制扁平视图")
	}
	if flat, ok := a.loadReplyViewMode(); !ok || flat {
		t.Errorf("定位评论不应改写账号的视图偏好：flat=%v ok=%v", flat, ok)
	}
	a.closeDetail()

	// 换账号：读不到匿名（或上一个账号）的视图偏好。
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok",
		TokenType:   "Bearer",
		Username:    "jimmy",
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.loadReplyViewMode(); ok {
		t.Error("登录后读到了匿名账号的视图偏好（未按账号隔离）")
	}
	a.saveReplyViewMode(true)
	a.auth.Logout()
	if flat, ok := a.loadReplyViewMode(); !ok || flat {
		t.Errorf("退出后应回到匿名账号的楼层视图偏好：flat=%v ok=%v", flat, ok)
	}
}
