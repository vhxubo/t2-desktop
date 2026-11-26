#!/usr/bin/env pwsh
# 本地提交前门禁：与 scripts/check.sh 的检查项一致，用 PowerShell 执行，不依赖 Bash/WSL。
# 与 scripts/check.sh 一样，在仓库根目录执行。
$ErrorActionPreference = 'Stop'
$env:CGO_ENABLED = '0'

function Fail([string]$message) {
    [Console]::Error.WriteLine($message)
    exit 1
}

function Assert-LastExit {
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}

# 逐个检查已跟踪的 Go 文件，避免 xargs 在 Git Bash 下的差异。
$unformatted = @()
foreach ($file in (git ls-files -- '*.go')) {
    $out = gofmt -l $file
    if ($out) { $unformatted += $out }
}
Assert-LastExit
if ($unformatted.Count -gt 0) {
    Fail ("以下文件需要 gofmt：`n{0}" -f ($unformatted -join "`n"))
}

git grep -inE '//[[:space:]]*(TODO|FIXME|XXX|HACK)|panic\([[:space:]]*"(not implemented|unimplemented|TODO)"' -- '*.go'
switch ($LASTEXITCODE) {
    0 { Fail '请清理 Go 代码中的未完成标记。' }
    1 { }
    default { exit $LASTEXITCODE }
}

go build ./...
Assert-LastExit
go tool mygo vet .
Assert-LastExit
go test -count=1 -shuffle=on -timeout=15m ./...
Assert-LastExit
Write-Host '检查通过。'
