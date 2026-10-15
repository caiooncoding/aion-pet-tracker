package main

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/jchv/go-webview2/pkg/edge"
	"github.com/jchv/go-webview2/webviewloader"
)

// The overlay as our own borderless window with WebView2 (the Edge engine that
// ships with Windows 10/11) inside. Owning the window means no Edge title bar to
// hide: the page is the whole window. If WebView2 isn't available, openOverlay
// falls back to an Edge --app window.

var (
	pRegisterClassExW = user32.NewProc("RegisterClassExW")
	pCreateWindowExW  = user32.NewProc("CreateWindowExW")
	pDefWindowProcW   = user32.NewProc("DefWindowProcW")
	pDestroyWindow    = user32.NewProc("DestroyWindow")
	pPostQuitMessage  = user32.NewProc("PostQuitMessage")
	pTranslateMessage = user32.NewProc("TranslateMessage")
	pDispatchMessageW = user32.NewProc("DispatchMessageW")
	pLoadCursorW      = user32.NewProc("LoadCursorW")
	pGetModuleHandleW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")
	ownWindow         bool // true when the overlay is our WebView2 window
	wv                *edge.Chromium
	wndProcCB         = syscall.NewCallback(overlayWndProc)
)

const (
	wsPopupW       = 0x80000000
	wsSysMenuW     = 0x00080000
	wsMinBoxW      = 0x00020000
	wsClipChildren = 0x02000000
	wsExTopmost    = 0x00000008
	wmDestroy      = 0x0002
	wmMove         = 0x0003
	wmSize         = 0x0005
	wmActivate     = 0x0006
	wmCloseMsg     = 0x0010
	swShowNoAct    = 4
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

func overlayWndProc(h, msg, wp, lp uintptr) uintptr {
	switch msg {
	case wmSize:
		if wv != nil {
			wv.Resize()
		}
	case wmMove:
		if wv != nil {
			_ = wv.NotifyParentWindowPositionChanged()
		}
	case wmActivate:
		if wv != nil && wp&0xFFFF != 0 {
			wv.Focus()
		}
	case wmCloseMsg:
		pDestroyWindow.Call(h)
		return 0
	case wmDestroy:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProcW.Call(h, msg, wp, lp)
	return r
}

// openNative runs the overlay window until it's closed. Returns false (without
// having shown anything) if WebView2 can't be used.
func openNative(url string, st *State) bool {
	if os.Getenv("PT_EDGE_WINDOW") != "" {
		return false
	}
	ver, err := webviewloader.GetInstalledVersion()
	if err != nil || ver == "" {
		logf("WebView2 not available (%v), using an Edge app window", err)
		return false
	}
	runtime.LockOSThread() // the window, WebView2 and the message loop share this thread

	hinst, _, _ := pGetModuleHandleW.Call(0)
	cls, _ := syscall.UTF16PtrFromString("PetTrackerOverlay")
	cur, _, _ := pLoadCursorW.Call(0, 32512) // IDC_ARROW
	wc := wndClassEx{WndProc: wndProcCB, Instance: hinst, Cursor: cur, ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	set := st.GetSettings()
	title, _ := syscall.UTF16PtrFromString(overlayTitle)
	h, _, err := pCreateWindowExW.Call(
		wsExTopmost|wsExAppWindow|wsExLayered,
		uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)),
		wsPopupW|wsSysMenuW|wsMinBoxW|wsClipChildren,
		uintptr(set.X), uintptr(set.Y), uintptr(set.W), uintptr(set.H),
		0, 0, hinst, 0)
	if h == 0 {
		logf("could not create the overlay window: %v", err)
		runtime.UnlockOSThread()
		return false
	}
	winMu.Lock()
	curWin = h
	ownWindow = true
	winMu.Unlock()
	applyExStyle(h, set) // a layered window needs its alpha before it's shown
	pref := uint32(dwmRound)
	pDwmSetWindowAttribute.Call(h, dwmCornerPref, uintptr(unsafe.Pointer(&pref)), 4)
	setWindowIdentity(h)
	pShowWindow.Call(h, swShowNoAct)

	wv = edge.NewChromium()
	wv.DataPath = filepath.Join(dataDir(), "webview")
	if !wv.Embed(h) {
		logf("WebView2 failed to start, using an Edge app window")
		wv = nil
		winMu.Lock()
		curWin, ownWindow = 0, false
		winMu.Unlock()
		pDestroyWindow.Call(h)
		runtime.UnlockOSThread()
		return false
	}
	if c2 := wv.GetController().GetICoreWebView2Controller2(); c2 != nil {
		_ = c2.PutDefaultBackgroundColor(edge.COREWEBVIEW2_COLOR{A: 255, R: 0x15, G: 0x18, B: 0x1c})
	}
	if s, err := wv.GetSettings(); err == nil {
		_ = s.PutAreDefaultContextMenusEnabled(false)
		_ = s.PutAreDevToolsEnabled(false)
		_ = s.PutIsZoomControlEnabled(false)
		_ = s.PutIsStatusBarEnabled(false)
		_ = s.PutAreBrowserAcceleratorKeysEnabled(false)
	}
	wv.Resize()
	wv.Navigate(url)
	logf("overlay window ready (WebView2 %s)", ver)

	go hotkeyLoop()
	go lockWatcher()
	go func() {
		time.Sleep(3 * time.Second)
		dumpWindow(h)
		setWindowIdentity(h)
		for {
			applyExStyle(h, st.GetSettings())
			pSetWindowPos.Call(h, hwndTopmost, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoActivate)
			time.Sleep(1500 * time.Millisecond)
		}
	}()

	var msg [48]byte
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&msg[0])))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg[0])))
	}
	logf("overlay window closed, exiting")
	st.dirty = true
	st.Save()
	os.Exit(0)
	return true
}
