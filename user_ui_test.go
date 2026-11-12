package main

import (
	"image"
	"image/color"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 统一的用户标识：头像边长必须跟着昵称字号走，字号越大头像越大。
func TestUserTagAvatarFollowsNameSize(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Gap(10).Padding(10).Children(func() {
			a.userTag(c, userTagSpec{Name: "small", Label: "@small", Size: 12, Bold: true})
			a.userTag(c, userTagSpec{Name: "big", Label: "@big", Size: 20, Bold: true})
		})
	}, 400, 240)
	tt.Frame()

	small, ok := tt.AccessByRole(ui.RoleImage, "small")
	if !ok {
		t.Fatalf("12px 昵称旁没有头像：%q", tt.Texts())
	}
	big, ok := tt.AccessByRole(ui.RoleImage, "big")
	if !ok {
		t.Fatalf("20px 昵称旁没有头像：%q", tt.Texts())
	}
	if got, want := small.Bounds.W, userAvatarSize(12); got != want {
		t.Errorf("12px 昵称的头像宽 %.1f，期望 %.1f", got, want)
	}
	if got, want := big.Bounds.W, userAvatarSize(20); got != want {
		t.Errorf("20px 昵称的头像宽 %.1f，期望 %.1f", got, want)
	}
	if big.Bounds.W <= small.Bounds.W {
		t.Errorf("字号变大后头像没有变大：%.1f -> %.1f", small.Bounds.W, big.Bounds.W)
	}
	if !tt.HasText("@small") || !tt.HasText("@big") {
		t.Errorf("昵称文本没有渲染：%q", tt.Texts())
	}
}

// 评论作者与列表行作者的头像跟着昵称字号走：评论区 12px 昵称配 18px 头像。
// 帖子作者的头像在详情页头单独放大到固定边长，并贴在页头最左侧。
func TestDetailAuthorAvatarsFollowNameSize(t *testing.T) {
	a := detailAppWithReplies(t)
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	author, ok := tt.AccessByRole(ui.RoleImage, "jimmy")
	if !ok {
		t.Fatalf("帖子作者没有头像：%q", tt.Texts())
	}
	if got, want := author.Bounds.W, topicAvatarSize; got != want {
		t.Errorf("帖子作者头像宽 %.1f，期望 %.1f", got, want)
	}
	if author.Bounds.X > 20 {
		t.Errorf("帖子作者头像应贴在页头最左侧：x=%.1f", author.Bounds.X)
	}
	for _, name := range []string{"dannyng", "alice", "mod"} {
		comment, ok := tt.AccessByRole(ui.RoleImage, name)
		if !ok {
			t.Fatalf("评论作者 %q 没有头像：%q", name, tt.Texts())
		}
		if got, want := comment.Bounds.W, userAvatarSize(12); got != want {
			t.Errorf("评论作者 %q 的头像宽 %.1f，期望 %.1f", name, got, want)
		}
	}
}

// 通知行的操作者头像画在「@昵称 …」前，原来的整句文案保持不变。
func TestNotificationRowShowsActorAvatar(t *testing.T) {
	a := newApp(t.TempDir())
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	st := a.tabs[notificationTabIndex()]
	st.loaded = true
	st.items = []Item{{Key: "n1", Notif: &notification{
		ID: "n1", Type: "mention",
		FromUser: mustObject(t, `{"username":"dannyng"}`),
		Payload:  mustObject(t, `{"commentText":"这条是提及"}`),
	}}}
	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()

	// 通知卡片本身是按钮，头像在无障碍树里并入按钮文案；Find 取先出现的
	// 头像标签，刚好能核对头像尺寸。
	avatar, ok := tt.Find("dannyng")
	if !ok {
		t.Fatalf("通知操作者没有头像：%q", tt.Texts())
	}
	if got, want := avatar.W, userAvatarSize(13); got != want {
		t.Errorf("通知操作者头像宽 %.1f，期望 %.1f", got, want)
	}
	if !tt.HasText("@dannyng 提到了你") {
		t.Errorf("通知文案被改动：%q", tt.Texts())
	}
}

