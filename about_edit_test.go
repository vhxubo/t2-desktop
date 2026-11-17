package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 写个人介绍的契约：路径、两个字段、幂等键与鉴权；响应结构与读端点一致。
func TestUpdateMyAboutRequest(t *testing.T) {
	type request struct {
		path, key, auth, body string
	}
	reqCh := make(chan request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		reqCh <- request{
			path: r.URL.Path, key: r.Header.Get("Idempotency-Key"),
			auth: r.Header.Get("Authorization"), body: string(raw),
		}
		fmt.Fprint(w, envelopeJSON(`{
			"content":"服务端清理后的正文","is_public":false,
			"achievement_count":{"coins":"1200"}
		}`))
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	about, err := c.updateMyAbout(context.Background(), "key-1", "原稿", false)
	if err != nil {
		t.Fatalf("保存介绍失败：%v", err)
	}
	if about.Content != "服务端清理后的正文" || about.IsPublic || !about.PublicKnown {
		t.Fatalf("响应解析不对：%+v", about)
	}
	if about.Stats["coins"] != "1200" {
		t.Errorf("统计占位没有解析：%+v", about.Stats)
	}

	req := <-reqCh
	if req.path != "/api/v1/me/about" || req.key != "key-1" || req.auth != "Bearer tok" {
		t.Errorf("请求 = %+v", req)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.body), &body); err != nil {
		t.Fatal(err)
	}
	if body["content"] != "原稿" || body["is_public"] != false {
		t.Errorf("请求体 = %s", req.body)
	}
}

// 入口只给本人，且介绍读到之后才出现；别人的资料卡上没有编辑按钮。
func TestUserCardAboutEditorOnlySelf(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv)
	a.userCard = newUserCardState("alice")
	a.userCard.aboutLoaded = true
	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if tt.HasText("编辑介绍") {
		t.Errorf("别人的资料卡不该有编辑入口：%q", tt.Texts())
	}

	a.userCard = newUserCardState("reader")
	a.userCard.aboutLoaded = true
	tt = ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if !tt.HasText("编辑介绍") {
		t.Errorf("本人资料卡缺少编辑入口：%q", tt.Texts())
	}

	// 介绍还没读到：同样不给入口，避免空草稿覆盖原内容。
	a.userCard.aboutLoaded = false
	tt = ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if tt.HasText("编辑介绍") {
		t.Errorf("介绍没读到时不该给编辑入口：%q", tt.Texts())
	}
}

// 保存后：草稿关掉、正文换成服务端清理过的版本、has_readme 跟着更新。
func TestUserCardAboutEditorSaves(t *testing.T) {
	reqCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		reqCh <- string(raw) + "|" + r.Header.Get("Idempotency-Key")
		fmt.Fprint(w, envelopeJSON(`{"content":"清理后的新介绍","is_public":false}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.userCard = newUserCardState("reader")
	cs := a.userCard
	cs.aboutLoaded = true
	cs.about = userAbout{
		Content: "旧介绍", IsPublic: true, PublicKnown: true,
	}
	cs.user.HasReadme = true

	a.openUserCardAboutEditor()
	if !cs.aboutEditOpen || cs.aboutDraft != "旧介绍" || !cs.aboutPublic {
		t.Fatalf("打开编辑框时没有带入原内容：%+v", cs)
	}
	cs.aboutDraft = "新介绍"
	cs.aboutPublic = false
	a.submitUserCardAbout()
	waitProfile(t, "保存个人介绍", func() bool {
		return !cs.aboutBusy && !cs.aboutEditOpen
	})

	if cs.about.Content != "清理后的新介绍" || cs.about.IsPublic {
		t.Errorf("保存后正文没有替换：%+v", cs.about)
	}
	if cs.aboutErrEdit != "" || cs.aboutKey != "" || cs.aboutSig != "" {
		t.Errorf("成功后没有收拾状态：%+v", cs)
	}
	if !cs.user.HasReadme {
		t.Error("保存后应标记为已填写介绍")
	}
	req := <-reqCh
	if !strings.HasPrefix(req, `{"content":"新介绍","is_public":false}|`) {
		t.Errorf("请求体 / 键 = %q", req)
	}
	if key := strings.TrimPrefix(req, `{"content":"新介绍","is_public":false}|`); !keyPattern.MatchString(key) {
		t.Errorf("幂等键格式不对：%q", key)
	}
}

// 超过 20000 字符不发请求，直接在框里说明原因。
func TestUserCardAboutEditorTooLong(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv)
	a.userCard = newUserCardState("reader")
	cs := a.userCard
	cs.aboutLoaded = true
	cs.aboutEditOpen = true
	cs.aboutDraft = strings.Repeat("字", userAboutMaxChars+1)

	a.submitUserCardAbout()
	if !strings.Contains(cs.aboutErrEdit, "不能超过") {
		t.Errorf("超长没有给出原因：%q", cs.aboutErrEdit)
	}
	if cs.aboutBusy {
		t.Error("超长内容不该发请求")
	}
}

// 模态框：标题、字符数、公开开关与确认按钮都在；取消后关掉。
func TestUserCardAboutEditorUI(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv)
	a.userCard = newUserCardState("reader")
	cs := a.userCard
	cs.aboutLoaded = true
	cs.about = userAbout{Content: "旧介绍", IsPublic: true, PublicKnown: true}

	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if err := tt.Click("编辑介绍"); err != nil {
		t.Fatalf("点「编辑介绍」失败：%v", err)
	}
	tt.Frame()
	if !cs.aboutEditOpen {
		t.Fatal("点「编辑介绍」没有打开编辑框")
	}
	for _, want := range []string{
		"编辑个人介绍", "保存并支付 500 金币", "公开（广场可见）",
		"仅社区成员可见", "3 / 20000 字符",
	} {
		if !tt.HasText(want) {
			t.Errorf("编辑框缺少 %q：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("取消"); err != nil {
		t.Fatalf("点「取消」失败：%v", err)
	}
	if cs.aboutEditOpen {
		t.Error("取消后编辑框还开着")
	}
}
