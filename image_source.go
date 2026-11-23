package main

import (
	"encoding/binary"
	"net/url"
	"path"
	"strings"
)

// 图片代理与格式适配。
//
// 2Libra 会把正文里的外部图片改写成 wsrv.nl 的图片代理地址，并且默认带上
// output=webp。浏览器都能解动画 WebP，桌面端却没有纯 Go 的动画 WebP 解码器：
// x/image/webp 遇到带 ANIM 的容器直接报 invalid format，于是 gif 在应用里
// 只能显示「图片加载失败」。
//
// 这里做两件事：
//
//   - 发请求前去掉图片代理的强制转码参数，让服务端按原格式返回（gif 仍是 gif），
//     预览里的逐帧播放器才拿得到完整动画。缓存键仍是界面里的原始地址。
//   - 真正的动画 WebP 退回第一帧显示（见 webpFirstFrame），至少不报错、不空白。

// imageMaxBytes 是单张图片允许的原始字节上限。动图（gif / webp）通常比静态
// 图大得多，4 MB 会把一段稍长的 gif 截断，解码自然失败。
const imageMaxBytes = 16 << 20

// imageProxyHost 报告 host 是不是已知的图片代理。只对这些域名做格式改写与
// 目标地址识别，别的站点一律按普通链接处理。
func imageProxyHost(host string) bool {
	switch strings.TrimPrefix(strings.ToLower(host), "www.") {
	case "wsrv.nl", "images.weserv.nl":
		return true
	}
	return false
}

func imageProxyURL(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https") &&
		imageProxyHost(u.Hostname())
}

// imageRequestURL 返回真正要请求的地址：图片代理上的强制输出格式去掉，其余
// 参数原样保留。别的地址、没有 output 的地址原样返回。
func imageRequestURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !imageProxyURL(u) {
		return raw
	}
	q := u.Query()
	changed := false
	// output 是新版的名字，we 是旧版 weserv 的格式参数；两个都清掉。
	for _, key := range []string{"output", "we"} {
		if q.Has(key) {
			q.Del(key)
			changed = true
		}
	}
	if !changed {
		return raw
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// imageProxyTarget 从图片代理地址里取它真正指向的图片地址：先看 url 参数，
// 再看 default 回退地址。目标本身也可能被套了一层（ddg 的 ?u=），继续往里
// 解到扩展名能认成图片为止。取不到返回空串。
func imageProxyTarget(u *url.URL) string {
	if !imageProxyURL(u) {
		return ""
	}
	q := u.Query()
	for _, key := range []string{"url", "default"} {
		raw := strings.TrimSpace(q.Get(key))
		if raw == "" {
			continue
		}
		if inner, err := url.Parse(raw); err == nil {
			if target := strings.TrimSpace(inner.Query().Get("u")); target != "" {
				raw = target
			}
		}
		if inner, err := url.Parse(raw); err == nil {
			if imageExts[strings.ToLower(path.Ext(inner.Path))] {
				return raw
			}
		}
	}
	return ""
}

// riffChunk 是 WebP（RIFF 容器）里的一个块：id、负载与含头尾对齐的原始字节。
type riffChunk struct {
	id      string
	payload []byte
	raw     []byte
}

// riffChunks 按 RIFF 规则拆块：每块 4 字节 id + 4 字节小端长度 + 负载，
// 负载为奇数长度时补一个填充字节。越界就停，损坏的输入不会读出界。
func riffChunks(b []byte) []riffChunk {
	var out []riffChunk
	for i := 0; i+8 <= len(b); {
		id := string(b[i : i+4])
		n := int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		if n < 0 || i+8+n > len(b) {
			break
		}
		end := i + 8 + n
		if end%2 == 1 && end < len(b) {
			end++
		}
		out = append(out, riffChunk{id: id, payload: b[i+8 : i+8+n], raw: b[i:end]})
		i = end
	}
	return out
}

// webpFirstFrame 从一个动画 WebP 里取出第一帧，重新封成一张静态 WebP：
// x/image/webp 不认 ANIM 容器，但解静态的 VP8 / VP8L 没问题。帧带独立
// ALPH 块时补一个只声明 alpha 的 VP8X 头。取不到就返回 nil，调用方按普通
// 图片失败处理。
func webpFirstFrame(data []byte) []byte {
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil
	}
	var (
		alpha []byte
		frame []byte
		w, h  int
	)
	for _, c := range riffChunks(data[12:]) {
		if c.id != "ANMF" || len(c.payload) < 16 {
			continue
		}
		// 帧头：x(3) y(3) 宽-1(3) 高-1(3) 时长(3) 标志(1)，随后是帧内块。
		w = int(le24(c.payload[6:9])) + 1
		h = int(le24(c.payload[9:12])) + 1
		for _, sub := range riffChunks(c.payload[16:]) {
			switch sub.id {
			case "ALPH":
				alpha = sub.raw
			case "VP8 ", "VP8L":
				frame = sub.raw
			}
			if frame != nil {
				break
			}
		}
		break
	}
	if frame == nil || w <= 0 || h <= 0 {
		return nil
	}
	body := make([]byte, 0, len(frame)+len(alpha)+18)
	if alpha != nil {
		vp8x := make([]byte, 18)
		copy(vp8x, "VP8X")
		binary.LittleEndian.PutUint32(vp8x[4:], 10)
		vp8x[8] = 0x10 // alpha
		putLE24(vp8x[12:], w-1)
		putLE24(vp8x[15:], h-1)
		body = append(body, vp8x...)
		body = append(body, alpha...)
	}
	body = append(body, frame...)
	out := make([]byte, 12+len(body))
	copy(out, "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(4+len(body)))
	copy(out[8:], "WEBP")
	copy(out[12:], body)
	return out
}

func le24(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 }

func putLE24(b []byte, v int) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
}
