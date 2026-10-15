"""Extract NPC and map names from Aion2Flow locale pack (en-US.bin) to JSON."""
import sys, struct, json, lz4.block
MASK = 0x46
data = open(sys.argv[1], "rb").read()
magic, ver, kind, _, ulen, chk, clen = struct.unpack_from("<IHBBiQi", data, 0)
off = struct.calcsize("<IHBBiQi")
payload = lz4.block.decompress(data[off:off+clen], uncompressed_size=ulen)
o = 0
pm, pv, n = struct.unpack_from("<IHi", payload, o); o += 10
hdrs = []
for _ in range(n):
    sid, cnt, ln, ck = struct.unpack_from("<HiiQ", payload, o); o += 18; hdrs.append((sid, cnt, ln))
out = {}
for sid, cnt, ln in hdrs:
    sec = payload[o:o+ln]; o += ln
    if sid in (102, 103):
        m = {}; p = 0
        for _ in range(cnt):
            k = struct.unpack_from("<I" if sid == 103 else "<i", sec, p)[0]; p += 4
            l = struct.unpack_from("<i", sec, p)[0]; p += 4
            m[k] = bytes(b ^ MASK for b in sec[p:p+l]).decode("utf-8"); p += l
        out["npcs" if sid == 102 else "maps"] = m
json.dump(out, open(sys.argv[2], "w"), ensure_ascii=False)
print({k: len(v) for k, v in out.items()})
