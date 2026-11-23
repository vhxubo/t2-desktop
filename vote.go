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
// 时交给 auth_gate.go 的全局授权链路，授权成功后由用户自己再投一次。
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
// voted 记的是已投过：本次会话里提交成功 / 服务端回 409，或详情与结果端点
// 明确下发「我已投」。详情重新加载不会把它清掉。gen 用来丢弃切帖子之前的迟到
// 结果。
//
// result / resultOpen / resultLoading / resultErr 是结果分布：没投过的人票数默认
// 藏着，点开「查看投票结果」才读一次；投过票的人加载详情时就会直接展开。
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
	Voted        bool
	VotedKnown   bool
}

// voteVotedFlag 容错读「我是否已投」。详情与 /vote/result 都没有在文档里给出
// 这个字段的确定形状：可能是布尔的 voted / has_voted，也可能是我选中的选项
// uuid（my_option_id 一类）。认不出来就返回「未知」，不把没投过当成投过。
func voteVotedFlag(o object) (bool, bool) {
	boolKeys := []string{"voted", "has_voted", "is_voted", "my_voted", "participated", "has_participated"}
	if o.has(boolKeys...) {
		return o.flag(boolKeys...), true
	}
	for _, k := range []string{"my_vote", "my_option_id", "selected_option_id", "my_choice"} {
		if o.str(k) != "" {
			return true, true
		}
	}
	for _, k := range []string{"my_option_ids", "my_votes", "my_choices"} {
		if len(rawArray(o[k])) > 0 || o.str(k) != "" {
			return true, true
		}
	}
	return false, false
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
		out.Voted, out.VotedKnown = voteVotedFlag(o)
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
// 这是只读查询，没投过的人点开才读，投过的人加载详情时自动读。
func (a *app) loadVoteResult(d *detailState) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.topic.Vote == nil || !a.autoLoad {
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
			if !a.detailLive(d) || d.vote.resultGen != gen {
				return
			}
			d.vote.resultLoading = false
			if err != nil {
				if a.handleAuthError(err) {
					return
				}
				d.vote.resultErr = friendlyError(err)
				return
			}
			d.vote.result = &res
			// 结果端点也带「我已投」时，按投过票处理：直接展开，不必再点一次。
			if res.VotedKnown && res.Voted && !d.vote.voted {
				d.vote.voted = true
				d.vote.resultOpen = true
			}
		})
	}()
}

// toggleVoteResult 展开 / 收起结果分布。只有展开时才读端点，收起不读。
func (a *app) toggleVoteResult(d *detailState) {
	if d == nil || !a.detailLive(d) {
		return
	}
	d.vote.resultOpen = !d.vote.resultOpen
	// 每次展开都重读一次：票数可能已经变了（比如收起后投了票），点开看到的
	// 该是当下的分布，而不是上次那份过期的。
	if d.vote.resultOpen {
		a.loadVoteResult(d)
	}
}

// showVoteResult 在投票成功（或已投过）后把结果直接摆出来：投过票的人有权直接
// 看分布，不必再点一次「查看投票结果」。票数刚变过，已经读过的那份要重读。
func (a *app) showVoteResult(d *detailState) {
	if d == nil {
		return
	}
	d.vote.resultOpen = true
	a.loadVoteResult(d)
}

// submitVote 提交选中的选项。结果一律走主窗口 toast（见 toasts.go），页面里
// 只留「正在提交」的进度。成功后重新拉详情同步状态，并把结果分布直接展开：
// 投过票的人不用再点一次「查看投票结果」。
func (a *app) submitVote(d *detailState, optionIDs []string, multi bool) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.topic.Vote == nil {
		return
	}
	v := d.topic.Vote
	if d.vote.busy || d.vote.voted || v.Finished {
		return
	}
	if v.ID == "" || len(optionIDs) == 0 {
		a.toast(a.detailToastFor(d), "投票信息不完整，请刷新帖子后重试")
		return
	}
	// 选项 uuid 必须来自当前这一份详情：详情刷新后旧选项可能已经不在了。
	valid := make(map[string]bool, len(v.Options))
	for _, o := range v.Options {
		valid[o.ID] = true
	}
	for _, id := range optionIDs {
		if !valid[id] {
			a.toast(a.detailToastFor(d), "选项已变化，请重新选择")
			return
		}
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能参与投票。")
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
			if !a.detailLive(d) || d.vote.gen != gen {
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
					// 并拉一次详情同步状态。
					d.vote.voted = true
					a.toast(a.detailToastFor(d), "你已经投过票了")
					a.showVoteResult(d)
					a.loadTopic()
					a.notifyInteractDone(nil)
					return
				}
				if a.handleAuthError(err) {
					a.notifyInteractDone(err)
					return
				}
				a.toast(a.detailToastFor(d), friendlyError(err))
				a.notifyInteractDone(err)
				return
			}
			d.vote.voted = true
			d.vote.checks = make([]bool, len(d.vote.checks))
			a.toast(a.detailToastFor(d), "已投票")
			a.showVoteResult(d)
			a.loadTopic()
			a.notifyInteractDone(nil)
		})
	}()
}
