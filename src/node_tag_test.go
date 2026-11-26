package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 节点 / 分类名在各处都走同一枚 Lobe 默认 Tag：标签文字是独立的一段，
// 不再和「 · 」拼成灰色副标题。这里逐处盯住那枚标签的文字。
func TestNodeNamesUseLobeTag(t *testing.T) {
	t.Run("列表副标题", func(t *testing.T) {
		a := testApp(t)
		a.tabs[0].loaded = true
		a.tabs[0].items = []Item{itemFrom([]byte(`{"short_id":"A1","title":"测试标题",
			"author":{"username":"bighammer"},
			"node":{"name":"职场","slug":"work"},
			"created_at":"2026-11-01T10:00:00.000Z"}`), kindTopic)}
		tt := ui.NewTester(a.view, 1100, 760)
		tt.Frame()
		if _, ok := tt.Find("职场"); !ok {
			t.Fatalf("列表里没有独立的节点标签：%q", tt.Texts())
		}
		if tt.HasText("职场 · ") {
			t.Errorf("节点名还和分隔符拼在一段文本里：%q", tt.Texts())
		}
	})

	t.Run("无作者列表副标题", func(t *testing.T) {
		a := testApp(t)
		a.tabs[0].loaded = true
		a.tabs[0].items = []Item{itemFrom([]byte(`{"short_id":"A2","title":"我的帖子",
			"node":{"name":"职场","slug":"work"},
			"created_at":"2026-11-01T10:00:00.000Z"}`), kindTopic)}
		tt := ui.NewTester(a.view, 1100, 760)
		tt.Frame()
		if _, ok := tt.Find("职场"); !ok {
			t.Fatalf("无作者的帖子行没有独立的节点标签：%q", tt.Texts())
		}
	})

	t.Run("详情页副标题", func(t *testing.T) {
		a := detailAppWithReplies(t)
		tt := ui.NewTester(a.view, 1100, 760)
		tt.Frame()
		if _, ok := tt.Find("生活 · 职场"); !ok {
			t.Fatalf("详情页没有独立的两级分类标签：%q", tt.Texts())
		}
	})

	t.Run("评论行", func(t *testing.T) {
		a := testApp(t)
		a.tab = 4
		a.tabs[4].loaded = true
		a.tabs[4].items = []Item{itemFrom([]byte(`{"id":"c1","floor":7,"content":"评论正文",
			"author":{"username":"dannyng"},
			"post":{"short_id":"S1","title":"帖子标题","node":{"slug":"n","name":"节点"}}}`), kindComment)}
		tt := ui.NewTester(a.view, 1100, 760)
		tt.Frame()
		if _, ok := tt.Find("节点"); !ok {
			t.Fatalf("评论行没有独立的节点标签：%q", tt.Texts())
		}
	})
}
