package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
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

func TestParsePathInput(t *testing.T) {
	got := parsePathInput(`"C:\A B\one.mp4" "D:\two.mov"`)
	if len(got) != 2 || got[0] != `C:\A B\one.mp4` || got[1] != `D:\two.mov` {
		t.Fatalf("got %#v", got)
	}
	got = parsePathInput(`a.mp4;b.mov`)
	if len(got) != 2 {
		t.Fatalf("semicolon parse %#v", got)
	}
}

func TestExpandDirectory(t *testing.T) {
	td := t.TempDir()
	if err := os.WriteFile(filepath.Join(td, "b.mov"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(td, "a.mp4"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(td, "ignore.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := expandInputs([]string{td})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
	if filepath.Base(got[0]) != "a.mp4" || filepath.Base(got[1]) != "b.mov" {
		t.Fatalf("sort %#v", got)
	}
}

func TestIntegrationProResConform(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["prores_ks"] {
		t.Skip("prores_ks unavailable")
	}
	td := t.TempDir()
	src := filepath.Join(td, "source 59.94.mkv")
	if out, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=60000/1001", "-frames:v", "120", "-c:v", "ffv1", src).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, out)
	}
	info, err := probeMedia(caps.FFprobe, src, true)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{caps: caps, enc: enc}
	req := ConvertOptions{Preset: "prores_422", Conform: true, CaptureFPS: "60000/1001", Timescale: "0.1"}
	dst := filepath.Join(td, "out.mov")
	args, fps, dur, err := e.buildCommand(info, req, dst)
	if err != nil {
		t.Fatal(err)
	}
	if fps == nil || math.Abs(ratFloat(fps)-599.4005994) > 0.001 {
		t.Fatalf("fps %v", fps)
	}
	if err := e.runFFmpeg(context.Background(), args, dur, func(progressInfo) {}); err != nil {
		t.Fatal(err)
	}
	outInfo, err := probeMedia(caps.FFprobe, dst, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(outInfo.Profile, "Standard") || !strings.EqualFold(outInfo.PixelFormat, "yuv422p10le") {
		t.Fatalf("ProRes 422 probe profile/pixel format=%s/%s", outInfo.Profile, outInfo.PixelFormat)
	}
	if p := verify(info, outInfo, fps, dur); len(p) != 0 {
		t.Fatalf("verify: %v", p)
	}
	if outInfo.FrameCount != 120 {
		t.Fatalf("frames=%d", outInfo.FrameCount)
	}
}

func TestIntegrationXvidCompact(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["libxvid"] {
		t.Skip("libxvid unavailable")
	}
	td := t.TempDir()
	src := filepath.Join(td, "source.mkv")
	if out, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-frames:v", "60", "-map", "0:v", "-map", "1:a", "-c:v", "ffv1", "-c:a", "pcm_s16le", src).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, out)
	}
	info, err := probeMedia(caps.FFprobe, src, true)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{caps: caps, enc: enc}
	dst := filepath.Join(td, "out.avi")
	args, fps, dur, err := e.buildCommand(info, ConvertOptions{Preset: "xvid_compact"}, dst)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.runFFmpeg(context.Background(), args, dur, func(progressInfo) {}); err != nil {
		t.Fatal(err)
	}
	outInfo, err := probeMedia(caps.FFprobe, dst, true)
	if err != nil {
		t.Fatal(err)
	}
	if p := verify(info, outInfo, fps, dur); len(p) != 0 {
		t.Fatalf("verify: %v", p)
	}
	if outInfo.FrameCount != 60 {
		t.Fatalf("frames=%d", outInfo.FrameCount)
	}
	if len(outInfo.Audio) != 1 {
		t.Fatalf("audio tracks=%d", len(outInfo.Audio))
	}
}

func TestSourceClassification(t *testing.T) {
	cases := []struct {
		in   MediaInfo
		want string
	}{
		{MediaInfo{Codec: "h264"}, "compressed"},
		{MediaInfo{Codec: "hevc"}, "compressed"},
		{MediaInfo{Codec: "mpeg4", CodecTag: "XVID"}, "compressed"},
		{MediaInfo{Codec: "lagarith"}, "lossless"},
		{MediaInfo{Codec: "magicyuv"}, "lossless"},
		{MediaInfo{Codec: "ffv1"}, "lossless"},
		{MediaInfo{Codec: "prores"}, "intermediate"},
	}
	for _, tc := range cases {
		if got := sourceClass(tc.in); got != tc.want {
			t.Fatalf("sourceClass(%+v)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestNativeXvidArgsAreFrameSafeAndFixedQuant(t *testing.T) {
	info := MediaInfo{Path: `C:\clips\master.avi`, Codec: "lagarith", Width: 2560, Height: 1440, FrameCount: 645}
	target := big.NewRat(300, 1)
	for preset, q := range map[string]string{
		"xvid_max":     "1",
		"xvid_compact": "2",
		"xvid_small":   "3",
	} {
		args := nativeXvidArgs(info, ConvertOptions{Preset: preset}, "out.avi", target)
		joined := " " + strings.Join(args, " ") + " "
		if !strings.Contains(joined, " -i C:\\clips\\master.avi -type 2 ") {
			t.Fatalf("native Xvid should use direct AVI/VFW input: %v", args)
		}
		for _, want := range []string{
			" -cq " + q + " ",
			" -imin " + q + " ",
			" -imax " + q + " ",
			" -pmin " + q + " ",
			" -pmax " + q + " ",
			" -max_bframes 0 ",
			" -max_key_interval 240 ",
			" -frames 645 ",
			" -framerate 300.000000 ",
		} {
			if !strings.Contains(joined, want) {
				t.Fatalf("%s args missing %q: %v", preset, want, args)
			}
		}
	}
}

func TestNativeXvidShareQ2Args(t *testing.T) {
	info := MediaInfo{Path: "C:/clips/master.avi", Codec: "lagarith", Width: 2560, Height: 1440, FrameCount: 645}
	args := nativeXvidArgs(info, ConvertOptions{Preset: "xvid_max_q2"}, "out.m4v", big.NewRat(300, 1))
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{
		" -cq 2 ", " -quality 6 ", " -vhqmode 4 ", " -max_bframes 2 ",
		" -bvhq ", " -bquant_ratio 100 ", " -bquant_offset 0 ", " -metric 0 ",
		" -imin 2 ", " -imax 2 ", " -pmin 2 ", " -pmax 2 ", " -bmin 2 ", " -bmax 2 ",
		" -max_key_interval 240 ", " -nopacked ", " -slices 1 ", " -frames 645 ",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Share Q2 args missing %q: %v", want, args)
		}
	}
	for _, forbidden := range []string{" -qpel ", " -gmc ", " -masking ", " -qmatrix ", " -qmatrix_intra ", " -qmatrix_inter "} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("Share Q2 unexpectedly enabled %q: %v", forbidden, args)
		}
	}
}

func TestNativeXvidEfficientQ2Args(t *testing.T) {
	info := MediaInfo{Path: "C:/clips/master.avi", Codec: "lagarith", Width: 2560, Height: 1440, FrameCount: 645}
	args := nativeXvidArgs(info, ConvertOptions{Preset: "xvid_efficient_q2"}, "out.m4v", big.NewRat(300, 1))
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{
		" -cq 2 ", " -quality 6 ", " -vhqmode 4 ", " -max_bframes 2 ",
		" -bvhq ", " -bquant_ratio 100 ", " -bquant_offset 100 ", " -metric 0 ",
		" -imin 2 ", " -imax 2 ", " -pmin 2 ", " -pmax 2 ", " -bmin 2 ", " -bmax 31 ",
		" -max_key_interval 240 ", " -nopacked ", " -slices 1 ", " -frames 645 ",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Efficient Q2 args missing %q: %v", want, args)
		}
	}
	for _, forbidden := range []string{" -qpel ", " -gmc ", " -masking ", " -qmatrix ", " -qmatrix_intra ", " -qmatrix_inter "} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("Efficient Q2 unexpectedly enabled %q: %v", forbidden, args)
		}
	}
}

func TestFFmpegXvidShareFallbackUsesFullRDB0(t *testing.T) {
	e := &Engine{enc: map[string]bool{"libxvid": true}}
	info := MediaInfo{Path: "master.mkv", Codec: "ffv1", FPS: "30/1", FPSFloat: 30, Duration: 1, FrameCount: 30, FrameCountExact: true}
	args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "xvid_max_q2", StripAudio: true}, "out.avi")
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{" -c:v libxvid ", " -qscale:v 2 ", " -mbd rd ", " -bf 0 ", " -trellis 1 ", " -me_quality 4 "} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Share fallback missing %q: %v", want, args)
		}
	}
	if strings.Contains(joined, " -mbd bits ") {
		t.Fatalf("Share fallback must use full RD: %v", args)
	}
}

func TestFFmpegXvidEfficientFallbackUsesFullRDB0(t *testing.T) {
	e := &Engine{enc: map[string]bool{"libxvid": true}}
	info := MediaInfo{Path: "master.mkv", Codec: "ffv1", FPS: "30/1", FPSFloat: 30, Duration: 1, FrameCount: 30, FrameCountExact: true}
	args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "xvid_efficient_q2", StripAudio: true}, "out.avi")
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{" -c:v libxvid ", " -qscale:v 2 ", " -mbd rd ", " -bf 0 ", " -trellis 1 ", " -me_quality 4 "} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Efficient fallback missing %q: %v", want, args)
		}
	}
	if strings.Contains(joined, " -mbd bits ") {
		t.Fatalf("Efficient fallback must use full RD: %v", args)
	}
}

func TestVerifyRejectsFrameLoss(t *testing.T) {
	in := MediaInfo{FrameCount: 90, FPSFloat: 300, Duration: 0.3}
	out := MediaInfo{FrameCount: 88, FPSFloat: 300, Duration: 0.3}
	problems := verify(in, out, big.NewRat(300, 1), 0.3)
	if len(problems) == 0 {
		t.Fatal("expected frame-loss verification failure")
	}
}

func TestSizeDeltaPercent(t *testing.T) {
	if got := sizeDeltaPercent(1000, 250); math.Abs(got-(-75)) > 1e-9 {
		t.Fatalf("got %v", got)
	}
}

func TestNativeXvidEligibility(t *testing.T) {
	if !nativeXvidEligible(MediaInfo{Path: `C:\clips\master.avi`, Codec: "lagarith"}) {
		t.Fatal("Lagarith AVI should use native Xvid")
	}
	if nativeXvidEligible(MediaInfo{Path: `C:\clips\master.mkv`, Codec: "ffv1"}) {
		t.Fatal("non-AVI source must not use native Xvid direct path")
	}
	if nativeXvidEligible(MediaInfo{Path: `C:\clips\download.avi`, Codec: "h264"}) {
		t.Fatal("distribution-compressed AVI should not use native Xvid direct path")
	}
}

func TestParseXvidProgressLine(t *testing.T) {
	line := "     321 frames( 53%) encoded,  26.10 fps, Average Bitrate = 12345kbps"
	frames, pct, fps, ok := parseXvidProgressLine(line)
	if !ok {
		t.Fatal("progress line was not parsed")
	}
	if frames != 321 || math.Abs(pct-0.53) > 1e-9 || fps != "26.10" {
		t.Fatalf("got frames=%d pct=%v fps=%q", frames, pct, fps)
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

func TestBuildCommandUsesFrameSafeConformChain(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["prores_ks"] {
		t.Skip("prores_ks unavailable")
	}
	e := &Engine{caps: caps, enc: enc}
	info := MediaInfo{Path: "input.avi", FPS: "30/1", FPSFloat: 30, Duration: 10, FrameCount: 300, FrameCountExact: true}
	args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "prores_lt", Conform: true, Timescale: "0.1"}, "out.mov")
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{
		" -vf settb=expr=1/300,setpts=N,fps=300/1 ",
		" -fps_mode passthrough ",
		" -enc_time_base filter ",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	if strings.Contains(joined, " -fps_mode cfr ") || strings.Contains(joined, " -r 300/1 ") {
		t.Fatalf("unsafe CFR/output-r conform path returned: %v", args)
	}
}

func TestEditPresetDefaultsToProResLT(t *testing.T) {
	got, err := normalizePreset("edit")
	if err != nil {
		t.Fatal(err)
	}
	if got != "prores_lt" {
		t.Fatalf("edit preset=%q", got)
	}
	got, err = normalizePreset("prores")
	if err != nil {
		t.Fatal(err)
	}
	if got != "prores_422" {
		t.Fatalf("explicit prores preset=%q", got)
	}
}

func TestNon4444ProResRejectsAlphaInputs(t *testing.T) {
	alpha := []MediaInfo{{Path: "alpha.mkv", HasAlpha: true}}
	for _, preset := range []string{"prores_lt", "prores_422", "prores_hq"} {
		if err := validatePresetInputs(preset, alpha); err == nil || !strings.Contains(err.Error(), "ProRes 4444") {
			t.Fatalf("%s accepted alpha input without a ProRes 4444 error: %v", preset, err)
		}
	}
	if err := validatePresetInputs("prores_4444", alpha); err != nil {
		t.Fatalf("ProRes 4444 rejected alpha input: %v", err)
	}

	e := &Engine{enc: map[string]bool{"prores_ks": true}}
	info := alpha[0]
	info.FPS = "30/1"
	info.FPSFloat = 30
	info.Duration = 1
	if _, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "prores_lt"}, "out.mov"); err == nil {
		t.Fatal("buildCommand silently accepted alpha input for ProRes LT")
	}
}

