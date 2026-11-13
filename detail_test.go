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
  "node":{"id":"n1","slug":"work","name":"职场",
    "parent":{"id":"n0","slug":"life","name":"生活"},"parent_slug":"life"},
  "tags":[{"name":"职场"},{"name":"讨论"}],
  "reactions_summary":[{"emoji":"👍","count":8},{"emoji":"❤️","count":6}],
  "my_reactions":["👍"],
  "rewards":[{"amount":200,"user":{"username":"Jimmy"}},{"amount":100,"user":{"username":"alice"}}],
  "postscripts":[{"content":"补充一点 :collector:","created_at":"2026-11-01T10:00:00.000Z"}],
  "extra":{"vote":{"title":"选哪个","voting_finished":false,"max_choices":1,
    "options":[{"id":"o1","text":"A","vote_count":5},{"id":"o2","text":"B","vote_count":3}]}},
  "future_field":{"unknown":true}
}`

const detailRepliesJSON = `{
  "items":[
    {"id":"c1","content":"第一楼 :feel_wronged:","floor":1,"created_at":"2026-11-01T10:00:00.000Z",
     "author":{"id":"u2","username":"dannyng"},"reactions":[{"emoji":"👍","count":2,"mine":false}],
     "my_reactions":["👍"],"rewards":[{"amount":100,"user":{"username":"jimmy"}}],
     "children":[{"id":"c1a","content":"楼中楼","floor":2,
       "parent":{"id":"c1","content":"第一楼 :sigh:","floor":1},"author":{"username":"alice"}}]},
    {"id":"c2","content":"第二楼","floor":3,"is_deleted":true,"author":null,
     "parent":{"id":"c1","content":"第一楼","floor":1}}
  ],
  "pinned":[{"id":"p1","content":"置顶公告","author":{"username":"mod"}}],
  "total":4,"page":1,"limit":100,"total_pages":1
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
	if got := repliesPath("a1b2c3d4", 1, false); got != "/v1/topics/a1b2c3d4/replies?page=1&limit=100" {
		t.Errorf("评论第 1 页路径 = %s", got)
	}
	if got := repliesPath("a1b2c3d4", 2, true); got != "/v1/topics/a1b2c3d4/replies?page=2&limit=100&flat=1" {
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
	// 两级分类：线上详情把父节点放在 node.parent，旧响应只有 parent_slug。
	if p.NodeParentName != "生活" || p.NodeParentSlug != "life" ||
		p.categoryPath() != "生活 · 职场" {
		t.Errorf("节点父级解析错误：%+v", p)
	}
	if got := (&topicDetail{NodeName: "职场"}).categoryPath(); got != "职场" {
		t.Errorf("只有一级分类时应退回节点名：%q", got)
	}
	if got := (&topicDetail{NodeName: "职场", NodeParentSlug: "life"}).categoryPath(); got != "职场" {
		t.Errorf("只知父节点 slug 时不应拿它顶替父名：%q", got)
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
	if p.FavoriteCountKnown {
		t.Error("响应里没有 favorited_count 时不应把收藏数当成已知的 0")
	}
	if len(p.Reactions) != 2 || p.Reactions[0].Emoji != "👍" || p.Reactions[0].Count != 8 ||
		!p.Reactions[0].Mine || p.Reactions[1].Mine {
		t.Errorf("帖子表情表态解析错误：%+v", p.Reactions)
	}
	if p.Rewards.Total != 300 || p.Rewards.Count != 2 || len(p.Rewards.Users) != 2 ||
		p.Rewards.Users[0] != "Jimmy" {
		t.Errorf("帖子打赏解析错误：%+v", p.Rewards)
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
	if res.Total != 4 || res.Page != 1 || res.Limit != 100 || res.TotalPages != 1 {
		t.Fatalf("评论分页元数据错误：%+v", res)
	}
	if len(res.Items) != 2 || len(res.Pinned) != 1 {
		t.Fatalf("items/pinned 数量错误：%+v", res)
	}
	first := res.Items[0]
	if first.ID != "c1" || first.Author != "dannyng" || first.Floor != 1 || first.Content == "" {
		t.Errorf("第一条评论错误：%+v", first)
	}
	if len(first.Reactions) != 1 ||
		first.Reactions[0] != (reactionStat{Emoji: "👍", Count: 2, Mine: true}) {
		t.Errorf("评论表情表态解析错误：%+v", first.Reactions)
	}
	if first.Rewards.Total != 100 || first.Rewards.Count != 1 ||
		len(first.Rewards.Users) != 1 || first.Rewards.Users[0] != "jimmy" {
		t.Errorf("评论打赏解析错误：%+v", first.Rewards)
	}
	if len(first.Children) != 1 || first.Children[0].Depth != 1 ||
		first.Children[0].Author != "alice" {
		t.Errorf("嵌套回复错误：%+v", first.Children)
	}
	if first.Children[0].ParentSummary != "第一楼" {
		t.Errorf("parent 摘要里的表情短码未清理：%q", first.Children[0].ParentSummary)
	}
	if first.Children[0].ParentID != "c1" {
		t.Errorf("父评论 uuid 未解析：%q", first.Children[0].ParentID)
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

// 线上 API 与 api-read.md 的示例字段名并不完全一致：投票在顶层 d.vote，
// 表态是 reactions_summary，置顶评论是 pinned_comments。这里按真实响应
// 的形状断言，避免示例字段能过测试、真实帖子却渲染不出来。
func TestDecodeLiveFieldNames(t *testing.T) {
	topic, err := decodeTopic([]byte(`{
		"short_id":"LIVE001","title":"线上字段","content":"x",
		"vote":{"title":"选哪个","voting_finished":true,"max_choices":1,
			"options":[{"text":"A","vote_count":2},{"text":"B","vote_count":1}]}
	}`), "LIVE001")
	if err != nil {
		t.Fatal(err)
	}
	if topic.Vote == nil || topic.Vote.Title != "选哪个" || !topic.Vote.Finished ||
		len(topic.Vote.Options) != 2 || topic.Vote.Options[0].Votes != 2 {
		t.Errorf("顶层 vote 没有解析：%+v", topic.Vote)
	}

	pg := decodeReplies([]byte(`{
		"items":[{"id":"c1","content":"评论","floor":1,
			"author":{"username":"dannyng"},
			"reactions_summary":[{"emoji":"👍","count":3}]}],
		"pinned_comments":[{"id":"p1","content":"置顶公告",
			"author":{"username":"mod"}}],
		"total":2,"page":1,"limit":100,"total_pages":1}`))
	if len(pg.Items) != 1 || len(pg.Items[0].Reactions) != 1 ||
		pg.Items[0].Reactions[0] != (reactionStat{Emoji: "👍", Count: 3}) {
		t.Errorf("reactions_summary 没有解析：%+v", pg.Items)
	}
	if len(pg.Pinned) != 1 || !pg.Pinned[0].Pinned || pg.Pinned[0].Content != "置顶公告" {
		t.Errorf("pinned_comments 没有解析：%+v", pg.Pinned)
	}
}

// 表情表态与打赏的解析要兼容文档与线上两种写法：reactions_summary /
// reactions、my_reactions（字符串数组或对象数组）和单条记录里的 mine；
// 打赏按金额合计，0 元记录不计数，同一个人只留一个昵称。
func TestDecodeReactionsAndRewards(t *testing.T) {
	topic, err := decodeTopic([]byte(`{
		"short_id":"SOCIAL1","title":"互动","content":"x",
		"reactions":[{"emoji":"👍","count":2,"mine":true},{"emoji":"🎉","count":1}],
		"my_reactions":[{"emoji":"🎉"},{"emoji":"🔥"}],
		"rewards":[{"amount":30,"user":{"username":"alice"}},
			{"amount":"20","user":{"username":"bob"}},
			{"amount":0,"user":{"username":"zero"}}]
	}`), "SOCIAL1")
	if err != nil {
		t.Fatal(err)
	}
	if len(topic.Reactions) != 3 || !topic.Reactions[0].Mine || !topic.Reactions[1].Mine ||
		!topic.Reactions[2].Mine || topic.Reactions[2].Emoji != "🔥" ||
		topic.Reactions[2].Count != 0 {
		t.Errorf("reactions / my_reactions 的「我」状态解析错误：%+v", topic.Reactions)
	}
	if topic.Rewards.Total != 50 || topic.Rewards.Count != 2 ||
		strings.Join(topic.Rewards.Users, ",") != "alice,bob" {
		t.Errorf("打赏解析错误：%+v", topic.Rewards)
	}

	// 没有互动字段时是零值，界面据此不占位。
	bare, err := decodeTopic([]byte(`{"short_id":"BARE","title":"无互动","content":"x"}`), "BARE")
	if err != nil {
		t.Fatal(err)
	}
	if len(bare.Reactions) != 0 || bare.Rewards.Total != 0 || bare.Rewards.Count != 0 {
		t.Errorf("无互动时应为零值：%+v / %+v", bare.Reactions, bare.Rewards)
	}
}

// 新评论按视图落位：楼中楼进父评论的子树，顶层与扁平时间序接在已加载
// 评论之后，不能插到列表最顶端。
func TestInsertReplyPlacement(t *testing.T) {
	base := []replyItem{
		{ID: "p1", Pinned: true},
		{ID: "c1", Floor: 1},
		{ID: "c1a", Floor: 2, Depth: 1},
		{ID: "c1b", Floor: 3, Depth: 2},
		{ID: "c2", Floor: 4},
	}
	nested, at := insertReply(append([]replyItem(nil), base...), replyItem{ID: "new"}, "c1", false)
	want := []string{"p1", "c1", "c1a", "c1b", "new", "c2"}
	if at != 4 || len(nested) != len(want) {
		t.Fatalf("楼中楼落位 %d / %d：%+v", at, len(nested), nested)
	}
	for i, id := range want {
		if nested[i].ID != id {
			t.Errorf("楼中楼顺序 [%d] = %s，期望 %s：%+v", i, nested[i].ID, id, nested)
		}
	}
	if nested[4].Depth != 1 {
		t.Errorf("楼中楼深度 = %d，期望 1", nested[4].Depth)
	}

	top, at := insertReply(append([]replyItem(nil), base...), replyItem{ID: "top"}, "", false)
	if at != len(base) || top[len(top)-1].ID != "top" {
		t.Errorf("顶层评论应接在末尾：at=%d %+v", at, top)
	}
	flat, at := insertReply(append([]replyItem(nil), base...), replyItem{ID: "f"}, "", true)
	if at != len(base) || flat[len(flat)-1].ID != "f" {
		t.Errorf("扁平时间序应接在末尾：at=%d %+v", at, flat)
	}
	orphan, at := insertReply(append([]replyItem(nil), base...), replyItem{ID: "o"}, "missing", false)
	if at != len(base) || orphan[len(orphan)-1].ID != "o" {
		t.Errorf("父评论缺失时应保留新评论：at=%d %+v", at, orphan)
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
		"/api/v1/topics/ABC12345/replies?page=2&limit=100&flat=1",
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
	// 视图偏好按账号记：切一次就落盘，换帖子打开也沿用。
	if flat, ok := a.loadReplyViewMode(); !ok || !flat {
		t.Errorf("切换后没有按账号存下视图偏好：flat=%v ok=%v", flat, ok)
	}
	if err := view.Click("关闭详情"); err != nil {
		t.Errorf("返回列表失败：%v", err)
	}
	if a.pageName != "list" || a.detail != nil {
		t.Errorf("返回后状态不对：%s %+v", a.pageName, a.detail)
	}
}

// 详情页的互动区：表态归并成一枚胶囊（只给总数与表情），点开才看到每种表情
// 各几次；打赏汇总单独一枚。帖子在正文末尾，评论在正文下方，两边的数值不会
// 互相顶掉。
func TestDetailShowsReactionsAndRewards(t *testing.T) {
	a := testApp(t)
	a.openDetail(Item{ShortID: "JBYGf2A", URL: "https://2libra.com/post/work/JBYGf2A"})
	topic, err := decodeTopic([]byte(detailTopicJSON), "JBYGf2A")
	if err != nil {
		t.Fatal(err)
	}
	a.detail.topic = &topic
	pg := decodeReplies([]byte(detailRepliesJSON))
	a.detail.replyList = flattenReplies(pg)
	a.detail.replyTotal = pg.Total
	a.detail.replyLoaded = true

	tt := ui.NewTester(a.view, 1100, 760)
	left, ok := tt.Find("300") // 帖子打赏合计
	if !ok {
		t.Fatalf("帖子打赏状态没有显示：%q", tt.Texts())
	}
	right, ok := tt.Find("100") // 评论打赏
	if !ok {
		t.Fatalf("评论打赏状态没有显示：%q", tt.Texts())
	}
	if left.X >= right.X {
		t.Errorf("帖子打赏应位于左栏、评论打赏位于右栏：%+v / %+v", left, right)
	}
	// 表情摘要默认是「叠放的表情 + 总次数」：帖子 8 + 6 = 14，评论 2；
	// 具体每个表情几次在悬停说明与点开的明细里。右侧是两个图标入口。
	for _, want := range []string{"👍", "❤️", "💰", "表情，共 14 次", "表情，共 2 次", "表情", "打赏"} {
		if tt.HasText(want) {
			continue
		}
		if _, ok := tt.Find(want); !ok {
			t.Errorf("详情页缺少 %q：%q", want, tt.Texts())
		}
	}

	// 点摘要才看到每种表情各自几次。
	if err := tt.Click("表情，共 14 次"); err != nil {
		t.Fatalf("点开表情明细失败：%v", err)
	}
	tt.Frame()
	for _, want := range []string{"表情明细", "👍 8", "❤️ 6", "我"} {
		if !tt.HasText(want) {
			t.Errorf("表情明细缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 表态与打赏固定在正文底部：正文滚走时，表态 / 打赏汇总与两个入口原地不动，
// 长帖滚到哪儿都能直接表态或打赏。
func TestTopicInteractionBarStaysPinned(t *testing.T) {
	a := detailAppWithReplies(t)
	var body strings.Builder
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&body, "第 %d 段正文。\n\n", i)
	}
	a.detail.topic.Content = body.String()
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	before, ok := tt.Find("表情，共 14 次")
	if !ok {
		t.Fatalf("正文底部没有表态汇总：%q", tt.Texts())
	}
	if before.Y < 520 {
		t.Errorf("表态汇总不在正文底部：%+v", before)
	}
	if _, ok := tt.Find("300"); !ok {
		t.Fatalf("正文底部没有打赏汇总：%q", tt.Texts())
	}

	// 把正文滚下去：内容动了，底部的互动栏不动。
	tt.Scroll(300, 300, 0, 800)
	tt.Frame()
	if a.detail.bodyScroll.Y <= 0 {
		t.Fatalf("正文没有滚动：%+v", a.detail.bodyScroll)
	}
	after, ok := tt.Find("表情，共 14 次")
	if !ok {
		t.Fatalf("滚动正文后表态汇总消失了：%q", tt.Texts())
	}
	if dy := after.Y - before.Y; dy > 1 || dy < -1 {
		t.Errorf("滚动正文时表态汇总跟着移动了：Y %.1f -> %.1f", before.Y, after.Y)
	}
}

// 详情页是左右两张卡片：左边标题 + 正文，右边评论；评论卡片用和页面
// 不同的底色分出来，输入框固定在评论卡片底部。
func TestDetailSplitViewLayout(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1100, 760)

	title, ok := tt.Find("正文与评论的测试帖")
	if !ok {
		t.Fatalf("找不到帖子标题：%q", tt.Texts())
	}
	comment, ok := tt.Find("评论")
	if !ok {
		t.Fatalf("找不到评论卡片标题：%q", tt.Texts())
	}
	if title.X >= comment.X {
		t.Errorf("标题应在左栏、评论在右栏：标题 %+v / 评论 %+v", title, comment)
	}
	// 标题用大字号加粗：24px 的行高明显高于正文的 13px 评论。
	if title.H < 28 {
		t.Errorf("帖子标题字号不够大：%+v", title)
	}
	// 标题只出现一次，正文里不再重复一遍。
	seen := 0
	for _, s := range tt.Texts() {
		if s == "正文与评论的测试帖" {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("帖子标题出现了 %d 次，应只在正文顶部出现一次：%q", seen, tt.Texts())
	}

	// 评论列表在右栏，输入框固定在列表下方的卡片底部。
	first, ok := tt.Find("1 楼")
	if !ok {
		t.Fatalf("找不到第一条评论：%q", tt.Texts())
	}
	editor, ok := tt.Find("写评论")
	if !ok {
		t.Fatalf("找不到评论输入框：%q", tt.Texts())
	}
	if editor.X < comment.X-20 {
		t.Errorf("输入框应属于右栏：输入框 %+v / 评论 %+v", editor, comment)
	}
	if first.Y >= editor.Y {
		t.Errorf("输入框应固定在评论列表下方：第一条评论 %+v / 输入框 %+v", first, editor)
	}
	if bottom := editor.Y + editor.H; bottom > 760 {
		t.Errorf("输入框被顶出窗口：底边 %.1f", bottom)
	}

	// 右侧卡片的底色必须和页面底色不同，双栏边界才看得清。
	im := tt.Image()
	page := im.RGBAAt(4, int(comment.Y+comment.H/2))
	panel := im.RGBAAt(int(comment.X)-6, int(comment.Y+comment.H/2))
	if page == panel {
		t.Errorf("评论卡片没有独立底色：页面 %v / 卡片 %v", page, panel)
	}
}

// 作者信息收进正文、放在标题下面：小头像、昵称与等级在标题与副标题之间，
// 帖子编号让位；分类和标签收在作者下方的副标题里（分类显示两级，第二行是
// 标签，并在分类行给出浏览数）。详情页不再显示关注按钮：关注入口只在用户
// 资料卡里。
func TestDetailAuthorSitsBelowTitle(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1100, 760)

	if tt.HasText("帖子 JBYGf2A") {
		t.Errorf("正文已有内容时不应再显示帖子编号：%q", tt.Texts())
	}
	if _, ok := tt.Find("关注"); ok {
		t.Errorf("详情页不应再有作者关注按钮：%q", tt.Texts())
	}
	author, ok := tt.Find("jimmy")
	if !ok {
		t.Fatalf("正文里没有发帖人昵称：%q", tt.Texts())
	}
	title, ok := tt.Find("正文与评论的测试帖")
	if !ok {
		t.Fatalf("找不到正文标题：%q", tt.Texts())
	}
	if author.Y <= title.Y {
		t.Errorf("发帖人信息应在标题下方，实际昵称 %+v / 标题 %+v", author, title)
	}
	if r, ok := tt.Find("Lv.4"); !ok || r.Y <= title.Y {
		t.Errorf("作者等级应在标题下方：%+v ok=%v / 标题 %+v", r, ok, title)
	}
	// 分类（两级）与标签在作者行下面。
	for _, want := range []string{"生活 · 职场", "340 次浏览", "#职场  #讨论"} {
		r, ok := tt.Find(want)
		if !ok {
			t.Errorf("作者下方缺少 %q：%q", want, tt.Texts())
			continue
		}
		if r.Y <= author.Y {
			t.Errorf("%q 应在作者行下方：%+v / 作者 %+v", want, r, author)
		}
	}
}

// 楼层号吸在评论头部行的最右侧，作者、徽章与时间留在左边。
func TestReplyFloorSitsAtRowRight(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1100, 760)

	floor, ok := tt.Find("1 楼")
	if !ok {
		t.Fatalf("找不到楼层号：%q", tt.Texts())
	}
	author, ok := tt.Find("dannyng")
	if !ok {
		t.Fatalf("找不到楼层作者：%q", tt.Texts())
	}
	if floor.X <= author.X {
		t.Errorf("楼层号应在作者右侧：楼层 %+v / 作者 %+v", floor, author)
	}
	// 评论卡片右边缘在窗口右侧 14px 处，列表与评论行各留 10px 内边距；
	// 楼层号贴着这一行右端，不会停在中间。
	if right := floor.X + floor.W; right < 1020 {
		t.Errorf("楼层号没有吸在评论行右端：右边缘 %.1f", right)
	}
}

// 楼中楼不占楼层：服务端给楼中楼的 floor 是 null，界面不能拿 flat_floor
// 当楼层号；父评论本身也是楼中楼时，引用改用作者名而不是「回复 0 楼」。
func TestNestedReplyIsNotCountedAsFloor(t *testing.T) {
	a := detailAppWithReplies(t)
	a.detail.replyList = []replyItem{
		{ID: "c1", Author: "dannyng", Floor: 1, Content: "第一楼"},
		{ID: "c2", Author: "alice", Floor: 2, Content: "第二楼"},
		{ID: "c2a", Author: "bob", FlatFloor: 3, ParentID: "c2", ParentFloor: 2,
			ParentAuthor: "alice", ParentSummary: "第二楼", Content: "楼中楼"},
		{ID: "c2a1", Author: "carol", FlatFloor: 4, ParentID: "c2a",
			ParentAuthor: "bob", ParentSummary: "楼中楼", Content: "回复楼中楼"},
	}
	a.detail.replyTotal = len(a.detail.replyList)

	tt := ui.NewTester(a.view, 1100, 760)
	for _, want := range []string{"1 楼", "2 楼", "回复 2 楼：第二楼", "回复 bob：楼中楼"} {
		if !tt.HasText(want) {
			t.Errorf("缺少 %q：%q", want, tt.Texts())
		}
	}
	for _, unwanted := range []string{"3 楼", "4 楼", "回复 0 楼"} {
		if tt.HasText(unwanted) {
			t.Errorf("楼中楼不应显示 %q：%q", unwanted, tt.Texts())
		}
	}
}

// 楼中楼上方的「回复 N 楼 / 回复某人」是正文的一部分，必须和同一行的评论
// 正文左对齐。父评论已经在列表里时这条引用是可点的按钮，而 ButtonBase 默认
// 居中；只把按钮拉宽不会把文字带到行首，多级回复会明显缩进去一截。
func TestNestedReplyParentLabelLeftAligned(t *testing.T) {
	a := detailAppWithReplies(t)
	a.detail.replyList = []replyItem{
		{ID: "c2", Author: "alice", Floor: 2, Content: "第二楼"},
		{ID: "c2a", Author: "bob", FlatFloor: 3, ParentID: "c2", ParentFloor: 2,
			ParentAuthor: "alice", ParentSummary: "第二楼", Content: "楼中楼"},
		{ID: "c2a1", Author: "carol", FlatFloor: 4, ParentID: "c2a",
			ParentAuthor: "bob", ParentSummary: "楼中楼", Content: "回复楼中楼"},
	}
	a.detail.replyTotal = len(a.detail.replyList)

	tt := ui.NewTester(a.view, 1100, 760)
	cases := []struct{ label, content string }{
		{"回复 2 楼：第二楼", "楼中楼"},
		{"回复 bob：楼中楼", "回复楼中楼"},
	}
	for _, c := range cases {
		label, ok := tt.Find(c.label)
		if !ok {
			t.Fatalf("找不到被回复文案 %q：%q", c.label, tt.Texts())
		}
		content, ok := tt.Find(c.content)
		if !ok {
			t.Fatalf("找不到评论正文 %q：%q", c.content, tt.Texts())
		}
		if dy := label.Y - content.Y; dy > 30 || dy < -30 {
			t.Errorf("%q 应在 %q 上方：label=%+v content=%+v", c.label, c.content, label, content)
		}
		if label.X > content.X+0.5 || label.X < content.X-0.5 {
			t.Errorf("%q 没有和正文左对齐：label.X=%.1f content.X=%.1f",
				c.label, label.X, content.X)
		}
	}
}

// 回复楼中楼时，输入框上方的提示用作者名：目标没有楼层，不能把
// flat_floor 当成「正在回复 N 楼」。
func TestReplyComposerNestedTargetUsesAuthor(t *testing.T) {
	a := detailAppWithReplies(t)
	a.detail.replyList = []replyItem{{
		ID: "c1a", Author: "alice", FlatFloor: 2, ParentID: "c1", ParentFloor: 1,
		ParentAuthor: "dannyng", ParentSummary: "第一楼", Content: "楼中楼",
	}}
	a.detail.replyTotal = 1

	tt := ui.NewTester(a.view, 1100, 760)
	if err := tt.Click("回复"); err != nil {
		t.Fatalf("点回复失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("正在回复 alice") {
		t.Errorf("回复楼中楼应显示作者名：%q", tt.Texts())
	}
	if tt.HasText("正在回复 2 楼") {
		t.Errorf("楼中楼没有楼层，不能提示正在回复 2 楼：%q", tt.Texts())
	}
}

// 评论输入框展开后，点它自己的工具条、视图切换与两个面板入口都不能把它
// 折回一行：这些控件一按下，文本框就失焦，只看焦点决定展开与否的话，
// 输入框会连同刚打开的面板一起缩回去，评论列表重新占满、页面往下一沉。
func TestReplyComposerStaysOpenOnInnerClick(t *testing.T) {
	a := detailAppWithReplies(t)
	a.emojis = decodeEmojis([]byte(detailEmojisJSON))
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	if err := tt.Click("写评论"); err != nil {
		t.Fatalf("点输入框失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("插入表情") {
		t.Fatalf("聚焦后输入框没有展开：%q", tt.Texts())
	}

	// 工具条插入标记：焦点留在输入框里，输入框保持展开。
	if err := tt.Click("加粗"); err != nil {
		t.Fatalf("点工具条按钮失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("插入表情") || !tt.HasText("加粗") {
		t.Errorf("点工具条后输入框缩回了：%q", tt.Texts())
	}
	if strings.TrimSpace(a.detail.draft) == "" {
		t.Errorf("工具条按钮没有插入标记：%q", a.detail.draft)
	}

	// 表情面板：打开后连面板一起留在页面上，挑一个表情也不会收回去。
	if err := tt.Click("插入表情"); err != nil {
		t.Fatalf("点插入表情失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !a.detail.emojiOpen || !tt.HasText("搜索表情") {
		t.Fatalf("表情面板没有打开：%q", tt.Texts())
	}
	if !tt.HasText("插入表情") {
		t.Errorf("打开表情面板后输入框缩回了：%q", tt.Texts())
	}
	if err := tt.Click("叹气"); err != nil {
		t.Fatalf("点表情失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !strings.Contains(a.detail.draft, ":sigh:") {
		t.Errorf("点表情没有插入短码：%q", a.detail.draft)
	}
	if !tt.HasText("插入表情") {
		t.Errorf("插入表情后输入框缩回了：%q", tt.Texts())
	}

	// 视图切换同理：切到预览，输入框与预览都还在。
	if err := tt.Click("预览"); err != nil {
		t.Fatalf("点视图切换失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if a.detail.preview != 1 || !tt.HasText("评论视图") {
		t.Errorf("视图切换后输入框缩回了：preview=%d %q", a.detail.preview, tt.Texts())
	}

	// 收起面板、切回编辑、清空草稿后，点评论列表里的元素仍然把输入框折回
	// 一行，把空间还给列表。
	a.detail.emojiOpen = false
	a.detail.preview = 0
	a.detail.draft = ""
	tt.Frame()
	if err := tt.Click("第一楼 :feel_wronged:"); err != nil {
		t.Fatalf("点评论正文失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if tt.HasText("插入表情") {
		t.Errorf("点评论列表后输入框应折回一行：%q", tt.Texts())
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

// 线上 201 有时只回 id / 楼层，不回正文。刚发的评论必须用这次提交的内容
// 补上，否则它在列表里显示「（无内容）」，刷新后才看得到正文。
func TestCreateReplyFallsBackToSubmittedContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"r9","floor":7,"author":{"username":"jimmy"}}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}

	reply, err := c.createReply(context.Background(), "JBYGf2A", "刚发的评论", "")
	if err != nil {
		t.Fatalf("发评论失败：%v", err)
	}
	if reply.Content != "刚发的评论" {
		t.Errorf("服务端没回正文时应补上提交内容：%+v", reply)
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
	// 回复入口常驻在评论底部那一行的右侧，不再依赖悬浮；悬浮前后都在。
	// 注意：嵌套评论的「回复 N 楼」父引用始终可见，故用 Find 精确匹配按钮文字。
	if _, ok := tt.Find("回复"); !ok {
		t.Fatal("评论底部应常驻回复入口")
	}
	if r, ok := tt.Find("1 楼"); !ok {
		t.Fatal("找不到评论行用于悬浮")
	} else {
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
	}
	if _, ok := tt.Find("回复"); !ok {
		t.Error("悬浮评论行后回复入口不应消失")
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

// 评论底部固定一行：最左边是表态图标，中间是这条评论拿到的表态 / 打赏
// 汇总，最右边是打赏图标与回复图标；不悬浮也能看到全部入口。
func TestReplyFooterShowsSummaryAndReply(t *testing.T) {
	a := detailAppWithReplies(t)
	// 只留一条评论：避免置顶楼层与嵌套回复的「回复」按钮干扰定位。
	a.detail.replyList = []replyItem{{
		ID: "c1", Author: "dannyng", Floor: 1, Content: "第一楼",
		Reactions: []reactionStat{{Emoji: "👍", Count: 2}},
		Rewards:   rewardStat{Total: 100, Count: 1},
	}}
	a.detail.replyTotal = 1
	tt := ui.NewTester(a.view, 1100, 760)

	// 第一条评论（c1）带 👍 2 次与 100 金币打赏。
	summary, ok := tt.Find("表情，共 2 次")
	if !ok {
		t.Fatalf("评论底部没有展示表态汇总：%q", tt.Texts())
	}
	if !tt.HasText("100") {
		t.Errorf("评论底部没有展示打赏汇总：%q", tt.Texts())
	}
	// 「回复」常驻在评论底部，不依赖悬浮。
	reply, ok := tt.Find("回复")
	if !ok {
		t.Fatalf("评论底部没有回复入口：%q", tt.Texts())
	}
	if summary.X >= reply.X {
		t.Errorf("表态汇总应在左侧、回复在右侧：summary=%+v reply=%+v", summary, reply)
	}
	if dy := summary.Y - reply.Y; dy > 10 || dy < -10 {
		t.Errorf("汇总与回复应在同一行：Δy=%v", dy)
	}
}

// 评论底部的回复 / 表态 / 打赏那一行常驻，悬浮评论行不会改变整条评论的
// 高度：「回复」不再随悬浮出现，因此不可能发生上下跳动。
func TestReplyButtonNoLayoutShift(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1100, 760)

	// 未悬浮时「回复」已经在评论底部。
	if _, ok := tt.Find("回复"); !ok {
		t.Fatal("评论底部应常驻回复入口")
	}
	base, ok := tt.Find("楼中楼")
	if !ok {
		t.Fatal("找不到第二条评论用于对照")
	}
	yBase := base.Y

	row1, ok := tt.Find("1 楼")
	if !ok {
		t.Fatal("找不到第一条评论用于悬浮")
	}
	tt.Move(row1.X+row1.W/2, row1.Y+row1.H/2)
	tt.Frame()

	after, ok := tt.Find("楼中楼")
	if !ok {
		t.Fatal("悬浮后第二条评论消失")
	}
	dy := after.Y - yBase
	if dy > 1 || dy < -1 {
		t.Errorf("悬浮评论行后整条评论高度变化，发生跳动：Y %.1f -> %.1f (Δ%v)", yBase, after.Y, dy)
	}
}

// 点「回复 N 楼：…」跳到父评论：长列表滚到底后点最后一条评论里的父楼
// 引用，列表回到顶部并把高亮移到父评论上。
func TestParentJumpScrollsToParent(t *testing.T) {
	a := detailAppWithReplies(t)
	rows := make([]replyItem, 0, 60)
	for i := 0; i < 60; i++ {
		rows = append(rows, replyItem{
			ID: fmt.Sprintf("c%d", i), Author: "u", Floor: i + 1,
			Content: fmt.Sprintf("第%d条评论", i+1),
		})
	}
	rows[59].ParentID = "c0"
	rows[59].ParentFloor = 1
	rows[59].ParentSummary = "第一条评论"
	a.detail.replyList = rows
	a.detail.replyTotal = len(rows)
	a.detail.replyLoaded = true
	a.detail.list.ScrollTo(59, ui.End)

	tt := ui.NewTester(a.view, 1100, 760)
	if !tt.HasText("第60条评论") {
		t.Fatalf("滚动到底部后看不到最后一条评论：%q", tt.Texts())
	}
	if tt.HasText("第5条评论") {
		t.Fatalf("开始时列表已经显示顶部评论，无法验证跳转：%q", tt.Texts())
	}
	if err := tt.Click("回复 1 楼：第一条评论"); err != nil {
		t.Fatalf("点击父楼引用失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("第5条评论") {
		t.Errorf("点击父楼引用后没有滚到父评论：%q", tt.Texts())
	}
	if a.detail.focusID != "c0" {
		t.Errorf("父评论没有被高亮：focusID=%q", a.detail.focusID)
	}
}

// 评论永久链接按楼层/扁平视图和分页位置拼：楼层视图 /post/ 带 p，
// 嵌套回复 /post-flat/ 按 flat_floor 算 p。
func TestReplyPermalink(t *testing.T) {
	topic := &topicDetail{URL: "https://2libra.com/post/work/X"}
	cases := []struct {
		name string
		r    replyItem
		want string
	}{
		{"第一页", replyItem{ID: "c1", Floor: 1},
			"https://2libra.com/post/work/X?commentId=c1"},
		{"第二页", replyItem{ID: "c2", Floor: 31},
			"https://2libra.com/post/work/X?commentId=c2&p=2"},
		{"嵌套回复", replyItem{ID: "c3", FlatFloor: 61, ParentID: "c1"},
			"https://2libra.com/post-flat/work/X?commentId=c3&p=3"},
		{"没有 id", replyItem{Floor: 1}, ""},
	}
	for _, c := range cases {
		if got := replyPermalink(topic, "", c.r); got != c.want {
			t.Errorf("%s：链接 = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// 点楼层号在系统浏览器里打开这条评论。
func TestFloorLabelOpensComment(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1100, 760)
	if err := tt.Click("1 楼"); err != nil {
		t.Fatalf("点击楼层号失败：%v（%q）", err, tt.Texts())
	}
	got := tt.OpenedURLs()
	want := "https://2libra.com/post/work/a1b2c3d4?commentId=c1"
	if len(got) != 1 || got[0] != want {
		t.Errorf("楼层号打开的链接 = %v，期望 [%s]", got, want)
	}
}

// 从评论列表点进来时，目标评论常常不在第 1 页：首屏没找到要自动续拉
// 后面的页，直到找到目标并定位，而不是只显示第 1 页就说「来自评论定位」。
func TestCommentFocusLoadsLaterPages(t *testing.T) {
	var mu sync.Mutex
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()
		if page == "2" {
			_, _ = fmt.Fprint(w, `{"c":0,"m":"请求成功","d":{"items":[
				{"id":"goal","content":"目标评论","floor":140,"author":{"username":"late"}}],
				"total":140,"page":2,"limit":100,"total_pages":2}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"c":0,"m":"请求成功","d":{"items":[
			{"id":"c1","content":"第一页评论","floor":1,"author":{"username":"first"}}],
			"total":140,"page":1,"limit":100,"total_pages":2}}`)
	}))
	defer srv.Close()

	a := testApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.detail = &detailState{
		shortID: "X", focusID: "goal", focusPending: true, flat: true, replyPage: 1,
	}
	a.loadReplies(1)

	deadline := time.Now().Add(2 * time.Second)
	for replyIndexByID(a.detail.replyList, "goal") < 0 {
		if time.Now().After(deadline) {
			t.Fatalf("没有自动续拉到目标评论：%+v", a.detail.replyList)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if a.detail.focusPending || a.detail.focusMiss {
		t.Errorf("找到目标后定位状态不对：pending=%v miss=%v", a.detail.focusPending, a.detail.focusMiss)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pages) != 2 || pages[0] != "1" || pages[1] != "2" {
		t.Errorf("续拉页码不对：%v", pages)
	}
}

// 目标评论不在这篇帖子里（或已删除）时，续拉完最后一页就停下并提示，
// 不能一直自动翻页。
func TestCommentFocusMissingStops(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		_, _ = fmt.Fprint(w, `{"c":0,"m":"请求成功","d":{"items":[
			{"id":"c1","content":"第一页评论","floor":1,"author":{"username":"first"}}],
			"total":1,"page":1,"limit":100,"total_pages":1}}`)
	}))
	defer srv.Close()

	a := testApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	a.detail = &detailState{
		shortID: "X", focusID: "gone", focusPending: true, flat: true, replyPage: 1,
	}
	a.loadReplies(1)

	deadline := time.Now().Add(2 * time.Second)
	for a.detail.focusPending {
		if time.Now().After(deadline) {
			t.Fatal("目标不存在时没有停止定位")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !a.detail.focusMiss {
		t.Error("目标不存在时应标记未找到")
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Errorf("不该继续请求后面的页：%d 次", requests)
	}
}

// Esc 与右上角关闭同义：没有内联层级时回到列表。
func TestEscapeClosesDetail(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.pageName != "list" || a.detail != nil {
		t.Errorf("Esc 没有关闭详情：page=%s detail=%v", a.pageName, a.detail)
	}
}

// 内联浏览时 Esc 先退一层，与关闭按钮的返回栈行为一致。
func TestEscapePopsInlineDetail(t *testing.T) {
	a := detailAppWithReplies(t)
	a.openDetail(Item{ShortID: "SECOND", URL: "https://2libra.com/post/work/SECOND"})
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.pageName != "detail" || a.detail == nil || a.detail.shortID != "JBYGf2A" {
		t.Errorf("Esc 没有退回上一层：page=%s detail=%+v", a.pageName, a.detail)
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
			"total":105,"page":2,"limit":100,"total_pages":2}}`)
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
	// 点击不会触发，这里绕过按钮验证守卫逻辑本身）。校验不通过只在窗口底部
	// 弹一条 toast，页面上不留一行红字。
	a.detail.draft = ""
	a.submitReply()
	tt.Frame()
	if !tt.HasText("评论内容不能为空") {
		t.Errorf("空内容应被校验拦截并提示：%q", tt.Texts())
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
	// 评论会扣金币：成功后必须发出全局金币刷新事件。
	events := make(chan coinEvent, 1)
	a.onCoinsChanged = func(event coinEvent) { events <- event }
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
	select {
	case event := <-events:
		if event != coinEventReply {
			t.Errorf("评论事件 = %q，期望 %q", event, coinEventReply)
		}
	default:
		t.Error("评论成功后没有发出金币刷新事件")
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
	// 顶层评论应接在已加载列表末尾（而不是插到置顶和第 1 楼前面）。
	if last := a.detail.replyList[len(a.detail.replyList)-1]; last.ID != "new1" {
		t.Errorf("新评论没有接到列表末尾：%+v", a.detail.replyList)
	}
}

// 服务端创建评论后只回 id / 楼层、不回正文时，新评论也要立刻带着提交的
// 正文显示出来，不能等到刷新。这是「发完显示（无内容）」的回归测试。
func TestReplyComposerShowsSubmittedContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"new1","floor":99,"author":{"username":"jimmy"}}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.client.Base = srv.URL + "/api"
	a.openDetail(Item{ShortID: "JBYGf2A", URL: "https://2libra.com/post/work/JBYGf2A"})
	topic, _ := decodeTopic([]byte(detailTopicJSON), "JBYGf2A")
	a.detail.topic = &topic
	pg := decodeReplies([]byte(detailRepliesJSON))
	a.detail.replyList = flattenReplies(pg)
	a.detail.replyTotal = pg.Total
	a.detail.replyLoaded = true

	a.detail.draft = "发完立刻显示"
	done := make(chan struct{})
	a.onReplyPosted = func(replyItem, error) { close(done) }
	tt := ui.NewTester(a.view, 1100, 760)
	a.submitReply()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("发送回调超时")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !tt.HasText("发完立刻显示") {
		if time.Now().After(deadline) {
			t.Fatalf("发送后没有立刻显示评论正文：%q", tt.Texts())
		}
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if tt.HasText("（无内容）") {
		t.Errorf("新评论不应显示「（无内容）」：%q", tt.Texts())
	}
}

// 回复别人的评论发出去后也不能占楼层：服务端只给 flat_floor，不能把它
// 提成 floor 显示成「9 楼」；父评论引用仍按顶层楼层显示。
func TestReplyComposerNestedReplyHasNoFloor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"new2","floor":null,"flat_floor":9,"author":{"username":"jimmy"}}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	a.client.Base = srv.URL + "/api"
	a.openDetail(Item{ShortID: "JBYGf2A", URL: "https://2libra.com/post/work/JBYGf2A"})
	topic, _ := decodeTopic([]byte(detailTopicJSON), "JBYGf2A")
	a.detail.topic = &topic
	pg := decodeReplies([]byte(detailRepliesJSON))
	a.detail.replyList = flattenReplies(pg)
	a.detail.replyTotal = pg.Total
	a.detail.replyLoaded = true

	a.detail.replyToID = "c1"
	a.detail.replyToFloor = 1
	a.detail.replyToName = "dannyng"
	a.detail.draft = "回复第一楼"
	done := make(chan struct{})
	a.onReplyPosted = func(replyItem, error) { close(done) }
	tt := ui.NewTester(a.view, 1100, 760)
	a.submitReply()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("发送回调超时")
	}
	at := replyIndexByID(a.detail.replyList, "new2")
	if at < 0 {
		t.Fatalf("新回复没有插入列表：%+v", a.detail.replyList)
	}
	last := a.detail.replyList[at]
	if last.Floor != 0 || last.FlatFloor != 9 {
		t.Errorf("楼中楼不应被提成楼层：%+v", last)
	}
	if last.ParentID != "c1" || last.ParentFloor != 1 || last.ParentAuthor != "dannyng" {
		t.Errorf("父评论引用没有补齐：%+v", last)
	}
	if a.detail.replyToName != "" {
		t.Errorf("发送后应清空回复目标：%q", a.detail.replyToName)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !tt.HasText("回复第一楼") {
		if time.Now().After(deadline) {
			t.Fatalf("发送后没有显示回复内容：%q", tt.Texts())
		}
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if tt.HasText("9 楼") {
		t.Errorf("楼中楼不应显示成 9 楼：%q", tt.Texts())
	}
}
