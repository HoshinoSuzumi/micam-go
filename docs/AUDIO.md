# 音频支持方案（先设计，不动代码）

## 1. 结论

1. **把音频拉下来不难。** `0x102` 的 body 里 `enableaudio` 置 1，音频就和视频一起走 channel 2，
   靠 32 字节明文头里的 codec id 区分；`miss.Packet` 已经能解出 codec / flags / 时间戳。
   协议层几乎不用改，`StartVideo` 早就留了 `audio bool` 参数，只是 `session.go` 传的是 `false`。
2. **难的是输出。** 这台相机大概率是 G.711 A-law（PCMA），而 **MPEG-TS 没有 G.711 的位置**：
   ffmpeg 只肯把它写成 `0x06` 私有流、自己都读不回来；VLC 的 PMT 表里也没有 A-law。
   实测见 §3.3。所以"`/cam.ts` 一条流里音画都有、mpv / VLC / Safari 都能放"这件事，
   **必须把音频转成 AAC**（或者相机本身给 Opus）。
3. **能不转码的路线只有两条**：相机给 Opus（透传），或者音频单独一条地址（旁路）。
   如果接受旁路，1–2 天就能听到声音，且 `/cam.ts` 一个字节都不用改。

推荐顺序：**先 P0 实测 → 先落旁路 → 再决定合流**。旁路要写的代码（包分类、缓冲、时间轴、
状态、界面、丢弃策略）在合流方案里 100% 复用，不是白做。

工作量：能听到声音 1–2 人日；`/cam.ts` 音画合流并让通用播放器能放 2–4 人日
（要引入音频编码器）；HLS 也带音频再 +2–3 人日。

## 2. 目标与非目标

目标（按优先级）：

1. 能听到摄像头的声音。
2. 不破坏视频路径：`--audio=off`（默认）时 `/cam.ts` **逐字节不变**。
3. 音频是附加项：没有音频包、格式不认识、编码器起不来，都只是"没声音"，不能影响画面、不能触发重连。

非目标：

- 双向对讲（channel 3 出站音频、`0x106`/`0x108`）。独立功能，别混进来。
- 回放（`0x10D`）、多摄像头。
- 视频转码。永远不做。

## 3. 已知事实

### 3.1 协议（来自上游实现，不是猜的）

| 事实 | 出处 |
|---|---|
| 音频与视频**同一个 channel 2**，靠 32 字节明文头区分：4=H.264、5=H.265、1024=PCM、1026=PCMU、**1027=PCMA**、**1032=Opus** | go2rtc `pkg/xiaomi/miss/client.go` 常量表；MiBeeNvr `internal/xiaomi/miss.go` |
| 开音频最直接的方式是 `0x102` 里 `{"videoquality":q,"enableaudio":1}`，go2rtc 默认发 1 | go2rtc `StartMedia` |
| `0x104`/`0x105` 是独立的 audio start/stop；go2rtc 实现了但主线不调用，MiBeeNvr 用它做单独的音频流程。**本机型是否需要补 `0x104`，要实测** | go2rtc `StartAudio`；MiBeeNvr `StartAudio` |
| 采样率不在命令里，在**每包 header 的 flags**：`(flags>>3)&0xF != 0` → 16000，否则 8000 | go2rtc `Packet.SampleRate`；issue #2006 的修复就是补这段 |
| 解密后的音频 payload 是**裸 A-law**（1 字节 = 1 采样），没有每帧子头；go2rtc 直接塞进 RTP，并用 `len(payload)` 推进时间戳。Opus 则按 40 ms/包推进 | go2rtc `producer.go` |
| 音频时间戳不可依赖：go2rtc 干脆不用相机的 ts（LoockV2 音频 ts 恒为 0，别家也不保证单调） | go2rtc `ReadPacket` 的 model 分支 |
| 另有 `0x10B`/`0x10C` get audio format，可以直接问相机要音频格式；回包结构无文档，值得在探测里打出来 | MiBeeNvr `GetAudioFormat` |

### 3.2 本仓库现状

