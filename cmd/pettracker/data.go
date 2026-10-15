package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NPC code -> English name, extracted from Aion2Flow's en-US resource pack.
//
//go:embed assets/npcs.json.gz
var npcNamesGz []byte

// Game pet id -> English pet name (from aion2wiki.date, which uses the client's ids).
//
//go:embed assets/petids.json
var petIDsJSON []byte

// Snapshot of wikily.gg pet data (monsters, icons) used until a fresh copy downloads.
//
//go:embed assets/pets-snapshot.json.gz
var petsSnapshotGz []byte

type PetInfo struct {
	Slug     string   `json:"s"`
	Name     string   `json:"n"`
	Genus    string   `json:"g"`
	Icon     string   `json:"i"`
	Monsters []string `json:"m"`
	Zones    []string `json:"z"`
}

type PetData struct {
	mu       sync.RWMutex
	npcName  map[int]string
	pets     map[string]*PetInfo
	monster  map[string]string // lower(monster name) -> pet slug
	npcSlug  map[int]string    // cache
	idName   map[int]string    // game pet id -> name
	idSlug   map[int]string    // game pet id -> wikily slug
	cacheDir string
}

// LoadPetData loads built-in data plus the cache, and refreshes the cache in the background.
func LoadPetData(cacheDir string) *PetData { return loadPetData(cacheDir, true) }

// LoadPetDataOffline uses only built-in data and the cache (tests, replays).
func LoadPetDataOffline(cacheDir string) *PetData { return loadPetData(cacheDir, false) }

func loadPetData(cacheDir string, online bool) *PetData {
	d := &PetData{npcName: map[int]string{}, pets: map[string]*PetInfo{}, monster: map[string]string{}, npcSlug: map[int]string{}, idName: map[int]string{}, idSlug: map[int]string{}, cacheDir: cacheDir}
	var raw map[string]string
	if json.Unmarshal(petIDsJSON, &raw) == nil {
		for k, v := range raw {
			if n, err := strconv.Atoi(k); err == nil {
				d.idName[n] = v
			}
		}
	}
	if zr, err := gzip.NewReader(bytes.NewReader(petsSnapshotGz)); err == nil {
		var list []*PetInfo
		if json.NewDecoder(zr).Decode(&list) == nil {
			d.setPets(list)
		}
	}
	if zr, err := gzip.NewReader(bytes.NewReader(npcNamesGz)); err == nil {
		var raw map[string]string
		if json.NewDecoder(zr).Decode(&raw) == nil {
			for k, v := range raw {
				if n, err := strconv.Atoi(k); err == nil {
					d.npcName[n] = v
				}
			}
		}
	}
	cache := cacheDir + string(os.PathSeparator) + "pets-data.json"
	if b, err := os.ReadFile(cache); err == nil {
		var list []*PetInfo
		if json.Unmarshal(b, &list) == nil {
			d.setPets(list)
		}
	}
	st, err := os.Stat(cache)
	if online && (err != nil || time.Since(st.ModTime()) > 7*24*time.Hour || len(d.pets) == 0) {
		go d.refresh(cache)
	}
	return d
}

func (d *PetData) setPets(list []*PetInfo) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pets = map[string]*PetInfo{}
	d.monster = map[string]string{}
	d.npcSlug = map[int]string{}
	byName := map[string]string{}
	for _, p := range list {
		d.pets[p.Slug] = p
		byName[normName(p.Name)] = p.Slug
		for _, m := range p.Monsters {
			d.monster[strings.ToLower(strings.TrimSpace(m))] = p.Slug
		}
	}
	d.idSlug = map[int]string{}
	for id, n := range d.idName {
		if sl, ok := byName[normName(n)]; ok {
			d.idSlug[id] = sl
		}
	}
}

func normName(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// SlugForPetID maps the game's pet id to its wiki slug.
func (d *PetData) SlugForPetID(id int) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.idSlug[id]
}

// PetIDNames returns id -> name for every known pet.
func (d *PetData) PetIDName(id int) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.idName[id]
}

// IDForSlug is the reverse of SlugForPetID.
func (d *PetData) IDForSlug(slug string) int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for id, sl := range d.idSlug {
		if sl == slug {
			return id
		}
	}
	return 0
}

