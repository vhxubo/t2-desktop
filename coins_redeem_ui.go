package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/feedback"
	"github.com/lobehub/lobe-mygo/pkg/layout"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	lobetheme "github.com/lobehub/lobe-mygo/pkg/theme"
)

// 金币窗口的「兑换」页：我兑换过的兑换码（GET /v1/me/redeem-codes）。
// 领取在帖子详情页的兑换码块里（见 redeem.go），这里只回看历史：换到了什么
// 码、红包备注、来自哪篇帖子。数据在 redeem_history.go。
//
// 与「历史」页一样是页码分页：滚到底自动续拉，续拉失败只在列表底部提示。
// 界面走 Lobe UI：标题、转圈、错误提示与空状态都用 lobe-mygo 组件，文案保持不变。

func (a *app) ensureCoinRedeem() {
	cs := a.coins
	if cs == nil || cs.redeemLoading || cs.redeemLoaded || cs.redeemWait {
		return
	}
	a.loadCoinRedeem(1)
}

// loadCoinRedeem 拉第 page 页。page==1 时重建列表；page>1 时按 id 去重后追加。
// 续拉失败只记 moreErr，保留已有内容。
func (a *app) loadCoinRedeem(page int) {
	cs := a.coins
	if cs == nil || page < 1 || cs.redeemLoading {
		return
	}
	if page == 1 {
		cs.redeemSeq++
		cs.redeemLoaded = false
		cs.redeemItems = nil
		cs.redeemNoMore = false
		cs.redeemErr = ""
		cs.redeemMoreErr = ""
		cs.redeemList.ScrollTo(0, ui.Start)
	}
	seq := cs.redeemSeq
	cs.redeemLoading = true
	cs.redeemWait = false
	cs.redeemPage = page
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	go func() {
		defer cancel()
		res, err := a.client.fetchRedeemedCodes(ctx, page)
		a.publish(func() {
			if a.coins != cs || cs.redeemSeq != seq {
				return
			}
			cs.redeemLoading = false
			cs.redeemLoaded = true
			if err != nil {
				if a.handleAuthErrorFor(toastCoins, err, func() { a.ensureCoinRedeem() }) {
					cs.redeemWait = true
					return
				}
				if page == 1 {
					cs.redeemErr = friendlyError(err)
				} else {
					cs.redeemMoreErr = friendlyError(err)
				}
				return
			}
			if page == 1 {
				cs.redeemItems = res.Items
			} else {
				seen := make(map[string]bool, len(cs.redeemItems))
				for _, code := range cs.redeemItems {
					seen[code.ID] = true
				}
				added := 0
				for _, code := range res.Items {
					if !seen[code.ID] {
						cs.redeemItems = append(cs.redeemItems, code)
						seen[code.ID] = true
						added++
					}
				}
				if added == 0 {
					cs.redeemNoMore = true
				}
			}
			cs.redeemPage = page
			cs.redeemTotal = res.Total
			cs.redeemTotalPages = res.TotalPages
			if len(res.Items) == 0 || (res.TotalPages > 0 && page >= res.TotalPages) {
				cs.redeemNoMore = true
			}
			cs.redeemErr = ""
			cs.redeemMoreErr = ""
		})
	}()
}

// reloadCoinRedeem 丢掉已拉到的记录，从第一页重拉（页面上的「刷新」）。
func (a *app) reloadCoinRedeem() {
	cs := a.coins
	if cs == nil {
		return
	}
	cs.redeemSeq++
	cs.redeemLoading = false
	cs.redeemLoaded = false
	cs.redeemWait = false
	cs.redeemErr = ""
	cs.redeemMoreErr = ""
	a.loadCoinRedeem(1)
}

