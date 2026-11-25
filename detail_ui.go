package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/markdown/parser"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/motion"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// detailState is per-post UI state. Background work only publishes results
// through app.publish; no element or context is ever retained here.
// host 指向承载这篇帖子的详情窗口（老的内联路径为 nil）；dead 在窗口关闭、
// 标签关闭或合并搬走后置位，后台结果据此丢弃。
type detailState struct {
	shortID, focusID, fallbackURL string
	host                          *detailWindow
	dead                          bool
	topic                         *topicDetail
	topicLoading                  bool
	topicErr                      string
	topicGen                      uint64
	// nodeWatch* 跟踪「节点寻找中」：不选节点发的帖子先落在星云，服务端
	// 异步分配。定时器只在 UI 线程读写，网络结果经 publish 回来。
	nodeWatchTimer *time.Timer
	nodeWatchGen   uint64
	nodeWatchTries int
	// 本地正文缓存：cachedAt 是缓存写入时间；fromCache 表示当前正文来自
	// cache/offline.json；cacheNote 是详情页顶部的一行离线说明。
	cachedAt  time.Time
	fromCache bool
	cacheNote string

	replyList    []replyItem
	replyLoading bool
	replyErr     string
	// replyBlocked 来自评论接口的 d:null：无权访问或帖子不存在。与正文的
	// access_guard.passed 一起决定可见性，不靠「列表空不空」反推。
	replyBlocked bool
	// replyMoreErr 是「续拉下一页评论」失败的原因：已经显示出来的评论
	// 不能被整页错误替换，只在列表底部提示并可重试。
	replyMoreErr          string
	replyLoaded           bool
	replyPage, replyTotal int
	replyTotalPages       int
	replyNoMore           bool // 评论已到底（空页 / 无新增），停止无限滚动续拉
	replyGen              uint64
	// flat 是评论区的视图：false 楼层视图、true 扁平时间序。它是本机偏好，
	// 切换时立即落盘（见 reply_view.go 的 saveReplyViewMode），与具体帖子
	// 无关，打开别的帖子也沿用。
	flat bool
	// 评论楼层视图的本地记忆（见 reply_view.go）。viewMem 是这次打开读到的
	// 上一次状态（按帖子记，不含视图模式）；collapsed 是当前折叠的楼中楼父
	// 评论（运行态，来自 viewMem）；readBase 是「新评论」的基线时间；stayIdx /
	// stayID 是评论区实时停留的行号与评论 ID；display 是剔除折叠隐藏行后的
	// 显示列表，所有滚动定位与列表行数都基于它，避免隐藏行打乱索引；
	// hiddenCount 记录每个折叠父被收起的后代条数，供「展开 N 条回复」使用。
	viewMem   *replyViewMemory
	collapsed map[string]bool
	// revealed 记着本次浏览里被手动展开的被屏蔽评论 id。只在内存，不落盘：
	// 关掉详情、换一篇帖子就丢弃（「不需要记忆」）。键是评论 uuid，同一页
	// 多条被屏蔽的评论互不影响；名单重新加载后由 clearRevealed 清空。
	revealed    map[string]bool
	readBase    time.Time
	stayIdx     int
	stayID      string
	display     []replyItem
	hiddenCount map[string]int
	// 评论区按作者过滤的临时视角：只作用于 display，不落盘、不改 replyList。
	// authorFilter 空串表示不过滤；authorFilterID 有值时优先按 id 比对；
	// authorFilterIsOP 标出入口是不是「只看楼主」，决定文案与匿名帖处理；
	// authorFilterName 是展示用昵称；filterAutoPages 记已为过滤自动续拉的页数。
	authorFilter     string
	authorFilterID   string
	authorFilterIsOP bool
	authorFilterName string
	filterAutoPages  int
	list             ui.ListState
	// base64Open 记着评论里哪些 Base64 片段已经被点开成解码态。键是「评论
	// id | 片段序号 | 编码串」（见 base64RevealKey），所以同一条评论里的多个
	// 片段互不串扰；它只在这一次浏览里有效，不落盘也不进后台。
	base64Open map[string]bool
	// bodyScroll 是左栏正文的滚动偏移：TrackScroll 记下来，供「返回顶部」
	// 判断正文是不是已经滚离顶部、以及把它直接写回 0 回顶。
	bodyScroll ui.ScrollState
	// bodyCollapsed 表示左栏正文已向左收起，评论区占满整行；replyCollapsed
	// 表示右栏评论区已向右收起，正文占满整行。两者互斥：收起一边后另一边
	// 已经铺满整行，再收就没有意义了。它们只是界面状态，不落盘：每次进入
	// 详情都重置成双栏展开（见 newDetailState 与 closeDetail 的弹栈分支），
	// 下次打开不会继承上一次的收起。两个例外：从通知跳到具体评论时默认
	// 收起正文（见 openNotificationTarget），便于直接看到那层楼；评论加载
	// 完成且确实一条都没有时默认收起评论区（见 loadRepliesFor），把整行让
	// 给正文，想看空状态仍可从右边缘「展开评论」拉回来。
	bodyCollapsed  bool
	replyCollapsed bool
	// bodyWidth 是正文栏在展开时的宽度（DIP），由 Lobe 的 DraggablePanel
	// 在拖动接缝时改写。和 bodyCollapsed 一样只在这一次浏览里有效，不落盘：
	// 每次进入详情都用默认值（见 openDetail）。
	bodyWidth float32
	// layoutWidth 是上一次构建看到的窗口内容宽度（DIP）。详情窗口被拉大
	// 或缩小时按新旧宽度比缩放 bodyWidth，正文与评论的分栏比例跟着窗口
	// 走；拖动接缝时宽度没变，不会覆盖用户调好的比例。
	layoutWidth float32

	// 从评论列表点进来时要定位到具体某条评论。目标常常不落在第 1 页
	// （扁平序按 flat_floor 排），所以首屏没找到时自动续拉后面的页，
	// focusTries 限制最多拉多少页，focusMiss 在找完还没有时置位。
	focusPending bool
	focusTries   int
	focusMiss    bool

	// notifID / notifCommentID 是「从通知点进来」的那条消息与它指向的评论。
	// 消息不在点开时就标为已读：等 notifCommentID 这层楼真正进入评论区可视区
	// 才落账（见 markNotificationSeen）。notifCommentID 单独存一份，因为用户
	// 在评论区点「回复 N 楼」会把 focusID 换成父评论。
	notifID        string
	notifCommentID string

	// avatarFollow 按用户 uuid 记头像右键菜单发起的关注状态：正文作者与
	// 多条评论里的同一个人共用一份，提交中不会被连点重复发起。只在内存，
	// 关掉详情就丢；状态写回走 submitFollow 的 publish，不碰 UI 元素。
	avatarFollow map[string]*followState
	// avatarRelations 按昵称记右键菜单打开时异步取到的用户关系（公开资料里的
	// is_following）。筛选名单是进程级共享快照，由 ensureContentFilter 统一
	// 拉取；这里只补资料卡之外的关注状态，失败按 30s 退避。
	avatarRelations map[string]*avatarRelationState
	// fav 是标题下方作者行收藏按钮的状态；react 是表态条与选择器的状态
	// （见 interact.go），vote 是正文末尾投票块的状态（见 vote.go）。三者
	// 都只在 UI 线程读写，网络结果通过 publish 回来。
	fav   favoriteState
	react reactionState
	vote  voteUIState
	bet   betUIState
	// restricted 是正文末尾受限内容块的状态（见 restricted.go）。
	restricted restrictedUIState
	// redeem 是正文末尾兑换码块的状态（见 redeem.go）。
	redeem redeemUIState
	// pool 是正文末尾金币池注资气泡的状态（见 reward_pool.go）。
	pool rewardPoolUIState
	// bmBusy 是正在提交书签的评论 id：一次只提交一条，避免连点把同一条
	// 评论反复加 / 删（见 bookmarks.go）。
	bmBusy string
	// coll 是作者行「合集」气泡的状态（见 collections.go）。
	coll collectionState
	// collNav 是当前帖子所在合集的导航状态：读合集内的帖子列表，用它给出
	// 合集条数与「上一篇」。按合集 id 记账，换帖子时不会把上一篇的结果串到
	// 下一篇上（见 collection_browse.go）。
	collNav detailCollectionNav
	// collID / collName 是「从合集浏览点开」时带来的上下文。详情接口的稳定
	// 字段没有合集归属，这份上下文是正文里能画出当前合集卡片的依据。
	collID   string
	collName string
	// comments 是作者「关闭 / 开启评论」那一次操作的状态（见 comments_status.go）。
	comments commentsStatusState
	// lottery 是作者「发起抽奖」的状态：中奖人数、在途与回包代次
	// （见 lottery.go）。开奖结果本身存在 topic.Lottery 里。
	lottery lotteryState

	// composer: write a comment (optionally as a nested reply to replyTo).
	// 发送失败的原因不落在页面上，用主窗口的 toast 说明（见 submitReply）。
	// mdEditor 与发帖窗口共用选区、工具面板、表情与预览状态
	// （见 editor_core.go）；正文字符串仍是下面的 draft。
	mdEditor
	draft string
	// 本地草稿（见 draft.go）：帧尾算 draftFingerprint，与 draftSavedFP 不同
	// 才落盘。focusComposer 是从草稿箱恢复后的一次性聚焦标记，由 replyInput
	// 消费一次：点完「打开」就能接着打字。
	draftFP       uint64
	draftSavedFP  uint64
	focusComposer bool
	// replyTo 是当前在回哪条评论；零值表示发一条新的顶层评论。带上层级
	// 与父评论，是因为回复三级评论时提交用的 parentId 不是被点的那条
	// （见 detail.go 的 replyTarget）。
	replyTo replyTarget
	posting bool
	// composerField is the input's identity across builds: its committed
	// focus decides whether the composer rests at one line or unfolds.
	composerField ui.Handle
	// composerIdle folds the composer back after a comment was sent, until
	// the input is selected again or the next draft is typed.
	composerIdle bool
	// composerFold 是点了「收起输入框」后的手动折叠标记：向上展开的输入框
	// 折回一行，草稿与回复目标都保留。折叠态的输入框上盖着一枚透明的展开
	// 按钮（见 replyInput）：工具条带 KeepFocus，点了收起按钮焦点仍留在
	// 输入框里，靠焦点跳变认不出用户的下一次点击，只能给一个显式入口。
	// 再次展开后标记即清，只存内存，换帖或关窗随 detailState 一起丢弃。
	composerFold bool
	// composerExpanded 记着上一帧评论输入框是不是展开的。输入框从一行长成
	// 多行会吃掉评论列表的高度，展开那一帧要把列表往上推一段，免得底部
	// 那条评论被长出来的编辑器盖住（见 replyPane）。
	composerExpanded bool
	// composerPushPending 是点某条评论的「回复」入口后的一次性标记：replyTo
	// 在构建那一行的 Clicked 里才写入，而 replyPane 在构建列表之前就算完了
	// 展开状态，会漏掉这次展开，下一帧靠它补一次向上推（见 replyEntry）。
	composerPushPending bool
	// replyBottomID 记着上一帧评论列表最底下那条可见评论的 id。推列表时列表
	// 可能已经因为编辑器展开而变矮、那一行被裁掉，按 id 才能找回它并完整
	// 推回视口（见 replyPane）。
	replyBottomID string
	// composerDetached 表示这份草稿正在独立写评论窗口里编辑（见
	// reply_window.go）：页面里的评论框只留一行说明与「收回」入口，同一个
	// 草稿不会在两个窗口里各有一个输入框。winPreview 是那个窗口右侧预览栏
	// 的开关，默认显示。
	composerDetached bool
	winPreview       bool
	// mentions 是 @ 提及的两份名单（守护者 / 本帖参与人）与气泡状态。名单
	// 只在这里存运行态，不落盘（见 mentions.go）。
	mentions mentionState
	// emojiShortcodes 是键入 ":" 弹出的社区表情短码候选面板状态。候选来自
	// 进程级表情表，没有名单与网络请求（见 emoji_shortcode.go）。
	emojiShortcodes emojiShortcodeState
}

// collectionContext 给出当前帖子的合集归属。详情响应偶尔也会带（本地归入后
// 就会写回 topic），优先用它；没有时退回从合集浏览点开时带来的上下文。两者
// 都没有时 known 为 false，正文不画合集卡片。
func (d *detailState) collectionContext() (id, name string, known bool) {
	if d == nil {
		return "", "", false
	}
	if d.topic != nil && d.topic.CollectionKnown && d.topic.CollectionID != "" {
		return d.topic.CollectionID, d.topic.CollectionName, true
	}
	if d.collID != "" {
		return d.collID, d.collName, true
	}
	return "", "", false
}

func (d *detailState) originURL() string {
	if d.topic != nil && d.topic.URL != "" {
		if d.focusID != "" {
			// 已定位到的评论用完整永久链接（含页码），站外打开也能落到那层楼；
			// 还没加载到它时退回只带 commentId 的地址。
			if at := replyIndexByID(d.replyList, d.focusID); at >= 0 {
				return replyPermalink(d.topic, d.fallbackURL, d.replyList[at])
			}
			return d.topic.URL + "?commentId=" + url.QueryEscape(d.focusID)
		}
		return d.topic.URL
	}
	return d.fallbackURL
}

// 楼主回复上那枚「OP」标签的配色：站点网页用 #00d3bb 的青绿底配
// oklch(38% .063 188.416) 的深青字。这套颜色不跟主题的强调色走——强调色
// 已经用来表示「当前账号的回复」与置顶 / 定位，OP 要自己是一眼可辨的标记。
var (
	opTagBackground = ui.Hex("#00d3bb")
	opTagText       = ui.Oklch(0.38, 0.063, 188.416)
)

// PK 投票的队伍标签：所有 PK 帖子统一按选项列表顺序，第一个红队、第二个
// 蓝队。底色固定不跟主题走，深色字在浅底上始终清楚。
var (
	pkTeamNames       = [...]string{"红队", "蓝队"}
	pkTeamBackgrounds = [...]ui.Color{
		ui.Hex("#ffa1b1"), // 红队
		ui.Hex("#66d6fe"), // 蓝队
	}
	pkTeamText = ui.Hex("#1f2937")
)

// commentHelperBG 是浅色主题评论区里「引用 / 辅助块」的底色：取 Lobe 的
// FillTertiary（三级填充）叠在白色容器面板上，得到不透明的淡灰。Markdown
// 引用、代码与表格、楼中楼的父评论引用行都用它，与正文区分。深色主题不
// 写死这个颜色，沿用 Markdown 默认主题与透明底。
var commentHelperBG = func() ui.Color {
	tk := lobeCfg.Tokens(false)
	return tk.FillTertiary.Over(tk.BgContainer)
}()

// commentHelperBlockBg 返回一条引用 / 辅助块在当前主题下的底色：浅色用
// Lobe FillTertiary 派生的淡灰，深色保持透明（沿用主题原有的淡化处理）。
func commentHelperBlockBg(t *ui.Theme) ui.Color {
	if t.Dark {
		return ui.Transparent
	}
	return commentHelperBG
}

// replyLocateMaxPages 是从评论列表点进来、首屏没找到目标评论时最多自动
// 续拉的页数：30 条一页，20 页足够覆盖绝大多数帖子，又不会无限请求。
const replyLocateMaxPages = 20

// mdTextStyle 是一类 Markdown 视图的排版：基准字号、行高倍数与块间距（em）。
// 标题、代码、列表缩进等以 em 计的长度都按 size 换算，所以只描述这三项。
// imageMaxH 是这类视图的图片高度上限，单位为 DIP，0 表示不限制。
type mdTextStyle struct {
	size, line, block, item float32
	imageMaxH               float32
}

// 个人简介夹在资料卡里，比正文与评论都紧，也不跟随阅读排版缩放。正文与
// 评论的排版见 typography.go 的 mdStyles：字号与行距都按偏好现算。
var bioTextStyle = mdTextStyle{size: userCardBioFontSize, line: 1.6, block: 0.6}

// newMarkdownOptions 造一套 Markdown 渲染选项。颜色、字体等未写的字段由
// Markdown 默认主题按窗口外观补齐（见 markdown.Theme 的合并规则）。
func newMarkdownOptions(a *app, st mdTextStyle, mentions bool) *markdown.Options {
	return &markdown.Options{
		// Links to another 2Libra post are intercepted and shown in place;
		// only other http(s) links leave the app. javascript: is dropped.
		OnLink: a.handleLink,
		LoadImage: func(u string) (ui.ImageSource, error) {
			return a.loadImage(u)
		},
		// Markdown 默认只画整段图片，段落内的配图与 :code: 表情会只剩
		// [说明]；这类段落改由 article_render.go 接管。组图按解析好的文档
		// 现认（见 articlePictures），点开的预览弹窗正好翻这一份源文里的图：
		// 正文一份、每条评论各一份，互不掺和。
		Block:    a.articleBlockAt(st.size, st.imageMaxH, a.picturesForDoc, mentions),
		WrapCode: true,
		Theme: &markdown.Theme{
			FontSize:     st.size,
			LineHeight:   st.line,
			BlockSpacing: st.block,
			ItemSpacing:  st.item,
		},
	}
}

// newCommentMarkdownOptions 造评论正文的 Markdown 选项：与正文同一套渲染，
// 字号与行距由调用方按偏好传入。light 为真时把引用 / 代码 / 表格这些辅助块
// 统一成 commentHelperBG，并接管引用块的画法（见 commentQuoteBlock）；false
// 时深色主题照旧由 Markdown 默认主题按外观给色。两份一起重建，渲染时按窗口
// 外观二选一，避免复制带 sync.Map 的 Options。
func (a *app) newCommentMarkdownOptions(st mdTextStyle, light bool) *markdown.Options {
	o := newMarkdownOptions(a, st, true)
	if light {
		o.Theme.CodeBackground = commentHelperBG
		o.Theme.CodeBlockBackground = commentHelperBG
		o.Theme.TableHeader = commentHelperBG
		o.Theme.TableStripe = commentHelperBG
	}
	base := o.Block
	o.Block = func(c *ui.Context, d *parser.Document, i int) bool {
		if light {
			if b := d.Block(i); b.Kind == parser.Quote && b.Alert == parser.NoAlert {
				a.commentQuoteBlock(c, d, i, o)
				return true
			}
		}
		return base(c, d, i)
	}
	return o
}

// commentMdOptions 返回当前窗口外观该用的评论 Markdown 选项：浅色用铺了
// Lobe 派生辅助块底色的 commentMdLight，深色用原来的 commentMd。
func (a *app) commentMdOptions(c *ui.Context) *markdown.Options {
	return a.commentMdOptionsForDark(c.Theme().Dark)
}

func (a *app) commentMdOptionsForDark(dark bool) *markdown.Options {
	if dark {
		return a.commentMd
	}
	return a.commentMdLight
}

// commentQuoteBlock 把评论中的 Markdown 引用块画成 Lobe 派生淡灰底色的阅读块。
// 引用内文去掉每行开头的 > 之后用同一份浅色评论选项重新渲染，表情、配图与
// 嵌套引用因此仍按原样排版（嵌套引用会再走进这个 Block 回调）。o 即那份
// 浅色选项，复用它自己的图片缓存。
func (a *app) commentQuoteBlock(c *ui.Context, d *parser.Document, i int, o *markdown.Options) {
	b := d.Block(i)
	raw := d.Source()[b.Start:b.End]
	tk := lobetheme.TokensOf(c)
	q := ui.Column(c).Background(commentHelperBG).
		BorderStartWidth(3).BorderColor(tk.Border).Padding(6, 12)
	q.Children(func() {
		a.markdownView(c, unquoteSource(raw), o)
	})
}

// unquoteSource 去掉 Markdown 引用块每行开头的 > 前缀，供 commentQuoteBlock
// 把内文交给嵌套的 Markdown 视图重新排版。没有前缀的惰性续行原样保留。
func unquoteSource(src string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "> "):
			lines[i] = line[2:]
		case strings.HasPrefix(line, ">"):
			lines[i] = strings.TrimPrefix(line, ">")
		}
	}
	return strings.Join(lines, "\n")
}

// handleLink is the Markdown OnLink. A link to another post replaces the
// current detail view (inline), like following a link in a reader; the
// previous post stays on a small back stack. Self-links are ignored.
func (a *app) handleLink(raw string) {
	if it, ok := parseInAppTopicLink(raw); ok {
		if a.detail != nil && it.ShortID == a.detail.shortID {
			return
		}
		a.openDetail(it)
		return
	}
	a.openExternal(raw)
}

func (a *app) openExternal(raw string) {
	if raw == "" {
		return
	}
	u, err := url.Parse(raw)
	if err != nil {
		return
	}
	if u.Scheme == "" {
		if !strings.HasPrefix(raw, "/") {
			return
		}
		raw = siteBaseURL + raw
	} else if !ui.SafeToOpen(raw) {
		return
	}
	a.srv.OpenURL(raw) // ui.Services.OpenURL must run on the UI thread
}

// loadImage backs markdown images (including the :code: emoji map) with the
// global image cache: memory first, then the disk cache, then one http(s)-only
// download. Images here are post content, so they take the temporary cache
// that expires after imageCacheTTL.
func (a *app) loadImage(raw string) (ui.ImageSource, error) {
	return a.loadImageNow(raw, imageCacheTemporary)
}

