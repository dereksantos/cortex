#!/bin/bash
# Build Cortex for multiple platforms

set -e

VERSION="${VERSION:-0.1.0}"
BUILD_DIR="build"

echo "🔨 Building Cortex v${VERSION} for multiple platforms..."

# Clean build directory
rm -rf "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

# Build for each platform
platforms=(
    "darwin/amd64"
    "darwin/arm64"
    "linux/amd64"
    "linux/arm64"
    "windows/amd64"
)

for platform in "${platforms[@]}"; do
    IFS='/' read -r -a array <<< "$platform"
    GOOS="${array[0]}"
    GOARCH="${array[1]}"

    output_name="cortex-${VERSION}-${GOOS}-${GOARCH}"

    if [ "$GOOS" = "windows" ]; then
        output_name="${output_name}.exe"
    fi

    echo "Building $output_name..."

    GOOS=$GOOS GOARCH=$GOARCH go build \
        -ldflags "-X main.version=${VERSION}" \
        -o "$BUILD_DIR/$output_name" \
        ./cmd/cortex

    # Create tarball (except for Windows)
    if [ "$GOOS" != "windows" ]; then
        tar -czf "$BUILD_DIR/${output_name}.tar.gz" -C "$BUILD_DIR" "$output_name"
        rm "$BUILD_DIR/$output_name"
    else
        zip -j "$BUILD_DIR/${output_name%.exe}.zip" "$BUILD_DIR/$output_name"
        rm "$BUILD_DIR/$output_name"
    fi
done

echo ""
echo "✅ Build complete! Binaries in $BUILD_DIR/"
ls -lh "$BUILD_DIR"
