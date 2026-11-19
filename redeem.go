package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// 兑换码（api-read.md 的「我的」与 api-economy.md 的「兑换码」）
//
//	GET  /v1/topics/:short_id             详情里的 extra.redeem_codes：只有
//	                                      masked_code 与 is_redeemed，已领取的
//	                                      才给完整 code
//	GET  /v1/me/redeem-codes              我兑换过的兑换码（scope read:me）
//	POST /v1/redeem-codes/:code/redeem 🔑 scope write:economy，需开通
//
// 路径参数是人类可读的兑换码（帖子详情里那个 masked_code 是脱敏值，不能用来
// 领取；完整码由发帖人另行告知），所以必须按 URL 路径段转义再拼进去。文档说
// 传内部 uuid 也兼容，但客户端不应该知道 uuid，这里只按用户输入的字符串走。
//
// 已领完 / 已过期 / 不存在是 400 invalid_request 或 409 conflict，两者的 m
// 都是可以直接展示的原因；逻辑判断只看状态码与 code。每用户可领个数由帖子的
// max_redeem_per_user 决定，由服务端校验。
//
// 领取是标 🔑 且会改变金币的写操作：同一串码重试（超时、授权后重放）复用
// 同一个 Idempotency-Key，换一串码就是另一笔，换新键。

const (
	// redeemTimeout 与打赏 / 竞猜一致：写操作给 20 秒，网络卡住时按钮不会
	// 一直转圈。
	redeemTimeout = 20 * time.Second
	// redeemCodeMax 是单个兑换码的长度上限，取文档给发帖时的 redeem_codes
	// 每项上限（≤200 字）。按字符计数，与 compose.go 的 composeCodeTextMax
	// 同口径，避免中文码被按字节误判。
	redeemCodeMax = 200
	// redeemedCodesPath 是「我兑换过的兑换码」。只读且只限本人：路径里没有
	// 任何用户参数，领码仍走 POST /v1/redeem-codes/:code/redeem。
	redeemedCodesPath = "/v1/me/redeem-codes"
	// redeemedCodesLimit 是该端点每页条数：文档默认 50、上限 50。
	redeemedCodesLimit = 50
)

// redeemPath 拼领取路径。兑换码里可能有空格、斜杠、中文，整段必须转义，
// 不能拼成一个多级路径。
func redeemPath(code string) string {
	return "/v1/redeem-codes/" + url.PathEscape(code) + "/redeem"
}

// redeemCodeEntry 是详情里的一条兑换码。Masked 是脱敏值，任何人都能看到；
// Code 只有当前用户已经领到它之后服务端才会下发，所以 Code 非空就等于
// 「我已领取」。
type redeemCodeEntry struct {
	Masked   string
	Code     string
	Redeemed bool
}

// redeemBlock 是帖子里的兑换码块：兑换说明、每人可领个数与码列表。
// MaxPerUserKnown 区分「服务端没给这个字段」和「每人可领 0 个」，后者
// 不该出现在界面上。
type redeemBlock struct {
	Description     string
	MaxPerUser      int
	MaxPerUserKnown bool
	Codes           []redeemCodeEntry
}

// redeemResult 是 POST 成功后的响应。文档没有给成功响应的字段表，所以只认
// 认得出来的字段：Message 是服务端说明（可直接展示），Code 是服务端给出的
// 完整码（没有就用用户输入的那一串），Coins 只在字段明确表示「奖励」时才填
// —— 名字叫 coins 的字段可能是余额，不能当成这次获得的金币（签到那边有同
// 一条约定，见 coins.go 的 coinSignStatus.RewardKnown）。
type redeemResult struct {
	Message    string
	Code       string
	Coins      float64
	CoinsKnown bool
}

/* ------------------------------------------------------------------
   解码
---------------------------------------------------------------- */

