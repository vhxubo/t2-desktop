package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// filterFixture 是一小段评论树：楼主的置顶楼层 + 楼中楼里的一楼 + 两个路人的
// 回复。Depth 故意交错，用来验过滤只改副本、退出后树形原样恢复。
func filterFixture() []replyItem {
	return []replyItem{
		{ID: "p1", Author: "楼主", AuthorID: "op", Floor: 1, Pinned: true, Depth: 0},
		{ID: "r1", Author: "路人", AuthorID: "u2", Floor: 2, Depth: 1},
		{ID: "r2", Author: "楼主", AuthorID: "op", Floor: 3, Depth: 0},
		{ID: "r3", Author: "路人", AuthorID: "u3", Floor: 4, Depth: 1},
	}
}

func filteredState(rows []replyItem) *detailState {
	return &detailState{
		replyList: rows,
		topic:     &topicDetail{Author: "楼主", AuthorID: "op"},
	}
}

func TestFilterRepliesKeepsOnlyAuthor(t *testing.T) {
	d := filteredState(filterFixture())
	setAuthorFilter(d, "楼主", "op", true)
	if len(d.display) != 2 {
		t.Fatalf("过滤后条数 = %d，期望 2：%+v", len(d.display), d.display)
	}
	if d.display[0].ID != "p1" || d.display[1].ID != "r2" {
		t.Errorf("过滤后顺序不对：%s, %s", d.display[0].ID, d.display[1].ID)
	}
	if d.display[0].Floor != 1 || d.display[1].Floor != 3 {
		t.Errorf("过滤后楼层号被改写：%d, %d", d.display[0].Floor, d.display[1].Floor)
	}
}

func TestFilterRepliesFlattensDepth(t *testing.T) {
	d := filteredState(filterFixture())
	setAuthorFilter(d, "楼主", "op", true)
	for _, r := range d.display {
		if r.Depth != 0 {
			t.Errorf("过滤后 %s 的 Depth = %d，期望 0", r.ID, r.Depth)
		}
	}
	for i, r := range d.replyList {
		if r.Depth != filterFixture()[i].Depth {
			t.Errorf("replyList[%d].Depth 被改成 %d", i, r.Depth)
		}
	}
}

func TestFilterRepliesByIdBeatsName(t *testing.T) {
	rows := []replyItem{
		{ID: "a1", Author: "alice", AuthorID: "u1"},
		{ID: "a2", Author: "alice", AuthorID: "u2"},
		{ID: "a3", Author: "alice"}, // 老响应没给 id，按昵称命中
	}
	d := filteredState(rows)
	setAuthorFilter(d, "alice", "u1", false)
	if len(d.display) != 2 || d.display[0].ID != "a1" || d.display[1].ID != "a3" {
		t.Fatalf("id 优先 / 缺 id 退回昵称判定不对：%+v", d.display)
	}
}

func TestFilterRepliesAnonymousTopic(t *testing.T) {
	d := filteredState(filterFixture())
	d.topic.Author = anonymousAuthor
	setAuthorFilter(d, "", "", true)
	if d.authorFilter != "" {
		t.Fatalf("匿名帖不该产生过滤态：%q", d.authorFilter)
	}
	// 即使外部强行塞了「匿名」，也不能把匿名回复当成楼主的。
	d.authorFilter, d.authorFilterIsOP = anonymousAuthor, true
	if d.matchesAuthorFilter(replyItem{Author: anonymousAuthor}) {
		t.Error("匿名回复被误判成楼主回复")
	}
}

func TestFilterRepliesPinned(t *testing.T) {
	rows := []replyItem{
		{ID: "pin-op", Author: "楼主", AuthorID: "op", Pinned: true},
		{ID: "pin-mod", Author: "mod", AuthorID: "u9", Pinned: true},
	}
	d := filteredState(rows)
	setAuthorFilter(d, "楼主", "op", true)
	if len(d.display) != 1 || d.display[0].ID != "pin-op" {
		t.Fatalf("置顶评论没有按作者过滤：%+v", d.display)
	}
}

func TestClearAuthorFilterRestoresTree(t *testing.T) {
	d := filteredState(filterFixture())
	d.rebuildDisplay()
	before := append([]replyItem(nil), d.display...)
	setAuthorFilter(d, "楼主", "op", true)
	clearAuthorFilter(d)
	if !reflect.DeepEqual(d.display, before) {
		t.Errorf("清空过滤后 display 与过滤前不一致：%+v / %+v", d.display, before)
	}
	if d.authorFilter != "" || d.authorFilterID != "" || d.authorFilterIsOP {
		t.Errorf("清空过滤后状态没归零：%+v", d)
	}
}

