# PreRecs research and tuning notes

These notes describe why the release presets are configured the way they are. Numbers from the real-footage bakeoff are machine/material specific; they are evidence for the defaults, not universal codec benchmarks.

## Real test material

Primary master used for the final Xvid bakeoff:

- `death_jump.avi`
- Lagarith lossless
- 2560×1440
- 30 fps capture timing
- 645 decoded frames
- 21.5 s slowed capture
- ~500 MiB
- conformed target: 300 fps / 2.15 s

Additional downloaded examples were 2560×1440 H.264 at 600 fps. Those were useful for proving that Compact must be source-aware: an already-compressed ~228 MiB H.264 clip became ~1.74 GiB under the first FFmpeg/libxvid Q2 implementation.

## Xvid encoder choice

The legacy batch file used FFmpeg's native `mpeg4` encoder plus an XVID FourCC. The first remake moved to FFmpeg `libxvid`.

Real high-FPS testing found an important difference: FFmpeg/libxvid Q2 encoded directly at 300 fps was substantially smaller but also measurably less faithful than the same frames encoded at their original timing. On a 90-frame section of the Lagarith master:

- FFmpeg/libxvid Q2 at 30-fps timing: ~1.31 MB, SSIM ~0.99849, PSNR ~57.06 dB
- FFmpeg/libxvid Q2 at 300-fps timing: ~0.74 MB, SSIM ~0.99759, PSNR ~54.97 dB

The installed Xvid reference encoder (`xvid_encraw`, xvidcore 1.3.7) did not show that high-FPS penalty. Native Xvid Q2 at 300 fps produced:

- 90/90 decoded frames
- true 300/300 fps AVI timing
- ~1.34 MB
- SSIM ~0.99860
- PSNR ~57.33 dB

Therefore compatible lossless AVI masters prefer native Xvid. FFmpeg/libxvid remains the universal fallback.

## Native Xvid Q presets

On the same 90-frame Lagarith section at 300 fps:

- Q1: ~4.35 MB, SSIM ~0.99921
- Q2: ~1.34 MB, SSIM ~0.99860
- Q3: ~0.99 MB, SSIM ~0.99856

Q2 became the original Compact preset. Q3 remains a useful smaller/faster quantizer option. Q1 has strongly diminishing returns and is kept only as an advanced extreme-Xvid option. In the public source, normal SHARE uses the later VHQ4/B2 Q2 tuning; the original VHQ1/B0 Q2 behavior remains available as `xvid-fast`.

The encoder's default minimum I/P quantizer is 2, so PreRecs explicitly sets the min/max I/P quantizer bounds to the selected Q value. This makes the Q1 option genuinely Q1 rather than silently clamping it to Q2.

## Xvid settings rejected after testing

- **B-frames in the old direct-AVI test**: the original 90 -> 88 result was later traced to delayed-output/draining and mux handling, not inherent frame loss from B-frames. The release retested B-frames through the completed raw elementary-stream path and enabled B2 in the strict Q2 path that is now normal SHARE.
- **Huge HFR GOPs**: rejected. GOP 600/1200/3000 saved only ~0.23% versus GOP 240 on the 645-frame master.
- **ME quality 6** through FFmpeg: rejected for default. About 20% slower for ~0.27% size reduction on the sample.
- **Full RD (`mbd=rd`)** through FFmpeg: rejected for default. About 11% smaller but roughly 7.6× slower.
- **Forced FFmpeg Xvid thread counts**: no meaningful speed change in the tested build.
- **Xvid app-level multi-instance mode**: rejected for the main path. It encoded the full historical Compact benchmark at ~22 fps versus ~24.7 fps for that older explicit 8-thread/4-slice direct-AVI path, then still required remuxing. Current SHARE/Efficient native Xvid uses one slice.
- **Raw YUV stdin to the Windows reference encoder**: rejected. A long binary stream terminated early when the C runtime interpreted a byte as text EOF. Native Xvid therefore reads eligible AVI masters through VfW, but the release first verifies that the legacy VfW path can actually reach the final advertised frame. Large/OpenDML AVIs that fail that preflight go directly to FFmpeg libxvid.

