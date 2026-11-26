#!/usr/bin/env pwsh
# 校验发布 tag、版本与更新日志；与 scripts/release-metadata.sh 输出一致，不依赖 Bash/WSL 和 jq。
param(
    [Parameter(Mandatory = $true)]
    [string]$Tag
)
$ErrorActionPreference = 'Stop'
# 与 scripts/release-metadata.sh 一样，在仓库根目录执行。
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)

function Fail([string]$message) {
    [Console]::Error.WriteLine($message)
    exit 1
}

$cfg = Get-Content -Raw -LiteralPath 'mygo.json' | ConvertFrom-Json
$version = [string]$cfg.version
if ([string]::IsNullOrWhiteSpace($version)) {
    Fail 'mygo.json 缺少有效的 version。'
}
$prefix = if ($cfg.updates -and $cfg.updates.PSObject.Properties['tagPrefix']) {
    [string]$cfg.updates.tagPrefix
} else {
    'v'
}
if ($Tag -ne "v$version" -or ($prefix -ne 'v' -and $prefix -ne '')) {
    Fail "发布 tag $Tag 必须等于 v 加上 mygo.json 的版本 $version，且 MyGo 必须使用 v 前缀。"
}

$heading = "## $version"
$notes = @()
$found = $false
foreach ($line in (Get-Content -LiteralPath 'CHANGELOG.md')) {
    $line = $line -replace '\r$', ''
    if ($found -and $line -like '## *') { break }
    if ($found) { $notes += $line }
    if ($line -eq $heading) { $found = $true }
}
# 命令替换会去掉尾部空行，这里同样去掉，保证与 Bash 版输出一致。
$end = $notes.Count
while ($end -gt 0 -and [string]::IsNullOrWhiteSpace($notes[$end - 1])) { $end-- }
$notes = if ($end -eq 0) { @() } else { @($notes[0..($end - 1)]) }
if (-not ($notes -join "`n").Trim()) {
    Fail "CHANGELOG.md 缺少非空的「## $version」小节。"
}
[Console]::Out.Write(($notes -join "`n") + "`n")
