package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"math"
	"strconv"

	"github.com/egoist/mygo/ui"
)

// 金币池（api-read.md 的 extra.reward_pool、api-economy.md 的「金币池」）
//
//	GET  /v1/topics/:short_id   详情里的 extra.reward_pool（线上在顶层
//	                            reward_pool）：current_coins / finished /
//	                            is_expired，只有状态，没有领取接口
//	评论里的 reward_pool_rewards: [{"amount": 53}]：这条评论从池里领到的金币
//	POST /v1/topics/:short_id/reward-pool 🔑  注资（200～1000 金币，需开通）
//
// 领取由服务端在浏览帖子时按概率完成，客户端不调接口；发帖时可以随帖带上
// 初始金币（见 compose.go 的金币池块）。已存在的池子可以从详情页的「投入
// 金币」补币：金额 200～1000，带幂等键，成功后更新池子状态。

// 金币池注资金额范围（文档：200～1000）。
const (
	rewardPoolCoinsLo = 200
	rewardPoolCoinsHi = 1000
)

// rewardPoolUIState 是详情页金币池注资弹窗的状态。只在 UI 线程读写；网络
// 结果经 publish 回来。key / sig 与打赏同一套规则：金额不变的重试复用同一个
// 幂等键，金额变了才换键。
type rewardPoolUIState struct {
	open   bool
	amount float64
	busy   bool
	err    string
	key    string
	sig    string
	gen    uint64
}

// rewardPoolInfo 是帖子的金币池状态。实测只有三个字段：current_coins（池子里
// 还剩多少金币）、finished（是否已结束）、is_expired（是否已过期）。命中记录
// 不在帖子上，而是挂在评论的 reward_pool_rewards 里。
type rewardPoolInfo struct {
	CurrentCoins int
	// CoinsKnown 区分「余额是 0」和「响应里没给余额」：后者不能写成
	// 「已领完」，那是把缺字段当成事实。
	CoinsKnown bool
	// Finished / Expired 分开存：结束与过期是两种收尾，界面说法不同。
	Finished bool
	Expired  bool
	// Known 表示响应里确实带了金币池块。没有这个键的帖子不画这一块。
	Known bool
}

// decodeTopicRewardPool 取帖子的金币池状态。实测响应把 reward_pool 放在
// 详情顶层（d.reward_pool），api-read.md 写的是 d.extra.reward_pool，两处都认。
// 没有这个键时返回 nil —— 不能顺手给一个 0 金币的池子，那会变成「这帖有金币池
// 但已经空了」的假消息。
func decodeTopicRewardPool(o object) *rewardPoolInfo {
	raw := o.sub("reward_pool")
	if raw == nil {
		if extra := o.sub("extra"); extra != nil {
			raw = extra.sub("reward_pool")
		}
	}
	if raw == nil {
		return nil
	}
	p := rewardPoolInfo{Known: true}
	if n, ok := raw.number("current_coins", "currentCoins"); ok {
		p.CurrentCoins = int(math.Round(n))
		p.CoinsKnown = true
	}
	if p.CurrentCoins < 0 {
		p.CurrentCoins = 0
	}
	p.Finished = raw.flag("finished")
	p.Expired = raw.flag("is_expired", "expired")
	return &p
}

// topicRewardPoolPath 是给已有金币池补币的端点。路径带 short_id；但线上服务端
// 仍然会校验请求体里的 post_id（只靠路径会得到「帖子 ID 不能为空」），所以两处
// 都带上，文档说的「路径优先」不受影响。
func topicRewardPoolPath(shortID string) string {
	return topicPath(shortID) + "/reward-pool"
}

// rewardPoolBody 是 POST /v1/topics/:short_id/reward-pool 的请求体。
type rewardPoolBody struct {
	PostID string `json:"post_id,omitempty"`
	Coins  int    `json:"coins"`
}

