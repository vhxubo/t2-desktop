package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

/* ------------------------------------------------------------------
   规则：基础扣费、徽章定价、最优解
--------------------------------------------------------------- */

// fixedCostBadge 造一枚固定扣费徽章（负数表示扣除）。
func fixedCostBadge(id, name string, cost float64) badgeWallEntry {
	return badgeWallEntry{BadgeID: id, Name: name, HasCommentCost: true, CommentCost: cost}
}

// percentCostBadge 造一枚评论专属减免徽章。
func percentCostBadge(id, name string, pct float64) badgeWallEntry {
	return badgeWallEntry{
		BadgeID: id, Name: name,
		HasCommentCostPercent: true, CommentCostPercent: pct,
	}
}

// systemCostBadge 造一枚金币系统普适减免徽章。
func systemCostBadge(id, name string, pct float64) badgeWallEntry {
	return badgeWallEntry{
		BadgeID: id, Name: name,
		HasCoinSystemCostPercent: true, CoinSystemCostPercent: pct,
	}
}

// 基础扣费阶梯与站点前端一致：字数越多扣越多，边界值按「大于」判断。
func TestCommentBaseCostLadder(t *testing.T) {
	cases := []struct {
		length int
		want   int
	}{
		{0, 10}, {1, 10}, {100, 10},
		{101, 30}, {1000, 30},
		{1001, 60}, {2000, 60},
		{2001, 120}, {5000, 120},
		{5001, 200}, {20000, 200},
	}
	for _, c := range cases {
		if got := commentBaseCost(c.length); got != c.want {
			t.Errorf("commentBaseCost(%d) = %d，期望 %d", c.length, got, c.want)
		}
	}
}

// 固定扣费徽章按绝对值覆盖阶梯；减免徽章在阶梯上打折；两者都没有时按阶梯。
func TestCommentCostWithBadges(t *testing.T) {
	fixed := fixedCostBadge("b-fixed", "前排打手", -8)
	percent := percentCostBadge("b-pct", "都听我说！", 0.3)
	plain := badgeWallEntry{BadgeID: "b-plain", Name: "早起鸟"}

	if got := commentCostWith(120, &fixed); got != 8 {
		t.Errorf("固定扣费徽章下 120 字应扣 8 金币，得到 %d", got)
	}
	if got := commentCostWith(120, &percent); got != 21 {
		t.Errorf("30%% 减免下 120 字应扣 21 金币，得到 %d", got)
	}
	if got := commentCostWith(10, &percent); got != 7 {
		t.Errorf("30%% 减免下 10 字应扣 7 金币，得到 %d", got)
	}
	if got := commentCostWith(120, &plain); got != 30 {
		t.Errorf("无关徽章不应改变扣费，得到 %d", got)
	}
	if got := commentCostWith(120, nil); got != 30 {
		t.Errorf("不戴徽章应按阶梯计费，得到 %d", got)
	}
}

// 评论专属减免与金币系统普适减免按乘法叠加；负比例表示费用上浮。
func TestCommentDiscountCombinesPercent(t *testing.T) {
	both := badgeWallEntry{
		BadgeID: "b-both", HasCommentCostPercent: true, CommentCostPercent: 0.3,
		HasCoinSystemCostPercent: true, CoinSystemCostPercent: 0.3,
	}
	off, ok := both.commentDiscount()
	if !ok || off < 0.50 || off > 0.52 {
		t.Errorf("两项减免叠加比例 = %v（ok=%v），期望约 0.51", off, ok)
	}
	if got := commentCostWith(101, &both); got != 15 {
		t.Errorf("叠加减免后 101 字应扣约 15 金币，得到 %d", got)
	}

	owl := systemCostBadge("b-owl", "夜间猫头鹰", -0.2)
	if off, ok := owl.commentDiscount(); !ok || off != -0.2 {
		t.Errorf("负比例应原样保留：%v（ok=%v）", off, ok)
	}
	if got := commentCostWith(120, &owl); got != 36 {
		t.Errorf("费用上浮 20%% 后应扣 36 金币，得到 %d", got)
	}

	// 0 是合法值：0% 减免不能被当成「没这个属性」，100% 减免要算出 0 金币。
	zero := percentCostBadge("b-zero", "零减免", 0)
	if off, ok := zero.commentDiscount(); !ok || off != 0 {
		t.Errorf("0 减免应被认成有效属性：%v（ok=%v）", off, ok)
	}
	if got := commentCostWith(120, &zero); got != 30 {
		t.Errorf("0%% 减免仍是全额：%d", got)
	}
	free := percentCostBadge("b-free", "免单", 1)
	if off, ok := free.commentDiscount(); !ok || off != 1 {
		t.Errorf("100%% 减免应被认成有效属性：%v（ok=%v）", off, ok)
	}
	if got := commentCostWith(120, &free); got != 0 {
		t.Errorf("100%% 减免应算出 0 金币，得到 %d", got)
	}
}

