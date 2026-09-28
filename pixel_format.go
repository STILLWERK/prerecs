package main

import (
	"fmt"
	"strconv"
	"strings"
)

func deriveBitDepth(p string) int {
	low := strings.ToLower(strings.TrimSpace(p))
	if low == "" {
		return 0
	}

	for _, prefix := range []string{
		"yuva420p", "yuva422p", "yuva444p",
		"yuv420p", "yuv422p", "yuv444p", "yuv440p", "yuv411p", "yuv410p",
		"gbrap", "gbrp", "gray", "ya",
	} {
		if !strings.HasPrefix(low, prefix) {
			continue
		}
		rest := low[len(prefix):]
		if rest == "" {
			return 8
		}
		if rest[0] < '0' || rest[0] > '9' {
			return 0
		}
		end := 0
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		depth, err := strconv.Atoi(rest[:end])
		if err != nil || depth <= 0 {
			return 0
		}
		return depth
	}

	for _, prefix := range []string{"rgb48", "bgr48", "rgba64", "bgra64", "argb64", "abgr64"} {
		if strings.HasPrefix(low, prefix) && validPackedSuffix(low[len(prefix):]) {
			return 16
		}
	}
	for _, prefix := range []string{"rgb24", "bgr24"} {
		if strings.HasPrefix(low, prefix) && validPackedSuffix(low[len(prefix):]) {
			return 8
		}
	}
	for _, prefix := range []string{"rgba", "bgra", "argb", "abgr"} {
		if low == prefix {
			return 8
		}
	}
	for _, pixFmt := range []string{"nv12", "nv21", "uyvy422", "yuyv422", "vyuy422", "yuv422p"} {
		if low == pixFmt {
			return 8
		}
	}
	// Full-range planar YUV as emitted by FFmpeg's MJPEG and J-family codecs,
	// plus packed/semi-planar 8-bit layouts that carry no depth suffix —
	// including the FFmpeg 8.x successors (vuyx ≙ v308, vuya ≙ v408).
	for _, pixFmt := range []string{"yuvj411p", "yuvj420p", "yuvj422p", "yuvj440p", "yuvj444p", "nv16", "nv24", "v308", "v408", "vuyx", "vuya", "uyva", "ayuv", "pal8", "rgb0", "bgr0", "0rgb", "0bgr"} {
		if low == pixFmt {
			return 8
		}
	}
	// Packed high-bit formats whose names do not carry the depth as a suffix;
	// reporting the real depth keeps the >8-bit rejection message accurate.
	for _, pixFmt := range []string{"v210", "v410", "r210"} {
		if low == pixFmt {
			return 10
		}
	}
	// 16-bit packed formats whose names carry no depth digits.
	for _, pixFmt := range []string{"v216", "v216le", "v216be"} {
		if low == pixFmt {
			return 16
		}
	}
	// Float planar formats (EXR/HDR decodes) report no numeric depth in the
	// name; without this they collapse to BitDepth 0 and the conversion pins
	// silently crush them through the 8-bit path.
	for _, pixFmt := range []string{"gbrpf32le", "gbrpf32be", "gbrapf32le", "gbrapf32be", "grayf32le", "grayf32be", "rgbf32le", "rgbf32be", "rgbaf32le", "rgbaf32be", "yaf32le", "yaf32be"} {
		if low == pixFmt {
			return 32
		}
	}
	// These descriptors always carry an endian suffix in ffprobe output
	// (p210le/be, x2rgb10le/be, ...); the bare enum name never appears.
	for _, prefix := range []string{"p010", "p210", "p410", "nv20", "v30x", "xv30", "x2rgb10", "x2bgr10", "y210", "y410"} {
		if strings.HasPrefix(low, prefix) && validPackedSuffix(low[len(prefix):]) {
			return 10
		}
	}
	for _, prefix := range []string{"p012", "p212", "p412", "y212", "y412", "xv36"} {
		if strings.HasPrefix(low, prefix) && validPackedSuffix(low[len(prefix):]) {
			return 12
		}
	}
	for _, prefix := range []string{"p016", "p216", "p416", "y216", "y416", "ayuv64"} {
		if strings.HasPrefix(low, prefix) && validPackedSuffix(low[len(prefix):]) {
			return 16
		}
	}
	return 0
}

func validPackedSuffix(suffix string) bool {
	return suffix == "" || suffix == "le" || suffix == "be"
}

