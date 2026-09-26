#!/usr/bin/env bash
# LoadSim 一键部署脚本。两种托管方式按账号权限二选一：
#
#   systemd 模式（默认，需要 root）
#     sudo ./install.sh                       # 使用脚本同目录下的 loadsim 二进制
#     sudo ./install.sh /tmp/loadsim          # 指定二进制路径（也支持发布 tar.gz）
#     sudo ./install.sh --mode cron           # root 下的 cron 变体（一般优先 systemd）
#     产物：/usr/local/bin/loadsim、/etc/loadsim/loadsim.env、
#           /etc/systemd/system/loadsim.service、/usr/local/sbin/{loadsim-report,loadsim-cron}
#
#   一般账号模式（--user，无需任何 sudo）
#     发布包解压后就是安装目录：bin/ config/ run/ log/
#     ./bin/install.sh --user                 # 就地启用：写 config、装卸载脚本、启动
#     ./bin/install.sh --user --prefix /客户目录  # 可选，复制到已存在的客户目录
#
#   公共参数：
#     --acceptance-sec 0    # 跳过验收（systemd 前台验收 / cron 快速验收）
#     --no-enable           # 安装并验收，但不启用
#     --cpu-band 55:60 --memory-band 65:70 --yield-policy gradual --memory-max 2048
# 卸载使用同目录的 uninstall.sh，不使用本脚本。
#
# 重跑本脚本会先停掉已有实例，清掉目标路径上的旧程序、旧配置和旧 crontab 行，
# 再按本次参数重新安装并验收。历史日志保留。

set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

MODE=systemd
USER_MODE=0
INSTALL_BIN=/usr/local/bin/loadsim
ENV_DIR=/etc/loadsim
ENV_FILE="${ENV_DIR}/loadsim.env"
UNIT_FILE=/etc/systemd/system/loadsim.service
	REPORT_BIN=/usr/local/sbin/loadsim-report
	UNINSTALL_BIN=""
	CRON_BIN=/usr/local/sbin/loadsim-cron
CRON_LINE="*/5 * * * * ${CRON_BIN} ensure"

CPU_BAND=55:60
MEMORY_BAND=65:70
YIELD_POLICY=gradual
MEMORY_MAX=2048
ACCEPTANCE_SEC=600
STATUS_INTERVAL_SEC=5
STATUS_SET=0
ENABLE_SERVICE=1
	BINARY=""
PREFIX=""

usage() {
  awk 'NR == 1 { next } /^#$/ { print ""; next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "${BASH_SOURCE[0]}"
  exit 0
}

fail() {
  printf 'install.sh: %s\n' "$*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
	    --user) USER_MODE=1 ;;
    --prefix) PREFIX="${2:?missing value}"; shift ;;
    --mode) MODE="${2:?missing value}"; shift ;;
    --cpu-band) CPU_BAND="${2:?missing value}"; shift ;;
    --memory-band) MEMORY_BAND="${2:?missing value}"; shift ;;
    --yield-policy) YIELD_POLICY="${2:?missing value}"; shift ;;
    --memory-max) MEMORY_MAX="${2:?missing value}"; shift ;;
    --acceptance-sec) ACCEPTANCE_SEC="${2:?missing value}"; shift ;;
    --status-interval-sec) STATUS_INTERVAL_SEC="${2:?missing value}"; STATUS_SET=1; shift ;;
    --no-enable) ENABLE_SERVICE=0 ;;
    -h|--help) usage ;;
    -*) fail "未知参数: $1（--help 查看用法）" ;;
    *) BINARY="$1" ;;
  esac
  shift
