package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

/* ------------------------------------------------------------------
   个人介绍端点：路径、解码与 404
----------------------------------------------------------------- */

// 个人介绍端点的路径沿用 userPath，昵称里的特殊字符必须转义。
func TestUserAboutPath(t *testing.T) {
	if got, want := userAboutPath("jimmy"), "/v1/users/jimmy/about"; got != want {
		t.Errorf("介绍路径 = %q，期望 %q", got, want)
	}
	if got, want := userAboutPath("a/b"), "/v1/users/a%2Fb/about"; got != want {
		t.Errorf("昵称转义不对：%q，期望 %q", got, want)
	}
}

// 线上响应的字段：content 是 Markdown，可能为 null；achievement_count 的键
// 小写、值是字符串数字；emojiMap 与帖子的 emojis 同形（可能带相对地址）。
func TestDecodeUserAbout(t *testing.T) {
	d := json.RawMessage(`{
		"content":"### 你好\n\n金币：{{COINS}}\n小写：{{tip_out_coins}}\n未知：{{NOPE}}",
		"is_public":true,
		"created_at":"2025-12-26T07:43:16.228Z",
		"updated_at":"2026-10-30T02:51:35.215Z",
		"emojiMap":{":sigh:":"/emojis/sigh.png?size=medium"},
		"achievement_count":{"coins":"129404","tip_out_coins":"247055","level":13},
		"constantsLabel":{"coins":"金币"}
	}`)
	got := decodeUserAbout(d)
	if got.Content == "" {
		t.Fatal("介绍正文没有解析出来")
	}
	if !got.PublicKnown || !got.IsPublic {
		t.Errorf("公开状态解析不对：%+v", got)
	}
	if got.UpdatedAt != "2026-10-30T02:51:35.215Z" {
		t.Errorf("更新时间解析不对：%q", got.UpdatedAt)
	}
	if got.Emojis[":sigh:"] != "https://r2.2libra.com/emojis/sigh.png?size=medium" {
		t.Errorf("emojiMap 解析不对：%+v", got.Emojis)
	}
	if got.Stats["coins"] != "129404" || got.Stats["level"] != "13" {
		t.Errorf("统计数据解析不对：%+v", got.Stats)
	}
	want := "### 你好\n\n金币：129404\n小写：247055\n未知：{{NOPE}}"
	if out := expandAboutStats(got.Content, got.Stats); out != want {
		t.Errorf("占位符替换 = %q，期望 %q", out, want)
	}

	// 没有填写介绍：content 是 null，不能因此报错，也不能编出「公开 / 不公开」。
	empty := decodeUserAbout(json.RawMessage(`{"content":null,"achievement_count":{}}`))
	if empty.Content != "" || empty.PublicKnown || empty.IsPublic || empty.Stats != nil {
		t.Errorf("空介绍解析不对：%+v", empty)
	}
	// 响应里没有 is_public 时同样不能当成「不公开」。
	if unknown := decodeUserAbout(json.RawMessage(`{"content":"x"}`)); unknown.PublicKnown {
		t.Errorf("没有 is_public 时不该报告已知：%+v", unknown)
	}
}