## Native Xvid quality-search tradeoff

On the full 645-frame Lagarith clip:

- quality 6 / VHQ 1: ~24.7 fps, ~36.2 MB
- quality 4 / VHQ 0: ~26.8 fps, ~38.2 MB, essentially the same SSIM on the checked section

The earlier Compact implementation kept quality 6 / VHQ 1. That exact behavior is retained as the Fast/Compatibility preset while normal SHARE uses the later VHQ4/B2 tuning.

## Xvid Q2 Efficient B-frame quantizer study

After VHQ4/B2 strict Q2 was established as the normal SHARE path, the efficiency study focused on reducing size without weakening the Q2 reference pictures.

The useful discovery was to keep I/P VOPs at Q2 while allowing B-VOPs to quantize slightly higher. With I/P fixed Q2, B quantizer range 2..31, ratio 100, and offset 100, Xvid 1.3.7 consistently selected B-Q3 on the tested material.

Across five unrelated 180-frame real masters, B-Q3 reduced size by 5.962%, 3.701%, 4.116%, 8.794%, and 7.697%, averaging 6.054%. Mean quality deltas versus strict all-Q2 were SSIM -0.000009, PSNR -0.078 dB, and VMAF -0.025; the worst source-level VMAF delta was -0.053.

A 645-frame Ballista regression crossed GOP boundaries and retained all 645 pictures. Raw stream size fell from 22,002,893 to 20,372,979 bytes (about 7.4%). SSIM changed 0.997741 -> 0.997706, PSNR 54.774666 -> 54.603443 dB, and VMAF 97.567358 -> 97.535116. Frame-by-frame VMAF had median delta 0.0, worst delta -0.393, and no frame worse by 0.5 or more.

More aggressive B quantizers showed diminishing returns on the XPR sample:

- B-Q3: about 6.0% smaller, VMAF -0.053;
- B-Q4: about 8.1% smaller, VMAF -0.154;
- B-Q5: about 9.7% smaller, VMAF -0.187;
- B-Q6: about 10.9% smaller, VMAF -0.239.

Other retests did not beat the selected point: VHQ2/VHQ3 traded too much size for little/no speed, quality 5 was larger, PSNR-HVS-M RD was slower/larger, B3 was dramatically larger, and open GOP saved only about 0.03% on the long clip.

The result is kept separate as **Xvid Q2 Efficient** rather than replacing strict SHARE. Strict SHARE remains the highest-quality Q2 path; Efficient is the better storage-efficiency trade.

## SHARE default-preset decision

The public source does not introduce a new Xvid quality algorithm. It selects which already-tested Q2 path is considered normal.

The decision follows the earlier bakeoff: the VHQ4/B2 strict-Q2 configuration repeatedly measured slightly higher quality while cutting file size by roughly 33.7% to 49.1% across five unrelated masters. On the first 180-frame sample it was about 48.6% smaller than VHQ1/B0 Compact and also scored higher by SSIM, PSNR, and VMAF.

A fresh behavioral regression used the same 180-frame real Lagarith material. The legacy `compact` command selected the tuned path and produced 3,678,424 bytes with B-frames. `xvid-fast` selected the preserved old Compact path and produced 7,161,362 bytes with B0. Both outputs were exactly 180 frames, 300/1 fps, XVID/YUV420P, and passed full `-xerror` decoding.

Because the old path's remaining advantage is mainly modest encode speed, it is now named Fast/Compatibility rather than remaining the normal SHARE choice.

## Strict Q2 Xvid study

The search kept Compact unchanged and tested a separate strict-Q2 path. On the first 180-frame real Lagarith sample:

- Compact Q2 / VHQ1 / B0: 7.15 MB at 30.1 fps
- VHQ4 / B0: 6.20 MB at 18.0 fps
- VHQ4 / B1 with fixed-Q2 B frames: 5.80 MB at 21.5 fps
- VHQ4 / B2 with fixed-Q2 B frames: 3.67 MB at 26.0 fps

