package main

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// 评论框上方的「发评金币与徽章」状态栏。
//
// 站点在评论框上方实时显示字数与预计扣费，并在更省金币的徽章存在时给一个
// 切换入口。这里把同一套规则搬到桌面端：规则本身都是纯函数（便于测试），
// 界面在 comment_cost_ui.go，请求与状态在文件后半段。
//
// 规则（与站点一致）：
//
//	基础扣费阶梯     字数 >5000 → 200、>2000 → 120、>1000 → 60、>100 → 30、
//	                 其余 10 金币；开放 API 没有下发这个数值的端点，阶梯跟着
//	                 站点一起改（见 commentBaseCost）
//	固定扣费徽章     badge.attributes.comment_cost（负数表示扣除，如「前排打手」
//	                 恒定 8 金币）直接覆盖阶梯
//	减免徽章         comment_cost_percent（评论专属，如「都听我说！」0.3）与
//	                 coin_system_cost_percent（金币系统普适，如「大平衡者」0.3）
//	                 按乘法叠加；同类可能有多枚，全部参与比价
//	最优解           只有比「不戴」更便宜时才给切换按钮；同价的多枚并排成
//	                 二选一，戴着其中一枚时另一枚仍作为同价替换出现；比最优更贵
//	                 的候选不展示，也不提供「摘下」
//	走个面           attributes.daily_active_gain_percent（活跃度 +20%）与评论
//	                 费用无关，只在状态栏最左侧给一个佩戴 / 摘下开关
//
// 潘多拉事件优先于徽章：回归起点锁徽章、妙笔生花固定费用、多说多写只校验字数
// （未达不能发送）、金币升降机按事件改价、小表情给出约束（见
// pandoraCommentEffect）。

const (
	// commentCostTTL 是徽章与佩戴状态的缓存时间：徽章很少变，佩戴状态由本应用
	// 自己切换时即时更新，这里只在长时间停留后顺便校准一次。
	commentCostTTL = 10 * time.Minute
	// commentCostRetry 是加载失败后的退避起点：评论框打开着时不要反复发同一个
	// 注定失败的请求。
	commentCostRetry = time.Minute
	// commentCostTimeout 是读徽章 / 佩戴状态与切换徽章的超时。
	commentCostTimeout = 20 * time.Second
)

/* ------------------------------------------------------------------
   基础扣费与徽章定价
--------------------------------------------------------------- */

// commentBaseCost 是不戴任何徽章时发一条评论的基础扣费（金币），阶梯镜像
// 站点前端的评论计费规则：字数越多扣越多。
func commentBaseCost(n int) int {
	switch {
	case n > 5000:
		return 200
	case n > 2000:
		return 120
	case n > 1000:
		return 60
	case n > 100:
		return 30
	}
	return 10
}

// commentDiscount 是这枚徽章对「发评金币」的减免比例；没有减免字段时第二个
// 返回值为 false。0.3 表示省 30%，-0.2 表示费用上浮 20%（如「夜间猫头鹰」）。
func (b badgeWallEntry) commentDiscount() (float64, bool) {
	own, hasOwn := b.CommentCostPercent, b.HasCommentCostPercent
	sys, hasSys := b.CoinSystemCostPercent, b.HasCoinSystemCostPercent
	switch {
	case hasOwn && hasSys:
		// 两个字段同时存在时按乘法叠加（站内目前没有这种徽章，防御性处理）。
		return 1 - (1-own)*(1-sys), true
	case hasOwn:
		return own, true
	case hasSys:
		return sys, true
	}
	return 0, false
}

// commentCostWith 是戴某一枚徽章发一条评论的预计扣费；badge 为 nil 表示不戴。
// 固定扣费优先于减免（两者同时存在时以固定值为准），与站点一致。
func commentCostWith(n int, badge *badgeWallEntry) int {
	base := commentBaseCost(n)
	if badge == nil {
		return base
	}
	if badge.HasCommentCost {
		return int(math.Round(math.Abs(badge.CommentCost)))
	}
	if off, ok := badge.commentDiscount(); ok {
		return int(math.Round(float64(base) * (1 - off)))
	}
	return base
}

