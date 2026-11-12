package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func waitCoin(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等待超时：%s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func coinTestClient(t *testing.T, srv *httptest.Server) *app {
	t.Helper()
	a := testApp(t)
	a.client.Base = srv.URL + "/api"
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Scope: "read:economy"}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestDecodeCoinTransactionPage(t *testing.T) {
	d := json.RawMessage(`{
		"items": [
			{"id":"c1","reason":"checkin","amount":5,"created_at":"2026-11-12T02:00:00Z",
			 "check_data":{"outcome":"success","roll":12}},
			{"id":"c2","reason":"badge_equip","amount":-20,"base_amount":-20,
			 "discount_info":{"discounted_by":"badge","base_amount":-30,"discount_num":0.67},
			 "relate_path":"/post/work/ABCD1234","relate_title":"一个帖子",
			 "relate_comment":"整段评论","from_user":{"username":"jimmy"},"is_alias":true,
			 "created_at":"2026-11-11T02:00:00Z"}
		],
		"total": 42, "page": 2, "limit": 20, "total_pages": 3
	}`)
	page := decodeCoinTransactionPage(d, 1)
	if page.Total != 42 || page.Page != 2 || page.Limit != 20 || page.TotalPages != 3 {
		t.Fatalf("分页字段不对：%+v", page)
	}
	if len(page.Items) != 2 {
		t.Fatalf("流水条数不对：%+v", page.Items)
	}
	if page.Items[0].Reason != "checkin" || page.Items[0].Amount != 5 || len(page.Items[0].CheckData) == 0 {
		t.Errorf("签到流水解析不对：%+v", page.Items[0])
	}
	tx := page.Items[1]
	if tx.Amount != -20 || tx.BaseAmount != -30 || !tx.HasDiscount {
		t.Errorf("折扣字段解析不对：%+v", tx)
	}
	if tx.RelateTitle != "一个帖子" || tx.RelateComment != "整段评论" || tx.FromUser != "jimmy" || !tx.IsAlias {
		t.Errorf("关联字段解析不对：%+v", tx)
	}
	income, expense, net := coinTotals(page.Items)
	if income != 5 || expense != 20 || net != -15 {
		t.Errorf("今日合计不对：income=%v expense=%v net=%v", income, expense, net)
	}
}

func TestDecodeCoinSignAndRanking(t *testing.T) {
	sign := decodeCoinSign(json.RawMessage(`{"signed":true,"streak":6,"coins":12}`))
	if !sign.Known || !sign.Signed || sign.Streak != 6 || sign.Coins != 12 {
		t.Errorf("签到状态解析不对：%+v", sign)
	}
	old := coinNow
	coinNow = func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) }
	defer func() { coinNow = old }()
	sign = decodeCoinSign(json.RawMessage(`{"sign_date":"2026-11-12"}`))
	if !sign.Known || !sign.Signed {
		t.Errorf("只有 sign_date 时也应识别为今天已签到：%+v", sign)
	}
	// coins 也可能是余额：只有明确写了奖励字段才允许当成「获得」展示。
	reward := decodeCoinSign(json.RawMessage(`{"signed":true,"streak":6,"reward_coins":5}`))
	if !reward.RewardKnown || reward.Coins != 5 {
		t.Errorf("明确的奖励字段没有识别：%+v", reward)
	}
	ambiguous := decodeCoinSign(json.RawMessage(`{"signed":true,"coins":1234}`))
	if ambiguous.RewardKnown {
		t.Errorf("coins 不应被当成明确的签到奖励：%+v", ambiguous)
	}

	ranking := decodeCoinRanking(json.RawMessage(`[
		{"username":"bopomofo","balance":1139778,"rank":1,"is_pro":true},
		{"username":"jimmy","balance":88,"rank":2}
	]`))
	if len(ranking) != 2 || ranking[0].Rank != 1 || ranking[0].Balance != 1139778 || !ranking[0].IsPro {
		t.Errorf("排行榜解析不对：%+v", ranking)
	}
	if ranking[1].Username != "jimmy" || ranking[1].Balance != 88 {
		t.Errorf("排行榜第二项不对：%+v", ranking[1])
	}
}

