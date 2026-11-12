package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 金币页的数据层。这一页是独立的金币窗口（见 coins_window.go），入口是
// 主窗口标题栏上登录账号左侧的金币数。除签到外都是只读端点，与
// api-economy.md 的「金币查询 / 签到 / 榜单」一一对应；标「需开通」的端点
// 失败时只降级展示，不参与授权弹窗。
//
//	GET  /v1/me/coins/balance              余额
//	GET  /v1/me/coins/transactions         金币流水（分页 + 日期过滤，无 limit）
//	GET  /v1/me/coins/balance-with-ranking 余额 + 我的排名（需开通）
//	GET  /v1/me/coins/today-transaction    今日收支明细（需开通）
//	GET  /v1/me/coins/daily-flow           按日聚合（需开通）
//	GET  /v1/me/coins/expense-breakdown    收入 / 支出分类（需开通）
//	GET  /v1/me/sign                       今日签到状态
//	POST /v1/me/sign                       签到（scope write:economy）
//	GET  /v1/coins/ranking                 金币排行榜（需开通）
//	GET  /v1/pandora/today                 今日盲盒状态（需开通，只读）
//
// 签到是金币窗口里唯一的写操作：它没有请求体、没有幂等键，今日重复签到由
// 服务端以 409 conflict 拒绝。开盲盒、竞猜、兑换、解锁等其它会改变金币的
// 操作仍然不做，界面上也不提供入口；发帖、回复、表态与打赏在各自的页面里，
// 成功后统一发出金币变动事件（见 coin_events.go），本页已经加载过的数据会
// 跟着刷新。
const (
	coinBalancePath        = "/v1/me/coins/balance"
	coinBalanceRankingPath = "/v1/me/coins/balance-with-ranking"
	coinTransactionsPath   = "/v1/me/coins/transactions"
	coinTodayPath          = "/v1/me/coins/today-transaction"
	coinDailyFlowPath      = "/v1/me/coins/daily-flow"
	coinBreakdownPath      = "/v1/me/coins/expense-breakdown"
	coinSignPath           = "/v1/me/sign"
	coinRankingPath        = "/v1/coins/ranking"
	pandoraTodayPath       = "/v1/pandora/today"

	coinRankingLimit = 30
	coinFlowDays     = 30

	// coinSignTimeout 是提交签到的超时。签到本身很轻，和收藏 / 表态一样给
	// 20 秒，避免网络卡住时按钮一直转圈。
	coinSignTimeout = 20 * time.Second
)

// 金币历史的时间范围。只作为 query 参数使用，客户端不做二次筛选：
// 服务端按东八区自然日解释 start_date / end_date。
const (
	coinRangeAll = iota
	coinRangeToday
	coinRange7Days
	coinRange30Days
)

var coinRangeLabels = []string{"全部", "今天", "近 7 天", "近 30 天"}

// coinNow 单独拿出来给测试替换；接口日期按东八区自然日解释。
var coinNow = func() time.Time {
	return time.Now().In(time.FixedZone("UTC+8", 8*3600))
}

func coinTodayDate() string { return coinNow().Format("2006-01-02") }

func coinRangeDates(r int) (start, end string) {
	now := coinNow()
	switch r {
	case coinRangeToday:
		d := now.Format("2006-01-02")
		return d, d
	case coinRange7Days:
		return now.AddDate(0, 0, -6).Format("2006-01-02"), now.Format("2006-01-02")
	case coinRange30Days:
		return now.AddDate(0, 0, -29).Format("2006-01-02"), now.Format("2006-01-02")
	default:
		return "", ""
	}
}

// coinMonthStart 是收支分类的默认区间起点：本月 1 日。
func coinMonthStart() string {
	now := coinNow()
	return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02")
}

