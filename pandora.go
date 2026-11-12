package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 潘多拉盲盒的数据层与动作。
//
// 盲盒是站点每天发放一次的随机事件：先开一次，再按服务端给的重抽次数重抽。
// 这个应用把「今天的抽奖」明确做成两次，并提供独立窗口展示：
//
//	GET  /v1/pandora/today   今日状态 + 当前事件（scope read:economy，需开通）
//	POST /v1/pandora/open    开盲盒，无请求体（scope write:economy，需开通）🔑
//	POST /v1/pandora/reroll  重抽，body 是 box_instance_id（同上）🔑
//
// 两个写端点都标 🔑：每次用户动作铸一个新的 Idempotency-Key，超时重试沿用
// 同一个键；授权链路重跑时是一次新的动作，换新键。响应字段没有在文档里逐项
// 列出，按「认得的字段用、不认识的忽略」解码，并兼容「完整今日状态」与
// 「只返回一个 box」两种形状。
//
// 站点的 reroll_limit 可能大于 1，但本应用的产品约定是「开一次 + 重抽一次」
// 共两次：见 pandoraStatus.canReroll。多出来的重抽次数留给站点，不在应用里
// 提供第三个入口。

const (
	pandoraOpenPath   = "/v1/pandora/open"
	pandoraRerollPath = "/v1/pandora/reroll"

	// pandoraActionTimeout 是一次开盒 / 重抽的超时。写操作会改变金币与站内
	// 状态，给得比普通读稍宽，但别让按钮一直转圈。
	pandoraActionTimeout = 25 * time.Second
)

// pandoraNow 返回「现在」，测试可以替换。时间按东八区解释，与接口日期一致。
var pandoraNow = func() time.Time { return coinNow() }

// pandoraBox 是今日盲盒本体的解码结果。字段按线上响应实测；文档只列了其中
// 一部分，多出来的 tier_code / apply_at 一并认。
type pandoraBox struct {
	InstanceID string
	EventCode  string
	EventName  string
	// EventTitle 是站点已经拼好的一句话结果，例如「小小金币雨，获得 67 金币。」。
	EventTitle string
	TierCode   string
	Rarity     string
	// RolledValue / RolledKnown 分开存：rolled_value 可能是 null，null 不能
	// 当成 0 展示。
	RolledValue float64
	RolledKnown bool
	OpenAt      string
	ApplyAt     string
	ExpireAt    string
	// RerollDeadlineAt 是重抽截止时间（UTC），展示时转东八区。
	RerollDeadlineAt string
	RerollUsed       int
	RerollLimit      int
	// CountersKnown 表示响应里真的带了重抽计数：动作响应只返回 box 时，
	// 缺计数就在本地按「用掉一次」补。
	CountersKnown     bool
	ForcePurpleReroll bool
}

func (b pandoraBox) known() bool {
	return b.InstanceID != "" || b.EventCode != "" || b.EventName != "" ||
		b.EventTitle != "" || b.Rarity != "" || b.TierCode != ""
}

// pandoraStatus 是 GET /v1/pandora/today 的解码结果。
type pandoraStatus struct {
	// Known 表示这份响应能用来画界面（有今日状态或至少有一个 box）。
	Known bool
	// FlagsKnown 单独记：动作响应可能只带 box，此时不能拿零值覆盖今日状态。
	FlagsKnown     bool
	HasBox         bool
	CanOpen        bool
	OpenedToday    bool
	ActiveExtended bool
	NextOpenAt     string
	Box            pandoraBox
	BoxKnown       bool
}

// 本应用每天只有两次抽奖：开启盲盒算第一次，重抽算第二次。
func (s pandoraStatus) drawsUsed() int {
	if !s.OpenedToday {
		return 0
	}
	if s.BoxKnown && s.Box.RerollUsed > 0 {
		return 2
	}
	return 1
}

