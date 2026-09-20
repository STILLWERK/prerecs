# PreRecs

PreRecs is a Windows console application for preparing high-frame-rate game captures and other prerequisite video for editing workflows. The current source version is **v1.0.2**; v1.0.0 and v1.0.1 remain frozen.

It has two complementary jobs:

- create compact Xvid community/share copies from lossless or high-quality masters;
- turn downloaded H.264, HEVC, AV1, VP9, Xvid, and other compressed clips into edit-friendly intermediates.

PreRecs can also conform a slowed capture to its effective frame rate. It repackages the existing decoded frames; it does not interpolate or invent frames.

## Why it exists

High-frame-rate prerecs expose several failure modes that a single FFmpeg command does not handle reliably:

- legacy AVI frame tables can be stale or wrong;
- timestamps can be incomplete or gapped;
- native Xvid's Windows AVI writer can lock files or leave inconsistent RIFF sizes;
- delayed B-frames must be drained before an output is wrapped;
- copying incompatible audio into AVI can change framing or timing;
- an output that already exists may be valid, stale, truncated, or encoded with the wrong codec.

PreRecs keeps the conversion path explicit and verifies every successful output by decoding it again.

## Presets

The interactive menu recommends ProRes 422 LT for general editing. SHARE is the explicit distribution-oriented preset.

| Preset | Output | Use |
| --- | --- | --- |
| **SHARE / Xvid Q2** (`share`) | XVID AVI | Strict quality-first Xvid path for compatible lossless AVI masters. |
| **Xvid Q2 Efficient** (`xvid-efficient`) | XVID AVI | Separate storage-efficiency tradeoff: I/P at Q2 and tested B-VOP behavior effectively at Q3. |
| **Xvid Q2 Fast / Compatibility** (`xvid-fast`) | XVID AVI | Preserved older Compact behavior with VHQ1 and no B-frames. |
| **Xvid Q3 Small** (`xvid-q3`) | XVID AVI | Smaller/faster Xvid option. |
| **Xvid Q1 Extreme** (`xvid-q1`) | XVID AVI | Very large, highest-quality Xvid quantizer option. |
| **Edit-ready / ProRes 422 LT** (`edit`) | ProRes MOV | General-purpose editing intermediate and the interactive default recommendation. |
| **ProRes 422** (`prores`) | ProRes MOV | Quality step up from LT. |
| **ProRes 422 HQ** (`hq`) | ProRes MOV | Maximum normal ProRes tier. |
| **ProRes 4444** (`4444`) | ProRes MOV | RGB, 4:4:4, and alpha-capable sources. |
| **MagicYUV Lossless** (`magicyuv`) | MagicYUV AVI | Fast lossless intermediate when the optional Windows codec/plugin is installed. |
| **Ut Video Lossless** (`utvideo`) | Ut Video AVI | Free lossless alternative exposed by FFmpeg. |

### SHARE / Xvid Q2

SHARE is the strict quality-first Xvid preset. On the native Xvid path it retains the tested configuration:

- native Xvid VHQ4, quality 6, up to two B-frames, and B-frame RD;
- strict Q2 bounds for I, P, and B pictures;
- H.263 quantization, trellis, and chroma motion estimation;
- GOP length 240, up to eight encoder threads, and one slice;
- QPel off, GMC off, packed bitstream off, masking/adaptive quantization off, and custom matrices off;
- YUV420P output.

The native encoder writes a raw MPEG-4 Part 2 stream first. FFmpeg then wraps those exact packets into AVI without re-encoding. The fallback is deliberately conservative: libxvid at Q2 with full macroblock RD and B-frames disabled.

SHARE aliases include `compact`, `xvid`, `xvid-q2`, and `xvid-max-q2`. They all select the tuned SHARE path for command-line compatibility.

### Xvid Q2 Efficient

Efficient is a separate preset and does not weaken SHARE. Native Xvid keeps I/P references fixed at Q2 and uses VHQ4, up to two B-frames, B-frame RD, H.263 quantization, trellis, chroma ME, GOP 240, up to eight threads, one slice, and the same disabled QPel/GMC/packed/masking/AQ/custom-matrix features. Its B quantizer range and ratio/offset formula select B-Q3 consistently on the tested Xvid material.

The five-source bakeoff reduced the raw stream by 3.70%–8.79%, averaging 6.05%, with mean changes of SSIM -0.000009, PSNR -0.078 dB, and VMAF -0.025. These figures are measurements of the tested material, not universal codec guarantees.

FFmpeg's normal Xvid AVI path cannot safely reproduce the native unpacked B-frame behavior. Efficient therefore uses the same conservative strict-Q2/full-RD/B0 fallback as SHARE when native Xvid is unavailable or fails its integrity checks.

