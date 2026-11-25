package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testDraftEntry(kind string, at time.Time) draftEntry {
	e := draftEntry{
		ID:        "test-" + kind + "-" + at.Format("150405.000000000"),
		Kind:      kind,
		CreatedAt: at,
		UpdatedAt: at,
	}
	if kind == draftKindCompose {
		e.Title = "标题"
		e.Body = "正文第一行\n正文第二行"
		e.NodeID = "node-uuid"
		e.NodeLabel = "职场 · 工作"
		e.Mode = composeModePost
		e.Modules = &composeDraftModules{
			Vote: &composeDraftVote{
				TypeIndex: 1, Title: "存哪", Options: []string{"一个文件", "每份一个文件"},
				Checked: []bool{true, false}, MaxChoices: 1, DeadlineOn: true,
				DeadlineAt: at.Add(time.Hour),
			},
			Restricted: &composeDraftRestricted{AccessChoice: "pro", Content: "付费内容"},
		}
	} else {
		e.ShortID = "abc123"
		e.PostTitle = "一篇帖子"
		e.Text = "第 3 条我试了，确实……"
		e.ReplyTo = draftTarget{ID: "cmt-88", Level: 2, ParentID: "cmt-12", Floor: 12,
			Author: "老王", AuthorID: "u-9"}
	}
	return e
}

func TestDraftStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Microsecond)
	want := []draftEntry{testDraftEntry(draftKindCompose, now), testDraftEntry(draftKindReply, now.Add(-time.Hour))}
	s := newDraftStore(dir, "alice-1a2b3c")
	for _, e := range want {
		s.put(e)
	}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	got := newDraftStore(dir, "alice-1a2b3c").rows()
	// rows 按 UpdatedAt 倒序
	if len(got) != 2 || !reflect.DeepEqual(got[0], want[0]) || !reflect.DeepEqual(got[1], want[1]) {
		t.Fatalf("草稿读写不一致：\n got %+v\nwant %+v", got, want)
	}
	if got[1].ReplyTo.Floor != 12 || got[1].ReplyTo.Author != "老王" {
		t.Errorf("reply_to 没读回来：%+v", got[1].ReplyTo)
	}
	if got[0].Modules == nil || got[0].Modules.Vote == nil || len(got[0].Modules.Vote.Options) != 2 {
		t.Errorf("组件块没读回来：%+v", got[0].Modules)
	}
}

func TestDraftStoreScopedByAccount(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	a := newDraftStore(dir, "alice-1a2b3c")
	b := newDraftStore(dir, "bob-4d5e6f")
	a.put(testDraftEntry(draftKindCompose, now))
	if err := a.save(); err != nil {
		t.Fatal(err)
	}
	if b.count() != 0 {
		t.Fatalf("bob 不该看到 alice 的草稿：%+v", b.rows())
	}
	b.put(testDraftEntry(draftKindReply, now))
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	if a.count() != 1 || newDraftStore(dir, "alice-1a2b3c").count() != 1 {
		t.Errorf("写 bob 不该影响 alice：a=%d", a.count())
	}
	if _, err := os.Stat(filepath.Join(dir, "drafts", "alice-1a2b3c.json")); err != nil {
		t.Errorf("alice 的草稿文件不在：%v", err)
	}
}

func TestDraftStoreCorruptFileFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "drafts", "local.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newDraftStore(dir, "local")
	if s.count() != 0 {
		t.Errorf("坏文件应按空草稿处理：%+v", s.rows())
	}
	if _, err := os.Stat(path + ".bad"); err != nil {
		t.Errorf("坏文件该另存为 .bad：%v", err)
	}
}

