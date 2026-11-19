package main

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobeoverlay "github.com/lobehub/lobe-mygo/pkg/overlay"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 命令面板：标题栏输入框点击即展开，默认在「命令」模式，列出全部命令；切到
// 「搜索」模式才是原来的搜索窗口入口。输入框用 mygo 的 SearchField，自带一键
// 清空按钮（非空时出现）。命令本身不重复实现动作，只是调用已有的入口。
//
// 输入框的文字存在 palette.text：命令模式下它是过滤词，搜索模式下它是关键词。
// 回车成功后文字交给搜索窗口（a.search.q）并就地清空；切回空输入时也自动回到
// 默认的命令模式。

const (
	paletteWidth     float32 = 360 // 下拉面板宽度，略宽于标题栏搜索框
	paletteMaxHeight float32 = 360 // 列表最高，超出就滚动，不顶到内容区
)

const (
	paletteModeCommands = iota
	paletteModeSearch
)

var paletteModeLabels = []string{"命令", "搜索"}

// paletteCommand 是面板里的一条命令。keywords 是拼音 / 英文 / 别名，让中文
// 标题也能被拉丁字母搜到（「发帖」打 fa 或 post 都能命中）。
type paletteCommand struct {
	id       string
	title    string
	hint     string
	keywords string
	icon     *ui.SVG
	run      func()
}

// paletteState 是命令面板跨帧保存的状态：面板是否展开、当前高亮哪一项、当前
// 处在哪种模式，以及输入框的文字。文字与搜索窗口的 q 分开存：回车成功后这边
// 清空，搜索词留在搜索窗口里，不会跟着一起被清掉。
type paletteState struct {
	open         bool
	selected     int
	mode         int
	text         string    // 标题栏输入框的文字
	wasFocused   bool      // 上一帧输入框是否聚焦：聚焦时展开，失焦时收起
	suppressOpen bool      // 面板刚因失焦/外部点击关掉，忽略随后焦点回到锚点造成的重开
	input        ui.Handle // 输入框身份，切模式后把焦点还给它，好接着输入
}