Aliases are `xvid-efficient`, `xvid-q2-efficient`, and `efficient`.

## Requirements and installation

PreRecs is a Windows console application. Each GitHub release publishes a versioned ZIP, a standalone `PreRecs.exe`, and `SHA256SUMS.txt`. The ZIP contains the executable and project documentation, not third-party codec installers or FFmpeg binaries.

Required:

1. `ffmpeg.exe` and `ffprobe.exe` from a Windows FFmpeg build;
2. an FFmpeg build with the encoder needed by the selected preset. In particular, Xvid fallback requires `libxvid`, ProRes requires `prores_ks`, and Ut Video requires `utvideo`.

Put FFmpeg in one of these locations:

- a `tools` directory beside `PreRecs.exe`;
- the same directory as `PreRecs.exe`;
- the system `PATH`.

You can also set `PRERECS_FFMPEG` and `PRERECS_FFPROBE` to explicit executable paths. PreRecs checks those environment variables first.

Native Xvid is optional. Install a compatible Xvid reference encoder and make `xvid_encraw.exe` available through `PRERECS_XVID_ENCRAW`, a `tools`/application directory, a standard Xvid installation directory, or `PATH`. Native Xvid is used only for eligible lossless AVI masters after a Windows VfW final-frame preflight. If the preflight fails, or the native encode fails its frame-count check, PreRecs falls back to FFmpeg libxvid when that encoder is available.

MagicYUV is optional, proprietary, and never bundled. Its preset is offered only when the Windows codec/plugin registration and the FFmpeg encoder are both detected. Ut Video is the free lossless alternative.

Download the release ZIP or standalone executable, install the external tools you need, and run `PreRecs.exe` from PowerShell or a console. Use `SHA256SUMS.txt` to verify either release asset. No installer is required.

## Usage

Interactive mode:

```powershell
.\PreRecs.exe
```

Direct examples:

```powershell
# Strict quality-first Xvid share copy
.\PreRecs.exe --preset share --yes .\master.avi

# Separate Q2-efficient native-Xvid preset
.\PreRecs.exe --preset xvid-efficient --yes .\master.avi

# Edit-ready intermediate from a downloaded compressed clip
.\PreRecs.exe --preset edit --yes .\downloaded_h264.avi

# Keep no audio in the intermediate
.\PreRecs.exe --preset edit --strip-audio --yes .\downloaded_h264.avi

# Force the mature CPU ProRes encoder instead of the experimental Vulkan path
.\PreRecs.exe --preset edit --cpu-prores --yes .\clip.avi

# Optional lossless paths
.\PreRecs.exe --preset magicyuv --yes .\master.avi
.\PreRecs.exe --preset utvideo --yes .\master.avi

# Conform a 30 fps capture recorded at game timescale 0.1 to 300 fps
.\PreRecs.exe --preset edit --timescale 0.1 --capture-fps 30 .\cinematic.avi

# Explicitly override the normal protection against re-encoding compressed input
.\PreRecs.exe --preset share --force-xvid .\already-compressed.avi
```

Useful options:

```text
--preset <name>       Select a preset.
--timescale <value>   Conform slowed capture timing.
--capture-fps <rate>  Override the detected capture rate.
--strip-audio         Remove audio instead of copying compatible tracks.
--output <folder>     Write outputs to a specific directory.
--yes                 Skip the final interactive confirmation.
--force-xvid          Allow Xvid on already-compressed sources.
--cpu-prores          Disable the experimental Vulkan ProRes path.
--plain               Disable ANSI styling.
--version             Print the application version.
```

## Conversion and verification behavior

The workflow is intentionally staged:

1. metadata-only analysis identifies the source codec, dimensions, pixel format, timing, and audio;
2. compressed sources receive a visible exact decode scan instead of trusting a stale container frame table;
3. the selected encoder is run with the tested timing and codec path;
4. the output is fully decoded and checked before it is reported as successful.

All FFmpeg decode stages (source scan, conversion input, and output verification) run with strict decoder-error handling (`-xerror -err_detect explode`). FFmpeg can otherwise log decode errors yet still exit 0 after silently dropping frames; a bitstream-corrupt source now fails loudly instead of producing a verified truncated output.

Verification checks the exact decoded frame count, frame rate, normalized duration, dimensions, requested codec/tag, expected Xvid or lossless pixel format, audio presence and track count, and the copied audio codec when audio is retained. A mismatch fails the job.

