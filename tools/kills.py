"""Kill summary from an Aion2Flow stream.log: NPC spawns (4036) + HP->0 (008D)."""
import sys
sys.path.insert(0, "tools")
from decode_stream import decode, read_varint
from collections import Counter

TAGS1 = {0x10, 0x20, 0x21, 0x22, 0x30, 0x32}

def parse_spawn(f):
    o = read_varint(f, 0)[1] + 2
    r = read_varint(f, o)
    if not r or r[0] <= 0: return None
    eid = r[0]; o += r[1]
    if o + 7 > len(f): return None
    t0, t1, t2 = f[o], f[o+1], f[o+2]
    if not ((t1 in TAGS1 and t2 == 0) or (t0 == 0x1C and t1 == 0 and t2 == 0)): return None
    code = int.from_bytes(f[o+3:o+7], "little")
    return (eid, code) if 2_000_000 <= code <= 2_999_999 else None

def parse_state(f):
    """4136: entity varint, 3 mode bytes, u32 npc code."""
    o = read_varint(f, 0)[1] + 2
    r = read_varint(f, o)
    if not r or r[0] <= 0: return None
    o += r[1]
    if o + 7 > len(f): return None
    code = int.from_bytes(f[o+3:o+7], "little")
    return (r[0], code) if 2_000_000 <= code <= 2_999_999 else None

def parse_hp(f):
    o = read_varint(f, 0)[1] + 2
    vals = []
    for _ in range(4):
        r = read_varint(f, o)
        if not r: return None
        vals.append(r[0]); o += r[1]
    if tuple(vals[1:]) != (2, 1, 0) or o + 4 > len(f): return None
    return vals[0], int.from_bytes(f[o:o+4], "little")

def run(path, verbose=False):
    species = {}; dead = set(); kills = Counter(); unknown = 0
    for ts, d, op, f in decode(path):
        if op in ("4036", "4136"):
            s = parse_spawn(f) if op == "4036" else parse_state(f)
            if s: species[s[0]] = s[1]; dead.discard(s[0])
        elif op == "008D":
            h = parse_hp(f)
            if h and h[1] == 0 and h[0] not in dead:
                dead.add(h[0])
                code = species.get(h[0])
                if code is None: unknown += 1
                else:
                    kills[code] += 1
                    if verbose: print(ts, "KILL", code, "entity", h[0])
    return species, kills, unknown

if __name__ == "__main__":
    sp, k, u = run(sys.argv[1], "-v" in sys.argv)
    print("spawned npcs:", len(sp), "| distinct species:", len(set(sp.values())))
    print("deaths with species:", sum(k.values()), "| deaths w/o spawn seen:", u)
    for code, c in k.most_common(15): print(f"  {code}: {c}")
