//go:build !darwin

package main

import (
	_ "embed"
	"log"

	"github.com/egoist/mygo/ui"
)

const (
	harmonySansSCRegularFile = "fonts/HarmonyOS_Sans_SC_Regular.ttf"
	harmonySansSCBoldFile    = "fonts/HarmonyOS_Sans_SC_Bold.ttf"
)

//go:embed fonts/HarmonyOS_Sans_SC_Regular.ttf
var harmonySansSCRegularData []byte

//go:embed fonts/HarmonyOS_Sans_SC_Bold.ttf
var harmonySansSCBoldData []byte

// 非 macOS 的系统字体对中文未必是鸿蒙字体，随包注册常规与加粗两个字重。
// 两份字体注册在同一 family 下，mygo 按文字的字重从家族里挑对应文件，
// 700 因此是真正的 Bold，而不是系统合成的粗体。
func usesHarmonySans() bool { return true }

func init() {
	registerHarmonySansFont(harmonySansSCRegularFile, harmonySansSCRegularData)
	registerHarmonySansFont(harmonySansSCBoldFile, harmonySansSCBoldData)
}

func registerHarmonySansFont(file string, data []byte) {
	if err := ui.RegisterFont(data, appFontFamily); err != nil {
		log.Printf("注册内置字体 %s 失败：%v", file, err)
	}
}
