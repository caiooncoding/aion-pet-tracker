package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

var logger = log.New(os.Stderr, "", log.LstdFlags)

func logf(format string, a ...any) { logger.Printf(format, a...) }

// Replay feeds an Aion2Flow-style stream.log (see tools/pcap_to_stream.py) through the parser.
func Replay(path string, st *State) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	decs := map[string]*StreamDecoder{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		parts := strings.Split(sc.Text(), "|")
		if len(parts) < 4 {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			continue
		}
		var dir, conn, data string
		for _, p := range parts[1:] {
			switch {
			case strings.HasPrefix(p, "dir="):
				dir = p[4:]
			case strings.HasPrefix(p, "data="):
				data = p[5:]
			case strings.Contains(p, "->"):
				conn = p
			}
		}
		if dir != "inbound" || data == "" {
			continue
		}
		b, err := hex.DecodeString(data)
		if err != nil {
			continue
		}
		d := decs[conn]
		if d == nil {
			d = &StreamDecoder{}
			decs[conn] = d
		}
		d.Push(b, func(fr Frame) { st.HandleFrame(fr, ts) })
	}
	return sc.Err()
}

func printSummary(st *State, at time.Time) {
	v := st.View(at)
	fmt.Printf("frames=%d haveList=%v pets=%d\n", v.Frames, v.HaveList, len(v.All))
	fmt.Println("RECENT:")
	for _, p := range v.Recent {
		fmt.Printf("  %-20s Lv%d %d/%d  +%d this session  rate=%.2f killsLeft=%d\n", p.Name, p.Level, p.Souls, p.Need, p.Session, p.Rate, p.KillsLeft)
	}
	fmt.Println("NEARBY:")
	for _, p := range v.Nearby {
		fmt.Printf("  %-20s x%d  Lv%d %d/%d max=%v\n", p.Name, p.Nearby, p.Level, p.Souls, p.Need, p.Max)
	}
	if v.Map != nil {
		fmt.Printf("MAP: %s calibrated=%v mobs=%d\n", v.Map.Label, v.Map.Calibrated, len(v.Map.Mobs))
		for i, m := range v.Map.Mobs {
			if i < 5 {
				fmt.Printf("  pet %d at (%.0f, %.0f) max=%v\n", m.PetID, m.X, m.Y, m.Max)
			}
		}
	}
	fmt.Println("TOP:")
	for i, p := range v.All {
		if i >= 8 {
			break
		}
		fmt.Printf("  %-20s Lv%d %d/%d\n", p.Name, p.Level, p.Souls, p.Need)
	}
}
