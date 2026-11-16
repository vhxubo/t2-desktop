package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// 最新评论的分组只切分「相邻且同名」：A A A B A A 必须是 3、1、2 三组，
// 不能因为 A 出现过就把后面那两条并回第一组，也不能把中间那条 B 吞掉。
func TestGroupCommentsOnlyAdjacentSameAuthor(t *testing.T) {
	names := []string{"a", "a", "a", "b", "a", "a"}
	items := make([]Item, len(names))
	for i, name := range names {
		items[i] = commentRow(i, name, "S1", "帖子标题")
	}
	groups := groupComments(items)
	if len(groups) != 3 {
		t.Fatalf("分组数 = %d，期望 3：%+v", len(groups), groups)
	}
	wantLen := []int{3, 1, 2}
	wantStart := []int{0, 3, 4}
	for i, g := range groups {
		if len(g.members) != wantLen[i] || g.start != wantStart[i] {
			t.Errorf("第 %d 组 = %d 条（起点 %d），期望 %d 条（起点 %d）",
				i, len(g.members), g.start, wantLen[i], wantStart[i])
		}
	}
	// 组内保持接口返回的顺序：折叠时露出来的必须是出现最早的那条。
	if groups[2].members[0].CommentID != items[4].CommentID {
		t.Errorf("组首条不是出现最早的那条：%q / %q",
			groups[2].members[0].CommentID, items[4].CommentID)
	}
}

// 「匿名」与没有作者信息的行各自成组：不同人的匿名评论并到一起就是把别人的
// 话算到同一个人头上。
func TestGroupCommentsKeepsAnonymousSeparate(t *testing.T) {
	items := []Item{
		commentRow(0, "匿名", "S1", "帖子"),
		commentRow(1, "匿名", "S1", "帖子"),
		{Key: "raw-1", Title: "没有 Extra 的行"},
		{Key: "raw-2", Title: "另一个没有 Extra 的行"},
	}
	groups := groupComments(items)
	if len(groups) != 4 {
		t.Fatalf("匿名与无作者的行不该聚合：%d 组 %+v", len(groups), groups)
	}
	for _, g := range groups {
		if len(g.members) != 1 {
			t.Errorf("组里有 %d 条，期望各自单独成组", len(g.members))
		}
	}
}

// 分组行的说明要分清「同一个帖子下连着刷」和「横跨好几个帖子」。
func TestCommentGroupTitle(t *testing.T) {
	same := []Item{
		commentRow(0, "a", "S1", "社会观察"),
		commentRow(1, "a", "S1", "社会观察"),
	}
	cross := []Item{
		commentRow(0, "a", "S1", "社会观察"),
		commentRow(1, "a", "S2", "职场"),
	}
	noPost := []Item{
		{Key: "k1", Extra: &rowExtra{Author: "a"}},
		{Key: "k2", Extra: &rowExtra{Author: "a"}},
	}
	cases := []struct {
		members []Item
		want    string
	}{
		{same, "在《社会观察》下连续评论 2 条"},
		{cross, "横跨 2 个帖子连续评论 2 条"},
		{noPost, "连续评论 2 条"},
	}
	for _, c := range cases {
		if got := commentGroupTitle(c.members, commentGroupPosts(c.members)); got != c.want {
			t.Errorf("说明 = %q，期望 %q", got, c.want)
		}
	}
}

// 默认折叠：5 条同人连续评论只出「分组行 + 首条」两行；展开后 5 条全出，
// 收起又回到两行。单条评论不画分组行，普通 tab 也不分组。
func TestFoldRowsDefaultShowsFirstOnly(t *testing.T) {
	a := testApp(t)
	st := a.tabs[4]
	for i := 0; i < 5; i++ {
		st.items = append(st.items, commentRow(i, "a", "S1", "帖子标题"))
	}
	st.refold(tabSpecs[4])

	rows := st.foldRows()
	if len(rows) != 2 {
		t.Fatalf("折叠后可见行 = %d，期望 2（分组行 + 首条）", len(rows))
	}
	if rows[0].group == nil || rows[0].total != 5 {
		t.Errorf("第一行不是 5 条的分组行：%+v", rows[0])
	}
	if rows[1].item.CommentID != "c0" {
		t.Errorf("露出来的不是最早那条：%q", rows[1].item.CommentID)
	}

	st.toggle(*rows[0].group)
	rows = st.foldRows()
	if len(rows) != 6 {
		t.Fatalf("展开后可见行 = %d，期望 6（分组行 + 5 条）", len(rows))
	}
	if !rows[0].open {
		t.Error("展开后分组行没有标成已展开")
	}

	st.toggle(*rows[0].group)
	if rows = st.foldRows(); len(rows) != 2 {
		t.Errorf("收起后可见行 = %d，期望 2", len(rows))
	}

	// 单条评论不画分组行：分组结果本身只有一组一条。
	one := &tabState{items: []Item{commentRow(0, "a", "S1", "帖子标题")}}
	one.refold(tabSpecs[4])
	if rows := one.foldRows(); len(rows) != 1 || rows[0].group != nil {
		t.Errorf("单条评论不该画分组行：%+v", rows)
	}

	// 「动态」等没打开聚合的 tab 一律逐行列出。
	plain := &tabState{items: []Item{
		commentRow(0, "a", "S1", "帖子标题"),
		commentRow(1, "a", "S1", "帖子标题"),
	}}
	plain.refold(tabSpecs[6])
	if rows := plain.foldRows(); len(rows) != 2 {
		t.Errorf("没开聚合的 tab 不该合并：%d 行", len(rows))
	}
}