func TestDraftStorePrunesOldestReply(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().UTC().Add(-time.Hour)
	s := newDraftStore(dir, "local")
	for i := 0; i < draftMaxReply+1; i++ {
		e := testDraftEntry(draftKindReply, base.Add(time.Duration(i)*time.Second))
		e.ID = "reply-" + e.ShortID + "-" + e.ID
		s.put(e)
	}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	rows := newDraftStore(dir, "local").rows()
	if len(rows) != draftMaxReply {
		t.Fatalf("回复草稿应截到 %d 条，得到 %d", draftMaxReply, len(rows))
	}
	for _, e := range rows {
		if e.UpdatedAt.Equal(base) {
			t.Error("最旧的那条回复草稿没被丢掉")
		}
	}
}

func TestDraftStripUploadPlaceholders(t *testing.T) {
	in := "正文 ![截图](upload:3) 后面 ![正常](https://example.com/a.png) 结束"
	want := "正文  后面 ![正常](https://example.com/a.png) 结束"
	if got := stripUploadPlaceholders(in); got != want {
		t.Fatalf("占位符剥离错误：got %q want %q", got, want)
	}
}

func TestComposeDraftEntrySkipsClosedModules(t *testing.T) {
	st := newComposeState()
	st.title = "标题"
	st.voteTitle = "没打开也别存"
	e := st.draftEntry()
	if e.Modules != nil {
		t.Fatalf("组件都关着时不该写 modules：%+v", e.Modules)
	}
	st.voteOpen = true
	e = st.draftEntry()
	if e.Modules == nil || e.Modules.Vote == nil {
		t.Fatal("打开投票后应写入 vote 块")
	}
	if e.Modules.Score != nil {
		t.Error("没打开的评分块不该写")
	}
}

func TestComposeDraftApplyResetsKeyAndReceipt(t *testing.T) {
	a := testApp(t)
	st := a.compose
	if st == nil {
		st = newComposeState()
		a.compose = st
	}
	st.key, st.keySig = "old-key", "old-sig"
	st.okTitle, st.okShortID, st.okURL, st.okPending = "旧回执", "sid", "url", true
	e := testDraftEntry(draftKindCompose, time.Now().UTC())
	a.applyComposeDraft(st, e)
	if st.key != "" || st.keySig != "" {
		t.Errorf("恢复后幂等键没清：%q / %q", st.key, st.keySig)
	}
	if st.okTitle != "" || st.okShortID != "" || st.okURL != "" || st.okPending {
		t.Errorf("恢复后旧回执没清：%+v", st)
	}
	if st.draftID != e.ID || st.title != e.Title || st.nodeID != e.NodeID {
		t.Errorf("草稿内容没恢复：%+v", st)
	}
	if !st.voteOpen || st.voteTitle != "存哪" {
		t.Errorf("投票组件没恢复：%+v", st)
	}
	if st.draftFP != st.draftSavedFP {
		t.Error("恢复这一下不该留下未落盘的改动")
	}
}

func TestComposeDraftFingerprintCoversModules(t *testing.T) {
	st := newComposeState()
	base := st.draftFingerprint()
	st.voteOpen = true
	st.voteOptions = []string{"甲", "乙"}
	if st.draftFingerprint() == base {
		t.Error("改投票选项应改变指纹")
	}
	st.voteOpen = false
	st.anonymous = true
	if st.draftFingerprint() == base {
		t.Error("切匿名开关应改变指纹")
	}
	st.anonymous = false
	st.nodeID = "node-uuid"
	if st.draftFingerprint() == base {
		t.Error("选节点应改变指纹")
	}
}

func TestReplyDraftFingerprintCoversTarget(t *testing.T) {
	d := &detailState{draft: "同一段话"}
	base := d.draftFingerprint()
	d.replyTo = replyTarget{ID: "cmt-1", Floor: 3}
	if d.draftFingerprint() == base {
		t.Error("换「回复谁」应改变指纹")
	}
	d.replyTo = replyTarget{ID: "cmt-2", Floor: 3}
	if d.draftFingerprint() == base {
		t.Error("换被回复的评论应改变指纹")
	}
}

