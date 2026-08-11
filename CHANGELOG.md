# 版本历史

这里记录 LoadSim 每个公开版本对使用者有影响的主要变化。完整发布正文、校验文件和二进制产物以 [GitHub Releases](https://github.com/fanderchan/loadsim/releases) 为准。

## [v0.7.1] - 2026-08-11

这次补丁更新修复部分 BCLinux 厂商内核延迟汇总 schedstat 运行时间时，整机 CPU 采样可能短时超过物理容量并导致 `fill` 退出的问题。

### 主要变化

- 整机 CPU 采样改为同时读取 `/proc/schedstat` 和 `/proc/stat`，每个窗口取更高的使用率：前者保留对 `SCHED_IDLE` worker 的准确记账，后者补足普通优先级业务满载时 schedstat 延迟刷新造成的短时少记。
- schedstat 短时批量刷新导致的超容量值现在保守按 100% 饱和处理，让 LoadSim 优先退出 CPU 竞争，而不是因厂商内核的记账刷新节奏报错退出。
- 状态与 `check` 的 CPU 来源更新为 `proc:schedstat+stat`；schedstat 版本、双源 CPU 集合、计数器单调性、读取耗时、采样时窗和控制边界校验仍然保留。
- 修复候选在 BCLinux 8.2 与 BCLinux 21.10 各完成 CPU、CPU＋内存两个 5 分钟场景；业务争抢期间普通优先级负载获得 98.9%～99.5% 的整机 CPU 能力，未出现 LoadSim 错误、OOM 或内核异常。

## [v0.7.0] - 2026-08-11

这次更新降低整机 CPU 调度计数器偶发慢读造成的误退出，并在首条状态日志中增加 cgroup CPU 运行环境诊断。

### 主要变化

- `/proc/schedstat` 单次读取超过耗时上限时丢弃该快照并立即重试，只有连续三次慢读才失败关闭，避免宿主机抢占或虚拟机短暂停顿造成一次性误退出，同时不放宽有效样本的读取耗时标准。
- `fill` 的首条 CPU 状态行新增 cgroup v1/v2 诊断，会记录当前层和可见祖先层中最紧的 CPU quota、最低相对权重及累计 throttling；不输出 cgroup 路径，诊断失败也不会中止负载。

## [v0.6.1] - 2026-08-06

这次更新修复整机 CPU 接近满载时，调度运行时间与用户态单调时钟之间的微小计时偏差可能导致填充进程误判统计无效并退出的问题。

### 主要变化

- `/proc/schedstat` 整机采样允许最多 0.5% 的满载计时偏差，并把该窄幅范围内的 busy 时长与使用率一并钳制到物理容量。
- 超过容差的计数器异常仍然失败关闭，不降低版本、CPU 集合、单调性、采样时窗和读取耗时等既有安全校验。
- 新增 BCLinux 21.10 满载业务竞争复现值的回归测试，同时验证明显超限仍会被拒绝。

## [v0.6.0] - 2026-08-06

这次更新把生产 CPU 填充收敛为物理机和普通虚拟机的整机控制，移除复杂且在部分虚拟化环境中不可靠的 CPU cgroup 记账路径。

### 主要变化

- CPU `fill` 与 `check` 改用 `/proc/schedstat` 汇总整机每个逻辑 CPU 的任务运行时间，避开当前虚拟化环境中 `/proc/stat` 和 cgroup 根计数少记 `SCHED_IDLE` 工作时间的问题。
- CPU 控制口径明确收敛为物理机和普通虚拟机整机，不再解析 cgroup v1/v2 的 CPU 用量、quota 或 cpuset 文件；worker 容量只校验进程 affinity 与 `GOMAXPROCS`。
- 新增 schedstat 版本、逻辑 CPU 集合、计数器单调性、采样时窗和读取耗时校验，关键统计不连续时失败关闭。
- 修复多 CPU worker 在热循环中反复轮询同一个停止 channel 所造成的全局锁竞争，改用原子停止标记。
- 状态输出的 CPU 来源固定为 `proc:schedstat`，边界类型固定为 `host`；内存仍独立检查宿主机及可见的有限 cgroup 约束。

## [v0.5.0] - 2026-07-28

这是一次不保留旧命令兼容性的产品重构，目标是把生产资源填充与实验室造压彻底分开，并让 CPU、内存使用同一套区间表达。

### 主要变化

- 移除 `cpu`、`ram`、`combo`，新增按意图划分的 `fill`、`stress`、`check`。
- `fill` 使用 `--cpu LOW:HIGH` 和 `--memory LOW:HIGH` 统一表达总使用率闭区间。
- 内存填充必须显式设置且只能设置一个绝对上限：`--memory-max-mib` 或 `--memory-max-gib`。
- 生产 CPU worker 默认改用 Linux `SCHED_IDLE`；每个线程设置后都读回校验，不支持时失败关闭。普通调度与 `nice` 只作为显式回退。
- 内存增长和正常释放使用独立限速。控制器下调和正常停止会渐进归还；紧急安全水位仍立即解除全部映射。
- 新增 `check --active`，用于核对 cgroup CPU 记账边界、内存约束并主动验证 `SCHED_IDLE`。
- 未知或已经移除的子命令现在明确返回非零，不再只显示帮助后以成功状态退出。
- 修复 cgroup v1 以页对齐大整数表示“不限内存”时被误报为有限约束的问题。
- 更新状态字段、生产示例、systemd 托管说明以及物理机、VM、容器的边界说明。

## [v0.4.0] - 2026-07-26

这是一次面向生产安全边界的更新。重点不是制造更猛烈的负载，而是让 LoadSim 在物理机、虚拟机和容器中更清楚地识别自己能用多少资源，并在统计异常、约束变化或内存接近危险水位时优先停止和归还资源。

### 主要变化

- RAM 新增按总使用率闭区间调节的 `adaptive` 模式，支持低水位确认、增长限速、超上限立即释放和 LoadSim 自身占用硬上限。
- CPU 新增 `system` 总量范围，使用当前可见 cgroup 根记账，避开部分虚拟化内核 `/proc/stat` 少记低优先级 CPU 时间的问题；用量、quota 和 cpuset 无法证明属于同一 hierarchy 时会失败关闭。
- CPU worker 默认使用 Linux `nice=19`，业务线程更容易抢回调度时间；CPU 容量会同时考虑 affinity、`GOMAXPROCS` 和 cgroup v1/v2 quota。
- CPU 状态新增直接可读的上下限、最大自身贡献、记账来源和不泄露路径的边界短 ID；采样期间发生同容量 remount/rebind 也能被识别。
- RAM 改为使用匿名 `mmap` 逐页触碰，并在缩容时通过 `munmap` 实际归还；同时加入 OOM 优先牺牲、KSM 合并规避和 core dump 规避。
- 内存启动预算和运行期护栏同时检查宿主机及可见 cgroup 的多层约束；探测失败、约束突变或越过安全水位时失败关闭。
- 发布包新增完整第三方许可证汇编和签名构建来源证明，GitHub Actions 固定到审查过的提交；发布对比按语义版本和已发布 Release 选择，不再依赖标签创建时间。
- README 增加生产动态填充示例、容器/VM/物理机边界、状态字段说明和串行上线检查。

### 兼容性

- 官方产物继续提供 Linux `amd64`、`arm64` 静态二进制，并保留 CentOS 7 启动冒烟验证。
- 源码构建最低 Go 版本升级到 `1.25.12`。

## [v0.3.0] - 2026-03-09

首个正式版，提供 `cpu`、`ram`、`combo` 三个命令，建立 fixed/wave 负载模式、整机 CPU 目标控制、RAM 块与速率控制，以及 CI、Release 和 CentOS 7 冒烟验证。

### 测试版历史

- `v0.3.0-beta.1`：首次公开 beta，统一 `LoadSim` / `loadsim` 命名并完成三个核心命令。
- `v0.3.0-beta.2`：增加官方 `linux/amd64` 产物的 CentOS 7 启动验证。

[v0.7.1]: https://github.com/fanderchan/loadsim/releases/tag/v0.7.1
[v0.7.0]: https://github.com/fanderchan/loadsim/releases/tag/v0.7.0
[v0.6.1]: https://github.com/fanderchan/loadsim/releases/tag/v0.6.1
[v0.6.0]: https://github.com/fanderchan/loadsim/releases/tag/v0.6.0
[v0.5.0]: https://github.com/fanderchan/loadsim/releases/tag/v0.5.0
[v0.4.0]: https://github.com/fanderchan/loadsim/releases/tag/v0.4.0
[v0.3.0]: https://github.com/fanderchan/loadsim/releases/tag/v0.3.0
