package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 竞猜（api-economy.md）
//
//	GET  /v1/topics/:short_id/bets                        scope read:economy，需开通
//	POST /v1/topics/:short_id/bets/:option_id/participate 🔑 scope write:economy，需开通
//
// 两个端点都标「需开通」：默认不授予，未开通时返回 403 insufficient_scope，
// 交给 auth_gate.go 的授权链路引导补 scope，授权后由用户自己再点一次。写端点带 🔑，
// 必须带 Idempotency-Key；同一笔投注的超时重试复用同一个键，
// 金额或选项变了换新键，否则会重复扣币。
//
// 帖子详情 GET /v1/topics/:short_id 的 extra.bet（线上也可能在顶层 d.bet）
// 带 title / status / participate_deadline / options[]{id,text,total_coins} /
// settled_option_id，匿名可读，用来画竞猜块。GET /bets 额外带 settled_at 与
// 「当前用户参与情况」——文档没有给出这两个字段名，解码时对多种历史拼写
// 容错（见 decodeBetMine），不依赖 m 的中文文案。
//
// 参与只用到 options[].id（uuid）与用户输入的金额，不必先读 /bets；「我的
// 参与」是用户主动点开才读的一次只读查询（和投票结果分布一致），避免每次
// 打开带竞猜的帖子都发一个默认无权限的 403 请求。

const betTimeout = 20 * time.Second

// 文档没有给竞猜的投注金额范围（打赏是 1～500、金币池注资是 200～1000，
// 竞猜只写「金额必填，从余额扣除」）。这里只用一个宽松的软上限挡住误操作，
// 真正的范围校验交给服务端；不要把这个上限当成 API 契约。
const (
	betCoinsMin = 1
	betCoinsMax = 1000000
)

func topicBetsPath(shortID string) string {
	return topicPath(shortID) + "/bets"
}

func topicBetParticipatePath(shortID, optionID string) string {
	return topicPath(shortID) + "/bets/" + url.PathEscape(optionID) + "/participate"
}

// betOption 是一个竞猜选项：ID 是 uuid，参与时作为路径里的 option_id。
type betOption struct {
	ID         string
	Text       string
	TotalCoins int
}

// betMine 是当前用户的参与情况。文档没写字段名，Known 区分「确实没参与」
// 和「响应里没有这个字段」——后者不该在界面上断言「你还没投注」。
type betMine struct {
	Known    bool
	OptionID string
	Coins    int
}

// betInfo 是竞猜块的归一结果，详情里的 extra.bet 与 GET /bets 都解到这里。
type betInfo struct {
	Title               string
	Status              string
	ParticipateDeadline string
	Options             []betOption
	SettledOptionID     string
	SettledAt           string
	Mine                betMine
}

// decodeBetInfo 从 bet 对象解码。options 可能直接是数组，也可能包在
// options / items / list 里；字段名按文档原样再加少量历史别名。
func decodeBetInfo(o object) *betInfo {
	if o == nil {
		return nil
	}
	b := &betInfo{
		Title:               cleanText(o.str("title")),
		Status:              o.str("status", "state"),
		ParticipateDeadline: o.str("participate_deadline", "bet_deadline", "deadline"),
		SettledOptionID:     o.str("settled_option_id", "winning_option_id", "win_option_id"),
		SettledAt:           o.str("settled_at", "settled_time"),
	}
	list := rawArray(o["options"])
	if len(list) == 0 {
		for _, k := range []string{"items", "list"} {
			if list = rawArray(o[k]); len(list) > 0 {
				break
			}
		}
	}
	for _, raw := range list {
		opt := asObject(raw)
		if opt == nil {
			continue
		}
		row := betOption{
			ID:         opt.str("id", "option_id"),
			Text:       cleanText(opt.str("text", "title", "name")),
			TotalCoins: opt.num("total_coins", "coins", "bet_coins"),
		}
		if row.ID == "" && row.Text == "" {
			continue
		}
		b.Options = append(b.Options, row)
	}
	b.Mine = decodeBetMine(o)
	if b.Title == "" && len(b.Options) == 0 {
		return nil
	}
	return b
}

