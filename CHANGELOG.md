# Changelog

## Unreleased

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
