# 版本历史

## 0.7.1

- 发布包解开就是安装目录：`bin/`、`config/`、`run/`、`log/`。`bin/install.sh --user` 写入配置并启动。`setsid` 脱离登录会话，crontab 每 5 分钟检查一次，进程不在就拉起。
- 重装先停止旧进程，删除旧程序、旧配置和旧 crontab 行，再按本次参数写入。日志保留。
- `LOADSIM_MEMORY_MAX_MIB` 使用正整数 MiB。当前发布二进制不接受 `auto`。
- 默认目标是 CPU `55:60`、内存 `65:70`、上限 `2048` MiB、`gradual`。
- CPU 采样同时读 `/proc/schedstat` 和 `/proc/stat`，每个窗口取更高值。schedstat 超容量时按 100% 处理，不再因此退出。
- 同一台机器检测到已有 `loadsim fill` 时拒绝再启动。
