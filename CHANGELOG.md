# 版本历史

这里记录 LoadSim 每个公开版本对使用者有影响的主要变化。完整发布正文、校验文件和二进制产物以 [GitHub Releases](https://github.com/fanderchan/loadsim/releases) 为准。

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

[v0.5.0]: https://github.com/fanderchan/loadsim/releases/tag/v0.5.0
[v0.4.0]: https://github.com/fanderchan/loadsim/releases/tag/v0.4.0
[v0.3.0]: https://github.com/fanderchan/loadsim/releases/tag/v0.3.0
