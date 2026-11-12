package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// 「发帖」窗口的数据、校验与请求。
//
// 端点与字段名逐字照抄 openapi v1 的 api-write.md：
//
//	GET  /v1/nodes      节点列表（顶层自带 children；?parent=<slug> 取子节点）
//	POST /v1/topics 🔑  发帖（必须带 Idempotency-Key，成功返回 201）
//
// 发帖的可选块按文档的字段名原样组装：vote（投票）、score（评分）、
// bet（竞猜）、redeem_codes（兑换码 / 红包）、reward_pool（金币池）与
// access_guard_*（可见性门槛）。文档明确「随时可以新增字段」，所以解码
// 一律忽略不认识的字段，不做严格结构体绑定。
//
// 付费墙的隐藏内容（restricted_*）没有做：文档只写了「付费 / D20 检定 /
// 竞拍」这些中文说法，没给 restricted_access_type 的机器可读取值，猜一个
// 值发出去只会换来 400，所以宁可缺这一块也不写死。
const (
	composeTitleMax       = 500
	composeContentMax     = 20000
	composeTitlePrefixMax = 100
	composeTipMax         = 500
	composeContactMax     = 500
	composeStatusMax      = 50

	composeVoteTitleMax  = 16
	composeOptionMax     = 20 // vote.options 上限
	composeOptionTextMax = 100

	composeScoreTargetMax  = 255
	composeScoreCommentMax = 500

	composeBetTitleMax     = 255
	composeBetOptionsMax   = 6
	composeBetOptionMax    = 255
	composeBetOptionsMin   = 2
	composeCodeMax         = 500 // redeem_codes 最多 500 个
	composeCodeTextMax     = 200
	composeCodeDescMax     = 2000
	composeRedeemPerUserLo = 1
	composeRedeemPerUserHi = 100

	composePoolCoinsLo = 1
	composePoolCoinsHi = 1000000
	composePoolProbLo  = 0.1
	composePoolProbHi  = 0.5

	composeGuardLevelLo = 1
	composeGuardLevelHi = 100
	composeGuardAttrHi  = 10
)

// composeNode 是一个可发帖的节点。发帖必须用 uuid（node_id），不是 slug；
// 级联选择按 id 记选中项，显示时才换成名字。
type composeNode struct {
	ID         string
	Slug       string
	Name       string
	ParentSlug string
	ParentName string
	PostCount  int
	// Label 只在显示名撞车时使用（同一个父节点下有两个同名节点，例如 AI 下的
	// 两个「中转站」）：补上 slug 才能在两列级联选择里区分。正常情况下为空，
	// nameDisplay() 现算。
	Label string
}

// parentDisplay 是「父节点 · 子节点」的形式，子节点才带父名，避免两个同名
// 节点分不清。
func (n composeNode) parentDisplay() string {
	if n.ParentName != "" && n.ParentName != n.Name {
		return n.ParentName + " · " + n.Name
	}
	return n.Name
}

// nameDisplay 是级联选择第二列里的一行。父节点已经在第一列展开，所以只
// 显示这一级自己的名字；同名兄弟补上 slug，否则两行看起来一模一样。
func (n composeNode) nameDisplay() string {
	if n.Label != "" {
		return n.Name + " (" + n.Slug + ")"
	}
	return n.Name
}

// createdTopic 是 POST /v1/topics 之后界面需要的东西：short_id 用来在应用内
// 打开刚发的帖子，URL 用来在浏览器里看原文。
type createdTopic struct {
	ShortID string
	URL     string
}

// 可见性门槛的取值。空串表示不限制（不发送 access_guard_* 字段）。
const (
	guardNone      = ""
	guardLogin     = "login"
	guardLevel     = "level"
	guardAttribute = "attribute"
)

// guardTypeLabels 是下拉框里的顺序与文案，顺序与 guardTypeValues 一一对应。
// 下拉框只能绑定字符串，所以界面里存的是标签，发请求前再换回机器取值。
var guardTypeLabels = []string{"所有人可见", "登录用户可见", "达到等级可见", "满足属性可见"}

