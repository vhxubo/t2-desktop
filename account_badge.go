package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/display"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 「当前账号」标签
//
// 多账号时每个账号一个窗口，窗口标题栏里已经有头像与昵称；但发帖窗口的
// 「发布」离标题栏很远，写完之后很容易不确认就点下去。这里给它紧挨着挂一枚
// 标签，写明「当前账号 @昵称」。
//
// 评论不挂在输入栏里：输入框要尽可能宽，账号标签挤在发送按钮左边会吃掉
// 右侧一大截宽度。它改落在详情页底部版权声明那一行的右侧（见
// topicSourceFooter）。那枚标签还要承担「临时换个账号回复」，所以详情页与
// 独立写评论窗口用的是 reply_identity.go 的 replyIdentityBadge；这里只留给
// 发帖窗口的「发布」旁边。
//
// 只有本机挂了一个以上账号时才出现：单账号时标题栏已经说明身份，这枚标签
// 只会白占版面。名字取这个窗口自己的会话（a.auth），不是账号册里记的
// 「上次用的那个」：标签必须和真正发请求的那份令牌是同一个人。
//
// 用强调色的浅底而不是一行灰字：它要紧挨着主按钮、让人在点下去之前看见，
// 读起来又必须是一眼扫完的短句（不再写「以某某的身份发布」那种整句）。

// accountBadgeNameMaxWidth 是标签文字的最大宽度：昵称过长时在标签内截断，
// 不会把「发送」「发布」按钮或底部的版权声明挤出窗口。
const accountBadgeNameMaxWidth = 150

// 多账号时每个账号一扇窗口，但附属窗口（通知、设置、金币、发帖……）的自绘
// 标题栏只有自己的名字：任务栏、Alt+Tab 与屏幕上并排的几扇窗口长得一模一样，
// 不写明是谁的窗口就只能一个个点开看。这里给它们同一枚账号标识，位置也和
// 主窗口的一致 —— 标题栏最右、窗口按钮左侧。
//
// 名字取这个窗口自己的会话（a.auth），不是账号册里记的「上次用的那个」：
// 窗口里发的每个请求、收到的每条消息都来自这份令牌。

// accountWindowTagWidth 是标题栏里账号标识的最大宽度：昵称过长时在标识内
// 截断，不会把窗口标题或右侧的窗口按钮挤出去。
const accountWindowTagWidth = 120

// windowAccountName 是这扇窗口属于哪个账号的展示名：只有多账号时才写，
// 单账号的窗口标题已经说明身份。会话里还没读到昵称时退回账号册里记的那个。
func (a *app) windowAccountName() string {
	if !a.multiAccount() {
		return ""
	}
	if a.auth != nil {
		if name := a.auth.username(); name != "" {
			return name
		}
	}
	if a.book != nil {
		if acc := a.book.account(a.accountID); acc != nil {
			return acc.name()
		}
	}
	return ""
}

// accountWindowTag 把「这扇窗口属于哪个账号」画在标题栏右侧：单账号与未登录
// 窗口不画，免得白占版面。点它打开账号面板，和其它账号入口一致。
func (a *app) accountWindowTag(c *ui.Context) {
	name := a.windowAccountName()
	if name == "" {
		return
	}
	t := c.Theme()
	ui.Spacer(c).Grow(1)
	// 标题栏里只有文字、没有头像，正好用 Lobe 的胶囊 Tag：圆角、内边距与
	// 字号都由组件按主题统一处理，这里只换成应用的品牌色并限制宽度。
	tag := display.Tag(c, "@"+name, display.TagProps{
		Size: lobetheme.Small, Round: true,
	}).Shrink(0).MaxWidth(accountWindowTagWidth + 16).
		Background(t.Accent.Mix(t.Background, 0.88)).
		TextColor(t.Accent).Cursor(ui.CursorPointer)
	tag.Label("账号 " + name)
	lobeoverlay.Tooltip(c, tag, "这扇窗口属于 @"+name+"；点开账号面板可以查看或切换账号",
		lobeoverlay.TooltipProps{})
	tag.OnClick(a.showAccounts)
}

// subWindowTitle 是附属窗口的原生标题：加上账号名，任务栏与 Alt+Tab 里才
// 分得清「通知」「设置」是哪一扇。
func (a *app) subWindowTitle(base string) string {
	if name := a.windowAccountName(); name != "" {
		return base + " · " + name
	}
	return base
}

// multiAccount 报告本机是否挂了不止一个账号。没有账号册（无头测试、旧的
// 单窗口路径）按单账号处理。
func (a *app) multiAccount() bool {
	return a.book != nil && a.book.accountCount() > 1
}

// accountBadge 画「当前账号 @昵称」那一枚标签：发帖窗口里紧挨着「发布」。
// 单账号或未登录时返回零元素，不占版面。详情页与独立写评论窗口的身份标签
// 还要能切换临时回复账号，见 reply_identity.go 的 replyIdentityBadge。
func (a *app) accountBadge(c *ui.Context) ui.Element {
	if !a.multiAccount() || !a.auth.loggedIn() {
		return ui.Element{}
	}
	t := c.Theme()
	name := a.auth.username()
	tint := t.Accent.Mix(t.Background, 0.88)
	badge := ui.Row(c).Gap(5).AlignItems(ui.Center).Padding(2, 7).
		Radius(999).Shrink(0).Background(tint).
		Border(1, t.Accent.Mix(t.Background, 0.55)).
		Cursor(ui.CursorPointer)
	badge.Children(func() {
		avatarView(c, name, a.avatarBitmap(a.auth.avatarURL()), userAvatarSize(10))
		text := "当前账号"
		if name != "" {
			text = "当前账号 @" + name
		}
		display.Text(c, text, display.TextProps{
			FontSize: 11, Strong: true, Rows: 1,
		}).TextColor(t.Accent).MaxWidth(accountBadgeNameMaxWidth).MinWidth(0)
	})
	// 无障碍名与悬停提示说清「这个窗口是谁」，以及去哪儿换账号。
	label := "当前账号"
	tip := "这个窗口登录的是当前账号；点开账号面板可以查看或切换账号"
	if name != "" {
		label = "当前账号 " + name
		tip = "这个窗口登录的是 @" + name + "；点开账号面板可以查看或切换账号"
	}
	badge.Label(label)
	badge.Tooltip(tip)
	badge.OnClick(a.showAccounts)
	return badge
}
