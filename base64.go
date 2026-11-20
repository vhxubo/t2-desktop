package main

import (
	"encoding/base64"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/display"
)

// 编辑器里的「Base64 编码」与评论正文里的解码块共用的约定。
//
// 编码后插入正文的是**裸 Base64 串**，没有额外标记：用户复制出去就是一段
// 标准 Base64，别处也能直接解码。评论渲染时靠正则把正文里的 Base64 串认出来，
// 切成「普通 Markdown」与「编码片段」：普通段照旧走 markdown.View，片段画成
// 独立组件——默认是编码串加一个悬浮才出现的「原文」按钮；点开后换成解码块，
// 里面是「复制」与「恢复」。展开状态按「评论 id + 片段序号」记在 detailState
// 里，重绘不会自己合上。
//
// 裸串没有标记，正则必须保守一点才不会误伤正文：只认足够长、两侧不是字母
// 数字、且解出来是合法 UTF-8 可打印文本的一串。普通英文单词、哈希、URL 片段
// 都过不了这几关（见 splitBase64Segments）。

// base64DialogWidth 是编码 Modal 的宽度：里面是一段输入与一行编码预览，
// 520 够宽又不会在窄窗口里占满。
const base64DialogWidth float32 = 520

// base64ActionSlot 是编码块右侧给「原文」按钮留的固定宽度。按钮只在悬浮
// 时出现，但位置一直占着，出现时不会把编码串挤得换行。
const base64ActionSlot float32 = 44

// base64MonoFont 让编码串按等宽显示，+ / = 与字母不会挤在一起。
const base64MonoFont = "monospace"

// base64TextRE 先粗筛出候选串，真正是不是 Base64 由 splitBase64Segments 判定。
// 最短 8 个字符对应 5 字节原文，中文「你好」的编码正好 8 位；再短就会把
// work / test 这类普通单词也卷进来。Go 的正则没有前后向断言，词边界在那里
// 用字符判断补上。
var base64TextRE = regexp.MustCompile(`[A-Za-z0-9+/]{8,}={0,2}`)

// isBase64Char 报告一个字节能不能出现在 Base64 串里；用来检查候选串两侧，
// 避免把长单词或 URL 路径中的一小段当成编码。
func isBase64Char(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' ||
		b >= '0' && b <= '9' || b == '+' || b == '/' || b == '='
}

// encodeBase64Text 把一段原文编成可直接插进正文的裸 Base64 串。
func encodeBase64Text(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

// decodeBase64Token 解出一个裸 Base64 串。除了标准带填充的写法，也接受用户
// 自己粘进来的无填充串（补上 = 再解）。解不开、解出空内容或不是可打印 UTF-8
// 的一律拒绝，调用方把这些串当普通正文显示。
func decodeBase64Token(token string) (string, bool) {
	if len(token) < 4 {
		return "", false
	}
	padded := token
	if n := len(padded) % 4; n != 0 {
		padded += strings.Repeat("=", 4-n)
	}
	b, err := base64.StdEncoding.DecodeString(padded)
	if err != nil || len(b) == 0 || !utf8.Valid(b) {
		return "", false
	}
	for _, r := range string(b) {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	return string(b), true
}

// base64Segment 是正文按 Base64 串切开后的一段。token 非空表示这段是编码
// 片段，否则 text 是交给 Markdown 的普通正文。
type base64Segment struct {
	text  string
	token string
}

// splitBase64Segments 按裸 Base64 串把正文切开。正则只负责找候选，候选还要
// 过「两侧不是 Base64 字符 + 解得出可打印 UTF-8」两关才算数，所以正文里恰好
// 出现的一串英文、链接里的路径、十六进制串都不会被误判成编码。
func splitBase64Segments(src string) []base64Segment {
	matches := base64TextRE.FindAllStringIndex(src, -1)
	if len(matches) == 0 {
		return []base64Segment{{text: src}}
	}
	out := make([]base64Segment, 0, len(matches)*2+1)
	last := 0
	for _, m := range matches {
		start, end := m[0], m[1]
		if start > 0 && isBase64Char(src[start-1]) {
			continue
		}
		if end < len(src) && isBase64Char(src[end]) {
			continue
		}
		token := src[start:end]
		if _, ok := decodeBase64Token(token); !ok {
			continue
		}
		if start > last {
			out = append(out, base64Segment{text: src[last:start]})
		}
		out = append(out, base64Segment{token: token})
		last = end
	}
	if last < len(src) {
		out = append(out, base64Segment{text: src[last:]})
	}
	if len(out) == 0 {
		return []base64Segment{{text: src}}
	}
	return out
}

// base64RevealKey 是一条评论里某个编码片段的展开状态键。同一条评论里出现两次
// 相同编码也要各自记住展开状态，所以带上片段序号。
func base64RevealKey(commentID string, index int, token string) string {
	return commentID + "|" + strconv.Itoa(index) + "|" + token
}

// base64Revealed 读一个片段现在是不是解码态，没开时默认显示编码串。
func (d *detailState) base64Revealed(key string) bool {
	return d != nil && d.base64Open != nil && d.base64Open[key]
}

// base64SetRevealed 切换一个片段的展开状态。detailState 里没有这张表时顺手建
// 一张：它只在 UI 线程的构建帧里读写，不落盘也不进后台。
func (d *detailState) base64SetRevealed(key string, on bool) {
	if d == nil {
		return
	}
	if d.base64Open == nil {
		d.base64Open = make(map[string]bool)
	}
	if on {
		d.base64Open[key] = true
	} else {
		delete(d.base64Open, key)
	}
}

// base64CommentBlock 画评论正文里的一个 Base64 片段。默认态是编码串加一个悬浮
// 才出现的「原文」按钮；点开后换成解码块，块里是「复制」与「恢复」。
func (a *app) base64CommentBlock(c *ui.Context, d *detailState, token, key string) {
	plain, ok := decodeBase64Token(token)
	if !ok {
		// splitBase64Segments 已经验过一遍；这里再兜一次，免得将来别的入口
		// 直接调本函数时画出一个解不开的块。
		ui.Text(c, token).FontSize(commentFontSize).MaxLines(4)
		return
	}
	if d.base64Revealed(key) {
		a.base64DecodedBlock(c, d, plain, key)
		return
	}
	a.base64EncodedBlock(c, d, token, key)
}

// base64EncodedBlock 是编码态：淡底圆角块里放等宽的编码串，右侧固定给「原文」
// 留一个槽位。悬浮整块才画出按钮，位置一直占着，所以出现时编码串不会重排。
func (a *app) base64EncodedBlock(c *ui.Context, d *detailState, token, key string) {
	t := c.Theme()
	box := ui.Box(c).FillWidth().Padding(7, 10).Radius(6).
		Background(commentHelperBlockBg(t))
	hovered := box.Hovered()
	box.Children(func() {
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, token).Font(base64MonoFont).FontSize(12).TextColor(t.Text).
				Selectable().MinWidth(0).Grow(1).Tooltip("Base64 编码内容")
			ui.Row(c).Width(base64ActionSlot).Justify(ui.End).Children(func() {
				if !hovered {
					return
				}
				b := ui.Button(c, "原文").Background(ui.Transparent).
					Border(0, t.Border).TextColor(t.Accent).
					FontSize(12).Padding(0, 4).Radius(5).KeepFocus()
				b.Tooltip("查看解码后的原文")
				if b.Clicked() {
					d.base64SetRevealed(key, true)
				}
			})
		})
	})
}

