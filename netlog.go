package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

/* ------------------------------------------------------------------
   全局网络请求记录器（网络调试窗口的数据来源）
---------------------------------------------------------------- */

// 每个窗口发出的 HTTP 请求都经过 proxyManager 的共享 transport。
// netRecorder 在这层 transport 外再包一层 netLogTransport：请求发出时记下
// 方法、地址、请求头与请求体，响应回来后记下状态码、响应头，并在响应体被
// 读完时顺手截一段。所有记录放在进程级的环形缓冲里，网络调试窗口（见
// netlog_ui.go）随时取快照展示，类似 Chrome DevTools 的 Network 面板。
//
// 记录器默认开启：不开启就「打开窗口只看到打开之后的请求」，排查问题时
// 总是慢一步。上限与截断保证内存有界：最多 netLogMaxEntries 条，每条最多
// 记 netLogMaxBody 字节的请求 / 响应体。

const (
	// netLogMaxEntries 是环形缓冲的条数上限。超出后丢最旧的记录。
	netLogMaxEntries = 800
	// netLogMaxBody 是单条记录里请求体 / 响应体各自的最大字节数。超出部分
	// 截断并打上标记，界面会说明「已截断」。
	netLogMaxBody = 16 << 10
)

// netLogKind 是请求的分类，供调试窗口的过滤芯片使用。按「这行流量是谁的」
// 划分：2Libra 开放 API、图片（上传与下载）、其余（更新检查、第三方图床等）。
const (
	netLogKindAPI = iota
	netLogKindImage
	netLogKindOther
)

// netHeader 是调试窗口里展示的一行头：键 + 值。敏感头的值在记录时就脱敏。
type netHeader struct {
	Key   string
	Value string
}

// netEntry 是一次 HTTP 交互的完整记录。Done 为 false 时表示响应体还没读完
// （或 SSE 这类长连接还在流式传输），界面显示「进行中 / 流式」。
type netEntry struct {
	ID     int
	Start  time.Time
	Method string
	URL    string

	ReqHeaders       []netHeader
	ReqBody          string
	ReqBodyTruncated bool
	ReqBodyNote      string // 二进制等不适合展示的请求体说明

	Status      int // 0 表示没拿到 HTTP 响应（连接失败等）
	RespHeaders []netHeader
	RespCT      string // 响应 Content-Type，用于分类与展示判断
	RespBody    string
	RespTrunc   bool
	RespNote    string

	Elapsed time.Duration
	Err     string
	Done    bool
	Stream  bool // SSE 等长连接：响应体边流边记
}

// kind 返回这条记录的分类。响应头还没回来时先按 URL 猜：2Libra 的请求
// 立刻归为 API，其余等 Content-Type 到了再修正。
func (e *netEntry) kind() int {
	if strings.Contains(e.URL, "2libra.com") {
		return netLogKindAPI
	}
	ct := e.RespCT
	if ct == "" && !e.Done && e.Status == 0 {
		return netLogKindOther // 还没响应：先归「其他」，头到了再修正
	}
	ct = strings.ToLower(ct)
	switch {
	case strings.HasPrefix(ct, "image/"):
		return netLogKindImage
	default:
		return netLogKindOther
	}
}

// uiKind 返回过滤芯片用的下标：与 netLogKindLabels 对齐（0 全部 / 1 2Libra /
// 2 图片 / 3 其他）。条目本身永远不会是「全部」，所以不做那个映射。
func (e *netEntry) uiKind() int {
	switch e.kind() {
	case netLogKindAPI:
		return 1
	case netLogKindImage:
		return 2
	default:
		return 3
	}
}

// failed 报告这条请求是不是一眼要看出的问题行：连接失败或 4xx / 5xx。
func (e *netEntry) failed() bool {
	return e.Err != "" || e.Status >= 400
}

// netRecorder 是进程级的请求记录缓冲。挂在 proxyManager 上（appDeps 共享
// 同一个 proxyManager），因此所有窗口的请求都记进同一份。
type netRecorder struct {
	mu      sync.Mutex
	entries []*netEntry
	seq     int
	paused  bool
	watch   []func() // 新记录 / 记录完成时通知（供调试窗口刷新）
}

func newNetRecorder() *netRecorder {
	return &netRecorder{}
}

// active 报告当前是否应该记录。暂停时请求照发，只是不再写进缓冲。
func (r *netRecorder) active() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.paused
}

// SetPaused 暂停 / 恢复记录。
func (r *netRecorder) SetPaused(paused bool) {
	r.mu.Lock()
	r.paused = paused
	r.mu.Unlock()
}

func (r *netRecorder) Paused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}

// Clear 清空缓冲。已选中的条目由界面自己处理（按 ID 找不到就取消选中）。
func (r *netRecorder) Clear() {
	r.mu.Lock()
	r.entries = nil
	r.mu.Unlock()
}

// Snapshot 返回当前缓冲的浅拷贝（按时间先后）。界面每帧重新取，条目本身
// 还会在响应体读完时继续补全，无需深拷贝。
func (r *netRecorder) Snapshot() []*netEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*netEntry, len(r.entries))
	copy(out, r.entries)
	return out
}

