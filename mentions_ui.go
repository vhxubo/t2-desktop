package main

import (
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 评论输入框里的 @ 候选面板：一个角贴着正文里那个 @，键入 @ 即打开，选中后
// 把整块 @前缀 换成 "@名字 "（名字后的空格由拼串补上，服务端只认这个写法）。
// 名单为空 / 无权 / 超过 50 人时隐藏对应入口，而不是点一下没反应。
//
// 界面全用 Lobe 组件：浮层是 BgElevated + BoxShadowSecondary 的面板，行是
// display.List（头像 / 图标、名称、分身标记、悬浮与键盘选中态都由它画），
// 转圈、错误与重试走 feedback / display / action。尺寸跟着内容走：宽度由
// 最长的一行撑开（只兜住上下限），高度由行数决定，超出 @ 上下方的空间才滚。
//
// 页面评论框和独立写评论窗口共用 detailState，所以面板状态带 host：只有
// 点开它的那扇窗口画内容。

const (
	// mentionPanelShown 限制一次画出的参与人行数：名单全量不分页，几百人
	// 一次铺满面板既慢又没法挑，搜索框可以把范围缩到要找的人。
	mentionPanelShown = 50
	// 面板宽度由最长的一行撑开，这两个值只兜住「太窄」与「用户名太长」。
	mentionPanelMinW float32 = 176
	mentionPanelMaxW float32 = 300
	// mentionPanelMaxH 是内容区的高度上限；@ 上方 / 下方的空间更小时按空间来。
	mentionPanelMaxH float32 = 232
	// mentionPanelMargin 是面板与窗口边缘至少留的间距，mentionPanelGap 是
	// 面板与 @ 那一行之间的缝。
	mentionPanelMargin float32 = 8
	mentionPanelGap    float32 = 4
	// mentionPanelPad 是面板内边距，也是面板与行之间露出的那一圈底。
	mentionPanelPad float32 = 4
	// 下面几个是 List 一行的固定尺寸，用来估高度与宽度：头像槽 22、行上下
	// 内边距 6、行左右内边距 8（Compact）、文字前的间距 12。
	mentionRowH       float32 = 34
	mentionRowGap     float32 = 2
	mentionDividerH   float32 = 9
	mentionRowLead    float32 = 22
	mentionRowLeadGap float32 = 12
	mentionRowPadX    float32 = 8
	// mentionRowChrome 是一行的固定开销：面板内边距、行内边距、头像槽与间距。
	mentionRowChrome = mentionPanelPad*2 + mentionRowPadX*2 + mentionRowLead + mentionRowLeadGap
	// 行里文字的字号，与 List 的行一致；分身标记走 Extra，小一号。
	mentionFontSize  float32 = 14
	mentionExtraSize float32 = 12
	mentionExtraGap  float32 = 12
	// mentionStatusH 是列表上方状态行（转圈 / 错误 + 重试）的估计高度。
	mentionStatusH float32 = 34
)

// mentionAtRect 用与输入框一致的排版算出 @ 符号本身的矩形。锚的是 @ 字符
// （tokenStart），不是会继续移动的输入光标：用户 typing 时面板稳稳钉在那个
// @ 上，不会每敲一个字就往右滑一格。正文或宽度没变时复用排版，长草稿不每帧
// 重算。
func mentionAtRect(c *ui.Context, d *detailState, padX, padY float32) ui.Rect {
	field := d.composerField.Bounds(c)
	width := max(40, field.W-padX*2)
	st := &d.mentions
	if st.shape == nil || st.shapeText != d.draft || st.shapeWidth != width {
		st.shape = ui.ShapeText(d.draft, ui.Font{Size: commentFontSize}, width)
		st.shapeText, st.shapeWidth = d.draft, width
	}
	rs := []rune(d.draft)
	at := st.tokenStart
	if at < 0 || at > len(rs) {
		at = 0
	}
	units := 0
	for _, ch := range rs[:at] {
		if utf8.RuneLen(ch) > 2 {
			units += 2
		} else {
			units++
		}
	}
	r := st.shape.Caret(units)
	return ui.Rect{X: field.X + padX + r.X, Y: field.Y + padY + r.Y, W: r.W, H: r.H}
}

// mentionPopover 让面板的一个角紧贴 @：下方放得下就从 @ 的左下角向下展开，
// 放不下就把面板下沿贴到 @ 上方；两边都不够时选大的那一边，内容自己滚。
// 面板不超出窗口左右边缘（@ 在行尾时往左让），也不会盖住窗口标题栏。
func (a *app) mentionPopover(c *ui.Context, d *detailState, host replySurface, padX, padY float32) {
	st := &d.mentions
	if !st.open || st.host != host {
		return
	}
	data := a.mentionData(d, d.draft)
	rows := a.mentionRows(d, data)
	status := mentionPanelStatus(data, st)
	if len(rows) == 0 && !status {
		// 一个可 @ 的人都没有：不摆一块空面板，等名单回来或用户改搜索词。
		return
	}
	tk := lobetheme.TokensOf(c)
	at := mentionAtRect(c, d, padX, padY)
	winW, winH := c.Size()
	width := st.panelWidth(rows)

	chrome := float32(0)
	if status {
		chrome = mentionStatusH
	}
	// 上下各能让出多少：扣掉与窗口边缘的间距、与 @ 之间的缝。
	above := at.Y - mentionPanelMargin - mentionPanelGap
	below := winH - at.Y - at.H - mentionPanelMargin - mentionPanelGap
	want := mentionRowsHeight(rows) + chrome
	up := below < want && above > below
	space := below
	if up {
		space = above
	}
	// 高度上限取「内容高度」与「这一侧的空间」里更小的那个：行数少时面板就
	// 矮，行数多时正好铺到窗口边缘再滚动。
	maxH := min(mentionPanelMaxH, max(mentionRowH, space-chrome))

	left := at.X
	if left+width > winW-mentionPanelMargin {
		left = winW - mentionPanelMargin - width
	}
	left = max(left, mentionPanelMargin)

	ui.Overlay(c, func() {
		dismiss := ui.Box(c).Absolute().Left(0).Top(0).Fill()
		dismiss.Label("收起提及候选").OnClick(func() {
			st.open = false
			// 顺手停掉排队中的用户搜索：面板都收起了，不该再花一次配额。
			st.resetSearch()
		})

		// KeepFocus：在面板里点行、点重试都不把焦点从输入框抢走，选完人
		// 光标还在原处，接着就能打字。
		panel := ui.Column(c).Width(width).Padding(mentionPanelPad).Gap(mentionPanelGap).
			Radius(tk.BorderRadius).Background(tk.BgElevated).TextColor(tk.Text).
			FontSize(tk.FontSize).KeepFocus().Label("提及候选面板")
		if up {
			panel.Absolute().Left(left).Bottom(winH - at.Y + mentionPanelGap)
		} else {
			panel.Absolute().Left(left).Top(at.Y + at.H + mentionPanelGap)
		}
		tk.Elevate(panel, lobetheme.BoxShadowSecondary)
		if panel.OverlayShortcut(0, ui.KeyEscape) {
			st.open = false
		}
		panel.Children(func() { a.mentionPanel(c, d, host, rows, data, maxH) })
	})
}

// mentionPanelStatus 表示列表上方要不要画一行状态（转圈或错误 + 重试）。
func mentionPanelStatus(data mentionResult, st *mentionState) bool {
	if data.PeopleBusy && !st.participantsLoaded {
		return true
	}
	if data.PeopleErr != "" {
		return true
	}
	// 本地没命中、正在搜用户：给一行转圈，别让人以为面板只肯给「按输入」。
	if data.SearchBusy && len(data.Search) == 0 {
		return true
	}
	if data.SearchErr != "" {
		return true
	}
	return st.guardiansErr != "" && !st.guardiansLoaded && !data.GuardiansBusy
}

// mentionPanel 只放可操作的项：快捷拼串行与参与人行共用一份 Lobe List（快捷
// 行固定排在最前面），上面留加载转圈与失败重试。没有任何说明性文案。
func (a *app) mentionPanel(c *ui.Context, d *detailState, host replySurface,
	rows []mentionRow, data mentionResult, maxH float32) {
	st := &d.mentions
	if data.PeopleBusy && !st.participantsLoaded {
		lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
	}
	if data.PeopleErr != "" {
		display.Text(c, data.PeopleErr, display.TextProps{
			Type: display.TextWarning, FontSize: 11, Rows: 2,
		})
		if !data.PeopleBusy && !data.PeopleDenied {
			if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.reloadMentionParticipants(d)
			}
		}
	}
	if st.guardiansErr != "" && !st.guardiansLoaded && !data.GuardiansBusy {
		if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
			a.reloadMentionGuardians(d)
		}
	}
	// 搜用户失败：只影响「本地没命中」这条补位链路，说清原因并给重试；
	// 下面的「按输入」仍然可用，所以不是死路。
	if data.SearchBusy && len(data.Search) == 0 {
		lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
	}
	if data.SearchErr != "" {
		display.Text(c, "搜索用户失败："+data.SearchErr, display.TextProps{
			Type: display.TextWarning, FontSize: 11, Rows: 2,
		})
		if !data.SearchBusy {
			if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.retryMentionSearch(d)
			}
		}
	}
	if len(rows) == 0 {
		return
	}
	// 列表高度跟着内容：行少时面板就矮，行多时到上限再滚动。
	ui.Scroll(c).MaxHeight(maxH).Children(func() {
		if key := display.List(c, display.ListProps{
			Items:   a.mentionListItems(c, rows),
			Active:  a.mentionActiveKey(d),
			Compact: true,
		}); key != "" {
			for _, row := range rows {
				if row.Key != key {
					continue
				}
				st.host = host
				a.insertMention(d, row.Text)
				break
			}
		}
	})
}

