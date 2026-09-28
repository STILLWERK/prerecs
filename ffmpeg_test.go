package main

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

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
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	// -enc_time_base filter requires FFmpeg 6.1; the fps filter already emits
	// CFR timestamps so the keyword is omitted to keep FFmpeg 5.1 working.
	if strings.Contains(joined, " -enc_time_base ") {
		t.Fatalf("enc_time_base flag returned: %v", args)
	}
	if strings.Contains(joined, " -fps_mode cfr ") || strings.Contains(joined, " -r 300/1 ") {
		t.Fatalf("unsafe CFR/output-r conform path returned: %v", args)
	}
}

// A stderr flood beyond the diagnostic cap must not stall the run: the copier
// goroutine keeps draining the pipe even though only the tail is retained, and
// the reported error stays bounded.
func TestRunFFmpegDrainsOverlongStderrLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg is a shell script")
	}
	td := t.TempDir()
	fake := filepath.Join(td, "ffmpeg")
	script := "#!/bin/sh\nhead -c 262144 /dev/zero | tr '\\0' 'x' 1>&2\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &Engine{caps: Capabilities{FFmpeg: fake}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	start := time.Now()
	err := e.runFFmpeg(ctx, []string{"-i", "in"}, 0, func(progressInfo) {})
	if err == nil {
		t.Fatal("expected nonzero-exit error")
	}
	if ctx.Err() != nil {
		t.Fatalf("runFFmpeg did not drain stderr before the 15s timeout (deadlock)")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("drain took %s", time.Since(start))
	}
	if len(err.Error()) > 33000 {
		t.Fatalf("error buffer not bounded: %d bytes", len(err.Error()))
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
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	if strings.Contains(joined, " -enc_time_base ") {
		t.Fatalf("enc_time_base flag returned: %v", args)
	}
}

