package main

import (
	"encoding/json"
	"net/http"
)

// Settings are the overlay preferences, saved with the rest of the state.
type Settings struct {
	Opacity   int    `json:"opacity"` // 40..100 (% window opacity)
	HideMaxed bool   `json:"hideMaxed"`
	Tab       string `json:"tab"` // nearby | recent | all
	Locked    bool   `json:"locked"`
	Compact   bool   `json:"compact"`
	// Window rectangle in screen pixels (0 = default).
	X, Y, W, H int
	FullH      int `json:"fullH,omitempty"` // height before compact mode shrank it
}

func defaultSettings() Settings {
	return Settings{Opacity: 92, Tab: "nearby", W: 340, H: 520, X: 40, Y: 140}
}

func (s *Settings) normalize() {
	d := defaultSettings()
	if s.Opacity < 40 || s.Opacity > 100 {
		s.Opacity = d.Opacity
	}
	switch s.Tab {
	case "nearby", "recent", "all":
	default:
		s.Tab = d.Tab
	}
	if s.W < 200 || s.W > 2400 || s.H < 80 || s.H > 3000 {
		s.W, s.H = d.W, d.H
	}
	if s.X < -4000 || s.X > 10000 || s.Y < -4000 || s.Y > 10000 {
		s.X, s.Y = d.X, d.Y
	}
}

func (s *State) GetSettings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Settings
}

// UpdateSettings applies a change and pushes it to the window and the page.
func (s *State) UpdateSettings(f func(*Settings)) Settings {
	s.mu.Lock()
	f(&s.Settings)
	s.Settings.normalize()
	cur := s.Settings
	s.dirty = true
	s.notify()
	s.mu.Unlock()
	applyWindowSettings(cur)
	return cur
}

// Window control endpoints used by the page (drag, resize, close, lock…).
func registerWindowAPI(mux *http.ServeMux, st *State) {
	mux.HandleFunc("/api/settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost {
			var in map[string]json.RawMessage
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
				http.Error(w, "bad json", 400)
				return
			}
			cur := st.UpdateSettings(func(s *Settings) {
				b, _ := json.Marshal(s)
				var m map[string]json.RawMessage
				_ = json.Unmarshal(b, &m)
				for k, v := range in {
					switch k {
					case "opacity", "hideMaxed", "tab", "locked", "compact":
						m[k] = v
					}
				}
				b, _ = json.Marshal(m)
				_ = json.Unmarshal(b, s)
			})
			_ = json.NewEncoder(w).Encode(cur)
			return
		}
		_ = json.NewEncoder(w).Encode(st.GetSettings())
	})
	mux.HandleFunc("/api/window", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		switch r.URL.Query().Get("do") {
		case "move":
			go windowDrag(st, false)
		case "resize":
			go windowDrag(st, true)
		case "fit": // compact mode: shrink to the content (h = device pixels), or restore
			h := atoi(r.URL.Query().Get("h"))
			cur := st.GetSettings()
			if h > 0 {
				if cur.FullH == 0 || cur.H > h+40 {
					st.UpdateSettings(func(s *Settings) { s.FullH = s.H })
				}
				windowResize(h)
			} else if cur.FullH > 0 {
				windowResize(cur.FullH)
				st.UpdateSettings(func(s *Settings) { s.H, s.FullH = s.FullH, 0 })
			}
		case "minimize":
			windowMinimize()
		case "close":
			st.dirty = true
			st.Save()
			windowClose()
		default:
			http.Error(w, "unknown action", 400)
			return
		}
		w.WriteHeader(204)
	})
}
