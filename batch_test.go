package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

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

// TestProcessItemCancelsExistingOutputProbe covers cancellation while an
// existing candidate's metadata probe is in flight: the item must report
// cancelled immediately instead of rejecting candidates and starting a doomed
// encode, the candidate must be preserved, and the reserved numbered output
// name must not be left behind.
func TestProcessItemCancelsExistingOutputProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake executable is not portable to Windows")
	}
	td := t.TempDir()
	src := filepath.Join(td, "clip.avi")
	if err := os.WriteFile(src, []byte("fake source"), 0644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(td, "out")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	cand := filepath.Join(outDir, "clip_xvid_compact.avi")
	if err := os.WriteFile(cand, []byte("fake candidate"), 0644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(td, "ffprobe")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 60\n"), 0755); err != nil {
		t.Fatal(err)
	}
	info := MediaInfo{
		Path: src, Codec: "ffv1", FPS: "30/1", FPSFloat: 30,
		Duration: 1, FrameCount: 30, FrameCountExact: true,
		PixelFormat: "yuv420p", BitDepth: 8, Chroma: "4:2:0",
	}
	e := &Engine{caps: Capabilities{FFprobe: fake}}
	rep, _ := captureReporter()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	item := processItem(ctx, theme{}, e, info, ConvertOptions{Preset: "xvid_compact", OutputDir: outDir, StripAudio: true}, rep)
	if item.Status != "cancelled" {
		t.Fatalf("status=%q, want cancelled", item.Status)
	}
	if !fileExists(cand) {
		t.Fatal("pre-existing candidate was removed")
	}
	if fileExists(filepath.Join(outDir, "clip_xvid_compact_2.avi")) {
		t.Fatal("reserved numbered output name left behind")
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

// --no-native-xvid must bypass the xvid_encraw path entirely: the item
// converts through FFmpeg libxvid and no native stage is ever announced. The
// fake encoder is armed to fail, so a regression that still calls it is
// distinguishable from a clean libxvid conversion only through the reported
// lines and backend label.
func TestNoNativeXvidBypassesNativePath(t *testing.T) {
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
	// Count invocations on the seam itself — stronger than matching reporter
	// phrasing, which a rename could silently blind.
	nativeCalls := 0
	inner := xvidEncrawCommand
	xvidEncrawCommand = func(ctx context.Context, path string, args ...string) *exec.Cmd {
		nativeCalls++
		return inner(ctx, path, args...)
	}
	t.Cleanup(func() { xvidEncrawCommand = inner })

	info, err := probeMedia(e.caps.FFprobe, src, true)
	if err != nil {
		t.Fatal(err)
	}
	rep, lines := captureReporter()
	item := processItem(context.Background(), theme{}, e, info,
		ConvertOptions{Preset: "xvid_compact", OutputDir: td, StripAudio: true, NoNativeXvid: true}, rep)
	if item.Status != "ok" {
		t.Fatalf("conversion failed: status=%q msg=%q", item.Status, item.Message)
	}
	if nativeCalls != 0 {
		t.Fatalf("xvid_encraw invoked %d times despite --no-native-xvid", nativeCalls)
	}
	for _, l := range *lines {
		if strings.Contains(strings.ToLower(l), "native xvid") {
			t.Fatalf("native path announced despite --no-native-xvid: %q", l)
		}
	}
	if !strings.Contains(item.Backend, "libxvid") {
		t.Fatalf("backend=%q, want FFmpeg libxvid", item.Backend)
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

// For trusted-codec containers like AVI, nb_frames is itself a header field —
// a stale table can claim more frames than the stream holds while staying
// internally consistent, so no pre-encode scan runs and the claimed count
// survives to verification. When the output's real decoded count disagrees,
// PreRecs must rescan the source once and re-verify against decoded truth
// rather than fail an honest conversion.
func TestProcessItemStaleFrameCountRescansAndVerifies(t *testing.T) {
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
		"-frames:v", "30", "-c:v", "ffv1", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, b)
	}
	// Claim 90 frames in the header while the stream really holds 30. FFV1 is
	// a trusted codec, so the claim is accepted as exact without a scan.
	patchStrhLength(t, src, 90)
	info, err := probeMedia(caps.FFprobe, src, false)
	if err != nil {
		t.Fatal(err)
	}
	if info.FrameCount != 90 || !info.FrameCountExact {
		t.Fatalf("fixture did not produce a stale trusted count: %+v", info)
	}
	outDir := filepath.Join(td, "out")
	rep, _ := captureReporter()
	item := processItem(context.Background(), theme{}, &Engine{caps: caps, enc: enc}, info,
		ConvertOptions{Preset: "xvid_compact", OutputDir: outDir, StripAudio: true}, rep)
	if item.Status != "ok" {
		t.Fatalf("stale-count source failed instead of rescanning: status=%q msg=%q", item.Status, item.Message)
	}
	if item.InputInfo.FrameCount != 30 {
		t.Fatalf("rescan did not replace the stale count: %d", item.InputInfo.FrameCount)
	}
	outInfo, err := probeMedia(caps.FFprobe, item.Output, true)
	if err != nil {
		t.Fatal(err)
	}
	if outInfo.FrameCount != 30 {
		t.Fatalf("output frames=%d, want 30", outInfo.FrameCount)
	}
	if math.Abs(outInfo.Duration-1.0) > 0.2 {
		t.Fatalf("output duration %.3fs — timing was not preserved (~1.0s expected)", outInfo.Duration)
	}
}

// The same stale-count rescue must NOT rescue a conform output when the rate
// it was built from is impeached and the timing cannot be recomputed: the
// timeline is unverifiable, so the job fails rather than report VERIFIED.
func TestProcessItemStaleFrameCountConformFails(t *testing.T) {
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
		"-frames:v", "30", "-c:v", "ffv1", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, b)
	}
	patchStrhLength(t, src, 90)
	info, err := probeMedia(caps.FFprobe, src, false)
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(td, "out")
	rep, _ := captureReporter()
	item := processItem(context.Background(), theme{}, &Engine{caps: caps, enc: enc}, info,
		ConvertOptions{Preset: "xvid_compact", OutputDir: outDir, StripAudio: true, Conform: true, Timescale: "0.1"}, rep)
	if item.Status != "failed" {
		t.Fatalf("unverifiable conform output must fail, got status=%q msg=%q", item.Status, item.Message)
	}
	if !strings.Contains(item.Message, "timing cannot be verified") {
		t.Fatalf("expected unverifiable-timing failure message, got %q", item.Message)
	}
}

