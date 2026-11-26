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
	"time"

	"github.com/egoist/mygo/ui"
)

// 改节点端点与两个编辑窗口的契约：
//
//	POST /v1/topics/:short_id/node 🔑  body 只有 node_id，回 d.node
//	can_edit / can_edit_node 是窗口状态，不是权限判断
//	400 edit_window_expired 隐藏入口、不重试
//
// 路径、字段名、幂等键和错误分支都按 api-write.md / api-read.md / changelog.md 断言。

func TestTopicNodePathMatchesContract(t *testing.T) {
	if got, want := topicNodePath("a1b2"), "/v1/topics/a1b2/node"; got != want {
		t.Errorf("改节点路径 = %s，期望 %s", got, want)
	}
	if got, want := topicNodePath("a/b"), "/v1/topics/a%2Fb/node"; got != want {
		t.Errorf("short_id 没有转义：%s，期望 %s", got, want)
	}
	if topicNodeScope != "write:topic" {
		t.Errorf("改节点 scope = %q，期望 write:topic", topicNodeScope)
	}
}

type nodeRec struct {
	mu     sync.Mutex
	calls  int
	method string
	path   string
	auth   string
	key    string
	body   map[string]any
	code   string
	status int
}

func (r *nodeRec) snapshot() (calls int, method, path, auth, key string, body map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.method, r.path, r.auth, r.key, r.body
}

