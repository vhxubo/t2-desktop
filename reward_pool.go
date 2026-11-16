package main

import (
	"encoding/json"
	"fmt"
	"math"

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
// 领取由服务端在浏览帖子时按概率完成，客户端不调接口；注资这个应用还没有入口，
// 但发帖时可以随帖带上初始金币（见 compose.go 的金币池块）。所以这里只做只读
// 展示：正文末尾一块状态，评论头部标出这条评论领到了多少。

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

// rewardPoolStateText 是金币池当前状态的一句话：结束与过期分开说，余额已知
// 且为 0 时说明是领完了，其余都算还在发放。
func rewardPoolStateText(p *rewardPoolInfo) string {
	switch {
	case p == nil:
		return ""
	case p.Expired:
		return "已过期"
	case p.Finished:
		return "已结束"
	case p.CoinsKnown && p.CurrentCoins <= 0:
		return "已领完"
	}
	return "进行中"
}

// rewardPoolSection 画正文末尾的金币池状态：池里还剩多少、现在是哪种状态。
// 它是只读块，没有任何入口——领取由服务端在浏览时按概率完成，注资这个应用
// 还没有入口（见文件头的说明）。
func (a *app) rewardPoolSection(c *ui.Context, p *rewardPoolInfo) {
	if p == nil || !p.Known {
		return
	}
	t := c.Theme()
	ui.Divider(c)
	ui.Column(c).FillWidth().Padding(12, 14).Gap(6).Radius(10).
		Background(t.Surface).Border(1, t.Border).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, "金币池").FontSize(commentFontSize).Bold()
			ui.Spacer(c).Grow(1)
			ui.Text(c, rewardPoolStateText(p)).FontSize(metaSmallFontSize).
				TextColor(t.TextMuted)
		})
		line := "浏览这篇帖子时按概率发放，领到的人会标在各自的评论下面。"
		if p.CoinsKnown {
			line = fmt.Sprintf("池中还有 %d 金币。", p.CurrentCoins) + line
		}
		ui.Text(c, line).FontSize(metaFontSize).TextColor(t.TextMuted).MaxLines(3)
	})
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
