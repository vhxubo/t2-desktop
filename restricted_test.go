package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 文档声明的路径必须逐字一致，short_id 里的特殊字符要先转义。
func TestRestrictedPaths(t *testing.T) {
	for got, want := range map[string]string{
		topicRestrictedPath("AB12"):        "/v1/topics/AB12/restricted",
		topicRestrictedAuctionPath("AB12"): "/v1/topics/AB12/restricted/auction",
		topicRestrictedBidsPath("AB12"):    "/v1/topics/AB12/restricted/auction/bids",
		topicRestrictedUnlockPath("AB12"):  "/v1/topics/AB12/restricted/unlock",
		topicRestrictedPath("a/b?c"):       "/v1/topics/a%2Fb%3Fc/restricted",
	} {
		if got != want {
			t.Errorf("路径 = %s，期望 %s", got, want)
		}
	}
}

// 旧响应把门槛包在 extra.restricted 对象里，顶层 d.restricted 也认。
func TestDecodeTopicRestricted(t *testing.T) {
	raw := `{"short_id":"R1","title":"受限","extra":{"restricted":{
        "access_type":"paid","required_coins":100,"required_level":5,
        "required_attributes":[{"name":"魅力","value":7}]
    }}}`
	topic, err := decodeTopic([]byte(raw), "R1")
	if err != nil {
		t.Fatal(err)
	}
	r := topic.Restricted
	if r == nil || !r.Known || r.AccessType != "paid" {
		t.Fatalf("门槛 = %+v", r)
	}
	if !r.CoinsKnown || r.Coins != 100 || !r.LevelKnown || r.Level != 5 {
		t.Errorf("金币 / 等级解码不对：%+v", r)
	}
	if !r.AttrKnown || r.AttrName != "魅力" || r.AttrValue != 7 {
		t.Errorf("属性解码不对：%+v", r)
	}
	lines := strings.Join(r.gateLines(), " / ")
	for _, want := range []string{"需要 100 金币解锁", "需要等级 5", "需要魅力达到 7"} {
		if !strings.Contains(lines, want) {
			t.Errorf("门槛说明 %q 里没有 %q", lines, want)
		}
	}
}

// 当前 api-read.md：门槛字段扁平地写在 extra 下，没有 extra.restricted 对象。
// 这是线上真实帖子的形态：认不出来整块受限内容（含竞拍按钮）都会消失。
func TestDecodeTopicRestrictedFlat(t *testing.T) {
	raw := `{"short_id":"R2","title":"受限","extra":{
        "restricted_access_type":"auction","has_restricted_hidden_body":true,
        "restricted_d20_attr":"魅力","restricted_d20_threshold":7
    }}`
	topic, err := decodeTopic([]byte(raw), "R2")
	if err != nil {
		t.Fatal(err)
	}
	r := topic.Restricted
	if r == nil || !r.Known || r.AccessType != "auction" {
		t.Fatalf("扁平门槛没认出来，整块 UI 会消失：%+v", r)
	}
	if !r.AttrKnown || r.AttrName != "魅力" || r.AttrValue != 7 {
		t.Errorf("D20 属性解码不对：%+v", r)
	}
	if lines := strings.Join(r.gateLines(), " / "); !strings.Contains(lines, "需要魅力达到 7") {
		t.Errorf("门槛说明 %q 里没有 D20 属性", lines)
	}
}

// 普通帖子没有门槛信号时不能凭空多出一块受限内容。
func TestDecodeTopicRestrictedAbsent(t *testing.T) {
	for _, raw := range []string{
		`{"short_id":"N","extra":{"restricted_access_type":null,"has_restricted_hidden_body":false}}`,
		`{"short_id":"N","extra":{"lottery":{"finished_at":"x"}}}`,
	} {
		topic, err := decodeTopic([]byte(raw), "N")
		if err != nil {
			t.Fatal(err)
		}
		if topic.Restricted != nil {
			t.Errorf("普通帖子被当成受限内容：%+v（%s）", topic.Restricted, raw)
		}
	}
}

