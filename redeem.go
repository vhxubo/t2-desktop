package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
)

// 兑换码（api-read.md 的「我的」与 api-economy.md 的「兑换码」）
//
//	GET  /v1/topics/:short_id             详情里的 extra.redeem_codes：每项带
//	                                      id、code（中间用 * 脱敏）、is_redeemed、
//	                                      redeemed_by、redeemed_at
//	GET  /v1/me/redeem-codes              我兑换过的兑换码（scope read:me）
//	POST /v1/redeem-codes/:code/redeem 🔑 scope write:economy，需开通
//
// 领取按每条码的 id 走：详情里的 code 是脱敏值，不能用来领取；网页端也是点
// 一条未领取的码、把它的 id 交给领取接口，成功后响应 d.code 才是完整码。
// 公开 API 的路径参数同时接受人类可读的完整码和内部 uuid（api-economy.md），
// 这里用详情给的 id。id 仍是路径段，照旧按 URL 转义拼进去。
//
// 已领完 / 已过期 / 不存在是 400 invalid_request 或 409 conflict，两者的 m
// 都是可以直接展示的原因；逻辑判断只看状态码与 code。每用户可领个数由帖子的
// max_redeem_per_user 决定，客户端先挡一道，服务端仍会校验。
//
// 领取是标 🔑 且会改变金币的写操作：同一条码重试用同一个 Idempotency-Key，
// 换一条码就是另一笔，换新键。

const (
	// redeemTimeout 与打赏 / 竞猜一致：写操作给 20 秒，网络卡住时按钮不会
	// 一直转圈。
	redeemTimeout = 20 * time.Second
	// redeemedCodesPath 是「我兑换过的兑换码」。只读且只限本人：路径里没有
	// 任何用户参数，领码仍走 POST /v1/redeem-codes/:code/redeem。
	redeemedCodesPath = "/v1/me/redeem-codes"
	// redeemedCodesLimit 是该端点每页条数：文档默认 50、上限 50。
	redeemedCodesLimit = 50
)

// redeemPath 拼领取路径。兑换码 / id 仍是路径段，整段必须转义，不能拼成
// 多级路径。
func redeemPath(code string) string {
	return "/v1/redeem-codes/" + url.PathEscape(code) + "/redeem"
}

// redeemCodeEntry 是详情里的一条兑换码。字段口径按线上实测（2026-11-17
// 的 changelog 已更正文档）：Masked 来自 code，未领取 / 已领取都是脱敏值；
// ID 是领取用的 uuid；RedeemedByID / RedeemedByName 来自 redeemed_by 对象。
// Code 只在本机刚领到时才有值——完整码只在领取响应里出现一次。
type redeemCodeEntry struct {
	ID             string
	Masked         string
	Code           string
	Redeemed       bool
	RedeemedByID   string
	RedeemedByName string
	RedeemedAt     string
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
// 完整码；金额只在字段明确表示「奖励 / 红包」时才填 —— 名字叫 coins 的字段
// 可能是余额，不能当成这次获得的金币（签到那边有同一条约定，见 coins.go）。
type redeemResult struct {
	Message        string
	Code           string
	Coins          float64
	CoinsKnown     bool
	RedPacket      float64
	RedPacketKnown bool
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

// decodeRedeemBlock 把一个兑换码块解成结构化数据。线上给裸数组，早期文档
// 的包一层写法 { codes, description, max_redeem_per_user } 也认。
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
				ID:         e.str("id", "uuid", "code_id"),
				Masked:     e.str("code", "masked_code", "masked", "mask"),
				Code:       e.str("full_code", "redeem_code"),
				Redeemed:   e.flag("is_redeemed", "redeemed", "used"),
				RedeemedAt: e.str("redeemed_at"),
			}
			// redeemed_by 是带 id / username 的用户对象；也认早期文档里直接
			// 给用户名字符串的写法。
			if by := e.sub("redeemed_by"); by != nil {
				entry.RedeemedByID = by.str("id")
				entry.RedeemedByName = by.str("username", "name")
			} else {
				entry.RedeemedByName = e.str("redeemed_by")
			}
			// 完整码出现即视为已领取，即使服务端没带 is_redeemed。
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
	for _, key := range []string{"reward_coins", "award_coins", "reward_amount", "coins_reward",
		"red_packet_amount", "redPacketAmount", "red_packet", "redPacket", "packet_amount"} {
		raw, ok := o[key]
		if !ok {
			continue
		}
		var f float64
		if json.Unmarshal(raw, &f) == nil {
			if strings.Contains(strings.ToLower(key), "packet") {
				out.RedPacket, out.RedPacketKnown = f, true
			} else {
				out.Coins, out.CoinsKnown = f, true
			}
		}
	}
	return out
}

