//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32                     = syscall.NewLazyDLL("user32.dll")
	pEnumWindows               = user32.NewProc("EnumWindows")
	pGetWindowTextW            = user32.NewProc("GetWindowTextW")
	pIsWindowVisible           = user32.NewProc("IsWindowVisible")
	pSetWindowPos              = user32.NewProc("SetWindowPos")
	pGetWindowLongPtrW         = user32.NewProc("GetWindowLongPtrW")
	pSetWindowLongPtrW         = user32.NewProc("SetWindowLongPtrW")
	pSetLayeredWindowAttributes = user32.NewProc("SetLayeredWindowAttributes")
	pMessageBoxW               = user32.NewProc("MessageBoxW")
)

const (
	gwlExStyle     = ^uintptr(19) // -20
	wsExLayered    = 0x00080000
	wsExToolWindow = 0x00000080
	lwaAlpha       = 0x2
	hwndTopmost    = ^uintptr(0) // -1
	swpNoMove      = 0x0002
	swpNoSize      = 0x0001
	swpNoActivate  = 0x0010
	overlayTitle   = "Pet Tracker"
	overlayAlpha   = 235
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
func openOverlay(url string) {
	browser := findEdge()
	if browser == "" {
		logf("Edge/Chrome not found, opening default browser")
		_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
		return
	}
	profile := filepath.Join(dataDir(), "window")
	cmd := exec.Command(browser,
		"--app="+url,
		"--user-data-dir="+profile,
		"--window-size=340,560",
		"--window-position=40,140",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-features=Translate,msEdgeSidebarV2,msUndersideButton",
	)
	if err := cmd.Start(); err != nil {
		logf("could not open overlay window: %v", err)
		return
	}
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

func overlayExists() bool { return len(overlayWindows()) > 0 }

// keepOnTop pins the overlay above the game, and quits when the window is closed
// (Edge can keep its process alive in the background, so the window is watched too).
func keepOnTop(cmd *exec.Cmd) {
	styled := map[uintptr]bool{}
	seen, missing := false, 0
	for {
		wins := overlayWindows()
		if len(wins) > 0 {
			seen, missing = true, 0
		} else if seen {
			missing++
			if missing >= 3 {
				logf("overlay window closed, exiting")
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				os.Exit(0)
			}
		}
		for _, h := range wins {
			if !styled[h] {
				ex, _, _ := pGetWindowLongPtrW.Call(h, gwlExStyle)
				pSetWindowLongPtrW.Call(h, gwlExStyle, ex|wsExLayered)
				pSetLayeredWindowAttributes.Call(h, 0, overlayAlpha, lwaAlpha)
				styled[h] = true
				logf("overlay window pinned on top")
			}
			pSetWindowPos.Call(h, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
		}
		time.Sleep(2 * time.Second)
	}
}