// 分类：固定扣费与减免分开、同 id 去重、互不相关的 attributes 不误判。
func TestClassifyCommentBadges(t *testing.T) {
	list := []badgeWallEntry{
		fixedCostBadge("b-fixed-12", "前排打手 II", -12),
		percentCostBadge("b-pct-30", "都听我说！", 0.3),
		fixedCostBadge("b-fixed-8", "前排打手", -8),
		systemCostBadge("b-sys-30", "大平衡者", 0.3),
		percentCostBadge("b-pct-10", "省一点", 0.1),
		{BadgeID: "b-plain", Name: "早起鸟", Level: 2},
		fixedCostBadge("b-fixed-8", "重复条目", -8),
	}
	set := classifyCommentBadges(list)
	if len(set.Fixed) != 2 || set.Fixed[0].BadgeID != "b-fixed-8" || set.Fixed[1].BadgeID != "b-fixed-12" {
		t.Errorf("固定扣费分类不对（应按价格从低到高）：%+v", set.Fixed)
	}
	if len(set.Percent) != 3 {
		t.Fatalf("减免分类数量不对：%+v", set.Percent)
	}
	// 减免从多到少：两枚 30% 排在前面，10% 垫底。
	if set.Percent[2].BadgeID != "b-pct-10" {
		t.Errorf("减免没有按比例从多到少排序：%+v", set.Percent)
	}
	if got := len(set.all()); got != 5 {
		t.Errorf("候选池应去重后剩 5 枚，得到 %d：%+v", got, set.all())
	}
}

// 最优解：只保留真正比「不戴」便宜的候选，同价的全部入选。
func TestCommentOptimalBadges(t *testing.T) {
	set := classifyCommentBadges([]badgeWallEntry{
		fixedCostBadge("b-fixed-8", "前排打手", -8),
		percentCostBadge("b-pct-30", "都听我说！", 0.3),
		systemCostBadge("b-sys-30", "大平衡者", 0.3),
	})
	// 120 字：不戴 30、减免 21、固定 8 → 只有固定扣费是最优。
	opt := commentOptimalBadges(120, set)
	if len(opt) != 1 || opt[0].BadgeID != "b-fixed-8" {
		t.Errorf("120 字的最优解应只有固定 8 金币：%+v", opt)
	}
	// 10 字：不戴 10、减免 7、固定 8 → 减免最优，两枚同价的都入选。
	opt = commentOptimalBadges(10, set)
	if len(opt) != 2 {
		t.Fatalf("10 字时两枚 30%% 徽章应同时入选：%+v", opt)
	}
	for _, b := range opt {
		if b.BadgeID == "b-fixed-8" {
			t.Errorf("比最优更贵的固定扣费不该入选：%+v", opt)
		}
	}
	// 没有比不戴更便宜的时候不给任何建议。
	if opt := commentOptimalBadges(120, classifyCommentBadges([]badgeWallEntry{
		fixedCostBadge("b-expensive", "贵徽章", -50),
	})); len(opt) != 0 {
		t.Errorf("比不戴更贵时不该有最优解：%+v", opt)
	}
}

/* ------------------------------------------------------------------
   模型：字数、状态、建议
--------------------------------------------------------------- */

// modelOf 用给定条件算一帧状态栏内容。
func modelOf(length int, badges []badgeWallEntry, worn map[string]bool,
	pe commentPandoraEffect) commentCostModel {
	return commentCostModelFor(commentCostInput{
		Length:  length,
		Badges:  classifyCommentBadges(badges),
		Worn:    worn,
		Pandora: pe,
	})
}

// 没有徽章时状态栏只说当前费用，不给任何建议。
func TestCommentCostModelWithoutBadges(t *testing.T) {
	m := modelOf(120, nil, nil, commentPandoraEffect{})
	if m.Count != "120 字" {
		t.Errorf("字数 = %q", m.Count)
	}
	if m.Status != "当前：未佩戴 · 发评约 30 金币" {
		t.Errorf("状态 = %q", m.Status)
	}
	if m.Hint != "" || len(m.Actions) != 0 || m.Warn {
		t.Errorf("没有徽章时不该有建议：%+v", m)
	}
}

// 有更省的徽章时给一枚切换按钮，并说明能省多少金币。
func TestCommentCostModelSuggestsCheaperBadge(t *testing.T) {
	badges := []badgeWallEntry{percentCostBadge("b-pct", "都听我说！", 0.3)}
	m := modelOf(120, badges, nil, commentPandoraEffect{})
	if len(m.Actions) != 1 {
		t.Fatalf("应给一枚切换按钮：%+v", m)
	}
	if m.Actions[0].Label != "都听我说！ 21 金币" {
		t.Errorf("按钮文案 = %q", m.Actions[0].Label)
	}
	if m.Hint != "可省 9 金币" || m.Tone != commentToneGood {
		t.Errorf("说明 = %q（tone %v）", m.Hint, m.Tone)
	}
}

