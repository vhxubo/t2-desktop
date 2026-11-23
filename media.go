package main

import (
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui/markdown/parser"
)

// 媒体识别：把正文里的一条链接认成「图片」或「视频」，供正文嵌入与预览
// 窗口共用。
//
// 2Libra 的正文只给 Markdown 与一条条链接，站外视频（YouTube、哔哩哔哩）
// 常常是一整段只有一条链接，图片也常被贴成裸地址。这里统一认出它们：
//
//   - 图片：按扩展名认（png / jpg / gif / webp / avif / bmp / svg），正文里
//     独占一段的裸地址按图片嵌入，与 ![]() 写法同等对待。
//   - 站外视频：YouTube（watch / shorts / youtu.be / embed）与哔哩哔哩
//     （/video/BV…、/video/av…、b23.tv 短链）认成视频条目，正文里画成
//     一张封面卡片，点开预览窗口后在浏览器里播放。
//   - 直链视频：mp4 / webm / mov / m4v / ogv / mkv 同样算视频。
//
// 认不出来的一律返回 false，调用方按普通链接处理：不要凭猜测把别人的页面
// 变成嵌入内容。

// mediaKind 区分预览组里的一项是什么。
type mediaKind int

const (
	mediaImage mediaKind = iota // 图片（含 gif / webp 等动图）
	mediaVideo                  // 站外视频
)

// videoProvider 是视频的来源，决定卡片上的名字与封面地址。
type videoProvider int

const (
	videoNone videoProvider = iota
	videoYouTube
	videoBilibili
	videoDirect
)

// mediaRef 是从链接里认出来的一项媒体。URL 是源文里那一条链接本身，其余
// 字段按来源填：图片只有 URL；视频另外带上在浏览器里打开的页面、站外播放器
// 的嵌入地址与封面（B 站封面要另调接口，留空）。
type mediaRef struct {
	URL      string
	Kind     mediaKind
	Provider videoProvider
	// ID 是视频编号（YouTube 的 11 位 id、B 站的 BV 号或 av 号）；短链认不出
	// 编号时为空。
	ID       string
	WatchURL string
	EmbedURL string
	ThumbURL string
}

var (
	youtubeIDRe  = regexp.MustCompile(`^[A-Za-z0-9_-]{6,20}$`)
	bilibiliBVRe = regexp.MustCompile(`^BV[0-9A-Za-z]{10}$`)
	bilibiliAvRe = regexp.MustCompile(`^av[0-9]+$`)
)

// imageExts 是能当图片预览的扩展名（小写，带点）。
var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".avif": true, ".bmp": true, ".svg": true,
}

// videoExts 是能当视频认的直链扩展名（小写，带点）。
var videoExts = map[string]bool{
	".mp4": true, ".webm": true, ".mov": true, ".m4v": true,
	".ogv": true, ".ogg": true, ".mkv": true,
}

// parseMediaRef 认一条链接：先按视频认，再按图片认。两者都不是时 ok 为 false。
func parseMediaRef(raw string) (mediaRef, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return mediaRef{}, false
	}
	if ref, ok := parseVideoLink(raw); ok {
		return ref, true
	}
	if isImageLink(raw) {
		return mediaRef{URL: raw, Kind: mediaImage}, true
	}
	return mediaRef{}, false
}

// isImageLink 报告一条链接是不是图片：只看路径的扩展名，查询串与锚点不参与。
func isImageLink(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return imageExts[strings.ToLower(path.Ext(u.Path))]
}

// parseVideoLink 认出 YouTube / 哔哩哔哩 / 直链视频。认不出来返回 false。
func parseVideoLink(raw string) (mediaRef, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return mediaRef{}, false
	}
	host := strings.ToLower(strings.TrimPrefix(u.Hostname(), "www."))
	switch host {
	case "youtu.be":
		id := strings.Trim(u.Path, "/")
		if youtubeIDRe.MatchString(id) {
			return youtubeRef(id), true
		}
	case "youtube.com", "m.youtube.com", "music.youtube.com", "youtube-nocookie.com":
		if id := youtubeIDFromPath(u); id != "" {
			return youtubeRef(id), true
		}
		if id := strings.TrimSpace(u.Query().Get("v")); youtubeIDRe.MatchString(id) {
			return youtubeRef(id), true
		}
	case "bilibili.com", "m.bilibili.com":
		if id := bilibiliIDFromPath(u.Path); id != "" {
			return bilibiliRef(id, raw), true
		}
	case "b23.tv":
		// 短链要在跳转之后才知道是哪个视频：认成视频，但编号与封面留空，
		// 播放时直接打开这条短链，由浏览器自己跳转。
		if strings.Trim(u.Path, "/") != "" {
			return mediaRef{
				URL: raw, Kind: mediaVideo, Provider: videoBilibili, WatchURL: raw,
			}, true
		}
	}
	if videoExts[strings.ToLower(path.Ext(u.Path))] {
		return mediaRef{
			URL: raw, Kind: mediaVideo, Provider: videoDirect,
			WatchURL: raw, EmbedURL: raw,
		}, true
	}
	return mediaRef{}, false
}

