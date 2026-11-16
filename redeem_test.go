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

// 兑换码是路径段，且是人类可读字符串：空格、斜杠、中文都必须转义成一个
// 路径段，不能拼出多级路径。
func TestRedeemPathEscapesCode(t *testing.T) {
	for got, want := range map[string]string{
		redeemPath("ABCD-1234"): "/v1/redeem-codes/ABCD-1234/redeem",
		redeemPath("a/b c"):     "/v1/redeem-codes/a%2Fb%20c/redeem",
		redeemPath("红包码"):       "/v1/redeem-codes/%E7%BA%A2%E5%8C%85%E7%A0%81/redeem",
	} {
		if got != want {
			t.Errorf("兑换码路径 = %s，期望 %s", got, want)
		}
	}
}

// api-read.md 把兑换码块写在 extra.redeem_codes：只有 masked_code 与
// is_redeemed，已领取的才给完整 code。说明与每人可领个数可能跟列表放在一起，
// 也可能落在顶层。
func TestDecodeTopicRedeem(t *testing.T) {
	raw := `{"short_id":"R1","title":"发码","extra":{"code_description":"先到先得",
        "max_redeem_per_user":2,"redeem_codes":[
        {"masked_code":"ABCD-****","is_redeemed":false},
        {"masked_code":"WXYZ-****","is_redeemed":true,"code":"WXYZ-1234"}
    ]}}`
	topic, err := decodeTopic([]byte(raw), "R1")
	if err != nil {
		t.Fatal(err)
	}
	b := topic.Redeem
	if b == nil || len(b.Codes) != 2 {
		t.Fatalf("兑换码块 = %+v", b)
	}
	if b.Description != "先到先得" || !b.MaxPerUserKnown || b.MaxPerUser != 2 {
		t.Errorf("说明 / 每人可领 = %+v", b)
	}
	if b.Codes[0].Masked != "ABCD-****" || b.Codes[0].Code != "" || b.Codes[0].Redeemed {
		t.Errorf("未领取的码 = %+v", b.Codes[0])
	}
	// 完整码出现即视为已领取，即使服务端没带 is_redeemed。
	if b.Codes[1].Code != "WXYZ-1234" || !b.Codes[1].Redeemed {
		t.Errorf("已领取的码 = %+v", b.Codes[1])
	}

	// 包一层 { codes, description } 的写法同样认。
	wrapped, err := decodeTopic([]byte(`{"short_id":"R2","extra":{"redeem_codes":{
        "codes":[{"masked_code":"M-1"}],"description":"限时"}}} `), "R2")
	if err != nil {
		t.Fatal(err)
	}
	if wrapped.Redeem == nil || wrapped.Redeem.Description != "限时" ||
		len(wrapped.Redeem.Codes) != 1 || wrapped.Redeem.Codes[0].Masked != "M-1" {
		t.Errorf("包装写法 = %+v", wrapped.Redeem)
	}

	// 空数组 / 空对象什么都不表示：界面不该因此多出一张空卡片。
	for _, d := range []string{`{"short_id":"R3","extra":{"redeem_codes":[]}}`,
		`{"short_id":"R3","extra":{"redeem_codes":{}}}`} {
		empty, err := decodeTopic([]byte(d), "R3")
		if err != nil {
			t.Fatal(err)
		}
		if empty.Redeem != nil {
			t.Errorf("空兑换码块被当成有内容：%+v", empty.Redeem)
		}
	}
}

