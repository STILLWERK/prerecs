package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Vulkan ProRes is a very new FFmpeg encoder. Use it automatically only for
// source layouts whose range/matrix do not need an RGB/full-range conversion.
// Those less-common inputs stay on the mature CPU encoder for now. Any runtime
// Vulkan failure also falls back to CPU in processItem.
func (e *Engine) canUseVulkanProRes(info MediaInfo, req ConvertOptions) bool {
	if req.CPUProRes || !e.caps.HasProResVulkan || !isProResPreset(req.Preset) {
		return false
	}
	if info.HasAlpha && req.Preset != "prores_4444" {
		return false
	}
	if isGrayAlphaPixelFormat(info.PixelFormat) {
		return false
	}
	if isRGBPixelFormat(info.PixelFormat) || strings.EqualFold(info.ColorSpace, "gbr") || strings.EqualFold(info.ColorRange, "pc") {
		return false
	}
	// >10-bit sources get the deeper CPU pin (yuv444p12le/gbrp16le chain) so
	// their extra precision actually survives; the Vulkan path pins 10-bit.
	if info.BitDepth > 10 {
		return false
	}
	return true
}

func (e *Engine) buildProResVulkanCommand(info MediaInfo, req ConvertOptions, out string) ([]string, *big.Rat, float64, error) {
	if err := validatePresetInputs(req.Preset, []MediaInfo{info}); err != nil {
		return nil, nil, 0, err
	}
	if !e.caps.HasProResVulkan {
		return nil, nil, 0, errors.New("FFmpeg build does not include prores_ks_vulkan")
	}
	if !isProResPreset(req.Preset) {
		return nil, nil, 0, fmt.Errorf("preset %q is not ProRes", req.Preset)
	}

	_, target, expectedDur, err := expectedTiming(info, req)
	if err != nil {
		return nil, nil, 0, err
	}
	// -xerror + -err_detect explode: a source that reports decoder errors must
	// fail the encode rather than produce a silently truncated output.
	args := []string{"-hide_banner", "-nostdin", "-y", "-init_hw_device", "vulkan=prerecs_vk", "-filter_hw_device", "prerecs_vk", "-xerror", "-err_detect", "explode", "-i", info.Path, "-map", "0:V:0"}
	filters := []string{}
	if cf := colorFilter(info); cf != "" {
		filters = append(filters, cf)
	}
	if req.Conform {
		filters = append(conformVideoFilters(target), filters...)
	} else if compressedSourceNeedsTimelineRebuild(info, req) {
		// Verify the output against the rate actually stamped: with the
		// container rate impeached the rebuild falls back to derived intent,
		// and a muxer that ignores it must not slip past verification.
		if rt := rebuildTargetRate(info, target); rt != nil {
			filters = append(conformVideoFilters(rt), filters...)
			expectedDur = float64(info.FrameCount) / ratFloat(rt)
			target = rt
		}
	}

	profile := map[string]string{"prores_lt": "1", "prores_422": "2", "prores_hq": "3", "prores_4444": "4"}[req.Preset]
	// Share the CPU path's pixel-format selection so >8-bit sources keep the
	// 12-bit targets here too instead of crushing through 10le.
	pix := proresPixelFormat(info, req.Preset)
	alphaBits := ""
	if req.Preset == "prores_4444" && info.HasAlpha {
		alphaBits = strconv.Itoa(proresAlphaBits(info))
	}
	filters = append(filters, "format="+pix, "hwupload")
	args = append(args, "-vf", strings.Join(filters, ","))
	args = append(args, "-fps_mode", "passthrough")
	// -alpha_bits is only meaningful when alpha exists; omit it otherwise
	// rather than forcing an explicit 0 on a young encoder.
	args = append(args, "-c:v", "prores_ks_vulkan", "-profile:v", profile, "-quant_mat", "auto")
	if alphaBits != "" {
		args = append(args, "-alpha_bits", alphaBits)
	}
	args = append(args, "-async_depth", "4")
	args = append(args, e.audioArgs(req)...)
	args = append(args, "-movflags", "+write_colr")
	args = append(args, colorOutputArgs(info, req.Preset)...)
	args = append(args, provenanceArgs(info, req)...)
	args = append(args, "-progress", "pipe:1", "-stats_period", "0.25", "-nostats", out)
	return args, target, expectedDur, nil
}