func (a *app) fetchImage(raw string) ([]byte, error) {
	return fetchImageBytes(a.client.httpClient(), raw)
}

// fetchImageBytes 取一张图片的原始字节。hc 必须来自 proxyManager 的共享
// transport：图片也是出站请求，同样要走配置的代理。「我的」窗口预取账号头像
// 时用的是同一个函数。
//
// 请求地址先经 imageRequestURL 适配图片代理：2Libra 的 wsrv.nl 代理默认把
// gif 转成动画 WebP，而桌面端解不了动画 WebP，去掉转码参数才能拿到原图。
func fetchImageBytes(hc *http.Client, raw string) ([]byte, error) {
	if _, err := parseImageURL(raw); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageRequestURL(raw), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "2libra-native-reader/0.1 (+mygo)")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("图片 HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, imageMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > imageMaxBytes {
		return nil, fmt.Errorf("图片超过 %s 上限", formatBytes(imageMaxBytes))
	}
	return data, nil
}

// ensureAvatar 保证当前登录账号的头像始终在加载。账号头像长期挂在标题栏，
// 不受页面切换影响：只要还有地址就继续重试（见 prefetchPersistent），
// 地址为空表示用户没有头像，退回按昵称生成的图案。可以在每帧渲染时调用。
func (a *app) ensureAvatar() {
	raw := normalizeAvatarURL(a.auth.avatarURL())
	if raw == "" {
		return
	}
	if !a.autoLoad {
		return // headless 测试不碰网络
	}
	a.images.prefetchPersistent(raw, imageCacheStable, a.imageFetcher())
}

// openDetail 是打开一篇帖子的统一入口：有主窗口时开独立的详情窗口（同一篇
// 已开则只带前台），无头测试与最后一个窗口关闭后的兜底仍走内联页。
func (a *app) openDetail(it Item) {
	if it.ShortID == "" {
		return
	}
	if a.win != nil || len(a.detailWindows) > 0 {
		a.openDetailWindow(it)
		return
	}
	a.openDetailInline(it)
}

// newDetailState 只造状态，不加载：详情窗口先建好标签再填内容，避免两张
// 窗口同时打开时把结果发到错的游标上。
func (a *app) newDetailState(it Item) *detailState {
	return &detailState{
		mdEditor:     newMDEditor(),
		winPreview:   true,
		shortID:      it.ShortID,
		collID:       it.CollectionID,
		collName:     it.CollectionName,
		focusID:      it.CommentID,
		focusPending: it.CommentID != "",
		fallbackURL:  it.URL,
		replyPage:    1,
		flat:         it.CommentID != "",
		revealed:     make(map[string]bool),
		// 收起状态与 bodyWidth 都不继承：每次进来都是双栏展开。
		// 默认正文宽度在 view 里按窗口宽度取 40%，这样不同窗口下初始比例一致。
	}
}

// loadDetail 摆首屏缓存并发请求。所有网络回调都只认 d 本身（detailLive），
// 因此几篇帖子同时在加载也不会互相顶掉。
func (a *app) loadDetail(d *detailState, it Item) {
	// 先摆上缓存里的正文和第一页评论：网络慢或断开时第一帧就有内容。
	a.preloadDetailCache(d)
	// 加载评论楼层视图的本地记忆：折叠、停留位置与新评论基线，都按帖子记。
	// 从评论列表点进来要定位某条评论时不恢复折叠（展开全部便于定位）。
	if mem, ok := a.loadReplyView(it.ShortID); ok {
		d.viewMem = &mem
		d.readBase = mem.ReadAt
		d.stayIdx = mem.ScrollIdx
		d.stayID = mem.ReadToID
		if it.CommentID == "" && len(mem.Collapsed) > 0 {
			d.collapsed = make(map[string]bool, len(mem.Collapsed))
			for _, id := range mem.Collapsed {
				d.collapsed[id] = true
			}
		}
	}
	// 视图模式是本机偏好，不跟帖子走。
	if it.CommentID == "" {
		if flat, ok := a.loadReplyViewMode(); ok {
			d.flat = flat
		}
	}
	// 本地已读：点开即算读过，回到列表时这一行置灰。从最新评论等列表带
	// 着评论 id 进来时，那条评论也单独记一笔，只置灰它自己。
	a.markPostRead(it.ShortID)
	if it.CommentID != "" {
		a.markCommentRead(it.CommentID)
	}
	a.recordReading(it, d.topic)
	// 稍后再看：默认打开即从列表移除；开了「保留」则标成已读，留作待读清单。
	a.handleWatchLaterOpen(it.ShortID)
	a.status = ""
	// load* are no-ops when autoLoad is off, so headless UI tests never
	// touch the network.
	a.loadTopicFor(d)
	a.loadRepliesFor(d, 1)
	a.ensureEmojis()
}

// openDetailInline 是老的内联路径：主窗口页面切成 detail，保留 20 层返回栈。
// 无头测试、以及已经没有任何原生窗口时的兜底走这里。
func (a *app) openDetailInline(it Item) {
	// 过滤是「这篇帖子的临时视角」：换帖子、关详情时清空，不随账号落盘。
	clearAuthorFilter(a.detail)
	// 换帖子就把独立写评论窗口收起来：它绑的是上一份 detailState 的草稿，
	// 留着会让人以为写的是刚打开的这一篇。
	a.closeReplyWindow()
	// 上一页如果还在等 AI 分配节点，先把它的轮询停掉；内联返回同一篇时
	// closeDetail 会重新启动（见下方弹栈分支）。
	a.stopDetailNodeWatch(a.detail)
	if a.pageName == "detail" && a.detail != nil {
		// Inline navigation: keep the post we came from so "✕ 关闭" walks
		// back through the chain before dropping to the list.
		if len(a.detailStack) >= 20 {
			a.detailStack = a.detailStack[1:]
		}
		a.detailStack = append(a.detailStack, a.detail)
	} else {
		a.detailStack = nil
	}
	a.detail = a.newDetailState(it)
	a.loadDetail(a.detail, it)
	a.pageName = "detail"
}

func (a *app) closeDetail() {
	if h := a.detailHostOf(a.detail); h != nil {
		// 独立窗口里「关闭」只作用于当前标签：还有别的帖子时窗口留着。
		a.closeDetailTab(h, h.active)
		return
	}
	a.status = ""
	// 离开评论区时独立写评论窗口也一起收起：草稿还留在那个 detailState
	// 里，回到这篇帖子时会原样出现。
	a.closeReplyWindow()
	// 离开评论区前把浏览状态（视图、停留位置、折叠、新评论基线）落盘，下次
	// 回到同一篇时沿用。inline 跳转（压栈）和真正回到列表都会走到这里。
	if a.detail != nil {
		// 过滤后的行号不能写进浏览记忆：退出时先回到原列表再落盘。
		clearAuthorFilter(a.detail)
		a.stopDetailNodeWatch(a.detail)
		a.saveReplyView(a.detail)
	}
	if n := len(a.detailStack); n > 0 {
		a.detail = a.detailStack[n-1]
		a.detailStack = a.detailStack[:n-1]
		// 重新进入一篇帖子时过滤不保留（切走时就该清掉，这里兜一层）。
		clearAuthorFilter(a.detail)
		// 返回的这一页可能还在等 AI 分配节点：重新开始轮询（离开时停掉了）。
		a.watchPendingNode(a.detail)
		// 退回上一篇同样是「进入详情」：双栏重新展开，收起状态不跨页面保留。
		a.detail.bodyCollapsed = false
		a.detail.replyCollapsed = false
		return
	}
	a.detail = nil
	a.pageName = "list"
}

func (a *app) loadTopic() { a.loadTopicFor(a.detail) }

func (a *app) loadTopicFor(d *detailState) {
	if d == nil || d.topicLoading || !a.autoLoad {
		return
	}
	d.topicLoading = true
	d.topicErr = ""
	a.reqSeq++
	gen := a.reqSeq
	d.topicGen = gen
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		topic, err := a.client.fetchTopic(ctx, d.shortID)
		a.publish(func() {
			if !a.detailLive(d) || d.topicGen != gen {
				return
			}
			d.topicLoading = false
			if err != nil {
				a.handleAuthError(err)
				// 已有缓存正文时不要把整页换成错误：继续显示缓存，只在顶部
				// 说明这一份是本地内容。
				if d.topic != nil {
					a.markDetailOffline(d)
					return
				}
				d.topicErr = friendlyError(err)
				return
			}
			d.topic = &topic
			d.fromCache = false
			d.cacheNote = ""
			// 不选节点发的帖子会先停在星云：显示「节点寻找中…」，并在后台
			// 轮询到服务端分配好为止（见 topic_node.go）。已经确定节点的
			// 帖子要停掉可能还在跑的旧轮询。
			if topic.pendingNode() {
				a.watchPendingNode(d)
			} else {
				a.stopDetailNodeWatch(d)
			}
			a.prefetchAvatars(topic.Avatar)
			// 收藏状态随详情下发（带上令牌才有意义）；交给作者行的收藏按钮。
			syncFavorite(d, topic, a.auth.username())
			// 投票选项可能随刷新变化，勾选框的行数跟着对齐。
			syncVote(d, topic)
			// 投过票的人直接看分布，没投过的人仍要点「查看投票结果」才看得到。
			// 详情带令牌时才会有「我已投」，匿名加载不动。
			if topic.Vote != nil && topic.Vote.VotedKnown && topic.Vote.Voted {
				d.vote.voted = true
				a.showVoteResult(d)
			}
			// 竞猜选项若已变化，收起指向旧选项的投注面板。
			syncBets(d, topic)
			// 池子没了 / 结束了，收起注资气泡。
			syncRewardPool(d, &topic)
			// 受限内容 / 竞拍：正文拿到且确实有门槛就自动把三块只读内容拉回来
			// 直接展示，不必再点「查看内容 / 竞拍状态 / 出价记录」。自动模式未
			// 登录不发请求、缺 scope 不弹授权，失败原因留在块里可重试（见
			// restricted.go 的 autoLoadRestricted）。
			a.autoLoadRestricted(d)
			// 正文拿到后写一份本机缓存，并补全阅读记录里打开时还缺的字段。
			a.updateReadingTopic(d.shortID, &topic)
			a.updateWatchLaterTopic(d.shortID, &topic)
			a.saveCachedDetail(d)
			// 正文带了合集归属就顺手读一次合集内帖子：条数与「上一篇」都
			// 从这里来，没带则正文不出现合集卡片。
			a.ensureDetailCollectionNav(d)
		})
	}()
}

func (a *app) loadReplies(page int) { a.loadRepliesFor(a.detail, page) }

func (a *app) loadRepliesFor(d *detailState, page int) {
	if d == nil || d.replyLoading || page < 1 || !a.autoLoad {
		return
	}
	d.replyLoading = true
	d.replyErr = ""
	d.replyMoreErr = ""
	d.replyPage = page
	d.replyBlocked = false
	a.reqSeq++
	gen := a.reqSeq
	d.replyGen = gen
	flat := d.flat
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetchReplies(ctx, d.shortID, page, flat)
		a.publish(func() {
			if !a.detailLive(d) || d.replyGen != gen {
				return
			}
			d.replyLoading = false
			if err != nil {
				// 缺权限时不只把错误画在页面上：全局授权弹窗会带用户去补权限，
				// 回调成功后重发这一页。
				a.handleAuthError(err)
				if page > 1 && len(d.replyList) > 0 {
					// 已经有评论时续拉失败：保留列表，在底部提示并可重试。
					d.replyMoreErr = friendlyError(err)
				} else if page == 1 && len(d.replyList) > 0 {
					// 第一页失败但缓存里已经有评论：继续显示缓存评论，顶部
					// 说明当前是本地内容，不把评论区换成错误页。
					a.markDetailOffline(d)
				} else {
					d.replyErr = friendlyError(err)
				}
				return
			}
			d.replyMoreErr = ""
			d.replyBlocked = res.Blocked
			rows := flattenReplies(res)
			if page == 1 {
				d.replyList = rows
				d.rebuildDisplay()
				d.replyNoMore = len(rows) == 0
				// 一条评论都没有时评论区只是一行「暂无评论」，白占掉右栏：
				// 默认收起它，把整行让给正文。加载失败不走这里，错误与重试
				// 入口要留在屏幕上；从通知 / 评论链接定位进来也不收，避免
				// 和默认的「收起正文」叠在一起，两边都没内容可看。
				if d.replyNoMore && !d.replyBlocked && !d.bodyCollapsed && d.focusID == "" {
					d.replyCollapsed = true
				}
				// 恢复本地记忆的停留位置：没有定位目标时滚回上次读到的行；
				// 有定位目标（从评论列表点进来）时交给下面的 focus 逻辑，
				// 此时不恢复折叠、按扁平序展开全部，便于定位。
				if d.focusID != "" {
					d.list.ScrollTo(0, ui.Start)
				} else if d.viewMem != nil && d.stayIdx > 0 && d.stayIdx < len(d.display) {
					d.list.ScrollTo(d.stayIdx, ui.Start)
				} else {
					d.list.ScrollTo(0, ui.Start)
				}
			} else {
				seen := make(map[string]bool, len(d.replyList))
				for _, r := range d.replyList {
					seen[r.ID] = true
				}
				added := 0
				for _, r := range rows {
					if !seen[r.ID] {
						d.replyList = append(d.replyList, r)
						seen[r.ID] = true
						added++
					}
				}
				// 服务端返回空页或没有新增条目：标记到底，避免无限加载。
				d.replyNoMore = added == 0
				d.rebuildDisplay()
			}
			d.replyTotal = res.Total
			d.replyTotalPages = res.TotalPages
			d.replyPage = page
			d.replyLoaded = true
			// 评论作者头像随评论一起预取，列表行第一帧就能画（没到则先画
			// 生成的图案）。
			a.prefetchReplyAvatars(rows)
			// 定位目标评论：找到就滚过去。目标不在首屏时继续自动续拉，
			// 直到找到、到底或用完预算；用户手动续拉时 focusPending 已
			// 经清掉，不会把用户刚滚到的位置拽走。
			at := -1
			if d.focusID != "" {
				at = replyIndexByID(d.display, d.focusID)
			}
			if at >= 0 {
				d.list.ScrollTo(at, ui.Start)
				d.focusPending = false
				d.focusMiss = false
			} else if d.focusPending {
				hasMore := d.replyTotalPages == 0 || page < d.replyTotalPages
				if d.focusTries < replyLocateMaxPages && hasMore && !d.replyNoMore {
					d.focusTries++
					a.loadReplies(page + 1)
				} else {
					d.focusPending = false
					d.focusMiss = true
				}
			}
			if page == 1 {
				// 只缓存第一页评论；续拉的页不写回，避免缓存无限增长。
				a.saveCachedDetail(d)
			}
		})
	}()
}

// refreshTopic 是正文区「刷新」：只重拉当前帖子的正文，评论区留在原处。
// 旧正文在请求回来之前保持显示（loadTopic 只在成功时替换），长帖点刷新
// 不会先闪成空白；失败仍走离线那条路（顶部说明这份是本地内容）。
func (a *app) refreshTopic() {
	d := a.detail
	if d == nil {
		return
	}
	d.topicErr = ""
	if d.topicLoading || !a.autoLoad {
		return
	}
	a.loadTopic()
}

// refreshReplies 是评论区「刷新」：只重拉第一页评论，正文不动。续拉也从
// 第 1 页重新开始；旧评论在请求回来之前保持显示，不会先清空。
func (a *app) refreshReplies() {
	d := a.detail
	if d == nil {
		return
	}
	d.replyErr = ""
	d.replyMoreErr = ""
	d.replyNoMore = false
	// 刷新保留过滤（只是重拉第一页），续拉预算从头算。
	d.filterAutoPages = 0
	if d.replyLoading || !a.autoLoad {
		return
	}
	a.loadReplies(1)
}

// /v1/emojis changes very rarely: one request per app run is enough, as the
// docs recommend. A failure leaves the short codes verbatim.
func (a *app) ensureEmojis() {
	if a.emojiLoading || a.emojiTried || !a.autoLoad {
		return
	}
	a.emojiLoading = true
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		m, err := a.client.fetchEmojis(ctx)
		a.publish(func() {
			a.emojiLoading = false
			if err != nil {
				// 失败不锁死：启动时的预取可能碰上网络还没好，下次打开详情
				// 会再试一次，正文不必在整个运行期都显示 :code: 短码。
				a.emojiTried = false
				// 表情表也可能因权限被拒；授权成功后重试一次。
				a.handleAuthError(err)
				a.status = "表情表加载失败，正文短码原样显示"
				return
			}
			a.emojiTried = true
			a.emojis = m
		})
	}()
}

// detailView 是详情页的顶层画法。inline=true 时还画主窗口标题栏与 Esc 返回
// 列表；详情窗口传 false，标题栏、Esc 与标签栏由窗口自己管。
func (a *app) detailView(c *ui.Context, inline bool) {
	defer a.draftTick()
	if inline {
		// Esc 与右上角关闭同义：内联浏览时先退一层，再回到列表。
		c.OnShortcut(0, ui.KeyEscape, func() { a.closeDetail() })
		a.titleBar(c)
		ui.Divider(c)
	}
	d := a.detail
	if d == nil {
		ui.Column(c).Fill().Center().Gap(12).Children(func() {
			ui.Text(c, "没有打开的帖子").TextColor(c.Theme().TextMuted)
			if ui.Button(c, "← 返回列表").Clicked() {
				a.pageName = "list"
			}
		})
		return
	}
	a.detailContent(c, d)
	a.flushSeenNotifications()
}

// detailBodyMinWidth 是正文栏缩放后保留的最小宽度（DIP），与 DraggablePanel
// 的 MinSize 共用，免得窗口缩小时接缝被挤进正文里。
const detailBodyMinWidth float32 = 200

// detailBottomBarHeight 是左右两栏底部条带的总高度（DIP，含各自 1px 分隔线）：
// 左栏是正文底部的表态 / 打赏互动栏，右栏是折叠态的评论输入框。两栏共用同一
// 个高度，默认视图里底边齐平；评论框展开后自然往下长，不再受这个值约束。
// 条带只放一枚图标与一行摘要，收紧到 48 让正文和评论多出一点阅读空间。
const detailBottomBarHeight float32 = 48

// replyComposerPadY 是评论区输入栏外层的上下内边距。折叠态的输入框据此扣掉
// 外层留白与分隔线，让整条底部条带正好是 detailBottomBarHeight。
const replyComposerPadY float32 = 4

