#!/bin/sh
# Builds SlotDrop.app for macOS 13 or newer, for both Apple Silicon and Intel.
# Needs Go 1.26+ from https://go.dev/dl/ and Apple's command line tools
# (xcode-select --install).
set -eu
cd "$(dirname "$0")"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

export CGO_ENABLED=1 GOOS=darwin MACOSX_DEPLOYMENT_TARGET=13.0
# Wails' file dialogs need this framework. The wails CLI adds it; go build doesn't.
export CGO_LDFLAGS="-framework UniformTypeIdentifiers"
for arch in arm64 amd64; do
	GOARCH=$arch go build -trimpath -tags desktop,production -ldflags "-s -w" -o "$work/SlotDrop-$arch" .
done

app=SlotDrop.app
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
lipo -create -output "$app/Contents/MacOS/SlotDrop" "$work/SlotDrop-arm64" "$work/SlotDrop-amd64"
cp build/darwin/Info.plist "$app/Contents/"

# The icon, from the same picture as the Windows one.
mkdir "$work/SlotDrop.iconset"
for size in 16 32 128 256; do
	sips -z $size $size build/winres/icon.png --out "$work/SlotDrop.iconset/icon_${size}x${size}.png" >/dev/null
	[ $size -lt 256 ] && sips -z $((size * 2)) $((size * 2)) build/winres/icon.png --out "$work/SlotDrop.iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns -o "$app/Contents/Resources/SlotDrop.icns" "$work/SlotDrop.iconset"

# Ad-hoc signature: Apple Silicon won't run an app without one. It isn't an
# Apple Developer ID, so Gatekeeper still asks the first time it's opened.
codesign --force --sign - "$app"
echo "Built $app"