func (e *Engine) buildCommand(info MediaInfo, req ConvertOptions, out string) ([]string, *big.Rat, float64, error) {
	if err := validatePresetInputs(req.Preset, []MediaInfo{info}); err != nil {
		return nil, nil, 0, err
	}
	// -xerror + -err_detect explode: a source that reports decoder errors must
	// fail the encode rather than produce a silently truncated output.
	args := []string{"-hide_banner", "-nostdin", "-y", "-xerror", "-err_detect", "explode", "-i", info.Path, "-map", "0:V:0"}
	filters := []string{}
	if cf := colorFilter(info); cf != "" {
		filters = append(filters, cf)
	}
	filters = append(filters, proresRGBConversionFilters(info, req.Preset)...)
	filters = append(filters, xvidRGBConversionFilters(info, req.Preset)...)
	_, target, expectedDur, err := expectedTiming(info, req)
	if err != nil {
		return nil, nil, 0, err
	}
	var rebuildRate *big.Rat
	if !req.Conform && compressedSourceNeedsTimelineRebuild(info, req) {
		// Distribution files found in the wild can have stale AVI indexes and
		// broken/gapped timestamps. Once SCAN has established the actual decoded
		// picture count, build a clean constant-rate editing timeline from those
		// pictures instead of carrying bad source timestamps into the
		// intermediate. When no rate survives impeachment the rebuild is
		// skipped — the verify pass then checks the passthrough output's own
		// timestamps rather than pretending it was repaired.
		rebuildRate = rebuildTargetRate(info, target)
	}
	if req.Conform {
		filters = append(conformVideoFilters(target), filters...)
		args = append(args, "-vf", strings.Join(filters, ","), "-fps_mode", "passthrough")
	} else if rebuildRate != nil {
		filters = append(conformVideoFilters(rebuildRate), filters...)
		args = append(args, "-vf", strings.Join(filters, ","), "-fps_mode", "passthrough")
		if info.FrameCount > 0 {
			expectedDur = float64(info.FrameCount) / ratFloat(rebuildRate)
		}
		// The output is verified against the rate actually stamped — which may
		// be a derived fallback when the container rate was impeached.
		target = rebuildRate
	} else {
		if len(filters) > 0 {
			args = append(args, "-vf", strings.Join(filters, ","))
		}
		args = append(args, "-fps_mode", "passthrough")
	}

	switch req.Preset {
	case "xvid_compact", "xvid_max_q2", "xvid_efficient_q2", "xvid_small", "xvid_max":
		if !e.enc["libxvid"] {
			return nil, nil, 0, errors.New("FFmpeg build does not include libxvid")
		}
		q := "2"
		if req.Preset == "xvid_small" {
			q = "3"
		} else if req.Preset == "xvid_max" {
			q = "1"
		}
		mbd := "bits"
		if req.Preset == "xvid_max_q2" || req.Preset == "xvid_efficient_q2" {
			mbd = "rd"
		}
		args = append(args, "-c:v", "libxvid", "-qscale:v", q, "-g", "240", "-bf", "0", "-flags", "+mv4+aic", "-trellis", "1", "-me_quality", "4", "-mbd", mbd, "-gmc", "0", "-pix_fmt", "yuv420p")
		args = append(args, e.audioArgs(req)...)
		args = append(args, "-vtag", "XVID")
	case "prores_lt", "prores_422", "prores_hq", "prores_4444":
		if !e.enc["prores_ks"] {
			return nil, nil, 0, errors.New("FFmpeg build does not include prores_ks")
		}
		profile := map[string]string{"prores_lt": "1", "prores_422": "2", "prores_hq": "3", "prores_4444": "4"}[req.Preset]
		pix := proresPixelFormat(info, req.Preset)
		args = append(args, "-c:v", "prores_ks", "-profile:v", profile, "-quant_mat", "auto", "-pix_fmt", pix)
		if req.Preset == "prores_4444" && info.HasAlpha {
			args = append(args, "-alpha_bits", strconv.Itoa(proresAlphaBits(info)))
		}
		args = append(args, e.audioArgs(req)...)
		args = append(args, "-movflags", "+write_colr")
	case "magicyuv_lossless":
		if !e.enc["magicyuv"] {
			return nil, nil, 0, errors.New("FFmpeg build does not include magicyuv")
		}
		if !e.caps.MagicInstalled {
			return nil, nil, 0, errors.New("MagicYUV system/plugin installation was not detected")
		}
		pix, err := lossless8BitPixFmt(info, "MagicYUV")
		if err != nil {
			return nil, nil, 0, err
		}
		args = append(args, "-c:v", "magicyuv", "-pred", "gradient", "-pix_fmt", pix)
		args = append(args, e.audioArgs(req)...)
	case "utvideo_lossless":
		if !e.enc["utvideo"] {
			return nil, nil, 0, errors.New("FFmpeg build does not include utvideo")
		}
		pix, err := lossless8BitPixFmt(info, "Ut Video")
		if err != nil {
			return nil, nil, 0, err
		}
		args = append(args, "-c:v", "utvideo", "-pred", "left", "-pix_fmt", pix)
		args = append(args, e.audioArgs(req)...)
	default:
		return nil, nil, 0, fmt.Errorf("unknown preset %q", req.Preset)
	}
	args = append(args, colorOutputArgs(info, req.Preset)...)
	args = append(args, provenanceArgs(info, req)...)
	args = append(args, "-progress", "pipe:1", "-stats_period", "0.25", "-nostats", out)
	return args, target, expectedDur, nil
}

