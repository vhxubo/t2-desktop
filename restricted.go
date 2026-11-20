package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 受限内容（api-economy.md「付费墙（受限内容）」）
//
//	GET  /v1/topics/:short_id/restricted              scope read，需开通
//	GET  /v1/topics/:short_id/restricted/auction      scope read，需开通
//	GET  /v1/topics/:short_id/restricted/auction/bids scope read，需开通
//	POST /v1/topics/:short_id/restricted/unlock  🔑   scope write:economy，需开通
//
// 四个端点都默认不授予：未开通时 403 insufficient_scope（带 required_scope），
// 交给 auth_gate.go 的授权链路引导补 scope，成功后重放被中断的调用。
// 解锁是花币操作，必须带 Idempotency-Key：同一笔重试（超时、授权后重放）
// 复用同一个键，成功后清空，下一次是新的解锁就换新键。
//
// 文档只写了「未解锁时返回门槛信息（金币 / 等级 / 属性），已解锁时返回
// 隐藏内容正文」「竞拍状态有当前价、剩余时间、前 N 名」以及「出价记录」，
// 没有给出任何子字段名。这里沿用 bets.go / pandora.go 的做法：对多种历史
// 拼写做容错读取，认得的字段才用，Known 区分「服务端确实没有」和「响应
// 里没有这个字段」；不猜机器码、不凭猜测判定能不能解锁。能不能扣钱只认
// 服务端给出的价格，且界面必须先让用户确认金额（terms.md 第二部分二.4）。

const restrictedTimeout = 20 * time.Second

func topicRestrictedPath(shortID string) string {
	return topicPath(shortID) + "/restricted"
}

func topicRestrictedAuctionPath(shortID string) string {
	return topicRestrictedPath(shortID) + "/auction"
}

func topicRestrictedBidsPath(shortID string) string {
	return topicRestrictedAuctionPath(shortID) + "/bids"
}

func topicRestrictedUnlockPath(shortID string) string {
	return topicRestrictedPath(shortID) + "/unlock"
}

// restrictedInfo 是一篇受限内容的门槛与解锁后的正文。
// ContentKnown / CoinsKnown 等标志让界面只在服务端真的给了值时才展示。
type restrictedInfo struct {
	Known        bool
	Unlocked     bool
	UnlockKnown  bool
	Content      string
	ContentKnown bool
	// Coins 是解锁价格，只有它已知且为正数时才提供解锁入口。
	Coins      int
	CoinsKnown bool
	Level      int
	LevelKnown bool
	AttrName   string
	AttrValue  int
	AttrKnown  bool
	// AccessType 原样保留服务端机器码；未知取值不影响界面，不拿它猜行为。
	AccessType string
}

// gateLines 把认得的门槛拼成给用户看的说明。没有认得的字段时返回空，
// 界面不会凭空写出「需要 0 金币」这种假信息。
func (r restrictedInfo) gateLines() []string {
	var out []string
	if r.LevelKnown {
		out = append(out, fmt.Sprintf("需要等级 %d", r.Level))
	}
	if r.AttrKnown {
		name := r.AttrName
		if name == "" {
			name = "属性值"
		}
		out = append(out, fmt.Sprintf("需要%s达到 %d", name, r.AttrValue))
	}
	if r.CoinsKnown {
		out = append(out, fmt.Sprintf("需要 %d 金币解锁", r.Coins))
	}
	return out
}

// auctionRow 是竞拍榜单 / 出价记录里的一行。
type auctionRow struct {
	Name       string
	Coins      int
	CoinsKnown bool
	At         string
}

// auctionInfo 是竞拍式受限内容的当前状态。
type auctionInfo struct {
	Known         bool
	Price         int
	PriceKnown    bool
	EndAt         string
	RemainSeconds int
	RemainKnown   bool
	Top           []auctionRow
}

// auctionBids 是出价记录。Known 为 false 表示响应里没认出列表字段，
// 不能当成「没有人出价」。
type auctionBids struct {
	Known bool
	Rows  []auctionRow
}

// ---- 解码 ----

// restrictedObject 把文档没固定下来的外层包装剥掉，外层已有的字段优先。
func restrictedObject(d json.RawMessage) object {
	o := asObject(d)
	if o == nil {
		return nil
	}
	overlay(o, "restricted", "detail", "post", "topic", "data")
	return o
}

