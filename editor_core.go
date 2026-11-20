package main

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// 发帖窗口与评论编辑器共用的一套 Markdown 编辑状态。
//
// 两边都是「一个字符串正文 + 一个 TextArea」，选区、工具条插入、编辑/预览
// 切换、表情面板与图片面板的行为完全相同。状态放在这里，正文指针由调用方
// 传进来（发帖是 composeState.body，评论是 detailState.draft），这样两边
// 都不必保存 ui.Element 或 *ui.Context。
//
// composeState 与 detailState 都匿名嵌入它，所以 st.selStart、st.preview
// 这类字段在外层仍可直接读写，旧的调用与测试不受影响。

// composeSelection 是一段按 rune 计的选区。ok 为真表示插入内容后要把
// 光标 / 选区恢复到这里。
type composeSelection struct {
	ok         bool
	start, end int
}

// mdEditor 是编辑器的纯状态：只在 UI 线程读写。
type mdEditor struct {
	// 选区来自上一帧构建输入框时读到的值；pendingSel 是刚插入内容后
	// 希望恢复的选区，由同一个（或下一个）构建帧里的输入框执行。
	selStart, selEnd int
	pendingSel       composeSelection
	// preview 是视图：0 编辑，1 预览。
	preview int
	// emojiOpen / imageOpen 是两个工具面板；两者互斥，打开一个就收起另一个。
	emojiOpen bool
	imageOpen bool
	// emojiQuery 是表情搜索词；emojiOnlyPost 为真时只看当前帖子自带的表情。
	emojiQuery    string
	emojiOnlyPost bool
	// emojiTab 切换社区图片表情（0）与内置系统表情（1）。
	emojiTab int
	// emojiCategory 是系统表情的分类筛选，值为中文标签，空串与「全部」同义。
	emojiCategory string
	// emojiShown 限制一次画出的表情数，面板用「显示更多」逐批展开。
	emojiShown int
	// base64Open 是「Base64 编码」Modal 的开合；base64Input 是 Modal 里的原文
	// 输入框。确认后把编码串插进正文，窗口关掉、下次打开都能重来。
	base64Open  bool
	base64Input string
	// 插入帖子选择器（GET /v1/me/topics）：面板开合、搜索词、结果和退避。
	// 与表情 / 图片一样是纯 UI 状态，跟着各自编辑器的生命周期走。
	topicOpen     bool
	topicQuery    string
	topicQueried  string
	topicLoading  bool
	topicLoaded   bool
	topicErr      string
	topicSeq      uint64
	topicItems    []myTopicPick
	topicTriedAt  time.Time
	topicFailures int
	// topicEditAt 是检测到搜索词变化的时间，用来给远程搜索做短暂防抖：
	// 打一串字只发一次请求，而不是每敲一个键都打一次接口。
	topicEditAt time.Time
}

// systemEmojiAll 是系统表情分类筛选的「全部」选项，与选择器里的第一项一致。
const systemEmojiAll = "全部"

// newMDEditor 给一份默认状态：表情面板每批 40 个。
func newMDEditor() mdEditor {
	return mdEditor{emojiShown: emojiPageSize, emojiCategory: systemEmojiAll}
}

const (
	emojiPageSize = 40
	emojiColumns  = 10
	// emojiCellSize 是表情格子的边长：图标 20 DIP 加一圈内边距。格子定宽，
	// 只有两三个表情时不会被均分拉成半屏一个。
	emojiCellSize float32 = 30
	// uploadPlaceholderPrefix 是图片还没传完时正文里的临时 URL。
	// 预览会先把这些占位符换成中文说明，绝不会把它们交给 Markdown 渲染器。
	uploadPlaceholderPrefix = "](upload:"
)

// ------------------------------------------------------------------
// 插入 Markdown 标记
// ------------------------------------------------------------------

// runeSel 把选区夹到正文范围内并归一顺序。
func (e *mdEditor) runeSel(body string) ([]rune, int, int) {
	rs := []rune(body)
	a, b := e.selStart, e.selEnd
	if a > b {
		a, b = b, a
	}
	a = max(0, min(a, len(rs)))
	b = max(0, min(b, len(rs)))
	return rs, a, b
}

// wrapSelection 用 prefix / suffix 包住选区；没有选区时插入 placeholder
// 并把它选中，接着打字就是替换占位文字。
func (e *mdEditor) wrapSelection(body *string, prefix, suffix, placeholder string) {
	rs, a, b := e.runeSel(*body)
	inner := string(rs[a:b])
	if inner == "" {
		inner = placeholder
	}
	*body = string(rs[:a]) + prefix + inner + suffix + string(rs[b:])
	start := a + utf8.RuneCountInString(prefix)
	e.pendingSel = composeSelection{ok: true, start: start, end: start + utf8.RuneCountInString(inner)}
}

