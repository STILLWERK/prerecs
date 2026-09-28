//go:build windows

package main

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// kernel32.dll is a KnownDLL and always resolves from System32, so loading it
// through the normal search order is safe.
var kernel32 = syscall.NewLazyDLL("kernel32.dll")

var (
	loadLibraryExW = kernel32.NewProc("LoadLibraryExW")
	getProcAddress = kernel32.NewProc("GetProcAddress")
)

// systemDLL is a minimal stand-in for x/sys/windows.NewLazySystemDLL: the
// stdlib offers only NewLazyDLL, which resolves through the full DLL search
// order. avifil32 is not a KnownDLL, so that order would pick up a payload
// avifil32.dll planted beside the executable or in the working directory.
// Loading with LOAD_LIBRARY_SEARCH_SYSTEM32 restricts resolution to System32.
type systemDLL struct {
	name string
	once sync.Once
	h    uintptr
	err  error
}

func (d *systemDLL) load() (uintptr, error) {
	d.once.Do(func() {
		name, err := syscall.UTF16PtrFromString(d.name)
		if err != nil {
			d.err = err
			return
		}
		const loadLibrarySearchSystem32 = 0x00000800
		h, _, e := loadLibraryExW.Call(uintptr(unsafe.Pointer(name)), 0, loadLibrarySearchSystem32)
		if h == 0 {
			if e != nil && e != syscall.Errno(0) {
				d.err = e
			} else {
				d.err = fmt.Errorf("LoadLibraryExW failed to load %s", d.name)
			}
			return
		}
		d.h = h
	})
	return d.h, d.err
}

func (d *systemDLL) NewProc(name string) *systemProc { return &systemProc{dll: d, name: name} }

type systemProc struct {
	dll  *systemDLL
	name string
	once sync.Once
	addr uintptr
	err  error
}

// Call mirrors syscall.LazyProc.Call so the VfW call sites are unchanged.
func (p *systemProc) Call(a ...uintptr) (r1, r2 uintptr, lastErr error) {
	p.once.Do(func() {
		h, err := p.dll.load()
		if err != nil {
			p.err = err
			return
		}
		cname, err := syscall.BytePtrFromString(p.name)
		if err != nil {
			p.err = err
			return
		}
		addr, _, _ := getProcAddress.Call(h, uintptr(unsafe.Pointer(cname)))
		if addr == 0 {
			p.err = fmt.Errorf("%s!%s not found", p.dll.name, p.name)
			return
		}
		p.addr = addr
	})
	if p.err != nil {
		return 0, 0, p.err
	}
	return syscall.SyscallN(p.addr, a...)
}

var (
	aviDLL                     = &systemDLL{name: "avifil32.dll"}
	procAVIFileInit            = aviDLL.NewProc("AVIFileInit")
	procAVIFileExit            = aviDLL.NewProc("AVIFileExit")
	procAVIFileOpenW           = aviDLL.NewProc("AVIFileOpenW")
	procAVIFileGetStream       = aviDLL.NewProc("AVIFileGetStream")
	procAVIStreamGetFrameOpen  = aviDLL.NewProc("AVIStreamGetFrameOpen")
	procAVIStreamGetFrame      = aviDLL.NewProc("AVIStreamGetFrame")
	procAVIStreamGetFrameClose = aviDLL.NewProc("AVIStreamGetFrameClose")
	procAVIStreamRelease       = aviDLL.NewProc("AVIStreamRelease")
	procAVIFileRelease         = aviDLL.NewProc("AVIFileRelease")
)

const streamTypeVideo = uintptr(0x73646976) // 'vids'

// vfwProbeTimeout bounds the synchronous VfW frame probe. A wedged legacy
// codec DLL is exactly the failure this preflight exists to catch — without a
// bound, a blocked AVIStreamGetFrame would hang the worker goroutine forever
// and swallow Ctrl+C for the whole batch. On timeout the caller treats the
// probe as "cannot decode" and routes to FFmpeg libxvid; the leaked probe
// goroutine stays parked on the wedged call without harming the process.
var vfwProbeTimeout = 30 * time.Second

func vfwCanDecodeFrame(path string, frame int64) (bool, error) {
	type result struct {
		ok  bool
		err error
	}
	ch := make(chan result, 1)
	go func() {
		ok, err := vfwCanDecodeFrameSync(path, frame)
		ch <- result{ok, err}
	}()
	select {
	case r := <-ch:
		return r.ok, r.err
	case <-time.After(vfwProbeTimeout):
		return false, errors.New("VfW frame probe timed out; using FFmpeg libxvid")
	}
}

// vfwCanDecodeFrameSync asks the same legacy VfW/AVIFile stack used by
// xvid_encraw whether a specific frame can actually be retrieved. Some
// OpenDML/Lagarith AVIs advertise their full frame count but AVIStreamGetFrame
// cannot reach the tail of the file; native Xvid then exits early with fewer
// frames. A cheap last-frame probe lets PreRecs route those files directly to
// FFmpeg libxvid.
func vfwCanDecodeFrameSync(path string, frame int64) (bool, error) {
	if frame < 0 {
		return false, fmt.Errorf("invalid frame index %d", frame)
	}
	if _, err := aviDLL.load(); err != nil {
		// The Call sites discard lastErr; without this a missing/blocked
		// avifil32 would surface as "AVIFileOpenW failed: 0x00000000".
		return false, err
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	procAVIFileInit.Call()
	defer procAVIFileExit.Call()

	var file uintptr
	r, _, _ := procAVIFileOpenW.Call(uintptr(unsafe.Pointer(&file)), uintptr(unsafe.Pointer(p)), 0, 0)
	if int32(r) != 0 || file == 0 {
		return false, fmt.Errorf("AVIFileOpenW failed: 0x%08x", uint32(r))
	}
	defer procAVIFileRelease.Call(file)

	var stream uintptr
	r, _, _ = procAVIFileGetStream.Call(file, uintptr(unsafe.Pointer(&stream)), streamTypeVideo, 0)
	if int32(r) != 0 || stream == 0 {
		return false, fmt.Errorf("AVIFileGetStream failed: 0x%08x", uint32(r))
	}
	defer procAVIStreamRelease.Call(stream)

	gf, _, _ := procAVIStreamGetFrameOpen.Call(stream, 0)
	if gf == 0 || gf == ^uintptr(0) {
		return false, fmt.Errorf("AVIStreamGetFrameOpen failed")
	}
	defer procAVIStreamGetFrameClose.Call(gf)

	framePtr, _, _ := procAVIStreamGetFrame.Call(gf, uintptr(frame))
	return framePtr != 0, nil
}
