# PreRecs v1.0.0 QA and historical evidence

## Efficient preset implementation

Regression tests lock **Xvid Q2 Efficient** to:

- quality 6 / VHQ4;
- two unpacked B-frames;
- B-frame RD enabled;
- I/P fixed Q2;
- B quantizer range 2..31 with ratio 100 / offset 100, selecting B-Q3 on the tested Q2 material;
- H.263 metric 0, GOP 240, one slice, up to 8 threads;
- QPel, GMC, masking/AQ, and custom matrices disabled.

Tests also verify aliases, interactive MORE-menu mapping, `_xvid_efficient_q2.avi` naming, XVID codec verification, YUV420P verification, and conservative FFmpeg fallback Q2 / full RD / B0.

## Five-source 180-frame bakeoff

Strict SHARE Q2/B-Q2 was compared with Efficient I/P-Q2 + B-Q3 and a more aggressive B-Q4 variant across XPR, Ballista, Nade Raid, DSR, and Shotgun masters.

Efficient B-Q3 versus strict SHARE:

- average size: **6.05% smaller**;
- range: **3.70% to 8.79% smaller**;
- average SSIM delta: **-0.000009**;
- average PSNR delta: **-0.078 dB**;
- average VMAF delta: **-0.025**;
- worst VMAF delta: **-0.053**;
- average encode-speed delta in the sample set: about **+2.8%**.

All 15 strict/Q3/Q4 outputs decoded exactly 180/180 frames, were XVID/YUV420P at 300/1 fps, and passed full `ffmpeg -xerror` decoding.

B-Q4 averaged 8.35% smaller, but its average VMAF loss (-0.066) and PSNR loss (-0.164 dB) were substantially larger, so Q3 was selected as the efficiency knee.

## Full 645-frame regression

The final Efficient behavior was tested on the complete Ballista `death_jump.avi` source:

- strict SHARE raw Xvid: 22,002,893 bytes;
- Efficient raw Xvid: 20,372,979 bytes;
- reduction: **7.4%**;
- final PreRecs Efficient AVI: 20,394,582 bytes;
- decoded frames: **645/645**;
- codec/tag: XVID;
- pixel format: YUV420P;
- B-frames present;
- frame rate: 300/1;
- duration: 2.150000 s;
- independent FFmpeg `-xerror`: exit 0.

Quality:

- SSIM: 0.997741 -> **0.997706**;
- PSNR: 54.774666 -> **54.603443 dB**;
- VMAF: 97.567358 -> **97.535116**.

Frame-by-frame VMAF delta across all 645 frames:

- median: **0.0**;
- worst frame: **-0.393367**;
- 1st percentile: about **-0.215**;
- frames below -0.5: **0**;
- frames below -1.0: **0**;
- minimum absolute VMAF of the clip was unchanged.

## Rejected tuning branches

- VHQ2/B2: about 9% faster on the first sample but 5.15% larger.
- VHQ3/B2: larger and not meaningfully faster than VHQ4.
- quality 5: larger with no useful speed advantage.
- PSNR-HVS-M RD metric: slower and larger.
- B3: dramatically larger on the first sample; B4 frame-count path was not a useful/stable direction.
- open GOP: only ~0.03% smaller on the 645-frame Efficient run.
- B-Q4/Q5/Q6: continued shrinking files but quality cost rose faster than storage savings; Q3 is the selected knee.

## Fallback and reuse behavior

A 30-frame FFV1/MKV master forced Efficient through the non-native path. The candidate reported **FFmpeg libxvid Efficient fallback (strict Q2 full RD/B0; native B-Q3 unavailable)** and produced 30/30 XVID/YUV420P frames at 300/1 fps with has_b_frames=0. Independent `ffmpeg -xerror` decoding exited 0.

Re-running Efficient against the existing 180-frame native output fully decoded and accepted `sample180_xvid_efficient_q2.avi`, then skipped re-encoding without creating a numbered duplicate.

## Code gate

The release source passed:

- `gofmt`
- `go test -count=1 ./...`
- `go vet ./...`
- `go test -race -count=1 ./...`
- Windows amd64 CGO-disabled cross-build


---

## SHARE default-preset regression

## Default-preset regression

The release promotes the already-validated VHQ4/B2 strict-Q2 encoder to normal SHARE and keeps the old VHQ1/B0 Compact encoder as Xvid Q2 Fast / Compatibility.

Regression tests verify:

