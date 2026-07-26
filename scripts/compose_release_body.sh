#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

if [ "$#" -ne 2 ]; then
  echo "用法: $0 <tag> <output-file>" >&2
  exit 1
fi

TAG="$1"
OUTPUT_FILE="$2"
REPO_SLUG="${GITHUB_REPOSITORY:-}"
NOTES_FILE=".github/release-notes/${TAG}.md"
IS_BETA="false"

STABLE_TAG_PATTERN='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'
BETA_TAG_PATTERN='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)-beta\.(0|[1-9][0-9]*)$'

if [[ "$TAG" =~ $BETA_TAG_PATTERN ]]; then
  IS_BETA="true"
elif [[ ! "$TAG" =~ $STABLE_TAG_PATTERN ]]; then
  echo "标签格式无效: $TAG" >&2
  echo "只允许 v<主版本>.<次版本>.<修订版本> 或 v<主版本>.<次版本>.<修订版本>-beta.<序号>" >&2
  exit 1
fi

if [ ! -f "$NOTES_FILE" ]; then
  echo "缺少发布说明文件: $NOTES_FILE" >&2
  exit 1
fi

if [ "$IS_BETA" != "true" ]; then
  if ! awk '
    BEGIN {
      expected[1] = "## [新增]"
      expected[2] = "## [变更]"
      expected[3] = "## [修复]"
      expected[4] = "## [移除]"
      section = 0
      valid = 1
    }
    /^## / {
      section++
      if (section > 4 || $0 != expected[section]) {
        printf "正式版发布说明章节无效（第 %d 个二级章节）: %s\n", section, $0 > "/dev/stderr"
        valid = 0
      }
      next
    }
    section > 0 && $0 !~ /^[[:space:]]*$/ {
      has_content[section] = 1
    }
    END {
      if (section != 4) {
        printf "正式版发布说明必须恰好包含 4 个二级章节，实际为 %d 个。\n", section > "/dev/stderr"
        valid = 0
      }
      for (i = 1; i <= 4; i++) {
        if (!has_content[i]) {
          printf "正式版发布说明章节缺少内容: %s\n", expected[i] > "/dev/stderr"
          valid = 0
        }
      }
      exit(valid ? 0 : 1)
    }
  ' "$NOTES_FILE"; then
    echo "正式版发布说明必须依次使用：## [新增]、## [变更]、## [修复]、## [移除]。" >&2
    exit 1
  fi
fi

mkdir -p "$(dirname "$OUTPUT_FILE")"
cat "$NOTES_FILE" >"$OUTPUT_FILE"

PREVIOUS_TAG="$(
  if [ "${PREVIOUS_TAG_OVERRIDE_SET:-false}" = "true" ]; then
    printf '%s\n' "${PREVIOUS_TAG_OVERRIDE:-}"
  else
    if [ -n "${PUBLISHED_TAGS_FILE:-}" ]; then
      if [ ! -f "$PUBLISHED_TAGS_FILE" ] || [ ! -r "$PUBLISHED_TAGS_FILE" ]; then
        echo "已发布标签文件不存在或不可读: $PUBLISHED_TAGS_FILE" >&2
        exit 1
      fi
      cat "$PUBLISHED_TAGS_FILE"
    else
      git tag --list
    fi |
    awk -v current="$TAG" -v current_is_beta="$IS_BETA" '
      function compare_uint(left, right) {
        if (length(left) != length(right)) {
          return length(left) < length(right) ? -1 : 1
        }
        if ("x" left == "x" right) {
          return 0
        }
        return ("x" left < "x" right) ? -1 : 1
      }
      function parse_version(tag, fields, count) {
        tag = substr(tag, 2)
        count = split(tag, fields, /[-.]/)
        parsed_major = fields[1]
        parsed_minor = fields[2]
        parsed_patch = fields[3]
        parsed_is_beta = count == 5 && fields[4] == "beta"
        parsed_beta = parsed_is_beta ? fields[5] : "0"
      }
      function compare_core(left_major, left_minor, left_patch, right_major, right_minor, right_patch) {
        component_order = compare_uint(left_major, right_major)
        if (component_order != 0) {
          return component_order
        }
        component_order = compare_uint(left_minor, right_minor)
        if (component_order != 0) {
          return component_order
        }
        return compare_uint(left_patch, right_patch)
      }
      function candidate_is_newer() {
        core_order = compare_core(parsed_major, parsed_minor, parsed_patch, best_major, best_minor, best_patch)
        if (core_order != 0) {
          return core_order > 0
        }
        if (parsed_is_beta != best_is_beta) {
          return !parsed_is_beta
        }
        return parsed_is_beta && compare_uint(parsed_beta, best_beta) > 0
      }
      BEGIN {
        parse_version(current)
        current_major = parsed_major
        current_minor = parsed_minor
        current_patch = parsed_patch
        current_beta = parsed_beta
        found = 0
      }
      {
        candidate = $0
        if (candidate !~ /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-beta\.(0|[1-9][0-9]*))?$/ || candidate == current) {
          next
        }

        parse_version(candidate)
        core_order = compare_core(parsed_major, parsed_minor, parsed_patch, current_major, current_minor, current_patch)

        if (current_is_beta != "true") {
          if (parsed_is_beta || core_order >= 0) {
            next
          }
        } else if (core_order > 0 || (core_order == 0 && (!parsed_is_beta || compare_uint(parsed_beta, current_beta) >= 0))) {
          next
        }

        if (!found || candidate_is_newer()) {
          found = 1
          best_tag = candidate
          best_major = parsed_major
          best_minor = parsed_minor
          best_patch = parsed_patch
          best_is_beta = parsed_is_beta
          best_beta = parsed_beta
        }
      }
      END {
        if (found) {
          print best_tag
        }
      }
    '
  fi
)"

