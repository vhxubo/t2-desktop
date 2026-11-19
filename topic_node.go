package main

import (
	"context"
	"errors"
	"strings"
	"time"
)

// 不选节点的 AI 自动分区（api-write.md 的「发帖」一节）。
//
// 不传 node_id 时帖子先落在「星云」节点（slug: nebula），服务端随后用 AI
// 按标题与正文选节点并异步挪过去。识别完成前帖子不进任何列表，只能凭
// short_id 读详情；文档要求客户端轮询详情，直到 node.slug 不再是 nebula。
//
// 两处入口共用同一套退避：发帖成功的回执（composeState）与详情页
// （detailState）。错误不弹授权框、不打断阅读：这只是后台补全一个展示字段，
// 用户真正需要的正文和评论已经在了。

const (
	// nebulaNodeSlug 是 AI 自动分区的临时节点；用 slug 判断，不按名字猜。
	nebulaNodeSlug = "nebula"
	// nodeWatchFirstDelay 是第一次轮询的等待时间；通常几秒内就能分好。
	nodeWatchFirstDelay = 2 * time.Second
	// nodeWatchMaxDelay 是退避上限，避免用户一直等却看不到最后一次尝试。
	nodeWatchMaxDelay = 10 * time.Second
	// nodeWatchMaxTries 限制总请求数：30 秒左右还没分好就停下，用户手动
	// 刷新详情会重新开始。
	nodeWatchMaxTries = 6
	// nodeWatchTimeout 是单次详情请求的超时；比正文加载短，失败就等下一轮。
	nodeWatchTimeout = 15 * time.Second
)

// pendingNode 报告这篇帖子的节点是否还在等 AI 分配。slug 缺失时不猜：
// 没有节点信息的帖子按「未知」处理，不进入轮询。
func (t *topicDetail) pendingNode() bool {
	return t != nil && strings.EqualFold(strings.TrimSpace(t.NodeSlug), nebulaNodeSlug)
}

// nodeWatchFatal 报告这次失败重试也没用：令牌 / 权限 / 帖子不可见都不是
// 等几秒就能变的，继续轮询只会多打几次注定失败的请求。
func nodeWatchFatal(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.Status {
	case 401, 403, 404:
		return true
	}
	return false
}

// nodeWatchBackoff 按已尝试次数退避：2s、4s、6s……最多 10s。
func nodeWatchBackoff(tries int) time.Duration {
	if tries < 1 {
		tries = 1
	}
	d := time.Duration(tries) * nodeWatchFirstDelay
	if d > nodeWatchMaxDelay {
		return nodeWatchMaxDelay
	}
	return d
}

/* ------------------------------------------------------------------
   详情页
-------------------------------------------------------------- */

// stopDetailNodeWatch 取消详情页的轮询。离开详情、刷新后节点已确定、或者
// 开始新的详情时调用；gen 自增让已经发出的请求结果作废。
func (a *app) stopDetailNodeWatch(d *detailState) {
	if d == nil {
		return
	}
	d.nodeWatchGen++
	if d.nodeWatchTimer != nil {
		d.nodeWatchTimer.Stop()
		d.nodeWatchTimer = nil
	}
	d.nodeWatchTries = 0
}

// watchPendingNode 在详情加载成功后启动轮询：只有节点还在星云、且当前没有
// 排着的定时器时才启动，手动刷新不会叠加第二条链路。
func (a *app) watchPendingNode(d *detailState) {
	if d == nil || d.topic == nil || !a.autoLoad || !d.topic.pendingNode() {
		return
	}
	if d.nodeWatchTimer != nil {
		return
	}
	d.nodeWatchGen++
	gen := d.nodeWatchGen
	d.nodeWatchTries = 0
	a.scheduleDetailNodeWatch(d, gen, nodeWatchFirstDelay)
}

func (a *app) scheduleDetailNodeWatch(d *detailState, gen uint64, delay time.Duration) {
	d.nodeWatchTimer = time.AfterFunc(delay, func() {
		a.publish(func() {
			if !a.detailLive(d) || d.nodeWatchGen != gen {
				return
			}
			d.nodeWatchTimer = nil
			a.probeDetailNode(d, gen)
		})
	})
}

// probeDetailNode 读一次详情，只取节点字段。还在星云就按退避再排一次；
// 分好了就把节点、URL 与缓存就地补上，并提示一句结果。
func (a *app) probeDetailNode(d *detailState, gen uint64) {
	if d == nil || d.nodeWatchTries >= nodeWatchMaxTries {
		return
	}
	d.nodeWatchTries++
	tries := d.nodeWatchTries
	shortID := d.shortID
	ctx, cancel := context.WithTimeout(context.Background(), nodeWatchTimeout)
	go func() {
		defer cancel()
		topic, err := a.client.fetchTopic(ctx, shortID)
		a.publish(func() {
			if !a.detailLive(d) || d.nodeWatchGen != gen {
				return
			}
			if err != nil {
				// 网络抖动或服务端短暂失败：用完次数之前继续等，不把错误
				// 画到已经读到的正文上；鉴权 / 不可见则不再重试。
				if tries < nodeWatchMaxTries && !nodeWatchFatal(err) {
					a.scheduleDetailNodeWatch(d, gen, nodeWatchBackoff(tries))
				}
				return
			}
			// 还在星云、或者响应里根本没有节点字段，都继续等：空节点不能
			// 当成「已分配」，那只是这次响应没带。
			if topic.pendingNode() || topic.NodeSlug == "" {
				if tries < nodeWatchMaxTries {
					a.scheduleDetailNodeWatch(d, gen, nodeWatchBackoff(tries))
				}
				return
			}
			a.applyDetailNode(d, &topic)
		})
	}()
}

