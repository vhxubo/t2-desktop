package main

import (
	"testing"
)

// 详情缓存不把受限内容的付费正文写进本机：门槛信息保留（离线时还能看到
// 价格），正文清空。
func TestDetailCacheStripsRestrictedBody(t *testing.T) {
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
		t.Fatal("应读到刚写入的详情缓存")
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
}
