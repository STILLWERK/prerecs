package main

import (
	"math/big"
	"strings"
	"testing"
)

func TestVerifyRejectsFrameLoss(t *testing.T) {
	in := MediaInfo{FrameCount: 90, FPSFloat: 300, Duration: 0.3}
	out := MediaInfo{FrameCount: 88, FPSFloat: 300, Duration: 0.3}
	problems := verify(in, out, big.NewRat(300, 1), 0.3)
	if len(problems) == 0 {
		t.Fatal("expected frame-loss verification failure")
	}
}

func TestVerifyRejectsMissingTimingMetadata(t *testing.T) {
	in := MediaInfo{FrameCount: 100, FPSFloat: 25, Duration: 4}
	out := MediaInfo{FrameCount: 100}
	problems := verify(in, out, big.NewRat(25, 1), 4)
	var missingRate, missingDur bool
	for _, problem := range problems {
		if strings.Contains(problem, "frame rate missing") {
			missingRate = true
		}
		if strings.Contains(problem, "duration missing") {
			missingDur = true
		}
	}
	if !missingRate || !missingDur {
		t.Fatalf("missing timing metadata was not reported (rate=%v duration=%v): %v", missingRate, missingDur, problems)
	}
}

func TestShareVerifierRequiresYUV420P(t *testing.T) {
	in := MediaInfo{Width: 1920, Height: 1080, FrameCount: 30, FPSFloat: 30, Duration: 1}
	out := MediaInfo{Width: 1920, Height: 1080, FrameCount: 30, FPSFloat: 30, Duration: 1, Codec: "mpeg4", CodecTag: "XVID", PixelFormat: "yuv444p"}
	problems := verifyOutput(in, out, ConvertOptions{Preset: "xvid_max_q2", StripAudio: true}, big.NewRat(30, 1), 1)
	found := false
	for _, problem := range problems {
		if strings.Contains(problem, "pixel format mismatch") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Share verifier accepted wrong pixel format: %v", problems)
	}
}

func TestEfficientVerifierRequiresYUV420P(t *testing.T) {
	in := MediaInfo{Width: 1920, Height: 1080, FrameCount: 30, FPSFloat: 30, Duration: 1}
	out := MediaInfo{Width: 1920, Height: 1080, FrameCount: 30, FPSFloat: 30, Duration: 1, Codec: "mpeg4", CodecTag: "XVID", PixelFormat: "yuv444p"}
	problems := verifyOutput(in, out, ConvertOptions{Preset: "xvid_efficient_q2", StripAudio: true}, big.NewRat(30, 1), 1)
	found := false
	for _, problem := range problems {
		if strings.Contains(problem, "pixel format mismatch") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Efficient verifier accepted wrong pixel format: %v", problems)
	}
}

func TestPresetCodecMatches(t *testing.T) {
	if !presetCodecMatches("xvid_compact", MediaInfo{Codec: "mpeg4", CodecTag: "XVID"}) {
		t.Fatal("xvid should match")
	}
	if !presetCodecMatches("xvid_efficient_q2", MediaInfo{Codec: "mpeg4", CodecTag: "XVID"}) {
		t.Fatal("efficient xvid should match")
	}
	if presetCodecMatches("xvid_compact", MediaInfo{Codec: "mpeg4", CodecTag: "FMP4"}) {
		t.Fatal("wrong mpeg4 tag must not match")
	}
	if !presetCodecMatches("prores_lt", MediaInfo{Codec: "prores", Profile: "LT", PixelFormat: "yuv422p10le"}) {
		t.Fatal("prores should match")
	}
	if presetCodecMatches("prores_lt", MediaInfo{Codec: "prores", Profile: "4444", PixelFormat: "yuv444p10le"}) {
		t.Fatal("wrong ProRes profile must not match LT")
	}
	if !presetCodecMatches("magicyuv_lossless", MediaInfo{Codec: "magicyuv"}) {
		t.Fatal("magicyuv should match")
	}
}

func TestProResPresetMatchesProfileAndPixelFormat(t *testing.T) {
	if presetCodecMatches("prores_lt", MediaInfo{Codec: "prores", Profile: "4444", PixelFormat: "yuv444p10le"}) {
		t.Fatal("ProRes LT accepted a 4444-format output")
	}
	if presetCodecMatches("prores_4444", MediaInfo{Codec: "prores", Profile: "Standard", PixelFormat: "yuv422p10le"}) {
		t.Fatal("ProRes 4444 accepted a 422-format output")
	}
	if !presetCodecMatches("prores_4444", MediaInfo{Codec: "prores", Profile: "4444", PixelFormat: "yuv444p10le"}) {
		t.Fatal("ProRes 4444 rejected a non-alpha 4444 output")
	}
	if !presetCodecMatches("prores_4444", MediaInfo{Codec: "prores", Profile: "4444", PixelFormat: "yuv444p12le"}) {
		t.Fatal("ProRes 4444 rejected FFmpeg's encoder-normalized non-alpha output")
	}
	alphaIn := MediaInfo{HasAlpha: true}
	alphaOut := MediaInfo{Codec: "prores", Profile: "4444", PixelFormat: "yuva444p10le", HasAlpha: true}
	if !proresOutputMatchesInput("prores_4444", alphaIn, alphaOut) {
		t.Fatal("ProRes 4444 rejected an alpha-capable output")
	}
	if proresOutputMatchesInput("prores_4444", MediaInfo{}, alphaOut) {
		t.Fatal("non-alpha ProRes 4444 input accepted an alpha output")
	}
}

func TestVerifyOutputAudioAndFormat(t *testing.T) {
	in := MediaInfo{
		Width: 320, Height: 180, PixelFormat: "yuv420p", FrameCount: 30, FPSFloat: 30, Duration: 1,
		Audio: []string{"aac"},
	}
	good := MediaInfo{
		Codec: "mpeg4", CodecTag: "XVID", Width: 320, Height: 180, PixelFormat: "yuv420p",
		FrameCount: 30, FPSFloat: 30, Duration: 1,
		Audio: []string{"aac"},
	}
	if p := verifyOutput(in, good, ConvertOptions{Preset: "xvid_compact"}, big.NewRat(30, 1), 1); len(p) != 0 {
		t.Fatalf("good output problems: %v", p)
	}
	noAudio := good
	noAudio.Audio = nil
	if p := verifyOutput(in, noAudio, ConvertOptions{Preset: "xvid_compact"}, big.NewRat(30, 1), 1); len(p) == 0 {
		t.Fatal("missing copied audio should fail verification")
	}
	if p := verifyOutput(in, noAudio, ConvertOptions{Preset: "xvid_compact", StripAudio: true}, big.NewRat(30, 1), 1); len(p) != 0 {
		t.Fatalf("stripped audio should verify: %v", p)
	}
	badPix := good
	badPix.PixelFormat = "yuv422p"
	if p := verifyOutput(in, badPix, ConvertOptions{Preset: "xvid_compact"}, big.NewRat(30, 1), 1); len(p) == 0 {
		t.Fatal("wrong Xvid pixel format should fail verification")
	}
}
