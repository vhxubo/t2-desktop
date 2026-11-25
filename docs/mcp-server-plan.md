# MCP 服务能力设计（局域网 Agent 接入）

- 状态：待评审，未实施
- 日期：2026-11-21
- 范围：应用内置 MCP 服务端、设置页开关与免责声明、读工具（帖子列表 / 正文 / 评论）、写工具（发帖 / 回复评论）
- 约束：界面走 lobe-mygo；所有出站请求仍走 `proxyManager` 共享 transport 与共享限流闸门；令牌不得明文落盘

## 1. 目标与非目标

### 1.1 目标

1. 在客户端进程内起一个 MCP 服务端，让本机或局域网内的 AI Agent 读取 2Libra 内容。
2. 第一批只做**读**：帖子列表（首页 / 各分区 / 最新 / 社区）、帖子正文、评论、最新评论流、关注动态，以及批量聚合（一次取多篇正文与评论，供总结）；确认稳定后再做**写**：发帖、回复评论。具体场景见 1.3。
3. 能力默认关闭，必须在「设置 → MCP」里手动开启；开启前弹免责声明，明确后果由使用者承担。
4. 界面与交互保持 Lobe 风格，不新造一套视觉语言。
5. 为后续持续开发留出清晰的分层：协议层、工具层、数据映射层、界面层互不粘连。

### 1.2 非目标（本期不做）

- 不做 stdio 传输，不做独立守护进程 / 独立可执行文件。
- 不做公网暴露、反向代理、TLS 终止、OAuth 授权服务器。
- 不做 MCP 的 `resources` / `prompts` / `sampling` / `elicitation`（P3 再评估）。
- 不做发短动态、投票、打赏、竞猜、图床上传等扩展写能力。
- 不在 MCP 里复制一套业务规则；能复用 `Client` 与既有解码的，一律复用。

### 1.3 典型使用场景

这些场景决定工具粒度：Agent 不会满足于「一次一条」，它需要按「一批内容 + 受控上下文」读取。因此工具分两层——**原语**（列表、正文、评论）负责精确取数，**聚合**（`t2_collect_posts` / `t2_digest_feed`）负责把一批帖子连正文带评论一次取回，并统一截断，避免模型自己循环十几次、把上下文和限流都打满。

| 编号 | 场景 | 工具链 | 输出与约束 |
| --- | --- | --- | --- |
| S1 | 今日首页热门内容总结 | `t2_list_posts(feed=hot, limit=20)` → `t2_collect_posts(short_ids, comment_limit=5)` | 先拿 20 条标题，再按需取正文；正文默认 3k 字/篇，评论 5 条/篇 |
| S2 | 其它各分区的内容总结 | `t2_list_nodes` → 逐个 `t2_list_posts(feed=home, parent_slug=…, limit=10)` 或 `node_id=…` → 可选 `t2_collect_posts` | 分区来自节点树，不写死；一轮最多 6 个分区，避免瞬间打满限流 |
| S3 | 最新评论里的热门，以及大家都说了什么 | `t2_list_latest_comments(limit=50, sort=reactions)` | 接口只有一页 50 条；`sort=reactions` 只在这 50 条内排序，不冒充全站热门；每条带正文摘要 |
| S4 | 动态页面里的人都发布了什么 | `t2_list_timeline(page, limit=30)` | 帖子与评论混排，每条带 `kind: post \| comment`；需登录 |
| S5 | 某个列表所有页的正文总结与评论总结 | 循环 `t2_list_posts(page=1..N)` 取 `short_ids` → `t2_collect_posts` 每批 ≤10 | 单次最多 5 页 / 30 篇；超出由 Agent 分批，工具不无限翻页 |

补充说明：

- S1 / S2 在 P2 可以直接用 `t2_digest_feed` 一次完成（列表 + 正文 + 评论），减少往返。
- S5 本质是 N+1 请求（1 次列表 + 每篇 1 次正文 + 1 次评论），必须靠批量上限、并发 2 与截断控制；见 8.3。
- S5 的「列表」默认指首页 / 分区 / 最新这类信息流分页；如果指的是某个合集，走 P3 的 `t2_list_collection_posts` 取 `short_ids`，再交给同一个 `t2_collect_posts`。
- 所有场景的「总结」由模型完成，服务端只提供受控素材，不引入 LLM 依赖，也不在工具里生成摘要文案。

对应的自然语言提问（写进 README 的示例）：

```text
总结一下今天首页的热门内容。
AI 分区最近都在聊什么？
最新评论里大家在讨论什么？挑几条热度高的说说。
我关注的人今天都发了什么？
把这个列表里的帖子都读一遍，分别总结正文和评论。
```

## 2. 关键决策

| 编号 | 决策 | 结论与理由 |
| --- | --- | --- |
| 1 | 传输方式 | MCP Streamable HTTP，单端点 `POST /mcp`，响应 `application/json`。局域网 Agent 无法用 stdio，HTTP 是唯一合理选择 |
| 2 | 进程模型 | 与应用同进程，作为进程级单例挂在 `appDeps` 上。需要 `AuthManager`、`mygo.Secrets`、共享代理与限流闸门，拆进程只会重复实现这些 |
| 3 | 默认状态 | `enabled=false`，不监听任何端口；`write_enabled=false` |
| 4 | 监听范围 | 默认只绑 `127.0.0.1`；打开「允许局域网访问」才绑 `0.0.0.0`，且仍逐请求校验来源必须是回环或私有地址 |
| 5 | 认证 | 每个服务一个 Bearer 令牌，32 字节随机；比对用 `subtle.ConstantTimeCompare`；令牌存 OS 凭据库，不可用时才退回 `mcp.json`（0600） |
| 6 | 免责声明 | 两个独立确认点：首次开启服务、首次开启写操作，各弹一次；时间戳记在配置里 |
| 7 | 身份 | 固定使用应用当前的登录会话；未登录时读走匿名，写直接报错并提示先登录 |
| 8 | 工具命名 | 统一 `t2_` 前缀，避免与其它 MCP 服务重名；工具描述写清「只读 / 写入」 |
| 9 | 幂等 | 写工具接受可选 `idempotency_key`；缺省按「工具 + 规范化参数」做 SHA-256，重试不会重复发帖 |
| 10 | 审计 | 追加写 `mcp-audit.jsonl`（0600，2 MB 轮转）；只记工具名、参数摘要、结果码、耗时，不记正文 |
| 11 | 限流 | 复用 `proxyManager.rateLimitGate()`；MCP 入口另加每来源 120 次/分钟、全局并发 4 的本地保护 |
| 12 | 端口 | 默认 `8737`；被占用时报错并保持关闭，不静默换端口，避免客户端配置指向错误地址 |
| 13 | 工具分层 | 原语（列表 / 正文 / 评论）+ 聚合（批量取正文与评论）；聚合只做取数与截断，不做摘要、不引入 LLM |
| 14 | 上下文预算 | 单篇正文默认 3k 字、单条评论 500 字、单次响应 ≤200 KB；超限先丢评论再截正文，结果里标 `truncated` |

## 3. 现状与代码位置

