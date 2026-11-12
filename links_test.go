package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The real link found in JBYGf2A's markdown body:
// [大家下班或者是周末还会去学习吗？求助](https://2libra.com/post/workplace-stories/D7UP3tD)
const (
	inlinePostURL  = "https://2libra.com/post/workplace-stories/D7UP3tD"
	inlineLinkText = "大家下班或者是周末还会去学习吗？求助"
	inlineOrigin   = "https://2libra.com/post/work/JBYGf2A"
)

// A post link must be recognised in every shape the site emits, and a
// lookalike must never be swallowed: sending a user to the in-app detail
// view for a foreign URL would hide where the content came from.
func TestParseInAppTopicLink(t *testing.T) {
	accept := []struct {
		raw, shortID, commentID, origin string
	}{
		{inlinePostURL, "D7UP3tD", "", inlinePostURL},
		{"https://2libra.com/post/D7UP3tD", "D7UP3tD", "", "https://2libra.com/post/D7UP3tD"},
		{"http://2libra.com/post/workplace-stories/D7UP3tD", "D7UP3tD", "", inlinePostURL},
		{"https://www.2libra.com/post/workplace-stories/D7UP3tD", "D7UP3tD", "", inlinePostURL},
		{"www.2libra.com/post/workplace-stories/D7UP3tD", "D7UP3tD", "", inlinePostURL},
		{"//2libra.com/post/workplace-stories/D7UP3tD", "D7UP3tD", "", inlinePostURL},
		{"/post/workplace-stories/D7UP3tD", "D7UP3tD", "", inlinePostURL},
		{"/post/D7UP3tD", "D7UP3tD", "", "https://2libra.com/post/D7UP3tD"},
		{"  " + inlinePostURL + "  ", "D7UP3tD", "", inlinePostURL},
		{inlinePostURL + "/", "D7UP3tD", "", inlinePostURL},
		{"https://2libra.com:443/post/workplace-stories/D7UP3tD", "D7UP3tD", "", inlinePostURL},
		// Chinese prose and Markdown autolinks leave punctuation behind.
		{inlinePostURL + ").", "D7UP3tD", "", inlinePostURL},
		{inlinePostURL + "。", "D7UP3tD", "", inlinePostURL},
		// Comment anchors become ?commentId= on the source link.
		{inlinePostURL + "?commentId=f86db611", "D7UP3tD", "f86db611", inlinePostURL + "?commentId=f86db611"},
		{inlinePostURL + "?comment_id=f86db611", "D7UP3tD", "f86db611", inlinePostURL + "?commentId=f86db611"},
		{inlinePostURL + "#comment-f86db611", "D7UP3tD", "f86db611", inlinePostURL + "?commentId=f86db611"},
		{inlinePostURL + "#comment_f86db611", "D7UP3tD", "f86db611", inlinePostURL + "?commentId=f86db611"},
		{inlinePostURL + "/#reply-f86db611", "D7UP3tD", "f86db611", inlinePostURL + "?commentId=f86db611"},
	}
	for _, tc := range accept {
		it, ok := parseInAppTopicLink(tc.raw)
		if !ok {
			t.Errorf("应识别站内帖子链接 %q", tc.raw)
			continue
		}
		if it.ShortID != tc.shortID || it.CommentID != tc.commentID || it.URL != tc.origin || it.Key != tc.shortID {
			t.Errorf("%q => %+v，期望 short_id=%s comment=%s url=%s",
				tc.raw, it, tc.shortID, tc.commentID, tc.origin)
		}
	}

	reject := []string{
		"",
		"https://example.com/post/workplace-stories/D7UP3tD",
		"https://2libra.com.evil.com/post/workplace-stories/D7UP3tD",
		"https://evil2libra.com/post/workplace-stories/D7UP3tD",
		"https://r2.2libra.com/post/workplace-stories/D7UP3tD",
		"//evil.com/post/workplace-stories/D7UP3tD",
		"https://2libra.com@evil.com/post/workplace-stories/D7UP3tD",
		"https://evil.com@2libra.com/post/workplace-stories/D7UP3tD",
		"https://2libra.com:8080/post/workplace-stories/D7UP3tD",
		"ftp://2libra.com/post/workplace-stories/D7UP3tD",
		"javascript:alert(1)",
		"https://2libra.com/topic/workplace-stories/D7UP3tD",
		"https://2libra.com/post/",
		"https://2libra.com/post/work/D7UP3tD/extra",
		"https://2libra.com/post/work/abc",    // short_id 太短
		"https://2libra.com/post/work/bad!id", // short_id 非法字符
		"post/work/D7UP3tD",                   // 相对当前帖子，不是站内根路径
		"https://2libra.com/openapi/v1/index.md",
	}
	for _, raw := range reject {
		if it, ok := parseInAppTopicLink(raw); ok {
			t.Errorf("不该拦截 %q => %+v", raw, it)
		}
	}
}

