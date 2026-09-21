package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeriveBitDepthCommonPixelFormats(t *testing.T) {
	cases := map[string]int{
		"rgb24":       8,
		"bgr24":       8,
		"rgb48le":     16,
		"rgb48be":     16,
		"bgr48le":     16,
		"bgr48be":     16,
		"rgba":        8,
		"bgra":        8,
		"argb":        8,
		"abgr":        8,
		"rgba64le":    16,
		"rgba64be":    16,
		"bgra64le":    16,
		"bgra64be":    16,
		"yuv420p":     8,
		"yuv420p9le":  9,
		"yuv420p9be":  9,
		"yuv444p10le": 10,
		"yuv422p14le": 14,
		"gbrp":        8,
		"gbrp9le":     9,
		"gbrp12le":    12,
		"gbrp16le":    16,
		"gray":        8,
		"gray16le":    16,
		"ya8":         8,
		"ya16le":      16,
		"ya16be":      16,
		"yuvj411p":    8,
		"yuvj420p":    8,
		"yuvj422p":    8,
		"yuvj440p":    8,
		"yuvj444p":    8,
		"nv16":        8,
		"nv24":        8,
		"v308":        8,
		"v408":        8,
		"vuyx":        8,
		"vuya":        8,
		"uyva":        8,
		"ayuv":        8,
		"pal8":        8,
		"rgb0":        8,
		"bgr0":        8,
		"0rgb":        8,
		"0bgr":        8,
		"v210":        10,
		"v410":        10,
		"r210":        10,
		// These formats only ever appear with an endian suffix in ffprobe
		// output — test the names actually seen in the wild, including the
		// FFmpeg 8.x successors (xv30/xv36) and packed YUVA (y41x).
		"p010le":    10,
		"p210le":    10,
		"p210be":    10,
		"p410le":    10,
		"nv20le":    10,
		"v30xle":    10,
		"xv30le":    10,
		"x2rgb10le": 10,
		"x2bgr10le": 10,
		"y210le":    10,
		"y410le":    10,
		"p012le":    12,
		"p212le":    12,
		"p412le":    12,
		"y212le":    12,
		"y412le":    12,
		"xv36le":    12,
		"p016le":    16,
		"p216le":    16,
		"p416le":    16,
		"y216le":    16,
		"y416le":    16,
		"ayuv64le":  16,
	}
	for pixFmt, want := range cases {
		if got := deriveBitDepth(pixFmt); got != want {
			t.Errorf("deriveBitDepth(%q)=%d, want %d", pixFmt, got, want)
		}
	}
	for _, pixFmt := range []string{"mysteryfmt", "rgbunknown", "yuv420pfoo"} {
		if got := deriveBitDepth(pixFmt); got != 0 {
			t.Errorf("deriveBitDepth(%q)=%d, want unknown 0", pixFmt, got)
		}
	}
}

func TestUnknownBitDepthCannotPassLossless8BitGate(t *testing.T) {
	for _, codec := range []string{"MagicYUV", "Ut Video"} {
		if _, err := lossless8BitPixFmt(MediaInfo{PixelFormat: "mysteryfmt", BitDepth: 0, Chroma: "4:2:0"}, codec); err == nil {
			t.Fatalf("%s accepted unknown bit depth", codec)
		}
	}
}

func TestIntegrationUnreportedHighBitDepthFailsLosslessGate(t *testing.T) {
	caps, _, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	td := t.TempDir()
	src := filepath.Join(td, "rgb48.nut")
	if b, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=32x32:rate=1", "-frames:v", "1", "-pix_fmt", "rgb48le", "-c:v", "rawvideo", src).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, b)
	}
	info, err := probeMedia(caps.FFprobe, src, false)
	if err != nil {
		t.Fatal(err)
	}
	if info.BitDepth != 16 {
		t.Fatalf("rgb48le probe bit depth=%d, want 16", info.BitDepth)
	}
	for _, codec := range []string{"MagicYUV", "Ut Video"} {
		if err := validatePresetInputs(map[string]string{"MagicYUV": "magicyuv_lossless", "Ut Video": "utvideo_lossless"}[codec], []MediaInfo{info}); err == nil {
			t.Fatalf("%s gate accepted unreported high-bit-depth source", codec)
		}
	}
}

