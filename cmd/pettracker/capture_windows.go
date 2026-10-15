//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Minimal Npcap binding: wpcap.dll is loaded at runtime, no cgo needed.

var (
	wpcap            *syscall.DLL
	pFindAllDevs     *syscall.Proc
	pFreeAllDevs     *syscall.Proc
	pOpenLive        *syscall.Proc
	pNextEx          *syscall.Proc
	pDatalink        *syscall.Proc
	pCompile         *syscall.Proc
	pSetFilter       *syscall.Proc
	pGetErr          *syscall.Proc
	pLibVersion      *syscall.Proc
	loadOnce         sync.Once
	loadErr          error
)

func loadNpcap() error {
	loadOnce.Do(func() {
		k32 := syscall.NewLazyDLL("kernel32.dll")
		sysDir := os.Getenv("SystemRoot")
		if sysDir == "" {
			sysDir = `C:\Windows`
		}
		npcapDir := filepath.Join(sysDir, "System32", "Npcap")
		if p, err := syscall.UTF16PtrFromString(npcapDir); err == nil {
			_, _, _ = k32.NewProc("SetDllDirectoryW").Call(uintptr(unsafe.Pointer(p)))
		}
		wpcap, loadErr = syscall.LoadDLL(filepath.Join(npcapDir, "wpcap.dll"))
		if loadErr != nil {
			wpcap, loadErr = syscall.LoadDLL("wpcap.dll")
		}
		if loadErr != nil {
			loadErr = fmt.Errorf("Npcap not found (wpcap.dll): %v", loadErr)
			return
		}
		get := func(n string) *syscall.Proc {
			p, err := wpcap.FindProc(n)
			if err != nil && loadErr == nil {
				loadErr = err
			}
			return p
		}
		pFindAllDevs = get("pcap_findalldevs")
		pFreeAllDevs = get("pcap_freealldevs")
		pOpenLive = get("pcap_open_live")
		pNextEx = get("pcap_next_ex")
		pDatalink = get("pcap_datalink")
		pCompile = get("pcap_compile")
		pSetFilter = get("pcap_setfilter")
		pGetErr = get("pcap_geterr")
		pLibVersion = get("pcap_lib_version")
	})
	return loadErr
}

func cstr(p uintptr) string {
	if p == 0 {
		return ""
	}
	var sb strings.Builder
	for i := 0; i < 4096; i++ {
		c := *(*byte)(unsafe.Pointer(p + uintptr(i)))
		if c == 0 {
			break
		}
		sb.WriteByte(c)
	}
	return sb.String()
}

// pcap_if_t: next, name, description, addresses (pointers), flags (u32)
type pcapIf struct {
	Next        uintptr
	Name        uintptr
	Description uintptr
	Addresses   uintptr
	Flags       uint32
}

type device struct{ name, desc string }

func findDevices() ([]device, error) {
	if err := loadNpcap(); err != nil {
		return nil, err
	}
	var head uintptr
	errbuf := make([]byte, 512)
	r, _, _ := pFindAllDevs.Call(uintptr(unsafe.Pointer(&head)), uintptr(unsafe.Pointer(&errbuf[0])))
	if int32(r) != 0 {
		return nil, fmt.Errorf("pcap_findalldevs: %s", cstr(uintptr(unsafe.Pointer(&errbuf[0]))))
	}
	defer pFreeAllDevs.Call(head)
	var out []device
	for p := head; p != 0; {
		d := (*pcapIf)(unsafe.Pointer(p))
		out = append(out, device{cstr(d.Name), cstr(d.Description)})
		p = d.Next
	}
	return out, nil
}

func listDevices() error {
	devs, err := findDevices()
	if err != nil {
		return err
	}
	v, _, _ := pLibVersion.Call()
	fmt.Println(cstr(v))
	for _, d := range devs {
		fmt.Printf("%s\n    %s\n", d.desc, d.name)
	}
	return nil
}

// bpf_program: u32 len (+pad), pointer
type bpfProgram struct {
	Len   uint32
	_     uint32
	Insns uintptr
}

func StartCapture(st *State) error {
	devs, err := findDevices()
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		return errors.New("no network adapters visible to Npcap (is Npcap installed and running?)")
	}
	tr := NewTracker(st)
	opened := 0
	var wg sync.WaitGroup
	for _, d := range devs {
		if strings.Contains(strings.ToLower(d.desc), "loopback") {
			continue
		}
		name, _ := syscall.BytePtrFromString(d.name)
		errbuf := make([]byte, 512)
		h, _, _ := pOpenLive.Call(uintptr(unsafe.Pointer(name)), 65535, 0, 200, uintptr(unsafe.Pointer(&errbuf[0])))
		if h == 0 {
			logf("skip adapter %s: %s", d.desc, cstr(uintptr(unsafe.Pointer(&errbuf[0]))))
			continue
		}
		filter, _ := syscall.BytePtrFromString("tcp")
		var prog bpfProgram
		if r, _, _ := pCompile.Call(h, uintptr(unsafe.Pointer(&prog)), uintptr(unsafe.Pointer(filter)), 1, 0xffffffff); int32(r) == 0 {
			pSetFilter.Call(h, uintptr(unsafe.Pointer(&prog)))
		}
		link, _, _ := pDatalink.Call(h)
		opened++
		logf("listening on %s (link %d)", d.desc, int(link))
		wg.Add(1)
		go func(h uintptr, link int, desc string) {
			defer wg.Done()
			loop(h, link, desc, tr)
		}(h, int(link), d.desc)
	}
	if opened == 0 {
		return errors.New("could not open any network adapter with Npcap (try running as administrator)")
	}
	go func() {
		for range time.Tick(30 * time.Second) {
			tr.mu.Lock()
			logf("stats: %d tcp packets, %d game stream(s)", tr.pkts, tr.games)
			tr.mu.Unlock()
		}
	}()
	wg.Wait()
	return errors.New("all capture loops stopped")
}

// pcap_pkthdr on Windows: struct timeval {long sec; long usec} (32-bit longs), u32 caplen, u32 len
type pktHdr struct {
	Sec    int32
	Usec   int32
	Caplen uint32
	Len    uint32
}

func loop(h uintptr, link int, desc string, tr *Tracker) {
	var hdr uintptr
	var data uintptr
	for {
		r, _, _ := pNextEx.Call(h, uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data)))
		switch int32(r) {
		case 1:
			ph := (*pktHdr)(unsafe.Pointer(hdr))
			n := int(ph.Caplen)
			if n <= 0 || n > 262144 {
				continue
			}
			pkt := unsafe.Slice((*byte)(unsafe.Pointer(data)), n)
			buf := make([]byte, n)
			copy(buf, pkt)
			tr.Packet(link, buf, time.Unix(int64(ph.Sec), int64(ph.Usec)*1000))
		case 0:
			continue
		default:
			e, _, _ := pGetErr.Call(h)
			logf("capture on %s stopped: %s", desc, cstr(e))
			return
		}
	}
}
