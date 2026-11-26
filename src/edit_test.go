package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 编辑帖子 / 编辑评论 / 追加附言三个写端点的契约与行为：
//
//	POST /v1/topics/:short_id/edit          只传要改的字段，不带幂等键
//	POST /v1/replies/:id/edit               正文必填
//	POST /v1/topics/:short_id/postscript 🔑 必须带 Idempotency-Key
//
// 路径、字段名与「谁可以改」都按 api-write.md 原文，下面逐条断言。

func TestEditPathsMatchContract(t *testing.T) {
	if got, want := topicEditPath("a1b2"), "/v1/topics/a1b2/edit"; got != want {
		t.Errorf("编辑帖子路径 = %s，期望 %s", got, want)
	}
	if got, want := topicPostscriptPath("a1b2"), "/v1/topics/a1b2/postscript"; got != want {
		t.Errorf("附言路径 = %s，期望 %s", got, want)
	}
	if got, want := replyEditPath("c1"), "/v1/replies/c1/edit"; got != want {
		t.Errorf("编辑评论路径 = %s，期望 %s", got, want)
	}
	// 评论 id 是路径段，必须转义，不能拼出别的端点。
	if got, want := replyEditPath("a/b?c"), "/v1/replies/a%2Fb%3Fc/edit"; got != want {
		t.Errorf("评论 id 未转义：%s，期望 %s", got, want)
	}
	// 帖子 short_id 同样转义（topicPath 已经做过，这里确认没有被绕过）。
	if got, want := topicEditPath("a/b"), "/v1/topics/a%2Fb/edit"; got != want {
		t.Errorf("short_id 未转义：%s，期望 %s", got, want)
	}
}

// editRecorder 是一台假的服务端：记下方法、路径、鉴权、幂等键与请求体。
type editRecorder struct {
	mu     sync.Mutex
	calls  []string
	auth   string
	key    string
	body   map[string]any
	status int
}

func newEditServer(t *testing.T, rec *editRecorder) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		rec.mu.Lock()
		rec.calls = append(rec.calls, r.Method+" "+r.URL.Path)
		rec.auth = r.Header.Get("Authorization")
		rec.key = r.Header.Get("Idempotency-Key")
		rec.body = body
		status := rec.status
		rec.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, envelopeJSON(`{}`))
	}))
}

func (r *editRecorder) snapshot() (calls []string, auth, key string, body map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...), r.auth, r.key, r.body
}

// signedInApp 造一个已登录的应用，网络指向假服务端。
func signedInApp(t *testing.T, url string) *app {
	t.Helper()
	a := testApp(t)
	m := newAuthManager(t.TempDir())
	if err := m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	a.auth = m
	// 应用自己的请求统一从 proxyManager 的 transport 走；测试里直接把 client
	// 指向假服务端，鉴权仍由同一份 AuthManager 提供。
	a.client.Auth = m
	a.client.Base = url + "/api"
	return a
}

// 编辑帖子只发要改的字段：正文没动就不发 content，也不带幂等键（文档没有 🔑）。
func TestEditTopicSendsOnlyChangedFields(t *testing.T) {
	rec := &editRecorder{}
	srv := newEditServer(t, rec)
	defer srv.Close()
	a := signedInApp(t, srv.URL)

	if err := a.client.editTopic(context.Background(), "a1b2", map[string]any{"title": "新标题"}); err != nil {
		t.Fatalf("编辑帖子失败：%v", err)
	}
	calls, auth, key, body := rec.snapshot()
	if len(calls) != 1 || calls[0] != "POST /api/v1/topics/a1b2/edit" {
		t.Fatalf("编辑帖子请求 = %v", calls)
	}
	if auth != "Bearer tok" {
		t.Errorf("鉴权头 = %q", auth)
	}
	if key != "" {
		t.Errorf("编辑帖子不该带幂等键：%q", key)
	}
	if len(body) != 1 || body["title"] != "新标题" {
		t.Errorf("请求体 = %v，期望只有 title", body)
	}

	// 两项都改：都发出去。
	if err := a.client.editTopic(context.Background(), "a1b2",
		map[string]any{"title": "标题", "content": "正文"}); err != nil {
		t.Fatalf("编辑帖子失败：%v", err)
	}
	_, _, _, body = rec.snapshot()
	if body["title"] != "标题" || body["content"] != "正文" {
		t.Errorf("请求体 = %v，期望 title + content", body)
	}
	// 什么都不改时不该发请求。
	if err := a.client.editTopic(context.Background(), "a1b2", map[string]any{}); err == nil {
		t.Error("空请求体应当报错")
	}
}

