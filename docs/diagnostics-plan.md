# 诊断日志与诊断包设计

- 状态：设计待实施
- 日期：2026-11-24
- 范围：进程日志落盘与滚动、日志脱敏、网络记录快照导出、诊断包 zip、设置页入口
- 约束：只写本机文件，不上传、不联网；不阻塞 UI 线程；诊断包默认不含令牌、密码、草稿与正文

## 1. 目标

现在出问题时能拿到的现场只有两样：用户口述，以及要求用户手动打开「网络调试」窗口截图。

要补的是：

1. **日志落盘**：启动、授权、代理切换、SSE 重连、更新、文件损坏这类事件留下可回溯的记录。
2. **一键导出诊断包**：把版本、平台、脱敏后的配置、网络记录、缓存统计和最近日志打成一个 zip，
   用户发给维护者即可复现大部分问题。
3. **导出前统一脱敏**：令牌、OAuth 授权码、刷新令牌、Cookie、代理密码都不能进包。

## 2. 非目标

- 不做崩溃转储（core dump）与符号化。
- 不做远程上报、遥测或任何自动上传。
- 不记录用户正在编辑的草稿正文、帖子正文（网络记录里的请求 / 响应体除外，见 4.3）。
- 不做日志级别 UI（不提供「调试模式」开关），日志始终按固定级别写。
- 不替代「网络调试」窗口；两者共享同一份记录器，窗口是实时看，诊断包是带走。

## 3. 现状与代码位置

| 现有实现 | 位置 | 现状 |
| --- | --- | --- |
| 日志调用 | `auth.go`、`draft.go:194`、`offline_store.go:229`、`main.go:2951` 等 | 全部走标准库 `log`，默认写 stderr；打包成 GUI 后 stderr 通常不可见 |
| 网络记录 | `netlog.go:20-36` | 进程内环形缓冲，`netLogMaxEntries=800`，每条请求 / 响应体最多 16 KiB，退出即丢 |
| 敏感头脱敏 | `netlog.go:426-430`（`netRedactHeader`） | 只覆盖请求头 / 响应头；**请求体没有脱敏** |
| 记录快照 | `netlog.go:158-164`（`Snapshot`） | 返回 `*netEntry` 的浅拷贝，条目还会被后台继续补全 |
| 配置目录 | `auth.go:77`（`configDir`），可用 `TWOLIBRA_CONFIG_DIR` 重定向 | 已有 |
| 保存对话框 | `image_viewer.go:1357`（`mygo.Dialog.Save`） | 图片「另存为」已在用，可复用 |
| 版本号 | `about_ui.go:187`（`mygo.App.Version()`） | 已有 |
| 设置页 | `settings_ui.go:34-42` | 六个大类 tab；「通用」页已有外观、窗口、签到、更新等块 |

## 4. 已确认决策

| 编号 | 决策 | 结论 |
| --- | --- | --- |
| 1 | 日志文件位置 | `<配置目录>/logs/app.log`，单文件上限 2 MiB，保留 `app.log`、`app.log.1`、`app.log.2` |
| 2 | 写日志方式 | `log.SetOutput(io.MultiWriter(os.Stderr, sink))`；sink 内部异步写，队列满直接丢弃并计数，绝不阻塞调用方 |
| 3 | 脱敏时机 | 日志在入队前脱敏；网络记录在导出时再脱敏一次（头和体都过） |
| 4 | 诊断包格式 | zip，固定成员名见 5.3；不打包整个配置目录 |
| 5 | 网络记录默认包含 | 默认包含，导出对话框里可取消；说明会含帖子标题与正文片段 |
| 6 | 偏好原文 | 默认只给「有多少项、多大」，不给通知关键词原文；勾选「包含偏好原文」才写完整值 |
| 7 | 导出线程 | 对话框在主线程，收集与压包在后台 goroutine，结果用 toast 回主线程 |
| 8 | 入口 | 设置 →「通用」页新增「诊断」块，不新开 tab |
| 9 | 日志目录入口 | 同一块提供「打开日志目录」；落地时先查 mygo 是否有文件管理器 API，没有就降级为复制日志路径（见 5.5） |

