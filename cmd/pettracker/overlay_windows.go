//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32                      = syscall.NewLazyDLL("user32.dll")
	dwmapi                      = syscall.NewLazyDLL("dwmapi.dll")
	pEnumWindows                = user32.NewProc("EnumWindows")
	pGetWindowTextW             = user32.NewProc("GetWindowTextW")
	pIsWindowVisible            = user32.NewProc("IsWindowVisible")
	pSetWindowPos               = user32.NewProc("SetWindowPos")
	pGetWindowLongPtrW          = user32.NewProc("GetWindowLongPtrW")
	pSetWindowLongPtrW          = user32.NewProc("SetWindowLongPtrW")
	pSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	pMessageBoxW                = user32.NewProc("MessageBoxW")
	pGetCursorPos               = user32.NewProc("GetCursorPos")
	pGetWindowRect              = user32.NewProc("GetWindowRect")
	pGetAsyncKeyState           = user32.NewProc("GetAsyncKeyState")
	pShowWindow                 = user32.NewProc("ShowWindow")
	pPostMessageW               = user32.NewProc("PostMessageW")
	pRegisterHotKey             = user32.NewProc("RegisterHotKey")
	pGetMessageW                = user32.NewProc("GetMessageW")
	pDwmSetWindowAttribute      = dwmapi.NewProc("DwmSetWindowAttribute")
	pEnumChildWindows           = user32.NewProc("EnumChildWindows")
	pGetClassNameW              = user32.NewProc("GetClassNameW")
	pSetWindowRgn               = user32.NewProc("SetWindowRgn")
	gdi32                       = syscall.NewLazyDLL("gdi32.dll")
	pCreateRoundRectRgn         = gdi32.NewProc("CreateRoundRectRgn")
)

const (
	gwlStyle        = ^uintptr(15) // -16
	gwlExStyle      = ^uintptr(19) // -20
	wsCaption       = 0x00C00000
	wsThickFrame    = 0x00040000
	wsSysMenu       = 0x00080000
	wsMinimizeBox   = 0x00020000
	wsMaximizeBox   = 0x00010000
	wsPopup         = 0x80000000
	wsExLayered     = 0x00080000
	wsExTransparent = 0x00000020
	wsExToolWindow  = 0x00000080
	wsExAppWindow   = 0x00040000
	lwaAlpha        = 0x2
	hwndTopmost     = ^uintptr(0) // -1
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020
	swMinimize      = 6
	wmClose         = 0x0010
	wmHotkey        = 0x0312
	vkLButton       = 0x01
	modControl      = 0x0002
	modShift        = 0x0004
	modNoRepeat     = 0x4000
	overlayTitle    = "Pet Tracker"
	dwmCornerPref   = 33 // DWMWA_WINDOW_CORNER_PREFERENCE
	dwmRound        = 2
)

type point struct{ X, Y int32 }
type rect struct{ L, T, R, B int32 }

var (
	winMu  sync.Mutex
	curWin uintptr
	st0    *State // set by openOverlay, used by hotkey + settings
)

func fatalBox(msg string) {
	t, _ := syscall.UTF16PtrFromString("Pet Tracker")
	m, _ := syscall.UTF16PtrFromString(msg)
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x10)
}

func findEdge() string {
	var cands []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
		if base := os.Getenv(env); base != "" {
			cands = append(cands, filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"))
		}
	}
	for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LOCALAPPDATA"} {
		if base := os.Getenv(env); base != "" {
			cands = append(cands, filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"))
		}
	}
	for _, c := range cands {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// openOverlay opens the page as a small app window and keeps it on top of the game.
func openOverlay(url string, st *State) {
	st0 = st
	browser := findEdge()
	if browser == "" {
		logf("Edge/Chrome not found, opening default browser")
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
		return
	}
	set := st.GetSettings()
	profile := filepath.Join(dataDir(), "window")
	cmd := exec.Command(browser,
		"--app="+url,
		"--user-data-dir="+profile,
		fmt.Sprintf("--window-size=%d,%d", set.W, set.H),
		fmt.Sprintf("--window-position=%d,%d", set.X, set.Y),
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate,msEdgeSidebarV2,msUndersideButton",
	)
	if err := cmd.Start(); err != nil {
		logf("could not open overlay window: %v", err)
		return
	}
	go hotkeyLoop()
	go lockWatcher()
	// Edge often hands the window to another process and exits right away,
	// so the process can't tell us when the window closes. keepOnTop watches the window itself.
	go func() { _ = cmd.Wait() }()
	keepOnTop(cmd)
}

// syscall.NewCallback slots are never freed, so the enum callback is created once.
var (
	enumMu    sync.Mutex
	enumFound []uintptr
	enumCB    = syscall.NewCallback(func(h, _ uintptr) uintptr {
		if v, _, _ := pIsWindowVisible.Call(h); v == 0 {
			return 1
		}
		buf := make([]uint16, 256)
		n, _, _ := pGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), 256)
		if n > 0 && strings.HasPrefix(syscall.UTF16ToString(buf[:n]), overlayTitle) {
			enumFound = append(enumFound, h)
		}
		return 1
	})
)