func TestClearAllDraftsClearsLiveState(t *testing.T) {
	a := testApp(t)
	a.compose = newComposeState()
	a.compose.title = "要被清掉的草稿"
	d := &detailState{shortID: "abc123", draft: "要被清掉的回复", replyTo: replyTarget{ID: "cmt-1"}}
	a.detail = d
	a.flushDrafts()
	if a.drafts.count() != 2 {
		t.Fatalf("清空前应有 2 条草稿：%d", a.drafts.count())
	}
	n, err := a.clearAllDrafts()
	if err != nil || n != 2 {
		t.Fatalf("清空结果 n=%d err=%v", n, err)
	}
	if a.compose.title != "" || d.draft != "" || d.replyTo.ID != "" {
		t.Error("清空后 live 状态没清掉")
	}
	a.draftTick()
	a.flushDrafts()
	if a.drafts.count() != 0 {
		t.Errorf("清空后再 tick 又把草稿写回来了：%+v", a.drafts.rows())
	}
}

func TestDeleteDraftClearsLiveCompose(t *testing.T) {
	a := testApp(t)
	a.compose = newComposeState()
	a.compose.title = "只删这一条"
	a.flushDrafts()
	id := a.compose.draftID
	if id == "" {
		t.Fatal("flush 后应有 draftID")
	}
	a.deleteDraft(id)
	if a.drafts.count() != 0 {
		t.Errorf("删完箱子应该空了：%+v", a.drafts.rows())
	}
	a.draftTick()
	a.flushDrafts()
	if a.drafts.count() != 0 {
		t.Errorf("删完又复活了：%+v", a.drafts.rows())
	}
}

func TestComposeDraftSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false
	st := newComposeState()
	st.title = "重启后要还在"
	st.body = "正文内容"
	st.nodeID = "node-uuid"
	a.compose = st
	a.flushDrafts()

	restarted := newApp(dir)
	restarted.autoLoad = false
	e, ok := restarted.drafts.latestCompose(composeModePost)
	if !ok {
		t.Fatal("重启后草稿箱里没有那条草稿")
	}
	fresh := newComposeState()
	restarted.applyComposeDraft(fresh, e)
	if fresh.title != st.title || fresh.body != st.body || fresh.nodeID != st.nodeID {
		t.Fatalf("恢复出来的字段不对：%+v", fresh)
	}
}

func TestReplyDraftSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false
	d := &detailState{shortID: "abc123", draft: "回复正文", replyTo: replyTarget{
		ID: "cmt-88", Level: 2, ParentID: "cmt-12", Floor: 12, Author: "老王", AuthorID: "u-9",
	}}
	a.detail = d
	a.flushDrafts()

	restarted := newApp(dir)
	restarted.autoLoad = false
	e, ok := restarted.drafts.find(replyDraftID("abc123"))
	if !ok {
		t.Fatal("重启后没有那条回复草稿")
	}
	got := &detailState{shortID: "abc123"}
	got.applyReplyDraft(e)
	if got.draft != d.draft || got.replyTo != d.replyTo {
		t.Fatalf("回复草稿恢复不对：draft=%q replyTo=%+v", got.draft, got.replyTo)
	}
	if got.replyTo.Floor != 12 || got.replyTo.Author != "老王" || !got.focusComposer {
		t.Errorf("楼层 / 作者 / 聚焦标记丢了：%+v", got)
	}
}

func TestFlushSkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false
	a.compose = newComposeState()
	a.compose.title = "只写一次"
	a.flushDrafts()
	path := a.drafts.path
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 改写文件为哨兵内容；若第二次 flush 真的写盘，哨兵会被覆盖。
	sentinel := []byte("SENTINEL")
	if err := os.WriteFile(path, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	a.draftTick()
	a.flushDrafts()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(sentinel) {
		t.Errorf("指纹没变却写了盘：\nbefore %q\nafter %q", before, after)
	}
}

