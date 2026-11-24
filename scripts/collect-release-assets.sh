#!/usr/bin/env bash
set -euo pipefail
shopt -s nullglob

source_dir=${1:?请提供下载产物的目录}
output_dir=${2:?请提供新的汇总目录}
tag=${3:?请提供发布 tag}
version=${tag#v}
mkdir "$output_dir"
output_dir=$(cd "$output_dir" && pwd)

for platform in macOS Windows Linux; do
  (
    cd "$source_dir/release-$platform"
    while IFS= read -r line; do
      file=${line:66}
      matches=(*/"$file")
      if [[ "$file" == */* || ${#matches[@]} -eq 0 ]]; then
        printf '找不到 %s 校验和对应的产物：%s\n' "$platform" "$file" >&2
        exit 1
      fi
      for match in "${matches[@]}"; do
        printf '%s  %s\n' "${line:0:64}" "$match" | sha256sum --check --strict
        if [[ -e "$output_dir/$file" ]]; then
          cmp "$match" "$output_dir/$file"
        else
          cp "$match" "$output_dir/$file"
        fi
      done
    done < SHA256SUMS
    for manifest in */update-*.json; do
      name=${manifest##*/}
      [[ ! -e "$output_dir/$name" ]]
      cp "$manifest" "$output_dir/$name"
    done
  )
done

cd "$output_dir"
for target in darwin-arm64 darwin-amd64 windows-amd64 windows-arm64 linux-amd64 linux-arm64; do
  manifest="update-$target.json"
  refs=$(jq -er --arg version "$version" '
    if .version != $version or (.notes | length) == 0 then error("发布版本或说明不匹配") else
      (., .deltas[]?) | if .url and .size > 0 and (.signature | length > 0)
      then [.url, .size] | @tsv else error("更新元数据不完整") end
    end
  ' "$manifest")
  while IFS=$'\t' read -r url size; do
    asset=${url##*/}
    if [[ "$url" != "https://github.com/${GITHUB_REPOSITORY:?}/releases/download/$tag/$asset" ||
          ! -f "$asset" || $(wc -c < "$asset") -ne "$size" ]]; then
      printf '%s 引用的产物不存在或大小不匹配：%s\n' "$manifest" "$asset" >&2
      exit 1
    fi
  done <<< "$refs"
done
assets=()
for file in *; do
  case "$file" in update-*.json) continue ;; esac
  assets+=("$file")
done
sha256sum -- "${assets[@]}" > SHA256SUMS
