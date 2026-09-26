# LoadSim setsid 部署

普通账号执行。不要加 sudo。

## 1. 解压

发布包解开就是安装目录，没有 `deploy/`：

```text
loadsim_0.7.1_linux_amd64/
  bin/loadsim
  bin/loadsim-cron
  bin/loadsim-report
  bin/install.sh
  bin/uninstall.sh
  config/loadsim.env.example
  run/
  log/
  LICENSE
  THIRD_PARTY_LICENSES.txt
```

- `bin/loadsim`：填充程序。`fill` 把整机 CPU、内存维持在目标区间。
- `bin/loadsim-cron`：保活脚本。`ensure` 发现进程不在就拉起，`start`、`stop`、`status` 供手工使用。
- `bin/loadsim-report`：读取当天日志，输出中文效果报告。不参与填充。
- `bin/install.sh`：写入 `config/loadsim.env`，登记 crontab，并启动。程序已在 `bin/` 里，不会再复制一遍。
- `bin/uninstall.sh`：停止进程，删除 `loadsim`、`loadsim-cron`、`loadsim-report`、`config/` 和 crontab 保活行。保留 `install.sh`、`uninstall.sh` 和 `log/`，方便重装。
- `config/loadsim.env.example`：配置样例。安装后实际使用的是 `config/loadsim.env`。
- `run/`：运行时存放 `loadsim.pid`，记录 fill 进程号。`start` 写入，`stop` 删除，`status` 和 `stop` 靠它找进程。不用手工改。
- `log/`：按天写 `fill-日期.log`。卸载和重装都不删除。

就用这个目录：

```bash
cd /tmp
tar -xzf loadsim_0.7.1_linux_amd64.tar.gz
cd loadsim_0.7.1_linux_amd64
bash bin/install.sh --user
```

`install.sh` 写入 `config/loadsim.env`，然后启动。程序和 `uninstall.sh` 已经在 `bin/` 里。

要换到客户目录时，把解压目录拷过去再装：

```bash
mkdir -p /客户目录
cp -a loadsim_0.7.1_linux_amd64/. /客户目录/
cd /客户目录
bash bin/install.sh --user
```

也可以在解压目录执行 `bash bin/install.sh --user --prefix /客户目录`。`--prefix` 后面必须跟已存在的目录，空着会报 `missing value`。

后面命令里的安装目录，换成实际路径。

## 2. 安装结果

```text
bin/loadsim
bin/loadsim-cron
bin/loadsim-report
bin/uninstall.sh
config/loadsim.env
run/loadsim.pid
log/fill-日期.log
```

crontab：

```text
*/5 * * * * <安装目录>/bin/loadsim-cron ensure
```

`bin` 不在 PATH 时，把安装目录换成实际路径：

```bash
echo 'export PATH="<安装目录>/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```

## 3. 检查

```bash
loadsim check --active
loadsim-cron status
```

`status` 退出码 0 为运行中。进程会话号等于自身 pid，父进程为 1。

## 4. 改配置

编辑 `config/loadsim.env`：

```ini
LOADSIM_CPU_BAND=55:60
LOADSIM_MEMORY_BAND=65:70
LOADSIM_YIELD_POLICY=gradual
LOADSIM_STATUS_INTERVAL_SEC=60
LOADSIM_MEMORY_MAX_MIB=2048
```

生效：

```bash
loadsim-cron stop
loadsim-cron start
```

保活行还在时，`stop` 后最多 5 分钟会被重新拉起。

## 5. 停止

```bash
crontab -l | grep -vF '<安装目录>/bin/loadsim-cron' | crontab -
loadsim-cron stop
loadsim-cron status
```

`status` 应显示未运行，退出码 1。

## 6. 恢复

```bash
loadsim-cron start
( crontab -l 2>/dev/null | grep -vF '<安装目录>/bin/loadsim-cron' || true
  echo '*/5 * * * * <安装目录>/bin/loadsim-cron ensure' ) | crontab -
```

## 7. 看效果

```bash
loadsim-report --file log/fill-$(date +%F).log
```

在安装目录下执行。不在安装目录时，把 `log/` 换成安装目录下的日志路径。

## 8. 升级

把新包解压到临时目录，再装回原来的安装目录：

```bash
tar -xzf loadsim_0.8.0_linux_amd64.tar.gz
bash loadsim_0.8.0_linux_amd64/bin/install.sh --user --prefix <安装目录>
```

先停旧进程，清掉旧程序、旧配置和旧 crontab 行，再按本次参数重新安装。`log/` 保留。

## 9. 卸载

在安装目录执行：

```bash
bin/uninstall.sh
```

删除 `bin/` 里的程序和脚本、`config/`，以及 crontab 保活行。`log/` 保留。不需要 root。
