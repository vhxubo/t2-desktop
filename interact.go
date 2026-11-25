package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 收藏与表态
//
//	POST   /v1/topics/:short_id/favorite       收藏（scope interact）
//	DELETE /v1/topics/:short_id/favorite       取消收藏（scope interact）
//	POST   /v1/topics/:short_id/reaction       帖子表态（toggle，scope interact）
//	POST   /v1/replies/:id/reaction            评论表态（toggle，scope interact）
//
// 收藏和表态都不需要幂等键。表态是 toggle 语义：已经表过同一个表情再调一次
// 就取消，所以本地按当前状态取反，界面先乐观更新，失败再回滚。
//
// 表态接口要求服务端能据此生成对方的通知，所以请求体里除表情外还要带上
// 接收方 uuid、帖子路径、标题与节点 uuid；评论表态另要正文与楼层。这些字段
// 少一个就 400，因此 detail 解析里补了 node.id / node 推荐表情 / 评论作者
// uuid（见 detail.go）。

const interactTimeout = 20 * time.Second

// reactionEmojiMax 是节点推荐表情的取用上限：表态选择器不该变成一屏表情墙。
const reactionEmojiMax = 12

// reactionTopic 是表态目标里的「整个帖子」，其余取值是评论 uuid。
const reactionTopic = "topic"

// favoriteState 是详情页收藏按钮的状态。只在 UI 线程读写；网络结果通过
// app.publish 回来。viewer 记录这份状态属于哪个登录账号，换账号后重新读。
type favoriteState struct {
	known     bool // 服务端是否明确给过收藏状态
	favorited bool
	busy      bool   // 正在提交收藏 / 取消收藏
	viewer    string // 状态属于哪个账号
	gen       uint64
}

// reactionState 是互动区的界面状态。表情与明细是贴着入口的 Lobe 气泡，打赏是
// 盖在整页上的 Modal；三者都只记「开没开、对着谁」：
//
//	reactOpen / reactFor     表情气泡（笑脸图标）
//	rewardOpen / rewardFor   打赏弹窗（金币图标）
//	detailsOpen / detailsFor 表情明细气泡（点摘要胶囊）
//
// amount 是打赏金额草稿（1～500）。rewardKey / rewardSig 是打赏的幂等键与它
// 对应的请求签名：同一笔打赏的超时重试必须用同一个键，
// 金额或目标变了才换新键。
type reactionState struct {
	reactOpen  bool
	reactFor   string
	rewardOpen bool
	rewardFor  string

	detailsOpen bool
	detailsFor  string

	busy bool // 正在提交表态
	gen  uint64

	amount     float64
	rewardBusy bool
	rewardErr  string
	rewardKey  string
	rewardSig  string
	rewardGen  uint64
}

/* ------------------------------------------------------------------
   端点与请求体
-------------------------------------------------------------- */

func topicFavoritePath(shortID string) string {
	return topicPath(shortID) + "/favorite"
}

func topicReactionPath(shortID string) string {
	return topicPath(shortID) + "/reaction"
}

func replyReactionPath(replyID string) string {
	return "/v1/replies/" + url.PathEscape(replyID) + "/reaction"
}

// topicReactionBody 的字段名照抄文档：除 path / postTitle 外都是 camelCase，
// 少一个服务端就 400。
type topicReactionBody struct {
	Emoji     string `json:"emoji"`
	RecUserID string `json:"recUserId"`
	Path      string `json:"path"`
	PostTitle string `json:"postTitle"`
	NodeID    string `json:"nodeId"`
}

type replyReactionBody struct {
	Emoji        string `json:"emoji"`
	RecUserID    string `json:"recUserId"`
	Path         string `json:"path"`
	Comment      string `json:"comment"`
	LocatedFloor int    `json:"locatedFloor"`
	FlatFloor    int    `json:"flatFloor,omitempty"`
	NodeID       string `json:"nodeId"`
}

// rewardBody 是 POST /v1/rewards 的请求体（打赏）。type 取 post / comment，
// 帖子用 short_id，评论用 uuid；金额 1～500，超出服务端会拒绝。
type rewardBody struct {
	Type      string `json:"type"`
	Amount    int    `json:"amount"`
	PostID    string `json:"post_id,omitempty"`
	CommentID string `json:"comment_id,omitempty"`
	Path      string `json:"path,omitempty"`
}

// rewardAmountLo / rewardAmountHi 是文档给的打赏金额范围。
const (
	rewardAmountLo = 1
	rewardAmountHi = 500
)

// sendReward 提交一笔打赏。这个端点标了 🔑，必须带幂等键：同一笔打赏的
// 超时重试要用同一个键，否则可能重复扣币。
func (c *Client) sendReward(ctx context.Context, key string, body rewardBody) error {
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		_, err := c.postJSON(ctx, c.apiURL("/v1/rewards"), token, key, body)
		return struct{}{}, err
	})
	return err
}

// setTopicFavorite 收藏或取消收藏，走和读请求相同的刷新 + 重试链路。目标
// 状态已经成立（409 conflict / 404 not_found）视为成功：收藏是幂等动作。
func (c *Client) setTopicFavorite(ctx context.Context, shortID string, favorite bool) error {
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		var err error
		if favorite {
			_, err = c.postJSON(ctx, c.apiURL(topicFavoritePath(shortID)), token, "", nil)
		} else {
			_, err = c.deleteJSON(ctx, c.apiURL(topicFavoritePath(shortID)), token)
		}
		var ae *apiError
		if errors.As(err, &ae) &&
			(ae.Status == http.StatusConflict || ae.Status == http.StatusNotFound) {
			err = nil
		}
		return struct{}{}, err
	})
	return err
}

func (c *Client) sendTopicReaction(ctx context.Context, shortID string, body topicReactionBody) error {
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		_, err := c.postJSON(ctx, c.apiURL(topicReactionPath(shortID)), token, "", body)
		return struct{}{}, err
	})
	return err
}

func (c *Client) sendReplyReaction(ctx context.Context, replyID string, body replyReactionBody) error {
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		_, err := c.postJSON(ctx, c.apiURL(replyReactionPath(replyID)), token, "", body)
		return struct{}{}, err
	})
	return err
}

/* ------------------------------------------------------------------
   详情状态：同步与请求体组装
-------------------------------------------------------------- */

// syncFavorite 把详情响应里的收藏状态交给按钮。请求在途时不覆盖：那次提交
// 的结果才是最新的。viewer 是这份状态所属的登录账号（未登录时为空）。
func syncFavorite(d *detailState, t topicDetail, viewer string) {
	if d == nil || d.fav.busy {
		return
	}
	d.fav.known = t.FavoriteKnown
	d.fav.favorited = t.FavoriteKnown && t.Favorited
	d.fav.viewer = viewer
}

