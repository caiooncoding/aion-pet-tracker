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

// frameless strips the title bar and borders; the page draws its own header.
func frameless(h uintptr) {
	style, _, _ := pGetWindowLongPtrW.Call(h, gwlStyle)
	want := (style &^ (wsCaption | wsThickFrame | wsSysMenu | wsMinimizeBox | wsMaximizeBox)) | wsPopup
	if want != style {
		pSetWindowLongPtrW.Call(h, gwlStyle, want)
		pSetWindowPos.Call(h, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate|swpFrameChanged)
		pref := uint32(dwmRound)
		pDwmSetWindowAttribute.Call(h, dwmCornerPref, uintptr(unsafe.Pointer(&pref)), 4)
	}
}

func applyExStyle(h uintptr, set Settings) {
	ex, _, _ := pGetWindowLongPtrW.Call(h, gwlExStyle)
	want := ex | wsExLayered
	if set.Locked {
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
				frameless(h)
				// put it exactly where it was last time (saved in screen pixels)
				set := st0.GetSettings()
				pSetWindowPos.Call(h, 0, uintptr(set.X), uintptr(set.Y), uintptr(set.W), uintptr(set.H), swpNoZOrder|swpNoActivate)
			}
			frameless(h)
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
	pSetWindowPos.Call(h, 0, 0, 0, uintptr(r.R-r.L), uintptr(height), swpNoMove|swpNoZOrder|swpNoActivate)
	if st0 != nil {
		st0.UpdateSettings(func(s *Settings) { s.H = height })
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
