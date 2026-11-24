package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// 本地内容屏蔽：名单与网页端「设置 → 内容屏蔽」同源，服务端只下发名单，
// 过滤完全由客户端做（接口契约见 docs/content-filter-plan.md）。
//
// 设计要点：
//   - 列表保留接口原始行（tabState.raw），可见行（tabState.items）由名单派生；
//     在资料卡上屏蔽 / 取消后立刻重算，不重新请求。
//   - 评论不在数据层删，渲染时判定并压成一行提示，展开态只在内存里。
//   - 名单是账号级数据：一个账号窗口一份快照，缓存按账号作用域落盘。
//   - 失败 fail-open：不隐藏任何内容，管理页显示原因并可重试。

const (
	contentFilterPath       = "/v1/me/content-filter"
	contentFilterMaxPerList = 50
	contentFilterMaxUserLen = 255
	contentFilterMaxNodeLen = 100
	contentFilterMaxBytes   = 3500
	contentFilterSkipPages  = 3
	contentFilterWait       = 5 * time.Second

	contentFilterCachePrefix = "content-filter:"
)

// 命中原因只报最外层的一条：用户 > 节点 > 关键词。
const (
	filterKindUser    = "user"
	filterKindNode    = "node"
	filterKindKeyword = "keyword"
)

type contentFilterDTO struct {
	BlockedUsers []string `json:"blocked_users"`
	BlockedNodes []string `json:"blocked_nodes"`
	Keywords     []string `json:"keywords"`
}

type blockReason struct {
	Kind  string // "user" / "node" / "keyword"
	Value string // 命中的名单条目，保留原写法，用于提示
}

func (r blockReason) text() string {
	switch r.Kind {
	case filterKindUser:
		return fmt.Sprintf("已屏蔽 %s 的评论", r.Value)
	case filterKindKeyword:
		return fmt.Sprintf("已屏蔽含「%s」的评论", r.Value)
	default:
		return "已屏蔽的评论"
	}
}

// contentFilter 是不可变快照：加载成功后整体替换，渲染时只读，不加锁。
type contentFilter struct {
	raw      contentFilterDTO
	users    map[string]bool
	nodes    map[string]bool
	keywords []string // 保留原写法，匹配时再 ToLower
}

// normalize 是所有匹配的唯一归一化入口：首尾空白 + Unicode 简单折叠。
// 名单与内容都来自同一个站点，ToLower 足够，不引入 x/text/cases。
func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func newContentFilter(d contentFilterDTO) *contentFilter {
	f := &contentFilter{
		raw:   cloneFilterDTO(d),
		users: make(map[string]bool, len(d.BlockedUsers)),
		nodes: make(map[string]bool, len(d.BlockedNodes)),
	}
	for _, s := range d.BlockedUsers {
		if k := normalize(s); k != "" {
			f.users[k] = true
		}
	}
	for _, s := range d.BlockedNodes {
		if k := normalize(s); k != "" {
			f.nodes[k] = true
		}
	}
	for _, s := range d.Keywords {
		if normalize(s) != "" {
			f.keywords = append(f.keywords, s)
		}
	}
	return f
}

func cloneFilterDTO(d contentFilterDTO) contentFilterDTO {
	return contentFilterDTO{
		BlockedUsers: append([]string(nil), d.BlockedUsers...),
		BlockedNodes: append([]string(nil), d.BlockedNodes...),
		Keywords:     append([]string(nil), d.Keywords...),
	}
}

func (f *contentFilter) userHit(name string) (blockReason, bool) {
	if f == nil || name == "" || name == "匿名" {
		return blockReason{}, false
	}
	key := normalize(name)
	if key == "" || !f.users[key] {
		return blockReason{}, false
	}
	// 命中时再找回名单里保留的原写法，提示里显示用户当初添加的样子。
	for _, orig := range f.raw.BlockedUsers {
		if normalize(orig) == key {
			return blockReason{Kind: filterKindUser, Value: orig}, true
		}
	}
	return blockReason{}, false
}

func (f *contentFilter) nodeHit(name string) (blockReason, bool) {
	if f == nil || name == "" {
		return blockReason{}, false
	}
	key := normalize(name)
	if key == "" || !f.nodes[key] {
		return blockReason{}, false
	}
	for _, orig := range f.raw.BlockedNodes {
		if normalize(orig) == key {
			return blockReason{Kind: filterKindNode, Value: orig}, true
		}
	}
	return blockReason{}, false
}

