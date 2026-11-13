package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 只校验文件头 sniff，不需要一张能解码的完整图：头 8 字节 + 填充即可。
var pngHeadForUpload = append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
	bytes.Repeat([]byte{7}, 40)...)

func writeTestImage(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// 默认预设按「免 Token 优先」排：Kappa 与几家免注册的第三方图床在前，官方
// 图床其后，要 Key / Client-ID / Token 的 StarDots / Imgur / SM.MS / 兰空与
// 自定义排在最后。免 Token 的默认启用，「当前使用」落在 Kappa。
func TestDefaultImageHostConfig(t *testing.T) {
	a := testApp(t)
	want := []string{
		"kappa", "mjj", "imgbb", "photo_lily", "appinn", "tikolu", "111666_best",
		"skyimg", "skyimg_webp", "litterbox", "catbox", "x0", "2libra",
		"stardots", "imgur", "smms", "lsky", "custom",
	}
	if len(a.hosts.Hosts) != len(want) {
		t.Fatalf("默认图库数量 = %d，期望 %d", len(a.hosts.Hosts), len(want))
	}
	for i, id := range want {
		if got := a.hosts.Hosts[i].ID; got != id {
			t.Errorf("第 %d 条图库 = %q，期望 %q", i, got, id)
		}
	}
	if a.hosts.Active != "kappa" {
		t.Errorf("默认图库 = %q，期望免 Token 的 kappa", a.hosts.Active)
	}
	enabled := map[string]bool{
		"kappa": true, "imgbb": true, "photo_lily": true, "appinn": true,
		"tikolu": true, "skyimg": true, "2libra": true,
	}
	for _, h := range a.hosts.Hosts {
		if h.Enabled != enabled[h.ID] {
			t.Errorf("预设 %q 的启用状态 = %v，期望 %v", h.ID, h.Enabled, enabled[h.ID])
		}
	}
	// 免 Token 图床不填 Token 也要就绪；官方图床未登录时不行；StarDots 要凭据。
	for _, id := range []string{"kappa", "imgbb", "photo_lily", "appinn",
		"tikolu", "skyimg"} {
		if !a.hostReady(hostByID(a.hosts, id)) {
			t.Errorf("%s 免 Token，默认配置就该就绪", id)
		}
	}
	if a.hostReady(hostByID(a.hosts, "2libra")) {
		t.Error("未登录时官方图床不该判定为就绪")
	}
	if r := a.hostNotReadyReason(hostByID(a.hosts, "2libra")); !strings.Contains(r, "登录") {
		t.Errorf("未登录提示没有说清原因：%q", r)
	}
	// 启用后免 Token 的 Skyimg WebP / MJJ / 111666 同样就绪；StarDots 还差
	// Key / Secret。
	for _, id := range []string{"mjj", "111666_best"} {
		h := hostByID(a.hosts, id)
		h.Enabled = true
		if !a.hostReady(h) {
			t.Errorf("%s 免 Token，启用后就该就绪", id)
		}
		h.Enabled = false
	}
	webp := hostByID(a.hosts, "skyimg_webp")
	webp.Enabled = true
	if !a.hostReady(webp) {
		t.Error("Skyimg WebP 免 Token，启用后就该就绪")
	}
	dots := hostByID(a.hosts, "stardots")
	dots.Enabled = true
	if a.hostReady(dots) {
		t.Error("没填 Key / Secret 时 StarDots 不该就绪")
	}
	if r := a.hostNotReadyReason(dots); !strings.Contains(r, "Secret") {
		t.Errorf("StarDots 未配置的提示 = %q", r)
	}
	dots.Token, dots.Secret = "key", "secret"
	if !a.hostReady(dots) {
		t.Error("填好 Key / Secret 后 StarDots 应就绪")
	}
	// 还原成默认的启用状态，下面的「启用了哪几条」才作数。
	webp.Enabled, dots.Enabled = false, false
	dots.Token, dots.Secret = "", ""
	if err := a.saveImageHosts(); err != nil {
		t.Fatalf("默认配置应该能直接保存：%v", err)
	}
	names := a.hostNames()
	if len(names) != len(enabled) {
		t.Errorf("编辑器里该看到 %d 条启用的图库：%v", len(enabled), names)
	}
}

func TestValidateImageHostConfig(t *testing.T) {
	base := imageHost{
		ID: "x", Name: "自建", Kind: imageHostGeneric, Field: "file",
		Endpoint: "https://img.test/api/upload", MaxBytes: defaultImageMaxBytes,
		Enabled: true,
	}
	// 停用的空地址预设（兰空 / 自定义）不参与完整性校验。
	disabled := base
	disabled.Enabled = false
	disabled.Endpoint = ""
	if err := validateImageHost(disabled); err != nil {
		t.Errorf("停用项不该要求接口地址：%v", err)
	}
	cases := []struct {
		name string
		edit func(*imageHost)
		want string
	}{
		{"空名称", func(h *imageHost) { h.Name = "  " }, "名称"},
		{"未知类型", func(h *imageHost) { h.Kind = "ftp" }, "类型"},
		{"缺接口地址", func(h *imageHost) { h.Endpoint = "" }, "接口地址"},
		{"不是 http 地址", func(h *imageHost) { h.Endpoint = "ftp://x.test/up" }, "接口地址"},
		{"缺文件字段名", func(h *imageHost) { h.Field = "" }, "字段名"},
		{"请求头格式错", func(h *imageHost) { h.Header = "Authorization" }, "请求头"},
		{"表单字段格式错", func(h *imageHost) { h.Form = "reqtype" }, "表单字段"},
		{"URL 路径有空段", func(h *imageHost) { h.URLPath = "data..url" }, "URL 路径"},
	}
	for _, c := range cases {
		h := base
		c.edit(&h)
		if err := validateImageHost(h); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：期望包含 %q 的错误，得到 %v", c.name, c.want, err)
		}
	}
	// 官方图床不要求接口地址与 Token；重复 ID 则由配置级校验拦下。
	official := imageHost{ID: "o", Name: "官方", Kind: imageHostOfficial, Enabled: true}
	if err := validateImageHost(official); err != nil {
		t.Errorf("官方图床不该要求接口配置：%v", err)
	}
	a := testApp(t)
	a.hosts.Hosts = append(a.hosts.Hosts, imageHost{ID: "2libra", Name: "重复", Kind: imageHostOfficial})
	if err := a.validateImageHostConfig(); err == nil {
		t.Error("重复 ID 应被拒绝")
	}
}

