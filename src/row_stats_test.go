package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
	// 最后一条回复的 uuid 供列表行的「最后回复」链接定位楼层用。
	if it.Stats.LastCommentID != "c1" {
		t.Errorf("最后一条回复 id = %q，期望 c1", it.Stats.LastCommentID)
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

// 帖子行的「最后回复」是独立入口：点它进正文并直接定位到最后一条回复。
// 它是最内层可点元素，点它不会连带触发外层整行的点击（返回栈不该变深）。
func TestTopicRowLastReplyJumpsToComment(t *testing.T) {
	a := testApp(t)
	it := itemFrom([]byte(topicWithReplyStatsJSON), kindTopic)
	a.tabs[0].items = []Item{it}
	a.tabs[0].loaded = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if err := tt.Click("最后回复"); err != nil {
		t.Fatalf("点最后回复失败：%v", err)
	}
	tt.Frame()
	if a.pageName != "detail" || a.detail == nil {
		t.Fatalf("点最后回复没有打开详情：%q", a.pageName)
	}
	if a.detail.shortID != "VAnfxaR" {
		t.Errorf("打开的帖子不对：%q", a.detail.shortID)
	}
	if a.detail.focusID != "c1" || !a.detail.focusPending {
		t.Errorf("没有定位到最后一条回复：focusID=%q pending=%v", a.detail.focusID, a.detail.focusPending)
	}
	if len(a.detailStack) != 0 {
		t.Errorf("点最后回复连带触发了整行点击：返回栈 %d 层", len(a.detailStack))
	}
}

// 时间与最高楼层只是说明文字，不绑定「跳到最后回复」：点它们按普通整行
// 打开帖子，不带定位目标，也不连带触发行内链接。
func TestTopicRowTimeAndFloorDoNotJump(t *testing.T) {
	a := testApp(t)
	it := itemFrom([]byte(topicWithReplyStatsJSON), kindTopic)
	a.tabs[0].items = []Item{it}
	a.tabs[0].loaded = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if err := tt.Click(it.Stats.Updated); err != nil {
		t.Fatalf("点最后回复时间失败：%v", err)
	}
	tt.Frame()
	if a.detail == nil {
		t.Fatal("点最后回复时间没有打开详情")
	}
	if a.detail.focusID != "" || a.detail.focusPending {
		t.Errorf("点时间不该定位到最后回复：focusID=%q pending=%v", a.detail.focusID, a.detail.focusPending)
	}
	if len(a.detailStack) != 0 {
		t.Errorf("点时间触发了行内链接：返回栈 %d 层", len(a.detailStack))
	}
}

// 接口没给最后一条回复的 uuid 时，「最后回复」退回纯文本：点它仍按普通行
// 打开帖子本身，不带着定位目标。
func TestTopicRowLastReplyWithoutIDIsPlainText(t *testing.T) {
	a := testApp(t)
	it := itemFrom([]byte(`{
		"id":"45e41a09","short_id":"YYa7u49",
		"node":{"id":"n2","name":"作品分享","slug":"personal-works"},
		"title":"fable5.2 做的清明上河图",
		"author":{"username":"maqingxiongs"},
		"created_at":"2026-11-12T11:12:40.245Z",
		"comment_count":7,"latest_commented_at":"2026-11-12T13:12:40.245Z",
		"last_commented_by":{"username":"Kyun"},
		"extra":{"latest_comment_floor":9}
	}`), kindTopic)
	if it.Stats.LastCommentID != "" {
		t.Fatalf("不该解析出最后回复 id：%q", it.Stats.LastCommentID)
	}
	a.tabs[0].items = []Item{it}
	a.tabs[0].loaded = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if err := tt.Click("最后回复"); err != nil {
		t.Fatalf("点最后回复失败：%v", err)
	}
	tt.Frame()
	if a.detail == nil {
		t.Fatal("点最后回复没有打开详情")
	}
	if a.detail.focusID != "" || a.detail.focusPending {
		t.Errorf("没有 id 时点了不该定位：focusID=%q pending=%v", a.detail.focusID, a.detail.focusPending)
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

// 线上 is_pinned 在 pinned_until 过期后仍为 true，信息流不能继续画置顶角标。
func TestTopicPinRespectsPinnedUntil(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	cases := []struct {
		name   string
		fields string
		want   bool
	}{
		{"已过期", `"is_pinned":true,"pinned_until":"` + past + `"`, false},
		{"未过期", `"is_pinned":true,"pinned_until":"` + future + `"`, true},
		{"别名字段过期", `"pinned":true,"pinned_until":"` + past + `"`, false},
		{"旧响应没有时间", `"is_pinned":true`, true},
		{"时间为 null", `"is_pinned":true,"pinned_until":null`, true},
		{"时间无法解析", `"is_pinned":true,"pinned_until":"不是时间"`, true},
		{"本来未置顶", `"is_pinned":false,"pinned_until":"` + future + `"`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"id":"p1","short_id":"P1","title":"置顶判断",`+
				`"author":{"username":"u1"},"created_at":"2026-11-25T07:33:55.722Z",%s}`, c.fields)
			it := itemFrom([]byte(raw), kindTopic)
			if it.Pinned != c.want {
				t.Errorf("置顶标记 = %v，期望 %v", it.Pinned, c.want)
			}
			if got := strings.Contains(it.Meta, "置顶"); got != c.want {
				t.Errorf("摘要里的置顶 = %v，Meta = %q", got, it.Meta)
			}
		})
	}
}

func TestTopicRowDropsExpiredPin(t *testing.T) {
	a := testApp(t)
	until := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	it := itemFrom([]byte(fmt.Sprintf(`{
		"id":"yAGbLJL","short_id":"yAGbLJL",
		"node":{"id":"n1","name":"社区讨论","slug":"forum-function"},
		"title":"到期后不该再有角标",
		"author":{"username":"vhxubo"},
		"created_at":"2026-11-25T07:33:55.722Z","is_pinned":true,
		"pinned_until":%q,"comment_count":0
	}`, until)), kindTopic)
	a.tabs[0].items = []Item{it}
	a.tabs[0].loaded = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if tt.HasText("置顶") {
		t.Errorf("置顶过期后不该再显示角标：%q", tt.Texts())
	}
}
