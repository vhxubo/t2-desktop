#!/usr/bin/env bash
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
temp_root=$(cd "${TMPDIR:-/tmp}" && pwd -P)
fixture=$(mktemp -d "$temp_root/t2-hook-test.XXXXXXXX")
trap '[[ "$fixture" == "$temp_root"/t2-hook-test.* ]] && rm -rf -- "$fixture"' EXIT
cd "$fixture"
git init -q -b main
git config user.name '测试'
git config user.email fixture@example.invalid
git config core.autocrlf false
mkdir -p .githooks scripts
cp "$root/.githooks/pre-push" .githooks/
chmod +x .githooks/pre-push
cp "$root/scripts/release-metadata.sh" scripts/
cp "$root/scripts/release-metadata.ps1" scripts/
printf '%s\n' '{"version":"1.2.3"}' > mygo.json
printf '## 1.2.3\n\n发布说明\n' > CHANGELOG.md
printf 'pass\n' > result
cat > scripts/check.sh <<'EOF'
printf 'check\n'
test "$(cat result)" = pass
EOF
cat > scripts/check.ps1 <<'EOF'
Write-Output 'check'
if ((Get-Content result -Raw).Trim() -ne 'pass') { exit 1 }
EOF
git add .
git commit -qm 'test: create passing fixture'
passing=$(git rev-parse HEAD)
git tag -am 'test: create release tag' v1.2.3
tag_oid=$(git rev-parse refs/tags/v1.2.3)
printf 'fail\n' > result
printf '## 1.2.3\n\n' > CHANGELOG.md
git add result CHANGELOG.md
git commit -qm 'test: create failing fixture'
failing=$(git rev-parse HEAD)
printf 'pass\n' > result

run_hook() {
  printf '%s\n' "$@" | .githooks/pre-push origin fixture 2>&1
}

expect_failure() {
  local message=$1
  shift
  if run_hook "$@" > hook.log 2>&1; then
    printf '预期钩子失败：%s\n' "$message" >&2
    exit 1
  fi
  grep -F "$message" hook.log
}

zero=${passing//?/0}
branch="refs/heads/main $passing refs/heads/main $zero"
release="refs/tags/v1.2.3 $tag_oid refs/tags/v1.2.3 $zero"
output=$(run_hook "$branch" "$release")
[[ $(printf '%s\n' "$output" | grep -cx check) == 1 ]]
expect_failure check "refs/heads/main $failing refs/heads/main $zero"
expect_failure '必须等于' "refs/tags/v1.2.3 $tag_oid refs/tags/v2.0.0 $zero"
expect_failure '缺少非空' "HEAD $failing refs/tags/v1.2.3 $zero"
[[ -z "$(run_hook "(delete) $zero refs/heads/gone $passing" "refs/tags/notes $failing refs/tags/notes $zero")" ]]
[[ $(git worktree list --porcelain | grep -c '^worktree ') == 1 ]]
[[ $(cat result) == pass ]]