func TestEmptyDraftDeletesEntry(t *testing.T) {
	a := testApp(t)
	a.compose = newComposeState()
	a.compose.title = "写点东西"
	a.flushDrafts()
	if a.drafts.count() != 1 {
		t.Fatalf("应有 1 条草稿：%d", a.drafts.count())
	}
	a.compose.title = ""
	a.compose.body = ""
	a.flushDrafts()
	if a.drafts.count() != 0 {
		t.Errorf("清空正文后条目该消失：%+v", a.drafts.rows())
	}
}

func TestComposeDraftNeedsTitleOrBody(t *testing.T) {
	st := newComposeState()
	st.nodeID = "node-uuid"
	st.anonymous = true
	st.voteOpen = true
	st.voteTitle = "存哪"
	st.voteOptions = []string{"甲", "乙"}
	if !draftEntryEmpty(st.draftEntry()) {
		t.Fatal("只选了节点、匿名和投票，不该算草稿")
	}
	st.title = "   "
	if !draftEntryEmpty(st.draftEntry()) {
		t.Fatal("只有空白标题，不该算草稿")
	}
	st.title = "有标题"
	if draftEntryEmpty(st.draftEntry()) {
		t.Fatal("有标题就算草稿")
	}
	st.title = ""
	st.body = "有正文"
	if draftEntryEmpty(st.draftEntry()) {
		t.Fatal("有正文就算草稿")
	}
	st.body = "![图](upload:1)"
	if !draftEntryEmpty(st.draftEntry()) {
		t.Fatal("只有未完成的上传占位符，不该算草稿")
	}
	st.mode = composeModeMoment
	st.momentDraft = "动态正文"
	if draftEntryEmpty(st.draftEntry()) {
		t.Fatal("动态有正文就算草稿")
	}
}

func TestReplyDraftNeedsText(t *testing.T) {
	d := &detailState{shortID: "abc123", replyTo: replyTarget{ID: "cmt-88", Floor: 12, Author: "老王"}}
	if !draftEntryEmpty(d.draftEntry()) {
		t.Fatal("只点了「回复」没写正文，不该算草稿")
	}
	d.draft = "  \n\t "
	if !draftEntryEmpty(d.draftEntry()) {
		t.Fatal("只有空白正文，不该算草稿")
	}
	d.draft = "![](upload:7)"
	if !draftEntryEmpty(d.draftEntry()) {
		t.Fatal("只有未完成的上传占位符，不该算草稿")
	}
	d.draft = "回复 12 楼：确实如此"
	if draftEntryEmpty(d.draftEntry()) {
		t.Fatal("有正文就算草稿")
	}
}

func TestFlushSkipsReplyDraftWithoutText(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false
	a.detail = &detailState{shortID: "abc123", replyTo: replyTarget{
		ID: "cmt-88", Level: 2, ParentID: "cmt-12", Floor: 12, Author: "老王", AuthorID: "u-9",
	}}
	// 点「回复」会改变 replyTo 指纹并触发自动保存，但空正文不该落盘。
	a.flushDrafts()
	if a.drafts.count() != 0 {
		t.Fatalf("只点回复不该落盘：count=%d rows=%+v", a.drafts.count(), a.drafts.rows())
	}
	a.detail.draft = "写点正文"
	a.flushDrafts()
	if a.drafts.count() != 1 {
		t.Fatalf("写了正文后该落盘：count=%d", a.drafts.count())
	}
	if e, ok := a.drafts.find(replyDraftID("abc123")); !ok || e.Text != "写点正文" || e.ReplyTo.ID != "cmt-88" {
		t.Fatalf("回复草稿内容 / 目标不对：%+v ok=%v", e, ok)
	}
	// 清空正文但保留回复目标，条目也要跟着删掉。
	a.detail.draft = ""
	a.flushDrafts()
	if a.drafts.count() != 0 {
		t.Fatalf("正文清空后条目该消失：count=%d", a.drafts.count())
	}
}

