package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// 「编辑」窗口的数据、校验与请求：改已发表的帖子、改自己的评论、给帖子追加
// 附言。三个端点逐字照抄 openapi v1 的 api-write.md：
//
//	POST /v1/topics/:short_id/edit          编辑帖子（只传要改的字段）
//	POST /v1/replies/:id/edit               编辑评论
//	POST /v1/topics/:short_id/postscript 🔑 追加附言（必须带 Idempotency-Key）
//
// 三条容易写错的规则：
//   - 编辑帖子「传了就必须非空」：正文要清空时不能发空串（会 400），所以空
//     正文干脆不发这个字段；界面上把正文清空是误操作，本地就拦住。
//   - 只允许作者本人：两个 edit 端点非作者返回 403 permission_denied，所以
//     入口只在「昵称等于当前账号」时出现（见 isMine）。
//   - 附言是追加式的，创建后不能改也不能删，窗口里要先说清楚。
//
// 三个端点都不返回约定的响应体（文档只写了请求），所以成功后一律用本地提交
// 的值更新页面与缓存，不拿响应去猜字段。
//
// 编辑时限：文档里没有。api-write.md 的「编辑帖子」「评论」两节只写了作者本人
// 与 last-write-wins（并发以后提交的为准），index.md 的配额一节也只有通用调用
// 频次；terms.md 与 changelog.md 同样没有任何「发布后 X 分钟内可改」「最多改
// 几次」的规则。读端点也没有 can_edit / editable_until / edited_at 这类字段
// （帖子的 updated_at 语义未定义，评论根本没有这个字段），所以客户端**不做**
// 本地时限判断——编一个窗口出来只会在允许编辑时把入口藏掉。真正的裁决在服务端：
// 超时或超次数时它回 403 permission_denied，m 是可读的中文原因，friendlyError
// 会原样展示（permission_denied 不走授权链路，见 auth_gate.go），按钮留着让
// 用户重试是安全的。

// editKind 是编辑窗口这次在改什么。
type editKind int

const (
	editKindTopic      editKind = iota // 帖子：标题 + 正文
	editKindReply                      // 评论：正文
	editKindPostscript                 // 附言：正文，追加式
)

func topicEditPath(shortID string) string {
	return topicPath(shortID) + "/edit"
}

func replyEditPath(replyID string) string {
	return "/v1/replies/" + url.PathEscape(replyID) + "/edit"
}

func topicPostscriptPath(shortID string) string {
	return topicPath(shortID) + "/postscript"
}

// editTopic 提交帖子要改的字段。调用方只放真正改动过的键：文档要求传了的
// 字段必须非空，空串也等于「提交了空值」；清除可见性门槛用
// access_guard_type: "none"，不是省略字段。
func (c *Client) editTopic(ctx context.Context, shortID string, payload map[string]any) error {
	if len(payload) == 0 {
		return fmt.Errorf("没有要修改的内容")
	}
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		_, err := c.postJSON(ctx, c.apiURL(topicEditPath(shortID)), token, "", payload)
		return struct{}{}, err
	})
	return err
}

// editReply 提交一条评论的新正文。
func (c *Client) editReply(ctx context.Context, replyID, content string) error {
	payload := map[string]any{"content": content}
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		_, err := c.postJSON(ctx, c.apiURL(replyEditPath(replyID)), token, "", payload)
		return struct{}{}, err
	})
	return err
}

// addPostscript 给帖子追加一段附言。端点标了 🔑：幂等键由调用方给，同一份
// 内容重试必须复用同一个键，否则超时重发会追加两条一样的附言。
func (c *Client) addPostscript(ctx context.Context, shortID, content, key string) error {
	payload := map[string]any{"content": content}
	_, err := withAuthRetry(c, ctx, func(token string) (struct{}, error) {
		_, err := c.postJSON(ctx, c.apiURL(topicPostscriptPath(shortID)), token, key, payload)
		return struct{}{}, err
	})
	return err
}

// editGuardKeep 是编辑窗口可见范围下拉框的第一项：不发送任何 access_guard_*
// 字段，保持帖子当前的限制。发帖窗口的「所有人可见」对应「不设置」，而编辑
// 窗口清除限制必须显式发送 access_guard_type: "none"，两者不能混用。
const editGuardKeep = "keep"

