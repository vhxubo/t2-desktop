package main

import (
	"github.com/egoist/mygo/ui"
)

// 详情页的临时回复身份
//
// 多账号时每个账号一扇窗口，但「看的是这篇帖子，想用另一个账号回一句」并不
// 值得再找到同一篇帖子、再打开一次：窗口是自己的账号，回复却可以临时借用
// 本机另一个账号的令牌。
//
// 这里只改「评论提交用谁的令牌」这一件事：
//
//   - 阅读、收藏、投票、表情表态与通知都还是窗口自己的账号，不跟着变；
//   - 换过来的身份只活在这一份 detailState 上，关闭详情页、从正文链接跳到
//     别的帖子再返回、或者把这个账号退出，都会回到窗口自己的账号；不落盘，
//     下次打开还是自己；
//   - 选择入口是评论框下方那枚身份标签（原来只写「当前账号 @谁」），点开
//     气泡列出本机挂着的账号，选中即换；标签换成「回复身份 @谁」。
//
// 提交走对方账号自己的 AuthManager（令牌、刷新、凭据都是它那一份），所以
// 401 刷新与 scope 判断都按那个账号来；与它自己的窗口并发请求是安全的
// （AuthManager 的刷新是单飞的）。缺权限时不能弹本窗口的授权弹窗——那会
// 重新授权错的人——改为提示用户去那个账号的窗口补授权。

// replySurface 标记身份标签画在哪扇窗口里。气泡的展开状态存在 detailState
// 上，但只有点开它的那扇窗口才渲染，主窗口与独立写评论窗口不会同时弹。
type replySurface string

const (
	replySurfaceDetail replySurface = "detail"
	replySurfaceWindow replySurface = "reply"
)

// replyIdentity 是一次评论提交真正要用的账号。Switched 表示它来自这篇帖子
// 的临时切换；Missing 表示选中的账号已经不在本机账号册里或登录已失效。
type replyIdentity struct {
	ID       string
	Name     string
	Avatar   string
	Auth     *AuthManager
	Switched bool
	Missing  bool
}

// currentReplyName 是窗口自己的账号名：会话里没读到昵称时退回账号册里记的
// 那个，两者都没有就是空串（界面上只写「当前账号」）。
func (a *app) currentReplyName() string {
	if a.auth != nil {
		if name := a.auth.username(); name != "" {
			return name
		}
	}
	if a.book != nil && a.accountID != "" {
		if acc := a.book.account(a.accountID); acc != nil {
			return acc.name()
		}
	}
	return ""
}

// replyIdentityFor 解析这张详情页此刻该用哪个账号回复。没切换时就是窗口
// 自己的会话；切换的账号被退出或登录失效时标记 Missing，由调用方决定是
// 拦下发送还是让用户换一个。
func (a *app) replyIdentityFor(d *detailState) replyIdentity {
	me := replyIdentity{Auth: a.auth}
	if a.auth != nil {
		me.ID = a.accountID
		me.Name = a.currentReplyName()
		me.Avatar = a.auth.avatarURL()
	}
	if d == nil || d.replyAsID == "" || a.book == nil {
		return me
	}
	acc := a.book.account(d.replyAsID)
	if acc == nil {
		return replyIdentity{ID: d.replyAsID, Switched: true, Missing: true}
	}
	if acc.ID == me.ID {
		// 选中的就是窗口自己的账号：按没切换处理，不额外借令牌。
		return me
	}
	out := replyIdentity{
		ID:       acc.ID,
		Name:     acc.name(),
		Avatar:   acc.Avatar,
		Auth:     acc.auth,
		Switched: true,
	}
	if acc.auth != nil {
		if name := acc.auth.username(); name != "" {
			out.Name = name
		}
		if avatar := acc.auth.avatarURL(); avatar != "" {
			out.Avatar = avatar
		}
	}
	if acc.auth == nil || !acc.auth.loggedIn() {
		out.Missing = true
	}
	return out
}

// replyClientFor 造这次提交用的客户端：没切换就用窗口自己的，切换了就复制
// 一份换成对方的会话（代理、Base 与测试用的 HTTP 客户端都与窗口共用）。
func (a *app) replyClientFor(identity replyIdentity) *Client {
	if !identity.Switched || identity.Auth == nil || identity.Auth == a.auth {
		return a.client
	}
	c := *a.client
	c.Auth = identity.Auth
	c.bearer = nil
	return &c
}

// noteReplyCoins 把「评论扣了金币」报给真正扣费的那个账号：自己发就刷新本
// 窗口；借用别的账号发就通知那个账号的窗口（它开着时），不能刷新成本窗口
// 的余额。只在 UI 线程调用。
func (a *app) noteReplyCoins(identity replyIdentity) {
	if !identity.Switched {
		a.coinsChanged(coinEventReply)
		return
	}
	if a.book == nil {
		return
	}
	if acc := a.book.account(identity.ID); acc != nil && acc.app != nil && acc.app != a {
		acc.app.coinsChanged(coinEventReply)
	}
}

