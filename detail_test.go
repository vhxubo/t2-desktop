package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// Shapes follow the JSON samples in api-read.md 帖子 / 评论 / 表情, with a
// few extra blocks (postscripts, vote, children, pinned) and an unknown
// field that decoders must ignore.
const detailTopicJSON = `{
  "short_id":"a1b2c3d4","title":"正文与评论的测试帖",
  "content":"第一段 :sigh: 正文。\n\n- 列表项一\n- 列表项二",
  "type":"normal","is_pinned":true,"comments_closed":false,
  "views":340,"comment_count":42,"created_at":"2026-11-01T09:00:00.000Z",
  "author":{"id":"u1","username":"jimmy","avatar_url":"https://r2.2libra.com/avatars/x.jpg","level":4},
  "node":{"id":"n1","slug":"work","name":"职场"},
  "tags":[{"name":"职场"},{"name":"讨论"}],
  "postscripts":[{"content":"补充一点 :collector:","created_at":"2026-11-01T10:00:00.000Z"}],
  "extra":{"vote":{"title":"选哪个","voting_finished":false,"max_choices":1,
    "options":[{"id":"o1","text":"A","vote_count":5},{"id":"o2","text":"B","vote_count":3}]}},
  "future_field":{"unknown":true}
}`

const detailRepliesJSON = `{
  "items":[
    {"id":"c1","content":"第一楼 :feel_wronged:","floor":1,"created_at":"2026-11-01T10:00:00.000Z",
     "author":{"id":"u2","username":"dannyng"},"reactions":[{"emoji":"👍","count":2,"mine":false}],
     "children":[{"id":"c1a","content":"楼中楼","floor":2,
       "parent":{"id":"c1","content":"第一楼 :sigh:","floor":1},"author":{"username":"alice"}}]},
    {"id":"c2","content":"第二楼","floor":3,"is_deleted":true,"author":null,
     "parent":{"id":"c1","content":"第一楼","floor":1}}
  ],
  "pinned":[{"id":"p1","content":"置顶公告","author":{"username":"mod"}}],
  "total":4,"page":1,"limit":30,"total_pages":1
}`

const detailEmojisJSON = `[
  {"id":"e1","code":":sigh:","name":"叹气","img_url":"https://r2.2libra.com/emojis/sigh.png",
   "names":["叹气","sigh"],"keywords":["sigh"]},
  {"id":"e2","code":":collector:","name":"收藏","img_url":"https://r2.2libra.com/emojis/collector.png"}
]`

func TestDetailPathsMatchContract(t *testing.T) {
	if got := topicPath("a1b2c3d4"); got != "/v1/topics/a1b2c3d4" {
		t.Errorf("详情路径 = %s", got)
	}
	if got := repliesPath("a1b2c3d4", 1, false); got != "/v1/topics/a1b2c3d4/replies?page=1&limit=30" {
		t.Errorf("评论第 1 页路径 = %s", got)
	}
	if got := repliesPath("a1b2c3d4", 2, true); got != "/v1/topics/a1b2c3d4/replies?page=2&limit=30&flat=1" {
		t.Errorf("评论第 2 页 / flat 路径 = %s", got)
	}
	if got := repliesPath("a1b2c3d4", 0, false); !strings.Contains(got, "page=1") {
		t.Errorf("page 应至少为 1：%s", got)
	}
	if got := topicPath("a/b?c=d"); got != "/v1/topics/a%2Fb%3Fc=d" {
		t.Errorf("short_id 未转义：%s", got)
	}
}

