package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 桌面宠物（t2-pet）：一个透明的、始终在最前的小窗口，画的是一杆黄铜天平；
// 收到通知时以「它自己说话」的对话框形式讲一句，右键有菜单，拖动能换位置。
//
// 天平不是静态图标：平时横梁轻轻摇摆，右盘的砝码数跟着未读消息走，未读
// 越多右边越沉；来消息或说句话时以说话时刻为起点叠一段阻尼摆动，像刚被
// 放上一颗砝码。姿态全在 pet_ui.go 的 petMotion 里按时间算，不往状态里存
// 动画中间量。
//
// 为什么是同一个进程里的第二个窗口，而不是单独的 t2-pet 应用：
//   - 刷新令牌每次刷新都会轮换（见 auth.go 的 refreshLocked）：两个进程各存
//     一份 token.json，谁后刷新谁把对方的 refresh_token 作废，用户被踢下线；
//   - 通知靠 SSE 长连接（stream.go），第二条流会双倍建连、双倍配额，而接入
//     条款把「多开应用」明确列为规避配额；
//   - 新应用要重新申请 client_id、回调协议，用户还得再授权一次；
//   - 代码共享得先把 auth / api / stream 抽成 internal 包，那比宠物本身大。
// 所以宠物就是第二个窗口：一个安装包，托盘菜单里开关。
//
// 窗口只有 120×128：透明窗口的透明区域照样吞鼠标（mygo 的 PassThrough 只是
// 把指针交给同一窗口里的下层元素，不会交给桌面），窗口越小，桌面上被挡住的
// 死区越小。气泡因此不画在这个窗口里，而是用挂在它上面的 flyout。
//
// 交互：拖动整块移动（DragWindow），右键出菜单。左键不做点击——mygo 里
// DragWindow 与 OnClick 落在同一个元素上时互斥（input.go:481），要么能拖、
// 要么能点。拖得动比点得动重要：点不动的宠物还能用右键，拖不动的宠物没法用。

const (
	petWindowW = 120
	petWindowH = 128

	// 气泡的尺寸：FitContent 会按文字量收缩到这个区间内。
	petBubbleW    = 240
	petBubbleH    = 96
	petBubbleMinW = 150
	petBubbleMaxW = 280
	petBubbleMaxH = 240
	// petBubbleTail 是气泡底部那个指向宠物的小尖角占的高度。
	petBubbleTail = 8
	// petBubbleGap 是气泡尖角与宠物窗口顶边之间的距离。
	petBubbleGap = 2
)

// petBubbleShowFor 是气泡自己收起的时间。提成变量是为了测试能把它缩短。
var petBubbleShowFor = 12 * time.Second

// petMood 是宠物此刻的状态：平时静置，说话时支点上的宝石亮起来。
type petMood int

const (
	petIdle petMood = iota
	petTalk
)

// petState 是宠物显示的状态：当前状态、气泡里的标题与正文、点气泡要打开的目标。
// 只在主线程读写。
type petState struct {
	mood  petMood
	title string
	line  string
	// target 是点气泡时要打开的地方（帖子 / 评论），空表示只读的一句话。
	target string
	// at 是气泡出现的时刻，也是天平那一下倾摆的起点（见 petMotion）。
	at time.Time
	// open 表示气泡此刻是否在屏幕上。
	open bool
	// gen 让过期的自动收起回调失效：新气泡覆盖旧气泡后，旧定时器到点不再
	// 把新气泡收掉。
	gen uint64
}

// petConfig 是落盘在配置目录 pet.json 的内容。
type petConfig struct {
	Enabled bool `json:"enabled"`
}

// petStore 保存「要不要显示桌面宠物」这一个偏好。只在主线程读写。
type petStore struct {
	dir     string
	enabled bool
}

func newPetStore(dir string) *petStore {
	if dir == "" {
		dir = configDir()
	}
	s := &petStore{dir: dir}
	var cfg petConfig
	if err := readJSONFile(filepath.Join(dir, "pet.json"), &cfg); err == nil {
		s.enabled = cfg.Enabled
	}
	return s
}

// save 把当前选择写回 pet.json。写失败时本次运行仍按新选择处理，只把错误
// 交给调用方提示；下次启动会回到旧配置。
func (s *petStore) save() error {
	if s == nil {
		return nil
	}
	if err := writeJSONFile(filepath.Join(s.dir, "pet.json"), petConfig{Enabled: s.enabled}); err != nil {
		return fmt.Errorf("保存桌面宠物设置失败：%w", err)
	}
	return nil
}

// on 报告用户是否开着桌面宠物。没有偏好对象（无头测试里手工拼的 app）时
// 按关闭处理，不凭空冒出一个窗口。
func (s *petStore) on() bool { return s != nil && s.enabled }

