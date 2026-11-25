package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func filterApp(t *testing.T) *app {
	t.Helper()
	a := testApp(t)
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: "jimmy",
		Scope: "read profile read:me write:comment interact",
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestDecodeContentFilterTolerant(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want contentFilterDTO
	}{
		{"三个数组", `{"blocked_users":["Alice"],"blocked_nodes":["职场"],"keywords":["剧透"]}`,
			contentFilterDTO{[]string{"Alice"}, []string{"职场"}, []string{"剧透"}}},
		{"空数组", `{"blocked_users":[],"blocked_nodes":[],"keywords":[]}`,
			contentFilterDTO{[]string{}, []string{}, []string{}}},
		{"null 与缺字段", `{"blocked_users":null,"keywords":["x"],"unknown":1}`,
			contentFilterDTO{Keywords: []string{"x"}}},
		{"字段类型异常", `{"blocked_users":[1,"ok",null,{}],"blocked_nodes":"x"}`,
			contentFilterDTO{BlockedUsers: []string{"ok"}}},
		{"null 整体", `null`, contentFilterDTO{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeContentFilter(json.RawMessage(tc.raw))
			if len(got.BlockedUsers) != len(tc.want.BlockedUsers) ||
				len(got.BlockedNodes) != len(tc.want.BlockedNodes) ||
				len(got.Keywords) != len(tc.want.Keywords) {
				t.Fatalf("解码结果 = %+v，期望 %+v", got, tc.want)
			}
		})
	}
}

func TestContentFilterNormalizeAndHit(t *testing.T) {
	f := newContentFilter(contentFilterDTO{
		BlockedUsers: []string{"  Alice  "},
		BlockedNodes: []string{"职场"},
		Keywords:     []string{"剧 透"},
	})
	if _, ok := f.userHit("  ALICE "); !ok {
		t.Error("用户名应忽略大小写与首尾空白")
	}
	if _, ok := f.userHit("匿名"); ok {
		t.Error("匿名作者不参与用户匹配")
	}
	if _, ok := f.userHit(""); ok {
		t.Error("空作者不参与用户匹配")
	}
	if _, ok := f.nodeHit("职场"); !ok {
		t.Error("节点按显示名匹配")
	}
	if _, ok := f.nodeHit("work"); ok {
		t.Error("节点不能按 slug 匹配")
	}
	if _, ok := f.keywordHit("这里是剧 透内容"); !ok {
		t.Error("中文关键词应命中标题")
	}

	// 优先级：用户 > 节点 > 关键词，同一条只报最外层原因。
	mix := newContentFilter(contentFilterDTO{
		BlockedUsers: []string{"alice"}, BlockedNodes: []string{"职场"}, Keywords: []string{"剧透"},
	})
	r, ok := mix.matchItem(Item{Author: "ALICE", Title: "剧透", Node: nodeRef{Name: "职场"}})
	if !ok || r.Kind != filterKindUser || r.Value != "alice" {
		t.Fatalf("命中原因 = %+v %v，期望用户 alice", r, ok)
	}
	r, ok = mix.matchItem(Item{Author: "bob", Title: "剧透", Node: nodeRef{Name: "职场"}})
	if !ok || r.Kind != filterKindNode {
		t.Fatalf("没有用户命中时应报节点，得到 %+v", r)
	}
	// 评论行的作者藏在 Extra 里；已删除的评论不折叠。
	it := Item{Title: "无作者", Extra: &rowExtra{Author: "Alice", Excerpt: "正文"}}
	if _, ok := mix.matchItem(it); !ok {
		t.Error("评论行应按 Extra.Author 命中用户")
	}
	if _, ok := mix.matchComment("alice", "正文", true); ok {
		t.Error("已删除的评论不参与折叠")
	}
}

