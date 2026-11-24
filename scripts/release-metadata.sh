#!/usr/bin/env bash
set -euo pipefail

tag=${1:?请提供发布 tag，例如 v1.0.0}
version=$(jq -er '.version | select(type == "string" and length > 0)' mygo.json)
if [[ "$tag" != "v$version" ]] || ! jq -e '(.updates.tagPrefix // "v") as $prefix | $prefix == "v" or $prefix == ""' mygo.json >/dev/null; then
  printf '发布 tag %s 必须等于 v 加上 mygo.json 的版本 %s，且 MyGo 必须使用 v 前缀。\n' "$tag" "$version" >&2
  exit 1
fi

notes=$(awk -v heading="## $version" '
  { sub(/\r$/, "") }
  found && /^## / { exit }
  found { print }
  $0 == heading { found = 1 }
' CHANGELOG.md)
if [[ ! "$notes" =~ [^[:space:]] ]]; then
  printf 'CHANGELOG.md 缺少非空的「## %s」小节。\n' "$version" >&2
  exit 1
fi
printf '%s\n' "$notes"
