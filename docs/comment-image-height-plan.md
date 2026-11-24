# 评论区图片高度上限设计

- 状态：设计待实施
- 日期：2026-11-24
- 范围：详情页评论区里的配图（独占一段的裸图、文字间的图、表格里的图）与站外视频封面卡片
- 约束：保持原图比例、不裁切、不新增网络请求与偏好项；正文与图片预览窗口保持现状

## 1. 目标

评论区的配图和正文共用 `articlePicture`：宽度按 `MaxWidthPercent(100)` 撑到评论栏，高度只由 `AspectRatio(w / h)` 推出，没有上限。一张竖图在评论栏里能比十几条评论还高，一条评论就把整个评论区占满。

要补的是一条**评论专用的图片高度上限**：

1. 评论里的配图最高 240 DIP，宽度按原比例跟着收窄，不裁掉内容。
2. 加载中、加载失败、「默认不加载图片」三种占位与成图用同一套尺寸，图到位后下方文字不跳动。
3. 正文配图、表情、多图预览窗口的现有行为一字不改；点开预览仍看完整大图。

## 2. 非目标

- 不改正文配图高度；正文是主要内容，保留现在的撑栏宽阅读体验。
- 不改多图预览窗口的缩放、翻页和「另存为」（`image_viewer.go`）。
- 不改图片缓存、下载、限流、`lazyLoad` 偏好与图床配置。
- 不改评论树、分页和 `markdown_breaks.go` 的硬换行改写。
- 不裁切图片、不改成固定缩略图；行内 `:code:` 表情仍按 1em 画，不受影响。
- 不新增偏好项、不落盘；这是评论区的排版规则，不是账号习惯。

## 3. 现状与代码位置

| 现有实现 | 位置 | 说明 |
| --- | --- | --- |
| 配图渲染 | `article_render.go:373-413`（`articlePicture`） | `MaxWidthPercent(100)` + `AspectRatio(w / h)`，无高度上限；389/391 行为成图，403 行为已知尺寸空框 |
| 加载中 / 失败占位 | `article_render.go:422-453`（`articlePicturePending`） | 426 行按真实宽高留白，428 行未知尺寸固定 72 DIP |
| 懒加载占位 | `article_render.go:486-504`（`articlePictureLazy`） | 492/494 行与上面同款尺寸 |
| 图片入口 | `article_render.go:54-110`（`articleBlockAt` / `articleBlock`） | 独占一段的媒体在 80-95 行分流；混合段在 108 行 |
| 混合段与表格 | `article_render.go:288-303`（`articleMixedInlines`）、`592-677`（`articleTable` / `articleCell` / `articleCellWidth` / `articlePictureWidth`） | 表格单元格里的配图也走 `articlePicture`，列宽按自然宽度估算（698 行） |
| 高度常量 | `article_render.go:351-359` | `imageUnknownSizePlaceholderH=72`、`imageVideoCardMaxH=380`；**后者只用在视频卡片**，图片没有对应常量 |
| 视频卡片 | `article_render.go:510-514`（`articleVideoEmbed`） | `FillWidth().AspectRatio(16/9).MaxHeight(380)`，封面 `Fit(Cover)`（533 行） |
| Markdown 选项 | `detail_ui.go:331-352`（`newMarkdownOptions`） | `Block: a.articleBlockAt(st.size, a.picturesForDoc, mentions)` |
| 评论选项 | `detail_ui.go:359-377`（`newCommentMarkdownOptions`） | 复用 `newMarkdownOptions(a, commentTextStyle, true)`，只改辅助块底色并接管引用块 |
| 排版样式 | `detail_ui.go:315-327` | `mdTextStyle{size,line,block,item}`；`postTextStyle` / `commentTextStyle` / `bioTextStyle` |
| 评论正文 | `badges_ui.go:71-94`（`commentText`） | 一律经 `markdown_breaks.go:64` 的 `markdownView(...).FillWidth()` |
| 评论栏宽度 | `detail_ui.go:1031` | `ui.Column(c).Grow(1)`，初始约占窗口六成；图片撑满这一栏就会很高 |
| 现有尺寸断言 | `article_render_test.go:372-416`（`TestArticlePictureLoadingKeepsLayout`） | 用 `tt.Access("配图").Bounds` 断言占位高度，可照搬到评论 |

结论：**没有评论专用的图片尺寸代码**。正文与评论的差别只有字号（16 / 15）与 `@` 是否可点，两者都不参与图片布局。因此改动点必须放在这条共享链路里，用参数区分上下文。