// mentionRow 是面板里的一行。快捷拼串行与参与人行只差左边是图标还是头像，
// 点一下都把 Text 写进正文。
type mentionRow struct {
	Key     string
	Label   string
	Icon    *ui.SVG
	Person  mentionPerson
	Extra   string
	Text    string
	Divider bool
}

// mentionPersonKey 是参与人行的键：按归一后的用户名取，名单里重名的只留一行。
func mentionPersonKey(p mentionPerson) string { return "person:" + mentionKey(p.Username) }

// mentionRows 生成面板里的行：快捷拼串在前，参与人按当前 @前缀 过滤在后；
// 本地一个都没命中时，再补上搜到的用户与一行「按输入」——面板因此永远跟着
// 输入走，不会因为名单里没这个人就空掉。名单还没回来或名额已满时对应项不
// 出现，不摆一个点了没反应的入口。抽奖助手只在自己的帖子里出现：服务端只认
// 帖主发的一级评论，换账号回复或别人的帖子写了也不开奖，所以入口与工具条用
// 同一套作者判断（见 canLottery）。
func (a *app) mentionRows(d *detailState, data mentionResult) []mentionRow {
	exclude := a.mentionExclude(d)
	query := mentionKey(d.mentions.query)
	out := make([]mentionRow, 0, 8)
	// 分隔线只在两段之间出现一次，避免空段留下一条孤零零的线。
	divider := func() {
		if len(out) > 0 {
			out = append(out, mentionRow{Divider: true})
		}
	}
	if data.Slots > 0 {
		// 快捷行也按输入过滤：打了 "@所" 仍能挑到 @所有人，打了别的字就让位给
		// 逐人候选，不再占着最上面三行。
		if all := buildMentions(data.Participants, exclude, data.Slots); all != "" &&
			data.Total <= mentionNotifyLimit && mentionQuickHit("@所有人", query) {
			out = append(out, mentionRow{Key: "quick:all", Label: "@所有人",
				Icon: accountsIcon, Text: all})
		}
		if random := pickRandomMention(data.Guardians, exclude); random != "" &&
			mentionQuickHit("@随机守护者", query) {
			out = append(out, mentionRow{Key: "quick:random", Label: "@随机守护者",
				Icon: profileIcon, Text: mentionAt(random)})
		}
		if a.canLottery(d) && !a.replyIdentityFor(d).Switched &&
			mentionQuickHit("@"+lotteryName, query) {
			out = append(out, mentionRow{Key: "quick:lottery", Label: "@" + lotteryName,
				Icon: lotteryIcon, Text: lotteryCommand(lotteryDefaultWinners) + " "})
		}
	}
	people := mentionPeople(data.Participants, exclude, d.mentions.query)
	if len(people) > mentionPanelShown {
		people = people[:mentionPanelShown]
	}
	if len(people) > 0 {
		divider()
		for _, p := range people {
			row := mentionRow{
				Key: mentionPersonKey(p), Label: "@" + p.Username, Person: p,
				Text: mentionAt(p.Username),
			}
			if p.IsAlias {
				row.Extra = "分身"
			}
			out = append(out, row)
		}
	}
	// 本地名单里没有这个人：先列搜到的用户，最后留一行「按输入」。
	var remote []mentionPerson
	if len(people) == 0 {
		remote = mentionRemotePeople(data.Search, exclude, people)
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
		if row, ok := mentionEchoRow(d.mentions.query, exclude); ok {
			divider()
			out = append(out, row)
		}
	}
	return out
}

