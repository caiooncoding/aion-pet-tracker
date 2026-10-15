package main

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"time"
)

// ReplayPcap feeds a .pcapng / .pcap capture file through the live TCP tracker.
func ReplayPcap(path string, t *Tracker) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) < 24 {
		return errors.New("file too small")
	}
	if binary.LittleEndian.Uint32(b) == 0x0A0D0D0A {
		return readPcapng(b, t)
	}
	return readPcap(b, t)
}

func readPcap(b []byte, t *Tracker) error {
	var bo binary.ByteOrder = binary.LittleEndian
	nano := false
	switch binary.LittleEndian.Uint32(b) {
	case 0xa1b2c3d4:
	case 0xa1b23c4d:
		nano = true
	case 0xd4c3b2a1:
		bo = binary.BigEndian
	case 0x4d3cb2a1:
		bo, nano = binary.BigEndian, true
	default:
		return errors.New("not a pcap file")
	}
	link := int(bo.Uint32(b[20:]) & 0xffff)
	o := 24
	for o+16 <= len(b) {
		sec, frac := int64(bo.Uint32(b[o:])), int64(bo.Uint32(b[o+4:]))
		incl := int(bo.Uint32(b[o+8:]))
		o += 16
		if o+incl > len(b) {
			break
		}
		ns := frac * 1000
		if nano {
			ns = frac
		}
		t.Packet(link, b[o:o+incl], time.Unix(sec, ns))
		o += incl
	}
	return nil
}

func readPcapng(b []byte, t *Tracker) error {
	type iface struct {
		link int
		res  float64 // seconds per tick
	}
	var ifs []iface
	var bo binary.ByteOrder = binary.LittleEndian
	o := 0
	for o+12 <= len(b) {
		typ := bo.Uint32(b[o:])
		if typ == 0x0A0D0D0A { // section header decides byte order
			if binary.LittleEndian.Uint32(b[o+8:]) == 0x1A2B3C4D {
				bo = binary.LittleEndian
			} else {
				bo = binary.BigEndian
			}
			ifs = nil
		}
		blen := int(bo.Uint32(b[o+4:]))
		if blen < 12 || o+blen > len(b) {
			break
		}
		body := b[o+8 : o+blen-4]
		switch typ {
		case 1: // IDB
			ic := iface{link: int(bo.Uint16(body)), res: 1e-6}
			p := 8
			for p+4 <= len(body) {
				code, l := bo.Uint16(body[p:]), int(bo.Uint16(body[p+2:]))
				if code == 0 || p+4+l > len(body) {
					break
				}
				if code == 9 && l >= 1 { // if_tsresol
					v := body[p+4]
					if v&0x80 == 0 {
						r := 1.0
						for i := 0; i < int(v); i++ {
							r /= 10
						}
						ic.res = r
					} else {
						ic.res = 1.0 / float64(uint64(1)<<(v&0x7f))
					}
				}
				p += 4 + (l+3)&^3
			}
			ifs = append(ifs, ic)
		case 6: // EPB
			if len(body) < 20 {
				break
			}
			id := int(bo.Uint32(body))
			ticks := uint64(bo.Uint32(body[4:]))<<32 | uint64(bo.Uint32(body[8:]))
			capl := int(bo.Uint32(body[12:]))
			if id >= len(ifs) || 20+capl > len(body) {
				break
			}
			secs := float64(ticks) * ifs[id].res
			ts := time.Unix(0, int64(secs*1e9))
			t.Packet(ifs[id].link, body[20:20+capl], ts)
		}
		o += blen
	}
	return nil
}

var _ = io.EOF
