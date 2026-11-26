package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
)

// 「评论开关」：作者关闭 / 重新开启自己帖子下的评论。端点逐字照抄
// openapi v1 的 api-write.md「评论开关」一节：
//
//	POST /v1/topics/:short_id/comments-status   scope write:topic，仅作者
//
// 请求体只有 closed 一个布尔值；端点没有标 🔑，所以不带 Idempotency-Key。
// 响应 d 里是 { message, comments_closed, comments_reopened_at,
// next_close_available_at }，逻辑只认 comments_closed。
//
// 三条容易踩的规则：
//   - 只有作者能开关：非作者返回 403 permission_denied。入口因此只在昵称对得上
//     时出现（与编辑入口同一个判断，见 canEditTopic），不拿一个点了必然 403 的
//     按钮占位置；匿名帖确认不了作者，同样不给。
//   - 重新开启有冷却时间：冷却期内再关闭会被拒，服务端用 next_close_available_at
//     说明下一次能关的时间。确认框里先把这条讲清楚，不在客户端自己编倒计时。
//   - 这个端点属于文档里标「需开通」的高级能力：应用没被开通时服务端回
//     403 insufficient_scope。重新授权解决不了它，所以单独说明一句，不把用户
//     送进一个注定失败的浏览器授权流程（见 commentsStatusUngranted）。

// commentsStatusScope 是这个端点要求的权限范围（文档「评论开关」一节）。
const commentsStatusScope = "write:topic"

func topicCommentsStatusPath(shortID string) string {
	return topicPath(shortID) + "/comments-status"
}

// commentsStatusResult 是评论开关响应里我们认得的字段。Known 区分「服务端确认
// 了关闭状态」和「响应里没有这个字段」——后者不能拿 false 当「已开启」。响应里
// 还有 message 与冷却时间，但提示文案用本地固定说法，不拿服务端文案做逻辑。
type commentsStatusResult struct {
	Closed bool
	Known  bool
}

func decodeCommentsStatus(raw json.RawMessage) commentsStatusResult {
	o := asObject(raw)
	if o == nil {
		return commentsStatusResult{}
	}
	closed, known := o.boolValue("comments_closed")
	return commentsStatusResult{Closed: closed, Known: known}
}

// setTopicCommentsClosed 提交一次评论开关，返回服务端说明的状态。
func (c *Client) setTopicCommentsClosed(ctx context.Context, shortID string, closed bool) (commentsStatusResult, error) {
	var res commentsStatusResult
	payload := map[string]any{"closed": closed}
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		d, err := c.postJSON(ctx, c.apiURL(topicCommentsStatusPath(shortID)), token, "", payload)
		if err != nil {
			return struct{}{}, err
		}
		res = decodeCommentsStatus(d)
		return struct{}{}, nil
	})
	return res, err
}

// commentsStatusState 是「关闭 / 开启评论」这一次操作的界面状态：awaiting 表示
// 确认框开着（这个动作影响其他读者，而且重新开启有冷却），busy 表示请求在途，
// gen 用来丢弃迟到的响应（同一帖连点、或换帖之后回来的结果都不该落到页面上）。
type commentsStatusState struct {
	awaiting bool
	busy     bool
	gen      uint64
}

// canToggleComments 报告这篇帖子此刻是否给出「关闭 / 开启评论」入口。接口只允许
// 作者本人；它与 10 分钟的正文编辑窗口无关，不能因为 can_edit 关闭就跟着消失。
func (a *app) canToggleComments(d *detailState) bool {
	return a.isPostAuthor(d)
}

// askToggleComments 展开确认框。关闭评论会影响其他读者的回复，重新开启又有冷却
// 时间，所以不点一下就发生。
func (a *app) askToggleComments(d *detailState) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.comments.busy || !a.canToggleComments(d) {
		return
	}
	d.comments.awaiting = true
}

