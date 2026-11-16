package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 金币池：只读展示帖子的池子余额与状态，以及每条评论从池里领到了多少。

func TestDecodeTopicRewardPool(t *testing.T) {
	// 线上实测把 reward_pool 放在详情顶层。
	p, err := decodeTopic([]byte(`{
		"short_id":"POOL1","title":"有金币池","content":"正文",
		"reward_pool":{"current_coins":320,"finished":false,"is_expired":false}
	}`), "POOL1")
	if err != nil {
		t.Fatal(err)
	}
	if p.RewardPool == nil || !p.RewardPool.Known || p.RewardPool.CurrentCoins != 320 ||
		!p.RewardPool.CoinsKnown || p.RewardPool.Finished || p.RewardPool.Expired {
		t.Fatalf("顶层金币池解析错误：%+v", p.RewardPool)
	}

	// api-read.md 写的是 extra.reward_pool，两处都认。
	p, err = decodeTopic([]byte(`{
		"short_id":"POOL2","title":"extra 金币池","content":"正文",
		"extra":{"reward_pool":{"current_coins":100,"finished":true}}
	}`), "POOL2")
	if err != nil {
		t.Fatal(err)
	}
	if p.RewardPool == nil || p.RewardPool.CurrentCoins != 100 || !p.RewardPool.Finished ||
		p.RewardPool.Expired {
		t.Fatalf("extra 金币池解析错误：%+v", p.RewardPool)
	}

	// is_expired 是另一个键，不能和 finished 混成一个。
	p, err = decodeTopic([]byte(`{
		"short_id":"POOL3","title":"已过期","content":"正文",
		"extra":{"reward_pool":{"current_coins":5,"is_expired":true}}
	}`), "POOL3")
	if err != nil {
		t.Fatal(err)
	}
	if p.RewardPool == nil || !p.RewardPool.Expired || p.RewardPool.Finished {
		t.Fatalf("过期标记解析错误：%+v", p.RewardPool)
	}

	// 没有金币池的帖子不画这一块：nil 与「池子空了」是两回事。
	p, err = decodeTopic([]byte(`{"short_id":"POOL4","title":"没有池子","content":"正文"}`), "POOL4")
	if err != nil {
		t.Fatal(err)
	}
	if p.RewardPool != nil {
		t.Fatalf("没有 reward_pool 时不应造出一个池子：%+v", p.RewardPool)
	}

	// 池子块在、但没给余额：不能把缺字段当成「已领完」。
	p, err = decodeTopic([]byte(`{
		"short_id":"POOL6","title":"没给余额","content":"正文",
		"reward_pool":{"finished":false}
	}`), "POOL6")
	if err != nil {
		t.Fatal(err)
	}
	if p.RewardPool == nil || p.RewardPool.CoinsKnown {
		t.Fatalf("没给 current_coins 时不应把余额当成已知：%+v", p.RewardPool)
	}
	if got := rewardPoolStateText(p.RewardPool); got != "进行中" {
		t.Errorf("余额未知时的状态 = %q，期望 %q", got, "进行中")
	}

	// 异常余额夹到 0，不把负数展示出去。
	p, err = decodeTopic([]byte(`{
		"short_id":"POOL5","title":"负数","content":"正文",
		"reward_pool":{"current_coins":-8}
	}`), "POOL5")
	if err != nil {
		t.Fatal(err)
	}
	if p.RewardPool == nil || p.RewardPool.CurrentCoins != 0 {
		t.Fatalf("负数余额应夹到 0：%+v", p.RewardPool)
	}
}

func TestDecodeRewardPoolWinnings(t *testing.T) {
	// 文档给的是 {"amount": 53}；数字数组也认。0 与负数不记。
	got := decodeRewardPoolWinnings([]byte(`[
		{"amount":53},{"coins":21},{"amount":0},{"amount":-5},{"other":1},7,8.4
	]`))
	want := []int{53, 21, 7, 8}
	if len(got) != len(want) {
		t.Fatalf("领到的金币 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("领到的金币 = %v，期望 %v", got, want)
		}
	}
	if got := decodeRewardPoolWinnings(nil); got != nil {
		t.Errorf("没有记录时不应给出金额：%v", got)
	}
	if total := rewardPoolWinningsTotal([]int{53, 0, -1, 21}); total != 74 {
		t.Errorf("合计 = %d，期望 74", total)
	}
}

func TestRewardPoolStateText(t *testing.T) {
	cases := []struct {
		pool *rewardPoolInfo
		want string
	}{
		{&rewardPoolInfo{CurrentCoins: 320, CoinsKnown: true}, "进行中"},
		{&rewardPoolInfo{CurrentCoins: 0, CoinsKnown: true}, "已领完"},
		{&rewardPoolInfo{}, "进行中"},
		{&rewardPoolInfo{CurrentCoins: 320, CoinsKnown: true, Finished: true}, "已结束"},
		{&rewardPoolInfo{CurrentCoins: 320, CoinsKnown: true, Expired: true}, "已过期"},
	}
	for _, c := range cases {
		if got := rewardPoolStateText(c.pool); got != c.want {
			t.Errorf("%+v 的状态 = %q，期望 %q", c.pool, got, c.want)
		}
	}
}

// 详情页正文末尾画出金币池状态；评论头部标出这条评论从池里领到的合计。
func TestRewardPoolSectionAndReplyWinnings(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "POOL01", URL: "https://2libra.com/post/work/POOL01"})
	topic, err := decodeTopic([]byte(`{
		"short_id":"POOL01","title":"有池子的帖子","content":"一段正文。",
		"author":{"username":"jimmy"},"reward_pool":{"current_coins":320}
	}`), "POOL01")
	if err != nil {
		t.Fatal(err)
	}
	a.detail.topic = &topic
	a.detail.replyList = []replyItem{{
		ID: "c1", Author: "甲", Floor: 1, Content: "领到了",
		RewardPoolWinnings: []int{53, 21},
	}}
	a.detail.replyTotal = 1
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	for _, want := range []string{"金币池", "池中还有 320 金币", "进行中", "金币池 +74"} {
		if !tt.HasText(want) {
			t.Errorf("详情页缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 没有金币池的帖子不留空块，也不要一个 0 金币的池子。
func TestRewardPoolSectionHiddenWithoutPool(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "NOPOOL", URL: "https://2libra.com/post/work/NOPOOL"})
	topic, err := decodeTopic([]byte(`{
		"short_id":"NOPOOL","title":"普通帖子","content":"一段正文。","author":{"username":"jimmy"}
	}`), "NOPOOL")
	if err != nil {
		t.Fatal(err)
	}
	a.detail.topic = &topic
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	// 标题里不能再出现「金币池」三个字，否则这条断言就成了自证。
	for _, want := range []string{"池中还有", "金币池", "进行中"} {
		if tt.HasText(want) {
			t.Errorf("没有 reward_pool 时不应画金币池块（命中 %q）：%q", want, tt.Texts())
		}
	}
}
