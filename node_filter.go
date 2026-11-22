package main

import (
	"net/url"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 节点引用与首页筛选。
//
// nodeRef 是一处「可以筛到首页的节点」引用，可能来自帖子行、评论行、详情
// 页或侧边栏快捷入口。接口各处给的信息不一样：列表带 uuid 与 slug，评论
// 往往只有 slug，详情两样都有。筛选时优先用 node_id，缺 id 再按 slug 从共享
// 节点目录里补齐；实在补不到就不瞎筛，给一句提示并触发一次加载。
//
// 节点筛选只存在于首页（tabSpecs[0]），社区讨论虽然同样打 /v1/topics，但
// 固定筛到社区节点，不吃这里的条件。

// nodeRef 是一个可以筛到首页的节点引用。ID 是 node_id（uuid），
// Slug 是节点自身 slug，ParentSlug 是父节点 slug。
type nodeRef struct {
	ID         string `json:"id,omitempty"`
	Slug       string `json:"slug,omitempty"`
	ParentSlug string `json:"parent_slug,omitempty"`
	Name       string `json:"name,omitempty"`
	ParentName string `json:"parent_name,omitempty"`
	// Sort 是这个节点自己的排序偏好（value / latest），空串等同价值优先。
	// 只有侧边栏快捷入口会写它；从接口里读到的节点引用不带排序。
	Sort string `json:"sort,omitempty"`
}

// key 是快捷入口去重用的稳定标识：优先 uuid，没有时退回 slug。
func (r nodeRef) key() string {
	if r.ID != "" {
		return "id:" + r.ID
	}
	if r.Slug != "" {
		return "slug:" + strings.ToLower(r.Slug)
	}
	return ""
}

// equal 比较两个引用是否指向同一筛选目标。id 与 slug 只在同类标识之间比较：
// 一个只有 slug 的引用与另一个只有 id 的引用可能指向同一节点，但那一刻无法
// 确认，交给 resolve 之后再比。
func (r nodeRef) equal(o nodeRef) bool {
	if r.ID != "" && o.ID != "" {
		return r.ID == o.ID
	}
	if r.Slug != "" && o.Slug != "" {
		return strings.EqualFold(r.Slug, o.Slug)
	}
	return false
}

// label 是筛选 chip / 快捷入口上的文字：子节点显示「父 · 子」，父分类
// 显示分类名；父名缺失时只显示子名，两者都没有时退回 slug。
func (r nodeRef) label() string {
	if r.Name == "" {
		r.Name = r.Slug
	}
	if r.ParentName != "" && r.ParentName != r.Name {
		return r.ParentName + " · " + r.Name
	}
	return r.Name
}

type nodeFilterKind uint8

const (
	nodeFilterAll nodeFilterKind = iota
	nodeFilterParent
	nodeFilterNode
)

type nodeFilter struct {
	Kind nodeFilterKind
	Ref  nodeRef
}

// allNodes 是首页默认筛选：接口参数与加筛选之前逐字节一致。
func allNodes() nodeFilter { return nodeFilter{Kind: nodeFilterAll} }

func parentFilter(r nodeRef) nodeFilter { return nodeFilter{Kind: nodeFilterParent, Ref: r} }

func nodeFilterOf(r nodeRef) nodeFilter { return nodeFilter{Kind: nodeFilterNode, Ref: r} }

// isAll 报告是不是默认的全部分类。
func (f nodeFilter) isAll() bool { return f.Kind == nodeFilterAll }

// sameAs 供侧边栏高亮用：两个筛选指向同一个节点或父分类才算命中。
func (f nodeFilter) sameAs(o nodeFilter) bool {
	if f.Kind != o.Kind {
		return false
	}
	if f.Kind == nodeFilterAll {
		return true
	}
	return f.Ref.equal(o.Ref)
}

// nodeFilterLabel 是筛选 chip 上的文字。
func nodeFilterLabel(f nodeFilter) string {
	switch f.Kind {
	case nodeFilterParent:
		if f.Ref.ParentSlug != "" && f.Ref.Name == "" {
			f.Ref.Name = f.Ref.ParentSlug
		}
		return f.Ref.label()
	case nodeFilterNode:
		return f.Ref.label()
	default:
		return "全部节点"
	}
}

// homeQuery 生成首页第 1 页的 query。参数顺序与原来的默认请求一致：
// page、limit、节点条件、sort；父分类与子节点条件互斥。排序取自筛选里的
// 节点引用：每个快捷入口各自决定价值优先还是最新，默认仍是价值优先。
func homeQuery(f nodeFilter) string {
	q := "page=1&limit=50&"
	switch f.Kind {
	case nodeFilterParent:
		q += "parent_slug=" + url.QueryEscape(f.Ref.Slug)
	case nodeFilterNode:
		q += "node_id=" + url.QueryEscape(f.Ref.ID)
	default:
		q += "parent_slug=All"
	}
	return q + "&sort=" + url.QueryEscape(validNodeSort(f.Ref.Sort))
}

// specFor 是列表 spec 的唯一入口：带节点筛选的首页与带搜索条件的通知都
// 在这里生成。鉴权、刷新、分页都读它，不会出现「列表用了筛选、鉴权判断
// 没用」的错位。第 2 页仍由 endpoint 只替换 page，筛选留在 Query 里。
func (a *app) specFor(idx int) tabSpec {
	if idx < 0 || idx >= len(tabSpecs) {
		return tabSpec{}
	}
	spec := tabSpecs[idx]
	if spec.FilterByNode && idx < len(a.tabs) && a.tabs[idx] != nil {
		spec.Query = homeQuery(a.tabs[idx].homeFilterNode)
	}
	if spec.Kind == kindNotification && idx < len(a.tabs) && a.tabs[idx] != nil {
		st := a.tabs[idx]
		spec.Query = notificationsQuery(st.filter, st.keyword)
	}
	return spec
}

// nodeRefFromNode 把共享节点表里的一条转成节点引用。
func nodeRefFromNode(n composeNode) nodeRef {
	return nodeRef{
		ID:         n.ID,
		Slug:       n.Slug,
		ParentSlug: n.ParentSlug,
		Name:       n.Name,
		ParentName: n.ParentName,
	}
}

// parentRefFromNode 把顶层节点转成父分类引用。顶层节点的 name 就是分类名，
// parent_slug 字段为空，筛选要用它自己的 slug。
func parentRefFromNode(n composeNode) nodeRef {
	return nodeRef{Slug: n.Slug, Name: n.Name}
}

// nodeRefFromComment 与 nodeRefFromTopic 分别把评论 / 详情里的节点信息归一。
func nodeRefFromComment(c commentItem) nodeRef {
	return nodeRef{
		ID:         c.NodeID,
		Slug:       c.NodeSlug,
		ParentSlug: c.ParentNodeSlug,
		Name:       c.NodeName,
		ParentName: c.ParentNodeName,
	}
}

func nodeRefFromTopic(p *topicDetail) nodeRef {
	if p == nil {
		return nodeRef{}
	}
	return nodeRef{
		ID:         p.NodeID,
		Slug:       p.NodeSlug,
		ParentSlug: p.NodeParentSlug,
		Name:       p.NodeName,
		ParentName: p.NodeParentName,
	}
}

// resolveNodeRef 按 slug 从节点目录里补 id、父 slug 与名字。同名节点不瞎选：
// 只要按 slug 找不到唯一一项，就保留原引用并让调用方提示用户去筛选里选。
func (a *app) resolveNodeRef(ref nodeRef) nodeRef {
	if ref.ID != "" && ref.Name != "" {
		return ref
	}
	nodes := a.nodeCatalogNodes()
	var found *composeNode
	if ref.Slug != "" {
		for i := range nodes {
			if strings.EqualFold(nodes[i].Slug, ref.Slug) {
				if found != nil && found.ID != nodes[i].ID {
					return ref // 同名 slug 冲突，宁可不筛
				}
				found = &nodes[i]
			}
		}
	}
	if ref.ID != "" {
		for i := range nodes {
			if nodes[i].ID == ref.ID {
				if found != nil && found.Slug != ref.Slug {
					return ref
				}
				found = &nodes[i]
				break
			}
		}
	}
	if found == nil {
		return ref
	}
	n := *found
	if ref.Name != "" {
		n.Name = ref.Name
	}
	if ref.ParentName != "" {
		n.ParentName = ref.ParentName
	}
	// 排序是节点引用自己的配置，补 id / 父名时不能丢。
	resolved := nodeRefFromNode(n)
	resolved.Sort = ref.Sort
	return resolved
}

// setHomeFilter 把筛选应用到首页：清掉旧列表与分页状态、回顶部、立刻拉第 1 页。
func (a *app) setHomeFilter(f nodeFilter) {
	if a.tab < 0 || a.tab >= len(a.tabs) {
		return
	}
	st := a.tabs[a.tab]
	if st == nil {
		return
	}
	st.homeFilterNode = f
	st.authWait = false
	st.loaded = false
	st.items = nil
	st.page = 1
	st.total = 0
	st.totalPages = 0
	st.noMore = false
	st.err = ""
	st.moreErr = ""
	st.fold.reset()
	st.list.ScrollTo(0, ui.Start)
	a.load(a.tab, 1)
}

// showNodeFeed 是所有「筛到这个节点」入口的统一动作：帖子行的 Tag、详情页、
// 评论行、用户资料卡与侧边栏快捷入口都走这里。从详情等窗口触发时先把主
// 窗口拉到前面，避免筛完了看不到结果。
func (a *app) showNodeFeed(ref nodeRef) {
	if ref.ID == "" && ref.Slug == "" {
		return
	}
	resolved := a.resolveNodeRef(ref)
	if resolved.ID == "" && ref.ParentSlug != "" {
		// 列表 / 评论只给了子节点的 slug，节点表里又没查到：按 parent_slug
		// 去筛会变成另一个分类，宁可什么也不筛，先触发一次加载并提示。
		a.ensureNodeCatalog()
		a.toast(toastMain, "节点还没加载好，请稍后再试")
		return
	}
	f := nodeFilterOf(resolved)
	if resolved.ID == "" {
		// 父分类没有 uuid，按 slug 走 parent_slug。
		f = parentFilter(resolved)
	}
	a.pageName = "list"
	a.tab = 0
	a.navSel = tabID(0)
	st := a.tabs[0]
	if st == nil {
		return
	}
	st.filterOpen = false
	st.filterSearch = ""
	a.setHomeFilter(f)
	if a.win != nil {
		a.raiseMainWindow()
	}
}