var guardTypeValues = []string{guardNone, guardLogin, guardLevel, guardAttribute}

func guardFromLabel(label string) string {
	for i, l := range guardTypeLabels {
		if l == label {
			return guardTypeValues[i]
		}
	}
	return guardNone
}

// voteTypeValues 是投票类型：文档的取值只有 pk（单选）与 multiple（多选）。
// 两项用分段控件切换，所以状态里存的是下标。
var voteTypeValues = []string{"pk", "multiple"}

func voteTypeAt(i int) string {
	if i < 0 || i >= len(voteTypeValues) {
		return voteTypeValues[0]
	}
	return voteTypeValues[i]
}

// redPacketAmounts 是 max_red_packet_amount 允许的取值，文档只给了这六个数。
var redPacketAmounts = []float64{88, 188, 288, 388, 488, 588}

// redPacketLabels 是分段控件上的文案，与 redPacketAmounts 一一对应。
var redPacketLabels = []string{"88", "188", "288", "388", "488", "588"}

// redPacketAt 按分段控件的下标取金额；越界时退回允许的最小值。
func redPacketAt(i int) float64 {
	if i < 0 || i >= len(redPacketAmounts) {
		return redPacketAmounts[0]
	}
	return redPacketAmounts[i]
}

// composeSelection 是正文输入框里的一段选区，按 rune 计。ok 为真表示插入
// Markdown 标记后要把光标 / 选区恢复到这里。
type composeSelection struct {
	ok         bool
	start, end int
}

// composeState 是「发帖」窗口的全部界面状态。只在 UI 线程读写；后台请求
// 完成后通过 publish 回填，并用 seq 丢弃过期结果。
type composeState struct {
	// 节点级联选择
	nodes       []composeNode
	nodesAt     time.Time
	nodeSeq     uint64
	nodeLoading bool
	nodeLoaded  bool
	nodeErr     string
	// nodeFailedAt 是上一次刷新失败的时间：失败后按 nodeRetryCooldown 冷却，
	// 否则视图每帧重建都会再发一次注定失败的请求（「重试」按钮会清掉它）。
	nodeFailedAt time.Time
	// nodePick 是第一列的展开状态；nodeID 是选中的节点 uuid，提交时直接用。
	nodePick cascaderState
	nodeID   string

	// 基础字段
	title       string
	body        string
	titleField  ui.Handle
	bodyField   ui.Handle
	focusTitle  bool
	titlePrefix string
	tip         string
	contact     string
	status      string
	anonymous   bool

	// 正文编辑器的选区与工具条状态。sel* 是上一帧构建输入框时读到的选区，
	// 工具条按钮据此决定把 Markdown 标记插在哪里；pendingSel 是刚插完标记
	// 之后希望恢复的选区，在同一个构建帧里交给输入框。
	selStart, selEnd int
	pendingSel       composeSelection
	// preview 是正文区的视图：0 编辑，1 预览。
	preview int

	// 投票块
	voteOpen          bool
	voteTypeIndex     int // 0 单选（pk），1 多选（multiple）
	voteTitle         string
	voteOptions       []string
	voteResultVisible bool
	voteDeadline      string
	voteMaxChoices    float64

	// 评分块
	scoreOpen    bool
	scoreTarget  string
	scoreValue   float64
	scoreComment string

	// 竞猜块
	betOpen     bool
	betTitle    string
	betOptions  []string
	betDeadline string

	// 兑换码 / 红包块
	redeemOpen       bool
	redeemCodes      string // 一行一个
	codeDescription  string
	maxRedeemPerUser float64
	redPacket        bool
	redPacketIndex   int // redPacketAmounts 的下标

	// 金币池块
	poolOpen        bool
	poolCoins       float64
	poolProbability float64

	// 可见性门槛：界面里存下拉框的标签，payload 时换成 access_guard_type。
	guardOpen      bool
	guardChoice    string
	guardLevel     float64
	guardAttr      string
	guardAttrValue float64

	// 提交状态
	posting bool
	err     string
	// okTitle / okShortID / okURL 是发表成功后的回执：表单已经清空，这一行
	// 留在窗口里让作者立刻打开刚发的帖子。
	okTitle   string
	okShortID string
	okURL     string
	// key 是这一次提交的幂等键。网络超时后用同一个键重试不会重复发帖；
	// 发表成功后清掉，下一次发帖换新的。keySig 是铸键时那份请求体的摘要：
	// 同一个键配不同的请求体会被服务端判成 idempotency_key_reuse（409），
	// 所以用户一改内容就必须换键。
	key    string
	keySig string
}