// decodeBetMine 容错读取「当前用户参与情况」。文档只写了「外加当前用户的
// 参与情况」而没给字段名，线上可能是平铺的 my_option_id / my_coins，也可能
// 包在 my_bet / participation / mine 里；participated 之类的布尔只用来确认
// 字段确实存在。
func decodeBetMine(o object) betMine {
	var mine betMine
	if sub := o.sub("my_bet", "my_participation", "participation", "mine", "user_bet"); sub != nil {
		mine.OptionID = sub.str("option_id", "optionId", "bet_option_id", "selected_option_id")
		mine.Coins = sub.num("coins", "amount", "total_coins", "bet_coins")
		mine.Known = sub.has("option_id", "optionId", "coins", "amount", "participated", "has_participated")
	}
	if mine.OptionID == "" {
		mine.OptionID = o.str("my_option_id", "my_bet_option_id", "participated_option_id", "selected_option_id")
	}
	if mine.Coins == 0 {
		mine.Coins = o.num("my_coins", "my_bet_coins", "participated_coins")
	}
	if !mine.Known {
		mine.Known = o.has("my_option_id", "my_bet_option_id", "my_coins", "my_bet_coins",
			"participated", "has_participated", "my_bet", "participation")
	}
	return mine
}

// decodeTopicBet 取详情里的竞猜块：线上在顶层 d.bet，早期文档在 d.extra.bet，
// 两处都认（与投票块一致的策略）。
func decodeTopicBet(o object) *betInfo {
	if b := o.sub("bet"); b != nil {
		return decodeBetInfo(b)
	}
	if extra := o.sub("extra"); extra != nil {
		return decodeBetInfo(extra.sub("bet"))
	}
	return nil
}

// decodeTopicBets 解析 GET /bets 的 d：直接是 bet 对象，或包在 bet / bets 里。
func decodeTopicBets(d json.RawMessage) betInfo {
	o := asObject(d)
	if o == nil {
		return betInfo{}
	}
	if inner := o.sub("bet", "bets"); inner != nil {
		o = inner
	}
	if b := decodeBetInfo(o); b != nil {
		return *b
	}
	return betInfo{}
}

// betSettled 报告竞猜是否已经结算：settled_option_id 是最稳的信号，status
// 的历史拼写作为兜底。
func betSettled(b *betInfo) bool {
	if b == nil {
		return false
	}
	if b.SettledOptionID != "" || b.SettledAt != "" {
		return true
	}
	switch strings.ToLower(b.Status) {
	case "settled", "resolved", "finished", "ended", "closed":
		return true
	}
	return false
}

// betParticipationClosed 报告现在还能不能参与：已结算、状态关闭，或参与
// 截止时间已过。时间解析失败时不擅自判定截止，交由服务端裁决。
func betParticipationClosed(b *betInfo) bool {
	if b == nil || betSettled(b) {
		return true
	}
	switch strings.ToLower(b.Status) {
	case "closed", "ended", "finished":
		return true
	}
	if t, ok := betTime(b.ParticipateDeadline); ok && !time.Now().Before(t) {
		return true
	}
	return false
}

func betTime(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// betCoinsValue 把用户输入的金额收紧到合法区间并取整：NumberInput 的 step
// 是 1，但防御性地处理小数，避免把 1.9 这样的值原样发给服务端。
func betCoinsValue(v float64) int {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return betCoinsMin
	}
	n := int(math.Round(v))
	if n < betCoinsMin {
		return betCoinsMin
	}
	if n > betCoinsMax {
		return betCoinsMax
	}
	return n
}

// betBody 的请求体只有 coins 一个字段，原样照抄文档（snake_case 不适用，
// 这里就是 coins）。
type betBody struct {
	Coins int `json:"coins"`
}

// fetchTopicBets 读一次竞猜信息（含我的参与）。纯读端点，scope read:economy。
func (c *Client) fetchTopicBets(ctx context.Context, shortID string) (betInfo, error) {
	return withAuthRetry(c, ctx, func(token string) (betInfo, error) {
		d, err := c.getJSON(ctx, c.apiURL(topicBetsPath(shortID)), token)
		if err != nil {
			return betInfo{}, err
		}
		return decodeTopicBets(d), nil
	})
}

