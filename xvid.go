package main

import (
	"context"
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
	"time"
)

func isXvidPreset(p string) bool {
	return p == "xvid_compact" || p == "xvid_max_q2" || p == "xvid_efficient_q2" || p == "xvid_small" || p == "xvid_max"
}

func isTunedXvidQ2(p string) bool {
	return p == "xvid_max_q2" || p == "xvid_efficient_q2"
}

func xvidQuant(p string) string {
	switch p {
	case "xvid_small":
		return "3"
	case "xvid_max":
		return "1"
	default:
		return "2"
	}
}

func nativeXvidThreads() (int, int) {
	cpus := runtime.NumCPU()
	threads := cpus
	if threads > 8 {
		threads = 8
	}
	if threads < 1 {
		threads = 1
	}
	slices := 1
	if cpus >= 4 {
		slices = 4
	} else if cpus >= 2 {
		slices = 2
	}
	return threads, slices
}

func formatFPSFloat(r *big.Rat) string {
	if r == nil {
		return "30"
	}
	return strconv.FormatFloat(ratFloat(r), 'f', 6, 64)
}

func nativeXvidEligible(info MediaInfo) bool {
	if strings.ToLower(filepath.Ext(info.Path)) != ".avi" {
		return false
	}
	class := sourceClass(info)
	return class == "lossless" || strings.EqualFold(info.Codec, "rawvideo")
}

func nativeXvidNeedsExactFrameScan(info MediaInfo) bool {
	if !nativeXvidEligible(info) || !info.FrameCountExact || info.FrameCount <= 0 {
		return false
	}
	if info.FPSFloat <= 0 || info.Duration <= 0 {
		return true
	}
	expected := int64(math.Round(info.Duration * info.FPSFloat))
	return expected <= 0 || expected != info.FrameCount
}

// frameBound caps the encode at a decode-verified frame count; pass 0 to let
// the encoder run to stream end — the only safe choice when the container's
// claimed count was never verified, since an understated table would
// otherwise truncate the output to match the lie.
func nativeXvidArgs(info MediaInfo, req ConvertOptions, tmpVideo string, target *big.Rat, frameBound int64) []string {
	threads, slices := nativeXvidThreads()
	q := xvidQuant(req.Preset)
	vhq := "1"
	maxB := "0"
	if isTunedXvidQ2(req.Preset) {
		vhq = "4"
		maxB = "2"
		slices = 1
	}
	bmin, bmax := q, q
	if req.Preset == "xvid_efficient_q2" {
		// Keep I/P references at strict Q2 but allow Xvid's B quantizer
		// formula to select Q3 (ratio 100 + offset 100) for disposable B-VOPs.
		bmin, bmax = "2", "31"
	}
	xargs := []string{
		"-i", info.Path, "-type", "2",
		"-o", tmpVideo, "-framerate", formatFPSFloat(target),
		"-cq", q, "-quality", "6", "-vhqmode", vhq,
		"-max_bframes", maxB, "-max_key_interval", "240", "-nopacked",
		"-imin", q, "-imax", q, "-pmin", q, "-pmax", q, "-bmin", bmin, "-bmax", bmax,
		"-threads", strconv.Itoa(threads), "-slices", strconv.Itoa(slices), "-progress", "1000000",
	}
	if req.Preset == "xvid_max_q2" {
		xargs = append(xargs, "-bvhq", "-bquant_ratio", "100", "-bquant_offset", "0", "-metric", "0")
	} else if req.Preset == "xvid_efficient_q2" {
		xargs = append(xargs, "-bvhq", "-bquant_ratio", "100", "-bquant_offset", "100", "-metric", "0")
	}
	if frameBound > 0 {
		xargs = append(xargs, "-frames", strconv.FormatInt(frameBound, 10))
	}
	return xargs
}

type mpeg4VOPCounter struct {
	offset int64
	carry  []byte
	count  int64
}