| 位置 | 现状 |
|---|---|
| `internal/miss/client.go:103` | `StartVideo(quality int, audio bool)` 已经能发 `enableaudio`，`cmd/micam/session.go:228` 传的是 `false` |
| `internal/miss/packet.go` | 已有 1027/1032 常量、`IsVideo()`、`CodecName()`；缺 `IsAudio()` |
| `cmd/micam/session.go:328` | `if !packet.IsVideo() { continue }` —— 音频在这里被丢掉 |
| `internal/ts/muxer.go` | 只写一路视频：PMT 一个 ES（0x1B/0x24，PID 0x100），PES 只带 PTS，PCR 在视频 PID 上 |
| `internal/sink/sink.go` | 每连接一个封装器的工厂（`Factory(w, codec)`），默认 `TS`。这是"第二种输出格式"的接缝，但它按**视频** codec 建，且要同时写音视频两轨 |
| `internal/stream/server.go` `viewer.offer` | 观众从**关键帧**起播；队列按"帧"计（25 帧≈1 秒）。音频没有关键帧，混进来会互相触发重同步 |
| `internal/stream/server.go` `viewer.pump` | 第一帧到达时 `newSink(w, p.Codec)` 建封装器；音频若先进会拿音频 codec 去建 TS 封装器（错） |
| `internal/hls/segmenter.go` `Write` | 现在直接丢弃非 H.265 包。音频要作为第二条轨加在这里，而不是让它被丢掉 |
| `internal/cli/options.go`、`internal/ctl/config.go`、`internal/status/status.go` | 都没有音频开关/字段 |
| `docs/PROTOCOL.md` | §10 说 channel 3 是"出站音频（未实现）"、§11.2 说 `0x104/0x105` 未实现、§11.4 写 `enableaudio:0` —— 都要更新 |

### 3.3 播放器与封装（本机实测，ffprobe 9.0.2）

同一段 8 kHz 单声道 A-law，手工封成几种 TS 再看 ffprobe 认什么：

| 封装 | ffprobe 结论 |
|---|---|
| stream_type `0x90` + PES `0xC0` + 裸 A-law | `codec_name=unknown` |
| stream_type `0x06` + PES `0xBD` + 裸 A-law | `codec_name=bin_data` |
| ffmpeg 自己 `-c copy -f mpegts` 出来的 A-law | 也是 `0x06`，**自己读回来同样 `bin_data`** |
| `0x80` + PMT 注册描述符 `HDMV` + Blu-ray LPCM 帧 | 被识别为 `pcm_bluray`（48 kHz/单声道/16 bit）；但解码器只收 **48/96/192 kHz**、16/20/24 bit，单声道还要按 2 声道承载 → 48 kHz/16 bit 双声道 = **1.5 Mbps** |

结论：

- **G.711 在 TS 里没有通用位置。** 标准表里没有它，ffmpeg 的 muxer 只能落 `0x06` 并警告
  "may not be recognized upon reading"；VLC 的 PMT 表里 `0x90` 是 Blu-ray 图形（PG），
  `0x83`/`0x80` 是 LPCM，同样没有 A-law。把 PCMA 裸塞进 TS ≈ 写一个没人能播的 PID。
- TS 能"通用地"带 PCM，但代价是 48 kHz 起、双声道 1.5 Mbps —— 为 64 kbps 的语音付 24 倍带宽，不划算。
- A-law **进不了 MP4**（ffmpeg：`Could not find tag for codec pcm_alaw in stream`，只能 `.mov`），
  所以 HLS/fMP4 的音频只能是 AAC 或 Opus。
- 反过来，**8 kHz AAC 在 TS 里能被正常识别**（实测 `aac, 8000 Hz, mono`），HLS 也认。

### 3.4 这台相机到底给什么？必须实测

`xiaomi.camera.92ac1` 不在任何公开机型表里，音频 codec / 采样率 / 时间戳只能测。
已知分布：老机型多为 PCMA 8k（个别"非标 PCMA"实际是 16k，见 go2rtc #2006），
2024 年后的新机型（如 `isa.camera.cw501d`）直接给 Opus 48k。

## 4. 方案

### 4.1 路线 A：音频旁路 `/cam.wav`（推荐先做）

```
camera ──channel 2──► miss.Packet(1027) ──A-law→s16 查表──► audioHub ──► GET /cam.wav
                                   └── 视频照旧 ──► GET /cam.ts（不动）
```

