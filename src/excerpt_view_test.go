package main

import (
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

// 最新评论摘要里的 @提及与图片链接都是可点的：提及开资料卡，图片开预览；
// 两者都不抢整行的「打开帖子」。
func TestLatestCommentExcerptMarksClickable(t *testing.T) {
	a := testApp(t)
	it := itemFrom([]byte(`{"id":"c1","content":"@小明 看 `+testExcerptImage+`","author":{"username":"alice"},`+
		`"post":{"short_id":"S1","title":"帖子"}}`), kindComment)
	a.tab = 4
	a.tabs[4].loaded = true
	a.tabs[4].items = []Item{it}
	page := a.pageName
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, label := range []string{"查看 @小明 的资料", "查看图片", "@小明 看 " + testExcerptImage} {
		if !tt.HasText(label) {
			t.Errorf("摘要缺少 %q：%q", label, tt.Texts())
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
	if err := tt.Click("查看图片"); err != nil {
		t.Fatalf("点击图片链接失败：%v（%q）", err, tt.Texts())
	}
	if a.viewer == nil || !a.viewer.open {
		t.Error("点击图片链接没有打开预览")
	}
	if a.pageName != page {
		t.Errorf("点图片连带打开了帖子：%q → %q", page, a.pageName)
	}
}

// 通知摘要里的可点片段同理：点提及开资料卡、点图片开预览，都不连带打开消息。
func TestNotificationExcerptMarksClickable(t *testing.T) {
	a := notificationWindowApp(t)
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
	if err := tt.Click("查看图片"); err != nil {
		t.Fatalf("点击通知里的图片链接失败：%v（%q）", err, tt.Texts())
	}
	if a.viewer == nil || !a.viewer.open {
		t.Error("通知里的图片链接没有打开预览")
	}
	if a.pageName != page {
		t.Errorf("点图片连带打开了消息：%q → %q", page, a.pageName)
	}
}
