//go:build windows

package main

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"
)

// The overlay window belongs to Edge, so by default the taskbar shows Edge's
// icon and groups it with Edge. Give the window our own AppUserModelID and
// relaunch info (shell property store) plus our icon (WM_SETICON).

var (
	shell32                      = syscall.NewLazyDLL("shell32.dll")
	ole32                        = syscall.NewLazyDLL("ole32.dll")
	pSHGetPropertyStoreForWindow = shell32.NewProc("SHGetPropertyStoreForWindow")
	pExtractIconExW              = shell32.NewProc("ExtractIconExW")
	pCoInitializeEx              = ole32.NewProc("CoInitializeEx")
	pSendMessageW                = user32.NewProc("SendMessageW")
	iidPropertyStore             = guid{0x886d8eeb, 0x8cf2, 0x4446, [8]byte{0x8d, 0x02, 0xcd, 0xba, 0x1d, 0xbd, 0xcf, 0x99}}
	fmtAppUserModel              = guid{0x9F4C2855, 0x9F79, 0x4B39, [8]byte{0xA8, 0xD0, 0xE1, 0xD4, 0x2D, 0xE1, 0xD5, 0xF3}}
)

const (
	appUserModelID = "caiooncoding.AionPetTracker"
	wmSetIcon      = 0x0080
	iconSmall      = 0
	iconBig        = 1
	vtLPWStr       = 31
)

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type propertyKey struct {
	Fmt guid
	Pid uint32
}

// PROPVARIANT (24 bytes on x64): vt + 3 reserved words, then the value.
type propVariant struct {
	VT      uint16
	_, _, _ uint16
	Val     uintptr
	_       uintptr
}

type iPropertyStoreVtbl struct {
	QueryInterface, AddRef, Release, GetCount, GetAt, GetValue, SetValue, Commit uintptr
}

type iPropertyStore struct{ vtbl *iPropertyStoreVtbl }

func setWindowIdentity(h uintptr) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pCoInitializeEx.Call(0, 2) // COINIT_APARTMENTTHREADED

	exe, err := os.Executable()
	if err != nil {
		return
	}

	// icon
	exeW, _ := syscall.UTF16PtrFromString(exe)
	var big, small uintptr
	if n, _, _ := pExtractIconExW.Call(uintptr(unsafe.Pointer(exeW)), 0, uintptr(unsafe.Pointer(&big)), uintptr(unsafe.Pointer(&small)), 1); n > 0 {
		if big != 0 {
			pSendMessageW.Call(h, wmSetIcon, iconBig, big)
		}
		if small != 0 {
			pSendMessageW.Call(h, wmSetIcon, iconSmall, small)
		}
	}

	// taskbar identity
	var ps *iPropertyStore
	if r, _, _ := pSHGetPropertyStoreForWindow.Call(h, uintptr(unsafe.Pointer(&iidPropertyStore)), uintptr(unsafe.Pointer(&ps))); r != 0 || ps == nil {
		logf("taskbar: no property store (0x%x)", r)
		return
	}
	defer syscall.SyscallN(ps.vtbl.Release, uintptr(unsafe.Pointer(ps)))
	set := func(pid uint32, value string) {
		w, _ := syscall.UTF16PtrFromString(value)
		key := propertyKey{fmtAppUserModel, pid}
		pv := propVariant{VT: vtLPWStr, Val: uintptr(unsafe.Pointer(w))}
		syscall.SyscallN(ps.vtbl.SetValue, uintptr(unsafe.Pointer(ps)), uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&pv)))
		runtime.KeepAlive(w)
	}
	set(2, `"`+exe+`"`)    // RelaunchCommand
	set(3, exe+",0")       // RelaunchIconResource
	set(4, "Pet Tracker")  // RelaunchDisplayNameResource
	set(5, appUserModelID) // ID (set last: the shell picks the others up with it)
	syscall.SyscallN(ps.vtbl.Commit, uintptr(unsafe.Pointer(ps)))
	logf("taskbar icon set")
}
