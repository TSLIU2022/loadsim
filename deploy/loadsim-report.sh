#!/usr/bin/env bash
# LoadSim 运行效果报告：把状态行汇总成人能直接读的结论（达标率、极值、余量）。
#
# 用法：
#   ./loadsim-report.sh                                  # 读本次开机以来 systemd 服务的全部状态行
#   ./loadsim-report.sh --since '10 min ago'             # 只看最近 10 分钟
#   ./loadsim-report.sh --file acceptance.log            # 分析 install.sh 前台验收日志
#   cat status.log | ./loadsim-report.sh --file -        # 从管道读取
#   ./loadsim-report.sh --since '10 min ago' --fail-below 90
#       # 巡检模式：CPU 或内存达标率低于 90% 时以非零退出，可直接放进 cron
#
# 读 systemd 日志需要能访问 journal 的账号（root 或 systemd-journal 组）。

set -euo pipefail

SINCE=""
FILE=""
FAIL_BELOW=""

usage() {
  sed -n '2,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --since) SINCE="${2:?missing value}"; shift ;;
    --file) FILE="${2:?missing value}"; shift ;;
    --fail-below) FAIL_BELOW="${2:?missing value}"; shift ;;
    -h|--help) usage ;;
    *) echo "未知参数: $1（--help 查看用法）" >&2; exit 2 ;;
  esac
  shift
done

# 从状态行流中汇总。awk 输出 TSV：
# 样本数 首时间 末时间 cpu样本 cpu达标 cpu_min cpu_avg cpu_max cpu_low cpu_high
# mem样本 mem达标 mem_min mem_avg mem_max mem_low mem_high rss峰值 可用内存最低
aggregate() {
  awk '
    function num(text) { gsub(/[^0-9.:-]/, "", text); return text }
    {
      split("", v)
      split($1, t, /[\[\]]/); ts = t[2]
      for (i = 2; i <= NF; i++) {
        pos = index($i, "=")
        if (pos > 1) v[substr($i, 1, pos - 1)] = substr($i, pos + 1)
      }
      if (v["mode"] != "fill" && v["mode"] != "stress") next
      if (lines == 0) first = ts
      last = ts; lines++

      if (v["cpu_band"] != "" && !has_cpuband) {
        split(num(v["cpu_band"]), cb, /:/); has_cpuband = 1
      }
      if (v["memory_band"] != "" && !has_memband) {
        split(num(v["memory_band"]), mb, /:/); has_memband = 1
      }
      if (v["cpu_observed"] != "") {
        c = num(v["cpu_observed"]) + 0
        if (!has_cpu) { cmin = c; has_cpu = 1 }
        if (c < cmin) cmin = c
        if (c > cmax) cmax = c
        csum += c; cn++
        if (cb[1] != "" && c + 0 >= cb[1] + 0 && c + 0 <= cb[2] + 0) cok++
      }
      if (v["memory_observed"] != "" && v["memory_requested"] + 0 > 0) {
        m = num(v["memory_observed"]) + 0
        if (!has_mem) { mmin = m; has_mem = 1 }
        if (m < mmin) mmin = m
        if (m > mmax) mmax = m
        msum += m; mn++
        if (mb[1] != "" && m + 0 >= mb[1] + 0 && m + 0 <= mb[2] + 0) mok++
      }
      if (v["process_rss"] != "") {
        r = num(v["process_rss"]) + 0
        if (r > rssmax) rssmax = r
      }
      if (v["memory_available"] != "") {
        a = num(v["memory_available"]) + 0
        if (!has_avail || a < availmin) { availmin = a; has_avail = 1 }
      }
    }
    END {
      printf "%d\t%s\t%s\t", lines, first, last
      if (has_cpu) printf "%d\t%d\t%.1f\t%.1f\t%.1f\t%.1f\t%.1f\t", cn, cok, cmin, csum / cn, cmax, cb[1] + 0, cb[2] + 0
      else printf "0\t0\t0\t0\t0\t0\t0\t"
      if (has_mem) printf "%d\t%d\t%.1f\t%.1f\t%.1f\t%.1f\t%.1f\t", mn, mok, mmin, msum / mn, mmax, mb[1] + 0, mb[2] + 0
      else printf "0\t0\t0\t0\t0\t0\t0\t"
      printf "%d\t%d\n", rssmax, (has_avail ? availmin : -1)
    }
  '
}