// fundRewardPool 给金币池注资。这个端点标了 🔑：同一笔注资的超时重试与授权后
// 重放必须复用同一个 key，金额变了才换新 key。
func (c *Client) fundRewardPool(ctx context.Context, shortID, key string, coins int) error {
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		_, err := c.postJSON(ctx, c.apiURL(topicRewardPoolPath(shortID)), token, key,
			rewardPoolBody{PostID: shortID, Coins: coins})
		return struct{}{}, err
	})
	return err
}

// syncRewardPool 在详情刷新后收掉指向旧池子的注资弹窗：池子没了、结束或过期，
// 就不该还留着一颗可以继续投入的按钮。
func syncRewardPool(d *detailState, t *topicDetail) {
	if d == nil || t == nil {
		return
	}
	p := t.RewardPool
	if p == nil || p.Finished || p.Expired {
		d.pool.open = false
	}
}

// decodeRewardPoolWinnings 解一条评论的 reward_pool_rewards。条目形如
// {"amount": 53}；未知字段忽略，非正数（0 / 缺金额）的记录不记，免得评论
// 底下冒出一个「金币池 +0」。
func decodeRewardPoolWinnings(raw json.RawMessage) []int {
	var out []int
	for _, item := range rawArray(raw) {
		o := asObject(item)
		if o == nil {
			// 线上也可能直接给数字数组。
			if n, ok := rawNumber(item); ok && n > 0 {
				out = append(out, int(math.Round(n)))
			}
			continue
		}
		amount := 0
		if n, ok := o.number("amount", "coins"); ok {
			amount = int(math.Round(n))
		}
		if amount > 0 {
			out = append(out, amount)
		}
	}
	return out
}

// rewardPoolWinningsTotal 是这条评论从金币池里一共领到多少金币。
func rewardPoolWinningsTotal(amounts []int) int {
	total := 0
	for _, n := range amounts {
		if n > 0 {
			total += n
		}
	}
	return total
}

// rewardPoolHint 是金币池的统一说明。详情页只展示金币池状态与这一句，不再
// 写「按概率发放、领到的人标在评论下面」这类额外描述。
const rewardPoolHint = "金币会随着回复数量动态增加，首次回复有概率获得金币池中部分金币奖励。"

// rewardPoolStateText 是金币池结束时的一行补充状态。结束与过期对用户来说都
// 是「已结束」，归到同一句；进行中（含余额为 0）不再单独标注，直接展示金币
// 数即可。
func rewardPoolStateText(p *rewardPoolInfo) string {
	if p != nil && (p.Finished || p.Expired) {
		return "（已结束）"
	}
	return ""
}

// canFundRewardPool 报告能否给详情页的金币池注资：只有作者本人、且池子还在
// 进行时才有入口。别人的帖子只能看状态，不给出「投入金币」。
func (a *app) canFundRewardPool(d *detailState, p *rewardPoolInfo) bool {
	return d != nil && d.topic != nil && a.isMine(d.topic.Author) &&
		p != nil && !p.Finished && !p.Expired
}

// rewardPoolSection 画正文末尾的金币池：一行「💰 N 金币」，结束时补一行
// 「（已结束）」并把金币归零，底下是统一说明。注资入口只给自己的帖子，池子
// 结束或过期后不再提供。
func (a *app) rewardPoolSection(c *ui.Context, d *detailState, p *rewardPoolInfo) {
	if p == nil || !p.Known {
		return
	}
	t := c.Theme()
	ended := p.Finished || p.Expired
	coins := 0
	if !ended && p.CoinsKnown {
		coins = p.CurrentCoins
	}
	ui.Divider(c)
	ui.Column(c).FillWidth().Padding(12, 14).Gap(6).Radius(10).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, fmt.Sprintf("💰 %d 金币", coins)).FontSize(commentFontSize).Bold()
			ui.Spacer(c).Grow(1)
			if a.canFundRewardPool(d, p) {
				btn := ui.Button(c, "投入金币").FontSize(11).Padding(2, 8).Shrink(0)
				btn.Label("投入金币").Tooltip("给这个金币池补充金币（200～1000）")
				if btn.Clicked() {
					a.openRewardPoolPopup(d)
				}
			}
		})
		if ended {
			ui.Text(c, rewardPoolStateText(p)).FontSize(metaSmallFontSize).TextColor(t.TextMuted)
		}
		ui.Text(c, rewardPoolHint).FontSize(metaFontSize).TextColor(t.TextMuted).MaxLines(3)
	})
}

