package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 金币池：只读展示帖子的池子余额与状态，以及每条评论从池里领到了多少。

func TestDecodeTopicRewardPool(t *testing.T) {
	// 线上实测把 reward_pool 放在详情顶层。
	p, err := decodeTopic([]byte(`{
		"short_id":"POOL1","title":"有金币池","content":"正文",
		"reward_pool":{"current_coins":320,"finished":false,"is_expired":false}
	}`), "POOL1")
	if err != nil {
		t.Fatal(err)
	}
	if p.RewardPool == nil || !p.RewardPool.Known || p.RewardPool.CurrentCoins != 320 ||
		!p.RewardPool.CoinsKnown || p.RewardPool.Finished || p.RewardPool.Expired {
		t.Fatalf("顶层金币池解析错误：%+v", p.RewardPool)
	}

	// api-read.md 写的是 extra.reward_pool，两处都认。
	p, err = decodeTopic([]byte(`{
		"short_id":"POOL2","title":"extra 金币池","content":"正文",
		"extra":{"reward_pool":{"current_coins":100,"finished":true}}
	}`), "POOL2")
	if err != nil {
		t.Fatal(err)
	}
	if p.RewardPool == nil || p.RewardPool.CurrentCoins != 100 || !p.RewardPool.Finished ||
		p.RewardPool.Expired {
		t.Fatalf("extra 金币池解析错误：%+v", p.RewardPool)
	}

	// is_expired 是另一个键，不能和 finished 混成一个。
	p, err = decodeTopic([]byte(`{
		"short_id":"POOL3","title":"已过期","content":"正文",
		"extra":{"reward_pool":{"current_coins":5,"is_expired":true}}
	}`), "POOL3")
	if err != nil {
		t.Fatal(err)
	}
	if p.RewardPool == nil || !p.RewardPool.Expired || p.RewardPool.Finished {
		t.Fatalf("过期标记解析错误：%+v", p.RewardPool)
	}

	// 没有金币池的帖子不画这一块：nil 与「池子空了」是两回事。
	p, err = decodeTopic([]byte(`{"short_id":"POOL4","title":"没有池子","content":"正文"}`), "POOL4")
	if err != nil {
		t.Fatal(err)
	}
	if p.RewardPool != nil {
		t.Fatalf("没有 reward_pool 时不应造出一个池子：%+v", p.RewardPool)
	}

	// 池子块在、但没给余额：不能把缺字段当成「已领完」。
	p, err = decodeTopic([]byte(`{
		"short_id":"POOL6","title":"没给余额","content":"正文",
		"reward_pool":{"finished":false}
	}`), "POOL6")
	if err != nil {
		t.Fatal(err)
	}
	if p.RewardPool == nil || p.RewardPool.CoinsKnown {
		t.Fatalf("没给 current_coins 时不应把余额当成已知：%+v", p.RewardPool)
	}
	if got := rewardPoolStateText(p.RewardPool); got != "" {
		t.Errorf("余额未知时的状态 = %q，期望空（不再标注进行中）", got)
	}

	// 异常余额夹到 0，不把负数展示出去。
	p, err = decodeTopic([]byte(`{
		"short_id":"POOL5","title":"负数","content":"正文",
		"reward_pool":{"current_coins":-8}
	}`), "POOL5")
	if err != nil {
		t.Fatal(err)
	}
	if p.RewardPool == nil || p.RewardPool.CurrentCoins != 0 {
		t.Fatalf("负数余额应夹到 0：%+v", p.RewardPool)
	}
}

func TestDecodeRewardPoolWinnings(t *testing.T) {
	// 文档给的是 {"amount": 53}；数字数组也认。0 与负数不记。
	got := decodeRewardPoolWinnings([]byte(`[
		{"amount":53},{"coins":21},{"amount":0},{"amount":-5},{"other":1},7,8.4
	]`))
	want := []int{53, 21, 7, 8}
	if len(got) != len(want) {
		t.Fatalf("领到的金币 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("领到的金币 = %v，期望 %v", got, want)
		}
	}
	if got := decodeRewardPoolWinnings(nil); got != nil {
		t.Errorf("没有记录时不应给出金额：%v", got)
	}
	if total := rewardPoolWinningsTotal([]int{53, 0, -1, 21}); total != 74 {
		t.Errorf("合计 = %d，期望 74", total)
	}
}

