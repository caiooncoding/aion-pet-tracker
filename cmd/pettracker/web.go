package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

//go:embed assets/overlay.html
var overlayHTML []byte

//go:embed assets/icon.png
var iconPNG []byte

func serveWeb(port int, st *State) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(overlayHTML)
	})
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		_, _ = w.Write(iconPNG)
	})
	mux.HandleFunc("/api/view", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st.View(time.Now()))
	})
	mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no streaming", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		ch := st.Subscribe()
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		send := func() bool {
			b, _ := json.Marshal(st.View(time.Now()))
			if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
				return false
			}
			fl.Flush()
			return true
		}
		if !send() {
			return
		}
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ch:
			case <-tick.C:
			}
			if !send() {
				return
			}
		}
	})
	mux.HandleFunc("/api/pets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st.data.List())
	})
	registerWindowAPI(mux, st)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	logf("overlay page on http://127.0.0.1:%d/", port)
	return http.Serve(ln, mux)
}