// commentPercentText 是减免比例的中文描述：0.3 → 「减免 30%」，-0.2 → 「增加 20%」。
func commentPercentText(ratio float64) string {
	pct := int(math.Round(ratio * 100))
	if pct >= 0 {
		return "减免 " + strconv.Itoa(pct) + "%"
	}
	return "增加 " + strconv.Itoa(-pct) + "%"
}

// commentBadgeSets 是两类会改变发评费用的徽章。同类可能有多枚（「都听我说！」
// 与「大平衡者」都是 30% 减免），全部留下按实际价格比价。
type commentBadgeSets struct {
	Fixed   []badgeWallEntry // attributes.comment_cost
	Percent []badgeWallEntry // comment_cost_percent / coin_system_cost_percent
}

// all 把两组拍平成一个候选池。
func (s commentBadgeSets) all() []badgeWallEntry {
	out := make([]badgeWallEntry, 0, len(s.Fixed)+len(s.Percent))
	out = append(out, s.Fixed...)
	return append(out, s.Percent...)
}

// classifyCommentBadges 从「我的徽章」里挑出两类评论费用徽章。只认
// comment_cost / comment_cost_percent / coin_system_cost_percent，站内其他
// attributes（exp、float_percent、daily_active_gain_percent…）不会被误判。
func classifyCommentBadges(list []badgeWallEntry) commentBadgeSets {
	var out commentBadgeSets
	seen := make(map[string]bool, len(list))
	for _, b := range list {
		if b.BadgeID == "" || seen[b.BadgeID] {
			continue
		}
		if b.HasCommentCost {
			out.Fixed = append(out.Fixed, b)
			seen[b.BadgeID] = true
			continue
		}
		if _, ok := b.commentDiscount(); ok {
			out.Percent = append(out.Percent, b)
			seen[b.BadgeID] = true
		}
	}
	// 固定扣费从便宜到贵、减免从多到少：同价时排在前面的那枚先成为候选。
	sort.SliceStable(out.Fixed, func(i, j int) bool {
		return math.Abs(out.Fixed[i].CommentCost) < math.Abs(out.Fixed[j].CommentCost)
	})
	sort.SliceStable(out.Percent, func(i, j int) bool {
		a, _ := out.Percent[i].commentDiscount()
		b, _ := out.Percent[j].commentDiscount()
		return a > b
	})
	return out
}

// wornCommentBadges 是当前佩戴中的费用徽章。
func wornCommentBadges(set commentBadgeSets, worn map[string]bool) []badgeWallEntry {
	var out []badgeWallEntry
	for _, b := range set.all() {
		if worn[b.BadgeID] {
			out = append(out, b)
		}
	}
	return out
}

// commentOptimalBadges 是当前字数下真正能省的「最优解」：只保留费用最低、
// 且低于不戴的候选。同价的会同时入选，供状态栏给出二选一。
func commentOptimalBadges(n int, set commentBadgeSets) []badgeWallEntry {
	cands := set.all()
	if len(cands) == 0 {
		return nil
	}
	base := commentBaseCost(n)
	best := base
	for i := range cands {
		if cost := commentCostWith(n, &cands[i]); cost < best {
			best = cost
		}
	}
	if best >= base {
		return nil
	}
	out := make([]badgeWallEntry, 0, 2)
	for i := range cands {
		if commentCostWith(n, &cands[i]) == best {
			out = append(out, cands[i])
		}
	}
	return out
}

/* ------------------------------------------------------------------
   「走个面」：活跃度开关
--------------------------------------------------------------- */

// 「走个面」在徽章库里是 id do_me_a_favor、属性 daily_active_gain_percent 0.2
// （活跃度 +20%）。它不改变评论金币，所以不进入费用推荐，只在状态栏最左侧
// 提供独立的佩戴 / 摘下入口。
const (
	activityBadgeID   = "do_me_a_favor"
	activityBadgeName = "走个面"
)

// isActivityBadge 认「走个面」：有 daily_active_gain_percent 属性，或者 code /
// 名字对得上。
func isActivityBadge(b badgeWallEntry) bool {
	if b.HasDailyActiveGainPercent {
		return true
	}
	if b.Code == activityBadgeID {
		return true
	}
	return strings.Contains(b.Name, activityBadgeName)
}

// findActivityBadge 在「我的徽章」里找「走个面」。
func findActivityBadge(list []badgeWallEntry) (badgeWallEntry, bool) {
	for _, b := range list {
		if b.BadgeID != "" && isActivityBadge(b) {
			return b, true
		}
	}
	return badgeWallEntry{}, false
}