func TestEditReplySendsContent(t *testing.T) {
	rec := &editRecorder{}
	srv := newEditServer(t, rec)
	defer srv.Close()
	a := signedInApp(t, srv.URL)

	if err := a.client.editReply(context.Background(), "c1", "改过的评论"); err != nil {
		t.Fatalf("编辑评论失败：%v", err)
	}
	calls, _, key, body := rec.snapshot()
	if len(calls) != 1 || calls[0] != "POST /api/v1/replies/c1/edit" {
		t.Fatalf("编辑评论请求 = %v", calls)
	}
	if key != "" {
		t.Errorf("编辑评论不该带幂等键：%q", key)
	}
	if body["content"] != "改过的评论" {
		t.Errorf("请求体 = %v", body)
	}
}

// 附言端点标了 🔑：必须带 Idempotency-Key，而且同一份内容重试要复用同一个键。
func TestPostscriptSendsIdempotencyKey(t *testing.T) {
	rec := &editRecorder{}
	srv := newEditServer(t, rec)
	defer srv.Close()
	a := signedInApp(t, srv.URL)

	st := newPostscriptState("a1b2", nil, a.md)
	st.body = "补充两句"
	key := st.postscriptKey()
	if key == "" {
		t.Fatal("附言必须铸一个幂等键")
	}
	if again := st.postscriptKey(); again != key {
		t.Errorf("同一份正文重试换了键：%q -> %q", key, again)
	}
	if err := a.client.addPostscript(context.Background(), "a1b2", st.body, key); err != nil {
		t.Fatalf("追加附言失败：%v", err)
	}
	calls, _, got, body := rec.snapshot()
	if len(calls) != 1 || calls[0] != "POST /api/v1/topics/a1b2/postscript" {
		t.Fatalf("附言请求 = %v", calls)
	}
	if got != key {
		t.Errorf("幂等键 = %q，期望 %q", got, key)
	}
	if body["content"] != "补充两句" {
		t.Errorf("请求体 = %v", body)
	}

	// 正文改了就要换新键：同一个键配不同正文会被判 409。
	st.body = "又改了一句"
	if next := st.postscriptKey(); next == key {
		t.Error("正文变了还复用旧幂等键")
	}
}