// decodeTopicRedeem 从帖子详情里取兑换码块：文档写在 extra.redeem_codes，
// 顶层也认。兑换说明与每人可领个数可以和列表放在一起，也可能落在 extra 或
// 顶层，两处都看。
func decodeTopicRedeem(o object) *redeemBlock {
	var raw json.RawMessage
	if extra := o.sub("extra"); extra != nil {
		raw = extra["redeem_codes"]
	}
	if len(raw) == 0 {
		raw = o["redeem_codes"]
	}
	if len(raw) == 0 {
		return nil
	}
	b := decodeRedeemBlock(raw)
	if b == nil {
		return nil
	}
	if b.Description == "" {
		if extra := o.sub("extra"); extra != nil {
			b.Description = cleanText(extra.str("code_description"))
		}
		if b.Description == "" {
			b.Description = cleanText(o.str("code_description"))
		}
	}
	if !b.MaxPerUserKnown {
		for _, o := range []object{o.sub("extra"), o} {
			if o != nil && o.has("max_redeem_per_user") {
				b.MaxPerUserKnown = true
				b.MaxPerUser = o.num("max_redeem_per_user")
				break
			}
		}
	}
	return b
}

// decodeRedeemBlock 把一个兑换码块解成结构化数据。线上可能给裸数组，也可能
// 包一层 { codes, description, max_redeem_per_user }；子字段名文档只给了
// masked_code / is_redeemed / code 三个，列表字段按几种常见拼写容错。
func decodeRedeemBlock(raw json.RawMessage) *redeemBlock {
	if list := rawArray(raw); len(list) > 0 {
		return &redeemBlock{Codes: decodeRedeemEntries(list)}
	}
	o := asObject(raw)
	if o == nil {
		return nil
	}
	b := &redeemBlock{
		Description: cleanText(o.str("description", "code_description", "note", "text")),
	}
	if o.has("max_redeem_per_user") {
		b.MaxPerUserKnown = true
		b.MaxPerUser = o.num("max_redeem_per_user")
	}
	for _, key := range []string{"codes", "items", "list", "redeem_codes"} {
		if list := rawArray(o[key]); len(list) > 0 {
			b.Codes = decodeRedeemEntries(list)
			break
		}
	}
	// 空块什么都不表示，界面不该因此多出一张空卡片。
	if len(b.Codes) == 0 && b.Description == "" {
		return nil
	}
	return b
}

func decodeRedeemEntries(list []json.RawMessage) []redeemCodeEntry {
	var out []redeemCodeEntry
	for _, item := range list {
		if e := asObject(item); e != nil {
			entry := redeemCodeEntry{
				Masked:   e.str("masked_code", "masked", "mask"),
				Code:     e.str("code", "full_code", "redeem_code"),
				Redeemed: e.flag("is_redeemed", "redeemed", "used"),
			}
			// 文档：已领取的才给完整 code。完整码出现即视为已领取，即使
			// 服务端没带 is_redeemed。
			if entry.Code != "" {
				entry.Redeemed = true
			}
			if entry.Masked == "" {
				entry.Masked = entry.Code
			}
			if entry.Masked == "" {
				continue
			}
			out = append(out, entry)
			continue
		}
		// 也认裸字符串列表：没有脱敏信息时它就是码本身。
		var s string
		if json.Unmarshal(item, &s) == nil && s != "" {
			out = append(out, redeemCodeEntry{Masked: s})
		}
	}
	return out
}

// decodeRedeemResult 容错读领取成功的响应：字段名文档没给，认得的才用。
func decodeRedeemResult(d json.RawMessage) redeemResult {
	var out redeemResult
	o := asObject(d)
	if o == nil {
		return out
	}
	out.Message = cleanText(o.str("message", "msg", "tip"))
	out.Code = o.str("code", "full_code", "redeem_code")
	for _, key := range []string{"reward_coins", "award_coins", "reward_amount", "coins_reward"} {
		raw, ok := o[key]
		if !ok {
			continue
		}
		var f float64
		if json.Unmarshal(raw, &f) == nil {
			out.Coins, out.CoinsKnown = f, true
			break
		}
	}
	return out
}

/* ------------------------------------------------------------------
   请求
---------------------------------------------------------------- */

// redeemCode 领取一个兑换码。文档明确没有请求体，所以 payload 传 nil；
// 领取会改变金币，按标 🔑 的规则必须带 Idempotency-Key（由调用方给）。
func (c *Client) redeemCode(ctx context.Context, idempotencyKey, code string) (redeemResult, error) {
	var out redeemResult
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		d, err := c.postJSON(ctx, c.apiURL(redeemPath(code)), token, idempotencyKey, nil)
		if err != nil {
			return struct{}{}, err
		}
		out = decodeRedeemResult(d)
		return struct{}{}, nil
	})
	return out, err
}