/* ------------------------------------------------------------------
   开关、窗口与气泡
---------------------------------------------------------------- */

// startPet 在应用就绪后按上次的选择决定要不要把宠物放出来。实验功能没开时
// 不放大任何宠物窗口——这是宠物被放出来的总闸（见 experiment.go）。
func (a *app) startPet() {
	if !a.petExperimentOn() || !a.petStore.on() {
		return
	}
	a.showPetWindow()
}

// showPet 是「打开桌面宠物」的全局入口：任何窗口、任何 goroutine 都能调用。
func (a *app) showPet() { a.publish(func() { a.setPetEnabled(true) }) }

// hidePet 是「收起桌面宠物」的全局入口。
func (a *app) hidePet() { a.publish(func() { a.setPetEnabled(false) }) }

// togglePet 是托盘菜单的开关。
func (a *app) togglePet() { a.publish(func() { a.setPetEnabled(!a.petShown) }) }

// setPetEnabled 是「桌面宠物」开关的唯一入口：托盘菜单（以后还有设置窗口）
// 都走这里，所以落盘与结果提示只写一份。只在主线程调用。实验功能没开时它
// 直接返回：用户的具体开关（petStore）只在实验开关之上才有效。
func (a *app) setPetEnabled(on bool) {
	if !a.petExperimentOn() {
		return
	}
	if on == a.petShown && on == a.petStore.enabled {
		return
	}
	a.petStore.enabled = on
	if err := a.petStore.save(); err != nil {
		a.toast(toastMain, err.Error())
	}
	if on {
		a.showPetWindow()
		a.toast(toastMain, "桌面宠物出来了，拖它可以换位置")
		return
	}
	a.hidePetWindow()
	a.toast(toastMain, "桌面宠物已收起")
}

// showPetWindow 在主线程上真正建出宠物窗口。重复调用只把已经藏起来的窗口
// 拿回来，不会开出第二只。
func (a *app) showPetWindow() {
	a.setPetShown(true)
	// 无头测试没有主循环：碰 mygo 的窗口会等一个不存在的处理者，所以只把
	// 状态摆好，测试直接渲染 petView / petBubbleView。
	if a.win == nil {
		return
	}
	if win := a.petWin; win != nil {
		win.Show()
		win.Invalidate()
		return
	}
	win := mygo.NewWindow(mygo.WindowOptions{
		Title:             "桌面宠物",
		Width:             petWindowW,
		Height:            petWindowH,
		Frameless:         true,
		Transparent:       true,
		Panel:             true, // 浮在所有窗口之上，不进任务栏与 Alt+Tab
		SkipTaskbar:       true,
		DisableResize:     true,
		DisableMinimize:   true,
		DisableMaximize:   true,
		DisableFullScreen: true,
		DisableShadow:     true, // 窗口是一整块透明，阴影只会画出个方框
		StateKey:          "pet",
		Content:           ui.View(a.petView),
	})
	a.petWin = win
	// 宠物窗口整块透明，Windows 11 给窗口画的那圈描边和贴着圆角的阴影，在桌面
	// 上就是一个空心方框加一圈灰雾，所以两样都让它别画（见 window_chrome_windows.go）。
	removeWindowChrome(win.NativeHandle())
	// 关掉宠物窗口等于「收起宠物」，不退出应用：主窗口还在（可能收在托盘里）。
	win.OnClose(func(e *mygo.CloseEvent) {
		if a.quitting {
			return
		}
		e.PreventDefault()
		a.setPetEnabled(false)
	})
	win.OnClosed(func() {
		a.petWin = nil
		a.petBubble = nil
		a.setPetShown(false)
	})
}

// hidePetWindow 把宠物收起来：窗口只 Hide 不 Close，下次打开还是原来那一位
// 置（位置由 StateKey 记住，Hide/Show 之间也保持不变）。只在主线程调用。
func (a *app) hidePetWindow() {
	a.hidePetBubble()
	if win := a.petWin; win != nil {
		win.Hide()
	}
	a.setPetShown(false)
}

// setPetShown 记下宠物此刻是否在屏幕上，并让主窗口重画一次（托盘菜单下次
// 弹出时会读到这个状态）。只在主线程调用。
func (a *app) setPetShown(shown bool) {
	if a.petShown == shown {
		return
	}
	a.petShown = shown
	if a.win != nil {
		a.win.Invalidate()
	}
}

// redrawPetWindow 让宠物按最新状态重画。只在主线程调用。
func (a *app) redrawPetWindow() {
	if win := a.petWin; win != nil {
		win.Invalidate()
	}
}

