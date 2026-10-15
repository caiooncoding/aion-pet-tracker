package main

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Opcodes (server -> client), confirmed on Caio's capture 2026-10-14.
const (
	opPetList   = 0x0090 // full pet collection at login: levels + souls
	opPetSoul   = 0x0D90 // soul gained: pet id + delta
	opNpcSpawn  = 0x4036
	opNpcState  = 0x4136
	opNpcHP     = 0x008D
	opMyKill    = 0xA6FF // arrives right after a monster you killed dies (exp gain)
	opMove1     = 0x1A37 // entity movement updates
	opMove2     = 0x1B37
	opMove3     = 0x1C37
	entityTTL   = 120 * time.Second
	killWindow  = 4 * time.Second
	recentLimit = 12
)

// soulsToNext returns souls needed to leave a level (0 = maxed).
func soulsToNext(level int) int {
	switch level {
	case 0:
		return 5
	case 1:
		return 25
	case 2:
		return 75
	}
	return 0
}

type Pet struct {
	ID         int       `json:"id"`
	Level      int       `json:"level"`
	Souls      int       `json:"souls"`
	LastGain   time.Time `json:"lastGain,omitempty"`
	Kills      int       `json:"kills"`      // kills of this pet's monsters while tracking
	KillSouls  int       `json:"killSouls"`  // souls that came right after those kills
	SessionAdd int       `json:"sessionAdd"` // souls gained this session
}

type entity struct {
	code    int
	seen    time.Time
	dead    bool
	x, y, z float32
	hasPos  bool
}

type kill struct {
	code int
	at   time.Time
	used bool
	mine bool
}

type State struct {
	mu sync.Mutex

	Pets     map[int]*Pet      `json:"pets"`
	PetSlug  map[int]string    `json:"petSlug"`  // pet id -> wikily slug (learned)
	NpcPet   map[int]int       `json:"npcPet"`   // npc code -> pet id (learned)
	ListSeen time.Time         `json:"listSeen"` // last full list from login
	Settings Settings          `json:"settings"`
	Calibs   map[string]Calib  `json:"calibs"` // zone map calibration, learned
	Extra    map[string]string `json:"extra,omitempty"`

	entities  map[int]*entity
	obs       []obs
	kills     []kill
	lastFrame time.Time
	frames    int
	path      string
	dirty     bool
	data      *PetData
	listeners []chan struct{}
}

func NewState(path string, data *PetData) *State {
	s := &State{
		Pets:     map[int]*Pet{},
		PetSlug:  map[int]string{},
		NpcPet:   map[int]int{},
		entities: map[int]*entity{},
		path:     path,
		data:     data,
		Settings: defaultSettings(),
		Calibs:   map[string]Calib{},
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, s)
		if s.Pets == nil {
			s.Pets = map[int]*Pet{}
		}
		if s.PetSlug == nil {
			s.PetSlug = map[int]string{}
		}
		if s.NpcPet == nil {
			s.NpcPet = map[int]int{}
		}
		for _, p := range s.Pets {
			p.SessionAdd = 0
		}
		s.Settings.normalize()
		if s.Calibs == nil {
			s.Calibs = map[string]Calib{}
		}
	}
	return s
}

func (s *State) Subscribe() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{}, 1)
	s.listeners = append(s.listeners, ch)
	return ch
}

// Touch pushes a fresh view to the overlay.
func (s *State) Touch() {
	s.mu.Lock()
	s.notify()
	s.mu.Unlock()
}

