package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 图库（图床）配置与图片上传协议。
//
// 编辑器只认识一件事：把一张本地图片交给「当前图库」，换回一个 https URL，
// 然后写进 Markdown。至于这个 URL 是 2Libra 官方上传接口给的，还是用户自己
// 的 SM.MS / Imgur / 自建兰空图床给的，由这里的配置决定，界面和上传队列不
// 关心。
//
// 内置预设覆盖几类常见协议，另外可以让用户手工添加任意「multipart 上传 +
// JSON 响应」的图床：给接口地址、文件字段名、可选请求头与返回 URL 的取值
// 路径即可；路径留空时会在响应里自动找第一个图片地址。
//
// 官方接口（api-economy.md「上传」）：
//
//	POST /v1/upload/image   scope upload
//	Content-Type: multipart/form-data，字段名 file
//	JPEG / PNG / WebP，单张 ≤10 MB，每人每天 20 张
//	201 → {c,m,d:{image_key,image_url},t}
//	配额用尽 → 429 rate_limited

const (
	imageHostKVKey = "imagehosts:v1"

	defaultImageMaxBytes int64 = 10 << 20 // 官方上限 10 MB
	maxImageMaxBytes     int64 = 20 << 20 // 自建图床允许放大到 20 MB
	minImageMaxBytes     int64 = 1 << 20
	imageUploadTimeout         = 30 * time.Second
)

type imageHostKind string

const (
	// imageHostOfficial 走 2Libra 自己的 /v1/upload/image 与 {c,m,d,t} 信封，
	// 令牌来自应用登录态，缺 upload scope 时由全局授权链路补上。
	imageHostOfficial imageHostKind = "official"
	// imageHostGeneric 是通用的 multipart + JSON 图床。
	imageHostGeneric imageHostKind = "generic"
)

// imageHost 是一条图库配置。Builtin 的内置项可以改字段 / 停用，但不能删除；
// 用户手工添加的项 Builtin=false，可以改也可以删。
type imageHost struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Kind     imageHostKind `json:"kind"`
	Endpoint string        `json:"endpoint,omitempty"`
	Field    string        `json:"field,omitempty"`
	Header   string        `json:"header,omitempty"` // "Authorization: Bearer {{token}}"，一行一个
	Token    string        `json:"token,omitempty"`
	URLPath  string        `json:"url_path,omitempty"` // 点分路径，如 data.url
	MaxBytes int64         `json:"max_bytes,omitempty"`
	Enabled  bool          `json:"enabled,omitempty"`
	Builtin  bool          `json:"builtin,omitempty"`
	Note     string        `json:"note,omitempty"`
}

type imageHostConfig struct {
	Active string      `json:"active"`
	Hosts  []imageHost `json:"hosts"`
}

// defaultImageHostConfig 是首次运行时的一组预设：官方图床直接可用，其余几个
// 默认停用，用户填上 Token / 地址再启用。这样「默认常用的几个」摆在设置里，
// 又不会让没配好的图床出现在编辑器的选择框里。
func defaultImageHostConfig() imageHostConfig {
	return imageHostConfig{
		Active: "2libra",
		Hosts: []imageHost{
			{
				ID: "2libra", Name: "2Libra 官方图床", Kind: imageHostOfficial,
				Field: "file", MaxBytes: defaultImageMaxBytes, Enabled: true, Builtin: true,
				Note: "登录 2Libra 即可使用；需要 upload 权限，每人每天 20 张。",
			},
			{
				ID: "smms", Name: "SM.MS", Kind: imageHostGeneric,
				Endpoint: "https://sm.ms/api/v2/upload", Field: "smfile",
				Header: "Authorization: {{token}}", URLPath: "data.url",
				MaxBytes: defaultImageMaxBytes, Enabled: false, Builtin: true,
				Note: "在 SM.MS 账户页生成 API Token 后填入。",
			},
			{
				ID: "imgur", Name: "Imgur", Kind: imageHostGeneric,
				Endpoint: "https://api.imgur.com/3/image", Field: "image",
				Header: "Authorization: Client-ID {{token}}", URLPath: "data.link",
				MaxBytes: defaultImageMaxBytes, Enabled: false, Builtin: true,
				Note: "填入应用的 Client-ID（匿名上传）。",
			},
			{
				ID: "lsky", Name: "兰空图床 / Chevereto 兼容", Kind: imageHostGeneric,
				Field: "file", Header: "Authorization: Bearer {{token}}",
				URLPath: "data.url", MaxBytes: defaultImageMaxBytes, Enabled: false, Builtin: true,
				Note: "自建图床：填接口地址（如 https://example.com/api/v1/upload）与 Token。",
			},
			{
				ID: "custom", Name: "自定义图库", Kind: imageHostGeneric,
				Field: "file", URLPath: "", MaxBytes: defaultImageMaxBytes,
				Enabled: false, Builtin: true,
				Note: "任意 multipart 上传服务：填接口地址、字段名；URL 路径留空会自动在响应里找。",
			},
		},
	}
}

