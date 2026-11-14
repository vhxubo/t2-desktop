package main

import (
	"fmt"
	"path/filepath"
)

// 实验功能：默认全部关闭，只在用户主动到设置里打开后才生效。每一项都是
// 一个独立开关，落盘在配置目录的 experiment.json，老用户（文件不存在）按
// 全部关闭处理。实验功能可能不稳定，仅供提前试用，关掉即回到旧行为。
type experimentStore struct {
	dir string
	// pet 是桌面宠物（t2-pet）的实验开关。默认 false：宠物不出现在托盘菜单
	// 里、启动也不会放出来，直到用户打开它。见 pet.go 与 settings_ui.go。
	pet bool
}

// experimentConfig 是持久化到 experiment.json 的内容。
type experimentConfig struct {
	Pet bool `json:"pet"`
}

func newExperimentStore(dir string) *experimentStore {
	if dir == "" {
		dir = configDir()
	}
	s := &experimentStore{dir: dir}
	var cfg experimentConfig
	if err := readJSONFile(filepath.Join(dir, "experiment.json"), &cfg); err == nil {
		s.pet = cfg.Pet
	}
	return s
}

// save 把当前选择写回 experiment.json。写失败时本次运行仍按新选择处理，只把
// 错误交给调用方提示；下次启动会回到旧配置。
func (s *experimentStore) save() error {
	if s == nil {
		return nil
	}
	if err := writeJSONFile(filepath.Join(s.dir, "experiment.json"), experimentConfig{Pet: s.pet}); err != nil {
		return fmt.Errorf("保存实验功能设置失败：%w", err)
	}
	return nil
}

// petOn 报告桌面宠物这一实验功能是否开启。没有偏好对象（无头测试里手工拼的
// app）时按关闭处理，不凭空冒出一只宠物。
func (s *experimentStore) petOn() bool { return s != nil && s.pet }

// petExperimentOn 报告桌面宠物这一实验功能当前是否可用：实验开关要开、偏好
// 对象（petStore）也要在。它是宠物一切「被放出来 / 开口说话」的总闸，比
// petStore.on()（用户的具体开关）高一级——实验没开，用户开了也没用。
func (a *app) petExperimentOn() bool {
	return a.experiment != nil && a.experiment.petOn() && a.petStore != nil
}

// setPetExperiment 是设置窗口里「桌面宠物（实验功能）」开关的唯一入口：写入
// 偏好、落盘，并立即让宠物按新状态出现或消失。只在主线程调用。
func (a *app) setPetExperiment(on bool) {
	if a.experiment == nil {
		return
	}
	if a.experiment.pet == on {
		return
	}
	a.experiment.pet = on
	if err := a.experiment.save(); err != nil {
		a.toast(toastSettings, err.Error())
		return
	}
	if on {
		// 开了实验开关就把宠物按用户之前的选择放出来；没开过宠物就先不显示，
		// 由托盘菜单的「桌面宠物」去开。
		a.startPet()
		a.toast(toastSettings, "桌面宠物已开启（实验功能），可从托盘菜单开关")
		return
	}
	// 关掉实验开关：把宠物收起来，但保留用户在 pet.json 里的选择，下次再开
	// 实验开关时按原选择恢复，不必重新开一次。
	a.hidePetWindow()
	a.toast(toastSettings, "桌面宠物已关闭（实验功能）")
}
