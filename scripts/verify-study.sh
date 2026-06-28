#!/usr/bin/env bash
#
# verify-study.sh — executable verification contract for the engine-unification
# + study-subagent work (docs/engine-unification.md, docs/study-subagent.md).
#
# It encodes the structural expectations as machine checks: symbols that must be
# GONE, symbols/packages that must EXIST, the study tool surface, the package
# import graph, and (optionally, against a base ref) the net-LOC band and the
# "engine work must not touch navigator.go" scoped-diff rule.
#
# This is a CONTRACT, not a smoke test: before the work is implemented most
# checks FAIL by design and flip to PASS as phases land. Run it after each phase
# to watch the contract go green; wire it into CI once the work is complete.
#
# Usage:
#   scripts/verify-study.sh                 # structural checks (current tree)
#   scripts/verify-study.sh --diff-base REF # also LOC band + scoped-diff vs REF
#
set -uo pipefail
cd "$(dirname "$0")/.."

MOD="github.com/dereksantos/cortex"
PASS=0; FAIL=0; SKIP=0
DIFF_BASE=""
[ "${1:-}" = "--diff-base" ] && DIFF_BASE="${2:-}"

green() { printf '\033[32m%s\033[0m' "$1"; }
red()   { printf '\033[31m%s\033[0m' "$1"; }
yellow(){ printf '\033[33m%s\033[0m' "$1"; }

ok()   { PASS=$((PASS+1)); printf '  %s %s\n' "$(green PASS)" "$1"; }
no()   { FAIL=$((FAIL+1)); printf '  %s %s\n' "$(red FAIL)" "$1"; [ -n "${2:-}" ] && printf '         %s\n' "$2"; }
skip() { SKIP=$((SKIP+1)); printf '  %s %s\n' "$(yellow SKIP)" "$1"; }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$1"; }

# Go source only, excluding vendored/generated.
GO_SRC() { grep -rln --include='*.go' "$@" . 2>/dev/null | grep -v '/vendor/'; }

# --- absent: pattern must NOT appear in any Go file (impl OR test) -------------
absent() { # label  regex
  local hits; hits=$(grep -rEl --include='*.go' "$2" . 2>/dev/null | grep -v '/vendor/')
  if [ -z "$hits" ]; then ok "$1"; else no "$1" "still referenced in: $(echo "$hits" | tr '\n' ' ')"; fi
}
# --- present: pattern MUST appear in non-test Go -------------------------------
present() { # label  regex
  if grep -rEl --include='*.go' "$2" . 2>/dev/null | grep -v '/vendor/' | grep -qv '_test\.go'; then
    ok "$1"; else no "$1" "expected '$2' in non-test Go"; fi
}
file_absent()  { if [ ! -e "$1" ]; then ok "absent: $1"; else no "absent: $1" "still on disk"; fi; }
file_present() { if [ -e "$1" ]; then ok "present: $1"; else no "present: $1" "missing"; fi; }
# --- present_in: pattern MUST appear under a specific path (scoped) ------------
present_in() { # label  path  regex
  if [ -d "$2" ] && grep -rEl --include='*.go' "$3" "$2" 2>/dev/null | grep -qv '_test\.go'; then
    ok "$1"; else no "$1" "expected '$3' under $2"; fi
}
# --- present_test: pattern MUST appear in a _test.go (the eval-first checks) ---
present_test() { # label  regex
  if grep -rEl --include='*_test.go' "$2" . 2>/dev/null | grep -v '/vendor/' | grep -q .; then
    ok "$1"; else no "$1" "expected '$2' in a _test.go"; fi
}

############################################################################
hdr "1. Deletions — the old navigator + project_index must be GONE (→ 0 refs)"
############################################################################
file_absent cmd/loop/navigator.go
file_absent cmd/loop/navigator_test.go
file_absent internal/projectindex
absent "no runNavigator"        '\brunNavigator\b'
absent "no navMap/navTools/navMaxDepth/navAllowed" '\b(navMap|navTools|navMaxDepth|navAllowed)\b'
absent "no navFinalize/navSeed/clampNavRead"       '\b(navFinalize|navSeed|clampNavRead)\b'
absent "no navigatorSystem"     '\bnavigatorSystem\b'
absent "no nav* budget consts"  '\b(navReadBudgetBytes|navReadLines|navMaxIterations|navMapBudget)\b'
absent "no Navigate method"     'func \([^)]*\) Navigate\('
absent "no ProjectIndexTool"    '\bProjectIndexTool\b'
absent "no FunctionProjectIndex" '\bFunctionProjectIndex\b'
absent "no projectindex import" "$MOD/internal/projectindex"

############################################################################
hdr "2. Engine — the one loop + its seams must EXIST"
############################################################################
present "runLoop engine"        'func runLoop\('
present "Sender seam"           'type Sender interface'
present "AgentDispatcher seam"  'type AgentDispatcher interface'
present "Toolset"               'type Toolset struct'
present "Bounds"                'type Bounds struct'
present "Progress sink"         'type Progress func'
present "requestFor builder"    'func requestFor\('
file_present internal/agent     # phase 3: engine moved to its own package
# Eval-first: the behavior-preservation characterization test must exist BEFORE
# the Resolve→Turn fold (it records the coder loop's message/dispatch/stop
# sequence against today's behavior and must stay green through the fold).
present_test "characterization test (locks coder-loop behavior)" 'func TestCoderLoopCharacterization\('

