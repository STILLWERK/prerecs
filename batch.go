package main

import (
	"context"
	"errors"
	"fmt"
	"math"
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
		// The impeached rate is kept aside: when packet timestamps are later
		// proven destroyed, the container's claim is the only surviving
		// statement of intended rate the timeline rebuild can trust.
		info.ImpeachedFPS = info.FPS
		info.FPS, info.FPSFloat = "", 0
		info.Duration = 0
	}
	if info.FPSFloat > 0 && info.Duration > 0 {
		// Refresh duration against the exact count only when the rate was
		// corroborated by a real duration above. With Duration == 0 the rate
		// passed unchallenged; fabricating an expected duration from it would
		// let a stale rate manufacture the verification it is judged against
		// (and falsely fail honest passthrough outputs that carry real
		// timestamps).
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
		// The scan doubles as the audio integrity check for sources whose
		// audio will ride along, and as the timestamp-health measurement that
		// decides whether the compressed-source timeline rebuild may run —
		// only proven-broken PTS (duplicate/non-monotonic/missing stats)
		// justifies flattening to constant rate; healthy VFR is preserved.
		includeAudio := len(info.Audio) > 0 && !opts.StripAudio && !opts.Conform
		observeTiming := e.caps.HasVfrdet && !opts.Conform && sourceClass(info) == "compressed" && (opts.StripAudio || len(info.Audio) == 0)
		count, stats, scanErr := e.countDecodedFrames(ctx, info, includeAudio, observeTiming, func(p progressInfo) {
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
		if observeTiming {
			// The spread check compares the decoded presentation span against
			// the minimum-implied duration over surviving metadata claims —
			// rate, duration, and (for destroyed-PTS sources) the impeached
			// rate claim. Minimum-implied keeps one stale-large claim from
			// inflating the reference into a false "collapsed" verdict on a
			// healthy stream; impeachment has already pruned contradictions.
			info.TimestampsBroken = brokenTimestamps(stats, count, timingReference(info, count))
		}
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
			// Compute the signature at decision time: the probe ran before
			// interactive prompts, so a source replaced while the user sat in
			// the wizard must not be judged by the old content's hash.
			srcSig := sourceSignature(info.Path)
			for _, cand := range existing {
				candStat, statErr := os.Stat(cand)
				if statErr != nil || (srcStat != nil && candStat.ModTime().Before(srcStat.ModTime())) {
					continue
				}
				rep.line(fmt.Sprintf("Existing output detected: %s", oneLine(filepath.Base(cand))))
				rep.line("Checking it before deciding whether to re-encode...")
				outInfo, probeErr := probeMediaBound(ctx, e.caps.FFprobe, cand, false)
				if probeErr != nil {
					// ctx.Err() distinguishes the batch's own cancellation
					// from the per-probe deadline: a timed-out candidate is
					// just rejected, not treated as a cancelled job.
					if ctx.Err() != nil {
						rep.line(ui.yellow("OUTPUT PROBE CANCELLED"))
						rep.line(indentError(probeErr.Error(), 2))
						if relErr := releaseOutputReservation(out); relErr != nil {
							rep.line(ui.yellow("WARNING: could not release reserved output name: " + strictConsoleText(relErr.Error())))
						}
						item.Status = "cancelled"
						item.Message = probeErr.Error()
						return item
					}
					if errors.Is(probeErr, context.DeadlineExceeded) {
						probeErr = fmt.Errorf("metadata probe exceeded %s", mediaProbeTimeout)
					}
					rep.line(ui.dim("Rejected: metadata probe failed: " + strictConsoleText(probeErr.Error())))
					continue
				}
				if !presetCodecMatches(opts.Preset, outInfo) {
					rep.line(ui.dim(fmt.Sprintf("Rejected: codec is %s/%s, expected %s", strictConsoleText(outInfo.Codec), strictConsoleText(outInfo.CodecTag), presetLabel(opts.Preset))))
					continue
				}
				// Identical dims/rate/count prove nothing about content — the
				// embedded provenance tag is what ties this output to THIS
				// source. Untagged or foreign-tagged files are never adopted.
				if !provenanceMatches(srcSig, jobSignature(opts), outInfo) {
					rep.line(ui.dim("Rejected: output was not produced from this source (provenance tag missing or different)."))
					continue
				}
				checkStarted := time.Now()
				decoded, candTiming, decodeErr := e.countDecodedFrames(ctx, outInfo, len(outInfo.Audio) > 0, info.TimestampsBroken, func(p progressInfo) {
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
				// The spread reference is the candidate's own declared cadence —
				// when source metadata was impeached, expectedDur0 is 0 and a
				// claimless reference would skip the check entirely, adopting a
				// file that still carries the destroyed timeline.
				candRef := expectedDur0
				if outInfo.FPSFloat > 0 && decoded > 0 {
					candRef = float64(decoded) / outInfo.FPSFloat
				}
				if info.TimestampsBroken && candTiming != nil && brokenTimestamps(candTiming, decoded, candRef) {
					problems = append(problems, "candidate timestamps are still broken (non-monotonic or collapsed PTS)")
				}
				// A broken source's rebuild rate is deterministic: a candidate
				// stamped at any other rate cannot be this job's output even if
				// it otherwise verifies (e.g. an artifact from a build that
				// derived timing differently). Skip the check when nothing can
				// be derived — the passthrough output's own health then rules.
				if info.TimestampsBroken && expectedFPS0 == nil && outInfo.FPSFloat > 0 {
					if rt := rebuildTargetRate(info, nil); rt != nil {
						if f := ratFloat(rt); math.Abs(outInfo.FPSFloat-f) > math.Max(.01, f*.002) {
							problems = append(problems, fmt.Sprintf("candidate rate %.6g does not match the rebuild rate %.6g for this broken source", outInfo.FPSFloat, f))
						}
					}
				}
				if len(problems) == 0 {
					// srcSig was computed before the candidate was probed and
					// decoded; a source swapped mid-evaluation must not inherit
					// the decision — the adopted output would then belong to
					// content that no longer exists. Re-checking costs one hash
					// on the adoption path only.
					if sourceSignature(info.Path) != srcSig {
						rep.line(ui.yellow("Source changed while checking existing output; not adopting it."))
						break
					}
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
			rep.line(ui.yellow("Existing output was stale or failed validation; preserving it and writing a numbered copy."))
		}
	}
	item.Output = out

	// The reservation claimed the name, not the inode: re-check the path is
	// still the regular file we created before handing it to a subprocess
	// that reopens and truncates it (a swapped-in link would write elsewhere).
	if err := checkReservedOutput(out); err != nil {
		item.Status = "failed"
		item.Message = err.Error()
		rep.line(ui.red("FAILED") + " " + strictConsoleText(err.Error()))
		// Deliberately no releaseOutputReservation: the inode here may not be
		// the placeholder we created, so nothing is removed by name.
		return item
	}

	// removeOut deletes the file this run produced. A failed removal is
	// surfaced rather than swallowed: on Windows a transient lock can refuse
	// the delete, and silently leaving the file would let a bad output keep
	// masquerading under a canonical name.
	removeOut := func() {
		// Unlink only what still looks like the file this run created: if the
		// path was swapped for a link or non-file since the reservation check,
		// unlinking by name could destroy something else.
		st, lerr := os.Lstat(out)
		switch {
		case errors.Is(lerr, os.ErrNotExist):
			return
		case lerr != nil:
			rep.line(ui.yellow("WARNING: could not stat output before cleanup: " + strictConsoleText(lerr.Error())))
			return
		case !st.Mode().IsRegular():
			rep.line(ui.yellow("WARNING: output path no longer looks like the produced file; leaving it in place"))
			return
		}
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

	// Encode, then verify. When the native Xvid path produced the file and
	// verification rejects it, retry once through FFmpeg libxvid: xvid_encraw
	// treats a mid-stream AVIStreamGetFrame failure as clean end-of-stream and
	// exits 0, so a stalled VfW decode only becomes visible here.
	nativeVerifyRejected := false
	var outInfo MediaInfo
	var problems []string
	for {
		usedNative := isXvidPreset(opts.Preset) && !nativeVerifyRejected && e.caps.HasNativeXvid && !opts.NoNativeXvid && nativeXvidEligible(info) && info.FPS != ""
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
				// The file at `out` is now libxvid-produced — mark native as
				// rejected so a later verify failure doesn't waste a redundant
				// identical libxvid encode, and clear usedNative: the impeached-
				// rate rejection below applies only while `out` actually carries
				// a native `-r`-stamped file, which this output is not.
				nativeVerifyRejected = true
				usedNative = false
				setLibxvidBackend(&item, opts.Preset)
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
				setLibxvidBackend(&item, opts.Preset)
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
			item.Message = err.Error()
			if isCtxErr(err) {
				item.Status = "cancelled"
				rep.line(ui.yellow("ENCODE CANCELLED"))
				rep.line(indentError(err.Error(), 2))
				removeOut()
				return item
			}
			item.Status = "failed"
			removeOut()
			rep.line(ui.red("ENCODE FAILED"))
			rep.line(indentError(err.Error(), 2))
			return item
		}

		// From here on `out` is a file this run produced. If it cannot pass
		// verification it must not stay behind under the canonical output name
		// masquerading as a valid result — remove it. (Pre-existing candidates are
		// still preserved deliberately in the reuse check above.)
		var probeErr error
		outInfo, probeErr = probeMediaBound(ctx, e.caps.FFprobe, out, false)
		if probeErr != nil {
			item.Elapsed = time.Since(started)
			item.Message = probeErr.Error()
			// A probe that only hit the per-probe deadline is a failure of this
			// item, not a job cancellation — and the unverified output must not
			// stay behind under the canonical name.
			if ctx.Err() != nil {
				item.Status = "cancelled"
				rep.line(ui.yellow("VERIFY PROBE CANCELLED"))
			} else {
				item.Status = "failed"
				if errors.Is(probeErr, context.DeadlineExceeded) {
					item.Message = fmt.Sprintf("output metadata probe exceeded %s", mediaProbeTimeout)
				}
				removeOut()
				rep.line(ui.red("VERIFY PROBE FAILED"))
			}
			rep.line(indentError(item.Message, 2))
			return item
		}
		rep.line("Decoding output for frame/timing verification...")
		verifyStarted := time.Now()
		// When the source scan proved broken timestamps, the output's own
		// cadence is checked too: a passthrough output that carried the damage
		// across fails verification rather than shipping VERIFIED-broken media.
		outStats := info.TimestampsBroken
		decodedFrames, outTiming, decodeErr := e.countDecodedFrames(ctx, outInfo, len(outInfo.Audio) > 0, outStats, func(p progressInfo) {
			p.Stage = "VERIFY"
			p = exactFrameProgress(p, info.FrameCount, verifyStarted)
			rep.progress(p)
		})
		rep.finish()
		if decodeErr != nil {
			item.Status = processErrorStatus(decodeErr)
			item.Elapsed = time.Since(started)
			item.Message = decodeErr.Error()
			if item.Status == "cancelled" {
				// The encoded output is complete but unverified; keep it so a later
				// run can re-check and reuse it instead of discarding the work.
				rep.line(ui.yellow("VERIFY DECODE CANCELLED"))
			} else {
				removeOut()
				rep.line(ui.red("VERIFY DECODE FAILED"))
			}
			rep.line(indentError(decodeErr.Error(), 2))
			return item
		}
		outInfo.FrameCount = decodedFrames
		outInfo.FrameCountExact = true
		item.OutputInfo = outInfo
		problems = verifyOutput(info, outInfo, opts, expectedFPS, expectedDur)
		// Reference the output's own declared cadence, not the (possibly
		// impeached) source expectation — a passthrough output on a source
		// whose claims were cleared still carries the destroyed timeline and
		// must not verify on the strength of a missing reference.
		outRef := expectedDur
		if outInfo.FPSFloat > 0 && decodedFrames > 0 {
			outRef = float64(decodedFrames) / outInfo.FPSFloat
		}
		if outStats && outTiming != nil && brokenTimestamps(outTiming, decodedFrames, outRef) {
			problems = append(problems, "output timestamps are still broken (non-monotonic or collapsed PTS)")
		}
		if len(problems) > 0 && !inputScanned {
			// The input's frame count came from container tables trusted without
			// a decode scan — but for trusted-codec containers like AVI that count
			// is itself a header field that can lie exactly like the compressed
			// tables do. If the output's real decoded count disagrees, the claim
			// may be the stale field: rescan the source once and re-verify against
			// the decoded truth instead of failing an honest conversion.
			rep.line("Source frame count was container-claimed; doing one exact decode scan to check whether the table was stale...")
			rescanStarted := time.Now()
			count, _, scanErr := e.countDecodedFrames(ctx, info, false, false, func(p progressInfo) {
				p.Stage = "RESCAN"
				p = exactFrameProgress(p, info.FrameCount, rescanStarted)
				rep.progress(p)
			})
			rep.finish()
			// One decode is enough: the retry loop must not rescan the same
			// source again.
			inputScanned = true
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
				// A native-produced output was stamped with `-r <claimed rate>`:
				// when the rescan just impeached that rate the file carries the
				// lie, so it must be re-encoded through libxvid (passthrough
				// timing) rather than adopted on frame count alone.
				if usedNative && info.FPS == "" {
					problems = append(problems, "native output was stamped at the impeached container rate")
				}
			}
		}
		if len(problems) > 0 && usedNative && !nativeVerifyRejected && e.enc["libxvid"] && ctx.Err() == nil {
			rep.line(ui.yellow("Native Xvid output failed verification; re-encoding through FFmpeg libxvid."))
			rep.line(ui.dim(strictConsoleText(strings.Join(problems, "; "))))
			nativeVerifyRejected = true
			continue
		}
		break
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

	// The output should carry the embedded provenance tag. A tag for a
	// *different* signature means the source bytes changed mid-encode — the
	// file on disk is tagged for content it does not contain, so it must not
	// survive as a VERIFIED result (a later run could adopt it for the wrong
	// source). A simply-absent tag is the muxer/drop case: keep the file, but
	// warn once since future runs can never reuse it.
	postSig, postJob := sourceSignature(info.Path), jobSignature(opts)
	want := provenanceComment(postSig, postJob)
	got := strings.TrimSpace(outInfo.ProvenanceTag)
	// Only our own tag proves a content mismatch: ffmpeg copies input global
	// metadata by default, so an output may legitimately carry the source's
	// ordinary comment — that is tag-absent, not tag-foreign.
	if want != "" && strings.HasPrefix(got, provenancePrefix) && !strings.EqualFold(got, want) {
		item.Status = "failed"
		item.Elapsed = time.Since(started)
		item.Message = "output provenance tag does not match the current source (source changed during encode)"
		removeOut()
		rep.line(ui.red("VERIFY FAILED"))
		rep.line("- " + item.Message)
		return item
	}
	if !provenanceMatches(postSig, postJob, outInfo) {
		rep.line(ui.yellow("WARNING: output did not retain the provenance tag; future runs cannot reuse it."))
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
	rep.line("Output: " + oneLine(out))
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

// Test seams: the parallel orchestration below only triggers when
// batchWorkerCount returns >= 2, which needs more CPUs than typical CI has.
// Substituting these in tests exercises the fan-out deterministically.
var (
	processItemFunc      = processItem
	batchWorkerCountFunc = batchWorkerCount
)

func runBatch(ctx context.Context, ui theme, e *Engine, infos []MediaInfo, opts ConvertOptions) BatchResult {
	defer removeEmptyCreatedDirs()
	workers := batchWorkerCountFunc(opts, infos)
	if workers <= 1 {
		result := BatchResult{Items: make([]ItemResult, 0, len(infos))}
		fmt.Println(ui.bold("CONVERTING"))
		for i, info := range infos {
			if ctx.Err() != nil {
				break
			}
			fmt.Printf("\n  [%d/%d] %s\n", i+1, len(infos), oneLine(filepath.Base(info.Path)))
			item := processItemFunc(ctx, ui, e, info, opts, sequentialReporter(ui))
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
				item := processItemFunc(ctx, ui, e, j.info, opts, rep)
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