func TestContentFilterLatestCommentMatchesReplyTo(t *testing.T) {
	f := newContentFilter(contentFilterDTO{Keywords: []string{"广告"}, BlockedUsers: []string{"spammer"}})

	// 最新评论行：正文与作者都不含命中，被回复人命中关键词。
	comment := Item{Title: "随便聊聊", Extra: &rowExtra{
		Author: "bob", ReplyTo: "广告小王子", Excerpt: "今天天气不错",
	}}
	r, ok := f.matchItem(comment)
	if !ok || r.Kind != filterKindKeyword || r.Value != "广告" {
		t.Fatalf("被回复人命中关键词应屏蔽整条，得到 %+v %v", r, ok)
	}

	// 被回复人命中屏蔽用户名单，同样屏蔽整条。
	r, ok = f.matchItem(Item{Extra: &rowExtra{Author: "bob", ReplyTo: "SPAMMER", Excerpt: "正文"}})
	if !ok || r.Kind != filterKindUser || r.Value != "spammer" {
		t.Fatalf("被回复人命中用户名单应屏蔽整条，得到 %+v %v", r, ok)
	}

	// 作者命中关键词、正文命中关键词，行为不变。
	if _, ok := f.matchItem(Item{Author: "bob", Title: "全是广告的标题"}); !ok {
		t.Error("正文命中关键词仍应屏蔽")
	}
	// 作者命中关键词但被回复人和正文都不含，不应屏蔽（只按被回复人）。
	if _, ok := f.matchItem(Item{Extra: &rowExtra{Author: "广告号", ReplyTo: "bob", Excerpt: "正文"}}); ok {
		t.Error("评论作者命中关键词不该按这条规则屏蔽")
	}

	// 都没命中时放行。
	if _, ok := f.matchItem(Item{Extra: &rowExtra{Author: "bob", ReplyTo: "小明", Excerpt: "正文"}}); ok {
		t.Error("作者与被回复人都没命中不该屏蔽")
	}

	// 大小写归一化。
	up := newContentFilter(contentFilterDTO{Keywords: []string{"AD"}})
	if _, ok := up.matchItem(Item{Extra: &rowExtra{Author: "bob", ReplyTo: "ad号", Excerpt: "正文"}}); !ok {
		t.Error("被回复人命中关键词应忽略大小写")
	}

	// 详情评论不按被回复人过滤：作者命中关键词不应屏蔽，正文命中仍有效。
	if _, ok := f.matchComment("广告号", "今天天气不错", false); ok {
		t.Error("详情评论不应按作者命中关键词屏蔽")
	}
	if _, ok := f.matchComment("bob", "广告正文", false); !ok {
		t.Error("详情评论正文命中关键词仍应屏蔽")
	}
}

func TestValidateFilterDTO(t *testing.T) {
	full := make([]string, contentFilterMaxPerList)
	for i := range full {
		full[i] = fmt.Sprintf("u%d", i)
	}
	if why := validateFilterDTO(contentFilterDTO{BlockedUsers: full}, ""); why != "" {
		t.Fatalf("50 条边界应通过：%s", why)
	}
	if why := validateFilterDTO(contentFilterDTO{BlockedUsers: append(append([]string(nil), full...), "extra")}, ""); why == "" {
		t.Error("超过 50 条应被拦下")
	}
	if why := validateFilterDTO(contentFilterDTO{BlockedUsers: []string{" jimmy "}}, "jimmy"); why == "" {
		t.Error("不能屏蔽自己")
	}
	if why := validateFilterDTO(contentFilterDTO{BlockedUsers: []string{strings.Repeat("a", 256)}}, ""); why == "" {
		t.Error("用户单项 255 字符上限")
	}
	if why := validateFilterDTO(contentFilterDTO{BlockedNodes: []string{strings.Repeat("中", 100)}}, ""); why != "" {
		t.Errorf("节点 100 字符应通过：%s", why)
	}
	if why := validateFilterDTO(contentFilterDTO{BlockedNodes: []string{strings.Repeat("中", 101)}}, ""); why == "" {
		t.Error("节点单项 100 字符上限")
	}
	big := make([]string, contentFilterMaxPerList)
	for i := range big {
		big[i] = strings.Repeat("词", 8)
	}
	if why := validateFilterDTO(contentFilterDTO{Keywords: big}, ""); why == "" {
		t.Error("整份编码超 3500 字节应被拦下")
	}
	if why := validateFilterDTO(contentFilterDTO{BlockedUsers: []string{"  "}}, ""); why == "" {
		t.Error("空项应被拦下")
	}
}

