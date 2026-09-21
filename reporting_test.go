package main

import (
	"math"
	"testing"
	"time"
	"unicode"
)

func TestSizeDeltaPercent(t *testing.T) {
	if got := sizeDeltaPercent(1000, 250); math.Abs(got-(-75)) > 1e-9 {
		t.Fatalf("got %v", got)
	}
}

func TestETAWaitsForWarmup(t *testing.T) {
	if got := etaFromProgress(time.Now().Add(-10*time.Second), 0.04); got != 0 {
		t.Fatalf("ETA should be hidden below 5%%, got %v", got)
	}
	if got := etaFromProgress(time.Now().Add(-time.Second), 0.50); got != 0 {
		t.Fatalf("ETA should be hidden before 2s, got %v", got)
	}
	if got := etaFromProgress(time.Now().Add(-10*time.Second), 0.50); got <= 0 {
		t.Fatalf("ETA should be available after warmup, got %v", got)
	}
}

func TestConciseErrorNormalizesMultilineMessages(t *testing.T) {
	got := conciseError("first line\nsecond\tline\r\nthird line", 80)
	if got != "first line second line third line" {
		t.Fatalf("concise error=%q", got)
	}
}

// Filenames and tool error text reach the console; control bytes in them must
// not be able to inject escape sequences or carriage-return rewrites.
func TestSafeConsoleTextStripsControlBytes(t *testing.T) {
	in := "evil\x1b[2K\x1b]0;pwned\x07\rname\x00\nsecond line"
	got := safeConsoleText(in)
	for _, r := range got {
		if r != '\n' && !unicode.IsPrint(r) {
			t.Fatalf("control byte %#U survived sanitization in %q", r, got)
		}
	}
	if want := "evil[2K]0;pwnedname\nsecond line"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// The theme's own SGR survives: reporters receive pre-styled output, and
	// these codes can only change colors.
	styled := "\x1b[32mVERIFIED\x1b[0m \x1b[2mdone\x1b[0m"
	if got := safeConsoleText(styled); got != styled {
		t.Fatalf("SGR styling must be preserved, got %q", got)
	}
	// Foreign SGR — conceal, blink, 256-color — and non-SGR escapes drop.
	if got := safeConsoleText("pre\x1b[8mconcealed\x1b[5mblink\x1b[38;5;9mred"); got != "pre[8mconcealed[5mblink[38;5;9mred" {
		t.Fatalf("foreign SGR should be inert, got %q", got)
	}
	if got := safeConsoleText("\x1b[2J\x1b[H\x1b]0;t\x07x"); got != "[2J[H]0;tx" {
		t.Fatalf("non-SGR escapes should drop their ESC byte, got %q", got)
	}
}

// Untrusted fragments (filenames, probed metadata, tool errors) are sanitized
// before styling is composed around them: even a theme code like \x1b[32m must
// not survive inside untrusted text, or a filename could recolor/reset the
// status output that follows it.
func TestStrictConsoleTextStripsAllEscapes(t *testing.T) {
	cases := map[string]string{
		"evil\x1b[32mVERIFIED\x1b[0m.avi":   "evilVERIFIED.avi",
		"clip\x1b[8m\x1b[0m.mov":            "clip.mov",
		"pre\x1b[38;5;9mred\x1b[2Kpost.mkv": "preredpost.mkv",
		"ti\x1b]0;pwned\x07tle.mp4":         "title.mp4",
		"os\x1b]0;pwned\x1b\\c.mp4":         "osc.mp4",
		"plain\x00name\rline\nnext.avi":     "plainnameline\nnext.avi",
	}
	for in, want := range cases {
		if got := strictConsoleText(in); got != want {
			t.Fatalf("strictConsoleText(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestExactFrameProgressUsesFramesAndWallClockFPS(t *testing.T) {
	p := progressInfo{Percent: 1, Frame: "25", FPS: "0.00"}
	got := exactFrameProgress(p, 100, time.Now().Add(-5*time.Second))
	if math.Abs(got.Percent-0.25) > 1e-9 {
		t.Fatalf("percent=%v want 0.25", got.Percent)
	}
	if got.FPS == "" || got.FPS == "0.00" {
		t.Fatalf("expected wall-clock fps fallback, got %q", got.FPS)
	}
	if got.TotalFrames != 100 {
		t.Fatalf("total frames=%d", got.TotalFrames)
	}
}
