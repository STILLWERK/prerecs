# Changelog

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
