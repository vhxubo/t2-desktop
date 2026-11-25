package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

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
	m := newAuthManagerAt(b.dir, b.proxy)
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

// waitForToast 等一条结果提示排进队列。
//
// 后台结果回到 UI 线程时，先清 busy、关窗口，最后才把提示排进队列（见各
// submit 的收尾顺序）。测试只等前一步就断言提示，会在两步之间抢跑：等状态
// 的循环每 5ms 看一次，而两步只差几百纳秒，本机几乎撞不上，macOS 回归上撞到
// 过（编辑评论保存后「没有报告评论结果」）。要断言提示，就等提示自己。
func waitForToast(t *testing.T, a *app, part string) {
	t.Helper()
	waitFor(t, func() bool { return hasToastContaining(a, part) }, "没有提示："+part)
}

// waitForShownToast 等一条提示出现在窗口上。提示只在构建期发出，排队之后可能
// 已经被某一帧领走画进窗口，所以队列和窗口都要看：队列里有就先让这一帧领走，
// 再按窗口上的文案断言。两处都算结果被报告了。
func waitForShownToast(t *testing.T, a *app, tt *ui.Tester, part string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		tt.Frame() // 队列里有提示时，这一帧会把它们交给 toast 层
		if tt.HasText(part) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("没有提示 %q：队列 %v，窗口 %q", part, toastTexts(a), tt.Texts())
		}
		time.Sleep(5 * time.Millisecond)
	}
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
