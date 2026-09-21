package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"time"
)

type ffprobeDoc struct {
	Streams []struct {
		CodecName        string `json:"codec_name"`
		Profile          string `json:"profile"`
		CodecType        string `json:"codec_type"`
		Width            int    `json:"width"`
		Height           int    `json:"height"`
		PixFmt           string `json:"pix_fmt"`
		BitsPerRawSample string `json:"bits_per_raw_sample"`
		AvgFrameRate     string `json:"avg_frame_rate"`
		RFrameRate       string `json:"r_frame_rate"`
		Duration         string `json:"duration"`
		NBFrames         string `json:"nb_frames"`
		NBReadFrames     string `json:"nb_read_frames"`
		CodecTagString   string `json:"codec_tag_string"`
		ColorRange       string `json:"color_range"`
		ColorSpace       string `json:"color_space"`
		ColorTransfer    string `json:"color_transfer"`
		ColorPrimaries   string `json:"color_primaries"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		Size     string `json:"size"`
	} `json:"format"`
}

func metadataFrameCountTrusted(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "lagarith", "magicyuv", "ffv1", "huffyuv", "utvideo", "rawvideo", "prores", "dnxhd", "cfhd":
		return true
	default:
		return false
	}
}

var mediaProbeTimeout = 30 * time.Second

// probeMedia's countFrames mode is for diagnostics and integration fixtures.
// Conversion integrity uses Engine.countDecodedFrames instead because it makes
// decoder errors fatal; an ffprobe frame count is not an integrity check.
func probeMedia(ffprobe, path string, countFrames bool) (MediaInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mediaProbeTimeout)
	defer cancel()
	return probeMediaContext(ctx, ffprobe, path, countFrames)
}

func probeMediaContext(ctx context.Context, ffprobe, path string, countFrames bool) (MediaInfo, error) {
	args := []string{"-v", "error"}
	if countFrames {
		args = append(args, "-count_frames", "-count_packets")
	}
	args = append(args, "-show_streams", "-show_format", "-of", "json", path)
	cmd := exec.CommandContext(ctx, ffprobe, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return MediaInfo{}, ctx.Err()
	}
	if err != nil {
		return MediaInfo{}, fmt.Errorf("ffprobe: %s", strings.TrimSpace(string(out)))
	}
	var doc ffprobeDoc
	if err := json.Unmarshal(out, &doc); err != nil {
		return MediaInfo{}, err
	}
	vi := -1
	for i := range doc.Streams {
		if doc.Streams[i].CodecType == "video" {
			vi = i
			break
		}
	}
	if vi < 0 {
		return MediaInfo{}, errors.New("no video stream found")
	}
	sv := doc.Streams[vi]
	dur := parseFloat(sv.Duration)
	if dur == 0 {
		dur = parseFloat(doc.Format.Duration)
	}
	frames := parseInt64(sv.NBFrames)
	// Compressed community files frequently carry stale AVI/container frame
	// tables. Treat metadata counts as exact only for codecs whose frame tables
	// are dependable in this workflow. Distribution codecs get a visible exact
	// decode scan before conversion instead of trusting nb_frames blindly.
	frameCountExact := frames > 0 && metadataFrameCountTrusted(sv.CodecName)
	if countFrames && parseInt64(sv.NBReadFrames) > 0 {
		frames = parseInt64(sv.NBReadFrames)
		frameCountExact = true
	}
	// Corroborate the r_frame_rate fallback only with trusted frame counts.
	// Compressed containers can carry stale frame tables (the reason they get
	// an exact decode scan at all); an untrusted count must neither confirm
	// nor deny the fallback, so it is re-checked after the scan instead.
	framesForCorrob := frames
	if !frameCountExact {
		framesForCorrob = 0
	}
	fpsStr, fpsRat := selectFrameRate(sv.AvgFrameRate, sv.RFrameRate, framesForCorrob, dur)
	if fpsRat == nil && framesForCorrob > 0 && dur > 0 && (validRate(sv.AvgFrameRate) || validRate(sv.RFrameRate)) {
		// A rate existed but trusted count/duration impeached it — the
		// metadata is inconsistent without saying which field lied, so the
		// duration is suspect too. Clearing it keeps verification from
		// comparing honest passthrough timing against a stale expectation.
		dur = 0
	}
	fpsFloat := 0.0
	if fpsRat != nil {
		fpsFloat = ratFloat(fpsRat)
	}
	if frames <= 0 && dur > 0 && fpsFloat > 0 {
		frames = int64(math.Round(dur * fpsFloat))
		frameCountExact = false
	}
	bit := parseInt(sv.BitsPerRawSample)
	if bit == 0 {
		bit = deriveBitDepth(sv.PixFmt)
	}
	info := MediaInfo{Path: path, Codec: sv.CodecName, CodecTag: sv.CodecTagString, Profile: sv.Profile, Width: sv.Width, Height: sv.Height, PixelFormat: sv.PixFmt, BitDepth: bit, FPS: fpsStr, FPSFloat: fpsFloat, Duration: dur, FrameCount: frames, FrameCountExact: frameCountExact, SizeBytes: parseInt64(doc.Format.Size), ColorRange: sv.ColorRange, ColorSpace: sv.ColorSpace, ColorTransfer: sv.ColorTransfer, ColorPrimaries: sv.ColorPrimaries, HasAlpha: hasAlpha(sv.PixFmt), Chroma: chroma(sv.PixFmt), Audio: []string{}}
	for _, sa := range doc.Streams {
		if sa.CodecType != "audio" {
			continue
		}
		info.Audio = append(info.Audio, sa.CodecName)
	}
	return info, nil
}
