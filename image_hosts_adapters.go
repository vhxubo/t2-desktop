package main

import (
	"context"
	"crypto/md5"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	randv2 "math/rand/v2"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 内置图床协议（Adapter）。
//
// 下面这些接口没法用「一次 multipart + 从 JSON 里取地址」描述：MJJ.Today 与
// ImgBB 要先从上传页里扒一个 auth_token，Skyimg 要先取 CSRF 令牌并带上两个
// 固定的 x-sync-token，Tikolu 只回图片 ID，StarDots 要按 Key/Secret 现签一个
// MD5 签名，Imgur 与 111666.best 则在多个公开凭据之间轮换。它们全部来自
// 「通用图片上传助手」脚本（utags/userscripts）里实现过的图床。
//
// 配置里的 Adapter 指到这里的某一家；接口地址仍然可以在设置里覆盖（留空用
// 内置地址），测试也因此能把请求指到 httptest 上。

const (
	adapterImgur      = "imgur"
	adapterTikolu     = "tikolu"
	adapterMJJ        = "mjj"
	adapterImgBB      = "imgbb"
	adapterSkyimg     = "skyimg"
	adapterSkyimgWebp = "skyimg_webp"
	adapter111666     = "111666_best"
	adapterStarDots   = "stardots"
)

const (
	imgurUploadURL      = "https://api.imgur.com/3/upload"
	tikoluUploadURL     = "https://tikolu.net/i/"
	mjjUploadURL        = "https://mjj.today/json"
	imgbbUploadURL      = "https://imgbb.com/json"
	skyimgUploadURL     = "https://skyimg.net/upload"
	best111666UploadURL = "https://i.111666.best/image"
	starDotsUploadURL   = "https://api.stardots.io/openapi/file/upload"
)

// imgurPublicClientIDs 是脚本里内置的一组公共 Client-ID，只用来让「装好就能
// 传」成立；它们属于别人的应用，随时可能被 Imgur 停用，所以填了自己的
// Client-ID 时只用用户那一份。
var imgurPublicClientIDs = []string{
	"3107b9ef8b316f3",
	"442b04f26eefc8a",
	"59cfebe717c09e4",
	"60605aad4a62882",
	"6c65ab1d3f5452a",
	"83e123737849aa9",
	"9311f6be1c10160",
	"c4a4a563f698595",
	"81be04b9e4a08ce",
}

// best111666PublicTokens 同上：111666.best 的公共令牌，失败时逐个轮换。
var best111666PublicTokens = []string{
	"6Fqz4pDz949bhzMOvUj2Ytgiy17ARsWz",
	"FcyNm0KvmHx73qOcwbm0uZ89rXOQFuIT",
	"yHF9Br2kXZqEC0sQR2hOSKlGv0A6hyMU",
	"B56UgFSDhGeXpK1WSNBd6NakwuWHEmGP",
	"qFxuIgXxCTOY0cj5VDiZPZW7uwPVbT7L",
}

// skyimgSyncToken 是 Skyimg 前端写死的两个常量，接口拿它区分「原图」与
// 「转 WebP」两条上传路径，不带就回 403。
const (
	skyimgSyncTokenRaw  = "f68b6cc9282eac325398df4bb608ee14e28219533b350ba3c911abdd3742681a"
	skyimgSyncTokenWebp = "47e9f97c4c3ea304ef8ff4f232e27c7095d4c1cd7f6930860b083affd03ac831"
)

// adapterEndpoint 返回某套内置协议的默认接口地址；留空表示不是内置协议。
func adapterEndpoint(adapter string) string {
	switch adapter {
	case adapterImgur:
		return imgurUploadURL
	case adapterTikolu:
		return tikoluUploadURL
	case adapterMJJ:
		return mjjUploadURL
	case adapterImgBB:
		return imgbbUploadURL
	case adapterSkyimg:
		return skyimgUploadURL
	case adapterSkyimgWebp:
		return skyimgUploadURL + "?webp=true"
	case adapter111666:
		return best111666UploadURL
	case adapterStarDots:
		return starDotsUploadURL
	}
	return ""
}

// adapterMaxBytes 是各家接口自己的单张上限。只有比应用上限（20 MB）更严的
// 才需要返回，免得用户看到「配置上没超限、上传却被拒」。
func adapterMaxBytes(adapter string) int64 {
	switch adapter {
	case adapterTikolu:
		return 8 << 20
	case adapterMJJ:
		return 10 << 20
	case adapterImgur:
		return 20 << 20
	}
	return 0
}

// adapterReady 判断一条内置协议图床是否具备上传条件：只有 StarDots 要凭据，
// 其余几家靠页面令牌或内置公共令牌，填好地址就能传。
func adapterReady(h *imageHost) bool {
	if h.Adapter == adapterStarDots {
		return strings.TrimSpace(h.Token) != "" && strings.TrimSpace(h.Secret) != ""
	}
	return true
}

// uploadEndpoint 是「用户覆盖的地址优先，否则用内置地址」。
func (h imageHost) uploadEndpoint() string {
	if u := strings.TrimSpace(h.Endpoint); u != "" {
		return u
	}
	return adapterEndpoint(h.Adapter)
}

// uploadToAdapter 按 Adapter 分发到各家实现。
func (a *app) uploadToAdapter(ctx context.Context, h imageHost, src uploadSource, data []byte) (string, error) {
	switch h.Adapter {
	case adapterImgur:
		return a.uploadToImgur(ctx, h, src, data)
	case adapterTikolu:
		return a.uploadToTikolu(ctx, h, src, data)
	case adapterMJJ:
		u, err := a.uploadToChevereto(ctx, h.uploadEndpoint(), src, data)
		if err != nil {
			return "", err
		}
		// MJJ.Today 回的 i.mji.rip 在国内解析不到，同站的 i.mij.rip 可以。
		return strings.Replace(u, "i.mji.rip", "i.mij.rip", 1), nil
	case adapterImgBB:
		return a.uploadToChevereto(ctx, h.uploadEndpoint(), src, data)
	case adapterSkyimg:
		return a.uploadToSkyimg(ctx, h, src, data, false)
	case adapterSkyimgWebp:
		return a.uploadToSkyimg(ctx, h, src, data, true)
	case adapter111666:
		return a.uploadTo111666(ctx, h, src, data)
	case adapterStarDots:
		return a.uploadToStarDots(ctx, h, src, data)
	}
	return "", fmt.Errorf("图库「%s」用了未知的内置上传协议 %q，请恢复默认后重配", h.Name, h.Adapter)
}

// ------------------------------------------------------------------
// Chevereto：MJJ.Today / ImgBB
// ------------------------------------------------------------------

// cheveretoTokenPattern 是 Chevereto 上传页里内联的那行令牌。MJJ.Today 与
// ImgBB 都跑在 Chevereto 上，上传接口必须带这个页面里现成的 auth_token，
// 否则只回一句「非法请求」。
var cheveretoTokenPattern = regexp.MustCompile(`PF\.obj\.config\.auth_token\s*=\s*["']([A-Za-z0-9]+)["']`)

// uploadToChevereto 走「取上传页令牌 → 提交 multipart」这两步。响应里
// status_code 为 200 且 image.url 非空才算成功。
func (a *app) uploadToChevereto(ctx context.Context, endpoint string, src uploadSource, data []byte) (string, error) {
	origin := endpointOrigin(endpoint)
	if origin == "" {
		return "", errors.New("图库接口地址要写成 https://… 这样的完整地址")
	}
	page, status, err := a.client.getText(ctx, origin+"/upload", nil)
	if err != nil {
		return "", uploadNetError(err)
	}
	if status >= 400 {
		return "", fmt.Errorf("取上传页失败（HTTP %d），站点可能被网络拦截", status)
	}
	m := cheveretoTokenPattern.FindSubmatch(page)
	if len(m) < 2 {
		return "", errors.New("上传页里找不到 auth_token：站点可能改版，或被网络拦截了")
	}
	form := [][2]string{
		{"type", "file"},
		{"action", "upload"},
		{"timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10)},
		{"auth_token", string(m[1])},
		{"expiration", ""},
		{"nsfw", "0"},
	}
	body, status, err := a.client.sendMultipart(ctx, http.MethodPost, endpoint, "", nil, form,
		"source", src.Name, src.Mime, data)
	if err != nil {
		return "", uploadNetError(err)
	}
	var resp struct {
		StatusCode int `json:"status_code"`
		Image      struct {
			URL string `json:"url"`
		} `json:"image"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", hostBadBody(status, body)
	}
	u := absoluteImageURL(resp.Image.URL, endpoint)
	if resp.StatusCode != http.StatusOK || !isImageURL(u) {
		// Chevereto 拒绝时会把原因放在 error.message 里，比整段 JSON 好读。
		if msg := strings.TrimSpace(resp.Error.Message); msg != "" {
			return "", errors.New("图库拒绝上传：" + msg)
		}
		return "", hostBadBody(status, body)
	}
	return u, nil
}

// ------------------------------------------------------------------
// Skyimg
// ------------------------------------------------------------------

// uploadToSkyimg 先取 CSRF 令牌，再带着 origin 与两个 x-sync-token 之一上传。
// webp 为 true 时走 ?webp=true，站点会把图转成 WebP 再存。
func (a *app) uploadToSkyimg(ctx context.Context, h imageHost, src uploadSource, data []byte, webp bool) (string, error) {
	endpoint := h.uploadEndpoint()
	if webp && !strings.Contains(endpoint, "webp=") {
		if strings.Contains(endpoint, "?") {
			endpoint += "&webp=true"
		} else {
			endpoint += "?webp=true"
		}
	}
	origin := endpointOrigin(endpoint)
	if origin == "" {
		return "", errors.New("图库接口地址要写成 https://… 这样的完整地址")
	}
	// /csrf-token 会同时下发一个 _csrf Cookie，令牌与 Cookie 必须成对出现：
	// 少了 Cookie，上传接口只回 403「请求无效或已过期」。两次请求共用一个 jar。
	jar, err := cookiejar.New(nil)
	if err != nil {
		return "", err
	}
	hc := a.client.clientWithJar(jar)
	raw, status, err := a.client.getTextWith(ctx, hc, origin+"/csrf-token", nil)
	if err != nil {
		return "", uploadNetError(err)
	}
	var tok struct {
		CsrfToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil || strings.TrimSpace(tok.CsrfToken) == "" {
		return "", fmt.Errorf("取 CSRF 令牌失败（HTTP %d）：站点可能改版，或被网络拦截了", status)
	}
	sync := skyimgSyncTokenRaw
	if webp {
		sync = skyimgSyncTokenWebp
	}
	headers := map[string]string{
		"Origin":       origin,
		"X-Csrf-Token": tok.CsrfToken,
		"X-Sync-Token": sync,
	}
	body, status, err := a.client.sendMultipartWith(ctx, hc, http.MethodPost, endpoint, "", headers, nil,
		"file", src.Name, src.Mime, data)
	if err != nil {
		return "", uploadNetError(err)
	}
	// 地址偶尔被反引号包住（前端用模板字符串拼过），去掉再判。
	u := strings.ReplaceAll(findImageURLAny(body), "`", "")
	u = absoluteImageURL(u, origin)
	if !isImageURL(u) {
		return "", hostBadBody(status, body)
	}
	return u, nil
}

// ------------------------------------------------------------------
// Imgur / 111666.best：多凭据轮换
// ------------------------------------------------------------------

// uploadToImgur 用 Client-ID 匿名上传。没填自己的 Client-ID 时轮换内置的
// 公共 ID —— 一个被限流就换下一个，全都失败才报错。
func (a *app) uploadToImgur(ctx context.Context, h imageHost, src uploadSource, data []byte) (string, error) {
	var lastErr error
	for _, id := range rotateIDs(h.Token, imgurPublicClientIDs) {
		body, status, err := a.client.sendMultipart(ctx, http.MethodPost, h.uploadEndpoint(),
			"Client-ID "+id, nil, nil, "image", src.Name, src.Mime, data)
		if err != nil {
			return "", uploadNetError(err)
		}
		var resp struct {
			Success bool `json:"success"`
			Data    struct {
				Link string `json:"link"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &resp); err == nil && resp.Success && isImageURL(resp.Data.Link) {
			return resp.Data.Link, nil
		}
		lastErr = hostStatusError("Imgur", status, body)
	}
	return "", lastErr
}

// uploadTo111666 与 Imgur 一样轮换令牌，区别是令牌放在 auth-token 请求头里，
// 文件字段叫 payload，响应里 src 可能是站内路径。
func (a *app) uploadTo111666(ctx context.Context, h imageHost, src uploadSource, data []byte) (string, error) {
	endpoint := h.uploadEndpoint()
	var lastErr error
	for _, token := range rotateIDs(h.Token, best111666PublicTokens) {
		body, status, err := a.client.sendMultipart(ctx, http.MethodPost, endpoint, "",
			map[string]string{"auth-token": token}, nil, "payload", src.Name, src.Mime, data)
		if err != nil {
			return "", uploadNetError(err)
		}
		var resp struct {
			OK  bool   `json:"ok"`
			Src string `json:"src"`
		}
		if err := json.Unmarshal(body, &resp); err == nil && resp.OK {
			if u := absoluteImageURL(resp.Src, endpoint); isImageURL(u) {
				return u, nil
			}
		}
		lastErr = hostStatusError("111666.best", status, body)
	}
	return "", lastErr
}

// rotateIDs 把用户填的那一个（如果填了）或内置的一组打乱后返回：这类接口的
// 公共凭据随时可能被限流，随机起点能让连续上传分散到不同凭据上。
func rotateIDs(token string, builtin []string) []string {
	if token = strings.TrimSpace(token); token != "" {
		return []string{token}
	}
	out := append([]string(nil), builtin...)
	randv2.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// ------------------------------------------------------------------
// Tikolu
// ------------------------------------------------------------------

// uploadToTikolu 的接口只回图片 ID，地址要自己拼成 <接口>/<ID>。
func (a *app) uploadToTikolu(ctx context.Context, h imageHost, src uploadSource, data []byte) (string, error) {
	endpoint := h.uploadEndpoint()
	body, status, err := a.client.sendMultipart(ctx, http.MethodPost, endpoint, "", nil,
		[][2]string{{"upload", "true"}}, "file", src.Name, src.Mime, data)
	if err != nil {
		return "", uploadNetError(err)
	}
	var resp struct {
		Status string `json:"status"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(body, &resp); err == nil &&
		resp.Status == "uploaded" && strings.TrimSpace(resp.ID) != "" {
		return strings.TrimSuffix(endpoint, "/") + "/" + resp.ID, nil
	}
	return "", hostBadBody(status, body)
}

// ------------------------------------------------------------------
// StarDots
// ------------------------------------------------------------------

// uploadToStarDots 按官方规则现签请求：sign = upper(md5(timestamp|secret|nonce))，
// 连 Key / Nonce / Timestamp 一起放进 x-stardots-* 请求头，文件用 PUT 提交。
func (a *app) uploadToStarDots(ctx context.Context, h imageHost, src uploadSource, data []byte) (string, error) {
	key, secret := strings.TrimSpace(h.Token), strings.TrimSpace(h.Secret)
	if key == "" || secret == "" {
		return "", errors.New("StarDots 需要 API Key 与 API Secret，请到「设置 → 图库」里填写")
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := starDotsNonce()
	sum := md5.Sum([]byte(timestamp + "|" + secret + "|" + nonce))
	headers := map[string]string{
		"X-Stardots-Key":               key,
		"X-Stardots-Nonce":             nonce,
		"X-Stardots-Timestamp":         timestamp,
		"X-Stardots-Sign":              strings.ToUpper(hex.EncodeToString(sum[:])),
		"X-Stardots-Assistant-Version": "t2-desktop",
	}
	form := [][2]string{{"filename", src.Name}, {"space", strings.TrimSpace(h.Bucket)}}
	body, status, err := a.client.sendMultipart(ctx, http.MethodPut, h.uploadEndpoint(), "", headers, form,
		"file", src.Name, src.Mime, data)
	if err != nil {
		return "", uploadNetError(err)
	}
	var resp struct {
		Success   bool   `json:"success"`
		Message   string `json:"message"`
		RequestID string `json:"requestId"`
		Data      struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", hostBadBody(status, body)
	}
	if resp.Success && isImageURL(resp.Data.URL) {
		return resp.Data.URL, nil
	}
	if msg := strings.TrimSpace(resp.Message); msg != "" {
		if resp.RequestID != "" {
			return "", fmt.Errorf("StarDots：%s（%s）", msg, resp.RequestID)
		}
		return "", errors.New("StarDots：" + msg)
	}
	return "", hostBadBody(status, body)
}

// starDotsNonce 是 20 位以内的随机串：官方要求每次请求都不同，重复会被判
// 重放。
func starDotsNonce() string {
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return strconv.FormatInt(time.Now().Unix(), 10) + hex.EncodeToString(b[:])
}

// ------------------------------------------------------------------
// 公共小工具
// ------------------------------------------------------------------

// findImageURLAny 和 findImageURL 一样在 JSON 里找地址，但接受站内路径
// （/i/x.png）：内置协议拿到相对地址后自己补全，不能在这里就被 isImageURL
// 判死。
func findImageURLAny(body []byte) string {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return ""
	}
	if s, ok := v.(string); ok && urlish(s) {
		return s
	}
	preferred := []string{"url", "image_url", "imageUrl", "link", "src", "thumbnail", "original", "path"}
	var found string
	var walk func(any, int)
	walk = func(x any, depth int) {
		if found != "" || depth > 8 {
			return
		}
		switch t := x.(type) {
		case map[string]any:
			for _, k := range preferred {
				if s, ok := t[k].(string); ok && urlish(s) {
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
			if urlish(t) {
				found = t
			}
		}
	}
	walk(v, 0)
	return found
}

func urlish(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") ||
		strings.HasPrefix(s, "/")
}

// hostStatusError 把一次失败的响应压成一句能看懂的话：回网页多半是被拦截，
// 回 JSON 就带上图床自己给的原因。
func hostStatusError(name string, status int, body []byte) error {
	if looksLikeHTML(body) {
		return fmt.Errorf("%s 返回的是网页而不是接口响应（HTTP %d），站点可能被网络拦截", name, status)
	}
	return fmt.Errorf("%s 返回 HTTP %d：%s", name, status, briefBody(body))
}
