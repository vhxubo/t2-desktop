# AGENTS.md

本仓库的编码代理约定。动代码前先读这里。

## 提交信息：只能用英文

**所有提交信息必须用英文书写，不得包含中文。** 历史里原有的中文提交信息已按这一
要求全部改写为英文（只改写了提交信息，代码内容未变），新增提交同样只写英文。

- 格式遵循 Conventional Commits：`type(scope): subject`；类型用 `feat`、`fix`、
  `refactor`、`test`、`docs`、`chore`。
- subject 用祈使句，不超过约 72 个字符，结尾不加句号。
- 需要解释背景时另起一行写正文：与 subject 之间空一行，每行约 76 个字符，
  可以用短横线列表。
- 正文里引用标识符照抄，包括代码中的中文 UI 文案（例如按钮文案）——那是引用
  代码里的字符串，不是用中文写提交信息。
- 不写工具或代理署名（例如 "Generated with ..."）。

示例：

```
fix: white 32px title bar with vector window buttons

The bar was painted with the stock light theme's grey surface, a band across
a pure white page.

- height 24 -> 32 DIP, buttons 38x24 -> 46x32
- close hovers to the system red #e81123 with a white cross
```

## 代码与注释

- 代码注释、日志、界面文案保持中文，与现有代码一致；只有提交信息必须英文。
- 测试会断言这些中文文案，改文案要同步改测试。

## 交互反馈：结果优先用窗口内 toast

**操作结果（成功或失败）优先用窗口内 toast，不要用内联文字占版面。** 保存代理、
清理缓存、标记已读、发评论、关注作者这类「点一下就有结果」的动作，结果都以窗口
底部的 toast 报告，几秒后自己消失。

- 新代码要发提示时用 `a.toast(target, text)`（见 `toasts.go`），不要在图里加一行
  红色或绿色的结果文字。页面里只留「正在做」的进度（转圈 + 说明），不留结论。
- `c.Toast` 只能在构建期调用。后台结果先排队，再由目标窗口的 view 调
  `a.drainToasts(c, ...)` 领走；不要在协程或状态回调里直接调 `c.Toast`。
- 目标窗口不在了就退回主窗口（`mainClaimsToasts`），别让结果悄悄丢掉。
- 页面级状态例外，仍留在页面里：整页「加载失败」、「加载更多失败」这类要说明页面
  为什么是空的、并且要给出「重试」入口的提示保持内联，不要换成 toast。
- 测试用 `tt.HasText(...)` 断言 toast 文案，用 `tt.Advance(...)` 验证它会自己消失；
  toast 只在构建期发出，所以断言前要先 `tt.Frame()` 让窗口领走队列里的提示。

## 项目约定

- Go 桌面应用，界面是 github.com/egoist/mygo 的原生 UI 包 `ui`，没有 webview 页面。
- 应用状态里不要保存 `ui.Element` 或 `*ui.Context`：元素每次构建都会失效。
  后台结果通过 `Window.Update` / `app.publish` 回到 UI 线程。
- 改 UI 前先按 `.agents/skills/mygo-maintenance/SKILL.md` 的约定核对 mygo 版本与 API。
- 登录令牌（含 refresh_token）一律走 `mygo.Secrets`（OS 凭据库：Windows
  凭据管理器 / macOS Keychain / Linux Secret Service），不得明文落盘。实现见
  `auth.go` 的 `tokenStore` 接口与 `secureTokenStore`：仅在凭据库不可用
  （`errors.ErrUnsupported`）或会话超过 `mygo.MaxSecretSize` 时才退回明文
  `token.json`。测试经 `tokenStoreFactory` 改用文件实现以隔离并保留 `token.json`
  断言。terms.md 要求令牌加密存储，这条硬规则对应。

## 网络请求：一律走配置的代理

**所有出站 HTTP(S) 请求都必须走 `proxy.json` 里配置的代理**，跟随系统 / 手动 /
直连三种模式与「不走代理的地址」一并生效。不允许出现「列表走代理、某个功能
直连」的漏口；新加端点、图床、图片链接、第三方库或插件时同样适用。

- 应用自己的请求统一用 `Client.httpClient()` / `AuthManager.http` /
  `Client.streamHTTPClient()`，它们都来自 `proxyManager.Transport()` 这一个共享
  transport（见 `proxy.go`、`api.go`、`stream.go`）。不要写 `http.Get`、
  `http.Post`、`&http.Client{}` 或自建 `http.Client` / `http.Transport`。
- `main()` 在装载更新插件之前调 `installGlobalProxy`，把规则接到
  `http.DefaultTransport` 与 mygo 的 `netproxy.Transport()` 上：检查更新、
  GitHub Release manifest / 更新包下载、裸客户端兜底与第三方库默认客户端
  因此都走代理。接新插件或依赖时先确认它用这两条全局出口之一，否则显式把
  `a.proxy.Client(...)` 传进去（例如插件选项里的 `Options.Client`）。