func TestNormalizeImageHostsRepairsSavedConfig(t *testing.T) {
	cfg := defaultImageHostConfig()
	cfg.Hosts = cfg.Hosts[:1] // 模拟旧版本缓存：只剩一条预设
	cfg.Hosts[0].Name = "我的 Kappa"
	cfg.Active = "smms" // 停用项不能当「当前使用」
	cfg.Hosts = append(cfg.Hosts, imageHost{
		ID: "user-9", Name: "我的图库", Kind: imageHostGeneric, Enabled: true,
		Field: "file", Endpoint: "https://img.test/up", MaxBytes: 99 << 20,
	})
	out := normalizeImageHosts(cfg)
	presets := len(defaultImageHostConfig().Hosts)
	if len(out.Hosts) != presets+1 {
		t.Fatalf("预设应补齐到 %d 条 + 1 条自定义，实际 %d", presets, len(out.Hosts))
	}
	if out.Hosts[0].ID != "kappa" || out.Hosts[len(out.Hosts)-1].ID != "user-9" {
		t.Errorf("内置项应在前、自定义项在后：%v", hostIDs(out))
	}
	if got := hostByID(out, "kappa").Name; got != "我的 Kappa" {
		t.Errorf("用户改过的内置项应保留：%q", got)
	}
	if got := hostByID(out, "user-9").MaxBytes; got != defaultImageMaxBytes {
		t.Errorf("越界的上限应夹回默认值：%d", got)
	}
	if out.Active != "kappa" {
		t.Errorf("停用的激活项应回落到第一条启用的图库：%q", out.Active)
	}
	// 内置图床全停用时，落到第一条启用的自定义图床。
	for i := range out.Hosts {
		if out.Hosts[i].ID != "user-9" {
			out.Hosts[i].Enabled = false
		}
	}
	again := imageHostConfig{Active: "2libra", Hosts: out.Hosts}
	if got := normalizedActiveID(again); got != "user-9" {
		t.Errorf("应选中第一条启用的图库：%q", got)
	}
}

// 旧缓存里已经下线的预设（0x0.st / uguu.se）要丢掉，别留成自定义项，
// 也别让「当前使用」继续指向一条已经不能用的图床。
func TestNormalizeImageHostsDropsRetiredPresets(t *testing.T) {
	cfg := defaultImageHostConfig()
	cfg.Active = "0x0"
	cfg.Hosts = append(cfg.Hosts,
		imageHost{ID: "0x0", Name: "0x0.st（免 Token）", Kind: imageHostGeneric,
			Endpoint: "https://0x0.st", Field: "file", Enabled: true, Builtin: true},
		imageHost{ID: "uguu", Name: "Uguu（免 Token · 临时）", Kind: imageHostGeneric,
			Endpoint: "https://uguu.se/upload.php", Field: "files[]", Enabled: true, Builtin: true},
	)
	out := normalizeImageHosts(cfg)
	if hostByID(out, "0x0") != nil || hostByID(out, "uguu") != nil {
		t.Errorf("下线的预设不该留下：%v", hostIDs(out))
	}
	for _, h := range out.Hosts {
		if h.Name == "0x0.st（免 Token）" || h.Name == "Uguu（免 Token · 临时）" {
			t.Errorf("下线的预设不该变成自定义项：%+v", h)
		}
	}
	if out.Active != "kappa" {
		t.Errorf("当前使用应回落到可用的图床：%q", out.Active)
	}
}

