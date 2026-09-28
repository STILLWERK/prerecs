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

// The ffprobe call must keep stdout and stderr in separate sinks: a non-fatal
// error line merged into the JSON document used to fail the whole probe with a
// bare unmarshal error.
func TestProbeMediaContextErrorPaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake executable is not portable to Windows")
	}
	dir := t.TempDir()
	writeFake := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	jsonDoc := `{"streams":[{"codec_name":"h264","codec_type":"video","width":64,"height":64,"pix_fmt":"yuv420p","avg_frame_rate":"30/1","r_frame_rate":"30/1","duration":"1.0","nb_frames":"30"}],"format":{"duration":"1.0","size":"1024"}}`

	// Non-fatal stderr noise alongside valid JSON must still parse.
	noisy := writeFake("ffprobe-noisy", "#!/bin/sh\necho '[h264] could not find codec parameters' >&2\nprintf '%s' '"+jsonDoc+"'\n")
	info, err := probeMediaContext(context.Background(), noisy, filepath.Join(dir, "in.mp4"), false)
	if err != nil {
		t.Fatalf("stderr noise corrupted the probe result: %v", err)
	}
	if info.Codec != "h264" || info.FrameCount != 30 {
		t.Fatalf("probe result wrong: %+v", info)
	}

	// A nonzero exit must quote stderr — not an empty or merged message.
	failing := writeFake("ffprobe-fail", "#!/bin/sh\necho 'moov atom not found' >&2\nexit 3\n")
	_, err = probeMediaContext(context.Background(), failing, filepath.Join(dir, "in.mp4"), false)
	if err == nil || !strings.Contains(err.Error(), "moov atom not found") {
		t.Fatalf("stderr diagnostic lost: %v", err)
	}

	// A Start failure must surface the underlying error, not "ffprobe: ".
	_, err = probeMediaContext(context.Background(), filepath.Join(dir, "missing-ffprobe"), "in.mp4", false)
	if err == nil || !strings.Contains(err.Error(), "no such file") && !strings.Contains(err.Error(), "cannot find") {
		t.Fatalf("start failure lost the underlying error: %v", err)
	}

	// Garbage stdout must be reported as unparsable output, not a bare
	// encoding/json error with no context.
	garbage := writeFake("ffprobe-garbage", "#!/bin/sh\nprintf 'not json'\n")
	_, err = probeMediaContext(context.Background(), garbage, "in.mp4", false)
	if err == nil || !strings.Contains(err.Error(), "unparsable") {
		t.Fatalf("unparsable output not wrapped: %v", err)
	}

	// A NaN duration string must be clamped rather than poison downstream math.
	nanDoc := strings.Replace(jsonDoc, `"duration":"1.0"`, `"duration":"nan"`, -1)
	nanProbe := writeFake("ffprobe-nan", "#!/bin/sh\nprintf '%s' '"+nanDoc+"'\n")
	info, err = probeMediaContext(context.Background(), nanProbe, "in.mp4", false)
	if err != nil {
		t.Fatalf("nan duration probe failed: %v", err)
	}
	if info.Duration != 0 {
		t.Fatalf("NaN duration leaked into MediaInfo: %v", info.Duration)
	}

	// An implausible stream-level duration must fall back to the format-level
	// one, not zero out and discard usable metadata.
	fallbackDoc := `{"streams":[{"codec_name":"h264","codec_type":"video","width":64,"height":64,"pix_fmt":"yuv420p","avg_frame_rate":"30/1","r_frame_rate":"30/1","duration":"1e300","nb_frames":"30"}],"format":{"duration":"2.5","size":"1024"}}`
	fallbackProbe := writeFake("ffprobe-fallback", "#!/bin/sh\nprintf '%s' '"+fallbackDoc+"'\n")
	info, err = probeMediaContext(context.Background(), fallbackProbe, "in.mp4", false)
	if err != nil {
		t.Fatalf("fallback duration probe failed: %v", err)
	}
	if info.Duration != 2.5 {
		t.Fatalf("implausible stream duration shadowed format duration: %v", info.Duration)
	}
}