func TestEditPayloadValidation(t *testing.T) {
	topic := &topicDetail{ShortID: "a1b2", Title: "标题", Content: "正文"}
	base := newTopicEditState(topic, nil)

	// 标题清空：接口要求「传了就必须非空」，本地就拦住。
	empty := *base
	empty.title = "   "
	if _, err := empty.payload(); err == nil || !strings.Contains(err.Error(), "标题不能为空") {
		t.Errorf("空标题应报错：%v", err)
	}

	// 正文清空：不能静默保留旧正文，直接拦住。
	cleared := *base
	cleared.body = ""
	if _, err := cleared.payload(); err == nil || !strings.Contains(err.Error(), "正文不能为空") {
		t.Errorf("清空正文应报错：%v", err)
	}

	// 原文本来就没有正文：不发 content，只改标题。
	bare := newTopicEditState(&topicDetail{ShortID: "a1b2", Title: "标题"}, nil)
	p, err := bare.payload()
	if err != nil {
		t.Fatalf("空正文的帖子只改标题应当允许：%v", err)
	}
	if _, ok := p["content"]; ok {
		t.Errorf("没有正文时不该发 content：%v", p)
	}

	// 超长正文 / 标题。
	long := *base
	long.body = strings.Repeat("字", composeContentMax+1)
	if _, err := long.payload(); err == nil || !strings.Contains(err.Error(), "正文最多") {
		t.Errorf("超长正文应报错：%v", err)
	}
	longTitle := *base
	longTitle.title = strings.Repeat("字", composeTitleMax+1)
	if _, err := longTitle.payload(); err == nil || !strings.Contains(err.Error(), "标题最多") {
		t.Errorf("超长标题应报错：%v", err)
	}

	// 图片还没传完：正文里的占位符不能发出去。
	pending := *base
	pending.body = "正文 ![图](upload:7)"
	if _, err := pending.payload(); err == nil || !strings.Contains(err.Error(), "图片未处理完") {
		t.Errorf("未完成的图片应拦住：%v", err)
	}

	// 评论与附言：正文不能为空。
	reply := newReplyEditState("a1b2", replyItem{ID: "c1", Content: "旧评论"}, nil)
	reply.body = "  "
	if _, err := reply.payload(); err == nil || !strings.Contains(err.Error(), "评论内容不能为空") {
		t.Errorf("空评论应报错：%v", err)
	}
	ps := newPostscriptState("a1b2", nil, nil)
	if _, err := ps.payload(); err == nil || !strings.Contains(err.Error(), "附言不能为空") {
		t.Errorf("空附言应报错：%v", err)
	}
}

// changed 只在真的改了东西时为真：多一个空格也算改动，正文按原样比较。
func TestEditChanged(t *testing.T) {
	st := newTopicEditState(&topicDetail{ShortID: "a1b2", Title: "标题", Content: "正文"}, nil)
	if st.changed() {
		t.Error("刚打开就报有改动")
	}
	st.title = " 标题 "
	if st.changed() {
		t.Error("标题只有首尾空白不算改动")
	}
	st.body = "正文 "
	if !st.changed() {
		t.Error("正文多一个空格也是改动")
	}
	r := newReplyEditState("a1b2", replyItem{ID: "c1", Content: "旧评论"}, nil)
	if r.changed() {
		t.Error("评论刚打开就报有改动")
	}
	r.body = "新评论"
	if !r.changed() {
		t.Error("评论改了却没认出来")
	}
}

// 详情里的可见性门槛要能读出来，编辑窗口才能显示「当前」并只在改动时发送。
func TestDecodeTopicAccessGuard(t *testing.T) {
	topic, err := decodeTopic([]byte(`{
		"short_id":"g1","title":"限制帖",
		"access_guard_type":"attribute","access_guard_attr":"力量","access_guard_attr_value":3
	}`), "g1")
	if err != nil {
		t.Fatal(err)
	}
	if !topic.GuardKnown || topic.GuardType != guardAttribute ||
		topic.GuardAttr != "力量" || topic.GuardAttrValue != 3 {
		t.Errorf("顶层门槛没有解出来：%+v", topic)
	}

	nested, err := decodeTopic([]byte(`{
		"short_id":"g2","title":"限制帖",
		"access_guard":{"type":"level","level":5}
	}`), "g2")
	if err != nil {
		t.Fatal(err)
	}
	if !nested.GuardKnown || nested.GuardType != guardLevel || nested.GuardLevel != 5 {
		t.Errorf("嵌套门槛没有解出来：%+v", nested)
	}

	absurd, err := decodeTopic([]byte(`{"short_id":"g3","title":"普通帖"}`), "g3")
	if err != nil {
		t.Fatal(err)
	}
	if absurd.GuardKnown {
		t.Error("没有门槛字段时不该声称知道当前限制")
	}
}