// 老缓存里的 Imgur 预设是通用 multipart 配置：升级后要改走内置协议，但用户
// 填过的 Client-ID（Token）与接口地址要留着，不能把已经能用的配置打断。
func TestNormalizeImageHostsAdoptsImgurAdapter(t *testing.T) {
	cfg := imageHostConfig{
		Active: "imgur",
		Hosts: []imageHost{{
			ID: "imgur", Name: "Imgur", Kind: imageHostGeneric,
			Endpoint: "https://api.imgur.com/3/image", Field: "image",
			Header: "Authorization: Client-ID {{token}}", Token: "my-client-id",
			URLPath: "data.link", MaxBytes: defaultImageMaxBytes,
			Enabled: true, Builtin: true,
		}},
	}
	out := normalizeImageHosts(cfg)
	h := hostByID(out, "imgur")
	if h.Adapter != adapterImgur {
		t.Errorf("旧 Imgur 预设应改走内置协议：%q", h.Adapter)
	}
	if h.Token != "my-client-id" || h.Endpoint != "https://api.imgur.com/3/image" {
		t.Errorf("用户填过的 Client-ID / 地址被改了：%+v", h)
	}
	a := testApp(t)
	if !a.hostReady(h) {
		t.Error("有 Client-ID 的内置 Imgur 应该就绪")
	}
	// 没填 Client-ID 的旧配置同样能用了：内置协议会轮换公共 ID。
	cfg.Hosts[0].Token = ""
	if !a.hostReady(hostByID(normalizeImageHosts(cfg), "imgur")) {
		t.Error("没填 Client-ID 时内置协议应靠公共 ID 就绪")
	}
}

func hostIDs(cfg imageHostConfig) []string {
	out := make([]string, 0, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		out = append(out, h.ID)
	}
	return out
}

func TestInspectImage(t *testing.T) {
	t.Run("按文件头修正扩展名", func(t *testing.T) {
		got, err := inspectImage(uploadSource{Name: "photo.jpg", Data: pngHeadForUpload}, defaultImageMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != "photo.png" || got.Mime != "image/png" {
			t.Errorf("得到 %+v", got)
		}
	})
	t.Run("保留合法 jpeg 扩展名", func(t *testing.T) {
		jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3}
		got, err := inspectImage(uploadSource{Name: "a.jpeg", Data: jpeg}, defaultImageMaxBytes)
		if err != nil || got.Name != "a.jpeg" || got.Mime != "image/jpeg" {
			t.Errorf("得到 %+v, err=%v", got, err)
		}
	})
	t.Run("识别 WebP", func(t *testing.T) {
		webp := []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")
		got, err := inspectImage(uploadSource{Name: "a.png", Data: webp}, defaultImageMaxBytes)
		if err != nil || got.Mime != "image/webp" || got.Name != "a.webp" {
			t.Errorf("得到 %+v, err=%v", got, err)
		}
	})
	t.Run("拒绝非图片", func(t *testing.T) {
		if _, err := inspectImage(uploadSource{Name: "note.png", Data: []byte("hello world")},
			defaultImageMaxBytes); err == nil {
			t.Error("文本文件不该通过校验")
		}
	})
	t.Run("拒绝超大图", func(t *testing.T) {
		if _, err := inspectImage(uploadSource{Name: "big.png", Data: pngHeadForUpload}, 16); err == nil {
			t.Error("超过上限应被拒绝")
		}
	})
	t.Run("拒绝文件夹", func(t *testing.T) {
		if _, err := inspectImage(uploadSource{Path: t.TempDir()}, defaultImageMaxBytes); err == nil {
			t.Error("拖入文件夹应被拒绝")
		}
	})
	t.Run("本地路径也能读出字节", func(t *testing.T) {
		p := writeTestImage(t, "disk.png", pngHeadForUpload)
		got, err := inspectImage(uploadSource{Path: p, Name: "disk.png"}, defaultImageMaxBytes)
		if err != nil {
			t.Fatal(err)
		}
		b, err := got.bytes()
		if err != nil || !bytes.Equal(b, pngHeadForUpload) {
			t.Errorf("读回内容不符：%d 字节, err=%v", len(b), err)
		}
	})
}