func TestPresetMenuMapsShareAndFastCorrectly(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()

	e := &Engine{caps: Capabilities{HasXvid: true}}
	stdinReader = bufio.NewReader(strings.NewReader("1\n"))
	got, err := choosePreset(theme{}, e, nil)
	if err != nil || got != "xvid_max_q2" {
		t.Fatalf("main SHARE menu=%q, want xvid_max_q2", got)
	}

	stdinReader = bufio.NewReader(strings.NewReader("4\n3\n"))
	got, err = choosePreset(theme{}, e, nil)
	if err != nil || got != "xvid_efficient_q2" {
		t.Fatalf("MORE Xvid Q2 Efficient=%q, want xvid_efficient_q2", got)
	}

	stdinReader = bufio.NewReader(strings.NewReader("4\n4\n"))
	got, err = choosePreset(theme{}, e, nil)
	if err != nil || got != "xvid_compact" {
		t.Fatalf("MORE Xvid Q2 Fast=%q, want xvid_compact", got)
	}
}

func TestShareAliasesUseTunedQ2AndFastKeepsLegacyCompact(t *testing.T) {
	for _, alias := range []string{"share", "compact", "xvid", "xvid-q2", "xvid-max-q2"} {
		got, err := normalizePreset(alias)
		if err != nil {
			t.Fatalf("normalizePreset(%q): %v", alias, err)
		}
		if got != "xvid_max_q2" {
			t.Fatalf("normalizePreset(%q)=%q, want xvid_max_q2", alias, got)
		}
	}
	for _, alias := range []string{"xvid-efficient", "xvid-q2-efficient", "efficient"} {
		got, err := normalizePreset(alias)
		if err != nil {
			t.Fatalf("normalizePreset(%q): %v", alias, err)
		}
		if got != "xvid_efficient_q2" {
			t.Fatalf("normalizePreset(%q)=%q, want xvid_efficient_q2", alias, got)
		}
	}
	for _, alias := range []string{"xvid-fast", "xvid-compact", "fast", "compat", "compatibility"} {
		got, err := normalizePreset(alias)
		if err != nil {
			t.Fatalf("normalizePreset(%q): %v", alias, err)
		}
		if got != "xvid_compact" {
			t.Fatalf("normalizePreset(%q)=%q, want xvid_compact", alias, got)
		}
	}
	if got := presetLabel("xvid_efficient_q2"); got != "Xvid Q2 Efficient" {
		t.Fatalf("efficient label=%q", got)
	}
	if got := presetLabel("xvid_max_q2"); got != "Share / Xvid Q2" {
		t.Fatalf("share label=%q", got)
	}
	if got := presetLabel("xvid_compact"); got != "Xvid Q2 Fast / Compatibility" {
		t.Fatalf("fast label=%q", got)
	}
}

func TestShareVerifierRequiresYUV420P(t *testing.T) {
	in := MediaInfo{Width: 1920, Height: 1080, FrameCount: 30, FPSFloat: 30, Duration: 1}
	out := MediaInfo{Width: 1920, Height: 1080, FrameCount: 30, FPSFloat: 30, Duration: 1, Codec: "mpeg4", CodecTag: "XVID", PixelFormat: "yuv444p"}
	problems := verifyOutput(in, out, ConvertOptions{Preset: "xvid_max_q2", StripAudio: true}, big.NewRat(30, 1), 1)
	found := false
	for _, problem := range problems {
		if strings.Contains(problem, "pixel format mismatch") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Share verifier accepted wrong pixel format: %v", problems)
	}
}

func TestEfficientVerifierRequiresYUV420P(t *testing.T) {
	in := MediaInfo{Width: 1920, Height: 1080, FrameCount: 30, FPSFloat: 30, Duration: 1}
	out := MediaInfo{Width: 1920, Height: 1080, FrameCount: 30, FPSFloat: 30, Duration: 1, Codec: "mpeg4", CodecTag: "XVID", PixelFormat: "yuv444p"}
	problems := verifyOutput(in, out, ConvertOptions{Preset: "xvid_efficient_q2", StripAudio: true}, big.NewRat(30, 1), 1)
	found := false
	for _, problem := range problems {
		if strings.Contains(problem, "pixel format mismatch") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Efficient verifier accepted wrong pixel format: %v", problems)
	}
}

func TestGatherInputsEOF(t *testing.T) {
	old := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader(""))
	defer func() { stdinReader = old }()
	_, err := gatherInputs(nil, theme{})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestETAWaitsForWarmup(t *testing.T) {
	if got := etaFromProgress(time.Now().Add(-10*time.Second), 0.04); got != 0 {
		t.Fatalf("ETA should be hidden below 5%%, got %v", got)
	}
	if got := etaFromProgress(time.Now().Add(-time.Second), 0.50); got != 0 {
		t.Fatalf("ETA should be hidden before 2s, got %v", got)
	}
	if got := etaFromProgress(time.Now().Add(-10*time.Second), 0.50); got <= 0 {
		t.Fatalf("ETA should be available after warmup, got %v", got)
	}
}

func TestHeadlessExitCodeOnCancellation(t *testing.T) {
	if got := headlessExitCode(BatchResult{}, context.Canceled); got != 130 {
		t.Fatalf("cancelled headless job exit code=%d, want 130", got)
	}
	if got := headlessExitCode(BatchResult{Failures: 1}, nil); got != 1 {
		t.Fatalf("failed headless job exit code=%d, want 1", got)
	}
	if got := headlessExitCode(BatchResult{Successes: 1}, nil); got != 0 {
		t.Fatalf("successful headless job exit code=%d, want 0", got)
	}
	if got := headlessExitCode(BatchResult{InputFailures: 1}, nil); got != 1 {
		t.Fatalf("partial-probe headless job exit code=%d, want 1", got)
	}
}

func TestProcessErrorStatusDistinguishesCancellation(t *testing.T) {
	if got := processErrorStatus(context.Canceled); got != "cancelled" {
		t.Fatalf("context cancellation status=%q, want cancelled", got)
	}
	if got := processErrorStatus(errors.New("verification failed")); got != "failed" {
		t.Fatalf("ordinary error status=%q, want failed", got)
	}
}