// restrictedInt 按顺序在几层对象里找整数字段。用存在性而不是非零值判断
// Known，这样价格或等级真的是 0 时也不会被当成「服务端没给」。
func restrictedInt(objs []object, keys ...string) (int, bool) {
	for _, o := range objs {
		if o == nil {
			continue
		}
		for _, k := range keys {
			raw, ok := o[k]
			if !ok || strings.TrimSpace(string(raw)) == "null" {
				continue
			}
			var n int
			if json.Unmarshal(raw, &n) == nil {
				return n, true
			}
			var f float64
			if json.Unmarshal(raw, &f) == nil {
				return int(f), true
			}
		}
	}
	return 0, false
}

// decodeRestrictedObject 解详情里的 extra.restricted，也解 GET /restricted 的 d。
// 有这个对象本身就说明帖子有门槛（Known），即使字段一个也不认得。
func decodeRestrictedObject(o object) restrictedInfo {
	if o == nil {
		return restrictedInfo{}
	}
	r := restrictedInfo{
		Known:      true,
		AccessType: o.str("access_type", "restricted_access_type", "type"),
	}
	for _, k := range []string{"unlocked", "is_unlocked", "has_unlocked", "accessible", "can_view"} {
		if raw, ok := o[k]; ok {
			var b bool
			if json.Unmarshal(raw, &b) == nil {
				r.UnlockKnown, r.Unlocked = true, b
				break
			}
		}
	}
	for _, k := range []string{"content", "restricted_content", "hidden_content", "body", "text", "markdown"} {
		if s := cleanText(o.str(k)); s != "" {
			r.Content, r.ContentKnown = s, true
			break
		}
	}
	// 门槛可能平铺，也可能包在一层 gate / requirement 里。
	gate := o
	if g := o.sub("gate", "requirement", "requirements", "threshold", "condition",
		"unlock_condition", "restricted_gate"); g != nil {
		gate = g
	}
	r.Coins, r.CoinsKnown = restrictedInt([]object{gate, o},
		"coins", "required_coins", "unlock_coins", "coin", "price", "cost")
	r.Level, r.LevelKnown = restrictedInt([]object{gate, o},
		"level", "required_level", "min_level", "level_required")
	if a := gate.sub("attribute", "attr", "required_attribute", "required_attr"); a != nil {
		r.AttrName = cleanText(a.str("name", "key", "title"))
		r.AttrValue, r.AttrKnown = restrictedInt([]object{a}, "value", "min", "level", "required")
	} else {
		r.AttrName = cleanText(gate.str("attribute", "attr", "required_attribute", "required_attr", "attribute_name"))
		r.AttrValue, r.AttrKnown = restrictedInt([]object{gate, o},
			"attribute_value", "attr_value", "required_attribute_value", "attribute_level")
	}
	for _, key := range []string{"attributes", "required_attributes", "attrs"} {
		for _, raw := range rawArray(gate[key]) {
			a := asObject(raw)
			if a == nil {
				continue
			}
			name := cleanText(a.str("name", "key", "title"))
			value, ok := restrictedInt([]object{a}, "value", "min", "level", "required")
			if name != "" || ok {
				r.AttrName, r.AttrValue, r.AttrKnown = name, value, true
				break
			}
		}
		if r.AttrKnown {
			break
		}
	}
	// 未解锁时不会返回正文：有正文就足以说明已经解锁。
	if !r.UnlockKnown && r.ContentKnown {
		r.UnlockKnown, r.Unlocked = true, true
	}
	return r
}

func decodeRestricted(d json.RawMessage) restrictedInfo {
	return decodeRestrictedObject(restrictedObject(d))
}

// decodeTopicRestricted 取详情里的门槛。这里要兼容两代响应：
//
//   - 当前 api-read.md：门槛字段扁平地写在 extra 下（restricted_access_type /
//     has_restricted_hidden_body / restricted_d20_* / restricted_auction_*），
//     extra 里没有一个叫 restricted 的对象；
//   - 旧响应与早期测试：门槛包在 extra.restricted（或顶层 restricted）对象里。
//
// 两代都可能同时出现，嵌套对象优先；都没有时把扁平字段拼成一个对象，再交给
// decodeRestrictedObject 统一解。只要认出门槛信号就返回非 nil —— restrictedSection
// 用这个值决定要不要画整块，画出来之后竞拍状态 / 出价记录才有按钮可点。
func decodeTopicRestricted(o object) *restrictedInfo {
	var gate object
	if extra := o.sub("extra"); extra != nil {
		gate = extra.sub("restricted")
	}
	if gate == nil {
		gate = o.sub("restricted")
	}
	if gate == nil {
		gate = flatRestrictedGate(o)
	}
	if gate == nil {
		return nil
	}
	r := decodeRestrictedObject(gate)
	return &r
}

