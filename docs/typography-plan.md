# 阅读排版可调设计（字号 / 行距 / 页宽 / 列表密度）

- 状态：已实施（含档位之外的 lobeui 精确数值输入）
- 日期：2026-11-24
- 范围：详情正文与评论的字号、行距，列表页正文栏宽度，列表行密度，设置页「阅读与评论」内的排版块
- 约束：进程级共享、立即生效、默认值与现状逐像素一致；不改系统 UI 字号

## 1. 目标

详情页字号是写死的常量（`detail_ui.go:263-272`），不同 DPI、不同视力、不同阅读距离下没有调整余地。
要补的是四组可调项，并且**默认档位与现在的渲染完全一致**，升级后首帧不变：

1. 正文字号：正文与评论一起缩放，保持现有阶梯比例。
2. 行距：正文与评论的 Markdown 行高倍数。
3. 页宽：列表类页面正文栏的最大宽度（现在是固定 760）。
4. 列表密度：列表行的上下留白与行内间距。

## 2. 非目标

- 不改标题栏、侧边栏、按钮、设置页等系统 UI 的字号。
- 不改发帖 / 评论编辑器的输入字号；编辑是输入场景，不是阅读场景。
- 不做字体族切换与字重调整；字体方案见 `fonts/` 与 `font.go`，只有 Regular + Bold。
- 不做窗口整体缩放；高 DPI 由操作系统处理。
- 阅读偏好只在本机保存一份，进程内共用。
- 不影响资料卡「个人简介」的字号（仍用 `userCardBioFontSize`）。

## 3. 现状与代码位置

| 现有实现 | 位置 | 说明 |
| --- | --- | --- |
| 详情页字号阶梯 | `detail_ui.go:263-272` | `postTitleFontSize=26`、`postBodyFontSize=16`、`postAuthorFontSize=14`、`commentFontSize=15`、`sectionTitleFontSize=16`、`metaFontSize=13`、`metaSmallFontSize=12`、`noticeFontSize=12` |
| Markdown 排版 | `detail_ui.go:315-327` | `mdTextStyle{size,line,block,item}`；`postTextStyle`、`commentTextStyle`、`bioTextStyle` 三个全局 var |
| Markdown 选项缓存 | `main.go:360-369` | `a.md`、`a.commentMd`、`a.commentMdLight`、`a.bioMd` 启动时建好；带 `sync.Map`，不能复制 |
| 页宽 | `main.go:1619`（`feedMaxWidth=760`）、`main.go:2281-2289`（`feedInset`） | 窗口宽于 760 时把多余空间等分到两侧 |
| 列表行密度 | `main.go:2520`（`PaddingY(10).PaddingX(12)`）、行内 `Gap(4)` | 帖子行、合并行、通知行、稍后再看行同类 |
| 设置页 | `settings_ui.go:34-42`、`settings_ui.go:503-522` | 「阅读与评论」页现有「评论」与「阅读记录」两块 |
| 进程级偏好模式 | `image_prefs.go:17-81` | 落一个 JSON，读写方法与 Reset 都可照搬 |
| 像素断言 | `detail_test.go:632-635` | 断言标题高度，说明字号改动会被测试捕捉 |

字号常量在非测试代码里共 **76 处**引用：`metaFontSize=34`、`metaSmallFontSize=20`、
`commentFontSize=13`、`postAuthorFontSize=3`、`sectionTitleFontSize=2`、`postBodyFontSize=2`、
`postTitleFontSize=1`、`noticeFontSize=1`。这是本设计里主要的改动面。

## 4. 已确认决策

