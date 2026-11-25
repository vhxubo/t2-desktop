# AGENTS.md

## 提交与文案

- 提交信息只用英文，遵循 Conventional Commits：`type(scope): subject`（scope 可省略），类型用 `feat`、`fix`、`refactor`、`test`、`docs`、`chore`。标题用祈使句，约 72 字符以内，不加句号；正文可原样引用代码里的中文字符串。不写工具或代理署名，不同功能分开提交。
- 注释、日志和界面文案保持中文、简短；改文案同步更新测试。除非明确要求，不新增 Tooltip。

## 原生 UI

- 界面全部用 mygo 的原生 `ui`，不使用 WebView。改 UI 前按 [mygo 维护技能](.agents/skills/mygo-maintenance/SKILL.md) 和 [官方技能说明](https://mygo.lobehub.com/skills.md) 核对锁定版本与 API。
- 不在状态中保存 `ui.Element` 或 `*ui.Context`；后台结果经 `Window.Update` / `app.publish` 发回 UI 线程。
- UI 以当前代码、mygo 与 2Libra 文档、`ui.NewTester` 断言为准。不要拿截图或文档图片当参考，也不要重建截图目录或给文档添加截图。
- 用户头像统一用 `avatarView` / `avatarWithPro`。无图时用 `username` 生成确定性图案，不能用昵称或首字母替代。

## 账号与授权

- `AuthManager` 管唯一的账号会话，全局同时只允许一次授权；`app` 管主窗口及附属窗口状态，关窗不退账号。代理、外观、图片和离线缓存经 `appDeps` 进程内共享。
- 访问和刷新令牌作为单条记录存入 `mygo.Secrets`；不要扩大 `secureTokenStore` 的明文回退范围，仅凭据库不支持或数据超过 `mygo.MaxSecretSize` 时允许回退。
- 登录、重新登录和补充授权统一走系统浏览器 + 深链回调，经 `handleDeepLink` / `AuthManager.Complete` 完成。未登录时主窗口直接显示登录页，不再另开登录窗口；深链回调只落到这一个会话。

## 网络与限流

- 所有应用内 HTTP(S) 遵循 `proxy.json`（含直连和绕过规则）。请求走 `Client.httpClient()`、`AuthManager.http` 或 `Client.streamHTTPClient()`，不另建客户端或绕过共享 transport。系统浏览器外链由浏览器自己处理代理。
- 插件和更新器走 `installGlobalProxy` 接管的 `http.DefaultTransport` / mygo `netproxy.Transport()`，否则显式注入代理客户端；切换代理时要关闭旧的空闲连接。
- 2Libra JSON 请求统一走 `sendJSON` 的共享 `rateLimitGate`：发请求前查闸门，每个响应都更新闸门，`Remaining` 为零时等到 `Reset`；429 或 `rate_limited` 优先按 `Retry-After` 退避，其次 `Reset`，再用错误体 `retry_after`。每帧调用的 `ensure*` 在普通失败后也要冷却。SSE、图床和 multipart 上传保留各自的退避或配额规则。

## 2Libra API

- 加端点或改字段前，读 [开放 API v1](https://r2.2libra.com/openapi/v1/index.md) 的对应分册：[授权](https://r2.2libra.com/openapi/v1/oauth.md)、[读取](https://r2.2libra.com/openapi/v1/api-read.md)、[写入](https://r2.2libra.com/openapi/v1/api-write.md)、[经济](https://r2.2libra.com/openapi/v1/api-economy.md)。新增能力还要读[接入条款](https://r2.2libra.com/openapi/v1/terms.md)。
- 路径、参数和字段名照文档原样使用；列表按端点取字段（见 `tabSpec.ListKey`），容错解码并忽略未知字段。文档与实测不符时，以实测为准并注明原因。
- JSON 响应从 `{c,m,d,t}` 解包，访问令牌在 `d.access_token`；通知 SSE 例外，只用 `Authorization` 头。错误用 HTTP 状态码和 `code` 判断，不解析中文 `m`；`insufficient_scope` 交给 `auth_gate.go` 补授权并重跑。
- 只申请必要的 scope；默认能力所需的 scope 加到 `defaultScopes`，需开通的端点在应用申请时说明用途。标 🔑 的写端点必须带 `Idempotency-Key`，超时重试复用，内容改变则换键。
- 发帖 `restricted_*` 枚举和校验参考 [2Libra 发帖规则](2libra-发帖规则.md) 与官网发帖页，不从中文文案推断。新端点先实现路径与解码，补 HTTP 测试，再接 UI。

## 媒体预览

- `media.go` 统一识别媒体；只有独占一段的媒体链接才能嵌入，未知类型和行内链接仍按普通链接处理。新增类型先补识别测试，保证 `articlePictures` 按源文顺序收组，与预览渲染一一对应。
- 视频在预览里经 `openExternal` 用系统浏览器播放，不新增 WebView 或直连流。动图沿用 `image_viewer.go` 的纯 Go 路径，动画 WebP 只显示首帧。配图懒加载只影响正文和评论，不影响头像、行内表情和徽章；图片偏好保持进程级共享。

## 检查与发版

- 按 [CHECKS.md](CHECKS.md) 做提交前验证和发版门禁。发版操作见 [RELEASE.md](RELEASE.md)；代理只做本地准备，触发公开发布的远程推送须由人工执行。