func (s *State) notify() {
	for _, ch := range s.listeners {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *State) Save() {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	b, _ := json.MarshalIndent(s, "", " ")
	s.dirty = false
	s.mu.Unlock()
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

func u32(b []byte, o int) (int, bool) {
	if o < 0 || o+4 > len(b) {
		return 0, false
	}
	return int(binary.LittleEndian.Uint32(b[o:])), true
}

func validPetID(id int) bool { return id >= 1000 && id < 10000 }

// HandleFrame processes one server frame. now = capture time.
func (s *State) HandleFrame(f Frame, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames++
	s.lastFrame = now
	changed := false
	switch f.Op {
	case opPetList:
		changed = s.onPetList(f.Data, now)
	case opPetSoul:
		changed = s.onPetSoul(f.Data, now)
	case opNpcSpawn, opNpcState:
		s.onNpc(f.Data, f.Op == opNpcSpawn, now)
	case opNpcHP:
		changed = s.onHP(f.Data, now)
	case opMove1, opMove2, opMove3:
		s.onMove(f.Data, now)
	case opMyKill:
		changed = s.onMyKill(now)
	}
	if changed {
		s.dirty = true
		s.notify()
	}
}

// 0090: u32 ?, varint n, n*(u32 id, u32 id, u32 level), varint m, m*(u32 id, u32 souls)
func (s *State) onPetList(d []byte, now time.Time) bool {
	o := 4
	n, k, ok := readVarint(d, o)
	if !ok || n <= 0 || n > 2000 {
		return false
	}
	o += k
	levels := map[int]int{}
	for i := 0; i < n; i++ {
		a, ok1 := u32(d, o)
		b, ok2 := u32(d, o+4)
		lv, ok3 := u32(d, o+8)
		if !ok1 || !ok2 || !ok3 || a != b || !validPetID(a) || lv > 10 {
			return false
		}
		levels[a] = lv
		o += 12
	}
	m, k, ok := readVarint(d, o)
	if !ok || m > 2000 {
		return false
	}
	o += k
	souls := map[int]int{}
	for i := 0; i < m; i++ {
		a, ok1 := u32(d, o)
		v, ok2 := u32(d, o+4)
		if !ok1 || !ok2 || !validPetID(a) || v > 100000 {
			return false
		}
		souls[a] = v
		o += 8
	}
	seen := map[int]bool{}
	for id, lv := range levels {
		p := s.pet(id)
		p.Level, p.Souls = lv, souls[id]
		seen[id] = true
	}
	for id, v := range souls {
		if !seen[id] {
			p := s.pet(id)
			p.Level, p.Souls = 0, v
		}
	}
	s.ListSeen = now
	logf("pet list: %d owned, %d with souls", len(levels), len(souls))
	return true
}

func (s *State) pet(id int) *Pet {
	p := s.Pets[id]
	if p == nil {
		p = &Pet{ID: id}
		s.Pets[id] = p
	}
	return p
}

// 0D90: varint n, n*(u32 pet id, u32 delta)
func (s *State) onPetSoul(d []byte, now time.Time) bool {
	n, k, ok := readVarint(d, 0)
	if !ok || n <= 0 || n > 50 {
		return false
	}
	o := k
	changed := false
	for i := 0; i < n; i++ {
		id, ok1 := u32(d, o)
		delta, ok2 := u32(d, o+4)
		o += 8
		if !ok1 || !ok2 || !validPetID(id) || delta <= 0 || delta > 1000 {
			continue
		}
		p := s.pet(id)
		p.Souls += delta
		p.SessionAdd += delta
		p.LastGain = now
		for need := soulsToNext(p.Level); need > 0 && p.Souls >= need; need = soulsToNext(p.Level) {
			p.Souls -= need
			p.Level++
		}
		s.attributeSoul(p, delta, now)
		logf("soul +%d -> pet %d %s Lv%d %d/%d", delta, id, s.petName(id), p.Level, p.Souls, soulsToNext(p.Level))
		changed = true
	}
	return changed
}

// attributeSoul links the soul to the kill that caused it, learning names as it goes.
func (s *State) attributeSoul(p *Pet, delta int, now time.Time) {
	for i := len(s.kills) - 1; i >= 0; i-- {
		kl := &s.kills[i]
		if kl.used || !kl.mine || now.Sub(kl.at) > killWindow || now.Before(kl.at.Add(-time.Second)) {
			continue
		}
		if mapped, ok := s.NpcPet[kl.code]; ok && mapped != p.ID {
			continue
		}
		slug := s.data.SlugForNpc(kl.code)
		if known, ok := s.slugOf(p.ID); ok && slug != "" && slug != known {
			continue
		}
		kl.used = true
		if _, ok := s.NpcPet[kl.code]; !ok {
			s.NpcPet[kl.code] = p.ID
		}
		if _, ok := s.slugOf(p.ID); !ok && slug != "" {
			s.PetSlug[p.ID] = slug
			logf("learned: pet %d = %s", p.ID, slug)
		}
		p.KillSouls += delta
		return
	}
}

func (s *State) onNpc(d []byte, spawn bool, now time.Time) {
	id, k, ok := readVarint(d, 0)
	if !ok || id <= 0 || k+7 > len(d) {
		return
	}
	o := k
	if spawn {
		t0, t1, t2 := d[o], d[o+1], d[o+2]
		if !((t2 == 0 && (t1 == 0x10 || t1 == 0x20 || t1 == 0x21 || t1 == 0x22 || t1 == 0x30 || t1 == 0x32)) || (t0 == 0x1C && t1 == 0 && t2 == 0)) {
			return
		}
	}
	code, _ := u32(d, o+3)
	if code < 2_000_000 || code > 2_999_999 {
		return
	}
	e := &entity{code: code, seen: now}
	if old := s.entities[id]; old != nil && old.code == code {
		e.dead, e.x, e.y, e.z, e.hasPos = old.dead, old.x, old.y, old.z, old.hasPos
	}
	// position: three floats right after the npc code (offset 9 from the entity id)
	if x, y, z, ok := f32x3(d, o+9); ok && plausiblePos(x, y, z) {
		e.x, e.y, e.z, e.hasPos = x, y, z, true
	}
	s.entities[id] = e
	if e.hasPos {
		s.observeSpawn(code, e.x, e.y)
	}
}

// 1A37/1B37/1C37: entity varint, 1-2 header bytes, then x,y,z floats.
// The header length varies, so take the offset whose position is closest
// to where the entity was last seen.
func (s *State) onMove(d []byte, now time.Time) {
	id, k, ok := readVarint(d, 0)
	if !ok {
		return
	}
	e := s.entities[id]
	if e == nil || !e.hasPos {
		return
	}
	best, bd := -1, float32(4000*4000)
	var bx, by, bz float32
	for _, off := range []int{1, 2} {
		x, y, z, ok := f32x3(d, k+off)
		if !ok || !plausiblePos(x, y, z) {
			continue
		}
		dx, dy := x-e.x, y-e.y
		if dd := dx*dx + dy*dy; dd < bd {
			best, bd, bx, by, bz = off, dd, x, y, z
		}
	}
	if best >= 0 {
		e.x, e.y, e.z, e.seen = bx, by, bz, now
	}
}

func f32x3(b []byte, o int) (x, y, z float32, ok bool) {
	if o < 0 || o+12 > len(b) {
		return 0, 0, 0, false
	}
	x = math.Float32frombits(binary.LittleEndian.Uint32(b[o:]))
	y = math.Float32frombits(binary.LittleEndian.Uint32(b[o+4:]))
	z = math.Float32frombits(binary.LittleEndian.Uint32(b[o+8:]))
	return x, y, z, true
}

func plausiblePos(x, y, z float32) bool {
	ok := func(v float32, lim float32) bool { return v == v && v > -lim && v < lim }
	return ok(x, 1e6) && ok(y, 1e6) && ok(z, 1e5) && (x > 1 || x < -1) && (y > 1 || y < -1)
}

// 008D: varint npc, varint 2, varint 1, varint 0, u32 hp
func (s *State) onHP(d []byte, now time.Time) bool {
	o := 0
	var v [4]int
	for i := range v {
		x, k, ok := readVarint(d, o)
		if !ok {
			return false
		}
		v[i] = x
		o += k
	}
	if v[1] != 2 || v[2] != 1 || v[3] != 0 {
		return false
	}
	hp, ok := u32(d, o)
	if !ok {
		return false
	}
	e := s.entities[v[0]]
	if e == nil {
		return false
	}
	e.seen = now
	if hp != 0 || e.dead {
		return false
	}
	e.dead = true
	s.kills = append(s.kills, kill{code: e.code, at: now})
	if len(s.kills) > 64 {
		s.kills = s.kills[len(s.kills)-64:]
	}
	return false
}

// onMyKill marks the most recent death as ours and counts it toward its pet.
func (s *State) onMyKill(now time.Time) bool {
	for i := len(s.kills) - 1; i >= 0; i-- {
		kl := &s.kills[i]
		if now.Sub(kl.at) > time.Second {
			break
		}
		if kl.mine {
			continue
		}
		kl.mine = true
		if pid := s.petForNpc(kl.code); pid != 0 {
			s.pet(pid).Kills++
			return true
		}
		return false
	}
	return false
}

func (s *State) petForNpc(code int) int {
	if pid, ok := s.NpcPet[code]; ok {
		return pid
	}
	if slug := s.data.SlugForNpc(code); slug != "" {
		return s.idOfSlug(slug)
	}
	return 0
}

// slugOf: user/learned override first, then the built-in id table.
func (s *State) slugOf(id int) (string, bool) {
	if sl, ok := s.PetSlug[id]; ok {
		return sl, true
	}
	if sl := s.data.SlugForPetID(id); sl != "" {
		return sl, true
	}
	return "", false
}

func (s *State) idOfSlug(slug string) int {
	for id, sl := range s.PetSlug {
		if sl == slug {
			return id
		}
	}
	return s.data.IDForSlug(slug)
}

func (s *State) petName(id int) string {
	if sl, ok := s.slugOf(id); ok {
		if info := s.data.Pet(sl); info != nil {
			return info.Name
		}
		return sl
	}
	return ""
}

// ---- view model for the overlay ----

type PetView struct {
	ID        int     `json:"id"`
	Name      string  `json:"name"`
	Slug      string  `json:"slug,omitempty"`
	Icon      string  `json:"icon,omitempty"`
	Genus     string  `json:"genus,omitempty"`
	Level     int     `json:"level"`
	Souls     int     `json:"souls"`
	Need      int     `json:"need"`
	Max       bool    `json:"max"`
	Ago       int     `json:"ago"` // seconds since last soul, -1 = never this session
	Session   int     `json:"session"`
	Rate      float64 `json:"rate"`      // souls per kill (learned, default 1)
	KillsLeft int     `json:"killsLeft"` // estimate
	Nearby    int     `json:"nearby"`
}

type View struct {
	Version  string      `json:"version"`
	Update   *UpdateInfo `json:"update,omitempty"`
	Settings Settings    `json:"settings"`
	Focus    *PetView    `json:"focus,omitempty"` // the pet you're farming right now
	Map      *MapView    `json:"map,omitempty"`
	Session  int         `json:"session"` // souls gained this session
	Live     bool        `json:"live"`
	Frames   int         `json:"frames"`
	HaveList bool        `json:"haveList"`
	Recent   []PetView   `json:"recent"`
	Nearby   []PetView   `json:"nearby"`
	All      []PetView   `json:"all"`
}

func (s *State) View(now time.Time) View {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := View{Version: version, Update: currentUpdate(), Settings: s.Settings, Live: now.Sub(s.lastFrame) < 15*time.Second, Frames: s.frames, HaveList: !s.ListSeen.IsZero()}

	mk := func(p *Pet) PetView {
		pv := PetView{ID: p.ID, Level: p.Level, Souls: p.Souls, Need: soulsToNext(p.Level), Session: p.SessionAdd, Ago: -1}
		pv.Max = pv.Need == 0
		if sl, ok := s.slugOf(p.ID); ok {
			pv.Slug = sl
			if info := s.data.Pet(sl); info != nil {
				pv.Name, pv.Icon, pv.Genus = info.Name, info.Icon, info.Genus
			}
		}
		if pv.Name == "" {
			pv.Name = s.data.PetIDName(p.ID)
		}
		if pv.Name == "" {
			pv.Name = "Pet #" + itoa(p.ID)
		}
		if !p.LastGain.IsZero() {
			pv.Ago = int(now.Sub(p.LastGain).Seconds())
		}
		pv.Rate = 1
		if p.Kills >= 5 && p.KillSouls > 0 {
			pv.Rate = float64(p.KillSouls) / float64(p.Kills)
		}
		if !pv.Max {
			left := pv.Need - pv.Souls
			pv.KillsLeft = int(float64(left)/pv.Rate + 0.999)
		}
		return pv
	}

	// nearby: live NPC entities grouped by pet
	counts := map[string]int{}
	for id, e := range s.entities {
		if now.Sub(e.seen) > entityTTL {
			delete(s.entities, id)
			continue
		}
		if e.dead {
			continue
		}
		key := ""
		if pid, ok := s.NpcPet[e.code]; ok {
			if sl, ok := s.slugOf(pid); ok {
				key = sl
			} else {
				key = "#" + itoa(pid)
			}
		} else if sl := s.data.SlugForNpc(e.code); sl != "" {
			key = sl
		}
		if key != "" {
			counts[key]++
		}
	}
	for key, c := range counts {
		var pv PetView
		if strings.HasPrefix(key, "#") {
			id := atoi(key[1:])
			pv = mk(s.pet(id))
		} else if id := s.idOfSlug(key); id != 0 && s.Pets[id] != nil {
			pv = mk(s.Pets[id])
		} else if id != 0 && !s.ListSeen.IsZero() {
			pv = mk(&Pet{ID: id}) // in the table but not in your list = not collected yet
		} else {
			info := s.data.Pet(key)
			if info == nil {
				continue
			}
			pv = PetView{Name: info.Name, Slug: key, Icon: info.Icon, Genus: info.Genus, Need: -1, Ago: -1}
		}
		pv.Nearby = c
		v.Nearby = append(v.Nearby, pv)
	}
	sort.Slice(v.Nearby, func(i, j int) bool {
		if v.Nearby[i].Max != v.Nearby[j].Max {
			return !v.Nearby[i].Max
		}
		return v.Nearby[i].Nearby > v.Nearby[j].Nearby
	})

	for _, p := range s.Pets {
		pv := mk(p)
		v.All = append(v.All, pv)
		if pv.Ago >= 0 {
			v.Recent = append(v.Recent, pv)
		}
	}
	sort.Slice(v.Recent, func(i, j int) bool { return v.Recent[i].Ago < v.Recent[j].Ago })
	if len(v.Recent) > recentLimit {
		v.Recent = v.Recent[:recentLimit]
	}
	sort.Slice(v.All, func(i, j int) bool {
		a, b := v.All[i], v.All[j]
		if a.Max != b.Max {
			return !a.Max
		}
		if a.Level != b.Level {
			return a.Level > b.Level
		}
		return a.Souls > b.Souls
	})
	for _, p := range s.Pets {
		v.Session += p.SessionAdd
	}
	v.Map = s.mapView()
	// focus: last pet you got a soul for (if recent), else the busiest unfinished pet nearby
	if len(v.Recent) > 0 && v.Recent[0].Ago >= 0 && v.Recent[0].Ago < 300 && !v.Recent[0].Max {
		f := v.Recent[0]
		for _, n := range v.Nearby {
			if n.ID == f.ID {
				f.Nearby = n.Nearby
			}
		}
		v.Focus = &f
	} else {
		for _, n := range v.Nearby {
			if !n.Max && n.Need > 0 {
				f := n
				v.Focus = &f
				break
			}
		}
	}
	return v
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
