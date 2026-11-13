package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
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

// uploadGate 把上传接口卡住，直到 open。无头环境下 publish 直接在 worker
// 协程里跑回调，上传若在构建帧的同一时刻完成，占位符替换会和这一帧读正文撞
// 在一起（-race 会报）；卡一下就能先把「占位符进了正文」断言完，再放开。
type uploadGate struct {
	ch   chan struct{}
	once sync.Once
}

func newUploadGate(t *testing.T) *uploadGate {
	t.Helper()
	g := &uploadGate{ch: make(chan struct{})}
	// 测试中途 t.Fatal 时也要放开，否则 httptest.Server.Close 会等这个请求。
	t.Cleanup(g.open)
	return g
}

func (g *uploadGate) open() { g.once.Do(func() { close(g.ch) }) }

// gatedUploadHandler 是卡在闸门后面的 uploadOKHandler。
func (g *uploadGate) handler(w http.ResponseWriter, r *http.Request) {
	<-g.ch
	uploadOKHandler(w, r)
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

// 直接往正文里粘一张图：截图工具复制出来的剪贴板只有图片、没有文字，编辑器
// 应当自己认出这是图片，走图床队列，正文里先留占位符、传完换成真实地址。
func TestPasteImageIntoComposeEditor(t *testing.T) {
	gate := newUploadGate(t)
	a, _ := uploadTestApp(t, gate.handler)
	done := make(chan uploadJob, 2)
	a.onUploadDone = func(j uploadJob) { done <- j }
	composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()

	if err := tt.Click("正文内容"); err != nil {
		t.Fatalf("点正文输入框失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	tt.SetClipboardData(transfer.New(transfer.NewItem(
		transfer.Bytes(transfer.PNG, pngHeadForUpload))))
	tt.Key(ui.Cmd, ui.KeyV)
	tt.Frame()

	if !strings.Contains(a.compose.body, "![剪贴板图片.png](upload:") {
		t.Fatalf("粘贴图片后正文应有占位符：%q toasts=%v", a.compose.body, toastTexts(a))
	}
	if len(a.uploads) != 1 {
		t.Errorf("队列里的任务数 = %d", len(a.uploads))
	}
	gate.open()
	j := waitUpload(t, done)
	if j.State != uploadDone || j.Target != uploadTargetCompose {
		t.Fatalf("任务状态 = %+v", j)
	}
	tt.Frame()
	if strings.Contains(a.compose.body, "upload:") ||
		!strings.Contains(a.compose.body, "https://img.test/photo.png") {
		t.Errorf("占位符没换成真实地址：%q", a.compose.body)
	}
}