// refresh downloads pet + monster data from wikily.gg's public dataset.
func (d *PetData) refresh(cache string) {
	base := "https://wikily.gg/api/wikis/aion-2/datasets/pets/list?limit=120&locale=en&offset="
	if v := os.Getenv("PT_DATA_URL"); v != "" {
		base = v
	}
	type group struct {
		Label    string `json:"label"`
		Category string `json:"category"`
	}
	type loc struct {
		Groups []group `json:"groups"`
	}
	type src struct {
		Href  string `json:"href"`
		Title string `json:"title"`
	}
	type row struct {
		Data struct {
			Slug     string `json:"slug"`
			Name     string `json:"name"`
			PetType  string `json:"petType"`
			Type     string `json:"type"`
			Icon     string `json:"icon"`
			Loc1     json.RawMessage `json:"loc1"`
			Loc2     json.RawMessage `json:"loc2"`
			Loc3     json.RawMessage `json:"loc3"`
			SrcPart5 json.RawMessage `json:"srcPart5"`
			SrcPart6 json.RawMessage `json:"srcPart6"`
		} `json:"data"`
	}
	var list []*PetInfo
	client := &http.Client{Timeout: 20 * time.Second}
	for off := 0; off < 2000; {
		req, _ := http.NewRequest("GET", base+strconv.Itoa(off), nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36 PetTracker/"+version)
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			logf("pet data download failed: %v", err)
			return
		}
		var page struct {
			Rows       []row `json:"rows"`
			TotalCount int   `json:"totalCount"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil {
			logf("pet data parse failed: %v", err)
			return
		}
		for _, r := range page.Rows {
			x := r.Data
			p := &PetInfo{Slug: x.Slug, Name: x.Name, Genus: x.PetType, Icon: x.Icon}
			if p.Genus == "" {
				p.Genus = x.Type
			}
			seenM, seenZ := map[string]bool{}, map[string]bool{}
			for _, raw := range []json.RawMessage{x.Loc1, x.Loc2, x.Loc3} {
				var l loc
				if len(raw) == 0 || json.Unmarshal(raw, &l) != nil {
					continue
				}
				for _, g := range l.Groups {
					if g.Label != "" && !seenM[g.Label] {
						seenM[g.Label] = true
						p.Monsters = append(p.Monsters, g.Label)
					}
					if g.Category != "" && !seenZ[g.Category] {
						seenZ[g.Category] = true
						p.Zones = append(p.Zones, g.Category)
					}
				}
			}
			var srcs, more []src
			_ = json.Unmarshal(x.SrcPart5, &srcs)
			_ = json.Unmarshal(x.SrcPart6, &more)
			for _, s := range append(srcs, more...) {
				if strings.HasPrefix(s.Href, "/monsters/") && !seenM[s.Title] {
					seenM[s.Title] = true
					p.Monsters = append(p.Monsters, s.Title)
				}
			}
			if p.Slug != "" {
				list = append(list, p)
			}
		}
		off += len(page.Rows)
		if len(page.Rows) == 0 || off >= page.TotalCount {
			break
		}
	}
	if len(list) == 0 {
		return
	}
	d.setPets(list)
	if b, err := json.Marshal(list); err == nil {
		_ = os.MkdirAll(d.cacheDir, 0o755)
		_ = os.WriteFile(cache, b, 0o644)
	}
	logf("pet data: %d pets loaded from wikily.gg", len(list))
}

func (d *PetData) NpcName(code int) string {
	return d.npcName[code]
}

// SlugForNpc maps an NPC code to the pet whose souls it drops (by monster name).
func (d *PetData) SlugForNpc(code int) string {
	d.mu.RLock()
	if s, ok := d.npcSlug[code]; ok {
		d.mu.RUnlock()
		return s
	}
	d.mu.RUnlock()
	name := strings.ToLower(d.npcName[code])
	slug := ""
	if name != "" {
		slug = d.monsterLookup(name)
	}
	d.mu.Lock()
	if len(d.pets) > 0 { // don't cache misses before data loads
		d.npcSlug[code] = slug
	}
	d.mu.Unlock()
	return slug
}

func (d *PetData) monsterLookup(name string) string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.monster[name]
}

func (d *PetData) Pet(slug string) *PetInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.pets[slug]
}

type PetName struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Genus string `json:"genus"`
}

func (d *PetData) List() []PetName {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]PetName, 0, len(d.pets))
	for _, p := range d.pets {
		out = append(out, PetName{p.Slug, p.Name, p.Genus})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
