# 版本历史

这里记录 LoadSim 每个公开版本对使用者有影响的主要变化。完整发布正文、校验文件和二进制产物以 [GitHub Releases](https://github.com/fanderchan/loadsim/releases) 为准。

## [v0.7.2] - 2026-09-27

这次更新把发布包改成安装目录布局，新增免 sudo 卸载脚本，并让 `fill` 的内存上限支持自动选择。

### 主要变化

- 发布包解开就是安装目录：`bin/`、`config/`、`run/`、`log/`，不再有 `deploy/` 子目录。`bin/install.sh --user` 就地写入配置并启动，程序不再复制一遍。
- 新增 `bin/uninstall.sh` 随包分发。普通账号免 sudo 卸载：停止进程、删除程序与配置、清理 crontab 保活行，日志保留。
- `fill --memory-max-mib` 在正整数 MiB 之外接受 `auto`，按机器内存自动选择上限。0.7.1 的发布二进制不支持 `auto`。
- `loadsim-cron status` 输出中文状态摘要：运行状态、CPU 与内存的目标和当前值、LoadSim 占用、可用内存与日志路径。
- 修复就地重装失败：目标目录已有程序时，安装脚本不再删除作为安装来源的 `bin/` 内文件。

## [v0.7.1] - 2026-08-11

- 发布包用 `deploy/install.sh --user --prefix` 安装到客户目录或当前目录。`setsid` 脱离登录会话，crontab 每 5 分钟检查一次，进程不在就拉起。
- 重装先停止旧进程，删除旧程序、旧配置和旧 crontab 行，再按本次参数写入。日志保留。
- `LOADSIM_MEMORY_MAX_MIB` 只接受正整数 MiB，不接受 `auto`。
- 默认目标是 CPU `55:60`、内存 `65:70`、上限 `2048` MiB、`gradual`。
- CPU 采样同时读 `/proc/schedstat` 和 `/proc/stat`，每个窗口取更高值。schedstat 超容量时按 100% 处理，不再因此退出。
- 同一台机器检测到已有 `loadsim fill` 时拒绝再启动。