// snapshotForExport 供诊断包导出：在锁内把每条记录按值复制（响应体读完时
// 后台还会补字段，不能把 *netEntry 交给后台）。头切片也另存一份。
func (r *netRecorder) snapshotForExport() []netEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]netEntry, len(r.entries))
	for i, e := range r.entries {
		out[i] = *e
		out[i].ReqHeaders = append([]netHeader(nil), e.ReqHeaders...)
		out[i].RespHeaders = append([]netHeader(nil), e.RespHeaders...)
	}
	return out
}

// ByID 按 ID 找一条记录；找不到（已被清空或挤掉）返回 nil。
func (r *netRecorder) ByID(id int) *netEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.entries {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// Count 返回当前缓冲里的条数。
func (r *netRecorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// addWatcher 注册一个新记录到达 / 记录完成时的回调，返回反注册函数。
// 回调在任意的网络协程里触发，收到通知的界面自己把刷新挪回主线程。
func (r *netRecorder) addWatcher(fn func()) (remove func()) {
	r.mu.Lock()
	r.watch = append(r.watch, fn)
	last := len(r.watch) - 1
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			if last < len(r.watch) {
				r.watch[last] = nil
			}
			r.mu.Unlock()
		})
	}
}

// notify 把回调拷出来在锁外调用，避免回调里再进记录器时死锁。
func (r *netRecorder) notify() {
	r.mu.Lock()
	fns := make([]func(), 0, len(r.watch))
	for _, fn := range r.watch {
		if fn != nil {
			fns = append(fns, fn)
		}
	}
	r.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// append 把新记录放进缓冲，必要时挤掉最旧的，然后通知界面。
func (r *netRecorder) append(e *netEntry) {
	r.mu.Lock()
	r.seq++
	e.ID = r.seq
	r.entries = append(r.entries, e)
	if len(r.entries) > netLogMaxEntries {
		r.entries = r.entries[len(r.entries)-netLogMaxEntries:]
	}
	r.mu.Unlock()
	r.notify()
}

// finish 补全耗时与完成标记，然后通知界面刷新这一条。
func (r *netRecorder) finish(e *netEntry) {
	if e.Done {
		return
	}
	e.Done = true
	e.Elapsed = time.Since(e.Start)
	r.notify()
}

// begin 在请求发出前记下方法、地址、请求头与请求体。请求体最多读
// netLogMaxBody 字节，剩下的照常交给 transport 发出去，请求内容不受影响。
func (r *netRecorder) begin(req *http.Request) *netEntry {
	e := &netEntry{
		Start:  time.Now(),
		Method: req.Method,
		URL:    req.URL.String(),
	}
	for k, vv := range req.Header {
		for _, v := range vv {
			e.ReqHeaders = append(e.ReqHeaders, netHeader{Key: k, Value: netRedactHeader(k, v)})
		}
	}
	sort.Slice(e.ReqHeaders, func(i, j int) bool { return e.ReqHeaders[i].Key < e.ReqHeaders[j].Key })

	if req.Body != nil && !netBinaryCT(req.Header.Get("Content-Type")) {
		head, _ := io.ReadAll(io.LimitReader(req.Body, netLogMaxBody+1))
		e.ReqBodyTruncated = len(head) > netLogMaxBody
		e.ReqBody = string(head)
		if e.ReqBodyTruncated {
			// 只展示前 netLogMaxBody 字节；读出来的整段（含多读的那 1 字节）
			// 必须原样还回流里，否则 transport 发出的内容比 ContentLength
			// 少一个字节，服务端会报 body length 不符。
			e.ReqBody = string(head[:netLogMaxBody])
		}
		// 读过的部分还回去，未读部分留在原 Body 里，transport 拿到的流完整。
		req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(head), req.Body))
	} else if req.Body != nil {
		e.ReqBodyNote = "二进制请求体，不在调试窗口里展示"
	}
	r.append(e)
	return e
}

// header 记下响应头与传输错误。响应体交给 captureBody 边读边记；
// 二进制或错误响应在这里直接收尾。
func (r *netRecorder) header(e *netEntry, resp *http.Response, err error) {
	if err != nil {
		e.Err = err.Error()
		r.finish(e)
		return
	}
	e.Status = resp.StatusCode
	e.RespCT = resp.Header.Get("Content-Type")
	for k, vv := range resp.Header {
		for _, v := range vv {
			e.RespHeaders = append(e.RespHeaders, netHeader{Key: k, Value: netRedactHeader(k, v)})
		}
	}
	sort.Slice(e.RespHeaders, func(i, j int) bool { return e.RespHeaders[i].Key < e.RespHeaders[j].Key })
	ct := strings.ToLower(e.RespCT)
	if strings.HasPrefix(ct, "image/") {
		e.RespNote = "图片内容，不在调试窗口里展示"
		r.finish(e)
	} else if strings.HasPrefix(ct, "text/event-stream") {
		// 通知长连接：响应体是持续到达的事件流，边流边记，关闭时才收尾。
		e.Stream = true
	}
}

