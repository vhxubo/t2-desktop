package main

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
)

// registerBuiltinCommands 注册应用自带的命令。这里只描述「是什么、怎么触发」，
// 不碰面板的绘制；新增功能时在这里加一条，面板、快捷键与设置页会一起出现。
// 各命令的 Run 都复用已有的 showXXX 入口，不另写一套业务逻辑。
func (a *app) registerBuiltinCommands() {
	add := func(def commandDef) { a.commands.register(def) }

	for i, n := range []struct {
		label, kw string
		icon      *ui.SVG
	}{
		{"首页", "home shouye index", lucide.Home()},
		{"今日热议", "hot remen today", lucide.Flame()},
		{"近期热议", "recent zuijin", lucide.Clock()},
		{"新发表", "new xin fabiao", lucide.PenLine()},
		{"最新评论", "comments pinglun latest", lucide.MessagesSquare()},
		{"社区讨论", "community shequ", lucide.Activity()},
		{"动态", "follows dongtai timeline", lucide.UsersRound()},
	} {
		idx := i
		add(commandDef{
			ID: "tab-" + strconv.Itoa(idx), Title: n.label, Category: commandCategoryNavigate,
			Hint: "切换到「" + n.label + "」", Keywords: n.kw, Icon: n.icon,
			Run: func() { a.selectTab(idx) },
		})
	}

	add(commandDef{
		ID: "palette", Title: "命令中心", Category: commandCategoryTools,
		Hint: "展开全部命令", Keywords: "command palette mingling kuaijie",
		Icon: lucide.Zap(), DefaultShortcut: cmdKey(ui.KeyK),
		Run: func() {
			a.palette.open = true
			a.palette.suppressOpen = false
		},
	})
	add(commandDef{
		ID: "search", Title: "搜索", Category: commandCategoryTools,
		Hint: "打开搜索窗口", Keywords: "search sousuo",
		Icon: lucide.Search(), DefaultShortcut: cmdShiftKey(ui.KeyF), Run: a.showSearch,
	})
	add(commandDef{
		ID: "post", Title: "发帖", Category: commandCategoryCreate,
		Hint: "写一篇带标题的帖子", Keywords: "post fabie tie",
		Icon: lucide.SquarePen(), DefaultShortcut: cmdKey(ui.KeyN), Run: a.showCompose,
	})
	add(commandDef{
		ID: "moment", Title: "发短动态", Category: commandCategoryCreate,
		Hint: "不发标题直接说点什么", Keywords: "moment duan dongtai",
		Icon: lucide.PenLine(), DefaultShortcut: cmdShiftKey(ui.KeyM), Run: a.showMomentCompose,
	})
	add(commandDef{
		ID: "drafts", Title: "草稿箱", Category: commandCategoryCreate,
		Hint: "继续写还没发出的草稿", Keywords: "draft cao gao xiang",
		Icon: lucide.FilePen(), DefaultShortcut: cmdShiftKey(ui.KeyD), Run: a.showDrafts,
	})
	add(commandDef{
		ID: "notifications", Title: "通知", Category: commandCategorySocial,
		Hint: "打开通知窗口", Keywords: "notice tongzhi message",
		Icon: lucide.Bell(), DefaultShortcut: cmdShiftKey(ui.KeyN), Run: a.showNotifications,
	})
	add(commandDef{
		ID: "follows", Title: "关注与粉丝", Category: commandCategorySocial,
		Hint: "查看关注与粉丝名单", Keywords: "follow guanzhu fans fensi",
		Icon: lucide.UsersRound(), DefaultShortcut: cmdShiftKey(ui.KeyU),
		Run: func() { a.showFollows(0) },
	})
	add(commandDef{
		ID: "profile", Title: "我的", Category: commandCategoryAccount,
		Hint: "打开个人资料窗口", Keywords: "me wode profile",
		Icon: lucide.UserRound(), DefaultShortcut: cmdShiftKey(ui.KeyP), Run: a.showProfile,
	})
	add(commandDef{
		ID: "badges", Title: "徽章墙", Category: commandCategoryAccount,
		Hint: "查看与佩戴我的徽章", Keywords: "badge huizhang award",
		Icon: lucide.Award(), Run: func() { a.showProfileTab(profileTabBadges) },
	})
	add(commandDef{
		ID: "collections", Title: "我的合集", Category: commandCategoryAccount,
		Hint: "按合集浏览我发过的帖子", Keywords: "collection heji folder",
		Icon: lucide.FolderClosed(), Run: func() {
			if a.profile == nil {
				a.profile = newProfileState()
			}
			a.profile.contentKind = profileContentCollections
			a.showProfileTab(profileTabContent)
		},
	})
	add(commandDef{
		ID: "contentFilter", Title: "内容屏蔽", Category: commandCategoryAccount,
		Hint: "管理不想看到的内容", Keywords: "filter pingbi block shield",
		Icon: lucide.EyeOff(), Run: func() { a.showProfileTab(profileTabFilter) },
	})
	add(commandDef{
		ID: "coins", Title: "金币", Category: commandCategoryAccount,
		Hint: "余额、明细与排行", Keywords: "coin jinbi money",
		Icon: lucide.Coins(), DefaultShortcut: cmdShiftKey(ui.KeyC), Run: a.showCoins,
	})
	add(commandDef{
		ID: "redeem", Title: "兑换记录", Category: commandCategoryAccount,
		Hint: "我兑换过的兑换码", Keywords: "redeem duihuan code ticket",
		Icon: lucide.Ticket(), Run: func() {
			if a.coins == nil {
				a.coins = newCoinState()
			}
			a.coins.view = coinViewRedeem
			a.showCoins()
			a.redrawCoinWindow()
		},
	})
	add(commandDef{
		ID: "sign", Title: "签到", Category: commandCategoryAccount,
		Hint: "提交今日签到领金币", Keywords: "sign qiandao checkin",
		Icon: lucide.CalendarCheck2(), DefaultShortcut: cmdShiftKey(ui.KeyS),
		Run: a.signInNow,
	})
	add(commandDef{
		ID: "pandora", Title: "盲盒", Category: commandCategoryAccount,
		Hint: "打开潘多拉抽卡", Keywords: "pandora manghe blind",
		Icon: lucide.Gift(), DefaultShortcut: cmdShiftKey(ui.KeyB), Run: a.showPandora,
	})
	add(commandDef{
		ID: "scores", Title: "评分榜", Category: commandCategorySocial,
		Hint: "打开评分榜窗口", Keywords: "score pingfen ranking",
		Icon: lucide.Trophy(), DefaultShortcut: cmdShiftKey(ui.KeyR), Run: a.showScores,
	})
	add(commandDef{
		ID: "watchLater", Title: "稍后再看", Category: commandCategoryTools,
		Hint: "打开收藏与浏览历史", Keywords: "watch later shaohou history",
		Icon: lucide.Bookmark(), DefaultShortcut: cmdShiftKey(ui.KeyW), Run: a.showWatchLater,
	})
	add(commandDef{
		ID: "accounts", Title: "账号", Category: commandCategoryAccount,
		Hint: "打开账号面板切换账号", Keywords: "account zhanghao switch",
		Icon: lucide.UsersRound(), DefaultShortcut: cmdShiftKey(ui.KeyA), Run: a.showAccounts,
	})
	add(commandDef{
		ID: "settings", Title: "设置", Category: commandCategoryTools,
		Hint: "代理、外观与图片缓存", Keywords: "settings shezhi proxy config",
		Icon: lucide.Settings(), DefaultShortcut: cmdKey(ui.KeyComma), Run: a.showSettings,
	})
	add(commandDef{
		ID: "shortcutSettings", Title: "快捷键设置", Category: commandCategoryTools,
		Hint: "录制、修改或恢复命令快捷键", Keywords: "shortcut kuaijie keyboard binding",
		Icon: lucide.Keyboard(), DefaultShortcut: cmdShiftKey(ui.KeyK), Run: func() {
			a.settingsTab = settingsTabShortcuts
			a.showSettings()
		},
	})
	add(commandDef{
		ID: "about", Title: "关于", Category: commandCategoryTools,
		Hint: "版本、开发者与协议", Keywords: "about guanyu version",
		Icon: lucide.Info(), Run: a.showAbout,
	})
	add(commandDef{
		ID: "netlog", Title: "网络日志", Category: commandCategoryTools,
		Hint: "查看应用发出的 HTTP 请求", Keywords: "network wangluo log request",
		Icon: lucide.Network(), Run: a.showNetLog,
	})
	add(commandDef{
		ID: "refresh", Title: "刷新", Category: commandCategoryTools,
		Hint: "重新加载当前列表", Keywords: "refresh shuaxin reload",
		Icon: lucide.RefreshCw(), DefaultShortcut: cmdKey(ui.KeyR), Run: a.reloadCurrent,
	})
	add(commandDef{
		ID: "update", Title: "检查更新", Category: commandCategoryTools,
		Hint: "看看有没有新版本", Keywords: "update gengxin upgrade",
		Icon: lucide.Zap(), Run: a.checkForUpdates,
	})
	add(commandDef{
		ID: "login", Title: "登录", Category: commandCategoryAccount,
		Hint: "授权登录 2Libra", Keywords: "login denglu signin",
		Icon: lucide.LogIn(), Run: func() { a.startLogin() },
		Available: func() bool { return a.auth == nil || !a.auth.loggedIn() },
	})
	add(commandDef{
		ID: "signup", Title: "注册账号", Category: commandCategoryAccount,
		Hint: "在浏览器里注册 2Libra", Keywords: "signup zhuce register",
		Icon: lucide.UserPlus(), Run: a.openSignup,
		Available: func() bool { return a.auth == nil || !a.auth.loggedIn() },
	})
	add(commandDef{
		ID: "logout", Title: "退出登录", Category: commandCategoryAccount,
		Hint: "退出当前账号", Keywords: "logout tuichu signout",
		Icon: lucide.LogOut(), Run: a.logoutAccount,
		Available: func() bool { return a.auth != nil && a.auth.loggedIn() },
	})
}
