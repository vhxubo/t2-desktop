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

// 兑换码 / id 是路径段，且是人类可读字符串：空格、斜杠、中文都必须转义成
// 一个路径段，不能拼出多级路径。
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

// 线上详情的真实字段（2026-11-17 changelog 更正过文档）：extra.redeem_codes[]
// 每项带 id、code（中间用 * 脱敏）、is_redeemed、redeemed_by（用户对象）、
// redeemed_at。2026-11-17 的文档修正明确说没有 masked_code。
func TestDecodeTopicRedeem(t *testing.T) {
	raw := `{"short_id":"R1","title":"发码","extra":{"code_description":"先到先得",
        "max_redeem_per_user":2,"redeem_codes":[
        {"id":"id-1","code":"ABCD-****XPFJ","is_redeemed":false,"redeemed_by":null,"redeemed_at":null},
        {"id":"id-2","code":"WXYZ-****RJKL","is_redeemed":true,
         "redeemed_by":{"id":"u-1","username":"jimmy"},"redeemed_at":"2026-11-20T01:57:21.576Z"}
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
	first := b.Codes[0]
	if first.ID != "id-1" || first.Masked != "ABCD-****XPFJ" || first.Code != "" || first.Redeemed {
		t.Errorf("未领取的码 = %+v", first)
	}
	// 已领取的码仍只有脱敏值；领取者信息来自 redeemed_by 对象。
	second := b.Codes[1]
	if second.ID != "id-2" || second.Masked != "WXYZ-****RJKL" || second.Code != "" || !second.Redeemed {
		t.Errorf("已领取的码 = %+v", second)
	}
	if second.RedeemedByID != "u-1" || second.RedeemedByName != "jimmy" || second.RedeemedAt == "" {
		t.Errorf("领取者 / 时间 = %+v", second)
	}

	// 包一层 { codes, description } 的写法同样认。
	wrapped, err := decodeTopic([]byte(`{"short_id":"R2","extra":{"redeem_codes":{
        "codes":[{"id":"m-1","code":"M-1"}],"description":"限时"}}} `), "R2")
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

// 成功响应的字段表文档没给：message / 完整码 / 明确标注为红包或奖励的金额
// 才认，名字叫 coins 的字段可能是余额，不能当成这次获得的金币。
func TestDecodeRedeemResult(t *testing.T) {
	r := decodeRedeemResult(json.RawMessage(`{"message":"领取成功","code":"ABCD-1234","red_packet_amount":100}`))
	if r.Message != "领取成功" || r.Code != "ABCD-1234" || !r.RedPacketKnown || r.RedPacket != 100 {
		t.Errorf("响应解码 = %+v", r)
	}
	if got := redeemSuccessText(r); got != "领取成功" {
		t.Errorf("成功提示 = %q", got)
	}
	// 驼峰写法的红包金额也认。
	if c := decodeRedeemResult(json.RawMessage(`{"redPacketAmount":88}`)); !c.RedPacketKnown || c.RedPacket != 88 {
		t.Errorf("驼峰红包金额 = %+v", c)
	}
	// 只有 coins（可能是余额）时不声称「获得」。
	balance := decodeRedeemResult(json.RawMessage(`{"coins":999}`))
	if balance.CoinsKnown || balance.RedPacketKnown {
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
		io.WriteString(w, envelopeJSON(`{"message":"领取成功","code":"ABCD-1234-XPFJ","red_packet_amount":100}`))
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
	if res.Code != "ABCD-1234-XPFJ" || !res.RedPacketKnown || res.RedPacket != 100 {
		t.Errorf("响应解码 = %+v", res)
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
    "extra":{"redeem_codes":[
        {"id":"id-1","code":"ABCD-****XPFJ","is_redeemed":false,"redeemed_by":null,"redeemed_at":null},
        {"id":"id-2","code":"WXYZ-****RJKL","is_redeemed":true,
         "redeemed_by":{"id":"u-1","username":"jimmy"},"redeemed_at":"2026-11-20T01:57:21.576Z"},
        {"id":"id-3","code":"MINE-****CODE","is_redeemed":true,
         "redeemed_by":{"id":"u-reader","username":"reader"},"redeemed_at":"2026-11-20T01:58:00.000Z"},
        {"id":"id-4","code":"OTHER-****CODE","is_redeemed":false,"redeemed_by":null,"redeemed_at":null}
    ],"code_description":"先到先得","max_redeem_per_user":2}}
`

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

// 同一条码失败后重试复用幂等键（按 id，不是脱敏码）；成功后清空键、把完整码
// 记在码条上，同时发出金币变动事件。
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
		// 响应带完整码：客户端要把它留在码条上，而不是显示脱敏值。
		io.WriteString(w, envelopeJSON(`{"code":"ABCD-1234-XPFJ","red_packet_amount":30}`))
	}))
	defer srv.Close()
	a := loginApp(t, srv)
	events := make(chan coinEvent, 2)
	a.onCoinsChanged = func(e coinEvent) { events <- e }
	d := redeemTestDetail(t, a)

	a.submitRedeemCode(d, "id-1")
	c1 := readRedeemCall(t, got)
	waitFor(t, func() bool { return !d.redeem.busy }, "首次领取失败回到 UI 线程")
	if c1.path != "/v1/redeem-codes/id-1/redeem" {
		t.Errorf("领取按 id 走，实际路径 %q", c1.path)
	}
	if c1.key == "" || d.redeem.key != c1.key {
		t.Fatalf("首次幂等键 = %q / 状态 %q", c1.key, d.redeem.key)
	}
	if d.topic.Redeem.Codes[0].Redeemed {
		t.Errorf("失败后不该把码标成已领取")
	}

	fail.Store(false)
	a.submitRedeemCode(d, "id-1")
	c2 := readRedeemCall(t, got)
	waitFor(t, func() bool { return !d.redeem.busy }, "重试成功回到 UI 线程")
	if c2.key != c1.key {
		t.Errorf("同一条码重试应复用幂等键：%q vs %q", c1.key, c2.key)
	}
	if d.redeem.key != "" || d.redeem.sig != "" {
		t.Errorf("成功后幂等键应清空：%+v", d.redeem)
	}
	if d.redeem.full["id-1"] != "ABCD-1234-XPFJ" {
		t.Errorf("完整码没记下来：%v", d.redeem.full)
	}
	e := d.topic.Redeem.Codes[0]
	if !e.Redeemed || e.RedeemedByName != "reader" || e.Code != "ABCD-1234-XPFJ" {
		t.Errorf("码条没标成我领的：%+v", e)
	}
	select {
	case ev := <-events:
		if ev != coinEventRedeem {
			t.Errorf("金币事件 = %q，期望 %q", ev, coinEventRedeem)
		}
	case <-time.After(time.Second):
		t.Error("没有发出金币事件")
	}

	// 换一条码是另一笔：必然是新键。
	a.submitRedeemCode(d, "id-4")
	c3 := readRedeemCall(t, got)
	waitFor(t, func() bool { return !d.redeem.busy }, "第二笔领取完成")
	if c3.key == c1.key {
		t.Errorf("换了兑换码应换新幂等键：%q", c3.key)
	}
}

