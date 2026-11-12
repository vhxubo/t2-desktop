package main

import (
	"errors"
	"net/http"
	"strings"
)

// 全局授权链路
//
// 所有前台数据操作（列表、详情、评论发布、消息已读）拿到 API 错误后都先交给
// handleAuthError：401 与 403 缺权限不当作普通错误展示，而是登记进唯一的
// authGate 弹窗。用户点「前往授权」后走已有的浏览器 + 深链回调流程；回调被
// 接受时 onAuthChanged 会把登记下来的调用原样重跑，用户不用回到原来的位置
// 再点一次。
//
// 后台任务（未读数轮询、通知长连接重连）不打开弹窗：它们不是用户主动发起的
// 操作，失败时保持静默；等用户真正打开对应页面时，前台请求会发起授权。

// authReplayLimit 是同时登记的被中断调用上限。正常一次只会有一两个；上限
// 只是防止异常情况下无限增长，超出时丢弃最旧的一个。
const authReplayLimit = 16

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

// authGate 是全局唯一的授权弹窗状态。open 只表示弹窗是否可见；replays 在
// 弹窗关闭后仍会保留到回调完成，因此「打开浏览器 → 切回应用 → 回调送达」
// 期间用户不需要再点任何按钮。
type authGate struct {
	open    bool
	need    authNeed
	replays []func()
}

// demandAuthorization 登记一次需要授权的操作。已有弹窗时合并缺失的 scope，
// 并把新调用追加到待重跑列表：一次授权可以解决同时失败的多条请求。
func (a *app) demandAuthorization(need authNeed, replay func()) {
	if need.Message == "" {
		need.Message = "这次操作需要授权后才能继续。"
	}
	if !a.authGate.open {
		a.authGate.open = true
		a.authGate.need = authNeed{}
	}
	a.authGate.need.Scopes = mergeScopeList(a.authGate.need.Scopes, need.Scopes)
	if need.Message != "" {
		a.authGate.need.Message = need.Message
	}
	if replay != nil {
		if len(a.authGate.replays) >= authReplayLimit {
			a.authGate.replays = a.authGate.replays[1:]
		}
		a.authGate.replays = append(a.authGate.replays, replay)
	}
	if a.onAuthPrompt != nil {
		a.onAuthPrompt()
	}
}

// handleAuthError 是所有前台调用的统一错误入口。返回 true 表示这次失败已经
// 由授权流程接管，调用方只负责收起加载状态，不再展示普通错误。
func (a *app) handleAuthError(err error, replay func()) bool {
	need, ok := authNeedFromError(err)
	if !ok {
		return false
	}
	a.demandAuthorization(need, replay)
	return true
}

// requestLogin 是「需要登录」的显式入口（私有 tab、错误页、评论框）：和被
// API 拒绝一样走同一个弹窗，授权成功后重跑 replay。
func (a *app) requestLogin(message string, replay func()) {
	a.demandAuthorization(loginNeed(message), replay)
}

// authorizeGate 是弹窗里的「前往授权」：先关掉弹窗，保留待重跑调用，再按
// 缺失的 scope（叠加默认范围）打开系统浏览器。
func (a *app) authorizeGate() {
	scopes := append([]string(nil), a.authGate.need.Scopes...)
	a.authGate.open = false
	a.startLoginWithScopes(scopes)
}

// cancelAuthGate 放弃授权：待重跑调用一并丢弃，避免以后某次登录成功后突然
// 执行一个早就过期的操作。
func (a *app) cancelAuthGate(reason string) {
	a.authGate = authGate{}
	if reason == "" {
		return
	}
	a.loginMsg = reason
	a.status = reason
}

// replayAuthorizedCalls 在授权成功后按登记顺序重跑被中断的调用，并清空整个
// 授权状态。写操作每次都重新发起，因此统一使用新的 Idempotency-Key。
func (a *app) replayAuthorizedCalls() {
	replays := a.authGate.replays
	a.authGate = authGate{}
	for _, fn := range replays {
		fn()
	}
	if a.onAuthReplay != nil {
		a.onAuthReplay()
	}
}

// pendingAuthScopes 是登录回退页重新发起授权时要带上的额外 scope。
func (a *app) pendingAuthScopes() []string {
	return append([]string(nil), a.authGate.need.Scopes...)
}

// hasPendingAuthorization 表示还有调用在等一次授权（弹窗可能已经关闭，用户
// 正在浏览器里）。
func (a *app) hasPendingAuthorization() bool {
	return len(a.authGate.replays) > 0
}
