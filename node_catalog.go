package main

import (
	"context"
	"time"
)

// 共享节点目录：首页筛选、侧边栏快捷入口、节点配置弹窗与发帖窗口都读同一
// 份节点表。节点表变化很少，本地缓存（nodes.go 的 nodeSnapshot /
// storeNodeCache）已有一层；这里再加一层「单飞」：同一时刻最多一个请求，
// 失败后冷却一分钟，视图每帧调用也不会对着失败的服务端反复重试。

// nodeCatalog 是进程内某账号窗口的节点表状态，只在 UI 线程读写。
type nodeCatalog struct {
	nodes    []composeNode
	at       time.Time
	loaded   bool
	loading  bool
	err      string
	failedAt time.Time
	seq      uint64
}

// nodeCatalogNodes 返回当前可见的节点表。优先用目录自己的结果，目录还没
// 拉过时退回本地缓存（哪怕已过期），让第一帧就有东西可画。
func (a *app) nodeCatalogNodes() []composeNode {
	if len(a.nodeCat.nodes) > 0 {
		return a.nodeCat.nodes
	}
	nodes, _, ok := a.nodeSnapshot()
	if ok {
		return nodes
	}
	return nil
}

// ensureNodeCatalog 保证节点表在后台加载一次。缓存新鲜时同步返回；失败后
// 冷却期内直接返回，不重发注定失败的请求。
func (a *app) ensureNodeCatalog() {
	cat := &a.nodeCat
	if cat.loading {
		return
	}
	if len(cat.nodes) == 0 {
		if nodes, at, ok := a.nodeSnapshot(); ok {
			cat.nodes, cat.at, cat.loaded = nodes, at, true
		}
	}
	if len(cat.nodes) > 0 && nodeCacheFresh(cat.at) {
		return
	}
	if !cat.failedAt.IsZero() && time.Since(cat.failedAt) < nodeRetryCooldown {
		return
	}
	if !a.autoLoad && len(cat.nodes) == 0 {
		return
	}
	cat.loading = true
	cat.err = ""
	cat.seq++
	gen := cat.seq
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		nodes, err := a.client.loadNodeTree(ctx)
		a.publish(func() {
			if err == nil && len(nodes) > 0 {
				// 窗口关掉也要写缓存：下次打开就不用再请求。
				a.storeNodeCache(nodes, time.Now())
			}
			if cat.seq != gen {
				return
			}
			cat.loading = false
			if err != nil {
				cat.failedAt = time.Now()
				if len(cat.nodes) == 0 {
					cat.err = friendlyError(err)
				}
				a.syncComposeNodeState()
				a.notifyNodesLoaded(err)
				return
			}
			cat.nodes = nodes
			cat.at = a.nodes.at
			cat.loaded = true
			cat.err = ""
			cat.failedAt = time.Time{}
			a.syncComposeNodeState()
			a.notifyNodesLoaded(nil)
		})
	}()
}

// retryNodeCatalog 跳过失败冷却，立即重新拉一次节点表（「重试」按钮用）。
func (a *app) retryNodeCatalog() {
	a.nodeCat.failedAt = time.Time{}
	a.nodeCat.at = time.Time{}
	a.ensureNodeCatalog()
}
