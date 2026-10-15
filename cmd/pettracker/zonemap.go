package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// Zone maps: tiles from wikily.gg shipped in the "maps" folder next to the exe,
// spawn points (map pixels) embedded below. The game sends monster positions in
// world units; each zone has a linear map = s*world + offset, which is fitted by
// matching the monsters you see against their known spawn points.

//go:embed assets/spawns.json.gz
var spawnsGz []byte

type zoneInfo struct {
	Frame  float64                 // map size in pixels (square)
	Spawns map[string][][2]float64 // monster name -> spawn points
}

// Calib maps world (x,y) to map pixels.
type Calib struct {
	S, OX, OY float64
	N         int     // observations used
	Err       float64 // mean error in map px
}

var zoneLabels = map[string]string{
	"verteron":               "Verteron",
	"altgard":                "Altgard",
	"chaotic-lower-reshanta": "Chaotic Lower Reshanta",
	"ishalgen":               "Ishalgen",
	"poeta":                  "Poeta",
}

// Verteron fitted on a real capture (mean error ~4 px on a 16384 px map).
var builtinCalib = map[string]Calib{
	"verteron": {S: 0.020353, OX: 8203.6, OY: 8181.95, N: 51, Err: 4.3},
}

var (
	zonesOnce sync.Once
	zones     map[string]*zoneInfo
	nameZones map[string][]string // monster name -> zones it spawns in
)

func loadZones() {
	zonesOnce.Do(func() {
		zones = map[string]*zoneInfo{}
		nameZones = map[string][]string{}
		zr, err := gzip.NewReader(bytes.NewReader(spawnsGz))
		if err != nil {
			return
		}
		var raw struct {
			Frames map[string][2]float64              `json:"frames"`
			Maps   map[string]map[string][][2]float64 `json:"maps"`
		}
		if json.NewDecoder(zr).Decode(&raw) != nil {
			return
		}
		for z, mons := range raw.Maps {
			zi := &zoneInfo{Frame: raw.Frames[z][0], Spawns: map[string][][2]float64{}}
			for name, pts := range mons {
				key := strings.ToLower(name)
				zi.Spawns[key] = pts
				nameZones[key] = append(nameZones[key], z)
			}
			zones[z] = zi
		}
	})
}

type obs struct {
	zone string
	name string
	x, y float64
}

const obsKeep = 400

// observeSpawn records a monster position for zone detection and calibration.
// Called with s.mu held.
func (s *State) observeSpawn(code int, x, y float32) {
	loadZones()
	name := strings.ToLower(s.data.NpcName(code))
	zs := nameZones[name]
	if len(zs) == 0 {
		return
	}
	for _, z := range zs {
		s.obs = append(s.obs, obs{z, name, float64(x), float64(y)})
	}
	if len(s.obs) > obsKeep {
		s.obs = s.obs[len(s.obs)-obsKeep:]
	}
}

// currentZone = the zone most of the recently seen monsters belong to.
func (s *State) currentZone() string {
	votes := map[string]int{}
	start := len(s.obs) - 80
	if start < 0 {
		start = 0
	}
	for _, o := range s.obs[start:] {
		votes[o.zone]++
	}
	best, bv := "", 0
	for z, v := range votes {
		if v > bv {
			best, bv = z, v
		}
	}
	return best
}

func (s *State) calibFor(zone string) (Calib, bool) {
	if c, ok := s.Calibs[zone]; ok {
		return c, true
	}
	c, ok := builtinCalib[zone]
	return c, ok
}

// maybeCalibrate fits the zone transform once enough monsters were seen there.
func (s *State) maybeCalibrate(zone string) {
	zi := zones[zone]
	if zi == nil {
		return
	}
	var pts []obs
	names := map[string]bool{}
	for _, o := range s.obs {
		if o.zone == zone && len(zi.Spawns[o.name]) > 0 {
			pts = append(pts, o)
			names[o.name] = true
		}
	}
	cur, have := s.calibFor(zone)
	if len(pts) < 15 || len(names) < 2 || (have && cur.N >= len(pts)) {
		return
	}
	// refit only every +50% observations once calibrated
	if have && float64(len(pts)) < float64(cur.N)*1.5 {
		return
	}
	limit := 10 * zi.Frame / 16384 // accept fits within ~10 px on a 16k map
	var cands []Calib
	if have {
		cands = append(cands, cur)
	}
	for _, s0 := range []float64{0.020353, 0.020353 * zi.Frame / 16384} {
		cands = append(cands, Calib{S: s0, OX: zi.Frame / 2, OY: zi.Frame / 2})
	}
	best := Calib{Err: math.Inf(1)}
	for _, c := range cands {
		if f := icp(c, pts, zi); f.Err < best.Err {
			best = f
		}
	}
	if best.Err >= limit { // default guesses failed: search the offset from scratch
		for _, s0 := range []float64{0.020353, 0.020353 * zi.Frame / 16384, 0.020353 / 2, 0.020353 * 2} {
			if c, ok := houghOffset(s0, pts, zi); ok {
				if f := icp(c, pts, zi); f.Err < best.Err {
					best = f
				}
			}
		}
	}
	if best.Err < limit {
		best.N = len(pts)
		s.Calibs[zone] = best
		s.dirty = true
		logf("map: %s calibrated from %d monsters (error %.1f px)", zone, best.N, best.Err)
	}
}