- `share`, `compact`, `xvid`, `xvid-q2`, and `xvid-max-q2` resolve to the tuned `xvid_max_q2` implementation.
- `xvid-fast`, `xvid-compact`, `fast`, `compat`, and `compatibility` resolve to the preserved old `xvid_compact` implementation.
- Interactive menu item SHARE selects tuned Q2.
- Interactive MORE -> Xvid Q2 Fast selects the old Compact implementation.
- Tuned SHARE remains strict Q2 I/P/B, quality 6, VHQ4, B2, B-frame RD, ratio 100 / offset 0, GOP 240, unpacked, one slice, with QPel/GMC/masking/custom matrices disabled.
- FFmpeg fallback remains Q2 / full macroblock RD / B0.
- Final verification now explicitly requires YUV420P for tuned SHARE as well as every other Xvid preset.

## Real 180-frame alias test

A 180-frame Lagarith section from the real XPR footage was stream-copied to a deterministic AVI and encoded through the release candidate at 30 -> 300 fps.

Legacy `--preset compact` now selected tuned SHARE:

- output: `sample180_xvid_max_q2.avi`
- 3,678,424 bytes
- 180/180 decoded frames
- XVID / YUV420P
- has_b_frames = 1
- 300/1 fps
- 0.600000 s
- independent FFmpeg `-xerror`: exit 0

`--preset xvid-fast` selected the preserved old Compact path:

- output: `sample180_xvid_compact.avi`
- 7,161,362 bytes
- 180/180 decoded frames
- XVID / YUV420P
- has_b_frames = 0
- 300/1 fps
- 0.600000 s
- independent FFmpeg `-xerror`: exit 0

The tuned default was 48.6% smaller on this sample while preserving the exact decoded frame count and timing.

## Code gate

The release candidate passed:

- `gofmt`
- `go test -count=1 ./...`
- `go vet ./...`
- `go test -race -count=1 ./...`
- Windows amd64 cross-build


---

## Strict SHARE Q2 and native-Xvid QA

## Code gate

The final source was formatted and tested with Go 1.26.5 under Ubuntu 26.04 WSL, then cross-built as a Windows amd64 console executable.

Required gate:

- `gofmt`
- `go test -count=1 ./...`
- `go vet ./...`
- `go test -race -count=1 ./...`
- Windows amd64 console build

Regression tests lock Xvid Q2 Maximum to strict Q2 I/P/B quantizers, quality 6, VHQ4, B2, B-frame RD, B quantizer ratio 100 / offset 0, GOP 240, unpacked bitstream, one slice, and no QPel/GMC/masking/custom matrix. The FFmpeg fallback is separately locked to Q2 / full RD / B0.

## Four-file native Maximum batch

The exact release candidate encoded the complete Ballista/Yemen folder with four workers at 30 -> 300 fps:

- death: 509 -> 509 frames
- death_jump: 645 -> 645 frames
- jump: 903 -> 903 frames
- pov: 1114 -> 1114 frames

All four selected native Xvid Maximum Q2 (VHQ4/B2, 8 threads/1 slice). An independent second pass used `ffprobe -count_frames` plus FFmpeg `-xerror`; every output was XVID / YUV420P, had B-frames present, was exactly 300/1 fps, and matched the expected duration. All four passed.

## Maximum worker benchmark

The same four real 2560x1440 masters were limited to the first 300 encoded frames each using the final Maximum settings:

- 1 worker: 42.596 s, 28.17 aggregate fps
- 2 workers: 24.117 s, 49.76 aggregate fps
- 4 workers: 14.025 s, 85.56 aggregate fps

All three runs produced the same total compressed byte count (34,805,614 bytes). Four workers remains the release cap.

## Native PCM audio copy

A 90-frame section copied losslessly from a real Lagarith master was given a PCM S16LE 48 kHz mono track. Maximum Q2 produced 90/90 XVID frames with B-frames and retained PCM S16LE audio.

Decoded audio MD5 was identical before and after:

`7a16c5da560f79056660d3f52a0422bf`

## Raw MPEG-4 -> AVI packet preservation

A 180-frame native Maximum elementary stream was wrapped to AVI using the release remux path.

- raw MPEG-4 packets: 180
- AVI video packets: 180
- ordered SHA-256 payload hashes: identical for every packet

This confirms the wrap step changes only the container; compressed video payloads are not re-encoded or mutated.

## Collision and recovery

A valid Maximum output was truncated intentionally to 4096 bytes and placed at the base output name. PreRecs rejected the damaged AVI, preserved it unchanged, and wrote a verified `_2` replacement.