// newComposeState 造一份空表单：默认可见性为「所有人可见」、投票单选、
// 各数量字段取文档允许的最小值，避免一打开就显示 0 这种必然被服务端拒绝的值。
func newComposeState() *composeState {
	return &composeState{
		voteOptions:      []string{"", ""},
		voteMaxChoices:   1,
		betOptions:       []string{"", ""},
		maxRedeemPerUser: composeRedeemPerUserLo,
		poolCoins:        100,
		guardChoice:      guardTypeLabels[0],
		guardLevel:       composeGuardLevelLo,
	}
}

// ------------------------------------------------------------------
// 节点列表（请求与本地缓存在 nodes.go）
// ------------------------------------------------------------------

// ensureComposeNodes 是受保护的任务：先把本地缓存里的节点摆进级联选择（内存
// 或磁盘，哪怕过期），再按新鲜度决定要不要刷新；正在加载时不重复发请求，
// 失败时保留上一次的结果。
func (a *app) ensureComposeNodes() {
	st := a.compose
	if st == nil || st.nodeLoading {
		return
	}
	// 1) 缓存优先：首帧就有可选项，不用先看一屏「正在读取节点…」。
	if len(st.nodes) == 0 {
		if nodes, at, ok := a.nodeSnapshot(); ok {
			st.nodes, st.nodesAt, st.nodeLoaded = nodes, at, true
			if st.selectedNode() == nil {
				st.nodeID = defaultComposeNode(nodes)
			}
		}
	}
	// 2) 新鲜期内直接用，不再请求。
	if len(st.nodes) > 0 && nodeCacheFresh(st.nodesAt) {
		return
	}
	// 3) 刚失败过就先等一下，别对着一个失败的服务端每帧重试。
	if !st.nodeFailedAt.IsZero() && time.Since(st.nodeFailedAt) < nodeRetryCooldown {
		return
	}
	if !a.autoLoad && len(st.nodes) == 0 {
		return
	}
	st.nodeLoading = true
	st.nodeErr = ""
	st.nodeSeq++
	gen := st.nodeSeq
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		nodes, err := a.client.loadNodeTree(ctx)
		a.publish(func() {
			if err == nil {
				// 窗口已经关掉也把结果留下来，下次打开直接命中缓存。
				a.storeNodeCache(nodes, time.Now())
			}
			if st != a.compose || st.nodeSeq != gen {
				return
			}
			st.nodeLoading = false
			if err != nil {
				// 已经有上一次的列表就继续用，只把原因记下来；冷却从这里开始。
				st.nodeFailedAt = time.Now()
				if len(st.nodes) == 0 {
					st.nodeErr = friendlyError(err)
				}
				a.notifyNodesLoaded(err)
				return
			}
			st.nodeErr = ""
			st.nodeFailedAt = time.Time{}
			st.nodes = nodes
			st.nodeLoaded = true
			st.nodesAt = a.nodes.at
			if st.selectedNode() == nil {
				st.nodeID = defaultComposeNode(nodes)
			}
			a.notifyNodesLoaded(nil)
		})
	}()
}

// notifyNodesLoaded 触发节点加载完成的测试钩子。
func (a *app) notifyNodesLoaded(err error) {
	if a.onNodesLoaded != nil {
		a.onNodesLoaded(err)
	}
}

