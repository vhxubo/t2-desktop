[CmdletBinding()]
param(
    [string]$ExePath,
    [string]$OutputDirectory = (Join-Path $PSScriptRoot '..\docs\images'),
    [ValidateRange(1, 60)][int]$TimeoutSeconds = 8,
    [ValidateRange(0, 10000)][int]$SettleMilliseconds = 900,
    [string]$TopicTitle,
    [switch]$CurrentWindowsOnly,
    [switch]$Strict
)

$ErrorActionPreference = 'Stop'
if ($env:OS -ne 'Windows_NT' -or -not [Environment]::UserInteractive) {
    throw '请在已解锁的 Windows 桌面会话运行。'
}

Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes, System.Drawing
if (-not ('T2Capture.Native' -as [type])) {
    Add-Type -Namespace T2Capture -Name Native -MemberDefinition @'
    private delegate bool EnumProc(IntPtr window, IntPtr param);
    [StructLayout(LayoutKind.Sequential)]
    public struct Rect { public int Left, Top, Right, Bottom; }
    [StructLayout(LayoutKind.Sequential)]
    private struct Point { public int X, Y; }
    [DllImport("user32.dll")] private static extern bool EnumWindows(EnumProc callback, IntPtr param);
    [DllImport("user32.dll")] private static extern bool IsWindowVisible(IntPtr window);
    [DllImport("user32.dll")] private static extern uint GetWindowThreadProcessId(IntPtr window, out uint process);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] private static extern int GetWindowText(IntPtr window, System.Text.StringBuilder text, int length);
    [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr window);
    [DllImport("user32.dll")] public static extern bool ShowWindowAsync(IntPtr window, int command);
    [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr window, IntPtr dc, uint flags);
    [DllImport("user32.dll")] public static extern bool GetClientRect(IntPtr window, out Rect rect);
    [DllImport("user32.dll")] public static extern IntPtr SetThreadDpiAwarenessContext(IntPtr context);
    [DllImport("user32.dll")] private static extern bool ScreenToClient(IntPtr window, ref Point point);
    [DllImport("user32.dll")] private static extern IntPtr ChildWindowFromPointEx(IntPtr window, Point point, uint flags);
    [DllImport("user32.dll")] private static extern bool PostMessage(IntPtr window, uint message, IntPtr wparam, IntPtr lparam);
    public static bool Click(IntPtr window, int x, int y) {
        var previous = SetThreadDpiAwarenessContext(new IntPtr(-4));
        try {
            Point point = new Point { X = x, Y = y };
            if (!ScreenToClient(window, ref point)) return false;
            Rect rect;
            if (!GetClientRect(window, out rect) || point.X < 0 || point.Y < 0 || point.X >= rect.Right || point.Y >= rect.Bottom)
                return false;
            var child = ChildWindowFromPointEx(window, point, 3);
            if (child == IntPtr.Zero) return false;
            point = new Point { X = x, Y = y };
            if (!ScreenToClient(child, ref point)) return false;
            var position = new IntPtr((point.Y << 16) | (point.X & 0xffff));
            return PostMessage(child, 0x0200, IntPtr.Zero, position) &&
                   PostMessage(child, 0x0201, new IntPtr(1), position) && PostMessage(child, 0x0202, IntPtr.Zero, position);
        } finally {
            if (previous != IntPtr.Zero) SetThreadDpiAwarenessContext(previous);
        }
    }
    public static IntPtr[] Windows(int processId) {
        var windows = new System.Collections.Generic.List<IntPtr>();
        EnumWindows((window, param) => {
            uint owner;
            GetWindowThreadProcessId(window, out owner);
            if (owner == processId && IsWindowVisible(window) && Title(window).Length != 0)
                windows.Add(window);
            return true;
        }, IntPtr.Zero);
        return windows.ToArray();
    }
    public static string Title(IntPtr window) {
        var text = new System.Text.StringBuilder(1024);
        GetWindowText(window, text, text.Capacity);
        return text.ToString();
    }
'@
}

function Wait-Result([scriptblock]$Poll, [string]$Failure) {
    $clock = [Diagnostics.Stopwatch]::StartNew()
    do {
        $value = & $Poll
        if ($value) { return $value }
        Start-Sleep -Milliseconds 150
    } while ($clock.Elapsed.TotalSeconds -lt $TimeoutSeconds)
    throw $Failure
}

function Get-Processes {
    Get-Process -Name $processName -ErrorAction SilentlyContinue | Where-Object {
        $_.SessionId -eq $sessionId -and (-not $ExePath -or $_.Path -eq $ExePath)
    }
}

