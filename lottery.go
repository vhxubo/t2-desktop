package main

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// 抽奖助手（@抽奖助手）不是独立端点，而是「帖主发一条一级评论」触发的副作用，
// 见 api-write.md「提及（@）与抽奖」。客户端的完整链路是：
//
//  1. 发帖人身份发一条一级评论，正文为「@抽奖助手 抽 N 个」（数量必填，阿拉伯
//     或中文数字均可；这里统一用阿拉伯数字），不传 parentId，level 为 0；
//     分身身份不触发，所以这里固定用窗口自己的账号。
//  2. 评论的 201 只表示写入成功，开奖随后异步完成。按文档建议每 3 秒轮询一次
//     GET /v1/topics/:short_id，最多 30 秒，直到 extra.lottery 出现；不要靠
//     评论数变化去猜开奖时间。
//  3. 开奖失败时评论照样 201，原因作为一条 type=system 通知发给帖主。超时后
//     拉一次系统通知，把 payload.error 展示给用户（拉不到就给固定说法）。
//
// 触发评论走 Client.createReply（proxyManager 的共享 transport + 限流闸门），
// 轮询走 Client.fetchTopic，不在这里自建 http.Client。

const (
	lotteryName           = "抽奖助手"
	lotteryMinWinners     = 1
	lotteryMaxWinners     = 10
	lotteryDefaultWinners = 1
	// lotteryScope 是触发评论要求的写权限（写评论）。
	lotteryScope        = "write:comment"
	lotteryFetchTimeout = 12 * time.Second
	lotteryPostTimeout  = 25 * time.Second
)

// 文档建议的轮询节奏是 3 秒一次、最多 30 秒；做成变量方便测试把节奏压缩到
// 毫秒级，线上仍按文档值走。
var (
	lotteryPollInterval = 3 * time.Second
	lotteryPollTimeout  = 30 * time.Second
)

// lotteryWinner 是开奖结果里的一名中奖者。楼层字段有两种口径：floor 是楼层
// 视图的楼层，flat_floor 是扁平视图的楼层；展示时优先 floor，缺失再用 flat_floor。
type lotteryWinner struct {
	Username  string
	Floor     int
	FlatFloor int
	CommentID string
}

// lotteryComment 是抽奖助手发出的那条中奖名单评论的位置。
type lotteryComment struct {
	ID           string
	LocatedFloor int
	FlatFloor    int
}

// lotteryInfo 是 extra.lottery 的完整开奖结果。非空指针表示帖子已经开过奖，
// 同一帖只能开一次；解码为空对象时返回 nil，不当成「已开奖」。
type lotteryInfo struct {
	FinishedAt string
	Comment    lotteryComment
	Winners    []lotteryWinner
}

// decodeTopicLottery 用容错读取解码 extra.lottery：线上在 extra 里，也兼容
// 直接平铺在顶层的形状；未知字段忽略，缺字段留零值。空对象不算「已开奖」。
func decodeTopicLottery(o object) *lotteryInfo {
	var raw object
	if extra := o.sub("extra"); extra != nil {
		raw = extra.sub("lottery")
	}
	if raw == nil {
		raw = o.sub("lottery")
	}
	if raw == nil {
		return nil
	}
	out := &lotteryInfo{FinishedAt: raw.str("finished_at", "finishedAt")}
	if c := raw.sub("comment"); c != nil {
		out.Comment = lotteryComment{
			ID:           c.str("id"),
			LocatedFloor: c.num("located_floor", "locatedFloor"),
			FlatFloor:    c.num("flat_floor", "flatFloor"),
		}
	}
	for _, item := range rawArray(raw["winners"]) {
		w := asObject(item)
		if w == nil {
			continue
		}
		name := strings.TrimSpace(w.str("username"))
		if name == "" {
			continue
		}
		out.Winners = append(out.Winners, lotteryWinner{
			Username:  name,
			Floor:     w.num("floor"),
			FlatFloor: w.num("flat_floor", "flatFloor"),
			CommentID: w.str("comment_id", "commentId"),
		})
	}
	if out.FinishedAt == "" && out.Comment.ID == "" && len(out.Winners) == 0 {
		return nil
	}
	return out
}

