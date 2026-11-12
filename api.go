package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	apiBaseURL      = "https://2libra.com/api"
	siteBaseURL     = "https://2libra.com"
	communityNodeID = "eb3e8ec7-6b0f-43bf-956f-7a18ca23396a"
	defaultLimit    = 50
)

// pagingMode describes how a tab asks for its next page.
type pagingMode uint8

const (
	pagingOff     pagingMode = iota // 不分页：金币页、我的帖子 / 合集等整页返回
	pagingPages                     // 第 2 页起带 page
	pagingIgnored                   // 最新评论：服务端忽略 page，不做翻页
)

type itemKind uint8

const (
	kindTopic itemKind = iota
	kindComment
	kindCollection
	kindTimeline     // 动态：同一份响应里混有帖子与评论
	kindNotification // 通知：行渲染与交互都独立
)

// tabSpec is the exact contract from the API table: path, page-1 query,
// the list field inside d, whether a token is needed, and how paging works.
type tabSpec struct {
	Label     string
	Path      string
	Query     string // 原样保留第 1 页的 query，不改参数名、不加参数
	ListKey   string // posts / items / ""（d 本身是数组）/ auto（文档未约定）
	NeedsAuth bool
	Paging    pagingMode
	Kind      itemKind
}

var tabSpecs = []tabSpec{
	{"首页", "/v1/topics", "page=1&limit=50&parent_slug=All&sort=value", "posts", false, pagingPages, kindTopic},
	{"今日热议", "/v1/topics/hot", "", "posts", false, pagingPages, kindTopic},
	{"近期热议", "/v1/topics/hot", "period=7d", "posts", false, pagingPages, kindTopic},
	{"新发表", "/v1/topics/latest", "", "posts", false, pagingPages, kindTopic},
	// 最新评论端点仍只接受 ≤50（服务端返回「每页最多显示 50 条」），
	// 不要跟着帖子评论的 100 一起改。
	{"最新评论", "/v1/comments/latest", "limit=50", "items", false, pagingIgnored, kindComment},
	{"社区讨论", "/v1/topics", "page=1&limit=50&node_id=" + communityNodeID, "posts", false, pagingPages, kindTopic},
	{"动态", "/v1/me/follows/timeline", "page=1&limit=50", "items", true, pagingPages, kindTimeline},
	// 收藏 / 书签 / 我的帖子 / 我的合集不在侧边栏：它们是「我的」窗口里的
	// 四类内容，端点见 profile.go 的 profileContentSpecs。
	// 通知走开放 API 的消息列表；字段名是 list（不是 items），筛选另带
	// type / unread_only，由 notificationQuery 生成。它不再是侧边栏页面：
	// 入口是标题栏铃铛打开的弹窗，保留这一项是为了复用通用加载与分页。
	{"通知", notificationsPath, notificationsQuery(notificationFilterAll), "list", true, pagingPages, kindNotification},
	// 金币页聚合多个只读端点，不再是侧边栏页面：它是独立的金币窗口，入口
	// 见 coins_window.go；加载由 coinState 统一调度，不走通用 fetch。
}

func tabLabels() []string {
	out := make([]string, len(tabSpecs))
	for i, s := range tabSpecs {
		out[i] = s.Label
	}
	return out
}

// endpoint keeps the page-1 query byte-for-byte as specified. Only from
// page 2 on do we replace/add page, and never introduce any other param.
func endpoint(base string, s tabSpec, page int) string {
	q := s.Query
	if page > 1 {
		v, err := url.ParseQuery(q)
		if err != nil {
			v = url.Values{}
		}
		v.Set("page", strconv.Itoa(page))
		q = v.Encode()
	}
	u := strings.TrimSuffix(base, "/") + s.Path
	if q != "" {
		u += "?" + q
	}
	return u
}