// mentionListItems 把行翻成 Lobe List 的行：快捷行画图标、参与人画头像，都在
// 同一个 22 宽的槽里，两类行的文字因此对齐；分身标记走 Extra，不另造标签。
func (a *app) mentionListItems(c *ui.Context, rows []mentionRow) []display.ListItem {
	tk := lobetheme.TokensOf(c)
	out := make([]display.ListItem, 0, len(rows))
	for _, row := range rows {
		if row.Divider {
			out = append(out, display.ListItem{Divider: true})
			continue
		}
		item := display.ListItem{Key: row.Key, Label: row.Label, Extra: row.Extra}
		if row.Person.Username != "" {
			a.prefetchAvatars(row.Person.Avatar)
			item.Avatar = func() {
				avatarView(c, row.Person.Username, a.avatarBitmap(row.Person.Avatar), 22)
			}
		} else {
			item.Avatar = func() {
				ui.Box(c).Size(22, 22).Center().Children(func() {
					ui.Icon(c, row.Icon).Size(14, 14).TextColor(tk.TextTertiary)
				})
			}
		}
		out = append(out, item)
	}
	return out
}

// mentionActiveKey 是键盘上下键当前指着的那一行：面板里高亮它，回车选的就是
// 它。快捷行与「按输入」不参与键盘循环——打了 "@ali" 还让回车先撞上 @所有人
// 会很意外；候选顺序与 mentionCandidates 完全一致，两处各筛各的会错位。
func (a *app) mentionActiveKey(d *detailState) string {
	st := &d.mentions
	if !st.typing || !st.open {
		return ""
	}
	people := a.mentionPicks(d)
	if st.active < 0 || st.active >= len(people) {
		return ""
	}
	return mentionPersonKey(people[st.active])
}

