package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// 合集（api-write.md）
//
//	GET  /v1/me/collections             我的合集（读，见 profile.go）
//	POST /v1/me/collections             新建（scope read:me）
//	POST /v1/me/collections/assign      归入 / 移出（scope read:me）
//
// 读端点在「我的 - 我的内容 - 合集」里；这里补的是写的一半：在帖子详情页
// 直接新建合集、把当前帖子归入某个合集，或把它移出。
//
// 两个写端点的 scope 都是 read:me（历史原因，不需要 write:*），缺权限时仍走
// auth_gate.go 的全局授权链路。
//
// assign 有两种写法：单个 collection_id（null 表示移出）与批量的
// collection_ids（目标全集，空数组 = 移出全部）。这里用单个写法：一次只
// 归入一个合集，不必先知道这篇帖子还在哪些合集里。传了 collection_ids 就
// 会覆盖全集，在没有「当前归属」数据时那样做会误删别的合集。

const (
	myCollectionsPath     = "/v1/me/collections"
	collectionsAssignPath = "/v1/me/collections/assign"
	// collectionNameMax 是新建合集时界面的字数上限。文档没有给上限，这里
	// 只防一个明显过长的名字，不假装知道服务端的真实约束。
	collectionNameMax = 50
)

// collectionItem 是「我的合集」里的一项。Count 是服务端给的帖子数，缺字段
// 时为 0（不显示数字，而不是显示一个假的 0）。
type collectionItem struct {
	ID    string
	Name  string
	Count int
}

// fetchMyCollections 读我的合集。端点整页返回，d 可能是裸数组，也可能包在
// collections / items 里；两种形状都认。
func (c *Client) fetchMyCollections(ctx context.Context) ([]collectionItem, error) {
	return withAuthRetry(c, ctx, func(token string) ([]collectionItem, error) {
		d, err := c.getJSON(ctx, c.apiURL(myCollectionsPath), token)
		if err != nil {
			return nil, err
		}
		return decodeCollections(d), nil
	})
}

func decodeCollections(d json.RawMessage) []collectionItem {
	list := rawArray(d)
	if len(list) == 0 {
		if o := asObject(d); o != nil {
			for _, k := range []string{"collections", "items", "list"} {
				if list = rawArray(o[k]); len(list) > 0 {
					break
				}
			}
		}
	}
	var out []collectionItem
	for _, raw := range list {
		o := asObject(raw)
		if o == nil {
			continue
		}
		id := o.str("id")
		name := cleanText(o.str("name", "title"))
		if id == "" || name == "" {
			continue
		}
		out = append(out, collectionItem{
			ID:    id,
			Name:  name,
			Count: o.num("topic_count", "post_count", "item_count", "count"),
		})
	}
	return out
}

// createCollection 新建一个合集。响应里带创建结果时用它拿 id，没带时返回
// 只填了名字的项，调用方据此重新读一次列表。
func (c *Client) createCollection(ctx context.Context, name string) (collectionItem, error) {
	return withAuthRetry(c, ctx, func(token string) (collectionItem, error) {
		d, err := c.postJSON(ctx, c.apiURL(myCollectionsPath), token, "",
			map[string]any{"name": name})
		if err != nil {
			return collectionItem{}, err
		}
		item := collectionItem{Name: name}
		if o := asObject(d); o != nil {
			// d 可能是合集本身，也可能包在 collection 里。
			if inner := o.sub("collection"); inner != nil {
				o = inner
			}
			if id := o.str("id"); id != "" {
				item.ID = id
			}
			if got := cleanText(o.str("name", "title")); got != "" {
				item.Name = got
			}
			item.Count = o.num("topic_count", "post_count", "item_count", "count")
		}
		return item, nil
	})
}