// flatRestrictedGate 把 extra 下扁平的 restricted_* 字段拼成一个门槛对象。
// 没有门槛信号（access_type 为空 / null，且没有 has_hidden_body）时返回 nil，
// 让普通帖子不会多出一块空的受限内容。字段能读多少读多少：文档没给出的
// auction 子字段不猜，留给 /restricted/auction 端点。
func flatRestrictedGate(o object) object {
	extra := o
	if e := o.sub("extra"); e != nil {
		extra = e
	}
	access := cleanText(extra.str("restricted_access_type", "access_type"))
	hasHidden := extra.flag("has_restricted_hidden_body")
	if access == "" && !hasHidden {
		return nil
	}
	gate := object{}
	if access != "" {
		gate["restricted_access_type"] = json.RawMessage(strconv.Quote(access))
	}
	// D20 检定门槛 → decodeRestrictedObject 认的属性名 / 阈值。
	if raw, ok := extra["restricted_d20_attr"]; ok && strings.TrimSpace(string(raw)) != "null" {
		gate["attribute_name"] = raw
	}
	if raw, ok := extra["restricted_d20_threshold"]; ok && strings.TrimSpace(string(raw)) != "null" {
		gate["attribute_value"] = raw
	}
	return gate
}

// decodeAuctionRows 认一个出价 / 榜单数组。行里的昵称可能被包在
// user / bidder 对象里，金额也可能是 amount / bid / price。
func decodeAuctionRows(raw json.RawMessage) []auctionRow {
	list := rawArray(raw)
	if len(list) == 0 {
		if o := asObject(raw); o != nil {
			for _, k := range []string{"bids", "items", "list", "records", "rows", "bidders", "data"} {
				if list = rawArray(o[k]); len(list) > 0 {
					break
				}
			}
		}
	}
	var rows []auctionRow
	for _, raw := range list {
		o := asObject(raw)
		if o == nil {
			continue
		}
		name := cleanText(o.str("username", "bidder", "name", "nickname", "user_name"))
		if name == "" {
			if user := o.sub("user", "bidder", "author"); user != nil {
				name = cleanText(user.str("username", "name", "nickname"))
			}
		}
		coins, hasCoins := restrictedInt([]object{o}, "coins", "amount", "price", "bid", "bid_coins")
		at := o.str("created_at", "bid_at", "time", "at")
		if name == "" && at == "" && !hasCoins {
			continue
		}
		rows = append(rows, auctionRow{Name: name, Coins: coins, CoinsKnown: hasCoins, At: at})
	}
	return rows
}

func decodeRestrictedAuction(d json.RawMessage) auctionInfo {
	o := restrictedObject(d)
	if o == nil {
		return auctionInfo{}
	}
	a := auctionInfo{
		Known: true,
		EndAt: o.str("end_at", "ends_at", "auction_end_at", "deadline", "expire_at"),
	}
	a.Price, a.PriceKnown = restrictedInt([]object{o},
		"current_price", "price", "current_bid", "highest_bid", "coins", "bid_coins")
	a.RemainSeconds, a.RemainKnown = restrictedInt([]object{o},
		"remaining_seconds", "remain_seconds", "seconds_left", "time_left", "remaining")
	for _, key := range []string{"top", "top_bidders", "leaderboard", "leaders", "bidders", "ranking"} {
		if raw, ok := o[key]; ok {
			if rows := decodeAuctionRows(raw); len(rows) > 0 {
				a.Top = rows
				break
			}
		}
	}
	return a
}

func decodeRestrictedBids(d json.RawMessage) auctionBids {
	if rawArray(d) != nil {
		return auctionBids{Known: true, Rows: decodeAuctionRows(d)}
	}
	o := restrictedObject(d)
	if o == nil {
		return auctionBids{}
	}
	for _, key := range []string{"bids", "items", "list", "records", "history", "data"} {
		if raw, ok := o[key]; ok {
			rows := decodeAuctionRows(raw)
			if len(rows) > 0 || rawArray(raw) != nil {
				return auctionBids{Known: true, Rows: rows}
			}
		}
	}
	// 没认出列表字段时不要声称「没有出价」，让界面不显示这一块。
	return auctionBids{}
}

// ---- 请求 ----

func (c *Client) fetchRestricted(ctx context.Context, shortID string) (restrictedInfo, error) {
	return withAuthRetry(c, ctx, func(token string) (restrictedInfo, error) {
		d, err := c.getJSON(ctx, c.apiURL(topicRestrictedPath(shortID)), token)
		if err != nil {
			return restrictedInfo{}, err
		}
		return decodeRestricted(d), nil
	})
}