// The timeline rebuild rewrites video timestamps from scratch, so it must not
// run while audio is being stream-copied — the copied track would keep the
// source timeline and drift against rebuilt video. Stripped or audio-less
// compressed sources still get the clean CFR chain; audio-carrying inputs
// keep passthrough timing for both streams.
func TestCompressedSourceWithCopiedAudioKeepsPassthroughTiming(t *testing.T) {
	e := &Engine{caps: Capabilities{}, enc: map[string]bool{"prores_ks": true}}
	info := MediaInfo{
		Path:            "download.mp4",
		Codec:           "h264",
		FPS:             "600/1",
		FPSFloat:        600,
		Duration:        3.328333333,
		FrameCount:      1997,
		FrameCountExact: true,
		PixelFormat:     "yuv420p",
		BitDepth:        8,
		Chroma:          "4:2:0",
		Audio:           []string{"aac"},
	}
	args, _, dur, err := e.buildCommand(info, ConvertOptions{Preset: "prores_lt"}, "out.mov")
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	if strings.Contains(joined, " settb=expr=") || strings.Contains(joined, " setpts=N") {
		t.Fatalf("timeline rebuild ran while audio is stream-copied: %v", args)
	}
	if !strings.Contains(joined, " -map 0:a? -c:a copy ") {
		t.Fatalf("audio copy args missing: %v", args)
	}
	if !strings.Contains(joined, " -fps_mode passthrough ") {
		t.Fatalf("passthrough timing missing: %v", args)
	}
	// Passthrough duration is the probed one, not count/target.
	if math.Abs(dur-3.328333333) > 1e-6 {
		t.Fatalf("duration=%f, want probed duration", dur)
	}

	// The same source with audio stripped rebuilds the timeline again.
	args, _, dur, err = e.buildCommand(info, ConvertOptions{Preset: "prores_lt", StripAudio: true}, "out.mov")
	if err != nil {
		t.Fatal(err)
	}
	joined = " " + strings.Join(args, " ") + " "
	if !strings.Contains(joined, " -vf settb=expr=1/600,setpts=N,fps=600/1 ") {
		t.Fatalf("stripped-audio source did not get the rebuild: %v", args)
	}
	if math.Abs(dur-1997.0/600.0) > 1e-9 {
		t.Fatalf("stripped rebuild duration=%f, want %f", dur, 1997.0/600.0)
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
// Process lifecycle: inherited pipes, cancellation, diagnostics
// ---------------------------------------------------------------------------

// A wrapper script can exit while a spawned child still holds its stdout and
// stderr pipes open. The run must still return once the encoder itself is
// gone — bounded by childPipeWaitDelay — rather than waiting for pipe EOF
// forever. An exit-0 process that stalls its pipes is judged by the output it
// produced, not the wedged plumbing; a nonzero exit still fails.
func TestRunFFmpegSurvivesDescendantHeldPipes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg is a shell script")
	}
	old := childPipeWaitDelay
	childPipeWaitDelay = 300 * time.Millisecond
	defer func() { childPipeWaitDelay = old }()

	for _, tc := range []struct {
		name    string
		script  string
		wantErr bool
	}{
		{
			name:    "clean exit with stuck pipes",
			script:  "#!/bin/sh\nprintf 'frame=5\\nprogress=end\\n'\nsleep 5 >&1 2>&2 &\nexit 0\n",
			wantErr: false,
		},
		{
			name:    "failure exit with stuck pipes",
			script:  "#!/bin/sh\necho 'encoder exploded' 1>&2\nsleep 5 >&1 2>&2 &\nexit 1\n",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			td := t.TempDir()
			fake := filepath.Join(td, "ffmpeg")
			if err := os.WriteFile(fake, []byte(tc.script), 0o755); err != nil {
				t.Fatal(err)
			}
			e := &Engine{caps: Capabilities{FFmpeg: fake}}
			start := time.Now()
			err := e.runFFmpeg(context.Background(), []string{"-i", "in"}, 0, func(progressInfo) {})
			// Without the WaitDelay bound this call would wait out the full
			// 5s grandchild sleep instead of returning at ~300ms.
			if elapsed := time.Since(start); elapsed > 4*time.Second {
				t.Fatalf("runFFmpeg did not return promptly: %s", elapsed)
			}
			if tc.wantErr && err == nil {
				t.Fatal("nonzero exit with stuck pipes was not reported")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("clean exit with stuck pipes failed: %v", err)
			}
		})
	}
}

// Cancellation must surface as the context error, not as a generic ffmpeg
// failure — headless exit codes distinguish the two.
func TestRunFFmpegCancellationSurfacesCtxErr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg is a shell script")
	}
	td := t.TempDir()
	fake := filepath.Join(td, "ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &Engine{caps: Capabilities{FFmpeg: fake}}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := e.runFFmpeg(ctx, []string{"-i", "in"}, 0, func(progressInfo) {})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("cancelled run did not return promptly: %s", elapsed)
	}
}

// The scan relies on the LAST progress block for the frame count; a trailing
// line without a newline at EOF must still be delivered.
func TestRunFFmpegDeliversUnterminatedFinalProgressLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake ffmpeg is a shell script")
	}
	td := t.TempDir()
	fake := filepath.Join(td, "ffmpeg")
	script := "#!/bin/sh\nprintf 'frame=7\\nout_time_us=1000000\\nprogress=continue\\n'\nprintf 'frame=42'" // no trailing newline
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := &Engine{caps: Capabilities{FFmpeg: fake}}
	var last progressInfo
	if err := e.runFFmpeg(context.Background(), []string{"-i", "in"}, 0, func(p progressInfo) { last = p }); err != nil {
		t.Fatal(err)
	}
	if last.Frame != "42" {
		t.Fatalf("final unterminated frame= line lost: %+v", last)
	}
}

