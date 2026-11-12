package main

import (
	"time"
)

// 金币页的本地缓存。
//
// 余额、近 30 天资产曲线、今日明细与流水首屏都写进 cache/offline.json。
// 打开金币页时先把缓存填进 coinState 立即渲染，弱网下不用先看一会儿空白
// 和转圈；网络请求照常发起，成功后再覆盖缓存。请求失败时如果手里有缓存，
// 页面继续显示缓存内容，并在顶部说明"显示的是哪个时间的本地数据"。
//
// 缓存只属于当前登录账号（见 accountScope）；未登录或还没拿到昵称时不写盘。

// coinCacheData 是 coin:<账号> 里保存的一份金币页快照。
type coinCacheData struct {
	SavedAt      time.Time `json:"saved_at"`
	TodayDate    string    `json:"today_date,omitempty"`
	HasBase      bool      `json:"has_base"`
	HasToday     bool      `json:"has_today"`
	HasHistory   bool      `json:"has_history"`
	HasDaily     bool      `json:"has_daily"`
	HasBreakdown bool      `json:"has_breakdown"`
	HasRanking   bool      `json:"has_ranking"`

	BalanceOK   bool              `json:"balance_ok"`
	Balance     int               `json:"balance,omitempty"`
	RankOK      bool              `json:"rank_ok"`
	Rank        int               `json:"rank,omitempty"`
	Sign        coinSignStatus    `json:"sign"`
	Pandora     coinPandoraStatus `json:"pandora"`
	PandoraSeen bool              `json:"pandora_seen"`

	Today        []coinTransaction `json:"today,omitempty"`
	TodayIncome  float64           `json:"today_income,omitempty"`
	TodayExpense float64           `json:"today_expense,omitempty"`
	TodayNet     float64           `json:"today_net,omitempty"`
	TodayFrom    string            `json:"today_from,omitempty"`

	HistoryRange      int               `json:"history_range"`
	History           []coinTransaction `json:"history,omitempty"`
	HistoryPage       int               `json:"history_page,omitempty"`
	HistoryTotal      int               `json:"history_total,omitempty"`
	HistoryTotalPages int               `json:"history_total_pages,omitempty"`
	HistoryNoMore     bool              `json:"history_no_more,omitempty"`

	Daily         []coinDailyFlow `json:"daily,omitempty"`
	BreakdownType string          `json:"breakdown_type,omitempty"`
	Breakdown     []coinBreakdown `json:"breakdown,omitempty"`
	Ranking       []coinRankEntry `json:"ranking,omitempty"`
}

// loadCoinCache 把当前账号的缓存填进 a.coins。只填数据、不置 loaded：
// 页面照常按需发起网络刷新，缓存只是首屏的底稿。
func (a *app) loadCoinCache() {
	cs := a.coins
	key := a.coinCacheKey()
	if cs == nil || key == "" || a.kv == nil {
		return
	}
	var data coinCacheData
	// 缓存时间以条目里记下的 saved_at 为准（见 saveCoinCache），存储层返回的
	// 写入时间只是兜底信息，这里用不到。
	_, ok := a.kv.get(key, &data)
	if !ok || data.SavedAt.IsZero() {
		return
	}
	cs.updatedAt = data.SavedAt
	cs.fromCache = true
	cs.cacheNote = "本地缓存 · 更新于 " + formatCacheTime(data.SavedAt) + "，正在刷新…"

	if data.HasBase {
		if data.BalanceOK {
			cs.balance, cs.balanceOK = data.Balance, true
		}
		if data.RankOK {
			cs.rank, cs.rankOK = data.Rank, true
		}
		cs.sign = data.Sign
		cs.pandora = data.Pandora
		cs.pandoraSeen = data.PandoraSeen
	}
	// 「今日」只对同一个自然日有效，隔天不能把昨天的明细当成今天的。
	if data.HasToday && data.TodayDate == coinTodayDate() {
		cs.todayItems = data.Today
		cs.todayIncome, cs.todayExpense, cs.todayNet =
			data.TodayIncome, data.TodayExpense, data.TodayNet
		cs.todayFrom = data.TodayFrom
	}
	// 流水与分类都带筛选条件：范围 / 收支类型不一致时不能拿出来顶替。
	if data.HasHistory && data.HistoryRange == cs.historyRange {
		cs.historyItems = data.History
		cs.historyPage = data.HistoryPage
		cs.historyTotal = data.HistoryTotal
		cs.historyTotalPages = data.HistoryTotalPages
		cs.historyNoMore = data.HistoryNoMore
	}
	if data.HasDaily {
		cs.dailyItems = data.Daily
	}
	if data.HasBreakdown && data.BreakdownType == cs.breakdownType {
		cs.breakdownItems = data.Breakdown
	}
	if data.HasRanking {
		cs.rankingItems = data.Ranking
	}

	// 缓存里的头像地址也值得预取：头像本身长期缓存在图片缓存里，这里只
	// 保证没有缓存图片时头像位不会一直空着。
	a.prefetchCoinTransactions(cs.todayItems)
	a.prefetchCoinTransactions(cs.historyItems)
	for _, e := range cs.rankingItems {
		a.prefetchAvatars(e.AvatarURL)
	}
}