// ---------------------------------------------------------------------------
// Per-item probe deadline
// ---------------------------------------------------------------------------

// The existing-candidate probe and the post-encode probe run under the shared
// per-probe deadline: a wedged ffprobe rejects the candidate / fails the item
// after mediaProbeTimeout rather than stalling until user cancellation. A
// child-deadline failure is a per-item failure, not a job cancellation.
func TestProcessItemCandidateProbeTimeoutRejects(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake executable is not portable to Windows")
	}
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["libxvid"] || !enc["ffv1"] {
		t.Skip("libxvid/ffv1 unavailable")
	}
	caps.HasNativeXvid = false
	old := mediaProbeTimeout
	mediaProbeTimeout = 300 * time.Millisecond
	defer func() { mediaProbeTimeout = old }()

	td := t.TempDir()
	src := filepath.Join(td, "clip.avi")
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-frames:v", "15", "-c:v", "ffv1", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, b)
	}
	outDir := filepath.Join(td, "out")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	// A candidate that exists but cannot be probed within the deadline.
	cand := filepath.Join(outDir, "clip_xvid_compact.avi")
	if err := os.WriteFile(cand, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}
	fakeProbe := filepath.Join(td, "ffprobe")
	if err := os.WriteFile(fakeProbe, []byte("#!/bin/sh\nexec sleep 60\n"), 0755); err != nil {
		t.Fatal(err)
	}
	caps.FFprobe = fakeProbe

	info := MediaInfo{
		Path: src, Codec: "ffv1", FPS: "30/1", FPSFloat: 30,
		Duration: 0.5, FrameCount: 15, FrameCountExact: true,
		PixelFormat: "yuv420p", BitDepth: 8, Chroma: "4:2:0",
	}
	e := &Engine{caps: caps, enc: enc}
	rep, _ := captureReporter()
	start := time.Now()
	item := processItem(context.Background(), theme{}, e, info, ConvertOptions{Preset: "xvid_compact", OutputDir: outDir, StripAudio: true}, rep)
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("wedged probes were not bounded: %s", elapsed)
	}
	if item.Status == "cancelled" {
		t.Fatal("per-probe timeout must not masquerade as cancellation")
	}
	// The wedged probe also hits the post-encode metadata read, so the item
	// fails there — but bounded and reported as failure, never VERIFIED.
	if item.Status != "failed" || !strings.Contains(item.Message, "probe") {
		t.Fatalf("status=%q message=%q — want failed probe", item.Status, item.Message)
	}
	if !fileExists(cand) {
		t.Fatal("timed-out candidate was removed — only fresh output is disposable")
	}
}

