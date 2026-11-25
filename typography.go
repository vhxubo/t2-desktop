package main

import (
	"fmt"
	"math"
	"path/filepath"
)

// 阅读排版偏好：正文 / 评论的字号与行距、列表页的正文栏宽度、列表行密度。
// 四项都是进程级的一份（见 appDeps），多账号窗口共用；默认档位与改造前
// 逐像素一致，升级后的首帧不变。系统 UI 字号、编辑器输入字号与资料卡
// 「个人简介」都不在这里。
//
// 每组设置都存实际数值，档位只是常用数值的快捷按钮：设置页同时提供 lobeui
// 数字输入框做精确控制，输入非预设值时档位按钮不再高亮。
const typographyFileName = "typography.json"

// 档位预设。字号用 DIP，行距用十分之一倍（17 表示 1.7），页宽用 DIP，
// 密度由行上下留白与行内间距两个 DIP 值组成。
var (
	typographyBodySizes    = []int{14, 16, 18, 20}
	typographyLineHeights  = []int{15, 17, 19}
	typographyPageWidths   = []int{640, 760, 880}
	typographyBodyOptions  = []string{"小", "标准", "大", "特大"}
	typographyLineOptions  = []string{"紧凑", "标准", "宽松"}
	typographyWidthOptions = []string{"窄", "标准", "宽"}
	typographyDensityNames = []string{"紧凑", "标准", "宽松"}
	typographyDensityPads  = []int{6, 10, 14}
	typographyDensityGaps  = []int{2, 4, 6}
)

// 精确数值的合法区间。比档位宽，允许在设置页里逐 DIP / 逐 0.1 倍微调。
const (
	typographyBodyMin  = 12
	typographyBodyMax  = 28
	typographyLineMin  = 12
	typographyLineMax  = 24
	typographyWidthMin = 560
	typographyWidthMax = 1200
	typographyPadMin   = 4
	typographyPadMax   = 20
	typographyGapMin   = 1
	typographyGapMax   = 12

	typographyDensityCompact  = 0
	typographyDensityStandard = 1
	typographyDensityLoose    = 2
)

type typographyConfig struct {
	BodySize   int `json:"body_size"`   // 12～28 DIP
	LineHeight int `json:"line_height"` // 12～24，十分之一倍
	PageWidth  int `json:"page_width"`  // 560～1200 DIP
	RowPadY    int `json:"row_pad_y"`   // 4～20 DIP
	RowGap     int `json:"row_gap"`     // 1～12 DIP
}

type typographyStore struct {
	dir string
	p   typographyConfig
}

func defaultTypographyConfig() typographyConfig {
	return typographyConfig{
		BodySize:   16,
		LineHeight: 17,
		PageWidth:  int(feedMaxWidth),
		RowPadY:    10,
		RowGap:     4,
	}
}

// normalizeTypographyConfig 逐项校验：缺字段、越界值只让那一项回默认，
// 不会把整份偏好丢掉。档位写入的数一定在范围内，精确输入可能越界。
func normalizeTypographyConfig(cfg typographyConfig) typographyConfig {
	def := defaultTypographyConfig()
	check := func(v, lo, hi, fallback int) int {
		if v < lo || v > hi {
			return fallback
		}
		return v
	}
	cfg.BodySize = check(cfg.BodySize, typographyBodyMin, typographyBodyMax, def.BodySize)
	cfg.LineHeight = check(cfg.LineHeight, typographyLineMin, typographyLineMax, def.LineHeight)
	cfg.PageWidth = check(cfg.PageWidth, typographyWidthMin, typographyWidthMax, def.PageWidth)
	cfg.RowPadY = check(cfg.RowPadY, typographyPadMin, typographyPadMax, def.RowPadY)
	cfg.RowGap = check(cfg.RowGap, typographyGapMin, typographyGapMax, def.RowGap)
	return cfg
}

func newTypographyStore(dir string) *typographyStore {
	if dir == "" {
		dir = configDir()
	}
	s := &typographyStore{dir: dir, p: defaultTypographyConfig()}
	var cfg typographyConfig
	if err := readJSONFile(filepath.Join(dir, typographyFileName), &cfg); err == nil {
		s.p = normalizeTypographyConfig(cfg)
	}
	return s
}

// save 只写盘，不改内存：四项的设置方法先写好内存值，再交给 save 落盘。
// 写失败时内存保留新值（与 imagePrefsStore.save 一致），调用方按错误提示，
// 需要停在旧值的界面用 restore 撤回内存改动。
func (s *typographyStore) save() error {
	if s == nil {
		return nil
	}
	if err := writeJSONFile(filepath.Join(s.dir, typographyFileName), s.p); err != nil {
		return fmt.Errorf("保存阅读排版失败：%w", err)
	}
	return nil
}

// Reset 恢复默认排版并落盘。
func (s *typographyStore) Reset() error {
	if s == nil {
		return nil
	}
	s.p = defaultTypographyConfig()
	return s.save()
}

func (s *typographyStore) snapshot() typographyConfig {
	if s == nil {
		return defaultTypographyConfig()
	}
	return s.p
}

// restore 只撤回内存值，不写盘：设置项写盘失败时让界面停在旧值。
func (s *typographyStore) restore(cfg typographyConfig) {
	if s != nil {
		s.p = cfg
	}
}

func (s *typographyStore) bodySize() int {
	if s == nil {
		return defaultTypographyConfig().BodySize
	}
	return s.p.BodySize
}

func (s *typographyStore) lineHeight() int {
	if s == nil {
		return defaultTypographyConfig().LineHeight
	}
	return s.p.LineHeight
}

func (s *typographyStore) pageWidth() int {
	if s == nil {
		return defaultTypographyConfig().PageWidth
	}
	return s.p.PageWidth
}