// Comments are rendered as plain text, so their post links need an
// explicit in-app action. Deduplicate and cap at three buttons per row.
func TestInAppTopicLinksFromPlainText(t *testing.T) {
	content := "看这篇 " + inlinePostURL + " 和 /post/work/JBYGf2A，" +
		"还有 https://2libra.com/post/work/D7UP3tD 重复，" +
		"外部 https://example.com/post/work/EXTERN9 不算。"
	links := inAppTopicLinks(content)
	if len(links) != 2 {
		t.Fatalf("应提取 2 个去重站内链接，实际 %+v", links)
	}
	if links[0].ShortID != "D7UP3tD" || links[1].ShortID != "JBYGf2A" {
		t.Errorf("链接顺序 / ID 不对：%+v", links)
	}
	if links[0].URL != inlinePostURL {
		t.Errorf("原文链接不对：%s", links[0].URL)
	}

	many := ""
	for _, id := range []string{"AAAA1111", "BBBB2222", "CCCC3333", "DDDD4444"} {
		many += " https://2libra.com/post/work/" + id
	}
	if got := inAppTopicLinks(many); len(got) != 3 {
		t.Errorf("每条评论最多 3 个按钮，实际 %d", len(got))
	}
	if got := inAppTopicLinks("纯文本评论，没有链接"); len(got) != 0 {
		t.Errorf("纯文本不应产生按钮：%+v", got)
	}
}

// Clicking a Markdown link to another post must switch the detail view in
// place, keep the previous post for "← 返回", and never call OpenURL.
func TestMarkdownPostLinkOpensInline(t *testing.T) {
	a := testApp(t) // autoLoad=false：headless 不发网络请求
	a.openDetail(Item{ShortID: "JBYGf2A", URL: inlineOrigin})
	a.detail.topic = &topicDetail{
		ShortID: "JBYGf2A",
		Title:   "继半年前的求助帖，如今顺利拿到了 offer！",
		Author:  "bighammer",
		URL:     inlineOrigin,
		Content: "半年前的求助帖： [" + inlineLinkText + "](" + inlinePostURL + ")\n\n另有 [外部资料](https://example.com/doc)。",
	}
	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText(inlineLinkText) {
		t.Fatalf("正文链接没有渲染出来：%q", tt.Texts())
	}
	if err := tt.Click(inlineLinkText); err != nil {
		t.Fatalf("点击正文站内链接失败：%v（%q）", err, tt.Texts())
	}
	if a.pageName != "detail" || a.detail == nil || a.detail.shortID != "D7UP3tD" {
		t.Fatalf("没有内联切到目标帖子：%s %+v", a.pageName, a.detail)
	}
	if len(a.detailStack) != 1 || a.detailStack[0].shortID != "JBYGf2A" {
		t.Errorf("返回栈没有记住上一篇：%+v", a.detailStack)
	}
	if got := tt.OpenedURLs(); len(got) != 0 {
		t.Errorf("站内链接被交给了系统浏览器：%q", got)
	}
	tt.Frame()
	if !tt.HasText("内联浏览 1 层") || !tt.HasText("帖子 D7UP3tD") {
		t.Errorf("缺少内联浏览提示：%q", tt.Texts())
	}
}