// 粘文字还是粘文字：只有剪贴板里没有文字时才当图片处理，否则从网页上复制
// 图片带出来的那段文字（标题或地址）会莫名其妙变成一次上传。
func TestPasteTextIntoComposeEditorStaysText(t *testing.T) {
	a, _ := uploadTestApp(t, uploadOKHandler)
	composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()

	if err := tt.Click("正文内容"); err != nil {
		t.Fatalf("点正文输入框失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	tt.SetClipboard("一段普通文字")
	tt.Key(ui.Cmd, ui.KeyV)
	tt.Frame()

	if a.compose.body != "一段普通文字" {
		t.Errorf("正文 = %q", a.compose.body)
	}
	if len(a.uploads) != 0 {
		t.Errorf("文字粘贴不该入队：%d", len(a.uploads))
	}
}

// 资源管理器 / 访达复制文件时，剪贴板上的文字就是那个路径。整段文字是一个
// 存在的图片文件时按图片处理，别把路径原样插进正文。
func TestPasteImageFilePathUploadsInsteadOfInsertingPath(t *testing.T) {
	gate := newUploadGate(t)
	a, _ := uploadTestApp(t, gate.handler)
	done := make(chan uploadJob, 2)
	a.onUploadDone = func(j uploadJob) { done <- j }
	composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()

	if err := tt.Click("正文内容"); err != nil {
		t.Fatalf("点正文输入框失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	p := writeTestImage(t, "copied.png", pngHeadForUpload)
	tt.SetClipboard(p)
	tt.Key(ui.Cmd, ui.KeyV)
	tt.Frame()

	if strings.Contains(a.compose.body, p) {
		t.Errorf("路径被当正文插进去了：%q", a.compose.body)
	}
	if !strings.Contains(a.compose.body, "![copied.png](upload:") {
		t.Fatalf("正文里应有这张图的占位符：%q", a.compose.body)
	}
	gate.open()
	j := waitUpload(t, done)
	if j.State != uploadDone {
		t.Fatalf("任务状态 = %+v", j)
	}
}

// 访达复制图片文件时，剪贴板上的文字只有文件名，完整路径在文件列表里。
// 这种情况同样要认成图片，而不是把文件名插进正文。
func TestPasteImageFileNamedOnlyByText(t *testing.T) {
	gate := newUploadGate(t)
	a, _ := uploadTestApp(t, gate.handler)
	done := make(chan uploadJob, 2)
	a.onUploadDone = func(j uploadJob) { done <- j }
	composeReady(a)
	tt := ui.NewTester(a.composeWindowView, composeWindowWidth, 900)
	tt.Frame()

	if err := tt.Click("正文内容"); err != nil {
		t.Fatalf("点正文输入框失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	p := writeTestImage(t, "finder.png", pngHeadForUpload)
	d, err := transfer.FileData(p)
	if err != nil {
		t.Fatal(err)
	}
	// 文件列表 + 只有文件名的文字，和访达复制出来的剪贴板一样。
	tt.SetClipboardData(transfer.New(append(d.Items(),
		transfer.NewItem(transfer.Bytes(transfer.Text, []byte(filepath.Base(p)))))...))
	tt.Key(ui.Cmd, ui.KeyV)
	tt.Frame()

	if !strings.Contains(a.compose.body, "![finder.png](upload:") {
		t.Fatalf("正文里应有这张图的占位符：%q", a.compose.body)
	}
	gate.open()
	if j := waitUpload(t, done); j.State != uploadDone {
		t.Fatalf("任务状态 = %+v", j)
	}
}

// 评论输入框挂的是同一个钩子，任务要落在评论草稿里，不能串到发帖正文。
func TestPasteImageIntoReplyComposer(t *testing.T) {
	gate := newUploadGate(t)
	a, _ := uploadTestApp(t, gate.handler)
	done := make(chan uploadJob, 2)
	detail := detailAppWithReplies(t)
	detail.client.HTTP = a.client.HTTP
	detail.hosts = a.hosts
	detail.hostActiveLabel = a.hostActiveLabel
	detail.onUploadDone = func(j uploadJob) { done <- j }
	detail.compose = a.compose
	detail.detail.draft = ""

	tt := ui.NewTester(detail.view, 1100, 760)
	tt.Frame()
	if err := tt.Click("写评论"); err != nil {
		t.Fatalf("点评论输入框失败：%v（%q）", err, tt.Texts())
	}
	tt.Frame()
	tt.SetClipboardData(transfer.New(transfer.NewItem(
		transfer.Bytes(transfer.PNG, pngHeadForUpload))))
	tt.Key(ui.Cmd, ui.KeyV)
	tt.Frame()

	if !strings.Contains(detail.detail.draft, "![剪贴板图片.png](upload:") {
		t.Fatalf("评论草稿里应有占位符：%q", detail.detail.draft)
	}
	if detail.compose.body != "" {
		t.Errorf("发帖正文被误改：%q", detail.compose.body)
	}
	gate.open()
	j := waitUpload(t, done)
	if j.Target != uploadTargetReply || j.State != uploadDone {
		t.Fatalf("任务状态 = %+v", j)
	}
	tt.Frame()
	if !strings.Contains(detail.detail.draft, "https://img.test/photo.png") ||
		strings.Contains(detail.detail.draft, "upload:") {
		t.Errorf("评论草稿没换成真实地址：%q", detail.detail.draft)
	}
}

func TestClipboardImagePath(t *testing.T) {
	p := writeTestImage(t, "shot.PNG", pngHeadForUpload) // 扩展名大写也要认
	if got := clipboardImagePath(p); got != p {
		t.Errorf("裸路径 = %q，应为 %q", got, p)
	}
	if got := clipboardImagePath(`"` + p + `"`); got != p {
		t.Errorf("带引号的路径 = %q，应为 %q", got, p)
	}
	u := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(p)}).String()
	if got := clipboardImagePath(u); got != p {
		t.Errorf("file:// URI = %q，应为 %q", got, p)
	}

	for _, text := range []string{
		"",
		"一段普通文字",
		"https://example.com/a.png",            // 网络地址不是本地文件
		filepath.Join(t.TempDir(), "gone.png"), // 文件不存在
		writeTestImage(t, "note.txt", []byte("hi")), // 不是图片扩展名
		p + "\n第二行", // 多行不是路径
	} {
		if got := clipboardImagePath(text); got != "" {
			t.Errorf("%q 不该认成图片路径，得到 %q", text, got)
		}
	}
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

// 内置协议的图床画的是另一套字段：StarDots 要 Key / Secret / 存储桶，Imgur
// 可以填自己的 Client-ID，其余几家只说明「不用填 Token」，不该露出通用的
// 字段名 / URL 路径这些输入框。
func TestSettingsWindowRendersAdapterHostEditor(t *testing.T) {
	cases := []struct {
		label string
		want  []string
	}{
		{"StarDots", []string{"上传接口地址（留空用内置地址）", "API Key",
			"API Secret", "存储桶（Bucket）"}},
		{"Imgur", []string{"Client-ID（留空用内置公共 ID）"}},
		{"MJJ.Today（免 Token）", []string{"这条内置协议不用填 Token"}},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			a := testApp(t)
			a.hostEditLabel = c.label
			tt := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1600)
			tt.Frame()
			for _, want := range c.want {
				if !tt.HasText(want) {
					t.Errorf("内置协议表单缺少 %q：%q", want, tt.Texts())
				}
			}
			for _, unwanted := range []string{"文件字段名", "返回 URL 路径", "附加表单字段"} {
				if tt.HasText(unwanted) {
					t.Errorf("内置协议不该显示 %q：%q", unwanted, tt.Texts())
				}
			}
		})
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