func TestDecodeTopic(t *testing.T) {
	p, err := decodeTopic([]byte(detailTopicJSON), "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if p.ShortID != "a1b2c3d4" || p.Title != "正文与评论的测试帖" ||
		!strings.Contains(p.Content, "列表项一") {
		t.Fatalf("详情字段错误：%+v", p)
	}
	if p.Author != "jimmy" || p.AuthorLevel != 4 || p.NodeName != "职场" ||
		p.NodeSlug != "work" || p.Views != 340 || p.CommentCount != 42 || !p.IsPinned {
		t.Errorf("详情元数据错误：%+v", p)
	}
	if p.URL != "https://2libra.com/post/work/a1b2c3d4" {
		t.Errorf("原文链接错误：%s", p.URL)
	}
	if len(p.Tags) != 2 || p.Tags[0] != "职场" {
		t.Errorf("标签解析错误：%v", p.Tags)
	}
	if len(p.Postscripts) != 1 || !strings.Contains(p.Postscripts[0].Content, ":collector:") {
		t.Errorf("附言解析错误：%v", p.Postscripts)
	}
	if p.Vote == nil || p.Vote.Title != "选哪个" || len(p.Vote.Options) != 2 ||
		p.Vote.Options[0].Votes != 5 {
		t.Errorf("投票块解析错误：%+v", p.Vote)
	}
	if p.CommentsClosed {
		t.Error("comments_closed 不应为 true")
	}

	// 匿名帖 author 为 null，不能 panic，也不能留空昵称。
	anon := `{"short_id":"ANON","title":"匿名","content":"x","author":null,"node":null}`
	p2, err := decodeTopic([]byte(anon), "ANON")
	if err != nil || p2.Author != "匿名" || p2.URL != "https://2libra.com/post/ANON" {
		t.Errorf("匿名帖解析错误：%+v %v", p2, err)
	}

	// 帖子不可见时可能返回 200 + d:null。
	_, err = decodeTopic([]byte("null"), "GONE")
	if err == nil || !strings.Contains(friendlyError(err), "not_found") {
		t.Errorf("d:null 应转成 not_found：%v", err)
	}
}

func TestDecodeReplies(t *testing.T) {
	res := decodeReplies([]byte(detailRepliesJSON))
	if res.Total != 4 || res.Page != 1 || res.Limit != 30 || res.TotalPages != 1 {
		t.Fatalf("评论分页元数据错误：%+v", res)
	}
	if len(res.Items) != 2 || len(res.Pinned) != 1 {
		t.Fatalf("items/pinned 数量错误：%+v", res)
	}
	first := res.Items[0]
	if first.ID != "c1" || first.Author != "dannyng" || first.Floor != 1 ||
		first.Reactions != "👍2" || first.Content == "" {
		t.Errorf("第一条评论错误：%+v", first)
	}
	if len(first.Children) != 1 || first.Children[0].Depth != 1 ||
		first.Children[0].Author != "alice" {
		t.Errorf("嵌套回复错误：%+v", first.Children)
	}
	if first.Children[0].ParentSummary != "第一楼" {
		t.Errorf("parent 摘要里的表情短码未清理：%q", first.Children[0].ParentSummary)
	}
	if !res.Items[1].Deleted || res.Items[1].Author != "匿名" {
		t.Errorf("已删除 / 无 author 的评论处理错误：%+v", res.Items[1])
	}
	rows := flattenReplies(res)
	if len(rows) != 4 || !rows[0].Pinned || rows[2].ID != "c1a" || rows[2].Depth != 1 {
		t.Errorf("楼层展平顺序错误：%+v", rows)
	}
	if got := decodeReplies([]byte("null")); len(got.Items) != 0 || got.Total != 0 {
		t.Errorf("d:null 应为空评论页：%+v", got)
	}
}

func TestEmojiMapAndRendering(t *testing.T) {
	m := decodeEmojis([]byte(detailEmojisJSON))
	if len(m) != 2 || m[":sigh:"].ImgURL != "https://r2.2libra.com/emojis/sigh.png" {
		t.Fatalf("表情映射错误：%+v", m)
	}
	src := "有啥看啥:feel_wronged: 和 :sigh:"
	md := replaceEmoji(src, m, true)
	if !strings.Contains(md, "![叹气](https://r2.2libra.com/emojis/sigh.png)") ||
		!strings.Contains(md, ":feel_wronged:") {
		t.Errorf("markdown 表情替换错误：%s", md)
	}
	plain := replaceEmoji(src, m, false)
	if !strings.Contains(plain, "[叹气]") || strings.Contains(plain, "img_url") {
		t.Errorf("纯文本表情替换错误：%s", plain)
	}
	if got := replaceEmoji(src, nil, true); got != src {
		t.Errorf("没有映射表时应原样保留短码：%s", got)
	}
	// 数组外包一层对象也能读（服务端随时可能加字段）。
	wrapped := `{"emojis":` + detailEmojisJSON + `,"extra":1}`
	if len(decodeEmojis([]byte(wrapped))) != 2 {
		t.Error("对象形式的表情表解析失败")
	}
}

func TestItemCarriesDetailIDs(t *testing.T) {
	post := `{"short_id":"JBYGf2A","title":"拿到了 offer","author":{"username":"bighammer"},"node":{"slug":"work"}}`
	var env envelope
	if err := json.Unmarshal([]byte(envelopeJSON(`{"posts":[`+post+`]}`)), &env); err != nil {
		t.Fatal(err)
	}
	res, err := decodePage(env.D, tabSpecs[0], 1)
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("帖子列表解析失败：%v", err)
	}
	if res.Items[0].ShortID != "JBYGf2A" || res.Items[0].CommentID != "" {
		t.Errorf("帖子行缺少 ShortID：%+v", res.Items[0])
	}
	comment := `{"id":"f86db611","floor":12,"content":"有啥看啥","author":{"username":"dannyng"},` +
		`"post":{"short_id":"wEOIDnn","title":"社会观察","node":{"slug":"social-observation"}}}`
	var env2 envelope
	if err := json.Unmarshal([]byte(envelopeJSON(`{"items":[`+comment+`]}`)), &env2); err != nil {
		t.Fatal(err)
	}
	res2, err := decodePage(env2.D, tabSpecs[4], 1)
	if err != nil || len(res2.Items) != 1 {
		t.Fatalf("评论列表解析失败：%v", err)
	}
	if res2.Items[0].ShortID != "wEOIDnn" || res2.Items[0].CommentID != "f86db611" {
		t.Errorf("评论行缺少帖子/评论 ID：%+v", res2.Items[0])
	}
}