// canReroll 回答「还能不能抽第二次」。除了服务端条件，这里硬性只放行一次
// 重抽：reroll_used 一旦大于 0，今天的两次抽奖就结束了。
func (s pandoraStatus) canReroll(now time.Time) bool {
	if !s.BoxKnown || !s.HasBox || !s.OpenedToday {
		return false
	}
	if s.Box.RerollUsed > 0 || s.Box.RerollLimit <= 0 {
		return false
	}
	if b := s.Box; b.RerollDeadlineAt != "" {
		if t, ok := profileTime(b.RerollDeadlineAt); ok && !now.Before(t) {
			return false
		}
	}
	if b := s.Box; b.ExpireAt != "" {
		if t, ok := profileTime(b.ExpireAt); ok && !now.Before(t) {
			return false
		}
	}
	return true
}

// readyToDraw 表示窗口或标题栏入口需要提醒用户「今天还有一次能抽」。
func (s pandoraStatus) readyToDraw(now time.Time) bool { return s.CanOpen || s.canReroll(now) }

// pandoraDisplayStatus 回答标题栏入口该看哪一份状态：窗口完整加载过就用
// status；否则用登录 / 启动时静默读到的 entryStatus。
func pandoraDisplayStatus(st *pandoraState) (pandoraStatus, bool) {
	if st == nil {
		return pandoraStatus{}, false
	}
	if st.loaded || st.status.Known {
		return st.status, true
	}
	if st.entryKnown {
		return st.entryStatus, true
	}
	return pandoraStatus{}, false
}

// pandoraActiveEvent 返回仍在有效期内、且有名字的潘多拉事件。首页展示的是
// 这个事件的名字，悬浮展示的是它的具体效果（event_title）。
func pandoraActiveEvent(status pandoraStatus, known bool) (pandoraBox, bool) {
	if !known || !status.BoxKnown {
		return pandoraBox{}, false
	}
	box := status.Box
	if strings.TrimSpace(box.EventName) == "" {
		return pandoraBox{}, false
	}
	if box.ExpireAt != "" {
		if t, ok := profileTime(box.ExpireAt); ok && !pandoraNow().Before(t) {
			return pandoraBox{}, false
		}
	}
	return box, true
}

// pandoraEntryName 是标题栏入口的可见文字：抽中事件后直接显示事件名，
// 没有进行中的事件时才显示「潘多拉」。
func pandoraEntryName(st *pandoraState) string {
	if box, ok := pandoraActiveEvent(pandoraDisplayStatus(st)); ok {
		return strings.TrimSpace(box.EventName)
	}
	return "潘多拉"
}

// pandoraEntryHint 是入口的悬浮说明：事件名 + 具体效果 + 剩余时间。没有
// 进行中的事件时退回抽奖状态说明。
func pandoraEntryHint(st *pandoraState) string {
	status, known := pandoraDisplayStatus(st)
	if box, ok := pandoraActiveEvent(status, known); ok {
		name := strings.TrimSpace(box.EventName)
		effect := strings.TrimSpace(box.EventTitle)
		hint := "潘多拉「" + name + "」"
		if effect != "" && effect != name {
			hint += "：" + effect
		}
		if remain := pandoraRemainText(box.ExpireAt, pandoraNow()); remain != "" {
			hint += "（" + remain + "）"
		}
		return hint + "；点击打开潘多拉"
	}
	if known && status.readyToDraw(pandoraNow()) {
		return "潘多拉盲盒：今天还能抽一次，点击打开"
	}
	if known && status.OpenedToday {
		return "潘多拉盲盒：今天的抽奖已完成，点击查看"
	}
	return "打开潘多拉盲盒"
}

// ---- 解码 ----

