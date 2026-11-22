package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestNodeEditorLimitAndDuplicate(t *testing.T) {
	a := testApp(t)
	st := &nodeEditor{}
	a.addNodeShortcut(st, nodeRef{ID: "n1", Name: "节点 1"})
	a.addNodeShortcut(st, nodeRef{ID: "n1", Name: "节点 1"})
	if len(st.draft) != 1 {
		t.Errorf("重复项被加进草稿：%+v", st.draft)
	}
	for i := 2; i <= nodeShortcutMax; i++ {
		a.addNodeShortcut(st, nodeRef{ID: "n" + string(rune('0'+i)), Name: "节点"})
	}
	a.addNodeShortcut(st, nodeRef{ID: "n9", Name: "第 9 个"})
	if len(st.draft) != nodeShortcutMax {
		t.Errorf("快捷入口数 = %d，期望 %d", len(st.draft), nodeShortcutMax)
	}
}

func TestNodeEditorCancelDiscards(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	a.nodeShortcuts = []nodeRef{{ID: "n-life", Slug: "life", Name: "生活"}}
	a.openNodeEditor()
	a.nodeEditor.draft = []nodeRef{{ID: "n-work", Slug: "work", Name: "职场"}}
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("节点快捷入口") {
		t.Fatalf("配置弹窗没打开：%q", tt.Texts())
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.nodeEditor.open {
		t.Error("取消后弹窗还开着")
	}
	if len(a.nodeShortcuts) != 1 || a.nodeShortcuts[0].ID != "n-life" {
		t.Errorf("取消不该写回草稿：%+v", a.nodeShortcuts)
	}
}

// 配置弹窗里没有全局排序 / 启动表单：排序是节点行上的小切换。
func TestNodeEditorPerNodeSort(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	a.openNodeEditorOnMain()
	a.nodeEditor.draft = []nodeRef{
		{ID: "n-life", Slug: "life", Name: "生活"},
		{ID: "n-work", Slug: "work", Name: "职场", ParentSlug: "life", ParentName: "生活"},
	}
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	for _, label := range []string{"搜索节点", "排序方式", "启动时", "筛选设置"} {
		if _, ok := tt.Find(label); ok {
			t.Errorf("弹窗里还有不该出现的控件 %q：%q", label, tt.Texts())
		}
	}
	if err := tt.Click("切换排序：生活 · 职场"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.nodeEditor.draft[1].Sort != nodeSortLatest {
		t.Errorf("排序没写到这个节点上：%+v", a.nodeEditor.draft)
	}
	if a.nodeEditor.draft[0].Sort != "" {
		t.Errorf("不该顺带改别的节点：%+v", a.nodeEditor.draft)
	}
	if err := tt.Click("切换排序：生活 · 职场"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.nodeEditor.draft[1].Sort != nodeSortValue {
		t.Errorf("再点一次没切回价值优先：%+v", a.nodeEditor.draft[1])
	}
}

func TestNodeEditorSavePersists(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	a.openNodeEditor()
	a.nodeEditor.draft = []nodeRef{
		{ID: "n-work", Slug: "work", Name: "职场", ParentSlug: "life", ParentName: "生活", Sort: nodeSortLatest},
	}
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.nodeEditor.open {
		t.Error("保存后弹窗没关")
	}
	if len(a.nodeShortcuts) != 1 || a.nodeShortcuts[0].Sort != nodeSortLatest {
		t.Errorf("保存时没把节点排序写进去：%+v", a.nodeShortcuts)
	}
	dir := filepath.Dir(filepath.Dir(a.kv.path))
	second := newApp(dir)
	second.loadNodePrefs()
	if len(second.nodeShortcuts) != 1 || second.nodeShortcuts[0].Sort != nodeSortLatest {
		t.Errorf("重启后快捷入口或它的排序丢失：%+v", second.nodeShortcuts)
	}
}

func TestNodeEditorSearchAddsAndCloses(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	a.openNodeEditor()
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("添加节点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.nodeEditor.pickerOpen {
		t.Fatal("点「添加节点」应打开二级添加面板")
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if err := tt.Click("搜索节点"); err != nil {
		t.Fatal(err)
	}
	tt.Type("职场")
	tt.Frame()
	if err := tt.Click("生活 · 职场"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.nodeEditor.draft) != 1 || a.nodeEditor.draft[0].ID != "n-work" {
		t.Fatalf("搜索结果没有加入草稿：%+v", a.nodeEditor.draft)
	}
	if a.nodeEditor.pickerOpen {
		t.Error("选完节点应该直接关掉二级面板，回到配置弹窗")
	}
	if a.nodeEditor.search != "" {
		t.Errorf("选完应该清空搜索词，实际 %q", a.nodeEditor.search)
	}
}

// 二级添加面板里的列表要按窗口高度铺开，不能被旧的 260 上限截成几行。
func TestNodeEditorPickerFillsDrawerHeight(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	nodes := []composeNode{{ID: "n-life", Slug: "life", Name: "生活"}}
	for i := 0; i < 30; i++ {
		nodes = append(nodes, composeNode{
			ID: "n-row-" + strconv.Itoa(i), Slug: "row-" + strconv.Itoa(i),
			Name: fmt.Sprintf("列表节点%02d", i), ParentSlug: "life", ParentName: "生活",
		})
	}
	a.nodeCat.nodes = nodes
	a.nodeCat.loaded = true
	a.nodeCat.at = time.Now()
	a.openNodeEditorOnMain()
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if err := tt.Click("添加节点"); err != nil {
		t.Fatal(err)
	}
	tt.Advance(400 * time.Millisecond)
	tt.Frame()
	if info, ok := tt.Access("生活"); !ok || info.Bounds.W < 100 {
		t.Fatalf("左列父分类被压扁：%+v %v", info.Bounds, ok)
	}
	r, ok := tt.Find("列表节点15")
	if !ok {
		t.Fatalf("抽屉列表被截断，第 16 行没画出来：%q", tt.Texts())
	}
	if r.Y+r.H > 760 {
		t.Errorf("第 16 行超出窗口：%+v", r)
	}
}

// 首页筛选浮层不再有全局排序开关，默认按价值优先。
func TestFilterPopoverHasNoGlobalSort(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("全部节点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("排序"); ok {
		t.Fatalf("筛选浮层里还有排序开关：%q", tt.Texts())
	}
	waitQuery(t, e, "sort=value")
}