func overlayWindows() []uintptr {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumFound = nil
	pEnumWindows.Call(enumCB, 0)
	return append([]uintptr(nil), enumFound...)
}

func window() uintptr {
	winMu.Lock()
	defer winMu.Unlock()
	return curWin
}

// Edge draws its own title bar inside the window, so styles can't remove it.
// Instead the window is clipped (SetWindowRgn) to the web page area: the
// Chrome_RenderWidgetHostHWND child is exactly the page, so its rect is the cut.
var (
	childMu    sync.Mutex
	childFound uintptr
	childCB    = syscall.NewCallback(func(h, _ uintptr) uintptr {
		buf := make([]uint16, 128)
		n, _, _ := pGetClassNameW.Call(h, uintptr(unsafe.Pointer(&buf[0])), 128)
		if n > 0 && syscall.UTF16ToString(buf[:n]) == "Chrome_RenderWidgetHostHWND" {
			if v, _, _ := pIsWindowVisible.Call(h); v != 0 {
				childFound = h
				return 0
			}
		}
		return 1
	})
	lastClip rect
)

func pageRect(top uintptr) (rect, bool) {
	childMu.Lock()
	defer childMu.Unlock()
	childFound = 0
	pEnumChildWindows.Call(top, childCB, 0)
	if childFound == 0 {
		return rect{}, false
	}
	var r rect
	pGetWindowRect.Call(childFound, uintptr(unsafe.Pointer(&r)))
	return r, r.R > r.L && r.B > r.T
}

// clipToPage hides Edge's title bar and borders by clipping the window to the page.
func clipToPage(h uintptr) {
	page, ok := pageRect(h)
	if !ok {
		return
	}
	var win rect
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&win)))
	c := rect{page.L - win.L, page.T - win.T, page.R - win.L, page.B - win.T}
	if c == lastClip {
		return
	}
	lastClip = c
	rgn, _, _ := pCreateRoundRectRgn.Call(uintptr(c.L), uintptr(c.T), uintptr(c.R+1), uintptr(c.B+1), 16, 16)
	if rgn != 0 {
		pSetWindowRgn.Call(h, rgn, 1) // the system owns rgn now
		logf("clipped window to page (%dx%d, title bar %dpx)", c.R-c.L, c.B-c.T, c.T)
	}
}

// Lock hotspot: the lock button stays clickable while the rest is click-through.
// The page reports the button's rect (CSS px, relative to the page) and the
// device pixel ratio; a watcher turns click-through off while the cursor is on it.
type hotspot struct{ X, Y, W, H, DPR float64 }

var (
	hotMu    sync.Mutex
	hot      hotspot
	overLock bool
)

func setLockHotspot(x, y, w, h, dpr float64) {
	hotMu.Lock()
	hot = hotspot{x, y, w, h, dpr}
	hotMu.Unlock()
}

func lockWatcher() {
	for {
		time.Sleep(40 * time.Millisecond)
		h := window()
		if h == 0 || st0 == nil {
			continue
		}
		set := st0.GetSettings()
		inside := false
		if set.Locked {
			hotMu.Lock()
			hs := hot
			hotMu.Unlock()
			if page, ok := pageRect(h); ok && hs.DPR > 0 && hs.W > 0 {
				var p point
				pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
				pad := 4.0
				l := float64(page.L) + (hs.X-pad)*hs.DPR
				t := float64(page.T) + (hs.Y-pad)*hs.DPR
				r := float64(page.L) + (hs.X+hs.W+pad)*hs.DPR
				b := float64(page.T) + (hs.Y+hs.H+pad)*hs.DPR
				fx, fy := float64(p.X), float64(p.Y)
				inside = fx >= l && fx <= r && fy >= t && fy <= b
			}
		}
		hotMu.Lock()
		changed := inside != overLock
		overLock = inside
		hotMu.Unlock()
		if changed {
			applyExStyle(h, set)
		}
	}
}

func applyExStyle(h uintptr, set Settings) {
	hotMu.Lock()
	over := overLock
	hotMu.Unlock()
	ex, _, _ := pGetWindowLongPtrW.Call(h, gwlExStyle)
	want := ex | wsExLayered
	if set.Locked && !over {
		want |= wsExTransparent
	} else {
		want &^= wsExTransparent
	}
	if want != ex {
		pSetWindowLongPtrW.Call(h, gwlExStyle, want)
	}
	alpha := uintptr(set.Opacity * 255 / 100)
	pSetLayeredWindowAttributes.Call(h, 0, alpha, lwaAlpha)
}

