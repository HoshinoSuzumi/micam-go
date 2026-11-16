# micam 协议规格 v1

> 语言无关的互操作规格。任何实现（Go / Rust / 其他）满足本文的**字节级**要求，即可与小米米家
> 摄像头互通。
>
> 来源：`https://github.com/eugeneRover/micam` @ `2cc9d9e7607014d1d05c17bc5676544fbb0d5d24`
> （Java 参考实现 + 抓包测试）。本文只描述**契约**，不描述 Java 的类结构与线程模型。
>
> 配套验收基线：`testdata/vectors.json`（由 `tools/verify_vectors.py` 独立校验）。

---

## 0. 阅读约定

每个结构都标注**字节序**。这不是洁癖：同一次握手里混用了三种约定，是移植中最常见的失败原因。

| 记号 | 含义 |
|---|---|
| `BE` | 大端（网络序） |
| `LE` | 小端 |
| `REV` | 字节反转（`08 10 11 45` → `69.17.16.8`） |
| `REQ` | 必须字节级一致，否则对端不认 |
| `BEH` | 只需行为等价（时序、语义），不要求字节相同 |

---

## 1. 角色与拓扑

```
┌──────────┐  HTTPS 443        ┌──────────────┐
│ Mi cloud │◄─────────────────►│              │
└──────────┘  (登录/签名 API)   │              │
                                │    client    │
┌──────────────┐  UDP 32100     │  (本程序)    │
│ rendezvous×N │◄───────────────►│              │
└──────────────┘  (发现/呼叫)    │              │
                                │              │
┌──────────────┐  UDP 25000     │              │
│   relay      │◄───────────────►│              │
└──────┬───────┘  (注册/会话口)  └──────┬───────┘
       │                               │
       │  UDP（中继转发，双向）          │ HTTP 8554（MPEG-TS）
       ▼                               ▼
┌──────────────┐                 ┌──────────┐
│   camera     │                 │  player  │
└──────────────┘                 └──────────┘
```

**关键约束**：客户端**不主动打洞**。所有媒体都经 relay 转发。客户端只需要**出站** UDP；
不需要任何入站端口，也不需要公网 IP。

---

## 2. 传输与端口（REQ）

| 用途 | 协议 | 端口 |
|---|---|---|
| 小米账号 / 云 API | TCP | 443 |
| rendezvous（发现、呼叫） | UDP | **32100** |
| relay 注册 / 换会话端口 | UDP | **25000** |
| relay 分配的媒体会话 | UDP | **动态高端口**（由 `RLY_PORT_ACK` 给出） |
| 本地 MPEG-TS 分发 | TCP | 8554（可配） |

**单一 UDP socket 复用**：发现、注册、媒体全部走同一个本地 UDP 端口。若为不同目的地新建
socket，rendezvous 与 relay 会看到不同的外部端点，会话建立失败（表现为静默超时）。

---

## 3. 阶段 1：小米云登录（REQ）

### 3.1 常量

| 项 | 值 |
|---|---|
| 登录基址 | `https://account.xiaomi.com/pass` |
| `sid` | `xiaomiio` |
| JSON 前缀 | `&&&START&&&`（响应体前若有则剥离） |
| User-Agent | `Android-7.1.1-1.0.0-ONEPLUS A3010-136-<deviceId> APP/xiaomi.smarthome APPV/62830` |

`deviceId`：16 位 `[A-Z0-9]` 随机串。**一旦生成必须永久固定**——小米把二次验证绑定在
deviceId 上，每次重新生成会导致无限 2FA 循环。

### 3.2 Cookie 预备

登录前在 cookie jar（域 `.xiaomi.com`、路径 `/`）中种入：

| Cookie | 值 |
|---|---|
| `sdkVersion` | `accountsdk-18.8.15` |
| `deviceId` | `<deviceId>` |
| `userId` | 已有凭据时写入 |
| `passToken` | 已有凭据时写入 |

客户端必须**跟随重定向**（`Redirect.ALWAYS`）并接受所有 cookie。

### 3.3 第一步：探测登录态

```
GET https://account.xiaomi.com/pass/serviceLogin?sid=xiaomiio&_json=true
```