// topicSitePath 是表态接口要的站内路径（如 /post/work/a1b2c3d4）。详情里
// 已有原文链接，直接截掉站点前缀；链接缺失时表态无法完成，调用方要报错。
func topicSitePath(t *topicDetail) string {
	if t == nil || t.URL == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(t.URL, siteBaseURL); ok && strings.HasPrefix(rest, "/") {
		return rest
	}
	if u, err := url.Parse(t.URL); err == nil && strings.HasPrefix(u.Path, "/") {
		return u.Path
	}
	return ""
}

// canReactTopic 报告这篇帖子能不能表态：表态通知要有接收方（作者 uuid），
// 匿名帖的 author 是 null，没有可通知的人。节点与标题缺失时请求也会 400，
// 一并挡在入口处。
func canReactTopic(t *topicDetail) bool {
	return t != nil && t.AuthorID != "" && t.NodeID != "" &&
		strings.TrimSpace(t.Title) != "" && topicSitePath(t) != ""
}

// topicReactionNeed 组装帖子表态请求体；必填字段不齐时 ok 为 false。
func topicReactionNeed(t *topicDetail, emoji string) (topicReactionBody, bool) {
	if t == nil || emoji == "" {
		return topicReactionBody{}, false
	}
	path := topicSitePath(t)
	if t.AuthorID == "" || t.NodeID == "" || path == "" || strings.TrimSpace(t.Title) == "" {
		return topicReactionBody{}, false
	}
	return topicReactionBody{
		Emoji:     emoji,
		RecUserID: t.AuthorID,
		Path:      path,
		PostTitle: t.Title,
		NodeID:    t.NodeID,
	}, true
}

// replyReactionNeed 组装评论表态请求体。评论正文最多 2000 字，超出按字符
// 截断（服务端只把它用于通知展示）。楼层优先用 floor，嵌套回复没有 floor
// 时退回 flat_floor。
func replyReactionNeed(t *topicDetail, r replyItem, emoji string) (replyReactionBody, bool) {
	if t == nil || emoji == "" || r.ID == "" {
		return replyReactionBody{}, false
	}
	path := topicSitePath(t)
	if r.AuthorID == "" || t.NodeID == "" || path == "" {
		return replyReactionBody{}, false
	}
	floor := r.Floor
	if floor <= 0 {
		floor = r.FlatFloor
	}
	if floor <= 0 {
		floor = r.LocatedFloor
	}
	// 评论正文最多 2000 字：服务端只把它用于通知展示。这里按字符硬截断，
	// 不加省略号（省略号会把长度顶到 2001）。
	comment := strings.TrimSpace(r.Content)
	if runes := []rune(comment); len(runes) > 2000 {
		comment = string(runes[:2000])
	}
	return replyReactionBody{
		Emoji:        emoji,
		RecUserID:    r.AuthorID,
		Path:         path,
		Comment:      comment,
		LocatedFloor: floor,
		FlatFloor:    r.FlatFloor,
		NodeID:       t.NodeID,
	}, true
}

/* ------------------------------------------------------------------
   本地状态更新（乐观更新与回滚共用）
-------------------------------------------------------------- */

// applyReaction 把一个表情在目标上的「我是否表过」设成 mine，并同步计数。
// 计数不会减到 0 以下；取消到 0 的那枚表情从列表里移除，列表保持精简。
func applyReaction(stats []reactionStat, emoji string, mine bool) []reactionStat {
	for i := range stats {
		if stats[i].Emoji != emoji {
			continue
		}
		stats[i].Mine = mine
		if mine {
			stats[i].Count++
		} else if stats[i].Count > 0 {
			stats[i].Count--
		}
		if !mine && stats[i].Count == 0 {
			return append(stats[:i], stats[i+1:]...)
		}
		return stats
	}
	if !mine {
		return stats
	}
	return append(stats, reactionStat{Emoji: emoji, Count: 1, Mine: true})
}

// reactionMine 报告我是否已经表过这个表情。
func reactionMine(stats []reactionStat, emoji string) bool {
	for _, s := range stats {
		if s.Emoji == emoji {
			return s.Mine
		}
	}
	return false
}

// reactionStatsOf 返回目标当前的表情表态汇总。
func reactionStatsOf(d *detailState, target string) []reactionStat {
	if d == nil {
		return nil
	}
	if target == reactionTopic {
		if d.topic == nil {
			return nil
		}
		return d.topic.Reactions
	}
	for i := range d.replyList {
		if d.replyList[i].ID == target {
			return d.replyList[i].Reactions
		}
	}
	return nil
}

// setReactionStatsOf 把更新后的表态汇总写回目标。
func setReactionStatsOf(d *detailState, target string, stats []reactionStat) {
	if d == nil {
		return
	}
	if target == reactionTopic {
		if d.topic != nil {
			d.topic.Reactions = stats
		}
		return
	}
	for i := range d.replyList {
		if d.replyList[i].ID == target {
			d.replyList[i].Reactions = stats
			return
		}
	}
}

// findReply 在已显示的评论里按 uuid 找一行。
func findReply(list []replyItem, id string) (replyItem, bool) {
	for _, r := range list {
		if r.ID == id {
			return r, true
		}
	}
	return replyItem{}, false
}

/* ------------------------------------------------------------------
   收藏
-------------------------------------------------------------- */