// youtubeRef 按视频 id 组出一条 YouTube 条目：播放页、嵌入页与缩略图都由
// id 直接拼出来，不用再请求接口。
func youtubeRef(id string) mediaRef {
	return mediaRef{
		URL:      "https://www.youtube.com/watch?v=" + id,
		Kind:     mediaVideo,
		Provider: videoYouTube,
		ID:       id,
		WatchURL: "https://www.youtube.com/watch?v=" + id,
		EmbedURL: "https://www.youtube.com/embed/" + id,
		ThumbURL: "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg",
	}
}

// youtubeIDFromPath 从 /shorts/<id>、/embed/<id>、/live/<id>、/v/<id> 里取 id。
func youtubeIDFromPath(u *url.URL) string {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	switch parts[0] {
	case "shorts", "embed", "live", "v", "watch":
		if youtubeIDRe.MatchString(parts[1]) {
			return parts[1]
		}
	}
	return ""
}

// bilibiliIDFromPath 从 /video/BV… 或 /video/av… 里取编号，原样保留大小写。
func bilibiliIDFromPath(p string) string {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) < 2 || parts[0] != "video" {
		return ""
	}
	id := parts[1]
	if bilibiliBVRe.MatchString(id) || bilibiliAvRe.MatchString(id) {
		return id
	}
	return ""
}

// bilibiliRef 按编号组出一条哔哩哔哩条目。封面要调 B 站接口才知道，这里不
// 额外发请求：卡片用来源底色加编号，播放时打开原页面。
func bilibiliRef(id, raw string) mediaRef {
	watch := "https://www.bilibili.com/video/" + id
	embed := "https://player.bilibili.com/player.html?autoplay=1&high_quality=1"
	if bilibiliBVRe.MatchString(id) {
		embed += "&bvid=" + id
	} else {
		embed += "&aid=" + strings.TrimPrefix(id, "av")
	}
	return mediaRef{
		URL:      raw,
		Kind:     mediaVideo,
		Provider: videoBilibili,
		ID:       id,
		WatchURL: watch,
		EmbedURL: embed,
	}
}

// providerLabel 是卡片上写的来源名。
func (r mediaRef) providerLabel() string {
	switch r.Provider {
	case videoYouTube:
		return "YouTube"
	case videoBilibili:
		return "哔哩哔哩"
	case videoDirect:
		return "视频"
	}
	return "视频"
}

// title 是这条媒体的说明文字：视频给出「来源 · 编号」，图片取地址最后一段，
// 取不到就叫「图片」。它同时是占位与读屏用的名字。
func (r mediaRef) title() string {
	if r.Kind == mediaVideo {
		if r.ID != "" {
			return r.providerLabel() + " · " + r.ID
		}
		return r.providerLabel() + "视频"
	}
	return imageNameFromURL(r.URL)
}

// imageNameFromURL 从图片地址里取一个好认的名字，取不到就叫「图片」。
func imageNameFromURL(raw string) string {
	if u, err := url.Parse(strings.TrimSpace(raw)); err == nil {
		if name := strings.TrimSpace(path.Base(u.Path)); name != "" && name != "." && name != "/" {
			return name
		}
	}
	return "图片"
}

// standaloneMediaLink 认出一段「整段只有一条链接或一条裸地址」的段落，并返回
// 它指向的媒体。Markdown 图片语法（![]()）走解析树的 Image 节点，不在这里。
//
// 只认顶层内联、且段里没有别的文字：正文中间夹着的链接仍然是普通链接，
// 不能因为指向视频就把整段变成卡片。强调 / 行内代码 / 图片一律不认，交给
// 原来的渲染路径。
func standaloneMediaLink(in []parser.Inline) (mediaRef, bool) {
	var text strings.Builder
	dest := ""
	links := 0
	for k := 0; k < len(in); k = in[k].Next {
		n := &in[k]
		switch n.Kind {
		case parser.Text, parser.Mention:
			text.WriteString(n.Text)
		case parser.SoftBreak, parser.LineBreak:
			text.WriteString("\n")
		case parser.Link:
			links++
			dest = strings.TrimSpace(n.Dest)
			// 链接文字里还有强调或图片时不是「一条链接独占一段」。
			for j := k + 1; j < n.Next; j++ {
				if inner := &in[j]; inner.Kind != parser.Text && inner.Kind != parser.Mention {
					return mediaRef{}, false
				}
			}
		default:
			return mediaRef{}, false
		}
	}
	body := strings.TrimSpace(text.String())
	switch {
	case links == 1 && (body == "" || body == dest):
		return parseMediaRef(dest)
	case links == 0 && body != "" && !strings.ContainsAny(body, " \n\t"):
		return parseMediaRef(body)
	}
	return mediaRef{}, false
}