// lotteryCommand 拼出触发抽奖的评论正文。@ 后面必须有空格，否则服务端把
// 整段当用户名、既不高亮也不触发抽奖指令。
func lotteryCommand(count int) string {
	return "@" + lotteryName + " 抽 " + strconv.Itoa(count) + " 个"
}

// lotteryResultText 把开奖结果压成一条 toast：中奖者带楼层，超过 5 人折叠。
func lotteryResultText(info *lotteryInfo) string {
	if info == nil {
		return "开奖完成，但没有产生中奖名单"
	}
	if len(info.Winners) == 0 {
		return "开奖完成，但没有符合资格的参与人"
	}
	const shown = 5
	parts := make([]string, 0, shown+1)
	for i, w := range info.Winners {
		if i >= shown {
			break
		}
		floor := w.Floor
		if floor == 0 {
			floor = w.FlatFloor
		}
		if floor > 0 {
			parts = append(parts, "@"+w.Username+"（"+strconv.Itoa(floor)+" 楼）")
		} else {
			parts = append(parts, "@"+w.Username)
		}
	}
	if len(info.Winners) > shown {
		parts = append(parts, "等 "+strconv.Itoa(len(info.Winners))+" 人")
	}
	return "开奖完成，中奖：" + strings.Join(parts, "、")
}

// lotteryFailureReason 在开奖超时后拉一次系统通知，把服务端给出的失败原因
// 取出来。只认 type=system 且 payload.error/message/content/detail 非空的消息，
// 并且只认这次抽奖发起之后到达的（按 ISO 时间字符串比较）。拉不到时返回空串，
// 调用方改用固定说法，不让一条旧通知冒充这次失败的原因。
func (c *Client) lotteryFailureReason(ctx context.Context, since string) string {
	reason := ""
	_, _ = withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		d, err := c.getJSON(ctx, c.apiURL(notificationsPath+"?page=1&limit=10&type=system"), token)
		if err != nil {
			return struct{}{}, err
		}
		for _, n := range decodeNotificationsPage(d, 1).Items {
			if n.Type != "system" {
				continue
			}
			if since != "" && (n.CreatedAt == "" || n.CreatedAt < since) {
				continue // 时间缺失或早于本次抽奖，都不能冒充这次的原因
			}
			if msg := strings.TrimSpace(n.Payload.str("error", "message", "content", "detail")); msg != "" {
				reason = msg
				break
			}
		}
		return struct{}{}, nil
	})
	return reason
}

// lotteryState 是一次抽奖的界面状态：awaiting 决定确认框是否展开，count 是
// 选中的中奖人数，busy 在途时把工具条图标换成转圈并禁用，gen 丢弃迟到结果。
// 只活在 detailState 上，不存 ui.Element / *ui.Context。
type lotteryState struct {
	awaiting bool
	count    int
	busy     bool
	gen      uint64
}

// canLottery 报告这篇帖子是否给出抽奖入口。抽奖只允许帖主本人发起，判断与
// 编辑 / 关闭评论入口一致：昵称等于当前账号，且不是匿名帖。
func (a *app) canLottery(d *detailState) bool {
	return a.canEditTopic(d)
}

// askLottery 展开「发起抽奖」确认框。已经开过奖时直接展示结果，不再弹框。
func (a *app) askLottery(d *detailState) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.lottery.busy || !a.canLottery(d) {
		return
	}
	if d.topic.Lottery != nil {
		a.toast(a.detailToastFor(d), lotteryResultText(d.topic.Lottery))
		return
	}
	if d.lottery.count < lotteryMinWinners || d.lottery.count > lotteryMaxWinners {
		d.lottery.count = lotteryDefaultWinners
	}
	d.lottery.awaiting = true
}

