package main

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// 发帖窗口的 @ 提及与 : 表情短码唤醒逻辑。
//
// 评论编辑器里这两套面板在 mentions.go / emoji_shortcode.go 里，状态挂在
// detailState 上；发帖窗口没有帖子上下文（没有参与人、没有抽奖），所以这里
// 复用那里的纯逻辑（token 识别、过滤、整块替换、名单解码与用户搜索），只把
// 状态与名单加载按「一个发帖窗口」重新接一遍。帖子正文与动态正文是两段独立
// 草稿，各自持有一份 composeSuggestState，切换模式互不串味。
//
// 状态只活在 composeState 上，不存 ui.Element / *ui.Context；后台名单与搜索
// 结果通过 a.publish 回到 UI 线程再写进来。

// composeSuggestState 是一个编辑框（帖子正文或动态正文）的候选面板运行态。
type composeSuggestState struct {
	mentions        mentionState
	emojiShortcodes emojiShortcodeState
}

// composeSuggestEditor 是构建某一帧时「当前编辑框」的视图：正文指针、共用
// 编辑器状态、焦点 handle、字号与它自己的候选状态。它由 composeState 的字段
// 现取现用（指针都指向 composeState 内部稳定的内存），可以安全地捕获进
// OnChange / OnTextKey 的闭包，跟着窗口重建也不会失效。
type composeSuggestEditor struct {
	body  *string
	ed    *mdEditor
	field *ui.Handle
	font  float32
	s     *composeSuggestState
}

// composeSuggestFontSize 与 Lobe field.TextArea 的字号一致，用来按输入框的
// 排版算 @ / : 的位置；composeSuggestPadX / PadY 是它根节点的留白。
const (
	composeSuggestFontSize float32 = 14
	composeSuggestPadX     float32 = 12
	composeSuggestPadY     float32 = 8
)

// composeSuggestLive 判断后台结果还属不属于这扇发帖窗口。窗口关掉后
// a.compose 会换掉或清空，过期的名单 / 搜索结果直接丢弃。
func (a *app) composeSuggestLive(st *composeState) bool {
	return st != nil && a.compose == st
}

// composeMentionExclude 是发帖时要从候选里排掉的名字：窗口自己的账号。
// 自己 @ 自己不会发通知，列出来只会误导。
func (a *app) composeMentionExclude() map[string]bool {
	out := map[string]bool{}
	if key := mentionKey(a.currentReplyName()); key != "" {
		out[key] = true
	}
	return out
}

// ------------------------------------------------------------------
// 名单加载：守护者 + 按输入搜全站用户
// ------------------------------------------------------------------

// ensureComposeGuardians 首次键入 @ 或缓存过期时取一次守护者名单。退避与
// 有效期沿用 detail 的口径，避免每帧重发注定失败的请求。
func (a *app) ensureComposeGuardians(e composeSuggestEditor, st *composeState) {
	s := &e.s.mentions
	if s.guardiansBusy || !a.autoLoad || a.client == nil || !a.composeSuggestLive(st) {
		return
	}
	now := time.Now()
	if s.guardiansLoaded {
		if now.Sub(s.guardiansAt) < mentionCacheTTL {
			return
		}
	} else if !s.triedGuardians.IsZero() &&
		now.Sub(s.triedGuardians) < mentionRetryAfter(s.failGuardians) {
		return
	}
	s.guardiansBusy = true
	s.triedGuardians = now
	ctx, cancel := context.WithTimeout(context.Background(), mentionTimeout)
	go func() {
		defer cancel()
		roster, err := a.client.fetchGuardians(ctx)
		a.publish(func() {
			if !a.composeSuggestLive(st) {
				return
			}
			s.guardiansBusy = false
			if err != nil {
				s.failGuardians++
				s.guardiansErr = friendlyError(err)
			} else {
				s.failGuardians = 0
				s.guardiansErr = ""
				s.guardiansLoaded = true
				s.guardiansAt = time.Now()
				s.guardians = roster.People
			}
			// 名单刚回来（或确认失败）时可能正卡着一个 @前缀 在等它：这里补排
			// 一次用户搜索，不必等用户再敲一个字面板才有反应。
			a.ensureComposeMentionSearch(e, st)
		})
	}()
}