// 同价的并重徽章并排成二选一；戴着其中一枚时另一枚仍作为同价替换出现。
func TestCommentCostModelTiedBadges(t *testing.T) {
	badges := []badgeWallEntry{
		percentCostBadge("b-a", "都听我说！", 0.3),
		systemCostBadge("b-b", "大平衡者", 0.3),
	}
	m := modelOf(120, badges, nil, commentPandoraEffect{})
	if len(m.Actions) != 2 {
		t.Fatalf("同价的两枚应并排出现：%+v", m.Actions)
	}
	m = modelOf(120, badges, map[string]bool{"b-a": true}, commentPandoraEffect{})
	if len(m.Actions) != 1 || m.Actions[0].Badge.BadgeID != "b-b" {
		t.Fatalf("戴着一枚时应只提供另一枚替换：%+v", m.Actions)
	}
	if m.Status != "当前：都听我说！ · 发评约 21 金币" {
		t.Errorf("状态 = %q", m.Status)
	}
	if m.Hint != "当前最省" {
		t.Errorf("同价最优时说明 = %q", m.Hint)
	}
}

// 减免输给固定扣费时把原因说清楚，而不是只报「可省」。
func TestCommentCostModelDiscountLosesToFixed(t *testing.T) {
	badges := []badgeWallEntry{
		percentCostBadge("b-pct", "都听我说！", 0.3),
		fixedCostBadge("b-fixed", "前排打手", -8),
	}
	m := modelOf(120, badges, map[string]bool{"b-pct": true}, commentPandoraEffect{})
	if len(m.Actions) != 1 || m.Actions[0].Label != "前排打手 8 金币" {
		t.Fatalf("应建议换成固定扣费徽章：%+v", m.Actions)
	}
	if m.Hint != "减免 30% 不如固定 8 金币" || !m.Warn || m.Tone != commentToneWarn {
		t.Errorf("说明 = %q（warn=%v tone=%v）", m.Hint, m.Warn, m.Tone)
	}
}

// 戴着一枚比不戴更贵的徽章时提醒多花多少。
func TestCommentCostModelWarnsExpensiveBadge(t *testing.T) {
	m := modelOf(120, []badgeWallEntry{fixedCostBadge("b-cost", "贵徽章", 50)},
		map[string]bool{"b-cost": true}, commentPandoraEffect{})
	if m.Hint != "比不戴贵 20 金币" || !m.Warn {
		t.Errorf("说明 = %q（warn=%v）", m.Hint, m.Warn)
	}
	if len(m.Actions) != 0 {
		t.Errorf("比不戴更贵时不该给切换建议：%+v", m.Actions)
	}
}

// 多枚费用徽章同戴时费用叠加方式未知，只提醒，不提供摘下。
func TestCommentCostModelMultipleWorn(t *testing.T) {
	badges := []badgeWallEntry{
		fixedCostBadge("b-fixed", "前排打手", -8),
		percentCostBadge("b-pct", "都听我说！", 0.3),
	}
	m := modelOf(120, badges, map[string]bool{"b-fixed": true, "b-pct": true},
		commentPandoraEffect{})
	if !strings.Contains(m.Status, "同戴，估算可能不准") {
		t.Errorf("状态没有说明同戴：%q", m.Status)
	}
	if m.Hint != "多枚同戴，估算可能不准" || !m.Warn {
		t.Errorf("说明 = %q（warn=%v）", m.Hint, m.Warn)
	}
	if len(m.Actions) != 0 {
		t.Errorf("同戴时不该再给建议：%+v", m.Actions)
	}
}

/* ------------------------------------------------------------------
   潘多拉事件优先
--------------------------------------------------------------- */

// 回归起点：徽章掉落锁定，按无徽章计费，不给切换按钮。
func TestCommentCostModelPandoraLock(t *testing.T) {
	pe := commentPandoraEffect{
		Active: true, Code: "badge_drop_lock", Name: "回归起点", LockBadge: true,
	}
	m := modelOf(120, []badgeWallEntry{percentCostBadge("b-pct", "都听我说！", 0.3)},
		nil, pe)
	if !strings.Contains(m.Status, "徽章已掉落锁定") {
		t.Errorf("状态 = %q", m.Status)
	}
	if m.Hint != "下次开盒前无法佩戴徽章，按无徽章计费" || !m.Warn {
		t.Errorf("说明 = %q（warn=%v）", m.Hint, m.Warn)
	}
	if len(m.Actions) != 0 {
		t.Errorf("锁定期间不该给切换按钮：%+v", m.Actions)
	}
}

// 多说多写：只盯字数，未达门槛标红并说明还差多少；达标的说明可以发送。
func TestCommentCostModelPandoraMinLength(t *testing.T) {
	pe := commentPandoraEffect{Active: true, Code: "comment_min_length", Name: "多说多写", MinLength: 100}
	m := modelOf(40, nil, nil, pe)
	if m.Count != "40/100 字" {
		t.Errorf("字数 = %q", m.Count)
	}
	if m.Status != "潘多拉「多说多写」生效中 · 需 ≥100 字" {
		t.Errorf("状态 = %q", m.Status)
	}
	if m.Hint != "还差 60 字才能发送（需 ≥100 字）" || !m.Warn || m.Tone != commentToneBad {
		t.Errorf("说明 = %q（warn=%v tone=%v）", m.Hint, m.Warn, m.Tone)
	}
	if len(m.Actions) != 0 {
		t.Errorf("字数门槛档不该给徽章建议：%+v", m.Actions)
	}

	m = modelOf(120, nil, nil, pe)
	if m.Hint != "已满足 100 字，可以发送" || m.Warn {
		t.Errorf("达标后的说明 = %q（warn=%v）", m.Hint, m.Warn)
	}
}

