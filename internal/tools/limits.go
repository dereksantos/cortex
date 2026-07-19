package tools

// limits.go is the config-bound override seam for the tool surface's
// numeric caps (docs/configuration.md's `tools.*` section). Each field
// mirrors a constant that used to be hardcoded inline; DefaultLimits()
// reproduces every one of those constants exactly, so a session that never
// calls Configure (every test, and any caller that predates this file)
// behaves byte-identically to before. cmd/cortex's NewCortexSession is the
// only production caller, resolving the active values from *Config once at
// session construction — see Config.toolLimits().
//
// A package var (not per-call threading) because these are read-only numeric
// ceilings consulted deep inside free functions (grepFiles, capLine,
// truncateUTF8) that have no ToolDeps parameter to carry an override
// through; unlike the subagent Bounds (session-scoped, threaded via
// RunSubagent's sa parameter — see study.go), there is exactly one process-
// wide value for these at a time, and every production entry point sets it
// once before any tool call. Tests that need a non-default value save/
// restore Limits around the call (see grep_test.go), the same pattern
// transport_test.go already uses for retryBackoff.
type Limits struct {
	CurationBudgetTokens int
	MaxToolOutput        int
	OutlineDefaultBudget int

	DefaultRangeLines int
	MaxRangeLines     int
	MaxReadBytes      int

	GrepMaxHits        int
	GrepLineCap        int
	GrepMaxOutputBytes int

	FetchTimeoutSec   int
	FetchMaxRedirects int
	FetchMaxBodyBytes int

	DefaultSearchMax int
	MaximumSearchMax int
}

// DefaultLimits reproduces today's hardcoded constants exactly.
func DefaultLimits() Limits {
	return Limits{
		CurationBudgetTokens: defaultCurationBudgetTokens,
		MaxToolOutput:        defaultMaxToolOutput,
		OutlineDefaultBudget: defaultOutlineBudget,

		DefaultRangeLines: defaultDefaultRangeLines,
		MaxRangeLines:     defaultMaxRangeLines,
		MaxReadBytes:      defaultMaxReadBytes,

		GrepMaxHits:        defaultGrepMaxHits,
		GrepLineCap:        defaultGrepLineCap,
		GrepMaxOutputBytes: defaultGrepMaxOutputBytes,

		FetchTimeoutSec:   defaultFetchTimeoutSec,
		FetchMaxRedirects: defaultFetchMaxRedirects,
		FetchMaxBodyBytes: defaultFetchMaxBodyBytes,

		DefaultSearchMax: defaultSearchMax,
		MaximumSearchMax: maximumSearchMax,
	}
}

// active is the process-wide effective Limits. Configure overrides it;
// unconfigured, it's DefaultLimits() — today's behavior.
var active = DefaultLimits()

// Configure installs l as the active Limits for every subsequent tool call.
// Any zero field in l falls back to DefaultLimits() for that field, so a
// caller may pass a partially-filled Limits (e.g. from Config.toolLimits(),
// which already resolved zero-fields itself) or a fully-resolved one
// interchangeably.
func Configure(l Limits) {
	def := DefaultLimits()
	active = Limits{
		CurationBudgetTokens: orDefault(l.CurationBudgetTokens, def.CurationBudgetTokens),
		MaxToolOutput:        orDefault(l.MaxToolOutput, def.MaxToolOutput),
		OutlineDefaultBudget: orDefault(l.OutlineDefaultBudget, def.OutlineDefaultBudget),
		DefaultRangeLines:    orDefault(l.DefaultRangeLines, def.DefaultRangeLines),
		MaxRangeLines:        orDefault(l.MaxRangeLines, def.MaxRangeLines),
		MaxReadBytes:         orDefault(l.MaxReadBytes, def.MaxReadBytes),
		GrepMaxHits:          orDefault(l.GrepMaxHits, def.GrepMaxHits),
		GrepLineCap:          orDefault(l.GrepLineCap, def.GrepLineCap),
		GrepMaxOutputBytes:   orDefault(l.GrepMaxOutputBytes, def.GrepMaxOutputBytes),
		FetchTimeoutSec:      orDefault(l.FetchTimeoutSec, def.FetchTimeoutSec),
		FetchMaxRedirects:    orDefault(l.FetchMaxRedirects, def.FetchMaxRedirects),
		FetchMaxBodyBytes:    orDefault(l.FetchMaxBodyBytes, def.FetchMaxBodyBytes),
		DefaultSearchMax:     orDefault(l.DefaultSearchMax, def.DefaultSearchMax),
		MaximumSearchMax:     orDefault(l.MaximumSearchMax, def.MaximumSearchMax),
	}
	fetchHTTPClient = newSafeHTTPClient()
}

// ResetLimits restores DefaultLimits() — test cleanup helper (t.Cleanup(tools.ResetLimits)).
func ResetLimits() {
	active = DefaultLimits()
	fetchHTTPClient = newSafeHTTPClient()
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}
