package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 默认值必须和改动前的行为一致：桌面通知 / 提示音 / 角标都开，所有分类都
// 提醒，每次最多 3 条，没有关键词过滤。老用户没有 notify.json 时不应有任何
// 行为变化。
func TestNotificationPrefsDefaultsAndPersist(t *testing.T) {
	dir := t.TempDir()
	s := newNotificationPrefsStore(dir)
	if !s.notificationEnabled() || !s.soundEnabled() || !s.badgeEnabled() {
		t.Fatalf("默认值 = 通知 %v / 提示音 %v / 角标 %v，期望全开",
			s.notificationEnabled(), s.soundEnabled(), s.badgeEnabled())
	}
	if s.burstLimit() != 3 || s.keywordText() != "" || s.keywordIsBlacklist() {
		t.Fatalf("默认值 = 每次 %d 条 / 关键词 %q / 黑名单 %v",
			s.burstLimit(), s.keywordText(), s.keywordIsBlacklist())
	}
	for _, item := range notifyCategoryItems {
		if !s.categoryEnabled(item.Key) {
			t.Errorf("分类 %q 默认应为开", item.Label)
		}
	}

	if err := s.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := s.setSound(false); err != nil {
		t.Fatal(err)
	}
	if err := s.setBadge(false); err != nil {
		t.Fatal(err)
	}
	if err := s.setCategory(notifyCategoryReply, false); err != nil {
		t.Fatal(err)
	}
	if err := s.setKeyword("金币"); err != nil {
		t.Fatal(err)
	}
	if err := s.setKeywordBlacklist(true); err != nil {
		t.Fatal(err)
	}
	if err := s.setBurst(2); err != nil {
		t.Fatal(err)
	}

	again := newNotificationPrefsStore(dir)
	if again.notificationEnabled() || again.soundEnabled() || again.badgeEnabled() {
		t.Error("重读后三个总开关应为关")
	}
	if again.categoryEnabled(notifyCategoryReply) || !again.categoryEnabled(notifyCategoryMention) {
		t.Error("只应静音「回复」分类")
	}
	if again.keywordText() != "金币" || !again.keywordIsBlacklist() {
		t.Errorf("关键词偏好没落盘：%q / 黑名单 %v", again.keywordText(), again.keywordIsBlacklist())
	}
	if again.burstLimit() != 5 {
		t.Errorf("每次条数 = %d，期望 5", again.burstLimit())
	}
}

// 分类与关键词只决定弹不弹窗：总开关关掉时全部不弹；关键词支持白名单
// （「包含时提醒」）和黑名单（「包含时不提醒」）两种模式。
func TestNotificationShouldNotifyFilters(t *testing.T) {
	s := newNotificationPrefsStore(t.TempDir())
	reply := decodeNotification([]byte(`{"id":"n1","type":"reply","payload":{"commentText":"看看这条"}}`))
	reward := decodeNotification([]byte(`{"id":"n2","type":"get_reward","payload":{"amount":5}}`))

	if !s.shouldNotify(reply) || !s.shouldNotify(reward) {
		t.Fatal("默认应全部提醒")
	}
	if err := s.setCategory(notifyCategoryReply, false); err != nil {
		t.Fatal(err)
	}
	if s.shouldNotify(reply) {
		t.Error("静音「回复」后不该再提醒回复消息")
	}
	if !s.shouldNotify(reward) {
		t.Error("静音「回复」不该影响打赏消息")
	}
	if err := s.setCategory(notifyCategoryReply, true); err != nil {
		t.Fatal(err)
	}

	if err := s.setKeyword("金币"); err != nil {
		t.Fatal(err)
	}
	if s.shouldNotify(reply) || !s.shouldNotify(reward) {
		t.Error("白名单模式：只提醒文本含「金币」的消息")
	}
	if err := s.setKeywordBlacklist(true); err != nil {
		t.Fatal(err)
	}
	if !s.shouldNotify(reply) || s.shouldNotify(reward) {
		t.Error("黑名单模式：文本含「金币」的消息不该提醒")
	}
	if err := s.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	if s.shouldNotify(reply) || s.shouldNotify(reward) {
		t.Error("总开关关掉后不该再弹任何通知")
	}
}