- A-law → 16 bit PCM 是查表，不是转码：64 kbps → 128 kbps，可逆，不需要编码器，不需要外部依赖。
- 用 WAV 而不是裸 `.alaw`：`<audio src>`、mpv、VLC、ffmpeg、HA 都能直接放；
  裸 A-law 还得记着 `-f alaw -ar 8000 -ac 1`。
- 头部：44 字节 RIFF，`RIFF` 与 `data` 长度都写 `0xFFFFFFFF`（直播常规做法），
  `Content-Type: audio/wav`。个别播放器若不认，退回"裸 alaw + 文档说明"。
- 时间轴：按采样数推进，不用相机 ts；队尾有界（约 500 ms），跟不上就丢最旧。直播不需要补。
- 和视频的关系：两条独立连接、各自缓冲，**不保证**两个播放器音画同步（起点差几十到几百 ms）。
  管理界面里可以 `<video muted>` + `<audio>` 两个元素，用 `currentTime` 粗对齐。
- **不走 `sink.Factory`**：那个接缝是"每连接一个、按视频 codec 建、写音视频两轨"的封装器；
  `/cam.wav` 是"只订阅音频、自己写 WAV 头"的独立 handler + hub，混进 sink 反而拧。
- 优点：不转码、零依赖、`/cam.ts` 不动、坏了不影响视频。
- 缺点：不是一个流；外部播放器"开一个地址就音画都有"做不到。

这是"我就想听听声音"的最低成本答案，也是后面所有方案的公共地基。

### 4.2 路线 B：TS 里带音频

#### B1 相机给 Opus（最好情况，零转码）

- TS：`0x06` + 注册描述符 `Opus` + DVB extension descriptor `0x80`（channel config）。ffmpeg 认这个组合。
- fMP4/HLS：`Opus` sample entry；Safari 对 fMP4 里的 Opus 要实测。
- 工作：PMT 加第二个 ES、PES `0xC0`、PTS 按每包 40 ms 推进。

#### B2 相机给 PCMA（大概率）→ 转 AAC

"一条流 + 通用播放器 + HLS 也能用"的唯一路线。TS 用 `0x0F`（ADTS AAC），
fMP4 用 `mp4a` + `esds`。编码器二选一：

| 选项 | 代价 | 风险 |
|---|---|---|
| 外挂 `ffmpeg` 子进程（`-f alaw -ar 8k -i pipe:0 -c:a aac -ar 48000 -f adts pipe:1`） | 镜像里多一个 ~70 MB 依赖；进程要守着（崩了重启、退出要收尾）；延迟 +20–130 ms | 成熟、参数好调 |
| 纯 Go AAC（如 `tphakala/go-aac`） | 新依赖（LGPL-2.1，还依赖 simd 包）；只支持 44.1/48 kHz → 必须重采样 | 新库、单一作者；和"只依赖 x/crypto"的原则冲突 |

倾向**外挂 ffmpeg、默认关闭**（`--audio=aac`）：把不确定性关在一个进程里，坏了只是没声音；
Go 侧仍然静态、无 cgo、好交叉编译。注意 AAC 帧长 1024 采样：8 kHz 下是 128 ms/帧，
要先重采样到 48 kHz 才能把音频延迟压到 21 ms 量级。

### 4.3 方案 C（否掉）：把 PCMA 当 LPCM 塞进 TS

8k → 48k 上采样 + Blu-ray LPCM 能用（见 §3.3），但 1.5 Mbps、非标准用途、单声道还要补静音声道。
为语音付 24 倍带宽不值得。

### 4.4 TS 细节（到 B 阶段才用）

先说结构：音频轨有**两个落脚点**，各管一段。

- `/cam.ts`：在 `ts.Muxer` 里加第二路 ES（每观众一份，和视频共用同一个 `Clock` 基准）。
- `/cam.m3u8`：在 `hls.Segmenter` 里加第二轨（全局只有一个，`Publish` 已经在喂它；
  它现在 `Write` 开头就把非 H.265 丢了，要先把音频接住）。
- `stream.Publish` 要能同时接受音频包；`viewer` 只按**视频**包建 sink，
  音频包不能触发 `newSink`（否则会拿 1027 当 H.264/265 建 TS 封装器）。

然后才是封装细节：

