package main

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/motion"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 详情页作者 / 评论的「头像 + 昵称」上的右键菜单。菜单就地弹出，左键仍然
// 走 userTag 原来的「打开资料卡」。顺序固定：@名字、分割线、关注、屏蔽。
// 关注 / 屏蔽两项按当前状态换文案：已关注「取消关注」、未关注「关注ta」，
// 已屏蔽「移除屏蔽」、未屏蔽「屏蔽ta」（见 avatar_context_menu_test.go）。

const (
	detailAvatarAtKey     = "at"
	detailAvatarFollowKey = "follow"
	detailAvatarBlockKey  = "block"

	detailAvatarFollowLabel   = "关注ta"
	detailAvatarUnfollowLabel = "取消关注"
	detailAvatarBlockLabel    = "屏蔽ta"
	detailAvatarUnblockLabel  = "移除屏蔽"
)

// avatarRelationState 是右键菜单打开时现取的用户关系。关注状态来自公开资料
// 的 is_following；屏蔽状态读进程级的 contentFilter 快照，不在这里缓存。
type avatarRelationState struct {
	followKnown bool
	following   bool
	busy        bool
	triedAt     time.Time
}

// 拉资料失败后的退避，写法同其它 ensure* 加载器；别每帧重发注定失败的请求。
const avatarRelationRetry = 30 * time.Second

// ensureAvatarRelation 在右键菜单打开时取一次这位用户的关注状态。公开资料
// 才带 is_following，帖子详情与评论响应都没有；取回来写进 detailState，
// publish 触发重绘，菜单还开着就能把「关注ta」翻成「取消关注」。
func (a *app) ensureAvatarRelation(d *detailState, name string) {
	if d == nil || d.dead || name == "" || a.auth == nil ||
		!a.auth.loggedIn() || a.client == nil {
		return
	}
	key := mentionKey(name)
	if key == "" {
		return
	}
	if d.avatarRelations == nil {
		d.avatarRelations = make(map[string]*avatarRelationState)
	}
	rel := d.avatarRelations[key]
	if rel == nil {
		rel = &avatarRelationState{}
		d.avatarRelations[key] = rel
	}
	if rel.followKnown || rel.busy {
		return
	}
	if !rel.triedAt.IsZero() && time.Since(rel.triedAt) < avatarRelationRetry {
		return
	}
	rel.busy = true
	rel.triedAt = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), followTimeout)
	go func() {
		defer cancel()
		p, err := a.client.fetchAccountProfile(ctx, name)
		a.publish(func() {
			if d.dead {
				return
			}
			rel.busy = false
			if err == nil && p.FollowKnown {
				rel.followKnown = true
				rel.following = p.IsFollowing
			}
		})
	}()
}

// narrowContextMenu 是 Lobe 右键菜单的同款窄版：Tokens、动效、阴影都取自
// lobe，但把 lobeoverlay.ContextMenu 写死的 MinWidth(220) 换成 120，短标签
// 不再撑出一大片空白。只用到 Label / Disabled / Divider 三种字段，正好覆盖
// 头像菜单这几行；右击、键盘菜单键与选中回调仍是 mygo 的 ContextMenuBase。
func narrowContextMenu(c *ui.Context, target ui.Element, items []lobeoverlay.MenuItem) (chosen string, menuOpen bool) {
	tk := lobetheme.TokensOf(c)
	open := ui.Local(target, "contextMenu", func() bool { return false })
	m := ui.ContextMenuBase(c, target, open)
	menuOpen = m.Open()
	chosen = ""
	m.Popup(func(panel ui.Element) {
		panel.Padding(4).Radius(tk.BorderRadius).MinWidth(120).
			Background(tk.BgElevated).Transition(motion.Popup()).AttachedOrigin()
		tk.Elevate(panel, lobetheme.BoxShadowSecondary)
		panel.Children(func() {
			for i, it := range items {
				if it.Divider {
					ui.Box(c.Key(i)).Height(1).Margin(4, 0).AlignSelf(ui.Stretch).Background(tk.Border)
					continue
				}
				item := m.Item().Disabled(it.Disabled).
					PaddingY(6).PaddingX(12).MinHeight(32).Radius(tk.BorderRadiusSM).
					AlignSelf(ui.Stretch).FontSize(14).
					Transition(ui.ElementTransition{
						Duration: 150 * time.Millisecond, Ease: motion.EaseOut, Colors: true,
					})
				fg := tk.Text
				switch {
				case it.Disabled:
					fg = tk.TextDisabled
					item.Opacity(0.5)
				case item.Pressed():
					item.Background(tk.FillSecondary)
				case item.Highlighted():
					item.Background(tk.FillTertiary)
				}
				item.TextColor(fg)
				item.Children(func() {
					ui.Text(c, it.Label).Grow(1).SingleLine()
				})
				if item.Clicked() && !it.Disabled {
					chosen = it.Key
				}
			}
		})
	})
	return chosen, menuOpen
}