// The headline reuse path: a candidate that probes, decodes and verifies must
// be reported as skipped-with-existing instead of re-encoded, and the fresh
// reservation for a numbered name must be released.
func TestProcessItemReusesVerifiedExistingOutput(t *testing.T) {
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
		"-frames:v", "30", "-c:v", "ffv1", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, b)
	}
	info, err := probeMedia(caps.FFprobe, src, false)
	if err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(td, "out")
	if err := os.MkdirAll(outDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Produce the canonical output with the same build path a conversion
	// would use, so verification has no reason to reject it.
	cand := filepath.Join(outDir, "clip_xvid_compact.avi")
	e := &Engine{caps: caps, enc: enc}
	args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "xvid_compact", StripAudio: true}, cand)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.runFFmpeg(context.Background(), args, 0, func(progressInfo) {}); err != nil {
		t.Fatalf("candidate encode: %v", err)
	}
	candStat, err := os.Stat(cand)
	if err != nil {
		t.Fatal(err)
	}

	rep, lines := captureReporter()
	item := processItem(context.Background(), theme{}, e, info, ConvertOptions{Preset: "xvid_compact", OutputDir: outDir, StripAudio: true}, rep)
	if item.Status != "skipped" || item.Output != cand {
		t.Fatalf("existing verified output was not reused: status=%q output=%q msg=%q", item.Status, item.Output, item.Message)
	}
	if item.Backend != "existing verified output" {
		t.Fatalf("backend=%q", item.Backend)
	}
	st2, err := os.Stat(cand)
	if err != nil || !st2.ModTime().Equal(candStat.ModTime()) {
		t.Fatal("existing candidate was modified during reuse")
	}
	if fileExists(filepath.Join(outDir, "clip_xvid_compact_2.avi")) {
		t.Fatal("numbered reservation was left behind after reuse")
	}
	joined := strings.Join(*lines, "\n")
	if !strings.Contains(joined, "VERIFIED") {
		t.Fatalf("reuse path did not verify: %v", *lines)
	}
}