func decodePandoraStatus(d json.RawMessage) pandoraStatus {
	o := asObject(d)
	if o == nil {
		return pandoraStatus{}
	}
	out := pandoraStatus{
		FlagsKnown:     o.has("has_box", "can_open", "opened_today", "next_open_at", "active_extended"),
		HasBox:         o.flag("has_box", "hasBox"),
		CanOpen:        o.flag("can_open", "canOpen"),
		OpenedToday:    o.flag("opened_today", "openedToday"),
		ActiveExtended: o.flag("active_extended", "activeExtended"),
		NextOpenAt:     o.str("next_open_at", "nextOpenAt"),
	}
	if box := o.sub("box"); box != nil {
		out.Box = decodePandoraBox(box)
		out.BoxKnown = out.Box.known()
	}
	// 动作响应可能直接把 box 的字段铺在顶层；同形的 box 对象是更常见的
	// 形状，两种都认。
	if !out.BoxKnown {
		if box := decodePandoraBox(o); box.known() {
			out.Box, out.BoxKnown = box, true
		}
	}
	out.Known = out.FlagsKnown || out.BoxKnown
	return out
}

func decodePandoraBox(o object) pandoraBox {
	if o == nil {
		return pandoraBox{}
	}
	b := pandoraBox{
		InstanceID:        o.str("box_instance_id", "boxInstanceId", "instance_id"),
		EventCode:         o.str("event_code", "eventCode"),
		EventName:         o.str("event_name", "eventName"),
		EventTitle:        o.str("event_title", "eventTitle"),
		TierCode:          o.str("tier_code", "tierCode"),
		Rarity:            o.str("rarity"),
		OpenAt:            o.str("open_at", "openAt"),
		ApplyAt:           o.str("apply_at", "applyAt"),
		ExpireAt:          o.str("expire_at", "expireAt"),
		RerollDeadlineAt:  o.str("reroll_deadline_at", "rerollDeadlineAt"),
		RerollUsed:        o.num("reroll_used", "rerollUsed"),
		RerollLimit:       o.num("reroll_limit", "rerollLimit"),
		CountersKnown:     o.has("reroll_used", "reroll_limit"),
		ForcePurpleReroll: o.flag("force_purple_reroll", "forcePurpleReroll"),
	}
	if v, ok := o.number("rolled_value", "rolledValue"); ok {
		b.RolledValue, b.RolledKnown = v, true
	}
	return b
}

// pandoraActionResult 是一次开盒 / 重抽的解码结果：状态之外还可能带本次
// 扣费与服务端文案，能展示的就展示，没有的不编。
type pandoraActionResult struct {
	Status    pandoraStatus
	Cost      float64
	CostKnown bool
	Message   string
}

func decodePandoraAction(d json.RawMessage) pandoraActionResult {
	out := pandoraActionResult{Status: decodePandoraStatus(d)}
	o := asObject(d)
	if o == nil {
		return out
	}
	if v, ok := o.number("cost", "cost_coins", "costCoins", "coin_cost"); ok {
		out.Cost, out.CostKnown = v, true
	} else if o.has("cost", "cost_coins", "costCoins", "coin_cost") {
		out.CostKnown = true
	}
	out.Message = o.str("message", "msg")
	return out
}

// applyPandoraAction 把动作响应并进当前状态。动作响应可能只是「一个新的
// box」，此时保留旧状态里的日期与标志，再按动作把「今天已经抽过」落定。
func applyPandoraAction(prev, got pandoraStatus, action string) pandoraStatus {
	next := got
	if !next.FlagsKnown {
		next.FlagsKnown = prev.FlagsKnown
		next.HasBox = prev.HasBox
		next.CanOpen = prev.CanOpen
		next.OpenedToday = prev.OpenedToday
		next.ActiveExtended = prev.ActiveExtended
		next.NextOpenAt = prev.NextOpenAt
	}
	if !next.BoxKnown {
		next.Box, next.BoxKnown = prev.Box, prev.BoxKnown
	}
	next.Known = true
	next.HasBox, next.OpenedToday, next.CanOpen = true, true, false
	if action == "reroll" && next.BoxKnown && !next.Box.CountersKnown && prev.BoxKnown {
		// 只返回 box 的重抽响应没带计数：按「用掉一次」补上，按钮立刻消失，
		// 不必等下一次 GET 把界面改回来。
		next.Box.RerollUsed = prev.Box.RerollUsed + 1
		next.Box.RerollLimit = prev.Box.RerollLimit
		next.Box.CountersKnown = true
	}
	return next
}

