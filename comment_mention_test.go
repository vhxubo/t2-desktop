package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// displayMentions 的口径要和编辑器的 mentionBlocks 一致，只是显示允许 @名字
// 出现在段尾（评论末尾没有尾随空格也是提及）。
func TestDisplayMentions(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []displayMention
	}{
		{
			name: "中文昵称与 ASCII 昵称",
			body: "你好 @小明 和 @jimmy，谢谢",
			want: []displayMention{{Start: 3, End: 6, Name: "小明"}, {Start: 9, End: 15, Name: "jimmy"}},
		},
		{
			name: "段尾没有空格也算",
			body: "找 @小明",
			want: []displayMention{{Start: 2, End: 5, Name: "小明"}},
		},
		{
			name: "中文标点截断名字",
			body: "@小明，你好",
			want: []displayMention{{Start: 0, End: 3, Name: "小明"}},
		},
		{
			name: "邮箱里的 @ 不算",
			body: "邮件 a@example.com 到了",
			want: nil,
		},
		{
			name: "句中 @ 不算",
			body: "name@example.com",
			want: nil,
		},
		{
			name: "空的 @ 不算",
			body: "@ 后面才是名字",
			want: nil,
		},
		{
			name: "多个连续提及",
			body: "@甲 @乙 @丙",
			want: []displayMention{
				{Start: 0, End: 2, Name: "甲"},
				{Start: 3, End: 5, Name: "乙"},
				{Start: 6, End: 8, Name: "丙"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := displayMentions(tc.body)
			if len(got) != len(tc.want) {
				t.Fatalf("displayMentions(%q) = %+v, want %+v", tc.body, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("第 %d 个提及 = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// commentWithMention 造一条带提及的评论，返回渲染它的 tester。
func commentWithMention(t *testing.T, content string) (*app, *ui.Tester) {
	t.Helper()
	a, _ := emojiDetailApp(t, "正文")
	a.detail.replyList = []replyItem{{ID: "c1", Author: "dannyng", Floor: 1, Content: content}}
	a.detail.replyTotal = 1
	a.detail.replyLoaded = true
	return a, ui.NewTester(a.view, 1100, 760)
}

// 评论里 @ 与空格之间的名字要画成一块组件；点它打开这个用户的资料卡。
func TestCommentMentionChipOpensUserCard(t *testing.T) {
	a, tt := commentWithMention(t, "你好 @小明 和 @jimmy，结尾")
	for _, want := range []string{"你好 @小明 和 @jimmy，结尾", "查看 @小明 的资料", "查看 @jimmy 的资料"} {
		if !tt.HasText(want) {
			t.Errorf("提及组件没有画出来：%q（%q）", want, tt.Texts())
		}
	}
	if err := tt.Click("查看 @小明 的资料"); err != nil {
		t.Fatalf("点击提及组件失败：%v（%q）", err, tt.Texts())
	}
	if a.userCard == nil || a.userCard.username != "小明" {
		t.Errorf("点击中文提及没有打开资料卡：%+v", a.userCard)
	}
	if err := tt.Click("查看 @jimmy 的资料"); err != nil {
		t.Fatalf("点击 ASCII 提及组件失败：%v", err)
	}
	if a.userCard == nil || a.userCard.username != "jimmy" {
		t.Errorf("点击 ASCII 提及没有切换资料卡：%+v", a.userCard)
	}
}

// 邮箱里的 @ 不该变成组件；没有表情、配图的普通段落也要接管并画出组件。
func TestCommentMentionIgnoresEmail(t *testing.T) {
	_, tt := commentWithMention(t, "发到 a@example.com 就好")
	if tt.HasText("查看 @example.com 的资料") {
		t.Errorf("邮箱被误认成提及组件：%q", tt.Texts())
	}
	if !tt.HasText("a@example.com") {
		t.Errorf("邮箱文字丢了：%q", tt.Texts())
	}
}

// 提及与行内表情同时出现时，两者的对象坐标不能互相错位。
func TestCommentMentionWithEmoji(t *testing.T) {
	a, tt := commentWithMention(t, "@小明 看 :sigh:")
	if !tt.HasText("查看 @小明 的资料") || !tt.HasText("\uFFFC") {
		t.Fatalf("提及或表情没有画出来：%q", tt.Texts())
	}
	if err := tt.Click("查看 @小明 的资料"); err != nil {
		t.Fatalf("点击表情旁的提及失败：%v", err)
	}
	if a.userCard == nil || a.userCard.username != "小明" {
		t.Errorf("表情旁的提及没打开资料卡：%+v", a.userCard)
	}
}

// 提及组件只属于评论区：帖子正文照旧是普通文字，不抢正文里的链接点击。
func TestPostBodyMentionStaysPlainText(t *testing.T) {
	a, _ := emojiDetailApp(t, "@jimmy 看一下")
	tt := ui.NewTester(a.view, 1100, 760)
	if tt.HasText("查看 @jimmy 的资料") {
		t.Errorf("帖子正文的 @ 也变成了组件：%q", tt.Texts())
	}
	if !tt.HasText("@jimmy") {
		t.Errorf("帖子正文的 @ 文字丢了：%q", tt.Texts())
	}
}
