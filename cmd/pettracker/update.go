package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Update check: asks GitHub for the latest release a little after start and then
// every few hours. When there's a newer version the header shows a button; clicking
// it downloads the installer and runs it silently. The installer closes this app,
// replaces the files (pets and settings stay in %APPDATA%) and starts it again.

const releasesAPI = "https://api.github.com/repos/caiooncoding/aion-pet-tracker/releases/latest"

type UpdateInfo struct {
	Version string `json:"version"`
	URL     string `json:"-"` // installer download
	Page    string `json:"page"`
	State   string `json:"state,omitempty"` // "", "downloading", "installing", "failed"
}

var (
	updMu sync.Mutex
	upd   *UpdateInfo
)

func currentUpdate() *UpdateInfo {
	updMu.Lock()
	defer updMu.Unlock()
	if upd == nil {
		return nil
	}
	c := *upd
	return &c
}

func updateLoop(st *State) {
	if os.Getenv("PT_NO_UPDATE") != "" || version == "dev" {
		return
	}
	time.Sleep(20 * time.Second)
	for {
		if u, err := checkUpdate(); err != nil {
			logf("update check: %v", err)
		} else if u != nil {
			updMu.Lock()
			isNew := upd == nil || upd.Version != u.Version
			if isNew {
				upd = u
			}
			updMu.Unlock()
			if isNew {
				logf("update available: %s", u.Version)
				st.Touch()
			}
		}
		time.Sleep(6 * time.Hour)
	}
}

func checkUpdate() (*UpdateInfo, error) {
	req, _ := http.NewRequest("GET", releasesAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "PetTracker/"+version)
	c := &http.Client{Timeout: 15 * time.Second}
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("github: %s", res.Status)
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Page   string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(res.Body).Decode(&rel); err != nil {
		return nil, err
	}
	v := strings.TrimPrefix(rel.Tag, "v")
	if !newerVersion(v, version) {
		return nil, nil
	}
	u := &UpdateInfo{Version: v, Page: rel.Page}
	for _, a := range rel.Assets {
		if strings.HasPrefix(a.Name, "PetTracker-Setup-") && strings.HasSuffix(a.Name, ".exe") {
			u.URL = a.URL
		}
	}
	if u.URL == "" {
		return nil, nil // release without an installer (yet)
	}
	return u, nil
}

// newerVersion reports whether a > b for dotted numeric versions ("0.10.1" > "0.9.3").
func newerVersion(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func setUpdateState(st *State, s string) {
	updMu.Lock()
	if upd != nil {
		upd.State = s
	}
	updMu.Unlock()
	st.Touch()
}

// installUpdate downloads the installer to %TEMP% and starts it in silent mode.
func installUpdate(st *State) error {
	u := currentUpdate()
	if u == nil || u.URL == "" {
		return errors.New("no update")
	}
	if u.State == "downloading" || u.State == "installing" {
		return nil
	}
	setUpdateState(st, "downloading")
	path, err := download(u.URL, filepath.Join(os.TempDir(), "PetTracker-Setup-"+u.Version+".exe"))
	if err != nil {
		logf("update download: %v", err)
		setUpdateState(st, "failed")
		return err
	}
	setUpdateState(st, "installing")
	st.dirty = true
	st.Save()
	logf("running %s", path)
	if err := runInstaller(path); err != nil {
		logf("update run: %v", err)
		setUpdateState(st, "failed")
		return err
	}
	return nil
}

func download(url, dst string) (string, error) {
	c := &http.Client{Timeout: 5 * time.Minute}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "PetTracker/"+version)
	res, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("download: %s", res.Status)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, res.Body)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return "", err
	}
	if n < 1<<20 || (res.ContentLength > 0 && n != res.ContentLength) {
		os.Remove(tmp)
		return "", fmt.Errorf("download incomplete (%d bytes)", n)
	}
	os.Remove(dst)
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}
