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

// 「发帖 / 发动态」窗口的数据、校验与请求。
//
// 端点与字段名逐字照抄 openapi v1 的 api-write.md：
//
//	GET  /v1/nodes              节点列表（顶层自带 children；?parent=<slug> 取子节点）
//	POST /v1/topics 🔑          发帖（必须带 Idempotency-Key，成功返回 201）
//	POST /v1/topics/moments 🔑  发一条 1～500 字的短动态
//
// 发帖的可选块按文档的字段名原样组装：vote（投票）、score（评分）、
// bet（竞猜）、redeem_codes（兑换码 / 红包）、reward_pool（金币池）、
// access_guard_*（可见性门槛）与 restricted_*（隐藏内容 / 付费墙）。文档明确
// 「随时可以新增字段」，所以解码一律忽略不认识的字段，不做严格结构体绑定。
// restricted_* 的字段名与枚举取自官网发帖页的 JS chunk（文档只给了中文说法）。
//
// node_id 在文档里标成必填，但线上接口允许不带：缺这个字段时由站点的 AI
// 按标题与正文判定分类（实测），所以发帖窗口默认不选节点，请求体里也就没有
// node_id（见 payload）。想指定时在写作区最下面的两列级联选择里选，选中才
// 带 uuid——文档里的「必填」指的是要指定节点时必须给 uuid，不是 slug。
const (
	composeTitleMax   = 500
	composeContentMax = 20000
	// composeMomentMax 是动态正文上限（POST /v1/topics/moments）。
	composeMomentMax = 500

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

	// 隐藏内容：枚举与校验范围照官网发帖页（站点 JS chunk）。正文上限沿用
	// 普通正文的 20000 字，避免客户端发出服务端必然拒绝的超长内容。
	composeRestrictedContentMax   = 20000
	composeRestrictedD20Lo        = 1
	composeRestrictedD20Hi        = 25
	composeRestrictedDurationLo   = 1
	composeRestrictedDurationHi   = 12
	composeRestrictedAuctionTopLo = 1
)

// 「发帖」窗口的两种模式：发正式帖子，或发一条短动态。动态没有标题、节点与
// 附加组件，只有 1～500 字的正文。
const (
	composeModePost = iota
	composeModeMoment
)

var composeModeLabels = []string{"帖子", "动态"}

// composeNode 是一个可发帖的节点。指定节点时用的是 uuid（node_id），不是// slug；级联选择按 id 记选中项，显示时才换成名字。
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
	// NodeSlug 是创建响应里的节点 slug：不选节点发帖时是 nebula，客户端
	// 要轮询详情，直到服务端的 AI 把它挪到正式节点（见 topic_node.go）。
	NodeSlug string
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

// 隐藏内容的访问类型。取值逐字照官网发帖页的 PostRestrictedAccessType，
// 标签是同一份 chunk 里展示给用户的文案；下拉框存标签，请求里发机器取值。
const (
	restrictedAccessPro     = "pro"
	restrictedAccessReward  = "reward"
	restrictedAccessD20     = "d20"
	restrictedAccessAuction = "auction"
)

var restrictedAccessLabels = []string{"Pro 可见", "打赏可见", "属性检定可见", "竞拍可见"}

var restrictedAccessValues = []string{
	restrictedAccessPro, restrictedAccessReward, restrictedAccessD20, restrictedAccessAuction,
}

func restrictedAccessFromLabel(label string) string {
	for i, l := range restrictedAccessLabels {
		if l == label {
			return restrictedAccessValues[i]
		}
	}
	return restrictedAccessD20 // 与官网发帖页的默认值一致
}