// coinTransaction 是流水中的一条。字段名来自开放 API 与站点同一份数据：
// reason 是机器码，relate_* 是可选的关联帖子 / 评论，discount_info 记录
// 徽章或潘多拉造成的实付折扣。
type coinTransaction struct {
	ID            string
	Reason        string
	Amount        float64
	BaseAmount    float64
	HasDiscount   bool
	DiscountBy    string
	CreatedAt     string
	RelatePath    string
	RelateTitle   string
	RelateComment string
	Emoji         string
	FromUser      string
	FromAvatar    string // 打赏人等来源用户的头像地址
	FromBadges    []badgeInfo
	IsAlias       bool
	CheckData     object
}

type coinTransactionPage struct {
	Items      []coinTransaction
	Total      int
	Page       int
	Limit      int
	TotalPages int
}

type coinSignStatus struct {
	Known  bool
	Signed bool
	Streak int
	Coins  float64
	// RewardKnown 表示 Coins 来自明确的奖励字段（reward_coins / today_coins /
	// reward）。文档没有给签到响应的字段表，name 为 coins 的字段可能是余额，
	// 所以只有明确标注过是奖励的字段才允许展示成「获得」。
	RewardKnown bool
}

type coinDailyFlow struct {
	Date    string
	Income  float64
	Expense float64
	Net     float64
}

type coinBreakdown struct {
	Reason string
	Amount float64
	Ratio  float64
}

type coinRankEntry struct {
	Rank      int
	Username  string
	Balance   int
	IsPro     bool
	AvatarURL string
	Badges    []badgeInfo
}

type coinPandoraStatus struct {
	Known       bool
	HasBox      bool
	CanOpen     bool
	OpenedToday bool
	EventTitle  string
	NextOpenAt  string
}

// coinState 是金币页的全部界面状态。只有 UI 线程读写；后台请求完成后通过
// publish 回填，并用每类数据的 seq 丢弃过期结果。
type coinState struct {
	view        int
	todayList   ui.ListState
	historyList ui.ListState
	rankingList ui.ListState

	baseSeq     uint64
	baseLoading bool
	baseLoaded  bool
	baseWait    bool // 首屏失败在等全局授权补权限，避免每帧重发
	balance     int
	balanceOK   bool
	balanceErr  string
	rank        int
	rankOK      bool

	sign        coinSignStatus
	signErr     string
	signBusy    bool   // 正在提交签到；按钮据此禁用，避免连点
	signGen     uint64 // 丢弃过期的签到结果（刷新、重授权、换账号）
	pandora     coinPandoraStatus
	pandoraSeen bool

	todaySeq     uint64
	todayLoading bool
	todayLoaded  bool
	todayWait    bool
	todayItems   []coinTransaction
	todayIncome  float64
	todayExpense float64
	todayNet     float64
	todayErr     string
	todayNote    string
	todayFrom    string // today-transaction / transactions

	historySeq        uint64
	historyLoading    bool
	historyLoaded     bool
	historyWait       bool
	historyItems      []coinTransaction
	historyPage       int
	historyTotal      int
	historyTotalPages int
	historyNoMore     bool
	historyRange      int
	historyErr        string
	historyMoreErr    string

	dailySeq     uint64
	dailyLoading bool
	dailyLoaded  bool
	dailyItems   []coinDailyFlow
	dailyErr     string

	breakdownSeq     uint64
	breakdownLoading bool
	breakdownLoaded  bool
	breakdownType    string
	breakdownItems   []coinBreakdown
	breakdownErr     string

	rankingSeq     uint64
	rankingLoading bool
	rankingLoaded  bool
	rankingItems   []coinRankEntry
	rankingErr     string

	// 本地缓存状态。updatedAt 是当前显示数据的新鲜时间；fromCache 表示首屏
	// 来自 cache/offline.json（之后网络刷新成功会清掉）；cacheNote 是金币页
	// 顶部的一行说明，告诉用户现在看的是不是本地缓存。
	updatedAt time.Time
	fromCache bool
	cacheNote string
}

func newCoinState() *coinState {
	return &coinState{breakdownType: "income"}
}

