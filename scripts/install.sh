#!/bin/bash
# Install Cortex locally

set -e

echo "📦 Installing Cortex..."

# Build the binary
go build -o cortex ./cmd/cortex

# Detect OS
if [[ "$OSTYPE" == "darwin"* ]]; then
    INSTALL_DIR="/usr/local/bin"
elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
    INSTALL_DIR="$HOME/.local/bin"
else
    echo "❌ Unsupported OS: $OSTYPE"
    exit 1
fi

# Create install directory if it doesn't exist
mkdir -p "$INSTALL_DIR"

# Copy binary
echo "Installing to $INSTALL_DIR/cortex..."
cp cortex "$INSTALL_DIR/cortex"
chmod +x "$INSTALL_DIR/cortex"

echo "✅ Cortex installed successfully!"
echo ""
echo "📖 Next steps:"
echo "   cd your-project"
echo "   cortex init --auto"
echo "   cortex daemon"
