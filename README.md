# LoadSim

[![CI](https://github.com/fanderchan/loadsim/actions/workflows/ci.yml/badge.svg)](https://github.com/fanderchan/loadsim/actions/workflows/ci.yml)

LoadSim 是面向 Linux 的资源填充与负载模拟工具。它的主要用途是在资源利用率考核场景中填补空闲 CPU 或内存，并在业务变忙时主动让出资源；它不是性能基准测试工具。

公开命令按使用意图划分：

- `loadsim fill`：生产填充，把整机总 CPU、总内存或两者维持在闭区间内。
- `loadsim stress`：测试造压，生成明确的固定或周期波动负载。
- `loadsim check`：只读检查整机 CPU 采样与内存保护边界；加 `--active` 时额外验证一次 `SCHED_IDLE`。
- `loadsim version`：显示版本。

旧的 `cpu`、`ram`、`combo` 参数体系已经移除。CPU 与内存都使用 `LOW:HIGH` 表达总使用率区间，避免同一个目标出现两套不对称写法。

> [!WARNING]
> LoadSim 会真实消耗 CPU 和内存。首次上线必须先运行 `check --active`，再从小上限、短时长开始串行验证。不要在没有监控、停止手段和业务变更窗口时直接无限运行。

## 系统要求

- Linux `amd64` 或 `arm64`
- 可读取 v10–v17 `/proc/schedstat` 的物理机或普通虚拟机
- 从源码构建需要 `go.mod` 指定的 Go 版本

CPU 填充只面向宿主机操作系统，不支持容器内运行。程序运行本身通常不需要 root。设置 `oom_score_adj=1000` 或安装到系统目录可能需要额外权限；权限不足时 LoadSim 会拒绝启动，不会静默降低保护。

## 安装

### 使用 GitHub Release

[GitHub Releases](https://github.com/fanderchan/loadsim/releases) 提供 Linux 静态二进制压缩包和 `checksums.txt`：

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
```

安装了 GitHub CLI 时，还可以验证构建来源证明：

```bash
gh attestation verify "${ARCHIVE}" -R fanderchan/loadsim
```

来源证明用于确认产物来自本仓库的发布工作流，不能替代代码审查和上线回归。

### 从源码构建

```bash
git clone https://github.com/fanderchan/loadsim.git
cd loadsim
GOTOOLCHAIN=auto go build -o loadsim .
./loadsim version
```

## 生产填充

### 只填充 CPU

下面的命令让整台物理机或虚拟机的总 CPU 使用率保持在 30%–50% 闭区间：

```bash
loadsim fill \
  --cpu 30:50 \
  --duration-sec 0 \
  --status-interval-sec 5
```

控制规则是：

- 低于 30% 时逐步增加 LoadSim 的驱动力。
- 30%–50% 内保持，不因小幅波动反复增减。
- 高于 50% 时逐步降低驱动力。
- `--duration-sec 0` 表示持续运行；默认值为 60 秒，防止误操作后无限造压。

`fill` 固定使用整机 `system` 口径：从 `/proc/schedstat` 读取每个逻辑 CPU 的任务运行时间并汇总，不读取 CPU cgroup，也不使用在部分虚拟化内核中可能少记低优先级 CPU 时间的 `/proc/stat`。启动和运行期间会校验 schedstat 版本、逻辑 CPU 集合、计数器单调性和读取耗时；满载时最多容忍 0.5% 的跨时钟计时偏差并钳制到物理容量，超过该边界或无法证明采样连续可靠时仍然失败关闭。

LoadSim 自身能使用的 worker 数量只取进程 CPU affinity 与 `GOMAXPROCS` 的较小值，并在运行期间重新检查。CPU 路径不解析 cgroup v1/v2 的用量、quota 或 cpuset 文件。

CPU worker 默认使用 Linux `SCHED_IDLE`。普通业务即使也是 `nice=19`，仍然比 `SCHED_IDLE` worker 优先。每个 worker 都会在独立 OS 线程上设置并读回调度策略；内核、容器安全策略或权限不支持时，命令失败关闭。

只有明确接受较弱的让步保证时，才使用普通调度回退：

```bash
loadsim fill \
  --cpu 30:50 \
  --cpu-scheduler normal \
  --cpu-nice 19 \
  --duration-sec 0
```

普通调度下 `nice=19` 只能提供相对权重，不能保证同为 `nice=19` 的业务一定优先。

CPU 默认每 3 秒控制一次、采样 1 秒、每轮最多改变 5 个百分点：

```text
--cpu-control-ms 3000
--cpu-sample-ms 1000
--cpu-max-step 5
```

这些默认值用于过滤偶发尖刺并减少控制抖动。需要更快响应时应先缩短测试时长观察曲线，不要一次把三个值都调得很激进。

### 只填充内存

内存区间必须同时设置一个 LoadSim 自身的绝对上限，并且只能二选一：

```bash
loadsim fill \
  --memory 30:50 \
  --memory-max-gib 2 \
  --duration-sec 0
```

或：

```bash
loadsim fill \
  --memory 30:50 \
  --memory-max-mib 2048 \
  --duration-sec 0
```

`--memory-max-gib` 和 `--memory-max-mib` 的数值都不带单位字符串。前者适合整 GiB 上限，后者适合 2330MiB 这类精确值；两者同时出现或都不出现都会报错。

区间表示可见系统的总内存使用率，不是 LoadSim 自身 RSS：

- 所有可见约束都低于 30%，且连续两个样本成立后，才向区间中点补充。
- 任一宿主机或 cgroup 约束高于 50%，立即决定向区间中点缩减。
- 区间内保持当前目标。
- 无论系统多空闲，LoadSim 的映射量都不会超过显式绝对上限。

默认正常增长为 64MiB/s，正常释放为 256MiB/s：

```text
--memory-grow-mib-per-sec 64
--memory-release-mib-per-sec 256
```

控制器下调、运行时间结束、`SIGINT` 或 `SIGTERM` 都按释放速率渐进 `munmap`，避免大容量机器一次解除大量映射造成页表回收和内核抖动。CPU worker 会先停止，以便业务立即拿回 CPU。

紧急安全水位与正常调节是两条独立路径。可用内存达到安全水位、内存探测失败或约束发生危险变化时，LoadSim 会绕过释放限速，立即解除自己的全部映射并非零退出。紧急场景优先避免业务 OOM。

### 同时填充 CPU 和内存

两个资源直接组合，不再使用单独的组合子命令：

```bash
loadsim fill \
  --cpu 30:50 \
  --memory 30:50 \
  --memory-max-gib 2 \
  --duration-sec 0 \
  --status-interval-sec 5
```

CPU 观察整机调度统计；内存同时检查宿主机和进程可见的有限安全约束。任一关键探测器失败时都会停止本次填充；内存发生紧急风险时，先解除内存映射，再停止 CPU。

## 内存保护

LoadSim 使用匿名 `mmap` 分块申请内存并逐页写入，使页面真实驻留；缩容通过 `munmap` 真实归还。页面标记为不进入 core dump，并写入不同标记以降低 KSM 合并造成的虚假占用。

保护包括：

- 启动前分别检查宿主机和所有可见的有限 cgroup 约束。
- 每次增长前重新验证完整安全预算。
- 默认每 100ms 检查一次紧急水位。
- 自动水位通常为总内存的 10%，且通常不少于 128MiB；小容量环境最多保留总量的 25%。
- 启动预算还会额外保留分配块和抖动余量。
- 默认请求 `oom_score_adj=1000`，使真正发生 OOM 时优先牺牲 LoadSim。

高级参数：

```text
--memory-min-available-mib 0
--memory-check-ms 100
--memory-block-mib 16
--oom-score-adj 1000
```

`--memory-min-available-mib 0` 使用自动水位。受限容器明确需要继承外部 OOM 策略时，可使用 `--oom-score-adj -1`。生产 `fill` 不提供绕过启动预算的 `--force`。

绝对上限不是“必须占满”的目标，而是最坏情况下 LoadSim 可以使用的保险丝。应按最小有效 cgroup 或主机容量选择，并为业务峰值、页缓存、内核内存和不可观测进程留足余量。

## 环境检查

只读检查：

```bash
loadsim check
```

上线前检查，同时在一个用完即销毁的 OS 线程上设置并读回 `SCHED_IDLE`：

```bash
loadsim check --active
```

机器可读输出：

```bash
loadsim check --active --json
```

检查结果会显示：

- 整机 CPU 采样来源、匿名短 ID、逻辑 CPU 数量和采样使用率。
- `SCHED_IDLE` 是否经过主动验证。
- 宿主机及每个可见有限 cgroup 的总量、已用量和可用量。

输出不会打印 cgroup 路径、初始化凭据或其他部署秘密。

## 测试造压

`stress` 面向实验室和监控验证，不根据业务自动让步到总量区间。

固定 CPU：

```bash
loadsim stress \
  --cpu-percent 50 \
  --cpu-cores 4 \
  --duration-sec 60
```

CPU 周期波动：

```bash
loadsim stress \
  --cpu-wave 20:80 \
  --period-sec 120 \
  --duration-sec 600
```

固定内存：

```bash
loadsim stress \
  --memory-gib 2 \
  --duration-sec 60
```

CPU 与内存同时波动：

```bash
loadsim stress \
  --cpu-wave 20:60 \
  --memory-wave-mib 512:2048 \
  --period-sec 120 \
  --duration-sec 600
```

`stress` 默认使用普通调度和 `nice=0`，因为它的意图是制造明确测试负载。它仍保留内存启动预算、运行期护栏、OOM 优先牺牲和渐进释放。`--force` 只允许绕过测试负载的启动目标预算，不能关闭运行期安全水位或探测失败保护。

## 物理机与虚拟机

- CPU `fill` 只支持直接运行在物理机或普通虚拟机的宿主机操作系统中，控制口径始终是整机。
- CPU 采样不区分 cgroup v1 和 v2，也不依赖 CPU cgroup 文件；应以 `check` 输出的 `proc:schedstat` 和 `host` 边界为准。
- 不要在容器中运行 CPU `fill`。容器内的 `/proc/schedstat` 可能反映宿主机，而 worker 能使用的资源却受容器约束，两者不构成可靠的同一控制边界。
- 逻辑 CPU 集合、schedstat 版本或计数器、进程 affinity、`GOMAXPROCS` 发生变化时，CPU 控制器失败关闭，确认新环境后再重启。
- 内存保护仍会读取宿主机和可见的有限 cgroup 约束，避免忽略 systemd `MemoryMax` 等实际内存上限；这不参与 CPU 使用率采样。

状态中的 `cpu_source=proc:schedstat`、`cpu_boundary=host:...` 和 `cpu_scope_cpus` 表示整机 CPU 控制口径；`memory_scope` 与 `memory_guard_scope` 独立表示内存边界。

## 使用 systemd 托管

LoadSim 默认把运行状态写到前台标准输出，便于直接调试。生产环境建议由 systemd 托管，日志自然进入 journal：

```ini
[Unit]
Description=LoadSim production filler
After=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/loadsim fill --cpu 30:50 --duration-sec 0 --status-interval-sec 5
Restart=on-failure
RestartSec=10
TimeoutStopSec=600
CPUWeight=1

[Install]
WantedBy=multi-user.target
```

查看后台日志：

```bash
journalctl -u loadsim -f
```

`TimeoutStopSec` 应大于“最大内存上限 ÷ 正常释放速率”并留出余量，否则 systemd 可能在渐进释放完成前发送 `SIGKILL`。组合场景请把完整命令写入独立脚本或使用 systemd 支持的续行方式。

## 上线顺序

建议每种环境串行执行：

1. `loadsim check --active`。
2. 只启用 CPU，使用较窄上限和 10 分钟时长，同时模拟正常业务与偶发尖刺。
3. 检查业务延迟、整机 CPU 采样、LoadSim 驱动力和停止后的残留。
4. 再启用 CPU+内存，内存绝对上限先取保守值。
5. 检查增长、区间保持、业务变忙后的渐退、紧急水位和停止后的 RSS。
6. 确认无异常后，才把 `--duration-sec` 改为 `0`。

不要并行运行多个煲机场景，否则会争抢同一宿主机资源并污染结论。

## 状态与退出

状态行使用 `key=value`，例如：

```text
[12:00:00] mode=fill cpu_band=30.0:50.0% cpu_scope=system cpu_scheduler=idle cpu_drive=18.0% cpu_workers=2/4 cpu_scope_cpus=4.00 cpu_source=proc:schedstat cpu_boundary=host:schedcpu-... cpu_observed=39.4% memory_scope=host memory_total=38.2% process_rss=9MiB
```

正常达到时长或收到 `SIGINT` / `SIGTERM` 后返回成功。控制器错误、记账矛盾、边界变化、安全探测失败或紧急水位触发时返回非零。状态输出为英文是为了保持脚本字段稳定；仓库文档和发布说明使用中文。

## 开发验证

```bash
test -z "$(gofmt -l .)"
GOTOOLCHAIN=auto go vet ./...
GOTOOLCHAIN=auto go test ./...
GOTOOLCHAIN=auto go test -race ./...
GOTOOLCHAIN=auto go build ./...
```

版本历史见 [CHANGELOG.md](CHANGELOG.md)，每个公开版本的完整正文和构建产物以 GitHub Releases 为准。
