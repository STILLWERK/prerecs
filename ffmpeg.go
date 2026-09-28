package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os/exec"
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
	args := []string{"-hide_banner", "-nostdin", "-y", "-init_hw_device", "vulkan=prerecs_vk", "-filter_hw_device", "prerecs_vk", "-xerror", "-err_detect", "explode", "-i", info.Path, "-map", "0:v:0"}
	filters := []string{}
	if cf := colorFilter(info); cf != "" {
		filters = append(filters, cf)
	}
	if req.Conform {
		filters = append(conformVideoFilters(target), filters...)
	} else if compressedSourceNeedsTimelineRebuild(info, req, target) {
		filters = append(conformVideoFilters(target), filters...)
		if info.FrameCount > 0 {
			expectedDur = float64(info.FrameCount) / ratFloat(target)
		}
	}

	profile := map[string]string{"prores_lt": "1", "prores_422": "2", "prores_hq": "3", "prores_4444": "4"}[req.Preset]
	pix := "yuv422p10le"
	alphaBits := ""
	if req.Preset == "prores_4444" {
		pix = "yuv444p10le"
		if info.HasAlpha {
			pix = "yuva444p10le"
			alphaBits = strconv.Itoa(proresAlphaBits(info))
		}
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
	args = append(args, "-progress", "pipe:1", "-stats_period", "0.25", "-nostats", out)
	return args, target, expectedDur, nil
}

func (e *Engine) buildCommand(info MediaInfo, req ConvertOptions, out string) ([]string, *big.Rat, float64, error) {
	if err := validatePresetInputs(req.Preset, []MediaInfo{info}); err != nil {
		return nil, nil, 0, err
	}
	// -xerror + -err_detect explode: a source that reports decoder errors must
	// fail the encode rather than produce a silently truncated output.
	args := []string{"-hide_banner", "-nostdin", "-y", "-xerror", "-err_detect", "explode", "-i", info.Path, "-map", "0:v:0"}
	filters := []string{}
	if cf := colorFilter(info); cf != "" {
		filters = append(filters, cf)
	}
	filters = append(filters, proresRGBConversionFilters(info, req.Preset)...)
	_, target, expectedDur, err := expectedTiming(info, req)
	if err != nil {
		return nil, nil, 0, err
	}
	if req.Conform {
		filters = append(conformVideoFilters(target), filters...)
		args = append(args, "-vf", strings.Join(filters, ","), "-fps_mode", "passthrough")
	} else if compressedSourceNeedsTimelineRebuild(info, req, target) {
		// Distribution files found in the wild can have stale AVI indexes and
		// broken/gapped timestamps. Once SCAN has established the actual decoded
		// picture count, build a clean constant-rate editing timeline from those
		// pictures instead of carrying bad source timestamps into the intermediate.
		filters = append(conformVideoFilters(target), filters...)
		args = append(args, "-vf", strings.Join(filters, ","), "-fps_mode", "passthrough")
		if info.FrameCount > 0 {
			expectedDur = float64(info.FrameCount) / ratFloat(target)
		}
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
	args = append(args, "-progress", "pipe:1", "-stats_period", "0.25", "-nostats", out)
	return args, target, expectedDur, nil
}

// compressedSourceNeedsTimelineRebuild reports whether a scanned compressed
// source gets the clean constant-rate timeline rebuild. Rebuilding rewrites
// video timestamps from scratch, so it only runs when no audio is carried —
// stream-copied audio keeps the source timeline and would drift out of sync
// with rebuilt video. Audio-carrying inputs keep passthrough timing for both
// streams instead.
func compressedSourceNeedsTimelineRebuild(info MediaInfo, req ConvertOptions, target *big.Rat) bool {
	return target != nil && sourceClass(info) == "compressed" && info.FrameCountExact && info.FrameCount > 0 &&
		(req.StripAudio || len(info.Audio) == 0)
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
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
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
			return fmt.Errorf("ffmpeg: %w", err)
		}
		return fmt.Errorf("ffmpeg: %s", msg)
	}
	return nil
}

func (e *Engine) countDecodedFrames(ctx context.Context, info MediaInfo, progress func(progressInfo)) (int64, error) {
	// -xerror + -err_detect explode make decoder corruption fatal. Without them
	// FFmpeg logs decode errors but can still exit 0 after silently dropping
	// frames — which would let a truncated count pass verification as truth.
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-xerror", "-err_detect", "explode",
		"-i", info.Path,
		"-map", "0:v:0", "-an", "-sn", "-dn",
		"-fps_mode", "passthrough",
		"-progress", "pipe:1", "-stats_period", "0.25", "-nostats",
		"-f", "null", "-",
	}
	var last int64
	started := time.Now()
	err := e.runFFmpeg(ctx, args, info.Duration, func(p progressInfo) {
		if n := parseInt64(strings.TrimSpace(p.Frame)); n > 0 {
			last = n
			if elapsed := time.Since(started).Seconds(); elapsed > 0 {
				p.FPS = fmt.Sprintf("%.2f", float64(n)/elapsed)
			}
		}
		progress(p)
	})
	if err != nil {
		return 0, err
	}
	if last <= 0 {
		return 0, errors.New("decoder did not report a frame count")
	}
	return last, nil
}