All B-frame configurations decoded all 180/180 pictures. Compact measured SSIM 0.995222 / PSNR 51.49 dB / VMAF 96.906; Maximum Q2 measured SSIM 0.997783 / PSNR 54.62 dB / VMAF 97.265 while being about 48.6% smaller.

Across five unrelated real masters, Maximum Q2 reduced the first 300 encoded frames versus Compact by about 45.4%, 43.6%, 33.7%, 49.1%, and 42.9%. The quality advantage also repeated across those samples rather than appearing only on the tuning clip.

Settings that did not survive the bakeoff:

- disabling B-frame RD made output larger and slower;
- MPEG quantization was larger and slower than H.263 quantization;
- QPel was about 26% larger and substantially slower;
- GMC had essentially no size benefit and was slower;
- QPel + GMC was clearly worse;
- four slices were only marginally faster and about 0.5% larger than one slice;
- variance masking saved about 5% and could raise VMAF, but reduced SSIM/PSNR, so it remains off for strict Q2;
- a bundled custom matrix looked excellent on one sample but was not universal and became larger on another source, so Maximum does not use a custom matrix.

The resulting native Maximum configuration is quality 6, VHQ4, B2, B-frame RD, fixed Q2 for I/P/B, B quantizer ratio 100 / offset 0, H.263 quantization, unpacked bitstream, GOP 240, 8 threads, and one slice. AQ/masking, QPel, GMC, and custom matrices are off.

The FFmpeg fallback intentionally differs: Q2 + full macroblock RD + B0. FFmpeg's normal Xvid-in-AVI path uses packed bitstream behavior when B-frames are enabled, so Maximum does not enable fallback B-frames merely to mirror the native path.

## ProRes

Main release choices after the real Nade Raid bakeoff:

- ProRes 422 LT: default Edit-ready preset
- ProRes 422: Quality preset
- ProRes 422 HQ: advanced maximum normal ProRes tier
- ProRes 4444: advanced only for actual RGB/4:4:4/alpha sources

`prores_ks` supports frame/slice threading. On the real 1440p Lagarith sample, leaving threading automatic was faster than forcing 4, 8, or 16 threads. The implementation therefore leaves thread selection to FFmpeg and uses the profile-selected quantization matrix (`quant_mat=auto`).

The Vulkan ProRes encoder worked on the test machine and was only modestly faster on the sample (~69.6 fps versus ~66 fps CPU for a short test), so it is not a default release path.

## Timescale conform

For capture FPS `F` and game timescale `T`, effective FPS is `F/T`.

PreRecs rebuilds video timing from the existing frame sequence and then verifies decoded frame count. It does not use optical-flow interpolation or fabricate frames.

For native Xvid AVI masters, the Xvid reference encoder is given the effective target frame rate directly. For FFmpeg/ProRes and fallback paths, timestamps are rebuilt with frame-number-based timing.

## MagicYUV

MagicYUV is optional and paid. It remains an advanced lossless-master option only when an installed Windows MagicYUV codec/plugin and FFmpeg's MagicYUV encoder are detected. It is never bundled or required for community distribution.


## 2026-09-19 native Xvid AVI container finding

Real 1440p300 outputs from Xvid 1.3.7 decoded with exact frame counts, but MediaInfo reported AVI conformance errors such as `File size is less than expected size` and `Element size is more than maximal permitted size`. Direct inspection showed the native Xvid AVI had a RIFF-declared size 482 bytes shorter than the actual file.

A lossless FFmpeg remux (`-c:v copy -vtag XVID`) produced an AVI whose RIFF-declared size exactly matched the file size. All 1,196 compressed video packet MD5 hashes matched before and after the remux. The cleanup remux was later superseded by the current design: native Xvid writes a raw MPEG-4 Part 2 elementary stream and FFmpeg creates the final AVI directly, so the legacy Xvid AVI writer is no longer used for output at all.


## 2026-09-19 ProRes / timing / progress regression

A second real-footage pass used four 2560×1440 30 fps Lagarith masters totaling 2,998,126,970 bytes (~2.79 GiB), conformed to 300 fps.