done

	# 用户级安装只支持 cron 托管。
	# 未指定 --prefix 时：install.sh 在 bin/ 下就装它所在的安装目录，否则装当前目录。
	if [[ "${USER_MODE}" -eq 1 ]]; then
	  MODE=cron
	  if [[ -z "${PREFIX}" ]]; then
	    if [[ "$(basename "${SCRIPT_DIR}")" == "bin" && -x "${SCRIPT_DIR}/loadsim" ]]; then
	      PREFIX="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
	    else
	      PREFIX="$(pwd)"
	    fi
	  elif [[ "${PREFIX}" != /* ]]; then
    PREFIX="$(pwd)/${PREFIX}"
  fi
  PREFIX="$(cd -- "${PREFIX}" && pwd)"
  INSTALL_BIN="${PREFIX}/bin/loadsim"
  ENV_DIR="${PREFIX}/config"
  ENV_FILE="${ENV_DIR}/loadsim.env"
	  REPORT_BIN="${PREFIX}/bin/loadsim-report"
	  UNINSTALL_BIN="${PREFIX}/bin/uninstall.sh"
	  CRON_BIN="${PREFIX}/bin/loadsim-cron"
  CRON_LINE="*/5 * * * * ${CRON_BIN} ensure"
fi

case "${MODE}" in
  systemd|cron) ;;
  *) fail "--mode 只支持 systemd 或 cron" ;;
esac
# cron 托管全天后台执行，状态日志默认降频到 60s 一条。
if [[ "${MODE}" == "cron" && "${STATUS_SET}" -eq 0 ]]; then
  STATUS_INTERVAL_SEC=60
fi

install_dir() {
  local dir="$1" mode="$2"
  if [[ "${USER_MODE}" -eq 1 ]]; then
    mkdir -p "${dir}"
    chmod "${mode}" "${dir}"
  else
    install -d -o root -g root -m "${mode}" "${dir}"
  fi
}

install_file() {
  local src="$1" dst="$2" mode="$3"
  if [[ "${USER_MODE}" -eq 1 ]]; then
    install -m "${mode}" "${src}" "${dst}"
  else
    install -o root -g root -m "${mode}" "${src}" "${dst}"
  fi
}

	need_root() {
	  [[ "$(id -u)" -eq 0 ]] || fail "请用 root 运行（sudo ./install.sh ...）"
	}
	
if [[ "${USER_MODE}" -eq 1 ]]; then
  if [[ "$(id -u)" -eq 0 ]]; then
    echo "警告：当前是 root；--user 会安装到 ${PREFIX} 并由 root 的 crontab 保活，一般账号部署请用普通账号执行。" >&2
  fi
else
  need_root
fi
[[ "$(uname -s)" == "Linux" ]] || fail "LoadSim 只支持 Linux"
if [[ "${MODE}" == "systemd" ]]; then
  command -v systemctl >/dev/null 2>&1 || fail "未找到 systemctl，本机可用 --user 改用一般账号 cron 托管"
else
  command -v crontab >/dev/null 2>&1 || fail "未找到 crontab 命令，无法安装 cron 托管"
