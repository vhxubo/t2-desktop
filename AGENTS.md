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

## 操作按钮：图标 + tooltip

**界面上的操作按钮优先用「图标 + tooltip」表现，不写文字标签；所有关闭按钮一律
如此。** 图标把宽度留给内容，说明放在悬浮提示里，读屏与点击测试靠无障碍名命中。

- 关闭一律用 ✕ 图标 + tooltip「关闭」。窗口标题栏用 `titleBarButton`，详情页悬浮
  工具条用 `detailToolButton`，编辑器工具条用 `editorIconButton`，对话框标题行 /
  设置项 / 列表行用 `iconActionButton`（见 `icon_button.go`）。不要写
  `ui.Button(c, "关闭")`，也不要为某个位置再抄一套图标按钮画法。
- 通行图标（叉、废纸篓、刷新、返回、收起 / 展开、齿轮、退出、发送）能独立表意的
  动作都用图标；含义不确定、或同一处并排几个图标会分不清的（账号行里的「打开 /
  退出」）保留文字。
- 图标按钮两样必须齐备：`.Label(...)` 给读屏、键盘焦点与点击测试用，`.Tooltip(...)`
  说明点了会发生什么（不是把 label 复述一遍）。只画图标不挂 Tooltip 等于让人猜。
- 图标本身只是装饰时（窗口标题、页签、空状态插图）才可以直接 `ui.Icon` 放着。
- 文字按钮留给这些位置：主操作（发布、保存、签到、投入金币）、对话框底部成对的
  取消 / 确认、页面级状态里的「重试 / 去登录 / 打开浏览器授权」、带内容的行内按钮
  （昵称、节点、楼层号）与二选一芯片。
- 新图标加在 `icons.go`（24×24、`stroke=currentColor` 的线性图标），不要在界面
  代码里现场拼 SVG。
- 改按钮文案或换成图标时同步改测试：图标按钮用 `tt.Click("无障碍名")` /
  `tt.Access("无障碍名")` 命中，`AccessInfo.Description` 就是 tooltip 文案。

## 项目约定

- Go 桌面应用，界面是 github.com/egoist/mygo 的原生 UI 包 `ui`，没有 webview 页面；
  唯一的例外是多账号授权页（见下方「授权走哪个浏览器」），它放在内置浏览器窗口里。
- 应用状态里不要保存 `ui.Element` 或 `*ui.Context`：元素每次构建都会失效。
  后台结果通过 `Window.Update` / `app.publish` 回到 UI 线程。
- 改 UI 前先按 `.agents/skills/mygo-maintenance/SKILL.md` 的约定核对 mygo 版本与 API。
- 登录令牌（含 refresh_token）一律走 `mygo.Secrets`（OS 凭据库：Windows
  凭据管理器 / macOS Keychain / Linux Secret Service），不得明文落盘。实现见
  `auth.go` 的 `tokenStore` 接口与 `secureTokenStore`：仅在凭据库不可用
  （`errors.ErrUnsupported`）或会话超过 `mygo.MaxSecretSize` 时才退回明文
  文件（空键是旧版的 `token.json`，多账号是 `token-<id>.json`）。测试经
  `tokenStoreFactory` 改用文件实现以隔离并保留 `token.json` 断言。terms.md 要求
  令牌加密存储，这条硬规则对应。

## 多账号：每个账号一个窗口

本机可以同时挂多个账号，实现分三处，改动前先弄清分工：

- `auth.go`：`AuthManager` 仍然只管**一个**账号（一份令牌、一次刷新、一次授权、
  一条凭据）。`tokenStore` 的键就是账号 id（见 `tokenSecretKey` /
  `tokenFilePath`），空键是旧版单账号会话，升级后第一次启动由
  `accountBook.migrateLegacySession` 迁移成正式账号。