// 成功响应的字段表文档没给：message / 完整码 / 明确标注为奖励的金额才认，
// 名字叫 coins 的字段可能是余额，不能当成这次获得的金币。
func TestDecodeRedeemResult(t *testing.T) {
	r := decodeRedeemResult(json.RawMessage(`{"message":"领取成功","code":"ABCD-1234","reward_coins":100}`))
	if r.Message != "领取成功" || r.Code != "ABCD-1234" || !r.CoinsKnown || r.Coins != 100 {
		t.Errorf("响应解码 = %+v", r)
	}
	if got := redeemSuccessText(r); got != "领取成功" {
		t.Errorf("成功提示 = %q", got)
	}
	// 只有 coins（可能是余额）时不声称「获得」。
	balance := decodeRedeemResult(json.RawMessage(`{"coins":999}`))
	if balance.CoinsKnown {
		t.Errorf("把可能的余额当成奖励：%+v", balance)
	}
	if got := redeemSuccessText(balance); got != "兑换成功" {
		t.Errorf("无说明无奖励的提示 = %q", got)
	}
	// d 为 null / 裸字符串都不能 panic。
	if empty := decodeRedeemResult(json.RawMessage(`null`)); empty != (redeemResult{}) {
		t.Errorf("空响应 = %+v", empty)
	}
}

type redeemCall struct {
	method, path, auth, key, body string
}

// recordRedeemCalls 造一个已登录的 app，把领取请求落在测试服务器上。
func recordRedeemCalls(t *testing.T, status int, body string) (*app, chan redeemCall, func()) {
	t.Helper()
	got := make(chan redeemCall, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- redeemCall{
			method: r.Method, path: r.URL.EscapedPath(),
			auth: r.Header.Get("Authorization"), key: r.Header.Get("Idempotency-Key"),
			body: string(b),
		}
		if status != 0 {
			w.WriteHeader(status)
			io.WriteString(w, body)
			return
		}
		io.WriteString(w, envelopeJSON(`{"message":"领取成功","reward_coins":100}`))
	}))
	return loginApp(t, srv), got, srv.Close
}

func readRedeemCall(t *testing.T, got chan redeemCall) redeemCall {
	t.Helper()
	select {
	case c := <-got:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("没有收到领取请求")
		return redeemCall{}
	}
}

// 领取是标 🔑 的写端点：POST、路径转义、带令牌与幂等键、没有请求体。
func TestRedeemCodeRequest(t *testing.T) {
	a, got, closeSrv := recordRedeemCalls(t, 0, "")
	defer closeSrv()
	res, err := a.client.redeemCode(context.Background(), "key-1", "A/B 12")
	if err != nil {
		t.Fatalf("领取失败：%v", err)
	}
	if !res.CoinsKnown || res.Coins != 100 {
		t.Errorf("奖励金额没有解出来：%+v", res)
	}
	c := readRedeemCall(t, got)
	if c.method != http.MethodPost || c.path != "/v1/redeem-codes/A%2FB%2012/redeem" {
		t.Errorf("请求 = %s %s", c.method, c.path)
	}
	if c.auth != "Bearer test-token" || c.key != "key-1" {
		t.Errorf("鉴权 / 幂等键 = %q / %q", c.auth, c.key)
	}
	if c.body != "" {
		t.Errorf("文档写明无请求体，实际 = %q", c.body)
	}
}

// 已领完 / 已过期是 400 invalid_request 或 409 conflict，原因在 m 里；
// 未开通是 403 insufficient_scope，要原样带 required_scope 回授权链路。
func TestRedeemCodeErrorBranches(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		code   string
		text   string
	}{
		{"已领完", http.StatusBadRequest,
			`{"c":400,"code":"invalid_request","m":"兑换码已被领完","d":null,"t":0}`,
			"invalid_request", "兑换码已被领完"},
		{"已过期", http.StatusConflict,
			`{"c":409,"code":"conflict","m":"兑换码已过期","d":null,"t":0}`,
			"conflict", "兑换码已过期"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _, closeSrv := recordRedeemCalls(t, tc.status, tc.body)
			defer closeSrv()
			_, err := a.client.redeemCode(context.Background(), "k", "CODE")
			var ae *apiError
			if !asAPIError(err, &ae) || ae.Code != tc.code {
				t.Fatalf("错误 = %v", err)
			}
			if got := redeemErrorText(err); got != tc.text {
				t.Errorf("提示 = %q，期望服务端原文 %q", got, tc.text)
			}
		})
	}

	a, _, closeSrv := recordRedeemCalls(t, http.StatusForbidden,
		`{"c":403,"code":"insufficient_scope","m":"需要开通","d":null,"t":0,"required_scope":["write:economy"]}`)
	defer closeSrv()
	_, err := a.client.redeemCode(context.Background(), "k", "CODE")
	var ae *apiError
	if !asAPIError(err, &ae) || ae.Code != "insufficient_scope" || ae.Status != http.StatusForbidden {
		t.Fatalf("错误 = %v", err)
	}
	if len(ae.RequiredScope) != 1 || ae.RequiredScope[0] != "write:economy" {
		t.Errorf("required_scope = %v", ae.RequiredScope)
	}
}