fi

	# 解析二进制位置：显式参数 > 脚本同目录的 loadsim > 发布 tar.gz。
	if [[ -n "${BINARY}" && "${BINARY}" == *.tar.gz ]]; then
	  TARBALL="${BINARY}"
	  BINARY="$(mktemp -d /tmp/loadsim-install.XXXXXX)/loadsim"
	  echo "解压 ${TARBALL} -> ${BINARY}"
	  tar -xzf "${TARBALL}" -C "$(dirname "${BINARY}")" --strip-components=1 \
	    --wildcards '*/bin/loadsim' '*/loadsim'
	elif [[ -z "${BINARY}" ]]; then
	  for candidate in "${SCRIPT_DIR}/loadsim" "${SCRIPT_DIR}/../loadsim"; do
	    if [[ -x "${candidate}" && -f "${candidate}" ]]; then
	      BINARY="${candidate}"
	      break
	    fi
	  done
	  [[ -n "${BINARY}" ]] ||
	    fail "未找到 loadsim 二进制，请将其路径（或发布 tar.gz）作为参数传入"
	fi
	[[ -n "${BINARY}" && -f "${BINARY}" ]] || fail "未找到 loadsim 二进制，请将其路径作为参数传入"
	canon_path() {
	  local dir
	  dir="$(cd -- "$(dirname -- "$1")" 2>/dev/null && pwd)" || return 1
	  printf '%s/%s\n' "${dir}" "$(basename -- "$1")"
	}
	same_path() {
	  local left right
	  left="$(canon_path "$1")" || return 1
	  right="$(canon_path "$2")" || return 1
	  [[ "${left}" == "${right}" ]]
	}
	
	# 发布包里脚本在 bin/ 且不带 .sh。仓库里仍是 deploy/*.sh。
	resolve_deploy_script() {
	  local name="$1" bare="${1%.sh}" candidate
	  for candidate in \
	    "${SCRIPT_DIR}/${bare}" \
	    "${SCRIPT_DIR}/${name}" \
	    "${SCRIPT_DIR}/../deploy/${name}" \
	    "${SCRIPT_DIR}/../${name}"; do
	    [[ -f "${candidate}" ]] && {
	      printf '%s\n' "${candidate}"
	      return 0
	    }
	  done
	  return 1
	}

install_crontab_entry() {
  local tmp
  tmp="$(mktemp)"
  { crontab -l 2>/dev/null | grep -vF "${CRON_BIN}" || true; echo "${CRON_LINE}"; } >"${tmp}"
  crontab "${tmp}"
  rm -f "${tmp}"
}

# 目标路径上已有安装时，先停进程并清掉旧程序、旧配置和旧保活行。
# 日志目录保留。之后按本次参数重新写入，避免升级继续沿用失效配置。
remove_crontab_entry() {
  local needle="$1" tmp
  command -v crontab >/dev/null 2>&1 || return 0
  tmp="$(mktemp)"
  { crontab -l 2>/dev/null | grep -vF "${needle}" || true; } >"${tmp}"
  crontab "${tmp}" 2>/dev/null || true
  rm -f "${tmp}"
}

stop_existing_cron() {
  local cron_bin="$1"
  if [[ -n "${cron_bin}" && -x "${cron_bin}" ]]; then
    echo "停止已有 cron 托管：${cron_bin}"
    "${cron_bin}" stop >/dev/null 2>&1 || true
  fi
}

