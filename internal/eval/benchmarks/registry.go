package benchmarks

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrUnknownBenchmark is returned by Get when name is not registered.
var ErrUnknownBenchmark = errors.New("unknown benchmark")

var (
	regMu sync.RWMutex
	reg   = map[string]func() Benchmark{}
)

// Register installs a benchmark constructor under name. Per-benchmark
// packages call this from init() so importing the package side-effects
// the registration.
//
// Register panics on duplicate registration to surface programming errors
// at process start rather than at first --benchmark invocation.
func Register(name string, ctor func() Benchmark) {
	if name == "" {
		panic("benchmarks: Register with empty name")
	}
	if ctor == nil {
		panic("benchmarks: Register with nil constructor for " + name)
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := reg[name]; dup {
		panic("benchmarks: duplicate registration: " + name)
	}
	reg[name] = ctor
}

// Get returns a fresh Benchmark instance for the given name. The error
// wraps ErrUnknownBenchmark and lists the registered names so the CLI
// can surface a clean message.
func Get(name string) (Benchmark, error) {
	regMu.RLock()
	ctor, ok := reg[name]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q (registered: %v)", ErrUnknownBenchmark, name, Registered())
	}
	return ctor(), nil
}

// Registered returns the sorted list of benchmark names. Useful for
// CLI help text and error messages.
func Registered() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// resetRegistryForTest clears the registry. Test-only helper; not
// exported because callers outside _test.go should never reset a
// process-global registry.
func resetRegistryForTest() {
	regMu.Lock()
	defer regMu.Unlock()
	reg = map[string]func() Benchmark{}
}