> 决策 9 落地前先确认 mygo 是否提供「在文件管理器中显示」的 API；没有就只给「导出诊断包」，
> 不为了一个按钮引入平台相关代码。

## 5. 设计

### 5.1 日志落盘

新增 `logfile.go`：

```go
type logFileSink struct {
    mu      sync.Mutex
    dir     string
    file    *os.File
    size    int64
    maxSize int64
    keep    int
    queue   chan []byte
    dropped atomic.Int64
}
```

行为：

- `installLogSink(dir)` 用 `sync.Once` 只装一次，在 `main()` 里做其它初始化**之前**调用，
  这样配置损坏、深链注册失败这类早期日志也能落盘。
- 保留 `log.SetFlags(log.LstdFlags | log.Lmicroseconds)`，stderr 照旧输出，开发时仍然看得见。
- `Write(p)` 只做三件事：按行拆分、单行超 4 KiB 截断并追加 `…(truncated)`、脱敏后投进队列。
  队列容量 4096；满了 `dropped++` 并返回成功（不能让 `log.Printf` 的调用方阻塞）。
- 单写协程负责打开、追加、轮转：
  - 写前检查 `size + len(line) > maxSize`；
  - 轮转顺序：删 `app.log.2` → `app.log.1` 改名 `app.log.2` → `app.log` 改名 `app.log.1` → 新建 `app.log`；
  - 新文件第一行写 `t2-desktop <version> <GOOS/GOARCH> go<version> started <RFC3339>`。
- 退出：`mygo.App.OnBeforeQuit` 里调用 `flushLogSink(200ms)`，最多等 200 ms，超时丢弃剩余队列。
- 无头测试直接构造 `logFileSink{dir: t.TempDir(), ...}`，不经过全局 `log.SetOutput`。

### 5.2 脱敏

新增 `redact.go`，两套函数：

```go
func redactText(s string) string      // 日志行、URL、任意文本
func redactJSONBody(s string) string  // 网络记录里的请求 / 响应体
```

`redactText` 至少覆盖：

- `Authorization: Bearer <...>`、裸 `Bearer <...>`；
- `access_token=`、`refresh_token=`、`id_token=`、`code_verifier=`、`client_secret=`、`password=`；
- OAuth 回调 URL 里的 `code=`（只对 `/oauth/` 路径生效，避免误伤业务字段）；
- `Cookie:` / `Set-Cookie:` 的整行值；
- 代理 URL 里的 `user:pass@`（复用 `proxy.go` 里状态展示的脱敏口径）。

`redactJSONBody`：

- 先尝试 `json.Unmarshal`，把下列键（不区分大小写、递归任意层级）替换为 `"***"`：
  `access_token`、`refresh_token`、`id_token`、`code_verifier`、`client_secret`、`password`；
- 不替换泛化的 `code` / `token`：2Libra 的响应信封用 `code` 表示错误码，分页字段里有
  `next_token` 之类，抹掉它们会让诊断包失去排障价值；
- 解析失败时退回 `redactText`；
- 保留 `ReqBodyTruncated` / `RespTrunc` 标记，诊断包读的人能知道内容不全。

> 这是本设计里最重要的安全点：`POST /api/oauth/token` 的请求体同时含 `refresh_token` 与
> `code_verifier`，当前 `netlog` 只脱敏了头，体是原样记录的。导出前必须过 `redactJSONBody`。

### 5.3 诊断包内容

zip 成员固定为：

