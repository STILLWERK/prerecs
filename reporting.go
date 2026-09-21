package main

import (
	"fmt"
	"math"
	"math/big"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

type theme struct {
	enabled bool
}

func (t theme) c(code, s string) string {
	if !t.enabled {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (t theme) bold(s string) string { return t.c("1", s) }

func (t theme) cyan(s string) string { return t.c("36", s) }

func (t theme) green(s string) string { return t.c("32", s) }

func (t theme) yellow(s string) string { return t.c("33", s) }

func (t theme) red(s string) string { return t.c("31", s) }

func (t theme) dim(s string) string { return t.c("2", s) }

func analyzeInputs(ffprobe string, paths []string, ui theme) ([]MediaInfo, int) {
	infos := make([]MediaInfo, 0, len(paths))
	inputFailures := 0
	fmt.Println(ui.bold("ANALYZING") + ui.dim("  metadata only"))
	for i, p := range paths {
		fmt.Printf("  [%d/%d] %s ... ", i+1, len(paths), strictConsoleText(filepath.Base(p)))
		info, err := probeMedia(ffprobe, p, false)
		if err != nil {
			inputFailures++
			fmt.Println(ui.red("FAILED"))
			fmt.Println("      " + strictConsoleText(err.Error()))
			continue
		}
		infos = append(infos, info)
		fmt.Println(ui.green("OK"))
	}
	return infos, inputFailures
}

func printHeader(ui theme, caps Capabilities, noNativeXvid bool) {
	fmt.Println()
	fmt.Println(ui.cyan(ui.bold("  PRE-RECS")) + "  " + ui.dim(version))
	fmt.Println(ui.dim("  Edit-ready community prerenders for game editing"))
	fmt.Println(ui.dim("  ------------------------------------------------"))
	fmt.Println()
	xvid := ui.red("no")
	if caps.HasLibXvid || (caps.HasNativeXvid && !noNativeXvid) {
		xvid = ui.green("yes")
	}
	prores := ui.red("no")
	if caps.HasProRes {
		prores = ui.green("CPU")
	}
	if caps.HasProRes && caps.HasProResVulkan {
		prores = ui.green("GPU/CPU")
	}
	magic := ui.dim("not installed")
	if caps.HasMagicYUV && caps.MagicInstalled {
		magic = ui.green("installed")
	}
	native := ui.dim("FFmpeg fallback")
	if caps.HasNativeXvid {
		native = ui.green("native Xvid")
		if noNativeXvid {
			native = ui.yellow("native Xvid disabled")
		}
	}
	fmt.Printf("  Xvid %s (%s)   ProRes %s   MagicYUV %s\n", xvid, native, prores, magic)
	if caps.HasProResVulkan {
		fmt.Println(ui.dim("  ProRes Vulkan GPU fast path available (experimental; automatic CPU fallback)"))
	}
	fmt.Printf("  %s\n\n", ui.dim(strictConsoleText(shortFFmpeg(caps.Version))))
}

func shortFFmpeg(v string) string {
	if len(v) > 100 {
		return v[:100] + "..."
	}
	return v
}

func printSourceTable(ui theme, infos []MediaInfo) {
	fmt.Println()
	fmt.Println(ui.bold("SOURCE"))
	fmt.Printf("  %-3s %-25s %-11s %-9s %-9s %-11s %-10s %-10s\n", "#", "File", "Dimensions", "FPS", "Frames", "Codec", "File size", "Source")
	fmt.Printf("  %-3s %-25s %-11s %-9s %-9s %-11s %-10s %-10s\n", "---", "-------------------------", "-----------", "---------", "---------", "-----------", "----------", "----------")
	for i, in := range infos {
		name := clipName(filepath.Base(in.Path), 25)
		fps := "?"
		if in.FPSFloat > 0 {
			fps = fmt.Sprintf("%.3f", in.FPSFloat)
		}
		frames := frameCountLabel(in)
		codec := strings.ToUpper(in.Codec)
		if strings.EqualFold(in.Codec, "mpeg4") && in.CodecTag != "" {
			codec = strings.ToUpper(in.CodecTag)
		}
		codec = clipName(codec, 11)
		fmt.Printf("  %-3d %-25s %-11s %-9s %-9s %-11s %-10s %-10s\n",
			i+1, name, fmt.Sprintf("%dx%d", in.Width, in.Height), fps, frames, codec, humanBytes(in.SizeBytes), sourceClass(in))
	}
	fmt.Println()
}

func frameCountLabel(in MediaInfo) string {
	if in.FrameCount <= 0 {
		return "?"
	}
	prefix := ""
	if !in.FrameCountExact {
		prefix = "~"
	}
	return prefix + strconv.FormatInt(in.FrameCount, 10)
}

func humanBytes(n int64) string {
	if n <= 0 {
		return "?"
	}
	const unit = 1024.0
	v := float64(n)
	if v < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for _, u := range units {
		v /= unit
		if v < unit || u == "TiB" {
			if v >= 100 {
				return fmt.Sprintf("%.0f %s", v, u)
			}
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return "?"
}

func printRecommendation(ui theme, infos []MediaInfo, caps Capabilities, noNativeXvid bool) {
	compressed, masters := 0, 0
	for _, in := range infos {
		if isDistributionCompressed(in) {
			compressed++
		} else if sourceClass(in) == "lossless" || sourceClass(in) == "intermediate" {
			masters++
		}
	}
	fmt.Println(ui.bold("RECOMMENDATION"))
	switch {
	case compressed == len(infos):
		fmt.Println("  These files are already distribution-compressed.")
		fmt.Println("  For editing, convert them to an intraframe/lossless intermediate instead of Xvid again.")
		if recommendedProResPreset(infos) == "prores_4444" {
			fmt.Println(ui.dim("  These sources contain alpha; ProRes 4444 is required to preserve it."))
		} else if caps.HasMagicYUV && caps.MagicInstalled {
			fmt.Println(ui.dim("  ProRes 422 LT is the broad edit-ready choice; MagicYUV is the fast lossless choice when installed."))
		} else {
			fmt.Println(ui.dim("  ProRes 422 LT is the broad edit-ready choice."))
		}
		fmt.Println(ui.dim("  Transcoding cannot restore detail already lost to H.264/HEVC/Xvid/AV1 and the intermediate will usually be much larger."))
	case masters == len(infos):
		backend := xvidBackendDescription(nativeXvidCaps(caps, noNativeXvid), infos)
		editPreset := recommendedProResPreset(infos)
		editRecommendation := "ProRes 422 LT is the edit-ready default"
		if editPreset == "prores_4444" {
			editRecommendation = "ProRes 4444 is required to preserve source alpha"
		}
		fmt.Printf("  Master/intermediate sources detected. Share/Xvid Q2 will use %s; %s.\n", backend, editRecommendation)
		all4208 := true
		for _, in := range infos {
			if in.Chroma != "4:2:0" || in.BitDepth > 8 {
				all4208 = false
				break
			}
		}
		if all4208 {
			fmt.Println(ui.dim("  These sources are 8-bit 4:2:0, so higher ProRes tiers add headroom but cannot restore missing chroma/bit depth."))
		}
		fmt.Println(ui.dim("  ProRes is chosen for decode/scrubbing performance, not guaranteed size reduction; highly compressible lossless masters can be smaller."))
	default:
		fmt.Println("  Mixed source types detected. Share/Xvid will protect already-compressed files from blind re-encoding.")
	}
	fmt.Println()
}

func printEffectiveRates(ui theme, infos []MediaInfo, opts ConvertOptions) {
	scale, err := parseRat(opts.Timescale)
	if err != nil {
		return
	}
	fmt.Println()
	fmt.Println("  Effective rates:")
	for _, in := range infos {
		src, _ := parseRatAllowZero(in.FPS)
		if opts.CaptureFPS != "" {
			src, _ = parseRat(opts.CaptureFPS)
		}
		if src == nil {
			continue
		}
		target := new(big.Rat).Quo(src, scale)
		warn := ""
		if ratFloat(target) > 999 {
			warn = "  " + ui.yellow("AE >999 FPS warning")
		}
		fmt.Printf("    %-28s  %.3f -> %.3f FPS%s\n", clipName(filepath.Base(in.Path), 28), ratFloat(src), ratFloat(target), warn)
	}
	fmt.Println()
}

func printPlan(ui theme, infos []MediaInfo, opts ConvertOptions) {
	fmt.Println()
	fmt.Println(ui.bold("READY"))
	fmt.Printf("  Preset:  %s\n", presetLabel(opts.Preset))
	if opts.Conform {
		cap := "detected per file"
		if opts.CaptureFPS != "" {
			cap = opts.CaptureFPS
		}
		fmt.Printf("  Timing:  conform  capture=%s  timescale=%s  audio=strip\n", cap, opts.Timescale)
	} else {
		fmt.Println("  Timing:  preserve source timing")
	}
	if opts.OutputDir == "" {
		fmt.Println("  Output:  converted_prerecs beside each source")
	} else {
		fmt.Printf("  Output:  %s\n", strictConsoleText(opts.OutputDir))
	}
	skipped := 0
	if isXvidPreset(opts.Preset) && opts.SkipCompressed && !opts.ForceXvid {
		for _, in := range infos {
			if isDistributionCompressed(in) {
				skipped++
			}
		}
	}
	if skipped > 0 {
		fmt.Printf("  Files:   %d  (%d already-compressed source%s will be kept)\n\n", len(infos), skipped, pluralS(skipped))
	} else {
		fmt.Printf("  Files:   %d\n\n", len(infos))
	}
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

type itemReporter struct {
	line     func(string)
	progress func(progressInfo)
	finish   func()
}

func sequentialReporter(ui theme) itemReporter {
	plainMax := 0
	return itemReporter{
		line: func(s string) {
			for _, line := range strings.Split(safeConsoleText(s), "\n") {
				fmt.Println("      " + line)
			}
		},
		progress: func(p progressInfo) { drawProgress(ui, p, &plainMax) },
		finish:   func() { finishProgress(ui, &plainMax) },
	}
}

type parallelProgressState struct {
	lastPrint   time.Time
	lastPercent float64
	stage       string
}

type parallelPrinter struct {
	mu     sync.Mutex
	ui     theme
	total  int
	states map[int]parallelProgressState
}

func newParallelPrinter(ui theme, total int) *parallelPrinter {
	return &parallelPrinter{ui: ui, total: total, states: make(map[int]parallelProgressState)}
}

func (p *parallelPrinter) prefix(index int, name string) string {
	return fmt.Sprintf("  [%d/%d] %-28s", index+1, p.total, clipName(name, 28))
}

func (p *parallelPrinter) line(index int, name, msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, line := range strings.Split(safeConsoleText(msg), "\n") {
		fmt.Printf("%s  %s\n", p.prefix(index, name), line)
	}
}

func compactProgressText(pi progressInfo) string {
	parts := []string{}
	stage := pi.Stage
	if stage == "" {
		stage = "WORK"
	}
	parts = append(parts, fmt.Sprintf("%-10s %6.2f%%", stage, pi.Percent*100))
	if pi.Frame != "" {
		if pi.TotalFrames > 0 {
			parts = append(parts, pi.Frame+"/"+strconv.FormatInt(pi.TotalFrames, 10))
		} else {
			parts = append(parts, pi.Frame+" frames")
		}
	}
	if pi.FPS != "" {
		if v, err := strconv.ParseFloat(strings.TrimSpace(pi.FPS), 64); err != nil || v > .005 {
			parts = append(parts, pi.FPS+" fps")
		}
	}
	if pi.ETA > 0 && pi.Percent < .9999 {
		parts = append(parts, "ETA "+formatETA(pi.ETA))
	}
	return strings.Join(parts, "  ")
}

func (p *parallelPrinter) progress(index int, name string, pi progressInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	st := p.states[index]
	stageChanged := pi.Stage != st.stage
	pctJump := pi.Percent-st.lastPercent >= .05
	if !stageChanged && pi.Percent < .9999 && !pctJump && !st.lastPrint.IsZero() && now.Sub(st.lastPrint) < time.Second {
		return
	}
	fmt.Printf("%s  %s\n", p.prefix(index, name), compactProgressText(pi))
	p.states[index] = parallelProgressState{lastPrint: now, lastPercent: pi.Percent, stage: pi.Stage}
}

func printBatchSummary(ui theme, result BatchResult, cancelled error) {
	fmt.Println()
	fmt.Println(ui.bold("SUMMARY"))
	for _, item := range result.Items {
		name := filepath.Base(item.Source)
		switch item.Status {
		case "ok":
			fmt.Printf("  %s %-28s", ui.green("OK"), clipName(name, 28))
			if item.InputInfo.SizeBytes > 0 && item.OutputInfo.SizeBytes > 0 {
				fmt.Printf("  %s -> %s", humanBytes(item.InputInfo.SizeBytes), humanBytes(item.OutputInfo.SizeBytes))
				if pct := sizeDeltaPercent(item.InputInfo.SizeBytes, item.OutputInfo.SizeBytes); !math.IsNaN(pct) {
					fmt.Printf("  (%+.1f%%)", pct)
				}
			}
			fmt.Println()
			if item.InputInfo.FrameCount > 0 && item.OutputInfo.FrameCount > 0 {
				fmt.Printf("     frames %d -> %d", item.InputInfo.FrameCount, item.OutputInfo.FrameCount)
			}
			if item.OutputInfo.FPSFloat > 0 {
				fmt.Printf("   %.3f fps", item.OutputInfo.FPSFloat)
			}
			if item.Elapsed > 0 {
				fmt.Printf("   %s", formatElapsed(item.Elapsed))
			}
			fmt.Println()
			fmt.Printf("     %s\n", ui.dim(item.Backend))
		case "skipped":
			fmt.Printf("  %s %-28s  %s\n", ui.yellow("SKIP"), clipName(name, 28), strictConsoleText(item.Message))
		case "failed":
			fmt.Printf("  %s %-28s  %s\n", ui.red("FAIL"), clipName(name, 28), conciseError(item.Message, 70))
		}
	}
	fmt.Println()
	fmt.Printf("  %s %d verified", ui.green("OK"), result.Successes)
	if result.Skipped > 0 {
		fmt.Printf("   %s %d skipped", ui.yellow("SKIP"), result.Skipped)
	}
	if result.Failures > 0 {
		fmt.Printf("   %s %d failed", ui.red("FAIL"), result.Failures)
	}
	if result.InputFailures > 0 {
		fmt.Printf("   %s %d input(s) failed analysis", ui.red("FAIL"), result.InputFailures)
	}
	fmt.Println()
	if cancelled != nil {
		fmt.Println("  " + ui.yellow("Conversion cancelled."))
	} else if result.InputFailures > 0 {
		fmt.Println("  " + ui.yellow("Some requested inputs failed analysis; only the successfully analyzed inputs are included above."))
	} else if result.Failures == 0 && result.Successes > 0 {
		fmt.Println("  " + ui.green("All outputs passed decoded-frame and timing verification."))
	} else if result.Failures == 0 && result.Successes == 0 && result.Skipped > 0 {
		fmt.Println("  " + ui.green("No re-encode was needed; skipped items were left unchanged."))
	}
}

func sizeDeltaPercent(in, out int64) float64 {
	if in <= 0 || out <= 0 {
		return math.NaN()
	}
	return (float64(out)/float64(in) - 1) * 100
}

func formatElapsed(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%.0fms", float64(d)/float64(time.Millisecond))
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

type progressInfo struct {
	Percent     float64
	FPS         string
	Speed       string
	Frame       string
	Time        float64
	TotalFrames int64
	Bytes       int64
	ETA         time.Duration
	Stage       string
}

func drawProgress(ui theme, p progressInfo, plainMax *int) {
	width := 28
	filled := int(math.Round(p.Percent * float64(width)))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
	extra := ""
	if p.Frame != "" {
		if p.TotalFrames > 0 {
			extra += "  " + p.Frame + "/" + strconv.FormatInt(p.TotalFrames, 10) + " frames"
		} else {
			extra += "  " + p.Frame + " frames"
		}
	}
	if p.FPS != "" {
		// Some FFmpeg mux/wrap-only stages report fps=0.00 even though they are
		// progressing normally. Hide that meaningless field rather than making a
		// healthy copy/remux look stalled.
		if v, err := strconv.ParseFloat(strings.TrimSpace(p.FPS), 64); err != nil || v > .005 {
			extra += "  " + p.FPS + " fps"
		}
	}
	if p.Speed != "" {
		extra += "  " + p.Speed
	}
	if p.Bytes > 0 {
		extra += "  " + humanBytes(p.Bytes)
	}
	if p.ETA > 0 && p.Percent < 0.9999 {
		extra += "  ETA " + formatETA(p.ETA)
	}
	label := ""
	if p.Stage != "" {
		label = p.Stage + " "
	}
	line := fmt.Sprintf("      %s[%s] %6.2f%%%s", label, bar, p.Percent*100, extra)
	if ui.enabled {
		fmt.Print("\r\x1b[2K" + line)
	} else {
		// No erase-in-sequence on dumb terminals: pad every redraw to the
		// longest line this progress display has emitted so a shorter one
		// never leaves a tail behind; plainMax resets on finishProgress.
		if pad := *plainMax - len(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		if len(line) > *plainMax {
			*plainMax = len(line)
		}
		fmt.Print("\r" + line)
	}
}

func formatETA(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%02ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%02d:%02d", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%02d:%02d:%02d", int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60)
}

func etaFromProgress(start time.Time, percent float64) time.Duration {
	if percent <= 0 || percent >= 1 {
		return 0
	}
	elapsed := time.Since(start)
	// Early extrapolation is wildly unstable on codecs that warm up slowly
	// (notably native Xvid). Hide ETA until there is enough real work to
	// estimate from instead of flashing multi-hour guesses for a short clip.
	if percent < 0.05 || elapsed < 2*time.Second {
		return 0
	}
	return time.Duration(float64(elapsed) * (1 - percent) / percent)
}

func exactFrameProgress(p progressInfo, totalFrames int64, started time.Time) progressInfo {
	p.TotalFrames = totalFrames
	frames := parseInt64(strings.TrimSpace(p.Frame))
	if frames <= 0 || totalFrames <= 0 {
		return p
	}
	p.Percent = math.Max(0, math.Min(1, float64(frames)/float64(totalFrames)))
	p.ETA = etaFromProgress(started, p.Percent)
	reported := parseFloat(strings.TrimSpace(p.FPS))
	if reported <= 0 {
		if elapsed := time.Since(started).Seconds(); elapsed > 0 {
			p.FPS = fmt.Sprintf("%.2f", float64(frames)/elapsed)
		}
	}
	return p
}

func finishProgress(ui theme, plainMax *int) {
	if ui.enabled {
		fmt.Print("\r\x1b[2K")
	} else {
		fmt.Print("\r" + strings.Repeat(" ", *plainMax) + "\r")
		*plainMax = 0
	}
}

// consoleSGR lists every SGR sequence the theme emits. Anything else carrying
// ESC into a console sink came from untrusted text.
var consoleSGR = []string{"\x1b[0m", "\x1b[1m", "\x1b[2m", "\x1b[31m", "\x1b[32m", "\x1b[33m", "\x1b[36m"}

// safeConsoleText strips terminal control bytes from untrusted strings —
// filenames, FFmpeg/ffprobe messages — while preserving the theme's own SGR
// styling. Foreign attributes (conceal, blink, 256-color) and every other
// escape sequence lose their ESC and print as inert text.
func safeConsoleText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\x1b' {
			rest := string(rs[i:])
			for _, sgr := range consoleSGR {
				if strings.HasPrefix(rest, sgr) {
					b.WriteString(sgr)
					i += len(sgr) - 1 // ASCII-only: rune count == byte count
					break
				}
			}
			continue
		}
		if r == '\n' || unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// strictConsoleText strips every terminal escape and non-print byte from
// untrusted text — filenames, probed metadata, FFmpeg/ffprobe messages —
// before it is composed into styled output. Unlike safeConsoleText it trusts
// no SGR: theme styling is only ever applied around sanitized fragments,
// never accepted from inside them, so a hostile name cannot spoof the theme.
func strictConsoleText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		if r == '\x1b' {
			// Consume the sequence instead of leaving "[32m" residue: CSI runs
			// ESC [ … to a 0x40–0x7e final byte; OSC runs ESC ] … to BEL or ST.
			if i+1 < len(rs) && rs[i+1] == '[' {
				for i += 2; i < len(rs) && (rs[i] < 0x40 || rs[i] > 0x7e); i++ {
				}
			} else if i+1 < len(rs) && rs[i+1] == ']' {
				for i += 2; i < len(rs) && rs[i] != '\x07' && !(rs[i] == '\x1b' && i+1 < len(rs) && rs[i+1] == '\\'); i++ {
				}
				if i < len(rs) && rs[i] == '\x1b' {
					i++ // ST is two bytes; the loop's i++ passes the backslash
				}
			}
			continue
		}
		if r == '\n' || unicode.IsPrint(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func clipName(s string, n int) string {
	s = strictConsoleText(s)
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}

func conciseError(s string, n int) string {
	return clipName(strings.Join(strings.Fields(s), " "), n)
}

func indentError(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(strings.TrimSpace(strictConsoleText(s)), "\n")
	if len(lines) > 8 {
		lines = append(lines[:8], "...")
	}
	return pad + strings.Join(lines, "\n"+pad)
}
