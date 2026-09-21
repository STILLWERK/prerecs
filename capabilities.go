package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

var capabilityProbeTimeout = 10 * time.Second

func capabilityCommandOutput(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), capabilityProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
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
	versionLine := strings.Split(strings.TrimSpace(string(out)), "\n")[0]
	encOut, err := capabilityCommandOutput(ffmpeg, "-hide_banner", "-encoders")
	if err != nil {
		return Capabilities{}, nil, err
	}
	enc := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(encOut)))
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) >= 2 && len(f[0]) == 6 && (strings.Contains(f[0], "V") || strings.Contains(f[0], "A")) {
			enc[f[1]] = true
		}
	}
	magic := detectMagicYUV()
	xvidRaw := findXvidEncRaw()
	hasNative := xvidRaw != ""
	hasVulkanProRes := enc["prores_ks_vulkan"] && probeProResVulkan(ffmpeg)
	return Capabilities{FFmpeg: ffmpeg, FFprobe: ffprobe, Version: versionLine, HasXvid: enc["libxvid"] || hasNative, HasLibXvid: enc["libxvid"], HasNativeXvid: hasNative, XvidEncRaw: xvidRaw, HasProRes: enc["prores_ks"], HasProResVulkan: hasVulkanProRes, HasMagicYUV: enc["magicyuv"], HasUtVideo: enc["utvideo"], MagicInstalled: magic}, enc, nil
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
