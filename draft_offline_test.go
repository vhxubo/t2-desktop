package main

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// 断网发送失败时，评论草稿必须已经落盘，也不能误报「草稿没能保存到本机」。
func TestReplyDraftSurvivesOfflineSendFailure(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false
	m := newAuthManager(t.TempDir())
	_ = m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"})
	a.auth = m
	a.detail = &detailState{shortID: "JBYGf2A", draft: "断网也要保住"}
	// 模拟用户写完还没等到 800ms 防抖落盘就点了发送。
	a.draftTick()

	// 指向一个不可达端口，模拟连接失败（真实网络抖动时的表现）。
	a.client.HTTP = &http.Client{Timeout: 2 * time.Second}
	a.client.Base = "http://127.0.0.1:1/api"

	done := make(chan error, 1)
	a.onReplyPosted = func(_ replyItem, err error) { done <- err }

	a.submitReply()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("断网却发送成功了")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("发送回调超时")
	}

	if a.detail.draft != "断网也要保住" {
		t.Errorf("发送失败后草稿被清掉了：%q", a.detail.draft)
	}
	// 从磁盘重新读：发送失败后草稿必须已经在本机文件里，不能只留在内存。
	disk := newDraftStore(dir, draftScope(a))
	if e, ok := disk.find(replyDraftID("JBYGf2A")); !ok || e.Text != "断网也要保住" {
		t.Errorf("发送失败后草稿没有落盘：%+v", disk.rows())
	}

	a.toastMu.Lock()
	var texts []string
	for _, note := range a.toasts {
		texts = append(texts, note.text)
	}
	a.toastMu.Unlock()
	for _, text := range texts {
		if strings.Contains(text, "草稿没") || strings.Contains(text, "草稿保存失败") {
			t.Errorf("网络失败时误报草稿保存失败：%q", texts)
		}
	}
}

// 发帖断网失败后，正文同样必须已经落盘，关窗 / 进程结束都不该丢。
func TestComposeDraftSavedAfterOfflinePostFailure(t *testing.T) {
	dir := t.TempDir()
	a := newApp(dir)
	a.autoLoad = false
	m := newAuthManager(t.TempDir())
	_ = m.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"})
	a.auth = m
	a.compose = newComposeState()
	a.compose.title = "断网也要保住标题"
	a.compose.body = "断网也要保住正文"
	a.draftTick()

	a.client.HTTP = &http.Client{Timeout: 2 * time.Second}
	a.client.Base = "http://127.0.0.1:1/api"

	done := make(chan error, 1)
	a.onPostPublished = func(_ createdTopic, err error) { done <- err }

	a.submitCompose()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("断网却发帖成功了")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("发帖回调超时")
	}

	if a.compose.title != "断网也要保住标题" || a.compose.body != "断网也要保住正文" {
		t.Errorf("发帖失败后表单被清掉了：%+v", a.compose)
	}
	disk := newDraftStore(dir, draftScope(a))
	e, ok := disk.latestCompose(composeModePost)
	if !ok || e.Title != "断网也要保住标题" || e.Body != "断网也要保住正文" {
		t.Errorf("发帖失败后草稿没有落盘：%+v", disk.rows())
	}
}
