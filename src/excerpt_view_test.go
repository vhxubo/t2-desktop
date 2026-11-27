package main

import (
	"strconv"
	"testing"

	"github.com/egoist/mygo/ui"
)

const testExcerptImage = "https://r2.2libra.com/attachments/pic.png"

func TestExcerptMarks(t *testing.T) {
	text := "你好 @小明，看 " + testExcerptImage + " 和 @jimmy"
	var got []string
	for _, mk := range excerptMarks(text) {
		if mk.Name != "" {
			got = append(got, "@"+mk.Name)
		} else {
			got = append(got, mk.URL)
		}
	}
	want := []string{"@小明", testExcerptImage, "@jimmy"}
	if len(got) != len(want) {
		t.Fatalf("可点片段 = %q，期望 %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 段 = %q，期望 %q", i, got[i], want[i])
		}
	}
}

func TestExcerptMarksIgnoresOrdinaryLinks(t *testing.T) {
	for _, text := range []string{
		"邮件 a@example.com 到了",
		"看 https://example.com/post/1",
		"没有链接的一段话",
	} {
		if got := excerptMarks(text); len(got) != 0 {
			t.Errorf("%q 不该有可点片段：%+v", text, got)
		}
	}
}

// 通知正文里的裸图片地址补成 Markdown 配图；已经是图片 / 链接目标或行内
// 代码的地址不能再套一层 ![]()。
func TestMarkdownImageSource(t *testing.T) {
	const url = "https://r2.2libra.com/a.png"
	for _, tc := range []struct{ in, want string }{
		{"看 " + url, "看 ![](" + url + ")"},
		{"![图](" + url + ")", "![图](" + url + ")"},
		{"![](" + url + "#inline-md)", "![](" + url + "#inline-md)"},
		{"![](<" + url + "#inline-md>)", "![](<" + url + "#inline-md>)"},
		{"[图](<" + url + "#inline-md>)", "[图](<" + url + "#inline-md>)"},
		{"[图](" + url + ")", "[图](" + url + ")"},
		{"`" + url + "`", "`" + url + "`"},
	} {
		if got := markdownImageSource(tc.in); got != tc.want {
			t.Errorf("markdownImageSource(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// 用户链接后面的表情是独立表情，摘要要留下它并画成行内图片，不能只显示
// @名字就把表情吞掉（`[@John](…) :good:`）。
func TestLatestCommentExcerptRendersEmojiAfterUserLink(t *testing.T) {
	const content = `[@John](/user/John/about) :good:`
	it := itemFrom([]byte(`{"id":"c1","content":"`+content+`","author":{"username":"alice"},`+
		`"post":{"short_id":"S1","title":"帖子"}}`), kindComment)
	if it.Extra.Excerpt != "@John :good:" {
		t.Fatalf("摘要把表情吞掉了：%q", it.Extra.Excerpt)
	}
	a := testApp(t)
	a.emojis = map[string]emojiInfo{
		":good:": {Code: ":good:", Name: "好", ImgURL: "https://r2.2libra.com/emojis/good.png"},
	}
	a.images.put("https://r2.2libra.com/emojis/good.png", solidBitmap(64, 64, marker))
	a.tab = 4
	a.tabs[4].loaded = true
	a.tabs[4].items = []Item{it}
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("@John") || !tt.HasText("\uFFFC") {
		t.Errorf("提及或行内表情没画出来：%q", tt.Texts())
	}
	if n := countMarker(tt.Image()); n == 0 {
		t.Error("表情图片没有画到列表行上")
	}
}

// 最新评论正文里的 @提及是 Markdown 用户链接（[@名字](/user/名字/about)）。
// 点它要开这个人的资料卡：既不交给浏览器跳网页，也不连带打开帖子。
func TestLatestCommentUserLinkOpensUserCard(t *testing.T) {
	const content = `[@John](/user/John/about)`
	it := itemFrom([]byte(`{"id":"c1","content":"`+content+`","author":{"username":"alice"},`+
		`"post":{"short_id":"S1","title":"帖子"}}`), kindComment)
	a := testApp(t)
	a.tab = 4
	a.tabs[4].loaded = true
	a.tabs[4].items = []Item{it}
	page := a.pageName
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	// 整段正文是一个 RichText 标签，中心落在空白处；链接在段落开头，
	// 点左边第一个字才落在 @John 这条 Markdown 链接上。
	r, ok := tt.Find("@John")
	if !ok {
		t.Fatalf("正文里没有 @John：%q", tt.Texts())
	}
	tt.ClickAt(r.X+8, r.Y+r.H/2)
	if a.userCard == nil || a.userCard.username != "John" {
		t.Errorf("点击用户链接没有打开 John 的资料卡：%+v", a.userCard)
	}
	if got := tt.OpenedURLs(); len(got) != 0 {
		t.Errorf("用户链接被交给了系统浏览器：%q", got)
	}
	if a.pageName != page {
		t.Errorf("点用户链接连带打开了帖子：%q → %q", page, a.pageName)
	}
}

// 通知摘要里的用户链接 + 表情同理。
func TestNotificationExcerptRendersEmojiAfterUserLink(t *testing.T) {
	const content = `[@John](/user/John/about) :good:`
	n := &notification{ID: "n1", Type: "mention", IsRead: true,
		Payload: mustObject(t, `{"commentText":`+strconv.Quote(content)+`}`)}
	if got := notificationViewOf(n).Excerpt; got != content {
		t.Fatalf("通知摘要把表情吞掉了：%q", got)
	}
	a := notificationWindowApp(t)
	a.emojis = map[string]emojiInfo{
		":good:": {Code: ":good:", Name: "好", ImgURL: "https://r2.2libra.com/emojis/good.png"},
	}
	a.images.put("https://r2.2libra.com/emojis/good.png", solidBitmap(64, 64, marker))
	st := a.tabs[notificationTabIndex()]
	st.items = []Item{{Key: "n1", Notif: n}}
	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	if !tt.HasText("\uFFFC") {
		t.Errorf("通知里的行内表情没画出来：%q", tt.Texts())
	}
	if countMarker(tt.Image()) == 0 {
		t.Error("通知里的表情图片没有画出来")
	}
}

// 最新评论正文与详情页评论走同一个 Markdown 渲染：@提及开资料卡，裸图片地址
// 按配图画出来、点开进预览；两者都不抢整行的「打开帖子」。
func TestLatestCommentBodyMarksClickable(t *testing.T) {
	a := testApp(t)
	a.images.put(testExcerptImage, solidBitmap(64, 64, marker))
	it := itemFrom([]byte(`{"id":"c1","content":"@小明 看 `+testExcerptImage+`","author":{"username":"alice"},`+
		`"post":{"short_id":"S1","title":"帖子"}}`), kindComment)
	a.tab = 4
	a.tabs[4].loaded = true
	a.tabs[4].items = []Item{it}
	page := a.pageName
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, label := range []string{"查看 @小明 的资料", "图片"} {
		if !tt.HasText(label) {
			t.Errorf("正文缺少 %q：%q", label, tt.Texts())
		}
	}
	if err := tt.Click("查看 @小明 的资料"); err != nil {
		t.Fatalf("点击提及失败：%v（%q）", err, tt.Texts())
	}
	if a.userCard == nil || a.userCard.username != "小明" {
		t.Errorf("点击提及没有打开资料卡：%+v", a.userCard)
	}
	if a.pageName != page {
		t.Errorf("点提及连带打开了帖子：%q → %q", page, a.pageName)
	}
	if err := tt.Click("图片"); err != nil {
		t.Fatalf("点击配图失败：%v（%q）", err, tt.Texts())
	}
	if a.viewer == nil || !a.viewer.open {
		t.Error("点击配图没有打开预览")
	}
	if a.pageName != page {
		t.Errorf("点配图连带打开了帖子：%q → %q", page, a.pageName)
	}
}

// 通知正文按 Markdown 渲染：@提及仍能点开资料卡，裸露的图片地址被补成
// Markdown 配图后点开预览，两者都不连带打开消息。
func TestNotificationExcerptMarksClickable(t *testing.T) {
	a := notificationWindowApp(t)
	a.images.put(testExcerptImage, solidBitmap(64, 64, marker))
	st := a.tabs[notificationTabIndex()]
	st.items = []Item{{Key: "n1", Notif: &notification{
		ID: "n1", Type: "mention", IsRead: true,
		FromUser: mustObject(t, `{"username":"alice"}`),
		Payload:  mustObject(t, `{"commentText":"@小明 看 `+testExcerptImage+`","postShortId":"abc"}`),
	}}}
	page := a.pageName
	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	if err := tt.Click("查看 @小明 的资料"); err != nil {
		t.Fatalf("点击通知里的提及失败：%v（%q）", err, tt.Texts())
	}
	if a.userCard == nil || a.userCard.username != "小明" {
		t.Errorf("通知里的提及没有打开资料卡：%+v", a.userCard)
	}
	if a.pageName != page {
		t.Errorf("点提及连带打开了消息：%q → %q", page, a.pageName)
	}
	if err := tt.Click("图片"); err != nil {
		t.Fatalf("点击通知里的配图失败：%v（%q）", err, tt.Texts())
	}
	if a.viewer == nil || !a.viewer.open {
		t.Error("通知里的配图没有打开预览")
	}
	if a.pageName != page {
		t.Errorf("点配图连带打开了消息：%q → %q", page, a.pageName)
	}
}
