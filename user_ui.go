package main

import (
	"math"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/display"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 全应用统一的用户标识：昵称旁永远画头像，头像边长按昵称字号换算，
// 字号变大时头像同步变大。标题栏、列表行、评论、通知与帖子详情的作者行
// 都走 userTag，不再各自摆一个固定尺寸的 Avatar。
//
// 头像位图按地址缓存在全局图片缓存里（见 image_cache.go），并在数据到达时
// 预取（见 prefetchAvatars）：渲染时只读缓存，没加载完就先画生成的图案，
// 避免在每一帧里发起网络请求。

// userAvatarScale 是头像边长相对昵称字号的倍数。昵称的行高大约为字号的
// 1.4–1.5 倍，头像取 1.5 倍时与昵称视觉齐平：12px 昵称配 18px 头像，
// 14px 昵称配 21px 头像，11px 的列表副标题配 17px 头像。
const userAvatarScale float32 = 1.5

// userAvatarMinSize 是头像的最小边长：再小的字号也不至于把头像压成看不清
// 的小点。
const userAvatarMinSize float32 = 14

// userHoverPadX 是「头像 + 昵称」悬浮块左右常驻的内边距，让圆角底色和阴影
// 不紧贴文字；只加水平方向，免得撑高详情评论头部那一行固定的高度。
const userHoverPadX float32 = 6

// userAvatarSize 返回昵称字号对应的头像边长。结果向上取整到整数 DIP：
// 圆形头像与半透明边缘在 0.5px 尺寸下容易被抗锯齿吃掉一圈，看起来就像
// 偏移或被截断。
func userAvatarSize(fontSize float32) float32 {
	if fontSize <= 0 {
		fontSize = 12
	}
	size := float32(math.Ceil(float64(fontSize * userAvatarScale)))
	if size < userAvatarMinSize {
		return userAvatarMinSize
	}
	return size
}

// userTagSpec 描述一处「头像 + 昵称 + 佩戴徽章」。Name 是昵称，也是头像占位
// 与无障碍名称的来源；Label 是可选的显示文本（例如「@jimmy」），空时直接
// 显示昵称。Size 是昵称字号，头像尺寸由它换算。Badges 只能来自已经拿到的
// 用户对象（author.equipped_badges）；没有就是空，不为了补徽章单独请求。
type userTagSpec struct {
	Name     string
	Avatar   string
	Label    string
	Size     float32
	Bold     bool
	Color    ui.Color
	MaxLines int
	Badges   []badgeInfo
	// Pro 为 true 时在头像右下角叠加官网同款 Pro 图标，替代过去的「PRO」文字。
	Pro bool
	// Click 覆盖点击行为。默认点开这个用户的资料卡（见 user_card.go）；
	// 标题栏的账号区传自己的动作，点它打开「我的」而不是资料卡。
	Click func()
	// UserMenu 不为 nil 时给「头像 + 昵称」这一块挂右键菜单。收到的是本次
	// 构建出的元素，必须当帧使用（菜单要拿它做锚点）；userTag 全应用通用，
	// 只有详情页的作者与评论头像会传它。
	UserMenu func(identity ui.Element)
}

// userTag 画一个带头像与佩戴徽章的用户标识。Color 为零值（透明）时沿用主题
// 正文色；MaxLines 为 0 / 1 时单行显示，更大时按指定行数折行（通知标题这类
// 较长的句子仍能换行）。徽章只画 u.Badges 里已有的，绝不在这里补请求。
//
// 整块可点：OnClick 会把这一行标成可点击元素，它是最内层的可交互元素，所以
// 点昵称打开资料卡时不会连带触发外面那一行（列表行、通知卡片）的点击。
// 这里仍然用普通 Row 而不是 ButtonBase：ButtonBase 会把头像并进按钮的无障碍
// 文案、并给整块加内边距，前者让头像不再是可命名的图片节点，后者会挪动所有
// 用到 userTag 的版式。
func (a *app) userTag(c *ui.Context, u userTagSpec) ui.Element {
	if u.Name == "" {
		return ui.Element{}
	}
	size := u.Size
	if size <= 0 {
		size = 12
	}
	avatar := userAvatarSize(size)
	gap := avatar * 0.3
	if gap < 4 {
		gap = 4
	}
	// 头像与昵称之间保留原来的呼吸感；徽章单独成组，紧贴昵称（2px），
	// 不给徽章左右留大块空白。
	tk := lobetheme.TokensOf(c)
	row := ui.Row(c).Gap(2).AlignItems(ui.Center).Cursor(ui.CursorPointer)
	row.Children(func() {
		// 头像 + 昵称是右键菜单的锚点：以前靠「查看 @xx 的资料」tooltip
		// 提示可点，现在悬浮时铺一层圆角背景加柔和阴影代替，不画描边。
		// 内边距常驻，悬浮只换底色与阴影，版式不会跟着跳动。
		identity := ui.Row(c).Gap(gap).AlignItems(ui.Center).
			Radius(tk.BorderRadiusSM).PaddingX(userHoverPadX)
		if identity.Hovered() {
			identity.Background(tk.FillSecondary)
			// Lobe 的 BoxShadowTertiary 两层阴影，但不走 tk.Elevate：
			// 它在阴影外还会补一圈 1px 描边，那正是要去掉的边框。
			alpha1, alpha2 := float32(0.04), float32(0.02)
			if tk.Dark {
				alpha1, alpha2 = 0.06, 0.03
			}
			identity.Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, alpha1)).
				Shadow(0, 2, 4, 0, ui.RGBA(0, 0, 0, alpha2))
		}
		identity.Children(func() {
			a.avatarWithPro(c, u.Name, a.avatarBitmap(u.Avatar), avatar, u.Pro)
			label := u.Label
			if label == "" {
				label = u.Name
			}
			props := display.TextProps{FontSize: size, Rows: 1, Strong: u.Bold}
			if u.MaxLines > 1 {
				props.Rows = u.MaxLines
			}
			text := display.Text(c, label, props)
			if u.Color != ui.Transparent {
				text.TextColor(u.Color)
			}
		})
		if u.UserMenu != nil {
			u.UserMenu(identity)
		}
		// 徽章跟着昵称，数据来自响应里的 equipped_badges，没拿到就什么都不画。
		if len(u.Badges) > 0 {
			ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
				a.authorBadges(c, u.Badges)
			})
		}
	})
	click := u.Click
	if click == nil {
		click = func() { a.showUserCard(u.Name) }
	}
	row.OnClick(click)
	return row
}

