package main

import (
	"encoding/binary"
	"fmt"
	"hash"
	"hash/fnv"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// 本地草稿：发帖 / 动态、评论 / 回复的输入内容按账号落在磁盘上。进程异常退出
// （崩溃、断电、任务管理器结束）后，重新打开能从草稿箱原样恢复，包括「发一条
// 新评论」还是「回复某一层楼」。
//
// 数据模型与运行态严格分开：composeState / detailState 里全是 ui.Handle、定时器
// 与编辑器状态，不能直接序列化，所以这里单独定义一份 DTO，双向显式映射。
// 落盘复用 auth.go 的 writeJSONFile（先 .tmp 再原子改名），不引入数据库，也不
// 复用 offline_store.go 的 localKV —— 那是整份重写的缓存，不适合逐字保存。

const (
	draftVersion     = 1
	draftMaxReply    = 200
	draftMaxCompose  = 50
	draftMaxFileSize = 4 << 20 // 4 MiB
	// draftDebounce 是停手后落盘的延迟；draftMaxDelay 是连续打字时距上次改动
	// 的最长落盘间隔，保证崩溃最多丢 5 秒内容。
	draftDebounce = 800 * time.Millisecond
	draftMaxDelay = 5 * time.Second
)

const (
	draftKindCompose = "compose"
	draftKindReply   = "reply"
)

// draftTarget 是「正在回复哪一层楼」的快照。零值表示发一条新的顶层评论。
type draftTarget struct {
	ID       string `json:"id,omitempty"`
	Level    int    `json:"level,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Floor    int    `json:"floor,omitempty"`
	Author   string `json:"author,omitempty"`
	AuthorID string `json:"author_id,omitempty"`
}

func (t draftTarget) toRuntime() replyTarget {
	return replyTarget{
		ID: t.ID, Level: t.Level, ParentID: t.ParentID,
		Floor: t.Floor, Author: t.Author, AuthorID: t.AuthorID,
	}
}

func draftTargetFrom(rt replyTarget) draftTarget {
	return draftTarget{
		ID: rt.ID, Level: rt.Level, ParentID: rt.ParentID,
		Floor: rt.Floor, Author: rt.Author, AuthorID: rt.AuthorID,
	}
}

// composeDraftModules 是发帖表单右侧栏那些附加组件。每个块只在它自己的开关
// 打开时写入，未知字段一律忽略（与 API 解码同一套容错口径）。
type composeDraftModules struct {
	Vote       *composeDraftVote       `json:"vote,omitempty"`
	Score      *composeDraftScore      `json:"score,omitempty"`
	Bet        *composeDraftBet        `json:"bet,omitempty"`
	Redeem     *composeDraftRedeem     `json:"redeem,omitempty"`
	Pool       *composeDraftPool       `json:"pool,omitempty"`
	Guard      *composeDraftGuard      `json:"guard,omitempty"`
	Restricted *composeDraftRestricted `json:"restricted,omitempty"`
}

type composeDraftVote struct {
	TypeIndex  int       `json:"type_index,omitempty"`
	Title      string    `json:"title,omitempty"`
	Options    []string  `json:"options,omitempty"`
	Checked    []bool    `json:"checked,omitempty"`
	MaxChoices float64   `json:"max_choices,omitempty"`
	DeadlineOn bool      `json:"deadline_on,omitempty"`
	DeadlineAt time.Time `json:"deadline_at,omitempty"`
}

type composeDraftScore struct {
	Target  string  `json:"target,omitempty"`
	Value   float64 `json:"value,omitempty"`
	Comment string  `json:"comment,omitempty"`
}

type composeDraftBet struct {
	Title      string    `json:"title,omitempty"`
	Options    []string  `json:"options,omitempty"`
	Checked    []bool    `json:"checked,omitempty"`
	DeadlineOn bool      `json:"deadline_on,omitempty"`
	DeadlineAt time.Time `json:"deadline_at,omitempty"`
}

type composeDraftRedeem struct {
	Codes            string  `json:"codes,omitempty"`
	Description      string  `json:"description,omitempty"`
	MaxRedeemPerUser float64 `json:"max_redeem_per_user,omitempty"`
	RedPacket        bool    `json:"red_packet,omitempty"`
	RedPacketIndex   int     `json:"red_packet_index,omitempty"`
}

type composeDraftPool struct {
	Coins       float64 `json:"coins,omitempty"`
	Probability float64 `json:"probability,omitempty"`
}

type composeDraftGuard struct {
	Choice    string  `json:"choice,omitempty"`
	Level     float64 `json:"level,omitempty"`
	Attr      string  `json:"attr,omitempty"`
	AttrValue float64 `json:"attr_value,omitempty"`
}

type composeDraftRestricted struct {
	AccessChoice    string    `json:"access_choice,omitempty"`
	Content         string    `json:"content,omitempty"`
	D20Attr         string    `json:"d20_attr,omitempty"`
	D20Threshold    float64   `json:"d20_threshold,omitempty"`
	DurationMonths  float64   `json:"duration_months,omitempty"`
	AuctionTopN     float64   `json:"auction_top_n,omitempty"`
	AuctionDeadline time.Time `json:"auction_deadline,omitempty"`
}

// draftEntry 是磁盘上的一条草稿。Mode 沿用 composeModePost / composeModeMoment。
type draftEntry struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"` // compose | reply
	Mode      int       `json:"mode,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// compose
	Title     string               `json:"title,omitempty"`
	Body      string               `json:"body,omitempty"`
	Moment    string               `json:"moment,omitempty"`
	NodeID    string               `json:"node_id,omitempty"`
	NodeLabel string               `json:"node_label,omitempty"`
	Anonymous bool                 `json:"anonymous,omitempty"`
	Modules   *composeDraftModules `json:"modules,omitempty"`

	// reply
	ShortID   string      `json:"short_id,omitempty"`
	PostTitle string      `json:"post_title,omitempty"`
	Text      string      `json:"text,omitempty"`
	ReplyTo   draftTarget `json:"reply_to,omitempty"`
}

type draftFile struct {
	Version int          `json:"version"`
	Account string       `json:"account"`
	Entries []draftEntry `json:"entries"`
}

// draftStore 是一个账号（或一个 scope）的全部草稿。目录与文件名只由 accountIDFor
// 生成，字符集是 [a-z0-9_-]，不需要再转义。每个账号窗口各自持有一份实例。
type draftStore struct {
	mu      sync.Mutex
	dir     string
	scope   string
	path    string
	entries []draftEntry
}

func newDraftStore(dir, scope string) *draftStore {
	s := &draftStore{dir: dir, scope: scope}
	if dir != "" {
		s.path = filepath.Join(dir, "drafts", scope+".json")
	}
	s.load()
	return s
}

func (s *draftStore) load() {
	if s.path == "" {
		return
	}
	var f draftFile
	if err := readJSONFile(s.path, &f); err != nil {
		if os.IsNotExist(err) {
			return
		}
		// 文件损坏（半截 JSON、手工改坏）：另存 .bad，从空草稿继续，只记日志。
		// 草稿读不出来不该让应用起不来。
		log.Printf("草稿文件 %s 读不出来（%v），另存为 .bad 后从空草稿开始", s.path, err)
		if _, statErr := os.Stat(s.path + ".bad"); statErr == nil {
			_ = os.Rename(s.path, s.path+".bad."+time.Now().Format("20060102150405"))
		} else {
			_ = os.Rename(s.path, s.path+".bad")
		}
		return
	}
	if f.Version != draftVersion {
		log.Printf("草稿文件 %s 的版本 %d 不认识，按空草稿处理", s.path, f.Version)
		return
	}
	// 旧版只选了节点 / 开了组件也会落盘，这类没有标题和正文的条目不再算草稿。
	entries := make([]draftEntry, 0, len(f.Entries))
	for _, e := range f.Entries {
		if e.Kind == draftKindCompose && !composeDraftHasText(e) {
			continue
		}
		entries = append(entries, e)
	}
	s.entries = entries
}

func (s *draftStore) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return nil
	}
	s.pruneLocked()
	f := draftFile{Version: draftVersion, Account: s.scope, Entries: s.entries}
	if err := writeJSONFile(s.path, &f); err != nil {
		return err
	}
	// 写盘后如果体积超限，继续丢最旧的回复草稿直到装得下再写一次。
	if info, statErr := os.Stat(s.path); statErr == nil && info.Size() > draftMaxFileSize {
		for info.Size() > draftMaxFileSize && len(s.entries) > 0 {
			s.dropOldestLocked(draftKindReply)
			if len(s.entries) == 0 || !s.hasKindLocked(draftKindReply) {
				s.dropOldestLocked("")
			}
			if err := writeJSONFile(s.path, &draftFile{Version: draftVersion, Account: s.scope, Entries: s.entries}); err != nil {
				return err
			}
			info, statErr = os.Stat(s.path)
			if statErr != nil {
				break
			}
		}
	}
	return nil
}

func (s *draftStore) hasKindLocked(kind string) bool {
	for _, e := range s.entries {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func (s *draftStore) put(e draftEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID == e.ID && s.entries[i].Kind == e.Kind {
			s.entries[i] = e
			return
		}
	}
	s.entries = append(s.entries, e)
}

func (s *draftStore) delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteLocked(id)
}

func (s *draftStore) deleteLocked(id string) {
	out := s.entries[:0]
	for _, e := range s.entries {
		if e.ID != id {
			out = append(out, e)
		}
	}
	clear(s.entries[len(out):])
	s.entries = out
}

func (s *draftStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
}

func (s *draftStore) find(id string) (draftEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.ID == id {
			return e, true
		}
	}
	return draftEntry{}, false
}

// rows 返回按更新时间倒序的副本：草稿箱按「最近编辑」排。
func (s *draftStore) rows() []draftEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]draftEntry, len(s.entries))
	copy(out, s.entries)
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func (s *draftStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// latestCompose 找最近编辑的一条指定模式的发帖草稿，供 FAB 续写。
func (s *draftStore) latestCompose(mode int) (draftEntry, bool) {
	for _, e := range s.rows() {
		if e.Kind == draftKindCompose && e.Mode == mode {
			return e, true
		}
	}
	return draftEntry{}, false
}

func (s *draftStore) pruneLocked() {
	var compose, reply []draftEntry
	for _, e := range s.entries {
		switch e.Kind {
		case draftKindReply:
			reply = append(reply, e)
		default:
			if !composeDraftHasText(e) {
				continue
			}
			compose = append(compose, e)
		}
	}
	sortByUpdated := func(list []draftEntry) {
		sort.Slice(list, func(i, j int) bool { return list[i].UpdatedAt.After(list[j].UpdatedAt) })
	}
	sortByUpdated(compose)
	sortByUpdated(reply)
	if len(compose) > draftMaxCompose {
		log.Printf("发帖草稿超过 %d 条，丢弃 %d 条最旧的", draftMaxCompose, len(compose)-draftMaxCompose)
		compose = compose[:draftMaxCompose]
	}
	if len(reply) > draftMaxReply {
		log.Printf("回复草稿超过 %d 条，丢弃 %d 条最旧的", draftMaxReply, len(reply)-draftMaxReply)
		reply = reply[:draftMaxReply]
	}
	s.entries = append(append(make([]draftEntry, 0, len(compose)+len(reply)), compose...), reply...)
}

func (s *draftStore) dropOldestLocked(kind string) {
	if len(s.entries) == 0 {
		return
	}
	idx := -1
	for i, e := range s.entries {
		if kind != "" && e.Kind != kind {
			continue
		}
		if idx < 0 || s.entries[i].UpdatedAt.Before(s.entries[idx].UpdatedAt) {
			idx = i
		}
	}
	if idx >= 0 {
		log.Printf("草稿文件超过 %d MiB，丢弃最旧的一条草稿 %s", draftMaxFileSize>>20, s.entries[idx].ID)
		s.entries = append(s.entries[:idx], s.entries[idx+1:]...)
	}
}

// draftScope 是草稿文件的隔离作用域：账号窗口用账号 id，未登录窗口用 guest，
// 旧版单账号会话用 local。
func draftScope(a *app) string {
	if a == nil || a.auth == nil || !a.auth.loggedIn() {
		return "guest"
	}
	if a.accountID != "" {
		return a.accountID
	}
	return "local"
}

// stripUploadPlaceholders 剥掉未完成的图片上传占位符。上传任务不跨进程，留着
// 会让恢复后的草稿永远发不出去（payload 会一直报「还有 N 张图片未处理完」）。
var uploadPlaceholderPattern = regexp.MustCompile(`!\[[^\]]*\]\(upload:[^)]*\)`)

func stripUploadPlaceholders(s string) string {
	return uploadPlaceholderPattern.ReplaceAllString(s, "")
}

// ------------------------------------------------------------------
// 指纹：判断「要不要落盘」。不给二十多个输入点挂 OnChange，帧尾算一次指纹即可。
// ------------------------------------------------------------------

type draftHash struct{ h hash.Hash64 }

func newDraftHash() *draftHash { return &draftHash{h: fnv.New64a()} }

func (x *draftHash) str(s string) {
	_, _ = x.h.Write([]byte(s))
	_, _ = x.h.Write([]byte{0}) // 分隔符，避免 "12|3" 与 "1|23" 撞在一起
}

func (x *draftHash) b(v bool) {
	if v {
		_, _ = x.h.Write([]byte{1})
	} else {
		_, _ = x.h.Write([]byte{0})
	}
}

func (x *draftHash) i(v int) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], uint64(int64(v)))
	_, _ = x.h.Write(buf[:])
}

func (x *draftHash) f(v float64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], math.Float64bits(v))
	_, _ = x.h.Write(buf[:])
}

func (x *draftHash) tm(v time.Time) {
	if v.IsZero() {
		x.i(0)
		return
	}
	x.i(int(v.UnixNano()))
}

func (x *draftHash) strs(v []string) {
	x.i(len(v))
	for _, s := range v {
		x.str(s)
	}
}

func (x *draftHash) bools(v []bool) {
	x.i(len(v))
	for _, b := range v {
		x.b(b)
	}
}

func (x *draftHash) sum() uint64 { return x.h.Sum64() }

// draftFingerprint 把 composeState 的可存字段按固定顺序哈希。顺序必须与
// composeState.draftEntry 的字段一一对应，改字段时两边一起改。
func (st *composeState) draftFingerprint() uint64 {
	if st == nil {
		return 0
	}
	x := newDraftHash()
	x.i(st.mode)
	x.str(st.title)
	x.str(stripUploadPlaceholders(st.body))
	x.str(stripUploadPlaceholders(st.momentDraft))
	x.str(st.nodeID)
	x.b(st.anonymous)

	if st.voteOpen {
		x.b(true)
		x.i(st.voteTypeIndex)
		x.str(st.voteTitle)
		x.strs(st.voteOptions)
		x.bools(st.voteChecked)
		x.f(st.voteMaxChoices)
		x.b(st.voteDeadlineOn)
		x.tm(st.voteDeadlineAt)
	} else {
		x.b(false)
	}
	if st.scoreOpen {
		x.b(true)
		x.str(st.scoreTarget)
		x.f(st.scoreValue)
		x.str(st.scoreComment)
	} else {
		x.b(false)
	}
	if st.betOpen {
		x.b(true)
		x.str(st.betTitle)
		x.strs(st.betOptions)
		x.bools(st.betChecked)
		x.b(st.betDeadlineOn)
		x.tm(st.betDeadlineAt)
	} else {
		x.b(false)
	}
	if st.redeemOpen {
		x.b(true)
		x.str(st.redeemCodes)
		x.str(st.codeDescription)
		x.f(st.maxRedeemPerUser)
		x.b(st.redPacket)
		x.i(st.redPacketIndex)
	} else {
		x.b(false)
	}
	if st.poolOpen {
		x.b(true)
		x.f(st.poolCoins)
		x.f(st.poolProbability)
	} else {
		x.b(false)
	}
	if st.guardOpen {
		x.b(true)
		x.str(st.guardChoice)
		x.f(st.guardLevel)
		x.str(st.guardAttr)
		x.f(st.guardAttrValue)
	} else {
		x.b(false)
	}
	if st.restrictedOpen {
		x.b(true)
		x.str(st.restrictedAccessChoice)
		x.str(st.restrictedContent)
		x.str(st.restrictedD20Attr)
		x.f(st.restrictedD20Threshold)
		x.f(st.restrictedDurationMonths)
		x.f(st.restrictedAuctionTopN)
		x.tm(st.restrictedAuctionDeadline)
	} else {
		x.b(false)
	}
	return x.sum()
}

func (d *detailState) draftFingerprint() uint64 {
	if d == nil {
		return 0
	}
	x := newDraftHash()
	x.str(stripUploadPlaceholders(d.draft))
	rt := draftTargetFrom(d.replyTo)
	x.str(rt.ID)
	x.i(rt.Level)
	x.str(rt.ParentID)
	x.i(rt.Floor)
	x.str(rt.Author)
	x.str(rt.AuthorID)
	return x.sum()
}

// ------------------------------------------------------------------
// 运行态 ↔ DTO 映射
// ------------------------------------------------------------------

func (st *composeState) draftEntry() draftEntry {
	e := draftEntry{
		ID: st.draftID, Kind: draftKindCompose, Mode: st.mode,
		Title:     st.title,
		Body:      stripUploadPlaceholders(st.body),
		Moment:    stripUploadPlaceholders(st.momentDraft),
		NodeID:    st.nodeID,
		Anonymous: st.anonymous,
	}
	if n := st.selectedNode(); n != nil {
		e.NodeLabel = n.parentDisplay()
	}
	mods := &composeDraftModules{}
	has := false
	if st.voteOpen {
		has = true
		mods.Vote = &composeDraftVote{
			TypeIndex: st.voteTypeIndex, Title: st.voteTitle,
			Options:    append([]string(nil), st.voteOptions...),
			Checked:    append([]bool(nil), st.voteChecked...),
			MaxChoices: st.voteMaxChoices, DeadlineOn: st.voteDeadlineOn,
			DeadlineAt: st.voteDeadlineAt,
		}
	}
	if st.scoreOpen {
		has = true
		mods.Score = &composeDraftScore{Target: st.scoreTarget, Value: st.scoreValue, Comment: st.scoreComment}
	}
	if st.betOpen {
		has = true
		mods.Bet = &composeDraftBet{
			Title:      st.betTitle,
			Options:    append([]string(nil), st.betOptions...),
			Checked:    append([]bool(nil), st.betChecked...),
			DeadlineOn: st.betDeadlineOn, DeadlineAt: st.betDeadlineAt,
		}
	}
	if st.redeemOpen {
		has = true
		mods.Redeem = &composeDraftRedeem{
			Codes: st.redeemCodes, Description: st.codeDescription,
			MaxRedeemPerUser: st.maxRedeemPerUser,
			RedPacket:        st.redPacket, RedPacketIndex: st.redPacketIndex,
		}
	}
	if st.poolOpen {
		has = true
		mods.Pool = &composeDraftPool{Coins: st.poolCoins, Probability: st.poolProbability}
	}
	if st.guardOpen {
		has = true
		mods.Guard = &composeDraftGuard{
			Choice: st.guardChoice, Level: st.guardLevel,
			Attr: st.guardAttr, AttrValue: st.guardAttrValue,
		}
	}
	if st.restrictedOpen {
		has = true
		mods.Restricted = &composeDraftRestricted{
			AccessChoice: st.restrictedAccessChoice, Content: st.restrictedContent,
			D20Attr: st.restrictedD20Attr, D20Threshold: st.restrictedD20Threshold,
			DurationMonths: st.restrictedDurationMonths, AuctionTopN: st.restrictedAuctionTopN,
			AuctionDeadline: st.restrictedAuctionDeadline,
		}
	}
	if has {
		e.Modules = mods
	}
	return e
}

// applyComposeDraft 全量覆盖一份发帖表单。顺序很关键：先停轮询，再覆盖状态，
// 然后清幂等键与回执，最后设 id、对齐指纹 —— 恢复这一下不该立刻再写一次盘。
func (a *app) applyComposeDraft(st *composeState, e draftEntry) {
	a.stopComposeNodeWatch(st)
	*st = *newComposeState()
	st.mode = e.Mode
	st.title = e.Title
	st.body = e.Body
	st.momentDraft = e.Moment
	st.nodeID = e.NodeID
	st.anonymous = e.Anonymous
	st.key, st.keySig = "", ""
	st.okTitle, st.okShortID, st.okURL, st.okPending = "", "", "", false
	if m := e.Modules; m != nil {
		if v := m.Vote; v != nil {
			st.voteOpen = true
			st.voteTypeIndex, st.voteTitle, st.voteOptions = v.TypeIndex, v.Title, append([]string(nil), v.Options...)
			st.voteChecked = append([]bool(nil), v.Checked...)
			st.voteMaxChoices, st.voteDeadlineOn, st.voteDeadlineAt = v.MaxChoices, v.DeadlineOn, v.DeadlineAt
		}
		if v := m.Score; v != nil {
			st.scoreOpen = true
			st.scoreTarget, st.scoreValue, st.scoreComment = v.Target, v.Value, v.Comment
		}
		if v := m.Bet; v != nil {
			st.betOpen = true
			st.betTitle, st.betOptions = v.Title, append([]string(nil), v.Options...)
			st.betChecked = append([]bool(nil), v.Checked...)
			st.betDeadlineOn, st.betDeadlineAt = v.DeadlineOn, v.DeadlineAt
		}
		if v := m.Redeem; v != nil {
			st.redeemOpen = true
			st.redeemCodes, st.codeDescription = v.Codes, v.Description
			st.maxRedeemPerUser, st.redPacket, st.redPacketIndex = v.MaxRedeemPerUser, v.RedPacket, v.RedPacketIndex
		}
		if v := m.Pool; v != nil {
			st.poolOpen = true
			st.poolCoins, st.poolProbability = v.Coins, v.Probability
		}
		if v := m.Guard; v != nil {
			st.guardOpen = true
			st.guardChoice, st.guardLevel, st.guardAttr, st.guardAttrValue = v.Choice, v.Level, v.Attr, v.AttrValue
			// 老草稿可能存的是自由输入的中文属性名；下拉框只认官网字典里的机器取值。
			if d20AttrLabel(st.guardAttr) == "" {
				st.guardAttr = d20Attrs[0].Value
			}
		}
		if v := m.Restricted; v != nil {
			st.restrictedOpen = true
			st.restrictedAccessChoice, st.restrictedContent = v.AccessChoice, v.Content
			st.restrictedD20Attr, st.restrictedD20Threshold = v.D20Attr, v.D20Threshold
			if d20AttrLabel(st.restrictedD20Attr) == "" {
				st.restrictedD20Attr = d20Attrs[0].Value
			}
			st.restrictedDurationMonths, st.restrictedAuctionTopN, st.restrictedAuctionDeadline =
				v.DurationMonths, v.AuctionTopN, v.AuctionDeadline
		}
	}
	st.draftID = e.ID
	st.draftFP, st.draftSavedFP = st.draftFingerprint(), st.draftFingerprint()
	st.focusTitle = strings.TrimSpace(e.Title) == ""
}

func (d *detailState) draftEntry() draftEntry {
	e := draftEntry{
		ID:      replyDraftID(d.shortID),
		Kind:    draftKindReply,
		ShortID: d.shortID,
		Text:    stripUploadPlaceholders(d.draft),
		ReplyTo: draftTargetFrom(d.replyTo),
	}
	if d.topic != nil {
		e.PostTitle = d.topic.Title
	}
	return e
}

func replyDraftID(shortID string) string { return "reply-" + shortID }

func (d *detailState) applyReplyDraft(e draftEntry) {
	d.draft = e.Text
	d.replyTo = e.ReplyTo.toRuntime()
	d.composerIdle = false
	d.focusComposer = true
	d.draftFP, d.draftSavedFP = d.draftFingerprint(), d.draftFingerprint()
}

// composeDraftHasText 判断一条发帖草稿有没有值得保存的内容：帖子看标题或正文，
// 动态看正文。只选了节点、切了匿名、开了投票等组件都不算草稿，免得草稿箱里
// 堆满没有半句话的条目；动态正文同样属于「正文」。
func composeDraftHasText(e draftEntry) bool {
	return strings.TrimSpace(e.Title) != "" ||
		strings.TrimSpace(e.Body) != "" ||
		strings.TrimSpace(e.Moment) != ""
}

func draftEntryEmpty(e draftEntry) bool {
	switch e.Kind {
	case draftKindReply:
		return strings.TrimSpace(e.Text) == "" && e.ReplyTo.ID == ""
	default:
		return !composeDraftHasText(e)
	}
}

// ------------------------------------------------------------------
// 自动保存：帧尾算指纹 → 防抖定时器 → publish 回 UI 线程落盘。
// ------------------------------------------------------------------

// liveDetailStates 列出所有还活着的详情状态：主窗口内联页、详情窗口各标签与
// 返回栈、独立写评论窗口借用的那一份。只在这些状态有输入框时算它们的指纹。
func (a *app) liveDetailStates() []*detailState {
	seen := make(map[*detailState]bool)
	var out []*detailState
	add := func(d *detailState) {
		if d == nil || d.dead || seen[d] {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	add(a.detail)
	for _, h := range a.detailWindows {
		if h == nil || h.removed {
			continue
		}
		for _, d := range h.states {
			add(d)
		}
	}
	for _, d := range a.detailStack {
		add(d)
	}
	add(a.replyWinFor)
	return out
}

// draftTick 在 view 帧尾调用，幂等：同一帧调几次都只是重算指纹。
func (a *app) draftTick() {
	now := time.Now()
	if st := a.compose; st != nil {
		fp := st.draftFingerprint()
		st.draftFP = fp
		if fp != st.draftSavedFP {
			a.markDraftDirty(now)
		}
	}
	for _, d := range a.liveDetailStates() {
		fp := d.draftFingerprint()
		d.draftFP = fp
		if fp != d.draftSavedFP {
			a.markDraftDirty(now)
		}
	}
	a.scheduleDraftTimer(now)
}

func (a *app) markDraftDirty(now time.Time) {
	if !a.draftPending {
		a.draftPending = true
		a.draftDirtySince = now
	}
}

func (a *app) scheduleDraftTimer(now time.Time) {
	if a.win == nil || !a.draftPending || a.draftTimer != nil {
		return // 无头测试不挂定时器，由测试显式调 flushDrafts
	}
	delay := draftDebounce
	if elapsed := now.Sub(a.draftDirtySince); elapsed >= draftMaxDelay-draftDebounce {
		delay = draftMaxDelay - elapsed
	}
	if delay < 0 {
		delay = 0
	}
	a.draftTimer = time.AfterFunc(delay, func() {
		a.publish(func() { a.flushDrafts() })
	})
}

// draftsForCurrent 在作用域变化（登录 / 退出 / 换账号）时换一份 store。旧 store
// 已经在强制落盘点保存过，这里只切引用。
func (a *app) draftsForCurrent() *draftStore {
	scope := draftScope(a)
	if a.drafts != nil && a.drafts.scope == scope {
		return a.drafts
	}
	a.drafts = newDraftStore(a.draftDir, scope)
	return a.drafts
}

// flushDrafts 把改动的运行态写成 DTO 并落盘。只在 UI 线程读写运行态；定时器
// 回调经 publish 回到这里。空条目直接删，不留一条空草稿。
func (a *app) flushDrafts() {
	a.flushDraftsInto(a.draftsForCurrent())
}

// flushDraftsInto 把改动写进指定的 store。退出 / 换账号时 auth 状态可能已经变
// 了，必须显式用旧作用域那份 store，否则内容会被写进新账号或 guest 的文件。
func (a *app) flushDraftsInto(store *draftStore) {
	if store == nil {
		return
	}
	if a.draftTimer != nil {
		a.draftTimer.Stop()
		a.draftTimer = nil
	}
	now := time.Now()
	changed := false
	// 写盘失败时把 draftSavedFP 退回原值，下一次 tick 会再试；成功才对齐。
	var saved []struct {
		set     func(uint64)
		old, to uint64
	}

	if st := a.compose; st != nil {
		fp := st.draftFingerprint()
		if fp != st.draftSavedFP {
			changed = true
			e := st.draftEntry()
			if e.ID == "" {
				e.ID = "compose-" + randomHex(8)
				e.CreatedAt = now
			} else if old, ok := store.find(e.ID); ok {
				e.CreatedAt = old.CreatedAt
			}
			e.UpdatedAt = now
			if draftEntryEmpty(e) {
				store.delete(e.ID)
				st.draftID = ""
			} else {
				store.put(e)
				st.draftID = e.ID
			}
			st.draftFP = fp
			saved = append(saved, struct {
				set     func(uint64)
				old, to uint64
			}{func(v uint64) { st.draftSavedFP = v }, st.draftSavedFP, fp})
		}
	}
	for _, d := range a.liveDetailStates() {
		fp := d.draftFingerprint()
		if fp != d.draftSavedFP {
			changed = true
			e := d.draftEntry()
			if old, ok := store.find(e.ID); ok {
				e.CreatedAt = old.CreatedAt
			} else {
				e.CreatedAt = now
			}
			e.UpdatedAt = now
			if draftEntryEmpty(e) {
				store.delete(e.ID)
			} else {
				store.put(e)
			}
			d.draftFP = fp
			saved = append(saved, struct {
				set     func(uint64)
				old, to uint64
			}{func(v uint64) { d.draftSavedFP = v }, d.draftSavedFP, fp})
		}
	}
	if changed {
		if err := store.save(); err != nil {
			log.Printf("草稿保存失败：%v", err)
			a.toast(toastMain, "草稿没能保存到本机")
			for _, s := range saved {
				s.set(s.old)
			}
			a.draftPending = true
			return
		}
	}
	for _, s := range saved {
		s.set(s.to)
	}
	a.draftPending = false
}

// stopDraftTimer 停掉在途的防抖定时器；退出、关窗等收尾路径调用。
func (a *app) stopDraftTimer() {
	if a.draftTimer != nil {
		a.draftTimer.Stop()
		a.draftTimer = nil
	}
}

// deleteDraft 删一条草稿，并同步清掉对应的 live 状态，否则下一次自动保存会把
// 刚删的内容又写回去。
func (a *app) deleteDraft(id string) {
	store := a.draftsForCurrent()
	store.delete(id)
	// 清 live：发帖草稿按 id 对齐，回复草稿按 short_id 对齐。
	if st := a.compose; st != nil && st.draftID == id {
		fp := st.draftFingerprint()
		st.draftID = ""
		st.draftFP, st.draftSavedFP = fp, fp
	}
	for _, d := range a.liveDetailStates() {
		if replyDraftID(d.shortID) != id {
			continue
		}
		d.draft = ""
		d.replyTo = replyTarget{}
		d.draftFP, d.draftSavedFP = d.draftFingerprint(), d.draftFingerprint()
	}
	_ = store.save()
	a.draftPending = false
}

// clearLiveDrafts 只清运行态并对齐指纹，不碰 store。登录态切换时先 flush 进旧
// 作用域，再把 live 状态清掉，免得下一帧按新作用域把旧内容又写一份。
func (a *app) clearLiveDrafts() {
	if st := a.compose; st != nil {
		mode := st.mode
		*st = *newComposeState()
		st.mode = mode
		fp := st.draftFingerprint()
		st.draftFP, st.draftSavedFP = fp, fp
	}
	for _, d := range a.liveDetailStates() {
		d.draft = ""
		d.replyTo = replyTarget{}
		d.draftFP, d.draftSavedFP = d.draftFingerprint(), d.draftFingerprint()
	}
	a.draftPending = false
}

// clearAllDrafts 清空当前账号的全部草稿，并清掉 live 状态。删完立刻落盘，别等
// 下一次 tick 把内容又写回来。
func (a *app) clearAllDrafts() (int, error) {
	store := a.draftsForCurrent()
	n := store.count()
	store.clear()
	a.clearLiveDrafts()
	if a.draftDir == "" {
		// 无头 / 未配置目录时只清内存，文件本就不落盘。
		a.draftPending = false
		return n, nil
	}
	if err := os.Remove(store.path); err != nil && !os.IsNotExist(err) {
		return n, err
	}
	a.draftPending = false
	return n, nil
}

// draftCounts 按类型统计草稿条数，供「清空全部」的确认文案用。
func draftCounts(rows []draftEntry) (compose, moment, reply int) {
	for _, e := range rows {
		switch e.Kind {
		case draftKindReply:
			reply++
		case draftKindCompose:
			if e.Mode == composeModeMoment {
				moment++
			} else {
				compose++
			}
		}
	}
	return
}

func draftSummary(e draftEntry) string {
	switch e.Kind {
	case draftKindReply:
		if e.ReplyTo.Floor > 0 {
			return fmt.Sprintf("回复 %d 楼", e.ReplyTo.Floor)
		}
		if e.ReplyTo.Author != "" {
			return "回复 " + e.ReplyTo.Author
		}
		return "新评论"
	default:
		if e.Mode == composeModeMoment {
			return "动态"
		}
		return "帖子"
	}
}