// paletteCommands 列全部命令。顺序就是没有过滤词时的展示顺序：先页面导航，
// 再常用动作，最后按登录态给「退出登录」或「登录」。
func (a *app) paletteCommands() []paletteCommand {
	nav := []struct {
		label string
		kw    string
		icon  *ui.SVG
		idx   int
	}{
		{"首页", "home shouye index", lucide.Home(), 0},
		{"今日热议", "hot remen today", lucide.Flame(), 1},
		{"近期热议", "recent zuijin", lucide.Clock(), 2},
		{"新发表", "new xin fabiao", lucide.PenLine(), 3},
		{"最新评论", "comments pinglun latest", lucide.MessagesSquare(), 4},
		{"社区讨论", "community shequ", lucide.Activity(), 5},
		{"动态", "follows dongtai timeline", lucide.UsersRound(), 6},
	}
	cmds := make([]paletteCommand, 0, len(nav)+16)
	for _, n := range nav {
		idx := n.idx
		cmds = append(cmds, paletteCommand{
			id: "tab-" + strconv.Itoa(idx), title: n.label,
			hint: "切换到「" + n.label + "」", keywords: n.kw, icon: n.icon,
			run: func() { a.selectTab(idx) },
		})
	}
	cmds = append(cmds,
		paletteCommand{id: "search", title: "搜索", hint: "打开搜索窗口",
			keywords: "search sousuo", icon: lucide.Search(), run: a.showSearch},
		paletteCommand{id: "post", title: "发帖", hint: "写一篇带标题的帖子",
			keywords: "post fabie tie", icon: lucide.SquarePen(), run: a.showCompose},
		paletteCommand{id: "moment", title: "发短动态", hint: "不发标题直接说点什么",
			keywords: "moment duan dongtai", icon: lucide.PenLine(), run: a.showMomentCompose},
		paletteCommand{id: "notifications", title: "通知", hint: "打开通知窗口",
			keywords: "notice tongzhi message", icon: lucide.Bell(), run: a.showNotifications},
		paletteCommand{id: "profile", title: "我的", hint: "打开个人资料窗口",
			keywords: "me wode profile", icon: lucide.UserRound(), run: a.showProfile},
		paletteCommand{id: "coins", title: "金币", hint: "余额、明细与排行",
			keywords: "coin jinbi money", icon: lucide.Coins(), run: a.showCoins},
		paletteCommand{id: "sign", title: "签到", hint: "提交今日签到领金币",
			keywords: "sign qiandao checkin", icon: lucide.CalendarCheck2(), run: a.signInNow},
		paletteCommand{id: "pandora", title: "盲盒", hint: "打开潘多拉抽卡",
			keywords: "pandora manghe blind", icon: lucide.Gift(), run: a.showPandora},
		paletteCommand{id: "scores", title: "评分榜", hint: "打开评分榜窗口",
			keywords: "score pingfen ranking", icon: lucide.Trophy(), run: a.showScores},
		paletteCommand{id: "follows", title: "关注与粉丝", hint: "查看关注与粉丝名单",
			keywords: "follow guanzhu fans fensi", icon: lucide.UsersRound(),
			run: func() { a.showFollows(0) }},
		paletteCommand{id: "accounts", title: "账号", hint: "打开账号面板切换账号",
			keywords: "account zhanghao switch", icon: lucide.UsersRound(), run: a.showAccounts},
		paletteCommand{id: "settings", title: "设置", hint: "代理、外观与图片缓存",
			keywords: "settings shezhi proxy config", icon: lucide.Settings(), run: a.showSettings},
		paletteCommand{id: "about", title: "关于", hint: "版本、开发者与协议",
			keywords: "about guanyu version", icon: lucide.Info(), run: a.showAbout},
		paletteCommand{id: "netlog", title: "网络日志", hint: "查看应用发出的 HTTP 请求",
			keywords: "network wangluo log request", icon: lucide.Network(), run: a.showNetLog},
		paletteCommand{id: "refresh", title: "刷新", hint: "重新加载当前列表",
			keywords: "refresh shuaxin reload", icon: lucide.RefreshCw(), run: a.reloadCurrent},
		paletteCommand{id: "update", title: "检查更新", hint: "看看有没有新版本",
			keywords: "update gengxin upgrade", icon: lucide.Zap(), run: a.checkForUpdates},
	)
	if a.auth.loggedIn() {
		cmds = append(cmds, paletteCommand{
			id: "logout", title: "退出登录", hint: "退出当前账号",
			keywords: "logout tuichu signout", icon: lucide.LogOut(), run: a.logoutAccount,
		})
	} else {
		cmds = append(cmds, paletteCommand{
			id: "login", title: "登录", hint: "授权登录 2Libra",
			keywords: "login denglu signin", icon: lucide.LogIn(), run: func() { a.startLogin() },
		})
	}
	return cmds
}

// filterPalette 按过滤词筛命令。标题、说明与 keywords 都参与匹配，大小写不敏感；
// 空词返回全量（保持定义时的顺序）。
func filterPalette(cmds []paletteCommand, q string) []paletteCommand {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return cmds
	}
	out := make([]paletteCommand, 0, len(cmds))
	for _, c := range cmds {
		hay := strings.ToLower(c.title + " " + c.hint + " " + c.keywords)
		if strings.Contains(hay, q) {
			out = append(out, c)
		}
	}
	return out
}

// paletteMove 让 ↑ / ↓ 在过滤后的列表里移动高亮，首尾循环。搜索模式没有列表，
// 这些键不做事，交给文本框移动光标。
func (a *app) paletteMove(q string, delta int) {
	if a.palette.mode != paletteModeCommands {
		return
	}
	cmds := filterPalette(a.paletteCommands(), q)
	if len(cmds) == 0 {
		return
	}
	a.palette.selected = (a.palette.selected + delta + len(cmds)) % len(cmds)
}

// paletteRun 处理回车：命令模式执行高亮的命令，搜索模式打开搜索窗口。
func (a *app) paletteRun(q string) bool {
	if a.palette.mode == paletteModeSearch {
		a.commitTitleSearch(q)
		return true
	}
	cmds := filterPalette(a.paletteCommands(), q)
	if len(cmds) == 0 {
		// 没有匹配的命令：把这个词当成关键词，直接进搜索窗口。
		a.commitTitleSearch(q)
		return true
	}
	i := a.palette.selected
	if i < 0 || i >= len(cmds) {
		i = 0
	}
	cmd := cmds[i]
	a.closePalette()
	cmd.run()
	return true
}

