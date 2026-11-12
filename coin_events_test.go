package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 金币变动事件（coin_events.go）的测试：事件要刷新标题栏与金币窗口里已经
// 加载过的数据，并且把同一瞬间的多次变动合并成一轮请求。

// coinEventPaths 是金币窗口会请求的全部端点，按加载顺序排列。
var coinEventPaths = []string{
	coinBalancePath,
	coinBalanceRankingPath,
	coinSignPath,
	pandoraTodayPath,
	coinTodayPath,
	coinTransactionsPath,
	coinDailyFlowPath,
	coinBreakdownPath,
	coinRankingPath,
}

// coinEventRecorder 记录每个金币端点被请求了几次。
type coinEventRecorder struct {
	mu    sync.Mutex
	paths map[string]int
}

func (r *coinEventRecorder) hit(path string) {
	r.mu.Lock()
	r.paths[path]++
	r.mu.Unlock()
}

func (r *coinEventRecorder) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paths[path]
}

func (r *coinEventRecorder) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.paths {
		n += c
	}
	return n
}

// coinEventServer 是所有金币端点的假服务端：余额取 balance 的当前值，其余
// 端点各回一份最小可用的数据。
func coinEventServer(t *testing.T, balance *atomic.Int64) (*httptest.Server, *coinEventRecorder) {
	t.Helper()
	rec := &coinEventRecorder{paths: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api")
		rec.hit(path)
		switch path {
		case coinBalancePath:
			_, _ = io.WriteString(w, envelopeJSON(fmt.Sprintf(`{"coins":%d}`, balance.Load())))
		case coinBalanceRankingPath:
			// 不带 coins：余额以 /balance 为准，避免盖掉被测的数值。
			_, _ = io.WriteString(w, envelopeJSON(`{"rank":7}`))
		case coinSignPath:
			_, _ = io.WriteString(w, envelopeJSON(`{"signed":true,"streak":2}`))
		case pandoraTodayPath:
			_, _ = io.WriteString(w, envelopeJSON(`{"has_box":false}`))
		case coinTodayPath:
			_, _ = io.WriteString(w, envelopeJSON(`[{"id":"t1","reason":"post","amount":-5}]`))
		case coinTransactionsPath:
			_, _ = io.WriteString(w, envelopeJSON(
				`{"items":[{"id":"h1","reason":"comment","amount":-1}],"total":1,"page":1,"total_pages":1}`))
		case coinDailyFlowPath:
			_, _ = io.WriteString(w, envelopeJSON(`[{"date":"2026-11-12","income":1,"expense":2}]`))
		case coinBreakdownPath:
			_, _ = io.WriteString(w, envelopeJSON(`[{"reason":"post","amount":3}]`))
		case coinRankingPath:
			_, _ = io.WriteString(w, envelopeJSON(`[{"username":"jimmy","balance":9,"rank":1}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, rec
}

// 金币窗口打开过（各块都已加载）时，一次金币变动事件把余额、今日明细、
// 流水、统计与排行全部重拉一遍；同一合并窗口里的多次事件只发一轮请求。
func TestCoinEventRefreshesLoadedCoinData(t *testing.T) {
	var balance atomic.Int64
	balance.Store(100)
	srv, rec := coinEventServer(t, &balance)
	defer srv.Close()

	a := coinTestClient(t, srv)
	a.autoLoad = true
	cs := a.coins
	cs.baseLoaded, cs.balance, cs.balanceOK = true, 100, true
	cs.todayLoaded = true
	cs.historyLoaded = true
	cs.dailyLoaded = true
	cs.breakdownLoaded = true
	cs.rankingLoaded = true

	old := coinRefreshDelay
	coinRefreshDelay = 20 * time.Millisecond
	defer func() {
		coinRefreshDelay = old
		a.stopCoinTagWatch()
	}()

	balance.Store(88)
	a.coinsChanged(coinEventPost)
	a.coinsChanged(coinEventReply)
	a.coinsChanged(coinEventReward)

	waitCoin(t, "金币窗口数据刷新", func() bool {
		return !cs.baseLoading && !cs.todayLoading && !cs.historyLoading &&
			!cs.dailyLoading && !cs.breakdownLoading && !cs.rankingLoading &&
			cs.balance == 88 && len(cs.todayItems) == 1 && len(cs.historyItems) == 1
	})
	for _, path := range coinEventPaths {
		if got := rec.count(path); got != 1 {
			t.Errorf("%s 请求 %d 次，三次事件应合并成一轮", path, got)
		}
	}
}

// 金币窗口从没打开过时，金币变动事件只校准标题栏余额，不把流水、统计与
// 排行一起请求出来。
func TestCoinEventWithoutCoinPageOnlyRefreshesTag(t *testing.T) {
	var balance atomic.Int64
	balance.Store(42)
	srv, rec := coinEventServer(t, &balance)
	defer srv.Close()

	a := coinTestClient(t, srv)
	a.autoLoad = true

	old := coinRefreshDelay
	coinRefreshDelay = 20 * time.Millisecond
	defer func() {
		coinRefreshDelay = old
		a.stopCoinTagWatch()
	}()

	a.coinsChanged(coinEventReaction)
	waitCoin(t, "标题栏金币刷新", func() bool {
		return a.coins.balanceOK && a.coins.balance == 42
	})
	time.Sleep(40 * time.Millisecond) // 让可能多出来的请求也落下来
	if got := rec.count(coinBalancePath); got != 1 {
		t.Errorf("余额请求 %d 次，期望 1 次", got)
	}
	if got := rec.total(); got != 1 {
		t.Errorf("没打开过金币窗口时只该请求余额，实际 %d 个请求", got)
	}
}