func (f *contentFilter) keywordHit(text string) (blockReason, bool) {
	if f == nil || len(f.keywords) == 0 {
		return blockReason{}, false
	}
	lower := normalize(text)
	if lower == "" {
		return blockReason{}, false
	}
	for _, word := range f.keywords {
		if k := normalize(word); k != "" && strings.Contains(lower, k) {
			return blockReason{Kind: filterKindKeyword, Value: word}, true
		}
	}
	return blockReason{}, false
}

// matchItem 判定一条列表行是否命中。作者为空或「匿名」不参与用户匹配；
// 节点只按显示名匹配，Node.Name 为空时跳过；关键词对帖子匹配标题，
// 动态与评论行再带上正文摘要（列表接口不返回帖子正文，这是接口限制）。
func (f *contentFilter) matchItem(it Item) (blockReason, bool) {
	if f == nil {
		return blockReason{}, false
	}
	author, text, node := it.Author, it.Title, it.Node.Name
	if x := it.Extra; x != nil {
		if x.Author != "" {
			author = x.Author // 最新评论行的 Item.Author 是空的
		}
		if x.Excerpt != "" {
			text += " " + x.Excerpt
		}
		if node == "" {
			node = x.Node.Name
		}
	}
	if r, ok := f.userHit(author); ok {
		return r, true
	}
	if r, ok := f.nodeHit(node); ok {
		return r, true
	}
	return f.keywordHit(text)
}

// matchComment 判定一条详情评论。子回复由渲染层单独处理：这里只折叠这一条。
// 已删除的评论本来就只显示「该评论已删除」，不再盖一层提示。
func (f *contentFilter) matchComment(author, content string, deleted bool) (blockReason, bool) {
	if f == nil || deleted {
		return blockReason{}, false
	}
	if r, ok := f.userHit(author); ok {
		return r, true
	}
	return f.keywordHit(content)
}

// contentFilterState 只在 UI 线程读写；网络结果经 publish 回来。
type contentFilterState struct {
	filter  *contentFilter // nil 表示「不知道」，此时不过滤
	loaded  bool
	loading bool
	err     string
	gen     uint64
	force   bool // 有 force 请求在等当前请求回来

	// saving / savingUser 是写请求的在途状态：资料卡按钮与管理页保存按钮
	// 都读它，期间不接受第二次提交。savingUser 非空表示快捷屏蔽某个用户名。
	saving     bool
	savingUser string

	// 失败退避，写法同 comment_cost.go 的 triedAt / failures。
	triedAt  time.Time
	failures int
	synced   bool // 本生命周期内至少成功 GET 过一次

	// pendingSince 用于「首次加载最多等 5 秒」：超时按无名单放行。
	pendingSince time.Time

	// 管理页草稿：chip 的增删只动这里，点保存才 PUT 整份替换。
	draft     contentFilterDTO
	dirty     bool
	entry     [3]string // 三个区块的输入框草稿，与 draft 三个数组一一对应
	fieldErrs [3]string // 用户 / 节点 / 关键词，对应服务端 400 的 field
	saveErr   string
}

func newContentFilterState() *contentFilterState { return &contentFilterState{} }

// ready 回答首页是否可以放行：名单未落定前最多等 5 秒，超时或失败都按
// 「无名单」继续，不能把首页卡死。未登录没有名单，立刻放行。
func (s *contentFilterState) ready(loggedIn bool) bool {
	if s == nil || !loggedIn {
		return true
	}
	if s.loaded || s.err != "" {
		return true
	}
	if !s.pendingSince.IsZero() && time.Since(s.pendingSince) >= contentFilterWait {
		return true
	}
	return false
}

// retryAfter 是 GET 失败后的退避：30s → 1m → 5m，封顶 5m。
func (s *contentFilterState) retryAfter() time.Duration {
	delay := 30 * time.Second
	for i := 1; i < s.failures && delay < 5*time.Minute; i++ {
		delay *= 2
		if delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
	}
	return delay
}