// houghOffset votes for the map offset: every pairing of an observation with a
// spawn point of the same monster suggests offset = spawn - s*world; the most
// popular offset (in 16 px bins) wins.
func houghOffset(sc float64, pts []obs, zi *zoneInfo) (Calib, bool) {
	const bin = 16.0
	votes := map[[2]int]int{}
	best, bv := [2]int{}, 0
	step := 1
	if len(pts) > 150 {
		step = len(pts) / 150
	}
	for i := 0; i < len(pts); i += step {
		p := pts[i]
		seen := map[[2]int]bool{}
		for _, q := range zi.Spawns[p.name] {
			k := [2]int{int(math.Floor((q[0] - sc*p.x) / bin)), int(math.Floor((q[1] - sc*p.y) / bin))}
			if seen[k] {
				continue
			}
			seen[k] = true
			votes[k]++
			if votes[k] > bv {
				best, bv = k, votes[k]
			}
		}
	}
	if bv < 5 {
		return Calib{}, false
	}
	return Calib{S: sc, OX: (float64(best[0]) + 0.5) * bin, OY: (float64(best[1]) + 0.5) * bin}, true
}

// icp: iteratively match each observation to its nearest spawn point of the
// same monster and solve map = s*world + o by least squares.
func icp(c Calib, pts []obs, zi *zoneInfo) Calib {
	for it := 0; it < 25; it++ {
		var sx, sy, sX, sY, sxx, sxX, n, errSum float64
		for _, p := range pts {
			mx, my := c.S*p.x+c.OX, c.S*p.y+c.OY
			bd := math.Inf(1)
			var bx, by float64
			for _, q := range zi.Spawns[p.name] {
				dx, dy := q[0]-mx, q[1]-my
				if d := dx*dx + dy*dy; d < bd {
					bd, bx, by = d, q[0], q[1]
				}
			}
			errSum += math.Sqrt(bd)
			sx += p.x
			sy += p.y
			sX += bx
			sY += by
			sxx += p.x*p.x + p.y*p.y
			sxX += p.x*bx + p.y*by
			n++
		}
		c.Err = errSum / n
		// least squares for isotropic scale + offset
		den := sxx - (sx*sx+sy*sy)/n
		if den <= 0 {
			break
		}
		ns := (sxX - (sx*sX+sy*sY)/n) / den
		if ns <= 0 {
			break
		}
		c.S = ns
		c.OX = (sX - ns*sx) / n
		c.OY = (sY - ns*sy) / n
	}
	return c
}

// ---- view ----

type MapMob struct {
	X     float64 `json:"x"` // map pixels
	Y     float64 `json:"y"`
	PetID int     `json:"pet"`
	Slug  string  `json:"slug,omitempty"`
	Name  string  `json:"name"`
	Max   bool    `json:"max"`
}

type MapView struct {
	Zone       string   `json:"zone"`
	Label      string   `json:"label"`
	Frame      float64  `json:"frame"`
	Calibrated bool     `json:"calibrated"`
	Mobs       []MapMob `json:"mobs"`
}

// mapView builds the Map tab data. Called with s.mu held.
func (s *State) mapView() *MapView {
	loadZones()
	zone := s.currentZone()
	if zone == "" {
		return nil
	}
	s.maybeCalibrate(zone)
	zi := zones[zone]
	mv := &MapView{Zone: zone, Label: zoneLabels[zone], Frame: zi.Frame}
	if mv.Label == "" {
		mv.Label = zone
	}
	c, ok := s.calibFor(zone)
	if !ok {
		return mv
	}
	mv.Calibrated = true
	for _, e := range s.entities {
		if e.dead || !e.hasPos {
			continue
		}
		pid := s.petForNpc(e.code)
		slug := ""
		if pid == 0 {
			slug = s.data.SlugForNpc(e.code)
			if slug == "" {
				continue // not a pet monster
			}
			pid = s.idOfSlug(slug)
		}
		m := MapMob{X: c.S*float64(e.x) + c.OX, Y: c.S*float64(e.y) + c.OY, PetID: pid, Slug: slug}
		if m.Name = s.petName(pid); m.Name == "" {
			m.Name = s.data.PetIDName(pid)
		}
		if m.Name == "" && slug != "" {
			if info := s.data.Pet(slug); info != nil {
				m.Name = info.Name
			}
		}
		if p := s.Pets[pid]; p != nil && soulsToNext(p.Level) == 0 {
			m.Max = true
		}
		if m.X < 0 || m.Y < 0 || m.X > zi.Frame || m.Y > zi.Frame {
			continue
		}
		mv.Mobs = append(mv.Mobs, m)
	}
	return mv
}

// ---- tiles ----

var tileName = regexp.MustCompile(`^/maps/([a-z0-9-]+)/(-?\d+_-?\d+)\.webp$`)

func mapsDir() string {
	var cands []string
	if exe, err := os.Executable(); err == nil {
		cands = append(cands, filepath.Join(filepath.Dir(exe), "maps"))
	}
	cands = append(cands, filepath.Join("assets", "maps"), filepath.Join("..", "..", "assets", "maps"))
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	return ""
}

func registerMapAPI(mux *http.ServeMux) {
	dir := mapsDir()
	if dir == "" {
		logf("map: no maps folder found next to the exe")
	} else {
		logf("map tiles from %s", dir)
	}
	mux.HandleFunc("/maps/", func(w http.ResponseWriter, r *http.Request) {
		m := tileName.FindStringSubmatch(r.URL.Path)
		if m == nil || dir == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "max-age=604800")
		w.Header().Set("Content-Type", "image/webp")
		http.ServeFile(w, r, filepath.Join(dir, m[1], m[2]+".webp"))
	})
}