const redeemDetailJSON = `{
    "short_id":"RED01","title":"发码帖","content":"正文",
    "extra":{"redeem_codes":{"codes":[
        {"masked_code":"ABCD-****","is_redeemed":false},
        {"masked_code":"WXYZ-****","is_redeemed":true,"code":"WXYZ-1234"}
    ],"description":"先到先得","max_redeem_per_user":1}}
}`

func redeemTestDetail(t *testing.T, a *app) *detailState {
	t.Helper()
	topic, err := decodeTopic([]byte(redeemDetailJSON), "RED01")
	if err != nil {
		t.Fatal(err)
	}
	if topic.Redeem == nil {
		t.Fatal("测试详情里没有兑换码块")
	}
	a.pageName = "detail"
	d := &detailState{shortID: "RED01", topic: &topic}
	a.detail = d
	return d
}

// 空码、超长码在发请求前就拦下；结果用 toast 报告，不发请求。
func TestSubmitRedeemCodeRejectsBadInput(t *testing.T) {
	a, got, closeSrv := recordRedeemCalls(t, 0, "")
	defer closeSrv()
	d := redeemTestDetail(t, a)

	a.submitRedeemCode(d)
	if len(a.toasts) != 1 || a.toasts[0].text != "请先输入兑换码" {
		t.Errorf("空码提示 = %+v", a.toasts)
	}
	a.toasts = nil

	d.redeem.code = strings.Repeat("x", redeemCodeMax+1)
	a.submitRedeemCode(d)
	if len(a.toasts) != 1 || !strings.Contains(a.toasts[0].text, "最多") {
		t.Errorf("超长码提示 = %+v", a.toasts)
	}
	select {
	case c := <-got:
		t.Errorf("不该发请求，却收到 %+v", c)
	case <-time.After(60 * time.Millisecond):
	}
}

