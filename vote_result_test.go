package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 结果端点的 vote_id 是查询参数、snake_case，路径必须严格照文档。
func TestVoteResultPathMatchesContract(t *testing.T) {
	got := topicVoteResultPath("a1b2c3d4", "v1")
	want := "/v1/topics/a1b2c3d4/vote/result?vote_id=v1"
	if got != want {
		t.Errorf("投票结果路径 = %s，期望 %s", got, want)
	}
}

// 读结果要带令牌（登录后个性化字段才有意义），并且只发一次 GET。
func TestClientVoteResult(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.RequestURI() + " auth=" + r.Header.Get("Authorization")
		_, _ = fmt.Fprint(w, envelopeJSON(`{"options":[
			{"id":"o1","text":"A","vote_count":6},{"id":"o2","text":"B","vote_count":2}],
			"total_votes":8,"result_visible":true}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}

	res, err := c.fetchVoteResult(context.Background(), "T1", "v1")
	if err != nil {
		t.Fatalf("读投票结果失败：%v", err)
	}
	if want := "GET /api/v1/topics/T1/vote/result?vote_id=v1 auth=Bearer tok"; got != want {
		t.Errorf("请求 = %q，期望 %q", got, want)
	}
	if len(res.Options) != 2 || res.Options[0].Votes != 6 || res.Total != 8 || !res.Visible {
		t.Errorf("结果解析不对：%+v", res)
	}
}

// 文档没有给响应示例：裸数组、results 包装、votes / count 字段都要认；
// 没有总票数时用各选项求和。
func TestDecodeVoteResultShapes(t *testing.T) {
	bare := decodeVoteResult(json.RawMessage(`[{"id":"o1","text":"A","votes":4},{"id":"o2","text":"B","count":1}]`))
	if len(bare.Options) != 2 || bare.Total != 5 {
		t.Errorf("裸数组解析不对：%+v", bare)
	}
	wrapped := decodeVoteResult(json.RawMessage(`{"results":[{"id":"o1","vote_count":3}],"total":3,"result_visible":false}`))
	if len(wrapped.Options) != 1 || wrapped.Total != 3 || !wrapped.VisibleKnown || wrapped.Visible {
		t.Errorf("results 包装解析不对：%+v", wrapped)
	}
	// 没有 result_visible 字段时按「未知」处理，不能当成不公开。
	unknown := decodeVoteResult(json.RawMessage(`{"items":[{"id":"o1","vote_count":2}]}`))
	if unknown.VisibleKnown {
		t.Errorf("没有 result_visible 时应按未知处理：%+v", unknown)
	}
}

// 投票块里的「查看投票结果」：点开读一次端点，按详情里的选项顺序画出票数
// 与百分比。
func TestDetailVoteResultToggle(t *testing.T) {
	var calls int
	a := detailAppWithReplies(t)
	a.autoLoad = true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 表情表之类的其它请求也会打到这个假服务端；只统计结果端点。
		if !strings.HasSuffix(r.URL.Path, "/vote/result") {
			_, _ = fmt.Fprint(w, envelopeJSON(`{"items":[],"total":0,"page":1,"limit":100,"total_pages":1}`))
			return
		}
		calls++
		if r.URL.Query().Get("vote_id") != "v1" {
			t.Errorf("vote_id = %q", r.URL.Query().Get("vote_id"))
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{"options":[
			{"id":"o1","text":"A","vote_count":6},{"id":"o2","text":"B","vote_count":2}],
			"total_votes":8}`))
	}))
	defer srv.Close()
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.detail.topic.Vote.ID = "v1"

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("查看投票结果"); err != nil {
		t.Fatalf("点不到查看结果：%v（%q）", err, tt.Texts())
	}
	waitFor(t, func() bool { return a.detail.vote.result != nil }, "投票结果没有加载出来")
	tt.Frame()

	if calls != 1 {
		t.Errorf("结果请求次数 = %d，期望 1", calls)
	}
	if !tt.HasText("共 8 票") {
		t.Errorf("没有显示总票数：%q", tt.Texts())
	}
	if !tt.HasText("6 票 · 75%") || !tt.HasText("2 票 · 25%") {
		t.Errorf("没有按选项显示分布：%q", tt.Texts())
	}
	// 再点一次收起，不重复请求。
	if err := tt.Click("收起投票结果"); err != nil {
		t.Fatalf("点不到收起结果：%v", err)
	}
	tt.Frame()
	if a.detail.vote.resultOpen {
		t.Error("再点一次应收起结果")
	}
	if calls != 1 {
		t.Errorf("收起不该重新请求：%d 次", calls)
	}
}

// 投票已结束时，打开帖子就自动展开结果分布：这类帖子结果就是主要信息。
func TestDetailVoteResultAutoLoadsWhenFinished(t *testing.T) {
	a := detailAppWithReplies(t)
	a.autoLoad = true
	// 正文加载完会写本机缓存；测试不等那次写盘，关掉它避免 TempDir 清理时
	// 与后台写文件撞车。
	a.kv = nil
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/topics/T1" {
			_, _ = fmt.Fprint(w, envelopeJSON(`{"short_id":"T1","title":"已结束的投票",
				"vote":{"id":"v9","title":"选哪个","voting_finished":true,"max_choices":1,
				"options":[{"id":"o1","text":"A","vote_count":7}]}}`))
			return
		}
		if r.URL.Path == "/api/v1/topics/T1/vote/result" {
			_, _ = fmt.Fprint(w, envelopeJSON(`{"options":[{"id":"o1","text":"A","vote_count":7}],"total_votes":7}`))
			return
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{"items":[],"total":0,"page":1,"limit":100,"total_pages":1}`))
	}))
	defer srv.Close()
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.detail.shortID = "T1"

	a.loadTopic()
	waitFor(t, func() bool {
		return a.detail.vote.result != nil && a.detail.vote.resultOpen
	}, "已结束的投票没有自动展开结果")

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText("7 票 · 100%") {
		t.Errorf("没有画出结果分布：%q", tt.Texts())
	}
}
