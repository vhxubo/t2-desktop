package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 编解码走同一套约定：插入正文的是裸 Base64，解码拿回来的必须是原文，中文也
// 不能变成问号。
func TestBase64RoundTrip(t *testing.T) {
	for _, plain := range []string{"hello", "你好", "Base64 解码测试 12345"} {
		token := encodeBase64Text(plain)
		if strings.ContainsAny(token, "[]:") {
			t.Errorf("%q 的编码里不该有标记字符：%q", plain, token)
		}
		got, ok := decodeBase64Token(token)
		if !ok || got != plain {
			t.Errorf("解码 %q 得到 %q, ok=%v，期望 %q", token, got, ok, plain)
		}
	}
	// 用户手贴的无填充串也能认；解不开和空串不能认。
	if got, ok := decodeBase64Token("aGVsbG8"); !ok || got != "hello" {
		t.Errorf("无填充 Base64 应能解码，得到 %q, ok=%v", got, ok)
	}
	for _, bad := range []string{"", "hi", "not-base64!!!", "########"} {
		if _, ok := decodeBase64Token(bad); ok {
			t.Errorf("%q 不该被当成 Base64", bad)
		}
	}
}

// 正文里夹一段编码串时要切成三段，编码串本身不交给 Markdown，否则评论里会
// 直接露出 Base64 而是不是可点的组件。
func TestSplitBase64Segments(t *testing.T) {
	token := encodeBase64Text("你好世界")
	src := "前面 " + token + " 后面"
	segs := splitBase64Segments(src)
	if len(segs) != 3 {
		t.Fatalf("切成 %d 段，期望 3 段：%+v", len(segs), segs)
	}
	if segs[0].text != "前面 " || segs[1].token != token || segs[2].text != " 后面" {
		t.Fatalf("切分结果不对：%+v", segs)
	}

	// 没有编码串时原样返回一段，评论仍走原来的 Markdown 路径。
	plain := "一段普通的评论，带个 URL https://2libra.com/post/abc 和英文 feedback"
	if segs := splitBase64Segments(plain); len(segs) != 1 || segs[0].text != plain {
		t.Fatalf("普通正文被切碎了：%+v", segs)
	}
}

// 裸 Base64 没有标记，容易误伤。长英文单词、哈希、URL 里的路径段都不能被
// 当成编码串，即使它们的字符集看起来像 Base64。
func TestSplitBase64AvoidsFalsePositives(t *testing.T) {
	for _, line := range []string{
		"这是一条普通评论 feedback from work",
		"abcdefghijklmnop",
		"打开 https://2libra.com/u/aGVsbG8 看看",
	} {
		if segs := splitBase64Segments(line); len(segs) != 1 {
			t.Errorf("%q 被误判成 Base64：%+v", line, segs)
		}
	}
}