// 同一串码失败后重试复用幂等键；成功后清空键、清空输入，并把完整码记在
// 本地（详情只给脱敏值），同时发出金币变动事件。
func TestSubmitRedeemCodeReusesKeyAndRecordsClaim(t *testing.T) {
	got := make(chan redeemCall, 4)
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- redeemCall{method: r.Method, path: r.URL.EscapedPath(),
			key: r.Header.Get("Idempotency-Key"), body: string(b)}
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"c":500,"code":"internal_error","m":"稍后再试","d":null,"t":0}`)
			return
		}
		// 响应不带完整码：客户端应把用户输入的那一串当成本次领到的码。
		io.WriteString(w, envelopeJSON(`{"reward_coins":30}`))
	}))
	defer srv.Close()
	a := loginApp(t, srv)
	events := make(chan coinEvent, 2)
	a.onCoinsChanged = func(e coinEvent) { events <- e }
	d := redeemTestDetail(t, a)
	d.redeem.code = "CODE-1"

	a.submitRedeemCode(d)
	c1 := readRedeemCall(t, got)
	waitFor(t, func() bool { return !d.redeem.busy }, "首次领取失败回到 UI 线程")
	if c1.key == "" || d.redeem.key != c1.key {
		t.Fatalf("首次幂等键 = %q / 状态 %q", c1.key, d.redeem.key)
	}
	if d.redeem.code != "CODE-1" {
		t.Errorf("失败后不该清掉输入：%q", d.redeem.code)
	}

	fail.Store(false)
	a.submitRedeemCode(d)
	c2 := readRedeemCall(t, got)
	waitFor(t, func() bool { return !d.redeem.busy }, "重试成功回到 UI 线程")
	if c2.key != c1.key {
		t.Errorf("同一串码重试应复用幂等键：%q vs %q", c1.key, c2.key)
	}
	if d.redeem.key != "" || d.redeem.sig != "" {
		t.Errorf("成功后幂等键应清空：%+v", d.redeem)
	}
	if d.redeem.code != "" {
		t.Errorf("成功后输入框应清空：%q", d.redeem.code)
	}
	if len(d.redeem.claimed) != 1 || d.redeem.claimed[0] != "CODE-1" {
		t.Errorf("已领取记录 = %v", d.redeem.claimed)
	}
	select {
	case e := <-events:
		if e != coinEventRedeem {
			t.Errorf("金币事件 = %q，期望 %q", e, coinEventRedeem)
		}
	case <-time.After(time.Second):
		t.Error("没有发出金币事件")
	}

	// 换一串码是另一笔：必然是新键。
	fail.Store(false)
	d.redeem.code = "CODE-2"
	a.submitRedeemCode(d)
	c3 := readRedeemCall(t, got)
	waitFor(t, func() bool { return !d.redeem.busy }, "第二笔领取完成")
	if c3.key == c1.key {
		t.Errorf("换了兑换码应换新幂等键：%q", c3.key)
	}
	if len(d.redeem.claimed) != 2 {
		t.Errorf("两笔领取都该记下来：%v", d.redeem.claimed)
	}
}

// 详情页正文末尾要画出兑换码块：脱敏值、已领取状态、输入框与领取按钮。
// 领取成功后结果走 toast，本地补一行完整码，不必重发详情请求。
func TestRedeemSectionRendersAndSubmits(t *testing.T) {
	a, got, closeSrv := recordRedeemCalls(t, 0, "")
	defer closeSrv()
	d := redeemTestDetail(t, a)
	d.redeem.code = "CODE-1"

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"兑换码", "先到先得", "每人可领 1 个", "ABCD-****", "WXYZ-1234", "领取"} {
		if !tt.HasText(want) {
			t.Errorf("没有 %q：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("领取"); err != nil {
		t.Fatalf("点领取失败：%v（%q）", err, tt.Texts())
	}
	c := readRedeemCall(t, got)
	if c.method != http.MethodPost || c.path != "/v1/redeem-codes/CODE-1/redeem" || c.key == "" {
		t.Errorf("领取请求 = %+v", c)
	}
	waitFor(t, func() bool { return !d.redeem.busy }, "领取完成")
	// toast 只在构建期发出：先让窗口领走队列里的提示。
	tt.Frame()
	if !tt.HasText("领取成功") {
		t.Errorf("没有用 toast 报告结果：%q", tt.Texts())
	}
	if !tt.HasText("已领取 · CODE-1") {
		t.Errorf("领取成功后没有补上完整码：%q", tt.Texts())
	}
}

// 未登录时只给登录入口，不发领取请求。
func TestRedeemSectionAsksForLogin(t *testing.T) {
	a := testApp(t)
	d := redeemTestDetail(t, a)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("登录后领取") {
		t.Errorf("未登录时没有登录入口：%q", tt.Texts())
	}
	if !tt.HasText("登录后可以领取兑换码") {
		t.Errorf("未登录时没有说明：%q", tt.Texts())
	}
	if err := tt.Click("登录后领取"); err != nil {
		t.Fatalf("点登录入口失败：%v", err)
	}
	if !a.authGate.open {
		t.Error("没有摆出授权弹窗")
	}
	_ = d
}