// ---- 请求 ----

func (c *Client) fetchPandora(ctx context.Context) (pandoraStatus, error) {
	d, err := c.coinGET(ctx, pandoraTodayPath, nil)
	if err != nil {
		return pandoraStatus{}, err
	}
	return decodePandoraStatus(d), nil
}

// openPandora 开今日盲盒。无请求体，但按文档必须带 Idempotency-Key。
func (c *Client) openPandora(ctx context.Context, idempotencyKey string) (pandoraActionResult, error) {
	var out pandoraActionResult
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		d, err := c.postJSON(ctx, c.base()+pandoraOpenPath, token, idempotencyKey, nil)
		if err != nil {
			return struct{}{}, err
		}
		out = decodePandoraAction(d)
		return struct{}{}, nil
	})
	return out, err
}

// rerollPandora 重抽当前盲盒。box_instance_id 来自 today 或 open 的响应，
// 不能自己编。
func (c *Client) rerollPandora(ctx context.Context, idempotencyKey, instanceID string) (pandoraActionResult, error) {
	var out pandoraActionResult
	payload := map[string]any{"box_instance_id": instanceID}
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		d, err := c.postJSON(ctx, c.base()+pandoraRerollPath, token, idempotencyKey, payload)
		if err != nil {
			return struct{}{}, err
		}
		out = decodePandoraAction(d)
		return struct{}{}, nil
	})
	return out, err
}

// ---- 界面状态 ----

// pandoraState 是潘多拉窗口的界面状态，只在 UI 线程读写。
type pandoraState struct {
	seq     uint64
	loading bool
	loaded  bool
	// wait 表示上次失败正在等全局授权补权限，避免每帧重发同一个 403。
	wait bool
	err  string

	status pandoraStatus

	// entryStatus 是登录 / 启动时静默读到的今日状态，只给标题栏入口用：
	// 用户还没打开潘多拉窗口时，首页也要能直接显示事件名与悬浮说明。窗口
	// 打开后会重新读一份完整的 status，不用这份兜底数据。
	entryStatus  pandoraStatus
	entryKnown   bool
	entryLoading bool

	// busy 是正在提交的动作："open" / "reroll"；按钮据此禁用并显示进度。
	busy string

	// owner 是这份状态属于哪个账号。重新授权（同一个账号补权限）时保留状态，
	// 重抽才能接着用原来的 box_instance_id 重跑；换账号 / 退出则整体丢弃。
	owner string
}

func newPandoraState() *pandoraState { return &pandoraState{} }

func (a *app) ensurePandoraLoaded() {
	st := a.pandora
	if st == nil || !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	if st.loading || st.loaded || st.wait {
		return
	}
	a.loadPandoraStatus()
}

// loadPandoraEntry 是登录 / 启动时的静默读取：只填标题栏入口要用的
// entryStatus，不弹授权、不显示错误，也不占窗口的 status。失败时入口保持
// 「潘多拉」，用户点开窗口时会走正式加载并把原因展示出来。
func (a *app) loadPandoraEntry() {
	st := a.pandora
	if st == nil || !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	if st.loading || st.loaded || st.entryLoading {
		return
	}
	st.entryLoading = true
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		status, err := a.client.fetchPandora(ctx)
		a.publish(func() {
			if a.pandora != st {
				return
			}
			st.entryLoading = false
			if err != nil {
				return
			}
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			st.entryStatus = status
			st.entryKnown = status.Known
		})
	}()
}

