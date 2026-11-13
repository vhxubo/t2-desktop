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

// uploadTestApp 把图库换成一台 httptest 通用图床，省掉登录态与真实网络。
func uploadTestApp(t *testing.T, handler http.HandlerFunc) (*app, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	a := testApp(t)
	a.client.HTTP = srv.Client()
	a.hosts = imageHostConfig{
		Active: "test",
		Hosts: []imageHost{{
			ID: "test", Name: "测试图床", Kind: imageHostGeneric,
			Endpoint: srv.URL, Field: "file", URLPath: "data.url",
			MaxBytes: defaultImageMaxBytes, Enabled: true,
		}},
	}
	a.hostActiveLabel = "测试图床"
	a.compose = newComposeState()
	return a, srv
}

func uploadOKHandler(w http.ResponseWriter, r *http.Request) {
	_, _ = fmt.Fprint(w, `{"data":{"url":"https://img.test/photo.png"}}`)
}

// waitUpload 收一条任务完成通知。上传在后台 goroutine 跑，结果经 publish
// 回到 UI 线程；无头环境下 publish 同步执行，channel 就是天然的路障。
func waitUpload(t *testing.T, ch <-chan uploadJob) uploadJob {
	t.Helper()
	select {
	case j := <-ch:
		return j
	case <-time.After(5 * time.Second):
		t.Fatal("等上传结果超时")
		return uploadJob{}
	}
}

func TestEnqueueUploadsReplacesPlaceholder(t *testing.T) {
	a, _ := uploadTestApp(t, uploadOKHandler)
	done := make(chan uploadJob, 2)
	a.onUploadDone = func(j uploadJob) { done <- j }
	p := writeTestImage(t, "photo.png", pngHeadForUpload)

	if n := a.enqueueUploads(uploadTargetCompose, []uploadSource{{Path: p, Name: "photo.png"}}); n != 1 {
		t.Fatalf("入队数量 = %d", n)
	}
	if !strings.Contains(a.compose.body, "](upload:1)") {
		t.Fatalf("正文里应有占位符：%q", a.compose.body)
	}
	if got := editorPendingUploads(a.compose.body); got != 1 {
		t.Errorf("未完成图片数 = %d", got)
	}
	a.compose.title = "标题"
	if _, err := a.compose.payload(); err == nil ||
		!strings.Contains(err.Error(), "未处理完") {
		t.Errorf("发布前应拦住未传完的图片：%v", err)
	}
	// 上传中的占位符在预览里要换成中文说明，不能变成坏链接。
	if got := a.editorPreviewSource(a.compose.body, nil); !strings.Contains(got, "正在上传图片") || strings.Contains(got, "upload:") {
		t.Errorf("预览源文 = %q", got)
	}

	j := waitUpload(t, done)
	if j.State != uploadDone || j.URL != "https://img.test/photo.png" {
		t.Fatalf("任务状态 = %+v", j)
	}
	if !strings.Contains(a.compose.body, "![photo.png](https://img.test/photo.png)") ||
		strings.Contains(a.compose.body, "upload:") {
		t.Errorf("占位符没换成真实地址：%q", a.compose.body)
	}
	if got := editorPendingUploads(a.compose.body); got != 0 {
		t.Errorf("上传完成后仍有 %d 个占位符", got)
	}
	if _, err := a.compose.payload(); err != nil {
		t.Errorf("上传完成后应允许发布：%v", err)
	}
	// 已完成任务若正文里还残留占位符，预览应直接用真实地址。
	if got := a.editorPreviewSource("![photo.png](upload:1)", nil); !strings.Contains(got, "https://img.test/photo.png") {
		t.Errorf("已完成任务的残留占位符没被替换：%q", got)
	}
}