// startLottery 发起一次抽奖：先发触发评论，成功后再轮询开奖结果。所有校验
// 都在 UI 线程完成，网络结果统一经 a.publish 回到 UI 线程写状态、发 toast。
func (a *app) startLottery(d *detailState) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.lottery.busy || !a.canLottery(d) {
		return
	}
	target := a.detailToastFor(d)
	d.lottery.awaiting = false
	switch {
	case d.topic.Lottery != nil:
		a.toast(target, "这篇帖子已经开过奖，不能重复抽奖")
		return
	case d.topic.CommentsClosed:
		a.toast(target, "帖子已关闭评论，无法发起抽奖")
		return
	case a.client == nil || !a.autoLoad:
		return
	case a.auth == nil || !a.auth.loggedIn():
		a.toast(target, "请先登录后再抽奖")
		return
	}
	count := d.lottery.count
	if count < lotteryMinWinners || count > lotteryMaxWinners {
		count = lotteryDefaultWinners
	}
	d.lottery.busy = true
	d.lottery.gen++
	gen := d.lottery.gen
	shortID := d.shortID
	cmd := lotteryCommand(count)
	// ISO 时间只到秒，和通知的 created_at 同为 UTC 时可直接比较。
	since := time.Now().UTC().Add(-time.Minute).Format("2006-01-02T15:04:05")

	go func() {
		postCtx, postCancel := context.WithTimeout(context.Background(), lotteryPostTimeout)
		created, err := a.client.createReply(postCtx, shortID, cmd, replyTarget{})
		postCancel()
		if err != nil {
			a.publish(func() {
				if !a.detailLive(d) || d.lottery.gen != gen {
					return
				}
				d.lottery.busy = false
				if a.handleAuthErrorWithScopesFor(target, err, []string{lotteryScope}, func() {
					if a.detailLive(d) {
						a.startLottery(d)
					}
				}) {
					return
				}
				a.toast(target, "发起抽奖失败："+friendlyError(err))
			})
			return
		}

		// 触发评论已写入：和手工发评论一样就地插进列表，发帖人立刻看到自己
		// 那条「@抽奖助手 抽 N 个」，而不必等下一次刷新。
		a.publish(func() {
			if !a.detailLive(d) {
				return
			}
			if created.Author == "" || created.Author == "匿名" {
				created.Author = a.auth.username()
			}
			if created.Floor == 0 && created.FlatFloor > 0 {
				created.Floor = created.FlatFloor
			}
			d.replyList, _ = insertReply(d.replyList, created, "", d.flat)
			d.rebuildDisplay()
			d.replyTotal++
			a.toast(target, "抽奖已发起，正在等待开奖…")
		})

		// 按文档建议轮询详情：3 秒一次，最多 30 秒，只看 extra.lottery。
		ctx, cancel := context.WithTimeout(context.Background(), lotteryPollTimeout)
		defer cancel()
		ticker := time.NewTicker(lotteryPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				// 网络请求留在后台线程：把系统通知里的失败原因先取回来，
				// publish 到 UI 线程的只有字符串。
				fctx, fcancel := context.WithTimeout(context.Background(), lotteryFetchTimeout)
				reason := a.client.lotteryFailureReason(fctx, since)
				fcancel()
				a.publish(func() {
					if !a.detailLive(d) || d.lottery.gen != gen {
						return
					}
					d.lottery.busy = false
					if reason == "" {
						reason = "开奖还在进行中，稍后刷新这篇帖子能看到中奖名单；失败原因请到系统消息里查看"
					}
					a.toast(target, "抽奖未完成："+reason)
				})
				return
			case <-ticker.C:
				fctx, fcancel := context.WithTimeout(context.Background(), lotteryFetchTimeout)
				topic, err := a.client.fetchTopic(fctx, shortID)
				fcancel()
				if err != nil || topic.Lottery == nil {
					// 网络抖动 / 还没开完都继续等下一次，不把中间失败亮给用户。
					continue
				}
				result := topic.Lottery
				a.publish(func() {
					if !a.detailLive(d) || d.lottery.gen != gen {
						return
					}
					d.lottery.busy = false
					if d.topic != nil {
						d.topic.Lottery = result
					}
					a.toast(target, lotteryResultText(result))
				})
				return
			}
		}
	}()
}
