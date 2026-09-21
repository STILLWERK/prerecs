package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProbeProResVulkanTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake executable is not portable to Windows")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "ffmpeg")
	pidFile := filepath.Join(dir, "sleep.pid")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 60 &\necho $! > '"+pidFile+"'\nwait\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			return
		}
		if p, err := os.FindProcess(pid); err == nil {
			_ = p.Kill()
		}
	})
	old := vulkanProbeTimeout
	vulkanProbeTimeout = 300 * time.Millisecond
	defer func() { vulkanProbeTimeout = old }()
	start := time.Now()
	if probeProResVulkan(fake) {
		t.Fatal("hung probe reported Vulkan available")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("probe was not bounded by the timeout: %v", elapsed)
	}
}

func TestCapabilityCommandOutputTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake executable is not portable to Windows")
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "faketool")
	pidFile := filepath.Join(dir, "sleep.pid")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nsleep 60 &\necho $! > '"+pidFile+"'\nwait\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			return
		}
		if p, err := os.FindProcess(pid); err == nil {
			p.Kill()
		}
	})
	old := capabilityProbeTimeout
	capabilityProbeTimeout = 300 * time.Millisecond
	defer func() { capabilityProbeTimeout = old }()
	start := time.Now()
	_, err := capabilityCommandOutput(fake)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("command was not bounded by the timeout: %v", elapsed)
	}
}