// detailContent 是详情页真正的内容：左正文、右评论，底部来源与正文右下角的操作组。
// 显式收 d，让两扇详情窗口各画各的那篇，不靠游标在帧与帧之间保持一致。
func (a *app) detailContent(c *ui.Context, d *detailState) {
	// 主题已由窗口的 view 按外观模式设好，这里直接取用，避免重复解析。
	t := c.Theme()
	tk := lobetheme.TokensOf(c)
	ui.Column(c).Grow(1).FillWidth().Children(func() {
		w, _ := c.Size()
		if w > 0 {
			switch {
			case d.layoutWidth <= 0:
				// 首次进入按窗口宽度给一个 40% 的初始宽度：正文读起来是窄栏，
				// 评论区拿剩下的 60% 展开评论树。
				d.bodyWidth = (w - 40) * 0.40
				d.layoutWidth = w
			case w != d.layoutWidth:
				// 窗口尺寸变了（拉边框、最大化、缩放或合窗）：按前后宽度比
				// 缩放正文，正文和评论的分栏比例跟着窗口走，用户拖过接缝
				// 定下的比例也一样保留。一帧可能重建多次，记下新宽度后
				// 后续重建拿到同一个 w，不会重复缩放。
				d.bodyWidth *= w / d.layoutWidth
				d.layoutWidth = w
			}
			// 正文最多占 80% 宽度，给评论区至少留一条能读的列；同时保住
			// 接缝能继续拖动的最小宽度。
			if maxW := w * 0.8; d.bodyWidth > maxW {
				d.bodyWidth = maxW
			}
			if d.bodyWidth < detailBodyMinWidth && w*0.8 >= detailBodyMinWidth {
				d.bodyWidth = detailBodyMinWidth
			}
		} else if d.bodyWidth <= 0 {
			// 还没有窗口尺寸时给个兜底，拿到尺寸后再按 40% 重算。
			d.bodyWidth = 420
		}
		// 正文可滚动（Grow(1) 吃满面板剩余高度，MinHeight(0) 让它收缩到比
		// 内容更矮，正文在框内滚动）；作者信息收在标题下方，底部固定的是
		// 表态 / 打赏互动栏。面板内部铺一层 Lobe 表面，靠背景色与评论区
		// 区分，不再画一圈描边。收起哪一边都复用同一份正文。
		buildBody := func() ui.Element {
			return ui.Column(c).FillWidth().FillHeight().MinHeight(0).
				Radius(tk.BorderRadiusLG).Background(tk.BgContainer).Children(func() {
				ui.Scroll(c).Grow(1).FillWidth().MinHeight(0).Padding(20, 28).Gap(14).
					TrackScroll(&d.bodyScroll).Children(func() {
					a.topicBody(c, d)
				})
				a.topicInteractionBar(c, d)
				// 右栏评论区收起后，评论输入框没有地方可待：把它挪到左栏正文
				// 底部、表态互动栏下方继续展示，这样收起评论也能直接写回复。
				// 两种布局互斥，同一帧只会画一份输入框，composerField 的焦点
				// 身份仍只有一处（见 replyInput）。
				if d.replyCollapsed {
					a.replyComposer(c, d)
				}
				// 页面操作贴在正文面板右下角，悬浮在正文上方，不占高度。
				a.detailToolBar(c, d)
			})
		}
		// 评论区用一块底色和白色正文面板分出层次；收起正文时它吃掉整行。
		// 浅色下做成平角的阅读面板，深色沿用原来的圆角卡片。
		replyRadius, replyBg := detailReplyStyle(t, tk)
		ui.Row(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
			if d.replyCollapsed {
				// 右栏已收起：正文占满整行，右边缘留一条窄栏把评论区拉回来。
				// 这里必须 FillHeight：ui.Row 默认把子元素垂直居中，不撑满就会
				// 让正文整块悬在中间（和双栏时的顶部对齐不一致）。撑满后正文
				// 照旧从顶部往下排，收起前后位置不跳。
				ui.Column(c).Grow(1).MinWidth(0).FillHeight().MinHeight(0).Children(func() { buildBody() })
				// 正文铺满时没有接缝可依，箭头改贴在右上角，不再悬在半空
				// 垂直居中。
				detailExpandToggle(c, tk, true, true, "展开评论", "展开评论区，恢复双栏", chevronLeftIcon,
					func() { d.replyCollapsed = false })
				return
			}
			if d.bodyCollapsed {
				// 左栏已收起：评论区占满整行，左边缘的窄栏把正文拉回来。
				replyCol := ui.Column(c).Grow(1).MinWidth(0).FillHeight().MinHeight(0).
					Radius(replyRadius).Background(replyBg).Label("评论区")
				replyCol.Children(func() { a.replyPane(c, d) })
				detailExpandToggle(c, tk, false, false, "展开正文", "展开正文，恢复双栏", chevronRightIcon,
					func() { d.bodyCollapsed = false })
				return
			}
			// 双栏展开：正文仍是 Lobe 的可拖拽面板，接缝留在两栏之间，拖动照旧
			// 调宽。收起入口不用 Lobe 自带那一枚（挂在面板一侧、只跟着正文
			// 显隐）；这里自己画对称的一对，都以接缝为中心，悬浮任意一栏两枚
			// 一起出现，左右都好点。
			expanded := true
			var bodyHost ui.Element
			layout.DraggablePanel(c, &d.bodyWidth, &expanded, layout.DraggablePanelProps{
				Placement:         layout.Left,
				MinSize:           detailBodyMinWidth,
				CollapseThreshold: 120,
				NoToggle:          true,
			}, func() { bodyHost = buildBody() })
			// 拖到阈值以下 Lobe 会自己收起面板，这里同步回页面状态。
			if !expanded {
				d.bodyCollapsed = true
			}

			replyCol := ui.Column(c).Grow(1).MinWidth(0).FillHeight().MinHeight(0).
				Radius(replyRadius).Background(replyBg).Label("评论区")
			replyCol.Children(func() { a.replyPane(c, d) })
			// 收起按钮放在跨接缝的定位盒里：两枚各占接缝左右半边，都落在盒子
			// 的可点击范围内。ZIndex 抬高到可拖拽面板之上，免得被它盖住。
			seam := ui.Box(c).Absolute().Left(d.bodyWidth-20).TopPercent(50).
				TranslatePercent(0, -50).Size(40, 40).ZIndex(2)
			seam.Children(func() {
				detailSplitToggles(c, d, tk, bodyHost, replyCol, seam)
			})
		})
		// 数据来源声明 + 可点击的原文链接占满整个窗口底部，放在最底部。
		// 进入详情就画出来：正文加载完成后高度不变，卡片区域不会跳。
		a.topicSourceFooter(c, d)
		// 正文收起时整列操作组跟着正文面板一起隐藏：正文已经不在画面上，这
		// 一列本就属于它；再画到内容区右下角只会压住评论区最下方那条评论的
		// 回复入口（评论区自己的操作组已移到输入框发送右侧，见 replyComposerToolBar）。

		// 「关闭 / 开启评论」的确认框：作者点了操作组里的图标才展开，盖在整页上。
		a.commentsStatusDialog(c, d)
		// 兑换码的领取确认框：点一条未领取的码才展开，领到后切到成功页看完整码。
		a.redeemDialog(c, d)
		// 打赏、表态与明细都是贴着入口的 Lobe Popover（见 interact.go）；
		// 合集、注资、竞猜与解锁确认是盖在整页上的 Lobe Modal（见 collections.go /
		// reward_pool.go / bets.go / restricted_ui.go）。
		a.collectionDialog(c, d)
		a.rewardPoolDialog(c, d)
		a.betDialog(c, d)
		a.restrictedUnlockDialog(c, d)
		// 抽奖确认框：帖主点「发起抽奖」后展开，选人数、确认后才发触发评论。
		a.lotteryDialog(c, d)
	})
}

var detailSeamFade = ui.Animation{Duration: 160 * time.Millisecond, Ease: motion.EaseOut}

// 接缝箭头只是提示，不该像主按钮那么抢眼：悬浮时也只淡入到半透明。
const detailSeamArrowOpacity float32 = 0.55

// 正文铺满时，「展开评论」贴在右上角固定高度，不再按接缝的逻辑垂直居中。
const detailExpandTop float32 = 56

// detailSplitToggles 在两栏接缝上画对称的一对收起按钮：左半边收起正文、
// 右半边收起评论，两枚都以接缝为中心。悬浮任意一栏或按钮时两枚一起淡入，
// 不会藏掉其中一边让人找不到。
func detailSplitToggles(c *ui.Context, d *detailState, tk *lobetheme.Tokens, bodyHost, replyHost, seam ui.Element) {
	shown := float32(0)
	left := ui.ButtonBase(c).Label("收起正文").Tooltip("收起正文，评论区占满整行").
		Absolute().Left(0).Top(0).Size(20, 40).Cursor(ui.CursorPointer).
		Background(tk.BgContainer).Border(1, tk.BorderSecondary)
	right := ui.ButtonBase(c).Label("收起评论").Tooltip("收起评论区，正文占满整行").
		Absolute().Left(20).Top(0).Size(20, 40).Cursor(ui.CursorPointer).
		Background(tk.BgContainer).Border(1, tk.BorderSecondary)
	if bodyHost.Hovered() || replyHost.Hovered() || seam.Hovered() ||
		left.Hovered() || right.Hovered() || left.FocusVisible() || right.FocusVisible() {
		shown = 1
	}
	target := shown * detailSeamArrowOpacity
	left.Opacity(left.AnimateTo("shown", target, detailSeamFade))
	right.Opacity(right.AnimateTo("shown", target, detailSeamFade))
	leftInk, rightInk := tk.TextTertiary, tk.TextTertiary
	if left.Hovered() {
		leftInk = tk.TextSecondary
	}
	if right.Hovered() {
		rightInk = tk.TextSecondary
	}
	left.Children(func() { ui.Icon(c, chevronLeftIcon).FontSize(12).TextColor(leftInk) })
	right.Children(func() { ui.Icon(c, chevronRightIcon).FontSize(12).TextColor(rightInk) })
	if left.Clicked() {
		d.bodyCollapsed = true
	}
	if right.Clicked() {
		d.replyCollapsed = true
	}
}

// detailExpandToggle 在一栏收起后，于内容区对应边缘留一条窄栏放展开按钮：
// atRight 为真贴右边缘（展开评论），否则贴左边缘（展开正文）。atTop 为真时
// 贴顶部固定位置，否则垂直居中；正文铺满时没有接缝，箭头不宜悬在半空。
// restore 由调用方决定清掉哪一份收起状态。
func detailExpandToggle(c *ui.Context, tk *lobetheme.Tokens, atRight, atTop bool, label, tip string, icon *ui.SVG, restore func()) {
	b := ui.ButtonBase(c).Label(label).Tooltip(tip).
		Absolute().Size(16, 40).Radius(8).Cursor(ui.CursorPointer).
		Background(tk.BgContainer).Border(1, tk.BorderSecondary)
	if atTop {
		b.Top(detailExpandTop)
	} else {
		b.TopPercent(50).TranslatePercent(0, -50)
	}
	if atRight {
		b.Right(0)
	} else {
		b.Left(0)
	}
	glyph := tk.TextTertiary
	if b.Hovered() {
		glyph = tk.TextSecondary
	}
	b.Children(func() { ui.Icon(c, icon).FontSize(12).TextColor(glyph) })
	if b.Clicked() {
		restore()
	}
}

// detailReplyStyle 返回评论区面板的圆角与底色。
//
// 浅色主题去掉圆角，底色直接用 Lobe 的 BgContainer（#ffffff），与正文面板
// 完全一致——两栏不再靠底色区分，评论区里的引用 / 辅助块改用 Lobe 的
// FillTertiary 铺出层次（见 commentHelperBG）。深色主题沿用原来的做法：圆角卡片 + 页面
// 底色混一点文字色得到的深灰。
func detailReplyStyle(t *ui.Theme, tk *lobetheme.Tokens) (float32, ui.Color) {
	if t.Dark {
		return tk.BorderRadiusLG, t.Background.Mix(t.Text, 0.05)
	}
	return 0, tk.BgContainer
}

// detailToolKind 决定一枚页面操作交给哪支画法。
type detailToolKind int

const (
	detailToolPlain    detailToolKind = iota // 普通图标，见 detailToolButton
	detailToolCopy                           // 复制链接要拿构建期的 Context
	detailToolRefresh                        // 在途时换成转圈并禁用
	detailToolComments                       // 评论开关有自己的忙碌态
)

// detailToolAction 是正文区竖排操作组里的一枚动作。操作组与详情窗口的自定义
// 标题栏共用这一份清单，条件只写一遍：常驻只剩一枚且没有「更多」动作时收进
// 标题栏（见 detailWindowTitleBar），其余画成正文区右下角的竖排操作组。
type detailToolAction struct {
	key     string // ActionIconGroup 用它在点击那一帧认出被点的动作
	kind    detailToolKind
	icon    *ui.SVG
	label   string
	tip     string
	href    string
	onClick func()
	busy    bool
	more    bool // 不常用动作：收进操作组末尾的「更多」菜单，不占常驻图标位
}

// detailToolSplit 把清单拆成常驻图标与「更多」菜单两组。返回值都保持原来的
// 顺序，常驻那组按界面顺序竖排，更多那组按同样顺序进菜单。
func detailToolSplit(acts []detailToolAction) (items, more []detailToolAction) {
	for _, act := range acts {
		if act.more {
			more = append(more, act)
		} else {
			items = append(items, act)
		}
	}
	return
}

// detailToolActions 按界面顺序列出此刻该出现的动作。
func (a *app) detailToolActions(d *detailState) []detailToolAction {
	acts := make([]detailToolAction, 0, 8)
	// 复制链接常驻：分享帖子是高频动作，不进「更多」。
	if href := d.originURL(); href != "" {
		acts = append(acts, detailToolAction{
			key: "copy", kind: detailToolCopy, icon: lucide.Copy(),
			label: "复制帖子链接", tip: "把这篇帖子的链接复制到剪贴板", href: href,
		})
	}
	// 只在正文滚离顶部时出现，且只管正文：评论区有自己的一键回顶。正文
	// 展开时操作组常驻右下角可达，不会像旧版页头那样为了放它固定占掉一行；
	// 正文收起时整组都不画，自然也没有正文的回顶。
	if !d.bodyCollapsed && d.bodyScroll.Y > 0 {
		acts = append(acts, detailToolAction{
			key: "top", icon: lucide.ArrowUp(), label: "返回顶部", tip: "正文回到顶部",
			// 直接把偏移写回 ScrollState 就是立即回顶（ScrollTo 会走动画）。
			onClick: func() {
				d.bodyScroll.X, d.bodyScroll.Y = 0, 0
			},
		})
	}
	// 作者的入口：改标题 / 正文看 can_edit，改节点看 can_edit_node，两个窗口
	// 各自计时、各自关闭；追加附言与关闭评论不受窗口影响，只看作者身份。
	if a.canEditTopic(d) {
		acts = append(acts, detailToolAction{
			key: "edit", icon: lucide.PenLine(), label: "编辑帖子", tip: "修改标题与正文",
			onClick: func() { a.showTopicEdit(d) }, more: true,
		})
	}
	if a.isPostAuthor(d) {
		acts = append(acts, detailToolAction{
			key: "postscript", icon: lucide.FilePlus2(), label: "追加附言", tip: "在帖子末尾追加一段附言",
			onClick: func() { a.showPostscript(d) }, more: true,
		})
	}
	// 抽奖只给帖主：点开先确认（人数可调），确认后才发触发评论。已经开过奖
	// 的帖子把同一入口改成「查看开奖结果」，只报结果不再发起。
	if a.canLottery(d) {
		icon, label, tip := lotteryIcon, "发起抽奖", "以评论触发抽奖助手，随机抽取中奖者"
		if d.topic != nil && d.topic.Lottery != nil {
			label, tip = "查看开奖结果", "这篇帖子已经开过奖"
		}
		if d.lottery.busy {
			label, tip = "正在抽奖", "开奖进行中…"
		}
		acts = append(acts, detailToolAction{
			key: "lottery", icon: icon, label: label, tip: tip, busy: d.lottery.busy, more: true,
			onClick: func() { a.askLottery(d) },
		})
	}
	if a.canEditNode(d) {
		acts = append(acts, detailToolAction{
			key: "node", icon: lucide.FolderTree(), label: "修改节点", tip: "更换帖子所属节点",
			onClick: func() { a.showTopicNodeEdit(d) }, more: true,
		})
	}
	if d.topic != nil && a.canToggleComments(d) {
		icon, label, tip := lucide.MessageSquareOff(), "关闭评论", "关闭评论：关闭后其他读者不能再回复"
		if d.topic.CommentsClosed {
			icon, label, tip = lucide.MessageCircle(), "开启评论", "重新开启这篇帖子的评论"
		}
		busy := d.comments.busy
		if busy {
			label, tip = "正在提交评论开关", "正在提交…"
		}
		acts = append(acts, detailToolAction{
			key: "comments", kind: detailToolComments, icon: icon, label: label, tip: tip, busy: busy, more: true,
		})
	}
	// 屏蔽作者：不是自己、且作者名已知时才给入口。已屏蔽时同一项变成
	// 「取消屏蔽」，与资料卡的按钮共用 setUserBlocked 写路径。
	if d.topic != nil && d.topic.Author != "" && !a.isMine(d.topic.Author) {
		blocked := a.filterBlocksUser(d.topic.Author)
		icon, label, tip := lucide.EyeOff(), "屏蔽作者", "不再显示这个人的帖子和评论"
		if blocked {
			icon, label, tip = lucide.Eye(), "取消屏蔽", "取消对这个作者的屏蔽"
		}
		author := d.topic.Author
		acts = append(acts, detailToolAction{
			key: "block-author", icon: icon, label: label, tip: tip, more: true,
			onClick: func() { a.setUserBlocked(author, !blocked) },
		})
	}
	loading := d.topicLoading
	tip := "重新加载正文"
	if loading {
		tip = "正在刷新…"
	}
	acts = append(acts, detailToolAction{
		key: "refresh", kind: detailToolRefresh, icon: lucide.RefreshCw(), label: "刷新", tip: tip, busy: loading,
	})
	// 独立详情窗口的自定义标题栏已经有「关闭」，不再悬浮一枚重复的关闭按钮。
	if d.host == nil {
		acts = append(acts, detailToolAction{
			key: "close", icon: lucide.X(), label: "关闭详情", tip: "关闭详情（Esc）", onClick: a.closeDetail, more: true,
		})
	}
	return acts
}

// detailToolGroupInset 是竖排操作组离正文面板右下角的留白。
const detailToolGroupInset float32 = 14

// replyComposerToolReserve 是评论区操作组嵌到发送按钮右侧后，输入框文字
// 右侧额外让出的宽度（DIP）。四枚 Small 图标连间隙约 114，留一点余量；
// 不预留的话最后一个字会钻到图标底下。
const replyComposerToolReserve float32 = 120

// replyJumpExtraReserve 是回顶与「跳到最新回复」同屏时，输入框文字为
// 第五枚 Small 图标再多让出的宽度。
const replyJumpExtraReserve float32 = 28

// detailToolBar 把页面操作收成正文区右下角的一列 Lobe ActionIconGroup：竖排、
// 绝对定位在正文面板内，不占正文高度，也不随正文滚走。作者专属的改帖 / 附言 /
// 评论开关和「关闭详情」不常用，走组件自带的 More 菜单（见 ActionIconGroup 的
// Menu 字段），常驻列只留返回顶部、复制链接与刷新，避免右下角图标堆成一串。
// 只在正文面板画出来时调用：正文收起后整列一起隐藏，不会退到评论区右下角压住
// 最下方评论的回复入口（见 detailContent）。
func (a *app) detailToolBar(c *ui.Context, d *detailState) {
	acts := a.detailToolActions(d)
	icons, more := detailToolSplit(acts)
	// 没有动作时连空组也不画。常驻只剩一枚且没有「更多」动作时交给详情窗口
	// 标题栏；还有「更多」动作就得保留这一列，否则那些入口没地方放。
	if len(icons) == 0 && len(more) == 0 {
		return
	}
	if d.host != nil && len(icons) == 1 && len(more) == 0 {
		return
	}
	items := make([]action.ActionGroupItem, 0, len(icons))
	for _, act := range icons {
		items = append(items, action.ActionGroupItem{
			Key: act.key, Icon: act.icon, Label: act.label, Disabled: act.busy,
		})
	}
	menu := make([]lobeoverlay.MenuItem, 0, len(more))
	for _, act := range more {
		menu = append(menu, lobeoverlay.MenuItem{
			Key: act.key, Icon: act.icon, Label: act.label, Disabled: act.busy,
		})
	}
	// 正文互动栏固定在面板底部，操作组贴在它上方；评论收起时正文底部
	// 还会多一条输入框，同样让开。
	bottom := detailToolGroupInset
	if a.topicInteractionVisible(d) {
		bottom += detailBottomBarHeight
	}
	if d.replyCollapsed {
		bottom += detailBottomBarHeight
	}
	host := ui.Box(c).Absolute().Right(detailToolGroupInset).Bottom(bottom).ZIndex(2)
	host.Children(func() {
		if key := action.ActionIconGroup(c, action.ActionIconGroupProps{
			Items: items, Menu: menu, Size: lobetheme.Small, Vertical: true,
			Variant: action.GroupOutlined, Shadow: true,
		}); key != "" {
			for _, act := range acts {
				if act.key == key {
					a.triggerDetailToolAction(c, d, act)
					break
				}
			}
		}
	})
}

// triggerDetailToolAction 执行竖排操作组里被点中的那一枚。忙碌的动作在组里
// 已经禁用，这里再挡一道，免得确认框弹出后重复提交。
func (a *app) triggerDetailToolAction(c *ui.Context, d *detailState, act detailToolAction) {
	if act.busy {
		return
	}
	switch act.kind {
	case detailToolCopy:
		a.copyToClipboard(c, act.href, a.detailToast(), "已复制链接")
	case detailToolRefresh:
		a.refreshTopic()
	case detailToolComments:
		a.askToggleComments(d)
	default:
		if act.onClick != nil {
			act.onClick()
		}
	}
}

// renderDetailToolAction 把清单里的一枚动作交给对应的画法（详情窗口标题栏
// 收下唯一一枚动作时用），按钮规范仍由各画法自己负责。
func (a *app) renderDetailToolAction(c *ui.Context, d *detailState, act detailToolAction) {
	switch act.kind {
	case detailToolCopy:
		a.detailCopyButton(c, act.href, act.label, act.tip)
	case detailToolRefresh:
		a.detailRefreshButton(c, d)
	case detailToolComments:
		a.commentsStatusButton(c, d)
	default:
		a.detailToolButton(c, act.icon, act.label, act.tip, act.onClick)
	}
}

// 标题栏里那枚动作的几何锁定值：30 的方形热区 + 17 的图标，与标题栏高度
// 对齐。正文区里的操作组改用 Lobe 的 ActionIconGroup（Small），不再用这两个数。
const (
	detailToolBox   float32 = 30
	detailToolGlyph float32 = 17
)

// detailActionIcon 是详情页标题栏那枚动作用的 Lobe 图标按钮：ActionIcon 管方块
// 尺寸、圆角、透明底、hover 浅底与键盘焦点，label 给读屏和点击测试，tip 说明
// 点了会发生什么。忙碌时退成同尺寸的禁用方块 + Lobe 转圈，请求在途不会连点。
func (a *app) detailActionIcon(c *ui.Context, icon *ui.SVG, label, tip string, busy bool, onClick func()) ui.Element {
	tk := lobetheme.TokensOf(c)
	if busy {
		b := ui.ButtonBase(c).Size(detailToolBox, detailToolBox).Center().Disabled(true).
			Radius(tk.BorderRadiusSM).Background(ui.Transparent).Border(1, ui.Transparent)
		b.Label(label).Tooltip(tip)
		b.Children(func() { lobeSpin(c, feedback.SpinProps{CustomSize: 15}) })
		return b
	}
	b := action.StyleActionIcon(c, ui.ButtonBase(c), icon, action.ActionIconProps{
		BlockSize: detailToolBox, GlyphSize: detailToolGlyph,
	})
	// Lobe 默认用 TextTertiary 画静止图标、TextSecondary 画悬停图标；这里把
	// 悬停提亮到完整的 Text，和正文里其它图标的层级保持一致。
	ink := tk.TextSecondary
	if b.Hovered() {
		ink = tk.Text
	}
	b.TextColor(ink).Label(label).Tooltip(tip)
	if onClick != nil {
		b.OnClick(onClick)
	}
	return b
}