/* ------------------------------------------------------------------
   界面状态与动作
---------------------------------------------------------------- */

// redeemUIState 是详情页兑换码块的界面状态，只在 UI 线程读写。
type redeemUIState struct {
	code string // 用户输入的完整兑换码
	busy bool
	gen  uint64

	// key / sig 是幂等键与它对应的那一笔（帖子 + 码）：同一串码重试复用，
	// 换算另一串码就是新的一笔。成功后清空。
	key string
	sig string

	// claimed 是本机刚领到的完整码。详情只给 masked_code，领取成功后完整
	// 码只在响应里出现一次，记在这里才能继续显示「已领取」，不必重发一次
	// 详情请求（那会再计一次浏览）。
	claimed []string
}

// submitRedeemCode 提交一次领取。结果（成功、失败、服务端拒绝的原因）一律用
// 主窗口 toast 报告，块里只留「正在领取」的进度。
func (a *app) submitRedeemCode(d *detailState) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.topic.Redeem == nil || d.redeem.busy {
		return
	}
	code := strings.TrimSpace(d.redeem.code)
	if code == "" {
		a.toast(a.detailToastFor(d), "请先输入兑换码")
		return
	}
	if n := utf8.RuneCountInString(code); n > redeemCodeMax {
		a.toast(a.detailToastFor(d), fmt.Sprintf("兑换码最多 %d 字，当前 %d 字", redeemCodeMax, n))
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能领取兑换码。", func() {
			if a.detailLive(d) {
				a.submitRedeemCode(d)
			}
		})
		return
	}
	// 同一串码重试（超时、授权后重放）复用同一个键；换码即换新键。
	sig := d.shortID + "|" + code
	if d.redeem.key == "" || d.redeem.sig != sig {
		d.redeem.key = randomURLSafe(24)
		d.redeem.sig = sig
	}
	key := d.redeem.key
	d.redeem.busy = true
	d.redeem.gen++
	gen := d.redeem.gen
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), redeemTimeout)
	go func() {
		defer cancel()
		res, err := a.client.redeemCode(ctx, key, code)
		a.publish(func() {
			if !a.detailLive(d) || d.redeem.gen != gen {
				return
			}
			d.redeem.busy = false
			// 请求期间换了账号 / 退出登录：这一笔不属于当前会话。
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthError(err, func() {
					if a.detailLive(d) {
						a.submitRedeemCode(d)
					}
				}) {
					return
				}
				a.toast(a.detailToastFor(d), redeemErrorText(err))
				return
			}
			d.redeem.key = ""
			d.redeem.sig = ""
			d.redeem.code = ""
			full := res.Code
			if full == "" {
				full = code
			}
			d.redeem.addClaimed(full)
			markRedeemClaimed(d.topic.Redeem, full)
			a.toast(a.detailToastFor(d), redeemSuccessText(res))
			a.coinsChanged(coinEventRedeem)
		})
	}()
}

func (st *redeemUIState) addClaimed(code string) {
	if code == "" {
		return
	}
	for _, have := range st.claimed {
		if have == code {
			return
		}
	}
	st.claimed = append(st.claimed, code)
}

// markRedeemClaimed 把本地已知的完整码标成已领取：服务端下次给的详情里会
// 带上完整 code，到时列表自己就会显示「已领取」，这里只是让当前这一份立刻
// 跟上，不必等下一次刷新。
func markRedeemClaimed(b *redeemBlock, code string) {
	if b == nil || code == "" {
		return
	}
	for i := range b.Codes {
		if b.Codes[i].Code == code {
			b.Codes[i].Redeemed = true
		}
	}
}

// redeemErrorText 把领取失败翻成用户能看懂的一句话。已领完 / 已过期 /
// 不存在这类 400 / 409 的 m 本身就是给用户看的原因，直接展示；其它情况
// 沿用统一的 friendlyError。
func redeemErrorText(err error) string {
	var ae *apiError
	if errors.As(err, &ae) && ae.Message != "" {
		switch ae.Code {
		case "invalid_request", "conflict", "permission_denied", "spend_limit_exceeded":
			return ae.Message
		}
	}
	return friendlyError(err)
}

