package main

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// 图片上传队列。
//
// 拖拽、文件选择、剪贴板三条入口最后都落到 enqueueUploads：立刻在正文里
// 插入 ![名字](upload:ID) 占位符，再把任务排进串行的后台队列。占位符有
// 两个作用——用户马上看到「图放在这里了」，发布前也能据此拦住还没传完的
// 草稿（editorPendingUploads）。上传成功后换成真实 URL，失败则保留占位符
// 并给出重试 / 移除，绝不会把临时标记发到服务端。
//
// 其中剪贴板那条还分成两个动作：面板里的「粘贴剪贴板图片」按钮（pasteImages），
// 以及直接往输入框里按 Cmd/Ctrl+V（pasteIntoEditor，挂在 TextArea 的
// OnPaste 上，粘的是图片就自动上传）。
//
// a.uploads 只在 UI 线程读写；后台 goroutine 只拿一份任务的快照，结果一律
// 经 a.publish 回到 UI 线程。Window.Update 是异步的，取下一个任务时用一个
// 带缓冲的 channel 把值带回来，不能假设 publish 的回调已经同步执行完。

type uploadState int

const (
	uploadQueued uploadState = iota
	uploadRunning
	uploadDone
	uploadFailed
)

// uploadTarget 指出占位符插在哪一份正文里。用枚举而不是 *string：发帖草稿
// 会原地重置、详情切帖时整个 detailState 会被换掉，存指针会指向过期状态。
type uploadTarget int

const (
	uploadTargetCompose uploadTarget = iota
	uploadTargetReply
)

type uploadJob struct {
	ID          uint64
	Placeholder string
	Name        string
	State       uploadState
	URL         string
	Err         string
	Target      uploadTarget
	Host        imageHost
	Source      uploadSource
}

// editorState 找到目标编辑器当前的正文与共用状态。窗口关掉 / 换了帖子时
// 返回 nil，调用方直接放弃这次操作，不写进过期的字符串。
func (a *app) editorState(t uploadTarget) (*string, *mdEditor) {
	switch t {
	case uploadTargetCompose:
		if a.compose == nil {
			return nil, nil
		}
		return &a.compose.body, &a.compose.mdEditor
	case uploadTargetReply:
		if a.detail == nil {
			return nil, nil
		}
		return &a.detail.draft, &a.detail.mdEditor
	}
	return nil, nil
}

func (a *app) editorToastTarget(t uploadTarget) toastTarget {
	if t == uploadTargetCompose {
		return toastCompose
	}
	return toastMain
}

// uploadImageAlt 把文件名清成能安全写进 Markdown 链接文字与 URL 里的样子：
// 方括号、圆括号和换行都会破坏 ![]() 结构。
func uploadImageAlt(name string) string {
	name = strings.TrimSpace(name)
	name = strings.NewReplacer("[", "", "]", "", "(", "", ")", "",
		"\r", " ", "\n", " ").Replace(name)
	if name == "" {
		name = "图片"
	}
	return name
}

func uploadPlaceholder(id uint64, name string) string {
	return "![" + uploadImageAlt(name) + "](upload:" + strconv.FormatUint(id, 10) + ")"
}

func (a *app) uploadByID(id uint64) *uploadJob {
	for _, j := range a.uploads {
		if j.ID == id {
			return j
		}
	}
	return nil
}

// ------------------------------------------------------------------
// 入队
// ------------------------------------------------------------------

// hostNotReadyReason 把「图库不能用」翻译成能照着做的一句话，避免只弹
// 一句「上传失败」让用户去猜是没登录、没启用、不是 Pro 还是没填 Token。
func (a *app) hostNotReadyReason(h *imageHost) string {
	switch {
	case h == nil:
		return "还没有可用的图库，请到「设置 → 图库」里选一个"
	case !h.Enabled:
		return "图库「" + h.Name + "」未启用，请到「设置 → 图库」里开启"
	case h.Kind == imageHostOfficial:
		if a.auth == nil || !a.auth.loggedIn() {
			return "上传图片需要先登录 2Libra"
		}
		pro, known := a.proStatus()
		switch {
		case !known:
			return "还没确认你的 2Libra Pro 状态，请稍后重试；也可以到「设置 → 图库」换用免 Token 图床"
		case !pro:
			return "2Libra 官方图床只对 Pro 会员开放，请到「设置 → 图库」换用免 Token 图床"
		default:
			return "图库「" + h.Name + "」暂时不可用，请到「设置 → 图库」里检查"
		}
	case h.Adapter == adapterStarDots:
		return "图库「" + h.Name + "」需要 StarDots 的 API Key 与 API Secret，请到「设置 → 图库」里填写"
	case h.Adapter != "":
		return "图库「" + h.Name + "」暂时不可用，请到「设置 → 图库」里检查"
	case strings.TrimSpace(h.Endpoint) == "":
		return "图库「" + h.Name + "」还没填接口地址，请到「设置 → 图库」里配置"
	default:
		return "图库「" + h.Name + "」需要 Token，请到「设置 → 图库」里填写"
	}
}