The first FFmpeg conform implementation was unsafe: `setpts=N/(300*TB)` was evaluated in the source 1/30 timebase and then combined with `-r 300 -fps_mode cfr`. FFmpeg reported duplicated and dropped frames in short tests even when the final total frame count matched the input.

The release now assigns one integer timestamp per captured frame in the target-rate timebase and normalizes frame duration before the encoder:

`settb=expr=1/target_fps,setpts=N,fps=target_fps`

with `-fps_mode passthrough -enc_time_base filter`. A 30-frame FFV1 lossless regression at 30→300 fps produces exactly 30 output frames, exactly 0.100 s, exactly 300 fps, and all 30 decoded frame hashes match the source in order.

On all four real masters, ProRes 422 LT totaled ~2.30 GiB versus ~2.79 GiB Lagarith and ~3.19 GiB ProRes 422. LT also decoded faster in the local FFmpeg null-sink test (~704–784 fps) than both standard 422 (~633–736 fps) and Lagarith (~360–479 fps). Standard 422 remains slightly cleaner by SSIM, but LT is the better default for the intended edit-ready/storage balance.

Initial analysis is now metadata-only. On the same four AVI masters the metadata pass completed in about 172 ms; the old `-count_frames` analysis required a full stream scan. Exact full decoding is deferred to sources without trustworthy `nb_frames` and to final verification.

FFmpeg stages use `-progress pipe:1 -stats_period 0.25 -nostats`. Earlier development still depended on native Xvid's carriage-return console progress. That interface proved too buffered for short prerecs and was replaced by direct VOP counting from the growing raw MPEG-4 elementary stream.


## Packaged real-folder regression

The actual Windows EXE, not just raw FFmpeg commands, was run over the complete four-file Nade Raid set. ProRes 422 LT finished 4/4 verified at 2,471,157,118 bytes total (-17.58% vs Lagarith). Native Xvid Q2 finished 4/4 verified at 180,709,430 bytes total (-93.97%). Every cleaned Xvid AVI had a RIFF declared-size delta of exactly zero.

Native Xvid's own 10-frame progress records are reliable, but percentage-based ETA is noisy during codec warm-up. The release therefore hides ETA until both 5% of frames and two seconds have elapsed. Final verification derives decode fps from counted frames divided by wall-clock time, rather than trusting FFmpeg's null-output `fps=0.00` field.

## Live-Xvid and intermediate-workflow findings

Xvid 1.3.7's Windows progress writes are buffered enough that a parser can receive most updates only near the end of a short prerec. Its AVIFile output also locks the AVI against concurrent reads. PreRecs therefore no longer depends on either interface for progress.

The Xvid reference encoder writes an MPEG-4 Part 2 elementary stream during encoding. PreRecs incrementally scans newly appended bytes for the VOP start code `00 00 01 B6`. With B-frames disabled, the VOP count is the encoded picture count. A real 645-frame master showed useful progress from 4/645 onward rather than sitting at 0%.

The final stream is wrapped by FFmpeg with `-c:v copy -vtag XVID`. A 90-frame A/B test found every compressed packet hash and every decoded frame hash identical to the old direct-AVI Xvid output.

Downloaded community AVIs can contain internally inconsistent metadata. One H.264 AVI advertised 2,046 frames and 3.410 s at 600 fps but decoded to 1,997 pictures. Its decoded timestamps were also incomplete/gapped. PreRecs now treats compressed-source metadata counts as estimates, decodes once to determine the real picture count, and constructs a clean CFR editing timeline from frame number at the nominal rate. The correct intermediate in this case is 1,997 frames / 600 fps = 3.3283 s.

MagicYUV testing also established that the tested FFmpeg encoder exposes 8-bit planar formats. YUV444 must remain YUV444; converting it to RGB would be an unnecessary color-representation change. A 45-frame FFV1 YUV444 test round-tripped through both MagicYUV and Ut Video with all decoded frame hashes identical. Higher-bit-depth inputs are rejected for these two presets rather than silently reduced to 8-bit.

## Lossless AVI frame-count guard