// mentionRowsHeight 估出行集合的高度，用来决定向上还是向下展开。
func mentionRowsHeight(rows []mentionRow) float32 {
	h, n := float32(0), 0
	for _, r := range rows {
		if r.Divider {
			h += mentionDividerH
			continue
		}
		if n > 0 {
			h += mentionRowGap
		}
		h += mentionRowH
		n++
	}
	return h
}

// panelWidth 按最长的一行算面板宽度：宽度由内容撑开，名单里名字都短时面板
// 就窄，不会留出一条空白的右边；只兜住「太窄」与「长到离谱」两端。行集合
// （名单 + 搜索词）没变时复用上一次的量法，不每帧整形文字。
func (st *mentionState) panelWidth(rows []mentionRow) float32 {
	var key strings.Builder
	for _, r := range rows {
		key.WriteString(r.Label)
		key.WriteByte('\t')
		key.WriteString(r.Extra)
		key.WriteByte('\n')
	}
	if k := key.String(); k == st.sizeKey && k != "" {
		return st.sizeW
	}
	labels := make([]string, 0, len(rows))
	extras := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Divider {
			continue
		}
		labels = append(labels, r.Label)
		if r.Extra != "" {
			extras = append(extras, r.Extra)
		}
	}
	w := mentionRowChrome + mentionTextWidth(labels, mentionFontSize)
	if len(extras) > 0 {
		w += mentionExtraGap + mentionTextWidth(extras, mentionExtraSize)
	}
	// 多留 2 DIP：排版宽度是小数，贴着算出来的宽度画会把最后一笔压掉。
	w = min(max(w+2, mentionPanelMinW), mentionPanelMaxW)
	st.sizeKey, st.sizeW = key.String(), w
	return w
}

// mentionTextWidth 量一组单行文字里最宽的那一行，宽度零表示只按换行断行。
func mentionTextWidth(lines []string, size float32) float32 {
	if len(lines) == 0 {
		return 0
	}
	w, _ := ui.ShapeText(strings.Join(lines, "\n"), ui.Font{Size: size}, 0).Size()
	return w
}

