# micam-go

小米米家摄像头的无头取流服务，**Go 重写**。协议与
[eugeneRover/micam](https://github.com/eugeneRover/micam)（Java，commit `2cc9d9e`）互通：
登录小米云 → 走官方 App 同款 CS2 中继 → 与摄像头建立 MISS 会话 → 解密媒体 →
**不转码**，原样复用成 MPEG-TS 经 HTTP 输出。

单静态二进制 6.8 MB，`CGO_ENABLED=0`，可直接交叉编译到 `linux/arm64`。

## 当前状态

| 阶段 | 内容 | 状态 |
|---|---|---|
| 计划 1 | 语言无关协议规格 + golden vectors + 独立校验器 | ✅ 127 项断言全绿 |
| 计划 2 | 模块划分、接口设计、go2rtc 复用清单 | ✅ `docs/DESIGN.md` |
| M0 | 模块骨架 + 向量加载器 | ✅ |
| M1 | PPPP 线格式 / initstring / 0x28 / DRW | ✅ |
| M2 | 云登录 / 签名 / 设备列表 / 供给 + 凭据存储 | ✅ |
| M3 | 密钥协商、媒体解密、MISS 客户端 | ✅ |
| M4 | 中继握手 + 会话通道 | ✅ 伪造 relay 回环测试 |
| M5 | TS 复用 / HTTP 分发 / CLI | ✅ |
| M6 | Dockerfile(arm64) + compose | ✅ |

**尚未做的**：真机联调（需要真实小米账号与摄像头）。M4 的九步握手已在回环上跑通，
但"真实网络下 24 连发是否足够"这类问题只有真机能回答。

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
| `--quality` | `2` | 0 auto / 1 SD / 2 HD / 3 full |
| `--connect-timeout` | `90` | 中继握手总超时（秒） |
| `--pass-token` | — | 浏览器复制的 `passToken`，免密码免 2FA |
| `--relogin` | off | 丢弃缓存会话重新登录 |
| `--verbose` | off | 逐包打印 PPPP |

环境变量：`MICAM_USER` `MICAM_REGION` `MICAM_DID` `MICAM_PASS_TOKEN` `MICAM_PASS_TOKEN_FILE`
`MICAM_BIND` `MICAM_PORT` `MICAM_QUALITY`。

退出码与上游一致：`1` 无摄像头/传输不支持，`2` 登录或 2FA，`3` 中继失败，`4` 离线，
`5` 无媒体，`6` 端口占用。

### 二次验证

密码登录若被 2FA 挑战，确认那个 URL **没有用**——验证绑定浏览器的 deviceId。
唯一出路是在浏览器登录 `https://account.xiaomi.com`，从 DevTools 复制 `passToken`：

```sh
printf '%s' "$PASS_TOKEN" > secrets/pass_token
```

`passToken` 近似等价于账号密码，务必 `chmod 600`。

## Docker（ARM fnOS）

```sh
docker compose up -d --build
```

镜像默认 `MICAM_BIND=0.0.0.0`、`HOME=/data`，所以凭据落在 `./data` 卷里。
上游硬编码 `127.0.0.1` 的问题在这里不存在——Go 版直接监听可配置地址，不需要 socat。

⚠️ 这个流**没有任何鉴权**。只把它发布在可信网段，或前面挂一层带认证的反代。

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
