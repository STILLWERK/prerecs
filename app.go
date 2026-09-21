package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
)

func runApplication() {
	cfg, args, err := parseFlags(os.Args[1:])
	if errors.Is(err, errShowHelp) {
		printUsage()
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, strictConsoleText(err.Error()))
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
		fmt.Fprintln(os.Stderr, ui.red("Error: ")+strictConsoleText(err.Error()))
		pauseIfDoubleClicked()
		os.Exit(1)
	}
	engine := &Engine{caps: caps, enc: enc}
	printHeader(ui, caps, cfg.noNativeXvid)

	jobArgs := args
	for {
		paths, err := gatherInputs(jobArgs, ui)
		if err != nil {
			if errors.Is(err, io.EOF) {
				if code := missingInputExitCode(cfg); code != 0 {
					fmt.Fprintln(os.Stderr, ui.red("Error: ")+"no input paths given and stdin reached EOF")
					os.Exit(code)
				}
				return
			}
			fmt.Fprintln(os.Stderr, ui.red("Error: ")+strictConsoleText(err.Error()))
			if cfg.yes {
				os.Exit(1)
			}
			jobArgs = nil
			continue
		}
		if len(paths) == 0 {
			fmt.Println(ui.dim("No files selected."))
			if code := missingInputExitCode(cfg); code != 0 {
				os.Exit(code)
			}
			return
		}

		infos, inputFailures := analyzeInputs(caps.FFprobe, paths, ui)
		if len(infos) == 0 {
			fmt.Fprintln(os.Stderr, ui.red("No readable video files."))
			if cfg.yes {
				os.Exit(1)
			}
			jobArgs = nil
			continue
		}

		printSourceTable(ui, infos)
		printRecommendation(ui, infos, caps, cfg.noNativeXvid)

		opts, err := collectOptions(cfg, ui, engine, infos)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// Interactive EOF (Ctrl+D) exits quietly. Under --yes an
				// automation run that never got its required input must not
				// report success for zero work.
				if code := missingInputExitCode(cfg); code != 0 {
					fmt.Fprintln(os.Stderr, ui.red("Error: ")+"required input missing and stdin reached EOF")
					os.Exit(code)
				}
				return
			}
			fmt.Fprintln(os.Stderr, ui.red("Error: ")+strictConsoleText(err.Error()))
			if cfg.yes {
				os.Exit(1)
			}
			jobArgs = nil
			continue
		}

		printPlan(ui, infos, opts)
		if !cfg.yes {
			start, err := askYesNo("Start conversion?", true)
			if err != nil {
				return
			}
			if !start {
				fmt.Println(ui.dim("Cancelled."))
				again, err := askYesNo("Start a new job?", true)
				if err != nil || !again {
					return
				}
				jobArgs = nil
				cfg = resetInteractiveJob(cfg)
				continue
			}
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		result := runBatch(ctx, ui, engine, infos, opts)
		result.InputFailures = inputFailures
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
			choice, err := askChoice("Choose", []string{"1", "2", "3"}, "1")
			if err != nil {
				return
			}
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
					fmt.Println(ui.yellow("  Could not open folder: ") + strictConsoleText(err.Error()))
				}
			case "3":
				return
			}
		}
	nextJob:
		continue
	}
}

// missingInputExitCode treats a headless invocation that produced zero inputs
// as a usage error: automation must not read "did nothing" as success.
func missingInputExitCode(cfg cliConfig) int {
	if cfg.yes {
		return 2
	}
	return 0
}

func headlessExitCode(result BatchResult, cancelled error) int {
	if cancelled != nil {
		return 130
	}
	if result.Failures > 0 || result.InputFailures > 0 {
		return 1
	}
	// "Did nothing" must not read as success to automation: a --yes run in
	// which no item produced or confirmed an output (e.g. every input was
	// skipped as already distribution-compressed) exits nonzero. A skip that
	// resolved to a verified existing output counts as delivered — the
	// requested end state already exists on disk.
	produced := result.Successes
	for _, it := range result.Items {
		if it.Status == "skipped" && it.Output != "" {
			produced++
		}
	}
	if produced == 0 && len(result.Items) > 0 {
		return 2
	}
	return 0
}

// isCtxErr reports whether err means the context was cancelled or its
// deadline elapsed — both are user/timeout cancellations, not job failures.
func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func processErrorStatus(err error) string {
	if isCtxErr(err) {
		return "cancelled"
	}
	return "failed"
}

func resetInteractiveJob(c cliConfig) cliConfig {
	c.preset = ""
	c.timescale = ""
	c.captureFPS = ""
	c.stripAudio = false
	c.outputDir = ""
	c.forceXvid = false
	c.cpuProRes = false
	// noNativeXvid is session-scoped like yes/plain: it exists for machines
	// whose native encoder is broken, so it must not silently expire between
	// interactive jobs.
	return c
}

// Folder launchers are intentionally detached from conversion contexts: Start
// returns immediately, and the opener must be allowed to outlive the job.
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