// prefixLines 给选区覆盖的每一行加前缀；没有选区时只处理光标所在的那一行。
// numbered 为真时用 1. 2. 3. 的行号前缀（有序列表）。
func (e *mdEditor) prefixLines(body *string, prefix string, numbered bool) {
	rs, a, b := e.runeSel(*body)
	from := editorLineStart(rs, a)
	to := b
	if a == b {
		to = editorLineEnd(rs, a)
	}
	lines := strings.Split(string(rs[from:to]), "\n")
	for i, ln := range lines {
		marker := prefix
		if numbered {
			marker = strconv.Itoa(i+1) + ". "
		}
		lines[i] = marker + ln
	}
	seg := strings.Join(lines, "\n")
	*body = string(rs[:from]) + seg + string(rs[to:])
	e.pendingSel = composeSelection{ok: true, start: from, end: from + utf8.RuneCountInString(seg)}
}

// insertText 在选区处插入一段文字（替换选区），并把光标放到插入内容之后。
func (e *mdEditor) insertText(body *string, text string) {
	rs, a, b := e.runeSel(*body)
	*body = string(rs[:a]) + text + string(rs[b:])
	at := a + utf8.RuneCountInString(text)
	e.pendingSel = composeSelection{ok: true, start: at, end: at}
	e.selStart, e.selEnd = at, at
}

// replaceMentionToken 把正文里 [start,end) 的 @前缀 整段换成 text（例如
// 选完候选后的 "@名字 "），并把光标放到替换后的末尾。选区直接记在状态里，
// 下一帧由输入框的 syncSelection 还回去；这里不碰任何 ui.Element。
func (e *mdEditor) replaceMentionToken(body *string, start, end int, text string) {
	rs := []rune(*body)
	if start < 0 || end > len(rs) || start > end {
		return
	}
	*body = string(rs[:start]) + text + string(rs[end:])
	at := start + utf8.RuneCountInString(text)
	e.pendingSel = composeSelection{ok: true, start: at, end: at}
	e.selStart, e.selEnd = at, at
}

// insertMany 一次插入多段文字（例如拖进来的几张图），每段接在前一段之后。
// 它不走 pendingSel：同一个构建帧里连续插入时，选区必须就地推进，否则
// 后面的图片会全部叠在第一个位置。
func (e *mdEditor) insertMany(body *string, texts []string) {
	if len(texts) == 0 {
		return
	}
	rs, a, b := e.runeSel(*body)
	var sb strings.Builder
	sb.WriteString(string(rs[:a]))
	at := a
	for _, text := range texts {
		sb.WriteString(text)
		at += utf8.RuneCountInString(text)
	}
	sb.WriteString(string(rs[b:]))
	*body = sb.String()
	e.selStart, e.selEnd = at, at
	e.pendingSel = composeSelection{ok: true, start: at, end: at}
}

func editorLineStart(rs []rune, at int) int {
	for at > 0 && rs[at-1] != '\n' {
		at--
	}
	return at
}

func editorLineEnd(rs []rune, at int) int {
	for at < len(rs) && rs[at] != '\n' {
		at++
	}
	return at
}

// syncSelection 处理工具条插入标记之后的选区：pendingSel 是刚算好的位置，
// 交给同一个构建帧里的输入框；否则把当前选区记下来，供下一帧的工具条
// 按钮使用（元素出构建帧就失效，所以选区必须先读进状态）。
func (e *mdEditor) syncSelection(area ui.Element) {
	if e.pendingSel.ok {
		area.SetTextSelection(e.pendingSel.start, e.pendingSel.end)
		e.selStart, e.selEnd = e.pendingSel.start, e.pendingSel.end
		e.pendingSel = composeSelection{}
		return
	}
	e.selStart, e.selEnd = area.TextSelection()
}

// togglePanel 打开一个面板时收起另一个；再点同一个就是收起。
func (e *mdEditor) togglePanel(which string) {
	switch which {
	case "emoji":
		e.emojiOpen = !e.emojiOpen
		if e.emojiOpen {
			e.imageOpen = false
			e.topicOpen = false
			e.emojiShown = emojiPageSize
		}
	case "image":
		e.imageOpen = !e.imageOpen
		if e.imageOpen {
			e.emojiOpen = false
			e.topicOpen = false
		}
	case "topic":
		e.topicOpen = !e.topicOpen
		if e.topicOpen {
			e.emojiOpen = false
			e.imageOpen = false
		}
	case "base64":
		e.base64Open = !e.base64Open
		if e.base64Open {
			e.emojiOpen = false
			e.imageOpen = false
			e.topicOpen = false
		}
	}
}

