package main

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"
)

// 提及（@）能力的数据层：两个名单端点 + 正文拼串规则。
//
// @ 在 2Libra API 里只是正文纯文本，没有单独的提交端点（api-write.md
// 「提及（@）与抽奖」）。客户端要做的只有两件事：
//
//   - 取名单：GET /v1/meta/guardians（@随机守护者）与
//     GET /v1/topics/:short_id/participants（@所有人 / 逐个挑人）。
//   - 拼正文："@名字 "（后面必须补空格，否则服务端把整段当用户名，不发通知）。
//
// 拼串前必须排除自己（含临时换用的回复身份），单条内容最多 50 个 @ 会发
// 通知。名单缓存与失败退避都放在 detailState 的 mentionState 里：参与人名单
// 单独限流 20/30 次每分，打开编辑器取一次就够，不能每帧或每次按键重拉。
//
// 所有请求都走 Client.getJSON（即 proxyManager 的共享 transport 与限流闸门），
// 不在这里自建 http.Client。

const (
	// mentionNotifyLimit 是单条内容会产生通知的 @ 上限；第 51 个起仍留在
	// 正文里，但不发通知。拼串时按「还剩几个名额」截断。
	mentionNotifyLimit = 50
	// 名单变化很慢：守护者随人员变动自动生效，参与人名单取一次就能覆盖一次
	// 写评论。10 分钟后重开气泡才刷新，省掉限流。
	mentionCacheTTL = 10 * time.Minute
	// 失败后的退避：30 秒起，逐次翻倍到 4 分钟，避免每帧重发注定失败的请求。
	mentionRetry   = 30 * time.Second
	mentionTimeout = 12 * time.Second
)

// mentionPerson 是两份名单共用的最小字段。avatar_url 只用于参与人行的头像；
// 守护者名单没有这个字段，留空即只画首字母。
type mentionPerson struct {
	ID       string
	Username string
	Avatar   string
	IsAlias  bool
}

// mentionRoster 是名单响应：total 用来判断「@所有人」是否超过 50 人上限。
type mentionRoster struct {
	People []mentionPerson
	Total  int
}

// fetchGuardians 读守护者名单（scope read，可匿名）。路径与字段名照
// api-read.md「元信息」，列表字段是 items，不是 posts / list / 裸数组。
func (c *Client) fetchGuardians(ctx context.Context) (mentionRoster, error) {
	var out mentionRoster
	d, err := withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		return c.getJSON(ctx, c.apiURL("/v1/meta/guardians"), token)
	})
	if err != nil {
		return out, err
	}
	return decodeMentionRoster(d), nil
}

// fetchParticipants 读帖子参与人名单（scope read，可匿名；无权访问该帖时
// 403 permission_denied）。全量不分页，short_id 按路径段转义。
func (c *Client) fetchParticipants(ctx context.Context, shortID string) (mentionRoster, error) {
	var out mentionRoster
	d, err := withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		return c.getJSON(ctx, c.apiURL(topicPath(shortID)+"/participants"), token)
	})
	if err != nil {
		return out, err
	}
	return decodeMentionRoster(d), nil
}

// decodeMentionRoster 用容错读取解码两个名单端点：未知字段一律忽略，
// items 里的 id / username / avatar_url / is_alias 各取各的，缺字段不报错。
func decodeMentionRoster(d json.RawMessage) mentionRoster {
	o := asObject(d)
	if o == nil {
		return mentionRoster{}
	}
	out := mentionRoster{Total: o.num("total")}
	for _, raw := range rawArray(o["items"]) {
		row := asObject(raw)
		if row == nil {
			continue
		}
		name := strings.TrimSpace(row.str("username"))
		if name == "" {
			continue
		}
		out.People = append(out.People, mentionPerson{
			ID:       row.str("id"),
			Username: name,
			Avatar:   row.str("avatar_url"),
			IsAlias:  row.flag("is_alias"),
		})
	}
	if out.Total == 0 {
		out.Total = len(out.People)
	}
	return out
}

// ------------------------------------------------------------------
// 键入 @ 的自动补全
// ------------------------------------------------------------------

// mentionBlock 是一个已经写完的提及（@名字 + 尾随空格）在正文里的 rune
// 范围，[Start, End) 含尾随空格。编辑器用它给提及上色加粗，并用它实现
// 「退格一次删掉整块」——提交给服务端的仍是纯文本 "@名字 "。
type mentionBlock struct{ Start, End int }

