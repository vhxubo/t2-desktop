package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// 合集（api-write.md / api-read.md 的「合集」）
//
//	GET    /v1/me/collections                     我的合集（读，见 profile.go）
//	POST   /v1/me/collections                     新建（scope read:me）
//	PATCH  /v1/me/collections/:collectionId       改名（scope read:me，仅创建者）
//	DELETE /v1/me/collections/:collectionId       删除（scope read:me，仅创建者）
//	POST   /v1/me/collections/assign              归入 / 移出（scope read:me）
//	GET    /v1/users/:userName/collections        某用户的公开合集
//	GET    /v1/collections/:collectionId/topics   合集内的帖子
//
// 读端点在「我的 - 我的内容 - 合集」里；这里补的是写的一半：在帖子详情页
// 直接新建合集、把当前帖子归入某个合集，或把它移出；在「我的」窗口里改名与
// 删除合集。
//
// 写端点的 scope 都是 read:me（历史原因，不需要 write:*），缺权限时仍走
// auth_gate.go 的全局授权链路。
//
// assign 只支持单个 collection_id（null 表示移出）。批量的 collection_ids
// 已于 2026-11-12 从文档删除（一篇帖子只能属于一个合集，传它必定 400），
// 这里也不发：一次只归入一个合集，不必先知道这篇帖子还在哪些合集里。
const (
	myCollectionsPath     = "/v1/me/collections"
	collectionsAssignPath = "/v1/me/collections/assign"
	// collectionNameMax 是新建 / 改名时界面的字数上限。文档明确 ≤30 字
	// （api-write.md 的「合集」一节），超长会被服务端 400 invalid_request。
	collectionNameMax = 30
	// collectionTopicsLimit 是合集内帖子列表每页条数：与其它帖子列表一致，
	// 该端点没有单独的 limit 上限说明。
	collectionTopicsLimit = 50
)

// collectionItem 是「我的合集」里的一项。Count 是服务端给的帖子数，缺字段
// 时为 0（不显示数字，而不是显示一个假的 0）。
type collectionItem struct {
	ID    string
	Name  string
	Count int
}

// collectionPath 拼单个合集的路径：改名（PATCH）与删除（DELETE）都用它。
// 合集 id 是服务端给的 uuid，转义后拼进去，别让特殊字符改变路径层级。
func collectionPath(id string) string {
	return myCollectionsPath + "/" + url.PathEscape(id)
}

// userCollectionsPath 是某用户的公开合集列表。昵称转义规则复用 userPath。
func userCollectionsPath(name string) string {
	return userPath(name) + "/collections"
}

// collectionTopicsPath 是合集内的帖子列表。合集 id 同样按路径段转义。
func collectionTopicsPath(id string) string {
	return "/v1/collections/" + url.PathEscape(id) + "/topics"
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

// fetchUserCollections 读某用户的公开合集。它是匿名可读的端点，登录后照常
// 带令牌：响应里可能带个性化字段，解码时只用合集的 id / 名字 / 帖子数。
func (c *Client) fetchUserCollections(ctx context.Context, name string) ([]collectionItem, error) {
	if name == "" {
		return nil, errors.New("还不知道要读谁的合集")
	}
	d, err := withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		return c.getJSON(ctx, c.apiURL(userCollectionsPath(name)), token)
	})
	if err != nil {
		return nil, err
	}
	return decodeCollections(d), nil
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
		item, ok := decodeCollectionObject(raw)
		// 没有 id 或名字的项跳过：弹窗里点不了，也不该显示成「未命名」。
		if ok && item.ID != "" {
			out = append(out, item)
		}
	}
	return out
}

// decodeCollectionObject 解一个合集对象：响应可能直接是合集，也可能包在
// collection 里。名字是必须的（没有名字的行没法展示），id 由调用方按需要
// 补齐。ok 为 false 表示这一项认不出来。
func decodeCollectionObject(raw json.RawMessage) (collectionItem, bool) {
	o := asObject(raw)
	if o == nil {
		return collectionItem{}, false
	}
	if inner := o.sub("collection"); inner != nil {
		o = inner
	}
	name := cleanText(o.str("name", "title"))
	if name == "" {
		return collectionItem{}, false
	}
	return collectionItem{
		ID:    o.str("id"),
		Name:  name,
		Count: o.num("topic_count", "post_count", "item_count", "count"),
	}, true
}