func TestClientDetailEndpoints(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/topics/ABC12345":
			_, _ = fmt.Fprint(w, envelopeJSON(detailTopicJSON))
		case "/api/v1/topics/ABC12345/replies":
			_, _ = fmt.Fprint(w, envelopeJSON(detailRepliesJSON))
		case "/api/v1/emojis":
			_, _ = fmt.Fprint(w, envelopeJSON(detailEmojisJSON))
		default:
			t.Errorf("意外路径 %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api"}
	ctx := context.Background()

	topic, err := c.fetchTopic(ctx, "ABC12345")
	if err != nil || topic.Title != "正文与评论的测试帖" {
		t.Fatalf("详情请求失败：%v %+v", err, topic)
	}
	replies, err := c.fetchReplies(ctx, "ABC12345", 2, true)
	if err != nil || len(replies.Items) != 2 {
		t.Fatalf("评论请求失败：%v %+v", err, replies)
	}
	emojis, err := c.fetchEmojis(ctx)
	if err != nil || len(emojis) != 2 {
		t.Fatalf("表情请求失败：%v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"/api/v1/topics/ABC12345",
		"/api/v1/topics/ABC12345/replies?page=2&limit=30&flat=1",
		"/api/v1/emojis",
	}
	for i, w := range want {
		if i >= len(seen) || seen[i] != w {
			t.Errorf("第 %d 个请求 = %q，期望 %q（实际 %v）", i, at(seen, i), w, seen)
		}
	}
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

// A 401 on the detail endpoint must go through the same single refresh +
// retry path as the list tabs (index.md 4.5: never silently downgrade).
func TestDetailRefreshesOn401(t *testing.T) {
	var success atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"new","refresh_token":"r2","token_type":"Bearer","expires_in":3600}}`)
		case "/api/v1/topics/XYZ":
			if r.Header.Get("Authorization") != "Bearer new" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = fmt.Fprint(w, `{"c":401,"code":"unauthorized","m":"令牌失效","d":null}`)
				return
			}
			success.Add(1)
			_, _ = fmt.Fprint(w, envelopeJSON(detailTopicJSON))
		default:
			t.Errorf("意外路径 %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := newAuthManager(dir)
	m.apiBase = srv.URL + "/api"
	m.http = srv.Client()
	if err := m.storeToken(tokenData{AccessToken: "old", RefreshToken: "r1", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: m}
	if _, err := c.fetchTopic(context.Background(), "XYZ"); err != nil {
		t.Fatalf("刷新后重试失败：%v", err)
	}
	if success.Load() != 1 || m.accessToken() != "new" {
		t.Errorf("未按新令牌重试一次：成功 %d 次，令牌 %s", success.Load(), m.accessToken())
	}
}

func TestDetailErrorCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","m":"缺少权限范围","required_scope":["read"],"d":null}`)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api"}
	_, err := c.fetchReplies(context.Background(), "ABC12345", 1, false)
	if err == nil || !strings.Contains(friendlyError(err), "read") {
		t.Errorf("403 错误提示不对：%v", err)
	}
}

