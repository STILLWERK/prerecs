package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

func sourceClass(in MediaInfo) string {
	c := strings.ToLower(in.Codec)
	tag := strings.ToLower(in.CodecTag)
	if c == "mpeg4" && (tag == "xvid" || tag == "divx" || tag == "dx50") {
		return "compressed"
	}
	switch c {
	case "h264", "hevc", "h265", "av1", "vp9", "vp8", "mpeg4", "mpeg2video", "mpeg1video", "vc1", "wmv3", "msmpeg4v3",
		"wmv1", "wmv2", "h263", "h263p", "h263i", "flv1", "theora", "vp6", "vp6f", "vp6a", "cinepak":
		return "compressed"
		// MJPEG is deliberately absent: it is common as an acquisition/intermediate
		// codec (capture cards, HDMI recorders), not only as distribution media,
		// so it must not trigger the "already compressed" skip warning.
	case "lagarith", "magicyuv", "ffv1", "huffyuv", "utvideo", "rawvideo":
		return "lossless"
	case "prores", "dnxhd", "cfhd":
		return "intermediate"
	default:
		return "other"
	}
}

func isDistributionCompressed(in MediaInfo) bool { return sourceClass(in) == "compressed" }

func compressedInputs(infos []MediaInfo) []MediaInfo {
	out := []MediaInfo{}
	for _, in := range infos {
		if isDistributionCompressed(in) {
			out = append(out, in)
		}
	}
	return out
}

func recommendedProResPreset(infos []MediaInfo) string {
	for _, in := range infos {
		if in.HasAlpha {
			return "prores_4444"
		}
	}
	return "prores_lt"
}

func audioCopyCompatible(preset, codec string) bool {
	c := strings.ToLower(strings.TrimSpace(codec))
	if c == "" {
		return false
	}
	if strings.HasPrefix(preset, "xvid") || preset == "magicyuv_lossless" || preset == "utvideo_lossless" {
		return strings.HasPrefix(c, "pcm_") || c == "mp3" || c == "mp2"
	}
	if strings.HasPrefix(preset, "prores") {
		return strings.HasPrefix(c, "pcm_") || c == "aac" || c == "alac" || c == "mp3" || c == "ac3" || c == "eac3"
	}
	return false
}

func incompatibleAudioCopies(preset string, infos []MediaInfo) []string {
	bad := []string{}
	seen := map[string]bool{}
	for _, in := range infos {
		for _, a := range in.Audio {
			if audioCopyCompatible(preset, a) {
				continue
			}
			label := strictConsoleText(filepath.Base(in.Path)) + "=" + strictConsoleText(strings.ToUpper(a))
			if !seen[label] {
				seen[label] = true
				bad = append(bad, label)
			}
		}
	}
	return bad
}

func normalizePreset(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "share", "compact", "xvid", "xvid-q2":
		return "xvid_max_q2", nil
	case "xvid-efficient", "xvid-q2-efficient", "efficient":
		return "xvid_efficient_q2", nil
	case "xvid-fast", "xvid-compact", "fast", "compat", "compatibility":
		return "xvid_compact", nil
	case "edit", "prores-lt", "lt":
		return "prores_lt", nil
	case "prores", "422", "prores422":
		return "prores_422", nil
	case "max", "maximum", "hq", "prores-hq":
		return "prores_hq", nil
	case "xvid-max-q2":
		return "xvid_max_q2", nil
	case "xvid-small", "xvid-q3", "q3", "small":
		return "xvid_small", nil
	case "xvid-max", "xvid-q1", "q1":
		return "xvid_max", nil
	case "4444", "prores-4444":
		return "prores_4444", nil
	case "magicyuv", "magic":
		return "magicyuv_lossless", nil
	case "lossless":
		return "magicyuv_lossless", nil
	case "utvideo", "ut", "ut-video":
		return "utvideo_lossless", nil
	default:
		return "", fmt.Errorf("unknown preset %q", v)
	}
}