// 编辑窗口的可见范围：原值没读到时默认「保持当前设置」，作者主动选了才发
// access_guard_*；清除限制必须显式发 "none"。
func TestEditGuardPayload(t *testing.T) {
	level := &topicDetail{
		ShortID: "a1b2", Title: "标题", Content: "正文",
		GuardKnown: true, GuardType: guardLevel, GuardLevel: 5,
	}
	st := newTopicEditState(level, nil)
	if st.guardChoice != editGuardLabel(guardLevel) || st.guardLevel != 5 {
		t.Errorf("门槛没有预填：choice=%q level=%v", st.guardChoice, st.guardLevel)
	}
	if st.changed() {
		t.Error("刚打开就报有改动")
	}
	p, err := st.payload()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p["access_guard_type"]; ok {
		t.Errorf("没改可见范围却发了门槛字段：%v", p)
	}

	// 只改等级：发 type + level，不发属性字段。
	st.guardLevel = 6
	if !st.changed() {
		t.Error("改了等级却没认出来")
	}
	p, err = st.payload()
	if err != nil {
		t.Fatal(err)
	}
	if p["access_guard_type"] != guardLevel || p["access_guard_level"] != 6 {
		t.Errorf("等级门槛请求体 = %v", p)
	}
	if _, ok := p["access_guard_attr"]; ok {
		t.Errorf("等级门槛不该带属性：%v", p)
	}

	// 清除限制：必须是 "none"，不是省略字段。
	st.guardChoice = editGuardLabel(guardNone)
	p, err = st.payload()
	if err != nil {
		t.Fatal(err)
	}
	if p["access_guard_type"] != "none" {
		t.Errorf("清除限制请求体 = %v", p)
	}
	if _, ok := p["access_guard_level"]; ok {
		t.Errorf("清除限制不该再带等级：%v", p)
	}

	// 属性门槛：下拉框存中文标签，请求体发官网字典的机器取值，数值 0～10。
	attr := newTopicEditState(&topicDetail{
		ShortID: "a1b2", Title: "标题", Content: "正文",
		GuardKnown: true, GuardType: guardAttribute, GuardAttr: "karma", GuardAttrValue: 2,
	}, nil)
	if attr.guardAttr != "财富" {
		t.Errorf("属性门槛的初始标签应为财富，实际 %q", attr.guardAttr)
	}
	attr.guardChoice = editGuardLabel(guardAttribute)
	attr.guardAttr = ""
	if _, err := attr.payload(); err == nil || !strings.Contains(err.Error(), "D20") {
		t.Errorf("没选属性应报错：%v", err)
	}
	attr.guardAttr = "力量"
	if _, err := attr.payload(); err == nil {
		t.Errorf("属性不在 D20 字典里应报错")
	}
	attr.guardAttr = "财富"
	attr.guardAttrValue = 4
	p, err = attr.payload()
	if err != nil {
		t.Fatal(err)
	}
	if p["access_guard_type"] != guardAttribute || p["access_guard_attr"] != "karma" ||
		p["access_guard_attr_value"] != 4 {
		t.Errorf("属性门槛请求体 = %v", p)
	}

	// 原值未知：默认严格保持，主动选「所有人可见」才算一次明确的重设。
	unknown := newTopicEditState(&topicDetail{ShortID: "a1b2", Title: "标题", Content: "正文"}, nil)
	if unknown.guardChoice != editGuardKeep || unknown.guardChanged() {
		t.Errorf("原值未知时不该默认改门槛：choice=%q", unknown.guardChoice)
	}
	unknown.guardChoice = editGuardLabel(guardNone)
	if !unknown.guardChanged() {
		t.Error("主动清除门槛没有被认出来")
	}
	p, err = unknown.payload()
	if err != nil {
		t.Fatal(err)
	}
	if p["access_guard_type"] != "none" {
		t.Errorf("未知原值时清除门槛请求体 = %v", p)
	}
}

func TestEditGuardSummaryMapsD20Attr(t *testing.T) {
	if got := editGuardSummary(guardSelection{Type: guardAttribute, Attr: "karma", AttrValue: 3}); got != "财富 ≥ 3 可见" {
		t.Errorf("机器取值应显示中文标签：%q", got)
	}
	if got := editGuardSummary(guardSelection{Type: guardAttribute, Attr: "力量", AttrValue: 3}); got != "力量 ≥ 3 可见" {
		t.Errorf("未知取值应原样显示：%q", got)
	}
}