func (s *typographyStore) rowPadY() int {
	if s == nil {
		return defaultTypographyConfig().RowPadY
	}
	return s.p.RowPadY
}

func (s *typographyStore) rowGap() int {
	if s == nil {
		return defaultTypographyConfig().RowGap
	}
	return s.p.RowGap
}

// density 返回当前密度命中的档位序号，精确数值不等于任何预设时返回 -1。
func (s *typographyStore) density() int {
	return densityPresetIndex(s.rowPadY(), s.rowGap())
}

func densityPresetIndex(pad, gap int) int {
	for i := range typographyDensityPads {
		if pad == typographyDensityPads[i] && gap == typographyDensityGaps[i] {
			return i
		}
	}
	return -1
}

func (s *typographyStore) setBodySize(v int) error {
	return s.set("正文字号", v, typographyBodyMin, typographyBodyMax, func() { s.p.BodySize = v })
}

func (s *typographyStore) setLineHeight(v int) error {
	return s.set("行距", v, typographyLineMin, typographyLineMax, func() { s.p.LineHeight = v })
}

func (s *typographyStore) setPageWidth(v int) error {
	return s.set("页宽", v, typographyWidthMin, typographyWidthMax, func() { s.p.PageWidth = v })
}

func (s *typographyStore) setRowPadY(v int) error {
	return s.set("行留白", v, typographyPadMin, typographyPadMax, func() { s.p.RowPadY = v })
}

func (s *typographyStore) setRowGap(v int) error {
	return s.set("行内间距", v, typographyGapMin, typographyGapMax, func() { s.p.RowGap = v })
}

// setDensity 把密度设成某个档位：两个 DIP 值一起写，保证档位与精确视图一致。
func (s *typographyStore) setDensity(v int) error {
	if v < 0 || v >= len(typographyDensityPads) {
		return fmt.Errorf("列表密度档位无效")
	}
	before := s.snapshot()
	if err := s.setRowPadY(typographyDensityPads[v]); err != nil {
		return err
	}
	if err := s.setRowGap(typographyDensityGaps[v]); err != nil {
		s.restore(before)
		return err
	}
	return nil
}

// set 校验精确数值区间后落盘；越界值不会写进内存。
func (s *typographyStore) set(label string, v, lo, hi int, apply func()) error {
	if s == nil {
		return fmt.Errorf("设置%s失败：偏好未初始化", label)
	}
	if v < lo || v > hi {
		return fmt.Errorf("%s应在 %d～%d 之间", label, lo, hi)
	}
	apply()
	return s.save()
}

// typeBase 是标准档的字号阶梯，保留改造前的数值作为比例基准。标题目前用
// Lobe 的标题字阶绘制，notice 也没有独立调用点；两项仍留在阶梯里，保证
// 换算函数覆盖整组字号。
var typeBase = struct {
	title, body, author, comment, section, meta, metaSmall, notice float32
}{26, 16, 14, 15, 16, 13, 12, 12}

type typeSizes struct {
	title, body, author, comment, section, meta, metaSmall, notice float32
}

// roundHalfDIP 把缩放结果落到 0.5 DIP，避免出现 13.333 这种像素边界。
func roundHalfDIP(v float32) float32 {
	return float32(math.Round(float64(v)*2)) / 2
}

// typeSizes 按正文字号档位换算出整套阶梯：标准档严格等于 typeBase。
func (a *app) typeSizes() typeSizes {
	body := defaultTypographyConfig().BodySize
	if a != nil && a.typePrefs != nil {
		body = a.typePrefs.bodySize()
	}
	scale := float32(body) / typeBase.body
	scaled := func(base float32) float32 { return roundHalfDIP(base * scale) }
	return typeSizes{
		title:     scaled(typeBase.title),
		body:      scaled(typeBase.body),
		author:    scaled(typeBase.author),
		comment:   scaled(typeBase.comment),
		section:   scaled(typeBase.section),
		meta:      scaled(typeBase.meta),
		metaSmall: scaled(typeBase.metaSmall),
		notice:    scaled(typeBase.notice),
	}
}

// mdStyles 按当前字号与行距算正文 / 评论的 Markdown 排版。个人简介不参与
// 缩放；评论图片沿用固定高度上限，评论行距比正文紧 0.05。
func (a *app) mdStyles() (post, comment, bio mdTextStyle) {
	ts := a.typeSizes()
	line := float32(a.lineHeight()) / 10
	post = mdTextStyle{size: ts.body, line: line, block: 0.85, item: 0.35}
	comment = mdTextStyle{size: ts.comment, line: line - 0.05, block: 0.7, item: 0.3,
		imageMaxH: commentImageMaxH}
	bio = bioTextStyle
	return
}

func (a *app) lineHeight() int {
	if a == nil {
		return defaultTypographyConfig().LineHeight
	}
	return a.typePrefs.lineHeight()
}

// rowPadY / rowGap 是列表行的上下留白与行内间距。只管纵向密度，横向
// PaddingX(12) 不变，也不改列表字号。两者都是设置页可直接输入的 DIP 值。
func (a *app) rowPadY() float32 {
	if a == nil || a.typePrefs == nil {
		return float32(defaultTypographyConfig().RowPadY)
	}
	return float32(a.typePrefs.rowPadY())
}

func (a *app) rowGap() float32 {
	if a == nil || a.typePrefs == nil {
		return float32(defaultTypographyConfig().RowGap)
	}
	return float32(a.typePrefs.rowGap())
}

func (a *app) pageWidth() int {
	if a == nil {
		return defaultTypographyConfig().PageWidth
	}
	return a.typePrefs.pageWidth()
}

func (a *app) density() int {
	if a == nil {
		return typographyDensityStandard
	}
	return a.typePrefs.density()
}
