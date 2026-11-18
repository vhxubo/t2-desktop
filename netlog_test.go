package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 用真实的 httptest 服务与 proxyManager 的客户端走一遍请求，确认记录器把
// 请求与响应都记了下来：方法、URL、状态码、响应体、请求体与耗时。
func TestNetLogRecordsRequestAndResponse(t *testing.T) {
	clearProxyEnv(t)
	a := testApp(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 服务端把请求体读完再回：顺带验证记录器读完的前半截被完整还回。
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"title":"你好"}` {
			t.Errorf("服务端收到的请求体不完整：%q", string(body))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"c":0,"m":"ok","d":{"posts":[]},"t":1}`))
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/topics?page=2", strings.NewReader(`{"title":"你好"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.secret-token-value")
	resp, err := a.proxy.Client(5 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	entries := a.proxy.net.Snapshot()
	if len(entries) != 1 {
		t.Fatalf("应当记录 1 条请求，实际 %d 条", len(entries))
	}
	e := entries[0]
	if e.Method != http.MethodPost || e.URL != srv.URL+"/v1/topics?page=2" {
		t.Errorf("方法或 URL 记录不对：%s %s", e.Method, e.URL)
	}
	if e.Status != http.StatusOK {
		t.Errorf("状态码记录不对：%d", e.Status)
	}
	if e.ReqBody != `{"title":"你好"}` {
		t.Errorf("请求体记录不对：%q", e.ReqBody)
	}
	if !strings.Contains(e.RespBody, `"c":0`) {
		t.Errorf("响应体记录不对：%q", e.RespBody)
	}
	if !e.Done {
		t.Error("响应体读完并关闭后记录应当标记为完成")
	}
	// 登录令牌必须脱敏：不能把整段 Bearer 令牌摊在调试窗口里。
	for _, h := range e.ReqHeaders {
		if strings.EqualFold(h.Key, "Authorization") && strings.Contains(h.Value, "secret-token-value") {
			t.Errorf("Authorization 头没有脱敏：%q", h.Value)
		}
	}
}

// 暂停后不再记录新请求，清空后缓冲为空；恢复后继续记录。
func TestNetLogPauseAndClear(t *testing.T) {
	clearProxyEnv(t)
	a := testApp(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`ok`))
	}))
	defer srv.Close()
	client := a.proxy.Client(5 * time.Second)

	do := func() {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/ping", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	do()
	if a.proxy.net.Count() != 1 {
		t.Fatalf("恢复状态应当记录请求：%d", a.proxy.net.Count())
	}
	a.proxy.net.SetPaused(true)
	do()
	if a.proxy.net.Count() != 1 {
		t.Errorf("暂停期间不应记录新请求：%d", a.proxy.net.Count())
	}
	a.proxy.net.Clear()
	if a.proxy.net.Count() != 0 {
		t.Errorf("清空后应当没有记录：%d", a.proxy.net.Count())
	}
	a.proxy.net.SetPaused(false)
	do()
	if a.proxy.net.Count() != 1 {
		t.Errorf("恢复后应当继续记录：%d", a.proxy.net.Count())
	}
}

// 超长请求体只记前一段，但服务端必须收到完整内容；超长响应体同理截断。
// 文本体截断展示，二进制体只留说明；两种情况下传输内容都不受影响。
func TestNetLogTruncatesLongBodies(t *testing.T) {
	clearProxyEnv(t)
	a := testApp(t)
	big := strings.Repeat("x", netLogMaxBody*3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != big {
			t.Errorf("服务端收到的请求体被记录层截掉了：%d 字节", len(body))
		}
		w.Write([]byte(big))
	}))
	defer srv.Close()

	client := a.proxy.Client(5 * time.Second)
	do := func(ct string) *netEntry {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/upload", strings.NewReader(big))
		req.Header.Set("Content-Type", ct)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(got) != big {
			t.Errorf("响应被记录层截掉了：%d 字节", len(got))
		}
		entries := a.proxy.net.Snapshot()
		return entries[len(entries)-1]
	}

	// 二进制请求体：不进记录，只留说明。
	e := do("application/octet-stream")
	if e.ReqBody != "" || e.ReqBodyNote == "" {
		t.Errorf("二进制请求体应只留说明：%q / %q", e.ReqBody, e.ReqBodyNote)
	}
	if !e.RespTrunc || len(e.RespBody) != netLogMaxBody {
		t.Errorf("超长响应体应截断到 %d 字节：%d（截断标记 %v）", netLogMaxBody, len(e.RespBody), e.RespTrunc)
	}

	// 文本请求体：截断展示，传输不受影响。
	e = do("text/plain")
	if !e.ReqBodyTruncated || len(e.ReqBody) != netLogMaxBody {
		t.Errorf("超长文本请求体应截断到 %d 字节：%d（截断标记 %v）",
			netLogMaxBody, len(e.ReqBody), e.ReqBodyTruncated)
	}
}