### 3.1 已有可复用资产

| 能力 | 位置 | 复用方式 |
| --- | --- | --- |
| 列表端点契约 | `api.go` 的 `tabSpecs`、`endpoint`、`decodePage`、`extractList` | MCP 自己拼 query（需可调 `limit`），路径与参数名照抄 |
| 容错解码 | `api.go` 的 `object` / `asObject` / `str` / `num` / `sub` | 所有 MCP DTO 都用它读，不写严格结构体 |
| 帖子详情 | `detail.go` 的 `fetchTopic`、`topicDetail`、`decodeTopic` | 直接调用，再把 `Content` 转纯文本 |
| 评论 | `detail.go` 的 `fetchReplies`、`replyItem`、`replyPage` | 直接调用；要可调 `limit` 时走新增的 `fetchRepliesLimit`，树形与扁平两种都支持 |
| 最新评论流 | `api.go` 的 `tabSpecs[4]`（`/v1/comments/latest`，`ListKey=items`）、`comments.go` 的 `decodeCommentItem` | 单页 50 条；DTO 直接读 `decodeCommentItem` 的字段，不重写解码 |
| 关注动态 | `api.go` 的 `tabSpecs[6]`（`/v1/me/follows/timeline`）、`comments.go` 的 `looksLikeComment` | 同一响应混排帖子与评论，按 `looksLikeComment` 分流后再套各自的 DTO |
| 节点树 | `nodes.go` 的 `loadNodeTree`、`composeNode`（`ID` / `Slug` / `Name` / `ParentSlug` / `PostCount`） | 直接调用；`ParentSlug` 正好是 `t2_list_posts` 的筛选参数 |
| 表态计数 | `comments.go` 的 `reactionTextFrom` | 只复用思路；MCP 需要数值，另写 `reactionCount(o)` 累加 `count`，不解析展示字符串 |
| 发帖 | `compose.go` 的 `createTopic`、`composeTitleMax`、`composeContentMax` | 复用常量与调用；payload 由 MCP 层按文档字段拼 |
| 回复 | `detail.go` 的 `createReply`、`replyTarget`、`replyMaxLevel` | 需要抽出底层 `postReply`（见 8.4） |
| HTML 转文本 | `comments.go` 的 `htmlToText` | 正文与评论统一转纯文本 |
| 会话与令牌刷新 | `auth.go` 的 `AuthManager`、`withAuthRetry` | MCP 每次请求现建 `Client{Auth: auth, Proxy: deps.proxy}` |
| 共享代理 | `proxy.go` 的 `proxyManager.Transport()` / `Client()` | 不新建任何 `http.Client` 或 `Transport` |
| 共享限流 | `rate_limit.go` 的 `rateLimitGate` | 由 `Client` 自动使用，MCP 不重复实现退避 |
| 凭据库 | `auth.go` 的 `secureTokenStore`（`mygo.Secrets`） | 新增 `mcpTokenStore`，沿用同一套「不可用才落盘」策略 |
| 弹窗与设置卡片 | `disclaimer_ui.go`、`settings_ui.go` 的 `settingsSection` / `settingsCheckbox` | 免责声明复用 `feedback.Modal` 写法；设置页复用卡片版式 |
| 配置读写 | `auth.go` 的 `configDir` / `readJSONFile` / `writeJSONFile` | `mcp.json` 用同一套，权限 0600 |

### 3.2 现状缺口

1. 应用目前**没有任何入站 HTTP 服务**，`net.Listen` / `http.Server` 在仓库里只出现在测试中。生命周期、优雅关闭、端口占用处理都要新建。
2. 设置页已有 8 个分类，宽度 520；再加一个会挤压标签栏，需要同时调整窗口宽度并验证。
3. 缺少「当前会话是否可用」的进程级只读判断。`AuthManager` 本身已有锁，MCP 可以直接调用，但要新增一个 `mcpAuth()` 把「未登录 / 已退出」的判断收在一处，避免工具层各写一遍。
4. 写操作缺少可被非 UI 调用方复用的底层函数：`createReply` 只接受 `replyTarget`，无法表达「显式 parentId + replyCommentId + 可选 level」。
5. 最新评论没有 `Client` 级方法：通用 `fetch` 只返回 UI 用的 `Item`，拿不到完整正文；需要新增 `fetchLatestComments` 返回原始对象。
6. 节点树有 `loadNodeTree`，但没有进程级只读缓存；MCP 连续调用会重复请求，需要自建 10 分钟内存缓存。

## 4. 总体架构

### 4.1 组件

```
┌──────────────────────────── T2 桌面进程 ────────────────────────────┐
│                                                                     │
│  AI Agent (本机 / 局域网)                                            │
│      │  HTTP + JSON-RPC + Bearer                                    │
│      ▼                                                              │
│  ┌──────────────┐   ┌───────────────┐   ┌──────────────────────┐    │
│  │ mcp_server   │──▶│ mcp_protocol  │──▶│ mcp_tools_read/write │    │
│  │ 监听/中间件  │   │ 方法分发      │   │ 参数校验 + DTO 映射  │    │
│  └──────┬───────┘   └───────────────┘   └──────────┬───────────┘    │
│         │                                          │                │
│         │ 审计                                      │ Client          │
│         ▼                                          ▼                │
│  ┌──────────────┐                       ┌──────────────────────┐    │
│  │ mcp_audit    │                       │ 2Libra 开放 API v1   │    │
│  └──────────────┘                       │ 经 proxyManager      │    │
│                                         │ + 共享限流闸门       │    │
│  ┌──────────────┐   ┌───────────────┐   └──────────────────────┘    │
│  │ mcp_config   │◀──│ 设置页 mcp_ui │                                │
│  │ + Secrets    │   │ + 免责声明    │                                │
│  └──────────────┘   └───────────────┘                                │
└─────────────────────────────────────────────────────────────────────┘
```

### 4.2 一次工具调用的路径

1. 连接进入 `mcp_server`，依次经过：panic 恢复 → 请求体上限 → Content-Type → Origin 校验 → 来源 IP 校验 → Bearer 校验 → 本地限流 → 协议分发。
2. `mcp_protocol` 解析 JSON-RPC，`tools/call` 按名字查工具表。
3. 工具层取应用唯一的 `*AuthManager`，现场构造 `Client{Auth: ..., Proxy: deps.proxy}`。
4. 调 `fetchTopic` / `fetchReplies` / `createTopic` / `postReply`，错误统一映射。
5. 结果同时给 `content[0].text`（人读的紧凑 JSON）与 `structuredContent`（机器读）。
6. 审计记录一条，写盘失败只记日志，不影响响应。

### 4.3 生命周期

- 启用：保存配置 → `start()` 建 listener → 起 `http.Server` goroutine → 设置页显示地址与状态。
- 停用：`shutdown(ctx 3s)` → 关闭 listener → 清空会话表 → 状态置为「已停止」。
- 改端口 / 改监听范围：先停后起；失败时回到「已停止」并 toast 原因，不保留半开状态。
- 应用退出：在 `OnBeforeQuit` 链路里调用 `Stop()`，与窗口关闭同一优先级，不阻塞退出超过 3 秒。
- 退出登录：不重启服务；下一次请求发现未登录，读降级为匿名，写返回明确错误。

