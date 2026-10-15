package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"
)

var version = "0.4.1" // overridden at build time with -ldflags "-X main.version=..."

func dataDir() string {
	if d := os.Getenv("APPDATA"); d != "" {
		return filepath.Join(d, "PetTracker")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".pettracker")
}

func main() {
	replay := flag.String("replay", "", "replay a stream.log instead of live capture (testing)")
	port := flag.Int("port", 7319, "local port for the overlay page")
	noWindow := flag.Bool("no-window", false, "don't open the overlay window")
	dir := flag.String("data", dataDir(), "where state and caches are kept")
	listDev := flag.Bool("list", false, "list network adapters and exit")
	pcapFile := flag.String("pcap", "", "replay a Wireshark .pcapng capture (testing)")
	serve := flag.Bool("serve", false, "with -pcap/-replay: keep serving the page afterwards (testing)")
	flag.Parse()

	_ = os.MkdirAll(*dir, 0o755)
	if lf, err := os.OpenFile(filepath.Join(*dir, "pettracker.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
		logger.SetOutput(multiWriter{os.Stderr, lf})
	}
	logf("Pet Tracker %s, data in %s", version, *dir)

	if *listDev {
		if err := listDevices(); err != nil {
			fmt.Println("error:", err)
		}
		return
	}

	data := LoadPetData(*dir)
	st := NewState(filepath.Join(*dir, "state.json"), data)

	if *replay != "" || *pcapFile != "" {
		var err error
		if *pcapFile != "" {
			tr := NewTracker(st)
			err = ReplayPcap(*pcapFile, tr)
			logf("pcap: %d packets, %d game stream(s)", tr.pkts, tr.games)
		} else {
			err = Replay(*replay, st)
		}
		if err != nil {
			fmt.Println("replay error:", err)
			os.Exit(1)
		}
		st.mu.Lock()
		last := st.lastFrame
		st.mu.Unlock()
		printSummary(st, last)
		st.dirty = true
		st.Save()
		if !*serve {
			return
		}
		shift := time.Since(last)
		st.mu.Lock()
		for _, e := range st.entities {
			e.seen = e.seen.Add(shift)
		}
		for _, p := range st.Pets {
			if !p.LastGain.IsZero() {
				p.LastGain = p.LastGain.Add(shift)
			}
		}
		st.mu.Unlock()
		go func() {
			for range time.Tick(time.Second) {
				st.mu.Lock()
				st.lastFrame = time.Now()
				st.mu.Unlock()
			}
		}()
		_ = serveWeb(*port, st)
		return
	}

	go func() {
		for range time.Tick(3 * time.Second) {
			st.Save()
		}
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/", *port)
	go func() {
		if err := serveWeb(*port, st); err != nil {
			logf("web server: %v", err)
			fatalBox("Pet Tracker could not start on port " + fmt.Sprint(*port) + ".\nIs it already running? Close the other Pet Tracker window first.\n\n" + err.Error())
			os.Exit(1)
		}
	}()

	go func() {
		if err := StartCapture(st); err != nil {
			logf("capture: %v", err)
			fatalBox("Pet Tracker can't read network traffic:\n\n" + err.Error() + "\n\nMake sure Npcap is installed (it comes with Wireshark).")
			os.Exit(1)
		}
	}()

	go updateLoop(st)

	if !*noWindow {
		time.Sleep(300 * time.Millisecond)
		go openOverlay(url, st)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	st.dirty = true
	st.Save()
}

type multiWriter []interface{ Write([]byte) (int, error) }

func (m multiWriter) Write(p []byte) (int, error) {
	for _, w := range m {
		_, _ = w.Write(p)
	}
	return len(p), nil
}