// participateBet 提交一笔投注。key 由调用方给，同一笔投注重试必须复用。
func (c *Client) participateBet(ctx context.Context, key, shortID, optionID string, coins int) error {
	if optionID == "" {
		return fmt.Errorf("缺少竞猜选项")
	}
	if coins < betCoinsMin {
		return fmt.Errorf("投注金额至少 %d 金币", betCoinsMin)
	}
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		_, err := c.postJSON(ctx, c.apiURL(topicBetParticipatePath(shortID, optionID)), token, key, betBody{Coins: coins})
		return struct{}{}, err
	})
	return err
}

// betUIState 是详情页竞猜块的界面状态，只在 UI 线程读写。
//
// info 是用户主动点「我的参与」后 GET /bets 拿到的权威信息（覆盖详情的那一
// 份，含 settled_at 与我的投注）；busy/amount/selected 是参与面板的草稿与
// 进度；key/sig 是这笔投注的幂等键与签名，内容不变就复用。gen/loadGen 丢弃
// 切帖子之前在途的迟到结果。
type betUIState struct {
	busy      bool
	gen       uint64
	key       string
	sig       string
	selected  string
	amount    float64
	panelFor  string
	panelOpen bool // Lobe Modal 的开合标志，由 panelFor 指定投注的选项

	info    *betInfo
	loading bool
	err     string
	loadGen uint64
}

// syncBets 在详情刷新后对齐状态：换了帖子或投注面板指向的选项已经不在选项
// 列表里，就收起面板；正在提交的那一笔不动，等它自己的结果回来。
func syncBets(d *detailState, t topicDetail) {
	if d == nil || d.bet.busy {
		return
	}
	b := t.Bet
	if b == nil || d.bet.panelFor == "" {
		return
	}
	for _, o := range b.Options {
		if o.ID == d.bet.panelFor {
			return
		}
	}
	d.bet.panelFor = ""
	d.bet.selected = ""
	d.bet.panelOpen = false
}

// openBetPanel 打开 / 收起某个选项的投注弹窗。金额给一个默认值，避免用户
// 一进来就是 0 再报错。
func (a *app) openBetPanel(d *detailState, optionID string) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.topic.Bet == nil {
		return
	}
	if betParticipationClosed(d.topic.Bet) {
		return
	}
	if d.bet.panelFor == optionID {
		d.bet.panelFor = ""
		d.bet.selected = ""
		d.bet.panelOpen = false
		return
	}
	d.bet.panelFor = optionID
	d.bet.selected = optionID
	d.bet.panelOpen = true
	if d.bet.amount < betCoinsMin {
		d.bet.amount = 10
	}
}

// loadBets 读一次 GET /bets，把「我的参与」和结算信息并进当前展示。和投票
// 结果分布一样，这是用户主动发起的只读查询：失败只把原因留在竞猜块里可重
// 试，不弹 toast。
func (a *app) loadBets(d *detailState) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.topic.Bet == nil || !a.autoLoad {
		return
	}
	if d.bet.loading {
		return
	}
	d.bet.loading = true
	d.bet.err = ""
	d.bet.loadGen++
	gen := d.bet.loadGen
	shortID := d.shortID
	ctx, cancel := context.WithTimeout(context.Background(), betTimeout)
	go func() {
		defer cancel()
		res, err := a.client.fetchTopicBets(ctx, shortID)
		a.publish(func() {
			if !a.detailLive(d) || d.bet.loadGen != gen {
				return
			}
			d.bet.loading = false
			if err != nil {
				if a.handleAuthError(err) {
					return
				}
				d.bet.err = friendlyError(err)
				return
			}
			d.bet.info = &res
		})
	}()
}

// betDisplay 返回当前用来展示的竞猜块：优先用 GET /bets 读到的权威信息，
// 没有时退回详情里的那一份。
func betDisplay(d *detailState) *betInfo {
	if d == nil || d.topic == nil {
		return nil
	}
	if d.bet.info != nil {
		return d.bet.info
	}
	return d.topic.Bet
}

