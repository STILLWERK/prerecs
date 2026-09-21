package main

import (
	"fmt"
	"math"
	"math/big"
	"strings"
)

func presetCodecMatches(preset string, out MediaInfo) bool {
	switch preset {
	case "xvid_compact", "xvid_max_q2", "xvid_efficient_q2", "xvid_small", "xvid_max":
		return strings.EqualFold(out.Codec, "mpeg4") && strings.EqualFold(out.CodecTag, "XVID")
	case "prores_lt":
		return strings.EqualFold(out.Codec, "prores") && strings.EqualFold(out.Profile, "LT") && strings.EqualFold(out.PixelFormat, "yuv422p10le")
	case "prores_422":
		return strings.EqualFold(out.Codec, "prores") && strings.EqualFold(out.Profile, "Standard") && strings.EqualFold(out.PixelFormat, "yuv422p10le")
	case "prores_hq":
		return strings.EqualFold(out.Codec, "prores") && strings.EqualFold(out.Profile, "HQ") && strings.EqualFold(out.PixelFormat, "yuv422p10le")
	case "prores_4444":
		return strings.EqualFold(out.Codec, "prores") && strings.EqualFold(out.Profile, "4444") && prores4444PixelFormat(out.PixelFormat)
	case "magicyuv_lossless":
		return strings.EqualFold(out.Codec, "magicyuv")
	case "utvideo_lossless":
		return strings.EqualFold(out.Codec, "utvideo")
	default:
		return false
	}
}

func prores4444PixelFormat(pixFmt string) bool {
	low := strings.ToLower(strings.TrimSpace(pixFmt))
	// Current FFmpeg prores_ks builds report non-alpha profile-4444 output as
	// yuv444p12le even when yuv444p10le is requested. Keep the requested
	// encoder format, but accept that encoder-normalized profile-4444 result.
	return low == "yuv444p10le" || low == "yuv444p12le" || strings.HasPrefix(low, "yuva444p")
}

func proresOutputMatchesInput(preset string, in, out MediaInfo) bool {
	if !presetCodecMatches(preset, out) {
		return false
	}
	if preset == "prores_4444" {
		if in.HasAlpha {
			return hasAlpha(out.PixelFormat)
		}
		return strings.EqualFold(out.PixelFormat, "yuv444p10le") || strings.EqualFold(out.PixelFormat, "yuv444p12le")
	}
	return true
}

func verifyOutput(in, out MediaInfo, opts ConvertOptions, expected *big.Rat, expectedDur float64) []string {
	p := verify(in, out, expected, expectedDur)
	if in.Width > 0 && in.Height > 0 && (out.Width != in.Width || out.Height != in.Height) {
		p = append(p, fmt.Sprintf("dimension mismatch: %dx%d input vs %dx%d output", in.Width, in.Height, out.Width, out.Height))
	}
	if !presetCodecMatches(opts.Preset, out) {
		p = append(p, fmt.Sprintf("codec/profile/pixel format mismatch: output is %s/%s/%s/%s for preset %s", out.Codec, out.Profile, out.CodecTag, out.PixelFormat, opts.Preset))
	} else if isProResPreset(opts.Preset) && !proresOutputMatchesInput(opts.Preset, in, out) {
		p = append(p, fmt.Sprintf("ProRes profile/pixel format mismatch: output is %s/%s", out.Profile, out.PixelFormat))
	}

	wantAudio := len(in.Audio) > 0 && !opts.StripAudio && !opts.Conform
	if !wantAudio {
		if len(out.Audio) != 0 {
			p = append(p, fmt.Sprintf("audio mismatch: expected no audio, got %d track(s)", len(out.Audio)))
		}
	} else {
		if len(out.Audio) != len(in.Audio) {
			p = append(p, fmt.Sprintf("audio track mismatch: expected %d got %d", len(in.Audio), len(out.Audio)))
		} else if !opts.Conform {
			// Normal-timing audio is stream-copied. Verify that "keep audio" really
			// means the same codec came through, not a silent transcode or omission.
			for i := range in.Audio {
				if !strings.EqualFold(in.Audio[i], out.Audio[i]) {
					p = append(p, fmt.Sprintf("audio codec mismatch on track %d: expected %s got %s", i+1, in.Audio[i], out.Audio[i]))
				}
			}
		}
	}

	switch opts.Preset {
	case "xvid_compact", "xvid_max_q2", "xvid_efficient_q2", "xvid_small", "xvid_max":
		if !strings.EqualFold(out.PixelFormat, "yuv420p") {
			p = append(p, fmt.Sprintf("pixel format mismatch: Xvid expected yuv420p got %s", out.PixelFormat))
		}
	case "magicyuv_lossless":
		if want, err := lossless8BitPixFmt(in, "MagicYUV"); err == nil && !strings.EqualFold(out.PixelFormat, want) {
			p = append(p, fmt.Sprintf("pixel format mismatch: MagicYUV expected %s got %s", want, out.PixelFormat))
		}
	case "utvideo_lossless":
		if want, err := lossless8BitPixFmt(in, "Ut Video"); err == nil && !strings.EqualFold(out.PixelFormat, want) {
			p = append(p, fmt.Sprintf("pixel format mismatch: Ut Video expected %s got %s", want, out.PixelFormat))
		}
	case "prores_4444":
		if in.HasAlpha && !out.HasAlpha {
			p = append(p, "alpha mismatch: ProRes 4444 source has alpha but output does not")
		}
	}
	return p
}

func verify(in, out MediaInfo, expected *big.Rat, expectedDur float64) []string {
	p := []string{}
	if in.FrameCount > 0 && in.FrameCount != out.FrameCount {
		p = append(p, fmt.Sprintf("frame count mismatch: %d input vs %d output", in.FrameCount, out.FrameCount))
	}
	if expected != nil {
		if out.FPSFloat <= 0 {
			p = append(p, "frame rate missing from output metadata")
		} else {
			e := ratFloat(expected)
			if math.Abs(e-out.FPSFloat) > math.Max(.01, e*.002) {
				p = append(p, fmt.Sprintf("frame rate mismatch: expected %.6g got %.6g", e, out.FPSFloat))
			}
		}
	}
	if expectedDur > 0 {
		if out.Duration <= 0 {
			p = append(p, "duration missing from output metadata")
		} else {
			period := .05
			if expected != nil {
				period = 1 / ratFloat(expected)
			}
			if math.Abs(expectedDur-out.Duration) > math.Max(.05, period*2.5) {
				p = append(p, fmt.Sprintf("duration mismatch: expected ~%.4fs got %.4fs", expectedDur, out.Duration))
			}
		}
	}
	return p
}
