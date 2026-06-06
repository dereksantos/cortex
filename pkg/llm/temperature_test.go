package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvTemperature(t *testing.T) {
	cases := []struct {
		name   string
		set    bool
		val    string
		wantOK bool
		want   float64
	}{
		{"unset", false, "", false, 0},
		{"zero", true, "0", true, 0},
		{"point7", true, "0.7", true, 0.7},
		{"empty", true, "", false, 0},
		{"garbage", true, "hot", false, 0},
		{"negative", true, "-1", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(TemperatureEnv, tc.val)
			} else {
				t.Setenv(TemperatureEnv, "")
			}
			got := envTemperature()
			if tc.wantOK {
				if got == nil {
					t.Fatalf("envTemperature() = nil, want %v", tc.want)
				}
				if *got != tc.want {
					t.Errorf("envTemperature() = %v, want %v", *got, tc.want)
				}
			} else if got != nil {
				t.Errorf("envTemperature() = %v, want nil", *got)
			}
		})
	}
}

// TestCompatRequestTemperatureOmitempty locks the wire contract: a nil
// temperature (today's default) MUST NOT serialize a "temperature" key,
// so the request bytes are identical to pre-change; a pinned temperature
// (including 0) MUST serialize it.
func TestCompatRequestTemperatureOmitempty(t *testing.T) {
	none := compatRequest{Model: "m", MaxTokens: 10}
	b, _ := json.Marshal(none)
	if strings.Contains(string(b), "temperature") {
		t.Errorf("nil temperature serialized a temperature key: %s", b)
	}

	zero := 0.0
	pinned := compatRequest{Model: "m", MaxTokens: 10, Temperature: &zero}
	b, _ = json.Marshal(pinned)
	if !strings.Contains(string(b), `"temperature":0`) {
		t.Errorf("pinned temperature=0 not serialized: %s", b)
	}
}

// TestClientTemperatureWiring confirms construction reads the env and
// SetTemperature overrides it.
func TestClientTemperatureWiring(t *testing.T) {
	t.Setenv(TemperatureEnv, "0")
	c := NewOpenAICompatClient(EndpointConfig{Name: "x", BaseURL: "http://localhost:1/v1"})
	if c.temperature == nil || *c.temperature != 0 {
		t.Fatalf("constructor did not read CORTEX_TEMPERATURE=0 (got %v)", c.temperature)
	}
	c.SetTemperature(0.5)
	if c.temperature == nil || *c.temperature != 0.5 {
		t.Errorf("SetTemperature(0.5) not applied (got %v)", c.temperature)
	}
}