// submitBet 提交一笔投注。结果一律用主窗口 toast 报告，面板里只留「正在
// 参与」的进度。成功后本地把金额记进选项。
func (a *app) submitBet(d *detailState, optionID string) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.topic.Bet == nil || d.bet.busy {
		return
	}
	if betParticipationClosed(d.topic.Bet) {
		a.toast(a.detailToastFor(d), "竞猜已经结束，不能再参与")
		return
	}
	// 选项 uuid 必须来自当前这一份详情，刷新后旧选项可能已经不在了。
	valid := false
	for _, o := range d.topic.Bet.Options {
		if o.ID == optionID {
			valid = true
			break
		}
	}
	if !valid {
		a.toast(a.detailToastFor(d), "选项已变化，请重新选择")
		return
	}
	coins := betCoinsValue(d.bet.amount)
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能参与竞猜。")
		return
	}
	// 同一笔投注（同选项、同金额）重试复用同一个幂等键；改了金额或选项
	// 就是另一笔，换新键。
	sig := fmt.Sprintf("%s|%s|%d", d.shortID, optionID, coins)
	if d.bet.key == "" || d.bet.sig != sig {
		d.bet.key = randomURLSafe(24)
		d.bet.sig = sig
	}
	key := d.bet.key
	d.bet.busy = true
	d.bet.err = ""
	d.bet.gen++
	gen := d.bet.gen
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), betTimeout)
	go func() {
		defer cancel()
		err := a.client.participateBet(ctx, key, d.shortID, optionID, coins)
		a.publish(func() {
			if !a.detailLive(d) || d.bet.gen != gen {
				return
			}
			d.bet.busy = false
			// 请求期间换了账号 / 退出登录：这一笔不属于当前会话。
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthError(err) {
					return
				}
				d.bet.err = friendlyError(err)
				a.toast(a.detailToastFor(d), friendlyError(err))
				return
			}
			// 成功才记账：失败时界面停在原样，不假装投过。
			d.bet.key = ""
			d.bet.sig = ""
			d.bet.panelFor = ""
			d.bet.selected = ""
			d.bet.panelOpen = false
			d.bet.info = nil // 本地这份已过期，下次「我的参与」重新读
			applyBet(d, optionID, coins, actor)
			a.toast(a.detailToastFor(d), fmt.Sprintf("已投入 %d 金币", coins))
		})
	}()
}

// applyBet 把一笔成功的投注就地记到详情的选项汇总与「我的参与」上，不必
// 重发一次详情请求（那次请求还可能拿到 CDN 上的旧副本）。
func applyBet(d *detailState, optionID string, coins int, actor string) {
	if d == nil || d.topic == nil || d.topic.Bet == nil || coins <= 0 {
		return
	}
	for i := range d.topic.Bet.Options {
		if d.topic.Bet.Options[i].ID == optionID {
			d.topic.Bet.Options[i].TotalCoins += coins
			break
		}
	}
	m := &d.topic.Bet.Mine
	m.Known = true
	m.OptionID = optionID
	m.Coins += coins
	_ = actor
}

/* ------------------------------------------------------------------
   竞猜块的界面
-------------------------------------------------------------- */

// betSection 画正文末尾的竞猜块：每个选项一行，显示累计投入，点开 Modal
// 填金币数并确认。已结算时标出中奖选项、禁用参与；「我的参与」是一次用户
// 主动发起的只读查询（见 loadBets）。
func (a *app) betSection(c *ui.Context, d *detailState) {
	t := c.Theme()
	ts := a.typeSizes()
	b := betDisplay(d)
	if b == nil {
		return
	}
	ui.Divider(c)
	ui.Column(c).FillWidth().Padding(12, 14).Gap(6).Radius(10).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		title := "竞猜"
		if b.Title != "" {
			title += " · " + b.Title
		}
		ui.Text(c, title).FontSize(ts.comment).Bold()
		if b.ParticipateDeadline != "" {
			if tm, ok := betTime(b.ParticipateDeadline); ok {
				ui.Textf(c, "参与截止 %s", tm.Format("01-02 15:04")).
					FontSize(ts.metaSmall).TextColor(t.TextMuted)
			}
		}
		closed := betParticipationClosed(b)
		for _, o := range b.Options {
			o := o
			label := o.Text
			if label == "" {
				label = "选项"
			}
			if o.TotalCoins > 0 {
				label += fmt.Sprintf(" · %s 金币", coinThousands(o.TotalCoins))
			}
			if betSettled(b) && o.ID == b.SettledOptionID {
				label += " · 已中签"
			}
			row := ui.Button(c, label).FontSize(ts.meta).FillWidth().
				Disabled(closed || d.bet.busy || o.ID == "")
			if !closed && row.Clicked() {
				a.openBetPanel(d, o.ID)
			}
		}
		// 我的参与 + 状态刷新：只有登录用户、且详情里确认有竞猜时才给入口。
		if a.auth.loggedIn() {
			a.betMineRow(c, d, b)
		}
		if d.bet.err != "" {
			ui.Text(c, "读取参与情况失败："+d.bet.err).FontSize(11).
				TextColor(t.Danger).MaxLines(2)
		}
	})
}

