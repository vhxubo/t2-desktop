package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
)

// 窗口内提示。操作的结果（成功或者失败）不再用内联文字占版面，而是在窗口
// 底部弹一条 toast，几秒后自己消失：报错与页面内容分开，页面不会因为一次
// 失败多出一行红字，也不会一直留着。
//
// toast 只能在构建期通过 *ui.Context 发出，而后台结果是在 publish 回到 UI
// 线程时产生的，所以先按窗口排队，等那个窗口下一次重建时再交给 Lobe 的
// feedback.ShowToast。它画在窗口的 overlay 上，也就是内容、悬浮按钮与授权 /
// 询问弹窗之上，所以提示不会被页面里的任何一层挡住。

// toastTarget 是提示该出现在哪个窗口：操作发生在哪个窗口就提示在哪个窗口；
// 那个窗口不在了就退回主窗口，结果总有人看见。主窗口之外的每个窗口的 view
// 都要调一次 drainToasts，认领发给自己的提示。
type toastTarget int

const (
	toastMain          toastTarget = iota // 主窗口：列表、详情、登录页与关闭询问
	toastNotifications                    // 通知窗口
	toastSettings                         // 设置窗口
	toastProfile                          // 「我的」窗口
	toastCoins                            // 金币窗口：签到等金币操作
	toastCompose                          // 发帖窗口
	toastUserCard                         // 用户资料窗口：关注 / 取关
	toastSearch                           // 搜索窗口
	toastPandora                          // 潘多拉弹窗：错误与授权提示（抽卡结果只画在卡片里）
	toastReply                            // 独立写评论窗口：发送结果
	toastEdit                             // 编辑窗口：保存 / 追加附言的结果
	toastNodeEdit                         // 修改节点窗口：提交结果
	toastFollows                          // 关注与粉丝窗口：回关 / 取关
	toastViewer                           // 图片预览窗口：另存为结果
	toastNetLog                           // 网络调试窗口：复制请求数据的结果
	toastAbout                            // 关于窗口
)

// toastNote 是一条等着显示的提示。
type toastNote struct {
	target toastTarget
	text   string
}

// toastWindow 返回提示对应的窗口；主窗口之外的窗口没打开时返回 nil，由
// mainClaimsToasts 让主窗口接手。只在 UI 线程调用。
func (a *app) toastWindow(target toastTarget) *mygo.Window {
	if target >= toastDetailBase {
		return a.toastDetailWindow(target)
	}
	switch target {
	case toastNotifications:
		return a.notifWin
	case toastSettings:
		return a.settingsWin
	case toastProfile:
		return a.profileWin
	case toastCoins:
		return a.coinWin
	case toastCompose:
		return a.composeWin
	case toastUserCard:
		return a.userCardWin
	case toastSearch:
		return a.searchWin
	case toastReply:
		return a.replyWin
	case toastEdit:
		return a.editWin
	case toastNodeEdit:
		return a.nodeEditWin
	case toastFollows:
		return a.followsWin
	case toastViewer:
		return a.viewerWin
	case toastNetLog:
		return a.netLogWin
	case toastAbout:
		return a.aboutWin
	}
	return a.win
}

// toast 记下一条提示，并让将来显示它的那个窗口重画。可能由后台 goroutine
// （经 publish，例如清空缓存后）调用，所以用 toastMu 保护队列；读窗口对象
// 仍假定在 UI 线程附近发生。
func (a *app) toast(target toastTarget, text string) {
	if text == "" {
		return
	}
	a.toastMu.Lock()
	a.toasts = append(a.toasts, toastNote{target: target, text: text})
	a.toastMu.Unlock()
	// 目标窗口开着就只重画它；不在了就由主窗口下一帧显示，规则与
	// mainClaimsToasts 一致。
	if win := a.toastWindow(target); win != nil {
		win.Invalidate()
		return
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// drainToasts 把这一帧归本窗口的提示交给 Lobe 的 feedback.ShowToast，其余
// 留在队列里等自己的窗口。claims 回答「这条提示归不归本窗口」。
// 各窗口的 view 都用 defer 调它：overlay 按建树顺序叠放，feedback.ToastHost
// 必须在弹窗、浮层之后建，toast 才会盖在它们上面（mygo 的 z-index 不改
// overlay 之间的顺序）。
func (a *app) drainToasts(c *ui.Context, claims func(toastTarget) bool) {
	a.toastMu.Lock()
	var show []string
	if len(a.toasts) > 0 {
		kept := a.toasts[:0]
		for _, n := range a.toasts {
			if claims(n.target) {
				show = append(show, n.text)
				continue
			}
			kept = append(kept, n)
		}
		clear(a.toasts[len(kept):])
		a.toasts = kept
	}
	a.toastMu.Unlock()
	for _, text := range show {
		feedback.ShowToast(c, feedback.ToastProps{Title: text})
	}
	// 底部居中，沿用原来 mygo toast 的位置：右下角会压住底部按钮等控件，
	// 弹提示时用户就点不到了。
	feedback.ToastHost(c, feedback.ToastHostProps{Placement: feedback.ToastBottom})
}

// mainClaimsToasts 说主窗口该不该显示这条提示：主窗口自己的当然显示；其它
// 窗口不在了，它们的提示也退到主窗口，免得结果没人看见。
func (a *app) mainClaimsToasts(target toastTarget) bool {
	if target == toastMain {
		return true
	}
	return a.toastWindow(target) == nil
}

// claimsToasts 是通知、设置、「我的」、金币、发帖、图片预览这些附属窗口用的
// 规则：只显示发给自己的提示。
func (a *app) claimsToasts(mine toastTarget) func(toastTarget) bool {
	return func(target toastTarget) bool { return target == mine }
}