// 编辑窗口把当前可见范围摆在选项下方；改成「所有人可见」后请求里带 none。
func TestTopicEditSendsGuardChange(t *testing.T) {
	rec := &editRecorder{}
	srv := newEditServer(t, rec)
	defer srv.Close()
	a := signedInApp(t, srv.URL)

	a.detail = &detailState{
		shortID: "a1b2",
		topic: &topicDetail{
			ShortID: "a1b2", Title: "标题", Content: "正文", Author: "jimmy",
			GuardKnown: true, GuardType: guardLogin,
		},
	}
	st := newTopicEditState(a.detail.topic, a.md)
	st.guardChoice = editGuardLabel(guardNone)
	a.edit = st

	tt := ui.NewTester(a.editWindowView, editWindowWidth, editWindowHeight)
	tt.Frame()
	if !tt.HasText("当前：登录用户可见。") {
		t.Errorf("没有显示当前可见范围：%q", tt.Texts())
	}
	if err := tt.Click("保存修改"); err != nil {
		t.Fatalf("点不到保存：%v", err)
	}
	waitFor(t, func() bool { return a.edit == nil }, "保存请求没有完成")

	_, _, _, body := rec.snapshot()
	if body["access_guard_type"] != "none" {
		t.Errorf("清除限制请求体 = %v", body)
	}
	if !a.detail.topic.GuardKnown || a.detail.topic.GuardType != guardNone {
		t.Errorf("页面上的门槛没有更新：%+v", a.detail.topic)
	}
}

// 入口只给作者本人：编辑接口非作者返回 403，界面上不该出现点了必然失败的按钮。
func TestEditEntryOnlyForAuthor(t *testing.T) {
	a := testApp(t)
	m := newAuthManager(t.TempDir())
	if err := m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	a.auth = m

	if !a.isMine("jimmy") {
		t.Error("自己的昵称应当认得出来")
	}
	if a.isMine("alice") {
		t.Error("别人的昵称不该当成自己")
	}
	if a.isMine("匿名") || a.isMine("") {
		t.Error("匿名帖无法确认作者，不能给入口")
	}

	d := &detailState{shortID: "a1b2", topic: &topicDetail{ShortID: "a1b2", Author: "jimmy"}}
	if !a.canEditTopic(d) {
		t.Error("自己的帖子应当可以编辑")
	}
	d.topic.Author = "alice"
	if a.canEditTopic(d) {
		t.Error("别人的帖子不该给编辑入口")
	}
	if a.canEditTopic(&detailState{}) {
		t.Error("没有正文时不该给编辑入口")
	}

	if !a.canEditReply(replyItem{ID: "c1", Author: "jimmy"}) {
		t.Error("自己的评论应当可以编辑")
	}
	if a.canEditReply(replyItem{ID: "c1", Author: "jimmy", Deleted: true}) {
		t.Error("已删除的评论不该给编辑入口")
	}
	if a.canEditReply(replyItem{ID: "c1", Author: "alice"}) {
		t.Error("别人的评论不该给编辑入口")
	}
	if a.canEditReply(replyItem{Author: "jimmy"}) {
		t.Error("没有 id 的评论无法编辑")
	}
}