// 金币档的「评论不扣金币」按 0 金币算，字数门槛仍然有效。
func TestCommentCostModelPandoraFreeComment(t *testing.T) {
	pe := commentPandoraEffect{
		Active: true, Code: "comment_min_length", Name: "多说多写",
		FreeComment: true, MinLength: 100,
	}
	m := modelOf(120, nil, nil, pe)
	if m.Status != "潘多拉「多说多写」生效中 · 发评 0 金币" {
		t.Errorf("状态 = %q", m.Status)
	}
	if m.Hint != "生效期间评论不扣金币（需 ≥100 字）" || m.Tone != commentToneGood {
		t.Errorf("说明 = %q（tone %v）", m.Hint, m.Tone)
	}

	m = modelOf(40, nil, nil, pe)
	if m.Hint != "还差 60 字才能发送（需 ≥100 字）" || !m.Warn {
		t.Errorf("未达字数时的说明 = %q（warn=%v）", m.Hint, m.Warn)
	}
}

// 妙笔生花固定费用：徽章不参与，也不给切换按钮。
func TestCommentCostModelPandoraFixedCost(t *testing.T) {
	pe := commentPandoraEffect{Active: true, Code: "fixed_text_cost", Name: "妙笔生花", FixedCost: 12, HasFixedCost: true}
	m := modelOf(120, []badgeWallEntry{percentCostBadge("b-pct", "都听我说！", 0.3)}, nil, pe)
	if m.Status != "潘多拉「妙笔生花」生效中 · 发评固定 12 金币" {
		t.Errorf("状态 = %q", m.Status)
	}
	if m.Hint != "潘多拉已固定费用，徽章不参与计算" {
		t.Errorf("说明 = %q", m.Hint)
	}
	if len(m.Actions) != 0 {
		t.Errorf("固定费用时不该给建议：%+v", m.Actions)
	}
}

// 金币升降机按事件改价，徽章不叠加；截止时间写进状态。
func TestCommentCostModelPandoraModifier(t *testing.T) {
	pe := commentPandoraEffect{
		Active: true, Code: "coin_modifier_percent", Name: "金币升降机",
		ModifierPct: 20, HasModifier: true,
		ExpireAt: "2026-11-16T10:00:00.000Z",
	}
	m := modelOf(120, nil, nil, pe)
	if !strings.Contains(m.Status, "发评约 36 金币") {
		t.Errorf("状态 = %q", m.Status)
	}
	if m.Hint != "徽章不叠加：发评费用按事件计算" {
		t.Errorf("说明 = %q", m.Hint)
	}
}

// 小表情约束只追加一句说明，不覆盖已有的费用结论。
func TestCommentCostModelPandoraEmojiConstraint(t *testing.T) {
	pe := commentPandoraEffect{Active: true, Code: "comment_custom_emoji_required", Name: "小表情", NeedEmoji: true}
	m := modelOf(120, nil, nil, pe)
	if !strings.Contains(m.Hint, "潘多拉要求评论包含自定义表情") {
		t.Errorf("说明 = %q", m.Hint)
	}

	pe.NeedEmoji = true
	m = modelOf(120, []badgeWallEntry{percentCostBadge("b-pct", "都听我说！", 0.3)}, nil, pe)
	if m.Hint != "可省 9 金币 · 潘多拉要求评论包含自定义表情" {
		t.Errorf("说明 = %q", m.Hint)
	}
}

/* ------------------------------------------------------------------
   「走个面」与事件解析
--------------------------------------------------------------- */

// 「走个面」认属性、code 与名字三种途径，加成兼容 0.2 与 20 两种写法。
func TestActivityBadgeDetection(t *testing.T) {
	byAttr := badgeWallEntry{BadgeID: "b1", Name: "走个面", HasDailyActiveGainPercent: true, DailyActiveGainPercent: 0.2}
	byCode := badgeWallEntry{BadgeID: "b2", Code: activityBadgeID, Name: "露面"}
	if !isActivityBadge(byAttr) || !isActivityBadge(byCode) {
		t.Error("走个面没有被认出来")
	}
	if isActivityBadge(badgeWallEntry{BadgeID: "b3", Name: "早起鸟"}) {
		t.Error("无关徽章被认成了走个面")
	}
	if gain, ok := activityGainPercent(byAttr); !ok || gain != 20 {
		t.Errorf("0.2 应换算成 20%%，得到 %d（ok=%v）", gain, ok)
	}
	if got := activityBadgeLabel(byAttr, false); got != "走个面 +20%" {
		t.Errorf("按钮文案 = %q", got)
	}
	if got := activityBadgeLabel(byAttr, true); got != "走个面 +20%（佩戴中）" {
		t.Errorf("佩戴中的按钮文案 = %q", got)
	}
	if b, ok := findActivityBadge([]badgeWallEntry{{BadgeID: "x"}, byAttr}); !ok || b.BadgeID != "b1" {
		t.Errorf("没有从徽章列表里找到走个面：%+v（%v）", b, ok)
	}
}