// assignTopicCollection 把帖子归入某个合集。collectionID 为空时传 null，
// 表示移出合集。409 / 404 视为成功：目标状态已经成立。
func (c *Client) assignTopicCollection(ctx context.Context, shortID, collectionID string) error {
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		var value any // nil 序列化成 null：移出合集
		if collectionID != "" {
			value = collectionID
		}
		_, err := c.postJSON(ctx, c.apiURL(collectionsAssignPath), token, "",
			map[string]any{"short_id": shortID, "collection_id": value})
		var ae *apiError
		if errors.As(err, &ae) &&
			(ae.Status == http.StatusConflict || ae.Status == http.StatusNotFound) {
			err = nil
		}
		return struct{}{}, err
	})
	return err
}

/* ------------------------------------------------------------------
   详情页状态：合集气泡
-------------------------------------------------------------- */

// collectionState 是详情页合集气泡的状态，只在 UI 线程读写。busy 表示正在
// 提交一次归入 / 移出；creating 表示正在新建。gen 丢弃切换帖子之前的迟到
// 结果。
type collectionState struct {
	open     bool
	loading  bool
	loaded   bool
	err      string
	items    []collectionItem
	busy     bool
	creating bool
	draft    string
	gen      uint64
}

// ensureCollections 在气泡第一次打开时读一次合集列表。失败不清 loaded，
// 下次打开会再试。
func (a *app) ensureCollections(d *detailState) {
	if d == nil || a.detail != d || !a.autoLoad || !a.auth.loggedIn() {
		return
	}
	if d.coll.loaded || d.coll.loading {
		return
	}
	d.coll.loading = true
	d.coll.err = ""
	d.coll.gen++
	gen := d.coll.gen
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		items, err := a.client.fetchMyCollections(ctx)
		a.publish(func() {
			if a.detail != d || d.coll.gen != gen {
				return
			}
			d.coll.loading = false
			if err != nil {
				if !a.auth.loggedIn() || a.auth.username() != actor {
					return
				}
				d.coll.err = friendlyError(err)
				a.handleAuthError(err, func() {
					if a.detail == d {
						a.ensureCollections(d)
					}
				})
				return
			}
			d.coll.items = items
			d.coll.loaded = true
		})
	}()
}

// canCollectTopic 报告这篇帖子能不能归入合集。合集是「我」组织自己内容的
// 方式，服务端也只允许作者本人归入（别人的帖子会 403）；匿名帖的 author
// 为 null（解码成「匿名」），确认不了是不是自己发的，同样不给入口，免得
// 点开只换来一句错误。
func (a *app) canCollectTopic(d *detailState) bool {
	return d != nil && d.topic != nil && a.isMine(d.topic.Author)
}

// toggleCollectionPicker 开 / 关合集气泡。打开时顺手读一次列表，第一帧里
// 就能看到自己的合集。
func (a *app) toggleCollectionPicker(d *detailState) {
	if d == nil || a.detail != d || !a.canCollectTopic(d) {
		return
	}
	d.coll.open = !d.coll.open
	if d.coll.open {
		a.ensureCollections(d)
	}
}

// assignTopicCollection 归入（add=true）或移出（add=false）当前帖子。
// 成功后把状态写回详情，页面里的按钮与气泡都跟着变；「我的 - 合集」的
// 帖子数已经过期，清掉缓存。
func (a *app) assignTopicCollection(d *detailState, item collectionItem, add bool) {
	if d == nil || a.detail != d || d.topic == nil || d.coll.busy || !a.canCollectTopic(d) {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能管理合集。", func() {
			if a.detail == d {
				a.assignTopicCollection(d, item, add)
			}
		})
		return
	}
	if d.shortID == "" {
		a.toast(toastMain, "缺少帖子标识，暂时无法归入合集")
		return
	}
	d.coll.busy = true
	target := ""
	if add {
		target = item.ID
	}
	shortID, actor := d.shortID, a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		err := a.client.assignTopicCollection(ctx, shortID, target)
		a.publish(func() {
			if a.detail != d {
				return
			}
			d.coll.busy = false
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthError(err, func() {
					a.assignTopicCollection(d, item, add)
				}) {
					return
				}
				a.toast(toastMain, friendlyError(err))
				return
			}
			d.topic.CollectionKnown = true
			if add {
				d.topic.CollectionID = item.ID
				d.topic.CollectionName = item.Name
				a.toast(toastMain, "已加入合集「"+item.Name+"」")
			} else {
				d.topic.CollectionID = ""
				d.topic.CollectionName = ""
				a.toast(toastMain, "已移出合集")
			}
			a.invalidateCollections()
		})
	}()
}