// reloadComposeGuardians 清掉失败标记重来（面板里的「重试」）。
func (a *app) reloadComposeGuardians(e composeSuggestEditor, st *composeState) {
	s := &e.s.mentions
	if s.guardiansBusy {
		return
	}
	s.triedGuardians = time.Time{}
	s.guardiansErr = ""
	a.ensureComposeGuardians(e, st)
}

// ensureComposeMentionSearch 是守卫式任务：本地名单没命中时停顿一下再搜用户。
// 同一个词只排一次队、发一次请求，退格回到旧前缀直接吃缓存。
func (a *app) ensureComposeMentionSearch(e composeSuggestEditor, st *composeState) {
	s := &e.s.mentions
	sr := &s.search
	if !s.typing || !s.open || !a.autoLoad || a.client == nil || !a.composeSuggestLive(st) {
		return
	}
	q := mentionKey(s.query)
	if utf8.RuneCountInString(q) < mentionSearchMin {
		s.resetSearch()
		return
	}
	// 本地名单还没回来时先别搜：守护者里可能本来就有这个人。
	if !s.guardiansLoaded && s.guardiansErr == "" {
		return
	}
	if len(mentionPeople(s.guardians, a.composeMentionExclude(), q)) > 0 {
		s.resetSearch()
		return
	}
	if sr.query == q || sr.pending == q {
		return
	}
	if people, ok := sr.cache[q]; ok {
		sr.query, sr.people, sr.err, sr.pending = q, people, "", ""
		return
	}
	sr.pending = q
	if sr.timer != nil {
		sr.timer.Stop()
	}
	sr.timer = time.AfterFunc(mentionSearchDelay, func() {
		a.publish(func() {
			if !a.composeSuggestLive(st) {
				return
			}
			s.search.pending = ""
			a.runComposeMentionSearch(e, st)
		})
	})
}

// runComposeMentionSearch 真的发出一次搜索。query 取当前前缀，不用排队时记下的
// 那个词：防抖窗口里用户可能又改了几个字母。
func (a *app) runComposeMentionSearch(e composeSuggestEditor, st *composeState) {
	s := &e.s.mentions
	sr := &s.search
	if !s.typing || !s.open || sr.busy || !a.autoLoad || a.client == nil ||
		!a.composeSuggestLive(st) {
		return
	}
	q := mentionKey(s.query)
	if utf8.RuneCountInString(q) < mentionSearchMin || sr.query == q {
		return
	}
	if len(mentionPeople(s.guardians, a.composeMentionExclude(), q)) > 0 {
		return
	}
	sr.busy = true
	sr.query = q
	sr.err = ""
	sr.timer = nil
	sr.seq++
	seq := sr.seq
	ctx, cancel := context.WithTimeout(context.Background(), mentionSearchTimeout)
	go func() {
		defer cancel()
		people, err := a.client.searchMentionUsers(ctx, q)
		a.publish(func() {
			if !a.composeSuggestLive(st) || sr.seq != seq {
				return
			}
			sr.busy = false
			if err != nil {
				sr.err = friendlyError(err)
				// 失败后 query 留在原地，同一前缀不会再自动重发；重试走面板按钮。
				return
			}
			sr.err = ""
			sr.people = people
			if sr.cache == nil || len(sr.cache) >= mentionSearchCacheMax {
				sr.cache = make(map[string][]mentionPerson, mentionSearchCacheMax)
			}
			sr.cache[q] = people
			for _, p := range people {
				a.prefetchAvatars(p.Avatar)
			}
			// 请求在路上时用户可能又改过前缀，这里补排一次。
			a.ensureComposeMentionSearch(e, st)
		})
	}()
}

// retryComposeMentionSearch 清掉失败标记重来（面板里的「重试」）。
func (a *app) retryComposeMentionSearch(e composeSuggestEditor, st *composeState) {
	sr := &e.s.mentions.search
	sr.query = ""
	sr.err = ""
	sr.pending = ""
	a.ensureComposeMentionSearch(e, st)
}

// ------------------------------------------------------------------
// 候选数据
// ------------------------------------------------------------------

// composeMentionData 汇总面板要用的名单与搜索状态。每帧只读不算。
func (a *app) composeMentionData(e composeSuggestEditor, body string) mentionResult {
	s := &e.s.mentions
	res := mentionResult{
		Guardians:     s.guardians,
		Slots:         mentionSlots(body),
		GuardiansBusy: s.guardiansBusy,
		GuardiansErr:  s.guardiansErr,
	}
	if q := mentionKey(s.query); q != "" && s.search.query == q {
		res.Search = s.search.people
		res.SearchBusy = s.search.busy
		res.SearchErr = s.search.err
	}
	return res
}