function Get-MainWindows {
    foreach ($process in Get-Processes) {
        foreach ($handle in [T2Capture.Native]::Windows($process.Id)) {
            $title = [T2Capture.Native]::Title($handle)
            if ($title -match '^T2 Desktop · (.+)$') {
                [pscustomobject]@{ Handle = $handle; ProcessId = $process.Id; Name = $Matches[1] }
            }
        }
    }
}

function Resolve-App {
    $mainWindows = @(Get-MainWindows)
    if ($mainWindows.Count -eq 0) {
        if (Get-Processes) {
            throw '应用已运行，但找不到已登录的主窗口。请从托盘恢复窗口。'
        }
        $launchPath = $ExePath
        if (-not $launchPath) {
            foreach ($key in @('HKCU:\Software\Microsoft', 'HKLM:\Software\Microsoft', 'HKLM:\Software\WOW6432Node\Microsoft')) {
                $install = Get-ItemProperty "$key\Windows\CurrentVersion\Uninstall\com.vhxubo.t2-desktop" -ErrorAction SilentlyContinue
                if ($install.InstallLocation) {
                    $candidate = Join-Path $install.InstallLocation 't2-desktop.exe'
                    if (Test-Path -LiteralPath $candidate -PathType Leaf) { $launchPath = $candidate; break }
                }
            }
        }
        if (-not $launchPath) {
            $candidate = Join-Path $env:LOCALAPPDATA 'Programs\t2-desktop\t2-desktop.exe'
            if (Test-Path -LiteralPath $candidate -PathType Leaf) { $launchPath = $candidate }
        }
        if (-not $launchPath) { throw '找不到已安装的 T2 Desktop，请用 -ExePath 指定程序。' }
        Write-Host '正在启动 T2 Desktop（使用本机原有会话）。'
        Start-Process -FilePath $launchPath
        $mainWindows = @(Wait-Result { Get-MainWindows } '没有出现已登录的主窗口；请先在应用中完成登录。')
    }
    if ($mainWindows.Count -ne 1) { throw '找到多个已登录主窗口，请先关闭多余的窗口。' }
    return $mainWindows[0]
}

function Find-Window([string]$Title) {
    foreach ($handle in [T2Capture.Native]::Windows($app.ProcessId)) {
        $name = [T2Capture.Native]::Title($handle)
        if ($name -ceq "$Title · $($app.Name)") { return $handle }
    }
}

function Get-Controls([IntPtr]$Window, [Windows.Automation.Condition]$Condition) {
    try {
        $root = [Windows.Automation.AutomationElement]::FromHandle($Window)
        foreach ($element in $root.FindAll('Descendants', $Condition)) {
            if (-not $element.Current.IsOffscreen -and $element.Current.IsEnabled) { $element }
        }
    } catch [Windows.Automation.ElementNotAvailableException] { return }
}

function Invoke-Control($Element) {
    $pattern = $null
    if ($Element.TryGetCurrentPattern([Windows.Automation.InvokePattern]::Pattern, [ref]$pattern)) {
        $pattern.Invoke()
    } elseif ($Element.TryGetCurrentPattern([Windows.Automation.SelectionItemPattern]::Pattern, [ref]$pattern)) {
        $pattern.Select()
    } else {
        return $false
    }
    return $true
}

function Get-NamedControls([IntPtr]$Window, [string]$Name) {
    $condition = New-Object Windows.Automation.PropertyCondition -ArgumentList ([Windows.Automation.AutomationElement]::NameProperty, $Name)
    Get-Controls $Window $condition
}

function Select-Control([IntPtr]$Window, [string]$Name, [switch]$Optional) {
    $select = {
        foreach ($element in Get-NamedControls $Window $Name) {
            if (Invoke-Control $element) { return $true }
        }
    }
    if ($Optional) { return (& $select) }
    [void](Wait-Result $select "未找到可操作的控件「$Name」。")
}

function Get-PaletteMode {
    Get-NamedControls $app.Handle '命令' | Where-Object { $_.Current.ControlType -eq [Windows.Automation.ControlType]::Button } | Select-Object -First 1
}