clean_existing_install() {
  local removed=0
  if [[ "${USER_MODE}" -eq 1 ]]; then
    local legacy_home="${HOME}/.local/bin/loadsim-cron"
    stop_existing_cron "${CRON_BIN}"
    if [[ "${legacy_home}" != "${CRON_BIN}" ]]; then
      stop_existing_cron "${legacy_home}"
    fi
    remove_crontab_entry "loadsim-cron"
	    if [[ -e "${ENV_FILE}" || -e "${legacy_home}" ]]; then
	      echo "清理已有安装：${PREFIX}/config（${PREFIX}/log 保留）"
	      removed=1
	    elif [[ -e "${INSTALL_BIN}" || -e "${REPORT_BIN}" || -e "${CRON_BIN}" ]]; then
	      echo "目标目录已有程序，按本次参数重写配置"
	      removed=1
	    fi
	    # 来源就是目标文件时不能删，否则后面无文件可装。
	    if [[ -e "${INSTALL_BIN}" ]] && ! same_path "${BINARY}" "${INSTALL_BIN}"; then
	      rm -f "${INSTALL_BIN}"
	    fi
	    if [[ -e "${REPORT_BIN}" ]] && ! { REPORT_KEEP="$(resolve_deploy_script loadsim-report.sh)" && same_path "${REPORT_KEEP}" "${REPORT_BIN}"; }; then
	      rm -f "${REPORT_BIN}"
	    fi
	    if [[ -e "${CRON_BIN}" ]] && ! { CRON_KEEP="$(resolve_deploy_script loadsim-cron.sh)" && same_path "${CRON_KEEP}" "${CRON_BIN}"; }; then
	      rm -f "${CRON_BIN}"
	    fi
	    rm -f "${ENV_FILE}" \
	      "${ENV_DIR}"/loadsim.env.bak-* \
	      "${HOME}/.local/bin/loadsim" \
	      "${HOME}/.local/bin/loadsim-report" \
	      "${legacy_home}" \
	      "${HOME}/.config/loadsim/loadsim.env"
    rmdir "${ENV_DIR}" "${HOME}/.config/loadsim" "${HOME}/.local/bin" 2>/dev/null || true
  else
    if [[ "${MODE}" == "systemd" ]] && systemctl is-active --quiet loadsim.service 2>/dev/null; then
      echo "停止已有 loadsim.service"
      systemctl stop loadsim.service || true
    fi
    stop_existing_cron "${CRON_BIN}"
    if [[ -e "${UNIT_FILE}" ]]; then
      systemctl disable --now loadsim.service >/dev/null 2>&1 || true
      rm -f "${UNIT_FILE}"
      systemctl daemon-reload || true
      echo "已移除旧单元 ${UNIT_FILE}"
      removed=1
    fi
    remove_crontab_entry "${CRON_BIN}"
    if [[ -e "${INSTALL_BIN}" || -e "${REPORT_BIN}" || -e "${CRON_BIN}" || -e "${ENV_FILE}" ]]; then
      echo "清理已有安装：${INSTALL_BIN}、${ENV_FILE}（/var/log/loadsim/ 保留）"
      removed=1
    fi
    rm -f "${INSTALL_BIN}" "${REPORT_BIN}" "${CRON_BIN}" "${ENV_FILE}"
    rmdir "${ENV_DIR}" 2>/dev/null || true
  fi
  if [[ "${removed}" -eq 0 ]]; then
    echo "目标路径没有已有安装，直接安装"
  fi
}

WAS_ACTIVE=0
clean_existing_install

VERSION="$("${BINARY}" version 2>/dev/null | awk '{print $2}')"
[[ -n "${VERSION}" ]] || fail "无法从 ${BINARY} 读取版本号"

		install_dir "$(dirname "${INSTALL_BIN}")" 0755
		if same_path "${BINARY}" "${INSTALL_BIN}"; then
		  echo "二进制已在 ${INSTALL_BIN} (LoadSim ${VERSION})"
		else
		  echo "安装二进制 ${BINARY} -> ${INSTALL_BIN} (LoadSim ${VERSION})"
		  install_file "${BINARY}" "${INSTALL_BIN}" 0755
		fi
		
		if REPORT_SRC="$(resolve_deploy_script loadsim-report.sh)"; then
		  install_dir "$(dirname "${REPORT_BIN}")" 0755
		  if same_path "${REPORT_SRC}" "${REPORT_BIN}"; then
		    echo "巡检脚本已在 ${REPORT_BIN}"
		  else
		    echo "安装巡检脚本 ${REPORT_SRC} -> ${REPORT_BIN}"
		    install_file "${REPORT_SRC}" "${REPORT_BIN}" 0755
		  fi
		else
		  echo "未找到 loadsim-report，跳过巡检脚本安装（不影响填充服务）"
		fi
	
		if [[ "${USER_MODE}" -eq 1 ]]; then
		  if UNINSTALL_SRC="$(resolve_deploy_script uninstall.sh)"; then
		    if same_path "${UNINSTALL_SRC}" "${UNINSTALL_BIN}"; then
		      echo "卸载脚本已在 ${UNINSTALL_BIN}"
		    else
		      echo "安装卸载脚本 ${UNINSTALL_SRC} -> ${UNINSTALL_BIN}"
		      install_file "${UNINSTALL_SRC}" "${UNINSTALL_BIN}" 0755
		    fi
		  else
		    echo "未找到 uninstall.sh，跳过卸载脚本安装"
		  fi
		fi

	# 已有配置在清理阶段删除，这里始终按本次参数写入。
	if [[ -f "${ENV_FILE}" ]]; then
	  echo "配置 ${ENV_FILE} 在清理后仍存在，覆盖为本次安装参数"
	else
	  echo "写入配置 ${ENV_FILE}"
	fi
	install_dir "${ENV_DIR}" 0750
	cat > "${ENV_FILE}" <<EOF
