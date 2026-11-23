package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Read endpoints used by the in-app detail page:
//
//	GET /v1/topics/:short_id                  postscripts / vote / reactions
//	GET /v1/topics/:short_id/replies         items + pinned, ?flat=1 supported
//	GET /v1/emojis                          :code: -> img_url mapping
//
// Paths, query names and field names are copied verbatim from api-read.md.
// 评论接口放宽后每页 100 条（默认值与上限都是 100）；站点自己的评论页
// 仍按 30 条分页，永久链接的 p 见 comments.go 的 siteCommentPageSize，
// 两者不是同一个东西。
const replyLimit = 100

// topicPath escapes the short_id so a malformed list row can never turn
// into a different endpoint (or a path outside /v1/topics).
func topicPath(shortID string) string {
	return "/v1/topics/" + url.PathEscape(shortID)
}

func repliesPath(shortID string, page int, flat bool) string {
	if page < 1 {
		page = 1
	}
	p := fmt.Sprintf("%s/replies?page=%d&limit=%d", topicPath(shortID), page, replyLimit)
	if flat {
		p += "&flat=1"
	}
	return p
}

func (c *Client) apiURL(pathQuery string) string {
	return strings.TrimSuffix(c.base(), "/") + pathQuery
}

// topicDetail only keeps the stable fields from api-read.md. Every decoder
// below ignores unknown fields, as the v1 compatibility promise requires.
type topicDetail struct {
	ShortID, Title, Content, Type string
	Author                        string
	AuthorID                      string // 作者 uuid，表态接口的 recUserId 需要
	Avatar                        string // 作者头像 URL，标题下方展示
	Badges                        []badgeInfo
	AuthorIsPro                   bool // 作者是 Pro 会员时头像带官网 Pro 图标
	AuthorLevel                   int
	NodeName, NodeSlug            string
	// NodeParentName / NodeParentSlug 是节点上一级：线上详情把父节点放在
	// node.parent 里（同时带 parent_slug），列表接口只带 parent_slug。
	// 两级分类据此显示成「父 · 子」，只有 slug、拿不到父名时退回一级。
	NodeParentName, NodeParentSlug string
	// NodeID 是节点 uuid：表态接口把它作为必填的 nodeId。NodeEmojis 是节点
	// 推荐的表态表情（main_emojis + sub_emojis），表态选择器直接用它。
	NodeID                   string
	NodeEmojis               []string
	CreatedAt, UpdatedAt     string
	Views, CommentCount      int
	IsPinned, CommentsClosed bool
	Tags                     []string
	Postscripts              []postscript
	Vote                     *voteBlock
	// Bet 是竞猜块：线上在 d.bet，早期文档在 d.extra.bet（见 bets.go）。
	Bet *betInfo
	// Restricted 是隐藏内容的门槛（api-read.md 的 extra.restricted）：
	// 未解锁时只有条件，已解锁时可能带正文，字段按受限内容的容错解码读取。
	Restricted *restrictedInfo
	// Redeem 是兑换码块（api-read.md 的 extra.redeem_codes）：每条带 id 与
	// 脱敏的 code，已领取的还带 redeemed_by。点未领取的码按 id 领取，见 redeem.go。
	Redeem *redeemBlock
	// RewardPool 是随帖的金币池状态。线上写在详情顶层 d.reward_pool，
	// 早期文档写在 d.extra.reward_pool：两处都认，见 decodeTopicRewardPool。
	RewardPool *rewardPoolInfo
	// Lottery 是开奖结果（extra.lottery）。出现它表示这篇帖子已经开过奖，
	// 同一帖只能开一次；触发与轮询见 lottery.go。
	Lottery *lotteryInfo
	// 收藏状态：favorited 只在带令牌时有意义。字段缺失时 FavoriteKnown 为
	// false，按钮按未收藏展示，点一下以服务端结果为准。
	Favorited     bool
	FavoriteKnown bool
	// FavoritedCount 是这篇帖子的收藏数。FavoriteCountKnown 区分「确实 0 人
	// 收藏」和「响应里没有这个字段」：后者不该在按钮旁边凭空写一个 0。
	FavoritedCount     int
	FavoriteCountKnown bool
	// 合集归属：文档的稳定字段里没有它。线上实测在 d.post_collection
	// （没归入时是 null），老响应可能是 collection_id / collection /
	// collections。CollectionKnown 区分「确实没归入合集」和「响应里没有这个
	// 字段」——后者按钮只能提供归入，不能声称「没在合集里」。
	CollectionID    string
	CollectionName  string
	CollectionCount int
	CollectionKnown bool
	// CollectionPrev / CollectionNext 是详情接口 post_collection 里直接给出的
	// 上 / 下一篇（short_id / node_slug / title）。有它们就不必再拉合集内帖子
	// 列表去找位置，卡片点一下就能打开。
	CollectionPrev *collectionNeighbor
	CollectionNext *collectionNeighbor
	// AccessGuard* 是可见性门槛（登录 / 等级 / 属性）。读接口的稳定字段表里
	// 没有它，但线上详情会带；GuardKnown 区分「确实不限制」与「响应没有这个
	// 字段」：编辑窗口在后者只能让作者重新选，不能替他清掉已有门槛。
	GuardType      string
	GuardLevel     int
	GuardAttr      string
	GuardAttrValue int
	GuardKnown     bool
	// GuardPassed 是 access_guard.passed：false 表示当前令牌无权看这篇帖子，
	// 服务端已按 11-19 口径把内容块置空，客户端要渲染访问墙（而不是继续画
	// 一个空白正文）。GuardPassedKnown 区分「明确返回 false」与「老响应 /
	// 离线缓存没有这个键」：后者按可见处理，避免把正常帖子误判成无权。
	GuardPassed      bool
	GuardPassedKnown bool
	// CanEdit / CanEditNode 是 11-20 契约里的两个窗口状态：作者本人（含匿名
	// 帖真实发布者）+ 发帖后 10 分钟内、各自成功后关闭，两者互相独立。
	// 它们只回答「窗口还开着吗」，不是权限判断：账户被限制编辑时字段仍可能
	// 为 true，真正调用才会拿到 403 permission_denied。Known 区分「服务端
	// 明确给了值」与「老响应 / 离线缓存没有这个键」，后者退回本地作者判断。
	CanEdit, CanEditNode           bool
	CanEditKnown, CanEditNodeKnown bool
	// Reactions 是帖子收到的表情表态汇总；Rewards 是打赏合计与打赏者。
	Reactions []reactionStat
	Rewards   rewardStat
	// Emojis 是这篇帖子自己的短码 → 图片映射；社区自定义图标会出现在
	// 这里，值可能是 /emojis/xxx.png 这样的相对地址。
	Emojis map[string]string
	URL    string // 原文链接，展示内容时必须保留
}

