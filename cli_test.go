package main

import (
	"bufio"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
	"time"
)

func TestInteractiveNativeOnlyXvidCanSkipCompressedInputs(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	e := &Engine{
		caps: Capabilities{HasXvid: true, HasNativeXvid: true, HasLibXvid: false},
		enc:  map[string]bool{},
	}
	infos := []MediaInfo{
		{Path: `C:\clips\master.avi`, Codec: "lagarith"},
		{Path: `C:\clips\download.mp4`, Codec: "h264"},
	}
	stdinReader = bufio.NewReader(strings.NewReader("1\n1\nn\n"))
	opts, err := collectOptions(cliConfig{outputDir: t.TempDir()}, theme{}, e, infos)
	if err != nil {
		t.Fatalf("interactive native-only mixed batch was rejected before skip choice: %v", err)
	}
	if opts.Preset != "xvid_max_q2" || !opts.SkipCompressed {
		t.Fatalf("options=%+v, want SHARE with compressed input skipped", opts)
	}
}

func TestCollectOptionsRejectsUnsupportedXvidInputEarly(t *testing.T) {
	e := &Engine{
		caps: Capabilities{HasXvid: true, HasNativeXvid: true, HasLibXvid: false},
		enc:  map[string]bool{},
	}
	info := MediaInfo{Path: `C:\clips\master.mkv`, Codec: "ffv1"}
	_, err := collectOptions(cliConfig{preset: "share", yes: true, forceXvid: true}, theme{}, e, []MediaInfo{info})
	if err == nil || !strings.Contains(err.Error(), "libxvid") {
		t.Fatalf("unsupported native-only Xvid input was not rejected early: %v", err)
	}
	mixed := []MediaInfo{
		{Path: `C:\clips\master.avi`, Codec: "lagarith"},
		{Path: `C:\clips\download.mp4`, Codec: "h264"},
	}
	_, err = collectOptions(cliConfig{preset: "share", yes: true, forceXvid: true}, theme{}, e, mixed)
	if err == nil || !strings.Contains(err.Error(), "libxvid") {
		t.Fatalf("force-Xvid mixed batch was not rejected early: %v", err)
	}
}

// A --preset that names an encoder this FFmpeg/system lacks must fail once up
// front, not once per item mid-batch.
func TestCollectOptionsRejectsMissingPresetEncoderEarly(t *testing.T) {
	e := &Engine{caps: Capabilities{}, enc: map[string]bool{}}
	infos := []MediaInfo{{Path: "clip.avi", Codec: "ffv1", PixelFormat: "yuv420p", BitDepth: 8, Chroma: "4:2:0"}}
	for preset, want := range map[string]string{"edit": "prores_ks", "magicyuv": "magicyuv", "utvideo": "utvideo"} {
		if _, err := collectOptions(cliConfig{preset: preset, yes: true}, theme{}, e, infos); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("--preset %s should fail early naming %s, got %v", preset, want, err)
		}
	}
	// MagicYUV also requires the system codec/plugin install, not just the
	// FFmpeg encoder.
	magicFFmpegOnly := &Engine{caps: Capabilities{MagicInstalled: false}, enc: map[string]bool{"magicyuv": true}}
	if _, err := collectOptions(cliConfig{preset: "magicyuv", yes: true}, theme{}, magicFFmpegOnly, infos); err == nil || !strings.Contains(err.Error(), "MagicYUV") {
		t.Fatalf("encoder-only MagicYUV should be rejected up front: %v", err)
	}
}

// The compressed-source prompt can switch the job to ProRes — encoder
// availability must be checked against the final preset, not the one the
// user originally selected.
func TestCompressedPromptPresetSwitchRevalidatesEncoder(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	stdinReader = bufio.NewReader(strings.NewReader("3\n"))
	e := &Engine{
		caps: Capabilities{HasXvid: true, HasLibXvid: true},
		enc:  map[string]bool{"libxvid": true},
	}
	infos := []MediaInfo{{Path: "clip.mp4", Codec: "h264", PixelFormat: "yuv420p", BitDepth: 8, Chroma: "4:2:0"}}
	_, err := collectOptions(cliConfig{preset: "share"}, theme{}, e, infos)
	if err == nil || !strings.Contains(err.Error(), "prores_ks") {
		t.Fatalf("switching to ProRes without prores_ks should fail early, got %v", err)
	}
}

