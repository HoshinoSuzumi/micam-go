# micam-go 设计（计划 2）

> 前一份交付：`docs/PROTOCOL.md` + `testdata/vectors.json` + `tools/verify_vectors.py`
> （已用独立 Python 实现跑通 **127 项**断言）。

---

## 1. 目标与非目标

**目标**

1. 与上游 Java 实现**协议 1:1 互通**，向量级字节一致。
2. 单个静态二进制（`CGO_ENABLED=0`），约 10 MB，可直接交叉编译到 `linux/arm64`。
3. 修掉两处部署硬伤：**监听地址可配**、**凭据/参数可由环境与文件提供**（零交互）。
4. 保持"不转码、只复用"的原始设计。

**非目标（v1 明确不做）**

- 多摄像头并发（保持"一进程一摄像头"，用多实例解决）。
- 音频、PTZ、SD 卡回放（`0x10D`）。
- `tutk` / `agora` / `mtp` 传输。
- 直连打洞（relay 已足够，且对称 NAT 下不可行）。

---

## 2. 包结构

```
micam-go/
├── cmd/micam/main.go          仅负责组装与退出码
├── internal/
│   ├── cli/                   参数解析、交互提示、退出码
│   ├── creds/                 credentials.json 读写（0600）
│   ├── cloud/                 登录 / 签名 / RC4 / device_list / miss_get_vendor
│   ├── pppp/                  Frame UID Addr InitString Relay DataChannel Conn Setup
│   ├── miss/                  X25519+HSalsa20+ChaCha20、MISS 客户端、媒体包解析
│   ├── ts/                    MPEG-TS 复用器
│   ├── stream/                HTTP 分发 + 观众扇出
│   └── testvec/               读取 testdata/vectors.json（仅测试用）
├── docs/PROTOCOL.md
├── testdata/vectors.json
└── tools/verify_vectors.py
```

分层原则：`pppp` 与 `miss` 不依赖 `cloud`，`ts` / `stream` 只依赖 `miss.Packet`。
这样 `ts` 可以脱离网络单测，`pppp` 可以脱离相机单测。

**Java → Go 对照表**

| Java | Go |
|---|---|
| `micam.pppp.Frame` | `pppp/frame.go` |
| `micam.pppp.Msg` | `pppp/msg.go`（常量 + `Name()`） |
| `micam.pppp.Uid` | `pppp/uid.go` |
| `micam.pppp.AddrBlock` | `pppp/addr.go` |
| `micam.pppp.InitString` | `pppp/initstring.go` |
| `micam.pppp.RelaySession` | `pppp/relay.go` |
| `micam.pppp.DataChannel` | `pppp/drw.go` |
| `micam.pppp.PpppConnection` | `pppp/conn.go` |
| `micam.pppp.MissSetup` | `pppp/setup.go` |
| `micam.cloud.*` | `cloud/*.go` |
| `micam.miss.*` | `miss/*.go` |
| `micam.server.TsMuxer` | `ts/muxer.go` |
| `micam.server.HttpStreamServer` | `stream/server.go` |
| `micam.cli.*` | `cli/*.go`、`cmd/micam/main.go` |
| `micam.creds.*` | `creds/*.go` |

---

## 3. 核心接口

### 3.1 线格式（纯函数，无状态）

```go
package pppp

type Frame struct {
    Type    byte
    Payload []byte
}

func EncodeFrame(t byte, payload []byte) []byte          // F1 | t | len BE | payload
func ParseFrame(buf []byte) (Frame, bool)                // 声明长度可大于实到长度

type UID struct {
    Prefix string
    Serial uint32
    Check  string
}

func ParseUID(s string) (UID, error)                     // "ABCDEF-123456-GHIJK"
func (u UID) Encode() [20]byte
func DecodeUID(b []byte) UID

type Addr struct {
    IP   netip.Addr
    Port uint16
}

func (a Addr) Encode() [16]byte                          // family BE / port LE / IP 反转
func DecodeAddr(b []byte) Addr
func (a Addr) Compact() [8]byte                          // family BE / port BE / IP 正序

func DecodeInitString(enc string) (string, error)        // 尾随逗号保留
func Servers(enc string) ([]netip.Addr, error)
```

### 3.2 中继会话（阶段式，独占 socket 读）

```go
package pppp

type Session struct {
    conn   *net.UDPConn
    uid    UID
    relay  *net.UDPAddr
    token  [4]byte
    servers []netip.Addr
    log    *slog.Logger
}

// Dial 完成 PROTOCOL.md §8.5 的 1–9 步。
// 返回后 socket 内可能仍有未读的 RLY_RDY 重复包，由 Conn 的读循环自然丢弃。
func Dial(ctx context.Context, conn *net.UDPConn, uid UID,
          servers []netip.Addr, timeout time.Duration, log *slog.Logger) (*Session, error)

func (s *Session) Relay() *net.UDPAddr
func (s *Session) Token() [4]byte
```