function Open-CommandPalette {
    $inputBox = Wait-Result { Get-NamedControls $app.Handle '搜索关键词' | Select-Object -First 1 } '找不到命令中心输入框。'
    if (-not (Get-PaletteMode)) {
        $homeControl = Get-NamedControls $app.Handle '首页' | Select-Object -First 1
        if ($homeControl) { $homeControl.SetFocus(); Start-Sleep -Milliseconds 150 }
        $inputBox.SetFocus()
        Start-Sleep -Milliseconds 150
        if (-not (Get-PaletteMode)) {
            $bounds = $inputBox.Current.BoundingRectangle
            if (-not [T2Capture.Native]::Click($app.Handle, [int]($bounds.X + $bounds.Width / 2), [int]($bounds.Y + $bounds.Height / 2))) {
                throw '无法点击命令中心输入框。'
            }
        }
    }
    [void](Wait-Result { Get-PaletteMode } '未检测到命令中心。')
    return $inputBox.GetCurrentPattern([Windows.Automation.ValuePattern]::Pattern)
}

function Invoke-CommandEntry([string]$Name) {
    $value = Open-CommandPalette
    try {
        if (Select-Control $app.Handle '命令' -Optional) { Start-Sleep -Milliseconds 150 }
        $value.SetValue($Name)
        Select-Control $app.Handle "命令 $Name"
    } catch {
        $value.SetValue('')
        throw
    }
}

function Save-Window([IntPtr]$Window, [string]$File) {
    if ([T2Capture.Native]::IsIconic($Window)) { [void][T2Capture.Native]::ShowWindowAsync($Window, 9) }
    Start-Sleep -Milliseconds $SettleMilliseconds
    $previousDpi = [T2Capture.Native]::SetThreadDpiAwarenessContext([IntPtr](-4))
    try {
        $rect = New-Object T2Capture.Native+Rect
        if (-not [T2Capture.Native]::GetClientRect($Window, [ref]$rect) -or $rect.Right -le 0 -or $rect.Bottom -le 0) {
            throw '目标窗口没有可捕获的客户区。'
        }
        $bitmap = New-Object Drawing.Bitmap -ArgumentList $rect.Right, $rect.Bottom
        try {
            $graphics = [Drawing.Graphics]::FromImage($bitmap)
            try {
                $dc = $graphics.GetHdc()
                try { $printed = [T2Capture.Native]::PrintWindow($Window, $dc, 3) }
                finally { $graphics.ReleaseHdc($dc) }
            } finally { $graphics.Dispose() }
            if (-not $printed) { throw 'Windows 未能捕获该窗口。' }
            $first = $bitmap.GetPixel(0, 0)
            $varied = $false
            for ($row = 0; $row -lt $bitmap.Height -and -not $varied; $row += 7) {
                for ($column = 0; $column -lt $bitmap.Width; $column += 7) {
                    if ($bitmap.GetPixel($column, $row) -ne $first) { $varied = $true; break }
                }
            }
            if (-not $varied) { throw '窗口捕获为空白；请确认桌面已解锁且窗口完成绘制。' }
            $bitmap.Save((Join-Path $runDirectory $File), [Drawing.Imaging.ImageFormat]::Png)
        } finally { $bitmap.Dispose() }
    } finally {
        if ($previousDpi -ne [IntPtr]::Zero) { [void][T2Capture.Native]::SetThreadDpiAwarenessContext($previousDpi) }
    }
}

function Capture-Scene([string]$Id, [scriptblock]$Navigate, [switch]$PassThru) {
    try {
        $window = & $Navigate
        if (-not $window) { throw '该场景的入口或数据当前不可用。' }
        $file = "$Id.png"
        Save-Window $window $file
        $results.Add([pscustomobject]@{ scene = $Id; status = 'captured'; file = $file })
        Write-Host "已截图：$Id"
        if ($PassThru) { return $window }
    } catch {
        $results.Add([pscustomobject]@{ scene = $Id; status = 'skipped'; reason = $_.Exception.Message })
        Write-Warning "${Id}：$($_.Exception.Message)"
    }
}

function Capture-Tabs($Window, [string]$Prefix, [System.Collections.IDictionary]$Tabs) {
    foreach ($tab in $Tabs.GetEnumerator()) {
        Capture-Scene "$Prefix-$($tab.Key)" {
            if (-not $Window) { throw '父窗口未打开。' }
            Select-Control $Window $tab.Value
            $Window
        }
    }
}

function Capture-Panel([string]$Id, [string]$Title, [string]$Entry, [System.Collections.IDictionary]$Tabs, [scriptblock]$Open) {
    $window = Capture-Scene $Id {
        if ($Open) {
            & $Open
        } elseif (-not (Select-Control $app.Handle $Entry -Optional)) {
            Invoke-CommandEntry $Entry
        }
        Wait-Result { Find-Window $Title } "未打开「$Title」窗口。"
    } -PassThru
    if ($Tabs) { Capture-Tabs $window $Id $Tabs }
}

