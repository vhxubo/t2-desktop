package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// voteDetailJSON 是一份带单选投票的详情：投票与选项都带 uuid，服务端还给了
// type / max_choices / voting_finished，界面据此决定走哪个端点。
const voteDetailJSON = `{
  "short_id":"VOTE01","title":"投票帖","content":"正文",
  "author":{"id":"u1","username":"jimmy"},
  "node":{"id":"node-1","name":"职场","slug":"work"},
  "vote":{"id":"vote-1","type":"pk","title":"选哪个","max_choices":1,
    "voting_finished":false,
    "options":[{"id":"opt-1","text":"甲","vote_count":2},
               {"id":"opt-2","text":"乙","vote_count":5}]}
}`

const voteMultiDetailJSON = `{
  "short_id":"VOTE01","title":"投票帖","content":"正文",
  "author":{"id":"u1","username":"jimmy"},
  "node":{"id":"node-1","name":"职场","slug":"work"},
  "vote":{"id":"vote-1","type":"multiple","title":"选哪几个","max_choices":2,
    "voting_finished":false,
    "options":[{"id":"opt-1","text":"甲","vote_count":2},
               {"id":"opt-2","text":"乙","vote_count":5},
               {"id":"opt-3","text":"丙","vote_count":1}]}
}`

func voteTestDetail(t *testing.T, a *app, raw string) *topicDetail {
	t.Helper()
	topic, err := decodeTopic([]byte(raw), "VOTE01")
	if err != nil {
		t.Fatal(err)
	}
	a.pageName = "detail"
	a.detail = &detailState{shortID: "VOTE01", topic: &topic}
	syncVote(a.detail, topic)
	return &topic
}

// 投票块的类型与选项 uuid 都要能解码，否则提交时拿不到 voteId / optionId。
func TestDecodeVoteTypeAndOptions(t *testing.T) {
	topic, err := decodeTopic([]byte(voteMultiDetailJSON), "VOTE01")
	if err != nil {
		t.Fatal(err)
	}
	if topic.Vote == nil || topic.Vote.ID != "vote-1" || topic.Vote.Type != "multiple" {
		t.Fatalf("投票块 = %+v", topic.Vote)
	}
	if topic.Vote.MaxChoice != 2 || topic.Vote.Finished || !voteIsMulti(topic.Vote) {
		t.Errorf("多选判定不对：%+v", topic.Vote)
	}
	if len(topic.Vote.Options) != 3 || topic.Vote.Options[2].ID != "opt-3" {
		t.Errorf("选项 uuid 没解出来：%+v", topic.Vote.Options)
	}
	single, err := decodeTopic([]byte(voteDetailJSON), "VOTE01")
	if err != nil {
		t.Fatal(err)
	}
	if voteIsMulti(single.Vote) {
		t.Error("pk 投票不该判成多选")
	}
}

type voteCall struct {
	method, path, key, auth, body string
}