// editGuardLabels / editGuardValues 是编辑窗口的下拉框选项，顺序一一对应。
// 第一项是「保持当前设置」，避免在读响应没有带门槛字段时，改个标题就把作者
// 原来的限制悄悄清掉。
var editGuardLabels = []string{"保持当前设置", "所有人可见", "登录用户可见", "达到等级可见", "满足属性可见"}

var editGuardValues = []string{editGuardKeep, guardNone, guardLogin, guardLevel, guardAttribute}

func guardFromEditLabel(label string) string {
	for i, l := range editGuardLabels {
		if l == label {
			return editGuardValues[i]
		}
	}
	return editGuardKeep
}

func editGuardLabel(typ string) string {
	for i, v := range editGuardValues {
		if v == typ {
			return editGuardLabels[i]
		}
	}
	return editGuardKeep
}

// normalizeGuardType 把服务端可能回传的 "none" / 空串统一成 guardNone。
func normalizeGuardType(typ string) string {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "", "none":
		return guardNone
	case guardLogin:
		return guardLogin
	case guardLevel:
		return guardLevel
	case guardAttribute:
		return guardAttribute
	}
	return strings.ToLower(strings.TrimSpace(typ))
}

// guardSelection 是可见性门槛的一份取值。Type 取 guardNone / guardLogin /
// guardLevel / guardAttribute；Level 与 Attr* 只在对应类型下有值。
type guardSelection struct {
	Type      string
	Level     int
	Attr      string
	AttrValue int
}

// editGuard 把详情里的 access_guard_* 转成编辑窗口的初始值。读响应没有带
// 这个字段、或类型不认识时返回 false：窗口从「保持当前设置」开始，作者仍可
// 主动重新设置或清除，但不会因为一个默认值而误改。
func (t *topicDetail) editGuard() (guardSelection, bool) {
	if t == nil || !t.GuardKnown {
		return guardSelection{}, false
	}
	typ := normalizeGuardType(t.GuardType)
	switch typ {
	case guardNone, guardLogin, guardLevel, guardAttribute:
	default:
		return guardSelection{}, false
	}
	return guardSelection{
		Type:      typ,
		Level:     t.GuardLevel,
		Attr:      strings.TrimSpace(t.GuardAttr),
		AttrValue: t.GuardAttrValue,
	}, true
}

// editState 是编辑窗口的全部状态。只在 UI 线程读写；后台请求完成后通过
// publish 回填，窗口关掉就整体丢弃（不做草稿持久化：编辑是一次性的短操作，
// 留一份旧正文反而会在下次打开时盖掉最新内容）。
type editState struct {
	kind    editKind
	shortID string // 帖子 short_id：编辑帖子与追加附言用
	replyID string // 评论 uuid：编辑评论用

	title string // 只有编辑帖子时有
	body  string

	// 可见范围：只对编辑帖子有意义。guardChoice 存下拉框文案；origGuard /
	// guardKnown 是打开窗口时详情里的原值，用来判断「真的改了」。
	guardChoice    string
	guardLevel     float64
	guardAttr      string
	guardAttrValue float64
	origGuard      guardSelection
	guardKnown     bool
	// origTitle / origBody 是打开窗口时的原文：用来判断「有没有改动」，
	// 也是正文不能清空这条校验的基准。
	origTitle, origBody string

	// 预览用的两份东西：编辑帖子按正文排版，编辑评论按评论排版；社区自定义
	// 表情（:code: 短码）在帖子与评论里都可能出现，打开窗口时一并带上。
	mdOpts *markdown.Options
	emojis map[string]string

	titleField ui.Handle
	bodyField  ui.Handle
	// focusBody 表示打开窗口后把焦点放进正文（编辑评论 / 追加附言没有标题，
	// 第一件事就是改正文）。
	focusBody bool
	// winPreview 是右侧预览栏的开关，默认显示：改一段旧文字时对照着看更省心。
	winPreview bool

	busy bool
	// key / keySig 是附言的幂等键与铸键时那份正文的摘要（见 submitEdit）：
	// 授权重跑与超时重试复用同一个键，内容一改就换新键。
	key    string
	keySig string

	mdEditor
}