// toggleFavorite 收藏或取消收藏当前帖子。未登录先走全局授权弹窗，授权成功
// 后由用户自己再点一次；缺 scope 的 403 也一样。界面先乐观更新，失败回滚。
func (a *app) toggleFavorite(d *detailState) {
	if d == nil || !a.detailLive(d) || d.topic == nil {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能收藏帖子。")
		return
	}
	if d.fav.busy {
		return
	}
	desired := !(d.fav.known && d.fav.favorited)
	before := d.fav
	// 当前正文对象：请求期间可能刷新出一个新的 topic，失败回滚时不能把
	// 那份新响应的收藏数改成这个旧值。
	topic := d.topic
	countBefore := topic.FavoritedCount
	// 界面先乐观更新：收藏状态翻过去，收藏数跟着加减。服务端没有下发收藏数
	// 时不显示、也不猜一个数字。
	if topic.FavoriteCountKnown {
		if desired {
			topic.FavoritedCount++
		} else if topic.FavoritedCount > 0 {
			topic.FavoritedCount--
		}
	}
	d.fav.favorited = desired
	d.fav.known = true
	d.fav.busy = true
	d.fav.viewer = a.auth.username()
	d.fav.gen++
	gen := d.fav.gen
	shortID := d.shortID
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), interactTimeout)
	go func() {
		defer cancel()
		err := a.client.setTopicFavorite(ctx, shortID, desired)
		a.publish(func() {
			if !a.detailLive(d) || d.fav.gen != gen {
				return
			}
			d.fav.busy = false
			// 请求期间换了账号 / 退出了登录：这次结果不属于当前会话。
			if !a.auth.loggedIn() || a.auth.username() != actor {
				d.fav.known = false
				d.fav.favorited = false
				d.fav.viewer = ""
				return
			}
			if err != nil {
				d.fav = before
				if d.topic == topic {
					d.topic.FavoritedCount = countBefore
				}
				if a.handleAuthError(err) {
					a.notifyInteractDone(err)
					return
				}
				a.toast(a.detailToastFor(d), friendlyError(err))
				a.notifyInteractDone(err)
				return
			}
			if desired {
				a.toast(a.detailToastFor(d), "已收藏")
			} else {
				a.toast(a.detailToastFor(d), "已取消收藏")
			}
			// 「我的」窗口里的收藏列表已经过期：清掉缓存，下次打开时重新拉。
			a.invalidateFavorites()
			a.notifyInteractDone(nil)
		})
	}()
}

// invalidateFavorites 让「我的 - 收藏」下次打开时重新请求。收藏在详情页发生
// 变化后，那份列表不能再沿用旧结果。
func (a *app) invalidateFavorites() {
	if a.profile == nil {
		return
	}
	st := &a.profile.content[profileContentFavorites]
	if st.loading {
		return // 正在拉的这一页回来后会覆盖缓存，不必插队
	}
	st.loaded = false
	st.items = nil
	st.page = 0
	st.total = 0
	st.totalPages = 0
	st.noMore = false
	st.err = ""
	st.moreErr = ""
}

/* ------------------------------------------------------------------
   表态
-------------------------------------------------------------- */

// openReactionPopup 打开表情气泡。点笑脸入口时走 prepareReactionPopup 切换
// 目标；这里供登录回调与显式打开用，直接把气泡打开。
func (a *app) openReactionPopup(d *detailState, target string) {
	if d == nil || !a.detailLive(d) {
		return
	}
	if a.isOwnTarget(d, target) {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能添加表情。")
		return
	}
	a.prepareReactionPopup(d, target)
	d.react.reactOpen = true
}

// prepareReactionPopup 把表态目标设为 target，并关掉同屏的其它弹层。
func (a *app) prepareReactionPopup(d *detailState, target string) {
	d.react.rewardOpen = false
	d.react.detailsOpen = false
	d.react.reactFor = target
}

// openRewardPopup 打开打赏弹窗（金币入口）。点金币入口时走 prepareRewardPopup
// 切换目标；这里供登录回调与显式打开用。
func (a *app) openRewardPopup(d *detailState, target string) {
	if d == nil || !a.detailLive(d) {
		return
	}
	if a.isOwnTarget(d, target) {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能打赏。")
		return
	}
	a.prepareRewardPopup(d, target)
	d.react.rewardOpen = true
}

// prepareRewardPopup 记下打赏目标、排掉别的弹层，并把金额与错误复位。
func (a *app) prepareRewardPopup(d *detailState, target string) {
	d.react.reactOpen = false
	d.react.detailsOpen = false
	d.react.rewardFor = target
	d.react.rewardErr = ""
	if d.react.amount < rewardAmountLo {
		d.react.amount = 5
	}
}

// openReactionDetails 打开表情明细气泡（点摘要胶囊）。明细只读展示也能看，
// 不要求登录。点胶囊时走 prepareReactionDetails 切换目标。
func (a *app) openReactionDetails(d *detailState, target string) {
	if d == nil || !a.detailLive(d) {
		return
	}
	a.prepareReactionDetails(d, target)
	d.react.detailsOpen = true
}

// prepareReactionDetails 记下明细目标并排掉别的弹层。
func (a *app) prepareReactionDetails(d *detailState, target string) {
	d.react.reactOpen = false
	d.react.rewardOpen = false
	d.react.detailsFor = target
}

// isOwnAuthor 报告作者昵称是不是当前登录用户。详情里只可靠地拿到用户名，
// 没有缓存当前用户的 uuid，所以这里沿用原有的按昵称比对口径（评论一直这么
// 判断），避免为了一个入口去依赖未必已加载的资料页状态。
func (a *app) isOwnAuthor(author string) bool {
	name := strings.TrimSpace(a.auth.username())
	return name != "" && strings.EqualFold(name, strings.TrimSpace(author))
}

// isOwnTarget 报告表态 / 打赏目标是不是当前登录用户自己的帖子或评论。这两种
// 动作都会通知目标作者，发给自己没有意义，也不该花自己的金币打赏自己，所以
// 界面不加入口，动作本身也直接拦下。
func (a *app) isOwnTarget(d *detailState, target string) bool {
	if d == nil || d.topic == nil {
		return false
	}
	if target == reactionTopic {
		return a.isOwnAuthor(d.topic.Author)
	}
	r, ok := findReply(d.replyList, target)
	return ok && a.isOwnAuthor(r.Author)
}

// reactionTargetReady / rewardTargetReady 只检查目标本身的信息齐不齐，不管
// 是不是自己的内容。互动栏是否占位要看这两个条件，这样自己的帖子只是不画
// 入口，不会让整条栏消失、把右下角的操作组挪到 toast 区。
func reactionTargetReady(d *detailState, target string) bool {
	if d == nil || d.topic == nil {
		return false
	}
	if target == reactionTopic {
		return canReactTopic(d.topic)
	}
	r, ok := findReply(d.replyList, target)
	return ok && r.AuthorID != "" &&
		d.topic.NodeID != "" && topicSitePath(d.topic) != ""
}

func rewardTargetReady(d *detailState, target string) bool {
	if d == nil || d.topic == nil {
		return false
	}
	if target == reactionTopic {
		return d.topic.AuthorID != "" && d.shortID != ""
	}
	r, ok := findReply(d.replyList, target)
	return ok && r.AuthorID != "" && d.shortID != ""
}

// canActOn 报告某个表态目标能不能操作：目标本身要有作者 uuid / 节点 / 标题
// 与原文路径（评论只要作者 uuid），且不能是自己的帖子或评论。匿名帖没有可
// 通知的人，只读展示。
func (a *app) canActOn(d *detailState, target string) bool {
	return reactionTargetReady(d, target) && !a.isOwnTarget(d, target)
}