// createCollectionForTopic 新建一个合集并立刻把当前帖子归进去：用户点
// 「新建」时想的是「把这帖放进这个新合集」，分两步做会多一次点击。
func (a *app) createCollectionForTopic(d *detailState) {
	if d == nil || a.detail != d || d.coll.creating || d.coll.busy || !a.canCollectTopic(d) {
		return
	}
	name := strings.TrimSpace(d.coll.draft)
	if name == "" {
		a.toast(toastMain, "请先填写合集名称")
		return
	}
	if utf8.RuneCountInString(name) > collectionNameMax {
		a.toast(toastMain, fmt.Sprintf("合集名称最多 %d 个字", collectionNameMax))
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能新建合集。", func() {
			if a.detail == d {
				a.createCollectionForTopic(d)
			}
		})
		return
	}
	d.coll.creating = true
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		item, err := a.client.createCollection(ctx, name)
		a.publish(func() {
			if a.detail != d {
				return
			}
			d.coll.creating = false
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthError(err, func() { a.createCollectionForTopic(d) }) {
					return
				}
				a.toast(toastMain, friendlyError(err))
				return
			}
			d.coll.draft = ""
			a.invalidateCollections()
			if item.ID == "" {
				// 服务端没回创建结果：重新读一次列表，让新合集出现在气泡里。
				d.coll.loaded = false
				a.ensureCollections(d)
				a.toast(toastMain, "已新建合集「"+item.Name+"」")
				return
			}
			// 本地先记上，避免等待重读列表；随后直接归入这个新合集。
			d.coll.items = append(d.coll.items, item)
			d.coll.loaded = true
			a.assignTopicCollection(d, item, true)
		})
	}()
}

// invalidateCollections 让「我的 - 合集」下次打开时重新请求：合集里的帖子
// 数变了，那份列表不能再沿用旧结果。
func (a *app) invalidateCollections() {
	if a.profile == nil {
		return
	}
	st := &a.profile.content[profileContentCollections]
	if st.loading {
		return
	}
	st.loaded = false
	st.items = nil
	st.page = 0
	st.total = 0
	st.totalPages = 0
	st.noMore = false
	st.err = ""
	st.moreErr = ""
}

/* ------------------------------------------------------------------
   界面：合集按钮与气泡
-------------------------------------------------------------- */

// collectionButton 是作者行上收藏左侧的「合集」胶囊：点开气泡，选择把帖子
// 归入哪个合集，或者新建一个。详情里带当前合集时按钮直接说明「已在合集」。
// 只在帖子属于当前账号时出现（见 canCollectTopic）。
func (a *app) collectionButton(c *ui.Context, d *detailState) {
	if !a.canCollectTopic(d) {
		return
	}
	t := c.Theme()
	label := "合集"
	tip := "把这篇帖子归入我的合集"
	if d.topic.CollectionKnown && d.topic.CollectionID != "" {
		label = "已在合集"
		if d.topic.CollectionName != "" {
			tip = "已归入「" + d.topic.CollectionName + "」，点一下管理"
		} else {
			tip = "已归入某个合集，点一下管理"
		}
	}
	b := ui.Button(c, label).
		Background(ui.Transparent).
		Border(1, t.Border).
		FontSize(metaFontSize).
		Padding(4, 10).
		Radius(999).
		Shrink(0)
	hovered := b.Hovered()
	ink := t.TextMuted
	if hovered && !d.coll.busy {
		ink = t.Text
		b.Background(t.SurfaceHover)
		b.Border(1, t.Accent)
	}
	if d.coll.busy {
		ink = t.TextMuted
		b.Disabled(true)
	}
	b.TextColor(ink)
	b.Label("合集").Tooltip(tip)
	if !d.coll.busy {
		b.OnClick(func() {
			if !a.auth.loggedIn() {
				a.requestLogin("登录后才能管理合集。", func() {
					if a.detail == d {
						a.toggleCollectionPicker(d)
					}
				})
				return
			}
			a.toggleCollectionPicker(d)
		})
	}
	if d.coll.open {
		ui.Popover(c, b, &d.coll.open, func() {
			a.collectionPanel(c, d)
		})
	}
}