// 未解锁没有正文，已解锁才给正文；显式布尔优先，没布尔时由正文推断。
func TestDecodeRestrictedUnlockedContent(t *testing.T) {
	r := decodeRestricted(json.RawMessage(`{"unlocked":true,"content":"隐藏的正文"}`))
	if !r.Known || !r.UnlockKnown || !r.Unlocked || !r.ContentKnown || r.Content != "隐藏的正文" {
		t.Fatalf("已解锁响应 = %+v", r)
	}
	if len(r.gateLines()) != 0 {
		t.Errorf("没有门槛字段时不该造出说明：%v", r.gateLines())
	}
	inferred := decodeRestricted(json.RawMessage(`{"hidden_content":"另一段正文"}`))
	if !inferred.Unlocked || !inferred.ContentKnown || inferred.Content != "另一段正文" {
		t.Errorf("由正文推断解锁失败：%+v", inferred)
	}
	// 未知字段不能把解码带偏。
	future := decodeRestricted(json.RawMessage(`{"future_field":{"a":1},"unlocked":false}`))
	if !future.UnlockKnown || future.Unlocked || len(future.gateLines()) != 0 {
		t.Errorf("未知字段解码 = %+v", future)
	}
}

// 竞拍状态与出价记录的子字段文档没给：包一层 / 裸数组 / 嵌套昵称都要认。
func TestDecodeRestrictedAuctionAndBids(t *testing.T) {
	a := decodeRestrictedAuction(json.RawMessage(`{
        "current_price":120,"remaining_seconds":3600,
        "top_bidders":[{"username":"甲","coins":120},{"user":{"username":"乙"},"amount":90}]
    }`))
	if !a.Known || !a.PriceKnown || a.Price != 120 || !a.RemainKnown || a.RemainSeconds != 3600 {
		t.Fatalf("竞拍状态 = %+v", a)
	}
	if len(a.Top) != 2 || a.Top[0].Name != "甲" || a.Top[0].Coins != 120 || a.Top[1].Name != "乙" {
		t.Errorf("领先名单 = %+v", a.Top)
	}

	b := decodeRestrictedBids(json.RawMessage(`{"bids":[
        {"bidder":"甲","price":120,"created_at":"2026-11-16T01:02:03.000Z"}
    ]}`))
	if !b.Known || len(b.Rows) != 1 || b.Rows[0].Name != "甲" || b.Rows[0].Coins != 120 {
		t.Fatalf("出价记录 = %+v", b)
	}
	bare := decodeRestrictedBids(json.RawMessage(`[{"username":"乙","amount":90}]`))
	if !bare.Known || len(bare.Rows) != 1 || bare.Rows[0].Coins != 90 {
		t.Errorf("裸数组出价记录 = %+v", bare)
	}
	// 没认出列表字段不能当成「无人出价」。
	if unknown := decodeRestrictedBids(json.RawMessage(`{"future_field":[]}`)); unknown.Known {
		t.Errorf("未知列表被当成空记录：%+v", unknown)
	}
}

type restrictedCall struct {
	method, path, auth, key, body string
}

func recordRestrictedCalls(t *testing.T, status int, body string) (*app, chan restrictedCall, func()) {
	t.Helper()
	got := make(chan restrictedCall, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- restrictedCall{
			method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"),
			key: r.Header.Get("Idempotency-Key"), body: string(b),
		}
		if status != 0 {
			w.WriteHeader(status)
			io.WriteString(w, body)
			return
		}
		io.WriteString(w, envelopeJSON(`{"unlocked":true,"content":"解锁后的正文"}`))
	}))
	return loginApp(t, srv), got, srv.Close
}

func readRestrictedCall(t *testing.T, got chan restrictedCall) restrictedCall {
	t.Helper()
	select {
	case c := <-got:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("没有收到受限内容请求")
		return restrictedCall{}
	}
}