// ---- 响应解析 ----
//
// 经济类端点的响应结构没有在文档里逐字段列出，这里按「认得的字段用、不
// 认识的字段忽略」处理，并兼容站点当前使用的 items 列表。

func rawNumber(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f, true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return v, true
		}
	}
	return 0, false
}

func (o object) number(keys ...string) (float64, bool) {
	for _, k := range keys {
		if raw, ok := o[k]; ok {
			if f, ok := rawNumber(raw); ok {
				return f, true
			}
		}
	}
	return 0, false
}

func (o object) boolValue(keys ...string) (bool, bool) {
	for _, k := range keys {
		if raw, ok := o[k]; ok {
			var b bool
			if json.Unmarshal(raw, &b) == nil {
				return b, true
			}
		}
	}
	return false, false
}

func decodeCoinTransaction(raw json.RawMessage) coinTransaction {
	o := asObject(raw)
	if o == nil {
		return coinTransaction{}
	}
	tx := coinTransaction{
		ID:            o.str("id", "uuid"),
		Reason:        o.str("reason", "type"),
		CreatedAt:     o.str("created_at", "createdAt"),
		RelatePath:    o.str("relate_path", "relatePath"),
		RelateTitle:   o.str("relate_title", "relateTitle"),
		RelateComment: o.str("relate_comment", "relateComment"),
		Emoji:         o.str("emoji"),
		IsAlias:       o.flag("is_alias", "isAlias"),
		CheckData:     o.sub("check_data", "checkData"),
	}
	tx.Amount, _ = o.number("amount", "coins", "coin")
	tx.BaseAmount = tx.Amount
	if d := o.sub("discount_info", "discountInfo"); d != nil {
		tx.DiscountBy = d.str("discounted_by", "discountedBy")
		if base, ok := d.number("base_amount", "baseAmount"); ok {
			tx.BaseAmount = base
			tx.HasDiscount = math.Abs(tx.BaseAmount-tx.Amount) > 0.004
		}
		if tx.BaseAmount == 0 && tx.Amount != 0 {
			tx.BaseAmount = tx.Amount
		}
	}
	if from := o.sub("from_user", "fromUser", "user", "actor"); from != nil {
		tx.FromUser = from.str("username", "name", "nickname")
		tx.FromAvatar = authorAvatar(from)
		tx.FromBadges = decodeBadges(from)
	}
	return tx
}

func decodeCoinTransactionList(d json.RawMessage) []coinTransaction {
	raws := rawArray(d)
	if raws == nil {
		if o := asObject(d); o != nil {
			for _, key := range []string{"items", "list", "records", "transactions"} {
				if raws = rawArray(o[key]); raws != nil {
					break
				}
			}
		}
	}
	out := make([]coinTransaction, 0, len(raws))
	for _, raw := range raws {
		tx := decodeCoinTransaction(raw)
		if tx.ID == "" && tx.Reason == "" && tx.Amount == 0 && tx.CreatedAt == "" {
			continue
		}
		if tx.ID == "" {
			tx.ID = fmt.Sprintf("%s|%s|%.2f", tx.Reason, tx.CreatedAt, tx.Amount)
		}
		out = append(out, tx)
	}
	return out
}

func decodeCoinTransactionPage(d json.RawMessage, requestedPage int) coinTransactionPage {
	out := coinTransactionPage{Page: requestedPage}
	if o := asObject(d); o != nil {
		out.Total = rawInt(o["total"])
		if p := rawInt(o["page"]); p > 0 {
			out.Page = p
		}
		out.Limit = rawInt(o["limit"])
		out.TotalPages = rawInt(o["total_pages"])
	}
	out.Items = decodeCoinTransactionList(d)
	if out.Page <= 0 {
		out.Page = requestedPage
	}
	if out.Total <= 0 {
		out.Total = len(out.Items)
	}
	if out.TotalPages <= 0 {
		out.TotalPages = 1
	}
	return out
}

