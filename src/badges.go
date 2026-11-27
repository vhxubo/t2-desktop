package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 徽章墙与佩戴切换。「我的」窗口的「徽章」标签页对应这几个端点：
//
//	GET    /v1/me/badges                  我拥有的全部徽章（scope profile）
//	GET    /v1/me/badges/equipped         我佩戴中的徽章（scope profile）
//	POST   /v1/me/badges/:badgeId/equip   佩戴（scope interact 或 write:economy）🔑
//	DELETE /v1/me/badges/:badgeId/equip   取消佩戴（同上）🔑
//
// 佩戴 / 取消佩戴是这里唯一的写操作。按 2026-11-12 的变更，佩戴不消耗金币、
// 也不占金币变动额度：默认 scope 里的 interact 已足够，历史令牌只持
// write:economy 也照常放行。缺权限时交给 auth_gate 补授权，授权成功后同一次
// 操作需要用户自己再点一次。两个方向都标 🔑：一次点击（含超时重试）带同一
// 个 Idempotency-Key，避免重复提交。
//
// 徽章图有两个尺寸：icon_url 是 22×15 的小图，只用于用户名旁 ≤32px 的槽位；
// icon_url_large 才是徽章墙用的图。部分徽章只有小图，这时回退到小图，绝不
// 用 code 去拼图片地址（有徽章的 code 与文件名不一致，会 404）。

const (
	myBadgesPath         = "/v1/me/badges"
	myBadgesEquippedPath = "/v1/me/badges/equipped"
)

// myBadgeEquipPath 是佩戴 / 取消佩戴的路径。badgeId 是徽章本体的 uuid，从
// GET /v1/me/badges 的 item.badge.id 取，不是 user_badge 的 id。
func myBadgeEquipPath(badgeID string) string {
	return myBadgesPath + "/" + url.PathEscape(badgeID) + "/equip"
}

// badgeWallEntry 是徽章墙里的一枚徽章。响应是 user_badge 包 badge 的嵌套结构，
// 佩戴接口要的是内层 badge.id，所以两个 id 分开存。
type badgeWallEntry struct {
	UserBadgeID  string // user_badge 的 uuid
	BadgeID      string // 徽章本体 uuid：佩戴 / 取消佩戴用
	Code         string // 徽章机器码，例如 do_me_a_favor
	Name         string
	Description  string
	IconURL      string // 22×15 的小图，用户名旁用
	IconURLLarge string // 徽章墙用；没有时回退 IconURL
	Level        int
	UserCount    int
	// UserPercentage 是持有该徽章的用户比例；0 表示接口没给。
	UserPercentage float64
	AchievedAt     string
	Equipped       bool

	// 下面几项来自 badge.attributes，是「发评会不会更便宜」的全部依据（见
	// comment_cost.go）。字段缺失与 0 是两件不同的事（0 表示这枚徽章把发评
	// 费用压到 0），所以每一项都配一个 Has 标记，不做零值推断。
	//
	//	comment_cost                 固定扣费，负数表示扣除（前排打手 8 金币）
	//	comment_cost_percent         评论专属减免（都听我说！0.3）
	//	coin_system_cost_percent     金币系统普适减免（大平衡者 0.3）
	//	daily_active_gain_percent    活跃度加成（走个面 0.2），与评论费用无关
	CommentCost               float64
	HasCommentCost            bool
	CommentCostPercent        float64
	HasCommentCostPercent     bool
	CoinSystemCostPercent     float64
	HasCoinSystemCostPercent  bool
	DailyActiveGainPercent    float64
	HasDailyActiveGainPercent bool
}

// profileBadgeDisplayName 是徽章在界面上的名字。接口偶尔会有没有名称的条目，
// 这时仍给一个可读的称呼，而不是让卡片留空。
func profileBadgeDisplayName(b badgeWallEntry) string {
	if name := strings.TrimSpace(b.Name); name != "" {
		return name
	}
	return "未命名徽章"
}