// activityGainPercent 是活跃度加成百分比；兼容 0.2（比例）与 20（百分数）两种
// 下发形态，缺失时第二个返回值为 false。
func activityGainPercent(b badgeWallEntry) (int, bool) {
	if !b.HasDailyActiveGainPercent {
		return 0, false
	}
	v := b.DailyActiveGainPercent
	if v <= 1 && v > -1 {
		v *= 100
	}
	return int(math.Round(v)), true
}

// activityBadgeLabel 是「走个面」按钮上的文字：名字 + 加成，佩戴中再加上状态。
func activityBadgeLabel(b badgeWallEntry, worn bool) string {
	name := profileBadgeDisplayName(b)
	if strings.TrimSpace(b.Name) == "" {
		name = activityBadgeName
	}
	if gain, ok := activityGainPercent(b); ok {
		name += " +" + strconv.Itoa(gain) + "%"
	}
	if worn {
		return name + "（佩戴中）"
	}
	return name
}

/* ------------------------------------------------------------------
   潘多拉事件
--------------------------------------------------------------- */

// commentPandoraEffect 是生效中的潘多拉事件对「发评」的影响。每个盒子只带
// 一个事件，因此这些标志互斥（小表情约束可以与别的并存）。
type commentPandoraEffect struct {
	Active bool
	Code   string
	Name   string
	// ExpireAt 是事件截止时间（原样保留接口给的值，展示时转东八区）。
	ExpireAt string
	// LockBadge 对应「回归起点」：徽章全部掉落且锁定，无法佩戴。
	LockBadge bool
	// FixedCost 对应「妙笔生花」：发评固定为这么多金币。
	FixedCost    int
	HasFixedCost bool
	// FreeComment 表示事件文案写明「评论不扣除金币」（多说多写的金币档）。
	FreeComment bool
	// ModifierPct 对应「金币升降机」等按百分比改价的事件。
	ModifierPct int
	HasModifier bool
	// MinLength 是评论正文的最少字数（「多说多写」）；未达不能发送。
	MinLength int
	// NeedEmoji 表示评论里必须带自定义表情（「小表情」）。
	NeedEmoji bool
}

var (
	pandoraFixedCostRe = regexp.MustCompile(`固定为\s*(\d+)\s*金币`)
	pandoraPercentRe   = regexp.MustCompile(`([+-]?\d+)\s*%`)
	pandoraMinLengthRe = regexp.MustCompile(`(\d+)\s*个字`)
	pandoraFreeCoinRe  = regexp.MustCompile(`不扣除?金币`)
)

// pandoraNamedNumber 取事件数值：优先服务端下发的 rolled_value，缺失时从
// event_title 文案兜底解析（测试盒子的字段没有在文档里逐个列出，两条路径都
// 保留）。
func pandoraNamedNumber(box pandoraBox, re *regexp.Regexp) (int, bool) {
	if box.RolledKnown {
		return int(math.Round(box.RolledValue)), true
	}
	if m := re.FindStringSubmatch(box.EventTitle); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			return n, true
		}
	}
	return 0, false
}

// pandoraCommentEffect 解析生效中的潘多拉事件对发评费用估算的影响。事件没
// 生效（apply_at 未到 / expire_at 已过 / 没有 event_code）时返回零值。
func pandoraCommentEffect(status pandoraStatus, now time.Time) commentPandoraEffect {
	if !status.BoxKnown {
		return commentPandoraEffect{}
	}
	box := status.Box
	code := strings.TrimSpace(box.EventCode)
	if code == "" {
		return commentPandoraEffect{}
	}
	if box.ApplyAt != "" {
		if t, ok := profileTime(box.ApplyAt); ok && t.After(now) {
			return commentPandoraEffect{}
		}
	}
	if box.ExpireAt != "" {
		if t, ok := profileTime(box.ExpireAt); ok && !now.Before(t) {
			return commentPandoraEffect{}
		}
	}
	name := strings.TrimSpace(box.EventName)
	if name == "" {
		name = code
	}
	pe := commentPandoraEffect{
		Active:   true,
		Code:     code,
		Name:     name,
		ExpireAt: box.ExpireAt,
		// 事件文案里写「评论不扣除金币」时一律按 0 金币算，不论档位。
		FreeComment: pandoraFreeCoinRe.MatchString(box.EventTitle),
	}
	switch code {
	case "badge_drop_lock":
		pe.LockBadge = true
	case "fixed_text_cost":
		pe.FixedCost, pe.HasFixedCost = pandoraNamedNumber(box, pandoraFixedCostRe)
	case "coin_modifier_percent":
		pe.ModifierPct, pe.HasModifier = pandoraNamedNumber(box, pandoraPercentRe)
	case "comment_min_length":
		if n, ok := pandoraNamedNumber(box, pandoraMinLengthRe); ok {
			pe.MinLength = n
		}
	}
	// comment_constraint 是盒子结构化下发的约束，比文案解析可靠，优先采用。
	if box.CommentMinLength > 0 {
		pe.MinLength = box.CommentMinLength
	}
	pe.NeedEmoji = box.NeedCustomEmoji || code == "comment_custom_emoji_required"
	return pe
}

