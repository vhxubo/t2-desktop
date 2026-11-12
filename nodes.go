package main

import (
	"context"
	"encoding/json"
	"log"
	"net/url"
	"sync"
	"time"
)

// 节点（板块）列表与本地缓存
//
//	GET /v1/nodes                 顶层节点；**线上响应里每个节点自带 children**
//	GET /v1/nodes?parent=<slug>   某个节点的子节点；**线上响应是 {items:[...]}**
//
// 文档说这两个端点都返回数组，线上并不一致：顶层数组里嵌着子节点，子节点
// 查询返回的是对象包一层 items。两种形状这里都认，父子关系以子对象的
// parent_slug 为准，缺省时用本次请求的父节点补上；父节点名与子节点名分开
// 带着，级联选择才能把父节点放在第一列、子节点放在第二列。
//
// 节点变化极少（文档建议本地缓存 10 分钟以上），所以缓存分两层：内存 +
// cache/offline.json 的 nodes:v1。首屏先把缓存摆上（哪怕已经过期），过期后
// 再在后台刷新——用户不会因为刷新而先看到一个空的级联选择。
//
// 一次顶层请求就够：线上顶层节点自带 children，不再按 parent 逐个问。
// 只有响应里真的没有 children 字段时（旧版 / 精简响应）才退回逐个问，
// 并发上限 nodeChildrenWorkers，某一支失败只丢那一支。

const (
	// nodeCacheKey 是本地缓存里这份节点表的键。
	nodeCacheKey = "nodes:v1"
	// nodeChildrenWorkers 是退回「按 parent 逐个问」时的并发上限：节点总量
	// 只有十几个，并发 4 足够快，也不会一下打出十几个请求。
	nodeChildrenWorkers = 4
	// nodeRetryCooldown 是刷新失败后的冷却时间：视图每帧都会调用加载，
	// 没有冷却就会对着一个失败的服务端反复请求。
	nodeRetryCooldown = time.Minute
)

// nodeCacheTTL 是节点缓存的新鲜期。文档建议 10 分钟以上，这里取 15 分钟：
// 节点很少变，15 分钟既省请求，也不至于让新节点太久看不到。提成变量是为了
// 测试能把它缩短。
var nodeCacheTTL = 15 * time.Minute

// nodeCache 是内存里的一份节点快照。tried 表示已经在内存或磁盘上找过一次，
// 之后即使没找到也不再读盘（节点表只写不删，读一次够）。
type nodeCache struct {
	nodes []composeNode
	at    time.Time
	tried bool
}

// nodeCacheFile 是落盘的结构。SavedAt 单独存一份，不依赖 KV 条目的写入时间：
// 两者一起写，但读的时候以文件里的为准，语义更明确。
type nodeCacheFile struct {
	Nodes   []composeNode `json:"nodes"`
	SavedAt time.Time     `json:"saved_at"`
}

// nodeRow 是顶层节点的一条：节点本身 + 它自带的 children。
type nodeRow struct {
	node        composeNode
	children    []composeNode
	hasChildren bool // 响应里带了这个键（哪怕是空数组），就不用再按 parent 问
}

/* ------------------------------------------------------------------
   请求与解码
-------------------------------------------------------------- */

// nodeEndpoint 是节点列表地址；parent 非空时取该节点的子节点。
func nodeEndpoint(c *Client, parent string) string {
	u := c.apiURL("/v1/nodes")
	if parent != "" {
		u += "?parent=" + url.QueryEscape(parent)
	}
	return u
}

// nodeItems 去掉响应外层：顶层是数组，子节点查询是 {items:[...]}。文档与
// 线上不一致，这个函数是唯一的分歧点，解码器本身不用关心。
func nodeItems(d json.RawMessage) []json.RawMessage {
	if items := rawArray(d); len(items) > 0 {
		return items
	}
	o := asObject(d)
	if o == nil {
		return nil
	}
	for _, key := range []string{"items", "nodes", "list"} {
		if items := rawArray(o[key]); len(items) > 0 {
			return items
		}
	}
	return nil
}

// decodeNode 解码一个节点。parent 非空时把父节点的名字与 slug 补上，子节点
// 响应里缺 parent_slug 的情况也能归位。
func decodeNode(o object, parent composeNode) composeNode {
	n := composeNode{
		ID:         o.str("id"),
		Slug:       o.str("slug"),
		Name:       o.str("name"),
		ParentSlug: o.str("parent_slug"),
		PostCount:  o.num("post_count"),
	}
	if parent.Name != "" || parent.Slug != "" {
		if n.ParentSlug == "" {
			n.ParentSlug = parent.Slug
		}
		n.ParentName = parent.Name
	}
	return n
}

// fetchNodeRows 读一遍节点列表：顶层带 children，或者某个父节点的子节点。
func (c *Client) fetchNodeRows(ctx context.Context, parent string) ([]nodeRow, error) {
	raws, err := withAuthRetry(c, ctx, func(token string) ([]json.RawMessage, error) {
		d, err := c.getJSON(ctx, nodeEndpoint(c, parent), token)
		if err != nil {
			return nil, err
		}
		return nodeItems(d), nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]nodeRow, 0, len(raws))
	for _, raw := range raws {
		o := asObject(raw)
		if o == nil {
			continue
		}
		n := decodeNode(o, composeNode{})
		if n.ID == "" || n.Name == "" {
			continue
		}
		row := nodeRow{node: n, hasChildren: o.has("children")}
		for _, childRaw := range rawArray(o["children"]) {
			co := asObject(childRaw)
			if co == nil {
				continue
			}
			ch := decodeNode(co, n)
			if ch.ID == "" || ch.Name == "" {
				continue
			}
			row.children = append(row.children, ch)
		}
		out = append(out, row)
	}
	return out, nil
}