// enqueueUploads 校验每张图并插入占位符，返回真正排进队列的张数。
// 一次拖多张时只有一张格式不对也照样上传其余的，坏的那几张合并成一条提示。
func (a *app) enqueueUploads(t uploadTarget, sources []uploadSource) int {
	body, e := a.editorState(t)
	if body == nil || len(sources) == 0 {
		return 0
	}
	host := a.activeHost()
	if !a.hostReady(host) {
		// 官方图床的 Pro 状态还没读到时顺手补一次：确认完再拖一次就能用。
		if host != nil && host.Kind == imageHostOfficial && a.auth != nil && a.auth.loggedIn() {
			if _, known := a.proStatus(); !known {
				a.refreshProStatus(true)
			}
		}
		a.toast(a.editorToastTarget(t), a.hostNotReadyReason(host))
		return 0
	}
	h := *host
	var texts []string
	var jobs []*uploadJob
	var bad []string
	for _, src := range sources {
		checked, err := inspectImage(src, h.MaxBytes)
		if err != nil {
			bad = append(bad, src.Name+"："+err.Error())
			continue
		}
		a.uploadSeq++
		j := &uploadJob{
			ID:          a.uploadSeq,
			Placeholder: uploadPlaceholder(a.uploadSeq, checked.Name),
			Name:        checked.Name,
			State:       uploadQueued,
			Target:      t,
			Host:        h,
			Source:      checked,
		}
		jobs = append(jobs, j)
		if len(texts) > 0 {
			texts = append(texts, "\n\n") // 图片各自独占一行
		}
		texts = append(texts, j.Placeholder)
	}
	if len(jobs) > 0 {
		// 预览模式不画输入框，选区读不到，用户也看不见刚插进来的占位符。
		e.preview = 0
		e.insertMany(body, texts)
		a.uploads = append(a.uploads, jobs...)
		a.startUploadWorker()
	}
	if len(bad) > 0 {
		a.toast(a.editorToastTarget(t), strings.Join(bad, "；"))
	}
	return len(jobs)
}

func (a *app) enqueueDroppedFiles(t uploadTarget, paths []string) int {
	var out []uploadSource
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "file://") {
			// file:// 交给系统剪贴板那一路处理；这里只收本地路径。
			continue
		}
		out = append(out, uploadSource{Path: p, Name: filepath.Base(p)})
	}
	return a.enqueueUploads(t, out)
}

// pickImages 打开系统文件选择框。headless 测试没有原生对话框，用
// a.pickImagePaths 换成直接返回几个路径。
func (a *app) pickImages(t uploadTarget) int {
	var paths []string
	var err error
	if a.pickImagePaths != nil {
		paths, err = a.pickImagePaths()
	} else {
		parent := a.win
		if t == uploadTargetCompose && a.composeWin != nil {
			parent = a.composeWin
		}
		paths, err = mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:      parent,
			Title:       "选择要插入的图片",
			ButtonLabel: "插入",
			Message:     "支持 JPEG / PNG / WebP；单张上限由图库设置决定。",
			Filters: []mygo.FileFilter{{
				Name:      "图片",
				MIMETypes: []string{"image/jpeg", "image/png", "image/webp"},
			}},
			Multiple: true,
		})
	}
	if err != nil {
		a.toast(a.editorToastTarget(t), "选择图片失败："+err.Error())
		return 0
	}
	return a.enqueueDroppedFiles(t, paths)
}

// clipboardImageSources 先看剪贴板列出了哪些格式，再只读取需要的那几种：
// 复制的是图片文件就走路径，直接从截图工具复制的就取 PNG 字节。
func (a *app) clipboardImageSources() []uploadSource {
	held := a.srv.ClipboardFormats()
	if len(held) == 0 {
		return nil
	}
	seen := make(map[transfer.Format]bool, 3)
	var formats []transfer.Format
	for _, f := range held {
		if f != transfer.FileList && f != transfer.URIList && f != transfer.PNG {
			continue
		}
		if !seen[f] {
			seen[f] = true
			formats = append(formats, f)
		}
	}
	if len(formats) == 0 {
		return nil
	}
	d := a.srv.ReadClipboardData(formats...)
	var out []uploadSource
	if paths, err := d.Files(); err == nil {
		for _, p := range paths {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, uploadSource{Path: p, Name: filepath.Base(p)})
			}
		}
	}
	if len(out) == 0 {
		if b, err := d.Read(transfer.PNG); err == nil && len(b) > 0 {
			out = append(out, uploadSource{Name: "剪贴板图片.png", Data: b})
		}
	}
	return out
}