// Clicking a row opens the in-app detail page: post body on one side,
// floors on the other, with author + source link retained (terms.md).
func TestClickOpensDetailView(t *testing.T) {
	a := testApp(t) // autoLoad=false：headless 不发网络请求
	row := Item{
		Key: "JBYGf2A", Title: "测试帖子标题", Subtitle: "职场 · jimmy",
		URL: "https://2libra.com/post/work/JBYGf2A", ShortID: "JBYGf2A",
	}
	a.tabs[0].items = []Item{row}
	a.tabs[0].loaded = true
	a.tabs[0].page = 1
	a.tabs[0].total = 1
	tt := ui.NewTester(a.view, 1100, 760)
	if err := tt.Click("测试帖子标题"); err != nil {
		t.Fatalf("点击列表行失败：%v（%q）", err, tt.Texts())
	}
	if a.pageName != "detail" || a.detail == nil || a.detail.shortID != "JBYGf2A" {
		t.Fatalf("点击后没有进入详情页：%s %+v", a.pageName, a.detail)
	}

	topic, err := decodeTopic([]byte(detailTopicJSON), "JBYGf2A")
	if err != nil {
		t.Fatal(err)
	}
	pg := decodeReplies([]byte(detailRepliesJSON))
	a.detail.topic = &topic
	a.detail.replyList = flattenReplies(pg)
	a.detail.replyTotal = pg.Total
	a.detail.replyPage = 1
	a.detail.replyTotalPages = 1
	a.detail.replyLoaded = true
	a.emojis = decodeEmojis([]byte(detailEmojisJSON))

	view := ui.NewTester(a.view, 1100, 760)
	for _, want := range []string{
		"正文与评论的测试帖", "jimmy", "列表项一", "选哪个", "补充一点",
		"置顶公告", "第一楼", "楼中楼", "dannyng", "该评论已删除",
		"内容来自 2Libra 用户",
	} {
		if !view.HasText(want) {
			t.Errorf("详情页没有显示 %q：%q", want, view.Texts())
		}
	}
	// 评论数（图标 + 数值）与视图切换（图标按钮）现在贴在写评论旁边。
	if _, ok := view.Find("评论数"); !ok {
		t.Errorf("详情页没有评论数按钮：%q", view.Texts())
	}
	if _, ok := view.Find("切到扁平时间序"); !ok {
		t.Errorf("详情页没有视图切换按钮：%q", view.Texts())
	}
	if !view.HasText("版权归原作者所有") {
		t.Error("缺少版权与原文链接声明")
	}
	if err := view.Click("切到扁平时间序"); err != nil {
		t.Errorf("切换评论视图失败：%v", err)
	}
	if !a.detail.flat {
		t.Error("flat 视图未切换")
	}
	if err := view.Click("关闭详情"); err != nil {
		t.Errorf("返回列表失败：%v", err)
	}
	if a.pageName != "list" || a.detail != nil {
		t.Errorf("返回后状态不对：%s %+v", a.pageName, a.detail)
	}
}

// A comment row opens its parent topic, remembers the comment id and
// starts in the flat view so the floor can be located.
func TestClickCommentOpensParentTopic(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{
		Key: "f86db611", Title: "有啥看啥", ShortID: "wEOIDnn",
		CommentID: "f86db611", URL: "https://2libra.com/post/social-observation/wEOIDnn?commentId=f86db611",
	})
	if a.pageName != "detail" || a.detail == nil {
		t.Fatal("评论行没有打开详情页")
	}
	d := a.detail
	if d.shortID != "wEOIDnn" || d.focusID != "f86db611" || !d.flat {
		t.Errorf("评论定位信息不对：%+v", d)
	}
	topic, _ := decodeTopic([]byte(detailTopicJSON), "wEOIDnn")
	d.topic = &topic
	if !strings.Contains(d.originURL(), "?commentId=f86db611") {
		t.Errorf("原文链接应定位到评论：%s", d.originURL())
	}
	view := ui.NewTester(a.view, 1100, 760)
	if !view.HasText("来自评论定位") {
		t.Errorf("缺少评论定位提示：%q", view.Texts())
	}
	if _, ok := view.Find("切到楼层视图"); !ok {
		t.Errorf("详情页没有视图切换按钮：%q", view.Texts())
	}
	a.closeDetail()
	if a.detail != nil || a.pageName != "list" {
		t.Error("关闭详情页状态不对")
	}
}

// Optional live check: TWOLIBRA_LIVE=1 go test -run TestLiveDetail -v
func TestLiveDetail(t *testing.T) {
	if os.Getenv("TWOLIBRA_LIVE") == "" {
		t.Skip("设置 TWOLIBRA_LIVE=1 才会访问真实 API")
	}
	c := &Client{}
	res, err := c.fetch(context.Background(), tabSpecs[0], 1)
	if err != nil || len(res.Items) == 0 {
		t.Fatalf("取首页失败：%v", err)
	}
	id := res.Items[0].ShortID
	if id == "" {
		t.Skip("首页首条没有 short_id")
	}
	topic, err := c.fetchTopic(context.Background(), id)
	if err != nil {
		t.Fatalf("详情失败：%v", err)
	}
	replies, err := c.fetchReplies(context.Background(), id, 1, false)
	if err != nil {
		t.Fatalf("评论失败：%v", err)
	}
	t.Logf("%s · %s · %d 字正文 · %d 条评论 / 共 %d 条",
		id, topic.Title, len(topic.Content), len(replies.Items), replies.Total)

	// Render the real markdown body and floors headlessly once, so a shape
	// the docs did not mention cannot silently break the detail page.
	a := testApp(t)
	a.openDetail(Item{ShortID: id, URL: topic.URL})
	a.detail.topic = &topic
	a.detail.replyList = flattenReplies(replies)
	a.detail.replyTotal = replies.Total
	a.detail.replyPage = 1
	a.detail.replyTotalPages = replies.TotalPages
	a.detail.replyLoaded = true
	if emojis, err := c.fetchEmojis(context.Background()); err == nil {
		a.emojis = emojis
	}
	view := ui.NewTester(a.view, 1100, 760)
	if topic.Title == "" || !view.HasText(topic.Title) {
		t.Errorf("真实标题没有渲染出来：%q", view.Texts())
	}
	if !view.HasText("内容来自 2Libra 用户") {
		t.Error("真实详情页缺少版权与原文链接声明")
	}
}

