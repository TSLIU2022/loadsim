# LoadSim

[![CI](https://github.com/fanderchan/loadsim/actions/workflows/ci.yml/badge.svg)](https://github.com/fanderchan/loadsim/actions/workflows/ci.yml)

LoadSim（负载场景模拟器）是一个面向 Linux 的命令行工具，用来构造可控的 CPU、RAM 或 CPU+RAM 组合负载。它适合验证监控告警、观察资源曲线和演练容量阈值，不是性能基准测试工具。

当前提供三个子命令：

- `loadsim cpu`：固定或周期波动的 CPU 负载
- `loadsim ram`：固定、周期波动或按总使用率区间自适应的 RAM 占用
- `loadsim combo`：同时运行 CPU 和 RAM 场景

> [!WARNING]
> LoadSim 会主动消耗真实资源。请先在隔离环境中验证，并为目标主机或容器保留足够余量。除非已有变更窗口、监控和止损措施，否则不要直接在生产环境运行。

## 系统要求

- Linux `amd64` 或 `arm64`
- 从源码编译需要 Go `1.25.12` 或更高版本

运行 LoadSim 本身通常不需要 root 权限；只有安装到 `/usr/local/bin` 等系统目录时可能需要 `sudo`。

## 安装

### 从 GitHub Release 安装

[GitHub Releases](https://github.com/fanderchan/loadsim/releases) 提供 Linux `amd64` 和 `arm64` 压缩包，以及对应的 `checksums.txt`。下面的 Bash 示例会下载最新正式版、校验 SHA-256 并安装：

```bash
set -euo pipefail

VERSION="$(
  curl -fsSL -o /dev/null -w '%{url_effective}' \
    https://github.com/fanderchan/loadsim/releases/latest
)"
VERSION="${VERSION##*/}"

case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

ARCHIVE="loadsim_${VERSION#v}_linux_${ARCH}.tar.gz"
BASE_URL="https://github.com/fanderchan/loadsim/releases/download/${VERSION}"

curl -fLO "${BASE_URL}/${ARCHIVE}"
curl -fLO "${BASE_URL}/checksums.txt"
sed 's#  dist/#  #' checksums.txt |
  grep -F "  ${ARCHIVE}" |
  sha256sum -c -

tar -xzf "${ARCHIVE}"
sudo install -m 0755 \
  "loadsim_${VERSION#v}_linux_${ARCH}/loadsim" \
  /usr/local/bin/loadsim

loadsim version
```

如果要安装 beta 版，请在[发布页](https://github.com/fanderchan/loadsim/releases)选择标签，并手动把 `VERSION` 设为该标签，例如 `VERSION=v1.0.0-beta.1`。

版本演进摘要见 [CHANGELOG.md](CHANGELOG.md)，每个版本的完整说明和构建产物以 GitHub Releases 为准。

发布工作流还会为两个压缩包生成使用短期 Sigstore 证书签名的 GitHub 构建来源证明。安装了 GitHub CLI 时，可以在下载目录验证“文件摘要是否与该仓库的发布工作流一致”：

```bash
gh attestation verify "${ARCHIVE}" -R fanderchan/loadsim
```

来源证明能识别产物被替换或来自其他仓库，但不能证明程序本身没有缺陷；它不能替代 SHA-256 校验、代码审查和上线前回归。

### 从源码编译

```bash
git clone https://github.com/fanderchan/loadsim.git
cd loadsim
go version
go build -o loadsim .
./loadsim version
```

也可以运行仓库中的构建脚本：

```bash
./build.sh
./build/loadsim version
```

## 快速开始

以下命令未指定 `--time` 时都会在 60 秒后自动停止。

让 4 个 worker 的总体负载达到 50%：

```bash
loadsim cpu --scope workers --percent 50 --cores 4
```

把当前可见系统边界的总 CPU 使用率调向 30%：

```bash
loadsim cpu --scope system --percent 30
```

占用 256MB RAM：

```bash
loadsim ram --size 256
```

让 RAM 在 64MB 和 256MB 之间波动，并把变化速度限制为每秒 16MB：

```bash
loadsim ram \
  --mode wave \
  --min-size 64 \
  --max-size 256 \
  --period 120 \
  --rate-limit 16
```

让总内存使用率在 30%–50% 闭区间内保持，LoadSim 自身最多分配有效内存的 30%，增长速度限制为每秒 128MB：

```bash
loadsim ram \
  --mode adaptive \
  --min-percent 30 \
  --max-percent 50 \
  --max-load-percent 30 \
  --adaptive-ms 3000 \
  --rate-limit 128
```

同时制造 CPU 和 RAM 负载：

```bash
loadsim combo \
  --cpu-scope workers \
  --cpu-percent 30 \
  --cpu-cores 2 \
  --ram-size 256
```

只有显式指定 `--time 0` 才会无限运行；使用 `Ctrl+C`、`SIGINT` 或 `SIGTERM` 停止：

```bash
loadsim ram --size 256 --time 0
```

## CPU 目标语义

### `scope=workers`

`workers` 是默认范围。百分比作用于 LoadSim 所选 worker 在当前调度约束下的有效容量，不代表宿主机整体百分比。

例如：

```bash
loadsim cpu --scope workers --percent 50 --cores 4
```

这表示 4 个 worker 的有效容量总体使用约 50%；没有额外限制时，理论上最多贡献约 2 个逻辑 CPU 的计算量。LoadSim 可能把负载集中到部分 worker，而不是让每个 worker 都保持相同占空比；它在 8 核宿主机和 64 核宿主机上对应的整机占比也不同。

如果 cgroup 只给进程 `0.5 CPU`，默认的 1 个 worker 使用 `--percent 50` 时会把实际驱动缩放到约 `0.25 CPU`，不会把 `0.5 CPU` 配额全部吃满。显式 `--cores` 少于进程容量时，有效容量也不会超过这些 worker 自己最多能提供的 CPU 数。

### `scope=system`

`system` 是生产区间控制的推荐口径。它读取当前进程可见的 cgroup CPU 记账根：

- 在普通物理机或 VM 上，这个根通常覆盖整台机器上的全部进程。
- 在启用了 cgroup namespace 的容器里，这个根通常就是容器自身的 CPU 边界。
- 如果容器运行时把宿主机 cgroup 根直接暴露进容器，它表示宿主机边界；以状态中的 `cpu_source`、`cpu_boundary` 和 `scope_cpus` 为准。

控制器用 `cpu.stat`（cgroup v2）或 `cpuacct.usage`（cgroup v1）计算该边界内所有任务实际获得的 CPU 时间，并按边界根的 quota、cpuset 和在线 CPU 数确定分母。业务变忙时总量会上升，LoadSim 会降低自己的驱动力度；业务释放后再渐进补充。计数器倒退、采样期间容量变化、可见边界容量变化或统计无法包含 LoadSim 自身用量时都会失败关闭。

`system` 只能观察它所处的可见记账边界。把 LoadSim 放进一个与业务完全隔离、且拥有独立 cgroup namespace 的容器后，它看不到兄弟容器的业务用量，也就不能维持兄弟容器所在更大边界的总使用率。要控制宿主机总量，应从宿主机运行 LoadSim；要控制单个业务容器总量，LoadSim 必须与业务处于同一可见 CPU 记账边界。

### `scope=host`

`host` 是为兼容旧用法保留的 `/proc/stat` 口径。它在物理机上通常是本机 CPU，在虚拟机里是该虚拟机看到的 vCPU；在容器里通常仍是宿主机 CPU 视图，而不是容器 CPU 配额。某些虚拟化内核的 `/proc/stat` 会明显少记低优先级线程的 CPU 时间，因此新部署优先使用 `system`。

CPU affinity、`GOMAXPROCS`、`--cores` 和 cgroup v1/v2 CPU quota 都可能限制当前进程能够使用的 CPU 容量。LoadSim 会按这些限制估算自身最多能贡献多少，但不会把“自身最大贡献”误当作“边界总量最大可达值”：已有业务负载也属于 `host` / `system` 总量的一部分。

例如，一个只获得 2 个 CPU 配额的进程运行在 64 核宿主机上时，LoadSim 自身最多只贡献约 3.125 个百分点；如果业务原本已经使用 47%，两者仍可能共同进入 50% 附近。业务很空闲时则无法单独达到 50%，控制器会停在有效驱动力上限，而不是突破 quota。如果想表达“占用进程可用 worker 容量的 50%”，应使用 `--scope workers --percent 50`。

`--cores=0` 是默认值，表示按 affinity、`GOMAXPROCS` 和 cgroup quota 共同决定的进程可用 CPU 容量选择 worker 数，不代表越过这些限制使用宿主机全部核心。显式指定的 worker 数超过进程可用上限时也会启动失败。

`scope_contribution_max` 是 LoadSim 自身的调度容量上界，不是总量目标或精度承诺。CPU 争用、虚拟化 steal time 或未暴露给进程的额外限流仍可能让实际值低于目标；控制器最多把 `drive` 推到当前有效配额对应的上限，不能突破内核或平台限制。

为避免错误的 CPU 统计让控制器持续加压，`host` 和 `system` 都会把边界忙碌时间与 LoadSim 自身的进程 CPU 时间交叉校验。连续两个有效窗口出现“不可能成立”的结果时，命令会立即把驱动力降为零、停止 worker，并以非零状态退出。这个保护主要覆盖虚拟化、容器统计口径或内核采样异常，不代表 CPU 目标一定能精确达到。

运行期间还会每秒重新检查逻辑 CPU 数、affinity、`GOMAXPROCS` 和 cgroup CPU quota。容器编排器或 systemd 动态修改其中任一项时，LoadSim 不会拿旧容量继续加压，而是立即停止 CPU worker 并报错退出；确认新限制后应重新启动命令。

### worker 优先级与空闲策略

CPU worker 默认以 Linux `nice=19` 运行，让普通优先级的业务线程更容易抢回 CPU。可用 `--worker-nice 0..19` 调整，组合模式对应 `--cpu-worker-nice`；`inherit` 会继承启动进程的优先级并取消这层默认让步，只适合明确了解调度影响的场景。

`nice` 只影响 Linux 调度器中的相对竞争关系，不能替代 cgroup 的 CPU 权重和硬限制。尤其当 LoadSim 与业务位于不同容器或 cgroup 时，生产环境仍应把 LoadSim 放到独立低权重 cgroup、systemd scope 或容器中。

- `--idle-mode park`：保留 worker 池，让暂时空闲的 worker 休眠；这是默认值，适合稳定或频繁变化的负载。
- `--idle-mode trim`：回收不需要的 worker；空闲 footprint 更小，但波动时会增加创建和回收。

`--control-ms`、`--sample-ms`、`--deadband` 和 `--max-step` 用于调整 `host` / `system` 控制器的响应速度与抖动。建议先使用默认值，再根据实际曲线微调。

固定模式的 `--percent` 是死区中点，`--deadband` 是中点两侧各自的宽度。例如 `--percent 40 --deadband 10` 表示总 CPU 在 30%–50% 闭区间内时保持当前驱动力，低于 30% 才补充，高于 50% 才让出。`--max-step` 限制每轮增减幅度，避免控制器因短时抖动反复大幅调整。

## RAM 目标语义

LoadSim 在 Linux 上使用匿名 `mmap` 创建内存映射，并逐页写入以实际触碰页面。缩容和停止时使用 `munmap` 解除映射，因此下降阶段会真实归还相应映射，而不是只减少 Go 对象的逻辑计数。

固定和波动模式下，`--rate-limit` 同时限制增长和下降速度。实现会按真实经过时间累计额度，并保留不足 1MB 的小数额度；只有累计到完整 1MB 才执行调整。到达目标后不会囤积可用于下一次变化的突发额度。`0` 表示不限速，负数会被拒绝。

`--block-size` 控制单个映射块的大小。较小的块有利于细粒度变化，但会增加映射数量和系统调用；一般先使用默认值。

### `mode=adaptive`

自适应模式控制的是当前进程可见的总内存使用率，不是“LoadSim 进程自身 RSS 百分比”。它同时观察宿主机和所有可见的有限 cgroup 约束：

- 只有所有约束都低于 `--min-percent`，并且连续两个采样均成立时，才向区间中点渐进补充。
- 任一约束高于 `--max-percent`，首个采样就向区间中点释放；释放不受增长限速影响。
- 所有约束都在闭区间内时保持，不反复申请和释放。
- `--max-load-percent` 是 LoadSim 自身的硬上限；默认 30%，即使系统完全空闲也不会让 LoadSim 映射超过最小有效内存边界的 30%。
- 每次增长前都会重新验证完整内存安全预算；探测失败或越过安全水位时仍会释放全部映射并非零退出。

自适应模式要求 `--rate-limit` 为正数，只用它限制增长；拒绝 `--force`，并把 `--block-size` 限制在 1–64MB。`--adaptive-ms` 是慢速区间控制周期，默认 3 秒；独立的 `--memory-check-ms` 仍以默认 100ms 执行紧急安全检查。允许的区间上限不超过 80%，上下限至少相差 5 个百分点。

## 安全默认值

- `cpu`、`ram` 和 `combo` 的默认运行时间都是 60 秒。
- `ram` 和 `combo` 的默认 RAM 固定目标都是 256MB。
- `--time 0` 是唯一表示不自动停止的值；负数会报错。
- 子命令不接受位置参数，拼错或多写参数会报错，不会静默忽略。
- CPU worker 默认使用 `nice=19`；`host` / `system` 模式发现 CPU 统计口径与自身用量连续矛盾时会失败关闭并停止 worker。
- RAM 启动前和运行中都会逐项检查宿主机以及所有可见的 cgroup 内存约束，不会只按“绝对可用量最小”折叠成一个口径；波动模式的启动预算按 `--max-size` 检查。
- RAM 自适应模式从 0MB 启动，先运行安全护栏，再经两个低水位样本确认后增长；最坏自身占用上限也必须通过启动预算检查。
- 自动安全水位是有效总内存的 10%，通常不少于 128MB；小容量环境最多保留总量的 25%，大内存机器不设固定上限。启动预算还会在水位之外额外保留分配块和抖动余量，避免正常达到目标后立即自触发。
- 运行期默认每 100ms 检查一次。可用内存达到或低于水位、或者探测失败时，命令会失败关闭：`ram` 立即解除全部映射，`combo` 先释放 RAM，再停止 CPU。紧急释放不受 `--rate-limit` 限制。
- `ram` 和 `combo` 默认把自身 `oom_score_adj` 设为 `1000` 并读回校验，使 LoadSim 在真正发生 OOM 时优先成为牺牲对象。

`--memory-min-available` 可以显式指定水位（MB），`0` 使用上述自动策略；`--memory-check-ms` 调整采样周期，最小值是 10ms，避免过密读取 `/proc` 和 cgroup 文件本身造成额外开销。固定和波动模式下，`ram --force` 与 `combo --force` 只绕过启动目标预算，不会关闭初始水位、运行期水位或探测失败保护；自适应模式拒绝 `--force`。受限容器如果明确需要继承外部 OOM 策略，可使用 `--oom-score-adj -1`；`0` 到 `1000` 表示写入对应值，写入或读回失败会拒绝启动，不会静默降级。

## 状态输出

状态行采用便于脚本读取的 `key=value` 格式。示意：

```text
[12:00:00] ram mode=wave desired=128MB target=96MB current=96MB block=16MB rate_limit=16MB/s memory_scope=cgroup(memory.max) memory=41.0% memory_guard_scope=host memory_available=15200MB memory_min_available=13108MB memory_check=100ms oom_score_adj=1000 process_rss=101MB
[12:00:02] cpu mode=fixed scope=system idle=park worker_nice=19 target=40.0% band=30.0-50.0% drive=18.0% workers=2/4 host_cpus=4 process_cpus=4.00 scope_cpus=4.00 scope_contribution_max=100.0% cpu_source=cgroup1:cpuacct.usage cpu_boundary=ancestor:cgcpu-0123456789abcdef0123456789abcdef scope_cpu=39.4% memory_scope=host memory=38.2% process_rss=7MB
```

RAM 的三个目标字段含义不同：

- `desired`：固定模式下的用户目标、波动模式当前时刻的理想目标，或自适应控制器最新请求的映射量。
- `target`：应用 `--rate-limit` 后，本轮准备达到的目标。
- `current`：已经成功 `mmap` 的 LoadSim 内存量。

常见通用字段：

- CPU 状态中的 `target` / 组合状态中的 `cpu_target`：当前 CPU 请求目标。
- `band` / `cpu_band`：当前 CPU 总量控制闭区间；固定模式下，`40 ± 10` 会直接显示为 `30.0-50.0%`。
- `drive` / `cpu_drive`：当前施加到 worker 集合的占空比。
- `workers`：活跃 worker 数 / 可用 worker 数。
- `worker_nice` / `cpu_worker_nice`：CPU worker 的 Linux nice 值；`19` 最愿意向普通优先级任务让步。
- `host_cpus`：当前 Linux CPU 视图的逻辑 CPU 数。
- `process_cpus`：综合 affinity、`GOMAXPROCS` 和 cgroup quota 后，LoadSim 进程当前最多可用的等效 CPU 数；它可以是小数。
- `scope_cpus`：CPU 目标边界的等效容量；`system` 会同时考虑可见 cgroup 根的 quota 和 cpuset，`host` 使用 `/proc/stat` 的逻辑 CPU 数。
- `scope_contribution_max`：按当前 worker 数、`process_cpus` 和 `scope_cpus` 计算出的 LoadSim 自身最大贡献比例。它不是当前边界总量的最大可达值；已有业务负载也会计入总量目标。
- `cpu_source`：CPU 目标数据源，常见值是 `workers`、`proc-stat`、`cgroup1:cpuacct.usage` 或 `cgroup2:cpu.stat`。
- `cpu_boundary`：`system` 口径下不泄露 cgroup 路径的边界类型和稳定短 ID，例如 `ancestor:cgcpu-...` 或 `namespace-root:cgcpu-...`；运行中 ID、类型、来源或容量发生变化都会失败关闭。`host` / `workers` 显示 `none`。
- `scope_cpu`：仅在 `scope=host` / `system` 且控制器已有通过一致性校验的样本时出现，表示当前目标边界的总使用率。RAM 和 `scope=workers` 不输出这个字段。
- `memory_scope`：内存统计口径，`host` 表示宿主机，`cgroup(memory.max)`、`cgroup(memory.high)`、`cgroup(memory.limit_in_bytes)` 或 `cgroup(memory.memsw.limit_in_bytes)` 表示对应类型的有效 cgroup 约束；该约束可能来自当前层级的祖先。为避免泄露部署路径，状态不打印具体 cgroup 路径。
- `memory`：对应 `memory_scope` 口径的内存使用率。
- `memory_guard_scope`：在本次护栏采样中最接近触发边界的约束，并使用与 `memory_scope` 相同的类型标识。它可能与仅用于展示使用率的 `memory_scope` 不同。
- `memory_available`：`memory_guard_scope` 当前可用内存。
- `memory_min_available`：`memory_guard_scope` 对应的安全水位。程序仍会独立检查其余所有宿主机和 cgroup 约束。
- `memory_check`：运行期内存护栏采样周期。
- `band` / `ram_band`：自适应 RAM 的总使用率闭区间。
- `band_action` / `ram_band_action`：本轮是确认低水位、增长、保持还是释放。
- `band_scope` / `ram_band_scope` 与 `band_memory` / `ram_band_memory`：本轮最需要关注的同一次容量采样口径和使用率。
- `band_target` / `ram_band_target`：自适应控制器本轮请求的 LoadSim 映射量；`hard_cap` / `ram_hard_cap` 是自身绝对上限。
- `oom_score_adj`：实际请求的 OOM 调整值；`inherit` 表示显式继承外部策略。
- `process_rss`：LoadSim 进程的实际 RSS，包含程序自身开销，不只包含压力映射。

`current` 是 LoadSim 成功建立的映射量，`process_rss` 才是内核报告的整个进程驻留集，两者不要求完全相等。如果主要系统状态采样失败，状态行仍会保留核心控制字段并省略观测字段；如果只有某个附加指标不可读，展示值可能回退到 `host` 口径或显示为 `0`。这种展示回退不会放宽安全判断：CPU 控制和 RAM 护栏所需的探测失败时仍会停止负载并返回非零状态。

## 容器、OOM 与内存观测边界

- CPU 的 `system` 以当前可见 cgroup 记账根为边界，并用同一边界的 quota/cpuset 约束分母；`host` 则使用 `/proc/stat`。物理机、虚拟机和容器看到的根可能不同，必须以 `cpu_source`、`cpu_boundary`、`scope_cpus` 和小目标实测确认。混合或拆分 cgroup 无法证明用量与容量属于同一边界时会拒绝启动。
- CPU quota 与 RAM 护栏都从 `/proc/self/mountinfo` 解析真实 cgroup 挂载，支持常见的 v1、v2、hybrid、bind mount 和 cgroup namespace；不会假定控制器固定挂在 `/sys/fs/cgroup`。明确存在 membership 却无法读取对应控制器时会失败关闭。
- RAM 会沿层级检查祖先约束；cgroup v2 同时考虑 `memory.max` 和更保守的 `memory.high`。安全判断逐项应用各自的自动百分比水位和显式水位，避免“宿主机绝对可用量较多、但已经低于宿主机百分比水位”这类漏判。`memory_scope` 只是状态使用率的展示口径，实际护栏以所有约束为准。
- 自适应 RAM 的增长要求宿主机和所有可见 cgroup 约束同时低于下限；任一约束超过上限都会触发部分释放。v1 中大于宿主机容量的近似无限上限不会扩大 LoadSim 自身硬上限，因为最终取所有可见容量中的最小值。
- 匿名映射会逐页触碰，并使用每次映射独立的随机盐给每页写入不同标记，避免同一进程或多个 LoadSim 实例的页面被 KSM 合并；映射同时标记为不进入 core dump，避免大目标在异常崩溃时制造同等大小的转储文件。Linux 仍可能把页面换出到 swap，因此 RSS、cgroup memory 和 LoadSim 的映射量仍不要求完全一致。
- `munmap` 能解除映射，但监控系统的采样周期、缓存和指标聚合可能让图表延迟显示下降。
- 运行期护栏、`oom_score_adj` 和启动预算只能降低误伤概率，无法保证阻止突发 OOM；内核仍可能直接终止进程，使其来不及打印停止原因。
- CPU 调度、宿主机其他负载和虚拟化噪声都会影响瞬时精度；请按一段时间的曲线评估，不要把单次采样当作保证值。

如果 `host` 模式报告 `CPU accounting mismatch`，说明当前环境的 `/proc/stat` 无法完整解释本进程实际消耗的 CPU 时间。不要放宽校验；优先改用 `system`。如果 `system` 也失败或它的可见根不是你想控制的业务边界，应改用 `workers` 加外部 cgroup/编排控制，不能继续声称维持总量区间。

## 生产动态填充命令

下面两条命令把 40% 作为区间中点，把 10 个百分点作为 CPU 死区，因此实际目标区间是 30%–50%。`scope=system` 以当前可见 cgroup 根作为“总量”边界：物理机/VM 通常是整机，cgroup namespace 容器通常是该容器。上线前必须先从小目标确认状态中的 `cpu_source`、`cpu_boundary`、`scope_cpus` 与预期边界一致。

仅动态填充 CPU：

```bash
loadsim cpu \
  --mode fixed \
  --scope system \
  --percent 40 \
  --deadband 10 \
  --control-ms 3000 \
  --sample-ms 1000 \
  --max-step 5 \
  --worker-nice 19 \
  --idle-mode park \
  --time 0 \
  --status-interval 5
```

同时动态填充 CPU 和内存：

```bash
loadsim combo \
  --cpu-mode fixed \
  --cpu-scope system \
  --cpu-percent 40 \
  --cpu-deadband 10 \
  --cpu-control-ms 3000 \
  --cpu-sample-ms 1000 \
  --cpu-max-step 5 \
  --cpu-worker-nice 19 \
  --cpu-idle-mode park \
  --ram-mode adaptive \
  --ram-min-percent 30 \
  --ram-max-percent 50 \
  --ram-max-load-percent 30 \
  --ram-adaptive-ms 3000 \
  --ram-rate-limit 64 \
  --ram-block-size 16 \
  --memory-check-ms 100 \
  --oom-score-adj 1000 \
  --time 0 \
  --status-interval 5
```

CPU worker 的 `nice=19` 让普通优先级业务更容易即时抢占调度时间，3 秒控制器负责慢速把总使用率拉回区间；RAM 最多以 64MB/s 渐进增长，高于上限时释放不受该增长限速影响，会直接向中点回收。区间控制不等于硬隔离，必须配合下一节的 cgroup、容器或 systemd 资源边界。

如果 `scope_contribution_max` 很小，30%–50% 总量区间是否能达到取决于已有业务负载；长期低于下限且 `drive` 已到有效上限，表示 LoadSim 的贡献不足。不要用扩大权限或取消 quota 的方式盲目绕过。`--scope workers --percent 30` 可以作为让步型兜底，但它只表示 LoadSim 自身占用可用 worker 容量的 30%，不能声称维持系统总 CPU 30%–50%。

## 生产环境建议

LoadSim 内置的是最后一道自我保护，不是业务资源编排器。CPU 依靠 `nice=19` 和 `system` 控制器让步；自适应 RAM 每 3 秒做区间调整，独立安全护栏每 100ms 检测并在危险时紧急 `munmap`。突发负载、内核 OOM、调度延迟或监控采样延迟仍可能先于程序反应。

生产使用至少再加一层操作系统硬边界：

- 控制物理机/VM 总量：从宿主系统运行 LoadSim，并放入独立、低 `CPUWeight` 的 systemd service/scope；这样它仍能读取根 cgroup 总量，但调度上优先让业务。
- 控制单个容器总量：只有 LoadSim 与业务看到同一个 cgroup 记账根时，`system` 才能闭环控制。单独的兄弟容器通常只能看到自身，不能感知业务容器；这种部署应由宿主机或编排平台提供上层总量指标。
- 内存仍要设置外部 `MemoryHigh` / `MemoryMax` 止损，但注意：LoadSim 会把自己可见的 cgroup 内存限制也作为自适应约束，限制过小会让命令保守拒绝或提前停止。
- 先在同版本内核和同类运行环境做小目标试运行，再逐步提高；设置外部 watchdog、告警和一键停止，不要无人值守地使用 `--time 0`。
- 内存目标要按业务峰值而不是当前空闲量计算。`--force` 不能关闭运行期护栏，但会放宽启动预算，不建议作为常规生产参数。

如果目标只是改善资源利用率考核，优先让平台调度器回收或超卖可抢占资源。LoadSim 更适合短时、受控的容量演练；没有独立 cgroup/容器硬限制时，不应把它当作长期生产资源填充器。

上线前应在同类机器上按 CPU、RAM、组合的顺序串行试跑；每一步正常退出并确认没有残留进程后再继续：

```bash
set -euo pipefail

loadsim cpu --scope workers --percent 10 --cores 1 --time 30
! pgrep -x loadsim

loadsim ram --size 128 --time 30
! pgrep -x loadsim

loadsim combo \
  --cpu-scope workers --cpu-percent 10 --cpu-cores 1 \
  --ram-size 128 --time 30
! pgrep -x loadsim
```

## 命令帮助

README 只维护稳定概念和常用示例。完整参数、默认值和当前版本支持的选项以程序自身帮助为准：

```bash
loadsim --help
loadsim cpu --help
loadsim ram --help
loadsim combo --help
loadsim version
```
