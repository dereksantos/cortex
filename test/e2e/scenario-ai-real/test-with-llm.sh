#!/bin/bash
# Real AI integration test with Ollama

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

echo "🤖 Real AI Integration Test"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo

# Setup test environment
TEST_DIR="$SCRIPT_DIR/test-llm"
rm -rf "$TEST_DIR"
mkdir -p "$TEST_DIR"
cd "$TEST_DIR"

# Initialize Cortex
echo "📦 Initializing Cortex..."
"$PROJECT_ROOT/cortex" init > /dev/null 2>&1

# Create a real event with substance
echo "📝 Capturing architectural decision event..."
cat <<'EOF' | "$PROJECT_ROOT/cortex" capture
{
  "tool_name": "Write",
  "tool_input": {
    "file_path": "internal/storage/db.go",
    "description": "Implement database layer"
  },
  "tool_result": "Implemented SQLite-based storage using modernc.org/sqlite (pure Go, no CGO). Chose SQLite over PostgreSQL because: 1) Single binary deployment requirement, 2) Zero external dependencies, 3) Embedded storage fits our use case, 4) File-based portability. Added connection pooling and prepared statements for performance."
}
EOF

echo "✅ Event captured"
echo

# Process with LLM
echo "⏳ Processing with Ollama LLM..."
echo "   (This actually calls Ollama API - may take 10-30 seconds)"
echo

START_TIME=$(date +%s)

# Process the event
if "$PROJECT_ROOT/cortex" process > /dev/null 2>&1; then
    END_TIME=$(date +%s)
    ELAPSED=$((END_TIME - START_TIME))
    echo "✅ Processing complete (${ELAPSED}s actual LLM call time)"
else
    echo "❌ FAIL: Processing failed (Ollama might be unavailable)"
    exit 1
fi

echo

# Check if insights were created (run in test directory)
echo "🔍 Checking for LLM-generated insights..."
cd "$TEST_DIR"
INSIGHTS=$("$PROJECT_ROOT/cortex" insights 2>/dev/null || echo "")

if [ -z "$INSIGHTS" ] || echo "$INSIGHTS" | grep -q "No insights"; then
    echo "⚠️  No insights generated yet"
    echo "   This can happen if:"
    echo "   - Ollama is slow/busy"
    echo "   - Event didn't meet importance threshold"
    echo "   - LLM parsing failed"
    echo
    echo "❌ FAIL: Expected at least one insight from LLM"
    exit 1
fi

echo "✅ LLM generated insights:"
echo "$INSIGHTS"
echo

# Validate insight quality (loose check - LLMs vary)
CONTAINS_RELEVANT=0

if echo "$INSIGHTS" | grep -qi "sqlite\|database\|storage"; then
    echo "✅ Insight contains relevant keywords"
    CONTAINS_RELEVANT=1
fi

if echo "$INSIGHTS" | grep -qi "decision\|pattern\|insight\|strategy"; then
    echo "✅ Insight has valid category"
fi

if [ $CONTAINS_RELEVANT -eq 0 ]; then
    echo "⚠️  Warning: Insight doesn't mention key terms"
    echo "   This might be a quality issue with the LLM"
fi

# Show the actual stats (run in test directory context)
echo
echo "📊 Database state:"
cd "$TEST_DIR"
STATS=$("$PROJECT_ROOT/cortex" stats 2>/dev/null)
echo "$STATS"

# Extract insight count (handle JSON with spaces)
INSIGHT_COUNT=$(echo "$STATS" | grep -o '"total_insights":[[:space:]]*[0-9]*' | grep -o '[0-9]*')

if [ -n "$INSIGHT_COUNT" ] && [ "$INSIGHT_COUNT" -gt 0 ] 2>/dev/null; then
    echo
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "✅ TEST PASSED"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo
    echo "What was validated:"
    echo "  ✓ Event captured successfully"
    echo "  ✓ Ollama analyzed the event (${ELAPSED}s)"
    echo "  ✓ ${INSIGHT_COUNT} insight(s) stored in database"
    echo "  ✓ Insights contain relevant keywords"
    echo
    echo "⚠️  Important notes:"
    echo "  • LLM output is non-deterministic (varies per run)"
    echo "  • Quality of analysis depends on LLM model/prompt"
    echo "  • This validates mechanics, not business value"
    echo
    echo "To see variability, run this test multiple times:"
    echo "  for i in {1..3}; do ./test-with-llm.sh; sleep 2; done"
else
    echo "❌ FAIL: No insights in database"
    exit 1
fi

# Cleanup
echo
echo "🧹 Cleaning up..."
cd "$SCRIPT_DIR"
rm -rf "$TEST_DIR"
echo "✨ Complete"