// collectionNeighbor 是合集里当前帖子的上 / 下一篇。详情接口直接给出
// short_id / node_slug / title，不必再拉合集内帖子列表去找位置。
type collectionNeighbor struct {
	ShortID  string
	NodeSlug string
	Title    string
}

// reactionStat 是一种表情的聚合表态：谁、多少人、我是否也选了它。
// 线上字段名是 reactions_summary，my_reactions 单独给出我选过的表情。
type reactionStat struct {
	Emoji string
	Count int
	Mine  bool
}

// rewardStat 是一条内容收到的打赏汇总：金额合计、打赏次数（按记录数）与
// 打赏者昵称；同一个人多次打赏只记一次昵称。
type rewardStat struct {
	Total int
	Count int
	Users []string
}

type postscript struct {
	Content, CreatedAt string
	Emojis             map[string]string
}

// badgeInfo 是用户佩戴的社区徽章；icon_url 是 22×15 的小图，直接用于
// 昵称旁。佩戴「忽冷忽热」时服务端还会带 weather，网页会用天气图标和
// 温度代替徽章图标。
type badgeInfo struct {
	Name, Code, IconURL string
	Weather             *weatherInfo
	// Status 是「我来过」徽章的一句状态（如「在搬砖」），展示在徽章右侧。
	// 只有 badge.code == "i_was_here" 的项才可能有这个值；服务端已经按有效期
	// 过滤过，客户端不按 updated_at 自己判断，也不把它写进本机缓存。
	Status *badgeStatusInfo `json:"-"`
}

// badgeCodeIWasHere 是「我来过」徽章的机器码：equipped_badges 里只有这一项
// 会带 status。
const badgeCodeIWasHere = "i_was_here"

// badgeStatusInfo 是徽章状态里我们真正展示的部分。updated_at 只用于排查，
// 解码时直接忽略。
type badgeStatusInfo struct {
	Text string
}

type weatherInfo struct {
	SymbolCode  string
	Temperature float64
}

type voteBlock struct {
	// ID 是投票 uuid：提交时作为 voteId（见 vote.go）。
	ID   string
	Type string // pk（单选）/ multiple（多选），与 MaxChoice 一起决定走哪个端点
	// 标题与选项都可能有缺失，投票块按「有标题或有选项」判断是否存在。
	Title     string
	Finished  bool
	Options   []voteOption
	MaxChoice int
	// Voted / VotedKnown 是带令牌时详情可能一起下发的「我是否已投」。投过票
	// 的人有权直接看分布，不必再点「查看投票结果」（见 loadTopicFor）。
	Voted      bool
	VotedKnown bool
}

type voteOption struct {
	// ID 是选项 uuid：提交时作为 optionId / optionIds（见 vote.go）。
	ID    string
	Text  string
	Votes int
}

// replyItem 是一条评论。楼层视图里的 children 会按 Depth 摊平显示，界面
// 不需要递归布局。
type replyItem struct {
	ID, Content, Author, CreatedAt string
	// AuthorID 是评论作者的 uuid：表态接口的 recUserId 必填，和帖子作者
	// 一样只能从响应里取，不能拿昵称代替。
	AuthorID string
	// Avatar 是评论作者的头像地址；统一用户标识据此在昵称旁画头像。
	Avatar                         string
	AuthorIsPro                    bool // 评论作者是 Pro 会员时头像带 Pro 图标
	Floor, FlatFloor, LocatedFloor int
	// Level 是评论在楼层树里的层级：1 = 楼层评论，2 = 楼中楼，3 = 三级回复。
	// 站点最多三级，回复三级评论产生的是同级的兄弟节点，不是第四层
	// （见 replyTarget）。服务端没给 level 时按树里的深度兜底。
	Level    int
	ParentID string // 被回复评论的 uuid，点父评论引用时用来定位
	// ReplyCommentID 是真正被回复的那条评论。三级回复的 ParentID 停在二级
	// 评论上，想回的是谁只能看这个字段；一级 / 二级评论两者是同一个值。
	ReplyCommentID  string
	ParentFloor     int    // 父评论的楼层；父评论也是楼中楼时为 0
	ParentAuthor    string // 父评论作者，父评论没有楼层时用于引用
	ParentSummary   string
	Reactions       []reactionStat
	Rewards         rewardStat
	Emojis          map[string]string
	Badges          []badgeInfo
	Deleted, Pinned bool
	// Bookmarked / BookmarkKnown 是这条评论的书签状态。带令牌时服务端会下发
	// is_bookmarked；匿名响应没有这个键，不能当成「没加书签」。
	Bookmarked    bool
	BookmarkKnown bool
	// RewardPoolWinnings 是这条评论从帖子金币池里领到的金币（元为单位，按
	// 笔保留）。服务端下发 reward_pool_rewards: [{"amount": 53}]，没有命中
	// 时是空数组。楼中楼不会中奖，所以 children 里通常都是空的。
	RewardPoolWinnings []int
	// UserPKChoice 是 PK 投票里这位评论者选的选项 uuid，和详情 vote.options[].id
	// 对应（见 replyRow 的队伍标签）。老响应或没投票的评论没有这个字段 / 为空串。
	UserPKChoice string
	Depth        int
	Children     []replyItem
	// HasChildren 标记这条评论在原始数据里带子树（楼中楼）。flattenReplies
	// 把树摊平后 Children 被清空，但保留这个标志，供楼层视图在评论旁显示
	// 折叠 / 展开入口，并据此隐藏被折叠的子回复。
	HasChildren bool
}

