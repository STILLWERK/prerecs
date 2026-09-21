package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOutputCollisionFamily(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "clip.avi")
	if err := os.WriteFile(src, []byte("src"), 0644); err != nil {
		t.Fatal(err)
	}
	existing, next, err := outputCandidates(src, d, "xvid_compact")
	if err != nil || len(existing) != 0 || !strings.HasSuffix(next, "clip_xvid_compact.avi") {
		t.Fatalf("initial existing=%v next=%q err=%v", existing, next, err)
	}
	if err := os.WriteFile(next, []byte("one"), 0644); err != nil {
		t.Fatal(err)
	}
	existing, next, err = outputCandidates(src, d, "xvid_compact")
	if err != nil || len(existing) != 1 || !strings.HasSuffix(next, "clip_xvid_compact_2.avi") {
		t.Fatalf("collision existing=%v next=%q err=%v", existing, next, err)
	}
	if err := os.WriteFile(next, []byte("two"), 0644); err != nil {
		t.Fatal(err)
	}
	existing, next, err = outputCandidates(src, d, "xvid_compact")
	if err != nil || len(existing) != 2 || !strings.HasSuffix(next, "clip_xvid_compact_3.avi") {
		t.Fatalf("second collision existing=%v next=%q err=%v", existing, next, err)
	}
}

func TestSameStemDefaultOutputReservationIsAtomic(t *testing.T) {
	d := t.TempDir()
	paths := []string{filepath.Join(d, "clip.avi"), filepath.Join(d, "clip.mov")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("source"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	type result struct {
		path string
		err  error
	}
	results := make(chan result, len(paths))
	var wg sync.WaitGroup
	for _, path := range paths {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			<-start
			_, next, err := outputCandidates(path, "", "xvid_max_q2")
			results <- result{path: next, err: err}
		}(path)
	}
	close(start)
	wg.Wait()
	close(results)

	var got []result
	for result := range results {
		got = append(got, result)
	}
	if len(got) != 2 || got[0].err != nil || got[1].err != nil {
		t.Fatalf("reservations=%+v", got)
	}
	if got[0].path == got[1].path {
		t.Fatalf("same-stem sources reserved the same output: %q", got[0].path)
	}
	for _, result := range got {
		if !fileExists(result.path) {
			t.Fatalf("reservation did not hold final target %q", result.path)
		}
	}
}

func TestOutputReservationCanBeReleasedOnCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reserved.avi")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	releaseOutputReservation(path)
	if fileExists(path) {
		t.Fatal("released output reservation still exists")
	}
}

func TestEfficientOutputSuffix(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "clip.avi")
	if err := os.WriteFile(src, []byte("src"), 0644); err != nil {
		t.Fatal(err)
	}
	existing, next, err := outputCandidates(src, d, "xvid_efficient_q2")
	if err != nil || len(existing) != 0 || !strings.HasSuffix(next, "clip_xvid_efficient_q2.avi") {
		t.Fatalf("efficient output existing=%v next=%q err=%v", existing, next, err)
	}
}

// ---------------------------------------------------------------------------
// Sparse numbered outputs (#5)
// ---------------------------------------------------------------------------

func TestOutputCandidatesSparse(t *testing.T) {
	td := t.TempDir()
	src := filepath.Join(td, "clip.avi")
	if err := os.WriteFile(src, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	// Sparse: base name missing, _2 and _4 exist.
	for _, n := range []string{"clip_xvid_compact_2.avi", "clip_xvid_compact_4.avi"} {
		if err := os.WriteFile(filepath.Join(out, n), []byte("old"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	existing, next, err := outputCandidates(src, out, "xvid_compact")
	if err != nil {
		t.Fatal(err)
	}
	if len(existing) != 2 || filepath.Base(existing[0]) != "clip_xvid_compact_2.avi" || filepath.Base(existing[1]) != "clip_xvid_compact_4.avi" {
		t.Fatalf("existing=%v, want [_2 _4] in order", existing)
	}
	if filepath.Base(next) != "clip_xvid_compact.avi" {
		t.Fatalf("next=%s, want base name (lowest free)", filepath.Base(next))
	}
}

func TestOutputCandidatesContiguous(t *testing.T) {
	td := t.TempDir()
	src := filepath.Join(td, "clip.avi")
	if err := os.WriteFile(src, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	for _, n := range []string{"clip_xvid_compact.avi", "clip_xvid_compact_2.avi"} {
		if err := os.WriteFile(filepath.Join(out, n), []byte("old"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	existing, next, err := outputCandidates(src, out, "xvid_compact")
	if err != nil {
		t.Fatal(err)
	}
	if len(existing) != 2 {
		t.Fatalf("existing=%v", existing)
	}
	if filepath.Base(next) != "clip_xvid_compact_3.avi" {
		t.Fatalf("next=%s, want _3", filepath.Base(next))
	}
	releaseOutputReservation(next)
	// Non-numeric or wrong-suffix names must not be treated as candidates.
	os.WriteFile(filepath.Join(out, "clip_xvid_compact_x.avi"), []byte("y"), 0644)
	os.WriteFile(filepath.Join(out, "clip_xvid_compact_7.bak"), []byte("y"), 0644)
	os.WriteFile(filepath.Join(out, "clip_xvid_compact_99.avi"), []byte("old"), 0644)
	existing, next, err = outputCandidates(src, out, "xvid_compact")
	if err != nil {
		t.Fatal(err)
	}
	if len(existing) != 3 || filepath.Base(existing[2]) != "clip_xvid_compact_99.avi" {
		t.Fatalf("existing=%v", existing)
	}
	if filepath.Base(next) != "clip_xvid_compact_3.avi" {
		t.Fatalf("next=%s, want lowest free slot _3", filepath.Base(next))
	}
}

func TestHasDuplicateOutputStems(t *testing.T) {
	mk := func(p string) MediaInfo { return MediaInfo{Path: p} }
	// Same stem + same effective output dir (default converted_prerecs) → dup.
	if !hasDuplicateOutputStems("", []MediaInfo{mk("/a/clip.avi"), mk("/a/clip.mov")}) {
		t.Fatal("same-dir same-stem not detected")
	}
	// Same stem but different source dirs → different default output dirs → ok.
	if hasDuplicateOutputStems("", []MediaInfo{mk("/a/clip.avi"), mk("/b/clip.mov")}) {
		t.Fatal("different-dir same-stem falsely detected")
	}
	// A shared custom output dir makes cross-dir same-stem sources collide.
	if !hasDuplicateOutputStems("/out", []MediaInfo{mk("/a/clip.avi"), mk("/b/clip.mov")}) {
		t.Fatal("custom-outdir same-stem not detected")
	}
	// Case-insensitive stem match (Windows filesystems).
	if !hasDuplicateOutputStems("", []MediaInfo{mk("/a/Clip.avi"), mk("/a/CLIP.mkv")}) {
		t.Fatal("case-variant same-stem not detected")
	}
	if hasDuplicateOutputStems("/out", []MediaInfo{mk("/a/one.avi"), mk("/b/two.avi")}) {
		t.Fatal("distinct stems falsely detected")
	}
}
