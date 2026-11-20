package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 选择器端点与解码：q 走 query 转义，不分页；响应容忍裸数组与 posts 包装，
// 未知字段忽略，缺 short_id / 标题的行跳过。
func TestMyTopicsPathAndDecode(t *testing.T) {
	if got, want := myTopicsPath(""), "/v1/me/topics"; got != want {
		t.Errorf("空搜索词路径 = %s，期望 %s", got, want)
	}
	if got, want := myTopicsPath("甲 & 乙"), "/v1/me/topics?q="+url.QueryEscape("甲 & 乙"); got != want {
		t.Errorf("带搜索词路径 = %s，期望 %s", got, want)
	}
	raw := `[
		{"short_id":"A1","title":"第一篇","created_at":"2026-11-01T09:00:00Z",
		 "node":{"slug":"work","name":"职场"},"comment_count":9,"unknown":1},
		{"title":"没有 short_id"},
		{"short_id":"B2","title":"第二篇","node":null}
	]`
	items := decodeMyTopicPicks([]byte(raw))
	if len(items) != 2 {
		t.Fatalf("解码条数 = %d，期望 2：%+v", len(items), items)
	}
	if items[0].ShortID != "A1" || items[0].Title != "第一篇" ||
		items[0].NodeSlug != "work" || items[0].NodeName != "职场" {
		t.Errorf("第一条字段不对：%+v", items[0])
	}
	if items[1].ShortID != "B2" || items[1].NodeSlug != "" {
		t.Errorf("第二条字段不对：%+v", items[1])
	}
	wrapped := decodeMyTopicPicks([]byte(`{"posts":[{"short_id":"C3","title":"包装里的"}]}`))
	if len(wrapped) != 1 || wrapped[0].ShortID != "C3" {
		t.Errorf("posts 包装没认出来：%+v", wrapped)
	}
	// 插入的是 Markdown 链接，标题里的方括号要转义，节点 slug 进路径。
	if got, want := myTopicPickLink(items[0]), "[第一篇](https://2libra.com/post/work/A1)"; got != want {
		t.Errorf("插入的链接 = %s，期望 %s", got, want)
	}
	odd := myTopicPick{ShortID: "X1", Title: "【A】里的 [标题]", NodeSlug: "work"}
	if got, want := myTopicPickLink(odd), "[【A】里的 \\[标题\\]](https://2libra.com/post/work/X1)"; got != want {
		t.Errorf("方括号没转义：%s，期望 %s", got, want)
	}
}

