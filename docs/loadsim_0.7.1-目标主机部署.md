# LoadSim 部署手册

两种托管方式按部署账号权限**二选一**，章节之间完全独立，按需跟随其中一章操作即可：

| 章节 | 适用环境 | 托管方式 | 需要权限 |
| --- | --- | --- | --- |
| 第 1 章 systemd 部署 | 有 root 的物理机/虚拟机 | systemd 服务 + journal 日志 | root 或 sudo |
| 第 2 章 一般账号部署 | 只有普通账号、无法 sudo | cron 保活 + 安装目录日志 | 普通账号即可 |

两种方式共用同一个二进制，`fill` 本身不需要任何特权；差别只在安装路径、日志位置和保活机制。**同一台机器只启用一种**：同时运行两套填充会争抢资源并破坏目标区间（脚本有互斥保护，检测到其他账号的 `loadsim fill` 会拒绝启动，但请勿依赖它共存）。

发布 tar.gz 本身就是一体化部署包（含二进制与全部部署脚本），拷一个文件到目标机任意目录：

```bash
scp loadsim_0.7.1_linux_amd64.tar.gz 目标机:/tmp/
ssh 目标机
cd /tmp && tar -xzf loadsim_0.7.1_linux_amd64.tar.gz
```

解开就是安装目录，没有 `deploy/`：

| 路径 | 作用 |
| --- | --- |
| `bin/loadsim` | 填充程序。`fill` 维持整机 CPU、内存区间 |
| `bin/loadsim-cron` | 保活。`ensure` 在进程不在时拉起；另有 `start`、`stop`、`status` |
| `bin/loadsim-report` | 读日志出中文效果报告，不参与填充 |
| `bin/install.sh` | 写 `config/loadsim.env`、登记 crontab 并启动 |
| `bin/uninstall.sh` | 停止并删除程序、配置和保活行，保留 `log/`。普通账号即可 |
| `config/loadsim.env.example` | 配置样例。安装后生效的是 `config/loadsim.env` |
| `run/` | 存放 `loadsim.pid`，记录 fill 进程号。`start` 写入，`stop` 删除，不用手工改 |
| `log/` | 按天写 `fill-日期.log`，卸载和重装都保留 |

安装前建议先验证环境（两种模式通用，普通账号即可执行）：

```bash
./loadsim_0.7.1_linux_amd64/bin/loadsim check --active
```

确认整机 CPU 采样正常、`sched_idle=supported`、内存总量与可用量符合预期。

---

## 第 1 章 systemd 托管（root）

### 1.1 安装

```bash
sudo bash /tmp/loadsim_0.7.1_linux_amd64/bin/install.sh
```

脚本自动识别同包内的 `loadsim` 二进制，依次完成：

1. 安装二进制到 `/usr/local/bin/loadsim`；
2. 巡检脚本安装到 `/usr/local/sbin/loadsim-report`；
3. 写入默认配置 `/etc/loadsim/loadsim.env`（重装会先删除旧配置，再按本次参数写入）；
4. 渲染 `/etc/systemd/system/loadsim.service` 并 `daemon-reload`；
5. 做一次 **600 秒前台验收**，结束自动输出效果报告；
6. 验收无异常后 `systemctl enable --now loadsim` 并显示服务状态。

任一步失败脚本会停止且不会启用服务。常用参数：`--acceptance-sec`（0 跳过验收）、`--cpu-band 40:50`、`--memory-band 60:70`、`--yield-policy`、`--memory-max`、`--no-enable`、`--help`。卸载用同目录的 `uninstall.sh`。root 环境若不想用 systemd，也可 `--mode cron` 装到系统路径（保活机制同第 2 章，日志在 `/var/log/loadsim/`）。

### 1.2 配置

`/etc/loadsim/loadsim.env`（重装会按本次参数重新生成）：

```ini
LOADSIM_CPU_BAND=55:60        # 整机 CPU 目标区间
LOADSIM_MEMORY_BAND=65:70     # 整机内存目标区间
LOADSIM_YIELD_POLICY=gradual
LOADSIM_STATUS_INTERVAL_SEC=5
LOADSIM_MEMORY_MAX_MIB=2048   # LoadSim 自身绝对上限，单位 MiB；0.7.1 只接受正整数
```

修改后 `sudo systemctl restart loadsim` 生效。当前余量不满足内存目标时服务会拒绝启动并说明原因。

### 1.3 验收怎么算通过

验收每 5 秒输出一行 `key=value` 状态，结束时输出 `summary` 行和中文效果报告：

- `cpu_observed_avg`、`memory_observed_avg` 逐步进入目标区间（启动爬坡会拉低达标率，看进入区间后的样本）；
- `memory_available_min` 始终高于安全水位、退出码为 0、业务侧无异常。

验收日志同时被 `tee` 到 `/tmp/loadsim-acceptance.*.log`，可用 `loadsim-report --file <日志>` 复盘。

### 1.4 日常运维