// postFile 要真的把 sniff 出来的 Content-Type 写进 multipart part，
// 并且带上 Authorization、用户自定义请求头与附加表单字段。
func TestPostFileWritesMultipartHeaders(t *testing.T) {
	var gotField, gotFilename, gotPartType, gotAuth, gotKey, gotReqtype string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("Content-Type 不是 multipart：%v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		// 附加字段写在文件之前，先把它们读完。
		for {
			part, err := mr.NextPart()
			if err != nil {
				t.Errorf("读 part 失败：%v", err)
				break
			}
			if part.FormName() == "reqtype" {
				b, _ := io.ReadAll(part)
				gotReqtype = string(b)
				continue
			}
			gotField = part.FormName()
			gotFilename = part.FileName()
			gotPartType = part.Header.Get("Content-Type")
			gotBody, _ = io.ReadAll(part)
			break
		}
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.Header.Get("X-Api-Key")
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	body, status, err := c.postFile(context.Background(), srv.URL, "Bearer tok",
		map[string]string{"X-Api-Key": "abc"}, [][2]string{{"reqtype", "fileupload"}},
		"image", "my photo.png", "image/png", pngHeadForUpload)
	if err != nil || status != http.StatusOK {
		t.Fatalf("postFile 失败：status=%d err=%v", status, err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("响应体 = %q", body)
	}
	if gotField != "image" || gotFilename != "my photo.png" {
		t.Errorf("字段名 / 文件名 = %q / %q", gotField, gotFilename)
	}
	if gotReqtype != "fileupload" {
		t.Errorf("附加表单字段 reqtype = %q，期望 fileupload", gotReqtype)
	}
	if gotPartType != "image/png" {
		t.Errorf("part Content-Type = %q，期望 image/png", gotPartType)
	}
	if !bytes.Equal(gotBody, pngHeadForUpload) {
		t.Errorf("上传字节数 = %d", len(gotBody))
	}
	if gotAuth != "Bearer tok" || gotKey != "abc" {
		t.Errorf("请求头 = %q / %q", gotAuth, gotKey)
	}
}

func TestEscapeMultipartQuote(t *testing.T) {
	if got := escapeMultipartQuote("a\"b\\c\r\nd"); got != `a\"b\\c d` {
		t.Errorf("转义结果 = %q", got)
	}
}

func TestUploadToGenericExtractsURL(t *testing.T) {
	cases := []struct {
		name    string
		urlPath string
		reply   string
		want    string
	}{
		{"按配置路径取", "data.url", `{"data":{"url":"https://img.test/a.png"}}`, "https://img.test/a.png"},
		{"路径留空自动找", "", `{"items":[{"link":"https://img.test/b.png"}]}`, "https://img.test/b.png"},
		{"数组下标路径", "data.0.url", `{"data":[{"url":"https://img.test/c.png"}]}`, "https://img.test/c.png"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, c.reply)
			}))
			defer srv.Close()
			a := testApp(t)
			a.client.HTTP = srv.Client()
			h := imageHost{
				ID: "h", Name: "图床", Kind: imageHostGeneric, Endpoint: srv.URL,
				Field: "file", Header: "Authorization: Bearer {{token}}", Token: "tok",
				URLPath: c.urlPath, MaxBytes: defaultImageMaxBytes, Enabled: true,
			}
			got, err := a.uploadImage(context.Background(), h,
				uploadSource{Name: "a.png", Mime: "image/png", Data: pngHeadForUpload})
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("取到 URL = %q，期望 %q", got, c.want)
			}
		})
	}
	t.Run("图库明确拒绝", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, `{"success":false,"message":"Token 无效"}`)
		}))
		defer srv.Close()
		a := testApp(t)
		a.client.HTTP = srv.Client()
		h := imageHost{Kind: imageHostGeneric, Endpoint: srv.URL, Field: "file", MaxBytes: defaultImageMaxBytes}
		_, err := a.uploadToGeneric(context.Background(), h,
			uploadSource{Name: "a.png", Mime: "image/png"}, pngHeadForUpload)
		if err == nil || !strings.Contains(err.Error(), "Token 无效") {
			t.Errorf("期望带上图库给的原因，得到 %v", err)
		}
	})
}