/* ------------------------------------------------------------------
   注资弹窗
--------------------------------------------------------------- */

// openRewardPoolPopup 打开给金币池注资的 Lobe Modal。未登录时先走授权，回来后
// 再打开；金额为空或越界时先夹到合法区间。弹窗本身挂在 detailContent 上。
func (a *app) openRewardPoolPopup(d *detailState) {
	if d == nil || !a.detailLive(d) {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能给金币池注资。")
		return
	}
	a.prepareRewardPoolAmount(d)
	d.pool.open = true
}

// rewardPoolDialog 是金币池注资弹窗：点「投入金币」后盖在整页上，填金额或点
// 常用金额，确认后才扣币。标题栏与 Esc / 点窗外关闭由 Lobe Modal 提供。
func (a *app) rewardPoolDialog(c *ui.Context, d *detailState) {
	if !d.pool.open {
		return
	}
	feedback.Modal(c, &d.pool.open, feedback.ModalProps{
		Title:    "给金币池注资",
		Width:    380,
		NoFooter: true,
	}, func() {
		a.rewardPoolPanel(c, d)
	})
}

// prepareRewardPoolAmount 把注资金额夹到 200～1000，并清掉上一次的错误。
func (a *app) prepareRewardPoolAmount(d *detailState) {
	d.pool.err = ""
	if d.pool.amount < rewardPoolCoinsLo {
		d.pool.amount = rewardPoolCoinsLo
	}
	if d.pool.amount > rewardPoolCoinsHi {
		d.pool.amount = rewardPoolCoinsHi
	}
}

// rewardPoolPanel 是注资弹窗里的内容：金额、常用金额、余额与确认按钮。金额由
// 用户填或点常用金额，按钮上写着具体数字，点下去才会扣币。
func (a *app) rewardPoolPanel(c *ui.Context, d *detailState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(8).Children(func() {
		if d == nil || d.topic == nil || d.topic.RewardPool == nil {
			return
		}
		if !a.auth.loggedIn() {
			a.loginHint(c, "登录后才能给金币池注资。")
			return
		}
		if p := d.topic.RewardPool; p.CoinsKnown {
			ui.Textf(c, "池中还有 %d 金币。", p.CurrentCoins).
				FontSize(11).TextColor(t.TextMuted)
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.NumberInput(c, &d.pool.amount, rewardPoolCoinsLo, rewardPoolCoinsHi, 50).
				Label("注资金额").Width(120)
			ui.Text(c, "金币").FontSize(metaFontSize).TextColor(t.TextMuted)
			if a.coins != nil && a.coins.balanceOK {
				ui.Textf(c, "余额 %s", coinThousands(a.coins.balance)).
					FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
		ui.Text(c, "常用金额").FontSize(11).TextColor(t.TextMuted)
		ui.Row(c).Gap(4).Wrap().Children(func() {
			for _, amount := range []int{200, 500, 1000} {
				a.rewardPoolAmountChip(c, d, amount)
			}
		})
		ui.Text(c, "会从你的余额扣除，确认后不可撤销。").
			FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		if d.pool.err != "" {
			ui.Text(c, d.pool.err).FontSize(11).TextColor(t.Danger).MaxLines(2)
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c).Grow(1)
			if d.pool.busy {
				lobeSpin(c, feedback.SpinProps{CustomSize: 13})
				ui.Text(c, "正在投入…").FontSize(11).TextColor(t.TextMuted)
				return
			}
			btn := ui.PrimaryButton(c,
				fmt.Sprintf("投入 %d 金币", rewardPoolAmount(d.pool.amount)))
			if btn.Clicked() {
				a.submitRewardPool(d)
			}
		})
	})
}