func TestCoinRangeDatesUseShanghaiDays(t *testing.T) {
	old := coinNow
	coinNow = func() time.Time {
		return time.Date(2026, 11, 12, 23, 30, 0, 0, time.FixedZone("UTC+8", 8*3600))
	}
	defer func() { coinNow = old }()

	if s, e := coinRangeDates(coinRangeToday); s != "2026-11-12" || e != "2026-11-12" {
		t.Errorf("今天范围不对：%s ~ %s", s, e)
	}
	if s, e := coinRangeDates(coinRange7Days); s != "2026-11-06" || e != "2026-11-12" {
		t.Errorf("近 7 天范围不对：%s ~ %s", s, e)
	}
	if s, e := coinRangeDates(coinRange30Days); s != "2026-10-14" || e != "2026-11-12" {
		t.Errorf("近 30 天范围不对：%s ~ %s", s, e)
	}
	if coinMonthStart() != "2026-11-01" {
		t.Errorf("本月起点不对：%s", coinMonthStart())
	}
}

func TestCoinClientReadsAllEndpoints(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.URL.RawQuery
		mu.Unlock()
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("%s 缺少令牌：%q", r.URL.Path, got)
		}
		switch r.URL.Path {
		case "/api/v1/me/coins/balance":
			_, _ = io.WriteString(w, envelopeJSON(`{"coins":1234}`))
		case "/api/v1/me/coins/balance-with-ranking":
			_, _ = io.WriteString(w, envelopeJSON(`{"coins":1234,"rank":7}`))
		case "/api/v1/me/coins/transactions":
			if r.URL.Query().Get("page") != "1" || r.URL.Query().Get("start_date") != "2026-11-01" {
				t.Errorf("流水参数不对：%v", r.URL.Query())
			}
			_, _ = io.WriteString(w, envelopeJSON(`{"items":[{"id":"x","reason":"post","amount":10}],"total":1,"page":1,"limit":20,"total_pages":1}`))
		case "/api/v1/me/coins/today-transaction":
			_, _ = io.WriteString(w, envelopeJSON(`[{"id":"t","reason":"checkin","amount":5}]`))
		case "/api/v1/me/coins/daily-flow":
			_, _ = io.WriteString(w, envelopeJSON(`[{"date":"2026-11-12","income":5,"expense":2,"net":3}]`))
		case "/api/v1/me/coins/expense-breakdown":
			if r.URL.Query().Get("flow_type") != "income" {
				t.Errorf("分类 flow_type 不对：%v", r.URL.Query())
			}
			_, _ = io.WriteString(w, envelopeJSON(`[{"reason":"checkin","amount":5,"ratio":0.5}]`))
		case "/api/v1/me/sign":
			_, _ = io.WriteString(w, envelopeJSON(`{"signed":true,"streak":3}`))
		case "/api/v1/coins/ranking":
			_, _ = io.WriteString(w, envelopeJSON(`[{"username":"jimmy","balance":99,"rank":2}]`))
		case "/api/v1/pandora/today":
			_, _ = io.WriteString(w, envelopeJSON(`{"has_box":true,"can_open":true,"box":{"event_title":"翻转"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := coinTestClient(t, srv)
	ctx := t.Context()

	if coins, err := a.client.fetchCoinBalance(ctx); err != nil || coins != 1234 {
		t.Errorf("余额 = %d, %v", coins, err)
	}
	if coins, rank, err := a.client.fetchCoinBalanceRanking(ctx); err != nil || coins != 1234 || rank != 7 {
		t.Errorf("余额+排名 = %d/%d, %v", coins, rank, err)
	}
	page, err := a.client.fetchCoinTransactions(ctx, 1, "2026-11-01", "")
	if err != nil || len(page.Items) != 1 || page.Items[0].Amount != 10 {
		t.Errorf("流水 = %+v, %v", page, err)
	}
	today, err := a.client.fetchCoinTodayTransactions(ctx)
	if err != nil || len(today) != 1 || today[0].Amount != 5 {
		t.Errorf("今日明细 = %+v, %v", today, err)
	}
	daily, err := a.client.fetchCoinDailyFlow(ctx, "2026-11-01", "2026-11-12")
	if err != nil || len(daily) != 1 || daily[0].Net != 3 {
		t.Errorf("按日聚合 = %+v, %v", daily, err)
	}
	breakdown, err := a.client.fetchCoinBreakdown(ctx, "2026-11-01", "2026-11-12", "income")
	if err != nil || len(breakdown) != 1 || breakdown[0].Amount != 5 {
		t.Errorf("分类 = %+v, %v", breakdown, err)
	}
	sign, err := a.client.fetchCoinSign(ctx)
	if err != nil || !sign.Signed || sign.Streak != 3 {
		t.Errorf("签到 = %+v, %v", sign, err)
	}
	ranking, err := a.client.fetchCoinRanking(ctx)
	if err != nil || len(ranking) != 1 || ranking[0].Balance != 99 {
		t.Errorf("排行 = %+v, %v", ranking, err)
	}
	pandora, err := a.client.fetchCoinPandora(ctx)
	if err != nil || !pandora.Known || !pandora.CanOpen || pandora.EventTitle != "翻转" {
		t.Errorf("盲盒 = %+v, %v", pandora, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 9 {
		t.Errorf("应请求 9 个只读端点，实际 %d：%v", len(seen), seen)
	}
}

func TestCoinHistoryRangeSendsDateFilter(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me/coins/transactions" {
			http.NotFound(w, r)
			return
		}
		got = r.URL.RawQuery
		_, _ = io.WriteString(w, envelopeJSON(`{"items":[{"id":"h1","reason":"post","amount":10,"relate_title":"帖子"}],"total":1,"page":1,"limit":20,"total_pages":1}`))
	}))
	defer srv.Close()
	a := coinTestClient(t, srv)
	old := coinNow
	coinNow = func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) }
	defer func() { coinNow = old }()
	a.coins.historyRange = coinRange7Days
	a.loadCoinHistory(1)
	waitCoin(t, "流水加载", func() bool { return a.coins.historyLoaded })
	if !strings.Contains(got, "start_date=2026-11-06") || !strings.Contains(got, "end_date=2026-11-12") {
		t.Errorf("历史筛选没有带上日期：%s", got)
	}
	if len(a.coins.historyItems) != 1 || a.coins.historyItems[0].RelateTitle != "帖子" {
		t.Errorf("历史流水没有解析：%+v", a.coins.historyItems)
	}
}

func TestCoinTodayFallsBackToTransactions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/me/coins/today-transaction":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"c":403,"code":"insufficient_scope","required_scope":["read:economy"],"m":"未开通","d":null}`)
		case "/api/v1/me/coins/transactions":
			_, _ = io.WriteString(w, envelopeJSON(`{"items":[{"id":"t1","reason":"checkin","amount":8}],"total":1,"page":1,"limit":20,"total_pages":1}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := coinTestClient(t, srv)
	a.loadCoinToday()
	waitCoin(t, "今日明细回退", func() bool { return a.coins.todayLoaded })
	if a.coins.todayErr != "" {
		t.Fatalf("回退后不应有错误：%s", a.coins.todayErr)
	}
	if a.coins.todayFrom != "transactions" || len(a.coins.todayItems) != 1 {
		t.Errorf("没有回退到流水：%+v from=%s", a.coins.todayItems, a.coins.todayFrom)
	}
	if !strings.Contains(a.coins.todayNote, "未开通") {
		t.Errorf("缺少回退说明：%q", a.coins.todayNote)
	}
	if a.coins.todayIncome != 8 || a.coins.todayNet != 8 {
		t.Errorf("今日合计不对：%+v", a.coins)
	}
}

func TestCoinCoreScopeErrorOpensAuthGate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/me/coins/balance":
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"c":403,"code":"insufficient_scope","required_scope":["read:economy"],"m":"缺少权限范围","d":null}`)
		case "/api/v1/me/sign":
			_, _ = io.WriteString(w, envelopeJSON(`{"signed":false}`))
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"c":403,"code":"insufficient_scope","required_scope":["read:economy"],"m":"未开通","d":null}`)
		}
	}))
	defer srv.Close()
	a := coinTestClient(t, srv)
	a.loadCoinBase()
	waitCoin(t, "余额授权失败", func() bool { return a.coins.baseLoaded })
	if !a.authGate.open {
		t.Fatal("缺权限时应打开全局授权弹窗")
	}
	if len(a.authGate.need.Scopes) != 1 || a.authGate.need.Scopes[0] != "read:economy" {
		t.Errorf("授权弹窗缺少 read:economy：%+v", a.authGate.need)
	}
	if !a.coins.baseWait {
		t.Error("缺权限时应标记等待授权，避免每帧重发")
	}
	if a.coins.balanceErr != "" {
		t.Errorf("缺权限不应再显示普通错误：%q", a.coins.balanceErr)
	}
}

func TestCoinWindowRendersBalanceTodayHistoryStatsRanking(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	// 金币页是独立的金币窗口，既不在「我的」窗口里，也不占侧边栏。
	cs := a.coins
	cs.baseLoaded, cs.baseLoading = true, false
	cs.balance, cs.balanceOK, cs.rank, cs.rankOK = 1234, true, 3, true
	cs.sign = coinSignStatus{Known: true, Signed: true, Streak: 5}
	cs.todayLoaded = true
	cs.todayItems = []coinTransaction{
		{ID: "t1", Reason: "checkin", Amount: 5, CreatedAt: "2026-11-12T02:00:00Z"},
	}
	cs.todayIncome, cs.todayExpense, cs.todayNet = 5, 0, 5
	cs.historyLoaded = true
	cs.historyTotal, cs.historyTotalPages, cs.historyPage = 1, 1, 1
	cs.historyItems = []coinTransaction{
		{ID: "h1", Reason: "post", Amount: -2, BaseAmount: -4, HasDiscount: true,
			CreatedAt: "2026-11-11T02:00:00Z", RelatePath: "/post/work/ABCD1234", RelateTitle: "一个帖子"},
	}
	cs.dailyLoaded = true
	cs.dailyItems = []coinDailyFlow{{Date: "2026-11-12", Income: 5, Expense: 2, Net: 3}}
	cs.breakdownLoaded = true
	cs.breakdownItems = []coinBreakdown{{Reason: "checkin", Amount: 5, Ratio: 0.5}}
	cs.rankingLoaded = true
	cs.rankingItems = []coinRankEntry{
		{Rank: 1, Username: "bopomofo", Balance: 1139778, IsPro: true},
		{Rank: 2, Username: "jimmy", Balance: 99},
	}

	tt := ui.NewTester(a.coinWindowView, coinWindowWidth, coinWindowHeight)
	for _, want := range []string{"金币", "1,234 金币", "今日获得", "+5 金币", "今日已签到 · 连续 5 天", "签到", "今日明细"} {
		if !tt.HasText(want) {
			t.Fatalf("金币首页缺少 %q：%q", want, tt.Texts())
		}
	}

	if err := tt.Click("历史"); err != nil {
		t.Fatalf("切换到历史失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	for _, want := range []string{"金币流水", "一个帖子", "原价 -4", "共 1 条记录"} {
		if !tt.HasText(want) {
			t.Fatalf("历史页缺少 %q：%q", want, tt.Texts())
		}
	}

	if err := tt.Click("统计"); err != nil {
		t.Fatalf("切换到统计失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	for _, want := range []string{"近 30 天收支", "2026-11-12", "收入构成", "签到", "50.0%"} {
		if !tt.HasText(want) {
			t.Fatalf("统计页缺少 %q：%q", want, tt.Texts())
		}
	}

	if err := tt.Click("排行"); err != nil {
		t.Fatalf("切换到排行失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	for _, want := range []string{"金币排行榜", "bopomofo", "1,139,778 金币", "（我）"} {
		if !tt.HasText(want) {
			t.Fatalf("排行页缺少 %q：%q", want, tt.Texts())
		}
	}
}

func TestCoinBreakdownToggleReadsExpenseOnly(t *testing.T) {
	var mu sync.Mutex
	flowTypes := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me/coins/expense-breakdown" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","required_scope":["read:economy"],"m":"未开通","d":null}`)
			return
		}
		mu.Lock()
		flowTypes = append(flowTypes, r.URL.Query().Get("flow_type"))
		mu.Unlock()
		_, _ = io.WriteString(w, envelopeJSON(`[{"reason":"checkin","amount":5,"ratio":0.5}]`))
	}))
	defer srv.Close()
	a := coinTestClient(t, srv)
	a.setCoinBreakdownType("expense")
	waitCoin(t, "支出分类加载", func() bool { return a.coins.breakdownLoaded })
	mu.Lock()
	defer mu.Unlock()
	if len(flowTypes) != 1 || flowTypes[0] != "expense" {
		t.Errorf("分类请求类型不对：%v", flowTypes)
	}
	if len(a.coins.breakdownItems) != 1 {
		t.Errorf("支出分类没有解析：%+v", a.coins.breakdownItems)
	}
}