func TestConciseErrorNormalizesMultilineMessages(t *testing.T) {
	got := conciseError("first line\nsecond\tline\r\nthird line", 80)
	if got != "first line second line third line" {
		t.Fatalf("concise error=%q", got)
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

func TestDeriveBitDepthCommonPixelFormats(t *testing.T) {
	cases := map[string]int{
		"rgb24":       8,
		"bgr24":       8,
		"rgb48le":     16,
		"rgb48be":     16,
		"bgr48le":     16,
		"bgr48be":     16,
		"rgba":        8,
		"bgra":        8,
		"argb":        8,
		"abgr":        8,
		"rgba64le":    16,
		"rgba64be":    16,
		"bgra64le":    16,
		"bgra64be":    16,
		"yuv420p":     8,
		"yuv420p9le":  9,
		"yuv420p9be":  9,
		"yuv444p10le": 10,
		"yuv422p14le": 14,
		"gbrp":        8,
		"gbrp9le":     9,
		"gbrp12le":    12,
		"gbrp16le":    16,
		"gray":        8,
		"gray16le":    16,
		"ya8":         8,
		"ya16le":      16,
		"ya16be":      16,
	}
	for pixFmt, want := range cases {
		if got := deriveBitDepth(pixFmt); got != want {
			t.Errorf("deriveBitDepth(%q)=%d, want %d", pixFmt, got, want)
		}
	}
	for _, pixFmt := range []string{"mysteryfmt", "rgbunknown", "yuv420pfoo"} {
		if got := deriveBitDepth(pixFmt); got != 0 {
			t.Errorf("deriveBitDepth(%q)=%d, want unknown 0", pixFmt, got)
		}
	}
}

func TestUnknownBitDepthCannotPassLossless8BitGate(t *testing.T) {
	for _, codec := range []string{"MagicYUV", "Ut Video"} {
		if _, err := lossless8BitPixFmt(MediaInfo{PixelFormat: "mysteryfmt", BitDepth: 0, Chroma: "4:2:0"}, codec); err == nil {
			t.Fatalf("%s accepted unknown bit depth", codec)
		}
	}
}

func TestIntegrationUnreportedHighBitDepthFailsLosslessGate(t *testing.T) {
	caps, _, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	td := t.TempDir()
	src := filepath.Join(td, "rgb48.nut")
	if b, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=32x32:rate=1", "-frames:v", "1", "-pix_fmt", "rgb48le", "-c:v", "rawvideo", src).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, b)
	}
	info, err := probeMedia(caps.FFprobe, src, false)
	if err != nil {
		t.Fatal(err)
	}
	if info.BitDepth != 16 {
		t.Fatalf("rgb48le probe bit depth=%d, want 16", info.BitDepth)
	}
	for _, codec := range []string{"MagicYUV", "Ut Video"} {
		if err := validatePresetInputs(map[string]string{"MagicYUV": "magicyuv_lossless", "Ut Video": "utvideo_lossless"}[codec], []MediaInfo{info}); err == nil {
			t.Fatalf("%s gate accepted unreported high-bit-depth source", codec)
		}
	}
}

func TestXvidAvailabilityUsesSourceEligibility(t *testing.T) {
	caps := Capabilities{HasXvid: true, HasNativeXvid: true, HasLibXvid: false}
	eligible := []MediaInfo{{Path: `C:\clips\master.avi`, Codec: "lagarith"}}
	nonEligible := []MediaInfo{{Path: `C:\clips\master.mkv`, Codec: "ffv1"}}
	mixed := append(append([]MediaInfo{}, eligible...), nonEligible...)

	if ok, _ := xvidAvailableForInputs(caps, eligible, false); !ok {
		t.Fatal("native-only eligible AVI should be available without libxvid")
	}
	if ok, _ := xvidAvailableForInputs(caps, nonEligible, false); ok {
		t.Fatal("native-only capability should not be available for non-eligible input")
	}
	if ok, _ := xvidAvailableForInputs(caps, mixed, false); ok {
		t.Fatal("native-only capability should not be available for a mixed batch")
	}
}

func TestXvidRecommendationDoesNotPromiseMissingFallback(t *testing.T) {
	eligible := []MediaInfo{{Path: `C:\clips\master.avi`, Codec: "lagarith"}}
	nonEligible := []MediaInfo{{Path: `C:\clips\master.mkv`, Codec: "ffv1"}}

	nativeOnly := Capabilities{HasXvid: true, HasNativeXvid: true, HasLibXvid: false}
	if got := xvidBackendDescription(nativeOnly, eligible); strings.Contains(strings.ToLower(got), "fallback") {
		t.Fatalf("native-only recommendation promised a fallback: %q", got)
	}
	if got := xvidBackendDescription(nativeOnly, nonEligible); !strings.Contains(strings.ToLower(got), "unavailable") {
		t.Fatalf("native-only non-eligible recommendation=%q, want unavailable", got)
	}

	withFallback := Capabilities{HasXvid: true, HasNativeXvid: true, HasLibXvid: true}
	if got := xvidBackendDescription(withFallback, eligible); !strings.Contains(strings.ToLower(got), "fallback") {
		t.Fatalf("native+libxvid recommendation=%q, want fallback", got)
	}
}

func TestInteractiveNativeOnlyXvidCanSkipCompressedInputs(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	e := &Engine{
		caps: Capabilities{HasXvid: true, HasNativeXvid: true, HasLibXvid: false},
		enc:  map[string]bool{},
	}
	infos := []MediaInfo{
		{Path: `C:\clips\master.avi`, Codec: "lagarith"},
		{Path: `C:\clips\download.mp4`, Codec: "h264"},
	}
	stdinReader = bufio.NewReader(strings.NewReader("1\n1\nn\n"))
	opts, err := collectOptions(cliConfig{outputDir: t.TempDir()}, theme{}, e, infos)
	if err != nil {
		t.Fatalf("interactive native-only mixed batch was rejected before skip choice: %v", err)
	}
	if opts.Preset != "xvid_max_q2" || !opts.SkipCompressed {
		t.Fatalf("options=%+v, want SHARE with compressed input skipped", opts)
	}
}

func TestAlphaInteractivePresetStaysInMenuForProRes4444(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	e := &Engine{caps: Capabilities{HasProRes: true}}
	stdinReader = bufio.NewReader(strings.NewReader("2\n4\n7\n"))
	got, err := choosePreset(theme{}, e, []MediaInfo{{Path: "alpha.mkv", HasAlpha: true}})
	if err != nil || got != "prores_4444" {
		t.Fatalf("alpha interactive selection=%q err=%v, want ProRes 4444 after staying in menu", got, err)
	}
}

func TestAlphaRecommendationUsesProRes4444(t *testing.T) {
	if got := recommendedProResPreset([]MediaInfo{{Path: "alpha.mkv", HasAlpha: true}}); got != "prores_4444" {
		t.Fatalf("alpha recommendation=%q, want prores_4444", got)
	}
	if got := recommendedProResPreset([]MediaInfo{{Path: "master.avi"}}); got != "prores_lt" {
		t.Fatalf("normal recommendation=%q, want prores_lt", got)
	}
}

func TestCollectOptionsRejectsUnsupportedXvidInputEarly(t *testing.T) {
	e := &Engine{
		caps: Capabilities{HasXvid: true, HasNativeXvid: true, HasLibXvid: false},
		enc:  map[string]bool{},
	}
	info := MediaInfo{Path: `C:\clips\master.mkv`, Codec: "ffv1"}
	_, err := collectOptions(cliConfig{preset: "share", yes: true, forceXvid: true}, theme{}, e, []MediaInfo{info})
	if err == nil || !strings.Contains(err.Error(), "libxvid") {
		t.Fatalf("unsupported native-only Xvid input was not rejected early: %v", err)
	}
	mixed := []MediaInfo{
		{Path: `C:\clips\master.avi`, Codec: "lagarith"},
		{Path: `C:\clips\download.mp4`, Codec: "h264"},
	}
	_, err = collectOptions(cliConfig{preset: "share", yes: true, forceXvid: true}, theme{}, e, mixed)
	if err == nil || !strings.Contains(err.Error(), "libxvid") {
		t.Fatalf("force-Xvid mixed batch was not rejected early: %v", err)
	}
}

func TestHasAlphaRecognizesPackedARGBFormats(t *testing.T) {
	for _, pixFmt := range []string{"rgba", "bgra", "argb", "abgr", "yuva420p", "yuva444p", "gbrap", "ya8", "ya16le", "ya16be"} {
		if !hasAlpha(pixFmt) {
			t.Fatalf("hasAlpha(%q)=false, want true", pixFmt)
		}
	}
	for _, pixFmt := range []string{"rgb24", "bgr24", "yuv420p", "yuv444p"} {
		if hasAlpha(pixFmt) {
			t.Fatalf("hasAlpha(%q)=true, want false", pixFmt)
		}
	}
}

func TestPackedRGBFormatsUseRGBConversionClassification(t *testing.T) {
	for _, pixFmt := range []string{"argb", "abgr", "rgba", "bgra", "gbrap"} {
		if !isRGBPixelFormat(pixFmt) {
			t.Fatalf("isRGBPixelFormat(%q)=false, want true", pixFmt)
		}
		if got := chroma(pixFmt); got != "4:4:4" {
			t.Fatalf("chroma(%q)=%q, want 4:4:4", pixFmt, got)
		}
	}
}

func TestNativeXvidFrameCountConsistencyGuard(t *testing.T) {
	consistent := MediaInfo{Path: `C:\clips\master.avi`, Codec: "lagarith", FrameCount: 90, FrameCountExact: true, FPSFloat: 30, Duration: 3}
	if nativeXvidNeedsExactFrameScan(consistent) {
		t.Fatal("consistent lossless AVI metadata should keep the fast path")
	}
	suspicious := consistent
	suspicious.FrameCount = 80
	if !nativeXvidNeedsExactFrameScan(suspicious) {
		t.Fatal("duration/frame mismatch should require an exact scan")
	}
	unknownDuration := consistent
	unknownDuration.Duration = 0
	if !nativeXvidNeedsExactFrameScan(unknownDuration) {
		t.Fatal("missing duration should require an exact scan")
	}
}

func TestLossless8BitPixFmtPreservesLayout(t *testing.T) {
	cases := []struct {
		name  string
		info  MediaInfo
		codec string
		want  string
	}{
		{"yuv420", MediaInfo{PixelFormat: "yuv420p", BitDepth: 8, Chroma: "4:2:0"}, "MagicYUV", "yuv420p"},
		{"yuv422", MediaInfo{PixelFormat: "yuv422p", BitDepth: 8, Chroma: "4:2:2"}, "MagicYUV", "yuv422p"},
		{"yuv444", MediaInfo{PixelFormat: "yuv444p", BitDepth: 8, Chroma: "4:4:4"}, "MagicYUV", "yuv444p"},
		{"rgb", MediaInfo{PixelFormat: "gbrp", BitDepth: 8, Chroma: "4:4:4"}, "MagicYUV", "gbrp"},
		{"rgba", MediaInfo{PixelFormat: "gbrap", BitDepth: 8, Chroma: "4:4:4", HasAlpha: true}, "MagicYUV", "gbrap"},
		{"yuva444", MediaInfo{PixelFormat: "yuva444p", BitDepth: 8, Chroma: "4:4:4", HasAlpha: true}, "MagicYUV", "yuva444p"},
		{"ut-yuv444", MediaInfo{PixelFormat: "yuv444p", BitDepth: 8, Chroma: "4:4:4"}, "Ut Video", "yuv444p"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lossless8BitPixFmt(tc.info, tc.codec)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestLossless8BitPixFmtRejectsHigherBitDepth(t *testing.T) {
	for _, codec := range []string{"MagicYUV", "Ut Video"} {
		_, err := lossless8BitPixFmt(MediaInfo{PixelFormat: "yuv420p10le", BitDepth: 10, Chroma: "4:2:0"}, codec)
		if err == nil || !strings.Contains(err.Error(), "10-bit") {
			t.Fatalf("%s should reject 10-bit source, err=%v", codec, err)
		}
	}
}

func TestCompressedScannedSourceGetsCleanCFR(t *testing.T) {
	e := &Engine{caps: Capabilities{}, enc: map[string]bool{"prores_ks": true}}
	info := MediaInfo{
		Path:            "download.avi",
		Codec:           "h264",
		FPS:             "600/1",
		FPSFloat:        600,
		Duration:        3.328333333,
		FrameCount:      1997,
		FrameCountExact: true,
		PixelFormat:     "yuv420p",
		BitDepth:        8,
		Chroma:          "4:2:0",
	}
	args, fps, dur, err := e.buildCommand(info, ConvertOptions{Preset: "prores_lt"}, "out.mov")
	if err != nil {
		t.Fatal(err)
	}
	if fps == nil || math.Abs(ratFloat(fps)-600) > 1e-9 {
		t.Fatalf("fps=%v", fps)
	}
	wantDur := 1997.0 / 600.0
	if math.Abs(dur-wantDur) > 1e-9 {
		t.Fatalf("duration=%f want=%f", dur, wantDur)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{
		" -vf settb=expr=1/600,setpts=N,fps=600/1 ",
		" -fps_mode passthrough ",
		" -enc_time_base filter ",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
}

func TestUtVideoBuildPreservesYUV444(t *testing.T) {
	e := &Engine{caps: Capabilities{}, enc: map[string]bool{"utvideo": true}}
	info := MediaInfo{
		Path:            "master.avi",
		Codec:           "ffv1",
		FPS:             "30/1",
		FPSFloat:        30,
		Duration:        1,
		FrameCount:      30,
		FrameCountExact: true,
		PixelFormat:     "yuv444p",
		BitDepth:        8,
		Chroma:          "4:4:4",
	}
	args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "utvideo_lossless"}, "out.avi")
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{" -c:v utvideo ", " -pred left ", " -pix_fmt yuv444p "} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
}

func TestNativeXvidWritesRawStreamForLiveProgress(t *testing.T) {
	info := MediaInfo{Path: `C:\clips\master.avi`, Codec: "lagarith", Width: 2560, Height: 1440, FrameCount: 645}
	args := nativeXvidArgs(info, ConvertOptions{Preset: "xvid_compact"}, "out.m4v", big.NewRat(300, 1))
	joined := " " + strings.Join(args, " ") + " "
	if !strings.Contains(joined, " -o out.m4v ") {
		t.Fatalf("native Xvid should write an elementary stream for live progress: %v", args)
	}
	if strings.Contains(joined, " -avi ") {
		t.Fatalf("native Xvid must not use its locked/malformed AVI writer: %v", args)
	}
	if !strings.Contains(joined, " -progress 1000000 ") {
		t.Fatalf("stderr progress should be effectively disabled because VOP counting is authoritative: %v", args)
	}
}

func TestMPEG4VOPCounterAcrossPollBoundaries(t *testing.T) {
	td := t.TempDir()
	path := filepath.Join(td, "video.m4v")
	if err := os.WriteFile(path, []byte{0x12, 0x00, 0x00}, 0644); err != nil {
		t.Fatal(err)
	}
	c := &mpeg4VOPCounter{}
	if got, err := c.poll(path); err != nil || got != 0 {
		t.Fatalf("first poll got=%d err=%v", got, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	// First VOP spans the previous poll boundary. Second is fully contained here.
	if _, err := f.Write([]byte{0x01, 0xB6, 0xAA, 0x00, 0x00, 0x01, 0xB6, 0xBB, 0x00}); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if got, err := c.poll(path); err != nil || got != 2 {
		t.Fatalf("second poll got=%d err=%v", got, err)
	}
	f, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0x00, 0x01, 0xB6, 0xCC}); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if got, err := c.poll(path); err != nil || got != 3 {
		t.Fatalf("third poll got=%d err=%v", got, err)
	}
	// Polling again without new bytes must not double-count old VOPs.
	if got, err := c.poll(path); err != nil || got != 3 {
		t.Fatalf("repeat poll got=%d err=%v", got, err)
	}
}

func TestExactFrameProgressUsesFramesAndWallClockFPS(t *testing.T) {
	p := progressInfo{Percent: 1, Frame: "25", FPS: "0.00"}
	got := exactFrameProgress(p, 100, time.Now().Add(-5*time.Second))
	if math.Abs(got.Percent-0.25) > 1e-9 {
		t.Fatalf("percent=%v want 0.25", got.Percent)
	}
	if got.FPS == "" || got.FPS == "0.00" {
		t.Fatalf("expected wall-clock fps fallback, got %q", got.FPS)
	}
	if got.TotalFrames != 100 {
		t.Fatalf("total frames=%d", got.TotalFrames)
	}
}

func TestAudioModes(t *testing.T) {
	e := &Engine{enc: map[string]bool{}}
	keep := strings.Join(e.audioArgs(ConvertOptions{}), " ")
	if !strings.Contains(keep, "-c:a copy") || strings.Contains(keep, "-an") {
		t.Fatalf("normal timing should copy audio unchanged, got %q", keep)
	}
	strip := strings.Join(e.audioArgs(ConvertOptions{StripAudio: true}), " ")
	if strip != "-an" {
		t.Fatalf("strip audio = %q", strip)
	}
	conform := strings.Join(e.audioArgs(ConvertOptions{Conform: true}), " ")
	if conform != "-an" {
		t.Fatalf("conform default should strip audio, got %q", conform)
	}
}

func TestOutputCollisionFamily(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "clip.avi")
	if err := os.WriteFile(src, []byte("src"), 0644); err != nil {
		t.Fatal(err)
	}
	existing, next, err := outputCandidates(src, d, "xvid_compact")
	if err != nil || len(existing) != 0 || !strings.HasSuffix(next, "clip_xvid_compact.avi") {
		t.Fatalf("initial existing=%v next=%q err=%v", existing, next, err)
	}
	if err := os.WriteFile(next, []byte("one"), 0644); err != nil {
		t.Fatal(err)
	}
	existing, next, err = outputCandidates(src, d, "xvid_compact")
	if err != nil || len(existing) != 1 || !strings.HasSuffix(next, "clip_xvid_compact_2.avi") {
		t.Fatalf("collision existing=%v next=%q err=%v", existing, next, err)
	}
	if err := os.WriteFile(next, []byte("two"), 0644); err != nil {
		t.Fatal(err)
	}
	existing, next, err = outputCandidates(src, d, "xvid_compact")
	if err != nil || len(existing) != 2 || !strings.HasSuffix(next, "clip_xvid_compact_3.avi") {
		t.Fatalf("second collision existing=%v next=%q err=%v", existing, next, err)
	}
}

func TestSameStemDefaultOutputReservationIsAtomic(t *testing.T) {
	d := t.TempDir()
	paths := []string{filepath.Join(d, "clip.avi"), filepath.Join(d, "clip.mov")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("source"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	type result struct {
		path string
		err  error
	}
	results := make(chan result, len(paths))
	var wg sync.WaitGroup
	for _, path := range paths {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			<-start
			_, next, err := outputCandidates(path, "", "xvid_max_q2")
			results <- result{path: next, err: err}
		}(path)
	}
	close(start)
	wg.Wait()
	close(results)

	var got []result
	for result := range results {
		got = append(got, result)
	}
	if len(got) != 2 || got[0].err != nil || got[1].err != nil {
		t.Fatalf("reservations=%+v", got)
	}
	if got[0].path == got[1].path {
		t.Fatalf("same-stem sources reserved the same output: %q", got[0].path)
	}
	for _, result := range got {
		if !fileExists(result.path) {
			t.Fatalf("reservation did not hold final target %q", result.path)
		}
	}
}

func TestOutputReservationCanBeReleasedOnCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reserved.avi")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	releaseOutputReservation(path)
	if fileExists(path) {
		t.Fatal("released output reservation still exists")
	}
}

func TestEfficientOutputSuffix(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "clip.avi")
	if err := os.WriteFile(src, []byte("src"), 0644); err != nil {
		t.Fatal(err)
	}
	existing, next, err := outputCandidates(src, d, "xvid_efficient_q2")
	if err != nil || len(existing) != 0 || !strings.HasSuffix(next, "clip_xvid_efficient_q2.avi") {
		t.Fatalf("efficient output existing=%v next=%q err=%v", existing, next, err)
	}
}

func TestPresetCodecMatches(t *testing.T) {
	if !presetCodecMatches("xvid_compact", MediaInfo{Codec: "mpeg4", CodecTag: "XVID"}) {
		t.Fatal("xvid should match")
	}
	if !presetCodecMatches("xvid_efficient_q2", MediaInfo{Codec: "mpeg4", CodecTag: "XVID"}) {
		t.Fatal("efficient xvid should match")
	}
	if presetCodecMatches("xvid_compact", MediaInfo{Codec: "mpeg4", CodecTag: "FMP4"}) {
		t.Fatal("wrong mpeg4 tag must not match")
	}
	if !presetCodecMatches("prores_lt", MediaInfo{Codec: "prores", Profile: "LT", PixelFormat: "yuv422p10le"}) {
		t.Fatal("prores should match")
	}
	if presetCodecMatches("prores_lt", MediaInfo{Codec: "prores", Profile: "4444", PixelFormat: "yuv444p10le"}) {
		t.Fatal("wrong ProRes profile must not match LT")
	}
	if !presetCodecMatches("magicyuv_lossless", MediaInfo{Codec: "magicyuv"}) {
		t.Fatal("magicyuv should match")
	}
}

func TestProResPresetMatchesProfileAndPixelFormat(t *testing.T) {
	if presetCodecMatches("prores_lt", MediaInfo{Codec: "prores", Profile: "4444", PixelFormat: "yuv444p10le"}) {
		t.Fatal("ProRes LT accepted a 4444-format output")
	}
	if presetCodecMatches("prores_4444", MediaInfo{Codec: "prores", Profile: "Standard", PixelFormat: "yuv422p10le"}) {
		t.Fatal("ProRes 4444 accepted a 422-format output")
	}
	if !presetCodecMatches("prores_4444", MediaInfo{Codec: "prores", Profile: "4444", PixelFormat: "yuv444p10le"}) {
		t.Fatal("ProRes 4444 rejected a non-alpha 4444 output")
	}
	if !presetCodecMatches("prores_4444", MediaInfo{Codec: "prores", Profile: "4444", PixelFormat: "yuv444p12le"}) {
		t.Fatal("ProRes 4444 rejected FFmpeg's encoder-normalized non-alpha output")
	}
	alphaIn := MediaInfo{HasAlpha: true}
	alphaOut := MediaInfo{Codec: "prores", Profile: "4444", PixelFormat: "yuva444p10le", HasAlpha: true}
	if !proresOutputMatchesInput("prores_4444", alphaIn, alphaOut) {
		t.Fatal("ProRes 4444 rejected an alpha-capable output")
	}
	if proresOutputMatchesInput("prores_4444", MediaInfo{}, alphaOut) {
		t.Fatal("non-alpha ProRes 4444 input accepted an alpha output")
	}
}

func TestVerifyOutputAudioAndFormat(t *testing.T) {
	in := MediaInfo{
		Width: 320, Height: 180, PixelFormat: "yuv420p", FrameCount: 30, FPSFloat: 30, Duration: 1,
		Audio: []string{"aac"},
	}
	good := MediaInfo{
		Codec: "mpeg4", CodecTag: "XVID", Width: 320, Height: 180, PixelFormat: "yuv420p",
		FrameCount: 30, FPSFloat: 30, Duration: 1,
		Audio: []string{"aac"},
	}
	if p := verifyOutput(in, good, ConvertOptions{Preset: "xvid_compact"}, big.NewRat(30, 1), 1); len(p) != 0 {
		t.Fatalf("good output problems: %v", p)
	}
	noAudio := good
	noAudio.Audio = nil
	if p := verifyOutput(in, noAudio, ConvertOptions{Preset: "xvid_compact"}, big.NewRat(30, 1), 1); len(p) == 0 {
		t.Fatal("missing copied audio should fail verification")
	}
	if p := verifyOutput(in, noAudio, ConvertOptions{Preset: "xvid_compact", StripAudio: true}, big.NewRat(30, 1), 1); len(p) != 0 {
		t.Fatalf("stripped audio should verify: %v", p)
	}
	badPix := good
	badPix.PixelFormat = "yuv422p"
	if p := verifyOutput(in, badPix, ConvertOptions{Preset: "xvid_compact"}, big.NewRat(30, 1), 1); len(p) == 0 {
		t.Fatal("wrong Xvid pixel format should fail verification")
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

func TestAudioCopyCompatibility(t *testing.T) {
	for _, codec := range []string{"pcm_s16le", "pcm_s24le", "mp3", "mp2"} {
		if !audioCopyCompatible("xvid_compact", codec) {
			t.Fatalf("%s should be safe for AVI stream copy", codec)
		}
	}
	if audioCopyCompatible("xvid_compact", "aac") {
		t.Fatal("AAC should not be treated as safe for AVI stream copy")
	}
	if !audioCopyCompatible("prores_lt", "aac") {
		t.Fatal("AAC should be safe for MOV/ProRes stream copy")
	}
	infos := []MediaInfo{{Path: "clip.mp4", Audio: []string{"aac"}}}
	bad := incompatibleAudioCopies("xvid_compact", infos)
	if len(bad) != 1 || bad[0] != "clip.mp4=AAC" {
		t.Fatalf("bad=%v", bad)
	}
	if bad = incompatibleAudioCopies("prores_lt", infos); len(bad) != 0 {
		t.Fatalf("ProRes/AAC should be compatible, got %v", bad)
	}
}

func TestColorOutputArgsDropsGBRForYUVEncoders(t *testing.T) {
	in := MediaInfo{ColorSpace: "gbr", ColorRange: "pc", ColorTransfer: "bt709", ColorPrimaries: "bt709"}
	for _, preset := range []string{"xvid_compact", "xvid_max_q2", "xvid_efficient_q2", "xvid_small", "xvid_max", "prores_lt", "prores_422", "prores_hq", "prores_4444"} {
		joined := strings.Join(colorOutputArgs(in, preset), " ")
		if strings.Contains(joined, "-colorspace gbr") {
			t.Fatalf("%s propagated invalid GBR matrix to YUV encoder: %s", preset, joined)
		}
	}
	joined := " " + strings.Join(colorOutputArgs(in, "prores_4444"), " ") + " "
	if !strings.Contains(joined, " -color_range tv ") || !strings.Contains(joined, " -colorspace bt709 ") {
		t.Fatalf("RGB ProRes output must describe converted YUV planes: %s", joined)
	}
	for _, preset := range []string{"magicyuv_lossless", "utvideo_lossless"} {
		joined := strings.Join(colorOutputArgs(in, preset), " ")
		if !strings.Contains(joined, "-colorspace gbr") {
			t.Fatalf("%s should preserve GBR metadata for RGB-capable lossless output: %s", preset, joined)
		}
	}
}

func TestBuildCPUProResRGBAlphaPreservesAlphaGraph(t *testing.T) {
	e := &Engine{caps: Capabilities{}, enc: map[string]bool{"prores_ks": true}}
	info := MediaInfo{
		Path: "rgb-alpha.mkv", FPS: "30/1", FPSFloat: 30, FrameCount: 30, FrameCountExact: true, Duration: 1,
		PixelFormat: "gbrap", ColorSpace: "gbr", ColorRange: "pc", Chroma: "4:4:4", BitDepth: 8, HasAlpha: true,
	}
	args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "prores_4444"}, "out.mov")
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{
		"split=2[c][a];[c]format=gbrp,scale=out_color_matrix=bt709:out_range=tv,format=yuv444p10le[c10];[a]alphaextract,format=gray[a8];[c10][a8]alphamerge",
		" -pix_fmt yuva444p10le ", " -alpha_bits 8 ", " -color_range tv ", " -colorspace bt709 ",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}

func TestIntegrationProResRGBAlphaRoundTrip(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["prores_ks"] || !enc["ffv1"] {
		t.Skip("prores_ks/ffv1 unavailable")
	}
	td := t.TempDir()
	src := filepath.Join(td, "rgb-alpha.mkv")
	out := filepath.Join(td, "out.mov")
	// Build a non-constant alpha gradient so a conversion that modifies alpha
	// cannot accidentally pass this test because every pixel is opaque.
	filter := "[0:v]format=gbrp[base];[1:v]format=gray[a];[base][a]alphamerge,format=gbrap"
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-f", "lavfi", "-i", "nullsrc=size=160x90:rate=30,format=gray,geq=lum=X/W*255",
		"-filter_complex", filter, "-frames:v", "8", "-c:v", "ffv1", "-level", "3", "-pix_fmt", "gbrap", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, b)
	}
	info, err := probeMedia(caps.FFprobe, src, true)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{caps: caps, enc: enc}
	args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "prores_4444", StripAudio: true}, out)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(caps.FFmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("encode: %v %s", err, b)
	}
	alphaMD5 := func(path string) string {
		b, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-vf", "alphaextract,format=gray", "-f", "md5", "-").CombinedOutput()
		if err != nil {
			t.Fatalf("alpha md5 %s: %v %s", path, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	if a, b := alphaMD5(src), alphaMD5(out); a != b {
		t.Fatalf("alpha changed: src=%s out=%s", a, b)
	}
}

func TestIntegrationProResPackedRGBAlphaRoundTrip(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["prores_ks"] || !enc["ffv1"] {
		t.Skip("prores_ks/ffv1 unavailable")
	}
	td := t.TempDir()
	for _, pixFmt := range []string{"argb", "abgr"} {
		t.Run(pixFmt, func(t *testing.T) {
			src := filepath.Join(td, pixFmt+".nut")
			out := filepath.Join(td, pixFmt+".mov")
			filter := fmt.Sprintf("[0:v]format=rgb24[base];[1:v]format=gray[a];[base][a]alphamerge,format=%s", pixFmt)
			if b, err := exec.Command(caps.FFmpeg,
				"-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
				"-f", "lavfi", "-i", "nullsrc=size=160x90:rate=30,format=gray,geq=lum=X/W*255",
				"-filter_complex", filter, "-frames:v", "8", "-c:v", "rawvideo", "-pix_fmt", pixFmt, src,
			).CombinedOutput(); err != nil {
				t.Fatalf("source: %v %s", err, b)
			}
			info, err := probeMedia(caps.FFprobe, src, true)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.EqualFold(info.PixelFormat, pixFmt) || !info.HasAlpha {
				t.Fatalf("source format/alpha=%s/%t", info.PixelFormat, info.HasAlpha)
			}
			e := &Engine{caps: caps, enc: enc}
			args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "prores_4444", StripAudio: true}, out)
			if err != nil {
				t.Fatal(err)
			}
			if b, err := exec.Command(caps.FFmpeg, args...).CombinedOutput(); err != nil {
				t.Fatalf("encode: %v %s", err, b)
			}
			alphaMD5 := func(path string) string {
				b, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-vf", "alphaextract,format=gray", "-f", "md5", "-").CombinedOutput()
				if err != nil {
					t.Fatalf("alpha md5 %s: %v %s", path, err, b)
				}
				return strings.TrimSpace(string(b))
			}
			if source, output := alphaMD5(src), alphaMD5(out); source != output {
				t.Fatalf("alpha changed: source=%s output=%s", source, output)
			}
		})
	}
}