func decodeCoinBalance(d json.RawMessage) (int, bool) {
	if o := asObject(d); o != nil {
		for _, candidate := range []object{o, o.sub("user", "account", "wallet")} {
			if candidate == nil {
				continue
			}
			if f, ok := candidate.number("coins", "balance", "coin", "amount"); ok {
				return int(math.Round(f)), true
			}
		}
	}
	if f, ok := rawNumber(d); ok {
		return int(math.Round(f)), true
	}
	return 0, false
}

func decodeCoinBalanceRanking(d json.RawMessage) (coins, rank int, coinsOK, rankOK bool) {
	o := asObject(d)
	if o == nil {
		return 0, 0, false, false
	}
	if f, ok := o.number("coins", "balance", "coin"); ok {
		coins, coinsOK = int(math.Round(f)), true
	}
	if n, ok := o.number("rank", "ranking", "position", "place"); ok {
		rank, rankOK = int(math.Round(n)), true
	}
	if sub := o.sub("user", "account"); sub != nil {
		if !coinsOK {
			if f, ok := sub.number("coins", "balance"); ok {
				coins, coinsOK = int(math.Round(f)), true
			}
		}
		if !rankOK {
			if n, ok := sub.number("rank", "ranking", "position"); ok {
				rank, rankOK = int(math.Round(n)), true
			}
		}
	}
	return coins, rank, coinsOK, rankOK
}

func decodeCoinSign(d json.RawMessage) coinSignStatus {
	out := coinSignStatus{}
	root := asObject(d)
	if root == nil {
		return out
	}
	candidates := []object{root}
	for _, key := range []string{"status", "sign", "today", "data"} {
		if sub := root.sub(key); sub != nil {
			candidates = append(candidates, sub)
		}
	}
	for _, o := range candidates {
		if o == nil {
			continue
		}
		if b, ok := o.boolValue("signed", "signed_today", "today_signed", "is_signed", "already_signed", "checked_in"); ok {
			out.Known, out.Signed = true, b
			break
		}
	}
	if !out.Known {
		for _, o := range candidates {
			if o == nil {
				continue
			}
			if d := o.str("sign_date", "date"); d != "" {
				out.Known = true
				out.Signed = d == coinTodayDate()
				break
			}
		}
	}
	for _, o := range candidates {
		if o == nil {
			continue
		}
		if out.Streak == 0 {
			if n, ok := o.number("streak", "continuous_days", "sign_streak", "days"); ok {
				out.Streak = int(math.Round(n))
			}
		}
		if !out.RewardKnown {
			if n, ok := o.number("reward_coins", "today_coins", "reward"); ok {
				out.Coins, out.RewardKnown = n, true
			}
		}
		if out.Coins == 0 {
			if n, ok := o.number("coins", "coin"); ok {
				out.Coins = n
			}
		}
	}
	return out
}

func decodeCoinDailyFlows(d json.RawMessage) []coinDailyFlow {
	raws := rawArray(d)
	if raws == nil {
		if o := asObject(d); o != nil {
			for _, key := range []string{"items", "list", "records", "daily", "daily_flow"} {
				if raws = rawArray(o[key]); raws != nil {
					break
				}
			}
		}
	}
	out := make([]coinDailyFlow, 0, len(raws))
	for _, raw := range raws {
		o := asObject(raw)
		if o == nil {
			continue
		}
		f := coinDailyFlow{Date: o.str("date", "day", "sign_date")}
		f.Income, _ = o.number("income", "coins_in", "in")
		f.Expense, _ = o.number("expense", "coins_out", "out")
		if n, ok := o.number("net"); ok {
			f.Net = n
		} else {
			f.Net = f.Income - f.Expense
		}
		if f.Date == "" {
			continue
		}
		out = append(out, f)
	}
	return out
}

