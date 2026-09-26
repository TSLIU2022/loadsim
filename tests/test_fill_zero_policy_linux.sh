#!/usr/bin/env bash
set -euo pipefail

binary="${1:-/usr/local/bin/loadsim}"
if [[ "${binary}" == */* ]]; then
  binary_dir="${binary%/*}"
  binary_name="${binary##*/}"
else
  binary_dir="."
  binary_name="${binary}"
fi
test_dir="$(mktemp -d "${TMPDIR:-/tmp}/loadsim-zero-policy.XXXXXX")"

cleanup() {
  local status=$?
  if [[ -n "${cpu_fill_pid:-}" ]]; then
    kill "${cpu_fill_pid}" 2>/dev/null || true
    wait "${cpu_fill_pid}" 2>/dev/null || true
  fi
  if [[ -n "${cpu_stress_pid:-}" ]]; then
    kill "${cpu_stress_pid}" 2>/dev/null || true
    wait "${cpu_stress_pid}" 2>/dev/null || true
  fi
  rm -rf -- "${test_dir}"
  exit "${status}"
}
trap cleanup EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

require() {
  command -v "$1" >/dev/null || fail "missing required command: $1"
}

wait_for_log() {
  local path=$1
  local expression=$2
  local deadline=$((SECONDS + 20))
  while (( SECONDS < deadline )); do
    if grep -Eq -- "${expression}" "${path}"; then
      return 0
    fi
    sleep 1
  done
  cat "${path}" >&2 || true
  fail "timed out waiting for ${expression} in ${path}"
}

wait_for_new_log() {
  local path=$1
  local line_count=$2
  local expression=$3
  local deadline=$((SECONDS + 20))
  while (( SECONDS < deadline )); do
    if tail -n "+$((line_count + 1))" "${path}" | grep -Eq -- "${expression}"; then
      return 0
    fi
    sleep 1
  done
  cat "${path}" >&2 || true
  fail "timed out waiting for a new ${expression} in ${path}"
}

[[ "$(uname -s)" == "Linux" ]] || fail "this integration test requires Linux"
[[ "${EUID}" -eq 0 ]] || fail "run as root so the memory test can use a transient systemd scope"
[[ -x "${binary}" ]] || fail "LoadSim binary is not executable: ${binary}"
binary_dir="$(cd -P -- "${binary_dir}" && pwd)" || fail "could not resolve LoadSim binary directory"
binary="${binary_dir}/${binary_name}"
require grep
require python3
require systemd-run
require systemctl
require tail
require wc
require nproc
[[ "$(stat -fc %T /sys/fs/cgroup)" == "cgroup2fs" ]] || fail "this integration test requires cgroup v2"

cpu_band="$(
  "${binary}" check --active --json --sample-ms 500 |
    python3 -c '
import json
import math
import sys

observed = json.load(sys.stdin)["cpu"]["observed_percent"]
low = math.ceil(observed + 10)
high = low + 20
if high > 80:
    raise SystemExit(
        f"host baseline {observed:.1f}% leaves no safe CPU test band"
    )
print(f"{low}:{high}")
'
)" || fail "could not derive a CPU test band from the active host baseline"
memory_band="$(
  "${binary}" check --active --json --sample-ms 500 |
    python3 -c '
import json
import math
import sys

for capacity in json.load(sys.stdin)["memory"]:
    if capacity["source"] == "host":
        observed = capacity["used_mib"] * 100 / capacity["total_mib"]
        low = math.ceil(observed + 10)
        high = low + 20
        if high > 80:
            raise SystemExit(
                f"host memory baseline {observed:.1f}% leaves no safe test band"
            )
        print(f"{low}:{high}")
        break
else:
    raise SystemExit("active check did not report host memory capacity")
'
)" || fail "could not derive a memory test band from the active host baseline"

printf 'Memory test band: %s%%\n' "${memory_band}"

printf 'CPU test band: %s%%\n' "${cpu_band}"
systemctl is-system-running --wait >/dev/null 2>&1 || true

