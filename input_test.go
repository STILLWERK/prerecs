package main

import (
	"bufio"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePathInput(t *testing.T) {
	got := parsePathInput(`"C:\A B\one.mp4" "D:\two.mov"`)
	if len(got) != 2 || got[0] != `C:\A B\one.mp4` || got[1] != `D:\two.mov` {
		t.Fatalf("got %#v", got)
	}
	got = parsePathInput(`a.mp4;b.mov`)
	if len(got) != 2 {
		t.Fatalf("semicolon parse %#v", got)
	}
}

func TestExpandDirectory(t *testing.T) {
	td := t.TempDir()
	if err := os.WriteFile(filepath.Join(td, "b.mov"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(td, "a.mp4"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(td, "ignore.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := expandInputs([]string{td})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
	if filepath.Base(got[0]) != "a.mp4" || filepath.Base(got[1]) != "b.mov" {
		t.Fatalf("sort %#v", got)
	}
}

func TestGatherInputsEOF(t *testing.T) {
	old := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader(""))
	defer func() { stdinReader = old }()
	_, err := gatherInputs(nil, theme{})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Generated-name guard (#12), codec classes (#8), frame-rate fallback (#13),
// Vulkan probe timeout (#11)
// ---------------------------------------------------------------------------

func TestIsGeneratedOutputName(t *testing.T) {
	for name, want := range map[string]bool{
		"clip_prores_lt.mov":            true,
		"clip_xvid_max_q2.avi":          true,
		"clip_xvid_max_q2_3.avi":        true,
		"clip_utvideo_lossless_10.avi":  true,
		"CLIP_PRORES_LT.MOV":            true, // extension is case-insensitive
		"clip_prores_lt_extra.mov":      false,
		"clip_xvid_compact_abc.avi":     false,
		"clip_xvid_compact_7.bak":       false,
		"myxvid_compact.avi":            false, // no underscore boundary
		"xvid_compact.avi":              false, // bare preset name is not stem_preset
		"clip.mp4":                      false,
		"clip_prores_lt.mp4":            false, // outputs are only .avi/.mov
		"vacation_prores_4444.mov":      true,  // exact generated pattern: skip
		"vacation_prores_4444_12.mov":   true,
		"holiday_magicyuv_lossless.AVI": true,
	} {
		if got := isGeneratedOutputName(name); got != want {
			t.Errorf("isGeneratedOutputName(%q)=%v, want %v", name, got, want)
		}
	}
}

func TestExpandInputsSkipsGeneratedOutputs(t *testing.T) {
	td := t.TempDir()
	for _, name := range []string{"a.mp4", "b_prores_lt.mov", "c_xvid_max_q2_2.avi", "d_xvid_max_q2x.avi", "note.txt"} {
		if err := os.WriteFile(filepath.Join(td, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := expandInputs([]string{td})
	if err != nil {
		t.Fatal(err)
	}
	var bases []string
	for _, p := range got {
		bases = append(bases, filepath.Base(p))
	}
	want := []string{"a.mp4", "d_xvid_max_q2x.avi"}
	if strings.Join(bases, ",") != strings.Join(want, ",") {
		t.Fatalf("expanded=%v, want %v", bases, want)
	}
}

func TestExpandInputsSkipsTempStream(t *testing.T) {
	td := t.TempDir()
	for _, n := range []string{"clip.avi", "clip_xvid_compact.video.tmp.m4v", "clip_xvid_compact.avi"} {
		if err := os.WriteFile(filepath.Join(td, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := expandInputs([]string{td})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || filepath.Base(got[0]) != "clip.avi" {
		t.Fatalf("expandInputs=%v, want only clip.avi", got)
	}
}