// mentionBlocks 扫出正文里所有写完的提及。只认 @ 前是开头或空白、@ 后是一
// 段非空用户名、并且后面有空白的：正在输入的 @前缀 没有尾随空格，不会高亮，
// 也不会被当成一整块删掉（那会打断用户继续打字）。邮箱里的 @ 不算。
func mentionBlocks(body string) []mentionBlock {
	rs := []rune(body)
	var out []mentionBlock
	for i := 0; i < len(rs); i++ {
		if rs[i] != '@' {
			continue
		}
		if i > 0 && !unicode.IsSpace(rs[i-1]) {
			continue
		}
		j := i + 1
		for j < len(rs) && !unicode.IsSpace(rs[j]) {
			j++
		}
		if j == i+1 || j >= len(rs) {
			continue
		}
		out = append(out, mentionBlock{Start: i, End: j + 1})
		i = j
	}
	return out
}

// mentionWordRange 把 start 处的 @ 起、到这段用户名末尾（下一个空白为止）
// 扩成一整块，并连带吃掉后面那一个空格。
//
// 选中候选时要换掉的是整块，而不是「@ 到光标」：用户把光标放回 @jimmy 中间
// 的 j 后面再挑 Jimmy，只换光标前那一段会留下 "@Jimmyimmy"。
func mentionWordRange(body string, start int) (int, int) {
	rs := []rune(body)
	start = max(0, min(start, len(rs)))
	if start >= len(rs) || rs[start] != '@' {
		return start, start
	}
	end := start + 1
	for end < len(rs) && !unicode.IsSpace(rs[end]) {
		end++
	}
	// 后面已经有一个空格就一并吃掉：mentionAt 自己会补一个，不然会出现两个。
	if end < len(rs) && (rs[end] == ' ' || rs[end] == '\t') {
		end++
	}
	return start, end
}

// mentionTokenAt 找光标处正在输入的 @前缀，返回它在正文里的 rune 范围。
// 口径与服务端一致：@ 前是开头或空白、@ 后直到空白是一整个用户名；
// 邮箱里的 @ 不算。光标不在 @ 后面（已输入空格、或 @ 在词中）时返回 false。
func mentionTokenAt(body string, caret int) (int, int, bool) {
	rs := []rune(body)
	if caret < 0 || caret > len(rs) {
		return 0, 0, false
	}
	i := caret - 1
	for i >= 0 && !unicode.IsSpace(rs[i]) {
		i--
	}
	start := i + 1
	if start >= caret || rs[start] != '@' {
		return 0, 0, false
	}
	if start > 0 && !unicode.IsSpace(rs[start-1]) {
		return 0, 0, false // name@example.com
	}
	return start, caret, true
}

// onMentionInput 是评论输入框的 OnChange：正文里一出现 @前缀，就把候选面板
// 打开、按前缀过滤；输入空格或删掉 @ 后自动收起。手动点工具条图标打开的
// 面板 typing 为 false，不受正文输入影响。
func (a *app) onMentionInput(d *detailState, host replySurface, caret int) {
	st := &d.mentions
	start, end, ok := mentionTokenAt(d.draft, caret)
	if !ok {
		if st.typing {
			st.typing = false
			st.open = false
			st.tokenStart, st.tokenEnd = -1, -1
			st.query = ""
			st.active = 0
		}
		return
	}
	st.typing = true
	st.open = true
	st.host = host
	st.tokenStart, st.tokenEnd = start, end
	st.query = string([]rune(d.draft)[start+1 : end])
	st.active = 0
	a.ensureMentionGuardians(d)
	a.ensureMentionParticipants(d)
}