- `accounts.go`：`accountBook` 管「本机有哪些账号」——账号索引 `accounts.json`
  （只有昵称/头像这类展示信息，**绝不放令牌**）、每个账号一个 `*account`（会话 +
  可选的窗口状态）、添加账号的浏览器授权、退出账号、以及窗口没开的账号的未读
  轮询与令牌保活。全局同一时刻只允许一次授权在等回调（`claimLogin`）。
- `app_window.go` + `main.go` 的 `newAppWithDeps`：`app` 是**一个账号的界面**，
  窗口关掉就丢弃，账号仍留在账号册里。代理、外观、图片缓存与离线缓存是进程级
  共享依赖（`appDeps`），不要每个窗口各建一份：离线缓存各自整份写回会互相覆盖。

加功能时按这个分工放代码：会话相关进 `AuthManager`，账号列表与窗口调度进
`accountBook`，界面状态进 `app`。新窗口都必须能安全销毁并重建（视图每帧重算，
状态里不存窗口对象以外的临时值）。

### 授权走哪个浏览器

- **第一个账号**走系统浏览器 + 自定义协议深链（`OnOpenURL` → `accountBook.handleDeepLink`）。
- **第二个及以后的账号、以及重新登录**，只要系统 webview 可用（`mygo.WebViewAvailable()`），
  就走 `login_window.go` 的内置浏览器窗口：`mygo.NewEphemeralSession()` 的独立会话，
  没有系统浏览器里的 cookie，用户可以直接登录另一个账号，不必先去网页端退出。
  授权结果的回调是自定义协议，webview 不会加载它，由 `Page.OnWillNavigate` /
  `OnDidFailLoad` 拦下后交给同一个 `completeCallback`（跟深链完全同一条完成路径）。
- 内置浏览器起不来（缺 WebView2 运行时 / WebKitGTK、应用未就绪）时退回系统浏览器，
  面板上给出原因。不要为内置浏览器另写一套令牌交换：那是 `AuthManager.Complete` 的活。
- 重新登录时授权回来的昵称会和账号核对（`persistLogin`）：不是同一个人就另算新账号，
  绝不把别人的令牌装到已有账号上。

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

### 限流退避：被 429 之后不要再发请求

- 2Libra API 的限流是 HTTP 429 + `code: "rate_limited"`，错误体根上的
  `retry_after` 是秒数（见开放 API 文档 4.6）。`Client.getJSON` / `writeJSON`
  统一走 `sendJSON`：它先查 `rateLimitGate`，退避期内直接返回 `rate_limited`
  错误而不发网络请求；收到限流后按 `retry_after` 退避，没给时按 5/10/20/
  40/60 秒逐级退避，封顶 60 秒。
- 闸门挂在共享的 `proxyManager` 上，多个账号窗口与后台轮询共用；一个窗口
  被限流，其它窗口在退避期内也跳过。无头测试里没有共享闸门时客户端各自
  用私有的一份。
- 视图每帧调用的 `ensure*` 加载器必须在失败后退避：`ensureLoaded` 已经会
  先看闸门；新加载器不要每帧重发注定被拒的请求。普通失败也沿用各状态里
  已有的 `triedAt` / `failures` 冷却写法（见 `comment_cost.go`、`nodes.go`）。
