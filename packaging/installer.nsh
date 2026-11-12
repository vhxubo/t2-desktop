; t2-desktop 的 Windows 安装程序钩子，由 mygo.json 的 windows.installerInclude 引入。
;
; 打包要求：安装程序必须以管理员身份运行（UAC 提权），装好之后的应用仍按
; 普通用户运行，不需要管理员权限。
;
; mygo build 生成的脚本先在开头写 RequestExecutionLevel user，再把本文件
; include 进来（在 Section 之前）。NSIS 允许重复声明，最后一条生效，因此这里
; 覆盖成 requireAdministrator：双击安装包就弹 UAC，而不是先按普通权限启动。
RequestExecutionLevel admin
