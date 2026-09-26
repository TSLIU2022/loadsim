#!/usr/bin/env bash
# LoadSim cron 后台托管：全天保活的启动包装，供 crontab 周期调用。
#
# 设计要点：
# - ensure 在 fill 没有运行时就启动，覆盖手动 stop、外部 kill、异常退出、
#   被 OOM 杀掉和机器重启；进程还在则立即退出；
# - setsid 让 fill 脱离调用方会话，退出 SSH 或关闭终端不会把它带走；
# - 状态日志按天写在 LOG_DIR，--status-interval-sec 建议 60 以上，日志量很小。
#
# 用法：
#   loadsim-cron.sh ensure   # crontab 调用：没在运行就拉起，平时立即退出
#   loadsim-cron.sh start    # 手动启动
#   loadsim-cron.sh stop     # 优雅停止：SIGTERM 后等待内存按释放速率归还
#   loadsim-cron.sh status   # 运行状态与最后一条状态行（退出码 0=运行中）
#
# crontab（由 install.sh 自动写入；root 系统级装到 root，--user 装到当前用户）：
#   */5 * * * * /usr/local/sbin/loadsim-cron ensure      # 系统级
#   */5 * * * * <安装目录>/bin/loadsim-cron ensure       # 用户级（--user）
#
# 配置按 环境变量 LOADSIM_ENV_FILE > /etc/loadsim/ > <安装目录>/config/ 顺序解析。

set -euo pipefail

# 二进制定位：环境变量 > 托管脚本同目录（用户级 <安装目录>/bin/loadsim）> 系统路径。
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
PREFIX="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
DEFAULT_BIN=/usr/local/bin/loadsim
[[ -x "${SCRIPT_DIR}/loadsim" ]] && DEFAULT_BIN="${SCRIPT_DIR}/loadsim"
BINARY="${LOADSIM_BIN:-${DEFAULT_BIN}}"

# 配置解析顺序：环境变量指定 > 系统级（root 安装）> 用户级（--user 安装）。
if [[ -n "${LOADSIM_ENV_FILE:-}" ]]; then
  ENV_FILE="${LOADSIM_ENV_FILE}"
elif [[ -r /etc/loadsim/loadsim.env ]]; then
  ENV_FILE=/etc/loadsim/loadsim.env
elif [[ -r "${PREFIX}/config/loadsim.env" ]]; then
  ENV_FILE="${PREFIX}/config/loadsim.env"
else
  ENV_FILE=""
fi