// badgeList 取响应里的条目数组。文档说 d 本身就是数组；这里同时认
// {items:[...]} 这种包装，免得线上换了形状时整页空白。
func badgeList(d json.RawMessage) []json.RawMessage {
	if raw := rawArray(d); raw != nil {
		return raw
	}
	o := asObject(d)
	if o == nil {
		return nil
	}
	for _, key := range []string{"items", "badges"} {
		if raw := rawArray(o[key]); raw != nil {
			return raw
		}
	}
	return nil
}

// decodeUserBadges 解析 GET /v1/me/badges。徽章本体在 item.badge 里，不在
// 顶层；user_percentage 可能是数字或字符串，两种都认。
func decodeUserBadges(d json.RawMessage) []badgeWallEntry {
	var out []badgeWallEntry
	for _, raw := range badgeList(d) {
		item := asObject(raw)
		if item == nil {
			continue
		}
		badge := item.sub("badge")
		if badge == nil {
			badge = item
		}
		b := badgeWallEntry{
			UserBadgeID:    item.str("id"),
			BadgeID:        item.str("badge_id"),
			Code:           badge.str("code"),
			Name:           badge.str("name"),
			Description:    badge.str("description"),
			IconURL:        badge.str("icon_url"),
			IconURLLarge:   badge.str("icon_url_large"),
			Level:          badge.num("level"),
			UserCount:      badge.num("user_count"),
			UserPercentage: rawFloat(badge["user_percentage"]),
			AchievedAt:     item.str("achieved_at"),
		}
		// attributes 是每枚徽章自己的数值表：只认识「与发评费用 / 活跃度有关」
		// 的那四项，exp、float_percent 之类的属性不参与任何判断。
		if attr := badge.sub("attributes"); attr != nil {
			b.CommentCost, b.HasCommentCost = badgeFloat(attr, "comment_cost")
			b.CommentCostPercent, b.HasCommentCostPercent =
				badgeFloat(attr, "comment_cost_percent")
			b.CoinSystemCostPercent, b.HasCoinSystemCostPercent =
				badgeFloat(attr, "coin_system_cost_percent")
			b.DailyActiveGainPercent, b.HasDailyActiveGainPercent =
				badgeFloat(attr, "daily_active_gain_percent")
		}
		if b.BadgeID == "" {
			b.BadgeID = badge.str("id")
		}
		// IconURL 是用户名旁的小图；IconURLLarge 没有就是没有，不拿小图
		// 冒充大图 —— 徽章墙据此决定画多大。
		if b.IconURL == "" {
			b.IconURL = b.IconURLLarge
		}
		// 佩戴端点可能顺带在每项里标出状态；标准响应没有这个字段，缺省 false，
		// 真实状态以 /badges/equipped 为准。
		b.Equipped = item.flag("equipped", "is_equipped")
		if b.BadgeID == "" && b.Name == "" && b.IconURL == "" {
			continue
		}
		out = append(out, b)
	}
	return out
}

// decodeEquippedBadgeIDs 解析 GET /v1/me/badges/equipped，只保留 badge_id。
// 佩戴状态按徽章本体 id 对齐，墙里没有匹配项的就是未佩戴。
func decodeEquippedBadgeIDs(d json.RawMessage) map[string]bool {
	out := make(map[string]bool)
	for _, raw := range badgeList(d) {
		item := asObject(raw)
		if item == nil {
			continue
		}
		id := item.str("badge_id")
		if id == "" {
			if badge := item.sub("badge"); badge != nil {
				id = badge.str("id")
			}
		}
		if id != "" {
			out[id] = true
		}
	}
	return out
}

// rawFloat 读一个可能是数字、数字字符串或缺失的浮点字段。
func rawFloat(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		f, _ = strconv.ParseFloat(strings.TrimSpace(s), 64)
		return f
	}
	return 0
}

