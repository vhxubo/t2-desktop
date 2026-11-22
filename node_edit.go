package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// 修改帖子节点分区：11-20 新增的写端点，与「编辑标题 / 正文」是两个完全
// 独立的窗口，各自计时、各自成功后关闭。契约逐字照抄 api-write.md：
//
//	POST /v1/topics/:short_id/node 🔑  scope write:topic
//	body: { "node_id": "uuid" }       resp: { c:0, d:{ node:{id,slug,name} } }
//
// 只有作者本人（含匿名帖真实发布者）能改：入口只认详情下发的 can_edit_node，
// 不靠昵称比对，匿名帖自己发的也能看到入口。10 分钟窗口与唯一一次机会都由
// 服务端裁决；窗口过期 / 机会用掉回 400 edit_window_expired，此时隐藏入口，
// 不再重试（试错本身就会白白失败，且提示不该让用户反复点）。
//
// 同节点返回 200 但不消耗机会、失败不消耗机会；目标不存在回 404。客户端在
// 选中当前节点时不发请求，省掉一次注定无意义的往返。

const topicNodeScope = "write:topic"

func topicNodePath(shortID string) string {
	return topicPath(shortID) + "/node"
}

// nodeChange 是 d.node 里落地后的节点。响应可能多带字段，解码时全部忽略。
type nodeChange struct {
	ID, Slug, Name string
}

func decodeNodeChange(raw json.RawMessage) nodeChange {
	o := asObject(raw)
	if o == nil {
		return nodeChange{}
	}
	n := o.sub("node")
	if n == nil {
		n = o
	}
	return nodeChange{ID: n.str("id"), Slug: n.str("slug"), Name: n.str("name")}
}

// setTopicNode 提交一次改节点。端点标 🔑：同一目标的重试（授权后重跑、超时
// 重发）必须复用同一个幂等键，换目标才铸新键，见 nodeEditState.nodeKey。
func (c *Client) setTopicNode(ctx context.Context, shortID, nodeID, key string) (nodeChange, error) {
	var res nodeChange
	if strings.TrimSpace(nodeID) == "" {
		return res, fmt.Errorf("缺少目标节点")
	}
	payload := map[string]any{"node_id": nodeID}
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		d, err := c.postJSON(ctx, c.apiURL(topicNodePath(shortID)), token, key, payload)
		if err != nil {
			return struct{}{}, err
		}
		res = decodeNodeChange(d)
		return struct{}{}, nil
	})
	return res, err
}

// nodeEditState 是「修改节点」窗口的全部状态。只在 UI 线程读写，网络结果
// 经 publish 回来；不存 ui.Element / *ui.Context。
type nodeEditState struct {
	shortID string
	detail  *detailState
	// current 是帖子当前所属节点；target 是新选的节点。两者都是 nodeRef，
	// ID 是 node_id（uuid）， slug 只用于同节点判断与显示。
	current nodeRef
	target  nodeFilter
	// open / activeRoot / search 是节点级联选择器的持久状态。
	open       bool
	activeRoot string
	search     string
	busy       bool
	gen        uint64
	// key / keySig 是这一目标的幂等键与铸键时的目标 id。
	key, keySig string
}

func newNodeEditState(d *detailState) *nodeEditState {
	if d == nil {
		return nil
	}
	st := &nodeEditState{shortID: d.shortID, detail: d}
	if d.topic != nil {
		st.current = nodeRefFromTopic(d.topic)
		st.target = nodeFilterOf(st.current)
	}
	return st
}

// sameNode 报告选中的是不是当前节点。同节点服务端会回 200 且不消耗机会，
// 但没必要发这一趟请求；两边 id 都拿到时按 id 比，否则退回 slug。
func (st *nodeEditState) sameNode() bool {
	if st == nil || st.target.Kind != nodeFilterNode {
		return true
	}
	a, b := st.target.Ref, st.current
	if a.ID != "" && b.ID != "" {
		return a.ID == b.ID
	}
	if a.Slug != "" && b.Slug != "" {
		return strings.EqualFold(a.Slug, b.Slug)
	}
	return false
}

// nodeKey 返回这一目标的幂等键：目标没变就复用（授权重跑、超时重发不会改
// 两次），换目标就换键，同一个键配不同 node_id 会被服务端判冲突。
func (st *nodeEditState) nodeKey() string {
	if st == nil {
		return ""
	}
	sig := st.target.Ref.ID
	if sig == "" {
		sig = st.target.Ref.Slug
	}
	if st.key != "" && st.keySig == sig {
		return st.key
	}
	st.key = randomURLSafe(24)
	st.keySig = sig
	return st.key
}