// 金币页唯一会改变金币的入口是签到。按钮只在今天还没签到时出现；开盲盒、
// 打赏等其它写操作仍然只引导用户去站点。
func TestCoinPageOnlySignsAndNeverOtherWrites(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.coins.baseLoaded, a.coins.todayLoaded, a.coins.sign = true, true, coinSignStatus{Known: true, Signed: false}
	tt := ui.NewTester(a.coinWindowView, coinWindowWidth, coinWindowHeight)
	tt.Frame()
	if _, ok := tt.Find("立即签到"); !ok {
		t.Fatalf("未签到时缺少签到按钮：%q", tt.Texts())
	}
	for _, forbidden := range []string{"签到领金币", "开盲盒", "打赏"} {
		if tt.HasText(forbidden) {
			t.Errorf("金币页不该出现写操作入口 %q：%q", forbidden, tt.Texts())
		}
	}
	// 金币窗口自带标题栏与窗口按钮，和通知 / 设置窗口一致。
	for _, label := range []string{"最小化", "关闭"} {
		if _, ok := tt.Find(label); !ok {
			t.Errorf("金币窗口缺少「%s」按钮：%q", label, tt.Texts())
		}
	}
}

// 签到是金币页唯一实现的写操作：POST 不带请求体、不带幂等键；成功后按响应
// 点亮签到状态，并重新拉余额与今日明细（签到同时改变金币与流水）。
func TestCoinSignInPostsAndRefreshes(t *testing.T) {
	var mu sync.Mutex
	posts, balances, todays := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("%s 缺少令牌：%q", r.URL.Path, got)
		}
		switch {
		case r.URL.Path == "/api/v1/me/sign" && r.Method == http.MethodPost:
			if key := r.Header.Get("Idempotency-Key"); key != "" {
				t.Errorf("签到没有幂等要求，不该带 Idempotency-Key：%q", key)
			}
			if body, _ := io.ReadAll(r.Body); len(body) != 0 {
				t.Errorf("签到不该带请求体：%q", body)
			}
			mu.Lock()
			posts++
			mu.Unlock()
			_, _ = io.WriteString(w, envelopeJSON(`{"signed":true,"streak":6,"reward_coins":5}`))
		case r.URL.Path == "/api/v1/me/sign":
			_, _ = io.WriteString(w, envelopeJSON(`{"signed":true,"streak":6,"reward_coins":5}`))
		case r.URL.Path == "/api/v1/me/coins/balance":
			mu.Lock()
			balances++
			mu.Unlock()
			_, _ = io.WriteString(w, envelopeJSON(`{"coins":105}`))
		case r.URL.Path == "/api/v1/me/coins/balance-with-ranking":
			_, _ = io.WriteString(w, envelopeJSON(`{"coins":105,"rank":9}`))
		case r.URL.Path == "/api/v1/me/coins/today-transaction":
			mu.Lock()
			todays++
			mu.Unlock()
			_, _ = io.WriteString(w, envelopeJSON(`[{"id":"c1","reason":"checkin","amount":5}]`))
		case r.URL.Path == "/api/v1/pandora/today":
			_, _ = io.WriteString(w, envelopeJSON(`{"has_box":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := coinTestClient(t, srv)
	a.coins.sign = coinSignStatus{Known: true, Signed: false}
	a.signInNow()
	waitCoin(t, "签到并刷新余额", func() bool {
		return !a.coins.signBusy && a.coins.sign.Signed &&
			a.coins.balance == 105 && a.coins.todayLoaded
	})
	mu.Lock()
	gotPosts, gotBalances, gotTodays := posts, balances, todays
	mu.Unlock()
	if gotPosts != 1 {
		t.Errorf("应提交 1 次签到，实际 %d", gotPosts)
	}
	if gotBalances == 0 || gotTodays == 0 {
		t.Errorf("签到后应刷新余额与今日明细：余额 %d 次，今日 %d 次", gotBalances, gotTodays)
	}
	if a.coins.sign.Streak != 6 || a.coins.sign.Coins != 5 {
		t.Errorf("签到状态没有按响应更新：%+v", a.coins.sign)
	}
	if len(a.coins.todayItems) != 1 || a.coins.todayItems[0].Reason != "checkin" {
		t.Errorf("今日明细没有刷新：%+v", a.coins.todayItems)
	}
	if len(a.toasts) != 1 || a.toasts[0].target != toastCoins {
		t.Fatalf("签到结果应投递到金币窗口：%+v", a.toasts)
	}
	for _, want := range []string{"签到成功", "5 金币", "连续 6 天"} {
		if !strings.Contains(a.toasts[0].text, want) {
			t.Errorf("签到成功提示缺少 %q：%q", want, a.toasts[0].text)
		}
	}
}

// 今天已经签过时服务端返回 409 conflict：不要当成失败展示，按已签到处理，
// 并从签到状态接口同步连续天数。
func TestCoinSignConflictMarksSignedToday(t *testing.T) {
	var mu sync.Mutex
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/me/sign":
			if r.Method == http.MethodPost {
				mu.Lock()
				posts++
				mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"c":409,"code":"conflict","m":"今日已签到","d":null}`)
				return
			}
			_, _ = io.WriteString(w, envelopeJSON(`{"signed":true,"streak":3}`))
		case "/api/v1/me/coins/balance":
			_, _ = io.WriteString(w, envelopeJSON(`{"coins":100}`))
		case "/api/v1/me/coins/balance-with-ranking":
			_, _ = io.WriteString(w, envelopeJSON(`{"coins":100,"rank":9}`))
		case "/api/v1/pandora/today":
			_, _ = io.WriteString(w, envelopeJSON(`{"has_box":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := coinTestClient(t, srv)
	a.coins.sign = coinSignStatus{Known: true, Signed: false, Streak: 2}
	a.signInNow()
	waitCoin(t, "重复签到按已签到处理", func() bool {
		return !a.coins.signBusy && a.coins.sign.Signed && a.coins.sign.Streak == 3
	})
	mu.Lock()
	gotPosts := posts
	mu.Unlock()
	if gotPosts != 1 {
		t.Errorf("应提交 1 次签到，实际 %d", gotPosts)
	}
	if len(a.toasts) != 1 || !strings.Contains(a.toasts[0].text, "已经签过") {
		t.Errorf("重复签到应提示今天已经签过：%+v", a.toasts)
	}
}

// 缺少 write:economy 时和收藏 / 表态一样交给全局授权弹窗，而不是弹一条
// 普通错误 toast；授权成功后会原样重跑一次签到。
func TestCoinSignNeedsWriteEconomyScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/me/sign" && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"c":403,"code":"insufficient_scope","required_scope":["write:economy"],"m":"缺少权限范围","d":null}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	a := coinTestClient(t, srv)
	a.coins.sign = coinSignStatus{Known: true, Signed: false}
	a.signInNow()
	waitCoin(t, "签到缺权限进授权弹窗", func() bool {
		return !a.coins.signBusy && a.authGate.open
	})
	found := false
	for _, scope := range a.authGate.need.Scopes {
		if scope == "write:economy" {
			found = true
		}
	}
	if !found {
		t.Errorf("授权弹窗缺少 write:economy：%+v", a.authGate.need)
	}
	if len(a.authGate.replays) != 1 {
		t.Errorf("授权成功后应重跑签到：%+v", a.authGate.replays)
	}
	if len(a.toasts) != 0 {
		t.Errorf("缺权限时不该弹普通错误 toast：%+v", a.toasts)
	}
	if a.coins.sign.Signed {
		t.Error("缺权限失败时不能把状态改成已签到")
	}
}

// 签到按钮接在金币窗口的余额卡片里：未签到时出现，点一下真的提交；结果以
// 金币窗口自己的 toast 报告，签完之后按钮消失。
func TestCoinSignButtonSubmitsAndReportsToast(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/me/sign":
			if r.Method == http.MethodPost {
				posts.Add(1)
				_, _ = io.WriteString(w, envelopeJSON(`{"signed":true,"streak":6,"reward_coins":5}`))
				return
			}
			_, _ = io.WriteString(w, envelopeJSON(`{"signed":true,"streak":6}`))
		case "/api/v1/me/coins/balance":
			_, _ = io.WriteString(w, envelopeJSON(`{"coins":105}`))
		case "/api/v1/me/coins/balance-with-ranking":
			_, _ = io.WriteString(w, envelopeJSON(`{"coins":105,"rank":9}`))
		case "/api/v1/me/coins/today-transaction":
			_, _ = io.WriteString(w, envelopeJSON(`[{"id":"c1","reason":"checkin","amount":5}]`))
		case "/api/v1/pandora/today":
			_, _ = io.WriteString(w, envelopeJSON(`{"has_box":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := coinTestClient(t, srv)
	a.coins.baseLoaded, a.coins.balance, a.coins.balanceOK = true, 100, true
	a.coins.todayLoaded = true
	a.coins.sign = coinSignStatus{Known: true, Signed: false}
	tt := ui.NewTester(a.coinWindowView, coinWindowWidth, coinWindowHeight)
	tt.Frame()
	if _, ok := tt.Find("立即签到"); !ok {
		t.Fatalf("未签到时缺少签到按钮：%q", tt.Texts())
	}
	if err := tt.Click("立即签到"); err != nil {
		t.Fatalf("点击签到失败：%v", err)
	}
	waitCoin(t, "点击签到提交", func() bool {
		return posts.Load() == 1 && !a.coins.signBusy && a.coins.sign.Signed
	})

	tt = ui.NewTester(a.coinWindowView, coinWindowWidth, coinWindowHeight)
	tt.Frame()
	if !tt.HasText("获得 5 金币 · 连续 6 天") {
		t.Fatalf("金币窗口没有显示签到结果的 toast：%q", tt.Texts())
	}
	if _, ok := tt.Find("立即签到"); ok {
		t.Errorf("已签到后仍显示签到按钮：%q", tt.Texts())
	}
	tt.Advance(6 * time.Second)
	if tt.HasText("获得 5 金币 · 连续 6 天") {
		t.Errorf("签到提示过期后还留在窗口里，说明它是内联文字而不是 toast：%q", tt.Texts())
	}
}

// 千分位分隔符是标题栏金币数与金币窗口共用的写法。
func TestCoinThousandsGroupsDigits(t *testing.T) {
	cases := map[int]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000",
		1234567: "1,234,567", -1234: "-1,234",
	}
	for n, want := range cases {
		if got := coinThousands(n); got != want {
			t.Errorf("coinThousands(%d) = %q，期望 %q", n, got, want)
		}
	}
}

// 标题栏金币数只给登录用户看：未登录时那个位置什么都不画，登录后显示
// 💰 + 千分位余额，并且排在账号标识的左边，点一下打开金币窗口。
func TestCoinTagShownOnlyForLoggedInUser(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if tt.HasText("💰") || tt.HasText("金币余额") {
		t.Fatalf("未登录时不该出现金币数：%q", tt.Texts())
	}

	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: "jimmy",
	}); err != nil {
		t.Fatal(err)
	}
	a.onAuthChanged(nil)
	// 无头测试不发真实请求，直接摆出一份余额。
	a.coins.balance, a.coins.balanceOK = 1234567, true

	tt = ui.NewTester(a.view, 1080, 720)
	tt.Frame()
	if !tt.HasText("💰") || !tt.HasText("1,234,567") {
		t.Fatalf("登录后标题栏没有金币数：%q", tt.Texts())
	}
	coinR, ok := tt.Find("金币余额")
	if !ok {
		t.Fatalf("金币数没有可访问名：%q", tt.Texts())
	}
	userR, ok := tt.Find("jimmy")
	if !ok {
		t.Fatalf("标题栏没有账号标识：%q", tt.Texts())
	}
	if coinR.X >= userR.X {
		t.Errorf("金币数应在账号标识左侧：金币 x=%.0f，账号 x=%.0f", coinR.X, userR.X)
	}
	iconR, ok := tt.Find("💰")
	if !ok {
		t.Fatalf("金币数缺少 💰 图标：%q", tt.Texts())
	}
	amountR, ok := tt.Find("1,234,567")
	if !ok {
		t.Fatalf("找不到千分位余额：%q", tt.Texts())
	}
	if iconR.X >= amountR.X {
		t.Errorf("💰 图标应在金额左侧：图标 x=%.0f，金额 x=%.0f", iconR.X, amountR.X)
	}

	opened := 0
	a.onCoinsOpen = func() { opened++ }
	if err := tt.Click("金币余额"); err != nil {
		t.Fatalf("点击金币数失败：%v", err)
	}
	if opened != 1 {
		t.Errorf("点金币数没有打开金币窗口：opened=%d", opened)
	}
	if a.coinWin != nil {
		t.Error("headless 测试里不该创建金币窗口")
	}
}