// badgeFloat 读徽章属性里的一个数值，并回答「这个键在不在」。0 是合法值
// （例如把发评费用减免到 0），不能用 0 当成「没给」。
func badgeFloat(o object, keys ...string) (float64, bool) {
	for _, k := range keys {
		raw, ok := o[k]
		if !ok {
			continue
		}
		if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
			continue
		}
		return rawFloat(raw), true
	}
	return 0, false
}

/* ------------------------------------------------------------------
   请求
---------------------------------------------------------------- */

// fetchMyBadges 读徽章墙。读端点匿名可用，但登录后仍照常带令牌：本人访问
// 会多返回 custom_fields 里的隐私条目。
func (c *Client) fetchMyBadges(ctx context.Context) ([]badgeWallEntry, error) {
	d, err := withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		return c.getJSON(ctx, c.apiURL(myBadgesPath), token)
	})
	if err != nil {
		return nil, err
	}
	return decodeUserBadges(d), nil
}

// fetchEquippedBadgeIDs 读当前佩戴中的徽章 id 集合。
func (c *Client) fetchEquippedBadgeIDs(ctx context.Context) (map[string]bool, error) {
	d, err := withAuthRetry(c, ctx, func(token string) (json.RawMessage, error) {
		return c.getJSON(ctx, c.apiURL(myBadgesEquippedPath), token)
	})
	if err != nil {
		return nil, err
	}
	return decodeEquippedBadgeIDs(d), nil
}

// setBadgeEquipped 佩戴或取消佩戴一枚徽章，返回服务端给的 message（可直接
// 展示）。key 是一次点击的幂等键：超时重试用同一个，不能每次
// 调用重新生成。重复佩戴 / 重复取消的 409 conflict 视为目标状态已成立。
func (c *Client) setBadgeEquipped(ctx context.Context, badgeID, key string, equip bool) (string, error) {
	var message string
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		var d json.RawMessage
		var err error
		if equip {
			d, err = c.postJSON(ctx, c.apiURL(myBadgeEquipPath(badgeID)), token, key, nil)
		} else {
			d, err = c.deleteWithKeyJSON(ctx, c.apiURL(myBadgeEquipPath(badgeID)), token, key)
		}
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusConflict {
			err = nil
		}
		if err == nil {
			if o := asObject(d); o != nil {
				message = o.str("message", "msg")
			}
		}
		return struct{}{}, err
	})
	return message, err
}

/* ------------------------------------------------------------------
   加载与切换：状态只在 UI 线程读写，后台结果经 publish 回来
---------------------------------------------------------------- */

// loadProfileBadges 同时读徽章墙与佩戴状态。两个请求顺序发出：都只要
// profile scope，缺权限时同一次授权可以把它们一起补上。
func (a *app) loadProfileBadges() {
	ps := a.profile
	if ps == nil || !a.auth.loggedIn() || ps.badgeLoading {
		return
	}
	ps.badgeSeq++
	seq := ps.badgeSeq
	ps.badgeLoading = true
	ps.badgeErr = ""
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		badges, err := a.client.fetchMyBadges(ctx)
		var equipped map[string]bool
		if err == nil {
			equipped, err = a.client.fetchEquippedBadgeIDs(ctx)
		}
		a.publish(func() {
			if a.profile != ps || ps.badgeSeq != seq {
				return
			}
			ps.badgeLoading = false
			ps.badgeLoaded = true
			if err != nil {
				ps.badgeErr = friendlyError(err)
				// 缺 profile 权限（旧令牌）或登录失效时走全局授权链路，
				// 缺权限时交给授权弹窗；普通网络错误只留在页面里重试。
				a.handleAuthErrorFor(toastProfile, err)
				return
			}
			for i := range badges {
				badges[i].Equipped = equipped[badges[i].BadgeID]
			}
			ps.badges = badges
			ps.badgeErr = ""
			a.syncProfileEquippedBadges(ps)
			a.prefetchBadgeIcons(badges)
		})
	}()
}

