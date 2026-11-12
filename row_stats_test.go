package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 线上 /v1/topics 的帖子对象直接带最后回复人、最后回复时间、最高楼层与
// 回复数；列表行要用这几个字段，不能把 latest_commented_at 当成发帖时间。
const topicWithReplyStatsJSON = `{
	"id":"d5c49401-3c4e-4eee-8c08-abd868ef8886",
	"short_id":"VAnfxaR",
	"node":{"id":"n1","name":"个人生活","slug":"personal-life"},
	"title":"这个月的情绪有问题",
	"author":{"id":"u1","username":"winmer","avatar_url":"https://example.com/w.png"},
	"created_at":"2026-11-12T00:28:31.196Z",
	"is_pinned":false,
	"comment_count":83,
	"latest_commented_at":"2026-11-12T13:17:38.335Z",
	"last_commented_by":{"id":"u2","username":"Kyun"},
	"extra":{"latest_comment":{"id":"c1"},"latest_comment_floor":42}
}`

func TestTopicItemCarriesReplyStats(t *testing.T) {
	it := itemFrom([]byte(topicWithReplyStatsJSON), kindTopic)
	if it.Stats == nil {
		t.Fatalf("帖子行没有解析回复时效信息：%+v", it)
	}
	if it.Stats.Replier != "Kyun" {
		t.Errorf("最后回复人 = %q，期望 Kyun", it.Stats.Replier)
	}
	if want := relativeTime("2026-11-12T13:17:38.335Z"); it.Stats.Updated != want {
		t.Errorf("最后回复时间 = %q，期望 %q", it.Stats.Updated, want)
	}
	if it.Stats.MaxFloor != 42 {
		t.Errorf("最高楼层 = %d，期望 42", it.Stats.MaxFloor)
	}
	if it.Stats.Replies != 83 {
		t.Errorf("回复数 = %d，期望 83", it.Stats.Replies)
	}
	if it.Pinned {
		t.Error("这篇帖子没有置顶")
	}
	// 发帖时间仍然来自 created_at，不受最后回复时间影响。
	if want := relativeTime("2026-11-12T00:28:31.196Z"); it.SubtitleSuffix != " · "+want {
		t.Errorf("副标题里的发帖时间 = %q，期望 %q", it.SubtitleSuffix, " · "+want)
	}
}

// 帖子行要同时画出最后回复人 / 更新时间 / 最高楼层，回复数只给数字：
// 右侧的回复气泡后面不能再出现「N 回复」这种文本。
func TestTopicRowShowsLatestReplyAndBareReplyCount(t *testing.T) {
	a := testApp(t)
	it := itemFrom([]byte(topicWithReplyStatsJSON), kindTopic)
	a.tabs[0].items = []Item{it}
	a.tabs[0].loaded = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	for _, want := range []string{"最后回复", "Kyun", it.Stats.Updated, "42 楼", "83"} {
		if !tt.HasText(want) {
			t.Errorf("列表行缺少 %q：%q", want, tt.Texts())
		}
	}
	if tt.HasText("83 回复") {
		t.Errorf("回复数不应再带「回复」文本：%q", tt.Texts())
	}

	author, ok := tt.Find("winmer")
	if !ok {
		t.Fatalf("找不到发帖人：%q", tt.Texts())
	}
	title, ok := tt.Find("这个月的情绪有问题")
	if !ok {
		t.Fatalf("找不到帖子标题：%q", tt.Texts())
	}
	if title.H <= author.H {
		t.Errorf("标题应比作者等辅助文案更大：标题 %+v / 作者 %+v", title, author)
	}
	replier, ok := tt.Find("Kyun")
	if !ok {
		t.Fatalf("找不到最后回复人：%q", tt.Texts())
	}
	if replier.Y <= author.Y {
		t.Errorf("最后回复应排在发帖人下一行：最后回复 %+v / 发帖人 %+v", replier, author)
	}
	floor, ok := tt.Find("42 楼")
	if !ok {
		t.Fatalf("找不到最高楼层：%q", tt.Texts())
	}
	if floor.X <= replier.X {
		t.Errorf("最高楼层应在最后回复人右侧：楼层 %+v / 回复人 %+v", floor, replier)
	}
	count, ok := tt.Find("83")
	if !ok {
		t.Fatalf("找不到回复数：%q", tt.Texts())
	}
	if count.X <= floor.X {
		t.Errorf("回复数应靠右显示：回复数 %+v / 楼层 %+v", count, floor)
	}
}

// 还没人回复的帖子不画「最后回复」行，但回复数仍然显示 0；置顶帖把
// 置顶角标放在回复数旁边。
func TestTopicRowWithoutReplies(t *testing.T) {
	a := testApp(t)
	it := itemFrom([]byte(`{
		"id":"45e41a09","short_id":"YYa7u49",
		"node":{"id":"n2","name":"作品分享","slug":"personal-works"},
		"title":"fable5.2 做的清明上河图",
		"author":{"id":"u3","username":"maqingxiongs"},
		"created_at":"2026-11-12T11:12:40.245Z","is_pinned":true,
		"comment_count":0,"latest_commented_at":"2026-11-12T11:12:40.248Z",
		"last_commented_by":null,"extra":null
	}`), kindTopic)
	a.tabs[0].items = []Item{it}
	a.tabs[0].loaded = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if tt.HasText("最后回复") {
		t.Errorf("零回复的帖子不应显示最后回复行：%q", tt.Texts())
	}
	if !tt.HasText("0") {
		t.Errorf("零回复的帖子也要显示回复数 0：%q", tt.Texts())
	}
	if !tt.HasText("置顶") {
		t.Errorf("置顶帖缺少置顶角标：%q", tt.Texts())
	}
}