/* ------------------------------------------------------------------
   请求
---------------------------------------------------------------- */

// redeemCode 领取一条兑换码。文档明确没有请求体，所以 payload 传 nil；
// 领取会改变金币，按标 🔑 的规则必须带 Idempotency-Key（由调用方给）。
// target 优先用详情给的 id，没有 id 时才用完整码。
func (c *Client) redeemCode(ctx context.Context, idempotencyKey, target string) (redeemResult, error) {
	var out redeemResult
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		d, err := c.postJSON(ctx, c.apiURL(redeemPath(target)), token, idempotencyKey, nil)
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
	busy bool
	gen  uint64

	// key / sig 是幂等键与它对应的那一笔（帖子 + 码 id）：同一条码重试复用，
	// 换一条码就是新的一笔。成功后清空。
	key string
	sig string

	// dialogID 非空表示「领取确认框」开着，值是那条码的 id。dialogDone 表示
	// 已经领到、弹窗切到成功页；claimedFull 是响应里的完整码。
	dialogID    string
	dialogDone  bool
	claimedFull string
	redPacket   float64

	// full 按码 id 记下本机刚领到的完整码。详情只给脱敏值，领完之后要把
	// 完整码留在码条上，用户才能复制，且不必重发一次详情请求（那会再计一次
	// 浏览）。
	full map[string]string
}

// redeemClaimedByMe 判断一条已领取的码是不是当前账号领的。网页端按
// redeemed_by.id 与自己的 id 比；桌面端令牌里只有用户名，就用用户名比，
// 本机刚领到的那条也在领的时候补上了用户名。
func redeemClaimedByMe(e redeemCodeEntry, me string) bool {
	return e.Redeemed && me != "" && e.RedeemedByName == me
}

// redeemMyCount 数已经由我领到的条数，用于每人上限的本地拦截。
func redeemMyCount(b *redeemBlock, me string) int {
	if b == nil {
		return 0
	}
	n := 0
	for _, e := range b.Codes {
		if redeemClaimedByMe(e, me) {
			n++
		}
	}
	return n
}

// redeemOpenDialog 打开某条码的领取确认框。别人领过的、自己领过的、超过
// 每人上限的都不让再领；未登录先去登录。
func (a *app) redeemOpenDialog(d *detailState, id string) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.topic.Redeem == nil || d.redeem.busy {
		return
	}
	b := d.topic.Redeem
	var e *redeemCodeEntry
	for i := range b.Codes {
		if b.Codes[i].ID == id {
			e = &b.Codes[i]
			break
		}
	}
	if e == nil || e.Redeemed || e.ID == "" {
		return
	}
	me := a.auth.username()
	if b.MaxPerUserKnown && b.MaxPerUser > 0 && redeemMyCount(b, me) >= b.MaxPerUser {
		a.toast(a.detailToastFor(d), fmt.Sprintf("已达到单个用户最多可领取 %d 个兑换码的限制", b.MaxPerUser))
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能领取兑换码。")
		return
	}
	st := &d.redeem
	st.dialogID = id
	st.dialogDone = false
	st.claimedFull = ""
}

