# fnOS 应用包（fpk）

把 micam 打包成飞牛 fnOS 应用：应用中心里安装，走 Docker 运行，配置表单由 wizard 提供，
启动/停止/状态由应用中心管理。

```
manifest                     应用元数据（key=value）
ICON.PNG / ICON_256.PNG      应用中心图标（make_icons.py 生成）
config/privilege             以 root 运行：生命周期脚本要调用 docker compose
config/resource              声明 docker-project，交给 fnOS 托管容器
wizard/config                「应用设置」里的配置表单
cmd/main                     start / stop / status
cmd/install_* upgrade_* uninstall_* config_*   生命周期脚本
app/docker/docker-compose.yaml                  实际运行的服务
app/ui/config                桌面入口（iframe 指向 index.cgi）
app/ui/index.cgi             静态文件 CGI，不需要额外端口
app/www/index.html           应用界面：播放地址与用法
```

## 构建

需要 `fnpack`（官方 1.2.3，Windows / Linux 都有）：

<https://developer.fnnas.com/docs/cli/fnpack>

```sh
make fpk
# 等价于：fnpack build --directory packaging/fnos
```

产物是 `micam.fpk`，可在应用中心上传安装。

`manifest` 里的 `version` 要和镜像 tag 对齐；发版时可以改成 CI 从 git tag 注入。

## 安装后的样子

1. 应用中心安装 → 「应用设置」里填账号、`userId`、`passToken`、DID、清晰度、连接方式；
2. 保存后 `cmd/config_callback` 写入 `etc/micam.env` 与 `etc/pass_token`，然后重建容器；
3. 应用卡片显示运行状态，可启动/停止；点开是 `app/www/index.html`，给出播放地址；
4. 凭据缓存在 `var/`，重装或升级后不需要重新登录。

## 几处需要真机确认的地方

这个包是照着官方文档写的，没有 fnOS 设备做端到端验证。已知的不确定点：

- **`docker-project` 是否由框架代管容器生命周期**。如果是，`cmd/main` 里的 compose 调用就是
  多余的；脚本都写成了幂等操作（`up -d` 不会重复创建，`down` 重复执行无害），两条路径都能跑。
- **`run-as: root`**。官方建议普通应用用 `package` 模式，但生命周期脚本要调 `docker compose`，
  需要 docker socket 权限。如果 fnOS 允许 `package` 用户访问 docker 组，应改回最小权限。
- **`checkport=false`**。容器用 host 网络，端口检查交给 compose，避免和应用中心重复判断。

排查时优先看 `TRIM_TEMP_LOGFILE` 里的输出，以及 `/var/apps/micam/var` 下的容器日志。

## 安全

- `pass_token` 与 `micam.env` 都是 `0600`，且 token 只以只读卷挂进容器；
- 流本身**没有鉴权**，只应在可信网段使用；
- 应用以 root 运行生命周期脚本，但服务进程在容器里以 uid 10001 运行。
