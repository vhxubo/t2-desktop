package main

import (
	"testing"
)

// 详情缓存按账号作用域分开，且不把受限内容的付费正文写进本机：门槛信息
// 保留（离线时还能看到价格），正文清空。换一个账号读不到上一篇账号的缓存。
func TestDetailCacheSeparatesAccountsAndStripsRestrictedBody(t *testing.T) {
	a := testApp(t)
	jimmy := newAuthManager(t.TempDir())
	if err := jimmy.storeToken(tokenData{
		AccessToken: "tok", TokenType: "Bearer", Username: "jimmy",
	}); err != nil {
		t.Fatal(err)
	}
	a.auth = jimmy

	topic := &topicDetail{
		ShortID: "R9", Title: "受限帖",
		Restricted: &restrictedInfo{
			Known: true, Unlocked: true,
			Content: "解锁后的付费正文", ContentKnown: true,
			Coins: 30, CoinsKnown: true,
		},
	}
	a.saveCachedDetail(&detailState{shortID: "R9", topic: topic, replyPage: 1})

	got, ok := a.readCachedDetail("R9")
	if !ok {
		t.Fatal("同一账号应读到自己的详情缓存")
	}
	if got.Topic.Restricted == nil {
		t.Fatal("受限内容的门槛信息不该被一起剥掉")
	}
	if got.Topic.Restricted.Content != "" || got.Topic.Restricted.ContentKnown {
		t.Errorf("付费正文落进了本机缓存：%+v", got.Topic.Restricted)
	}
	if !got.Topic.Restricted.CoinsKnown || got.Topic.Restricted.Coins != 30 {
		t.Errorf("门槛信息应保留：%+v", got.Topic.Restricted)
	}
	// 写缓存不能改动页面上正在显示的正文。
	if topic.Restricted.Content != "解锁后的付费正文" || !topic.Restricted.ContentKnown {
		t.Errorf("写缓存修改了内存里的正文：%+v", topic.Restricted)
	}

	alice := newAuthManager(t.TempDir())
	if err := alice.storeToken(tokenData{
		AccessToken: "tok2", TokenType: "Bearer", Username: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	a.auth = alice
	if _, ok := a.readCachedDetail("R9"); ok {
		t.Error("另一个账号不该读到上一个账号的详情缓存")
	}

	a.auth = jimmy
	if _, ok := a.readCachedDetail("R9"); !ok {
		t.Error("切回原账号后缓存应还在")
	}
}

// 已登录但昵称还没回来时不能落盘：那段窗口里的键没有账号身份，换一个账号
// 登录就会读到上一份详情与个人状态。
func TestDetailCacheSkipsUnnamedSession(t *testing.T) {
	a := testApp(t)
	m := newAuthManager(t.TempDir())
	if err := m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.auth = m
	if got := a.accountScope(); got != sessionScope {
		t.Fatalf("前置条件：昵称为空时应是 session 作用域，实际 %q", got)
	}

	a.saveCachedDetail(&detailState{shortID: "S1", topic: &topicDetail{ShortID: "S1"}})
	if _, ok := a.readCachedDetail("S1"); ok {
		t.Error("昵称未到时不该写详情缓存")
	}
	if n := a.kv.countPrefix(detailCachePrefix); n != 0 {
		t.Errorf("昵称未到时写进了 %d 条详情缓存", n)
	}
}