- PMT 两个 ES：视频 PID `0x0100`（`0x1B`/`0x24`）、音频 PID `0x0101`（`0x0F` 或 `0x06`+Opus）。
  **PCR 仍留在视频 PID**。
- 每个 PID 独立的 continuity_counter；音频 PES 可以带长度（帧小，远小于 64 KB），stream_id `0xC0`。
- PMT 现在"每个关键帧 + 每 20 帧"发一次。音频 ES 必须在**第一个音频包到达前**就出现在 PMT 里，
  否则观众先看到的是视频-only 的 PMT。做法：会话启动时先"探"到视频参数集 + 一个音频包，
  再开 HTTP server（思路和 go2rtc 的 probe 一样），这样第一帧起 PMT 就是完整的。
- 音频 PTS **不要**直接用相机 ts，用采样数建时间轴（`pts += samples * 90000 / rate`），
  再用到达时间做慢速校正：
  - 正常时样本计数是精确的；
  - UDP 丢包会让计数落后于真实时间 → 用 `Packet.Seq` 的缺口把缺的时长补上（按平均包时长估）；
  - 偏差超过约 300 ms 才跳变（直播丢一小段可以接受），小于就靠样本计数，别动；
  - 起播：音频 PTS 一律 ≥ 该观众第一个视频关键帧的 PTS，避免"先响后画"。
- 观众队列：视频和音频**分开**。现在 `offer()` 的"落后就丢积压、等关键帧"对音频是错的
  （音频没有关键帧，混在一个队列里会互相触发重同步）。建议 `viewer` 拆成
  `{video chan, audio chan}`：音频按毫秒（500 ms）有界、丢最旧；视频重同步时顺手清空音频。
- HLS：`OUTPUTS.md` 的全局 `Segmenter` 里再加一条音频轨，init 段要带 `esds`，分片各自 `tfdt`。

## 5. 配置与界面

| 位置 | 新增 |
|---|---|
| CLI | `--audio=off|on|auto`（默认 `off`）、`--audio-codec=passthrough|aac`；`auto` 表示"相机给什么就透传什么，不认识就不出音频" |
| 环境 | `MICAM_AUDIO`、`MICAM_AUDIO_CODEC`（与现有约定一致） |
| `internal/ctl/config.go` + `web/` | 配置卡片加一个"音频"开关；播放卡片加 `/cam.wav`（旁路）或说明 |
| `internal/status/status.go` | `audio: {codec, rate, packets, dropped, encoder}`，界面显示"有声音 / 无音频包 / 编码器未启动" |
| `PROTOCOL.md` | 音频 codec 表、`enableaudio`、flags 里的采样率、`0x104`/`0x10B`、channel 2 音视频交织 |
| `README.md` | "不转码"改成"**视频**不转码；音频可选转码（`--audio=aac`）" |

## 6. 分阶段与工作量

| 阶段 | 内容 | 行为变化 | 人日 |
|---|---|---|---|
| P0 探测 | `--probe-audio`：打 codec id / 包长 / flags / 采样率 / 相机 ts 单调性 / seq 缺口 / 前 16 字节 hex；可选试 `0x104`、`0x10B` | 只多日志 | 0.5 |
| P1 管道 + 旁路 | `IsAudio()`、`audioHub`、`/cam.wav`、`--audio`、状态字段、界面开关、`--dump-audio` | 新增 `/cam.wav`；`/cam.ts` 不变 | 1–1.5 |
| P2 合流 | PMT 双 ES、音频 PES/PTS、观众队列拆分、A/V 同步、ffmpeg 编码器（若 PCMA） | `--audio` 打开时 `/cam.ts` 带音频 | 2–4 |
| P3 HLS 音频 | fMP4 第二轨、init 段 `esds`、分片 `tfdt` | `/cam.m3u8` 带音频 | +2–3 |
| P4 打磨 | 长测、漂移、重连、向量、文档 | — | 1–2 |

## 7. 风险

1. **播放器兼容是硬约束，不是工程量。** PCMA in TS 没有任何通用播放器认（§3.3），
   任何"直接透传"的承诺都是假的。
