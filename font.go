package main

import (
	"embed"
	"log"
	"runtime"

	"github.com/egoist/mygo/ui"
)

// 界面字体：非 macOS 用随应用打包的鸿蒙字体，macOS 保留系统自带的苹方
// （PingFang SC）。字体版权与授权条款见 fonts/LICENSE，随包副本见
// resources/third_party/HarmonyOS_Sans_LICENSE.txt。
const appFontFamily = "HarmonyOS Sans SC"

//go:embed fonts/HarmonyOS_Sans_SC_Regular.ttf fonts/HarmonyOS_Sans_SC_Medium.ttf fonts/HarmonyOS_Sans_SC_Bold.ttf
var harmonySansSC embed.FS

// 顺序按字重从轻到重注册：同一字族下，mygo 按字体声明的字重匹配，
// 重名的才取先注册的。中文的 Medium / Bold 因此不用退化成合成粗体。
var harmonySansSCFiles = []string{
	"fonts/HarmonyOS_Sans_SC_Regular.ttf",
	"fonts/HarmonyOS_Sans_SC_Medium.ttf",
	"fonts/HarmonyOS_Sans_SC_Bold.ttf",
}

// usesHarmonySans 报告这个平台是否用打包的鸿蒙字体。macOS 的系统字体
// 对中文就是苹方，按用户要求不覆盖。
func usesHarmonySans() bool { return runtime.GOOS != "darwin" }

func init() {
	if !usesHarmonySans() {
		return
	}
	for _, name := range harmonySansSCFiles {
		data, err := harmonySansSC.ReadFile(name)
		if err != nil {
			log.Printf("读取内置字体 %s 失败：%v", name, err)
			continue
		}
		if err := ui.RegisterFont(data, appFontFamily); err != nil {
			log.Printf("注册内置字体 %s 失败：%v", name, err)
		}
	}
}