A second identical run decoded and accepted the `_2` output and did not create `_3`.

## >2 GiB VfW fallback

`dsr_skate/nade_throw_world.avi` is a real ~2.1 GiB Lagarith/OpenDML source with 1930 frames. Windows VfW could not retrieve the final advertised frame, so the native preflight correctly skipped `xvid_encraw`.

The live fallback command used FFmpeg libxvid Q2 with full macroblock RD and B0 (`-mbd rd -bf 0`). Final output:

- 1930/1930 decoded frames
- XVID / YUV420P
- has_b_frames = 0
- exactly 300/1 fps
- 6.433333 s
- independent FFmpeg `-xerror` decode: exit 0


---

## Batch, Vulkan, and alpha QA

## Code gate

Final release source passed:

- `gofmt`
- `go test -count=1 ./...`
- `go vet ./...`
- `go test -race -count=1 ./...`
- Windows amd64 console build

Regression coverage includes the earlier timing/collision/audio/VfW tests plus parallel batch scheduling, Vulkan ProRes command construction/eligibility, source-aware ProRes alpha precision, and an RGB gradient-alpha ProRes 4444 round-trip test.

## Xvid performance and parallel stability

Same four real 2560x1440 Lagarith masters, same native Xvid Q2 settings, full PreRecs workflow including WRAP and decoded verification:

- sequential: **104.10 s**;
- four workers: **37.03 s**;
- end-to-end speedup: **2.81x**;
- all jobs exited 0;
- corresponding sequential/parallel AVI outputs had identical sizes and identical SHA-256 hashes.

Thread/slice tuning on 300 real frames showed the existing 8-thread / 4-slice single-job configuration is already close to the useful ceiling: 22.9 fps at 8/4 versus 23.3 fps at 24/24. Six concurrent Xvid jobs were slower than four on the tested i7-13700K, so the release caps Xvid workers at four.

A mixed seven-file `xpr_slums` stress run exercised native Xvid and large-AVI FFmpeg fallback simultaneously:

- **7/7 VERIFIED**, 0 failures, exit 0;
- three large VfW-incompatible AVIs were caught by preflight and used FFmpeg fallback;
- zero unexpected native-Xvid runtime failures/retries;
- independent probing confirmed every output's source/output frame count, XVID tag, YUV420 format, and exact 300/1 fps.

Parallel collision/recovery was also exercised by truncating one output while three valid outputs already existed. The broken base remained byte-for-byte untouched, a verified `_2` replacement was created, and the next parallel rerun reused `_2` without creating `_3`.

## MagicYUV worker pool

Four real Ballista/Yemen 2560x1440 Lagarith masters were run through MagicYUV at 30 -> 300 fps with two workers:

- death: 509/509 verified;
- death_jump: 645/645 verified;
- jump: 903/903 verified;
- pov: 1114/1114 verified;
- **4/4 VERIFIED**, 0 failures, exit 0.

A predictor benchmark kept `gradient`: roughly 326 fps / 251.6 MiB for 300 frames versus 316 fps / 305.7 MiB for `left`; `median` was effectively tied with gradient. Four simultaneous MagicYUV encodes were slower than two.

## Vulkan ProRes

On the tested RTX 3060 / current FFmpeg build, 300 real 2560x1440 frames encoded as ProRes LT at approximately:

- CPU `prores_ks`: **35.0 fps**;
- Vulkan async depth 1: **120.3 fps**;
- Vulkan async depth 4: **143.9 fps**;
- Vulkan async depth 8: **147.4 fps**.

The release uses async depth 4 because depth 8 added little extra throughput. CPU and Vulkan outputs decoded as ordinary ProRes LT (`apcs`, `yuv422p10le`) with the same frame count/timing. Measured source-relative quality was essentially the same: CPU SSIM ~0.997457 / PSNR ~55.315 dB, Vulkan SSIM ~0.997404 / PSNR ~55.238 dB.

Integrated PreRecs GPU tests passed LT, 422, HQ, and 4444, all with full decoded verification. A real malformed H.264/AVI (`deathanim.avi`) was SCAN-corrected to 1,997 real pictures, encoded by Vulkan at nominal 600 fps, and verified 1,997 -> 1,997 frames at 600/1.

GPU availability is tested with a real one-frame Vulkan ProRes encode at startup. With Vulkan intentionally broken via an invalid ICD path, PreRecs reported ProRes CPU and completed on `prores_ks`. A separate forced runtime Vulkan failure also retried successfully on CPU.

