//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	aviDLL                     = syscall.NewLazyDLL("avifil32.dll")
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

// vfwCanDecodeFrame asks the same legacy VfW/AVIFile stack used by xvid_encraw
// whether a specific frame can actually be retrieved. Some OpenDML/Lagarith
// AVIs advertise their full frame count but AVIStreamGetFrame cannot reach the
// tail of the file; native Xvid then exits early with fewer frames. A cheap
// last-frame probe lets PreRecs route those files directly to FFmpeg libxvid.
func vfwCanDecodeFrame(path string, frame int64) (bool, error) {
	if frame < 0 {
		return false, fmt.Errorf("invalid frame index %d", frame)
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