/* ------------------------------------------------------------------
   状态栏模型（纯函数）
--------------------------------------------------------------- */

// commentCostTone 是状态栏文字的语义色彩：由界面映射到主题颜色。
type commentCostTone int

const (
	commentToneMuted commentCostTone = iota // 普通说明
	commentToneGood                         // 有利（当前最省 / 可省 N）
	commentToneWarn                         // 需要注意（比不戴贵 / 换一枚不划算）
	commentToneBad                          // 硬约束（潘多拉字数没到）
)

// commentCostAction 是状态栏里的一枚切换按钮：换成这枚更省金币的徽章。
type commentCostAction struct {
	Label string
	Tip   string
	Badge badgeWallEntry
}

// commentActivityAction 是「走个面」的佩戴 / 摘下开关。
type commentActivityAction struct {
	Label  string
	Tip    string
	Badge  badgeWallEntry
	Worn   bool
	Locked bool
}

// commentCostModel 是状态栏一帧的全部内容。
type commentCostModel struct {
	Count  string
	Status string
	Hint   string
	Tone   commentCostTone
	// Warn 表示字数要标红提醒（潘多拉字数没达到）。
	Warn     bool
	Actions  []commentCostAction
	Activity *commentActivityAction
}

// commentCostInput 是算一帧状态栏需要的一切。
type commentCostInput struct {
	Length   int
	Badges   commentBadgeSets
	Worn     map[string]bool
	Pandora  commentPandoraEffect
	Activity *badgeWallEntry
	// Busy 表示正在佩戴 / 摘下徽章：按钮禁用并显示进度。
	Busy bool
}

// needMoreText 是「差多少字」的说明，潘多拉的硬约束都用它。
func needMoreText(short, minLength int) string {
	return fmt.Sprintf("还差 %d 字才能发送（需 ≥%d 字）", short, minLength)
}