type replyPage struct {
	Items, Pinned                  []replyItem
	Total, Page, Limit, TotalPages int
	// Blocked 表示响应 d 为 null：帖子不存在，或当前令牌无权访问（api-read.md
	// 的评论端点）。这两种情况外形相同，客户端只需要知道「不是真的没评论」，
	// 不能拿空数组画成「暂无评论」。
	Blocked bool
}

type emojiInfo struct {
	Code, Name, ImgURL string
}

func (c *Client) fetchTopic(ctx context.Context, shortID string) (topicDetail, error) {
	return withAuthRetry(c, ctx, func(token string) (topicDetail, error) {
		d, err := c.getJSON(ctx, c.apiURL(topicPath(shortID)), token)
		if err != nil {
			return topicDetail{}, err
		}
		return decodeTopic(d, shortID)
	})
}

func (c *Client) fetchReplies(ctx context.Context, shortID string, page int, flat bool) (replyPage, error) {
	return withAuthRetry(c, ctx, func(token string) (replyPage, error) {
		d, err := c.getJSON(ctx, c.apiURL(repliesPath(shortID, page, flat)), token)
		if err != nil {
			return replyPage{}, err
		}
		return decodeReplies(d), nil
	})
}

func (c *Client) fetchEmojis(ctx context.Context) (map[string]emojiInfo, error) {
	return withAuthRetry(c, ctx, func(token string) (map[string]emojiInfo, error) {
		d, err := c.getJSON(ctx, c.apiURL("/v1/emojis"), token)
		if err != nil {
			return nil, err
		}
		return decodeEmojis(d), nil
	})
}

