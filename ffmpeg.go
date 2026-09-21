package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os/exec"
	"strconv"
	"strings"
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
	cleanTimeline := false
	if req.Conform {
		filters = append(conformVideoFilters(target), filters...)
		cleanTimeline = true
	} else if sourceClass(info) == "compressed" && info.FrameCountExact && target != nil {
		filters = append(conformVideoFilters(target), filters...)
		cleanTimeline = true
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
	if cleanTimeline {
		args = append(args, "-fps_mode", "passthrough", "-enc_time_base", "filter")
	} else {
		args = append(args, "-fps_mode", "passthrough")
	}
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
		args = append(args, "-vf", strings.Join(filters, ","), "-fps_mode", "passthrough", "-enc_time_base", "filter")
	} else if sourceClass(info) == "compressed" && info.FrameCountExact && target != nil {
		// Distribution files found in the wild can have stale AVI indexes and
		// broken/gapped timestamps. Once SCAN has established the actual decoded
		// picture count, build a clean constant-rate editing timeline from those
		// pictures instead of carrying bad source timestamps into the intermediate.
		filters = append(conformVideoFilters(target), filters...)
		args = append(args, "-vf", strings.Join(filters, ","), "-fps_mode", "passthrough", "-enc_time_base", "filter")
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
		if pix == "yuva444p" || pix == "gray" {
			return nil, nil, 0, fmt.Errorf("ut video cannot preserve source pixel format %s with this FFmpeg build", info.PixelFormat)
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

func (e *Engine) runFFmpeg(ctx context.Context, args []string, expectedDur float64, progress func(progressInfo)) error {
	cmd := exec.CommandContext(ctx, e.caps.FFmpeg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var errBuf strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		// ReadSlice drains in bounded fragments: a pathological overlong line
		// can neither stall the pipe nor land in memory whole, and the error
		// buffer keeps only the first 32 KiB. ErrBufferFull is a full fragment,
		// not a failure — keep draining or a long line would block FFmpeg on
		// the pipe exactly like the old capped scanner did.
		r := bufio.NewReader(stderr)
		for {
			frag, rerr := r.ReadSlice('\n')
			if remain := maxDiagnosticBytes - errBuf.Len(); remain > 0 {
				if len(frag) > remain {
					frag = frag[:remain]
				}
				errBuf.Write(frag)
			}
			if rerr != nil && !errors.Is(rerr, bufio.ErrBufferFull) {
				return
			}
		}
	}()

	started := time.Now()
	pi := progressInfo{}
	r := bufio.NewReader(stdout)
	for {
		raw, rerr := r.ReadString('\n')
		if k, v, ok := strings.Cut(strings.TrimRight(raw, "\r\n"), "="); ok {
			switch k {
			case "frame":
				pi.Frame = v
			case "fps":
				pi.FPS = strings.TrimSpace(v)
			case "speed":
				pi.Speed = strings.TrimSpace(v)
			case "total_size":
				pi.Bytes = parseInt64(strings.TrimSpace(v))
			case "out_time_us", "out_time_ms":
				// FFmpeg <5.x emitted the same microsecond value under the
				// misleading name out_time_ms; accept both keys.
				us, _ := strconv.ParseFloat(v, 64)
				pi.Time = us / 1e6
				if expectedDur > 0 {
					pi.Percent = math.Max(0, math.Min(1, pi.Time/expectedDur))
				}
				pi.ETA = etaFromProgress(started, pi.Percent)
				progress(pi)
			case "progress":
				if v == "end" {
					pi.Percent = 1
					pi.ETA = 0
					progress(pi)
				}
			}
		}
		if rerr != nil {
			break
		}
	}
	// Drain stderr fully before reaping: Wait closes the pipes after process
	// exit, which could otherwise truncate the tail of the error buffer.
	<-done
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("ffmpeg: %s", strings.TrimSpace(errBuf.String()))
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