// mentionPeople 是面板里的逐人列表与键盘上下键共用的口径：排掉自己（含临时
// 换用的回复身份）、按当前 @前缀 过滤、按用户名去重，顺序与名单一致。
//
// 两处必须是同一份：键盘高亮的那一行就是回车会选的那一个，各筛各的会错位。
func mentionPeople(people []mentionPerson, exclude map[string]bool, query string) []mentionPerson {
	q := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(query, "@")))
	out := make([]mentionPerson, 0, len(people))
	seen := make(map[string]bool, len(people))
	for _, p := range people {
		key := mentionKey(p.Username)
		if key == "" || exclude[key] || seen[key] {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(p.Username), q) {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

// syncMentionToken 每帧按当前光标复核一次正在输入的 @前缀：光标移出这个词
// （点别处、方向键走到词外）就收起面板，移到词中间就按光标前那几个字母重新
// 过滤。只看 OnChange 的话，光标动了而正文没变，面板会停在旧位置、按旧前缀
// 过滤，看着就不跟手。
func (a *app) syncMentionToken(d *detailState) {
	st := &d.mentions
	if !st.typing || !st.open {
		return
	}
	start, end, ok := mentionTokenAt(d.draft, d.mdEditor.selEnd)
	if !ok {
		st.typing = false
		st.open = false
		st.tokenStart, st.tokenEnd = -1, -1
		st.query = ""
		st.active = 0
		return
	}
	if start == st.tokenStart && end == st.tokenEnd {
		return
	}
	st.tokenStart, st.tokenEnd = start, end
	st.query = string([]rune(d.draft)[start+1 : end])
	st.active = 0
}

// mentionCandidates 是键盘上下键 / 回车要用的候选用户名。与面板逐人列表同一
// 份口径（见 mentionPeople），因此下标就是列表里的顺序。
func (a *app) mentionCandidates(d *detailState) []string {
	data := a.mentionData(d, d.draft)
	people := mentionPeople(data.Participants, a.mentionExclude(d), d.mentions.query)
	out := make([]string, 0, len(people))
	for _, p := range people {
		out = append(out, p.Username)
	}
	return out
}

// ------------------------------------------------------------------
// 拼串：服务端只认正文里的纯文本 @
// ------------------------------------------------------------------

// mentionKey 把用户名归一成比较用的键：去首尾空白与可能的前导 @，忽略大小写。
// 服务端同样忽略用户名大小写，本地排除自己时必须用同一口径。
func mentionKey(name string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "@"))
}

// mentionAt 拼出一个能被服务端识别的提及：@ 名字后必须有一个空白字符，
// 否则「@jimmy，你好」会被整体当成用户名，找不到人也不发通知。
func mentionAt(name string) string {
	name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "@"))
	if name == "" {
		return ""
	}
	return "@" + name + " "
}

// countMentions 数正文里能产生通知的 @ 个数。口径与服务端一致的一部分：
// @ 前是开头或空白、后跟非空白（取到下一个空白为止为一整个用户名）；
// 邮箱里的 @ 不算。带标点的写法客户端按保守口径计入名额，宁可少 @ 一个，
// 也不要第 51 个起静默不发通知。高亮与是否通知是两套口径，这里只估名额。
func countMentions(body string) int {
	rs := []rune(body)
	n := 0
	for i, r := range rs {
		if r != '@' {
			continue
		}
		if i > 0 && !unicode.IsSpace(rs[i-1]) {
			continue // name@example.com 以及夹在词里的 @
		}
		if i+1 >= len(rs) || unicode.IsSpace(rs[i+1]) {
			continue
		}
		n++
	}
	return n
}

// mentionSlots 返回这条草稿还能放几个会发通知的 @。拼「@所有人」时按它
// 截断：草稿里已有的 @ 也算进 50 个名额。
func mentionSlots(draft string) int {
	return max(0, mentionNotifyLimit-countMentions(draft))
}

// buildMentions 把名单拼成 "@a @b @c "，按 mentionKey 去重、排除自己、
// 最多用 slots 个名额。返回空串表示一个可 @ 的人都没有（或名额已满）。
func buildMentions(people []mentionPerson, exclude map[string]bool, slots int) string {
	if slots <= 0 {
		return ""
	}
	var sb strings.Builder
	seen := make(map[string]bool, len(people))
	used := 0
	for _, p := range people {
		key := mentionKey(p.Username)
		if key == "" || exclude[key] || seen[key] {
			continue
		}
		seen[key] = true
		at := mentionAt(p.Username)
		if at == "" {
			continue
		}
		sb.WriteString(at)
		used++
		if used >= slots {
			break
		}
	}
	return sb.String()
}

// pickRandomMention 从名单里随机挑一个可 @ 的 username。所有候选都被排除时
// 返回空串，界面据此隐藏「@随机守护者」入口，而不是点一下什么也不发生。
func pickRandomMention(people []mentionPerson, exclude map[string]bool) string {
	candidates := make([]string, 0, len(people))
	seen := make(map[string]bool, len(people))
	for _, p := range people {
		key := mentionKey(p.Username)
		if key == "" || exclude[key] || seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, p.Username)
	}
	if len(candidates) == 0 {
		return ""
	}
	return candidates[rand.Intn(len(candidates))]
}