// rewardPoolAmountChip 是一枚常用注资金额：当前金额高亮，点一下填进输入框。
// 读屏名带上「常用金额」，免得和下面的确认按钮撞名。
func (a *app) rewardPoolAmountChip(c *ui.Context, d *detailState, amount int) {
	t := c.Theme()
	chip := ui.Button(c, strconv.Itoa(amount)).FontSize(11).Padding(2, 8).Radius(999)
	if rewardPoolAmount(d.pool.amount) == amount {
		chip.Background(t.Accent).TextColor(t.AccentText)
	} else {
		chip.Background(t.Surface).TextColor(t.TextMuted).Border(1, t.Border)
	}
	chip.Label("常用金额 " + strconv.Itoa(amount) + " 金币")
	chip.Tooltip("填入 " + strconv.Itoa(amount) + " 金币")
	if chip.Clicked() {
		d.pool.amount = float64(amount)
		d.pool.err = ""
	}
}

// rewardPoolAmount 把输入框里的浮点数收成 200～1000 的整数。
func rewardPoolAmount(amount float64) int {
	n := int(math.Round(amount))
	if n < rewardPoolCoinsLo {
		return rewardPoolCoinsLo
	}
	if n > rewardPoolCoinsHi {
		return rewardPoolCoinsHi
	}
	return n
}

// submitRewardPool 提交一笔金币池注资。同一笔（金额不变）的超时重试与授权后
// 重放复用同一个幂等键；金额变了才换新键。成功后关掉弹窗、刷新余额并重读详情，
// 让池子的余额以服务端为准。
func (a *app) submitRewardPool(d *detailState) {
	if d == nil || !a.detailLive(d) || d.pool.busy || d.topic == nil || d.topic.RewardPool == nil {
		return
	}
	// 界面入口已经按作者过滤；这里再拦一道，防止直接调用绕过限制。
	if !a.isMine(d.topic.Author) {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能给金币池注资。")
		return
	}
	amount := rewardPoolAmount(d.pool.amount)
	sig := fmt.Sprintf("%s|%d", d.shortID, amount)
	if d.pool.key == "" || d.pool.sig != sig {
		d.pool.key = randomURLSafe(24)
		d.pool.sig = sig
	}
	key := d.pool.key
	d.pool.busy = true
	d.pool.err = ""
	d.pool.gen++
	gen := d.pool.gen
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), interactTimeout)
	go func() {
		defer cancel()
		err := a.client.fundRewardPool(ctx, d.shortID, key, amount)
		a.publish(func() {
			if !a.detailLive(d) || d.pool.gen != gen {
				return
			}
			d.pool.busy = false
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthError(err) {
					return
				}
				d.pool.err = friendlyError(err)
				return
			}
			// 成功才清键与收起弹窗：失败时界面停在原金额，重试仍用同一个键。
			d.pool.key = ""
			d.pool.sig = ""
			d.pool.open = false
			d.pool.err = ""
			if p := d.topic.RewardPool; p != nil && p.CoinsKnown {
				p.CurrentCoins += amount
			}
			a.toast(a.detailToastFor(d), fmt.Sprintf("已向金币池投入 %d 金币", amount))
			a.loadTopic()
		})
	}()
}

// replyRewardPoolWinnings 是评论头部的一枚小标签：这条评论从帖子金币池里
// 一共领到多少金币。没领到（或接口没给记录）时不画。
func (a *app) replyRewardPoolWinnings(c *ui.Context, r replyItem) {
	total := rewardPoolWinningsTotal(r.RewardPoolWinnings)
	if total <= 0 {
		return
	}
	t := c.Theme()
	ui.Text(c, fmt.Sprintf("金币池 +%d", total)).FontSize(metaSmallFontSize).
		TextColor(t.Accent).
		Tooltip("这条评论从帖子金币池里领到的金币")
}
