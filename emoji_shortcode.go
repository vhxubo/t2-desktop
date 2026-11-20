package main

import (
	"unicode"

	"github.com/egoist/mygo/ui"
)

// 表情短码（:sad:）的键入补全。与 @ 提及共用一套做法，但没有名单与网络
// 请求：候选直接来自进程级表情表叠上当前帖子的社区图标（见 a.emojis、
// replyCommunityEmojis），只活在 detailState 上，不存 ui.Element / *ui.Context。
//
// 键入 ":" 打开候选，继续打字按短码 / 中文名过滤；选中时把从 ":" 到这段末尾
// 整段换成 ":sad:"。写完的 ":sad:" 是一个整体，光标落在里面按退格 / Delete
// 一次清掉，不会一个冒号一个字往回啃。打完结尾的冒号或补空格即收起面板，
// 与 @ 后面补空格结束是同一个意思。

const (
	// emojiShortcodeShown 限制一次画出的候选行数：表情表可能有几百条，全铺
	// 在气泡里既慢又挑不过来，继续打字能把范围缩到要找的那个。
	emojiShortcodeShown = 30
)

// emojiShortcodeState 是 : 候选面板的运行态。它与 mentionState 互斥：同一
// 时刻评论框只画一块候选面板（见 onEmojiShortcodeInput / onMentionInput）。
type emojiShortcodeState struct {
	open   bool
	host   replySurface
	typing bool
	query  string // ":" 之后已敲入的字符，不含首尾冒号
	// tokenStart / tokenEnd 是这次 ":" 开头那段在正文里的 rune 范围。
	tokenStart, tokenEnd int
	// active 是键盘上下键选中的候选下标，鼠标点击不依赖它。
	active int
	// 光标定位要重新排版草稿；正文或宽度没变时复用，避免长草稿每帧整形。
	shapeText  string
	shapeWidth float32
	shape      *ui.TextLayout
}

// reset 收起面板并清掉这一轮 ":" 前缀。缓存的表情表不动。
func (st *emojiShortcodeState) reset() {
	st.open = false
	st.typing = false
	st.query = ""
	st.tokenStart, st.tokenEnd = -1, -1
	st.active = 0
}

// emojiBlock 是一个写完的短码（含首尾冒号）在正文里的 rune 范围 [Start, End)。
type emojiBlock struct{ Start, End int }

// isShortcodeRune 判断一个字符能否出现在短码里。口径与服务端的短码正则一致
// （字母、数字、下划线，含中文），邮箱与 URL 里的冒号因此不会被当成短码。
func isShortcodeRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// emojiShortcodeBlocks 扫出正文里所有写完的 ":code:"。只认首尾成对冒号、
// 中间至少有一个短码字符的写法；正在输入的 ":sa" 不会被当成一整块删掉。
func emojiShortcodeBlocks(body string) []emojiBlock {
	rs := []rune(body)
	var out []emojiBlock
	for i := 0; i < len(rs); i++ {
		if rs[i] != ':' {
			continue
		}
		j := i + 1
		for j < len(rs) && isShortcodeRune(rs[j]) {
			j++
		}
		if j == i+1 || j >= len(rs) || rs[j] != ':' {
			continue
		}
		out = append(out, emojiBlock{Start: i, End: j + 1})
		i = j
	}
	return out
}

// emojiShortcodeWordRange 把 start 处的 ":" 扩到这段末尾（下一个空白为止），
// 供选中候选时整段替换。光标停在短码中间也不会在后面留下残字。
func emojiShortcodeWordRange(body string, start int) (int, int) {
	rs := []rune(body)
	start = max(0, min(start, len(rs)))
	if start >= len(rs) || rs[start] != ':' {
		return start, start
	}
	end := start + 1
	for end < len(rs) && !unicode.IsSpace(rs[end]) {
		end++
	}
	return start, end
}

// emojiShortcodeTokenAt 找光标处正在输入的 ":" 前缀。向后扫到空白，只认以
// ":" 开头、后面全是短码字符的一段；结尾已经补上冒号表示这段写完了，不再
// 弹候选（和 @ 后面补空格收起是同一条规则）。
func emojiShortcodeTokenAt(body string, caret int) (int, int, bool) {
	rs := []rune(body)
	if caret < 0 || caret > len(rs) {
		return 0, 0, false
	}
	i := caret - 1
	for i >= 0 && !unicode.IsSpace(rs[i]) {
		i--
	}
	start := i + 1
	if start >= caret || rs[start] != ':' {
		return 0, 0, false
	}
	if caret > start+1 && rs[caret-1] == ':' {
		return 0, 0, false
	}
	for k := start + 1; k < caret; k++ {
		if !isShortcodeRune(rs[k]) {
			return 0, 0, false
		}
	}
	return start, caret, true
}

// onEmojiShortcodeInput 是评论输入框 OnChange 里处理 ":" 的一半：正文一出现
// ":" 前缀就打开候选、按前缀过滤；补空格 / 结尾冒号 / 删掉冒号后自动收起。
// 手动点工具条表情入口不经过这条路径，不受正文输入影响。
func (a *app) onEmojiShortcodeInput(d *detailState, host replySurface, caret int) {
	st := &d.emojiShortcodes
	start, end, ok := emojiShortcodeTokenAt(d.draft, caret)
	if !ok {
		if st.typing {
			st.reset()
		}
		return
	}
	st.typing = true
	st.open = true
	st.host = host
	st.tokenStart, st.tokenEnd = start, end
	st.query = string([]rune(d.draft)[start+1 : end])
	st.active = 0
	// 同一时刻只画一块候选面板：从 @ 切到 : 时把提及面板收掉。
	d.mentions.reset()
}

// syncEmojiShortcodeToken 每帧按当前光标复核 ":" 前缀：光标移出这段就收起，
// 移到中间就按光标前那几个字符重新过滤。只看 OnChange 的话，光标动了而正文
// 没变，面板会停在旧位置、按旧前缀过滤。
func (a *app) syncEmojiShortcodeToken(d *detailState) {
	st := &d.emojiShortcodes
	if !st.typing || !st.open {
		return
	}
	start, end, ok := emojiShortcodeTokenAt(d.draft, d.mdEditor.selEnd)
	if !ok {
		st.reset()
		return
	}
	if start == st.tokenStart && end == st.tokenEnd {
		return
	}
	st.tokenStart, st.tokenEnd = start, end
	st.query = string([]rune(d.draft)[start+1 : end])
	st.active = 0
}

// emojiShortcodeCandidates 是当前候选：全局表叠上帖子自带的社区图标，按短码
// 或中文名过滤，再截到一次画得出的行数。键盘上下键与面板列表共用这一份，
// 高亮的那一行就是回车会插进去的那一个。
func (a *app) emojiShortcodeCandidates(d *detailState) []emojiInfo {
	list := editorEmojis(a.emojis, a.replyCommunityEmojis(d))
	out := editorFilteredEmojis(list, d.emojiShortcodes.query)
	if len(out) > emojiShortcodeShown {
		out = out[:emojiShortcodeShown]
	}
	return out
}

// emojiShortcodeActiveKey 是键盘上下键当前指着的那一行的 List key。
func (a *app) emojiShortcodeActiveKey(d *detailState) string {
	st := &d.emojiShortcodes
	if !st.typing || !st.open {
		return ""
	}
	cands := a.emojiShortcodeCandidates(d)
	if st.active < 0 || st.active >= len(cands) {
		return ""
	}
	return "emoji:" + emojiCode(cands[st.active])
}
