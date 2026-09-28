package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProbeProResVulkanTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake executable is not portable to Windows")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "ffmpeg")
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
			_ = p.Kill()
		}
	})
	old := vulkanProbeTimeout
	vulkanProbeTimeout = 300 * time.Millisecond
	defer func() { vulkanProbeTimeout = old }()
	start := time.Now()
	if probeProResVulkan(fake) {
		t.Fatal("hung probe reported Vulkan available")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("probe was not bounded by the timeout: %v", elapsed)
	}
}

func TestCapabilityCommandOutputTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake executable is not portable to Windows")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "faketool")
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
	old := capabilityProbeTimeout
	capabilityProbeTimeout = 300 * time.Millisecond
	defer func() { capabilityProbeTimeout = old }()
	start := time.Now()
	_, err := capabilityCommandOutput(fake)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("command was not bounded by the timeout: %v", elapsed)
	}
}

// -fps_mode first exists in FFmpeg 5.1 — the floor must reject anything older
// at startup instead of failing per-command mid-batch.
func TestFFmpegVersionFloor(t *testing.T) {
	for _, tc := range []struct {
		line string
		ok   bool
	}{
		{"ffmpeg version 8.0.1-3ubuntu2", true},
		{"ffmpeg version 7.1-full_build-www.gyan.dev", true},
		{"ffmpeg version 6.1.1", true},
		{"ffmpeg version 5.1", true},
		{"ffmpeg version 5.1.2 Copyright (c)", true},
		{"ffmpeg version 5.0", false},
		{"ffmpeg version 5.0.3", false},
		{"ffmpeg version 4.4.2", false},
		{"ffmpeg version 3.4.11", false},
		// Snapshot/master builds carry no dotted version: treated as modern,
		// real failures surface if that assumption is ever wrong.
		{"ffmpeg version N-110521-gd15a1b63d0", true},
		{"ffmpeg version git-2023-11-01-abcdef", true},
		{"garbage without a version", true},
		{"", true},
	} {
		if got := ffmpegVersionOK(tc.line); got != tc.ok {
			t.Errorf("ffmpegVersionOK(%q)=%v, want %v", tc.line, got, tc.ok)
		}
	}
}

// stderr merges into CombinedOutput, so warnings can precede the version
// headline; the scan must skip noise instead of adopting it as the version.
func TestFFmpegVersionLineSkipsLeadingNoise(t *testing.T) {
	out := []byte("some driver warning on stderr\nffmpeg version 7.1-3ubuntu5\nbuilt with gcc 13\n")
	if got := ffmpegVersionLine(out); got != "ffmpeg version 7.1-3ubuntu5" {
		t.Fatalf("version line = %q", got)
	}
	if got := ffmpegVersionLine([]byte("  ffmpeg version N-110000-gabc  \nconfig: --enable-x")); got != "ffmpeg version N-110000-gabc" {
		t.Fatalf("snapshot version line = %q", got)
	}
	// No headline at all: fall back to the first line for diagnostics.
	if got := ffmpegVersionLine([]byte("total garbage\nmore")); got != "total garbage" {
		t.Fatalf("fallback = %q", got)
	}
}

// `ffmpeg -encoders` rows are `<flags> <name> <desc>`; legend lines spell
// `=` as the name and must not register as encoders, and flag-column width
// must not be position-parsed (a wider field in a future FFmpeg would
// silently empty the map if it were).
func TestParseEncoderNames(t *testing.T) {
	out := []byte(`Encoders:
 V..... = Video
 A..... = Audio
 V..... libx264              libx264 H.264 / AVC / MPEG-4 AVC
 VF.... prores_ks            Apple ProRes (iCodec Pro)
 A..... aac                  AAC (Advanced Audio Coding)
 S..... mov_text             MOV text
`)
	enc := parseEncoderNames(out)
	for _, name := range []string{"libx264", "prores_ks", "aac"} {
		if !enc[name] {
			t.Fatalf("missing encoder %q in %v", name, enc)
		}
	}
	if enc["="] || enc["mov_text"] {
		t.Fatalf("bogus entries registered: %v", enc)
	}
}

// A shim that exits 0 while a descendant still holds its pipes reports
// ErrWaitDelay; that is success, matching probeMediaContext/runFFmpeg.
func TestCapabilityCommandOutputOrphanedPipe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake executable is not portable to Windows")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "faketool")
	pidFile := filepath.Join(dir, "sleep.pid")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 30 <&1 1>&2 2>/dev/null &\necho $! > '"+pidFile+"'\necho ok\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				if p, err := os.FindProcess(pid); err == nil {
					p.Kill()
				}
			}
		}
	})
	out, err := capabilityCommandOutput(fake)
	if err != nil {
		t.Fatalf("exit-0 shim with a pipe-holding descendant must not fail: %v", err)
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("lost output: %q", out)
	}
}
