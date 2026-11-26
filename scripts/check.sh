#!/usr/bin/env bash
set -euo pipefail

export CGO_ENABLED=0

unformatted=$(
  while IFS= read -r -d '' f; do
    gofmt -l "$f"
  done < <(git ls-files -z -- '*.go')
)
if [[ -n "$unformatted" ]]; then
  printf '以下文件需要 gofmt：\n%s\n' "$unformatted" >&2
  exit 1
fi

if git grep -inE '//[[:space:]]*(TODO|FIXME|XXX|HACK)|panic\([[:space:]]*"(not implemented|unimplemented|TODO)"' -- '*.go'; then
  echo '请清理 Go 代码中的未完成标记。' >&2
  exit 1
else
  status=$?
  if [[ "$status" != 1 ]]; then exit "$status"; fi
fi

# 主包目录名是 src，默认输出名与仓库根目录的 src/ 同名（Windows 为 src.exe），统一写到 build/check/。
go build -o build/check/ ./...
go tool mygo vet .
go test -count=1 -shuffle=on -timeout=15m ./...