// 面板打开后经共享 Client 拉 /v1/me/topics，点一行把链接写进正文并收起。
// 换搜索词会重拉，q 用 QueryEscape 拼在路径上。
func TestMyTopicPickerLoadSearchAndInsert(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("q") == "旧" {
			_, _ = fmt.Fprint(w, envelopeJSON(`[{"short_id":"P1","title":"我的旧帖","node":{"slug":"work","name":"职场"}}]`))
			return
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`[{"short_id":"P2","title":"最新的帖子","node":{"slug":"life","name":"生活"}}]`))
	}))
	defer srv.Close()

	a := testApp(t)
	a.autoLoad = true
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer", Username: "jimmy"}); err != nil {
		t.Fatal(err)
	}
	a.client = &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: a.auth}

	e := newMDEditor()
	e.togglePanel("topic")
	body := ""
	tt := ui.NewTester(func(c *ui.Context) {
		a.editorPanels(c, uploadTargetCompose, &e, &body, nil)
	}, 420, 320)
	tt.Frame()
	waitFor(t, func() bool { return e.topicLoaded }, "我的帖子没有加载回来")
	tt.Frame()
	if !tt.HasText("最新的帖子") {
		t.Fatalf("列表没画出来：%q", tt.Texts())
	}

	// 换搜索词：下一帧重新拉，q 转义后出现在 URL 里。
	e.topicQuery = "旧"
	a.reloadMyTopicPicker(uploadTargetCompose, &e, "旧")
	waitFor(t, func() bool { return e.topicLoaded && e.topicQueried == "旧" }, "搜索没有回来")
	tt.Frame()
	if !tt.HasText("我的旧帖") {
		t.Fatalf("搜索结果没画出来：%q", tt.Texts())
	}
	if err := tt.Click("我的旧帖"); err != nil {
		t.Fatalf("点帖子失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if got, want := body, "[我的旧帖](https://2libra.com/post/work/P1)"; got != want {
		t.Errorf("插入的正文 = %q，期望 %q", got, want)
	}
	if e.topicOpen {
		t.Error("选中后应收起选择器面板")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) < 2 {
		t.Fatalf("请求次数 = %d，期望至少两次：%v", len(paths), paths)
	}
	if !strings.HasPrefix(paths[0], "/api/v1/me/topics?") {
		t.Errorf("首次请求 = %s，期望选择器端点", paths[0])
	}
	if !strings.Contains(paths[1], "q="+url.QueryEscape("旧")) {
		t.Errorf("搜索请求没带 q：%s", paths[1])
	}
}

// 真实详情页评论框里点帖子行：既要把链接写进草稿，也不能把输入框收起。
// 之前 expanded 没把 topicOpen 算进去，点面板行失焦后输入框会缩回一行。
func TestTopicPickerClickInDetailComposer(t *testing.T) {
	a := detailAppWithReplies(t)
	a.autoLoad = false
	d := a.detail
	e := &d.mdEditor
	// 结果预先备好，避开网络；面板开合完全走真实点击流程。
	e.topicLoaded = true
	e.topicItems = []myTopicPick{{ShortID: "P1", Title: "我的旧帖", NodeSlug: "work", NodeName: "职场"}}

	tt := ui.NewTester(a.view, 1100, 900)
	tt.Frame()
	// 空草稿、无回复目标：点输入框展开，和用户的操作顺序一致。
	if err := tt.Click("写评论"); err != nil {
		t.Fatalf("点不到评论输入框：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if err := tt.Click("插入帖子"); err != nil {
		t.Fatalf("点不到插入帖子入口：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !e.topicOpen {
		t.Fatalf("点入口后面板没打开：%q", tt.Texts())
	}
	if !tt.HasText("我的旧帖") {
		t.Fatalf("帖子行没渲染：%q", tt.Texts())
	}
	if err := tt.Click("我的旧帖"); err != nil {
		t.Fatalf("点不到帖子行：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	t.Logf("draft=%q topicOpen=%v replyTo=%q", d.draft, e.topicOpen, d.replyTo.ID)
	if got, want := d.draft, "[我的旧帖](https://2libra.com/post/work/P1)"; got != want {
		t.Errorf("点击后草稿 = %q，期望 %q", got, want)
	}
	if !tt.HasText("插入表情") {
		t.Error("点完帖子后评论框被收起了，工具条都不见了")
	}
}

// 「插入帖子」入口在所有共用编辑器的工具条上：点它开面板，再点表情会把
// 它收起，三个面板始终互斥。
func TestEditorTopicButtonInAllEditors(t *testing.T) {
	a := testApp(t)
	e := newMDEditor()
	body := ""
	tt := ui.NewTester(func(c *ui.Context) {
		a.editorPanelButtons(c, &e, &body, nil, "插入表情", "上传图片")
	}, 320, 80)
	tt.Frame()
	if !tt.HasText("插入帖子") {
		t.Fatalf("工具条缺少插入帖子入口：%q", tt.Texts())
	}
	if err := tt.Click("插入帖子"); err != nil {
		t.Fatalf("点插入帖子失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !e.topicOpen {
		t.Error("点插入帖子应打开选择器面板")
	}
	if err := tt.Click("插入表情"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if e.topicOpen || !e.emojiOpen {
		t.Errorf("打开表情应收起帖子面板：topic=%v emoji=%v", e.topicOpen, e.emojiOpen)
	}
}

// 退避：失败后窗口内不重发，超时才重试。
func TestMyTopicPickerBackoff(t *testing.T) {
	if got := myTopicRetryAfter(1); got != 10*time.Second {
		t.Errorf("第 1 次失败退避 = %v，期望 10s", got)
	}
	if got := myTopicRetryAfter(8); got.String() != "1m0s" {
		t.Errorf("多次失败退避应封顶 1 分钟，得到 %v", got)
	}
	e := newMDEditor()
	e.topicErr = "失败"
	e.topicTriedAt = time.Now()
	if e.topicDue(time.Now().Add(5 * time.Second)) {
		t.Error("退避窗口内不该重发")
	}
	if !e.topicDue(time.Now().Add(11 * time.Second)) {
		t.Error("退避窗口过后应重发")
	}
}