## 5. 安全模型

### 5.1 威胁与对策

| 威胁 | 对策 |
| --- | --- |
| 同机其它程序偷令牌 | 令牌存 `mygo.Secrets`；文件兜底 0600；界面默认打码，点「显示」才展示 |
| 局域网未授权访问 | 默认只绑回环；开局域网后仍要求 Bearer；来源必须是回环 / RFC1918 / 链路本地地址 |
| DNS rebinding（恶意网页打本机端口） | 校验 `Origin`：空或 `null` 放行（非浏览器客户端），否则必须是 `http://127.0.0.1:<port>`、`http://localhost:<port>` 或本机私有地址 |
| 恶意 Agent 以用户身份发帖 | 写操作独立开关，默认关；首次开启再弹一次声明；所有写调用进审计 |
| Agent 循环请求打爆开放 API | 共享 `rateLimitGate` 先挡；入口再加 120 次/分钟、并发 4 |
| 令牌泄露 | 设置里「重新生成」立即换令牌，旧令牌下一次请求即 401；换令牌需二次确认 |
| 端口被其它程序占用 | 启动失败即报错，不自动换端口；界面提示换端口后重试 |
| 请求体过大 / 慢连接 | 请求体 1 MiB；`ReadHeaderTimeout` 5s、`ReadTimeout` 30s、`WriteTimeout` 60s、`IdleTimeout` 120s |
| 审计日志泄露隐私 | 只记参数摘要（SHA-256 前 16 位）与短 ID，不记正文与令牌 |

### 5.2 来源地址判定

```go
// allowRemote 判断这个来源是否可以访问 MCP。
// 局域网模式下也只放行私有地址：0.0.0.0 监听是为了方便，不是为了对公网开放。
func allowRemote(ip net.IP) bool {
    return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
```

- `X-Forwarded-For` 一律不信，只看 `RemoteAddr`。
- IPv6 只放行 `::1` 与 `fc00::/7`、`fe80::/10`。

### 5.3 免责声明

两个确认点，文案共用一段组件，避免两处漂移：

- 首次把「启用 MCP 服务」从关打到开。
- 首次把「允许写操作」从关打到开。

文案（设置页弹窗，`feedback.Modal`，`NoFooter`，两个动作）：

```
启用后，本机会运行一个 MCP 服务。持有访问令牌的 AI 客户端可以读取你可见的内容；
打开写操作后，还可以用你的身份发帖与回复。

· 访问令牌等同于你的权限，请勿分享给不可信的人或程序。
· AI 生成的内容、误操作及由此产生的任何后果由使用者承担，与开发者无关。
· 服务默认只监听本机；开启局域网访问前请确认所处网络可信。

[取消]  [我已了解并启用]
```

- 点「取消」或 Esc / ✕：开关回弹到关，不写配置。
- 点「我已了解并启用」：写 `disclaimer_accepted_at`，再启动服务。
- 写操作那次写 `write_disclaimer_accepted_at`，文案末段换成「AI 可能以你的身份发布内容，请自行核对」。

## 6. 配置与存储

### 6.1 `mcp.json`

```json
{
  "enabled": false,
  "write_enabled": false,
  "bind_lan": false,
  "port": 8737,
  "token": "",
  "disclaimer_accepted_at": "",
  "write_disclaimer_accepted_at": "",
  "audit_enabled": true
}
```

- 路径：`configDir()/mcp.json`，`writeJSONFile` 保证 0600。
- `token` 字段只在凭据库不可用时才写；正常情况下留空，令牌在 `mygo.Secrets` 的 `mcp-server-token`。
- 身份固定使用应用当前登录会话，设置页不再提供身份选择。
- 读取时对未知字段忽略、缺失字段取默认值；损坏时按默认值处理并在设置页提示「配置已重置」。

### 6.2 令牌存储

```go
// mcpTokenStore 与 auth.go 的 tokenStore 同构：
// 优先 OS 凭据库，只有 errors.ErrUnsupported 或超长时才退回 mcp.json。
type mcpTokenStore struct {
    dir string
    cfg *mcpConfig
}
```

- 生成：`crypto/rand` 32 字节 → `base64.RawURLEncoding`，长度 43。
- 比对：`subtle.ConstantTimeCompare`。
- 重置：先生成新令牌写库成功，再让旧令牌失效（内存里的 `token` 字段换掉即可），避免出现「新旧都不认」的窗口。

### 6.3 状态机

```
[已关闭] --启用+声明--> [启动中] --成功--> [运行中]
    ^                      |                 |
    |                      +--失败--> [已关闭]（toast 原因）
    +--------停用/退出------+-----------------+
```

- 运行中改端口 / 改范围 = 停止 → 启动；界面显示「重启中」。
- 退出登录不改变服务状态，只影响下一次请求的身份解析。

## 7. 协议层

### 7.1 传输与端点

- 端点：`POST /mcp`。
- 只接受 `Content-Type: application/json`，否则 415。
- `Accept` 必须包含 `application/json`；同时接受 `text/event-stream`，但服务端固定回 JSON（单条响应不需要 SSE）。
- 通知（无 `id`）返回 202 + 空体。
- `GET /mcp` 返回 405（不提供服务端主动推送）；`DELETE /mcp` 结束会话，返回 204。
- `initialize` 成功时下发 `Mcp-Session-Id`；后续请求带则校验，不带也接受（兼容无状态客户端）。会话表 30 分钟空闲过期，最多 8 个。

### 7.2 版本协商

- 支持版本白名单：`2025-06-18`（初始实现）。
- 客户端请求的 `protocolVersion` 在白名单内则回显；否则回最新支持版本，由客户端决定是否继续。
- 后续请求带 `MCP-Protocol-Version` 时校验，不在白名单返回 400。
- 批量请求（JSON 数组）：按规范逐条处理，通知不回响应；单条失败只影响该条。实现成本低，同时兼容仍发批量的旧客户端。

### 7.3 JSON-RPC 骨架

```go
type rpcRequest struct {
    JSONRPC string          `json:"jsonrpc"`
    ID      json.RawMessage `json:"id,omitempty"`
    Method  string          `json:"method"`
    Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
    Code    int    `json:"code"`
    Message string `json:"message"`
    Data    any    `json:"data,omitempty"`
}
```

错误码约定：

| 场景 | 处理 |
| --- | --- |
| JSON 解析失败 | `-32700` |
| 不是合法 JSON-RPC / 缺 method | `-32600` |
| 未知方法 | `-32601` |
| 参数不合 schema | `-32602`，`data` 带字段路径 |
| 工具内部 panic | `-32603`，日志留栈，响应不带栈 |
| 2Libra 业务失败（404 / 403 / 429） | **不作为 JSON-RPC 错误**，返回 `isError: true` 的工具结果，让模型能读到原因并自行调整 |
| 写操作未开启 / 未登录 | 同上，`isError: true`，文案给出「去 T2 设置 → MCP 打开」 |