func TestHasAlphaRecognizesPackedARGBFormats(t *testing.T) {
	for _, pixFmt := range []string{"rgba", "bgra", "argb", "abgr", "yuva420p", "yuva444p", "gbrap", "gbraf16le", "ya8", "ya16le", "ya16be", "v408", "vuya", "uyva", "ayuv", "ayuv64le", "y410le", "y412le", "y416le", "pal8"} {
		if !hasAlpha(pixFmt) {
			t.Fatalf("hasAlpha(%q)=false, want true", pixFmt)
		}
	}
	// The packed siblings carry X padding, not alpha.
	for _, pixFmt := range []string{"rgb24", "bgr24", "yuv420p", "yuv444p", "v308", "vuyx", "v30xle", "xv30le", "xv36le", "y210le", "y212le", "y216le", "x2rgb10le", "x2bgr10le"} {
		if hasAlpha(pixFmt) {
			t.Fatalf("hasAlpha(%q)=true, want false", pixFmt)
		}
	}
}

func TestPackedRGBFormatsUseRGBConversionClassification(t *testing.T) {
	for _, pixFmt := range []string{"argb", "abgr", "rgba", "bgra", "gbrap", "0rgb", "0bgr", "x2rgb10le", "x2bgr10le", "r210"} {
		if !isRGBPixelFormat(pixFmt) {
			t.Fatalf("isRGBPixelFormat(%q)=false, want true", pixFmt)
		}
		if got := chroma(pixFmt); got != "4:4:4" {
			t.Fatalf("chroma(%q)=%q, want 4:4:4", pixFmt, got)
		}
	}
}