// collectionPanel 是合集气泡的内容：我的合集（点一行就归入）、新建入口，
// 以及「移出合集」。请求在途时只留一行进度，结果由主窗口 toast 报告。
func (a *app) collectionPanel(c *ui.Context, d *detailState) {
	t := c.Theme()
	ui.Column(c).Width(250).Gap(6).Children(func() {
		ui.Text(c, "加入合集").FontSize(metaFontSize).TextColor(t.TextMuted)
		switch {
		case d.coll.loading && len(d.coll.items) == 0:
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c).Size(12, 12)
				ui.Text(c, "正在读取合集…").FontSize(11).TextColor(t.TextMuted)
			})
		case d.coll.err != "":
			ui.Text(c, d.coll.err).FontSize(11).TextColor(t.Danger).MaxLines(2)
			a.smallAction(c, "重试", false, func() {
				d.coll.loaded = false
				a.ensureCollections(d)
			})
		case len(d.coll.items) == 0:
			ui.Text(c, "还没有合集，在下面新建一个。").
				FontSize(11).TextColor(t.TextMuted).MaxLines(2)
		default:
			for _, item := range d.coll.items {
				a.collectionRow(c, d, item)
			}
		}
		if d.topic != nil && d.topic.CollectionKnown && d.topic.CollectionID != "" {
			current := collectionItem{ID: d.topic.CollectionID, Name: d.topic.CollectionName}
			a.smallAction(c, "移出合集", d.coll.busy, func() {
				a.assignTopicCollection(d, current, false)
			})
		}
		ui.Divider(c)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c, &d.coll.draft).Label("合集名称").
				Placeholder("新合集名称").Width(150)
			btn := ui.PrimaryButton(c, "新建")
			if d.coll.creating || d.coll.busy {
				btn.Disabled(true)
			}
			if btn.Clicked() {
				a.createCollectionForTopic(d)
			}
		})
		if d.coll.creating {
			ui.Text(c, "正在新建合集…").FontSize(11).TextColor(t.TextMuted)
		}
	})
}

// collectionRow 是气泡里的一行合集：点一下归入；当前所在的合集点一下移出。
func (a *app) collectionRow(c *ui.Context, d *detailState, item collectionItem) {
	t := c.Theme()
	current := d.topic != nil && d.topic.CollectionKnown && d.topic.CollectionID == item.ID
	row := ui.Row(c).FillWidth().Padding(3, 6).Radius(6).Gap(6).
		AlignItems(ui.Center).Background(ui.Transparent)
	if row.Hovered() && !d.coll.busy {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Text(c, item.Name).FontSize(metaFontSize).SingleLine().Grow(1)
		if item.Count > 0 {
			ui.Textf(c, "%d", item.Count).FontSize(11).TextColor(t.TextMuted).Shrink(0)
		}
		if current {
			ui.Text(c, "已在").FontSize(11).Bold().TextColor(t.Accent).Shrink(0)
		}
	})
	row.Label("合集 " + item.Name)
	if current {
		row.Tooltip("点一下把帖子移出这个合集")
	} else {
		row.Tooltip("把帖子归入这个合集")
	}
	if !d.coll.busy {
		row.OnClick(func() { a.assignTopicCollection(d, item, !current) })
	}
}