// 登录后立即读一次余额，之后按间隔刷新；退出登录时停掉，不再发请求。
func TestCoinTagPeriodicRefresh(t *testing.T) {
	var hits atomic.Int32
	var balance atomic.Int64
	balance.Store(42)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/me/coins/balance" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("余额请求缺少令牌：%q", got)
		}
		hits.Add(1)
		_, _ = io.WriteString(w, envelopeJSON(fmt.Sprintf(`{"coins":%d}`, balance.Load())))
	}))
	defer srv.Close()

	a := testApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: "jimmy",
	}); err != nil {
		t.Fatal(err)
	}
	old := coinTagRefreshEvery
	coinTagRefreshEvery = 20 * time.Millisecond
	defer func() {
		coinTagRefreshEvery = old
		a.stopCoinTagWatch()
	}()

	a.startCoinTagWatch()
	waitCoin(t, "登录后读取金币数", func() bool {
		return a.coins.balanceOK && a.coins.balance == 42
	})

	// 服务端余额变了：不用手动刷新，定时器到点后标题栏与金币窗口共用
	// 的数字自己跟上。
	balance.Store(1234)
	waitCoin(t, "定时刷新金币数", func() bool { return a.coins.balance == 1234 })
	if hits.Load() < 2 {
		t.Errorf("应至少请求 2 次余额，实际 %d", hits.Load())
	}

	a.stopCoinTagWatch()
	// 让停掉前已经在路上的那一次请求落完，再确认之后不再发请求。
	time.Sleep(60 * time.Millisecond)
	before := hits.Load()
	time.Sleep(80 * time.Millisecond)
	if got := hits.Load(); got != before {
		t.Errorf("停掉定时器后仍在刷新：%d → %d", before, got)
	}
}