// compressedSourceNeedsTimelineRebuild reports whether a scanned compressed
// source gets the clean constant-rate timeline rebuild. Rebuilding rewrites
// video timestamps from scratch, so it only runs when no audio is carried —
// stream-copied audio keeps the source timeline and would drift out of sync
// with rebuilt video. Audio-carrying inputs keep passthrough timing for both
// streams instead.
//
// The rebuild also requires proven-broken timestamps: the exact decode scan's
// vfrdet delta stats flag duplicated/non-monotonic PTS, a collapsed spread, or
// missing stats. Healthy cadence — constant OR variable frame rate — goes
// through passthrough so genuine VFR captures are not silently flattened.
func compressedSourceNeedsTimelineRebuild(info MediaInfo, req ConvertOptions) bool {
	return sourceClass(info) == "compressed" && info.FrameCountExact && info.FrameCount > 0 &&
		info.TimestampsBroken &&
		(req.StripAudio || len(info.Audio) == 0)
}

// rebuildTargetRate resolves the frame rate a broken-timestamp rebuild should
// stamp. The container's rate is used when it survived impeachment; when it
// was cleared, derive one from intact container evidence — the declared
// duration, then (only when packet PTS are proven destroyed) the impeached
// rate claim itself, since destroyed timestamps cannot contradict it. The
// decoded presentation spread is deliberately never a rate source: a broken
// source's spread is the very thing that collapsed, so frames/spread invents
// absurd rates. Returns nil when nothing trustworthy remains; callers then
// verify the passthrough output's own timestamps rather than pretending it
// was repaired.
func rebuildTargetRate(info MediaInfo, target *big.Rat) *big.Rat {
	if target != nil {
		return target
	}
	if info.FrameCount <= 1 {
		return nil
	}
	if info.Duration > 0 {
		if rate := new(big.Rat).SetFloat64(float64(info.FrameCount) / info.Duration); rate != nil {
			if f := ratFloat(rate); f >= 1 && f <= 2000 {
				return rate
			}
		}
	}
	if info.TimestampsBroken && info.ImpeachedFPS != "" {
		if rate, err := parseRat(info.ImpeachedFPS); err == nil && rate != nil {
			if f := ratFloat(rate); f >= 1 && f <= 2000 {
				return rate
			}
		}
	}
	return nil
}