// POST /v1/topics/:short_id/replies: path, auth, mandatory Idempotency-Key,
// body fields (content + optional parentId) and the 201 decoded reply.
func TestCreateReplyPostsComment(t *testing.T) {
	var mu sync.Mutex
	var method, path, auth, key, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		method = r.Method
		path = r.URL.Path
		auth = r.Header.Get("Authorization")
		key = r.Header.Get("Idempotency-Key")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"r9","content":"顶","floor":7,"author":{"username":"jimmy"},"created_at":"2026-11-01T10:00:00.000Z"}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}

	reply, err := c.createReply(context.Background(), "JBYGf2A", "顶", "c1")
	if err != nil {
		t.Fatalf("发评论失败：%v", err)
	}
	mu.Lock()
	if method != "POST" || path != "/api/v1/topics/JBYGf2A/replies" {
		t.Errorf("请求不对：%s %s", method, path)
	}
	if auth != "Bearer tok" || key == "" {
		t.Errorf("缺鉴权/幂等头：auth=%q key=%q", auth, key)
	}
	if !strings.Contains(body, `"content":"顶"`) || !strings.Contains(body, `"parentId":"c1"`) {
		t.Errorf("请求体缺少 content/parentId：%s", body)
	}
	mu.Unlock()
	if reply.ID != "r9" || reply.Content != "顶" || reply.Floor != 7 || reply.Author != "jimmy" {
		t.Errorf("返回的评论解析错误：%+v", reply)
	}

	// 顶层评论不带 parentId，也不该出现该字段。
	mu.Lock()
	body = ""
	mu.Unlock()
	if _, err := c.createReply(context.Background(), "JBYGf2A", "顶", ""); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(body, "parentId") {
		t.Errorf("顶层评论不该带 parentId：%s", body)
	}
}

// A 401 on the reply endpoint must refresh + retry exactly like the reads.
func TestCreateReplyRefreshesOn401(t *testing.T) {
	var success atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/token":
			_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"new","refresh_token":"r2","token_type":"Bearer","expires_in":3600}}`)
		case "/api/v1/topics/XYZ/replies":
			if r.Header.Get("Authorization") != "Bearer new" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = fmt.Fprint(w, `{"c":401,"code":"unauthorized","m":"令牌失效","d":null}`)
				return
			}
			success.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"r1","content":"x","floor":1,"author":{"username":"jimmy"}}`))
		default:
			t.Errorf("意外路径 %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := newAuthManager(dir)
	m.apiBase = srv.URL + "/api"
	m.http = srv.Client()
	if err := m.storeToken(tokenData{AccessToken: "old", RefreshToken: "r1", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: m}
	if _, err := c.createReply(context.Background(), "XYZ", "x", ""); err != nil {
		t.Fatalf("刷新后重试失败：%v", err)
	}
	if success.Load() != 1 || m.accessToken() != "new" {
		t.Errorf("未按新令牌重试一次：成功 %d 次，令牌 %s", success.Load(), m.accessToken())
	}
}

// A 403 insufficient_scope (missing write:comment) surfaces a friendly hint.
func TestCreateReplyScopeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `{"c":403,"code":"insufficient_scope","m":"缺少权限范围","required_scope":["write:comment"],"d":null}`)
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}
	_, err := c.createReply(context.Background(), "ABC12345", "x", "")
	if err == nil || !strings.Contains(friendlyError(err), "write:comment") {
		t.Errorf("403 错误提示不对：%v", err)
	}
}

// The composer shows a login prompt for anonymous users and no send button.
func TestReplyComposerAnonymous(t *testing.T) {
	a := testApp(t) // 无令牌 = 未登录
	a.openDetail(Item{ShortID: "JBYGf2A", URL: "https://2libra.com/post/work/JBYGf2A"})
	topic, _ := decodeTopic([]byte(detailTopicJSON), "JBYGf2A")
	a.detail.topic = &topic
	pg := decodeReplies([]byte(detailRepliesJSON))
	a.detail.replyList = flattenReplies(pg)
	a.detail.replyTotal = pg.Total
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760) // 单一 Tester：避免同一 ListState 被两个引擎渲染
	if !tt.HasText("写评论") {
		t.Error("没有评论输入框")
	}
	if !tt.HasText("登录后评论") {
		t.Error("未登录时应引导登录后再评论")
	}
	if tt.HasText("发送") {
		t.Error("未登录不应出现发送按钮")
	}
	// 回复入口仅在悬浮评论行时出现，位于右上角，保持默认状态清爽。
	// 注意：嵌套评论的「回复 N 楼」父引用始终可见，故用 Find 精确匹配按钮文字。
	if _, ok := tt.Find("回复"); ok {
		t.Error("未悬浮评论行时不应展示回复入口")
	}
	if r, ok := tt.Find("1 楼"); !ok {
		t.Fatal("找不到评论行用于悬浮")
	} else {
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
	}
	if _, ok := tt.Find("回复"); !ok {
		t.Error("悬浮评论行后应出现回复入口")
	}
}