// Item is one row of the native list, whatever the source endpoint.
type Item struct {
	Key       string
	Title     string
	Subtitle  string
	Meta      string
	URL       string // 站外原文链接，保留作者署名与来源时使用
	ShortID   string // 帖子对外 ID；非空才可以在应用内打开详情
	CommentID string // 从评论列表进入时，要定位的评论 uuid
	// Author / AuthorAvatar / SubtitlePrefix / SubtitleSuffix 把「节点 · 昵称 ·
	// 时间」这类副标题里的昵称拆出来，交给统一的用户标识组件，在昵称旁
	// 画头像。Subtitle 仍是完整文本，供测试与无障碍读取，不参与渲染。
	Author         string
	AuthorAvatar   string
	AuthorBadges   []badgeInfo
	SubtitlePrefix string
	SubtitleSuffix string
	// Extra 是最新评论 / 动态行的富展示字段；普通榜单行为 nil。
	Extra *rowExtra
	// Notif 是通知行归一后的消息；只有通知数据的行非 nil。
	Notif *notification
	// Stats 是帖子列表行的时效信息；合集、阅读记录等非帖子行为 nil。
	Stats *replyStats
	// Pinned 标记置顶帖：列表行的置顶角标读它，不用再从 Meta 文本里猜。
	Pinned bool
}

// replyStats 是帖子列表接口里与「最近一次回复」有关的一组字段：
//
//	latest_commented_at       最后回复时间（无人回复时等于发帖时间）
//	last_commented_by.username 最后回复人
//	extra.latest_comment_floor 最新（也是最高）楼层
//	comment_count             回复数
//
// 时间在解码时已经转成「3 分钟前」这种展示文本，界面直接读。
type replyStats struct {
	Replier  string
	Updated  string
	MaxFloor int
	Replies  int
}

type pageResult struct {
	Items      []Item
	Total      int
	Page       int
	Limit      int
	TotalPages int
}

type pageMeta struct {
	Total, Page, Limit, TotalPages int
}

// envelope is the {c,m,d,t} wrapper used by every /api/* response.
// code only exists for /api/v1/*.
type envelope struct {
	C             int             `json:"c"`
	M             string          `json:"m"`
	D             json.RawMessage `json:"d"`
	T             int64           `json:"t"`
	Code          string          `json:"code"`
	U             string          `json:"u"`
	RequiredScope []string        `json:"required_scope"`
	RetryAfter    int             `json:"retry_after"`
	Field         string          `json:"field"`
}

type apiError struct {
	Status        int
	Code          string
	Message       string
	RequiredScope []string
	RetryAfter    int
}

func (e *apiError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, e.Message)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
}

// friendly turns the machine-readable code into an actionable hint.
func (e *apiError) friendly() string {
	switch e.Code {
	case "unauthorized":
		return "登录已失效（401 unauthorized），请重新登录"
	case "insufficient_scope":
		if len(e.RequiredScope) > 0 {
			return "缺少权限范围 " + strings.Join(e.RequiredScope, ", ") + "，请重新授权"
		}
		return "缺少权限范围（403 insufficient_scope），请重新授权"
	case "permission_denied":
		return e.Message // 用户级限制，直接展示服务端文案
	case "rate_limited":
		if e.RetryAfter > 0 {
			return fmt.Sprintf("请求过于频繁，请 %d 秒后重试", e.RetryAfter)
		}
		return "请求过于频繁（429 rate_limited），请稍后重试"
	case "not_found":
		return "内容不存在或不可见（404 not_found）"
	case "invalid_request":
		return "参数错误（400 invalid_request）：" + e.Message
	case "internal_error":
		return "服务端错误（500），请稍后重试"
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Error()
}

func friendlyError(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.friendly()
	}
	return err.Error()
}

// Client performs the read requests. Auth may be nil for anonymous-only use.
type Client struct {
	HTTP   *http.Client
	Base   string
	Auth   *AuthManager
	UA     string
	bearer func() string // test seam

	// Proxy 提供全应用共享的 transport：普通请求和 SSE 长连接都从这里
	// 拿客户端，代理规则因此只需维护一份。测试可直接覆盖 HTTP / StreamHTTP。
	Proxy *proxyManager

	// StreamHTTP is used only by the notification SSE connection. A client
	// with an overall Timeout would cut the stream; nil falls back to HTTP
	// (tests) and then to a client without a timeout.
	StreamHTTP *http.Client

	// 以下三个只给测试压缩重连节奏用；生产用 stream.go 的默认值。
	streamBackoffSeq     []time.Duration
	streamReconnectDelay time.Duration
	streamIdleTimeout    time.Duration
}