// 第 2 页正好接上第 1 页末尾的同一个人时，分组必须跨页续上，不能断成两组。
func TestRefoldMergesAcrossPages(t *testing.T) {
	st := &tabState{items: []Item{commentRow(0, "a", "S1", "帖子标题")}}
	st.refold(tabSpecs[4])
	if len(st.fold.groups) != 1 {
		t.Fatalf("基线分组长 = %d", len(st.fold.groups))
	}
	st.items = append(st.items,
		commentRow(1, "a", "S1", "帖子标题"),
		commentRow(2, "a", "S1", "帖子标题"),
	)
	st.refold(tabSpecs[4])
	if len(st.fold.groups) != 1 || len(st.fold.groups[0].members) != 3 {
		t.Errorf("跨页续接失败：%d 组 %+v", len(st.fold.groups), st.fold.groups)
	}
}

// 整页刷新（回到第 1 页）要清掉展开状态：否则刚刷新的一屏还停在上次展开的样子。
func TestFoldResetsOnReload(t *testing.T) {
	st := &tabState{}
	st.fold.open = map[string]bool{"c0": true}
	st.fold.reset()
	if len(st.fold.open) != 0 || len(st.fold.groups) != 0 {
		t.Errorf("reset 之后仍留着分组或展开状态：%+v", st.fold)
	}
}

// 列表里的默认表现：5 条同人连续评论只露一条正文，点分组行展开到全部，
// 再点一次收起。这同时守住「聚合不会丢内容」。
func TestLatestCommentsFoldInList(t *testing.T) {
	a := testApp(t)
	a.tab = 4
	st := a.tabs[4]
	bodies := []string{"第一条正文", "第二条正文", "第三条正文", "第四条正文", "第五条正文"}
	for i, body := range bodies {
		it := itemFrom([]byte(`{"id":"c`+string(rune('0'+i))+"\","+
			`"content":"`+body+`",`+
			`"author":{"username":"dannyng"},`+
			`"post":{"short_id":"S1","title":"帖子标题","node":{"slug":"n","name":"节点"}}}`), kindComment)
		st.items = append(st.items, it)
	}
	st.loaded = true
	st.refold(tabSpecs[4])

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	if !tt.HasText(bodies[0]) {
		t.Fatalf("折叠时首条没有出现：%q", tt.Texts())
	}
	for _, body := range bodies[1:] {
		if tt.HasText(body) {
			t.Errorf("折叠时不该出现 %q：%q", body, tt.Texts())
		}
	}
	if !tt.HasText("共 5 条") {
		t.Errorf("分组行没有写出条数：%q", tt.Texts())
	}

	if err := tt.Click("展开 @dannyng 的连续评论"); err != nil {
		t.Fatalf("点分组行失败：%v", err)
	}
	tt.Frame()
	for _, body := range bodies {
		if !tt.HasText(body) {
			t.Errorf("展开后缺少 %q：%q", body, tt.Texts())
		}
	}

	if err := tt.Click("收起 @dannyng 的连续评论"); err != nil {
		t.Fatalf("点收起失败：%v", err)
	}
	tt.Frame()
	if tt.HasText(bodies[1]) {
		t.Errorf("收起后第二条仍在：%q", tt.Texts())
	}
	if !tt.HasText(bodies[0]) {
		t.Errorf("收起后首条不见了：%q", tt.Texts())
	}
}