func decodeTopic(d json.RawMessage, requested string) (topicDetail, error) {
	o := asObject(d)
	if o == nil {
		// A hidden / deleted post can come back as 200 + {d:null}.
		return topicDetail{}, &apiError{
			Status: 404, Code: "not_found", Message: "帖子不存在或不可见",
		}
	}
	shortID := o.str("short_id")
	if shortID == "" {
		shortID = requested
	}
	t := topicDetail{
		ShortID:        shortID,
		Title:          o.str("title"),
		Content:        o.str("content"), // raw markdown, rendered by the UI
		Type:           o.str("type"),
		Author:         "匿名", // 匿名帖的 author 为 null，必须判空
		CreatedAt:      o.str("created_at"),
		UpdatedAt:      o.str("updated_at"),
		Views:          o.num("views"),
		CommentCount:   o.num("comment_count"),
		IsPinned:       o.flag("is_pinned"),
		CommentsClosed: o.flag("comments_closed"),
		URL:            topicURL(shortID, ""), // 没有 node 时也要有原文链接
	}
	if a := o.sub("author"); a != nil {
		if name := a.str("username"); name != "" {
			t.Author = name
		}
		t.AuthorID = a.str("id")
		t.AuthorLevel = a.num("level")
		t.Avatar = authorAvatar(a)
		t.Badges = decodeBadges(a)
		t.AuthorIsPro = a.flag("is_pro", "isPro")
	}
	if n := o.sub("node"); n != nil {
		t.NodeName = n.str("name")
		t.NodeSlug = n.str("slug")
		t.NodeID = n.str("id")
		t.NodeEmojis = nodeEmojiList(n)
		// 父节点线上在 node.parent 里；部分响应只带 parent_slug。没有父名
		// 时仍只显示一级分类，不拿 slug 顶替名字。
		parent := n.sub("parent", "parent_node")
		if parent == nil {
			parent = o.sub("parent_node")
		}
		if parent != nil {
			t.NodeParentName = parent.str("name")
			t.NodeParentSlug = parent.str("slug")
		}
		if t.NodeParentSlug == "" {
			t.NodeParentSlug = n.str("parent_slug")
		}
		t.URL = topicURL(shortID, t.NodeSlug)
	}
	// favorited 在匿名响应里是 false，但要靠字段本身判断「是不是知道」：
	// 老版本接口或未登录时可能根本不带这个键。
	if o.has("favorited", "is_favorited") {
		t.FavoriteKnown = true
		t.Favorited = o.flag("favorited", "is_favorited")
	}
	if o.has("favorited_count") {
		t.FavoriteCountKnown = true
		t.FavoritedCount = o.num("favorited_count")
	}
	// 合集归属不在文档的稳定字段里：有就展示，没有就按未知处理。
	if o.has("collection_id") {
		t.CollectionKnown = true
		t.CollectionID = o.str("collection_id")
	}
	if col := o.sub("collection"); col != nil {
		t.CollectionKnown = true
		if id := col.str("id"); id != "" {
			t.CollectionID = id
		}
		t.CollectionName = cleanText(col.str("name", "title"))
	}
	if list := rawArray(o["collections"]); len(list) > 0 {
		t.CollectionKnown = true
		if first := asObject(list[0]); first != nil {
			if id := first.str("id"); id != "" {
				t.CollectionID = id
			}
			if name := cleanText(first.str("name", "title")); name != "" {
				t.CollectionName = name
			}
		}
	}
	// 线上真实字段是 d.post_collection：对象（可能再包一层 collection），
	// 也可能是数组。它比上面那些兼容形状更准确，读到了就覆盖。
	if raw := o["post_collection"]; len(raw) > 0 {
		if list := rawArray(raw); len(list) > 0 {
			raw = list[0]
		}
		if col := asObject(raw); col != nil {
			if inner := col.sub("collection"); inner != nil {
				col = inner
			}
			if id := col.str("id", "collection_id"); id != "" {
				t.CollectionID = id
				t.CollectionKnown = true
			}
			if name := cleanText(col.str("name", "title")); name != "" {
				t.CollectionName = name
				t.CollectionKnown = true
			}
			if n := col.num("topic_count", "post_count", "item_count", "count"); n > 0 {
				t.CollectionCount = n
			}
			t.CollectionPrev = decodeCollectionNeighbor(col["prev"])
			t.CollectionNext = decodeCollectionNeighbor(col["next"])
		}
	}
	for _, raw := range rawArray(o["tags"]) {
		tag := asObject(raw)
		if tag == nil {
			continue
		}
		if name := tag.str("name"); name != "" {
			t.Tags = append(t.Tags, name)
		}
	}
	// 可见性门槛：文档只把 access_guard_* 列在编辑端点的请求体里，读响应
	// 可能平铺在顶层，也可能收在 access_guard 对象里；两种都认。字段缺失时
	// 保持 GuardKnown=false，编辑窗口据此只允许「重新设置」，不会默认清除。
	if g := o.sub("access_guard"); g != nil {
		t.GuardKnown = true
		t.GuardType = g.str("type", "access_guard_type")
		t.GuardLevel = g.num("level", "access_guard_level")
		t.GuardAttr = g.str("attr", "attribute", "access_guard_attr")
		t.GuardAttrValue = g.num("attr_value", "attribute_value", "access_guard_attr_value")
		// 文档把 passed 放在 access_guard 里；线上偶尔平铺在顶层，两处都认。
		t.GuardPassed, t.GuardPassedKnown = boolFieldOf(g, "passed")
		if !t.GuardPassedKnown {
			t.GuardPassed, t.GuardPassedKnown = boolFieldOf(o, "access_guard_passed")
		}
	} else if o.has("access_guard_type", "access_guard_level", "access_guard_attr") {
		t.GuardKnown = true
		t.GuardType = o.str("access_guard_type")
		t.GuardLevel = o.num("access_guard_level")
		t.GuardAttr = o.str("access_guard_attr")
		t.GuardAttrValue = o.num("access_guard_attr_value")
		t.GuardPassed, t.GuardPassedKnown = boolFieldOf(o, "access_guard_passed")
	}
	// 编辑 / 改节点两个窗口状态：字段可能是布尔，缺失时按老响应处理。
	if v, known := boolFieldOf(o, "can_edit"); known {
		t.CanEdit, t.CanEditKnown = v, true
	}
	if v, known := boolFieldOf(o, "can_edit_node"); known {
		t.CanEditNode, t.CanEditNodeKnown = v, true
	}
	t.Emojis = decodeEmojiURLs(o["emojis"])
	t.Reactions = decodeReactionStats(o["reactions_summary"], o["reactions"], o["my_reactions"])
	t.Rewards = decodeRewards(o["rewards"])
	t.Postscripts = decodePostscripts(o["postscripts"])
	// 线上 API 把投票放在 d.vote（顶层）；早期文档示例写在 d.extra.vote。
	// 两处都认，避免投票块在真实帖子上去世。
	if vote := o.sub("vote"); vote != nil {
		t.Vote = decodeVote(vote)
	} else if extra := o.sub("extra"); extra != nil {
		if vote := extra.sub("vote"); vote != nil {
			t.Vote = decodeVote(vote)
		}
	}
	// 竞猜：线上与文档的位置同样不固定（d.bet / d.extra.bet），两处都认。
	t.Bet = decodeTopicBet(o)
	// 受限内容门槛：文档写在 extra.restricted，顶层也认。
	t.Restricted = decodeTopicRestricted(o)
	// 兑换码块同样写在 extra.redeem_codes：每条只有 id 与脱敏值，领取时按
	// id 请求（见 redeem.go）。
	t.Redeem = decodeTopicRedeem(o)
	// 金币池：线上在顶层 d.reward_pool，早期文档在 d.extra.reward_pool。
	t.RewardPool = decodeTopicRewardPool(o)
	// 开奖结果：api-write.md「抽奖」一节写在 extra.lottery，顶层也认。
	t.Lottery = decodeTopicLottery(o)
	return t, nil
}

// decodeCollectionNeighbor 读 post_collection.prev / next。null、空对象、没有
// short_id 时都按「没有这一篇」处理，卡片就不会画出点不开的入口。
func decodeCollectionNeighbor(raw json.RawMessage) *collectionNeighbor {
	n := asObject(raw)
	if n == nil {
		return nil
	}
	id := n.str("short_id")
	if id == "" {
		return nil
	}
	return &collectionNeighbor{
		ShortID:  id,
		NodeSlug: n.str("node_slug"),
		Title:    cleanText(n.str("title")),
	}
}

// boolFieldOf 读一个布尔字段并区分「缺失」与「false」。两个返回值分别是
// 字段值和是否真的见到了这个键；字段不是布尔时同样按缺失处理，让老响应和
// 离线缓存继续按「没有访问墙」渲染。
func boolFieldOf(o object, keys ...string) (value, known bool) {
	if o == nil {
		return false, false
	}
	for _, k := range keys {
		raw, ok := o[k]
		if !ok {
			continue
		}
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			return b, true
		}
	}
	return false, false
}

// guardBlocks 报告这篇帖子是否被引擎按访问墙拦下。只有明确读到
// access_guard.passed == false 才算；判断依据是 passed 本身，不是正文是否
// 为空（正文可能本来就空，见 11-19 的访问限制口径）。
func (t *topicDetail) guardBlocks() bool {
	return t != nil && t.GuardPassedKnown && !t.GuardPassed
}

// contentBlocked 统一两路可见性信号：详情的 access_guard.passed == false，
// 以及评论接口的 d:null。二者都不从正文 / 列表是否为空推断（空正文的帖子
// 完全合法，见 11-19 的访问限制口径）。
func (d *detailState) contentBlocked() bool {
	if d == nil {
		return false
	}
	return d.topic.guardBlocks() || d.replyBlocked
}

