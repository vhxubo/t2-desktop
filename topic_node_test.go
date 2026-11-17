package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// AI 自动分区：不选节点发帖后，帖子先落在星云，详情页与发帖回执都要等到
// 服务端把 node.slug 改成正式节点。这里锁住判断、文案与轮询结果的落地。

func TestPendingNodeDetection(t *testing.T) {
	cases := []struct {
		slug string
		want bool
	}{
		{"nebula", true},
		{"NEBULA", true},
		{" nebula ", true},
		{"work", false},
		{"", false},
	}
	for _, c := range cases {
		topic := &topicDetail{NodeSlug: c.slug}
		if got := topic.pendingNode(); got != c.want {
			t.Errorf("pendingNode(%q) = %v，期望 %v", c.slug, got, c.want)
		}
	}
	if (*topicDetail)(nil).pendingNode() {
		t.Error("nil 帖子不应当成待分配")
	}

	pending := &topicDetail{NodeSlug: nebulaNodeSlug, NodeName: "星云"}
	if got := pending.categoryPath(); got != "节点寻找中…" {
		t.Errorf("待分配分类 = %q", got)
	}
	done := &topicDetail{NodeSlug: "work", NodeName: "职场"}
	if got := done.categoryPath(); got != "职场" {
		t.Errorf("已分配分类 = %q", got)
	}
}

func TestNodeWatchBackoff(t *testing.T) {
	if got := nodeWatchBackoff(0); got != nodeWatchFirstDelay {
		t.Errorf("第 0 次退避 = %v", got)
	}
	if got := nodeWatchBackoff(1); got != nodeWatchFirstDelay {
		t.Errorf("第 1 次退避 = %v", got)
	}
	if got := nodeWatchBackoff(3); got != 6*time.Second {
		t.Errorf("第 3 次退避 = %v", got)
	}
	if got := nodeWatchBackoff(99); got != nodeWatchMaxDelay {
		t.Errorf("退避没有封顶：%v", got)
	}
}

func TestNodeAssignedText(t *testing.T) {
	cases := map[[2]string]string{
		{"职场", "work"}: "节点已分配：职场",
		{"", "work"}:   "节点已分配：work",
		{"", ""}:       "节点已分配",
		{"  ", "  "}:   "节点已分配",
	}
	for in, want := range cases {
		if got := nodeAssignedText(in[0], in[1]); got != want {
			t.Errorf("nodeAssignedText(%q, %q) = %q，期望 %q", in[0], in[1], got, want)
		}
	}
}

// 轮询到正式节点后，详情页的节点、原文链接与本地缓存一起更新，并报告结果。
func TestProbeDetailNodeAppliesAssignedNode(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = fmt.Fprint(w, envelopeJSON(`{
			"short_id":"N1","title":"等分类的帖子","content":"正文",
			"node":{"id":"n-work","slug":"work","name":"职场","parent_slug":"community","parent":{"slug":"community","name":"社区"}}
		}`))
	}))
	defer srv.Close()
	a := signedInApp(t, srv.URL)
	d := &detailState{
		shortID: "N1",
		topic:   &topicDetail{ShortID: "N1", Title: "等分类的帖子", NodeSlug: nebulaNodeSlug, NodeName: "星云"},
	}
	a.detail = d
	d.nodeWatchGen = 7

	a.probeDetailNode(d, 7)
	waitFor(t, func() bool { return !d.topic.pendingNode() }, "节点分配结果没有写回")

	if hits != 1 {
		t.Errorf("轮询请求次数 = %d，期望 1", hits)
	}
	if d.topic.NodeSlug != "work" || d.topic.NodeName != "职场" || d.topic.NodeID != "n-work" {
		t.Errorf("节点没有更新：%+v", d.topic)
	}
	if d.topic.NodeParentName != "社区" || d.topic.NodeParentSlug != "community" {
		t.Errorf("父节点没有更新：%+v", d.topic)
	}
	if !strings.Contains(d.topic.URL, "/post/work/N1") {
		t.Errorf("原文链接没有换成正式地址：%q", d.topic.URL)
	}
	if !queuedToast(a, "节点已分配：职场") {
		t.Error("没有报告节点分配结果")
	}
}