// resetReplyIdentity 清掉临时回复身份：离开详情页（关闭、内联跳转或返回）
// 时调用。不弹提示——页面都已经变了，结果写在新的页面上反而多余。
func (a *app) resetReplyIdentity(d *detailState) {
	if d == nil {
		return
	}
	d.replyAsID = ""
	d.identityOpen = false
	d.identityHost = ""
}

// applyReplyIdentity 采用选中的账号（id 为空表示窗口自己的账号），关掉气泡
// 并在操作发生的那扇窗口里报告结果。
func (a *app) applyReplyIdentity(d *detailState, host replySurface, id, name string) {
	if d == nil {
		return
	}
	if id == a.accountID {
		id = ""
	}
	changed := d.replyAsID != id
	d.replyAsID = id
	d.identityOpen = false
	if !changed {
		return
	}
	target := toastMain
	if host == replySurfaceWindow {
		target = toastReply
	}
	if id == "" {
		if name := a.currentReplyName(); name != "" {
			a.toast(target, "已恢复为以 @"+name+" 的身份回复")
		} else {
			a.toast(target, "已恢复为当前账号的回复身份")
		}
		return
	}
	a.toast(target, "已切换为以 @"+name+" 的身份回复，关闭详情页后恢复")
}

// toggleReplyIdentityPicker 开关身份气泡：在另一扇窗口里点开时把气泡挪过去，
// 而不是同时显示两个。
func (a *app) toggleReplyIdentityPicker(d *detailState, host replySurface) {
	if d == nil {
		return
	}
	if d.identityOpen && d.identityHost == host {
		d.identityOpen = false
		return
	}
	d.identityOpen = true
	d.identityHost = host
}

// replyIdentityBadge 是评论框下方那枚身份标签：多账号时显示「当前账号 @谁」，
// 临时换过账号后显示「回复身份 @谁」。点开气泡可以换账号。
//
// 它是详情页里唯一说明「这条评论以谁的身份发出」的地方，所以只在这扇窗口
// 自己的身份与详情页状态都可见时画（单账号、未登录时不画）。
func (a *app) replyIdentityBadge(c *ui.Context, d *detailState, host replySurface) ui.Element {
	if d == nil {
		return ui.Element{}
	}
	if d.replyAsID == "" && (!a.multiAccount() || !a.auth.loggedIn()) {
		return ui.Element{}
	}
	identity := a.replyIdentityFor(d)
	// 另一个账号的头像第一次出现时先取一次：没到之前 avatarView 画昵称首字母。
	a.prefetchAvatars(identity.Avatar)
	t := c.Theme()
	bg := t.Accent.Mix(t.Background, 0.88)
	border := t.Accent.Mix(t.Background, 0.55)
	ink := t.Accent
	if identity.Switched {
		// 换成别人的令牌是这一页里的特殊状态：底色再深一档，提醒它不是窗口
		// 的日常身份。
		bg = t.Accent.Mix(t.Background, 0.8)
		border = t.Accent.Mix(t.Background, 0.3)
	}
	if identity.Missing {
		bg = t.Danger.Mix(t.Background, 0.9)
		border = t.Danger.Mix(t.Background, 0.5)
		ink = t.Danger
	}
	badge := ui.ButtonBase(c).Gap(5).AlignItems(ui.Center).Padding(2, 7).
		Radius(999).Shrink(0).Background(bg).Border(1, border)
	badge.Children(func() {
		avatarView(c, identity.Name, a.avatarBitmap(identity.Avatar), userAvatarSize(10))
		text := "当前账号"
		if identity.Name != "" {
			text += " @" + identity.Name
		}
		if identity.Switched {
			if identity.Name == "" {
				text = "回复身份已失效"
			} else {
				text = "回复身份 @" + identity.Name
			}
		}
		ui.Text(c, text).FontSize(11).Bold().TextColor(ink).SingleLine().
			MaxWidth(accountBadgeNameMaxWidth).MinWidth(0)
		if identity.Missing && identity.Name != "" {
			ui.Text(c, "登录已失效").FontSize(10).TextColor(t.Danger).Shrink(0)
		}
		ui.Icon(c, swapAccountIcon).Size(12, 12).TextColor(ink).Shrink(0)
	})
	me := a.currentReplyName()
	switch {
	case identity.Missing:
		badge.Label("回复身份已失效").Tooltip(
			"选中的回复账号已经不可用了；点一下换回当前账号或其他账号")
	case identity.Switched:
		tip := "这篇帖子的评论会以 @" + identity.Name + " 的身份发送；" +
			"只对这一篇生效，关闭详情页后恢复"
		if me != "" {
			tip += "为 @" + me
		}
		tip += "。点一下可以换回来。"
		badge.Label("回复身份 " + identity.Name).Tooltip(tip)
	case identity.Name != "":
		badge.Label("当前账号 " + identity.Name).Tooltip(
			"点一下换个账号来回复这篇帖子；只对这一篇生效，关闭详情页后恢复")
	default:
		badge.Label("当前账号").Tooltip(
			"点一下换个账号来回复这篇帖子；只对这一篇生效，关闭详情页后恢复")
	}
	badge.OnClick(func() { a.toggleReplyIdentityPicker(d, host) })
	if d.identityOpen && d.identityHost == host {
		ui.Popover(c, badge, &d.identityOpen, func() {
			a.replyIdentityPanel(c, d, host)
		})
	}
	return badge
}