func TestEnqueueUploadsSkipsBadFiles(t *testing.T) {
	a, _ := uploadTestApp(t, uploadOKHandler)
	done := make(chan uploadJob, 2)
	a.onUploadDone = func(j uploadJob) { done <- j }
	good := writeTestImage(t, "good.png", pngHeadForUpload)
	bad := writeTestImage(t, "bad.png", []byte("this is not an image"))

	n := a.enqueueUploads(uploadTargetCompose, []uploadSource{
		{Path: good, Name: "good.png"},
		{Path: bad, Name: "bad.png"},
	})
	if n != 1 {
		t.Fatalf("只有一张合法图应入队，实际 %d", n)
	}
	if !strings.Contains(a.compose.body, "good.png") || strings.Contains(a.compose.body, "bad.png") {
		t.Errorf("占位符不该包含被拒绝的文件：%q", a.compose.body)
	}
	if !hasToastContaining(a, "只支持 JPEG") {
		t.Errorf("坏文件应有原因提示：%v", toastTexts(a))
	}
	waitUpload(t, done)
}

// hasToastContaining 与 interact_test.go 的 hasToast（精确匹配）区分开。
func hasToastContaining(a *app, part string) bool {
	for _, s := range toastTexts(a) {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}

func toastTexts(a *app) []string {
	out := make([]string, 0, len(a.toasts))
	for _, n := range a.toasts {
		out = append(out, n.text)
	}
	return out
}

func TestEnqueueUploadsRequiresReadyHost(t *testing.T) {
	a := testApp(t)
	// 默认图库是免 Token 的 Kappa，本身就绪；切到需要登录的官方图床来
	// 验证「不就绪就不入队」。
	a.setActiveHostByLabel("2Libra 官方图床")
	a.compose = newComposeState()
	p := writeTestImage(t, "photo.png", pngHeadForUpload)
	if n := a.enqueueUploads(uploadTargetCompose, []uploadSource{{Path: p}}); n != 0 {
		t.Errorf("未登录时不应入队：%d", n)
	}
	if a.compose.body != "" {
		t.Errorf("未入队却改了正文：%q", a.compose.body)
	}
	if !hasToastContaining(a, "登录") {
		t.Errorf("应提示先登录：%v", toastTexts(a))
	}
	if len(a.uploads) != 0 {
		t.Errorf("队列里不该有任务：%d", len(a.uploads))
	}
}

func TestFailedUploadCanBeRemovedOrRetried(t *testing.T) {
	fail := true
	a, _ := uploadTestApp(t, func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"message":"disk full"}`)
			return
		}
		uploadOKHandler(w, r)
	})
	done := make(chan uploadJob, 4)
	a.onUploadDone = func(j uploadJob) { done <- j }
	p := writeTestImage(t, "photo.png", pngHeadForUpload)

	if n := a.enqueueUploads(uploadTargetCompose, []uploadSource{{Path: p, Name: "photo.png"}}); n != 1 {
		t.Fatalf("入队数量 = %d", n)
	}
	j := waitUpload(t, done)
	if j.State != uploadFailed {
		t.Fatalf("第一次应失败：%+v", j)
	}
	if got := editorPendingUploads(a.compose.body); got != 1 {
		t.Errorf("失败后占位符应保留：%d", got)
	}
	if got := a.editorPreviewSource(a.compose.body, nil); !strings.Contains(got, "图片上传失败") || strings.Contains(got, "upload:") {
		t.Errorf("失败预览源文 = %q", got)
	}
	if got := a.pendingUploadCount(); got != 1 {
		t.Errorf("待处理任务数 = %d", got)
	}

	// 重试：同一张图、同一个占位符，不重复插入正文。
	fail = false
	a.retryUpload(j.ID)
	j2 := waitUpload(t, done)
	if j2.State != uploadDone {
		t.Fatalf("重试应成功：%+v", j2)
	}
	if strings.Contains(a.compose.body, "upload:") ||
		!strings.Contains(a.compose.body, "https://img.test/photo.png") {
		t.Errorf("重试后正文 = %q", a.compose.body)
	}
	if len(strings.Split(a.compose.body, "![photo.png]")) != 2 {
		t.Errorf("重试不该重复插入图片：%q", a.compose.body)
	}

	// 移除：正文里的占位符与队列里的任务一起清掉。
	a.uploads = []*uploadJob{{ID: 77, Placeholder: "![gone.png](upload:77)",
		Name: "gone.png", State: uploadFailed, Target: uploadTargetCompose}}
	a.compose.body = "前文\n\n![gone.png](upload:77)\n\n后文"
	a.removeUpload(77)
	if strings.Contains(a.compose.body, "gone.png") || len(a.uploads) != 0 {
		t.Errorf("移除失败：body=%q jobs=%d", a.compose.body, len(a.uploads))
	}
	if !strings.Contains(a.compose.body, "前文") || !strings.Contains(a.compose.body, "后文") {
		t.Errorf("移除时误删了正文：%q", a.compose.body)
	}
}

func TestDragDropIntoComposeEditor(t *testing.T) {
	a, _ := uploadTestApp(t, uploadOKHandler)
	done := make(chan uploadJob, 2)
	a.onUploadDone = func(j uploadJob) { done <- j }
	composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()

	r, ok := tt.Find("正文内容")
	if !ok {
		t.Fatalf("找不到正文编辑区：%q", tt.Texts())
	}
	p := writeTestImage(t, "dropped.png", pngHeadForUpload)
	if !tt.DropFiles(r.X+r.W/2, r.Y+r.H/2, p) {
		t.Fatal("拖放事件没有被编辑区接收")
	}
	if !strings.Contains(a.compose.body, "dropped.png") {
		t.Fatalf("拖入后正文应出现占位符：%q", a.compose.body)
	}
	j := waitUpload(t, done)
	if j.State != uploadDone || !strings.Contains(a.compose.body, "https://img.test/photo.png") {
		t.Errorf("拖放上传没走完：%+v body=%q", j, a.compose.body)
	}
}

func TestPickImagesUsesTestHook(t *testing.T) {
	a, _ := uploadTestApp(t, uploadOKHandler)
	done := make(chan uploadJob, 2)
	a.onUploadDone = func(j uploadJob) { done <- j }
	p := writeTestImage(t, "picked.png", pngHeadForUpload)
	a.pickImagePaths = func() ([]string, error) { return []string{p}, nil }

	if n := a.pickImages(uploadTargetCompose); n != 1 {
		t.Fatalf("选择图片应入队 1 张，实际 %d", n)
	}
	if !strings.Contains(a.compose.body, "picked.png") {
		t.Errorf("正文 = %q", a.compose.body)
	}
	waitUpload(t, done)
}

func TestUploadPlaceholderEscapesMarkdown(t *testing.T) {
	got := uploadPlaceholder(9, "a[b](c)\n.png")
	if strings.Contains(got, "[") && strings.Count(got, "[") != 1 {
		t.Errorf("alt 里的方括号会破坏 Markdown：%q", got)
	}
	if strings.ContainsAny(got, "\n\r") || !strings.HasSuffix(got, "(upload:9)") {
		t.Errorf("占位符格式不对：%q", got)
	}
	if got := uploadPlaceholder(1, "  "); !strings.HasPrefix(got, "![图片]") {
		t.Errorf("空文件名应有兜底 alt：%q", got)
	}
}

// 表情面板插入的是短码，预览才换成图片；社区自带的图标要叠在全局表上。
func TestEditorEmojiPanelAndPreview(t *testing.T) {
	a := testApp(t)
	a.emojis = decodeEmojis([]byte(detailEmojisJSON))
	composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()

	if err := tt.Click("表情"); err != nil {
		t.Fatalf("找不到表情入口：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	if !a.compose.emojiOpen || a.compose.imageOpen {
		t.Fatal("表情面板应与图片面板互斥")
	}
	if !tt.HasText(":sigh:") || !tt.HasText("搜索表情") {
		t.Errorf("表情面板没画出全局表情：%q", tt.Texts())
	}
	if err := tt.Click("叹气"); err != nil {
		t.Fatalf("找不到表情单元：%v：%q", err, tt.Texts())
	}
	tt.Frame()
	if !strings.Contains(a.compose.body, ":sigh:") {
		t.Errorf("点击表情应插入短码：%q", a.compose.body)
	}

	// 过滤：只看本帖表情时，全局短码不出现。
	community := map[string]string{":party:": "/emojis/party.png"}
	list := editorEmojis(a.emojis, community)
	if got := editorFilteredEmojis(list, "", true, editorCommunityCodes(community)); len(got) != 1 || got[0].Code != ":party:" {
		t.Errorf("只看本帖表情过滤失败：%+v", got)
	}
	if got := editorFilteredEmojis(list, "sigh", false, nil); len(got) != 1 || got[0].Code != ":sigh:" {
		t.Errorf("按短码搜索失败：%+v", got)
	}
	merged := mergeEmojiMaps(a.emojis, community)
	if got := replaceEmoji("来 :party: 一下", merged, true); !strings.Contains(got, "https://r2.2libra.com/emojis/party.png") {
		t.Errorf("社区表情相对地址没补成完整 URL：%q", got)
	}
	if got := a.editorPreviewSource("来 :sigh: 一下", merged); !strings.Contains(got, "https://r2.2libra.com/emojis/sigh.png") {
		t.Errorf("预览没把短码换成图片：%q", got)
	}
}

func TestReplyEmojiPanelUsesPostEmojis(t *testing.T) {
	a := detailAppWithReplies(t)
	a.emojis = decodeEmojis([]byte(detailEmojisJSON))
	d := a.detail
	if d == nil {
		t.Fatal("详情状态为空")
	}
	d.topic = &topicDetail{Emojis: map[string]string{":party:": "/emojis/party.png"}}
	if got := a.replyCommunityEmojis(d); got[":party:"] == "" {
		t.Error("评论面板没拿到帖子自带表情")
	}
	merged := a.replyEmojis(d)
	if _, ok := merged[":sigh:"]; !ok {
		t.Error("评论预览丢了全局表情")
	}
	if got := a.editorPreviewSource("hi :party:", merged); !strings.Contains(got, "r2.2libra.com/emojis/party.png") {
		t.Errorf("评论预览没渲染社区表情：%q", got)
	}
}

func TestSettingsWindowShowsImageHostsSection(t *testing.T) {
	a := testApp(t)
	// 图库 section 排在图片缓存后面，用高一点的无头窗口让按钮落在可视区内。
	tt := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1600)
	tt.Frame()
	for _, want := range []string{"图库（图床）", "当前使用", "添加自定义图库", "恢复默认", "保存图库设置"} {
		if !tt.HasText(want) {
			t.Errorf("设置窗口缺少 %q：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("添加自定义图库"); err != nil {
		t.Fatalf("添加按钮不可用：%v", err)
	}
	tt.Frame()
	if !containsString(a.allHostNames(), "我的图库 1") {
		t.Errorf("自定义图库没加进列表：%v", a.allHostNames())
	}
}

// 同一个队列要能同时服务发帖与评论两份正文，互不串台。
func TestEnqueueUploadsToReplyDraft(t *testing.T) {
	a, _ := uploadTestApp(t, uploadOKHandler)
	done := make(chan uploadJob, 2)
	a.onUploadDone = func(j uploadJob) { done <- j }
	detail := detailAppWithReplies(t)
	detail.client.HTTP = a.client.HTTP
	detail.hosts = a.hosts
	detail.hostActiveLabel = a.hostActiveLabel
	detail.onUploadDone = a.onUploadDone
	detail.compose = a.compose
	detail.detail.draft = ""

	p := writeTestImage(t, "reply.png", pngHeadForUpload)
	if n := detail.enqueueUploads(uploadTargetReply,
		[]uploadSource{{Path: p, Name: "reply.png"}}); n != 1 {
		t.Fatalf("评论入队数量 = %d", n)
	}
	if !strings.Contains(detail.detail.draft, "reply.png") {
		t.Errorf("占位符应落在评论草稿里：%q", detail.detail.draft)
	}
	if detail.compose.body != "" {
		t.Errorf("发帖正文被误改：%q", detail.compose.body)
	}
	if got := detail.uploadsFor(uploadTargetCompose); len(got) != 0 {
		t.Errorf("任务被挂到了错误的编辑器：%d", len(got))
	}
	if got := detail.uploadsFor(uploadTargetReply); len(got) != 1 {
		t.Errorf("评论任务列表 = %d", len(got))
	}
	j := waitUpload(t, done)
	if j.Target != uploadTargetReply || j.State != uploadDone {
		t.Fatalf("任务状态 = %+v", j)
	}
	if !strings.Contains(detail.detail.draft, "![reply.png](https://img.test/photo.png)") {
		t.Errorf("评论草稿 = %q", detail.detail.draft)
	}
}

func TestEnqueueUploadsWithoutEditor(t *testing.T) {
	a, _ := uploadTestApp(t, uploadOKHandler)
	a.compose = nil
	p := writeTestImage(t, "photo.png", pngHeadForUpload)
	if n := a.enqueueUploads(uploadTargetCompose, []uploadSource{{Path: p}}); n != 0 {
		t.Errorf("没有发帖窗口时不应入队：%d", n)
	}
	if len(a.uploads) != 0 {
		t.Errorf("队列不该留任务：%d", len(a.uploads))
	}
}

// 后台任务跑完前用户就把这一张移除了：迟到的结果只应被忽略，不能
// 把正文改坏或 panic。
func TestLateUploadResultAfterRemovalIsIgnored(t *testing.T) {
	a, _ := uploadTestApp(t, uploadOKHandler)
	a.compose.body = "前文\n\n![gone.png](upload:5)\n\n后文"
	a.uploads = []*uploadJob{{ID: 5, Placeholder: "![gone.png](upload:5)",
		Name: "gone.png", State: uploadRunning, Target: uploadTargetCompose}}
	a.removeUpload(5)
	a.finishUpload(5, "https://img.test/late.png", nil)
	if strings.Contains(a.compose.body, "gone.png") || strings.Contains(a.compose.body, "late.png") {
		t.Errorf("迟到的结果改动了正文：%q", a.compose.body)
	}
	if len(a.uploads) != 0 {
		t.Errorf("任务不该被重新加回来：%d", len(a.uploads))
	}
}

// 通用图床那一半的编辑表单（Password / NumberInput / Select / TextArea）也要
// 能画出来，官方图床分支覆盖不到这些控件。
func TestSettingsWindowRendersGenericHostEditor(t *testing.T) {
	a := testApp(t)
	a.hostEditLabel = "SM.MS"
	tt := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1600)
	tt.Frame()
	for _, want := range []string{"上传接口地址", "文件字段名", "返回 URL 路径",
		"请求头（一行一个）", "Token（替换上面的 {{token}}）",
		"附加表单字段（一行一个 name=value）", "响应体直接就是图片地址", "单张上限"} {
		if !tt.HasText(want) {
			t.Errorf("通用图床表单缺少 %q：%q", want, tt.Texts())
		}
	}
}

// 官方图床只对 Pro 开放：设置里要按当前账号的状态给出不同说明。
func TestSettingsWindowOfficialHostProNote(t *testing.T) {
	a := testApp(t)
	a.hostEditLabel = "2Libra 官方图床"
	tt := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1600)
	tt.Frame()
	if !tt.HasText("只对 Pro 会员开放") {
		t.Errorf("没说明 Pro 门槛：%q", tt.Texts())
	}
	if !tt.HasText("当前未登录") {
		t.Errorf("未登录时没说明会先确认 Pro 状态：%q", tt.Texts())
	}

	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", Username: "jimmy", TokenType: "Bearer",
	}); err != nil {
		t.Fatal(err)
	}
	a.auth.setProStatus(false)
	tt.Frame()
	if !tt.HasText("当前账号不是 Pro") {
		t.Errorf("非 Pro 账号没看到提示：%q", tt.Texts())
	}
	a.auth.setProStatus(true)
	tt.Frame()
	if !tt.HasText("当前账号是 Pro") {
		t.Errorf("Pro 账号没看到提示：%q", tt.Texts())
	}
}

// submitReply 与 composeState.payload 都要拦住未完成上传，不能把
// ![…](upload:ID) 发到服务端。
func TestSubmitReplyBlocksPendingUploads(t *testing.T) {
	a := detailAppWithReplies(t)
	d := a.detail
	d.draft = "一张图 ![x.png](upload:42)"
	before := len(a.toasts)
	a.submitReply()
	if d.draft != "一张图 ![x.png](upload:42)" {
		t.Errorf("被拦下时草稿不该被清空：%q", d.draft)
	}
	if len(a.toasts) <= before || !hasToastContaining(a, "未处理完") {
		t.Errorf("应提示还有图片未处理完：%v", toastTexts(a))
	}
}