func TestIntegrationProResGrayAlphaRoundTrip(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["prores_ks"] || !enc["ffv1"] {
		t.Skip("prores_ks/ffv1 unavailable")
	}
	td := t.TempDir()
	src := filepath.Join(td, "gray-alpha.nut")
	out := filepath.Join(td, "gray-alpha.mov")
	filter := "[0:v]format=gray[base];[1:v]format=gray[a];[base][a]alphamerge,format=ya8"
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-f", "lavfi", "-i", "nullsrc=size=160x90:rate=30,format=gray,geq=lum=X/W*255",
		"-filter_complex", filter, "-frames:v", "8", "-c:v", "rawvideo", "-pix_fmt", "ya8", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, b)
	}
	info, err := probeMedia(caps.FFprobe, src, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(info.PixelFormat, "ya8") || !info.HasAlpha {
		t.Fatalf("source format/alpha=%s/%t", info.PixelFormat, info.HasAlpha)
	}
	if err := validatePresetInputs("prores_lt", []MediaInfo{info}); err == nil {
		t.Fatal("ProRes LT accepted gray+alpha input")
	}
	e := &Engine{caps: caps, enc: enc}
	args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "prores_4444", StripAudio: true}, out)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(caps.FFmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("encode: %v %s", err, b)
	}
	alphaMD5 := func(path string) string {
		b, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-vf", "alphaextract,format=gray", "-f", "md5", "-").CombinedOutput()
		if err != nil {
			t.Fatalf("alpha md5 %s: %v %s", path, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	if source, output := alphaMD5(src), alphaMD5(out); source != output {
		t.Fatalf("gray alpha changed: source=%s output=%s", source, output)
	}
}

func TestBatchWorkerCountBounds(t *testing.T) {
	infos := make([]MediaInfo, 10)
	for i := range infos {
		infos[i].Path = fmt.Sprintf("clip-%d.avi", i)
	}
	if got := batchWorkerCount(ConvertOptions{Preset: "xvid_max_q2"}, infos); got < 1 || got > 4 {
		t.Fatalf("xvid workers=%d, want 1..4", got)
	}
	if got := batchWorkerCount(ConvertOptions{Preset: "magicyuv_lossless"}, infos); got < 1 || got > 2 {
		t.Fatalf("magicyuv workers=%d, want 1..2", got)
	}
	if got := batchWorkerCount(ConvertOptions{Preset: "prores_lt"}, infos); got != 1 {
		t.Fatalf("prores workers=%d, want 1", got)
	}
	dup := []MediaInfo{{Path: "/a/one/death.avi"}, {Path: "/b/two/death.avi"}}
	if got := batchWorkerCount(ConvertOptions{Preset: "xvid_compact", OutputDir: `C:\out`}, dup); got != 1 {
		t.Fatalf("shared-folder duplicate stems must serialize, workers=%d", got)
	}
}

func TestVulkanProResEligibility(t *testing.T) {
	e := &Engine{caps: Capabilities{HasProResVulkan: true}}
	req := ConvertOptions{Preset: "prores_lt"}
	yuv := MediaInfo{PixelFormat: "yuv420p", ColorRange: "tv"}
	if !e.canUseVulkanProRes(yuv, req) {
		t.Fatal("ordinary YUV ProRes should use Vulkan when available")
	}
	req.CPUProRes = true
	if e.canUseVulkanProRes(yuv, req) {
		t.Fatal("--cpu-prores must disable Vulkan")
	}
	req.CPUProRes = false
	rgb := MediaInfo{PixelFormat: "gbrap", ColorSpace: "gbr", ColorRange: "pc"}
	if e.canUseVulkanProRes(rgb, req) {
		t.Fatal("RGB/full-range source should stay on conservative CPU path")
	}
	grayAlpha := MediaInfo{PixelFormat: "ya8", HasAlpha: true}
	if e.canUseVulkanProRes(grayAlpha, ConvertOptions{Preset: "prores_4444"}) {
		t.Fatal("gray+alpha source must stay on CPU alpha-preservation path")
	}
}

func TestBuildVulkanProResCommand(t *testing.T) {
	e := &Engine{caps: Capabilities{HasProResVulkan: true}}
	info := MediaInfo{Path: "source.avi", FPS: "30/1", FPSFloat: 30, FrameCount: 300, FrameCountExact: true, Duration: 10, PixelFormat: "yuv420p", Chroma: "4:2:0", BitDepth: 8}
	args, fps, dur, err := e.buildProResVulkanCommand(info, ConvertOptions{Preset: "prores_lt"}, "out.mov")
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{" -init_hw_device vulkan=prerecs_vk ", " -filter_hw_device prerecs_vk ", " format=yuv422p10le,hwupload ", " -c:v prores_ks_vulkan ", " -profile:v 1 ", " -async_depth 4 "} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, " -alpha_bits ") {
		t.Fatalf("alpha_bits must be omitted for non-alpha input: %s", joined)
	}
	if fps == nil || ratFloat(fps) != 30 || math.Abs(dur-10) > 1e-9 {
		t.Fatalf("timing fps=%v dur=%v", fps, dur)
	}
}

func TestBuildVulkanProResCommandAlphaBits(t *testing.T) {
	e := &Engine{caps: Capabilities{HasProResVulkan: true}}
	info := MediaInfo{Path: "alpha.avi", FPS: "30/1", FPSFloat: 30, FrameCount: 300, FrameCountExact: true, Duration: 10, PixelFormat: "yuva420p", HasAlpha: true, Chroma: "4:2:0", BitDepth: 8}
	args, _, _, err := e.buildProResVulkanCommand(info, ConvertOptions{Preset: "prores_4444"}, "out.mov")
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	if !strings.Contains(joined, " -alpha_bits 8 ") {
		t.Fatalf("alpha source must keep -alpha_bits 8: %s", joined)
	}
}

func TestIntegrationParallelXvidBatch(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["libxvid"] || !enc["ffv1"] {
		t.Skip("libxvid/ffv1 unavailable")
	}
	td := t.TempDir()
	infos := make([]MediaInfo, 0, 4)
	for i := 0; i < 4; i++ {
		src := filepath.Join(td, fmt.Sprintf("clip%d.avi", i))
		filter := fmt.Sprintf("testsrc2=size=160x90:rate=30,drawbox=x=%d:y=10:w=20:h=20:t=fill", i*10)
		if b, err := exec.Command(caps.FFmpeg,
			"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", filter,
			"-frames:v", "30", "-c:v", "ffv1", src,
		).CombinedOutput(); err != nil {
			t.Fatalf("source %d: %v %s", i, err, b)
		}
		info, err := probeMedia(caps.FFprobe, src, true)
		if err != nil {
			t.Fatal(err)
		}
		infos = append(infos, info)
	}
	e := &Engine{caps: caps, enc: enc}
	out := filepath.Join(td, "out")
	res := runBatch(context.Background(), theme{}, e, infos, ConvertOptions{Preset: "xvid_compact", OutputDir: out, StripAudio: true})
	if res.Failures != 0 || res.Successes != 4 {
		t.Fatalf("result success=%d fail=%d skip=%d items=%+v", res.Successes, res.Failures, res.Skipped, res.Items)
	}
	for _, item := range res.Items {
		if item.Status != "ok" || item.OutputInfo.FrameCount != 30 || !strings.EqualFold(item.OutputInfo.CodecTag, "XVID") {
			t.Fatalf("bad parallel item: %+v", item)
		}
	}
}

func TestProResAlphaBits(t *testing.T) {
	if got := proresAlphaBits(MediaInfo{}); got != 0 {
		t.Fatalf("no alpha bits=%d", got)
	}
	if got := proresAlphaBits(MediaInfo{HasAlpha: true, BitDepth: 8}); got != 8 {
		t.Fatalf("8-bit alpha bits=%d", got)
	}
	if got := proresAlphaBits(MediaInfo{HasAlpha: true, BitDepth: 10}); got != 16 {
		t.Fatalf("10-bit alpha bits=%d", got)
	}
}

// ---------------------------------------------------------------------------
// v1.0.2 integrity hardening regression tests
// ---------------------------------------------------------------------------

func captureReporter() (itemReporter, *[]string) {
	lines := []string{}
	return itemReporter{
		line:     func(s string) { lines = append(lines, s) },
		progress: func(progressInfo) {},
		finish:   func() {},
	}, &lines
}

func TestStrictDecodeArgsOnEncodePaths(t *testing.T) {
	e := &Engine{
		caps: Capabilities{HasProResVulkan: true},
		enc:  map[string]bool{"libxvid": true, "prores_ks": true},
	}
	info := MediaInfo{Path: "in.avi", Codec: "ffv1", FPS: "30/1", FPSFloat: 30, FrameCount: 30, FrameCountExact: true, Duration: 1, PixelFormat: "yuv420p"}
	for _, preset := range []string{"xvid_compact", "prores_lt"} {
		args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: preset, StripAudio: true}, "out.bin")
		if err != nil {
			t.Fatalf("%s: %v", preset, err)
		}
		joined := " " + strings.Join(args, " ") + " "
		if !strings.Contains(joined, " -xerror ") || !strings.Contains(joined, " -err_detect explode ") {
			t.Fatalf("%s encode args missing strict decode flags: %s", preset, joined)
		}
	}
	vargs, _, _, err := e.buildProResVulkanCommand(info, ConvertOptions{Preset: "prores_lt", StripAudio: true}, "out.mov")
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(vargs, " ") + " "
	if !strings.Contains(joined, " -xerror ") || !strings.Contains(joined, " -err_detect explode ") {
		t.Fatalf("Vulkan encode args missing strict decode flags: %s", joined)
	}
}

