#!/bin/bash
# Run all eval scenarios across all models for Grafana dashboard data
# Usage: nohup ./scripts/run-all-evals.sh > logs/evals-$(date +%Y%m%d-%H%M%S).log 2>&1 &

set -e

# Configuration
CORTEX_BIN="./cortex"
SCENARIOS_DIR="test/evals/v2"
LOG_DIR="logs"
PROVIDER="ollama"

# Models to evaluate (ordered by size)
MODELS=(
    "smollm:360m"
    "qwen2:0.5b"
    "qwen2.5:0.5b"
    "tinyllama"
    "qwen2.5-coder:1.5b"
    "gemma2:2b"
)

# Get all scenarios
SCENARIOS=($(ls ${SCENARIOS_DIR}/*.yaml 2>/dev/null | xargs -n1 basename | sed 's/.yaml$//'))

# Create log directory
mkdir -p "${LOG_DIR}"

# Calculate totals
TOTAL_MODELS=${#MODELS[@]}
TOTAL_SCENARIOS=${#SCENARIOS[@]}
TOTAL_EVALS=$((TOTAL_MODELS * TOTAL_SCENARIOS))
ESTIMATED_MINS=$((TOTAL_EVALS * 3))

# Tracking
PASSED=0
FAILED=0
SKIPPED=0
START_TIME=$(date +%s)

log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1"
}

log "=========================================="
log "Cortex Eval Runner"
log "=========================================="
log "Models: ${TOTAL_MODELS}"
log "Scenarios: ${TOTAL_SCENARIOS}"
log "Total evals: ${TOTAL_EVALS}"
log "Estimated time: ~${ESTIMATED_MINS} minutes (~$((ESTIMATED_MINS / 60)) hours)"
log "Git commit: $(git rev-parse --short HEAD 2>/dev/null || echo 'unknown')"
log "Git branch: $(git branch --show-current 2>/dev/null || echo 'unknown')"
log "=========================================="
echo ""

# Check cortex binary exists
if [[ ! -x "${CORTEX_BIN}" ]]; then
    log "ERROR: ${CORTEX_BIN} not found or not executable"
    log "Run: go build ./cmd/cortex"
    exit 1
fi

# Check ollama is running
if ! ollama list &>/dev/null; then
    log "ERROR: ollama is not running"
    log "Run: ollama serve"
    exit 1
fi

# Progress tracking
CURRENT=0

for model in "${MODELS[@]}"; do
    log "------------------------------------------"
    log "MODEL: ${model}"
    log "------------------------------------------"

    for scenario in "${SCENARIOS[@]}"; do
        CURRENT=$((CURRENT + 1))
        PERCENT=$((CURRENT * 100 / TOTAL_EVALS))
        ELAPSED=$(($(date +%s) - START_TIME))

        if [[ $CURRENT -gt 1 ]]; then
            AVG_SECS=$((ELAPSED / (CURRENT - 1)))
            REMAINING_EVALS=$((TOTAL_EVALS - CURRENT + 1))
            ETA_SECS=$((REMAINING_EVALS * AVG_SECS))
            ETA_MINS=$((ETA_SECS / 60))
            ETA_STR=" | ETA: ${ETA_MINS}m"
        else
            ETA_STR=""
        fi

        log "[${CURRENT}/${TOTAL_EVALS}] (${PERCENT}%)${ETA_STR} ${model} × ${scenario}"

        SCENARIO_FILE="${SCENARIOS_DIR}/${scenario}.yaml"
        EVAL_START=$(date +%s)

        if ${CORTEX_BIN} eval -p ${PROVIDER} -m "${model}" --scenario "${SCENARIO_FILE}" 2>&1; then
            EVAL_END=$(date +%s)
            EVAL_DURATION=$((EVAL_END - EVAL_START))
            log "  ✓ PASSED (${EVAL_DURATION}s)"
            PASSED=$((PASSED + 1))
        else
            EVAL_END=$(date +%s)
            EVAL_DURATION=$((EVAL_END - EVAL_START))
            log "  ✗ FAILED (${EVAL_DURATION}s)"
            FAILED=$((FAILED + 1))
        fi
    done
done

# Summary
END_TIME=$(date +%s)
TOTAL_DURATION=$((END_TIME - START_TIME))
TOTAL_MINS=$((TOTAL_DURATION / 60))
TOTAL_SECS=$((TOTAL_DURATION % 60))

echo ""
log "=========================================="
log "EVAL RUN COMPLETE"
log "=========================================="
log "Passed:  ${PASSED}"
log "Failed:  ${FAILED}"
log "Skipped: ${SKIPPED}"
log "Total:   ${TOTAL_EVALS}"
log "Duration: ${TOTAL_MINS}m ${TOTAL_SECS}s"
log "Database: .context/db/evals_v2.db"
log "=========================================="

# Exit with error if any failed
if [[ ${FAILED} -gt 0 ]]; then
    exit 1
fi