- 检查更新由 mygo updater 内部发请求，依赖 `netproxy.Transport()` 的共享单例；
  升级 mygo 前重核 `updater.go` 的 `httpGet`，并跑
  `TestUpdateCheckUsesConfiguredProxy`。
- `Save` / `Reload` 已经断开所有共享 transport 的空闲连接；新加的 transport
  要接进 `closeIdleConnections`，否则切换代理后会复用连着旧代理的连接。
- 例外：`a.srv.OpenURL` 打开系统浏览器（登录、注册、外链），由浏览器自己的
  代理设置决定，不是应用内请求。
- 新增网络出口要补一条 `httptest` 假代理用例，断言请求确实经过代理
  （写法见 `proxy_test.go`）。

## 2Libra 开放 API 文档

应用里的数据全部来自 2Libra 开放 API v1。文档入口：
<https://r2.2libra.com/openapi/v1/index.md>（通用约定、错误码、端点总表）。加端点
或改字段前先读对应分册，不要凭记忆写：

| 分册 | 内容 |
| --- | --- |
| [oauth.md](https://r2.2libra.com/openapi/v1/oauth.md) | 授权流程、PKCE、令牌刷新与撤销 |
| [api-read.md](https://r2.2libra.com/openapi/v1/api-read.md) | 读端点：参数、分页、响应字段 |
| [api-write.md](https://r2.2libra.com/openapi/v1/api-write.md) | 发帖、评论、投票、表态、收藏、关注 |
| [api-economy.md](https://r2.2libra.com/openapi/v1/api-economy.md) | 金币、签到、打赏、盲盒、付费墙、上传 |
| [terms.md](https://r2.2libra.com/openapi/v1/terms.md) | 第三方接入条款，涉及新能力时必读 |
| [changelog.md](https://r2.2libra.com/openapi/v1/changelog.md) | 面向第三方的变更记录 |
| [full.md](https://r2.2libra.com/openapi/v1/full.md) | 以上全文拼接版 |

硬规则（写错任何一条都会是难查的线上问题）：

- **路径、参数名、字段名原样照抄**，包括 camelCase 与 snake_case 混用的端点
  （投票的 `voteId` / `optionId`、评论的 `parentId`）。
- 所有响应（含 `POST /api/oauth/token`）都在 `{c,m,d,t}` 信封里；令牌在
  `d.access_token`，不在顶层。
- 逻辑判断只依赖 HTTP 状态码 + `code`，不要解析 `m` 的中文文案。
  `insufficient_scope` 与 `permission_denied` 都是 403，只能靠 `code` 区分；缺权限
  统一交给 `auth_gate.go` 的授权链路，它会重跑被中断的调用。
- 标 🔑 的写端点必须带 `Idempotency-Key`（`postJSON` 的第 3 个参数）：超时重试用
  同一个键，内容变了换新键，见 `compose.go`。
- **列表字段名不统一**：`posts` / `items` / `list` / 裸数组按端点各不相同。用
  `tabSpec.ListKey` 声明，不要写通用的「取 items」函数。
- 未知字段必须忽略：解码走 `object.str/num/sub` 这类容错读取，不要用严格结构体
  一次 Unmarshal 到位（v1 承诺只增字段）。
- scope 按最小必要申请；新增能力要加进 `auth.go` 的 `defaultScopes`。文档标
  「需开通」的端点默认不授予，需要在应用申请页面说明用途。
- SSE 端点 `GET /v1/me/notifications/stream` 没有信封，只认 `Authorization` 头，
  实现见 `stream.go`（15 分钟一轮重连是正常节奏）。

文档分册与代码的对应关系：

| 分册 | 代码 |
| --- | --- |
| oauth.md | `auth.go`、`auth_ui.go`、`auth_gate.go` |
| api-read.md | `api.go`（列表与解码）、`detail.go`、`profile.go`、`attributes.go`（六维属性）、`badges.go`（徽章墙与佩戴状态）、`user_card.go`、`notifications.go` |
| api-write.md | `compose.go`、`vote.go`（投票）、`detail.go`（发评论）、`follow.go`、`interact.go` |
| api-economy.md | `coins.go`、`coins_window.go`、`badges.go`（佩戴切换，scope `write:economy`）、`image_hosts.go`（上传；官方图床只对 Pro 开放）、`image_hosts_adapters.go`（第三方图床协议：Imgur / Tikolu / MJJ.Today / ImgBB / Skyimg / 111666.best / StarDots，来自「通用图片上传助手」脚本） |
| 搜索（`/v1/search`） | `search.go`、`search_ui.go` |
| 节点（`/v1/nodes`） | `nodes.go`（请求、父子关系、本地缓存）、`compose.go`（两列选项）、`cascader.go`（级联选择组件） |

新端点的落地顺序：按文档声明路径与解码 → 用 `httptest` 加测试（断言路径、查询
参数、请求体字段与错误分支）→ 最后画界面。文档与线上响应不一致时以实测为准，
并在注释里写清原因（例：`GET /v1/nodes` 顶层节点自带 `children`，而
`?parent=<slug>` 返回的是 `{items:[...]}` 而不是文档写的数组；`nodes.go` 两种
形状都认，正常情况一次顶层请求就够）。

## 提交前验证

- `CGO_ENABLED=0 go test ./...`
- `go tool mygo vet .`
- `gofmt -l .` 不应输出任何文件。
- 改 UI 时用 `ui.NewTester` 复现：它的无头渲染与真实窗口一致，可用
  `Find` / `Texts` / `Image` 断言布局与像素。
- 新增或改动网络请求时，补一条 `proxy_test.go` 的假代理用例并跑通，
  确认这个出口确实经过配置的代理。

## 发版要求

**发版（打 tag / 出包 / 推送更新）前，必须满足以下硬性条件。** 任何一条不达标
都不得发版；宁可推迟发布，也不带着残缺或隐患上线。

### 1. 构建与运行必须正常

- `CGO_ENABLED=0 go build ./...` 必须零错误、零未定义引用；不得靠注释掉代码或
  砍功能来绕过编译报错。
- 应用必须能正常启动并进入主界面：授权登录链路、主列表加载、窗口与托盘都能
  跑起来，不能「能编译但一打开就崩」。
- 不得有任何会在启动期或空闲期必现的 panic / 空指针 / 越界；已有的恢复逻辑
  （如 SSE 重连、代理切换断连）要真的生效，而不是形同虚设。

### 2. 不得有未完成的代码逻辑

- 禁止以占位方式提交：`panic("not implemented")`、`// TODO` / `// FIXME` 标记、
  空函数体、`return nil` / `return 0` 占位返回、只声明不实现的接口。
- 凡是本次发版范围内宣称支持的功能，必须完整可运行、有真实行为，不能是「半截
  功能」或「点了没反应」的壳。
- 跨文件、跨包的调用要闭合：入口有处理、出错有兜底、结果有反馈（toast 或内联
  状态），不留「中间人」式的半成品接线。

### 3. 不得有残留代码

- 清理死代码：未被引用的函数、变量、常量、类型、字段；改完逻辑后旧实现要删掉，
  不要整段注释掉留着「以后参考」。
- 清理调试残留：临时 `println` / `fmt.Print` / `log.Print`、临时开关、写死的测试
  token、调试用的硬编码地址与端口。
- 清理过期分支保护：被新实现完全取代的旧函数，连同其测试与引用一并移除，保持
  代码库「只此一份真相」。

### 4. 基础功能必须正常

发版前至少手测或通过无头测试确认以下核心路径可用：

- 授权：登录、刷新令牌、scope 不足走 `auth_gate.go` 重跑。
- 阅读：主列表加载、详情、评论、六维属性、徽章墙与佩戴、用户卡片、通知流。
- 互动：发帖、评论、投票、表态、收藏、关注。
- 经济：金币、签到、打赏、盲盒、付费墙、上传（含图床适配）。
- 搜索与节点：搜索、节点级联选择、发帖两列选项。
- 网络出口：所有请求确实走 `proxy.json` 配置（含检查更新、插件默认客户端）。

### 5. 回归与校验全绿

- `CGO_ENABLED=0 go test ./...` 全绿，含 `proxy_test.go` 假代理断言、
  `auth_gate` 授权链路、`TestUpdateCheckUsesConfiguredProxy`。
- `go tool mygo vet .` 无新增告警；`gofmt -l .` 无输出。
- 改动 UI 文案已同步改测试；API 字段名按 2Libra 文档原样，无臆测。
- 发版即意味以上「提交前验证」全部满足，发版失败时先回滚到最近一个全绿提交。

### 6. 发版自查清单

发版前逐条核对，全部打勾方可进行：

- [ ] `CGO_ENABLED=0 go build ./...` 零错误
- [ ] 应用能启动并进入主界面，无必现 panic
- [ ] 无 `TODO` / `FIXME` / 占位 / 空实现
- [ ] 无死代码、调试残留、被注释的旧实现
- [ ] 上述「基础功能」每条路径已验证可用
- [ ] `CGO_ENABLED=0 go test ./...` 全绿
- [ ] `go tool mygo vet .` 与 `gofmt -l .` 干净
- [ ] UI 文案与测试、API 字段与文档一致
