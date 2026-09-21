package main

import (
	"math"
	"math/big"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRatTiming(t *testing.T) {
	r, err := parseRat("0.1")
	if err != nil || r.Cmp(big.NewRat(1, 10)) != 0 {
		t.Fatalf("parse 0.1: %v %v", r, err)
	}
	src := big.NewRat(30, 1)
	target := new(big.Rat).Quo(src, r)
	if target.Cmp(big.NewRat(300, 1)) != 0 {
		t.Fatalf("target %s", target.RatString())
	}
}

func TestConformFiltersPreserveEveryFrame(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["ffv1"] {
		t.Skip("ffv1 unavailable")
	}
	td := t.TempDir()
	src := filepath.Join(td, "source.mkv")
	out := filepath.Join(td, "conformed.mkv")
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30",
		"-frames:v", "30", "-c:v", "ffv1", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, b)
	}
	filter := strings.Join(conformVideoFilters(big.NewRat(300, 1)), ",")
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y", "-i", src,
		"-map", "0:v:0", "-vf", filter,
		"-fps_mode", "passthrough", "-enc_time_base", "filter",
		"-c:v", "ffv1", out,
	).CombinedOutput(); err != nil {
		t.Fatalf("conform: %v %s", err, b)
	}

	outInfo, err := probeMedia(caps.FFprobe, out, true)
	if err != nil {
		t.Fatal(err)
	}
	if outInfo.FrameCount != 30 {
		t.Fatalf("frames=%d", outInfo.FrameCount)
	}
	if math.Abs(outInfo.FPSFloat-300) > 0.001 {
		t.Fatalf("fps=%f", outInfo.FPSFloat)
	}
	if math.Abs(outInfo.Duration-0.1) > 0.002 {
		t.Fatalf("duration=%f", outInfo.Duration)
	}

	frameHashes := func(path string) []string {
		cmd := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-map", "0:v:0", "-f", "framehash", "-")
		b, err := cmd.Output()
		if err != nil {
			t.Fatalf("framehash %s: %v", path, err)
		}
		var hashes []string
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.Split(line, ",")
			hashes = append(hashes, strings.TrimSpace(parts[len(parts)-1]))
		}
		return hashes
	}
	a := frameHashes(src)
	b := frameHashes(out)
	if len(a) != 30 || len(b) != 30 {
		t.Fatalf("hash counts %d/%d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("frame %d changed: %s != %s", i, a[i], b[i])
		}
	}
}