// authorFilterDetailApp 造一页有三条评论的详情页：楼主两条、路人一条。
// 用它跑无头 UI 用例，点击工具条与右键菜单都能落到同一份 detailState。
func authorFilterDetailApp(t *testing.T) (*app, *ui.Tester) {
	t.Helper()
	a := detailAppWithReplies(t)
	a.detail.topic.Author = "jimmy"
	a.detail.topic.AuthorID = "u1"
	a.detail.replyList = []replyItem{
		{ID: "o1", Author: "jimmy", AuthorID: "u1", Floor: 1, Content: "楼主补充一"},
		{ID: "x1", Author: "dannyng", AuthorID: "u2", Floor: 2, Content: "路人回复"},
		{ID: "o2", Author: "jimmy", AuthorID: "u1", Floor: 3, Content: "楼主补充二"},
	}
	a.detail.replyLoaded = true
	a.detail.rebuildDisplay()
	return a, ui.NewTester(a.view, 1100, 760)
}

func TestReplyToolbarTogglesAuthorFilter(t *testing.T) {
	a, tt := authorFilterDetailApp(t)
	tt.Frame()
	if !tt.HasText("只看楼主") {
		t.Fatalf("工具条没有「只看楼主」：%q", tt.Texts())
	}
	if info, ok := tt.Access("只看楼主"); !ok || info.Disabled {
		t.Fatal("有评论且不是匿名帖时「只看楼主」应可点")
	}
	if err := tt.Click("只看楼主"); err != nil {
		t.Fatalf("点只看楼主失败：%v", err)
	}
	tt.Frame()
	if !tt.HasText("只看楼主 · 2 条") {
		t.Errorf("过滤计数不对：%q", tt.Texts())
	}
	if tt.HasText("路人回复") || !tt.HasText("楼主补充一") || !tt.HasText("楼主补充二") {
		t.Errorf("过滤后只剩楼主两条才对：%q", tt.Texts())
	}
	if len(a.detail.display) != 2 {
		t.Fatalf("display 条数 = %d，期望 2", len(a.detail.display))
	}
	if !tt.HasText("取消只看楼主") {
		t.Errorf("过滤态按钮文案不对：%q", tt.Texts())
	}
	if err := tt.Click("取消只看楼主"); err != nil {
		t.Fatalf("点取消过滤失败：%v", err)
	}
	tt.Frame()
	if a.detail.authorFilter != "" || len(a.detail.display) != 3 {
		t.Fatalf("取消过滤后没恢复全量：%q", tt.Texts())
	}
	if !tt.HasText("路人回复") || tt.HasText("只看楼主 · 2 条") {
		t.Errorf("取消过滤后界面不对：%q", tt.Texts())
	}
}

// 匿名帖没有「楼主」可看：入口禁用，且不会因为点了而进入过滤态。
func TestReplyToolbarFilterDisabledOnAnonymousTopic(t *testing.T) {
	a, tt := authorFilterDetailApp(t)
	a.detail.topic.Author = anonymousAuthor
	tt.Frame()
	if info, ok := tt.Access("只看楼主"); !ok || !info.Disabled {
		t.Fatalf("匿名帖的「只看楼主」应禁用：%+v %v", info, ok)
	}
	if a.detail.authorFilter != "" {
		t.Errorf("匿名帖不该有过滤态：%q", a.detail.authorFilter)
	}
}