// 还在星云时继续排队，不把「等分类」当成失败；测试直接停掉定时器，避免等
// 退避时间。
func TestProbeDetailNodeKeepsWaitingWhileNebula(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, envelopeJSON(`{
			"short_id":"N1","title":"等分类的帖子","node":{"slug":"nebula","name":"星云"}
		}`))
	}))
	defer srv.Close()
	a := signedInApp(t, srv.URL)
	d := &detailState{shortID: "N1", topic: &topicDetail{ShortID: "N1", NodeSlug: nebulaNodeSlug}}
	a.detail = d
	d.nodeWatchGen = 3

	a.probeDetailNode(d, 3)
	waitFor(t, func() bool { return d.nodeWatchTimer != nil }, "还在星云时没有排下一次轮询")
	if !d.topic.pendingNode() {
		t.Fatal("服务端仍回星云，却被当成已分配")
	}
	a.stopDetailNodeWatch(d)
	if d.nodeWatchTimer != nil {
		t.Error("停止后定时器没有清掉")
	}
	a.toastMu.Lock()
	n := len(a.toasts)
	a.toastMu.Unlock()
	if n != 0 {
		t.Errorf("等分类期间不该弹提示：%v", a.toasts)
	}
}

// 分好节点后发帖回执里的原文链接也要更新：用户点「看原文」不能还停在
// /post/nebula/。
func TestApplyComposeNodeUpdatesReceipt(t *testing.T) {
	a := testApp(t)
	st := newComposeState()
	st.okShortID = "N1"
	st.okTitle = "等分类的帖子"
	st.okPending = true
	st.okURL = topicURL("N1", nebulaNodeSlug)
	a.compose = st

	a.applyComposeNode(st, &topicDetail{
		ShortID: "N1", NodeSlug: "work", NodeName: "职场", URL: topicURL("N1", "work"),
	})
	if st.okPending {
		t.Error("节点分好后回执还在「分配中」")
	}
	if st.okURL != topicURL("N1", "work") {
		t.Errorf("回执链接 = %q", st.okURL)
	}

	// 旧帖子的迟到结果不能改到新回执上。
	st.okShortID = "N2"
	st.okPending = true
	st.okURL = topicURL("N2", nebulaNodeSlug)
	a.applyComposeNode(st, &topicDetail{
		ShortID: "N1", NodeSlug: "work", NodeName: "职场", URL: topicURL("N1", "work"),
	})
	if !st.okPending || st.okURL != topicURL("N2", nebulaNodeSlug) {
		t.Errorf("旧帖子的结果覆盖了新回执：%+v", st)
	}
}

func TestComposeNodePending(t *testing.T) {
	cases := []struct {
		created    createdTopic
		nodeChosen bool
		want       bool
	}{
		{createdTopic{NodeSlug: nebulaNodeSlug}, false, true},
		{createdTopic{NodeSlug: nebulaNodeSlug}, true, true},
		{createdTopic{NodeSlug: ""}, false, true},
		{createdTopic{NodeSlug: ""}, true, false},
		{createdTopic{NodeSlug: "work"}, false, false},
	}
	for _, c := range cases {
		if got := composeNodePending(c.created, c.nodeChosen); got != c.want {
			t.Errorf("composeNodePending(%+v, %v) = %v，期望 %v", c.created, c.nodeChosen, got, c.want)
		}
	}
}

func TestComposeReceiptText(t *testing.T) {
	st := &composeState{okTitle: "标题"}
	if got := composeReceiptText(st); got != "已发表：标题" {
		t.Errorf("普通回执 = %q", got)
	}
	st.okPending = true
	if got := composeReceiptText(st); got != "已发表：标题（节点分配中…）" {
		t.Errorf("待分配回执 = %q", got)
	}
}

// 详情页显示「节点寻找中…」与解释，而不是把「星云」当成正式分类。
func TestPendingNodeDetailShowsWaiting(t *testing.T) {
	a := testApp(t)
	a.pageName = "detail"
	a.detail = &detailState{
		shortID:     "N1",
		replyLoaded: true,
		topic: &topicDetail{
			ShortID: "N1", Title: "等分类的帖子", Content: "正文",
			NodeSlug: nebulaNodeSlug, NodeName: "星云",
		},
	}
	tt := ui.NewTester(a.view, 1200, 800)
	tt.Frame()
	if !tt.HasText("节点寻找中…") {
		t.Errorf("没有显示节点待定：%q", tt.Texts())
	}
	if !tt.HasText("节点由 AI 自动分配，通常几秒后完成。") {
		t.Errorf("没有解释节点为什么在等：%q", tt.Texts())
	}
	if tt.HasText("星云") {
		t.Errorf("不该把星云当正式分类显示：%q", tt.Texts())
	}
}
