#!/usr/bin/env python3
"""Convert a Wireshark .pcap/.pcapng capture into Aion2Flow-style stream.log lines,
keeping only the TCP connection that looks like the AION 2 world server.

Usage: pcap_to_stream.py capture.pcapng out.stream.log
"""
import sys, socket, datetime
from collections import defaultdict
import dpkt

sys.path.insert(0, __import__("os").path.dirname(__file__))
from decode_stream import split_frames

KNOWN = {"0438", "0238", "0036", "008D", "1D37", "0338", "2B38", "0638", "0538", "2A38", "4136", "4036"}


def ip_str(b):
    try:
        return socket.inet_ntop(socket.AF_INET if len(b) == 4 else socket.AF_INET6, b)
    except Exception:
        return b.hex()


def read_packets(path):
    with open(path, "rb") as f:
        try:
            reader = dpkt.pcapng.Reader(f)
        except ValueError:
            f.seek(0)
            reader = dpkt.pcap.Reader(f)
        linktype = reader.datalink()
        for ts, buf in reader:
            try:
                if linktype == dpkt.pcap.DLT_EN10MB:
                    ip = dpkt.ethernet.Ethernet(buf).data
                elif linktype in (dpkt.pcap.DLT_RAW, 101, 228, 229):
                    ip = dpkt.ip.IP(buf) if buf[0] >> 4 == 4 else dpkt.ip6.IP6(buf)
                elif linktype == dpkt.pcap.DLT_NULL or linktype == 0:
                    ip = dpkt.loopback.Loopback(buf).data
                else:
                    ip = dpkt.ethernet.Ethernet(buf).data
            except Exception:
                continue
            if not isinstance(ip, (dpkt.ip.IP, dpkt.ip6.IP6)):
                continue
            tcp = ip.data
            if not isinstance(tcp, dpkt.tcp.TCP) or not tcp.data:
                continue
            yield ts, ip_str(ip.src), tcp.sport, ip_str(ip.dst), tcp.dport, tcp.seq, bytes(tcp.data)


def reassemble(segments):
    """segments: list of (ts, seq, data). Returns ordered list of (ts, seq, data) without overlaps."""
    segments.sort(key=lambda s: (s[1], s[0]))
    out = []
    nxt = None
    for ts, seq, data in segments:
        if nxt is not None:
            # 32-bit seq wrap-safe delta
            delta = (seq - nxt) & 0xFFFFFFFF
            if delta > 0x7FFFFFFF:  # seq < nxt: overlap/retransmission
                overlap = (nxt - seq) & 0xFFFFFFFF
                if overlap >= len(data):
                    continue
                data = data[overlap:]
                seq = nxt
        out.append((ts, seq, data))
        nxt = (seq + len(data)) & 0xFFFFFFFF
    return out


def score_stream(chunks):
    buf = b"".join(c[2] for c in sorted(chunks, key=lambda s: s[1])[:400])
    frames, _ = split_frames(buf)
    return sum(1 for op, _ in frames if op in KNOWN)


def main():
    src, dst = sys.argv[1], sys.argv[2]
    flows = defaultdict(list)
    for ts, sip, sp, dip, dp, seq, data in read_packets(src):
        flows[(sip, sp, dip, dp)].append((ts, seq, data))

    scored = sorted(((score_stream(v), k) for k, v in flows.items()), reverse=True)
    print("top flows by AION 2 frame score:")
    for s, k in scored[:5]:
        print(f"  {s:6d}  {k[0]}:{k[1]} -> {k[2]}:{k[3]}  ({len(flows[k])} segments)")
    if not scored or scored[0][0] == 0:
        print("No AION 2 stream found.")
        sys.exit(1)

    game = scored[0][1]
    server = (game[0], game[1])
    inbound = game
    outbound = (game[2], game[3], game[0], game[1])
    # also keep any other flows from the same server endpoint (reconnects / zone changes)
    keep = [k for k in flows if (k[0], k[1]) == server or (k[2], k[3]) == server]

    lines = []
    keep.sort(key=lambda k: min(t for t, _, _ in flows[k]))
    for k in keep:
        d = "inbound" if (k[0], k[1]) == server else "outbound"
        for ts, seq, data in reassemble(flows[k]):
            iso = datetime.datetime.fromtimestamp(ts, datetime.timezone.utc).isoformat()
            lines.append(f"{iso}|dir={d}|{k[0]}:{k[1]}->{k[2]}:{k[3]}|seq={seq}|len={len(data)}|data={data.hex().upper()}")
    with open(dst, "w") as f:
        for l in lines:
            f.write(l + "\n")
    print(f"wrote {len(lines)} chunks from {len(keep)} flow(s) to {dst}")


if __name__ == "__main__":
    main()
