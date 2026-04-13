// Package system provides system resource detection
package system

import (
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Info holds system information
type Info struct {
	OS             string
	Arch           string
	CPUCores       int
	TotalRAMGB     float64
	AvailableRAMGB float64
}

// Detect returns system information
func Detect() (*Info, error) {
	info := &Info{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		CPUCores: runtime.NumCPU(),
	}

	// Detect RAM based on OS
	switch runtime.GOOS {
	case "darwin":
		info.TotalRAMGB = detectRAMMacOS()
	case "linux":
		info.TotalRAMGB = detectRAMLinux()
	case "windows":
		info.TotalRAMGB = detectRAMWindows()
	default:
		info.TotalRAMGB = 8.0 // Default guess
	}

	// Available RAM is harder to detect reliably, estimate as 70% of total
	info.AvailableRAMGB = info.TotalRAMGB * 0.7

	return info, nil
}

func detectRAMMacOS() float64 {
	cmd := exec.Command("sysctl", "-n", "hw.memsize")
	output, err := cmd.Output()
	if err != nil {
		return 8.0 // Default
	}

	bytes, err := strconv.ParseInt(strings.TrimSpace(string(output)), 10, 64)
	if err != nil {
		return 8.0
	}

	return float64(bytes) / (1024 * 1024 * 1024) // Convert to GB
}

func detectRAMLinux() float64 {
	cmd := exec.Command("grep", "MemTotal", "/proc/meminfo")
	output, err := cmd.Output()
	if err != nil {
		return 8.0
	}

	// Parse "MemTotal:       16384000 kB"
	fields := strings.Fields(string(output))
	if len(fields) < 2 {
		return 8.0
	}

	kb, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 8.0
	}

	return kb / (1024 * 1024) // Convert KB to GB
}

func detectRAMWindows() float64 {
	cmd := exec.Command("wmic", "computersystem", "get", "totalphysicalmemory")
	output, err := cmd.Output()
	if err != nil {
		return 8.0
	}

	lines := strings.Split(string(output), "\n")
	if len(lines) < 2 {
		return 8.0
	}

	bytes, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil {
		return 8.0
	}

	return float64(bytes) / (1024 * 1024 * 1024)
}

// FormatOS returns a human-readable OS name
func (i *Info) FormatOS() string {
	osNames := map[string]string{
		"darwin":  "macOS",
		"linux":   "Linux",
		"windows": "Windows",
	}

	if name, ok := osNames[i.OS]; ok {
		return name
	}
	return i.OS
}

// String returns a formatted string representation
func (i *Info) String() string {
	return fmt.Sprintf("%s (%s) - %d cores, %.1f GB RAM",
		i.FormatOS(), i.Arch, i.CPUCores, i.TotalRAMGB)
}