| 成员 | 内容 | 隐私处理 |
| --- | --- | --- |
| `README.txt` | 这个包是什么、包含什么、不包含什么、怎么反馈 | 固定文案 |
| `meta.json` | 版本、GOOS/GOARCH、Go 版本、启动时间、运行时长、主题、配置目录、代理模式 | 代理地址只留 host:port，用户名密码脱敏 |
| `app.log` / `app.log.1` | 滚动日志 | 写入时已脱敏 |
| `netlog.ndjson` | 最多 800 条网络记录，一行一条 JSON | 头与体都过 `redact`；可整体取消 |
| `prefs.json` | 各偏好 store 的摘要：图片、评论费用、稍后再看、更新通道 / 镜像、自动签到、命令绑定数量 | 默认不含通知关键词原文、不含代理密码 |
| `cache.json` | 图片缓存占用与张数、离线缓存分片数与字节数、草稿条数与字节数 | 不含草稿与正文内容 |

不打包：`token.json`、任何 `drafts/*.json`、`cache/` 目录、图片文件。

`meta.json` 示例：

```json
{
  "version": "1.0.0-beta.2",
  "goos": "windows",
  "goarch": "amd64",
  "go": "go1.27.3",
  "started_at": "2026-11-24T09:00:00+08:00",
  "uptime_seconds": 3600,
  "theme": "dark",
  "proxy_mode": "manual",
  "proxy_endpoint": "socks5://u:***@127.0.0.1:1080",
  "config_dir": "%APPDATA%\\com.vhxubo.t2-desktop"
}
```

`config_dir` 只写结尾一级目录名，不写完整用户路径。

### 5.4 导出流程

新增 `diagnostics.go`：

```go
type diagnosticsOptions struct {
    IncludeNetLog bool
    IncludePrefs  bool
}

func (a *app) startDiagnosticsExport(opts diagnosticsOptions)
```

1. 主线程调用 `mygo.Dialog.Save`，默认文件名
   `t2-diagnostics-<version>-<yyyyMMdd-HHmmss>.zip`，默认目录取系统下载目录。
   无头测试用 `diagnosticsPathHook func() (string, error)` 注入路径。
2. 后台 goroutine 收集数据：
   - 日志：从 `logs/` 读取 `app.log*`，只取末尾 2 MiB；
   - 网络记录：`netRecorder` 在锁内把每条 `netEntry` **按值复制**（不要传 `*netEntry`，
     后台还会补字段），复制后再脱敏；
   - 偏好与缓存统计：调用现有 store 的只读方法；没有只读方法的，直接读内存字段。
3. 写临时文件 `<目标>.tmp`，再用 `os.Rename` 覆盖目标；失败时删临时文件。
4. `publish` 回主线程：成功 toast「已导出诊断包：<文件名>」，失败 toast 带原因。

并发约束：

- 不把 `*netEntry`、`*ui.Context`、`ui.Element` 带进后台；
- 读日志文件时文件可能正在写，允许读到半行，`README.txt` 里说明这一点；
- 导出期间按钮禁用，避免同一路径被两个 goroutine 同时写。

### 5.5 界面

设置 →「通用」页，在「更新」块之后新增「诊断」块（`settingsSection`，不新开 tab）：

- 说明文字：一段两行，讲清楚诊断包用于排查问题、不会自动上传。
- 复选框「包含网络请求记录」默认开，说明「会包含帖子标题与正文片段」。
- 复选框「包含偏好原文」默认关。
- 主按钮「导出诊断包」，导出中显示「正在打包…」并禁用。
- 次按钮「打开日志目录」：若 mygo 没有现成的文件管理器 API，就不放这个按钮，
  改为在说明里给出日志路径的复制按钮（复用 `copyToClipboard` + toast）。

关于「打开日志目录」：`openExternal` 走系统浏览器，不能用来打开目录。落地时先查
`mygo` 是否有 `ShellOpen` / `RevealInFileManager`；没有就按上面的降级方案做。

## 6. 代码改动清单