## 4. 已确认决策

| 编号 | 决策 | 结论 |
| --- | --- | --- |
| 1 | 上限值 | `commentImageMaxH = 240` DIP，约合 10 行评论文字（15 × 1.65 ≈ 25 DIP/行）；单一常量，便于后续调 |
| 2 | 作用范围 | 只作用于评论；正文与资料卡简介的 `imageMaxH = 0`，表示沿用现有逻辑 |
| 3 | 缩放方式 | 保比例收窄：宽度取 `min(栏宽, maxH × w / h)`，高度由比例推出；不用 `MaxHeight` 直接压扁，也不用 `Fit(Cover)` 裁切 |
| 4 | 参数载体 | 在 `mdTextStyle` 加 `imageMaxH float32`，随 `newMarkdownOptions` 传进 Block 链，不在 `app` 上存全局开关 |
| 5 | 占位一致性 | 成图、已知尺寸空框、加载中 / 失败、懒加载占位都过同一个宽度计算，避免加载前后跳变 |
| 6 | 视频卡片 | 评论区一并降到同一个上限；正文仍是 `imageVideoCardMaxH=380` |
| 7 | 表格列宽 | `articlePictureWidth` 同步按上限估算，避免评论里表格列宽虚高 |
| 8 | 预览行为 | 点击仍打开原有图组预览，预览里看原图；上限只影响行内显示 |
| 9 | 持久化 | 不写文件、不进设置页；换帖、重登都不需要清理状态 |
| 10 | 窄窗口 | 上限是 DIP 硬值；栏宽本身比 `maxH × w/h` 窄时仍按 100% 栏宽显示，不会把图撑出评论栏 |

## 5. 设计

### 5.1 常量与样式字段

在 `article_render.go:351-359` 的常量块里加入：

```go
// commentImageMaxH 是评论里配图与视频卡片的高度上限：再高的竖图也要
// 收窄到这个高度，不能让一条评论占满整个评论区。正文不设上限。
commentImageMaxH float32 = 240
```

同时把 `imageVideoCardMaxH` 的注释改成「正文里」的视频卡片上限，避免两处混淆。

在 `detail_ui.go:315-327` 给 `mdTextStyle` 加一个字段：

```go
type mdTextStyle struct {
    size, line, block, item float32
    imageMaxH               float32 // 0 表示不限制图片高度（正文、简介）
}

var (
    postTextStyle    = mdTextStyle{size: postBodyFontSize, line: 1.7, block: 0.85, item: 0.35}
    commentTextStyle = mdTextStyle{size: commentFontSize, line: 1.65, block: 0.7, item: 0.3,
        imageMaxH: commentImageMaxH}
    bioTextStyle = mdTextStyle{size: userCardBioFontSize, line: 1.6, block: 0.6}
)
```

字号缩放（见 `docs/typography-plan.md`）如果后续落地，可在 `newMarkdownOptions` 里把 `st.imageMaxH` 按 `size / commentFontSize` 缩放；本次保持 DIP 硬值，不引入 em 换算。

### 5.2 参数传递链路

`articlePicture` 在 `article_render.go` 内部被调用，外部调用点只在 `articleBlockAt` 链上，因此按现有风格逐层加一个 `imageMaxH float32` 参数即可：

```
detail_ui.go:331 newMarkdownOptions(a, st, mentions)
  └─ article_render.go:54  articleBlockAt(st.size, st.imageMaxH, picturesForDoc, mentions)
       └─ article_render.go:63 articleBlock(..., imageMaxH, ...)
            ├─ articlePicture(..., imageMaxH)            // 独占裸图
            ├─ articlePicturePending(..., imageMaxH)
            ├─ articlePictureLazy(..., imageMaxH)
            ├─ articleVideoEmbed(..., imageMaxH)
            ├─ articleMixedParagraph → articleMixedInlines → articlePicture
            └─ articleTable → articleCell → articleMixedInlines → articlePicture
                           └─ articleCellWidth → articlePictureWidth
```

逐点清单：

| 函数 | 位置 | 改动 |
| --- | --- | --- |
| `newMarkdownOptions` | `detail_ui.go:331` | `articleBlockAt(st.size, st.imageMaxH, a.picturesForDoc, mentions)` |
| `articleBlockAt` / `articleBlock` | `article_render.go:54` / `63` | 新增参数，转发给下面四个分支 |
| `articleMixedParagraph` / `articleMixedInlines` | `article_render.go:277` / `288` | 新增参数，传到 `articlePicture` |
| `articleTable` / `articleCell` / `articleCellWidth` / `articlePictureWidth` | `article_render.go:592` / `670` / `681` / `698` | 新增参数，画图和列宽估算都带上 |
| `articlePicture` / `articlePicturePending` / `articlePictureLazy` / `articleVideoEmbed` | `article_render.go:373` / `422` / `486` / `510` | 新增参数，按下节计算上限 |