func TestRewardPoolStateText(t *testing.T) {
	cases := []struct {
		pool *rewardPoolInfo
		want string
	}{
		{&rewardPoolInfo{CurrentCoins: 320, CoinsKnown: true}, ""},
		{&rewardPoolInfo{CurrentCoins: 0, CoinsKnown: true}, ""},
		{&rewardPoolInfo{}, ""},
		{&rewardPoolInfo{CurrentCoins: 320, CoinsKnown: true, Finished: true}, "（已结束）"},
		{&rewardPoolInfo{CurrentCoins: 320, CoinsKnown: true, Expired: true}, "（已结束）"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := rewardPoolStateText(c.pool); got != c.want {
			t.Errorf("%+v 的状态 = %q，期望 %q", c.pool, got, c.want)
		}
	}
}

// 详情页正文末尾画出自己的金币池：💰 + 余额、统一说明，以及注资入口；
// 评论头部标出这条评论从池里领到的合计。
func TestRewardPoolSectionAndReplyWinnings(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv) // 当前登录用户 reader
	a.openDetail(Item{ShortID: "POOL01", URL: "https://2libra.com/post/work/POOL01"})
	topic, err := decodeTopic([]byte(`{
		"short_id":"POOL01","title":"有池子的帖子","content":"一段正文。",
		"author":{"username":"reader"},"reward_pool":{"current_coins":320}
	}`), "POOL01")
	if err != nil {
		t.Fatal(err)
	}
	a.detail.topic = &topic
	a.detail.replyList = []replyItem{{
		ID: "c1", Author: "甲", Floor: 1, Content: "领到了",
		RewardPoolWinnings: []int{53, 21},
	}}
	a.detail.replyTotal = 1
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{
		"💰 320 金币", "投入金币", "金币池 +74", rewardPoolHint,
	} {
		if !tt.HasText(want) {
			t.Errorf("详情页缺少 %q：%q", want, tt.Texts())
		}
	}
	// 不再画旧的「金币池」标题说明与「池中还有」文案。
	for _, stale := range []string{"池中还有", "浏览这篇帖子时按概率发放", "进行中"} {
		if tt.HasText(stale) {
			t.Errorf("详情页不应再出现旧文案 %q：%q", stale, tt.Texts())
		}
	}
}

// 别人的帖子只读展示金币池状态，不给「投入金币」入口。
func TestRewardPoolSectionOthersPostHasNoFundButton(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv) // 当前登录用户 reader
	a.openDetail(Item{ShortID: "POOL02", URL: "https://2libra.com/post/work/POOL02"})
	topic, err := decodeTopic([]byte(`{
		"short_id":"POOL02","title":"别人的帖子","content":"一段正文。",
		"author":{"username":"jimmy"},"reward_pool":{"current_coins":320}
	}`), "POOL02")
	if err != nil {
		t.Fatal(err)
	}
	a.detail.topic = &topic
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("💰 320 金币") {
		t.Errorf("别人的帖子也应展示金币池状态：%q", tt.Texts())
	}
	if tt.HasText("投入金币") {
		t.Errorf("别人的帖子不该给注资入口：%q", tt.Texts())
	}
}