func TestAvatarMenuFiltersByAuthor(t *testing.T) {
	a, tt := authorFilterDetailApp(t)
	tt.Frame()
	if err := tt.RightClick("dannyng"); err != nil {
		t.Fatalf("右键评论头像失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText(detailAvatarFilterLabel) {
		t.Fatalf("头像菜单缺少「只看 ta 的回复」：%q", tt.Texts())
	}
	if err := tt.Click(detailAvatarFilterLabel); err != nil {
		t.Fatalf("点头像菜单过滤失败：%v", err)
	}
	tt.Frame()
	if a.detail.authorFilter != "dannyng" || a.detail.authorFilterID != "u2" {
		t.Fatalf("没有按头像作者过滤：%+v", a.detail)
	}
	if a.detail.authorFilterIsOP || len(a.detail.display) != 1 {
		t.Fatalf("过滤结果不对：%+v", a.detail.display)
	}
	if tt.HasText("楼主补充一") || !tt.HasText("路人回复") {
		t.Errorf("按作者过滤后界面不对：%q", tt.Texts())
	}
}

// 自己是评论作者时，「只看 ta 的回复」不可点：看自己没有意义。
func TestAvatarMenuFilterDisabledForSelf(t *testing.T) {
	a, tt := authorFilterDetailApp(t)
	m := newAuthManager(t.TempDir())
	_ = m.storeToken(tokenData{AccessToken: "tok", Username: "dannyng"})
	a.auth = m
	tt.Frame()
	// 标题栏也显示账号名，按文本右击会先命中那里；这里按评论正文定位到
	// 它的头部（头像与昵称在正文上方左对齐），右键那块身份区域。
	content, ok := tt.Find("路人回复")
	if !ok {
		t.Fatalf("找不到评论正文：%q", tt.Texts())
	}
	tt.RightClickAt(content.X+10, content.Y-20)
	tt.Frame()
	if _, ok := tt.Find(detailAvatarFilterLabel); !ok {
		t.Fatalf("头像菜单缺少「只看 ta 的回复」：%q", tt.Texts())
	}
	// 菜单项禁用时点击不会提交；过滤态应保持为空。
	if err := tt.Click(detailAvatarFilterLabel); err != nil {
		t.Fatalf("点禁用的菜单项失败：%v", err)
	}
	tt.Frame()
	if a.detail.authorFilter != "" {
		t.Errorf("自己的「只看 ta 的回复」不该可点：%q", a.detail.authorFilter)
	}
}

func TestFilterEmptyStateAndViewAll(t *testing.T) {
	a, tt := authorFilterDetailApp(t)
	// 楼主在已加载范围内没有回复。
	for i := range a.detail.replyList {
		a.detail.replyList[i].Author = "dannyng"
		a.detail.replyList[i].AuthorID = "u2"
	}
	setAuthorFilter(a.detail, "jimmy", "u1", true)
	tt.Frame()
	if !tt.HasText("楼主还没有回复") || !tt.HasText("查看全部") {
		t.Fatalf("过滤空态缺文案或入口：%q", tt.Texts())
	}
	if err := tt.Click("查看全部"); err != nil {
		t.Fatalf("点查看全部失败：%v", err)
	}
	tt.Frame()
	if a.detail.authorFilter != "" || len(a.detail.display) != 3 {
		t.Fatalf("查看全部后没恢复全量：%q", tt.Texts())
	}
}

// 从通知定位到某条评论时过滤要让位，否则目标行会被藏起来。
func TestNotificationFocusClearsFilter(t *testing.T) {
	a, tt := authorFilterDetailApp(t)
	setAuthorFilter(a.detail, "jimmy", "u1", true)
	a.openNotificationTarget("https://2libra.com/post/work/JBYGf2A?commentId=x1", "n1")
	tt.Frame()
	if a.detail == nil || a.detail.authorFilter != "" {
		t.Fatalf("通知定位后过滤没清空：%+v", a.detail)
	}
	if a.detail.focusID != "x1" {
		t.Errorf("通知定位丢了目标评论：%q", a.detail.focusID)
	}
}

// 过滤是这篇帖子的临时视角：视图切换与刷新都保留，计数按新数据更新。
func TestFilterSurvivesViewSwitchAndRefresh(t *testing.T) {
	a, tt := authorFilterDetailApp(t)
	setAuthorFilter(a.detail, "jimmy", "u1", true)
	tt.Frame()

	for _, label := range []string{"切到扁平时间序", "切到楼层视图"} {
		if err := tt.Click(label); err != nil {
			t.Fatalf("点 %q 失败：%v", label, err)
		}
		tt.Frame()
		if a.detail.authorFilter != "jimmy" {
			t.Fatalf("切换视图后过滤丢了：%q", a.detail.authorFilter)
		}
	}
	if err := tt.Click("刷新评论"); err != nil {
		t.Fatalf("点刷新评论失败：%v", err)
	}
	tt.Frame()
	if a.detail.authorFilter != "jimmy" {
		t.Fatalf("刷新后过滤丢了：%q", a.detail.authorFilter)
	}
	if a.detail.filterAutoPages != 0 {
		t.Errorf("刷新后续拉预算没重置：%d", a.detail.filterAutoPages)
	}
}

// 假服务端第 1 页没有命中，第 2 页才有楼主回复：过滤打开后应自动续拉并显示它。
func TestFilterAutoLoadsLaterPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = fmt.Fprint(w, `{"c":0,"m":"请求成功","d":{"items":[
				{"id":"goal","content":"楼主补了一楼","floor":120,
				 "author":{"id":"u1","username":"jimmy"}}],
				"total":120,"page":2,"limit":100,"total_pages":2}}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"c":0,"m":"请求成功","d":{"items":[
			{"id":"x1","content":"路人回复","floor":1,
			 "author":{"id":"u2","username":"dannyng"}}],
			"total":120,"page":1,"limit":100,"total_pages":2}}`)
	}))
	defer srv.Close()

	a, tt := authorFilterDetailApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	setAuthorFilter(a.detail, "jimmy", "u1", true)
	a.loadReplies(1)

	deadline := time.Now().Add(3 * time.Second)
	for replyIndexByID(a.detail.display, "goal") < 0 {
		if time.Now().After(deadline) {
			t.Fatalf("过滤态没有自动续拉到第 2 页：%+v", a.detail.display)
		}
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	tt.Frame()
	if !tt.HasText("楼主补了一楼") {
		t.Errorf("命中行没画出来：%q", tt.Texts())
	}
}