cpu_log="${test_dir}/cpu-fill.log"
"${binary}" fill \
  --cpu "${cpu_band}" \
  --yield-policy zero \
  --cpu-control-ms 1000 \
  --cpu-sample-ms 500 \
  --cpu-max-step 100 \
  --duration-sec 25 \
  --status-interval-sec 1 \
  >"${cpu_log}" 2>&1 &
cpu_fill_pid=$!
wait_for_log "${cpu_log}" 'cpu_drive=[1-9][0-9]*\.?[0-9]*%'
cpu_pressure_start_line="$(wc -l < "${cpu_log}")"
"${binary}" stress \
  --cpu-percent 100 \
  --cpu-cores "$(nproc)" \
  --duration-sec 8 \
  >"${test_dir}/cpu-stress.log" 2>&1 &
cpu_stress_pid=$!
wait_for_new_log "${cpu_log}" "${cpu_pressure_start_line}" 'cpu_drive=0\.0%'
wait "${cpu_stress_pid}"
unset cpu_stress_pid
cpu_recovery_start_line="$(wc -l < "${cpu_log}")"
wait_for_new_log "${cpu_log}" "${cpu_recovery_start_line}" 'cpu_drive=[1-9][0-9]*\.?[0-9]*%'
wait "${cpu_fill_pid}"
unset cpu_fill_pid

memory_log="${test_dir}/memory-fill.log"
memory_scope_script="${test_dir}/memory-scope.sh"
cat >"${memory_scope_script}" <<'EOF'
set -euo pipefail

log="${LOADSIM_MEMORY_LOG}"
"${LOADSIM_BINARY}" fill \
  --memory "${LOADSIM_MEMORY_BAND}" \
  --memory-max-mib 128 \
  --yield-policy zero \
  --memory-control-ms 500 \
  --memory-grow-mib-per-sec 128 \
  --memory-release-mib-per-sec 1 \
  --duration-sec 20 \
  --status-interval-sec 1 \
  >"${log}" 2>&1 &
fill_pid=$!
cleanup_scope() {
  kill "${fill_pid}" 2>/dev/null || true
  wait "${fill_pid}" 2>/dev/null || true
}
trap cleanup_scope EXIT

wait_for() {
  local expression=$1
  local deadline=$((SECONDS + 15))
  while (( SECONDS < deadline )); do
    if grep -Eq -- "${expression}" "${log}"; then
      return 0
    fi
    sleep 1
  done
  cat "${log}" >&2 || true
  return 1
}

wait_for_new_log() {
  local line_count=$1
  local expression=$2
  local deadline=$((SECONDS + 15))
  while (( SECONDS < deadline )); do
    if tail -n "+$((line_count + 1))" "${log}" | grep -Eq -- "${expression}"; then
      return 0
    fi
    sleep 1
  done
  cat "${log}" >&2 || true
  return 1
}

wait_for "memory_current=[1-9][0-9]*MiB"
pressure_start_line="$(wc -l < "${log}")"
python3 -c "import time; block = bytearray(400 * 1024 * 1024); [block.__setitem__(offset, 1) for offset in range(0, len(block), 4096)]; time.sleep(6)" &
pressure_pid=$!
wait_for_new_log "${pressure_start_line}" "memory_current=0MiB"
wait "${pressure_pid}"
recovery_start_line="$(wc -l < "${log}")"
wait_for_new_log "${recovery_start_line}" "memory_current=[1-9][0-9]*MiB"
EOF

systemd-run \
  --wait \
  --setenv="LOADSIM_BINARY=${binary}" \
  --setenv="LOADSIM_MEMORY_LOG=${memory_log}" \
  --setenv="LOADSIM_MEMORY_BAND=${memory_band}" \
  --collect \
  --quiet \
  --property=MemoryMax=1G \
  --property=MemorySwapMax=0 \
  /bin/bash "${memory_scope_script}"

printf 'PASS: CPU and memory zero-yield controllers cleared and recovered\n'
