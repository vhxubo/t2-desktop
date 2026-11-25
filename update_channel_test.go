package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 「稳定渠道」默认关，也就是默认接收全部版本；打开后重新读回还是打开，
// 重置回到默认（接收测试版）。
func TestUpdateChannelStorePersist(t *testing.T) {
	dir := t.TempDir()
	s := newUpdateChannelStore(dir)
	if s.stable() || !s.prerelease() {
		t.Fatal("默认该接收全部版本（含测试版）")
	}
	if err := s.setStable(true); err != nil {
		t.Fatal(err)
	}
	if again := newUpdateChannelStore(dir); !again.stable() {
		t.Error("重新读回的选择不是「稳定渠道」")
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if s.stable() || !s.prerelease() {
		t.Error("重置后没有回到默认通道")
	}
	if again := newUpdateChannelStore(dir); again.stable() {
		t.Error("重置没有落盘")
	}
}

// 老版本落盘的 prerelease 字段按反选读回，保留用户之前的通道选择。
func TestUpdateChannelLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		prerelease bool
		wantStable bool
	}{
		{true, false},
		{false, true},
	} {
		if err := writeJSONFile(filepath.Join(dir, "update_channel.json"),
			updateChannelConfig{Prerelease: &c.prerelease}); err != nil {
			t.Fatal(err)
		}
		if got := newUpdateChannelStore(dir).stable(); got != c.wantStable {
			t.Errorf("prerelease=%v 读回稳定渠道=%v，期望 %v", c.prerelease, got, c.wantStable)
		}
	}
}

// 开关接到 mygo 更新器上：打开后 Prereleases 为真，关掉后为假。测试结束时
// 恢复成关闭，别让同进程里的其他用例按测试版通道检查更新。
func TestApplyUpdateChannelSetsUpdater(t *testing.T) {
	old := mygo.Updater.Prereleases()
	t.Cleanup(func() { mygo.Updater.SetPrereleases(old) })

	applyUpdateChannel(true)
	if !mygo.Updater.Prereleases() {
		t.Fatal("打开后更新器仍在只收正式版")
	}
	applyUpdateChannel(false)
	if mygo.Updater.Prereleases() {
		t.Fatal("关掉后更新器仍在收测试版")
	}
}