// hasAuthScope 报告当前令牌是否已经带上某个权限范围。发帖窗口据此在底部
// 提前提示「还需要 write:topic」，而不是等写完点发布才被服务端拦下来。
func (a *app) hasAuthScope(want string) bool {
	for _, s := range a.auth.scopes() {
		if s == want {
			return true
		}
	}
	return false
}

// defaultComposeNode 是首次打开时的默认节点：优先「社区讨论」，其次第一个。
// 返回 uuid，提交与级联选择都用它。
func defaultComposeNode(nodes []composeNode) string {
	for _, n := range nodes {
		if n.ID == communityNodeID {
			return n.ID
		}
	}
	if len(nodes) > 0 {
		return nodes[0].ID
	}
	return ""
}

// selectedNode 返回级联选择当前选中的节点；没选中时为 nil。
func (st *composeState) selectedNode() *composeNode {
	if st == nil || st.nodeID == "" {
		return nil
	}
	for i := range st.nodes {
		if st.nodes[i].ID == st.nodeID {
			return &st.nodes[i]
		}
	}
	return nil
}

// nodeCascaderOptions 把扁平的节点表拼成级联选择的两列：第一列是顶层
// 分类，第二列是它下面的节点。顶层分类本身只是分类，不单独作为可选项；
// 没有子节点的分类是叶子，点第一列就会直接选中。
func nodeCascaderOptions(nodes []composeNode) []cascaderOption {
	roots := make([]composeNode, 0, len(nodes))
	for _, n := range nodes {
		if n.ParentSlug == "" {
			roots = append(roots, n)
		}
	}
	// 数据异常（全是子节点、没有顶层）时退回一份平铺列表，至少还能选。
	if len(roots) == 0 {
		roots = nodes
	}
	out := make([]cascaderOption, 0, len(roots))
	for _, root := range roots {
		opt := cascaderOption{Value: root.ID, Label: root.nameDisplay()}
		if root.ParentSlug == "" && root.Slug != "" {
			for _, n := range nodes {
				if n.ID != root.ID && n.ParentSlug == root.Slug {
					opt.Children = append(opt.Children, cascaderOption{
						Value:  n.ID,
						Label:  n.nameDisplay(),
						Detail: nodePostCount(n.PostCount),
					})
				}
			}
		}
		// 有子节点的顶层分类只是分类，post_count 是下面所有子节点的合计，
		// 显示在分类上会让人以为能直接发到这里；只有叶子才显示帖子数。
		if len(opt.Children) == 0 {
			opt.Detail = nodePostCount(root.PostCount)
		}
		out = append(out, opt)
	}
	return out
}

