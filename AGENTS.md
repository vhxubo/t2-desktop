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
| api-read.md | `api.go`（列表与解码）、`detail.go`、`profile.go`、`user_card.go`、`notifications.go` |
| api-write.md | `compose.go`、`detail.go`（发评论）、`follow.go`、`interact.go` |
| api-economy.md | `coins.go`、`coins_window.go` |
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
