#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
temp_root=$(cd "${TMPDIR:-/tmp}" && pwd -P)
fixture=$(mktemp -d "$temp_root/t2-release-test.XXXXXXXX")
trap '[[ "$fixture" == "$temp_root"/t2-release-test.* ]] && rm -rf -- "$fixture"' EXIT
cd "$fixture"
export GITHUB_REPOSITORY=test/desktop

for pair in macOS/darwin Windows/windows Linux/linux; do
  folder="release-${pair%/*}"
  mkdir "$folder"
  for arch in amd64 arm64; do
    target="${pair#*/}-$arch"
    asset="desktop-$target.tar.gz"
    mkdir "$folder/$target"
    printf '%s' "$target" > "$folder/$target/$asset"
    jq -n --arg asset "$asset" --argjson size "${#target}" '{
      version: "1.2.3", notes: "发布说明", signature: "fixture-signature", size: $size,
      url: ("https://github.com/test/desktop/releases/download/v1.2.3/" + $asset)
    }' > "$folder/$target/update-$target.json"
    (cd "$folder/$target" && sha256sum "$asset") >> "$folder/SHA256SUMS"
  done
done

bash "$root/scripts/collect-release-assets.sh" . assets v1.2.3
(cd assets && sha256sum -c SHA256SUMS)
[[ $(wc -l < assets/SHA256SUMS) -eq 6 ]]

expect_failure() {
  local output=$1 message=$2
  if bash "$root/scripts/collect-release-assets.sh" . "$output" v1.2.3 > errors.log 2>&1; then
    printf '预期汇总失败：%s\n' "$output" >&2
    exit 1
  fi
  grep -F "$message" errors.log
}

manifest=release-Linux/linux-arm64/update-linux-arm64.json
mv "$manifest" saved.json
expect_failure missing 'update-linux-arm64.json'
jq '.size += 1' saved.json > "$manifest"
expect_failure bad-size '大小不匹配'
mv saved.json "$manifest"
printf '变更\n' >> release-Linux/linux-arm64/desktop-linux-arm64.tar.gz
expect_failure corrupt 'FAILED'