// timingReference returns the duration a healthy stream is implied to span,
// as the minimum over independently-claimed positive candidates: count/rate,
// the container duration, and — for destroyed-PTS sources — count over the
// impeached rate claim. Surviving fields are mutually consistent by the time
// this runs (reconcileScannedInput has pruned contradictions), so min() only
// narrows the reference against a stale-large claim: a header that lies 8×
// above the real cadence can no longer fabricate a "collapsed spread" verdict
// and retime a healthy stream, while a genuinely destroyed timeline still
// collapses far below any claim it could produce.
func timingReference(info MediaInfo, frames int64) float64 {
	best := math.Inf(1)
	if info.FPSFloat > 0 && frames > 0 {
		best = math.Min(best, float64(frames)/info.FPSFloat)
	}
	if info.Duration > 0 {
		best = math.Min(best, info.Duration)
	}
	if info.ImpeachedFPS != "" && frames > 0 {
		if r, err := parseRat(info.ImpeachedFPS); err == nil && r != nil {
			if f := ratFloat(r); f > 0 {
				best = math.Min(best, float64(frames)/f)
			}
		}
	}
	if math.IsInf(best, 1) {
		return 0
	}
	return best
}

func (e *Engine) audioArgs(req ConvertOptions) []string {
	if req.StripAudio {
		return []string{"-an"}
	}
	if req.Conform {
		return []string{"-an"}
	}
	return []string{"-map", "0:a?", "-c:a", "copy"}
}

const maxDiagnosticBytes = 32 * 1024

// progressPipeWriter parses FFmpeg's `-progress pipe:1` stream. It is
// installed as cmd.Stdout, so exec's internal copier goroutine delivers every
// byte and Wait waits for the drain — the final block (which carries the
// encode's frame count) cannot be dropped the way a post-Wait reader could.
type progressPipeWriter struct {
	expectedDur float64
	started     time.Time
	emit        func(progressInfo)
	mu          sync.Mutex
	pi          progressInfo
	buf         []byte
	closed      bool
}

func (w *progressPipeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		// WaitDelay can return while a copier goroutine is still mid-write on
		// an abandoned pipe; drop those bytes rather than emit past flush.
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	consumed := 0
	for {
		i := bytes.IndexByte(w.buf[consumed:], '\n')
		if i < 0 {
			break
		}
		w.line(string(w.buf[consumed : consumed+i]))
		consumed += i + 1
	}
	w.buf = append(w.buf[:0], w.buf[consumed:]...)
	// -progress output is small key=value lines; a pathological unterminated
	// flood must not grow this buffer without bound.
	if len(w.buf) > maxDiagnosticBytes {
		w.buf = w.buf[:0]
	}
	return len(p), nil
}

// flush handles a trailing fragment that ended without a newline at EOF. The
// fragment's keys only mutate pi — a final frame= with no following
// out_time/progress key would never be emitted — so emit the accumulated state
// once the fragment is parsed.
func (w *progressPipeWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if len(w.buf) > 0 {
		w.line(string(w.buf))
		w.buf = nil
		w.emit(w.pi)
	}
}

func (w *progressPipeWriter) line(s string) {
	k, v, ok := strings.Cut(strings.TrimRight(s, "\r"), "=")
	if !ok {
		return
	}
	switch k {
	case "frame":
		w.pi.Frame = v
	case "fps":
		w.pi.FPS = strings.TrimSpace(v)
	case "speed":
		w.pi.Speed = strings.TrimSpace(v)
	case "total_size":
		w.pi.Bytes = parseInt64(strings.TrimSpace(v))
	case "out_time_us", "out_time_ms":
		// FFmpeg <5.x emitted the same microsecond value under the
		// misleading name out_time_ms; accept both keys.
		us, _ := strconv.ParseFloat(v, 64)
		w.pi.Time = us / 1e6
		if w.expectedDur > 0 {
			w.pi.Percent = math.Max(0, math.Min(1, w.pi.Time/w.expectedDur))
		}
		w.pi.ETA = etaFromProgress(w.started, w.pi.Percent)
		w.emit(w.pi)
	case "progress":
		if v == "end" {
			w.pi.Percent = 1
			w.pi.ETA = 0
			w.emit(w.pi)
		}
	}
}