func (a *app) loadPandoraStatus() {
	st := a.pandora
	if st == nil {
		return
	}
	st.seq++
	seq := st.seq
	st.loading = true
	st.err = ""
	st.owner = a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		status, err := a.client.fetchPandora(ctx)
		a.publish(func() {
			if a.pandora != st || st.seq != seq {
				return
			}
			st.loading = false
			if err != nil {
				if a.handleAuthError(err, a.reloadPandora) {
					st.wait = true
					return
				}
				st.loaded = true
				st.err = friendlyError(err)
				return
			}
			st.loaded = true
			st.wait = false
			st.status = status
			st.entryStatus = status
			st.entryKnown = status.Known
			st.err = ""
		})
	}()
}

// reloadPandora 丢掉当前状态后重读：刷新按钮、授权重跑与动作成功后的校准
// 都走这里。只在 UI 线程调用。
func (a *app) reloadPandora() {
	st := a.pandora
	if st == nil {
		return
	}
	st.seq++
	st.loading = false
	st.loaded = false
	st.wait = false
	st.err = ""
	a.loadPandoraStatus()
}

// pandoraAction 是开盒 / 重抽共用的提交路径。kind 只有 "open" 与 "reroll"。
func (a *app) pandoraAction(kind string) {
	st := a.pandora
	if st == nil || st.busy != "" {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录 2Libra 后才能抽取潘多拉盲盒。", func() { a.pandoraAction(kind) })
		return
	}
	switch kind {
	case "open":
		if st.status.Known && !st.status.CanOpen {
			a.toast(toastPandora, "今天的盲盒已经开过了，明天再来")
			return
		}
	case "reroll":
		if !st.status.canReroll(pandoraNow()) {
			a.toast(toastPandora, "今天没有可用的重抽次数了")
			return
		}
		if st.status.Box.InstanceID == "" {
			a.toast(toastPandora, "还没拿到这个盲盒的编号，请先刷新")
			return
		}
	}
	st.busy = kind
	actor := a.auth.username()
	st.owner = actor
	instance := st.status.Box.InstanceID
	key := randomURLSafe(24)
	ctx, cancel := context.WithTimeout(context.Background(), pandoraActionTimeout)
	go func() {
		defer cancel()
		var res pandoraActionResult
		var err error
		if kind == "reroll" {
			res, err = a.client.rerollPandora(ctx, key, instance)
		} else {
			res, err = a.client.openPandora(ctx, key)
		}
		a.publish(func() {
			if a.pandora != st {
				return
			}
			st.busy = ""
			// 请求期间换了账号 / 退出了登录：这次结果不属于当前会话。
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				var ae *apiError
				if errors.As(err, &ae) && ae.Code == "conflict" {
					// 今日已开 / 重抽次数已用完：服务端拒绝，按「已经抽过」处理。
					a.toast(toastPandora, "这次抽奖已经用过了，明天再来")
					a.reloadPandora()
					return
				}
				if a.handleAuthError(err, func() { a.pandoraAction(kind) }) {
					return
				}
				a.toast(toastPandora, friendlyError(err))
				return
			}
			next := applyPandoraAction(st.status, res.Status, kind)
			st.status = next
			st.entryStatus = next
			st.entryKnown = true
			st.loaded, st.wait, st.err = true, false, ""
			text := pandoraActionText(kind, res, next)
			a.toast(toastPandora, text)
			// 开盒 / 重抽可能改变金币与流水：交给全局金币事件统一刷新。
			a.coinsChanged(coinEventPandora)
			// 动作响应不一定带完整今日状态，再读一次 today 校准。直接调
			// loadPandoraStatus 而不是 reloadPandora：保留刚抽到的结果，
			// 刷新期间不把卡片退回「正在读取」。
			a.loadPandoraStatus()
		})
	}()
}

func (a *app) openPandoraBox()   { a.pandoraAction("open") }
func (a *app) rerollPandoraBox() { a.pandoraAction("reroll") }

// ---- 展示辅助 ----

