# T2 Desktop

2Libra 的桌面客户端。基于 [mygo](https://github.com/egoist/mygo) 的纯原生
UI（无 WebView），用 Go 编写，支持 Windows / macOS / Linux。

所有内容数据来自 [2Libra 开放 API v1](https://r2.2libra.com/openapi/v1/index.md)。

---

## 功能特性

- **浏览与无限滚动**：热门、最新、最新评论三个信息流，分页拉取到底自动停止。
- **帖子详情**：Markdown 正文渲染、评论、表情、配图，正文内联跳转带返回栈。
- **登录 2Libra**：浏览器 OAuth（PKCE）+ 自定义协议深链回调；未关联协议时
  可手动粘贴回调链接。
- **消息中心**：未读角标、SSE 长连接、系统通知与已读批量管理。
- **金币**：余额、今日明细、流水、统计与排行各自独立加载，未开通的能力只降级
  对应模块；标题栏金币数实时联动。
- **潘多拉**：今日盲盒与抽奖进度。
- **我的**：概览 / 内容 / 回复 / 徽章墙 / 六维属性（权限分）五个标签页。
- **发帖与评论**：独立写帖窗口、独立写评论窗口，草稿不丢字。
- **图床（图库）**：内置 Imgur / Tikolu / MJJ.Today / ImgBB / Skyimg /
  111666.best / StarDots，官方图床仅对 Pro 开放。
- **搜索与节点**：全文搜索、节点级联选择。
- **代理**：系统代理与自定义代理（含账号密码、不走代理的域名）。
- **本地缓存**：阅读历史、已读状态、金币与帖子正文离线缓存，只读写本机文件。
- **外观**：浅色 / 深色主题、系统托盘、窗口模式。
- **自动更新**：从 GitHub Release 检查并安装更新，更新包签名校验。

---

## 技术栈

| 项 | 说明 |
| --- | --- |
| 语言 | Go 1.27+ |
| UI | `github.com/egoist/mygo` 原生 UI 包 `ui`（无 WebView） |
| 构建工具 | `mygo`（`go tool mygo build`） |
| 网络 | `golang.org/x/net` |
| 包标识 | `com.vhxubo.t2-desktop` |
| 发布 | GitHub Release（`vhxubo/t2-desktop`），更新包用 Ed25519 私钥签名 |

> mygo 不需要 cgo，构建始终 `CGO_ENABLED=0`，结果可复现，且与 go.mod 无关。

---

## 构建与运行

需要先安装 Go 1.27+ 与 mygo 工具（`go install github.com/egoist/mygo/cmd/mygo@latest`，
或依赖 `go.mod` 里的 `tool` 指令，用 `go tool mygo` 调用）。

```bash
# 配置好 MYGO_UPDATER_PRIVATE_KEY 后（本地构建可省略，仅影响更新包签名）
go tool mygo build            # 构建当前平台
go tool mygo build -platform "windows/amd64,windows/arm64"  # 交叉编译
go tool mygo build -upload    # 构建并上传到对应 release
```

- 产物输出到 `build/`。
- Windows 安装包由 `packaging/installer.nsh` 钩子扩展（安装包需要 UAC 提权，
  应用本身以普通用户运行）。
- macOS 需在 `mygo.json` 配置 `macos.signingIdentity` / `macos.notarize` 才能
  通过 Gatekeeper。

---

## 开发

### 常用命令

```bash
CGO_ENABLED=0 go test ./...     # 跑全部测试
go tool mygo vet .              # mygo 静态检查
gofmt -l .                      # 不应输出任何文件
```

### UI 自动化测试

改 UI 时用 `ui.NewTester` 复现：无头渲染与真实窗口一致，可用 `Find` / `Texts` /
`Image` 断言布局与像素。toast 只在构建期发出，断言前要 `tt.Frame()` 让窗口领走
队列里的提示，再用 `tt.HasText(...)` / `tt.Advance(...)` 验证文案与自动消失。

### 约定

- **提交信息只用英文**，遵循 Conventional Commits（`feat` / `fix` / `refactor` /
  `test` / `docs` / `chore`）；中文只允许出现在代码、注释、日志与界面文案里。
- 应用状态里**不要保存** `ui.Element` 或 `*ui.Context`；后台结果通过
  `Window.Update` / `app.publish` 回到 UI 线程。
- 新端点的落地顺序：按文档声明路径与解码 → `httptest` 加测试 → 最后画界面。
- 涉及 2Libra API 的新能力，先读 `AGENTS.md` 里的文档分册对照表；路径、参数名、
  字段名原样照抄，未知字段必须忽略（v1 承诺只增字段）。

更完整的代理约定见 [`AGENTS.md`](./AGENTS.md)。

---

## 项目结构（关键文件）

| 模块 | 主要文件 |
| --- | --- |
| 入口与主窗口 | `main.go` |
| 授权 / 令牌 | `auth.go`、`auth_ui.go`、`auth_gate.go` |
| 列表与解码 | `api.go`、`rows.go`、`post_read.go` |
| 帖子详情 | `detail.go`、`detail_ui.go`、`detail_cache.go`、`article_render.go` |
| 个人主页 | `profile.go`、`profile_ui.go`、`attributes.go`（六维属性）、`badges.go`（徽章墙） |
| 发帖 / 评论 | `compose.go`、`compose_ui.go`、`comments.go`、`reply_view.go` |
| 交互（投票 / 表态 / 关注） | `interact.go`、`follow.go` |
| 金币 | `coins.go`、`coins_ui.go`、`coins_window.go`、`coin_events.go`、`coin_cache.go` |
| 潘多拉 | `pandora.go`、`pandora_window.go`、`pandora_ui.go` |
| 通知 | `notifications.go`、`notifications_ui.go`、`stream.go`（SSE） |
| 搜索 / 节点 | `search.go`、`search_ui.go`、`nodes.go`、`cascader.go` |
| 图床 / 编辑器上传 | `image_hosts.go`、`image_hosts_adapters.go`、`editor_upload.go` |
| 代理 / 主题 / 托盘 | `proxy.go`、`theme.go`、`tray.go`、`window_mode.go` |
| 本地缓存 | `offline_store.go`、`offline_cache.go`、`image_cache.go`、`reading_history.go` |
| 窗口内提示 | `toasts.go` |
| 发布说明 | `CHANGELOG.md` |

---

## 更新与发布

- 推送形如 `v0.1.0` 的 tag 即触发 [`.github/workflows/release.yml`](./.github/workflows/release.yml)
  的发布流水线：每个平台在各自 runner 上并行构建、签名更新包，上传到同一个
  draft release，全部成功后才自动发布。
- tag **必须**与 `mygo.json` 里的 `version` 一致（预发布版本带 `-beta.1` 之类
  后缀，只发成 prerelease，不占用 `latest`）。
- 构建需要仓库 secret `MYGO_UPDATER_PRIVATE_KEY`（`mygo keygen` 生成的
  `mygo-update.key` 内容），用于签名更新包，与应用 `mygo.json` 里的
  `updates.publicKey` 配对。

---

## 许可

本项目采用 [MIT 许可证](./LICENSE)。