// applyDetailNode 把轮询到的正式节点补到当前详情上。只改节点相关字段，
// 不覆盖用户正在读的正文、评论与互动状态。
func (a *app) applyDetailNode(d *detailState, topic *topicDetail) {
	if d == nil || d.topic == nil || topic == nil || topic.pendingNode() || topic.NodeSlug == "" {
		return
	}
	t := d.topic
	t.NodeID = topic.NodeID
	t.NodeName = topic.NodeName
	t.NodeSlug = topic.NodeSlug
	t.NodeParentName = topic.NodeParentName
	t.NodeParentSlug = topic.NodeParentSlug
	t.NodeEmojis = topic.NodeEmojis
	if topic.URL != "" {
		t.URL = topic.URL
	} else if t.NodeSlug != "" {
		t.URL = topicURL(t.ShortID, t.NodeSlug)
	}
	a.updateReadingTopic(t.ShortID, t)
	a.saveCachedDetail(d)
	a.toast(a.detailToastFor(d), nodeAssignedText(t.NodeName, t.NodeSlug))
}

// nodeAssignedText 是节点分配完成的一句提示；名字缺失时退回 slug，都没有
// 就只说分配完成，不编一个空冒号。
func nodeAssignedText(name, slug string) string {
	if label := strings.TrimSpace(name); label != "" {
		return "节点已分配：" + label
	}
	if label := strings.TrimSpace(slug); label != "" {
		return "节点已分配：" + label
	}
	return "节点已分配"
}

/* ------------------------------------------------------------------
   发帖回执
-------------------------------------------------------------- */

// stopComposeNodeWatch 取消发帖回执上的轮询。重新发布 / 切到动态 / 窗口
// 状态被替换时调用。
func (a *app) stopComposeNodeWatch(st *composeState) {
	if st == nil {
		return
	}
	st.nodeWatchGen++
	if st.nodeWatchTimer != nil {
		st.nodeWatchTimer.Stop()
		st.nodeWatchTimer = nil
	}
	st.nodeWatchTries = 0
	st.nodeWatchShortID = ""
}

// watchComposeNode 在发帖成功且节点还是星云时启动轮询：分好后更新回执里的
// 原文链接，让「看原文 / 在应用内打开」拿到正式地址。
func (a *app) watchComposeNode(st *composeState, shortID string) {
	if st == nil || shortID == "" || !a.autoLoad {
		return
	}
	st.nodeWatchGen++
	gen := st.nodeWatchGen
	st.nodeWatchShortID = shortID
	st.nodeWatchTries = 0
	a.scheduleComposeNodeWatch(st, gen, nodeWatchFirstDelay)
}

func (a *app) scheduleComposeNodeWatch(st *composeState, gen uint64, delay time.Duration) {
	st.nodeWatchTimer = time.AfterFunc(delay, func() {
		a.publish(func() {
			if a.compose != st || st.nodeWatchGen != gen {
				return
			}
			st.nodeWatchTimer = nil
			a.probeComposeNode(st, gen)
		})
	})
}

func (a *app) probeComposeNode(st *composeState, gen uint64) {
	if st == nil || st.nodeWatchTries >= nodeWatchMaxTries {
		return
	}
	st.nodeWatchTries++
	tries := st.nodeWatchTries
	shortID := st.nodeWatchShortID
	ctx, cancel := context.WithTimeout(context.Background(), nodeWatchTimeout)
	go func() {
		defer cancel()
		topic, err := a.client.fetchTopic(ctx, shortID)
		a.publish(func() {
			if a.compose != st || st.nodeWatchGen != gen {
				return
			}
			if err != nil {
				if tries < nodeWatchMaxTries && !nodeWatchFatal(err) {
					a.scheduleComposeNodeWatch(st, gen, nodeWatchBackoff(tries))
				}
				return
			}
			if topic.pendingNode() || topic.NodeSlug == "" {
				if tries < nodeWatchMaxTries {
					a.scheduleComposeNodeWatch(st, gen, nodeWatchBackoff(tries))
				}
				return
			}
			a.applyComposeNode(st, &topic)
		})
	}()
}

// applyComposeNode 把正式节点写回发帖回执。窗口已经关掉也照样更新状态：
// 下次打开发帖窗口时回执里的链接就是对的；只有窗口还开着才弹提示。
func (a *app) applyComposeNode(st *composeState, topic *topicDetail) {
	if st == nil || topic == nil || topic.pendingNode() || topic.NodeSlug == "" ||
		!st.okPending || st.okShortID != topic.ShortID {
		return
	}
	st.okPending = false
	switch {
	case topic.URL != "":
		st.okURL = topic.URL
	case topic.ShortID != "":
		st.okURL = topicURL(topic.ShortID, topic.NodeSlug)
	}
	a.redrawComposeWindow()
	if a.composeWin != nil {
		a.toast(toastCompose, nodeAssignedText(topic.NodeName, topic.NodeSlug))
	}
}
