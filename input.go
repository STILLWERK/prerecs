package main

import (
	"fmt"
	"os"
	"path/filepath"
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
	quote := rune(0)
	flush := func() {
		s := strings.TrimSpace(b.String())
		b.Reset()
		if s != "" {
			out = append(out, trimOuterQuotes(s))
		}
	}
	for _, ch := range line {
		if ch == '"' || ch == '\'' {
			if !quoted {
				quoted = true
				quote = ch
				continue
			}
			if quote == ch {
				quoted = false
				continue
			}
		}
		if !quoted && (ch == ';' || ch == '\t') {
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
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

func expandInputs(items []string) ([]string, error) {
	seen := map[string]bool{}
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
				a, _ := filepath.Abs(n)
				if !seen[a] {
					seen[a] = true
					out = append(out, a)
				}
			}
			continue
		}
		a, _ := filepath.Abs(p)
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out, nil
}

// generatedOutputSuffixes are the canonical preset tokens PreRecs appends to
// output filenames: `stem_<preset>.ext` and `stem_<preset>_N.ext`.
var generatedOutputSuffixes = []string{
	"xvid_compact", "xvid_max_q2", "xvid_efficient_q2", "xvid_small", "xvid_max",
	"prores_lt", "prores_422", "prores_hq", "prores_4444",
	"magicyuv_lossless", "utvideo_lossless",
}

// isGeneratedOutputName reports whether a filename matches the exact naming
// convention of a PreRecs output. It only triggers on a trailing
// `_<preset>` or `_<preset>_<digits>` before an .avi/.mov extension — the
// precise names this program itself produces — so arbitrary user media with a
// vaguely similar substring stays eligible. A user file that happens to share
// the exact generated pattern is indistinguishable from real output and is
// skipped only inside directory scans; it can still be passed explicitly.
func isGeneratedOutputName(name string) bool {
	// Case-fold the whole name: Windows filesystems are case-insensitive, and
	// generated names always carry lowercase preset tokens.
	low := strings.ToLower(name)
	ext := filepath.Ext(low)
	if ext != ".avi" && ext != ".mov" {
		return false
	}
	stem := low[:len(low)-len(ext)]
	for _, p := range generatedOutputSuffixes {
		if strings.HasSuffix(stem, "_"+p) {
			return true
		}
		marker := "_" + p + "_"
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
