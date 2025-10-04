#!/bin/bash
# Cleanup script for Scenario 1

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_DIR="$SCRIPT_DIR/test-cortex"

echo "🧹 Cleaning up Scenario 1 test environment..."

if [ -d "$TEST_DIR" ]; then
    rm -rf "$TEST_DIR"
    echo "✅ Test directory removed: $TEST_DIR"
else
    echo "ℹ️  No test directory to clean"
fi

echo "✨ Cleanup complete!"