`newCommentMarkdownOptions` 的引用块包装（`detail_ui.go:367-376`）只是把 `o.Block` 再包一层，`imageMaxH` 已经烘进 `articleBlockAt`，不需要改。`main.go:783/786` 的正文与简介选项也不用改：`imageMaxH` 为 0，行为与现在完全一致。

### 5.3 尺寸计算（核心）

加一个纯函数，便于单测：

```go
// imageBoxWidth 返回图片为不越过 imageMaxH 而需要的最大宽度。
// 尺寸未知或不设上限时返回 0，调用方就不加 MaxWidth。
func imageBoxWidth(w, h, maxH float32) float32 {
    if w <= 0 || h <= 0 || maxH <= 0 {
        return 0
    }
    return maxH * w / h
}
```

成图（`article_render.go:388-392`）：

```go
img := ui.Image(c, src).MaxWidthPercent(100).Radius(4).Label(alt)
if w > 0 && h > 0 {
    img.AspectRatio(w / h)
    if maxW := imageBoxWidth(w, h, imageMaxH); maxW > 0 {
        img.MaxWidth(maxW)
    }
}
```

mygo v0.10.0 的语义已经核对过，无需额外裁剪容器：

- `ui/element.go:63-99`：`MaxWidth` 与 `MaxWidthPercent` 同时设置时取**两者中较小**的（`max-width: min(78%, 34rem)` 语义）。
- `ui/layout.go:39-48`：`clampW` 应用上限；`ui/layout.go:510-518`：高度由 `w / aspect` 推出。
- 因此宽度 ≤ `min(栏宽, maxH × w / h)`，高度 ≤ `maxH`，比例严格保持；`ui.Image` 默认 `Contain`，不会拉伸或裁掉内容。

**不要用 `MaxHeight` 代替**：`ui/layout.go:504-508` 是先算自然高度再 clamp，宽度仍按栏宽算，结果会得到一个「宽而矮」的盒子，默认 `Contain` 下图片被缩小居中，反而更小。按宽度反向限制才能得到「最高 240、其余按比例」的直觉结果。

### 5.4 占位与未知尺寸

三处占位与成图共用 `imageBoxWidth`：

- `articlePicture:403` 的已知尺寸空框：`Width(w).MaxWidthPercent(100).AspectRatio(w / h)` 之后，若上限有效再 `.MaxWidth(imageBoxWidth(w, h, imageMaxH))`。
- `articlePicturePending:426`：同款处理；加载中转圈与失败说明的盒子继续居中，不改文案。
- `articlePictureLazy:492`：同款处理。
- 未知尺寸（`articlePicturePending:428`、`articlePictureLazy:494`）仍是 `FillWidth().Height(72)`，72 < 240，不需要改；若将来把上限调到 72 以下，再取 `min(72, maxH)`。

这样图到达前后盒子边界一致，`TestArticlePictureLoadingKeepsLayout` 的「下方文字不移动」断言在评论区也成立。

### 5.5 视频卡片

`articleVideoEmbed:511` 的上限改为按上下文取值：

```go
limit := imageVideoCardMaxH
if imageMaxH > 0 {
    limit = min(limit, imageMaxH)
}
card := ui.Box(c).FillWidth().AspectRatio(16.0 / 9).MaxHeight(limit)...
```

正文 `imageMaxH=0`，仍是 380；评论最多 240。封面本来就 `Fit(ui.Cover)`，变矮只是多裁一点边缘，不涉及新逻辑。

### 5.6 表格里的图

`articlePictureWidth`（`article_render.go:698-708`）现在只读自然宽度，需要同时读高度再套上限：

```go
// 已知尺寸：min(w, maxW)；评论里再收窄到不超过 maxH 对应的宽度。
if w, h, ok := a.images.cachedSize(dest, imageCacheTemporary); ok && w > 0 {
    if max := imageBoxWidth(w, h, imageMaxH); max > 0 {
        w = min(w, max)
    }
    return min(w, maxW)
}
```

`a.images.source(dest)` 分支同理（`ui.ImageSize` 已返回宽高）。这样评论里表格单元格不会按原图的宽预留一列，渲染出来的图又窄一截。`articleCellWidth` 把 `imageMaxH` 传下来即可。