// 三个读端点的路径、方法与令牌，以及响应解码。
func TestFetchRestrictedEndpoints(t *testing.T) {
	a, got, closeSrv := recordRestrictedCalls(t, 0, "")
	defer closeSrv()
	ctx := context.Background()
	info, err := a.client.fetchRestricted(ctx, "R1")
	if err != nil || !info.ContentKnown || info.Content != "解锁后的正文" {
		t.Fatalf("读受限内容失败：%+v / %v", info, err)
	}
	c := readRestrictedCall(t, got)
	if c.method != http.MethodGet || c.path != "/v1/topics/R1/restricted" || c.auth != "Bearer test-token" {
		t.Errorf("第一个请求 = %+v", c)
	}
	if c.key != "" || c.body != "" {
		t.Errorf("读端点不该带幂等键或请求体：%+v", c)
	}
	if _, err := a.client.fetchRestrictedAuction(ctx, "R1"); err != nil {
		t.Fatal(err)
	}
	if c = readRestrictedCall(t, got); c.path != "/v1/topics/R1/restricted/auction" {
		t.Errorf("竞拍状态路径 = %s", c.path)
	}
	if _, err := a.client.fetchRestrictedBids(ctx, "R1"); err != nil {
		t.Fatal(err)
	}
	if c = readRestrictedCall(t, got); c.path != "/v1/topics/R1/restricted/auction/bids" {
		t.Errorf("出价记录路径 = %s", c.path)
	}
}

// 解锁是标 🔑 的写端点：POST、无请求体、带令牌与幂等键。
func TestUnlockRestrictedRequest(t *testing.T) {
	a, got, closeSrv := recordRestrictedCalls(t, 0, "")
	defer closeSrv()
	res, err := a.client.unlockRestricted(context.Background(), "key-1", "R1")
	if err != nil || !res.Unlocked || !res.ContentKnown {
		t.Fatalf("解锁失败：%+v / %v", res, err)
	}
	c := readRestrictedCall(t, got)
	if c.method != http.MethodPost || c.path != "/v1/topics/R1/restricted/unlock" {
		t.Errorf("请求 = %s %s", c.method, c.path)
	}
	if c.auth != "Bearer test-token" || c.key != "key-1" {
		t.Errorf("鉴权 / 幂等键 = %q / %q", c.auth, c.key)
	}
	if c.body != "" {
		t.Errorf("文档写明无请求体，实际 = %q", c.body)
	}
}

// 未开通的 403 insufficient_scope 要把 code 与 required_scope 原样带回授权链路。
func TestUnlockRestrictedInsufficientScope(t *testing.T) {
	a, _, closeSrv := recordRestrictedCalls(t, http.StatusForbidden,
		`{"c":403,"code":"insufficient_scope","m":"需要开通","d":null,"t":0,"required_scope":["write:economy"]}`)
	defer closeSrv()
	_, err := a.client.unlockRestricted(context.Background(), "key-1", "R1")
	var ae *apiError
	if !asAPIError(err, &ae) || ae.Code != "insufficient_scope" || ae.Status != http.StatusForbidden {
		t.Fatalf("错误 = %v", err)
	}
	if len(ae.RequiredScope) != 1 || ae.RequiredScope[0] != "write:economy" {
		t.Errorf("required_scope = %v", ae.RequiredScope)
	}
}

func restrictedTestDetail(t *testing.T, a *app, coins int) *detailState {
	t.Helper()
	raw := fmt.Sprintf(`{"short_id":"R1","title":"受限帖","content":"公开正文",
        "extra":{"restricted":{"required_coins":%d}}}`, coins)
	topic, err := decodeTopic([]byte(raw), "R1")
	if err != nil {
		t.Fatal(err)
	}
	a.pageName = "detail"
	d := &detailState{shortID: "R1", topic: &topic}
	a.detail = d
	return d
}

