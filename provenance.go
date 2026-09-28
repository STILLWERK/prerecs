package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
)

// Output reuse is only safe when a candidate can prove it was produced from
// this exact source with these exact job settings — the stem+preset naming
// namespace is shared by same-named files from different folders and by
// different containers (clip.mp4 vs clip.mkv), which can carry identical
// dimensions/rate/count while holding completely different pictures.
//
// PreRecs embeds "prerecs1 src=<hash> job=<hash>" in the output comment tag
// (ICMT for AVI, ©cmt for MOV — the only provenance channel that round-trips
// both containers without extra mux flags). Candidates lacking the tag —
// foreign files, outputs from older versions, and unfinished mid-encode
// reservations — are never adopted.

const provenancePrefix = "prerecs1"

// sourceSignature fingerprints file content cheaply: size plus a head/middle/
// tail sample. Outputs embed it so reuse decisions verify provenance instead
// of hoping identical metadata implies identical content. For files above 3 MB
// the unhashed mid-section gaps are the known blind-spot bound — two same-size
// sources differing only there collide; the full decode + metadata checks
// still gate adoption. A read failure yields "" and the caller never adopts
// an output under that signature.
func sourceSignature(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	h := sha256.New()
	var sizeBuf [8]byte
	binary.LittleEndian.PutUint64(sizeBuf[:], uint64(st.Size()))
	h.Write(sizeBuf[:])
	const chunk = 1 << 20
	buf := make([]byte, chunk)
	for _, off := range []int64{0, st.Size()/2 - chunk/2, st.Size() - chunk} {
		if off < 0 {
			continue
		}
		// ReadAt returns io.EOF with a short read — files smaller than the
		// chunk, and every tail read, land here. Treating EOF as failure would
		// silently drop the sample and collapse the signature to size-only.
		if n, err := f.ReadAt(buf, off); n > 0 && (err == nil || errors.Is(err, io.EOF)) {
			h.Write(buf[:n])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// jobSignature captures every option that changes the encoded payload, so a
// conform output is never adopted for a normal job (or vice versa) even though
// both share the stem+preset filename slot. Timescale/CaptureFPS are hashed as
// canonical rationals so "0.1" and "1/10" reuse correctly. StripAudio folds
// into Conform (identical payload either way); CPUProRes and NoNativeXvid are
// hashed because they select different encoders — and for prores_4444 on
// 9–10-bit sources, a different output pixel format.
func jobSignature(opts ConvertOptions) string {
	h := sha256.New()
	canonical := func(s string) string {
		if r, err := parseRat(s); err == nil {
			return ratString(r)
		}
		return s
	}
	for _, s := range []string{opts.Preset, canonical(opts.Timescale), canonical(opts.CaptureFPS)} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	for _, b := range []bool{opts.Conform, opts.StripAudio || opts.Conform, opts.CPUProRes, opts.NoNativeXvid} {
		v := byte(0)
		if b {
			v = 1
		}
		h.Write([]byte{v})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func provenanceComment(srcSig, jobSig string) string {
	if srcSig == "" {
		return ""
	}
	return provenancePrefix + " src=" + srcSig + " job=" + jobSig
}

// provenanceArgs embeds the signature pair as an output-format comment. Both
// AVI (INFO/ICMT) and MOV (©cmt) round-trip the comment field on every FFmpeg
// build in the supported range. The source hash is computed at encode time,
// not from the earlier probe, so a source swapped mid-run can never carry a
// stale signature into its output tag.
func provenanceArgs(info MediaInfo, opts ConvertOptions) []string {
	c := provenanceComment(sourceSignature(info.Path), jobSignature(opts))
	if c == "" {
		return nil
	}
	return []string{"-metadata", "comment=" + c}
}

// provenanceMatches reports whether a reuse candidate was produced from this
// source under the current job options.
func provenanceMatches(srcSig, jobSig string, cand MediaInfo) bool {
	if srcSig == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(cand.ProvenanceTag), provenanceComment(srcSig, jobSig))
}