// reloadProfileBadges 丢掉已经拉到的徽章，重新读（徽章页的「刷新」）。
func (a *app) reloadProfileBadges() {
	ps := a.profile
	if ps == nil || ps.badgeLoading || ps.badgeBusyID != "" {
		return
	}
	ps.badgeLoaded = false
	ps.badgeErr = ""
	ps.badges = nil
	a.loadProfileBadges()
}

// refreshBadgesAfterToggle 在一次佩戴 / 取消佩戴成功后重新读回数据。本地只改了
// Equipped 标记；徽章列表与资料头的徽章展示（图、天气 / 状态）都以服务端为准，
// 重新读一次才能把两处和真正的佩戴状态对齐。actor 是发起这次操作的账号名，
// 请求期间登录态变了就不该拿新数据覆盖旧的资料。
func (a *app) refreshBadgesAfterToggle(actor string) {
	a.loadProfileBadges()
	if actor != "" {
		a.loadProfileInfo(actor)
	}
	// 自己的资料卡和资料头共用同一份佩戴状态：切换后也重读它，资料卡上的
	// 徽章行与佩戴图标一起跟上，不必等下次打开窗口。
	if cs := a.userCard; cs != nil && a.userCardSelf() {
		a.reloadUserCardBadges()
		a.loadUserCardProfile()
	}
}

// openBadgePicker 从个人中心（fromCard=false）或自己的资料卡（fromCard=true）
// 打开徽章切换弹窗。别人的资料卡不会调用它：资料头只在自己的窗口里挂事件。
func (a *app) openBadgePicker(fromCard bool) {
	ps := a.profile
	if ps == nil || !a.auth.loggedIn() {
		return
	}
	if fromCard {
		cs := a.userCard
		if cs == nil || !a.userCardSelf() || !cs.badges.loaded || cs.badges.err != "" {
			return
		}
		// 自己的资料卡读的是等价的徽章墙端点，直接交给弹窗与提交逻辑共用。
		ps.badges = cs.badges.entries
		ps.badgeLoaded = true
		ps.badgeErr = ""
	} else if !ps.badgeLoaded {
		a.loadProfileBadges()
	}
	ps.badgePickerOpen = true
	ps.badgePickerFromCard = fromCard
	if fromCard {
		a.redrawUserCardWindow()
	} else {
		a.redrawProfileWindow()
	}
}

// prefetchBadgeIcons 预取徽章墙的大图。徽章图属于长期缓存：同一枚徽章反复
// 佩戴不会换图，下载一次就留在本机。
func (a *app) prefetchBadgeIcons(badges []badgeWallEntry) {
	if !a.autoLoad {
		return
	}
	for _, b := range badges {
		url := b.IconURLLarge
		if url == "" {
			url = b.IconURL
		}
		if url != "" {
			a.prefetchImage(url, imageCacheStable)
		}
	}
}

