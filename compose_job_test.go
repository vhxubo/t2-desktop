package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 前缀拼法与网页端一致：片段按节点、类型、职位、薪资、远程排序，用 / 连接。
func TestComposeJobPrefix(t *testing.T) {
	cases := []struct {
		name string
		st   composeState
		want string
	}{
		{"只职位", composeState{jobPosition: "后端"}, "【后端】"},
		{"职位加薪资", composeState{jobPosition: "后端", jobSalary: "25k"}, "【后端/25k】"},
		{"全填", composeState{
			jobNode: jobNodeHunting, jobType: jobTypeFullTime,
			jobPosition: "后端", jobSalary: "25k", jobRemote: true,
		}, "【求职/全职/后端/25k/远程】"},
		{"只远程", composeState{jobRemote: true}, "【远程】"},
		{"只节点类型", composeState{jobNode: jobNodeRecruit}, "【招人啦】"},
		{"全空", composeState{}, ""},
		{"未知取值按网页端兜底", composeState{jobNode: "other", jobType: "other"}, "【招人啦/兼职】"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.st
			if got := composeJobPrefix(&st); got != tc.want {
				t.Errorf("前缀 = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// 循环剥掉叠加的旧前缀；作者自己写的【求职者】不能误删；点评前缀保留。
func TestComposeStripJobPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"【求职/全职】【招人啦/后端】标题", "标题"},
		{"【求职者】标题", "【求职者】标题"},
		{"《X》点评：8 分｜【求职/全职】标题", "《X》点评：8 分｜标题"},
		{"【投票】【求职/全职】标题", "【投票】标题"},
		{"普通标题", "普通标题"},
	}
	for _, tc := range cases {
		if got := composeStripJobPrefix(tc.in); got != tc.want {
			t.Errorf("剥前缀(%q) = %q，期望 %q", tc.in, got, tc.want)
		}
	}
}

// 工作前缀始终在最前：系统标签挪过前缀位置后也要重建回正确顺序。
func TestComposeApplyJobTitleOrder(t *testing.T) {
	st := composeState{
		jobOpen: true, jobNode: jobNodeHunting, jobType: jobTypeFullTime,
		jobPosition: "后端",
		title:       "【求职/全职/后端】【投票】原标题",
	}
	if got, want := composeApplyJobTitle(&st), "【求职/全职/后端】【投票】原标题"; got != want {
		t.Fatalf("标题顺序 = %q，期望 %q", got, want)
	}
	// 加了投票之后，composeSetTitleTag 会把工作前缀挪到【投票】后面。
	st.title = "【投票】【求职/全职/后端】原标题"
	if got, want := composeApplyJobTitle(&st), "【求职/全职/后端】【投票】原标题"; got != want {
		t.Fatalf("系统标签在前时没重新排好：%q，期望 %q", got, want)
	}
	// 点评前缀在工作前缀之后、系统标签之前。
	st.title = "求职标题《X》点评：8 分｜【投票】正文"
	st.jobNode, st.jobType, st.jobPosition = jobNodeHunting, jobTypeFullTime, ""
	if got, want := composeApplyJobTitle(&st), "【求职/全职】求职标题《X》点评：8 分｜【投票】正文"; got != want {
		t.Fatalf("点评前缀位置不对：%q，期望 %q", got, want)
	}
	// 移除组件后前缀消失，其余前缀保留。
	st.jobOpen = false
	st.title = "【求职/全职】【投票】标题"
	if got, want := composeApplyJobTitle(&st), "【投票】标题"; got != want {
		t.Fatalf("移除后没剥掉工作前缀：%q，期望 %q", got, want)
	}
	// 只填远程时前缀不以「求职」开头，也要能按当前值精确剥掉。
	st.jobOpen, st.jobNode, st.jobType = true, "", ""
	st.jobRemote, st.title = true, "【远程】标题"
	if got, want := composeApplyJobTitle(&st), "【远程】标题"; got != want {
		t.Fatalf("只填远程的前缀处理不对：%q，期望 %q", got, want)
	}
}

// 联系方式走文档的 contact 字段；不新增 job / work_content 之类的请求体字段。
func TestComposeJobPayload(t *testing.T) {
	st := newComposeState()
	st.title = "【求职/全职/后端/25k/远程】招聘后端"
	st.jobOpen, st.jobNode, st.jobType = true, jobNodeHunting, jobTypeFullTime
	st.jobPosition, st.jobSalary, st.jobRemote = "后端", "25k", true
	st.jobContact = "微信：dev-123"
	p, err := st.payload()
	if err != nil {
		t.Fatal(err)
	}
	assertField(t, p, "title", "【求职/全职/后端/25k/远程】招聘后端")
	assertField(t, p, "title_prefix", "【求职/全职/后端/25k/远程】")
	assertField(t, p, "contact", "微信：dev-123")
	for k := range p {
		if strings.HasPrefix(k, "job") || k == "work_content" {
			t.Errorf("请求体出现了文档里没有的字段 %q", k)
		}
	}
}

func TestComposeJobContactLimit(t *testing.T) {
	st := newComposeState()
	st.title, st.jobOpen = "标题", true
	st.jobContact = strings.Repeat("联", composeJobContactMax+1)
	_, err := st.payload()
	if err == nil || err.Error() != "联系方式最多 500 字" {
		t.Fatalf("超长联系方式的校验 = %v，期望「联系方式最多 500 字」", err)
	}
}

// 匿名发帖允许使用工作内容（网页端允许），开关不撤工作前缀。
func TestComposeJobAnonymousKeepsPrefix(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.title = "【求职/全职】招聘"
	st.jobOpen, st.jobNode, st.jobType = true, jobNodeHunting, jobTypeFullTime
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if err := tt.Click("匿名发帖"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !st.anonymous {
		t.Fatal("匿名开关没生效")
	}
	if !st.jobOpen || st.title != "【求职/全职】招聘" {
		t.Errorf("匿名后工作状态不对：open=%v title=%q", st.jobOpen, st.title)
	}
}

// 草稿保存 / 恢复六个字段，并在恢复时重建标题前缀；旧草稿没有 job 字段时正常。
func TestComposeJobDraftRoundTrip(t *testing.T) {
	a := testApp(t)
	st := newComposeState()
	st.title = "【求职/全职/后端/25k/远程】招聘"
	st.jobOpen, st.jobNode, st.jobType = true, jobNodeHunting, jobTypeFullTime
	st.jobPosition, st.jobSalary, st.jobRemote, st.jobContact = "后端", "25k", true, "wx:123"
	e := st.draftEntry()
	if e.Modules == nil || e.Modules.Job == nil {
		t.Fatalf("草稿没写入工作内容：%+v", e.Modules)
	}
	probe := *st
	probe.jobContact = "wx:456"
	if st.draftFingerprint() == probe.draftFingerprint() {
		t.Fatal("草稿指纹没覆盖工作内容字段")
	}

	got := newComposeState()
	a.applyComposeDraft(got, e)
	if !got.jobOpen || got.jobNode != st.jobNode || got.jobType != st.jobType ||
		got.jobPosition != st.jobPosition || got.jobSalary != st.jobSalary ||
		got.jobRemote != st.jobRemote || got.jobContact != st.jobContact {
		t.Fatalf("草稿恢复的字段不对：%+v", got)
	}
	if got.title != st.title {
		t.Errorf("恢复后标题 = %q，期望 %q", got.title, st.title)
	}

	// 老草稿：标题里的工作前缀原样保留，但不会启用组件。
	old := e
	old.Modules = &composeDraftModules{}
	old.Title = "【求职/全职】旧草稿"
	legacy := newComposeState()
	a.applyComposeDraft(legacy, old)
	if legacy.jobOpen || legacy.title != "【求职/全职】旧草稿" {
		t.Errorf("旧草稿恢复不对：open=%v title=%q", legacy.jobOpen, legacy.title)
	}
}

// 侧栏里「工作内容」排在投票之前，点开弹窗能看到全部字段与预览行。
func TestComposeJobModuleDialogFields(t *testing.T) {
	a := testApp(t)
	composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	job, ok := tt.Find("工作内容")
	if !ok {
		t.Fatalf("侧栏没有工作内容：%q", tt.Texts())
	}
	vote, ok := tt.Find("投票")
	if !ok || job.Y >= vote.Y {
		t.Fatalf("工作内容没排在投票之前：job=%+v vote=%+v", job, vote)
	}
	if err := tt.Click("工作内容"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, want := range []string{
		"节点类型", "工作类型", "职位", "薪资", "支持远程", "联系方式", "不填",
	} {
		if !tt.HasText(want) {
			t.Errorf("工作内容弹窗缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 在弹窗里填求职 / 全职 / 后端 / 25k / 远程，预览与完成后的标题都要带前缀。
func TestComposeJobDialogBuildsPrefix(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.title = "原标题"
	st.voteOpen = true // 已有投票组件时，工作前缀仍要在投票标签之前
	st.title = "【投票】原标题"
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if err := tt.Click("工作内容"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("求职"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("全职"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	jobType := func(label, text string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatalf("点不到 %q：%v（%q）", label, err, tt.Texts())
		}
		tt.Type(text)
		tt.Frame()
	}
	jobType("职位", "后端")
	jobType("薪资", "25k")
	if err := tt.Click("支持远程"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("【求职/全职/后端/25k/远程】") {
		t.Fatalf("预览没有反映填写内容：%q", tt.Texts())
	}
	if err := tt.Click("完成"); err != nil {
		t.Fatal(err)
	}
	if !st.jobOpen || st.jobNode != jobNodeHunting || st.jobType != jobTypeFullTime ||
		st.jobPosition != "后端" || st.jobSalary != "25k" || !st.jobRemote {
		t.Fatalf("完成后字段没落账：%+v", st)
	}
	if want := "【求职/全职/后端/25k/远程】【投票】原标题"; st.title != want {
		t.Errorf("标题 = %q，期望 %q", st.title, want)
	}
	// 移除组件只剥前缀，六个字段还留在内存里，再次打开弹窗仍在。
	tt.Frame()
	if err := tt.Click("移除工作内容"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st.jobOpen || st.title != "【投票】原标题" {
		t.Errorf("移除后状态不对：open=%v title=%q", st.jobOpen, st.title)
	}
	if err := tt.Click("工作内容"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st.jobPosition != "后端" || st.jobSalary != "25k" || !st.jobRemote {
		t.Errorf("再次打开弹窗后字段丢了：%+v", st)
	}
	if !tt.HasText("【求职/全职/后端/25k/远程】") {
		t.Errorf("再次打开弹窗没有预览：%q", tt.Texts())
	}
}

// 六项全空点「完成」：toast 提示未填写，行仍是「设置」。
func TestComposeJobEmptyValidation(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.title = "标题"
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if err := tt.Click("工作内容"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("完成"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st.jobOpen {
		t.Fatal("全空时仍然启用了工作内容")
	}
	if !tt.HasText("未填写内容") {
		t.Errorf("没有提示未填写内容：%q", tt.Texts())
	}
	if !tt.HasText("已添加 0 / 8 项") {
		t.Errorf("侧栏行数不对：%q", tt.Texts())
	}
}

// 当前节点是求职时，打开弹窗预选「求职」。
func TestComposeJobPresetNode(t *testing.T) {
	a := testApp(t)
	st := composeReady(a)
	st.nodes = append(st.nodes, composeNode{
		ID: "uuid-job", Slug: jobNodeHunting, Name: "求职", ParentSlug: "career", ParentName: "职业发展",
	})
	st.nodeID = "uuid-job"
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()
	if err := tt.Click("工作内容"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st.jobNode != jobNodeHunting {
		t.Fatalf("没有按当前节点预选节点类型：%q", st.jobNode)
	}
	if !tt.HasText("【求职】") {
		t.Errorf("预选后预览不对：%q", tt.Texts())
	}
}

// POST /v1/topics 的请求体：标题带前缀、title_prefix 等于前缀、contact 按文档提交。
func TestComposeJobPublishRequest(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/topics" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("请求体不是 JSON：%v", err)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, envelopeJSON(`{"short_id":"Job123"}`))
	}))
	defer srv.Close()

	a := loginApp(t, srv)
	st := composeReady(a)
	st.title = "【求职/全职/后端】招聘"
	st.jobOpen, st.jobNode, st.jobType = true, jobNodeHunting, jobTypeFullTime
	st.jobPosition, st.jobContact = "后端", "wx:dev"
	var done sync.WaitGroup
	done.Add(1)
	a.onPostPublished = func(created createdTopic, err error) {
		if err != nil {
			t.Errorf("发表失败：%v", err)
		}
		done.Done()
	}
	a.submitCompose()
	done.Wait()

	assertField(t, gotBody, "title", "【求职/全职/后端】招聘")
	assertField(t, gotBody, "title_prefix", "【求职/全职/后端】")
	assertField(t, gotBody, "contact", "wx:dev")
	for k := range gotBody {
		if strings.HasPrefix(k, "job") || k == "work_content" {
			t.Errorf("请求体出现了文档里没有的字段 %q", k)
		}
	}
}