// TestCorruptSourceNeverVerifies is the headline v1.0.1 regression: FFmpeg can
// log decoder errors yet exit 0 with a partial frame count, so the scan used to
// accept a truncated count as truth and VERIFY then confirmed the same
// truncated output. With strict decode flags the scan must fail, no output is
// produced, and nothing reports VERIFIED.
func TestCorruptSourceNeverVerifies(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["libxvid"] {
		t.Skip("libxvid unavailable")
	}
	td := t.TempDir()
	src := filepath.Join(td, "clip.avi")
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-frames:v", "60", "-c:v", "libxvid", "-q:v", "4", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("fixture encode: %v %s", err, b)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(td, "corrupt.avi")
	broken := append([]byte(nil), data...)
	lo, hi := len(broken)*30/100, len(broken)*60/100
	for i := lo; i < hi; i++ {
		broken[i] = 0
	}
	if err := os.WriteFile(corrupt, broken, 0644); err != nil {
		t.Fatal(err)
	}

	// Demonstrate the vulnerable condition: the same file decoded without the
	// strict flags may still exit 0 while dropping frames. If a future FFmpeg
	// makes errors fatal by default this simply stops being interesting.
	nonStrict := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", corrupt, "-map", "0:v:0", "-an", "-sn", "-dn",
		"-fps_mode", "passthrough", "-f", "null", "-")
	if err := nonStrict.Run(); err == nil {
		t.Log("vulnerability condition reproduced: non-strict ffmpeg exited 0 on a corrupt stream")
	} else {
		t.Logf("non-strict ffmpeg exited nonzero on this corruption: %v", err)
	}

	info, err := probeMedia(caps.FFprobe, corrupt, false)
	if err != nil {
		t.Fatalf("ffprobe should still read the corrupt container: %v", err)
	}
	e := &Engine{caps: caps, enc: enc}
	if _, err := e.countDecodedFrames(context.Background(), info, func(progressInfo) {}); err == nil {
		t.Fatal("strict decode scan accepted a corrupt source")
	}

	outDir := filepath.Join(td, "out")
	rep, lines := captureReporter()
	item := processItem(context.Background(), theme{}, e, info, ConvertOptions{Preset: "xvid_compact", OutputDir: outDir, StripAudio: true}, rep)
	if item.Status != "failed" {
		t.Fatalf("corrupt source produced status %q, want failed", item.Status)
	}
	for _, l := range *lines {
		if strings.Contains(l, "VERIFIED") {
			t.Fatalf("VERIFIED was reported for a corrupt source; lines: %v", *lines)
		}
	}
	if item.Output != "" && fileExists(item.Output) {
		t.Fatalf("failed item left output behind: %s", item.Output)
	}
	entries, _ := os.ReadDir(outDir)
	for _, en := range entries {
		if supportedExt[strings.ToLower(filepath.Ext(en.Name()))] {
			t.Fatalf("failed conversion left media output behind: %s", en.Name())
		}
	}

	res := runBatch(context.Background(), theme{}, e, []MediaInfo{info}, ConvertOptions{Preset: "xvid_compact", OutputDir: filepath.Join(td, "out2"), StripAudio: true})
	if code := headlessExitCode(res, nil); code == 0 {
		t.Fatal("headless exit code 0 for a corrupt source")
	}
}