// 回复输入框默认只占一行，把高度让给评论列表；点击（选中）后展开为多行
// 编辑器，失焦或发送成功后折回一行，有草稿或正在回复某层楼时保持展开。
func TestReplyComposerCollapsesUnlessSelected(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1100, 760)

	collapsed, ok := tt.Find("写评论")
	if !ok {
		t.Fatal("找不到回复输入框")
	}
	if collapsed.H > 45 {
		t.Fatalf("回复输入框默认应只占一行：高 %.1f", collapsed.H)
	}
	if !tt.HasText("发送") {
		t.Error("折叠状态也应保留发送按钮")
	}

	// 选中（点击输入框）后展开
	tt.ClickAt(collapsed.X+collapsed.W/2, collapsed.Y+collapsed.H/2)
	tt.Frame()
	expanded, ok := tt.Find("写评论")
	if !ok {
		t.Fatal("输入框消失")
	}
	if expanded.H < collapsed.H+20 {
		t.Errorf("选中后应展开：%.1f -> %.1f", collapsed.H, expanded.H)
	}

	// 发送成功后（composerIdle）即便仍在输入框里，也折回一行
	a.detail.composerIdle = true
	tt.Frame()
	if r, ok := tt.Find("写评论"); !ok || r.H > 45 {
		t.Errorf("发送后应折回一行：%+v", r)
	}

	// 失焦后保持折叠；再次选中重新展开
	blur := func() {
		r, ok := tt.Find("评论数")
		if !ok {
			t.Fatal("找不到评论数按钮，无法移开焦点")
		}
		tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
	}
	blur()
	if r, ok := tt.Find("写评论"); !ok || r.H > 45 {
		t.Errorf("失焦后应保持折叠：%+v", r)
	}
	if r, ok := tt.Find("写评论"); ok {
		tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
	}
	if r, ok := tt.Find("写评论"); !ok || r.H < collapsed.H+20 {
		t.Errorf("再次选中后应重新展开：%+v", r)
	}

	// 有草稿时不因失焦收起正在编辑的内容
	a.detail.draft = "还没写完"
	blur()
	if r, ok := tt.Find("写评论"); !ok || r.H < collapsed.H+20 {
		t.Errorf("有草稿时应保持展开：%+v", r)
	}
}

// detailAppWithReplies 打开一个详情页，填好正文与评论，并置为登录态。
func detailAppWithReplies(t *testing.T) *app {
	t.Helper()
	a := testApp(t)
	m := newAuthManager(t.TempDir())
	_ = m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"})
	a.auth = m
	a.openDetail(Item{ShortID: "JBYGf2A", URL: "https://2libra.com/post/work/JBYGf2A"})
	topic, _ := decodeTopic([]byte(detailTopicJSON), "JBYGf2A")
	a.detail.topic = &topic
	pg := decodeReplies([]byte(detailRepliesJSON))
	a.detail.replyList = flattenReplies(pg)
	a.detail.replyTotal = pg.Total
	a.detail.replyLoaded = true
	return a
}

// 悬浮评论行时右上角的「回复」按钮出现，但整条评论的高度保持不变，
// 不应发生上下跳动：头部行与底部链接行都固定高度占位。
func TestReplyButtonNoLayoutShift(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1100, 760)

	// 未悬浮时，以第二条评论「楼中楼」的纵向位置作为对照基准。
	base, ok := tt.Find("楼中楼")
	if !ok {
		t.Fatal("找不到第二条评论用于对照")
	}
	yBase := base.Y

	// 悬浮第一条评论（"1 楼"），触发右上角「回复」按钮出现。
	row1, ok := tt.Find("1 楼")
	if !ok {
		t.Fatal("找不到第一条评论用于悬浮")
	}
	tt.Move(row1.X+row1.W/2, row1.Y+row1.H/2)
	tt.Frame()

	if _, ok := tt.Find("回复"); !ok {
		t.Fatal("悬浮后没有出现回复入口")
	}
	after, ok := tt.Find("楼中楼")
	if !ok {
		t.Fatal("悬浮后第二条评论消失")
	}
	dy := after.Y - yBase
	if dy > 1 || dy < -1 {
		t.Errorf("悬浮出现回复按钮后整条评论高度变化，发生跳动：Y %.1f -> %.1f (Δ%v)", yBase, after.Y, dy)
	}
}

