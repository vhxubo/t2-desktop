package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

const badgeAuthorJSON = `{
  "short_id":"BADGE1","title":"徽章","content":"正文",
  "author":{"username":"jimmy","equipped_badges":[
    {"badge":{"name":"全勤","icon_url":"https://r2.2libra.com/badge/svg/year_streak.svg","icon_url_large":"https://r2.2libra.com/badge/year_streak.svg"}},
    {"badge":{"name":"忽冷忽热","icon_url":"https://r2.2libra.com/badge/svg/hot_and_cold.svg"},"weather":{"symbol_code":"fog","temperature":2.7}}
  ]}
}`

const badgeReplyJSON = `{
  "items":[{
    "id":"c1","content":"评论","floor":1,
    "author":{"username":"dannyng","equipped_badges":[
      {"badge":{"name":"混沌","icon_url":"https://r2.2libra.com/badge/svg/chaos.svg"}}
    ]}
  }],"total":1,"page":1,"limit":30,"total_pages":1
}`

// 作者佩戴的社区徽章要解码出来：图标、名称，以及「忽冷忽热」的天气。
func TestDecodeEquippedBadges(t *testing.T) {
	p, err := decodeTopic([]byte(badgeAuthorJSON), "BADGE1")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Badges) != 2 {
		t.Fatalf("作者徽章数量 = %d，期望 2：%+v", len(p.Badges), p.Badges)
	}
	if p.Badges[0].Name != "全勤" ||
		p.Badges[0].IconURL != "https://r2.2libra.com/badge/svg/year_streak.svg" {
		t.Errorf("第一枚徽章错误：%+v", p.Badges[0])
	}
	if p.Badges[1].Weather == nil || p.Badges[1].Weather.SymbolCode != "fog" ||
		p.Badges[1].Weather.Temperature != 2.7 {
		t.Errorf("天气徽章错误：%+v", p.Badges[1])
	}

	pg := decodeReplies([]byte(badgeReplyJSON))
	if len(pg.Items) != 1 || len(pg.Items[0].Badges) != 1 ||
		pg.Items[0].Badges[0].Name != "混沌" {
		t.Errorf("评论作者徽章错误：%+v", pg.Items)
	}
	if weatherIconURL("fog") != "https://r2.2libra.com/weather/svg/fog.svg" {
		t.Errorf("天气图标地址错误：%s", weatherIconURL("fog"))
	}
}

// 帖子的 emojis 表可能只给相对地址；合并到全局表时统一成媒体域名，并
// 保留全局表里已有的中文名。
func TestTopicEmojiMapMerge(t *testing.T) {
	p, err := decodeTopic([]byte(`{
		"short_id":"E1","title":"图标","content":"x",
		"emojis":{":sigh:":"/emojis/sigh.png?size=medium",":community:":"/emojis/community.png"}
	}`), "E1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Emojis[":community:"] != "https://r2.2libra.com/emojis/community.png" {
		t.Errorf("相对地址没有解析：%+v", p.Emojis)
	}
	global := decodeEmojis([]byte(detailEmojisJSON))
	merged := mergeEmojiMaps(global, p.Emojis)
	if merged[":sigh:"].Name != "叹气" ||
		merged[":sigh:"].ImgURL != "https://r2.2libra.com/emojis/sigh.png?size=medium" {
		t.Errorf("已有表情的本地地址 / 名称合并错误：%+v", merged[":sigh:"])
	}
	if merged[":community:"].Name != "community" {
		t.Errorf("社区图标缺少可用名称：%+v", merged[":community:"])
	}
	if got := mergeEmojiMaps(global, nil); len(got) != len(global) {
		t.Errorf("空映射不应改变全局表：%d", len(got))
	}
}

// 评论里也会出现内置表情 / 社区图标，要按行内图片画，而不是 [name]。
func TestCommentEmojiRendersInline(t *testing.T) {
	a, _ := emojiDetailApp(t, "正文")
	a.detail.replyList = []replyItem{{
		ID: "c1", Author: "dannyng", Floor: 1, Content: "恭喜 :sigh: 结尾",
	}}
	a.detail.replyTotal = 1
	a.detail.replyLoaded = true
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("\uFFFC") {
		t.Errorf("评论表情没有成为行内图片：%q", tt.Texts())
	}
	if n := countMarker(tt.Image()); n == 0 {
		t.Error("评论表情没有画出来")
	}
}

func badgeApp(t *testing.T) *app {
	t.Helper()
	a := testApp(t)
	a.openDetail(Item{ShortID: "BADGE1", URL: "https://2libra.com/post/work/BADGE1"})
	a.detail.topic = &topicDetail{
		ShortID: "BADGE1", Title: "徽章", Content: "正文", Author: "jimmy",
		Badges: []badgeInfo{{
			Name:    "全勤",
			IconURL: "https://r2.2libra.com/badge/svg/year_streak.svg",
		}},
		URL: "https://2libra.com/post/work/BADGE1",
	}
	a.detail.replyList = []replyItem{{
		ID: "c1", Author: "dannyng", Floor: 1, Content: "评论",
		Badges: []badgeInfo{{
			Name:    "忽冷忽热",
			IconURL: "https://r2.2libra.com/badge/svg/hot_and_cold.svg",
			Weather: &weatherInfo{SymbolCode: "fog", Temperature: 2.7},
		}},
	}}
	a.detail.replyTotal = 1
	a.detail.replyLoaded = true
	bmp := solidBitmap(22, 15, marker)
	a.images.put("https://r2.2libra.com/badge/svg/year_streak.svg", bmp)
	a.images.put("https://r2.2libra.com/weather/svg/fog.svg", bmp)
	return a
}

// 正文作者与评论作者昵称旁都要画出徽章图标；天气徽章还要带温度。
func TestAuthorBadgeIconsRender(t *testing.T) {
	a := badgeApp(t)
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("全勤") {
		t.Errorf("正文作者徽章没有渲染：%q", tt.Texts())
	}
	if !tt.HasText("忽冷忽热") || !tt.HasText("3°") {
		t.Errorf("评论作者的天气徽章没有渲染：%q", tt.Texts())
	}
	if n := countMarker(tt.Image()); n == 0 {
		t.Error("徽章图标没有画到界面上")
	}
}