// commentCostModelFor 算出状态栏这一帧要显示什么。潘多拉事件优先于徽章，
// 徽章只在没有事件接管时参与比价。
func commentCostModelFor(in commentCostInput) commentCostModel {
	base := commentBaseCost(in.Length)
	m := commentCostModel{Count: fmt.Sprintf("%d 字", in.Length)}
	pe := in.Pandora
	needMore := 0
	if pe.Active && pe.MinLength > 0 {
		// 字数门槛直接读写数区：窄的时候看不到 hint 也能判断能不能发。
		m.Count = fmt.Sprintf("%d/%d 字", in.Length, pe.MinLength)
		if in.Length < pe.MinLength {
			needMore = pe.MinLength - in.Length
		}
	}
	m.Activity = commentActivityActionFor(in)

	if pe.Active {
		until := ""
		if clock := pandoraClock(pe.ExpireAt); clock != "" {
			until = " · " + clock + " 截止"
		}
		switch {
		case pe.LockBadge:
			m.Status = "潘多拉「" + pe.Name + "」生效中" + until + " · 徽章已掉落锁定"
			m.Hint, m.Tone, m.Warn = "下次开盒前无法佩戴徽章，按无徽章计费", commentToneBad, true
			return m
		case pe.FreeComment:
			m.Status = "潘多拉「" + pe.Name + "」生效中 · 发评 0 金币"
			switch {
			case needMore > 0:
				m.Hint, m.Tone, m.Warn = needMoreText(needMore, pe.MinLength), commentToneBad, true
			case pe.MinLength > 0:
				m.Hint = fmt.Sprintf("生效期间评论不扣金币（需 ≥%d 字）", pe.MinLength)
				m.Tone = commentToneGood
			default:
				m.Hint, m.Tone = "生效期间评论不扣金币", commentToneGood
			}
			return m
		case pe.MinLength > 0:
			m.Status = fmt.Sprintf("潘多拉「%s」生效中 · 需 ≥%d 字", pe.Name, pe.MinLength)
			if needMore > 0 {
				m.Hint, m.Tone, m.Warn = needMoreText(needMore, pe.MinLength), commentToneBad, true
			} else {
				m.Hint = fmt.Sprintf("已满足 %d 字，可以发送", pe.MinLength)
				m.Tone = commentToneGood
			}
			return m
		case pe.HasFixedCost:
			m.Status = fmt.Sprintf("潘多拉「%s」生效中 · 发评固定 %d 金币", pe.Name, pe.FixedCost)
			m.Hint = "潘多拉已固定费用，徽章不参与计算"
			return m
		case pe.Code == "coin_modifier_percent" || pe.HasModifier:
			cost := int(math.Round(float64(base) * (1 + float64(pe.ModifierPct)/100)))
			m.Status = fmt.Sprintf("潘多拉「%s」生效中%s · 发评约 %d 金币", pe.Name, until, cost)
			m.Hint = "徽章不叠加：发评费用按事件计算"
			return m
		}
	}

	worn := wornCommentBadges(in.Badges, in.Worn)
	var cur *badgeWallEntry
	if len(worn) > 0 {
		cur = &worn[0]
	}
	curCost := commentCostWith(in.Length, cur)
	switch {
	case len(worn) > 1:
		// 站点如何处理多枚费用徽章未知，明确说明估算可能不准。
		names := make([]string, 0, len(worn))
		for _, b := range worn {
			names = append(names, profileBadgeDisplayName(b))
		}
		m.Status = fmt.Sprintf("当前：%s · 发评约 %d 金币（同戴，估算可能不准）",
			strings.Join(names, " + "), curCost)
		m.Hint, m.Tone, m.Warn = "多枚同戴，估算可能不准", commentToneWarn, true
		return finishCommentPandora(m, pe)
	case cur == nil:
		m.Status = fmt.Sprintf("当前：未佩戴 · 发评约 %d 金币", curCost)
	default:
		m.Status = fmt.Sprintf("当前：%s · 发评约 %d 金币",
			profileBadgeDisplayName(*cur), curCost)
	}

	// 最优解里除去当前佩戴的那枚，剩下的都可以替换：未佩戴时是全部最优解，
	// 戴着其中一枚时同价的并重徽章仍放在右边。最多两枚并排。
	targets := make([]badgeWallEntry, 0, 2)
	for _, b := range commentOptimalBadges(in.Length, in.Badges) {
		if in.Worn[b.BadgeID] {
			continue
		}
		if len(targets) >= 2 {
			break
		}
		targets = append(targets, b)
	}
	if len(targets) == 0 {
		switch {
		case cur != nil && curCost > base:
			m.Hint = fmt.Sprintf("比不戴贵 %d 金币", curCost-base)
			m.Tone, m.Warn = commentToneWarn, true
		case cur != nil:
			m.Hint, m.Tone = "当前最省", commentToneGood
		}
		return finishCommentPandora(m, pe)
	}

	for i := range targets {
		b := targets[i]
		cost := commentCostWith(in.Length, &b)
		m.Actions = append(m.Actions, commentCostAction{
			Label: fmt.Sprintf("%s %d 金币", profileBadgeDisplayName(b), cost),
			Tip: fmt.Sprintf("切换到「%s」 · 发评约 %d 金币；切换徽章可能扣除金币",
				profileBadgeDisplayName(b), cost),
			Badge: b,
		})
	}
	targetCost := commentCostWith(in.Length, &targets[0])
	switch {
	case cur != nil && curCost == targetCost:
		// 当前已是同价最优，右边的按钮只是换成另一枚并重徽章。
		m.Hint, m.Tone = "当前最省", commentToneGood
	case cur != nil && isDiscountOnly(*cur) && !isDiscountOnly(targets[0]):
		// 减免输给固定扣费：把原因说清楚（「减免 30% 不如固定 8 金币」）。
		off, _ := cur.commentDiscount()
		m.Hint = commentPercentText(off) + fmt.Sprintf(" 不如固定 %d 金币", targetCost)
		m.Tone, m.Warn = commentToneWarn, true
	default:
		m.Hint = fmt.Sprintf("可省 %d 金币", curCost-targetCost)
		m.Tone = commentToneGood
	}
	return finishCommentPandora(m, pe)
}