// renameCollection 给合集改名（PATCH，body 只有 name）。id 不变，名字以服务端
// 确认为准（可能会去掉首尾空白）；响应没带结果时用提交的名字兜底。
func (c *Client) renameCollection(ctx context.Context, id, name string) (collectionItem, error) {
	return withAuthRetry(c, ctx, func(token string) (collectionItem, error) {
		d, err := c.patchJSON(ctx, c.apiURL(collectionPath(id)), token,
			map[string]any{"name": name})
		if err != nil {
			return collectionItem{}, err
		}
		item, ok := decodeCollectionObject(d)
		if !ok {
			return collectionItem{ID: id, Name: name}, nil
		}
		if item.ID == "" {
			item.ID = id
		}
		return item, nil
	})
}

// deleteCollection 删掉一个合集（仅创建者）。合集里的帖子只是被取消挂载，
// 帖子、评论与浏览数都不受影响，但不可恢复。404 视为成功：目标状态已经
// 成立，不必让用户看见一次「已经没了」的错误。
func (c *Client) deleteCollection(ctx context.Context, id string) error {
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		_, err := c.deleteJSON(ctx, c.apiURL(collectionPath(id)), token)
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			err = nil
		}
		return struct{}{}, err
	})
	return err
}

// assignTopicCollection 把帖子归入某个合集。collectionID 为空时传 null，
// 表示移出合集。409 / 404 视为成功：目标状态已经成立。
func (c *Client) assignTopicCollection(ctx context.Context, shortID, collectionID string) error {
	var value any // nil 序列化成 null：移出合集
	if collectionID != "" {
		value = collectionID
	}
	return c.assignTopicPayload(ctx, map[string]any{
		"short_id":      shortID,
		"collection_id": value,
	})
}

// assignTopicNewCollection 一步建新合集并把帖子归进去。文档的 new_collection_name
// 优先于 collection_id，省掉「先建再归入」两次写请求和中间态。
func (c *Client) assignTopicNewCollection(ctx context.Context, shortID, name string) error {
	return c.assignTopicPayload(ctx, map[string]any{
		"short_id":            shortID,
		"new_collection_name": name,
	})
}