func (a *app) pasteImages(t uploadTarget) int {
	srcs := a.clipboardImageSources()
	if len(srcs) == 0 {
		a.toast(a.editorToastTarget(t), "剪贴板里没有图片；先复制一张图片或图片文件再点这里")
		return 0
	}
	return a.enqueueUploads(t, srcs)
}

// pendingPaste 是一次还没落地的图片粘贴：粘到了哪个编辑器、粘的是什么。
type pendingPaste struct {
	target  uploadTarget
	sources []uploadSource
}

// pasteIntoEditor 是正文输入框的粘贴钩子（TextArea 的 OnPaste）：粘进来的
// 是图片就吞掉这次粘贴，改走图床队列；是文字就返回 false，让输入框照常插入。
//
// 判断按顺序来：
//   - 整段文字就是一个存在的本地图片文件路径（资源管理器 / 访达复制文件时
//     剪贴板上的文字就是它），或者一个指向图片的 file:// URI，按图片处理；
//   - 其余有文字的情况一律当文字粘贴：从网页上复制图片常常同时带一段文字
//     （标题或图片地址），那时用户要的多半是那段文字；
//   - 没有文字时才去读剪贴板里的图片数据——截图工具直接复制的就是这种，
//     MyGo 的 OnPaste 也正是在这种时候给出空字符串。
//
// 这里只登记、不动正文：回调跑在输入框的事件处理里，此刻改绑定的正文会被
// 输入框自己持有的文本写回去（见 TextKey.SetText 的说明），占位符当场就没了。
// 真正的插入交给目标编辑器下一次构建时的 drainPaste，路径与拖放完全一样。
//
// 返回 true 表示这次粘贴由我们接管：输入框的选区与撤销历史都不动，正文里
// 随后出现的是 ![名字](upload:ID) 占位符。
func (a *app) pasteIntoEditor(t uploadTarget, text string) bool {
	srcs := a.pastedImageSources(text)
	if len(srcs) == 0 {
		return false
	}
	a.pendingPaste = &pendingPaste{target: t, sources: srcs}
	return true
}

// pastedImageSources 从这次粘贴里认出图片：先看文字是不是图片文件路径，再在
// 没有文字时读剪贴板里的图片数据。
func (a *app) pastedImageSources(text string) []uploadSource {
	if p := clipboardImagePath(text); p != "" {
		return []uploadSource{{Path: p, Name: filepath.Base(p)}}
	}
	if strings.TrimSpace(text) != "" {
		// 访达复制文件时，剪贴板上的文字只有文件名，完整路径在文件列表里。
		if p := a.clipboardImageFileNamed(text); p != "" {
			return []uploadSource{{Path: p, Name: filepath.Base(p)}}
		}
		return nil
	}
	return a.clipboardImageSources()
}

// clipboardImageFileNamed 在剪贴板只带着一个文件、且它的名字正是这段文字时
// 返回它的路径。只有这种情况才把文字当成图片：否则「正文里恰好写着某个图片
// 文件名」也会被当成一次上传。
func (a *app) clipboardImageFileNamed(text string) string {
	name := strings.TrimSpace(text)
	if name == "" || strings.ContainsAny(name, "\r\n") {
		return ""
	}
	held := a.srv.ClipboardFormats()
	files := false
	for _, f := range held {
		if f == transfer.FileList || f == transfer.URIList {
			files = true
			break
		}
	}
	if !files {
		return ""
	}
	paths, err := a.srv.ReadClipboardData(transfer.FileList, transfer.URIList).Files()
	if err != nil || len(paths) != 1 {
		return ""
	}
	if filepath.Base(paths[0]) != name || !isImageFileName(paths[0]) {
		return ""
	}
	return paths[0]
}