// childPipeWaitDelay bounds how long Run waits for the stdout/stderr copier
// goroutines after the process exits. A wrapper script or crashed decoder can
// leave a descendant holding the pipes open; without a bound those reads block
// forever even though the encoder itself is already gone.
var childPipeWaitDelay = 5 * time.Second

func (e *Engine) runFFmpeg(ctx context.Context, args []string, expectedDur float64, progress func(progressInfo)) error {
	_, err := e.runFFmpegTail(ctx, args, expectedDur, progress)
	return err
}

// runFFmpegTail is runFFmpeg that also hands back the captured stderr tail so
// callers that need diagnostics beyond the exit status (e.g. the timestamp
// stats emitted by the integrity scan) do not have to re-run the process.
func (e *Engine) runFFmpegTail(ctx context.Context, args []string, expectedDur float64, progress func(progressInfo)) (string, error) {
	if progress == nil {
		progress = func(progressInfo) {}
	}
	cmd := exec.CommandContext(ctx, e.caps.FFmpeg, args...)
	var stderr boundedTailWriter
	pw := &progressPipeWriter{expectedDur: expectedDur, started: time.Now(), emit: progress}
	cmd.Stdout = pw
	cmd.Stderr = &stderr
	// With io.Writer sinks, WaitDelay forcibly closes the copier pipes when the
	// process has exited but descendants keep them open: Run returns
	// ErrWaitDelay instead of hanging the worker. In the normal case Wait still
	// waits for the copiers to finish, so no output is lost.
	cmd.WaitDelay = childPipeWaitDelay
	err := cmd.Run()
	pw.flush()
	if errors.Is(err, exec.ErrWaitDelay) {
		if st := cmd.ProcessState; st != nil && st.ExitCode() == 0 {
			// FFmpeg exited cleanly but a descendant kept its pipes open and
			// WaitDelay force-closed the copiers. A completed mux is judged by
			// output verification, not by the wedged plumbing.
			err = nil
		}
	}
	if err == nil {
		// The process finished cleanly. Checking ctx first would let a
		// cancellation that landed between Run() and here discard a complete,
		// valid output — the caller deletes it as "cancelled".
		return stderr.String(), nil
	}
	if ctx.Err() != nil {
		return stderr.String(), ctx.Err()
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		switch {
		case errors.Is(err, exec.ErrWaitDelay):
			if msg == "" {
				msg = "process exited but its output pipes stayed open"
			} else {
				msg += " (output pipes did not close after exit)"
			}
		case msg == "":
			// No diagnostics to quote — a Start failure or a signal kill must
			// still surface the underlying error, not an empty "ffmpeg:".
			return stderr.String(), fmt.Errorf("ffmpeg: %w", err)
		}
		return stderr.String(), fmt.Errorf("ffmpeg: %s", msg)
	}
	return stderr.String(), nil
}

// timingStats carries the vfrdet delta summary from an integrity scan: min/max
// frame deltas in presentation order (B-frames handled by the filter), or nil
// when timing was not observed on that scan. vfrdet omits min/max entirely for
// uniformly spaced streams — HasDeltas distinguishes "uniform" (healthy) from
// "variable with a non-positive minimum" (broken).
//
// LastOutSec is the final decoded presentation timestamp reported by
// -progress — the real end-to-end spread of the stream, which vfrdet's
// delta classification cannot see: a stream where every frame shares one PTS
// (or monotonically collapses) reports as "uniform" while its spread is a
// single frame's duration. The spread check closes that blind spot.
type timingStats struct {
	Reported   bool
	HasDeltas  bool
	MinDelta   int64
	MaxDelta   int64
	Count      int64
	LastOutSec float64
}

// The VFR: field can spell an undefined ratio as nan/-nan (0 deltas seen) —
// accept it so "reported zero deltas" is distinguishable from "never
// reported"; those two cases classify differently (empty/unmeasurable vs
// proven-unreadable).
var vfrSummaryRe = regexp.MustCompile(`VFR:(?:-?nan|[\d.]+) +\((\d+)/\d+\)(?: +min: +(-?\d+) +max: +(-?\d+))?`)