// 连续 10 页都没有命中时停下，底部给手动「继续加载」；点它再拉一页。
func TestFilterStopsAfterTenPagesAndContinuesManually(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		_, _ = fmt.Fprintf(w, `{"c":0,"m":"请求成功","d":{"items":[
			{"id":"x%d","content":"路人回复 %d","floor":%d,
			 "author":{"id":"u2","username":"dannyng"}}],
			"total":2000,"page":%d,"limit":100,"total_pages":20}}`, n, n, n, n)
	}))
	defer srv.Close()

	a, tt := authorFilterDetailApp(t)
	a.autoLoad = true
	a.client.Base = srv.URL + "/api"
	a.client.HTTP = srv.Client()
	setAuthorFilter(a.detail, "jimmy", "u1", true)
	a.loadReplies(1)

	deadline := time.Now().Add(5 * time.Second)
	for a.detail.filterAutoPages < replyFilterAutoMaxPages || a.detail.replyLoading {
		if time.Now().After(deadline) {
			t.Fatalf("没有在 10 页后停下：autoPages=%d loading=%v",
				a.detail.filterAutoPages, a.detail.replyLoading)
		}
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if got := hits.Load(); got != replyFilterAutoMaxPages+1 {
		t.Fatalf("自动续拉页数不对：%d，期望 %d", got, replyFilterAutoMaxPages+1)
	}
	tt.Frame()
	if !tt.HasText("继续加载（还有 9 页）") {
		t.Fatalf("过滤 10 页后没给手动继续入口：%q", tt.Texts())
	}
	if err := tt.Click("继续加载（还有 9 页）"); err != nil {
		t.Fatalf("点继续加载失败：%v", err)
	}
	tt.Frame()
	if a.detail.filterAutoPages >= replyFilterAutoMaxPages {
		t.Errorf("手动继续后自动续拉预算没重置：%d", a.detail.filterAutoPages)
	}
	wait := time.Now().Add(3 * time.Second)
	for hits.Load() <= replyFilterAutoMaxPages+1 && time.Now().Before(wait) {
		tt.Frame()
		time.Sleep(5 * time.Millisecond)
	}
	if got := hits.Load(); got <= replyFilterAutoMaxPages+1 {
		t.Errorf("手动继续加载没发出下一页请求：%d", got)
	}
}

// 续拉失败只显示在底部，过滤态保持，用户还能点「重试」。
func TestFilterLoadMoreFailureKeepsState(t *testing.T) {
	a, tt := authorFilterDetailApp(t)
	setAuthorFilter(a.detail, "jimmy", "u1", true)
	a.detail.replyPage = 2
	a.detail.replyTotalPages = 3
	a.detail.replyMoreErr = "服务端错误（500），请稍后重试"
	tt.Frame()
	for _, want := range []string{"加载更多失败", "重试", "只看楼主 · 2 条"} {
		if !tt.HasText(want) {
			t.Errorf("续拉失败时底部提示或计数丢失 %q：%q", want, tt.Texts())
		}
	}
	if a.detail.authorFilter != "jimmy" {
		t.Errorf("续拉失败把过滤态清掉了：%q", a.detail.authorFilter)
	}
}

// 过滤对象之外的新评论不显示，但 replyList 本体照常收下（计数由服务端给）。
func TestFilterKeepsNewCommentOutOfDisplay(t *testing.T) {
	d := filteredState(filterFixture())
	setAuthorFilter(d, "楼主", "op", true)
	d.replyList, _ = insertReply(d.replyList,
		replyItem{ID: "new", Author: "路人", AuthorID: "u4", Floor: 5}, "", false)
	d.rebuildDisplay()
	if replyIndexByID(d.display, "new") >= 0 {
		t.Error("不是过滤对象的新评论出现在 display 里")
	}
	if replyIndexByID(d.replyList, "new") < 0 {
		t.Error("新评论没有进 replyList 本体")
	}
	if strings.Join([]string{d.authorFilter, d.authorFilterID}, ":") != "楼主:op" {
		t.Errorf("插入评论改变了过滤状态：%q / %q", d.authorFilter, d.authorFilterID)
	}
}
