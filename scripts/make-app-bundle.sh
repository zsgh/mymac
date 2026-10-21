#!/bin/bash
# Package the mymac binary into a macOS app bundle.
#
# The bundle gives mymac a stable identity, which matters for the privacy
# grants: Full Disk Access and the automation permission are attached to the
# program that asks for them, so a terminal and mymac.app are two different
# entries in the system settings.
set -euo pipefail

cd "$(dirname "$0")/.."

BINARY="build/mymac"
BUNDLE="build/mymac.app"
VERSION=$(sed -n 's/.*Version = "\(.*\)".*/\1/p' internal/config/config.go | head -1)

if [ ! -x "$BINARY" ]; then
  echo "build/mymac not found; run 'make build' first" >&2
  exit 1
fi

rm -rf "$BUNDLE"
mkdir -p "$BUNDLE/Contents/MacOS"
mkdir -p "$BUNDLE/Contents/Resources"

cp "$BINARY" "$BUNDLE/Contents/MacOS/mymac"

# The icon.
if [ -f web/assets/icon-512.png ]; then
  ICONSET="$BUNDLE/Contents/Resources/mymac.iconset"
  mkdir -p "$ICONSET"
  for size in 16 32 64 128 256 512; do
    sips -z $size $size web/assets/icon-512.png \
      --out "$ICONSET/icon_${size}x${size}.png" >/dev/null 2>&1 || true
    double=$((size * 2))
    sips -z $double $double web/assets/icon-512.png \
      --out "$ICONSET/icon_${size}x${size}@2x.png" >/dev/null 2>&1 || true
  done
  cp web/assets/icon-512.png "$ICONSET/icon_512x512@2x.png" 2>/dev/null || true
  iconutil -c icns "$ICONSET" -o "$BUNDLE/Contents/Resources/mymac.icns" 2>/dev/null || true
  rm -rf "$ICONSET"
fi

cat > "$BUNDLE/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>mymac</string>
	<key>CFBundleDisplayName</key>
	<string>mymac</string>
	<key>CFBundleIdentifier</key>
	<string>com.mymac.mymac</string>
	<key>CFBundleExecutable</key>
	<string>mymac</string>
	<key>CFBundleIconFile</key>
	<string>mymac</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleShortVersionString</key>
	<string>$VERSION</string>
	<key>CFBundleVersion</key>
	<string>$VERSION</string>
	<key>LSMinimumSystemVersion</key>
	<string>13.0</string>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>LSApplicationCategoryType</key>
	<string>public.app-category.utilities</string>
</dict>
</plist>
PLIST

# A local signature: enough for the privacy grants to stick to the bundle.
if command -v codesign >/dev/null 2>&1; then
  codesign --force --deep --sign - "$BUNDLE" 2>&1 | sed 's/^/  /' || true
fi

echo "built $BUNDLE (v$VERSION)"
echo
echo "Unsigned builds need the quarantine flag removed:"
echo "  xattr -dr com.apple.quarantine \"$BUNDLE\""