// 免 Token 的 Catbox / 0x0.st 成功时直接返回地址：纯文本响应要原样取出来，
// 附加表单字段也要真的发出去。
func TestUploadToGenericTextResponse(t *testing.T) {
	var gotReqtype, gotField, gotFilename string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("解析 multipart 失败：%v", err)
		}
		gotReqtype = r.FormValue("reqtype")
		if f, hdr, err := r.FormFile("fileToUpload"); err == nil {
			f.Close()
			gotField, gotFilename = "fileToUpload", hdr.Filename
		}
		_, _ = fmt.Fprint(w, "https://files.catbox.moe/a.png\n")
	}))
	defer srv.Close()

	a := testApp(t)
	a.client.HTTP = srv.Client()
	h := imageHost{
		ID: "catbox", Name: "Catbox", Kind: imageHostGeneric, Endpoint: srv.URL,
		Field: "fileToUpload", Form: "reqtype=fileupload", TextResponse: true,
		MaxBytes: defaultImageMaxBytes, Enabled: true,
	}
	got, err := a.uploadImage(context.Background(), h,
		uploadSource{Name: "a.png", Mime: "image/png", Data: pngHeadForUpload})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://files.catbox.moe/a.png" {
		t.Errorf("取到 URL = %q", got)
	}
	if gotReqtype != "fileupload" || gotField != "fileToUpload" || gotFilename != "a.png" {
		t.Errorf("表单字段 = %q / %q / %q", gotReqtype, gotField, gotFilename)
	}

	// 纯文本接口报错时整段响应体是一句话，要原样带进错误里。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "413 File too large")
	}))
	defer bad.Close()
	a.client.HTTP = bad.Client()
	h.Endpoint = bad.URL
	_, err = a.uploadImage(context.Background(), h,
		uploadSource{Name: "a.png", Mime: "image/png", Data: pngHeadForUpload})
	if err == nil || !strings.Contains(err.Error(), "File too large") {
		t.Errorf("期望带上图库给的原因，得到 %v", err)
	}
}

