package main

// AION 2 world-stream framing (derived from Aion2Flow, GPL-3, and verified against captures).
//
//   frame   = varint V; total frame length = V + len(varint) - 4; opcode = next 2 bytes
//   compressed frame: varint [opt 0xF0..0xFE] FF FF <u32 LE size> <LZ4 block>  -> more frames
//   06 00 36 = filler between frames

import (
	"bytes"
	"encoding/binary"
	"errors"
)

var sepPattern = []byte{0x06, 0x00, 0x36}

const maxFrame = 4 << 20

func readVarint(b []byte, off int) (val int, n int, ok bool) {
	shift := 0
	for {
		if off+n >= len(b) {
			return 0, 0, false
		}
		c := b[off+n]
		n++
		val |= int(c&0x7f) << shift
		if c&0x80 == 0 {
			return val, n, true
		}
		shift += 7
		if shift >= 32 || n > 5 {
			return 0, 0, false
		}
	}
}

func frameLen(b []byte, off int) (int, bool) {
	v, n, ok := readVarint(b, off)
	if !ok {
		return 0, false
	}
	t := v + n - 4
	if t <= 0 || t > maxFrame {
		return 0, false
	}
	return t, true
}

// lz4Block decompresses a raw LZ4 block into a buffer of exactly size bytes.
func lz4Block(src []byte, size int) ([]byte, error) {
	dst := make([]byte, 0, size)
	i := 0
	for i < len(src) {
		tok := src[i]
		i++
		lit := int(tok >> 4)
		if lit == 15 {
			for {
				if i >= len(src) {
					return nil, errors.New("lz4: bad literal len")
				}
				c := src[i]
				i++
				lit += int(c)
				if c != 255 {
					break
				}
			}
		}
		if i+lit > len(src) || len(dst)+lit > size {
			return nil, errors.New("lz4: literal overflow")
		}
		dst = append(dst, src[i:i+lit]...)
		i += lit
		if i >= len(src) {
			break // last sequence has no match
		}
		if i+2 > len(src) {
			return nil, errors.New("lz4: truncated offset")
		}
		off := int(src[i]) | int(src[i+1])<<8
		i += 2
		if off == 0 || off > len(dst) {
			return nil, errors.New("lz4: bad offset")
		}
		ml := int(tok & 15)
		if ml == 15 {
			for {
				if i >= len(src) {
					return nil, errors.New("lz4: bad match len")
				}
				c := src[i]
				i++
				ml += int(c)
				if c != 255 {
					break
				}
			}
		}
		ml += 4
		if len(dst)+ml > size {
			return nil, errors.New("lz4: match overflow")
		}
		start := len(dst) - off
		for k := 0; k < ml; k++ {
			dst = append(dst, dst[start+k])
		}
	}
	return dst, nil
}

func tryDecompress(frame []byte) ([]byte, bool) {
	_, n, ok := readVarint(frame, 0)
	if !ok {
		return nil, false
	}
	off := n
	if off < len(frame) && frame[off] >= 0xF0 && frame[off] < 0xFF {
		off++
	}
	if off+6 > len(frame) || frame[off] != 0xFF || frame[off+1] != 0xFF {
		return nil, false
	}
	size := int(binary.LittleEndian.Uint32(frame[off+2:]))
	if size <= 0 || size > maxFrame {
		return nil, false
	}
	out, err := lz4Block(frame[off+6:], size)
	if err != nil {
		return nil, false
	}
	return out, true
}

// Frame is one decoded game message.
type Frame struct {
	Op   uint16 // big-endian view of the two opcode bytes, e.g. 0x0D90
	Body []byte // full frame including length varint and opcode
	Data []byte // bytes after the opcode
}

// splitFrames decodes back-to-back frames and returns any incomplete tail.
func splitFrames(buf []byte, depth int, emit func(Frame)) (rest []byte, count int) {
	off := 0
	for off < len(buf) {
		if bytes.HasPrefix(buf[off:], sepPattern) {
			off += 3
			continue
		}
		fl, ok := frameLen(buf, off)
		if !ok || off+fl > len(buf) {
			break
		}
		frame := buf[off : off+fl]
		off += fl
		if inner, ok := tryDecompress(frame); ok && depth < 3 {
			_, c := splitFrames(inner, depth+1, emit)
			count += c
			continue
		}
		_, n, _ := readVarint(frame, 0)
		if len(frame) < n+2 {
			continue
		}
		emit(Frame{Op: uint16(frame[n])<<8 | uint16(frame[n+1]), Body: frame, Data: frame[n+2:]})
		count++
	}
	return buf[off:], count
}

// findSync looks for an offset where `chain` frames parse back-to-back.
func findSync(buf []byte, start, chain int) int {
	for p := start; p < len(buf); p++ {
		off, ok := p, 0
		for ok < chain {
			if bytes.HasPrefix(buf[off:], sepPattern) {
				off += 3
				continue
			}
			fl, good := frameLen(buf, off)
			if !good || fl > 1<<20 || off+fl > len(buf) {
				break
			}
			off += fl
			ok++
		}
		if ok >= chain {
			return p
		}
	}
	return -1
}

// StreamDecoder turns a byte stream from the server into frames.
type StreamDecoder struct {
	pending []byte
	Frames  int
}

func (d *StreamDecoder) Push(chunk []byte, emit func(Frame)) {
	d.pending = append(d.pending, chunk...)
	rest, n := splitFrames(d.pending, 0, emit)
	d.Frames += n
	if n == 0 && len(rest) > 256*1024 {
		if p := findSync(rest, 1, 4); p >= 0 {
			rest, n = splitFrames(rest[p:], 0, emit)
			d.Frames += n
		} else {
			rest = rest[len(rest)-4096:]
		}
	}
	d.pending = append(d.pending[:0], rest...)
}

// knownOps are frequent server opcodes; used to recognise the game stream.
var knownOps = map[uint16]bool{0x0438: true, 0x0238: true, 0x0036: true, 0x008D: true, 0x1D37: true, 0x0338: true, 0x2B38: true, 0x0638: true, 0x0538: true, 0x2A38: true, 0x4136: true, 0x4036: true, 0x1A37: true, 0x1B37: true, 0x1C37: true}

// looksLikeGame scores a buffer of server bytes.
func looksLikeGame(buf []byte) bool {
	p := 0
	if fl, ok := frameLen(buf, 0); !ok || fl > len(buf) {
		p = findSync(buf, 0, 4)
		if p < 0 {
			return false
		}
	}
	hits := 0
	splitFrames(buf[p:], 0, func(f Frame) {
		if knownOps[f.Op] {
			hits++
		}
	})
	return hits >= 5
}
