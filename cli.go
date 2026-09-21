package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var stdinReader = bufio.NewReader(os.Stdin)

type cliConfig struct {
	preset       string
	timescale    string
	captureFPS   string
	stripAudio   bool
	outputDir    string
	yes          bool
	forceXvid    bool
	cpuProRes    bool
	noNativeXvid bool
	plain        bool
	showVersion  bool
}

// flagNeedsValue lists every option that consumes a following argument; all
// other recognised options are booleans.
var flagNeedsValue = map[string]bool{
	"preset": true, "timescale": true, "capture-fps": true, "output": true,
}

var flagBool = map[string]bool{
	"strip-audio": true, "yes": true, "force-xvid": true, "cpu-prores": true,
	"no-native-xvid": true, "plain": true, "version": true, "h": true, "help": true,
}

// reorderArgs moves recognised options ahead of positional paths before
// flag.Parse sees them. The standard flag package stops parsing at the first
// non-flag argument, which would treat `PreRecs file.avi --preset share` as
// three filenames. Quoting is already resolved by the OS at this point, and
// `--` terminates option handling so dash-prefixed paths remain usable.
func reorderArgs(args []string) ([]string, error) {
	flags := []string{}
	positional := []string{}
	endFlags := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if endFlags {
			positional = append(positional, a)
			continue
		}
		if a == "--" {
			endFlags = true
			continue
		}
		if len(a) > 1 && a[0] == '-' {
			name := strings.TrimLeft(a, "-")
			hasInline := false
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name = name[:eq]
				hasInline = true
			}
			switch {
			case flagNeedsValue[name]:
				flags = append(flags, a)
				if !hasInline {
					if i+1 >= len(args) {
						return nil, fmt.Errorf("flag needs an argument: %s", a)
					}
					i++
					flags = append(flags, args[i])
				}
			case flagBool[name]:
				flags = append(flags, a)
			default:
				return nil, fmt.Errorf("unknown option %s", a)
			}
			continue
		}
		positional = append(positional, a)
	}
	// Re-insert the end-of-options marker so positionals that begin with '-'
	// are not re-parsed as flags by flag.Parse.
	return append(flags, append([]string{"--"}, positional...)...), nil
}

// errShowHelp is the sentinel parseFlags returns for -h/-help in any spelling
// the flag package itself recognizes. The real parser — not a parallel
// pre-scan — decides what is help, so help detection can never disagree with
// flag.Parse (value consumption, `--` termination, unknown-option and
// bad-value errors all win over a help token, exactly as flag.Parse orders
// them).
var errShowHelp = errors.New("help requested")