// categoryPath 是节点的两级分类：有父节点时拼成「父 · 子」，只有一级时退回
// 节点名。父节点名来自 node.parent；只有 parent_slug 时（老响应 / 列表接口）
// 拿不到父名，同样只显示一级，不拿 slug 顶替。
func (t *topicDetail) categoryPath() string {
	if t.pendingNode() {
		// 不选节点发帖时服务端先把帖子放在「星云」，识别完成后才挪到正式
		// 节点；这一段等待期不要把「星云」当成真正的分类展示。
		return "节点寻找中…"
	}
	switch {
	case t.NodeParentName != "" && t.NodeName != "":
		return t.NodeParentName + " · " + t.NodeName
	case t.NodeName != "":
		return t.NodeName
	}
	return t.NodeParentName
}

func decodePostscripts(raw json.RawMessage) []postscript {
	list := rawArray(raw)
	if len(list) == 0 {
		if o := asObject(raw); o != nil {
			for _, k := range []string{"items", "list", "postscripts"} {
				if list = rawArray(o[k]); len(list) > 0 {
					break
				}
			}
		}
	}
	var out []postscript
	for _, item := range list {
		o := asObject(item)
		if o == nil {
			continue
		}
		content := o.str("content", "text", "body")
		if content == "" {
			continue
		}
		out = append(out, postscript{
			Content:   content,
			CreatedAt: o.str("created_at"),
			Emojis:    decodeEmojiURLs(o["emojis"]),
		})
	}
	return out
}

func decodeVote(o object) *voteBlock {
	v := &voteBlock{
		ID:        o.str("id"),
		Type:      o.str("type"),
		Title:     o.str("title"),
		Finished:  o.flag("voting_finished"),
		MaxChoice: o.num("max_choices"),
	}
	v.Voted, v.VotedKnown = voteVotedFlag(o)
	for _, raw := range rawArray(o["options"]) {
		opt := asObject(raw)
		if opt == nil {
			continue
		}
		v.Options = append(v.Options, voteOption{
			ID:    opt.str("id"),
			Text:  opt.str("text"),
			Votes: opt.num("vote_count"),
		})
	}
	if v.Title == "" && len(v.Options) == 0 {
		return nil
	}
	return v
}

func decodeReplies(d json.RawMessage) replyPage {
	var out replyPage
	o := asObject(d)
	if o == nil {
		// 无权访问与帖子不存在都回 200 + d:null，形状相同，统一按不可见处理。
		out.Blocked = true
		return out
	}
	out.Total = rawInt(o["total"])
	out.Page = rawInt(o["page"])
	out.Limit = rawInt(o["limit"])
	out.TotalPages = rawInt(o["total_pages"])
	for _, raw := range rawArray(o["items"]) {
		out.Items = append(out.Items, decodeReply(raw, 0))
	}
	// 线上 API 用 pinned_comments；pinned 是旧文档 / 示例里的名字。
	pinned := rawArray(o["pinned_comments"])
	if len(pinned) == 0 {
		pinned = rawArray(o["pinned"])
	}
	for _, raw := range pinned {
		r := decodeReply(raw, 0)
		r.Pinned = true
		out.Pinned = append(out.Pinned, r)
	}
	if out.Page == 0 {
		out.Page = 1
	}
	if out.Limit == 0 {
		out.Limit = replyLimit
	}
	return out
}

func decodeReply(raw json.RawMessage, depth int) replyItem {
	o := asObject(raw)
	if o == nil {
		return replyItem{}
	}
	r := replyItem{
		ID:           o.str("id"),
		Content:      o.str("content"), // raw markdown, rendered by the UI
		Author:       "匿名",
		CreatedAt:    o.str("created_at"),
		Floor:        o.num("floor"),
		FlatFloor:    o.num("flat_floor"),
		LocatedFloor: o.num("located_floor"),
		Level:        o.num("level"),
		Deleted:      o.flag("is_deleted"),
		Emojis:       decodeEmojiURLs(o["emojis"]),
		Depth:        depth,
	}
	// 服务端不保证每条响应都带 level（老响应、部分端点）。楼层视图里
	// 深度就是层级减一；扁平视图没有树，只能按顶层算，比不显示强。
	if r.Level <= 0 {
		r.Level = depth + 1
	}
	r.ReplyCommentID = o.str("reply_comment_id")
	r.UserPKChoice = o.str("user_pk_choice")
	if o.has("is_bookmarked", "bookmarked") {
		r.BookmarkKnown = true
		r.Bookmarked = o.flag("is_bookmarked", "bookmarked")
	}
	if a := o.sub("author"); a != nil {
		if name := a.str("username"); name != "" {
			r.Author = name
		}
		r.AuthorID = a.str("id")
		r.Avatar = authorAvatar(a)
		r.Badges = decodeBadges(a)
		r.AuthorIsPro = a.flag("is_pro", "isPro")
	}
	if p := o.sub("parent"); p != nil {
		r.ParentID = p.str("id")
		r.ParentFloor = p.num("floor")
		if a := p.sub("author"); a != nil {
			r.ParentAuthor = a.str("username")
		}
		// 「回复 N 楼：…」是父评论的纯文本预览，与列表行的评论摘要同一套
		// 规则去掉 Markdown 标记，避免引用里冒出 ** / > 之类符号。
		r.ParentSummary = truncate(commentExcerpt(p.str("content")), 60)
	}
	// 线上 API 的字段是 reactions_summary；reactions 是旧文档 / 示例里的名字。
	r.Reactions = decodeReactionStats(o["reactions_summary"], o["reactions"], o["my_reactions"])
	r.Rewards = decodeRewards(o["rewards"])
	r.RewardPoolWinnings = decodeRewardPoolWinnings(o["reward_pool_rewards"])
	if depth < 8 { // guard against pathological nesting
		for _, child := range rawArray(o["children"]) {
			r.Children = append(r.Children, decodeReply(child, depth+1))
		}
	}
	return r
}

