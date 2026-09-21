package main

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

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

// reconcileScannedInput records a trusted decoded frame count and re-evaluates
// the selected container rate against it before the rate can drive the
// constant-rate timeline rebuild. This covers avg_frame_rate too: on the
// containers scanned here it is header-derived, the same stale-metadata class
// as the untrusted frame tables — a stale header rate would silently retime
// the output while verification still passes against the same wrong value.
// A rate that disagrees with the decoded stream is discarded so passthrough
// preserves real timing instead of inventing one.
func reconcileScannedInput(info *MediaInfo, count int64, ui theme, rep itemReporter) {
	info.FrameCount = count
	info.FrameCountExact = true
	if _, rat := selectFrameRate("", info.FPS, count, info.Duration); rat == nil && info.FPS != "" {
		rep.line(ui.yellow(fmt.Sprintf("Container frame rate %s does not match the decoded stream; preserving source timing.", strictConsoleText(info.FPS))))
		// Disagreement proves the metadata is inconsistent but not which field
		// is stale — Duration could be the liar just as well. Keep it and
		// verification would compare honest passthrough timing against a
		// possibly-stale expected duration. Clear both: passthrough carries
		// real timestamps by construction and the exact frame count remains
		// the integrity gate. (A source with no rate at all skips this branch
		// — its duration was never contradicted and still gates verification.)
		info.FPS, info.FPSFloat = "", 0
		info.Duration = 0
	}
	if info.FPSFloat > 0 {
		info.Duration = float64(count) / info.FPSFloat
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

	inputScanned := false
	inconsistentMeta := nativeXvidNeedsExactFrameScan(info)
	if inconsistentMeta {
		info.FrameCountExact = false
	}
	if !info.FrameCountExact {
		if inconsistentMeta {
			rep.line("Lossless AVI metadata does not agree with its duration; doing one exact decode scan before conversion.")
		} else {
			rep.line("Source frame count is estimated; doing one exact decode scan before conversion.")
		}
		count, scanErr := e.countDecodedFrames(ctx, info, func(p progressInfo) {
			p.Stage = "SCAN"
			p.TotalFrames = info.FrameCount
			rep.progress(p)
		})
		rep.finish()
		inputScanned = true
		if scanErr != nil {
			item.Status = processErrorStatus(scanErr)
			item.Message = scanErr.Error()
			if item.Status == "cancelled" {
				rep.line(ui.yellow("SOURCE SCAN CANCELLED"))
			} else {
				rep.line(ui.red("SOURCE SCAN FAILED"))
			}
			rep.line(indentError(scanErr.Error(), 2))
			return item
		}
		reconcileScannedInput(&info, count, ui, rep)
		item.InputInfo = info
		rep.line(fmt.Sprintf("Exact source frames: %d", count))
	}

	existing, out, err := outputCandidates(info.Path, opts.OutputDir, opts.Preset)
	if err != nil {
		item.Status = "failed"
		item.Message = err.Error()
		rep.line(ui.red("FAILED") + " " + strictConsoleText(err.Error()))
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
				rep.line(fmt.Sprintf("Existing output detected: %s", strictConsoleText(filepath.Base(cand))))
				rep.line("Checking it before deciding whether to re-encode...")
				outInfo, probeErr := probeMediaContext(ctx, e.caps.FFprobe, cand, false)
				if probeErr != nil {
					if isCtxErr(probeErr) {
						rep.line(ui.yellow("OUTPUT PROBE CANCELLED"))
						rep.line(indentError(probeErr.Error(), 2))
						if relErr := releaseOutputReservation(out); relErr != nil {
							rep.line(ui.yellow("WARNING: could not release reserved output name: " + strictConsoleText(relErr.Error())))
						}
						item.Status = "cancelled"
						item.Message = probeErr.Error()
						return item
					}
					rep.line(ui.dim("Rejected: metadata probe failed: " + strictConsoleText(probeErr.Error())))
					continue
				}
				if !presetCodecMatches(opts.Preset, outInfo) {
					rep.line(ui.dim(fmt.Sprintf("Rejected: codec is %s/%s, expected %s", strictConsoleText(outInfo.Codec), strictConsoleText(outInfo.CodecTag), presetLabel(opts.Preset))))
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
					if isCtxErr(decodeErr) {
						rep.line(ui.yellow("OUTPUT CHECK CANCELLED"))
						rep.line(indentError(decodeErr.Error(), 2))
						if relErr := releaseOutputReservation(out); relErr != nil {
							rep.line(ui.yellow("WARNING: could not release reserved output name: " + strictConsoleText(relErr.Error())))
						}
						item.Status = "cancelled"
						return item
					}
					rep.line(ui.dim("Rejected: full decode check failed: " + strictConsoleText(decodeErr.Error())))
					continue
				}
				outInfo.FrameCount = decoded
				outInfo.FrameCountExact = true
				problems := verifyOutput(info, outInfo, opts, expectedFPS0, expectedDur0)
				if len(problems) == 0 {
					item.Output = cand
					item.OutputInfo = outInfo
					item.Status = "skipped"
					item.Backend = "existing verified output"
					item.Message = "existing output already verified"
					if relErr := releaseOutputReservation(out); relErr != nil {
						msg := "cleanup failed: " + strictConsoleText(relErr.Error())
						rep.line(ui.yellow("WARNING: " + msg))
						item.Message += "; " + msg
					}
					rep.line(ui.green("EXISTS / VERIFIED") + " — skipping re-encode.")
					rep.line(fmt.Sprintf("Frames: %d -> %d   Size: %s -> %s", info.FrameCount, outInfo.FrameCount, humanBytes(info.SizeBytes), humanBytes(outInfo.SizeBytes)))
					return item
				}
				rep.line(ui.dim("Rejected: " + strictConsoleText(strings.Join(problems, "; "))))
			}
			rep.line(ui.yellow("Existing output was stale or did not validate; preserving it and writing a numbered copy."))
		}
	}
	item.Output = out

	// removeOut deletes the file this run produced. A failed removal is
	// surfaced rather than swallowed: on Windows a transient lock can refuse
	// the delete, and silently leaving the file would let a bad output keep
	// masquerading under a canonical name.
	removeOut := func() {
		if rmErr := os.Remove(out); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			msg := "cleanup failed: " + strictConsoleText(rmErr.Error())
			rep.line(ui.yellow("WARNING: " + msg))
			if item.Message != "" {
				item.Message += "; " + msg
			} else {
				item.Message = msg
			}
		}
	}

	started := time.Now()
	var expectedFPS *big.Rat
	var expectedDur float64

	usedNative := isXvidPreset(opts.Preset) && e.caps.HasNativeXvid && !opts.NoNativeXvid && nativeXvidEligible(info) && info.FPS != ""
	if usedNative && info.FrameCountExact && info.FrameCount > 0 {
		ok, preflightErr := vfwCanDecodeFrame(info.Path, info.FrameCount-1)
		if preflightErr != nil {
			usedNative = false
			rep.line(ui.yellow("Native Xvid preflight could not validate the AVI; using FFmpeg libxvid."))
			rep.line(ui.dim(strictConsoleText(preflightErr.Error())))
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
		// Bound the encode by the claimed frame count only when that count was
		// decode-verified this run. A container table that understates the
		// stream would otherwise truncate the encode to the lie — and the
		// post-verify would see claim == output, reporting VERIFIED on a
		// partial conversion. Unbounded, the encoder stops at stream end and
		// the VOP/verify checks stay authoritative.
		frameBound := int64(0)
		if inputScanned {
			frameBound = info.FrameCount
		}
		expectedFPS, expectedDur, err = e.runNativeXvid(ctx, info, opts, out, frameBound, func(p progressInfo) {
			rep.progress(p)
		})
		if err != nil && e.enc["libxvid"] && ctx.Err() == nil {
			rep.finish()
			rep.line(ui.yellow("Native Xvid failed its frame-integrity check; retrying with FFmpeg libxvid."))
			rep.line(ui.dim(strictConsoleText(err.Error())))
			// Keep `out` occupied: it is the O_EXCL reservation guarding this
			// destination against concurrent same-stem runs. Removing it here
			// would briefly release the reservation before the fallback encode;
			// ffmpeg -y overwrites the placeholder/partial output itself.
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
			rep.line(ui.dim(strictConsoleText(err.Error())))
			// Same reservation rule: `out` must stay occupied through the
			// backend switch; ffmpeg -y truncates whatever the GPU attempt left.
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

	if err != nil {
		item.Elapsed = time.Since(started)
		if isCtxErr(err) {
			item.Status = "cancelled"
			rep.line(ui.yellow("ENCODE CANCELLED"))
			rep.line(indentError(err.Error(), 2))
			removeOut()
			return item
		}
		item.Status = "failed"
		item.Message = err.Error()
		removeOut()
		rep.line(ui.red("ENCODE FAILED"))
		rep.line(indentError(err.Error(), 2))
		return item
	}

	// From here on `out` is a file this run produced. If it cannot pass
	// verification it must not stay behind under the canonical output name
	// masquerading as a valid result — remove it. (Pre-existing candidates are
	// still preserved deliberately in the reuse check above.)
	outInfo, err := probeMediaContext(ctx, e.caps.FFprobe, out, false)
	if err != nil {
		item.Elapsed = time.Since(started)
		item.Status = processErrorStatus(err)
		item.Message = err.Error()
		if item.Status == "cancelled" {
			rep.line(ui.yellow("VERIFY PROBE CANCELLED"))
		} else {
			removeOut()
			rep.line(ui.red("VERIFY PROBE FAILED"))
		}
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
		item.Status = processErrorStatus(err)
		item.Elapsed = time.Since(started)
		item.Message = err.Error()
		if item.Status == "cancelled" {
			// The encoded output is complete but unverified; keep it so a later
			// run can re-check and reuse it instead of discarding the work.
			rep.line(ui.yellow("VERIFY DECODE CANCELLED"))
		} else {
			removeOut()
			rep.line(ui.red("VERIFY DECODE FAILED"))
		}
		rep.line(indentError(err.Error(), 2))
		return item
	}
	outInfo.FrameCount = decodedFrames
	outInfo.FrameCountExact = true
	item.OutputInfo = outInfo
	problems := verifyOutput(info, outInfo, opts, expectedFPS, expectedDur)
	if len(problems) > 0 && !inputScanned {
		// The input's frame count came from container tables trusted without
		// a decode scan — but for trusted-codec containers like AVI that count
		// is itself a header field that can lie exactly like the compressed
		// tables do. If the output's real decoded count disagrees, the claim
		// may be the stale field: rescan the source once and re-verify against
		// the decoded truth instead of failing an honest conversion.
		rep.line("Source frame count was container-claimed; doing one exact decode scan to check whether the table was stale...")
		rescanStarted := time.Now()
		count, scanErr := e.countDecodedFrames(ctx, info, func(p progressInfo) {
			p.Stage = "RESCAN"
			p = exactFrameProgress(p, info.FrameCount, rescanStarted)
			rep.progress(p)
		})
		rep.finish()
		switch {
		case scanErr != nil && isCtxErr(scanErr):
			// The encoded output is complete but unverified; keep it so a
			// later run can re-check and reuse it, matching the verify-decode
			// cancellation path.
			item.Status = "cancelled"
			item.Elapsed = time.Since(started)
			item.Message = scanErr.Error()
			rep.line(ui.yellow("SOURCE RESCAN CANCELLED"))
			rep.line(indentError(scanErr.Error(), 2))
			return item
		case scanErr != nil:
			rep.line(ui.dim("Source rescan failed: " + strictConsoleText(scanErr.Error())))
		case count != info.FrameCount:
			rep.line(ui.yellow(fmt.Sprintf("Container frame count %d was stale; the source actually decodes %d frames — re-verifying against the decoded count.", info.FrameCount, count)))
			reconcileScannedInput(&info, count, ui, rep)
			item.InputInfo = info
			if _, fps, dur, terr := expectedTiming(info, opts); terr == nil {
				expectedFPS, expectedDur = fps, dur
				problems = verifyOutput(info, outInfo, opts, expectedFPS, expectedDur)
			} else {
				// The output was encoded with now-impeached timing (e.g. a
				// conform built on the stale rate) and the expectations can
				// no longer be recomputed — the timeline cannot be verified,
				// so the rescue must fail rather than weaken the checks.
				problems = append(problems, "timing cannot be verified after correcting stale metadata: "+strictConsoleText(terr.Error()))
			}
		}
	}
	if len(problems) > 0 {
		item.Elapsed = time.Since(started)
		item.Status = "failed"
		item.Message = strings.Join(problems, "; ")
		removeOut()
		rep.line(ui.red("VERIFY FAILED"))
		for _, problem := range problems {
			rep.line("- " + strictConsoleText(problem))
		}
		return item
	}

	item.Status = "ok"
	item.Elapsed = time.Since(started)
	rep.line(ui.green("VERIFIED"))
	if info.FrameCount > 0 && outInfo.FrameCount > 0 {
		msg := fmt.Sprintf("Frames: %d -> %d", info.FrameCount, outInfo.FrameCount)
		if info.SizeBytes > 0 && outInfo.SizeBytes > 0 {
			msg += fmt.Sprintf("   Size: %s -> %s", humanBytes(info.SizeBytes), humanBytes(outInfo.SizeBytes))
		}
		rep.line(msg)
	}
	rep.line("Output: " + strictConsoleText(out))
	return item
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
	if hasDuplicateOutputStems(opts.OutputDir, infos) {
		// Identical basenames landing in the same effective output directory
		// race for the same numbered filename — this includes the default
		// converted_prerecs folder when two same-stem sources share a source
		// directory, not only a custom --output folder. Serialize that case
		// rather than weakening collision/recovery guarantees.
		workers = 1
	}
	return workers
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
			fmt.Printf("\n  [%d/%d] %s\n", i+1, len(infos), strictConsoleText(filepath.Base(info.Path)))
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
