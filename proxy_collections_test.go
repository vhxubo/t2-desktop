package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// 合集（改名 / 删除 / 公开合集 / 合集内帖子）、兑换记录与通知关键词这几条新
// 出口都必须走 proxy.json 配置的代理：它们用的是同一个共享 transport，这里
// 用假代理把它们逐个跑一遍，避免哪天真接成直连。
func TestCollectionAndRedeemEndpointsUseConfiguredProxy(t *testing.T) {
	clearProxyEnv(t)
	var mu sync.Mutex
	var paths []string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch {
		case r.Method == http.MethodPatch:
			_, _ = fmt.Fprint(w, envelopeJSON(`{"id":"c1","name":"改名后"}`))
		case r.Method == http.MethodDelete:
			_, _ = fmt.Fprint(w, envelopeJSON(`{"ok":true}`))
		case r.URL.Path == "/api/v1/me/redeem-codes":
			_, _ = fmt.Fprint(w, envelopeJSON(`{"items":[],"total":0}`))
		case r.URL.Path == "/api/v1/me/notifications":
			_, _ = fmt.Fprint(w, envelopeJSON(`{"list":[],"total":0}`))
		case strings.HasSuffix(r.URL.Path, "/collections"):
			_, _ = fmt.Fprint(w, envelopeJSON(`[{"id":"c1","name":"公开合集"}]`))
		case strings.HasSuffix(r.URL.Path, "/topics"):
			_, _ = fmt.Fprint(w, envelopeJSON(`{"posts":[],"total":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer proxySrv.Close()

	p := newProxyManager(t.TempDir())
	if err := p.Save(proxyConfig{Mode: proxyManual, URL: proxySrv.URL}); err != nil {
		t.Fatal(err)
	}
	c := &Client{
		Proxy:  p,
		Base:   "http://upstream.invalid/api",
		bearer: func() string { return "tok" },
	}
	ctx := context.Background()

	if _, err := c.renameCollection(ctx, "c1", "改名后"); err != nil {
		t.Fatalf("经代理改名失败：%v", err)
	}
	if err := c.deleteCollection(ctx, "c1"); err != nil {
		t.Fatalf("经代理删除合集失败：%v", err)
	}
	if _, err := c.fetchUserCollections(ctx, "jimmy"); err != nil {
		t.Fatalf("经代理读公开合集失败：%v", err)
	}
	if _, err := c.fetch(ctx, collectionTopicsSpec("c1"), 1); err != nil {
		t.Fatalf("经代理读合集内帖子失败：%v", err)
	}
	if _, err := c.fetchRedeemedCodes(ctx, 1); err != nil {
		t.Fatalf("经代理读兑换记录失败：%v", err)
	}
	if _, err := c.fetchNotifications(ctx, 1, notificationFilterAll, "关键词"); err != nil {
		t.Fatalf("经代理搜通知失败：%v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"PATCH /api/v1/me/collections/c1",
		"DELETE /api/v1/me/collections/c1",
		"GET /api/v1/users/jimmy/collections",
		"GET /api/v1/collections/c1/topics",
		"GET /api/v1/me/redeem-codes",
		"GET /api/v1/me/notifications",
	}
	if len(paths) != len(want) {
		t.Fatalf("代理收到 %d 次请求，期望 %d 次：%v", len(paths), len(want), paths)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("第 %d 次请求 = %s，期望 %s", i+1, paths[i], want[i])
		}
	}
}
