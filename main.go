package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const version = "1.0.0"

var supportedExt = map[string]bool{
	".avi": true, ".mp4": true, ".m4v": true, ".mov": true, ".mkv": true,
	".wmv": true, ".webm": true, ".mpg": true, ".mpeg": true, ".m2ts": true, ".ts": true,
}

var stdinReader = bufio.NewReader(os.Stdin)

type AudioInfo struct {
	Index      int
	Codec      string
	Channels   int
	SampleRate int
	Language   string
}

type MediaInfo struct {
	Path            string
	Codec           string
	CodecTag        string
	Profile         string
	Width           int
	Height          int
	PixelFormat     string
	BitDepth        int
	FPS             string
	FPSFloat        float64
	Duration        float64
	FrameCount      int64
	FrameCountExact bool
	SizeBytes       int64
	BitRate         int64
	ColorRange      string
	ColorSpace      string
	ColorTransfer   string
	ColorPrimaries  string
	HasAlpha        bool
	Chroma          string
	Audio           []AudioInfo
}

type Capabilities struct {
	FFmpeg          string
	FFprobe         string
	Version         string
	HasXvid         bool
	HasLibXvid      bool
	HasProRes       bool
	HasProResVulkan bool
	HasMagicYUV     bool
	HasUtVideo      bool
	HasNativeXvid   bool
	XvidEncRaw      string
	MagicInstalled  bool
	MagicDetail     string
}

type ConvertOptions struct {
	Preset         string
	Conform        bool
	CaptureFPS     string
	Timescale      string
	StripAudio     bool
	OutputDir      string
	SkipCompressed bool
	ForceXvid      bool
	CPUProRes      bool
}

type Engine struct {
	caps Capabilities
	enc  map[string]bool
}

type ItemResult struct {
	Source     string
	Output     string
	Status     string
	Backend    string
	Message    string
	InputInfo  MediaInfo
	OutputInfo MediaInfo
	Elapsed    time.Duration
}

type BatchResult struct {
	Successes     int
	Failures      int
	Skipped       int
	Items         []ItemResult
	LastOutputDir string
}

type ffprobeDoc struct {
	Streams []struct {
		Index            int               `json:"index"`
		CodecName        string            `json:"codec_name"`
		Profile          string            `json:"profile"`
		CodecType        string            `json:"codec_type"`
		Width            int               `json:"width"`
		Height           int               `json:"height"`
		PixFmt           string            `json:"pix_fmt"`
		BitsPerRawSample string            `json:"bits_per_raw_sample"`
		AvgFrameRate     string            `json:"avg_frame_rate"`
		RFrameRate       string            `json:"r_frame_rate"`
		Duration         string            `json:"duration"`
		NBFrames         string            `json:"nb_frames"`
		NBReadPackets    string            `json:"nb_read_packets"`
		NBReadFrames     string            `json:"nb_read_frames"`
		CodecTagString   string            `json:"codec_tag_string"`
		ColorRange       string            `json:"color_range"`
		ColorSpace       string            `json:"color_space"`
		ColorTransfer    string            `json:"color_transfer"`
		ColorPrimaries   string            `json:"color_primaries"`
		Channels         int               `json:"channels"`
		SampleRate       string            `json:"sample_rate"`
		Tags             map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		Size     string `json:"size"`
		BitRate  string `json:"bit_rate"`
	} `json:"format"`
}

type cliConfig struct {
	preset      string
	timescale   string
	captureFPS  string
	stripAudio  bool
	outputDir   string
	yes         bool
	forceXvid   bool
	cpuProRes   bool
	plain       bool
	showVersion bool
}

type theme struct {
	enabled bool
}

func (t theme) c(code, s string) string {
	if !t.enabled {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
func (t theme) bold(s string) string   { return t.c("1", s) }
func (t theme) cyan(s string) string   { return t.c("36", s) }
func (t theme) green(s string) string  { return t.c("32", s) }
func (t theme) yellow(s string) string { return t.c("33", s) }
func (t theme) red(s string) string    { return t.c("31", s) }
func (t theme) dim(s string) string    { return t.c("2", s) }

func main() {
	cfg, args, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		printUsage()
		os.Exit(2)
	}
	if cfg.showVersion {
		fmt.Println("PreRecs", version)
		return
	}

	ansi := !cfg.plain && os.Getenv("NO_COLOR") == "" && enableANSI()
	ui := theme{enabled: ansi}
	if ansi {
		fmt.Print("\x1b]0;PreRecs\x07")
	}

	caps, enc, err := detectCapabilities()
	if err != nil {
		fmt.Fprintln(os.Stderr, ui.red("Error: ")+err.Error())
		pauseIfDoubleClicked()
		os.Exit(1)
	}
	engine := &Engine{caps: caps, enc: enc}
	printHeader(ui, caps)

	jobArgs := args
	for {
		paths, err := gatherInputs(jobArgs, ui)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			fmt.Fprintln(os.Stderr, ui.red("Error: ")+err.Error())
			if cfg.yes {
				os.Exit(1)
			}
			jobArgs = nil
			continue
		}
		if len(paths) == 0 {
			fmt.Println(ui.dim("No files selected."))
			return
		}

		infos := make([]MediaInfo, 0, len(paths))
		fmt.Println(ui.bold("ANALYZING") + ui.dim("  metadata only"))
		for i, p := range paths {
			fmt.Printf("  [%d/%d] %s ... ", i+1, len(paths), filepath.Base(p))
			info, err := probeMedia(caps.FFprobe, p, false)
			if err != nil {
				fmt.Println(ui.red("FAILED"))
				fmt.Println("      " + err.Error())
				continue
			}
			infos = append(infos, info)
			fmt.Println(ui.green("OK"))
		}
		if len(infos) == 0 {
			fmt.Fprintln(os.Stderr, ui.red("No readable video files."))
			if cfg.yes {
				os.Exit(1)
			}
			jobArgs = nil
			continue
		}

		printSourceTable(ui, infos)
		printRecommendation(ui, infos, caps)

		opts, err := collectOptions(cfg, ui, engine, infos)
		if err != nil {
			fmt.Fprintln(os.Stderr, ui.red("Error: ")+err.Error())
			if cfg.yes {
				os.Exit(1)
			}
			jobArgs = nil
			continue
		}

		printPlan(ui, infos, opts)
		if !cfg.yes && !askYesNo("Start conversion?", true) {
			fmt.Println(ui.dim("Cancelled."))
			if !askYesNo("Start a new job?", true) {
				return
			}
			jobArgs = nil
			cfg = resetInteractiveJob(cfg)
			continue
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		result := runBatch(ctx, ui, engine, infos, opts)
		cancelled := ctx.Err()
		stop()
		printBatchSummary(ui, result, cancelled)

		if cfg.yes {
			os.Exit(headlessExitCode(result, cancelled))
		}

		for {
			fmt.Println()
			fmt.Println(ui.bold("NEXT"))
			fmt.Println("  1. Convert more clips")
			fmt.Println("  2. Open last output folder")
			fmt.Println("  3. Exit")
			choice := askChoice("Choose", []string{"1", "2", "3"}, "1")
			switch choice {
			case "1":
				jobArgs = nil
				cfg = resetInteractiveJob(cfg)
				fmt.Println()
				goto nextJob
			case "2":
				if result.LastOutputDir == "" {
					fmt.Println(ui.dim("  No output folder was created in the last job."))
				} else if err := openFolder(result.LastOutputDir); err != nil {
					fmt.Println(ui.yellow("  Could not open folder: ") + err.Error())
				}
			case "3":
				return
			}
		}
	nextJob:
		continue
	}
}

func headlessExitCode(result BatchResult, cancelled error) int {
	if cancelled != nil {
		return 130
	}
	if result.Failures > 0 {
		return 1
	}
	return 0
}

func resetInteractiveJob(c cliConfig) cliConfig {
	c.preset = ""
	c.timescale = ""
	c.captureFPS = ""
	c.stripAudio = false
	c.outputDir = ""
	c.forceXvid = false
	c.cpuProRes = false
	return c
}

func parseFlags(args []string) (cliConfig, []string, error) {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			printUsage()
			os.Exit(0)
		}
	}
	fs := flag.NewFlagSet("prerecs", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var c cliConfig
	fs.StringVar(&c.preset, "preset", "", "share|compact|xvid|xvid-q2|xvid-efficient|xvid-fast|edit|lossless|prores|hq|xvid-max-q2|xvid-small|xvid-max|4444|magicyuv|utvideo")
	fs.StringVar(&c.timescale, "timescale", "", "game timescale, e.g. 0.1")
	fs.StringVar(&c.captureFPS, "capture-fps", "", "capture FPS override, e.g. 30 or 60000/1001")
	fs.BoolVar(&c.stripAudio, "strip-audio", false, "remove audio instead of keeping it")
	fs.StringVar(&c.outputDir, "output", "", "custom output directory")
	fs.BoolVar(&c.yes, "yes", false, "skip final confirmation")
	fs.BoolVar(&c.forceXvid, "force-xvid", false, "force Xvid even when the source is already distribution-compressed")
	fs.BoolVar(&c.cpuProRes, "cpu-prores", false, "disable experimental Vulkan ProRes fast path and force CPU prores_ks")
	fs.BoolVar(&c.plain, "plain", false, "disable ANSI colors/progress styling")
	fs.BoolVar(&c.showVersion, "version", false, "print version")
	if err := fs.Parse(args); err != nil {
		return c, nil, err
	}
	return c, fs.Args(), nil
}