// canRewardOn 报告能不能打赏这个目标：打赏是金币变动，必须有接收方与目标
// 标识；匿名内容的 author 是 null，没有接收方。自己的帖子和评论都不打赏。
func (a *app) canRewardOn(d *detailState, target string) bool {
	return rewardTargetReady(d, target) && !a.isOwnTarget(d, target)
}

// reactionEmojisOf 是表态弹层里的表情：节点推荐 + 已有的聚合表情（保证已经
// 表过的表情还能取消），去重并截断。
func reactionEmojisOf(d *detailState, target string) []string {
	if d == nil || d.topic == nil {
		return nil
	}
	return reactionChoices(d.topic, reactionStatsOf(d, target))
}

// toggleReaction 对帖子或一条评论表 / 取消一个表情。未登录先走授权弹窗；
// 界面乐观更新，失败回滚并说明原因。服务端是 toggle 语义，所以请求体里
// 不带「要加还是取消」，本地状态就是唯一的依据。
func (a *app) toggleReaction(d *detailState, target, emoji string) {
	if d == nil || !a.detailLive(d) || d.topic == nil || emoji == "" || d.react.busy {
		return
	}
	// 入口已按 canActOn 隐藏；这里再拦一次，防止已经打开的气泡在内容换成
	// 自己的之后仍能提交。
	if a.isOwnTarget(d, target) {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能添加表情。")
		return
	}
	topic := d.topic
	var reply replyItem
	if target != reactionTopic {
		r, ok := findReply(d.replyList, target)
		if !ok {
			a.toast(a.detailToastFor(d), "这条评论已经不在列表里，请刷新后再表态")
			return
		}
		reply = r
		if _, ok := replyReactionNeed(topic, reply, emoji); !ok {
			a.toast(a.detailToastFor(d), "缺少表态所需的信息，请刷新后重试")
			return
		}
	} else if _, ok := topicReactionNeed(topic, emoji); !ok {
		a.toast(a.detailToastFor(d), "缺少表态所需的信息，请刷新后重试")
		return
	}

	mine := !reactionMine(reactionStatsOf(d, target), emoji)
	// before 是回滚用的快照；applyReaction 会原地改元素，所以喂给它的是另一份
	// 拷贝，不能让快照跟着变。
	before := append([]reactionStat(nil), reactionStatsOf(d, target)...)
	updated := applyReaction(append([]reactionStat(nil), before...), emoji, mine)
	setReactionStatsOf(d, target, updated)
	d.react.busy = true
	d.react.gen++
	gen := d.react.gen
	shortID := d.shortID
	actor := a.auth.username()
	// 请求体在发出前定好：界面可能在这期间被刷新替换。
	var topicBody topicReactionBody
	var replyBody replyReactionBody
	if target == reactionTopic {
		topicBody, _ = topicReactionNeed(topic, emoji)
	} else {
		replyBody, _ = replyReactionNeed(topic, reply, emoji)
	}
	ctx, cancel := context.WithTimeout(context.Background(), interactTimeout)
	go func() {
		defer cancel()
		var err error
		if target == reactionTopic {
			err = a.client.sendTopicReaction(ctx, shortID, topicBody)
		} else {
			err = a.client.sendReplyReaction(ctx, target, replyBody)
		}
		a.publish(func() {
			if !a.detailLive(d) || d.react.gen != gen {
				return
			}
			d.react.busy = false
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				setReactionStatsOf(d, target, before)
				if a.handleAuthError(err) {
					a.notifyInteractDone(err)
					return
				}
				a.toast(a.detailToastFor(d), friendlyError(err))
				a.notifyInteractDone(err)
				return
			}
			if mine {
				a.toast(a.detailToastFor(d), "已添加表情 "+emoji)
			} else {
				a.toast(a.detailToastFor(d), "已取消表情 "+emoji)
			}
			a.notifyInteractDone(nil)
		})
	}()
}

// notifyInteractDone 触发收藏 / 表态的测试钩子。headless 测试没有窗口，
// publish 直接在请求协程里执行，靠它把结果同步回测试。
func (a *app) notifyInteractDone(err error) {
	if a.onInteractDone != nil {
		a.onInteractDone(err)
	}
}

/* ------------------------------------------------------------------
   界面：收藏按钮、互动区与弹窗
-------------------------------------------------------------- */

// favoriteButton 是标题下方作者行最右侧的收藏按钮：星形图标 + 「收藏」/
// 「已收藏」短文案，描边胶囊；已收藏时星形填实并换成强调色。请求在途时
// 禁用，避免连点。
func (a *app) favoriteButton(c *ui.Context, d *detailState) {
	if d.topic == nil {
		return
	}
	tk := lobetheme.TokensOf(c)
	// 换了登录账号：上一个账号的收藏状态不能继续用，按未知展示。
	viewer := a.auth.username()
	if a.auth.loggedIn() && d.fav.known && d.fav.viewer != viewer {
		d.fav.known = false
		d.fav.favorited = false
		d.fav.viewer = ""
	}
	favorited := a.auth.loggedIn() && d.fav.known && d.fav.viewer == viewer && d.fav.favorited
	text, label, tip := "收藏", "收藏", "收藏这篇帖子"
	if favorited {
		text, label, tip = "已收藏", "取消收藏", "已收藏，点一下取消收藏"
	}
	if d.fav.busy {
		tip = "正在提交…"
	} else if d.topic.FavoriteCountKnown {
		// 收藏数是详情响应里的字段，放在按钮里跟着状态一起变；悬停说明把
		// 「几人收藏」讲清楚，光一个数字不至于被读成别的东西。
		tip += fmt.Sprintf("（%d 人收藏）", d.topic.FavoritedCount)
	}
	b := ui.Button(c, "").
		Background(ui.Transparent).
		Border(1, tk.BorderSecondary).
		FontSize(tk.FontSizeSM).
		Padding(4, 10).
		Radius(999).
		Shrink(0)
	hovered := b.Hovered()
	ink := tk.TextSecondary
	switch {
	case favorited:
		ink = tk.Gold
	case hovered && !d.fav.busy:
		ink = tk.TextHeading
	}
	if hovered && !d.fav.busy {
		b.Background(tk.FillSecondary)
		if !favorited {
			b.Border(1, tk.Primary)
		}
	}
	if d.fav.busy {
		b.Disabled(true)
	}
	b.TextColor(ink)
	b.Children(func() {
		icon := favoriteIcon
		if favorited {
			icon = favoriteOnIcon
		}
		ui.Icon(c, icon).Size(15, 15).TextColor(ink)
		ui.Text(c, text).FontSize(tk.FontSizeSM).TextColor(ink).SingleLine()
		if d.topic.FavoriteCountKnown {
			// 数字比状态文案再小一号、用等宽弱化色：它是数量，不是按钮的
			// 第二个状态词，长起来也不会让「已收藏」看起来像动作。
			ui.Textf(c, "%d", d.topic.FavoritedCount).FontSize(tk.FontSizeSM).
				Font(sidebarFont).TextColor(tk.TextDescription).SingleLine()
		}
	})
	b.Label(label).Tooltip(tip)
	if !d.fav.busy {
		b.OnClick(func() { a.toggleFavorite(d) })
	}
}