// TestStrictVerifyRejectsCorruptOutput proves the VERIFY stage itself is strict:
// a damaged "output" file fails countDecodedFrames instead of returning a
// truncated frame count that would match a truncated encode.
func TestStrictVerifyRejectsCorruptOutput(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["libxvid"] {
		t.Skip("libxvid unavailable")
	}
	td := t.TempDir()
	good := filepath.Join(td, "good.avi")
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-frames:v", "40", "-c:v", "libxvid", "-q:v", "4", good,
	).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, b)
	}
	data, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(td, "bad.avi")
	broken := append([]byte(nil), data...)
	lo, hi := len(broken)*40/100, len(broken)*70/100
	for i := lo; i < hi; i++ {
		broken[i] = 0xAA
	}
	if err := os.WriteFile(bad, broken, 0644); err != nil {
		t.Fatal(err)
	}
	outInfo, err := probeMedia(caps.FFprobe, bad, false)
	if err != nil {
		t.Fatalf("probe of damaged output: %v", err)
	}
	e := &Engine{caps: caps, enc: enc}
	if _, err := e.countDecodedFrames(context.Background(), outInfo, func(progressInfo) {}); err == nil {
		t.Fatal("strict verification decode accepted a damaged output")
	}
}

// ---------------------------------------------------------------------------
// Fake xvid_encraw: a subprocess helper standing in for the real binary so the
// whole runNativeXvid orchestration (poll, count, cancel, remux) runs in CI.
// ---------------------------------------------------------------------------

func vopOffsets(data []byte) []int {
	var offs []int
	for i := 0; i+3 < len(data); i++ {
		if data[i] == 0x00 && data[i+1] == 0x00 && data[i+2] == 0x01 && data[i+3] == 0xB6 {
			offs = append(offs, i)
		}
	}
	return offs
}

// TestFakeXvidEncrawHelper is not a test: it impersonates xvid_encraw when
// spawned as a child process with PRERECS_FAKE_XVID=1.
func TestFakeXvidEncrawHelper(t *testing.T) {
	if os.Getenv("PRERECS_FAKE_XVID") != "1" {
		return
	}
	// Args after "--" are the real xvid_encraw command line.
	xargs := []string{}
	for i, a := range os.Args {
		if a == "--" {
			xargs = os.Args[i+1:]
			break
		}
	}
	outPath := ""
	for i := 0; i+1 < len(xargs); i++ {
		if xargs[i] == "-o" {
			outPath = xargs[i+1]
		}
	}
	data, err := os.ReadFile(os.Getenv("PRERECS_FAKE_XVID_STREAM"))
	if err != nil || outPath == "" {
		fmt.Fprintln(os.Stderr, "fake xvid: bad invocation")
		os.Exit(64)
	}
	vops := vopOffsets(data)
	limit := len(vops)
	if n, _ := strconv.Atoi(os.Getenv("PRERECS_FAKE_XVID_VOPS")); n > 0 && n < limit {
		limit = n
	}
	delay, _ := time.ParseDuration(os.Getenv("PRERECS_FAKE_XVID_DELAY"))
	f, err := os.Create(outPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake xvid:", err)
		os.Exit(65)
	}
	if len(vops) == 0 {
		f.Write(data)
	} else {
		f.Write(data[:vops[0]])
		for i := 0; i < limit; i++ {
			end := len(data)
			if i+1 < len(vops) {
				end = vops[i+1]
			}
			if _, err := f.Write(data[vops[i]:end]); err != nil {
				os.Exit(66)
			}
			f.Sync()
			if delay > 0 {
				time.Sleep(delay)
			}
		}
	}
	f.Close()
	if code := os.Getenv("PRERECS_FAKE_XVID_EXIT"); code != "" {
		n, _ := strconv.Atoi(code)
		fmt.Fprintln(os.Stderr, "fake xvid: forced failure")
		os.Exit(n)
	}
	os.Exit(0)
}

func installFakeXvid(t *testing.T, stream string, extra map[string]string) {
	t.Helper()
	old := xvidEncrawCommand
	xvidEncrawCommand = func(ctx context.Context, path string, args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0],
			append([]string{"-test.run=^TestFakeXvidEncrawHelper$", "--"}, args...)...)
		env := append(os.Environ(), "PRERECS_FAKE_XVID=1", "PRERECS_FAKE_XVID_STREAM="+stream)
		for k, v := range extra {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
		return cmd
	}
	t.Cleanup(func() { xvidEncrawCommand = old })
}

// makeRealM4V builds a genuine MPEG-4 Part 2 elementary stream whose VOP count
// equals the requested frame count, and returns (path, vopCount).
func makeRealM4V(t *testing.T, ffmpeg, dir string, frames int) (string, int) {
	t.Helper()
	es := filepath.Join(dir, "es.m4v")
	if b, err := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=30",
		"-frames:v", strconv.Itoa(frames), "-c:v", "libxvid", "-bf", "0",
		"-f", "m4v", es,
	).CombinedOutput(); err != nil {
		t.Fatalf("m4v fixture: %v %s", err, b)
	}
	data, err := os.ReadFile(es)
	if err != nil {
		t.Fatal(err)
	}
	vops := vopOffsets(data)
	if len(vops) != frames {
		t.Fatalf("fixture has %d VOPs, want %d", len(vops), frames)
	}
	return es, frames
}

func nativeTestEngine(t *testing.T) *Engine {
	t.Helper()
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["libxvid"] {
		t.Skip("libxvid unavailable")
	}
	caps.HasNativeXvid = true
	caps.XvidEncRaw = "fake-xvid-encraw"
	return &Engine{caps: caps, enc: enc}
}

func TestNativeXvidFakeSuccess(t *testing.T) {
	e := nativeTestEngine(t)
	td := t.TempDir()
	es, frames := makeRealM4V(t, e.caps.FFmpeg, td, 20)
	installFakeXvid(t, es, nil)
	src := filepath.Join(td, "src.avi")
	info := MediaInfo{Path: src, Codec: "ffv1", FPS: "30/1", FPSFloat: 30, FrameCount: int64(frames), FrameCountExact: true, Duration: float64(frames) / 30}
	out := filepath.Join(td, "out.avi")
	target, dur, err := e.runNativeXvid(context.Background(), info, ConvertOptions{Preset: "xvid_compact", StripAudio: true}, out, func(progressInfo) {})
	if err != nil {
		t.Fatalf("native path failed: %v", err)
	}
	if target == nil || ratFloat(target) != 30 || math.Abs(dur-float64(frames)/30) > 1e-6 {
		t.Fatalf("timing fps=%v dur=%v", target, dur)
	}
	if !fileExists(out) {
		t.Fatal("remuxed AVI missing")
	}
	if fileExists(out + ".video.tmp.m4v") {
		t.Fatal("temporary elementary stream was not cleaned up")
	}
	outInfo, err := probeMedia(e.caps.FFprobe, out, true)
	if err != nil {
		t.Fatalf("probe remuxed output: %v", err)
	}
	if !strings.EqualFold(outInfo.CodecTag, "XVID") && !strings.EqualFold(outInfo.Codec, "mpeg4") {
		t.Fatalf("remuxed output is %s/%s, want mpeg4/XVID", outInfo.Codec, outInfo.CodecTag)
	}
}

func TestNativeXvidFakeWrongFrameCount(t *testing.T) {
	e := nativeTestEngine(t)
	td := t.TempDir()
	es, frames := makeRealM4V(t, e.caps.FFmpeg, td, 20)
	installFakeXvid(t, es, map[string]string{"PRERECS_FAKE_XVID_VOPS": "15"})
	info := MediaInfo{Path: filepath.Join(td, "src.avi"), Codec: "ffv1", FPS: "30/1", FPSFloat: 30, FrameCount: int64(frames), FrameCountExact: true, Duration: float64(frames) / 30}
	out := filepath.Join(td, "out.avi")
	_, _, err := e.runNativeXvid(context.Background(), info, ConvertOptions{Preset: "xvid_compact", StripAudio: true}, out, func(progressInfo) {})
	if err == nil || !strings.Contains(err.Error(), "VOP") {
		t.Fatalf("expected VOP count failure, got %v", err)
	}
	if fileExists(out) {
		t.Fatal("no AVI should exist when the frame-integrity check fails")
	}
	if fileExists(out + ".video.tmp.m4v") {
		t.Fatal("temporary stream leaked")
	}
}

func TestNativeXvidFakeExitError(t *testing.T) {
	e := nativeTestEngine(t)
	td := t.TempDir()
	es, frames := makeRealM4V(t, e.caps.FFmpeg, td, 10)
	installFakeXvid(t, es, map[string]string{"PRERECS_FAKE_XVID_EXIT": "3"})
	info := MediaInfo{Path: filepath.Join(td, "src.avi"), Codec: "ffv1", FPS: "30/1", FPSFloat: 30, FrameCount: int64(frames), FrameCountExact: true, Duration: float64(frames) / 30}
	out := filepath.Join(td, "out.avi")
	_, _, err := e.runNativeXvid(context.Background(), info, ConvertOptions{Preset: "xvid_compact", StripAudio: true}, out, func(progressInfo) {})
	if err == nil || !strings.Contains(err.Error(), "native Xvid failed") {
		t.Fatalf("expected encoder failure, got %v", err)
	}
	if fileExists(out) {
		t.Fatal("output must not exist after encoder failure")
	}
}

func TestNativeXvidFakeCancellation(t *testing.T) {
	e := nativeTestEngine(t)
	td := t.TempDir()
	es, frames := makeRealM4V(t, e.caps.FFmpeg, td, 60)
	installFakeXvid(t, es, map[string]string{"PRERECS_FAKE_XVID_DELAY": "80ms"})
	info := MediaInfo{Path: filepath.Join(td, "src.avi"), Codec: "ffv1", FPS: "30/1", FPSFloat: 30, FrameCount: int64(frames), FrameCountExact: true, Duration: float64(frames) / 30}
	out := filepath.Join(td, "out.avi")
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	_, _, err := e.runNativeXvid(ctx, info, ConvertOptions{Preset: "xvid_compact", StripAudio: true}, out, func(progressInfo) {})
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if fileExists(out + ".video.tmp.m4v") {
		t.Fatal("temporary stream leaked after cancellation")
	}
}

// ---------------------------------------------------------------------------
// Verification-failure cleanup (#3)
// ---------------------------------------------------------------------------

// The source claims an audio track the file does not actually carry, so a real
// encode succeeds but the output legitimately fails verification.
func encodeButVerifyFails(t *testing.T) (*Engine, MediaInfo) {
	t.Helper()
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["libxvid"] || !enc["ffv1"] {
		t.Skip("libxvid/ffv1 unavailable")
	}
	caps.HasNativeXvid = false
	td := t.TempDir()
	src := filepath.Join(td, "clip.avi")
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-frames:v", "20", "-c:v", "ffv1", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, b)
	}
	info, err := probeMedia(caps.FFprobe, src, true)
	if err != nil {
		t.Fatal(err)
	}
	info.Audio = []string{"aac"} // lie: the AVI has no audio stream
	return &Engine{caps: caps, enc: enc}, info
}

func TestProcessItemRemovesVerifyFailedOutput(t *testing.T) {
	e, info := encodeButVerifyFails(t)
	outDir := t.TempDir()
	rep, lines := captureReporter()
	item := processItem(context.Background(), theme{}, e, info, ConvertOptions{Preset: "xvid_compact", OutputDir: outDir}, rep)
	if item.Status != "failed" {
		t.Fatalf("status=%q, want failed", item.Status)
	}
	if !strings.Contains(item.Message, "audio") {
		t.Fatalf("expected audio mismatch message, got %q", item.Message)
	}
	if item.Output != "" && fileExists(item.Output) {
		t.Fatalf("verification-failed output left behind: %s", item.Output)
	}
	if item.Elapsed <= 0 {
		t.Fatal("elapsed time not recorded through verification")
	}
	for _, l := range *lines {
		if strings.Contains(l, "VERIFIED") {
			t.Fatalf("VERIFIED printed for failed output")
		}
	}
	entries, err := os.ReadDir(outDir)
	if err == nil {
		for _, en := range entries {
			if supportedExt[strings.ToLower(filepath.Ext(en.Name()))] {
				t.Fatalf("output dir still contains media file %s", en.Name())
			}
		}
	}
}

func TestProcessItemPreservesExistingBadCandidate(t *testing.T) {
	e, info := encodeButVerifyFails(t)
	outDir := t.TempDir()
	stale := filepath.Join(outDir, "clip_xvid_compact.avi")
	garbage := []byte("this is not a valid avi file")
	if err := os.WriteFile(stale, garbage, 0644); err != nil {
		t.Fatal(err)
	}
	rep, _ := captureReporter()
	item := processItem(context.Background(), theme{}, e, info, ConvertOptions{Preset: "xvid_compact", OutputDir: outDir}, rep)
	if item.Status != "failed" {
		t.Fatalf("status=%q, want failed (verification should fail on fake audio)", item.Status)
	}
	data, err := os.ReadFile(stale)
	if err != nil || string(data) != string(garbage) {
		t.Fatal("pre-existing invalid candidate was not preserved")
	}
	// The freshly encoded numbered copy must be gone after its verify failure.
	if fileExists(filepath.Join(outDir, "clip_xvid_compact_2.avi")) {
		t.Fatal("numbered output was not removed after verification failure")
	}
}