func (c *mpeg4VOPCounter) poll(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c.count, nil
		}
		return c.count, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return c.count, err
	}
	if st.Size() < c.offset {
		c.offset = 0
		c.carry = nil
		c.count = 0
	}
	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return c.count, err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return c.count, err
	}
	if len(b) == 0 {
		return c.count, nil
	}
	combined := make([]byte, 0, len(c.carry)+len(b))
	combined = append(combined, c.carry...)
	combined = append(combined, b...)
	for i := 0; i+3 < len(combined); i++ {
		// MPEG-4 Part 2 VOP start code: 00 00 01 B6. Every coded VOP is one
		// picture, including reordered B-VOPs, so this remains a frame counter.
		if combined[i] == 0x00 && combined[i+1] == 0x00 && combined[i+2] == 0x01 && combined[i+3] == 0xB6 {
			c.count++
		}
	}
	if len(combined) > 3 {
		c.carry = append(c.carry[:0], combined[len(combined)-3:]...)
	} else {
		c.carry = append(c.carry[:0], combined...)
	}
	c.offset += int64(len(b))
	return c.count, nil
}

type boundedTailWriter struct {
	buf []byte
}

func (w *boundedTailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if n >= maxDiagnosticBytes {
		w.buf = append(w.buf[:0], p[n-maxDiagnosticBytes:]...)
		return n, nil
	}
	if overflow := len(w.buf) + n - maxDiagnosticBytes; overflow > 0 {
		copy(w.buf, w.buf[overflow:])
		w.buf = w.buf[:len(w.buf)-overflow]
	}
	w.buf = append(w.buf, p...)
	return n, nil
}

func (w *boundedTailWriter) String() string {
	return string(w.buf)
}