// 金币窗口缩到最小尺寸时，视图切换、签到状态与刷新都还在窗口里。
func TestCoinWindowMinimumSize(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: "jimmy",
	}); err != nil {
		t.Fatal(err)
	}
	a.coins.baseLoaded, a.coins.balance, a.coins.balanceOK = true, 1234, true
	a.coins.sign = coinSignStatus{Known: true, Signed: true, Streak: 5}
	a.coins.todayLoaded = true
	tt := ui.NewTester(a.coinWindowView, coinWinMinWidth, coinWinMinHeight)
	tt.Frame()
	for _, label := range []string{"今日", "历史", "统计", "排行", "刷新"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Errorf("最小窗口下缺少 %q：%q", label, tt.Texts())
			continue
		}
		if r.X < 0 || r.X+r.W > coinWinMinWidth {
			t.Errorf("%q 超出 %d 宽的窗口：x=%.0f 宽 %.0f",
				label, coinWinMinWidth, r.X, r.W)
		}
	}

	// 未签到时多出一颗签到按钮，最小尺寸下它也不能被挤出窗口。
	a.coins.sign = coinSignStatus{Known: true, Signed: false}
	tt = ui.NewTester(a.coinWindowView, coinWinMinWidth, coinWinMinHeight)
	tt.Frame()
	r, ok := tt.Find("立即签到")
	if !ok {
		t.Fatalf("最小窗口下缺少签到按钮：%q", tt.Texts())
	}
	if r.X < 0 || r.X+r.W > coinWinMinWidth {
		t.Errorf("签到按钮超出 %d 宽的窗口：x=%.0f 宽 %.0f", coinWinMinWidth, r.X, r.W)
	}
}

// 未登录时点金币入口先弹全局授权窗，金币窗口本身不打开。
func TestCoinWindowNeedsLogin(t *testing.T) {
	a := testApp(t)
	a.showCoinWindow()
	if !a.authGate.open {
		t.Error("未登录时应弹出授权弹窗")
	}
	if a.coinWin != nil {
		t.Error("未登录时不该打开金币窗口")
	}
}