// decodeBadges reads a user object's equipped_badges. A badge may carry
// weather info: the web shows a weather glyph and the temperature in place
// of the badge icon then.
func decodeBadges(a object) []badgeInfo {
	var out []badgeInfo
	for _, raw := range rawArray(a["equipped_badges"]) {
		item := asObject(raw)
		if item == nil {
			continue
		}
		badge := item.sub("badge")
		if badge == nil {
			continue
		}
		b := badgeInfo{
			Name:    badge.str("name"),
			Code:    badge.str("code"),
			IconURL: badge.str("icon_url", "icon_url_large"),
		}
		// status 只跟着「我来过」徽章：对象表示有状态，null 或缺少这个键都
		// 当作没有。它是 item 上的字段，不在 badge 里。
		if b.Code == badgeCodeIWasHere {
			if s := item.sub("status"); s != nil {
				if text := strings.TrimSpace(s.str("text")); text != "" {
					b.Status = &badgeStatusInfo{Text: text}
				}
			}
		}
		if w := item.sub("weather"); w != nil {
			wi := &weatherInfo{SymbolCode: w.str("symbol_code")}
			if rawTemp, ok := w["temperature"]; ok {
				_ = json.Unmarshal(rawTemp, &wi.Temperature)
			}
			if wi.SymbolCode != "" {
				b.Weather = wi
			}
		}
		if b.IconURL == "" && b.Weather == nil {
			continue
		}
		out = append(out, b)
	}
	return out
}

func weatherIconURL(symbol string) string {
	return "https://r2.2libra.com/weather/svg/" + url.PathEscape(symbol) + ".svg"
}

// nodeEmojiList 合并节点推荐的表态表情：main_emojis 是主表态，sub_emojis
// 是更多可选。线上这两个数组里都是字面表情（也可能是站点自定义图标的名字），
// 表态接口原样收下即可。去重并截断，节点没给时返回 nil。
func nodeEmojiList(n object) []string {
	if n == nil {
		return nil
	}
	seen := make(map[string]bool, reactionEmojiMax)
	out := make([]string, 0, reactionEmojiMax)
	for _, key := range []string{"main_emojis", "sub_emojis"} {
		for _, raw := range rawArray(n[key]) {
			var emoji string
			if json.Unmarshal(raw, &emoji) != nil || emoji == "" || seen[emoji] {
				continue
			}
			seen[emoji] = true
			out = append(out, emoji)
			if len(out) == reactionEmojiMax {
				return out
			}
		}
	}
	return out
}

// decodeReactionStats 汇总帖子 / 评论的表情表态。summary 是现在的字段名
// （reactions_summary），legacy 是旧文档示例里的 reactions；两者都认。
// mineRaw 是 my_reactions：当前登录用户选过的表情，匿名请求里为空数组。
// 单条记录里的 mine 布尔值也认，谁先给出都算「我」已表态。
func decodeReactionStats(summary, legacy, mineRaw json.RawMessage) []reactionStat {
	raw := summary
	if len(rawArray(raw)) == 0 {
		raw = legacy
	}
	mine := make(map[string]bool)
	var mineOrder []string
	addMine := func(emoji string) {
		if emoji == "" || mine[emoji] {
			return
		}
		mine[emoji] = true
		mineOrder = append(mineOrder, emoji)
	}
	for _, item := range rawArray(mineRaw) {
		if o := asObject(item); o != nil {
			addMine(o.str("emoji"))
			continue
		}
		var emoji string
		if json.Unmarshal(item, &emoji) == nil {
			addMine(emoji)
		}
	}
	var out []reactionStat
	seen := make(map[string]bool)
	for _, item := range rawArray(raw) {
		o := asObject(item)
		if o == nil {
			continue
		}
		emoji := o.str("emoji")
		if emoji == "" {
			continue
		}
		seen[emoji] = true
		out = append(out, reactionStat{
			Emoji: emoji,
			Count: o.num("count"),
			Mine:  mine[emoji] || o.flag("mine"),
		})
		if len(out) == 6 {
			return out
		}
	}
	// 自己表过、但聚合里暂时没有（例如刚表态还没回填）的表情也要留着，
	// 这样「我」的高亮不会因为 summary 缺一项就消失。
	for _, emoji := range mineOrder {
		if seen[emoji] {
			continue
		}
		out = append(out, reactionStat{Emoji: emoji, Mine: true})
		if len(out) == 6 {
			break
		}
	}
	return out
}

// decodeRewards 把 rewards 数组折成「合计 / 次数 / 打赏者」。只统计有金额
// 的记录；同一个人的多次打赏不会重复写昵称，但次数按记录累计。
func decodeRewards(raw json.RawMessage) rewardStat {
	var out rewardStat
	seen := make(map[string]bool)
	for _, item := range rawArray(raw) {
		o := asObject(item)
		if o == nil {
			continue
		}
		amount := o.num("amount")
		if amount <= 0 {
			continue
		}
		out.Total += amount
		out.Count++
		if u := o.sub("user"); u != nil {
			if name := authorName(u); name != "" && !seen[name] {
				seen[name] = true
				out.Users = append(out.Users, name)
			}
		}
	}
	return out
}

// replyPermalink 拼一条评论在站点上的永久链接。楼层视图按 floor 分页，
// 嵌套回复没有 floor，改用 /post-flat/ 按 flat_floor 分页；页码与站点
// 每页条数（siteCommentPageSize）一致，站点才能定位到那条评论。
func replyPermalink(topic *topicDetail, fallback string, r replyItem) string {
	if r.ID == "" {
		return ""
	}
	base := fallback
	if topic != nil && topic.URL != "" {
		base = topic.URL
	}
	if base == "" {
		return ""
	}
	if u, err := url.Parse(base); err == nil {
		u.RawQuery = ""
		u.Fragment = ""
		base = u.String()
	}
	nested := r.Floor == 0
	if nested {
		base = strings.Replace(base, "/post/", "/post-flat/", 1)
	}
	floor := r.Floor
	if floor == 0 {
		floor = r.FlatFloor
	}
	if floor == 0 {
		floor = r.LocatedFloor
	}
	q := url.Values{}
	q.Set("commentId", r.ID)
	if p := (floor + siteCommentPageSize - 1) / siteCommentPageSize; p > 1 {
		q.Set("p", fmt.Sprintf("%d", p))
	}
	return base + "?" + q.Encode()
}