// ------------------------------------------------------------------
// 列表状态：缓存、退避、加载
// ------------------------------------------------------------------

// mentionState 是每篇详情自己的名单状态。它只活在 detailState 上，不存
// ui.Element / *ui.Context；后台结果通过 a.publish 回到 UI 线程再写进来。
// open / host 让气泡只在点开它的那扇窗口里画（与回复身份气泡同一套做法）。
type mentionState struct {
	guardians       []mentionPerson
	guardiansBusy   bool
	guardiansLoaded bool
	guardiansAt     time.Time
	guardiansErr    string

	participants      []mentionPerson
	participantsTotal int
	// participantsKey 是「帖子 + 回复身份」：换账号回复时可见性与自己是谁
	// 都会变，不能沿用上一个身份拿到的名单。
	participantsKey    string
	participantsBusy   bool
	participantsLoaded bool
	participantsAt     time.Time
	participantsDenied bool // 无权访问该帖：403 permission_denied
	participantsErr    string
	triedGuardians     time.Time
	triedParticipants  time.Time
	failGuardians      int
	failParticipants   int

	open  bool
	host  replySurface
	query string
	// typing 表示面板是被正文里正在输入的 @ 打开的（而不是点了工具条图标）。
	// tokenStart / tokenEnd 是这次 @前缀 在正文里的 rune 范围，选中候选时
	// 整段替换成 @名字 ；输入结束（空格 / 删掉 @）就自动收起面板。
	typing     bool
	tokenStart int
	tokenEnd   int
	// active 是键盘上下键选中的候选下标，鼠标点击不依赖它。
	active int
	// 光标定位要重新排版草稿；正文或宽度没变时复用，避免长草稿每帧整形。
	shapeText  string
	shapeWidth float32
	shape      *ui.TextLayout
	// blocksText / blocks 缓存这份正文扫出来的提及块：编辑器每帧要按它给提及
	// 上色，按键时要按它判断是否命中整块，同一份正文不必扫两遍。
	blocksText  string
	blocksValid bool
	blocks      []mentionBlock
	// sizeKey / sizeW 缓存按行内容量出来的面板宽度，行集合不变时不重复整形。
	sizeKey string
	sizeW   float32
}

// blocksFor 返回这份正文里的提及块，同一份正文只扫一次。
func (st *mentionState) blocksFor(text string) []mentionBlock {
	if !st.blocksValid || st.blocksText != text {
		st.blocks = mentionBlocks(text)
		st.blocksText = text
		st.blocksValid = true
	}
	return st.blocks
}

func mentionRetryAfter(failures int) time.Duration {
	delay := mentionRetry
	for i := 1; i < failures && delay < 4*mentionRetry; i++ {
		delay *= 2
	}
	return delay
}

// mentionClient 用这次回复真正会用的身份取名单：换了临时回复账号时，
// 参与人名单的可见性（403）和自己要排除的名字都按那个账号算。
func (a *app) mentionClient(d *detailState) (*Client, string) {
	identity := a.replyIdentityFor(d)
	return a.replyClientFor(identity), identity.ID
}

// mentionExclude 是需要从名单里排除的名字：窗口自己的账号、临时换用的回复
// 账号。当前版本没有分身身份选择，所以没有「当前分身名」可排除；以后接了
// 分身发言，只要把选中的分身名加进这个 map，@所有人 / @随机守护者会自动跟上。
func (a *app) mentionExclude(d *detailState) map[string]bool {
	out := map[string]bool{}
	for _, name := range []string{a.currentReplyName(), a.replyIdentityFor(d).Name} {
		if key := mentionKey(name); key != "" {
			out[key] = true
		}
	}
	return out
}

// ensureMentionGuardians 首次打开气泡或缓存过期时取一次守护者名单。
func (a *app) ensureMentionGuardians(d *detailState) {
	st := &d.mentions
	if st.guardiansBusy || !a.autoLoad || a.client == nil {
		return
	}
	now := time.Now()
	if st.guardiansLoaded {
		if now.Sub(st.guardiansAt) < mentionCacheTTL {
			return
		}
	} else if !st.triedGuardians.IsZero() &&
		now.Sub(st.triedGuardians) < mentionRetryAfter(st.failGuardians) {
		return
	}
	client, _ := a.mentionClient(d)
	st.guardiansBusy = true
	st.triedGuardians = now
	ctx, cancel := context.WithTimeout(context.Background(), mentionTimeout)
	go func() {
		defer cancel()
		roster, err := client.fetchGuardians(ctx)
		a.publish(func() {
			if !a.detailLive(d) {
				return
			}
			st.guardiansBusy = false
			if err != nil {
				st.failGuardians++
				if a.handleAuthErrorFor(a.replyAuthTarget(), err, func() { a.reloadMentionGuardians(d) }) {
					return
				}
				st.guardiansErr = friendlyError(err)
				return
			}
			st.failGuardians = 0
			st.guardiansErr = ""
			st.guardiansLoaded = true
			st.guardiansAt = time.Now()
			st.guardians = roster.People
		})
	}()
}