AAC stream-copy audio was rechecked through GPU ProRes: 189/189 input/output AAC packet hashes were identical.

## ProRes 4444 alpha

A nontrivial YUVA gradient-alpha source exposed that using 16-bit ProRes alpha for an 8-bit source changes the decoded 8-bit alpha values. The release selects 8-bit ProRes alpha for 8-bit inputs and 16-bit alpha for higher-bit inputs.

For YUV+alpha through Vulkan ProRes 4444, source/output alpha-plane MD5 hashes are identical.

For RGB+alpha, FFmpeg's direct `gbrap -> yuva444p10le` conversion modifies alpha before the encoder. The implementation therefore splits colour and alpha: RGB colour is converted independently to limited-range BT.709 YUV444 10-bit, the original alpha plane bypasses the colour conversion, and `alphamerge` recombines them before ProRes. The final Windows candidate produced identical source/output alpha-plane MD5 hashes on a nontrivial RGB gradient-alpha source and a normal 30/30-frame ProRes 4444 decode.

## Final candidate smoke

The exact final-candidate Windows binary was run again on:

- a fresh four-worker native-Xvid batch: 523, 553, 814, and 907 frames, all XVID / YUV420 / 300/1;
- ProRes LT Vulkan on `death_jump.avi`: 645/645 frames, `apcs`, `yuv422p10le`, 300/1;
- RGB gradient-alpha -> CPU ProRes 4444: alpha MD5 exact;
- YUV gradient-alpha -> Vulkan ProRes 4444: alpha MD5 exact.


---

## Timing, verification, and stability QA

## Code QA

Latest reconstructed release source passed:

- `gofmt`
- `go test -count=1 ./...`
- `go vet ./...`
- `go test -race -count=1 ./...`
- Windows amd64 console build

Regression tests cover safe high-FPS conform, stale compressed metadata, compressed-source CFR normalization, MagicYUV/Ut Video pixel-layout preservation, >8-bit rejection, native-Xvid arguments, VOP counting across poll boundaries, verification failure on frame loss, and headless EOF handling.

## Real native-Xvid test

`death_jump.avi`, Lagarith 2560x1440, 645 frames, 30 fps capture, 0.1 timescale -> 300 fps:

- live progress starts at 4/645 frames (0.62%) and continues in small increments;
- 645/645 encoded frames;
- FFmpeg WRAP uses stream copy, not re-encode;
- final 645/645 decoded frames;
- exact 300 fps;
- ~500 MiB -> 34.6 MiB;
- VERIFY passed; process exited 0.

A separate 90-frame equivalence test compared the old direct-Xvid AVI path with the new raw-MPEG4 -> FFmpeg AVI path:

- 90/90 compressed packet hashes identical;
- 90/90 decoded frame hashes identical;
- identical 300 fps / 0.300 s timing.

## Real compressed -> intermediate regression

`deathanim.avi`, downloaded 2560x1440 H.264 AVI at nominal 600 fps:

- AVI metadata claimed 2,046 frames / 3.410 s;
- exact decode SCAN found 1,997 pictures;
- PreRecs normalized the editing timeline to 1,997 / 600 = 3.3283 s.

**ProRes 422 LT**

- 1,997 -> 1,997 frames;
- exact 600 fps;
- normalized duration verified;
- ~16.5 MiB compressed source -> ~1.4 GiB intermediate;
- VERIFY passed; exit 0.

**MagicYUV Lossless**

- 1,997 -> 1,997 frames;
- exact 600 fps;
- normalized duration verified;
- ~16.5 MiB -> ~2.0 GiB;
- encode rate reached roughly 530 fps on the tested run;
- VERIFY passed; exit 0.

The size increase is expected: an editing intermediate trades storage for simpler decoding and timeline responsiveness and cannot restore detail already lost to H.264.

## Synthetic lossless-layout regression on RONG

45-frame 640x360 FFV1 YUV444P source:

- MagicYUV output: `yuv444p`, 45/45 frames, 30 fps, 1.500 s;
- Ut Video output: `yuv444p`, 45/45 frames, 30 fps, 1.500 s;
- MagicYUV decoded frame hashes: 45/45 identical to source;
- Ut Video decoded frame hashes: 45/45 identical to source.

10-bit FFV1 YUV420 source:

- MagicYUV preset rejected before encoding;
- exit code 1;
- no output file created;
- message directs the user to ProRes 422/4444 instead of silently reducing to 8-bit.

## Nade Raid baseline retained

Four real 2560x1440 Lagarith masters totaled 2,998,126,970 bytes (~2.79 GiB).