// detailToolButton 是标题栏里那枚动作的纯图标按钮：Lobe 的 ActionIcon 给浅底与
// 圆角，label 供读屏与测试识别，tip 是悬停说明。
func (a *app) detailToolButton(c *ui.Context, icon *ui.SVG, label, tip string, onClick func()) {
	a.detailActionIcon(c, icon, label, tip, false, onClick)
}

// detailCopyButton 是标题栏里那枚「复制链接」：把 href 写进系统剪贴板，结果
// 由主窗口 toast 报告（成功或失败都提示，见 copyToClipboard）。悬停时图标
// 换强调色，和旁边的页面操作区分开。
func (a *app) detailCopyButton(c *ui.Context, href, label, tip string) {
	b := a.detailActionIcon(c, lucide.Copy(), label, tip, false, nil)
	// 悬停时图标换强调色，和旁边的页面操作区分开。
	if b.Hovered() {
		b.TextColor(lobetheme.TokensOf(c).Primary)
	}
	if b.Clicked() {
		a.copyToClipboard(c, href, a.detailToast(), "已复制链接")
	}
}

// copyToClipboard 把一条链接写进剪贴板，并在成功或失败时发一条 toast。
// 拷贝结果在下一帧之前才回来（见 ui.WriteClipboardThen），所以提示不能在
// 这里同步发出：先排队，等那个窗口重建时由 drainToasts 交给 Lobe 的 feedback.ShowToast。写失败
// 多见于别的程序正占着剪贴板，此时 toast 里说明改用手动复制，而不是静默。
// c 是发出这次复制的那一帧的 Context：剪贴板服务只在构建期取，不存进应用
// 状态（元素与 Context 都不跨帧保存）。
func (a *app) copyToClipboard(c *ui.Context, text string, target toastTarget, okText string) {
	if text == "" || c == nil {
		return
	}
	c.WriteClipboardThen(text, func(err error) {
		if err != nil {
			a.toast(target, "复制失败："+err.Error()+"，可以手动选中链接复制")
			return
		}
		a.toast(target, okText)
	})
}

// detailRefreshButton 是标题栏里那枚刷新按钮：请求在途时换成转圈并禁用，
// 避免连点重复发请求。它只重拉正文，与正文区操作组里的刷新是同一个动作。
func (a *app) detailRefreshButton(c *ui.Context, d *detailState) {
	loading := d.topicLoading
	tip := "重新加载正文"
	if loading {
		tip = "正在刷新…"
	}
	a.detailActionIcon(c, lucide.RefreshCw(), "刷新", tip, loading, a.refreshTopic)
}

func (a *app) topicBody(c *ui.Context, d *detailState) {
	t := c.Theme()
	tk := lobetheme.TokensOf(c)
	ts := a.typeSizes()
	// 评论定位、离线缓存与内联浏览层级原来占着页头的一整行；现在收进
	// 正文顶部，只在有话说时才占一点点高度。
	a.topicNotice(c, d)
	if d.topicLoading && d.topic == nil {
		lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small, Tip: "正在加载正文…"})
		return
	}
	if d.topicErr != "" {
		ui.Text(c, "正文加载失败").FontSize(ts.section).Bold()
		ui.Text(c, d.topicErr).TextColor(t.Danger).MaxLines(4)
		if ui.Button(c, "重试").Clicked() {
			a.loadTopic()
		}
		return
	}
	p := d.topic
	if p == nil {
		// 正文还没到时仍以帖子编号占位：内联跳转后能看出当前是哪一帖。
		ui.Text(c, "帖子 "+d.shortID).FontSize(ts.meta).Bold().SingleLine()
		ui.Text(c, "暂无正文").TextColor(t.TextMuted)
		return
	}
	// 标题、作者行与副标题合成一块「文首」：组内只留 8px，组外靠 Scroll
	// 的 14px 与正文分开，读者一眼就能分出标题区与正文。标题是整页唯一的
	// 大字；收起 / 展开入口已经移到两栏中间的接缝上，标题右侧不必再留位。
	ui.Column(c).FillWidth().Gap(12).Children(func() {
		if title := strings.TrimSpace(p.Title); title != "" {
			// 标题是整页唯一的重心，字号由设置页的「详情标题字号」控制；
			// 默认 24 DIP 与原来的 Lobe H3 字阶一致，配负字距收紧大字号。
			ui.Text(c, title).FontSize(a.titleSize()).Bold().
				LetterSpacing(-0.4).TextColor(tk.TextHeading).MaxLines(3)
		}
		// 作者行：头像、昵称、等级与徽章，收藏按钮贴在这一行的最右侧。
		// 它随正文一起滚动，不再单独占一条页头。
		a.topicAuthorLine(c, d)
		// 副标题：分类（两级）· 浏览数 · 置顶 / 评论已关闭，以及标签。
		// 作者信息已经在标题下方，这里不再重复。
		a.topicSubtitle(c, p)
	})
	// 无权帖：引擎按 11-19 口径把内容块全部置空，只留外壳。这里据此渲染
	// 访问墙，绝不从「正文为空」反推受限；标题 / 标签为空时上面的文首已经
	// 自然不画，不会露出一个空标题。
	if d.contentBlocked() {
		a.accessWall(c, d, p)
		return
	}
	// 副标题与正文之间不再画分隔线：这条线在正文顶端显得像多余的一条。
	// 帖子自己的 emojis 可能包含社区自定义图标；并到全局表情表后一起渲染。
	emojis := mergeEmojiMaps(a.emojis, p.Emojis)
	if strings.TrimSpace(p.Content) == "" {
		ui.Text(c, "（正文为空）").TextColor(t.TextMuted)
	} else {
		a.markdownView(c, replaceEmoji(p.Content, emojis, true), a.md)
	}
	for i, ps := range p.Postscripts {
		layout.Divider(c, layout.DividerProps{})
		label := fmt.Sprintf("附言 %d", i+1)
		if ps.CreatedAt != "" {
			label += " · " + relativeTime(ps.CreatedAt)
		}
		ui.Text(c, label).FontSize(ts.meta).Bold()
		postEmojis := mergeEmojiMaps(a.emojis, p.Emojis, ps.Emojis)
		a.markdownView(c, replaceEmoji(ps.Content, postEmojis, true), a.md)
	}
	// 帖子属于某个合集时才画合集卡片（见 collection_browse.go）。
	if _, _, known := d.collectionContext(); known {
		layout.Divider(c, layout.DividerProps{})
		a.detailCollectionCard(c, d)
	}
	if v := p.Vote; v != nil {
		layout.Divider(c, layout.DividerProps{})
		ui.Column(c).FillWidth().Padding(14, 16).Gap(8).Radius(tk.BorderRadius).
			Background(tk.FillQuaternary).Transition(ui.ElementTransition{
			Duration: 150 * time.Millisecond, Ease: motion.EaseOut, Colors: true,
		}).Children(func() {
			ui.Text(c, "投票 · "+v.Title).FontSize(tk.FontSizeLG).Bold().TextColor(tk.TextHeading)
			a.voteSection(c, d, v)
		})
	}
	if p.Bet != nil {
		a.betSection(c, d)
	}
	if p.Restricted != nil {
		a.restrictedSection(c, d)
	}
	if p.Redeem != nil {
		a.redeemSection(c, d)
	}
	if p.RewardPool != nil {
		a.rewardPoolSection(c, d, p.RewardPool)
	}
	// 正文末尾不再画互动行：表态与打赏固定在左栏底部的互动栏里
	// （见 detailView 的 topicInteractionBar），正文再长也始终可达。
}

// accessGuardGuardian 是守护者专属节点的门槛类型，与 compose.go 里的
// guardNone/login/level/attribute 并列；它只在读响应里出现（作者侧不能设）。
const accessGuardGuardian = "guardian"

// accessGuardReason 把 access_guard 的机器字段翻成两层中文说明：标题一行、
// 细节一行。文案只按 type / level / attr 生成，不解析服务端的中文 m。
// type 未知或缺失时给一个通用说法，始终让用户知道「为什么这里是空的」。
func accessGuardReason(p *topicDetail) (title, detail string) {
	switch strings.TrimSpace(p.GuardType) {
	case guardLogin:
		return "登录后可见", "这篇帖子仅对登录用户开放，登录后即可查看正文与互动。"
	case guardLevel:
		return fmt.Sprintf("达到 %d 级可见", p.GuardLevel),
			"这篇帖子设置了最低等级门槛，达到要求等级后即可查看。"
	case guardAttribute:
		attr := strings.TrimSpace(p.GuardAttr)
		if label := d20AttrLabel(attr); label != "" {
			attr = label
		} else if attr == "" {
			attr = "指定"
		}
		return fmt.Sprintf("%s 属性达到 %d 可见", attr, p.GuardAttrValue),
			"这篇帖子设置了 D20 属性门槛，属性达标后即可查看。"
	case accessGuardGuardian:
		return "仅守护者可见", "这篇帖子属于守护者专属节点，仅守护者可以查看。"
	default:
		return "内容不可见", "这篇帖子设置了访问限制，满足条件后即可查看。"
	}
}

// accessWall 画无权帖的访问墙。它是正文的主体：说明门槛是什么，未登录时
// 给「去登录」入口；授权完成后由用户自己重新打开这篇帖子，墙随即被真正内容
// 替换。页面级状态留在页面里（不像一次性操作那样发 toast），所以用内联卡片。
func (a *app) accessWall(c *ui.Context, d *detailState, p *topicDetail) {
	tk := lobetheme.TokensOf(c)
	title, detail := accessGuardReason(p)
	ui.Column(c).FillWidth().Gap(10).Padding(24, 20).Radius(tk.BorderRadiusLG).
		Background(tk.FillQuaternary).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, lucide.Lock()).Size(22, 22).TextColor(tk.TextSecondary).Shrink(0)
			ui.Text(c, title).FontSize(tk.FontSizeLG).Bold().TextColor(tk.TextHeading)
		})
		ui.Text(c, detail).FontSize(tk.FontSizeSM).TextColor(tk.TextDescription).MaxLines(3)
		if !a.auth.loggedIn() {
			if ui.PrimaryButton(c, "去登录").Clicked() {
				a.requestLoginFor(a.detailToastFor(d), "登录后才能查看这篇帖子。")
			}
		}
	})
}

// voteSection 画正文末尾的投票块：单选时点某一项就直接投，多选时先勾选
// 再点「投票」。提交中页面里只留一行进度，成功与失败都由主窗口 toast 报告
// （见 submitVote）。已结束 / 已投过时整块禁用，避免重复提交。
func (a *app) voteSection(c *ui.Context, d *detailState, v *voteBlock) {
	tk := lobetheme.TokensOf(c)
	multi := voteIsMulti(v)
	locked := v.Finished || d.vote.voted || d.vote.busy
	// 详情刷新可能换掉选项列表：勾选框按当前行数绑定，syncVote 在下一份
	// 详情回来时还会再对齐一次。
	if len(d.vote.checks) != len(v.Options) {
		d.vote.checks = make([]bool, len(v.Options))
	}
	for i, o := range v.Options {
		i, o := i, o
		// 票数默认不展示，免得影响别人怎么投；没投过的人要点开下面的
		// 「查看投票结果」才看得到，投过票的人会自动展开（见 voteResultSection）。
		label := o.Text
		// 勾选框是有状态的控件，构造前用 Context.Key 固定身份。
		c.Key("vote-option-" + o.ID)
		if multi {
			ui.Checkbox(c, &d.vote.checks[i], label).
				FontSize(tk.FontSizeSM).Disabled(locked)
			continue
		}
		// 单选选项用 Lobe 的 Filled 按钮，标签文案原样保留供点击测试命中。
		btn := action.Button(c, label, action.ButtonProps{
			Variant: action.Filled, Size: lobetheme.Middle,
		}).FillWidth().Disabled(locked)
		if btn.Clicked() {
			a.submitVote(d, []string{o.ID}, false)
		}
	}
	switch {
	case d.vote.busy:
		ui.Text(c, "正在提交投票…").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
	case v.Finished:
		ui.Text(c, "投票已结束").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
	case d.vote.voted:
		ui.Text(c, "已投票，感谢参与").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
	case multi:
		btn := action.Button(c, "投票", action.ButtonProps{Variant: action.Primary}).
			Disabled(!anyVoteChecked(d.vote.checks))
		if btn.Clicked() {
			a.submitVoteMulti(d, v)
		}
	}
	a.voteResultSection(c, d, v)
}

// voteResultSection 是投票块里唯一展示票数的地方：一行「查看投票结果」展开
// / 收起，展开后每个选项一条进度条，显示票数与占比。分布来自 GET /vote/result
// （见 vote.go）；失败只在这里说明，不弹 toast —— 这是只读查询，没投过的人
// 主动点开才读。票数默认藏在选项文字后面（见 voteSection），投过票的人则在
// 详情加载时就自动展开。
func (a *app) voteResultSection(c *ui.Context, d *detailState, v *voteBlock) {
	tk := lobetheme.TokensOf(c)
	if v.ID == "" {
		return
	}
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		label := "查看投票结果"
		if d.vote.resultOpen {
			label = "收起投票结果"
		}
		a.smallAction(c, label, d.vote.resultLoading, func() { a.toggleVoteResult(d) })
		if d.vote.resultLoading {
			lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small})
			ui.Text(c, "正在读取结果…").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
		} else if d.vote.resultOpen && d.vote.result != nil && d.vote.result.Total > 0 {
			ui.Textf(c, "共 %d 票", d.vote.result.Total).FontSize(tk.FontSizeSM).
				Font(sidebarFont).TextColor(tk.TextDescription)
		}
	})
	if d.vote.resultErr != "" {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "读取投票结果失败："+d.vote.resultErr).FontSize(tk.FontSizeSM).
				TextColor(tk.Error).MaxLines(2).Grow(1)
			a.smallAction(c, "重试", false, func() { a.loadVoteResult(d) })
		})
	}
	if !d.vote.resultOpen || d.vote.result == nil {
		return
	}
	total := d.vote.result.Total
	for _, row := range voteResultRows(v, d.vote.result) {
		share := 0.0
		if total > 0 {
			share = float64(row.Votes) / float64(total)
		}
		percent := int(share*100 + 0.5)
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, row.Text).FontSize(tk.FontSizeSM).TextColor(tk.Text).
					SingleLine().Grow(1)
				ui.Textf(c, "%d 票 · %d%%", row.Votes, percent).FontSize(tk.FontSizeSM).
					Font(sidebarFont).TextColor(tk.TextDescription).Shrink(0)
			})
			ui.Progress(c, share)
		})
	}
	if total == 0 {
		ui.Text(c, "还没有人投票。").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
	}
}

// voteResultRows 把结果端点与详情里的选项合并成要画的几行：以详情顺序为
// 主（用户看到的就是这个顺序），结果里多出来的选项接在后面。
func voteResultRows(v *voteBlock, res *voteResult) []voteOption {
	if res == nil {
		return nil
	}
	byID := make(map[string]voteOption, len(res.Options))
	for _, o := range res.Options {
		if o.ID != "" {
			byID[o.ID] = o
		}
	}
	var rows []voteOption
	seen := make(map[string]bool, len(res.Options))
	if v != nil {
		for _, o := range v.Options {
			row := o
			if got, ok := byID[o.ID]; ok {
				row.Votes = got.Votes
				if got.Text != "" {
					row.Text = got.Text
				}
			}
			if o.ID != "" {
				seen[o.ID] = true
			}
			rows = append(rows, row)
		}
	}
	for _, o := range res.Options {
		if seen[o.ID] {
			continue
		}
		seen[o.ID] = true
		rows = append(rows, o)
	}
	return rows
}

func anyVoteChecked(checks []bool) bool {
	for _, on := range checks {
		if on {
			return true
		}
	}
	return false
}

// submitVoteMulti 收集勾选的选项并检查多选上限，然后走同一条提交链路。
func (a *app) submitVoteMulti(d *detailState, v *voteBlock) {
	var ids []string
	for i, on := range d.vote.checks {
		if on && i < len(v.Options) {
			ids = append(ids, v.Options[i].ID)
		}
	}
	if len(ids) == 0 {
		a.toast(a.detailToastFor(d), "请先选择投票选项")
		return
	}
	max := v.MaxChoice
	if max <= 0 {
		max = voteOptionMax
	}
	if len(ids) > max {
		a.toast(a.detailToastFor(d), fmt.Sprintf("最多选 %d 项", max))
		return
	}
	a.submitVote(d, ids, true)
}

// topicNotice 是正文顶部的一条状态说明：评论定位、离线缓存与内联浏览层级。
// 这些提示原先各占页头的一部分，压得页头很高；收到正文顶部后，没有提示时
// 连一行都不占。多条同时出现时在同一行内换行。
func (a *app) topicNotice(c *ui.Context, d *detailState) {
	tk := lobetheme.TokensOf(c)
	type notice struct {
		text  string
		color ui.Color
	}
	var notes []notice
	// 节点还在由 AI 分配时说明一句：这不是加载失败，帖子也还没出现在列表里。
	if d.topic != nil && d.topic.pendingNode() {
		notes = append(notes, notice{"节点由 AI 自动分配，通常几秒后完成。", tk.TextDescription})
	}
	if d.focusID != "" {
		text, color := "来自评论定位", tk.TextDescription
		if d.focusMiss {
			text, color = "未找到该评论", tk.Error
		}
		notes = append(notes, notice{text, color})
	}
	// 离线说明：正文来自本地缓存时，明确告诉用户这是哪个时间的内容，
	// 而不是让旧内容看起来像刚刷新过。
	if d.cacheNote != "" {
		notes = append(notes, notice{d.cacheNote, tk.TextDescription})
	}
	if n := len(a.detailStack); n > 0 {
		notes = append(notes, notice{fmt.Sprintf("内联浏览 %d 层", n), tk.TextDescription})
	}
	if len(notes) == 0 {
		return
	}
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Wrap().Font(sidebarFont).Children(func() {
		for _, nt := range notes {
			ui.Text(c, nt.text).FontSize(tk.FontSizeSM).TextColor(nt.color).SingleLine()
		}
	})
}

// topicAuthorLine 是标题下方的作者信息：左边是昵称字号换算出的小头像 +
// 昵称 + 等级 + 徽章，右边是收藏按钮。它跟着正文一起滚动，不再固定占一条
// 页头；头像仍可点开作者资料卡。关注按钮已经从详情页移除，关注入口只在
// 用户资料卡里。
func (a *app) topicAuthorLine(c *ui.Context, d *detailState) {
	p := d.topic
	if p == nil {
		return
	}
	tk := lobetheme.TokensOf(c)
	ts := a.typeSizes()
	ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
		// 作者身份在左侧自己的区域里换行，窄窗口也不会把收藏挤出这一行。
		ui.Row(c).Grow(1).MinWidth(0).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
			if strings.TrimSpace(p.Author) != "" {
				a.userTag(c, userTagSpec{
					Name: p.Author, Avatar: p.Avatar, Size: ts.author, Bold: true,
					Badges: p.Badges, Pro: p.AuthorIsPro,
					UserMenu: func(identity ui.Element) {
						a.detailAvatarMenu(c, identity, d, p.Author, p.AuthorID)
					},
				})
			}
			// 等级用等宽小字弱化，和昵称的强调拉开层次。
			if p.AuthorLevel > 0 {
				ui.Textf(c, "Lv.%d", p.AuthorLevel).FontSize(tk.FontSizeSM).
					Font(sidebarFont).TextColor(tk.TextDescription)
			}
		})
		// 收藏是帖子级的动作，贴在这条元信息行的最右侧；合集在它左边：
		// 「归入合集」是整理动作，收藏是标记动作，两个入口相邻但各管一件事。
		a.collectionButton(c, d)
		a.favoriteButton(c, d)
	})
}

// topicSubtitle 是标题下面的两行副标题：第一行是两级分类与帖子状态
// （置顶 / 评论已关闭），第二行是标签。分类和标签原来贴在页头作者旁边，收回
// 到标题下方后，标题区一眼能读出「这帖发在哪、打了什么标签」。浏览数已经挪到
// 评论区折叠输入框里、发送按钮左侧（见 replyComposerStats），这里不再重复。
func (a *app) topicSubtitle(c *ui.Context, p *topicDetail) {
	tk := lobetheme.TokensOf(c)
	path := p.categoryPath()
	if path == "" && !p.IsPinned && !p.CommentsClosed &&
		len(p.Tags) == 0 {
		return
	}
	// 元信息用等宽小字、弱化色，和标题的衬线体形成两层；行距收紧成一块
	// 副标题，再靠组外间距与标题、正文分开。
	ui.Column(c).FillWidth().Gap(6).Font(sidebarFont).Children(func() {
		if path != "" || p.IsPinned || p.CommentsClosed {
			ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Wrap().Children(func() {
				// 置顶用 Lobe 的金色 Tag，和列表页的置顶角标同一套视觉。
				if p.IsPinned {
					display.Tag(c, "置顶", display.TagProps{
						Color: display.TagGold, Size: lobetheme.Small,
					})
				}
				if path != "" {
					nodeTagAction(c, nodeTagProps{
						Label: path, Ref: nodeRefFromTopic(p), OnPick: a.showNodeFeed,
					})
				}
				if p.CommentsClosed {
					ui.Text(c, "评论已关闭").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
				}
			})
		}
		if len(p.Tags) > 0 {
			// 标签是副标题的第二行：等宽、弱化的小字，不和正文抢注意力。
			ui.Text(c, "#"+strings.Join(p.Tags, "  #")).FontSize(tk.FontSizeSM).
				TextColor(tk.TextDescription).SingleLine()
		}
	})
}

