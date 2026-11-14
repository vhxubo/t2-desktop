package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// 投票（api-write.md）
//
//	POST /v1/topics/:short_id/vote        scope write:topic
//	POST /v1/topics/:short_id/vote/multi  scope write:topic
//
// 这一组的请求体是 camelCase：voteId / optionId / optionIds，和别处的
// snake_case 不能顺手统一。两个端点都没标 🔑，不带 Idempotency-Key。
// scope 与发帖同为 write:topic（v1 的 scope 表里没有 write:vote），缺权限
// 时交给 auth_gate.go 的全局授权链路，授权成功后自动重放这一票。
//
// 详情里的投票 uuid / 选项 uuid 由 detail.go 的 decodeVote 解码。投票结果
// 不另写一套解码：提交成功后重新拉一次详情，票数与结束状态以服务端为准。

const voteTimeout = 20 * time.Second

// voteOptionMax 是 /vote/multi 的 optionIds 上限，来自文档。
const voteOptionMax = 20

func topicVotePath(shortID string) string {
	return topicPath(shortID) + "/vote"
}

func topicMultiVotePath(shortID string) string {
	return topicPath(shortID) + "/vote/multi"
}

// voteBody / multiVoteBody 的字段名照抄文档，服务端按名字取值。
type voteBody struct {
	VoteID   string `json:"voteId"`
	OptionID string `json:"optionId"`
}

type multiVoteBody struct {
	VoteID    string   `json:"voteId"`
	OptionIDs []string `json:"optionIds"`
}

// sendTopicVote 提交一票。multi 为真走多选端点；否则只取第一个选项走单选
// 端点。optionIDs 必须非空，且不能超过 voteOptionMax。
func (c *Client) sendTopicVote(ctx context.Context, shortID, voteID string, optionIDs []string, multi bool) error {
	if len(optionIDs) == 0 {
		return fmt.Errorf("没有选择投票选项")
	}
	if len(optionIDs) > voteOptionMax {
		return fmt.Errorf("最多选择 %d 个选项", voteOptionMax)
	}
	u := topicVotePath(shortID)
	var payload any = voteBody{VoteID: voteID, OptionID: optionIDs[0]}
	if multi {
		u = topicMultiVotePath(shortID)
		payload = multiVoteBody{VoteID: voteID, OptionIDs: optionIDs}
	}
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		_, err := c.postJSON(ctx, c.apiURL(u), token, "", payload)
		return struct{}{}, err
	})
	return err
}

// voteUIState 是详情页投票块的界面状态，只在 UI 线程读写。checks 与
// topic.Vote.Options 等长，供多选投票的勾选框绑定；busy 时整块禁用。
// voted 记的是本次会话里提交成功（或服务端回 409 说已投过），详情重新
// 加载不会把它清掉。gen 用来丢弃切帖子之前的迟到结果。
type voteUIState struct {
	busy   bool
	voted  bool
	gen    uint64
	checks []bool
}

// syncVote 在详情刷新后对齐勾选框的行数。请求在途时不改动：那一票提交完
// 之后 loadTopic 会再拉一次详情，届时再对齐。
func syncVote(d *detailState, t topicDetail) {
	if d == nil || d.vote.busy {
		return
	}
	n := 0
	if t.Vote != nil {
		n = len(t.Vote.Options)
	}
	if len(d.vote.checks) != n {
		d.vote.checks = make([]bool, n)
	}
}

// voteIsMulti 判断这次投票能不能选多项：文档给的是 vote.type=multiple 或
// max_choices>1，两种都认，线上少给一个字段时仍能按另一个判断。
func voteIsMulti(v *voteBlock) bool {
	return v != nil && (v.Type == "multiple" || v.MaxChoice > 1)
}

// submitVote 提交选中的选项。结果一律走主窗口 toast（见 toasts.go），页面里
// 只留「正在提交」的进度。成功后重新拉详情，把服务端算出的票数显示出来。
func (a *app) submitVote(d *detailState, optionIDs []string, multi bool) {
	if d == nil || a.detail != d || d.topic == nil || d.topic.Vote == nil {
		return
	}
	v := d.topic.Vote
	if d.vote.busy || d.vote.voted || v.Finished {
		return
	}
	if v.ID == "" || len(optionIDs) == 0 {
		a.toast(toastMain, "投票信息不完整，请刷新帖子后重试")
		return
	}
	// 选项 uuid 必须来自当前这一份详情：详情刷新后旧选项可能已经不在了。
	valid := make(map[string]bool, len(v.Options))
	for _, o := range v.Options {
		valid[o.ID] = true
	}
	for _, id := range optionIDs {
		if !valid[id] {
			a.toast(toastMain, "选项已变化，请重新选择")
			return
		}
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能参与投票。", func() {
			if a.detail == d {
				a.submitVote(d, optionIDs, multi)
			}
		})
		return
	}
	d.vote.busy = true
	d.vote.gen++
	gen := d.vote.gen
	shortID, voteID := d.shortID, v.ID
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), voteTimeout)
	go func() {
		defer cancel()
		err := a.client.sendTopicVote(ctx, shortID, voteID, optionIDs, multi)
		a.publish(func() {
			if a.detail != d || d.vote.gen != gen {
				return
			}
			d.vote.busy = false
			// 请求期间换了账号 / 退出登录：这一票不属于当前会话。
			if !a.auth.loggedIn() || a.auth.username() != actor {
				a.notifyInteractDone(err)
				return
			}
			if err != nil {
				var ae *apiError
				if errors.As(err, &ae) && ae.Status == http.StatusConflict {
					// 409：多半是在网页或别的客户端已经投过，按已投处理，
					// 并拉一次详情把票数显示对。
					d.vote.voted = true
					a.toast(toastMain, "你已经投过票了")
					a.loadTopic()
					a.notifyInteractDone(nil)
					return
				}
				if a.handleAuthError(err, func() {
					if a.detail == d {
						a.submitVote(d, optionIDs, multi)
					}
				}) {
					a.notifyInteractDone(err)
					return
				}
				a.toast(toastMain, friendlyError(err))
				a.notifyInteractDone(err)
				return
			}
			d.vote.voted = true
			d.vote.checks = make([]bool, len(d.vote.checks))
			a.toast(toastMain, "已投票")
			a.loadTopic()
			a.notifyInteractDone(nil)
		})
	}()
}