func decodeCoinBreakdown(d json.RawMessage) []coinBreakdown {
	raws := rawArray(d)
	if raws == nil {
		if o := asObject(d); o != nil {
			for _, key := range []string{"items", "list", "records", "breakdown"} {
				if raws = rawArray(o[key]); raws != nil {
					break
				}
			}
		}
	}
	out := make([]coinBreakdown, 0, len(raws))
	for _, raw := range raws {
		o := asObject(raw)
		if o == nil {
			continue
		}
		b := coinBreakdown{Reason: o.str("reason", "type")}
		b.Amount, _ = o.number("amount", "coins", "coin")
		if r, ok := o.number("ratio", "percent", "percentage"); ok {
			if r > 1.5 {
				r /= 100
			}
			b.Ratio = r
		}
		out = append(out, b)
	}
	return out
}

func decodeCoinRanking(d json.RawMessage) []coinRankEntry {
	raws := rawArray(d)
	if raws == nil {
		if o := asObject(d); o != nil {
			for _, key := range []string{"items", "list", "ranking", "users"} {
				if raws = rawArray(o[key]); raws != nil {
					break
				}
			}
		}
	}
	out := make([]coinRankEntry, 0, len(raws))
	for i, raw := range raws {
		o := asObject(raw)
		if o == nil {
			continue
		}
		e := coinRankEntry{
			Rank:      o.num("rank", "ranking", "position"),
			Username:  o.str("username", "name"),
			IsPro:     o.flag("is_pro", "isPro"),
			AvatarURL: o.str("avatar_url", "avatarUrl"),
			Badges:    decodeBadges(o),
		}
		if e.Rank <= 0 {
			e.Rank = i + 1
		}
		if f, ok := o.number("balance", "coins"); ok {
			e.Balance = int(math.Round(f))
		}
		if e.Username == "" {
			continue
		}
		out = append(out, e)
	}
	return out
}

func decodeCoinPandora(d json.RawMessage) coinPandoraStatus {
	o := asObject(d)
	if o == nil {
		return coinPandoraStatus{}
	}
	out := coinPandoraStatus{
		Known:       o.has("has_box", "can_open", "opened_today", "box"),
		HasBox:      o.flag("has_box", "hasBox"),
		CanOpen:     o.flag("can_open", "canOpen"),
		OpenedToday: o.flag("opened_today", "openedToday"),
		NextOpenAt:  o.str("next_open_at", "nextOpenAt"),
	}
	if box := o.sub("box"); box != nil {
		out.EventTitle = box.str("event_title", "eventTitle", "event_name", "eventName")
	}
	if out.EventTitle == "" {
		out.EventTitle = o.str("event_title", "eventTitle", "event_name", "eventName")
	}
	return out
}

// ---- 请求 ----

func (c *Client) coinGET(ctx context.Context, path string, query url.Values) (json.RawMessage, error) {
	return withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		u := c.base() + path
		if len(query) > 0 {
			u += "?" + query.Encode()
		}
		return c.getJSON(ctx, u, token)
	})
}

func coinTransactionsQuery(page int, start, end string) url.Values {
	q := url.Values{}
	q.Set("page", strconv.Itoa(page))
	if start != "" {
		q.Set("start_date", start)
	}
	if end != "" {
		q.Set("end_date", end)
	}
	return q
}

func (c *Client) fetchCoinBalance(ctx context.Context) (int, error) {
	d, err := c.coinGET(ctx, coinBalancePath, nil)
	if err != nil {
		return 0, err
	}
	coins, ok := decodeCoinBalance(d)
	if !ok {
		return 0, errors.New("余额响应里没有 coins")
	}
	return coins, nil
}

func (c *Client) fetchCoinBalanceRanking(ctx context.Context) (coins, rank int, err error) {
	d, err := c.coinGET(ctx, coinBalanceRankingPath, nil)
	if err != nil {
		return 0, 0, err
	}
	coins, rank, _, rankOK := decodeCoinBalanceRanking(d)
	if !rankOK {
		return coins, 0, errors.New("排名响应里没有 rank")
	}
	return coins, rank, nil
}

