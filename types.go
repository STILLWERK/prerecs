package main

import "time"

type MediaInfo struct {
	Path        string
	Codec       string
	CodecTag    string
	Profile     string
	Width       int
	Height      int
	PixelFormat string
	BitDepth    int
	FPS         string
	FPSFloat    float64
	Duration    float64
	FrameCount  int64
	// FrameCountExact marks a frame count that can be trusted to gate
	// verification — either a decode-verified count or metadata from a codec
	// whose container tables are dependable in this workflow.
	FrameCountExact bool
	SizeBytes       int64
	ColorRange      string
	ColorSpace      string
	ColorTransfer   string
	ColorPrimaries  string
	HasAlpha        bool
	Chroma          string
	// Audio holds the codec name of each audio stream, in stream order.
	Audio []string
	// ProvenanceTag is the container's embedded provenance comment, if any.
	// On reuse candidates it is matched against the source's content
	// signature and the job signature; a missing or foreign tag disqualifies
	// adoption.
	ProvenanceTag string
	// TimestampsBroken marks video timestamps that were proven unusable by
	// the exact decode scan (duplicate/non-advancing PTS or no stats at
	// all). Healthy timing — including variable frame rate — is preserved
	// through passthrough instead of being flattened to constant rate.
	TimestampsBroken bool
	// ImpeachedFPS is the container's declared frame rate after consistency
	// checks cleared it — unusable for normal verification, but when packet
	// timestamps are proven destroyed it is the only surviving statement of
	// intended rate, so the timeline rebuild may fall back to it.
	ImpeachedFPS string
}

type Capabilities struct {
	FFmpeg          string
	FFprobe         string
	Version         string
	HasXvid         bool
	HasLibXvid      bool
	HasProRes       bool
	HasProResVulkan bool
	HasMagicYUV     bool
	HasUtVideo      bool
	HasNativeXvid   bool
	XvidEncRaw      string
	MagicInstalled  bool
	// HasVfrdet gates the timestamp-health observation on the decode scan: a
	// --disable-everything build without the filter would fail every compressed
	// input at SCAN, so without it broken-timing repair silently degrades to
	// passthrough rather than crashing.
	HasVfrdet bool
}

type ConvertOptions struct {
	Preset         string
	Conform        bool
	CaptureFPS     string
	Timescale      string
	StripAudio     bool
	OutputDir      string
	SkipCompressed bool
	ForceXvid      bool
	CPUProRes      bool
	// NoNativeXvid disables the xvid_encraw path even when it is installed,
	// so a machine whose native encoder misbehaves can still convert through
	// FFmpeg libxvid instead of failing every run.
	NoNativeXvid bool
}

type Engine struct {
	caps Capabilities
	enc  map[string]bool
}

type ItemResult struct {
	Source     string
	Output     string
	Status     string
	Backend    string
	Message    string
	InputInfo  MediaInfo
	OutputInfo MediaInfo
	Elapsed    time.Duration
}

type BatchResult struct {
	Successes     int
	Failures      int
	InputFailures int
	Skipped       int
	Items         []ItemResult
	LastOutputDir string
}
