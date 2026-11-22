package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func testDraftsApp(t *testing.T) *app {
	t.Helper()
	a := testApp(t)
	a.autoLoad = false
	return a
}

func TestSidebarShowsDraftsWithCount(t *testing.T) {
	a := testDraftsApp(t)
	a.compose = newComposeState()
	a.compose.title = "草稿一"
	a.flushDrafts()
	a.compose = newComposeState()
	a.compose.title = "草稿二"
	a.flushDrafts()

	items := a.sidebarNavItems()
	var label string
	for _, it := range items {
		if it.Key == "drafts" {
			label = it.Label
		}
	}
	// 数字不再写进文案，改由 Lobe 红点角标展示，折叠/展开读同一份计数。
	if label != "草稿箱" {
		t.Fatalf("侧边栏草稿箱文案不对：%q（%+v）", label, items)
	}
	if got := a.navBadgeCount("drafts"); got != 2 {
		t.Fatalf("草稿箱角标计数 = %d，期望 2", got)
	}

	for _, expanded := range []bool{true, false} {
		a.navExpanded = expanded
		tt := ui.NewTester(a.view, 1200, 900)
		tt.Frame()
		row, ok := tt.Find("草稿箱")
		if !ok {
			t.Fatalf("展开=%v 时侧边栏没有草稿箱：%q", expanded, tt.Texts())
		}
		dot, ok := tt.Find("2")
		if !ok {
			t.Fatalf("展开=%v 时草稿箱没画出红点计数：%q", expanded, tt.Texts())
		}
		if dot.Y < row.Y-20 || dot.Y > row.Y+row.H+20 {
			t.Errorf("展开=%v 时红点计数 y=%.0f 不在草稿箱行 y=%.0f 附近", expanded, dot.Y, row.Y)
		}
	}
}

// 折叠态角标要贴着图标的右上角，不能落在带内边距的按钮角上。按钮方块 32、
// 图标 16，差的一半就是角标该往里收的距离（见 main.go 的 navBadgeInset）。
func TestSidebarCollapsedBadgeFollowsIcon(t *testing.T) {
	a := testDraftsApp(t)
	for _, title := range []string{"草稿一", "草稿二"} {
		a.compose = newComposeState()
		a.compose.title = title
		a.flushDrafts()
	}
	a.navExpanded = false

	tt := ui.NewTester(a.view, 1200, 900)
	tt.Frame()

	button, ok := tt.Find("草稿箱")
	if !ok {
		t.Fatalf("折叠态侧边栏没有草稿箱：%q", tt.Texts())
	}
	dot, ok := tt.Find("2")
	if !ok {
		t.Fatalf("折叠态草稿箱没画出角标：%q", tt.Texts())
	}

	// 图标在 32x32 按钮里居中、占 16x16，右上角相对按钮左上角是 (24, 8)。
	// 角标 pill 中心相对图标顶边再下移 navBadgeDrop，故落在 (24, 8+5)。
	wantX := button.X + (sidebarRailButtonSize+sidebarRailGlyphSize)/2
	wantY := button.Y + navBadgeInset + navBadgeDrop
	gotX, gotY := dot.X+dot.W/2, dot.Y+dot.H/2
	if d := gotX - wantX; d < -4 || d > 4 {
		t.Errorf("角标中心 x=%.1f，期望贴近图标右上角 %.1f（按钮 x=%.1f）", gotX, wantX, button.X)
	}
	if d := gotY - wantY; d < -4 || d > 4 {
		t.Errorf("角标中心 y=%.1f，期望贴近图标右上角 %.1f（按钮 y=%.1f）", gotY, wantY, button.Y)
	}
}

func TestDraftsPageKeepsSidebarAndOpensNoWindow(t *testing.T) {
	a := testDraftsApp(t)
	if a.win != nil || len(a.detailWindows) != 0 {
		t.Fatal("测试应用不该已有原生窗口")
	}
	a.handleSidebarAction("drafts")
	if a.pageName != "drafts" || a.navSel != "drafts" {
		t.Fatalf("没进草稿页：page=%q nav=%q", a.pageName, a.navSel)
	}
	tt := ui.NewTester(a.view, 1200, 900)
	tt.Frame()
	if a.win != nil || len(a.detailWindows) != 0 {
		t.Error("进草稿页不该新开窗口")
	}
	if _, ok := tt.Find("草稿箱"); !ok {
		t.Errorf("草稿页没画出标题：%q", tt.Texts())
	}
	if _, ok := tt.Find("还没有草稿"); !ok {
		t.Errorf("空草稿箱没画空态：%q", tt.Texts())
	}
}

func TestSelectTabLeavesDraftsPage(t *testing.T) {
	a := testDraftsApp(t)
	a.pageName = "drafts"
	a.navSel = "drafts"
	a.selectTab(0)
	if a.pageName != "list" || a.navSel != "t0" {
		t.Fatalf("点浏览页没回到列表：page=%q nav=%q", a.pageName, a.navSel)
	}
	// 点当前已选中的那一项（走 reclickTab）也要能离开草稿页。
	a.pageName = "drafts"
	a.selectTab(0)
	if a.pageName != "list" {
		t.Fatalf("重点当前页没离开草稿页：page=%q", a.pageName)
	}
}