// assignTopicPayload 是两条归入路径共用的写出口。409 / 404 表示目标状态已经
// 成立，按成功处理；其它错误原样带回给授权链路或界面。
func (c *Client) assignTopicPayload(ctx context.Context, payload map[string]any) error {
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		_, err := c.postJSON(ctx, c.apiURL(collectionsAssignPath), token, "", payload)
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
   详情页状态：合集弹窗
-------------------------------------------------------------- */

// collectionState 是详情页合集弹窗的状态，只在 UI 线程读写。busy 表示正在
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

// ensureCollections 在弹窗第一次打开时读一次合集列表。失败不清 loaded，
// 下次打开会再试。
func (a *app) ensureCollections(d *detailState) {
	if d == nil || !a.detailLive(d) || !a.autoLoad || !a.auth.loggedIn() {
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
			if !a.detailLive(d) || d.coll.gen != gen {
				return
			}
			d.coll.loading = false
			if err != nil {
				if !a.auth.loggedIn() || a.auth.username() != actor {
					return
				}
				d.coll.err = friendlyError(err)
				a.handleAuthError(err, func() {
					if a.detailLive(d) {
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

// toggleCollectionPicker 开 / 关合集弹窗（显式入口，登录回调与测试用）。
// 打开时顺手读一次列表，第一帧里就能看到自己的合集。
func (a *app) toggleCollectionPicker(d *detailState) {
	if d == nil || !a.detailLive(d) || !a.canCollectTopic(d) {
		return
	}
	d.coll.open = !d.coll.open
	if d.coll.open {
		a.ensureCollections(d)
	}
}

// assignTopicCollection 归入（add=true）或移出（add=false）当前帖子。
// 成功后把状态写回详情，页面里的按钮与弹窗都跟着变；「我的 - 合集」的
// 帖子数已经过期，清掉缓存。
func (a *app) assignTopicCollection(d *detailState, item collectionItem, add bool) {
	if d == nil || !a.detailLive(d) || d.topic == nil || d.coll.busy || !a.canCollectTopic(d) {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能管理合集。", func() {
			if a.detailLive(d) {
				a.assignTopicCollection(d, item, add)
			}
		})
		return
	}
	if d.shortID == "" {
		a.toast(a.detailToastFor(d), "缺少帖子标识，暂时无法归入合集")
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
			if !a.detailLive(d) {
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
				a.toast(a.detailToastFor(d), friendlyError(err))
				return
			}
			d.topic.CollectionKnown = true
			if add {
				d.topic.CollectionID = item.ID
				d.topic.CollectionName = item.Name
				a.toast(a.detailToastFor(d), "已加入合集「"+item.Name+"」")
			} else {
				d.topic.CollectionID = ""
				d.topic.CollectionName = ""
				a.toast(a.detailToastFor(d), "已移出合集")
			}
			a.invalidateCollections()
		})
	}()
}

// createCollectionForTopic 新建一个合集并立刻把当前帖子归进去：文档的
// new_collection_name 让这一步只发一次写请求，不再「先建再归入」。
func (a *app) createCollectionForTopic(d *detailState) {
	if d == nil || !a.detailLive(d) || d.coll.creating || d.coll.busy || !a.canCollectTopic(d) {
		return
	}
	name := strings.TrimSpace(d.coll.draft)
	if name == "" {
		a.toast(a.detailToastFor(d), "请先填写合集名称")
		return
	}
	if utf8.RuneCountInString(name) > collectionNameMax {
		a.toast(a.detailToastFor(d), fmt.Sprintf("合集名称最多 %d 个字", collectionNameMax))
		return
	}
	if !a.auth.loggedIn() {
		a.requestLogin("登录后才能新建合集。", func() {
			if a.detailLive(d) {
				a.createCollectionForTopic(d)
			}
		})
		return
	}
	if d.shortID == "" {
		a.toast(a.detailToastFor(d), "缺少帖子标识，暂时无法归入合集")
		return
	}
	d.coll.creating = true
	shortID, actor := d.shortID, a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		err := a.client.assignTopicNewCollection(ctx, shortID, name)
		// 归入回执不保证带新合集 id：重读一次我的合集，补齐列表与 id。
		var items []collectionItem
		var listErr error
		if err == nil {
			items, listErr = a.client.fetchMyCollections(ctx)
		}
		a.publish(func() {
			if !a.detailLive(d) {
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
				a.toast(a.detailToastFor(d), friendlyError(err))
				return
			}
			d.coll.draft = ""
			d.topic.CollectionKnown = true
			d.topic.CollectionName = name
			d.topic.CollectionID = ""
			if listErr == nil {
				d.coll.items = items
				d.coll.loaded = true
				for _, it := range items {
					if it.Name == name {
						d.topic.CollectionID = it.ID
						break
					}
				}
			} else {
				d.coll.loaded = false
			}
			a.invalidateCollections()
			a.toast(a.detailToastFor(d), "已加入合集「"+name+"」")
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

// invalidateDetailCollectionPicker 让详情页的合集弹窗下次打开时重读：改名或
// 删除之后，弹窗里留着的旧名字不能再点，否则点下去就是一次注定失败的归入。
func (a *app) invalidateDetailCollectionPicker() {
	if a.detail == nil || !a.detail.coll.loaded {
		return
	}
	a.detail.coll.loaded = false
	a.detail.coll.items = nil
	a.detail.coll.err = ""
}

/* ------------------------------------------------------------------
   界面：合集按钮与弹窗
-------------------------------------------------------------- */

// collectionButton 是作者行上收藏左侧的「合集」胶囊：点开 Modal，选择把帖子
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
					if a.detailLive(d) {
						a.toggleCollectionPicker(d)
					}
				})
				return
			}
			a.toggleCollectionPicker(d)
		})
	}
}

// collectionDialog 是合集弹窗：点作者行的「合集」胶囊后盖在整页上，选一行归入
// 或新建。它走 Lobe 的 Modal，标题栏与 Esc / 点窗外关闭由 Modal 提供。
func (a *app) collectionDialog(c *ui.Context, d *detailState) {
	if !d.coll.open {
		return
	}
	feedback.Modal(c, &d.coll.open, feedback.ModalProps{
		Title:    "加入合集",
		Width:    380,
		NoFooter: true,
	}, func() {
		a.collectionPanel(c, d)
	})
}

// collectionPanel 是合集弹窗里的内容：我的合集（点一行就归入）、新建入口，
// 以及「移出合集」。请求在途时只留一行进度，结果由主窗口 toast 报告。
func (a *app) collectionPanel(c *ui.Context, d *detailState) {
	t := c.Theme()
	ui.Column(c).FillWidth().Gap(6).Children(func() {
		switch {
		case d.coll.loading && len(d.coll.items) == 0:
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				lobeSpin(c, feedback.SpinProps{CustomSize: 12})
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

// collectionRow 是弹窗里的一行合集：点一下归入；当前所在的合集点一下移出。
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