// fetchNodeChildren 是旧响应形状的退路：按 parent 逐个取子节点，最多 4 个
// 并发。某一支失败只丢那一支，不影响其它节点。
func (c *Client) fetchNodeChildren(ctx context.Context, parents []composeNode) [][]composeNode {
	out := make([][]composeNode, len(parents))
	if len(parents) == 0 {
		return out
	}
	sem := make(chan struct{}, nodeChildrenWorkers)
	var wg sync.WaitGroup
	for i := range parents {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows, err := c.fetchNodeRows(ctx, parents[i].Slug)
			if err != nil {
				return
			}
			branch := make([]composeNode, 0, len(rows))
			parent := parents[i]
			for _, row := range rows {
				ch := row.node
				if ch.ParentSlug == "" {
					ch.ParentSlug = parent.Slug
				}
				ch.ParentName = parent.Name
				branch = append(branch, ch)
			}
			out[i] = branch
		}()
	}
	wg.Wait()
	return out
}

// loadNodeTree 拉一份完整的节点表：每个父节点后面跟着它的子节点。顶层响应
// 自带 children 时只发一次请求；否则按 parent 逐个补齐。
func (c *Client) loadNodeTree(ctx context.Context) ([]composeNode, error) {
	rows, err := c.fetchNodeRows(ctx, "")
	if err != nil {
		return nil, err
	}
	var missing []composeNode
	for _, row := range rows {
		// 响应里没有 children 键：这个父节点的子节点还得单独问一次。
		if !row.hasChildren && row.node.Slug != "" {
			missing = append(missing, row.node)
		}
	}
	childrenByParent := make(map[string][]composeNode, len(missing))
	if len(missing) > 0 {
		for i, branch := range c.fetchNodeChildren(ctx, missing) {
			if len(branch) > 0 {
				childrenByParent[missing[i].ID] = branch
			}
		}
	}
	out := make([]composeNode, 0, len(rows)+64)
	seen := make(map[string]bool, len(rows)+64)
	add := func(n composeNode) {
		if n.ID == "" || seen[n.ID] {
			return
		}
		seen[n.ID] = true
		out = append(out, n)
	}
	for _, row := range rows {
		add(row.node)
		for _, ch := range row.children {
			add(ch)
		}
		for _, ch := range childrenByParent[row.node.ID] {
			add(ch)
		}
	}
	disambiguateNodeLabels(out)
	return out, nil
}

// disambiguateNodeLabels 给显示名撞车的节点补上 slug：站点里同一个父节点下
// 确实有同名节点（AI 下的两个「中转站」），级联选择的第二列只显示节点名，
// 不补 slug 两行就一模一样；触发框也会显示成同一个名字，让用户分不清自己
// 选的是哪一个。
func disambiguateNodeLabels(nodes []composeNode) {
	counts := make(map[string]int, len(nodes))
	for i := range nodes {
		counts[nodes[i].parentDisplay()]++
	}
	for i := range nodes {
		n := &nodes[i]
		if counts[n.parentDisplay()] > 1 {
			n.Label = n.parentDisplay() + " (" + n.Slug + ")"
		}
	}
}

/* ------------------------------------------------------------------
   本地缓存
-------------------------------------------------------------- */

// nodeSnapshot 返回内存或磁盘里的节点表。第一次调用会读一次盘；之后只认
// 内存。返回的时间戳用于判断新鲜度。
func (a *app) nodeSnapshot() ([]composeNode, time.Time, bool) {
	if a.nodes.tried {
		if len(a.nodes.nodes) == 0 {
			return nil, time.Time{}, false
		}
		return a.nodes.nodes, a.nodes.at, true
	}
	a.nodes.tried = true
	if a.kv == nil {
		return nil, time.Time{}, false
	}
	var f nodeCacheFile
	at, ok := a.kv.get(nodeCacheKey, &f)
	if !ok || len(f.Nodes) == 0 {
		return nil, time.Time{}, false
	}
	if !f.SavedAt.IsZero() {
		at = f.SavedAt
	}
	a.nodes.nodes = f.Nodes
	a.nodes.at = at
	return a.nodes.nodes, at, true
}

// storeNodeCache 把新拉到的节点表写进内存与 cache/offline.json。窗口已经关掉
// 也要写：下次打开（或重启）就不用再请求。
func (a *app) storeNodeCache(nodes []composeNode, at time.Time) {
	if len(nodes) == 0 {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	a.nodes.nodes = nodes
	a.nodes.at = at
	a.nodes.tried = true
	if a.kv == nil {
		return
	}
	if err := a.kv.put(nodeCacheKey, nodeCacheFile{Nodes: nodes, SavedAt: at}, at); err != nil {
		log.Printf("节点缓存写入失败：%v", err)
	}
}

// nodeCacheFresh 报告缓存是否还在新鲜期内：过期的缓存照样先显示，只是会
// 在后台再刷新一次。
func nodeCacheFresh(at time.Time) bool {
	return !at.IsZero() && time.Since(at) < nodeCacheTTL
}