// avatarView 画一个头像。有图片时按头像框的实际尺寸等比缩放后完整显示
// （Contain：不裁切、不偏移，框内的空白由底色补上）；没有图片时画按昵称
// 生成的图案（见 identicon.go）。
//
// 不能直接用 ui.Avatar：它按当前主题字号的 2.25 倍创建图片子元素，外部再
// 用 Size 把头像框改小时，图片仍按主题尺寸绘制，被圆圈裁成中间一小块，
// 看起来就是头像被放大、偏移和截断。这里图片与框用同一个 size。
func avatarView(c *ui.Context, name string, bmp *ui.Bitmap, size float32) ui.Element {
	if size <= 0 {
		size = userAvatarMinSize
	}
	art := identiconFor(name, size)
	box := ui.Box(c).Size(size, size).Radius(size / 2).Clip().Shrink(0).Center().
		Background(art.bg).Role(ui.RoleImage).Label(name)
	// 图案画在背景之上、图片之下；元素自己的 Clip 只裁子元素，所以这里
	// 按同一个圆形再裁一次，图形不会溢出到圆外。
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		if bmp != nil {
			return
		}
		p.Clip(r, r.W/2, func() {
			art.paint(p, r.X, r.Y)
		})
	})
	box.Children(func() {
		if bmp != nil {
			ui.Image(c, bmp).Fit(ui.Contain).Size(size, size)
		}
	})
	return box
}

