package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

func hasDuplicateOutputStems(customDir string, infos []MediaInfo) bool {
	seen := map[string]bool{}
	for _, in := range infos {
		dir := customDir
		if dir == "" {
			dir = filepath.Join(filepath.Dir(in.Path), "converted_prerecs")
		}
		stem := strings.ToLower(strings.TrimSuffix(filepath.Base(in.Path), filepath.Ext(in.Path)))
		dirKey := filepath.Clean(dir)
		if runtime.GOOS == "windows" {
			// NTFS is case-insensitive: fold the directory so differently
			// spelled paths to the same folder still serialize together.
			dirKey = strings.ToLower(dirKey)
		}
		key := dirKey + "|" + stem
		if seen[key] {
			return true
		}
		seen[key] = true
	}
	return false
}

func outputCandidates(src, custom, preset string) ([]string, string, error) {
	base := custom
	if base == "" {
		base = filepath.Join(filepath.Dir(src), "converted_prerecs")
	}
	if err := os.MkdirAll(base, 0755); err != nil {
		return nil, "", err
	}
	ext := ".mov"
	if strings.HasPrefix(preset, "xvid") || preset == "magicyuv_lossless" || preset == "utvideo_lossless" {
		ext = ".avi"
	}
	stem := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
	// List the directory once so sparse numbered outputs are discovered for
	// reuse even when earlier slots are missing (e.g. only clip_p_2.avi exists).
	prefix := stem + "_" + preset
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, "", err
	}
	taken := map[int]bool{}
	type candidate struct {
		n    int
		path string
	}
	found := []candidate{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// NTFS is case-insensitive: on Windows match case-folded so a
		// differently-cased existing output is found for reuse instead of
		// being missed and duplicated under a numbered name.
		matchName, matchPrefix, matchExt := name, prefix, ext
		if runtime.GOOS == "windows" {
			matchName, matchPrefix, matchExt = strings.ToLower(name), strings.ToLower(prefix), strings.ToLower(ext)
		}
		rest := strings.TrimPrefix(matchName, matchPrefix)
		if rest == matchName {
			continue
		}
		n := 0
		if rest == matchExt {
			n = 1
		} else if strings.HasPrefix(rest, "_") && strings.HasSuffix(rest, matchExt) {
			mid := rest[1 : len(rest)-len(matchExt)]
			if !isDigits(mid) {
				continue
			}
			n = parseInt(mid)
			if n < 2 || n > 999999 {
				continue
			}
		} else {
			continue
		}
		if taken[n] {
			continue
		}
		taken[n] = true
		found = append(found, candidate{n: n, path: filepath.Join(base, name)})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].n < found[j].n })
	existing := make([]string, 0, len(found))
	for _, c := range found {
		existing = append(existing, c.path)
	}
	// Reserve the lowest free slot with O_EXCL so parallel runs stay atomic.
	for n := 1; n <= len(taken)+1 && n < 100000; n++ {
		if taken[n] {
			continue
		}
		name := prefix + ext
		if n > 1 {
			name = fmt.Sprintf("%s_%d%s", prefix, n, ext)
		}
		p := filepath.Join(base, name)
		reservation, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			if closeErr := reservation.Close(); closeErr != nil {
				_ = os.Remove(p)
				return existing, "", closeErr
			}
			return existing, p, nil
		}
		if errors.Is(err, os.ErrExist) {
			existing = append(existing, p)
			taken[n] = true
			continue
		}
		return existing, "", fmt.Errorf("reserve output %s: %w", p, err)
	}
	return existing, "", errors.New("could not choose unused output filename")
}

func releaseOutputReservation(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
