package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
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
	return probeMediaBound(context.Background(), ffprobe, path, countFrames)
}

// probeMediaBound runs a metadata probe under the shared per-probe deadline
// while still honoring cancellation of the surrounding job — a wedged ffprobe
// stalls for at most mediaProbeTimeout rather than until the user gives up.
func probeMediaBound(ctx context.Context, ffprobe, path string, countFrames bool) (MediaInfo, error) {
	pctx, cancel := context.WithTimeout(ctx, mediaProbeTimeout)
	defer cancel()
	return probeMediaContext(pctx, ffprobe, path, countFrames)
}

// maxProbeJSONBytes bounds the JSON document buffered from ffprobe stdout. A
// hostile or pathological container can inflate metadata arbitrarily; the
// probe timeout bounds duration but cannot bound bytes on its own.
const maxProbeJSONBytes = 16 << 20

// maxPlausibleDuration is a generosity bound on container-reported duration
// in seconds (~100 years): past it, the metadata is impeached as corrupt and
// the duration treated as absent rather than trusted for verification math.
const maxPlausibleDuration = 100 * 365.25 * 24 * 3600

// cappedBuffer accumulates a stream up to a byte ceiling, then discards the
// rest while flagging truncation. stdout of a probe is consumed as a whole
// document, so unlike boundedTailWriter it cannot just keep the tail — a
// cut-off JSON body must be reported rather than mis-parsed.
type cappedBuffer struct {
	buf       bytes.Buffer
	truncated bool
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	if rem := maxProbeJSONBytes - w.buf.Len(); len(p) > rem {
		if rem > 0 {
			w.buf.Write(p[:rem])
		}
		w.truncated = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

func probeMediaContext(ctx context.Context, ffprobe, path string, countFrames bool) (MediaInfo, error) {
	args := []string{"-v", "error"}
	if countFrames {
		args = append(args, "-count_frames", "-count_packets")
	}
	args = append(args, "-show_streams", "-show_format", "-of", "json", path)
	cmd := exec.CommandContext(ctx, ffprobe, args...)
	cmd.WaitDelay = time.Second
	// stdout and stderr stay in separate sinks: CombinedOutput would interleave
	// non-fatal error lines into the JSON document and fail the parse.
	var stdout cappedBuffer
	var stderr boundedTailWriter
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) {
		if st := cmd.ProcessState; st != nil && st.ExitCode() == 0 {
			// ffprobe exited cleanly but a descendant kept its pipes open and
			// WaitDelay expired — the buffered JSON document is complete.
			err = nil
		}
	}
	if err != nil && ctx.Err() != nil {
		// A killed probe under an active cancellation reports the
		// cancellation, not the signal — same ordering as runFFmpeg. A clean
		// exit (err == nil) instead falls through: the buffered document is
		// complete and wins over a cancellation that raced it.
		return MediaInfo{}, ctx.Err()
	}
	if err != nil {
		// oneLine: the basename is untrusted text embedded in single-line
		// error rendering; newlines in it must not forge output lines.
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			// A Start failure or signal kill carries no diagnostics; surface
			// the underlying error instead of an empty "ffprobe:".
			return MediaInfo{}, fmt.Errorf("ffprobe %s: %w", oneLine(filepath.Base(path)), err)
		}
		return MediaInfo{}, fmt.Errorf("ffprobe %s: %s", oneLine(filepath.Base(path)), msg)
	}
	if stdout.truncated {
		return MediaInfo{}, fmt.Errorf("ffprobe %s: metadata output exceeded %d bytes", oneLine(filepath.Base(path)), maxProbeJSONBytes)
	}
	var doc ffprobeDoc
	if err := json.Unmarshal(stdout.buf.Bytes(), &doc); err != nil {
		return MediaInfo{}, fmt.Errorf("ffprobe %s: unparsable metadata output: %w", oneLine(filepath.Base(path)), err)
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
	// !(dur > 0) also rejects NaN and negatives, so a corrupt stream-level
	// duration cannot shadow a valid format-level one.
	if !(dur > 0) {
		dur = parseFloat(doc.Format.Duration)
	}
	// A malformed or hostile container can report NaN, Inf, or absurd finite
	// durations; they must not reach the frame estimate or the verification
	// comparison below (a claimed 1e300 s would guarantee a spurious mismatch
	// on an honest encode). Treat anything unusable as absent.
	if !(dur > 0 && dur <= maxPlausibleDuration) {
		dur = 0
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
		// Bound well inside int64's exact-integer range: values approaching
		// 2^63 can Round to an unrepresentable boundary.
		if est := dur * fpsFloat; est > 0 && est < 1<<62 {
			frames = int64(math.Round(est))
		}
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