HTTP 层错误：401（缺 / 错令牌，带 `WWW-Authenticate: Bearer`）、403（来源不允许 / Origin 不合法）、413（体过大）、415（类型不对）、429（本地限流，带 `Retry-After`）。

### 7.4 工具返回格式

```go
type toolResult struct {
    Content           []contentBlock `json:"content"`
    StructuredContent any            `json:"structuredContent,omitempty"`
    IsError           bool           `json:"isError,omitempty"`
}
```

- `content[0]` 为 `{"type":"text","text":"<紧凑 JSON>"}`，保证不支持结构化输出的客户端也能用。
- `structuredContent` 与 `outputSchema` 一致（P1 先只给 `content`，P2 补 `outputSchema`）。
- 文本里不放 ANSI、不放大段 HTML；正文用纯文本，`content_html` 只在需要时给。

## 8. 工具设计

### 8.1 通用约定

- 名字：`t2_<verb>_<noun>`，全小写蛇形。
- 注解：
  - 读：`readOnlyHint: true`、`destructiveHint: false`、`idempotentHint: true`、`openWorldHint: false`。
  - 写：`readOnlyHint: false`、`destructiveHint: true`、`idempotentHint: true`（幂等键保证）、`openWorldHint: false`。
- 分页：`page` 从 1 起；帖子列表 `limit` 上限 50（列表端点的上限），评论上限 100（`detail.go` 的 `replyLimit`）；返回 `page` / `total_pages` / `has_more` / `next_page`。
- 时间：原样返回 ISO 8601 字符串，不做「3 分钟前」这种展示转换。
- 链接：每条结果都带 `url`，满足「保留作者署名与原文链接」的既有要求。
- 空值：字段缺失就省略，不编造 0 或「匿名」。
- 上下文预算（对应场景 1.3 的批量读取）：
  - 单篇正文默认 3000 字，`max_chars` 可调 500–8000；超长在段落边界截断并标 `truncated: true`。
  - 单条评论默认 500 字，最多 2000 字；只给正文，不带徽章、头像等展示字段。
  - 单次工具响应上限 200 KB；聚合工具先丢评论、再截正文，并在结果根上写 `dropped`。
- 排序诚实：接口不支持「全站热门」的评论排序；本地按表态数排序时必须标注 `ranked_within_page: true`，不能声称是全站热度。

### 8.2 P1：读工具

#### `t2_list_posts`

```json
{
  "type": "object",
  "properties": {
    "feed": {
      "type": "string",
      "enum": ["home", "hot", "weekly", "latest", "community"],
      "default": "home",
      "description": "home=首页（可按节点筛选）；hot=今日热议；weekly=近期热议；latest=新发表；community=社区讨论。关注动态请用 t2_list_timeline"
    },
    "page":  { "type": "integer", "minimum": 1, "default": 1 },
    "limit": { "type": "integer", "minimum": 1, "maximum": 50, "default": 20 },
    "node_id":     { "type": "string", "description": "子节点 uuid，仅 home 生效" },
    "parent_slug": { "type": "string", "description": "父分类 slug，仅 home 生效；与 node_id 二选一" },
    "sort": { "type": "string", "enum": ["value", "latest"], "description": "仅 home 生效，默认 value" }
  },
  "additionalProperties": false
}
```

端点映射（参数名逐字照抄文档）：

| feed | 路径 | 固定参数 |
| --- | --- | --- |
| home | `/v1/topics` | `parent_slug=All`（未筛选时）、`sort=value` |
| hot | `/v1/topics/hot` | 无 |
| weekly | `/v1/topics/hot` | `period=7d` |
| latest | `/v1/topics/latest` | 无 |
| community | `/v1/topics` | `node_id=<communityNodeID>` |

> 关注动态（`/v1/me/follows/timeline`）不放在这里：它同一份响应混排帖子与评论，字段结构不同，单独由 `t2_list_timeline` 提供。

返回：

```json
{
  "feed": "home",
  "page": 1,
  "total": 128,
  "total_pages": 3,
  "has_more": true,
  "next_page": 2,
  "posts": [
    {
      "short_id": "abc123",
      "title": "……",
      "author": { "username": "vhxubo", "id": "uuid" },
      "node": { "id": "uuid", "name": "个人生活", "slug": "life", "parent_slug": "life" },
      "created_at": "2026-11-20T10:00:00Z",
      "comment_count": 12,
      "views": 340,
      "pinned": false,
      "url": "https://2libra.com/post/abc123"
    }
  ]
}
```

实现要点：

- 不复用 `itemFrom`（它把时间转成展示文案、把字段摊平），新建 `mcp_dto.go` 的 `postSummaryFrom(object)`，只读文档字段。
- `communityNodeID` 直接用 `api.go` 的常量。
- `node_id` 与 `parent_slug` 同时给：`-32602`。
- `total_pages` 让 Agent 知道翻到第几页停（场景 5），服务端不主动翻页。

#### `t2_get_post`

```json
{
  "type": "object",
  "properties": {
    "short_id": { "type": "string" },
    "include_comments": { "type": "boolean", "default": false, "description": "同时带回评论，省一次往返" },
    "comment_limit": { "type": "integer", "minimum": 0, "maximum": 20, "default": 8 },
    "comment_sort": { "type": "string", "enum": ["floor", "reactions"], "default": "floor" },
    "include_html": { "type": "boolean", "default": false, "description": "true 时额外返回原始 HTML，默认只给纯文本" },
    "max_chars": { "type": "integer", "minimum": 500, "maximum": 8000, "default": 3000 }
  },
  "required": ["short_id"],
  "additionalProperties": false
}
```

返回字段：

- `short_id`、`title`、`url`
- `author`：`username` / `id` / `avatar`
- `node`：`id` / `name` / `slug` / `parent_name` / `parent_slug`
- `created_at` / `updated_at` / `views` / `comment_count` / `tags`
- `content_text`：`htmlToText(Content)` 的结果，按 `max_chars` 截断
- `content_html`：仅在 `include_html=true` 时返回
- `postscripts`：追加附言的 `content_text` 与时间
- `vote`：标题与选项（不含投票操作）
- `comments_closed` / `restricted` / `guard`：只回状态与条件，不回隐藏内容
- `comments`：`include_comments=true` 时返回，结构同 `t2_list_comments` 的扁平列表，已按 `comment_sort` 排序
- `truncated` / `comments_truncated`：正文超 `max_chars`、评论超 `comment_limit` 时分别置 true

实现：直接 `fetchTopic(ctx, shortID)`；`include_comments` 时按 `min(comment_limit*3, 100)` 调 `fetchRepliesLimit(ctx, shortID, 1, n, true)`（多取一些再排序），只回前 N 条；`404` 映射为 `isError` + 「帖子不存在或不可见」。

#### `t2_list_comments`

```json
{
  "type": "object",
  "properties": {
    "short_id": { "type": "string" },
    "page":  { "type": "integer", "minimum": 1, "default": 1 },
    "limit": { "type": "integer", "minimum": 1, "maximum": 100, "default": 20 },
    "flat":  { "type": "boolean", "default": false, "description": "true 返回扁平列表，false 返回楼中楼树" },
    "max_depth": { "type": "integer", "minimum": 1, "maximum": 3, "default": 3 }
  },
  "required": ["short_id"],
  "additionalProperties": false
}
```