// submitRedeemCode 确认领取某一条码。结果（成功、失败、服务端拒绝的原因）
// 一律用主窗口 toast 报告；请求期间弹窗里只留「正在领取」的进度。
func (a *app) submitRedeemCode(d *detailState, id string) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.topic.Redeem == nil || d.redeem.busy {
		return
	}
	b := d.topic.Redeem
	var e *redeemCodeEntry
	for i := range b.Codes {
		if b.Codes[i].ID == id {
			e = &b.Codes[i]
			break
		}
	}
	if e == nil || e.Redeemed {
		return
	}
	target := e.ID
	if target == "" {
		target = e.Code
	}
	if target == "" {
		a.toast(a.detailToastFor(d), "这个兑换码暂时无法领取")
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能领取兑换码。")
		return
	}
	// 同一条码的超时重试复用同一个键；换码即换新键。
	sig := d.shortID + "|" + id
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
		res, err := a.client.redeemCode(ctx, key, target)
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
				if a.handleAuthError(err) {
					return
				}
				a.toast(a.detailToastFor(d), redeemErrorText(err))
				return
			}
			d.redeem.key = ""
			d.redeem.sig = ""
			full := strings.TrimSpace(res.Code)
			if full == "" {
				full = e.Masked
			}
			if d.redeem.full == nil {
				d.redeem.full = make(map[string]string)
			}
			d.redeem.full[id] = full
			// 服务端下次给的详情里这一条仍是脱敏值，本地立刻标成我的，
			// 码条上就能显示完整码与「已领取」，不必再拉一次详情。
			e.Redeemed = true
			e.RedeemedByName = actor
			e.RedeemedAt = time.Now().UTC().Format(time.RFC3339)
			e.Code = full
			d.redeem.dialogDone = true
			d.redeem.claimedFull = full
			d.redeem.redPacket = res.RedPacket
			a.toast(a.detailToastFor(d), redeemSuccessText(res))
		})
	}()
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

// redeemSuccessText 是成功提示：优先服务端给的说明，其次红包 / 奖励金额，
// 最后退回一句通用文案。
func redeemSuccessText(res redeemResult) string {
	if res.Message != "" {
		return res.Message
	}
	if res.RedPacketKnown && res.RedPacket > 0 {
		return fmt.Sprintf("兑换成功，获得金币红包 %s 金币", coinThousands(int(res.RedPacket)))
	}
	if res.CoinsKnown && res.Coins > 0 {
		return fmt.Sprintf("兑换成功，获得 %s 金币", coinThousands(int(res.Coins)))
	}
	return "兑换成功"
}

/* ------------------------------------------------------------------
   界面
---------------------------------------------------------------- */

// redeemSection 画正文末尾的兑换码块：每条码是一片。未领取的点开确认框，
// 领到后码条上显示完整码并可复制；别人领过的、自己领过的都不可再点。
func (a *app) redeemSection(c *ui.Context, d *detailState) {
	b := d.topic.Redeem
	if b == nil {
		return
	}
	t := c.Theme()
	ts := a.typeSizes()
	ui.Divider(c)
	ui.Column(c).FillWidth().Padding(12, 14).Gap(6).Radius(10).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Text(c, "兑换码").FontSize(ts.comment).Bold()
		if b.Description != "" {
			ui.Text(c, b.Description).FontSize(ts.metaSmall).
				TextColor(t.TextMuted).MaxLines(4)
		}
		if b.MaxPerUserKnown && b.MaxPerUser > 0 {
			ui.Textf(c, "每人可领 %d 个", b.MaxPerUser).
				FontSize(ts.metaSmall).TextColor(t.TextMuted)
		}
		me := a.auth.username()
		atLimit := b.MaxPerUserKnown && b.MaxPerUser > 0 && redeemMyCount(b, me) >= b.MaxPerUser
		for i := range b.Codes {
			a.redeemCodeChip(c, d, &b.Codes[i], atLimit, me)
		}
		if !a.auth.loggedIn() {
			// 登录回来后详情页会整体重建，这里不需要另外登记重跑。
			if ui.Button(c, "登录后领取").FontSize(11).Clicked() {
				a.requestLogin("登录后才能领取兑换码。")
			}
		}
	})
}