// replyIndexByID 返回评论在已显示列表里的行号，找不到时返回 -1。
func replyIndexByID(list []replyItem, id string) int {
	if id == "" {
		return -1
	}
	for i := range list {
		if list[i].ID == id {
			return i
		}
	}
	return -1
}

// insertReply 把刚发布成功的评论放进当前列表，返回它落在的行号。顶层评论
// 与扁平时间序都排在已经显示出来的评论之后；楼层视图里的楼中楼插到父评论
// 的整棵子树后面，而不是跳到列表最顶端。更后面的页仍会按楼层顺序接上。
func insertReply(list []replyItem, created replyItem, parentID string, flat bool) ([]replyItem, int) {
	if flat || parentID == "" {
		return append(list, created), len(list)
	}
	parent := -1
	for i := range list {
		if list[i].ID == parentID {
			parent = i
			break
		}
	}
	if parent < 0 {
		// 父评论不在已加载的页里：至少不要丢掉刚发的评论。
		return append(list, created), len(list)
	}
	created.Depth = list[parent].Depth + 1
	end := parent + 1
	for end < len(list) && list[end].Depth > list[parent].Depth {
		end++
	}
	list = append(list, replyItem{})
	copy(list[end+1:], list[end:])
	list[end] = created
	return list, end
}

// flattenReplies shows pinned floors first, then top-level floors followed
// by their children (the floor view). flat=1 responses have no children.
func flattenReplies(p replyPage) []replyItem {
	out := make([]replyItem, 0, len(p.Items)+len(p.Pinned))
	var walk func([]replyItem)
	walk = func(list []replyItem) {
		for _, r := range list {
			shown := r
			shown.Children = nil
			// 原始数据里有子树就标上，摊平后仍带这个标志，折叠功能据此
			// 判断哪些评论能收起楼中楼。
			shown.HasChildren = len(r.Children) > 0
			out = append(out, shown)
			walk(r.Children)
		}
	}
	walk(p.Pinned)
	walk(p.Items)
	return out
}

// replyMaxLevel 是站点的回复层数上限：1 楼层评论、2 楼中楼、3 三级回复。
// 到三级就到底了——回复三级评论不会再挂第四层，服务端把新评论作为三级评论
// 的兄弟节点，父仍然是那条二级评论。线上数据与网页端提交都是这个形状。
const replyMaxLevel = 3

// replyTarget 是评论框当前在回的那条评论，零值表示发一条新的顶层评论。
//
// 三级回复是扁平存储的：真正回谁记在 replyCommentId 里，parentId 停在二级
// 评论上，level 恒为 3。所以「提交用的父」和「被点的那条」不是同一个，字段
// 必须按层级组装，见 postParentID / writeLevel。
type replyTarget struct {
	ID       string // 被点「回复」的那条评论
	Level    int    // 它的层级；0 表示未知（服务端没给）
	ParentID string // 它的父评论：三级评论的父是二级评论
	Floor    int
	Author   string
	AuthorID string
}

// postParentID 是提交时 parentId 的取值：回三级评论时停在它的父（二级评论）
// 上，新评论因此成为三级评论的兄弟；其余情况就是被回复的那条本身。
func (t replyTarget) postParentID() string {
	if t.Level >= replyMaxLevel {
		// 父评论缺失时宁可不发 parentId（当成新楼层），也不发一个必然被
		// 服务端判成第四层的 id。
		return t.ParentID
	}
	return t.ID
}

// writeLevel 是提交时 level 的取值，按开放文档从 0 起：评论帖子 0、回复一级
// 评论 1、回复二级评论 2、回复三级评论 3。它决定通知收件人，显式传可以避免
// 服务端靠 parentId / replyCommentId 推断。读接口里的 Level 从 1 起（1 = 楼层
// 评论），所以回复时正好等于被回复对象的 Level；评论帖子是 0。
// 层级未知时第二个返回值为 false，宁可不传（服务端会推断），也不发一个错的。
func (t replyTarget) writeLevel() (int, bool) {
	if t.ID == "" {
		return 0, true
	}
	if t.Level <= 0 {
		return 0, false
	}
	return min(t.Level, replyMaxLevel), true
}

// replyDepth 是这条评论在楼层树里的层级（读接口口径，1 起）：评论帖子是 1，
// 回复一级评论是 2，回复二级评论是 3，回复三级评论仍是 3（同级兄弟）。
// 201 响应常常不带 level，刚发的评论按它补齐，否则缩进会少一层。
func (t replyTarget) replyDepth() int {
	switch {
	case t.ID == "":
		return 1
	case t.Level <= 0:
		return 2 // 层级未知时至少按楼中楼算
	case t.Level >= replyMaxLevel:
		return replyMaxLevel
	}
	return t.Level + 1
}

// label 是「正在回复 X」里的 X：有楼层号报楼层，否则报作者名。
func (t replyTarget) label() string {
	if t.Floor > 0 {
		return fmt.Sprintf("%d 楼", t.Floor)
	}
	if t.Author != "" {
		return t.Author
	}
	return "评论"
}

