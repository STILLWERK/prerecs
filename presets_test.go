package main

import (
	"bufio"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestSourceClassification(t *testing.T) {
	cases := []struct {
		in   MediaInfo
		want string
	}{
		{MediaInfo{Codec: "h264"}, "compressed"},
		{MediaInfo{Codec: "hevc"}, "compressed"},
		{MediaInfo{Codec: "mpeg4", CodecTag: "XVID"}, "compressed"},
		{MediaInfo{Codec: "lagarith"}, "lossless"},
		{MediaInfo{Codec: "magicyuv"}, "lossless"},
		{MediaInfo{Codec: "ffv1"}, "lossless"},
		{MediaInfo{Codec: "prores"}, "intermediate"},
	}
	for _, tc := range cases {
		if got := sourceClass(tc.in); got != tc.want {
			t.Fatalf("sourceClass(%+v)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestEditPresetDefaultsToProResLT(t *testing.T) {
	got, err := normalizePreset("edit")
	if err != nil {
		t.Fatal(err)
	}
	if got != "prores_lt" {
		t.Fatalf("edit preset=%q", got)
	}
	got, err = normalizePreset("prores")
	if err != nil {
		t.Fatal(err)
	}
	if got != "prores_422" {
		t.Fatalf("explicit prores preset=%q", got)
	}
}

func TestNon4444ProResRejectsAlphaInputs(t *testing.T) {
	alpha := []MediaInfo{{Path: "alpha.mkv", HasAlpha: true}}
	for _, preset := range []string{"prores_lt", "prores_422", "prores_hq"} {
		if err := validatePresetInputs(preset, alpha); err == nil || !strings.Contains(err.Error(), "ProRes 4444") {
			t.Fatalf("%s accepted alpha input without a ProRes 4444 error: %v", preset, err)
		}
	}
	if err := validatePresetInputs("prores_4444", alpha); err != nil {
		t.Fatalf("ProRes 4444 rejected alpha input: %v", err)
	}

	e := &Engine{enc: map[string]bool{"prores_ks": true}}
	info := alpha[0]
	info.FPS = "30/1"
	info.FPSFloat = 30
	info.Duration = 1
	if _, _, _, err := e.buildCommand(info, ConvertOptions{Preset: "prores_lt"}, "out.mov"); err == nil {
		t.Fatal("buildCommand silently accepted alpha input for ProRes LT")
	}
}

func TestPresetMenuMapsShareAndFastCorrectly(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()

	e := &Engine{caps: Capabilities{HasXvid: true}}
	stdinReader = bufio.NewReader(strings.NewReader("1\n"))
	got, err := choosePreset(theme{}, e, nil)
	if err != nil || got != "xvid_max_q2" {
		t.Fatalf("main SHARE menu=%q, want xvid_max_q2", got)
	}

	stdinReader = bufio.NewReader(strings.NewReader("4\n3\n"))
	got, err = choosePreset(theme{}, e, nil)
	if err != nil || got != "xvid_efficient_q2" {
		t.Fatalf("MORE Xvid Q2 Efficient=%q, want xvid_efficient_q2", got)
	}

	stdinReader = bufio.NewReader(strings.NewReader("4\n4\n"))
	got, err = choosePreset(theme{}, e, nil)
	if err != nil || got != "xvid_compact" {
		t.Fatalf("MORE Xvid Q2 Fast=%q, want xvid_compact", got)
	}
}

func TestShareAliasesUseTunedQ2AndFastKeepsLegacyCompact(t *testing.T) {
	for _, alias := range []string{"share", "compact", "xvid", "xvid-q2", "xvid-max-q2"} {
		got, err := normalizePreset(alias)
		if err != nil {
			t.Fatalf("normalizePreset(%q): %v", alias, err)
		}
		if got != "xvid_max_q2" {
			t.Fatalf("normalizePreset(%q)=%q, want xvid_max_q2", alias, got)
		}
	}
	for _, alias := range []string{"xvid-efficient", "xvid-q2-efficient", "efficient"} {
		got, err := normalizePreset(alias)
		if err != nil {
			t.Fatalf("normalizePreset(%q): %v", alias, err)
		}
		if got != "xvid_efficient_q2" {
			t.Fatalf("normalizePreset(%q)=%q, want xvid_efficient_q2", alias, got)
		}
	}
	for _, alias := range []string{"xvid-fast", "xvid-compact", "fast", "compat", "compatibility"} {
		got, err := normalizePreset(alias)
		if err != nil {
			t.Fatalf("normalizePreset(%q): %v", alias, err)
		}
		if got != "xvid_compact" {
			t.Fatalf("normalizePreset(%q)=%q, want xvid_compact", alias, got)
		}
	}
	if got := presetLabel("xvid_efficient_q2"); got != "Xvid Q2 Efficient" {
		t.Fatalf("efficient label=%q", got)
	}
	if got := presetLabel("xvid_max_q2"); got != "Share / Xvid Q2" {
		t.Fatalf("share label=%q", got)
	}
	if got := presetLabel("xvid_compact"); got != "Xvid Q2 Fast / Compatibility" {
		t.Fatalf("fast label=%q", got)
	}
}

func TestXvidAvailabilityUsesSourceEligibility(t *testing.T) {
	caps := Capabilities{HasXvid: true, HasNativeXvid: true, HasLibXvid: false}
	eligible := []MediaInfo{{Path: `C:\clips\master.avi`, Codec: "lagarith"}}
	nonEligible := []MediaInfo{{Path: `C:\clips\master.mkv`, Codec: "ffv1"}}
	mixed := append(append([]MediaInfo{}, eligible...), nonEligible...)

	if ok, _ := xvidAvailableForInputs(caps, eligible, false); !ok {
		t.Fatal("native-only eligible AVI should be available without libxvid")
	}
	if ok, _ := xvidAvailableForInputs(caps, nonEligible, false); ok {
		t.Fatal("native-only capability should not be available for non-eligible input")
	}
	if ok, _ := xvidAvailableForInputs(caps, mixed, false); ok {
		t.Fatal("native-only capability should not be available for a mixed batch")
	}
}

func TestXvidRecommendationDoesNotPromiseMissingFallback(t *testing.T) {
	eligible := []MediaInfo{{Path: `C:\clips\master.avi`, Codec: "lagarith"}}
	nonEligible := []MediaInfo{{Path: `C:\clips\master.mkv`, Codec: "ffv1"}}

	nativeOnly := Capabilities{HasXvid: true, HasNativeXvid: true, HasLibXvid: false}
	if got := xvidBackendDescription(nativeOnly, eligible); strings.Contains(strings.ToLower(got), "fallback") {
		t.Fatalf("native-only recommendation promised a fallback: %q", got)
	}
	if got := xvidBackendDescription(nativeOnly, nonEligible); !strings.Contains(strings.ToLower(got), "unavailable") {
		t.Fatalf("native-only non-eligible recommendation=%q, want unavailable", got)
	}

	withFallback := Capabilities{HasXvid: true, HasNativeXvid: true, HasLibXvid: true}
	if got := xvidBackendDescription(withFallback, eligible); !strings.Contains(strings.ToLower(got), "fallback") {
		t.Fatalf("native+libxvid recommendation=%q, want fallback", got)
	}
}

func TestAlphaInteractivePresetStaysInMenuForProRes4444(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	e := &Engine{caps: Capabilities{HasProRes: true}}
	stdinReader = bufio.NewReader(strings.NewReader("2\n4\n7\n"))
	got, err := choosePreset(theme{}, e, []MediaInfo{{Path: "alpha.mkv", HasAlpha: true}})
	if err != nil || got != "prores_4444" {
		t.Fatalf("alpha interactive selection=%q err=%v, want ProRes 4444 after staying in menu", got, err)
	}
}

func TestAlphaRecommendationUsesProRes4444(t *testing.T) {
	if got := recommendedProResPreset([]MediaInfo{{Path: "alpha.mkv", HasAlpha: true}}); got != "prores_4444" {
		t.Fatalf("alpha recommendation=%q, want prores_4444", got)
	}
	if got := recommendedProResPreset([]MediaInfo{{Path: "master.avi"}}); got != "prores_lt" {
		t.Fatalf("normal recommendation=%q, want prores_lt", got)
	}
}

// --no-native-xvid must hide the native backend from availability checks and
// plan text so libxvid becomes the only accepted Xvid path.
func TestNoNativeXvidMasksAvailability(t *testing.T) {
	caps := Capabilities{HasXvid: true, HasNativeXvid: true, HasLibXvid: false, XvidEncRaw: "xvid_encraw"}
	infos := []MediaInfo{{Path: `C:\clips\master.avi`, Codec: "lagarith"}}
	if ok, missing := xvidAvailableForInputs(caps, infos, false); !ok || len(missing) != 0 {
		t.Fatalf("native-eligible input should be covered by native Xvid: ok=%v missing=%v", ok, missing)
	}
	masked := nativeXvidCaps(caps, true)
	if ok, missing := xvidAvailableForInputs(masked, infos, false); ok || len(missing) != 1 {
		t.Fatalf("--no-native-xvid should require libxvid: ok=%v missing=%v", ok, missing)
	}
	if masked.HasXvid {
		t.Fatal("HasXvid aggregate must collapse to libxvid when native is masked")
	}
	masked.HasLibXvid = true
	if ok, _ := xvidAvailableForInputs(masked, infos, false); !ok {
		t.Fatal("libxvid should satisfy availability when native Xvid is disabled")
	}
	if got := xvidBackendDescription(masked, infos); !strings.Contains(strings.ToLower(got), "ffmpeg") {
		t.Fatalf("backend description should name FFmpeg under --no-native-xvid, got %q", got)
	}
}

func TestAudioCopyCompatibility(t *testing.T) {
	for _, codec := range []string{"pcm_s16le", "pcm_s24le", "mp3", "mp2"} {
		if !audioCopyCompatible("xvid_compact", codec) {
			t.Fatalf("%s should be safe for AVI stream copy", codec)
		}
	}
	if audioCopyCompatible("xvid_compact", "aac") {
		t.Fatal("AAC should not be treated as safe for AVI stream copy")
	}
	if !audioCopyCompatible("prores_lt", "aac") {
		t.Fatal("AAC should be safe for MOV/ProRes stream copy")
	}
	infos := []MediaInfo{{Path: "clip.mp4", Audio: []string{"aac"}}}
	bad := incompatibleAudioCopies("xvid_compact", infos)
	if len(bad) != 1 || bad[0] != "clip.mp4=AAC" {
		t.Fatalf("bad=%v", bad)
	}
	if bad = incompatibleAudioCopies("prores_lt", infos); len(bad) != 0 {
		t.Fatalf("ProRes/AAC should be compatible, got %v", bad)
	}
}

func TestChoosePresetEOF(t *testing.T) {
	old := stdinReader
	defer func() { stdinReader = old }()
	e := &Engine{caps: Capabilities{HasXvid: true}}
	stdinReader = bufio.NewReader(strings.NewReader(""))
	done := make(chan error, 1)
	go func() {
		_, err := choosePreset(theme{}, e, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expected EOF, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("choosePreset looped forever on EOF")
	}
}

func TestSourceClassDistribution(t *testing.T) {
	for codec, want := range map[string]string{
		"h264": "compressed", "hevc": "compressed", "av1": "compressed",
		"vp9": "compressed", "mpeg4": "compressed", "mpeg2video": "compressed",
		"wmv1": "compressed", "wmv2": "compressed", "wmv3": "compressed",
		"h263": "compressed", "h263p": "compressed", "flv1": "compressed",
		"theora": "compressed", "vp6": "compressed", "vp6f": "compressed",
		"vp6a": "compressed", "cinepak": "compressed", "msmpeg4v3": "compressed",
		"lagarith": "lossless", "ffv1": "lossless", "utvideo": "lossless",
		"prores": "intermediate", "dnxhd": "intermediate",
		"mjpeg":   "other", // acquisition/intermediate codec, not distribution
		"unknown": "other",
	} {
		if got := sourceClass(MediaInfo{Codec: codec}); got != want {
			t.Errorf("sourceClass(%s)=%q, want %q", codec, got, want)
		}
	}
	// Codec-tag driven detection still works for xvid/divx/dx50 mpeg4.
	if got := sourceClass(MediaInfo{Codec: "mpeg4", CodecTag: "DX50"}); got != "compressed" {
		t.Errorf("mpeg4/DX50 = %q", got)
	}
}