// ---------------------------------------------------------------------------
// Prompt EOF propagation (#4)
// ---------------------------------------------------------------------------

func TestAskLineEOF(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	stdinReader = bufio.NewReader(strings.NewReader(""))
	if _, err := askLine("X", "def"); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
	// Trailing content without a newline is still a valid answer; the NEXT read
	// reports EOF.
	stdinReader = bufio.NewReader(strings.NewReader("y"))
	v, err := askLine("X", "")
	if err != nil || v != "y" {
		t.Fatalf("last-line answer=%q err=%v", v, err)
	}
	if _, err := askLine("X", "def"); !errors.Is(err, io.EOF) {
		t.Fatalf("second read expected EOF, got %v", err)
	}
}

func TestAskChoiceEOF(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	stdinReader = bufio.NewReader(strings.NewReader(""))
	done := make(chan error, 1)
	go func() {
		_, err := askChoice("Choose", []string{"1", "2"}, "1")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expected EOF, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("askChoice looped forever on EOF")
	}
}

func TestAskYesNoEOF(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	stdinReader = bufio.NewReader(strings.NewReader(""))
	if _, err := askYesNo("OK?", true); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
	stdinReader = bufio.NewReader(strings.NewReader("n"))
	v, err := askYesNo("OK?", true)
	if err != nil || v {
		t.Fatalf("answer=%v err=%v", v, err)
	}
}