// canEditNode 报告详情此刻是否给出「修改节点」入口。can_edit_node 是窗口
// 状态，服务端保证只有作者本人（含匿名帖真实发布者）才拿到 true；字段缺失
// 的老响应才退回本地作者判断，避免误把别人的帖子当自己的。
func (a *app) canEditNode(d *detailState) bool {
	if d == nil || d.topic == nil {
		return false
	}
	if d.topic.CanEditNodeKnown {
		return d.topic.CanEditNode
	}
	return a.isMine(d.topic.Author)
}

// redrawDetailFor 重画承载这篇帖子的界面：独立详情窗口和主窗口的内联详情
// 都要跟上入口 / 节点名的变化。
func (a *app) redrawDetailFor(d *detailState) {
	if h := a.detailHostOf(d); h != nil {
		h.invalidate()
	}
	if d != nil && a.detail == d && a.win != nil {
		a.win.Invalidate()
	}
}

func (a *app) nodeEditToast(text string) {
	a.toast(toastNodeEdit, text)
}

// submitNodeEdit 提交一次改节点。失败只弹 toast，页面保持原样；只有窗口
// 过期才把入口隐藏。幂等键在授权重跑时复用。
func (a *app) submitNodeEdit() {
	st := a.nodeEdit
	if st == nil || st.busy || !a.auth.loggedIn() {
		return
	}
	if st.target.Kind != nodeFilterNode || st.target.Ref.ID == "" {
		a.nodeEditToast("请选择目标节点")
		return
	}
	if st.sameNode() {
		// 服务端对同节点也是 200 且不消耗机会，这里连请求都不发。
		a.nodeEditToast("请选择其它节点")
		return
	}
	target, key := st.target.Ref, st.nodeKey()
	st.busy = true
	st.gen++
	gen := st.gen
	ctx, cancel := context.WithTimeout(context.Background(), interactTimeout)
	go func() {
		defer cancel()
		res, err := a.client.setTopicNode(ctx, st.shortID, target.ID, key)
		a.publish(func() {
			if st != a.nodeEdit || st.gen != gen {
				return // 窗口已经关掉，或又换了一次目标
			}
			st.busy = false
			if err != nil {
				// 窗口过期 / 机会用掉：隐藏入口，不重试（见文件头注释）。
				if isEditWindowExpired(err) {
					a.hideNodeEditEntry(st.detail)
					a.closeNodeEditWindow()
					a.nodeEditToast("改节点的机会已用完，入口已隐藏")
					return
				}
				a.handleAuthErrorFor(toastNodeEdit, err, func() {
					if a.nodeEdit == st {
						a.submitNodeEdit()
					}
				})
				a.nodeEditToast(friendlyError(err))
				return
			}
			a.applyNodeChange(st.detail, res, target)
			a.closeNodeEditWindow()
			a.nodeEditToast("节点已修改")
		})
	}()
}

// hideNodeEditEntry 把 can_edit_node 就地关掉。服务端已经回 400 说明窗口关闭，
// 下次详情刷新也会拿到 false；现在先改本地状态，让入口立刻消失。
func (a *app) hideNodeEditEntry(d *detailState) {
	if d == nil || d.topic == nil {
		return
	}
	d.topic.CanEditNode = false
	d.topic.CanEditNodeKnown = true
	a.redrawDetailFor(d)
}

// applyNodeChange 把服务端落地后的节点写回页面。响应里的 parent 没有给，
// 父节点沿用选择时那份目录信息；拿不到就只显示一级，不拿 slug 冒充父名。
func (a *app) applyNodeChange(d *detailState, res nodeChange, target nodeRef) {
	if d == nil || d.topic == nil {
		return
	}
	id, slug, name := res.ID, res.Slug, res.Name
	if id == "" {
		id = target.ID
	}
	if slug == "" {
		slug = target.Slug
	}
	if name == "" {
		name = target.Name
	}
	d.topic.NodeID, d.topic.NodeSlug, d.topic.NodeName = id, slug, name
	d.topic.NodeParentName, d.topic.NodeParentSlug = target.ParentName, target.ParentSlug
	d.topic.URL = topicURL(d.shortID, slug)
	d.topic.CanEditNode, d.topic.CanEditNodeKnown = false, true
	a.renameNodeRows(d.shortID, nodeRef{ID: id, Slug: slug, Name: name,
		ParentName: target.ParentName, ParentSlug: target.ParentSlug})
	a.saveCachedDetail(d)
	a.redrawDetailFor(d)
}

// renameNodeRows 更新内存里这篇帖子的节点引用：返回列表后点节点 Tag 应该筛到
// 新节点，不能还停在旧节点上。Subtitle 是展示文本，等列表刷新自然更新。
func (a *app) renameNodeRows(shortID string, ref nodeRef) {
	if shortID == "" || ref.ID == "" {
		return
	}
	for i := range a.tabs {
		items := a.tabs[i].items
		for j := range items {
			if items[j].ShortID == shortID {
				items[j].Node = ref
			}
		}
	}
}