// 设置窗口的「通知」页：开关点击后落盘、文案跟着变；关键词要点保存才生效。
func TestNotificationSettingsTogglesPersist(t *testing.T) {
	a := testApp(t)
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, settingsWindowHeight)
	st.Frame()
	if st.HasText("桌面通知") {
		t.Errorf("默认打开的通用页不该显示通知开关：%q", st.Texts())
	}
	if err := st.Click("通知"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	for _, label := range []string{"桌面通知", "通知提示音", "显示未读角标", "弹窗提醒的消息类型", "提醒关键词", "关键词匹配", "每次事件最多弹"} {
		if !st.HasText(label) {
			t.Errorf("通知设置页缺少 %q：%q", label, st.Texts())
		}
	}

	if err := st.Click("桌面通知"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.notifPrefs().notificationEnabled() {
		t.Error("点「桌面通知」后没有关闭")
	}
	if err := st.Click("通知提示音"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.notifPrefs().soundEnabled() {
		t.Error("点「通知提示音」后没有关闭")
	}
	if err := st.Click("回复"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.notifPrefs().categoryEnabled(notifyCategoryReply) {
		t.Error("点「回复」分类后没有静音")
	}
	if err := st.Click("5 条"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.notifPrefs().burstLimit() != 5 {
		t.Errorf("点「5 条」后条数 = %d", a.notifPrefs().burstLimit())
	}

	// 关键词要点「保存关键词」才落盘。
	a.notifyKeywordDraft = "金币"
	if err := st.Click("保存关键词"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.notifPrefs().keywordText() != "金币" {
		t.Errorf("关键词 = %q，期望「金币」", a.notifPrefs().keywordText())
	}

	again := newNotificationPrefsStore(a.notifPrefs().dir)
	if again.notificationEnabled() || again.soundEnabled() || again.categoryEnabled(notifyCategoryReply) {
		t.Fatal("通知设置没有写进 notify.json")
	}
	if again.keywordText() != "金币" || again.burstLimit() != 5 {
		t.Errorf("关键词 / 条数没落盘：%q / %d", again.keywordText(), again.burstLimit())
	}
}

func hasExactText(tt *ui.Tester, want string) bool {
	for _, s := range tt.Texts() {
		if s == want {
			return true
		}
	}
	return false
}

// 「显示未读角标」关掉后铃铛右下角的红点数字消失，但无障碍名里的未读数
// 保留：用户仍能从按钮名字里看到有几条未读，只是不再用红点强调。
func TestNotificationBadgeToggle(t *testing.T) {
	a := testApp(t)
	a.unread = 3
	view := func(c *ui.Context) { a.notificationBell(c) }
	tt := ui.NewTester(view, 160, 48)
	tt.Frame()
	if !hasExactText(tt, "3") {
		t.Fatalf("默认应画出角标数字：%q", tt.Texts())
	}
	if err := a.notifPrefs().setBadge(false); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if hasExactText(tt, "3") {
		t.Errorf("关掉角标后不该再画红点数字：%q", tt.Texts())
	}
	if !tt.HasText("通知（3 条未读）") {
		t.Errorf("铃铛名字里仍应带未读数：%q", tt.Texts())
	}
}

// 提示音偏好要传到原生通知：关掉后 Silent 为真，平台通知协程只读这份快照。
func TestNotificationSoundSetsSilent(t *testing.T) {
	a := testApp(t)
	n := decodeNotification([]byte(`{"id":"n1","type":"reply","payload":{"commentText":"在吗"}}`))
	if a.notificationNative(n).Silent {
		t.Error("默认应播提示音，Silent 不该为真")
	}
	if err := a.notifPrefs().setSound(false); err != nil {
		t.Fatal(err)
	}
	if !a.notificationNative(n).Silent {
		t.Error("关掉提示音后 Silent 应为真")
	}
}

// 免打扰时段：默认关闭；开启后支持同日区间与跨午夜，含首尾边界；两端相同
// 是零长度、按关闭处理。坏值在读盘时退回默认，不会把用户锁在免打扰里。
func TestNotificationQuietHours(t *testing.T) {
	s := newNotificationPrefsStore(t.TempDir())
	at := func(h, m int) time.Time {
		return time.Date(2026, 11, 21, h, m, 0, 0, time.Local)
	}
	if s.inQuietHours(at(23, 0)) {
		t.Error("默认不该启用免打扰")
	}
	if err := s.setQuiet(true); err != nil {
		t.Fatal(err)
	}
	// 默认时段 22:00–08:00 跨午夜。
	for _, tc := range []struct {
		h, m int
		want bool
	}{
		{21, 59, false}, {22, 0, true}, {23, 30, true},
		{0, 0, true}, {7, 59, true}, {8, 0, false}, {12, 0, false},
	} {
		if got := s.inQuietHours(at(tc.h, tc.m)); got != tc.want {
			t.Errorf("跨午夜 %02d:%02d = %v，期望 %v", tc.h, tc.m, got, tc.want)
		}
	}
	// 同日区间 09:00–18:00。
	if err := s.setQuietWindow("09:00", "18:00"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		h, m int
		want bool
	}{
		{8, 59, false}, {9, 0, true}, {17, 59, true},
		{18, 0, false}, {23, 0, false},
	} {
		if got := s.inQuietHours(at(tc.h, tc.m)); got != tc.want {
			t.Errorf("同日 %02d:%02d = %v，期望 %v", tc.h, tc.m, got, tc.want)
		}
	}
	if err := s.setQuietWindow("12:00", "12:00"); err != nil {
		t.Fatal(err)
	}
	if s.inQuietHours(at(12, 30)) {
		t.Error("两端相同的零长度时段不该挡消息")
	}
	if err := s.setQuietWindow("25:00", "oops"); err != nil {
		t.Fatal(err)
	}
	if s.quietFromText() != "12:00" || s.quietToText() != "12:00" {
		t.Errorf("坏时段应退回旧值：%q–%q", s.quietFromText(), s.quietToText())
	}
}

// 免打扰只挡弹窗：shouldNotify 用注入的时钟确认夜间不弹、白天照常弹。
func TestNotificationShouldNotifyRespectsQuietHours(t *testing.T) {
	s := newNotificationPrefsStore(t.TempDir())
	n := decodeNotification([]byte(`{"id":"n1","type":"reply","payload":{"commentText":"在吗"}}`))
	if err := s.setQuiet(true); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 11, 21, 23, 0, 0, 0, time.Local) }
	if s.shouldNotify(n) {
		t.Error("免打扰时段内不该弹通知")
	}
	s.now = func() time.Time { return time.Date(2026, 11, 21, 12, 0, 0, 0, time.Local) }
	if !s.shouldNotify(n) {
		t.Error("免打扰时段外应正常弹通知")
	}
}

// 长连接开关默认开、落盘后重读保持；这是切换 SSE / 轮询的依据。
func TestNotificationStreamPrefPersistence(t *testing.T) {
	dir := t.TempDir()
	s := newNotificationPrefsStore(dir)
	if !s.streamEnabled() {
		t.Fatal("实时长连接默认应开启（保持原有行为）")
	}
	if err := s.setStream(false); err != nil {
		t.Fatal(err)
	}
	if again := newNotificationPrefsStore(dir); again.streamEnabled() {
		t.Error("关掉长连接后重读应仍是关的")
	}
}

// 设置页里关掉「实时长连接」要立刻落盘并起轮询兜底；兜底用一小时的长
// 计时器，测试只断言它已经排上，不真的等它触发（避免网络请求）。
func TestNotificationStreamToggleStartsFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	a := newApp(t.TempDir())
	if err := a.auth.storeToken(tokenData{AccessToken: "tok", TokenType: "Bearer"}); err != nil {
		t.Fatal(err)
	}
	a.autoLoad = true
	a.client.Base = srv.URL + "/api/v1"
	a.client.StreamHTTP = srv.Client()

	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, settingsWindowHeight)
	st.Frame()
	if err := st.Click("通知"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !a.notifPrefs().streamEnabled() {
		t.Fatal("长连接默认应开启")
	}
	if err := st.Click("实时长连接"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.notifPrefs().streamEnabled() {
		t.Error("点开关后长连接应关闭")
	}
	if a.notifPollTimer == nil {
		t.Error("关掉长连接后应起轮询兜底")
	}
	if a.watchCancel != nil {
		t.Error("轮询模式下不该还有 SSE 连接")
	}
	if again := newNotificationPrefsStore(a.notifPrefs().dir); again.streamEnabled() {
		t.Error("长连接开关没有写进 notify.json")
	}
	a.stopNotificationWatch() // 收掉计时器，避免遗留
}

// 轮询模式下通知窗口标题栏显示「轮询中」，不显示像故障的「未连接」。
func TestNotificationWindowShowsPollingStatus(t *testing.T) {
	a := notificationWindowApp(t)
	if err := a.notifPrefs().setStream(false); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.notificationsWindowView, notifWindowWidth, notifWindowHeight)
	tt.Frame()
	if !tt.HasText("· 轮询中") {
		t.Errorf("关掉长连接后应显示轮询状态：%q", tt.Texts())
	}
	if tt.HasText("· 未连接") {
		t.Errorf("轮询是用户选择，不该显示成未连接：%q", tt.Texts())
	}
}

// 免打扰时段的保存按钮：合法时间落盘，非法时间给提示且不改配置。
func TestNotificationQuietWindowSave(t *testing.T) {
	a := testApp(t)
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, settingsWindowHeight)
	st.Frame()
	if err := st.Click("通知"); err != nil {
		t.Fatal(err)
	}
	st.Frame()

	a.notifyQuietFromDraft, a.notifyQuietToDraft = "23:30", "07:30"
	if err := st.Click("启用免打扰"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if err := st.Click("保存时段"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	p := a.notifPrefs()
	if !p.quietOn() || p.quietFromText() != "23:30" || p.quietToText() != "07:30" {
		t.Errorf("时段没落盘：启用 %v / %q–%q", p.quietOn(), p.quietFromText(), p.quietToText())
	}

	a.notifyQuietFromDraft = "25:99"
	if err := st.Click("保存时段"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if p.quietFromText() != "23:30" {
		t.Errorf("非法时间不该覆盖已保存的时段：%q", p.quietFromText())
	}
}