// closePalette 收起面板：清空输入、高亮与模式回到默认（命令模式）。
func (a *app) closePalette() {
	a.palette.open = false
	a.palette.text = ""
	a.palette.selected = 0
	a.palette.mode = paletteModeCommands
	// 执行命令后保持收起：焦点还在输入框里也别重新展开（mygo 会把焦点还给
	// 锚点），要再点一次输入框才重新打开。
	a.palette.wasFocused = false
	a.palette.suppressOpen = true
}

// commitTitleSearch 把标题栏输入交给搜索窗口并清空输入框、收起面板。搜索词留
// 在 a.search.q，标题栏这边回到空白，不会连带把搜索结果清掉。
func (a *app) commitTitleSearch(q string) {
	if a.search == nil {
		a.search = newSearchState()
	}
	a.search.q = q
	a.closePalette()
	a.showSearch()
}

// commandPalette 把下拉面板挂在搜索框下方。面板开合由 palette.open 驱动：点输入
// 框展开，执行命令、失焦或点窗外关闭。
func (a *app) commandPalette(c *ui.Context, anchor ui.Element, q string) {
	st := &a.palette
	tk := lobetheme.TokensOf(c)
	cmds := filterPalette(a.paletteCommands(), q)
	if st.selected < 0 {
		st.selected = 0
	}
	if st.selected >= len(cmds) {
		st.selected = len(cmds) - 1
	}

	lobeoverlay.Popover(c, anchor, &st.open, lobeoverlay.PopoverProps{
		Placement: lobeoverlay.Bottom,
	}, func() {
		ui.Column(c).Width(paletteWidth).Gap(2).Children(func() {
			// 顶部两种模式：命令（默认）与搜索。搜索是次要入口，放在切换里。用
			// 普通按钮而不是 Segmented，点完把焦点还给输入框，好接着直接打字。
			ui.Row(c).FillWidth().Padding(2, 4).Gap(4).AlignItems(ui.Center).Children(func() {
				for _, m := range []int{paletteModeCommands, paletteModeSearch} {
					mode := m
					variant := action.Text
					if st.mode == mode {
						variant = action.Primary
					}
					btn := action.Button(c, paletteModeLabels[mode], action.ButtonProps{
						Size: lobetheme.Small, Variant: variant,
					})
					if btn.Clicked() {
						st.mode = mode
						st.selected = 0
						st.input.Focus(c) // 焦点还给输入框，好接着打字
					}
				}
				ui.Spacer(c).Grow(1)
				hint := "↑↓ 选择 · ↵ 执行 · Esc 清空"
				if st.mode == paletteModeSearch {
					hint = "↵ 打开搜索窗口"
				}
				ui.Text(c, hint).FontSize(10).TextColor(tk.TextTertiary)
			})
			if st.mode == paletteModeSearch {
				label := "输入关键词后按回车搜索"
				if strings.TrimSpace(q) != "" {
					label = "按回车搜索「" + strings.TrimSpace(q) + "」"
				}
				ui.Box(c).FillWidth().Padding(14).Center().Children(func() {
					ui.Text(c, label).FontSize(12).TextColor(tk.TextSecondary)
				})
				return
			}
			if len(cmds) == 0 {
				label := "没有匹配的命令"
				if strings.TrimSpace(q) != "" {
					label = "没有匹配的命令，按回车搜索「" + strings.TrimSpace(q) + "」"
				}
				ui.Box(c).FillWidth().Padding(12).Center().Children(func() {
					ui.Text(c, label).FontSize(12).TextColor(tk.TextSecondary)
				})
				return
			}
			ui.Scroll(c).Width(paletteWidth).MaxHeight(paletteMaxHeight).Children(func() {
				for i := range cmds {
					idx := i
					cmd := &cmds[i]
					row := ui.ButtonBase(c).FillWidth().Padding(7, 9).Radius(tk.BorderRadiusSM).
						Gap(9).AlignItems(ui.Center)
					switch {
					case idx == st.selected:
						row.Background(tk.PrimaryBg)
					case row.Hovered():
						row.Background(tk.FillSecondary)
					}
					row.Children(func() {
						ui.Icon(c, cmd.icon).Size(15, 15).TextColor(tk.Primary).Shrink(0)
						ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
							ui.Text(c, cmd.title).FontSize(12).SingleLine().TextColor(tk.Text)
							ui.Text(c, cmd.hint).FontSize(10).SingleLine().TextColor(tk.TextSecondary)
						})
					})
					row.Label("命令 " + cmd.title).Tooltip(cmd.hint)
					row.OnClick(func() {
						a.closePalette()
						cmd.run()
					})
				}
			})
		})
	})
}