############################################################################
hdr "3. Study primitives — outline / grep / targeted+confined read"
############################################################################
file_present internal/outline
present_in "outline.Outline" internal/outline 'func Outline\('
present_in "outline.Render"  internal/outline 'func Render\('
present_in "outline.Entry"   internal/outline 'type Entry struct'
present "grep tool fn"          'func grepFiles\('
present "FunctionGrep"          '\bFunctionGrep\b'
present "GrepTool decl"         '\bGrepTool\b'
present "maxReadLines (one knob)" '\bmaxReadLines\b'
present "ConfinePath"           'func ConfinePath\('
present "TargetedRead"          'func TargetedRead\('
# the two byte/line constants must be GONE, collapsed into maxReadLines:
absent  "old readWholeFloor gone" '\breadWholeFloor\b'

############################################################################
hdr "4. Subagent profile + study tool surface"
############################################################################
present "Subagent type"         'type Subagent struct'
present "SubAgentRunner"        'type SubAgentRunner interface'
present "RunSubagent"           'func .*RunSubagent\('
present "Study profile var"     'Study = Subagent{'
# Study toolset MUST be exactly {outline, grep, read_file}: no recursion, no bash.
study_tools=$(grep -A4 'Study = Subagent{' cmd/loop/tools/tools.go 2>/dev/null | grep -E 'Tools:')
if echo "$study_tools" | grep -q 'OutlineTool' && echo "$study_tools" | grep -q 'GrepTool' \
   && echo "$study_tools" | grep -q 'ReadFile' && ! echo "$study_tools" | grep -q 'StudyTool' \
   && ! echo "$study_tools" | grep -q 'Bash'; then
  ok "Study.Tools == {outline, grep, read_file} (no study/bash)"
else
  no "Study.Tools == {outline, grep, read_file}" "got: ${study_tools:-<not found>}"
fi
# No search tool anywhere; memory_* surface unchanged.
absent  "no search tool (SearchTool)" '\bSearchTool\b'
present "memory_* surface intact" '\bMemoryWriteTool\b'

############################################################################
hdr "5. Package import graph (architecture invariants)"
############################################################################
import_only() { # label  pkg  allowed-internal-prefix
  if ! go list "$2" >/dev/null 2>&1; then skip "$1 (pkg $2 not built yet)"; return; fi
  local bad; bad=$(go list -deps "$2" 2>/dev/null | grep "^$MOD/" | grep -vE "$3")
  if [ -z "$bad" ]; then ok "$1"; else no "$1" "illegal deps: $(echo "$bad" | tr '\n' ' ')"; fi
}
# internal/agent may only reach pkg/llm (no cortex session / tools-by-name).
import_only "internal/agent → pkg/llm only" "$MOD/internal/agent" "^$MOD/pkg/llm$"
# internal/outline must not import cmd/loop (it's a leaf primitive).
import_only "internal/outline imports no cmd/loop" "$MOD/internal/outline" "^$MOD/(internal/.*|pkg/.*)$"

############################################################################
hdr "6. Build / vet / tests + eval gate (the green-at-every-phase invariant)"
############################################################################
if go build ./... >/dev/null 2>&1; then ok "go build ./..."; else no "go build ./..." "see: go build ./..."; fi
if go vet ./...   >/dev/null 2>&1; then ok "go vet ./...";   else no "go vet ./..."   "see: go vet ./...";   fi
if go test ./...  >/dev/null 2>&1; then ok "go test ./...";  else no "go test ./..."  "see: go test ./...";  fi
# ø is a HARD gate, not a report: study-eval must pin T (every probe passes) and
# exit non-zero otherwise, so an autonomous run reads pass/fail from the exit code.
present "study-eval pins T (all probes pass)" 'passes != total'
# ø must have TEETH: goal-hit alone under-discriminates (flight check 2026-06-28 —
# the old navigator brute-read its way to a pass). Phase 5 must score read volume,
# so a study that over-reads to the answer fails the bounded check. Bind to the
# JSON tag the telemetry emits (not a prose mention) so the check can't pass on a
# comment — it goes green only when the row actually carries the field.
present "study-eval scores reads/bounded (ø discriminates)" 'json:"(read_bytes|bounded)"'

############################################################################
if [ -n "$DIFF_BASE" ]; then
hdr "7. Diff contract vs $DIFF_BASE (LOC band + scoped-diff)"
  # Net source LOC delta should be near-flat: new engine+grep+outline paid for by
  # deleting navigator+projectindex. Wide band catches scope creep / missing deletes.
  delta_src=$(git diff --numstat "$DIFF_BASE"...HEAD -- '*.go' ':(exclude)*_test.go' 2>/dev/null \
              | awk '{a+=$1; d+=$2} END{print a-d+0}')
  if [ -n "$delta_src" ] && [ "$delta_src" -ge -600 ] && [ "$delta_src" -le 600 ]; then
    ok "net source LOC delta in band [-600,600]: $delta_src"
  else
    no "net source LOC delta in band [-600,600]" "got: ${delta_src:-?} (scope creep or missing deletions?)"
  fi
  # Test LOC should grow (new outline/grep/read/engine tests > deleted nav/index tests).
  delta_test=$(git diff --numstat "$DIFF_BASE"...HEAD -- '*_test.go' 2>/dev/null \
               | awk '{a+=$1; d+=$2} END{print a-d+0}')
  if [ -n "$delta_test" ] && [ "$delta_test" -ge 100 ]; then
    ok "net test LOC delta ≥ 100: $delta_test"
  else
    no "net test LOC delta ≥ 100" "got: ${delta_test:-?} (tests should grow)"
  fi
fi

############################################################################
printf '\n\033[1mResult:\033[0m %s passed, %s failed, %s skipped\n' \
  "$(green "$PASS")" "$([ "$FAIL" -gt 0 ] && red "$FAIL" || echo 0)" "$(yellow "$SKIP")"
[ "$FAIL" -eq 0 ]
