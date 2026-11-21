package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 常量列表按官网规则生成：键取自 achievement_count（Stats），显示名优先用
// constantsLabel，缺失时把下划线 key 转成可读名字，再按显示名排序。
func TestBuildAboutConstants(t *testing.T) {
	about := userAbout{
		Stats: map[string]string{"exp": "1200", "tip_out_coins": "88", "coins": "9"},
		ConstantsLabel: map[string]string{
			"exp":   "经验值",
			"coins": "金币",
		},
	}
	got := buildAboutConstants(about)
	if len(got) != 3 {
		t.Fatalf("常量数 = %d，期望 3：%+v", len(got), got)
	}
	byLabel := make(map[string]aboutConstant)
	for _, c := range got {
		byLabel[c.Label] = c
	}
	for _, want := range []struct{ label, token, line string }{
		{"经验值", "{{EXP}}", "经验值：{{EXP}}"},
		{"金币", "{{COINS}}", "金币：{{COINS}}"},
		{"Tip Out Coins", "{{TIP_OUT_COINS}}", "Tip Out Coins：{{TIP_OUT_COINS}}"},
	} {
		c, ok := byLabel[want.label]
		if !ok {
			t.Errorf("缺少常量 %q：%+v", want.label, got)
			continue
		}
		if c.Token != want.token || c.Line != want.line {
			t.Errorf("常量 %q = %+v，期望 %q / %q", want.label, c, want.token, want.line)
		}
	}
	// 排序用显示名：金币 < 经验值 < Tip Out Coins（按字母/Unicode 序）。
	for i := 1; i < len(got); i++ {
		if got[i-1].Label > got[i].Label {
			t.Errorf("常量没有按显示名排序：%+v", got)
		}
	}
	if all := aboutAllConstantsLine(got); !strings.Contains(all, "经验值：{{EXP}}") ||
		!strings.Contains(all, "金币：{{COINS}}") || strings.Count(all, "\n") != 2 {
		t.Errorf("全部常量 = %q", all)
	}
}

// 没有统计数据时下拉是空的，不该凭空造常量。
func TestBuildAboutConstantsEmpty(t *testing.T) {
	if got := buildAboutConstants(userAbout{}); len(got) != 0 {
		t.Errorf("空 about 不该有常量：%+v", got)
	}
	if got := aboutAllConstantsLine(nil); got != "" {
		t.Errorf("空常量列表的全量行 = %q", got)
	}
}

// 六个组件的 token 拼接与官网一致：空项丢掉、字段用 | / , 分隔、可省字段省略。
func TestAboutComponentTokens(t *testing.T) {
	cases := []struct{ name, got, want string }{
		{"轮播图", aboutCarouselToken([]string{" https://a.png ", "/b.png", "ftp://x", ""}),
			"[carousel:https://a.png,/b.png]"},
		{"非法图片", aboutCarouselToken([]string{"/api/x", "https://2libra.com/api/y"}), ""},
		{"折叠面板", aboutCollapseToken([][2]string{{"标题", "内容"}, {"空", ""}}),
			"[collapse:标题|内容]"},
		{"轮播文字", aboutTextRotateToken([]string{"甲", " ", "乙"}), "[text-rotate:甲,乙]"},
		{"环形进度", aboutRadialProgressToken(70), "[radial-progress:70]"},
		{"环形进度越界", aboutRadialProgressToken(200), "[radial-progress:100]"},
		{"进度条默认最大值", aboutProgressToken(40, 100), "[progress:40]"},
		{"进度条自定义最大值", aboutProgressToken(40, 120), "[progress:40:120]"},
		{"Hero 带链接", aboutHeroToken("https://bg.png", "标题", "描述", "按钮", "https://go"),
			"[hero:https://bg.png|标题|描述|按钮|https://go]"},
		{"Hero 省略按钮", aboutHeroToken("https://bg.png", "标题", "", "", ""),
			"[hero:https://bg.png|标题||]"},
		{"Hero 缺必填", aboutHeroToken("", "标题", "", "", ""), ""},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q，期望 %q", c.name, c.got, c.want)
		}
	}
	// 工具条里给的六个占位模板都不能是空串，否则点下去没反应。
	for _, tpl := range aboutComponentTemplates() {
		if tpl.Name == "" || tpl.Token == "" {
			t.Errorf("组件模板不完整：%+v", tpl)
		}
		if !strings.HasPrefix(tpl.Token, "[") || !strings.HasSuffix(tpl.Token, "]") {
			t.Errorf("组件 token 形状不对：%+v", tpl)
		}
	}
}

// 快捷模板插到光标处：选区在中间时插入，光标跟到插完的内容后面。
func TestAboutTemplateInsertAtCaret(t *testing.T) {
	st := &aboutTemplateState{}
	body := "甲乙"
	st.ed.selStart, st.ed.selEnd = 1, 1
	st.insert(&body, "{{EXP}}")
	if body != "甲{{EXP}}乙" {
		t.Errorf("插入结果 = %q", body)
	}
	if st.ed.selStart != 1+len("{{EXP}}") || st.ed.selEnd != st.ed.selStart {
		t.Errorf("插入后光标 = %d..%d", st.ed.selStart, st.ed.selEnd)
	}
	// 插入后两个下拉都要收起。
	st.constOpen, st.compOpen = true, true
	st.insert(&body, "[progress:40]")
	if st.constOpen || st.compOpen {
		t.Error("插入后下拉没有收起")
	}
}

// 编辑框顶部出现官网同款的两个下拉，点选后把模板写进草稿。
func TestAboutEditorQuickTemplatesUI(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv)
	a.userCard = newUserCardState("reader")
	cs := a.userCard
	cs.aboutLoaded = true
	cs.about = userAbout{
		Content:        "开头",
		Stats:          map[string]string{"exp": "1200", "tip_out_coins": "88"},
		ConstantsLabel: map[string]string{"exp": "经验值"},
	}

	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if err := tt.Click("编辑介绍"); err != nil {
		t.Fatalf("打开编辑框失败：%v", err)
	}
	tt.Frame()
	for _, want := range []string{"个人常量", "展示组件"} {
		if !tt.HasText(want) {
			t.Fatalf("编辑框缺少 %q：%q", want, tt.Texts())
		}
	}

	// 个人常量：展开后列出统计项与「全部常量」，点一项插进正文。
	if err := tt.Click("个人常量"); err != nil {
		t.Fatalf("打开个人常量失败：%v", err)
	}
	tt.Frame()
	for _, want := range []string{"经验值", "Tip Out Coins", "全部常量"} {
		if !tt.HasText(want) {
			t.Errorf("个人常量缺少 %q：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("经验值"); err != nil {
		t.Fatalf("选择常量失败：%v", err)
	}
	tt.Frame()
	if !strings.Contains(cs.aboutDraft, "经验值：{{EXP}}") {
		t.Errorf("常量没有插进草稿：%q", cs.aboutDraft)
	}

	// 展示组件：展开后点「环形进度」插入 token 模板。
	if err := tt.Click("展示组件"); err != nil {
		t.Fatalf("打开展示组件失败：%v", err)
	}
	tt.Frame()
	if !tt.HasText("环形进度") || !tt.HasText("轮播图") || !tt.HasText("Hero") {
		t.Errorf("展示组件列表不全：%q", tt.Texts())
	}
	if err := tt.Click("环形进度"); err != nil {
		t.Fatalf("选择组件失败：%v", err)
	}
	tt.Frame()
	if !strings.Contains(cs.aboutDraft, "[radial-progress:70]") {
		t.Errorf("组件 token 没有插进草稿：%q", cs.aboutDraft)
	}
}