func (c *Client) fetchRestrictedAuction(ctx context.Context, shortID string) (auctionInfo, error) {
	return withAuthRetry(c, ctx, func(token string) (auctionInfo, error) {
		d, err := c.getJSON(ctx, c.apiURL(topicRestrictedAuctionPath(shortID)), token)
		if err != nil {
			return auctionInfo{}, err
		}
		return decodeRestrictedAuction(d), nil
	})
}

func (c *Client) fetchRestrictedBids(ctx context.Context, shortID string) (auctionBids, error) {
	return withAuthRetry(c, ctx, func(token string) (auctionBids, error) {
		d, err := c.getJSON(ctx, c.apiURL(topicRestrictedBidsPath(shortID)), token)
		if err != nil {
			return auctionBids{}, err
		}
		return decodeRestrictedBids(d), nil
	})
}

// unlockRestricted 花金币解锁。文档明确「无请求体」，所以 payload 传 nil；
// key 由调用方给，同一笔解锁重试必须复用。
func (c *Client) unlockRestricted(ctx context.Context, key, shortID string) (restrictedInfo, error) {
	return withAuthRetry(c, ctx, func(token string) (restrictedInfo, error) {
		d, err := c.postJSON(ctx, c.apiURL(topicRestrictedUnlockPath(shortID)), token, key, nil)
		if err != nil {
			return restrictedInfo{}, err
		}
		return decodeRestricted(d), nil
	})
}

// ---- 界面状态与动作 ----

// restrictedLoad 是一次只读查询的状态。三块（info / auction / bids）各自一份，
// 详情打开时会并发自动拉取，互不覆盖；gen 丢弃切帖子之前的迟到结果。
type restrictedLoad struct {
	loading bool
	err     string
	gen     uint64
}

// restrictedUIState 只在 UI 线程读写。info / auction / bids 是三次只读查询各自
// 的结果，可以同时留在页面上：详情打开时自动并发拉取，读失败的原因留在块里
// 可重试。confirm 是「已展开金额确认面板」，submit 会再查一次它。
type restrictedUIState struct {
	info    *restrictedInfo
	auction *auctionInfo
	bids    *auctionBids
	loads   map[string]*restrictedLoad // info / auction / bids
	auto    bool                       // 当前详情是否已发起过自动拉取

	confirm bool
	busy    bool
	key     string
	sig     string
	gen     uint64
}

func (s *restrictedUIState) load(kind string) *restrictedLoad {
	if s.loads == nil {
		s.loads = make(map[string]*restrictedLoad)
	}
	l := s.loads[kind]
	if l == nil {
		l = &restrictedLoad{}
		s.loads[kind] = l
	}
	return l
}

// anyLoading 供界面判断「是否还有请求在跑」，用来禁用三个只读按钮。
func (s *restrictedUIState) anyLoading() bool {
	for _, l := range s.loads {
		if l.loading {
			return true
		}
	}
	return false
}

// restrictedUnlocked 以最近一次读取 / 解锁的结果为准；详情里的门槛是旧副本，
// 解锁成功后只有 d.restricted.info 会带上新的解锁状态。
func restrictedUnlocked(d *detailState) bool {
	if d == nil || d.topic == nil || d.topic.Restricted == nil {
		return false
	}
	if info := d.restricted.info; info != nil && info.UnlockKnown {
		return info.Unlocked
	}
	gate := d.topic.Restricted
	return gate.UnlockKnown && gate.Unlocked
}

// autoLoadRestricted 在详情正文拿到、且确实有门槛时调一次，把三块只读内容
// 并发拉回来直接展示。三块共用一次触发：切到别的帖子 / 刷新详情会换新的
// detailState 或复用同一状态，auto 标志保证不会重复发起。
//
// 这些端点需开通、默认不授予，所以自动模式刻意「安静」：未登录不发请求，
// 403 insufficient_scope 不弹授权弹窗（手动点按钮时才走完整授权链路）。
// 失败原因留在对应块里，可单独重试。
func (a *app) autoLoadRestricted(d *detailState) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.topic.Restricted == nil || !a.autoLoad {
		return
	}
	if d.restricted.auto {
		return
	}
	d.restricted.auto = true
	for _, kind := range []string{"info", "auction", "bids"} {
		a.loadRestricted(d, kind, true)
	}
}