// 已结束的池子金币归零、补一行「（已结束）」，且不再给注资入口。
func TestRewardPoolSectionFinished(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv) // 当前登录用户 reader
	a.openDetail(Item{ShortID: "POOL03", URL: "https://2libra.com/post/work/POOL03"})
	topic, err := decodeTopic([]byte(`{
		"short_id":"POOL03","title":"已结束的池子","content":"一段正文。",
		"author":{"username":"reader"},
		"reward_pool":{"current_coins":414,"finished":true}
	}`), "POOL03")
	if err != nil {
		t.Fatal(err)
	}
	a.detail.topic = &topic
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"💰 0 金币", "（已结束）", rewardPoolHint} {
		if !tt.HasText(want) {
			t.Errorf("已结束的金币池缺少 %q：%q", want, tt.Texts())
		}
	}
	if tt.HasText("投入金币") {
		t.Errorf("已结束的池子不该给注资入口：%q", tt.Texts())
	}
}

// 没有金币池的帖子不留空块，也不要一个 0 金币的池子。
func TestRewardPoolSectionHiddenWithoutPool(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "NOPOOL", URL: "https://2libra.com/post/work/NOPOOL"})
	topic, err := decodeTopic([]byte(`{
		"short_id":"NOPOOL","title":"普通帖子","content":"一段正文。","author":{"username":"jimmy"}
	}`), "NOPOOL")
	if err != nil {
		t.Fatal(err)
	}
	a.detail.topic = &topic
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	// 标题里不能再出现「金币池」三个字，否则这条断言就成了自证。
	for _, want := range []string{"池中还有", "金币池", "进行中"} {
		if tt.HasText(want) {
			t.Errorf("没有 reward_pool 时不应画金币池块（命中 %q）：%q", want, tt.Texts())
		}
	}
}

/* ------------------------------------------------------------------
   注资：路径 / 请求体 / 幂等键，以及气泡与提交链路
--------------------------------------------------------------- */