// insertMention 把拼好的提及写进正文。键入 @ 打开的面板不是「插入」而是
// 「整块替换」：从 @ 起、到这段用户名的末尾（连带后面那个空格）全换成
// "@名字 "。光标停在名字中间挑候选也换整块——只换光标前那一段会留下
// "@Jimmyimmy" 这样的残字。
func (a *app) insertMention(d *detailState, text string) {
	if text == "" {
		return
	}
	st := &d.mentions
	if st.typing && st.tokenStart >= 0 {
		start, end := mentionWordRange(d.draft, st.tokenStart)
		d.mdEditor.replaceMentionToken(&d.draft, start, max(end, st.tokenEnd), text)
	} else {
		d.mdEditor.insertText(&d.draft, text)
	}
	d.preview = 0
	st.typing = false
	st.open = false
	st.query = ""
	st.tokenStart, st.tokenEnd = -1, -1
	st.active = 0
	st.resetSearch()
}

// mentionDeleteBlock 让写完的 @名字 像一个整体：光标落在名字里任何一个位置
// 按退格 / Delete，一次清掉整块（含后面那个空格），而不是一个字一个字往回删。
// k.Text() 是按键到来时输入控件里最新的文本（可能比绑定值还新），光标取
// 上一帧提交到状态的选区；命中整块后用 k.SetText 同步回输入控件。
func (a *app) mentionDeleteBlock(d *detailState, k ui.TextKey, backspace bool) bool {
	if d.mdEditor.selStart != d.mdEditor.selEnd {
		return false // 有选区时交回输入控件正常处理
	}
	text := k.Text()
	rs := []rune(text)
	caret := d.mdEditor.selEnd
	if caret < 0 || caret > len(rs) {
		caret = len(rs)
	}
	blocks := d.mentions.blocksFor(text)
	var hit *mentionBlock
	for i := range blocks {
		b := blocks[i]
		if backspace {
			// 光标在块内任意位置（含尾随空格之后）：整块删掉。块首按退格
			// 是删前一个字，不算命中。
			if caret > b.Start && caret <= b.End {
				hit = &blocks[i]
				break
			}
		} else if caret >= b.Start && caret < b.End {
			hit = &blocks[i]
			break
		}
	}
	if hit == nil {
		return false
	}
	out := string(rs[:hit.Start]) + string(rs[hit.End:])
	at := hit.Start
	d.draft = out
	d.mdEditor.pendingSel = composeSelection{ok: true, start: at, end: at}
	d.mdEditor.selStart, d.mdEditor.selEnd = at, at
	k.SetText(out)
	return true
}

// mentionTextKey 让键入 @ 弹出的候选可以纯键盘操作：上下键换人，回车 / Tab
// 采纳，Escape 收起。面板没由输入打开时不消费按键，Ctrl+Enter 发送照旧。
// OnTextKey 在帧间运行，Element 已失效，所以改正文必须走 k.SetText。
func (a *app) mentionTextKey(d *detailState, k ui.TextKey) bool {
	// 提及块整体删除：与面板是否打开无关。光标在 @名字 里任何位置按退格
	//（或 Delete），一次删掉整块，而不是一个字一个字退。
	if k.Key == ui.KeyBackspace || k.Key == ui.KeyDelete {
		return a.mentionDeleteBlock(d, k, k.Key == ui.KeyBackspace)
	}
	st := &d.mentions
	if !st.typing || !st.open {
		return false
	}
	switch k.Key {
	case ui.KeyUp, ui.KeyDown:
		cands := a.mentionCandidates(d)
		if len(cands) == 0 {
			return false
		}
		step := 1
		if k.Key == ui.KeyUp {
			step = -1
		}
		st.active = (st.active + step + len(cands)) % len(cands)
		return true
	case ui.KeyEnter, ui.KeyTab:
		cands := a.mentionCandidates(d)
		if st.active < 0 || st.active >= len(cands) {
			return false
		}
		a.insertMention(d, mentionAt(cands[st.active]))
		k.SetText(d.draft)
		return true
	case ui.KeyEscape:
		st.typing = false
		st.open = false
		st.query = ""
		st.tokenStart, st.tokenEnd = -1, -1
		st.resetSearch()
		return true
	}
	return false
}
