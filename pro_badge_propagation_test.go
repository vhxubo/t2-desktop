package main

import (
	"encoding/json"
	"testing"

	"github.com/egoist/mygo/ui"
)

// Pro 图标按头像边长等比缩放：官网默认取 1/3，极小 / 极大头像有可读上下限。
func TestProMarkSizeScalesWithAvatar(t *testing.T) {
	for _, tc := range []struct {
		avatar float32
		want   float32
	}{
		{18, 8},  // 12px 昵称的最小头像，取下限
		{30, 10}, // 20px 昵称
		{84, 28}, // 用户卡片 56px 头像以外的大头像，取上限
	} {
		if got := proMarkSize(tc.avatar); got != tc.want {
			t.Errorf("头像 %.0f 的 Pro 图标 = %.0f，期望 %.0f", tc.avatar, got, tc.want)
		}
	}
}

// 统一用户标识直接驱动 Pro 图标：只有 Pro 用户的头像右下角才出现圆章。
func TestUserTagShowsProMarkOnlyForPro(t *testing.T) {
	a := proTestApp(t)
	for _, tc := range []struct {
		name string
		pro  bool
		want bool
	}{
		{"vip", true, true},
		{"free", false, false},
	} {
		tt := ui.NewTester(func(c *ui.Context) {
			a.userTag(c, userTagSpec{Name: tc.name, Pro: tc.pro})
		}, 200, 80)
		tt.Frame()
		_, got := tt.AccessByRole(ui.RoleImage, "2Libra Pro")
		if got != tc.want {
			t.Errorf("%s 的 Pro 图标 = %v，期望 %v", tc.name, got, tc.want)
		}
	}
}

// is_pro 是各读端点共有的字段：列表、评论、详情、通知与用户搜索都要读出来，
// 否则统一头像组件拿不到标记，图标就只会散落在一两个页面上。
func TestProStatusDecodedAcrossSurfaces(t *testing.T) {
	// 列表行与动态富行
	row := itemFrom([]byte(`{"short_id":"S1","title":"标题","author":{"username":"vip","is_pro":true}}`), kindTopic)
	if !row.AuthorIsPro {
		t.Error("列表行没有解出作者的 is_pro")
	}
	timeline := itemFrom([]byte(`{"short_id":"S2","content":"动态","author":{"username":"vip","is_pro":true}}`), kindTopic)
	if timeline.Extra == nil || !timeline.Extra.AuthorIsPro {
		t.Errorf("动态行的作者 Pro 状态丢失：%+v", timeline.Extra)
	}

	// 评论行（作者与被回复人）
	comment := decodeCommentItem(asObject([]byte(`{"id":"c1","content":"评论","author":{"username":"vip","is_pro":true},` +
		`"parent":{"author":{"username":"vip2","is_pro":true}},"reply_to_username":"vip2"}`)))
	if !comment.AuthorIsPro || !comment.ParentIsPro {
		t.Errorf("评论作者 / 父评论作者的 Pro 状态丢失：%+v", comment)
	}
	commentRow := itemFrom([]byte(`{"id":"c1","content":"评论","author":{"username":"vip","is_pro":true}}`), kindComment)
	if commentRow.Extra == nil || !commentRow.Extra.AuthorIsPro {
		t.Errorf("评论行的作者 Pro 状态丢失：%+v", commentRow.Extra)
	}

	// 帖子详情与评论
	td, err := decodeTopic([]byte(`{"short_id":"S1","author":{"username":"vip","is_pro":true}}`), "S1")
	if err != nil || !td.AuthorIsPro {
		t.Errorf("详情作者的 Pro 状态丢失：%+v, %v", td, err)
	}
	reply := decodeReply([]byte(`{"id":"r1","content":"回复","author":{"username":"vip","is_pro":true}}`), 0)
	if !reply.AuthorIsPro {
		t.Error("详情评论作者的 Pro 状态丢失")
	}

	// 通知的操作者
	n := decodeNotification([]byte(`{"id":"n1","type":"follow","from_user":{"username":"vip","is_pro":true},"payload":{}}`))
	if n == nil || !notificationViewOf(n).ActorIsPro {
		t.Error("通知操作者的 Pro 状态丢失")
	}

	// 用户搜索
	res := decodeSearch(searchUsers, json.RawMessage(`[{"username":"vip","is_pro":true}]`), 1)
	if len(res.hits) != 1 || !res.hits[0].isPro {
		t.Errorf("用户搜索的 Pro 状态丢失：%+v", res.hits)
	}
}