| 编号 | 决策 | 结论 |
| --- | --- | --- |
| 1 | 存储 | `<配置目录>/typography.json`，进程级一份（`appDeps`），照 `image_prefs.go` 的写法 |
| 2 | 默认值 | 16 / 1.7 / 760 / 标准；与现状完全一致 |
| 3 | 字号档位 | 小 14、标准 16、大 18、特大 20；按比例缩放整套阶梯，不改相对关系 |
| 4 | 行距档位 | 紧凑 1.5、标准 1.7、宽松 1.9；只作用于正文与评论的 Markdown，不改列表 |
| 5 | 页宽档位 | 窄 640、标准 760、宽 880；只作用于列表类内嵌页，不影响独立详情窗口的双栏比例 |
| 6 | 密度档位 | 紧凑 / 标准 / 宽松；行 `PaddingY` 6 / 10 / 14，行内 `Gap` 2 / 4 / 6 |
| 7 | 列表字号 | **不跟随**正文字号；列表是扫描场景，调大字会让一屏信息量骤降 |
| 8 | 生效方式 | 立即生效：改完立刻落盘、重建 Markdown 选项、重画所有窗口 |
| 9 | 设置位置 | 「阅读与评论」页新增「阅读排版」块，不新开 tab |
| 10 | 恢复默认 | 块内提供「恢复默认排版」，只重置四项，不动评论开关与阅读记录 |

## 5. 设计

### 5.1 偏好存储

新增 `typography.go`：

```go
type typographyConfig struct {
    BodySize   int `json:"body_size"`   // 14 / 16 / 18 / 20
    LineHeight int `json:"line_height"` // 15 / 17 / 19，除 10 得到 1.5 / 1.7 / 1.9
    PageWidth  int `json:"page_width"`  // 640 / 760 / 880
    Density    int `json:"density"`     // 0 紧凑 / 1 标准 / 2 宽松
}

type typographyStore struct {
    dir string
    p   typographyConfig
}
```

- `newTypographyStore(dir)` 读文件；缺字段、越界值一律回落到默认，不因为一个坏值让整个偏好失效。
- `save()` 用 `writeJSONFile` 原子写；失败只把错误交给调用方提示，内存保留新值（与 `imagePrefsStore.save` 一致）。
- `Reset()` 恢复默认并落盘。
- 只读访问器：`bodySize()`、`lineHeight()`、`pageWidth()`、`density()`。
- 加进 `appDeps`（`main.go:599-627`）与 `newAppDeps`（`main.go:635-651`），进程内共用一份。

### 5.2 字号阶梯

保留现有常量作为「标准档」的基准，新增计算入口：

```go
var typeBase = struct {
    title, body, author, comment, section, meta, metaSmall, notice float32
}{26, 16, 14, 15, 16, 13, 12, 12}

type typeSizes struct {
    title, body, author, comment, section, meta, metaSmall, notice float32
}

func (a *app) typeSizes() typeSizes {
    scale := float32(a.typePrefs.bodySize()) / typeBase.body
    ...
}
```

规则：

- `scale = bodySize / 16`。
- 每一项等于基准乘以 `scale`，四舍五入到 0.5 DIP，避免出现 13.333 这种值。
- 现有常量改名或保留为 `typeBase` 字段；**76 处引用逐项替换**为 `ts := a.typeSizes()` 后的字段。
- 每帧构建时读一次 `ts`，不要在循环里反复调用。

> 实现时用 `git grep -n 'postTitleFontSize|postBodyFontSize|postAuthorFontSize|commentFontSize|sectionTitleFontSize|metaFontSize|metaSmallFontSize|noticeFontSize'`
> 建一份清单，逐项勾掉；清单不进文档，避免行号漂移。

### 5.3 Markdown 选项

`mdTextStyle` 从全局 var 改为按偏好计算：

```go
func (a *app) mdStyles() (post, comment, bio mdTextStyle) {
    ts := a.typeSizes()
    line := float32(a.typePrefs.lineHeight()) / 10
    post = mdTextStyle{size: ts.body, line: line, block: 0.85, item: 0.35}
    comment = mdTextStyle{size: ts.comment, line: line - 0.05, block: 0.7, item: 0.3}
    bio = bioTextStyle // 不参与缩放
    return
}
```

- `newMarkdownOptions` 已经接收 `mdTextStyle`，不用改签名。
- `newCommentMarkdownOptions` 现在读全局 `commentTextStyle`，改成接收 `st mdTextStyle`。
- `a.md`、`a.commentMd`、`a.commentMdLight`、`a.bioMd` 改为在 `rebuildMarkdownViews()` 里创建；
  偏好变化时调用它重建，再 `a.invalidateReadingWindows()`。