func TestLossless8BitPixFmtPreservesLayout(t *testing.T) {
	cases := []struct {
		name  string
		info  MediaInfo
		codec string
		want  string
	}{
		{"yuv420", MediaInfo{PixelFormat: "yuv420p", BitDepth: 8, Chroma: "4:2:0"}, "MagicYUV", "yuv420p"},
		{"yuv422", MediaInfo{PixelFormat: "yuv422p", BitDepth: 8, Chroma: "4:2:2"}, "MagicYUV", "yuv422p"},
		{"yuv444", MediaInfo{PixelFormat: "yuv444p", BitDepth: 8, Chroma: "4:4:4"}, "MagicYUV", "yuv444p"},
		{"rgb", MediaInfo{PixelFormat: "gbrp", BitDepth: 8, Chroma: "4:4:4"}, "MagicYUV", "gbrp"},
		{"rgba", MediaInfo{PixelFormat: "gbrap", BitDepth: 8, Chroma: "4:4:4", HasAlpha: true}, "MagicYUV", "gbrap"},
		{"yuva444", MediaInfo{PixelFormat: "yuva444p", BitDepth: 8, Chroma: "4:4:4", HasAlpha: true}, "MagicYUV", "yuva444p"},
		{"ut-yuv444", MediaInfo{PixelFormat: "yuv444p", BitDepth: 8, Chroma: "4:4:4"}, "Ut Video", "yuv444p"},
		{"mjpeg-fullrange", MediaInfo{PixelFormat: "yuvj420p", BitDepth: 8, Chroma: "4:2:0"}, "MagicYUV", "yuv420p"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lossless8BitPixFmt(tc.info, tc.codec)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestLossless8BitPixFmtRejectsHigherBitDepth(t *testing.T) {
	for _, codec := range []string{"MagicYUV", "Ut Video"} {
		_, err := lossless8BitPixFmt(MediaInfo{PixelFormat: "yuv420p10le", BitDepth: 10, Chroma: "4:2:0"}, codec)
		if err == nil || !strings.Contains(err.Error(), "10-bit") {
			t.Fatalf("%s should reject 10-bit source, err=%v", codec, err)
		}
	}
}

func TestColorOutputArgsDropsGBRForYUVEncoders(t *testing.T) {
	in := MediaInfo{ColorSpace: "gbr", ColorRange: "pc", ColorTransfer: "bt709", ColorPrimaries: "bt709"}
	for _, preset := range []string{"xvid_compact", "xvid_max_q2", "xvid_efficient_q2", "xvid_small", "xvid_max", "prores_lt", "prores_422", "prores_hq", "prores_4444"} {
		joined := strings.Join(colorOutputArgs(in, preset), " ")
		if strings.Contains(joined, "-colorspace gbr") {
			t.Fatalf("%s propagated invalid GBR matrix to YUV encoder: %s", preset, joined)
		}
	}
	joined := " " + strings.Join(colorOutputArgs(in, "prores_4444"), " ") + " "
	if !strings.Contains(joined, " -color_range tv ") || !strings.Contains(joined, " -colorspace bt709 ") {
		t.Fatalf("RGB ProRes output must describe converted YUV planes: %s", joined)
	}
	for _, preset := range []string{"magicyuv_lossless", "utvideo_lossless"} {
		joined := strings.Join(colorOutputArgs(in, preset), " ")
		if !strings.Contains(joined, "-colorspace gbr") {
			t.Fatalf("%s should preserve GBR metadata for RGB-capable lossless output: %s", preset, joined)
		}
	}
}

func TestBuildCPUProResRGBAlphaPreservesAlphaGraph(t *testing.T) {
	e := &Engine{caps: Capabilities{}, enc: map[string]bool{"prores_ks": true}}
	info := MediaInfo{
		Path: "rgb-alpha.mkv", FPS: "30/1", FPSFloat: 30, FrameCount: 30, FrameCountExact: true, Duration: 1,
		PixelFormat: "gbrap", ColorSpace: "gbr", ColorRange: "pc", Chroma: "4:4:4", BitDepth: 8, HasAlpha: true,
	}
	args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "prores_4444"}, "out.mov")
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(args, " ") + " "
	for _, want := range []string{
		"split=2[c][a];[c]format=gbrp,scale=out_color_matrix=bt709:out_range=tv,format=yuv444p10le[c10];[a]alphaextract,format=gray[a8];[c10][a8]alphamerge",
		" -pix_fmt yuva444p10le ", " -alpha_bits 8 ", " -color_range tv ", " -colorspace bt709 ",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
}

func TestIntegrationProResRGBAlphaRoundTrip(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["prores_ks"] || !enc["ffv1"] {
		t.Skip("prores_ks/ffv1 unavailable")
	}
	td := t.TempDir()
	src := filepath.Join(td, "rgb-alpha.mkv")
	out := filepath.Join(td, "out.mov")
	// Build a non-constant alpha gradient so a conversion that modifies alpha
	// cannot accidentally pass this test because every pixel is opaque.
	filter := "[0:v]format=gbrp[base];[1:v]format=gray[a];[base][a]alphamerge,format=gbrap"
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-f", "lavfi", "-i", "nullsrc=size=160x90:rate=30,format=gray,geq=lum=X/W*255",
		"-filter_complex", filter, "-frames:v", "8", "-c:v", "ffv1", "-level", "3", "-pix_fmt", "gbrap", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, b)
	}
	info, err := probeMedia(caps.FFprobe, src, true)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{caps: caps, enc: enc}
	args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "prores_4444", StripAudio: true}, out)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(caps.FFmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("encode: %v %s", err, b)
	}
	alphaMD5 := func(path string) string {
		b, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-vf", "alphaextract,format=gray", "-f", "md5", "-").CombinedOutput()
		if err != nil {
			t.Fatalf("alpha md5 %s: %v %s", path, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	if a, b := alphaMD5(src), alphaMD5(out); a != b {
		t.Fatalf("alpha changed: src=%s out=%s", a, b)
	}
}

func TestIntegrationProResPackedRGBAlphaRoundTrip(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["prores_ks"] || !enc["ffv1"] {
		t.Skip("prores_ks/ffv1 unavailable")
	}
	td := t.TempDir()
	for _, pixFmt := range []string{"argb", "abgr"} {
		t.Run(pixFmt, func(t *testing.T) {
			src := filepath.Join(td, pixFmt+".nut")
			out := filepath.Join(td, pixFmt+".mov")
			filter := fmt.Sprintf("[0:v]format=rgb24[base];[1:v]format=gray[a];[base][a]alphamerge,format=%s", pixFmt)
			if b, err := exec.Command(caps.FFmpeg,
				"-hide_banner", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
				"-f", "lavfi", "-i", "nullsrc=size=160x90:rate=30,format=gray,geq=lum=X/W*255",
				"-filter_complex", filter, "-frames:v", "8", "-c:v", "rawvideo", "-pix_fmt", pixFmt, src,
			).CombinedOutput(); err != nil {
				t.Fatalf("source: %v %s", err, b)
			}
			info, err := probeMedia(caps.FFprobe, src, true)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.EqualFold(info.PixelFormat, pixFmt) || !info.HasAlpha {
				t.Fatalf("source format/alpha=%s/%t", info.PixelFormat, info.HasAlpha)
			}
			e := &Engine{caps: caps, enc: enc}
			args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "prores_4444", StripAudio: true}, out)
			if err != nil {
				t.Fatal(err)
			}
			if b, err := exec.Command(caps.FFmpeg, args...).CombinedOutput(); err != nil {
				t.Fatalf("encode: %v %s", err, b)
			}
			alphaMD5 := func(path string) string {
				b, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-vf", "alphaextract,format=gray", "-f", "md5", "-").CombinedOutput()
				if err != nil {
					t.Fatalf("alpha md5 %s: %v %s", path, err, b)
				}
				return strings.TrimSpace(string(b))
			}
			if source, output := alphaMD5(src), alphaMD5(out); source != output {
				t.Fatalf("alpha changed: source=%s output=%s", source, output)
			}
		})
	}
}

