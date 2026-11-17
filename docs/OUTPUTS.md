# 输出协议：HLS（P0 接口设计）

## 1. 为什么要做

现在只有一条输出：MPEG-TS over HTTP。它能被 mpv / VLC / ffmpeg 播，但 **iOS 和 macOS 的
Safari 只认 HLS**——不能装第三方播放器的地方就看不了。

已经用 ffmpeg 验证过：`-c copy -f hls -hls_segment_type fmp4` 出来的流在 Edge / Safari 里
直接播，2880×1620 的 H.265 零转码。技术前提成立，缺的是把它做进程序里。

## 2. 约束

- **不转码**。分辨率、码率、编码都不变。
- 源是 H.265，所以 HLS 只能用 **fMP4（CMAF）分片**，MPEG-TS 分片对 HEVC 不成立。
- 延迟由 GOP 决定，做不到 LL-HLS 那种 1 秒以内；目标是 4–6 秒，和 ffmpeg 方案一致。

## 3. 现状

| 位置 | 事实 |
|---|---|
| `stream/server.go:41` | `Server` 只服务一个路径（`Config.Path`） |
| `stream/server.go:373` | `pump` 每来一个观众就 `ts.NewMuxerWithLead` 一个复用器 |
| `stream/server.go:326` | `offer` 负责"从关键帧起播"和"落后时丢积压、重同步" |
| `stream/server.go:221` | `Publish` 同步扇出，不阻塞 |

## 4. 要守住的不变量

改完之后，这四条必须还成立：

1. `Publish` 永不阻塞摄像头读取。
2. 新观众从关键帧起播。
3. 观众落后时丢积压并重同步，而不是丢单帧。
4. **`/cam.ts` 的输出逐字节不变**——现有测试一行都不改，跑绿。

第 4 条是重构的验收标准。

## 5. 设计：把"分发"和"封装"分开

现在 `pump` 里直接 `ts.Muxer`，封装格式和连接生命周期绑死了。抽一层：

```go
// internal/sink
package sink

// Sink 把一帧媒体封装成某种线格式并写出去。
// Write 只会在一个 goroutine 里被调用，实现不需要自己加锁。
type Sink interface {
	Write(p miss.Packet) error
	Close() error
}

// Factory 为一条新连接创建 Sink。codec 在第一帧到达时才知道。
type Factory func(w io.Writer, codec uint32) Sink
```

`stream.Config` 增加一个字段，默认值是现有的 TS 适配器：

```go
type Config struct {
	Addr      string
	Path      string
	PTSLeadMS int
	Sink      sink.Factory // nil 表示 MPEG-TS，行为与现在完全一致
}
```

`viewer.pump` 里只改一行：`muxer = ...` → `s := cfg.Sink(w, p.Codec)`。

**P0 到此为止**，不新增任何端点。这一步合并后，用户看到的行为不变。

## 6. HLS 的形状不一样

TS 是"每个连接一个复用器"，因为它只写一条流。HLS 不是——**分片必须全局只切一次**：
所有观众看的是同一条流，各切一份不但浪费 CPU，还会让同一时刻的观众拿到不同的分片编号。

所以 HLS 是"一个生产者 + N 个读者"：

```go
// internal/hls
type Segmenter struct{ /* 环形缓冲，不落盘 */ }

func (s *Segmenter) Write(p miss.Packet) error        // 单生产者，由 Publish 驱动
func (s *Segmenter) Playlist() []byte                 // /cam.m3u8
func (s *Segmenter) Init() []byte                     // /cam/init.mp4
func (s *Segmenter) Segment(seq uint64) ([]byte, bool) // /cam/seg<N>.m4s
```

`Segmenter` 同时也是一个 `sink.Sink`（满足 `Write`），但全局只有一个实例，
和"每连接一个"的 TS 分处两条扇出分支：

```
Publish(p) ─┬─► viewer（每连接）──► ts.Sink ──► HTTP body
            └─► segmenter（唯一）──► playlist / init / segments
```

## 7. 路由

| 路径 | 内容 | 谁用 |
|---|---|---|
| `/cam.ts` | 现有 TS | mpv / VLC / ffmpeg / Frigate |
| `/cam.m3u8` | 播放列表 | Safari / iOS / Edge |
| `/cam/init.mp4` | fMP4 初始化段（含 `hvcC`） | 同上 |
| `/cam/seg<N>.m4s` | 分片 | 同上 |

## 8. 分段器要点