// 走个面的开关只关心佩戴与锁定，不影响次数 / 费用那一行。
func TestCommentCostModelActivityAction(t *testing.T) {
	act := badgeWallEntry{BadgeID: "b-act", Name: "走个面", HasDailyActiveGainPercent: true, DailyActiveGainPercent: 0.2}
	m := commentCostModelFor(commentCostInput{
		Length: 120, Worn: map[string]bool{}, Activity: &act,
	})
	if m.Activity == nil || m.Activity.Label != "走个面 +20%" || m.Activity.Worn {
		t.Fatalf("走个面开关不对：%+v", m.Activity)
	}
	m = commentCostModelFor(commentCostInput{
		Length: 120, Worn: map[string]bool{"b-act": true}, Activity: &act,
	})
	if m.Activity == nil || !m.Activity.Worn {
		t.Errorf("佩戴状态没有反映到开关上：%+v", m.Activity)
	}
	// 潘多拉锁定期间按钮禁用，说明里写清原因。
	m = commentCostModelFor(commentCostInput{
		Length: 120, Worn: map[string]bool{}, Activity: &act,
		Pandora: commentPandoraEffect{Active: true, Code: "badge_drop_lock", Name: "回归起点", LockBadge: true},
	})
	if m.Activity == nil || !m.Activity.Locked {
		t.Fatalf("锁定期间开关应禁用：%+v", m.Activity)
	}
	if !strings.Contains(m.Activity.Tip, "无法佩戴徽章") {
		t.Errorf("开关说明 = %q", m.Activity.Tip)
	}
}

// 事件解析：生效窗口、结构化字段优先、文案兜底、rolled_value 优先。
func TestPandoraCommentEffect(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.Local)
	status := func(box pandoraBox) pandoraStatus {
		return pandoraStatus{Known: true, BoxKnown: true, Box: box}
	}

	pe := pandoraCommentEffect(status(pandoraBox{EventCode: "badge_drop_lock", EventName: "回归起点"}), now)
	if !pe.Active || !pe.LockBadge {
		t.Fatalf("回归起点没有生效：%+v", pe)
	}

	// 还没到生效时间 / 已经过期都不算生效。
	if pe := pandoraCommentEffect(status(pandoraBox{
		EventCode: "badge_drop_lock", ApplyAt: now.Add(24 * time.Hour).Format(time.RFC3339Nano),
	}), now); pe.Active {
		t.Errorf("未到生效时间的事件不应生效：%+v", pe)
	}
	if pe := pandoraCommentEffect(status(pandoraBox{
		EventCode: "badge_drop_lock", ExpireAt: now.Add(-24 * time.Hour).Format(time.RFC3339Nano),
	}), now); pe.Active {
		t.Errorf("已过期的事件不应生效：%+v", pe)
	}

	// 站点字段优先于文案。
	pe = pandoraCommentEffect(status(pandoraBox{
		EventCode: "comment_min_length", EventName: "多说多写",
		CommentMinLength: 100,
	}), now)
	if pe.MinLength != 100 {
		t.Errorf("结构化字数门槛没有采用：%+v", pe)
	}

	// 字段缺失时从 event_title 兜底解析。
	cases := []struct {
		box  pandoraBox
		want func(commentPandoraEffect) bool
		desc string
	}{
		{
			box:  pandoraBox{EventCode: "fixed_text_cost", EventTitle: "发评固定为 12 金币"},
			want: func(pe commentPandoraEffect) bool { return pe.HasFixedCost && pe.FixedCost == 12 },
			desc: "固定费用",
		},
		{
			box:  pandoraBox{EventCode: "coin_modifier_percent", EventTitle: "金币消耗 +20%"},
			want: func(pe commentPandoraEffect) bool { return pe.HasModifier && pe.ModifierPct == 20 },
			desc: "按比例改价",
		},
		{
			box:  pandoraBox{EventCode: "comment_min_length", EventTitle: "评论需 ≥100 个字"},
			want: func(pe commentPandoraEffect) bool { return pe.MinLength == 100 },
			desc: "字数门槛",
		},
		{
			box:  pandoraBox{EventCode: "comment_min_length", EventTitle: "评论不扣除金币"},
			want: func(pe commentPandoraEffect) bool { return pe.FreeComment },
			desc: "评论不扣金币",
		},
		{
			// rolled_value 是服务端下发的结构化数值，优先于文案。
			box: pandoraBox{EventCode: "fixed_text_cost", EventTitle: "发评固定为 12 金币",
				RolledValue: 9, RolledKnown: true},
			want: func(pe commentPandoraEffect) bool { return pe.HasFixedCost && pe.FixedCost == 9 },
			desc: "rolled_value 优先",
		},
	}
	for _, c := range cases {
		if pe := pandoraCommentEffect(status(c.box), now); !c.want(pe) {
			t.Errorf("%s 没有解析出来：%+v", c.desc, pe)
		}
	}
}

