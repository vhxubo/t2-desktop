package main

import (
	"path/filepath"
	"testing"

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

func TestNodeEditorSavePersists(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	a.openNodeEditor()
	a.nodeEditor.draft = []nodeRef{
		{ID: "n-work", Slug: "work", Name: "职场", ParentSlug: "life", ParentName: "生活"},
	}
	a.nodeEditor.sort = 1
	a.nodeEditor.remember = nodeRememberOn
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.nodeEditor.open {
		t.Error("保存后弹窗没关")
	}
	if len(a.nodeShortcuts) != 1 {
		t.Errorf("保存时没去重：%+v", a.nodeShortcuts)
	}
	if a.nodePrefs.Sort != nodeSortLatest || !a.nodePrefs.RememberFilter {
		t.Errorf("偏好没保存：%+v", a.nodePrefs)
	}
	dir := filepath.Dir(filepath.Dir(a.kv.path))
	second := newApp(dir)
	second.loadNodePrefs()
	if len(second.nodeShortcuts) != 1 || second.nodePrefs.Sort != nodeSortLatest {
		t.Errorf("重启后偏好或快捷入口丢失：%+v %+v", second.nodeShortcuts, second.nodePrefs)
	}
}

func TestNodeEditorSearchAddsWithoutClosing(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	a.openNodeEditor()
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("选择节点：全部节点"); err != nil {
		t.Fatal(err)
	}
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
	if !a.nodeEditor.pickOpen {
		t.Error("配置弹窗里选完应该保持浮层打开，方便连续添加")
	}
	if a.nodeEditor.search != "" {
		t.Errorf("选完应该清空搜索词，实际 %q", a.nodeEditor.search)
	}
}

func TestFilterSortPersists(t *testing.T) {
	e := newNodeFeedEnv(t)
	a := nodeFeedApp(t, e)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("全部节点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("最新"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.nodePrefs.Sort != nodeSortLatest {
		t.Fatalf("排序偏好没切换：%+v", a.nodePrefs)
	}
	waitQuery(t, e, "sort=latest")
}