// 展开 / 收起不能让人眼前一跳：分组行必须留在原位，它下面露出来的评论
// 往上长，而不是整屏跟着顶上去。列表很短、还没占满视口时最容易被顶动——
// 滚动位置会被夹到内容末尾，分组行就跑到屏幕中间去了。
func TestFoldToggleKeepsGroupRowInPlace(t *testing.T) {
	a := testApp(t)
	a.tab = 4
	st := a.tabs[4]
	for i := 0; i < 2; i++ {
		st.items = append(st.items, commentRow(i, "a", "S1", "帖子标题"))
	}
	st.loaded = true
	st.refold(tabSpecs[4])

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	before, ok := tt.Find("共 2 条")
	if !ok {
		t.Fatalf("分组行没有渲染：%q", tt.Texts())
	}

	if err := tt.Click("展开 @a 的连续评论"); err != nil {
		t.Fatalf("点展开失败：%v", err)
	}
	for i := 0; i < 2; i++ {
		tt.Frame()
	}
	after, ok := tt.Find("共 2 条")
	if !ok {
		t.Fatalf("展开后分组行不见了：%q", tt.Texts())
	}
	if d := after.Y - before.Y; d > 0.5 || d < -0.5 {
		t.Errorf("展开后分组行跳了 %.1f DIP（%.1f -> %.1f）", d, before.Y, after.Y)
	}

	if err := tt.Click("收起 @a 的连续评论"); err != nil {
		t.Fatalf("点收起失败：%v", err)
	}
	for i := 0; i < 2; i++ {
		tt.Frame()
	}
	back, ok := tt.Find("共 2 条")
	if !ok {
		t.Fatalf("收起后分组行不见了：%q", tt.Texts())
	}
	if d := back.Y - before.Y; d > 0.5 || d < -0.5 {
		t.Errorf("收起后分组行跳了 %.1f DIP（%.1f -> %.1f）", d, before.Y, back.Y)
	}
}

// 视口里已经有一屏内容时同样不许跳：展开 / 收起之后分组行仍钉在原来的位置。
// 页面很长，列表要滚动才画得出下面的行，被测的分组刚好在首屏。
func TestFoldToggleKeepsGroupRowInPlaceWhileScrolled(t *testing.T) {
	a := testApp(t)
	a.tab = 4
	st := a.tabs[4]
	rows := []Item{
		commentRow(0, "a", "S1", "帖子标题"),
		commentRow(1, "a", "S1", "帖子标题"),
		commentRow(2, "a", "S1", "帖子标题"),
	}
	for i := 3; i < 20; i++ {
		rows = append(rows, commentRow(i, "u"+string(rune('0'+i)), "S1", "帖子标题"))
	}
	st.items = rows
	st.loaded = true
	st.refold(tabSpecs[4])

	tt := ui.NewTester(a.view, 1100, 760)
	tt.Frame()
	box, ok := tt.Find("共 3 条")
	if !ok || box.H == 0 {
		t.Fatalf("分组行没有渲染：%+v %v", box, ok)
	}
	// 组本来就是折叠的：先展开再收起，「收起」这一次才点得到。
	check := func(label string, wantY float32, frames int) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("点 %q 失败：%v", label, err)
		}
		for i := 0; i < frames; i++ {
			tt.Frame()
		}
		got, ok := tt.Find("共 3 条")
		if !ok {
			t.Fatalf("%q 之后分组行不见了：%q", label, tt.Texts())
		}
		if d := got.Y - wantY; d > 0.5 || d < -0.5 {
			t.Errorf("%q 之后分组行跳了 %.1f DIP（%.1f -> %.1f）", label, d, wantY, got.Y)
		}
	}

	check("展开 @a 的连续评论", box.Y, 3)
	check("收起 @a 的连续评论", box.Y, 3)
}

// 点展开 / 收起时分组行的位置由列表自己端住：动作里不碰滚动位置，列表把
// 行数变化当作「上面加了行」处理，被点的分组行留在原处。
func TestFoldToggleLeavesScrollAlone(t *testing.T) {
	a := testApp(t)
	a.tab = 4
	st := a.tabs[4]
	st.items = []Item{
		commentRow(0, "a", "S1", "帖子标题"),
		commentRow(1, "a", "S1", "帖子标题"),
	}
	st.refold(tabSpecs[4])
	before := len(st.foldRows())
	if st.fold.openFor(st.fold.groups[0]) {
		t.Fatal("前置条件：组应当是折叠的")
	}
	if !st.toggle(st.fold.groups[0]) {
		t.Fatal("第一次切换应当是「展开」")
	}
	if after := len(st.foldRows()); after <= before {
		t.Errorf("展开后可见行没有增加：%d -> %d", before, after)
	}
	// toggle 返回切换后的状态，不是「有没有发生变化」：切一次 true，再切 false。
	if st.toggle(st.fold.groups[0]) {
		t.Error("再切一次应当是「收起」")
	}
	if after := len(st.foldRows()); after != before {
		t.Errorf("收起后可见行 = %d，期望回到 %d", after, before)
	}
}

// commentRow 造一条归一后的评论行，字段与 commentItemFrom 的结果一致。
func commentRow(i int, author, shortID, postTitle string) Item {
	id := "c" + string(rune('0'+i))
	return Item{
		Key:       id,
		ShortID:   shortID,
		CommentID: id,
		Title:     "正文 " + id,
		Extra: &rowExtra{
			Author:    author,
			PostTitle: postTitle,
		},
	}
}