func newTopicEditState(t *topicDetail, mdOpts *markdown.Options) *editState {
	st := &editState{
		kind:           editKindTopic,
		shortID:        t.ShortID,
		title:          t.Title,
		body:           t.Content,
		origTitle:      strings.TrimSpace(t.Title),
		origBody:       t.Content,
		guardChoice:    editGuardKeep,
		guardLevel:     composeGuardLevelLo,
		guardAttrValue: 0,
		emojis:         t.Emojis,
		mdOpts:         mdOpts, // 正文排版（a.md）
		winPreview:     true,
		mdEditor:       newMDEditor(),
	}
	if guard, ok := t.editGuard(); ok {
		st.origGuard = guard
		st.guardKnown = true
		st.guardChoice = editGuardLabel(guard.Type)
		if guard.Level >= composeGuardLevelLo {
			st.guardLevel = float64(guard.Level)
		}
		st.guardAttr = guard.Attr
		if guard.AttrValue >= 0 {
			st.guardAttrValue = float64(guard.AttrValue)
		}
	}
	return st
}

func newReplyEditState(shortID string, r replyItem, mdOpts *markdown.Options) *editState {
	return &editState{
		kind:       editKindReply,
		shortID:    shortID,
		replyID:    r.ID,
		body:       r.Content,
		origBody:   r.Content,
		emojis:     r.Emojis,
		mdOpts:     mdOpts, // 评论按评论字号排版（a.commentMd）
		focusBody:  true,
		winPreview: true,
		mdEditor:   newMDEditor(),
	}
}

func newPostscriptState(shortID string, emojis map[string]string, mdOpts *markdown.Options) *editState {
	return &editState{
		kind:       editKindPostscript,
		shortID:    shortID,
		emojis:     emojis,
		mdOpts:     mdOpts,
		focusBody:  true,
		winPreview: true,
		mdEditor:   newMDEditor(),
	}
}

// windowTitle 是自绘标题栏与窗口标题用的名字。
func (st *editState) windowTitle() string {
	switch st.kind {
	case editKindTopic:
		return "编辑帖子"
	case editKindReply:
		return "编辑评论"
	case editKindPostscript:
		return "追加附言"
	}
	return "编辑"
}

// submitLabel 是底部主按钮的文案。附言是「追加」，不是「保存」：它不会覆盖
// 原文，说清楚动作才不会被误读成改写正文。
func (st *editState) submitLabel() string {
	if st.kind == editKindPostscript {
		return "发布附言"
	}
	return "保存修改"
}

// doneText 是成功后的 toast 文案。
func (st *editState) doneText() string {
	switch st.kind {
	case editKindTopic:
		return "帖子已更新"
	case editKindReply:
		return "评论已更新"
	case editKindPostscript:
		return "附言已追加"
	}
	return "已保存"
}

// hint 是编辑区上方的一句说明。附言是追加式的，这一点必须提前讲清楚。
func (st *editState) hint() string {
	switch st.kind {
	case editKindPostscript:
		return "附言会追加到帖子末尾，发布后不能修改或删除。"
	case editKindReply:
		return "只改这条评论的正文；改动会直接显示给所有读者。"
	}
	return "改完点保存：标题和正文都不能留空。"
}

// changed 报告这份编辑有没有实际改动。标题按去空白后比较，正文按原样比较：
// 多一个空格也是改动，不能替用户裁掉。
func (st *editState) changed() bool {
	if st.kind == editKindTopic {
		if strings.TrimSpace(st.title) != st.origTitle {
			return true
		}
		if st.guardChanged() {
			return true
		}
	}
	return st.body != st.origBody
}

// selectedGuard 把下拉框的当前选择转成请求体取值。guardKeep 不会走到这里：
// 调用方先看 guardChanged。
func (st *editState) selectedGuard() guardSelection {
	sel := guardSelection{Type: guardFromEditLabel(st.guardChoice)}
	if sel.Type == guardLevel {
		sel.Level = int(st.guardLevel)
	}
	if sel.Type == guardAttribute {
		sel.Attr = strings.TrimSpace(st.guardAttr)
		sel.AttrValue = int(st.guardAttrValue)
	}
	return sel
}