// ------------------------------------------------------------------
// 表情表与预览
// ------------------------------------------------------------------

// editorEmojis 把全局表情表与当前帖子自带的社区表情合并成一份按短码排序的
// 列表。map 的遍历顺序不稳定，工具面板必须先排序，否则每次重绘顺序都变。
func editorEmojis(base map[string]emojiInfo, extra map[string]string) []emojiInfo {
	merged := mergeEmojiMaps(base, extra)
	out := make([]emojiInfo, 0, len(merged))
	for _, e := range merged {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out
}

// editorCommunityCodes 是「只看本帖表情」时用的短码集合。
func editorCommunityCodes(extra map[string]string) map[string]bool {
	out := make(map[string]bool, len(extra))
	for code := range extra {
		out[code] = true
	}
	return out
}

// editorFilteredEmojis 按搜索词与「只看本帖」筛选。搜索同时匹配短码与中文名。
func editorFilteredEmojis(list []emojiInfo, query string, onlyPost bool, community map[string]bool) []emojiInfo {
	q := strings.ToLower(strings.TrimSpace(query))
	out := make([]emojiInfo, 0, len(list))
	for _, e := range list {
		if onlyPost && !community[e.Code] {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(emojiCode(e)), q) &&
			!strings.Contains(strings.ToLower(e.Name), q) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// emojiCode 是表情不带冒号的短码：服务端表情表的 code 自带冒号（":sigh:"），
// 社区表情表里的短码则不一定。内部一律按不带冒号的写法比较与搜索。
func emojiCode(e emojiInfo) string {
	return strings.Trim(e.Code, ":")
}

// emojiShortcode 是插入正文、以及悬停提示里显示的短码，永远只有一层冒号。
// 直接拼 ":"+Code+":" 会在服务端表情上变成「::sigh::」——正文里短码匹配
// 不到，预览和详情页就把它原样显示出来。
func emojiShortcode(e emojiInfo) string {
	if code := emojiCode(e); code != "" {
		return ":" + code + ":"
	}
	return ""
}

// systemEmojiOptions 给分类下拉用：第一项是「全部」。
func systemEmojiOptions() []string {
	opts := make([]string, 0, len(systemEmojiCategories)+1)
	opts = append(opts, systemEmojiAll)
	for _, cat := range systemEmojiCategories {
		opts = append(opts, cat.Label)
	}
	return opts
}

// filteredSystemEmojis 按分类与搜索词筛选内置系统表情。搜索匹配英文别名，
// 与线上 emoji-picker-react 的英文搜索保持一致。
func filteredSystemEmojis(query, category string) []systemEmoji {
	q := strings.ToLower(strings.TrimSpace(query))
	var catID string
	if category != "" && category != systemEmojiAll {
		for _, c := range systemEmojiCategories {
			if c.Label == category {
				catID = c.ID
				break
			}
		}
	}
	out := make([]systemEmoji, 0, len(systemEmojis))
	for _, e := range systemEmojis {
		if catID != "" && e.Category != catID {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Names), q) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// editorPendingUploads 数出正文里还没换成真实 URL 的图片占位符。发布前用它
// 拦一道，避免把 ![…](upload:ID) 这种临时标记发到服务端。
func editorPendingUploads(body string) int {
	return strings.Count(body, uploadPlaceholderPrefix)
}

// editorPreviewSource 把草稿变成可交给 Markdown 渲染器的源文：
// 先按上传任务的状态把占位符换成一段中文说明，再把 :code: 表情换成行内图片。
// 图片还在传、或传失败时预览里不会出现一个坏掉的链接。
func (a *app) editorPreviewSource(body string, emojis map[string]emojiInfo) string {
	for _, j := range a.uploads {
		if j.Placeholder == "" || !strings.Contains(body, j.Placeholder) {
			continue
		}
		switch j.State {
		case uploadDone:
			// 正常路径上占位符已被换掉；这里处理用户手工把占位符粘回来的
			// 情况，直接显示已拿到的真实地址，别再写成「正在上传」。
			if j.URL != "" {
				body = strings.ReplaceAll(body, j.Placeholder,
					"!["+uploadImageAlt(j.Name)+"]("+j.URL+")")
				continue
			}
		case uploadFailed:
			body = strings.ReplaceAll(body, j.Placeholder,
				"*（图片上传失败："+j.Name+"）*")
			continue
		}
		body = strings.ReplaceAll(body, j.Placeholder, "*（正在上传图片："+j.Name+"）*")
	}
	return replaceEmoji(body, emojis, true)
}
