package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
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
	for _, pixFmt := range []string{"rgba", "bgra", "argb", "abgr", "yuva420p", "yuva444p", "gbrap"} {
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
	info := MediaInfo{Audio: []AudioInfo{{Index: 1, Codec: "aac", Channels: 2, SampleRate: 48000}}}
	keep := strings.Join(e.audioArgs(info, ConvertOptions{}, nil, nil, false), " ")
	if !strings.Contains(keep, "-c:a copy") || strings.Contains(keep, "-an") {
		t.Fatalf("normal timing should copy audio unchanged, got %q", keep)
	}
	strip := strings.Join(e.audioArgs(info, ConvertOptions{StripAudio: true}, nil, nil, false), " ")
	if strip != "-an" {
		t.Fatalf("strip audio = %q", strip)
	}
	conform := strings.Join(e.audioArgs(info, ConvertOptions{Conform: true}, big.NewRat(30, 1), big.NewRat(300, 1), false), " ")
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
		Audio: []AudioInfo{{Codec: "aac", Channels: 2, SampleRate: 48000}},
	}
	good := MediaInfo{
		Codec: "mpeg4", CodecTag: "XVID", Width: 320, Height: 180, PixelFormat: "yuv420p",
		FrameCount: 30, FPSFloat: 30, Duration: 1,
		Audio: []AudioInfo{{Codec: "aac", Channels: 2, SampleRate: 48000}},
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
	infos := []MediaInfo{{Path: "clip.mp4", Audio: []AudioInfo{{Codec: "aac"}}}}
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
}

func TestBuildVulkanProResCommand(t *testing.T) {
	e := &Engine{caps: Capabilities{HasProResVulkan: true}}
	info := MediaInfo{Path: "source.avi", FPS: "30/1", FPSFloat: 30, FrameCount: 300, FrameCountExact: true, Duration: 10, PixelFormat: "yuv420p", Chroma: "4:2:0", BitDepth: 8}
	args, fps, dur, err := e.buildProResVulkanCommand(info, ConvertOptions{Preset: "prores_lt"}, "out.mov")
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{" -init_hw_device vulkan=prerecs_vk ", " -filter_hw_device prerecs_vk ", " format=yuv422p10le,hwupload ", " -c:v prores_ks_vulkan ", " -profile:v 1 ", " -async_depth 4 ", " -alpha_bits 0 "} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	if fps == nil || ratFloat(fps) != 30 || math.Abs(dur-10) > 1e-9 {
		t.Fatalf("timing fps=%v dur=%v", fps, dur)
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
