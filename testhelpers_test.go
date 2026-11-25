package main

import "testing"

// testBook 建一份隔离的进程级单例：令牌走文件存储（见 auth_store_test.go 的
// tokenStoreFactory），打开浏览器换成记录调用，不碰真实系统与主循环。
func testBook(t *testing.T) *desktop {
	t.Helper()
	b := newDesktop(t.TempDir())
	b.openURL = func(string) error { return nil }
	return b
}

// addTestAccount 给单例装上唯一的假会话：测试不跑浏览器授权，直接给一份
// 令牌，返回这份会话供窗口使用。
func addTestAccount(t *testing.T, b *desktop, name string) *AuthManager {
	t.Helper()
	m := newAuthManagerFor(b.dir, b.proxy, legacyTokenKey)
	m.store = &memoryTokenStore{}
	if err := m.storeToken(tokenData{
		AccessToken:  "tok-" + name,
		RefreshToken: "r-" + name,
		TokenType:    "Bearer",
		Username:     name,
	}); err != nil {
		t.Fatal(err)
	}
	b.auth = m
	return m
}

// singleAccountWindow 造一个带假登录会话的窗口：没有进程级单例，会话只属于
// 这个窗口。
func singleAccountWindow(t *testing.T, name string) *app {
	t.Helper()
	dir := t.TempDir()
	a := newApp(dir)
	m := newAuthManagerAt(dir, a.proxy)
	m.store = &memoryTokenStore{}
	if err := m.storeToken(tokenData{
		AccessToken:  "tok-" + name,
		RefreshToken: "r-" + name,
		TokenType:    "Bearer",
		Username:     name,
	}); err != nil {
		t.Fatal(err)
	}
	a.auth = m
	a.client.Auth = m
	a.autoLoad = false
	return a
}

// openTestDetail 给 app 摆上一篇带评论的帖子（与 detailAppWithReplies 同一套
// 夹具），评论输入框才会被画出来。
func openTestDetail(a *app) *app {
	a.openDetail(Item{ShortID: "JBYGf2A", URL: "https://2libra.com/post/work/JBYGf2A"})
	topic, _ := decodeTopic([]byte(detailTopicJSON), "JBYGf2A")
	a.detail.topic = &topic
	pg := decodeReplies([]byte(detailRepliesJSON))
	a.detail.replyList = flattenReplies(pg)
	a.detail.replyTotal = pg.Total
	a.detail.replyLoaded = true
	return a
}
