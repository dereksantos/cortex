package config

// WorkerPreset returns mode config for sprite/CI workers.
// Think in fast-only mode, no Dream or Digest. Capture and search enabled.
func WorkerPreset() *ModeConfig {
	return &ModeConfig{
		Think: &ThinkModeConfig{
			Enabled:   boolPtr(true),
			MaxBudget: intPtr(3),
			Mode:      "fast",
		},
		Dream:  &DreamModeConfig{Enabled: boolPtr(false)},
		Digest: &DigestModeConfig{Enabled: boolPtr(false)},
	}
}

// FullPreset returns mode config with everything enabled at default budgets.
// This is the default behavior when no modes config is specified.
func FullPreset() *ModeConfig {
	return &ModeConfig{
		Think: &ThinkModeConfig{Enabled: boolPtr(true)},
		Dream: &DreamModeConfig{Enabled: boolPtr(true)},
		Digest: &DigestModeConfig{Enabled: boolPtr(true)},
	}
}

// ObserverPreset returns mode config for read-only observation.
// Search only — no capture, no background processing.
func ObserverPreset() *ModeConfig {
	return &ModeConfig{
		Think:   &ThinkModeConfig{Enabled: boolPtr(false)},
		Dream:   &DreamModeConfig{Enabled: boolPtr(false)},
		Digest:  &DigestModeConfig{Enabled: boolPtr(false)},
		Capture: &CaptureModeConfig{Enabled: boolPtr(false)},
	}
}