// redeemCodeChip 画一条码。未领取的是可点的芯片，点开领取确认框；已领取的
// 只展示状态与完整码（本机领到时才知道），自己的那一条带复制按钮。
func (a *app) redeemCodeChip(c *ui.Context, d *detailState, e *redeemCodeEntry, atLimit bool, me string) {
	t := c.Theme()
	ts := a.typeSizes()
	display := e.Masked
	if full := d.redeem.full[e.ID]; full != "" {
		display = full
	} else if e.Code != "" {
		display = e.Code
	}
	mine := redeemClaimedByMe(*e, me)
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		if !e.Redeemed {
			tip := "点开领取这个兑换码，领取后可看到完整码并复制"
			if atLimit {
				tip = "你已达到这个帖子的每人领取上限"
			}
			chip := ui.Button(c, display).FontSize(11).Radius(6).
				Label("领取兑换码 " + display).Tooltip(tip)
			if chip.Clicked() {
				a.redeemOpenDialog(d, e.ID)
			}
			return
		}
		ui.Text(c, display).FontSize(ts.meta).Selectable().SingleLine().Shrink(1).MinWidth(0)
		if mine {
			ui.Text(c, "已领取").FontSize(ts.metaSmall).
				TextColor(t.Success).SingleLine()
			if full := d.redeem.full[e.ID]; full != "" {
				if iconActionButton(c, lucide.Copy(), "复制兑换码", "把完整兑换码复制到剪贴板").Clicked() {
					a.copyToClipboard(c, full, a.detailToastFor(d), "已复制兑换码")
				}
			}
			return
		}
		who := e.RedeemedByName
		if who == "" {
			who = "他人"
		}
		ui.Text(c, "已被 "+who+" 领取").FontSize(ts.metaSmall).
			TextColor(t.TextMuted).SingleLine()
	})
}

// redeemDialog 是领取确认框：先问一句，领到后切到成功页展示完整码与复制
// 按钮。确认后由 submitRedeemCode 发请求，结果用窗口 toast 报告。
func (a *app) redeemDialog(c *ui.Context, d *detailState) {
	st := &d.redeem
	if st.dialogID == "" {
		return
	}
	var e *redeemCodeEntry
	if d.topic != nil && d.topic.Redeem != nil {
		for i := range d.topic.Redeem.Codes {
			if d.topic.Redeem.Codes[i].ID == st.dialogID {
				e = &d.topic.Redeem.Codes[i]
				break
			}
		}
	}
	if e == nil {
		st.dialogID = ""
		return
	}
	t := c.Theme()
	shown := st.claimedFull
	if shown == "" {
		shown = e.Masked
	}
	okText, cancelText := "领取", "暂不需要"
	if st.dialogDone {
		okText, cancelText = "复制", "关闭"
	}
	open := true
	ok := feedback.Modal(c, &open, feedback.ModalProps{
		Title:      "领取兑换码",
		Width:      400,
		OkText:     okText,
		CancelText: cancelText,
	}, func() {
		ui.Column(c).FillWidth().Gap(8).Children(func() {
			ui.Text(c, shown).FontSize(14).Bold().Selectable().MaxLines(3)
			if st.dialogDone {
				ui.Text(c, "该兑换码领取成功。").FontSize(11).TextColor(t.Success)
				if st.redPacket > 0 {
					ui.Textf(c, "🧧 恭喜获得金币红包 %s 金币！", coinThousands(int(st.redPacket))).
						FontSize(11).TextColor(t.Warning)
				}
			} else {
				ui.Text(c, "需要领取这个兑换码吗？").FontSize(11).TextColor(t.TextMuted)
			}
			if st.busy {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					lobeSpin(c, feedback.SpinProps{CustomSize: 13})
					ui.Text(c, "正在领取…").FontSize(11).TextColor(t.TextMuted)
				})
			}
		})
	})
	// 点窗外、✕、Esc 与取消都关掉弹窗；成功页的「关闭」也是同一条路。
	if !open {
		st.dialogID = ""
		st.dialogDone = false
		st.claimedFull = ""
		return
	}
	if ok {
		if st.dialogDone {
			a.copyToClipboard(c, shown, a.detailToastFor(d), "已复制兑换码")
		} else if !st.busy {
			a.submitRedeemCode(d, st.dialogID)
		}
	}
}