ProRes verification also checks the encoded profile and pixel format: LT/422/HQ require `yuv422p10le`, while 4444 requests `yuv444p10le` without alpha or an alpha-capable 4444 format when the source has alpha. Current FFmpeg `prores_ks` builds may report non-alpha profile-4444 output as `yuv444p12le`; that encoder-normalized result is accepted. Non-4444 ProRes presets reject alpha sources instead of silently discarding the alpha plane.

When a matching output already exists, PreRecs decodes and verifies it first. A current valid output is reused. A stale, truncated, wrong-codec, or otherwise invalid pre-existing output is preserved and the new conversion receives the next numbered filename — including sparse numbered slots, so `clip_preset_2.avi` can be reused even when the base name is free. An output produced by the *current* run that fails probe, decode, or verification checks is removed rather than left behind under a normal-looking filename.

Output allocation atomically reserves the final target to prevent parallel same-stem collisions. An abrupt process termination can therefore leave a zero-byte placeholder; the next run preserves/rejects that invalid candidate and safely chooses a numbered replacement.

For a timing conform, PreRecs assigns frame-number timestamps in the target timebase and strips audio to avoid desynchronization. It does not use optical-flow interpolation or create new pictures.

## Build from source

The repository has a single Go module and uses only the Go standard library. Go 1.23 or newer is required; the release CI currently builds with Go 1.26.

From the repository root:

```powershell
go test -count=1 ./...
go vet ./...
go test -race -count=1 ./...
go build -trimpath -ldflags="-s -w" -o PreRecs.exe .
.\PreRecs.exe --version
```

For a Linux or WSL cross-build:

```bash
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags='-s -w' -o PreRecs.exe .
```

Run the resulting executable on Windows and confirm it reports `PreRecs 1.0.2`.

On Windows, `build_windows.ps1` runs the unit tests and vet before producing `PreRecs.exe`. The GitHub Actions CI also checks formatting, tests, vet, race tests, and a Windows amd64 CGO-disabled build. Tags beginning with `v` use the release workflow to build a Windows ZIP and SHA-256 checksum file.

## Limitations

- The supported runtime target is Windows amd64. The non-Windows build tags exist to make development and tests possible on Linux/WSL; native VfW Xvid and MagicYUV detection are Windows-specific.
- FFmpeg and ffprobe are required at runtime and are not redistributed by this repository.
- Native Xvid is an optional acceleration/quality path, not a requirement. Its direct path is limited to compatible lossless AVI masters and the installed Windows VfW stack.
- Lossless AVI metadata keeps the fast path only when its frame count agrees with the rounded duration/frame-rate estimate. Missing or inconsistent timing metadata triggers an exact decode scan before native Xvid uses the count for VfW preflight, encoder limits, or verification.
- MagicYUV is optional commercial software and is not included.
- The tested MagicYUV and Ut Video FFmpeg paths accept 8-bit source layouts. Higher-bit-depth sources are rejected rather than silently reduced under a “lossless” label; use an appropriate ProRes tier instead.
- Lagarith is supported as an input when FFmpeg can decode it, but the tested FFmpeg builds do not provide a Lagarith encoder.
- Intermediates can be much larger than compressed downloads. Transcoding cannot restore detail already lost by a distribution codec.
- Timing conforming strips audio by design. Normal-timing audio is stream-copied only when the destination container is known to accept the tested codec.
- Vulkan ProRes is an experimental fast path. It is probed at startup with a bounded (10-second) probe and automatically falls back to CPU `prores_ks` when the driver, probe, or runtime encode fails.
- Only the first video stream of each input is processed (`-map 0:v:0`); additional video streams are not converted.
- Folder inputs are scanned non-recursively: only files directly inside the folder are taken.
- Directory scans skip filenames that exactly match PreRecs' own generated output naming (`stem_<preset>.avi|mov`, `stem_<preset>_<n>.ext`) so a later run does not re-ingest its own results when the output directory overlaps a scanned folder. A file that deliberately shares the exact generated name can still be passed explicitly.
- Command-line options may appear before or after file paths; `--` terminates option parsing for paths that begin with a dash.

## Technical notes and QA evidence

The repository keeps the tuning rationale and benchmark evidence in [RESEARCH_NOTES.md](RESEARCH_NOTES.md) and the regression/stress results in [TESTED.md](TESTED.md). Those documents preserve useful development measurements while the public changelog records only the initial public release.

## License

The original PreRecs source and documentation in this repository are released under the MIT License; see [LICENSE](LICENSE). FFmpeg, Xvid, MagicYUV, Ut Video, and other external tools/codecs remain subject to their own licenses and are not relicensed or bundled here.

The historical `legacy/` batch files from the source archive are intentionally not part of this repository because their upstream `gmzorz/prerecs` project does not publish a license. See [LICENSING.md](LICENSING.md) for the scope and rationale.