- 若响应 JSON 含 `ssecurity`：`passToken` 已生效，**跳过密码与 2FA**，直接进 3.5。
- 否则进入 3.4。

### 3.4 第二步：密码登录（仅在无 passToken 时）

```
POST https://account.xiaomi.com/pass/serviceLoginAuth2
Content-Type: application/x-www-form-urlencoded

sid=xiaomiio
&hash=<UPPER(hex(MD5(password)))>
&callback=<step.callback>
&qs=<step.qs>
&user=<账号>
&_sign=<step._sign>
&_json=true
```

响应 JSON 中：

- 有 `notificationUrl` → **需要二次验证**。确认该 URL 对本实现无效（验证绑定浏览器
  deviceId）。唯一出路：浏览器登录后取 `passToken`。中断并报错。
- 有 `ssecurity` → 成功，继续 3.5。

### 3.5 第三步：换取 serviceToken

```
clientSign = base64( SHA1( "nonce=<step.nonce>&<ssecurity>" ) )
GET <step.location>&clientSign=<urlencode(clientSign)>
```

从 cookie jar 读 `serviceToken`。若为空则失败。

### 3.6 落盘字段

| 字段 | 来源 | 说明 |
|---|---|---|
| `deviceId` | 生成 | **必须稳定** |
| `ssecurity` | 3.3 / 3.4 / 3.5 | 签名密钥 |
| `userId` | 登录响应 | |
| `serviceToken` | 3.5 cookie | 数周有效 |
| `passToken` | 登录响应（若有） | 长效，可静默续期，**等同密码** |

`hasSession` = `ssecurity`/`serviceToken`/`userId` 三者非空；`canRenew` = `passToken` 非空。

**已存在 `passToken` + `userId` 时，3.3 会直接返回 `ssecurity`，全程无需密码与 2FA。**
这是容器化 / 无人值守部署的唯一推荐路径。

---

## 4. 阶段 2：签名 API（REQ，最易错）

### 4.1 主机

| region | 主机 |
|---|---|
| `cn` 或空 | `api.io.mi.com` |
| 其他 | `<region>.api.io.mi.com` |

region 取值：`cn`、`de`、`sg`、`us`、`ru`、`i2`。

### 4.2 nonce

```
nonce = base64( 8 随机字节 || BE_u32( 当前 Unix 分钟数 ) )     // 共 12 字节
```

### 4.3 signedNonce

```
signedNonce = base64( SHA256( base64decode(ssecurity) || base64decode(nonce) ) )
```

**RC4 的 key 是 `base64decode(signedNonce)` 这 32 个字节**，不是那串 base64 文本。

### 4.4 签名函数

```
signature(url, method, signedNonce, params) =
    base64( SHA1( method.toUpperCase() || "&" || apiPath(url)
                  || 对 params 每个 (k,v) 依次 "&" || k || "=" || v
                  || "&" || signedNonce ) )
```

- `apiPath(url)`：去掉 `https://<host>` 与 `/app` 前缀。例：
  `https://ru.api.io.mi.com/app/miotspec/action` → `/miotspec/action`。
- **值在签名时不做 URL 编码**（原始的 base64 可能含 `+ / =`），只在最终 POST 时才
  `urlencode`。提前编码必错。
- **参数顺序参与签名**：必须保持插入序（`data` → `rc4_hash__` → …）。

### 4.5 RC4

标准 RC4，但**丢弃前 1024 字节 keystream**。key 同上（32 字节）。加密与解密相同。

### 4.6 构造请求体（两步签名）

```
1. 明文参数:        {"data": <json>}
2. rc4_hash__ = signature(url, method, signedNonce, {"data": ...})
3. 用 RC4 加密**每一个 value**（含 data 与 rc4_hash__），输出 base64
4. signature  = signature(url, method, signedNonce, <已加密的参数，按序>)
5. 追加密文无关字段: ssecurity=<ssecurity>, _nonce=<nonce>
```

最终键序：`data, rc4_hash__, signature, ssecurity, _nonce`。

### 4.7 请求头与 Cookie