// guardChanged 报告可见范围是否被作者改过。保持当前设置永远不算改动；原值
// 没读到时，作者主动选了某一项就算改动（那是一次明确的重设）。
func (st *editState) guardChanged() bool {
	if st.kind != editKindTopic || st.guardChoice == editGuardKeep {
		return false
	}
	sel := st.selectedGuard()
	if !st.guardKnown {
		return true
	}
	if sel.Type != st.origGuard.Type {
		return true
	}
	switch sel.Type {
	case guardLevel:
		return sel.Level != st.origGuard.Level
	case guardAttribute:
		return sel.Attr != st.origGuard.Attr || sel.AttrValue != st.origGuard.AttrValue
	}
	return false
}

// guardPayload 把改过的可见范围写进编辑请求体。清除限制必须显式发
// access_guard_type: "none"（api-write.md 的编辑帖子一节）；没改就一个字段
// 都不发，避免把作者在网页端设的门槛冲掉。
func (st *editState) guardPayload(p map[string]any) error {
	if !st.guardChanged() {
		return nil
	}
	sel := st.selectedGuard()
	switch sel.Type {
	case guardNone:
		p["access_guard_type"] = "none"
	case guardLogin:
		p["access_guard_type"] = guardLogin
	case guardLevel:
		if sel.Level < composeGuardLevelLo || sel.Level > composeGuardLevelHi {
			return fmt.Errorf("可见等级应在 %d～%d 之间", composeGuardLevelLo, composeGuardLevelHi)
		}
		p["access_guard_type"] = guardLevel
		p["access_guard_level"] = sel.Level
	case guardAttribute:
		if sel.Attr == "" {
			return fmt.Errorf("按属性可见需要填写 D20 属性名")
		}
		if sel.AttrValue < 0 || sel.AttrValue > composeGuardAttrHi {
			return fmt.Errorf("属性值应在 0～%d 之间", composeGuardAttrHi)
		}
		p["access_guard_type"] = guardAttribute
		p["access_guard_attr"] = sel.Attr
		p["access_guard_attr_value"] = sel.AttrValue
	}
	return nil
}

// payload 组装请求体并完成校验：返回的 error 就是 toast 里那句话。
func (st *editState) payload() (map[string]any, error) {
	if n := utf8.RuneCountInString(st.body); n > composeContentMax {
		return nil, fmt.Errorf("正文最多 %d 字，当前 %d 字", composeContentMax, n)
	}
	if n := editorPendingUploads(st.body); n > 0 {
		return nil, fmt.Errorf("还有 %d 张图片未处理完，请等上传结束或移除后再保存", n)
	}
	switch st.kind {
	case editKindTopic:
		title := strings.TrimSpace(st.title)
		if title == "" {
			return nil, fmt.Errorf("标题不能为空")
		}
		if n := utf8.RuneCountInString(title); n > composeTitleMax {
			return nil, fmt.Errorf("标题最多 %d 字，当前 %d 字", composeTitleMax, n)
		}
		p := map[string]any{"title": title}
		// 原文有正文时不允许清空：文档要求 content 传了就必须非空，而「把正文
		// 删光」在接口上等于「不改正文」，静默保留旧正文会让人以为改成功了。
		if strings.TrimSpace(st.body) == "" {
			if strings.TrimSpace(st.origBody) != "" {
				return nil, fmt.Errorf("正文不能为空")
			}
		} else {
			p["content"] = st.body
		}
		if err := st.guardPayload(p); err != nil {
			return nil, err
		}
		return p, nil
	case editKindReply:
		if strings.TrimSpace(st.body) == "" {
			return nil, fmt.Errorf("评论内容不能为空")
		}
		return map[string]any{"content": st.body}, nil
	case editKindPostscript:
		if strings.TrimSpace(st.body) == "" {
			return nil, fmt.Errorf("附言不能为空")
		}
		return map[string]any{"content": st.body}, nil
	}
	return nil, fmt.Errorf("未知的编辑类型")
}