// brokenTimestamps classifies a decode scan's timestamp stats. Only deltas
// that cannot be played back — duplicated, non-monotonic, or never reported —
// justify rebuilding the timeline. A uniformly spaced or genuinely variable
// cadence is healthy timing and is preserved as-is.
func brokenTimestamps(st *timingStats, frames int64, refDurSec float64) bool {
	if st == nil || !st.Reported {
		// No stats means the stream never produced measurable timestamps —
		// the very case the rebuild exists for.
		return true
	}
	if st.HasDeltas && st.MinDelta <= 0 {
		// Delta range only exists when cadence varied; a non-positive minimum
		// is duplicated or non-monotonic PTS, which playback cannot express.
		return true
	}
	// A stream whose frames all share one PTS (or whose packet timestamps
	// collapse far below any surviving metadata claim) need not report a
	// delta range — uniform-zero PTS produces none at all — so the collapse
	// check must also cover stats that did vary: positive-but-tiny deltas
	// over a seconds-long clip are just as destroyed. The reference is a
	// minimum over surviving metadata claims, and the collapse must be
	// extreme (8×): a merely-stale rate claim — the exact garbage this path
	// exists to clean up — cannot fabricate a "collapsed" verdict and retime
	// a healthy stream.
	if frames > 1 && refDurSec > 0 && st.LastOutSec > 0 && st.LastOutSec*8 <= refDurSec {
		return true
	}
	return false
}

func (e *Engine) countDecodedFrames(ctx context.Context, info MediaInfo, includeAudio bool, observeTiming bool, progress func(progressInfo)) (int64, *timingStats, error) {
	// -xerror + -err_detect explode make decoder corruption fatal. Without them
	// FFmpeg logs decode errors but can still exit 0 after silently dropping
	// frames — which would let a truncated count pass verification as truth.
	// Retained audio is decoded in the same pass so damaged tracks cannot hide
	// behind a healthy video track.
	logLevel := "error"
	if observeTiming {
		// vfrdet emits its delta summary at INFO level at end of stream.
		logLevel = "info"
	}
	args := []string{
		"-hide_banner", "-loglevel", logLevel, "-nostdin",
		"-xerror", "-err_detect", "explode",
		"-i", info.Path,
		"-map", "0:V:0", "-sn", "-dn",
	}
	if includeAudio {
		args = append(args, "-map", "0:a?")
	}
	if observeTiming {
		args = append(args, "-vf", "vfrdet")
	}
	args = append(args,
		"-fps_mode", "passthrough",
		"-progress", "pipe:1", "-stats_period", "0.25", "-nostats",
		"-f", "null", "-",
	)
	var last int64
	var lastOut float64
	started := time.Now()
	stderr, err := e.runFFmpegTail(ctx, args, info.Duration, func(p progressInfo) {
		if n := parseInt64(strings.TrimSpace(p.Frame)); n > 0 {
			last = n
			if elapsed := time.Since(started).Seconds(); elapsed > 0 {
				p.FPS = fmt.Sprintf("%.2f", float64(n)/elapsed)
			}
		}
		if p.Time > lastOut {
			lastOut = p.Time
		}
		progress(p)
	})
	stats := parseTimingStats(stderr)
	if stats == nil {
		stats = &timingStats{}
	}
	stats.LastOutSec = lastOut
	if err != nil {
		return 0, stats, err
	}
	if last <= 0 {
		return 0, stats, errors.New("decoder did not report a frame count")
	}
	return last, stats, nil
}

func parseTimingStats(stderr string) *timingStats {
	matches := vfrSummaryRe.FindAllStringSubmatch(stderr, -1)
	if len(matches) == 0 {
		return nil
	}
	m := matches[len(matches)-1]
	st := &timingStats{Reported: true, Count: parseInt64(m[1])}
	if m[2] != "" && m[3] != "" {
		st.HasDeltas = true
		st.MinDelta = parseInt64(m[2])
		st.MaxDelta = parseInt64(m[3])
	}
	return st
}
