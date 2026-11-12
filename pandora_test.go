package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 潘多拉的数据层与界面：解码、两次抽奖的请求契约、授权链路、窗口入口与
// 最小尺寸下的可操作性。

func pandoraTestApp(t *testing.T, auto bool) *app {
	t.Helper()
	a := testApp(t)
	a.autoLoad = auto
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: "jimmy",
		Scope: "read profile read:me read:economy",
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

func freezePandoraNow(t *testing.T) {
	t.Helper()
	old := pandoraNow
	pandoraNow = func() time.Time {
		return time.Date(2026, 11, 12, 20, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	}
	t.Cleanup(func() { pandoraNow = old })
}

func waitPandora(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等待「%s」超时", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDecodePandoraToday(t *testing.T) {
	d := json.RawMessage(`{
		"has_box": true, "can_open": false, "opened_today": true,
		"next_open_at": "2026-11-12T16:00:00.000Z", "active_extended": false,
		"box": {
			"box_instance_id": "3ba2e842-ee28-46b9-bdd5-f19fa7463fdd",
			"event_code": "coin_gain_flat", "event_name": "金币雨",
			"event_title": "小小金币雨，获得 67 金币。", "tier_code": "bronze",
			"rarity": "bronze", "rolled_value": 67,
			"open_at": "2026-11-11T16:04:46.604Z", "apply_at": "2026-11-11T16:05:46.604Z",
			"expire_at": "2026-11-12T16:05:46.604Z",
			"reroll_deadline_at": "2026-11-11T16:05:36.604Z",
			"reroll_used": 0, "reroll_limit": 1, "force_purple_reroll": false
		}
	}`)
	st := decodePandoraStatus(d)
	if !st.Known || !st.FlagsKnown || !st.HasBox || !st.OpenedToday || st.CanOpen {
		t.Fatalf("今日状态解码不对：%+v", st)
	}
	if !st.BoxKnown || st.Box.InstanceID == "" || st.Box.EventName != "金币雨" {
		t.Fatalf("box 解码不对：%+v", st.Box)
	}
	if !st.Box.RolledKnown || st.Box.RolledValue != 67 {
		t.Errorf("rolled_value = %v（known=%v），期望 67", st.Box.RolledValue, st.Box.RolledKnown)
	}
	if st.Box.Rarity != "bronze" || !st.Box.CountersKnown || st.Box.RerollLimit != 1 {
		t.Errorf("稀有度 / 重抽计数解码不对：%+v", st.Box)
	}
	if st.drawsUsed() != 1 {
		t.Errorf("已抽次数 = %d，期望 1", st.drawsUsed())
	}
	if st.canReroll(pandoraNow()) {
		t.Error("重抽截止时间已过，不该再提供第二次抽奖")
	}
}

// 开盒响应只返回一个 box 时，今日状态里的标志不能被零值冲掉；重抽响应
// 没带计数时，本地按「用掉一次」补上，按钮立刻消失。
func TestPandoraActionMergePartialBox(t *testing.T) {
	freezePandoraNow(t)
	prev := pandoraStatus{
		Known: true, FlagsKnown: true, HasBox: false, CanOpen: true,
		NextOpenAt: "2026-11-12T16:00:00.000Z",
	}
	opened := decodePandoraStatus(json.RawMessage(`{"box":{
		"box_instance_id":"b1","event_name":"金币雨","event_title":"获得 67 金币。",
		"reroll_used":0,"reroll_limit":1,
		"reroll_deadline_at":"2026-11-12T15:00:00.000Z",
		"expire_at":"2026-11-12T16:05:46.604Z"}}`))
	next := applyPandoraAction(prev, opened, "open")
	if !next.OpenedToday || next.CanOpen || !next.HasBox || !next.BoxKnown {
		t.Fatalf("开盒后状态不对：%+v", next)
	}
	if next.NextOpenAt != prev.NextOpenAt {
		t.Errorf("partial 响应不应丢掉 next_open_at：%q", next.NextOpenAt)
	}
	if next.drawsUsed() != 1 {
		t.Errorf("开盒后已抽 %d 次，期望 1", next.drawsUsed())
	}
	if !next.canReroll(pandoraNow()) {
		t.Fatal("开盒后应还能抽第二次（重抽）")
	}

	rerolled := decodePandoraStatus(json.RawMessage(`{"box":{
		"box_instance_id":"b1","event_name":"头像翻转",
		"event_title":"头像翻转 24 小时"}}`))
	after := applyPandoraAction(next, rerolled, "reroll")
	if after.drawsUsed() != 2 {
		t.Errorf("重抽后已抽 %d 次，期望 2", after.drawsUsed())
	}
	if after.canReroll(pandoraNow()) {
		t.Error("本应用一天只抽两次，重抽后不该再有第三个入口")
	}
	if after.Box.EventName != "头像翻转" {
		t.Errorf("重抽结果没有替换：%+v", after.Box)
	}
}

// 两次抽奖的请求契约：POST /v1/pandora/open（无请求体）与
// POST /v1/pandora/reroll（box_instance_id），都带令牌与 Idempotency-Key，
// 成功后发出金币变动事件。点第二次重抽时不再发请求。
func TestPandoraOpenAndRerollContract(t *testing.T) {
	freezePandoraNow(t)
	var mu sync.Mutex
	var paths []string
	var keys []string
	var bodies []string
	var auths []string
	var gets, opens, rerolls atomic.Int32
	opened, rerolled := false, false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("%s 缺少令牌：%q", r.URL.Path, got)
		}
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		if key := r.Header.Get("Idempotency-Key"); key != "" {
			keys = append(keys, r.URL.Path+" "+key)
		}
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/pandora/today":
			gets.Add(1)
			mu.Lock()
			state := struct {
				opened, rerolled bool
			}{opened, rerolled}
			mu.Unlock()
			if !state.opened {
				_, _ = io.WriteString(w, envelopeJSON(`{
					"has_box":false,"can_open":true,"opened_today":false,
					"next_open_at":"2026-11-12T16:00:00.000Z"}`))
				return
			}
			used := 0
			if state.rerolled {
				used = 1
			}
			name, title := "金币雨", "小小金币雨，获得 67 金币。"
			if state.rerolled {
				name, title = "头像翻转", "头像翻转 24 小时"
			}
			_, _ = io.WriteString(w, envelopeJSON(`{
				"has_box":true,"can_open":false,"opened_today":true,
				"next_open_at":"2026-11-12T16:00:00.000Z",
				"box":{"box_instance_id":"b1","event_name":"`+name+`","event_title":"`+title+`",
					"rarity":"bronze","rolled_value":67,
					"reroll_used":`+strconv.Itoa(used)+`,"reroll_limit":1,
					"reroll_deadline_at":"2026-11-12T15:00:00.000Z",
					"expire_at":"2026-11-12T16:05:46.604Z"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pandora/open":
			opens.Add(1)
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(body))
			opened = true
			mu.Unlock()
			_, _ = io.WriteString(w, envelopeJSON(`{"box":{
				"box_instance_id":"b1","event_name":"金币雨",
				"event_title":"小小金币雨，获得 67 金币。","rarity":"bronze",
				"rolled_value":67,"reroll_used":0,"reroll_limit":1,
				"reroll_deadline_at":"2026-11-12T15:00:00.000Z",
				"expire_at":"2026-11-12T16:05:46.604Z"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pandora/reroll":
			rerolls.Add(1)
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(body))
			rerolled = true
			mu.Unlock()
			if !strings.Contains(string(body), `"box_instance_id":"b1"`) {
				t.Errorf("重抽请求体不对：%s", body)
			}
			// 只返回新 box，不带重抽计数：应用按「用掉一次」补。
			_, _ = io.WriteString(w, envelopeJSON(`{"box":{
				"box_instance_id":"b1","event_name":"头像翻转",
				"event_title":"头像翻转 24 小时","rarity":"silver"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := pandoraTestApp(t, true)
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	defer a.stopCoinRefresh() // 动作会触发合并的金币刷新，测试结束前取消
	events := make(chan coinEvent, 8)
	a.onCoinsChanged = func(e coinEvent) { events <- e }

	a.showPandoraWindow()
	waitPandora(t, "今日盲盒状态", func() bool {
		st := a.pandora
		return st != nil && st.loaded && st.status.CanOpen
	})

	a.openPandoraBox()
	waitPandora(t, "开盒成功", func() bool {
		st := a.pandora
		return st.busy == "" && st.loaded && st.status.OpenedToday &&
			st.status.BoxKnown && st.status.Box.RerollUsed == 0
	})
	if got := opens.Load(); got != 1 {
		t.Fatalf("开盒请求 %d 次，期望 1", got)
	}
	if got := a.pandora.status.drawsUsed(); got != 1 {
		t.Errorf("开盒后今日抽奖 %d/2，期望 1/2", got)
	}
	if !a.pandora.status.canReroll(pandoraNow()) {
		t.Fatal("开盒后没有给出第二次抽奖（重抽）")
	}
	if !pandoraHasToast(a, "盲盒已开启") {
		t.Errorf("开盒成功没有 toast：%+v", a.toasts)
	}
	select {
	case e := <-events:
		if e != coinEventPandora {
			t.Errorf("金币事件 = %q，期望 %q", e, coinEventPandora)
		}
	case <-time.After(time.Second):
		t.Fatal("开盒成功后没有发出金币变动事件")
	}
	if !a.pandora.entryKnown || a.pandora.entryStatus.Box.EventName != "金币雨" {
		t.Errorf("开盒后首页入口没有拿到事件名：%+v", a.pandora.entryStatus)
	}

	// 第二次抽奖：重抽。
	a.rerollPandoraBox()
	waitPandora(t, "重抽成功", func() bool {
		st := a.pandora
		return st.busy == "" && st.loaded && st.status.BoxKnown &&
			st.status.Box.RerollUsed >= 1
	})
	waitPandora(t, "重抽状态落定", func() bool {
		st := a.pandora
		return st.loaded && !st.status.canReroll(pandoraNow())
	})
	if got := rerolls.Load(); got != 1 {
		t.Fatalf("重抽请求 %d 次，期望 1", got)
	}
	if name := a.pandora.status.Box.EventName; name != "头像翻转" {
		t.Errorf("重抽后的事件 = %q，期望 头像翻转", name)
	}
	if !a.pandora.entryKnown || a.pandora.entryStatus.Box.EventName != "头像翻转" {
		t.Errorf("重抽后首页入口没有换成新事件名：%+v", a.pandora.entryStatus)
	}
	if got := a.pandora.status.drawsUsed(); got != 2 {
		t.Errorf("重抽后今日抽奖 %d/2，期望 2/2", got)
	}
	if !pandoraHasToast(a, "已再抽一次") {
		t.Errorf("重抽成功没有 toast：%+v", a.toasts)
	}
	select {
	case e := <-events:
		if e != coinEventPandora {
			t.Errorf("第二次金币事件 = %q，期望 %q", e, coinEventPandora)
		}
	case <-time.After(time.Second):
		t.Fatal("重抽成功后没有发出金币变动事件")
	}

	// 第三次：本应用只给两次抽奖，入口应当直接关掉，不再发请求。
	a.rerollPandoraBox()
	if got := rerolls.Load(); got != 1 {
		t.Errorf("用完两次后仍发起了重抽：%d 次", got)
	}
	if !pandoraHasToast(a, "今天没有可用的重抽次数") {
		t.Errorf("第三次抽奖没有提示：%+v", a.toasts)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(auths) == 0 || auths[0] != "Bearer tok" {
		t.Errorf("请求没有带令牌：%v", auths)
	}
	for _, p := range paths {
		if p == "POST /api/v1/pandora/open" || p == "POST /api/v1/pandora/reroll" {
			continue
		}
		if !strings.HasPrefix(p, "GET /api/v1/pandora/today") {
			t.Errorf("出现了意料之外的请求：%s", p)
		}
	}
	if len(keys) != 2 {
		t.Errorf("应给两次写请求各铸一个 Idempotency-Key，实际 %v", keys)
	}
	if len(bodies) != 2 || strings.TrimSpace(bodies[0]) != "" {
		t.Errorf("开盒不应带请求体；实际 %q", bodies)
	}
	if gets.Load() < 2 {
		t.Errorf("动作后没有重读今日状态：GET %d 次", gets.Load())
	}
}

// 缺少 write:economy 时和签到 / 收藏一样交给全局授权弹窗，不把 403 当普通
// 错误弹 toast；授权成功后重跑同一次抽奖。
func TestPandoraOpenNeedsAuthorization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/pandora/today":
			_, _ = io.WriteString(w, envelopeJSON(`{
				"has_box":false,"can_open":true,"opened_today":false}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pandora/open":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"c":403,"code":"insufficient_scope",
				"required_scope":["write:economy"],"m":"缺少权限范围","d":null}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := pandoraTestApp(t, true)
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.showPandoraWindow()
	waitPandora(t, "今日盲盒状态", func() bool {
		st := a.pandora
		return st != nil && st.loaded && st.status.CanOpen
	})
	a.openPandoraBox()
	waitPandora(t, "授权弹窗", func() bool { return a.authGate.open })
	if len(a.authGate.need.Scopes) != 1 || a.authGate.need.Scopes[0] != "write:economy" {
		t.Errorf("授权弹窗缺少 write:economy：%+v", a.authGate.need)
	}
	if !a.pandora.status.CanOpen {
		t.Error("授权失败时不应把「可开启」状态改掉")
	}
	if pandoraHasToast(a, "缺少权限") || pandoraHasToast(a, "403") {
		t.Errorf("缺权限不该显示普通错误 toast：%+v", a.toasts)
	}
}

// 授权成功后要能接着重跑「第二次抽奖」：重新授权会重建金币等状态，但同一
// 账号的潘多拉状态必须保留，重抽才拿得到原来的 box_instance_id。
func TestPandoraRerollReplaysAfterAuthorization(t *testing.T) {
	freezePandoraNow(t)
	var rerolls atomic.Int32
	var rerolled atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/pandora/today":
			used := 0
			name, title := "金币雨", "小小金币雨，获得 67 金币。"
			if rerolled.Load() {
				used, name, title = 1, "头像翻转", "头像翻转 24 小时"
			}
			_, _ = io.WriteString(w, envelopeJSON(`{
				"has_box":true,"can_open":false,"opened_today":true,
				"next_open_at":"2026-11-12T16:00:00.000Z",
				"box":{"box_instance_id":"b1","event_name":"`+name+`",
					"event_title":"`+title+`","rarity":"bronze",
					"reroll_used":`+strconv.Itoa(used)+`,"reroll_limit":1,
					"reroll_deadline_at":"2026-11-12T15:00:00.000Z",
					"expire_at":"2026-11-12T16:05:46.604Z"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/pandora/reroll":
			if rerolls.Add(1) == 1 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"c":403,"code":"insufficient_scope",
					"required_scope":["write:economy"],"m":"缺少权限范围","d":null}`)
				return
			}
			rerolled.Store(true)
			_, _ = io.WriteString(w, envelopeJSON(`{"box":{
				"box_instance_id":"b1","event_name":"头像翻转",
				"event_title":"头像翻转 24 小时","rarity":"silver",
				"reroll_used":1,"reroll_limit":1,
				"reroll_deadline_at":"2026-11-12T15:00:00.000Z",
				"expire_at":"2026-11-12T16:05:46.604Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := pandoraTestApp(t, true)
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.showPandoraWindow()
	waitPandora(t, "可重抽状态", func() bool {
		st := a.pandora
		return st != nil && st.loaded && st.status.canReroll(pandoraNow())
	})
	a.rerollPandoraBox()
	waitPandora(t, "重抽缺权限", func() bool { return a.authGate.open })
	if a.pandora == nil || !a.pandora.status.BoxKnown || a.pandora.status.Box.InstanceID != "b1" {
		t.Fatalf("等待授权时丢掉了盒子编号：%+v", a.pandora)
	}

	// 模拟用户在浏览器里完成授权：重跑登记的重抽，不再重新加载页面。
	a.autoLoad = false // 不让 onAuthChanged 顺带启动后台轮询
	a.onAuthChanged(nil)
	waitPandora(t, "授权后重跑重抽", func() bool {
		return rerolls.Load() == 2
	})
	waitPandora(t, "重抽结果", func() bool {
		st := a.pandora
		return st.busy == "" && st.loaded && st.status.Box.RerollUsed >= 1 &&
			!st.status.canReroll(pandoraNow())
	})
	if !pandoraHasToast(a, "已再抽一次") {
		t.Errorf("授权重跑后没有重抽成功提示：%+v", a.toasts)
	}
}

// 窗口里要一眼看到两次抽奖与本次事件；可开启时给第一颗按钮，开过之后给
// 「再抽一次」。标题栏入口只在登录后出现。
func TestPandoraWindowShowsDrawsAndEvent(t *testing.T) {
	freezePandoraNow(t)
	a := pandoraTestApp(t, false)
	a.pandora = &pandoraState{
		loaded: true,
		status: pandoraStatus{
			Known: true, FlagsKnown: true, HasBox: true, OpenedToday: true,
			NextOpenAt: "2026-11-12T16:00:00.000Z",
			BoxKnown:   true,
			Box: pandoraBox{
				InstanceID: "b1", EventName: "金币雨",
				EventTitle: "小小金币雨，获得 67 金币。", Rarity: "bronze",
				RolledValue: 67, RolledKnown: true,
				RerollUsed: 0, RerollLimit: 1, CountersKnown: true,
				RerollDeadlineAt: "2026-11-12T15:00:00.000Z",
				ExpireAt:         "2026-11-12T16:05:46.604Z",
			},
		},
	}
	tt := ui.NewTester(a.pandoraWindowView, pandoraWindowWidth, pandoraWindowHeight)
	tt.Frame()
	for _, want := range []string{
		"今日潘多拉", "1 / 2", "第一次 · 开启盲盒", "第二次 · 再抽一次",
		"第一次抽奖结果", "金币雨", "小小金币雨，获得 67 金币。", "稀有度 青铜",
		"再抽一次（第 2 次）",
	} {
		if !tt.HasText(want) {
			t.Errorf("窗口缺少 %q：%q", want, tt.Texts())
		}
	}
	if _, ok := tt.Find("重抽潘多拉盲盒"); !ok {
		t.Errorf("第二次抽奖没有可访问按钮：%q", tt.Texts())
	}
	if _, ok := tt.Find("开启潘多拉盲盒"); ok {
		t.Error("已经开启过时不应再出现「开启」按钮")
	}

	// 还可以开启的状态：0/2、第一颗按钮，且没有重抽入口。
	a.pandora = &pandoraState{loaded: true, status: pandoraStatus{
		Known: true, FlagsKnown: true, CanOpen: true,
		NextOpenAt: "2026-11-12T16:00:00.000Z",
	}}
	tt = ui.NewTester(a.pandoraWindowView, pandoraWindowWidth, pandoraWindowHeight)
	tt.Frame()
	for _, want := range []string{"0 / 2", "开启盲盒（第 1 次）"} {
		if !tt.HasText(want) {
			t.Errorf("可开启状态缺少 %q：%q", want, tt.Texts())
		}
	}
	if _, ok := tt.Find("开启潘多拉盲盒"); !ok {
		t.Error("可开启时没有可访问的开盒按钮")
	}
	if _, ok := tt.Find("重抽潘多拉盲盒"); ok {
		t.Error("还没开启时不应出现重抽按钮")
	}
}

// 金币窗口的今日页不再写「开启请到站点操作」：那一行改成入口，点开独立
// 的潘多拉窗口。
func TestCoinWindowPandoraRowOpensWindow(t *testing.T) {
	a := pandoraTestApp(t, false)
	a.coins.baseLoaded = true
	a.coins.todayLoaded = true
	a.coins.pandoraSeen = true
	a.coins.pandora = coinPandoraStatus{
		Known: true, CanOpen: true, EventTitle: "金币雨",
	}
	tt := ui.NewTester(a.coinWindowView, coinWindowWidth, coinWindowHeight)
	tt.Frame()
	if tt.HasText("开启请到站点操作") {
		t.Errorf("金币窗口还在让用户去站点开盲盒：%q", tt.Texts())
	}
	if !tt.HasText("打开潘多拉") {
		t.Fatalf("金币窗口缺少潘多拉入口：%q", tt.Texts())
	}
	opened := 0
	a.onPandoraOpen = func() { opened++ }
	if err := tt.Click("潘多拉盲盒"); err != nil {
		t.Fatalf("点击金币窗口的潘多拉入口失败：%v", err)
	}
	if opened != 1 {
		t.Errorf("金币窗口的潘多拉入口没有打开窗口：opened=%d", opened)
	}
}

func TestPandoraTitleBarEntry(t *testing.T) {
	freezePandoraNow(t)
	a := pandoraTestApp(t, false)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if !tt.HasText("潘多拉") {
		t.Fatalf("登录后标题栏没有潘多拉入口：%q", tt.Texts())
	}
	opened := 0
	a.onPandoraOpen = func() { opened++ }
	if err := tt.Click("潘多拉"); err != nil {
		t.Fatalf("点击潘多拉入口失败：%v", err)
	}
	if opened != 1 {
		t.Errorf("点潘多拉入口没有打开窗口：opened=%d", opened)
	}
	if a.pandora == nil {
		t.Error("打开潘多拉窗口时没有准备好状态")
	}
	if a.pandoraWin != nil {
		t.Error("headless 测试里不该创建潘多拉窗口")
	}

	// 抽中事件后，标题栏直接显示事件名；悬浮说明写具体效果。
	a.pandora = &pandoraState{
		entryKnown: true,
		entryStatus: pandoraStatus{
			Known: true, FlagsKnown: true, HasBox: true, OpenedToday: true,
			BoxKnown: true,
			Box: pandoraBox{
				InstanceID: "b1", EventName: "金币雨",
				EventTitle: "小小金币雨，获得 67 金币。", Rarity: "bronze",
				RerollUsed: 0, RerollLimit: 1, CountersKnown: true,
				RerollDeadlineAt: "2026-11-12T15:00:00.000Z",
				ExpireAt:         "2026-11-12T16:05:46.604Z",
			},
		},
	}
	tt = ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if !tt.HasText("金币雨") {
		t.Fatalf("抽中事件后首页没有直接显示事件名：%q", tt.Texts())
	}
	if _, ok := tt.Find("潘多拉"); !ok {
		t.Errorf("标题栏入口丢失了可访问名：%q", tt.Texts())
	}
	if name := pandoraEntryName(a.pandora); name != "金币雨" {
		t.Errorf("入口显示名 = %q，期望事件名 金币雨", name)
	}
	if hint := pandoraEntryHint(a.pandora); !strings.Contains(hint, "金币雨") ||
		!strings.Contains(hint, "小小金币雨，获得 67 金币。") {
		t.Errorf("悬浮说明没有具体效果：%q", hint)
	}
	// 最小窗口宽度下，事件名不能挤到金币数上。
	tt = ui.NewTester(a.view, 760, 600)
	tt.Frame()
	eventR, ok := tt.Find("金币雨")
	if !ok {
		t.Fatalf("窄窗口下标题栏没有事件名：%q", tt.Texts())
	}
	coinR, ok := tt.Find("金币余额")
	if !ok {
		t.Fatalf("窄窗口下找不到金币数：%q", tt.Texts())
	}
	if eventR.X+eventR.W > coinR.X {
		t.Errorf("事件名挤到了金币数：事件 %v，金币 %v", eventR, coinR)
	}

	// 事件过期后不再顶替入口名，退回「潘多拉」。
	a.pandora.entryStatus.Box.ExpireAt = "2026-11-12T11:00:00.000Z"
	if name := pandoraEntryName(a.pandora); name != "潘多拉" {
		t.Errorf("事件过期后入口显示 %q，期望 潘多拉", name)
	}

	// 未登录时不画入口。
	b := testApp(t)
	tt = ui.NewTester(b.view, 1080, 720)
	tt.Frame()
	if tt.HasText("潘多拉") {
		t.Errorf("未登录时不该出现潘多拉入口：%q", tt.Texts())
	}
}

// 悬浮首页的潘多拉入口时才展示具体效果：静止时只显示事件名，指针停留后
// 弹出「事件名 + 效果 + 剩余时间」。
func TestPandoraEntryTooltipShowsEffect(t *testing.T) {
	freezePandoraNow(t)
	a := pandoraTestApp(t, false)
	a.pandora = &pandoraState{
		entryKnown: true,
		entryStatus: pandoraStatus{
			Known: true, FlagsKnown: true, HasBox: true, OpenedToday: true,
			BoxKnown: true,
			Box: pandoraBox{
				InstanceID: "b1", EventName: "金币雨",
				EventTitle: "小小金币雨，获得 67 金币。", Rarity: "bronze",
				RerollUsed: 0, RerollLimit: 1, CountersKnown: true,
				RerollDeadlineAt: "2026-11-12T15:00:00.000Z",
				ExpireAt:         "2026-11-12T16:05:46.604Z",
			},
		},
	}
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	r, ok := tt.Find("潘多拉")
	if !ok {
		t.Fatalf("找不到潘多拉入口：%q", tt.Texts())
	}
	if tt.HasText("小小金币雨，获得 67 金币。") {
		t.Fatalf("没有悬浮就露出了具体效果：%q", tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(700 * time.Millisecond) // 默认悬浮说明要停留 0.6 秒
	if !tt.HasText("小小金币雨，获得 67 金币。") {
		t.Fatalf("悬浮后没有显示具体效果：%q", tt.Texts())
	}
	// 指针离开后提示自己收起，首页回到只显示事件名。
	tt.Move(10, 400)
	tt.Advance(time.Second)
	if tt.HasText("小小金币雨，获得 67 金币。") {
		t.Errorf("指针离开后悬浮说明还在：%q", tt.Texts())
	}
	if !tt.HasText("金币雨") {
		t.Errorf("指针离开后事件名丢了：%q", tt.Texts())
	}
}

// 事件名过长时截断：标题栏里的金币数与账号标识不能被挤出窗口。
func TestPandoraEntryNameTruncates(t *testing.T) {
	a := pandoraTestApp(t, false)
	a.pandora = &pandoraState{
		entryKnown: true,
		entryStatus: pandoraStatus{
			Known: true, FlagsKnown: true, HasBox: true, OpenedToday: true,
			BoxKnown: true,
			Box: pandoraBox{
				InstanceID: "b1", EventName: "特别长的潘多拉事件名字",
				EventTitle: "具体效果",
				ExpireAt:   "2026-11-13T16:05:46.604Z",
			},
		},
	}
	tt := ui.NewTester(a.view, 760, 600)
	tt.Frame()
	nameR, ok := tt.Find("潘多拉")
	if !ok {
		t.Fatalf("找不到潘多拉入口：%q", tt.Texts())
	}
	coinR, ok := tt.Find("金币余额")
	if !ok {
		t.Fatalf("找不到金币数：%q", tt.Texts())
	}
	if nameR.X+nameR.W > coinR.X {
		t.Errorf("长事件名挤到了金币数：入口 %v，金币 %v", nameR, coinR)
	}
	if !strings.Contains(pandoraEntryName(a.pandora), "…") {
		t.Errorf("长事件名没有截断：%q", pandoraEntryName(a.pandora))
	}
}

// 登录 / 启动时静默读一次今日状态：首页标题栏不用先打开潘多拉窗口就能
// 显示抽中的事件名；失败不弹授权。
func TestPandoraEntryLoadsOnLogin(t *testing.T) {
	freezePandoraNow(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/pandora/today" {
			_, _ = io.WriteString(w, envelopeJSON(`{
				"has_box":true,"can_open":false,"opened_today":true,
				"next_open_at":"2026-11-12T16:00:00.000Z",
				"box":{"box_instance_id":"b1","event_name":"金币雨",
					"event_title":"小小金币雨，获得 67 金币。","rarity":"bronze",
					"rolled_value":67,"reroll_used":0,"reroll_limit":1,
					"reroll_deadline_at":"2026-11-12T15:00:00.000Z",
					"expire_at":"2026-11-12T16:05:46.604Z"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	a := pandoraTestApp(t, true)
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.loadPandoraEntry()
	waitPandora(t, "首页入口的今日状态", func() bool {
		return a.pandora != nil && a.pandora.entryKnown
	})
	if a.pandora.loaded {
		t.Error("静默读取不应该把潘多拉窗口标成已加载")
	}
	if a.authGate.open {
		t.Error("静默读取失败 / 成功都不该弹授权")
	}
	a.autoLoad = false
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if !tt.HasText("金币雨") {
		t.Fatalf("首页没有直接显示潘多拉事件名：%q", tt.Texts())
	}
	if hint := pandoraEntryHint(a.pandora); !strings.Contains(hint, "小小金币雨，获得 67 金币。") {
		t.Errorf("首页悬浮说明没有具体效果：%q", hint)
	}
}

func TestPandoraWindowMinimumSize(t *testing.T) {
	freezePandoraNow(t)
	a := pandoraTestApp(t, false)
	a.pandora = &pandoraState{loaded: true, status: pandoraStatus{
		Known: true, FlagsKnown: true, CanOpen: true,
	}}
	tt := ui.NewTester(a.pandoraWindowView, pandoraWinMinWidth, pandoraWinMinHeight)
	tt.Frame()
	for _, want := range []string{"开启潘多拉盲盒", "刷新", "最小化", "关闭"} {
		r, ok := tt.Find(want)
		if !ok {
			t.Errorf("最小窗口下缺少 %q：%q", want, tt.Texts())
			continue
		}
		if r.X < 0 || r.Y < 0 || r.X+r.W > pandoraWinMinWidth+1 || r.Y+r.H > pandoraWinMinHeight+1 {
			t.Errorf("最小窗口下 %q 超出边界：%v", want, r)
		}
	}

	// 第二次抽奖的状态：事件卡片与重抽按钮在最小窗口里也要够得着。
	a.pandora = &pandoraState{loaded: true, status: pandoraStatus{
		Known: true, FlagsKnown: true, HasBox: true, OpenedToday: true,
		BoxKnown: true,
		Box: pandoraBox{
			InstanceID: "b1", EventName: "金币雨", EventTitle: "小小金币雨，获得 67 金币。",
			Rarity: "bronze", RolledValue: 67, RolledKnown: true,
			RerollUsed: 0, RerollLimit: 1, CountersKnown: true,
			RerollDeadlineAt: "2026-11-12T15:00:00.000Z",
			ExpireAt:         "2026-11-12T16:05:46.604Z",
		},
	}}
	tt = ui.NewTester(a.pandoraWindowView, pandoraWinMinWidth, pandoraWinMinHeight)
	tt.Frame()
	for _, want := range []string{"重抽潘多拉盲盒", "再抽一次（第 2 次）"} {
		r, ok := tt.Find(want)
		if !ok {
			t.Errorf("最小窗口下缺少 %q：%q", want, tt.Texts())
			continue
		}
		if r.X < 0 || r.Y < 0 || r.X+r.W > pandoraWinMinWidth+1 || r.Y+r.H > pandoraWinMinHeight+1 {
			t.Errorf("最小窗口下 %q 超出边界：%v", want, r)
		}
	}
}

func pandoraHasToast(a *app, part string) bool {
	for _, n := range a.toasts {
		if strings.Contains(n.text, part) {
			return true
		}
	}
	return false
}