// A compressed source carrying alpha must switch to ProRes 4444, not the
// non-alpha 422 LT fallback that would drop the alpha channel.
func TestCompressedAlphaSwitchUsesProRes4444(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	stdinReader = bufio.NewReader(strings.NewReader("3\nn\n"))
	e := &Engine{
		caps: Capabilities{HasXvid: true, HasLibXvid: true, HasProRes: true},
		enc:  map[string]bool{"libxvid": true, "prores_ks": true},
	}
	infos := []MediaInfo{{Path: "clip.mov", Codec: "h264", PixelFormat: "yuva420p", BitDepth: 8, Chroma: "4:2:0", HasAlpha: true}}
	opts, err := collectOptions(cliConfig{preset: "share", outputDir: t.TempDir()}, theme{}, e, infos)
	if err != nil {
		t.Fatal(err)
	}
	if opts.Preset != "prores_4444" {
		t.Fatalf("compressed alpha input should switch to ProRes 4444, got %q", opts.Preset)
	}
}

// The flag must survive collectOptions: it has to reach ConvertOptions and
// mask native availability, or the enforcement point in processItem and the
// early availability check both silently keep using xvid_encraw.
func TestNoNativeXvidPropagatesThroughCollectOptions(t *testing.T) {
	e := &Engine{
		caps: Capabilities{HasXvid: true, HasNativeXvid: true, HasLibXvid: false},
		enc:  map[string]bool{},
	}
	infos := []MediaInfo{{Path: `C:\clips\master.avi`, Codec: "lagarith"}}
	// Native-only machine + flag: nothing can satisfy the Xvid preset, so the
	// job must fail early with the libxvid hint.
	if _, err := collectOptions(cliConfig{preset: "share", yes: true, noNativeXvid: true}, theme{}, e, infos); err == nil || !strings.Contains(err.Error(), "libxvid") {
		t.Fatalf("--no-native-xvid on a native-only system should require libxvid: %v", err)
	}
	// With libxvid present the job proceeds and the flag reaches the options.
	e.caps.HasLibXvid = true
	e.enc["libxvid"] = true
	opts, err := collectOptions(cliConfig{preset: "share", yes: true, noNativeXvid: true}, theme{}, e, infos)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.NoNativeXvid {
		t.Fatal("noNativeXvid flag did not reach ConvertOptions")
	}
}