- `rebuildMarkdownViews` 必须在 UI 线程调用；`markdown.Options` 带 `sync.Map`，只整体替换指针，不原地改。
- 重建后已渲染的 Markdown 视图要能刷新：现有详情窗口每次 `view` 都读 `a.md` / `a.commentMd`，
  窗口 `Invalidate` 后下一帧自然用新选项。

### 5.4 页宽

把 `feedInset(c)` 改成方法：

```go
func (a *app) feedInset(c *ui.Context) float32 {
    width := float32(a.typePrefs.pageWidth())
    inset := float32(24)
    if w, _ := c.Size(); w > 0 {
        if avail := w - navSidebarWidth - 1; avail > width {
            inset = (avail - width) / 2
        }
    }
    return inset
}
```

- 调用点：`main.go:2297`、`main.go:2454`、`main.go:2480`，以及 `watch_later_ui.go`、
  `collection_browse.go`、`scores_ui.go`、`draft_ui.go` 里的同类页面。
- 通知窗口、设置窗口、金币窗口、发帖窗口的固定宽度不受影响。
- 独立详情窗口与主窗口内嵌详情是双栏布局，正文栏宽度由 `bodyWidth` 控制，**不叠加**页宽偏好；
  这里明确不改，避免「页宽 + 双栏比例」互相打架。
- `feedMaxWidth` 常量保留，作为默认值来源。

### 5.5 列表密度与精确输入

只改列表行的上下留白与行内间距，不改字号：

| 档位 | 行 `PaddingY` | 行内 `Gap` |
| --- | --- | --- |
| 紧凑 | 6 | 2 |
| 标准 | 10 | 4 |
| 宽松 | 14 | 6 |

- `main.go:2520` 的 `feedRowSurface(c).PaddingY(10)` 改为 `a.rowPadY()`。
- 行内 `ui.Column(c).Gap(4)` 改为 `a.rowGap()`。
- `foldRowView`（合并行）、通知行、稍后再看行、阅读历史行同样读这两个值。
- 横向 `PaddingX(12)` 不变；页宽负责横向，密度只负责纵向。

### 5.6 设置界面

「阅读与评论」页顶部新增 `readingTypographySection`：

- 标题「阅读排版」。
- 四行，每行左侧标签 + 右侧分段选择：
  - 正文字号：小 / 标准 / 大 / 特大
  - 行距：紧凑 / 标准 / 宽松
  - 页宽：窄 / 标准 / 宽
  - 列表密度：紧凑 / 标准 / 宽松
- 每改一项：`save()` → 成功 toast「已应用阅读排版」→ `rebuildMarkdownViews()` →
  `invalidateReadingWindows()`；失败 toast 带原因，界面停在旧值。
- 底部一行「恢复默认排版」按钮，二次确认复用 `settings_reset.go` 的确认弹窗样式，
  确认后 `Reset()` 并重画。
- 分段选择用现有组件（`segmented_test.go` 覆盖的那套），不新增下拉。
- 档位下方再给每组一行 lobeui `field.InputNumber`：正文字号 12～28 DIP、
  行距 1.2～2.4 倍、页宽 560～1200 DIP、行留白 4～20 DIP、行内间距 1～12 DIP。
  数值与档位共用同一份配置，输入非预设值时不选中任何档位；输入框边界由
  lobeui 夹紧，写盘前再校验一次。行留白与行内间距取代原「密度档位」枚举，
  档位按钮只是两组数值的快捷预设。

`invalidateReadingWindows`：

- 主窗口 + 所有详情窗口，以及打开着的「我的」/ 用户资料卡所在窗口；
- 按 `app_window.go` 里现有的窗口遍历方式实现；没有统一函数就在 `app_window.go` 加一个，
  遍历主窗口与 `a.detailWindows` 等附属窗口逐个 `Invalidate()`。
- 不改状态、不重建页面；只让下一帧按新字号重画。

## 6. 代码改动清单