// submit 按类型发请求。payload 已经在 submitEdit 里校验过，这里只做分发。
func (st *editState) submit(ctx context.Context, c *Client, payload map[string]any, key string) error {
	content, _ := payload["content"].(string)
	switch st.kind {
	case editKindTopic:
		return c.editTopic(ctx, st.shortID, payload)
	case editKindReply:
		return c.editReply(ctx, st.replyID, content)
	case editKindPostscript:
		return c.addPostscript(ctx, st.shortID, content, key)
	}
	return fmt.Errorf("未知的编辑类型")
}

// postscriptKey 返回这次附言要用的幂等键：内容没变就复用（授权重跑、超时
// 重试不会追加两条），内容变了换新键（同一个键配不同正文会被判 409）。
func (st *editState) postscriptKey() string {
	if st.kind != editKindPostscript {
		return ""
	}
	if st.key != "" && st.keySig == st.body {
		return st.key
	}
	st.key = randomURLSafe(24)
	st.keySig = st.body
	return st.key
}

// isMine 报告某个昵称是不是当前登录账号。编辑接口只允许作者本人，界面上也
// 只在作者本人时给出入口：匿名帖的 author 为 null（解码成「匿名」），无法
// 确认是不是自己发的，所以不给入口，避免点开只换来一句 403。
func (a *app) isMine(author string) bool {
	if a.auth == nil || author == "" || author == "匿名" {
		return false
	}
	name := strings.TrimSpace(a.auth.username())
	return name != "" && name == author
}

// canEditTopic 报告这篇帖子此刻是否给出「编辑 / 追更」入口。
func (a *app) canEditTopic(d *detailState) bool {
	return d != nil && d.topic != nil && a.isMine(d.topic.Author)
}

// canEditReply 报告这条评论此刻是否给出「编辑」入口。已删除的评论不能改。
func (a *app) canEditReply(r replyItem) bool {
	return r.ID != "" && !r.Deleted && a.isMine(r.Author)
}

// applyEdit 把提交成功的改动落到页面上：正文、标题与附言都在本地就地更新，
// 不去重发一次详情请求（那次请求还可能拿到 CDN 上的旧副本）。同一篇帖子已经
// 不在页面上时只提示，不动别的状态。
func (a *app) applyEdit(st *editState) {
	d := a.detail
	if d == nil || d.shortID != st.shortID {
		return
	}
	switch st.kind {
	case editKindTopic:
		if d.topic == nil {
			return
		}
		d.topic.Title = strings.TrimSpace(st.title)
		d.topic.Content = st.body
		d.topic.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if st.guardChanged() {
			sel := st.selectedGuard()
			d.topic.GuardKnown = true
			d.topic.GuardType = sel.Type
			d.topic.GuardLevel = sel.Level
			d.topic.GuardAttr = sel.Attr
			d.topic.GuardAttrValue = sel.AttrValue
		}
		// 列表行里的标题也要跟着换：返回列表时不该还是旧标题。
		a.renameTopicRows(d.shortID, d.topic.Title)
	case editKindReply:
		at := replyIndexByID(d.replyList, st.replyID)
		if at < 0 {
			return
		}
		d.replyList[at].Content = st.body
		// 折叠状态可能让这条评论不显示，重建一次显示列表最稳。
		d.rebuildDisplay()
	case editKindPostscript:
		if d.topic == nil {
			return
		}
		d.topic.Postscripts = append(d.topic.Postscripts, postscript{
			Content:   st.body,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
			Emojis:    d.topic.Emojis,
		})
	}
	a.saveCachedDetail(d)
}

// renameTopicRows 把各 tab 里这篇帖子的标题换成新标题。只改内存里的列表行，
// 不发请求：编辑已经成功，列表下次刷新会拿到服务端的规范值。
func (a *app) renameTopicRows(shortID, title string) {
	if shortID == "" || title == "" {
		return
	}
	for i := range a.tabs {
		items := a.tabs[i].items
		for j := range items {
			if items[j].ShortID == shortID {
				items[j].Title = title
			}
		}
	}
}

// editToast 把编辑相关的结果弹在编辑窗口里；窗口不在了由 toast 的退回规则
// 交给主窗口，结果不会丢。
func (a *app) editToast(text string) {
	a.toast(toastEdit, text)
}
