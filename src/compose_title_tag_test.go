package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 2libra 网页端把 5 个系统标签按固定顺序写进标题开头的同一个【】里；
// 桌面端额外保留作者自己写的非系统前缀，不被一次点击吞掉。
func TestComposeSetTitleTagOrderAndCustomPrefix(t *testing.T) {
	got := composeSetTitleTag("【吐槽】标题", "投票", true)
	if want := "【投票】【吐槽】标题"; got != want {
		t.Fatalf("加投票后标题 = %q，期望 %q", got, want)
	}
	got = composeSetTitleTag(got, "兑换码", true)
	if want := "【兑换码·投票】【吐槽】标题"; got != want {
		t.Fatalf("加兑换码后标题 = %q，期望 %q", got, want)
	}
	got = composeSetTitleTag(got, "💰", true)
	if want := "【兑换码·投票·💰】【吐槽】标题"; got != want {
		t.Fatalf("加金币池后标题 = %q，期望 %q", got, want)
	}
	got = composeSetTitleTag(got, "投票", false)
	if want := "【兑换码·💰】【吐槽】标题"; got != want {
		t.Fatalf("移除投票后标题 = %q，期望 %q", got, want)
	}
	if unchanged := composeSetTitleTag(got, "未知标签", true); unchanged != got {
		t.Errorf("非系统标签不该改动标题：%q -> %q", got, unchanged)
	}
	if unchanged := composeSetTitleTag("没有标签", "🔒", false); unchanged != "没有标签" {
		t.Errorf("删不存在的标签不该改动标题：%q", unchanged)
	}
	// 点评前缀写在标签前面时，标签仍要能被删掉。
	if got := composeSetTitleTag("《沙丘2》点评：8 分｜【投票】正文", "投票", false); got != "《沙丘2》点评：8 分｜正文" {
		t.Errorf("点评前缀后的标签没删掉：%q", got)
	}
}

// 一个【】里混了系统词与自定义词时，只把系统词收走，自定义词留在原处。
func TestComposeSetTitleTagMixedBlock(t *testing.T) {
	got := composeSetTitleTag("【吐槽·投票】正文", "兑换码", true)
	if want := "【兑换码·投票】【吐槽】正文"; got != want {
		t.Fatalf("混合块加标签 = %q，期望 %q", got, want)
	}
	got = composeSetTitleTag(got, "投票", false)
	if want := "【兑换码】【吐槽】正文"; got != want {
		t.Fatalf("混合块删标签 = %q，期望 %q", got, want)
	}
}

// 请求体的 title_prefix 是标题开头连续的 [] / 【】 块，正文中间的块不算。
func TestComposeTitlePrefix(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  string
	}{
		{"系统标签加自定义块", "【兑换码·投票】【转载】标题", "【兑换码·投票】【转载】"},
		{"方括号", "[转载]标题", "[转载]"},
		{"混合括号", "[转载]【投票】标题", "[转载]【投票】"},
		{"正文中间不算", "标题【投票】", ""},
		{"没有前缀", "普通标题", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := composeTitlePrefix(tc.title); got != tc.want {
				t.Errorf("title_prefix = %q，期望 %q", got, tc.want)
			}
		})
	}

	// 文档规定 title_prefix ≤100 字，超长自定义前缀截到 100 字再发。
	got := composeTitlePrefix("【" + strings.Repeat("长", 101) + "】正文")
	want := "【" + strings.Repeat("长", 99)
	if got != want {
		t.Errorf("超长前缀没有截到 100 字：got %d runes，want %d runes", len([]rune(got)), len([]rune(want)))
	}
}

// 点评不是【】标签，而是独立的《对象》点评：N 分｜ 前缀；重复打开要替换旧的。
func TestComposeReviewPrefix(t *testing.T) {
	if got, want := composeWithReviewPrefix("值得一看", "沙丘2", 8), "《沙丘2》点评：8 分｜值得一看"; got != want {
		t.Fatalf("生成点评前缀 = %q，期望 %q", got, want)
	}
	if got, want := composeWithReviewPrefix("《旧片》点评：7 分｜值得一看", "沙丘2", 9), "《沙丘2》点评：9 分｜值得一看"; got != want {
		t.Fatalf("重复评分应替换旧前缀：%q，期望 %q", got, want)
	}
	if got, want := composeStripReviewPrefix("《A》点评：1 分｜《B》点评：2 分｜正文"), "正文"; got != want {
		t.Fatalf("多个点评前缀没有剥干净：%q，期望 %q", got, want)
	}
	if got := composeStripReviewPrefix("《无分数》点评：好｜正文"); got != "《无分数》点评：好｜正文" {
		t.Errorf("非点评格式被误删：%q", got)
	}
}