| 文件 | 改动 |
| --- | --- |
| `typography.go`（新增） | 配置结构、store、档位与默认值、`Reset` |
| `typography_test.go`（新增） | 存储、越界回退、字号换算 |
| `main.go` | `appDeps` 加 `typePrefs`；`feedInset` 改方法；`rebuildMarkdownViews`；`invalidateReadingWindows`；调用点更新 |
| `detail_ui.go` | 字号常量改为 `typeBase` + `typeSizes()`；76 处引用替换；`mdStyles()` |
| `rows.go` / `main.go` / `watch_later_ui.go` / `collection_browse.go` / `scores_ui.go` / `draft_ui.go` | `feedInset` 调用改成 `a.feedInset`；列表密度取值 |
| `settings_ui.go` | 「阅读与评论」页加 `readingTypographySection` |
| `settings_reset.go` | 「恢复所有设置」里一并 `typePrefs.Reset()` |
| `detail_test.go` | 像素断言按默认档位保留；新增档位切换测试 |

## 7. 验收条件

### 7.1 单元测试

- [ ] `TestTypographyDefaults`：新目录读到 16 / 1.7 / 760 / 标准。
- [ ] `TestTypographyPersist`：改四项后重新构造 store，值一致。
- [ ] `TestTypographyInvalidFallsBack`：`body_size=99`、`line_height=0`、`page_width=-1`、`density=9`
      全部回落到默认，其它合法项保留。
- [ ] `TestTypographyReset`：`Reset()` 后四项回默认且已落盘。
- [ ] `TestTypeSizesScaling`：14 / 16 / 18 / 20 四档下
      `title > body > comment > meta > metaSmall` 恒成立，且标准档等于现有常量。
- [ ] `TestMdStylesLineHeight`：行距 1.5 / 1.7 / 1.9 分别映射到 `postTextStyle.line`，
      评论行距仍比正文略紧。

### 7.2 UI 测试（`ui.NewTester`）

- [ ] 「阅读与评论」页出现「阅读排版」「正文字号」「行距」「页宽」「列表密度」「恢复默认排版」。
- [ ] 默认选中「标准」；点「大」后详情页正文 `Text.H` 明显大于改前。
- [ ] 同一窗口宽度下，页宽「窄」比「标准」的行左内边距更大，「宽」更小。
- [ ] 密度「紧凑」的行高小于「宽松」。
- [ ] 「恢复默认排版」后四项回到标准，正文高度回到改前值。
- [ ] 设置页切到别的 tab 再切回来，四项仍显示已保存的值。

### 7.3 回归

- [ ] 默认档位下现有像素测试全部通过，不需要改断言
      （特别是 `detail_test.go:632-635` 与 `page_width_test.go`）。
- [ ] 发帖编辑器、评论输入框、设置页自身的字号不随偏好变化。
- [ ] 主窗口与附属窗口同时打开时，改一次四项，各窗口都变。
- [ ] 重启应用后四项保留。
- [ ] 独立详情窗口在 720×480 最小尺寸、特大字号下正文不横向溢出；评论栏仍可用。
- [ ] `CGO_ENABLED=0 go test ./...` 通过。
- [ ] `go tool mygo vet .` 无告警。
- [ ] `gofmt -l .` 无输出。

## 8. 风险与回退

| 风险 | 处理 |
| --- | --- |
| 76 处字号引用漏改，出现「某块不缩放」 | 建清单逐项替换；验收里用 4 档正文高度 + 截图目测两块区域 |
| Markdown 选项重建时机错误，读到半成品 | 只在 UI 线程 `rebuildMarkdownViews`；指针整体替换，不原地改 |
| 特大字号 + 窄窗口导致双栏挤压 | 字号上限 20；720×480 单独验收；必要时评论栏最小宽度提高并允许切换为上下布局 |
| 页宽与详情双栏互相干扰 | 页宽只作用于列表类内嵌页，详情页明确不叠加 |
| 默认值漂移导致现有像素测试红 | 标准档必须严格等于现有常量；先跑测试再改档位 |
| 偏好文件损坏导致间距异常 | 每个字段单独校验，坏字段回默认，不整份丢弃 |