// 「测试这个图床」要真的传一张 1×1 的 PNG，并把结果用 toast 报出来。
func TestTestImageHostUploadsTinyPNG(t *testing.T) {
	var gotName, gotType string
	var gotBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("解析 multipart 失败：%v", err)
		}
		if f, hdr, err := r.FormFile("file"); err == nil {
			gotName, gotType = hdr.Filename, hdr.Header.Get("Content-Type")
			gotBytes, _ = io.ReadAll(f)
			f.Close()
		}
		_, _ = fmt.Fprint(w, `{"link":"https://img.test/t2-test.png"}`)
	}))
	defer srv.Close()

	a := testApp(t)
	a.client.HTTP = srv.Client()
	h := hostByID(a.hosts, "kappa")
	h.Endpoint = srv.URL
	a.testImageHost(h)

	deadline := time.Now().Add(3 * time.Second)
	for !hasToastContaining(a, "测试成功") {
		if time.Now().After(deadline) {
			t.Fatalf("没有报告测试结果：%v", toastTexts(a))
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !hasToastContaining(a, "https://img.test/t2-test.png") {
		t.Errorf("成功提示没带上地址：%v", toastTexts(a))
	}
	if gotName != "t2-test.png" || gotType != "image/png" {
		t.Errorf("测试图 = %q / %q", gotName, gotType)
	}
	if !bytes.HasPrefix(gotBytes, []byte{0x89, 'P', 'N', 'G'}) {
		t.Errorf("测试图不是 PNG：% x", gotBytes[:min(8, len(gotBytes))])
	}
}

// 网络层错误要说人话：境外图床被拦截时，原始错误对用户没有意义。
func TestUploadNetError(t *testing.T) {
	if got := uploadNetError(errors.New(`Post "https://x.test": tls: server closed abruptly`)).
		Error(); !strings.Contains(got, "连不上图床") {
		t.Errorf("TLS 错误没有翻译：%q", got)
	}
	if got := uploadNetError(context.DeadlineExceeded).Error(); !strings.Contains(got, "超时") {
		t.Errorf("超时没有翻译：%q", got)
	}
	if got := uploadNetError(errors.New("some other problem")).Error(); got != "some other problem" {
		t.Errorf("其它错误应原样保留：%q", got)
	}
}

// 图床回了网页（接口地址填错 / 被拦截）时要说清楚，而不是甩一串标签。
func TestHostBadBodyExplainsHTML(t *testing.T) {
	err := hostBadBody(200, []byte("<!DOCTYPE html><html><body>blocked</body></html>"))
	if err == nil || !strings.Contains(err.Error(), "网页") {
		t.Errorf("HTML 响应应提示接口地址可能不对：%v", err)
	}
	err = hostBadBody(503, []byte("uploads disabled"))
	if err == nil || !strings.Contains(err.Error(), "uploads disabled") {
		t.Errorf("纯文本错误应带上原文：%v", err)
	}
}

// Catbox 系必须带 reqtype（Litterbox 还要 time），否则只回 HTTP 412。老版本
// 的缓存里没有这两个开关，读回来时要自动补齐，用户不必手填。
func TestNormalizeImageHostsRepairsCatboxFields(t *testing.T) {
	old := imageHostConfig{
		Active: "catbox",
		Hosts: []imageHost{
			// 旧版本存下来的 catbox：没有 Form，也没有 TextResponse。
			{
				ID: "catbox", Name: "Catbox", Kind: imageHostGeneric,
				Endpoint: "https://catbox.moe/user/api.php", Field: "fileToUpload",
				MaxBytes: defaultImageMaxBytes, Enabled: true, Builtin: true,
			},
			{
				ID: "litterbox", Name: "Litterbox", Kind: imageHostGeneric,
				Endpoint: "https://litterbox.catbox.moe/resources/internals/api.php",
				Field:    "fileToUpload", Form: "time=1h", // 用户改过时长，不能被覆盖
				MaxBytes: defaultImageMaxBytes, Enabled: false, Builtin: true,
			},
		},
	}
	out := normalizeImageHosts(old)
	cat := hostByID(out, "catbox")
	if cat.Form != "reqtype=fileupload" {
		t.Errorf("catbox 缺 reqtype 时应自动补上：%q", cat.Form)
	}
	if !cat.TextResponse {
		t.Error("catbox 的响应体就是地址，应自动打开纯文本模式")
	}
	lit := hostByID(out, "litterbox")
	if lit.Form != "time=1h\nreqtype=fileupload" {
		t.Errorf("应只补缺失字段、保留用户填的时长：%q", lit.Form)
	}
	// 用户自己添的、指向 Catbox 的图库同样要补齐。
	custom := clampImageHost(imageHost{
		ID: "user-1", Name: "我的 Catbox", Kind: imageHostGeneric,
		Endpoint: "https://litterbox.catbox.moe/resources/internals/api.php",
		Field:    "fileToUpload", MaxBytes: defaultImageMaxBytes, Enabled: true,
	})
	if custom.Form != "reqtype=fileupload\ntime=72h" || !custom.TextResponse {
		t.Errorf("自定义的 Catbox 项也应补齐：%q %v", custom.Form, custom.TextResponse)
	}
	// 不是 Catbox 的图库不该被塞字段。
	other := clampImageHost(imageHost{
		ID: "user-2", Name: "Kappa", Kind: imageHostGeneric,
		Endpoint: "https://kappa.lol/api/upload", Field: "file",
		MaxBytes: defaultImageMaxBytes, Enabled: true,
	})
	if other.Form != "" || other.TextResponse {
		t.Errorf("其它图库不该被改动：%q %v", other.Form, other.TextResponse)
	}
}

// 412 这类固定报错要翻成能照着改的提示，而不是把英文原文甩给用户。
func TestHostRejectionHint(t *testing.T) {
	msg, ok := hostRejectionHint(412, []byte("No request type given."))
	if !ok || !strings.Contains(msg, "reqtype=fileupload") {
		t.Errorf("缺 reqtype 的提示 = %q %v", msg, ok)
	}
	msg, ok = hostRejectionHint(412, []byte("No expire time specified."))
	if !ok || !strings.Contains(msg, "time=72h") {
		t.Errorf("缺 time 的提示 = %q %v", msg, ok)
	}
	msg, ok = hostRejectionHint(412, []byte("Precondition Failed"))
	if !ok || !strings.Contains(msg, "412") {
		t.Errorf("其它 412 也要说清是缺字段：%q %v", msg, ok)
	}
	if _, ok := hostRejectionHint(500, []byte("boom")); ok {
		t.Error("500 不该被当成缺字段")
	}
}

// 走真实上传路径时，412 的错误信息里要出现「reqtype」，而不是裸的 HTTP 412。
func TestUploadToGenericExplains412(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
		_, _ = fmt.Fprint(w, "No request type given.")
	}))
	defer srv.Close()

	a := testApp(t)
	a.client.HTTP = srv.Client()
	h := imageHost{
		ID: "catbox", Name: "Catbox", Kind: imageHostGeneric, Endpoint: srv.URL,
		Field: "fileToUpload", TextResponse: true,
		MaxBytes: defaultImageMaxBytes, Enabled: true,
	}
	_, err := a.uploadImage(context.Background(), h,
		uploadSource{Name: "a.png", Mime: "image/png", Data: pngHeadForUpload})
	if err == nil || !strings.Contains(err.Error(), "reqtype") {
		t.Errorf("412 应提示补 reqtype：%v", err)
	}
}

// parseFormFields 的值也要能引用 Token 与文件名，并把空行 / 坏行丢掉。
func TestParseFormFields(t *testing.T) {
	got := parseFormFields("reqtype=fileupload\n\nkey={{token}}\nfilename={{filename}}\n  \nbroken", "tok", "a.png")
	if len(got) != 3 {
		t.Fatalf("字段数 = %d，期望 3：%v", len(got), got)
	}
	if got[0][0] != "reqtype" || got[0][1] != "fileupload" {
		t.Errorf("第一个字段 = %v", got[0])
	}
	if got[1][0] != "key" || got[1][1] != "tok" {
		t.Errorf("Token 没有替换：%v", got[1])
	}
	if got[2][0] != "filename" || got[2][1] != "a.png" {
		t.Errorf("文件名没有替换：%v", got[2])
	}
}

