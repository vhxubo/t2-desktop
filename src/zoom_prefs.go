package main

import (
	"fmt"
	"math"
	"path/filepath"

	"github.com/egoist/mygo/ui"
)

// 界面缩放偏好，进程级一份（见 appDeps）：所有窗口每帧都把 c.SetZoom 设成
// 这个值，设置页、命令面板、快捷键与 Ctrl+滚轮改的都是同一份状态，落盘在
// zoom.json。这里用的是 mygo 的窗口内容缩放（文字、图标与布局一起放大），
// 不是把窗口截图拉大，阅读排版与图片预览各自的缩放都不受影响。
const (
	appZoomMin     float32 = 0.25
	appZoomMax     float32 = 5
	appZoomDefault float32 = 1
	// 滚轮一格的倍率，和浏览器常用的 10% 一档接近。
	appZoomWheelStep float32 = 1.1

	zoomPrefsFile = "zoom.json"
)

// zoomPresetValues / zoomPresetLabels 是设置页的常用档位；精确数值不在这些
// 档位上时哪个都不高亮，用户仍可用数值框手动设定 25%～500%。
var (
	zoomPresetValues = []float32{0.5, 0.75, 1, 1.25, 1.5, 2}
	zoomPresetLabels = []string{"50%", "75%", "100%", "125%", "150%", "200%"}
)

type zoomPrefsStore struct {
	dir  string
	zoom float32
}

type zoomPrefsConfig struct {
	Zoom float32 `json:"zoom"`
}

// clampAppZoom 把缩放夹在 mygo 支持的范围内，并收敛到两位小数：滚轮连滚时
// 不会攒出一串二进制浮点误差，设置页显示的百分比也不会跳动。
func clampAppZoom(z float32) float32 {
	if math.IsNaN(float64(z)) || z <= 0 {
		return appZoomDefault
	}
	z = min(max(z, appZoomMin), appZoomMax)
	return float32(math.Round(float64(z)*100)) / 100
}

func newZoomPrefsStore(dir string) *zoomPrefsStore {
	if dir == "" {
		dir = configDir()
	}
	s := &zoomPrefsStore{dir: dir, zoom: appZoomDefault}
	var cfg zoomPrefsConfig
	if err := readJSONFile(filepath.Join(dir, zoomPrefsFile), &cfg); err == nil {
		s.zoom = clampAppZoom(cfg.Zoom)
	}
	return s
}

func (s *zoomPrefsStore) value() float32 {
	if s == nil {
		return appZoomDefault
	}
	return s.zoom
}

func (s *zoomPrefsStore) save() error {
	if s == nil {
		return nil
	}
	if err := writeJSONFile(filepath.Join(s.dir, zoomPrefsFile), zoomPrefsConfig{Zoom: s.zoom}); err != nil {
		return fmt.Errorf("保存界面缩放设置失败：%w", err)
	}
	return nil
}

// set 只改内存，写盘交给调用方；失败时保留用户刚选的值，和其余偏好一致。
func (s *zoomPrefsStore) set(z float32) {
	if s != nil {
		s.zoom = clampAppZoom(z)
	}
}

// Reset 恢复 100% 并落盘，供设置页「一键重置所有设置」使用。
func (s *zoomPrefsStore) Reset() error {
	if s == nil {
		return nil
	}
	s.zoom = appZoomDefault
	return s.save()
}

/* ------------------------------------------------------------------
   应用层：每帧应用缩放、改缩放、通知所有窗口重画
---------------------------------------------------------------- */

// zoom 返回当前生效的缩放。nil 偏好（老测试用零值 app）按 100% 处理。
func (a *app) zoom() float32 {
	if a == nil {
		return appZoomDefault
	}
	return a.zoomPrefs.value()
}

// setAppZoom 改缩放并落盘，然后让所有窗口下一帧按新尺寸重建。只在 UI 线程
// 调用；写盘失败时内存里仍按新值走（本次运行有效），错误交给调用方提示。
func (a *app) setAppZoom(z float32) error {
	if a == nil || a.zoomPrefs == nil {
		return nil
	}
	next := clampAppZoom(z)
	if next == a.zoomPrefs.zoom {
		return nil
	}
	a.zoomPrefs.set(next)
	err := a.zoomPrefs.save()
	a.applyZoomChange()
	return err
}

func (a *app) zoomIn()  { _ = a.setAppZoom(a.zoom() * appZoomWheelStep) }
func (a *app) zoomOut() { _ = a.setAppZoom(a.zoom() / appZoomWheelStep) }
func (a *app) resetZoom() {
	_ = a.setAppZoom(appZoomDefault)
}

// applyZoomChange 让所有窗口按新的缩放重建。主窗口、详情窗口和各附属窗口
// 都是独立的 mygo 窗口，各自的 view 在 windowView 里读同一份偏好。只在 UI
// 线程调用。
func (a *app) applyZoomChange() {
	if a == nil {
		return
	}
	if a.win != nil {
		a.win.Invalidate()
	}
	for _, h := range a.detailWindows {
		h.invalidate()
	}
	for _, win := range a.auxiliaryWindows() {
		if win != nil {
			win.Invalidate()
		}
	}
}

// zoomInput 让 Ctrl+滚轮 / Ctrl+触控板缩放改整页缩放。挂在每扇窗口的根元素
// 上，没有被内层控件吃掉时由它接管；图片预览与头像裁剪这类自己处理滚轮的
// 控件也先问它一次，这样那两个区域里 Ctrl+滚轮同样缩放整页，普通滚轮仍归
// 它们自己。返回 true 表示事件已处理，不再滚动内容。
func (a *app) zoomInput(ev ui.InputEvent) bool {
	if ev.Mods&ui.Ctrl == 0 || a == nil {
		return false
	}
	switch ev.Kind {
	case ui.InputScroll:
		switch {
		case ev.DY < 0:
			a.zoomIn()
		case ev.DY > 0:
			a.zoomOut()
		default:
			return false
		}
		return true
	case ui.InputZoom:
		if ev.Scale <= 0 || ev.Scale == 1 {
			return false
		}
		a.zoomBy(ev.Scale)
		return true
	}
	return false
}

// zoomBy 按倍率缩放，供 InputZoom 使用。
func (a *app) zoomBy(factor float32) { _ = a.setAppZoom(a.zoom() * factor) }

// zoomPresetIndex 返回当前缩放落在哪个常用档位；-1 表示精确数值不在预设里。
func zoomPresetIndex(z float32) int {
	for i, v := range zoomPresetValues {
		if math.Abs(float64(z-v)) < 0.005 {
			return i
		}
	}
	return -1
}