2. 开音频后相机行为可能变（码率、GOP，甚至只给音频不给视频）——必须能一键关掉，且默认关。
3. A/V 同步：UDP 丢包 + 相机音频 ts 不可用，只能靠样本计数 + 校正；做不好会漂几百 ms。
4. 音频转码打破"不转码 / 零依赖"两条卖点（视频仍然零转码），README 要写清楚。
5. HLS 一旦开工，音频就被锁死在 AAC（A-law 进不了 MP4）；顺序上先定音频 codec，再做 HLS 分片。
6. `enableaudio:1` 未必对所有机型生效，要补 `0x104` 兜底；别写死一条路。

## 8. 未决问题（P0 要回答）

1. codec 是 1027 还是 1032？
2. 采样率 8k 还是 16k？会话中会变吗？
3. `enableaudio:1` 够不够？要不要 `0x104`？发 `0x104` 的时机在 video start 前还是后？
4. 音频包时长（20 ms / 40 ms / 其它）与 seq 是否连续？
5. 相机音频 ts 是否单调、单位 ms、与视频同基准？（决定要不要完全忽略它）
6. 开音频后视频码率 / GOP 有没有变化？

## 9. 验收

- `--audio=off`：`/cam.ts` 与现在的二进制输出**逐字节相同**；现有测试一行不改，全绿。
- 旁路：`ffprobe http://…/cam.wav` → `pcm_s16le, <rate> Hz, mono`；
  `ffmpeg -i http://…/cam.wav -t 10 -f null -` 无错误；浏览器 `<audio>` 能放。
- 合流：`ffprobe http://…/cam.ts` 两路流（hevc + aac/opus）；`ffmpeg -i … -t 60 -f null -` 无错误；
  60 秒内音画偏差 < 200 ms（拍手/打点实测）。
- 合成源端到端：假 Transport 按实时节奏产视频+音频，CI 里跑 ffprobe 断言，不需要真机。
- 真机：mpv / VLC / Safari 各播一次；30 分钟不掉链。

## 10. 复现 §3.3

```sh
ffmpeg -f lavfi -i "sine=frequency=440:sample_rate=8000:duration=1" -ac 1 -c:a pcm_alaw alaw.wav
ffmpeg -i alaw.wav -f alaw alaw.raw
# 手工封 PAT/PMT/PES 成三种变体（0x90+PES 0xC0 / 0x06+PES 0xBD / 0x80+HDMV+LPCM 帧）
ffprobe -show_entries stream=codec_name,codec_type,sample_rate,channels alaw_0x90.ts
ffmpeg -i alaw.wav -c copy -f mpegts self.ts && ffprobe self.ts   # ffmpeg 自己写的也读不回来
```

## 11. 参考

- go2rtc `pkg/xiaomi/miss/client.go`（codec 常量、`SampleRate`、`StartMedia`/`StartAudio`）
- go2rtc `pkg/xiaomi/miss/producer.go`（音频时间戳按样本数推进、Opus 40 ms）
- go2rtc issue #2006（flags bit3–6 → 16k 的修复）
- MiBeeNvr `internal/xiaomi/miss.go`（命令表 0x100–0x113、`enableaudio`、audio start）
- FFmpeg `libavformat/mpegtsenc.c`、`mpegts.c`、`libavcodec/pcm-bluray.c`
- VLC `modules/demux/mpeg/ts_psi.c`（0x90=PG、0x83/0x80=LPCM，无 A-law）

---

# 12. 结合 HLS 现状的补充（2026-11-18）

上一版写完时项目只有 MPEG-TS 一条输出。此后 HLS（`/cam.m3u8`，fMP4）落地并真机验证通过，
这让"音频放哪"的结论变了。

## 12.1 Apple 的硬约束把 B 路线逼到 AAC