// 官方图床只对 Pro 会员开放：未登录、非 Pro、还没确认都不能算就绪，
// 提示要说清楚是哪一种。
func TestOfficialImageHostRequiresPro(t *testing.T) {
	a := testApp(t)
	h := hostByID(a.hosts, "2libra")
	if a.hostReady(h) {
		t.Error("未登录时官方图床不该就绪")
	}
	if r := a.hostNotReadyReason(h); !strings.Contains(r, "登录") {
		t.Errorf("未登录提示 = %q", r)
	}
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", Username: "jimmy", TokenType: "Bearer",
	}); err != nil {
		t.Fatal(err)
	}
	if a.hostReady(h) {
		t.Error("还没确认 Pro 状态时不该就绪")
	}
	if r := a.hostNotReadyReason(h); !strings.Contains(r, "Pro") {
		t.Errorf("未确认提示 = %q", r)
	}
	a.auth.setProStatus(false)
	if a.hostReady(h) {
		t.Error("非 Pro 账号不该就绪")
	}
	if r := a.hostNotReadyReason(h); !strings.Contains(r, "Pro") || !strings.Contains(r, "Token") {
		t.Errorf("非 Pro 提示应指向免 Token 图床：%q", r)
	}
	a.auth.setProStatus(true)
	if !a.hostReady(h) {
		t.Error("Pro 账号应能用官方图床")
	}
	// 「我的」窗口读到的资料优先于缓存；但加载失败时不能当成「非 Pro」。
	a.profile.infoLoaded = true
	a.profile.infoErr = "网络错误"
	if !a.hostReady(h) {
		t.Error("资料加载失败不该把 Pro 账号判成非 Pro")
	}
	a.profile.infoErr = ""
	a.profile.user = accountProfile{Name: "jimmy", IsPro: false}
	if a.hostReady(h) {
		t.Error("资料说不是 Pro 时应以资料为准")
	}
}

// Pro 状态跟着令牌落盘：重启后不必再问一次，刷新令牌也不能把它丢掉。
func TestProStatusCachedWithToken(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok", RefreshToken: "r1", Username: "jimmy", TokenType: "Bearer",
	}); err != nil {
		t.Fatal(err)
	}
	a.auth.setProStatus(true)
	if pro, known := a.auth.proStatus(); !known || !pro {
		t.Fatalf("Pro 状态 = %v / %v", pro, known)
	}
	// 刷新令牌拿到的是新 tokenData，不带账号资料：Pro 结论要沿用。
	if err := a.auth.storeToken(tokenData{
		AccessToken: "tok2", RefreshToken: "r2", TokenType: "Bearer",
	}); err != nil {
		t.Fatal(err)
	}
	if pro, known := a.auth.proStatus(); !known || !pro {
		t.Errorf("刷新后 Pro 状态丢了：%v / %v", pro, known)
	}

	b := newApp(dir)
	if pro, known := b.auth.proStatus(); !known || !pro {
		t.Errorf("重开后 Pro 状态 = %v / %v", pro, known)
	}
	b.auth.Logout()
	if _, known := b.auth.proStatus(); known {
		t.Error("退出登录后不该还留着上一个账号的 Pro 状态")
	}
}