func hasAlpha(p string) bool {
	low := strings.ToLower(p)
	// Packed/palette formats whose pixdesc sets AV_PIX_FMT_FLAG_ALPHA. The
	// padded siblings (vuyx, v30x, xv30, xv36) carry X bits, not alpha.
	// v410 is FFmpeg ≤7.x's packed 10-bit YUVA (the enum was dropped in 8).
	switch low {
	case "v408", "vuya", "uyva", "pal8", "v410":
		return true
	}
	for _, prefix := range []string{"rgba", "bgra", "argb", "abgr", "yuva", "ayuv", "gbraf", "y410", "y412", "y416"} {
		if strings.HasPrefix(low, prefix) {
			return true
		}
	}
	return strings.Contains(low, "gbrap") || isGrayAlphaPixelFormat(low)
}

func isGrayAlphaPixelFormat(p string) bool {
	low := strings.ToLower(strings.TrimSpace(p))
	return strings.HasPrefix(low, "ya8") || strings.HasPrefix(low, "ya16")
}

func isRGBPixelFormat(p string) bool {
	low := strings.ToLower(strings.TrimSpace(p))
	for _, prefix := range []string{"rgb", "bgr", "gbr", "argb", "abgr", "0rgb", "0bgr", "x2rgb10", "x2bgr10", "r210"} {
		if strings.HasPrefix(low, prefix) {
			return true
		}
	}
	return false
}

func chroma(p string) string {
	low := strings.ToLower(p)
	if strings.Contains(low, "444") || isRGBPixelFormat(p) {
		return "4:4:4"
	}
	if strings.Contains(low, "422") {
		return "4:2:2"
	}
	if strings.Contains(low, "420") {
		return "4:2:0"
	}
	// Semi-planar and packed layouts carry no "4xx" digits in their names, so
	// the substring scan above misses them; without these entries the lossless
	// presets refuse common captures (nv12/nv16/nv24, packed 4:2:2/4:4:4) that
	// repack losslessly into planar.
	switch low {
	case "nv12", "nv21", "p010", "p012", "p016", "p010le", "p010be", "p012le", "p012be", "p016le", "p016be":
		return "4:2:0"
	case "nv16", "p210", "p212", "p216", "p210le", "p210be", "p212le", "p212be", "p216le", "p216be", "nv20", "nv20le", "nv20be", "y210", "y210le", "y210be", "y212", "y212le", "y212be", "y216", "y216le", "y216be", "yuyv422", "uyvy422", "vyuy422", "v210", "v216", "v216le", "v216be":
		return "4:2:2"
	case "nv24", "nv42", "p410", "p412", "p416", "p410le", "p410be", "p412le", "p412be", "p416le", "p416be", "v308", "v408", "vuyx", "vuya", "uyva", "ayuv", "v410", "y410", "y410le", "y410be", "y412", "y412le", "y412be", "y416", "y416le", "y416be", "v30x", "v30xle", "v30xbe", "xv30", "xv30le", "xv30be", "xv36", "xv36le", "xv36be":
		return "4:4:4"
	}
	return ""
}

func lossless8BitPixFmt(info MediaInfo, codec string) (string, error) {
	if info.BitDepth <= 0 {
		return "", fmt.Errorf("%s cannot establish the source bit depth for pixel format %s; refusing an unsafe 8-bit lossless conversion", codec, info.PixelFormat)
	}
	if info.BitDepth > 8 {
		return "", fmt.Errorf("%s through this FFmpeg build supports only the tested 8-bit pixel formats; source is %d-bit. Use ProRes 422/4444 instead", codec, info.BitDepth)
	}
	if info.HasAlpha {
		if isGrayAlphaPixelFormat(info.PixelFormat) {
			return "", fmt.Errorf("%s cannot preserve gray+alpha source pixel format %s; use ProRes 4444 instead", codec, info.PixelFormat)
		}
		if isRGBPixelFormat(info.PixelFormat) {
			return "gbrap", nil
		}
		if codec == "MagicYUV" && info.Chroma == "4:4:4" {
			return "yuva444p", nil
		}
		return "", fmt.Errorf("%s cannot preserve this source's %s alpha layout without changing chroma; use ProRes 4444 instead", codec, info.PixelFormat)
	}
	if isRGBPixelFormat(info.PixelFormat) {
		return "gbrp", nil
	}
	switch info.Chroma {
	case "4:2:0":
		return "yuv420p", nil
	case "4:2:2":
		return "yuv422p", nil
	case "4:4:4":
		return "yuv444p", nil
	}
	if codec == "MagicYUV" && strings.HasPrefix(strings.ToLower(info.PixelFormat), "gray") {
		return "gray", nil
	}
	return "", fmt.Errorf("%s cannot preserve source pixel format %s exactly", codec, info.PixelFormat)
}

func proresAlphaBits(info MediaInfo) int {
	if !info.HasAlpha {
		return 0
	}
	// The ProRes bitstream signals alpha precision in 8-bit units. Match an
	// 8-bit source with 8-bit alpha so the conversion into yuva444p10le does not
	// unnecessarily rescale the alpha plane. Higher-bit-depth alpha uses the
	// 16-bit lossless mode supported by ProRes 4444.
	if info.BitDepth > 0 && info.BitDepth <= 8 {
		return 8
	}
	return 16
}