// avatarInitials 取昵称首尾词的首字母，与 ui.Avatar 的占位规则一致。头像
// 不再用它（见 identicon.go），只剩徽章图标没加载时画徽章名的首字。
func avatarInitials(name string) string {
	var first, last rune
	inWord := false
	for _, r := range name {
		letter := unicode.IsLetter(r) || unicode.IsDigit(r)
		if letter && !inWord {
			if first == 0 {
				first = unicode.ToUpper(r)
			} else {
				last = unicode.ToUpper(r)
			}
		}
		inWord = letter
	}
	switch {
	case first == 0:
		return ""
	case last == 0:
		return string(first)
	}
	return string([]rune{first, last})
}

// avatarHSL 把 HSL（h 度、s/l 0–1）换成 RGB。头像图案的底色与图形都用它
// 从色相算出来，见 identicon.go。
func avatarHSL(h, s, l float64) ui.Color {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g = c, x
	case h < 120:
		r, g = x, c
	case h < 180:
		g, b = c, x
	case h < 240:
		g, b = x, c
	case h < 300:
		r, b = x, c
	default:
		r, b = c, x
	}
	return ui.RGB(uint8((r+m)*255), uint8((g+m)*255), uint8((b+m)*255))
}

// normalizeAvatarURL 把服务端可能给出的相对头像地址补成绝对地址：协议相对
// 的 //host 补 https:，站内绝对路径按媒体域名补全，与表情图片同一套规则。
// 其余地址原样返回，交给调用方用 http(s) 校验拦掉。
func normalizeAvatarURL(raw string) string {
	raw = strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(raw, "//"):
		return "https:" + raw
	case strings.HasPrefix(raw, "/"):
		return "https://r2.2libra.com" + raw
	}
	return raw
}

// avatarBitmap 只读缓存，不发起下载：头像由数据到达时的 prefetchAvatars
// 预取，还没到就先由 avatarView 画生成的图案。
func (a *app) avatarBitmap(raw string) *ui.Bitmap {
	raw = normalizeAvatarURL(raw)
	if raw == "" {
		return nil
	}
	return a.images.bitmap(raw)
}

// prefetchAvatars 为头像地址启动一次后台下载。头像属于长期缓存：换头像要
// 花金币、内容几乎不变，下载一次就长期留在本机，不参与定期清理。同一个
// 地址只下一次：无论成功、失败还是仍在下载都记在缓存里，失败后不再重试，
// 避免每帧重发注定失败的请求。
func (a *app) prefetchAvatars(urls ...string) {
	if !a.autoLoad {
		return // headless 测试不碰网络
	}
	for _, raw := range urls {
		raw = normalizeAvatarURL(raw)
		if raw == "" {
			continue
		}
		a.prefetchImage(raw, imageCacheStable)
	}
}

// prefetchReplyAvatars 预取一页评论作者的头像。
func (a *app) prefetchReplyAvatars(replies []replyItem) {
	urls := make([]string, 0, len(replies))
	for _, r := range replies {
		urls = append(urls, r.Avatar)
	}
	a.prefetchAvatars(urls...)
}

// prefetchItemAvatars 预取列表行里会展示的用户头像：动态 / 最新评论行的
// 作者与被回复人、普通行的作者，以及通知的操作者。
func (a *app) prefetchItemAvatars(items []Item) {
	urls := make([]string, 0, len(items)*2)
	for _, it := range items {
		if it.Extra != nil {
			urls = append(urls, it.Extra.AuthorAvatar, it.Extra.ReplyToAvatar)
		}
		urls = append(urls, it.AuthorAvatar)
		if it.Notif != nil {
			v := notificationViewOf(it.Notif)
			urls = append(urls, v.ActorAvatar)
			for _, u := range v.Reaction.People {
				urls = append(urls, u.Avatar)
			}
		}
	}
	a.prefetchAvatars(urls...)
}

// prefetchCoinTransactions 预取金币流水里打赏人等来源用户的头像。
func (a *app) prefetchCoinTransactions(txs []coinTransaction) {
	urls := make([]string, 0, len(txs))
	for _, tx := range txs {
		urls = append(urls, tx.FromAvatar)
	}
	a.prefetchAvatars(urls...)
}
