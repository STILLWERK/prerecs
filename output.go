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
	prefix := stem + "_" + preset
	existing, taken, err := scanOutputSlots(base, prefix, ext)
	if err != nil {
		return nil, "", err
	}
	reserved, err := reserveOutputSlot(base, prefix, ext, taken)
	if err != nil {
		return existing, "", err
	}
	return existing, reserved, nil
}

// scanOutputSlots lists base once and returns the pre-existing output paths
// matching prefix/ext, sorted by slot number, plus the set of claimed slots.
// Sparse numbered outputs are discovered for reuse even when earlier slots
// are missing (e.g. only clip_p_2.avi exists).
func scanOutputSlots(base, prefix, ext string) ([]string, map[int]bool, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, nil, err
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
	return existing, taken, nil
}

// reserveOutputSlot claims the lowest free slot with O_EXCL so parallel runs
// stay atomic. A slot that reports os.ErrExist was claimed by another run
// between the scan and this reservation: that path is the winner's active
// reservation and its contents are still in flux, so it stays out of the
// reuse list — the reuse check could otherwise verify a file the owner later
// deletes on its own verification failure.
func reserveOutputSlot(base, prefix, ext string, taken map[int]bool) (string, error) {
	for n := 1; n <= len(taken)+1 && n < 100000; n++ {
		if taken[n] {
			continue
		}
		name := prefix + ext
		if n > 1 {
			name = fmt.Sprintf("%s_%d%s", prefix, n, ext)
		}
		p := filepath.Join(base, name)
		// The reservation inode becomes the final output file (ffmpeg -y
		// truncates it in place), so keep it owner-only: converted media
		// should not become world-readable just because it was produced.
		reservation, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			if closeErr := reservation.Close(); closeErr != nil {
				_ = os.Remove(p)
				return "", closeErr
			}
			return p, nil
		}
		if errors.Is(err, os.ErrExist) {
			// A concurrent run claimed this slot between the directory scan
			// and the reservation.
			taken[n] = true
			continue
		}
		return "", fmt.Errorf("reserve output %s: %w", p, err)
	}
	return "", errors.New("could not choose unused output filename")
}

func releaseOutputReservation(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
