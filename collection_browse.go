package main

import (
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"strconv"

	"github.com/egoist/mygo/ui"
)

// 合集内容浏览（api-read.md「合集」）
//
//	GET /v1/users/:userName/collections    某用户的公开合集（匿名可读）
//	GET /v1/collections/:collectionId/topics  合集内的帖子
//
// 合集列表本身留在原来的窗口里（「我的 - 我的内容 - 合集」或用户资料卡的
// 「他的合集」分段），点开一个合集时就地换成它的帖子，右上角给一个「返回」。
// 两个窗口共用这一份状态与画法，各自把浏览状态挂在自己的 state 上
// （profileState.collBrowse / userCardState.collBrowse），互不影响。

// collectionBrowse 是一个正在浏览的合集。list 就是普通的一页列表状态：
// 加载走 loadSpec，与侧边栏列表共用分页、去重与错误处理。
type collectionBrowse struct {
	id     string
	name   string
	owner  string // 合集所属用户名；空表示就是当前账号自己
	target toastTarget
	list   tabState
}

// newCollectionBrowse 由合集列表里的一项造出浏览状态。owner 是列表的归属者：
// 从「我的」进来时为空（就是自己），从资料卡进来时是那个人的用户名。target
// 是这份列表所在的窗口：缺权限时授权弹窗要回到那里。
func newCollectionBrowse(item collectionItem, owner string, target toastTarget) *collectionBrowse {
	return &collectionBrowse{id: item.ID, name: item.Name, owner: owner, target: target}
}

// collectionTopicsSpec 是合集内帖子列表的端点。api-read.md 的「合集」一节
// 只说明它是帖子列表，没有约定列表字段名，也没有写分页参数，所以列表用 auto
// （按 posts / items 等已知字段名容错），分页按通用约定传 page / limit 50。
func collectionTopicsSpec(id string) tabSpec {
	return tabSpec{
		Label:   "合集内容",
		Path:    collectionTopicsPath(id),
		Query:   "page=1&limit=" + strconv.Itoa(collectionTopicsLimit),
		ListKey: "auto",
		Paging:  pagingPages,
		Kind:    kindTopic,
	}
}

// loadCollectionTopics 拉合集里的一页帖子。page==1 时重建列表，之后续拉；
// 结果回填前由 loadSpec 用 gen 挡掉过期的请求，所以点「返回」之后迟到的
// 结果不会又把它画出来。
func (a *app) loadCollectionTopics(b *collectionBrowse, page int) {
	if b == nil || b.id == "" || page < 1 {
		return
	}
	a.loadSpec(b.target, &b.list, collectionTopicsSpec(b.id), page, func() {
		a.loadCollectionTopics(b, page)
	})
}

// reloadCollectionTopics 丢掉已拉到的内容，从第一页重拉（浏览视图上的「刷新」）。
func (a *app) reloadCollectionTopics(b *collectionBrowse) {
	if b == nil || b.list.loading {
		return
	}
	b.list.loaded = false
	b.list.items = nil
	b.list.err = ""
	b.list.moreErr = ""
	b.list.noMore = false
	b.list.authWait = false
	b.list.list.ScrollTo(0, ui.Start)
	a.loadCollectionTopics(b, 1)
}

// collectionTopicsTotal 是「共 N 篇」的数字：优先用服务端给的总数，没给时
// 退回已经拉到的条数，不编一个 0。
func collectionTopicsTotal(st *tabState) int {
	if st.total > 0 {
		return st.total
	}
	return len(st.items)
}

// collectionTopicsBody 画一个合集里的帖子列表。open 决定点开一行做什么
// （两个窗口各自把帖子送到主窗口的正文），back 是「返回合集列表」。
func (a *app) collectionTopicsBody(c *ui.Context, b *collectionBrowse, open func(Item), back func()) {
	t := c.Theme()
	st := &b.list

	ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
		if iconActionButton(c, chevronLeftIcon, "返回合集列表", "回到合集列表").Clicked() {
			back()
		}
		ui.Text(c, "合集 · "+b.name).FontSize(13).Bold().SingleLine().Shrink(1).MinWidth(0)
		if b.owner != "" {
			ui.Text(c, "by @"+b.owner).FontSize(11).
				TextColor(t.TextMuted).SingleLine().Shrink(0)
		}
		if st.loaded {
			ui.Textf(c, "共 %d 篇", collectionTopicsTotal(st)).FontSize(11).
				TextColor(t.TextMuted).Shrink(0)
		}
		ui.Spacer(c).Grow(1)
		a.smallAction(c, "刷新", st.loading, func() { a.reloadCollectionTopics(b) })
	})

	switch {
	case st.loading && len(st.items) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(8).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 24})
			ui.Text(c, "正在加载合集内容…").FontSize(12).TextColor(t.TextMuted)
		})
		return
	case st.err != "":
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(10).Children(func() {
			ui.Text(c, "加载失败").FontSize(14).Bold()
			ui.Text(c, st.err).FontSize(12).TextColor(t.Danger).MaxLines(3)
			if ui.Button(c, "重试").Clicked() {
				a.loadCollectionTopics(b, 1)
			}
		})
		return
	case len(st.items) == 0:
		ui.Column(c).Grow(1).MinHeight(0).Center().Gap(6).Children(func() {
			ui.Text(c, "这个合集里还没有帖子。").FontSize(13).TextColor(t.TextMuted)
			ui.Text(c, "在帖子详情页的「合集」里可以把帖子归入合集。").FontSize(11).
				TextColor(t.TextMuted)
		})
		return
	}

	items := st.items
	st.list.Key = func(i int) any {
		if i >= 0 && i < len(items) {
			return items[i].Key
		}
		return i
	}
	ui.List(c, &st.list, len(items), func(i int) {
		a.rowWith(c, items[i], open)
	}).Grow(1).MinHeight(0).Gap(8)
	a.collectionTopicsMore(c, b)
}

// collectionTopicsMore 是合集列表底部的续拉行：还有下一页时给按钮，续拉失败
// 时给出原因并可重试，已有内容保持不动。
func (a *app) collectionTopicsMore(c *ui.Context, b *collectionBrowse) {
	t := c.Theme()
	st := &b.list
	switch {
	case st.moreErr != "":
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "加载更多失败："+st.moreErr).FontSize(11).
				TextColor(t.Danger).MaxLines(2).Grow(1)
			a.smallAction(c, "重试", false, func() { a.loadCollectionTopics(b, st.page) })
		})
	case st.loading && len(st.items) > 0:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 14})
			ui.Text(c, "正在加载更多…").FontSize(11).TextColor(t.TextMuted)
		})
	// 服务端给了总数且已经全部拿到时不再给按钮：留着它只会让人点一下拿到
	// 空页，白等一次请求。
	case !st.noMore && (st.totalPages == 0 || st.page < st.totalPages) &&
		!(st.total > 0 && len(st.items) >= st.total):
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "加载更多").Clicked() {
				a.loadCollectionTopics(b, st.page+1)
			}
			ui.Spacer(c).Grow(1)
		})
	}
}