// 从详情页点进来的三个入口都要把原文摆进编辑窗口：编辑不是从空白开始的。
func TestEditEntriesPrefillFromPage(t *testing.T) {
	a := testApp(t)
	a.detail = &detailState{
		shortID: "a1b2",
		topic: &topicDetail{ShortID: "a1b2", Title: "旧标题", Content: "旧正文",
			Author: "jimmy", Emojis: map[string]string{":mine:": "https://x/1.png"}},
	}

	a.showTopicEdit(a.detail)
	if a.edit == nil || a.edit.kind != editKindTopic {
		t.Fatalf("没有打开编辑帖子：%+v", a.edit)
	}
	if a.edit.title != "旧标题" || a.edit.body != "旧正文" {
		t.Errorf("编辑帖子没有带原文：%q / %q", a.edit.title, a.edit.body)
	}
	if a.edit.emojis[":mine:"] == "" {
		t.Error("编辑帖子没有带上帖子的社区表情")
	}

	a.showPostscript(a.detail)
	if a.edit == nil || a.edit.kind != editKindPostscript {
		t.Fatalf("没有打开追加附言：%+v", a.edit)
	}
	if a.edit.body != "" || !a.edit.focusBody {
		t.Errorf("附言应当从空正文开始并把焦点放进正文：%+v", a.edit)
	}

	reply := replyItem{ID: "c1", Content: "旧评论", Author: "jimmy"}
	a.showReplyEdit(a.detail, reply)
	if a.edit == nil || a.edit.kind != editKindReply || a.edit.replyID != "c1" {
		t.Fatalf("没有打开编辑评论：%+v", a.edit)
	}
	if a.edit.body != "旧评论" {
		t.Errorf("编辑评论没有带原文：%q", a.edit.body)
	}
	// 图片上传也要落在编辑窗口的正文里，提示弹在编辑窗口。
	if body, ed := a.editorState(uploadTargetEdit); body == nil || ed == nil {
		t.Error("编辑窗口没有接上上传目标")
	} else if body != &a.edit.body {
		t.Error("上传目标指错了正文")
	}
	if got := a.editorToastTarget(uploadTargetEdit); got != toastEdit {
		t.Errorf("编辑窗口的上传提示目标 = %v", got)
	}
}

// 已经开着的窗口里有未保存的改动时，点别的编辑入口不会把它覆盖掉。
func TestEditWindowKeepsUnsavedDraft(t *testing.T) {
	a := testApp(t)
	a.detail = &detailState{
		shortID: "a1b2",
		topic:   &topicDetail{ShortID: "a1b2", Title: "旧标题", Content: "旧正文", Author: "jimmy"},
	}
	a.edit = newTopicEditState(a.detail.topic, a.md)
	a.edit.body = "改到一半"
	// 没有窗口对象时（headless）同样按「窗口开着」处理：状态已经在了。
	a.openEditWindow(newPostscriptState("a1b2", nil, a.md))
	if a.edit.kind != editKindTopic || a.edit.body != "改到一半" {
		t.Errorf("未保存的草稿被覆盖了：%+v", a.edit)
	}
	if !queuedToast(a, "还有未保存的修改，请先保存或取消") {
		t.Error("没有提示草稿未保存")
	}
}

// queuedToast 报告队列里有没有这条提示：编辑窗口保存成功后自己会关掉，
// 提示按规则退到主窗口，所以直接看队列。
func queuedToast(a *app, want string) bool {
	a.toastMu.Lock()
	defer a.toastMu.Unlock()
	for _, n := range a.toasts {
		if n.text == want {
			return true
		}
	}
	return false
}

// 编辑帖子走完整链路：窗口里改标题与正文 → 点保存 → 请求发出 → 页面与列表行
// 就地更新 → 窗口关掉并报告结果。
func TestTopicEditWindowSavesAndUpdatesPage(t *testing.T) {
	rec := &editRecorder{}
	srv := newEditServer(t, rec)
	defer srv.Close()
	a := signedInApp(t, srv.URL)

	a.detail = &detailState{
		shortID: "a1b2",
		topic:   &topicDetail{ShortID: "a1b2", Title: "旧标题", Content: "旧正文", Author: "jimmy"},
	}
	a.tabs[0].items = []Item{{ShortID: "a1b2", Title: "旧标题"}}
	st := newTopicEditState(a.detail.topic, a.md)
	st.title = "新标题"
	st.body = "新正文"
	a.edit = st

	tt := ui.NewTester(a.editWindowView, editWindowWidth, editWindowHeight)
	tt.Frame()
	if !tt.HasText("编辑帖子") {
		t.Errorf("窗口标题不对：%q", tt.Texts())
	}
	if _, ok := tt.Find("保存修改"); !ok {
		t.Fatalf("找不到保存按钮：%q", tt.Texts())
	}
	if err := tt.Click("保存修改"); err != nil {
		t.Fatalf("点不到保存：%v", err)
	}
	// 等提示入队，不等窗口关掉：关窗口在提示之前，两者之间还隔着几步。
	waitForToast(t, a, "帖子已更新")

	calls, _, _, body := rec.snapshot()
	if len(calls) != 1 || calls[0] != "POST /api/v1/topics/a1b2/edit" {
		t.Fatalf("保存请求 = %v", calls)
	}
	if body["title"] != "新标题" || body["content"] != "新正文" {
		t.Errorf("请求体 = %v", body)
	}
	if a.detail.topic.Title != "新标题" || a.detail.topic.Content != "新正文" {
		t.Errorf("页面上的正文没更新：%+v", a.detail.topic)
	}
	if got := a.tabs[0].items[0].Title; got != "新标题" {
		t.Errorf("列表行标题 = %q，期望新标题", got)
	}
	// 本地缓存也要跟着改：下次打开先显示缓存，不该退回旧正文。
	if c, ok := a.readCachedDetail("a1b2"); ok && c.Topic.Title != "新标题" {
		t.Errorf("缓存里的标题 = %q", c.Topic.Title)
	}
}

