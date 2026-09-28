# Changelog

## v1.0.7 — Integrity and timing correctness patch

- Existing-output reuse can no longer return the wrong video for a different source. The output namespace is stem+preset, so `clip.mp4` and `clip.mkv` — or same-named files from different folders sharing `--output` — could adopt each other's verified output when dimensions, frame count, and rate matched. Outputs now embed a `prerecs1 src=<content-hash> job=<options-hash>` tag in the container comment (the only metadata channel that round-trips both AVI and MOV without extra mux flags), and a candidate is adopted only when both halves match a signature recomputed at decision time. The source hash covers the entire file — a sampled hash would miss same-size edits in unsampled regions, which raw masters hit naturally — and is read fresh at each check so a source swapped mid-run (even with preserved size/mtime) is caught. Untagged, foreign, or differently-optioned files are rejected and a numbered output is produced instead; an output carrying a mismatched `prerecs1` tag fails verification rather than shipping a lie.
- "Preserve source timing" no longer flattens genuine variable-frame-rate video into constant rate. Compressed sources without carried audio previously always rebuilt the timeline through `setpts=N`+`fps=`, which renumbers every frame to equal spacing while the printed mode claimed passthrough. The exact decode scan now observes real timestamp health via `vfrdet` plus the decoded presentation spread, and only proven-broken cadence (duplicate/non-monotonic PTS, collapsed spread, or absent stats) is rebuilt — healthy CFR and genuine VFR pass through untouched.
- The timeline rebuild also covers the cases metadata impeachment used to leave unhandled: when the container rate is rejected and duration survives, the rebuild derives `frames/duration`; when packet timestamps are destroyed outright, the impeached container rate is the surviving statement of intent and is used as the rebuild rate. When nothing trustworthy remains the output is left at passthrough and verified against its own decoded timestamps instead of pretending repair happened. Reuse candidates that carry still-broken timing are now rejected rather than adopted.
- `VERIFIED` now actually covers retained audio: the decode-integrity pass maps audio streams with `-xerror -err_detect explode`, so an output whose copied audio track is corrupt-but-parseable can no longer be adopted or passed as verified. The same check applies to freshly encoded outputs and existing-output candidates.
- Attached-picture streams (cover art) are excluded from video stream selection — the `0:V:0` selector is used everywhere so an MP3-with-artwork-style container cannot convert its cover image instead of the video track.
- >8-bit RGB/RGBA sources no longer crush to 8-bit precision in ProRes 4444: the planar pins are now bit-depth aware (`gbrp16le`/`gray16le`/`gbrap` targets with `yuv444p12le`/`yuva444p12le`), matching the ProRes bitstream ceiling, and the Vulkan ProRes path shares the same selection instead of hardcoding 10le. The alpha plane reattaches via `mergeplanes` for >8-bit sources — `alphamerge` quantizes the merged alpha to ~8-bit on every tested FFmpeg version (measured 80 vs 273 surviving levels) — while 8-bit sources keep `alphamerge`, which round-trips their alpha byte-for-byte. Gray+alpha (including `yaf32`) and float (`gbrpf32`, `rgbf32`, `rgbaf32`, `grayf32`) sources are classified correctly instead of silently quantizing through the 8-bit path, and `v410` is no longer mistaken for an alpha format (it is packed 10-bit 4:4:4 with padding; `y410` is the alpha variant).
- Audio-copy compatibility is now measured against the real muxers instead of a `pcm_*` prefix guess: AVI additionally accepts `ac3`, `eac3`, `wmav1`, `wmav2`, `flac`, `adpcm_ima_wav`, `adpcm_ms`; MOV additionally accepts `pcm_s8`/big-endian PCM, `aac`, `alac`, `mp2`, `ac3`, `eac3`, `wmav1`, `wmav2`, and QT/WAV ADPCM variants. Codecs that fail at mux (DTS, Opus, GSM, unsupported PCM layouts) are rejected up front instead of erroring mid-conversion.
- Native Xvid is no longer offered for alpha or RGB/GBR sources (alpha cannot survive the YUV-only pipeline, and encraw's fixed BT.601 conversion diverges from the explicit BT.709 chain libxvid uses), and a native encode that fails its frame-integrity check now marks the path rejected so a later verification failure cannot trigger a redundant second retry through the same encoder. The impeached-rate re-encode check also only applies while the output file actually came from the native path — an inline libxvid fallback output can no longer be deleted for a `-r` stamp it never carried.
- Release builds can no longer publish over a failing gate: the release workflow now throws on `gofmt`, `go vet`, and `go test` failures via explicit `$LASTEXITCODE` checks — previously a failed vet could be masked by a later passing test before packaging.
- Smaller correctness fixes: `--output` paths are absolutized (dash-prefixed directories no longer land in the working directory), interactive configuration resets job-scoped options after errors and new jobs, headless failures and interactive sessions that recorded any failure return nonzero exit codes (including EOF at later prompts), filename parsing accepts `;` and `'` in paths, empty output directories created by a failed batch are swept, FFmpeg capability detection gains a `vfrdet` probe, encoder parsing is whitespace-robust, VfW preflight is timeout-bounded, the conform FPS floor rejects unreasonably low targets, collapsed-but-varying positive PTS deltas are treated as destroyed timing rather than healthy VFR, and chroma/bit-depth classification covers `xv30`/`xv36`/`v216` endian variants, packed layouts like `vyuy422`, and planar float formats.
- New test coverage: provenance adoption/rejection and content-signature collisions, VFR preservation and collapsed-PTS repair end-to-end, corrupt retained-audio rejection, native-Xvid redundant-retry prevention, PCM/container mux matrices, chroma and bit-depth tables, timing-stat classification (uniform CFR vs VFR vs broken), path parsing, capability timeouts, encoder-name parsing, and empty-directory cleanup.

## v1.0.6 — Security and correctness hardening patch

- `avifil32.dll` is now loaded with `LOAD_LIBRARY_SEARCH_SYSTEM32` only: it is not a KnownDLL, so the default Windows DLL search order could pick up a payload library planted beside the executable or in the working directory on the first VfW call.
- FFprobe's stdout and stderr are captured separately — a non-fatal diagnostic line can no longer corrupt the JSON document and reject a valid file — and the JSON capture is bounded to 16 MiB. Probe failures now name the tool and file instead of returning a bare `invalid character`/`ffprobe:` error, and a clean ffprobe exit with a descendant-held pipe (WaitDelay) is treated as the success it is.
- A clean FFmpeg or native-Xvid exit can no longer be reclassified as cancelled (and the completed output deleted) when Ctrl+C lands between process exit and the cancellation check; the produced output is judged on its own merits. The native-Xvid classification is deterministic on both select arms.
- Container-reported durations that are NaN, infinite, or absurdly large are impeached as corrupt instead of driving frame estimates and verification math, and a corrupt stream-level duration can no longer shadow a valid format-level one. Frame-rate rationals that over/underflow float64 (e.g. `1e309`, `1e-999`) are rejected at parse.
- Reserved output slots are re-checked to be regular files immediately before encode (a swapped-in link fails the item), and the predictable native-Xvid temporary `.m4v` path refuses non-regular inodes. Reuse discovery and new reservations now share one slot bound.
- ffprobe-reported color metadata is whitelisted to the spellings FFmpeg emits before it can be interpolated into filtergraphs or output arguments; unrecognized values are dropped rather than trusted.
- A scanned frame rate no longer fabricates an expected duration it was never corroborated by, `filepath.Abs` failures no longer collapse inputs onto an empty path, and Windows inputs dedupe case-insensitively. Folder openers (`open`/`xdg-open`/`explorer`) are reaped instead of leaking child processes, and single-line console fields fold embedded newlines so a crafted filename cannot forge status lines.
- New test coverage: parallel batch orchestration and cancellation, native-Xvid descendant-pipe (WaitDelay) survival, ffprobe error paths, Windows-style input dedupe, reservation integrity, console sanitizers, and fuzz seeds for the path/rational/console parsers.
- CI now exercises the documented Go 1.23 floor (vet, build, test) and runs the suite on Windows; release tags are narrowed to `vX.Y.Z`, and Dependabot watches workflow actions.

## v1.0.5 — Compatibility and lifecycle patch

- The documented FFmpeg floor is now enforced at startup: PreRecs requires FFmpeg 5.1 or newer (`-fps_mode` does not exist in 5.0) and rejects older builds once, instead of failing mid-batch. Snapshot/git builds without a dotted release number are checked for `-fps_mode` support directly. `-enc_time_base filter`, which silently required FFmpeg 6.1, is no longer emitted — the `settb`/`setpts`/`fps` chain already produces the constant-rate timestamps it pinned, so FFmpeg 5.1–6.0 now work everywhere.
- FFmpeg and native-Xvid processes can no longer hang the batch when a wrapper script or a surviving descendant holds their stdout/stderr pipes open after exit: post-exit pipe closure is bounded (5 s), and a clean process exit is judged by the produced output rather than the wedged plumbing.
- Compressed-source timeline normalization no longer rebuilds video timestamps while audio is stream-copied — copied audio keeps the source timeline and would drift out of sync or trip a false duration-mismatch rejection. Audio-bearing compressed inputs keep passthrough timing for both streams; stripped or audio-less sources still get the clean constant-rate rebuild.
- Per-item existing-output and post-encode metadata probes now share the documented 30-second bound, and a probe timeout is reported as a failure/rejection rather than masquerading as cancellation.
- `--capture-fps` without `--timescale` fails fast under `--yes` instead of being silently ignored, and it seeds the interactive conform prompt as the default instead of being overwritten. An explicit `--strip-audio` now locks the choice instead of letting the audio menu's "keep" default undo it.
- FFmpeg stderr diagnostics retain the trailing 32 KiB — where the decisive error in a long log lives — matching the native-Xvid convention, instead of keeping the first 32 KiB and dropping the ending.
- Reuse candidates are sorted by slot number, and paths that lose a reservation `O_EXCL` race are kept out of the reuse list — they are another run's in-flight output, so verifying them could pass on content the owner later deletes. Generated-output name matching respects each preset family's real extension (`.avi` vs `.mov`), and `clipName`/`shortFFmpeg` no longer cut multibyte runes in half.
- `--preset` help lists the canonical `xvid-q3`/`xvid-q1` names alongside the others, and the MagicYUV-missing error names Ut Video as the free alternative.

## v1.0.4 — Maintainability and process-hardening patch

- Split the former monolithic `main.go` into responsibility-focused files for application wiring, CLI/input handling, presets, capability detection, media probing, FFmpeg execution, native Xvid, pixel formats, output reservations, verification, batch orchestration, and terminal reporting. The tests now follow the same structure; encoder tuning and normal conversion behavior are unchanged.
- Native `xvid_encraw` stdout and stderr capture is bounded to 32 KiB per stream, preventing a verbose or malfunctioning encoder from growing memory without limit while retaining the diagnostic tail.
- FFprobe metadata calls are bounded and honor batch cancellation. Capability probes use bounded contexts and `WaitDelay`, so canceled commands and inherited subprocess pipes cannot hold startup or verification open indefinitely. Existing-output probe cancellation now releases the reserved destination immediately.
- The compressed-source prompt switches alpha-bearing batches to ProRes 4444 instead of offering ProRes 422 LT, which cannot preserve alpha.
- Output verification now fails when expected frame-rate or duration metadata is absent instead of silently skipping those timing checks.
- Added regression coverage for bounded native-Xvid diagnostics, inherited-pipe timeouts, FFprobe cancellation, alpha-safe fallback selection, missing timing metadata, and reservation cleanup.

## v1.0.3 — Robustness and polish patch

- New `--no-native-xvid` option bypasses the `xvid_encraw` path entirely, so a machine whose native encoder fails verification can still convert through FFmpeg libxvid.
- `--preset` for ProRes, MagicYUV, and Ut Video now fails once during option collection when the required FFmpeg encoder or MagicYUV installation is missing, instead of failing every item mid-batch.
- Fallback bit-depth detection recognizes the full-range `yuvj*` planar formats FFmpeg emits for MJPEG — legitimate 8-bit sources like `yuvj420p` are no longer refused by the lossless presets as "unverifiable". Packed/semi-planar layouts (`nv16`/`nv24`, `v308`/`vuyx`, `pal8`, `rgb0`/`bgr0`/`0rgb`/`0bgr`) and high-bit packed formats (`v210`, `v410`, `r210`, `p210`/`p410`, `x2rgb10`/`x2bgr10`, `p012`/`p212`/`p216`/`p412`/`p416`, `y210`/`y216`, `nv20`, `v30x`, `xv30`, `xv36`, `ayuv64`) report accurate depths for clearer rejection messages, including the endian-suffixed spellings ffprobe actually emits. Alpha is now detected for the packed alpha formats (`v408`/`vuya`/`uyva`, `ayuv*`, `y410`/`y412`/`y416`, `gbraf*`, `pal8`) instead of only planar/`RGBA`-named ones.
- FFmpeg stdout/stderr pipes are drained without a per-line cap; a single pathological overlong output line can no longer stall an encoder behind a full pipe buffer, and stderr is drained in bounded fragments so it also cannot land in memory whole.
- Filenames and FFmpeg/ffprobe messages are stripped of terminal control bytes before printing (color/styling SGR sequences are preserved).
- Plain-mode (`--plain`) progress lines pad to the longest line emitted so a shorter line no longer leaves the tail of a longer one behind.
- The inconsistent-metadata decode scan message no longer names a specific backend, and the duplicate "estimated count" line it triggered is gone.

## v1.0.2 — Integrity and CLI hardening patch

- Source scans, FFmpeg conversion input decodes, and output verification decodes now run with strict decoder-error handling (`-xerror -err_detect explode`). FFmpeg could previously log decoder errors but still exit 0 after silently dropping frames, allowing a corrupt source to produce a truncated output that verified green. Affected files now fail loudly instead.
- Newly encoded outputs that fail probe, decode, or content verification are removed instead of remaining under the canonical output name. Pre-existing invalid candidates are still preserved and replaced with numbered outputs as before.
- Sparse numbered outputs are now discovered for reuse (e.g. `clip_preset_2.avi` is considered even when the base name is free).
- Command-line options are accepted before or after file/folder paths, and `--` terminates option parsing for dash-prefixed paths.
- `--timescale` and `--capture-fps` are validated once during option collection instead of failing per file mid-batch.
- `--yes` with no usable input now exits nonzero instead of reporting success for zero work.
- Interactive prompts propagate stdin EOF instead of potentially looping forever on an exhausted pipe.
- The Vulkan ProRes startup probe is bounded by a 10-second timeout so a wedged driver can no longer block program start; timeout simply disables the GPU path.
- Directory scans skip filenames matching the exact generated output suffix convention, preventing self-consumption when an output folder is later used as an input folder.
- `r_frame_rate` is used as a conservative fallback when `avg_frame_rate` is missing or `0/0`.
- Wider distribution-codec classification (WMV1/2, H.263, FLV1, Theora, VP6 variants, Cinepak) now triggers the already-compressed protection; MJPEG intentionally remains unclassified as it is a common acquisition/intermediate codec.
- Per-item elapsed time now includes output verification.
- The native Xvid orchestration path (VOP counting, cancellation, remux) is now covered by CI tests through a fake `xvid_encraw` test process; codec tuning is unchanged.
- CI now vets Windows-tagged sources (`GOOS=windows go vet`) in addition to building them.

## v1.0.1 — Hardening patch

- Hardened fallback bit-depth detection for packed RGB, planar YUV/GBR, grayscale, and gray+alpha pixel formats when FFprobe does not report `bits_per_raw_sample`.
- Rejects unsafe non-4444 ProRes alpha conversions and preserves supported gray+alpha through the ProRes 4444 path.
- Classifies cancellation consistently during source scan, encode, and verification; concise batch summaries now normalize multiline errors.
- Removed dead progress/audio helper state and made CI derive its version check from the application source.

## v1.0.0 — Initial public release

This is the first public release of PreRecs. The source was developed and benchmarked before publication; the internal development numbering is intentionally not presented as a public release sequence.

### Conversion paths

- Added the tuned **SHARE / Xvid Q2** preset as the strict quality-first Xvid path.
- Kept **Xvid Q2 Efficient** separate from SHARE. Native Xvid holds I/P at Q2 and uses the tested B-quantizer formula that selects B-Q3 on the benchmark material; its FFmpeg fallback remains strict Q2/full RD/B0.
- Kept Xvid Q2 Fast / Compatibility, Xvid Q3 Small, and Xvid Q1 Extreme available.
- Kept ProRes 422 LT, ProRes 422, ProRes 422 HQ, ProRes 4444, MagicYUV Lossless, and Ut Video Lossless available.
- Added the experimental Vulkan ProRes fast path with a real startup capability probe, `--cpu-prores`, and automatic CPU fallback.

### Integrity and workflow safeguards

- Native Xvid writes raw MPEG-4 Part 2 and FFmpeg wraps the exact packets into AVI, avoiding the legacy Xvid AVI writer's locking and RIFF-size issues.
- Native-Xvid eligibility includes the Windows VfW final-frame preflight. Unsafe or incomplete native paths fall back to verified FFmpeg libxvid when available.
- Compressed-source frame metadata is treated as an estimate until an exact decode scan establishes the authoritative picture count.
- Every successful output is decoded and checked for frame count, timing, duration, dimensions, codec/tag, pixel format, and audio expectations.
- Existing outputs are verified and reused when current and valid. Invalid outputs are preserved and replaced with a numbered candidate.
- Timing conform uses frame-number timestamps and strips audio to avoid desynchronization.
- Audio is stream-copied only for tested container/codec combinations; unsafe copies are rejected or require `--strip-audio`.
- Lossless presets preserve the tested 8-bit layout and reject unsupported higher-bit-depth conversions instead of silently reducing precision.

### Validation highlights

- The Efficient five-source bakeoff produced 3.70%–8.79% smaller raw streams, averaging 6.05% smaller than strict SHARE. Mean changes were SSIM -0.000009, PSNR -0.078 dB, and VMAF -0.025.
- A 645-frame Efficient regression retained 645/645 frames at 300 fps. Raw Xvid size fell from 22,002,893 to 20,372,979 bytes; the worst frame-level VMAF delta was -0.393 and no frame fell by 0.5 VMAF or more.
- Real stress coverage exercised native Xvid, large-AVI VfW fallback, ProRes, lossless intermediates, audio copy/strip, collision recovery, output reuse, alpha preservation, and exact frame/timing verification.
- The repository includes the detailed benchmark rationale in [RESEARCH_NOTES.md](RESEARCH_NOTES.md) and QA evidence in [TESTED.md](TESTED.md).