// drainPaste 在目标编辑器构建时领走刚粘进来的图片。调用点必须在构造输入框
// 之前：和拖放一样，正文要在这一帧的输入框读到之前改好。图库没就绪、图片
// 不合格都走 enqueueUploads 里现成的提示。
func (a *app) drainPaste(t uploadTarget) {
	p := a.pendingPaste
	if p == nil || p.target != t {
		return
	}
	a.pendingPaste = nil
	a.enqueueUploads(t, p.sources)
}

// clipboardImagePath 把一段粘贴文字认成图片文件路径。剪贴板上只有文字时，
// 「这段文字本身就是一个存在的图片文件」比「把路径原样插进正文」更像用户的
// 本意。认不出来就返回空串，交回普通的文字粘贴。
func clipboardImagePath(text string) string {
	s := strings.TrimSpace(text)
	if s == "" || strings.ContainsAny(s, "\r\n") {
		return ""
	}
	// Windows 复制带空格的路径时会加一对引号。
	if len(s) > 2 && strings.HasPrefix(s, `"`) && strings.HasSuffix(s, `"`) {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	if u, err := url.Parse(s); err == nil && u.Scheme == "file" {
		p := u.Path
		if len(p) > 3 && p[0] == '/' && p[2] == ':' {
			p = p[1:] // file:///C:/... 还原成 C:/...
		}
		s = filepath.FromSlash(p)
	} else if strings.Contains(s, "://") {
		return "" // 网络地址不是本地文件，交给文字粘贴
	}
	if !filepath.IsAbs(s) || !isImageFileName(s) {
		return ""
	}
	if info, err := os.Stat(s); err != nil || info.IsDir() {
		return ""
	}
	return s
}

// isImageFileName 只看扩展名，用来判断「这段文字像不像图片路径」；真正的
// 格式由 inspectImage 读文件头确认，扩展名撒谎的文件会被它拒掉。
func isImageFileName(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".jpg", ".jpeg", ".png", ".webp":
		return true
	}
	return false
}

// editorDropZone 把一个容器登记成拖放区，返回是否正被拖着文件悬停。
// DroppedFiles / FileDragOver 都要在 Children 之前调用：这一帧就得把
// flagDropTarget 记进节点，下一次 drop 事件才找得到它。
func (a *app) editorDropZone(zone ui.Element, t uploadTarget) bool {
	over := zone.FileDragOver()
	if files := zone.DroppedFiles(); len(files) > 0 {
		a.enqueueDroppedFiles(t, files)
	}
	return over
}

// ------------------------------------------------------------------
// 串行队列
// ------------------------------------------------------------------

// uploadTask 是后台 goroutine 拿到的任务快照。只有这三项是不可变的，
// 状态与结果始终由 UI 线程改写。
type uploadTask struct {
	id     uint64
	host   imageHost
	source uploadSource
}

func (a *app) startUploadWorker() {
	if a.uploadWorker {
		return
	}
	a.uploadWorker = true
	go a.runUploadQueue()
}

func (a *app) runUploadQueue() {
	for {
		task, ok := a.takeNextUpload()
		if !ok {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), imageUploadTimeout+5*time.Second)
		u, err := a.uploadImage(ctx, task.host, task.source)
		cancel()
		a.finishUpload(task.id, u, err)
	}
}

// takeNextUpload 在 UI 线程上把第一个待传任务标成 running。Window.Update
// 只是把回调投递到主线程，所以用一个容量为 1 的 channel 把任务带回来：
// headless 下 publish 同步执行，缓冲保证同样不会自锁。
func (a *app) takeNextUpload() (uploadTask, bool) {
	ch := make(chan uploadTask, 1)
	a.publish(func() {
		for _, j := range a.uploads {
			if j.State != uploadQueued {
				continue
			}
			j.State = uploadRunning
			ch <- uploadTask{id: j.ID, host: j.Host, source: j.Source}
			return
		}
		a.uploadWorker = false
		ch <- uploadTask{}
	})
	task := <-ch
	return task, task.id != 0
}

func (a *app) finishUpload(id uint64, url string, err error) {
	a.publish(func() {
		j := a.uploadByID(id)
		if j == nil {
			return // 用户已经把占位符连同任务一起删掉了
		}
		target := a.editorToastTarget(j.Target)
		if err == nil {
			j.State = uploadDone
			j.URL = url
			j.Err = ""
			a.replaceUploadPlaceholder(j, uploadRealMarkdown(j, url))
			a.toast(target, "图片已上传："+j.Name)
		} else {
			j.State = uploadFailed
			j.Err = friendlyError(err)
			// 官方图床缺 upload scope 时交给全局授权链路：补上权限后自动
			// 重传这一张，草稿与占位符都留在原地，不用重新拖一次。这种
			// 情况下只提示「等待授权」，不要再弹一条吓人的上传失败。
			if a.handleAuthError(err, func() { a.retryUpload(id) }) {
				a.toast(target, "上传图片需要额外授权，授权完成后会自动重传："+j.Name)
			} else {
				a.toast(target, "图片上传失败："+j.Err)
			}
		}
		a.notifyUpload(j)
	})
}

