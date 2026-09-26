#!/usr/bin/env bash
# LoadSim 卸载脚本。
#
#   一般账号（不要加 sudo）：
#     <安装目录>/bin/uninstall.sh
#     ./uninstall.sh --prefix /客户目录
#
#   root 的 systemd 安装才需要 root：
#     sudo ./uninstall.sh
#
# 停止 fill，删除程序、配置、systemd 单元和当前用户 crontab 保活行。
# 用户级保留 <prefix>/log；root 保留 /var/log/loadsim。

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
MODE=systemd
USER_MODE=0
INSTALL_BIN=/usr/local/bin/loadsim
ENV_DIR=/etc/loadsim
ENV_FILE="${ENV_DIR}/loadsim.env"
UNIT_FILE=/etc/systemd/system/loadsim.service
REPORT_BIN=/usr/local/sbin/loadsim-report
CRON_BIN=/usr/local/sbin/loadsim-cron
PREFIX=""

usage() {
  awk 'NR == 1 { next } /^#$/ { print ""; next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "${BASH_SOURCE[0]}"
  exit 0
}

fail() {
  printf 'uninstall.sh: %s\n' "$*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --user) USER_MODE=1 ;;
    --prefix) PREFIX="${2:?missing value}"; shift ;;
    --mode) MODE="${2:?missing value}"; shift ;;
    -h|--help) usage ;;
    *) fail "未知参数: $1（--help 查看用法）" ;;
  esac
  shift
done

# 从安装目录的 bin/uninstall.sh 执行时，直接卸载该目录，不需要 root。
if [[ "${USER_MODE}" -eq 0 && -z "${PREFIX}" && "$(basename "${SCRIPT_DIR}")" == "bin" && -x "${SCRIPT_DIR}/loadsim-cron" ]]; then
  USER_MODE=1
  PREFIX="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
fi

if [[ "${USER_MODE}" -eq 1 || -n "${PREFIX}" ]]; then
  USER_MODE=1
  MODE=cron
  if [[ -z "${PREFIX}" ]]; then
    PREFIX="$(pwd)"
  elif [[ "${PREFIX}" != /* ]]; then
    PREFIX="$(pwd)/${PREFIX}"
  fi
  PREFIX="$(cd -- "${PREFIX}" && pwd)"
  INSTALL_BIN="${PREFIX}/bin/loadsim"
  ENV_DIR="${PREFIX}/config"
  ENV_FILE="${ENV_DIR}/loadsim.env"
  REPORT_BIN="${PREFIX}/bin/loadsim-report"
  CRON_BIN="${PREFIX}/bin/loadsim-cron"
else
  [[ "$(id -u)" -eq 0 ]] || fail "未指定安装目录。普通账号执行 <安装目录>/bin/uninstall.sh，或 ./uninstall.sh --prefix /客户目录"
fi

case "${MODE}" in
  systemd|cron) ;;
  *) fail "--mode 只支持 systemd 或 cron" ;;
esac

remove_crontab_entry() {
  local needle="$1" tmp
  command -v crontab >/dev/null 2>&1 || return 0
  tmp="$(mktemp)"
  { crontab -l 2>/dev/null | grep -vF "${needle}" || true; } >"${tmp}"
  crontab "${tmp}" 2>/dev/null || true
  rm -f "${tmp}"
}

if [[ -x "${CRON_BIN}" ]]; then
  echo "停止 ${CRON_BIN}"
  "${CRON_BIN}" stop >/dev/null 2>&1 || true
fi

if [[ "${USER_MODE}" -eq 1 ]]; then
  legacy_home="${HOME}/.local/bin/loadsim-cron"
  if [[ "${legacy_home}" != "${CRON_BIN}" && -x "${legacy_home}" ]]; then
    echo "停止 ${legacy_home}"
    "${legacy_home}" stop >/dev/null 2>&1 || true
  fi
  remove_crontab_entry "loadsim-cron"
  rm -f "${INSTALL_BIN}" "${REPORT_BIN}" "${CRON_BIN}" "${ENV_FILE}" \
    "${ENV_DIR}"/loadsim.env.bak-* \
    "${HOME}/.local/bin/loadsim" \
    "${HOME}/.local/bin/loadsim-report" \
    "${legacy_home}" \
    "${HOME}/.config/loadsim/loadsim.env"
  rmdir "${ENV_DIR}" "${PREFIX}/bin" "${HOME}/.config/loadsim" "${HOME}/.local/bin" 2>/dev/null || true
  echo "已卸载 ${PREFIX}（${PREFIX}/log 保留）"
  exit 0
fi

remove_crontab_entry "${CRON_BIN}"
if [[ "${MODE}" == "systemd" ]] && command -v systemctl >/dev/null 2>&1; then
  systemctl disable --now loadsim.service >/dev/null 2>&1 || true
  if [[ -e "${UNIT_FILE}" ]]; then
    rm -f "${UNIT_FILE}"
    systemctl daemon-reload || true
    echo "已移除 ${UNIT_FILE}"
  fi
fi
rm -f "${INSTALL_BIN}" "${REPORT_BIN}" "${CRON_BIN}" "${ENV_FILE}"
rmdir "${ENV_DIR}" 2>/dev/null || true
echo "已卸载 ${INSTALL_BIN} 和 ${ENV_FILE}（/var/log/loadsim 保留）"