// normalizeImageHosts 把磁盘上读回的配置补全成当前版本认识的样子：
// 内置项按 ID 补齐（用户改过的内置项保留他的修改），自定义项原样保留，
// 上限与激活项都夹到合法范围。损坏的缓存不该让编辑器拿不到任何图床。
func normalizeImageHosts(cfg imageHostConfig) imageHostConfig {
	defaults := defaultImageHostConfig()
	byID := make(map[string]imageHost, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		if h.ID != "" {
			byID[h.ID] = h
		}
	}
	out := make([]imageHost, 0, len(cfg.Hosts)+len(defaults.Hosts))
	// 内置项按预设顺序排在前面；用户添加的排在后面。
	for _, d := range defaults.Hosts {
		h := d
		if saved, ok := byID[d.ID]; ok {
			saved.Kind = d.Kind
			saved.Builtin = true
			if strings.TrimSpace(saved.Name) == "" {
				saved.Name = d.Name
			}
			h = saved
		}
		out = append(out, clampImageHost(h))
		delete(byID, d.ID)
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	// map 顺序不稳定，自定义项按 ID 排，界面里每次打开顺序一致。
	sortStrings(ids)
	for _, id := range ids {
		h := byID[id]
		if h.Kind == "" {
			h.Kind = imageHostGeneric
		}
		h.Builtin = false
		out = append(out, clampImageHost(h))
	}
	cfg.Hosts = out
	cfg.Active = normalizedActiveID(cfg)
	return cfg
}

// normalizedActiveID 保证「当前使用」指向一条确实启用的图库。官方图床被
// 用户停用时优先选第一条启用的第三方图床，别把光标留在一条用不了的项上。
func normalizedActiveID(cfg imageHostConfig) string {
	if h := hostByID(cfg, cfg.Active); h != nil && h.Enabled {
		return cfg.Active
	}
	for _, h := range cfg.Hosts {
		if h.Enabled {
			return h.ID
		}
	}
	return "2libra"
}

func clampImageHost(h imageHost) imageHost {
	if h.Kind == "" {
		h.Kind = imageHostGeneric
	}
	if h.Field == "" {
		h.Field = "file"
	}
	if h.MaxBytes < minImageMaxBytes || h.MaxBytes > maxImageMaxBytes {
		h.MaxBytes = defaultImageMaxBytes
	}
	h.Name = strings.TrimSpace(h.Name)
	h.Endpoint = strings.TrimSpace(h.Endpoint)
	return h
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func hostByID(cfg imageHostConfig, id string) *imageHost {
	for i := range cfg.Hosts {
		if cfg.Hosts[i].ID == id {
			return &cfg.Hosts[i]
		}
	}
	return nil
}

// loadImageHosts 从离线缓存读回配置。没有记录、记录损坏时退回内置默认，
// 不写盘：用户还没改过设置，就没必要产生一份配置。
func (a *app) loadImageHosts() {
	var cfg imageHostConfig
	if a.kv == nil {
		a.hosts = normalizeImageHosts(defaultImageHostConfig())
		return
	}
	if _, ok := a.kv.get(imageHostKVKey, &cfg); !ok {
		cfg = defaultImageHostConfig()
	}
	a.hosts = normalizeImageHosts(cfg)
	a.hostActiveLabel = a.hostLabelByID(a.hosts.Active)
}

func (a *app) saveImageHosts() error {
	if err := a.validateImageHostConfig(); err != nil {
		return err
	}
	a.hosts = normalizeImageHosts(a.hosts)
	a.hostActiveLabel = a.hostLabelByID(a.hosts.Active)
	if a.kv == nil {
		return nil
	}
	return a.kv.put(imageHostKVKey, a.hosts, time.Now())
}

func (a *app) validateImageHostConfig() error {
	seen := make(map[string]bool, len(a.hosts.Hosts))
	for _, h := range a.hosts.Hosts {
		if h.ID == "" || seen[h.ID] {
			return fmt.Errorf("图库配置有重复或空的标识，请恢复默认后重配")
		}
		seen[h.ID] = true
		if err := validateImageHost(h); err != nil {
			return fmt.Errorf("%s：%w", h.Name, err)
		}
	}
	return nil
}

func validateImageHost(h imageHost) error {
	if strings.TrimSpace(h.Name) == "" {
		return errors.New("名称不能为空")
	}
	if h.Kind != imageHostOfficial && h.Kind != imageHostGeneric {
		return errors.New("类型只能是官方或通用图床")
	}
	if h.Kind == imageHostOfficial {
		return nil
	}
	// 停用的内置预设（兰空 / 自定义）接口地址本来就是空的，要等用户填好再
	// 启用。只校验已启用的项，否则默认配置一打开设置就永远保存不了。
	if !h.Enabled {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(h.Endpoint))
	if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" {
		return errors.New("接口地址要写成 https://… 这样的完整地址")
	}
	if strings.TrimSpace(h.Field) == "" {
		return errors.New("文件字段名不能为空")
	}
	for _, line := range strings.Split(strings.ReplaceAll(h.Header, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, _, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return errors.New("请求头要写成「名称: 值」，一行一个")
		}
	}
	if h.URLPath != "" {
		for _, part := range strings.Split(h.URLPath, ".") {
			if strings.TrimSpace(part) == "" {
				return errors.New("URL 路径用点分隔，不能出现空段")
			}
		}
	}
	return nil
}

// hostReady 判断一条图床是否真的能发请求：没启用、没填地址、或请求头里
// 要 Token 却没填，都不算就绪。
func (a *app) hostReady(h *imageHost) bool {
	if h == nil || !h.Enabled {
		return false
	}
	if h.Kind == imageHostOfficial {
		return a.auth != nil && a.auth.loggedIn()
	}
	if strings.TrimSpace(h.Endpoint) == "" {
		return false
	}
	return !strings.Contains(h.Header, "{{token}}") || strings.TrimSpace(h.Token) != ""
}

func (a *app) activeHost() *imageHost {
	return hostByID(a.hosts, a.hosts.Active)
}

func (a *app) hostByLabel(label string) *imageHost {
	for i := range a.hosts.Hosts {
		if a.hosts.Hosts[i].Name == label {
			return &a.hosts.Hosts[i]
		}
	}
	return nil
}

func (a *app) hostLabelByID(id string) string {
	if h := hostByID(a.hosts, id); h != nil {
		return h.Name
	}
	if h := hostByID(defaultImageHostConfig(), "2libra"); h != nil {
		return h.Name
	}
	return ""
}

// hostNames 是编辑器里可选图库的名字列表（只列已启用的）。
func (a *app) hostNames() []string {
	out := make([]string, 0, len(a.hosts.Hosts))
	for _, h := range a.hosts.Hosts {
		if h.Enabled {
			out = append(out, h.Name)
		}
	}
	return out
}

// ------------------------------------------------------------------
// 图片校验
// ------------------------------------------------------------------

// uploadSource 是一张待上传的图：要么来自拖拽 / 文件选择的本地路径，要么
// 来自剪贴板里的 PNG 字节。两者共用同一条上传队列。
type uploadSource struct {
	Name string
	Path string
	Data []byte
	Mime string
}

// inspectImage 校验大小与真实格式（只看文件头，不信扩展名），并补好
// multipart 要用的文件名与 Content-Type。
func inspectImage(src uploadSource, maxBytes int64) (uploadSource, error) {
	src.Name = strings.TrimSpace(src.Name)
	if src.Name == "" {
		src.Name = "image"
	}
	var head []byte
	var size int64
	if src.Path != "" {
		info, err := os.Stat(src.Path)
		if err != nil {
			return src, fmt.Errorf("读取图片失败：%v", err)
		}
		if info.IsDir() {
			return src, errors.New("拖进来的是文件夹，不是图片")
		}
		size = info.Size()
		f, err := os.Open(src.Path)
		if err != nil {
			return src, fmt.Errorf("读取图片失败：%v", err)
		}
		defer f.Close()
		head = make([]byte, 16)
		n, _ := io.ReadFull(f, head)
		head = head[:n]
	} else {
		size = int64(len(src.Data))
		head = src.Data
		if len(head) > 16 {
			head = head[:16]
		}
	}
	if maxBytes <= 0 || maxBytes > maxImageMaxBytes {
		maxBytes = defaultImageMaxBytes
	}
	if size > maxBytes {
		return src, fmt.Errorf("超过 %d MB 上限（%.1f MB）",
			maxBytes>>20, float64(size)/(1<<20))
	}
	mime := detectImageMIME(head)
	if mime == "" {
		return src, errors.New("只支持 JPEG / PNG / WebP 图片")
	}
	src.Mime = mime
	ext := strings.ToLower(filepath.Ext(src.Name))
	want := map[string]string{"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp"}[mime]
	if ext != want && !(mime == "image/jpeg" && ext == ".jpeg") {
		src.Name = strings.TrimSuffix(src.Name, ext) + want
	}
	return src, nil
}

func detectImageMIME(head []byte) string {
	switch {
	case len(head) >= 3 && head[0] == 0xff && head[1] == 0xd8 && head[2] == 0xff:
		return "image/jpeg"
	case len(head) >= 8 && bytes.Equal(head[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png"
	case len(head) >= 12 && string(head[:4]) == "RIFF" && string(head[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

func (s uploadSource) bytes() ([]byte, error) {
	if s.Path == "" {
		if len(s.Data) == 0 {
			return nil, errors.New("图片内容为空")
		}
		return s.Data, nil
	}
	return os.ReadFile(s.Path)
}

// ------------------------------------------------------------------
// multipart 请求与响应解码
// ------------------------------------------------------------------

// postFile 发一次 multipart/form-data 上传，原样返回响应体与状态码。
// 官方接口是 {c,m,d,t} 信封，第三方图床各有各的 JSON，所以解码交给调用方。
func (c *Client) postFile(ctx context.Context, u, authorization string,
	headers map[string]string, field, filename, contentType string, data []byte) ([]byte, int, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	// CreateFormFile 把 part 的 Content-Type 写死成 application/octet-stream，
	// 有些第三方图床会据此拒绝 JPEG / WebP。这里自己建 part，带上 inspectImage
	// 按文件头判出来的真实类型；字段名与文件名按 RFC 7578 转义。
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`,
		escapeMultipartQuote(field), escapeMultipartQuote(filename)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	partHeader.Set("Content-Type", contentType)
	part, err := w.CreatePart(partHeader)
	if err != nil {
		return nil, 0, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, 0, err
	}
	if err := w.Close(); err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &buf)
	if err != nil {
		return nil, 0, err
	}
	ua := c.UA
	if ua == "" {
		ua = "2libra-native-reader/0.1 (+mygo)"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", w.FormDataContentType())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return body, resp.StatusCode, err
}

// CRLF 要放在 \r / \n 前面：Replacer 按参数顺序匹配，先处理整组换行才
// 不会在文件名里留下两个连续空格。
var multipartQuote = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r\n", " ", "\r", " ", "\n", " ")

func escapeMultipartQuote(s string) string { return multipartQuote.Replace(s) }

// uploadImage 按图床类型把一张图传上去，返回可直接写进 Markdown 的 URL。
// 单张图给一条独立的超时：慢图床不该把后面排队的图片一起拖死。
func (a *app) uploadImage(ctx context.Context, h imageHost, src uploadSource) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, imageUploadTimeout)
	defer cancel()
	data, err := src.bytes()
	if err != nil {
		return "", err
	}
	if h.Kind == imageHostOfficial {
		return a.uploadToOfficial(ctx, src, data)
	}
	return a.uploadToGeneric(ctx, h, src, data)
}

// uploadToOfficial 沿用读端点的授权重试：401 会刷新一次令牌再重传。
// 上传接口没有幂等键（文档没有标 🔑），刷新令牌重传是安全的。
func (a *app) uploadToOfficial(ctx context.Context, src uploadSource, data []byte) (string, error) {
	return withAuthRetry(a.client, ctx, func(token string) (string, error) {
		auth := ""
		if token != "" {
			auth = "Bearer " + token
		}
		body, status, err := a.client.postFile(ctx,
			a.client.apiURL("/v1/upload/image"), auth, nil,
			"file", src.Name, src.Mime, data)
		if err != nil {
			return "", err
		}
		var env envelope
		if err := json.Unmarshal(body, &env); err != nil {
			return "", fmt.Errorf("上传响应不是 {c,m,d,t} 信封（HTTP %d）：%w", status, err)
		}
		if status >= 400 || env.C != 0 {
			return "", &apiError{
				Status:        status,
				Code:          env.Code,
				Message:       env.M,
				RequiredScope: env.RequiredScope,
				RetryAfter:    env.RetryAfter,
			}
		}
		u := asObject(env.D).str("image_url", "imageUrl", "url")
		if !isImageURL(u) {
			return "", errors.New("上传成功但响应里没有图片地址")
		}
		return u, nil
	})
}

// uploadToGeneric 是通用图床：请求头由配置拼出，返回 JSON 里按配置路径取
// 地址；路径留空时自动找响应里第一个 http(s) 图片地址。
func (a *app) uploadToGeneric(ctx context.Context, h imageHost, src uploadSource, data []byte) (string, error) {
	headers := make(map[string]string)
	authorization := ""
	for _, line := range strings.Split(strings.ReplaceAll(h.Header, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, _ := strings.Cut(line, ":")
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		value = strings.ReplaceAll(value, "{{token}}", h.Token)
		if strings.EqualFold(name, "Authorization") {
			authorization = value
			continue
		}
		headers[name] = value
	}
	body, status, err := a.client.postFile(ctx, h.Endpoint, authorization, headers,
		h.Field, src.Name, src.Mime, data)
	if err != nil {
		return "", err
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return "", fmt.Errorf("图库返回的不是 JSON（HTTP %d）：%w", status, err)
	}
	if o, ok := v.(map[string]any); ok {
		if success, has := o["success"].(bool); has && !success {
			msg, _ := o["message"].(string)
			if msg == "" {
				msg, _ = o["msg"].(string)
			}
			if msg == "" {
				msg = "图库拒绝上传"
			}
			return "", errors.New(msg)
		}
		if code, ok := o["code"].(string); ok && code != "" && code != "200" && code != "0" && status >= 400 {
			msg, _ := o["message"].(string)
			return "", fmt.Errorf("图库返回 %s：%s", code, msg)
		}
	}
	if status >= 400 {
		return "", fmt.Errorf("图库返回 HTTP %d", status)
	}
	u := jsonStringAt(v, h.URLPath)
	if !isImageURL(u) {
		u = findImageURL(v)
	}
	if !isImageURL(u) {
		return "", errors.New("图库响应里找不到图片地址，请检查「URL 路径」配置")
	}
	return u, nil
}

// jsonStringAt 按点分路径取字符串；数组下标写成数字，如 data.0.url。
func jsonStringAt(v any, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	cur := v
	for _, part := range strings.Split(path, ".") {
		switch x := cur.(type) {
		case map[string]any:
			cur = x[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return ""
			}
			cur = x[i]
		default:
			return ""
		}
	}
	s, _ := cur.(string)
	return s
}

// findImageURL 在任意 JSON 里找第一个像图片地址的 http(s) 字符串：优先
// url / image_url / link / src 这些常见键，其次才是任意字符串。
func findImageURL(v any) string {
	preferred := []string{"url", "image_url", "imageUrl", "link", "src", "original", "path"}
	var found string
	var walk func(any, int)
	walk = func(x any, depth int) {
		if found != "" || depth > 8 {
			return
		}
		switch t := x.(type) {
		case map[string]any:
			for _, k := range preferred {
				if s, ok := t[k].(string); ok && isImageURL(s) {
					found = s
					return
				}
			}
			for _, val := range t {
				walk(val, depth+1)
			}
		case []any:
			for _, val := range t {
				walk(val, depth+1)
			}
		case string:
			if isImageURL(t) {
				found = t
			}
		}
	}
	walk(v, 0)
	return found
}

func isImageURL(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") {
		return false
	}
	return !strings.ContainsAny(s, " \t\r\n\\\"") && len(s) < 2048
}