// 属性检定的 D20 属性：机器取值照官网 PostRestrictedD20Attr，中文标签照
// POST_RESTRICTED_D20_ATTR_LABEL_MAP。界面直接绑定机器取值。
var restrictedD20Attrs = []struct{ Value, Label string }{
	{"karma", "财富"},
	{"experience", "阅历"},
	{"charm", "魅力"},
	{"luck", "幸运"},
	{"diligence", "勤勉"},
	{"craft", "技艺"},
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
	// nodePick 是第一列的展开状态；nodeID 是手动选中的节点 uuid，提交时直接
	// 用。默认空串：不发送 node_id，发帖后由站点 AI 判定分类（见 payload）。
	nodePick cascaderState
	nodeID   string
	// nodeSearch 是级联浮层顶部搜索框的草稿；空串显示两列级联。
	nodeSearch string
	// 选中节点的简介：GET /v1/nodes/:slug 单独读一次，列表响应里没有可靠的
	// 简介字段。nodeDetailSlug 记录这份简介属于哪个节点。
	nodeDetailSlug    string
	nodeDetail        nodeDetail
	nodeDetailLoading bool
	nodeDetailErr     string
	nodeDetailSeq     uint64

	// 基础字段：界面只留标题与正文；匿名开关在右侧栏的「发布设置」里。
	// 文档里 title_prefix / tip / contact / status 这类补充字段不再展示，
	// 也不再进入请求体。
	title      string
	body       string
	titleField ui.Handle
	bodyField  ui.Handle
	focusTitle bool
	anonymous  bool
	// 本地草稿（见 draft.go）：draftID 是这份表单对应哪条草稿，空表示还没落过盘；
	// draftFP 是本帧指纹，draftSavedFP 是上次落盘时的指纹。帧尾算一次指纹，
	// 变了才走防抖落盘。
	draftID      string
	draftFP      uint64
	draftSavedFP uint64

	// 正文编辑器与评论编辑器共用一套选区、工具面板与预览状态（见
	// editor_core.go）。匿名嵌入，所以外层仍可直接读写 st.selStart /
	// st.preview 等字段；正文字符串仍留在这里，方便 payload 与草稿读写。
	mdEditor
	// bodySuggest / momentSuggest 是两段正文各自的 @ 提及与 : 表情短码候选
	// 状态（见 compose_suggest.go）。模式切换、草稿分开存，候选也各存各的。
	bodySuggest   composeSuggestState
	momentSuggest composeSuggestState

	// 附加组件：右侧栏常驻，点一项以弹窗编辑。下面每个 xxxOpen 是「这个组件
	// 随帖一起发送」的开关（不再是折叠面板的展开状态）；moduleOpen 为真时
	// moduleIndex 指向当前打开的那一块（见 compose_ui.go 的组件表）。
	moduleOpen  bool
	moduleIndex int

	// 投票块。voteChecked 与 voteOptions 等长，是行首勾选框的状态；
	// voteBatch* 是「批量添加」内联录入区的展开状态与一次粘贴的多行文本。
	voteOpen          bool
	voteTypeIndex     int // 0 单选（pk），1 多选（multiple）
	voteTitle         string
	voteOptions       []string
	voteChecked       []bool
	voteBatchOpen     bool
	voteBatchText     string
	voteResultVisible bool
	voteDeadlineOn    bool      // 截止时间选填：开关决定这一项是否进请求体
	voteDeadlineAt    time.Time // 本地时间，提交时转 UTC ISO 8601
	voteMaxChoices    float64

	// 评分块
	scoreOpen    bool
	scoreTarget  string
	scoreValue   float64
	scoreComment string

	// 竞猜块。批量编辑的状态与投票块同构。
	betOpen       bool
	betTitle      string
	betOptions    []string
	betChecked    []bool
	betBatchOpen  bool
	betBatchText  string
	betDeadlineOn bool
	betDeadlineAt time.Time

	// 兑换码 / 红包块
	redeemOpen       bool
	redeemCodes      string // 一行一个
	redeemBatchOpen  bool   // 「批量粘贴」内联录入区
	redeemBatchText  string
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

	// 隐藏内容（付费墙）：字段名与取值照官网发帖页。access 存下拉框标签，
	// attr 直接存机器取值；duration 为 0 表示不发送有效期。
	restrictedOpen            bool
	restrictedAccessChoice    string
	restrictedContent         string
	restrictedD20Attr         string
	restrictedD20Threshold    float64
	restrictedDurationMonths  float64
	restrictedAuctionTopN     float64
	restrictedAuctionDeadline time.Time

	// 模式：帖子 / 动态。两边的草稿分开存，切换模式不会互相覆盖。
	mode        int
	lastMode    int
	momentDraft string
	momentField ui.Handle
	momentBusy  bool
	momentErr   string
	momentKey   string
	momentSig   string
	momentGen   uint64

	// 提交状态
	posting bool
	err     string
	// okTitle / okShortID / okURL 是发表成功后的回执：表单已经清空，这一行
	// 留在窗口里让作者立刻打开刚发的帖子。
	okTitle   string
	okShortID string
	okURL     string
	// okPending 表示这篇帖子还在等 AI 分配节点：回执上说明「节点分配中…」，
	// 分好后会更新 okURL（见 topic_node.go）。nodeWatch* 是轮询的定时器与
	// 代数，只在 UI 线程读写。
	okPending        bool
	nodeWatchTimer   *time.Timer
	nodeWatchGen     uint64
	nodeWatchTries   int
	nodeWatchShortID string
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
		mdEditor:       newMDEditor(),
		voteOptions:    []string{"", ""},
		voteMaxChoices: 1,
		// 截止时间选填：开关默认关。先给一个「明天此时」的合理默认值，
		// 打开开关时日历不会停在公元 1 年。
		voteDeadlineAt:   time.Now().Add(24 * time.Hour),
		betOptions:       []string{"", ""},
		betDeadlineAt:    time.Now().Add(24 * time.Hour),
		maxRedeemPerUser: composeRedeemPerUserLo,
		poolCoins:        100,
		guardChoice:      guardTypeLabels[0],
		guardLevel:       composeGuardLevelLo,
		// 隐藏内容沿用官网发帖页的默认值：属性检定、财富、目标 10、竞拍前 3 名、
		// 截止时间 48 小时后；有效期留空表示不发送。
		restrictedAccessChoice:    restrictedAccessLabels[2],
		restrictedD20Attr:         restrictedD20Attrs[0].Value,
		restrictedD20Threshold:    10,
		restrictedAuctionTopN:     3,
		restrictedAuctionDeadline: time.Now().Add(48 * time.Hour),
	}
}