func validatePresetInputs(preset string, infos []MediaInfo) error {
	codec := ""
	switch preset {
	case "prores_lt", "prores_422", "prores_hq":
		for _, in := range infos {
			if in.HasAlpha {
				return fmt.Errorf("%s: source contains alpha; use ProRes 4444 to preserve it", filepath.Base(in.Path))
			}
		}
		return nil
	case "prores_4444":
		for _, in := range infos {
			if isGrayAlphaPixelFormat(in.PixelFormat) && in.BitDepth > 8 {
				return fmt.Errorf("%s: %s gray+alpha is not safely supported by the ProRes 4444 path; use an RGB/RGBA or supported YUVA source", filepath.Base(in.Path), in.PixelFormat)
			}
		}
		return nil
	case "magicyuv_lossless":
		codec = "MagicYUV"
	case "utvideo_lossless":
		codec = "Ut Video"
	default:
		return nil
	}
	for _, in := range infos {
		if _, err := lossless8BitPixFmt(in, codec); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(in.Path), err)
		}
	}
	return nil
}

// presetEncoderAvailable fails fast when a selected preset needs an encoder
// this FFmpeg/system does not have, instead of failing every item mid-batch.
// Xvid availability is validated per input by xvidAvailableForInputs.
func presetEncoderAvailable(caps Capabilities, enc map[string]bool, preset string) error {
	switch {
	case strings.HasPrefix(preset, "prores"):
		if !caps.HasProRes {
			return errors.New("the selected ProRes preset needs FFmpeg's prores_ks encoder, which this build does not include")
		}
	case preset == "magicyuv_lossless":
		if !enc["magicyuv"] {
			return errors.New("the MagicYUV preset needs FFmpeg's magicyuv encoder, which this build does not include")
		}
		if !caps.MagicInstalled {
			return errors.New("the MagicYUV preset needs a MagicYUV system/plugin installation, which was not detected")
		}
	case preset == "utvideo_lossless":
		if !enc["utvideo"] {
			return errors.New("the Ut Video preset needs FFmpeg's utvideo encoder, which this build does not include")
		}
	}
	return nil
}

// nativeXvidCaps returns caps with the native xvid_encraw path disabled when
// the user passed --no-native-xvid, so availability checks and backend
// descriptions treat libxvid as the only Xvid backend.
func nativeXvidCaps(caps Capabilities, noNative bool) Capabilities {
	if noNative {
		caps.HasNativeXvid = false
		caps.XvidEncRaw = ""
		// HasXvid aggregates both backends; with native masked off it must
		// collapse to the libxvid bit, or callers advertise Xvid that no
		// remaining backend can serve.
		caps.HasXvid = caps.HasLibXvid
	}
	return caps
}

func xvidAvailableForInputs(caps Capabilities, infos []MediaInfo, skipCompressed bool) (bool, []string) {
	missing := []string{}
	for _, in := range infos {
		if skipCompressed && isDistributionCompressed(in) {
			continue
		}
		if nativeXvidEligible(in) && caps.HasNativeXvid {
			continue
		}
		if caps.HasLibXvid {
			continue
		}
		missing = append(missing, strictConsoleText(filepath.Base(in.Path)))
	}
	return len(missing) == 0, missing
}

func xvidBackendDescription(caps Capabilities, infos []MediaInfo) string {
	if ok, missing := xvidAvailableForInputs(caps, infos, false); !ok {
		return "Xvid unavailable for " + strings.Join(missing, ", ") + "; use ProRes or install FFmpeg libxvid"
	}
	if caps.HasNativeXvid {
		nativeEligible := 0
		for _, in := range infos {
			if nativeXvidEligible(in) {
				nativeEligible++
			}
		}
		if nativeEligible == len(infos) {
			if caps.HasLibXvid {
				return "native Xvid when the VfW preflight passes, with verified FFmpeg fallback"
			}
			return "native Xvid when the VfW preflight passes; FFmpeg libxvid is not installed"
		}
		return "native Xvid where eligible, with verified FFmpeg fallback for the rest"
	}
	if caps.HasLibXvid {
		return "FFmpeg Xvid fallback"
	}
	return "Xvid unavailable; use ProRes or install FFmpeg libxvid"
}