func TestDraftsRowOpensCompose(t *testing.T) {
	a := testDraftsApp(t)
	st := newComposeState()
	st.title = "本地草稿应该怎么存"
	st.body = "想聊聊落盘节奏"
	st.voteOpen = true
	st.voteTitle = "存哪"
	a.compose = st
	a.flushDrafts()
	// 清掉 live，验证是从 store 恢复而不是接着原来的表单写。
	a.compose = nil
	a.showDrafts()

	tt := ui.NewTester(a.view, 1200, 900)
	tt.Frame()
	if err := tt.Click("打开草稿：本地草稿应该怎么存"); err != nil {
		t.Fatal(err)
	}
	if a.compose == nil || a.compose.title != "本地草稿应该怎么存" {
		t.Fatalf("点开草稿没恢复发帖表单：%+v", a.compose)
	}
	if !a.compose.voteOpen || a.compose.voteTitle != "存哪" {
		t.Errorf("投票组件没恢复：%+v", a.compose)
	}
}

func TestDraftsRowReusesOpenDetailWindow(t *testing.T) {
	a := testDraftsApp(t)
	d := &detailState{shortID: "abc123", draft: "旧内容"}
	a.detail = d
	e := draftEntry{
		ID: replyDraftID("abc123"), Kind: draftKindReply, ShortID: "abc123",
		Text: "恢复出来的回复", ReplyTo: draftTarget{ID: "cmt-88", Floor: 12, Author: "老王"},
		UpdatedAt: time.Now().UTC(),
	}
	a.draftsForCurrent().put(e)
	a.restoreReplyDraft(e)
	if a.detail != d {
		t.Fatal("已打开的详情被换掉了")
	}
	if d.draft != "恢复出来的回复" || d.replyTo.ID != "cmt-88" || d.replyTo.Floor != 12 {
		t.Fatalf("回复草稿没恢复：draft=%q replyTo=%+v", d.draft, d.replyTo)
	}
	if !d.focusComposer {
		t.Error("恢复后应标记一次性聚焦")
	}

	// detailView 构建一次后聚焦标记应被消费掉。
	tt := ui.NewTester(func(c *ui.Context) { a.detailView(c, true) }, 1200, 900)
	tt.Frame()
	if d.focusComposer {
		t.Error("聚焦标记该由输入框消费一次")
	}
}

func TestDraftsClearNeedsConfirm(t *testing.T) {
	a := testDraftsApp(t)
	a.compose = newComposeState()
	a.compose.title = "别被清掉"
	a.flushDrafts()
	a.showDrafts()

	tt := ui.NewTester(a.view, 1200, 900)
	tt.Frame()
	if err := tt.Click("清空全部"); err != nil {
		t.Fatal(err)
	}
	if !a.draftsClearOpen {
		t.Fatal("点清空全部只该弹确认，不该直接删")
	}
	tt.Frame()
	if a.drafts.count() != 1 {
		t.Fatalf("弹确认时草稿不该被删：%d", a.drafts.count())
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if a.draftsClearOpen || a.drafts.count() != 1 {
		t.Errorf("取消后状态不该变：open=%v count=%d", a.draftsClearOpen, a.drafts.count())
	}
}

func TestDraftsClearAllRemovesEverything(t *testing.T) {
	a := testDraftsApp(t)
	a.compose = newComposeState()
	a.compose.title = "发帖草稿"
	a.flushDrafts()
	d := &detailState{shortID: "abc123", draft: "回复草稿"}
	a.detail = d
	a.flushDrafts()
	if a.drafts.count() != 2 {
		t.Fatalf("清空前应有 2 条：%d", a.drafts.count())
	}
	a.showDrafts()
	a.draftsClearOpen = true

	tt := ui.NewTester(a.view, 1200, 900)
	tt.Frame()
	if err := tt.Click("清空草稿"); err != nil {
		t.Fatal(err)
	}
	if a.drafts.count() != 0 {
		t.Errorf("确认后箱子该空了：%+v", a.drafts.rows())
	}
	if a.compose.title != "" || d.draft != "" {
		t.Error("确认后 live 状态也该清空")
	}
	if a.draftsClearOpen {
		t.Error("确认后弹窗该关掉")
	}
	if _, err := os.Stat(a.drafts.path); err == nil {
		t.Error("确认后草稿文件该删掉")
	}
}

// 页头只留标题与条数：账号范围、保存位置这类文案说明不该占一行。
func TestDraftsHeaderHasNoScopeDescription(t *testing.T) {
	a := testDraftsApp(t)
	a.compose = newComposeState()
	a.compose.title = "一条草稿"
	a.flushDrafts()
	a.showDrafts()

	tt := ui.NewTester(a.view, 1200, 900)
	tt.Frame()
	for _, text := range tt.Texts() {
		if strings.Contains(text, "保存在这台电脑上") {
			t.Fatalf("草稿箱页头不该有账号范围说明：%q", tt.Texts())
		}
	}
}

func TestDraftsEmptyState(t *testing.T) {
	a := testDraftsApp(t)
	a.showDrafts()
	tt := ui.NewTester(a.view, 1200, 900)
	tt.Frame()
	if _, ok := tt.Find("还没有草稿"); !ok {
		t.Errorf("空箱子没画空态：%q", tt.Texts())
	}
	if _, ok := tt.Find("清空全部"); !ok {
		t.Errorf("页头该有清空全部按钮（即使禁用）：%q", tt.Texts())
	}
}