// createReply posts a new comment, optionally answering target, using the
// write:comment scope. It goes through withAuthRetry so a 401 triggers one
// refresh + retry like every read.
// A fresh Idempotency-Key is minted per call: retrying a timed-out request is
// the caller's job and must reuse the same key, while each new submit gets a
// new one (index.md: the key identifies one logical creation).
func (c *Client) createReply(ctx context.Context, shortID, content string, target replyTarget) (replyItem, error) {
	return withAuthRetry(c, ctx, func(token string) (replyItem, error) {
		payload := map[string]any{"content": content}
		// level 显式声明这条评论的层级（从 0 起），它决定通知收件人。评论
		// 帖子固定 0；嵌套回复的 parentId 是挂载点，replyCommentId 才是真正
		// 被回复的那条，三级回复的 parentId 因此停在二级评论上。
		parentID := target.postParentID()
		if parentID == "" {
			payload["level"] = 0
		} else {
			payload["parentId"] = parentID
			payload["replyCommentId"] = target.ID
			if level, known := target.writeLevel(); known {
				payload["level"] = level
			}
		}
		d, err := c.postJSON(ctx, c.apiURL(topicPath(shortID)+"/replies"),
			token, randomURLSafe(24), payload)
		if err != nil {
			return replyItem{}, err
		}
		r := decodeReply(d, 0)
		// 线上 201 只保证回创建结果，不保证带正文：有的响应只有 id / 楼层 /
		// 作者。刚发的评论此时若直接拿返回值插进列表，会显示「（无内容）」，
		// 刷新后才看到正文。正文就是这次提交的内容，缺失时直接补上。
		// 服务端给三级回复补的「@用户名 」前缀只在响应里出现，不要在本地补；
		// 响应带正文时以它为准。
		if strings.TrimSpace(r.Content) == "" {
			r.Content = content
		}
		return r, nil
	})
}

func decodeEmojis(d json.RawMessage) map[string]emojiInfo {
	out := make(map[string]emojiInfo)
	list := rawArray(d)
	if len(list) == 0 {
		if o := asObject(d); o != nil {
			for _, k := range []string{"emojis", "items", "list"} {
				if list = rawArray(o[k]); len(list) > 0 {
					break
				}
			}
		}
	}
	for _, raw := range list {
		o := asObject(raw)
		if o == nil {
			continue
		}
		// 短码统一成「:code:」：文档的 code 自带冒号，但不能假设每个来源
		// 都这样，否则同一张表里会混着两种写法。
		code := ":" + strings.Trim(o.str("code"), ":") + ":"
		e := emojiInfo{
			Code:   code,
			Name:   o.str("name"),
			ImgURL: o.str("img_url"),
		}
		if code != "::" && e.ImgURL != "" {
			out[e.Code] = e
		}
	}
	return out
}

// decodeEmojiURLs reads a post / reply's own short code → image map. The
// server may send relative paths ("/emojis/x.png?size=medium"); the web
// resolves them against the media origin. Keys are normalized to the
// ":code:" form the short-code scanner produces, so a table that omits the
// colons still renders and still inserts one pair of them.
func decodeEmojiURLs(raw json.RawMessage) map[string]string {
	o := asObject(raw)
	if o == nil {
		return nil
	}
	var out map[string]string
	for code, v := range o {
		if strings.Trim(code, ":") == "" {
			continue
		}
		var u string
		if json.Unmarshal(v, &u) != nil || u == "" {
			continue
		}
		if out == nil {
			out = make(map[string]string)
		}
		out[":"+strings.Trim(code, ":")+":"] = resolveEmojiURL(u)
	}
	return out
}

func resolveEmojiURL(u string) string {
	switch {
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"):
		return u
	case strings.HasPrefix(u, "//"):
		return "https:" + u
	case strings.HasPrefix(u, "/"):
		return "https://r2.2libra.com" + u
	}
	return u
}

// mergeEmojiMaps overlays a post's / reply's own icon map on the global
// one. Codes the global table lacks (community icons) get their own entry;
// known codes keep their human name but use the local image address.
func mergeEmojiMaps(base map[string]emojiInfo, extra ...map[string]string) map[string]emojiInfo {
	// 惰性复制：绝大多数帖子/评论没有自带表情，extra 为空。若在这里先
	// make + 拷贝整张全局表，再因为没有任何覆盖而丢掉，正文与每条可见
	// 评论每帧都要白做一遍分配（大表情表下尤其明显，是输入时偶发卡顿
	// 的来源）。只有真的遇到第一条有效覆盖时才建表。
	var out map[string]emojiInfo
	for _, m := range extra {
		for code, u := range m {
			if code == "" || u == "" {
				continue
			}
			if out == nil {
				out = make(map[string]emojiInfo, len(base)+8)
				for code, e := range base {
					out[code] = e
				}
			}
			e, ok := out[code]
			if !ok {
				e = emojiInfo{Code: code, Name: strings.Trim(code, ":")}
			}
			e.ImgURL = resolveEmojiURL(u)
			out[code] = e
		}
	}
	if out == nil {
		return base
	}
	return out
}

// rawArray only accepts a JSON array; a null / object / string yields nil.
func rawArray(raw json.RawMessage) []json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var out []json.RawMessage
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// replaceEmoji renders :code: short codes through the /v1/emojis map.
// With markdown=true it emits an image; plain text gets a readable [name].
// Unmapped codes are left verbatim: without the table that is all we can
// honestly show (api-read.md 表情).
func replaceEmoji(src string, emojis map[string]emojiInfo, markdown bool) string {
	if src == "" || len(emojis) == 0 {
		return src
	}
	return shortCode.ReplaceAllStringFunc(src, func(code string) string {
		e, ok := emojis[code]
		if !ok {
			return code
		}
		name := e.Name
		if name == "" {
			name = strings.Trim(code, ":")
		}
		name = strings.NewReplacer("[", "", "]", "", "\n", " ").Replace(name)
		if markdown && strings.HasPrefix(e.ImgURL, "https://") &&
			!strings.ContainsAny(e.ImgURL, "()[] \t\r\n") {
			return "![" + name + "](" + e.ImgURL + ")"
		}
		return "[" + name + "]"
	})
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