// 详情里别人领过的码、自己领过的码都不能再发请求；达到每人上限时同样拦下。
func TestOpenDialogBlocksUnclaimableCodes(t *testing.T) {
	a, got, closeSrv := recordRedeemCalls(t, 0, "")
	defer closeSrv()
	d := redeemTestDetail(t, a)

	a.redeemOpenDialog(d, "id-2") // 别人领的
	a.redeemOpenDialog(d, "id-3") // 自己领的
	a.redeemOpenDialog(d, "id-1")
	if d.redeem.dialogID != "id-1" {
		t.Fatalf("只有未领取的码能打开确认框：%q", d.redeem.dialogID)
	}
	select {
	case c := <-got:
		t.Errorf("打开确认框不该发请求：%+v", c)
	case <-time.After(60 * time.Millisecond):
	}
	// 此时我已领 1 个、上限 2，还能再领一个。把上限降到 1 就该拦住。
	d.topic.Redeem.MaxPerUser = 1
	a.toasts = nil
	a.redeemOpenDialog(d, "id-1")
	if len(a.toasts) == 0 || !strings.Contains(a.toasts[0].text, "最多可领取 1 个") {
		t.Errorf("达到上限没有提示：%+v", a.toasts)
	}
	if d.redeem.dialogID != "id-1" {
		t.Errorf("达到上限不该打开确认框：%q", d.redeem.dialogID)
	}
}