设计要点：**握手与后续会话共用同一个 `*net.UDPConn`**，由不同阶段独占读侧
（握手期同步读，会话期交给 `Conn` 的后台 goroutine）。这等价于 Java 的两段式，
避免引入订阅/多路复用而引入时序偏差。

### 3.3 会话通道

```go
package pppp

type Conn struct {
    conn  *net.UDPConn
    relay *net.UDPAddr
    local Addr
    cmdCh   *drw.Channel
    mediaCh *drw.Channel
    cmds  chan []byte
    media chan []byte
    // ...
}

func NewConn(conn *net.UDPConn, relay *net.UDPAddr, local Addr, log *slog.Logger) *Conn
func (c *Conn) Start()                                    // 启动唯一读循环
func (c *Conn) SendCommand(cmd uint32, body []byte) error // <u32 len><u32 cmd><body>
func (c *Conn) NextCommand(ctx context.Context) ([]byte, bool)
func (c *Conn) NextMedia(ctx context.Context) ([]byte, bool)
func (c *Conn) Close() error
```

读循环职责（缺一不可）：DRW 分片 ACK、乱序重组、`ALIVE` 回 `ALIVE_ACK`、
`0x28` 回写、`CLOSE` 退出。

### 3.4 MISS

```go
package miss

type Keys struct {
    Public  [32]byte
    Private [32]byte
}

func GenerateKeys() (Keys, error)                          // 用 x/crypto/curve25519
func SessionKey(priv [32]byte, peerPubHex string) ([32]byte, error)
func Decrypt(key [32]byte, payload []byte) ([]byte, error) // nonce = 4×0 || payload[:8]

type Client struct{ /* conn *pppp.Conn; key [32]byte */ }
func (c *Client) Authenticate(ctx context.Context) (string, error)
func (c *Client) StartVideo(quality int, audio bool) error
func (c *Client) StopVideo() error
func (c *Client) NextMedia(ctx context.Context) (*Packet, bool)

type Packet struct {
    Codec   uint32
    Seq     uint32
    Flags   uint32
    TS      int64
    Payload []byte
}
func (p Packet) Keyframe() bool
```

**不手写 X25519/ChaCha20**：`x/crypto/curve25519`、`x/crypto/nacl/box`（`Precompute`
即 `crypto_box_beforenm`）、`x/crypto/chacha20`（12 字节 nonce，计数器从 0 开始）。
只保留 30 行 RC4（需要 drop 1024，标准库不给）。

### 3.5 TS 复用器

```go
package ts

type Muxer struct { /* 每观众一份 */ }

func NewMuxer(w io.Writer, codec uint32) *Muxer
func (m *Muxer) Write(p miss.Packet) error
func (m *Muxer) Flush() error
```

保持：PCR 落后 PTS 27000 tick、自造单调时钟、每 20 帧重发表、关键帧 RA 标记。

### 3.6 HTTP 分发

```go
package stream

type Config struct {
    Addr string // "--bind" + "--port"，默认 127.0.0.1:8554
    Path string // "/cam.ts"
}

type Server struct{ /* mu, viewers map[*viewer]struct{} */ }

func New(cfg Config, log *slog.Logger) *Server
func (s *Server) Start() error
func (s *Server) Publish(p miss.Packet)   // 非阻塞，慢观众丢帧
func (s *Server) Close() error
```

要点：`http.Server{WriteTimeout: 0}`（长连接）、`http.Flusher`、每个连接独立
`Muxer`、等到关键帧才开始写。

### 3.7 组装

```go
// cmd/micam/main.go
func run(ctx context.Context, opts cli.Options) error {
    creds := creds.Load(...)
    c := cloud.New(creds)
    if !c.HasSession() { c.Login(ctx, password) }
    dev, err := c.Camera(ctx, opts.DID)
    prov, err := c.Provision(ctx, dev.DID)
    keys, _ := miss.GenerateKeys()          // 注意：须在 Provision 之前生成
    s, err := pppp.Dial(ctx, udp, uid, prov.Servers, opts.ConnectTimeout, log)
    conn := pppp.NewConn(...); conn.Start()
    cli := miss.NewClient(conn); cli.Authenticate(...); cli.StartVideo(...)
    srv := stream.New(...); srv.Start()
    for { p, ok := cli.NextMedia(ctx); if !ok { break }; srv.Publish(*p) }
}
```

---

## 4. 并发拓扑

```
        ┌──────────────────── udpConn ────────────────────┐
        │                                                 │
  handshake phase                                session phase
  (Dial, 同步读)                                 (Conn.rx goroutine)
                                                        │
                                        ┌───────────────┼───────────────┐
                                        ▼               ▼               ▼
                                  cmdCh(chan)     mediaCh(chan)     回 ACK / 0x28
                                        │               │
                                        ▼               ▼
                                   MISS 应答循环   主循环 → stream.Publish
                                                        │
                                              ┌─────────┴─────────┐
                                              ▼                   ▼
                                          viewer A chan       viewer B chan
```

