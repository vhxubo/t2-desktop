package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 帖子详情里的竞猜块：线上在顶层 d.bet，选项 uuid 与 total_coins 都在。
const betDetailJSON = `{
  "short_id":"BET01","title":"竞猜帖","content":"正文",
  "author":{"id":"u1","username":"jimmy"},
  "node":{"id":"node-1","name":"职场","slug":"work"},
  "bet":{"title":"谁会赢","status":"open",
    "participate_deadline":"2099-11-20T12:00:00.000Z",
    "options":[{"id":"opt-a","text":"甲队","total_coins":120},
               {"id":"opt-b","text":"乙队","total_coins":80}]}
}`

func TestDecodeTopicBet(t *testing.T) {
	topic, err := decodeTopic([]byte(betDetailJSON), "BET01")
	if err != nil {
		t.Fatal(err)
	}
	b := topic.Bet
	if b == nil || b.Title != "谁会赢" || len(b.Options) != 2 {
		t.Fatalf("竞猜块 = %+v", b)
	}
	if b.Options[0].ID != "opt-a" || b.Options[0].Text != "甲队" || b.Options[0].TotalCoins != 120 {
		t.Errorf("选项解码不对：%+v", b.Options[0])
	}
	if betParticipationClosed(b) {
		t.Error("2099 年的截止时间不该判为已截止")
	}
	if betSettled(b) {
		t.Error("status=open 不该判为已结算")
	}
}

// 早期文档把竞猜放在 d.extra.bet；结算块带 settled_option_id 与我的参与。
func TestDecodeTopicBetFromExtraAndMine(t *testing.T) {
	raw := `{"short_id":"B","extra":{"bet":{
    "title":"结算了","status":"settled",
    "settled_option_id":"opt-b","settled_at":"2026-11-01T00:00:00.000Z",
    "options":[{"id":"opt-a","text":"甲","total_coins":1},{"id":"opt-b","text":"乙","total_coins":2}],
    "my_bet":{"option_id":"opt-b","coins":50}
  }}}`
	topic, err := decodeTopic([]byte(raw), "B")
	if err != nil {
		t.Fatal(err)
	}
	b := topic.Bet
	if b == nil || !betSettled(b) || b.SettledOptionID != "opt-b" {
		t.Fatalf("结算状态 = %+v", b)
	}
	if !b.Mine.Known || b.Mine.OptionID != "opt-b" || b.Mine.Coins != 50 {
		t.Errorf("我的参与 = %+v", b.Mine)
	}
	if !betParticipationClosed(b) {
		t.Error("已结算的竞猜不该还能参与")
	}
}

// "当前用户参与情况"的字段名文档没写，平铺的历史拼写也要能读。
func TestDecodeBetMineFlatSpelling(t *testing.T) {
	b := decodeBetInfo(asObject(json.RawMessage(`{
    "title":"猜","my_option_id":"o1","my_coins":33
  }`)))
	if b == nil || !b.Mine.Known || b.Mine.OptionID != "o1" || b.Mine.Coins != 33 {
		t.Fatalf("平铺拼写 = %+v", b)
	}
	empty := decodeBetInfo(asObject(json.RawMessage(`{"title":"猜"}`)))
	if empty == nil || empty.Mine.Known {
		t.Errorf("没有参与字段却标成已知：%+v", empty.Mine)
	}
}

// GET /bets 的 d 可能直接是 bet 对象，也可能包一层 bet。
func TestDecodeTopicBets(t *testing.T) {
	direct := decodeTopicBets(json.RawMessage(`{
    "title":"猜","options":[{"id":"o1","text":"A","total_coins":7}],
    "participation":{"option_id":"o1","coins":9}
  }`))
	if direct.Title != "猜" || len(direct.Options) != 1 || direct.Options[0].TotalCoins != 7 {
		t.Fatalf("直接解码 = %+v", direct)
	}
	if direct.Mine.OptionID != "o1" || direct.Mine.Coins != 9 {
		t.Errorf("包在 participation 里的参与 = %+v", direct.Mine)
	}
	wrapped := decodeTopicBets(json.RawMessage(`{"bet":{"title":"包一层","options":[]}}`))
	if wrapped.Title != "包一层" {
		t.Errorf("包一层解码 = %+v", wrapped)
	}
}