// 详情页正文末尾要画出兑换码块：脱敏码、已领取状态、未领取的可点芯片；
// 点芯片开确认框，确认后按 id 领取，成功后码条显示完整码与复制按钮。
func TestRedeemSectionRendersAndClaims(t *testing.T) {
	a, got, closeSrv := recordRedeemCalls(t, 0, "")
	defer closeSrv()
	d := redeemTestDetail(t, a)

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"兑换码", "先到先得", "每人可领 2 个",
		"ABCD-****XPFJ", "WXYZ-****RJKL", "已被 jimmy 领取", "领取兑换码 ABCD-****XPFJ"} {
		if !tt.HasText(want) {
			t.Errorf("没有 %q：%q", want, tt.Texts())
		}
	}
	// 已领取的码不该出现领取入口。
	if tt.HasText("领取兑换码 WXYZ-****RJKL") || tt.HasText("领取兑换码 MINE-****CODE") {
		t.Errorf("已领取的码还能领取：%q", tt.Texts())
	}
	if err := tt.Click("领取兑换码 ABCD-****XPFJ"); err != nil {
		t.Fatalf("点兑换码失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("需要领取这个兑换码吗？") {
		t.Errorf("没有领取确认框：%q", tt.Texts())
	}
	if err := tt.Click("领取"); err != nil {
		t.Fatalf("点确认领取失败：%v（%q）", err, tt.Texts())
	}
	c := readRedeemCall(t, got)
	if c.method != http.MethodPost || c.path != "/v1/redeem-codes/id-1/redeem" || c.key == "" {
		t.Errorf("领取请求 = %+v", c)
	}
	waitFor(t, func() bool { return !d.redeem.busy }, "领取完成")
	tt.Frame()
	// toast 只在构建期发出：先让窗口领走队列里的提示。
	if !tt.HasText("领取成功") {
		t.Errorf("没有用 toast 报告结果：%q", tt.Texts())
	}
	// 成功页显示完整码与「复制」按钮，提示文案没写进图里。
	if !tt.HasText("该兑换码领取成功。") || !tt.HasText("ABCD-1234-XPFJ") || !tt.HasText("复制") {
		t.Errorf("成功页没显示完整码 / 复制：%q", tt.Texts())
	}
	if err := tt.Click("复制"); err != nil {
		t.Fatalf("点复制失败：%v（%q）", err, tt.Texts())
	}
	if got := tt.Clipboard(); got != "ABCD-1234-XPFJ" {
		t.Errorf("剪贴板里应是完整码，实际 %q", got)
	}
}

// 未登录时点未领取的码要走登录，不发领取请求。
func TestRedeemSectionAsksForLogin(t *testing.T) {
	a := testApp(t)
	d := redeemTestDetail(t, a)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("登录后领取") {
		t.Errorf("未登录时没有登录入口：%q", tt.Texts())
	}
	if err := tt.Click("领取兑换码 ABCD-****XPFJ"); err != nil {
		t.Fatalf("点兑换码失败：%v（%q）", err, tt.Texts())
	}
	if !a.authGate.open {
		t.Error("没有摆出授权弹窗")
	}
	_ = d
}