// topicSourceFooter 占满整个窗口底部，放在最底部：数据来源声明 + 可点击原文
// 链接（点一下用系统浏览器打开）。这一行从进入详情起就画出来，正文是否加载
// 完成都不改变它占的高度，正文出现时两栏卡片不会先高后矮地跳一下；原文地址
// 用打开这一帖时带进来的链接兜底，正文到了再换成规范的永久链接。
func (a *app) topicSourceFooter(c *ui.Context, d *detailState) {
	tk := lobetheme.TokensOf(c)
	layout.Divider(c, layout.DividerProps{})
	ui.Row(c).Padding(10, 20).Gap(10).AlignItems(ui.Center).Font(sidebarFont).Children(func() {
		ui.Row(c).Grow(1).MinWidth(0).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Text(c, "内容来自 2Libra 用户，版权归原作者所有。").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
			origin := d.originURL()
			if origin == "" {
				return
			}
			ui.Text(c, "原文链接：").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
			link := ui.Button(c, truncate(origin, 60)).
				Background(ui.Transparent).Border(0, tk.BorderSecondary).
				TextColor(tk.Primary).FontSize(tk.FontSizeSM).Padding(0, 2)
			if link.Clicked() {
				a.openExternal(origin)
			}
		})
	})
}

// replyJumpButtons 报告评论区工具条里两枚跳转按钮该不该出现。列表按
// display 构建，可视行号来自同一份 list 状态：滚离顶部才有回顶，还没到
// 末尾才有「跳到最新回复」。停在中间时两枚一起出现。
func replyJumpButtons(d *detailState) (top, latest bool) {
	if d == nil || len(d.display) == 0 {
		return false, false
	}
	first, last := d.list.Visible()
	return first > 0, last >= 0 && last < len(d.display)-1
}

// replyComposerToolBar 把评论区自己的操作（回顶 / 跳到最新 / 视图切换 / 刷新）
// 画在评论输入框右下角、发送按钮的右侧：回顶只滚到第一条评论，跳到最新滚到
// 当前已加载的最后一条，视图切换只切楼层 / 扁平，刷新只重拉评论第一页。
// 滚离顶部才露出回顶，没到末尾才露出跳到最新，另外两枚一直在。
// 不再悬在评论列表上，列表因此不用为它预留一条空带（修复底部空行）。
// 只在未点击输入框的折叠态出现：进入编辑态、或右侧评论栏收起时整组不画，
// 输入框照旧平铺在正文底部，只是此刻没有评论区可操作。
func (a *app) replyComposerToolBar(c *ui.Context, d *detailState) {
	tk := lobetheme.TokensOf(c)
	items := make([]action.ActionGroupItem, 0, 5)
	top, latest := replyJumpButtons(d)
	if top {
		items = append(items, action.ActionGroupItem{
			Key: "top", Icon: lucide.ArrowUp(), Label: "评论回到顶部",
		})
	}
	if latest {
		items = append(items, action.ActionGroupItem{
			Key: "latest", Icon: lucide.ArrowDown(), Label: "跳到最新回复",
		})
	}
	// 只看楼主：过滤态再点一次取消。匿名帖没有楼主，入口直接禁用。
	filterLabel := "只看楼主"
	if d.authorFilter != "" {
		filterLabel = "取消" + authorFilterLabel(d)
	}
	filterDisabled := d.replyLoading || len(d.replyList) == 0 ||
		d.topic == nil || isAnonymousTopic(d)
	items = append(items, action.ActionGroupItem{
		Key: "filter", Icon: lucide.Filter(), Label: filterLabel, Disabled: filterDisabled,
	})
	// 图标表示将要切到的视图，点一下在楼层 / 扁平时间序间切换。
	viewIcon, viewLabel := flatViewIcon, "切到扁平时间序"
	if d.flat {
		viewIcon, viewLabel = floorViewIcon, "切到楼层视图"
	}
	items = append(items, action.ActionGroupItem{
		Key: "view", Icon: viewIcon, Label: viewLabel, Disabled: d.replyLoading,
	})
	items = append(items, action.ActionGroupItem{
		Key: "refresh", Icon: lucide.RefreshCw(), Label: "刷新评论", Disabled: d.replyLoading,
	})
	// ActionIconGroup 没有逐项激活态；过滤时在整组左侧画一条主色竖条，和
	// 发帖侧栏「已添加」用的是同一套语言。不额外加 Tooltip，按钮的无障碍名
	// 由 Item 的 Label 给出。
	ui.Row(c).AlignItems(ui.Center).Gap(4).Children(func() {
		if d.authorFilter != "" {
			ui.Box(c).Size(3, 18).Radius(2).Background(tk.Primary)
		}
		if key := action.ActionIconGroup(c, action.ActionIconGroupProps{
			Items: items, Size: lobetheme.Small, Vertical: false,
			Variant: action.GroupOutlined,
		}); key != "" {
			switch key {
			case "top":
				d.list.ScrollTo(0, ui.Start)
			case "latest":
				scrollToLatestReply(d)
			case "filter":
				if d.authorFilter != "" {
					clearAuthorFilter(d)
				} else {
					setAuthorFilter(d, "", "", true)
				}
			case "view":
				d.flat = !d.flat
				d.replyList = nil
				d.replyLoaded = false
				d.replyNoMore = false
				d.replyMoreErr = ""
				// 换了视图重新找一次被定位的评论：楼层/扁平的分页位置不同。
				d.focusPending = d.focusID != ""
				d.focusTries = 0
				d.focusMiss = false
				a.loadReplies(1)
				// 视图偏好立即落盘：换一篇帖子打开也沿用（见 reply_view.go）。
				a.saveReplyViewMode(d.flat)
				// 过滤继续生效，但换视图重新拉第一页，续拉预算从头算。
				d.filterAutoPages = 0
			case "refresh":
				if !d.replyLoading {
					a.refreshReplies()
				}
			}
		}
	})
}

// scrollToLatestReply 滚到当前已加载评论的最后一条。行号取 display 而不是
// replyList：折叠的楼中楼与作者过滤都会改变列表行数，定位必须跟显示一致。
// 这里不碰 focusID——续拉下一页时定位逻辑会按它把视口拽回旧位置。
func scrollToLatestReply(d *detailState) {
	if d == nil || len(d.display) == 0 {
		return
	}
	d.list.ScrollTo(len(d.display)-1, ui.End)
}

func (a *app) replyPane(c *ui.Context, d *detailState) {
	tk := lobetheme.TokensOf(c)
	// 输入框从一行展开成多行时，评论区高度被编辑器吃掉一截。列表本身在
	// 这一帧之前还按折叠态排版，视口一缩，底部那条评论就被长出来的编辑器
	// 盖住。认出这个瞬间后，把上一帧最底下那条可见评论钉到列表底部，整列
	// 评论自然往上让出位置；滚动中的栏目和已经停在末尾的都一样处理。
	pushUp := d.composerPushPending ||
		(replyComposerExpanded(d, d.composerField.Focused(c)) && !d.composerExpanded)
	d.composerPushPending = false
	// 评论标题与计数已经挪到评论区底部折叠输入框里、发送按钮左侧（见
	// replyComposerStats）；视图切换、回顶 / 跳到最新与刷新嵌在同一行的发送
	// 按钮右侧（见 replyComposerToolBar），跟着评论区一起显隐。列表区因此直接从面板
	// 顶部铺到输入栏，底部不再留一条空白。评论区收起时 replyPane 不画，
	// 输入框平铺到正文底部，但那组评论操作也不再出现；编辑态同样收起。
	ui.Column(c).Grow(1).FillWidth().MinHeight(0).Children(func() {
		// display 是所有列表行数与滚动定位的唯一依据：先过滤作者再折叠，
		// 空列表 / 有列表都要走同一份结果。
		d.rebuildDisplay()
		first := -1 // 没有评论列表时没有可视行号，通知已读判断据此跳过
		switch {
		case d.replyLoading && len(d.replyList) == 0:
			ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
				lobeSpin(c, feedback.SpinProps{Size: lobetheme.Small, Tip: "正在加载评论…"})
			})
		case d.replyErr != "" && len(d.replyList) == 0:
			ui.Column(c).Grow(1).Center().Gap(8).Padding(16).Children(func() {
				ui.Text(c, "评论加载失败").FontSize(tk.FontSizeLG).Bold().TextColor(tk.TextHeading)
				ui.Text(c, d.replyErr).TextColor(tk.Error).MaxLines(4)
				if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
					a.loadReplies(d.replyPage)
				}
			})
		case len(d.replyList) == 0:
			ui.Column(c).Grow(1).Center().Children(func() {
				if d.contentBlocked() {
					// passed == false 或评论接口 d:null 都表示无权，不是「真的没人评论」。
					ui.Text(c, "评论不可见").FontSize(tk.FontSize).TextColor(tk.TextDescription)
					return
				}
				ui.Text(c, "暂无评论").FontSize(tk.FontSize).TextColor(tk.TextDescription)
			})
		case d.authorFilter != "" && len(d.display) == 0:
			a.replyFilterEmpty(c, d)
		default:
			rows := d.display
			// 评论树连线：display 是前序摊平的树，下一行比第 l 层深就说明该层
			// 竖线还要往下画。给每行算好各层的延续状态，replyRow 据此画导轨。
			guides := replyTreeGuides(rows)
			d.list.Key = func(i int) any {
				if i >= 0 && i < len(rows) && rows[i].ID != "" {
					return rows[i].ID
				}
				return i
			}
			// 实时记录停留位置：视口顶部的行号与评论 ID，供离开时落盘恢复。
			var last int
			first, last = d.list.Visible()
			// 先留住上一帧的底行 id，再记下这一帧的：展开可能发生在构建
			// 列表之后（点「回复」），那一帧列表已经变矮、底行被裁掉，
			// 只能用上一帧的 id 把它找回来。
			bottomID := d.replyBottomID
			if last >= first && last < len(rows) && rows[last].ID != "" {
				d.replyBottomID = rows[last].ID
			}
			if pushUp {
				target := last
				if at := replyIndexByID(rows, bottomID); at >= 0 {
					target = at
				}
				if target >= 0 && target < len(rows) {
					// 把之前最底下的评论贴到新视口的底部，列表随之上移，
					// 不会留下被编辑器盖住的半截评论。
					d.list.ScrollTo(target, ui.End)
				}
			}
			if first >= 0 && first < len(rows) {
				d.stayIdx = first
				if rows[first].ID != "" {
					d.stayID = rows[first].ID
				}
				// 从通知点进来时，目标评论真的出现在屏幕上才把那条消息标为已读。
				a.markNotificationSeen(d, first, last)
			}
			// 过滤态在列表上方显示命中条数：N 是当前已加载里的命中数，
			// 不是全帖命中数（服务端没有作者筛选参数）。
			if d.authorFilter != "" {
				ui.Row(c).Padding(4, 12, 2, 12).Children(func() {
					ui.Text(c, fmt.Sprintf("%s · %d 条", authorFilterLabel(d), len(rows))).
						FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
				})
			}
			// MyGo 的 ui.List 是虚拟化滚动区：只构建可视行，长帖也不会一次
			// 排完所有评论；Grow(1) + MinHeight(0) 让它吃满卡片中间，输入栏
			// 始终固定在卡片底部。
			//
			// 平铺的评论区：行与行之间没有卡片间距（Gap 0），行距由每行自己的
			// 上下 padding 给出，纵向导轨因此能首尾相接成一整条评论树。
			ui.List(c, &d.list, len(rows), func(i int) {
				a.replyRow(c, rows[i], guides[i], d.focusID, isNewReply(rows[i].CreatedAt, d.readBase))
			}).Grow(1).MinHeight(0).Padding(6, 12).Gap(0)
			a.loadMoreFooter(c, d.replyMoreErr, d.replyLoading, func() {
				a.loadReplies(d.replyPage)
			})
			// 无限滚动：滚到底且还有下一页时自动加载更多，移除原来的页码与上下页按钮。
			// 与首页 / 通知列表保持一致：AtEnd 触发、replyNoMore 兜底停止；
			// 续拉失败后停在底部提示上，等用户点「重试」，不再自动重发。
			// 过滤态另有 10 页预算，用完改画「继续加载」。
			a.replyMoreControl(c, d, d.list.AtEnd())
		}
	})
	a.replyComposer(c, d)
}

// markNotificationSeen 在「从通知点进来定位的那层楼」真正出现在评论列表可视区
// 时，把那条消息标为已读：点开只说明用户想看，看到评论才算读过。first / last
// 是列表上一帧显示出来的行号区间（含两端）；评论还没加载出来、没定位到，或者
// 用户还停在别处，消息就保持未读，等滚到它再落账。只落一次：发出去之前先把
// 待办清掉，后面的帧不会重复提交（评论被折叠、切换视图也一样）。
//
// 已读请求要等这一帧构建结束才发（见 flushSeenNotifications）：在视图里直接
// 起请求，结果会比视图先到，等测试绑上的回调就永远收不到。
func (a *app) markNotificationSeen(d *detailState, first, last int) {
	if d == nil || d.notifID == "" || d.notifCommentID == "" {
		return
	}
	at := replyIndexByID(d.display, d.notifCommentID)
	if at < 0 || at < first || at > last {
		return
	}
	a.seenNotifications = append(a.seenNotifications, d.notifID)
	d.notifID, d.notifCommentID = "", ""
}

// flushSeenNotifications 把视图里攒下的「看到了」提交为按条已读。它在该帧
// 构建结束之后调用，发出的请求一定晚于这一帧，不会被紧随其后的测试步骤错过。
func (a *app) flushSeenNotifications() {
	if len(a.seenNotifications) == 0 {
		return
	}
	ids := append([]string(nil), a.seenNotifications...)
	a.seenNotifications = nil
	a.markNotificationsRead(a.detailToast(), ids)
}

// replyCanLoadMore 汇报是否还能（且应）续拉下一页评论：已加载、未在读、未到底、
// 且仍有下一页。totalPages 缺失（为 0）时按空页去重兜底，继续尝试续拉。
func replyCanLoadMore(d *detailState) bool {
	if !d.replyLoaded || d.replyLoading || d.replyNoMore {
		return false
	}
	return d.replyTotalPages == 0 || d.replyPage < d.replyTotalPages
}

// replyMoreControl 是评论区底部的续拉控制。atEnd 由列表给出（空态没有列表，
// 调用方直接传 true）。不过滤时沿用原来的无限滚动；过滤时为了找命中评论自动
// 续拉，但最多 replyFilterAutoMaxPages 页，之后改为手点「继续加载」。
func (a *app) replyMoreControl(c *ui.Context, d *detailState, atEnd bool) {
	if d == nil || !atEnd || !replyCanLoadMore(d) || d.replyMoreErr != "" {
		return
	}
	if d.authorFilter == "" {
		a.loadReplies(d.replyPage + 1)
		return
	}
	if filterCanAutoLoad(d) {
		if a.autoLoad {
			d.filterAutoPages++
			a.loadReplies(d.replyPage + 1)
		}
		return
	}
	label := "继续加载"
	if d.replyTotalPages > d.replyPage {
		label += fmt.Sprintf("（还有 %d 页）", d.replyTotalPages-d.replyPage)
	}
	ui.Row(c).FillWidth().Padding(8, 12).AlignItems(ui.Center).Children(func() {
		if action.Button(c, label, action.ButtonProps{Size: lobetheme.Small}).Clicked() {
			// 手动续拉重置预算：用户明确要继续找，就再给一轮自动续拉。
			d.filterAutoPages = 0
			a.loadReplies(d.replyPage + 1)
		}
	})
}

// replyFilterEmpty 是过滤后已加载范围内 0 条命中的空态。副文案只在还能续拉
// 时出现，避免告诉用户「翻页」其实已经到底；底部仍保留续拉与失败重试入口。
func (a *app) replyFilterEmpty(c *ui.Context, d *detailState) {
	tk := lobetheme.TokensOf(c)
	ui.Column(c).Grow(1).Center().Gap(8).Padding(16).Children(func() {
		title := "ta 还没有回复"
		if d.authorFilterIsOP {
			title = "楼主还没有回复"
		}
		ui.Text(c, title).FontSize(tk.FontSizeLG).Bold().TextColor(tk.TextHeading)
		if replyCanLoadMore(d) {
			ui.Textf(c, "已加载 %d 条评论，可能还没到 ta 的楼层。", len(d.replyList)).
				FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
		}
		if action.Button(c, "查看全部", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
			clearAuthorFilter(d)
		}
	})
	a.loadMoreFooter(c, d.replyMoreErr, d.replyLoading, func() {
		a.loadReplies(d.replyPage)
	})
	a.replyMoreControl(c, d, true)
}

// 过滤态下为找命中评论自动续拉的页数上限。超过后停下，改由用户点「继续加载」，
// 避免为了看楼主一句话把整帖都拉完。
const replyFilterAutoMaxPages = 10

// isAnonymousTopic 报告这篇帖子的作者是不是「匿名」：匿名帖没有「楼主」可看，
// 只看楼主入口要禁用。
func isAnonymousTopic(d *detailState) bool {
	return d != nil && d.topic != nil && d.topic.Author == anonymousAuthor
}

// filterCanAutoLoad 报告过滤态下是否还能自动续拉。不过滤时不受这个预算约束。
func filterCanAutoLoad(d *detailState) bool {
	return d == nil || d.authorFilter == "" || d.filterAutoPages < replyFilterAutoMaxPages
}

// authorFilterLabel 是过滤入口在工具条上的无障碍名：「只看楼主」或「只看 ta」。
func authorFilterLabel(d *detailState) string {
	if d == nil {
		return ""
	}
	if d.authorFilterIsOP {
		return "只看楼主"
	}
	return "只看 " + d.authorFilterName
}

// setAuthorFilter 开启（或切换）按作者过滤，并立刻重建显示列表、回到顶部。
// isOP 为真时取当前帖子的作者；匿名帖没有楼主，入口不会走到这里。
func setAuthorFilter(d *detailState, name, id string, isOP bool) {
	if d == nil {
		return
	}
	if isOP {
		if d.topic == nil || isAnonymousTopic(d) || d.topic.Author == "" {
			return
		}
		name, id = d.topic.Author, d.topic.AuthorID
	}
	if name == "" && id == "" {
		return
	}
	d.authorFilter = name
	if d.authorFilter == "" {
		d.authorFilter = id
	}
	d.authorFilterID = id
	d.authorFilterIsOP = isOP
	d.authorFilterName = name
	if d.authorFilterName == "" {
		d.authorFilterName = id
	}
	d.filterAutoPages = 0
	d.rebuildDisplay()
	d.list.ScrollTo(0, ui.Start)
}

// clearAuthorFilter 退出过滤，display 回到原来的评论树与折叠状态。
func clearAuthorFilter(d *detailState) {
	if d == nil || d.authorFilter == "" {
		return
	}
	d.authorFilter, d.authorFilterID = "", ""
	d.authorFilterIsOP, d.authorFilterName = false, ""
	d.filterAutoPages = 0
	d.rebuildDisplay()
	d.list.ScrollTo(0, ui.Start)
}

// isTopicAuthor 报告这条评论是不是楼主的回复。带作者 id 时按 id 比对，否则
// 退回昵称；匿名帖（作者是「匿名」）不参与判断，否则每条匿名回复都会被标成
// 楼主。
func (a *app) isTopicAuthor(r replyItem) bool {
	d := a.detail
	if d == nil || d.topic == nil {
		return false
	}
	topic := d.topic
	if topic.AuthorID != "" && r.AuthorID != "" {
		return topic.AuthorID == r.AuthorID
	}
	if r.Author == "" || r.Author == "匿名" {
		return false
	}
	return topic.Author != "" && topic.Author != "匿名" && topic.Author == r.Author
}

// opTag 画昵称后面那枚「OP」标签。它只是一枚小标记，不参与点击：整行没有
// 任何动作挂在它身上，点它和点空白一样。
func (a *app) opTag(c *ui.Context) {
	ui.Text(c, "OP").FontSize(10).Bold().
		TextColor(opTagText).Background(opTagBackground).
		Padding(3, 5).Radius(4).
		Tooltip("楼主的回复")
}

// 评论树的导轨几何。评论区是前序摊平的树：每条评论按 Depth 缩进，左侧的
// 竖线加肘形把父子关系连起来，替代原来「一条评论一张圆角卡片」的画法。
// 列表 Gap 为 0、行距由行内 padding 给出，相邻行的竖线因此首尾相接。
const (
	replyTreeIndent = float32(22) // 每层的横向缩进
	replyRailInset  = float32(7)  // 竖线在本层缩进带里的横向位置
	replyRowPadY    = float32(10) // 行内上下留白，也是相邻评论之间的行距
	replyElbowInset = float32(21) // 肘部相对行顶的纵向位置（对齐头像中线）
)