- 只有 **一个** goroutine 读 socket。其余 goroutine 只写（`UDPConn.Write` 并发安全）。
- 队列都有界：`cmd` 32、`media` 256、每观众 60；满了就丢（媒体可丢，命令不可）。
- 退出：`signal.NotifyContext` → 取消 context → `stopVideo` → 关 socket → 关 HTTP。

---

## 5. 配置与凭据

| 来源 | 键 | 说明 |
|---|---|---|
| flag | `--user --region --did --port --bind --quality --connect-timeout --relogin --verbose` | |
| env | `MICAM_USER` `MICAM_REGION` `MICAM_DID` | 容器友好 |
| env（不建议） | `MICAM_PASSWORD` `MICAM_PASS_TOKEN` | 会出现在 `docker inspect` |
| 文件（推荐） | `MICAM_PASS_TOKEN_FILE` | 内容即 token，权限 0600 |
| 文件 | `<home>/.micam/credentials.json` | 自动读写，0600 |

优先级：flag > env > 文件。**首次运行仍可交互**，但预置 credentials 后完全零交互。

---

## 6. go2rtc 可复用部分

go2rtc（MIT）已实现同一套小米云与 MISS 协议（仅 LAN，无 relay）。建议**借知识、不 import**，
因为它的 API/payload 与 micam 有细微差别，直接依赖会把两套抽象焊死。

| 上游 | 对应 | 可省 |
|---|---|---|
| `pkg/xiaomi/cloud.go` | 登录、签名、`device_list` | 约 300 行 + 签名踩坑 |
| `internal/xiaomi/xiaomi.go` | `miss_get_vendor` 请求形状、`devicepass` | 约 100 行 |
| `pkg/xiaomi/miss/client.go` | 命令表、媒体头、codec id | 约 150 行 |
| `pkg/xiaomi/crypto/crypto.go` | `beforenm` + ChaCha20 分帧 | 设计参照（Go 版用 `nacl/box.Precompute`） |
| `pkg/xiaomi/miss/cs2/conn.go` | LAN 直连（本实现不用） | 仅作对照 |

**它没有 relay 传输**——`pppp/relay.go` 仍是全新的约 350 行，也是移植的真正难点。

---

## 7. 依赖

| 模块 | 用途 | 备注 |
|---|---|---|
| `golang.org/x/crypto` | curve25519 / nacl/box / chacha20 | 无 cgo |
| `golang.org/x/term` | 无回显密码输入 | 仅 TTY 分支 |
| 标准库 | 其余全部（含 RC4、JSON、HTTP） | `CGO_ENABLED=0` 可静态链接 |

不引入 cobra / viper / zap：`flag` + `slog` 足够，减少依赖面。

---

## 8. 里程碑与验收

| 里程碑 | 内容 | 验收 |
|---|---|---|
| M0 | 仓库骨架 + `testvec` 载入 + 空的表驱动测试 | `go test ./...` 能跑，向量用例标红 |
| M1 | `pppp` 线格式 + initstring + 0x28 + DRW | 向量组 `uid/addrblock/frame/relay_port_ack/initstring/miss_setup/drw` 全绿 |
| M2 | `cloud` 登录 + 签名 + RC4 | `cloud` 组全绿；真机登录拿到 `device_list` |
| M3 | `miss` 密钥 + 客户端 | `crypto` 组全绿 |
| M4 | `pppp.Dial` + `Conn` | 真机 `RELAY SESSION ESTABLISHED` |
| M5 | `ts` + `stream` + `cli` | `ffprobe` 无错、mpv/VLC 连续播放 ≥10 分钟 |
| M6 | Dockerfile（arm64）+ compose | fnOS 上 `docker compose up` 出画面 |

M1–M3 不需要摄像头，是本轮之后可以立刻推进的部分。

---

## 9. 与上游的有意差异

| 项 | 上游 | 本实现 | 理由 |
|---|---|---|---|
| 监听地址 | 硬编码 `127.0.0.1` | `--bind`，默认仍是 `127.0.0.1` | 容器可访问；默认保持安全 |
| 凭据来源 | 仅交互 + 文件 | + env / token 文件 | 无人值守 |
| 日志 | `System.out` | `slog`（text/json） | 可被容器采集 |
| 重连 | 断流即退出 | 仍退出，但可选 `--reconnect N` | 交给编排层更简单，可选自愈 |
| 观众队列 | 60 帧 | 60 帧 | 保持一致 |

协议层**零差异**——所有差异都在部署与运维面。

---

## 10. 风险

1. **本机无 Go 工具链、无直连网络**：代码需在你的机器上 `go mod tidy && go test ./...` 验证。
   因此实现策略是"先把纯函数层做到向量全绿”，网络层最后接。
2. **时序参数不可推导**：2500 ms 窗口 / 24 连发 等来自真机，照抄不优化。
3. **上游会漂移**：小米改协议时，Java 上游修了，Go 侧要跟。建议在 README 记录上游 commit。
4. **无 LICENSE**：自用可以，分发镜像前需征得作者同意。