```
User-Agent:                       <见 3.1>
x-xiaomi-protocal-flag-cli:       PROTOCAL-HTTP2      (拼写是上游原样)
Content-Type:                     application/x-www-form-urlencoded
MIOT-ENCRYPT-ALGORITHM:           ENCRYPT-RC4
Accept-Encoding:                  identity
Cookie: userId=<userId>; serviceToken=<t>; yetAnotherServiceToken=<t>;
        locale=en_GB; timezone=GMT%2B03%3A00; is_daylight=0; dst_offset=0;
        channel=MI_APP_STORE
```

Body：对每个 (k,v) 做 `urlencode(k)=urlencode(v)`，以 `&` 连接。

### 4.8 响应

响应体整体用**同一个 RC4 key** 解密（base64 → 明文 JSON）。
serviceToken 过期时返回的是**明文**，解密会失败——据此判定"需要重新登录"。

---

## 5. 阶段 3：设备列表（REQ）

```
POST /app/home/device_list
data = {"getVirtualModel":false,"getHuamiDevices":0}
```

响应 `result.list[]`，每项取 `did`、`name`、`model`、`localip`、`isOnline`。

摄像头判定：`model` 包含 `.camera.`。

**离线摄像头（isOnline=false）不可能建立中继会话**——它没有注册到 rendezvous。必须先据此
拒绝，而不是等 90 秒超时。

---

## 6. 阶段 4：P2P 供给（REQ）

```
POST /app/v2/device/miss_get_vendor
data = {"app_pubkey":"<hex>", "did":"<did>", "support_vendors":"TUTK_CS2_MTP"}
```

`app_pubkey` 不可省略：缺了服务端返回 `code -6 "nil request"`。

响应 `code` 必须为 `0`，然后：

| 字段 | 含义 |
|---|---|
| `result.vendor.vendor` | 传输族：1 tutk、3 agora、**4 cs2**、6 mtp |
| `result.vendor.vendor_params.p2p_id` | 形如 `ABCDEF-123456-GHIJK` 的 UID |
| `result.vendor.vendor_params.init_string` | 混淆的 rendezvous 列表 |
| `result.vendor.vendor_params.license` / `crc_key` | 本实现不使用 |
| `result.public_key` | 摄像头 X25519 公钥（hex，小端 32 字节） |
| `result.sign` | MISS 认证用的签名（hex） |
| `result.miss_version` | 仅用于日志 |

只有 `vendor == 4` 可实现；其余必须明确拒绝。

---

## 7. 阶段 5：init string 解码（REQ）

输入形如 `<大写字母串>[:<secret>]`，只取 `:` 之前部分。每 2 个字符 → 1 个输出字节。

```
for i in 0..(len/2 - 1):
    running = 0x39
    for j in 0..i-1:  running ^= out[j]
    hi = 16 * (enc[2i]   - 'A')
    lo =      (enc[2i+1] - 'A')
    out[i] = running ^ LUT[i mod 54] ^ (hi + lo)
```

- 初值 **0x39**（不是 0）。
- `running` 必须 XOR **全部历史输出字节**，不是滚动两两。
- LUT 恰好 **54 字节**，循环使用。

LUT（hex）：

```
4959433db5bf6da347534f6165e371e9677f02030badb3892b2f35c16b8b95
9711e5a70deff1050783fb9d3bc5c713171d1f2529d3df
```

输出是逗号分隔 IP 列表，**带尾部逗号**（C 解析器要求）。按 `,` 切分后去空、去空白即为
rendezvous 服务器 IP 列表。

---

## 8. 阶段 6：CS2 中继握手（REQ 字节 / REQ 时序）

### 8.1 帧格式

```
F1 | type:u8 | len:u16 BE | payload[len]
```

`len` **只算 payload**，不含 4 字节头。接收时若声明长度大于实际可用字节，按实际可用截断。

### 8.2 消息类型