返回每条评论：`id`、`parent_id`、`reply_comment_id`、`floor`、`level`、`author`、`created_at`、`content_text`、`deleted`、`reactions`（数值）、`rewards`、`children`（树形时）。

实现：`repliesPath` 目前把 `limit` 写死成 `replyLimit`（100），MCP 要可调 `limit`，因此先抽一个 `repliesPathLimit(shortID, page, limit, flat)`，`repliesPath` 改为用 `replyLimit` 调它（行为不变），MCP 走新函数；`max_depth` 只影响输出裁剪，不改请求。

#### `t2_get_me`

无参数。返回 `{ "authenticated": bool, "username", "avatar", "scopes", "is_pro", "role" }`。用于 Agent 在写之前确认身份，也方便排查「为什么写不了」。

#### `t2_list_latest_comments`

对应场景 S3。最新评论端点只有一页 50 条，没有翻页。

```json
{
  "type": "object",
  "properties": {
    "limit": { "type": "integer", "minimum": 1, "maximum": 50, "default": 30 },
    "sort": { "type": "string", "enum": ["latest", "reactions"], "default": "latest", "description": "reactions 只在这 50 条内按表态数排序" },
    "max_chars": { "type": "integer", "minimum": 200, "maximum": 2000, "default": 500 }
  },
  "additionalProperties": false
}
```

返回：

```json
{
  "has_more": false,
  "ranked_within_page": true,
  "comments": [
    {
      "id": "uuid",
      "short_id": "abc123",
      "post_title": "……",
      "author": "vhxubo",
      "content_text": "……",
      "floor": 3,
      "created_at": "2026-11-21T09:00:00Z",
      "reactions": 12,
      "rewards": 0,
      "node": { "name": "AI", "slug": "ai" },
      "url": "https://2libra.com/post/abc123#reply-uuid"
    }
  ]
}
```

实现：

- 新增 `Client.fetchLatestComments(ctx, limit) ([]json.RawMessage, error)`：GET `/v1/comments/latest?limit=N`，用 `extractList(d, "items")` 取数组，保留原始对象。
- DTO 用 `decodeCommentItem(asObject(raw))` 取文本字段，`reactionCount(raw)` 取数值（累加 `reactions_summary[].count`），不解析 `reactionTextFrom` 的展示字符串。
- `sort=reactions` 是本地排序，必须回 `ranked_within_page: true`；`sort=latest` 保持接口顺序，该字段为 false。

#### `t2_list_timeline`

对应场景 S4。关注动态同一份响应混排帖子与评论。

```json
{
  "type": "object",
  "properties": {
    "page":  { "type": "integer", "minimum": 1, "default": 1 },
    "limit": { "type": "integer", "minimum": 1, "maximum": 50, "default": 30 },
    "kind":  { "type": "string", "enum": ["all", "post", "comment"], "default": "all" }
  },
  "additionalProperties": false
}
```

返回 `items`：帖子项与 `t2_list_posts` 的 post 结构一致，另加 `kind: "post"`；评论项与 `t2_list_latest_comments` 的 comment 结构一致，另加 `kind: "comment"`。`kind` 过滤在服务端做，减少无关条目。

实现：

- 路径 `/v1/me/follows/timeline?page=&limit=`，需登录；匿名返回 `isError` + 「关注动态需要登录」。
- 判型必须在 `overlay` 之前：`looksLikeComment` 会把带 `content` 的对象都算评论，而帖子正文也可能在 `content` 里；先看 `comment` / `reply` 子对象，再退回 `looksLikeComment`。与 `api.go` 的 `itemFrom` 注释保持一致。
- 分页字段与 `t2_list_posts` 相同（`total_pages` / `has_more` / `next_page`）。

#### `t2_list_nodes`

对应场景 S2。没有它，Agent 只能猜分区名。

```json
{
  "type": "object",
  "properties": {
    "parent_slug": { "type": "string", "description": "只返回该父分类下的子节点；省略返回全部" },
    "include_empty": { "type": "boolean", "default": false, "description": "false 时过滤 post_count=0 的节点" },
    "flat": { "type": "boolean", "default": true }
  },
  "additionalProperties": false
}
```

返回 `nodes: [{ "id", "name", "slug", "parent_slug", "parent_name", "post_count" }]`；`flat=false` 时按父分类嵌套成 `children`。

实现：

- 直接调 `Client.loadNodeTree(ctx)`（已是 `Client` 方法，冷缓存时最多 1 次顶层 + 若干次子节点请求）。
- MCP 层加 10 分钟进程级缓存，避免 Agent 连续调用把节点接口打满；缓存只存 `[]composeNode`，不写 `nodes.json`（那是 UI 的缓存文件，两处写会互相覆盖）。
- `post_count` 为 0 的节点默认不返回，但 `parent_slug` 明确指定时保留，便于确认空分区。

### 8.3 批量聚合（服务场景 1 / 2 / 5）

#### `t2_collect_posts`

场景 5 若由 Agent 逐条调用：10 篇帖子需要 1 + 20 次往返，模型还要自己管截断与限流。批量工具把「取数 + 截断 + 部分失败」收到服务端。

```json
{
  "type": "object",
  "properties": {
    "short_ids": { "type": "array", "items": { "type": "string" }, "minItems": 1, "maxItems": 10 },
    "comment_limit": { "type": "integer", "minimum": 0, "maximum": 20, "default": 8 },
    "comment_sort": { "type": "string", "enum": ["floor", "reactions"], "default": "reactions" },
    "max_chars": { "type": "integer", "minimum": 500, "maximum": 8000, "default": 3000 },
    "include_html": { "type": "boolean", "default": false }
  },
  "required": ["short_ids"],
  "additionalProperties": false
}
```

行为：

- 去重保序，最多 10 个；超过直接 `-32602`，不静默截断。
- 每篇：`fetchTopic`；`comment_limit > 0` 时按 `min(comment_limit*3, 100)` 取第 1 页扁平评论，按 `comment_sort` 取前 N 条（`reactions` 用 `reactionCount` 排序）。
- 并发 2，整体超时 45 秒；请求被取消时停止后续抓取。
- 部分失败不拖垮整批：返回 `errors: [{ "short_id", "code", "message" }]`，成功项照常返回。
- 总预算 200 KB：先按 `max_chars` 截正文；仍超则逐篇减半 `comment_limit`；再超则只保留前若干篇并置 `dropped`。
- 返回 `posts` / `errors` / `dropped` / `truncated`，每篇带 `comments_truncated`。

#### `t2_digest_feed`（P2，可选）

一次完成「列表 + 正文 + 评论」，专给 S1 / S2 的总结任务：

```json
{
  "feed": "hot",
  "pages": 1,
  "limit": 10,
  "node_id": null,
  "parent_slug": null,
  "sort": "value",
  "comment_limit": 5,
  "max_chars": 3000
}
```

