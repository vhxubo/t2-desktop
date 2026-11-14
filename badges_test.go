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

// 同一个 equipped_badges 解码器要覆盖所有已经带徽章的用户对象：最新评论、
// 通知操作者 / 表态人、金币排行与流水、搜索用户、列表行作者。全部只读响应
// 里的字段，不额外请求。
func TestDecodeBadgesFromEveryUserObject(t *testing.T) {
	const yearStreak = `{"badge":{"name":"全勤","icon_url":"https://r2.2libra.com/badge/svg/year_streak.svg"}}`
	comment := mustObject(t, `{
		"id":"c1","content":"评论",
		"author":{"username":"jimmy","equipped_badges":[`+yearStreak+`]},
		"parent":{"id":"p1","content":"父评论","author":{"username":"alice","equipped_badges":[{`+
		`"badge":{"name":"混沌","icon_url":"https://r2.2libra.com/badge/svg/chaos.svg"}}]}}
	}`)
	ci := decodeCommentItem(comment)
	if len(ci.Badges) != 1 || ci.Badges[0].Name != "全勤" {
		t.Errorf("最新评论作者徽章错误：%+v", ci.Badges)
	}
	if len(ci.ParentBadges) != 1 || ci.ParentBadges[0].Name != "混沌" {
		t.Errorf("父评论作者徽章错误：%+v", ci.ParentBadges)
	}
	row := commentItemFrom(comment)
	if row.Extra == nil || len(row.Extra.AuthorBadges) != 1 ||
		len(row.Extra.ReplyToBadges) != 1 {
		t.Errorf("评论行没有带上作者与父评论徽章：%+v", row.Extra)
	}

	n := decodeNotification([]byte(`{"id":"n1","type":"reply","from_user":{` +
		`"username":"jimmy","equipped_badges":[` + yearStreak + `]},"payload":{}}`))
	if n == nil {
		t.Fatal("通知没有解析出来")
	}
	if v := notificationViewOf(n); len(v.ActorBadges) != 1 || v.ActorBadges[0].Name != "全勤" {
		t.Errorf("通知操作者徽章错误：%+v", v.ActorBadges)
	}

	overview := notificationReactionOverviewOf(mustObject(t, `{"relations":[{`+
		`"emoji":"👍","username":"jimmy","equipped_badges":[`+yearStreak+`]}]}`))
	if len(overview.People) != 1 || len(overview.People[0].Badges) != 1 {
		t.Errorf("表态用户徽章错误：%+v", overview.People)
	}

	ranks := decodeCoinRanking([]byte(`[{"username":"jimmy","balance":10,"equipped_badges":[` + yearStreak + `]}]`))
	if len(ranks) != 1 || len(ranks[0].Badges) != 1 || ranks[0].Badges[0].Name != "全勤" {
		t.Errorf("金币排行徽章错误：%+v", ranks)
	}

	tx := decodeCoinTransaction([]byte(`{"id":"t","reason":"reward","from_user":{` +
		`"username":"jimmy","equipped_badges":[` + yearStreak + `]}}`))
	if len(tx.FromBadges) != 1 || tx.FromBadges[0].Name != "全勤" {
		t.Errorf("金币流水来源用户徽章错误：%+v", tx.FromBadges)
	}

	users := decodeSearch(searchUsers, []byte(`[{"username":"jimmy","equipped_badges":[`+yearStreak+`]}]`), 1)
	if len(users.hits) != 1 || len(users.hits[0].badges) != 1 {
		t.Errorf("搜索用户徽章错误：%+v", users.hits)
	}

	item := itemFrom([]byte(`{"short_id":"S1","title":"标题","author":{`+
		`"username":"jimmy","equipped_badges":[`+yearStreak+`]}}`), kindTopic)
	if len(item.AuthorBadges) != 1 || item.AuthorBadges[0].Name != "全勤" {
		t.Errorf("列表行作者徽章错误：%+v", item.AuthorBadges)
	}
}

// 通用用户标识自己负责画徽章：数据里有才画，没有就保持原来的「头像 + 昵称」。
func TestUserTagRendersOnlyProvidedBadges(t *testing.T) {
	a := testApp(t)
	icon := "https://r2.2libra.com/badge/svg/year_streak.svg"
	// 真实徽章源图是 512×512 的位图；这里用同尺寸，防止图片按固有宽度
	// 把整行撑开（曾经出现过徽章占 328px 宽的问题）。
	a.images.put(icon, solidBitmap(512, 512, marker))
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Gap(10).Padding(10).Children(func() {
			a.userTag(c, userTagSpec{
				Name: "jimmy", Size: 12, Bold: true,
				Badges: []badgeInfo{{Name: "全勤", IconURL: icon}},
			})
			a.userTag(c, userTagSpec{Name: "dannyng", Size: 12, Bold: true})
		})
	}, 400, 160)
	tt.Frame()
	if !tt.HasText("全勤") {
		t.Errorf("userTag 没有画出已拿到的徽章：%q", tt.Texts())
	}
	if !tt.HasText("jimmy") || !tt.HasText("dannyng") {
		t.Errorf("徽章影响了昵称渲染：%q", tt.Texts())
	}
	badge, ok := tt.Find("全勤")
	if !ok {
		t.Fatalf("找不到徽章图片：%q", tt.Texts())
	}
	if badge.W > 16 || badge.H > 16 {
		t.Errorf("徽章占位 %.1f×%.1f，应紧贴昵称并限制在 15×15 左右", badge.W, badge.H)
	}
	// 只有 jimmy 带徽章；dannyng 没有数据，不应凭空多出图标。
	if n := countMarker(tt.Image()); n == 0 {
		t.Error("userTag 的徽章图标没有画出来")
	}
}

// 最新评论列表行也走统一的徽章渲染：解码到 author.equipped_badges 后，
// 列表里应当出现徽章名称。
func TestLatestCommentRowShowsAuthorBadges(t *testing.T) {
	a := testApp(t)
	const icon = "https://r2.2libra.com/badge/svg/go_scout_ahead.svg"
	a.images.put(icon, solidBitmap(22, 15, marker))
	it := itemFrom([]byte(`{"id":"c1","content":"评论","author":{`+
		`"username":"2Libre","equipped_badges":[{"badge":{"name":"前排打手",`+
		`"icon_url":"`+icon+`"}}]},"post":{"short_id":"S1","title":"帖子"}}`), kindComment)
	a.tab = 4
	a.tabs[4].loaded = true
	a.tabs[4].items = []Item{it}
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("前排打手") {
		t.Errorf("最新评论行没有显示作者徽章：%q", tt.Texts())
	}
	if !tt.HasText("@2Libre") {
		t.Errorf("最新评论行的作者没有渲染：%q", tt.Texts())
	}
}