// Only post links are intercepted. Everything else keeps the previous
// behaviour: safe schemes open externally, unsafe ones are dropped, and a
// link back to the current post does nothing.
func TestHandleLinkExternalAndSelfLinks(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "JBYGf2A", URL: inlineOrigin})
	tt := ui.NewTester(a.view, 1200, 800) // binds a.srv to the headless host

	a.handleLink("https://example.com/doc")
	a.handleLink("mailto:hi@example.com")
	a.handleLink("javascript:alert(1)")
	a.handleLink("https://2libra.com/api/v1/topics") // 站内但不是帖子
	a.handleLink("/docs/help")                       // 站内相对路径，补上站点前缀
	a.handleLink(inlineOrigin)                       // 自链接

	want := []string{
		"https://example.com/doc",
		"mailto:hi@example.com",
		"https://2libra.com/api/v1/topics",
		"https://2libra.com/docs/help",
	}
	if got := tt.OpenedURLs(); !slices.Equal(got, want) {
		t.Errorf("外部链接处理不对：%q，期望 %q", got, want)
	}
	if len(a.detailStack) != 0 || a.detail.shortID != "JBYGf2A" {
		t.Errorf("自链接不应压栈或换页：%+v %+v", a.detail, a.detailStack)
	}
}

// A plain-text comment gets a button that opens the linked post inline.
func TestCommentPlainTextLinkButton(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "JBYGf2A", URL: inlineOrigin})
	a.detail.replyList = []replyItem{
		{ID: "c1", Author: "dannyng", Floor: 1, Content: "半年前那篇 " + inlinePostURL + " 可以看看"},
		{ID: "c2", Author: "gone", Floor: 2, Deleted: true, Content: "半年前那篇 " + inlinePostURL},
	}
	a.detail.replyTotal = 2
	a.detail.replyPage = 1
	a.detail.replyTotalPages = 1
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1200, 800)
	// 链接按钮仅在悬浮评论行时出现：先把指针移到第一行（"1 楼"）。
	if r, ok := tt.Find("1 楼"); !ok {
		t.Fatal("找不到评论行用于悬浮")
	} else {
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
	}
	btn := "在应用内打开 D7UP3tD"
	if !tt.HasText(btn) {
		t.Fatalf("评论里缺少站内链接按钮：%q", tt.Texts())
	}
	if err := tt.Click(btn); err != nil {
		t.Fatalf("点击评论链接按钮失败：%v（%q）", err, tt.Texts())
	}
	if a.detail == nil || a.detail.shortID != "D7UP3tD" || len(a.detailStack) != 1 {
		t.Errorf("评论链接没有内联打开：%+v %+v", a.detail, a.detailStack)
	}
	if got := tt.OpenedURLs(); len(got) != 0 {
		t.Errorf("评论链接被交给了系统浏览器：%q", got)
	}
}

