package main

import "testing"

// 纯表情评论清掉短码就什么都不剩，列表行会退回帖子标题或「（无内容）」，
// 看起来就是这条评论没有正文。这种情况保留短码，让内容至少可见。
func TestCommentExcerptKeepsPureEmoji(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{":feel_wronged:", ":feel_wronged:"},
		{" :sad:  :joy: ", ":sad: :joy:"},
		{"**:sad:**", ":sad:"},
		{":sad:。", ":sad:。"},
		// 有正文时照旧抹掉短码，避免列表里出现一串冒号单词。
		{"说得对:feel_wronged:", "说得对"},
		{"", ""},
		{"   ", ""},
	} {
		if got := commentExcerpt(tc.in); got != tc.want {
			t.Errorf("commentExcerpt(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// 纯图片评论被 Markdown 摘要规则抹成空，列表里同样会退回帖子标题。留下地址
// 才能在摘要里看到并点开预览（uitext 见 excerptView）。
func TestCommentExcerptKeepsPureImageLink(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"![" + "](https://r2.2libra.com/a.png)", "https://r2.2libra.com/a.png"},
		{"  https://r2.2libra.com/a.gif  ", "https://r2.2libra.com/a.gif"},
		// 有正文时图片语法照旧退化成说明文字。
		{"看图 ![说明](https://r2.2libra.com/a.png)", "看图 说明"},
	} {
		if got := commentExcerpt(tc.in); got != tc.want {
			t.Errorf("commentExcerpt(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

func TestCommentItemFromPureEmojiShowsShortcode(t *testing.T) {
	it := commentItemFrom(asObject([]byte(`{
		"id":"c1","content":":feel_wronged:",
		"post":{"short_id":"S1","title":"帖子标题"}
	}`)))
	if it.Title != ":feel_wronged:" || it.Extra.Excerpt != ":feel_wronged:" {
		t.Errorf("纯表情评论行 = %q / %q，期望短码原文", it.Title, it.Extra.Excerpt)
	}
}