func TestNoNativeXvidFlagParsesAfterPaths(t *testing.T) {
	c, args, err := parseFlags([]string{"clip.avi", "--no-native-xvid", "--preset", "share"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.noNativeXvid || c.preset != "share" || len(args) != 1 || args[0] != "clip.avi" {
		t.Fatalf("cfg=%+v args=%v", c, args)
	}
}

// ---------------------------------------------------------------------------
// Prompt EOF propagation (#4)
// ---------------------------------------------------------------------------

func TestAskLineEOF(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	stdinReader = bufio.NewReader(strings.NewReader(""))
	if _, err := askLine("X", "def"); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
	// Trailing content without a newline is still a valid answer; the NEXT read
	// reports EOF.
	stdinReader = bufio.NewReader(strings.NewReader("y"))
	v, err := askLine("X", "")
	if err != nil || v != "y" {
		t.Fatalf("last-line answer=%q err=%v", v, err)
	}
	if _, err := askLine("X", "def"); !errors.Is(err, io.EOF) {
		t.Fatalf("second read expected EOF, got %v", err)
	}
}

func TestAskChoiceEOF(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	stdinReader = bufio.NewReader(strings.NewReader(""))
	done := make(chan error, 1)
	go func() {
		_, err := askChoice("Choose", []string{"1", "2"}, "1")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expected EOF, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("askChoice looped forever on EOF")
	}
}

func TestAskYesNoEOF(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	stdinReader = bufio.NewReader(strings.NewReader(""))
	if _, err := askYesNo("OK?", true); !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
	stdinReader = bufio.NewReader(strings.NewReader("n"))
	v, err := askYesNo("OK?", true)
	if err != nil || v {
		t.Fatalf("answer=%v err=%v", v, err)
	}
}

// ---------------------------------------------------------------------------
// CLI robustness (#6, #9, #10)
// ---------------------------------------------------------------------------

func TestCollectOptionsValidatesTimingFlags(t *testing.T) {
	e := &Engine{caps: Capabilities{HasProRes: true}, enc: map[string]bool{"prores_ks": true}}
	infos := []MediaInfo{{Path: "clip.mp4", Codec: "h264"}}
	if _, err := collectOptions(cliConfig{preset: "edit", timescale: "abc", yes: true}, theme{}, e, infos); err == nil || !strings.Contains(err.Error(), "timescale") {
		t.Fatalf("bad --timescale not rejected up front: %v", err)
	}
	if _, err := collectOptions(cliConfig{preset: "edit", captureFPS: "abc", timescale: "0.1", yes: true}, theme{}, e, infos); err == nil || !strings.Contains(err.Error(), "capture-fps") {
		t.Fatalf("bad --capture-fps not rejected up front: %v", err)
	}
	if _, err := collectOptions(cliConfig{preset: "edit", timescale: "0.1", captureFPS: "30", yes: true}, theme{}, e, infos); err != nil {
		t.Fatalf("valid timing flags rejected: %v", err)
	}
	opts, err := collectOptions(cliConfig{preset: "edit", timescale: "1/10", captureFPS: "60000/1001", yes: true}, theme{}, e, infos)
	if err != nil || !opts.Conform || opts.Timescale != "1/10" {
		t.Fatalf("fractional timing flags: opts=%+v err=%v", opts, err)
	}
}

func TestReorderArgs(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantErr  bool
		wantLast []string
		check    func(t *testing.T, c cliConfig, args []string)
	}{
		{
			name: "options first",
			in:   []string{"--preset", "share", "file.avi"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if c.preset != "share" || len(args) != 1 || args[0] != "file.avi" {
					t.Fatalf("cfg=%+v args=%v", c, args)
				}
			},
		},
		{
			name: "options after positional",
			in:   []string{"file.avi", "--preset", "share"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if c.preset != "share" || len(args) != 1 || args[0] != "file.avi" {
					t.Fatalf("cfg=%+v args=%v", c, args)
				}
			},
		},
		{
			name: "interleaved",
			in:   []string{"file1.avi", "--preset", "share", "file2.avi", "--yes"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if c.preset != "share" || !c.yes || len(args) != 2 || args[0] != "file1.avi" || args[1] != "file2.avi" {
					t.Fatalf("cfg=%+v args=%v", c, args)
				}
			},
		},
		{
			name: "dash terminator",
			in:   []string{"--", "-weird.avi"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if len(args) != 1 || args[0] != "-weird.avi" {
					t.Fatalf("cfg=%+v args=%v", c, args)
				}
			},
		},
		{
			name: "help flag is a literal path after terminator",
			in:   []string{"--", "-h"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if len(args) != 1 || args[0] != "-h" {
					t.Fatalf("cfg=%+v args=%v", c, args)
				}
			},
		},
		{
			name: "inline value",
			in:   []string{"file.avi", "--preset=share"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if c.preset != "share" {
					t.Fatalf("cfg=%+v", c)
				}
			},
		},
		{
			name: "single dash form",
			in:   []string{"file.avi", "-preset", "share", "-yes"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if c.preset != "share" || !c.yes {
					t.Fatalf("cfg=%+v", c)
				}
			},
		},
		{
			name:    "missing value",
			in:      []string{"file.avi", "--preset"},
			wantErr: true,
		},
		{
			name:    "unknown option",
			in:      []string{"file.avi", "--bogus"},
			wantErr: true,
		},
		{
			name: "bool with inline value",
			in:   []string{"file.avi", "--yes=true"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if !c.yes {
					t.Fatalf("cfg=%+v", c)
				}
			},
		},
		{
			name: "timescale and capture fps after path",
			in:   []string{"file.avi", "--timescale", "0.1", "--capture-fps", "30", "--strip-audio"},
			check: func(t *testing.T, c cliConfig, args []string) {
				if c.timescale != "0.1" || c.captureFPS != "30" || !c.stripAudio {
					t.Fatalf("cfg=%+v", c)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, args, err := parseFlags(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got cfg=%+v args=%v", cfg, args)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, cfg, args)
		})
	}
}

// parseFlags is exercised directly rather than a parallel help pre-scan: the
// real flag parser decides what counts as help, so these cases pin the actual
// observable outcomes — help (errShowHelp), a parse error, or a clean parse.
func TestParseFlagsHelp(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want string // "help", "err", or "ok"
	}{
		{"plain help", []string{"-h"}, "help"},
		{"long help", []string{"--help"}, "help"},
		{"help among options", []string{"--preset", "share", "--help"}, "help"},
		{"help after terminator is a path", []string{"--", "--help"}, "ok"},
		{"h after terminator is a path", []string{"--", "-h"}, "ok"},
		{"terminator consumed as option value", []string{"--output", "--", "--help", "clip.avi"}, "help"},
		{"terminator consumed as preset value", []string{"--preset", "--", "-h"}, "help"},
		{"help consumed as option value", []string{"--output", "--help", "clip.avi"}, "ok"},
		{"h consumed as preset value", []string{"--preset", "-h", "clip.avi"}, "ok"},
		{"positional named like an option", []string{"output", "--", "--help"}, "ok"},
		{"single-dash long help", []string{"-help"}, "help"},
		{"double-dash short help", []string{"--h"}, "help"},
		{"help with inline value", []string{"-h=x"}, "help"},
		{"long help with inline value", []string{"--help=x"}, "help"},
		{"triple dash is not help", []string{"---h"}, "err"},
		{"help spelling consumed as value", []string{"--timescale", "-help", "clip.avi"}, "ok"},
		{"help after parsed option", []string{"--timescale", "0.5", "-help"}, "help"},
		// Malformed option streams lose to the parser's own errors — help is
		// not claimed when the command line could never parse.
		{"unknown option before help", []string{"-bogus", "-h"}, "err"},
		{"unknown option after help", []string{"-h", "-bogus"}, "err"},
		{"bad bool value before help", []string{"-yes=bad", "-h"}, "err"},
		{"missing option value", []string{"--output"}, "err"},
		{"missing value after help token", []string{"-h", "--preset"}, "err"},
		{"negated help still shows help", []string{"-h=false"}, "help"},
		{"triple dash long help is not help", []string{"---help"}, "err"},
		{"no help", []string{"file.avi", "--yes"}, "ok"},
		{"empty", nil, "ok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := parseFlags(tc.in)
			got := "ok"
			if errors.Is(err, errShowHelp) {
				got = "help"
			} else if err != nil {
				got = "err"
			}
			if got != tc.want {
				t.Fatalf("parseFlags(%v) outcome=%q (err=%v), want %q", tc.in, got, err, tc.want)
			}
		})
	}
}

// The reorderArgs classification tables must stay in exact sync with the
// flag set: a flag missing from them reads as "unknown option", a bool
// mis-tabled as value-taking would swallow the next token, and registering
// h/help would silently kill the ErrHelp path. This test fails on any drift.
func TestFlagTablesMatchRegistration(t *testing.T) {
	var c cliConfig
	fs := newFlagSet(&c)
	fs.VisitAll(func(f *flag.Flag) {
		g, ok := f.Value.(flag.Getter)
		if !ok {
			t.Fatalf("flag %q does not implement flag.Getter", f.Name)
		}
		switch g.Get().(type) {
		case bool:
			if !flagBool[f.Name] || flagNeedsValue[f.Name] {
				t.Fatalf("bool flag %q misclassified in reorder tables", f.Name)
			}
		case string:
			if !flagNeedsValue[f.Name] || flagBool[f.Name] {
				t.Fatalf("string flag %q misclassified in reorder tables", f.Name)
			}
		default:
			t.Fatalf("flag %q has unexpected value type %T", f.Name, g.Get())
		}
	})
	for name := range flagNeedsValue {
		if fs.Lookup(name) == nil {
			t.Fatalf("flagNeedsValue lists %q but no such flag is registered", name)
		}
	}
	for name := range flagBool {
		if name == "h" || name == "help" {
			continue // intentional unregistered sentinels driving ErrHelp
		}
		if fs.Lookup(name) == nil {
			t.Fatalf("flagBool lists %q but no such flag is registered", name)
		}
	}
	if fs.Lookup("h") != nil || fs.Lookup("help") != nil {
		t.Fatal("h/help must stay unregistered so flag.Parse yields ErrHelp")
	}
}

func TestAskLineWhitespaceThenEOF(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	// A whitespace-only fragment at EOF must not silently become the default —
	// under --yes that would convert "no answer" into an accepted choice.
	stdinReader = bufio.NewReader(strings.NewReader(" "))
	if _, err := askLine("X", "def"); !errors.Is(err, io.EOF) {
		t.Fatalf("whitespace-only EOF should surface EOF, got %v", err)
	}
	// A real token at EOF is still consumed as an answer.
	stdinReader = bufio.NewReader(strings.NewReader("  5"))
	if v, err := askLine("X", "def"); err != nil || v != "5" {
		t.Fatalf("partial line at EOF: v=%q err=%v", v, err)
	}
	// Whitespace terminated by a newline is an explicit empty answer → default.
	stdinReader = bufio.NewReader(strings.NewReader("   \n"))
	if v, err := askLine("X", "def"); err != nil || v != "def" {
		t.Fatalf("blank line should yield default: v=%q err=%v", v, err)
	}
}