func TestContentFilterReady(t *testing.T) {
	s := newContentFilterState()
	if !s.ready(false) {
		t.Error("未登录不该等名单")
	}
	if s.ready(true) {
		t.Error("首次无缓存时应等待名单")
	}
	s.pendingSince = time.Now().Add(-contentFilterWait)
	if !s.ready(true) {
		t.Error("超过 5 秒应超时放行")
	}
	s.pendingSince = time.Now()
	s.err = "网络错误"
	if !s.ready(true) {
		t.Error("失败应 fail-open")
	}
	s.err = ""
	s.loaded = true
	if !s.ready(true) {
		t.Error("名单到位后应放行")
	}
}

func TestSaveContentFilterContractAndOverwrite(t *testing.T) {
	var mu sync.Mutex
	var method, path, auth, ct, idem string
	var body contentFilterDTO
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		method, path = r.Method, r.URL.Path
		auth, ct, idem = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Header.Get("Idempotency-Key")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		mu.Unlock()
		_, _ = fmt.Fprint(w, envelopeJSON(`{"blocked_users":["alice"],"blocked_nodes":[],"keywords":["剧透"]}`))
	}))
	defer srv.Close()

	a := filterApp(t)
	a.client.Base = srv.URL
	a.client.HTTP = srv.Client()
	a.writeContentFilter(contentFilterDTO{
		BlockedUsers: []string{"alice"}, BlockedNodes: []string{}, Keywords: []string{},
	}, toastMain, nil)
	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return a.contentFilter.filter != nil && !a.contentFilter.saving
	}, "保存没有完成")

	if method != http.MethodPut || path != "/v1/me/content-filter" {
		t.Errorf("写请求 = %s %s", method, path)
	}
	if auth != "Bearer tok" || ct != "application/json" {
		t.Errorf("请求头不对：auth=%q content-type=%q", auth, ct)
	}
	if idem != "" {
		t.Errorf("这个端点不该带 Idempotency-Key：%q", idem)
	}
	if body.BlockedUsers == nil || body.BlockedNodes == nil || body.Keywords == nil {
		t.Errorf("三个数组都必须提交：%+v", body)
	}
	f := a.contentFilter.filter
	if len(f.raw.BlockedUsers) != 1 || f.raw.BlockedUsers[0] != "alice" ||
		len(f.raw.Keywords) != 1 || f.raw.Keywords[0] != "剧透" {
		t.Errorf("没有用响应里的名单覆盖快照：%+v", f.raw)
	}
}

func TestContentFilterCacheRoundTrip(t *testing.T) {
	a := filterApp(t)
	key := a.contentFilterCacheKey()
	if !strings.HasPrefix(key, contentFilterCachePrefix) {
		t.Fatalf("缓存键 = %q", key)
	}
	d := contentFilterDTO{BlockedUsers: []string{"alice"}, BlockedNodes: []string{}, Keywords: []string{"剧透"}}
	a.writeContentFilterCache(d)
	a.contentFilter = newContentFilterState()
	a.loadContentFilterCache()
	if !a.contentFilter.loaded || a.contentFilter.filter == nil {
		t.Fatal("缓存没有装进首帧状态")
	}
	if got := a.contentFilter.filter.raw; len(got.BlockedUsers) != 1 || got.BlockedUsers[0] != "alice" {
		t.Fatalf("缓存内容不对：%+v", got)
	}
	a.auth.Logout()
	a.onAuthChanged(nil)
	if a.contentFilter.filter != nil || a.contentFilterCacheKey() != "" {
		t.Fatal("退出后不应沿用上一次登录的名单快照")
	}
}