- SSE 通知流有自己的 5s→10s→20s→40s→60s 重连退避（`stream.go`）；第三方
  图床与 multipart 上传各有自己的配额规则，不受 2Libra API 闸门影响。

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
| api-read.md | `api.go`（列表与解码）、`detail.go`、`profile.go`（`/v1/me` 与公开资料）、`attributes.go`（六维属性）、`badges.go`（我的徽章墙与佩戴状态）、`user_card.go`（资料卡、个人介绍、他人徽章墙）、`notifications.go`（含 `keyword` 搜索）、`follows.go`（关注 / 粉丝名单、关注流未读数）、`collections.go`（公开合集、合集内帖子，浏览界面见 `collection_browse.go`）、`redeem_history.go`（我兑换过的兑换码，界面在金币窗口「兑换」页） |
| api-write.md | `compose.go`（发帖与短动态）、`topic_node.go`（不选节点时的 AI 自动分区轮询与「节点寻找中」）、`vote.go`（投票与结果分布）、`detail.go`（发评论）、`edit.go`（编辑帖子 / 编辑评论 / 追加附言，界面在 `edit_window.go`）、`comments_status.go`（关闭 / 开启评论）、`follow.go`、`interact.go`、`bookmarks.go`（评论书签）、`collections.go`（新建合集 / 改名 / 删除 / 归入与移出） |
| api-economy.md | `coins.go`、`coins_window.go`、`pandora.go`（盲盒）、`badges.go`（佩戴切换，scope `write:economy`）、`restricted.go`（受限内容读取、竞拍与解锁）、`bets.go`（竞猜）、`redeem.go`（兑换码领取，界面在详情页正文末尾）、`reward_pool.go`（金币池状态与注资，含评论领到的金币）、`avatar.go`（换头像 / 重置，界面在「我的」资料头）、`about_edit.go`（个人介绍编辑，界面在本人资料卡）、`scores.go` / `scores_window.go`（评分榜）、`image_hosts.go`（上传；官方图床只对 Pro 开放）、`image_hosts_adapters.go`（第三方图床协议：Imgur / Tikolu / MJJ.Today / ImgBB / Skyimg / 111666.best / StarDots，来自「通用图片上传助手」脚本） |
| 搜索（`/v1/search`） | `search.go`、`search_ui.go` |
| 标签（`/v1/tags/hot`） | `tags.go`、`search_ui.go`（搜索空状态的热门标签） |
| 节点（`/v1/nodes`） | `nodes.go`（请求、父子关系、本地缓存、单节点详情）、`compose.go`（两列选项与选后简介）、`cascader.go`（级联选择组件） |

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

其中构建、格式、静态检查、回归测试、占位标记已由
`.github/workflows/release.yml` 的 `release-gate` 作业在推送 tag 时自动拦截；
该作业不过就不进入多平台构建，见 `RELEASE.md`。

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

## 发版步骤（本地优先，远程人工）

代理可独立完成「本地准备」，但**触发 CI 公开发版的远程推送必须由人工执行**——
推 tag 即触发构建并对外发布，不是代理该替人做的决定。

1. **本地先过发布门禁**：在本地跑与 `release-gate` 一致的命令，全绿才继续——
   `CGO_ENABLED=0 go build ./...`、`gofmt -l .` 无输出、
   `go tool mygo vet .`、`CGO_ENABLED=0 go test ./...`、且无
   `TODO`/`FIXME`/`panic("not implemented")` 占位。门禁不过先修，不进下一步。
2. **改版本号**：`mygo.json` 的 `version` 递增（如 `0.2.0` → `0.3.0`）；预发布带
   `-beta.1` 之类的后缀（只发 prerelease，不占 `latest`）。
3. **写更新日志**：把 `CHANGELOG.md` 顶部「未发布」小节改成 `## <version>`，并
   在其上新开一个空的「未发布」占位（lead-in 文案保留）。
4. **本地提交**：英文信息 `chore: release vX.Y.Z`（见上方「提交信息」硬规则）。
5. **本地打 tag**：`git tag v<version>`，tag 必须与 `mygo.json` 的 `version` 完全
   相等（含预发布后缀），否则 release 工作流会在校验步 `exit 1`。
6. **推远程交由人工**：代理**不**自动 `git push`。由人在本地确认后执行
   `git push origin main && git push origin v<version>`，推送 tag 即触发 CI：
   `release-gate` 门禁 → 多平台构建并签名更新包 → 自动发布为 `latest`
   （带预发布后缀则只发 prerelease）。仓库须为 Public，且配置了
   `MYGO_UPDATER_PRIVATE_KEY` secret，否则构建作业会报错退出。

> 详细 CI 机制见 `RELEASE.md`；`CHANGELOG.md` 的 `<version>` 小节同时作为 GitHub
> Release 说明与应用内更新窗口的发布说明，写清本次改动。