// 追加附言：窗口里只有正文，发布后追加到正文末尾，并说明「不能修改或删除」。
func TestPostscriptWindowAppends(t *testing.T) {
	rec := &editRecorder{}
	srv := newEditServer(t, rec)
	defer srv.Close()
	a := signedInApp(t, srv.URL)

	a.detail = &detailState{
		shortID: "a1b2",
		topic:   &topicDetail{ShortID: "a1b2", Title: "标题", Content: "正文", Author: "jimmy"},
	}
	st := newPostscriptState("a1b2", nil, a.md)
	st.body = "补充：这件事有后续"
	a.edit = st

	tt := ui.NewTester(a.editWindowView, editWindowWidth, editWindowHeight)
	tt.Frame()
	if !tt.HasText("附言会追加到帖子末尾，发布后不能修改或删除。") {
		t.Errorf("没有说明附言不可编辑：%q", tt.Texts())
	}
	if err := tt.Click("发布附言"); err != nil {
		t.Fatalf("点不到发布附言：%v", err)
	}
	waitFor(t, func() bool { return queuedToast(a, "附言已追加") }, "没有报告附言结果")
	if a.edit != nil {
		t.Error("附言发布后没有关闭编辑窗口")
	}

	calls, _, key, body := rec.snapshot()
	if len(calls) != 1 || calls[0] != "POST /api/v1/topics/a1b2/postscript" {
		t.Fatalf("附言请求 = %v", calls)
	}
	if key == "" {
		t.Error("附言必须带 Idempotency-Key")
	}
	if body["content"] != "补充：这件事有后续" {
		t.Errorf("请求体 = %v", body)
	}
	if n := len(a.detail.topic.Postscripts); n != 1 {
		t.Fatalf("附言没有追加到页面上：%d 条", n)
	}
	if got := a.detail.topic.Postscripts[0].Content; got != "补充：这件事有后续" {
		t.Errorf("附言内容 = %q", got)
	}
}

// 编辑评论：窗口里改正文 → 保存 → 评论列表里那一条就地更新。
func TestReplyEditWindowUpdatesComment(t *testing.T) {
	rec := &editRecorder{}
	srv := newEditServer(t, rec)
	defer srv.Close()
	a := signedInApp(t, srv.URL)

	a.detail = &detailState{
		shortID:   "a1b2",
		topic:     &topicDetail{ShortID: "a1b2", Author: "jimmy"},
		replyList: []replyItem{{ID: "c1", Content: "旧评论", Author: "jimmy", Floor: 1}},
	}
	a.detail.rebuildDisplay()
	st := newReplyEditState("a1b2", a.detail.replyList[0], a.commentMd)
	st.body = "改过的评论"
	a.edit = st

	tt := ui.NewTester(a.editWindowView, editWindowWidth, editWindowHeight)
	tt.Frame()
	if !tt.HasText("编辑评论") {
		t.Errorf("窗口标题不对：%q", tt.Texts())
	}
	if err := tt.Click("保存修改"); err != nil {
		t.Fatalf("点不到保存：%v", err)
	}
	// 等提示入队，不等窗口关掉：关窗口在提示之前，两者之间还隔着几步。
	waitForToast(t, a, "评论已更新")

	calls, _, _, body := rec.snapshot()
	if len(calls) != 1 || calls[0] != "POST /api/v1/replies/c1/edit" {
		t.Fatalf("保存请求 = %v", calls)
	}
	if body["content"] != "改过的评论" {
		t.Errorf("请求体 = %v", body)
	}
	if got := a.detail.replyList[0].Content; got != "改过的评论" {
		t.Errorf("评论正文没更新：%q", got)
	}
}

