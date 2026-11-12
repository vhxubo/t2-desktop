package main

import (
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

func TestCoinTabRendersBalanceTodayHistoryStatsRanking(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	idx := coinTabIndex()
	if idx < 0 {
		t.Fatal("没有金币 tab")
	}
	a.tab, a.lastTab, a.navSel = idx, idx, tabID(idx)
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

	tt := ui.NewTester(a.view, 1200, 800)
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

func TestCoinPageHasNoWriteActions(t *testing.T) {
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.tab, a.lastTab, a.navSel = coinTabIndex(), coinTabIndex(), tabID(coinTabIndex())
	a.coins.baseLoaded, a.coins.todayLoaded, a.coins.sign = true, true, coinSignStatus{Known: true, Signed: false}
	tt := ui.NewTester(a.view, 1200, 800)
	for _, forbidden := range []string{"签到", "签到领金币", "开盲盒", "打赏"} {
		// 「签到」作为状态文案可以出现，但界面上不应有签到按钮；
		// 这里只检查会导致金币变化的动作文案。
		if forbidden == "签到" {
			continue
		}
		if tt.HasText(forbidden) {
			t.Errorf("金币页不该出现写操作入口 %q：%q", forbidden, tt.Texts())
		}
	}
}