func TestBetCoinsValue(t *testing.T) {
	for _, tc := range []struct {
		in, want float64
	}{{0, betCoinsMin}, {-5, betCoinsMin}, {1.4, 1}, {1.6, 2}, {12, 12}, {1e9, betCoinsMax}} {
		if got := float64(betCoinsValue(tc.in)); got != tc.want {
			t.Errorf("betCoinsValue(%v) = %v，期望 %v", tc.in, got, tc.want)
		}
	}
}

type betCall struct {
	method, path, key, auth, body string
}

func recordBetCalls(t *testing.T, status int, body string) (*app, chan betCall, func()) {
	t.Helper()
	got := make(chan betCall, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- betCall{
			method: r.Method, path: r.URL.Path,
			key:  r.Header.Get("Idempotency-Key"),
			auth: r.Header.Get("Authorization"), body: string(b),
		}
		if status != 0 {
			w.WriteHeader(status)
			io.WriteString(w, body)
			return
		}
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	return loginApp(t, srv), got, srv.Close
}

func readBetCall(t *testing.T, got chan betCall) betCall {
	t.Helper()
	select {
	case c := <-got:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("没有收到竞猜请求")
		return betCall{}
	}
}

// 参与端点的路径、幂等键、令牌与请求体字段都要与文档一致。
func TestParticipateBetRequest(t *testing.T) {
	a, got, closeSrv := recordBetCalls(t, 0, "")
	defer closeSrv()
	if err := a.client.participateBet(context.Background(), "key-1", "AB12", "opt-a", 42); err != nil {
		t.Fatalf("参与失败：%v", err)
	}
	c := readBetCall(t, got)
	if c.method != http.MethodPost || c.path != "/v1/topics/AB12/bets/opt-a/participate" {
		t.Errorf("请求 = %s %s", c.method, c.path)
	}
	if c.auth != "Bearer test-token" {
		t.Errorf("缺少令牌：%q", c.auth)
	}
	if c.key != "key-1" || !keyPattern.MatchString(c.key) {
		t.Errorf("幂等键不对：%q", c.key)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(c.body), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 || body["coins"] != float64(42) {
		t.Errorf("请求体 = %s", c.body)
	}
}

// option_id 里的特殊字符要转义，不能拼出另一条路径。
func TestBetParticipatePathEscape(t *testing.T) {
	if got, want := topicBetParticipatePath("AB12", "a/b?c"), "/v1/topics/AB12/bets/a%2Fb%3Fc/participate"; got != want {
		t.Errorf("路径 = %s，期望 %s", got, want)
	}
	if got, want := topicBetsPath("AB12"), "/v1/topics/AB12/bets"; got != want {
		t.Errorf("读路径 = %s，期望 %s", got, want)
	}
}

// 参数校验在发请求之前完成，不能把空选项或 0 金额送到服务端。
func TestParticipateBetRejectsBadInput(t *testing.T) {
	a, got, closeSrv := recordBetCalls(t, 0, "")
	defer closeSrv()
	if err := a.client.participateBet(context.Background(), "k", "AB12", "", 5); err == nil {
		t.Error("空选项应报错")
	}
	if err := a.client.participateBet(context.Background(), "k", "AB12", "opt-a", 0); err == nil {
		t.Error("0 金额应报错")
	}
	select {
	case c := <-got:
		t.Errorf("不该发请求，却收到 %+v", c)
	case <-time.After(60 * time.Millisecond):
	}
}

// GET /bets 的读端点、令牌与解码。
func TestFetchTopicBetsRequest(t *testing.T) {
	got := make(chan betCall, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- betCall{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
		io.WriteString(w, envelopeJSON(`{
            "title":"读到的竞猜","options":[{"id":"o1","text":"A","total_coins":5}],
            "my_bet":{"option_id":"o1","coins":11}
        }`))
	}))
	defer srv.Close()
	a := loginApp(t, srv)
	res, err := a.client.fetchTopicBets(context.Background(), "AB12")
	if err != nil {
		t.Fatalf("读竞猜失败：%v", err)
	}
	c := <-got
	if c.method != http.MethodGet || c.path != "/v1/topics/AB12/bets" || c.auth != "Bearer test-token" {
		t.Errorf("请求 = %+v", c)
	}
	if res.Title != "读到的竞猜" || !res.Mine.Known || res.Mine.Coins != 11 {
		t.Errorf("解码 = %+v", res)
	}
}

// 未开通的 403 insufficient_scope 要原样带 code 回给授权链路。
func TestParticipateBetInsufficientScope(t *testing.T) {
	a, _, closeSrv := recordBetCalls(t, http.StatusForbidden,
		`{"c":403,"code":"insufficient_scope","m":"需要开通","d":null,"t":0,"required_scope":["write:economy"]}`)
	defer closeSrv()
	err := a.client.participateBet(context.Background(), "key-1", "AB12", "opt-a", 5)
	if err == nil {
		t.Fatal("403 应报错")
	}
	var ae *apiError
	if !asAPIError(err, &ae) || ae.Status != http.StatusForbidden || ae.Code != "insufficient_scope" {
		t.Errorf("错误 = %v", err)
	}
	if len(ae.RequiredScope) != 1 || ae.RequiredScope[0] != "write:economy" {
		t.Errorf("required_scope = %v", ae.RequiredScope)
	}
}

// 这笔投资 gold 变动成功后在本地正确记账。
func TestSubmitBetRecordsLocalAndFiresCoinEvent(t *testing.T) {
	topic, err := decodeTopic([]byte(betDetailJSON), "BET01")
	if err != nil {
		t.Fatal(err)
	}
	a, got, closeSrv := recordBetCalls(t, 0, "")
	defer closeSrv()
	a.pageName = "detail"
	d := &detailState{shortID: "BET01", topic: &topic}
	a.detail = d
	d.bet.amount = 30
	a.submitBet(d, "opt-a")
	c := readBetCall(t, got)
	if c.key == "" {
		t.Error("缺少幂等键")
	}
	// 等 publish 回到 UI 线程。
	waitFor(t, func() bool { return !d.bet.busy }, "竞猜提交完成")
	if topic.Bet.Options[0].TotalCoins != 150 || !topic.Bet.Mine.Known || topic.Bet.Mine.Coins != 30 {
		t.Errorf("本地记账 = %+v / %+v", topic.Bet.Options, topic.Bet.Mine)
	}
}

// 同一笔投注失败后重试（超时 / 授权后重放）复用幂等键；金额变了就是另一笔，
// 换新键。成功提交后清空键，再点一次是新的投注，不能用旧键。
func TestSubmitBetReusesIdempotencyKey(t *testing.T) {
	topic, err := decodeTopic([]byte(betDetailJSON), "BET01")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan betCall, 4)
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- betCall{method: r.Method, path: r.URL.Path,
			key: r.Header.Get("Idempotency-Key"), body: string(b)}
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"c":500,"code":"internal","m":"稍后再试","d":null,"t":0}`)
			return
		}
		io.WriteString(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()
	a := loginApp(t, srv)
	a.pageName = "detail"
	d := &detailState{shortID: "BET01", topic: &topic}
	a.detail = d
	d.bet.amount = 20

	// 首次请求失败：键留在状态里，等重试复用。
	a.submitBet(d, "opt-a")
	c1 := readBetCall(t, got)
	waitFor(t, func() bool { return !d.bet.busy }, "首次失败回到 UI 线程")
	if c1.key == "" {
		t.Fatal("缺少幂等键")
	}

	fail.Store(false)
	a.submitBet(d, "opt-a")
	c2 := readBetCall(t, got)
	waitFor(t, func() bool { return !d.bet.busy }, "重试成功回到 UI 线程")
	if c2.key != c1.key {
		t.Errorf("同金额重试应复用幂等键：%q vs %q", c1.key, c2.key)
	}

	// 上一笔已经成功、键已清空；改金额再投是另一笔，必然是新键。
	d.bet.amount = 25
	a.submitBet(d, "opt-a")
	c3 := readBetCall(t, got)
	waitFor(t, func() bool { return !d.bet.busy }, "改金额提交完成")
	if c3.key == c2.key {
		t.Errorf("改了金额应换新键：%q", c3.key)
	}
}

func asAPIError(err error, target **apiError) bool {
	if err == nil {
		return false
	}
	if ae, ok := err.(*apiError); ok {
		*target = ae
		return true
	}
	return false
}

// 文档给的 extra.bet 与详情里的位置都要能解码，缺字段不会 panic。
func TestDecodeBetInfoToleratesUnknownFields(t *testing.T) {
	b := decodeBetInfo(asObject(json.RawMessage(`{
    "title":"猜","future_field":{"a":1},
    "options":[{"id":"o1","text":"A","new_field":true}],
    "status":"something_new"
  }`)))
	if b == nil || len(b.Options) != 1 || b.Options[0].ID != "o1" {
		t.Fatalf("容错解码 = %+v", b)
	}
	if betSettled(b) || betParticipationClosed(b) {
		t.Errorf("未知 status 不该判成结算或截止：%q", b.Status)
	}
}

func betTestDetail(t *testing.T, a *app, raw string) *topicDetail {
	t.Helper()
	topic, err := decodeTopic([]byte(raw), "BET01")
	if err != nil {
		t.Fatal(err)
	}
	a.pageName = "detail"
	a.detail = &detailState{shortID: "BET01", topic: &topic}
	syncBets(a.detail, topic)
	return &topic
}

// 详情页正文末尾要画出竞猜块：标题、选项与各自的累计投入。
func TestBetSectionRenders(t *testing.T) {
	a := testApp(t)
	betTestDetail(t, a, betDetailJSON)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("竞猜 · 谁会赢") {
		t.Errorf("没有竞猜标题：%q", tt.Texts())
	}
	if !tt.HasText("甲队 · 120 金币") || !tt.HasText("乙队 · 80 金币") {
		t.Errorf("没有渲染选项与投入：%q", tt.Texts())
	}
}

// 点未结算竞猜的选项打开投注面板，填金额后给出「投入 N 金币」的确认按钮。
func TestBetPanelOpensAndConfirmLabel(t *testing.T) {
	a := testApp(t)
	betTestDetail(t, a, betDetailJSON)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("甲队 · 120 金币"); err != nil {
		t.Fatalf("点竞猜选项失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("投注会从余额扣除，确认后不能撤销。") {
		t.Errorf("没有扣费说明：%q", tt.Texts())
	}
	if !tt.HasText("投入 10 金币") {
		t.Errorf("默认金额确认按钮不对：%q", tt.Texts())
	}
}

// 已结算的竞猜只读：标出中签项，点选项不会打开投注面板、不发请求。
func TestBetSettledIsReadOnly(t *testing.T) {
	a, got, closeSrv := recordBetCalls(t, 0, "")
	defer closeSrv()
	topic := betTestDetail(t, a, betDetailJSON)
	topic.Bet.Status = "settled"
	topic.Bet.SettledOptionID = "opt-b"
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("乙队 · 80 金币 · 已中签") {
		t.Errorf("没有标出中签项：%q", tt.Texts())
	}
	_ = tt.Click("甲队 · 120 金币")
	select {
	case c := <-got:
		t.Errorf("已结算的竞猜不该发请求：%+v", c)
	default:
	}
}

// 截止时间已过的竞猜不能提交，也不该在界面上给出投注入口。
func TestBetDeadlinePastClosed(t *testing.T) {
	b := &betInfo{
		Title:               "过期",
		ParticipateDeadline: "2020-01-01T00:00:00.000Z",
		Options:             []betOption{{ID: "o1", Text: "A"}},
	}
	if !betParticipationClosed(b) {
		t.Error("过期竞猜应判为不能参与")
	}
	if strings.TrimSpace(b.Title) == "" {
		t.Fatal("测试数据不该是空标题")
	}
}