func TestUploadToOfficialParsesEnvelope(t *testing.T) {
	var gotPath, gotAuth string
	status := http.StatusCreated
	reply := `{"c":0,"m":"请求成功","d":{"image_key":"public/2026/11/x.png",` +
		`"image_url":"https://r2.2libra.com/public/2026/11/x.png"},"t":1}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, reply)
	}))
	defer srv.Close()

	a := testApp(t)
	a.client.Base = srv.URL
	a.client.HTTP = srv.Client()
	a.client.bearer = func() string { return "test-token" }
	src := uploadSource{Name: "photo.png", Mime: "image/png"}
	got, err := a.uploadToOfficial(context.Background(), src, pngHeadForUpload)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://r2.2libra.com/public/2026/11/x.png" {
		t.Errorf("图片地址 = %q", got)
	}
	if gotPath != "/v1/upload/image" {
		t.Errorf("上传路径 = %q", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}

	// 429 rate_limited 要留成 apiError，授权链路才能识别并动态补 scope。
	status = http.StatusTooManyRequests
	reply = `{"c":429,"code":"rate_limited","m":"今日上传配额已用尽","d":null}`
	_, err = a.uploadToOfficial(context.Background(), src, pngHeadForUpload)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != "rate_limited" || ae.Status != http.StatusTooManyRequests {
		t.Errorf("期望 rate_limited apiError，得到 %v", err)
	}
}

func TestImageHostConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	smms := hostByID(a.hosts, "smms")
	smms.Enabled = true
	smms.Token = "saved-token"
	custom := hostByID(a.hosts, "custom")
	custom.Enabled = true
	custom.Endpoint = "https://img.test/up"
	if err := a.saveImageHosts(); err != nil {
		t.Fatal(err)
	}

	// 编辑器里切换「当前使用」只落盘 Active，不把未保存的字段编辑带进去。
	hostByID(a.hosts, "smms").Token = "unsaved-token"
	a.setActiveHostByLabel("自定义图库")
	if a.hosts.Active != "custom" {
		t.Fatalf("切换后 Active = %q", a.hosts.Active)
	}

	b := newApp(dir)
	if b.hosts.Active != "custom" {
		t.Errorf("重开后当前图库 = %q，期望 custom", b.hosts.Active)
	}
	if got := hostByID(b.hosts, "smms").Token; got != "saved-token" {
		t.Errorf("未保存的编辑被写进了磁盘：%q", got)
	}
	if b.hostActiveLabel != "自定义图库" {
		t.Errorf("下拉框标签 = %q", b.hostActiveLabel)
	}
	if got := b.hostConfigSummary(); !strings.Contains(got, "自定义图库") ||
		!strings.Contains(got, "启用 9 / 18") {
		t.Errorf("配置概览 = %q", got)
	}
	b.resetImageHosts()
	c := newApp(dir)
	if c.hosts.Active != "kappa" || len(c.hostNames()) != 7 {
		t.Errorf("恢复默认失败：active=%q names=%v", c.hosts.Active, c.hostNames())
	}
}

func TestImageHostSettingsActions(t *testing.T) {
	a := testApp(t)
	before := len(a.hosts.Hosts)
	a.addCustomImageHost()
	a.addCustomImageHost()
	if len(a.hosts.Hosts) != before+2 {
		t.Fatalf("添加自定义图库失败：%d → %d", before, len(a.hosts.Hosts))
	}
	last := a.hosts.Hosts[len(a.hosts.Hosts)-1]
	if last.Builtin || last.ID == "" {
		t.Errorf("自定义项应可删除且有唯一 ID：%+v", last)
	}
	a.removeImageHost(last.ID)
	if len(a.hosts.Hosts) != before+1 {
		t.Errorf("删除自定义图库失败：%d", len(a.hosts.Hosts))
	}
	a.removeImageHost("2libra") // 内置项不可删
	if hostByID(a.hosts, "2libra") == nil || len(a.hosts.Hosts) != before+1 {
		t.Error("内置预设不该被删掉")
	}
	if !containsString(a.allHostNames(), "2Libra 官方图床") {
		t.Errorf("编辑列表缺内置项：%v", a.allHostNames())
	}
	// 删掉当前使用的那条时落到第一条启用的图库（Kappa），不是官方图床：
	// 非 Pro 账号跳过去也用不了。
	mine := &a.hosts.Hosts[len(a.hosts.Hosts)-1]
	mine.Enabled = true
	a.setActiveHostByLabel(mine.Name)
	if a.hosts.Active != mine.ID {
		t.Fatalf("切换当前图库失败：%q", a.hosts.Active)
	}
	a.removeImageHost(mine.ID)
	if a.hosts.Active != "kappa" || a.hostActiveLabel != "Kappa（免 Token）" {
		t.Errorf("删除当前图库后 = %q / %q，期望落到 Kappa", a.hosts.Active, a.hostActiveLabel)
	}
}

func TestImageURLHelpers(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(`{"data":{"0":{"url":"https://img.test/a.png"}},
		"note":"not a url","bad":"https://img.test/has space.png"}`), &v); err != nil {
		t.Fatal(err)
	}
	if got := jsonStringAt(v, "data.0.url"); got != "https://img.test/a.png" {
		t.Errorf("点分路径取值为 %q", got)
	}
	if got := jsonStringAt(v, "data.9.url"); got != "" {
		t.Errorf("越界下标应返回空串：%q", got)
	}
	if isImageURL("https://img.test/has space.png") || !isImageURL("https://img.test/a.png") {
		t.Error("URL 校验没挡住带空格的地址")
	}
	if got := findImageURL(map[string]any{
		"msg": "https://img.test/wrong.png",
		"url": "https://img.test/right.png",
	}); got != "https://img.test/right.png" {
		t.Errorf("应优先取 url 键，得到 %q", got)
	}
}