func recordVoteCalls(t *testing.T, status int) (*app, chan voteCall, func()) {
	t.Helper()
	got := make(chan voteCall, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- voteCall{
			method: r.Method, path: r.URL.Path,
			key:  r.Header.Get("Idempotency-Key"),
			auth: r.Header.Get("Authorization"), body: string(b),
		}
		if status != 0 {
			w.WriteHeader(status)
			if status == http.StatusConflict {
				io.WriteString(w, `{"c":409,"code":"conflict","m":"已投过票","d":null,"t":0}`)
				return
			}
		}
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	return loginApp(t, srv), got, srv.Close
}

func readVoteCall(t *testing.T, got chan voteCall) voteCall {
	t.Helper()
	select {
	case c := <-got:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("没有收到投票请求")
		return voteCall{}
	}
}

// 单选：路径、camelCase 字段名、不带幂等键、带令牌。
func TestSendTopicVoteSingleRequest(t *testing.T) {
	a, got, closeSrv := recordVoteCalls(t, 0)
	defer closeSrv()
	if err := a.client.sendTopicVote(context.Background(), "AB12", "vote-1", []string{"opt-1"}, false); err != nil {
		t.Fatalf("投票失败：%v", err)
	}
	c := readVoteCall(t, got)
	if c.method != http.MethodPost || c.path != "/v1/topics/AB12/vote" {
		t.Errorf("请求 = %s %s", c.method, c.path)
	}
	if c.auth != "Bearer test-token" {
		t.Errorf("缺少令牌：%q", c.auth)
	}
	if c.key != "" {
		t.Errorf("投票端点不该带 Idempotency-Key：%q", c.key)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(c.body), &body); err != nil {
		t.Fatalf("请求体不是 JSON：%v（%q）", err, c.body)
	}
	if body["voteId"] != "vote-1" || body["optionId"] != "opt-1" || len(body) != 2 {
		t.Errorf("单选请求体 = %v", body)
	}
}

// 多选：走 /vote/multi，字段名是 optionIds（不是 optionId）。
func TestSendTopicVoteMultiRequest(t *testing.T) {
	a, got, closeSrv := recordVoteCalls(t, 0)
	defer closeSrv()
	if err := a.client.sendTopicVote(context.Background(), "AB12", "vote-1",
		[]string{"opt-1", "opt-3"}, true); err != nil {
		t.Fatalf("多选投票失败：%v", err)
	}
	c := readVoteCall(t, got)
	if c.path != "/v1/topics/AB12/vote/multi" {
		t.Errorf("多选路径 = %s", c.path)
	}
	if c.key != "" {
		t.Errorf("多选端点不该带 Idempotency-Key：%q", c.key)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(c.body), &body); err != nil {
		t.Fatalf("请求体不是 JSON：%v（%q）", err, c.body)
	}
	ids, _ := body["optionIds"].([]any)
	if body["voteId"] != "vote-1" || len(ids) != 2 || ids[0] != "opt-1" || ids[1] != "opt-3" {
		t.Errorf("多选请求体 = %v", body)
	}
	if _, ok := body["optionId"]; ok {
		t.Errorf("多选端点不该带单数的 optionId：%v", body)
	}
}

// 空选项与超过文档上限都在本地拦住，不发注定失败的请求。
func TestSendTopicVoteRejectsBadOptions(t *testing.T) {
	a, got, closeSrv := recordVoteCalls(t, 0)
	defer closeSrv()
	if err := a.client.sendTopicVote(context.Background(), "AB12", "vote-1", nil, false); err == nil {
		t.Error("空选项应报错")
	}
	many := make([]string, voteOptionMax+1)
	if err := a.client.sendTopicVote(context.Background(), "AB12", "vote-1", many, true); err == nil {
		t.Error("超过 20 个选项应报错")
	}
	select {
	case c := <-got:
		t.Errorf("不该发出请求：%+v", c)
	default:
	}
}

// 界面：点单选项就提交，成功后标记为已投并给 toast，不会重复发请求。
func TestVoteSectionSubmitsSingle(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()
	a := loginApp(t, srv)
	voteTestDetail(t, a, voteDetailJSON)
	done := make(chan error, 1)
	a.onInteractDone = func(err error) { done <- err }

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("甲 · 2 票"); err != nil {
		t.Fatalf("点投票选项失败：%v（%q）", err, tt.Texts())
	}
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("投票失败：%v", err)
	}
	if !a.detail.vote.voted || a.detail.vote.busy {
		t.Errorf("投票状态不对：%+v", a.detail.vote)
	}
	tt.Frame()
	if !tt.HasText("已投票") {
		t.Errorf("没有成功提示：%q", tt.Texts())
	}
	mu.Lock()
	got := append([]string(nil), paths...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "POST /v1/topics/VOTE01/vote" {
		t.Errorf("请求 = %v", got)
	}
}

// 多选：勾两个选项后走 /vote/multi，请求体里是这两个 optionId。
func TestVoteSectionSubmitsMulti(t *testing.T) {
	var mu sync.Mutex
	var body map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Unlock()
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()
	a := loginApp(t, srv)
	v := voteTestDetail(t, a, voteMultiDetailJSON)
	done := make(chan error, 1)
	a.onInteractDone = func(err error) { done <- err }

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	a.detail.vote.checks = []bool{true, false, true}
	a.submitVoteMulti(a.detail, v.Vote)
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("多选投票失败：%v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if path != "/v1/topics/VOTE01/vote/multi" {
		t.Errorf("多选路径 = %s", path)
	}
	ids, _ := body["optionIds"].([]any)
	if len(ids) != 2 || ids[0] != "opt-1" || ids[1] != "opt-3" {
		t.Errorf("多选请求体 = %v", body)
	}
}

// 409（在别处已投过）按已投处理，不当成普通错误吓用户。
func TestVoteConflictMarksVoted(t *testing.T) {
	a, got, closeSrv := recordVoteCalls(t, http.StatusConflict)
	defer closeSrv()
	voteTestDetail(t, a, voteDetailJSON)
	done := make(chan error, 1)
	a.onInteractDone = func(err error) { done <- err }

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("甲 · 2 票"); err != nil {
		t.Fatalf("点投票选项失败：%v（%q）", err, tt.Texts())
	}
	readVoteCall(t, got)
	if err := waitInteract(t, done); err != nil {
		t.Fatalf("409 不该当成失败：%v", err)
	}
	if !a.detail.vote.voted {
		t.Error("409 后应标记为已投")
	}
	tt.Frame()
	if !tt.HasText("你已经投过票了") {
		t.Errorf("没有已投提示：%q", tt.Texts())
	}
}

// 投票已结束时整块只读：点选项不会发请求。
func TestVoteFinishedIsReadOnly(t *testing.T) {
	a, got, closeSrv := recordVoteCalls(t, 0)
	defer closeSrv()
	topic := voteTestDetail(t, a, voteDetailJSON)
	topic.Vote.Finished = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("投票已结束") {
		t.Errorf("没有结束说明：%q", tt.Texts())
	}
	_ = tt.Click("甲 · 2 票")
	select {
	case c := <-got:
		t.Errorf("已结束的投票不该发请求：%+v", c)
	default:
	}
}

// 未登录点投票先走全局授权弹窗，不发请求；登录成功后会重放这一票。
func TestVoteNeedsLogin(t *testing.T) {
	got := make(chan voteCall, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- voteCall{method: r.Method, path: r.URL.Path}
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()
	a := testApp(t)
	a.client.Base = srv.URL
	voteTestDetail(t, a, voteDetailJSON)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("甲 · 2 票"); err != nil {
		t.Fatalf("点投票选项失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("登录后才能参与投票。") {
		t.Errorf("没有登录提示：%q", tt.Texts())
	}
	select {
	case c := <-got:
		t.Errorf("未登录不该发请求：%+v", c)
	default:
	}
}