- `pages` 1–3，`limit` 1–20，总篇数 ≤30；总响应 ≤300 KB；超时 60 秒。
- 不新增取数能力，内部就是 `t2_list_posts` + `t2_collect_posts` 的编排；P1 的 `t2_collect_posts` 够用时可以先不做。

#### 场景调用示例

```text
S1: t2_list_posts(feed="hot", limit=20)
    → t2_collect_posts(short_ids=[前 10 个], comment_limit=5)

S2: t2_list_nodes(flat=true)
    → 对每个父分类: t2_list_posts(feed="home", parent_slug="ai", limit=10)
    → t2_collect_posts(该分区前 5 篇)

S5: page = 1
    while page <= min(total_pages, 5):
      ids = t2_list_posts(feed="home", page=page, limit=20).posts[].short_id
      for batch in chunks(ids, 10):
        t2_collect_posts(batch, comment_limit=10)
      page += 1
```

### 8.4 P2：写工具

#### 前置改造：抽出 `postReply`

`detail.go` 现在只有：

```go
func (c *Client) createReply(ctx context.Context, shortID, content string, target replyTarget) (replyItem, error)
```

MCP 需要「显式 parentId + replyCommentId + 可选 level」，而 `replyTarget` 无法表达「level 未知但 parent 已知」。改法：

```go
// postReply 是回复的底层入口：payload 由调用方按开放文档拼好，
// createReply 与 MCP 共用这一条写路径，避免两套幂等与错误处理。
func (c *Client) postReply(ctx context.Context, shortID string, payload map[string]any, key string) (replyItem, error)

// createReply 保留原签名，内部改成拼 payload 后调 postReply。
func (c *Client) createReply(ctx context.Context, shortID, content string, target replyTarget) (replyItem, error)
```

- 原有测试全部保留，断言路径、`parentId`、`replyCommentId`、`level` 不变。
- 新增 `postReply` 的单测：只给 `parentId`、两者都给、都不给三种情况。

#### `t2_create_post`

```json
{
  "type": "object",
  "properties": {
    "title":   { "type": "string", "minLength": 1, "maxLength": 500 },
    "content": { "type": "string", "minLength": 1, "maxLength": 20000, "description": "Markdown；支持站内 emoji 短码" },
    "node_id": { "type": "string", "description": "必填，发到哪个子节点" },
    "tags":    { "type": "array", "items": { "type": "string" }, "maxItems": 5 },
    "idempotency_key": { "type": "string", "description": "可选；重试时复用同一个键" }
  },
  "required": ["title", "content", "node_id"],
  "additionalProperties": false
}
```

- 校验：长度用 `composeTitleMax` / `composeContentMax`；`node_id` 必填；`tags` 去空去重。
- 调用 `createTopic(ctx, payload, key)`，payload 只含 `title` / `content` / `node_id` / `tags`。
- 返回 `{ "short_id": "...", "url": "...", "node_slug": "..." }`。
- 幂等键缺省算法：`hex(sha256(tool + "|" + canonicalJSON(args)))[:32]`；相同内容重复提交会被服务端视为同一次创建。需要故意发两篇相同内容时，显式传不同的 `idempotency_key`（文档里写明）。

#### `t2_reply_comment`

```json
{
  "type": "object",
  "properties": {
    "short_id": { "type": "string" },
    "content":  { "type": "string", "minLength": 1 },
    "parent_comment_id": { "type": "string", "description": "挂载点：回复一级评论时就是它；回复楼中楼时是父评论" },
    "reply_to_comment_id": { "type": "string", "description": "真正被回复的评论；缺省等于 parent_comment_id" },
    "level": { "type": "integer", "minimum": 0, "maximum": 3, "description": "可选；缺省交给服务端推断" },
    "idempotency_key": { "type": "string" }
  },
  "required": ["short_id", "content"],
  "additionalProperties": false
}
```

payload 规则（与 `createReply` 的注释保持一致）：

- 无 `parent_comment_id`：`level=0`，不带 `parentId` / `replyCommentId`。
- 有：`parentId=parent_comment_id`；`replyCommentId=reply_to_comment_id || parent_comment_id`；`level` 只在显式给出时带。
- 站点最多三级；`level>3` 直接 `-32602`。

### 8.5 P3：扩展读与体验

- `t2_search`：关键词、类型、分页。
- `t2_get_user`：公开资料、徽章、近期帖子。
- `t2_list_collections` / `t2_list_collection_posts`。
- `resources`：把帖子注册成 `t2://post/<short_id>` 资源，支持 `resources/list` 与 `resources/read`（只读场景比工具更省 token）。
- 正文 HTML → Markdown 转换，优先复用 `article_render.go` 的解析结果。
- 写操作 dry-run：`confirm=false` 时只回预览与将要提交的 payload 摘要。
- 设置页加「工具开关」逐项禁用，以及审计日志查看器（最近 50 条，只读）。
- 摘要缓存：同一 `short_id` 在 10 分钟内重复读取时复用正文，减少 S5 的重复抓取。

## 9. 设置界面

### 9.1 分类与窗口

- 新增 `settingsTabMCP`，标签文案 `MCP`，排在「通知」之后。
- 9 个标签在 520 宽下会挤：把 `settingsWindowWidth` 调到 600，`settingsWindowMinW` 调到 520，并用 `ui.NewTester` 截图确认不换行、不截断。
- 页面结构：`layout.ScrollArea` + 三个 `settingsSection` 卡片。

### 9.2 版面

```
┌ 设置 ──────────────────────────────────────────────┐
│ 网络代理  图库  图片缓存  更新  窗口  外观  评论  通知  MCP │
├────────────────────────────────────────────────────┤
│ ┌ MCP 服务 ────────────────────────────────────┐   │
│ │ 供局域网内的 AI 客户端读取与发布内容          │   │
│ │ 启用 MCP 服务                          [ 关 ] │   │
│ └──────────────────────────────────────────────┘   │
│ ┌ 连接 ────────────────────────────────────────┐   │
│ │ 地址   http://127.0.0.1:8737/mcp      [复制]  │   │
│ │ 令牌   ••••••••••••••      [显示] [复制] [重置]│   │
│ │ 状态   已停止                                  │   │
│ └──────────────────────────────────────────────┘   │
│ ┌ 范围 ────────────────────────────────────────┐   │
│ │ 允许局域网访问                        [ 关 ]  │   │
│ │ 端口  [8737]                        [保存]    │   │
│ │ 允许写操作（发帖 / 回复）              [ 关 ]  │   │
│ └──────────────────────────────────────────────┘   │
│ [复制客户端配置]              [查看审计日志]        │
└────────────────────────────────────────────────────┘
```

- 开关立即落盘并触发启停；端口需要点「保存」才重启，避免每敲一位就重启。
- 「复制客户端配置」生成：