// 占位符替换只动认识的键：大小写不敏感，未知的 {{…}} 原样保留。
func TestExpandAboutStats(t *testing.T) {
	stats := map[string]string{"coins": "42", "level": "7"}
	cases := []struct{ in, want string }{
		{"金币：{{COINS}}", "金币：42"},
		{"{{coins}} / {{Coins}}", "42 / 42"},
		{"等级 {{LEVEL}} 与 {{UNKNOWN}}", "等级 7 与 {{UNKNOWN}}"},
		{"没有占位符", "没有占位符"},
		{"{{}}", "{{}}"},
	}
	for _, c := range cases {
		if got := expandAboutStats(c.in, stats); got != c.want {
			t.Errorf("expandAboutStats(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
	if got := expandAboutStats("{{COINS}}", nil); got != "{{COINS}}" {
		t.Errorf("没有统计表时应原样返回：%q", got)
	}
}

// 端点契约：匿名可读（不带 Authorization），没有介绍时的 404 是空介绍而
// 不是错误；其它错误照常上报。
func TestFetchUserAbout(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path+"|"+r.Header.Get("Authorization"))
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/users/jimmy/about":
			fmt.Fprint(w, envelopeJSON(`{"content":"你好，我是站长。","is_public":true}`))
		case "/api/v1/users/broken/about":
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"c":500,"code":"internal_error","m":"服务端错误","d":null}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"c":404,"code":"not_found","m":"介绍不存在","d":null}`)
		}
	}))
	defer srv.Close()

	c := &Client{Base: srv.URL + "/api"}
	about, err := c.fetchUserAbout(context.Background(), "jimmy")
	if err != nil || about.Content != "你好，我是站长。" || !about.IsPublic {
		t.Fatalf("读取介绍失败：%+v %v", about, err)
	}
	// 没有介绍：404 是空介绍，不是失败。
	empty, err := c.fetchUserAbout(context.Background(), "nobody")
	if err != nil || empty.Content != "" {
		t.Fatalf("404 应视为空介绍：%+v %v", empty, err)
	}
	// 其它错误要上报，界面才知道该显示「重试」。
	if _, err := c.fetchUserAbout(context.Background(), "broken"); err == nil {
		t.Fatal("500 不该被当成空介绍")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("请求次数不对：%v", requests)
	}
	for _, req := range requests {
		// 匿名可用：没登录时不发送（空的）Authorization 头，否则会被判 401。
		if !strings.HasSuffix(req, "|") {
			t.Errorf("匿名请求带了 Authorization 头：%q", req)
		}
	}
}

// 资料卡打开后，介绍与资料、两个列表并行读取；结果按 seq 回填。
func TestUserCardLoadsAbout(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/users/alice":
			fmt.Fprint(w, `{"c":0,"d":{"username":"alice","has_readme":true}}`)
		case "/api/v1/users/alice/about":
			fmt.Fprint(w, `{"c":0,"d":{"content":"我是 Alice。\n\n金币：{{COINS}}","is_public":true,`+
				`"updated_at":"2026-10-30T02:51:35.215Z","achievement_count":{"coins":"42"}}}`)
		case "/api/v1/users/alice/topics", "/api/v1/users/alice/replies":
			fmt.Fprint(w, `{"c":0,"d":{"posts":[],"items":[],"total":0,"page":1,"limit":20,"total_pages":1}}`)
		case "/api/v1/emojis":
			fmt.Fprint(w, envelopeJSON(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := testApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	a.showUserCardWindow("alice")
	// 资料与介绍、两个列表并行；等四块都回来再看请求记录，避免并发交错
	// 让断言读到还没发生的请求。
	waitProfile(t, "资料与个人介绍", func() bool {
		cs := a.userCard
		return cs != nil && cs.aboutLoaded && cs.profileLoaded &&
			cs.topics.loaded && cs.replies.loaded
	})

	about := a.userCard.about
	if about.Content == "" || about.Stats["coins"] != "42" || !about.IsPublic {
		t.Fatalf("介绍没有回填：%+v", about)
	}
	if a.userCard.aboutErr != "" {
		t.Errorf("成功读取后不该有错误：%q", a.userCard.aboutErr)
	}

	mu.Lock()
	defer mu.Unlock()
	seen := map[string]bool{}
	for _, p := range paths {
		seen[p] = true
	}
	for _, want := range []string{
		"/api/v1/users/alice",
		"/api/v1/users/alice/about",
		"/api/v1/users/alice/topics",
		"/api/v1/users/alice/replies",
	} {
		if !seen[want] {
			t.Errorf("没有请求 %s：%v", want, paths)
		}
	}
}