| 值 | 名 | 方向 |
|---|---|---|
| `0x67` | LIST_REQ1 | C→rendezvous |
| `0x69` | LIST_REQ_ACK | rendezvous→C |
| `0x70` | RLY_HELLO | C→relay |
| `0x71` | RLY_HELLO_ACK | relay→C |
| `0x72` | RLY_PORT | C→relay |
| `0x73` | RLY_PORT_ACK | relay→C |
| `0x80` | RLY_REQ | C→rendezvous |
| `0x81` | RLY_REQ_ACK | rendezvous→C |
| `0x82` | RLY_TO | rendezvous→C（候选 relay 地址 + token） |
| `0x83` | RLY_PKT | C→relay（注册 / 保活 / 转发数据） |
| `0x84` | RLY_RDY | relay→C（会话就绪） |
| `0x28` | MISS_SETUP | relay→C（会话链接，见第 9 节） |
| `0xD0` | DRW | 双向（可靠有序流，见第 10 节） |
| `0xD1` | DRW_ACK | 双向 |
| `0xE0` / `0xE1` | ALIVE / ALIVE_ACK | 双向保活 |
| `0xF0` | CLOSE | 双向 |

### 8.3 地址块（16 字节，REQ）

```
[0..1]  family  = 00 02        (BE)
[2..3]  port                   (LE)
[4..7]  IPv4                   (REV: out[4]=o4, out[5]=o3, out[6]=o2, out[7]=o1)
[8..15] 全 0
```

例：`203.0.113.8:29736` → `0002 2874 087100cb 0000000000000000`
（port 0x7428=29736 以 LE 写出为 `28 74`；IP `cb 00 71 08` 反转后为 `08 71 00 cb`。）

**同一会话里 `RLY_PORT_ACK` 的端口是 BE**，与这里相反。

### 8.4 UID（20 字节，REQ）

```
[0..7]   前缀 ASCII，右补 0
[8..11]  serial，BE u32
[12..19] 校验串 ASCII，右补 0
```

例：`ABCDEF-123456-GHIJK` → serial `123456` = `0x0001E240`。

### 8.5 握手流程（8 步）

设 `S` = rendezvous 列表，`U` = UID 字节。

| # | 动作 | 报文 |
|---|---|---|
| 1 | 向每个 `S:32100` 发 LIST_REQ1 | `F1 67 0014 <U>` |
| 2 | **固定窗口 2500 ms 排空**所有回复 | 收 `RLY_TO`：`addr16(16) + token(4)` |
| 3 | 向每个候选 relay 注册，**每个 token 各发 4 次** | `F1 83 001c token(4) + U + 00000000` |
| 4 | 向每个候选发 RLY_HELLO（空 payload） | `F1 70 0000` |
| 5 | 等第一个 `RLY_HELLO_ACK`，锁定 `relay.ip:25000` | 超时则整轮重来（最多 4 次，重发步骤 3） |
| 6 | 向 relay 发 RLY_PORT（空） | `F1 72 0000` |
| 7 | 收 `RLY_PORT_ACK`：`token(4) + port:u16 BE` | 得到**会话端口**与**会话 token** |
| 8 | 向 `S:32100` 发 RLY_REQ；同时向会话端口**连发 24 个** RLY_PKT 保活 | `F1 80 0028 U(20)+addr16(16)+token(4)`；`F1 83 001c sessionToken(4)+U+00000000` |
| 9 | 等 `RLY_RDY`（每次 2 s 窗口，最多 6 轮） | 成功即会话建立 |

两个"不报错但永久卡死"的陷阱：

1. **步骤 3 必须先注册**：relay 会静默丢弃未注册客户端的 HELLO。且每个 rendezvous 对同一个
   relay 发出的 token **不同**，必须全部收集并逐一回放。
2. **步骤 8 必须先向会话端口发包**：这是与步骤 5-7 不同的 UDP 目的地，对称 NAT 下尚无映射，
   relay 无法回包。不发则 `RLY_REQ_ACK` 到达而 `RLY_RDY` 永不到。

时序参数（实测可工作区间，建议原样保留）：

| 参数 | 值 |
|---|---|
| 发现窗口 | 2500 ms |
| 注册重发 | 每 token 4 次 |
| HELLO / PORT 重试 | 4 / 4 次，各 2500 / 2000 ms 窗口 |
| 会话端口连发 | 24 个 |
| 入会轮次 | 6 轮 × 2 s |
| 整体超时 | 默认 90 s，可配 |

---

## 8.6 局域网直连（可选路径）