// "← 返回" walks the inline chain first, then drops back to the list.
func TestInlineBackStackNavigation(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "POST0001", URL: "https://2libra.com/post/work/POST0001"})
	a.handleLink("https://2libra.com/post/work/POST0001") // 自链接忽略
	if len(a.detailStack) != 0 {
		t.Fatal("自链接不应压栈")
	}
	a.handleLink("https://2libra.com/post/work/POST0002")
	a.handleLink("https://2libra.com/post/work/POST0003")
	if a.detail.shortID != "POST0003" || len(a.detailStack) != 2 {
		t.Fatalf("内联跳转链不对：%+v %+v", a.detail, a.detailStack)
	}

	tt := ui.NewTester(a.view, 1200, 800)
	if !tt.HasText("内联浏览 2 层") {
		t.Errorf("缺少层数提示：%q", tt.Texts())
	}
	for _, want := range []string{"POST0002", "POST0001"} {
		if err := tt.Click("关闭详情"); err != nil {
			t.Fatalf("返回失败：%v（%q）", err, tt.Texts())
		}
		if a.detail == nil || a.detail.shortID != want {
			t.Fatalf("应回到 %s，实际 %+v", want, a.detail)
		}
		tt.Frame()
	}
	if len(a.detailStack) != 0 {
		t.Errorf("返回栈未清空：%+v", a.detailStack)
	}
	if err := tt.Click("关闭详情"); err != nil {
		t.Fatalf("最后一次返回失败：%v", err)
	}
	if a.pageName != "list" || a.detail != nil {
		t.Errorf("栈空后应回到列表：%s %+v", a.pageName, a.detail)
	}

	// Re-entering from the list starts a fresh chain.
	a.openDetail(Item{ShortID: "POST0009", URL: "https://2libra.com/post/work/POST0009"})
	if len(a.detailStack) != 0 {
		t.Errorf("从列表重新进入应清空返回栈：%+v", a.detailStack)
	}
}

// Deep chains cannot grow without bound, and draining the stack must
// always be able to get back to the list.
func TestInlineBackStackIsBounded(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "POST0000", URL: "https://2libra.com/post/work/POST0000"})
	for i := 1; i <= 30; i++ {
		a.handleLink(fmt.Sprintf("https://2libra.com/post/work/POST%04d", i))
	}
	if len(a.detailStack) > 20 {
		t.Errorf("返回栈超过 20 层：%d", len(a.detailStack))
	}
	if a.detail.shortID != "POST0030" {
		t.Errorf("最深一页不对：%s", a.detail.shortID)
	}
	for i := 0; i < 25 && a.pageName == "detail"; i++ {
		a.closeDetail()
	}
	if a.pageName != "list" || a.detail != nil || len(a.detailStack) != 0 {
		t.Errorf("清空返回栈后应回到列表：%s %+v %+v", a.pageName, a.detail, a.detailStack)
	}
}

// TWOLIBRA_LIVE=1 go test -run TestLiveInlinePostLink -v
// JBYGf2A really contains the link the user asked about. Check the whole
// chain against live data: markdown body -> parse -> handleLink -> in-app.
func TestLiveInlinePostLink(t *testing.T) {
	if os.Getenv("TWOLIBRA_LIVE") == "" {
		t.Skip("设置 TWOLIBRA_LIVE=1 才会访问真实 API")
	}
	c := &Client{}
	topic, err := c.fetchTopic(context.Background(), "JBYGf2A")
	if err != nil {
		t.Fatalf("取真实正文失败：%v", err)
	}
	links := inAppTopicLinks(topic.Content)
	if len(links) == 0 {
		t.Fatalf("真实正文里没有识别出站内帖子链接：%s", topic.Content)
	}
	t.Logf("%s · 正文 %d 字 · %d 个站内链接，首个 %s (%s)",
		topic.ShortID, len(topic.Content), len(links), links[0].ShortID, links[0].URL)

	a := testApp(t)
	a.openDetail(Item{ShortID: "JBYGf2A", URL: topic.URL})
	tt := ui.NewTester(a.view, 1200, 800) // binds a.srv
	a.handleLink(links[0].URL)
	if a.detail == nil || a.detail.shortID != links[0].ShortID {
		t.Errorf("真实站内链接没有内联打开：%+v", a.detail)
	}
	if len(a.detailStack) != 1 || a.detailStack[0].shortID != "JBYGf2A" {
		t.Errorf("真实跳转没有保留返回栈：%+v", a.detailStack)
	}
	if got := tt.OpenedURLs(); len(got) != 0 {
		t.Errorf("真实站内链接被交给了系统浏览器：%q", got)
	}
}
