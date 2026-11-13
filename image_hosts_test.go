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

// 默认预设要摆齐「官方 + SM.MS + Imgur + 兰空 + 自定义」，且默认配置
// 必须能直接保存：兰空 / 自定义是停用的空地址预设，校验不能把它们拦下。
func TestDefaultImageHostConfig(t *testing.T) {
	a := testApp(t)
	want := []string{"2libra", "smms", "imgur", "lsky", "custom"}
	if len(a.hosts.Hosts) != len(want) {
		t.Fatalf("默认图库数量 = %d，期望 %d", len(a.hosts.Hosts), len(want))
	}
	for i, id := range want {
		if got := a.hosts.Hosts[i].ID; got != id {
			t.Errorf("第 %d 条图库 = %q，期望 %q", i, got, id)
		}
	}
	if a.hosts.Active != "2libra" {
		t.Errorf("默认图库 = %q，期望 2libra", a.hosts.Active)
	}
	if a.hosts.Hosts[0].Kind != imageHostOfficial || !a.hosts.Hosts[0].Enabled {
		t.Error("官方图床应默认启用")
	}
	for i := 1; i < len(a.hosts.Hosts); i++ {
		if a.hosts.Hosts[i].Enabled {
			t.Errorf("预设 %q 没配好，不该默认启用", a.hosts.Hosts[i].ID)
		}
	}
	if err := a.saveImageHosts(); err != nil {
		t.Fatalf("默认配置应该能直接保存：%v", err)
	}
	names := a.hostNames()
	if len(names) != 1 || names[0] != "2Libra 官方图床" {
		t.Errorf("编辑器里只该看到官方图床：%v", names)
	}
	if a.hostReady(a.activeHost()) {
		t.Error("未登录时官方图床不该判定为就绪")
	}
	if r := a.hostNotReadyReason(a.activeHost()); !strings.Contains(r, "登录") {
		t.Errorf("未登录提示没有说清原因：%q", r)
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
	cfg.Hosts = cfg.Hosts[:2] // 模拟旧版本缓存：少了后面几条预设
	cfg.Hosts[1].Token = "my-smms-token"
	cfg.Active = "smms" // 停用项不能当「当前使用」
	cfg.Hosts = append(cfg.Hosts, imageHost{
		ID: "user-9", Name: "我的图库", Kind: imageHostGeneric, Enabled: true,
		Field: "file", Endpoint: "https://img.test/up", MaxBytes: 99 << 20,
	})
	out := normalizeImageHosts(cfg)
	if len(out.Hosts) != 6 {
		t.Fatalf("预设应补齐到 5 条 + 1 条自定义，实际 %d", len(out.Hosts))
	}
	if out.Hosts[0].ID != "2libra" || out.Hosts[5].ID != "user-9" {
		t.Errorf("内置项应在前、自定义项在后：%v", hostIDs(out))
	}
	if got := hostByID(out, "smms").Token; got != "my-smms-token" {
		t.Errorf("用户改过的内置项应保留：%q", got)
	}
	if got := hostByID(out, "user-9").MaxBytes; got != defaultImageMaxBytes {
		t.Errorf("越界的上限应夹回默认值：%d", got)
	}
	if out.Active != "2libra" {
		t.Errorf("停用的激活项应回落到第一条启用的图库：%q", out.Active)
	}
	// 官方图床也停用时，落到第一条启用的自定义图床。
	out.Hosts[0].Enabled = false
	again := imageHostConfig{Active: "2libra", Hosts: out.Hosts}
	if got := normalizedActiveID(again); got != "user-9" {
		t.Errorf("应选中第一条启用的图库：%q", got)
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
// 并且带上 Authorization 与用户自定义请求头。
func TestPostFileWritesMultipartHeaders(t *testing.T) {
	var gotField, gotFilename, gotPartType, gotAuth, gotKey string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("Content-Type 不是 multipart：%v", err)
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		part, err := mr.NextPart()
		if err != nil {
			t.Errorf("读 part 失败：%v", err)
		} else {
			gotField = part.FormName()
			gotFilename = part.FileName()
			gotPartType = part.Header.Get("Content-Type")
			gotBody, _ = io.ReadAll(part)
		}
		gotAuth = r.Header.Get("Authorization")
		gotKey = r.Header.Get("X-Api-Key")
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	body, status, err := c.postFile(context.Background(), srv.URL, "Bearer tok",
		map[string]string{"X-Api-Key": "abc"}, "image", "my photo.png", "image/png", pngHeadForUpload)
	if err != nil || status != http.StatusOK {
		t.Fatalf("postFile 失败：status=%d err=%v", status, err)
	}
	if string(body) != `{"ok":true}` {
		t.Errorf("响应体 = %q", body)
	}
	if gotField != "image" || gotFilename != "my photo.png" {
		t.Errorf("字段名 / 文件名 = %q / %q", gotField, gotFilename)
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
		!strings.Contains(got, "启用 3 / 5") {
		t.Errorf("配置概览 = %q", got)
	}
	b.resetImageHosts()
	c := newApp(dir)
	if c.hosts.Active != "2libra" || len(c.hostNames()) != 1 {
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