// topicInteractionRow 画帖子的一条互动行：左边是归并后的表态与打赏汇总，
// 右边是表态（笑脸）与打赏（金币）两个图标入口。它由 topicInteractionBar
// 固定渲染在正文底部，不再跟在正文末尾滚动。
func (a *app) topicInteractionRow(c *ui.Context, d *detailState) {
	if !a.topicInteractionVisible(d) {
		return
	}
	p := d.topic
	ui.Row(c).FillWidth().Gap(4).AlignItems(ui.Center).Children(func() {
		a.reactionSummaryInfo(c, d, reactionTopic, p.Rewards)
		ui.Spacer(c).Grow(1)
		a.reactionEntries(c, d, reactionTopic, interactEntryComfy)
	})
}

// topicInteractionVisible 报告帖子的互动行有没有内容可画：已经有人表态或
// 打赏，或者当前用户可以表态 / 打赏。匿名浏览且没人互动时整条不占位。
func (a *app) topicInteractionVisible(d *detailState) bool {
	if d == nil || d.topic == nil {
		return false
	}
	p := d.topic
	// 无权帖的内容块被置空（passed == false 或评论响应 d:null），表态 / 收藏
	// / 打赏写端点会 403 且不扣费。外壳的 reactions_summary 仍在，这里不画
	// 可点的互动入口，只留正文的访问墙。
	if d.contentBlocked() {
		return false
	}
	return len(visibleReactions(p.Reactions)) > 0 || p.Rewards.Total > 0 ||
		reactionTargetReady(d, reactionTopic) || rewardTargetReady(d, reactionTopic)
}

// topicInteractionBar 是左栏正文底部固定的一条互动栏：帖子收到的表态 /
// 打赏汇总在左，表态（笑脸）与打赏（金币）入口在右。它不参与正文滚动，长帖
// 滚到哪儿，汇总与入口都留在手边。
func (a *app) topicInteractionBar(c *ui.Context, d *detailState) {
	if !a.topicInteractionVisible(d) {
		return
	}
	layout.Divider(c, layout.DividerProps{})
	// 整条和右栏折叠态的评论输入框等高：外层分隔线 1px，这里吃满剩下的
	// 高度，两栏底边因此落在同一条线上。Column 默认沿主轴（垂直）从顶部排
	// 子元素，光有固定高度和对称内边距并不会让 28px 的图标居中（会偏上），
	// 必须显式 Justify(ui.Center) 把互动行放到条带中线上。
	ui.Column(c).FillWidth().Height(detailBottomBarHeight-1).
		Padding(6, 20).Justify(ui.Center).Label("正文互动栏").Children(func() {
		a.topicInteractionRow(c, d)
	})
}

// reactionSummaryInfo 在调用方的行里画左侧只读摘要：一枚表情胶囊（叠放的
// 表情 + 总次数），有人打赏时再跟一枚金币胶囊。没有互动时什么都不画，左侧
// 自然留白。胶囊悬停给出每种表情各几次，点开是贴着它的明细气泡。
func (a *app) reactionSummaryInfo(c *ui.Context, d *detailState, target string, rewards rewardStat) {
	if d == nil || d.topic == nil {
		return
	}
	t := c.Theme()
	ts := a.typeSizes()
	shown := visibleReactions(reactionStatsOf(d, target))
	total := reactionTotal(shown)
	if len(shown) > 0 {
		// 汇总胶囊走 Lobe 的 Small 圆角 Tag：FillTertiary 底、TextSecondary
		// 字，和弹层里的 Tag 同一套视觉。Tag 自带一枚空文字，这里往后追加
		// 叠放的表情与总数。
		chip := display.Tag(c, "", display.TagProps{
			Size: lobetheme.Small, Round: true,
		})
		chip.Tooltip(reactionSummaryTip(shown))
		chip.Label(fmt.Sprintf("表情，共 %d 次", total))
		chip.Children(func() {
			// 表情叠放：后一个向左压住前一个，形成一小摞圆片；超出
			// reactionStackMax 的表情不画，悬停与明细里能看到全部。
			for i, s := range shown {
				if i == reactionStackMax {
					break
				}
				emoji := s.Emoji
				box := ui.Box(c).Size(reactionStackSize, reactionStackSize).
					Radius(reactionStackSize / 2).Background(t.Surface).Center()
				if i > 0 {
					box.MarginStart(-reactionStackOverlap)
				}
				box.Children(func() {
					ui.Text(c, emoji).FontSize(12).SingleLine()
				})
			}
			ui.Textf(c, "%d", total).FontSize(ts.metaSmall).TextColor(t.TextMuted)
		})
		// 明细只读，不用登录。气泡关着时每个摘要都把 Popover 建出来，
		// 这样点哪一条都能被 Lobe 接住；开着时只留目标那一条，免得几条
		// 摘要同时弹面板。开合由 Popover 管，这里只记下是给谁看的。
		if !d.react.detailsOpen || d.react.detailsFor == target {
			lobeoverlay.Popover(c, chip, &d.react.detailsOpen, lobeoverlay.PopoverProps{
				Placement: lobeoverlay.Bottom,
			}, func() {
				a.detailsPanel(c, d, target)
			})
		}
		if chip.Clicked() {
			a.prepareReactionDetails(d, target)
		}
	}
	if rewards.Total > 0 {
		a.rewardBadge(c, rewards)
	}
}

// 表情摘要里那一摞圆片的尺寸：叠放三个也不会比一个数字宽太多。
const (
	reactionStackMax     = 3
	reactionStackSize    = 18
	reactionStackOverlap = 6
)