func TestCollisionVerificationUsesTargetFPS(t *testing.T) {
	info := MediaInfo{FPS: "30/1", FPSFloat: 30, FrameCount: 523, FrameCountExact: true, Duration: 17.433333}
	_, target, dur, err := expectedTiming(info, ConvertOptions{Conform: true, CaptureFPS: "30", Timescale: "0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if target == nil || math.Abs(ratFloat(target)-300) > 1e-9 {
		t.Fatalf("target=%v", target)
	}
	if math.Abs(dur-float64(523)/300) > 1e-6 {
		t.Fatalf("duration=%f", dur)
	}
	out := MediaInfo{Codec: "mpeg4", CodecTag: "XVID", Width: 2560, Height: 1440, PixelFormat: "yuv420p", FrameCount: 523, FPSFloat: 300, Duration: float64(523) / 300}
	in := info
	in.Width, in.Height, in.PixelFormat = 2560, 1440, "yuv420p"
	if p := verifyOutput(in, out, ConvertOptions{Preset: "xvid_compact", Conform: true, CaptureFPS: "30", Timescale: "0.1"}, target, dur); len(p) != 0 {
		t.Fatalf("valid conformed existing output should verify: %v", p)
	}
}

func TestSelectFrameRate(t *testing.T) {
	// avg_frame_rate always wins when present.
	if s, r := selectFrameRate("30/1", "25/1", 0, 0); s != "30/1" || r == nil || ratFloat(r) != 30 {
		t.Fatalf("avg preferred: %s %v", s, r)
	}
	// Uncorroborated fallback: no frame count or duration to check against
	// (elementary streams, bare containers) — r_frame_rate is the only rate.
	if s, r := selectFrameRate("0/0", "30/1", 0, 0); s != "30/1" || r == nil || ratFloat(r) != 30 {
		t.Fatalf("r_frame_rate fallback: %s %v", s, r)
	}
	if s, r := selectFrameRate("", "30000/1001", 0, 0); s != "30000/1001" || r == nil {
		t.Fatalf("missing avg: %s %v", s, r)
	}
	if s, r := selectFrameRate("0/0", "0/0", 0, 0); s != "" || r != nil {
		t.Fatalf("both unknown: %s %v", s, r)
	}
	if s, r := selectFrameRate("junk", "30/1", 0, 0); s != "30/1" || r == nil {
		t.Fatalf("bad avg falls back: %s %v", s, r)
	}
	// Corroborated fallback: frames/duration agree with r_frame_rate.
	if s, r := selectFrameRate("0/0", "30/1", 300, 10.0); s != "30/1" || r == nil {
		t.Fatalf("consistent metadata accepts fallback: %s %v", s, r)
	}
	// Container rounding within ~2.5 frames still passes.
	if s, r := selectFrameRate("0/0", "30/1", 300, 10.05); s != "30/1" || r == nil {
		t.Fatalf("rounded duration accepts fallback: %s %v", s, r)
	}
	// VFR hazard: 300 frames over 20s means a true 15 fps average — a nominal
	// r_frame_rate of 30 must not be trusted to rebuild the timeline.
	if s, r := selectFrameRate("0/0", "30/1", 300, 20.0); s != "" || r != nil {
		t.Fatalf("inconsistent r_frame_rate rejected: %s %v", s, r)
	}
	// Adversarial: 295 frames over 10s is a true 29.5 fps average. A relative
	// 2% rate tolerance would accept 30 fps and silently retime 1.67%; the
	// frame-quantum duration tolerance must reject it.
	if s, r := selectFrameRate("0/0", "30/1", 295, 10.0); s != "" || r != nil {
		t.Fatalf("sub-2%% fps drift must be rejected: %s %v", s, r)
	}
	// Long clips: a relative tolerance would scale the permitted drift with
	// duration; the quantum tolerance stays constant. 1h at nominal 30 fps
	// but real 29.5 fps must still reject.
	if s, r := selectFrameRate("0/0", "30/1", 106200, 3600.0); s != "" || r != nil {
		t.Fatalf("long-clip drift must be rejected: %s %v", s, r)
	}
	// avg_frame_rate is corroborated by the same trusted evidence: a stale
	// avg inconsistent with frames/duration is impeached and the consistent
	// r_frame_rate wins instead.
	if s, r := selectFrameRate("60/1", "30/1", 300, 10.0); s != "30/1" || r == nil {
		t.Fatalf("stale avg must fall back to consistent r: %s %v", s, r)
	}
	// Both rates impeached by trusted count/duration → no rate at all; the
	// caller must preserve timing rather than pick a liar.
	if s, r := selectFrameRate("60/1", "30/1", 300, 20.0); s != "" || r != nil {
		t.Fatalf("both rates inconsistent must reject: %s %v", s, r)
	}
	// Honest VFR: avg reflects the real average so it stays preferred.
	if s, r := selectFrameRate("30/1", "60/1", 300, 10.0); s != "30/1" || r == nil {
		t.Fatalf("consistent avg still preferred: %s %v", s, r)
	}
}

func TestRatStringSane(t *testing.T) {
	good := []string{"30", "30000/1001", "0.1", "23.976", "1e-3", "1E2"}
	for _, s := range good {
		if !ratStringSane(s) {
			t.Fatalf("ratStringSane(%q)=false, want true", s)
		}
		if _, err := parseRat(s); err != nil {
			t.Fatalf("parseRat(%q): %v", s, err)
		}
	}
	// The short string 1e999999999 would make big.Rat.SetString materialize a
	// ~10^9-digit numerator — reject before parsing, not after.
	bad := []string{"1e999999999", "1e-999999999", "1e1001", "1e+1001", "0x1p99", strings.Repeat("9", 65), "", "abc", "1e2e3", "1e-"}
	for _, s := range bad {
		if ratStringSane(s) {
			t.Fatalf("ratStringSane(%q)=true, want false", s)
		}
		if _, err := parseRat(s); err == nil {
			t.Fatalf("parseRat(%q) accepted", s)
		}
	}
}