func printUsage() {
	fmt.Println(`PreRecs - terminal prerec converter

Usage:
  PreRecs.exe [options] <file-or-folder> [more files...]
  PreRecs.exe

Options:
  --preset share|compact|xvid|xvid-q2|xvid-efficient|xvid-fast|edit|lossless|prores|hq|xvid-max-q2|xvid-small|xvid-max|4444|magicyuv|utvideo
  --timescale 0.1          conform slowed capture to effective FPS
  --capture-fps 30         override detected capture FPS
  --strip-audio            remove audio instead of keeping it
  --output <folder>        custom output directory
  --yes                    skip final confirmation
  --force-xvid             force Share/Xvid on already-compressed sources
  --cpu-prores             force CPU ProRes; disable Vulkan fast path
  --plain                  disable ANSI styling
  --version                print version

Examples:
  PreRecs.exe "clip.mp4"
  PreRecs.exe --preset share --yes "master.avi"
  PreRecs.exe --preset edit --timescale 0.1 --capture-fps 30 "cinematic.mp4"`)
}

func printHeader(ui theme, caps Capabilities) {
	fmt.Println()
	fmt.Println(ui.cyan(ui.bold("  PRE-RECS")) + "  " + ui.dim(version))
	fmt.Println(ui.dim("  Edit-ready community prerenders for game editing"))
	fmt.Println(ui.dim("  ------------------------------------------------"))
	fmt.Println()
	xvid := ui.red("no")
	if caps.HasXvid {
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
	}
	fmt.Printf("  Xvid %s (%s)   ProRes %s   MagicYUV %s\n", xvid, native, prores, magic)
	if caps.HasProResVulkan {
		fmt.Println(ui.dim("  ProRes Vulkan GPU fast path available (experimental; automatic CPU fallback)"))
	}
	fmt.Printf("  %s\n\n", ui.dim(shortFFmpeg(caps.Version)))
}

func shortFFmpeg(v string) string {
	if len(v) > 100 {
		return v[:100] + "..."
	}
	return v
}

func gatherInputs(args []string, ui theme) ([]string, error) {
	if len(args) > 0 {
		return expandInputs(args)
	}
	fmt.Println(ui.bold("INPUT"))
	fmt.Println("  Drag files onto PreRecs.exe, or paste file/folder paths here.")
	fmt.Println("  Multiple quoted paths are supported.")
	fmt.Print("\n  Path(s) > ")
	line, err := stdinReader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	return expandInputs(parsePathInput(line))
}

func parsePathInput(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if _, err := os.Stat(trimOuterQuotes(line)); err == nil {
		return []string{trimOuterQuotes(line)}
	}
	out := []string{}
	var b strings.Builder
	quoted := false
	quote := rune(0)
	flush := func() {
		s := strings.TrimSpace(b.String())
		b.Reset()
		if s != "" {
			out = append(out, trimOuterQuotes(s))
		}
	}
	for _, ch := range line {
		if ch == '"' || ch == '\'' {
			if !quoted {
				quoted = true
				quote = ch
				continue
			}
			if quote == ch {
				quoted = false
				continue
			}
		}
		if !quoted && (ch == ';' || ch == '\t') {
			flush()
			continue
		}
		if !quoted && ch == ' ' {
			if b.Len() > 0 {
				flush()
			}
			continue
		}
		b.WriteRune(ch)
	}
	flush()
	return out
}

func trimOuterQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