// ensureMentionParticipants 取当前帖子的参与人名单。同一次写评论只取一次：
// 换帖子、换回复身份或缓存过期才重拉，避免撞上该端点单独的每分限流。
func (a *app) ensureMentionParticipants(d *detailState) {
	st := &d.mentions
	if st.participantsBusy || !a.autoLoad || a.client == nil || d.shortID == "" {
		return
	}
	client, identityID := a.mentionClient(d)
	key := d.shortID + "|" + identityID
	if st.participantsLoaded && st.participantsKey == key &&
		time.Since(st.participantsAt) < mentionCacheTTL {
		return
	}
	if st.participantsKey == key && !st.participantsDenied &&
		!st.triedParticipants.IsZero() &&
		time.Since(st.triedParticipants) < mentionRetryAfter(st.failParticipants) {
		return
	}
	st.participantsBusy = true
	st.triedParticipants = time.Now()
	st.participantsKey = key
	st.participantsDenied = false
	st.participantsErr = ""
	ctx, cancel := context.WithTimeout(context.Background(), mentionTimeout)
	go func() {
		defer cancel()
		roster, err := client.fetchParticipants(ctx, d.shortID)
		a.publish(func() {
			if !a.detailLive(d) {
				return
			}
			st.participantsBusy = false
			if err != nil {
				// 无权访问这篇帖子：服务端明确 403 permission_denied，
				// 不要重试、也不要弹授权框（那不是 scope 问题）。
				if isPermissionDenied(err) {
					st.participantsDenied = true
					st.participantsErr = "你没有权限查看这篇帖子的参与人"
					return
				}
				st.failParticipants++
				if a.handleAuthErrorFor(a.replyAuthTarget(), err, func() { a.reloadMentionParticipants(d) }) {
					return
				}
				st.participantsErr = friendlyError(err)
				return
			}
			st.failParticipants = 0
			st.participantsErr = ""
			st.participantsLoaded = true
			st.participantsAt = time.Now()
			st.participants = roster.People
			st.participantsTotal = roster.Total
		})
	}()
}

// isPermissionDenied 只认机器可读的 code，不看 m 的中文文案。
func isPermissionDenied(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code == "permission_denied"
}

// reload* 丢掉失败状态重来一次（气泡里的「重试」与授权成功后的重放）。
func (a *app) reloadMentionGuardians(d *detailState) {
	st := &d.mentions
	if st.guardiansBusy {
		return
	}
	st.triedGuardians = time.Time{}
	st.guardiansErr = ""
	a.ensureMentionGuardians(d)
}

func (a *app) reloadMentionParticipants(d *detailState) {
	st := &d.mentions
	if st.participantsBusy {
		return
	}
	st.triedParticipants = time.Time{}
	st.participantsDenied = false
	st.participantsErr = ""
	a.ensureMentionParticipants(d)
}

// mentionResult 汇总面板要用的两份名单与拼串名额，避免界面里到处判空。
type mentionResult struct {
	Guardians     []mentionPerson
	Participants  []mentionPerson
	Total         int
	Slots         int
	GuardiansBusy bool
	PeopleBusy    bool
	GuardiansErr  string
	PeopleErr     string
	PeopleDenied  bool
}

// mentionData 读当前状态。缓存与请求状态都由 ensure* 在 UI 线程维护，
// 这里只读不算，视图每帧调用也不会改状态。
func (a *app) mentionData(d *detailState, draft string) mentionResult {
	st := &d.mentions
	return mentionResult{
		Guardians:     st.guardians,
		Participants:  st.participants,
		Total:         st.participantsTotal,
		Slots:         mentionSlots(draft),
		GuardiansBusy: st.guardiansBusy,
		PeopleBusy:    st.participantsBusy,
		GuardiansErr:  st.guardiansErr,
		PeopleErr:     st.participantsErr,
		PeopleDenied:  st.participantsDenied,
	}
}
