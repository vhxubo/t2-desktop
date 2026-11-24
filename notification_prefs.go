package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 通知偏好：进程级一份（见 appDeps），落盘在 notify.json。多账号共用一个
// 账号册、共用这一份开关，改完所有窗口下一帧就按新值画。
//
// 默认值都对应「和以前一样」：桌面通知开、提示音开、角标开、实时长连接开、
// 免打扰关、每次最多 3 条、所有分类都提醒、不按关键词过滤。因此前几个用
// 反向布尔（disabled / silent / hide_badge / no_stream）落盘，老用户没有这个
// 文件时不改变任何既有行为。
type notificationPrefsStore struct {
	dir       string
	enabled   bool
	sound     bool
	badge     bool
	stream    bool
	muted     map[string]bool
	keyword   string
	blacklist bool
	maxBurst  int
	// 免打扰时段用 HH:MM 存，支持跨午夜；两端相同视为关闭。只挡桌面弹窗。
	quietEnabled bool
	quietFrom    string
	quietTo      string
	// now 是免打扰判断用的时钟，生产为 time.Now；测试里换成固定时间。
	now func() time.Time
}

// notifyCategoryReply 等是「弹窗提醒哪些类型」的分类键。它们只决定桌面通知
// 弹不弹，不影响消息列表与未读角标：角标照常计数，用户仍可在通知窗口里看到。
const (
	notifyCategoryReply    = "reply"
	notifyCategoryMention  = "mention"
	notifyCategoryReaction = "reaction"
	notifyCategoryReward   = "reward"
	notifyCategoryFollow   = "follow"
	notifyCategorySystem   = "system"
	notifyCategoryOther    = "other"
)

// notifyCategoryItem 是设置窗口里的一枚分类开关。顺序即显示顺序。
type notifyCategoryItem struct {
	Key   string
	Label string
}

var notifyCategoryItems = []notifyCategoryItem{
	{notifyCategoryReply, "回复"},
	{notifyCategoryMention, "提及"},
	{notifyCategoryReaction, "表态与收藏"},
	{notifyCategoryReward, "打赏"},
	{notifyCategoryFollow, "关注"},
	{notifyCategorySystem, "系统"},
	{notifyCategoryOther, "其它"},
}

// notifyBurstChoices 是「一次事件最多弹几条」的档位，默认 3（见原来的
// pollNotifications）。设成 1 适合只想被打断一次的人，5 适合怕漏消息的人。
var notifyBurstChoices = []int{1, 3, 5}

// notificationCategory 把服务端消息类型归到设置里的分类。接口的类型字段没有
// 在文档里逐项承诺，认得的归一类，认不得的一律进「其它」，不会因为新类型出现
// 而让消息悄悄不再提醒。
func notificationCategory(typ string) string {
	switch typ {
	case "reply", "reply_mention", "comment_pin":
		return notifyCategoryReply
	case "mention":
		return notifyCategoryMention
	case "reaction", "like", "favorite":
		return notifyCategoryReaction
	case "reward", "get_reward":
		return notifyCategoryReward
	case "follow":
		return notifyCategoryFollow
	case "system", "pandora", "level_up", "content_check", "badge_unlocked":
		return notifyCategorySystem
	}
	return notifyCategoryOther
}

type notificationPrefsConfig struct {
	Disabled         bool     `json:"disabled"`
	Silent           bool     `json:"silent"`
	HideBadge        bool     `json:"hide_badge"`
	NoStream         bool     `json:"no_stream"`
	Muted            []string `json:"muted"`
	Keyword          string   `json:"keyword"`
	KeywordBlacklist bool     `json:"keyword_blacklist"`
	MaxPerBurst      int      `json:"max_per_burst"`
	QuietEnabled     bool     `json:"quiet_enabled"`
	QuietFrom        string   `json:"quiet_from"`
	QuietTo          string   `json:"quiet_to"`
}

func newNotificationPrefsStore(dir string) *notificationPrefsStore {
	if dir == "" {
		dir = configDir()
	}
	s := &notificationPrefsStore{
		dir:       dir,
		enabled:   true,
		sound:     true,
		badge:     true,
		stream:    true,
		muted:     map[string]bool{},
		maxBurst:  3,
		quietFrom: "22:00",
		quietTo:   "08:00",
		now:       time.Now,
	}
	var cfg notificationPrefsConfig
	if err := readJSONFile(filepath.Join(dir, "notify.json"), &cfg); err == nil {
		s.enabled = !cfg.Disabled
		s.sound = !cfg.Silent
		s.badge = !cfg.HideBadge
		s.stream = !cfg.NoStream
		for _, key := range cfg.Muted {
			s.muted[key] = true
		}
		s.keyword = strings.TrimSpace(cfg.Keyword)
		s.blacklist = cfg.KeywordBlacklist
		if cfg.MaxPerBurst > 0 {
			s.maxBurst = cfg.MaxPerBurst
		}
		s.quietEnabled = cfg.QuietEnabled
		s.quietFrom = normalizeQuietTime(cfg.QuietFrom, s.quietFrom)
		s.quietTo = normalizeQuietTime(cfg.QuietTo, s.quietTo)
	}
	return s
}

