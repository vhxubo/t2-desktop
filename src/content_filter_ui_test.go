package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
)

func blockedFilter() *contentFilter {
	return newContentFilter(contentFilterDTO{BlockedUsers: []string{"alice"}})
}

func TestReplyRowCollapsesAndExpandsPerComment(t *testing.T) {
	a := filterApp(t)
	a.contentFilter.filter = blockedFilter()
	a.detail = &detailState{revealed: make(map[string]bool)}
	row1 := replyItem{ID: "c1", Author: "alice", Content: "剧透正文一", Floor: 1}
	row2 := replyItem{ID: "c2", Author: "alice", Content: "剧透正文二", Floor: 2}
	tt := ui.NewTester(func(c *ui.Context) {
		a.replyRow(c, row1, replyTreeGuide{}, "", false)
		a.replyRow(c, row2, replyTreeGuide{}, "", false)
	}, 700, 260)
	tt.Frame()

	if n := countText(tt.Texts(), "已屏蔽 alice 的评论"); n != 2 {
		t.Fatalf("两条被屏蔽评论应各有一行提示，实际 %d 条：%q", n, tt.Texts())
	}
	if tt.HasText("剧透正文一") || tt.HasText("剧透正文二") {
		t.Fatalf("折叠时不应出现正文：%q", tt.Texts())
	}

	if err := tt.Click("展开"); err != nil {
		t.Fatalf("点不到展开：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("剧透正文一") {
		t.Error("展开后应出现这一条的正文")
	}
	if tt.HasText("剧透正文二") {
		t.Error("展开只作用于这一条，其它评论仍应折叠")
	}
	if !a.detail.revealed["c1"] || a.detail.revealed["c2"] {
		t.Errorf("展开态不对：%v", a.detail.revealed)
	}

	if err := tt.Click("收起"); err != nil {
		t.Fatalf("点不到收起：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if tt.HasText("剧透正文一") {
		t.Error("收起后正文应重新隐藏")
	}
}

func TestBlockedParentKeepsChildRepliesVisible(t *testing.T) {
	a := filterApp(t)
	a.contentFilter.filter = newContentFilter(contentFilterDTO{BlockedUsers: []string{"alice"}})
	a.detail = &detailState{revealed: make(map[string]bool)}
	parent := replyItem{ID: "p1", Author: "alice", Content: "被屏蔽的父评论", Floor: 1, HasChildren: true}
	child := replyItem{ID: "c1", Author: "bob", Content: "别人的子回复", Depth: 1}
	tt := ui.NewTester(func(c *ui.Context) {
		a.replyRow(c, parent, replyTreeGuide{}, "", false)
		a.replyRow(c, child, replyTreeGuide{}, "", false)
	}, 700, 260)
	tt.Frame()
	if !tt.HasText("已屏蔽 alice 的评论") {
		t.Fatalf("父评论应折叠：%q", tt.Texts())
	}
	if !tt.HasText("别人的子回复") || tt.HasText("被屏蔽的父评论") {
		t.Fatalf("只折叠这一条，子回复照常显示：%q", tt.Texts())
	}
}

func countText(texts []string, want string) int {
	n := 0
	for _, s := range texts {
		if s == want {
			n++
		}
	}
	return n
}

func TestKeywordBlockedReplyPrompt(t *testing.T) {
	a := filterApp(t)
	a.contentFilter.filter = newContentFilter(contentFilterDTO{Keywords: []string{"剧透"}})
	a.detail = &detailState{revealed: make(map[string]bool)}
	row := replyItem{ID: "c1", Author: "bob", Content: "这里有剧透内容", Floor: 1}
	tt := ui.NewTester(func(c *ui.Context) {
		a.replyRow(c, row, replyTreeGuide{}, "", false)
	}, 700, 200)
	tt.Frame()
	if !tt.HasText("已屏蔽含「剧透」的评论") {
		t.Fatalf("关键词命中提示不对：%q", tt.Texts())
	}
	if tt.HasText("这里有剧透内容") {
		t.Error("提示里不该出现评论正文")
	}
}

func TestRevealedIsDroppedWhenFilterChanges(t *testing.T) {
	a := filterApp(t)
	d := &detailState{revealed: map[string]bool{"c1": true}}
	a.detail = d
	a.detailStack = []*detailState{{revealed: map[string]bool{"c2": true}}}
	a.detailWindows = []*detailWindow{{
		states: []*detailState{{revealed: map[string]bool{"c3": true}}},
		stacks: [][]*detailState{{{revealed: map[string]bool{"c4": true}}}},
	}}
	a.clearRevealed()
	if len(d.revealed) != 0 || len(a.detailStack[0].revealed) != 0 ||
		len(a.detailWindows[0].states[0].revealed) != 0 ||
		len(a.detailWindows[0].stacks[0][0].revealed) != 0 {
		t.Fatal("名单变化后所有详情状态的展开态都应清空")
	}
}

func echoFilterServer(t *testing.T, puts *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			_, _ = fmt.Fprint(w, envelopeJSON(`{"blocked_users":[],"blocked_nodes":[],"keywords":[]}`))
			return
		}
		puts.Add(1)
		b, _ := io.ReadAll(r.Body)
		var dto contentFilterDTO
		_ = json.Unmarshal(b, &dto)
		resp, _ := json.Marshal(map[string]any{"c": 0, "m": "ok", "d": dto, "t": 1})
		_, _ = w.Write(resp)
	}))
}