// Cancelling mid-encode must classify as cancelled and release the reserved
// output name — a leftover 0-byte reservation would block the next run.
func TestProcessItemEncodeCancelRemovesReservedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake executable is not portable to Windows")
	}
	td := t.TempDir()
	fake := filepath.Join(td, "ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 60\n"), 0755); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(td, "out")
	info := MediaInfo{
		Path: filepath.Join(td, "clip.avi"), Codec: "ffv1", FPS: "30/1", FPSFloat: 30,
		Duration: 1, FrameCount: 30, FrameCountExact: true,
		PixelFormat: "yuv420p", BitDepth: 8, Chroma: "4:2:0",
	}
	e := &Engine{caps: Capabilities{FFmpeg: fake}, enc: map[string]bool{"prores_ks": true}}
	rep, _ := captureReporter()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	item := processItem(ctx, theme{}, e, info, ConvertOptions{Preset: "prores_lt", OutputDir: outDir, StripAudio: true}, rep)
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("cancelled encode did not return promptly: %s", elapsed)
	}
	if item.Status != "cancelled" {
		t.Fatalf("status=%q, want cancelled", item.Status)
	}
	if item.Output != "" && fileExists(item.Output) {
		t.Fatalf("cancelled item left reserved output behind: %s", item.Output)
	}
}

// ---------------------------------------------------------------------------
// Batch orchestration seams (processItemFunc / batchWorkerCountFunc) make the
// parallel path testable on machines too small to reach multiple workers.
// ---------------------------------------------------------------------------

func installBatchSeams(t *testing.T, workers int, fn func(context.Context, theme, *Engine, MediaInfo, ConvertOptions, itemReporter) ItemResult) {
	t.Helper()
	oldPI, oldWC := processItemFunc, batchWorkerCountFunc
	t.Cleanup(func() { processItemFunc, batchWorkerCountFunc = oldPI, oldWC })
	batchWorkerCountFunc = func(ConvertOptions, []MediaInfo) int { return workers }
	processItemFunc = fn
}

// The parallel worker pool must deliver every item exactly once, in input
// order, regardless of how interleaved the workers' completion order is.
func TestRunBatchParallelOrderingAndCompletion(t *testing.T) {
	const n = 7
	var mu sync.Mutex
	started := map[string]bool{}
	installBatchSeams(t, 3, func(ctx context.Context, ui theme, e *Engine, info MediaInfo, opts ConvertOptions, rep itemReporter) ItemResult {
		mu.Lock()
		started[info.Path] = true
		mu.Unlock()
		time.Sleep(5 * time.Millisecond)
		return ItemResult{Source: info.Path, Status: "ok", Output: filepath.Join("out", filepath.Base(info.Path)+".avi")}
	})
	infos := make([]MediaInfo, 0, n)
	for i := 0; i < n; i++ {
		infos = append(infos, MediaInfo{Path: fmt.Sprintf("src%02d.avi", i)})
	}
	result := runBatch(context.Background(), theme{}, &Engine{}, infos, ConvertOptions{})
	if result.Successes != n || len(result.Items) != n {
		t.Fatalf("successes=%d items=%d", result.Successes, len(result.Items))
	}
	for i, it := range result.Items {
		if it.Source != infos[i].Path {
			t.Fatalf("item %d is %s, want %s — parallel results must stay input-ordered", i, it.Source, infos[i].Path)
		}
	}
	if len(started) != n {
		t.Fatalf("only %d/%d items ran", len(started), n)
	}
}

// Cancelling mid-fanout must return promptly with the already-finished items
// recorded and no hang on the jobs/done channels.
func TestRunBatchParallelCancellation(t *testing.T) {
	installBatchSeams(t, 4, func(ctx context.Context, ui theme, e *Engine, info MediaInfo, opts ConvertOptions, rep itemReporter) ItemResult {
		select {
		case <-ctx.Done():
			return ItemResult{Source: info.Path, Status: "cancelled"}
		case <-time.After(150 * time.Millisecond):
			return ItemResult{Source: info.Path, Status: "ok"}
		}
	})
	infos := make([]MediaInfo, 0, 8)
	for i := 0; i < 8; i++ {
		infos = append(infos, MediaInfo{Path: fmt.Sprintf("src%02d.avi", i)})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := runBatch(ctx, theme{}, &Engine{}, infos, ConvertOptions{})
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("cancelled batch did not return promptly: %s", elapsed)
	}
	// No fake can finish inside the 50ms deadline (each takes 150ms), and
	// addBatchItem drops cancelled items — so the tally must be empty.
	if len(result.Items) != 0 || result.Successes != 0 || result.Failures != 0 || result.Skipped != 0 {
		t.Fatalf("cancelled run leaked items into the tally: %+v", result)
	}
}

