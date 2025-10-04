#!/bin/bash
# Setup script for Scenario 1: Database Decision
# This simulates Day 1 where the decision is made and captured

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

echo "🔧 Setting up Scenario 1: Database Decision Recall"
echo

# Initialize test cortex instance
TEST_DIR="$SCRIPT_DIR/test-cortex"
rm -rf "$TEST_DIR"
mkdir -p "$TEST_DIR"

cd "$TEST_DIR"

# Initialize Cortex
echo "📦 Initializing Cortex in test directory..."
"$PROJECT_ROOT/cortex" init

# Simulate Day 1: Database decision event
echo
echo "📝 Day 1: Capturing database decision..."
cat "$SCRIPT_DIR/mock-events/day1-database-decision.json" | \
    "$PROJECT_ROOT/cortex" capture

# Process the event (simulate daemon)
echo "⚙️  Processing events..."
"$PROJECT_ROOT/cortex" process

# Show what was captured
echo
echo "✅ Setup complete! Context captured:"
echo
"$PROJECT_ROOT/cortex" recent 1

echo
echo "📊 Database statistics:"
"$PROJECT_ROOT/cortex" stats

echo
echo "💡 To test retrieval (Day 30 simulation), run: ./test.sh"
