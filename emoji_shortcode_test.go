package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// emojiShortcodeUIApp 摆一篇带评论框的帖子和一份表情表。autoLoad 关着，候选
// 全部来自本地表情表，不需要任何网络。
func emojiShortcodeUIApp(t *testing.T) (*app, *ui.Tester) {
	t.Helper()
	a := openTestDetail(singleAccountWindow(t, "jimmy"))
	a.emojis = decodeEmojis([]byte(detailEmojisJSON))
	a.detail.replyTo.ID = "r1"
	tt := ui.NewTester(a.view, 1100, 900)
	tt.Frame()
	return a, tt
}

// openColon 聚焦评论框并键入 ":"，打开短码候选。
func openColon(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("写评论"); err != nil {
		t.Fatalf("点不到评论输入框：%v（%q）", err, tt.Texts())
	}
	tt.Type(":")
	tt.Frame()
}

// 键入 ":" 直接弹出社区短码候选，点一行把整段换成 ":code:"，面板自动收起。
func TestEmojiShortcodePanelOpensAndInserts(t *testing.T) {
	a, tt := emojiShortcodeUIApp(t)
	openColon(t, tt)
	if _, ok := tt.Find("表情候选面板"); !ok {
		t.Fatalf("键入 : 后没有弹出候选：%q", tt.Texts())
	}
	if _, ok := tt.Find(":sigh:"); !ok {
		t.Fatalf("候选里没有社区短码 :sigh:：%q", tt.Texts())
	}
	if _, ok := tt.Find(":collector:"); !ok {
		t.Fatalf("候选里没有第二个短码：%q", tt.Texts())
	}
	// 面板的角贴着输入的 ":"（不是居中于光标或钉在窗口角落）。
	tt.Advance(500 * time.Millisecond)
	tt.Frame()
	panel, ok := tt.Find("表情候选面板")
	if !ok {
		t.Fatalf("找不到候选面板：%q", tt.Texts())
	}
	caret, has := tt.TextCaret()
	if !has {
		t.Fatal("输入框没有上报光标位置")
	}
	dx := int(panel.X - caret.X)
	if dx < 0 {
		dx = -dx
	}
	// 行少时面板向下展开（上沿贴光标下沿），行多时向上展开（下沿贴光标上沿）；
	// 两种都算贴住 ":" 那一行。
	belowGap := int(panel.Y - (caret.Y + caret.H))
	if belowGap < 0 {
		belowGap = -belowGap
	}
	aboveGap := int((panel.Y + panel.H) - caret.Y)
	if aboveGap < 0 {
		aboveGap = -aboveGap
	}
	if dx > 40 || min(belowGap, aboveGap) > 60 {
		t.Errorf("面板的角没贴住 :：panel=%+v caret=%+v dx=%d below=%d above=%d",
			panel, caret, dx, belowGap, aboveGap)
	}

	if err := tt.Click(":sigh:"); err != nil {
		t.Fatalf("点不到候选行：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if a.detail.draft != ":sigh:" {
		t.Errorf("选中候选后的正文 = %q，期望 :sigh:", a.detail.draft)
	}
	if a.detail.emojiShortcodes.open || tt.HasText("表情候选面板") {
		t.Errorf("选中候选后面板没有收起：%q", tt.Texts())
	}
}

// 继续打字按短码过滤；选中时用整个 ":code:" 替换已经敲进去的 ":前缀"，
// 不会在正文里留下 ":si:sigh:" 这样的残段。
func TestEmojiShortcodeFiltersAndReplacesWholeToken(t *testing.T) {
	a, tt := emojiShortcodeUIApp(t)
	openColon(t, tt)
	tt.Type("si")
	tt.Frame()
	if _, ok := tt.Find(":sigh:"); !ok {
		t.Fatalf("按 si 没筛出 :sigh:：%q", tt.Texts())
	}
	if _, ok := tt.Find(":collector:"); ok {
		t.Errorf("按 si 不该还显示 :collector:：%q", tt.Texts())
	}
	if err := tt.Click(":sigh:"); err != nil {
		t.Fatalf("点不到筛选后的候选：%v", err)
	}
	tt.Frame()
	if a.detail.draft != ":sigh:" {
		t.Errorf("整段替换后的正文 = %q，期望 :sigh:", a.detail.draft)
	}
}

// 光标停在短码中间再挑候选：整段替换，光标后面的字符不能留下。
func TestEmojiShortcodePickFromMiddleReplacesWholeToken(t *testing.T) {
	a, tt := emojiShortcodeUIApp(t)
	openColon(t, tt)
	tt.Type("collect")
	tt.Frame()
	if a.detail.draft != ":collect" {
		t.Fatalf("正文 = %q，期望 :collect", a.detail.draft)
	}
	// 光标从段尾回到 ":" 后面，查询变空、候选重新全列出来。
	for i := 0; i < len("collect"); i++ {
		tt.Key(0, ui.KeyLeft)
	}
	tt.Frame()
	if a.detail.emojiShortcodes.tokenEnd != 1 {
		t.Fatalf("前缀没跟着光标走：%d", a.detail.emojiShortcodes.tokenEnd)
	}
	if err := tt.Click(":collector:"); err != nil {
		t.Fatalf("点不到候选：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if a.detail.draft != ":collector:" {
		t.Errorf("从中间选中后的正文 = %q，期望 :collector:", a.detail.draft)
	}
}

// 写完的短码是一个整体：光标落在里面按退格 / Delete，一次清掉整块。
func TestEmojiShortcodeBlockDeletedWhole(t *testing.T) {
	for _, key := range []ui.Key{ui.KeyBackspace, ui.KeyDelete} {
		a, tt := emojiShortcodeUIApp(t)
		openColon(t, tt)
		if err := tt.Click(":sigh:"); err != nil {
			t.Fatalf("点不到候选：%v", err)
		}
		tt.Frame()
		if a.detail.draft != ":sigh:" {
			t.Fatalf("选中候选后正文 = %q，期望 :sigh:", a.detail.draft)
		}
		// 光标从段尾回到短码中间，按一次键整块清掉。
		for i := 0; i < 3; i++ {
			tt.Key(0, ui.KeyLeft)
		}
		tt.Frame()
		tt.Key(0, key)
		tt.Frame()
		if a.detail.draft != "" {
			t.Errorf("按 %v 没有整块清除，正文 = %q", key, a.detail.draft)
		}
	}
}

// 选完候选后光标就在结尾冒号之后，按一次退格也应整块清掉。
func TestEmojiShortcodeDeletedWithOneBackspace(t *testing.T) {
	a, tt := emojiShortcodeUIApp(t)
	openColon(t, tt)
	if err := tt.Click(":sigh:"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Key(0, ui.KeyBackspace)
	tt.Frame()
	if a.detail.draft != "" {
		t.Errorf("退格一次没有整块清除，正文 = %q", a.detail.draft)
	}
}

// 补空格或结尾冒号表示这段写完了：候选面板自动收起，写好的字符留在正文里。
func TestEmojiShortcodeClosesOnSpaceAndColon(t *testing.T) {
	_, tt := emojiShortcodeUIApp(t)
	openColon(t, tt)
	if !tt.HasText("表情候选面板") {
		t.Fatalf("面板没打开：%q", tt.Texts())
	}
	tt.Type("si ")
	tt.Frame()
	if tt.HasText("表情候选面板") {
		t.Errorf("补了空格仍开着面板：%q", tt.Texts())
	}

	_, tt2 := emojiShortcodeUIApp(t)
	openColon(t, tt2)
	tt2.Type("sigh:")
	tt2.Frame()
	if tt2.HasText("表情候选面板") {
		t.Errorf("写完结尾冒号仍开着面板：%q", tt2.Texts())
	}
}

// 纯键盘操作：上下键换候选，回车采纳并替换前缀；Escape 收起。
func TestEmojiShortcodeKeyboardPick(t *testing.T) {
	a, tt := emojiShortcodeUIApp(t)
	openColon(t, tt)
	// 候选按短码排序：:collector: 在前，:sigh: 在后。Down 到 :sigh: 后回车。
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.detail.draft != ":sigh:" {
		t.Errorf("键盘选中的短码 = %q，期望 :sigh:", a.detail.draft)
	}
}

// 数据层：完成块的扫描、正在输入的前缀判定与整段替换范围。
func TestEmojiShortcodeTextRules(t *testing.T) {
	blocks := emojiShortcodeBlocks("hi :sigh: 和 :collector:，还有 :未完成")
	if len(blocks) != 2 {
		t.Fatalf("短码块 = %+v，期望 2 块", blocks)
	}
	if blocks[0] != (emojiBlock{3, 9}) || blocks[1] != (emojiBlock{12, 23}) {
		t.Errorf("短码块范围错误：%+v", blocks)
	}

	cases := []struct {
		body  string
		caret int
		from  int
		to    int
		ok    bool
	}{
		{":", 1, 0, 1, true},
		{":si", 3, 0, 3, true},
		{"你好 :si", 6, 3, 6, true},  // 中文后紧跟短码也认
		{":sigh:", 6, 0, 0, false}, // 结尾冒号表示写完
		{":si 后面", 4, 0, 0, false}, // 光标在空格后，不在段里
		{"a:b", 3, 0, 0, false},    // 不是短码字符
		{"::si", 4, 0, 0, false},   // 双冒号不成短码
	}
	for _, tc := range cases {
		from, to, ok := emojiShortcodeTokenAt(tc.body, tc.caret)
		if from != tc.from || to != tc.to || ok != tc.ok {
			t.Errorf("emojiShortcodeTokenAt(%q, %d) = (%d,%d,%v)，期望 (%d,%d,%v)",
				tc.body, tc.caret, from, to, ok, tc.from, tc.to, tc.ok)
		}
	}

	from, to := emojiShortcodeWordRange(":sig 后面", 0)
	if from != 0 || to != 4 {
		t.Errorf("emojiShortcodeWordRange = (%d,%d)，期望 (0,4)", from, to)
	}
	from, to = emojiShortcodeWordRange(":sigh: 后面", 0)
	if from != 0 || to != 6 {
		t.Errorf("整段范围 = (%d,%d)，期望 (0,6)", from, to)
	}
}

// 候选只来自社区短码表：系统 Unicode 表情不混进来，帖子自带的社区图标要叠上。
func TestEmojiShortcodeCandidatesIncludePostEmojis(t *testing.T) {
	a, tt := emojiShortcodeUIApp(t)
	a.detail.topic.Emojis = map[string]string{":party:": "/emojis/party.png"}
	openColon(t, tt)
	if _, ok := tt.Find(":party:"); !ok {
		t.Fatalf("帖子自带的社区短码没进候选：%q", tt.Texts())
	}
	if tt.HasText("😀") {
		t.Errorf("社区短码候选里不该混进系统表情：%q", tt.Texts())
	}
	tt.Type("part")
	tt.Frame()
	if _, ok := tt.Find(":party:"); !ok {
		t.Fatalf("按名称搜不到帖子自带的短码：%q", tt.Texts())
	}
}
