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

## 提交前验证

- `CGO_ENABLED=0 go test ./...`
- `go tool mygo vet .`
- `gofmt -l .` 不应输出任何文件。
- 改 UI 时用 `ui.NewTester` 复现：它的无头渲染与真实窗口一致，可用
  `Find` / `Texts` / `Image` 断言布局与像素。