// composeMentionRows 生成面板里的行：@随机守护者 → 守护者个人 → 搜到的用户 →
// 一行「按输入」。本地一个都没命中时才补搜索与「按输入」，所以面板永远跟着
// 输入走；名单没回来时对应项不出现，不摆点了没反应的入口。
func (a *app) composeMentionRows(s *composeSuggestState, body, query string) []mentionRow {
	exclude := a.composeMentionExclude()
	q := mentionKey(query)
	out := make([]mentionRow, 0, 8)
	divider := func() {
		if len(out) > 0 {
			out = append(out, mentionRow{Divider: true})
		}
	}
	st := &s.mentions
	if mentionSlots(body) > 0 {
		if random := pickRandomMention(st.guardians, exclude); random != "" &&
			mentionQuickHit("@随机守护者", q) {
			out = append(out, mentionRow{Key: "quick:random", Label: "@随机守护者",
				Icon: profileIcon, Text: mentionAt(random)})
		}
	}
	people := mentionPeople(st.guardians, exclude, query)
	if len(people) > mentionPanelShown {
		people = people[:mentionPanelShown]
	}
	if len(people) > 0 {
		divider()
		for _, p := range people {
			out = append(out, mentionRow{
				Key: mentionPersonKey(p), Label: "@" + p.Username, Person: p,
				Extra: "守护者", Text: mentionAt(p.Username),
			})
		}
	}
	var remote []mentionPerson
	if len(people) == 0 {
		remote = mentionRemotePeople(st.search.people, exclude, people)
	}
	if len(remote) > 0 {
		divider()
		for _, p := range remote {
			out = append(out, mentionRow{
				Key: mentionPersonKey(p), Label: "@" + p.Username, Person: p,
				Extra: "搜索", Text: mentionAt(p.Username),
			})
		}
	}
	if len(people) == 0 {
		if row, ok := mentionEchoRow(query, exclude); ok {
			divider()
			out = append(out, row)
		}
	}
	return out
}

// composeMentionPicks 是键盘上下键要走的逐人列表：本地守护者 → 搜到的用户。
// 面板逐人行与它必须同一份口径，键盘高亮的那一行就是回车会选的那一个。
func (a *app) composeMentionPicks(e composeSuggestEditor) []mentionPerson {
	st := &e.s.mentions
	exclude := a.composeMentionExclude()
	people := mentionPeople(st.guardians, exclude, st.query)
	if len(people) > mentionPanelShown {
		people = people[:mentionPanelShown]
	}
	if len(people) > 0 {
		return people
	}
	return mentionRemotePeople(st.search.people, exclude, people)
}

// composeEmojiCandidates 是 : 候选：进程级表情表按当前前缀过滤，最多画一屏。
func (a *app) composeEmojiCandidates(s *composeSuggestState) []emojiInfo {
	st := &s.emojiShortcodes
	out := editorFilteredEmojis(editorEmojis(a.emojis, nil), st.query)
	if len(out) > emojiShortcodeShown {
		out = out[:emojiShortcodeShown]
	}
	return out
}

// composeEmojiActiveKey 是键盘上下键当前指着的那一行的 List key。
func (a *app) composeEmojiActiveKey(s *composeSuggestState) string {
	st := &s.emojiShortcodes
	if !st.typing || !st.open {
		return ""
	}
	cands := a.composeEmojiCandidates(s)
	if st.active < 0 || st.active >= len(cands) {
		return ""
	}
	return "emoji:" + emojiCode(cands[st.active])
}

// ------------------------------------------------------------------
// 键入 @ / : 的开合与光标复核
// ------------------------------------------------------------------

// onComposeMentionInput 是正文 OnChange 里处理 @ 的一半：一出现 @前缀就开面板、
// 按前缀过滤；输入空格或删掉 @ 就自动收起。两类面板互斥。
func (a *app) onComposeMentionInput(e composeSuggestEditor, caret int, st *composeState) {
	s := &e.s.mentions
	start, end, ok := mentionTokenAt(*e.body, caret)
	if !ok {
		if s.typing {
			s.reset()
		}
		return
	}
	s.typing = true
	s.open = true
	s.tokenStart, s.tokenEnd = start, end
	s.query = string([]rune(*e.body)[start+1 : end])
	s.active = 0
	e.s.emojiShortcodes.reset()
	a.ensureComposeGuardians(e, st)
	a.ensureComposeMentionSearch(e, st)
}