// redeemSuccessText 是成功提示：优先服务端给的说明，其次是认出来的奖励
// 金额，最后退回一句通用文案。
func redeemSuccessText(res redeemResult) string {
	if res.Message != "" {
		return res.Message
	}
	if res.CoinsKnown && res.Coins > 0 {
		return fmt.Sprintf("兑换成功，获得 %s 金币", coinThousands(int(res.Coins)))
	}
	return "兑换成功"
}

/* ------------------------------------------------------------------
   界面
---------------------------------------------------------------- */

// redeemSection 画正文末尾的兑换码块：兑换说明、码列表与领取入口。列表里
// 只有脱敏值，完整码要用户自己粘贴进来；领取结果用主窗口 toast 报告。
func (a *app) redeemSection(c *ui.Context, d *detailState) {
	b := d.topic.Redeem
	if b == nil {
		return
	}
	t := c.Theme()
	ui.Divider(c)
	ui.Column(c).FillWidth().Padding(12, 14).Gap(6).Radius(10).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Text(c, "兑换码").FontSize(commentFontSize).Bold()
		if b.Description != "" {
			ui.Text(c, b.Description).FontSize(metaSmallFontSize).
				TextColor(t.TextMuted).MaxLines(4)
		}
		if b.MaxPerUserKnown && b.MaxPerUser > 0 {
			ui.Textf(c, "每人可领 %d 个", b.MaxPerUser).
				FontSize(metaSmallFontSize).TextColor(t.TextMuted)
		}
		for _, e := range b.Codes {
			text := e.Masked
			if e.Code != "" {
				text = e.Code
			}
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, text).FontSize(metaFontSize).SingleLine()
				if e.Redeemed {
					ui.Text(c, "已领取").FontSize(metaSmallFontSize).
						TextColor(t.Success).SingleLine()
				}
			})
		}
		// 刚在本机领到的完整码：详情还没刷新时也要看得见。
		for _, code := range redeemUnlistedClaims(b, d.redeem.claimed) {
			ui.Text(c, "已领取 · "+code).FontSize(metaFontSize).
				TextColor(t.Success).SingleLine()
		}
		if !a.auth.loggedIn() {
			ui.Text(c, "登录后可以领取兑换码，完整码由发帖人另行告知。").
				FontSize(metaSmallFontSize).TextColor(t.TextMuted).MaxLines(2)
			// 登录回来后详情页会整体重建，这里不需要另外登记重跑。
			if ui.Button(c, "登录后领取").FontSize(11).Clicked() {
				a.requestLogin("登录后才能领取兑换码。", nil)
			}
			return
		}
		ui.TextInput(c, &d.redeem.code).Label("兑换码").
			Placeholder("粘贴发帖人给你的完整兑换码").
			MaxLength(redeemCodeMax).FillWidth().Disabled(d.redeem.busy)
		ui.Text(c, "脱敏值不能用，请填发帖人给你的完整兑换码；已领完或已过期时以服务端的说明为准。").
			FontSize(metaSmallFontSize).TextColor(t.TextMuted).MaxLines(2)
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c).Grow(1)
			if d.redeem.busy {
				ui.Spinner(c).Size(13, 13)
				ui.Text(c, "正在领取…").FontSize(11).TextColor(t.TextMuted)
				return
			}
			if ui.PrimaryButton(c, "领取").
				Disabled(strings.TrimSpace(d.redeem.code) == "").Clicked() {
				a.submitRedeemCode(d)
			}
		})
	})
}

// redeemUnlistedClaims 过滤掉列表里已经显示过的码：服务端刷新后的详情会
// 给出完整 code，本机记的那一份就不必再重复一行。
func redeemUnlistedClaims(b *redeemBlock, claimed []string) []string {
	if len(claimed) == 0 {
		return nil
	}
	var out []string
	for _, code := range claimed {
		seen := false
		if b != nil {
			for _, e := range b.Codes {
				if e.Code == code {
					seen = true
					break
				}
			}
		}
		if !seen {
			out = append(out, code)
		}
	}
	return out
}