// The diagnostic buffer keeps the tail — the decisive error in a long FFmpeg
// log is always at the end, while the head was historically kept instead.
func TestBoundedTailWriterKeepsTail(t *testing.T) {
	var w boundedTailWriter
	head := strings.Repeat("H", maxDiagnosticBytes)
	tail := "the actual failure line"
	if _, err := w.Write([]byte(head)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(tail)); err != nil {
		t.Fatal(err)
	}
	got := w.String()
	if len(got) != maxDiagnosticBytes {
		t.Fatalf("buffer len=%d, want capped at %d", len(got), maxDiagnosticBytes)
	}
	if !strings.HasSuffix(got, tail) {
		t.Fatalf("tail not retained: ...%q", got[len(got)-40:])
	}
	if !strings.HasPrefix(got, "HH") {
		t.Fatal("head-of-buffer content unexpectedly dropped")
	}
}

// ---------------------------------------------------------------------------
// Per-preset argument construction
// ---------------------------------------------------------------------------

func TestBuildCommandPresetArgs(t *testing.T) {
	info := MediaInfo{Path: "in.avi", Codec: "ffv1", FPS: "30/1", FPSFloat: 30, Duration: 1, FrameCount: 30, FrameCountExact: true, PixelFormat: "yuv420p", BitDepth: 8, Chroma: "4:2:0"}
	join := func(args []string) string { return " " + strings.Join(args, " ") + " " }

	for _, tc := range []struct {
		name   string
		preset string
		enc    map[string]bool
		caps   Capabilities
		want   []string
		absent []string
	}{
		{
			name:   "xvid small is Q3",
			preset: "xvid_small",
			enc:    map[string]bool{"libxvid": true},
			want:   []string{" -c:v libxvid ", " -qscale:v 3 ", " -mbd bits ", " -bf 0 ", " -vtag XVID "},
		},
		{
			name:   "xvid max is Q1",
			preset: "xvid_max",
			enc:    map[string]bool{"libxvid": true},
			want:   []string{" -c:v libxvid ", " -qscale:v 1 ", " -mbd bits ", " -vtag XVID "},
		},
		{
			name:   "xvid compact is Q2 VHQ-style bits",
			preset: "xvid_compact",
			enc:    map[string]bool{"libxvid": true},
			want:   []string{" -qscale:v 2 ", " -mbd bits "},
			absent: []string{" -mbd rd "},
		},
		{
			name:   "magicyuv args",
			preset: "magicyuv_lossless",
			enc:    map[string]bool{"magicyuv": true},
			caps:   Capabilities{MagicInstalled: true},
			want:   []string{" -c:v magicyuv ", " -pred gradient ", " -pix_fmt yuv420p "},
		},
		{
			name:   "utvideo args",
			preset: "utvideo_lossless",
			enc:    map[string]bool{"utvideo": true},
			want:   []string{" -c:v utvideo ", " -pred left ", " -pix_fmt yuv420p "},
		},
		{
			name:   "prores 422 maps profile 2",
			preset: "prores_422",
			enc:    map[string]bool{"prores_ks": true},
			want:   []string{" -c:v prores_ks ", " -profile:v 2 ", " -pix_fmt yuv422p10le ", " -movflags +write_colr "},
		},
		{
			name:   "prores 4444 carries alpha args",
			preset: "prores_4444",
			enc:    map[string]bool{"prores_ks": true},
			want:   []string{" -profile:v 4 ", " -pix_fmt yuva444p10le ", " -alpha_bits 8 "},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := info
			if tc.preset == "prores_4444" {
				in.HasAlpha = true
			}
			e := &Engine{caps: tc.caps, enc: tc.enc}
			args, _, _, err := e.buildCommand(in, ConvertOptions{Preset: tc.preset, StripAudio: true}, "out")
			if err != nil {
				t.Fatal(err)
			}
			joined := join(args)
			for _, w := range tc.want {
				if !strings.Contains(joined, w) {
					t.Fatalf("missing %q in %v", w, args)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(joined, a) {
					t.Fatalf("unexpected %q in %v", a, args)
				}
			}
		})
	}
}
