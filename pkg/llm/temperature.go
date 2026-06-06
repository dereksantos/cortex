package llm

import (
	"os"
	"strconv"
)

// TemperatureEnv pins the sampling temperature for every LLM request
// cortex issues. Unset (the default) sends NO temperature, preserving
// each backend's own server-side default — i.e. today's behavior. Set it
// to "0" for deterministic eval runs.
//
// The env var is the transport that survives the eval's subprocess
// boundary: `eval codebase` shells out to a fresh cortex per cell, so a
// flag on the outer process can't reach the inner harness — but an
// injected env var can. Clients read it at construction; in-process
// callers can override with SetTemperature.
const TemperatureEnv = "CORTEX_TEMPERATURE"

// envTemperature returns the temperature pinned via CORTEX_TEMPERATURE,
// or nil when unset or unparseable (the caller then omits temperature
// from the request). The pointer form lets request structs use
// `omitempty`, so an unset temperature is never serialized and the wire
// shape is byte-identical to today's.
func envTemperature() *float64 {
	v := os.Getenv(TemperatureEnv)
	if v == "" {
		return nil
	}
	t, err := strconv.ParseFloat(v, 64)
	if err != nil || t < 0 {
		return nil
	}
	return &t
}