// toggleProfileBadge 佩戴或取消佩戴一枚徽章。name 是这次操作对应的徽章名，
// 用来组装结果的 toast；名字在一次点击里保持不变。
// key 为空表示一次新的点击；重放会带着同一个 key 回来。不先乐观更新：佩戴
// 状态以服务端的确认为准。
func (a *app) toggleProfileBadge(badgeID, name string, equip bool, key string) {
	ps := a.profile
	if ps == nil || badgeID == "" || !a.auth.loggedIn() {
		return
	}
	if ps.badgeBusyID != "" {
		return
	}
	if key == "" {
		key = randomURLSafe(24)
	}
	if name == "" {
		name = profileBadgeName(ps.badges, badgeID)
	}
	ps.badgeBusyID = badgeID
	actor := a.auth.username()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		message, err := a.client.setBadgeEquipped(ctx, badgeID, key, equip)
		a.publish(func() {
			if a.profile != ps {
				return
			}
			ps.badgeBusyID = ""
			// 请求期间登录态变了：这次结果不属于当前会话。
			if !a.auth.loggedIn() || a.auth.username() != actor {
				return
			}
			if err != nil {
				if a.handleAuthErrorFor(toastProfile, err) {
					return
				}
				a.toast(toastProfile, friendlyError(err))
				return
			}
			for i := range ps.badges {
				if ps.badges[i].BadgeID == badgeID {
					ps.badges[i].Equipped = equip
				}
			}
			// 只有徽章墙真的加载过才同步资料头：授权补 scope 会重置整个
			// 「我的」状态，这时的重放还没有墙数据，不能凭它把资料头清空。
			if ps.badgeLoaded {
				a.syncProfileEquippedBadges(ps)
			}
			// 评论框的状态栏读的是同一份佩戴状态：这里切换完要立刻跟上，
			// 否则评论框上方还显示着上一枚徽章的费用。
			a.noteBadgeEquipped(badgeID, equip)
			// 佩戴状态以服务端为准：重新读徽章列表与本人资料，两处的展示一起跟上。
			a.refreshBadgesAfterToggle(actor)
			if message == "" {
				if name == "" {
					message = "徽章佩戴状态已更新"
				} else if equip {
					message = "已佩戴「" + name + "」"
				} else {
					message = "已取消佩戴「" + name + "」"
				}
			}
			a.toast(toastProfile, message)
		})
	}()
}

// profileBadgeName 在已拉到的徽章里按徽章 uuid 找名字。
func profileBadgeName(badges []badgeWallEntry, badgeID string) string {
	for _, b := range badges {
		if b.BadgeID == badgeID {
			return profileBadgeDisplayName(b)
		}
	}
	return ""
}

// syncProfileEquippedBadges 把佩戴中的徽章同步到资料头：资料头本来就展示
// 佩戴中的徽章，切换后要立刻跟上。「忽冷忽热」这类带天气的徽章保留原来的
// 天气信息，「我来过」的状态也一并保留；这两项只出现在用户对象的
// equipped_badges 里，徽章墙没有。换下徽章后状态随它一起丢掉，重新戴上
// 不会从旧数据里恢复（服务端也不会自动恢复）。
func (a *app) syncProfileEquippedBadges(ps *profileState) {
	weather := make(map[string]*weatherInfo, len(ps.user.Badges))
	status := make(map[string]*badgeStatusInfo, len(ps.user.Badges))
	for _, b := range ps.user.Badges {
		if b.Weather != nil {
			weather[b.Name] = b.Weather
		}
		if b.Status != nil {
			status[b.Name] = b.Status
		}
	}
	infos := make([]badgeInfo, 0, len(ps.badges))
	for _, b := range ps.badges {
		if !b.Equipped {
			continue
		}
		icon := b.IconURL
		if icon == "" {
			icon = b.IconURLLarge
		}
		name := profileBadgeDisplayName(b)
		infos = append(infos, badgeInfo{
			Name:    name,
			Code:    b.Code,
			IconURL: icon,
			Weather: weather[name],
			Status:  status[name],
		})
	}
	ps.user.Badges = infos
}

/* ------------------------------------------------------------------
   展示辅助
---------------------------------------------------------------- */

// profileBadgeMeta 是徽章名下面那行：等级与持有比例。接口没给的项不编。
func profileBadgeMeta(b badgeWallEntry) string {
	parts := make([]string, 0, 2)
	if b.Level > 0 {
		parts = append(parts, "Lv."+strconv.Itoa(b.Level))
	}
	if b.UserPercentage > 0 {
		parts = append(parts, strconv.FormatFloat(b.UserPercentage, 'f', 2, 64)+"% 的用户拥有")
	} else if b.UserCount > 0 {
		parts = append(parts, fmt.Sprintf("%d 人拥有", b.UserCount))
	}
	return strings.Join(parts, " · ")
}

// profileBadgeAchievedText 是获得时间。解析不了时间时不显示，不编日期。
func profileBadgeAchievedText(raw string) string {
	if t, ok := profileTime(raw); ok {
		return "获得 " + t.Local().Format("2006-01-02")
	}
	return ""
}