| 文件 | 改动 |
| --- | --- |
| `logfile.go`（新增） | `logFileSink`、轮转、异步队列、`installLogSink`、`flushLogSink` |
| `redact.go`（新增） | `redactText`、`redactJSONBody` 与单测 |
| `diagnostics.go`（新增） | 收集、压包、`startDiagnosticsExport`、路径 hook |
| `main.go` | `main()` 最前面装 sink；`OnBeforeQuit` 里 flush；设置页加上下文入口所需字段 |
| `settings_ui.go` | 「通用」页新增 `diagnosticsSettingsSection` |
| `netlog.go` | 新增 `snapshotForExport()`（值拷贝）；不改现有记录路径 |
| `netlog_ui.go` | 不要求改动；导出入口不复用窗口按钮 |
| `README.md` | 「已知限制」外补一句诊断包入口；不写实现细节 |

## 7. 验收条件

### 7.1 单元测试

- [ ] `TestLogSinkWritesAndRotates`：写满 2 MiB 后出现 `app.log.1`，`app.log.2` 最多一份；
      新文件首行含版本与平台。
- [ ] `TestLogSinkDropsWhenQueueFull`：队列塞满后 `Write` 立即返回，`dropped` 增加，调用不阻塞。
- [ ] `TestRedactText`：Bearer、`refresh_token=`、`code_verifier=`、Cookie 行、代理 `user:pass@` 全部被替换。
- [ ] `TestRedactJSONBodyOAuth`：对 `{"refresh_token":"r","code_verifier":"v"}` 输出不含原值，
      且保留其它字段。
- [ ] `TestDiagnosticsBundle`：用 `diagnosticsPathHook` 导出到 `t.TempDir()`，用 `archive/zip`
      打开，断言成员包含 `README.txt`、`meta.json`、`app.log`、`netlog.ndjson`；
      `meta.json` 的 `version` 等于 `mygo.App.Version()`。
- [ ] `TestDiagnosticsBundleRedactsNetLog`：构造一条含 `Authorization: Bearer secret-token-value`
      与 OAuth 请求体的记录，导出后整个 zip 搜不到 `secret-token-value`、`refresh_token` 原值。
- [ ] `TestDiagnosticsSkipsSecrets`：配置目录里放 `token.json`、`drafts/a.json`，
      导出后 zip 里没有这两个文件名。

### 7.2 UI 测试（`ui.NewTester`）

- [ ] 「通用」页出现「诊断」「导出诊断包」「包含网络请求记录」「包含偏好原文」。
- [ ] 点击「导出诊断包」，hook 返回临时路径后出现 toast「已导出诊断包：…」，按钮恢复可用。
- [ ] hook 返回错误时出现「导出失败：…」，按钮恢复可用。
- [ ] 导出中按钮文案为「正在打包…」且不可重复点击。

### 7.3 手动验收

- [ ] 正常启动 → 退出 → `logs/app.log` 有启动行、协议注册行、退出前最后一条日志。
- [ ] 断网状态下打开通知、切代理、检查更新各一次，诊断包 `netlog.ndjson` 能看到对应失败记录。
- [ ] 导出包解压后 `README.txt` 能在不看代码的情况下说明每个文件是什么。
- [ ] 用文本编辑器搜 `Bearer`、`refresh_token`、代理密码，均搜不到原值。

### 7.4 门禁

- [ ] `CGO_ENABLED=0 go test ./...` 通过。
- [ ] `go tool mygo vet .` 无告警。
- [ ] `gofmt -l .` 无输出。
- [ ] 仅改文档时 `git diff --check` 干净。

## 8. 风险与回退

| 风险 | 处理 |
| --- | --- |
| 日志写盘影响性能 | 异步队列 + 队列满丢弃；只在关键路径 `log.Printf`，不新增高频日志 |
| 诊断包泄露令牌 | 写入前脱敏 + 导出前二次脱敏；单测直接搜索敏感串 |
| Windows 上日志文件被占用无法轮转 | 用「关闭 → 改名 → 新建」；改名失败时保留当前文件继续写，只记一次错误，不循环重试 |
| 用户不想分享网络记录 | 导出对话框可取消；`README.txt` 明确说明包含内容 |
| 打包大文件卡住 | 日志只取末尾 2 MiB，图片与缓存目录不打包；整个包目标 < 3 MiB |