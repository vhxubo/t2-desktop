package main

import "strings"

// 节点选择器的本机搜索：只在本进程已有的节点表里匹配，不发新请求。匹配
// 字段包括子节点名、父节点名、子节点 slug、父节点 slug 与「父 · 子」拼接，
// 大小写不敏感，中文按子串匹配。

// nodeHit 是一条搜索结果。父分类与子节点共用这个形状，由 IsParent 区分；
// 发帖窗口只取子节点，父分类的名字仍作为 Description 留在子节点行里。
type nodeHit struct {
	Ref       nodeRef
	IsParent  bool
	PostCount int
}

// nodeSearchLimit 是结果上限：命中太多时只显示前 50 条，组件提示继续输入。
const nodeSearchLimit = 50

// nodeSearch 返回有序命中：父分类排在最前，子节点保持节点表原来的顺序。
// 查询为空或纯空白时返回 nil，调用方据此画回两列级联。
func nodeSearch(nodes []composeNode, query string) []nodeHit {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	parents := make([]composeNode, 0, len(nodes))
	children := make([]composeNode, 0, len(nodes))
	for _, n := range nodes {
		if n.ParentSlug == "" {
			parents = append(parents, n)
		} else {
			children = append(children, n)
		}
	}
	out := make([]nodeHit, 0, nodeSearchLimit)
	for _, n := range parents {
		if !nodeMatches(n, q) {
			continue
		}
		if len(out) >= nodeSearchLimit {
			return out
		}
		out = append(out, nodeHit{Ref: parentRefFromNode(n), IsParent: true, PostCount: n.PostCount})
	}
	for _, n := range children {
		if !nodeMatches(n, q) {
			continue
		}
		if len(out) >= nodeSearchLimit {
			return out
		}
		out = append(out, nodeHit{Ref: nodeRefFromNode(n), PostCount: n.PostCount})
	}
	return out
}

// nodeMatches 逐项做大小写不敏感的子串匹配。父节点名与 slug 已经解码进
// composeNode，不需要再回查节点表。
func nodeMatches(n composeNode, q string) bool {
	fields := []string{n.Name, n.Slug, n.ParentName, n.ParentSlug, n.parentDisplay(), n.nameDisplay()}
	for _, f := range fields {
		if f != "" && strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	return false
}

// searchHitsForPicker 按选择器的用途整理搜索结果：发帖窗口不返回可选的
// 父分类（父名只作为子节点的分组说明留在 Ref 里）。
func searchHitsForPicker(nodes []composeNode, query string, selectableParents bool) []nodeHit {
	hits := nodeSearch(nodes, query)
	if selectableParents {
		return hits
	}
	out := make([]nodeHit, 0, len(hits))
	for _, h := range hits {
		if !h.IsParent {
			out = append(out, h)
		}
	}
	return out
}