// 更新插件走 netproxy 共享单例，绕不进记录层：proxyFor 里补的仅请求侧
// 记录要能认出「已经记过」的请求，不给同一个请求记两次。
func TestNetLogNoteUnwrappedSkipsMarkedRequests(t *testing.T) {
	a := testApp(t)
	req, _ := http.NewRequest(http.MethodGet, "https://2libra.com/api/v1/me", nil)

	// 带标记（已经过记录层）：不补记录。
	marked := req.WithContext(context.WithValue(req.Context(), netLogMarkerKey{}, true))
	a.proxy.net.noteUnwrapped(marked)
	if a.proxy.net.Count() != 0 {
		t.Errorf("已标记的请求不应重复记录：%d", a.proxy.net.Count())
	}

	// 不带标记（更新插件内部客户端）：补一条仅请求侧的记录。
	a.proxy.net.noteUnwrapped(req)
	entries := a.proxy.net.Snapshot()
	if len(entries) != 1 {
		t.Fatalf("应当补 1 条仅请求侧的记录，实际 %d 条", len(entries))
	}
	e := entries[0]
	if e.URL != req.URL.String() || e.Status != 0 {
		t.Errorf("仅请求侧的记录内容不对：%s %d", e.URL, e.Status)
	}
	if e.RespNote == "" {
		t.Error("仅请求侧的记录应当说明响应未捕获")
	}
}

