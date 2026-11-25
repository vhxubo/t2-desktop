package main

import (
	"archive/zip"
	"encoding/json"
	"errors"

	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
)

// 诊断包：把版本、脱敏后的配置摘要、网络记录、缓存统计与最近日志打成一个 zip。
// 全程只写本机文件，不上传、不联网。收集与压包在后台 goroutine 里做，结果用
// 窗口内 toast 回到主线程；导出前网络记录的头与体都再过一遍脱敏。
//
// 不打包本机会话、令牌、草稿与缓存正文：诊断包里的内容都能发给维护者。

// diagnosticsStartedAt 是进程启动时间，用于 meta.json 的 started_at 与运行时长。
var diagnosticsStartedAt = time.Now()

const (
	diagnosticsLogTailBytes = 2 << 20
	diagnosticsDir          = "logs"
)

// diagnosticsOptions 是导出对话框里选中的两项。
type diagnosticsOptions struct {
	IncludeNetLog bool
	IncludePrefs  bool
}

// diagnosticsSnapshot 是主线程拍下的一小份内存状态：偏好字段只在 UI 线程写，
// 先复制出来再交给后台，后台不直接读这些字段。
type diagnosticsSnapshot struct {
	version       string
	theme         string
	proxyMode     string
	proxyEndpoint string
	configDirName string
	startedAt     time.Time
	prefs         map[string]any
}

// startDiagnosticsExport 是「导出诊断包」按钮的入口，只在主线程调用。
func (a *app) startDiagnosticsExport(opts diagnosticsOptions) {
	if a.diagnosticsExporting {
		return
	}
	path, err := a.pickDiagnosticsPath()
	if err != nil {
		a.toast(toastSettings, "导出失败："+err.Error())
		return
	}
	if strings.TrimSpace(path) == "" {
		return // 用户在保存对话框里取消
	}
	if !strings.HasSuffix(strings.ToLower(path), ".zip") {
		path += ".zip"
	}
	snap := a.diagnosticsSnapshot(opts)
	a.diagnosticsExporting = true
	a.publish(func() {})

	go func() {
		err := a.writeDiagnosticsBundle(path, opts, snap)
		a.publish(func() {
			a.diagnosticsExporting = false
			if err != nil {
				a.toast(toastSettings, "导出失败："+err.Error())
			} else {
				a.toast(toastSettings, "已导出诊断包："+filepath.Base(path))
			}
			if a.onDiagnosticsExported != nil {
				a.onDiagnosticsExported(path, err)
			}
		})
	}()
}

// pickDiagnosticsPath 弹保存对话框。无头测试用 diagnosticsPathHook 注入路径：
// 没有窗口时不能碰原生对话框。
func (a *app) pickDiagnosticsPath() (string, error) {
	if a.diagnosticsPathHook != nil {
		return a.diagnosticsPathHook()
	}
	if a.win == nil {
		return "", errors.New("当前环境不支持选择保存位置")
	}
	name := diagnosticsFileName()
	def := name
	if dir, err := mygo.App.Path(mygo.PathDownloads); err == nil && dir != "" {
		def = filepath.Join(dir, name)
	}
	return mygo.Dialog.Save(mygo.SaveDialogOptions{
		Title:       "导出诊断包",
		DefaultPath: def,
		ButtonLabel: "保存",
		Filters:     []mygo.FileFilter{{Name: "诊断包", Extensions: []string{".zip"}}},
	})
}

