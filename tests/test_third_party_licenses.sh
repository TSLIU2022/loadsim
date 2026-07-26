#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(dirname "${script_dir}")"
test_dir="$(mktemp -d "${TMPDIR:-/tmp}/loadsim-license-test.XXXXXX")"

cleanup() {
  chmod -R u+rwX "${test_dir}" 2>/dev/null || true
  rm -rf -- "${test_dir}"
}
trap cleanup EXIT

output_file="${test_dir}/THIRD_PARTY_LICENSES-amd64.txt"
arm64_output="${test_dir}/THIRD_PARTY_LICENSES-arm64.txt"
amd64_binary="${test_dir}/loadsim-amd64"
arm64_binary="${test_dir}/loadsim-arm64"
cd "${project_dir}"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  bash scripts/generate_third_party_licenses.sh "${output_file}"
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  bash scripts/generate_third_party_licenses.sh "${arm64_output}"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -o "${amd64_binary}" .
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -o "${arm64_binary}" .

for module in \
  github.com/shirou/gopsutil/v4 \
  github.com/spf13/cobra \
  github.com/spf13/pflag \
  github.com/tklauser/go-sysconf \
  github.com/tklauser/numcpus \
  golang.org/x/sys; do
  grep -F -- "模块: ${module}" "${output_file}" >/dev/null
  grep -F -- "模块: ${module}" "${arm64_output}" >/dev/null
done

assert_module_set_matches_binary() {
  local license_file="$1"
  local binary="$2"
  local generated_modules="${test_dir}/generated-modules.txt"
  local binary_modules="${test_dir}/binary-modules.txt"

  awk '
    /^模块: / {
      module = substr($0, length("模块: ") + 1)
      next
    }
    module != "" && /^版本: / {
      print module "\t" substr($0, length("版本: ") + 1)
      module = ""
    }
  ' "${license_file}" | sort >"${generated_modules}"
  go version -m "${binary}" |
    awk '$1 == "dep" { print $2 "\t" $3 }' |
    sort >"${binary_modules}"
  if ! diff -u "${binary_modules}" "${generated_modules}"; then
    echo "FAIL: ${license_file} module set does not match ${binary}" >&2
    exit 1
  fi
}

assert_module_set_matches_binary "${output_file}" "${amd64_binary}"
assert_module_set_matches_binary "${arm64_output}" "${arm64_binary}"

grep -F -- '组件: Go 运行时与标准库' "${output_file}" >/dev/null
grep -F -- "版本: $(GOTOOLCHAIN=auto go env GOVERSION)" "${output_file}" >/dev/null
grep -F -- 'Additional IP Rights Grant (Patents)' "${output_file}" >/dev/null
patents_count="$(grep -c '^文件: PATENTS$' "${output_file}")"
if [[ "${patents_count}" -lt 2 ]]; then
  echo "FAIL: Go 工具链和 x/sys 的 PATENTS 没有分别归档" >&2
  exit 1
fi
grep -F -- 'Redistribution and use in source and binary forms' \
  "${output_file}" >/dev/null
grep -F -- 'Apache License' "${output_file}" >/dev/null

for generated in "${output_file}" "${arm64_output}"; do
  mode="$(stat -c '%a' "${generated}")"
  if [[ "${mode}" != "644" ]]; then
    echo "FAIL: ${generated} mode=${mode}, want 644" >&2
    exit 1
  fi
done

for module in \
  github.com/shirou/gopsutil/v4 \
  github.com/spf13/cobra \
  golang.org/x/sys; do
  version="$(go list -m -f '{{.Version}}' "${module}")"
  grep -F -- "\`${module}\` \`${version}\`" THIRD_PARTY_NOTICES.md >/dev/null
done

echo "PASS: third-party license bundle is complete"
