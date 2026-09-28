package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func appDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func fileExists(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }

func findTool(name string) string {
	exeName := name
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	envName := "PRERECS_" + strings.ToUpper(name)
	if v := os.Getenv(envName); v != "" && fileExists(v) {
		return v
	}
	base := appDir()
	for _, c := range []string{filepath.Join(base, "tools", exeName), filepath.Join(base, exeName)} {
		if fileExists(c) {
			return c
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

func findXvidEncRaw() string {
	if v := os.Getenv("PRERECS_XVID_ENCRAW"); v != "" && fileExists(v) {
		return v
	}
	exe := "xvid_encraw"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	base := appDir()
	candidates := []string{
		filepath.Join(base, "tools", exe),
		filepath.Join(base, exe),
	}
	if runtime.GOOS == "windows" {
		if pf86 := os.Getenv("ProgramFiles(x86)"); pf86 != "" {
			candidates = append(candidates, filepath.Join(pf86, "Xvid", "xvid_encraw.exe"))
		}
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			candidates = append(candidates, filepath.Join(pf, "Xvid", "xvid_encraw.exe"))
		}
		candidates = append(candidates, `C:\Program Files (x86)\Xvid\xvid_encraw.exe`, `C:\Program Files\Xvid\xvid_encraw.exe`)
	}
	for _, c := range candidates {
		if fileExists(c) {
			return c
		}
	}
	if p, err := exec.LookPath("xvid_encraw"); err == nil {
		return p
	}
	return ""
}

// The encode and decode-scan paths rely on -fps_mode, which first appeared in
// FFmpeg 5.1. Anything older fails per-command instead of here at startup.
const (
	ffmpegMinMajor = 5
	ffmpegMinMinor = 1
)

var ffmpegVersionPattern = regexp.MustCompile(`(?i)^ffmpeg version (?:n)?([0-9]+)\.([0-9]+)`)

// ffmpegVersionOK reports whether a `ffmpeg -version` headline meets the
// minimum version. It only answers for lines that carry a dotted release
// number; snapshot builds ("N-110521-g..." or "git-...") have none and return
// true so the caller can fall back to probing -fps_mode support directly.
func ffmpegVersionOK(line string) bool {
	m := ffmpegVersionPattern.FindStringSubmatch(line)
	if m == nil {
		return true
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major > ffmpegMinMajor || (major == ffmpegMinMajor && minor >= ffmpegMinMinor)
}

// ffmpegVersionLine finds the "ffmpeg version" headline in -version output.
// stderr noise merges into CombinedOutput, so a driver/font warning can
// precede it — taking the first line blindly would let garbage become both
// the reported version and the floor check's input.
func ffmpegVersionLine(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		if l := strings.TrimSpace(line); strings.HasPrefix(strings.ToLower(l), "ffmpeg version") {
			return l
		}
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")[0]
}

var capabilityProbeTimeout = 10 * time.Second

func capabilityCommandOutput(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), capabilityProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	// Same forgiveness probeMediaContext/runFFmpeg apply: a wrapper or shim
	// that exits 0 while a descendant holds the pipes open reports
	// ErrWaitDelay, which must not fail startup.
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0 {
		err = nil
	}
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, err
}

func detectCapabilities() (Capabilities, map[string]bool, error) {
	ffmpeg, ffprobe := findTool("ffmpeg"), findTool("ffprobe")
	if ffmpeg == "" || ffprobe == "" {
		return Capabilities{}, nil, errors.New("FFmpeg/ffprobe not found. Put them in a tools folder next to PreRecs.exe or add them to PATH")
	}
	out, err := capabilityCommandOutput(ffmpeg, "-hide_banner", "-version")
	if err != nil {
		return Capabilities{}, nil, err
	}
	versionLine := ffmpegVersionLine(out)
	if !ffmpegVersionOK(versionLine) {
		return Capabilities{}, nil, fmt.Errorf("%s — PreRecs requires FFmpeg 5.1 or newer", versionLine)
	}
	if ffmpegVersionPattern.FindStringSubmatch(versionLine) == nil {
		// Snapshot/git builds carry no dotted release number to compare — and
		// ancient snapshots predate 5.1 — so verify the option the floor
		// actually protects instead of trusting the unparseable line.
		help, err := capabilityCommandOutput(ffmpeg, "-hide_banner", "-h", "full")
		if err != nil {
			return Capabilities{}, nil, fmt.Errorf("cannot determine FFmpeg version (%s) and the -h probe failed: %w", versionLine, err)
		}
		if !bytes.Contains(help, []byte("fps_mode")) {
			return Capabilities{}, nil, fmt.Errorf("%s — PreRecs requires FFmpeg 5.1 or newer", versionLine)
		}
	}
	encOut, err := capabilityCommandOutput(ffmpeg, "-hide_banner", "-encoders")
	if err != nil {
		return Capabilities{}, nil, err
	}
	enc := parseEncoderNames(encOut)
	magic := detectMagicYUV()
	xvidRaw := findXvidEncRaw()
	hasNative := xvidRaw != ""
	hasVulkanProRes := enc["prores_ks_vulkan"] && probeProResVulkan(ffmpeg)
	// vfrdet ships in FFmpeg ≥4.1 and is never optional in practice, but a
	// stripped build without it must not hard-fail every compressed input at
	// the integrity scan — gate timing observation on its presence.
	filtersOut, _ := capabilityCommandOutput(ffmpeg, "-hide_banner", "-filters")
	hasVfrdet := bytes.Contains(filtersOut, []byte(" vfrdet "))
	return Capabilities{FFmpeg: ffmpeg, FFprobe: ffprobe, Version: versionLine, HasXvid: enc["libxvid"] || hasNative, HasLibXvid: enc["libxvid"], HasNativeXvid: hasNative, XvidEncRaw: xvidRaw, HasProRes: enc["prores_ks"], HasProResVulkan: hasVulkanProRes, HasMagicYUV: enc["magicyuv"], HasUtVideo: enc["utvideo"], MagicInstalled: magic, HasVfrdet: hasVfrdet}, enc, nil
}

// parseEncoderNames turns `ffmpeg -encoders` output into a name set. Encoder
// rows are `<flags> <name> <desc>` where flags is a fixed-width capability
// column — 6 chars today, but the width is not pinned so a wider flag field
// in a future FFmpeg cannot silently empty the map. Legend rows spell `=` as
// the name (`V..... = Video`), which would otherwise register a bogus encoder.
func parseEncoderNames(out []byte) map[string]bool {
	enc := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) >= 2 && f[1] != "=" && strings.ContainsAny(f[0], "VA") {
			enc[f[1]] = true
		}
	}
	return enc
}