Lossless AVI metadata remains a fast path when its advertised frame count agrees with the rounded duration/frame-rate estimate. Before native Xvid uses that count for VfW final-frame preflight, the `-frames` limit, or final verification, missing timing metadata or a count/duration mismatch now triggers one exact decode scan. This keeps the normal corpus fast while preventing an obviously inconsistent AVI from validating the native path against its own bad count.

The representative `vdub` material checked during this pass was internally consistent: `nade_cine_green.avi` reported and decoded 641 frames at 30 fps, and the existing stress evidence covers the large/OpenDML files that require the VfW fallback.

## Stability findings

A full real-folder stress run covered 30 Lagarith masters across `ballista_yemen`, `dsr_skate`, `nade_raid`, `shotgun_frost`, and `xpr_slums`: 38.2 GiB total, all 2560x1440/30 fps/YUV420, conformed to 300 fps. The packaged Windows build finished 30/30 verified with zero failures. Five large/OpenDML AVIs could not expose their final frame through the legacy VfW path; the new final-frame preflight identified all five before native Xvid was started and routed them directly to FFmpeg libxvid. There were zero unexpected native-Xvid runtime failures.

An independent second verification pass used `ffprobe -count_frames` rather than PreRecs' own verifier. It confirmed 30/30 outputs had the exact source frame count, 300/1 timing, XVID/MPEG-4 video, YUV420, matching dimensions, and no unexpected audio. A second whole-corpus collision pass launched zero encoders: all 30 existing outputs were decoded, accepted, and skipped with no numbered duplicates.

A preset matrix on a real 2560x1440 Lagarith master (641 frames, 30->300) passed ProRes LT, ProRes 422, ProRes 422 HQ, MagicYUV, Ut Video, Xvid Q3, and Xvid Q1. ProRes 4444 received a separate RGB/alpha regression. That test exposed an option-propagation bug: passing an RGB source's `-colorspace gbr` as an encoder option to the YUV-only `prores_ks` path fails. The release keeps the GBR tag on the input/frame side but does not pass the invalid encoder option to YUV Xvid/ProRes outputs. The final 4444 output decoded as 30/30 frames with profile 4444 / alpha, and the extracted alpha-plane MD5 matched the source exactly. The same RGB source also passed the FFmpeg Xvid fallback.

Audio behavior was deliberately kept simple. At normal timing, compatible audio is stream-copied unchanged or stripped. Packet hashes matched for AAC copied into ProRes/MOV and PCM copied into Xvid/AVI. AAC -> Xvid/AVI is blocked instead of pretending the container change is lossless; the user must strip audio or use an edit-ready MOV path. Conformed timing strips audio to avoid silent desynchronization.


## Batch performance research

- Earlier single-job thread/slice scaling for the historical Compact path on the i7-13700K plateaued around 8 threads / 4 slices. Current SHARE/Efficient native Xvid uses up to 8 threads and one slice; the old 8/4 result is retained only as benchmark context.
- Batch-level native-Xvid concurrency was the large safe win: four real clips completed end-to-end through PreRecs in 37.03 s versus 104.10 s sequentially (2.81x), with byte-identical sequential/parallel outputs. Six concurrent Xvid jobs were slower than four.
- Full decoded Xvid verification was cheap (~1.48 s total for four outputs), so verification remains mandatory.
- MagicYUV `gradient` remained the best tested predictor balance; two concurrent jobs helped, four did not.
- `prores_ks_vulkan` showed a major throughput gain on the RTX 3060. Async depth 4 reached ~144 fps versus ~35 fps CPU on a 300-frame 2560x1440 LT sample; depth 8 added little. Because the encoder is new, it is treated as an experimental fast path with an actual encode probe and automatic CPU fallback.
- Xvid raw-stdin piping was investigated as a way around the large-AVI VfW limitation. The installed Windows Xvid 1.3.7 CLI is not binary-stdin safe in this environment, so PreRecs does not ship a custom patched Xvid helper; large VfW-incompatible AVIs continue using the verified FFmpeg libxvid fallback.
