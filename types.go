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