// base64DecodedBlock 是解码态：解码后的文字换成普通正文色，下面一行放「复制」
// 与「恢复」——复制把解码结果写进剪贴板，恢复退回编码态。两个按钮常驻，因为
// 这一态本来就是用户点「原文」换来的，不该再用悬浮把操作藏起来。
func (a *app) base64DecodedBlock(c *ui.Context, d *detailState, plain, key string) {
	t := c.Theme()
	box := ui.Box(c).FillWidth().Padding(8, 10).Radius(6).
		Background(commentHelperBlockBg(t))
	box.Children(func() {
		ui.Column(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, plain).FontSize(commentFontSize).TextColor(t.Text).
				Selectable().MinWidth(0)
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				if ui.Button(c, "复制").Background(ui.Transparent).
					Border(0, t.Border).TextColor(t.Accent).
					FontSize(12).Padding(2, 6).Radius(5).
					Tooltip("把解码后的原文复制到剪贴板").Clicked() {
					a.copyToClipboard(c, plain, a.detailToast(), "已复制解码内容")
				}
				if ui.Button(c, "恢复").Background(ui.Transparent).
					Border(0, t.Border).TextColor(t.TextMuted).
					FontSize(12).Padding(2, 6).Radius(5).
					Tooltip("收起原文，恢复显示 Base64 编码").Clicked() {
					d.base64SetRevealed(key, false)
				}
			})
		})
	})
}

// base64HintText 是 Modal 里的一行说明：告诉用户插入的是什么、发出来以后怎么
// 看原文。文案保持短，Modal 是入口不是说明书。
const base64HintText = "把输入的文字编成 Base64 插入正文；发表后评论里悬浮编码串点「原文」即可解码查看。"

// base64DialogPreview 在 Modal 里给出编码结果预览：用户输入的同时就能看到会
// 插进正文的编码串，确认之前还有反悔的机会。
func (a *app) base64DialogPreview(c *ui.Context, input string) {
	t := c.Theme()
	if strings.TrimSpace(input) == "" {
		display.Text(c, "编码预览会显示在这里。", display.TextProps{
			Type: display.TextSecondary, FontSize: 12, Rows: 1,
		})
		return
	}
	ui.Box(c).FillWidth().Padding(6, 8).Radius(6).
		Background(commentHelperBlockBg(t)).Children(func() {
		ui.Text(c, encodeBase64Text(input)).Font(base64MonoFont).FontSize(12).
			TextColor(t.Text).Selectable().MinWidth(0)
	})
}