// syncComposeMentionToken 每帧按当前光标复核 @前缀：光标移出这个词就收起，
// 移到词中间就按光标前那几个字母重新过滤。
func (a *app) syncComposeMentionToken(e composeSuggestEditor, caret int, st *composeState) {
	s := &e.s.mentions
	if !s.typing || !s.open {
		return
	}
	start, end, ok := mentionTokenAt(*e.body, caret)
	if !ok {
		s.reset()
		return
	}
	if start == s.tokenStart && end == s.tokenEnd {
		return
	}
	s.tokenStart, s.tokenEnd = start, end
	s.query = string([]rune(*e.body)[start+1 : end])
	s.active = 0
	a.ensureComposeMentionSearch(e, st)
}

// onComposeEmojiInput 是正文 OnChange 里处理 : 的一半：补空格 / 结尾冒号 /
// 删掉冒号后收起，开面板时把 @ 面板收掉。
func (a *app) onComposeEmojiInput(e composeSuggestEditor, caret int) {
	s := &e.s.emojiShortcodes
	start, end, ok := emojiShortcodeTokenAt(*e.body, caret)
	if !ok {
		if s.typing {
			s.reset()
		}
		return
	}
	s.typing = true
	s.open = true
	s.tokenStart, s.tokenEnd = start, end
	s.query = string([]rune(*e.body)[start+1 : end])
	s.active = 0
	e.s.mentions.reset()
}

// syncComposeEmojiToken 每帧按当前光标复核 : 前缀。
func (a *app) syncComposeEmojiToken(e composeSuggestEditor, caret int) {
	s := &e.s.emojiShortcodes
	if !s.typing || !s.open {
		return
	}
	start, end, ok := emojiShortcodeTokenAt(*e.body, caret)
	if !ok {
		s.reset()
		return
	}
	if start == s.tokenStart && end == s.tokenEnd {
		return
	}
	s.tokenStart, s.tokenEnd = start, end
	s.query = string([]rune(*e.body)[start+1 : end])
	s.active = 0
}

// ------------------------------------------------------------------
// 采纳候选与键盘操作
// ------------------------------------------------------------------

// insertComposeMention 把拼好的提及写进正文。键入 @ 打开的面板是整块替换：从
// @ 起到用户名末尾（连带后面的空格）全换成 "@名字 "，光标停在名字中间挑
// 候选也换整块，不会留下 "@Jimmyimmy" 这样的残字。
func insertComposeMention(e composeSuggestEditor, text string) {
	if text == "" {
		return
	}
	s := &e.s.mentions
	if s.typing && s.tokenStart >= 0 {
		start, end := mentionWordRange(*e.body, s.tokenStart)
		e.ed.replaceMentionToken(e.body, start, max(end, s.tokenEnd), text)
	} else {
		e.ed.insertText(e.body, text)
	}
	e.ed.preview = 0
	s.reset()
}

// insertComposeEmoji 把选中的短码整块替换进去（含已补上的结尾冒号）。
func insertComposeEmoji(e composeSuggestEditor, text string) {
	if text == "" {
		return
	}
	s := &e.s.emojiShortcodes
	if s.typing && s.tokenStart >= 0 {
		start, end := emojiShortcodeWordRange(*e.body, s.tokenStart)
		e.ed.replaceMentionToken(e.body, start, max(end, s.tokenEnd), text)
	} else {
		e.ed.insertText(e.body, text)
	}
	e.ed.preview = 0
	s.reset()
}

// composeMentionDeleteBlock 让写完的 @名字 像一整块：光标在块内按退格 /
// Delete 一次清掉，而不是一个字一个字往回删。
func composeMentionDeleteBlock(e composeSuggestEditor, k ui.TextKey, backspace bool) bool {
	if e.ed.selStart != e.ed.selEnd {
		return false
	}
	rs := []rune(k.Text())
	caret := e.ed.selEnd
	if caret < 0 || caret > len(rs) {
		caret = len(rs)
	}
	blocks := e.s.mentions.blocksFor(k.Text())
	var hit *mentionBlock
	for i := range blocks {
		b := &blocks[i]
		if backspace {
			if caret > b.Start && caret <= b.End {
				hit = b
				break
			}
		} else if caret >= b.Start && caret < b.End {
			hit = b
			break
		}
	}
	if hit == nil {
		return false
	}
	out := string(rs[:hit.Start]) + string(rs[hit.End:])
	*e.body = out
	e.ed.pendingSel = composeSelection{ok: true, start: hit.Start, end: hit.Start}
	e.ed.selStart, e.ed.selEnd = hit.Start, hit.Start
	k.SetText(out)
	return true
}