// nodePostCount 是级联选择里的帖子数说明；0 不显示。
func nodePostCount(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// ------------------------------------------------------------------
// 正文的 Markdown 工具条
// ------------------------------------------------------------------

// composeFormat 是正文工具条上的一个 Markdown 动作。
type composeFormat int

const (
	composeBold composeFormat = iota
	composeItalic
	composeStrike
	composeInlineCode
	composeHeading
	composeQuote
	composeCodeBlock
	composeBulletList
	composeOrderedList
	composeLink
	composeImage
	composeDivider
)

// applyMarkdown 把一种 Markdown 标记写进正文。选区来自上一帧构建输入框时
// 读到的值（selStart / selEnd），写入后把希望恢复的选区记在 pendingSel，
// 由同一个构建帧里的输入框执行，用户不用再手动把光标挪回去。
func (st *composeState) applyMarkdown(f composeFormat) {
	switch f {
	case composeBold:
		st.wrapSelection("**", "**", "加粗文字")
	case composeItalic:
		st.wrapSelection("*", "*", "斜体文字")
	case composeStrike:
		st.wrapSelection("~~", "~~", "删除线")
	case composeInlineCode:
		st.wrapSelection("`", "`", "行内代码")
	case composeHeading:
		st.prefixLines("## ", false)
	case composeQuote:
		st.prefixLines("> ", false)
	case composeBulletList:
		st.prefixLines("- ", false)
	case composeOrderedList:
		st.prefixLines("", true)
	case composeCodeBlock:
		st.wrapSelection("```\n", "\n```", "代码")
	case composeLink:
		st.wrapSelection("[", "](url)", "链接文字")
	case composeImage:
		st.wrapSelection("![", "](url)", "图片说明")
	case composeDivider:
		st.insertText("\n\n---\n\n")
	}
}

// runeSel 把选区夹到正文范围内并归一顺序。
func (st *composeState) runeSel() ([]rune, int, int) {
	rs := []rune(st.body)
	a, b := st.selStart, st.selEnd
	if a > b {
		a, b = b, a
	}
	a = max(0, min(a, len(rs)))
	b = max(0, min(b, len(rs)))
	return rs, a, b
}

// wrapSelection 用 prefix / suffix 包住选区；没有选区时插入 placeholder
// 并把它选中，接着打字就是替换占位文字。
func (st *composeState) wrapSelection(prefix, suffix, placeholder string) {
	rs, a, b := st.runeSel()
	inner := string(rs[a:b])
	if inner == "" {
		inner = placeholder
	}
	st.body = string(rs[:a]) + prefix + inner + suffix + string(rs[b:])
	start := a + utf8.RuneCountInString(prefix)
	st.pendingSel = composeSelection{ok: true, start: start, end: start + utf8.RuneCountInString(inner)}
}

// prefixLines 给选区覆盖的每一行加前缀；没有选区时只处理光标所在的那一行。
// numbered 为真时用 1. 2. 3. 的行号前缀（有序列表）。
func (st *composeState) prefixLines(prefix string, numbered bool) {
	rs, a, b := st.runeSel()
	from := composeLineStart(rs, a)
	to := b
	if a == b {
		to = composeLineEnd(rs, a)
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
	st.body = string(rs[:from]) + seg + string(rs[to:])
	st.pendingSel = composeSelection{ok: true, start: from, end: from + utf8.RuneCountInString(seg)}
}

// insertText 在选区处插入一段文字（替换选区），并把光标放到插入内容之后。
func (st *composeState) insertText(text string) {
	rs, a, b := st.runeSel()
	st.body = string(rs[:a]) + text + string(rs[b:])
	at := a + utf8.RuneCountInString(text)
	st.pendingSel = composeSelection{ok: true, start: at, end: at}
}

func composeLineStart(rs []rune, at int) int {
	for at > 0 && rs[at-1] != '\n' {
		at--
	}
	return at
}

func composeLineEnd(rs []rune, at int) int {
	for at < len(rs) && rs[at] != '\n' {
		at++
	}
	return at
}

// ------------------------------------------------------------------
// 校验与请求体
// ------------------------------------------------------------------

// voteOptionTexts 是投票选项去空白后的结果；空行（用户还没填的默认行）直接
// 丢掉，不把它们当成空选项发出去。
func (st *composeState) voteOptionTexts() []string {
	out := make([]string, 0, len(st.voteOptions))
	for _, o := range st.voteOptions {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// betOptionTexts 同上。
func (st *composeState) betOptionTexts() []string {
	out := make([]string, 0, len(st.betOptions))
	for _, o := range st.betOptions {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// redeemCodeList 把一行一个的兑换码拆成数组：换行、逗号、分号都当分隔符，
// 去掉空白与空项，保持用户写的顺序。
func (st *composeState) redeemCodeList() []string {
	fields := strings.FieldsFunc(st.redeemCodes, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == '，' || r == ';' || r == '；'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// composeDeadline 把界面里写的本地时间换成文档要求的 ISO 8601（UTC，毫秒）。
// 接受「2026-11-01 11:00」「2026-11-01T11:00」与带秒的写法。
func composeDeadline(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return t.UTC().Format("2006-01-02T15:04:05.000Z"), nil
		}
	}
	return "", fmt.Errorf("时间格式应为 2026-11-01 11:00（本地时间）")
}

// payload 组装 POST /v1/topics 的请求体，同时完成校验：返回的 error 就是
// 界面要展示的那句中文。字段名、取值与长度上限全部照 api-write.md 原文。
func (st *composeState) payload() (map[string]any, error) {
	node := st.selectedNode()
	if node == nil {
		return nil, fmt.Errorf("请选择要发到哪个节点")
	}
	title := strings.TrimSpace(st.title)
	if title == "" {
		return nil, fmt.Errorf("标题不能为空")
	}
	if n := utf8.RuneCountInString(title); n > composeTitleMax {
		return nil, fmt.Errorf("标题最多 %d 字，当前 %d 字", composeTitleMax, n)
	}
	if n := utf8.RuneCountInString(st.body); n > composeContentMax {
		return nil, fmt.Errorf("正文最多 %d 字，当前 %d 字", composeContentMax, n)
	}

	p := map[string]any{"title": title, "node_id": node.ID}
	if st.body != "" {
		p["content"] = st.body
	}
	if st.anonymous {
		p["is_anonymous"] = true
	}
	if err := composeOptionalText(p, "title_prefix", st.titlePrefix, composeTitlePrefixMax, "标题前缀"); err != nil {
		return nil, err
	}
	if err := composeOptionalText(p, "tip", st.tip, composeTipMax, "提示信息"); err != nil {
		return nil, err
	}
	if err := composeOptionalText(p, "contact", st.contact, composeContactMax, "联系方式"); err != nil {
		return nil, err
	}
	if err := composeOptionalText(p, "status", st.status, composeStatusMax, "状态文案"); err != nil {
		return nil, err
	}

	if st.voteOpen {
		v, err := st.votePayload()
		if err != nil {
			return nil, err
		}
		p["vote"] = v
	}
	if st.scoreOpen {
		s, err := st.scorePayload()
		if err != nil {
			return nil, err
		}
		p["score"] = s
	}
	if st.betOpen {
		b, err := st.betPayload()
		if err != nil {
			return nil, err
		}
		p["bet"] = b
	}
	if st.redeemOpen {
		if err := st.redeemPayload(p); err != nil {
			return nil, err
		}
	}
	if st.poolOpen {
		pool, err := st.poolPayload()
		if err != nil {
			return nil, err
		}
		p["reward_pool"] = pool
	}
	if err := st.guardPayload(p); err != nil {
		return nil, err
	}
	return p, nil
}

// composeOptionalText 写入一个可选的文本字段：空值不发送，超长直接报错。
func composeOptionalText(p map[string]any, key, value string, maxRunes int, label string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if n := utf8.RuneCountInString(value); n > maxRunes {
		return fmt.Errorf("%s最多 %d 字，当前 %d 字", label, maxRunes, n)
	}
	p[key] = value
	return nil
}

func (st *composeState) votePayload() (map[string]any, error) {
	opts := st.voteOptionTexts()
	if len(opts) < 1 {
		return nil, fmt.Errorf("投票至少需要 1 个选项")
	}
	if len(opts) > composeOptionMax {
		return nil, fmt.Errorf("投票最多 %d 个选项，当前 %d 个", composeOptionMax, len(opts))
	}
	title := strings.TrimSpace(st.voteTitle)
	if title == "" {
		return nil, fmt.Errorf("投票标题不能为空")
	}
	if n := utf8.RuneCountInString(title); n > composeVoteTitleMax {
		return nil, fmt.Errorf("投票标题最多 %d 字，当前 %d 字", composeVoteTitleMax, n)
	}
	for _, o := range opts {
		if n := utf8.RuneCountInString(o); n > composeOptionTextMax {
			return nil, fmt.Errorf("投票选项最多 %d 字：%s", composeOptionTextMax, truncate(o, 12))
		}
	}
	deadline, err := composeDeadline(st.voteDeadline)
	if err != nil {
		return nil, fmt.Errorf("投票截止时间：%w", err)
	}
	typ := voteTypeAt(st.voteTypeIndex)
	v := map[string]any{
		"type":           typ,
		"title":          title,
		"options":        opts,
		"result_visible": st.voteResultVisible,
	}
	if deadline != "" {
		v["vote_deadline"] = deadline
	}
	if typ == "multiple" {
		choices := int(st.voteMaxChoices)
		if choices < 1 {
			choices = 1
		}
		v["max_choices"] = choices
	}
	return v, nil
}

func (st *composeState) scorePayload() (map[string]any, error) {
	target := strings.TrimSpace(st.scoreTarget)
	if target == "" {
		return nil, fmt.Errorf("评分对象不能为空")
	}
	if n := utf8.RuneCountInString(target); n > composeScoreTargetMax {
		return nil, fmt.Errorf("评分对象最多 %d 字，当前 %d 字", composeScoreTargetMax, n)
	}
	comment := strings.TrimSpace(st.scoreComment)
	if n := utf8.RuneCountInString(comment); n > composeScoreCommentMax {
		return nil, fmt.Errorf("评分理由最多 %d 字，当前 %d 字", composeScoreCommentMax, n)
	}
	s := map[string]any{"target_name": target, "score": int(st.scoreValue)}
	if comment != "" {
		s["comment"] = comment
	}
	return s, nil
}

func (st *composeState) betPayload() (map[string]any, error) {
	title := strings.TrimSpace(st.betTitle)
	if title == "" {
		return nil, fmt.Errorf("竞猜标题不能为空")
	}
	if n := utf8.RuneCountInString(title); n > composeBetTitleMax {
		return nil, fmt.Errorf("竞猜标题最多 %d 字，当前 %d 字", composeBetTitleMax, n)
	}
	opts := st.betOptionTexts()
	if len(opts) < composeBetOptionsMin {
		return nil, fmt.Errorf("竞猜至少需要 %d 个选项", composeBetOptionsMin)
	}
	if len(opts) > composeBetOptionsMax {
		return nil, fmt.Errorf("竞猜最多 %d 个选项，当前 %d 个", composeBetOptionsMax, len(opts))
	}
	for _, o := range opts {
		if n := utf8.RuneCountInString(o); n > composeBetOptionMax {
			return nil, fmt.Errorf("竞猜选项最多 %d 字：%s", composeBetOptionMax, truncate(o, 12))
		}
	}
	deadline, err := composeDeadline(st.betDeadline)
	if err != nil {
		return nil, fmt.Errorf("竞猜截止时间：%w", err)
	}
	b := map[string]any{"title": title, "options": opts}
	if deadline != "" {
		b["participate_deadline"] = deadline
	}
	return b, nil
}

// redeemPayload 把兑换码 / 红包写进 p（这几个字段在顶层，不嵌套）。
func (st *composeState) redeemPayload(p map[string]any) error {
	codes := st.redeemCodeList()
	if len(codes) > composeCodeMax {
		return fmt.Errorf("兑换码最多 %d 个，当前 %d 个", composeCodeMax, len(codes))
	}
	for _, c := range codes {
		if n := utf8.RuneCountInString(c); n > composeCodeTextMax {
			return fmt.Errorf("单个兑换码最多 %d 字：%s", composeCodeTextMax, truncate(c, 12))
		}
	}
	desc := strings.TrimSpace(st.codeDescription)
	if n := utf8.RuneCountInString(desc); n > composeCodeDescMax {
		return fmt.Errorf("兑换说明最多 %d 字，当前 %d 字", composeCodeDescMax, n)
	}
	perUser := int(st.maxRedeemPerUser)
	if perUser < composeRedeemPerUserLo || perUser > composeRedeemPerUserHi {
		return fmt.Errorf("每人可领数量应在 %d～%d 之间", composeRedeemPerUserLo, composeRedeemPerUserHi)
	}
	// 兑换码与红包至少要有一项，否则这个块什么也没配。
	if len(codes) == 0 && !st.redPacket {
		return fmt.Errorf("兑换码块需要填写兑换码，或打开红包")
	}
	if len(codes) > 0 {
		p["redeem_codes"] = codes
		p["max_redeem_per_user"] = perUser
	}
	if desc != "" {
		p["code_description"] = desc
	}
	if st.redPacket {
		p["enable_red_packet"] = true
		p["max_red_packet_amount"] = int(redPacketAt(st.redPacketIndex))
	}
	return nil
}

func (st *composeState) poolPayload() (map[string]any, error) {
	coins := int(st.poolCoins)
	if coins < composePoolCoinsLo || coins > composePoolCoinsHi {
		return nil, fmt.Errorf("金币池金额应在 %d～%d 之间", composePoolCoinsLo, composePoolCoinsHi)
	}
	pool := map[string]any{"initial_coins": coins}
	// 中奖概率是可选字段；0 表示用服务端默认值，不发送。
	if p := st.poolProbability; p > 0 {
		if p < composePoolProbLo || p > composePoolProbHi {
			return nil, fmt.Errorf("金币池概率应在 %.1f～%.1f 之间", composePoolProbLo, composePoolProbHi)
		}
		pool["probability"] = p
	}
	return pool, nil
}

// guardPayload 写入可见性门槛。不限制时不发送任何 access_guard_* 字段：
// 文档里清除限制用的是 "none"，那是编辑帖子时的写法。
func (st *composeState) guardPayload(p map[string]any) error {
	switch guardFromLabel(st.guardChoice) {
	case guardNone:
		return nil
	case guardLogin:
		p["access_guard_type"] = guardLogin
		return nil
	case guardLevel:
		lv := int(st.guardLevel)
		if lv < composeGuardLevelLo || lv > composeGuardLevelHi {
			return fmt.Errorf("可见等级应在 %d～%d 之间", composeGuardLevelLo, composeGuardLevelHi)
		}
		p["access_guard_type"] = guardLevel
		p["access_guard_level"] = lv
		return nil
	case guardAttribute:
		attr := strings.TrimSpace(st.guardAttr)
		if attr == "" {
			return fmt.Errorf("按属性可见需要填写 D20 属性名")
		}
		val := int(st.guardAttrValue)
		if val < 0 || val > composeGuardAttrHi {
			return fmt.Errorf("属性值应在 0～%d 之间", composeGuardAttrHi)
		}
		p["access_guard_type"] = guardAttribute
		p["access_guard_attr"] = attr
		p["access_guard_attr_value"] = val
		return nil
	}
	return nil
}

// createTopic 发一篇帖子。成功返回 201 与创建好的帖子；幂等键由调用方给，
// 同一个键重试不会重复发帖。
func (c *Client) createTopic(ctx context.Context, payload map[string]any, key string) (createdTopic, error) {
	return withAuthRetry(c, ctx, func(token string) (createdTopic, error) {
		d, err := c.postJSON(ctx, c.apiURL("/v1/topics"), token, key, payload)
		if err != nil {
			return createdTopic{}, err
		}
		o := asObject(d)
		shortID := o.str("short_id", "shortId")
		slug := ""
		if node := o.sub("node", "parent_node"); node != nil {
			slug = node.str("slug")
		}
		return createdTopic{ShortID: shortID, URL: topicURL(shortID, slug)}, nil
	})
}

// resetComposeForm 在发表成功后清空正文与各组件，保留节点选择：连着发两篇
// 通常还是发到同一个节点，节点列表也不用重拉。
func (st *composeState) resetComposeForm() {
	nodes, nodesAt, loaded, id, pick := st.nodes, st.nodesAt, st.nodeLoaded, st.nodeID, st.nodePick
	*st = *newComposeState()
	st.nodes, st.nodesAt, st.nodeLoaded, st.nodeID, st.nodePick = nodes, nodesAt, loaded, id, pick
}
