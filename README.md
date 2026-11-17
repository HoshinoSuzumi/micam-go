# micam-go

小米米家摄像头的无头取流服务，**Go 重写**。协议与
[eugeneRover/micam](https://github.com/eugeneRover/micam)（Java，commit `2cc9d9e`）互通：
登录小米云 → 走官方 App 同款 CS2 中继 → 与摄像头建立 MISS 会话 → 解密媒体 →
**不转码**，原样复用成 MPEG-TS 经 HTTP 输出。

单静态二进制 6.8 MB，`CGO_ENABLED=0`，可直接交叉编译到 `linux/arm64`。

## 当前状态

全流程已在真机跑通：登录 → 取会话参数 → 中继或局域网直连 → 解密 → HTTP 分发 → 播放器出画。

| 阶段 | 内容 | 状态 |
|---|---|---|
| 计划 1 | 语言无关协议规格 + golden vectors + 独立校验器 | ✅ 127 项断言全绿 |
| 计划 2 | 模块划分、接口设计、go2rtc 复用清单 | ✅ `docs/DESIGN.md` |
| M0 | 模块骨架 + 向量加载器 | ✅ |
| M1 | PPPP 线格式 / initstring / 0x28 / DRW | ✅ |
| M2 | 云登录 / 签名 / 设备列表 / 供给 + 凭据存储 | ✅ 已用真实账号验证 |
| M3 | 密钥协商、媒体解密、MISS 客户端 | ✅ 已出画 |
| M4 | 中继握手 + 会话通道 | ✅ 中继与局域网两条路径均验证 |
| M5 | TS 复用 / HTTP 分发 / CLI | ✅ PotPlayer 连续播放 |
| M6 | Dockerfile(arm64) + compose | ✅ |

## 目录

```
cmd/micam/              入口：参数、登录、选摄像头、串联全流程
internal/pppp/          Frame / UID / AddrBlock / InitString / 0x28 / DRW / 中继 / 会话
internal/cloud/         登录、两步签名、RC4-drop1024、设备列表、miss_get_vendor
internal/creds/         credentials.json 读写（0600、原子替换）
internal/miss/          X25519 + HSalsa20 会话密钥、ChaCha20 媒体解密、MISS 客户端
internal/ts/            MPEG-TS 复用（PAT/PMT/PES/PCR、单调时钟修复）
internal/stream/        HTTP 分发、每观众独立复用器与有界队列
internal/cli/           参数解析与交互提示
docs/PROTOCOL.md        协议规格（本项目的唯一真相）
docs/DESIGN.md          Go 设计、里程碑、与上游的差异
testdata/vectors.json   golden vectors（验收基线）
tools/verify_vectors.py 独立实现，校验上述向量
```

## 构建与测试

```sh
go test ./...     # 除 golang.org/x/crypto 外无第三方依赖
go vet ./...
make build        # -> bin/micam
make cross        # -> bin/micam-linux-arm64 (6.8 MB)
```

## 运行

```sh
micam --did 1234567890 --region cn --port 8554
```

| 参数 | 默认 | 说明 |
|---|---|---|
| `--user` | 提示 | 小米账号（uid / 手机 / 邮箱） |
| `--user-id` | — | **数字**账号 ID；用 `passToken` 登录时必填 |
| `--region` | `cn` | `cn` `de` `sg` `us` `ru` `i2` |
| `--did` | 提示 | 要取流的摄像头 DID |
| `--bind` | `127.0.0.1` | HTTP 监听地址；容器里用 `0.0.0.0` |
| `--port` | `8554` | HTTP 端口 |
| `--quality` | `2` | 0 auto / 1 SD / 2 HD / 3 full；协议支持到 5，新机型常把全分辨率放在 3 |
| `--pts-lead` | `100` | 画面提前量（毫秒）：越小越跟手，越大对软件解码越宽容 |
| `--list-cameras` | off | 只登录并列出摄像头（DID / 型号 / 在线状态）后退出，不连中继、不开端口 |
| `--reconnect` | `0` | **连续**断流后自动重连的次数；`0` 表示不重连，交给编排层重启 |
| `--data-dir` | `~/.micam` | 存放 `credentials.json` 的目录；家目录不可写时用它换位置 |
| `--lan` | `prefer` | `prefer` 先试局域网直连、失败回落中继；`only` 只用局域网；`off` 只用中继 |
| `--dump` | — | 把解密后的裸码流存到文件，不对外提供 HTTP |
| `--dump-seconds` | `5` | `--dump` 的抓取时长 |
| `--connect-timeout` | `90` | 中继握手总超时（秒） |
| `--pass-token` | — | 浏览器复制的 `passToken`，免密码免 2FA |
| `--relogin` | off | 丢弃缓存会话重新登录 |
| `--verbose` | off | 逐包打印 PPPP |

环境变量：`MICAM_USER` `MICAM_USER_ID` `MICAM_REGION` `MICAM_DID` `MICAM_PASS_TOKEN`
`MICAM_PASS_TOKEN_FILE` `MICAM_BIND` `MICAM_PORT` `MICAM_QUALITY` `MICAM_PTS_LEAD`
`MICAM_RECONNECT` `MICAM_LAN` `MICAM_VERBOSE`。

容器里改不了命令行参数，`--verbose` 用 `MICAM_VERBOSE=1` 打开。

### 先拿到 DID

```sh
micam --list-cameras --region cn --user <账号> --pass-token <token>
```

```
  DID          MODEL                          NAME          STATUS
  312345678    chuangmi.camera.ipc019         客厅          online
  412345678    chuangmi.camera.029a02         门口          OFFLINE
```

只做登录与 `device_list`，不建 UDP 会话、不占 HTTP 端口——最省事的查询方式。

### 局域网直连

默认 `--lan prefer`：先用 `device_list` 里的 `localip`，在摄像头的 **UDP 32108** 上直接尝试
建立会话。成功的话**完全不走中继**，延迟从几十毫秒降到个位数，也不受云端抖动影响：

```
-> camera:32108   LAN_SEARCH
<-                PUNCH_PKT
->                原样回送该包
<-                P2P_RDY        （此后对端端口 = 摄像头会话端口）
   之后就是普通的 DRW / MISS 流量，与中继路径完全一致
```

失败时（不在同一网段、摄像头不响应搜索、或 3 秒内无应答）自动回落中继，日志留一条
`no direct local session, using the relay` 的 WARN。

两种传输都支持：摄像头可以在 `P2P_RDY` 里选择 **UDP**，也可以选择 **TCP**（`cs2+tcp`，
`xiaomi.camera.92ac1` 这类新机型就是）。TCP 更稳——不受 MTU 与丢包影响——但少了 UDP 的
乱序重组，代码里按传输能力自动切换。

注意两点：摄像头必须与 micam 在**同一网段**；`localip` 是摄像头自己上报的，换了网络可能过期。
`--lan only` 可以强制只走局域网（调试用），`--lan off` 则完全跳过这次尝试。

退出码与上游一致：`1` 无摄像头/传输不支持，`2` 登录或 2FA，`3` 中继失败，`4` 离线，
`5` 无媒体，`6` 端口占用。

### 画面与延迟

**画面不如米家 App 清晰**：先看码流实际分辨率。

```sh
ffprobe -v error -select_streams v -show_entries stream=width,height -of csv=p=0 http://127.0.0.1:8554/cam.ts
```

低于手机上看到的，就把 `--quality` 往上调。`2` 通常是 HD，`3` 才是全分辨率（实测
`xiaomi.camera.92ac1` 如此），部分型号还接受 `4`/`5`；摄像头会忽略不认识的值。

**延迟**由这几层叠加，从上到下影响递减：

| 来源 | 量级 | 怎么减 |
|---|---|---|
| 播放器缓冲 | 2–10s | 换硬解播放器（mpv `--hwdec=auto`、PotPlayer），或调小其缓存 |
| PTS 提前量 | `--pts-lead`，默认 100ms | 调到 0 最跟手，代价是软件解码容易丢帧 |
| 观众队列 | ≤ 25 帧（约 1.2s） | 落后时整段丢弃并重同步到关键帧，不会累积 |
| 传输 | 局域网 <10ms / 中继 20–80ms | 用 `--lan prefer` |

**关于 `<codec> @ ... Could not find ref with POC ...` 这类报错**：先确认是不是播放器的问题。

```sh
ffmpeg -v error -i http://127.0.0.1:8554/cam.ts -t 10 -f null -
```

输出为空说明我们发出的码流是干净的，报错来自播放器**实时解码跟不上、自己丢帧**——
2K H.265 用软件解码很容易触发，换硬件解码即可。同样可以用 `--dump` 存一份裸码流离线验证。

### 断流与重连

`the camera stopped sending` 现在会带上真正的原因（`err=`），不再只是一句"停了"：

| `err` | 含义 | 往哪查 |
|---|---|---|
| `pppp: the camera closed the session` | 摄像头主动结束了会话 | 摄像头重启、被米家 App/HA 抢占、供电 |
| `pppp: no media for the idle timeout` | 15s 无媒体但连接还在 | 掉电、Wi-Fi 掉线——半开连接不会报错，只能靠看门狗发现 |
| `EOF` / `connection reset by peer` | socket 层断开 | 同上，通常是网络或摄像头侧 |

重连额度按**连续失败**计：会话跑满 60 秒就算这次故障结束，计数器清零。所以"稳跑一夜
再掉线"不会消耗额度。Mi 云接口不可达（`the Mi cloud is unreachable`）不算会话失败，
既不占额度也不会 `giving up`——重启进程修不好 DNS，只会把同一次登录失败重复一遍。

半夜断流时按顺序排除：

```sh
docker compose logs | grep -E "stopped sending|dropped the stream|cloud is unreachable"
ping -i 1 192.168.0.109        # 摄像头是否在丢包/重启
ss -tnp | grep 192.168.0.109   # 是否同时有别的客户端占着这个会话
cat /etc/resolv.conf           # 解析器指向 100.100.100.100 / fd7a:115c:a1e0::53 就是 Tailscale
systemctl status tailscaled    # MagicDNS 挂了会让 api.io.mi.com 整体不可达
```

`viewers=0` 说明当前没有人看，但流仍在以 20fps 持续拉取——摄像头会一直满负荷编码。
如果长时间没人看，停掉容器比让它空转更省摄像头寿命。

### 二次验证

密码登录若被 2FA 挑战，确认那个 URL **没有用**——验证绑定浏览器的 deviceId。
唯一出路是在浏览器登录 `https://account.xiaomi.com`，从 DevTools 复制 `passToken`：

```sh
printf '%s' "$PASS_TOKEN" > secrets/pass_token
```

`passToken` 近似等价于账号密码，务必 `chmod 600`。

### 怎么取 userId 与 passToken

两者都只在登录后的 Cookie 里，小米没有提供查看它们的界面：

1. 浏览器（Chrome / Edge）打开 `https://account.xiaomi.com` 并登录；
2. 按 <kbd>F12</kbd> → **Application**（应用）→ **Storage → Cookies → `https://account.xiaomi.com`**；
3. 过滤框输入 `userId`，取**名字正好是 `userId`** 那一行的 Value：一串纯数字；
4. 过滤框改成 `passToken`，取对应 Value（长得多）。

三个常见错误：把手机号当 `userId`（登录会报 passToken 被拒绝）；取了 `cUserId` 或
`serviceToken`；把 `passToken` 泄露到仓库里——它能访问你账号下的所有设备。

**`passToken was rejected` 到底是什么意思**：`account.xiaomi.com/pass/serviceLogin` 没有返回
`ssecurity`，小米对这对 cookie 的答复是 `code=70016 登录验证失败`。它只说明「这个 userId
配不上这个 token」，和 userId 的格式无关——数字账号也可能被拒。按概率排：

1. token 已经失效：改过密码、点过「退出所有设备」、或放太久被轮换了；
2. 浏览器登录的是**另一个账号**，和 `--user-id` 对不上——同一浏览器里两个号很常见；
3. 复制时被截断或多带了空格（token 很长，终端会折行）。

排查时别再从浏览器重抄一遍，直接用容器里那份——每次登录成功都会把小米轮换后的新 token
写回 `data/credentials.json`，所以它是最新的：

```sh
sudo jq -r .passToken data/credentials.json
```

日志里会带一个不泄密的指纹，用来判断两份是不是同一个东西：

```
err="cloud: the passToken was rejected: code=70016 登录验证失败
     (sent userId=\"2255231260\", passToken 144 bytes, sha256 9f2c1ab4)"
```

```sh
# 容器里那份的指纹
jq -r .passToken data/credentials.json | tr -d '\n' | shasum -a 256
```

长度和 sha256 前 8 位都对得上、却还是 70016，那就是 token 真的过期了；对不上，就是抄错了。

命令行传长 token 容易出事，建议走文件：

```sh
printf '%s' '<TOKEN>' > /tmp/pt && chmod 600 /tmp/pt
MICAM_PASS_TOKEN_FILE=/tmp/pt ./micam --user-id 2255231260
```

`--user-id` 已经给了数字账号，就不会再追问账号名了。

想降低风险，可以用小米的「设备共享」把摄像头共享给一个小号，用那个小号登录。

## Docker（ARM fnOS）

镜像由 GitHub Actions 构建，本机不需要 Docker：

| 来源 | 地址 |
|---|---|
| Docker Hub | `hoshinosuzumi/micam-go` |
| GHCR | `ghcr.io/<你的 GitHub 用户名>/micam-go` |

```sh
git clone <repo> micam && cd micam
cp .env.example .env                     # 填 MICAM_USER / MICAM_REGION / MICAM_DID
mkdir -p data secrets
printf '%s' '<passToken>' > secrets/pass_token
sudo chown -R 10001:10001 data                 # 容器以 uid 10001 运行
sudo chown 10001:10001 secrets/pass_token      # 否则容器读不到 token
sudo chmod 400 secrets/pass_token
docker compose pull
docker compose up -d
docker compose logs -f
```

本地改代码调试时，把 compose 里的 `image:` 注释掉、取消 `build: .` 的注释，再
`docker compose up -d --build`。

镜像默认 `MICAM_BIND=0.0.0.0`、`HOME=/data`，凭据落在 `./data`。
上游硬编码 `127.0.0.1` 的问题在这里不存在——Go 版直接监听可配置地址，不需要 socat。

**默认用 host 网络，不是可选项。** LAN 直连靠 `255.255.255.255` 广播，Docker 的 bridge
不会把它送到物理网段，摄像头收不到 `LAN_SEARCH` 就会**静默退回中继**——症状是"本机跑得通，
容器里又变回中继"。host 模式不能同时写 `ports:`，8554 直接开在宿主机上。

跨网段部署（VPS，或摄像头不在同一局域网）时改回 bridge：注释 `network_mode: host`、
加回 `ports: ["8554:8554"]`、并设 `MICAM_LAN=off` 省掉必然失败的 3 秒广播等待。

启动后确认走的是直连，而不是中继：

```sh
docker compose logs | grep -E "direct local session|connecting via"
```

### 打包成 fnOS 应用（fpk）

`packaging/fnos/` 是一套按飞牛规范写的应用包，用 `fnpack` 构建后可从应用中心安装：

| 需求 | 由谁提供 |
|---|---|
| 配置 | 「应用设置」里的表单（`wizard/config`）：账号、userId、passToken、DID、清晰度、连接方式 |
| 启动 / 停止 / 状态 | 应用中心，由 `cmd/main` 的 `start`/`stop`/`status` 实现 |
| 界面 | 应用卡片打开 `app/www/index.html`，走 CGI，不额外占端口 |

```sh
make fpk     # 需要 fnpack：https://developer.fnnas.com/docs/cli/fnpack
```

包结构、安装后的目录布局，以及几处**需要真机确认**的地方见
[`packaging/fnos/README.md`](packaging/fnos/README.md)。

### 发布新镜像

### 发布

三个工作流，除 CI 外都是**手动触发**：

| 工作流 | 作用 | 怎么触发 |
|---|---|---|
| `ci` | gofmt / vet / test | 推 main 与 PR 时自动 |
| `release` | 打 tag → 多架构镜像 → 推送；可选顺带打包 fpk | Actions → release → Run workflow |
| `fpk` | 把某个 tag 打成 `.fpk` | Actions → fpk → Run workflow |

**发版**：Actions → `release` → Run workflow，填版本号（如 `0.5.0`）。工作流会**自己创建
并推送 `v0.5.0`**，再用它构建镜像——不需要本地 `git tag`。tag 已存在会直接失败，避免覆盖。

三个可选开关：

| 输入 | 默认 | 作用 |
|---|---|---|
| `latest` | 开 | 是否同时把 `latest` 指向这个版本 |
| `fpk` | 开 | 发布完成后自动打包 fnOS 应用包 |
| `draft` | 开 | GitHub Release 建成草稿，检查过产物再手动发布 |

**产物都挂在 GitHub Release 上**：`micam-linux-arm64`、`micam-linux-amd64`，勾了 `fpk`
时还有 `micam_<版本>.fpk`。草稿只影响 Release 的可见性，镜像和 tag 都是正常发布的。

**只打包 fpk**：Actions → `fpk` → Run workflow。tag 留空表示用最新的 tag，也可以填指定
版本；它**只读仓库、不会打 tag**，产物同样挂到对应 tag 的 Release（没有就新建一个草稿）。

产物在工作流的 Artifacts 里：`micam-binaries-<版本>`（两个架构的静态二进制，不装 Docker
也能 `scp` 到 NAS 跑）和 `micam-fpk-<版本>`（fnOS 安装包）。

GHCR 只需要 `GITHUB_TOKEN`，无需配置。要同时推 Docker Hub，在仓库
**Settings → Secrets and variables → Actions** 里加：

| Secret | 值 |
|---|---|
| `DOCKERHUB_USERNAME` | Docker Hub 用户名（小写） |
| `DOCKERHUB_TOKEN` | Account Settings 里创建的 Access Token（Read & Write） |

没有这两个 secret 时，工作流只推 GHCR，不会失败。

⚠️ 这个流**没有任何鉴权**。只把它发布在可信网段，或前面挂一层带认证的反代。

## 真机验证记录

| 日期 | 环境 | 结果 |
|---|---|---|
| 2026-11-17 | `xiaomi.camera.92ac1`（H.265 2304×1296），经 CS2 中继 | 连续播放稳定；PTS 单调，PCR 落后 PTS；全程无转码 |
| 2026-11-17 | 同一摄像头，**局域网直连**（`cs2+tcp`） | 广播发现 → UID 认领 → TCP 会话建立，媒体不走中继 |
| 2026-11-17 | 全流程，`--quality 3 --pts-lead 100` | 画面与米家 App 相当，延迟明显下降；`resyncs=0` |

覆盖的完整链路：真实账号登录 → `device_list` → `miss_get_vendor` → 中继/局域网建链 →
MISS 认证 → 媒体解密 → TS 复用 → HTTP 分发 → PotPlayer / VLC / ffplay 出画。

两条实测结论已固化进 `8.6` 节：这个型号**只回应广播搜索**（单播被忽略），并且
**只提供 TCP 会话**（回 `P2P_RDY_TCP`）。后者是 `Link` 抽象存在的原因——同一条
MISS/DRW 逻辑要能跑在两种传输上。

关于 `[hevc] Could not find ref with POC` 这类解码器报错：用 `--dump` 抓原始码流、
再用 `ffmpeg -f null` 离线解码，两者都是零错误，所以那是播放器**自己丢帧**（软件解码
2K H.265 跟不上），不是流的问题。判别方法见「画面与延迟」一节。

## 为什么值得有它

上游测试里的向量来自独立参考实现与真机抓包，是复刻的唯一硬证据。
把它固化成语料 + 一个不同语言的校验器，任何后续实现（Go、Rust）都可以在**没有摄像头**
的情况下先证明自己是对的。

## 与上游的差异

协议层零差异。部署面有意改进：

| 项 | 上游 | 本实现 |
|---|---|---|
| 监听地址 | 硬编码 `127.0.0.1` | `--bind`，默认仍 `127.0.0.1` |
| 凭据来源 | 交互 + 文件 | 增加 env / token 文件 |
| 凭据目录 | JVM `user.home` | Go 的 `$HOME`（容器里可直接挂卷） |
| 日志 | stdout 文本 | `slog`，`--verbose` 切换 Debug |
| 区域默认值 | `ru` | `cn`（目标部署在国内） |

## 许可

上游仓库未附 LICENSE（默认保留所有权利）。本目录是供私人研究/自用重写的笔记与代码，
若要分发镜像或衍生作品，请先与上游作者确认。
