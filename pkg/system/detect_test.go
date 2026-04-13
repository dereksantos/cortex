package system

import (
	"runtime"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	info, err := Detect()
	if err != nil {
		t.Fatalf("Detect() returned error: %v", err)
	}

	if info == nil {
		t.Fatal("Detect() returned nil")
	}

	t.Run("OS matches runtime", func(t *testing.T) {
		if info.OS != runtime.GOOS {
			t.Errorf("expected OS %q, got %q", runtime.GOOS, info.OS)
		}
	})

	t.Run("Arch matches runtime", func(t *testing.T) {
		if info.Arch != runtime.GOARCH {
			t.Errorf("expected Arch %q, got %q", runtime.GOARCH, info.Arch)
		}
	})

	t.Run("CPUCores is positive", func(t *testing.T) {
		if info.CPUCores < 1 {
			t.Errorf("expected CPUCores >= 1, got %d", info.CPUCores)
		}
	})

	t.Run("CPUCores matches runtime", func(t *testing.T) {
		if info.CPUCores != runtime.NumCPU() {
			t.Errorf("expected CPUCores %d, got %d", runtime.NumCPU(), info.CPUCores)
		}
	})

	t.Run("TotalRAMGB is positive", func(t *testing.T) {
		if info.TotalRAMGB <= 0 {
			t.Errorf("expected TotalRAMGB > 0, got %f", info.TotalRAMGB)
		}
	})

	t.Run("AvailableRAMGB is 70% of Total", func(t *testing.T) {
		expected := info.TotalRAMGB * 0.7
		if info.AvailableRAMGB != expected {
			t.Errorf("expected AvailableRAMGB %f, got %f", expected, info.AvailableRAMGB)
		}
	})

	t.Run("AvailableRAMGB is less than or equal to TotalRAMGB", func(t *testing.T) {
		if info.AvailableRAMGB > info.TotalRAMGB {
			t.Errorf("AvailableRAMGB (%f) should not exceed TotalRAMGB (%f)",
				info.AvailableRAMGB, info.TotalRAMGB)
		}
	})
}

func TestInfo_FormatOS(t *testing.T) {
	tests := []struct {
		os       string
		expected string
	}{
		{"darwin", "macOS"},
		{"linux", "Linux"},
		{"windows", "Windows"},
		{"freebsd", "freebsd"},
		{"openbsd", "openbsd"},
		{"", ""},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.os, func(t *testing.T) {
			info := &Info{OS: tt.os}
			result := info.FormatOS()
			if result != tt.expected {
				t.Errorf("FormatOS() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestInfo_String(t *testing.T) {
	tests := []struct {
		name     string
		info     Info
		contains []string
	}{
		{
			name: "macOS info",
			info: Info{
				OS:         "darwin",
				Arch:       "arm64",
				CPUCores:   8,
				TotalRAMGB: 16.0,
			},
			contains: []string{"macOS", "arm64", "8 cores", "16.0 GB RAM"},
		},
		{
			name: "Linux info",
			info: Info{
				OS:         "linux",
				Arch:       "amd64",
				CPUCores:   4,
				TotalRAMGB: 32.5,
			},
			contains: []string{"Linux", "amd64", "4 cores", "32.5 GB RAM"},
		},
		{
			name: "Windows info",
			info: Info{
				OS:         "windows",
				Arch:       "amd64",
				CPUCores:   16,
				TotalRAMGB: 64.0,
			},
			contains: []string{"Windows", "amd64", "16 cores", "64.0 GB RAM"},
		},
		{
			name: "single core",
			info: Info{
				OS:         "linux",
				Arch:       "386",
				CPUCores:   1,
				TotalRAMGB: 1.0,
			},
			contains: []string{"1 cores", "1.0 GB RAM"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.info.String()
			for _, substr := range tt.contains {
				if !strings.Contains(result, substr) {
					t.Errorf("String() = %q, want to contain %q", result, substr)
				}
			}
		})
	}
}

func TestInfo_String_Format(t *testing.T) {
	info := Info{
		OS:         "darwin",
		Arch:       "arm64",
		CPUCores:   10,
		TotalRAMGB: 32.0,
	}

	result := info.String()
	expected := "macOS (arm64) - 10 cores, 32.0 GB RAM"

	if result != expected {
		t.Errorf("String() = %q, want %q", result, expected)
	}
}

func TestDetect_RealValues(t *testing.T) {
	// This test verifies that Detect returns reasonable values on the actual system
	info, err := Detect()
	if err != nil {
		t.Fatalf("Detect() error: %v", err)
	}

	// RAM should be at least 1GB on any modern system
	if info.TotalRAMGB < 1.0 {
		t.Errorf("TotalRAMGB seems too low: %f", info.TotalRAMGB)
	}

	// RAM should be less than 10TB (sanity check)
	if info.TotalRAMGB > 10000 {
		t.Errorf("TotalRAMGB seems too high: %f", info.TotalRAMGB)
	}

	// The formatted string should be non-empty
	str := info.String()
	if str == "" {
		t.Error("String() returned empty string")
	}

	// FormatOS should return something
	formatted := info.FormatOS()
	if formatted == "" {
		t.Error("FormatOS() returned empty string")
	}
}

func TestDetectRAMFunctions_Coverage(t *testing.T) {
	// These tests exist to cover the detectRAM* functions.
	// We can't easily test them in isolation without mocking exec.Command,
	// but we can verify they return reasonable default values.

	// The Detect function will call the appropriate detectRAM* function
	// based on runtime.GOOS, which gives us coverage of at least one path.

	info, _ := Detect()

	// On any system, RAM should either be detected or fall back to 8.0
	if info.TotalRAMGB < 1.0 {
		// If detection failed, it should be the default 8.0
		if info.TotalRAMGB != 8.0 {
			t.Errorf("expected TotalRAMGB to be detected or 8.0 default, got %f", info.TotalRAMGB)
		}
	}
}