// loadRestricted 读一次受限内容 / 竞拍状态 / 出价记录。auto 为真时是详情打开
// 后的静默自动拉取：未登录跳过、缺 scope 不弹授权；手动点击时为 false，沿用
// 登录 / 授权链路。读取失败的原因留在对应块里并可重试，不弹 toast。
func (a *app) loadRestricted(d *detailState, kind string, auto bool) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.topic.Restricted == nil || !a.autoLoad {
		return
	}
	st := d.restricted.load(kind)
	if st.loading || d.restricted.busy {
		return
	}
	if !a.auth.loggedIn() {
		// 自动拉取不该把登录框弹到正在读帖的人面前；手动点击才要求登录。
		if auto {
			return
		}
		a.requestLogin("登录后才能查看受限内容。", func() {
			if a.detailLive(d) {
				a.loadRestricted(d, kind, false)
			}
		})
		return
	}
	st.loading = true
	st.err = ""
	st.gen++
	gen := st.gen
	shortID := d.shortID
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), restrictedTimeout)
	go func() {
		defer cancel()
		var err error
		var info restrictedInfo
		var auction auctionInfo
		var bids auctionBids
		switch kind {
		case "info":
			info, err = a.client.fetchRestricted(ctx, shortID)
		case "auction":
			auction, err = a.client.fetchRestrictedAuction(ctx, shortID)
		case "bids":
			bids, err = a.client.fetchRestrictedBids(ctx, shortID)
		}
		a.publish(func() {
			if !a.detailLive(d) || st.gen != gen {
				return
			}
			st.loading = false
			// 请求期间换了账号 / 退出登录，结果不属于当前会话。
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				// 自动拉取遇到缺 scope 只是「未开通」，不弹授权打断阅读；
				// 手动点按钮时才交给授权链路补权限并重试。
				if !auto && a.handleAuthError(err, func() {
					if a.detailLive(d) {
						a.loadRestricted(d, kind, false)
					}
				}) {
					return
				}
				st.err = friendlyError(err)
				return
			}
			switch kind {
			case "info":
				r := info
				d.restricted.info = &r
			case "auction":
				v := auction
				d.restricted.auction = &v
			case "bids":
				v := bids
				d.restricted.bids = &v
			}
		})
	}()
}

// submitRestrictedUnlock 提交一次解锁。terms.md 要求在扣币前让用户明确
// 确认操作与金额，所以没有先打开确认弹窗时这里直接拒绝，不发出请求。
func (a *app) submitRestrictedUnlock(d *detailState) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.topic.Restricted == nil {
		return
	}
	if !d.restricted.confirm || d.restricted.busy {
		return
	}
	gate := d.topic.Restricted
	if !gate.CoinsKnown || gate.Coins <= 0 {
		a.toast(a.detailToastFor(d), "服务端没有给出解锁价格，请到原文查看")
		return
	}
	if restrictedUnlocked(d) {
		a.toast(a.detailToastFor(d), "这篇内容已经解锁")
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能解锁受限内容。", func() {
			if a.detailLive(d) {
				a.submitRestrictedUnlock(d)
			}
		})
		return
	}
	// 同一笔解锁（同一帖子、同一价格）重试复用幂等键；成功了就清空，
	// 再点一次是另一笔，换新键。
	price := gate.Coins
	sig := fmt.Sprintf("%s|%d", d.shortID, price)
	if d.restricted.key == "" || d.restricted.sig != sig {
		d.restricted.key = randomURLSafe(24)
		d.restricted.sig = sig
	}
	key := d.restricted.key
	d.restricted.busy = true
	d.restricted.gen++
	gen := d.restricted.gen
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), restrictedTimeout)
	go func() {
		defer cancel()
		res, err := a.client.unlockRestricted(ctx, key, d.shortID)
		a.publish(func() {
			if !a.detailLive(d) || d.restricted.gen != gen {
				return
			}
			d.restricted.busy = false
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				// 授权后重放还是这一笔，confirm 与 key 都留着。
				if a.handleAuthError(err, func() {
					if a.detailLive(d) {
						a.submitRestrictedUnlock(d)
					}
				}) {
					return
				}
				a.toast(a.detailToastFor(d), friendlyError(err))
				return
			}
			d.restricted.key = ""
			d.restricted.sig = ""
			d.restricted.confirm = false
			if res.Known {
				d.restricted.info = &res
			} else {
				// 解锁响应可能只回一个空信封；门槛信息仍在本帖详情里，
				// 本地记成已解锁，不拿空响应覆盖已知的价格与等级。
				local := *gate
				local.Unlocked, local.UnlockKnown = true, true
				d.restricted.info = &local
			}
			a.toast(a.detailToastFor(d), fmt.Sprintf("已花 %d 金币解锁", price))
		})
	}()
}