// ------------------------------------------------------------------
// 节点列表（请求与本地缓存在 nodes.go）
// ------------------------------------------------------------------

// ensureComposeNodes 委托共享节点目录加载：发帖窗口不再单独发请求，只把
// 目录的结果镜像到自己的渲染状态里。缓存优先、失败冷却与单飞都在目录里。
func (a *app) ensureComposeNodes() {
	st := a.compose
	if st == nil {
		return
	}
	cat := &a.nodeCat
	// 测试与调用方有时直接把加载态 / 错误摆进 composeState（例如展示一个
	// 失败的节点区）。目录没有对应状态时就不要反过来把它抹掉。
	if (st.nodeLoading && !cat.loading) || (st.nodeErr != "" && cat.err == "") ||
		(st.nodeLoaded && !cat.loaded) {
		return
	}
	// 发帖窗口的「重试」会清掉自己的冷却字段；把这个意图传给共享目录，
	// 否则目录还在冷却里，点重试看起来没有反应。
	if st.nodeFailedAt.IsZero() && !cat.failedAt.IsZero() {
		a.nodeCat.failedAt = time.Time{}
		a.nodeCat.at = time.Time{}
	}
	a.ensureNodeCatalog()
	a.syncComposeNodeState()
}

// syncComposeNodeState 把共享节点目录的状态抄回发帖窗口。保留 st.nodeSeq
// 作为发帖窗口自己的简介请求序号，与目录的加载序号互不干扰。
func (a *app) syncComposeNodeState() {
	st := a.compose
	if st == nil {
		return
	}
	cat := &a.nodeCat
	if len(cat.nodes) > 0 {
		st.nodes = cat.nodes
	}
	if !cat.at.IsZero() {
		st.nodesAt = cat.at
	}
	st.nodeLoaded = cat.loaded
	st.nodeLoading = cat.loading
	st.nodeErr = cat.err
	st.nodeFailedAt = cat.failedAt
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

// selectedNode 返回级联选择当前选中的节点；没选中时为 nil。默认就是没选中：
// 不指定节点时发帖后由站点 AI 判定分类。
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

// applyMarkdown 把一种 Markdown 标记写进正文。真正的插入逻辑在 editor_core.go，
// 评论编辑器用同一套；这里只把正文指针传过去，保留旧调用方式。
func (st *composeState) applyMarkdown(f composeFormat) {
	st.mdEditor.applyMarkdown(&st.body, f)
}

// applyMarkdown 是共用实现：选区来自上一帧构建输入框时读到的值，写入后把
// 希望恢复的选区记在 pendingSel，由输入框执行。
func (e *mdEditor) applyMarkdown(body *string, f composeFormat) {
	switch f {
	case composeBold:
		e.wrapSelection(body, "**", "**", "加粗文字")
	case composeItalic:
		e.wrapSelection(body, "*", "*", "斜体文字")
	case composeStrike:
		e.wrapSelection(body, "~~", "~~", "删除线")
	case composeInlineCode:
		e.wrapSelection(body, "`", "`", "行内代码")
	case composeHeading:
		e.prefixLines(body, "## ", false)
	case composeQuote:
		e.prefixLines(body, "> ", false)
	case composeBulletList:
		e.prefixLines(body, "- ", false)
	case composeOrderedList:
		e.prefixLines(body, "", true)
	case composeCodeBlock:
		e.wrapSelection(body, "```\n", "\n```", "代码")
	case composeLink:
		e.wrapSelection(body, "[", "](url)", "链接文字")
	case composeImage:
		e.wrapSelection(body, "![", "](url)", "图片说明")
	case composeDivider:
		e.insertText(body, "\n\n---\n\n")
	}
}

// 下面四个包装方法只为兼容发帖窗口原有的调用（含既有测试）。
func (st *composeState) runeSel() ([]rune, int, int) {
	return st.mdEditor.runeSel(st.body)
}

func (st *composeState) wrapSelection(prefix, suffix, placeholder string) {
	st.mdEditor.wrapSelection(&st.body, prefix, suffix, placeholder)
}

func (st *composeState) prefixLines(prefix string, numbered bool) {
	st.mdEditor.prefixLines(&st.body, prefix, numbered)
}

func (st *composeState) insertText(text string) {
	st.mdEditor.insertText(&st.body, text)
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

// composeDeadline 把界面上日期 / 时间控件给出的本地时间换成文档要求的
// ISO 8601（UTC，毫秒）。零值表示没设，返回空串（不发这个字段）。
func composeDeadline(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format("2006-01-02T15:04:05.000Z")
}

// payload 组装 POST /v1/topics 的请求体，同时完成校验：返回的 error 就是
// 界面要展示的那句中文。字段名、取值与长度上限全部照 api-write.md 原文。
func (st *composeState) payload() (map[string]any, error) {
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
	// 图片还在上传（或上传失败留在正文里）时不能发布：正文里那些
	// upload:ID 只是临时占位，发到站点就是一堆坏链接。
	if n := editorPendingUploads(st.body); n > 0 {
		return nil, fmt.Errorf("还有 %d 张图片未处理完，请等上传结束或移除后再发布", n)
	}

	p := map[string]any{"title": title}
	// 网页端把标题开头的【】/[] 块单独作为 title_prefix 一起发（≤100 字）。
	// 桌面端这些标签已经写进 title，这里照抄同一份提取结果，保持一致。
	if prefix := composeTitlePrefix(title); prefix != "" {
		p["title_prefix"] = prefix
	}
	// 节点是选填的：没选就不发 node_id，发帖后由站点的 AI 判定分类。手动
	// 选中了才带上 uuid（文档要求的是 uuid，不是 slug）。
	if node := st.selectedNode(); node != nil {
		p["node_id"] = node.ID
	}
	if st.body != "" {
		p["content"] = st.body
	}
	if st.anonymous {
		p["is_anonymous"] = true
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
	if err := st.restrictedPayload(p); err != nil {
		return nil, err
	}
	return p, nil
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
	typ := voteTypeAt(st.voteTypeIndex)
	v := map[string]any{
		"type":           typ,
		"title":          title,
		"options":        opts,
		"result_visible": st.voteResultVisible,
	}
	if st.voteDeadlineOn {
		if deadline := composeDeadline(st.voteDeadlineAt); deadline != "" {
			v["vote_deadline"] = deadline
		}
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
	b := map[string]any{"title": title, "options": opts}
	if st.betDeadlineOn {
		if deadline := composeDeadline(st.betDeadlineAt); deadline != "" {
			b["participate_deadline"] = deadline
		}
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

// restrictedPayload 写入隐藏内容（付费墙）。字段名与取值逐字照官网发帖页：
// restricted_access_type 是 pro / reward / d20 / auction，D20 属性用
// karma/experience/charm/luck/diligence/craft，只有对应类型才带各自的字段。
// 有效期留空（0）时不发送；未开启这一块时一个 restricted_* 字段都不发。
func (st *composeState) restrictedPayload(p map[string]any) error {
	if !st.restrictedOpen {
		return nil
	}
	if strings.TrimSpace(st.restrictedContent) == "" {
		return fmt.Errorf("隐藏内容不能为空")
	}
	if n := utf8.RuneCountInString(st.restrictedContent); n > composeRestrictedContentMax {
		return fmt.Errorf("隐藏内容最多 %d 字，当前 %d 字", composeRestrictedContentMax, n)
	}
	access := restrictedAccessFromLabel(st.restrictedAccessChoice)
	// 保留作者输入的原样内容（Markdown 的前导空格可能有意义），只用 trim 后的
	// 结果做非空判断。
	p["restricted_content"] = st.restrictedContent
	p["restricted_access_type"] = access

	switch access {
	case restrictedAccessD20:
		if st.restrictedD20Attr == "" {
			return fmt.Errorf("属性检定需要选择 D20 属性")
		}
		n := int(st.restrictedD20Threshold)
		if n < composeRestrictedD20Lo || n > composeRestrictedD20Hi {
			return fmt.Errorf("属性检定目标值应在 %d～%d 之间",
				composeRestrictedD20Lo, composeRestrictedD20Hi)
		}
		p["restricted_d20_attr"] = st.restrictedD20Attr
		p["restricted_d20_threshold"] = n
	case restrictedAccessAuction:
		top := int(st.restrictedAuctionTopN)
		if top < composeRestrictedAuctionTopLo {
			return fmt.Errorf("竞拍可见至少需要 %d 名解锁者", composeRestrictedAuctionTopLo)
		}
		p["restricted_auction_top_n"] = top
		if deadline := composeDeadline(st.restrictedAuctionDeadline); deadline != "" {
			p["restricted_auction_deadline"] = deadline
		}
	}

	if months := int(st.restrictedDurationMonths); months != 0 {
		if months < composeRestrictedDurationLo || months > composeRestrictedDurationHi {
			return fmt.Errorf("有效期应在 %d～%d 个月之间",
				composeRestrictedDurationLo, composeRestrictedDurationHi)
		}
		p["restricted_duration_months"] = months
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
		return createdTopic{ShortID: shortID, URL: topicURL(shortID, slug), NodeSlug: slug}, nil
	})
}

// createMoment 发一条短动态。响应与发帖一样是 201 + 创建好的对象；动态没有
// 节点，原文地址按无节点帖子拼。
func (c *Client) createMoment(ctx context.Context, key, content string) (createdTopic, error) {
	return withAuthRetry(c, ctx, func(token string) (createdTopic, error) {
		d, err := c.postJSON(ctx, c.apiURL("/v1/topics/moments"), token, key,
			map[string]any{"content": content})
		if err != nil {
			return createdTopic{}, err
		}
		shortID := asObject(d).str("short_id", "shortId")
		return createdTopic{ShortID: shortID, URL: topicURL(shortID, "")}, nil
	})
}

// resetComposeForm 在发表成功后清空正文与各组件。节点列表与手动指定的节点
// 都留着：默认本来就不指定（由 AI 判定），而用户特意选过节点时，连着发两篇
// 通常还是发到同一个节点，不该被一次发布悄悄改回去。
func (st *composeState) resetComposeForm() {
	// 清空表单等于放弃上一条回执：停掉还在跑的节点轮询，别让它在后台
	// 继续为一条已经不在界面上的帖子发请求。
	if st.nodeWatchTimer != nil {
		st.nodeWatchTimer.Stop()
		st.nodeWatchTimer = nil
	}
	st.nodeWatchGen++
	st.nodeWatchShortID = ""
	st.nodeWatchTries = 0
	mode := st.mode
	nodes, nodesAt, loaded, pick := st.nodes, st.nodesAt, st.nodeLoaded, st.nodePick
	id := st.nodeID
	*st = *newComposeState()
	st.mode = mode
	st.nodes, st.nodesAt, st.nodeLoaded, st.nodePick = nodes, nodesAt, loaded, pick
	st.nodeID = id
}

// resetMomentForm 清掉动态草稿与错误，保留幂等键以外的模式状态。
func (st *composeState) resetMomentForm() {
	st.momentDraft = ""
	st.momentErr = ""
	st.momentKey = ""
	st.momentSig = ""
}

// ------------------------------------------------------------------
// 多项输入的批量编辑
// ------------------------------------------------------------------

// composeBatchLines 把一次粘贴的多行文本拆成条目：按换行分、去空白与空行。
// 选项内容里可能有逗号，所以这里只认换行，不像兑换码那样按标点拆。
func composeBatchLines(raw string) []string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// appendComposeBatch 把批量粘贴的条目加进现有列表。当前全是空行时先丢掉这些
// 占位行，免得粘贴完列表里还混着几行空白；超过 max 的部分截断。返回新列表、
// 实际加入的条数与因上限被丢掉的条数，行数不足 min 时补占位行。
func appendComposeBatch(current []string, lines []string, min, max int) ([]string, int, int) {
	base := current
	allEmpty := true
	for _, o := range base {
		if strings.TrimSpace(o) != "" {
			allEmpty = false
			break
		}
	}
	if allEmpty {
		base = nil
	}
	out := append([]string{}, base...)
	added, skipped := 0, 0
	for _, line := range lines {
		if len(out) >= max {
			skipped++
			continue
		}
		out = append(out, line)
		added++
	}
	for len(out) < min {
		out = append(out, "")
	}
	return out, added, skipped
}

// syncComposeChecks 让勾选框数组与选项行等长：加行补 false，删行截短。
func syncComposeChecks(checked []bool, n int) []bool {
	if len(checked) > n {
		return checked[:n]
	}
	for len(checked) < n {
		checked = append(checked, false)
	}
	return checked
}

// removeComposeChecked 一次删掉勾中的行，但至少保留 min 行。返回新列表、
// 清空后的勾选状态、实际删掉的行数，以及是否因为下限留下了勾选项。
func removeComposeChecked(options []string, checked []bool, min int) ([]string, []bool, int, bool) {
	budget := len(options) - min
	if budget < 0 {
		budget = 0
	}
	out := make([]string, 0, len(options))
	flags := make([]bool, 0, len(options))
	removed, held := 0, false
	for i, o := range options {
		on := i < len(checked) && checked[i]
		if on && budget > 0 {
			budget--
			removed++
			continue
		}
		if on {
			held = true
		}
		out = append(out, o)
		flags = append(flags, false)
	}
	for len(out) < min {
		out = append(out, "")
		flags = append(flags, false)
	}
	return out, flags, removed, held
}
