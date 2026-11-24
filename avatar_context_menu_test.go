package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func avatarMenuTestApp(t *testing.T) (*app, *ui.Tester) {
	t.Helper()
	a, _ := emojiDetailApp(t, "正文")
	a.detail.replyList = []replyItem{{
		ID: "c1", Author: "dannyng", AuthorID: "u-danny", Floor: 1, Content: "你好",
	}}
	return a, ui.NewTester(a.view, 1100, 760)
}

// 右键评论作者头像弹出四项菜单：@名字、分割线、关注、屏蔽。菜单项文案与
// 顺序都固定（见 detailAvatarMenu），Lobe 把分割线画成一条没有文字的线，
// 这里只断言三个有字的项。
func TestDetailAvatarMenuShowsItems(t *testing.T) {
	_, tt := avatarMenuTestApp(t)
	tt.Frame()
	if err := tt.RightClick("dannyng"); err != nil {
		t.Fatalf("右键头像失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	for _, want := range []string{"@dannyng", detailAvatarFollowLabel, detailAvatarBlockLabel} {
		if !tt.HasText(want) {
			t.Errorf("头像右键菜单缺少 %q：%q", want, tt.Texts())
		}
	}
	// Lobe 原版右键菜单写死 MinWidth(220)，短标签撑出一大片空白；窄版
	// 菜单連同内边距也就一百出头。
	r, ok := tt.Find("@dannyng")
	if !ok {
		t.Fatalf("找不到菜单项 @dannyng：%q", tt.Texts())
	}
	if r.W > 120 {
		t.Errorf("头像右键菜单还是太宽：%.1f DIP", r.W)
	}
}

// 悬浮在「头像 + 昵称」上时铺一层圆角背景（不是描边或阴影），且不挪动
// 版式：先把像素拷出来，再移指针到作者名上比一帧。
func TestDetailAvatarHoverBackground(t *testing.T) {
	_, tt := avatarMenuTestApp(t)
	tt.Frame()
	r, ok := tt.Find("dannyng")
	if !ok {
		t.Fatalf("找不到评论作者：%q", tt.Texts())
	}
	before := append([]byte(nil), tt.Image().Pix...)
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	after := tt.Image().Pix
	changed := 0
	for i := range before {
		if i < len(after) && before[i] != after[i] {
			changed++
		}
	}
	if changed == 0 {
		t.Error("头像悬浮没有任何视觉反馈（圆角背景没画出来）")
	}
	if r2, ok := tt.Find("dannyng"); !ok || r2 != r {
		t.Errorf("悬浮让版式跳动了：悬浮前 %+v，悬浮后 %+v", r, r2)
	}
}

// 头像 + 昵称这块不再用「查看 @xx 的资料」tooltip 提示可点，悬浮时只有
// 一层浮起效果；提示文字本来会遮住下面的评论。
func TestDetailAvatarHasNoProfileTooltip(t *testing.T) {
	_, tt := avatarMenuTestApp(t)
	tt.Frame()
	r, ok := tt.Find("dannyng")
	if !ok {
		t.Fatalf("找不到评论作者：%q", tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(800 * time.Millisecond)
	if _, ok := tt.AccessByRole(ui.RoleTooltip, "查看 @dannyng 的资料"); ok {
		t.Error("头像悬浮仍弹出资料卡 tooltip")
	}
}

// 已关注时菜单换成「取消关注」，点了发 DELETE 取关。
func TestDetailAvatarMenuUnfollowsWhenFollowing(t *testing.T) {
	var method atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method.Store(r.Method)
		_, _ = w.Write([]byte(envelopeJSON(`{"success":true}`)))
	}))
	defer srv.Close()

	a, tt := avatarMenuTestApp(t)
	a.client.Base = srv.URL + "/api"
	a.auth.token = &tokenData{AccessToken: "tok", Username: "me"}
	a.detail.avatarFollow = map[string]*followState{
		"u-danny": {userID: "u-danny", following: true, known: true, viewer: "me"},
	}
	tt.Frame()
	if err := tt.RightClick("dannyng"); err != nil {
		t.Fatalf("右键头像失败：%v", err)
	}
	tt.Frame()
	if !tt.HasText(detailAvatarUnfollowLabel) || tt.HasText(detailAvatarFollowLabel) {
		t.Fatalf("已关注时菜单文案不对：%q", tt.Texts())
	}
	if err := tt.Click(detailAvatarUnfollowLabel); err != nil {
		t.Fatalf("点取消关注失败：%v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for method.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got, _ := method.Load().(string); got != http.MethodDelete {
		t.Errorf("取关请求方法 = %q，期望 DELETE", got)
	}
}

// 已屏蔽时菜单换成「移除屏蔽」，点了把作者从名单里移出去。
func TestDetailAvatarMenuUnblocksWhenBlocked(t *testing.T) {
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			body.Store(string(b))
		}
		_, _ = w.Write([]byte(envelopeJSON(`{"blocked_users":[],"blocked_nodes":[],"keywords":[]}`)))
	}))
	defer srv.Close()

	// 被屏蔽的作者在评论列表里会被折叠成提示行，没有头像可右击；正文作者
	// 行仍照常画头像，用它验菜单文案。
	a, _ := emojiDetailApp(t, "正文")
	a.client.Base = srv.URL
	a.auth.token = &tokenData{AccessToken: "tok", Username: "me"}
	a.contentFilter.filter = newContentFilter(contentFilterDTO{BlockedUsers: []string{"jimmy"}})
	a.contentFilter.synced = true // 名单已在本地，右键不该再用假服务的空名单覆盖
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.RightClick("jimmy"); err != nil {
		t.Fatalf("右键正文作者头像失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText(detailAvatarUnblockLabel) || tt.HasText(detailAvatarBlockLabel) {
		t.Fatalf("已屏蔽时菜单文案不对：%q", tt.Texts())
	}
	if err := tt.Click(detailAvatarUnblockLabel); err != nil {
		t.Fatalf("点移除屏蔽失败：%v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for body.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := body.Load().(string)
	if !strings.Contains(got, "blocked_users") || strings.Contains(got, "jimmy") {
		t.Errorf("移除屏蔽的请求体不对：%s", got)
	}
}

// 只构建菜单（没有右键打开）时绝不能发请求：否则表态、收藏、改评论开关
// 这些无关操作都会连带拉公开资料与屏蔽名单，既浪费配额也扰乱计数。
func TestDetailAvatarNoFetchWithoutRightClick(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(envelopeJSON(`{"blocked_users":[],"blocked_nodes":[],"keywords":[]}`)))
	}))
	defer srv.Close()

	a, _ := emojiDetailApp(t, "正文")
	a.client.Base = srv.URL + "/api"
	a.auth.token = &tokenData{AccessToken: "tok", Username: "me"}
	tt := ui.NewTester(a.view, 1100, 760)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("没有右键也发了 %d 次关系请求", n)
	}
}