func choosePreset(ui theme, e *Engine, infos []MediaInfo) (string, error) {
	for {
		fmt.Println(ui.bold("PRESET"))
		fmt.Println("  1. SHARE        Xvid Q2       " + ui.green("tuned VHQ4/B2 default"))
		if recommendedProResPreset(infos) == "prores_4444" {
			fmt.Println("  2. EDIT-READY   ProRes 422 LT " + ui.yellow("alpha sources require ProRes 4444"))
		} else {
			fmt.Println("  2. EDIT-READY   ProRes 422 LT " + ui.green("recommended"))
		}
		magicPrimary := "MagicYUV"
		magicCompatible := validatePresetInputs("magicyuv_lossless", infos) == nil
		if !(e.caps.HasMagicYUV && e.caps.MagicInstalled && magicCompatible) {
			magicPrimary += " (unavailable)"
		}
		fmt.Printf("  3. LOSSLESS     %-13s %s\n", magicPrimary, ui.dim("fast lossless intermediate"))
		fmt.Println("  4. MORE...")
		c, err := askChoice("Choose", []string{"1", "2", "3", "4"}, "2")
		if err != nil {
			return "", err
		}
		switch c {
		case "1":
			if !e.caps.HasXvid {
				fmt.Println(ui.yellow("  This FFmpeg build does not include libxvid."))
				fmt.Println()
				continue
			}
			if ok, missing := xvidAvailableForInputs(e.caps, infos, true); !ok {
				return "", fmt.Errorf("SHARE/Xvid is unavailable for %s: install FFmpeg libxvid or choose ProRes", strings.Join(missing, ", "))
			}
			return "xvid_max_q2", nil
		case "2":
			if !e.caps.HasProRes {
				fmt.Println(ui.yellow("  This FFmpeg build does not include prores_ks."))
				fmt.Println()
				continue
			}
			if err := validatePresetInputs("prores_lt", infos); err != nil {
				fmt.Println(ui.yellow("  " + strictConsoleText(err.Error())))
				fmt.Println()
				continue
			}
			return "prores_lt", nil
		case "3":
			if !(e.caps.HasMagicYUV && e.caps.MagicInstalled && magicCompatible) {
				fmt.Println(ui.yellow("  MagicYUV is not available. Install/licence MagicYUV and use an FFmpeg build with the encoder."))
				if err := validatePresetInputs("magicyuv_lossless", infos); err != nil {
					fmt.Println(ui.yellow("  " + strictConsoleText(err.Error())))
				}
				fmt.Println()
				continue
			}
			return "magicyuv_lossless", nil
		case "4":
			fmt.Println()
			fmt.Println(ui.bold("MORE PRESETS"))
			fmt.Println("  1. ProRes 422       quality step up from LT")
			fmt.Println("  2. ProRes 422 HQ    maximum normal ProRes tier")
			fmt.Println("  3. Xvid Q2 Efficient I/P Q2 + B Q3, ~6% smaller with tiny quality delta")
			fmt.Println("  4. Xvid Q2 Fast      old Compact VHQ1/B0, slightly faster")
			fmt.Println("  5. Xvid Q3 Small     smaller/faster than normal Share")
			fmt.Println("  6. Xvid Q1 Extreme   very large, highest Xvid quantizer quality")
			fmt.Println("  7. ProRes 4444       RGB/4:4:4/alpha sources only")
			ut := "unavailable"
			if e.caps.HasUtVideo {
				ut = "available"
			}
			fmt.Printf("  8. Ut Video Lossless (%s)\n", ut)
			fmt.Println("  0. Back")
			a, err := askChoice("Choose", []string{"0", "1", "2", "3", "4", "5", "6", "7", "8"}, "0")
			if err != nil {
				return "", err
			}
			switch a {
			case "1":
				if e.caps.HasProRes {
					if err := validatePresetInputs("prores_422", infos); err != nil {
						fmt.Println(ui.yellow("  " + strictConsoleText(err.Error())))
						fmt.Println()
						continue
					}
					return "prores_422", nil
				}
				fmt.Println(ui.yellow("  prores_ks is unavailable."))
				fmt.Println()
			case "2":
				if e.caps.HasProRes {
					if err := validatePresetInputs("prores_hq", infos); err != nil {
						fmt.Println(ui.yellow("  " + strictConsoleText(err.Error())))
						fmt.Println()
						continue
					}
					return "prores_hq", nil
				}
				fmt.Println(ui.yellow("  prores_ks is unavailable."))
				fmt.Println()
			case "3":
				if e.caps.HasXvid {
					if ok, missing := xvidAvailableForInputs(e.caps, infos, true); ok {
						return "xvid_efficient_q2", nil
					} else {
						return "", fmt.Errorf("xvid is unavailable for %s: install FFmpeg libxvid or choose ProRes", strings.Join(missing, ", "))
					}
				}
				fmt.Println(ui.yellow("  Xvid is unavailable."))
				fmt.Println()
			case "4":
				if e.caps.HasXvid {
					if ok, missing := xvidAvailableForInputs(e.caps, infos, true); ok {
						return "xvid_compact", nil
					} else {
						return "", fmt.Errorf("xvid is unavailable for %s: install FFmpeg libxvid or choose ProRes", strings.Join(missing, ", "))
					}
				}
				fmt.Println(ui.yellow("  Xvid is unavailable."))
				fmt.Println()
			case "5":
				if e.caps.HasXvid {
					if ok, missing := xvidAvailableForInputs(e.caps, infos, true); ok {
						return "xvid_small", nil
					} else {
						return "", fmt.Errorf("xvid is unavailable for %s: install FFmpeg libxvid or choose ProRes", strings.Join(missing, ", "))
					}
				}
				fmt.Println(ui.yellow("  Xvid is unavailable."))
				fmt.Println()
			case "6":
				if e.caps.HasXvid {
					if ok, missing := xvidAvailableForInputs(e.caps, infos, true); ok {
						return "xvid_max", nil
					} else {
						return "", fmt.Errorf("xvid is unavailable for %s: install FFmpeg libxvid or choose ProRes", strings.Join(missing, ", "))
					}
				}
				fmt.Println(ui.yellow("  Xvid is unavailable."))
				fmt.Println()
			case "7":
				if e.caps.HasProRes {
					return "prores_4444", nil
				}
				fmt.Println(ui.yellow("  prores_ks is unavailable."))
				fmt.Println()
			case "8":
				if e.caps.HasUtVideo && validatePresetInputs("utvideo_lossless", infos) == nil {
					return "utvideo_lossless", nil
				}
				if !e.caps.HasUtVideo {
					fmt.Println(ui.yellow("  FFmpeg build does not include the Ut Video encoder."))
				} else if err := validatePresetInputs("utvideo_lossless", infos); err != nil {
					fmt.Println(ui.yellow("  " + strictConsoleText(err.Error())))
				}
				fmt.Println()
			}
		}
	}
}

func presetLabel(p string) string {
	switch p {
	case "xvid_compact":
		return "Xvid Q2 Fast / Compatibility"
	case "xvid_max_q2":
		return "Share / Xvid Q2"
	case "xvid_efficient_q2":
		return "Xvid Q2 Efficient"
	case "xvid_small":
		return "Xvid Q3 Small"
	case "xvid_max":
		return "Xvid Q1 Extreme"
	case "prores_lt":
		return "Edit-ready / ProRes 422 LT"
	case "prores_422":
		return "Quality / ProRes 422"
	case "prores_hq":
		return "Maximum / ProRes 422 HQ"
	case "prores_4444":
		return "ProRes 4444"
	case "magicyuv_lossless":
		return "MagicYUV Lossless"
	case "utvideo_lossless":
		return "Ut Video Lossless"
	default:
		return p
	}
}

func isProResPreset(p string) bool {
	switch p {
	case "prores_lt", "prores_422", "prores_hq", "prores_4444":
		return true
	default:
		return false
	}
}
