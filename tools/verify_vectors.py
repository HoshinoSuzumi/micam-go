#!/usr/bin/env python3
"""Independent verifier for testdata/vectors.json.

Re-implements every data-layer codec and the cloud signing scheme from
docs/PROTOCOL.md using nothing but the Python standard library, then asserts the
golden vectors. The point is that this is a *second* implementation: if the
vectors were mis-transcribed, the spec misunderstood, or the two disagreed,
this script fails.

X25519 and HSalsa20 are implemented here as well, so the crypto vectors are
checked rather than trusted.

Usage:  python tools/verify_vectors.py [-v]
Exit:   0 all cases pass, 1 otherwise.
"""

from __future__ import annotations

import base64
import hashlib
import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
VECTORS = json.loads((ROOT / "testdata" / "vectors.json").read_text(encoding="utf-8"))

VERBOSE = "-v" in sys.argv or "--verbose" in sys.argv
_failures: list[str] = []
_checks = 0


def check(cond: bool, label: str, detail: str = "") -> None:
    global _checks
    _checks += 1
    if cond:
        if VERBOSE:
            print(f"  ok    {label}")
        return
    _failures.append(label if not detail else f"{label}: {detail}")
    print(f"  FAIL  {label}  {detail}")


def eq(got, want, label: str) -> None:
    check(got == want, label, f"got {got!r} want {want!r}")


def hx(b: bytes) -> str:
    return b.hex()


def unhex(s: str) -> bytes:
    return bytes.fromhex(s)


# --------------------------------------------------------------------------- #
# PPPP wire codecs
# --------------------------------------------------------------------------- #

LUT = unhex(
    "4959433db5bf6da347534f6165e371e9677f02030badb3892b2f35c16b8b95"
    "9711e5a70deff1050783fb9d3bc5c713171d1f2529d3df"
)
assert len(LUT) == 54, f"LUT must be 54 bytes, got {len(LUT)}"


def uid_encode(text: str) -> bytes:
    prefix, serial, check = text.split("-")
    out = bytearray(20)
    p = prefix.encode()
    out[0 : min(len(p), 8)] = p[:8]
    out[8:12] = int(serial).to_bytes(4, "big")
    c = check.encode()
    out[12 : 12 + min(len(c), 8)] = c[:8]
    return bytes(out)


def uid_decode(b: bytes, off: int = 0) -> str:
    def trimmed(i: int) -> str:
        end = i
        while end < i + 8 and b[end] != 0:
            end += 1
        return b[i:end].decode()

    return f"{trimmed(off)}-{int.from_bytes(b[off + 8 : off + 12], 'big')}-{trimmed(off + 12)}"


def addr_encode(ip: str, port: int) -> bytes:
    o = [int(x) for x in ip.split(".")]
    assert len(o) == 4
    out = bytearray(16)
    out[0], out[1] = 0, 2
    out[2] = port & 0xFF
    out[3] = (port >> 8) & 0xFF
    for i in range(4):
        out[4 + i] = o[3 - i]
    return bytes(out)


def addr_decode(b: bytes, off: int = 0) -> tuple[str, int]:
    port = b[off + 2] | (b[off + 3] << 8)
    ip = f"{b[off + 7]}.{b[off + 6]}.{b[off + 5]}.{b[off + 4]}"
    return ip, port


def frame_encode(mtype: int, payload: bytes) -> bytes:
    return bytes([0xF1, mtype]) + len(payload).to_bytes(2, "big") + payload


def frame_parse(buf: bytes):
    if len(buf) < 4 or buf[0] != 0xF1:
        return None
    declared = int.from_bytes(buf[2:4], "big")
    avail = min(declared, len(buf) - 4)
    return buf[1], buf[4 : 4 + avail]


