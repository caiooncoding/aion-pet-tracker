#!/usr/bin/env python3
"""Offline decoder for Aion2Flow stream.log dumps.

Turns the reassembled TCP hex lines into individual game frames:
  timestamp | direction | opcode | length | hex body

Framing (from Aion2Flow PacketTransportCodec / PacketFrameParser):
  frame   = varint V, total frame length = V + len(varint) - 4
  compressed frame: varint [optional 0xF0..0xFE] FF FF <u32 LE uncompressed len> <LZ4 block>
            -> the decompressed bytes are again a sequence of frames
  06 00 36 is a filler/separator pattern between frames
  opcode  = the 2 bytes right after the length varint
"""
import sys, json, argparse
from collections import defaultdict
import lz4.block

SEP = b"\x06\x00\x36"


def read_varint(b, off):
    val = shift = n = 0
    while True:
        if off + n >= len(b):
            return None
        c = b[off + n]
        n += 1
        val |= (c & 0x7F) << shift
        if not c & 0x80:
            return val, n
        shift += 7
        if shift >= 32 or n > 5:
            return None


def frame_len(b, off):
    r = read_varint(b, off)
    if not r:
        return None
    v, n = r
    total = v + n - 4
    return total if total > 0 else None


def try_decompress(frame):
    r = read_varint(frame, 0)
    if not r:
        return None
    off = r[1]
    if off < len(frame) and 0xF0 <= frame[off] < 0xFF:
        off += 1
    if frame[off:off + 2] != b"\xff\xff" or off + 6 > len(frame):
        return None
    ulen = int.from_bytes(frame[off + 2:off + 6], "little")
    if ulen <= 0 or ulen > 4 * 1024 * 1024:
        return None
    try:
        return lz4.block.decompress(frame[off + 6:], uncompressed_size=ulen)
    except Exception:
        return None


def split_frames(buf, depth=0):
    """Yield (opcode_hex, frame_bytes) from a buffer of back-to-back frames.
    Returns leftover bytes (incomplete frame) as last element via StopIteration value."""
    off = 0
    out = []
    while off < len(buf):
        if buf[off:off + 3] == SEP:
            off += 3
            continue
        fl = frame_len(buf, off)
        if fl is None or off + fl > len(buf):
            break
        frame = buf[off:off + fl]
        off += fl
        inner = try_decompress(frame)
        if inner is not None and depth < 3:
            sub, _ = split_frames(inner, depth + 1)
            out.extend(sub)
            continue
        r = read_varint(frame, 0)
        n = r[1] if r else 0
        op = frame[n:n + 2].hex().upper() if len(frame) >= n + 2 else "????"
        out.append((op, frame))
    return out, buf[off:]


def find_sync(buf, start=1, chain=4, limit=1 << 20):
    """Find the next offset where `chain` consecutive frames parse back-to-back."""
    end = min(len(buf), start + limit)
    for p in range(start, end):
        off, ok = p, 0
        while ok < chain:
            if buf[off:off + 3] == SEP:
                off += 3
                continue
            fl = frame_len(buf, off)
            if fl is None or fl > 1 << 20 or off + fl > len(buf):
                break
            off += fl
            ok += 1
        if ok >= chain:
            return p
    return None


def parse_log(path):
    """Yield (ts, direction, conn, bytes) chunks in file order."""
    with open(path, encoding="utf-8", errors="replace") as f:
        for line in f:
            parts = line.rstrip("\n").split("|")
            if len(parts) < 4:
                continue
            ts = parts[0]
            kv = {}
            for p in parts[1:]:
                if "=" in p:
                    k, v = p.split("=", 1)
                    kv[k] = v
            conn = next((p for p in parts[1:] if "->" in p), "?")
            data = kv.get("data")
            if not data:
                continue
            yield ts, kv.get("dir", "?"), conn, bytes.fromhex(data)


def decode(path, direction="inbound"):
    pending = defaultdict(bytes)
    for ts, d, conn, chunk in parse_log(path):
        if direction and d != direction:
            continue
        key = (d, conn)
        buf = pending[key] + chunk
        frames, rest = split_frames(buf)
        # stuck on garbage (capture started mid-frame / lost segment): resync
        if not frames and len(rest) > 256 * 1024:
            p = find_sync(rest)
            if p is not None:
                frames, rest = split_frames(rest[p:])
            else:
                rest = rest[-4096:]
        pending[key] = rest
        for op, frame in frames:
            yield ts, d, op, frame


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("log")
    ap.add_argument("--dir", default="inbound", help="inbound|outbound|'' for both")
    ap.add_argument("--stats", action="store_true", help="opcode histogram only")
    ap.add_argument("--op", action="append", help="only these opcodes (hex, e.g. 008D)")
    ap.add_argument("--jsonl", action="store_true")
    a = ap.parse_args()

    if a.stats:
        hist = defaultdict(int)
        for _, _, op, _ in decode(a.log, a.dir):
            hist[op] += 1
        for op, c in sorted(hist.items(), key=lambda x: -x[1]):
            print(f"{op}\t{c}")
        return

    want = {o.upper() for o in a.op} if a.op else None
    for ts, d, op, frame in decode(a.log, a.dir):
        if want and op not in want:
            continue
        if a.jsonl:
            print(json.dumps({"ts": ts, "dir": d, "op": op, "len": len(frame), "hex": frame.hex()}))
        else:
            print(f"{ts}|{d}|{op}|{len(frame)}|{frame.hex()}")


if __name__ == "__main__":
    main()