$sessionId = (Get-Process -Id $PID).SessionId
if ($ExePath) {
    $ExePath = (Get-Item -LiteralPath $ExePath).FullName
    if ([IO.Path]::GetExtension($ExePath) -ine '.exe') { throw '-ExePath 必须指向 .exe 文件。' }
}
$processName = if ($ExePath) { [IO.Path]::GetFileNameWithoutExtension($ExePath) } else { 't2-desktop' }
$app = Resolve-App
if ([T2Capture.Native]::IsIconic($app.Handle)) { [void][T2Capture.Native]::ShowWindowAsync($app.Handle, 9) }
$runDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
[void][IO.Directory]::CreateDirectory($runDirectory)
$results = [Collections.Generic.List[object]]::new()

try {
    if ($CurrentWindowsOnly) {
        Capture-Scene 'main' { $app.Handle }
        $index = 0
        foreach ($window in [T2Capture.Native]::Windows($app.ProcessId)) {
            if ($window -eq $app.Handle) { continue }
            $title = [T2Capture.Native]::Title($window)
            if ($title -notmatch '^T2 Desktop') {
                Capture-Scene ('window-{0:d2}' -f (++$index)) { $window }
            }
        }
    } else {
        Capture-Tabs $app.Handle 'main' ([ordered]@{
            home='首页'; 'hot-today'='今日热议'; 'hot-week'='近期热议'; latest='新发表'; comments='最新评论';
            community='社区讨论'; timeline='动态'; drafts='草稿箱'; 'watch-later'='稍后再看'; scores='评分榜'
        })
        Capture-Scene 'reading-history' {
            Select-Control $app.Handle '稍后再看'
            Select-Control $app.Handle '浏览历史'
            $app.Handle
        }
        Capture-Scene 'command-palette' {
            $value = Open-CommandPalette
            Select-Control $app.Handle '命令' -Optional | Out-Null
            $value.SetValue('')
            $app.Handle
        }
        Capture-Panel 'about' '关于' '关于'
        Capture-Panel 'settings' '设置' '设置' ([ordered]@{
            general='通用'; network='网络'; images='图片'; reading='阅读与评论'; notifications='通知'; shortcuts='快捷键'
        })
        Capture-Panel 'notifications' '通知' '通知' ([ordered]@{
            all='全部'; unread='未读'; replies='回复'; mentions='提及'; reactions='表态'; rewards='打赏'
        })
        Capture-Panel 'search' '搜索' '搜索' ([ordered]@{
            posts='帖子'; users='用户'; nodes='节点'; tags='标签'; bookmarks='收藏'
        })
        Capture-Panel 'profile' '个人中心' '个人中心' ([ordered]@{
            overview='概览'; about='个人介绍'; content='内容'; assets='资产'; badges='徽章'; settings='设置'
        }) -Open {
            if (-not (Select-Control $app.Handle '打开个人中心窗口' -Optional)) {
                Select-Control $app.Handle '个人中心'
                Select-Control $app.Handle '打开个人中心窗口'
            }
        }
        $profile = Find-Window '个人中心'
        foreach ($group in @(
            @{ tab='内容'; tabs=[ordered]@{ posts='发布'; collections='合集'; comments='评论' } },
            @{ tab='资产'; tabs=[ordered]@{ favorites='收藏'; bookmarks='书签' } },
            @{ tab='设置'; tabs=[ordered]@{ permissions='账号与权限'; filters='屏蔽设置' } }
        )) {
            foreach ($tab in $group.tabs.GetEnumerator()) {
                Capture-Scene "profile-$($tab.Key)" {
                    if (-not $profile) { throw '个人中心未打开。' }
                    Select-Control $profile $group.tab
                    Select-Control $profile $tab.Value
                    $profile
                }
            }
        }
        Capture-Panel 'follows' '关注与粉丝' '关注与粉丝' ([ordered]@{ following='我关注的人'; followers='关注我的人' }) -Open {
            if ($profile) {
                Select-Control $profile '概览'
                if ((Select-Control $profile '关注' -Optional) -or (Select-Control $profile '粉丝' -Optional)) { return }
            }
            Invoke-CommandEntry '关注与粉丝'
        }
        Capture-Panel 'coins' '金币' '金币' ([ordered]@{ today='今日'; history='历史'; stats='统计'; rankings='排行'; redeem='兑换' }) -Open {
            if (-not (Select-Control $app.Handle '金币' -Optional)) {
                Select-Control $app.Handle '个人中心'
                Select-Control $app.Handle '金币'
            }
        }
        Select-Control $app.Handle '首页' -Optional | Out-Null
        Capture-Panel 'compose-post' '发帖' '发帖'
        Capture-Panel 'compose-moment' '发动态' '发动态'
        Capture-Panel 'network-log' '网络调试' '网络日志' -Open {
            $settings = Find-Window '设置'
            if ($settings) {
                Select-Control $settings '网络'
                Select-Control $settings '网络调试'
            } else {
                Invoke-CommandEntry '网络日志'
            }
        }
        $detailWindow = Capture-Scene 'topic-detail' {
            Select-Control $app.Handle '首页'
            $before = @([T2Capture.Native]::Windows($app.ProcessId))
            [void](Wait-Result {
                foreach ($row in Get-Controls $app.Handle ([Windows.Automation.Condition]::TrueCondition)) {
                    $name = $row.Current.Name
                    if ($row.Current.ControlType -ne [Windows.Automation.ControlType]::Button -or $name -notmatch '最后回复') { continue }
                    if ($TopicTitle -and -not $name.StartsWith($TopicTitle, [StringComparison]::Ordinal)) { continue }
                    if (Invoke-Control $row) { return $true }
                }
            } '首页找不到匹配的帖子；可用 -TopicTitle 指定首页帖子的标题前缀。')
            $candidate = Wait-Result {
                foreach ($handle in @([T2Capture.Native]::Windows($app.ProcessId) | Sort-Object { $_ -in $before })) {
                    $title = [T2Capture.Native]::Title($handle)
                    if (($handle -eq $app.Handle -or $title.EndsWith(" · $($app.Name)", [StringComparison]::Ordinal)) -and
                        (Get-NamedControls $handle '评论区')) { return $handle }
                }
            } '没有检测到帖子详情。'
            $candidate
        } -PassThru
        foreach ($entry in @(
            @{ id='reply-editor'; label='独立窗口写评论'; title='写评论' },
            @{ id='edit-post'; label='编辑帖子'; title='编辑帖子' },
            @{ id='edit-comment'; label='编辑评论'; title='编辑评论' },
            @{ id='edit-node'; label='修改节点'; title='修改节点' }
        )) {
            Capture-Scene $entry.id {
                if ($detailWindow -and (Select-Control $detailWindow $entry.label -Optional)) {
                    Wait-Result { Find-Window $entry.title } "未打开「$($entry.title)」窗口。"
                }
            }
        }
        Capture-Scene 'user-card' {
            $follows = Find-Window '关注与粉丝'
            if ($follows) {
                $user = Get-Controls $follows ([Windows.Automation.Condition]::TrueCondition) |
                    Where-Object { $_.Current.Name -like '用户 *' } | Select-Object -First 1
                if ($user -and (Invoke-Control $user)) {
                    Wait-Result { Find-Window '用户资料' } '没有打开用户资料窗口。'
                }
            }
        }
        Capture-Scene 'media-preview' {
            if ($detailWindow) {
                foreach ($media in Get-Controls $detailWindow ([Windows.Automation.Condition]::TrueCondition)) {
                    if (($media.Current.Name -like '打开视频：*' -or $media.Current.ControlType -eq [Windows.Automation.ControlType]::Image) -and
                        (Invoke-Control $media)) {
                        Wait-Result { Find-Window '媒体预览' } '没有打开媒体预览窗口。'
                        break
                    }
                }
            }
        }
        Capture-Scene 'pandora' {
            Select-Control $app.Handle '首页'
            if (-not (Select-Control $app.Handle '潘多拉' -Optional)) {
                Select-Control $app.Handle '个人中心'
                if (-not (Select-Control $app.Handle '潘多拉' -Optional)) { Invoke-CommandEntry '盲盒' }
            }
            [void](Wait-Result { Get-NamedControls $app.Handle '潘多拉' | Where-Object { $_.Current.ControlType -eq [Windows.Automation.ControlType]::Text } | Select-Object -First 1 } '没有检测到潘多拉弹窗。')
            $app.Handle
        }
        if ($results[$results.Count - 1].status -eq 'captured' -and -not (Get-NamedControls $app.Handle '首页')) {
            Select-Control $app.Handle '关闭' -Optional | Out-Null
        }
    }
} finally {
    ConvertTo-Json -InputObject @($results.ToArray()) -Depth 4 | Set-Content -LiteralPath (Join-Path $runDirectory 'manifest.json') -Encoding UTF8
    Write-Host "截图目录：$runDirectory"
}

if (-not ($results | Where-Object status -eq 'captured')) { throw '没有成功捕获任何界面。' }
if ($Strict -and ($results | Where-Object status -ne 'captured')) { throw '存在未捕获的场景，见 manifest.json。' }