// replyTreeGuide 是一条评论在评论树里的连线状态：cont[l] 说第 l 层祖先在
// 本行下方还有后代，竖线因此贯穿整行；否则本行是那棵子树的最后一行，竖线
// 到肘部为止并向右折出。
type replyTreeGuide struct {
	cont []bool
}

// replyTreeGuides 为摊平后的评论列表算出每行的连线状态。前序排列保证同一
// 棵子树的行连续，所以只要比较下一行的深度就能判断某一层是否继续。
func replyTreeGuides(rows []replyItem) []replyTreeGuide {
	out := make([]replyTreeGuide, len(rows))
	for i, r := range rows {
		if r.Depth <= 0 {
			continue
		}
		g := replyTreeGuide{cont: make([]bool, r.Depth)}
		next := -1
		if i+1 < len(rows) {
			next = rows[i+1].Depth
		}
		for l := 0; l < r.Depth; l++ {
			g.cont[l] = next > l
		}
		out[i] = g
	}
	return out
}

// pkTeamTag 是一条评论在 PK 投票里挂的队伍标签：底色 + 文案。
type pkTeamTag struct {
	Text string
	Bg   ui.Color
}

// replyPKTeam 返回评论者选的队伍标签。choice 是评论的 user_pk_choice（选项
// uuid），与详情 vote.options[].id 对应：命中的选项在列表里的下标决定队伍——
// 所有 PK 帖子统一按第一个红队、第二个蓝队，文字与底色都固定，不看选项文本。
// 普通帖子、多选、没投票、选项对不上（老数据 / 投票改过选项）都返回 false，
// 不挂标签。
func replyPKTeam(v *voteBlock, choice string) (pkTeamTag, bool) {
	if v == nil || choice == "" {
		return pkTeamTag{}, false
	}
	if v.Type != "" && v.Type != "pk" {
		return pkTeamTag{}, false
	}
	for i, o := range v.Options {
		if o.ID == "" || o.ID != choice {
			continue
		}
		if i >= len(pkTeamNames) {
			return pkTeamTag{}, false
		}
		return pkTeamTag{Text: pkTeamNames[i], Bg: pkTeamBackgrounds[i]}, true
	}
	return pkTeamTag{}, false
}

// pkTeamTagView 把队伍标签画成 OP 标签那样的一小枚：淡色底 + 深色字，不占
// 第一行以外的版面。
func (a *app) pkTeamTagView(c *ui.Context, tag pkTeamTag) {
	ui.Text(c, tag.Text).FontSize(10).Bold().
		TextColor(pkTeamText).Background(tag.Bg).
		Padding(3, 5).Radius(4).
		Tooltip("该评论投给了" + tag.Text)
}

// paintReplyRails 给一条评论行画状态竖条、树形导轨与顶层分隔线。正常行和
// 被屏蔽的折叠行共用这一份，评论树的形状不会因为折叠而断开。
func paintReplyRails(c *ui.Context, row ui.Element, r replyItem, tree replyTreeGuide, focused, mine bool) {
	tk := lobetheme.TokensOf(c)
	marker, markerW := ui.Transparent, float32(0)
	switch {
	case focused:
		marker, markerW = tk.Primary, 3
	case r.Pinned:
		marker, markerW = tk.Primary, 2
	case mine:
		marker, markerW = tk.Primary.Alpha(0.55), 2
	}
	rail := tk.Border
	row.Draw(func(p *ui.Painter, box ui.Rect) {
		if markerW > 0 {
			p.Line(box.X+2, box.Y+6, box.X+2, box.Y+box.H-6, markerW, marker)
		}
		elbowY := box.Y + replyElbowInset
		contentX := box.X + 12 + float32(r.Depth)*replyTreeIndent - 4
		for l := 0; l < r.Depth; l++ {
			x := box.X + 12 + float32(l)*replyTreeIndent + replyRailInset
			cont := l < len(tree.cont) && tree.cont[l]
			endY := box.Y + box.H
			if !cont {
				endY = elbowY
			}
			p.Line(x, box.Y, x, endY, 1, rail)
			if l == r.Depth-1 || !cont {
				to := contentX
				if l+1 < r.Depth {
					to = box.X + 12 + float32(l+1)*replyTreeIndent + replyRailInset
				}
				p.Line(x, elbowY, to, elbowY, 1, rail)
			}
		}
		// 顶层楼层之间一道极淡的分隔线：平铺成一整块，但不会糊成一片。
		if r.Depth == 0 {
			p.Line(box.X+12, box.Y+box.H-1, box.X+box.W-12, box.Y+box.H-1, 1, tk.BorderSecondary)
		}
	})
}

func (a *app) replyRow(c *ui.Context, r replyItem, tree replyTreeGuide, focusID string, isNew bool) {
	t := c.Theme()
	tk := lobetheme.TokensOf(c)
	ts := a.typeSizes()
	focused := focusID != "" && r.ID != "" && r.ID == focusID
	mine := a.isMine(r.Author)
	// 头像右键菜单要用构建这一帧的详情：内联浏览换层后 a.detail 会变，
	// 把 d 抓在闭包里，操作才落在看到这条评论的那篇帖子上。
	d := a.detail
	name, userID := r.Author, r.AuthorID
	// 被屏蔽的评论数据不动（楼层号、树形导轨、定位与阅读位置都依赖它），
	// 只把这一行画成提示。展开态按评论 id 记在 detailState 里，只在内存。
	if reason, blocked := a.commentBlocked(r.Author, r.Content, r.Deleted); blocked &&
		!(a.detail != nil && a.detail.revealed[r.ID]) {
		a.blockedReplyRow(c, r, tree, reason)
		return
	}

	// 平铺的一行：静止时透明，悬浮只铺一层极淡的表面；没有卡片底色、圆角与
	// 描边。自己的回复与定位到的回复各留一档淡底，取代原来的虚线 / 实线框。
	row := ui.Column(c).FillWidth().Padding(replyRowPadY, 12, replyRowPadY,
		12+float32(r.Depth)*replyTreeIndent).Gap(6)
	row.Background(ui.Transparent)
	switch {
	case focused:
		row.Background(tk.PrimaryFillSecondary)
	case mine:
		row.Background(tk.PrimaryFillQuaternary)
	case row.Hovered():
		row.Background(tk.FillQuaternary)
	}

	// 状态竖条、树形导轨与分隔线：和被屏蔽的折叠行共用同一份画法。
	paintReplyRails(c, row, r, tree, focused, mine)
	hovered := row.Hovered()
	// 评论已关闭时整条都不能回复；已删除的评论同样不提供回复入口。
	closed := a.detail != nil && a.detail.topic != nil && a.detail.topic.CommentsClosed
	row.Children(func() {
		// 头部行只放身份信息，固定 22 高（跟着 13px 的昵称与头像走）：作者、
		// 徽章与时间靠左，楼层号吸在右端。回复 / 表态 / 打赏都在下面的
		// 固定底行里。
		ui.Row(c).Gap(8).AlignItems(ui.Center).Height(22).Children(func() {
			// 带楼中楼的评论可以折叠 / 展开：收起后只留这条本身，子回复
			// 藏起来（见 rebuildDisplay）。折叠入口只在有子树时出现。
			// 过滤态是平铺视角，折叠集合不生效（rebuildDisplay 已跳过），
			// 因此也不画折叠入口，免得点下去像有反应却什么都没收起。
			if r.HasChildren && a.detail != nil && a.detail.authorFilter == "" {
				collapsed := a.detail.collapsed[r.ID]
				fold := ui.Button(c, "").
					Background(ui.Transparent).Border(0, t.Border).
					TextColor(t.TextMuted).FontSize(ts.metaSmall).Padding(0, 0).Radius(4)
				if collapsed {
					fold.Children(func() {
						ui.Icon(c, chevronRightIcon).Size(13, 13).TextColor(t.TextMuted)
					})
					fold.Tooltip("展开楼中楼")
				} else {
					fold.Children(func() {
						ui.Icon(c, chevronDownIcon).Size(13, 13).TextColor(t.TextMuted)
					})
					fold.Tooltip("折叠楼中楼")
				}
				if fold.Clicked() {
					if a.detail.collapsed == nil {
						a.detail.collapsed = make(map[string]bool)
					}
					a.detail.collapsed[r.ID] = !collapsed
					a.detail.rebuildDisplay()
					if at := replyIndexByID(a.detail.display, r.ID); at >= 0 {
						a.detail.list.ScrollTo(at, ui.Start)
					}
					a.saveReplyView(a.detail)
				}
			}
			// 新评论标记：上次离开之后新增的回复，头部最左给一个 accent 小标签。
			if isNew {
				ui.Text(c, "新").FontSize(10).Bold().
					TextColor(t.AccentText).Background(t.Accent).
					Padding(3, 5).Radius(4)
			}
			// 评论作者统一由 userTag 渲染：头像跟着 13px 的昵称大小走，
			// 头部行固定 22 高时头像恰好与昵称齐平。
			a.userTag(c, userTagSpec{
				Name:   name,
				Avatar: r.Avatar,
				Size:   ts.meta,
				Bold:   true,
				Badges: r.Badges,
				Pro:    r.AuthorIsPro,
				UserMenu: func(identity ui.Element) {
					a.detailAvatarMenu(c, identity, d, name, userID)
				},
			})
			// 楼主的回复紧跟着昵称挂一枚 OP 标签，读长帖时一眼分得清哪几条
			// 是发帖人自己在下场回话（见 opTag）。
			if a.isTopicAuthor(r) {
				a.opTag(c)
			}
			if r.CreatedAt != "" {
				ui.Text(c, relativeTime(r.CreatedAt)).FontSize(tk.FontSizeSM).
					Font(sidebarFont).TextColor(tk.TextDescription)
			}
			// 手动展开过的被屏蔽评论在时间后面留一个很轻的「收起」入口：
			// 没有它，用户只能靠离开页面复原。只作用于这一条。
			if a.detail != nil && a.detail.revealed[r.ID] {
				collapse := ui.Button(c, "收起").Background(ui.Transparent).
					Border(0, tk.BorderSecondary).TextColor(tk.TextDescription).
					FontSize(tk.FontSizeSM).Padding(0, 0).Radius(tk.BorderRadiusXS)
				if collapse.Clicked() {
					delete(a.detail.revealed, r.ID)
				}
			}
			// 从帖子金币池里领到过金币的评论，在时间后面标出合计：这是这条
			// 评论拿到的额外收入，只有它自己有，不必再去翻金币流水。
			a.replyRewardPoolWinnings(c, r)
			// PK 投票的评论在作者行挂一枚队伍标签，紧跟身份与时间之后。
			if a.detail != nil && a.detail.topic != nil {
				if tag, ok := replyPKTeam(a.detail.topic.Vote, r.UserPKChoice); ok {
					a.pkTeamTagView(c, tag)
				}
			}
			// 楼层号放在这一行的最右侧：它是每层楼的标识，靠边排列不会把
			// 作者与时间挤散，长昵称也不会和它抢位置。只有顶层评论才有
			// floor；楼中楼服务端给的是 null，不能拿 flat_floor 当楼层号，
			// 否则回复别人的评论也会被算成一楼。
			ui.Spacer(c).Grow(1)
			floor := ""
			switch {
			case r.Pinned:
				floor = "置顶"
			case r.Floor > 0:
				floor = fmt.Sprintf("%d 楼", r.Floor)
			}
			if floor != "" {
				// 楼层号也是评论的永久链接：点一下用系统浏览器打开站点上的
				// 这条评论，复制分享都从那里走。
				href := ""
				if a.detail != nil {
					href = replyPermalink(a.detail.topic, a.detail.fallbackURL, r)
				}
				if href != "" {
					link := ui.Button(c, floor).Background(ui.Transparent).
						Border(0, tk.BorderSecondary).TextColor(tk.TextDescription).
						FontSize(tk.FontSizeSM).Font(sidebarFont).Padding(0, 0).Radius(tk.BorderRadiusXS)
					if link.Hovered() {
						link.TextColor(tk.Primary)
					}
					link.Tooltip("在浏览器打开这条评论")
					if link.Clicked() {
						a.openExternal(href)
					}
				} else {
					ui.Text(c, floor).FontSize(tk.FontSizeSM).Font(sidebarFont).
						TextColor(tk.TextDescription)
				}
			}
			// 回复、表态与打赏都收到评论底部那一行（见下面的 replyFooter），
			// 头部只留身份信息，不再随悬浮长出按钮。
		})
		// 三级回复的 parent 只是挂载点（二级评论），真正回的那条在
		// reply_comment_id 里：找得到就用它的作者与正文，引用和跳转都指向
		// 它，否则会显示成「回复二级评论的作者」，认错人。
		quoteID := r.ParentID
		label := replyParentLabel(r)
		if target, ok := replyQuoteTarget(a.detail, r); ok {
			label = replyQuoteLabel(target.Author, truncate(commentExcerpt(target.Content), 60), target.Floor)
			quoteID = target.ID
		}
		if label != "" {
			at := -1
			if a.detail != nil {
				at = replyIndexByID(a.detail.display, quoteID)
			}
			// 浅色主题里引用行铺一层辅助块淡灰，与白色评论区面板区分；文字
			// 仍和评论正文左对齐（左右 padding 留 0，见 parentJump）。
			if at >= 0 {
				// 父评论还在已加载的列表里：点一下跳过去并高亮它。
				a.parentJump(c, label, quoteID, at)
			} else {
				ui.Column(c).Padding(4, 8, 4, 0).Radius(tk.BorderRadius).
					Background(commentHelperBlockBg(t)).Children(func() {
					ui.Text(c, label).FontSize(ts.meta).TextColor(t.TextMuted).MaxLines(2)
				})
			}
		}
		content := r.Content
		if r.Deleted {
			content = "该评论已删除"
		}
		if strings.TrimSpace(content) == "" {
			content = "（无内容）"
		}
		a.commentText(c, content, r.ID, mergeEmojiMaps(a.emojis, r.Emojis))
		// 评论底部固定一行：左边是这条评论拿到的表态 / 打赏汇总，右边是
		// 「回复」与表态、打赏两个图标入口。这一行始终占位，悬浮或出现气泡
		// 都不会改变评论高度。
		if !r.Deleted {
			a.replyFooter(c, r, closed)
		}
		// 被折叠的评论在底部补一行「展开 N 条回复」，点一下就展开楼中楼。
		if r.HasChildren && a.detail != nil && a.detail.collapsed[r.ID] {
			n := a.detail.hiddenCount[r.ID]
			expand := ui.Button(c, fmt.Sprintf("展开 %d 条回复", n)).
				Background(ui.Transparent).Border(0, t.Border).
				TextColor(t.Accent).FontSize(ts.meta).Padding(2, 6).Radius(5)
			expand.Tooltip("展开楼中楼")
			if expand.Clicked() {
				if a.detail.collapsed == nil {
					a.detail.collapsed = make(map[string]bool)
				}
				a.detail.collapsed[r.ID] = false
				a.detail.rebuildDisplay()
				if at := replyIndexByID(a.detail.display, r.ID); at >= 0 {
					a.detail.list.ScrollTo(at, ui.Start)
				}
				a.saveReplyView(a.detail)
			}
		}
		// 评论里的应用内链接是次级操作：仅悬浮整条评论时出现，保持默认
		// 状态的清爽；其所在行固定高度占位，出现时同样不会撑高整条评论。
		if !r.Deleted {
			if links := inAppTopicLinks(r.Content); len(links) > 0 {
				ui.Row(c).Height(20).AlignItems(ui.Center).Children(func() {
					if hovered {
						ui.Row(c).Gap(6).Wrap().Children(func() {
							for _, link := range links {
								target := link
								a.replyAction(c, "在应用内打开 "+target.ShortID, func() {
									a.handleLink(target.URL)
								})
							}
						})
					}
				})
			}
		}
	})
}

// blockedReplyRow 把被屏蔽的评论压成一行提示：只显示命中的原因与「展开」，
// 绝不把评论正文画出来。左缩进与树形导轨和正常行一致，评论树因此不会断开；
// 楼中楼折叠、定位、阅读位置都不受影响，子回复照常显示。
func (a *app) blockedReplyRow(c *ui.Context, r replyItem, tree replyTreeGuide, reason blockReason) {
	t := c.Theme()
	tk := lobetheme.TokensOf(c)
	ts := a.typeSizes()
	row := ui.Column(c).FillWidth().Height(32).Padding(0, 12, 0,
		12+float32(r.Depth)*replyTreeIndent)
	row.Background(ui.Transparent)
	if row.Hovered() {
		row.Background(tk.FillQuaternary)
	}
	paintReplyRails(c, row, r, tree, false, false)
	row.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, reason.text()).FontSize(ts.meta).
				TextColor(tk.TextDescription).MaxLines(1)
			ui.Spacer(c).Grow(1)
			expand := ui.Button(c, "展开").Background(ui.Transparent).
				Border(0, tk.BorderSecondary).TextColor(t.Accent).
				FontSize(ts.meta).Padding(2, 6).Radius(5)
			if expand.Clicked() {
				if a.detail != nil {
					if a.detail.revealed == nil {
						a.detail.revealed = make(map[string]bool)
					}
					a.detail.revealed[r.ID] = true
				}
			}
		})
	})
}

// replyFooter 是评论底部固定的一行：最左边是表态（笑脸）入口，随后是这条
// 评论拿到的表态 / 打赏汇总；最右边是打赏（金币）入口与回复图标。整行始终
// 渲染，悬浮、打开气泡都不会让评论高度变化。
func (a *app) replyFooter(c *ui.Context, r replyItem, closed bool) {
	if a.detail == nil {
		return
	}
	d := a.detail
	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		if a.canActOn(d, r.ID) {
			a.reactionEntry(c, d, r.ID, interactEntryCompact)
		}
		a.reactionSummaryInfo(c, d, r.ID, r.Rewards)
		ui.Spacer(c).Grow(1)
		if a.canRewardOn(d, r.ID) {
			a.rewardEntry(c, d, r.ID, interactEntryCompact)
		}
		if a.canEditReply(r) {
			a.replyEditEntry(c, d, r)
		}
		a.bookmarkEntry(c, d, r)
		a.replyCopyEntry(c, d, r)
		if !closed {
			a.replyEntry(c, d, r)
		}
	})
}

// replyCopyEntry 是评论底行的「复制链接」：把这条评论的永久链接写进剪贴板
// （地址与楼层号点开的那个一样，见 replyPermalink）。已删除的评论和还在
// 加载中的评论没有可用的链接，不出这个入口。
func (a *app) replyCopyEntry(c *ui.Context, d *detailState, r replyItem) {
	if r.ID == "" || r.Deleted {
		return
	}
	href := replyPermalink(d.topic, d.fallbackURL, r)
	if href == "" {
		return
	}
	t := c.Theme()
	// 评论底行的图标统一走 Lobe ActionIcon：方块热区、圆角与悬停底色由组件
	// 给，这里只保留本应用原来的字形颜色层级和文案。
	b := action.ActionIcon(c, copyLinkIcon, action.ActionIconProps{
		BlockSize: interactEntryCompact.box, GlyphSize: interactEntryCompact.icon,
	}).Radius(interactEntryCompact.radius)
	ink := t.TextMuted
	if b.Hovered() {
		ink = t.Text
	}
	b.TextColor(ink).Label("复制评论链接").Tooltip("把这条评论的链接复制到剪贴板")
	if b.Clicked() {
		a.copyToClipboard(c, href, a.detailToastFor(d), "已复制评论链接")
	}
}

// bookmarkEntry 是评论底行的书签图标：已加书签时填实并换成强调色。未登录
// 也可以点，授权成功后由用户自己再点一次（见 bookmarks.go）。
func (a *app) bookmarkEntry(c *ui.Context, d *detailState, r replyItem) {
	if r.ID == "" || r.Deleted {
		return
	}
	t := c.Theme()
	on := a.replyBookmarked(d, r.ID)
	busy := d.bmBusy == r.ID
	icon := bookmarkIcon
	if on {
		icon = bookmarkOnIcon
	}
	b := action.ActionIcon(c, icon, action.ActionIconProps{
		BlockSize: interactEntryCompact.box, GlyphSize: interactEntryCompact.icon,
	}).Radius(interactEntryCompact.radius)
	ink := t.TextMuted
	switch {
	case on:
		ink = t.Accent
	case b.Hovered():
		ink = t.Text
	}
	if busy {
		b.Disabled(true)
	}
	label, tip := "加书签", "把这条评论加入书签"
	if on {
		label, tip = "取消书签", "已加书签，点一下移除"
	}
	if busy {
		tip = "正在提交…"
	}
	b.TextColor(ink).Label(label).Tooltip(tip)
	if !busy {
		b.OnClick(func() { a.toggleReplyBookmark(d, r) })
	}
}

