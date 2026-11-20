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

// 动作响应里的 cost 仍按容错方式解码，界面不再展示金额；这里守住缺字段
// 不报错、有字段能读出来。
func TestDecodePandoraActionCost(t *testing.T) {
	res := decodePandoraAction(json.RawMessage(`{"cost":12,"box":{"box_instance_id":"b1"}}`))
	if !res.CostKnown || res.Cost != 12 {
		t.Errorf("cost = %v（known=%v），期望 12", res.Cost, res.CostKnown)
	}
	if !res.Status.BoxKnown || res.Status.Box.InstanceID != "b1" {
		t.Errorf("box 解码不对：%+v", res.Status)
	}
	bare := decodePandoraAction(json.RawMessage(`{"box":{"box_instance_id":"b1"}}`))
	if bare.CostKnown {
		t.Errorf("没有价格字段却标成已知：%+v", bare)
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
// POST /v1/pandora/reroll（box_instance_id），都带令牌与 Idempotency-Key；
// 点第二次重抽时不再发请求。
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

	a.showPandora()
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
	// 结果只在卡片里展示一次，不再发结果 toast。
	if pandoraHasToast(a, "盲盒已开启") || pandoraHasToast(a, "已再抽一次") {
		t.Errorf("抽卡结果不该重复成 toast：%+v", a.toasts)
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
	// 第二次结果同样只在卡片里出一次，没有 toast。
	if pandoraHasToast(a, "已再抽一次") || pandoraHasToast(a, "盲盒已开启") {
		t.Errorf("抽卡结果不该重复成 toast：%+v", a.toasts)
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
	a.showPandora()
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
	a.showPandora()
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
	if pandoraHasToast(a, "已再抽一次") || pandoraHasToast(a, "盲盒已开启") {
		t.Errorf("抽卡结果不该重复成 toast：%+v", a.toasts)
	}
}

func pandoraCardTester(a *app, w, h int) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		a.applyTheme(c)
		a.pandoraCard(c, a.pandora)
	}, w, h)
}

