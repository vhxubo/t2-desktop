package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 兑换记录端点：路径、page/limit 参数与 items 的解码（含来源帖与已删除帖）。
func TestFetchRedeemedCodes(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()
		if r.URL.Path != "/api/v1/me/redeem-codes" {
			t.Errorf("路径不对：%s", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, envelopeJSON(`{"items":[
			{"id":"r1","code":"LIBRA-2026-ABCD","note":"🧧 268 金币",
			 "redeemed_at":"2026-11-13T14:22:05.031Z","code_description":"双十一活动兑换码",
			 "post":{"short_id":"aBcD1234","title":"活动帖标题","is_deleted":false,
			         "node":{"id":"n1","name":"分享","slug":"share"},
			         "author":{"username":"jimmy"}}},
			{"id":"r2","code":"OLD-CODE","post":{"short_id":"dead","title":"已删帖","is_deleted":true}}
		],"total":2,"page":1,"limit":50,"total_pages":1}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Base: srv.URL + "/api", bearer: func() string { return "tok" }}

	page, err := c.fetchRedeemedCodes(context.Background(), 1)
	if err != nil {
		t.Fatalf("读兑换记录失败：%v", err)
	}
	if len(page.Items) != 2 || page.Total != 2 || page.TotalPages != 1 {
		t.Fatalf("分页解析不对：%+v", page)
	}
	first := page.Items[0]
	if first.Code != "LIBRA-2026-ABCD" || first.Note != "🧧 268 金币" ||
		first.Description != "双十一活动兑换码" ||
		first.RedeemedAt != "2026-11-13T14:22:05.031Z" {
		t.Errorf("第一条记录解析不对：%+v", first)
	}
	if first.PostShortID != "aBcD1234" || first.PostTitle != "活动帖标题" ||
		first.PostNode != "分享" || first.PostDeleted {
		t.Errorf("来源帖解析不对：%+v", first)
	}
	// 帖子已删除时仍要返回，由界面把标题降级成不可点击。
	if second := page.Items[1]; !second.PostDeleted || second.PostTitle != "已删帖" {
		t.Errorf("已删除的帖子应当保留 is_deleted 标记：%+v", second)
	}

	mu.Lock()
	defer mu.Unlock()
	raw := got[0]
	idx := strings.Index(raw, "?")
	if idx < 0 {
		t.Fatalf("请求没有查询参数：%q", raw)
	}
	q, _ := url.Parse("/?" + raw[idx+1:])
	if q.Query().Get("page") != "1" || q.Query().Get("limit") != "50" {
		t.Errorf("分页参数不对：%v", q.Query())
	}
}

// 金币窗口的「兑换」页：记录列表画出码与来源帖，原始帖子链接到主窗口。
func TestCoinRedeemView(t *testing.T) {
	a := testApp(t)
	a.coins.view = coinViewRedeem
	a.coins.redeemLoaded = true
	a.coins.redeemItems = []redeemedCode{
		{ID: "r1", Code: "LIBRA-2026-ABCD", Note: "🧧 268 金币",
			Description: "双十一活动", RedeemedAt: "2026-11-13T14:22:05Z",
			PostShortID: "aBcD1234", PostTitle: "活动帖标题", PostNode: "分享"},
		{ID: "r2", Code: "OLD-CODE", PostShortID: "dead", PostTitle: "已删帖", PostDeleted: true},
	}
	a.coins.redeemTotal = 2
	a.coins.redeemPage = 1
	a.coins.redeemTotalPages = 1

	tt := ui.NewTester(a.coinWindowView, coinWindowWidth, coinWindowHeight)
	tt.Frame()
	for _, want := range []string{
		"兑换", "我兑换过的兑换码", "共 2 条",
		"LIBRA-2026-ABCD", "🧧 268 金币", "来自《活动帖标题》",
		"OLD-CODE", "原帖已删除",
	} {
		if !tt.HasText(want) {
			t.Errorf("兑换页缺少 %q：%q", want, tt.Texts())
		}
	}
	// 只有原帖还在的那条能点开。
	if _, ok := tt.Find("打开兑换码来源帖 LIBRA-2026-ABCD"); !ok {
		t.Errorf("可打开的记录没有入口：%q", tt.Texts())
	}
}