相机与客户端在同一网段时，可以完全绕开 rendezvous 与 relay。握手只有四步，全部在
**UDP 32108**：

| # | 方向 | 报文 |
|---|---|---|
| 1 | C→**广播** | `F1 30 0000`（LAN_SEARCH），每 200 ms 重发 |
| 2 | camera→C | `F1 41 …`（PUNCH_PKT），payload = 该相机的 20 字节 UID |
| 3 | C→camera | **原样回送**步骤 2 的整个 datagram，**发往它的源端口** |
| 4 | camera→C | `F1 42 …`（P2P_RDY），同一源端口即会话端口 |

**必须用广播。** 实测 `xiaomi.camera.92ac1`：把同样的报文单播到它的地址、端口 32108，
完全无应答；换成广播后 1 ms 内回包。因此搜索目标应同时包含 `255.255.255.255:32108`、
所在 /24 的定向广播、以及（作为兜底）相机自身地址。Linux 上需要 `SO_BROADCAST`，
Windows 不需要。

**用 UID 认领应答。** PUNCH_PKT 的 payload 就是相机 UID 的 20 字节线格式，同一网段里
可能有多台相机同时应答，必须按 UID 精确匹配，不能"谁先回就连谁"。实测回包：

```
F1 41 0014 | 58 4D 53 59 43 4E 52 00 | 00 02 37 0D | 44 53 43 52 44 00 00 00
             "XMSYCNR" + NUL          serial BE     "DSCRD" + NUL
```

对应 `p2p_id = XMSYCNR-145165-DSCRD`。

**会话端口不是 32108。** 回包来自相机的高位端口（实测 15793），步骤 3 必须发往该端口，
步骤 4 之后的对端地址也以这个端口为准。

此后同一个 socket 承载 DRW/MISS，与第 10–12 节完全一致。区别只有两点：

- **没有 0x28 链接**——那是 relay 专有的；
- 对端地址取 `P2P_RDY` 的源地址，而不是 relay 分配的会话端口。

若摄像头回 `F1 43`（P2P_RDY_TCP），表示它只提供 `cs2+tcp`——实测 `xiaomi.camera.92ac1`
就是这种。此时改用 **TCP** 连接：目标地址取该 UDP 回包的源地址（端口与 UDP 搜索端口不同），
也可从 payload 偏移 24 处的紧凑地址块（`02 00 | port BE | ip`）取得，两者都应尝试。

TCP 分帧与 UDP 不同，多 8 字节头（长度不含头本身）：

```
<u16 BE 长度> 68 00 00 00 00 00 <payload>
```

会话层随之有三处差异：

- **不发 DRW ACK**：TCP 保证送达，ACK 没有意义；
- **不做序号重组**：TCP 会话的初始序号是任意的，按 UID 之外的序号等待会永久卡住，
  必须按到达顺序直接拼接；
- **客户端每约 1 秒发一次 `F1 E0 0000`** 保活，否则空闲连接会被对端断开。

其余部分（MISS 命令、媒体头、ChaCha20 解密）与 UDP 完全一致。

MISS 的密钥与签名仍然来自云端 `miss_get_vendor`：局域网直连省掉的是**媒体传输**，不是取密钥。
因此即使全程走局域网，首次连接仍需要一次 HTTPS。

---

## 9. 阶段 7：0x28 会话链接（REQ）
relay 在 `RLY_RDY` 后约每秒重发一个 **88 字节**请求，直到被应答。未应答则数据流停住且**无
任何错误**。

请求判定：`len >= 88 && p[0] == 0xA1 && p[1] == 0x01`。

应答 = 对请求做**确定性重写**（长度不变）：

| 偏移 | 内容 |
|---|---|
| `[1]` | `0x02` |
| `[4..39]` | 不变（session id、tag、UID） |
| `[40..55]` | 不变（我方外部地址，relay 填写） |
| `[56..59]` | `FE FF FF FF` |
| `[60..63]` | session id **反转**（取 `request[4..7]` 的逆序） |
| `[64..79]` | **我方本地地址**（16 字节地址块） |
| `[80..87]` | 我方外部地址的**紧凑 8 字节形式** |