// detailAvatarMenu 为详情页的一处「头像 + 昵称」建右键菜单并执行选中的项。
// name 是它代表的昵称，userID 是它的 uuid（关注端点要 uuid，老响应可能没给）。
// d 是这一块所属的详情，必须是构建这一帧的那一份；回调在之后的帧里触发，
// 不再去读 a.detail，免得内联浏览切层后把操作打到别的帖子上。
func (a *app) detailAvatarMenu(c *ui.Context, identity ui.Element, d *detailState, name, userID string) {
	if d == nil {
		return
	}
	key := mentionKey(name)
	self := a.followAuthorIsSelf(name)
	// 注意：detailAvatarMenu 每帧都会构建，不能在函数开头就发请求——那会让
	// 无关操作（表态、收藏、改评论开关）都触发关注 / 名单请求。先把菜单
	// 建出来，只有 m.Open() 为真时才是用户真的右键打开了菜单，这时才取关系。
	// 两个加载器都是守卫式的，菜单开着每帧重画也不会重复发请求。
	// 关注状态优先用这篇详情里由本菜单提交并确认过的，其次是右键时现取的
	// 公开资料，再其次是刚看过的资料卡；都没有就按未关注展示。
	fs := d.avatarFollow[userID]
	fsKnown := fs != nil && fs.known && fs.viewer == a.auth.username()
	following := fsKnown && fs.following
	relationKnown := false
	rel := d.avatarRelations[key]
	if !fsKnown && rel != nil && rel.followKnown {
		following, relationKnown = rel.following, true
	}
	if !fsKnown && !relationKnown && a.userCard != nil && a.userCard.follow.known &&
		a.userCard.follow.viewer == a.auth.username() &&
		(a.userCard.user.ID == userID || mentionKey(a.userCard.username) == key) {
		following = a.userCard.follow.following
	}
	blocked := a.filterBlocksUser(name)
	followLabel, blockLabel := detailAvatarFollowLabel, detailAvatarBlockLabel
	if following {
		followLabel = detailAvatarUnfollowLabel
	}
	if blocked {
		blockLabel = detailAvatarUnblockLabel
	}
	items := []lobeoverlay.MenuItem{
		{
			Key: detailAvatarAtKey, Label: "@" + name,
			// mentionExclude 同时排除窗口账号与临时换用的回复账号；自己的
			// 名字不进回复框，免得发出一条 @ 自己的评论。
			Disabled: key == "" || self || a.mentionExclude(d)[key],
		},
		{Divider: true},
		{
			Key: detailAvatarFollowKey, Label: followLabel,
			Disabled: self || userID == "",
		},
		{
			Key: detailAvatarBlockKey, Label: blockLabel,
			Disabled: self,
		},
	}
	// 构建菜单与处理选中都在同一帧返回；Disabled 的项不会被选中，
	// 这里再挡一道，防状态在弹菜单期间被别的窗口改掉。
	chosen, menuOpen := narrowContextMenu(c, identity, items)
	if menuOpen {
		a.ensureAvatarRelation(d, name)
		a.ensureContentFilter(false)
	}
	switch chosen {
	case detailAvatarAtKey:
		if self || key == "" || a.mentionExclude(d)[key] {
			return
		}
		a.insertMention(d, mentionAt(name))
		d.focusComposer = true
	case detailAvatarFollowKey:
		a.detailAvatarToggleFollow(d, name, userID, !following)
	case detailAvatarBlockKey:
		if a.isMine(name) {
			return
		}
		a.setUserBlocked(name, !blocked)
	}
}

// detailAvatarToggleFollow 从头像菜单关注 / 取消关注一个用户。状态按 uuid 记在
// detailState 上，未登录时和资料卡一样先弹授权框；提交与回包交给 submitFollow。
func (a *app) detailAvatarToggleFollow(d *detailState, name, userID string, follow bool) {
	if userID == "" || a.followAuthorIsSelf(name) {
		return
	}
	if a.auth == nil || !a.auth.loggedIn() {
		a.requestLoginFor(a.detailToastFor(d), "登录后才能关注这位用户。")
		return
	}
	if d.avatarFollow == nil {
		d.avatarFollow = make(map[string]*followState)
	}
	fs := d.avatarFollow[userID]
	if fs == nil {
		fs = &followState{userID: userID, viewer: a.auth.username()}
		d.avatarFollow[userID] = fs
	}
	a.submitFollow(fs, follow, a.detailToastFor(d), func() bool {
		return a.detail == d && !d.dead
	})
}
