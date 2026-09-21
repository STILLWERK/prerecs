package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFastProbeAndExactDecodeCount(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["ffv1"] {
		t.Skip("ffv1 unavailable")
	}
	td := t.TempDir()
	src := filepath.Join(td, "source.mkv")
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30",
		"-frames:v", "45", "-c:v", "ffv1", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, b)
	}
	info, err := probeMedia(caps.FFprobe, src, false)
	if err != nil {
		t.Fatal(err)
	}
	if info.FrameCount <= 0 {
		t.Fatal("fast probe should provide at least an estimated frame count")
	}
	e := &Engine{caps: caps, enc: enc}
	count, err := e.countDecodedFrames(context.Background(), info, func(progressInfo) {})
	if err != nil {
		t.Fatal(err)
	}
	if count != 45 {
		t.Fatalf("decoded frames=%d", count)
	}
}

func TestAnalyzeInputsTracksPartialProbeFailures(t *testing.T) {
	caps, _, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	td := t.TempDir()
	valid := filepath.Join(td, "valid.mp4")
	corrupt := filepath.Join(td, "corrupt.mp4")
	if b, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=64x64:rate=10", "-frames:v", "3", "-c:v", "mpeg4", "-an", valid).CombinedOutput(); err != nil {
		t.Fatalf("valid source: %v %s", err, b)
	}
	if err := os.WriteFile(corrupt, []byte("not a video"), 0644); err != nil {
		t.Fatal(err)
	}
	infos, failures := analyzeInputs(caps.FFprobe, []string{valid, corrupt}, theme{})
	if len(infos) != 1 || failures != 1 {
		t.Fatalf("analyzeInputs returned %d infos and %d failures", len(infos), failures)
	}
}

func TestProbeMediaContextCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake executable is not portable to Windows")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "ffprobe")
	pidFile := filepath.Join(dir, "sleep.pid")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 60 &\necho $! > '"+pidFile+"'\nwait\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			return
		}
		if p, err := os.FindProcess(pid); err == nil {
			p.Kill()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := probeMediaContext(ctx, fake, filepath.Join(dir, "in.mp4"), false)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("probe was not bounded by the context: %v", elapsed)
	}
}

func TestMetadataFrameCountTrust(t *testing.T) {
	trusted := []string{"lagarith", "magicyuv", "ffv1", "huffyuv", "utvideo", "rawvideo", "prores", "dnxhd", "cfhd"}
	for _, codec := range trusted {
		if !metadataFrameCountTrusted(codec) {
			t.Fatalf("%s should have trusted metadata frame counts", codec)
		}
	}
	untrusted := []string{"h264", "hevc", "mpeg4", "av1", "vp9"}
	for _, codec := range untrusted {
		if metadataFrameCountTrusted(codec) {
			t.Fatalf("%s metadata frame count must be treated as estimated", codec)
		}
	}
}