// composeEmojiDeleteBlock 让写完的 :sad: 像一整块，与 @ 块同一个道理。
func composeEmojiDeleteBlock(e composeSuggestEditor, k ui.TextKey, backspace bool) bool {
	if e.ed.selStart != e.ed.selEnd {
		return false
	}
	rs := []rune(k.Text())
	caret := e.ed.selEnd
	if caret < 0 || caret > len(rs) {
		caret = len(rs)
	}
	blocks := emojiShortcodeBlocks(k.Text())
	var hit *emojiBlock
	for i := range blocks {
		b := &blocks[i]
		if backspace {
			if caret > b.Start && caret <= b.End {
				hit = b
				break
			}
		} else if caret >= b.Start && caret < b.End {
			hit = b
			break
		}
	}
	if hit == nil {
		return false
	}
	out := string(rs[:hit.Start]) + string(rs[hit.End:])
	*e.body = out
	e.ed.pendingSel = composeSelection{ok: true, start: hit.Start, end: hit.Start}
	e.ed.selStart, e.ed.selEnd = hit.Start, hit.Start
	k.SetText(out)
	return true
}

// composeMentionTextKey 让 @ 候选纯键盘操作：上下换人、回车 / Tab 采纳、
// Escape 收起；退格 / Delete 处理整块删除。面板没开时不消费按键。
func (a *app) composeMentionTextKey(e composeSuggestEditor, k ui.TextKey) bool {
	if k.Key == ui.KeyBackspace || k.Key == ui.KeyDelete {
		return composeMentionDeleteBlock(e, k, k.Key == ui.KeyBackspace)
	}
	s := &e.s.mentions
	if !s.typing || !s.open {
		return false
	}
	switch k.Key {
	case ui.KeyUp, ui.KeyDown:
		people := a.composeMentionPicks(e)
		if len(people) == 0 {
			return false
		}
		step := 1
		if k.Key == ui.KeyUp {
			step = -1
		}
		s.active = (s.active + step + len(people)) % len(people)
		return true
	case ui.KeyEnter, ui.KeyTab:
		people := a.composeMentionPicks(e)
		if s.active < 0 || s.active >= len(people) {
			return false
		}
		insertComposeMention(e, mentionAt(people[s.active].Username))
		k.SetText(*e.body)
		return true
	case ui.KeyEscape:
		s.reset()
		return true
	}
	return false
}

// composeEmojiTextKey 让 : 候选纯键盘操作，与 composeMentionTextKey 串在
// OnTextKey 里，各自只认自己的块与面板。
func (a *app) composeEmojiTextKey(e composeSuggestEditor, k ui.TextKey) bool {
	if k.Key == ui.KeyBackspace || k.Key == ui.KeyDelete {
		return composeEmojiDeleteBlock(e, k, k.Key == ui.KeyBackspace)
	}
	s := &e.s.emojiShortcodes
	if !s.typing || !s.open {
		return false
	}
	cands := a.composeEmojiCandidates(e.s)
	switch k.Key {
	case ui.KeyUp, ui.KeyDown:
		if len(cands) == 0 {
			return false
		}
		step := 1
		if k.Key == ui.KeyUp {
			step = -1
		}
		s.active = (s.active + step + len(cands)) % len(cands)
		return true
	case ui.KeyEnter, ui.KeyTab:
		if s.active < 0 || s.active >= len(cands) {
			return false
		}
		insertComposeEmoji(e, emojiShortcode(cands[s.active]))
		k.SetText(*e.body)
		return true
	case ui.KeyEscape:
		s.reset()
		return true
	}
	return false
}

// resetComposeSuggest 在切换帖子 / 动态模式时收起两块面板：草稿各存各的，
// 面板也不该跟着切过去。
func resetComposeSuggest(st *composeState) {
	if st == nil {
		return
	}
	st.bodySuggest.mentions.reset()
	st.bodySuggest.emojiShortcodes.reset()
	st.momentSuggest.mentions.reset()
	st.momentSuggest.emojiShortcodes.reset()
}