func (c *Client) fetchCoinTransactions(ctx context.Context, page int, start, end string) (coinTransactionPage, error) {
	d, err := c.coinGET(ctx, coinTransactionsPath, coinTransactionsQuery(page, start, end))
	if err != nil {
		return coinTransactionPage{}, err
	}
	return decodeCoinTransactionPage(d, page), nil
}

func (c *Client) fetchCoinTodayTransactions(ctx context.Context) ([]coinTransaction, error) {
	d, err := c.coinGET(ctx, coinTodayPath, nil)
	if err != nil {
		return nil, err
	}
	return decodeCoinTransactionList(d), nil
}

func (c *Client) fetchCoinSign(ctx context.Context) (coinSignStatus, error) {
	d, err := c.coinGET(ctx, coinSignPath, nil)
	if err != nil {
		return coinSignStatus{}, err
	}
	return decodeCoinSign(d), nil
}

// signToday 提交今日签到（POST /v1/me/sign，scope write:economy）。签到没有
// 请求体，也不在文档的幂等键清单里，因此不带 Idempotency-Key。它和其它写
// 操作一样走 withAuthRetry：401 先刷新令牌再重试一次，403 / 429 等原样交给
// 调用方。今日已签到时服务端返回 409 conflict，由调用方按「已经签过」处理。
func (c *Client) signToday(ctx context.Context) (coinSignStatus, error) {
	var status coinSignStatus
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		d, err := c.postJSON(ctx, c.base()+coinSignPath, token, "", nil)
		if err != nil {
			return struct{}{}, err
		}
		status = decodeCoinSign(d)
		return struct{}{}, nil
	})
	return status, err
}

func (c *Client) fetchCoinDailyFlow(ctx context.Context, start, end string) ([]coinDailyFlow, error) {
	q := url.Values{}
	if start != "" {
		q.Set("start_date", start)
	}
	if end != "" {
		q.Set("end_date", end)
	}
	d, err := c.coinGET(ctx, coinDailyFlowPath, q)
	if err != nil {
		return nil, err
	}
	return decodeCoinDailyFlows(d), nil
}

func (c *Client) fetchCoinBreakdown(ctx context.Context, start, end, flowType string) ([]coinBreakdown, error) {
	q := url.Values{}
	if start != "" {
		q.Set("start_date", start)
	}
	if end != "" {
		q.Set("end_date", end)
	}
	if flowType != "" {
		q.Set("flow_type", flowType)
	}
	d, err := c.coinGET(ctx, coinBreakdownPath, q)
	if err != nil {
		return nil, err
	}
	return decodeCoinBreakdown(d), nil
}

func (c *Client) fetchCoinRanking(ctx context.Context) ([]coinRankEntry, error) {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(coinRankingLimit))
	d, err := c.coinGET(ctx, coinRankingPath, q)
	if err != nil {
		return nil, err
	}
	return decodeCoinRanking(d), nil
}

func (c *Client) fetchCoinPandora(ctx context.Context) (coinPandoraStatus, error) {
	d, err := c.coinGET(ctx, pandoraTodayPath, nil)
	if err != nil {
		return coinPandoraStatus{}, err
	}
	return decodeCoinPandora(d), nil
}

// ---- 展示辅助 ----

