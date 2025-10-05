#!/bin/bash
# Check prerequisites for real AI test

set -e

echo "🔍 Checking prerequisites for AI integration test..."
echo

# Check Ollama
echo "1. Checking Ollama..."
if curl -s http://localhost:11434/api/tags > /dev/null 2>&1; then
    echo "   ✅ Ollama is running"
else
    echo "   ❌ Ollama is not running"
    echo "   Start with: ollama serve"
    exit 1
fi

# Check model
echo "2. Checking for mistral:7b model..."
if curl -s http://localhost:11434/api/tags | grep -q "mistral"; then
    echo "   ✅ Model available"
else
    echo "   ❌ Model not found"
    echo "   Install with: ollama pull mistral:7b"
    exit 1
fi

# Check cortex binary
echo "3. Checking cortex binary..."
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

if [ -f "$PROJECT_ROOT/cortex" ]; then
    echo "   ✅ Cortex binary found"
else
    echo "   ❌ Cortex binary not found"
    echo "   Build with: go build -o cortex ./cmd/cortex"
    exit 1
fi

echo
echo "✅ All prerequisites met!"
echo "   Run: ./test-with-llm.sh"