// 表态 / 打赏入口的尺寸。正文底部那条互动栏只有一枚图标加一条汇总，图标比
// 评论底行稍大一号；评论底行要和 11px 的正文挤在一行里，保持紧凑，一屏能多
// 读几条。两者共用同一套绘制，只有盒子边长、圆角与图标边长不同。
type interactEntryStyle struct {
	box    float32
	icon   float32
	radius float32
}

var (
	// interactEntryCompact 是评论底行的尺寸，和评论正文同一档。
	interactEntryCompact = interactEntryStyle{box: 20, icon: 14, radius: 6}
	// interactEntryComfy 是正文互动栏的尺寸：底栏收紧后图标跟着缩到 28/16，
	// 仍比评论底行大一档，一眼能找到但不再占去正文的空间。
	interactEntryComfy = interactEntryStyle{box: 28, icon: 16, radius: 8}
)

// reactionEntries 画右侧两个图标入口（表态 / 打赏）；两个都不可用时什么
// 都不画。评论与帖子共用，尺寸由调用方给（见 interactEntryCompact / Comfy）。
func (a *app) reactionEntries(c *ui.Context, d *detailState, target string, style interactEntryStyle) {
	if !a.canActOn(d, target) && !a.canRewardOn(d, target) {
		return
	}
	ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
		if a.canActOn(d, target) {
			a.reactionEntry(c, d, target, style)
		}
		if a.canRewardOn(d, target) {
			a.rewardEntry(c, d, target, style)
		}
	})
}

// visibleReactions 只保留真的有人表过的表情（自己刚表态、服务端还没回填
// 计数的也算），并按次数从多到少排。
func visibleReactions(stats []reactionStat) []reactionStat {
	out := make([]reactionStat, 0, len(stats))
	for _, s := range stats {
		if s.Emoji == "" || (s.Count <= 0 && !s.Mine) {
			continue
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// reactionTotal 是摘要上的总数：所有表情的次数之和。
func reactionTotal(stats []reactionStat) int {
	total := 0
	for _, s := range stats {
		total += s.Count
	}
	return total
}

// reactionSummaryTip 是表情胶囊的悬停说明：每种表情各几次，多个表情依次
// 拼接在一起（如「👍 8 ❤️ 6」），不再加多余的说明文字。
func reactionSummaryTip(stats []reactionStat) string {
	parts := make([]string, 0, len(stats))
	for _, s := range stats {
		if s.Count <= 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %d", s.Emoji, s.Count))
	}
	return strings.Join(parts, " ")
}

// reactionEntry 是表情入口：一个笑脸图标，点开贴着它的 Lobe Popover 选表情。
// 图标比文字入口更紧凑，也不会把右侧挤成一条长标签。
func (a *app) reactionEntry(c *ui.Context, d *detailState, target string, style interactEntryStyle) {
	t := c.Theme()
	active := d.react.reactOpen && d.react.reactFor == target
	// Lobe 的 ActionIcon 统一给方块热区、圆角、悬停底色与过渡；样式只决定
	// 尺寸，字形颜色沿用本应用原来的层级，展开态仍是强调色。
	b := action.ActionIcon(c, reactionIcon, action.ActionIconProps{
		BlockSize: style.box, GlyphSize: style.icon, Active: active,
	}).Radius(style.radius)
	ink := t.TextMuted
	switch {
	case active:
		ink = t.Accent
	case b.Hovered():
		ink = t.Text
	}
	if d.react.busy {
		b.Disabled(true)
	}
	b.TextColor(ink).Label("表情").Tooltip("添加一个表情")
	// 气泡关着时每个入口都把 Popover 建出来，让这一帧的点击能传进去；
	// 开着时只留目标那一个。开合交给 Lobe，这里只挑目标与做登录检查。
	if !d.react.reactOpen || d.react.reactFor == target {
		lobeoverlay.Popover(c, b, &d.react.reactOpen, lobeoverlay.PopoverProps{
			Placement: lobeoverlay.Bottom,
		}, func() {
			a.reactionPanel(c, d, target)
		})
	}
	if b.Clicked() {
		if !a.auth.loggedIn() {
			// Popover 已经把气泡打开了：未登录时撤销，先走全局授权弹窗。
			d.react.reactOpen = false
			a.requestLogin("登录后才能添加表情。")
			return
		}
		a.prepareReactionPopup(d, target)
	}
}

// rewardEntry 是打赏入口：一个金币图标，点开贴着它的 Lobe Popover 填金额。
// 和表态入口同一套开合逻辑：气泡关着时每个入口都把 Popover 建出来接点击，
// 开着时只留目标那一个；登录检查与选目标放在点击回调里。
func (a *app) rewardEntry(c *ui.Context, d *detailState, target string, style interactEntryStyle) {
	t := c.Theme()
	active := d.react.rewardOpen && d.react.rewardFor == target
	// 和表态入口同一套 Lobe ActionIcon；展开态的金色是打赏的辨识色，
	// 不跟 Lobe 默认的中性 Active 高亮，其余悬停 / 禁用反馈交给组件。
	b := action.ActionIcon(c, rewardIcon, action.ActionIconProps{
		BlockSize: style.box, GlyphSize: style.icon, Active: active,
	}).Radius(style.radius)
	ink := t.TextMuted
	switch {
	case active:
		ink = t.Warning
	case b.Hovered():
		ink = t.Text
	}
	if d.react.rewardBusy {
		b.Disabled(true)
	}
	b.TextColor(ink).Label("打赏").Tooltip("打赏作者金币")
	if !d.react.rewardOpen || d.react.rewardFor == target {
		lobeoverlay.Popover(c, b, &d.react.rewardOpen, lobeoverlay.PopoverProps{
			Placement: lobeoverlay.Bottom,
		}, func() {
			a.rewardPanel(c, d, target)
		})
	}
	if b.Clicked() {
		if !a.auth.loggedIn() {
			// Popover 已经把气泡打开了：未登录时撤销，先走全局授权弹窗。
			d.react.rewardOpen = false
			a.requestLogin("登录后才能打赏。")
			return
		}
		a.prepareRewardPopup(d, target)
		d.react.rewardOpen = true
	}
}

/* ------------------------------------------------------------------
   弹层内容：表情 / 打赏 / 明细
-------------------------------------------------------------- */

// reactionPanel 是表态气泡里的内容：节点推荐表情。面板本身是 Lobe Popover，
// 里面也全用 Lobe 控件（display.Text 文案 + action.Button 表情），字号收小到
// 11/17，一屏挑完不用滚动。点一个就提交并收起气泡。
func (a *app) reactionPanel(c *ui.Context, d *detailState, target string) {
	tk := lobetheme.TokensOf(c)
	ui.Column(c).Width(212).Gap(6).Children(func() {
		display.Text(c, "选择一个表情", display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 1,
		})
		if !a.auth.loggedIn() {
			a.loginHint(c, "登录后才能添加表情。")
			return
		}
		emojis := reactionEmojisOf(d, target)
		if len(emojis) == 0 {
			display.Text(c, "这个节点没有推荐表情。", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 2,
			})
			return
		}
		stats := reactionStatsOf(d, target)
		ui.Row(c).Gap(3).Wrap().Children(func() {
			for _, emoji := range emojis {
				chosen := emoji
				mine := reactionMine(stats, chosen)
				variant := action.Default
				if mine {
					variant = action.Filled
				}
				b := action.StyleButton(c, ui.ButtonBase(c), chosen, action.ButtonProps{
					Size: lobetheme.Small, Variant: variant,
				})
				b.FontSize(17).Label("选择表情 " + chosen).Tooltip("添加 " + chosen)
				if mine {
					b.TextColor(tk.Primary)
				}
				if d.react.busy {
					b.Disabled(true)
				}
				if b.Clicked() {
					d.react.reactOpen = false
					a.toggleReaction(d, target, chosen)
				}
			}
		})
	})
}

// rewardPresetAmounts 是打赏弹层里的常用金额：打赏多数是整数，手打一串数字
// 容易点错，点一下芯片就填进输入框。上限是 500，所以预设值到 500 为止。
var rewardPresetAmounts = []int{100, 150, 200, 300, 350, 400, 500}

// rewardPanel 是打赏气泡里的内容：金额、常用金额、余额与确认按钮。面板是贴着
// 金币入口的 Lobe Popover，内部文案走 display.Text、金额芯片走 Lobe Button，
// 宽度收到 248，点一下就能在原地填完，不用再等盖住整页的 Modal。按钮上写着
// 具体数字，点下去才会扣币。
func (a *app) rewardPanel(c *ui.Context, d *detailState, target string) {
	ui.Column(c).Width(248).Gap(6).Children(func() {
		display.Text(c, "打赏金币", display.TextProps{
			Strong: true, FontSize: 12, Rows: 1,
		})
		if !a.auth.loggedIn() {
			a.loginHint(c, "登录后才能打赏。")
			return
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.NumberInput(c, &d.react.amount, rewardAmountLo, rewardAmountHi, 1).
				Label("打赏金额").Width(92).Height(28).FontSize(12)
			display.Text(c, "金币", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
			if a.coins != nil && a.coins.balanceOK {
				display.Text(c, fmt.Sprintf("余额 %s", coinThousands(a.coins.balance)),
					display.TextProps{Type: display.TextSecondary, FontSize: 11, Rows: 1})
			}
		})
		display.Text(c, "常用金额", display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 1,
		})
		ui.Row(c).Gap(4).Wrap().Children(func() {
			for _, amount := range rewardPresetAmounts {
				a.rewardAmountChip(c, d, amount)
			}
		})
		display.Text(c, "从余额扣除，确认后不可撤销。", display.TextProps{
			Type: display.TextSecondary, FontSize: 10, Rows: 2,
		})
		if d.react.rewardErr != "" {
			display.Text(c, d.react.rewardErr, display.TextProps{
				Type: display.TextDanger, FontSize: 11, Rows: 2,
			})
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c).Grow(1)
			if d.react.rewardBusy {
				lobeSpin(c, feedback.SpinProps{CustomSize: 13})
				display.Text(c, "正在打赏…", display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
				return
			}
			btn := action.Button(c, fmt.Sprintf("打赏 %d 金币", rewardAmount(d.react.amount)),
				action.ButtonProps{Size: lobetheme.Small, Variant: action.Primary})
			btn.Disabled(d.react.busy)
			if btn.Clicked() {
				a.submitReward(d, target)
			}
		})
	})
}