# LoadSim 生产填充目标：整机 CPU 约 ${CPU_BAND%:*}-${CPU_BAND#*:}%，整机内存约 ${MEMORY_BAND%:*}-${MEMORY_BAND#*:}%。
LOADSIM_CPU_BAND=${CPU_BAND}
LOADSIM_MEMORY_BAND=${MEMORY_BAND}
LOADSIM_YIELD_POLICY=${YIELD_POLICY}
LOADSIM_STATUS_INTERVAL_SEC=${STATUS_INTERVAL_SEC}
# LoadSim 0.7.1 只接受正整数 MiB。需要按机器调整时直接改这个数。
LOADSIM_MEMORY_MAX_MIB=${MEMORY_MAX}
EOF
	if [[ "${USER_MODE}" -eq 1 ]]; then
	  chmod 0600 "${ENV_FILE}"
	else
	  chmod 0640 "${ENV_FILE}"
	fi

if [[ "${MODE}" == "cron" ]]; then
  # ---------------- cron 托管模式 ----------------
		if CRON_SRC="$(resolve_deploy_script loadsim-cron.sh)"; then
		    install_dir "$(dirname "${CRON_BIN}")" 0755
		    if same_path "${CRON_SRC}" "${CRON_BIN}"; then
		      echo "cron 托管脚本已在 ${CRON_BIN}"
		    else
		      echo "安装 cron 托管脚本 ${CRON_SRC} -> ${CRON_BIN}"
		      install_file "${CRON_SRC}" "${CRON_BIN}" 0755
		    fi
		  else
		    fail "未找到 loadsim-cron，无法安装 cron 托管"
		  fi

  if [[ "${ENABLE_SERVICE}" -eq 1 ]]; then
    if [[ "${ACCEPTANCE_SEC}" -gt 0 ]]; then
      echo
      echo "=== cron 模式快速验收：启动 fill 并观察 15s 存活 ==="
      "${CRON_BIN}" start
      sleep 15
      if ! "${CRON_BIN}" status; then
        fail "cron 模式验收失败：fill 未存活。请查看日志目录（root: /var/log/loadsim/，用户级: ${PREFIX}/log/）与 ${ENV_FILE} 配置"
      fi
    fi
    install_crontab_entry
    echo
    echo "已写入 crontab（$(id -un)）：${CRON_LINE}"
    if [[ "${USER_MODE}" -eq 1 ]] && [[ ":${PATH}:" != *":${PREFIX}/bin:"* ]]; then
      echo
      echo "提示：${PREFIX}/bin 不在 PATH 中，请加入 ~/.bashrc："
      echo "  echo 'export PATH=\"${PREFIX}/bin:\$PATH\"' >> ~/.bashrc && source ~/.bashrc"
    fi
    echo
    echo "部署完成。fill 全天后台执行；常用命令："
    echo "  ${CRON_BIN} status   # 运行状态与最后一条状态行"
    echo "  ${CRON_BIN} stop     # 优雅停止（释放内存；下个 cron 周期会重新拉起）"
	    echo "  ${CRON_BIN} start    # 立即启动"
	    if [[ "${USER_MODE}" -eq 1 ]]; then
	      echo "  ${PREFIX}/bin/uninstall.sh   # 卸载（保留 ${PREFIX}/log）"
	    fi
    if [[ "${USER_MODE}" -eq 1 ]]; then
      echo "  ${REPORT_BIN} --file ${PREFIX}/log/fill-\$(date +%F).log   # 效果报告（可选）"
    else
      echo "  ${REPORT_BIN} --file /var/log/loadsim/fill-\$(date +%F).log  # 效果报告（可选）"
    fi
  else
    echo
    echo "已安装但未启用（--no-enable）：未写入 crontab、未启动。"
    echo "启用：重跑本脚本（去掉 --no-enable），或手动 '${CRON_BIN} start' 并自行安装 crontab"
  fi
  exit 0