// 单页评论不展示分页控件（第 N 页 / 上一页 / 下一页），保持界面清爽。
func TestReplyPaginationHiddenOnSinglePage(t *testing.T) {
	a := detailAppWithReplies(t)
	a.detail.replyTotalPages = 1
	a.detail.replyPage = 1
	tt := ui.NewTester(a.view, 1100, 760)
	for _, hide := range []string{"第 1 页 / 共 1 页", "上一页", "下一页"} {
		if tt.HasText(hide) {
			t.Errorf("单页评论不应显示分页控件 %q：%q", hide, tt.Texts())
		}
	}
}

// 多页评论用无限滚动续拉：不再显示页码 / 上一页 / 下一页，而是由
// replyCanLoadMore 控制滚到底时是否还能继续加载。
func TestReplyInfiniteScrollCanLoadMore(t *testing.T) {
	a := detailAppWithReplies(t)
	a.detail.replyTotalPages = 2
	a.detail.replyPage = 1
	tt := ui.NewTester(a.view, 1100, 760)
	for _, hide := range []string{"第 1 页 / 共 2 页", "上一页", "下一页"} {
		if tt.HasText(hide) {
			t.Errorf("无限滚动不应显示分页控件 %q：%q", hide, tt.Texts())
		}
	}
	if !replyCanLoadMore(a.detail) {
		t.Error("第 1 / 2 页应允许继续加载")
	}
	a.detail.replyPage = 2
	if replyCanLoadMore(a.detail) {
		t.Error("最后一页不应继续加载")
	}
	a.detail.replyPage = 1
	a.detail.replyNoMore = true
	if replyCanLoadMore(a.detail) {
		t.Error("空页兜底标记后不应继续加载")
	}
}

