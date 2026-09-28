package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceSignatureDistinguishesContent(t *testing.T) {
	td := t.TempDir()
	a := filepath.Join(td, "a.bin")
	b := filepath.Join(td, "b.bin")
	c := filepath.Join(td, "c.bin")
	if err := os.WriteFile(a, []byte("same content for signature"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("different content entirely"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c, []byte("same content for signature"), 0644); err != nil {
		t.Fatal(err)
	}
	sigA, sigB, sigC := sourceSignature(a), sourceSignature(b), sourceSignature(c)
	if sigA == "" || sigB == "" || sigC == "" {
		t.Fatalf("empty signatures: %q %q %q", sigA, sigB, sigC)
	}
	if sigA == sigB {
		t.Fatal("different content produced the same signature")
	}
	if sigA != sigC {
		t.Fatal("identical content produced different signatures")
	}
	if sourceSignature(filepath.Join(td, "missing.bin")) != "" {
		t.Fatal("missing file must yield an empty signature")
	}
}

// Same-size files that differ only outside the old head/middle/tail samples
// must still produce different signatures — raw/uncompressed masters share
// size by construction for equal resolution+duration, so an unsampled-region
// edit would otherwise adopt a stale output as "same source".
func TestSourceSignatureDistinguishesUnsampledRegion(t *testing.T) {
	td := t.TempDir()
	data := make([]byte, 6<<20)
	for i := range data {
		data[i] = byte(i * 31)
	}
	a := filepath.Join(td, "a.bin")
	b := filepath.Join(td, "b.bin")
	if err := os.WriteFile(a, data, 0644); err != nil {
		t.Fatal(err)
	}
	// 1.5 MiB sits between the former head (0–1 MiB) and middle (2.5–3.5 MiB)
	// sample windows; the tail sample is untouched as well.
	data[1<<20+512<<10] ^= 0xFF
	if err := os.WriteFile(b, data, 0644); err != nil {
		t.Fatal(err)
	}
	if sourceSignature(a) == sourceSignature(b) {
		t.Fatal("edit inside the unsampled region produced a matching signature")
	}
}

func TestProvenanceMatchesRequiresBothHalves(t *testing.T) {
	opts := ConvertOptions{Preset: "prores_lt", StripAudio: true}
	src := "0123456789abcdef"
	tag := provenanceComment(src, jobSignature(opts))
	if !provenanceMatches(src, jobSignature(opts), MediaInfo{ProvenanceTag: tag}) {
		t.Fatal("matching tag not accepted")
	}
	if provenanceMatches(src, jobSignature(opts), MediaInfo{ProvenanceTag: provenanceComment("ffffffff", jobSignature(opts))}) {
		t.Fatal("foreign source tag adopted")
	}
	if provenanceMatches(src, jobSignature(ConvertOptions{Preset: "prores_lt"}), MediaInfo{ProvenanceTag: tag}) {
		t.Fatal("different job options adopted the same output")
	}
	if provenanceMatches(src, jobSignature(opts), MediaInfo{ProvenanceTag: "editorial notes"}) {
		t.Fatal("arbitrary comment accepted as provenance")
	}
	if provenanceMatches("", jobSignature(opts), MediaInfo{ProvenanceTag: ""}) {
		t.Fatal("missing source signature must never adopt")
	}
}

// Two different videos that share a stem, container-level metadata, and an
// output directory must never adopt each other's output — dims/rate/count
// prove nothing about content.
func TestProcessItemRejectsCrossSourceCandidate(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["libxvid"] || !enc["ffv1"] {
		t.Skip("libxvid/ffv1 unavailable")
	}
	td := t.TempDir()
	dirA, dirB, outDir := filepath.Join(td, "a"), filepath.Join(td, "b"), filepath.Join(td, "out")
	for _, d := range []string{dirA, dirB, outDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	srcA := filepath.Join(dirA, "clip.avi")
	srcB := filepath.Join(dirB, "clip.avi")
	for i, src := range []string{srcA, srcB} {
		lavfi := "testsrc2=size=160x90:rate=30"
		if i == 1 {
			lavfi = "mandelbrot=size=160x90:rate=30"
		}
		if b, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", lavfi, "-frames:v", "30", "-c:v", "ffv1", src).CombinedOutput(); err != nil {
			t.Fatalf("fixture %d: %v %s", i, err, b)
		}
	}
	e := &Engine{caps: caps, enc: enc}
	opts := ConvertOptions{Preset: "xvid_compact", OutputDir: outDir, StripAudio: true}

	infoA, err := probeMedia(caps.FFprobe, srcA, false)
	if err != nil {
		t.Fatal(err)
	}
	repA, _ := captureReporter()
	itemA := processItem(context.Background(), theme{}, e, infoA, opts, repA)
	if itemA.Status != "ok" {
		t.Fatalf("first conversion failed: %q %q", itemA.Status, itemA.Message)
	}

	infoB, err := probeMedia(caps.FFprobe, srcB, false)
	if err != nil {
		t.Fatal(err)
	}
	repB, linesB := captureReporter()
	itemB := processItem(context.Background(), theme{}, e, infoB, opts, repB)
	if itemB.Status != "ok" {
		t.Fatalf("second conversion failed: %q %q", itemB.Status, itemB.Message)
	}
	if itemB.Output == itemA.Output {
		t.Fatal("different source adopted the first source's output")
	}
	if !strings.HasSuffix(filepath.Base(itemB.Output), "_2.avi") {
		t.Fatalf("expected a numbered output, got %q", itemB.Output)
	}
	if !strings.Contains(strings.Join(*linesB, "\n"), "provenance") {
		t.Fatalf("rejection reason did not name provenance: %v", *linesB)
	}
}

// A reuse candidate whose audio stream decodes corrupt must not be adopted
// — the CHECK pass now decodes retained audio, not only video.
func TestProcessItemRejectsCorruptAudioCandidate(t *testing.T) {
	caps, enc, err := detectCapabilities()
	if err != nil {
		t.Skip(err)
	}
	if !enc["prores_ks"] || !enc["ffv1"] {
		t.Skip("prores_ks/ffv1 unavailable")
	}
	td := t.TempDir()
	src := filepath.Join(td, "tone.mkv")
	if b, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100",
		"-frames:v", "45", "-shortest", "-c:v", "ffv1", "-c:a", "aac", src).CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, b)
	}
	e := &Engine{caps: caps, enc: enc}
	outDir := filepath.Join(td, "out")
	opts := ConvertOptions{Preset: "prores_lt", OutputDir: outDir}

	info, err := probeMedia(caps.FFprobe, src, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Audio) == 0 {
		t.Fatal("fixture has no audio stream")
	}
	rep1, _ := captureReporter()
	item1 := processItem(context.Background(), theme{}, e, info, opts, rep1)
	if item1.Status != "ok" {
		t.Fatalf("initial conversion failed: %q %q", item1.Status, item1.Message)
	}

	// Corrupt only the AAC payload; the container and video stay intact, and
	// the provenance tag rides along with -c copy of global metadata.
	bad := filepath.Join(td, "bad.mov")
	if b, err := exec.Command(caps.FFmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-i", item1.Output, "-map", "0:v", "-c:v", "copy", "-map", "0:a", "-c:a", "copy",
		"-bsf:a", "noise=amount=4000:dropamount=0", bad).CombinedOutput(); err != nil {
		t.Skipf("cannot build corrupt-audio fixture: %v %s", err, b)
	}
	if err := os.Rename(bad, item1.Output); err != nil {
		t.Fatal(err)
	}

	rep2, lines2 := captureReporter()
	item2 := processItem(context.Background(), theme{}, e, info, opts, rep2)
	if item2.Output == item1.Output {
		t.Fatal("corrupt-audio candidate was adopted")
	}
	if !strings.Contains(strings.Join(*lines2, "\n"), "decode check failed") {
		t.Fatalf("corrupt audio was not rejected by the decode check: %v", *lines2)
	}
}