// saveCoinCache 把当前金币页状态整份写回缓存。各块请求成功时分别调用一次，
// 因此晚到的数据会覆盖早先的快照；尚未加载的块保留空值，但不会抹掉另一
// 份缓存——缓存载入发生在这个会话开始、之后所有写入都以它为基础。
func (a *app) saveCoinCache() {
	cs := a.coins
	key := a.coinCacheKey()
	if cs == nil || key == "" || a.kv == nil {
		return
	}
	now := time.Now()
	data := coinCacheData{
		SavedAt:      now,
		TodayDate:    coinTodayDate(),
		HasBase:      cs.baseLoaded || cs.balanceOK || cs.sign.Known || cs.pandoraSeen,
		HasToday:     cs.todayLoaded || cs.todayFrom != "" || len(cs.todayItems) > 0,
		HasHistory:   cs.historyLoaded || cs.historyPage > 0 || len(cs.historyItems) > 0,
		HasDaily:     cs.dailyLoaded || len(cs.dailyItems) > 0,
		HasBreakdown: cs.breakdownLoaded || len(cs.breakdownItems) > 0,
		HasRanking:   cs.rankingLoaded || len(cs.rankingItems) > 0,

		BalanceOK:   cs.balanceOK,
		Balance:     cs.balance,
		RankOK:      cs.rankOK,
		Rank:        cs.rank,
		Sign:        cs.sign,
		Pandora:     cs.pandora,
		PandoraSeen: cs.pandoraSeen,

		Today:        cs.todayItems,
		TodayIncome:  cs.todayIncome,
		TodayExpense: cs.todayExpense,
		TodayNet:     cs.todayNet,
		TodayFrom:    cs.todayFrom,

		HistoryRange:      cs.historyRange,
		History:           cs.historyItems,
		HistoryPage:       cs.historyPage,
		HistoryTotal:      cs.historyTotal,
		HistoryTotalPages: cs.historyTotalPages,
		HistoryNoMore:     cs.historyNoMore,

		Daily:         cs.dailyItems,
		BreakdownType: cs.breakdownType,
		Breakdown:     cs.breakdownItems,
		Ranking:       cs.rankingItems,
	}
	if err := a.kv.put(key, data, now); err != nil {
		logCacheWriteError(err)
		return
	}
	cs.updatedAt = now
	cs.fromCache = false
	cs.cacheNote = ""
}

// coinCacheFallback 在网络刷新失败、但当前已有可显示数据时调用：保留页面
// 上的数据，在顶部说明这是哪个时间的本地内容，而不是把整页换成错误页。
func (a *app) coinCacheFallback(cs *coinState) {
	if cs == nil {
		return
	}
	cs.cacheNote = "暂时无法刷新，显示" + coinCacheTimeText(cs.updatedAt)
}

func coinCacheTimeText(t time.Time) string {
	if t.IsZero() {
		return "上次加载的数据"
	}
	return " " + formatCacheTime(t) + " 的本地缓存"
}