// diagnosticsFileName 拼默认文件名。版本里的 beta 点号等保留，Windows 不允许
// 的字符换成下划线。
func diagnosticsFileName() string {
	version := mygo.App.Version()
	if version == "" {
		version = "unknown"
	}
	version = strings.Map(func(r rune) rune {
		switch r {
		case '\\', '/', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, version)
	return "t2-diagnostics-" + version + "-" + time.Now().Format("20060102-150405") + ".zip"
}

// diagnosticsSnapshot 在主线程收集不需要读文件的摘要。图片缓存、离线缓存与
// 草稿的磁盘统计留给后台，避免在 UI 线程遍历目录。
func (a *app) diagnosticsSnapshot(opts diagnosticsOptions) diagnosticsSnapshot {
	snap := diagnosticsSnapshot{
		version:   mygo.App.Version(),
		startedAt: diagnosticsStartedAt,
		prefs:     a.diagnosticsPrefs(opts),
	}
	if a.theme != nil {
		snap.theme = a.theme.modeName()
	}
	if a.proxy != nil {
		cfg, _ := a.proxy.Snapshot()
		snap.proxyMode = proxyModeName(cfg.Mode)
		snap.proxyEndpoint = diagnosticsProxyEndpoint(cfg.URL)
	}
	snap.configDirName = filepath.Base(a.draftDir)
	return snap
}

func proxyModeName(mode int) string {
	switch mode {
	case proxyManual:
		return "manual"
	case proxyDirect:
		return "direct"
	default:
		return "system"
	}
}

// diagnosticsProxyEndpoint 只留协议、用户名与 host:port，密码一律换成 ***。
func diagnosticsProxyEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	if u.User != nil {
		u.User = url.UserPassword(u.User.Username(), "***")
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// diagnosticsPrefs 是偏好摘要。默认不含通知关键词原文；IncludePrefs 打开才写
// 完整值。代理密码不在其中，meta.json 里的代理地址已经脱敏。
func (a *app) diagnosticsPrefs(opts diagnosticsOptions) map[string]any {
	prefs := map[string]any{}
	if a.imagePrefs != nil {
		prefs["images"] = map[string]any{
			"lazy_load":    a.imagePrefs.lazyLoad(),
			"auto_animate": a.imagePrefs.autoAnimate(),
		}
	}
	if a.commentCostPrefs != nil {
		prefs["comment_cost"] = map[string]any{
			"bar_visible":  a.commentCostPrefs.visible(),
			"show_on_send": a.commentCostPrefs.onSend(),
		}
	}
	if a.watchLaterPrefs != nil {
		prefs["watch_later"] = map[string]any{
			"keep_after_open": a.watchLaterPrefs.keepAfterOpen(),
		}
	}
	update := map[string]any{}
	if a.updateChannel != nil {
		update["channel"] = "prerelease"
		if a.updateChannel.stable() {
			update["channel"] = "stable"
		}
	}
	if a.updateMirror != nil {
		update["mirror"] = a.updateMirror.current()
	}
	update["idle_auto_download"] = a.idleAutoDownload()
	update["check_interval_hours"] = a.updateIntervalPrefs.interval() / time.Hour
	prefs["update"] = update
	if a.autoSignPrefs != nil {
		prefs["auto_sign"] = map[string]any{"enabled": a.autoSignPrefs.on()}
	}
	if a.commands != nil {
		commands := map[string]any{"available": len(a.commands.available())}
		if a.commands.prefs != nil {
			commands["custom_bindings"] = len(a.commands.prefs.snapshot().Bindings)
		}
		prefs["commands"] = commands
	}
	if p := a.notifPrefs(); p != nil {
		keyword := p.keywordText()
		notice := map[string]any{
			"enabled":            p.notificationEnabled(),
			"sound":              p.soundEnabled(),
			"badge":              p.badgeEnabled(),
			"stream":             p.streamEnabled(),
			"max_per_burst":      p.burstLimit(),
			"quiet_enabled":      p.quietOn(),
			"categories_muted":   len(p.muted),
			"keyword_configured": keyword != "",
			"keyword_length":     utf8.RuneCountInString(keyword),
		}
		if opts.IncludePrefs {
			notice["keyword"] = keyword
		}
		prefs["notification"] = notice
	}
	return prefs
}

// writeDiagnosticsBundle 在后台收集文件并写 zip。先写 <目标>.tmp 再改名，失败
// 时删掉临时文件；Windows 上目标已存在时改名会失败，先删目标再试一次。
func (a *app) writeDiagnosticsBundle(path string, opts diagnosticsOptions, snap diagnosticsSnapshot) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	err = a.buildDiagnosticsZip(f, opts, snap)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		// Windows 的改名不能覆盖已存在的文件；对话框确认覆盖后目标通常还在。
		if removeErr := os.Remove(path); removeErr == nil {
			err = os.Rename(tmp, path)
		}
		if err != nil {
			_ = os.Remove(tmp)
			return err
		}
	}
	return nil
}

func (a *app) buildDiagnosticsZip(w io.Writer, opts diagnosticsOptions, snap diagnosticsSnapshot) error {
	zw := zip.NewWriter(w)
	add := func(name string, data []byte) error {
		f, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}

	if err := add("README.txt", []byte(diagnosticsReadme(opts))); err != nil {
		return err
	}
	if err := add("meta.json", mustJSON(diagnosticsMetaOf(snap))); err != nil {
		return err
	}
	if err := add("prefs.json", mustJSON(snap.prefs)); err != nil {
		return err
	}
	if err := add("cache.json", mustJSON(a.diagnosticsCacheReport())); err != nil {
		return err
	}

	logDir := filepath.Join(a.draftDir, diagnosticsDir)
	current := readFileTail(filepath.Join(logDir, logFileName), diagnosticsLogTailBytes)
	if err := add(logFileName, current); err != nil {
		return err
	}
	if len(current) < diagnosticsLogTailBytes {
		if old, ok := readFileTailOK(filepath.Join(logDir, logFileName+".1"), int64(diagnosticsLogTailBytes-len(current))); ok {
			if err := add(logFileName+".1", old); err != nil {
				return err
			}
		}
	}

	if opts.IncludeNetLog {
		data, err := a.diagnosticsNetLog()
		if err != nil {
			return err
		}
		if err := add("netlog.ndjson", data); err != nil {
			return err
		}
	}
	return zw.Close()
}

type diagnosticsMeta struct {
	Version       string `json:"version"`
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	Go            string `json:"go"`
	StartedAt     string `json:"started_at"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Theme         string `json:"theme"`
	ProxyMode     string `json:"proxy_mode"`
	ProxyEndpoint string `json:"proxy_endpoint,omitempty"`
	ConfigDir     string `json:"config_dir"`
}

func diagnosticsMetaOf(snap diagnosticsSnapshot) diagnosticsMeta {
	return diagnosticsMeta{
		Version:       snap.version,
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		Go:            runtime.Version(),
		StartedAt:     snap.startedAt.Format(time.RFC3339),
		UptimeSeconds: int64(time.Since(snap.startedAt).Seconds()),
		Theme:         snap.theme,
		ProxyMode:     snap.proxyMode,
		ProxyEndpoint: snap.proxyEndpoint,
		ConfigDir:     snap.configDirName,
	}
}

// diagnosticsCacheReport 统计图片缓存、离线缓存分片与草稿文件。只输出数量与
// 字节数，不读草稿正文。
func (a *app) diagnosticsCacheReport() map[string]any {
	report := map[string]any{}
	if a.images != nil {
		s := a.images.Stats()
		report["images"] = map[string]any{
			"stable_files": s.StableFiles,
			"stable_bytes": s.StableBytes,
			"temp_files":   s.TempFiles,
			"temp_bytes":   s.TempBytes,
			"total_files":  s.TotalFiles(),
			"total_bytes":  s.TotalBytes(),
		}
	}
	cacheDir := filepath.Join(a.draftDir, "cache")
	shards, shardBytes := countFiles(cacheDir, func(name string) bool {
		return strings.HasSuffix(name, ".json")
	})
	report["offline"] = map[string]any{"shards": shards, "bytes": shardBytes}

	draftDir := filepath.Join(a.draftDir, "drafts")
	files, bytes := countFiles(draftDir, func(name string) bool {
		return strings.HasSuffix(name, ".json")
	})
	report["drafts"] = map[string]any{
		"files":   files,
		"entries": draftEntryCount(draftDir),
		"bytes":   bytes,
	}
	return report
}

// countFiles 统计目录下满足条件的普通文件数与总字节数；目录不存在算 0。
func countFiles(dir string, match func(string) bool) (files int, bytes int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, e := range entries {
		if e.IsDir() || (match != nil && !match(e.Name())) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files++
		bytes += info.Size()
	}
	return files, bytes
}

// draftEntryCount 只读每份草稿文件的条数，不把正文带进诊断包。
func draftEntryCount(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	total := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		var f draftFile
		if err := readJSONFile(filepath.Join(dir, e.Name()), &f); err != nil {
			continue
		}
		total += len(f.Entries)
	}
	return total
}

// diagnosticsNetLog 把网络记录按值快照后再脱敏，一行一条 JSON。
func (a *app) diagnosticsNetLog() ([]byte, error) {
	if a.proxy == nil || a.proxy.net == nil {
		return nil, nil
	}
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	for _, e := range a.proxy.net.snapshotForExport() {
		export := netEntryForExport(e)
		if err := enc.Encode(export); err != nil {
			return nil, err
		}
	}
	return []byte(buf.String()), nil
}

type netHeaderExport struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type netEntryExport struct {
	ID               int               `json:"id"`
	Start            time.Time         `json:"start"`
	Method           string            `json:"method"`
	URL              string            `json:"url"`
	ReqHeaders       []netHeaderExport `json:"req_headers,omitempty"`
	ReqBody          string            `json:"req_body,omitempty"`
	ReqBodyTruncated bool              `json:"req_body_truncated,omitempty"`
	ReqBodyNote      string            `json:"req_body_note,omitempty"`
	Status           int               `json:"status,omitempty"`
	RespHeaders      []netHeaderExport `json:"resp_headers,omitempty"`
	RespCT           string            `json:"resp_content_type,omitempty"`
	RespBody         string            `json:"resp_body,omitempty"`
	RespTrunc        bool              `json:"resp_truncated,omitempty"`
	RespNote         string            `json:"resp_note,omitempty"`
	ElapsedMS        int64             `json:"elapsed_ms"`
	Err              string            `json:"error,omitempty"`
	Done             bool              `json:"done"`
	Stream           bool              `json:"stream,omitempty"`
}

func netEntryForExport(e netEntry) netEntryExport {
	out := netEntryExport{
		ID:               e.ID,
		Start:            e.Start,
		Method:           e.Method,
		URL:              redactText(e.URL),
		ReqBody:          redactJSONBody(e.ReqBody),
		ReqBodyTruncated: e.ReqBodyTruncated,
		ReqBodyNote:      e.ReqBodyNote,
		Status:           e.Status,
		RespCT:           e.RespCT,
		RespBody:         redactJSONBody(e.RespBody),
		RespTrunc:        e.RespTrunc,
		RespNote:         e.RespNote,
		ElapsedMS:        e.Elapsed.Milliseconds(),
		Err:              redactText(e.Err),
		Done:             e.Done,
		Stream:           e.Stream,
	}
	for _, h := range e.ReqHeaders {
		out.ReqHeaders = append(out.ReqHeaders, netHeaderExport{
			Key: h.Key, Value: redactText(h.Value),
		})
	}
	for _, h := range e.RespHeaders {
		out.RespHeaders = append(out.RespHeaders, netHeaderExport{
			Key: h.Key, Value: redactText(h.Value),
		})
	}
	return out
}

// readFileTail 读文件末尾最多 max 字节；文件不存在或读不到时返回空。
func readFileTail(path string, max int64) []byte {
	data, _ := readFileTailOK(path, max)
	return data
}

func readFileTailOK(path string, max int64) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, false
	}
	start := info.Size() - max
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return nil, false
	}
	return data, true
}

func mustJSON(v any) []byte {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return []byte("{}")
	}
	return b
}

func diagnosticsReadme(opts diagnosticsOptions) string {
	var b strings.Builder
	b.WriteString("T2 Desktop 诊断包\n\n")
	b.WriteString("这个包用于排查问题，只包含本机数据，不会自动上传。\n\n")
	b.WriteString("包含的文件：\n")
	b.WriteString("  README.txt       本说明\n")
	b.WriteString("  meta.json        版本、平台、启动时间、运行时长、外观、代理模式与脱敏后的代理地址、配置目录名\n")
	b.WriteString("  prefs.json       偏好摘要（默认不含通知关键词原文）\n")
	b.WriteString("  cache.json       图片缓存、离线缓存与草稿的数量和占用，不含正文\n")
	b.WriteString("  app.log          当前日志；app.log.1 是上一份滚动日志\n")
	if opts.IncludeNetLog {
		b.WriteString("  netlog.ndjson    最近的网络请求记录，一行一条；请求与响应体可能包含帖子标题和正文片段\n")
	} else {
		b.WriteString("  netlog.ndjson    未包含（导出时取消勾选）\n")
	}
	b.WriteString("\n不包含：令牌、草稿正文、图片与缓存文件本身。\n")
	b.WriteString("敏感值（Authorization、Cookie、令牌、OAuth code、代理密码）已替换为 ***。\n")
	b.WriteString("日志是在运行中读取的，末尾可能是半行。\n")
	b.WriteString("\n反馈时请说明：做了什么操作、期望结果、实际结果，并把本包一起附上。\n")
	if opts.IncludePrefs {
		b.WriteString("本次导出勾选了「包含偏好原文」，prefs.json 里有通知关键词的完整内容。\n")
	}
	return b.String()
}