// 盒子里的 comment_constraint 是结构化下发的评论约束，盒子顶层与 payload
// 里都要认。
func TestDecodePandoraCommentConstraint(t *testing.T) {
	raw := `{"box_instance_id":"i1","event_code":"comment_min_length",
		"event_name":"多说多写","comment_constraint":{"min_length":120,
		"custom_emoji_required":true}}`
	status := decodePandoraStatus([]byte(raw))
	if !status.BoxKnown {
		t.Fatal("盒子没有解析出来")
	}
	if status.Box.CommentMinLength != 120 || !status.Box.NeedCustomEmoji {
		t.Errorf("评论区约束没有解析出来：%+v", status.Box)
	}

	raw = `{"event_code":"comment_custom_emoji_required",
		"payload":{"comment_constraint":{"custom_emoji_required":true}}}`
	status = decodePandoraStatus([]byte(raw))
	if !status.Box.NeedCustomEmoji {
		t.Errorf("payload 里的评论约束没有解析出来：%+v", status.Box)
	}
}

// 徽章 attributes 里的四项费用 / 活跃度字段要照着解码，其余属性忽略。
func TestDecodeBadgeCostAttributes(t *testing.T) {
	raw := `[{"badge_id":"b1","badge":{"id":"b1","code":"do_me_a_favor","name":"走个面",
		"attributes":{"comment_cost":-8,"comment_cost_percent":0.3,
		"coin_system_cost_percent":0.2,"daily_active_gain_percent":0.2,"exp":10}}}]`
	badges := decodeUserBadges([]byte(raw))
	if len(badges) != 1 {
		t.Fatalf("徽章数量不对：%+v", badges)
	}
	b := badges[0]
	if b.Code != "do_me_a_favor" {
		t.Errorf("徽章 code 没有解出来：%q", b.Code)
	}
	if !b.HasCommentCost || b.CommentCost != -8 {
		t.Errorf("comment_cost 没解出来：%+v", b)
	}
	if !b.HasCommentCostPercent || b.CommentCostPercent != 0.3 {
		t.Errorf("comment_cost_percent 没解出来：%+v", b)
	}
	if !b.HasCoinSystemCostPercent || b.CoinSystemCostPercent != 0.2 {
		t.Errorf("coin_system_cost_percent 没解出来：%+v", b)
	}
	if !b.HasDailyActiveGainPercent || b.DailyActiveGainPercent != 0.2 {
		t.Errorf("daily_active_gain_percent 没解出来：%+v", b)
	}

	// 没有 attributes 的徽章四项都必须是「没有」，不能把零值当成 0 减免。
	plain := decodeUserBadges([]byte(`[{"badge_id":"b2","badge":{"id":"b2","name":"早起鸟"}}]`))
	if len(plain) != 1 || plain[0].HasCommentCost || plain[0].HasCommentCostPercent ||
		plain[0].HasCoinSystemCostPercent || plain[0].HasDailyActiveGainPercent {
		t.Errorf("无关徽章被解成了费用徽章：%+v", plain)
	}
}

/* ------------------------------------------------------------------
   请求与动作
--------------------------------------------------------------- */

// commentCostApp 造一个登录态、数据已就绪的评论框环境。
func commentCostApp(t *testing.T) (*app, *ui.Tester) {
	t.Helper()
	a := detailAppWithReplies(t)
	// 详情测试替上了自己的会话对象，客户端还指着旧的那一个：这里接回来，
	// 否则佩戴请求不会带令牌。
	a.client.Auth = a.auth
	a.autoLoad = false // 状态栏的加载与图片预取都不发请求
	st := a.commentCost
	st.loaded = true
	st.loadedAt = time.Now()
	st.badges = []badgeWallEntry{
		fixedCostBadge("b-fixed", "前排打手", -8),
		percentCostBadge("b-pct", "都听我说！", 0.3),
		activityBadgeEntry("b-act", "走个面", 0.2),
	}
	st.equipped = map[string]bool{}
	st.sets = classifyCommentBadges(st.badges)
	act, _ := findActivityBadge(st.badges)
	st.activity = &act
	a.detail.draft = strings.Repeat("字", 120)
	tt := ui.NewTester(a.view, 1100, 900)
	tt.Frame()
	return a, tt
}

// activityBadgeEntry 造一枚「走个面」。
func activityBadgeEntry(id, name string, gain float64) badgeWallEntry {
	return badgeWallEntry{
		BadgeID: id, Name: name,
		HasDailyActiveGainPercent: true, DailyActiveGainPercent: gain,
	}
}