// rewardAmountChip 是打赏气泡里的一枚常用金额：Lobe Small 按钮改成全圆角，
// 数字即金额，当前金额用强调色高亮，点一下填进金额输入框。读屏名写成
// 「常用金额 200 金币」，免得一串裸数字说不清是什么，也不会和下方
// 「打赏 200 金币」的确认按钮撞名。
func (a *app) rewardAmountChip(c *ui.Context, d *detailState, amount int) {
	variant := action.Default
	if rewardAmount(d.react.amount) == amount {
		variant = action.Primary
	}
	chip := action.StyleButton(c, ui.ButtonBase(c), strconv.Itoa(amount), action.ButtonProps{
		Size: lobetheme.Small, Variant: variant,
	})
	chip.Radius(999).FontSize(11)
	if d.react.rewardBusy {
		chip.Disabled(true)
	}
	chip.Label(fmt.Sprintf("常用金额 %d 金币", amount)).
		Tooltip(fmt.Sprintf("点一下把金额填成 %d 金币", amount))
	if chip.Clicked() {
		d.react.amount = float64(amount)
		d.react.rewardErr = ""
	}
}

// detailsPanel 是摘要胶囊的气泡内容：每种表情各几次，自己选过的标「我」。
// 同样是一张贴着胶囊的 Lobe Popover，里面文案走 display.Text、标记走 Lobe Tag。
// 点某一行可以直接表态 / 取消，气泡保持打开，数字会就地更新。
func (a *app) detailsPanel(c *ui.Context, d *detailState, target string) {
	tk := lobetheme.TokensOf(c)
	shown := visibleReactions(reactionStatsOf(d, target))
	canAct := a.canActOn(d, target)
	ui.Column(c).Width(220).Gap(3).Children(func() {
		display.Text(c, "表情明细", display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 1,
		})
		if len(shown) == 0 {
			display.Text(c, "还没有人添加表情。", display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
			return
		}
		for _, s := range shown {
			stat := s
			row := ui.Row(c).FillWidth().Padding(3, 6).Radius(6).Gap(6).
				AlignItems(ui.Center).Background(ui.Transparent)
			if canAct && !d.react.busy {
				row.Label(fmt.Sprintf("%s %d", stat.Emoji, stat.Count))
				row.Tooltip(reactionBadgeTip(stat))
				if row.Hovered() {
					row.Background(tk.FillSecondary)
				}
				row.OnClick(func() { a.toggleReaction(d, target, stat.Emoji) })
			}
			row.Children(func() {
				display.Text(c, stat.Emoji, display.TextProps{FontSize: 14, Rows: 1})
				display.Text(c, strconv.Itoa(stat.Count), display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 1,
				})
				if stat.Mine {
					display.Tag(c, "我", display.TagProps{
						Size: lobetheme.Small, Color: display.TagBlue,
						Variant: display.TagFilled, Round: true,
					})
				}
			})
		}
	})
}