func TestLegacyEmptyReplyDroppedOnLoad(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	empty := testDraftEntry(draftKindReply, now)
	empty.Text, empty.ReplyTo = "", draftTarget{ID: "cmt-88", Floor: 12, Author: "老王"}
	real := testDraftEntry(draftKindReply, now.Add(-time.Hour))
	path := filepath.Join(dir, "drafts", "local.json")
	if err := writeJSONFile(path, &draftFile{
		Version: draftVersion, Account: "local",
		Entries: []draftEntry{empty, real},
	}); err != nil {
		t.Fatal(err)
	}

	s := newDraftStore(dir, "local")
	if s.count() != 1 {
		t.Fatalf("旧版空回复草稿不该再出现在箱子里：%+v", s.rows())
	}
	if _, ok := s.find(empty.ID); ok {
		t.Error("旧版空回复草稿没被过滤")
	}
	if _, ok := s.find(real.ID); !ok {
		t.Error("有正文的回复草稿不该被过滤")
	}
}

func TestFlushSkipsComposeDraftWithoutText(t *testing.T) {
	a := testApp(t)
	a.compose = newComposeState()
	a.compose.nodeID = "node-uuid"
	a.compose.guardOpen = true
	a.flushDrafts()
	if a.drafts.count() != 0 || a.compose.draftID != "" {
		t.Fatalf("没有标题和正文时不该落盘：count=%d id=%q", a.drafts.count(), a.compose.draftID)
	}
	a.compose.title = "终于写了标题"
	a.flushDrafts()
	if a.drafts.count() != 1 || a.compose.draftID == "" {
		t.Fatalf("写了标题后该落盘：count=%d id=%q", a.drafts.count(), a.compose.draftID)
	}
}

func TestLegacyEmptyComposeDroppedOnLoad(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	empty := testDraftEntry(draftKindCompose, now)
	empty.Title, empty.Body = "", ""
	empty.Modules = &composeDraftModules{Vote: &composeDraftVote{Title: "投票"}}
	real := testDraftEntry(draftKindCompose, now.Add(-time.Hour))
	reply := testDraftEntry(draftKindReply, now.Add(-2*time.Hour))
	path := filepath.Join(dir, "drafts", "local.json")
	if err := writeJSONFile(path, &draftFile{
		Version: draftVersion, Account: "local",
		Entries: []draftEntry{empty, real, reply},
	}); err != nil {
		t.Fatal(err)
	}

	s := newDraftStore(dir, "local")
	if s.count() != 2 {
		t.Fatalf("旧版空草稿不该再出现在箱子里：%+v", s.rows())
	}
	if _, ok := s.find(empty.ID); ok {
		t.Error("旧版空草稿没被过滤")
	}
	if _, ok := s.find(reply.ID); !ok {
		t.Error("回复草稿不该被一起过滤")
	}
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	reopened := newDraftStore(dir, "local")
	if _, ok := reopened.find(empty.ID); ok {
		t.Error("旧版空草稿又被写回磁盘")
	}
	if reopened.count() != 2 {
		t.Fatalf("正常草稿不该被误删：%+v", reopened.rows())
	}
}

func TestDraftCounts(t *testing.T) {
	rows := []draftEntry{
		{Kind: draftKindCompose, Mode: composeModePost},
		{Kind: draftKindCompose, Mode: composeModeMoment},
		{Kind: draftKindCompose, Mode: composeModeMoment},
		{Kind: draftKindReply},
		{Kind: draftKindReply},
		{Kind: draftKindReply},
	}
	c, m, r := draftCounts(rows)
	if c != 1 || m != 2 || r != 3 {
		t.Fatalf("分类计数错了：%d/%d/%d", c, m, r)
	}
}

func TestStripUploadPlaceholdersKeepsOrdinaryMarkdown(t *testing.T) {
	in := "![头像](https://example.com/a.png) 和 **粗体**"
	if got := stripUploadPlaceholders(in); !strings.Contains(got, "https://example.com/a.png") ||
		!strings.Contains(got, "**粗体**") {
		t.Fatalf("普通 Markdown 被误伤：%q", got)
	}
}