// isDiscountOnly 表示这枚徽章只靠减免改价（没有固定扣费字段）。
func isDiscountOnly(b badgeWallEntry) bool {
	if b.HasCommentCost {
		return false
	}
	_, ok := b.commentDiscount()
	return ok
}

// finishCommentPandora 给徽章分支收尾：字数 / 费用类事件已在前面接管，这里
// 只把小表情约束追加到说明里。
func finishCommentPandora(m commentCostModel, pe commentPandoraEffect) commentCostModel {
	if !pe.Active || !pe.NeedEmoji {
		return m
	}
	if hint := strings.TrimSpace(m.Hint); hint != "" {
		m.Hint = hint + " · 潘多拉要求评论包含自定义表情"
		return m
	}
	m.Hint = "潘多拉要求评论包含自定义表情"
	m.Tone = commentToneWarn
	return m
}

// commentActivityActionFor 造「走个面」按钮的显示数据；没有这枚徽章时返回 nil。
func commentActivityActionFor(in commentCostInput) *commentActivityAction {
	b := in.Activity
	if b == nil || b.BadgeID == "" {
		return nil
	}
	worn := in.Worn[b.BadgeID]
	locked := in.Pandora.Active && in.Pandora.LockBadge
	label := activityBadgeLabel(*b, worn)
	name := profileBadgeDisplayName(*b)
	if strings.TrimSpace(b.Name) == "" {
		name = activityBadgeName
	}
	var tip string
	switch {
	case locked:
		tip = fmt.Sprintf("潘多拉「%s」锁定中，下次开盒前无法佩戴徽章", in.Pandora.Name)
	case worn:
		tip = "点击摘下「" + name + "」" + activityGainTip(*b)
	default:
		tip = "点击佩戴「" + name + "」" + activityGainTip(*b)
	}
	if in.Busy {
		label = "处理中…"
	}
	return &commentActivityAction{Label: label, Tip: tip, Badge: *b, Worn: worn, Locked: locked}
}

// activityGainTip 是「走个面」悬停提示里的加成说明。
func activityGainTip(b badgeWallEntry) string {
	if gain, ok := activityGainPercent(b); ok {
		return fmt.Sprintf("，活跃度 +%d%%", gain)
	}
	return "，活跃度加成已生效"
}

/* ------------------------------------------------------------------
   数据：我的徽章 + 佩戴状态
--------------------------------------------------------------- */

// commentCostState 是状态栏的数据：我的徽章（含 attributes）与佩戴状态。
// 两者都是账号级的，与具体帖子无关，所以挂在 app 上而不是 detailState 上；
// 只在 UI 线程读写，网络结果经 publish 回来。
type commentCostState struct {
	seq     uint64
	loading bool
	loaded  bool
	err     string

	badges   []badgeWallEntry
	equipped map[string]bool
	sets     commentBadgeSets
	activity *badgeWallEntry

	// loadedAt 是上次成功加载的时间；triedAt / failures 用于失败退避。
	loadedAt time.Time
	triedAt  time.Time
	failures int

	// busyID 是正在佩戴 / 摘下的徽章 id：一次只提交一个，避免连点重复扣币。
	busyID string
}

func newCommentCostState() *commentCostState { return &commentCostState{} }

// commentCostBarVisible 回答评论框上方要不要画状态栏：未登录时没有账号数据
// 可算，字数仍由调用方按原样显示。
func (a *app) commentCostBarVisible() bool {
	return a.commentCost != nil && a.auth != nil && a.auth.loggedIn()
}

