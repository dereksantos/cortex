#!/bin/bash
# Release script for Cortex
# Creates a GitHub release with cross-platform binaries

set -e

if [ -z "$1" ]; then
    echo "Usage: ./scripts/release.sh <version>"
    echo "Example: ./scripts/release.sh 0.1.0"
    exit 1
fi

VERSION="$1"

echo "🚀 Preparing release v${VERSION}..."

# Update version in main.go
sed -i.bak "s/const version = \".*\"/const version = \"${VERSION}\"/" cmd/cortex/main.go
rm cmd/cortex/main.go.bak

# Build for all platforms
export VERSION="${VERSION}"
./scripts/build-all.sh

echo ""
echo "✅ Release artifacts ready in build/"
echo ""
echo "📖 Next steps:"
echo "   1. git add cmd/cortex/main.go"
echo "   2. git commit -m \"chore: bump version to v${VERSION}\""
echo "   3. git tag -a v${VERSION} -m \"Release v${VERSION}\""
echo "   4. git push && git push --tags"
echo "   5. gh release create v${VERSION} ./build/* --title \"v${VERSION}\" --notes \"See CHANGELOG.md\""
echo ""
echo "💡 Update Homebrew formula:"
echo "   - Download tarball from GitHub release"
echo "   - Calculate SHA256: shasum -a 256 cortex-${VERSION}.tar.gz"
echo "   - Update Formula/cortex.rb with new URL and SHA256"