if [[ -n "${FILE}" ]]; then
  if [[ "${FILE}" == "-" ]]; then
    RESULTS="$(aggregate)"
  else
    [[ -r "${FILE}" ]] || { echo "无法读取文件: ${FILE}" >&2; exit 2; }
    RESULTS="$(aggregate < "${FILE}")"
  fi
else
  JOURNAL_ARGS=(journalctl -u loadsim.service -o cat --no-pager)
  [[ -n "${SINCE}" ]] && JOURNAL_ARGS+=(--since "${SINCE}")
  if ! RESULTS="$("${JOURNAL_ARGS[@]}" 2>/dev/null | aggregate)"; then
    echo "无法读取 journal（尝试 sudo，或用 --file 指定日志文件）" >&2
    exit 2
  fi
fi

if [[ -z "${RESULTS//[$'\t' ]/}" ]]; then
  echo "没有读到任何状态行。" >&2
  exit 2
fi

IFS=$'\t' read -r LINES FIRST LAST CN COK CMIN CAVG CMAX CLOW CHIGH MN MOK MMIN MAVG MMAX MLOW MHIGH RSSMAX AVAMIN <<<"${RESULTS}"

echo "LoadSim 运行效果报告"
if [[ "${LINES}" -eq 0 ]]; then
  echo "样本: 0 条状态行${SINCE:+（范围: ${SINCE}）}"
  echo "结论: 无数据。请确认服务已启动、时间范围正确。"
  exit 2
fi
echo "样本: ${LINES} 条状态行（${FIRST} ~ ${LAST}）"

EXIT_CODE=0
ANY_RATIO_LOW=0
verdict() {
  local name="$1" inband="$2" total="$3" low="$4" high="$5" vmin="$6" vavg="$7" vmax="$8"
  local ratio=0
  [[ "${total}" -gt 0 ]] && ratio=$((100 * inband / total))
  local threshold="${FAIL_BELOW:-90}"
  local state="达标"
  if [[ "${ratio}" -lt "${threshold}" ]]; then
    state="未达标"
    ANY_RATIO_LOW=1
  elif [[ "${ratio}" -lt 100 ]]; then
    state="基本达标"
  fi
  if [[ -n "${FAIL_BELOW}" && "${ratio}" -lt "${FAIL_BELOW}" ]]; then
    EXIT_CODE=1
  fi
  printf '%s 目标 %s%%–%s%% 观测 min/avg/max = %s%%/%s%%/%s%% 达标 %s/%s (%s%%) [%s]\n' \
    "${name}" "${low}" "${high}" "${vmin}" "${vavg}" "${vmax}" \
    "${inband}" "${total}" "${ratio}" "${state}"
}

if [[ "${CN}" -gt 0 ]]; then
  verdict "CPU " "${COK}" "${CN}" "${CLOW}" "${CHIGH}" "${CMIN}" "${CAVG}" "${CMAX}"
fi
if [[ "${MN}" -gt 0 ]]; then
  verdict "内存" "${MOK}" "${MN}" "${MLOW}" "${MHIGH}" "${MMIN}" "${MAVG}" "${MMAX}"
fi

if [[ "${RSSMAX}" -gt 0 ]]; then
  printf 'LoadSim 进程 RSS 峰值 %sMiB' "${RSSMAX}"
  if [[ "${AVAMIN}" -ge 0 ]]; then
    printf '，系统可用内存最低 %sMiB' "${AVAMIN}"
  fi
  printf '\n'
fi

if [[ -n "${FAIL_BELOW}" ]]; then
  if [[ "${EXIT_CODE}" -eq 0 ]]; then
    echo "结论: 正常（达标率 ≥ ${FAIL_BELOW}%）"
  else
    echo "结论: 需要关注（达标率低于 ${FAIL_BELOW}%）"
  fi
elif [[ "${ANY_RATIO_LOW}" -eq 1 ]]; then
  echo "提示: 达标率包含启动爬坡阶段；评估稳态效果请用 --since 只取运行中后段。"
fi
exit "${EXIT_CODE}"