// toggleTopicComments 提交一次「关闭 / 开启评论」。成功后就地改页面上的评论
// 状态（副标题的「评论已关闭」与底部输入框跟着变），结果用主窗口的 toast 报告；
// 失败原因同样只用 toast，页面保持原样。
func (a *app) toggleTopicComments(d *detailState) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.comments.busy || !a.canToggleComments(d) {
		return
	}
	desired := !d.topic.CommentsClosed
	d.comments.awaiting = false
	d.comments.busy = true
	d.comments.gen++
	gen := d.comments.gen
	shortID := d.shortID
	ctx, cancel := context.WithTimeout(context.Background(), interactTimeout)
	go func() {
		defer cancel()
		res, err := a.client.setTopicCommentsClosed(ctx, shortID, desired)
		a.publish(func() {
			if !a.detailLive(d) || d.comments.gen != gen {
				return // 已经换了帖子或又提交过一次：这次结果不再改页面
			}
			d.comments.busy = false
			if err != nil {
				// 「需开通」是应用级限制，重新授权解决不了：直接说明，不弹授权。
				if a.commentsStatusUngranted(err) {
					a.toast(a.detailToastFor(d), "2Libra 还没有给本应用开通「关闭评论」：需要在应用申请页说明用途、由平台开通后才能用。")
					return
				}
				// 缺 write:topic 时先补授权，回调成功后按同样的目标重发一次；
				// 授权链路已经接手这次失败，不再另外弹一条错误提示。
				if a.handleAuthError(err) {
					return
				}
				a.toast(a.detailToastFor(d), friendlyError(err))
				return
			}
			closed := desired
			if res.Known {
				closed = res.Closed
			}
			a.applyCommentsClosed(d, closed)
		})
	}()
}

// applyCommentsClosed 把服务端确认的评论状态落到页面上。
func (a *app) applyCommentsClosed(d *detailState, closed bool) {
	if d == nil || d.topic == nil {
		return
	}
	d.topic.CommentsClosed = closed
	if closed {
		// 关闭之后「正在回复某条评论」挂在一个发不出去的输入框上，收掉这条引用；
		// 草稿正文留着，重新开启时还能接着写。
		d.replyTo = replyTarget{}
		a.toast(a.detailToastFor(d), "评论已关闭，其他读者不能再回复")
	} else {
		a.toast(a.detailToastFor(d), "评论已开启，其他读者可以继续回复")
	}
	a.saveCachedDetail(d)
}

// commentsStatusUngranted 报告这次失败是不是「端点没有给这个应用开通」。
// comments-status 属于文档里标「需开通」的高级能力：应用没开通时服务端回
// 403 insufficient_scope，而用户当前的令牌里其实已经有 write:topic —— 要求里
// 每一项都在已授予的范围内，说明缺的不是用户的授权，而是平台对这个应用的单独
// 开通。这时再弹一次授权框只会让用户白跑一趟浏览器。
func (a *app) commentsStatusUngranted(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != "insufficient_scope" {
		return false
	}
	required := ae.RequiredScope
	if len(required) == 0 {
		required = []string{commentsStatusScope}
	}
	for _, s := range required {
		if !a.hasAuthScope(s) {
			return false
		}
	}
	return true
}

// commentsStatusButton 是标题栏里那枚「关闭 / 开启评论」图标：只有作者看得到。
// 请求在途时换成转圈并禁用，避免连点把同一件事反复提交。
func (a *app) commentsStatusButton(c *ui.Context, d *detailState) {
	if !a.canToggleComments(d) {
		return
	}
	busy := d.comments.busy
	icon, label, tip := lucide.MessageSquareOff(), "关闭评论", "关闭评论：关闭后其他读者不能再回复"
	if d.topic.CommentsClosed {
		icon, label, tip = lucide.MessageCircle(), "开启评论", "重新开启这篇帖子的评论"
	}
	if busy {
		label, tip = "正在提交评论开关", "正在提交…"
	}
	a.detailActionIcon(c, icon, label, tip, busy, func() { a.askToggleComments(d) })
}

// commentsStatusDialog 是「关闭 / 开启评论」的确认框。确认后由
// toggleTopicComments 发请求，结果用主窗口的 toast 报告，框里不留结论文字。
func (a *app) commentsStatusDialog(c *ui.Context, d *detailState) {
	if d == nil || d.topic == nil || !d.comments.awaiting {
		return
	}
	t := c.Theme()
	closing := !d.topic.CommentsClosed
	title := "关闭这篇帖子的评论？"
	body := "关闭后其他读者不能再在这篇帖子下回复。重新开启后有一段时间不能再关闭，冷却结束前点了也没用。"
	confirm := "确认关闭评论"
	if !closing {
		title = "重新开启评论？"
		body = "开启后其他读者可以继续在这篇帖子下回复。"
		confirm = "确认开启评论"
	}
	open := true
	ok := feedback.Modal(c, &open, feedback.ModalProps{
		Title:      title,
		Width:      420,
		OkText:     confirm,
		CancelText: "取消",
	}, func() {
		ui.Text(c, body).TextColor(t.TextMuted).MaxLines(3)
	})
	// 点窗外、✕、Esc 与「取消」都与取消同义。
	if !open {
		d.comments.awaiting = false
		return
	}
	if ok {
		a.toggleTopicComments(d)
	}
}