[HLS 制作规范](https://developer.apple.com/documentation/http-live-streaming/hls-authoring-specification-for-apple-devices) 第 2 节：

- 2.2 能提供立体声的编解码器只有 **AAC-LC / HE-AAC v1,v2 / xHE-AAC / Apple Lossless / FLAC / APAC**
- 2.3 **必须提供 AAC 或 HE-AAC 格式的音频**

Opus 和未压缩 PCM 都不在这张表里。论坛实测 Opus 在 iOS 17+ 的 MP4 里能播、iOS 16 不能
（[Apple 开发者论坛](https://developer.apple.com/forums/thread/759210)），即"能用但不合规"。

结论：

```
PCMA → 想让 iOS 听到，只能 AAC 编码 → 需要编码器 → 与"零依赖静态二进制"冲突
Opus → 可透传，但只有 iOS 17+ 能听，旧设备静音
```

**路线 A 不解决 iOS 的音频**，这一点在上一版里没有说透：它是桌面/浏览器方案。

## 12.2 两条新的实测（本机 ffmpeg 9.0.2）

| 尝试 | 结果 |
|---|---|
| Opus 进 MPEG-TS | ✅ ffprobe 读回 `opus`（stream_type 0x06 + `Opus` 注册描述符） |
| 16-bit PCM 进 fMP4 | ✅ sample entry `ipcm`，8 kHz 单声道可读 |
| A-law 进 MPEG-TS | ❌ 仍是 `bin_data`（复核 §3.3，结论不变） |

`ipcm` 让"不转码把 PCM 放进 HLS"理论可行（A-law→PCM 只是查表），但 `ipcm` 同样不在
Apple 的清单里，需要真机验证，不建议作为主线。

## 12.3 路线 A 已实现

```
/cam.ts        视频（一个字节没动）
/cam.wav       声音：A-law → 16-bit PCM，44 字节 RIFF 头，长度填 0xFFFFFFFF
```

实现位置：

| 文件 | 内容 |
|---|---|
| `internal/audio/alaw.go` | G.711 A-law → 线性 16-bit，256 项查表 |
| `internal/audio/wav.go` | 流式 WAV 头（长度未知，播放器读直到断开） |
| `internal/stream/audio.go` | 独立观众队列与 HTTP 处理器 |
| `internal/stream/server.go` | `Publish` 按 `IsVideo`/`IsAudio` 分流，视频与音频互不串线 |

设计取舍：

- **A-law 解码按 ffmpeg 的逐字节结果做验收**：256 个码字全部对比，而不是"听上去对"。
- **队列 64 帧，满时丢最旧**。音频没有关键帧，没有"重同步"可做；保住"接近实时"比保住连续性重要。
- **Opus 直接跳过并告警一次**，不写进 WAV——写进去就是噪音。
- **音频不进视频观众队列**：观众的 sink 是拿第一帧的 codec 建的，1027 会造出一个不存在的 stream type。
- **不做重采样**：采样率由 `--audio-rate` 决定并写进 WAV 头，默认 8000（G.711 的定义）。

## 12.4 P0 仍然没做

打开 `--audio` 跑几分钟，日志里会出现：

```
msg="audio arrived" codec=PCMA bytes=160
```

`codec` 是 PCMA 还是 OPUS、`bytes` 决定帧长，这两个值决定要不要往 B 走。

## 12.5 判定树

```
audio arrived codec=OPUS  → B1（Opus 透传进 fMP4）：不转码，iOS 17+ 可用
audio arrived codec=PCMA  → 停在 A，除非接受 cgo + fdk-aac
```

B 阶段要改的两处已经写在 §4.2，不重复。
---

# 13. 路线 B1 已实现：Opus 进 HLS 音轨（2026-11-18）

## 13.1 结论先行

真机测出来相机给的是 **Opus（codec 1032）**，不是 PCMA。这改变了优先级：

- 路线 A 对这台相机**无效**：Opus 是压缩编码，展开不成 PCM，`/cam.wav` 只会输出 44 字节头
- 路线 B1 成立：Opus 包原样进 fMP4 的第二个 track，**零转码、零依赖**

## 13.2 实现

| 文件 | 内容 |
|---|---|
| `internal/hls/opus.go` | `Opus` sample entry + `dOps` 解码配置记录；从 TOC 读声道数 |
| `internal/hls/fmp4.go` | `moov` 第二个 `trak`；每个分片两个 `traf`（视频/音频），两段 `mdat` 数据按顺序排列 |
| `internal/hls/segmenter.go` | 音频包进自己的 pending 队列，随视频关键帧一起封片 |
| `internal/ts/clock.go` | `Origin()`：音频用视频时钟的原点，避免音频晚到几秒就整体错位 |

关键取舍：

- **音轨从第一次构建 init 时就存在**，不是"收到第一包再加"。后者会改变 init 的名字（内容寻址），
  逼所有已连接的播放器重新初始化。
- **音频时间轴以视频时钟的原点为基准**：`elapsed = audioTS - videoOrigin`。各自归一化会在
  音频晚到时产生几秒的 A/V 偏移。
- **每个 `traf` 的 `trun.data_offset` 指向 `mdat` 里自己那段数据**：视频在前，音频紧随其后，
  两段首尾相接，测试里断言了这一点。

## 13.3 验证

真实抓包（2880×1620，100 帧）+ 合成 Opus 包，`ffprobe`：

```
0,hevc,video
1,opus,audio          channels=1  sample_rate=48000  codec_tag=Opus
nb_read_frames=120    （60 视频帧 × 每帧 2 个音频包）
```

视频轨仍然解码零错误。分片结构：

```
moof
  traf (track 1, 60 samples, 230865 字节) → data_offset 2400
  traf (track 2, 120 samples, 4920 字节)  → data_offset 233265 = 2400 + 230865
mdat 238185 = 两轨数据总和，不多不少
```

## 13.4 iOS 的确定性仍然有限

Apple 的 HLS 规范只列 AAC，Opus 是"实现上能用、文档里没有"。iOS 17+ 能播的消息来自
[开发者论坛](https://developer.apple.com/forums/thread/759210)，不是规范。所以：

- Chrome / Edge / VLC / mpv：确定可以
- iOS 17+：大概率可以，需要真机确认
- iOS 16 及更早：静音

如果真机验证失败，唯一出路是 AAC（§4.2 的 B2），那需要 cgo 编码器，与静态单二进制冲突。

## 13.5 `/cam.wav` 的去留

对 Opus 相机它永远只有头。保留是因为 PCMA 相机（其它型号）仍然适用。

## 13.8 TS 音轨（2026-11-18）

OBS 在局域网里拉 TS 的延迟比 HLS 低，所以 TS 也需要声音。Opus 进 MPEG-TS 的形状（照 ffmpeg 的参考实现）：

```
PMT: stream_type=0x06  PID=0x0101  PCR 仍在视频 PID
     ES 描述符: 05 04 'Opus'   7f 02 80 01
PES: stream_id=0xBD，带真实长度，只有 PTS
```

实现要点：

- `ts.Muxer` 的 `writeVideo` 抽成 `writeElementary(pid, counter, pes, unit, withPCR, ...)`，
  视频和音频共用分包逻辑；音频包不带 PCR。
- `sink.AudioWriter` 接口：能带声音的 sink 实现 `WriteAudio`，不能的（或未开启音频的）直接丢弃。
- 音频时间轴同样以视频时钟的原点为基准（和 HLS 一致），否则音画会错开。
- PMT 的 section_length 随音轨存在与否变化，音频流从第一张表就声明，不会中途出现。

验证：`ffprobe` 读我们生成的 TS 得到 `hevc` + `opus` 两条流。
## 13.6 真机验证（2026-11-18）

`--audio` 打开后，线上分片的实测：

```
track 1 (视频): 60 samples, 3.020s, tfdt = 1630.621s
track 2 (音频): 76 samples, 3.002s, tfdt = 1630.639s
```

两条轨覆盖同一时间窗，起点差 18 毫秒（一个音频包的间隔），Chrome 里画面与声音同时正常。

至此路线 B1 打通：**不转码、不引入编码器，摄像头的声音跟着画面走同一条 HLS。**

## 13.7 这条路上踩过的三个坑（都是"声明与内容不一致"）

| 现象 | 真因 |
|---|---|
| 全浏览器黑屏，TS 正常 | init 声明了音轨，但 `Publish` 没把音频转发给分段器，分片里一个音频样本都没有 |
| 播两秒后一直转圈 | `Clock.Origin()` 返回了"上一个"时间戳而不是第一帧，音频 tfdt 比视频早 102 秒 |
| （更早）分片拿到但播不出 | `moof` 缺 `traf` 容器 |

共同点：**ffmpeg 对这三类问题全部宽容**，只有浏览器会拒绝。所以验收标准不能是"ffmpeg 能解"，必须是
"按播放器的方式解析：解析 URI 再取、按 traf 逐层找盒子、同一时间窗内两条轨的 tfdt 对得上"。