// ensureCommentCostData 在评论框出现时按需加载徽章与佩戴状态。它有缓存与
// 失败退避，所以每帧调用只会真正发一次请求。
func (a *app) ensureCommentCostData() {
	st := a.commentCost
	if st == nil || !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	// 潘多拉事件会接管整条状态栏：和标题栏入口读同一份今日状态，已经读过就
	// 不再重复请求。
	a.loadPandoraEntry()
	if st.loading || st.busyID != "" {
		return
	}
	now := time.Now()
	if st.loaded {
		if now.Sub(st.loadedAt) < commentCostTTL {
			return
		}
	} else if !st.triedAt.IsZero() && now.Sub(st.triedAt) < commentCostRetryAfter(st.failures) {
		return
	}
	a.loadCommentCostData()
}

// commentCostRetryAfter 是第 failures 次失败后的退避时间：1、2、4 分钟，封顶
// 4 分钟。评论框可能一直开着，失败后不能每帧重发同一个注定失败的请求。
func commentCostRetryAfter(failures int) time.Duration {
	delay := commentCostRetry
	for i := 1; i < failures && delay < 4*commentCostRetry; i++ {
		delay *= 2
	}
	return delay
}

// loadCommentCostData 读我的徽章与佩戴状态。两个请求顺序发出：都只需要
// profile scope，缺权限时同一次授权可以把它们一起补上。
func (a *app) loadCommentCostData() {
	st := a.commentCost
	if st == nil || !a.auth.loggedIn() {
		return
	}
	st.seq++
	seq := st.seq
	st.loading = true
	st.triedAt = time.Now()
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), commentCostTimeout)
	go func() {
		defer cancel()
		badges, err := a.client.fetchMyBadges(ctx)
		var equipped map[string]bool
		if err == nil {
			equipped, err = a.client.fetchEquippedBadgeIDs(ctx)
		}
		a.publish(func() {
			if a.commentCost != st || st.seq != seq {
				return
			}
			st.loading = false
			if err != nil {
				st.failures++
				// 缺 profile 权限（旧令牌）或登录失效时走全局授权链路，授权
				// 成功后重跑这次加载；普通网络错误只在状态栏里说明并给重试。
				if a.handleAuthErrorFor(a.replyAuthTarget(), err, a.reloadCommentCostData) {
					return
				}
				if !st.loaded {
					st.err = friendlyError(err)
				}
				return
			}
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			st.failures = 0
			st.loaded = true
			st.err = ""
			st.loadedAt = time.Now()
			for i := range badges {
				badges[i].Equipped = equipped[badges[i].BadgeID]
			}
			st.badges = badges
			st.equipped = equipped
			st.sets = classifyCommentBadges(badges)
			st.activity = nil
			if b, ok := findActivityBadge(badges); ok {
				st.activity = &b
			}
		})
	}()
}

// reloadCommentCostData 丢掉已经拿到的徽章重新读一次（状态栏的「重试」与
// 授权重放都走这里）。只在 UI 线程调用。
func (a *app) reloadCommentCostData() {
	st := a.commentCost
	if st == nil || st.loading || st.busyID != "" {
		return
	}
	st.loaded = false
	st.err = ""
	st.triedAt = time.Time{}
	a.loadCommentCostData()
}

// noteBadgeEquipped 在别处（「我的」徽章页）切换徽章后更新状态栏的佩戴状态：
// 两处看的是同一个账号的同一份佩戴状态，不能一边显示「佩戴中」另一边还是旧的。
func (a *app) noteBadgeEquipped(badgeID string, equip bool) {
	st := a.commentCost
	if st == nil || !st.loaded || badgeID == "" {
		return
	}
	if equip {
		// 站点同一时刻只戴一枚：佩戴会替换掉当前徽章。
		st.equipped = map[string]bool{badgeID: true}
		return
	}
	delete(st.equipped, badgeID)
}

// syncProfileBadgeState 把状态栏里的佩戴结果同步给「我的」徽章页。
func (a *app) syncProfileBadgeState(badgeID string, equip bool) {
	ps := a.profile
	if ps == nil || !ps.badgeLoaded {
		return
	}
	for i := range ps.badges {
		if ps.badges[i].BadgeID != badgeID {
			if equip {
				ps.badges[i].Equipped = false
			}
			continue
		}
		ps.badges[i].Equipped = equip
	}
	a.syncProfileEquippedBadges(ps)
}

/* ------------------------------------------------------------------
   切换徽章
--------------------------------------------------------------- */