// vulkanProbeTimeout bounds the startup capability probe. A wedged Vulkan
// driver could otherwise block program start forever; a timed-out probe simply
// means "no GPU fast path", and CPU prores_ks remains available.
var vulkanProbeTimeout = 10 * time.Second

func probeProResVulkan(ffmpeg string) bool {
	// The encoder can be compiled into FFmpeg even when the installed driver
	// cannot create a Vulkan device. A single 16x16 frame catches that at startup
	// in about half a second on the reference PC, avoiding repeated failed GPU
	// attempts later in a batch.
	ctx, cancel := context.WithTimeout(context.Background(), vulkanProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-init_hw_device", "vulkan=prerecs_probe", "-filter_hw_device", "prerecs_probe",
		"-f", "lavfi", "-i", "color=size=16x16:rate=1",
		"-frames:v", "1", "-vf", "format=yuv422p10le,hwupload",
		"-c:v", "prores_ks_vulkan", "-profile:v", "1", "-async_depth", "1",
		"-f", "null", "-",
	)
	return cmd.Run() == nil
}

func detectMagicYUV() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	keys := []string{`HKLM\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Drivers32`, `HKLM\SOFTWARE\WOW6432Node\Microsoft\Windows NT\CurrentVersion\Drivers32`}
	for _, k := range keys {
		out, _ := capabilityCommandOutput("reg", "query", k)
		low := strings.ToLower(string(out))
		if strings.Contains(low, "magic") || strings.Contains(low, "vidc.m8") || strings.Contains(low, "vidc.m0") || strings.Contains(low, "vidc.magy") {
			return true
		}
	}
	return false
}
