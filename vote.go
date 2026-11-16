package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// 投票（api-write.md）
//
//	POST /v1/topics/:short_id/vote        scope write:topic
//	POST /v1/topics/:short_id/vote/multi  scope write:topic
//	GET  /v1/topics/:short_id/vote/result scope read，?vote_id=uuid
//
// 这一组的请求体是 camelCase：voteId / optionId / optionIds，和别处的
// snake_case 不能顺手统一。两个写端点都没标 🔑，不带 Idempotency-Key。
// scope 与发帖同为 write:topic（v1 的 scope 表里没有 write:vote），缺权限
// 时交给 auth_gate.go 的全局授权链路，授权成功后自动重放这一票。
//
// 详情里的投票 uuid / 选项 uuid 由 detail.go 的 decodeVote 解码。投票结果
// 用单独的读端点取：详情只带各选项票数，拿不到「结果是否公开、总共多少票」
// 这类聚合信息，分布要读 /vote/result。文档没有给这个端点的响应示例，解码
// 按容错读取（options / results / items，票数 vote_count / votes / count），
// 见 decodeVoteResult。
//
// 投票结果不另写一套详情解码：提交成功后重新拉一次详情，票数与结束状态以
// 服务端为准；分布另外读一次 result。

const voteTimeout = 20 * time.Second

// voteOptionMax 是 /vote/multi 的 optionIds 上限，来自文档。
const voteOptionMax = 20

func topicVotePath(shortID string) string {
	return topicPath(shortID) + "/vote"
}

func topicMultiVotePath(shortID string) string {
	return topicPath(shortID) + "/vote/multi"
}

// topicVoteResultPath 是投票结果端点。注意 vote_id 是查询参数、snake_case，
// 与提交时的 voteId 不是同一个写法。
func topicVoteResultPath(shortID, voteID string) string {
	q := url.Values{}
	q.Set("vote_id", voteID)
	return topicPath(shortID) + "/vote/result?" + q.Encode()
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
//
// result / resultOpen / resultLoading / resultErr 是结果分布：只有点开
// 「查看投票结果」、投票结束后才读一次，不为了一个没人看的分布多发请求。
type voteUIState struct {
	busy   bool
	voted  bool
	gen    uint64
	checks []bool

	resultOpen    bool
	result        *voteResult
	resultLoading bool
	resultErr     string
	resultGen     uint64
}

// voteResult 是 GET /vote/result 的归一结果：各选项票数与总票数。Visible
// 记录结果是否公开（result_visible）；不公开时服务端只回票数分布、不给
// 明细，这里的 Options 仍然可用。
type voteResult struct {
	Options      []voteOption
	Total        int
	Visible      bool
	VisibleKnown bool
}

// fetchVoteResult 读一次投票结果。纯读端点，scope 是 read，匿名也能读。
func (c *Client) fetchVoteResult(ctx context.Context, shortID, voteID string) (voteResult, error) {
	return withAuthRetry(c, ctx, func(token string) (voteResult, error) {
		d, err := c.getJSON(ctx, c.apiURL(topicVoteResultPath(shortID, voteID)), token)
		if err != nil {
			return voteResult{}, err
		}
		return decodeVoteResult(d), nil
	})
}

// decodeVoteResult 容错解析投票结果。文档没有给响应示例，线上可能的形状：
// d 是裸数组，或包在 options / results / items 里；票数字段可能是
// vote_count / votes / count。总数没有单独字段时用各选项求和。
func decodeVoteResult(d json.RawMessage) voteResult {
	var out voteResult
	list := rawArray(d)
	sum := 0
	if o := asObject(d); o != nil {
		for _, k := range []string{"options", "results", "items", "list"} {
			if l := rawArray(o[k]); len(l) > 0 {
				list = l
				break
			}
		}
		out.Total = o.num("total_votes", "total", "vote_count", "votes", "count")
		if o.has("result_visible", "visible") {
			out.VisibleKnown = true
			out.Visible = o.flag("result_visible", "visible")
		}
	}
	for _, raw := range list {
		opt := asObject(raw)
		if opt == nil {
			continue
		}
		row := voteOption{
			ID:    opt.str("id", "option_id"),
			Text:  cleanText(opt.str("text", "title", "name")),
			Votes: opt.num("vote_count", "votes", "count"),
		}
		if row.ID == "" && row.Text == "" {
			continue
		}
		sum += row.Votes
		out.Options = append(out.Options, row)
	}
	if out.Total <= 0 {
		out.Total = sum
	}
	return out
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

// loadVoteResult 读一次投票结果分布。同一个投票在途时不重复发；gen 丢弃
// 切帖子之前的迟到结果。失败只把原因留在投票块里（可重试），不弹 toast：
// 这是一个用户主动点开的只读查询。
func (a *app) loadVoteResult(d *detailState) {
	if d == nil || a.detail != d || d.topic == nil || d.topic.Vote == nil || !a.autoLoad {
		return
	}
	v := d.topic.Vote
	if v.ID == "" || d.vote.resultLoading {
		return
	}
	d.vote.resultLoading = true
	d.vote.resultErr = ""
	d.vote.resultGen++
	gen := d.vote.resultGen
	shortID, voteID := d.shortID, v.ID
	ctx, cancel := context.WithTimeout(context.Background(), voteTimeout)
	go func() {
		defer cancel()
		res, err := a.client.fetchVoteResult(ctx, shortID, voteID)
		a.publish(func() {
			if a.detail != d || d.vote.resultGen != gen {
				return
			}
			d.vote.resultLoading = false
			if err != nil {
				if a.handleAuthError(err, func() {
					if a.detail == d {
						a.loadVoteResult(d)
					}
				}) {
					return
				}
				d.vote.resultErr = friendlyError(err)
				return
			}
			d.vote.result = &res
		})
	}()
}

// toggleVoteResult 展开 / 收起结果分布。第一次展开时读一次端点。
func (a *app) toggleVoteResult(d *detailState) {
	if d == nil || a.detail != d {
		return
	}
	d.vote.resultOpen = !d.vote.resultOpen
	if d.vote.resultOpen && d.vote.result == nil {
		a.loadVoteResult(d)
	}
}

// showVoteResult 在投票成功（或已投过）后把结果分布直接摆出来：投完最想
// 看的就是分布，不必再点一次。票数刚变过，已经读过的那一份要重读。
func (a *app) showVoteResult(d *detailState) {
	if d == nil {
		return
	}
	d.vote.resultOpen = true
	a.loadVoteResult(d)
}

// submitVote 提交选中的选项。结果一律走主窗口 toast（见 toasts.go），页面里
// 只留「正在提交」的进度。成功后重新拉详情，把服务端算出的票数显示出来，
// 并把结果分布直接展开。
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
					a.showVoteResult(d)
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
			a.showVoteResult(d)
			a.loadTopic()
			a.notifyInteractDone(nil)
		})
	}()
}
