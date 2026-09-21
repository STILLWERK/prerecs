package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeXvidArgsAreFrameSafeAndFixedQuant(t *testing.T) {
	info := MediaInfo{Path: `C:\clips\master.avi`, Codec: "lagarith", Width: 2560, Height: 1440, FrameCount: 645}
	target := big.NewRat(300, 1)
	for preset, q := range map[string]string{
		"xvid_max":     "1",
		"xvid_compact": "2",
		"xvid_small":   "3",
	} {
		args := nativeXvidArgs(info, ConvertOptions{Preset: preset}, "out.avi", target, info.FrameCount)
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
	args := nativeXvidArgs(info, ConvertOptions{Preset: "xvid_max_q2"}, "out.m4v", big.NewRat(300, 1), info.FrameCount)
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
	args := nativeXvidArgs(info, ConvertOptions{Preset: "xvid_efficient_q2"}, "out.m4v", big.NewRat(300, 1), info.FrameCount)
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

func TestNativeXvidWritesRawStreamForLiveProgress(t *testing.T) {
	info := MediaInfo{Path: `C:\clips\master.avi`, Codec: "lagarith", Width: 2560, Height: 1440, FrameCount: 645}
	// A zero frameBound must emit no -frames argument: an unverified container
	// count that understates the stream would truncate the encode to the lie
	// and let verification see claim == output.
	unbounded := nativeXvidArgs(info, ConvertOptions{Preset: "xvid_compact"}, "out.m4v", big.NewRat(300, 1), 0)
	if strings.Contains(" "+strings.Join(unbounded, " ")+" ", " -frames ") {
		t.Fatalf("unbounded encode must not pass -frames: %v", unbounded)
	}
	args := nativeXvidArgs(info, ConvertOptions{Preset: "xvid_compact"}, "out.m4v", big.NewRat(300, 1), info.FrameCount)
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
	noiseChunk := []byte(strings.Repeat("x", 4096))
	writeNoise := func(w *os.File, env string) {
		n, _ := strconv.Atoi(os.Getenv(env))
		for n > 0 {
			m := n
			if m > len(noiseChunk) {
				m = len(noiseChunk)
			}
			w.Write(noiseChunk[:m])
			n -= m
		}
	}
	writeNoise(os.Stdout, "PRERECS_FAKE_XVID_STDOUT_BYTES")
	writeNoise(os.Stderr, "PRERECS_FAKE_XVID_STDERR_BYTES")
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
	target, dur, err := e.runNativeXvid(context.Background(), info, ConvertOptions{Preset: "xvid_compact", StripAudio: true}, out, info.FrameCount, func(progressInfo) {})
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
	_, _, err := e.runNativeXvid(context.Background(), info, ConvertOptions{Preset: "xvid_compact", StripAudio: true}, out, info.FrameCount, func(progressInfo) {})
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
	_, _, err := e.runNativeXvid(context.Background(), info, ConvertOptions{Preset: "xvid_compact", StripAudio: true}, out, info.FrameCount, func(progressInfo) {})
	if err == nil || !strings.Contains(err.Error(), "native Xvid failed") {
		t.Fatalf("expected encoder failure, got %v", err)
	}
	if fileExists(out) {
		t.Fatal("output must not exist after encoder failure")
	}
}

func TestNativeXvidBoundsDiagnostics(t *testing.T) {
	e := nativeTestEngine(t)
	td := t.TempDir()
	es, frames := makeRealM4V(t, e.caps.FFmpeg, td, 10)
	installFakeXvid(t, es, map[string]string{
		"PRERECS_FAKE_XVID_EXIT":         "3",
		"PRERECS_FAKE_XVID_STDOUT_BYTES": strconv.Itoa(4 * maxDiagnosticBytes),
		"PRERECS_FAKE_XVID_STDERR_BYTES": strconv.Itoa(4 * maxDiagnosticBytes),
	})
	info := MediaInfo{Path: filepath.Join(td, "src.avi"), Codec: "ffv1", FPS: "30/1", FPSFloat: 30, FrameCount: int64(frames), FrameCountExact: true, Duration: float64(frames) / 30}
	out := filepath.Join(td, "out.avi")
	_, _, err := e.runNativeXvid(context.Background(), info, ConvertOptions{Preset: "xvid_compact", StripAudio: true}, out, info.FrameCount, func(progressInfo) {})
	if err == nil || !strings.Contains(err.Error(), "native Xvid failed") {
		t.Fatalf("expected encoder failure, got %v", err)
	}
	if len(err.Error()) > maxDiagnosticBytes+100 {
		t.Fatalf("unbounded diagnostic capture: error is %d bytes", len(err.Error()))
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
	_, _, err := e.runNativeXvid(ctx, info, ConvertOptions{Preset: "xvid_compact", StripAudio: true}, out, info.FrameCount, func(progressInfo) {})
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if fileExists(out + ".video.tmp.m4v") {
		t.Fatal("temporary stream leaked after cancellation")
	}
}
