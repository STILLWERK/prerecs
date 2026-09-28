package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
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

// createdOutDirs records output directories this run made so a fully-failed
// batch can remove the empty folder it left behind — os.Remove on a non-empty
// dir fails harmlessly, so the sweep is safe even when a sibling run filled it.
var createdOutDirs sync.Map

func outputCandidates(src, custom, preset string) ([]string, string, error) {
	base := custom
	if base == "" {
		base = filepath.Join(filepath.Dir(src), "converted_prerecs")
	}
	_, statErr := os.Stat(base)
	if err := os.MkdirAll(base, 0755); err != nil {
		return nil, "", err
	}
	if errors.Is(statErr, os.ErrNotExist) {
		createdOutDirs.Store(filepath.Clean(base), struct{}{})
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

// maxOutputSlot bounds both reuse discovery and new reservations so a run can
// never inherit or create absurdly numbered outputs.
const maxOutputSlot = 999999

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
			if n < 2 || n > maxOutputSlot {
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
	for n := 1; n <= len(taken)+1 && n <= maxOutputSlot; n++ {
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

// removeEmptyCreatedDirs sweeps output directories this run created that are
// still empty — e.g. after a batch where every item failed. Errors (including
// the dir being non-empty or already gone) are deliberately ignored.
func removeEmptyCreatedDirs() {
	createdOutDirs.Range(func(k, _ any) bool {
		createdOutDirs.Delete(k)
		_ = os.Remove(k.(string))
		return true
	})
}

func releaseOutputReservation(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// checkReservedOutput re-validates the O_EXCL reservation immediately before a
// backend opens the path for writing. The reservation holds the *name*, but
// ffmpeg/xvid later reopen the path itself: in a shared output directory an
// attacker could swap the placeholder for a link and redirect the encode
// elsewhere, so a non-regular inode here fails the item instead of encoding.
// This is best-effort narrowing, not a closed window: the child still reopens
// the path after the check, and a hardlink swap survives Lstat entirely (a
// hardlink *is* a regular file) — OS hardlink protections cover that case.
func checkReservedOutput(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("reserved output: %w", err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("reserved output %s is no longer a regular file", filepath.Base(path))
	}
	return nil
}
