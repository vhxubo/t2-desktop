package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		"编辑个人介绍", "保存（500 金币）", "公开",
		"3 / 20000 字符",
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

// 「我的 - 个人简介」独占一个标签页：正文与编辑入口都在，本人可改。编辑
// 弹窗带 Markdown 工具条、Lobe 公开开关、字数与底部审核规则小字。
func TestProfileAboutTabRendersAndEdits(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv)
	a.profile = newProfileState()
	ps := a.profile
	ps.tab = profileTabAbout
	ps.aboutLoaded = true
	ps.about = userAbout{Content: "我的介绍正文", IsPublic: true, PublicKnown: true}

	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	for _, want := range []string{"个人简介", "我的介绍正文", "编辑介绍"} {
		if !tt.HasText(want) {
			t.Errorf("「我的 - 个人简介」缺少 %q：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("编辑介绍"); err != nil {
		t.Fatalf("点「编辑介绍」失败：%v", err)
	}
	tt.Frame()
	if !ps.aboutEditOpen || ps.aboutDraft != "我的介绍正文" {
		t.Fatalf("没有带入原介绍：%q", tt.Texts())
	}
	for _, want := range []string{"编辑个人介绍", "保存（500 金币）", "6 / 20000 字符"} {
		if !tt.HasText(want) {
			t.Errorf("编辑框缺少 %q：%q", want, tt.Texts())
		}
	}
	// 底部小字给出公开范围、禁止 / 允许内容与违规后果。
	for _, want := range []string{
		"若设置为不公开，未登录用户将无法查看到你的介绍信息",
		"禁止内容：不得发布商业广告、违法违规、政治敏感、人身攻击等违规内容",
		"允许内容：个人以及个人产品介绍、项目推广、作品展示、开源资源、技术博客等合理分享",
		"违规内容将被清空，且一段时间内无法再次更新",
	} {
		if !tt.HasText(want) {
			t.Errorf("编辑框缺少底部说明 %q：%q", want, tt.Texts())
		}
	}
}

// 「我的 - 个人简介」走与资料卡同一条读 / 写端点，保存时带幂等键，
// 成功后正文就地替换成服务端清理过的版本。
func TestProfileAboutLoadsAndSaves(t *testing.T) {
	type request struct{ method, path, body, key string }
	reqCh := make(chan request, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/users/reader/about" {
			reqCh <- request{method: r.Method, path: r.URL.Path}
			fmt.Fprint(w, envelopeJSON(`{"content":"旧介绍","is_public":true}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/me/about" {
			raw, _ := io.ReadAll(r.Body)
			reqCh <- request{
				method: r.Method, path: r.URL.Path,
				body: string(raw), key: r.Header.Get("Idempotency-Key"),
			}
			fmt.Fprint(w, envelopeJSON(`{"content":"清理后的新介绍","is_public":false}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.client.Base = srv.URL + "/api"
	a.profile = newProfileState()
	ps := a.profile
	ps.tab = profileTabAbout

	a.loadProfileAbout()
	waitProfile(t, "「我的」读取个人介绍", func() bool { return ps.aboutLoaded })
	if ps.about.Content != "旧介绍" {
		t.Fatalf("读到的介绍不对：%+v", ps.about)
	}
	if got := <-reqCh; got.method != http.MethodGet || got.path != "/api/v1/users/reader/about" {
		t.Errorf("读端点 = %+v", got)
	}

	a.openProfileAboutEditor()
	ps.aboutDraft = "新介绍"
	ps.aboutPublic = false
	a.submitProfileAbout()
	waitProfile(t, "保存「我的」个人介绍", func() bool {
		return !ps.aboutBusy && !ps.aboutEditOpen
	})
	if ps.about.Content != "清理后的新介绍" {
		t.Errorf("保存后正文没有替换：%+v", ps.about)
	}
	got := <-reqCh
	if got.body != `{"content":"新介绍","is_public":false}` {
		t.Errorf("请求体不对：%q", got.body)
	}
	if !keyPattern.MatchString(got.key) {
		t.Errorf("幂等键格式不对：%q", got.key)
	}
}

// 介绍编辑框接上发帖那套 Markdown 编辑器：工具条、编辑 / 预览切换、表情
// 入口与 Lobe 公开开关都在，预览按展示时的样子渲染而不是露出 ** 源码。
func TestAboutEditorMarkdownToolbarAndPreview(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv)
	a.profile = newProfileState()
	ps := a.profile
	ps.tab = profileTabAbout
	ps.aboutLoaded = true
	ps.about = userAbout{Content: "**加粗**", IsPublic: true, PublicKnown: true}

	tt := ui.NewTester(a.profileWindowView, profileWindowWidth, profileWindowHeight)
	tt.Frame()
	if err := tt.Click("编辑介绍"); err != nil {
		t.Fatalf("点「编辑介绍」失败：%v", err)
	}
	tt.Frame()

	for _, want := range []string{
		"加粗", "斜体", "代码块", "链接", "图片", "Base64 编码",
		"编辑", "预览", "插入表情", "公开个人介绍",
	} {
		if !tt.HasText(want) {
			t.Errorf("编辑框缺少 %q：%q", want, tt.Texts())
		}
	}

	// 切到预览：**加粗** 要渲染成加粗文字，不该把 Markdown 标记原样显示。
	if err := tt.Click("预览"); err != nil {
		t.Fatalf("点「预览」失败：%v", err)
	}
	tt.Frame()
	if !tt.HasText("加粗") {
		t.Errorf("预览没有渲染加粗文字：%q", tt.Texts())
	}
	if tt.HasText("**加粗**") {
		t.Errorf("预览把 Markdown 源码当纯文字显示：%q", tt.Texts())
	}
}

// 个人常量很多时下拉仍要列出全部项（列表放进限高滚动区，弹层本身不溢出）。
func TestAboutConstantsPopoverWithManyItems(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	a := loginApp(t, srv)
	a.userCard = newUserCardState("reader")
	cs := a.userCard
	cs.aboutLoaded = true
	stats := make(map[string]string, 30)
	for i := 0; i < 30; i++ {
		stats["key_"+strconv.Itoa(i)] = "1"
	}
	cs.about = userAbout{Content: "开头", Stats: stats}

	tt := ui.NewTester(a.userCardWindowView, userCardWindowWidth, userCardWindowHeight)
	tt.Frame()
	if err := tt.Click("编辑介绍"); err != nil {
		t.Fatalf("打开编辑框失败：%v", err)
	}
	tt.Frame()
	if err := tt.Click("个人常量"); err != nil {
		t.Fatalf("打开个人常量失败：%v", err)
	}
	tt.Frame()
	for i := 0; i < 30; i++ {
		if !tt.HasText("{{KEY_" + strconv.Itoa(i) + "}}") {
			t.Errorf("个人常量缺少第 %d 项：%q", i, tt.Texts())
			break
		}
	}
	if !tt.HasText("全部常量") {
		t.Errorf("滚动区外缺少「全部常量」：%q", tt.Texts())
	}
}