```json
{
  "mcpServers": {
    "t2": {
      "type": "http",
      "url": "http://192.168.1.10:8737/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

- 地址行在未启用时显示占位 `未启用`，不展示令牌。
- 状态行：`已停止` / `启动中` / `运行中 · 端口 8737 · 请求 128` / `启动失败：端口被占用`。
- 禁用写操作时，若 `write_enabled=true`，开关自动回落并 toast「写操作已关闭」。

### 9.3 交互细节

- 总开关 → 需要声明 → `feedback.Modal` 盖在设置窗口上；确认后才写 `enabled=true`。
- 写开关 → 同样先声明；确认后写 `write_enabled=true`。
- 写开关关闭时，两个写工具不进入 `tools/list`；部分客户端会缓存工具列表，切换后需重连才会看到变化，设置页保存成功时提示一句。
- 「重置令牌」→ 二次确认弹窗（说明现有客户端会立刻失效）→ 生成并展示一次完整令牌，提供复制。
- 「显示令牌」默认 30 秒后自动打码，避免长期裸露在屏幕上。
- 所有按钮使用 `action.Button`，开关用 `choice.Checkbox`，输入用 `composeInput` + `field.InputProps`（与通知关键词输入一致），说明文字用 `settingsDescription`，不新造控件。
- 文案克制：卡片里只留一行说明，详细规则放本文档与 README。

## 10. 分批实施计划

每个批次独立可合并、可回滚；后一批依赖前一批的协议骨架，但不依赖其工具实现。

### P0 基础骨架（必须先做）

目标：能握手、能鉴权、能启停，工具列表为空也能被客户端连上。

| 任务 | 产出 |
| --- | --- |
| P0-1 配置与令牌 | `mcp_config.go`：`mcpConfig`、`loadMCPServerConfig`、`save`、`mcpTokenStore`、默认值、损坏回退 |
| P0-2 协议层 | `mcp_protocol.go`：JSON-RPC 解析、`initialize`、`notifications/initialized`、`ping`、`tools/list`（空）、错误码 |
| P0-3 服务生命周期 | `mcp_server.go`：listener、`http.Server`、`start/stop/restart`、优雅关闭、会话表 |
| P0-4 中间件 | 同文件：体上限、Content-Type、Origin、来源 IP、Bearer、限流、panic 恢复 |
| P0-5 审计 | `mcp_audit.go`：JSONL 追加、轮转、摘要函数 |
| P0-6 设置页 | `mcp_ui.go` + `settings_ui.go` 接入新 tab、总开关、地址/令牌/状态、免责声明 |
| P0-7 挂载 | `appDeps` 持有 `*mcpServer`；`newAppDeps` / `deps()` 传递；退出时 `Stop()` |

验收：

- 默认配置下 `lsof` / `netstat` 看不到监听端口。
- 开启后 `curl -X POST /mcp` 带正确令牌能完成 `initialize` → `tools/list`，返回空数组。
- 错误令牌 401；非私有来源 403；`Origin: https://evil.example` 403；体超 1 MiB 413。
- 关闭后端口立即释放；重复启停 20 次无 goroutine 泄漏（`runtime.NumGoroutine` 对比）。
- 设置页开关、声明、令牌复制在 `ui.NewTester` 下可复现。

### P1 读能力（第一优先级）

目标：覆盖 1.3 的五个场景——能读单个帖子与评论，也能按一批内容做总结。

| 任务 | 产出 |
| --- | --- |
| P1-1 DTO | `mcp_dto.go`：`postSummaryFrom`、`postDetailFrom`、`commentFrom`、`latestCommentFrom`、`reactionCount`，全部走 `object` 容错读取 |
| P1-2 列表 | `mcp_tools_read.go` 的 `t2_list_posts`，含 feed 表、query 拼装、`total_pages` |
| P1-3 正文 | `t2_get_post`，含 `content_text`、可选 `include_html` / `include_comments`、`max_chars` 截断、受限内容状态 |
| P1-4 评论 | `t2_list_comments`，树形 / 扁平、`max_depth` 裁剪；抽 `repliesPathLimit` / `fetchRepliesLimit` 以支持可调 `limit` |
| P1-5 最新评论 | `Client.fetchLatestComments` + `t2_list_latest_comments`（本地 `sort=reactions`，标注 `ranked_within_page`） |
| P1-6 动态 | `t2_list_timeline`，帖子 / 评论分流、`kind` 过滤、需登录 |
| P1-7 节点 | `t2_list_nodes` + 10 分钟进程级缓存（不写 UI 的 `nodes.json`） |
| P1-8 批量聚合 | `t2_collect_posts`：并发 2、部分失败、200 KB 预算、`dropped` / `truncated` |
| P1-9 身份 | `t2_get_me` |
| P1-10 错误映射 | `apiError` → `isError` 文案；429 带「请 N 秒后重试」；403 scope 提示重新授权 |
| P1-11 文档 | README 增加「MCP（实验）」一节：如何开启、如何填客户端配置、五个场景的示例提问 |

验收（按场景）：

- S1：`t2_list_posts(feed=hot)` + `t2_collect_posts` 能产出一份「今日热门」总结，覆盖 ≥10 条内容。
- S2：`t2_list_nodes` 能列出全部分区；对任意 3 个分区各取 10 条并总结，不写死节点名。
- S3：`t2_list_latest_comments(sort=reactions)` 返回 50 条内按表态排序的评论，正文可直接用于总结，并明确标注只在当前页排序。
- S4：`t2_list_timeline` 能区分帖子与评论；未登录时返回可读错误。
- S5：对 3 页列表（≤60 篇）分批调用 `t2_collect_posts`，单批响应不超过 200 KB，失败项单独列出且不影响其余项。
- 用真实客户端（Claude Desktop / Cline / Cherry Studio 任一）连上后，能完成「列出首页 10 条 → 打开第 3 条 → 读前 2 页评论」。
- 匿名（未登录）也能读公开内容；列表返回的 `url` 能直接在浏览器打开。
- 所有出站请求在「网络调试」窗口可见，且走配置的代理。

### P2 写能力（第二优先级）

目标：Agent 能以当前登录身份发帖、回复，且可审计、可重试。

| 任务 | 产出 |
| --- | --- |
| P2-1 回复重构 | `detail.go` 抽出 `postReply`；`createReply` 改为调用它；补单测 |
| P2-2 发帖 | `mcp_tools_write.go` 的 `t2_create_post`，长度校验、payload、幂等键 |
| P2-3 回复 | `t2_reply_comment`，parent / replyTo / level 规则 |
| P2-4 写开关 | 设置页写开关 + 第二次声明 + 运行时校验 |
| P2-5 幂等 | `mcpIdempotencyKey`：规范化 JSON、SHA-256、显式覆盖 |
| P2-6 审计增强 | 写调用记录 `short_id`、结果 URL、幂等键前 8 位 |
| P2-7 失败提示 | `insufficient_scope` → 明确指出缺 `write:topic` / `write:comment`，并说明去应用内重新授权 |
| P2-8 编排（可选） | `t2_digest_feed`：列表 + 正文 + 评论一次取回，`pages` 1–3、总篇数 ≤30、300 KB 预算 |

验收：

- 写开关关闭时，两个写工具都不出现在 `tools/list` 里（不是调用时报错），减少模型误判。
- 同一参数连续调用两次、复用同一幂等键，只产生一篇帖子 / 一条评论。
- 缺少 `write:topic` 时发帖，返回可读错误且不产生半成品。
- 审计日志里能看到每次写调用的工具、结果与耗时，且没有正文。