// 没有改动时保存按钮是禁用的：点下去什么也不会发生，也不会发一个内容相同的
// 请求。绕过按钮直接调提交入口同样被拦住（见 TestEditSubmitWithoutChanges）。
func TestEditSaveDisabledWithoutChanges(t *testing.T) {
	a := testApp(t)
	st := newTopicEditState(&topicDetail{ShortID: "a1b2", Title: "标题", Content: "正文"}, a.md)
	a.edit = st
	tt := ui.NewTester(a.editWindowView, editWindowWidth, editWindowHeight)
	tt.Frame()
	if !tt.HasText("还没有改动") {
		t.Errorf("没有提示「还没有改动」：%q", tt.Texts())
	}
	if _, ok := tt.Find("保存修改"); !ok {
		t.Fatalf("找不到保存按钮：%q", tt.Texts())
	}
	if err := tt.Click("保存修改"); err != nil {
		t.Fatalf("点不到保存：%v", err)
	}
	if a.edit == nil {
		t.Error("没有改动时不该关掉窗口")
	}
	a.toastMu.Lock()
	n := len(a.toasts)
	a.toastMu.Unlock()
	if n != 0 {
		t.Errorf("禁用按钮不该触发任何动作：%v", a.toasts)
	}
}

// 内容没变时提交入口直接拦住：不发请求，也不关窗口。
func TestEditSubmitWithoutChanges(t *testing.T) {
	rec := &editRecorder{}
	srv := newEditServer(t, rec)
	defer srv.Close()
	a := signedInApp(t, srv.URL)

	a.detail = &detailState{
		shortID: "a1b2",
		topic:   &topicDetail{ShortID: "a1b2", Title: "标题", Content: "正文", Author: "jimmy"},
	}
	a.edit = newTopicEditState(a.detail.topic, a.md)
	a.submitEdit()

	calls, _, _, _ := rec.snapshot()
	if len(calls) != 0 {
		t.Errorf("没有改动却发了请求：%v", calls)
	}
	if a.edit == nil {
		t.Error("没有改动时不该关掉窗口")
	}
	if !queuedToast(a, "没有改动") {
		t.Error("没有改动时应当说明一句")
	}
}

// 服务端拒绝（例如已经不是作者）时窗口留着，原因用 toast 报告，正文不丢。
func TestEditFailureKeepsDraft(t *testing.T) {
	rec := &editRecorder{status: http.StatusForbidden}
	srv := newEditServer(t, rec)
	defer srv.Close()
	a := signedInApp(t, srv.URL)

	a.detail = &detailState{
		shortID: "a1b2",
		topic:   &topicDetail{ShortID: "a1b2", Title: "旧标题", Content: "旧正文", Author: "jimmy"},
	}
	st := newTopicEditState(a.detail.topic, a.md)
	st.body = "新正文"
	a.edit = st
	a.submitEdit()
	// 失败原因也排在提示队列里，而且排在 busy 之后：等提示到了才算收尾。
	waitFor(t, func() bool { return !a.edit.busy && len(toastTexts(a)) > 0 }, "保存请求没有结束")

	if a.edit == nil {
		t.Fatal("失败后窗口不该关掉")
	}
	if a.edit.body != "新正文" {
		t.Errorf("失败后草稿被清掉了：%q", a.edit.body)
	}
	if a.detail.topic.Content != "旧正文" {
		t.Errorf("失败却改了页面正文：%q", a.detail.topic.Content)
	}
}