// 卡片一次只展示一张结果：抽到之后给事件与稀有度，旁边一枚重置图标；不写
// 进度格、倒计时、规则文案。没抽时只有卡背和抽取按钮。
func TestPandoraCardShowsOneResultAtATime(t *testing.T) {
	freezePandoraNow(t)
	a := pandoraTestApp(t, false)
	a.pandora = &pandoraState{
		loaded: true,
		status: pandoraStatus{
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
	tt := pandoraCardTester(a, 420, 320)
	tt.Frame()
	for _, want := range []string{"金币雨", "小小金币雨，获得 67 金币。", "青铜"} {
		if !tt.HasText(want) {
			t.Errorf("卡片缺少 %q：%q", want, tt.Texts())
		}
	}
	if _, ok := tt.Find("重抽一次"); !ok {
		t.Errorf("第一次抽完没有重置图标：%q", tt.Texts())
	}
	for _, gone := range []string{
		"今日潘多拉", "1 / 2", "第一次 · 开启盲盒", "关于潘多拉", "延期事件",
	} {
		if tt.HasText(gone) {
			t.Errorf("卡片里还有多余文案 %q：%q", gone, tt.Texts())
		}
	}

	// 抽完第二次：同一张卡片换成新结果，重置图标消失，不出现上一张。
	a.pandora.status.Box.RerollUsed = 1
	a.pandora.status.Box.EventName = "头像翻转"
	a.pandora.status.Box.EventTitle = "头像翻转 24 小时"
	tt = pandoraCardTester(a, 420, 320)
	tt.Frame()
	if !tt.HasText("头像翻转") || tt.HasText("金币雨") {
		t.Errorf("第二次结果没有只展示当前一张：%q", tt.Texts())
	}
	if _, ok := tt.Find("重抽一次"); ok {
		t.Error("两次都抽完后不该还有重置图标")
	}

	// 还没抽：卡背 + 抽取按钮，没有重置图标。
	a.pandora = &pandoraState{loaded: true, status: pandoraStatus{
		Known: true, FlagsKnown: true, CanOpen: true,
	}}
	tt = pandoraCardTester(a, 420, 320)
	tt.Frame()
	if !tt.HasText("抽取") {
		t.Errorf("可开启时没有抽取按钮：%q", tt.Texts())
	}
	if _, ok := tt.Find("抽取潘多拉盲盒"); !ok {
		t.Errorf("抽取按钮没有可访问名：%q", tt.Texts())
	}
	if _, ok := tt.Find("重抽一次"); ok {
		t.Error("还没抽时不该出现重置图标")
	}
}

// 金币窗口不再放潘多拉入口：它和签到一样收在标题栏「我的」的账号菜单里。
// 即使金币页读到了盲盒状态，窗口里也不该出现前往抽卡的入口。
func TestCoinWindowHasNoPandoraEntry(t *testing.T) {
	a := pandoraTestApp(t, false)
	a.coins.baseLoaded = true
	a.coins.todayLoaded = true
	a.coins.pandoraSeen = true
	a.coins.pandora = coinPandoraStatus{
		Known: true, CanOpen: true, EventTitle: "金币雨",
	}
	tt := ui.NewTester(a.coinWindowView, coinWindowWidth, coinWindowHeight)
	tt.Frame()
	for _, forbidden := range []string{"打开潘多拉", "潘多拉盲盒", "开启请到站点操作"} {
		if _, ok := tt.Find(forbidden); ok {
			t.Errorf("金币窗口不该出现潘多拉入口 %q：%q", forbidden, tt.Texts())
		}
	}
	if tt.HasText("金币雨") {
		t.Errorf("金币窗口不该展示盲盒事件：%q", tt.Texts())
	}
}

func TestPandoraTitleBarEntry(t *testing.T) {
	freezePandoraNow(t)
	a := pandoraTestApp(t, false)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if _, ok := tt.Find("我的"); !ok {
		t.Fatalf("登录后标题栏没有账号菜单：%q", tt.Texts())
	}
	opened := 0
	a.onPandoraOpen = func() { opened++ }
	openAccountMenu(t, tt)
	if !tt.HasText("潘多拉") {
		t.Fatalf("账号菜单里没有潘多拉入口：%q", tt.Texts())
	}
	if err := tt.Click("潘多拉"); err != nil {
		t.Fatalf("点击潘多拉入口失败：%v", err)
	}
	if opened != 1 {
		t.Errorf("点潘多拉入口没有打开窗口：opened=%d", opened)
	}
	if a.pandora == nil || !a.pandora.open {
		t.Error("点潘多拉入口没有打开抽卡弹窗")
	}

	// 抽中事件后，账号菜单里直接显示事件名；悬浮说明写具体效果。
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
	openAccountMenu(t, tt)
	if !tt.HasText("金币雨") {
		t.Fatalf("抽中事件后账号菜单没有直接显示事件名：%q", tt.Texts())
	}
	if name := pandoraEntryName(a.pandora); name != "金币雨" {
		t.Errorf("入口显示名 = %q，期望事件名 金币雨", name)
	}
	if hint := pandoraEntryHint(a.pandora); !strings.Contains(hint, "金币雨") ||
		!strings.Contains(hint, "小小金币雨，获得 67 金币。") {
		t.Errorf("悬浮说明没有具体效果：%q", hint)
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
	openAccountMenu(t, tt)
	r, ok := tt.Find("金币雨")
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
	// 指针离开后提示自己收起，气泡里回到只显示事件名。
	tt.Move(10, 400)
	tt.Advance(time.Second)
	if tt.HasText("小小金币雨，获得 67 金币。") {
		t.Errorf("指针离开后悬浮说明还在：%q", tt.Texts())
	}
	if !tt.HasText("金币雨") {
		t.Errorf("指针离开后事件名丢了：%q", tt.Texts())
	}
}

// 事件名过长时截断：账号菜单里的其它入口不会被长名字挤走。
func TestPandoraEntryNameTruncates(t *testing.T) {
	// 事件名里的 expire_at 是固定日期，不冻结「现在」的话过一天这个用例
	// 就会因为事件过期而看不到事件名（返回的只是默认「潘多拉」）。
	freezePandoraNow(t)
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
	openAccountMenu(t, tt)
	name := pandoraEntryName(a.pandora)
	if !tt.HasText(name) {
		t.Fatalf("气泡里没有潘多拉事件名：%q", tt.Texts())
	}
	if !strings.Contains(name, "…") {
		t.Errorf("长事件名没有截断：%q", name)
	}
}

// 登录 / 启动时静默读一次今日状态：账号菜单不用先打开潘多拉弹窗就能
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
		t.Error("静默读取不应该把潘多拉弹窗标成已加载")
	}
	if a.authGate.open {
		t.Error("静默读取失败 / 成功都不该弹授权")
	}
	a.autoLoad = false
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	openAccountMenu(t, tt)
	if !tt.HasText("金币雨") {
		t.Fatalf("账号菜单没有直接显示潘多拉事件名：%q", tt.Texts())
	}
	if hint := pandoraEntryHint(a.pandora); !strings.Contains(hint, "小小金币雨，获得 67 金币。") {
		t.Errorf("账号菜单悬浮说明没有具体效果：%q", hint)
	}
}

func TestPandoraCardFitsNarrowModal(t *testing.T) {
	freezePandoraNow(t)
	a := pandoraTestApp(t, false)
	a.pandora = &pandoraState{loaded: true, status: pandoraStatus{
		Known: true, FlagsKnown: true, CanOpen: true,
	}}
	tt := pandoraCardTester(a, 360, 300)
	tt.Frame()
	r, ok := tt.Find("抽取潘多拉盲盒")
	if !ok {
		t.Fatalf("窄弹窗里缺少抽取按钮：%q", tt.Texts())
	}
	if r.X < 0 || r.Y < 0 || r.X+r.W > 361 || r.Y+r.H > 301 {
		t.Errorf("窄弹窗下抽取按钮超出边界：%v", r)
	}

	// 抽到之后：重置图标在窄弹窗里也要够得着。
	a.pandora = &pandoraState{loaded: true, status: pandoraStatus{
		Known: true, FlagsKnown: true, HasBox: true, OpenedToday: true,
		BoxKnown: true,
		Box: pandoraBox{
			InstanceID: "b1", EventName: "金币雨",
			RerollUsed: 0, RerollLimit: 1, CountersKnown: true,
			RerollDeadlineAt: "2026-11-12T15:00:00.000Z",
			ExpireAt:         "2026-11-12T16:05:46.604Z",
		},
	}}
	tt = pandoraCardTester(a, 360, 300)
	tt.Frame()
	r, ok = tt.Find("重抽一次")
	if !ok {
		t.Fatalf("窄弹窗里缺少重置图标：%q", tt.Texts())
	}
	if r.X < 0 || r.Y < 0 || r.X+r.W > 361 || r.Y+r.H > 301 {
		t.Errorf("窄弹窗下重置图标超出边界：%v", r)
	}
}

// 弹窗由主窗口的 dialogs 画出来：开合状态变了就多一层 Modal，关掉就消失。
func TestPandoraDialogRendersInMainWindow(t *testing.T) {
	freezePandoraNow(t)
	a := pandoraTestApp(t, false)
	a.pandora.open = true
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if _, ok := tt.Find("抽取潘多拉盲盒"); !ok && !tt.HasText("今天没有可抽的盲盒") {
		t.Fatalf("打开弹窗后没看到卡片：%q", tt.Texts())
	}

	a.pandora.open = false
	tt = ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if tt.HasText("抽取潘多拉盲盒") {
		t.Errorf("关掉弹窗后卡片还在：%q", tt.Texts())
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
