#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(dirname "${script_dir}")"
test_repo="$(mktemp -d "${TMPDIR:-/tmp}/loadsim-release-test.XXXXXX")"

cleanup() {
  chmod -R u+rwX "${test_repo}" 2>/dev/null || true
  rm -rf -- "${test_repo}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_contains() {
  local path="$1"
  local expected="$2"
  grep -F -- "${expected}" "${path}" >/dev/null ||
    fail "${path} does not contain ${expected}"
}

assert_excludes() {
  local path="$1"
  local unexpected="$2"
  if grep -F -- "${unexpected}" "${path}" >/dev/null; then
    fail "${path} unexpectedly contains ${unexpected}"
  fi
}

cd "${test_repo}"
git init -q
git config user.name "LoadSim test"
git config user.email "loadsim-test@example.invalid"
printf 'fixture\n' >fixture.txt
git add fixture.txt
git commit -q -m "fixture"

# Deliberately create tags out of release order. Compare selection must depend
# on semantic version precedence, never tag creation time.
for tag in \
  v1.2.0 \
  v0.9.0 \
  v1.2.0-beta.2 \
  v2.0.0 \
  v1.0.0 \
  v1.2.0-beta.1 \
  v1.1.0; do
  git tag "${tag}"
done

mkdir -p .github/release-notes
for tag in v0.9.0 v1.0.0 v1.1.0 v1.2.0 v2.0.0; do
  printf '%s\n' \
    '## [新增]' '- 无。' '' \
    '## [变更]' '- 无。' '' \
    '## [修复]' '- 无。' '' \
    '## [移除]' '- 无。' \
    >".github/release-notes/${tag}.md"
done
for tag in v1.2.0-beta.1 v1.2.0-beta.2; do
  printf '%s\n' '## 测试版' '- 测试。' \
    >".github/release-notes/${tag}.md"
done

export GITHUB_REPOSITORY="example/loadsim"

bash "${project_dir}/scripts/compose_release_body.sh" \
  v1.2.0 \
  stable.md
assert_contains stable.md \
  "https://github.com/example/loadsim/compare/v1.1.0...v1.2.0"
assert_excludes stable.md "v1.2.0-beta.2...v1.2.0"

bash "${project_dir}/scripts/compose_release_body.sh" \
  v1.2.0-beta.2 \
  beta-two.md
assert_contains beta-two.md \
  "https://github.com/example/loadsim/compare/v1.2.0-beta.1...v1.2.0-beta.2"
assert_excludes beta-two.md "compare/v1.2.0...v1.2.0-beta.2"

bash "${project_dir}/scripts/compose_release_body.sh" \
  v1.2.0-beta.1 \
  beta-one.md
assert_contains beta-one.md \
  "https://github.com/example/loadsim/compare/v1.1.0...v1.2.0-beta.1"

bash "${project_dir}/scripts/compose_release_body.sh" \
  v0.9.0 \
  first-stable.md
assert_contains first-stable.md \
  "当前版本是首个正式版，变更范围为当前仓库全部代码。"

# Git tag may exist without a corresponding published GitHub Release. When the
# workflow supplies the published-release inventory, such a tag must not become
# the compare base.
git tag v1.1.9
printf '%s\n' \
  v0.9.0 \
  v1.0.0 \
  v1.1.0 \
  v1.1.8 \
  v1.2.0-beta.1 \
  v1.2.0-beta.2 \
  >published-tags.txt
PUBLISHED_TAGS_FILE="${test_repo}/published-tags.txt" \
  bash "${project_dir}/scripts/compose_release_body.sh" \
  v1.2.0 \
  published-only.md
assert_contains published-only.md \
  "https://github.com/example/loadsim/compare/v1.1.8...v1.2.0"
assert_excludes published-only.md "v1.1.9...v1.2.0"

# Numeric components are arbitrary-length unsigned decimals. AWK must compare
# them as decimal strings instead of converting them to imprecise doubles.
git tag v9007199254740992.0.0
git tag v9007199254740993.0.0
printf '%s\n' \
  '## [新增]' '- 无。' '' \
  '## [变更]' '- 无。' '' \
  '## [修复]' '- 无。' '' \
  '## [移除]' '- 无。' \
  >.github/release-notes/v9007199254740993.0.0.md
bash "${project_dir}/scripts/compose_release_body.sh" \
  v9007199254740993.0.0 \
  huge-version.md
assert_contains huge-version.md \
  "https://github.com/example/loadsim/compare/v9007199254740992.0.0...v9007199254740993.0.0"

git tag v3.0.0-beta.9007199254740992
git tag v3.0.0-beta.9007199254740993
printf '%s\n' '## 测试版' '- 测试。' \
  >.github/release-notes/v3.0.0-beta.9007199254740993.md
bash "${project_dir}/scripts/compose_release_body.sh" \
  v3.0.0-beta.9007199254740993 \
  huge-beta.md
assert_contains huge-beta.md \
  "https://github.com/example/loadsim/compare/v3.0.0-beta.9007199254740992...v3.0.0-beta.9007199254740993"

# Synchronizing an existing release freezes its recorded compare base even if a
# semantically intermediate tag is published later.
PREVIOUS_TAG_OVERRIDE_SET=true \
  PREVIOUS_TAG_OVERRIDE=v1.1.0 \
  bash "${project_dir}/scripts/compose_release_body.sh" \
  v1.2.0 \
  frozen-base.md
assert_contains frozen-base.md \
  "https://github.com/example/loadsim/compare/v1.1.0...v1.2.0"
assert_excludes frozen-base.md "v1.1.9...v1.2.0"

# An explicit empty override preserves the fact that a release was the first
# stable release, even after older releases are backfilled.
PREVIOUS_TAG_OVERRIDE_SET=true \
  PREVIOUS_TAG_OVERRIDE= \
  bash "${project_dir}/scripts/compose_release_body.sh" \
  v1.2.0 \
  frozen-first.md
assert_contains frozen-first.md \
  "当前版本是首个正式版，变更范围为当前仓库全部代码。"
assert_excludes frozen-first.md "/compare/"

if PREVIOUS_TAG_OVERRIDE_SET=true \
  PREVIOUS_TAG_OVERRIDE=v2.0.0 \
  bash "${project_dir}/scripts/compose_release_body.sh" \
    v1.2.0 \
    invalid-future-base.md; then
  fail "future override was accepted"
fi

if PREVIOUS_TAG_OVERRIDE_SET=true \
  PREVIOUS_TAG_OVERRIDE=v1.2.0-beta.2 \
  bash "${project_dir}/scripts/compose_release_body.sh" \
    v1.2.0 \
    invalid-stable-beta-base.md; then
  fail "stable release accepted a beta compare override"
fi

echo "PASS: release compare selection is published-only, precise, and stable"
