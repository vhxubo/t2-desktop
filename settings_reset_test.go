package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 一键重置要把设置窗口能改的每一份配置写回默认值，并且不碰登录态等数据。
func TestResetAllSettingsRestoresDefaults(t *testing.T) {
	a := testApp(t)
	dir := a.proxy.dir

	// 先把所有设置都改成非默认值。
	if err := a.proxy.Save(proxyConfig{
		Mode: proxyManual, URL: "http://127.0.0.1:7890", Username: "u", Password: "p", NoProxy: "localhost",
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.theme.Save(themeDark); err != nil {
		t.Fatal(err)
	}
	if err := a.closeMode.Save(closeApp); err != nil {
		t.Fatal(err)
	}
	if err := a.updateMirror.set("gh-proxy.com"); err != nil {
		t.Fatal(err)
	}
	if err := a.updateChannel.setPrerelease(true); err != nil {
		t.Fatal(err)
	}
	if err := a.commentCostPrefs.setVisible(false); err != nil {
		t.Fatal(err)
	}
	if err := a.commentCostPrefs.setOnSend(true); err != nil {
		t.Fatal(err)
	}
	if err := a.imagePrefs.setLazy(true); err != nil {
		t.Fatal(err)
	}
	if err := a.imagePrefs.setAnimate(false); err != nil {
		t.Fatal(err)
	}
	if err := a.watchLaterPrefs.setKeepAfterOpen(true); err != nil {
		t.Fatal(err)
	}
	if err := a.autoSignPrefs.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	p := a.notificationPrefs
	for _, change := range []func() error{
		func() error { return p.setEnabled(false) },
		func() error { return p.setSound(false) },
		func() error { return p.setBadge(false) },
		func() error { return p.setStream(false) },
		func() error { return p.setKeyword("金币") },
		func() error { return p.setCategory(notifyCategoryReply, false) },
		func() error { return p.setBurst(2) },
		func() error { return p.setQuiet(true) },
		func() error { return p.setQuietWindow("23:30", "07:30") },
	} {
		if err := change(); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.commands.prefs.setBinding("settings", "ctrl+shift+k"); err != nil {
		t.Fatal(err)
	}
	a.loadImageHosts()
	a.hosts.Active = "imgbb"
	if err := a.saveImageHosts(); err != nil {
		t.Fatal(err)
	}

	// 草稿也留半截内容，重置后应回到默认值。
	a.proxyURL = "http://127.0.0.1:9999"
	a.notifyKeywordDraft = "半截"
	a.notifyQuietFromDraft = "23:30"
	a.notifyQuietToDraft = "07:30"

	// 假 token 文件代表登录态：重置设置不能把账号登出去。
	tokenPath := filepath.Join(dir, "token-keeper.json")
	if err := os.WriteFile(tokenPath, []byte("keep-me"), 0o600); err != nil {
		t.Fatal(err)
	}

	a.settingsTab = settingsTabGeneral
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1800)
	st.Frame()
	if !st.HasText("一键重置所有设置") {
		t.Fatalf("通用页没有一键重置按钮：%q", st.Texts())
	}
	if err := st.Click("一键重置所有设置"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !st.HasText("确定恢复所有设置的默认值？") {
		t.Fatalf("点重置后没有弹出二次确认：%q", st.Texts())
	}
	if err := st.Click("确认重置"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !st.HasText("已重置所有设置为默认值") {
		t.Fatalf("重置后没有成功提示：%q", st.Texts())
	}

	// 内存里的设置全是默认值。
	if cfg, _ := a.proxy.Snapshot(); cfg.Mode != proxyFollowSystem || cfg.URL != "" || cfg.NoProxy != "" {
		t.Errorf("代理没有重置：%+v", cfg)
	}
	if a.theme.mode != themeFollowSystem {
		t.Errorf("外观没有重置：%d", a.theme.mode)
	}
	if a.closeMode.mode != closeAsk {
		t.Errorf("关闭行为没有重置：%d", a.closeMode.mode)
	}
	if a.updateMirror.current() != "" {
		t.Errorf("下载加速没有重置：%q", a.updateMirror.current())
	}
	if a.updateChannel.prerelease() {
		t.Error("接收测试版没有重置")
	}
	if !a.commentCostPrefs.visible() || a.commentCostPrefs.onSend() {
		t.Errorf("评论设置没有重置：%v %v", a.commentCostPrefs.visible(), a.commentCostPrefs.onSend())
	}
	if a.imagePrefs.lazyLoad() || !a.imagePrefs.autoAnimate() {
		t.Errorf("图片设置没有重置：%v %v", a.imagePrefs.lazyLoad(), a.imagePrefs.autoAnimate())
	}
	if a.watchLaterPrefs.keepAfterOpen() {
		t.Error("稍后再看设置没有重置")
	}
	if !a.autoSignPrefs.on() {
		t.Error("自动签到设置没有重置")
	}
	if !p.notificationEnabled() || !p.soundEnabled() || !p.badgeEnabled() || !p.streamEnabled() ||
		p.keywordText() != "" || p.keywordIsBlacklist() || !p.categoryEnabled(notifyCategoryReply) ||
		p.burstLimit() != 3 || p.quietOn() || p.quietFromText() != "22:00" || p.quietToText() != "08:00" {
		t.Errorf("通知设置没有重置：%+v", p)
	}
	if raw, ok := a.commands.prefs.binding("settings"); ok || raw != "" {
		t.Errorf("快捷键覆盖没有清掉：%q %v", raw, ok)
	}
	if a.hosts.Active != "kappa" {
		t.Errorf("图库没有重置：%q", a.hosts.Active)
	}
	if a.proxyURL != "" || a.proxyNoProxy != "" || a.notifyKeywordDraft != "" ||
		a.notifyQuietFromDraft != "22:00" || a.notifyQuietToDraft != "08:00" {
		t.Errorf("草稿没有同步：%q %q %q %q %q",
			a.proxyURL, a.proxyNoProxy, a.notifyKeywordDraft, a.notifyQuietFromDraft, a.notifyQuietToDraft)
	}
	if a.revealedImages != nil {
		t.Error("已点开的配图记录没有清掉")
	}

	// 磁盘上的配置也全是默认值（重新建一份 store 来读，不依赖内存）。
	var proxyCfg proxyConfig
	if err := readJSONFile(filepath.Join(dir, "proxy.json"), &proxyCfg); err != nil || proxyCfg.Mode != proxyFollowSystem {
		t.Errorf("proxy.json 没有重置：%+v %v", proxyCfg, err)
	}
	if got := newThemeManager(dir).mode; got != themeFollowSystem {
		t.Errorf("theme.json 没有重置：%d", got)
	}
	if got := newCloseModeStore(dir).mode; got != closeAsk {
		t.Errorf("window.json 没有重置：%d", got)
	}
	if got := newUpdateMirrorStore(dir).current(); got != "" {
		t.Errorf("update_mirror.json 没有重置：%q", got)
	}
	if got := newUpdateChannelStore(dir).prerelease(); got {
		t.Errorf("update_channel.json 没有重置：%v", got)
	}
	if s := newCommentCostPrefsStore(dir); !s.visible() || s.onSend() {
		t.Errorf("comment-cost.json 没有重置：%v %v", s.visible(), s.onSend())
	}
	if s := newImagePrefsStore(dir); s.lazyLoad() || !s.autoAnimate() {
		t.Errorf("images.json 没有重置：%v %v", s.lazyLoad(), s.autoAnimate())
	}
	if s := newWatchLaterPrefsStore(dir); s.keepAfterOpen() {
		t.Error("watch-later.json 没有重置")
	}
	if s := newAutoSignPrefsStore(dir); !s.on() {
		t.Error("auto_sign.json 没有重置")
	}
	if s := newNotificationPrefsStore(dir); s.keywordText() != "" || !s.notificationEnabled() || s.quietOn() {
		t.Errorf("notify.json 没有重置：%+v", s)
	}
	if raw, ok := newCommandPrefs(dir).binding("settings"); ok || raw != "" {
		t.Errorf("commands.json 没有重置：%q %v", raw, ok)
	}
	var hosts imageHostConfig
	if _, ok := newOfflineKV(dir).get(imageHostKVKey, &hosts); !ok || hosts.Active != "kappa" {
		t.Errorf("图库配置没有落回默认：%+v %v", hosts, ok)
	}
	if data, err := os.ReadFile(tokenPath); err != nil || string(data) != "keep-me" {
		t.Errorf("登录态文件被改动：%q %v", data, err)
	}
}

// 重置必须先弹二次确认；只点入口或点取消都不能改动任何设置。
func TestResetNeedsConfirmation(t *testing.T) {
	a := testApp(t)
	if err := a.theme.Save(themeDark); err != nil {
		t.Fatal(err)
	}
	a.settingsTab = settingsTabGeneral
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1800)
	st.Frame()
	if err := st.Click("一键重置所有设置"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !st.HasText("确定恢复所有设置的默认值？") || !st.HasText("确认重置") {
		t.Fatalf("没有弹出二次确认气泡：%q", st.Texts())
	}
	if a.theme.mode != themeDark {
		t.Fatal("只点入口就把设置重置了")
	}
	if err := st.Click("取消"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if st.HasText("确定恢复所有设置的默认值？") {
		t.Errorf("点取消后气泡还开着：%q", st.Texts())
	}
	if a.theme.mode != themeDark {
		t.Error("点取消也把设置重置了")
	}
	if a.resetConfirmOpen {
		t.Error("点取消没有关掉确认气泡")
	}
}

// 一键重置放在默认打开的「通用」页，其它页不重复出现。
func TestResetButtonOnlyOnGeneralTab(t *testing.T) {
	a := testApp(t)
	for tab, want := range map[int]bool{
		settingsTabGeneral:   true,
		settingsTabSidebar:   false,
		settingsTabNetwork:   false,
		settingsTabImages:    false,
		settingsTabReading:   false,
		settingsTabNotify:    false,
		settingsTabShortcuts: false,
	} {
		a.settingsTab = tab
		st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1800)
		st.Frame()
		if got := st.HasText("一键重置所有设置"); got != want {
			t.Errorf("分类 %d 的一键重置按钮 = %v，期望 %v", tab, got, want)
		}
	}
}