// replyEditEntry 是「编辑这条评论」的入口：只有作者本人看得到（见
// canEditReply）。它和书签、回复一样是评论底行的一枚图标，点开在编辑窗口里
// 改正文（见 edit_window.go）。
func (a *app) replyEditEntry(c *ui.Context, d *detailState, r replyItem) {
	t := c.Theme()
	b := action.ActionIcon(c, editIcon, action.ActionIconProps{
		BlockSize: interactEntryCompact.box, GlyphSize: interactEntryCompact.icon,
	}).Radius(interactEntryCompact.radius)
	ink := t.TextMuted
	if b.Hovered() {
		ink = t.Text
	}
	b.TextColor(ink).Label("编辑评论").Tooltip("修改这条评论的正文")
	if b.Clicked() {
		a.showReplyEdit(d, r)
	}
}

// replyEntry 是回复入口：和表态 / 打赏一样是一枚图标，放在评论底行最右端。
func (a *app) replyEntry(c *ui.Context, d *detailState, r replyItem) {
	t := c.Theme()
	b := action.ActionIcon(c, replyIcon, action.ActionIconProps{
		BlockSize: interactEntryCompact.box, GlyphSize: interactEntryCompact.icon,
	}).Radius(interactEntryCompact.radius)
	ink := t.TextMuted
	if b.Hovered() {
		ink = t.Text
	}
	b.TextColor(ink).Label("回复").Tooltip("回复这条评论")
	if b.Clicked() {
		d.replyTo = replyTarget{
			ID:       r.ID,
			Level:    r.Level,
			ParentID: r.ParentID,
			Floor:    r.Floor,
			Author:   r.Author,
			AuthorID: r.AuthorID,
		}
		d.composerIdle = false
		// 回复入口在列表行里，Clicked 晚于 replyPane 算展开状态；记一笔让
		// 下一帧补一次向上推，否则底部评论会被展开的输入框盖住。
		if !d.composerExpanded {
			d.composerPushPending = true
		}
		d.composerField.Focus(c)
	}
}

// replyParentLabel 是楼中楼上方那条父评论引用。父评论有楼层号时用「回复 N
// 楼」，父评论本身也是楼中楼（没有 floor）时退回作者名，避免出现「回复 0
// 楼」这种并不存在的楼层。
func replyParentLabel(r replyItem) string {
	return replyQuoteLabel(r.ParentAuthor, r.ParentSummary, r.ParentFloor)
}

// replyQuoteLabel 拼一条引用文案。summary 为空时只报楼层或作者。
func replyQuoteLabel(author, summary string, floor int) string {
	if floor > 0 {
		if summary != "" {
			return fmt.Sprintf("回复 %d 楼：%s", floor, summary)
		}
		return fmt.Sprintf("回复 %d 楼", floor)
	}
	if author != "" {
		if summary != "" {
			return "回复 " + author + "：" + summary
		}
		return "回复 " + author
	}
	if summary != "" {
		return "回复：" + summary
	}
	return ""
}

// replyQuoteTarget 找出三级回复真正在回的那条评论。它的 parent 只是挂载点
// （二级评论），被回复的是 reply_comment_id 里的那条；开放 API 不下发那条
// 评论的正文，只能在已经加载的列表里找。找不到、或本来就不是三级回复时
// 返回 false，调用方退回 parent 的引用。
func replyQuoteTarget(d *detailState, r replyItem) (replyItem, bool) {
	if d == nil || r.ReplyCommentID == "" || r.ReplyCommentID == r.ParentID {
		return replyItem{}, false
	}
	if at := replyIndexByID(d.display, r.ReplyCommentID); at >= 0 {
		return d.display[at], true
	}
	return replyItem{}, false
}

// parentJump 是「回复 N 楼：…」的可点击版本：父评论已经在列表里时，
// 点一下滚到父评论并把高亮移到它身上，方便顺着对话往上读。
func (a *app) parentJump(c *ui.Context, label, parentID string, at int) {
	t := c.Theme()
	tk := lobetheme.TokensOf(c)
	ts := a.typeSizes()
	// ButtonBase 默认把子元素居中；这条引用是正文的一部分，必须和下面的
	// 评论正文左对齐。光靠 Column 的拉伸只会让按钮变宽，里面的文字仍然
	// 居中，所以显式从行首开始排。左侧 padding 留 0，引用文字才不会相对
	// 评论正文缩进；浅色主题铺一层辅助块淡灰（深色保持透明）。
	b := ui.ButtonBase(c).Padding(4, 8, 4, 0).Radius(tk.BorderRadius).
		Background(commentHelperBlockBg(t)).Justify(ui.Start)
	color := t.TextMuted
	if b.Hovered() {
		color = t.Accent
	}
	b.Children(func() {
		ui.Text(c, label).FontSize(ts.meta).TextColor(color).MaxLines(2)
	})
	b.Tooltip("跳到被回复的评论")
	if b.Clicked() && a.detail != nil {
		a.detail.focusID = parentID
		a.detail.focusPending = false
		a.detail.focusMiss = false
		a.detail.list.ScrollTo(at, ui.Start)
	}
}

// reactionBadgeTip 是表情标签的悬停说明：人数 + 我是否也选过。
func reactionBadgeTip(r reactionStat) string {
	tip := r.Emoji
	if r.Count > 0 {
		tip = fmt.Sprintf("%s 有 %d 人表态", r.Emoji, r.Count)
	}
	if r.Mine {
		tip += "，你已表态"
	}
	return tip
}

// rewardBadgeTip 是打赏标签的悬停说明：总额、笔数与打赏者。
func rewardBadgeTip(r rewardStat) string {
	tip := fmt.Sprintf("累计打赏 %d 金币", r.Total)
	if r.Count > 1 {
		tip = fmt.Sprintf("%d 笔打赏，共 %d 金币", r.Count, r.Total)
	}
	if len(r.Users) > 0 {
		tip += "：" + strings.Join(r.Users, "、")
	}
	return tip
}

// replyAction 渲染一个纯文字按钮：透明背景 + 强调色文字，无填充、无边框，
// 用于评论行内的次级操作（在应用内打开、回复）。是否展示由调用方控制
// （通常只在行悬浮时出现）。
func (a *app) replyAction(c *ui.Context, label string, onClick func()) {
	btn := ui.Button(c, label).
		Background(ui.Transparent).
		Border(0, c.Theme().Border).
		TextColor(c.Theme().Accent).
		FontSize(a.typeSizes().meta).
		Padding(2, 7).
		Radius(6)
	if btn.Clicked() {
		onClick()
	}
}

// replyComposer sits at the bottom of the comment pane. It is gated the same
// way the API gates writes: anonymous users are sent to log in, closed posts
// show a hint, and a logged-in open post gets a Markdown editor whose send
// button is integrated into the field. 默认只占一行，把空间留给评论列表。
func (a *app) replyComposer(c *ui.Context, d *detailState) {
	tk := lobetheme.TokensOf(c)
	layout.Divider(c, layout.DividerProps{})
	// 输入栏固定在评论卡片底部：评论区的刷新与回顶已经嵌在发送按钮右侧，
	// 输入框的文字自己让出那一段宽度，这里只需要普通的内边距。折叠态的
	// 内层输入框会补足高度，让整条和左栏正文互动栏齐平。
	ui.Column(c).FillWidth().Padding(replyComposerPadY, 14).Gap(4).
		Label("评论输入栏").Children(func() {
		if d.replyTo.ID != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				// 回复顶层评论时报楼层；回复楼中楼时它没有楼层，改用作者名。
				ui.Text(c, "正在回复 "+d.replyTo.label()).FontSize(tk.FontSizeSM).
					TextColor(tk.Primary).Grow(1)
				a.replyAction(c, "取消", func() {
					d.replyTo = replyTarget{}
				})
			})
		}
		// 评论已关闭时不展示编辑器，仅给提示。
		if d.topic != nil && d.topic.CommentsClosed {
			ui.Text(c, "评论已关闭").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
			return
		}
		if d.contentBlocked() {
			// 无权帖发帖同样 403 且不扣费，别给一个点了必然失败的入口。
			ui.Text(c, "正文受限，暂不能评论").FontSize(tk.FontSizeSM).TextColor(tk.TextDescription)
			return
		}
		a.replyInput(c, d)
	})
}

// replyComposerExpanded 判断评论输入框这一帧是否处于展开态。replyPane 要在
// 构建列表时就认出「这一帧输入框会长高」，好把评论往上推；这里的条件必须和
// replyInput 里真正决定高度的完全一致，否则列表按折叠态排版、编辑器却展开，
// 底部评论仍会被盖住。focused 由调用方读 committed 焦点（两个地方读到同一值）。
func replyComposerExpanded(d *detailState, focused bool) bool {
	if d == nil {
		return false
	}
	// 手动收起优先于一切展开条件：草稿、回复目标与焦点都还在，只是用户
	// 暂时不想看到编辑框（见 composerFold）。
	if d.composerFold {
		return false
	}
	return d.replyTo.ID != "" || strings.TrimSpace(d.draft) != "" || d.focusComposer ||
		(focused && !d.composerIdle) ||
		d.emojiOpen || d.imageOpen || d.mdEditor.topicOpen || d.mdEditor.base64Open ||
		d.preview == 1
}

// replyInput is the Markdown editor for a new comment. It rests one line
// tall, like a quiet channel under the comments, and unfolds into the
// multiline field as soon as it is selected (focused), carries a draft, or
// is answering a floor. Sending folds it back so the list regains the room.
//
// 草稿搬进独立写评论窗口时（见 reply_window.go），这里只剩一行说明与「收回」
// 入口：同一个草稿只留一个输入框，不会两边各写一半。
func (a *app) replyInput(c *ui.Context, d *detailState) {
	if d.composerDetached {
		a.replyDetachedHint(c, d)
		return
	}
	t := c.Theme()
	tk := lobetheme.TokensOf(c)
	// 刚粘进来的图片在这里落地：必须在算 expanded 与构造输入框之前，
	// 和拖放一样，这一帧就要把占位符写进草稿并让输入框展开。
	a.drainPaste(uploadTargetReply)
	focused := d.composerField.Focused(c)
	if !focused {
		d.composerIdle = false // 失焦后再选中，重新展开
	}
	// 工具条、视图切换与图片面板都是输入框的一部分：点开它们会让文本框失焦。
	// 只看焦点决定展开与否，输入框会在点下按钮的那一帧缩回一行，刚打开的内联
	// 面板随之消失（评论列表重新占满、页面往下一沉）。表情、插入帖子与 Base64
	// 编码是 Lobe Modal，但弹窗盖在页面上时仍保留输入框的展开态，关窗后接着写。
	// base64Open 必须算进来：Modal 会把焦点从输入框挪走，少这一条会让输入框
	// 当场缩回、editorPanels 不再构建 Modal，Modal 又随之关闭，来回闪动。
	expanded := replyComposerExpanded(d, focused)
	// 折叠态的输入框是评论区底部的「入口」，不是一块空白的白板：上下留白
	// 比展开态略收一点，整条撑满卡片宽度，鼠标移上去底色提亮一档，暗示点
	// 一下就能写。
	vpad := float32(3)
	if expanded {
		vpad = 8
	}
	box := ui.Column(c).Padding(vpad, 8).Radius(tk.BorderRadius).Background(t.Background)
	// 整个评论框也是拖放区。登记要在 Children 之前；拖进图片后草稿立刻有
	// 内容，这一帧就该展开，让用户看见刚插进来的占位符。
	over := a.editorDropZone(box, uploadTargetReply)
	// 草稿非空时保持展开（粘贴 / 拖放同一帧就落进草稿）；但手动收起优先：
	// 折回一行不该被已有草稿顶回去（见 composerFold）。
	expanded = expanded || (!d.composerFold && strings.TrimSpace(d.draft) != "")
	// 记下这一帧的最终高度状态，供下一帧的 replyPane 认出展开瞬间。
	d.composerExpanded = expanded
	// 展开期间让列表跟着末尾走：编辑器继续长高（多写几行、开面板）时，
	// 底部评论同样被顶上去，不会重新被盖住。折叠时关掉，进详情仍从顶部看。
	d.list.FollowEnd = expanded
	if expanded {
		box = box.FillWidth()
	} else {
		// 折叠态是评论区底部的固定条带：外层 replyComposer 已经占了 1px
		// 分隔线和上下各 replyComposerPadY 的留白，这里补到和左栏正文
		// 互动栏同一高度，两栏底边才齐平。展开后交回内容自己撑高。
		box = box.Height(detailBottomBarHeight - 1 - 2*replyComposerPadY)
	}
	// 三种状态各自说一件事：拖进图片用强调色，编辑中用强调色描边，折叠态
	// 只在悬浮时提亮底色（不抢焦点环的颜色）。展开后是正在写字的地方，
	// 鼠标划过不再改底色，免得编辑面跟着闪。
	switch {
	case over:
		box.Border(1, t.Accent).Background(t.SurfaceHover)
	case focused:
		box.Border(1, t.Accent)
	case !expanded && box.Hovered():
		box.Border(1, t.Border).Background(t.SurfaceHover)
	default:
		box.Border(1, t.Border)
	}
	box.Children(func() {
		if expanded {
			// 折叠态仍只有一行：工具条、视图切换与两个面板都只在展开时出现。
			// 展开后头部只留一行——编辑 / 预览、Markdown 标记、表情 / 图片 /
			// 独立窗口都排在这一行里。原先工具条、视图切换、费用状态栏三层叠
			// 在输入框上方，写评论时头重脚轻；现在费用与发送一起挪到输入框
			// 下方，头部压成一行，正文区才是这一块的主体。
			//
			// 整行带 KeepFocus：按下按钮时焦点仍留在输入框里，光标与选区不动，
			// 插入标记后可以接着打字（见 editorToolbar），输入框也不会因为这一
			// 下失焦而缩回去。
			ui.Row(c).FillWidth().Gap(2).AlignItems(ui.Center).KeepFocus().
				Label("评论视图").Children(func() {
				// 编辑 / 预览：两个图标按钮代替文字 Segmented，平时只显示图标，
				// 悬浮由 Tooltip 说明，选中项用底色区分；保留「评论视图」无障碍名。
				a.editorIconToggle(c, d.preview == 0, editIcon, "编辑", "编辑模式",
					func() { d.preview = 0 })
				a.editorIconToggle(c, d.preview == 1, eyeIcon, "预览", "预览模式",
					func() { d.preview = 1 })
				// 一条短竖线把「编辑 / 预览」与标记区隔开，不再用整条灰带。
				ui.Box(c).Size(1, 14).Background(tk.BorderSecondary)
				a.editorToolbar(c, &d.mdEditor, &d.draft)
				a.editorPanelButtons(c, &d.mdEditor, &d.draft, a.replyCommunityEmojis(d), "插入表情", "插入图片")
				// 独立窗口：把草稿搬到一个单独的 mygo 窗口里写，页面留在
				// 帖子上，一边看正文一边写回复；文字一直留在 d.draft 里，
				// 收回来时原样接着写（见 reply_window.go）。
				if a.editorIconButton(c, expandIcon, "独立窗口写评论",
					"在独立窗口里写评论，一边看帖一边写").Clicked() {
					a.showReplyWindow()
				}
				// 收起输入框：把这个向上展开的编辑框折回一行，草稿与回复目标
				// 都保留，点折叠态的输入框接着写。工具条的其它按钮带
				// KeepFocus（插完标记不丢焦点），这一枚共用同样的外观但不带
				// 它：收起不需要保住焦点，而残留的焦点也不影响折叠——展开
				// 判定里 fold 优先于一切（见 replyComposerExpanded）。
				fold := ui.Button(c, "").
					Border(0, t.Border).
					TextColor(t.Text).
					FontSize(12).
					Padding(editorIconPadY, editorIconPadX).
					Radius(editorIconRadius)
				fold.Background(ui.Transparent)
				if fold.Hovered() {
					fold.Background(t.SurfaceHover)
				}
				fold.Children(func() {
					ui.Icon(c, chevronDownIcon).Size(editorIconSize, editorIconSize).TextColor(t.Text)
				})
				fold.Label("收起输入框").Tooltip("把评论输入框收回一行，草稿保留")
				if fold.Clicked() {
					d.composerFold = true
				}
				// 实时字数：评论正文在客户端不卡硬上限（只有表态通知正文按
				// 2000 字截断），所以只显示当前有多少字，不报上限；超限由
				// 服务端在发送时拦截。登录后字数在下面那条状态栏里报（见
				// comment_cost_ui.go），那里同时报发评金币与徽章建议，同一条
				// 链上不重复写两遍。
				if !a.commentCostBarVisible() {
					ui.Text(c, fmt.Sprintf("%d 字", utf8.RuneCountInString(d.draft))).
						FontSize(11).TextColor(t.TextMuted).SingleLine()
				}
			})
			a.editorPanels(c, uploadTargetReply, &d.mdEditor, &d.draft,
				a.replyCommunityEmojis(d))
			if d.preview == 1 {
				a.editorPreviewView(c, d.draft, a.replyEmojis(d),
					a.commentMdOptions(c), replyPreviewFit(d.draft))
			}
		}
		// 展开时上面会多出工具条、视图切换与面板。MyGo 的节点身份由
		// 「父节点 + 子节点序号」推导，若不给这一行固定 key，多出来的兄弟
		// 会把输入框挤到另一个 ID 上，焦点随之丢失（发送后折不回一行）。
		// 输入框本身是复合控件不能带 key，key 给包着它的 Row。
		c.Key("reply-composer-row")
		// 折叠态：输入框铺满整条，发送 / 计数 / 操作组浮在它右端（下面用
		// Absolute 以这一行为基准摆位）。展开态：输入框独占整行，发送与费用
		// 状态栏落到输入框下面一行（见文末），文字可以一直写到右边缘，不再
		// 被浮层按钮切掉一截。
		ui.Row(c).FillWidth().Children(func() {
			area := a.replyTextArea(c, d, replySurfaceDetail, replyFieldInline, expanded)
			// 从草稿箱恢复后一次性聚焦：只恢复「回谁」，不恢复面板与预览态。
			if d.focusComposer {
				d.composerField.Focus(c)
				d.focusComposer = false
			}
			// Ctrl+Enter 发送评论：绑在输入框自己的 handle 上，只有焦点在
			// 评论框里才生效，免得在正文或评论列表里按组合键也误发一条。
			// 草稿搬到独立窗口时这里不构建输入框，快捷键也随之消失，
			// 那边有自己的窗口级 Ctrl+Enter（见 reply_window.go）。
			d.composerField.OnShortcut(c, ui.Ctrl, ui.KeyEnter, a.submitReply)
			// 给浮在右下角的按钮留出正文位置：输入框本身仍吃满整行（点哪儿
			// 都能接着写），只是文字最右一段不落到按钮底下。左右内边距沿用
			// 输入框自己的默认值，只把右侧加宽。折叠态还并排着评论数与浏览
			// 数两枚计数，要多让出一段，占位文字才不会钻到它们底下。
			pad := t.Space(1.5)
			padX := t.Space(2.5)
			// 展开态发送按钮在输入框下面一行，正文不再为它让出右侧宽度；
			// 折叠态按钮仍浮在输入框右端，文字要让开按钮、两枚计数与操作组。
			var reserve float32
			if !expanded {
				reserve = replySendFloatReserve + replyStatsReserve
				// 打开「发送按钮显示金币数」后按钮会变宽，正文再多让出一段，
				// 免得写字时最后一个字钻到数字底下。
				if a.showCostOnSend() {
					if _, ok := a.commentCostForDraft(d.draft); ok {
						reserve += replySendCostReserve
					}
				}
				// 评论区自己的操作嵌在发送按钮右侧，文字再让出一段，免得
				// 最后一个字钻到图标底下。右栏收起时那组不画，不用留。
				if !d.replyCollapsed {
					reserve += replyComposerToolReserve
					// 停在评论中间时回顶与跳到最新两枚都在，多让出一枚图标。
					if top, latest := replyJumpButtons(d); top && latest {
						reserve += replyJumpExtraReserve
					}
				}
				// 手动收起但草稿还在时，清空按钮也浮在这一行，正文再让一段。
				if d.draft != "" {
					reserve += replyClearReserve
				}
			}
			area.Padding(pad, padX+reserve, pad, padX)
			// @ 候选面板在插入符处展开，没有工具栏入口（见 mentions_ui.go）。
			a.mentionPopover(c, d, replySurfaceDetail, padX, pad)
			// 键入 ":" 时同样的位置弹出社区表情短码候选（见 emoji_shortcode_ui.go）。
			a.emojiShortcodePopover(c, d, replySurfaceDetail, padX, pad)
			// 手动收起后的折叠态：工具条带 KeepFocus，点「收起输入框」焦点仍
			// 留在输入框里，靠焦点跳变认不出用户的下一次点击，这里盖一枚透明
			// 的整行按钮当展开入口，点它清掉折叠标记、按草稿重新展开。普通
			// 折叠态不画，点击输入框照旧聚焦展开。它构建在下面的 footer 之前，
			// 右端的发送与计数浮在它上面，点击不受影响。
			if d.composerFold {
				reopen := ui.Button(c, "").Label("展开评论输入框").
					Tooltip("展开输入框接着写").
					Background(ui.Transparent).Border(0, t.Border).
					Radius(tk.BorderRadius).
					Absolute().Left(0).Top(0).Right(0).Bottom(0)
				if reopen.Clicked() {
					d.composerFold = false
				}
			}
			// 折叠态的发送 / 登录后评论浮在这一行的右端，不参与排版，输入框
			// 因此不被按钮切掉右侧一截。Absolute 以输入框这一行为基准：
			// TopPercent + TranslatePercent 让整排按输入框自身高度上下居中。
			// 发送左边并排着评论数与浏览数、右边嵌着评论操作组（见
			// replyComposerStats）；展开态这两组都收起，改在下面一行。
			if !expanded {
				footer := ui.Row(c).AlignItems(ui.Center).Gap(8).Shrink(0).
					Absolute().TopPercent(50).TranslatePercent(0, -50).Right(0)
				footer.Children(func() {
					a.replyComposerStats(c, d)
					a.replyFieldActions(c, d, true)
					// 评论区操作只在未点击输入框时嵌在发送按钮右侧；已有计数在
					// 发送左边，两组之间加一条竖线区分，整排是「计数 → 发送 → 操作」。
					// 右栏收起时同理——输入框照旧平铺在正文底部，但此刻没有评论区
					// 可操作。
					if !d.replyCollapsed {
						ui.Box(c).Size(1, 18).Background(tk.BorderSecondary)
						a.replyComposerToolBar(c, d)
					}
				})
			}
		})
		// 展开态的底部一行：左边是评论费用状态栏（字数、预计发评金币与徽章），
		// 右边是发送按钮。费用栏收起时左边只留空，发送仍靠右。整行带
		// KeepFocus，按状态栏里的徽章切换不会把焦点从输入框挪走。
		if expanded {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).KeepFocus().
				Label("评论发送栏").Children(func() {
				ui.Box(c).Grow(1).MinWidth(0).Children(func() {
					a.commentCostBar(c, d.draft)
				})
				a.replyFieldActions(c, d, true)
			})
		}
	})
}