### 5.7 行为不变项

- 点图仍走 `openImageGroup` / `wantImageGroup` / `revealImage`，预览看原图。
- 表情仍走 `articleRenderer.addImage` 的 1em 行内对象，不经过 `articlePicture`。
- `media.go` 的识别、`articlePictures` 的收图顺序、缓存分区与限流都不动。
- 正文、附言、简介走 `postTextStyle` / `bioTextStyle`，`imageMaxH=0`，渲染逐像素不变。

## 6. 代码改动清单

| 文件 | 改动 |
| --- | --- |
| `article_render.go` | 新常量 `commentImageMaxH`、`imageBoxWidth`；六个渲染函数与四个中转函数加参数；成图 / 三种占位 / 视频卡片应用上限；表格列宽同步 |
| `detail_ui.go` | `mdTextStyle.imageMaxH`；`commentTextStyle` 设为 240；`newMarkdownOptions` 把上限传进 `articleBlockAt` |
| `article_render_test.go` / 新增 `comment_image_height_test.go` | 纯函数单测 + `ui.NewTester` 的评论 / 正文对照断言 |
| `article_table_image_test.go` | 评论表格列宽不虚高的回归断言 |
| `detail_test.go` | 正文配图高度不变、正文视频卡片仍是 380 的守门断言 |

## 7. 验收条件

### 7.1 单元测试

- [ ] `TestImageBoxWidth`：`800×400 / 240 → 480`；`400×1200 / 240 → 80`；`maxH=0`、宽或高为 0 时返回 0。
- [ ] `TestCommentImageStyleLimit`：`commentTextStyle.imageMaxH == 240`，`postTextStyle` 与 `bioTextStyle` 为 0。
- [ ] `TestArticlePictureWidthRespectsLimit`：缓存里有 `400×1200` 的图时，评论上限下表格列宽按 80 而不是 400 算。

### 7.2 UI 测试（`ui.NewTester`，按 `AGENTS.md` 约定不用截图）

- [ ] 评论里放一张 `400×1200` 的竖图，`tt.Access("配图").Bounds.H <= 240`，宽度约 80，绿色像素包围盒比例仍约为 1:3（没裁切、没变形）。
- [ ] 评论里放一张 `800×400` 的横图，高度 240、宽度 480；同一张图放在正文里高度仍是按栏宽算出的值（不受上限影响）。
- [ ] 已知尺寸的加载中占位与成图 `Bounds` 一致；图到达后「结尾段落」的 `Y` 不移动。
- [ ] 未知尺寸的懒加载占位高度仍是 72。
- [ ] 评论里的视频卡片高度 ≤ 240，正文里的视频卡片 ≤ 380。
- [ ] 表格单元格里的配图高度 ≤ 240，列宽与图宽一致，不出现大片空白。
- [ ] 点击评论里的图仍能打开图组预览（沿用现有点击断言）。
- [ ] 独占一段的裸图、文字中间的图、表格里的图三种入口都命中上限。

### 7.3 回归

- [ ] `TestArticlePictureLoadingKeepsLayout` 等正文图片测试不变。
- [ ] `TestCommentMarkdownRenders` 等评论排版测试不变。
- [ ] 窄窗口（720×480）下评论图不横向溢出评论栏。
- [ ] `CGO_ENABLED=0 go test ./...` 通过。
- [ ] `go tool mygo vet .` 无告警。
- [ ] `gofmt -l .` 无输出。

## 8. 风险与回退

| 风险 | 处理 |
| --- | --- |
| 极端竖图收窄到几十像素，看着像没显示全 | 保比例不裁切；点开预览看原图；上限是单一常量，可按反馈调大 |
| 参数漏传给某个调用点，出现「有的图限高、有的不限」 | 按 5.2 清单逐项核对；验收覆盖裸图 / 混合段 / 表格 / 视频四种入口 |
| 占位与成图尺寸不一致，图到达时文字跳动 | 三处占位与成图共用 `imageBoxWidth`；UI 测试断言 `Bounds` 与 `Y` 不变 |
| 表格列宽按原图预留，图实际变窄留白 | `articlePictureWidth` 同步套上限 |
| 正文被误改 | 正文与简介的 `imageMaxH=0`；正文配图 / 视频卡片高度单独守门 |
| 与后续字号缩放叠加后视觉不协调 | 上限独立于字号，先按 DIP 落地；字号方案落地时再决定要不要按 `size` 缩放 |