// decodeStringArray 容错读字符串数组：null、缺字段、元素类型异常都按空数组
// 处理。v1 只承诺加字段，三个数组「恒存在」仍要用容错读法兜住。
func decodeStringArray(raw json.RawMessage) []string {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		var s string
		if json.Unmarshal(item, &s) == nil && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func decodeContentFilter(d json.RawMessage) contentFilterDTO {
	o := asObject(d)
	if o == nil {
		return contentFilterDTO{}
	}
	return contentFilterDTO{
		BlockedUsers: decodeStringArray(o["blocked_users"]),
		BlockedNodes: decodeStringArray(o["blocked_nodes"]),
		Keywords:     decodeStringArray(o["keywords"]),
	}
}

func (c *Client) fetchContentFilter(ctx context.Context) (contentFilterDTO, error) {
	return withAuthRetry(c, ctx, func(token string) (contentFilterDTO, error) {
		d, err := c.getJSON(ctx, c.apiURL(contentFilterPath), token)
		if err != nil {
			return contentFilterDTO{}, err
		}
		return decodeContentFilter(d), nil
	})
}

// saveContentFilter writes the whole list in one PUT. The endpoint does not
// require an Idempotency-Key: submitting the same body twice is a no-op.
func (c *Client) saveContentFilter(ctx context.Context, body contentFilterDTO) (contentFilterDTO, error) {
	return withAuthRetry(c, ctx, func(token string) (contentFilterDTO, error) {
		d, err := c.putJSON(ctx, c.apiURL(contentFilterPath), token, body)
		if err != nil {
			return contentFilterDTO{}, err
		}
		return decodeContentFilter(d), nil
	})
}

type contentFilterCache struct {
	DTO     contentFilterDTO `json:"dto"`
	SavedAt time.Time        `json:"saved_at"`
}

func (a *app) contentFilterCacheKey() string {
	if a.auth == nil || !a.auth.loggedIn() {
		return ""
	}
	scope := a.persistableScope()
	if scope == "" || scope == anonymousScope {
		return ""
	}
	return contentFilterCachePrefix + scope
}

// loadContentFilterCache 同步摆上首帧底稿：有缓存时名单瞬间可用，首页不必
// 等一个网络往返。缓存损坏由 localKV.get 按未命中处理（只记日志）。
func (a *app) loadContentFilterCache() {
	s := a.contentFilter
	if s == nil {
		return
	}
	key := a.contentFilterCacheKey()
	if key == "" {
		return
	}
	var c contentFilterCache
	if _, ok := a.kv.get(key, &c); !ok {
		return
	}
	s.filter = newContentFilter(c.DTO)
	s.draft = cloneFilterDTO(c.DTO)
	s.loaded = true
}

func (a *app) writeContentFilterCache(d contentFilterDTO) {
	key := a.contentFilterCacheKey()
	if key == "" {
		return
	}
	logCacheWriteError(a.kv.put(key, contentFilterCache{DTO: cloneFilterDTO(d), SavedAt: time.Now()}, time.Now()))
}

// resetContentFilter 在换账号 / 退出时整体重建：名单是账号数据，绝不能让
// 下一个账号继承上一个账号的快照。磁盘缓存仍按新账号作用域重新读。
func (a *app) resetContentFilter() {
	a.contentFilter = newContentFilterState()
	a.loadContentFilterCache()
	// 退出登录后快照清空，之前被过滤掉的条目也要按空名单还原。
	a.applyFilterToLoadedTabs()
}

// 共享名单快照的只读判断：渲染与按钮都走它们，不各自拆字符串。
func (a *app) filterSnapshot() *contentFilter {
	if a.contentFilter == nil {
		return nil
	}
	return a.contentFilter.filter
}

func (a *app) filterBlocksUser(name string) bool {
	_, ok := a.filterSnapshot().userHit(name)
	return ok
}

func (a *app) filterBlocksNode(name string) bool {
	_, ok := a.filterSnapshot().nodeHit(name)
	return ok
}

func (a *app) filterKeywordHit(text string) (string, bool) {
	r, ok := a.filterSnapshot().keywordHit(text)
	return r.Value, ok
}

func (a *app) commentBlocked(author, content string, deleted bool) (blockReason, bool) {
	return a.filterSnapshot().matchComment(author, content, deleted)
}

// filterItems 由 raw 派生可见列表，hidden 是本页被剔除的条数。
// 没有名单或未开启过滤的 tab 直接返回原切片，零额外开销。
func (a *app) filterItems(spec tabSpec, raw []Item) ([]Item, int) {
	f := a.filterSnapshot()
	if f == nil || !spec.FilterBlocked {
		return raw, 0
	}
	out := make([]Item, 0, len(raw))
	hidden := 0
	for _, it := range raw {
		if _, blocked := f.matchItem(it); blocked {
			hidden++
			continue
		}
		out = append(out, it)
	}
	return out, hidden
}

// ensureContentFilter 是守卫式任务：每个 app 生命周期只拉一次名单，失败按
// 30s→1m→5m 退避；force 用于「我的 → 内容屏蔽」（每次切进去先看网页端最新）。
func (a *app) ensureContentFilter(force bool) {
	s := a.contentFilter
	if s == nil || a.client == nil || a.auth == nil || !a.auth.loggedIn() {
		return
	}
	if s.loading {
		if force {
			s.force = true
		}
		return
	}
	if !force {
		if s.synced {
			return
		}
		if !s.triedAt.IsZero() && time.Since(s.triedAt) < s.retryAfter() {
			return
		}
		if a.client.rateGate().remaining() > 0 {
			return
		}
		if st := a.proxy.netStatus(); st != nil && st.isOffline() {
			return
		}
	}
	s.loading = true
	s.force = false
	s.err = ""
	if s.pendingSince.IsZero() {
		s.pendingSince = time.Now()
	}
	s.gen++
	gen := s.gen
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		dto, err := a.client.fetchContentFilter(ctx)
		a.publish(func() {
			if a.contentFilter != s || s.gen != gen {
				return
			}
			s.loading = false
			s.triedAt = time.Now()
			// 当前请求在途时又有人点了「重试」：这一次结束后立刻再拉一次。
			again := s.force
			s.force = false
			if err != nil {
				s.failures++
				s.err = friendlyError(err)
				if a.handleAuthErrorFor(toastProfile, err) {
					return
				}
				if again {
					a.ensureContentFilter(true)
				}
				return
			}
			s.failures = 0
			s.err = ""
			s.synced = true
			s.loaded = true
			s.filter = newContentFilter(dto)
			s.draft = cloneFilterDTO(dto)
			s.dirty = false
			s.fieldErrs = [3]string{}
			s.saveErr = ""
			a.writeContentFilterCache(dto)
			a.applyFilterToLoadedTabs()
			a.clearRevealed()
			if again {
				a.ensureContentFilter(true)
			}
		})
	}()
}