// 工具条点「Base64 编码」开 Modal，输入原文确认后，正文里得到的是裸 Base64；
// 取消不写正文。
func TestEditorBase64DialogInsertsEncodedText(t *testing.T) {
	a := testApp(t)
	e := newMDEditor()
	body := "开头"
	// 测试里的视图没有 TextArea 帮忙同步选区，手动把光标放到正文末尾，
	// 让「插入」和真实编辑器里一样落在末尾。
	e.selStart, e.selEnd = 2, 2
	view := func(c *ui.Context) {
		a.editorToolbar(c, &e, &body)
		a.editorPanels(c, uploadTargetCompose, &e, &body, nil)
		ui.Text(c, body).Label("正文")
	}
	tt := ui.NewTester(view, 900, 700)
	tt.Frame()
	if err := tt.Click("Base64 编码"); err != nil {
		t.Fatalf("点 Base64 工具条失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !e.base64Open || !tt.HasText("编码并插入") {
		t.Fatalf("编码 Modal 没打开：%q", tt.Texts())
	}
	if err := tt.Click("原文"); err != nil {
		t.Fatalf("点原文输入框失败：%v（%q）", err, tt.Texts())
	}
	tt.Type("你好")
	tt.Frame()
	want := "开头" + encodeBase64Text("你好")
	if !tt.HasText(encodeBase64Text("你好")) {
		t.Fatalf("Modal 里没有编码预览：%q", tt.Texts())
	}
	if err := tt.Click("编码并插入"); err != nil {
		t.Fatalf("点确认失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if body != want {
		t.Errorf("正文 = %q，期望 %q", body, want)
	}
	if e.base64Open {
		t.Error("确认后 Modal 应关闭")
	}
}

// 评论框的展开态只看焦点。编码 Modal 会把焦点从输入框挪走，所以 expanded 必须
// 把 base64Open 算进去，否则 Modal 一打开评论框就缩回、editorPanels 不再构建
// Modal，窗口随即消失、焦点回来又展开——表现为点了按钮只闪一下，界面根本不出现。
func TestCommentBase64ModalStaysOpen(t *testing.T) {
	a := detailAppWithReplies(t)
	d := a.detail
	d.draft = ""
	d.replyTo = replyTarget{}
	d.preview = 0
	d.mdEditor = newMDEditor()
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("写评论"); err != nil {
		t.Fatalf("展开评论框失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	r, ok := tt.Find("Base64 编码")
	if !ok {
		t.Fatalf("找不到 Base64 按钮：%q", tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	if err := tt.Click("Base64 编码"); err != nil {
		t.Fatal(err)
	}
	// 多走几帧并推进动画时间：Modal 的进出场动画跨帧，焦点也已经落到弹窗里。
	for i := 0; i < 8; i++ {
		tt.Frame()
		tt.Advance(100 * time.Millisecond)
		if !d.mdEditor.base64Open || !tt.HasText("编码并插入") {
			t.Fatalf("第 %d 帧 Modal 没保持打开：open=%v（%q）",
				i, d.mdEditor.base64Open, tt.Texts())
		}
	}
	// 关窗后评论框恢复原样，能继续接着写。
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.mdEditor.base64Open {
		t.Error("取消后 Modal 应关闭")
	}
}

// 评论里的编码串渲染成组件：默认只显示编码串，「原文」悬浮才出现；点开后
// 是解码块，复制写进剪贴板，恢复退回编码态。
func TestCommentBase64RevealCopyRestore(t *testing.T) {
	a := detailAppWithReplies(t)
	plain := "Base64 解码测试 12345"
	token := encodeBase64Text(plain)
	a.detail.replyList = []replyItem{{ID: "cb64", Author: "alice", Floor: 1, Content: token}}
	a.detail.replyTotal = 1
	a.detail.rebuildDisplay()
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if _, ok := tt.Find(token); !ok {
		t.Fatalf("评论里没有显示编码串：%q", tt.Texts())
	}
	if _, ok := tt.Find("原文"); ok {
		t.Fatal("未悬浮时不该显示「原文」按钮")
	}

	r, ok := tt.Find(token)
	if !ok {
		t.Fatal("编码串消失")
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	if _, ok := tt.Find("原文"); !ok {
		t.Fatalf("悬浮后应出现「原文」按钮：%q", tt.Texts())
	}
	if err := tt.Click("原文"); err != nil {
		t.Fatalf("点原文失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText(plain) {
		t.Fatalf("解码后的原文没显示：%q", tt.Texts())
	}
	for _, name := range []string{"复制", "恢复"} {
		if _, ok := tt.Find(name); !ok {
			t.Errorf("解码块里缺少 %q：%q", name, tt.Texts())
		}
	}
	if err := tt.Click("复制"); err != nil {
		t.Fatalf("点复制失败：%v", err)
	}
	tt.Frame()
	if got := tt.Clipboard(); got != plain {
		t.Errorf("剪贴板 = %q，期望解码原文 %q", got, plain)
	}
	if err := tt.Click("恢复"); err != nil {
		t.Fatalf("点恢复失败：%v", err)
	}
	tt.Frame()
	if !tt.HasText(token) || tt.HasText(plain) {
		t.Errorf("恢复后应回到编码态：%q", tt.Texts())
	}
}

// 同一条评论里两段相同的编码串各自独立展开：点开其中一段，另一段仍是编码态。
func TestCommentBase64SegmentsIndependent(t *testing.T) {
	a := detailAppWithReplies(t)
	token := encodeBase64Text("独立展开")
	a.detail.replyList = []replyItem{{ID: "cb64x", Author: "alice", Floor: 1, Content: token + " " + token}}
	a.detail.replyTotal = 1
	a.detail.rebuildDisplay()
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	matches := 0
	for _, s := range tt.Texts() {
		if s == token {
			matches++
		}
	}
	if matches < 2 {
		t.Fatalf("两条相同的编码串都应显示：%q", tt.Texts())
	}
	if len(a.detail.base64Open) != 0 {
		t.Fatalf("初始不应有展开项：%+v", a.detail.base64Open)
	}
}