// save 先写盘，失败时内存里保留用户刚选的值（本次运行仍按新值走），只把错误
// 交给调用方提示。和 theme / commentCostPrefs 的处理一致。
func (s *notificationPrefsStore) save() error {
	cfg := notificationPrefsConfig{
		Disabled:         !s.enabled,
		Silent:           !s.sound,
		HideBadge:        !s.badge,
		NoStream:         !s.stream,
		Keyword:          s.keyword,
		KeywordBlacklist: s.blacklist,
		MaxPerBurst:      s.maxBurst,
		QuietEnabled:     s.quietEnabled,
		QuietFrom:        s.quietFrom,
		QuietTo:          s.quietTo,
	}
	for key, on := range s.muted {
		if on {
			cfg.Muted = append(cfg.Muted, key)
		}
	}
	if err := writeJSONFile(filepath.Join(s.dir, "notify.json"), cfg); err != nil {
		return fmt.Errorf("保存通知设置失败：%w", err)
	}
	return nil
}

// Reset 恢复默认通知偏好：各类提醒全开、不过滤、每次最多 3 条、免打扰关闭。
// 默认值与 newNotificationPrefsStore 一致；不动 now（测试里的固定时钟）。
func (s *notificationPrefsStore) Reset() error {
	s.enabled, s.sound, s.badge, s.stream = true, true, true, true
	s.muted = map[string]bool{}
	s.keyword, s.blacklist = "", false
	s.maxBurst = 3
	s.quietEnabled = false
	s.quietFrom, s.quietTo = "22:00", "08:00"
	return s.save()
}

// syncNotifyDrafts 把设置窗口的三个输入框草稿同步为已保存的偏好。
func (a *app) syncNotifyDrafts() {
	p := a.notifPrefs()
	a.notifyKeywordDraft = p.keywordText()
	a.notifyQuietFromDraft = p.quietFromText()
	a.notifyQuietToDraft = p.quietToText()
}

func (s *notificationPrefsStore) notificationEnabled() bool { return s.enabled }
func (s *notificationPrefsStore) soundEnabled() bool        { return s.sound }
func (s *notificationPrefsStore) badgeEnabled() bool        { return s.badge }
func (s *notificationPrefsStore) streamEnabled() bool       { return s.stream }
func (s *notificationPrefsStore) keywordText() string       { return s.keyword }
func (s *notificationPrefsStore) keywordIsBlacklist() bool  { return s.blacklist }
func (s *notificationPrefsStore) burstLimit() int           { return s.maxBurst }
func (s *notificationPrefsStore) quietOn() bool             { return s.quietEnabled }
func (s *notificationPrefsStore) quietFromText() string     { return s.quietFrom }
func (s *notificationPrefsStore) quietToText() string       { return s.quietTo }

// categoryEnabled 报告一类消息是否允许弹窗。muted 里没有的键一律算允许。
func (s *notificationPrefsStore) categoryEnabled(key string) bool {
	return !s.muted[key]
}

// burstChoice 把当前条数映射回设置窗口的档位序号；自定义值（例如旧版本或手工
// 改过配置）落在最近的档位上，界面不会显示一个没有选中的分段。
func (s *notificationPrefsStore) burstChoice() int {
	best, bestDist := 0, int(^uint(0)>>1)
	for i, n := range notifyBurstChoices {
		d := n - s.maxBurst
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}

func (s *notificationPrefsStore) setEnabled(on bool) error {
	if s.enabled == on {
		return nil
	}
	s.enabled = on
	return s.save()
}

func (s *notificationPrefsStore) setSound(on bool) error {
	if s.sound == on {
		return nil
	}
	s.sound = on
	return s.save()
}

func (s *notificationPrefsStore) setBadge(on bool) error {
	if s.badge == on {
		return nil
	}
	s.badge = on
	return s.save()
}

func (s *notificationPrefsStore) setStream(on bool) error {
	if s.stream == on {
		return nil
	}
	s.stream = on
	return s.save()
}

func (s *notificationPrefsStore) setQuiet(on bool) error {
	if s.quietEnabled == on {
		return nil
	}
	s.quietEnabled = on
	return s.save()
}

func (s *notificationPrefsStore) setQuietWindow(from, to string) error {
	from = normalizeQuietTime(from, s.quietFrom)
	to = normalizeQuietTime(to, s.quietTo)
	if s.quietFrom == from && s.quietTo == to {
		return nil
	}
	s.quietFrom, s.quietTo = from, to
	return s.save()
}

func (s *notificationPrefsStore) setCategory(key string, on bool) error {
	if s.categoryEnabled(key) == on {
		return nil
	}
	if on {
		delete(s.muted, key)
	} else {
		s.muted[key] = true
	}
	return s.save()
}

func (s *notificationPrefsStore) setKeyword(keyword string) error {
	keyword = strings.TrimSpace(keyword)
	if s.keyword == keyword {
		return nil
	}
	s.keyword = keyword
	return s.save()
}

func (s *notificationPrefsStore) setKeywordBlacklist(on bool) error {
	if s.blacklist == on {
		return nil
	}
	s.blacklist = on
	return s.save()
}

func (s *notificationPrefsStore) setBurst(choice int) error {
	if choice < 0 || choice >= len(notifyBurstChoices) {
		return nil
	}
	n := notifyBurstChoices[choice]
	if s.maxBurst == n {
		return nil
	}
	s.maxBurst = n
	return s.save()
}

// normalizeQuietTime 只接受 HH:MM（24 小时制），无效值退回默认。配置是用户
// 可以直接编辑的文件，界面也挡一道，但读盘时仍要兜住写坏的值。
func normalizeQuietTime(raw, fallback string) string {
	if _, _, ok := parseQuietTime(raw); ok {
		return raw
	}
	return fallback
}

// parseQuietTime 把 HH:MM 转成当天的分钟数。时、分都要两位数以内的合法值。
func parseQuietTime(raw string) (int, int, bool) {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	h, errH := strconv.Atoi(parts[0])
	m, errM := strconv.Atoi(parts[1])
	if errH != nil || errM != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, false
	}
	return h, m, true
}