// 续拉下一页评论失败时，已经显示的评论必须留在原地，不能被整页错误替换；
// 失败原因只在列表底部出现，并带「重试」。
func TestReplyLoadMoreFailureKeepsRows(t *testing.T) {
	a := detailAppWithReplies(t)
	a.detail.replyPage = 2
	a.detail.replyTotalPages = 2
	a.detail.replyMoreErr = "服务端错误（500），请稍后重试"

	tt := ui.NewTester(a.view, 1100, 760)
	for _, want := range []string{"第一楼", "该评论已删除", "加载更多失败", "重试"} {
		if !tt.HasText(want) {
			t.Errorf("续拉评论失败时缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 续拉下一页评论时，列表保持可见，底部显示加载进度。
func TestReplyLoadMoreShowsProgress(t *testing.T) {
	a := detailAppWithReplies(t)
	a.detail.replyLoading = true

	tt := ui.NewTester(a.view, 1100, 760)
	for _, want := range []string{"第一楼", "正在加载更多…"} {
		if !tt.HasText(want) {
			t.Errorf("续拉评论进度缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 评论续拉失败后点「重试」会重新请求失败的那一页，新评论接在原有评论后面。
func TestReplyLoadMoreRetryAppendsPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "page=2") {
			t.Errorf("重试请求了错误的页码：%s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = fmt.Fprint(w, `{"c":0,"m":"请求成功","d":{
			"items":[{"id":"late-1","content":"续拉的评论","floor":5,
				"author":{"username":"latecomer"}}],
			"total":5,"page":2,"limit":30,"total_pages":2}}`)
	}))
	defer srv.Close()

	a := detailAppWithReplies(t)
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.autoLoad = true
	a.detail.replyPage = 2
	a.detail.replyTotalPages = 2
	a.detail.replyMoreErr = "服务端错误（500），请稍后重试"

	tt := ui.NewTester(a.view, 1100, 760)
	if err := tt.Click("重试"); err != nil {
		t.Fatalf("点击重试失败：%v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !tt.HasText("续拉的评论") {
		if time.Now().After(deadline) {
			t.Fatalf("重试后没有加载出第 2 页评论：%q", tt.Texts())
		}
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if !tt.HasText("第一楼") {
		t.Errorf("续拉评论后原有评论丢失：%q", tt.Texts())
	}
}

// Logged-in users get a send button; clicking 回复 on a floor targets that
// comment for nesting and shows the "正在回复 N 楼" chip. An empty draft is a
// synchronous no-op that surfaces a validation message (no network goroutine).
func TestReplyComposerLoggedIn(t *testing.T) {
	a := testApp(t)
	dir := t.TempDir()
	m := newAuthManager(dir)
	_ = m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"})
	a.auth = m
	a.openDetail(Item{ShortID: "JBYGf2A", URL: "https://2libra.com/post/work/JBYGf2A"})
	topic, _ := decodeTopic([]byte(detailTopicJSON), "JBYGf2A")
	a.detail.topic = &topic
	pg := decodeReplies([]byte(detailRepliesJSON))
	a.detail.replyList = flattenReplies(pg)
	a.detail.replyTotal = pg.Total
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760) // 单一 Tester
	if !tt.HasText("发送") {
		t.Error("登录态下没有发送按钮")
	}
	// 「回复」入口悬浮时出现于右上角：先把指针移到第一行（"1 楼"）。
	if r, ok := tt.Find("1 楼"); !ok {
		t.Fatal("找不到评论行用于悬浮")
	} else {
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
	}
	if err := tt.Click("回复"); err != nil {
		t.Fatalf("点击回复失败：%v", err)
	}
	if a.detail.replyToID == "" {
		t.Error("点击回复后没有设置被回复的评论 id")
	}
	if !tt.HasText("正在回复") {
		t.Error("应设置『正在回复 N 楼』提示")
	}
	// 点「回复」后输入框应展开并聚焦，点完就能直接写。
	if r, ok := tt.Find("写评论"); !ok || r.H < 60 {
		t.Errorf("点回复后输入框应展开：%+v", r)
	}
	if !tt.Focused("写评论") {
		t.Error("点回复后应聚焦输入框")
	}
	// 空内容直接调用 submitReply 走同步校验、不发起请求（UI 上此时按钮已禁用，
	// 点击不会触发，这里绕过按钮验证守卫逻辑本身）。
	a.detail.draft = ""
	a.submitReply()
	if !strings.Contains(a.detail.postErr, "不能为空") {
		t.Errorf("空内容应被校验拦截：%q", a.detail.postErr)
	}
}

// A full send round trip through the composer: the draft is cleared, the new
// comment is prepended, and the total count increments.
func TestReplyComposerSends(t *testing.T) {
	var mu sync.Mutex
	var gotPath, gotAuth, gotKey, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/oauth/token" {
			_, _ = fmt.Fprint(w, `{"c":0,"d":{"access_token":"tok","refresh_token":"r","token_type":"Bearer","expires_in":3600}}`)
			return
		}
		mu.Lock()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.Header.Get("Idempotency-Key")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"new1","content":"测试评论","floor":99,"author":{"username":"jimmy"},"created_at":"2026-11-01T10:00:00.000Z"}`))
	}))
	defer srv.Close()

	a := testApp(t)
	dir := t.TempDir()
	m := newAuthManager(dir)
	m.apiBase = srv.URL + "/api"
	m.http = srv.Client()
	_ = m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"})
	a.auth = m
	a.client = &Client{HTTP: srv.Client(), Base: srv.URL + "/api", Auth: m}

	a.openDetail(Item{ShortID: "JBYGf2A", URL: "https://2libra.com/post/work/JBYGf2A"})
	topic, _ := decodeTopic([]byte(detailTopicJSON), "JBYGf2A")
	a.detail.topic = &topic
	pg := decodeReplies([]byte(detailRepliesJSON))
	a.detail.replyList = flattenReplies(pg)
	a.detail.replyTotal = pg.Total
	a.detail.replyLoaded = true

	// headless 无法模拟打字，直接设置草稿状态。直接调用 submitReply 走网络通道，
	// 不通过 Click 触发渲染：否则发送 goroutine 写 detail 状态会与 Tester 渲染
	// 读状态竞态（生产环境 publish 走单一 UI 线程，无此问题）。
	a.detail.draft = "测试评论"
	done := make(chan struct{})
	a.onReplyPosted = func(replyItem, error) { close(done) }
	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("发送") {
		t.Fatal("登录态下没有发送按钮")
	}
	a.submitReply()
	// onReplyPosted 钩子在发送 goroutine 内、状态已更新后触发，用作完成信号。
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("发送回调超时")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/api/v1/topics/JBYGf2A/replies" {
		t.Errorf("发评论路径不对：%s", gotPath)
	}
	if gotAuth != "Bearer tok" || gotKey == "" {
		t.Errorf("缺鉴权/幂等头：auth=%q key=%q", gotAuth, gotKey)
	}
	if !strings.Contains(gotBody, `"content":"测试评论"`) || strings.Contains(gotBody, "parentId") {
		t.Errorf("请求体不对：%s", gotBody)
	}
	if a.detail.draft != "" {
		t.Errorf("发送后草稿未清空：%q", a.detail.draft)
	}
	if a.detail.replyTotal != pg.Total+1 {
		t.Errorf("评论数未 +1：%d", a.detail.replyTotal)
	}
	if len(a.detail.replyList) != len(flattenReplies(pg))+1 {
		t.Errorf("新评论未插入列表：%d", len(a.detail.replyList))
	}
}