// pandoraActionText 是成功后的 toast：说清抽到了什么、花了多少、还能不能
// 再抽一次。金额只在响应明确给出且大于 0 时展示。
func pandoraActionText(kind string, res pandoraActionResult, status pandoraStatus) string {
	prefix := "盲盒已开启"
	if kind == "reroll" {
		prefix = "已再抽一次"
	}
	text := prefix
	if summary := pandoraBoxSummary(status.Box); summary != "" {
		text += "：" + summary
	}
	if res.CostKnown && res.Cost > 0 {
		text += "，消耗 " + coinAmountText(res.Cost) + " 金币"
	}
	if status.canReroll(pandoraNow()) {
		text += "；今天还能再抽一次"
	}
	return text
}

// pandoraBoxSummary 是一句短结果：稀有度 + 事件名 + 站点拼好的标题。
func pandoraBoxSummary(box pandoraBox) string {
	parts := make([]string, 0, 3)
	if label := pandoraRarityLabel(box.Rarity); label != "" {
		parts = append(parts, "【"+label+"】")
	}
	name := strings.TrimSpace(box.EventName)
	if name == "" {
		name = strings.TrimSpace(box.EventCode)
	}
	title := strings.TrimSpace(box.EventTitle)
	switch {
	case name != "" && title != "" && !strings.Contains(title, name):
		parts = append(parts, name+" · "+title)
	case title != "":
		parts = append(parts, title)
	case name != "":
		parts = append(parts, name)
	}
	if box.RolledKnown {
		parts = append(parts, "数值 "+coinAmountText(box.RolledValue))
	}
	return strings.Join(parts, " ")
}

func pandoraRarityLabel(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "bronze":
		return "青铜"
	case "silver":
		return "白银"
	case "gold":
		return "黄金"
	case "purple":
		return "紫色"
	}
	return ""
}

func pandoraRarityColor(t *ui.Theme, code string) ui.Color {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "purple":
		return ui.Hex("#a855f7")
	case "gold":
		return ui.Hex("#c9930a")
	case "silver":
		return ui.Hex("#7d8794")
	case "bronze":
		return ui.Hex("#b06a35")
	}
	return t.TextMuted
}

// pandoraClock 把接口的 UTC 时间转成东八区的「01-02 15:04」，解析不了就
// 原样返回。
func pandoraClock(raw string) string {
	t, ok := profileTime(raw)
	if !ok {
		return strings.TrimSpace(raw)
	}
	return t.In(coinNow().Location()).Format("01-02 15:04")
}

// pandoraWhenText 是「今天 / 明天 / 具体日期 + 时间」的写法，用于下一次
// 发放与截止时间。
func pandoraWhenText(raw string) string {
	t, ok := profileTime(raw)
	if !ok {
		return strings.TrimSpace(raw)
	}
	now := pandoraNow()
	local := t.In(now.Location())
	ly, lm, ld := local.Date()
	ty, tm, td := now.Date()
	tomorrow := now.AddDate(0, 0, 1)
	ny, nm, nd := tomorrow.Date()
	switch {
	case ly == ty && lm == tm && ld == td:
		return "今天 " + local.Format("15:04")
	case ly == ny && lm == nm && ld == nd:
		return "明天 " + local.Format("15:04")
	}
	return local.Format("01-02 15:04")
}

// pandoraRemainText 是「还剩多久」，用于事件到期与重抽截止。
func pandoraRemainText(raw string, now time.Time) string {
	t, ok := profileTime(raw)
	if !ok {
		return ""
	}
	d := t.Sub(now)
	if d <= 0 {
		return "已结束"
	}
	if d >= 24*time.Hour {
		days := int(d / (24 * time.Hour))
		hours := int((d % (24 * time.Hour)) / time.Hour)
		return fmt.Sprintf("还剩 %d 天 %d 小时", days, hours)
	}
	if d >= time.Hour {
		return fmt.Sprintf("还剩 %d 小时 %d 分", int(d/time.Hour), int((d%time.Hour)/time.Minute))
	}
	return fmt.Sprintf("还剩 %d 分钟", int(d/time.Minute)+1)
}