- ProRes 422 LT packaged batch: 2,471,157,118 bytes (~2.30 GiB), 4/4 verified, -17.58% overall.
- Standard ProRes 422 benchmark: 3,429,822,866 bytes (~3.19 GiB), larger than the Lagarith total.
- Native Xvid Q2 batch: 180,709,430 bytes, 4/4 verified, -93.97% overall.
- Measured decode throughput: Lagarith ~360–479 fps; ProRes LT ~704–784 fps; ProRes 422 ~633–736 fps.

## Stability regressions

**Collision intelligence**

- An up-to-date 523-frame / 300 fps Xvid output was fully decoded, verified, and reused. File count stayed unchanged; no numbered duplicate was created.
- A deliberately truncated 524,288-byte Xvid output decoded only 60/523 frames. PreRecs rejected it with explicit frame/duration reasons, preserved the original bytes unchanged, and wrote a verified numbered replacement.
- A second run found the numbered replacement, fully verified it, and skipped another encode.

**Audio policy**

- Normal-timing H.264/AAC -> ProRes LT with Keep audio: 48/48 AAC packet hashes identical between input and output.
- The same source with `--strip-audio`: zero output audio tracks.
- MagicYUV/PCM -> native Xvid with Keep audio: 47/47 PCM packet hashes identical between input and output.
- Conformed timing intentionally strips audio; there is no hidden audio time-stretch/re-encode path in the release.

**Windows VfW preflight**

The real `vdub` corpus contains several large Lagarith AVIs that FFmpeg can decode completely but the legacy VfW/AVIFile path used by `xvid_encraw` cannot seek to the final frame. The release probes the final VfW frame before starting native Xvid and routes those files directly to FFmpeg libxvid instead of wasting a partial native encode.
## Final release-gate stress run

Real corpus: 30 Lagarith masters across five actual `vdub` project folders (`ballista_yemen`, `dsr_skate`, `nade_raid`, `shotgun_frost`, `xpr_slums`), 38.2 GiB total. Every source was 2560x1440 / 30 fps / YUV420 and was conformed to 300 fps with Xvid Q2.

- packaged run: **30/30 VERIFIED**, 0 failures, overall exit 0;
- VfW final-frame preflight: exactly **5** affected large/OpenDML AVIs routed to FFmpeg libxvid;
- unexpected native-Xvid runtime failures/retries: **0**;
- independent `ffprobe -count_frames` verification: **30/30 pass** for decoded frames, 300/1 fps, XVID tag, dimensions, YUV420, and expected audio state;
- numbered duplicate outputs after the first pass: **0**;
- complete collision-only second pass: **30/30 existing outputs verified and skipped**, 0 encoders launched, 0 failures, still 30 files / 0 numbered duplicates.

The five VfW-preflight fallbacks were the expected large files in `dsr_skate` and `xpr_slums`; FFmpeg decoded and verified their full streams successfully.

## Final preset matrix

One real 2560x1440 Lagarith master (`nade_cine_green.avi`, 641 frames) was run through the final candidate at 30 -> 300 fps. All seven tested presets exited 0 and independently decoded back to 641 frames at 300/1:

- ProRes 422 LT;
- ProRes 422;
- ProRes 422 HQ;
- MagicYUV Lossless;
- Ut Video Lossless;
- Xvid Q3 Small;
- Xvid Q1 Maximum.

ProRes 4444 was tested separately with a 30-frame FFV1 RGB/alpha source. Final result: profile 4444, alpha-capable decode, 30/30 frames, 30 fps, and identical source/output alpha-plane MD5. The same RGB source also passed the FFmpeg Xvid fallback after the GBR/YUV metadata fix.

## Final recovery and audio checks

Using the exact final candidate:

- a valid Xvid output was truncated to 65,536 bytes; PreRecs decoded only 6/30 frames, rejected it, preserved the broken file byte-for-byte, and created a verified `_2` replacement;
- a third run rejected the broken base, verified `_2`, skipped encoding, and did **not** create `_3`;
- PCM -> native Xvid/AVI keep-audio: packet hashes identical;
- AAC -> ProRes/MOV keep-audio: packet hashes identical;
- `--strip-audio`: zero output audio tracks;
- 30 -> 300 conform: audio stripped by design;
- AAC -> Xvid/AVI keep-audio: rejected before encoding with a strip-audio/ProRes recommendation.

A Windows smoke candidate completed a native-Xvid + PCM keep-audio test with 30/30 verified frames and identical audio packet hashes.