// betDialog 是竞猜投注弹窗：点某个选项后盖在整页上填金额并确认。标题带上所
// 选选项，免得关掉面板后不知道投的是哪一项。它走 Lobe 的 Modal。
func (a *app) betDialog(c *ui.Context, d *detailState) {
	if d == nil || !d.bet.panelOpen || d.bet.panelFor == "" {
		return
	}
	b := betDisplay(d)
	if b == nil || betParticipationClosed(b) {
		return
	}
	var target betOption
	found := false
	for _, o := range b.Options {
		if o.ID == d.bet.panelFor {
			target, found = o, true
			break
		}
	}
	if !found {
		return
	}
	title := "参与竞猜"
	if target.Text != "" {
		title = "竞猜 · " + target.Text
	}
	feedback.Modal(c, &d.bet.panelOpen, feedback.ModalProps{
		Title:    title,
		Width:    380,
		NoFooter: true,
	}, func() {
		a.betPanel(c, d, target)
	})
	// 点窗外 / Esc 关窗时 Modal 只清 open，这里把目标也清掉，下次点同一
	// 选项才是「打开」而不是「再点一次收起」。
	if !d.bet.panelOpen {
		d.bet.panelFor = ""
		d.bet.selected = ""
	}
}

// betPanel 是投注弹窗里的内容：金额、余额与确认按钮，和打赏面板一样把「会
// 扣币」说清楚。
func (a *app) betPanel(c *ui.Context, d *detailState, o betOption) {
	t := c.Theme()
	ts := a.typeSizes()
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.NumberInput(c, &d.bet.amount, betCoinsMin, betCoinsMax, 1).
				Label("投入金币").Width(120).Disabled(d.bet.busy)
			ui.Text(c, "金币").FontSize(ts.meta).TextColor(t.TextMuted)
			if a.coins != nil && a.coins.balanceOK {
				ui.Textf(c, "余额 %s", coinThousands(a.coins.balance)).
					FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
		ui.Text(c, "投注会从余额扣除，确认后不能撤销。").FontSize(11).
			TextColor(t.TextMuted).MaxLines(2)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c).Grow(1)
			if d.bet.busy {
				lobeSpin(c, feedback.SpinProps{CustomSize: 13})
				ui.Text(c, "正在参与…").FontSize(11).TextColor(t.TextMuted)
				return
			}
			coins := betCoinsValue(d.bet.amount)
			if ui.PrimaryButton(c, fmt.Sprintf("投入 %d 金币", coins)).Clicked() {
				a.submitBet(d, o.ID)
			}
		})
	})
}

// betMineRow 显示我的投注，并给一个「我的参与」入口读 GET /bets。失败时
// 原因留在竞猜块里可点「重试」，不弹 toast。
func (a *app) betMineRow(c *ui.Context, d *detailState, b *betInfo) {
	t := c.Theme()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		mine := b.Mine
		if d.bet.info != nil {
			mine = d.bet.info.Mine
		}
		switch {
		case mine.Known && mine.Coins > 0:
			ui.Textf(c, "我已投入 %d 金币", mine.Coins).FontSize(11).TextColor(t.TextMuted)
		case mine.Known:
			ui.Text(c, "我还没有参与").FontSize(11).TextColor(t.TextMuted)
		}
		ui.Spacer(c).Grow(1)
		label := "我的参与"
		if d.bet.loading {
			label = "读取中…"
		}
		if ui.Button(c, label).FontSize(11).Disabled(d.bet.loading).Clicked() {
			a.loadBets(d)
		}
	})
}