- **只在关键帧切**。分片边界必须落在 I 帧上，否则后一个分片无法独立解码。
- **滑动窗口 6 段**，内存环形缓冲，不落盘。避免 ffmpeg 方案里"强杀留下孤儿分片"的问题。
- 序号单调递增，播放列表暴露 `EXT-X-MEDIA-SEQUENCE`，窗口滚动时最老的分片被覆盖。
- **参数集从流里抓**：每个 IDR 前都有 VPS/SPS/PPS，用它生成 `hvcC` 和 init 段。
- `tfdt` 用 90kHz 时间戳，复用 `ts.Muxer` 里那套单调修复逻辑（抽成共用函数，别抄两份）。
- 分片时长是 `max(配置值, GOP 长度)`。这台摄像头 GOP ≈ 2s，所以 `-hls_time 1` 实际出 2s 分片。

## 9. 配置与界面

- `--hls`：开关，默认开（多一个端点不增加成本）。
- 状态文件增加 `hls_url`，与其他地址一起发布。
- 播放卡片上多一行 HLS 地址，iOS 用户直接点。

## 10. 测试

| 层 | 内容 |
|---|---|
| 盒子结构 | `ftyp` / `moov` / `moof` / `mdat` 的字节级断言，加进 `testdata/vectors.json`，`verify_vectors.py` 独立复算 |
| 分段器 | 纯函数测试：给定帧序列，断言切点、序号、窗口滚动、播放列表文本 |
| 端到端 | CI 里 `ffprobe http://127.0.0.1:PORT/cam.m3u8`，断言 codec / 分辨率 / 帧数；合成帧源，不需要真机 |
| 回归 | 现有 `internal/ts` 与 `internal/stream` 测试全绿且不改动 |

## 11. 分阶段

| 阶段 | 内容 | 行为变化 |
|---|---|---|
| P0 | `sink` 抽象 + TS 适配器 | 无 |
| P1 | `internal/hls`：分片器、播放列表、`hvcC` | 新增 `/cam.m3u8` |
| P2 | 状态文件、界面、`--hls` 开关 | 界面上多一个地址 |
| P3 | 真机：Safari / iPhone / Edge 各播一次 | — |

## 12. 决定与实现状态

三个问题已经定了：

| 问题 | 决定 |
|---|---|
| HLS 默认开还是默认关 | **默认开**（`--hls`），关掉就退回只有 `/cam.ts` |
| 窗口大小 | **6 段**，`--hls-window` 可调 |
| 是否保留 `/cam.ts` | **保留**，它是现有测试和文档的基准 |

实现状态：

- **P0 完成**：`internal/sink` 抽象，TS 适配器，`stream.Config.Sink`。
- **P1 完成**：`internal/hls` —— Annex-B 拆分、SPS 解析、`hvcC`、fMP4 分片、滑动窗口、
  播放列表、`/cam.m3u8` 与 `/cam/{init.mp4,segN.m4s}` 三个端点。
- **P2 完成**：状态文件里的 `hls` 地址、启动横幅、`--hls` / `--hls-window` 两个参数、
  配置界面里的开关与窗口、应用设置（wizard）里的对应字段。
- **P3 完成**：真机（`xiaomi.camera.92ac1`，2880×1620）在 Edge 与 Chrome 里直接播放
  `http://<nas>:8554/cam.m3u8`，分片持续滚动。Safari / iPhone 未测。

## 13. 一个必须记住的教训

调通这段花了四轮，每一轮的原因不同，但错误是同一个：**把 ffmpeg 当验收标准**。

| 现象 | 真因 | 为什么 ffmpeg 没报 |
|---|---|---|
| 浏览器停在 0:00 | `moof` 里少了 `traf` 容器 | 它扫描 `tfhd`/`trun`，不管层级 |
| 同上 | 用 `hvc1` 但参数集不在码流里 | 它容错地回退到 `hvcC` |
| 同上 | 分片缺 `styp`、样本尺寸写成编码尺寸 | 两者都是可选的 |
| `APPEND_FAILED` | 单位矩阵写成 32 字节，`mvhd`/`tkhd` 各短 4 字节 | 它按字段偏移读，从不校验盒子声明长度 |

正确的验收方式是**按规范和参照实现逐字节对齐**：

- `trak` 现在与 ffmpeg 输出**逐字节相同**（546 字节）
- 测试钉死 `mvhd` = 108、`tkhd` = 92 这两个规范固定长度
- 分片结构 `styp + sidx + moof/traf + mdat` 与参照一致

`ffmpeg -f null` 能解出 100 帧只证明"它宽容"，不证明"我们正确"。下次写任何容器格式，
先拿参照实现做字节对比，再谈播放。

已知限制：

- 只处理 H.265；H.264 要走 `avcC`，目前直接忽略（不会产出坏的 init）。
- 分片时长由 GOP 决定，这台摄像头约 2 秒，所以 `--hls-window 6` 大约是 12 秒的缓冲。
- 不支持 LL-HLS（partial segments）。

验证记录：真实抓包（2880×1620，100 帧）跑通分片器，`ffmpeg -f null` 解码 100/100 帧；
HTTP 端点有集成测试覆盖；2026-11-17 在真机上由 Edge 与 Chrome 直接播放成功。
