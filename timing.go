package main

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// ratStringSane bounds the strings handed to big.Rat.SetString: the parser
// honours decimal exponents, so a short string like 1e999999999 would
// materialize a ~10^9-digit numerator in memory. Whitelisting the charset
// also rejects exotic forms (hex floats with 'p' exponents) that carry the
// same hazard. Rates, timescales and durations never need more than this.
func ratStringSane(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c == '/' || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-') {
			return false
		}
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		exp, err := strconv.Atoi(s[i+1:])
		if err != nil || exp > 1000 || exp < -1000 {
			return false
		}
	}
	return true
}

func parseRat(v string) (*big.Rat, error) {
	s := strings.TrimSpace(v)
	if !ratStringSane(s) {
		return nil, fmt.Errorf("invalid positive number/fraction %q", v)
	}
	r := new(big.Rat)
	if _, ok := r.SetString(s); !ok || r.Sign() <= 0 {
		return nil, fmt.Errorf("invalid positive number/fraction %q", v)
	}
	return r, nil
}

// validRate reports whether v parses to a positive rational frame rate.
func validRate(v string) bool {
	r, err := parseRatAllowZero(v)
	return err == nil && r != nil
}

func parseRatAllowZero(v string) (*big.Rat, error) {
	if v == "" || v == "0/0" || v == "N/A" {
		return nil, nil
	}
	if !ratStringSane(v) {
		return nil, errors.New("bad rational")
	}
	r := new(big.Rat)
	if _, ok := r.SetString(v); !ok || r.Sign() <= 0 {
		return nil, errors.New("bad rational")
	}
	return r, nil
}

func ratFloat(r *big.Rat) float64 { f, _ := r.Float64(); return f }

func ratString(r *big.Rat) string { return r.Num().String() + "/" + r.Denom().String() }

func parseFloat(v string) float64 { f, _ := strconv.ParseFloat(v, 64); return f }

func parseInt64(v string) int64 { n, _ := strconv.ParseInt(v, 10, 64); return n }

func parseInt(v string) int { n, _ := strconv.Atoi(v); return n }

// selectFrameRate picks the best available container frame rate. avg_frame_rate
// is preferred because it reflects the realised stream; r_frame_rate is a
// nominal/guessed fallback that can diverge from the true average on VFR
// material. Either field can carry a stale header value, so when a trusted
// frame count and duration exist, each candidate must agree with them: a rate
// that disagrees is impeached rather than allowed to drive the constant-rate
// timeline rebuild (which would silently retime content while verification
// passes against the same wrong value). Rates are trusted unconditionally only
// when no corroborating metadata exists at all (e.g. raw elementary streams).
// The exact decode scan still governs frame counts and output verification.
func selectFrameRate(avg, r string, frames int64, dur float64) (string, *big.Rat) {
	for _, cand := range []string{avg, r} {
		v, err := parseRatAllowZero(cand)
		if err != nil || v == nil {
			continue
		}
		if frames > 0 && dur > 0 {
			// Frame-quantum duration tolerance: permits container/timestamp
			// rounding (~2.5 frames) but does not let a rate disagreement
			// scale with clip length — a relative % would let a long clip
			// drift by minutes, silently retiming it.
			fps := ratFloat(v)
			if math.Abs(float64(frames)/fps-dur) > math.Max(0.01, 2.5/fps) {
				continue
			}
		}
		return cand, v
	}
	return "", nil
}

func expectedTiming(info MediaInfo, req ConvertOptions) (*big.Rat, *big.Rat, float64, error) {
	sourceFPS, err := parseRatAllowZero(info.FPS)
	if err != nil {
		return nil, nil, 0, err
	}
	target := sourceFPS
	expectedDur := info.Duration
	if req.Conform {
		if req.CaptureFPS != "" {
			sourceFPS, err = parseRat(req.CaptureFPS)
			if err != nil {
				return nil, nil, 0, err
			}
		}
		if sourceFPS == nil {
			return nil, nil, 0, errors.New("capture FPS is required because source FPS could not be determined")
		}
		scale, err := parseRat(req.Timescale)
		if err != nil {
			return nil, nil, 0, err
		}
		target = new(big.Rat).Quo(sourceFPS, scale)
		if ratFloat(target) > 2000 {
			return nil, nil, 0, fmt.Errorf("target FPS %.3f is unreasonably high", ratFloat(target))
		}
		if info.FrameCount > 0 {
			expectedDur = float64(info.FrameCount) / ratFloat(target)
		} else if info.Duration > 0 {
			expectedDur = info.Duration * ratFloat(sourceFPS) / ratFloat(target)
		}
	}
	return sourceFPS, target, expectedDur, nil
}

func conformVideoFilters(target *big.Rat) []string {
	if target == nil || target.Sign() <= 0 {
		return nil
	}
	// Give every captured frame exactly one integer timestamp in the target
	// frame-rate timebase, then let fps normalize frame duration. This avoids
	// the rounding bug caused by setpts=N/(fps*TB) when TB is still the source
	// timebase (e.g. 1/30 while conforming to 300 fps).
	tb := target.Denom().String() + "/" + target.Num().String()
	fps := ratString(target)
	return []string{
		"settb=expr=" + tb,
		"setpts=N",
		"fps=" + fps,
	}
}
