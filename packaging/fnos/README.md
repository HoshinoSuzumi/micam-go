# fnOS 应用包（fpk）

把 micam 打包成飞牛 fnOS 应用：应用中心里安装，**以静态二进制直接运行**，不依赖
Docker。配置表单由 wizard 提供，启动/停止/状态由应用中心管理。

```
manifest                     应用元数据（key=value）
ICON.PNG / ICON_256.PNG      应用中心图标（make_icons.py 生成）
config/privilege             以专用包用户运行（run-as: package）
config/resource              不声明任何资源（没有 docker-project、没有共享目录）
wizard/config                「应用设置」里的配置表单
cmd/main                     start / stop / status，兼看守循环
cmd/install_* upgrade_* uninstall_* config_*   生命周期脚本
app/bin/micam                打包时交叉编译进来的静态二进制（不入库）
app/www/                     管理界面，make web 的产物（不入库）
app/ui/config                桌面入口（iframe 指向 index.cgi）
app/ui/index.cgi             静态文件 + /api/* 转发给 micam ctl
```

## 为什么不用 Docker 模板

fnpack 的 docker 模板解决的是"应用由多个容器组成、需要受控运行环境"的场景。micam
只有**一个进程**、一个端口，容器带来的只有额外一层：要挂载配置、要 `chown 10001`、
要在 entrypoint 里读 env 文件、升级还得等镜像。直接用二进制：

- 启动是毫秒级，不需要拉镜像、不依赖 Docker Hub；
- 不需要端口映射，`network_mode: host` 那套取舍整个消失；
- LAN 直连的 `255.255.255.255` 广播直接就在物理网段上；
- 配置文件的属主就是跑服务的那个用户，没有 uid 10001 的错位。

代价：包里是**架构专属**的可执行文件，所以 `platform` 必须是 `x86` 或 `arm`，两种架构
各出一个包（`packaging/build-fpk.sh` 负责）。

## 管理界面

应用卡片打开的是 `app/ui/index.cgi`：静态文件从 `app/www/` 出，`/api/*` 直接
`exec app/bin/micam ctl`。这样界面和 API 都在飞牛网关下面，**不额外开端口**，也不需要
自己做鉴权。

`micam ctl` 每次请求起一个进程（约 20 ms），读 `etc/micam.env`、`var/status.json`、
`var/micam.log`，控制动作则转给 `cmd/main`，所以界面和「应用中心」永远不会对"服务是
怎么跑的"有两种说法。

界面源码在 `web/`（Svelte 5 + Tailwind），产物不提交，构建时复制进包里。

## 构建

```sh
make web                 # 构建界面（Node 20+）
make fpk                 # amd64 + arm64
make fpk ARCHS=arm64     # 只要 ARM 的
```

实际动作是 `packaging/build-fpk.sh <arch>`：在 `.tmp/fpk-<arch>/` 里组装一份干净的包
目录，交叉编译 `app/bin/micam`，把 `manifest` 的 `platform` 改成 `x86`/`arm`，然后调用
`fnpack build`，最后把产物改名成 `micam_<版本>_<arch>.fpk`。

需要 `fnpack`：<https://developer.fnnas.com/docs/cli/fnpack>。

版本号**不取自 manifest**，而是取自 `MICAM_VERSION`（CI 传入的 tag）或 `git describe`
的最近 tag：脚本把它同时写进 `manifest` 和二进制（`-X micam/internal/cli.Version=…`），
所以包上的版本、`micam --version`、界面里显示的版本一定一致。manifest 里留着的数字只是
上一次发布的结果，供直接 `fnpack build` 的人参考。

## 运行时目录

| 路径 | 内容 |
|---|---|
| `target/bin/micam` | 可执行文件（`TRIM_APPDEST`） |
| `etc/micam.env` | 用户可配置的值，由向导写入（`TRIM_PKGETC`） |
| `etc/pass_token` | passToken，`0600` |
| `var/data/credentials.json` | 小米会话缓存（`MICAM_DATA_DIR`） |
| `var/micam.log` | 服务日志，超过 5 MB 换一份 `micam.log.1` |
| `var/micam.pid` | 看守循环的 pid |

## 进程模型

官方没有给普通应用配 supervisor，`cmd/main` 就是那个 supervisor：

```
cmd/main start
  └─ setsid cmd/main __supervise      # 独立会话 = 独立进程组
       └─ micam ...                   # 日志重定向到 var/micam.log
            ↑ 退出码非 0 时 15 秒后重启（0 = 正常停止，6 = 端口占用，不重启）
```

如果平台以 root 执行生命周期脚本，`start_child` 会用 `runuser` 降回包用户再启动
服务——对外收流的进程不该是 root。

- `start` 立刻返回，等 pidfile 落地才报成功；
- `stop` 用**负 pid** 把整个进程组收掉（`kill -TERM -- -<pid>`），micam 会走
  `signal.NotifyContext` 优雅退出；10 秒不退再 `KILL`；
- `status` 只认 pidfile 里的进程，返回 0 或 3。

未配置时（缺 DID，或既没有 passToken 也没有会话缓存）`start` 不会启动，只在
`TRIM_TEMP_LOGFILE` 里写一句说明——留着不启动，比每 15 秒重启一次刷日志好排查。

## 从容器版升级

`install_callback` / `upgrade_callback` 会：

1. 删掉旧容器 `docker rm -f micam`（它还占着 8554）；
2. 把 `var/.micam/credentials.json` 搬到 `var/data/credentials.json`，不用重新登录；
3. 从 `micam.env` 里删掉 `MICAM_BIND` / `MICAM_PORT` / `MICAM_PASS_TOKEN_FILE` /
   `MICAM_DATA_DIR`——这些现在由 `cmd/main` 决定，同一事实只写一处。

## 几处需要真机确认的地方

这个包是照着官方文档写的，没有 fnOS 设备做端到端验证：

- **生命周期脚本的运行身份**。`config/privilege` 用的是 `run-as: package`，脚本读写的
  文件因此属于包用户；如果实际上脚本以 root 执行，脚本里已经带了 `chown` 兜底。
- **`setsid` 是否可用**。util-linux 一般都有；缺了就退化成普通后台进程，`stop` 仍能
  按 pid 停掉，只是不再按进程组收。
- **应用中心是否在停止后自动重启服务**。`start` 自己带看守循环，重复启动是幂等的。

## 安全

- `etc/pass_token` 与 `etc/micam.env` 都是 `0600`，属主是服务进程自己；
- 服务以包用户运行（root 执行脚本时也会降权），不需要任何特权；
- 流本身**没有鉴权**，只应在可信网段使用；
- 不声明共享目录，应用数据留在 `var/` 里。

## 状态文件

micam 每 2 秒把状态原子重写到 `var/status.json`（先写 `.tmp` 再 rename，读到的永远是
完整文档）：状态、会话号、摄像头、传输方式、编码、监听地址、帧数、帧率、观众数、重同步次数、
开始时间、最后的错误。界面轮询的就是它。

进程没在跑时，`micam ctl` 会用 pidfile 判断，把状态显示成"已停止"，同时保留最后一条
错误——这正是"为什么停了"的答案。