// sanitizeFilterList 按服务端规则清洗：去首尾空白、丢空串、大小写不敏感去重
// （保留第一次出现的写法）。与网页端保存的结果保持一致。
func sanitizeFilterList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		v := strings.TrimSpace(s)
		if v == "" {
			continue
		}
		k := normalize(v)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, v)
	}
	return out
}

// validateFilterDTO 在发请求前拦下所有已知限制，不把服务端的 400 当校验器。
// 返回空串表示通过；否则是可直接展示的原因。
func validateFilterDTO(d contentFilterDTO, self string) string {
	lists := [][]string{d.BlockedUsers, d.BlockedNodes, d.Keywords}
	limits := []int{contentFilterMaxUserLen, contentFilterMaxNodeLen, contentFilterMaxUserLen}
	for i, list := range lists {
		if len(list) > contentFilterMaxPerList {
			return fmt.Sprintf("每类名单最多 %d 条", contentFilterMaxPerList)
		}
		for _, s := range list {
			if strings.TrimSpace(s) == "" {
				return "名单里不能有空项"
			}
			if utf8.RuneCountInString(s) > limits[i] {
				return "单项过长，请删减后再保存"
			}
			if i == 0 && self != "" && strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(self)) {
				return "不能屏蔽自己"
			}
		}
	}
	// 整份 URL 编码后的体积：中文按 9 字节算（url.QueryEscape 正好给出这个口径）。
	bytes := 20 * 3 // 三个数组固定开销
	for _, list := range lists {
		for _, s := range list {
			bytes += 4 + len(url.QueryEscape(s))
		}
	}
	if bytes > contentFilterMaxBytes {
		return "名单过长，请删减后再保存"
	}
	return ""
}