紧凑 8 字节：`02 00 | port:u16 BE | ip[0..3]`（与 16 字节块不同，**全部 BE 且不反转**）。

"我方本地地址" = 用探测 socket `connect(relay)` 得到的出口网卡地址 + 主 socket 的本地端口。

---

## 10. 阶段 8：DRW 可靠通道（REQ）

封装在 `0xD0` 帧内：

```
F1 D0 <len:u16 BE> | D1 | channel:u8 | seq:u16 BE | <u32 BE 消息长度> | <消息体...>
```

- `channel`：`0` = 命令/响应，`2` = 入站媒体，`3` = 出站音频（未实现）。
- `seq` 为 16 位，按 2^16 回绕比较（差值取模），**不能用普通 `<`**。
- 一个 UDP 分片可能含 0 个、1 个或多个完整消息；也可能只是消息的一部分。
- 乱序分片需缓存，直到缺口补齐。

应答（`0xD1`）：

```
F1 D1 0006 | D1 | channel:u8 | 0001 | seq:u16 BE
```

**重复分片也必须应答**——对端重发说明它没收到上次 ack。

---

## 11. 阶段 9：MISS 会话（REQ）

### 11.1 命令帧（走 channel 0）

```
<u32 BE 总长(=4+len(body))> <u32 BE 命令> <body>
```

### 11.2 命令表

| 值 | 名 |
|---|---|
| `0x100` / `0x101` | 认证请求 / 响应 |
| `0x102` / `0x103` | 开始 / 停止视频 |
| `0x104` / `0x105` | 开始 / 停止音频（未实现） |
| `0x109` | 码流控制 |
| `0x10D` | 回放（未实现） |
| `0x110` | 设备信息 |
| `0x1001` | 加密信封 |

### 11.3 认证（明文）

```
{"public_key":"<hex>", "sign":"<hex>","uuid":"<uuid>","support_encrypt":15}
```

注意：`public_key` 与 `sign` 之间**两个空格原样保留**（抓包字节）。`uuid` 随机。
`support_encrypt` 固定 15。

成功响应体含 `"result":"success"`。UDP 可能丢包：每 2 s 无响应则重发，直到超时。

### 11.4 认证后的命令

一律包进 `0x1001`：内层 = `<u32 BE 命令> <body>`，然后整体 ChaCha20 加密（见 12.3），
再作为 `0x1001` 的 body 发出。

开始视频：`{"videoquality":<q>,"enableaudio":0}`，`q` ∈ {0 auto, 1 SD, 2 HD, 3 full}。

---

## 12. 阶段 10：媒体解密（REQ）

### 12.1 会话密钥

```
shared = X25519( client_private, camera_public )
key    = HSalsa20( shared, 16 个零字节 )        // = NaCl crypto_box_beforenm
```

**不是裸 X25519 输出**。少这一步不会有任何报错：帧照样到达、长度正常，内容全是噪声。
可用 NaCl `tests/core1.c` 与 RFC 7748 §6.1 向量自检（见 vectors.json）。

### 12.2 channel-2 消息头（32 字节，**LE**）

| 偏移 | 类型 | 含义 |
|---|---|---|
| 0 | u32 LE | 密文长度（= 明文长 + 8） |
| 4 | u32 LE | codec id：4=H.264，5=H.265，1027=PCMA，1032=Opus |
| 8 | u32 LE | 序号 |
| 12 | u32 LE | flags，bit0 = 关键帧 |
| 16 | u64 LE | 时间戳（毫秒，相机时钟，可能倒退） |
| 24 | 8 B | session id（本实现不使用） |

32 字节头**是明文**；其后是密文。

### 12.3 解密

```
nonce  = 00000000 || ciphertext[0..7]           // 12 字节
plain  = ChaCha20( key, nonce, counter=0, ciphertext[8..] )
```

输出是 Annex-B NAL 单元。**counter 从 0 开始**（不是 1，也不是 AEAD 的构造）。

---

## 13. 阶段 11：MPEG-TS 复用（BEH，但需满足下列硬性要求）

不要求与参考实现字节一致，但下列每条都必须满足，否则播放器直接丢流：