# 配置与 systemd 模式共用；不可读时使用内置默认值。
if [[ -n "${ENV_FILE}" && -r "${ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  . "${ENV_FILE}"
  set +a
fi

CPU_BAND="${LOADSIM_CPU_BAND:-55:60}"
MEMORY_BAND="${LOADSIM_MEMORY_BAND:-65:70}"
MEMORY_MAX="${LOADSIM_MEMORY_MAX_MIB:-2048}"
YIELD_POLICY="${LOADSIM_YIELD_POLICY:-gradual}"
STATUS_INTERVAL_SEC="${LOADSIM_STATUS_INTERVAL_SEC:-60}"
LOG_KEEP_DAYS="${LOADSIM_LOG_KEEP_DAYS:-30}"
STOP_TIMEOUT_SEC="${LOADSIM_STOP_TIMEOUT_SEC:-600}"

if [[ "$(id -u)" -eq 0 ]]; then
  RUN_DIR=/var/run/loadsim
  LOG_DIR=/var/log/loadsim
else
  RUN_DIR="${PREFIX}/run"
  LOG_DIR="${PREFIX}/log"
fi
PID_FILE="${RUN_DIR}/loadsim.pid"
LOCK_FILE="${RUN_DIR}/ensure.lock"

usage() {
  awk 'NR == 1 { next } /^#$/ { print ""; next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "${BASH_SOURCE[0]}"
  exit 0
}

fail() {
  printf 'loadsim-cron: %s\n' "$*" >&2
  exit 1
}

log_file() {
  printf '%s/fill-%s.log\n' "${LOG_DIR}" "$(date +%F)"
}

is_running() {
  local pid
  [[ -f "${PID_FILE}" ]] || return 1
  pid="$(cat "${PID_FILE}" 2>/dev/null || true)"
  [[ "${pid}" =~ ^[0-9]+$ ]] || return 1
  kill -0 "${pid}" 2>/dev/null
}

# 全局互斥：匹配任意安装路径下的 loadsim fill（包括其他账号的系统级部署），
# 防止两种部署方式或多个账号同时填充同一台机器。
any_fill_running() {
  pgrep -f "loadsim fill" >/dev/null 2>&1
}

# 当前账号启动的实例：优先 pidfile，其次兜底匹配手工启动、没有 pidfile 的进程。
find_running_pid() {
  if is_running; then
    cat "${PID_FILE}"
    return 0
  fi
  pgrep -U "$(id -u)" -f "loadsim fill" 2>/dev/null | head -n 1
}

acquire_lock() {
  if (set -o noclobber; echo $$ > "${LOCK_FILE}") 2>/dev/null; then
    trap 'rm -f "${LOCK_FILE}"' EXIT
    return 0
  fi
  # ensure 正常秒级退出；超过 10 分钟的锁视为残留。
  if [[ -n "$(find "${LOCK_FILE}" -mmin +10 2>/dev/null)" ]]; then
    rm -f "${LOCK_FILE}"
    if (set -o noclobber; echo $$ > "${LOCK_FILE}") 2>/dev/null; then
      trap 'rm -f "${LOCK_FILE}"' EXIT
      return 0
    fi
  fi
  return 1
}

do_start() {
  if [[ ! -x "${BINARY}" ]]; then
    fail "找不到可执行文件 ${BINARY}"
  fi
  mkdir -p "${RUN_DIR}" "${LOG_DIR}"
  if find_running_pid >/dev/null 2>&1; then
    echo "fill 已在运行 (pid $(find_running_pid))，无需重复启动"
    rm -f "${PID_FILE}"
    find_running_pid > "${PID_FILE}"
    return 0
  fi
  if any_fill_running; then
    fail "检测到其他账号/路径的 loadsim fill 正在运行，为避免双重填充拒绝启动。
（如需切换为当前部署托管，请先停止原实例）"
  fi
  find "${LOG_DIR}" -name 'fill-*.log' -mtime "+${LOG_KEEP_DAYS}" -delete 2>/dev/null || true

  # setsid 新建会话后在前台 exec fill，$! 就是 fill 的 pid。
  # 不使用 -f：带 -f 时 setsid 自己再 fork，shell 只能拿到 setsid 的 pid。
  setsid "${BINARY}" fill \
    --cpu "${CPU_BAND}" \
    --memory "${MEMORY_BAND}" \
    --memory-max-mib "${MEMORY_MAX}" \
    --yield-policy "${YIELD_POLICY}" \
    --duration-sec 0 \
    --status-interval-sec "${STATUS_INTERVAL_SEC}" \
    >>"$(log_file)" 2>&1 </dev/null &
  local pid=$!
  echo "${pid}" > "${PID_FILE}"
  # 本次运行的分隔标记：终止分类只看该标记之后的日志，避免上次运行的
  # stopped 行污染判断。
  echo "loadsim-cron: start pid=${pid} at $(date '+%F %T')" >>"$(log_file)"
  sleep 2
  if ! kill -0 "${pid}" 2>/dev/null; then
    rm -f "${PID_FILE}"
    fail "fill 启动失败，最近日志：
$(tail -n 5 "$(log_file)" 2>/dev/null || echo '(无日志)')"
  fi
  echo "已启动 fill (pid ${pid})：CPU ${CPU_BAND}，内存 ${MEMORY_BAND}，上限 ${MEMORY_MAX}，状态间隔 ${STATUS_INTERVAL_SEC}s"
}

do_stop() {
  local pid
  pid="$(find_running_pid || true)"
  if [[ -z "${pid}" ]]; then
    rm -f "${PID_FILE}"
    echo "fill 未在运行"
  else
    echo "发送 SIGTERM 到 ${pid}，等待内存按释放速率渐进归还…"
    kill -TERM "${pid}"
    local waited=0
    while kill -0 "${pid}" 2>/dev/null; do
      sleep 2
      waited=$((waited + 2))
      if (( waited >= STOP_TIMEOUT_SEC )); then
        echo "警告：${STOP_TIMEOUT_SEC}s 内尚未退出，进程可能仍在释放内存" >&2
        break
      fi
    done
    if kill -0 "${pid}" 2>/dev/null; then
      echo "进程 ${pid} 仍在退出过程中"
    else
      echo "已停止并归还资源（用时约 ${waited}s）"
    fi
    rm -f "${PID_FILE}"
  fi
}

do_ensure() {
  mkdir -p "${RUN_DIR}"
  acquire_lock || exit 0

  if is_running; then
    exit 0
  fi

  # 进程已不在：清掉失效 pidfile 后重新拉起。
  # 覆盖手动 stop、外部 kill、异常退出、OOM 和开机前遗留的 pidfile。
  rm -f "${PID_FILE}"

  # 兜底：本账号手工启动过、没有 pidfile 的实例视为仍在运行。
  if pgrep -U "$(id -u)" -f "loadsim fill" >/dev/null 2>&1; then
    find_running_pid > "${PID_FILE}"
    exit 0
  fi

  # 全局互斥：其他账号的系统级部署正在填充时保持沉默，不做双重填充。
  if any_fill_running; then
    exit 0
  fi

  do_start >/dev/null 2>&1 || true
}

	field() {
	  local key="$1" line="$2"
	  printf '%s\n' "${line}" | grep -oE "${key}=[^ ]+" | head -n 1 | cut -d= -f2-
	}
	
	do_status() {
	  local pid line
	  pid="$(find_running_pid || true)"
	  if [[ -n "${pid}" ]]; then
	    line="$(tail -n 1 "$(log_file)" 2>/dev/null || true)"
	    echo "运行中"
	    echo "进程: ${pid}"
	    echo "已运行: $(ps -o etime= -p "${pid}" 2>/dev/null | tr -d ' ')"
	    echo "CPU: 目标 $(field cpu_band "${line}")，当前 $(field cpu_observed "${line}")，LoadSim 贡献 $(field cpu_drive "${line}")"
	    echo "内存: 目标 $(field memory_band "${line}")，当前 $(field memory_observed "${line}")，LoadSim 占用 $(field memory_current "${line}") / 上限 $(field memory_cap "${line}")"
	    echo "可用内存: $(field memory_available "${line}")，安全水位 $(field memory_min_available "${line}")"
	    echo "日志: $(log_file)"
	    exit 0
	  fi
  if crontab -l 2>/dev/null | grep -qF "${SCRIPT_DIR}/$(basename "${BASH_SOURCE[0]}") ensure"; then
    echo "stopped: 未运行（crontab 保活仍在，下个周期会重新拉起）"
  else
    echo "stopped: 未运行（crontab 中没有保活任务，不会自动拉起）"
  fi
  exit 1
}

case "${1:-}" in
  ensure) do_ensure ;;
  start) do_start ;;
  stop) do_stop ;;
  status) do_status ;;
  -h|--help|help) usage ;;
  "") usage ;;
  *) fail "未知子命令: $1（-h 查看用法）" ;;
esac