func (a *app) notifyUpload(j *uploadJob) {
	if a.onUploadDone != nil {
		a.onUploadDone(*j)
	}
}

// uploadRealMarkdown 只把占位符里的 upload:ID 换成真实地址，保留
// ![alt](...) 这段 Markdown 图片语法，否则上传完正文里只剩一个裸 URL。
func uploadRealMarkdown(j *uploadJob, url string) string {
	return strings.Replace(j.Placeholder,
		"upload:"+strconv.FormatUint(j.ID, 10), url, 1)
}

// replaceUploadPlaceholder 把正文里的占位符换成完整替换串（移除时换成空串），
// 并按长度差修正选区：URL 通常比 upload:ID 长，不修正的话光标会落在
// 后半段正文中间。
func (a *app) replaceUploadPlaceholder(j *uploadJob, replacement string) {
	body, e := a.editorState(j.Target)
	if body == nil {
		return
	}
	at := strings.Index(*body, j.Placeholder)
	if at < 0 {
		return
	}
	before := utf8.RuneCountInString((*body)[:at])
	oldLen := utf8.RuneCountInString(j.Placeholder)
	newLen := utf8.RuneCountInString(replacement)
	fix := func(p int) int {
		switch {
		case p <= before:
			return p
		case p <= before+oldLen:
			return before + newLen // 光标原本落在占位符里，就跟到替换内容末尾
		default:
			return p + newLen - oldLen
		}
	}
	*body = (*body)[:at] + replacement + (*body)[at+len(j.Placeholder):]
	n := utf8.RuneCountInString(*body)
	e.selStart = max(0, min(fix(e.selStart), n))
	e.selEnd = max(0, min(fix(e.selEnd), n))
	e.pendingSel = composeSelection{}
}

// retryUpload 让失败的任务回到队尾。授权补权限后的重跑也走这里。
func (a *app) retryUpload(id uint64) {
	j := a.uploadByID(id)
	if j == nil || j.State != uploadFailed {
		return
	}
	j.State = uploadQueued
	j.Err = ""
	a.startUploadWorker()
}

// removeUpload 把占位符从正文里抹掉并丢弃任务。上传中的任务也能删：
// 后台结果回来时 uploadByID 找不到它，会被直接忽略。
func (a *app) removeUpload(id uint64) {
	j := a.uploadByID(id)
	if j == nil {
		return
	}
	a.replaceUploadPlaceholder(j, "")
	for i, x := range a.uploads {
		if x.ID == id {
			a.uploads = append(a.uploads[:i], a.uploads[i+1:]...)
			return
		}
	}
}

// uploadsFor 取出属于某个编辑器的任务，图片面板只列自己这一边的。
func (a *app) uploadsFor(t uploadTarget) []*uploadJob {
	out := make([]*uploadJob, 0, len(a.uploads))
	for _, j := range a.uploads {
		if j.Target == t {
			out = append(out, j)
		}
	}
	return out
}

// pendingUploadCount 是界面上「还有 N 张图片」的统计：未完成 = 排队中、
// 上传中与失败待处理，发布 / 发送前都会被拦下。
func (a *app) pendingUploadCount() int {
	n := 0
	for _, j := range a.uploads {
		if j.State != uploadDone {
			n++
		}
	}
	return n
}

// setActiveHostByLabel 是编辑器里切换图库的下拉框动作：立即生效并顺手
// 落盘，不必专门点「保存」。落盘只改磁盘上那份已保存配置的 Active 字段，
// 不把设置窗口里填到一半、还没校验的地址和 Token 一起写进缓存。
// 设置窗口里的完整编辑见 image_hosts_ui.go。
func (a *app) setActiveHostByLabel(label string) {
	h := a.hostByLabel(label)
	if h == nil || !h.Enabled {
		return
	}
	a.hosts.Active = h.ID
	a.hostActiveLabel = h.Name
	if a.kv == nil {
		return
	}
	var saved imageHostConfig
	if _, ok := a.kv.get(imageHostKVKey, &saved); !ok {
		saved = defaultImageHostConfig()
	}
	saved.Active = h.ID
	_ = a.kv.put(imageHostKVKey, saved, time.Now())
}
