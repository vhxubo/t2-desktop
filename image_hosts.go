package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
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
// 预设按「免 Token 优先」排：Kappa、MJJ.Today、ImgBB、Photo.Lily、Appinn、
// Tikolu、111666.best、Skyimg 都不用注册、不用填 Token，装好就能传，因此
// 排在前面并默认启用；2Libra 官方图床排在其后，只对 Pro 会员开放（见
// hostReady / hostNotReadyReason）；StarDots 要 Key + Secret、Imgur 要
// Client-ID、SM.MS 与兰空要 Token，默认停用，填好再启用。Catbox / Litterbox
// / x0.at 也免 Token，但分别受「国内直连不通」「只能留 72 小时」「拒绝部分
// 网络」的限制，默认停用并在 Note 里写清。设置里可以点「测试这个图床」当场
// 验证一条配置是否还能用。
//
// 上传协议有两条路：
//
//  1. 通用协议（Adapter 留空）：「multipart 上传 + 取回图片地址」，两种响应
//     形态都支持——JSON 按「URL 路径」（如 data.url）取值，留空时自动在响应
//     里找第一个 http(s) 图片地址；纯文本（TextResponse）则整个响应体就是
//     地址，如 Catbox 与 x0.at。需要额外表单字段的接口（Catbox 的
//     reqtype=fileupload）写在 Form 里，一行一个 name=value，值里可以写
//     {{token}} 与 {{filename}}（当前这张图的名字）。
//  2. 内置协议（Adapter 非空）：MJJ.Today / ImgBB 要先扒上传页里的
//     auth_token，Skyimg 要先取 CSRF 令牌，Tikolu 只回图片 ID，StarDots 要
//     现签一个 MD5 签名，Imgur 与 111666.best 在多个公开凭据之间轮换——通用
//     字段拼不出这些，见 image_hosts_adapters.go。
//
// 两条路拿到的地址如果不是完整的 http(s)（Photo.Lily / Appinn / 111666.best
// 会回 /img/xxx.png 这种站内路径），统一按接口地址补全，否则写进 Markdown
// 就是死链。
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
	// 令牌来自应用登录态，缺 upload scope 时由全局授权链路补上。它只对 Pro
	// 会员开放，判断见 app.hostReady。
	imageHostOfficial imageHostKind = "official"
	// imageHostGeneric 是通用的 multipart + JSON / 纯文本图床。
	imageHostGeneric imageHostKind = "generic"
)

