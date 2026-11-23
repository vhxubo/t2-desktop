package main

import (
	"errors"
	"net/http"
	"strings"

	"github.com/egoist/mygo"
)

// 全局授权链路
//
// 所有前台数据操作（列表、详情、评论发布、消息已读）拿到 API 错误后都先交给
// handleAuthErrorFor：401 与 403 缺权限不当作普通错误展示，而是登记进当前
// 账号唯一的 authGate；更底层的 HTTP 出口还会对 insufficient_scope 做一次
// 全局拦截（见 Client.onAuthError），不依赖每个调用点记得自己接。
// 弹窗画在当前聚焦的窗口里（见 focusedAuthTarget），后台请求从哪扇窗口发出
// 不重要——用户正在看的那一扇才会显示，「前往授权」第一眼就能看到。
// 用户点「前往授权」后走已有的浏览器 + 回调流程：账号窗口用内置浏览器的独立
// 会话（系统浏览器里可能登着另一个账号），没有账号册时用系统浏览器 + 深链。
// 授权成功后应用不会替用户重跑被中断的操作：由用户自己再点一次刚才的操作。
//
// 后台任务（未读数轮询、通知长连接重连）不打开弹窗：它们不是用户主动发起的
// 操作，失败时保持静默；等用户真正打开对应页面时，前台请求会发起授权。

// authNeed 是「当前会话为什么不能继续」：可能是根本没有登录，也可能是令牌
// 缺少服务端要求的权限范围。弹窗只认这一种描述。
type authNeed struct {
	Scopes  []string // 服务端 required_scope；为空表示需要（重新）登录
	Message string   // 弹窗里的说明
}

func loginNeed(message string) authNeed {
	if message == "" {
		message = "这次操作需要登录 2Libra 后才能继续。"
	}
	return authNeed{Message: message}
}

// authNeedFromError 把 API 错误翻译成缺失的授权。permission_denied 是用户级
// 限制，重新授权也解决不了，因此不进入授权流程。
func authNeedFromError(err error) (authNeed, bool) {
	// 刷新令牌被服务端明确拒绝（invalid_grant 等 4xx）也属于授权失效：
	// 网络抖动或 5xx 不算，那种情况保持原样、不清登录态。
	var te *tokenHTTPError
	if errors.As(err, &te) && te.fatal {
		return authNeed{Message: "登录状态已失效，需要重新授权。"}, true
	}
	var ae *apiError
	if !errors.As(err, &ae) {
		return authNeed{}, false
	}
	if ae.Code == "permission_denied" {
		return authNeed{}, false
	}
	switch {
	case ae.Status == http.StatusUnauthorized || ae.Code == "unauthorized":
		return authNeed{Message: "登录状态已失效或尚未登录，请重新授权。"}, true
	case ae.Code == "insufficient_scope":
		return scopeNeed(ae.RequiredScope), true
	case ae.Status == http.StatusForbidden && len(ae.RequiredScope) > 0:
		// 服务端只在缺 scope 时带 required_scope；普通 403 不能当成可授权。
		return scopeNeed(ae.RequiredScope), true
	}
	return authNeed{}, false
}

func scopeNeed(scopes []string) authNeed {
	scopes = cleanScopeList(scopes)
	msg := "当前授权不足，需要重新授权后才能继续。"
	if len(scopes) > 0 {
		msg = "当前授权缺少权限范围：" + strings.Join(scopes, "、") + "。"
	}
	return authNeed{Scopes: scopes, Message: msg}
}