```bash
systemctl status loadsim                  # 运行状态
journalctl -u loadsim -f                  # 实时状态行
loadsim-report --since '1 hour ago'       # 效果报告
sudo systemctl stop loadsim               # 停止（内存按释放速率渐进归还）
```

`Restart=always`、`RestartSec=30s`：服务退出、崩溃或被 OOM 杀掉后，systemd 会重新拉起；`systemctl stop` 是显式停止，不会立刻拉起，机器重启后由已启用的单元恢复。可选巡检 cron（达标率低于 90% 或无数据时报非零）：

```text
*/10 * * * * /usr/local/sbin/loadsim-report --since '10 min ago' --fail-below 90
```

### 1.5 升级与卸载

```bash
sudo bash loadsim_0.8.0_linux_amd64/bin/install.sh              # 升级（自动停旧起新）
sudo bash loadsim_0.8.0_linux_amd64/bin/uninstall.sh            # 卸载
```

---

## 第 2 章 一般账号部署（免 sudo）

适用于没有任何 sudo 权限的普通账号：解压目录就是安装目录，用 cron 保活，全天后台执行。`fill` 运行本身不需要特权。

### 2.1 安装

```bash
cd /tmp/loadsim_0.7.1_linux_amd64
bash bin/install.sh --user
```

要换到客户目录，先拷再装：

```bash
mkdir -p /客户目录
cp -a /tmp/loadsim_0.7.1_linux_amd64/. /客户目录/
cd /客户目录
bash bin/install.sh --user
```

注意**不要加 sudo**。脚本依次完成：

1. 确认 `bin/loadsim`、`bin/loadsim-cron`、`bin/loadsim-report`、`bin/uninstall.sh` 已在解压目录；
2. 清掉旧配置和当前用户 crontab 里的 `loadsim-cron` 行，再写入 `config/loadsim.env`；
3. 向**当前用户**的 crontab 写入一行保活任务：`*/5 * * * * <安装目录>/bin/loadsim-cron ensure`；
4. 快速验收：启动 fill 并观察 15 秒存活。

安装完成后 fill 由 `setsid` 脱离登录会话，全天后台执行。进程不在时，cron 在下个周期重新拉起，包括手动停止、外部 kill、异常退出、被 OOM 杀掉和机器重启。若提示安装目录的 `bin` 不在 PATH，按脚本给出的命令加入 `~/.bashrc`。状态日志在 `<prefix>/log/fill-日期.log`（每 60 秒一条，自动保留 30 天）。

### 2.2 配置

`<prefix>/config/loadsim.env`，格式与 systemd 模式完全相同。修改后生效：

```bash
loadsim-cron stop && loadsim-cron start
```

### 2.3 日常运维

```bash
loadsim-cron status    # 运行状态与最后一条状态行（退出码 0=运行中）
loadsim-cron start     # 立即启动

loadsim-report --file <prefix>/log/fill-$(date +%F).log   # 效果报告（可选）
```

`setsid` 只负责脱离登录会话：退出 SSH 或关闭终端后，填充进程继续运行，父进程变为 init。

### 2.4 停止

保活任务还在时，只执行 `loadsim-cron stop` 停不住。它会按释放速率归还内存并退出，但 crontab 里的 `ensure` 最多 5 分钟后又会拉起来。外部 `kill`、异常退出、被 OOM 杀掉、机器重启也一样，下个周期都会恢复。

要停住并保持停止，先去掉保活行，再停进程：

```bash
crontab -l | grep -vF '<prefix>/bin/loadsim-cron' | crontab -
loadsim-cron stop
loadsim-cron status    # 应显示未运行，退出码 1
```

程序、配置和日志都还在。需要恢复时执行 `loadsim-cron start`，并把保活行加回当前用户的 crontab：

```text
*/5 * * * * <prefix>/bin/loadsim-cron ensure
```

连程序一起移除时，用第 2.5 节的 `uninstall.sh --user`。

### 2.5 升级与卸载

```bash
bash loadsim_0.8.0_linux_amd64/bin/install.sh --user --prefix <安装目录>   # 升级
<安装目录>/bin/uninstall.sh                                                # 卸载
```

卸载会停止进程、删除 `<prefix>/bin` 下三个文件和 `<prefix>/config/`，并移除 crontab 保活行；`<prefix>/log/` 历史日志保留。

---

## 第 3 章 通用注意事项

- **不要并行填充**：同一台机器只保留一种部署；如果两种都装过，切换前先用原方式 `stop`/卸载（脚本互斥保护会在检测到其他实例时拒绝启动）。
- CPU `fill` 只支持物理机或普通虚拟机的宿主机系统，不支持容器。
- 大内存上限时确认停止等待时间大于"内存上限 ÷ 释放速率（默认 256MiB/s）"，避免渐进释放未完成就被强杀。
- `LOADSIM_MEMORY_MAX_MIB` 是 LoadSim 自身的绝对上限，0.7.1 只接受正整数 MiB；上限过大时，停止等待时间要大于“上限 ÷ 释放速率（默认 256MiB/s）”。
