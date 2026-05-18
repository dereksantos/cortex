package ops

// readString returns the in[key] value coerced to string. Returns "" if
// absent or wrong type — callers decide whether absence is fatal.
func readString(in map[string]any, key string) string {
	if v, ok := in[key].(string); ok {
		return v
	}
	return ""
}

// readInt returns the in[key] value coerced to int. Accepts int /
// int64 / float64 (YAML decodes numbers as float64). Returns def if
// absent or wrong type.
func readInt(in map[string]any, key string, def int) int {
	if v, ok := in[key]; ok {
		switch x := v.(type) {
		case int:
			return x
		case int64:
			return int(x)
		case float64:
			return int(x)
		}
	}
	return def
}

// readFloat64 returns the in[key] value coerced to float64. Returns
// def if absent or wrong type.
func readFloat64(in map[string]any, key string, def float64) float64 {
	if v, ok := in[key]; ok {
		switch x := v.(type) {
		case float64:
			return x
		case int:
			return float64(x)
		case int64:
			return float64(x)
		}
	}
	return def
}

// readStringSlice returns the in[key] value coerced to []string.
// Accepts []string or []any (mixed types skipped). Returns (nil, false)
// if absent or wrong type.
func readStringSlice(in map[string]any, key string) ([]string, bool) {
	v, ok := in[key]
	if !ok {
		return nil, false
	}
	switch x := v.(type) {
	case []string:
		return x, true
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out, true
	}
	return nil, false
}

// readFloat32Slice returns the in[key] value coerced to []float32.
// Accepts []float32, []float64, or []any of numeric values. Returns
// (nil, false) if absent or wrong type.
func readFloat32Slice(in map[string]any, key string) ([]float32, bool) {
	v, ok := in[key]
	if !ok {
		return nil, false
	}
	switch x := v.(type) {
	case []float32:
		return x, true
	case []float64:
		out := make([]float32, len(x))
		for i, e := range x {
			out[i] = float32(e)
		}
		return out, true
	case []any:
		out := make([]float32, 0, len(x))
		for _, e := range x {
			switch n := e.(type) {
			case float64:
				out = append(out, float32(n))
			case float32:
				out = append(out, n)
			case int:
				out = append(out, float32(n))
			}
		}
		return out, true
	}
	return nil, false
}