// coinRedeemView 是「兑换」页：标题行 + 记录列表。
func (a *app) coinRedeemView(c *ui.Context, cs *coinState) {
	ui.Row(c).FillWidth().Padding(8, 14, 6, 14).Gap(10).AlignItems(ui.Center).Children(func() {
		display.Text(c, "我兑换过的兑换码", display.TextProps{Strong: true, FontSize: 14, Rows: 1})
		display.Text(c, "按兑换时间倒序", display.TextProps{
			Type: display.TextSecondary, FontSize: 11, Rows: 1,
		})
		ui.Spacer(c).Grow(1)
		if cs.redeemTotal > 0 {
			display.Text(c, fmt.Sprintf("共 %d 条", cs.redeemTotal), display.TextProps{
				Type: display.TextSecondary, FontSize: 11, Rows: 1,
			})
		}
		a.smallAction(c, "刷新", cs.redeemLoading, func() { a.reloadCoinRedeem() })
	})

	switch {
	case cs.redeemLoading && len(cs.redeemItems) == 0:
		ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
			lobeSpin(c, feedback.SpinProps{CustomSize: 24})
			display.Text(c, "正在加载兑换记录…", display.TextProps{
				Type: display.TextSecondary, Rows: 1,
			})
		})
		return
	case cs.redeemErr != "":
		ui.Column(c).Grow(1).Center().Gap(12).Padding(24).Children(func() {
			feedback.Alert(c, "兑换记录加载失败", feedback.AlertProps{
				Type: feedback.Error, Variant: feedback.Soft,
				Description: cs.redeemErr, Selectable: true,
			})
			if action.Button(c, "重试", action.ButtonProps{Size: lobetheme.Small}).Clicked() {
				a.reloadCoinRedeem()
			}
		})
		return
	case len(cs.redeemItems) == 0:
		ui.Column(c).Grow(1).Center().Children(func() {
			display.Empty(c, display.EmptyProps{Icon: lucide.Ticket(), Title: "还没有兑换过兑换码。"})
		})
		return
	}

	items := cs.redeemItems
	cs.redeemList.Key = func(i int) any {
		if i >= 0 && i < len(items) {
			return items[i].ID
		}
		return i
	}
	ui.List(c, &cs.redeemList, len(items), func(i int) {
		a.coinRedeemedRow(c, items[i])
	}).Grow(1).Padding(8, 14).Gap(8)
	a.loadMoreFooter(c, cs.redeemMoreErr, cs.redeemLoading, func() {
		a.loadCoinRedeem(cs.redeemPage)
	})
	if cs.redeemLoaded && !cs.redeemLoading && cs.redeemMoreErr == "" && !cs.redeemNoMore &&
		cs.redeemPage > 0 && len(items) > 0 && cs.redeemList.AtEnd() {
		hasMore := cs.redeemTotalPages == 0 || cs.redeemPage < cs.redeemTotalPages
		if hasMore {
			a.loadCoinRedeem(cs.redeemPage + 1)
		}
	}
}

// coinRedeemedRow 是一条兑换记录：兑换码是主信息，下面一行是码描述 / 红包
// 备注 / 来源帖与兑换时间。原帖还在时整行可点，打开主窗口的正文。
func (a *app) coinRedeemedRow(c *ui.Context, code redeemedCode) {
	openable := code.PostShortID != "" && !code.PostDeleted
	row := layout.Block(c, layout.BlockProps{Clickable: true, Variant: layout.BlockOutlined}).
		FillWidth().Padding(10, 12)
	row.Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				display.Text(c, code.Code, display.TextProps{
					Strong: true, FontSize: 13, Rows: 1,
				}).Shrink(1).MinWidth(0).Ellipsis("…")
				if code.Note != "" {
					display.Text(c, code.Note, display.TextProps{
						Type: display.TextSuccess, FontSize: 11, Rows: 1,
					}).Shrink(0)
				}
			})
			parts := make([]string, 0, 4)
			if code.Description != "" {
				parts = append(parts, code.Description)
			}
			switch {
			case code.PostDeleted:
				parts = append(parts, "原帖已删除")
			case code.PostTitle != "":
				parts = append(parts, "来自《"+code.PostTitle+"》")
			}
			if code.PostNode != "" && !code.PostDeleted {
				parts = append(parts, code.PostNode)
			}
			if when := relativeTime(code.RedeemedAt); when != "" {
				parts = append(parts, when+"兑换")
			}
			if len(parts) > 0 {
				display.Text(c, strings.Join(parts, " · "), display.TextProps{
					Type: display.TextSecondary, FontSize: 11, Rows: 2,
				})
			}
		})
	})
	if openable {
		// 无障碍名带上兑换码：同屏可能有多条可点记录，读屏与测试都要能分清。
		row.Label("打开兑换码来源帖 " + code.Code)
		row.Tooltip("打开这篇帖子")
		row.OnClick(func() { a.openCoinRedeemPost(code) })
	}
}

// openCoinRedeemPost 把主窗口带到前台并打开来源帖。金币窗口留着不关：
// 读完还能回来接着看兑换记录。
func (a *app) openCoinRedeemPost(code redeemedCode) {
	if code.PostShortID == "" || code.PostDeleted {
		return
	}
	a.pageName = "list" // 从详情页的返回栈里出来，避免把列表页顶掉
	a.detailStack = nil
	a.openDetail(Item{ShortID: code.PostShortID, Title: code.PostTitle})
	a.showMainWindowAfterDetail()
}
