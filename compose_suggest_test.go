package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/egoist/mygo/ui"
)

// composeSuggestApp 摆一扇发帖窗口和一份本地表情表。autoLoad 关着，: 候选
// 全部来自本地表，@ 候选走「按输入」那一行，都不需要网络。
func composeSuggestApp(t *testing.T, moment bool) (*app, *composeState, *ui.Tester) {
	t.Helper()
	a := testApp(t)
	st := composeReady(a)
	a.emojis = decodeEmojis([]byte(detailEmojisJSON))
	if moment {
		st.mode = composeModeMoment
	}
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	return a, st, tt
}

// composeFocusType 点进某一个编辑框并键入一段文字。
func composeFocusType(t *testing.T, tt *ui.Tester, label, text string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatalf("点不到编辑框 %q：%v（%q）", label, err, tt.Texts())
	}
	tt.Type(text)
	tt.Frame()
}

// 帖子正文键入 ":" 弹出短码候选，点一行整段替换，面板自动收起。
func TestComposeColonOpensEmojiShortcodes(t *testing.T) {
	_, st, tt := composeSuggestApp(t, false)
	composeFocusType(t, tt, "正文内容", ":")
	if _, ok := tt.Find("表情候选面板"); !ok {
		t.Fatalf("键入 : 后没有弹出候选：%q", tt.Texts())
	}
	if _, ok := tt.Find(":sigh:"); !ok {
		t.Fatalf("候选里没有 :sigh:：%q", tt.Texts())
	}
	if err := tt.Click(":sigh:"); err != nil {
		t.Fatalf("点不到候选行：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if st.body != ":sigh:" {
		t.Errorf("插入后的正文 = %q，期望 :sigh:", st.body)
	}
	if tt.HasText("表情候选面板") {
		t.Errorf("选中候选后面板没有收起：%q", tt.Texts())
	}
}

// 继续打字按前缀过滤，选中时用整个 ":code:" 替换 ":前缀"，不留残段。
func TestComposeEmojiShortcodeFiltersAndReplacesToken(t *testing.T) {
	_, st, tt := composeSuggestApp(t, false)
	composeFocusType(t, tt, "正文内容", ":si")
	if _, ok := tt.Find(":sigh:"); !ok {
		t.Fatalf("按 si 没筛出 :sigh:：%q", tt.Texts())
	}
	if tt.HasText(":collector:") {
		t.Errorf("按 si 不该还显示 :collector:：%q", tt.Texts())
	}
	if err := tt.Click(":sigh:"); err != nil {
		t.Fatalf("点不到候选行：%v", err)
	}
	tt.Frame()
	if st.body != ":sigh:" {
		t.Errorf("整段替换后的正文 = %q，期望 :sigh:", st.body)
	}
}

// 动态正文同样键入 ":" 弹候选，选完写进动态草稿而不是帖子正文。
func TestComposeMomentColonOpensEmojiShortcodes(t *testing.T) {
	_, st, tt := composeSuggestApp(t, true)
	composeFocusType(t, tt, "动态内容", ":")
	if _, ok := tt.Find("表情候选面板"); !ok {
		t.Fatalf("动态正文键入 : 没弹候选：%q", tt.Texts())
	}
	if err := tt.Click(":sigh:"); err != nil {
		t.Fatalf("点不到候选行：%v", err)
	}
	tt.Frame()
	if st.momentDraft != ":sigh:" {
		t.Errorf("动态草稿 = %q，期望 :sigh:", st.momentDraft)
	}
	if st.body != "" {
		t.Errorf("帖子正文不该被动态编辑框改动：%q", st.body)
	}
}

// 帖子正文键入 "@前缀" 弹出提及候选，「按输入」那一行拼成 "@名字 " 写回正文。
func TestComposeAtOpensMentions(t *testing.T) {
	_, st, tt := composeSuggestApp(t, false)
	composeFocusType(t, tt, "正文内容", "@Ji")
	if _, ok := tt.Find("提及候选面板"); !ok {
		t.Fatalf("键入 @ 后没有弹出候选：%q", tt.Texts())
	}
	if err := tt.Click("@Ji"); err != nil {
		t.Fatalf("点不到「按输入」候选行：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if st.body != "@Ji " {
		t.Errorf("插入后的正文 = %q，期望 \"@Ji \"", st.body)
	}
	if tt.HasText("提及候选面板") {
		t.Errorf("选中候选后面板没有收起：%q", tt.Texts())
	}
}

// 动态正文的 @ 候选互不串味：选中写进动态草稿，帖子正文保持为空。
func TestComposeMomentAtOpensMentions(t *testing.T) {
	_, st, tt := composeSuggestApp(t, true)
	composeFocusType(t, tt, "动态内容", "@Bob")
	if _, ok := tt.Find("提及候选面板"); !ok {
		t.Fatalf("动态正文键入 @ 没弹候选：%q", tt.Texts())
	}
	if err := tt.Click("@Bob"); err != nil {
		t.Fatalf("点不到候选行：%v", err)
	}
	tt.Frame()
	if st.momentDraft != "@Bob " {
		t.Errorf("动态草稿 = %q，期望 \"@Bob \"", st.momentDraft)
	}
	if st.body != "" {
		t.Errorf("帖子正文不该被动态编辑框改动：%q", st.body)
	}
}

// : 与 @ 互斥：从 : 切到 @ 只画提及面板；再从 @ 切回 : 只画短码面板。
func TestComposeTriggerPanelsAreExclusive(t *testing.T) {
	_, st, tt := composeSuggestApp(t, false)
	composeFocusType(t, tt, "正文内容", ":si")
	if !tt.HasText("表情候选面板") {
		t.Fatalf("键入 : 没打开短码面板：%q", tt.Texts())
	}
	// 把光标挪回开头重打一个 @，短码面板应当收起。
	for i := 0; i < 3 && st.body != ""; i++ {
		tt.Key(0, ui.KeyBackspace)
		tt.Frame()
	}
	composeFocusType(t, tt, "正文内容", "@Ji")
	if !tt.HasText("提及候选面板") {
		t.Fatalf("键入 @ 没打开提及面板：%q", tt.Texts())
	}
	if tt.HasText("表情候选面板") {
		t.Errorf("打开提及面板后短码面板仍在：%q", tt.Texts())
	}
}

// 联网时 @ 会取守护者名单：名单回来后面板列出 @随机守护者与逐人候选，
// 点一行拼成 "@名字 " 写进正文，且排掉窗口自己的账号。
func TestComposeAtLoadsGuardians(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/meta/guardians":
			fmt.Fprint(w, envelopeJSON(`{"items":[`+
				`{"id":"g1","username":"jimmy"},`+
				`{"id":"g2","username":"守护姐姐"}],"total":2}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := singleAccountWindow(t, "jimmy")
	st := composeReady(a)
	a.autoLoad = true
	a.client = &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: a.auth}
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	composeFocusType(t, tt, "正文内容", "@")
	waitFor(t, func() bool { return st.bodySuggest.mentions.guardiansLoaded }, "守护者名单没有回来")
	tt.Frame()
	if !tt.HasText("@随机守护者") {
		t.Fatalf("没有 @随机守护者 快捷行：%q", tt.Texts())
	}
	if !tt.HasText("@守护姐姐") {
		t.Fatalf("守护者逐人候选没进面板：%q", tt.Texts())
	}
	if tt.HasText("@jimmy") {
		t.Errorf("窗口自己的账号不该列进候选：%q", tt.Texts())
	}
	if err := tt.Click("@守护姐姐"); err != nil {
		t.Fatalf("点不到守护者候选行：%v", err)
	}
	tt.Frame()
	if st.body != "@守护姐姐 " {
		t.Errorf("插入后的正文 = %q，期望 \"@守护姐姐 \"", st.body)
	}
}

// 键盘也能采纳候选：键入 ":si" 后回车，选中筛出来的 :sigh:。
func TestComposeEmojiShortcodeKeyboardInsert(t *testing.T) {
	_, st, tt := composeSuggestApp(t, false)
	composeFocusType(t, tt, "正文内容", ":si")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if st.body != ":sigh:" {
		t.Errorf("回车采纳后的正文 = %q，期望 :sigh:", st.body)
	}
}