### P3 扩展与体验

- 搜索、用户资料、合集读取（节点树已在 P1）。
- `resources` 支持。
- HTML → Markdown。
- 工具级开关、审计日志查看器、请求计数与最近错误面板。
- dry-run 预览。
- 评论摘要去重：同一作者连续刷屏时合并展示（复用 `groupComments` 的规则，但只用于输出裁剪，不改接口顺序）。

### P4 加固与发布

- 安全复核：Origin / Host / IPv6 / 慢速攻击 / 令牌时序。
- `go test -race` 跑 MCP 包；对 JSON-RPC 解析做 fuzz。
- 文档：README、CHANGELOG、Windows 防火墙放行说明（首次绑 0.0.0.0 会弹系统提示）。
- 版本标记为实验能力；发布说明写清「默认关闭、只建议可信局域网」。

## 11. 测试计划

### 11.1 单元

| 对象 | 用例 |
| --- | --- |
| `mcpConfig` | 默认值、缺字段、未知字段、损坏 JSON、0600 权限 |
| 令牌 | 生成长度与字符集、常量时间比对、重置后旧值失效 |
| 中间件 | 401 / 403 / 413 / 415 / 429、IPv4 与 IPv6 私有地址、`X-Forwarded-For` 被忽略 |
| 协议 | 合法 initialize、版本协商、未知方法、坏 JSON、批量数组、通知 202 |
| 工具 schema | 快照测试：名字、必填项、枚举、注解；防止无意改动破坏客户端 |
| DTO | 文档字段、缺字段、多余字段、`d:null`、嵌套 `post`/`topic` 两种形状；动态流帖子 / 评论分流 |
| 表态计数 | `reactionCount` 对 `reactions_summary` / `reactions` 两种键、缺 count、空数组都返回正确数值 |
| 批量聚合 | 去重保序、超过 10 个报 -32602、部分失败、200 KB 预算下的裁剪顺序（先评论后正文）、`dropped` |
| 幂等 | 参数顺序无关、显式键优先、本机单会话 |
| 审计 | 摘要不含正文、轮转、写失败不影响响应 |

### 11.2 集成

沿用现有 `httptest` 假 2Libra 的写法：

- 起假 API → 起 `mcpServer`（随机端口）→ 发真实 JSON-RPC。
- 断言出站请求的路径、query、`Authorization`、`Idempotency-Key`。
- 覆盖：列表分页、详情 404、评论树、发帖 201、回复 201、429 退避、scope 不足。
- 场景级用例（对应 1.3）：
  - S1：`hot` 列表 + `collect_posts`，断言只发出 1 次列表请求与 N 次详情请求（N ≤ 批量上限）。
  - S3：最新评论 `sort=reactions`，断言返回顺序按数值降序且 `ranked_within_page=true`。
  - S4：动态流同时含帖子与评论的响应，断言 `kind` 分流正确、`kind=comment` 时过滤掉帖子。
  - S5：三页列表分批聚合，断言每批 ≤10 篇、单批响应 ≤200 KB、某篇 404 时其余仍返回。
- 断言所有出站都经过共享 transport：复用 `proxy_test.go` 的假代理写法，加一条 MCP 用例。

### 11.3 无头界面

`ui.NewTester`：

- 设置页渲染新 tab，9 个标签不溢出。
- 开关 → 弹声明 → 取消：开关回弹、配置未写。
- 开关 → 弹声明 → 确认：`enabled=true`、服务状态变为运行中（用假 listener）。
- 写开关同样两条路径。
- 重置令牌：二次确认、旧令牌 401。

### 11.4 手工验收

- 同一局域网另一台机器用真实客户端连接，读 + 写各一次。
- Windows 防火墙拒绝后，界面能显示「启动失败 / 无法访问」而不是静默失败。
- 断网、代理关闭、API 429 三种异常下的错误文案可读。

## 12. 风险与缓解

| 风险 | 影响 | 缓解 |
| --- | --- | --- |
| 局域网内令牌被转发 | 他人可以用户身份发帖 | 默认回环；写开关；审计；令牌可一键重置；文档强调不要分享 |
| Agent 误发内容 | 个人声誉受损 | 写开关默认关；第二次声明；建议先只读试用；P3 加 dry-run |
| 设置页标签溢出 | 界面错位 | 先调宽窗口并截图验证；必要时把「通知 / MCP」合并为「高级」二级页 |
| 9 个 tab 的记忆成本 | 找不到入口 | 设置页内搜索/命令面板增加「MCP」跳转（P3） |
| 端口冲突 | 启动失败 | 明确报错、保留用户输入、提示换端口 |
| 登录态变化导致身份漂移 | 用错身份发帖 | 调用前核对当前会话；`t2_get_me` 可自检；写结果回显作者 |
| 大响应撑爆上下文 | 客户端报错 | 正文默认 3k 字（可调 8k）；评论默认 8 条；批量 10 篇 / 200 KB；超限先丢评论再截正文 |
| 批量抓取打满限流 | 正常浏览被拖慢 | 并发 2、单批 ≤10 篇、单次 ≤5 页；遇到 429 立即停止后续抓取，返回已完成部分并说明重试时间 |
| 「热门评论」被误解成全站排序 | 结论失真 | 接口只有一页 50 条；工具回 `ranked_within_page`，描述里写明「仅当前页排序」 |
| 节点缓存与 UI 缓存互相覆盖 | 节点列表错乱 | MCP 只用 10 分钟内存缓存，绝不写 `nodes.json` |
| 共享限流被 Agent 打满 | 正常浏览受影响 | 入口限流 + 共享闸门；日志可见；必要时临时关闭 MCP |
| 协议版本演进 | 客户端连不上 | 版本白名单 + 明确回退；P4 跟进最新规范 |

## 13. 后续演进

1. **stdio 适配器**：若桌面客户端只支持 stdio，可提供一个极薄的 `t2-mcp-stdio` 子命令，转发到本机 HTTP，不重复实现工具。
2. **多 Agent 接入**：多个 Agent 可同时连接，身份统一取应用当前登录会话，各自用不同的 Bearer 令牌，不引入身份漂移。
3. **MCP 授权**：跟随规范的 OAuth 演进，把 Bearer 换成标准授权流程。
4. **资源化**：帖子作为 `resource`，支持订阅更新通知（`resources/subscribe`）。
5. **局域网发现**：mDNS 广播服务名，减少手填地址；仅在同一可信网段开启。
6. **策略层**：按工具、按节点、按内容长度设置白名单，例如只允许回复不允许发帖。

## 14. 变更记录

| 日期 | 变更 |
| --- | --- |
| 2026-11-21 | 初稿：确定传输、安全模型、P0–P4 分批与验收标准 |
| 2026-11-21 | 补五个用户场景（1.3）并据此调整工具：新增最新评论、关注动态、节点树、批量聚合；`following` 从 `t2_list_posts` 拆到 `t2_list_timeline`；`t2_get_post` 支持带评论与截断 |