func proresPixelFormat(info MediaInfo, preset string) string {
	if preset != "prores_4444" {
		return "yuv422p10le"
	}
	// >8-bit sources keep their extra precision through the 12-bit encoder
	// target (the 4444 bitstream ceiling); pinning 10le would quantize them
	// to the 8-bit-converted chain's precision instead.
	if info.BitDepth > 8 {
		if info.HasAlpha {
			return "yuva444p12le"
		}
		return "yuv444p12le"
	}
	if info.HasAlpha {
		return "yuva444p10le"
	}
	return "yuv444p10le"
}

// proresPlanarPin returns the planar GBR/gray format that keeps source
// precision for the conversion chain: 16-bit for >8-bit sources, the classic
// 8-bit pins otherwise. Pinning the 8-bit variants unconditionally crushed
// high-bit RGB/alpha planes to 256 levels before the encoder saw them.
func proresPlanarPin(info MediaInfo, alpha bool) string {
	if alpha {
		if info.BitDepth > 8 {
			return "gray16le"
		}
		return "gray"
	}
	if info.BitDepth > 8 {
		return "gbrp16le"
	}
	return "gbrp"
}

// proresColorPixFmt is the colour-planes target inside the alpha-split chains —
// alphamerge reattaches the separately carried alpha, so the colour branch
// itself must not carry an alpha channel.
func proresColorPixFmt(info MediaInfo) string {
	if info.BitDepth > 8 {
		return "yuv444p12le"
	}
	return "yuv444p10le"
}

func proresRGBConversionFilters(info MediaInfo, preset string) []string {
	if !isProResPreset(preset) {
		return nil
	}
	if preset == "prores_4444" && isGrayAlphaPixelFormat(info.PixelFormat) {
		// Gray+alpha has no colour planes for the RGB conversion branch, but its
		// alpha plane still must bypass the gray-to-YUV conversion unchanged.
		return []string{
			"split=2[c][a];[c]format=" + proresPlanarPin(info, false) + ",format=" + proresColorPixFmt(info) + "[cX];[a]alphaextract,format=" + proresPlanarPin(info, true) + "[aX];[cX][aX]alphamerge",
		}
	}
	if !(isRGBPixelFormat(info.PixelFormat) || strings.EqualFold(info.ColorSpace, "gbr")) {
		return nil
	}
	pix := proresPixelFormat(info, preset)
	if preset == "prores_4444" && info.HasAlpha {
		// Do not let swscale touch the alpha plane while converting RGB to YUV.
		// A direct gbrap -> yuva444p10le conversion rescales 8-bit alpha values,
		// which breaks ProRes 4444's lossless-alpha guarantee. Convert only the
		// colour planes, carry alpha separately, then merge it back immediately
		// before the encoder. With -alpha_bits 8 an 8-bit alpha source round-trips
		// byte-for-byte through both prores_ks and prores_ks_vulkan.
		return []string{
			"split=2[c][a];[c]format=" + proresPlanarPin(info, false) + ",scale=out_color_matrix=bt709:out_range=tv,format=" + proresColorPixFmt(info) + "[cX];[a]alphaextract,format=" + proresPlanarPin(info, true) + "[aX];[cX][aX]alphamerge",
		}
	}
	return []string{"format=" + proresPlanarPin(info, false), "scale=out_color_matrix=bt709:out_range=tv", "format=" + pix}
}

// xvidRGBConversionFilters mirrors the ProRes RGB path for Xvid presets: Xvid
// encodes 4:2:0 YUV and a bare -pix_fmt leaves swscale to pick a default
// matrix (BT.601-range), visibly shifting colour versus the BT.709 conversion
// the ProRes presets emit for the same source.
func xvidRGBConversionFilters(info MediaInfo, preset string) []string {
	if !isXvidPreset(preset) {
		return nil
	}
	if !(isRGBPixelFormat(info.PixelFormat) || strings.EqualFold(info.ColorSpace, "gbr")) {
		return nil
	}
	return []string{"format=" + proresPlanarPin(info, false), "scale=out_color_matrix=bt709:out_range=tv", "format=yuv420p"}
}

