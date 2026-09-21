package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeadlessExitCodeOnCancellation(t *testing.T) {
	if got := headlessExitCode(BatchResult{}, context.Canceled); got != 130 {
		t.Fatalf("cancelled headless job exit code=%d, want 130", got)
	}
	if got := headlessExitCode(BatchResult{Failures: 1}, nil); got != 1 {
		t.Fatalf("failed headless job exit code=%d, want 1", got)
	}
	if got := headlessExitCode(BatchResult{Successes: 1}, nil); got != 0 {
		t.Fatalf("successful headless job exit code=%d, want 0", got)
	}
	if got := headlessExitCode(BatchResult{InputFailures: 1}, nil); got != 1 {
		t.Fatalf("partial-probe headless job exit code=%d, want 1", got)
	}
}

func TestProcessErrorStatusDistinguishesCancellation(t *testing.T) {
	if got := processErrorStatus(context.Canceled); got != "cancelled" {
		t.Fatalf("context cancellation status=%q, want cancelled", got)
	}
	if got := processErrorStatus(errors.New("verification failed")); got != "failed" {
		t.Fatalf("ordinary error status=%q, want failed", got)
	}
}

func TestMissingInputExitCode(t *testing.T) {
	if missingInputExitCode(cliConfig{yes: true}) == 0 {
		t.Fatal("--yes with no input must exit nonzero")
	}
	if missingInputExitCode(cliConfig{}) != 0 {
		t.Fatal("interactive EOF should still exit 0")
	}
}

// TestMainYesEOFNonzero runs the real main() in a helper process: with --yes,
// a valid source, no --preset, and stdin at EOF, the required preset selection
// cannot complete. Automation must not read "did nothing" as success — the
// process must exit nonzero.
func TestMainYesEOFNonzero(t *testing.T) {
	if os.Getenv("PRERECS_MAIN_HELPER") == "1" {
		var helperArgs []string
		if err := json.Unmarshal([]byte(os.Getenv("PRERECS_MAIN_ARGS")), &helperArgs); err != nil {
			os.Exit(70)
		}
		os.Args = append([]string{"prerecs"}, helperArgs...)
		main()
		return
	}
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["ffv1"] {
		t.Skip("ffv1 encoder unavailable")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "in.avi")
	if b, err := exec.Command(caps.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=30",
		"-frames:v", "3", "-c:v", "ffv1", src,
	).CombinedOutput(); err != nil {
		t.Fatalf("fixture encode: %v %s", err, b)
	}

	run := func(args ...string) int {
		payload, _ := json.Marshal(args)
		cmd := exec.Command(os.Args[0], "-test.run=^TestMainYesEOFNonzero$")
		cmd.Env = append(os.Environ(),
			"PRERECS_MAIN_HELPER=1",
			"PRERECS_MAIN_ARGS="+string(payload),
		)
		cmd.Stdin = strings.NewReader("") // immediate EOF
		err := cmd.Run()
		if err == nil {
			return 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		t.Fatalf("helper run failed: %v", err)
		return -1
	}

	if code := run("--yes"); code == 0 {
		t.Fatal("--yes with no input and EOF exited 0")
	}
	if code := run("--yes", src); code == 0 {
		t.Fatal("--yes with a file but no preset selection exited 0 on EOF")
	}
}

// ---------------------------------------------------------------------------
// Final-review regressions
// ---------------------------------------------------------------------------

func TestHeadlessExitCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  BatchResult
		want int
	}{
		{"successes", BatchResult{Successes: 1, Items: []ItemResult{{Status: "ok", Output: "o.avi"}}}, 0},
		{"failures", BatchResult{Failures: 1, Items: []ItemResult{{Status: "failed"}}}, 1},
		{"input failures", BatchResult{InputFailures: 1}, 1},
		// Every input skipped by policy (e.g. distribution-compressed under
		// --yes) produced no output at all — automation must see nonzero.
		{"all skipped nothing produced", BatchResult{Skipped: 2, Items: []ItemResult{{Status: "skipped"}, {Status: "skipped"}}}, 2},
		// A skip that resolved to an already-verified output did deliver the
		// requested end state — that is success.
		{"skip resolved to verified output", BatchResult{Skipped: 1, Items: []ItemResult{{Status: "skipped", Output: "o.avi"}}}, 0},
		{"mixed success and bare skip", BatchResult{Successes: 1, Skipped: 1, Items: []ItemResult{{Status: "ok", Output: "o.avi"}, {Status: "skipped"}}}, 0},
		{"nothing processed", BatchResult{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := headlessExitCode(tc.res, nil); got != tc.want {
				t.Fatalf("headlessExitCode=%d, want %d", got, tc.want)
			}
		})
	}
	if c := headlessExitCode(BatchResult{}, context.Canceled); c != 130 {
		t.Fatalf("cancelled exit code=%d, want 130", c)
	}
}