// cleanScopeList 去掉空串与重复项，保持原顺序。
func cleanScopeList(scopes []string) []string {
	seen := make(map[string]struct{}, len(scopes))
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// mergeScopeList 合并两个权限列表，保持首次出现的顺序。
func mergeScopeList(a, b []string) []string {
	return cleanScopeList(append(append([]string(nil), a...), b...))
}

// authFocusedWindow 由测试替换：无头渲染不创建真实 mygo 窗口，拿不到聚焦窗口。
var authFocusedWindow = func() *mygo.Window { return mygo.FocusedWindow() }

// focusedAuthTarget 返回当前聚焦窗口对应的 toastTarget。授权弹窗必须画在
// 用户眼前那一扇窗口——后台请求在 A 窗口失败、用户正在看 B 窗口时，弹在 A
// 里等于没弹。找不到（无头测试，或聚焦的是不画弹窗的窗口）时返回 ok=false，
// 调用方沿用传进来的 owner。
func (a *app) focusedAuthTarget() (toastTarget, bool) {
	w := authFocusedWindow()
	if w == nil {
		// 应用不在前台时没有聚焦窗口；弹在主窗口，用户切回来第一眼就能看到。
		if a.win != nil {
			return toastMain, true
		}
		return 0, false // 无头测试：没有真实窗口
	}
	switch w {
	case a.win:
		return toastMain, true
	case a.notifWin:
		return toastNotifications, true
	case a.settingsWin:
		return toastSettings, true
	case a.profileWin:
		return toastProfile, true
	case a.coinWin:
		return toastCoins, true
	case a.composeWin:
		return toastCompose, true
	case a.userCardWin:
		return toastUserCard, true
	case a.searchWin:
		return toastSearch, true
	case a.replyWin:
		return toastReply, true
	case a.editWin:
		return toastEdit, true
	case a.nodeEditWin:
		return toastNodeEdit, true
	case a.followsWin:
		return toastFollows, true
	case a.viewerWin:
		return toastViewer, true
	case a.netLogWin:
		return toastNetLog, true
	}
	for _, h := range a.detailWindows {
		if h.win == w && !h.removed {
			return h.toast, true
		}
	}
	return 0, false
}

// handleGlobalAuthError 是 Client.onAuthError 的实现：HTTP 出口拿到
// insufficient_scope 就调用它。它运行在发请求的 goroutine 上，状态改动经
// publish 回到 UI 线程后再登记弹窗。
//
// 平台级「需开通」的端点也会回 insufficient_scope，而 required_scope 当前的
// 令牌其实已经全有——这时再授权也拿不到权限，不该让用户白跑一趟浏览器（与
// comments_status.go 的 commentsStatusUngranted 同一条判断）。只有确实缺 scope
// 才弹窗。授权成功后应用不替用户重跑操作，用户需要自己再点一次。
func (a *app) handleGlobalAuthError(ae *apiError) {
	if ae == nil || ae.Code != "insufficient_scope" {
		return
	}
	allGranted := true
	for _, s := range ae.RequiredScope {
		if !a.hasAuthScope(s) {
			allGranted = false
			break
		}
	}
	if allGranted {
		return
	}
	need := scopeNeed(ae.RequiredScope)
	a.publish(func() {
		target, ok := a.focusedAuthTarget()
		if !ok {
			return // 无头测试：没有真实聚焦窗口，交给显式调用点登记
		}
		a.demandAuthorizationFor(target, need)
	})
}

// authGate 是当前唯一的授权弹窗状态。open 只表示弹窗是否可见；owners 在弹窗
// 关闭后仍会保留到回调完成，因此「打开浏览器 → 切回应用 → 回调送达」期间用户
// 不需要再点任何按钮去发起授权。授权成功后只清空这个状态，被中断的操作由用户
// 自己重新触发，应用不代劳。
//
// owners 是发起被中断调用的窗口：弹窗只画在这些窗口里，别的窗口（尤其是
// 主窗口）不跟着弹。同一个账号的多个窗口可以同时缺权限，它们各自显示同一份
// 说明、共用一次授权。
type authGate struct {
	open   bool
	owners map[toastTarget]struct{}
	need   authNeed
}

// wants 报告这次弹窗该不该画在 target 窗口里。
func (g *authGate) wants(target toastTarget) bool {
	if g == nil || g.owners == nil {
		return false
	}
	_, ok := g.owners[target]
	return ok
}

// demandAuthorizationFor 登记一次需要授权的操作。owner 是发起这次操作（也就
// 是缺少权限）的窗口。已有弹窗时合并缺失的 scope、把新窗口加进要显示弹窗的
// 集合：一次授权可以解决同时失败的多条请求。授权本身不自动重跑操作。
func (a *app) demandAuthorizationFor(owner toastTarget, need authNeed) {
	// 全局弹窗只画在用户此刻看得见的窗口：后台请求从哪扇窗口发出不重要，
	// 重要的是弹窗出现在当前聚焦窗口，第一眼就能看到「前往授权」。
	if focused, ok := a.focusedAuthTarget(); ok {
		owner = focused
	}
	// 发起这次操作的窗口已经关掉：操作随窗口一起作废，不弹到别的窗口去。
	// 无头测试没有真实窗口（a.win == nil），由各自的 view 直接渲染，不受此限。
	if owner != toastMain && a.win != nil && a.toastWindow(owner) == nil {
		return
	}
	if need.Message == "" {
		need.Message = "这次操作需要授权后才能继续。"
	}
	if !a.authGate.open {
		a.authGate.open = true
		a.authGate.owners = nil
		a.authGate.need = authNeed{}
	}
	if a.authGate.owners == nil {
		a.authGate.owners = make(map[toastTarget]struct{})
	}
	a.authGate.owners[owner] = struct{}{}
	a.authGate.need.Scopes = mergeScopeList(a.authGate.need.Scopes, need.Scopes)
	// 说明优先保留这次操作自己的原因（比如「登录后才能发动态」）；没有说明时
	// 才退回 scope 汇总。合并后的完整 scope 由弹窗里的「缺少的权限范围」一行
	// 列出，不会被这里的说明吞掉。
	if need.Message != "" {
		a.authGate.need.Message = need.Message
	} else if len(a.authGate.need.Scopes) > 0 {
		a.authGate.need.Message = scopeNeed(a.authGate.need.Scopes).Message
	}
	if a.onAuthPrompt != nil {
		a.onAuthPrompt()
	}
}

// demandAuthorization 是主窗口入口的简写：发起的窗口就是主窗口。
func (a *app) demandAuthorization(need authNeed) {
	a.demandAuthorizationFor(toastMain, need)
}

// handleAuthErrorFor 是窗口内前台调用的统一错误入口，owner 是发起调用的窗口。
// 返回 true 表示这次失败已经由授权流程接管，调用方只负责收起加载状态，不再
// 展示普通错误；授权完成后用户需要自己重新触发操作。
func (a *app) handleAuthErrorFor(owner toastTarget, err error) bool {
	return a.handleAuthErrorWithScopesFor(owner, err, nil)
}

// handleAuthErrorWithScopesFor 同上，但会补上这次操作已知需要的 scope：服务端
// 有时只回 insufficient_scope 而不带 required_scope，不能让授权页漏掉它。
func (a *app) handleAuthErrorWithScopesFor(owner toastTarget, err error, scopes []string) bool {
	need, ok := authNeedFromError(err)
	if !ok {
		return false
	}
	need.Scopes = mergeScopeList(need.Scopes, scopes)
	a.demandAuthorizationFor(owner, need)
	return true
}

// handleAuthError 是主窗口调用的简写。
func (a *app) handleAuthError(err error) bool {
	return a.handleAuthErrorFor(toastMain, err)
}

// requestLoginFor 是「需要登录」的显式窗口入口（私有 tab、错误页、评论框）：
// 和被 API 拒绝一样走同一个弹窗。授权成功后由用户自己重新触发操作。
func (a *app) requestLoginFor(owner toastTarget, message string) {
	a.demandAuthorizationFor(owner, loginNeed(message))
}

// requestLoginWithScopesFor 是「登录 + 这次操作已知需要的 scope」：登录授权
// 一次申请到位，回来不用再补第二次授权。发帖 / 发动态一打开窗口就知道要
// write:topic，登录时一并带上。
func (a *app) requestLoginWithScopesFor(owner toastTarget, message string, scopes []string) {
	need := loginNeed(message)
	need.Scopes = cleanScopeList(scopes)
	a.demandAuthorizationFor(owner, need)
}

// requestLogin 是主窗口调用的简写。
func (a *app) requestLogin(message string) {
	a.requestLoginFor(toastMain, message)
}

// authorizeGate 是弹窗里的「前往授权」：先关掉弹窗，再按缺失的 scope（叠加
// 默认范围）打开授权页。账号窗口走账号册的内置浏览器，未登录窗口走系统浏览器
// （见 startLoginWithScopes）。授权成功后应用不重跑操作，用户需要自己再点一次。
func (a *app) authorizeGate() {
	scopes := append([]string(nil), a.authGate.need.Scopes...)
	a.authGate.open = false
	// 未登录窗口里的「前往授权」其实就是添加一个账号：授权回来之后会给那个
	// 账号开一个自己的窗口，这个匿名窗口随之关掉。这次操作已知需要的 scope
	// 要带过去，否则新账号登录回来仍缺权限，还得再授权一次。
	if a.book != nil && a.accountID == "" {
		if a.auth != nil {
			scopes = mergeScopeList(a.auth.knownScopesList(), scopes)
		}
		a.cancelAuthGate("")
		a.book.startAddAccountScopes(scopes)
		return
	}
	a.startLoginWithScopes(scopes)
}

// cancelAuthGate 放弃授权：清空整个授权状态。
func (a *app) cancelAuthGate(reason string) {
	a.authGate = authGate{}
	if reason == "" {
		return
	}
	a.loginMsg = reason
	a.status = reason
}

// authGateWindowClosed 在某个窗口关掉时把它从授权弹窗里摘掉：别的窗口缺权限
// 的那一份继续保留。最后一个发起窗口也关掉时整次授权作废。只在 UI 线程调用。
func (a *app) authGateWindowClosed(owner toastTarget) {
	if !a.authGate.open && len(a.authGate.owners) == 0 {
		return
	}
	if a.authGate.owners != nil {
		delete(a.authGate.owners, owner)
	}
	if len(a.authGate.owners) > 0 {
		return
	}
	a.authGate = authGate{}
}

// pendingAuthScopes 是登录回退页重新发起授权时要带上的额外 scope。
func (a *app) pendingAuthScopes() []string {
	return append([]string(nil), a.authGate.need.Scopes...)
}

// hasPendingAuthorization 表示还有一次授权在进行中（弹窗可能已经关闭，用户
// 正在浏览器里）。授权成功后应用不代跑操作，这里只用于放弃授权时清理状态。
func (a *app) hasPendingAuthorization() bool {
	return a.authGate.open || len(a.authGate.owners) > 0
}