func (e *Engine) runNativeXvid(ctx context.Context, info MediaInfo, req ConvertOptions, out string, frameBound int64, progress func(progressInfo)) (*big.Rat, float64, error) {
	if !e.caps.HasNativeXvid || e.caps.XvidEncRaw == "" {
		return nil, 0, errors.New("native Xvid encoder not available")
	}
	if !nativeXvidEligible(info) {
		return nil, 0, errors.New("native Xvid direct path is only used for compatible AVI lossless masters")
	}
	_, target, expectedDur, err := expectedTiming(info, req)
	if err != nil {
		return nil, 0, err
	}
	if target == nil {
		return nil, 0, errors.New("source FPS could not be determined")
	}

	// Raw MPEG-4 Part 2 output is intentional. xvid_encraw's AVIFile writer
	// locks the AVI during encoding and can leave inconsistent RIFF size fields.
	// The elementary stream stays readable while it grows, which lets PreRecs
	// count VOP start codes for genuinely live frame progress. FFmpeg wraps the
	// exact packets into a clean AVI after the encode; no video re-encode occurs.
	tmpVideo := out + ".video.tmp.m4v"
	_ = os.Remove(tmpVideo)
	defer os.Remove(tmpVideo)

	progress(progressInfo{Percent: 0, TotalFrames: info.FrameCount, Stage: "ENCODE"})
	xargs := nativeXvidArgs(info, req, tmpVideo, target, frameBound)
	cmd := xvidEncrawCommand(ctx, e.caps.XvidEncRaw, xargs...)
	var stdout, stderr boundedTailWriter
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	counter := &mpeg4VOPCounter{}
	lastFrames := int64(-1)
	var waitErr error
	finished := false
	reapTimedOut := false

	for !finished {
		select {
		case <-ctx.Done():
			waitErr = ctx.Err()
			// CommandContext kills the child, but wait for the Wait goroutine
			// to reap it so the .m4v handle is released before the deferred
			// Remove — Windows cannot unlink a file a process still holds.
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				reapTimedOut = true
			}
			finished = true
		case err := <-done:
			waitErr = err
			finished = true
			frames, _ := counter.poll(tmpVideo)
			if frames > lastFrames {
				lastFrames = frames
				pct := 0.0
				if info.FrameCount > 0 {
					pct = math.Max(0, math.Min(1, float64(frames)/float64(info.FrameCount)))
				}
				elapsed := time.Since(started).Seconds()
				fps := ""
				if elapsed > 0 && frames > 0 {
					fps = fmt.Sprintf("%.2f", float64(frames)/elapsed)
				}
				progress(progressInfo{Percent: pct, FPS: fps, Frame: strconv.FormatInt(frames, 10), TotalFrames: info.FrameCount, ETA: etaFromProgress(started, pct), Stage: "ENCODE"})
			}
		case <-ticker.C:
			frames, pollErr := counter.poll(tmpVideo)
			if pollErr != nil || frames == lastFrames {
				continue
			}
			lastFrames = frames
			pct := 0.0
			if info.FrameCount > 0 {
				pct = math.Max(0, math.Min(1, float64(frames)/float64(info.FrameCount)))
			}
			elapsed := time.Since(started).Seconds()
			fps := ""
			if elapsed > 0 && frames > 0 {
				fps = fmt.Sprintf("%.2f", float64(frames)/elapsed)
			}
			progress(progressInfo{Percent: pct, FPS: fps, Frame: strconv.FormatInt(frames, 10), TotalFrames: info.FrameCount, ETA: etaFromProgress(started, pct), Stage: "ENCODE"})
		}
	}
	if ctx.Err() != nil {
		if reapTimedOut {
			return nil, 0, fmt.Errorf("%w (encoder did not exit within 5s of cancellation; %s may remain locked)", ctx.Err(), tmpVideo)
		}
		return nil, 0, ctx.Err()
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return nil, 0, fmt.Errorf("native Xvid failed: %s", tailText(msg, 14))
	}
	if !fileExists(tmpVideo) {
		return nil, 0, errors.New("native Xvid did not create an output stream")
	}
	finalFrames, _ := counter.poll(tmpVideo)
	// The VOP count is only a meaningful integrity check against a verified
	// count (frameBound). An unverified container claim may itself be stale —
	// a mismatch would reject a complete encode before the post-encode source
	// rescan can establish the authoritative count.
	if frameBound > 0 && finalFrames != frameBound {
		return nil, 0, fmt.Errorf("native Xvid wrote %d VOP frames; expected %d", finalFrames, frameBound)
	}
	progress(progressInfo{Percent: 1, FPS: fmt.Sprintf("%.2f", float64(max64(finalFrames, info.FrameCount))/math.Max(.001, time.Since(started).Seconds())), Frame: strconv.FormatInt(max64(finalFrames, info.FrameCount), 10), TotalFrames: info.FrameCount, Stage: "ENCODE"})

	needAudio := len(info.Audio) > 0 && !req.StripAudio && !req.Conform
	remux := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-r", ratString(target), "-f", "m4v", "-i", tmpVideo}
	if needAudio {
		remux = append(remux, "-i", info.Path, "-map", "0:v:0", "-map", "1:a?")
	} else {
		remux = append(remux, "-map", "0:v:0")
	}
	remux = append(remux, "-c:v", "copy", "-vtag", "XVID")
	if needAudio {
		remux = append(remux, "-c:a", "copy")
	}
	remux = append(remux, "-progress", "pipe:1", "-stats_period", "0.25", "-nostats", out)
	if err := e.runFFmpeg(ctx, remux, expectedDur, func(p progressInfo) {
		p.Stage = "WRAP"
		p.TotalFrames = info.FrameCount
		progress(p)
	}); err != nil {
		return nil, 0, fmt.Errorf("AVI wrap/remux failed: %w", err)
	}
	return target, expectedDur, nil
}

// xvidEncrawCommand is a test seam: production code always launches the real
// xvid_encraw binary, while tests substitute a fake encoder process.
var xvidEncrawCommand = func(ctx context.Context, path string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, path, args...)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func tailText(s string, lines int) string {
	p := strings.Split(strings.TrimSpace(s), "\n")
	if len(p) > lines {
		p = p[len(p)-lines:]
	}
	return strings.Join(p, "\n")
}
