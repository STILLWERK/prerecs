package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

var supportedExt = map[string]bool{
	".avi": true, ".mp4": true, ".m4v": true, ".mov": true, ".mkv": true,
	".wmv": true, ".webm": true, ".mpg": true, ".mpeg": true, ".m2ts": true, ".ts": true,
}

func gatherInputs(args []string, ui theme) ([]string, error) {
	if len(args) > 0 {
		return expandInputs(args)
	}
	fmt.Println(ui.bold("INPUT"))
	fmt.Println("  Drag files onto PreRecs.exe, or paste file/folder paths here.")
	fmt.Println("  Multiple quoted paths are supported.")
	fmt.Print("\n  Path(s) > ")
	line, err := stdinReader.ReadString('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	return expandInputs(parsePathInput(line))
}

func parsePathInput(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	if _, err := os.Stat(trimOuterQuotes(line)); err == nil {
		return []string{trimOuterQuotes(line)}
	}
	out := []string{}
	var b strings.Builder
	quoted := false
	flush := func() {
		s := strings.TrimSpace(b.String())
		b.Reset()
		if s != "" {
			out = append(out, trimOuterQuotes(s))
		}
	}
	for _, ch := range line {
		// Only `"` acts as a quoting character: it is illegal inside filenames
		// on both Windows and POSIX, while `'` and `;` are real, legal name
		// characters (it's.avi, semi;colon.avi) — treating them as syntax
		// silently converts the wrong file or splits a single name in two.
		if ch == '"' {
			quoted = !quoted
			continue
		}
		if !quoted && ch == '\t' {
			flush()
			continue
		}
		if !quoted && ch == ' ' {
			if b.Len() > 0 {
				flush()
			}
			continue
		}
		b.WriteRune(ch)
	}
	flush()
	return out
}

func trimOuterQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

func expandInputs(items []string) ([]string, error) {
	seen := map[string][]string{}
	out := []string{}
	for _, raw := range items {
		p := trimOuterQuotes(strings.TrimSpace(raw))
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if st.IsDir() {
			entries, err := os.ReadDir(p)
			if err != nil {
				return nil, err
			}
			names := make([]string, 0)
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				// Skip files that look like this tool's own generated outputs
				// (`stem_<preset>.ext`, `stem_<preset>_N.ext`) so a later run
				// against the same folder does not re-ingest its own results.
				// The native-Xvid raw stream (`*.video.tmp.m4v`) can survive an
				// interrupted run and ends in a supported extension — never
				// ingest it as a source either.
				if strings.HasSuffix(strings.ToLower(e.Name()), ".video.tmp.m4v") {
					continue
				}
				if supportedExt[strings.ToLower(filepath.Ext(e.Name()))] && !isGeneratedOutputName(e.Name()) {
					names = append(names, filepath.Join(p, e.Name()))
				}
			}
			sort.Strings(names)
			for _, n := range names {
				out = appendUniqueInput(out, seen, absOrSelf(n))
			}
			continue
		}
		out = appendUniqueInput(out, seen, absOrSelf(p))
	}
	return out, nil
}

// absOrSelf keeps the caller's spelling when filepath.Abs cannot resolve —
// an ignored error would otherwise collapse distinct failures into "".
func absOrSelf(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

// appendUniqueInput adds p unless a kept entry already refers to the same
// filesystem object. The dedupe key alone is not decisive on Windows: NTFS
// directories can opt into case sensitivity, where "MIXED.AVI" and
// "mixed.avi" are distinct files that fold to one key.
func appendUniqueInput(out []string, seen map[string][]string, p string) []string {
	key := inputDedupeKey(p)
	for _, prev := range seen[key] {
		if sameInputFile(prev, p) {
			return out
		}
	}
	seen[key] = append(seen[key], p)
	return append(out, p)
}

// sameInputFile confirms a key collision through the filesystem: identical
// spellings, or paths that stat to the same object, are duplicates.
func sameInputFile(a, b string) bool {
	if a == b {
		return true
	}
	sa, ea := os.Stat(a)
	sb, eb := os.Stat(b)
	return ea == nil && eb == nil && os.SameFile(sa, sb)
}

// inputDedupeKey matches the output-stem convention: NTFS is case-insensitive,
// so differently-cased spellings of the same file must dedupe on Windows or
// the same source would be enqueued twice and collide on output slots.
func inputDedupeKey(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// generatedOutputSuffixes are the canonical preset tokens PreRecs appends to
// output filenames: `stem_<preset>.ext` and `stem_<preset>_N.ext`. Each token
// pairs with the only extension its preset actually emits — .avi for
// Xvid/lossless presets, .mov for ProRes — so a user file whose name merely
// ends in the token under the other extension stays eligible.
var generatedOutputSuffixes = []struct {
	token string
	ext   string
}{
	{"xvid_compact", ".avi"}, {"xvid_max_q2", ".avi"}, {"xvid_efficient_q2", ".avi"},
	{"xvid_small", ".avi"}, {"xvid_max", ".avi"},
	{"prores_lt", ".mov"}, {"prores_422", ".mov"}, {"prores_hq", ".mov"}, {"prores_4444", ".mov"},
	{"magicyuv_lossless", ".avi"}, {"utvideo_lossless", ".avi"},
}

// isGeneratedOutputName reports whether a filename matches the exact naming
// convention of a PreRecs output. It only triggers on a trailing
// `_<preset>` or `_<preset>_<digits>` before the extension that preset emits —
// the precise names this program itself produces — so arbitrary user media
// with a vaguely similar substring stays eligible. A user file that happens
// to share the exact generated pattern is indistinguishable from real output
// and is skipped only inside directory scans; it can still be passed
// explicitly.
func isGeneratedOutputName(name string) bool {
	// Case-fold the whole name: Windows filesystems are case-insensitive, and
	// generated names always carry lowercase preset tokens.
	low := strings.ToLower(name)
	ext := filepath.Ext(low)
	stem := low[:len(low)-len(ext)]
	for _, p := range generatedOutputSuffixes {
		if ext != p.ext {
			continue
		}
		if strings.HasSuffix(stem, "_"+p.token) {
			return true
		}
		marker := "_" + p.token + "_"
		idx := strings.LastIndex(stem, marker)
		if idx >= 0 && isDigits(stem[idx+len(marker):]) {
			return true
		}
	}
	return false
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
