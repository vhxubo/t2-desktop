; t2-desktop 的 Windows 安装程序钩子，由 mygo.json 的 windows.installerInclude 引入。
;
; 保留 mygo 默认的按用户安装。完成页直接启动应用，应用会继承安装器的权限。

; 卸载时询问是否一并清理本机数据。复选框默认不勾选；静默卸载（/S）不显示
; 这一页，因此同样保留数据。mygo 生成的卸载页先删程序文件，这一页随后出现，
; 勾选的数据删除也放在这里，而不是卸载 Section 里。
Var unCleanData

Function un.DataPage
  nsDialogs::Create 1018
  Pop $0
  ${If} $0 == error
    Abort
  ${EndIf}

  ${NSD_CreateLabel} 0 0 100% 20u "勾选后删除本机的账号、设置与缓存。"
  Pop $0
  ${NSD_CreateCheckbox} 0 28u 100% 14u "同时清理用户数据"
  Pop $unCleanData
  ${NSD_SetState} $unCleanData ${BST_UNCHECKED}

  nsDialogs::Show
FunctionEnd

Function un.DataPageLeave
  ${NSD_GetState} $unCleanData $0
  ${If} $0 == ${BST_CHECKED}
    ; 应用自己的数据（令牌回退文件、账号、偏好、草稿、图片与离线缓存）
    RMDir /r "$APPDATA\T2 Desktop"
    ; 旧版本遗留目录，已不再使用；保留清理以便升级用户卸载时清干净
    RMDir /r "$APPDATA\2libra-native-reader"
    ; mygo 的窗口位置与更新器状态
    RMDir /r "$APPDATA\t2-desktop"
    RMDir /r "$LOCALAPPDATA\t2-desktop"
  ${EndIf}
FunctionEnd

UninstPage custom un.DataPage un.DataPageLeave