def initstring_decode(enc: str) -> str:
    enc = enc.split(":")[0]
    out = bytearray(len(enc) // 2)
    for i in range(len(out)):
        running = 0x39
        for j in range(i):
            running ^= out[j]
        hi = 16 * (ord(enc[2 * i]) - ord("A"))
        lo = ord(enc[2 * i + 1]) - ord("A")
        out[i] = (running ^ LUT[i % 54] ^ ((hi + lo) & 0xFF)) & 0xFF
    return out.decode()


def miss_setup_reply(req: bytes, local_ip: str, local_port: int) -> bytes:
    out = bytearray(req)
    out[1] = 0x02
    out[56:60] = b"\xfe\xff\xff\xff"
    out[60:64] = bytes(req[4:8][::-1])
    out[64:80] = addr_encode(local_ip, local_port)
    ext_ip, ext_port = addr_decode(req, 40)
    out[80:88] = compact_addr(ext_ip, ext_port)
    return bytes(out)


def compact_addr(ip: str, port: int) -> bytes:
    return b"\x02\x00" + port.to_bytes(2, "big") + bytes(int(x) for x in ip.split("."))


def drw_ack(channel: int, seq: int) -> bytes:
    return frame_encode(
        0xD1, bytes([0xD1, channel]) + (1).to_bytes(2, "big") + (seq & 0xFFFF).to_bytes(2, "big")
    )


class DataChannel:
    """Mirror of the reference reassembly rules: 16-bit wrap, holdback, multi-message."""

    SEQ_MASK = 0xFFFF
    SEQ_WINDOW = 0x8000

    def __init__(self) -> None:
        self.pending: dict[int, bytes] = {}
        self.buf = bytearray()
        self.messages: list[bytes] = []
        self.expected = 0

    @classmethod
    def _before(cls, a: int, b: int) -> bool:
        return ((b - a) & cls.SEQ_MASK) < cls.SEQ_WINDOW and a != b

    def push(self, seq: int, data: bytes) -> bool:
        seq &= self.SEQ_MASK
        if self._before(seq, self.expected):
            return False
        if seq != self.expected:
            self.pending.setdefault(seq, data)
            return False
        self.buf += data
        self.expected = (self.expected + 1) & self.SEQ_MASK
        while self.expected in self.pending:
            self.buf += self.pending.pop(self.expected)
            self.expected = (self.expected + 1) & self.SEQ_MASK
        self._extract()
        return True

    def _extract(self) -> None:
        while len(self.buf) >= 4:
            n = int.from_bytes(self.buf[:4], "big")
            if len(self.buf) < 4 + n:
                return
            self.messages.append(bytes(self.buf[4 : 4 + n]))
            del self.buf[: 4 + n]

    def poll(self):
        return self.messages.pop(0) if self.messages else None


# --------------------------------------------------------------------------- #
# Mi cloud signing
# --------------------------------------------------------------------------- #


def rc4(key: bytes, data: bytes, drop: int = 1024) -> bytes:
    s = list(range(256))
    j = 0
    for i in range(256):
        j = (j + s[i] + key[i % len(key)]) & 0xFF
        s[i], s[j] = s[j], s[i]
    i = j = 0
    for _ in range(drop):
        i = (i + 1) & 0xFF
        j = (j + s[i]) & 0xFF
        s[i], s[j] = s[j], s[i]
    out = bytearray()
    for b in data:
        i = (i + 1) & 0xFF
        j = (j + s[i]) & 0xFF
        s[i], s[j] = s[j], s[i]
        out.append(b ^ s[(s[i] + s[j]) & 0xFF])
    return bytes(out)


def signed_nonce(ssecurity: str, nonce: str) -> str:
    h = hashlib.sha256(base64.b64decode(ssecurity) + base64.b64decode(nonce)).digest()
    return base64.b64encode(h).decode()


def api_path(url: str) -> str:
    host = url.find("com")
    path = url[host + 3 :] if host >= 0 else url
    return path.replace("/app/", "/")


def signature(url: str, method: str, sn: str, params: list[tuple[str, str]]) -> str:
    s = method.upper() + "&" + api_path(url)
    for k, v in params:
        s += "&" + k + "=" + v
    s += "&" + sn
    return base64.b64encode(hashlib.sha1(s.encode()).digest()).decode()


def encrypt_params(url, method, sn, nonce, ssecurity, plain):
    out = dict(plain)
    out["rc4_hash__"] = signature(url, method, sn, list(out.items()))
    key = base64.b64decode(sn)
    for k in list(out):
        out[k] = base64.b64encode(rc4(key, out[k].encode())).decode()
    out["signature"] = signature(url, method, sn, list(out.items()))
    out["ssecurity"] = ssecurity
    out["_nonce"] = nonce
    return out


# --------------------------------------------------------------------------- #
# Crypto: X25519 + HSalsa20
# --------------------------------------------------------------------------- #

P = 2**255 - 19
A24 = 121665


def x25519(priv: bytes, pub: bytes) -> bytes:
    k = bytearray(priv)
    k[0] &= 248
    k[31] &= 127
    k[31] |= 64
    scalar = int.from_bytes(k, "little")
    u = int.from_bytes(pub, "little") & ((1 << 255) - 1)

    x1, x2, z2, x3, z3, swap = u, 1, 0, u, 1, 0
    for t in range(254, -1, -1):
        kt = (scalar >> t) & 1
        swap ^= kt
        if swap:
            x2, x3 = x3, x2
            z2, z3 = z3, z2
        swap = kt
        a = (x2 + z2) % P
        aa = a * a % P
        b = (x2 - z2) % P
        bb = b * b % P
        e = (aa - bb) % P
        c = (x3 + z3) % P
        d = (x3 - z3) % P
        da = d * a % P
        cb = c * b % P
        x3 = pow(da + cb, 2, P)
        z3 = x1 * pow(da - cb, 2, P) % P
        x2 = aa * bb % P
        z2 = e * (aa + A24 * e) % P
    if swap:
        x2, x3 = x3, x2
        z2, z3 = z3, z2
    return (x2 * pow(z2, P - 2, P) % P).to_bytes(32, "little")


def rotl(x: int, n: int) -> int:
    return ((x << n) | (x >> (32 - n))) & 0xFFFFFFFF


def hsalsa20_core(key: bytes, nonce16: bytes) -> bytes:
    c = b"expand 32-byte k"
    sigma = [int.from_bytes(c[i * 4 : i * 4 + 4], "little") for i in range(4)]
    k = [int.from_bytes(key[i * 4 : i * 4 + 4], "little") for i in range(8)]
    n = [int.from_bytes(nonce16[i * 4 : i * 4 + 4], "little") for i in range(4)]
    x = [
        sigma[0], k[0], k[1], k[2], k[3], sigma[1], n[0], n[1],
        n[2], n[3], sigma[2], k[4], k[5], k[6], k[7], sigma[3],
    ]
    for _ in range(10):
        x[4] ^= rotl((x[0] + x[12]) & 0xFFFFFFFF, 7)
        x[8] ^= rotl((x[4] + x[0]) & 0xFFFFFFFF, 9)
        x[12] ^= rotl((x[8] + x[4]) & 0xFFFFFFFF, 13)
        x[0] ^= rotl((x[12] + x[8]) & 0xFFFFFFFF, 18)
        x[9] ^= rotl((x[5] + x[1]) & 0xFFFFFFFF, 7)
        x[13] ^= rotl((x[9] + x[5]) & 0xFFFFFFFF, 9)
        x[1] ^= rotl((x[13] + x[9]) & 0xFFFFFFFF, 13)
        x[5] ^= rotl((x[1] + x[13]) & 0xFFFFFFFF, 18)
        x[14] ^= rotl((x[10] + x[6]) & 0xFFFFFFFF, 7)
        x[2] ^= rotl((x[14] + x[10]) & 0xFFFFFFFF, 9)
        x[6] ^= rotl((x[2] + x[14]) & 0xFFFFFFFF, 13)
        x[10] ^= rotl((x[6] + x[2]) & 0xFFFFFFFF, 18)
        x[3] ^= rotl((x[15] + x[11]) & 0xFFFFFFFF, 7)
        x[7] ^= rotl((x[3] + x[15]) & 0xFFFFFFFF, 9)
        x[11] ^= rotl((x[7] + x[3]) & 0xFFFFFFFF, 13)
        x[15] ^= rotl((x[11] + x[7]) & 0xFFFFFFFF, 18)
        x[1] ^= rotl((x[0] + x[3]) & 0xFFFFFFFF, 7)
        x[2] ^= rotl((x[1] + x[0]) & 0xFFFFFFFF, 9)
        x[3] ^= rotl((x[2] + x[1]) & 0xFFFFFFFF, 13)
        x[0] ^= rotl((x[3] + x[2]) & 0xFFFFFFFF, 18)
        x[6] ^= rotl((x[5] + x[4]) & 0xFFFFFFFF, 7)
        x[7] ^= rotl((x[6] + x[5]) & 0xFFFFFFFF, 9)
        x[4] ^= rotl((x[7] + x[6]) & 0xFFFFFFFF, 13)
        x[5] ^= rotl((x[4] + x[7]) & 0xFFFFFFFF, 18)
        x[11] ^= rotl((x[10] + x[9]) & 0xFFFFFFFF, 7)
        x[8] ^= rotl((x[11] + x[10]) & 0xFFFFFFFF, 9)
        x[9] ^= rotl((x[8] + x[11]) & 0xFFFFFFFF, 13)
        x[10] ^= rotl((x[9] + x[8]) & 0xFFFFFFFF, 18)
        x[12] ^= rotl((x[15] + x[14]) & 0xFFFFFFFF, 7)
        x[13] ^= rotl((x[12] + x[15]) & 0xFFFFFFFF, 9)
        x[14] ^= rotl((x[13] + x[12]) & 0xFFFFFFFF, 13)
        x[15] ^= rotl((x[14] + x[13]) & 0xFFFFFFFF, 18)
    out = [x[0], x[5], x[10], x[15], x[6], x[7], x[8], x[9]]
    return b"".join(w.to_bytes(4, "little") for w in out)


# --------------------------------------------------------------------------- #
# MPEG-TS helpers
# --------------------------------------------------------------------------- #


def crc32_mpeg(data: bytes) -> int:
    crc = 0xFFFFFFFF
    for b in data:
        crc ^= b << 24
        for _ in range(8):
            crc = ((crc << 1) ^ 0x04C11DB7) & 0xFFFFFFFF if crc & 0x80000000 else (crc << 1) & 0xFFFFFFFF
    return crc


def pts_encode(value: int, prefix: int = 0x02) -> bytes:
    return bytes(
        [
            (prefix << 4) | (((value >> 30) & 0x07) << 1) | 0x01,
            (value >> 22) & 0xFF,
            (((value >> 15) & 0x7F) << 1) | 0x01,
            (value >> 7) & 0xFF,
            ((value & 0x7F) << 1) | 0x01,
        ]
    )


def pts_decode(b: bytes) -> int:
    return (
        ((b[0] & 0x0E) << 29)
        | (b[1] << 22)
        | ((b[2] & 0xFE) << 14)
        | (b[3] << 7)
        | ((b[4] & 0xFE) >> 1)
    )


def pcr_encode(value: int) -> bytes:
    return bytes(
        [
            value >> 25,
            (value >> 17) & 0xFF,
            (value >> 9) & 0xFF,
            (value >> 1) & 0xFF,
            (((value & 1) << 7) | 0x7E) & 0xFF,
            0x00,
        ]
    )


def pcr_decode(b: bytes) -> int:
    return (
        (b[0] << 25) | (b[1] << 17) | (b[2] << 9) | (b[3] << 1) | ((b[4] & 0x80) >> 7)
    )


def advance_clock(timestamps: list[int], nominal_ms: int = 50, max_gap_ms: int = 1000) -> list[int]:
    elapsed = 0
    previous = 0
    started = False
    out = []
    for ts in timestamps:
        if not started:
            started = True
            previous = ts
            out.append(elapsed)
            continue
        delta = ts - previous
        previous = ts
        elapsed += delta if 0 < delta <= max_gap_ms else nominal_ms
        out.append(elapsed)
    return out


# --------------------------------------------------------------------------- #
# Cases
# --------------------------------------------------------------------------- #


def main() -> int:
    print(f"vectors: {ROOT / 'testdata' / 'vectors.json'}")

    print("uid")
    for c in VECTORS["uid"]:
        got = uid_encode(c["text"])
        eq(hx(got), c["hex"], f"{c['id']} encode")
        eq(len(got), c["len"], f"{c['id']} length")
        eq(uid_decode(unhex(c["hex"])), c["text"], f"{c['id']} decode")

    print("addrblock")
    for c in VECTORS["addrblock"]:
        got = addr_encode(c["ip"], c["port"])
        eq(hx(got), c["hex"], f"{c['id']} encode")
        eq(len(got), 16, f"{c['id']} length")
        eq(addr_decode(unhex(c["hex"])), (c["ip"], c["port"]), f"{c['id']} decode")

    print("frame")
    for c in VECTORS["frame"]["encode"]:
        payload = unhex(c["payload_hex"])
        eq(hx(frame_encode(c["type"], payload)), c["hex"], f"{c['id']} encode")
        parsed = frame_parse(unhex(c["hex"]))
        check(parsed is not None, c["id"] + " parse")
        if parsed:
            eq(parsed[0], c["type"], c["id"] + " parse type")
            eq(hx(parsed[1]), c["payload_hex"], c["id"] + " parse payload")
    for c in VECTORS["frame"]["decode"]:
        parsed = frame_parse(unhex(c["hex"]))
        check(parsed is not None, c["id"] + " parse")
        if parsed:
            eq(parsed[0], c["type"], c["id"] + " type")
            eq(hx(parsed[1]), c["payload_hex"], c["id"] + " payload")
    for c in VECTORS["frame"]["reject"]:
        check(frame_parse(unhex(c["hex"])) is None, c["id"] + " rejected", c["reason"])

    print("relay_port_ack")
    for c in VECTORS["relay_port_ack"]:
        p = unhex(c["payload_hex"])
        eq(hx(p[0:4]), c["token_hex"], c["id"] + " token")
        eq(int.from_bytes(p[4:6], "big"), c["port"], c["id"] + " port is big-endian")

    print("initstring")
    for c in VECTORS["initstring"]:
        raw = initstring_decode(c["enc"] + c.get("suffix", ""))
        eq(raw, c["raw"], c["id"] + " raw")
        eq([s for s in raw.split(",") if s], c["servers"], c["id"] + " servers")

    print("miss_setup")
    ms = VECTORS["miss_setup"]
    req = unhex(ms["request_hex"])
    eq(len(req), ms["len"], "miss_setup request length")
    check(len(req) >= 88 and req[0] == 0xA1 and req[1] == 0x01, "miss_setup is a request")
    reply = miss_setup_reply(req, ms["local"]["ip"], ms["local"]["port"])
    eq(len(reply), ms["len"], "miss_setup reply length")
    eq(hx(reply), ms["reply_hex"], "miss_setup reply bytes")
    check(not (reply[0] == 0xA1 and reply[1] == 0x01), "miss_setup reply is not a request")
    eq(addr_decode(req, 40), (ms["external"]["ip"], ms["external"]["port"]), "miss_setup external address")
    for c in ms["compact"]:
        eq(hx(compact_addr(c["ip"], c["port"])), c["hex"], c["id"])

    print("drw")
    for c in VECTORS["drw"]["acks"]:
        eq(hx(drw_ack(c["channel"], c["seq"])), c["hex"], c["id"])
    ar = VECTORS["drw"]["auth_response"]
    body = ar["json"].encode()
    eq(len(body), ar["json_len"], "auth_response json length")
    message = (4 + len(body)).to_bytes(4, "big") + ar["cmd"].to_bytes(4, "big") + body
    eq(len(message) - 4, ar["declared_len"], "auth_response declared length")
    eq(len(message) - 4, ar["polled_len"], "auth_response polled length")

    ch = DataChannel()
    check(ch.push(0, message), "auth_response single fragment accepted")
    polled = ch.poll()
    eq(len(polled), ar["polled_len"], "auth_response polled bytes")
    eq(int.from_bytes(polled[:4], "big"), ar["cmd"], "auth_response command")
    eq(polled[4:].decode(), ar["json"], "auth_response body")
    check(ch.poll() is None, "auth_response yields exactly one message")

    ch = DataChannel()
    ch.push(0, message[0:10])
    check(ch.poll() is None, "incomplete message is withheld")
    ch.push(1, message[10:])
    eq(len(ch.poll()), ar["polled_len"], "split message reassembled")

    ch = DataChannel()
    check(not ch.push(1, message[20:]), "out-of-order fragment rejected")
    check(len(ch.pending) == 1, "out-of-order fragment held back")
    check(ch.push(0, message[0:20]), "gap filler accepted")
    eq(len(ch.poll()), ar["polled_len"], "held-back fragment applied")
    check(len(ch.pending) == 0, "holdback buffer drained")

    ch = DataChannel()
    check(ch.push(0, message), "duplicate baseline accepted")
    check(not ch.push(0, message), "duplicate dropped")
    eq(len(ch.poll()), ar["polled_len"], "duplicate did not corrupt the stream")
    check(ch.poll() is None, "duplicate produced no second message")

    ch = DataChannel()
    ch.push(0, message + message)
    eq(len(ch.poll()), ar["polled_len"], "first of two concatenated messages")
    eq(len(ch.poll()), ar["polled_len"], "second of two concatenated messages")

    ch = DataChannel()
    for seq in range(0x10000):
        ch.push(seq, b"")
    check(ch.push(0, message), "sequence wraps back to zero")
    eq(len(ch.poll()), ar["polled_len"], "message after wraparound")

    print("cloud")
    c = VECTORS["cloud"]
    eq(signed_nonce(c["ssecurity"], c["nonce"]), c["signed_nonce"], "signedNonce")
    eq(api_path(c["url"]), c["api_path"], "apiPath")
    for case in c["api_paths"]:
        eq(api_path(case["url"]), case["path"], "apiPath " + case["url"])
    key = base64.b64decode(c["rc4"]["key"])
    eq(len(key), 32, "rc4 key is 32 raw bytes")
    cipher = base64.b64encode(rc4(key, c["rc4"]["plaintext"].encode())).decode()
    eq(cipher, c["rc4"]["cipher_b64"], "rc4 encrypt")
    eq(rc4(key, base64.b64decode(c["rc4"]["cipher_b64"])).decode(), c["rc4"]["plaintext"], "rc4 decrypt")

    sig = signature(c["url"], c["signature_plaintext"]["method"], c["signed_nonce"], [("data", c["data"])])
    eq(sig, c["signature_plaintext"]["expected"], "signature over plaintext params")

    out = encrypt_params(c["url"], "POST", c["signed_nonce"], c["nonce"], c["ssecurity"], {"data": c["data"]})
    eb = c["encrypted_body"]
    eq(list(out.keys()), eb["order"], "encrypted body key order")
    for k in ("data", "rc4_hash__", "signature", "ssecurity", "_nonce"):
        eq(out[k], eb[k], f"encrypted body {k}")
    eq(len(base64.b64decode(c["nonce"])), c["nonce_len"], "nonce is 12 bytes")

    print("crypto")
    cr = VECTORS["crypto"]
    x = cr["x25519"]
    eq(hx(x25519(unhex(x["alice_priv"]), unhex(x["bob_pub"]))), x["shared"], "X25519 alice->bob (RFC 7748)")
    eq(hx(x25519(unhex(x["bob_priv"]), unhex(x["alice_pub"]))), x["shared"], "X25519 bob->alice (RFC 7748)")
    base = (9).to_bytes(32, "little")
    eq(hx(x25519(unhex(x["alice_priv"]), base)), x["alice_pub"], "alice public key derivation")
    eq(hx(x25519(unhex(x["bob_priv"]), base)), x["bob_pub"], "bob public key derivation")
    hs = cr["hsalsa20"]
    eq(hx(hsalsa20_core(unhex(hs["input"]), unhex(hs["nonce16"]))), hs["core"], "HSalsa20 core (NaCl core1)")
    sk = cr["session_key"]
    derived = hsalsa20_core(x25519(unhex(sk["priv"]), unhex(sk["pub"])), b"\x00" * 16)
    eq(hx(derived), sk["expected"], "session key = crypto_box_beforenm")
    check(hx(derived) != x["shared"], "session key differs from the raw agreement")

    print("ts")
    t = VECTORS["ts"]
    eq(f"{crc32_mpeg(t['crc32_mpeg2']['input_ascii'].encode()):08x}", t["crc32_mpeg2"]["expected_hex"], "CRC-32/MPEG-2 check value")
    for case in t["pts_cases"]:
        enc = pts_encode(case["value"])
        eq(hx(enc), case["hex"], case["id"])
        eq(pts_decode(enc), case["value"], case["id"] + " round trip")
    for case in t["pcr_cases"]:
        enc = pcr_encode(case["value"])
        eq(hx(enc), case["hex"], case["id"])
        eq(pcr_decode(enc), case["value"], case["id"] + " round trip")
    eq(pcr_decode(pcr_encode(t["pts_offset_ticks"])), t["pts_offset_ticks"], "PCR round trip at the PTS offset")
    eq(t["pids"]["pmt"], 0x1000, "PMT pid")
    eq(t["pids"]["video"], 0x0100, "video pid")
    eq(t["stream_types"]["h264"], 0x1B, "H.264 stream type")
    eq(t["stream_types"]["h265"], 0x24, "H.265 stream type")
    eq(t["pmt_stream_type_offset"], 17, "PMT stream_type offset within the packet")

    mc = t["monotonic_case"]
    got = advance_clock(mc["timestamps_ms"], t["nominal_frame_ms"], t["max_plausible_gap_ms"])
    eq(got, mc["expected_elapsed_ms"], "monotonic clock repair")
    for i in range(1, len(got)):
        check(got[i] > got[i - 1], f"elapsed strictly increases at {i}")
    eq(t["gap_case"]["delta_ms"] * t["clock_hz"] // 1000, t["gap_case"]["ticks"], "ms to 90kHz ticks")

    print()
    if _failures:
        print(f"FAILED {len(_failures)} / {_checks} checks")
        for f in _failures:
            print("  -", f)
        return 1
    print(f"PASS {_checks} checks")
    return 0


if __name__ == "__main__":
    sys.exit(main())