func applyWindowSettings(set Settings) {
	if h := window(); h != 0 {
		applyExStyle(h, set)
	}
}

// keepOnTop pins the overlay above the game, and quits when the window is closed.
func keepOnTop(cmd *exec.Cmd) {
	seen, missing := false, 0
	var last uintptr
	for {
		wins := overlayWindows()
		if len(wins) > 0 {
			seen, missing = true, 0
			h := wins[0]
			winMu.Lock()
			curWin = h
			winMu.Unlock()
			if h != last {
				logf("overlay window ready")
				last = h
				go func(h uintptr) { // twice: Edge can reset the icon once the page loads
					setWindowIdentity(h)
					time.Sleep(4 * time.Second)
					setWindowIdentity(h)
				}(h)
				// put it exactly where it was last time (saved in screen pixels)
				set := st0.GetSettings()
				pSetWindowPos.Call(h, 0, uintptr(set.X), uintptr(set.Y), uintptr(set.W), uintptr(set.H), swpNoZOrder|swpNoActivate)
			}
			clipToPage(h)
			applyExStyle(h, st0.GetSettings())
			pSetWindowPos.Call(h, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
		} else if seen {
			missing++
			if missing >= 3 {
				logf("overlay window closed, exiting")
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				st0.dirty = true
				st0.Save()
				os.Exit(0)
			}
		}
		time.Sleep(1500 * time.Millisecond)
	}
}

var dragging sync.Mutex

// windowDrag follows the mouse while the left button is held: moves or resizes the overlay.
func windowDrag(st *State, resize bool) {
	h := window()
	if h == 0 || !dragging.TryLock() {
		return
	}
	defer dragging.Unlock()
	var start point
	var r rect
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&start)))
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	w0, h0 := r.R-r.L, r.B-r.T
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if s, _, _ := pGetAsyncKeyState.Call(vkLButton); s&0x8000 == 0 {
			break
		}
		var p point
		pGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
		dx, dy := p.X-start.X, p.Y-start.Y
		if resize {
			clipToPage(h)
			nw, nh := w0+dx, h0+dy
			if nw < 260 {
				nw = 260
			}
			if nh < 200 {
				nh = 200
			}
			pSetWindowPos.Call(h, 0, 0, 0, uintptr(nw), uintptr(nh), swpNoMove|swpNoZOrder|swpNoActivate)
		} else {
			pSetWindowPos.Call(h, 0, uintptr(int(r.L+dx)), uintptr(int(r.T+dy)), 0, 0, swpNoSize|swpNoZOrder|swpNoActivate)
		}
		time.Sleep(8 * time.Millisecond)
	}
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	time.Sleep(50 * time.Millisecond)
	clipToPage(h)
	st.UpdateSettings(func(s *Settings) {
		s.X, s.Y, s.W, s.H = int(r.L), int(r.T), int(r.R-r.L), int(r.B-r.T)
	})
}

// windowResize sets the overlay height (pixels), keeping width and position.
func windowResize(height int) {
	h := window()
	if h == 0 || height < 80 || height > 2000 {
		return
	}
	var r rect
	pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
	// height is the page height; add Edge's hidden title bar + border on top of it
	if page, ok := pageRect(h); ok {
		height += int((r.B - r.T) - (page.B - page.T))
	}
	pSetWindowPos.Call(h, 0, 0, 0, uintptr(r.R-r.L), uintptr(height), swpNoMove|swpNoZOrder|swpNoActivate)
	time.Sleep(80 * time.Millisecond)
	clipToPage(h)
	if st0 != nil {
		pGetWindowRect.Call(h, uintptr(unsafe.Pointer(&r)))
		st0.UpdateSettings(func(s *Settings) { s.H = int(r.B - r.T) })
	}
}

func windowMinimize() {
	if h := window(); h != 0 {
		pShowWindow.Call(h, swMinimize)
	}
}

func windowClose() {
	if h := window(); h != 0 {
		pPostMessageW.Call(h, wmClose, 0, 0)
	}
	go func() {
		time.Sleep(3 * time.Second)
		os.Exit(0)
	}()
}

// hotkeyLoop: Ctrl+Shift+P toggles lock (click-through) from anywhere, even in game.
func hotkeyLoop() {
	runtime.LockOSThread()
	const id = 1
	if r, _, err := pRegisterHotKey.Call(0, id, modControl|modShift|modNoRepeat, 'P'); r == 0 {
		logf("hotkey Ctrl+Shift+P unavailable: %v", err)
		return
	}
	logf("hotkey Ctrl+Shift+P toggles lock")
	var msg [48]byte
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		// MSG: hwnd (8), message (4) ...
		if *(*uint32)(unsafe.Pointer(&msg[8])) == wmHotkey && st0 != nil {
			st0.UpdateSettings(func(s *Settings) { s.Locked = !s.Locked })
		}
	}
}
