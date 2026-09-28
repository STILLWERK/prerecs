package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A native encode that fails internally hands off to libxvid in the same
// iteration; if that fallback output then fails verification, the retry loop
// must not re-enter the native path — without the nativeVerifyRejected mark the
// loop would re-run xvid_encraw forever for a persistently-unverifiable item.
func TestProcessItemNativeXvidNoRedundantRetryAfterFallback(t *testing.T) {
	e := nativeTestEngine(t)
	td := t.TempDir()
	const frames = 20
	es, _ := makeRealM4V(t, e.caps.FFmpeg, td, frames)

	nativeCalls := 0
	inner := xvidEncrawCommand
	xvidEncrawCommand = func(ctx context.Context, path string, args ...string) *exec.Cmd {
		nativeCalls++
		_ = inner // keep signature parity; the fake below replaces the process
		cmd := exec.CommandContext(ctx, os.Args[0],
			append([]string{"-test.run=^TestFakeXvidEncrawHelper$", "--"}, args...)...)
		cmd.Env = append(os.Environ(), "PRERECS_FAKE_XVID=1",
			"PRERECS_FAKE_XVID_STREAM="+es,
			"PRERECS_FAKE_XVID_VOPS=15")
		return cmd
	}
	t.Cleanup(func() { xvidEncrawCommand = inner })

	src := filepath.Join(td, "src.avi")
	if b, err := exec.Command(e.caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=30",
		"-frames:v", strconv.Itoa(frames), "-c:v", "ffv1", src).CombinedOutput(); err != nil {
		t.Fatalf("ffv1 fixture: %v %s", err, b)
	}
	info, err := probeMedia(e.caps.FFprobe, src, false)
	if err != nil {
		t.Fatal(err)
	}
	// Claim an audio track the source does not carry: every produced output
	// (native or libxvid) fails verifyOutput's track-count check, so the retry
	// loop can only exit by giving up — or by looping forever, pre-fix.
	info.Audio = []string{"aac"}
	// Force the upfront decode scan so the native encode runs with a verified
	// frameBound: the fake's underproduction then fails INSIDE runNativeXvid
	// and the inline libxvid fallback runs in the same iteration — the path the
	// nativeVerifyRejected mark exists to protect from a second native run.
	info.FrameCountExact = false

	rep, _ := captureReporter()
	item := processItem(context.Background(), theme{}, e, info,
		ConvertOptions{Preset: "xvid_compact", OutputDir: filepath.Join(td, "out")}, rep)

	if item.Status != "failed" {
		t.Fatalf("item must fail verification, got %q %q", item.Status, item.Message)
	}
	if !strings.Contains(item.Message, "audio track mismatch") {
		t.Fatalf("unexpected failure: %q", item.Message)
	}
	if nativeCalls != 1 {
		t.Fatalf("xvid_encraw ran %d times; after the inline libxvid fallback native must never retry", nativeCalls)
	}
}
