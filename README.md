# LoadSim

LoadSim 在 Linux 上把整机 CPU、内存维持在指定区间。业务变忙时让出资源。它不是性能测试工具。

- `loadsim fill`：维持整机 CPU、内存或两者的使用率。
- `loadsim stress`：按固定值或周期造压，不按业务自动让出。
- `loadsim check`：只读检查 CPU 采样和内存边界。`--active` 额外验证 `SCHED_IDLE`。
- `loadsim version`：显示版本。

CPU 和内存区间都写成 `LOW:HIGH`。

> [!WARNING]
> LoadSim 会真实占用 CPU 和内存。先运行 `check --active`，再从小上限、短时长开始。没有监控和停止手段时不要用 `--duration-sec 0`。

## 要求

- Linux `amd64` 或 `arm64`
- 能读取 `/proc/schedstat` 的物理机或普通虚拟机
- 源码构建使用 `go.mod` 里的 Go 版本

CPU 填充只在宿主机系统上运行，不支持容器。程序本身通常不需要 root。写 `oom_score_adj=1000` 或安装到系统目录失败时会退出，不会降级继续跑。

## 安装

发布包是 `loadsim_<版本>_linux_<架构>.tar.gz`。普通账号：

```bash
tar -xzf loadsim_0.7.1_linux_amd64.tar.gz
cd loadsim_0.7.1_linux_amd64
bash bin/install.sh --user
```

解开就是安装目录：`bin/`、`config/`、`run/`、`log/`，没有 `deploy/`，也不带 README 和 CHANGELOG。`bin/` 里的 `install.sh` 写入 `config/loadsim.env` 并启动。要换目录，先把解压内容拷到目标目录，再在那里执行 `bash bin/install.sh --user`。`setsid` 让 fill 脱离登录会话。进程不在时，cron 在 5 分钟内重新拉起。重装会先停旧进程，删掉旧程序、旧配置和旧保活行，再按本次参数安装。`log/` 保留。

root 使用 systemd：

```bash
sudo bash loadsim_0.7.1_linux_amd64/deploy/install.sh
```

二进制在 `/usr/local/bin/loadsim`，配置在 `/etc/loadsim/loadsim.env`，单元是 `loadsim.service`。`Restart=always`，`RestartSec=30s`。`systemctl stop` 不会立刻拉起。

同一台机器只启用一种。脚本发现已有 `loadsim fill` 时拒绝再启动。

从源码构建：

```bash
GOTOOLCHAIN=auto go build -o loadsim .
./loadsim version
```

## 填充

默认目标是整机 CPU 55%–60%、内存 65%–70%。0.7.1 的内存上限只接受正整数 MiB：

```bash
loadsim fill \
  --cpu 55:60 \
  --memory 65:70 \
  --memory-max-mib 2048 \
  --yield-policy gradual \
  --duration-sec 0 \
  --status-interval-sec 60
```

- 低于下界时增加占用。
- 区间内保持。
- 高于上界时按 `--yield-policy gradual` 逐步让出。
- `--yield-policy zero` 在 CPU 超上界后把驱动清零；内存超上界后立即解除全部映射。
- `--duration-sec 0` 一直运行。默认 60 秒。
- 内存占用不会超过 `--memory-max-mib`。

CPU 按整机 `/proc/schedstat` 和 `/proc/stat` 采样，每个窗口取更高值。worker 默认使用 `SCHED_IDLE`。内核不支持时命令失败。只有明确接受较弱让步时才用 `--cpu-scheduler normal --cpu-nice 19`。

内存用匿名映射并逐页写入，缩容时 `munmap`。正常增长 64MiB/s，正常释放 256MiB/s。可用内存降到安全水位、探测失败或约束变化时，立即解除全部映射并退出。

只填 CPU 时去掉 `--memory` 和 `--memory-max-mib`。只填内存时去掉 `--cpu`，但必须带 `--memory-max-mib`。

## 检查

```bash
loadsim check --active
```

输出整机 CPU 采样、`SCHED_IDLE` 是否可用、内存总量和可用量。不打印 cgroup 路径。

## 造压

```bash
loadsim stress --cpu-percent 50 --duration-sec 60
loadsim stress --memory-mib 512 --duration-sec 60
```

`stress` 使用普通调度，不按业务自动让到目标区间。内存启动预算、运行期水位和渐进释放仍然有效。

## 配置

`$PREFIX/config/loadsim.env` 或 `/etc/loadsim/loadsim.env`：

```ini
LOADSIM_CPU_BAND=55:60
LOADSIM_MEMORY_BAND=65:70
LOADSIM_YIELD_POLICY=gradual
LOADSIM_STATUS_INTERVAL_SEC=60
LOADSIM_MEMORY_MAX_MIB=2048
```

改完后：

```bash
loadsim-cron stop
loadsim-cron start
```

保活行还在时，`stop` 后最多 5 分钟会被拉起。要停住：

```bash
crontab -l | grep -vF "$PREFIX/bin/loadsim-cron" | crontab -
loadsim-cron stop
```

systemd 改配置后执行 `sudo systemctl restart loadsim`。

## 状态

状态行是 `key=value`。正常结束前有一行 `summary`，包含区间内样本数、观测最小/平均/最大、进程 RSS 峰值和最低可用内存。启动爬坡会拉低达标率。

英文状态字段保持稳定。中文汇总：

```bash
loadsim-report --file "$PREFIX/log/fill-$(date +%F).log"
```

systemd 日志是 `journalctl -u loadsim -f`，报告是 `loadsim-report --since '10 min ago'`。