// 设置窗口「更新」一块的开关：默认接收全部版本，点一下打开稳定渠道，
// 立即落盘并同步到更新器；再点一下回到默认。
func TestUpdateSettingsStableToggle(t *testing.T) {
	old := mygo.Updater.Prereleases()
	t.Cleanup(func() { mygo.Updater.SetPrereleases(old) })

	a := testApp(t)
	a.settingsTab = settingsTabGeneral
	st := ui.NewTester(a.settingsWindowView, settingsWindowWidth, 1400)
	st.Frame()
	if !st.HasText("稳定渠道") {
		t.Fatalf("设置窗口没有稳定渠道开关：%q", st.Texts())
	}
	if !a.updateChannel.prerelease() {
		t.Fatal("默认该接收全部版本")
	}
	if err := st.Click("稳定渠道"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if !a.updateChannel.stable() || mygo.Updater.Prereleases() {
		t.Fatalf("点开关没有打开稳定渠道：prefs=%v updater=%v",
			a.updateChannel.stable(), mygo.Updater.Prereleases())
	}
	if again := newUpdateChannelStore(a.updateChannel.dir); !again.stable() {
		t.Error("稳定渠道没有落盘")
	}
	if !st.HasText("检查更新只获取正式版 Release") {
		t.Errorf("打开后没有提示：%q", st.Texts())
	}
	if err := st.Click("稳定渠道"); err != nil {
		t.Fatal(err)
	}
	st.Frame()
	if a.updateChannel.stable() || !mygo.Updater.Prereleases() {
		t.Error("再点一下没有回到接收全部版本")
	}
	if !st.HasText("接收全部版本（含 Beta），并强制开启闲时自动下载") {
		t.Errorf("关掉后没有提示：%q", st.Texts())
	}
}

// 版本比较与 mygo internal/update 的 Compare 一致：数字按数值比、预发布版本
// 排在同号正式版之前、带 v 前缀与构建元数据不影响结果。
func TestCompareUpdateVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.10.0", "1.9.9", 1},
		{"1.0.0-beta.1", "1.0.0", -1},
		{"1.0.0", "1.0.0-beta.1", 1},
		{"1.0.0-beta.2", "1.0.0-beta.1", 1},
		{"1.2.0-beta.1", "1.1.0", 1},
		{"1.0.0+build.7", "1.0.0", 0},
	} {
		if got := compareUpdateVersions(c.a, c.b); got != c.want {
			t.Errorf("compareUpdateVersions(%q, %q) = %d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

// 正式版通道的 feed 仍是 latest release，拼法与 mygo build 一致。
func TestUpdateFeedURLLatest(t *testing.T) {
	want := "https://github.com/vhxubo/t2-desktop/releases/latest/download/update-" +
		runtime.GOOS + "-" + runtime.GOARCH + ".json"
	if got := updateFeedURL(); got != want {
		t.Errorf("latest feed = %q，期望 %q", got, want)
	}
	old := updateFeedBase
	updateFeedBase = "http://127.0.0.1:1"
	t.Cleanup(func() { updateFeedBase = old })
	if got := updateFeedURLAt(""); !strings.HasSuffix(got, "/releases/latest/download/update-"+runtime.GOOS+"-"+runtime.GOARCH+".json") {
		t.Errorf("空 tag 没有拼成 latest feed：%q", got)
	}
}

// 接收测试版（默认）且选了下载加速时，镜像源先用 GitHub API 挑出版本最高的
// release（跳过 draft），再取那个 tag 的 manifest。
func TestUpdateMirrorSourcePrereleaseManifest(t *testing.T) {
	var apiPath, feedPath atomic.Value
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiPath.Store(r.URL.Path + "?" + r.URL.RawQuery)
		_, _ = fmt.Fprint(w, `[
			{"tag_name":"v2.0.0","draft":true,"prerelease":false},
			{"tag_name":"v1.2.0-beta.1","draft":false,"prerelease":true},
			{"tag_name":"v1.1.0","draft":false,"prerelease":false},
			{"tag_name":"desktop-v9.0.0","draft":false,"prerelease":false}
		]`)
	}))
	defer api.Close()
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		feedPath.Store(r.URL.Path)
		_, _ = fmt.Fprint(w, updateManifestJSON)
	}))
	defer feed.Close()

	oldAPI, oldFeed := updateAPIBase, updateFeedBase
	updateAPIBase, updateFeedBase = api.URL, feed.URL
	t.Cleanup(func() { updateAPIBase, updateFeedBase = oldAPI, oldFeed })

	rc, err := mirrorSource{}.Manifest(context.Background(), true)
	if err != nil {
		t.Fatalf("拉取测试版 manifest 失败：%v", err)
	}
	body, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"version":"9.9.9"`) {
		t.Errorf("没有读回更新 manifest：%q", body)
	}
	if p, _ := apiPath.Load().(string); p != "/repos/vhxubo/t2-desktop/releases?per_page=100&page=1" {
		t.Errorf("release 列表路径 = %q", p)
	}
	want := "/vhxubo/t2-desktop/releases/download/v1.2.0-beta.1/update-" + runtime.GOOS + "-" + runtime.GOARCH + ".json"
	if p, _ := feedPath.Load().(string); p != want {
		t.Errorf("manifest 路径 = %q，期望 %q", p, want)
	}

	// 正式版通道不查 GitHub API，直接取 latest feed。
	apiPath.Store("")
	feedPath.Store("")
	rc, err = mirrorSource{}.Manifest(context.Background(), false)
	if err != nil {
		t.Fatalf("拉取正式版 manifest 失败：%v", err)
	}
	rc.Close()
	if p, _ := apiPath.Load().(string); p != "" {
		t.Errorf("正式版通道不该查 release 列表：%q", p)
	}
	want = "/vhxubo/t2-desktop/releases/latest/download/update-" + runtime.GOOS + "-" + runtime.GOARCH + ".json"
	if p, _ := feedPath.Load().(string); p != want {
		t.Errorf("正式版 manifest 路径 = %q，期望 %q", p, want)
	}
}

// 仓库里只有正式版时，接收测试版挑到的是最新的那个；一个都没有就报错。
func TestUpdateHighestTag(t *testing.T) {
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, body.Load().(string))
	}))
	defer srv.Close()
	oldAPI := updateAPIBase
	updateAPIBase = srv.URL
	t.Cleanup(func() { updateAPIBase = oldAPI })

	body.Store(`[{"tag_name":"v1.0.0","draft":false,"prerelease":false},
		{"tag_name":"v0.9.0","draft":false,"prerelease":false}]`)
	if tag, err := updateHighestTag(context.Background()); err != nil || tag != "v1.0.0" {
		t.Errorf("只有正式版时挑到 %q（%v），期望 v1.0.0", tag, err)
	}

	body.Store(`[{"tag_name":"v2.0.0","draft":true,"prerelease":false}]`)
	if tag, err := updateHighestTag(context.Background()); err == nil {
		t.Errorf("只有 draft 时不该挑到 %q", tag)
	}
}
