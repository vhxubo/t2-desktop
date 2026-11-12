package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 一个回车就该换行：段落里的软换行在渲染前改写成硬换行。
func TestHardBreakMarkdown(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"段落软换行", "第一行\n第二行", "第一行  \n第二行"},
		{"三行", "一\n二\n三", "一  \n二  \n三"},
		{"已是硬换行", "第一行  \n第二行", "第一行  \n第二行"},
		{"反斜杠硬换行", "第一行\\\n第二行", "第一行\\\n第二行"},
		{"空行分段", "第一段\n\n第二段", "第一段\n\n第二段"},
		{"引用", "> 一\n> 二", "> 一  \n> 二"},
		{"列表续行", "- 一\n  二", "- 一  \n  二"},
		{"围栏代码块", "```\n一\n二\n```", "```\n一\n二\n```"},
		{"缩进代码块", "    一\n    二", "    一\n    二"},
		{"表格", "| 一 | 二 |\n| - | - |\n| 三 | 四 |", "| 一 | 二 |\n| - | - |\n| 三 | 四 |"},
		{"CRLF", "一\r\n二", "一  \r\n二"},
		{"没有换行", "只有一行", "只有一行"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hardBreakMarkdown(c.in); got != c.want {
				t.Errorf("hardBreakMarkdown(%q) = %q，期望 %q", c.in, got, c.want)
			}
		})
	}
}

// 改写后再走一遍改写不应继续加空格：硬换行已经是最终形态。
func TestHardBreakMarkdownIdempotent(t *testing.T) {
	in := "一\n二\n\n> 三\n> 四"
	once := hardBreakMarkdown(in)
	if twice := hardBreakMarkdown(once); twice != once {
		t.Errorf("二次改写改变了结果：%q → %q", once, twice)
	}
}

// 正文与评论里的单个回车要真的排成两行，而不是并成「第一行 第二行」。
func TestSingleNewlineRendersAsLineBreak(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "NL1", URL: "https://2libra.com/post/work/NL1"})
	a.detail.topic = &topicDetail{
		ShortID: "NL1", Title: "换行", Author: "jimmy",
		Content: "第一行\n第二行",
		URL:     "https://2libra.com/post/work/NL1",
	}
	a.detail.replyList = []replyItem{{ID: "c1", Author: "dannyng", Floor: 1, Content: "上行\n下行"}}
	a.detail.replyTotal = 1
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	for _, want := range []string{"第一行\n第二行", "上行\n下行"} {
		if !tt.HasText(want) {
			t.Errorf("单个回车没有排成换行，找不到 %q：%q", want, tt.Texts())
		}
	}
	for _, raw := range []string{"第一行 第二行", "上行 下行"} {
		if tt.HasText(raw) {
			t.Errorf("单个回车被并成了空格：%q", raw)
		}
	}
}

// 带表情的段落走 article_render.go 的自定义渲染，单个回车同样要换行。
func TestSingleNewlineRendersWithEmoji(t *testing.T) {
	a, _ := emojiDetailApp(t, "第一行 :sigh:\n第二行")
	tt := ui.NewTester(a.view, 1100, 760)
	line := ""
	for _, s := range tt.Texts() {
		if strings.Contains(s, "第一行") {
			line = s
			break
		}
	}
	if line == "" {
		t.Fatalf("自定义渲染的正文丢失：%q", tt.Texts())
	}
	if !strings.Contains(line, "\n") || !strings.Contains(line, "第二行") {
		t.Errorf("单个回车没有排成换行：%q", line)
	}
	if strings.Contains(line, "第一行 第二行") {
		t.Errorf("单个回车被并成了空格：%q", line)
	}
}
