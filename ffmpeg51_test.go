package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Execution-level check against a real FFmpeg 5.1 build — the documented floor.
// The unit/integration suites run the host FFmpeg, which on modern machines is
// far newer than the floor; this test drives an actual 5.1 binary through the
// same processItem pipeline (probe -> scan -> encode -> remux -> verify) so a
// missing-option regression is caught by behaviour, not documentation.
//
// Point PRERECS_FFMPEG_51 / PRERECS_FFPROBE_51 at any 5.1-era binary pair, e.g.
// the johnvansickle static 5.1.1 build; otherwise the test skips.
func TestFFmpeg51EndToEndChain(t *testing.T) {
	ff := os.Getenv("PRERECS_FFMPEG_51")
	fp := os.Getenv("PRERECS_FFPROBE_51")
	if ff == "" || fp == "" {
		t.Skip("set PRERECS_FFMPEG_51 and PRERECS_FFPROBE_51 to a 5.1-era binary pair")
	}
	if out, err := exec.Command(ff, "-hide_banner", "-version").CombinedOutput(); err != nil {
		t.Skipf("PRERECS_FFMPEG_51 unusable: %v %s", err, out)
	}
	td := t.TempDir()
	outDir := filepath.Join(td, "out")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	e := &Engine{caps: Capabilities{FFmpeg: ff, FFprobe: fp}, enc: map[string]bool{"libxvid": true, "prores_ks": true}}

	// Conform path: the settb/setpts/fps chain plus -fps_mode passthrough is
	// everything the 6.1-only -enc_time_base keyword used to pin — it must run
	// clean on 5.1.
	src := filepath.Join(td, "master.avi")
	if b, err := exec.Command(ff,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-frames:v", "30", "-c:v", "ffv1", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("source fixture: %v %s", err, b)
	}
	info, err := probeMedia(fp, src, false)
	if err != nil {
		t.Fatalf("5.1 ffprobe: %v", err)
	}
	rep, _ := captureReporter()
	item := processItem(context.Background(), theme{}, e, info,
		ConvertOptions{Preset: "xvid_compact", OutputDir: outDir, StripAudio: true, Conform: true, Timescale: "0.1", CaptureFPS: "30"}, rep)
	if item.Status != "ok" {
		t.Fatalf("5.1 conform job: status=%q msg=%q", item.Status, item.Message)
	}
	if item.OutputInfo.FrameCount != 30 {
		t.Fatalf("conformed frames=%d, want 30", item.OutputInfo.FrameCount)
	}

	// Compressed source carrying audio: the exact decode scan must run on 5.1
	// and the audio-bearing passthrough path must verify.
	dl := filepath.Join(td, "download.mp4")
	if b, err := exec.Command(ff,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-frames:v", "30", "-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", dl,
	).CombinedOutput(); err != nil {
		t.Fatalf("compressed fixture: %v %s", err, b)
	}
	dlInfo, err := probeMedia(fp, dl, false)
	if err != nil {
		t.Fatalf("5.1 ffprobe compressed: %v", err)
	}
	rep2, lines := captureReporter()
	item = processItem(context.Background(), theme{}, e, dlInfo,
		ConvertOptions{Preset: "prores_lt", OutputDir: outDir}, rep2)
	if item.Status != "ok" {
		t.Fatalf("5.1 compressed+audio job: status=%q msg=%q\n%s", item.Status, item.Message, strings.Join(*lines, "\n"))
	}
	if len(item.OutputInfo.Audio) != len(dlInfo.Audio) {
		t.Fatalf("audio tracks %d -> %d", len(dlInfo.Audio), len(item.OutputInfo.Audio))
	}
}