// petAnnounce 把一批新消息交给宠物讲一句。调用方在 UI 线程（新消息的播报在
// publish 里跑）。一次来多条时只说最后一条，其余数量写进标题——气泡不是列表，
// 一口气念三条谁也读不完。
//
// 宠物有自己的开关（托盘里的「桌面宠物」），和系统通知的开关互不影响：关掉
// 系统通知、只留宠物的人来说，这一条路是通的。
func (a *app) petAnnounce(notices []nativeNotice) {
	if !a.petExperimentOn() || !a.petStore.on() || len(notices) == 0 {
		return
	}
	n := notices[len(notices)-1]
	title := n.Title
	if len(notices) > 1 {
		title = fmt.Sprintf("%s，另有 %d 条", title, len(notices)-1)
	}
	a.sayPet(title, n.Body, n.Target)
}

// petGreet 是右键菜单里的「说句话」：没有消息时也要有一个能让宠物开口的
// 入口，否则用户不知道它会不会说话。
func (a *app) petGreet() {
	if a.unread > 0 {
		a.sayPet("有新消息", fmt.Sprintf("还有 %d 条消息没看，点托盘里的通知就能看。", a.unread), "")
		return
	}
	a.sayPet("", "我在呢。拖我可以换位置，右键有菜单。", "")
}

// sayPet 让宠物冒出气泡说一句话，几秒后自己收起。只在主线程调用。
func (a *app) sayPet(title, line, target string) {
	if !a.petShown || (title == "" && line == "") {
		return
	}
	if a.pet == nil {
		a.pet = &petState{}
	}
	st := a.pet
	st.mood = petTalk
	st.title, st.line, st.target = title, line, target
	st.at = time.Now()
	st.open = true
	st.gen++
	gen := st.gen
	a.stopPetBubbleTimer()
	a.petTimer = time.AfterFunc(petBubbleShowFor, func() {
		a.publish(func() {
			if a.pet == nil || a.pet.gen != gen {
				return
			}
			a.hidePetBubble()
		})
	})
	a.showPetBubbleWindow()
	a.redrawPetWindow()
}

// hidePetBubble 收起气泡，宠物回到发呆。只在主线程调用。
func (a *app) hidePetBubble() {
	a.stopPetBubbleTimer()
	if a.pet == nil {
		return
	}
	a.pet.open = false
	a.pet.mood = petIdle
	a.pet.gen++ // 让还排着的自动收起回调失效
	if f := a.petBubble; f != nil {
		f.Hide()
	}
	a.redrawPetWindow()
	if a.onPetBubbleHidden != nil {
		a.onPetBubbleHidden()
	}
}

// stopPetBubbleTimer 停掉气泡的自动收起定时器。只在主线程调用。
func (a *app) stopPetBubbleTimer() {
	if a.petTimer != nil {
		a.petTimer.Stop()
		a.petTimer = nil
	}
}

// showPetBubbleWindow 把气泡弹出来。第一次用时现建一个 flyout：它挂在宠物
// 窗口上，宠物被拖动时会跟着走，宠物收起时一起关掉。
func (a *app) showPetBubbleWindow() {
	f := a.ensurePetBubble()
	if f == nil {
		return
	}
	f.Show()
	f.Invalidate()
}

// ensurePetBubble 返回气泡窗口，没有就建一个。无头测试（没有宠物窗口）时
// 返回 nil，测试直接渲染 petBubbleView。
func (a *app) ensurePetBubble() *mygo.Window {
	if a.petBubble != nil {
		return a.petBubble
	}
	if a.petWin == nil {
		return nil
	}
	f := mygo.NewFlyout(mygo.FlyoutOptions{
		Parent: a.petWin,
		// 锚点放在宠物窗口顶边中间：PlacementTop 让气泡长在窗口上方，
		// 上方没位置时 mygo 自己会翻到下面。
		Anchor:    mygo.Rectangle{X: petWindowW/2 - 8, Y: 0, Width: 16, Height: 8},
		Placement: mygo.PlacementTop,
		Gap:       petBubbleGap,
		Width:     petBubbleW,
		Height:    petBubbleH,
		// 按文字量收缩：一句话的气泡不该和一段话一样宽。
		FitContent: true,
		MinWidth:   petBubbleMinW,
		MaxWidth:   petBubbleMaxW,
		MaxHeight:  petBubbleMaxH,
		// 不抢键盘：气泡是通知，不是对话框，弹出来不能把用户正在写的东西
		// 打断。点了它才打开对应的帖子。
		Focusable: false,
		Shadow:    true,
		Content:   ui.View(a.petBubbleView),
	})
	a.petBubble = f
	return f
}

// openPetBubbleTarget 是点气泡后的动作：有目标就打开它，没有就只把气泡收掉。
func (a *app) openPetBubbleTarget() {
	target := ""
	if a.pet != nil {
		target = a.pet.target
	}
	a.hidePetBubble()
	if target != "" {
		a.openNotificationTarget(target)
	}
}