// Known FFmpeg color metadata enum names, in the spellings ffprobe actually
// emits (av_color_space_name/av_color_transfer_name/av_color_primaries_name —
// not the AVOption aliases, which differ: ffprobe says "iec61966-2-1" and
// "bt2020-10", never "iec61966_2_1"/"bt2020_10"). ffprobe reports these
// verbatim and they are interpolated into filtergraph strings, so
// unrecognized spellings are dropped rather than trusted — a surprising value
// must never become filter syntax.
var (
	validColorSpaces = map[string]bool{
		// gbr is ffprobe's reported colorspace for RGB-matrix sources; the
		// output side deliberately drops it for YUV encoders (colorOutputArgs).
		"gbr": true, "bt709": true, "fcc": true, "bt470bg": true,
		"smpte170m": true, "smpte240m": true, "ycgco": true,
		"bt2020nc": true, "bt2020c": true, "smpte2085": true,
		"chroma-derived-nc": true, "chroma-derived-c": true, "ictcp": true,
		// Emitted by FFmpeg ≥7.2; accepted here because the probing ffprobe
		// and the encoding ffmpeg are the same build.
		"ipt-c2": true, "ycgco-re": true, "ycgco-ro": true,
	}
	validColorTransfers = map[string]bool{
		"bt709": true, "bt470m": true, "bt470bg": true, "smpte170m": true,
		"smpte240m": true, "linear": true, "log100": true, "log316": true,
		"iec61966-2-4": true, "bt1361e": true, "iec61966-2-1": true,
		"bt2020-10": true, "bt2020-12": true, "smpte2084": true,
		"smpte428": true, "arib-std-b67": true,
	}
	validColorPrimaries = map[string]bool{
		"bt709": true, "bt470m": true, "bt470bg": true, "smpte170m": true,
		"smpte240m": true, "film": true, "bt2020": true, "smpte428": true,
		"smpte431": true, "smpte432": true, "jedec-p22": true, "ebu3213": true,
	}
)

func colorFilter(i MediaInfo) string {
	p := []string{}
	if i.ColorRange == "tv" {
		p = append(p, "range=limited")
	} else if i.ColorRange == "pc" {
		p = append(p, "range=full")
	}
	if i.ColorSpace != "" && i.ColorSpace != "unknown" && validColorSpaces[strings.ToLower(i.ColorSpace)] {
		p = append(p, "colorspace="+i.ColorSpace)
	}
	if i.ColorTransfer != "" && i.ColorTransfer != "unknown" && validColorTransfers[strings.ToLower(i.ColorTransfer)] {
		p = append(p, "color_trc="+i.ColorTransfer)
	}
	if i.ColorPrimaries != "" && i.ColorPrimaries != "unknown" && validColorPrimaries[strings.ToLower(i.ColorPrimaries)] {
		p = append(p, "color_primaries="+i.ColorPrimaries)
	}
	if len(p) == 0 {
		return ""
	}
	return "setparams=" + strings.Join(p, ":")
}

func colorOutputArgs(i MediaInfo, preset string) []string {
	a := []string{}
	// ProRes and Xvid both encode YUV in this build; RGB/GBR inputs get the
	// explicit limited-range BT.709 conversion before encoding (see
	// proresRGBConversionFilters/xvidRGBConversionFilters), so write metadata
	// describing those encoded YUV planes rather than the source RGB matrix.
	rgbToYUV := (strings.HasPrefix(preset, "prores") || strings.HasPrefix(preset, "xvid")) &&
		(isRGBPixelFormat(i.PixelFormat) || strings.EqualFold(i.ColorSpace, "gbr"))
	if rgbToYUV {
		a = append(a, "-color_range", "tv", "-colorspace", "bt709")
	} else if i.ColorRange == "tv" || i.ColorRange == "pc" {
		a = append(a, "-color_range", i.ColorRange)
	}
	// Xvid and every ProRes preset in this build encode a YUV pixel format.
	// A source tagged as RGB/GBR can legitimately carry color_space=gbr, but
	// passing `-colorspace gbr` through to those YUV encoders is invalid (and
	// prores_ks rejects it outright). Keep GBR as input metadata via setparams,
	// but do not claim an RGB matrix on a YUV encoded stream.
	yuvOutput := strings.HasPrefix(preset, "xvid") || strings.HasPrefix(preset, "prores")
	if !rgbToYUV && i.ColorSpace != "" && i.ColorSpace != "unknown" && validColorSpaces[strings.ToLower(i.ColorSpace)] && !(yuvOutput && strings.EqualFold(i.ColorSpace, "gbr")) {
		a = append(a, "-colorspace", i.ColorSpace)
	}
	if i.ColorTransfer != "" && i.ColorTransfer != "unknown" && validColorTransfers[strings.ToLower(i.ColorTransfer)] {
		a = append(a, "-color_trc", i.ColorTransfer)
	}
	if i.ColorPrimaries != "" && i.ColorPrimaries != "unknown" && validColorPrimaries[strings.ToLower(i.ColorPrimaries)] {
		a = append(a, "-color_primaries", i.ColorPrimaries)
	}
	return a
}