// 右键这一刻才拿关系：公开资料给 is_following，共享名单给屏蔽状态。两者
// 回来后菜单还开着，文案要当场从「关注ta / 屏蔽ta」翻成另一面。
func TestDetailAvatarMenuFetchesRelationOnRightClick(t *testing.T) {
	var profileHits, filterHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/users/jimmy":
			profileHits.Add(1)
			_, _ = fmt.Fprint(w, envelopeJSON(
				`{"id":"u-jimmy","username":"jimmy","is_following":true}`))
		case "/api/v1/me/content-filter":
			filterHits.Add(1)
			_, _ = fmt.Fprint(w, envelopeJSON(
				`{"blocked_users":["jimmy"],"blocked_nodes":[],"keywords":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// 用正文作者：被屏蔽的评论会整行折叠，右键菜单的锚点也就没了。
	a, _ := emojiDetailApp(t, "正文")
	a.client.Base = srv.URL + "/api"
	a.auth.token = &tokenData{AccessToken: "tok", Username: "me"}
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.RightClick("jimmy"); err != nil {
		t.Fatalf("右键头像失败：%v", err)
	}
	// 两个请求都回来后，再泵几帧让 publish 写回状态并重绘菜单。
	deadline := time.Now().Add(3 * time.Second)
	for (profileHits.Load() == 0 || filterHits.Load() == 0) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		tt.Frame()
	}
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if profileHits.Load() != 1 || filterHits.Load() != 1 {
		t.Errorf("右键后取关系次数不对：profile=%d filter=%d", profileHits.Load(), filterHits.Load())
	}
	if !tt.HasText(detailAvatarUnfollowLabel) || !tt.HasText(detailAvatarUnblockLabel) {
		t.Errorf("关系回来后菜单文案没翻面：%q", tt.Texts())
	}
	if tt.HasText(detailAvatarFollowLabel) || tt.HasText(detailAvatarBlockLabel) {
		t.Errorf("旧文案还留在菜单里：%q", tt.Texts())
	}
}

// 正文标题下的作者头像也有同一份菜单，关注拿到的是帖子作者的 uuid。
func TestDetailTopicAuthorAvatarMenu(t *testing.T) {
	a, _ := emojiDetailApp(t, "正文")
	a.detail.topic.AuthorID = "u-author"
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if err := tt.RightClick("jimmy"); err != nil {
		t.Fatalf("右键正文作者头像失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	for _, want := range []string{"@jimmy", detailAvatarFollowLabel, detailAvatarBlockLabel} {
		if !tt.HasText(want) {
			t.Errorf("正文作者头像菜单缺少 %q：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("@jimmy"); err != nil {
		t.Fatalf("点 @jimmy 失败：%v", err)
	}
	if a.detail.draft != "@jimmy " {
		t.Errorf("评论草稿 = %q，期望 @jimmy ", a.detail.draft)
	}
}

// 点 @名字 把带尾随空格的提及写进评论输入框，并把光标留在框里。
func TestDetailAvatarMentionInsertsIntoReply(t *testing.T) {
	a, tt := avatarMenuTestApp(t)
	tt.Frame()
	if err := tt.RightClick("dannyng"); err != nil {
		t.Fatalf("右键头像失败：%v", err)
	}
	tt.Frame()
	if err := tt.Click("@dannyng"); err != nil {
		t.Fatalf("点 @dannyng 失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	if got, want := a.detail.draft, "@dannyng "; got != want {
		t.Errorf("评论草稿 = %q，期望 %q", got, want)
	}
	if !tt.Focused("写评论") {
		t.Error("插入提及后没有把焦点还给评论输入框")
	}
}

// 自己的头像不能 @ 自己：菜单里的 @名字 是禁用项，点了草稿也不变。
func TestDetailAvatarMentionSkipsSelf(t *testing.T) {
	a, tt := avatarMenuTestApp(t)
	a.auth.token = &tokenData{AccessToken: "tok", Username: "dannyng"}
	tt.Frame()
	if err := tt.RightClick("dannyng"); err != nil {
		t.Fatalf("右键自己的头像失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	// 禁用项点了不该有动作；Tester 对禁用元素可能直接报点击失败，这里
	// 只以草稿为准，不把点击的返回值当断言。
	_ = tt.Click("@dannyng")
	if a.detail.draft != "" {
		t.Errorf("自己的头像竟然把 @dannyng 插进了草稿：%q", a.detail.draft)
	}
}

// 关注入口用 AuthorID 走 /v1/users/:uuid/follow，状态按 uuid 记在这篇详情上。
func TestDetailAvatarFollowUsesAuthorID(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/users/u-danny/follow" {
			if r.Method != http.MethodPost {
				t.Errorf("关注请求方法不对：%s", r.Method)
			}
			hits.Add(1)
		}
		// 右键时还会拉公开资料与屏蔽名单，给个无状态响应即可。
		_, _ = w.Write([]byte(envelopeJSON(`{"blocked_users":[],"blocked_nodes":[],"keywords":[]}`)))
	}))
	defer srv.Close()

	a, tt := avatarMenuTestApp(t)
	a.client.Base = srv.URL + "/api"
	a.auth.token = &tokenData{AccessToken: "tok", Username: "me"}
	tt.Frame()
	if err := tt.RightClick("dannyng"); err != nil {
		t.Fatalf("右键头像失败：%v", err)
	}
	tt.Frame()
	if err := tt.Click(detailAvatarFollowLabel); err != nil {
		t.Fatalf("点关注失败：%v（%q）", err, tt.Texts())
	}
	fs := a.detail.avatarFollow["u-danny"]
	if fs == nil || fs.userID != "u-danny" {
		t.Fatalf("关注状态没有按 uuid 记下来：%+v", a.detail.avatarFollow)
	}
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hits.Load() != 1 {
		t.Errorf("关注请求发了 %d 次，期望 1 次", hits.Load())
	}
}

// 屏蔽入口复用内容屏蔽名单，并把服务端返回的名单写入缓存。
func TestDetailAvatarBlockTogglesFilter(t *testing.T) {
	var puts atomic.Int32
	srv := echoFilterServer(t, &puts)
	defer srv.Close()

	a, tt := avatarMenuTestApp(t)
	a.client.Base = srv.URL
	a.client.HTTP = srv.Client()
	a.auth.token = &tokenData{AccessToken: "tok", Username: "me"}
	a.contentFilter.filter = newContentFilter(contentFilterDTO{})
	a.contentFilter.synced = true
	cacheKey := a.contentFilterCacheKey()
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), " "), "dannyng") {
		t.Fatalf("评论作者没有画出来：%q", tt.Texts())
	}
	if err := tt.RightClick("dannyng"); err != nil {
		t.Fatalf("右键头像失败：%v", err)
	}
	tt.Frame()
	if err := tt.Click(detailAvatarBlockLabel); err != nil {
		t.Fatalf("点屏蔽失败：%v（%q）", err, tt.Texts())
	}
	waitFor(t, func() bool {
		var saved contentFilterCache
		_, ok := a.kv.get(cacheKey, &saved)
		users := saved.DTO.BlockedUsers
		return ok && len(users) == 1 && users[0] == "dannyng"
	}, "屏蔽名单没有保存到缓存")
	if puts.Load() != 1 {
		t.Errorf("屏蔽名单写了 %d 次，期望 1 次", puts.Load())
	}
}