// 没展开金额确认面板就不许扣币；失败重试复用幂等键，成功后换新键。
func TestSubmitRestrictedUnlockNeedsConfirmAndReusesKey(t *testing.T) {
	got := make(chan restrictedCall, 4)
	var fail atomic.Bool
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- restrictedCall{method: r.Method, path: r.URL.Path,
			key: r.Header.Get("Idempotency-Key"), body: string(b)}
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"c":500,"code":"internal_error","m":"稍后再试","d":null,"t":0}`)
			return
		}
		io.WriteString(w, envelopeJSON(`{"unlocked":true}`))
	}))
	defer srv.Close()
	a := loginApp(t, srv)
	events := make(chan coinEvent, 2)
	a.onCoinsChanged = func(e coinEvent) { events <- e }
	d := restrictedTestDetail(t, a, 100)

	// 没确认：一次请求都不许发。
	a.submitRestrictedUnlock(d)
	select {
	case c := <-got:
		t.Errorf("未确认就发了请求：%+v", c)
	case <-time.After(60 * time.Millisecond):
	}
	d.restricted.confirm = true
	a.submitRestrictedUnlock(d)
	c1 := readRestrictedCall(t, got)
	waitFor(t, func() bool { return !d.restricted.busy }, "首次解锁失败回到 UI 线程")
	if c1.key == "" || d.restricted.key != c1.key {
		t.Errorf("首次幂等键 = %q / 状态 %q", c1.key, d.restricted.key)
	}
	fail.Store(false)
	a.submitRestrictedUnlock(d)
	c2 := readRestrictedCall(t, got)
	waitFor(t, func() bool { return !d.restricted.busy }, "重试成功回到 UI 线程")
	if c2.key != c1.key || c2.body != "" {
		t.Errorf("重试应复用同一把键且无请求体：%+v / %+v", c1, c2)
	}
	if d.restricted.confirm || d.restricted.key != "" || d.restricted.info == nil || !d.restricted.info.Unlocked {
		t.Errorf("成功后的状态 = %+v", d.restricted)
	}
	select {
	case e := <-events:
		if e != coinEventRestricted {
			t.Errorf("金币事件 = %q", e)
		}
	case <-time.After(time.Second):
		t.Error("没有发出金币事件")
	}
}

// 详情页正文末尾要画出门槛，点解锁先出金额确认，再点确认才发请求。
func TestRestrictedSectionRendersAndConfirms(t *testing.T) {
	a, got, closeSrv := testRestrictedUIApp(t)
	defer closeSrv()
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"受限内容", "需要 100 金币解锁", "花 100 金币解锁"} {
		if !tt.HasText(want) {
			t.Errorf("没有 %q：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("花 100 金币解锁"); err != nil {
		t.Fatalf("点解锁失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	for _, want := range []string{"此次解锁将扣除 100 金币，确认后不可撤销。", "确认花 100 金币解锁"} {
		if !tt.HasText(want) {
			t.Errorf("确认面板没有 %q：%q", want, tt.Texts())
		}
	}
	select {
	case c := <-got:
		t.Errorf("只展开确认面板不该发请求：%+v", c)
	default:
	}
	if err := tt.Click("确认花 100 金币解锁"); err != nil {
		t.Fatalf("点确认失败：%v（%q）", err, tt.Texts())
	}
	c := readRestrictedCall(t, got)
	if c.method != http.MethodPost || c.path != "/v1/topics/R1/restricted/unlock" || c.key == "" {
		t.Errorf("确认请求 = %+v", c)
	}
}

// 竞拍状态与出价记录读回来后直接画在受限内容块里。
func TestRestrictedAuctionAndBidsRender(t *testing.T) {
	a, _, closeSrv := testRestrictedUIApp(t)
	defer closeSrv()
	d := a.detail
	d.restricted.auction = &auctionInfo{
		Known: true, Price: 120, PriceKnown: true, RemainSeconds: 3600, RemainKnown: true,
		Top: []auctionRow{{Name: "甲", Coins: 120, CoinsKnown: true}},
	}
	d.restricted.bids = &auctionBids{
		Known: true, Rows: []auctionRow{{Name: "乙", Coins: 90, CoinsKnown: true}},
	}
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{
		"竞拍状态", "当前价 120 金币", "剩余 3600 秒", "甲 · 120 金币",
		"出价记录", "乙 · 90 金币",
	} {
		if !tt.HasText(want) {
			t.Errorf("没有 %q：%q", want, tt.Texts())
		}
	}
}

func testRestrictedUIApp(t *testing.T) (*app, chan restrictedCall, func()) {
	t.Helper()
	a, got, closeSrv := recordRestrictedCalls(t, 0, "")
	restrictedTestDetail(t, a, 100)
	return a, got, closeSrv
}

// 详情打开后自动拉取三块只读内容：info / auction / bids 各发一次 GET，
// 结果直接填进状态，不必再点按钮。
func TestAutoLoadRestrictedFetchesAllThree(t *testing.T) {
	got := make(chan restrictedCall, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- restrictedCall{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
		switch r.URL.Path {
		case "/v1/topics/AR1/restricted":
			io.WriteString(w, envelopeJSON(`{"unlocked":false}`))
		case "/v1/topics/AR1/restricted/auction":
			io.WriteString(w, envelopeJSON(`{"current_price":120,"remaining_seconds":3600}`))
		case "/v1/topics/AR1/restricted/auction/bids":
			io.WriteString(w, envelopeJSON(`{"bids":[{"bidder":"甲","price":120}]}`))
		default:
			t.Errorf("意外的路径 %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	a := loginApp(t, srv)
	a.autoLoad = true
	topic := topicDetail{ShortID: "AR1", Restricted: &restrictedInfo{Known: true}}
	d := &detailState{shortID: "AR1", topic: &topic}
	a.pageName = "detail"
	a.detail = d

	a.autoLoadRestricted(d)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		c := readRestrictedCall(t, got)
		if c.method != http.MethodGet || c.auth != "Bearer test-token" {
			t.Errorf("自动请求 = %+v", c)
		}
		seen[c.path] = true
	}
	for _, path := range []string{
		"/v1/topics/AR1/restricted",
		"/v1/topics/AR1/restricted/auction",
		"/v1/topics/AR1/restricted/auction/bids",
	} {
		if !seen[path] {
			t.Errorf("没有自动请求 %s：%v", path, seen)
		}
	}
	waitFor(t, func() bool {
		return d.restricted.info != nil && d.restricted.auction != nil && d.restricted.bids != nil
	}, "三块自动加载结果没有回到状态")
	if !d.restricted.auction.PriceKnown || d.restricted.auction.Price != 120 {
		t.Errorf("竞拍状态没解出来：%+v", d.restricted.auction)
	}
	if !d.restricted.bids.Known || len(d.restricted.bids.Rows) != 1 || d.restricted.bids.Rows[0].Name != "甲" {
		t.Errorf("出价记录没解出来：%+v", d.restricted.bids)
	}
	// 同一篇帖子再触发一次不该重复发请求。
	a.autoLoadRestricted(d)
	select {
	case c := <-got:
		t.Errorf("重复触发发了请求：%+v", c)
	case <-time.After(60 * time.Millisecond):
	}
}

// 自动拉取刻意安静：未登录不发请求、不弹登录；缺 scope 不弹授权，只留重试。
func TestAutoLoadRestrictedIsQuiet(t *testing.T) {
	got := make(chan restrictedCall, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- restrictedCall{path: r.URL.Path}
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"c":403,"code":"insufficient_scope","m":"需要开通","d":null,"t":0,"required_scope":["read"]}`)
	}))
	defer srv.Close()
	a := loginApp(t, srv)
	a.autoLoad = true
	// newApp 注册的会话变化回调会因 Logout / scope 收窄自己弹授权，那是测试
	// 脚手架副作用；整个测试断开它，只检验自动拉取本身不弹窗。
	a.auth.setOnChange(nil)
	topic := topicDetail{ShortID: "Q1", Restricted: &restrictedInfo{Known: true}}
	d := &detailState{shortID: "Q1", topic: &topic}
	a.pageName = "detail"
	a.detail = d

	// 退出登录后自动拉取应整体跳过，且不弹登录框。
	a.auth.Logout()
	a.authGate = authGate{}
	a.autoLoadRestricted(d)
	select {
	case c := <-got:
		t.Errorf("未登录还发了请求：%+v", c)
	default:
	}
	if a.authGate.open {
		t.Error("自动拉取不该弹登录框")
	}

	// 重新登录，403 insufficient_scope 应留错误可重试，但不弹授权弹窗。
	if err := a.auth.storeToken(tokenData{AccessToken: "test-token", TokenType: "Bearer",
		Username: "reader", Scope: "read"}); err != nil {
		t.Fatal(err)
	}
	a.authGate = authGate{}
	d2 := &detailState{shortID: "Q1", topic: &topic}
	a.detail = d2
	a.autoLoadRestricted(d2)
	for i := 0; i < 3; i++ {
		readRestrictedCall(t, got)
	}
	waitFor(t, func() bool {
		for _, kind := range []string{"info", "auction", "bids"} {
			if d2.restricted.load(kind).loading {
				return false
			}
		}
		return true
	}, "三块自动拉取没有结束")
	if a.authGate.open {
		t.Error("自动拉取遇 insufficient_scope 不该弹授权弹窗")
	}
	for _, kind := range []string{"info", "auction", "bids"} {
		if d2.restricted.load(kind).err == "" {
			t.Errorf("%s 的读取失败原因没留下", kind)
		}
	}
}
