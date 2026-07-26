#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 1 ]]; then
  echo "用法: $0 <输出文件>" >&2
  exit 1
fi

output_file="$1"
output_dir="$(dirname "${output_file}")"
module_list="$(mktemp "${TMPDIR:-/tmp}/loadsim-modules.XXXXXX")"
generated_file=""

cleanup() {
  rm -f -- "${module_list}"
  if [[ -n "${generated_file}" ]]; then
    rm -f -- "${generated_file}"
  fi
}
trap cleanup EXIT

mkdir -p "${output_dir}"
generated_file="$(mktemp "${output_dir}/.loadsim-licenses.XXXXXX")"

export LC_ALL=C
export GOWORK=off

go_root="$(go env GOROOT)"
go_version="$(go env GOVERSION)"
go_license="${go_root}/LICENSE"
go_patents="${go_root}/PATENTS"
for required_file in "${go_license}" "${go_patents}"; do
  if [[ ! -f "${required_file}" ]]; then
    echo "Go 工具链缺少必需的授权文件: ${required_file}" >&2
    exit 1
  fi
done

go list -deps \
  -f '{{with .Module}}{{if not .Main}}{{printf "%s\t%s\t%s" .Path .Version .Dir}}{{end}}{{end}}' \
  . |
  awk -F '\t' 'NF == 3 && !seen[$1]++' |
  sort >"${module_list}"

if [[ ! -s "${module_list}" ]]; then
  echo "没有发现二进制使用的第三方 Go 模块。" >&2
  exit 1
fi

{
  printf '%s\n\n' \
    'LoadSim 第三方许可证汇编' \
    '本文件由 scripts/generate_third_party_licenses.sh 根据目标平台实际链接的 Go 运行时、标准库和第三方模块生成。'

  printf '%s\n' \
    '================================================================================' \
    '组件: Go 运行时与标准库' \
    "版本: ${go_version}" \
    '--------------------------------------------------------------------------------' \
    '文件: LICENSE' \
    ''
  sed -e '$a\' "${go_license}"
  printf '%s\n\n' '文件: PATENTS' ''
  sed -e '$a\' "${go_patents}"
  printf '\n'

  while IFS=$'\t' read -r module_path module_version module_dir; do
    license_files=()
    for candidate in \
      "${module_dir}"/LICENSE* \
      "${module_dir}"/LICENCE* \
      "${module_dir}"/COPYING* \
      "${module_dir}"/NOTICE* \
      "${module_dir}"/PATENTS* \
      "${module_dir}"/COPYRIGHT*; do
      if [[ -f "${candidate}" ]]; then
        license_files+=("${candidate}")
      fi
    done

    if [[ "${#license_files[@]}" -eq 0 ]]; then
      echo "依赖 ${module_path} ${module_version} 缺少可识别的许可证文件。" >&2
      exit 1
    fi

    printf '%s\n' \
      '================================================================================' \
      "模块: ${module_path}" \
      "版本: ${module_version}" \
      '--------------------------------------------------------------------------------'
    for license_file in "${license_files[@]}"; do
      printf '文件: %s\n\n' "$(basename "${license_file}")"
      sed -e '$a\' "${license_file}"
    done
    printf '\n'
  done <"${module_list}"
} >"${generated_file}"

chmod 0644 "${generated_file}"
mv -f -- "${generated_file}" "${output_file}"