// 发送按钮浮在输入框右端时的留白：reserve 是正文让出的右侧宽度（实心发送
// 按钮最宽 44，加上一点间隙；空草稿时按钮只有 28 宽，同样的留白让文字离它
// 更远）。折叠态还要给并排的评论数与浏览数留位，见 replyStatsReserve。
const (
	replySendFloatReserve float32 = 40
	replyStatsReserve     float32 = 112
	// replySendCostReserve 是发送按钮上多出「N 金币」后正文额外让出的宽度。
	replySendCostReserve float32 = 36
	// replyClearReserve 是折叠态草稿非空时给「清空」按钮多留的宽度。
	replyClearReserve float32 = 48
)

// replyComposerStats 画折叠评论框右下角的两枚计数：评论数与浏览数。它们取代了
// 评论区顶部的「评论」标题行与正文副标题里的浏览数，统一成「图标 + 数字」，
// 紧挨着发送按钮的左侧。评论数沿用原来的点击回到评论顶部；浏览数只展示，
// 悬停说明具体次数。输入框点开后这一行不再构建，右下角只留发送按钮。
func (a *app) replyComposerStats(c *ui.Context, d *detailState) {
	tk := lobetheme.TokensOf(c)
	cnt := ui.Button(c, "").
		Background(ui.Transparent).
		Border(0, tk.BorderSecondary).
		TextColor(tk.TextDescription).
		FontSize(tk.FontSizeSM).
		Padding(2, 4).
		Radius(tk.BorderRadiusSM)
	cnt.Children(func() {
		ui.Icon(c, commentBubbleIcon).Size(14, 14).TextColor(tk.TextDescription)
		ui.Textf(c, "%d", d.replyTotal).FontSize(tk.FontSizeSM).Font(sidebarFont).
			TextColor(tk.TextDescription)
	})
	cnt.Label("评论数")
	cnt.Tooltip("回到评论顶部")
	if cnt.Clicked() {
		d.list.ScrollTo(0, ui.Start)
	}
	if d.topic == nil || d.topic.Views <= 0 {
		return
	}
	views := ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(0).Padding(2, 4)
	views.Children(func() {
		ui.Icon(c, eyeIcon).Size(14, 14).TextColor(tk.TextDescription)
		ui.Textf(c, "%d", d.topic.Views).FontSize(tk.FontSizeSM).Font(sidebarFont).
			TextColor(tk.TextDescription)
	})
	views.Label("浏览数")
	views.Tooltip(fmt.Sprintf("%d 次浏览", d.topic.Views))
}

// replyDetachedHint 是草稿在独立窗口里时页面上的那一行：说明去哪儿了，以及
// 把草稿收回页面评论框的入口。文字不复制一份到这里，免得两处不同步。
func (a *app) replyDetachedHint(c *ui.Context, d *detailState) {
	t := c.Theme()
	tk := lobetheme.TokensOf(c)
	ts := a.typeSizes()
	// 提示条走平面样式：不画圆角与整圈描边，靠一层浅底和旁边内容分开。
	row := ui.Row(c).FillWidth().Padding(6, 8).Gap(8).AlignItems(ui.Center).
		Background(tk.FillQuaternary)
	row.Children(func() {
		ui.Icon(c, expandIcon).Size(14, 14).TextColor(t.Accent)
		ui.Text(c, "正在独立窗口里写评论，可以一边看帖一边写").FontSize(ts.meta).
			TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0)
		if ui.Button(c, "收回评论区").
			Tooltip("把草稿收回页面里的评论框，已经写好的文字不变").Clicked() {
			a.closeReplyWindow()
		}
	})
}

// replyTextArea 画输入框本身。页面里的评论框与独立窗口共用它，只是行数与
// 占位文案不同；窗口里它占满左栏高度，页面里随内容长到 10 行。页面里的发送
// 按钮浮在评论框右下角（见 replyInput），输入框因此可以占满整条宽度。
func (a *app) replyTextArea(c *ui.Context, d *detailState, host replySurface, mode replyFieldMode, expanded bool) ui.Element {
	t := c.Theme()
	// 多行文本区即 Markdown 编辑器：透明背景、去边框，融入外层输入框。
	// 三个模式都 Grow(1)：输入框占满整条，不再给行尾的动作让位。
	// OnPaste 让直接粘进来的图片自动上传（见 pasteIntoEditor），文字仍按
	// 原样插入。
	// 输入场景不跟随阅读字号：评论框按基准字号画，锚点排版（mentions /
	// emoji 补全）也用同一个基准，改排版不会让正在打的字忽然挪位。
	area := ui.TextArea(c, &d.draft).Label("写评论").FontSize(typeBase.comment).
		MinHeight(0).Background(ui.Transparent).
		Border(0, t.Border).
		OnPaste(func(text string) bool {
			return a.pasteIntoEditor(uploadTargetReply, text)
		}).
		Bind(&d.composerField)
	switch {
	case mode == replyFieldWindow:
		area.Grow(1).MinHeight(0).Lines(8, 40).AutoFocus().
			Placeholder("说点什么…（支持 Markdown、表情短码 :smile:，图片可直接拖进来或粘贴）")
	case expanded:
		area.Grow(1).Lines(4, 10).
			Placeholder("说点什么…（支持 Markdown、表情短码 :smile:，图片可直接拖进来或粘贴）")
	default:
		area.Grow(1).Lines(1, 8).Placeholder("写评论…")
	}
	// 写完的 @名字 在编辑区里上色加粗成一块（mygo 的 TextRange 正是以
	// mentions 为例设计的）；提交给服务端的仍是纯文本 "@名字 "。
	var ranges []ui.TextRange
	for _, b := range d.mentions.blocksFor(d.draft) {
		ranges = append(ranges, ui.TextRange{
			Start: b.Start, End: b.End, Color: t.Accent, Weight: 600,
		})
	}
	if len(ranges) > 0 {
		area.TextRanges(ranges...)
	}
	d.syncSelection(area)
	// 光标每帧都在动（点一下、按方向键），@前缀 跟着光标重新认一次：面板
	// 因此贴在光标所在的那个 @ 上，而不是上一次打字的位置。只在焦点就在这个
	// 输入框里时做——页面与独立写评论窗口共用一份状态，没焦点的那一边别把
	// 自己的选区按到面板头上。
	if d.composerField.Focused(c) {
		a.syncMentionToken(d)
		a.syncEmojiShortcodeToken(d)
	}
	// 键入 @ / : 时自动弹出候选：OnChange 在输入落到正文后、重建前运行，
	// 这里的 element 仍有效，能读到真实光标位置。OnTextKey 则在帧间运行，
	// 只用状态与 k.SetText 处理上下键 / 回车 / Escape 与整块删除
	// （见 mentions_ui.go、emoji_shortcode_ui.go）。
	area.OnChange(func() {
		_, caret := area.TextSelection()
		a.onMentionInput(d, host, caret)
		a.onEmojiShortcodeInput(d, host, caret)
	})
	area.OnTextKey(func(k ui.TextKey) bool {
		return a.emojiShortcodeTextKey(d, k) || a.mentionTextKey(d, k)
	})
	return area
}

// clearReplyDraft 清空评论草稿。正在回复的目标留着（它有独立的「取消」入口），
// @ 与短码候选一起收掉，焦点还给输入框，清空后接着写。
func (a *app) clearReplyDraft(c *ui.Context, d *detailState) {
	d.draft = ""
	d.mdEditor.pendingSel = composeSelection{}
	d.mentions.reset()
	d.emojiShortcodes.reset()
	d.composerField.Focus(c)
}

// replyFieldActions 画输入框右下角那一个动作：未登录时是「登录后评论」，
// 登录后是发送按钮。两个动作都只显示图标，文字说明交由 Tooltip（Label 仍
// 保留文字作无障碍名与点击测试的命中名）。草稿非空时前面多一枚「清空」。
//
// 它返回一个 Row，由调用方决定摆在哪里：页面里浮在评论框右下角（见
// replyInput），独立写评论窗口里放在页脚、和账号标签并排（见
// replyWindowBody）。发送中时转圈也在这里，跟着动作一起走。
//
// detailPage 表示这是详情页的评论框。这里草稿为空时发送按钮禁用：没内容可
// 发，点它不该弹「评论内容不能为空」的 toast。折叠态是写评论的入口，展开态
// 是正在写字的地方，两者都守同一条规则；有草稿后按钮恢复实心可点。独立写评
// 论窗口传 false，保留原来可点的弱化按钮。
func (a *app) replyFieldActions(c *ui.Context, d *detailState, detailPage bool) ui.Element {
	t := c.Theme()
	// 发送按钮上的金币数要用到徽章数据。开关关着时完全不碰网络（默认如此）；
	// 打开后即使状态栏关着，也要在写评论时按需读一次。有缓存与退避，每帧调用
	// 不会重复发请求。
	if a.showCostOnSend() {
		a.ensureCommentCostData()
	}
	row := ui.Row(c).AlignItems(ui.Center).Gap(6).Shrink(0)
	row.Children(func() {
		if d.posting {
			lobeSpin(c, feedback.SpinProps{CustomSize: 16})
		}
		// 有草稿才给清空入口：页面评论框与独立写评论窗口共用这一行，
		// 两处都能一键清掉正文，接着在原处写。KeepFocus 让点击时不丢光标，
		// 空草稿后按钮随之消失。
		if d.draft != "" {
			clear := action.Button(c, "清空", action.ButtonProps{
				Size: lobetheme.Small, Variant: action.Text,
			}).Label("清空").KeepFocus().Disabled(d.posting)
			if clear.Clicked() {
				a.clearReplyDraft(c, d)
			}
		}
		if !a.auth.loggedIn() {
			login := ui.Button(c, "").
				Background(ui.Transparent).
				Border(0, t.Border).
				TextColor(t.Text).
				Padding(4, 6).
				Radius(6)
			login.Children(func() {
				ui.Icon(c, loginIcon).Size(16, 16).TextColor(t.Text)
			})
			login.Label("登录后评论").Tooltip("登录后才能发表评论")
			if login.Clicked() {
				a.requestLoginFor(a.replyAuthTarget(), "登录后才能发表评论。")
			}
			return
		}
		// 空草稿时发送按钮只是一枚弱化的图标：没有内容可发，实心的强调色
		// 按钮会把整条输入栏的重量压到右边，看着像「已经可以发」。有内容才
		// 换成实心的主按钮，这时它才是这一行里唯一该被看见的动作。
		empty := strings.TrimSpace(d.draft) == ""
		// 详情页草稿为空时禁掉发送按钮：没内容可发，点它不再弹「评论内容
		// 不能为空」的 toast。展开态与折叠态守同一条规则，免得点一下展开的
		// 空发送按钮时，输入框失焦折回一行、按钮恰好被禁用而吞掉这次点击。
		// 独立写评论窗口不在这里禁，空草稿的弱化按钮仍可点（submitReply 的
		// 空判断兜着）。
		sendDisabled := d.posting || (detailPage && empty)
		// 两种形态用同一份内边距：PrimaryButton 默认上下更厚，打字瞬间
		// 按钮会长高 4px，把展开态的发送栏和整条输入栏一起顶上去。
		const sendPadY, sendPadX float32 = 4, 6
		send := ui.Button(c, "").
			Background(ui.Transparent).
			Border(0, t.Border).
			Padding(sendPadY, sendPadX).
			Radius(8)
		ink := t.TextMuted
		if !empty {
			send = ui.PrimaryButton(c, "").Radius(8).Padding(sendPadY, sendPadX)
			ink = t.AccentText
		}
		if send.Hovered() && !sendDisabled {
			ink = t.Text
			if empty {
				send.Background(t.SurfaceHover)
			}
		}
		send.Disabled(sendDisabled)
		send.Children(func() {
			ui.Icon(c, sendIcon).Size(16, 16).TextColor(ink)
			// 「在发送按钮上显示消耗金币数」默认关；只有数据已到才画，
			// 不编一个不准确的数出来。数字与状态栏共用 commentCostEstimate，
			// 两处口径一致。
			if a.showCostOnSend() {
				if n, ok := a.commentCostForDraft(d.draft); ok {
					ui.Text(c, fmt.Sprintf("%d 金币", n)).FontSize(11).
						TextColor(ink).SingleLine()
				}
			}
		})
		send.Label("发送").Tooltip("发送评论（Ctrl+Enter）")
		if send.Clicked() {
			a.submitReply()
		}
	})
	return row
}

// replyCommunityEmojis 是当前帖子自带的社区表情表；没有正文数据时为空。
func (a *app) replyCommunityEmojis(d *detailState) map[string]string {
	if d == nil || d.topic == nil {
		return nil
	}
	return d.topic.Emojis
}

// replyEmojis 是评论预览用的完整表情表：先全局表，再叠上当前帖子的社区
// 自定义图标（同名短码以帖子为准），和详情页正文渲染走的是同一份合并。
func (a *app) replyEmojis(d *detailState) map[string]emojiInfo {
	return mergeEmojiMaps(a.emojis, a.replyCommunityEmojis(d))
}

// replyAuthTarget 是评论相关请求的授权窗口：草稿在独立写评论窗口里写，弹窗
// 就回到那个窗口；否则回到主窗口。
func (a *app) replyAuthTarget() toastTarget {
	if a.replyWindowOpen() {
		return toastReply
	}
	return a.detailToast()
}

// replyToast 把评论输入相关的结果弹在「正在写」的那个窗口里：草稿在独立
// 窗口里写就弹在窗口底部，否则弹在主窗口底部。窗口不在了由 toast 的退回
// 规则交给主窗口，结果不会丢。
func (a *app) replyToast(text string) {
	if a.replyWindowOpen() {
		a.toast(toastReply, text)
		return
	}
	a.toast(a.detailToast(), text)
}

// submitReply posts the draft through the Client. On success it clears the
// box, inserts the created comment where the current view expects it and
// scrolls to it, so the author sees it immediately in floor order.
//
// 发送失败（含校验不通过）只在窗口底部弹一条 toast：输入框保持原样、可以
// 直接改，页面上不留一行红字。
func (a *app) submitReply() {
	d := a.detail
	if d == nil || d.posting {
		return
	}
	content := strings.TrimSpace(d.draft)
	if content == "" {
		a.replyToast("评论内容不能为空")
		return
	}
	// 潘多拉的「多说多写」对评论字数有硬要求：没到门槛就发不出去，先在本地
	// 拦下来，免得白跑一次请求（判断依据见 comment_cost.go）。
	if short, minLength := a.commentCostShortfall(content); short > 0 {
		a.replyToast(fmt.Sprintf("还差 %d 字才能发送（潘多拉要求 ≥%d 字）", short, minLength))
		return
	}
	if n := editorPendingUploads(d.draft); n > 0 {
		a.replyToast("还有 " + strconv.Itoa(n) + " 张图片未处理完，请等上传结束或移除后再发送")
		return
	}
	if a.auth == nil || !a.auth.loggedIn() {
		a.replyToast("请先登录后再评论")
		return
	}
	if d.topic != nil && d.topic.CommentsClosed {
		a.replyToast("该帖子已关闭评论")
		return
	}
	target := d.replyTo
	// 正文里的「@被回复人」由服务端在需要时补（三级回复），客户端不自己加：
	// 重复补不会多收通知，但会让人以为发出去的内容和响应不一致。响应里的
	// content 才是最终正文，见 createReply。
	// parentID 是挂载点（三级回复挂在二级评论下），target.ID 才是被回复的
	// 那条。列表落位用挂载点，引用与跳转用 target。
	parentID := target.postParentID()
	parentFloor := target.Floor
	parentName := target.Author
	parentSummary := ""
	if at := replyIndexByID(d.display, target.ID); at >= 0 {
		parentSummary = truncate(commentExcerpt(d.display[at].Content), 60)
		if parentName == "" {
			parentName = d.display[at].Author
		}
	}
	d.posting = true
	client := a.client
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		created, err := client.createReply(ctx, d.shortID, content, target)
		a.publish(func() {
			if !a.detailLive(d) {
				return // navigated away; drop the stale result
			}
			d.posting = false
			if err != nil {
				// 缺 write:comment 时先补授权，回调成功后按同样的内容与父
				// 评论重发一次，用户不用重新输入。
				a.handleAuthErrorFor(a.replyAuthTarget(), err)
				a.replyToast(friendlyError(err))
				// 发送失败（网络抖动、超时）后马上把草稿落盘：这段时间里用户
				// 可能关窗或进程被动结束，不能只等 800ms 防抖，否则草稿会丢。
				a.flushDrafts()
				if a.onReplyPosted != nil {
					a.onReplyPosted(replyItem{}, err)
				}
				return
			}
			if created.Author == "" || created.Author == "匿名" {
				if u := a.auth.username(); u != "" {
					created.Author = u
					if created.Avatar == "" {
						created.Avatar = a.auth.avatarURL()
					}
				}
			}
			// 只有顶层评论才有楼层号。回复别人的评论不占楼层，不能把
			// flat_floor 提成 floor，否则刚发的楼中楼会显示成「N 楼」。
			if parentID == "" && created.Floor == 0 && created.FlatFloor > 0 {
				created.Floor = created.FlatFloor
			}
			// 服务端没回父评论信息时用提交前的目标补齐，刚发的楼中楼
			// 立刻就能显示父评论引用并点回去；父评论没有楼层时引用作者名。
			// 三级回复的 parent 是二级评论，真正回的那条记在 ReplyCommentID。
			if created.ParentID == "" {
				created.ParentID = parentID
			}
			if created.ReplyCommentID == "" {
				created.ReplyCommentID = target.ID
			}
			// 201 常常只回 id / 楼层，不带 level，decode 会按深度兜底成 1。
			// 刚发的回复按提交的目标补齐，否则它在列表里会被当成一级评论
			// （缩进少一层，再回复它时又会走错分支）。
			if created.Level <= 1 && target.ID != "" {
				created.Level = target.replyDepth()
			}
			if created.ParentFloor == 0 {
				created.ParentFloor = parentFloor
			}
			if created.ParentAuthor == "" {
				created.ParentAuthor = parentName
			}
			if created.ParentSummary == "" {
				created.ParentSummary = parentSummary
			}
			d.draft = ""
			d.replyTo = replyTarget{}
			// 回复草稿随发送成功删除：箱子里不该再留一条已经发出去的内容。
			a.deleteDraft(replyDraftID(d.shortID))
			d.composerIdle = true  // 发送后折回一行，把空间还给评论列表
			d.composerFold = false // 发出去就回到默认状态，下次点开直接能写
			// 工具面板与预览会让输入框保持展开（见 replyInput），发送后一并
			// 收起，否则空输入框还占着面板的高度，折不回一行。
			d.emojiOpen, d.imageOpen, d.preview = false, false, 0
			// 草稿是在独立窗口里写完的：评论已经发出去了，窗口没有可写的内容，
			// 一并收起，主窗口回到评论区。
			if d.composerDetached {
				a.closeReplyWindow()
			}
			// 按当前视图放到正确的位置：楼中楼落在父评论的子树后面，
			// 顶层与扁平时间序接在已加载评论之后，而不是插到最顶端。
			// 插到正确位置后再重建显示列表，并在显示空间里定位刚发的评论
			// 滚到视口底部（折叠会把行号打乱，必须用 display 而非 replyList）。
			d.replyList, _ = insertReply(d.replyList, created, parentID, d.flat)
			d.rebuildDisplay()
			if at := replyIndexByID(d.display, created.ID); at >= 0 {
				d.list.ScrollTo(at, ui.End)
			}
			d.replyTotal++
			a.prefetchAvatars(created.Avatar)
			if a.onReplyPosted != nil {
				a.onReplyPosted(created, nil)
			}
		})
	}()
}