func expandInputs(items []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range items {
		p := trimOuterQuotes(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if st.IsDir() {
			entries, err := os.ReadDir(p)
			if err != nil {
				return nil, err
			}
			names := make([]string, 0)
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if supportedExt[strings.ToLower(filepath.Ext(e.Name()))] {
					names = append(names, filepath.Join(p, e.Name()))
				}
			}
			sort.Strings(names)
			for _, n := range names {
				a, _ := filepath.Abs(n)
				if !seen[a] {
					seen[a] = true
					out = append(out, a)
				}
			}
			continue
		}
		a, _ := filepath.Abs(p)
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out, nil
}

func printSourceTable(ui theme, infos []MediaInfo) {
	fmt.Println()
	fmt.Println(ui.bold("SOURCE"))
	fmt.Printf("  %-3s %-25s %-11s %-9s %-9s %-11s %-10s %-10s\n", "#", "File", "Size", "FPS", "Frames", "Codec", "File size", "Source")
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

func sourceClass(in MediaInfo) string {
	c := strings.ToLower(in.Codec)
	tag := strings.ToLower(in.CodecTag)
	if c == "mpeg4" && (tag == "xvid" || tag == "divx" || tag == "dx50") {
		return "compressed"
	}
	switch c {
	case "h264", "hevc", "h265", "av1", "vp9", "vp8", "mpeg4", "mpeg2video", "mpeg1video", "vc1", "wmv3", "msmpeg4v3":
		return "compressed"
	case "lagarith", "magicyuv", "ffv1", "huffyuv", "utvideo", "rawvideo":
		return "lossless"
	case "prores", "dnxhd", "cfhd":
		return "intermediate"
	default:
		return "other"
	}
}

func isDistributionCompressed(in MediaInfo) bool { return sourceClass(in) == "compressed" }

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

func printRecommendation(ui theme, infos []MediaInfo, caps Capabilities) {
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
		if caps.HasMagicYUV && caps.MagicInstalled {
			fmt.Println(ui.dim("  ProRes 422 LT is the broad edit-ready choice; MagicYUV is the fast lossless choice when installed."))
		} else {
			fmt.Println(ui.dim("  ProRes 422 LT is the broad edit-ready choice."))
		}
		fmt.Println(ui.dim("  Transcoding cannot restore detail already lost to H.264/HEVC/Xvid/AV1 and the intermediate will usually be much larger."))
	case masters == len(infos):
		backend := "FFmpeg Xvid"
		if available, missing := xvidAvailableForInputs(caps, infos, false); !available {
			backend = "Xvid unavailable for " + strings.Join(missing, ", ") + "; use ProRes or install FFmpeg libxvid"
		} else if caps.HasNativeXvid {
			nativeEligible := 0
			for _, in := range infos {
				if nativeXvidEligible(in) {
					nativeEligible++
				}
			}
			switch {
			case nativeEligible == len(infos):
				backend = "native Xvid when the VfW preflight passes, with verified FFmpeg fallback"
			case nativeEligible > 0:
				backend = "native Xvid where eligible, with verified FFmpeg fallback for the rest"
			default:
				backend = "FFmpeg Xvid fallback"
			}
		}
		fmt.Printf("  Master/intermediate sources detected. Share/Xvid Q2 will use %s; ProRes 422 LT is the edit-ready default.\n", backend)
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

func compressedInputs(infos []MediaInfo) []MediaInfo {
	out := []MediaInfo{}
	for _, in := range infos {
		if isDistributionCompressed(in) {
			out = append(out, in)
		}
	}
	return out
}

func collectOptions(cfg cliConfig, ui theme, e *Engine, infos []MediaInfo) (ConvertOptions, error) {
	opts := ConvertOptions{OutputDir: cfg.outputDir, StripAudio: cfg.stripAudio, ForceXvid: cfg.forceXvid, CPUProRes: cfg.cpuProRes}
	if cfg.preset != "" {
		p, err := normalizePreset(cfg.preset)
		if err != nil {
			return opts, err
		}
		opts.Preset = p
	} else {
		var chooseErr error
		opts.Preset, chooseErr = choosePreset(ui, e, infos)
		if chooseErr != nil {
			return opts, chooseErr
		}
	}
	if strings.HasPrefix(opts.Preset, "xvid") {
		compressed := compressedInputs(infos)
		if len(compressed) > 0 && !opts.ForceXvid {
			if cfg.yes {
				opts.SkipCompressed = true
			} else {
				fmt.Println(ui.bold("SHARE SOURCE CHECK"))
				fmt.Println(ui.yellow("  Some inputs are already distribution-compressed:"))
				for _, in := range compressed {
					fmt.Printf("    - %s (%s, %s)\n", filepath.Base(in.Path), strings.ToUpper(in.Codec), humanBytes(in.SizeBytes))
				}
				fmt.Println(ui.dim("  Xvid cannot restore lost detail and may become much larger than the source."))
				fmt.Println("  1. Skip these files / keep originals " + ui.green("recommended"))
				fmt.Println("  2. Force Xvid anyway")
				fmt.Println("  3. Switch this job to ProRes 422 LT")
				c := askChoice("Choose", []string{"1", "2", "3"}, "1")
				switch c {
				case "1":
					opts.SkipCompressed = true
				case "2":
					opts.ForceXvid = true
				case "3":
					opts.Preset = "prores_lt"
				}
				fmt.Println()
			}
		}
	}
	if err := validatePresetInputs(opts.Preset, infos); err != nil {
		return opts, err
	}
	if isXvidPreset(opts.Preset) {
		skipCompressed := opts.SkipCompressed && !opts.ForceXvid
		if ok, missing := xvidAvailableForInputs(e.caps, infos, skipCompressed); !ok {
			return opts, fmt.Errorf("selected Xvid preset cannot run for %s: install FFmpeg libxvid or choose a compatible preset", strings.Join(missing, ", "))
		}
	}

	if cfg.timescale != "" {
		opts.Conform = true
		opts.Timescale = cfg.timescale
		opts.CaptureFPS = cfg.captureFPS
	} else if !cfg.yes {
		fmt.Println(ui.bold("TIMING"))
		if askYesNo("Was this captured with reduced in-game timescale?", false) {
			opts.Conform = true
			opts.CaptureFPS = askLine("Capture FPS override (Enter = detected FPS)", "")
			opts.Timescale = askLine("Game timescale", "0.1")
			if _, err := parseRat(opts.Timescale); err != nil {
				return opts, err
			}
			printEffectiveRates(ui, infos, opts)
			fmt.Println(ui.dim("  Audio will be stripped when conforming timing; copying it unchanged would desynchronize it."))
		}
	}

	if !opts.Conform {
		hasAudio := false
		for _, in := range infos {
			if len(in.Audio) > 0 {
				hasAudio = true
				break
			}
		}
		if hasAudio && !cfg.yes {
			fmt.Println()
			fmt.Println(ui.bold("AUDIO"))
			fmt.Println("  1. Keep audio unchanged " + ui.green("recommended"))
			fmt.Println("  2. Strip audio")
			opts.StripAudio = askChoice("Choose", []string{"1", "2"}, "1") == "2"
		}
		if !opts.StripAudio {
			bad := incompatibleAudioCopies(opts.Preset, infos)
			if len(bad) > 0 {
				msg := "audio cannot be safely stream-copied to this output container: " + strings.Join(bad, ", ")
				if cfg.yes {
					return opts, errors.New(msg + "; use --strip-audio or choose ProRes when you need to keep this audio")
				}
				fmt.Println()
				fmt.Println(ui.bold("AUDIO COMPATIBILITY"))
				fmt.Println(ui.yellow("  " + msg))
				fmt.Println(ui.dim("  PreRecs will not silently remux audio into a container where timing/framing may change."))
				if askYesNo("Strip audio and continue?", true) {
					opts.StripAudio = true
				} else {
					return opts, errors.New("audio copy cancelled; choose ProRes or strip audio")
				}
			}
		}
	}

	if opts.OutputDir == "" && !cfg.yes {
		fmt.Println()
		fmt.Println(ui.bold("OUTPUT"))
		out := askLine("Folder (Enter = converted_prerecs beside each source)", "")
		opts.OutputDir = out
	}
	return opts, nil
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
			if audioCopyCompatible(preset, a.Codec) {
				continue
			}
			label := filepath.Base(in.Path) + "=" + strings.ToUpper(a.Codec)
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
		missing = append(missing, filepath.Base(in.Path))
	}
	return len(missing) == 0, missing
}

func choosePreset(ui theme, e *Engine, infos []MediaInfo) (string, error) {
	for {
		fmt.Println(ui.bold("PRESET"))
		fmt.Println("  1. SHARE        Xvid Q2       " + ui.green("tuned VHQ4/B2 default"))
		fmt.Println("  2. EDIT-READY   ProRes 422 LT " + ui.green("recommended"))
		magicPrimary := "MagicYUV"
		magicCompatible := validatePresetInputs("magicyuv_lossless", infos) == nil
		if !(e.caps.HasMagicYUV && e.caps.MagicInstalled && magicCompatible) {
			magicPrimary += " (unavailable)"
		}
		fmt.Printf("  3. LOSSLESS     %-13s %s\n", magicPrimary, ui.dim("fast lossless intermediate"))
		fmt.Println("  4. MORE...")
		c := askChoice("Choose", []string{"1", "2", "3", "4"}, "2")
		switch c {
		case "1":
			if !e.caps.HasXvid {
				fmt.Println(ui.yellow("  This FFmpeg build does not include libxvid."))
				fmt.Println()
				continue
			}
			if ok, missing := xvidAvailableForInputs(e.caps, infos, false); !ok {
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
				return "", err
			}
			return "prores_lt", nil
		case "3":
			if !(e.caps.HasMagicYUV && e.caps.MagicInstalled && magicCompatible) {
				fmt.Println(ui.yellow("  MagicYUV is not available. Install/licence MagicYUV and use an FFmpeg build with the encoder."))
				if err := validatePresetInputs("magicyuv_lossless", infos); err != nil {
					fmt.Println(ui.yellow("  " + err.Error()))
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
			a := askChoice("Choose", []string{"0", "1", "2", "3", "4", "5", "6", "7", "8"}, "0")
			switch a {
			case "1":
				if e.caps.HasProRes {
					if err := validatePresetInputs("prores_422", infos); err != nil {
						return "", err
					}
					return "prores_422", nil
				}
				fmt.Println(ui.yellow("  prores_ks is unavailable."))
				fmt.Println()
			case "2":
				if e.caps.HasProRes {
					if err := validatePresetInputs("prores_hq", infos); err != nil {
						return "", err
					}
					return "prores_hq", nil
				}
				fmt.Println(ui.yellow("  prores_ks is unavailable."))
				fmt.Println()
			case "3":
				if e.caps.HasXvid {
					if ok, missing := xvidAvailableForInputs(e.caps, infos, false); ok {
						return "xvid_efficient_q2", nil
					} else {
						return "", fmt.Errorf("xvid is unavailable for %s: install FFmpeg libxvid or choose ProRes", strings.Join(missing, ", "))
					}
				}
				fmt.Println(ui.yellow("  Xvid is unavailable."))
				fmt.Println()
			case "4":
				if e.caps.HasXvid {
					if ok, missing := xvidAvailableForInputs(e.caps, infos, false); ok {
						return "xvid_compact", nil
					} else {
						return "", fmt.Errorf("xvid is unavailable for %s: install FFmpeg libxvid or choose ProRes", strings.Join(missing, ", "))
					}
				}
				fmt.Println(ui.yellow("  Xvid is unavailable."))
				fmt.Println()
			case "5":
				if e.caps.HasXvid {
					if ok, missing := xvidAvailableForInputs(e.caps, infos, false); ok {
						return "xvid_small", nil
					} else {
						return "", fmt.Errorf("xvid is unavailable for %s: install FFmpeg libxvid or choose ProRes", strings.Join(missing, ", "))
					}
				}
				fmt.Println(ui.yellow("  Xvid is unavailable."))
				fmt.Println()
			case "6":
				if e.caps.HasXvid {
					if ok, missing := xvidAvailableForInputs(e.caps, infos, false); ok {
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
					fmt.Println(ui.yellow("  " + err.Error()))
				}
				fmt.Println()
			}
		}
	}
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
		fmt.Printf("  Output:  %s\n", opts.OutputDir)
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
	return itemReporter{
		line: func(s string) {
			for _, line := range strings.Split(s, "\n") {
				fmt.Println("      " + line)
			}
		},
		progress: func(p progressInfo) { drawProgress(ui, p) },
		finish:   func() { finishProgress(ui) },
	}
}

func addBatchItem(result *BatchResult, item ItemResult) {
	if item.Status == "" || item.Status == "cancelled" {
		return
	}
	result.Items = append(result.Items, item)
	switch item.Status {
	case "ok":
		result.Successes++
	case "skipped":
		result.Skipped++
	case "failed":
		result.Failures++
	}
	if item.Output != "" && (item.Status == "ok" || item.Status == "skipped") {
		result.LastOutputDir = filepath.Dir(item.Output)
	}
}

func processItem(ctx context.Context, ui theme, e *Engine, info MediaInfo, opts ConvertOptions, rep itemReporter) ItemResult {
	item := ItemResult{Source: info.Path, InputInfo: info}
	if ctx.Err() != nil {
		item.Status = "cancelled"
		return item
	}

	if isXvidPreset(opts.Preset) && opts.SkipCompressed && isDistributionCompressed(info) && !opts.ForceXvid {
		item.Status = "skipped"
		item.Backend = "keep original"
		item.Message = "already distribution-compressed"
		rep.line(ui.yellow("SKIPPED") + " already distribution-compressed; keeping original")
		return item
	}

	if nativeXvidNeedsExactFrameScan(info) {
		info.FrameCountExact = false
		rep.line("Lossless AVI metadata does not agree with its duration; doing one exact decode scan before native Xvid.")
	}
	if !info.FrameCountExact {
		rep.line("Source frame count is estimated; doing one exact decode scan before conversion.")
		count, scanErr := e.countDecodedFrames(ctx, info, func(p progressInfo) {
			p.Stage = "SCAN"
			p.TotalFrames = info.FrameCount
			rep.progress(p)
		})
		rep.finish()
		if scanErr != nil {
			item.Status = "failed"
			item.Message = scanErr.Error()
			rep.line(ui.red("SOURCE SCAN FAILED"))
			rep.line(indentError(scanErr.Error(), 2))
			return item
		}
		info.FrameCount = count
		info.FrameCountExact = true
		oldDuration := info.Duration
		if info.FPSFloat > 0 {
			info.Duration = float64(count) / info.FPSFloat
		}
		item.InputInfo = info
		rep.line(fmt.Sprintf("Exact source frames: %d", count))
		tol := .01
		if info.FPSFloat > 0 {
			tol = math.Max(tol, 2.5/info.FPSFloat)
		}
		if oldDuration > 0 && info.Duration > 0 && math.Abs(oldDuration-info.Duration) > tol {
			rep.line(fmt.Sprintf("Normalized source timeline: %.4fs -> %.4fs (%d frames @ %.3f fps)", oldDuration, info.Duration, count, info.FPSFloat))
		}
	}

	existing, out, err := outputCandidates(info.Path, opts.OutputDir, opts.Preset)
	if err != nil {
		item.Status = "failed"
		item.Message = err.Error()
		rep.line(ui.red("FAILED") + " " + err.Error())
		return item
	}
	if len(existing) > 0 {
		_, expectedFPS0, expectedDur0, timingErr := expectedTiming(info, opts)
		if timingErr == nil {
			srcStat, _ := os.Stat(info.Path)
			for _, cand := range existing {
				candStat, statErr := os.Stat(cand)
				if statErr != nil || (srcStat != nil && candStat.ModTime().Before(srcStat.ModTime())) {
					continue
				}
				rep.line(fmt.Sprintf("Existing output detected: %s", filepath.Base(cand)))
				rep.line("Checking it before deciding whether to re-encode...")
				outInfo, probeErr := probeMedia(e.caps.FFprobe, cand, false)
				if probeErr != nil {
					rep.line(ui.dim("Rejected: metadata probe failed: " + probeErr.Error()))
					continue
				}
				if !presetCodecMatches(opts.Preset, outInfo) {
					rep.line(ui.dim(fmt.Sprintf("Rejected: codec is %s/%s, expected %s", outInfo.Codec, outInfo.CodecTag, presetLabel(opts.Preset))))
					continue
				}
				checkStarted := time.Now()
				decoded, decodeErr := e.countDecodedFrames(ctx, outInfo, func(p progressInfo) {
					p.Stage = "CHECK"
					p = exactFrameProgress(p, info.FrameCount, checkStarted)
					rep.progress(p)
				})
				rep.finish()
				if decodeErr != nil {
					rep.line(ui.dim("Rejected: full decode check failed: " + decodeErr.Error()))
					continue
				}
				outInfo.FrameCount = decoded
				outInfo.FrameCountExact = true
				problems := verifyOutput(info, outInfo, opts, expectedFPS0, expectedDur0)
				if len(problems) == 0 {
					releaseOutputReservation(out)
					item.Output = cand
					item.OutputInfo = outInfo
					item.Status = "skipped"
					item.Backend = "existing verified output"
					item.Message = "existing output already verified"
					rep.line(ui.green("EXISTS / VERIFIED") + " — skipping re-encode.")
					rep.line(fmt.Sprintf("Frames: %d -> %d   Size: %s -> %s", info.FrameCount, outInfo.FrameCount, humanBytes(info.SizeBytes), humanBytes(outInfo.SizeBytes)))
					return item
				}
				rep.line(ui.dim("Rejected: " + strings.Join(problems, "; ")))
			}
		}
		rep.line(ui.yellow("Existing output was stale or did not validate; preserving it and writing a numbered copy."))
	}
	item.Output = out

	started := time.Now()
	var expectedFPS *big.Rat
	var expectedDur float64

	usedNative := isXvidPreset(opts.Preset) && e.caps.HasNativeXvid && nativeXvidEligible(info)
	if usedNative && info.FrameCountExact && info.FrameCount > 0 {
		ok, preflightErr := vfwCanDecodeFrame(info.Path, info.FrameCount-1)
		if preflightErr != nil {
			usedNative = false
			rep.line(ui.yellow("Native Xvid preflight could not validate the AVI; using FFmpeg libxvid."))
			rep.line(ui.dim(preflightErr.Error()))
		} else if !ok {
			usedNative = false
			rep.line(ui.yellow("Native Xvid preflight: Windows VfW cannot decode the final source frame; using FFmpeg libxvid."))
			rep.line(ui.dim("The AVI is readable by FFmpeg, but the legacy VfW path used by xvid_encraw cannot reach the complete stream."))
		}
	}
	if usedNative {
		threads, slices := nativeXvidThreads()
		if opts.Preset == "xvid_max_q2" {
			slices = 1
			item.Backend = fmt.Sprintf("native Xvid Share Q2 (VHQ4/B2 strict Q2, %d threads/%d slice)", threads, slices)
		} else if opts.Preset == "xvid_efficient_q2" {
			slices = 1
			item.Backend = fmt.Sprintf("native Xvid Efficient Q2 (VHQ4/B2, I/P Q2 + B Q3, %d threads/%d slice)", threads, slices)
		} else if opts.Preset == "xvid_compact" {
			item.Backend = fmt.Sprintf("native Xvid Fast Q2 (VHQ1/B0, %d threads/%d slices)", threads, slices)
		} else {
			item.Backend = fmt.Sprintf("native Xvid (Q%s, %d threads/%d slices)", xvidQuant(opts.Preset), threads, slices)
		}
		rep.line("Encoder: " + item.Backend)
		expectedFPS, expectedDur, err = e.runNativeXvid(ctx, info, opts, out, func(p progressInfo) {
			rep.progress(p)
		})
		if err != nil && e.enc["libxvid"] && ctx.Err() == nil {
			rep.finish()
			rep.line(ui.yellow("Native Xvid failed its frame-integrity check; retrying with FFmpeg libxvid."))
			rep.line(ui.dim(err.Error()))
			_ = os.Remove(out)
			if opts.Preset == "xvid_max_q2" {
				item.Backend = "FFmpeg libxvid Share fallback (strict Q2 full RD/B0)"
			} else if opts.Preset == "xvid_efficient_q2" {
				item.Backend = "FFmpeg libxvid Efficient fallback (strict Q2 full RD/B0; native B-Q3 unavailable)"
			} else if opts.Preset == "xvid_compact" {
				item.Backend = "FFmpeg libxvid Fast fallback (B0)"
			} else {
				item.Backend = "FFmpeg libxvid fallback"
			}
			var args []string
			args, expectedFPS, expectedDur, err = e.buildCommand(info, opts, out)
			if err == nil {
				err = e.runFFmpeg(ctx, args, expectedDur, func(p progressInfo) {
					p.Stage = "ENCODE"
					p = exactFrameProgress(p, info.FrameCount, started)
					rep.progress(p)
				})
			}
		}
	} else if e.canUseVulkanProRes(info, opts) {
		item.Backend = "Vulkan ProRes GPU (experimental, async 4)"
		rep.line("Encoder: " + item.Backend)
		var args []string
		args, expectedFPS, expectedDur, err = e.buildProResVulkanCommand(info, opts, out)
		if err == nil {
			err = e.runFFmpeg(ctx, args, expectedDur, func(p progressInfo) {
				p.Stage = "GPU ENCODE"
				p = exactFrameProgress(p, info.FrameCount, started)
				rep.progress(p)
			})
		}
		if err != nil && e.caps.HasProRes && ctx.Err() == nil {
			rep.finish()
			rep.line(ui.yellow("Vulkan ProRes failed; retrying with CPU prores_ks."))
			rep.line(ui.dim(err.Error()))
			_ = os.Remove(out)
			item.Backend = "CPU prores_ks fallback"
			args, expectedFPS, expectedDur, err = e.buildCommand(info, opts, out)
			if err == nil {
				err = e.runFFmpeg(ctx, args, expectedDur, func(p progressInfo) {
					p.Stage = "CPU ENCODE"
					p = exactFrameProgress(p, info.FrameCount, started)
					rep.progress(p)
				})
			}
		}
	} else {
		if isXvidPreset(opts.Preset) {
			if opts.Preset == "xvid_max_q2" {
				item.Backend = "FFmpeg libxvid Share fallback (strict Q2 full RD/B0)"
			} else if opts.Preset == "xvid_efficient_q2" {
				item.Backend = "FFmpeg libxvid Efficient fallback (strict Q2 full RD/B0; native B-Q3 unavailable)"
			} else if opts.Preset == "xvid_compact" {
				item.Backend = "FFmpeg libxvid Fast fallback (B0)"
			} else {
				item.Backend = "FFmpeg libxvid fallback"
			}
		} else if isProResPreset(opts.Preset) {
			item.Backend = "CPU prores_ks"
		} else {
			item.Backend = "FFmpeg"
		}
		rep.line("Encoder: " + item.Backend)
		var args []string
		args, expectedFPS, expectedDur, err = e.buildCommand(info, opts, out)
		if err == nil {
			err = e.runFFmpeg(ctx, args, expectedDur, func(p progressInfo) {
				p.Stage = "ENCODE"
				p = exactFrameProgress(p, info.FrameCount, started)
				rep.progress(p)
			})
		}
	}
	rep.finish()
	item.Elapsed = time.Since(started)

	if err != nil {
		_ = os.Remove(out)
		if errors.Is(err, context.Canceled) {
			item.Status = "cancelled"
			return item
		}
		item.Status = "failed"
		item.Message = err.Error()
		rep.line(ui.red("ENCODE FAILED"))
		rep.line(indentError(err.Error(), 2))
		return item
	}

	outInfo, err := probeMedia(e.caps.FFprobe, out, false)
	if err != nil {
		item.Status = "failed"
		item.Message = err.Error()
		rep.line(ui.red("VERIFY PROBE FAILED"))
		rep.line(indentError(err.Error(), 2))
		return item
	}
	rep.line("Decoding output for frame/timing verification...")
	verifyStarted := time.Now()
	decodedFrames, err := e.countDecodedFrames(ctx, outInfo, func(p progressInfo) {
		p.Stage = "VERIFY"
		p = exactFrameProgress(p, info.FrameCount, verifyStarted)
		rep.progress(p)
	})
	rep.finish()
	if err != nil {
		item.Status = "failed"
		item.Message = err.Error()
		rep.line(ui.red("VERIFY DECODE FAILED"))
		rep.line(indentError(err.Error(), 2))
		return item
	}
	outInfo.FrameCount = decodedFrames
	outInfo.FrameCountExact = true
	item.OutputInfo = outInfo
	problems := verifyOutput(info, outInfo, opts, expectedFPS, expectedDur)
	if len(problems) > 0 {
		item.Status = "failed"
		item.Message = strings.Join(problems, "; ")
		rep.line(ui.red("VERIFY FAILED"))
		for _, problem := range problems {
			rep.line("- " + problem)
		}
		return item
	}

	item.Status = "ok"
	rep.line(ui.green("VERIFIED"))
	if info.FrameCount > 0 && outInfo.FrameCount > 0 {
		msg := fmt.Sprintf("Frames: %d -> %d", info.FrameCount, outInfo.FrameCount)
		if info.SizeBytes > 0 && outInfo.SizeBytes > 0 {
			msg += fmt.Sprintf("   Size: %s -> %s", humanBytes(info.SizeBytes), humanBytes(outInfo.SizeBytes))
		}
		rep.line(msg)
	}
	rep.line("Output: " + out)
	return item
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
	for _, line := range strings.Split(msg, "\n") {
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

func batchWorkerCount(opts ConvertOptions, infos []MediaInfo) int {
	if len(infos) < 2 {
		return 1
	}
	workers := 1
	if isXvidPreset(opts.Preset) {
		workers = (runtime.NumCPU() + 5) / 6
		if workers < 1 {
			workers = 1
		}
		if workers > 4 {
			workers = 4
		}
	} else if opts.Preset == "magicyuv_lossless" && runtime.NumCPU() >= 8 {
		workers = 2
	}
	if workers > len(infos) {
		workers = len(infos)
	}
	if opts.OutputDir != "" && hasDuplicateOutputStems(infos) {
		// A custom shared folder plus identical basenames from different source
		// directories can race for the same numbered filename. Serialize that
		// unusual case rather than weakening collision/recovery guarantees.
		workers = 1
	}
	return workers
}

func hasDuplicateOutputStems(infos []MediaInfo) bool {
	seen := map[string]bool{}
	for _, in := range infos {
		stem := strings.ToLower(strings.TrimSuffix(filepath.Base(in.Path), filepath.Ext(in.Path)))
		if seen[stem] {
			return true
		}
		seen[stem] = true
	}
	return false
}

func runBatch(ctx context.Context, ui theme, e *Engine, infos []MediaInfo, opts ConvertOptions) BatchResult {
	workers := batchWorkerCount(opts, infos)
	if workers <= 1 {
		result := BatchResult{Items: make([]ItemResult, 0, len(infos))}
		fmt.Println(ui.bold("CONVERTING"))
		for i, info := range infos {
			if ctx.Err() != nil {
				break
			}
			fmt.Printf("\n  [%d/%d] %s\n", i+1, len(infos), filepath.Base(info.Path))
			item := processItem(ctx, ui, e, info, opts, sequentialReporter(ui))
			addBatchItem(&result, item)
		}
		return result
	}

	fmt.Printf("%s  %s\n", ui.bold("CONVERTING"), ui.dim(fmt.Sprintf("%d workers", workers)))
	fmt.Println(ui.dim("  Multiple independent clips are encoded concurrently; per-file verification remains enabled."))
	type job struct {
		index int
		info  MediaInfo
	}
	type doneItem struct {
		index int
		item  ItemResult
	}
	jobs := make(chan job)
	done := make(chan doneItem, len(infos))
	printer := newParallelPrinter(ui, len(infos))
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				name := filepath.Base(j.info.Path)
				rep := itemReporter{
					line:     func(s string) { printer.line(j.index, name, s) },
					progress: func(pi progressInfo) { printer.progress(j.index, name, pi) },
					finish:   func() {},
				}
				printer.line(j.index, name, "START")
				item := processItem(ctx, ui, e, j.info, opts, rep)
				done <- doneItem{index: j.index, item: item}
				if ctx.Err() != nil {
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for i, info := range infos {
			select {
			case <-ctx.Done():
				return
			case jobs <- job{index: i, info: info}:
			}
		}
	}()
	go func() {
		wg.Wait()
		close(done)
	}()

	ordered := make([]ItemResult, len(infos))
	seen := make([]bool, len(infos))
	for d := range done {
		ordered[d.index] = d.item
		seen[d.index] = true
	}
	result := BatchResult{Items: make([]ItemResult, 0, len(infos))}
	for i := range ordered {
		if seen[i] {
			addBatchItem(&result, ordered[i])
		}
	}
	return result
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
			fmt.Printf("  %s %-28s  %s\n", ui.yellow("SKIP"), clipName(name, 28), item.Message)
		case "failed":
			fmt.Printf("  %s %-28s  %s\n", ui.red("FAIL"), clipName(name, 28), clipName(item.Message, 70))
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
	fmt.Println()
	if cancelled != nil {
		fmt.Println("  " + ui.yellow("Conversion cancelled."))
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

func openFolder(path string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("explorer.exe", path).Start()
	case "darwin":
		return exec.Command("open", path).Start()
	default:
		return exec.Command("xdg-open", path).Start()
	}
}

type progressInfo struct {
	Percent     float64
	FPS         string
	Speed       string
	Frame       string
	Time        float64
	Total       float64
	TotalFrames int64
	Bytes       int64
	ETA         time.Duration
	Stage       string
}

func drawProgress(ui theme, p progressInfo) {
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
	if ui.enabled {
		fmt.Printf("\r\x1b[2K      %s[%s] %6.2f%%%s", label, bar, p.Percent*100, extra)
	} else {
		fmt.Printf("\r      %s[%s] %6.2f%%%s", label, bar, p.Percent*100, extra)
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
func finishProgress(ui theme) {
	if ui.enabled {
		fmt.Print("\r\x1b[2K")
	} else {
		fmt.Print("\r")
	}
}

func askLine(label, def string) string {
	if def != "" {
		fmt.Printf("  %s [%s] > ", label, def)
	} else {
		fmt.Printf("  %s > ", label)
	}
	line, _ := stdinReader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}
func askChoice(label string, allowed []string, def string) string {
	set := map[string]bool{}
	for _, a := range allowed {
		set[a] = true
	}
	for {
		v := askLine(label, def)
		if set[v] {
			return v
		}
		fmt.Printf("  Choose one of: %s\n", strings.Join(allowed, ", "))
	}
}
func askYesNo(label string, def bool) bool {
	suffix := "y/N"
	if def {
		suffix = "Y/n"
	}
	for {
		v := strings.ToLower(strings.TrimSpace(askLine(label+" ("+suffix+")", "")))
		if v == "" {
			return def
		}
		if v == "y" || v == "yes" {
			return true
		}
		if v == "n" || v == "no" {
			return false
		}
	}
}
func clipName(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
func indentError(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 8 {
		lines = append(lines[:8], "...")
	}
	return pad + strings.Join(lines, "\n"+pad)
}

func pauseIfDoubleClicked() {
	// A failed double-clicked console otherwise disappears before the error can be read.
	if runtime.GOOS != "windows" {
		return
	}
	if len(os.Args) > 1 {
		return
	}
	fmt.Print("\nPress Enter to close...")
	_, _ = stdinReader.ReadString('\n')
}

func appDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}
func fileExists(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }
func findTool(name string) string {
	exeName := name
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	envName := "PRERECS_" + strings.ToUpper(name)
	if v := os.Getenv(envName); v != "" && fileExists(v) {
		return v
	}
	base := appDir()
	for _, c := range []string{filepath.Join(base, "tools", exeName), filepath.Join(base, exeName)} {
		if fileExists(c) {
			return c
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func findXvidEncRaw() string {
	if v := os.Getenv("PRERECS_XVID_ENCRAW"); v != "" && fileExists(v) {
		return v
	}
	exe := "xvid_encraw"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	base := appDir()
	candidates := []string{
		filepath.Join(base, "tools", exe),
		filepath.Join(base, exe),
	}
	if runtime.GOOS == "windows" {
		if pf86 := os.Getenv("ProgramFiles(x86)"); pf86 != "" {
			candidates = append(candidates, filepath.Join(pf86, "Xvid", "xvid_encraw.exe"))
		}
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			candidates = append(candidates, filepath.Join(pf, "Xvid", "xvid_encraw.exe"))
		}
		candidates = append(candidates, `C:\Program Files (x86)\Xvid\xvid_encraw.exe`, `C:\Program Files\Xvid\xvid_encraw.exe`)
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	if p, err := exec.LookPath("xvid_encraw"); err == nil {
		return p
	}
	return ""
}

func detectCapabilities() (Capabilities, map[string]bool, error) {
	ffmpeg, ffprobe := findTool("ffmpeg"), findTool("ffprobe")
	if ffmpeg == "" || ffprobe == "" {
		return Capabilities{}, nil, errors.New("FFmpeg/ffprobe not found. Put them in a tools folder next to PreRecs.exe or add them to PATH")
	}
	out, err := exec.Command(ffmpeg, "-hide_banner", "-version").CombinedOutput()
	if err != nil {
		return Capabilities{}, nil, err
	}
	versionLine := strings.Split(strings.TrimSpace(string(out)), "\n")[0]
	encOut, err := exec.Command(ffmpeg, "-hide_banner", "-encoders").CombinedOutput()
	if err != nil {
		return Capabilities{}, nil, err
	}
	enc := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(encOut)))
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) >= 2 && len(f[0]) == 6 && (strings.Contains(f[0], "V") || strings.Contains(f[0], "A")) {
			enc[f[1]] = true
		}
	}
	magic, detail := detectMagicYUV()
	xvidRaw := findXvidEncRaw()
	hasNative := xvidRaw != ""
	hasVulkanProRes := enc["prores_ks_vulkan"] && probeProResVulkan(ffmpeg)
	return Capabilities{FFmpeg: ffmpeg, FFprobe: ffprobe, Version: versionLine, HasXvid: enc["libxvid"] || hasNative, HasLibXvid: enc["libxvid"], HasNativeXvid: hasNative, XvidEncRaw: xvidRaw, HasProRes: enc["prores_ks"], HasProResVulkan: hasVulkanProRes, HasMagicYUV: enc["magicyuv"], HasUtVideo: enc["utvideo"], MagicInstalled: magic, MagicDetail: detail}, enc, nil
}

func probeProResVulkan(ffmpeg string) bool {
	// The encoder can be compiled into FFmpeg even when the installed driver
	// cannot create a Vulkan device. A single 16x16 frame catches that at startup
	// in about half a second on the reference PC, avoiding repeated failed GPU
	// attempts later in a batch.
	cmd := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-init_hw_device", "vulkan=prerecs_probe", "-filter_hw_device", "prerecs_probe",
		"-f", "lavfi", "-i", "color=size=16x16:rate=1",
		"-frames:v", "1", "-vf", "format=yuv422p10le,hwupload",
		"-c:v", "prores_ks_vulkan", "-profile:v", "1", "-async_depth", "1",
		"-f", "null", "-",
	)
	return cmd.Run() == nil
}

func detectMagicYUV() (bool, string) {
	if runtime.GOOS != "windows" {
		return false, "Windows system codec detection unavailable on this platform"
	}
	keys := []string{`HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Drivers32`, `HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows NT\CurrentVersion\Drivers32`}
	for _, k := range keys {
		out, _ := exec.Command("reg", "query", k).CombinedOutput()
		low := strings.ToLower(string(out))
		if strings.Contains(low, "magic") || strings.Contains(low, "vidc.m8") || strings.Contains(low, "vidc.m0") || strings.Contains(low, "vidc.magy") {
			return true, "MagicYUV VFW codec registration detected"
		}
	}
	return false, "MagicYUV system codec/plugin not detected"
}

func parseRat(v string) (*big.Rat, error) {
	r := new(big.Rat)
	if _, ok := r.SetString(strings.TrimSpace(v)); !ok || r.Sign() <= 0 {
		return nil, fmt.Errorf("invalid positive number/fraction %q", v)
	}
	return r, nil
}
func parseRatAllowZero(v string) (*big.Rat, error) {
	if v == "" || v == "0/0" || v == "N/A" {
		return nil, nil
	}
	r := new(big.Rat)
	if _, ok := r.SetString(v); !ok || r.Sign() <= 0 {
		return nil, errors.New("bad rational")
	}
	return r, nil
}
func ratFloat(r *big.Rat) float64 { f, _ := r.Float64(); return f }
func ratString(r *big.Rat) string { return r.Num().String() + "/" + r.Denom().String() }
func parseFloat(v string) float64 { f, _ := strconv.ParseFloat(v, 64); return f }
func parseInt64(v string) int64   { n, _ := strconv.ParseInt(v, 10, 64); return n }
func parseInt(v string) int       { n, _ := strconv.Atoi(v); return n }

func metadataFrameCountTrusted(codec string) bool {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "lagarith", "magicyuv", "ffv1", "huffyuv", "utvideo", "rawvideo", "prores", "dnxhd", "cfhd":
		return true
	default:
		return false
	}
}

func probeMedia(ffprobe, path string, count bool) (MediaInfo, error) {
	args := []string{"-v", "error"}
	if count {
		args = append(args, "-count_frames", "-count_packets")
	}
	args = append(args, "-show_streams", "-show_format", "-of", "json", path)
	out, err := exec.Command(ffprobe, args...).CombinedOutput()
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
	fpsRat, _ := parseRatAllowZero(sv.AvgFrameRate)
	fpsFloat := 0.0
	if fpsRat != nil {
		fpsFloat = ratFloat(fpsRat)
	}
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
	if count && parseInt64(sv.NBReadFrames) > 0 {
		frames = parseInt64(sv.NBReadFrames)
		frameCountExact = true
	}
	if frames <= 0 && dur > 0 && fpsFloat > 0 {
		frames = int64(math.Round(dur * fpsFloat))
		frameCountExact = false
	}
	bit := parseInt(sv.BitsPerRawSample)
	if bit == 0 {
		bit = deriveBitDepth(sv.PixFmt)
	}
	info := MediaInfo{Path: path, Codec: sv.CodecName, CodecTag: sv.CodecTagString, Profile: sv.Profile, Width: sv.Width, Height: sv.Height, PixelFormat: sv.PixFmt, BitDepth: bit, FPS: sv.AvgFrameRate, FPSFloat: fpsFloat, Duration: dur, FrameCount: frames, FrameCountExact: frameCountExact, SizeBytes: parseInt64(doc.Format.Size), BitRate: parseInt64(doc.Format.BitRate), ColorRange: sv.ColorRange, ColorSpace: sv.ColorSpace, ColorTransfer: sv.ColorTransfer, ColorPrimaries: sv.ColorPrimaries, HasAlpha: hasAlpha(sv.PixFmt), Chroma: chroma(sv.PixFmt), Audio: []AudioInfo{}}
	for _, sa := range doc.Streams {
		if sa.CodecType != "audio" {
			continue
		}
		lang := ""
		if sa.Tags != nil {
			lang = sa.Tags["language"]
		}
		info.Audio = append(info.Audio, AudioInfo{Index: sa.Index, Codec: sa.CodecName, Channels: sa.Channels, SampleRate: parseInt(sa.SampleRate), Language: lang})
	}
	return info, nil
}

func deriveBitDepth(p string) int {
	low := strings.ToLower(p)
	for _, n := range []int{16, 14, 12, 10} {
		if strings.Contains(low, strconv.Itoa(n)) {
			return n
		}
	}
	if low != "" {
		return 8
	}
	return 0
}
func hasAlpha(p string) bool {
	low := strings.ToLower(p)
	return strings.HasPrefix(low, "rgba") || strings.HasPrefix(low, "bgra") || strings.HasPrefix(low, "argb") || strings.HasPrefix(low, "abgr") || strings.Contains(low, "yuva") || strings.Contains(low, "gbrap")
}
func isRGBPixelFormat(p string) bool {
	low := strings.ToLower(strings.TrimSpace(p))
	return strings.HasPrefix(low, "rgb") || strings.HasPrefix(low, "bgr") || strings.HasPrefix(low, "gbr")
}
func chroma(p string) string {
	low := strings.ToLower(p)
	if strings.Contains(low, "444") || strings.HasPrefix(low, "rgb") || strings.HasPrefix(low, "gbr") {
		return "4:4:4"
	}
	if strings.Contains(low, "422") {
		return "4:2:2"
	}
	if strings.Contains(low, "420") {
		return "4:2:0"
	}
	return ""
}

func lossless8BitPixFmt(info MediaInfo, codec string) (string, error) {
	if info.BitDepth > 8 {
		return "", fmt.Errorf("%s through this FFmpeg build supports only the tested 8-bit pixel formats; source is %d-bit. Use ProRes 422/4444 instead", codec, info.BitDepth)
	}
	if info.HasAlpha {
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

func expectedTiming(info MediaInfo, req ConvertOptions) (*big.Rat, *big.Rat, float64, error) {
	sourceFPS, err := parseRatAllowZero(info.FPS)
	if err != nil {
		return nil, nil, 0, err
	}
	target := sourceFPS
	expectedDur := info.Duration
	if req.Conform {
		if req.CaptureFPS != "" {
			sourceFPS, err = parseRat(req.CaptureFPS)
			if err != nil {
				return nil, nil, 0, err
			}
		}
		if sourceFPS == nil {
			return nil, nil, 0, errors.New("capture FPS is required because source FPS could not be determined")
		}
		scale, err := parseRat(req.Timescale)
		if err != nil {
			return nil, nil, 0, err
		}
		target = new(big.Rat).Quo(sourceFPS, scale)
		if ratFloat(target) > 2000 {
			return nil, nil, 0, fmt.Errorf("target FPS %.3f is unreasonably high", ratFloat(target))
		}
		if info.FrameCount > 0 {
			expectedDur = float64(info.FrameCount) / ratFloat(target)
		} else if info.Duration > 0 {
			expectedDur = info.Duration * ratFloat(sourceFPS) / ratFloat(target)
		}
	}
	return sourceFPS, target, expectedDur, nil
}

func conformVideoFilters(target *big.Rat) []string {
	if target == nil || target.Sign() <= 0 {
		return nil
	}
	// Give every captured frame exactly one integer timestamp in the target
	// frame-rate timebase, then let fps normalize frame duration. This avoids
	// the rounding bug caused by setpts=N/(fps*TB) when TB is still the source
	// timebase (e.g. 1/30 while conforming to 300 fps).
	tb := target.Denom().String() + "/" + target.Num().String()
	fps := ratString(target)
	return []string{
		"settb=expr=" + tb,
		"setpts=N",
		"fps=" + fps,
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
	if info.HasAlpha {
		return "yuva444p10le"
	}
	return "yuv444p10le"
}

func proresRGBConversionFilters(info MediaInfo, preset string) []string {
	if !isProResPreset(preset) || !(isRGBPixelFormat(info.PixelFormat) || strings.EqualFold(info.ColorSpace, "gbr")) {
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
			"split=2[c][a];[c]format=gbrp,scale=out_color_matrix=bt709:out_range=tv,format=yuv444p10le[c10];[a]alphaextract,format=gray[a8];[c10][a8]alphamerge",
		}
	}
	return []string{"format=gbrp", "scale=out_color_matrix=bt709:out_range=tv", "format=" + pix}
}

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

	sourceFPS, target, expectedDur, err := expectedTiming(info, req)
	if err != nil {
		return nil, nil, 0, err
	}
	args := []string{"-hide_banner", "-nostdin", "-y", "-init_hw_device", "vulkan=prerecs_vk", "-filter_hw_device", "prerecs_vk", "-i", info.Path, "-map", "0:v:0"}
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
	alphaBits := "0"
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
	args = append(args, "-c:v", "prores_ks_vulkan", "-profile:v", profile, "-quant_mat", "auto", "-alpha_bits", alphaBits, "-async_depth", "4")
	args = append(args, e.audioArgs(info, req, sourceFPS, target, false)...)
	args = append(args, "-movflags", "+write_colr")
	args = append(args, colorOutputArgs(info, req.Preset)...)
	args = append(args, "-progress", "pipe:1", "-stats_period", "0.25", "-nostats", out)
	return args, target, expectedDur, nil
}

func (e *Engine) buildCommand(info MediaInfo, req ConvertOptions, out string) ([]string, *big.Rat, float64, error) {
	if err := validatePresetInputs(req.Preset, []MediaInfo{info}); err != nil {
		return nil, nil, 0, err
	}
	args := []string{"-hide_banner", "-nostdin", "-y", "-i", info.Path, "-map", "0:v:0"}
	filters := []string{}
	if cf := colorFilter(info); cf != "" {
		filters = append(filters, cf)
	}
	filters = append(filters, proresRGBConversionFilters(info, req.Preset)...)
	sourceFPS, target, expectedDur, err := expectedTiming(info, req)
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
		args = append(args, e.audioArgs(info, req, sourceFPS, target, true)...)
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
		args = append(args, e.audioArgs(info, req, sourceFPS, target, false)...)
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
		args = append(args, e.audioArgs(info, req, sourceFPS, target, false)...)
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
		args = append(args, e.audioArgs(info, req, sourceFPS, target, false)...)
	default:
		return nil, nil, 0, fmt.Errorf("unknown preset %q", req.Preset)
	}
	args = append(args, colorOutputArgs(info, req.Preset)...)
	args = append(args, "-progress", "pipe:1", "-stats_period", "0.25", "-nostats", out)
	return args, target, expectedDur, nil
}

func (e *Engine) audioArgs(info MediaInfo, req ConvertOptions, source, target *big.Rat, compact bool) []string {
	if req.StripAudio {
		return []string{"-an"}
	}
	if req.Conform {
		return []string{"-an"}
	}
	if !req.Conform {
		return []string{"-map", "0:a?", "-c:a", "copy"}
	}
	return []string{"-an"}
}

func colorFilter(i MediaInfo) string {
	p := []string{}
	if i.ColorRange == "tv" {
		p = append(p, "range=limited")
	} else if i.ColorRange == "pc" {
		p = append(p, "range=full")
	}
	if i.ColorSpace != "" && i.ColorSpace != "unknown" {
		p = append(p, "colorspace="+i.ColorSpace)
	}
	if i.ColorTransfer != "" && i.ColorTransfer != "unknown" {
		p = append(p, "color_trc="+i.ColorTransfer)
	}
	if i.ColorPrimaries != "" && i.ColorPrimaries != "unknown" {
		p = append(p, "color_primaries="+i.ColorPrimaries)
	}
	if len(p) == 0 {
		return ""
	}
	return "setparams=" + strings.Join(p, ":")
}
func colorOutputArgs(i MediaInfo, preset string) []string {
	a := []string{}
	rgbToProRes := strings.HasPrefix(preset, "prores") && (isRGBPixelFormat(i.PixelFormat) || strings.EqualFold(i.ColorSpace, "gbr"))
	if rgbToProRes {
		// ProRes is encoded as YUV in this build. RGB/GBR inputs are explicitly
		// converted to limited-range BT.709 before encoding, so write metadata
		// describing those encoded YUV planes rather than the source RGB matrix.
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
	if !rgbToProRes && i.ColorSpace != "" && i.ColorSpace != "unknown" && !(yuvOutput && i.ColorSpace == "gbr") {
		a = append(a, "-colorspace", i.ColorSpace)
	}
	if i.ColorTransfer != "" && i.ColorTransfer != "unknown" {
		a = append(a, "-color_trc", i.ColorTransfer)
	}
	if i.ColorPrimaries != "" && i.ColorPrimaries != "unknown" {
		a = append(a, "-color_primaries", i.ColorPrimaries)
	}
	return a
}

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
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if errBuf.Len() > 32000 {
				continue
			}
			errBuf.WriteString(sc.Text())
			errBuf.WriteByte('\n')
		}
	}()

	started := time.Now()
	pi := progressInfo{Total: expectedDur}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "frame":
			pi.Frame = v
		case "fps":
			pi.FPS = strings.TrimSpace(v)
		case "speed":
			pi.Speed = strings.TrimSpace(v)
		case "total_size":
			pi.Bytes = parseInt64(strings.TrimSpace(v))
		case "out_time_us":
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
	err = cmd.Wait()
	<-done
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("ffmpeg: %s", strings.TrimSpace(errBuf.String()))
	}
	return nil
}

func (e *Engine) countDecodedFrames(ctx context.Context, info MediaInfo, progress func(progressInfo)) (int64, error) {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
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

func isXvidPreset(p string) bool {
	return p == "xvid_compact" || p == "xvid_max_q2" || p == "xvid_efficient_q2" || p == "xvid_small" || p == "xvid_max"
}

func isTunedXvidQ2(p string) bool {
	return p == "xvid_max_q2" || p == "xvid_efficient_q2"
}

func xvidQuant(p string) string {
	switch p {
	case "xvid_small":
		return "3"
	case "xvid_max":
		return "1"
	default:
		return "2"
	}
}

func nativeXvidThreads() (int, int) {
	cpus := runtime.NumCPU()
	threads := cpus
	if threads > 8 {
		threads = 8
	}
	if threads < 1 {
		threads = 1
	}
	slices := 1
	if cpus >= 4 {
		slices = 4
	} else if cpus >= 2 {
		slices = 2
	}
	return threads, slices
}

func formatFPSFloat(r *big.Rat) string {
	if r == nil {
		return "30"
	}
	return strconv.FormatFloat(ratFloat(r), 'f', 6, 64)
}

func nativeXvidEligible(info MediaInfo) bool {
	if strings.ToLower(filepath.Ext(info.Path)) != ".avi" {
		return false
	}
	class := sourceClass(info)
	return class == "lossless" || strings.EqualFold(info.Codec, "rawvideo")
}

func nativeXvidNeedsExactFrameScan(info MediaInfo) bool {
	if !nativeXvidEligible(info) || !info.FrameCountExact || info.FrameCount <= 0 {
		return false
	}
	if info.FPSFloat <= 0 || info.Duration <= 0 {
		return true
	}
	expected := int64(math.Round(info.Duration * info.FPSFloat))
	return expected <= 0 || expected != info.FrameCount
}

func nativeXvidArgs(info MediaInfo, req ConvertOptions, tmpVideo string, target *big.Rat) []string {
	threads, slices := nativeXvidThreads()
	q := xvidQuant(req.Preset)
	vhq := "1"
	maxB := "0"
	if isTunedXvidQ2(req.Preset) {
		vhq = "4"
		maxB = "2"
		slices = 1
	}
	bmin, bmax := q, q
	if req.Preset == "xvid_efficient_q2" {
		// Keep I/P references at strict Q2 but allow Xvid's B quantizer
		// formula to select Q3 (ratio 100 + offset 100) for disposable B-VOPs.
		bmin, bmax = "2", "31"
	}
	xargs := []string{
		"-i", info.Path, "-type", "2",
		"-o", tmpVideo, "-framerate", formatFPSFloat(target),
		"-cq", q, "-quality", "6", "-vhqmode", vhq,
		"-max_bframes", maxB, "-max_key_interval", "240", "-nopacked",
		"-imin", q, "-imax", q, "-pmin", q, "-pmax", q, "-bmin", bmin, "-bmax", bmax,
		"-threads", strconv.Itoa(threads), "-slices", strconv.Itoa(slices), "-progress", "1000000",
	}
	if req.Preset == "xvid_max_q2" {
		xargs = append(xargs, "-bvhq", "-bquant_ratio", "100", "-bquant_offset", "0", "-metric", "0")
	} else if req.Preset == "xvid_efficient_q2" {
		xargs = append(xargs, "-bvhq", "-bquant_ratio", "100", "-bquant_offset", "100", "-metric", "0")
	}
	if info.FrameCount > 0 {
		xargs = append(xargs, "-frames", strconv.FormatInt(info.FrameCount, 10))
	}
	return xargs
}

func parseXvidProgressLine(line string) (frames int64, percent float64, fps string, ok bool) {
	var n int64
	var pct int
	var rate float64
	if _, err := fmt.Sscanf(strings.TrimSpace(line), "%d frames(%d%%) encoded, %f fps,", &n, &pct, &rate); err != nil {
		return 0, 0, "", false
	}
	return n, math.Max(0, math.Min(1, float64(pct)/100)), fmt.Sprintf("%.2f", rate), true
}

type mpeg4VOPCounter struct {
	offset int64
	carry  []byte
	count  int64
}

func (c *mpeg4VOPCounter) poll(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c.count, nil
		}
		return c.count, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return c.count, err
	}
	if st.Size() < c.offset {
		c.offset = 0
		c.carry = nil
		c.count = 0
	}
	if _, err := f.Seek(c.offset, io.SeekStart); err != nil {
		return c.count, err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return c.count, err
	}
	if len(b) == 0 {
		return c.count, nil
	}
	combined := make([]byte, 0, len(c.carry)+len(b))
	combined = append(combined, c.carry...)
	combined = append(combined, b...)
	for i := 0; i+3 < len(combined); i++ {
		// MPEG-4 Part 2 VOP start code: 00 00 01 B6. Every coded VOP is one
		// picture, including reordered B-VOPs, so this remains a frame counter.
		if combined[i] == 0x00 && combined[i+1] == 0x00 && combined[i+2] == 0x01 && combined[i+3] == 0xB6 {
			c.count++
		}
	}
	if len(combined) > 3 {
		c.carry = append(c.carry[:0], combined[len(combined)-3:]...)
	} else {
		c.carry = append(c.carry[:0], combined...)
	}
	c.offset += int64(len(b))
	return c.count, nil
}

func (e *Engine) runNativeXvid(ctx context.Context, info MediaInfo, req ConvertOptions, out string, progress func(progressInfo)) (*big.Rat, float64, error) {
	if !e.caps.HasNativeXvid || e.caps.XvidEncRaw == "" {
		return nil, 0, errors.New("native Xvid encoder not available")
	}
	if !nativeXvidEligible(info) {
		return nil, 0, errors.New("native Xvid direct path is only used for compatible AVI lossless masters")
	}
	_, target, expectedDur, err := expectedTiming(info, req)
	if err != nil {
		return nil, 0, err
	}
	if target == nil {
		return nil, 0, errors.New("source FPS could not be determined")
	}

	// Raw MPEG-4 Part 2 output is intentional. xvid_encraw's AVIFile writer
	// locks the AVI during encoding and can leave inconsistent RIFF size fields.
	// The elementary stream stays readable while it grows, which lets PreRecs
	// count VOP start codes for genuinely live frame progress. FFmpeg wraps the
	// exact packets into a clean AVI after the encode; no video re-encode occurs.
	tmpVideo := out + ".video.tmp.m4v"
	_ = os.Remove(tmpVideo)
	defer os.Remove(tmpVideo)

	progress(progressInfo{Percent: 0, Total: expectedDur, TotalFrames: info.FrameCount, Stage: "ENCODE"})
	xargs := nativeXvidArgs(info, req, tmpVideo, target)
	cmd := exec.CommandContext(ctx, e.caps.XvidEncRaw, xargs...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	counter := &mpeg4VOPCounter{}
	lastFrames := int64(-1)
	var waitErr error
	finished := false

	for !finished {
		select {
		case <-ctx.Done():
			waitErr = ctx.Err()
			finished = true
		case err := <-done:
			waitErr = err
			finished = true
			frames, _ := counter.poll(tmpVideo)
			if frames > lastFrames {
				lastFrames = frames
				pct := 0.0
				if info.FrameCount > 0 {
					pct = math.Max(0, math.Min(1, float64(frames)/float64(info.FrameCount)))
				}
				elapsed := time.Since(started).Seconds()
				fps := ""
				if elapsed > 0 && frames > 0 {
					fps = fmt.Sprintf("%.2f", float64(frames)/elapsed)
				}
				progress(progressInfo{Percent: pct, FPS: fps, Frame: strconv.FormatInt(frames, 10), TotalFrames: info.FrameCount, Total: expectedDur, ETA: etaFromProgress(started, pct), Stage: "ENCODE"})
			}
		case <-ticker.C:
			frames, pollErr := counter.poll(tmpVideo)
			if pollErr != nil || frames == lastFrames {
				continue
			}
			lastFrames = frames
			pct := 0.0
			if info.FrameCount > 0 {
				pct = math.Max(0, math.Min(1, float64(frames)/float64(info.FrameCount)))
			}
			elapsed := time.Since(started).Seconds()
			fps := ""
			if elapsed > 0 && frames > 0 {
				fps = fmt.Sprintf("%.2f", float64(frames)/elapsed)
			}
			progress(progressInfo{Percent: pct, FPS: fps, Frame: strconv.FormatInt(frames, 10), TotalFrames: info.FrameCount, Total: expectedDur, ETA: etaFromProgress(started, pct), Stage: "ENCODE"})
		}
	}
	if ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return nil, 0, fmt.Errorf("native Xvid failed: %s", tailText(msg, 14))
	}
	if !fileExists(tmpVideo) {
		return nil, 0, errors.New("native Xvid did not create an output stream")
	}
	finalFrames, _ := counter.poll(tmpVideo)
	if info.FrameCount > 0 && finalFrames != info.FrameCount {
		return nil, 0, fmt.Errorf("native Xvid wrote %d VOP frames; expected %d", finalFrames, info.FrameCount)
	}
	progress(progressInfo{Percent: 1, FPS: fmt.Sprintf("%.2f", float64(max64(finalFrames, info.FrameCount))/math.Max(.001, time.Since(started).Seconds())), Frame: strconv.FormatInt(max64(finalFrames, info.FrameCount), 10), TotalFrames: info.FrameCount, Total: expectedDur, Stage: "ENCODE"})

	needAudio := len(info.Audio) > 0 && !req.StripAudio && !req.Conform
	remux := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-r", formatFPSFloat(target), "-f", "m4v", "-i", tmpVideo}
	if needAudio {
		remux = append(remux, "-i", info.Path, "-map", "0:v:0", "-map", "1:a?")
	} else {
		remux = append(remux, "-map", "0:v:0")
	}
	remux = append(remux, "-c:v", "copy", "-vtag", "XVID")
	if needAudio {
		remux = append(remux, "-c:a", "copy")
	}
	remux = append(remux, "-progress", "pipe:1", "-stats_period", "0.25", "-nostats", out)
	if err := e.runFFmpeg(ctx, remux, expectedDur, func(p progressInfo) {
		p.Stage = "WRAP"
		p.TotalFrames = info.FrameCount
		progress(p)
	}); err != nil {
		return nil, 0, fmt.Errorf("AVI wrap/remux failed: %w", err)
	}
	return target, expectedDur, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func tailText(s string, lines int) string {
	p := strings.Split(strings.TrimSpace(s), "\n")
	if len(p) > lines {
		p = p[len(p)-lines:]
	}
	return strings.Join(p, "\n")
}

func outputCandidates(src, custom, preset string) ([]string, string, error) {
	base := custom
	if base == "" {
		base = filepath.Join(filepath.Dir(src), "converted_prerecs")
	}
	if err := os.MkdirAll(base, 0755); err != nil {
		return nil, "", err
	}
	ext := ".mov"
	if strings.HasPrefix(preset, "xvid") || preset == "magicyuv_lossless" || preset == "utvideo_lossless" {
		ext = ".avi"
	}
	stem := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	existing := []string{}
	for n := 1; n < 10000; n++ {
		name := stem + "_" + preset + ext
		if n > 1 {
			name = fmt.Sprintf("%s_%s_%d%s", stem, preset, n, ext)
		}
		p := filepath.Join(base, name)
		if fileExists(p) {
			existing = append(existing, p)
			continue
		}
		reservation, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			if closeErr := reservation.Close(); closeErr != nil {
				_ = os.Remove(p)
				return existing, "", closeErr
			}
			return existing, p, nil
		}
		if errors.Is(err, os.ErrExist) {
			existing = append(existing, p)
			continue
		}
		return existing, "", fmt.Errorf("reserve output %s: %w", p, err)
	}
	return existing, "", errors.New("could not choose unused output filename")
}

func releaseOutputReservation(path string) {
	_ = os.Remove(path)
}

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
				if !strings.EqualFold(in.Audio[i].Codec, out.Audio[i].Codec) {
					p = append(p, fmt.Sprintf("audio codec mismatch on track %d: expected %s got %s", i+1, in.Audio[i].Codec, out.Audio[i].Codec))
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
	if in.FrameCount > 0 && out.FrameCount > 0 && in.FrameCount != out.FrameCount {
		p = append(p, fmt.Sprintf("frame count mismatch: %d input vs %d output", in.FrameCount, out.FrameCount))
	}
	if expected != nil && out.FPSFloat > 0 {
		e := ratFloat(expected)
		if math.Abs(e-out.FPSFloat) > math.Max(.01, e*.002) {
			p = append(p, fmt.Sprintf("frame rate mismatch: expected %.6g got %.6g", e, out.FPSFloat))
		}
	}
	if expectedDur > 0 && out.Duration > 0 {
		period := .05
		if expected != nil {
			period = 1 / ratFloat(expected)
		}
		if math.Abs(expectedDur-out.Duration) > math.Max(.05, period*2.5) {
			p = append(p, fmt.Sprintf("duration mismatch: expected ~%.4fs got %.4fs", expectedDur, out.Duration))
		}
	}
	return p
}