// 注资端点必须打到文档路径，body 带 post_id 与 coins；路径里的 short_id 要转义。
func TestFundRewardPoolRequest(t *testing.T) {
	if got, want := topicRewardPoolPath("POOL01"), "/v1/topics/POOL01/reward-pool"; got != want {
		t.Errorf("注资路径 = %q，期望 %q", got, want)
	}
	if got, want := topicRewardPoolPath("a/b"), "/v1/topics/a%2Fb/reward-pool"; got != want {
		t.Errorf("short_id 转义不对：%q，期望 %q", got, want)
	}

	type request struct {
		method, path, key, auth, body string
	}
	reqCh := make(chan request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("请求体不是 JSON：%v", err)
		}
		raw, _ := json.Marshal(body)
		reqCh <- request{
			method: r.Method, path: r.URL.Path,
			key: r.Header.Get("Idempotency-Key"), auth: r.Header.Get("Authorization"),
			body: string(raw),
		}
		fmt.Fprint(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	if err := c.fundRewardPool(context.Background(), "POOL01", "key-1", 300); err != nil {
		t.Fatalf("注资请求失败：%v", err)
	}
	req := <-reqCh
	if req.method != http.MethodPost || req.path != "/api/v1/topics/POOL01/reward-pool" {
		t.Errorf("注资请求 = %s %s", req.method, req.path)
	}
	if req.key != "key-1" || req.auth != "Bearer tok" {
		t.Errorf("幂等键 / 鉴权不对：%q / %q", req.key, req.auth)
	}
	if req.body != `{"coins":300,"post_id":"POOL01"}` {
		t.Errorf("请求体 = %s，期望带 post_id 与 coins", req.body)
	}
}

// 金额收成 200～1000 的整数，越界夹到边界。
func TestRewardPoolAmount(t *testing.T) {
	cases := []struct {
		in   float64
		want int
	}{
		{0, 200}, {-50, 200}, {199.4, 200}, {200, 200},
		{300.6, 301}, {999, 999}, {1000, 1000}, {5000, 1000},
	}
	for _, c := range cases {
		if got := rewardPoolAmount(c.in); got != c.want {
			t.Errorf("rewardPoolAmount(%v) = %d，期望 %d", c.in, got, c.want)
		}
	}
}

// 提交成功后收起气泡、刷新金币事件，并按已知余额就地加上这笔钱。
func TestRewardPoolFundingSubmits(t *testing.T) {
	reqCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/topics/POOL01/reward-pool" {
			t.Errorf("注资请求 = %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		reqCh <- string(raw) + "|" + r.Header.Get("Idempotency-Key")
		fmt.Fprint(w, envelopeJSON(`{}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.openDetail(Item{ShortID: "POOL01", URL: "https://2libra.com/post/work/POOL01"})
	topic, err := decodeTopic([]byte(`{
		"short_id":"POOL01","title":"有池子的帖子","content":"一段正文。",
		"author":{"username":"reader"},"reward_pool":{"current_coins":320}
	}`), "POOL01")
	if err != nil {
		t.Fatal(err)
	}
	a.detail.topic = &topic
	a.detail.pool.amount = 300
	a.detail.pool.open = true

	done := make(chan coinEvent, 1)
	a.onCoinsChanged = func(e coinEvent) { done <- e }
	a.submitRewardPool(a.detail)
	select {
	case e := <-done:
		if e != coinEventRewardPool {
			t.Errorf("金币事件 = %q，期望 %q", e, coinEventRewardPool)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("注资没有完成")
	}

	req := <-reqCh
	if !strings.HasPrefix(req, `{"post_id":"POOL01","coins":300}|`) {
		t.Errorf("请求体 / 键 = %q", req)
	}
	key := strings.TrimPrefix(req, `{"post_id":"POOL01","coins":300}|`)
	if !keyPattern.MatchString(key) {
		t.Errorf("幂等键格式不对：%q", key)
	}
	if a.detail.pool.open || a.detail.pool.key != "" || a.detail.pool.busy {
		t.Errorf("成功后气泡状态没收拾：%+v", a.detail.pool)
	}
	if a.detail.topic.RewardPool == nil || a.detail.topic.RewardPool.CurrentCoins != 620 {
		t.Errorf("本地池子余额没有加上：%+v", a.detail.topic.RewardPool)
	}
}

// 面板：金额、常用金额、余额与确认按钮都在；按钮上写着实际金额。
func TestRewardPoolFundingPanel(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv)
	a.openDetail(Item{ShortID: "POOL01", URL: "https://2libra.com/post/work/POOL01"})
	topic, err := decodeTopic([]byte(`{
		"short_id":"POOL01","title":"有池子的帖子","content":"一段正文。",
		"author":{"username":"reader"},"reward_pool":{"current_coins":320}
	}`), "POOL01")
	if err != nil {
		t.Fatal(err)
	}
	a.detail.topic = &topic
	a.detail.replyLoaded = true
	a.detail.pool.amount = 300

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("投入金币"); err != nil {
		t.Fatalf("点「投入金币」失败：%v", err)
	}
	tt.Frame()
	if !a.detail.pool.open {
		t.Fatalf("点「投入金币」没有打开注资气泡")
	}
	for _, want := range []string{
		"投入金币", "给金币池注资", "池中还有 320 金币。",
		"常用金额", "投入 300 金币", "会从你的余额扣除",
	} {
		if !tt.HasText(want) {
			t.Errorf("注资面板缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 池子结束 / 过期 / 消失后，注资气泡必须收起。
func TestSyncRewardPoolClosesPopup(t *testing.T) {
	cases := []*topicDetail{
		{RewardPool: nil},
		{RewardPool: &rewardPoolInfo{Known: true, Finished: true}},
		{RewardPool: &rewardPoolInfo{Known: true, Expired: true}},
	}
	for _, topic := range cases {
		d := &detailState{pool: rewardPoolUIState{open: true}}
		syncRewardPool(d, topic)
		if d.pool.open {
			t.Errorf("池子 %+v 结束 / 消失后气泡仍开着", topic.RewardPool)
		}
	}

	d := &detailState{pool: rewardPoolUIState{open: true}}
	syncRewardPool(d, &topicDetail{RewardPool: &rewardPoolInfo{Known: true, CurrentCoins: 10}})
	if !d.pool.open {
		t.Error("进行中的池子不该收起气泡")
	}
}