// captureBody 把响应体包一层边读边记的流：读到 netLogMaxBody 为止，读完
// 或关闭时收尾。调用方拿到的响应体内容与关闭语义不变。
func (r *netRecorder) captureBody(inner io.ReadCloser, e *netEntry) io.ReadCloser {
	return &netLogBody{inner: inner, rec: r, e: e}
}

type netLogBody struct {
	inner io.ReadCloser
	rec   *netRecorder
	e     *netEntry
	buf   bytes.Buffer
	full  bool
}

func (b *netLogBody) Read(p []byte) (int, error) {
	n, err := b.inner.Read(p)
	if n > 0 {
		if room := netLogMaxBody - b.buf.Len(); room > 0 {
			take := n
			if take > room {
				take = room
			}
			b.buf.Write(p[:take])
			if b.buf.Len() >= netLogMaxBody && n > take {
				b.full = true
			}
		} else {
			b.full = true
		}
		if err == io.EOF {
			b.store()
			b.rec.finish(b.e)
		}
	}
	return n, err
}

func (b *netLogBody) Close() error {
	err := b.inner.Close()
	if !b.e.Done {
		b.store()
		b.rec.finish(b.e)
	}
	return err
}

func (b *netLogBody) store() {
	b.e.RespBody = b.buf.String()
	b.e.RespTrunc = b.full
	if b.full {
		b.e.RespNote = "响应体超过 " + netHumanSize(netLogMaxBody) + "，已截断"
	}
}

func netHumanSize(n int) string {
	return fmt.Sprintf("%.0f KB", float64(n)/1024)
}

// netLogMarkerKey 是请求 context 上的标记：经过记录 transport 的请求都带着
// 它。共享 transport 内部解析代理时会调 proxyFor，靠这个标记认出「已经记过」
// 的请求，不给它记第二条。
type netLogMarkerKey struct{}

// netLogTransport 把共享 transport 包一层：发出前记请求，回来后记响应。它同时
// 是网络状态机的探针——连接类失败上报一次（触发断网确认），确认离线后拦下后续
// 请求，让全应用各种退避重试都停下来（见 netstatus.go）。
type netLogTransport struct {
	inner  http.RoundTripper
	rec    *netRecorder
	status *networkStatus
}

func (t *netLogTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// 离线期间不再把请求发到网络：没有请求就没有重试，弹窗被关掉后用户再
	// 触发请求时借 nudge 把它顶回来。
	if t.status != nil && t.status.isOffline() {
		t.status.nudge()
		return nil, errOffline
	}
	if !t.rec.active() {
		resp, err := t.inner.RoundTrip(req)
		if err != nil && t.status != nil {
			t.status.reportFailure(err)
		}
		return resp, err
	}
	// 先打标记再往下传：共享 transport 内部解析代理时会调 proxyFor，靠标记
	// 区分「这个请求已经记过」，不给它补第二条。
	marked := req.WithContext(context.WithValue(req.Context(), netLogMarkerKey{}, true))
	e := t.rec.begin(marked)
	resp, err := t.inner.RoundTrip(marked)
	if err != nil && t.status != nil {
		t.status.reportFailure(err)
	}
	t.rec.header(e, resp, err)
	if err != nil || resp == nil {
		return resp, err
	}
	if e.Done { // 图片等二进制响应：头与说明已记完，原样返回
		return resp, nil
	}
	resp.Body = t.rec.captureBody(resp.Body, e)
	return resp, nil
}

// noteUnwrapped 给绕不进记录层的出口（更新插件用的 netproxy 共享单例等）
// 补一条仅请求侧的记录：没有标记说明这个请求不是 netLogTransport 发的。
// 状态码与响应体拿不到，界面会注明原因。
func (r *netRecorder) noteUnwrapped(req *http.Request) {
	if !r.active() {
		return
	}
	if req.Context().Value(netLogMarkerKey{}) == true {
		return // 已经由 netLogTransport 记过
	}
	if req.URL.Host == "" {
		return
	}
	e := r.begin(req)
	e.RespNote = "该请求走进程级默认出口（如更新插件内部客户端），未接入记录层，状态与响应体未知"
	r.finish(e)
}

// netRedactHeader 把敏感头的值替换成截短 + 长度说明：调试窗口是给人看请求
// 形状的，不把登录令牌整段摊在屏幕上。
func netRedactHeader(k, v string) string {
	switch strings.ToLower(k) {
	case "authorization", "proxy-authorization", "cookie", "set-cookie":
		if len(v) > 16 {
			return v[:12] + "…（已隐藏，共 " + strconv.Itoa(len(v)) + " 字符）"
		}
		return "（已隐藏）"
	}
	return v
}

// netBinaryCT 判断请求体是不是不适合当文本展示的二进制类型。
func netBinaryCT(ct string) bool {
	ct = strings.ToLower(ct)
	for _, prefix := range []string{"image/", "audio/", "video/", "application/octet-stream", "multipart/form-data"} {
		if strings.HasPrefix(ct, prefix) {
			return true
		}
	}
	return false
}