// 调试窗口本身：列表、筛选与选中详情都要能在无头渲染里工作。
func TestNetLogWindowView(t *testing.T) {
	clearProxyEnv(t)
	a := testApp(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"c":0,"m":"ok","d":{"total":1,"page":1}}`))
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/topics?page=1&limit=50", nil)
	resp, err := a.proxy.Client(5 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	rowLabel := "GET " + srv.URL + "/v1/topics?page=1&limit=50"
	tt := ui.NewTester(a.netLogWindowView, netLogWindowWidth, netLogWindowHeight)
	tt.Frame()

	if !tt.HasText("暂停记录") {
		t.Errorf("网络调试窗口没有暂停入口：%q", tt.Texts())
	}
	if !tt.HasText("清空记录") {
		t.Errorf("网络调试窗口没有清空入口：%q", tt.Texts())
	}
	if !tt.HasText("1 条") {
		t.Errorf("窗口没有显示记录条数：%q", tt.Texts())
	}
	if !tt.HasText(rowLabel) {
		t.Errorf("请求列表里没有这条请求：%q", tt.Texts())
	}

	// 点中一行后详情区展示概览与查询参数。
	if err := tt.Click(rowLabel); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("概览") || !tt.HasText("查询参数") {
		t.Errorf("选中请求后详情区没有展示概览与查询参数：%q", tt.Texts())
	}
	if !tt.HasText("limit") {
		t.Errorf("详情区没有列出查询参数名：%q", tt.Texts())
	}

	// 筛选一个不存在的关键字：列表为空，显示空状态提示。
	a.netLogFilter = "不存在的关键字"
	tt.Frame()
	if !tt.HasText("还没有请求") {
		t.Errorf("筛选无结果时没有空状态提示：%q", tt.Texts())
	}
}

// 分类过滤：图片响应归「图片」，JSON 归其他（非 2Libra 域名）或 2Libra。
func TestNetLogKindFilter(t *testing.T) {
	clearProxyEnv(t)
	a := testApp(t)
	jsonSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer jsonSrv.Close()
	imgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte{0x89, 0x50})
	}))
	defer imgSrv.Close()

	client := a.proxy.Client(5 * time.Second)
	for _, target := range []string{jsonSrv.URL + "/v1/topics", imgSrv.URL + "/a.png"} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}

	a.netLogKind = 2 // 图片
	if n := len(a.netLogFiltered(a.proxy.net.Snapshot())); n != 1 {
		t.Errorf("图片过滤应剩 1 条：%d", n)
	}
	a.netLogKind = 3 // 其他
	if n := len(a.netLogFiltered(a.proxy.net.Snapshot())); n != 1 {
		t.Errorf("其他过滤应剩 1 条（JSON 归其他，因为不是 2Libra 域名）：%d", n)
	}
	a.netLogKind = 0 // 全部
	if n := len(a.netLogFiltered(a.proxy.net.Snapshot())); n != 2 {
		t.Errorf("全部应剩 2 条：%d", n)
	}
}

// 详情区的每个数据都有复制入口：KV 行的值能单独拷走，请求体 / 响应体各有
// 一键复制，整条请求还能还原成 curl 命令；结果统一用本窗口 toast 报告。
func TestNetLogDetailCopy(t *testing.T) {
	clearProxyEnv(t)
	a := testApp(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"c":0,"m":"ok","d":{},"t":1}`))
	}))
	defer srv.Close()
	token := "Bearer " + strings.Repeat("t", 40)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/topics?page=1", strings.NewReader(`{"title":"你好"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)
	resp, err := a.proxy.Client(5 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	tt := ui.NewTester(a.netLogWindowView, netLogWindowWidth, netLogWindowHeight)
	tt.Frame()
	if err := tt.Click("POST " + srv.URL + "/v1/topics?page=1"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	// KV 行：URL 单独复制。
	if err := tt.Click("复制 URL"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if want := srv.URL + "/v1/topics?page=1"; tt.Clipboard() != want {
		t.Fatalf("复制 URL 应得到 %q，实际 %q", want, tt.Clipboard())
	}
	if !tt.HasText("已复制「URL」") {
		t.Errorf("复制成功没有用 toast 报告：%q", tt.Texts())
	}

	// KV 行：Authorization 也有复制入口；它的展示值在记录层已脱敏（见
	// TestNetLogRecordsRequestAndResponse），复制的自然也是脱敏值。该行在
	// 详情滚动区视口之外，用无障碍名确认入口存在即可。
	if _, ok := tt.Access("复制 Authorization"); !ok {
		t.Fatalf("Authorization 头没有复制入口：%q", tt.Texts())
	}

	// 请求体与响应体一键复制。
	if err := tt.Click("复制请求体"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := tt.Clipboard(); got != `{"title":"你好"}` {
		t.Fatalf("复制请求体应得到原始请求体，实际 %q", got)
	}
	if err := tt.Click("复制响应体"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := tt.Clipboard(); !strings.Contains(got, `"c":0`) {
		t.Fatalf("复制响应体应得到响应内容，实际 %q", got)
	}

	// 一键复制成 curl：以 curl 开头、带请求体与脱敏注释，结尾不能是续行符
	// （续行符后跟注释会被 bash 续进命令里）。
	if err := tt.Click("复制为 curl"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	cmd := tt.Clipboard()
	if !strings.HasPrefix(cmd, "curl") {
		t.Fatalf("复制的应是 curl 命令：%q", cmd)
	}
	if !strings.Contains(cmd, `{"title":"你好"}`) {
		t.Errorf("curl 命令里应带请求体：%q", cmd)
	}
	if strings.Contains(cmd, strings.Repeat("t", 40)) {
		t.Errorf("curl 命令不该包含完整令牌：%q", cmd)
	}
	if !strings.Contains(cmd, "已脱敏") {
		t.Errorf("curl 命令应注释说明敏感头已脱敏：%q", cmd)
	}
	if strings.HasSuffix(cmd, "\\") {
		t.Errorf("curl 命令不应以续行符结尾：%q", cmd)
	}
}

// curl 命令的 shell 转义：URL 与请求体里的单引号必须按 '\” 的惯用法转义，
// 整段命令贴进 bash 才不会断开。
func TestNetCurlCommandEscapesSingleQuotes(t *testing.T) {
	e := &netEntry{
		Method: "POST",
		URL:    "https://x.test/v1/a?name=O'Neill",
		ReqHeaders: []netHeader{
			{Key: "Content-Type", Value: "application/json"},
		},
		ReqBody: `{"s":"it's"}`,
	}
	cmd := netCurlCommand(e)
	if want := "curl 'https://x.test/v1/a?name=O'\\''Neill'"; !strings.HasPrefix(cmd, want) {
		t.Errorf("URL 的单引号没有按 shell 惯用法转义：\nwant %q\ngot  %q", want, cmd)
	}
	if want := `'{"s":"it'\''s"}'`; !strings.Contains(cmd, want) {
		t.Errorf("请求体的单引号没有转义：\nwant %q\ngot  %q", want, cmd)
	}
	if strings.HasSuffix(cmd, "\\") {
		t.Errorf("命令不应以续行符结尾：%q", cmd)
	}
}