// newFlagSet builds the flag set parseFlags uses. Registration lives in one
// place so tests can assert the reorderArgs classification tables stay in sync
// with it (a mis-tabled flag would corrupt the reorder, e.g. leak a value into
// the flags region or kill the ErrHelp path for -h).
func newFlagSet(c *cliConfig) *flag.FlagSet {
	fs := flag.NewFlagSet("prerecs", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // help/errors are reported by the caller, not flag
	fs.StringVar(&c.preset, "preset", "", "share|compact|xvid|xvid-q2|xvid-efficient|xvid-fast|edit|lossless|prores|hq|xvid-max-q2|xvid-small|xvid-max|4444|magicyuv|utvideo")
	fs.StringVar(&c.timescale, "timescale", "", "game timescale, e.g. 0.1")
	fs.StringVar(&c.captureFPS, "capture-fps", "", "capture FPS override, e.g. 30 or 60000/1001")
	fs.BoolVar(&c.stripAudio, "strip-audio", false, "remove audio instead of keeping it")
	fs.StringVar(&c.outputDir, "output", "", "custom output directory")
	fs.BoolVar(&c.yes, "yes", false, "skip final confirmation")
	fs.BoolVar(&c.forceXvid, "force-xvid", false, "force Xvid even when the source is already distribution-compressed")
	fs.BoolVar(&c.cpuProRes, "cpu-prores", false, "disable experimental Vulkan ProRes fast path and force CPU prores_ks")
	fs.BoolVar(&c.noNativeXvid, "no-native-xvid", false, "disable the native xvid_encraw path and always use FFmpeg libxvid")
	fs.BoolVar(&c.plain, "plain", false, "disable ANSI colors/progress styling")
	fs.BoolVar(&c.showVersion, "version", false, "print version")
	// h/help are intentionally NOT registered: flag.Parse returns ErrHelp for
	// undefined h/help, which parseFlags maps to errShowHelp.
	return fs
}

func parseFlags(args []string) (cliConfig, []string, error) {
	var c cliConfig
	fs := newFlagSet(&c)
	reordered, err := reorderArgs(args)
	if err != nil {
		return c, nil, err
	}
	if err := fs.Parse(reordered); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return c, nil, errShowHelp
		}
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
  --no-native-xvid         disable the native xvid_encraw path; use FFmpeg libxvid
  --plain                  disable ANSI styling
  --version                print version
  -h, --help               show this usage

Examples:
  PreRecs.exe "clip.mp4"
  PreRecs.exe --preset share --yes "master.avi"
  PreRecs.exe --preset edit --timescale 0.1 --capture-fps 30 "cinematic.mp4"`)
}

func collectOptions(cfg cliConfig, ui theme, e *Engine, infos []MediaInfo) (ConvertOptions, error) {
	opts := ConvertOptions{OutputDir: cfg.outputDir, StripAudio: cfg.stripAudio, ForceXvid: cfg.forceXvid, CPUProRes: cfg.cpuProRes, NoNativeXvid: cfg.noNativeXvid}
	// Validate CLI timing flags once up front so a bad value fails fast instead
	// of failing every item mid-batch.
	if cfg.timescale != "" {
		if _, err := parseRat(cfg.timescale); err != nil {
			return opts, fmt.Errorf("invalid --timescale value: %w", err)
		}
	}
	if cfg.captureFPS != "" {
		if _, err := parseRat(cfg.captureFPS); err != nil {
			return opts, fmt.Errorf("invalid --capture-fps value: %w", err)
		}
	}
	if cfg.preset != "" {
		p, err := normalizePreset(cfg.preset)
		if err != nil {
			return opts, err
		}
		opts.Preset = p
	} else {
		// Show the menu through the --no-native-xvid mask so it cannot offer
		// a backend the flag disabled for this session.
		masked := &Engine{caps: nativeXvidCaps(e.caps, cfg.noNativeXvid), enc: e.enc}
		var chooseErr error
		opts.Preset, chooseErr = choosePreset(ui, masked, infos)
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
					fmt.Printf("    - %s (%s, %s)\n", strictConsoleText(filepath.Base(in.Path)), strictConsoleText(strings.ToUpper(in.Codec)), humanBytes(in.SizeBytes))
				}
				fallbackPreset := recommendedProResPreset(infos)
				fallbackLabel := "ProRes 422 LT"
				if fallbackPreset == "prores_4444" {
					fallbackLabel = "ProRes 4444"
				}
				fmt.Println(ui.dim("  Xvid cannot restore lost detail and may become much larger than the source."))
				fmt.Println("  1. Skip these files / keep originals " + ui.green("recommended"))
				fmt.Println("  2. Force Xvid anyway")
				fmt.Printf("  3. Switch this job to %s\n", fallbackLabel)
				c, err := askChoice("Choose", []string{"1", "2", "3"}, "1")
				if err != nil {
					return opts, err
				}
				switch c {
				case "1":
					opts.SkipCompressed = true
				case "2":
					opts.ForceXvid = true
				case "3":
					opts.Preset = fallbackPreset
				}
				fmt.Println()
			}
		}
	}
	// After the compressed-source prompt: it can switch the job to ProRes, so
	// encoder availability must be checked against the final preset.
	if err := presetEncoderAvailable(e.caps, e.enc, opts.Preset); err != nil {
		return opts, err
	}
	if err := validatePresetInputs(opts.Preset, infos); err != nil {
		return opts, err
	}
	if isXvidPreset(opts.Preset) {
		skipCompressed := opts.SkipCompressed && !opts.ForceXvid
		if ok, missing := xvidAvailableForInputs(nativeXvidCaps(e.caps, cfg.noNativeXvid), infos, skipCompressed); !ok {
			return opts, fmt.Errorf("selected Xvid preset cannot run for %s: install FFmpeg libxvid or choose a compatible preset", strings.Join(missing, ", "))
		}
	}

	if cfg.timescale != "" {
		opts.Conform = true
		opts.Timescale = cfg.timescale
		opts.CaptureFPS = cfg.captureFPS
	} else if !cfg.yes {
		fmt.Println(ui.bold("TIMING"))
		conform, err := askYesNo("Was this captured with reduced in-game timescale?", false)
		if err != nil {
			return opts, err
		}
		if conform {
			opts.Conform = true
			if opts.CaptureFPS, err = askLine("Capture FPS override (Enter = detected FPS)", ""); err != nil {
				return opts, err
			}
			if opts.Timescale, err = askLine("Game timescale", "0.1"); err != nil {
				return opts, err
			}
			if _, err := parseRat(opts.Timescale); err != nil {
				return opts, err
			}
			if opts.CaptureFPS != "" {
				if _, err := parseRat(opts.CaptureFPS); err != nil {
					return opts, err
				}
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
			choice, err := askChoice("Choose", []string{"1", "2"}, "1")
			if err != nil {
				return opts, err
			}
			opts.StripAudio = choice == "2"
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
				strip, err := askYesNo("Strip audio and continue?", true)
				if err != nil {
					return opts, err
				}
				if strip {
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
		out, err := askLine("Folder (Enter = converted_prerecs beside each source)", "")
		if err != nil {
			return opts, err
		}
		opts.OutputDir = out
	}
	return opts, nil
}

func askLine(label, def string) (string, error) {
	if def != "" {
		fmt.Printf("  %s [%s] > ", label, def)
	} else {
		fmt.Printf("  %s > ", label)
	}
	line, err := stdinReader.ReadString('\n')
	line = strings.TrimSpace(line)
	if err != nil && line == "" {
		// A genuinely exhausted stdin must surface — including a trailing
		// whitespace-only fragment — because swallowing it turns prompts into
		// infinite loops and lets --yes silently accept the default answer.
		return "", err
	}
	if line == "" {
		return def, nil
	}
	return line, nil
}

func askChoice(label string, allowed []string, def string) (string, error) {
	set := map[string]bool{}
	for _, a := range allowed {
		set[a] = true
	}
	for {
		v, err := askLine(label, def)
		if err != nil {
			return "", err
		}
		if set[v] {
			return v, nil
		}
		fmt.Printf("  Choose one of: %s\n", strings.Join(allowed, ", "))
	}
}

func askYesNo(label string, def bool) (bool, error) {
	suffix := "y/N"
	if def {
		suffix = "Y/n"
	}
	for {
		v, err := askLine(label+" ("+suffix+")", "")
		if err != nil {
			return false, err
		}
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			return def, nil
		}
		if v == "y" || v == "yes" {
			return true, nil
		}
		if v == "n" || v == "no" {
			return false, nil
		}
	}
}