// A scanned source whose frame rate was never corroborated by a duration must
// not have a fabricated duration installed: passthrough outputs carry real
// timestamps, and comparing them against an invented expectation produces
// false verification failures.
func TestReconcileScannedInputDoesNotFabricateDuration(t *testing.T) {
	rep, _ := captureReporter()

	// Rate present but duration unknown: keep the rate, leave duration empty.
	info := MediaInfo{FPS: "30/1", FPSFloat: 30, Duration: 0}
	reconcileScannedInput(&info, 90, theme{}, rep)
	if info.Duration != 0 {
		t.Fatalf("uncorroborated rate manufactured duration %v", info.Duration)
	}
	if info.FPSFloat != 30 || info.FrameCount != 90 || !info.FrameCountExact {
		t.Fatalf("scan results should be retained: %+v", info)
	}

	// Corroborated rate+duration: duration is refreshed against the count.
	info = MediaInfo{FPS: "30/1", FPSFloat: 30, Duration: 3.0}
	reconcileScannedInput(&info, 90, theme{}, rep)
	if math.Abs(info.Duration-3.0) > 1e-9 {
		t.Fatalf("corroborated duration became %v", info.Duration)
	}

	// Contradictory rate: both fields are impeached and cleared.
	info = MediaInfo{FPS: "60/1", FPSFloat: 60, Duration: 3.0}
	reconcileScannedInput(&info, 90, theme{}, rep)
	if info.FPS != "" || info.FPSFloat != 0 || info.Duration != 0 {
		t.Fatalf("impeached metadata survived: %+v", info)
	}
}

// The Vulkan ProRes fast path must fall back to CPU prores_ks when the GPU
// encode fails, keeping the reserved output name and producing a verified file.
func TestProcessItemVulkanFallsBackToCPU(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell shim is not portable to Windows")
	}
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["prores_ks"] || !enc["ffv1"] {
		t.Skip("prores_ks or ffv1 unavailable")
	}
	td := t.TempDir()
	src := filepath.Join(td, "src.mkv")
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=30",
		"-frames:v", "30", "-c:v", "ffv1", "-an", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, b)
	}
	// A shim that fails only the Vulkan encoder and delegates everything else
	// to the real ffmpeg binary.
	shim := filepath.Join(td, "ffmpeg")
	script := "#!/bin/sh\nfor a in \"$@\"; do\n  if [ \"$a\" = \"prores_ks_vulkan\" ]; then\n    echo 'no vulkan device' >&2\n    exit 1\n  fi\ndone\nexec '" + caps.FFmpeg + "' \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	e := &Engine{caps: caps, enc: enc}
	e.caps.FFmpeg = shim
	e.caps.HasProResVulkan = true
	info, err := probeMedia(caps.FFprobe, src, false)
	if err != nil {
		t.Fatal(err)
	}
	rep, lines := captureReporter()
	item := processItem(context.Background(), theme{}, e, info, ConvertOptions{Preset: "prores_lt"}, rep)
	if item.Status != "ok" {
		t.Fatalf("status=%q message=%q lines=%v", item.Status, item.Message, *lines)
	}
	if !strings.Contains(item.Backend, "CPU prores_ks") {
		t.Fatalf("backend=%q, want CPU prores_ks fallback", item.Backend)
	}
	if !fileExists(item.Output) {
		t.Fatalf("verified output missing: %s", item.Output)
	}
}