// coinReasonLabel 把 reason 机器码翻成界面文案。未知 reason 原样展示，方便
// 新类型上线后仍能看到记录，而不是显示成空白。
var coinReasonLabels = map[string]string{
	"checkin":                          "签到",
	"comment":                          "评论",
	"post":                             "发帖",
	"system":                           "系统操作",
	"pandora":                          "潘多拉",
	"send_reaction":                    "发送表情",
	"get_reaction":                     "收到表情",
	"register":                         "注册",
	"suggestNode":                      "生成节点",
	"weekly_balance_reward":            "每周平衡值奖励",
	"daily_active_reward":              "每日活跃奖励",
	"inviteGain":                       "注册邀请",
	"send_reward":                      "发送打赏",
	"receive_reward":                   "收到打赏",
	"reward_pool":                      "金币池",
	"contentFormat":                    "内容排版",
	"content_check":                    "内容检定",
	"attribute_check":                  "属性检定",
	"makeup":                           "补签",
	"badge_equip":                      "徽章",
	"pin_post":                         "置顶",
	"pin_comment":                      "评论置顶",
	"get_pin_comment":                  "评论被置顶",
	"avatar":                           "头像",
	"update_readme":                    "介绍",
	"bet_create":                       "创建竞猜",
	"bet_participate":                  "参与竞猜",
	"bet_win":                          "竞猜获胜",
	"bet_lose":                         "竞猜失败",
	"bet_fee":                          "竞猜收益分配",
	"bet_invalid_refund":               "竞猜无效返还",
	"bet_invalid_penalty":              "竞猜无效处罚",
	"restricted_auction_win_deduct":    "竞拍获胜扣除",
	"restricted_auction_win_fee":       "竞拍获胜手续费",
	"restricted_auction_author_income": "竞拍作者收益",
	"restricted_auction_loser_penalty": "竞拍失败扣除",
	"reward_pool_create":               "金币池创建",
	"reward_pool_supplement":           "金币池补充",
	"reward_pool_refund":               "金币池返还",
	"edit_comment":                     "编辑评论",
	"edit_post":                        "编辑帖子",
	"edit_postscript":                  "编辑附言",
	"add_postscript":                   "添加附言",
	"send_red_packet":                  "发送红包",
	"receive_red_packet":               "收到红包",
	"pro_sponsor":                      "Pro 赞助赠送",
	"create_alias":                     "创建分身",
	"pandora_extend":                   "潘多拉事件延期",
	"showcase_bid_freeze":              "社区橱窗出价冻结",
	"showcase_bid_refund":              "社区橱窗冻结退回",
}

func coinReasonLabel(reason string) string {
	if label, ok := coinReasonLabels[reason]; ok {
		return label
	}
	if reason == "" {
		return "金币变动"
	}
	return reason
}

func coinTotals(items []coinTransaction) (income, expense, net float64) {
	for _, tx := range items {
		if tx.Amount >= 0 {
			income += tx.Amount
		} else {
			expense += -tx.Amount
		}
		net += tx.Amount
	}
	return income, expense, net
}

func coinAmountText(v float64) string {
	if math.Abs(v-math.Round(v)) < 0.005 {
		return strconv.FormatInt(int64(math.Round(v)), 10)
	}
	s := strconv.FormatFloat(v, 'f', 2, 64)
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}

func coinSignedText(v float64) string {
	if v > 0 {
		return "+" + coinAmountText(v)
	}
	return coinAmountText(v)
}

// coinExpenseText 给「支出」用：0 不写成 -0，正数前面补负号。
func coinExpenseText(v float64) string {
	if v <= 0 {
		return "0"
	}
	return "-" + coinAmountText(v)
}

func coinThousands(n int) string {
	negative := n < 0
	s := strconv.Itoa(n)
	if negative {
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if negative {
		return "-" + s
	}
	return s
}

// coinSortDailyNewestFirst 让按日曲线在列表里从新到旧展示；服务端可能按
// 升序返回，因此这里显式排序。
func coinSortDailyNewestFirst(items []coinDailyFlow) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].Date > items[j].Date })
}

// coinSortBreakdownDesc 让分类按金额从大到小排列。
func coinSortBreakdownDesc(items []coinBreakdown) {
	sort.SliceStable(items, func(i, j int) bool { return items[i].Amount > items[j].Amount })
}

// coinOptionalNote 把「需开通」端点的失败翻译成可展示的降级说明；这类端点
// 不进入全局授权弹窗，避免用户为一个本来就没开通的高级能力反复授权。
func coinOptionalNote(err error) string {
	if err == nil {
		return ""
	}
	var ae *apiError
	if errors.As(err, &ae) {
		if ae.Code == "insufficient_scope" || ae.Status == 403 {
			return "该接口未开通，暂时无法显示"
		}
	}
	return friendlyError(err)
}