// loginHint 是弹层里的登录提示：未登录时用户已经点开了入口，就地给一个
// 去登录的出口，不在弹窗里再套一层弹窗。owner 是弹层所在的窗口。文案与按钮
// 都走 Lobe，和表态 / 打赏气泡的其余内容同一套视觉。
func (a *app) loginHintFor(c *ui.Context, owner toastTarget, message string) {
	display.Text(c, message, display.TextProps{
		Type: display.TextSecondary, FontSize: 11, Rows: 2,
	})
	if action.Button(c, "去登录", action.ButtonProps{
		Size: lobetheme.Small, Variant: action.Primary,
	}).Clicked() {
		a.requestLoginFor(owner, message)
	}
}

// loginHint 是主窗口弹层里的登录提示。
func (a *app) loginHint(c *ui.Context, message string) {
	a.loginHintFor(c, a.detailToast(), message)
}

// rewardBadge 是打赏汇总胶囊：只显示金币合计，悬停给出次数与打赏者。
func (a *app) rewardBadge(c *ui.Context, rewards rewardStat) {
	if rewards.Total <= 0 {
		return
	}
	t := c.Theme()
	// 打赏汇总用 Lobe 的金色 Tag：金额一眼能认出是金币，仍是圆角小胶囊，
	// 悬停说明与点击明细都挂在 Tag 上。
	chip := display.Tag(c, "", display.TagProps{
		Size: lobetheme.Small, Round: true, Color: display.TagWarning,
	})
	chip.Tooltip(rewardBadgeTip(rewards))
	chip.Children(func() {
		ui.Text(c, "💰").FontSize(12)
		ui.Textf(c, "%d", rewards.Total).FontSize(a.typeSizes().metaSmall).TextColor(t.Warning)
	})
}

// reactionChoices 是可选表情：已有聚合与节点推荐的并集。已有聚合排在前面，
// 「别人表过、我没选」的表情也在第一眼可见。
func reactionChoices(p *topicDetail, stats []reactionStat) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, reactionEmojiMax)
	add := func(emoji string) {
		if emoji == "" || seen[emoji] || len(out) == reactionEmojiMax {
			return
		}
		seen[emoji] = true
		out = append(out, emoji)
	}
	for _, s := range stats {
		add(s.Emoji)
	}
	if p != nil {
		for _, emoji := range p.NodeEmojis {
			add(emoji)
		}
	}
	return out
}

/* ------------------------------------------------------------------
   打赏
-------------------------------------------------------------- */

// rewardAmount 把输入框里的浮点数收成 1～500 的整数。
func rewardAmount(amount float64) int {
	n := int(math.Round(amount))
	if n < rewardAmountLo {
		n = rewardAmountLo
	}
	if n > rewardAmountHi {
		n = rewardAmountHi
	}
	return n
}

// rewardNeed 组装打赏请求体：帖子用 short_id，评论用 uuid；path 只用于统计。
func rewardNeed(d *detailState, target string) (rewardBody, bool) {
	if d == nil || d.topic == nil {
		return rewardBody{}, false
	}
	path := topicSitePath(d.topic)
	if target == reactionTopic {
		if d.topic.AuthorID == "" || d.shortID == "" {
			return rewardBody{}, false
		}
		return rewardBody{Type: "post", PostID: d.shortID, Path: path}, true
	}
	r, ok := findReply(d.replyList, target)
	if !ok || r.AuthorID == "" {
		return rewardBody{}, false
	}
	return rewardBody{Type: "comment", CommentID: r.ID, Path: path}, true
}

// submitReward 提交一笔打赏。同一笔打赏的超时重试复用同一个
// 幂等键；金额或目标变了才换新键。成功后本地先记账，并刷新标题栏余额。
func (a *app) submitReward(d *detailState, target string) {
	if d == nil || !a.detailLive(d) || d.react.rewardBusy || d.react.busy {
		return
	}
	// 与表态同理：入口隐藏之外再拦一次，避免打赏弹层还开着时内容变成自己的。
	if a.isOwnTarget(d, target) {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能打赏。")
		return
	}
	body, ok := rewardNeed(d, target)
	if !ok {
		a.toast(a.detailToastFor(d), "缺少打赏所需的信息，请刷新后重试")
		return
	}
	amount := rewardAmount(d.react.amount)
	body.Amount = amount
	sig := fmt.Sprintf("%s|%d|%s|%s", target, amount, body.Type, body.Path)
	if d.react.rewardKey == "" || d.react.rewardSig != sig {
		d.react.rewardKey = randomURLSafe(24)
		d.react.rewardSig = sig
	}
	key := d.react.rewardKey
	d.react.rewardBusy = true
	d.react.rewardErr = ""
	d.react.rewardGen++
	gen := d.react.rewardGen
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), interactTimeout)
	go func() {
		defer cancel()
		err := a.client.sendReward(ctx, key, body)
		a.publish(func() {
			if !a.detailLive(d) || d.react.rewardGen != gen {
				return
			}
			d.react.rewardBusy = false
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthError(err) {
					a.notifyInteractDone(err)
					return
				}
				d.react.rewardErr = friendlyError(err)
				a.notifyInteractDone(err)
				return
			}
			// 成功才记账：失败时界面停在原数额，不会假装打过赏。
			d.react.rewardKey = ""
			d.react.rewardSig = ""
			a.applyReward(d, target, amount, actor)
			a.toast(a.detailToastFor(d), fmt.Sprintf("已打赏 %d 金币", amount))
			// 收起打赏弹层，让用户看到刚记上的总数。
			d.react.rewardOpen = false
			d.react.rewardErr = ""
			a.notifyInteractDone(nil)
		})
	}()
}

// applyReward 把一笔成功的打赏记进本地汇总，界面立刻能看到。
func (a *app) applyReward(d *detailState, target string, amount int, actor string) {
	if d == nil || amount <= 0 {
		return
	}
	add := func(r *rewardStat) {
		r.Total += amount
		r.Count++
		if actor == "" {
			return
		}
		for _, u := range r.Users {
			if u == actor {
				return
			}
		}
		r.Users = append(r.Users, actor)
	}
	if target == reactionTopic {
		if d.topic != nil {
			add(&d.topic.Rewards)
		}
		return
	}
	for i := range d.replyList {
		if d.replyList[i].ID == target {
			add(&d.replyList[i].Rewards)
			return
		}
	}
}