func (c *Client) base() string {
	if c.Base != "" {
		return c.Base
	}
	return apiBaseURL
}

func (c *Client) accessToken() string {
	if c.bearer != nil {
		return c.bearer()
	}
	if c.Auth != nil {
		return c.Auth.accessToken()
	}
	return ""
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	if c.Proxy != nil {
		return c.Proxy.Client(20 * time.Second)
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// withAuthRetry runs one GET-shaped read with the current token. A 401 with
// a token triggers one single-flight refresh and exactly one retry; a failed
// refresh drops the session. Every read endpoint goes through here, so the
// detail / replies / emojis paths behave exactly like the list tabs.
func withAuthRetry[T any](c *Client, ctx context.Context, fn func(string) (T, error)) (T, error) {
	token := c.accessToken()
	res, err := fn(token)
	if err == nil {
		return res, nil
	}
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != http.StatusUnauthorized || token == "" || c.Auth == nil {
		return res, err
	}
	fresh, rerr := c.Auth.refresh(ctx, token)
	if rerr != nil {
		// 只有授权真的失效（4xx）才清登录态；网络抖动或服务端 5xx 时保留
		// 令牌，避免一次断网就把用户踢下线。
		if authFailure(rerr) {
			c.Auth.invalidate(rerr)
		}
		return res, rerr
	}
	res, err = fn(fresh)
	// 新令牌也被 401 拒绝：会话确实没了，别再让每个请求反复刷新。
	if err != nil && errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
		c.Auth.invalidate(errors.New("登录已失效，请重新登录"))
	}
	return res, err
}

// fetch loads one page.
func (c *Client) fetch(ctx context.Context, s tabSpec, page int) (pageResult, error) {
	return withAuthRetry(c, ctx, func(token string) (pageResult, error) {
		return c.get(ctx, s, page, token)
	})
}

func (c *Client) get(ctx context.Context, s tabSpec, page int, token string) (pageResult, error) {
	d, err := c.getJSON(ctx, endpoint(c.base(), s, page), token)
	if err != nil {
		return pageResult{}, err
	}
	return decodePage(d, s, page)
}

// getJSON performs one GET and unwraps the {c,m,d,t} envelope. The caller
// gets d verbatim; unknown fields stay untouched for the decoder to ignore.
func (c *Client) getJSON(ctx context.Context, u, token string) (json.RawMessage, error) {
	var empty json.RawMessage
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return empty, err
	}
	ua := c.UA
	if ua == "" {
		ua = "2libra-native-reader/0.1 (+mygo)"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")
	// Never send an empty Authorization header: the API answers 401.
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return empty, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return empty, err
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return empty, fmt.Errorf("响应不是 {c,m,d,t} 信封（HTTP %d）：%w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 || env.C != 0 {
		return empty, &apiError{
			Status:        resp.StatusCode,
			Code:          env.Code,
			Message:       env.M,
			RequiredScope: env.RequiredScope,
			RetryAfter:    env.RetryAfter,
		}
	}
	return env.D, nil
}

// postJSON sends one POST with a JSON body and unwraps the {c,m,d,t} envelope,
// exactly like getJSON. It is the single write path: auth and idempotency
// headers and the same error mapping as the reads all live here, so the reply
// creator (and any future writes) reuse the read side's contract. A 2xx status
// is required (201 for resource creation, 200 for other writes); anything
// else becomes an apiError carrying code/required_scope/retry_after.
func (c *Client) postJSON(ctx context.Context, u, token, idempotencyKey string, payload any) (json.RawMessage, error) {
	return c.writeJSON(ctx, http.MethodPost, u, token, idempotencyKey, payload)
}

// deleteJSON sends one DELETE without a body through the same write path.
// Endpoints such as unfollow answer with the usual envelope and no payload.
func (c *Client) deleteJSON(ctx context.Context, u, token string) (json.RawMessage, error) {
	return c.writeJSON(ctx, http.MethodDelete, u, token, "", nil)
}

// writeJSON is the shared implementation of the write methods. A nil payload
// sends no body (and no Content-Type), which some endpoints require.
func (c *Client) writeJSON(ctx context.Context, method, u, token, idempotencyKey string, payload any) (json.RawMessage, error) {
	var empty json.RawMessage
	var reqBody io.Reader
	hasBody := payload != nil
	if hasBody {
		buf, err := json.Marshal(payload)
		if err != nil {
			return empty, err
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return empty, err
	}
	ua := c.UA
	if ua == "" {
		ua = "2libra-native-reader/0.1 (+mygo)"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")
	// 只有带 body 的写请求才声明 JSON；「全部已读」这类端点不接受多余内容。
	if hasBody {
		req.Header.Set("Content-Type", "application/json")
	}
	// Never send an empty Authorization header: the API answers 401.
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	// The Idempotency-Key is mandatory on create endpoints (index.md 4.1):
	// retrying after a timeout with the same key is safe and will not post
	// twice; a fresh action mints a new key when calling postJSON.
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return empty, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return empty, err
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return empty, fmt.Errorf("响应不是 {c,m,d,t} 信封（HTTP %d）：%w", resp.StatusCode, err)
	}
	if resp.StatusCode >= 400 || env.C != 0 {
		return empty, &apiError{
			Status:        resp.StatusCode,
			Code:          env.Code,
			Message:       env.M,
			RequiredScope: env.RequiredScope,
			RetryAfter:    env.RetryAfter,
		}
	}
	return env.D, nil
}

func decodePage(d json.RawMessage, s tabSpec, page int) (pageResult, error) {
	raws, meta, err := extractList(d, s.ListKey)
	if err != nil {
		return pageResult{}, err
	}
	out := pageResult{
		Items:      make([]Item, 0, len(raws)),
		Total:      meta.Total,
		Page:       meta.Page,
		Limit:      meta.Limit,
		TotalPages: meta.TotalPages,
	}
	if out.Page == 0 {
		out.Page = page
	}
	if out.Limit == 0 {
		out.Limit = defaultLimit
	}
	if out.Total == 0 {
		out.Total = len(raws)
	}
	if out.TotalPages == 0 {
		out.TotalPages = 1
	}
	for i, raw := range raws {
		it := itemFrom(raw, s.Kind)
		if it.Key == "" {
			it.Key = fmt.Sprintf("%s:%d:%d", s.Label, page, i)
		}
		out.Items = append(out.Items, it)
	}
	return out, nil
}

// extractList uses the tab's declared field as the single source of truth.
// Only tabs whose response shape the docs leave unspecified ("auto", or d
// being a bare array) fall back to the other known list field names.
func extractList(d json.RawMessage, key string) ([]json.RawMessage, pageMeta, error) {
	var meta pageMeta
	d = bytes.TrimSpace(d)
	if len(d) == 0 || bytes.Equal(d, []byte("null")) {
		return nil, meta, nil
	}
	var items []json.RawMessage
	if d[0] == '[' {
		if err := json.Unmarshal(d, &items); err != nil {
			return nil, meta, err
		}
		return items, meta, nil
	}
	if d[0] != '{' {
		return nil, meta, fmt.Errorf("无法识别的 d 类型：%s", string(d[:min(len(d), 24)]))
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(d, &obj); err != nil {
		return nil, meta, err
	}
	meta.Total = rawInt(obj["total"])
	meta.Page = rawInt(obj["page"])
	meta.Limit = rawInt(obj["limit"])
	meta.TotalPages = rawInt(obj["total_pages"])
	keys := []string{}
	switch {
	case key != "" && key != "auto":
		keys = []string{key} // 严格按端点约定
	default:
		keys = []string{"posts", "items", "topics", "collections", "list", "data", "records", "results"}
	}
	for _, k := range keys {
		if raw, ok := obj[k]; ok && len(bytes.TrimSpace(raw)) > 0 && bytes.TrimSpace(raw)[0] == '[' {
			if err := json.Unmarshal(raw, &items); err != nil {
				return nil, meta, err
			}
			break
		}
	}
	return items, meta, nil
}

func rawInt(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if json.Unmarshal(raw, &n) == nil {
		return n
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return int(f)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		i, _ := strconv.Atoi(s)
		return i
	}
	return 0
}

// object is a tolerant view over a JSON object: unknown/new fields are
// ignored, and several historical spellings can be read without guessing
// a single "one size fits all" list getter.
type object map[string]json.RawMessage

func asObject(raw json.RawMessage) object {
	var o object
	if len(raw) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return nil
	}
	if json.Unmarshal(raw, &o) != nil {
		return nil
	}
	return o
}

func (o object) str(keys ...string) string {
	for _, k := range keys {
		if raw, ok := o[k]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil && s != "" {
				return s
			}
		}
	}
	return ""
}

func (o object) num(keys ...string) int {
	for _, k := range keys {
		if raw, ok := o[k]; ok {
			if n := rawInt(raw); n != 0 {
				return n
			}
		}
	}
	return 0
}

func (o object) flag(keys ...string) bool {
	for _, k := range keys {
		if raw, ok := o[k]; ok {
			var b bool
			if json.Unmarshal(raw, &b) == nil && b {
				return true
			}
		}
	}
	return false
}

func (o object) sub(keys ...string) object {
	for _, k := range keys {
		if raw, ok := o[k]; ok {
			if v := asObject(raw); v != nil {
				return v
			}
		}
	}
	return nil
}

// has reports whether the object carries any of the keys. Nested wrappers are
// not consulted: kind detection must look only at the outer object.
func (o object) has(keys ...string) bool {
	for _, k := range keys {
		if _, ok := o[k]; ok {
			return true
		}
	}
	return false
}

// overlay copies keys from nested wrappers (post / topic / reply / ...)
// that are missing from the outer object. Timelines and bookmarks wrap
// their payload this way.
func overlay(o object, keys ...string) {
	for _, k := range keys {
		if sub := o.sub(k); sub != nil {
			for key, value := range sub {
				if _, exists := o[key]; !exists {
					o[key] = value
				}
			}
		}
	}
}

var shortCode = regexp.MustCompile(`:[A-Za-z0-9_\x{4e00}-\x{9fa5}]+:`)

func cleanText(s string) string {
	s = shortCode.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func relativeTime(s string) string {
	if s == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			d := time.Since(t)
			switch {
			case d < time.Minute:
				return "刚刚"
			case d < time.Hour:
				return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
			case d < 24*time.Hour:
				return fmt.Sprintf("%d 小时前", int(d.Hours()))
			case d < 30*24*time.Hour:
				return fmt.Sprintf("%d 天前", int(d.Hours()/24))
			}
			return t.Format("2006-01-02")
		}
	}
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func topicURL(shortID, nodeSlug string) string {
	if shortID == "" {
		return ""
	}
	if nodeSlug == "" {
		return siteBaseURL + "/post/" + shortID
	}
	return siteBaseURL + "/post/" + nodeSlug + "/" + shortID
}

func itemFrom(raw json.RawMessage, kind itemKind) Item {
	o := asObject(raw)
	if o == nil {
		return Item{}
	}
	if kind == kindNotification {
		return notificationItem(o)
	}
	if kind == kindTimeline {
		// 动态流同一份响应里混有帖子与评论。判型必须在 overlay 之前：
		// overlay 会把帖子正文摊平到外层，之后就分不清了。
		if looksLikeComment(o) {
			kind = kindComment
		} else {
			kind = kindTopic
		}
	}
	overlay(o, "post", "topic", "reply", "comment", "collection", "item", "target")
	if kind == kindComment {
		return commentItemFrom(o)
	}

	author := o.sub("author", "user", "created_by", "owner")
	node := o.sub("node", "parent_node")
	post := o.sub("post", "topic")
	postNode := object(nil)
	if post != nil {
		postNode = post.sub("node", "parent_node")
	}
	username := "匿名"
	avatarURL := ""
	var authorBadges []badgeInfo
	if author != nil {
		if name := author.str("username", "name", "nickname"); name != "" {
			username = name
		}
		avatarURL = authorAvatar(author)
		authorBadges = decodeBadges(author)
	}
	nodeName := ""
	nodeSlug := ""
	if node != nil {
		nodeName = node.str("name", "title")
		nodeSlug = node.str("slug")
	}
	if nodeName == "" && postNode != nil {
		nodeName = postNode.str("name", "title")
	}
	if nodeSlug == "" && postNode != nil {
		nodeSlug = postNode.str("slug")
	}
	created := o.str("created_at", "createdAt", "updated_at", "published_at")
	if created == "" && post != nil {
		created = post.str("created_at", "createdAt")
	}
	when := relativeTime(created)

	// 列表接口在帖子对象上直接给出最后回复人、最后回复时间、最高楼层与
	// 回复数（实测字段见 replyStats）。这几项只属于帖子行，评论与合集不走。
	stats := &replyStats{
		Replier: authorName(o.sub("last_commented_by", "last_replier", "latest_commenter")),
		Updated: relativeTime(o.str("latest_commented_at", "last_commented_at")),
		// extra 可能为 null；latest_comment_floor 是顶层楼层号，楼中楼不占楼层，
		// 所以它就是最高楼层。
		MaxFloor: o.sub("extra").num("latest_comment_floor", "max_floor"),
		Replies:  o.num("comment_count", "reply_count", "comments"),
	}

	shortID := o.str("short_id", "shortId")
	if shortID == "" && post != nil {
		shortID = post.str("short_id", "shortId")
	}
	id := o.str("id", "uuid", "reply_id", "comment_id")
	content := cleanText(o.str("content", "body", "text", "note", "remark", "description"))
	title := cleanText(o.str("title", "name", "subject"))
	if title == "" && post != nil {
		title = cleanText(post.str("title", "name"))
	}

	it := Item{Key: id, ShortID: shortID}
	switch kind {
	case kindCollection:
		it.Title = title
		if it.Title == "" {
			it.Title = content
		}
		if it.Title == "" {
			it.Title = "未命名合集"
		}
		parts := []string{"合集"}
		if username != "" && username != "匿名" {
			parts = append(parts, username)
			// 合集行本来就不展示匿名作者：只有真有昵称时才拆出作者段。
			it.Author = username
			it.AuthorAvatar = avatarURL
			it.AuthorBadges = authorBadges
			it.SubtitlePrefix = "合集 · "
			if when != "" {
				it.SubtitleSuffix = " · " + when
			}
		}
		if when != "" {
			parts = append(parts, when)
		}
		it.Subtitle = strings.Join(parts, " · ")
		count := o.num("topic_count", "post_count", "count", "total", "item_count", "posts_count")
		if count > 0 {
			it.Meta = fmt.Sprintf("%d 个帖子", count)
		}
		// 合集详情地址文档未约定，不拼未经证实的链接。
	default: // kindTopic, also used for 动态
		it.Title = title
		if it.Title == "" {
			it.Title = content
		}
		if it.Title == "" {
			it.Title = "（无标题）"
		}
		if shortID != "" {
			it.Key = shortID
		}
		parts := []string{}
		if nodeName != "" {
			parts = append(parts, nodeName)
			it.SubtitlePrefix = nodeName + " · "
		}
		parts = append(parts, username)
		it.Author = username
		it.AuthorAvatar = avatarURL
		it.AuthorBadges = authorBadges
		if when != "" {
			parts = append(parts, when)
			it.SubtitleSuffix = " · " + when
		}
		it.Subtitle = strings.Join(parts, " · ")
		it.Pinned = o.flag("is_pinned", "pinned")
		it.Stats = stats
		meta := []string{}
		if it.Pinned {
			meta = append(meta, "置顶")
		}
		if n := o.num("comment_count", "reply_count", "comments"); n > 0 {
			meta = append(meta, fmt.Sprintf("%d 回复", n))
		}
		if n := o.num("views", "view_count"); n > 0 {
			meta = append(meta, fmt.Sprintf("%d 浏览", n))
		}
		if n := o.num("score", "hot_score"); n > 0 {
			meta = append(meta, fmt.Sprintf("价值 %d", n))
		}
		it.Meta = strings.Join(meta, " · ")
		it.URL = topicURL(shortID, nodeSlug)
		// 动态流会带正文、表情表态与打赏；普通榜单没有这些字段，
		// topicExtra 返回 nil，行还是原来的紧凑样式。
		it.Extra = topicExtra(o, username, avatarURL, authorBadges, nodeName, when)
	}
	return it
}
