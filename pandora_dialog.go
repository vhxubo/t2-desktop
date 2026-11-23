package main

import (
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
)

// 潘多拉抽卡界面：不再单独开 mygo 窗口，入口在主窗口标题栏，点开后是一层
// Lobe Modal。卡片只展示当前这一张结果；抽第一张后旁边出现重置图标，点它
// 才抽第二张，不点就停在结果上。数据层与动作在 pandora.go，卡片在
// pandora_ui.go。
//
// Modal 画在主窗口的 dialogs 里，随主窗口关掉而消失，不必再维护一个独立
// 窗口的显示状态。

const pandoraModalWidth = 420

// pandoraIcon 是礼物盒：标题栏入口与卡片都用同一个图形。
var pandoraIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 12v9H4v-9"/><path d="M2 7h20v5H2z"/><path d="M12 7v14"/><path d="M12 7H7.5a2.5 2.5 0 0 1 0-5C11 2 12 7 12 7z"/><path d="M12 7h4.5a2.5 2.5 0 0 0 0-5C13 2 12 7 12 7z"/></svg>`))

/* ------------------------------------------------------------------
    入口与弹窗
--------------------------------------------------------------- */

// showPandora 是潘多拉抽卡弹窗的全局入口：任何窗口、任何 goroutine 都能调用。
// 工作挪到主线程；弹窗已经开着时只把主窗口带到前台。
func (a *app) showPandora() {
	if a.onPandoraOpen != nil {
		a.onPandoraOpen()
	}
	a.publish(a.openPandoraModal)
}

// openPandoraModal 在主线程打开弹窗。打开时补齐今日状态，第一帧就有可以抽
// 的卡片，而不是先看一会儿空壳。
func (a *app) openPandoraModal() {
	if !a.auth.loggedIn() {
		a.requestLogin("登录 2Libra 后才能打开潘多拉盲盒。")
		return
	}
	if a.pandora == nil {
		a.pandora = newPandoraState()
	}
	a.pandora.owner = a.auth.username()
	a.pandora.open = true
	a.ensurePandoraLoaded()
	// headless 测试没有主循环：碰 mygo 的窗口会等一个不存在的处理者，
	// 所以只把状态准备好，测试直接渲染 dialogs。
	if a.win == nil {
		return
	}
	// 从金币窗口点进来时主窗口可能关着：弹窗画在主窗口里，得先把那扇
	// 真正的主窗口拉起来（raiseMainWindow 会处理「只关窗口」的情况）。
	a.raiseMainWindow()
	if a.win != nil {
		a.win.Invalidate()
	}
}

// closePandora 关掉抽卡弹窗：退出登录或主窗口收尾时调用，别把上一个账号的
// 抽奖结果留在屏幕上。只在主线程调用。
func (a *app) closePandora() {
	st := a.pandora
	if st == nil || !st.open {
		return
	}
	st.open = false
	if a.win != nil {
		a.win.Invalidate()
	}
}

// pandoraReady 回答标题栏入口要不要亮出「可抽」角标。窗口状态优先，其次
// 是启动时静默读到的入口状态；两者都没有时用金币窗口顺带读到的状态兜底。
func (a *app) pandoraReady() bool {
	if status, ok := pandoraDisplayStatus(a.pandora); ok {
		return status.readyToDraw(pandoraNow())
	}
	if cs := a.coins; cs != nil && cs.pandoraSeen {
		return cs.pandora.CanOpen
	}
	return false
}

/* ------------------------------------------------------------------
    弹窗界面
--------------------------------------------------------------- */

// pandoraDialog 画在主窗口的 dialogs 里：✕、Esc 与点窗外都由 Modal 关窗，
// 关掉后 open 归零，下一帧就不再画。NoFooter 去掉取消 / 确定按钮，抽卡与
// 重抽都在卡片里完成。
func (a *app) pandoraDialog(c *ui.Context) {
	st := a.pandora
	if st == nil || !st.open {
		return
	}
	feedback.Modal(c, &st.open, feedback.ModalProps{
		Title:    "潘多拉",
		Width:    pandoraModalWidth,
		NoFooter: true,
	}, func() {
		a.pandoraCard(c, st)
	})
}
