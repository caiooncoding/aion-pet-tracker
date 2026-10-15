package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"
)

// Link types (pcap DLT_*)
const (
	dltNull     = 0
	dltEthernet = 1
	dltRaw      = 12
	dltRaw2     = 101
)

const (
	probeLimit  = 128 * 1024
	maxOOO      = 512
	flowIdleTTL = 10 * time.Minute
)

type flowKey struct {
	src, dst string
}

type flow struct {
	next    uint32
	started bool
	ooo     map[uint32][]byte
	probe   []byte
	status  int // 0 unknown, 1 game, -1 not game
	dec     StreamDecoder
	last    time.Time
}

// Tracker reassembles TCP streams and feeds game streams into the state.
type Tracker struct {
	mu    sync.Mutex
	flows map[flowKey]*flow
	st    *State
	games int
	pkts  int
}

func NewTracker(st *State) *Tracker {
	return &Tracker{flows: map[flowKey]*flow{}, st: st}
}

func (t *Tracker) Packet(link int, pkt []byte, ts time.Time) {
	var ipb []byte
	switch link {
	case dltEthernet:
		if len(pkt) < 14 {
			return
		}
		et := binary.BigEndian.Uint16(pkt[12:])
		off := 14
		for et == 0x8100 || et == 0x88a8 {
			if len(pkt) < off+4 {
				return
			}
			et = binary.BigEndian.Uint16(pkt[off+2:])
			off += 4
		}
		if et != 0x0800 && et != 0x86DD {
			return
		}
		ipb = pkt[off:]
	case dltNull:
		if len(pkt) < 4 {
			return
		}
		ipb = pkt[4:]
	case dltRaw, dltRaw2:
		ipb = pkt
	default:
		return
	}
	if len(ipb) < 20 {
		return
	}
	var src, dst net.IP
	var tcp []byte
	switch ipb[0] >> 4 {
	case 4:
		ihl := int(ipb[0]&0x0f) * 4
		if ipb[9] != 6 || ihl < 20 || len(ipb) < ihl {
			return
		}
		total := int(binary.BigEndian.Uint16(ipb[2:]))
		if total > len(ipb) || total < ihl {
			total = len(ipb)
		}
		if binary.BigEndian.Uint16(ipb[6:])&0x3fff != 0 { // fragmented: ignore
			return
		}
		src, dst = net.IP(ipb[12:16]), net.IP(ipb[16:20])
		tcp = ipb[ihl:total]
	case 6:
		if len(ipb) < 40 || ipb[6] != 6 { // no extension header support
			return
		}
		plen := int(binary.BigEndian.Uint16(ipb[4:]))
		end := 40 + plen
		if end > len(ipb) {
			end = len(ipb)
		}
		src, dst = net.IP(ipb[8:24]), net.IP(ipb[24:40])
		tcp = ipb[40:end]
	default:
		return
	}
	if len(tcp) < 20 {
		return
	}
	doff := int(tcp[12]>>4) * 4
	if doff < 20 || len(tcp) < doff {
		return
	}
	payload := tcp[doff:]
	flags := tcp[13]
	sport, dport := binary.BigEndian.Uint16(tcp[0:]), binary.BigEndian.Uint16(tcp[2:])
	seq := binary.BigEndian.Uint32(tcp[4:])
	key := flowKey{fmt.Sprintf("%s:%d", src, sport), fmt.Sprintf("%s:%d", dst, dport)}

	t.mu.Lock()
	defer t.mu.Unlock()
	t.pkts++
	f := t.flows[key]
	if flags&0x02 != 0 { // SYN: new connection
		if f != nil {
			delete(t.flows, key)
		}
		f = &flow{next: seq + 1, started: true, ooo: map[uint32][]byte{}, last: ts}
		t.flows[key] = f
		return
	}
	if len(payload) == 0 {
		if flags&0x05 != 0 && f != nil { // FIN/RST
			delete(t.flows, key)
		}
		return
	}
	if f == nil {
		f = &flow{ooo: map[uint32][]byte{}}
		t.flows[key] = f
	}
	f.last = ts
	if f.status == -1 {
		return
	}
	if !f.started {
		f.started, f.next = true, seq
	}
	data := append([]byte(nil), payload...)
	diff := int32(seq - f.next)
	switch {
	case diff == 0:
		t.deliver(key, f, data, ts)
	case diff < 0:
		over := int(-diff)
		if over < len(data) {
			t.deliver(key, f, data[over:], ts)
		}
	default:
		f.ooo[seq] = data
		if len(f.ooo) > maxOOO { // lost segment: skip the gap
			seqs := make([]uint32, 0, len(f.ooo))
			for s := range f.ooo {
				seqs = append(seqs, s)
			}
			sort.Slice(seqs, func(i, j int) bool { return int32(seqs[i]-f.next) < int32(seqs[j]-f.next) })
			f.next = seqs[0]
		}
	}
	// drain out-of-order segments that now fit
	for len(f.ooo) > 0 {
		progressed := false
		for s, d := range f.ooo {
			dd := int32(s - f.next)
			if dd > 0 {
				continue
			}
			delete(f.ooo, s)
			over := int(-dd)
			if over < len(d) {
				t.deliver(key, f, d[over:], ts)
			}
			progressed = true
		}
		if !progressed {
			break
		}
	}
	if t.pkts%5000 == 0 {
		for k, fl := range t.flows {
			if ts.Sub(fl.last) > flowIdleTTL {
				delete(t.flows, k)
			}
		}
	}
}

func (t *Tracker) deliver(key flowKey, f *flow, data []byte, ts time.Time) {
	f.next += uint32(len(data))
	switch f.status {
	case 1:
		f.dec.Push(data, func(fr Frame) { t.st.HandleFrame(fr, ts) })
	case 0:
		f.probe = append(f.probe, data...)
		if looksLikeGame(f.probe) {
			f.status = 1
			t.games++
			logf("game stream found: %s -> %s", key.src, key.dst)
			buf := f.probe
			f.probe = nil
			f.dec.Push(buf, func(fr Frame) { t.st.HandleFrame(fr, ts) })
		} else if len(f.probe) > probeLimit {
			f.status = -1
			f.probe = nil
		}
	}
}