// 普通列表行的副标题把昵称拆出来交给统一组件：作者旁有头像，节点与时间
// 仍在原来的位置。
func TestListSubtitleAuthorGetsAvatar(t *testing.T) {
	a := testApp(t)
	it := itemFrom([]byte(`{"short_id":"A1","title":"测试标题",
		"author":{"username":"bighammer","avatar_url":"https://example.com/a.png"},
		"node":{"name":"职场","slug":"work"},
		"created_at":"2026-11-01T10:00:00.000Z"}`), kindTopic)
	if it.Author != "bighammer" || it.AuthorAvatar != "https://example.com/a.png" {
		t.Fatalf("列表行没有拆出作者与头像：%+v", it)
	}
	if it.SubtitlePrefix != "职场 · " || it.SubtitleSuffix == "" {
		t.Errorf("副标题分段不对：前缀 %q 后缀 %q", it.SubtitlePrefix, it.SubtitleSuffix)
	}
	a.tabs[0].items = []Item{it}
	a.tabs[0].loaded = true
	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()

	// 列表行是按钮，头像并入按钮文案；头像标签先于昵称文本出现。
	avatar, ok := tt.Find("bighammer")
	if !ok {
		t.Fatalf("列表行作者没有头像：%q", tt.Texts())
	}
	if got, want := avatar.W, userAvatarSize(11); got != want {
		t.Errorf("列表行作者头像宽 %.1f，期望 %.1f", got, want)
	}
	if !tt.HasText("职场 · ") || !tt.HasText("bighammer") {
		t.Errorf("副标题前后缀没有渲染：%q", tt.Texts())
	}
}

// 同一个头像地址只下载一次；缓存命中后再次预取不会再发请求。
func TestPrefetchAvatarsLoadsEachURLOnce(t *testing.T) {
	var hits int32
	pixels := solidPNG(t, 8, 8, marker)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pixels)
	}))
	defer srv.Close()

	a := testApp(t)
	a.autoLoad = true
	a.client = &Client{HTTP: srv.Client()}
	u := srv.URL + "/a.png"
	a.prefetchAvatars(u, u)
	a.prefetchAvatars(u)

	deadline := time.Now().Add(3 * time.Second)
	for a.avatarBitmap(u) == nil {
		if time.Now().After(deadline) {
			t.Fatal("头像没有在 3 秒内加载完成")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("同一地址请求了 %d 次，期望 1 次", got)
	}
}

// 评论与动态解码出作者头像：评论作者来自 author.avatar_url，被回复人
// 沿用父评论作者的头像；对不上时不借用别人的头像。
func TestCommentRowsCarryAuthorAvatars(t *testing.T) {
	raw := []byte(`{
		"id":"c1","content":"第一楼","floor":1,
		"author":{"username":"dannyng","avatar_url":"https://r2.2libra.com/a.png"},
		"parent":{"id":"p1","content":"父评论","floor":1,
			"author":{"username":"alice","avatar_url":"https://r2.2libra.com/b.png"}},
		"reply_to_username":"alice",
		"post":{"short_id":"P1","title":"帖子"}
	}`)
	it := commentItemFrom(asObject(raw))
	x := it.Extra
	if x == nil {
		t.Fatal("评论行没有富展示字段")
	}
	if x.AuthorAvatar != "https://r2.2libra.com/a.png" {
		t.Errorf("作者头像 = %q", x.AuthorAvatar)
	}
	if x.ReplyToAvatar != "https://r2.2libra.com/b.png" {
		t.Errorf("被回复人头像 = %q", x.ReplyToAvatar)
	}

	mismatched := []byte(`{
		"id":"c2","content":"第二楼","floor":2,
		"author":{"username":"dannyng","avatar_url":"https://r2.2libra.com/a.png"},
		"parent":{"id":"p1","content":"父评论","floor":1,
			"author":{"username":"alice","avatar_url":"https://r2.2libra.com/b.png"}},
		"reply_to_username":"bob"
	}`)
	x2 := commentItemFrom(asObject(mismatched)).Extra
	if x2 == nil || x2.ReplyToAvatar != "" {
		t.Errorf("被回复人与父评论作者不一致时不应借用头像：%+v", x2)
	}
}

// 金币页的排行榜与「收到打赏 @某人」也走同一套用户标识：头像尺寸跟着
// 各自的字号走（排行 13px、流水详情 12px）。
func TestCoinRowsShowUserAvatars(t *testing.T) {
	a := testApp(t)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Gap(8).Padding(8).Children(func() {
			a.coinRankRow(c, coinRankEntry{Rank: 1, Username: "bighammer", Balance: 100})
			a.coinTransactionRow(c, coinTransaction{
				ID: "tx1", Reason: "receive_reward", Amount: 5,
				FromUser: "jimmy", RelateTitle: "一个帖子",
				CreatedAt: "2026-11-01T10:00:00.000Z",
			}, false)
		})
	}, 600, 300)
	tt.Frame()

	rank, ok := tt.Find("bighammer")
	if !ok {
		t.Fatalf("排行榜用户名旁没有头像：%q", tt.Texts())
	}
	if got, want := rank.W, userAvatarSize(13); got != want {
		t.Errorf("排行榜头像宽 %.1f，期望 %.1f", got, want)
	}
	from, ok := tt.Find("jimmy")
	if !ok {
		t.Fatalf("打赏详情里没有头像：%q", tt.Texts())
	}
	if got, want := from.W, userAvatarSize(12); got != want {
		t.Errorf("打赏人像宽 %.1f，期望 %.1f", got, want)
	}
	if !tt.HasText("@jimmy") || !tt.HasText("收到打赏") {
		t.Errorf("打赏文案没有保留：%q", tt.Texts())
	}
}