// writeContentFilter 是快捷屏蔽与管理页保存共用的写路径。成功用响应里的
// 名单覆盖快照（服务端会 trim / 去重，以它为准），写回缓存，并重算所有
// 已加载列表与评论区展开态。失败保留原状态，按钮不做乐观切换。
func (a *app) writeContentFilter(d contentFilterDTO, owner toastTarget, onOK func()) {
	s := a.contentFilter
	if s == nil || s.saving || a.client == nil || a.auth == nil {
		return
	}
	if !a.auth.loggedIn() {
		a.requestLoginFor(owner, "登录后才能修改内容屏蔽名单。")
		return
	}
	if st := a.proxy.netStatus(); st != nil && st.isOffline() {
		a.toast(owner, "离线时不能修改屏蔽名单")
		return
	}
	d = contentFilterDTO{
		BlockedUsers: sanitizeFilterList(d.BlockedUsers),
		BlockedNodes: sanitizeFilterList(d.BlockedNodes),
		Keywords:     sanitizeFilterList(d.Keywords),
	}
	if why := validateFilterDTO(d, a.auth.username()); why != "" {
		s.saveErr = why
		a.toast(owner, why)
		return
	}
	s.saving = true
	s.saveErr = ""
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	go func() {
		defer cancel()
		saved, err := a.client.saveContentFilter(ctx, d)
		a.publish(func() {
			if a.contentFilter != s {
				return
			}
			s.saving = false
			s.savingUser = ""
			if err != nil {
				if a.handleAuthErrorFor(owner, err) {
					return
				}
				// 400 invalid_request 带 field 时把服务端文案放到对应那块下面，
				// 用户不必猜是哪一类名单写错了。
				var ae *apiError
				if errors.As(err, &ae) && ae.Status == http.StatusBadRequest && ae.Code == "invalid_request" {
					switch ae.Field {
					case "blocked_users":
						s.fieldErrs[0] = ae.Message
					case "blocked_nodes":
						s.fieldErrs[1] = ae.Message
					case "keywords":
						s.fieldErrs[2] = ae.Message
					}
				}
				s.saveErr = friendlyError(err)
				a.toast(owner, s.saveErr)
				return
			}
			s.filter = newContentFilter(saved)
			s.loaded = true
			s.synced = true
			s.err = ""
			s.failures = 0
			s.draft = cloneFilterDTO(saved)
			s.dirty = false
			s.fieldErrs = [3]string{}
			s.saveErr = ""
			a.writeContentFilterCache(saved)
			a.applyFilterToLoadedTabs()
			a.clearRevealed()
			if onOK != nil {
				onOK()
			}
		})
	}()
}

// setUserBlocked 是资料卡 / 详情页共用的快捷屏蔽入口：在快照上增删后走
// 同一条写路径。不做乐观切换；失败时按钮回到原状态，toast 报告原因。
func (a *app) setUserBlocked(name string, blocked bool) {
	name = strings.TrimSpace(name)
	if name == "" || a.isMine(name) {
		return
	}
	if a.auth == nil || !a.auth.loggedIn() {
		a.requestLoginFor(toastMain, "登录后才能屏蔽用户。")
		return
	}
	s := a.contentFilter
	if s == nil || s.saving {
		return
	}
	base := contentFilterDTO{}
	if f := a.filterSnapshot(); f != nil {
		base = cloneFilterDTO(f.raw)
	}
	d := cloneFilterDTO(base)
	if blocked {
		d.BlockedUsers = append(d.BlockedUsers, name)
	} else {
		kept := make([]string, 0, len(d.BlockedUsers))
		for _, u := range d.BlockedUsers {
			if !strings.EqualFold(strings.TrimSpace(u), name) {
				kept = append(kept, u)
			}
		}
		d.BlockedUsers = kept
	}
	s.savingUser = name
	a.writeContentFilter(d, toastMain, func() {
		if blocked {
			a.toast(toastMain, fmt.Sprintf("已屏蔽 %s", name))
		} else {
			a.toast(toastMain, fmt.Sprintf("已取消屏蔽 %s", name))
		}
	})
}

// applyFilterToLoadedTabs 用 raw 重算所有已加载的过滤列表。屏蔽 / 取消后
// 立即生效，滚动位置尽量不动；取消时之前剔除的条目原样回来。
func (a *app) applyFilterToLoadedTabs() {
	for i, st := range a.tabs {
		if st == nil || !st.loaded || len(st.raw) == 0 {
			continue
		}
		spec := a.specFor(i)
		if !spec.FilterBlocked {
			continue
		}
		st.items, st.hidden = a.filterItems(spec, st.raw)
		st.refold(spec)
	}
}

// clearRevealed 丢掉所有详情状态的单条展开态。名单变化后展开态与规则可能
// 不一致，统一回到折叠；关掉详情本来也会丢，这里只是提前一步。
func (a *app) clearRevealed() {
	if d := a.detail; d != nil {
		d.revealed = nil
	}
	for _, d := range a.detailStack {
		d.revealed = nil
	}
	for _, h := range a.detailWindows {
		for _, d := range h.states {
			d.revealed = nil
		}
		for _, stack := range h.stacks {
			for _, d := range stack {
				d.revealed = nil
			}
		}
	}
}
