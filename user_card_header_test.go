package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 资料卡头部必须紧凑：标题栏只占一行、资料头紧跟其后，关注按钮与昵称同
// 一行。原来的 96 DIP 渐变横幅里只有关闭按钮，下面还单独留一行放关注按钮，
// 资料与列表被压到窗口下半部。
func TestUserCardHeaderIsCompact(t *testing.T) {
	a := userCardTestApp(t)
	cs := a.userCard
	cs.user.Level = 7
	cs.user.CreatedAt = "2024-03-01T10:00:00.000Z"
	cs.aboutLoaded = true
	cs.about = userAbout{Content: "一段介绍", PublicKnown: true, IsPublic: true}
	cs.topics.items = []Item{{Key: "T1", ShortID: "T1", Title: "第一篇主题"}}

	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()

	title, ok := tt.Find("用户资料")
	if !ok {
		t.Fatalf("标题栏没有画出来：%q", tt.Texts())
	}
	if title.Y < 0 || title.Y+title.H > titleBarHeight {
		t.Errorf("标题栏文字不在一行标题栏里：y=%.0f 高 %.0f", title.Y, title.H)
	}

	name, ok := tt.Find("爱丽丝")
	if !ok {
		t.Fatalf("资料头没有昵称：%q", tt.Texts())
	}
	// 标题栏 32 + 分隔线 + 资料头的 12 DIP 内边距：昵称应在窗口顶部 60 DIP
	// 以内。旧版横幅有 96 DIP，昵称落在 121 DIP。
	if name.Y > 60 {
		t.Errorf("昵称下方留了太多空白：y=%.0f", name.Y)
	}

	// 关注按钮与昵称在资料头同一行，不再自己占一行。
	follow, ok := tt.Find("关注他")
	if !ok {
		t.Fatalf("资料头没有关注按钮：%q", tt.Texts())
	}
	if follow.Y < name.Y || follow.Y > name.Y+30 {
		t.Errorf("关注按钮没有和昵称同一行：昵称 y=%.0f，按钮 y=%.0f", name.Y, follow.Y)
	}

	avatar, ok := tt.AccessByRole(ui.RoleImage, "alice")
	if !ok {
		t.Fatalf("资料头没有头像：%q", tt.Texts())
	}
	if got := avatar.Bounds.W; got != userCardAvatar {
		t.Errorf("头像边长 = %.0f，期望 %d", got, userCardAvatar)
	}

	// 昵称下方的身份行带注册时间，不把这块留白空着。
	if !tt.HasText("注册 2024-03-01") {
		t.Errorf("身份行没有注册时间：%q", tt.Texts())
	}
	// 等级与 PRO 一样画成小标签，跟着昵称。
	if !tt.HasText("Lv.7") {
		t.Errorf("资料头没有等级：%q", tt.Texts())
	}

	// 头部收紧后，标签页与第一行内容都落在窗口上半部。
	tabs, ok := tt.Find(userCardTabLabels[0])
	if !ok {
		t.Fatalf("没有标签页：%q", tt.Texts())
	}
	if tabs.Y > userCardWindowHeight/2 {
		t.Errorf("标签页被头部挤到窗口下半部：y=%.0f", tabs.Y)
	}
	row, ok := tt.Find("· 第一篇主题")
	if !ok {
		t.Fatalf("没有第一行主题：%q", tt.Texts())
	}
	if row.Y > 460 {
		t.Errorf("列表第一行位置过低：y=%.0f", row.Y)
	}
}

// 没有关注按钮时（自己的资料卡、未登录接口不给状态），资料头也不留一行
// 空白：昵称下面的第一块内容紧贴资料头。
func TestUserCardHeaderWithoutFollowKeepsDensity(t *testing.T) {
	a := userCardTestApp(t)
	// 当前登录账号就是资料卡的主人：关注按钮不出现。
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "alice"}); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()

	if tt.HasText("关注他") || tt.HasText("已关注") {
		t.Fatalf("自己的资料卡不该出现关注按钮：%q", tt.Texts())
	}
	name, ok := tt.Find("爱丽丝")
	if !ok {
		t.Fatalf("没有昵称：%q", tt.Texts())
	}
	bio, ok := tt.Find("个人简介")
	if !ok {
		t.Fatalf("没有个人简介一块：%q", tt.Texts())
	}
	// 昵称高约 21、身份行 16、资料头下边距 12、分隔线 1、简介上边距 10：
	// 两者间距应在一行身份行加边距的范围内。旧版这里还夹着一整行只有关注
	// 按钮的动作行（37 DIP），没有按钮时也照样留白。
	if gap := bio.Y - (name.Y + name.H); gap > 60 {
		t.Errorf("资料头与简介之间空出了 %.0f DIP", gap)
	}
}

// 窗口缩到最小宽度、昵称又很长时，关注按钮仍在窗口内，不会和昵称、徽章
// 叠在一起；资料头的顶部也不会多出空白。
func TestUserCardHeaderNarrowWindow(t *testing.T) {
	a := userCardTestApp(t)
	cs := a.userCard
	cs.user.DisplayName = strings.Repeat("很长很长的昵称", 8)
	cs.user.Badges = []badgeInfo{{Name: "守护者"}, {Name: "早起鸟"}}
	tt := ui.NewTester(a.userCardWindowView, userCardMinWidth, userCardMinHeight)
	tt.Frame()

	follow, ok := tt.Find("关注他")
	if !ok {
		t.Fatalf("窄窗口下没有关注按钮：%q", tt.Texts())
	}
	if follow.X+follow.W > userCardMinWidth {
		t.Errorf("关注按钮超出窗口：x=%.0f 宽 %.0f，窗口宽 %d",
			follow.X, follow.W, userCardMinWidth)
	}
	avatar, ok := tt.AccessByRole(ui.RoleImage, "alice")
	if !ok {
		t.Fatalf("窄窗口下没有头像：%q", tt.Texts())
	}
	if avatar.Bounds.X+avatar.Bounds.W > follow.X {
		t.Errorf("头像与关注按钮重叠：头像右边 %.0f，按钮左边 %.0f",
			avatar.Bounds.X+avatar.Bounds.W, follow.X)
	}
	if avatar.Bounds.Y > 60 {
		t.Errorf("窄窗口下资料头仍留大片空白：头像 y=%.0f", avatar.Bounds.Y)
	}
}