// imageHost 是一条图库配置。Builtin 的内置项可以改字段 / 停用，但不能删除；
// 用户手工添加的项 Builtin=false，可以改也可以删。
type imageHost struct {
	ID   string        `json:"id"`
	Name string        `json:"name"`
	Kind imageHostKind `json:"kind"`
	// Adapter 选一套内置上传协议（见 image_hosts_adapters.go）。留空表示按
	// 下面的通用字段拼一次 multipart 请求。
	Adapter  string `json:"adapter,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Field    string `json:"field,omitempty"`
	Header   string `json:"header,omitempty"` // "Authorization: Bearer {{token}}"，一行一个
	Token    string `json:"token,omitempty"`
	// Secret / Bucket 只有 StarDots 用：它要 API Key（Token）、API Secret 与
	// 存储桶名三样才能签出上传请求。
	Secret string `json:"secret,omitempty"`
	Bucket string `json:"bucket,omitempty"`
	// Form 是随文件一起提交的附加表单字段，一行一个 name=value。Catbox 这类
	// 接口要靠 reqtype=fileupload 说明这是一次上传，值里可以写 {{token}} 与
	// {{filename}}。
	Form    string `json:"form,omitempty"`
	URLPath string `json:"url_path,omitempty"` // 点分路径，如 data.url
	// TextResponse 表示接口成功时直接返回图片地址本身（Catbox、0x0.st），
	// 而不是 JSON。
	TextResponse bool   `json:"text_response,omitempty"`
	MaxBytes     int64  `json:"max_bytes,omitempty"`
	Enabled      bool   `json:"enabled,omitempty"`
	Builtin      bool   `json:"builtin,omitempty"`
	Note         string `json:"note,omitempty"`
}

type imageHostConfig struct {
	Active string      `json:"active"`
	Hosts  []imageHost `json:"hosts"`
}

// defaultImageHostConfig 是首次运行时的一组预设，按「免 Token 优先」排：
// 不用注册、不用 Token 的图床排在前面并默认启用，Kappa 当「当前使用」；
// 官方图床排在其后（只对 Pro 开放，默认也启用，Pro 用户可以直接切过去）；
// 需要 Key / Client-ID / Token 的 StarDots、Imgur、SM.MS 与兰空默认停用，
// 填好再启用。
//
// 免费图床更迭很快：0x0.st 已停止上传、uguu.se 长期 502、catbox.moe 在
// 国内直连不通，所以这里只保留实测能用的几家，并把临时 / 需要代理的情况
// 写进 Note。用户还可以在设置里点「测试这个图床」当场验证。
//
// 带 Adapter 的几条来自「通用图片上传助手」脚本（utags/userscripts），接口
// 形态与通用 multipart 不同，实现见 image_hosts_adapters.go；它们默认不带
// 接口地址，留空即用内置地址，也可以在设置里改成镜像站。
func defaultImageHostConfig() imageHostConfig {
	return imageHostConfig{
		Active: "kappa",
		Hosts: []imageHost{
			{
				ID: "kappa", Name: "Kappa（免 Token）", Kind: imageHostGeneric,
				Endpoint: "https://kappa.lol/api/upload", Field: "file",
				URLPath: "link", MaxBytes: defaultImageMaxBytes, Enabled: true, Builtin: true,
				Note: "免注册、免 Token，响应里的 link 就是图片直链；站点在境外，国内网络可能需要代理。",
			},
			{
				ID: "mjj", Name: "MJJ.Today（免 Token）", Kind: imageHostGeneric,
				Adapter: adapterMJJ, MaxBytes: defaultImageMaxBytes, Enabled: true, Builtin: true,
				Note: "免注册：先取上传页里的 auth_token 再提交，单张 10 MB 以内；站点在境外，国内网络可能需要代理。",
			},
			{
				ID: "imgbb", Name: "ImgBB（免 Token）", Kind: imageHostGeneric,
				Adapter: adapterImgBB, MaxBytes: maxImageMaxBytes, Enabled: true, Builtin: true,
				Note: "免注册，与 MJJ.Today 同一套 Chevereto 接口，单张 32 MB 以内；站点在境外。",
			},
			{
				ID: "photo_lily", Name: "Photo.Lily（免 Token）", Kind: imageHostGeneric,
				Endpoint: "https://photo.lily.lat/upload", Field: "file",
				URLPath: "0.src", MaxBytes: defaultImageMaxBytes, Enabled: true, Builtin: true,
				Note: "免注册，响应是数组、src 有时是站内路径，应用会补成完整地址；小站，随时可能下线。",
			},
			{
				ID: "appinn", Name: "Appinn 图床（免 Token）", Kind: imageHostGeneric,
				Endpoint: "https://h1.appinn.me/upload?authCode=appinn2&serverCompress=false" +
					"&uploadChannel=telegram&uploadNameType=default&autoRetry=true",
				Field: "file", Form: "filename={{filename}}", URLPath: "0.src",
				MaxBytes: maxImageMaxBytes, Enabled: true, Builtin: true,
				Note: "小众软件论坛的图床，免注册，文件走 Telegram 通道，单张 20 MB 以内。",
			},
			{
				ID: "tikolu", Name: "Tikolu（免 Token）", Kind: imageHostGeneric,
				Adapter: adapterTikolu, MaxBytes: 8 << 20, Enabled: true, Builtin: true,
				Note: "免注册，接口只回图片 ID，地址由应用拼成 https://tikolu.net/i/<ID>；单张 8 MB 以内。",
			},
			{
				ID: "111666_best", Name: "111666.best（免 Token）", Kind: imageHostGeneric,
				Adapter: adapter111666, MaxBytes: defaultImageMaxBytes, Enabled: true, Builtin: true,
				Note: "免注册：内置了几个公共令牌，失败时自动换下一个；站点在境外。",
			},
			{
				ID: "skyimg", Name: "Skyimg（免 Token）", Kind: imageHostGeneric,
				Adapter: adapterSkyimg, MaxBytes: maxImageMaxBytes, Enabled: true, Builtin: true,
				Note: "免注册：先取 CSRF 令牌再上传，单张 100 MB 以内（受应用上限约束）。",
			},
			{
				ID: "skyimg_webp", Name: "Skyimg WebP（免 Token）", Kind: imageHostGeneric,
				Adapter: adapterSkyimgWebp, MaxBytes: maxImageMaxBytes, Enabled: false, Builtin: true,
				Note: "同一站点，上传时转成 WebP 再存；需要更小的图时启用这一条。",
			},
			{
				ID: "litterbox", Name: "Litterbox（免 Token · 临时）", Kind: imageHostGeneric,
				Endpoint: "https://litterbox.catbox.moe/resources/internals/api.php",
				Field:    "fileToUpload", Form: "reqtype=fileupload\ntime=72h",
				TextResponse: true, MaxBytes: defaultImageMaxBytes,
				Enabled: false, Builtin: true,
				Note: "Catbox 的临时区：免注册，响应体就是图片地址，但文件最多只留 72 小时。",
			},
			{
				ID: "catbox", Name: "Catbox（免 Token）", Kind: imageHostGeneric,
				Endpoint: "https://catbox.moe/user/api.php", Field: "fileToUpload",
				Form: "reqtype=fileupload", TextResponse: true,
				MaxBytes: defaultImageMaxBytes, Enabled: false, Builtin: true,
				Note: "免注册、文件长期保存；国内直连通常不通（TLS 会被断开），需要走代理。",
			},
			{
				ID: "x0", Name: "x0.at（免 Token）", Kind: imageHostGeneric,
				Endpoint: "https://x0.at", Field: "file", TextResponse: true,
				MaxBytes: defaultImageMaxBytes, Enabled: false, Builtin: true,
				Note: "0x0.st 的镜像：免注册，响应体就是图片地址；部分网络（机房 IP）会被拒绝上传。",
			},
			{
				ID: "2libra", Name: "2Libra 官方图床", Kind: imageHostOfficial,
				Field: "file", MaxBytes: defaultImageMaxBytes, Enabled: true, Builtin: true,
				Note: "仅 Pro 会员可用；登录 2Libra 后自动确认身份，每人每天 20 张。",
			},
			{
				ID: "stardots", Name: "StarDots", Kind: imageHostGeneric,
				Adapter: adapterStarDots, MaxBytes: maxImageMaxBytes, Enabled: false, Builtin: true,
				Note: "要 API Key、API Secret 与存储桶名（在 StarDots 控制台的「Key and Secret」页获取）；签名上传。",
			},
			{
				ID: "imgur", Name: "Imgur", Kind: imageHostGeneric,
				Adapter: adapterImgur, MaxBytes: maxImageMaxBytes, Enabled: false, Builtin: true,
				Note: "填自己的 Client-ID 最稳；不填时用内置的几个公共 Client-ID 轮换，随时可能失效。",
			},
			{
				ID: "smms", Name: "SM.MS", Kind: imageHostGeneric,
				Endpoint: "https://sm.ms/api/v2/upload", Field: "smfile",
				Header: "Authorization: {{token}}", URLPath: "data.url",
				MaxBytes: defaultImageMaxBytes, Enabled: false, Builtin: true,
				Note: "在 SM.MS 账户页生成 API Token 后填入。",
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

// retiredImageHostIDs 是已经下线的内置预设：0x0.st 停止上传、uguu.se 长期
// 502，留着只会让用户白试一次。旧缓存里的这两条直接丢掉，既不变成用户删得
// 掉的自定义项，「当前使用」也会回落到第一条可用的图床。
var retiredImageHostIDs = map[string]bool{"0x0": true, "uguu": true}

// normalizeImageHosts 把磁盘上读回的配置补全成当前版本认识的样子：
// 内置项按 ID 补齐（用户改过的内置项保留他的修改），自定义项原样保留，
// 上限与激活项都夹到合法范围。损坏的缓存不该让编辑器拿不到任何图床。
func normalizeImageHosts(cfg imageHostConfig) imageHostConfig {
	defaults := defaultImageHostConfig()
	byID := make(map[string]imageHost, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		if h.ID != "" && !retiredImageHostIDs[h.ID] {
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
	// 一条都没启用时也别返回一个不存在的 ID：界面的下拉框与标签都按 ID
	// 取名字，退回第一条（预设里是免 Token 的 Kappa）。
	if len(cfg.Hosts) > 0 {
		return cfg.Hosts[0].ID
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
	h.Form = normalizeFormLines(h.Form)
	repairCatboxHost(&h)
	return h
}

// repairCatboxHost 补齐 Catbox 系的必填项。这两条接口的响应体就是一个地址，
// 而且必须带上 reqtype（Litterbox 还要 time），否则直接回 HTTP 412；老版本
// 的配置里既没有「附加表单字段」也没有「纯文本响应」这两个开关，读回来时补
// 上，用户才不会只看到一句 412 或者「返回的不是 JSON」。
func repairCatboxHost(h *imageHost) {
	if !strings.Contains(h.Endpoint, "catbox.moe") {
		return
	}
	h.TextResponse = true
	h.Form = ensureFormField(h.Form, "reqtype", "fileupload")
	if strings.Contains(h.Endpoint, "litterbox.") {
		h.Form = ensureFormField(h.Form, "time", "72h")
	}
}

// ensureFormField 只在缺这个字段时追加一行，用户自己填过的值不动。
func ensureFormField(form, name, value string) string {
	for _, line := range strings.Split(form, "\n") {
		if k, _, ok := strings.Cut(strings.TrimSpace(line), "="); ok &&
			strings.EqualFold(strings.TrimSpace(k), name) {
			return form
		}
	}
	if strings.TrimSpace(form) == "" {
		return name + "=" + value
	}
	return form + "\n" + name + "=" + value
}

// normalizeFormLines 把附加表单字段整理成一行一个 name=value：去掉首尾空白
// 与空行，避免从别处粘贴时带进来多余换行。
func normalizeFormLines(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := lines[:0]
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
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
	for _, line := range strings.Split(strings.ReplaceAll(h.Form, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, _, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return errors.New("附加表单字段要写成「名称=值」，一行一个")
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
// 要 Token 却没填，都不算就绪。官方图床还要求账号是 Pro 会员 —— 它只对 Pro
// 开放，而 Pro 状态要等账号资料读回来，未确认时同样按不可用处理。
func (a *app) hostReady(h *imageHost) bool {
	if h == nil || !h.Enabled {
		return false
	}
	if h.Kind == imageHostOfficial {
		if a.auth == nil || !a.auth.loggedIn() {
			return false
		}
		pro, known := a.proStatus()
		return known && pro
	}
	if strings.TrimSpace(h.Endpoint) == "" {
		return false
	}
	return !strings.Contains(h.Header, "{{token}}") || strings.TrimSpace(h.Token) != ""
}

// proStatus 返回当前账号是不是 Pro，以及这个结论是否已经确认。官方图床只对
// Pro 开放，但 Pro 状态来自账号资料，不能等用户打开「我的」窗口：登录时、
// 启动后都会主动读一次，结果缓存在 token.json；「我的」窗口读过更新鲜的
// 资料时以它为准。
func (a *app) proStatus() (pro, known bool) {
	// 只有真的读到资料才作数：加载失败时 infoLoaded 也是 true，但 user 还是
	// 零值，照它的 IsPro 会把 Pro 用户误判成非 Pro。
	if ps := a.profile; ps != nil && ps.infoLoaded && ps.infoErr == "" && ps.user.Name != "" {
		return ps.user.IsPro, true
	}
	if a.auth == nil {
		return false, false
	}
	return a.auth.proStatus()
}

// refreshProStatus 读一次当前账号的公开资料，把 is_pro 记进令牌缓存。force
// 为 false 时只在还没确认过的情况下才发请求（启动时用），force 为 true 时
// 不管缓存直接重读（刚登录、或用户在设置里点了「重新确认」）。
func (a *app) refreshProStatus(force bool) {
	if a.auth == nil || !a.autoLoad || a.proChecking || !a.auth.loggedIn() {
		return
	}
	if !force {
		if _, known := a.proStatus(); known {
			return
		}
	}
	name := a.auth.username()
	if name == "" {
		return
	}
	a.proChecking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		p, err := a.client.fetchAccountProfile(ctx, name)
		a.publish(func() {
			a.proChecking = false
			if err != nil || p.Name == "" {
				return
			}
			a.auth.setProStatus(p.IsPro)
		})
	}()
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
// 官方接口是 {c,m,d,t} 信封，第三方图床各有各的 JSON（也有直接回地址的
// 纯文本），所以解码交给调用方。form 是随文件一起提交的附加字段，按给定
// 顺序写在文件之前：有的接口要求说明「这是一次上传」的字段先出现。
func (c *Client) postFile(ctx context.Context, u, authorization string,
	headers map[string]string, form [][2]string, field, filename, contentType string, data []byte) ([]byte, int, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, kv := range form {
		if err := w.WriteField(kv[0], kv[1]); err != nil {
			return nil, 0, err
		}
	}
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

// testUploadPNG 是一张 1×1 的 PNG，只用于设置里的「测试这个图床」：上传一张
// 最小的真图，既能验证接口地址与字段名，又不会往图床上塞用户的照片。
func testUploadPNG() []byte {
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 0x2c, G: 0x6f, B: 0xff, A: 0xff})
	_ = png.Encode(&buf, img)
	return buf.Bytes()
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
			a.client.apiURL("/v1/upload/image"), auth, nil, nil,
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

// uploadToGeneric 是通用图床：请求头与附加表单字段由配置拼出，返回地址按
// 配置取——纯文本接口直接拿响应体，JSON 接口按路径取值，路径留空时自动找
// 响应里第一个 http(s) 图片地址。
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
		parseFormFields(h.Form, h.Token), h.Field, src.Name, src.Mime, data)
	if err != nil {
		return "", uploadNetError(err)
	}
	// 纯文本接口失败时响应体也只有一句话，JSON 接口的报错留给下面的分支去
	// 挑 success/message，这里只处理「不是 JSON 的失败」。
	if status >= 400 && !json.Valid(body) {
		if msg, ok := hostRejectionHint(status, body); ok {
			return "", errors.New(msg)
		}
	}
	if h.TextResponse {
		// Catbox / 0x0.st 这类接口成功时整个响应体就是一个地址。
		u := firstLine(string(body))
		if !isImageURL(u) {
			return "", hostBadBody(status, body)
		}
		return u, nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		if status >= 400 {
			return "", fmt.Errorf("图库返回 HTTP %d：%s", status, briefBody(body))
		}
		if looksLikeHTML(body) {
			return "", hostBadBody(status, body)
		}
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
		return "", fmt.Errorf("图库返回 HTTP %d：%s", status, briefBody(body))
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

// parseFormFields 把「一行一个 name=value」的附加字段转成 multipart 字段，
// 值里的 {{token}} 与请求头一样按 Token 替换。格式错的行走不到这里：
// validateImageHost 已经拦下并给出了提示。
func parseFormFields(form, token string) [][2]string {
	var out [][2]string
	for _, line := range strings.Split(strings.ReplaceAll(form, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out = append(out, [2]string{strings.TrimSpace(name), strings.ReplaceAll(value, "{{token}}", token)})
	}
	return out
}

// firstLine 取响应体的第一行并去掉空白：纯文本图床可能带上结尾换行。
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// hostRejectionHint 把几个固定报错翻成「照着改就能好」的提示。Catbox 系缺
// 字段时只回一句英文加 HTTP 412，原样抛给用户等于没说。
func hostRejectionHint(status int, body []byte) (string, bool) {
	s := strings.ToLower(string(body))
	switch {
	case strings.Contains(s, "no request type given"):
		return "图库要求先说明上传类型：在「附加表单字段」里加一行 reqtype=fileupload", true
	case strings.Contains(s, "no expire time specified"):
		return "图库要求指定保存时长：在「附加表单字段」里加一行 time=72h（也可填 1h / 12h / 24h / 72h）", true
	case status == 412:
		return "图库返回 HTTP 412：请求被拒绝，多半是缺了必填字段（检查「附加表单字段」），也可能是站点风控拦截：" + briefBody(body), true
	}
	return "", false
}

// uploadNetError 把网络层错误翻成一句用户能看懂的话：第三方图床多是境外
// 服务，被拦截时的原始错误（tls: server closed abruptly、EOF）对用户没有
// 意义，只会让人以为是应用的问题。
func uploadNetError(err error) error {
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(msg, "timeout"):
		return errors.New("上传超时：图床没有在限定时间内响应，换一个图床试试")
	case strings.Contains(msg, "tls:"), strings.Contains(msg, "eof"),
		strings.Contains(msg, "connection reset"), strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "no such host"):
		return errors.New("连不上图床：可能被网络拦截或服务已下线，换一个图床试试")
	}
	return err
}

// looksLikeHTML 判断响应体是不是一个网页。图床挂掉、被墙、或接口地址填成
// 了首页时都会回一页 HTML，这种失败要单独说清楚，不然用户只会看到一长串
// 标签，或者一句「不是 JSON」。
func looksLikeHTML(b []byte) bool {
	s := strings.TrimSpace(string(b))
	return strings.HasPrefix(s, "<")
}

// hostBadBody 是「图库没有给出图片地址」时的统一说明：网页多半是接口地址
// 不对或站点被拦截，其他内容就把响应体压成一句话带上。
func hostBadBody(status int, body []byte) error {
	if looksLikeHTML(body) {
		return fmt.Errorf("图库返回的是网页而不是图片地址（HTTP %d），接口地址可能填错了，或站点被网络拦截", status)
	}
	return fmt.Errorf("图库没有返回图片地址（HTTP %d）：%s", status, briefBody(body))
}

// briefBody 把响应体压成一行短说明放进错误信息里：图床出错时常回一整页
// HTML，原样抛给用户既看不懂也占满 toast。
func briefBody(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	r := []rune(s)
	if len(r) > 120 {
		s = string(r[:120]) + "…"
	}
	if s == "" {
		s = "空响应"
	}
	return s
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
