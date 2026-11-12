package main

import (
	"strings"
	"time"
)

// 离线缓存的分组与账号作用域。
//
// 金币与阅读记录是个人数据：换一个账号登录时不能把上一个账号的余额、
// 资产曲线或阅读历史拿出来。作用域按服务端返回的昵称区分；名字还没拿到
// （刚登录、身份接口没回来）时宁可不落盘，也不写入一个会被下个账号复用的
// 公共键。未登录时阅读记录单独放在 anonymous 下，仍可在本机回看。
const (
	coinCachePrefix    = "coin:"
	readingCachePrefix = "reading:"
	detailCachePrefix  = "detail:"

	anonymousScope = "anonymous"
	sessionScope   = "session" // 已登录但还没有昵称：只在内存里记，不落盘
)

// accountScope 返回当前个人缓存的作用域。
func (a *app) accountScope() string {
	if a.auth == nil || !a.auth.loggedIn() {
		return anonymousScope
	}
	name := strings.ToLower(strings.TrimSpace(a.auth.username()))
	if name == "" {
		return sessionScope
	}
	return "user:" + name
}

// persistableScope 返回可以写入磁盘的作用域；sessionScope 返回空，表示
// 这次的数据只留在内存里。金币缓存只属于登录账号，匿名时不落盘。
func (a *app) persistableScope() string {
	scope := a.accountScope()
	if scope == sessionScope {
		return ""
	}
	return scope
}

func (a *app) coinCacheKey() string {
	if a.auth == nil || !a.auth.loggedIn() {
		return ""
	}
	scope := a.persistableScope()
	if scope == "" || scope == anonymousScope {
		return ""
	}
	return coinCachePrefix + scope
}

func (a *app) readingCacheKey() string {
	scope := a.persistableScope()
	if scope == "" {
		return ""
	}
	return readingCachePrefix + scope
}

// formatCacheTime 是缓存时间在界面上的统一写法。秒没有意义，统一到分钟。
func formatCacheTime(t time.Time) string {
	if t.IsZero() {
		return "未知时间"
	}
	return t.Local().Format("2006-01-02 15:04")
}