| 要求 | 值 / 说明 |
|---|---|
| 包长 | 188 字节，`0x47` 同步字节 |
| PID | PAT `0x0000`，PMT `0x1000`，视频 `0x0100` |
| stream_type | H.264 `0x1B`，H.265 `0x24` |
| 表位置 | 每个新观众、每个关键帧、以及每 20 帧必须重发 PAT/PMT |
| CRC | CRC-32/MPEG-2：多项式 `0x04C11DB7`，初值全 1，**不取反**。校验值 `"123456789"` → `0x0376E6E7` |
| PES | 视频长度写 0（不限长），仅带 PTS，`data_alignment` 置位 |
| PCR | 每个访问单元的第一个 TS 包携带 PCR |
| **PCR 必须落后 PTS 300 ms**（27000 个 90 kHz tick） | 相等会让 mpv/VLC 判定所有帧迟到并全丢，ffplay 却正常 |
| 关键帧 | 置 `random_access_indicator` |
| 时间戳 | 必须**单调**。相机时钟会重复/倒退：只信任 `0 < delta <= 1000 ms` 的差值，否则回退到名义帧间隔 50 ms |
| 首帧 | PTS = 27000，PCR = 0 |
| 33 位限制 | PTS/PCR 必须 < 2^33；相机时间戳可能很大，必须重基到首帧 |

PTS 位布局（5 字节，`prefix` 为 `0x02` 表示仅 PTS）：

```
b0 = (prefix << 4) | (((v >> 30) & 0x07) << 1) | 0x01
b1 = (v >> 22) & 0xFF
b2 = (((v >> 15) & 0x7F) << 1) | 0x01
b3 = (v >> 7) & 0xFF
b4 = ((v & 0x7F) << 1) | 0x01
```

PCR 基（33 位）写成 6 字节：前 5 字节承载基值，第 6 字节为扩展（0），中间的保留位为 1。

---

## 14. 阶段 12：HTTP 分发（BEH）

- `GET <path>` → `200`，`Content-Type: video/mp2t`，`Cache-Control: no-store`，chunked。
- 非 GET → `405`。
- 每个观众**独立**的复用器与有界队列（约 1 秒，60 帧）；慢客户端丢帧，不得阻塞相机读取。
- **每个连接从关键帧开始**：在等到关键帧前不发送任何数据。
- 监听地址应可配置（参考实现硬编码 `127.0.0.1`，这是把它放进容器后的主要障碍）。

---

## 15. 退出码（BEH）

| 码 | 含义 |
|---|---|
| 0 | 正常退出 |
| 1 | 无摄像头 / 传输不支持 |
| 2 | 登录失败 / 需要二次验证 |
| 3 | 中继或 MISS 会话失败 |
| 4 | 云端报告摄像头离线 |
| 5 | 会话建立但无媒体 |
| 6 | 本地端口被占用 |

---

## 16. 凭据文件（BEH）

`<user.home>/.micam/credentials.json`，权限 `0600`：

```json
{
  "user": "1234567890",
  "region": "cn",
  "deviceId": "ABCDEFGHIJKLMNOP",
  "userId": "1234567890",
  "ssecurity": "base64...",
  "serviceToken": "...",
  "passToken": "..."
}
```

`passToken` 近似等价于账号密码：**只放在权限受限的持久卷里**，绝不写进镜像或明文环境变量。

---

## 17. 验收基线

`testdata/vectors.json` 收录了上游测试中的全部 golden vectors，`tools/verify_vectors.py`
以纯标准库独立复算，用于确认这些向量不是"自证自洽"。

| 组 | 覆盖 |
|---|---|
| `uid` / `addrblock` / `frame` | PPPP 编解码字节级 |
| `relay_port_ack` | 端口 BE 与 token 提取 |
| `initstring` | 4 组真实 init string |
| `miss_setup` | 0x28 请求→应答的完整改写 |
| `drw` | ack 形状、分片重组、序号回绕 |
| `cloud` | signedNonce、RC4、两步签名、最终键序 |
| `crypto` | RFC 7748 X25519、NaCl core1 HSalsa20、会话密钥 |
| `ts` | CRC 校验值、PTS/PCR 位布局、时间基参数 |

任何实现必须先让这些向量全绿，再谈真机联调。