// 加 / 改 / 移除组件都要同步标题，评分前缀要替换而不是叠加。
func TestComposeModuleDialogSyncTitle(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.title = "标题"
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)

	tt.Frame()
	if err := tt.Click("投票"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("添加此组件"); err != nil {
		t.Fatal(err)
	}
	if st.title != "【投票】标题" {
		t.Fatalf("添加投票后标题 = %q，期望 %q", st.title, "【投票】标题")
	}

	st.scoreTarget, st.scoreValue = "沙丘2", 8
	tt.Frame()
	if err := tt.Click("评分"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("添加此组件"); err != nil {
		t.Fatal(err)
	}
	if want := "《沙丘2》点评：8 分｜【投票】标题"; st.title != want {
		t.Fatalf("添加评分后标题 = %q，期望 %q", st.title, want)
	}

	st.scoreTarget, st.scoreValue = "沙丘3", 9
	tt.Frame()
	if err := tt.Click("评分"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("完成"); err != nil {
		t.Fatal(err)
	}
	if want := "《沙丘3》点评：9 分｜【投票】标题"; st.title != want {
		t.Fatalf("修改评分后标题 = %q，期望 %q", st.title, want)
	}

	tt.Frame()
	if err := tt.Click("移除投票"); err != nil {
		t.Fatal(err)
	}
	if want := "《沙丘3》点评：9 分｜标题"; st.title != want {
		t.Fatalf("移除投票后标题 = %q，期望 %q", st.title, want)
	}
	tt.Frame()
	if err := tt.Click("移除评分"); err != nil {
		t.Fatal(err)
	}
	if st.title != "标题" {
		t.Fatalf("移除评分后标题 = %q，期望 %q", st.title, "标题")
	}
}

// 「全部移除」同样要把标题里的系统标签收走，但保留自定义前缀。
func TestComposeRemoveAllModulesStripsTitleTags(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.title = "【兑换码·投票】【吐槽】标题"
	st.voteOpen, st.redeemOpen = true, true
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if err := tt.Click("全部移除附加组件"); err != nil {
		t.Fatal(err)
	}
	if st.enabledModuleCount() != 0 {
		t.Errorf("全部移除后仍开着 %d 项", st.enabledModuleCount())
	}
	if want := "【吐槽】标题"; st.title != want {
		t.Errorf("全部移除后标题 = %q，期望 %q", st.title, want)
	}
}

// 匿名发帖自动撤掉不兼容的金币池、竞猜与隐藏内容，并清掉对应标题标签。
func TestComposeAnonymousToggleDropsIncompatibleModules(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.title = "【竞猜·💰·🔒】【吐槽】匿名测试"
	st.poolOpen, st.betOpen, st.restrictedOpen = true, true, true
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if err := tt.Click("匿名发帖"); err != nil {
		t.Fatal(err)
	}
	if !st.anonymous {
		t.Fatal("点匿名开关没有改状态")
	}
	if st.poolOpen || st.betOpen || st.restrictedOpen {
		t.Errorf("匿名发帖应撤掉金币池、竞猜与隐藏内容：pool=%v bet=%v restricted=%v",
			st.poolOpen, st.betOpen, st.restrictedOpen)
	}
	if want := "【吐槽】匿名测试"; st.title != want {
		t.Errorf("匿名发帖后标题 = %q，期望 %q", st.title, want)
	}
	tt.Frame()
	if !tt.HasText("匿名发帖已移除金币池、竞猜、隐藏内容，及相关标题标签") {
		t.Errorf("自动移除没有用 toast 报告：%q", tt.Texts())
	}
}

// 网页端会把标题开头的前缀单独作为 title_prefix 一起提交。
func TestComposePayloadSendsTitlePrefix(t *testing.T) {
	st := newComposeState()
	st.title = "【兑换码·投票】【转载】标题"
	p, err := st.payload()
	if err != nil {
		t.Fatal(err)
	}
	assertField(t, p, "title", "【兑换码·投票】【转载】标题")
	assertField(t, p, "title_prefix", "【兑换码·投票】【转载】")
}