func TestIntegrationProResGrayAlphaRoundTrip(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["prores_ks"] || !enc["ffv1"] {
		t.Skip("prores_ks/ffv1 unavailable")
	}
	td := t.TempDir()
	src := filepath.Join(td, "gray-alpha.nut")
	out := filepath.Join(td, "gray-alpha.mov")
	filter := "[0:v]format=gray[base];[1:v]format=gray[a];[base][a]alphamerge,format=ya8"
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-f", "lavfi", "-i", "nullsrc=size=160x90:rate=30,format=gray,geq=lum=X/W*255",
		"-filter_complex", filter, "-frames:v", "8", "-c:v", "rawvideo", "-pix_fmt", "ya8", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("source: %v %s", err, b)
	}
	info, err := probeMedia(caps.FFprobe, src, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(info.PixelFormat, "ya8") || !info.HasAlpha {
		t.Fatalf("source format/alpha=%s/%t", info.PixelFormat, info.HasAlpha)
	}
	if err := validatePresetInputs("prores_lt", []MediaInfo{info}); err == nil {
		t.Fatal("ProRes LT accepted gray+alpha input")
	}
	e := &Engine{caps: caps, enc: enc}
	args, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "prores_4444", StripAudio: true}, out)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := exec.Command(caps.FFmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("encode: %v %s", err, b)
	}
	alphaMD5 := func(path string) string {
		b, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-i", path, "-vf", "alphaextract,format=gray", "-f", "md5", "-").CombinedOutput()
		if err != nil {
			t.Fatalf("alpha md5 %s: %v %s", path, err, b)
		}
		return strings.TrimSpace(string(b))
	}
	if source, output := alphaMD5(src), alphaMD5(out); source != output {
		t.Fatalf("gray alpha changed: source=%s output=%s", source, output)
	}
}

func TestProResAlphaBits(t *testing.T) {
	if got := proresAlphaBits(MediaInfo{}); got != 0 {
		t.Fatalf("no alpha bits=%d", got)
	}
	if got := proresAlphaBits(MediaInfo{HasAlpha: true, BitDepth: 8}); got != 8 {
		t.Fatalf("8-bit alpha bits=%d", got)
	}
	if got := proresAlphaBits(MediaInfo{HasAlpha: true, BitDepth: 10}); got != 16 {
		t.Fatalf("10-bit alpha bits=%d", got)
	}
}