func TestChoosePresetEOF(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	e := &Engine{caps: Capabilities{HasXvid: true}}
	stdinReader = bufio.NewReader(strings.NewReader(""))
	done := make(chan error, 1)
	go func() {
		_, err := choosePreset(theme{}, e, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expected EOF, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("choosePreset looped forever on EOF")
	}
}

// ---------------------------------------------------------------------------
// Sparse numbered outputs (#5)
// ---------------------------------------------------------------------------

func TestOutputCandidatesSparse(t *testing.T) {
	td := t.TempDir()
	src := filepath.Join(td, "clip.avi")
	if err := os.WriteFile(src, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	// Sparse: base name missing, _2 and _4 exist.
	for _, n := range []string{"clip_xvid_compact_2.avi", "clip_xvid_compact_4.avi"} {
		if err := os.WriteFile(filepath.Join(out, n), []byte("old"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	existing, next, err := outputCandidates(src, out, "xvid_compact")
	if err != nil {
		t.Fatal(err)
	}
	if len(existing) != 2 || filepath.Base(existing[0]) != "clip_xvid_compact_2.avi" || filepath.Base(existing[1]) != "clip_xvid_compact_4.avi" {
		t.Fatalf("existing=%v, want [_2 _4] in order", existing)
	}
	if filepath.Base(next) != "clip_xvid_compact.avi" {
		t.Fatalf("next=%s, want base name (lowest free)", filepath.Base(next))
	}
}

func TestOutputCandidatesContiguous(t *testing.T) {
	td := t.TempDir()
	src := filepath.Join(td, "clip.avi")
	if err := os.WriteFile(src, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	for _, n := range []string{"clip_xvid_compact.avi", "clip_xvid_compact_2.avi"} {
		if err := os.WriteFile(filepath.Join(out, n), []byte("old"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	existing, next, err := outputCandidates(src, out, "xvid_compact")
	if err != nil {
		t.Fatal(err)
	}
	if len(existing) != 2 {
		t.Fatalf("existing=%v", existing)
	}
	if filepath.Base(next) != "clip_xvid_compact_3.avi" {
		t.Fatalf("next=%s, want _3", filepath.Base(next))
	}
	releaseOutputReservation(next)
	// Non-numeric or wrong-suffix names must not be treated as candidates.
	os.WriteFile(filepath.Join(out, "clip_xvid_compact_x.avi"), []byte("y"), 0644)
	os.WriteFile(filepath.Join(out, "clip_xvid_compact_7.bak"), []byte("y"), 0644)
	os.WriteFile(filepath.Join(out, "clip_xvid_compact_99.avi"), []byte("old"), 0644)
	existing, next, err = outputCandidates(src, out, "xvid_compact")
	if err != nil {
		t.Fatal(err)
	}
	if len(existing) != 3 || filepath.Base(existing[2]) != "clip_xvid_compact_99.avi" {
		t.Fatalf("existing=%v", existing)
	}
	if filepath.Base(next) != "clip_xvid_compact_3.avi" {
		t.Fatalf("next=%s, want lowest free slot _3", filepath.Base(next))
	}
}

// ---------------------------------------------------------------------------
// CLI robustness (#6, #9, #10)
// ---------------------------------------------------------------------------

func TestCollectOptionsValidatesTimingFlags(t *testing.T) {
	e := &Engine{caps: Capabilities{HasProRes: true}, enc: map[string]bool{"prores_ks": true}}
	infos := []MediaInfo{{Path: "clip.mp4", Codec: "h264"}}
	if _, err := collectOptions(cliConfig{preset: "edit", timescale: "abc", yes: true}, theme{}, e, infos); err == nil || !strings.Contains(err.Error(), "timescale") {
		t.Fatalf("bad --timescale not rejected up front: %v", err)
	}
	if _, err := collectOptions(cliConfig{preset: "edit", captureFPS: "abc", timescale: "0.1", yes: true}, theme{}, e, infos); err == nil || !strings.Contains(err.Error(), "capture-fps") {
		t.Fatalf("bad --capture-fps not rejected up front: %v", err)
	}
	if _, err := collectOptions(cliConfig{preset: "edit", timescale: "0.1", captureFPS: "30", yes: true}, theme{}, e, infos); err != nil {
		t.Fatalf("valid timing flags rejected: %v", err)
	}
	opts, err := collectOptions(cliConfig{preset: "edit", timescale: "1/10", captureFPS: "60000/1001", yes: true}, theme{}, e, infos)
	if err != nil || !opts.Conform || opts.Timescale != "1/10" {
		t.Fatalf("fractional timing flags: opts=%+v err=%v", opts, err)
	}
}

func TestMissingInputExitCode(t *testing.T) {
	if missingInputExitCode(cliConfig{yes: true}) == 0 {
		t.Fatal("--yes with no input must exit nonzero")
	}
	if missingInputExitCode(cliConfig{}) != 0 {
		t.Fatal("interactive EOF should still exit 0")
	}
}

func TestReorderArgs(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantErr  bool
		wantLast []string
		check    func(t *testing.T, c cliConfig, args []string)
	}{
		{
			name: "options first",
			in:   []string{"--preset", "share", "file.avi"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if c.preset != "share" || len(args) != 1 || args[0] != "file.avi" {
					t.Fatalf("cfg=%+v args=%v", c, args)
				}
			},
		},
		{
			name: "options after positional",
			in:   []string{"file.avi", "--preset", "share"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if c.preset != "share" || len(args) != 1 || args[0] != "file.avi" {
					t.Fatalf("cfg=%+v args=%v", c, args)
				}
			},
		},
		{
			name: "interleaved",
			in:   []string{"file1.avi", "--preset", "share", "file2.avi", "--yes"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if c.preset != "share" || !c.yes || len(args) != 2 || args[0] != "file1.avi" || args[1] != "file2.avi" {
					t.Fatalf("cfg=%+v args=%v", c, args)
				}
			},
		},
		{
			name: "dash terminator",
			in:   []string{"--", "-weird.avi"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if len(args) != 1 || args[0] != "-weird.avi" {
					t.Fatalf("cfg=%+v args=%v", c, args)
				}
			},
		},
		{
			name: "help flag is a literal path after terminator",
			in:   []string{"--", "-h"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if len(args) != 1 || args[0] != "-h" {
					t.Fatalf("cfg=%+v args=%v", c, args)
				}
			},
		},
		{
			name: "inline value",
			in:   []string{"file.avi", "--preset=share"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if c.preset != "share" {
					t.Fatalf("cfg=%+v", c)
				}
			},
		},
		{
			name: "single dash form",
			in:   []string{"file.avi", "-preset", "share", "-yes"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if c.preset != "share" || !c.yes {
					t.Fatalf("cfg=%+v", c)
				}
			},
		},
		{
			name:    "missing value",
			in:      []string{"file.avi", "--preset"},
			wantErr: true,
		},
		{
			name:    "unknown option",
			in:      []string{"file.avi", "--bogus"},
			wantErr: true,
		},
		{
			name: "bool with inline value",
			in:   []string{"file.avi", "--yes=true"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if !c.yes {
					t.Fatalf("cfg=%+v", c)
				}
			},
		},
		{
			name: "timescale and capture fps after path",
			in:   []string{"file.avi", "--timescale", "0.1", "--capture-fps", "30", "--strip-audio"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if c.timescale != "0.1" || c.captureFPS != "30" || !c.stripAudio {
					t.Fatalf("cfg=%+v", c)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, args, err := parseFlags(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got cfg=%+v args=%v", cfg, args)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, cfg, args)
		})
	}
}

// ---------------------------------------------------------------------------
// Generated-name guard (#12), codec classes (#8), frame-rate fallback (#13),
// Vulkan probe timeout (#11)
// ---------------------------------------------------------------------------

func TestIsGeneratedOutputName(t *testing.T) {
	for name, want := range map[string]bool{
		"clip_prores_lt.mov":            true,
		"clip_xvid_max_q2.avi":          true,
		"clip_xvid_max_q2_3.avi":        true,
		"clip_utvideo_lossless_10.avi":  true,
		"CLIP_PRORES_LT.MOV":            true, // extension is case-insensitive
		"clip_prores_lt_extra.mov":      false,
		"clip_xvid_compact_abc.avi":     false,
		"clip_xvid_compact_7.bak":       false,
		"myxvid_compact.avi":            false, // no underscore boundary
		"xvid_compact.avi":              false, // bare preset name is not stem_preset
		"clip.mp4":                      false,
		"clip_prores_lt.mp4":            false, // outputs are only .avi/.mov
		"vacation_prores_4444.mov":      true,  // exact generated pattern: skip
		"vacation_prores_4444_12.mov":   true,
		"holiday_magicyuv_lossless.AVI": true,
	} {
		if got := isGeneratedOutputName(name); got != want {
			t.Errorf("isGeneratedOutputName(%q)=%v, want %v", name, got, want)
		}
	}
}

func TestExpandInputsSkipsGeneratedOutputs(t *testing.T) {
	td := t.TempDir()
	for _, name := range []string{"a.mp4", "b_prores_lt.mov", "c_xvid_max_q2_2.avi", "d_xvid_max_q2x.avi", "note.txt"} {
		if err := os.WriteFile(filepath.Join(td, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := expandInputs([]string{td})
	if err != nil {
		t.Fatal(err)
	}
	var bases []string
	for _, p := range got {
		bases = append(bases, filepath.Base(p))
	}
	want := []string{"a.mp4", "d_xvid_max_q2x.avi"}
	if strings.Join(bases, ",") != strings.Join(want, ",") {
		t.Fatalf("expanded=%v, want %v", bases, want)
	}
}

func TestSourceClassDistribution(t *testing.T) {
	for codec, want := range map[string]string{
		"h264": "compressed", "hevc": "compressed", "av1": "compressed",
		"vp9": "compressed", "mpeg4": "compressed", "mpeg2video": "compressed",
		"wmv1": "compressed", "wmv2": "compressed", "wmv3": "compressed",
		"h263": "compressed", "h263p": "compressed", "flv1": "compressed",
		"theora": "compressed", "vp6": "compressed", "vp6f": "compressed",
		"vp6a": "compressed", "cinepak": "compressed", "msmpeg4v3": "compressed",
		"lagarith": "lossless", "ffv1": "lossless", "utvideo": "lossless",
		"prores": "intermediate", "dnxhd": "intermediate",
		"mjpeg":   "other", // acquisition/intermediate codec, not distribution
		"unknown": "other",
	} {
		if got := sourceClass(MediaInfo{Codec: codec}); got != want {
			t.Errorf("sourceClass(%s)=%q, want %q", codec, got, want)
		}
	}
	// Codec-tag driven detection still works for xvid/divx/dx50 mpeg4.
	if got := sourceClass(MediaInfo{Codec: "mpeg4", CodecTag: "DX50"}); got != "compressed" {
		t.Errorf("mpeg4/DX50 = %q", got)
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
}

func TestProbeProResVulkanTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake executable is not portable to Windows")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 60\n"), 0755); err != nil {
		t.Fatal(err)
	}
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

// parseFlags is exercised directly rather than a parallel help pre-scan: the
// real flag parser decides what counts as help, so these cases pin the actual
// observable outcomes — help (errShowHelp), a parse error, or a clean parse.
func TestParseFlagsHelp(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want string // "help", "err", or "ok"
	}{
		{"plain help", []string{"-h"}, "help"},
		{"long help", []string{"--help"}, "help"},
		{"help among options", []string{"--preset", "share", "--help"}, "help"},
		{"help after terminator is a path", []string{"--", "--help"}, "ok"},
		{"h after terminator is a path", []string{"--", "-h"}, "ok"},
		{"terminator consumed as option value", []string{"--output", "--", "--help", "clip.avi"}, "help"},
		{"terminator consumed as preset value", []string{"--preset", "--", "-h"}, "help"},
		{"help consumed as option value", []string{"--output", "--help", "clip.avi"}, "ok"},
		{"h consumed as preset value", []string{"--preset", "-h", "clip.avi"}, "ok"},
		{"positional named like an option", []string{"output", "--", "--help"}, "ok"},
		{"single-dash long help", []string{"-help"}, "help"},
		{"double-dash short help", []string{"--h"}, "help"},
		{"help with inline value", []string{"-h=x"}, "help"},
		{"long help with inline value", []string{"--help=x"}, "help"},
		{"triple dash is not help", []string{"---h"}, "err"},
		{"help spelling consumed as value", []string{"--timescale", "-help", "clip.avi"}, "ok"},
		{"help after parsed option", []string{"--timescale", "0.5", "-help"}, "help"},
		// Malformed option streams lose to the parser's own errors — help is
		// not claimed when the command line could never parse.
		{"unknown option before help", []string{"-bogus", "-h"}, "err"},
		{"unknown option after help", []string{"-h", "-bogus"}, "err"},
		{"bad bool value before help", []string{"-yes=bad", "-h"}, "err"},
		{"missing option value", []string{"--output"}, "err"},
		{"no help", []string{"file.avi", "--yes"}, "ok"},
		{"empty", nil, "ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseFlags(tc.in)
			got := "ok"
			if errors.Is(err, errShowHelp) {
				got = "help"
			} else if err != nil {
				got = "err"
			}
			if got != tc.want {
				t.Fatalf("parseFlags(%v) outcome=%q (err=%v), want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

// TestMainYesEOFNonzero runs the real main() in a helper process: with --yes,
// a valid source, no --preset, and stdin at EOF, the required preset selection
// cannot complete. Automation must not read "did nothing" as success — the
// process must exit nonzero.
func TestMainYesEOFNonzero(t *testing.T) {
	if os.Getenv("PRERECS_MAIN_HELPER") == "1" {
		var helperArgs []string
		if err := json.Unmarshal([]byte(os.Getenv("PRERECS_MAIN_ARGS")), &helperArgs); err != nil {
			os.Exit(70)
		}
		os.Args = append([]string{"prerecs"}, helperArgs...)
		main()
		return
	}
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["ffv1"] {
		t.Skip("ffv1 encoder unavailable")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "in.avi")
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=30",
		"-frames:v", "3", "-c:v", "ffv1", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("fixture encode: %v %s", err, b)
	}

	run := func(args ...string) int {
		payload, _ := json.Marshal(args)
		cmd := exec.Command(os.Args[0], "-test.run=^TestMainYesEOFNonzero$")
		cmd.Env = append(os.Environ(),
			"PRERECS_MAIN_HELPER=1",
			"PRERECS_MAIN_ARGS="+string(payload),
		)
		cmd.Stdin = strings.NewReader("") // immediate EOF
		err := cmd.Run()
		if err == nil {
			return 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		t.Fatalf("helper run failed: %v", err)
		return -1
	}

	if code := run("--yes"); code == 0 {
		t.Fatal("--yes with no input and EOF exited 0")
	}
	if code := run("--yes", src); code == 0 {
		t.Fatal("--yes with a file but no preset selection exited 0 on EOF")
	}
}

// ---------------------------------------------------------------------------
// Final-review regressions
// ---------------------------------------------------------------------------

func TestHeadlessExitCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  BatchResult
		want int
	}{
		{"successes", BatchResult{Successes: 1, Items: []ItemResult{{Status: "ok", Output: "o.avi"}}}, 0},
		{"failures", BatchResult{Failures: 1, Items: []ItemResult{{Status: "failed"}}}, 1},
		{"input failures", BatchResult{InputFailures: 1}, 1},
		// Every input skipped by policy (e.g. distribution-compressed under
		// --yes) produced no output at all — automation must see nonzero.
		{"all skipped nothing produced", BatchResult{Skipped: 2, Items: []ItemResult{{Status: "skipped"}, {Status: "skipped"}}}, 2},
		// A skip that resolved to an already-verified output did deliver the
		// requested end state — that is success.
		{"skip resolved to verified output", BatchResult{Skipped: 1, Items: []ItemResult{{Status: "skipped", Output: "o.avi"}}}, 0},
		{"mixed success and bare skip", BatchResult{Successes: 1, Skipped: 1, Items: []ItemResult{{Status: "ok", Output: "o.avi"}, {Status: "skipped"}}}, 0},
		{"nothing processed", BatchResult{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := headlessExitCode(tc.res, nil); got != tc.want {
				t.Fatalf("headlessExitCode=%d, want %d", got, tc.want)
			}
		})
	}
	if c := headlessExitCode(BatchResult{}, context.Canceled); c != 130 {
		t.Fatalf("cancelled exit code=%d, want 130", c)
	}
}

func TestAskLineWhitespaceThenEOF(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	// A whitespace-only fragment at EOF must not silently become the default —
	// under --yes that would convert "no answer" into an accepted choice.
	stdinReader = bufio.NewReader(strings.NewReader(" "))
	if _, err := askLine("X", "def"); !errors.Is(err, io.EOF) {
		t.Fatalf("whitespace-only EOF should surface EOF, got %v", err)
	}
	// A real token at EOF is still consumed as an answer.
	stdinReader = bufio.NewReader(strings.NewReader("  5"))
	if v, err := askLine("X", "def"); err != nil || v != "5" {
		t.Fatalf("partial line at EOF: v=%q err=%v", v, err)
	}
	// Whitespace terminated by a newline is an explicit empty answer → default.
	stdinReader = bufio.NewReader(strings.NewReader("   \n"))
	if v, err := askLine("X", "def"); err != nil || v != "def" {
		t.Fatalf("blank line should yield default: v=%q err=%v", v, err)
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

func TestHasDuplicateOutputStems(t *testing.T) {
	mk := func(p string) MediaInfo { return MediaInfo{Path: p} }
	// Same stem + same effective output dir (default converted_prerecs) → dup.
	if !hasDuplicateOutputStems("", []MediaInfo{mk("/a/clip.avi"), mk("/a/clip.mov")}) {
		t.Fatal("same-dir same-stem not detected")
	}
	// Same stem but different source dirs → different default output dirs → ok.
	if hasDuplicateOutputStems("", []MediaInfo{mk("/a/clip.avi"), mk("/b/clip.mov")}) {
		t.Fatal("different-dir same-stem falsely detected")
	}
	// A shared custom output dir makes cross-dir same-stem sources collide.
	if !hasDuplicateOutputStems("/out", []MediaInfo{mk("/a/clip.avi"), mk("/b/clip.mov")}) {
		t.Fatal("custom-outdir same-stem not detected")
	}
	// Case-insensitive stem match (Windows filesystems).
	if !hasDuplicateOutputStems("", []MediaInfo{mk("/a/Clip.avi"), mk("/a/CLIP.mkv")}) {
		t.Fatal("case-variant same-stem not detected")
	}
	if hasDuplicateOutputStems("/out", []MediaInfo{mk("/a/one.avi"), mk("/b/two.avi")}) {
		t.Fatal("distinct stems falsely detected")
	}
}

func TestExpandInputsSkipsTempStream(t *testing.T) {
	td := t.TempDir()
	for _, n := range []string{"clip.avi", "clip_xvid_compact.video.tmp.m4v", "clip_xvid_compact.avi"} {
		if err := os.WriteFile(filepath.Join(td, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := expandInputs([]string{td})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || filepath.Base(got[0]) != "clip.avi" {
		t.Fatalf("expandInputs=%v, want only clip.avi", got)
	}
}

// TestProcessItemHoldsReservationThroughFallback drives a real conversion where
// the first backend fails: the O_EXCL reservation file must still exist when
// the fallback ffmpeg starts, otherwise a concurrent same-stem run could claim
// the destination mid-switch. A wrapper around the real ffmpeg records, per
// invocation, whether the trailing output path already exists.
func TestProcessItemHoldsReservationThroughFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script ffmpeg wrapper needs /bin/sh")
	}
	e := nativeTestEngine(t)
	td := t.TempDir()
	src := filepath.Join(td, "clip.avi")
	if b, err := exec.Command(e.caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-frames:v", "15", "-c:v", "ffv1", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, b)
	}
	es, _ := makeRealM4V(t, e.caps.FFmpeg, td, 15)
	installFakeXvid(t, es, map[string]string{"PRERECS_FAKE_XVID_EXIT": "3"})

	rec := filepath.Join(td, "ffmpeg-calls.txt")
	wrap := filepath.Join(td, "ffmpeg-wrap.sh")
	script := "#!/bin/sh\n" +
		"last=\"\"\nfor a in \"$@\"; do last=\"$a\"; done\n" +
		"if [ -f \"$last\" ]; then s=present; else s=missing; fi\n" +
		"echo \"$s $last\" >> \"" + rec + "\"\n" +
		"exec \"" + e.caps.FFmpeg + "\" \"$@\"\n"
	if err := os.WriteFile(wrap, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	e.caps.FFmpeg = wrap

	info, err := probeMedia(e.caps.FFprobe, src, true)
	if err != nil {
		t.Fatal(err)
	}
	rep, _ := captureReporter()
	item := processItem(context.Background(), theme{}, e, info, ConvertOptions{Preset: "xvid_compact", OutputDir: td, StripAudio: true}, rep)
	if item.Status != "ok" {
		t.Fatalf("fallback conversion failed: status=%q msg=%q", item.Status, item.Message)
	}
	data, err := os.ReadFile(rec)
	if err != nil {
		t.Fatalf("wrapper log missing: %v", err)
	}
	checked := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		status, outPath, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		ext := strings.ToLower(filepath.Ext(outPath))
		if ext != ".avi" && ext != ".mov" {
			continue // scan/verify invocations end in "-", not an output path
		}
		checked++
		if status != "present" {
			t.Fatalf("output path did not exist when ffmpeg ran — reservation was released early: %s\nall calls:\n%s", line, data)
		}
	}
	if checked == 0 {
		t.Fatalf("no output-writing ffmpeg invocation recorded:\n%s", data)
	}
}

// patchStrhLength rewrites the AVI stream header's dwLength field, which
// ffprobe uses for the stream duration — letting a test claim a duration that
// disagrees with the decoded frames without corrupting a single packet.
func patchStrhLength(t *testing.T, path string, frames uint32) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	idx := bytes.Index(data, []byte("strh"))
	if idx < 0 {
		t.Fatal("strh chunk not found")
	}
	binary.LittleEndian.PutUint32(data[idx+8+32:], frames) // dwLength field
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

// A container whose declared duration disagrees with its decoded frames proves
// the metadata is inconsistent but does not say which field is stale. PreRecs
// must not fail the conversion (the decoded stream is intact) nor retime it —
// it must fall back to passthrough timing and verify on the exact frame count.
func TestProcessItemStaleDurationPreservesTiming(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["libxvid"] {
		t.Skip("libxvid unavailable")
	}
	td := t.TempDir()
	src := filepath.Join(td, "clip.avi")
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-frames:v", "30", "-c:v", "libxvid", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, b)
	}
	// Claim 90 frames/3s in the header while the stream really holds 30/1s.
	patchStrhLength(t, src, 90)
	info, err := probeMedia(caps.FFprobe, src, false)
	if err != nil {
		t.Fatal(err)
	}
	if info.Duration <= 1.5 {
		t.Fatalf("fixture did not produce a stale duration: %v", info.Duration)
	}
	outDir := filepath.Join(td, "out")
	rep, _ := captureReporter()
	item := processItem(context.Background(), theme{}, &Engine{caps: caps, enc: enc}, info,
		ConvertOptions{Preset: "xvid_compact", OutputDir: outDir, StripAudio: true}, rep)
	if item.Status != "ok" {
		t.Fatalf("stale-duration source failed instead of preserving timing: status=%q msg=%q", item.Status, item.Message)
	}
	outInfo, err := probeMedia(caps.FFprobe, item.Output, false)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(outInfo.Duration-1.0) > 0.2 {
		t.Fatalf("output duration %.3fs — timing was not preserved (~1.0s expected)", outInfo.Duration)
	}
}