// setCommentCostBadge 佩戴（equip）或摘下（!equip）状态栏里的一枚徽章。
// 费用徽章只做「替换」，摘下只给「走个面」用。key 为空表示一次新的点击；
// 授权重放会带着同一个 key 回来，避免重复扣币。
//
// 网页端切换前会先用 /api/badges/equip/check-cost 问一次这次要不要收费；开放
// API v1 没有这个端点（api-economy.md 的「徽章佩戴」只有 equip），所以这里不预检，
// 把「切换徽章可能扣除金币」写进按钮的悬停提示，服务端确认的结果（含扣费金额）
// 用窗口内 toast 原样报告。
func (a *app) setCommentCostBadge(badge badgeWallEntry, equip bool, key string) {
	st := a.commentCost
	if st == nil || badge.BadgeID == "" || st.busyID != "" || !a.auth.loggedIn() {
		return
	}
	name := profileBadgeDisplayName(badge)
	if equip {
		// 回归起点生效期间佩戴请求必然失败，先在本地拦下来，别让用户白等一次
		// 请求（状态栏正常时不会走到这里）。
		if pe := a.commentCostPandora(); pe.Active && pe.LockBadge {
			a.replyToast(fmt.Sprintf("潘多拉「%s」生效中，下次开盒前无法佩戴徽章", pe.Name))
			return
		}
	}
	if key == "" {
		key = randomURLSafe(24)
	}
	st.busyID = badge.BadgeID
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), commentCostTimeout)
	go func() {
		defer cancel()
		message, err := a.client.setBadgeEquipped(ctx, badge.BadgeID, key, equip)
		a.publish(func() {
			if a.commentCost != st {
				return
			}
			st.busyID = ""
			// 请求期间换了账号 / 退出了登录：这次结果不属于当前会话。
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthErrorFor(a.replyAuthTarget(), err, func() { a.setCommentCostBadge(badge, equip, key) }) {
					return
				}
				verb := "切换"
				if !equip {
					verb = "摘下"
				}
				a.replyToast("「" + name + "」" + verb + "失败：" + friendlyError(err))
				return
			}
			a.noteBadgeEquipped(badge.BadgeID, equip)
			a.syncProfileBadgeState(badge.BadgeID, equip)
			if message == "" {
				if equip {
					message = "已佩戴「" + name + "」"
				} else {
					message = "已摘下「" + name + "」"
				}
			}
			a.replyToast(message)
			// 佩戴可能扣金币，交给全局金币事件刷新标题栏与金币窗口。
			a.coinsChanged(coinEventBadge)
		})
	}()
}

// commentCostPandora 是状态栏读到的潘多拉事件效果。
func (a *app) commentCostPandora() commentPandoraEffect {
	status, known := pandoraDisplayStatus(a.pandora)
	if !known {
		return commentPandoraEffect{}
	}
	return pandoraCommentEffect(status, pandoraNow())
}

// commentCostShortfall 是潘多拉字数门槛还差多少字；0 表示不受约束或已经够。
func (a *app) commentCostShortfall(content string) (short, minLength int) {
	pe := a.commentCostPandora()
	if !pe.Active || pe.MinLength <= 0 {
		return 0, 0
	}
	if n := commentLength(content); n < pe.MinLength {
		return pe.MinLength - n, pe.MinLength
	}
	return 0, pe.MinLength
}

// commentLength 是评论字数的口径：按 rune 计，与状态栏里显示的「N 字」一致。
// 状态栏每帧、每次击键都会算它，用 RuneCountInString 避免为长草稿反复
// 分配一个等长的 []rune（一帧里字数、模型、门槛检查会各算一次）。
func commentLength(s string) int {
	return utf8.RuneCountInString(s)
}

/* ------------------------------------------------------------------
   界面用的小工具
--------------------------------------------------------------- */

// commentCostModelNow 按当前状态与草稿算出状态栏内容。数据还没到或者没登录
// 时不返回模型（第二个返回值为 false），由界面只画字数与进度。
func (a *app) commentCostModelNow(draft string) (commentCostModel, bool) {
	st := a.commentCost
	if st == nil || !st.loaded {
		return commentCostModel{}, false
	}
	return commentCostModelFor(commentCostInput{
		Length:   commentLength(draft),
		Badges:   st.sets,
		Worn:     st.equipped,
		Pandora:  a.commentCostPandora(),
		Activity: st.activity,
		Busy:     st.busyID != "",
	}), true
}