fi

# ---------------- systemd 托管模式 ----------------
echo "渲染 systemd 单元 ${UNIT_FILE}"
cat > "${UNIT_FILE}" <<EOF
[Unit]
Description=LoadSim resource fill controller
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
DynamicUser=yes
EnvironmentFile=${ENV_FILE}
ExecStart=${INSTALL_BIN} fill --cpu \${LOADSIM_CPU_BAND} --memory \${LOADSIM_MEMORY_BAND} --memory-max-mib \${LOADSIM_MEMORY_MAX_MIB} --yield-policy \${LOADSIM_YIELD_POLICY} --duration-sec 0 --status-interval-sec \${LOADSIM_STATUS_INTERVAL_SEC}
Restart=always
RestartSec=30s
TimeoutStopSec=10min
CPUWeight=1
OOMScoreAdjust=1000
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=full

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "${UNIT_FILE}"
systemctl daemon-reload

if [[ "${ACCEPTANCE_SEC}" -gt 0 ]]; then
  echo
  echo "=== 前台验收 ${ACCEPTANCE_SEC}s（使用 ${ENV_FILE} 的配置，结束后自动启用服务） ==="
  ACCEPT_LOG="$(mktemp /tmp/loadsim-acceptance.XXXXXX.log)"
  set -a
  # shellcheck disable=SC1090
  . "${ENV_FILE}"
  set +a
  EXIT_CODE=0
  "${INSTALL_BIN}" fill \
    --cpu "${LOADSIM_CPU_BAND}" \
    --memory "${LOADSIM_MEMORY_BAND}" \
    --memory-max-mib "${LOADSIM_MEMORY_MAX_MIB}" \
    --yield-policy "${LOADSIM_YIELD_POLICY}" \
    --duration-sec "${ACCEPTANCE_SEC}" \
    --status-interval-sec "${LOADSIM_STATUS_INTERVAL_SEC}" \
    2>&1 | tee "${ACCEPT_LOG}" || EXIT_CODE=$?
  if [[ "${EXIT_CODE}" -ne 0 ]]; then
    rm -f "${ACCEPT_LOG}"
    fail "前台验收失败（退出码 ${EXIT_CODE}），已安装文件但不会启用服务；请先排查后再手动 systemctl enable --now loadsim"
  fi
  if [[ -x "${REPORT_BIN}" ]]; then
    echo
    "${REPORT_BIN}" --file "${ACCEPT_LOG}" || true
  fi
  rm -f "${ACCEPT_LOG}"
fi

if [[ "${ENABLE_SERVICE}" -eq 1 ]]; then
  echo
  echo "启用并启动 loadsim.service"
  systemctl enable --now loadsim.service
  systemctl --no-pager --lines=0 status loadsim.service || true
  echo
  echo "部署完成。常用命令："
  echo "  systemctl status loadsim            # 服务状态"
  echo "  journalctl -u loadsim -f            # 实时状态行"
  echo "  loadsim-report --since '10 min ago' # 运行效果报告"
elif [[ "${WAS_ACTIVE}" -eq 1 ]]; then
  echo
  echo "注意：服务升级前在运行，已按 --no-enable 要求保持停止状态。"
  echo "确认无误后执行：sudo systemctl start loadsim"
else
  echo
  echo "已安装但未启用。确认配置后执行：sudo systemctl enable --now loadsim"
fi
