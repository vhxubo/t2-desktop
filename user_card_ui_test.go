package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// userCardTestApp 造一份资料与两个列表都已到位的资料卡状态，headless 里不
// 创建真实窗口，测试直接渲染 userCardWindowView。
func userCardTestApp(t *testing.T) *app {
	t.Helper()
	a := testApp(t)
	a.userCard = newUserCardState("alice")
	cs := a.userCard
	cs.profileLoaded = true
	cs.user = accountProfile{
		Name: "alice", DisplayName: "爱丽丝",
		HasReadme: true, Followers: 12, Following: 3,
	}
	cs.topics.loaded = true
	cs.replies.loaded = true
	return a
}

// 资料卡里的帖子 / 回复行：主窗口切到对应正文，资料卡本身留着不关。
func TestUserCardDetailKeepsCardOpen(t *testing.T) {
	a := testApp(t)
	a.userCard = newUserCardState("alice")
	a.setUserCardWindowShown(true)

	a.openUserCardDetail(Item{ShortID: "T1", CommentID: "R1"})

	if a.pageName != "detail" {
		t.Fatalf("点帖子后主窗口没有进正文页：%q", a.pageName)
	}
	if a.detail == nil || a.detail.shortID != "T1" || a.detail.focusID != "R1" {
		t.Fatalf("打开的帖子不对：%+v", a.detail)
	}
	if a.userCard == nil || !a.userCardShown {
		t.Error("打开帖子后资料卡窗口不该被关掉")
	}
}

// 「个人简介」展示 about 端点的 Markdown 正文：表情短码换成图片说明、
// {{KEY}} 换成统计值，末尾说明是否公开与更新时间。
func TestUserCardAboutRenders(t *testing.T) {
	a := userCardTestApp(t)
	cs := a.userCard
	cs.aboutLoaded = true
	cs.about = userAbout{
		Content:     "### 你好\n\n我是爱丽丝，喜欢写 Go。\n\n金币：{{COINS}}",
		IsPublic:    true,
		PublicKnown: true,
		UpdatedAt:   "2026-10-30T02:51:35.215Z",
		Stats:       map[string]string{"coins": "1294"},
	}
	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	for _, want := range []string{
		"个人简介", "你好", "我是爱丽丝，喜欢写 Go。", "金币：1294",
		"介绍公开", "更新于 2026-10-30",
	} {
		if !tt.HasText(want) {
			t.Errorf("资料卡缺少介绍文案 %q：%q", want, tt.Texts())
		}
	}
	// 旧版在这里只说「填写过」；正文现在真的展示了，不能再出现这句话。
	if tt.HasText("正文没有在这里展示") {
		t.Errorf("介绍正文已展示，仍出现旧占位文案：%q", tt.Texts())
	}
}

// 介绍的四种状态：读取中、没有填写、仅自己可见、读取失败（带重试）。
func TestUserCardAboutStates(t *testing.T) {
	a := userCardTestApp(t)
	cs := a.userCard
	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if !tt.HasText("正在读取个人介绍…") {
		t.Errorf("读取中缺少说明：%q", tt.Texts())
	}

	cs.aboutLoaded = true
	cs.about = userAbout{PublicKnown: true, IsPublic: true}
	tt.Frame()
	if !tt.HasText("还没有填写个人介绍") {
		t.Errorf("没有介绍时缺少说明：%q", tt.Texts())
	}

	// 不公开且没有正文：不能写成「没有填写」。
	cs.about = userAbout{PublicKnown: true}
	tt.Frame()
	if !tt.HasText("仅自己可见") || tt.HasText("还没有填写个人介绍") {
		t.Errorf("不公开的介绍状态不对：%q", tt.Texts())
	}

	cs.aboutErr = "网络不可用"
	tt.Frame()
	for _, want := range []string{"介绍暂时不可用", "网络不可用", "重试"} {
		if !tt.HasText(want) {
			t.Errorf("读取失败时缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 很长的介绍不会把统计、标签页和列表挤出窗口：介绍在块内滚动（窗口被缩小
// 时也随之收缩），其余区块仍留在窗口里。
func TestUserCardAboutLongContentKeepsLayout(t *testing.T) {
	for _, height := range []int{userCardWindowHeight, userCardMinHeight} {
		a := userCardTestApp(t)
		cs := a.userCard
		cs.aboutLoaded = true
		cs.about = userAbout{
			Content:     strings.Repeat("这是一段很长的个人介绍，用来撑高内容区。\n\n", 40),
			PublicKnown: true,
			IsPublic:    true,
		}
		cs.topics.items = []Item{{Key: "T1", ShortID: "T1", Title: "第一篇主题"}}
		tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, height)
		tt.Frame()

		// 列表行渲染成「· 标题」，按行上的完整文本定位。
		for _, label := range append(append([]string{}, userCardTabLabels...), "· 第一篇主题") {
			r, ok := tt.Find(label)
			if !ok {
				t.Fatalf("窗口高 %d 时，很长的介绍把 %q 挤没了：%q", height, label, tt.Texts())
			}
			if r.Y+r.H > float32(height) {
				t.Errorf("窗口高 %d 时 %q 被挤出窗口：y=%.0f 高 %.0f",
					height, label, r.Y, r.H)
			}
		}
	}
}

// 介绍读取失败后的「重试」会重新请求 about，结果回填后就地换成正文。
func TestUserCardAboutRetry(t *testing.T) {
	var got int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/alice/about" {
			http.NotFound(w, r)
			return
		}
		got++
		fmt.Fprint(w, `{"c":0,"d":{"content":"重试后读到的介绍","is_public":true}}`)
	}))
	defer srv.Close()

	a := userCardTestApp(t)
	a.client.Base = srv.URL + "/api"
	cs := a.userCard
	cs.aboutLoaded = true
	cs.aboutErr = "网络不可用"

	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if err := tt.Click("重试"); err != nil {
		t.Fatalf("点重试失败：%v", err)
	}
	waitProfile(t, "重试后的个人介绍", func() bool {
		return cs.aboutLoaded && cs.aboutErr == "" && cs.about.Content != ""
	})
	tt.Frame()
	if got != 1 {
		t.Errorf("重试请求次数 = %d，期望 1", got)
	}
	if !tt.HasText("重试后读到的介绍") {
		t.Errorf("重试成功后没有显示正文：%q", tt.Texts())
	}
}