// 通知里的聚合表态也把点名的用户画上头像：计数与 @昵称文案保持原样，
// 头像跟着 11px 的名单字号走。
func TestNotificationReactionNamesGetAvatars(t *testing.T) {
	a := newApp(t.TempDir())
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	n := decodeNotificationObject(mustObject(t, `{
		"id":"n2","type":"reaction",
		"payload":{"relations":[{"emoji":"👍","username":"alice"},{"emoji":"👍","username":"bob"}],
			"reactions":{"👍":3}}
	}`))
	if n == nil {
		t.Fatal("消息解码失败")
	}
	st := a.tabs[notificationTabIndex()]
	st.loaded = true
	st.items = []Item{{Key: "n2", Notif: n}}
	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()

	for _, name := range []string{"alice", "bob"} {
		avatar, ok := tt.Find(name)
		if !ok {
			t.Fatalf("表态名单里的 %q 没有头像：%q", name, tt.Texts())
		}
		if got, want := avatar.W, userAvatarSize(11); got != want {
			t.Errorf("%q 的头像宽 %.1f，期望 %.1f", name, got, want)
		}
	}
	if !tt.HasText("👍3") || !tt.HasText("@alice") || !tt.HasText("@bob") {
		t.Errorf("表态文案没有保留：%q", tt.Texts())
	}
}

// 头像图片必须按头像框的实际尺寸绘制。ui.Avatar 的图片子元素按主题字号的
// 2.25 倍创建，外部缩小头像框后图片仍按大尺寸绘制，被圆圈裁成中间一块；
// 这里用一张四周带绿色边框的图片验证边框能画到头像边缘，说明整图完整显示。
func TestAvatarImageIsNotCropped(t *testing.T) {
	const src = 40
	img := image.NewRGBA(image.Rect(0, 0, src, src))
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for y := 0; y < src; y++ {
		for x := 0; x < src; x++ {
			c := white
			if x < 2 || y < 2 || x >= src-2 || y >= src-2 {
				c = marker
			}
			img.SetRGBA(x, y, c)
		}
	}
	bmp := ui.NewBitmap(img)

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Padding(10).Children(func() {
			avatarView(c, "borderuser", bmp, 18)
		})
	}, 80, 80)
	tt.Frame()

	r, ok := tt.Find("borderuser")
	if !ok {
		t.Fatalf("头像没有渲染：%q", tt.Texts())
	}
	if r.W != 18 || r.H != 18 {
		t.Errorf("头像框 = %.0f×%.0f，期望 18×18", r.W, r.H)
	}
	if n := countMarker(tt.Image()); n == 0 {
		t.Error("头像图片的绿色边框没有画出来：图片没有按头像框尺寸完整显示")
	}
}

// 没有图片时画昵称首字母，文字要落在头像框内，不能被圆圈裁掉。
func TestAvatarInitialsFallback(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Padding(10).Children(func() {
			avatarView(c, "jimmy", nil, 24)
		})
	}, 80, 80)
	tt.Frame()

	avatar, ok := tt.Find("jimmy")
	if !ok {
		t.Fatalf("头像没有渲染：%q", tt.Texts())
	}
	initial, ok := tt.Find("J")
	if !ok {
		t.Fatalf("头像没有画昵称首字母：%q", tt.Texts())
	}
	if initial.X < avatar.X || initial.Y < avatar.Y ||
		initial.X+initial.W > avatar.X+avatar.W ||
		initial.Y+initial.H > avatar.Y+avatar.H {
		t.Errorf("首字母 %v 超出了头像框 %v", initial, avatar)
	}
}

// 头像地址可能是协议相对或站内绝对路径：补成绝对地址后才能走下载与缓存。
func TestNormalizeAvatarURL(t *testing.T) {
	cases := map[string]string{
		"":                               "",
		"  https://r2.2libra.com/a.png ": "https://r2.2libra.com/a.png",
		"//r2.2libra.com/a.png":          "https://r2.2libra.com/a.png",
		"/avatars/a.png":                 "https://r2.2libra.com/avatars/a.png",
		"https://cdn.example/a.png":      "https://cdn.example/a.png",
	}
	for in, want := range cases {
		if got := normalizeAvatarURL(in); got != want {
			t.Errorf("normalizeAvatarURL(%q) = %q，期望 %q", in, got, want)
		}
	}
}