// 状态栏按当前字数给出字数、当前费用与建议；按钮按名字点得到。
func TestCommentCostBarRenders(t *testing.T) {
	_, tt := commentCostApp(t)
	for _, want := range []string{
		"120 字", "当前：未佩戴 · 发评约 30 金币", "可省 22 金币",
		"前排打手 8 金币", "走个面 +20%",
	} {
		if !tt.HasText(want) {
			t.Errorf("状态栏缺少 %q：%q", want, tt.Texts())
		}
	}
	// 未登录时没有状态栏，字数回到工具条右端。
	a := testApp(t)
	a.openDetail(Item{ShortID: "JBYGf2A", URL: "https://2libra.com/post/work/JBYGf2A"})
	topic, _ := decodeTopic([]byte(detailTopicJSON), "JBYGf2A")
	a.detail.topic = &topic
	a.detail.draft = strings.Repeat("字", 120)
	ttLoggedOut := ui.NewTester(a.view, 1100, 900)
	ttLoggedOut.Frame()
	if ttLoggedOut.HasText("当前：未佩戴 · 发评约 30 金币") {
		t.Error("未登录时不该出现费用状态栏")
	}
	if !ttLoggedOut.HasText("120 字") {
		t.Errorf("未登录时工具条右端应保留字数：%q", ttLoggedOut.Texts())
	}
}

// 点建议按钮发一次佩戴请求：POST、带幂等键、成功后状态栏跟到新徽章，结果
// 用窗口内 toast 报告。
func TestCommentCostBarSwitchesBadge(t *testing.T) {
	var mu sync.Mutex
	var methods, paths, keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		paths = append(paths, r.URL.Path)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("佩戴请求没有带令牌：%q", got)
		}
		fmt.Fprint(w, envelopeJSON(`{"message":"已佩戴「前排打手」，扣除 5 金币"}`))
	}))
	defer srv.Close()

	a, tt := commentCostApp(t)
	a.client.Base = srv.URL + "/api"
	if err := tt.Click("前排打手 8 金币"); err != nil {
		t.Fatalf("点切换按钮失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	waitFor(t, func() bool {
		return a.commentCost.busyID == "" && a.commentCost.equipped["b-fixed"]
	}, "佩戴完成")
	mu.Lock()
	gotMethods := append([]string(nil), methods...)
	gotPaths := append([]string(nil), paths...)
	gotKeys := append([]string(nil), keys...)
	mu.Unlock()
	if len(gotMethods) != 1 || gotMethods[0] != http.MethodPost {
		t.Fatalf("佩戴方法不对：%v", gotMethods)
	}
	if gotPaths[0] != "/api/v1/me/badges/b-fixed/equip" {
		t.Errorf("佩戴路径 = %s", gotPaths[0])
	}
	if gotKeys[0] == "" {
		t.Error("佩戴请求没有带幂等键")
	}

	tt.Frame()
	if !tt.HasText("已佩戴「前排打手」，扣除 5 金币") {
		t.Errorf("结果没有用 toast 报告：%q", tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("当前：前排打手 · 发评约 8 金币") {
		t.Errorf("状态栏没有跟上新徽章：%q", tt.Texts())
	}
}

// 「走个面」是独立的佩戴 / 摘下开关：佩戴发 POST，摘下发 DELETE，与评论
// 字数、费用无关。
func TestCommentCostActivityToggle(t *testing.T) {
	var mu sync.Mutex
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		fmt.Fprint(w, envelopeJSON(`{"message":"已更新佩戴状态"}`))
	}))
	defer srv.Close()

	a, tt := commentCostApp(t)
	a.client.Base = srv.URL + "/api"
	if err := tt.Click("走个面 +20%"); err != nil {
		t.Fatalf("点走个面失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	waitFor(t, func() bool {
		return a.commentCost.busyID == "" && a.commentCost.equipped["b-act"]
	}, "佩戴走个面")

	tt.Frame()
	if err := tt.Click("走个面 +20%（佩戴中）"); err != nil {
		t.Fatalf("再点一次没能摘下：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	waitFor(t, func() bool {
		return a.commentCost.busyID == "" && !a.commentCost.equipped["b-act"]
	}, "摘下走个面")
	mu.Lock()
	defer mu.Unlock()
	if len(methods) != 2 || methods[0] != http.MethodPost || methods[1] != http.MethodDelete {
		t.Errorf("走个面的佩戴 / 摘下方法不对：%v", methods)
	}
}

