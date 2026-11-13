package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 表情短码只有一层冒号：服务端表情表的 code 自带冒号（":sigh:"），社区表里
// 的则不一定。直接拼 ":"+Code+":" 会插进「::sigh::」——正文里短码匹配不到，
// 预览与详情页就把它原样显示出来。
func TestEmojiShortcodeHasSingleColon(t *testing.T) {
	cases := map[string]string{
		":sigh:":  ":sigh:",
		"sigh":    ":sigh:",
		"::sigh:": ":sigh:",
	}
	for code, want := range cases {
		if got := emojiShortcode(emojiInfo{Code: code}); got != want {
			t.Errorf("emojiShortcode(%q) = %q，期望 %q", code, got, want)
		}
	}
	// 面板里插入的短码必须能被正文渲染认出来：替换后是行内图片，不是短码原文。
	m := decodeEmojis([]byte(detailEmojisJSON))
	src := "来 " + emojiShortcode(m[":sigh:"]) + " 一下"
	if strings.Contains(src, "::") {
		t.Errorf("插入的短码多了一层冒号：%q", src)
	}
	if !strings.Contains(replaceEmoji(src, m, true), "![叹气]") {
		t.Errorf("插入的短码没有被渲染成图片：%q", src)
	}
	// 社区表情表没带冒号时，解码后也要补成能匹配的写法。
	p, err := decodeTopic([]byte(`{"short_id":"E1","title":"x","content":"x",
		"emojis":{"party":"/emojis/party.png"}}`), "E1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Emojis[":party:"]; !ok {
		t.Errorf("社区表情短码没有补齐冒号：%+v", p.Emojis)
	}
}

// 评论草稿可以搬到独立窗口里写，再原样收回页面里的评论框：两边共用同一个
// d.draft，所以搬过去、写一段、收回来都不能丢字。
func TestReplyComposerDetachesAndRestores(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if err := tt.Click("写评论"); err != nil {
		t.Fatalf("点输入框失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	a.detail.draft = "先写一半"
	tt.Frame()

	// 展开图标把草稿搬进独立窗口：页面里换成一行说明与「收回」入口。
	if err := tt.Click("独立窗口写评论"); err != nil {
		t.Fatalf("点展开按钮失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !a.detail.composerDetached {
		t.Fatal("点展开后应进入独立窗口状态")
	}
	if !tt.HasText("正在独立窗口里写评论") || !tt.HasText("收回评论区") {
		t.Errorf("页面里没有留下说明与收回入口：%q", tt.Texts())
	}
	if _, ok := tt.Find("写评论"); ok {
		t.Errorf("独立窗口开着时页面里不该还有输入框：%q", tt.Texts())
	}
	if a.detail.draft != "先写一半" {
		t.Errorf("搬进窗口时草稿丢了：%q", a.detail.draft)
	}

	// 收回：草稿原样回到页面里的评论框，并且因为已有内容而保持展开。
	if err := tt.Click("收回评论区"); err != nil {
		t.Fatalf("点收回失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if a.detail.composerDetached {
		t.Error("收回后应回到页面里的评论框")
	}
	if _, ok := tt.Find("写评论"); !ok {
		t.Errorf("收回后没有输入框：%q", tt.Texts())
	}
	if a.detail.draft != "先写一半" {
		t.Errorf("收回后草稿丢了：%q", a.detail.draft)
	}
	if !tt.HasText("插入表情") {
		t.Errorf("收回后输入框应保持展开：%q", tt.Texts())
	}
}

// 独立窗口里左写右预览：工具条、发送、预览栏都在，草稿为空时预览给的是说明
// 而不是一片空白。
func TestReplyWindowShowsEditorAndPreview(t *testing.T) {
	a := detailAppWithReplies(t)
	a.emojis = decodeEmojis([]byte(detailEmojisJSON))
	a.detail.draft = "写点 :sigh: 试试"
	a.showReplyWindow() // headless：只摆状态，窗口本身由测试直接渲染
	if !a.detail.composerDetached || a.replyWinFor != a.detail {
		t.Fatal("打开独立窗口没有接上详情状态")
	}

	tt := ui.NewTester(a.replyWindowView, replyWindowWidth, replyWindowHeight)
	tt.Frame()
	for _, want := range []string{"写评论", "收回评论区", "发送", "插入表情", "预览"} {
		if !tt.HasText(want) {
			t.Errorf("窗口里缺少 %q：%q", want, tt.Texts())
		}
	}
	if !tt.HasText("Ctrl+Enter 发送") {
		t.Errorf("窗口底部没有发送说明：%q", tt.Texts())
	}
	// 预览把 :sigh: 换成了图片（无头渲染里图片占位是 U+FFFC），草稿本身不变。
	if !tt.HasText("\uFFFC") {
		t.Errorf("预览没有把短码换成图片：%q", tt.Texts())
	}
	if a.detail.draft != "写点 :sigh: 试试" {
		t.Errorf("预览改动了草稿：%q", a.detail.draft)
	}

	// 预览开关收起右栏，编辑区还在。
	if err := tt.Click("预览"); err != nil {
		t.Fatalf("点预览开关失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if a.detail.winPreview {
		t.Error("点预览开关没有收起预览栏")
	}
	if _, ok := tt.Find("写评论"); !ok {
		t.Errorf("收起预览后编辑区也消失了：%q", tt.Texts())
	}

	// Esc 之外的显式入口：收回评论区把草稿还给页面。
	if err := tt.Click("收回评论区"); err != nil {
		t.Fatalf("点收回失败：%v（%q）", err, tt.Texts())
	}
	if a.detail.composerDetached || a.replyWinFor != nil {
		t.Error("收回后窗口状态没有清掉")
	}
	if a.detail.draft != "写点 :sigh: 试试" {
		t.Errorf("收回后草稿丢了：%q", a.detail.draft)
	}
}

// 换帖子时独立写评论窗口要跟着收起来：它绑的是上一份草稿，留着会让人以为
// 写的是刚打开的这一篇。
func TestOpenDetailClosesReplyWindow(t *testing.T) {
	a := detailAppWithReplies(t)
	a.detail.draft = "上一帖的草稿"
	a.showReplyWindow()
	if !a.detail.composerDetached {
		t.Fatal("没有进入独立窗口状态")
	}
	a.openDetail(Item{ShortID: "other01", URL: "https://2libra.com/post/x/other01"})
	if a.replyWinFor != nil || a.detail.composerDetached {
		t.Error("换帖子后独立窗口状态没有清掉")
	}
}
