package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"

	"github.com/egoist/mygo/ui"
)

// 合集内容浏览（api-read.md「合集」）
//
//	GET /v1/users/:userName/collections    某用户的公开合集（匿名可读）
//	GET /v1/collections/:collectionId/topics  合集内的帖子
//
// 合集列表本身留在原来的窗口里（「我的 - 我的内容 - 合集」或用户资料卡的
// 「ta的合集」分段），点开一个合集时就地换成它的帖子，右上角给一个「返回」。
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
	a.loadSpec(b.target, &b.list, collectionTopicsSpec(b.id), page, nil)
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
		a.smallRefreshButton(c, st.loading, func() { a.reloadCollectionTopics(b) })
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
	// 详情接口不带合集归属，只有这里知道点开的是哪张合集：把 id / 名字盖到
	// 这一行上，详情页据此画当前合集卡片与「上一篇」（见 detail_ui.go）。
	openFromCollection := func(picked Item) {
		if picked.CollectionID == "" {
			picked.CollectionID = b.id
			picked.CollectionName = b.name
		}
		open(picked)
	}
	ui.List(c, &st.list, len(items), func(i int) {
		a.rowWithNoWatchLater(c, items[i], openFromCollection)
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

/* ------------------------------------------------------------------
    详情页的当前合集卡片
-------------------------------------------------------------- */

// detailCollectionNavMaxPages 是找「上一篇」时最多翻的页数。合集内帖子按
// 页拉取，当前帖不一定在第一页；十页（500 条）足够覆盖现实里的合集，又不会
// 让一篇帖子把配额耗在翻页上。
const detailCollectionNavMaxPages = 10

// detailCollectionNav 是详情页正文里「当前合集」卡片的数据。它只留展示需要
// 的结果，整份帖子列表在后台 goroutine 里用完就丢。
type detailCollectionNav struct {
	id      string // 已经为哪个合集发过请求；换帖子 / 换合集时重置
	loading bool
	loaded  bool
	count   int
	hasPrev bool
	prev    Item
	err     string
	gen     uint64
}

// ensureDetailCollectionNav 在正文带合集归属时读一次合集内帖子：用它给出条数
// 与当前帖子的上一篇。换合集才重读，失败后由卡片上的「重试」显式触发，视图
// 每帧调用也不会重复发请求。
func (a *app) ensureDetailCollectionNav(d *detailState) {
	if d == nil || !a.detailLive(d) || !a.autoLoad {
		return
	}
	id, _, known := d.collectionContext()
	if !known {
		return
	}
	nav := &d.collNav
	if nav.id == id && (nav.loading || nav.loaded) {
		return
	}
	// 详情接口已经把条数和上 / 下一篇给了：直接用，卡片第一次渲染就有入口，
	// 也省掉一次合集列表请求。prev 为 null 表示这是第一篇，没什么可找的。
	if d.topic != nil && d.topic.CollectionCount > 0 &&
		(d.topic.CollectionPrev != nil || d.topic.CollectionNext != nil) {
		nav.id, nav.loaded, nav.count = id, true, d.topic.CollectionCount
		return
	}
	*nav = detailCollectionNav{id: id, loading: true}
	nav.gen++
	gen := nav.gen
	shortID, actor := d.shortID, a.auth.username()
	target := a.detailToastFor(d)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		spec := collectionTopicsSpec(id)
		var items []Item
		count := 0
		found := -1
		var loadErr error
		for page := 1; page <= detailCollectionNavMaxPages; page++ {
			res, err := a.client.fetch(ctx, spec, page)
			if err != nil {
				loadErr = err
				break
			}
			base := len(items)
			items = append(items, res.Items...)
			if count == 0 {
				count = res.Total
			}
			for i, it := range res.Items {
				if it.Key == shortID || it.ShortID == shortID {
					found = base + i
					break
				}
			}
			if found >= 0 {
				break
			}
			if len(res.Items) == 0 ||
				(res.TotalPages > 0 && page >= res.TotalPages) ||
				(res.Total > 0 && len(items) >= res.Total) {
				break
			}
		}
		a.publish(func() {
			if !a.detailLive(d) || d.collNav.gen != gen || d.collNav.id != id {
				return
			}
			nav.loading = false
			if loadErr != nil {
				if !a.auth.loggedIn() || a.auth.username() != actor {
					return
				}
				if a.handleAuthErrorFor(target, loadErr) {
					return
				}
				if len(items) == 0 {
					nav.err = friendlyError(loadErr)
					return
				}
				// 已经翻到一些帖子：保留可用信息，不把整张卡换成错误。
			}
			if count == 0 && d.topic != nil {
				count = d.topic.CollectionCount
			}
			if count == 0 {
				count = len(items)
			}
			nav.count = count
			if found > 0 && items[found-1].ShortID != "" {
				nav.prev = items[found-1]
				nav.hasPrev = true
			}
			nav.loaded = true
		})
	}()
}

// openDetailCollectionBrowse 打开发帖人的资料卡，落在「ta的合集」并直接进入
// 这篇帖子所在的合集。合集是作者组织的，别人的合集不该跑到「我的」窗口里看。
func (a *app) openDetailCollectionBrowse(d *detailState) {
	id, name, known := d.collectionContext()
	if !known {
		return
	}
	author := ""
	if d.topic != nil {
		author = strings.TrimSpace(d.topic.Author)
	}
	if author == "" || author == "匿名" {
		return // 匿名帖没有可展示的资料卡
	}
	if name == "" {
		name = "未命名合集"
	}
	// 资料卡换人时会整份重建状态，所以 tab / collBrowse 要在同一个 UI 线程
	// 回调里、窗口打开之后再写，不能提前挂到会被丢掉的那份状态上。
	a.publish(func() {
		if a.userCard == nil || a.userCard.username != author {
			a.userCard = newUserCardState(author)
		}
		cs := a.userCard
		cs.tab = userCardTabCollections
		cs.collBrowse = newCollectionBrowse(collectionItem{
			ID: id, Name: name,
		}, author, toastUserCard)
		a.showUserCardWindow(author)
		a.loadCollectionTopics(cs.collBrowse, 1)
	})
}

// detailCollectionNeighbor 返回卡片上「上一篇 / 下一篇」可点的目标。详情接口
// 直接给出 prev / next 时优先用它；老响应没有时，上一篇退回从合集列表算出的
// 位置。next 只有接口给了才会有，不能靠列表倒推。
func detailCollectionNeighbor(d *detailState, next bool) (Item, bool) {
	if d == nil {
		return Item{}, false
	}
	if d.topic != nil {
		var n *collectionNeighbor
		if next {
			n = d.topic.CollectionNext
		} else {
			n = d.topic.CollectionPrev
		}
		if n != nil && n.ShortID != "" {
			return Item{
				ShortID: n.ShortID,
				Title:   n.Title,
				URL:     topicURL(n.ShortID, n.NodeSlug),
			}, true
		}
	}
	if !next && d.collNav.hasPrev && d.collNav.prev.ShortID != "" {
		return d.collNav.prev, true
	}
	return Item{}, false
}

// openDetailNeighborPost 在同一合集里打开当前帖子的上 / 下一篇。沿用 openDetail
// 的统一入口：详情窗口里会多一个标签，那篇帖子再按自己的合集继续翻。
func (a *app) openDetailNeighborPost(d *detailState, next bool) {
	p, ok := detailCollectionNeighbor(d, next)
	if !ok || p.ShortID == "" {
		return
	}
	id, name, _ := d.collectionContext()
	// 把合集上下文一起带过去，下一篇才会接着画自己的合集卡片与相邻入口。
	a.openDetail(Item{
		ShortID: p.ShortID, Title: p.Title, URL: p.URL,
		CollectionID: id, CollectionName: name,
	})
}

// collectionNeighborRow 画「上一篇 / 下一篇」一行：图标、标签与标题，点整行打开。
func (a *app) collectionNeighborRow(c *ui.Context, d *detailState, next bool) {
	p, ok := detailCollectionNeighbor(d, next)
	if !ok {
		return
	}
	tk := lobetheme.TokensOf(c)
	label, icon := "上一篇", lucide.ArrowLeft()
	if next {
		label, icon = "下一篇", lucide.ArrowRight()
	}
	title := p.Title
	if title == "" {
		title = p.ShortID
	}
	row := ui.Row(c).FillWidth().Gap(8).Padding(4, 6).Radius(6).
		AlignItems(ui.Center).Background(ui.Transparent)
	row.Children(func() {
		ui.Icon(c, icon).Size(14, 14).TextColor(tk.TextSecondary).Shrink(0)
		ui.Text(c, label).FontSize(tk.FontSizeSM).
			TextColor(tk.TextSecondary).Shrink(0)
		ui.Text(c, truncate(title, 40)).FontSize(tk.FontSizeSM).
			SingleLine().Shrink(1).MinWidth(0)
	})
	row.Label(label + " " + title)
	row.OnClick(func() { a.openDetailNeighborPost(d, next) })
}

// detailCollectionCard 画正文末尾的当前合集卡片：合集名、条数、「查看全部」
// 和相邻的上 / 下一篇。详情给了 prev / next 就能立即点开，不必等列表请求。
func (a *app) detailCollectionCard(c *ui.Context, d *detailState) {
	_, name, known := d.collectionContext()
	if !known {
		return
	}
	tk := lobetheme.TokensOf(c)
	nav := &d.collNav
	if name == "" {
		name = "合集"
	}
	ui.Column(c).FillWidth().Gap(8).Padding(12, 14).Radius(tk.BorderRadius).
		Background(tk.FillQuaternary).Children(func() {
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, lucide.FolderClosed()).Size(16, 16).
				TextColor(tk.TextSecondary).Shrink(0)
			ui.Text(c, "合集 · "+name).FontSize(tk.FontSizeSM).Bold().
				SingleLine().Shrink(1).MinWidth(0)
			count := nav.count
			if !nav.loaded && d.topic != nil {
				// 详情的 post_collection 可能自带条数：列表还没回来时先用它。
				count = d.topic.CollectionCount
			}
			if count > 0 {
				ui.Textf(c, "共 %d 篇", count).FontSize(tk.FontSizeSM).
					TextColor(tk.TextSecondary).Shrink(0)
			}
			ui.Spacer(c).Grow(1)
			a.smallAction(c, "查看全部", false, func() {
				a.openDetailCollectionBrowse(d)
			})
		})
		if nav.err != "" {
			ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "合集加载失败："+nav.err).FontSize(11).
					TextColor(tk.Error).MaxLines(2).Grow(1)
				a.smallAction(c, "重试", false, func() {
					d.collNav = detailCollectionNav{}
					a.ensureDetailCollectionNav(d)
				})
			})
		} else {
			a.collectionNeighborRow(c, d, false)
			a.collectionNeighborRow(c, d, true)
		}
	})
}
