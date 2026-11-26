package main

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestSidebarPrefsNormalizeAndPersist(t *testing.T) {
	dir := t.TempDir()
	s := newSidebarPrefsStore(dir)

	// 去掉一项、改一项顺序、藏掉一项，再塞进一个未知 ID。
	want := s.order()
	want = append(want[1:], want[0])
	want = want[:len(want)-1]
	s.setOrder(want)
	if !s.setVisible("scores", false) {
		t.Fatal("评分榜默认应可见，才能藏掉它")
	}
	s.items = append(s.items, sidebarPref{ID: "removed-tab", Visible: true})
	if err := s.save(); err != nil {
		t.Fatal(err)
	}

	again := newSidebarPrefsStore(dir)
	if !slices.Equal(again.order(), s.order()) {
		t.Errorf("重新读入的顺序 = %v，期望 %v", again.order(), s.order())
	}
	if again.visible("scores") {
		t.Error("隐藏状态没有写进 sidebar.json")
	}
	for _, item := range again.items {
		if item.ID == "removed-tab" {
			t.Error("未知 ID 不该进入偏好")
		}
	}

	// 缺项在读取时补回默认顺序，已有项仍按用户的顺序排在前面。
	cfg := sidebarPrefsConfig{Version: 1, Items: []sidebarPref{{ID: "t2", Visible: false}}}
	if err := writeJSONFile(filepath.Join(dir, sidebarPrefsFileName), cfg); err != nil {
		t.Fatal(err)
	}
	third := newSidebarPrefsStore(dir)
	if got := third.order()[0]; got != "t2" {
		t.Errorf("首项 = %s，期望保留的 t2", got)
	}
	if len(third.items) != len(defaultSidebarPrefs()) {
		t.Errorf("缺项没有补齐：%d / %d", len(third.items), len(defaultSidebarPrefs()))
	}
	if third.visible("t2") {
		t.Error("保存的隐藏状态不该被补齐逻辑覆盖")
	}

	if err := third.Reset(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(third.order(), defaultOrderIDs()) || !third.visible("t2") {
		t.Errorf("重置后 = %v / t2 可见 %v，期望默认顺序且全部可见", third.order(), third.visible("t2"))
	}
}

func defaultOrderIDs() []string {
	out := make([]string, 0)
	for _, p := range defaultSidebarPrefs() {
		out = append(out, p.ID)
	}
	return out
}

func TestSidebarCustomNavOrderAndVisibility(t *testing.T) {
	a := testApp(t)
	a.sidebarPrefs.setOrder([]string{"scores", "t0", "watchlater"})
	a.sidebarPrefs.setVisible("t1", false)
	a.navExpanded = true

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	scores, ok := tt.Find("评分榜")
	if !ok {
		t.Fatalf("侧边栏缺少评分榜：%q", tt.Texts())
	}
	home, ok := tt.Find("首页")
	if !ok {
		t.Fatalf("侧边栏缺少首页：%q", tt.Texts())
	}
	if scores.Y >= home.Y {
		t.Errorf("自定义排序后评分榜 y=%.0f 应在首页 y=%.0f 之上", scores.Y, home.Y)
	}
	if _, ok := tt.Find("今日热议"); ok {
		t.Error("隐藏今日热议后侧边栏不该还显示它")
	}
	if _, ok := tt.Find("草稿箱"); !ok {
		t.Error("草稿箱没有参与自定义排序，但仍应显示")
	}
	node, ok := tt.Find("节点")
	if !ok || node.Y <= home.Y {
		t.Errorf("节点分组应始终排在自定义导航之后：%+v", node)
	}

	// 折叠成图标轨后顺序仍然一致，排序不只作用于展开列表。
	a.navExpanded = false
	tt.Frame()
	railScores, ok := tt.Find("评分榜")
	if !ok {
		t.Fatalf("折叠轨缺少评分榜：%q", tt.Texts())
	}
	railHome, ok := tt.Find("首页")
	if !ok {
		t.Fatalf("折叠轨缺少首页：%q", tt.Texts())
	}
	if railScores.Y >= railHome.Y {
		t.Errorf("折叠轨里评分榜 y=%.0f 应在首页 y=%.0f 之上", railScores.Y, railHome.Y)
	}
}

func TestSidebarSettingsTogglesAndReorder(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1000)
	tt.Frame()
	if err := tt.Click("侧边栏"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.settingsTab != settingsTabSidebar {
		t.Fatalf("点了侧边栏标签后 settingsTab=%d", a.settingsTab)
	}
	for _, label := range []string{"侧边栏导航", "首页", "评分榜", "显示评分榜"} {
		if !tt.HasText(label) {
			t.Errorf("侧边栏设置页缺少 %q：%q", label, tt.Texts())
		}
	}

	// 可见性：勾选框的无障碍名带入口名，点一下立即落盘。
	if err := tt.Click("显示评分榜"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.sidebarPrefs.visible("scores") {
		t.Error("点显示勾选框后没有隐藏评分榜")
	}
	again := newSidebarPrefsStore(a.sidebarPrefs.dir)
	if again.visible("scores") {
		t.Error("显示状态没有写进 sidebar.json")
	}

	// 排序：Lobe SortableList 的手柄支持 Space + 方向键，把首页往下挪一位。
	handles := sidebarReorderHandles(tt)
	if len(handles) != len(defaultSidebarPrefs()) {
		t.Fatalf("排序手柄 %d 个，期望每个入口一个", len(handles))
	}
	h := handles[0]
	tt.ClickAt(h.Bounds.X+h.Bounds.W/2, h.Bounds.Y+h.Bounds.H/2)
	tt.Frame()
	tt.Key(0, ui.KeySpace)
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeySpace)
	tt.Advance(time.Second)
	tt.Frame()
	order := a.sidebarPrefs.order()
	if order[0] != "t1" || order[1] != "t0" {
		t.Errorf("排序后前两项 = %v，期望 t1,t0", order[:2])
	}
	again = newSidebarPrefsStore(a.sidebarPrefs.dir)
	if !slices.Equal(again.order(), order) {
		t.Errorf("排序没有写进 sidebar.json：%v / %v", again.order(), order)
	}
}

func sidebarReorderHandles(tt *ui.Tester) []ui.AccessInfo {
	var out []ui.AccessInfo
	for _, n := range tt.AccessAll() {
		if n.Role != ui.RoleButton {
			continue
		}
		if n.Name == "拖动排序" || n.Name == "Reorder" {
			out = append(out, n)
		}
	}
	return out
}

func TestResetAllSettingsRestoresSidebarNav(t *testing.T) {
	a := testApp(t)
	a.sidebarPrefs.setOrder([]string{"scores", "t0"})
	a.sidebarPrefs.setVisible("t1", false)
	if err := a.sidebarPrefs.save(); err != nil {
		t.Fatal(err)
	}
	a.resetAllSettings()
	if !slices.Equal(a.sidebarPrefs.order(), defaultOrderIDs()) {
		t.Errorf("重置后顺序 = %v，期望默认", a.sidebarPrefs.order())
	}
	for _, p := range a.sidebarPrefs.items {
		if !p.Visible {
			t.Errorf("重置后 %s 仍是隐藏的", p.ID)
		}
	}
}