// 潘多拉锁徽章期间不切徽章：状态栏不给按钮，动作入口本身也会在本地拦下来，
// 只弹一条说明，不浪费一次注定失败的请求。
func TestCommentCostSwitchRefusedWhenLocked(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		fmt.Fprint(w, envelopeJSON(`{"message":"不该到这里"}`))
	}))
	defer srv.Close()

	a, tt := commentCostApp(t)
	a.client.Base = srv.URL + "/api"
	// 回归起点生效：徽章锁定。
	a.pandora.status = pandoraStatus{
		Known: true, BoxKnown: true,
		Box: pandoraBox{EventCode: "badge_drop_lock", EventName: "回归起点"},
	}
	a.pandora.loaded = true
	tt.Frame()
	if !tt.HasText("徽章已掉落锁定") {
		t.Errorf("状态栏没有说明锁定：%q", tt.Texts())
	}
	if _, ok := tt.Find("前排打手 8 金币"); ok {
		t.Errorf("锁定期间不该给切换按钮：%q", tt.Texts())
	}
	a.setCommentCostBadge(fixedCostBadge("b-fixed", "前排打手", -8), true, "")
	tt.Frame()
	if !tt.HasText("无法佩戴徽章") {
		t.Errorf("锁定期间没有说明原因：%q", tt.Texts())
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("锁定期间不该发佩戴请求，实际发了 %d 次", calls)
	}
}

// 发评论前按潘多拉的字数门槛拦一道：没到门槛就只弹 toast，不发请求。
func TestSubmitReplyBlockedByPandoraMinLength(t *testing.T) {
	a := detailAppWithReplies(t)
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		fmt.Fprint(w, envelopeJSON(`{"id":"new1","content":"x","floor":9}`))
	}))
	defer srv.Close()
	a.client.Base = srv.URL + "/api"
	a.pandora.status = pandoraStatus{
		Known: true, BoxKnown: true,
		Box: pandoraBox{
			EventCode: "comment_min_length", EventName: "多说多写", CommentMinLength: 100,
		},
	}
	a.detail.draft = "写得太短了"

	a.submitReply()
	tt := ui.NewTester(a.view, 1100, 900)
	tt.Frame()
	if !tt.HasText("还差 95 字才能发送（潘多拉要求 ≥100 字）") {
		t.Errorf("字数不够时没有拦下发送：%q", tt.Texts())
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Errorf("字数不够时不该发请求，实际发了 %d 次", calls)
	}
	if a.detail.draft != "写得太短了" {
		t.Errorf("拦下后草稿应保持原样：%q", a.detail.draft)
	}
}

// 独立写评论窗口里也有同一条状态栏：窗口宽度足够时内容一致。
func TestCommentCostBarInReplyWindow(t *testing.T) {
	a, _ := commentCostApp(t)
	a.showReplyWindow()
	wt := ui.NewTester(a.replyWindowView, replyWindowWidth, replyWindowHeight)
	wt.Frame()
	for _, want := range []string{"120 字", "当前：未佩戴 · 发评约 30 金币", "走个面 +20%"} {
		if !wt.HasText(want) {
			t.Errorf("独立窗口缺少 %q：%q", want, wt.Texts())
		}
	}
}

// 加载失败后的退避是递增的：评论框一直开着时不能每帧重发同一个失败请求。
func TestCommentCostRetryBackoff(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, time.Minute}, {1, time.Minute}, {2, 2 * time.Minute},
		{3, 4 * time.Minute}, {9, 4 * time.Minute},
	}
	for _, c := range cases {
		if got := commentCostRetryAfter(c.failures); got != c.want {
			t.Errorf("第 %d 次失败后的退避 = %v，期望 %v", c.failures, got, c.want)
		}
	}
}

// 状态栏用的三个端点（我的徽章、佩戴状态、切换徽章）都必须走配置的代理：
// 这是新的调用方，不能出现「别的页面走代理、这一条直连」的漏口。
func TestCommentCostEndpointsUseConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	var mu sync.Mutex
	var paths []string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me/badges":
			fmt.Fprint(w, envelopeJSON(`[{"badge_id":"b1","badge":{"id":"b1","name":"都听我说！",
				"attributes":{"comment_cost_percent":0.3}}}]`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me/badges/equipped":
			fmt.Fprint(w, envelopeJSON(`[]`))
		default:
			fmt.Fprint(w, envelopeJSON(`{"message":"佩戴成功"}`))
		}
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	c := &Client{
		Proxy:  p,
		Base:   "http://upstream.invalid/api",
		bearer: func() string { return "tok" },
	}
	badges, err := c.fetchMyBadges(context.Background())
	if err != nil {
		t.Fatalf("经代理读徽章失败：%v", err)
	}
	if len(badges) != 1 || !badges[0].HasCommentCostPercent {
		t.Fatalf("经代理读到的徽章不对：%+v", badges)
	}
	if _, err := c.fetchEquippedBadgeIDs(context.Background()); err != nil {
		t.Fatalf("经代理读佩戴状态失败：%v", err)
	}
	if _, err := c.setBadgeEquipped(context.Background(), "b1", "k1", true); err != nil {
		t.Fatalf("经代理切换徽章失败：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"GET /api/v1/me/badges",
		"GET /api/v1/me/badges/equipped",
		"POST /api/v1/me/badges/b1/equip",
	}
	if len(paths) != len(want) {
		t.Fatalf("代理收到的请求 = %v，期望 %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("第 %d 个请求 = %s，期望 %s", i+1, paths[i], want[i])
		}
	}
}