if [ "${PREVIOUS_TAG_OVERRIDE_SET:-false}" = "true" ] &&
  [ -n "$PREVIOUS_TAG" ] &&
  [[ ! "$PREVIOUS_TAG" =~ $STABLE_TAG_PATTERN ]] &&
  [[ ! "$PREVIOUS_TAG" =~ $BETA_TAG_PATTERN ]]; then
  echo "指定的上一版标签格式无效: $PREVIOUS_TAG" >&2
  exit 1
fi

if [ "${PREVIOUS_TAG_OVERRIDE_SET:-false}" = "true" ] &&
  [ -n "$PREVIOUS_TAG" ]; then
  if ! awk -v current="$TAG" -v previous="$PREVIOUS_TAG" -v current_is_beta="$IS_BETA" '
    function compare_uint(left, right) {
      if (length(left) != length(right)) {
        return length(left) < length(right) ? -1 : 1
      }
      if ("x" left == "x" right) {
        return 0
      }
      return ("x" left < "x" right) ? -1 : 1
    }
    function parse_version(tag, result, fields, count) {
      tag = substr(tag, 2)
      count = split(tag, fields, /[-.]/)
      result["major"] = fields[1]
      result["minor"] = fields[2]
      result["patch"] = fields[3]
      result["is_beta"] = count == 5 && fields[4] == "beta"
      result["beta"] = result["is_beta"] ? fields[5] : "0"
    }
    function compare_core(left, right, order) {
      order = compare_uint(left["major"], right["major"])
      if (order != 0) {
        return order
      }
      order = compare_uint(left["minor"], right["minor"])
      if (order != 0) {
        return order
      }
      return compare_uint(left["patch"], right["patch"])
    }
    BEGIN {
      parse_version(previous, previous_version)
      parse_version(current, current_version)
      core_order = compare_core(previous_version, current_version)

      if (current_is_beta != "true") {
        valid = !previous_version["is_beta"] && core_order < 0
      } else {
        valid = core_order < 0 ||
          (core_order == 0 &&
            previous_version["is_beta"] &&
            compare_uint(previous_version["beta"], current_version["beta"]) < 0)
      }
      exit(valid ? 0 : 1)
    }
  '; then
    echo "指定的上一版标签 $PREVIOUS_TAG 不能作为 $TAG 的历史对比起点。" >&2
    exit 1
  fi
fi

printf "\n\n## 变更对比\n\n" >>"$OUTPUT_FILE"

if [ -n "$PREVIOUS_TAG" ] && [ -n "$REPO_SLUG" ]; then
  printf "**完整变更**: https://github.com/%s/compare/%s...%s\n" \
    "$REPO_SLUG" "$PREVIOUS_TAG" "$TAG" >>"$OUTPUT_FILE"
elif [ "$IS_BETA" = "true" ]; then
  printf "当前 beta 版本之前没有可用于对比的已发布标签。\n" >>"$OUTPUT_FILE"
else
  printf "当前版本是首个正式版，变更范围为当前仓库全部代码。\n" >>"$OUTPUT_FILE"
  if [ -n "$REPO_SLUG" ]; then
    printf "**当前代码**: https://github.com/%s/tree/%s\n" \
      "$REPO_SLUG" "$TAG" >>"$OUTPUT_FILE"
  fi
fi