func TestProfileFilterTabChipsAndSave(t *testing.T) {
	var puts atomic.Int32
	srv := echoFilterServer(t, &puts)
	defer srv.Close()

	a := profileWindowApp(t)
	a.client.Base = srv.URL
	a.client.HTTP = srv.Client()
	a.contentFilter.filter = newContentFilter(contentFilterDTO{})
	a.contentFilter.draft = cloneFilterDTO(contentFilterDTO{})
	a.profile.tab = profileTabSettings
	a.profile.settingsKind = profileSettingsFilter
	a.profile.tabSeen = profileTabSettings
	a.profile.settingsSeen = profileSettingsFilter // 别让这一帧再触发一次强制 GET

	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	for _, want := range []string{
		"屏蔽设置",
		"屏蔽用户（0/50）", "屏蔽节点（0/50）", "屏蔽关键词（0/50）",
	} {
		if !tt.HasText(want) {
			t.Errorf("管理页缺少文案 %q：%q", want, tt.Texts())
		}
	}
	if tt.HasText("同一份名单") {
		t.Errorf("屏蔽设置不应展示顶部说明小字：%q", tt.Texts())
	}

	if err := tt.Click("输入用户名"); err != nil {
		t.Fatalf("点不到用户名输入框：%v", err)
	}
	tt.Type("alice")
	tt.Frame()
	if err := tt.Click("添加"); err != nil {
		t.Fatalf("点不到添加按钮：%v", err)
	}
	tt.Frame()
	if !tt.HasText("未保存") || !tt.HasText("alice") {
		t.Fatalf("添加后应有未保存标记与 chip：%q", tt.Texts())
	}
	if err := tt.Click("撤销改动"); err != nil {
		t.Fatalf("点不到撤销改动：%v", err)
	}
	tt.Frame()
	if tt.HasText("alice") {
		t.Error("撤销后 chip 应消失")
	}

	// 再加回来并保存：发出 PUT 且用响应覆盖本地。
	_ = tt.Click("输入用户名")
	tt.Type("alice")
	tt.Frame()
	_ = tt.Click("添加")
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatalf("点不到保存：%v", err)
	}
	waitFor(t, func() bool { return puts.Load() == 1 && !a.contentFilter.saving }, "保存没有发出 PUT")
	if len(a.contentFilter.filter.raw.BlockedUsers) != 1 || a.contentFilter.filter.raw.BlockedUsers[0] != "alice" {
		t.Errorf("保存后快照没有用响应覆盖：%+v", a.contentFilter.filter.raw)
	}
}

func TestUserCardBlockButton(t *testing.T) {
	var puts atomic.Int32
	srv := echoFilterServer(t, &puts)
	defer srv.Close()

	a := filterApp(t)
	a.client.Base = srv.URL
	a.client.HTTP = srv.Client()
	a.contentFilter.filter = newContentFilter(contentFilterDTO{})
	cs := newUserCardState("alice")
	a.userCard = cs
	tt := ui.NewTester(func(c *ui.Context) { a.userCardHeader(c, cs) }, 600, 180)
	tt.Frame()
	if !tt.HasText("屏蔽") {
		t.Fatalf("资料头应有屏蔽按钮：%q", tt.Texts())
	}
	if err := tt.Click("屏蔽"); err != nil {
		t.Fatalf("点不到屏蔽按钮：%v", err)
	}
	waitFor(t, func() bool { return puts.Load() == 1 && !a.contentFilter.saving }, "屏蔽没有发出 PUT")
	tt.Frame()
	if !tt.HasText("已屏蔽") {
		t.Fatalf("成功后按钮应变已屏蔽：%q", tt.Texts())
	}

	// 自己的资料卡不画这个按钮。
	self := newUserCardState("jimmy")
	a.userCard = self
	tt2 := ui.NewTester(func(c *ui.Context) { a.userCardHeader(c, self) }, 600, 180)
	tt2.Frame()
	if tt2.HasText("屏蔽") || tt2.HasText("已屏蔽") {
		t.Errorf("自己的资料卡不该出现屏蔽按钮：%q", tt2.Texts())
	}
}