// inQuietHours 报告 now 是否落在免打扰时段内。结束早于开始表示跨午夜
// （例如 22:00–08:00）：那段时间是「大于等于开始 或 小于结束」。两端相同
// 是零长度时段，按关闭处理，免得用户填错后整天收不到提醒。
func (s *notificationPrefsStore) inQuietHours(now time.Time) bool {
	if !s.quietEnabled {
		return false
	}
	fh, fm, okFrom := parseQuietTime(s.quietFrom)
	th, tm, okTo := parseQuietTime(s.quietTo)
	if !okFrom || !okTo {
		return false
	}
	from, to := fh*60+fm, th*60+tm
	if from == to {
		return false
	}
	cur := now.Hour()*60 + now.Minute()
	if from < to {
		return cur >= from && cur < to
	}
	return cur >= from || cur < to
}

// shouldNotify 决定一条新消息要不要弹桌面通知。关键词是在消息文本上做忽略
// 大小写的包含匹配；「包含时提醒」是白名单，「包含时不提醒」是黑名单。免打扰
// 时段内的消息一律不弹，但仍记账为已见，时段结束后不会补弹。这三项都只作用于
// 弹窗，消息列表与未读数完全不受影响。
func (s *notificationPrefsStore) shouldNotify(n *notification) bool {
	if n == nil || !s.enabled || !s.categoryEnabled(notificationCategory(n.Type)) {
		return false
	}
	if s.now != nil && s.inQuietHours(s.now()) {
		return false
	}
	keyword := strings.TrimSpace(s.keyword)
	if keyword == "" {
		return true
	}
	v := notificationViewOf(n)
	hay := strings.ToLower(strings.Join([]string{
		v.ActorName, v.Action, v.TargetTitle, v.Excerpt, v.Detail, v.TypeLabel,
	}, " "))
	hit := strings.Contains(hay, strings.ToLower(keyword))
	return hit != s.blacklist
}

// defaultNotificationPrefs 只给 &app{} 这类没有共享依赖的测试对象兜底，不在
// 包初始化时读用户配置。
var defaultNotificationPrefs = &notificationPrefsStore{
	enabled: true, sound: true, badge: true, stream: true,
	muted: map[string]bool{}, maxBurst: 3,
	quietFrom: "22:00", quietTo: "08:00", now: time.Now,
}

func (a *app) notifPrefs() *notificationPrefsStore {
	if a.notificationPrefs != nil {
		return a.notificationPrefs
	}
	return defaultNotificationPrefs
}

// saveNotifyPref 统一处理「写偏好 → 失败提示 → 全窗口重画」。设置窗口与通知
// 窗口的快捷开关都走这里，既不会漏落盘，也不会只重画当前窗口。
func (a *app) saveNotifyPref(target toastTarget, apply func() error) {
	if err := apply(); err != nil {
		a.toast(target, err.Error())
		return
	}
	a.notificationPrefChanged()
}

// notificationPrefChanged 让所有受影响的地方按新偏好重画：设置页自身的开关、
// 主窗口标题栏的铃铛角标、通知窗口操作行里的快捷开关。
func (a *app) notificationPrefChanged() {
	a.redrawSettingsWindow()
	if a.win != nil {
		a.win.Invalidate()
	}
	if a.notifWin != nil {
		a.notifWin.Invalidate()
	}
}