func newNodeServer(t *testing.T, rec *nodeRec) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, rq *http.Request) {
		raw, _ := io.ReadAll(rq.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		rec.mu.Lock()
		rec.calls++
		rec.method, rec.path, rec.auth, rec.key, rec.body = rq.Method, rq.URL.Path, rq.Header.Get("Authorization"), rq.Header.Get("Idempotency-Key"), body
		status, code := rec.status, rec.code
		rec.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if code != "" {
			_, _ = fmt.Fprintf(w, `{"c":%d,"code":%q,"m":"窗口已过","d":null,"t":0}`, status, code)
			return
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{"node":{"id":"n2","slug":"life","name":"生活","extra":1}}`))
	}))
}

// 端点只发 node_id（不是 slug），带鉴权与幂等键；响应只认 d.node 的三个字段。
func TestSetTopicNodeRequestAndResponse(t *testing.T) {
	rec := &nodeRec{}
	srv := newNodeServer(t, rec)
	defer srv.Close()
	a := signedInApp(t, srv.URL)

	res, err := a.client.setTopicNode(context.Background(), "a1b2", "n2", "key-1")
	if err != nil {
		t.Fatalf("改节点失败：%v", err)
	}
	if res.ID != "n2" || res.Slug != "life" || res.Name != "生活" {
		t.Fatalf("响应解码 = %+v", res)
	}
	calls, method, path, auth, key, body := rec.snapshot()
	if calls != 1 || method != http.MethodPost || path != "/api/v1/topics/a1b2/node" {
		t.Fatalf("请求 = %d / %s %s", calls, method, path)
	}
	if auth != "Bearer tok" || key != "key-1" {
		t.Errorf("鉴权 / 幂等键 = %q / %q", auth, key)
	}
	if len(body) != 1 || body["node_id"] != "n2" {
		t.Errorf("请求体 = %v，期望只有 node_id", body)
	}
	if _, err := a.client.setTopicNode(context.Background(), "a1b2", "", "key-2"); err == nil {
		t.Error("空 node_id 不该发请求")
	}
}

func TestEditWindowExpiredError(t *testing.T) {
	rec := &nodeRec{code: "edit_window_expired", status: http.StatusBadRequest}
	srv := newNodeServer(t, rec)
	defer srv.Close()
	a := signedInApp(t, srv.URL)

	_, err := a.client.setTopicNode(context.Background(), "a1b2", "n2", "k")
	if err == nil || !isEditWindowExpired(err) {
		t.Fatalf("应当识别 400 edit_window_expired，实际 %v", err)
	}
	if got := friendlyError(err); !strings.Contains(got, "入口已关闭") {
		t.Errorf("过期提示 = %q，不该引导重试", got)
	}
	// 其它 400 仍是普通参数错误，别把 invalid_request 当成窗口过期。
	other := &apiError{Status: 400, Code: "invalid_request", Message: "黑名单节点"}
	if isEditWindowExpired(other) || !strings.Contains(friendlyError(other), "invalid_request") {
		t.Error("invalid_request 不该被当成窗口过期")
	}
}

// 详情里的两个字段是窗口状态：明确给出 false 就藏入口，没给时退回作者判断。
func TestDecodeEditWindowFlags(t *testing.T) {
	env := envelopeJSON(`{"short_id":"a1b2","can_edit":true,"can_edit_node":false}`)
	var e envelope
	if err := json.Unmarshal([]byte(env), &e); err != nil {
		t.Fatal(err)
	}
	got, err := decodeTopic(e.D, "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if !got.CanEditKnown || !got.CanEdit || !got.CanEditNodeKnown || got.CanEditNode {
		t.Fatalf("窗口状态解码错了：%+v", got)
	}
	old, err := decodeTopic(json.RawMessage(`{"short_id":"a1b2"}`), "a1b2")
	if err != nil {
		t.Fatal(err)
	}
	if old.CanEditKnown || old.CanEditNodeKnown {
		t.Error("老响应没带字段时不该标成已知")
	}
}

// 无权时评论接口回 d:null，不能画成「暂无评论」。
func TestDecodeRepliesNullIsBlocked(t *testing.T) {
	got := decodeReplies(json.RawMessage(`null`))
	if !got.Blocked || len(got.Items) != 0 {
		t.Fatalf("d:null 应当标成不可见：%+v", got)
	}
	normal := decodeReplies(json.RawMessage(`{"items":[],"total":0,"page":1}`))
	if normal.Blocked {
		t.Error("正常空列表不该标成不可见")
	}
}

func nodeEditTestApp(t *testing.T, srv *httptest.Server) (*app, *detailState) {
	t.Helper()
	a := signedInApp(t, srv.URL)
	d := &detailState{shortID: "a1b2", topic: &topicDetail{
		ShortID: "a1b2", Title: "标题", Author: "jimmy",
		NodeID: "n1", NodeSlug: "work", NodeName: "职场",
		CanEditKnown: true, CanEdit: true,
		CanEditNodeKnown: true, CanEditNode: true,
		URL: "https://2libra.com/post/work/a1b2",
	}}
	a.detail = d
	return a, d
}

func waitNodeEdit(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待后台提交结果超时")
}

func TestEditWindowExpiryHidesEntryWithoutRetry(t *testing.T) {
	rec := &nodeRec{code: "edit_window_expired", status: http.StatusBadRequest}
	srv := newNodeServer(t, rec)
	defer srv.Close()
	a, d := nodeEditTestApp(t, srv)

	st := newNodeEditState(d)
	st.target = nodeFilterOf(nodeRef{ID: "n2", Slug: "life", Name: "生活"})
	a.nodeEdit = st
	a.submitNodeEdit()
	waitNodeEdit(t, func() bool { return a.nodeEdit == nil })

	calls, _, _, _, _, _ := rec.snapshot()
	if calls != 1 {
		t.Errorf("过期后发了 %d 次请求，期望 1 次（不重试）", calls)
	}
	if !d.topic.CanEditNodeKnown || d.topic.CanEditNode {
		t.Error("改节点机会用掉后应当隐藏入口")
	}
	if !d.topic.CanEdit {
		t.Error("两个窗口独立，改节点失败不该关掉正文编辑入口")
	}
}

// 正文窗口收到 edit_window_expired 时同样只收窗口、藏入口，不再重试。
func TestTopicEditExpiryHidesEntryWithoutRetry(t *testing.T) {
	rec := &nodeRec{code: "edit_window_expired", status: http.StatusBadRequest}
	srv := newNodeServer(t, rec)
	defer srv.Close()
	a, d := nodeEditTestApp(t, srv)

	st := newTopicEditState(d.topic, nil)
	st.detail = d
	st.title = "新标题"
	a.edit = st
	a.submitEdit()
	waitNodeEdit(t, func() bool { return a.edit == nil })

	calls, _, path, _, _, _ := rec.snapshot()
	if calls != 1 || !strings.HasSuffix(path, "/edit") {
		t.Errorf("正文编辑请求 = %d 次 / %s", calls, path)
	}
	if !d.topic.CanEditKnown || d.topic.CanEdit {
		t.Error("正文窗口过期后应当隐藏编辑入口")
	}
	if !d.topic.CanEditNode {
		t.Error("两个窗口独立，正文过期不该关掉改节点入口")
	}
}

func TestNodeEditAppliesLandedNode(t *testing.T) {
	rec := &nodeRec{}
	srv := newNodeServer(t, rec)
	defer srv.Close()
	a, d := nodeEditTestApp(t, srv)

	st := newNodeEditState(d)
	st.target = nodeFilterOf(nodeRef{ID: "n2", Slug: "life", Name: "生活", ParentName: "分类", ParentSlug: "cat"})
	a.nodeEdit = st
	a.submitNodeEdit()
	waitNodeEdit(t, func() bool { return a.nodeEdit == nil })

	if d.topic.NodeID != "n2" || d.topic.NodeSlug != "life" || d.topic.NodeName != "生活" {
		t.Fatalf("页面节点没有更新：%+v", d.topic)
	}
	if d.topic.NodeParentName != "分类" || d.topic.NodeParentSlug != "cat" {
		t.Errorf("父节点没跟上：%s / %s", d.topic.NodeParentName, d.topic.NodeParentSlug)
	}
	if d.topic.CanEditNodeKnown && d.topic.CanEditNode {
		t.Error("改节点成功后机会应已用掉")
	}
	if !d.topic.CanEdit {
		t.Error("改节点不该消耗正文编辑窗口")
	}
	if !strings.Contains(d.topic.URL, "/life/") {
		t.Errorf("原文链接没跟上新节点：%s", d.topic.URL)
	}
}

func TestNodeEditSkipsSameNode(t *testing.T) {
	rec := &nodeRec{}
	srv := newNodeServer(t, rec)
	defer srv.Close()
	a, d := nodeEditTestApp(t, srv)

	st := newNodeEditState(d)
	if !st.sameNode() {
		t.Fatal("初始选中的就是当前节点")
	}
	a.nodeEdit = st
	a.submitNodeEdit()
	calls, _, _, _, _, _ := rec.snapshot()
	if calls != 0 {
		t.Errorf("同节点不该发请求，实际 %d 次", calls)
	}
}

func TestNodeEditEntryUsesWindowState(t *testing.T) {
	a := testApp(t)
	m := newAuthManager(t.TempDir())
	if err := m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	a.auth = m

	has := func(label string, d *detailState) bool {
		for _, act := range a.detailToolActions(d) {
			if act.label == label {
				return true
			}
		}
		return false
	}
	// can_edit 关掉只影响正文编辑；附言与改节点都还在。
	d := &detailState{shortID: "a1b2", topic: &topicDetail{
		Author: "jimmy", NodeID: "n1",
		CanEditKnown: true, CanEdit: false,
		CanEditNodeKnown: true, CanEditNode: true,
	}}
	if has("编辑帖子", d) || !has("追加附言", d) || !has("修改节点", d) {
		t.Errorf("入口显隐错了：%+v", a.detailToolActions(d))
	}
	// can_edit_node 关掉时只藏改节点。
	d.topic.CanEditNode = false
	if has("修改节点", d) {
		t.Error("机会用掉后不该再显示改节点入口")
	}
	// 匿名帖真实发布者拿到 can_edit_node 时也有入口，不能按昵称拦。
	d.topic.Author, d.topic.CanEditNode = "匿名", true
	if !a.canEditNode(d) {
		t.Error("匿名帖真实发布者也应能改节点")
	}
	if a.isPostAuthor(d) || has("追加附言", d) {
		t.Error("匿名帖按昵称判断不了作者，附言入口不该出现")
	}
}

func TestNodeEditWindowRenders(t *testing.T) {
	a := testApp(t)
	d := &detailState{shortID: "a1b2", topic: &topicDetail{
		ShortID: "a1b2", NodeID: "n1", NodeSlug: "work", NodeName: "职场",
		CanEditNodeKnown: true, CanEditNode: true,
	}}
	a.nodeEdit = newNodeEditState(d)
	a.nodeCat.nodes = []composeNode{{ID: "n2", Slug: "life", Name: "生活", ParentSlug: "cat", ParentName: "分类"}}
	tt := ui.NewTester(a.nodeEditWindowView, 520, 460)
	tt.Frame()
	for _, want := range []string{"修改节点", "当前节点：职场", "目标节点", "取消", "确定"} {
		if !tt.HasText(want) {
			t.Errorf("改节点窗口缺少 %q：%q", want, tt.Texts())
		}
	}
	// 还没选新节点，「确定」不该可用。
	if !a.nodeEdit.sameNode() {
		t.Error("打开窗口时目标应当是当前节点")
	}
}