// replyIdentityOption 是气泡里的一行：当前账号（Current）或本机另一个账号。
type replyIdentityOption struct {
	ID         string
	Name       string
	Avatar     string
	Current    bool
	NeedsLogin bool
}

// replyIdentityPanel 是身份气泡的内容：当前账号、本机其他账号，以及「只对
// 这一篇生效」的说明。点一行就换过去，结果用窗口内 toast 报告。
func (a *app) replyIdentityPanel(c *ui.Context, d *detailState, host replySurface) {
	t := c.Theme()
	ui.Column(c).Width(272).Gap(6).Children(func() {
		ui.Text(c, "用哪个账号回复").FontSize(metaFontSize).TextColor(t.TextMuted)
		current := replyIdentityOption{Current: true, Name: a.currentReplyName()}
		if a.auth != nil {
			current.Avatar = a.auth.avatarURL()
		}
		a.replyIdentityRow(c, d, host, current)
		if a.book != nil {
			for _, acc := range a.book.accountsView() {
				if acc.ID == a.accountID {
					continue
				}
				a.replyIdentityRow(c, d, host, replyIdentityOption{
					ID:         acc.ID,
					Name:       acc.Name,
					Avatar:     acc.Avatar,
					NeedsLogin: acc.NeedsLogin,
				})
			}
		}
		ui.Divider(c)
		ui.Text(c, "只对这篇帖子的评论生效；关闭详情页后恢复为窗口自己的账号。").
			FontSize(10).TextColor(t.TextMuted).MaxLines(3)
		a.smallAction(c, "管理账号", false, func() {
			d.identityOpen = false
			a.showAccounts()
		})
	})
}

// replyIdentityRow 是气泡里的一行账号：头像、昵称与当前使用状态；登录已失效
// 的账号仍然列出来（它还在账号册里），点一下提示先重新登录。
func (a *app) replyIdentityRow(c *ui.Context, d *detailState, host replySurface, opt replyIdentityOption) {
	t := c.Theme()
	active := (opt.Current && d.replyAsID == "") || (!opt.Current && d.replyAsID == opt.ID)
	a.prefetchAvatars(opt.Avatar)
	row := ui.ButtonBase(c).FillWidth().Padding(4, 8).Radius(8).Gap(8).
		AlignItems(ui.Center).Background(ui.Transparent)
	if active {
		row.Background(t.Accent.Mix(t.Background, 0.9)).
			Border(1, t.Accent.Mix(t.Background, 0.55))
	} else if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		avatarView(c, opt.Name, a.avatarBitmap(opt.Avatar), 22)
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			text := "@" + opt.Name
			if opt.Current {
				text = "当前账号"
				if opt.Name != "" {
					text += " @" + opt.Name
				}
			}
			ui.Text(c, text).FontSize(12).TextColor(t.Text).SingleLine().
				MaxWidth(accountBadgeNameMaxWidth)
			if opt.NeedsLogin {
				ui.Text(c, "登录已失效，需要重新授权").FontSize(10).
					TextColor(t.Danger).SingleLine()
			}
		})
		if active {
			ui.Text(c, "使用中").FontSize(10).TextColor(t.Accent).Shrink(0)
		}
	})

	label := "以 " + opt.Name + " 的身份回复"
	if opt.Current {
		label = "切回当前账号"
		if opt.Name != "" {
			label += " " + opt.Name
		}
	}
	if opt.NeedsLogin {
		label += "（登录已失效）"
	}
	if active {
		label += "（使用中）"
	}
	row.Label(label)
	switch {
	case opt.NeedsLogin:
		row.Tooltip("这个账号的登录已失效，先在账号面板重新登录后再切换")
	case opt.Current:
		row.Tooltip("回到窗口自己的账号；临时换用的回复身份关闭详情页后也会回到这里")
	default:
		row.Tooltip("这篇帖子的评论改以 @" + opt.Name + " 的身份发送；关闭详情页后恢复")
	}
	row.OnClick(func() {
		d.identityOpen = false
		if opt.NeedsLogin {
			target := toastMain
			if host == replySurfaceWindow {
				target = toastReply
			}
			a.toast(target, "「"+opt.Name+"」的登录已失效，先在账号面板重新登录后再切换")
			return
		}
		a.applyReplyIdentity(d, host, opt.ID, opt.Name)
	})
}
