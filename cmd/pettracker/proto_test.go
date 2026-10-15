package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"
)

func varint(v int) []byte {
	var b []byte
	for v > 0x7f {
		b = append(b, byte(v&0x7f)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func le32(v int) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(v))
	return b
}

// frame builds a game frame: varint(len+4-varintLen) | opcode | body.
func frame(op uint16, body []byte) []byte {
	payload := append([]byte{byte(op >> 8), byte(op)}, body...)
	for n := 1; n <= 5; n++ {
		total := n + len(payload)
		v := varint(total + 4 - n)
		if len(v) == n {
			return append(v, payload...)
		}
	}
	panic("frame too big")
}

func TestLZ4Block(t *testing.T) {
	blk, _ := hex.DecodeString("ff05000102030405060708090a0b0c0d0e0f10111213140079ce68656c6c6f20776f726c64200c0050776f726c64")
	want := append(bytes.Repeat(func() []byte {
		b := make([]byte, 20)
		for i := range b {
			b[i] = byte(i)
		}
		return b
	}(), 8), []byte("hello world hello world hello world")...)
	got, err := lz4Block(blk, len(want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("lz4 mismatch")
	}
}

func TestFrameSplitAcrossChunks(t *testing.T) {
	a := frame(0x1234, []byte{1, 2, 3})
	b := frame(0x5678, bytes.Repeat([]byte{9}, 300))
	stream := append(append(append([]byte{}, a...), sepPattern...), b...)
	var d StreamDecoder
	var ops []uint16
	for i := 0; i < len(stream); i += 7 { // feed in small pieces
		end := i + 7
		if end > len(stream) {
			end = len(stream)
		}
		d.Push(stream[i:end], func(f Frame) { ops = append(ops, f.Op) })
	}
	if len(ops) != 2 || ops[0] != 0x1234 || ops[1] != 0x5678 {
		t.Fatalf("got ops %x", ops)
	}
}

func newTestState(t *testing.T) *State {
	t.Helper()
	dir := t.TempDir()
	return NewState(dir+"/state.json", LoadPetDataOffline(dir))
}

func petListFrame(levels map[int]int, souls map[int]int) []byte {
	body := le32(0)
	body = append(body, varint(len(levels))...)
	for id, lv := range levels {
		body = append(body, le32(id)...)
		body = append(body, le32(id)...)
		body = append(body, le32(lv)...)
	}
	body = append(body, varint(len(souls))...)
	for id, s := range souls {
		body = append(body, le32(id)...)
		body = append(body, le32(s)...)
	}
	return frame(opPetList, body)
}

func soulFrame(id, delta int) []byte {
	body := varint(1)
	body = append(body, le32(id)...)
	body = append(body, le32(delta)...)
	return frame(opPetSoul, body)
}

func feed(st *State, now time.Time, frames ...[]byte) {
	var d StreamDecoder
	for _, f := range frames {
		d.Push(f, func(fr Frame) { st.HandleFrame(fr, now) })
	}
}

func TestPetListAndSoulGain(t *testing.T) {
	st := newTestState(t)
	now := time.Now()
	feed(st, now, petListFrame(map[int]int{1038: 2, 1022: 3}, map[int]int{1038: 26, 1035: 4}))

	if p := st.Pets[1038]; p == nil || p.Level != 2 || p.Souls != 26 {
		t.Fatalf("Airon after list: %+v", p)
	}
	if p := st.Pets[1035]; p == nil || p.Level != 0 || p.Souls != 4 {
		t.Fatalf("unowned pet should be level 0 with souls: %+v", p)
	}

	feed(st, now.Add(time.Second), soulFrame(1038, 1), soulFrame(1038, 1))
	if p := st.Pets[1038]; p.Souls != 28 || p.SessionAdd != 2 {
		t.Fatalf("after 2 souls: %+v", p)
	}

	// level 0 -> 1 needs 5 souls
	feed(st, now.Add(2*time.Second), soulFrame(1035, 1))
	if p := st.Pets[1035]; p.Level != 1 || p.Souls != 0 {
		t.Fatalf("rank-up roll over: %+v", p)
	}
}

func TestViewNamesFromIDTable(t *testing.T) {
	st := newTestState(t)
	now := time.Now()
	feed(st, now, petListFrame(map[int]int{1038: 2}, map[int]int{1038: 26}))
	v := st.View(now)
	if len(v.All) != 1 || v.All[0].Name != "Airon" || v.All[0].Need != 75 {
		t.Fatalf("view: %+v", v.All)
	}
}

func TestSoulToNext(t *testing.T) {
	for lv, want := range map[int]int{0: 5, 1: 25, 2: 75, 3: 0} {
		if got := soulsToNext(lv); got != want {
			t.Errorf("level %d: got %d want %d", lv, got, want)
		}
	}
}
