package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// user_pk_choice 是线上才有的字段（api-read.md 的稳定字段表里没有），按其它
// 评论区字段的惯例用容错解码读取；楼中楼也要带上。
func TestDecodeReplyUserPKChoice(t *testing.T) {
	r := decodeReply([]byte(`{
		"id":"r1","content":"x","user_pk_choice":"opt-blue",
		"children":[{"id":"r2","user_pk_choice":"opt-red"}]
	}`), 0)
	if r.UserPKChoice != "opt-blue" {
		t.Errorf("顶层 user_pk_choice = %q，期望 opt-blue", r.UserPKChoice)
	}
	if len(r.Children) != 1 || r.Children[0].UserPKChoice != "opt-red" {
		t.Errorf("楼中楼的 user_pk_choice 没解出来：%+v", r.Children)
	}
	if got := decodeReply([]byte(`{"id":"r3","user_pk_choice":null}`), 0).UserPKChoice; got != "" {
		t.Errorf("null / 缺字段应解码成空串，得到 %q", got)
	}
}

// 所有 PK 帖子统一按选项顺序定队伍：第一个红队、第二个蓝队。文案取选项文本，
// 缺失时退回队名。普通帖子、多选、没投票、选项对不上都不挂标签。
func TestReplyPKTeam(t *testing.T) {
	// 选项文本故意写成别的：标签文字只按列表位置固定，与选项文本无关。
	v := &voteBlock{Type: "pk", Options: []voteOption{
		{ID: "red", Text: "正方观点"}, {ID: "blue", Text: "反方观点"}, {ID: "third"},
	}}
	cases := []struct {
		name    string
		v       *voteBlock
		choice  string
		want    pkTeamTag
		wantTag bool
	}{
		{"红队是第一个选项", v, "red", pkTeamTag{"红队", ui.Hex("#ffa1b1")}, true},
		{"蓝队是第二个选项", v, "blue", pkTeamTag{"蓝队", ui.Hex("#66d6fe")}, true},
		{"老响应没有选项文本", &voteBlock{Type: "pk", Options: []voteOption{{ID: "red"}}},
			"red", pkTeamTag{"红队", ui.Hex("#ffa1b1")}, true},
		{"第三个选项不挂标签", v, "third", pkTeamTag{}, false},
		{"没投票", v, "", pkTeamTag{}, false},
		{"选项对不上", v, "gone", pkTeamTag{}, false},
		{"没有投票块", nil, "red", pkTeamTag{}, false},
		{"多选不算 PK", &voteBlock{Type: "multiple", Options: []voteOption{{ID: "red"}}},
			"red", pkTeamTag{}, false},
		{"老响应缺 type 时按 PK 处理", &voteBlock{Options: []voteOption{{ID: "red"}}},
			"red", pkTeamTag{"红队", ui.Hex("#ffa1b1")}, true},
	}
	for _, tc := range cases {
		got, ok := replyPKTeam(tc.v, tc.choice)
		if ok != tc.wantTag || got != tc.want {
			t.Errorf("%s：replyPKTeam = (%+v, %v)，期望 (%+v, %v)", tc.name, got, ok, tc.want, tc.wantTag)
		}
	}
}

// 队伍标签要落在回复第一行的作者信息之后、楼层号之前，底色是红 / 蓝队色；
// 没投票的回复不挂标签。
func TestReplyRowPKTeamTag(t *testing.T) {
	a := detailAppWithReplies(t)
	a.theme.mode = themeLight
	a.detail.topic.Vote = &voteBlock{ID: "v1", Type: "pk", Options: []voteOption{
		{ID: "opt-red", Text: "正方观点"},
		{ID: "opt-blue", Text: "反方观点"},
	}}
	a.detail.replyList = []replyItem{
		{ID: "r1", Author: "alice", Floor: 1, Content: "红队的人在这", UserPKChoice: "opt-red"},
		{ID: "r2", Author: "bob", Floor: 2, Content: "蓝队的人在这", UserPKChoice: "opt-blue"},
		{ID: "r3", Author: "carol", Floor: 3, Content: "没投票的人在这"},
	}
	a.detail.replyTotal = len(a.detail.replyList)
	a.detail.rebuildDisplay()

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	im := tt.Image()

	seen := 0
	for _, text := range tt.Texts() {
		if text == "红队" || text == "蓝队" {
			seen++
		}
	}
	if seen != 2 {
		t.Fatalf("只有投过 PK 的两条回复该带队伍标签，实际 %d 枚：%q", seen, tt.Texts())
	}

	check := func(label, author, floor string, want ui.Color) {
		t.Helper()
		tag, ok := tt.Find(label)
		if !ok {
			t.Fatalf("找不到队伍标签 %q：%q", label, tt.Texts())
		}
		name, ok := tt.Find(author)
		if !ok {
			t.Fatalf("找不到作者 %q：%q", author, tt.Texts())
		}
		floorText, ok := tt.Find(floor)
		if !ok {
			t.Fatalf("找不到楼层号 %q：%q", floor, tt.Texts())
		}
		if tag.X <= name.X || tag.X >= floorText.X {
			t.Errorf("%s 标签应在作者之后、楼层号之前：tag=%+v author=%+v floor=%+v",
				label, tag, name, floorText)
		}
		if d := tag.Y + tag.H/2 - (floorText.Y + floorText.H/2); absInt(int(d)) > 2 {
			t.Errorf("%s 标签应在回复第一行：tag=%+v floor=%+v", label, tag, floorText)
		}
		hits := 0
		for y := int(tag.Y); y < int(tag.Y+tag.H); y++ {
			for x := int(tag.X); x < int(tag.X+tag.W); x++ {
				c := im.RGBAAt(x, y)
				if absInt(int(c.R)-int(want.R)) <= 2 &&
					absInt(int(c.G)-int(want.G)) <= 2 &&
					absInt(int(c.B)-int(want.B)) <= 2 {
					hits++
				}
			}
		}
		if hits < 60 {
			t.Errorf("%s 标签底色应有 #%02x%02x%02x，只量到 %d 个像素", label, want.R, want.G, want.B, hits)
		}
	}
	check("红队", "alice", "1 楼", ui.Hex("#ffa1b1"))
	check("蓝队", "bob", "2 楼", ui.Hex("#66d6fe"))
}
